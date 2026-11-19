package httpapi

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

// platformOwner: an organisation whose admin is the platform owner (platform@example.com, see TestMain).
func platformOwner(t *testing.T) *org {
	t.Helper()
	o := signup(t, "generic")
	ctx := context.Background()
	if _, err := itOwnerPool.Exec(ctx, `UPDATE users SET email = 'platform@example.com' WHERE tenant_id = $1`, o.tenant); err != nil {
		// already taken by an earlier test: sign in as that owner instead
		c := newClient(t)
		c.ok("POST", "/auth/login", map[string]any{"email": "platform@example.com", "password": "correct-horse-1"}, 200)
		me := c.ok("GET", "/me", nil, 200)
		return &org{c: c, tenant: str(me.body["tenant_id"])}
	}
	c := newClient(t)
	c.ok("POST", "/auth/login", map[string]any{"email": "platform@example.com", "password": "correct-horse-1"}, 200)
	o.c = c
	return o
}

func day(offset int) string {
	n := time.Now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, offset).Format(time.DateOnly)
}

func TestPlatformConsole(t *testing.T) {
	needDB(t)
	p := platformOwner(t)
	a := signup(t, "lebanon")
	if !p.c.ok("GET", "/me", nil, 200).body["is_platform_admin"].(bool) {
		t.Fatal("platform owner not recognised")
	}

	// every /platform route refuses an organisation admin
	param := regexp.MustCompile(`\{[a-z_]+\}`)
	for _, pat := range itAPI.protected {
		method, path, _ := strings.Cut(pat, " ")
		path = strings.TrimPrefix(path, v1)
		if !strings.HasPrefix(path, "/platform/") || path == "/platform/support/end" {
			continue
		}
		path = param.ReplaceAllString(path, a.tenant)
		var body any
		if method != "GET" {
			body = map[string]any{}
		}
		if r := a.c.do(method, path, body); r.status != 403 {
			t.Errorf("%s %s by an organisation admin: %d", method, path, r.status)
		}
	}
	// and the support exit does nothing outside a support view
	if r := a.c.do("POST", "/platform/support/end", map[string]any{}); r.status != 409 {
		t.Fatalf("support end outside a view: %d", r.status)
	}

	find := func() map[string]any {
		for _, x := range p.c.ok("GET", "/platform/tenants", nil, 200).list {
			if str(obj(x)["id"]) == a.tenant {
				return obj(x)
			}
		}
		t.Fatal("organisation not listed")
		return nil
	}
	if s := str(find()["state"]); s != "trial" {
		t.Fatalf("new organisation state %s", s)
	}
	if str(a.c.ok("GET", "/me", nil, 200).body["sub_state"]) != "trial" {
		t.Fatal("me.sub_state not trial")
	}
	p.c.ok("POST", "/platform/tenants/"+a.tenant+"/extend-trial", map[string]any{"days": 10}, 200)

	// renew one month after the trial: starter, paid from today
	r := p.c.ok("POST", "/platform/tenants/"+a.tenant+"/renew", map[string]any{"period": "month", "method": "whish", "reference": "W-1"}, 200)
	to := time.Now().UTC().AddDate(0, 1, -1).Format(time.DateOnly)
	if str(r.body["state"]) != "active" || str(r.body["plan"]) != "starter" || str(r.body["paid_until"]) != to {
		t.Fatalf("after renewal: %s", r.raw)
	}
	eqDec(t, "default price", r.body["price"], "25")
	// a second renewal continues the paid period
	r = p.c.ok("POST", "/platform/tenants/"+a.tenant+"/renew", map[string]any{"period": "year", "amount": "250", "method": "cash"}, 200)
	toT, _ := time.Parse(time.DateOnly, to)
	if want := toT.AddDate(1, 0, 0).Format(time.DateOnly); str(r.body["paid_until"]) != want || str(r.body["billing_cycle"]) != "yearly" {
		t.Fatalf("yearly renewal: paid_until %v want %s", r.body["paid_until"], want)
	}
	if n := len(p.c.ok("GET", "/platform/payments", nil, 200).list); n < 2 {
		t.Fatalf("payments listed: %d", n)
	}

	// grace: past the end date but within the grace days — still works
	p.c.ok("PATCH", "/platform/tenants/"+a.tenant, map[string]any{"paid_until": day(-3), "grace_days": 7}, 200)
	if s := str(find()["state"]); s != "grace" {
		t.Fatalf("state %s, want grace", s)
	}
	a.customer(t, "During grace", "")
	// expired: read-only
	p.c.ok("PATCH", "/platform/tenants/"+a.tenant, map[string]any{"paid_until": day(-10)}, 200)
	me := a.c.ok("GET", "/me", nil, 200)
	if str(me.body["sub_state"]) != "expired" {
		t.Fatalf("me state %v", me.body["sub_state"])
	}
	if r := a.c.do("POST", a.p("/customers"), map[string]any{"name": "Refused"}); r.status != 402 || str(r.body["error"]) != "subscription_expired" {
		t.Fatalf("write while expired: %d %s", r.status, r.raw)
	}
	a.c.ok("GET", a.p("/customers"), nil, 200)
	sub := a.c.ok("GET", "/subscription", nil, 200)
	if str(obj(sub.body["subscription"])["state"]) != "expired" {
		t.Fatalf("my subscription: %s", sub.raw)
	}
	req := a.c.ok("POST", "/subscription/requests", map[string]any{"kind": "renew", "plan": "starter", "cycle": "monthly", "method": "omt", "reference": "OMT-77"}, 201)
	reqs := p.c.ok("GET", "/platform/requests", nil, 200).list
	found := false
	for _, x := range reqs {
		if str(obj(x)["id"]) == str(req.body["id"]) {
			found = true
		}
	}
	if !found {
		t.Fatal("renewal request not in the console")
	}
	r = p.c.ok("POST", "/platform/tenants/"+a.tenant+"/renew", map[string]any{"period": "month", "method": "omt", "reference": "OMT-77",
		"request_id": str(req.body["id"])}, 200)
	if str(r.body["state"]) != "active" || str(r.body["paid_until"]) != to {
		t.Fatalf("renewal of an expired plan starts today: %s", r.raw)
	}
	a.customer(t, "After renewal", "")
	if n := len(p.c.ok("GET", "/platform/requests", nil, 200).list); n != 0 {
		for _, x := range p.c.ok("GET", "/platform/requests", nil, 200).list {
			if str(obj(x)["id"]) == str(req.body["id"]) {
				t.Fatal("request still open after the renewal")
			}
		}
	}

	// block: open sessions end, sign-in says why; unblock gives access back
	email := str(a.c.ok("GET", "/me", nil, 200).body["email"])
	p.c.ok("POST", "/platform/tenants/"+a.tenant+"/block", map[string]any{"reason": "unpaid"}, 200)
	if r := a.c.do("GET", "/me", nil); r.status != 401 {
		t.Fatalf("blocked organisation still signed in: %d", r.status)
	}
	c2 := newClient(t)
	if r := c2.do("POST", "/auth/login", map[string]any{"email": email, "password": "correct-horse-1"}); r.status != 403 ||
		str(r.body["error"]) != "org_blocked" || str(r.body["detail"]) != "unpaid" {
		t.Fatalf("login while blocked: %d %s", r.status, r.raw)
	}
	if r := c2.do("POST", "/auth/login", map[string]any{"email": email, "password": "wrong-password-1"}); r.status != 401 {
		t.Fatalf("wrong password on a blocked organisation must stay generic: %d", r.status)
	}
	p.c.ok("POST", "/platform/tenants/"+a.tenant+"/unblock", map[string]any{}, 200)
	c2.ok("POST", "/auth/login", map[string]any{"email": email, "password": "correct-horse-1"}, 200)
	a.c = c2

	// reminders: once per kind and end date
	p.c.ok("PATCH", "/platform/tenants/"+a.tenant, map[string]any{"paid_until": day(2)}, 200)
	p.c.ok("POST", "/platform/lifecycle/run", map[string]any{}, 200)
	p.c.ok("POST", "/platform/lifecycle/run", map[string]any{}, 200)
	var n int
	if err := itOwnerPool.QueryRow(context.Background(), `SELECT count(*) FROM platform_reminders WHERE tenant_id = $1 AND kind = 'd3'`, a.tenant).Scan(&n); err != nil || n != 1 {
		t.Fatalf("3-day reminder rows: %d %v", n, err)
	}

	// plans: a plan with a user limit
	p.c.ok("POST", "/platform/plans", map[string]any{"code": "gold_" + randHex(2), "name_en": "Gold", "price_monthly": "40", "max_users": 2}, 200)
	plans := p.c.ok("GET", "/platform/plans", nil, 200).list
	gold := ""
	for _, x := range plans {
		if strings.HasPrefix(str(obj(x)["code"]), "gold_") {
			gold = str(obj(x)["code"])
		}
	}
	p.c.ok("PATCH", "/platform/tenants/"+a.tenant, map[string]any{"plan": gold}, 200)
	a.c.ok("POST", "/users", map[string]any{"email": "u" + randHex(4) + "@example.com", "full_name": "Second", "role": "accountant",
		"password": "long-enough-pw-1"}, 201)
	if r := a.c.do("POST", "/users", map[string]any{"email": "u" + randHex(4) + "@example.com", "full_name": "Third", "role": "accountant",
		"password": "long-enough-pw-1"}); r.status != 402 || str(r.body["error"]) != "plan_limit_users" {
		t.Fatalf("third user over the plan limit: %d %s", r.status, r.raw)
	}

	// detail, reset password, disable a user
	d := p.c.ok("GET", "/platform/tenants/"+a.tenant, nil, 200)
	if len(d.body["users"].([]any)) != 2 || len(d.body["payments"].([]any)) < 3 || len(d.body["events"].([]any)) == 0 {
		t.Fatalf("detail: %s", d.raw)
	}
	var second string
	for _, u := range d.body["users"].([]any) {
		if str(obj(u)["email"]) != email {
			second = str(obj(u)["id"])
		}
	}
	p.c.ok("PATCH", "/platform/tenants/"+a.tenant+"/users/"+second, map[string]any{"is_active": false}, 200)
	pw := p.c.ok("POST", "/platform/tenants/"+a.tenant+"/reset-password", map[string]any{}, 200)
	if str(pw.body["email"]) != email || len(str(pw.body["password"])) < 10 {
		t.Fatalf("reset: %s", pw.raw)
	}
	c3 := newClient(t)
	c3.ok("POST", "/auth/login", map[string]any{"email": email, "password": str(pw.body["password"])}, 200)
	a.c = c3

	// support view: read-only, then back to the console
	p.c.ok("POST", "/platform/tenants/"+a.tenant+"/support", map[string]any{}, 200)
	sv := p.c.ok("GET", "/me", nil, 200)
	if str(sv.body["tenant_id"]) != a.tenant || str(obj(sv.body["support"])["by"]) != "platform@example.com" || sv.body["is_platform_admin"].(bool) {
		t.Fatalf("support view me: %s", sv.raw)
	}
	p.c.ok("GET", a.p("/customers"), nil, 200)
	if r := p.c.do("POST", a.p("/customers"), map[string]any{"name": "Support wrote"}); r.status != 403 || str(r.body["error"]) != "support_read_only" {
		t.Fatalf("support view wrote: %d %s", r.status, r.raw)
	}
	if r := p.c.do("GET", "/platform/tenants", nil); r.status != 403 {
		t.Fatalf("console open inside the support view: %d", r.status)
	}
	p.c.ok("POST", "/platform/support/end", map[string]any{}, 200)
	if str(p.c.ok("GET", "/me", nil, 200).body["tenant_id"]) != p.tenant {
		t.Fatal("support end did not go back to the owner")
	}
	sum := p.c.ok("GET", "/platform/summary", nil, 200)
	if dec(t, sum.body["organisations"]).IntPart() < 2 {
		t.Fatalf("summary: %s", sum.raw)
	}
	var events int
	_ = itOwnerPool.QueryRow(context.Background(), `SELECT count(*) FROM platform_events WHERE tenant_id = $1 AND action IN ('support_start', 'support_end')`, a.tenant).Scan(&events)
	if events != 2 {
		t.Fatalf("support view events: %d", events)
	}
}
