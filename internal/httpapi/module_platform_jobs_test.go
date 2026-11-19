package httpapi

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// Background jobs skip expired and blocked organisations (their queued work waits) and pick up again after a renewal.
func TestPlatformJobsSkipInactive(t *testing.T) {
	needDB(t)
	ctx := context.Background()
	p := platformOwner(t)
	a := signup(t, "lebanon")
	tpl := a.c.ok("POST", a.p("/recurring"), map[string]any{"name": "Rent", "kind": "journal", "frequency": "monthly",
		"next_date": day(0), "currency": "USD", "lines": []any{
			map[string]any{"account_id": a.accounts["611"], "debit": "100"},
			map[string]any{"account_id": a.accounts["531"], "credit": "100"}}}, 201)
	id := str(tpl.body["id"])
	runs := func() string {
		for _, x := range a.c.ok("GET", a.p("/recurring"), nil, 200).list {
			if str(obj(x)["id"]) == id {
				return str(obj(x)["runs"])
			}
		}
		t.Fatal("template not listed")
		return ""
	}
	var msg string
	if err := itOwnerPool.QueryRow(ctx, `INSERT INTO messages (tenant_id, company_id, phone, channel, event, body)
		VALUES ($1, $2, '+96170000001', 'whatsapp', 'test', 'held while expired') RETURNING id::text`, a.tenant, a.company).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	msgAttempts := func() (string, int) {
		var st string
		var n int
		if err := itOwnerPool.QueryRow(ctx, `SELECT status, attempts FROM messages WHERE id = $1`, msg).Scan(&st, &n); err != nil {
			t.Fatal(err)
		}
		return st, n
	}
	jobsOK := func() bool {
		var ok bool
		if err := itOwnerPool.QueryRow(ctx, `SELECT tenant_jobs_ok($1)`, a.tenant).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// trial ended: expired — nothing runs for it
	p.c.ok("PATCH", "/platform/tenants/"+a.tenant, map[string]any{"trial_ends_at": day(-2)}, 200)
	if jobsOK() {
		t.Fatal("expired organisation still runs jobs")
	}
	if _, err := itAPI.RunDueRecurring(ctx); err != nil {
		t.Fatal(err)
	}
	if r := runs(); r != "0" {
		t.Fatalf("recurring entry posted for an expired organisation: runs %s", r)
	}
	for i := 0; i < 5; i++ {
		if _, err := itAPI.sendBatch(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if st, n := msgAttempts(); st != "queued" || n != 0 {
		t.Fatalf("message of an expired organisation was sent: %s, %d attempts", st, n)
	}
	res, err := itAPI.RunPlatformLifecycle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res["held_companies"] < 1 {
		t.Fatalf("held jobs not reported: %v", res)
	}
	var owner bool
	if err := itOwnerPool.QueryRow(ctx, `SELECT platform_owner FROM tenants WHERE id = $1`, p.tenant).Scan(&owner); err != nil || !owner {
		t.Fatalf("platform owner organisation not flagged: %v %v", owner, err)
	}
	var paused int
	_ = itOwnerPool.QueryRow(ctx, `SELECT count(*) FROM platform_events WHERE tenant_id = $1 AND action = 'state' AND detail ->> 'jobs' = 'paused'`,
		a.tenant).Scan(&paused)
	if paused != 1 {
		t.Fatalf("jobs paused event: %d", paused)
	}

	// renewed: the due entry posts and the message goes out
	p.c.ok("POST", "/platform/tenants/"+a.tenant+"/renew", map[string]any{"period": "month", "method": "cash"}, 200)
	if !jobsOK() {
		t.Fatal("renewed organisation still skipped")
	}
	if _, err := itAPI.RunDueRecurring(ctx); err != nil {
		t.Fatal(err)
	}
	if r := runs(); r != "1" {
		t.Fatalf("recurring entry after renewal: runs %s", r)
	}
	for i := 0; i < 40; i++ {
		if st, n := msgAttempts(); st != "queued" || n > 0 {
			break
		}
		if _, err := itAPI.sendBatch(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if st, n := msgAttempts(); st == "queued" && n == 0 {
		t.Fatal("message still held after the renewal")
	}

	// blocked: skipped again
	p.c.ok("POST", "/platform/tenants/"+a.tenant+"/block", map[string]any{"reason": "unpaid"}, 200)
	if jobsOK() {
		t.Fatal("blocked organisation still runs jobs")
	}
	p.c.ok("POST", "/platform/tenants/"+a.tenant+"/unblock", map[string]any{}, 200)
	if !jobsOK() {
		t.Fatal("unblocked organisation still skipped")
	}
}

// A plan's app list is enforced; apps already in use when the list narrows are kept (grandfathered).
func TestPlanAppsEnforced(t *testing.T) {
	needDB(t)
	p := platformOwner(t)
	a := signup(t, "lebanon")
	a.c.ok("PUT", a.p("/apps"), map[string]any{"apps": []string{"msg", "pos"}}, 200)
	a.c.ok("GET", a.p("/pos/registers"), nil, 200)

	code := "basic_" + randHex(3)
	p.c.ok("POST", "/platform/plans", map[string]any{"code": code, "name_en": "Basic", "price_monthly": "10", "apps": []string{"msg"}}, 200)
	// moved to a plan without the till: its routes stop, with a code the screens explain
	p.c.ok("PATCH", "/platform/tenants/"+a.tenant, map[string]any{"plan": code}, 200)
	me := a.c.ok("GET", "/me", nil, 200)
	if pa, _ := me.body["plan_apps"].([]any); len(pa) != 1 || str(pa[0]) != "msg" {
		t.Fatalf("me.plan_apps: %v", me.body["plan_apps"])
	}
	if r := a.c.do("GET", a.p("/pos/registers"), nil); r.status != 403 || str(r.body["error"]) != "app_not_in_plan" {
		t.Fatalf("till outside the plan: %d %s", r.status, r.raw)
	}
	// a new app outside the plan cannot be switched on; an app already on may stay in the list
	if r := a.c.do("PUT", a.p("/apps"), map[string]any{"apps": []string{"msg", "pos", "hr"}}); r.status != 403 ||
		str(r.body["error"]) != "app_not_in_plan" || str(r.body["detail"]) != "hr" {
		t.Fatalf("switch on an app outside the plan: %d %s", r.status, r.raw)
	}
	a.c.ok("PUT", a.p("/apps"), map[string]any{"apps": []string{"msg", "pos"}}, 200)
	// core apps always work
	a.c.ok("GET", a.p("/customers"), nil, 200)

	// the owner narrows the plan's list: organisations keep the apps they already use
	p.c.ok("PATCH", "/platform/plans/"+code, map[string]any{"apps": []string{"msg", "crm"}}, 200)
	d := p.c.ok("GET", "/platform/tenants/"+a.tenant, nil, 200)
	gf, _ := obj(d.body["tenant"])["grandfathered_apps"].([]any)
	if len(gf) != 1 || str(gf[0]) != "pos" {
		t.Fatalf("grandfathered apps: %v", obj(d.body["tenant"])["grandfathered_apps"])
	}
	a.c.ok("GET", a.p("/pos/registers"), nil, 200)
	pa := []string{}
	for _, x := range a.c.ok("GET", "/me", nil, 200).body["plan_apps"].([]any) {
		pa = append(pa, str(x))
	}
	if !slices.Contains(pa, "pos") || !slices.Contains(pa, "crm") {
		t.Fatalf("plan apps with grandfathered: %v", pa)
	}
	// the owner removes the grandfathered apps
	p.c.ok("PATCH", "/platform/tenants/"+a.tenant, map[string]any{"grandfathered_apps": []string{}}, 200)
	if r := a.c.do("GET", a.p("/pos/registers"), nil); r.status != 403 || str(r.body["error"]) != "app_not_in_plan" {
		t.Fatalf("till after removing grandfathered apps: %d %s", r.status, r.raw)
	}
	if r := p.c.do("PATCH", "/platform/plans/"+code, map[string]any{"apps": []string{"bogus"}}); r.status != 400 {
		t.Fatalf("unknown app in a plan: %d", r.status)
	}

	// setup: the console shows the owner's messaging providers and the payment contact
	s := p.c.ok("GET", "/platform/setup", nil, 200)
	if _, ok := s.body["providers"].([]any); !ok || !strings.Contains(s.raw, "sales_phone") {
		t.Fatalf("setup: %s", s.raw)
	}
	if r := a.c.do("GET", "/platform/setup", nil); r.status != 403 {
		t.Fatalf("setup by an organisation admin: %d", r.status)
	}
}
