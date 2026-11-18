package httpapi

import (
	"context"
	"strings"
	"testing"

	pxcrypto "github.com/mmdelhajj/paxve/internal/crypto"
	"github.com/mmdelhajj/paxve/internal/routeros/fakerouter"
)

// Module K integration tests: router secrets encrypted, suspend rule, reconnect after payment,
// router worker retries, SSRF guard.

func ispSetup(t *testing.T) {
	t.Helper()
	box, err := pxcrypto.NewBox(strings.Repeat("7a", 32))
	if err != nil {
		t.Fatal(err)
	}
	itAPI.SetSecretBox(box)
	itAPI.AllowPrivateRouters = true
}

func TestRouterSecretsAndConnection(t *testing.T) {
	needDB(t)
	ispSetup(t)
	fr, err := fakerouter.Start("api", "s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()
	o := signup(t, "generic")
	rt := o.c.ok("POST", o.p("/routers"), map[string]any{"name": "Core", "host": "127.0.0.1", "port": fr.Port(),
		"username": "api", "password": "s3cret-pass"}, 201)
	id := str(rt.body["id"])
	if strings.Contains(rt.raw, "s3cret") || rt.body["has_password"] != true {
		t.Fatalf("password leaked or missing: %s", rt.raw)
	}
	var stored string
	if err := itOwnerPool.QueryRow(context.Background(), `SELECT password_enc FROM routers WHERE id = $1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !pxcrypto.IsSealed(stored) || strings.Contains(stored, "s3cret") {
		t.Fatalf("router password not encrypted at rest: %q", stored)
	}
	if l := o.c.ok("GET", o.p("/routers"), nil, 200); strings.Contains(l.raw, "s3cret") || strings.Contains(l.raw, "enc:v1") {
		t.Fatalf("list leaks secret: %s", l.raw)
	}
	test := o.c.ok("POST", o.p("/routers/"+id+"/test"), map[string]any{}, 200)
	if test.body["ok"] != true || str(test.body["identity"]) != "FakeRouter" {
		t.Fatalf("test connection: %s", test.raw)
	}
	// keeping the password when the form sends it back masked
	o.c.ok("PATCH", o.p("/routers/"+id), map[string]any{"password": "••••", "name": "Core 1"}, 200)
	if r := o.c.ok("POST", o.p("/routers/"+id+"/test"), map[string]any{}, 200); r.body["ok"] != true {
		t.Fatalf("password lost after masked update: %s", r.raw)
	}
	// wrong password gives a clean error
	o.c.ok("PATCH", o.p("/routers/"+id), map[string]any{"password": "wrong"}, 200)
	if r := o.c.ok("POST", o.p("/routers/"+id+"/test"), map[string]any{}, 200); r.body["ok"] != false || !strings.Contains(str(r.body["error"]), "login failed") {
		t.Fatalf("wrong password: %s", r.raw)
	}
	// SSRF guard: internal addresses refused when not explicitly allowed
	itAPI.AllowPrivateRouters = false
	r := o.c.ok("POST", o.p("/routers/"+id+"/test"), map[string]any{}, 200)
	itAPI.AllowPrivateRouters = true
	if r.body["ok"] != false || !strings.Contains(str(r.body["error"]), "blocked") {
		t.Fatalf("private address not blocked: %s", r.raw)
	}
	// other tenant cannot see the router
	other := signup(t, "generic")
	if l := other.c.do("GET", o.p("/routers"), nil); l.status == 200 && len(l.list) != 0 {
		t.Fatalf("other tenant sees routers")
	}
}

func TestISPSuspendAndReconnect(t *testing.T) {
	needDB(t)
	ispSetup(t)
	ctx := context.Background()
	fr, err := fakerouter.Start("api", "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()
	fr.AddSecret("ali")
	fr.AddSecret("rima")

	o := signup(t, "lebanon")
	router := str(o.c.ok("POST", o.p("/routers"), map[string]any{"name": "R1", "host": "127.0.0.1", "port": fr.Port(),
		"username": "api", "password": "pw"}, 201).body["id"])
	plan := o.plan(t, "Internet 20M", "30")
	late, ok := o.customer(t, "Ali Late", ""), o.customer(t, "Rima OnTime", "")
	subLate := str(o.c.ok("POST", o.p("/customers/"+late+"/subscriptions"), map[string]any{"plan_id": plan, "start_date": "2026-01-01",
		"router_id": router, "network_username": "ali", "service_type": "pppoe"}, 201).body["id"])
	subOK := str(o.c.ok("POST", o.p("/customers/"+ok+"/subscriptions"), map[string]any{"plan_id": plan, "start_date": "2026-01-01",
		"router_id": router, "network_username": "rima"}, 201).body["id"])
	if r := o.c.do("POST", o.p("/customers/"+ok+"/subscriptions"), map[string]any{"plan_id": plan, "router_id": router, "network_username": "rima"}); r.status != 409 {
		t.Fatalf("duplicate username on same router accepted: %d", r.status)
	}
	// Ali has a bill overdue since January; Rima's bill is not due yet
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": late, "currency": "USD", "issue_date": "2026-01-01", "due_date": "2026-01-10",
		"lines": []any{map[string]any{"description": "Internet Jan", "quantity": "1", "unit_price": "30"}}}, 201)
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": ok, "currency": "USD", "issue_date": today(), "due_date": "2099-01-01",
		"lines": []any{map[string]any{"description": "Internet", "quantity": "1", "unit_price": "30"}}}, 201)
	o.c.ok("PUT", o.p("/isp/settings"), map[string]any{"auto_suspend_enabled": true, "suspend_after_days": 5, "reconnect_on_payment": true}, 200)

	// the worker path (all tenants) suspends exactly Ali
	if _, err := itAPI.RunISPRules(ctx); err != nil {
		t.Fatal(err)
	}
	subs := func(cust string) map[string]any {
		d := o.c.ok("GET", o.p("/customers/"+cust), nil, 200)
		return obj(d.body["subscriptions"].([]any)[0])
	}
	if s := subs(late); str(s["status"]) != "suspended" || str(s["suspended_reason"]) != "overdue" {
		t.Fatalf("late subscription not suspended: %v", s)
	}
	if s := subs(ok); str(s["status"]) != "active" {
		t.Fatalf("on-time subscription suspended: %v", s)
	}
	if n := o.c.ok("POST", o.p("/isp/run"), map[string]any{}, 200); str(n.body["suspended"]) != "0" {
		t.Fatalf("second run suspended again: %v", n.body)
	}
	for i := 0; i < 3; i++ {
		if _, err := itAPI.ProcessRouterActions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if d, _ := fr.Disabled("ali"); !d || fr.ActiveSessions("ali") != 0 {
		t.Fatalf("ali not disabled on router (sessions %d)", fr.ActiveSessions("ali"))
	}
	if d, _ := fr.Disabled("rima"); d {
		t.Fatal("rima disabled")
	}
	if s := subs(late); str(s["network_status"]) != "disabled" {
		t.Fatalf("network_status = %v", s["network_status"])
	}

	// paying the overdue bill reconnects automatically
	total := str(obj(inv.body["invoice"])["total"])
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": late, "currency": "USD",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": total}}}, 201)
	if s := subs(late); str(s["status"]) != "active" {
		t.Fatalf("not reactivated after payment: %v", s)
	}
	for i := 0; i < 3; i++ {
		if _, err := itAPI.ProcessRouterActions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if d, _ := fr.Disabled("ali"); d {
		t.Fatal("ali still disabled after payment")
	}
	acts := o.c.ok("GET", o.p("/router-actions?subscription_id="+subLate), nil, 200).list
	if len(acts) != 2 || str(obj(acts[0])["action"]) != "enable" || str(obj(acts[0])["status"]) != "sent" {
		t.Fatalf("actions log: %v", acts)
	}

	// an unreachable router: the action fails gracefully and is retried later
	dead := str(o.c.ok("POST", o.p("/routers"), map[string]any{"name": "Dead", "host": "127.0.0.1", "port": 1, "username": "x", "password": "y"}, 201).body["id"])
	o.c.ok("PATCH", o.p("/subscriptions/"+subOK), map[string]any{"router_id": dead}, 200)
	o.c.ok("POST", o.p("/subscriptions/"+subOK+"/network"), map[string]any{"action": "disable"}, 200)
	if _, err := itAPI.ProcessRouterActions(ctx); err != nil {
		t.Fatal(err)
	}
	a := obj(o.c.ok("GET", o.p("/router-actions?subscription_id="+subOK), nil, 200).list[0])
	if str(a["status"]) != "queued" || str(a["attempts"]) != "1" || str(a["error"]) == "" {
		t.Fatalf("failed action not scheduled for retry: %v", a)
	}
	if r := o.c.do("POST", o.p("/subscriptions/"+subOK+"/network"), map[string]any{"action": "explode"}); r.status != 400 {
		t.Fatalf("bad action accepted: %d", r.status)
	}
	o.trialBalanced(t)
}
