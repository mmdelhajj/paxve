package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmdelhajj/paxve/internal/auth"
)

func TestEveryRouteHasPermission(t *testing.T) {
	s := New(nil, nil, nil, "", nil)
	func() { // Handler() needs no database; a nil session manager may panic at the very end
		defer func() { _ = recover() }()
		_ = s.Handler()
	}()
	if len(s.protected) < 200 {
		t.Fatalf("only %d protected routes recorded", len(s.protected))
	}
	for _, p := range s.protected {
		if p == "/" {
			continue
		}
		perm := permForPattern(p)
		if perm == "" {
			t.Errorf("%s: no permission declared in routePerms", p)
			continue
		}
		if perm != permAny && !validPerm(perm) {
			t.Errorf("%s: unknown permission %q", p, perm)
		}
	}
	for _, list := range presets {
		for _, p := range list {
			if !validPerm(p) {
				t.Errorf("preset has unknown permission %q", p)
			}
		}
	}
}

// login signs a new client in as email/password (no two-factor).
func loginAs(t *testing.T, email, pw string) *client {
	t.Helper()
	c := newClient(t)
	c.ok("POST", "/auth/login", map[string]any{"email": email, "password": pw}, 200)
	return c
}

func TestCustomRolesAndCompanyAccess(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")

	// a "Clerk" may only see and add customers
	role := o.c.ok("POST", "/roles", map[string]any{"name": "Clerk", "base": "data_entry",
		"permissions": []string{"customers.view", "customers.create"}}, 201)
	roleID := str(role.body["id"])
	email := "clerk" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": email, "full_name": "Clerk", "role_id": roleID, "locale": "en", "password": "clerk-secret-9"}, 201)
	c := loginAs(t, email, "clerk-secret-9")
	me := c.ok("GET", "/me", nil, 200)
	if str(me.body["role_name"]) != "Clerk" || len(me.body["permissions"].([]any)) != 2 {
		t.Fatalf("me: %s", me.raw)
	}
	c.ok("POST", o.p("/customers"), map[string]any{"name": "By clerk"}, 201)
	if r := c.do("GET", o.p("/accounts"), nil); r.status != 403 {
		t.Fatalf("clerk read accounts: %d", r.status)
	}
	if r := c.do("GET", "/users", nil); r.status != 403 {
		t.Fatalf("clerk listed users: %d", r.status)
	}

	// the role is edited: the clerk gains journal drafts at the next request
	o.c.ok("PATCH", "/roles/"+roleID, map[string]any{"permissions": []string{"customers.view", "customers.create", "accounting.view"}}, 200)
	c.ok("GET", o.p("/accounts"), nil, 200)
	// a deactivated role grants nothing
	o.c.ok("PATCH", "/roles/"+roleID, map[string]any{"is_active": false}, 200)
	if r := c.do("GET", o.p("/customers"), nil); r.status != 403 {
		t.Fatalf("deactivated role still works: %d", r.status)
	}

	// a manager who may manage users cannot hand out more than they have
	mgr := o.c.ok("POST", "/roles", map[string]any{"name": "HR", "base": "read_only",
		"permissions": []string{"users.view", "users.create", "users.edit", "customers.view"}}, 201)
	mEmail := "hr" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": mEmail, "full_name": "HR", "role_id": str(mgr.body["id"]), "password": "hr-secret-pass-9"}, 201)
	h := loginAs(t, mEmail, "hr-secret-pass-9")
	if r := h.do("POST", "/users", map[string]any{"email": "x" + randHex(4) + "@example.com", "full_name": "X", "role": "admin", "password": "another-pass-9"}); r.status != 403 || str(r.body["error"]) != "cannot_grant" {
		t.Fatalf("escalation to admin: %d %s", r.status, r.raw)
	}
	if r := h.do("POST", "/roles", map[string]any{"name": "Boss", "base": "admin", "permissions": []string{"users.edit", "settings.manage"}}); r.status != 403 {
		t.Fatalf("role with more rights: %d %s", r.status, r.raw)
	}
	var ownerID string
	for _, u := range o.c.ok("GET", "/users", nil, 200).list {
		if str(obj(u)["role"]) == "admin" {
			ownerID = str(obj(u)["id"])
		}
	}
	if r := h.do("POST", "/users/"+ownerID+"/reset-link", map[string]any{}); r.status != 403 {
		t.Fatalf("reset link for the owner by HR: %d", r.status)
	}

	// company access: a second company; the clerk is limited to the first
	second := str(o.c.ok("POST", "/companies", map[string]any{"name_en": "Second Co " + randHex(3), "no_chart": true}, 201).body["id"])
	o.c.ok("PATCH", "/roles/"+roleID, map[string]any{"is_active": true}, 200)
	var clerkID string
	for _, u := range o.c.ok("GET", "/users", nil, 200).list {
		if str(obj(u)["email"]) == email {
			clerkID = str(obj(u)["id"])
		}
	}
	o.c.ok("PATCH", "/users/"+clerkID, map[string]any{"company_ids": []string{o.company}}, 200)
	if l := c.ok("GET", "/companies", nil, 200).list; len(l) != 1 || str(obj(l[0])["id"]) != o.company {
		t.Fatalf("companies visible to clerk: %v", l)
	}
	if r := c.do("GET", "/companies/"+second+"/customers", nil); r.status != 404 {
		t.Fatalf("clerk opened another company: %d", r.status)
	}
	c.ok("GET", o.p("/customers"), nil, 200)
	// no company list is accepted from a user who is limited himself... (owner is not limited)
	o.c.ok("PATCH", "/users/"+clerkID, map[string]any{"all_companies": true}, 200)
	c.ok("GET", "/companies/"+second+"/customers", nil, 200)
}

func TestTwoFactorSessionsAndReset(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	me := o.c.ok("GET", "/me", nil, 200)
	email := str(me.body["email"])

	// set up two-factor
	setup := o.c.ok("POST", "/me/mfa/setup", map[string]any{}, 200)
	secret := str(setup.body["secret"])
	if !strings.HasPrefix(str(setup.body["otpauth_uri"]), "otpauth://totp/") {
		t.Fatalf("uri: %s", setup.raw)
	}
	if r := o.c.do("POST", "/me/mfa/enable", map[string]any{"code": "000000"}); r.status != 400 {
		t.Fatalf("wrong code accepted: %d", r.status)
	}
	en := o.c.ok("POST", "/me/mfa/enable", map[string]any{"code": auth.TOTPCodeAt(secret, time.Now())}, 200)
	codes := en.body["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Fatalf("recovery codes: %v", codes)
	}

	// sign-in now needs the second step
	c := newClient(t)
	r := c.ok("POST", "/auth/login", map[string]any{"email": email, "password": "correct-horse-1"}, 200)
	if r.body["mfa_required"] != true {
		t.Fatalf("login without mfa step: %s", r.raw)
	}
	if r := c.do("GET", "/me", nil); r.status != 401 {
		t.Fatalf("half signed-in session can read: %d", r.status)
	}
	if r := c.do("POST", "/auth/mfa", map[string]any{"code": "123456"}); r.status != 401 {
		t.Fatalf("bad code: %d", r.status)
	}
	// the code used to enable is spent; the next time step is accepted
	c.ok("POST", "/auth/mfa", map[string]any{"code": auth.TOTPCodeAt(secret, time.Now().Add(30*time.Second))}, 200)
	c.ok("GET", "/me", nil, 200)

	// a recovery code works once
	c2 := newClient(t)
	c2.ok("POST", "/auth/login", map[string]any{"email": email, "password": "correct-horse-1"}, 200)
	c2.ok("POST", "/auth/mfa", map[string]any{"recovery_code": str(codes[0])}, 200)
	c3 := newClient(t)
	c3.ok("POST", "/auth/login", map[string]any{"email": email, "password": "correct-horse-1"}, 200)
	if r := c3.do("POST", "/auth/mfa", map[string]any{"recovery_code": str(codes[0])}); r.status != 401 {
		t.Fatalf("recovery code reused: %d", r.status)
	}

	// sessions: the owner sees several and ends the others
	list := o.c.ok("GET", "/me/sessions", nil, 200).list
	if len(list) < 3 {
		t.Fatalf("sessions: %d", len(list))
	}
	o.c.ok("POST", "/me/sessions/revoke-others", map[string]any{}, 200)
	if r := c.do("GET", "/me", nil); r.status != 401 {
		t.Fatalf("revoked session still works: %d", r.status)
	}
	o.c.ok("GET", "/me", nil, 200)

	// organisation requires two-factor: a user without it may only set it up
	o.c.ok("PUT", "/security/settings", map[string]any{"require_mfa": true}, 200)
	uEmail := "staff" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": uEmail, "full_name": "Staff", "role": "accountant", "password": "staff-pass-word-1"}, 201)
	s := loginAs(t, uEmail, "staff-pass-word-1")
	if m := s.ok("GET", "/me", nil, 200); m.body["mfa_setup_required"] != true {
		t.Fatalf("setup not required: %s", m.raw)
	}
	if r := s.do("GET", o.p("/customers"), nil); r.status != 403 || str(r.body["error"]) != "mfa_setup_required" {
		t.Fatalf("user without mfa used the app: %d %s", r.status, r.raw)
	}
	o.c.ok("PUT", "/security/settings", map[string]any{"require_mfa": false}, 200)

	// password policy and reset link
	var staffID string
	for _, u := range o.c.ok("GET", "/users", nil, 200).list {
		if str(obj(u)["email"]) == uEmail {
			staffID = str(obj(u)["id"])
		}
	}
	if r := o.c.do("PATCH", "/users/"+staffID, map[string]any{"password": "password123"}); r.status != 400 || str(r.body["error"]) != "common_password" {
		t.Fatalf("common password: %d %s", r.status, r.raw)
	}
	link := str(o.c.ok("POST", "/users/"+staffID+"/reset-link", map[string]any{}, 201).body["url"])
	tok := link[strings.LastIndex(link, "/")+1:]
	anon := newClient(t)
	anon.ok("GET", "/auth/reset/"+tok, nil, 200)
	if r := anon.do("POST", "/auth/reset", map[string]any{"token": tok, "password": "short"}); r.status != 400 {
		t.Fatalf("weak reset password: %d", r.status)
	}
	anon.ok("POST", "/auth/reset", map[string]any{"token": tok, "password": "brand-new-pass-77"}, 200)
	if r := anon.do("POST", "/auth/reset", map[string]any{"token": tok, "password": "brand-new-pass-78"}); r.status != 404 {
		t.Fatalf("reset link reused: %d", r.status)
	}
	if r := s.do("GET", "/me", nil); r.status != 401 {
		t.Fatalf("session survived a password reset: %d", r.status)
	}
	loginAs(t, uEmail, "brand-new-pass-77")
	// forgot-password never says whether the address exists
	anon.ok("POST", "/auth/forgot", map[string]any{"email": "nobody-" + randHex(3) + "@example.com"}, 200)
}

func TestApprovalsAndScopedKeys(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Approve Me", "")
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-06",
		"lines": []any{map[string]any{"description": "Work", "quantity": "1", "unit_price": "100"}}}, 201)
	invID := str(obj(inv.body["invoice"])["id"])
	if invID == "" {
		invID = str(inv.body["id"])
	}

	o.c.ok("PUT", o.p("/approval-rules"), map[string]any{"void": true, "credit_note": true, "payment_over": "500"}, 200)
	aEmail := "acc" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": aEmail, "full_name": "Accountant", "role": "accountant", "password": "acct-pass-word-1"}, 201)
	a := loginAs(t, aEmail, "acct-pass-word-1")

	// the accountant's void waits for approval
	r := a.ok("POST", o.p("/invoices/"+invID+"/void"), map[string]any{"reason": "typo"}, 202)
	reqID := str(r.body["approval_id"])
	if st := str(obj(o.c.ok("GET", o.p("/invoices/"+invID), nil, 200).body["invoice"])["status"]); st == "void" {
		t.Fatal("voided before approval")
	}
	if r := a.do("POST", o.p("/approvals/"+reqID+"/approve"), map[string]any{}); r.status != 403 {
		t.Fatalf("accountant approved: %d", r.status)
	}
	if l := a.ok("GET", o.p("/approvals?status=pending"), nil, 200).list; len(l) != 1 {
		t.Fatalf("requester sees %d requests", len(l))
	}
	res := o.c.ok("POST", o.p("/approvals/"+reqID+"/approve"), map[string]any{"note": "ok"}, 200)
	if str(res.body["status"]) != "approved" {
		t.Fatalf("approve: %s", res.raw)
	}
	if st := str(obj(o.c.ok("GET", o.p("/invoices/"+invID), nil, 200).body["invoice"])["status"]); st != "void" {
		t.Fatalf("invoice status after approval: %s", st)
	}
	if r := o.c.do("POST", o.p("/approvals/"+reqID+"/approve"), map[string]any{}); r.status != 409 {
		t.Fatalf("approved twice: %d", r.status)
	}
	// the admin (holds billing.approve) acts directly
	inv2 := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-06",
		"lines": []any{map[string]any{"description": "Work", "quantity": "1", "unit_price": "10"}}}, 201)
	id2 := str(obj(inv2.body["invoice"])["id"])
	o.c.ok("POST", o.p("/invoices/"+id2+"/void"), map[string]any{"reason": "x"}, 200)
	// a request can be rejected
	inv3 := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-06",
		"lines": []any{map[string]any{"description": "Work", "quantity": "1", "unit_price": "10"}}}, 201)
	id3 := str(obj(inv3.body["invoice"])["id"])
	rq := str(a.ok("POST", o.p("/invoices/"+id3+"/void"), map[string]any{"reason": "y"}, 202).body["approval_id"])
	o.c.ok("POST", o.p("/approvals/"+rq+"/reject"), map[string]any{"note": "no"}, 200)
	o.trialBalanced(t)

	// API key limited to reading customers
	key := str(o.c.ok("POST", "/api-keys", map[string]any{"name": "CRM sync", "role": "accountant", "scopes": []string{"customers.view"}}, 201).body["key"])
	call := func(path string) int {
		req, _ := http.NewRequest("GET", itServer.URL+"/api/v1"+path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := call(o.p("/customers")); c != 200 {
		t.Fatalf("scoped key customers: %d", c)
	}
	if c := call(o.p("/accounts")); c != 403 {
		t.Fatalf("scoped key accounts: %d", c)
	}
	if r := o.c.do("POST", "/api-keys", map[string]any{"name": "bad", "role": "read_only", "scopes": []string{"billing.void"}}); r.status != 400 {
		t.Fatalf("scope beyond role accepted: %d", r.status)
	}
}

func TestConsolidatedReport(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	second := str(o.c.ok("POST", "/companies", map[string]any{"name_en": "Branch " + randHex(3), "base_currency": "USD", "local_currency": "LBP", "no_chart": true}, 201).body["id"])
	o.c.do("POST", "/companies/"+second+"/accounts/load-template", map[string]any{"template": "lebanon"})
	accs := map[string]string{}
	for _, a := range o.c.ok("GET", "/companies/"+second+"/accounts", nil, 200).list {
		accs[str(obj(a)["code"])] = str(obj(a)["id"])
	}
	post := func(cid, cash, rev, amt string) {
		o.c.ok("POST", "/companies/"+cid+"/journal", map[string]any{"date": "2026-05-01", "post": true, "lines": []any{
			map[string]any{"account_id": cash, "debit": amt}, map[string]any{"account_id": rev, "credit": amt}}}, 201)
	}
	post(o.company, o.accounts["531"], o.accounts["701"], "100")
	if accs["531"] != "" {
		post(second, accs["531"], accs["701"], "50")
	}
	tb := o.c.ok("GET", "/reports/consolidated?report=trial-balance&from=2026-01-01&to=2026-12-31&currency=USD", nil, 200)
	tot := obj(tb.body["totals"])
	if !dec(t, tot["debit"]).Equal(dec(t, tot["credit"])) {
		t.Fatalf("consolidated not balanced: %v", tot)
	}
	pl := o.c.ok("GET", "/reports/consolidated?report=profit-loss&from=2026-01-01&to=2026-12-31", nil, 200)
	want := "100"
	if accs["531"] != "" {
		want = "150"
	}
	eqDec(t, "consolidated income", obj(pl.body["totals"])["income"], want)
	bs := o.c.ok("GET", "/reports/consolidated?report=balance-sheet&to=2026-12-31", nil, 200)
	bt := obj(bs.body["totals"])
	if !dec(t, bt["assets"]).Equal(dec(t, bt["liabilities_equity"])) {
		t.Fatalf("consolidated balance sheet: %v", bt)
	}
}

func TestWebhooksSignedAndRetried(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	var mu sync.Mutex
	var got []*http.Request
	var bodies [][]byte
	fails := 1
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		got, bodies = append(got, r), append(bodies, b)
		if fails > 0 {
			fails--
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	}))
	defer recv.Close()

	if r := o.c.do("POST", o.p("/webhooks"), map[string]any{"url": "ftp://x", "events": []string{"customer.created"}}); r.status != 400 {
		t.Fatalf("bad url accepted: %d", r.status)
	}
	h := o.c.ok("POST", o.p("/webhooks"), map[string]any{"url": recv.URL + "/hook", "events": []string{"customer.created", "invoice.created"}}, 201)
	secret, hookID := str(h.body["secret"]), str(h.body["id"])
	o.customer(t, "Hooked", "")

	ctx := context.Background()
	// the server refuses private addresses unless allowed
	if _, err := itAPI.sendWebhookBatch(ctx, webhookClient(false)); err != nil {
		t.Fatal(err)
	}
	ds := o.c.ok("GET", o.p("/webhooks/"+hookID+"/deliveries"), nil, 200).list
	if len(ds) != 1 || str(obj(ds[0])["status"]) != "pending" || !strings.Contains(str(obj(ds[0])["last_error"]), "not allowed") {
		t.Fatalf("private target: %v", ds)
	}
	// retry now against the test receiver: first answer 500 (retried later), then 204
	o.c.do("POST", o.p("/webhook-deliveries/"+str(obj(ds[0])["id"])+"/retry"), map[string]any{})
	itAPI.OwnerPool.Exec(ctx, `UPDATE webhook_deliveries SET status = 'pending', next_attempt_at = now() WHERE webhook_id = $1`, hookID)
	if _, err := itAPI.sendWebhookBatch(ctx, webhookClient(true)); err != nil {
		t.Fatal(err)
	}
	itAPI.OwnerPool.Exec(ctx, `UPDATE webhook_deliveries SET next_attempt_at = now() WHERE webhook_id = $1 AND status = 'pending'`, hookID)
	if _, err := itAPI.sendWebhookBatch(ctx, webhookClient(true)); err != nil {
		t.Fatal(err)
	}
	ds = o.c.ok("GET", o.p("/webhooks/"+hookID+"/deliveries"), nil, 200).list
	if str(obj(ds[0])["status"]) != "sent" {
		t.Fatalf("delivery: %v", ds[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("receiver got %d calls", len(got))
	}
	last, body := got[1], bodies[1]
	if last.Header.Get("X-Paxve-Event") != "customer.created" || !strings.Contains(string(body), `"Hooked"`) || strings.Contains(string(body), "portal_token") {
		t.Fatalf("payload: %s %s", last.Header.Get("X-Paxve-Event"), body)
	}
	sig := last.Header.Get("X-Paxve-Signature")
	ts, v1, _ := strings.Cut(strings.TrimPrefix(sig, "t="), ",v1=")
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	if hex.EncodeToString(m.Sum(nil)) != v1 {
		t.Fatalf("bad signature %s", sig)
	}
}
