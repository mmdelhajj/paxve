package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Phase 8: branches (default branch on documents, branch-limited users, branch P&L), inter-company (mirrored bill,
// loan between the companies, eliminations in the consolidated balance sheet and P&L, void follows), document
// layouts, dashboards (own, role default, data with permissions), sign-in hardening (complexity, expiry, history,
// idle logout, Google sign-in through a fake provider) and purchase tenders / blanket agreements.
func TestEnterprisePhase8(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	ctx := context.Background()
	admin := str(o.c.ok("GET", "/me", nil, 200).body["email"])

	// ======================= branches =======================
	b1 := o.c.ok("POST", o.p("/branches"), map[string]any{"code": "TYR", "name": "Tyre", "phone": "07 123"}, 201).body
	b2 := o.c.ok("POST", o.p("/branches"), map[string]any{"code": "SDN", "name": "Saida"}, 201).body
	if r := o.c.do("POST", o.p("/branches"), map[string]any{"code": "TYR", "name": "Again"}); r.status != 409 {
		t.Errorf("duplicate branch code: %d", r.status)
	}
	if str(b1["cost_center_code"]) != "BR-TYR" {
		t.Errorf("branch cost center: %v", b1)
	}
	clerk := "clerk" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": clerk, "full_name": "Branch Clerk", "role": "accountant", "password": "Clerk-pass-123", "locale": "en"}, 201)
	var clerkID string
	for _, u := range o.c.ok("GET", o.p("/branches/users"), nil, 200).list {
		if str(obj(u)["email"]) == clerk {
			clerkID = str(obj(u)["id"])
		}
	}
	if clerkID == "" {
		t.Fatal("clerk not in the branch users")
	}
	o.c.ok("PUT", o.p("/branches/users/"+clerkID), map[string]any{"branch_ids": []string{str(b1["id"])}}, 200)
	if r := o.c.do("PUT", o.p("/branches/users/"+clerkID), map[string]any{"branch_ids": []string{o.accounts["531"]}}); r.status != 400 {
		t.Errorf("a non-branch id accepted: %d", r.status)
	}
	cl := loginAs(t, clerk, "Clerk-pass-123")
	cust := o.customer(t, "Branch customer", "")
	clInv := str(obj(cl.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-10",
		"lines": []any{map[string]any{"description": "Tyre sale", "quantity": "1", "unit_price": "30"}}}, 201).body["invoice"])["id"])
	adInv := str(obj(o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-10",
		"lines": []any{map[string]any{"description": "Head office sale", "quantity": "1", "unit_price": "70"}}}, 201).body["invoice"])["id"])
	if got := ownerQuery(t, `SELECT coalesce(branch_id::text, '') FROM invoices WHERE id = $1`, clInv); got != str(b1["id"]) {
		t.Errorf("clerk invoice branch %q, want Tyre", got)
	}
	if got := ownerQuery(t, `SELECT coalesce(branch_id::text, '') FROM invoices WHERE id = $1`, adInv); got != "" {
		t.Errorf("admin invoice got a branch %q", got)
	}
	if got := ownerQuery(t, `SELECT count(*)::text FROM journal_lines l JOIN invoices i ON i.journal_entry_id = l.entry_id
		WHERE i.id = $1 AND l.cost_center_id = $2`, clInv, str(b1["cost_center_id"])); got == "0" {
		t.Error("the clerk's revenue line has no branch cost center")
	}
	cl.ok("GET", o.p("/invoices/"+adInv), nil, 200) // company-wide: visible
	o.c.ok("PUT", o.p("/branches/assign"), map[string]any{"doc": "invoice", "id": adInv, "branch_id": str(b2["id"])}, 200)
	if r := cl.do("GET", o.p("/invoices/"+adInv), nil); r.status != 404 {
		t.Errorf("a Tyre clerk opens a Saida invoice: %d", r.status)
	}
	rep := o.c.ok("GET", o.p("/branches/report?from=2026-11-01&to=2026-11-30"), nil, 200).body
	for _, row := range rep["rows"].([]any) {
		r := obj(row)
		switch str(r["code"]) {
		case "TYR":
			eqDec(t, "Tyre income", r["income"], "30")
		case "SDN":
			eqDec(t, "Saida income", r["income"], "70")
		}
	}
	o.trialBalanced(t)

	// ======================= inter-company =======================
	bc := o.c.ok("POST", "/companies", map[string]any{"name_en": "Sister Co " + randHex(3), "base_currency": "USD", "local_currency": "LBP"}, 201).body
	bID := str(bc["id"])
	bAcc := map[string]string{}
	for _, a := range o.c.ok("GET", "/companies/"+bID+"/accounts", nil, 200).list {
		bAcc[str(obj(a)["code"])] = str(obj(a)["id"])
	}
	ownerQuery(t, `UPDATE companies SET default_expense_account_id = $2 WHERE id = $1 RETURNING 'ok'`, bID, bAcc["611"])
	link := o.c.ok("POST", "/intercompany/links", map[string]any{"company_a": o.company, "company_b": bID}, 201).body
	if r := o.c.do("POST", "/intercompany/links", map[string]any{"company_a": bID, "company_b": o.company}); r.status != 409 {
		t.Errorf("second link for the same pair: %d", r.status)
	}
	icInv := str(obj(o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": str(link["a_customer_id"]), "currency": "USD", "issue_date": "2026-11-12",
		"lines": []any{map[string]any{"description": "Management fee", "quantity": "2", "unit_price": "50"}}}, 201).body["invoice"])["id"])
	sync := o.c.ok("POST", "/intercompany/sync", map[string]any{}, 200).body
	if dec(t, sync["created"]).IntPart() != 1 {
		t.Fatalf("sync: %v %v", sync, o.c.ok("GET", "/intercompany/docs", nil, 200).raw)
	}
	billTotal := ownerQuery(t, `SELECT b.total::text || '|' || b.supplier_id::text FROM ic_docs d JOIN bills b ON b.id = d.dst_bill WHERE d.src_invoice = $1`, icInv)
	if !strings.HasPrefix(billTotal, "100.0000|") || !strings.HasSuffix(billTotal, str(link["b_supplier_id"])) {
		t.Errorf("mirrored bill %q", billTotal)
	}
	if n := dec(t, o.c.ok("POST", "/intercompany/sync", map[string]any{}, 200).body["created"]).IntPart(); n != 0 {
		t.Errorf("second sync made %d more bills", n)
	}
	tr := o.c.ok("POST", "/intercompany/transfers", map[string]any{"link_id": str(link["id"]), "from_company": o.company, "kind": "loan",
		"date": "2026-11-13", "currency": "USD", "amount": "500", "from_account_id": o.accounts["531"], "to_account_id": bAcc["531"]}, 201).body
	if r := o.c.do("POST", "/intercompany/transfers", map[string]any{"link_id": str(link["id"]), "from_company": o.company, "kind": "gift",
		"currency": "USD", "amount": "5", "from_account_id": o.accounts["531"], "to_account_id": bAcc["531"]}); r.status != 400 {
		t.Errorf("unknown transfer kind: %d", r.status)
	}
	q := "&companies=" + o.company + "," + bID + "&currency=USD&from=2026-01-01&to=2026-12-31&eliminate=1"
	bs := o.c.ok("GET", "/reports/consolidated?report=balance-sheet"+q, nil, 200).body
	after := obj(bs["totals_after"])
	eqDec(t, "consolidated BS balances after eliminations", after["assets"], str(after["liabilities_equity"]))
	before := obj(bs["totals"])
	eqDec(t, "assets eliminated (loan 500 + open invoice 100)", dec(t, before["assets"]).Sub(dec(t, after["assets"])).String(), "600")
	for _, e := range bs["eliminations"].([]any) {
		if str(obj(e)["code"]) == "IC-DIFF" {
			t.Errorf("an inter-company difference with matching sides: %v", e)
		}
	}
	pl := o.c.ok("GET", "/reports/consolidated?report=profit-loss"+q, nil, 200).body
	plA, plB := obj(pl["totals"]), obj(pl["totals_after"])
	eqDec(t, "income eliminated", dec(t, plA["income"]).Sub(dec(t, plB["income"])).String(), "100")
	eqDec(t, "net unchanged by eliminations", plA["net"], str(plB["net"]))
	o.c.ok("POST", "/intercompany/transfers/"+str(tr["id"])+"/void", map[string]any{}, 200)
	if r := o.c.do("POST", "/intercompany/transfers/"+str(tr["id"])+"/void", map[string]any{}); r.status != 409 {
		t.Errorf("void twice: %d", r.status)
	}
	for _, l := range o.c.ok("GET", "/intercompany/links", nil, 200).list {
		if str(obj(l)["id"]) == str(link["id"]) {
			eqDec(t, "A current account after the void", obj(l)["a_balance"], "0")
			eqDec(t, "B current account after the void", obj(l)["b_balance"], "0")
		}
	}
	o.c.ok("POST", o.p("/invoices/"+icInv+"/void"), map[string]any{"reason": "wrong"}, 200)
	if v := dec(t, o.c.ok("POST", "/intercompany/sync", map[string]any{}, 200).body["voided"]).IntPart(); v != 1 {
		t.Errorf("voided sale not followed: %d", v)
	}
	if st := ownerQuery(t, `SELECT b.status || '|' || d.status FROM ic_docs d JOIN bills b ON b.id = d.dst_bill WHERE d.src_invoice = $1`, icInv); st != "void|source_void" {
		t.Errorf("mirrored bill after the sale was voided: %s", st)
	}
	o.trialBalanced(t)
	(&org{c: o.c, company: bID}).trialBalanced(t)
	// a user limited to company A does not see the link
	lim := "lim" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": lim, "full_name": "Only A", "role": "accountant", "password": "Only-a-pass-123", "company_ids": []string{o.company}}, 201)
	lc := loginAs(t, lim, "Only-a-pass-123")
	if n := len(lc.ok("GET", "/intercompany/links", nil, 200).list); n != 0 {
		t.Errorf("a user of company A only sees %d links", n)
	}
	if r := lc.do("POST", "/intercompany/transfers", map[string]any{"link_id": str(link["id"]), "from_company": o.company, "kind": "loan",
		"currency": "USD", "amount": "5", "from_account_id": o.accounts["531"], "to_account_id": bAcc["531"]}); r.status != 404 {
		t.Errorf("a user of company A only moves money to B: %d", r.status)
	}

	// ======================= layouts =======================
	o.c.ok("PUT", o.p("/doc-layouts/invoice"), map[string]any{"accent": "#123456", "hide": []string{"customer_phone"}, "qr": "doc", "bilingual": "en",
		"order": []string{"header", "lines", "customer", "totals", "footer"}}, 200)
	lay := o.c.ok("GET", o.p("/doc-layouts"), nil, 200).body
	if str(obj(lay["invoice"])["accent"]) != "#123456" || len(obj(lay["receipt"])) != 0 {
		t.Errorf("layouts: %v", lay)
	}
	for _, bad := range []map[string]any{{"accent": "red"}, {"hide": []string{"password"}}, {"paper": "a0"}, {"bilingual": "xx"}} {
		if r := o.c.do("PUT", o.p("/doc-layouts/invoice"), bad); r.status != 400 {
			t.Errorf("bad layout %v accepted: %d", bad, r.status)
		}
	}
	if r := o.c.do("PUT", o.p("/doc-layouts/passport"), map[string]any{}); r.status != 400 {
		t.Errorf("unknown document type: %d", r.status)
	}

	// ======================= dashboards =======================
	if src := str(o.c.ok("GET", o.p("/dashboards/mine"), nil, 200).body["source"]); src != "builtin" {
		t.Errorf("first dashboard source %q", src)
	}
	o.c.ok("PUT", o.p("/dashboards/roles/accountant"), map[string]any{"widgets": []any{map[string]any{"id": "a", "metric": "receivables", "size": 1}}}, 200)
	if src := str(cl.ok("GET", o.p("/dashboards/mine"), nil, 200).body["source"]); src != "role" {
		t.Errorf("accountant dashboard source %q", src)
	}
	o.c.ok("PUT", o.p("/dashboards/mine"), map[string]any{"widgets": []any{map[string]any{"id": "a", "metric": "revenue_month", "size": 1},
		map[string]any{"id": "b", "metric": "revenue_12m", "size": 2}, map[string]any{"id": "c", "metric": "top_debtors", "size": 2}}}, 200)
	if src := str(o.c.ok("GET", o.p("/dashboards/mine"), nil, 200).body["source"]); src != "user" {
		t.Errorf("own dashboard source %q", src)
	}
	if r := o.c.do("PUT", o.p("/dashboards/mine"), map[string]any{"widgets": []any{map[string]any{"id": "a", "metric": "salaries_of_ceo", "size": 1}}}); r.status != 400 {
		t.Errorf("unknown metric saved: %d", r.status)
	}
	data := o.c.ok("POST", o.p("/dashboards/data"), map[string]any{"metrics": []string{"revenue_month", "revenue_12m", "top_debtors", "cash", "pending_approvals"}}, 200).body
	if len(obj(obj(data["revenue_12m"])["data"])["series"].([]any)) != 12 {
		t.Errorf("12 months chart: %v", data["revenue_12m"])
	}
	for _, k := range []string{"revenue_month", "cash", "pending_approvals", "top_debtors"} {
		if obj(data[k])["data"] == nil {
			t.Errorf("metric %s: %v", k, data[k])
		}
	}
	// a collector does not get the money metrics they may not see
	col := "col" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": col, "full_name": "Col", "role": "collector", "password": "Collect-pass-123"}, 201)
	cc := loginAs(t, col, "Collect-pass-123")
	cd := cc.ok("POST", o.p("/dashboards/data"), map[string]any{"metrics": []string{"payables", "collections_today"}}, 200).body
	if obj(cd["payables"])["denied"] != true {
		t.Errorf("collector sees payables: %v", cd["payables"])
	}

	// ======================= sign-in =======================
	o.c.ok("PUT", "/security/settings", map[string]any{"password_complexity": true}, 200)
	if r := cl.do("PATCH", "/me", map[string]any{"current_password": "Clerk-pass-123", "new_password": "onlylowercaseletters"}); r.status != 400 || !strings.Contains(r.raw, "weak_password_complexity") {
		t.Errorf("simple password accepted with complexity on: %d %s", r.status, r.raw)
	}
	o.c.ok("PUT", "/security/settings", map[string]any{"password_expiry_days": 30}, 200)
	ownerQuery(t, `UPDATE users SET password_changed_at = now() - interval '40 days' WHERE email = $1 RETURNING 'ok'`, clerk)
	if me := cl.ok("GET", "/me", nil, 200).body; me["password_expired"] != true {
		t.Errorf("expired password not flagged: %v", me["password_expired"])
	}
	if r := cl.do("GET", o.p("/invoices"), nil); r.status != 403 || !strings.Contains(r.raw, "password_expired") {
		t.Errorf("expired password still works: %d", r.status)
	}
	cl.ok("PATCH", "/me", map[string]any{"current_password": "Clerk-pass-123", "new_password": "New-Clerk-pass-456"}, 200)
	cl.ok("GET", o.p("/invoices"), nil, 200)
	if r := newClient(t).do("POST", "/auth/login", map[string]any{"email": clerk, "password": "wrong-one-99"}); r.status != 401 {
		t.Errorf("wrong password: %d", r.status)
	}
	failed := o.c.ok("GET", "/security/login-history?failed=1", nil, 200).list
	if len(failed) == 0 || str(obj(failed[0])["email"]) != clerk || str(obj(failed[0])["reason"]) != "wrong_password" {
		t.Errorf("failed sign-in not in the history: %v", failed)
	}
	if len(cl.ok("GET", "/me/login-history", nil, 200).list) == 0 {
		t.Error("own sign-in history empty")
	}
	o.c.ok("PUT", o.p("/idle-logout"), map[string]any{"minutes": 15}, 200)
	if r := o.c.do("PUT", o.p("/idle-logout"), map[string]any{"minutes": 2}); r.status != 400 {
		t.Errorf("2 minutes idle accepted: %d", r.status)
	}
	if n := dec(t, cl.ok("GET", "/me", nil, 200).body["idle_logout_min"]).IntPart(); n != 15 {
		t.Errorf("idle minutes in /me: %d", n)
	}
	ownerQuery(t, `UPDATE user_sessions s SET last_seen_at = now() - interval '30 minutes' FROM users u WHERE u.id = s.user_id AND u.email = $1
		AND s.revoked_at IS NULL RETURNING 'ok'`, clerk)
	if r := cl.do("GET", "/me", nil); r.status != 401 {
		t.Errorf("idle session still open: %d", r.status)
	}
	o.c.ok("PUT", o.p("/idle-logout"), map[string]any{"minutes": 0}, 200)

	// Google sign-in through a fake provider
	var nonce string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			nonce = r.URL.Query().Get("nonce")
			if r.URL.Query().Get("code_challenge_method") != "S256" {
				http.Error(w, "pkce", 400)
				return
			}
			http.Redirect(w, r, itServer.URL+v1+"/auth/oidc/google/callback?code=good&state="+url.QueryEscape(r.URL.Query().Get("state")), http.StatusFound)
		case "/token":
			_ = r.ParseForm()
			if r.PostForm.Get("code") != "good" || r.PostForm.Get("code_verifier") == "" {
				http.Error(w, "bad", 400)
				return
			}
			email := admin
			if strings.Contains(r.UserAgent(), "unknown") {
				email = "nobody@example.com"
			}
			claims, _ := json.Marshal(map[string]any{"iss": "https://accounts.google.com", "aud": "cid-test", "exp": time.Now().Add(time.Hour).Unix(),
				"nonce": nonce, "email": email, "email_verified": true})
			tok := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
			_ = json.NewEncoder(w).Encode(map[string]string{"id_token": tok})
		}
	}))
	defer fake.Close()
	oidcOverride = func(m map[string]oidcProvider) {
		m["google"] = oidcProvider{Key: "google", AuthURL: fake.URL + "/authorize", TokenURL: fake.URL + "/token", ClientID: "cid-test", Secret: "x",
			issuerOK: func(iss string) bool { return iss == "https://accounts.google.com" }}
	}
	defer func() { oidcOverride = nil }()
	if prov := newClient(t).ok("GET", "/auth/oidc/providers", nil, 200).list; len(prov) != 1 || str(prov[0]) != "google" {
		t.Errorf("providers: %v", prov)
	}
	sso := newClient(t)
	sso.hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !strings.HasPrefix(req.URL.String(), itServer.URL) && !strings.HasPrefix(req.URL.String(), fake.URL) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	req, _ := http.NewRequest("GET", itServer.URL+v1+"/auth/oidc/google/start", nil)
	req.Header.Set("X-Client-IP", sso.ip)
	res, err := sso.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if loc := res.Header.Get("Location"); !strings.HasSuffix(loc, "/#/dashboard") {
		t.Fatalf("Google sign-in ended at %q (%d)", loc, res.StatusCode)
	}
	if me := sso.ok("GET", "/me", nil, 200).body; str(me["email"]) != admin {
		t.Errorf("signed in as %v", me["email"])
	}
	// a callback without the matching state is refused
	bad := newClient(t)
	bad.hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ = http.NewRequest("GET", itServer.URL+v1+"/auth/oidc/google/callback?code=good&state=forged", nil)
	req.Header.Set("X-Client-IP", bad.ip)
	if res, err := bad.hc.Do(req); err != nil || !strings.Contains(res.Header.Get("Location"), "sso_error=sso_state") {
		t.Errorf("forged state: %v %v", err, res.Header.Get("Location"))
	}
	if r := bad.do("GET", "/me", nil); r.status != 401 {
		t.Errorf("forged callback signed in: %d", r.status)
	}
	// the organisation can switch it off
	o.c.ok("PUT", "/security/settings", map[string]any{"sso_allowed": false}, 200)
	off := newClient(t)
	off.hc.CheckRedirect = sso.hc.CheckRedirect
	req, _ = http.NewRequest("GET", itServer.URL+v1+"/auth/oidc/google/start", nil)
	req.Header.Set("X-Client-IP", off.ip)
	if res, err := off.hc.Do(req); err != nil || !strings.Contains(res.Header.Get("Location"), "sso_not_allowed") {
		t.Errorf("sso off: %v %v", err, res.Header.Get("Location"))
	}
	o.c.ok("PUT", "/security/settings", map[string]any{"sso_allowed": true, "password_expiry_days": 0, "password_complexity": false}, 200)

	// ======================= tenders and agreements =======================
	s1 := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Supplier One", "currency": "USD"}, 201).body["id"])
	s2 := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Supplier Two", "currency": "USD"}, 201).body["id"])
	td := o.c.ok("POST", o.p("/purchasing/tenders"), map[string]any{"title": "Cables 2027", "currency": "USD", "deadline": "2026-12-15",
		"lines": []any{map[string]any{"description": "Cable 10mm", "quantity": "10"}, map[string]any{"description": "Breaker 32A", "quantity": "5"}}}, 201).body
	tid := str(obj(td["tender"])["id"])
	tl := td["lines"].([]any)
	l1, l2 := str(obj(tl[0])["id"]), str(obj(tl[1])["id"])
	o.c.ok("POST", o.p("/purchasing/tenders/"+tid+"/status"), map[string]any{"status": "open"}, 200)
	o.c.ok("PUT", o.p("/purchasing/tenders/"+tid+"/bids"), map[string]any{"supplier_id": s1, "lead_days": 5,
		"lines": []any{map[string]any{"tender_line_id": l1, "unit_price": "2"}, map[string]any{"tender_line_id": l2, "unit_price": "5"}}}, 200)
	cmp := o.c.ok("PUT", o.p("/purchasing/tenders/"+tid+"/bids"), map[string]any{"supplier_id": s2, "lead_days": 9,
		"lines": []any{map[string]any{"tender_line_id": l1, "unit_price": "1.8"}, map[string]any{"tender_line_id": l2, "unit_price": "6"}}}, 200).body
	var bid1, bid2 string
	for _, b := range cmp["bids"].([]any) {
		if str(obj(b)["supplier_id"]) == s1 {
			bid1 = str(obj(b)["id"])
			eqDec(t, "bid 1 total", obj(b)["total"], "45")
		} else {
			bid2 = str(obj(b)["id"])
			eqDec(t, "bid 2 total", obj(b)["total"], "48")
		}
	}
	for _, l := range cmp["lines"].([]any) {
		if str(obj(l)["id"]) == l1 && str(obj(l)["best_bid_id"]) != bid2 {
			t.Errorf("best bid of line 1: %v", l)
		}
	}
	if r := o.c.do("PUT", o.p("/purchasing/tenders/"+tid), map[string]any{"lines": []any{map[string]any{"description": "x", "quantity": "1"}}}); r.status != 409 {
		t.Errorf("lines changed after bids: %d", r.status)
	}
	aw := o.c.ok("POST", o.p("/purchasing/tenders/"+tid+"/award"), map[string]any{"lines": []any{
		map[string]any{"tender_line_id": l1, "bid_id": bid2}, map[string]any{"tender_line_id": l2, "bid_id": bid1}}}, 200).body
	if len(aw["made"].([]any)) != 2 {
		t.Fatalf("award made %v", aw["made"])
	}
	tot := ownerQuery(t, `SELECT string_agg(total::text || ':' || status, ',' ORDER BY total) FROM purchase_orders WHERE tender_id = $1`, tid)
	if tot != "18.0000:confirmed,25.0000:confirmed" {
		t.Errorf("award orders %s", tot)
	}
	if r := o.c.do("PUT", o.p("/purchasing/tenders/"+tid+"/bids"), map[string]any{"supplier_id": s1, "lines": []any{}}); r.status != 409 {
		t.Errorf("bid after award: %d", r.status)
	}
	ag := o.c.ok("POST", o.p("/purchasing/agreements"), map[string]any{"supplier_id": s1, "title": "Diesel 2026-27", "currency": "USD",
		"start_date": "2026-01-01", "end_date": "2027-12-31", "lines": []any{map[string]any{"description": "Diesel (L)", "unit_price": "1.5", "quantity": "100"}}}, 201).body
	aid := str(obj(ag["agreement"])["id"])
	al := str(obj(ag["lines"].([]any)[0])["id"])
	if r := o.c.do("POST", o.p("/purchasing/agreements/"+aid+"/order"), map[string]any{"lines": []any{map[string]any{"agreement_line_id": al, "quantity": "1"}}}); r.status != 409 {
		t.Errorf("order on a draft agreement: %d", r.status)
	}
	o.c.ok("POST", o.p("/purchasing/agreements/"+aid+"/status"), map[string]any{"status": "active"}, 200)
	ord := o.c.ok("POST", o.p("/purchasing/agreements/"+aid+"/order"), map[string]any{"order_date": "2026-11-15",
		"lines": []any{map[string]any{"agreement_line_id": al, "quantity": "40"}}}, 201).body
	eqDec(t, "remaining after 40", obj(obj(ord["agreement"])["lines"].([]any)[0])["remaining"], "60")
	if r := o.c.do("POST", o.p("/purchasing/agreements/"+aid+"/order"), map[string]any{"order_date": "2026-11-16",
		"lines": []any{map[string]any{"agreement_line_id": al, "quantity": "70"}}}); r.status != 409 || !strings.Contains(r.raw, "agreement_exceeded") {
		t.Errorf("over the agreement: %d %s", r.status, r.raw)
	}
	if v := ownerQuery(t, `SELECT total::text FROM purchase_orders WHERE id = $1`, str(ord["order_id"])); v != "60.0000" {
		t.Errorf("agreement order total %s", v)
	}
	if r := o.c.do("POST", o.p("/purchasing/agreements/"+aid+"/status"), map[string]any{"status": "cancelled"}); r.status != 409 {
		t.Errorf("cancel a used agreement: %d", r.status)
	}
	_ = ctx
	o.trialBalanced(t)
}
