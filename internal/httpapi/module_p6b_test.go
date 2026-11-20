package httpapi

import (
	"testing"
	"time"
)

// Phase 6b: the public marketing site (plans, demo requests → platform leads, honeypot), the owner app
// (/owner/today), the customer app (portal: consumption, tickets, orders), payroll posting-date option,
// supplier opening balances import (one opening bill per row, trial balance balanced), the smart alert for catalog
// prices lowered by hand at the till, the device time of offline payments (off-hours check) and collectors kept
// to their own cash boxes.
func TestPhase6b(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	usd := o.methods["Cash USD"]

	// ---------- public site ----------
	pub := newClient(t)
	site := pub.ok("GET", "/public/site", nil, 200).body
	if len(site["plans"].([]any)) == 0 {
		t.Fatalf("public plans: %v", site)
	}
	for _, p := range site["plans"].([]any) {
		if str(obj(p)["code"]) == "trial" {
			t.Errorf("the hidden trial plan is shown: %v", p)
		}
	}
	lead := "Lead " + randHex(4)
	pub.ok("POST", "/public/demo-request", map[string]any{"name": lead, "phone": "70123456", "business": "pharmacy", "city": "Tyre", "lang": "ar"}, 200)
	pub.ok("POST", "/public/demo-request", map[string]any{"name": "Bot " + lead, "phone": "70123456", "website": "http://spam"}, 200)
	if r := pub.do("POST", "/public/demo-request", map[string]any{"name": "No contact"}); r.status != 400 {
		t.Errorf("lead without phone or e-mail: %d", r.status)
	}
	if r := pub.do("POST", "/public/demo-request", map[string]any{"name": "x", "phone": "70123456", "business": "casino"}); r.status != 400 {
		t.Errorf("unknown business type: %d", r.status)
	}
	if r := o.c.do("GET", "/platform/leads", nil); r.status != 403 {
		t.Errorf("an organisation admin reads leads: %d", r.status)
	}
	p := platformOwner(t)
	var leadID string
	for _, l := range p.c.ok("GET", "/platform/leads?status=new", nil, 200).list {
		switch str(obj(l)["name"]) {
		case lead:
			leadID = str(obj(l)["id"])
			if str(obj(l)["business"]) != "pharmacy" || str(obj(l)["city"]) != "Tyre" {
				t.Errorf("lead fields: %v", l)
			}
		case "Bot " + lead:
			t.Errorf("the honeypot request was stored")
		}
	}
	if leadID == "" {
		t.Fatal("demo request not in the platform leads")
	}
	up := p.c.ok("PATCH", "/platform/leads/"+leadID, map[string]any{"status": "contacted", "notes": "called, demo Tuesday"}, 200).body
	if str(up["status"]) != "contacted" || str(up["notes"]) != "called, demo Tuesday" {
		t.Errorf("lead update: %v", up)
	}
	if r := p.c.do("PATCH", "/platform/leads/"+leadID, map[string]any{"status": "maybe"}); r.status != 400 {
		t.Errorf("bad lead status: %d", r.status)
	}

	// ---------- payroll posting date option ----------
	if str(o.c.ok("GET", o.p("/payroll-options"), nil, 200).body["post_date"]) != "auto" {
		t.Error("payroll posting date default is not auto")
	}
	o.c.ok("PUT", o.p("/payroll-options"), map[string]any{"post_date": "period_end"}, 200)
	if str(o.c.ok("GET", o.p("/payroll-options"), nil, 200).body["post_date"]) != "period_end" {
		t.Error("payroll posting date not saved")
	}
	if r := o.c.do("PUT", o.p("/payroll-options"), map[string]any{"post_date": "tomorrow"}); r.status != 400 {
		t.Errorf("bad posting date mode: %d", r.status)
	}

	// ---------- supplier opening balances ----------
	sup := o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Old Supplier", "currency": "USD"}, 201).body
	supCode, supID := str(sup["code"]), str(sup["id"])
	rows := []any{
		map[string]any{"supplier_code": supCode, "amount": "1,200.50", "currency": "USD", "reference": "OLD-1", "date": "2026-01-15"},
		map[string]any{"supplier_name": "old supplier", "amount": "8950000", "currency": "LBP", "description": "LBP balance"},
	}
	chk := o.c.ok("POST", o.p("/import/supplier_openings"), map[string]any{"rows": rows, "dry_run": true}, 200).body
	if dec(t, chk["valid"]).IntPart() != 2 {
		t.Fatalf("supplier openings check: %v", chk)
	}
	if r := o.c.do("POST", o.p("/import/supplier_openings"), map[string]any{"rows": []any{map[string]any{"supplier_code": "NOPE", "amount": "5"}}}); r.status != 422 {
		t.Errorf("unknown supplier imported: %d", r.status)
	}
	done := o.c.ok("POST", o.p("/import/supplier_openings"), map[string]any{"rows": rows}, 200).body
	if dec(t, done["created"]).IntPart() != 2 {
		t.Fatalf("supplier openings: %v", done)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM bills WHERE supplier_id = $1 AND notes = 'Opening balance (import)' AND status <> 'void'`, supID); n != "2" {
		t.Errorf("opening bills: %s", n)
	}
	eqDec(t, "supplier USD opening", ownerQuery(t, `SELECT coalesce(sum(total), 0)::text FROM bills WHERE supplier_id = $1 AND currency = 'USD'`, supID), "1200.50")
	o.trialBalanced(t)

	// ---------- collectors keep to their own cash box ----------
	colEmail := "col" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": colEmail, "full_name": "Rami", "role": "collector", "password": "collector-pass-1"}, 201)
	var col string
	for _, u := range o.c.ok("GET", "/users", nil, 200).list {
		if str(obj(u)["email"]) == colEmail {
			col = str(obj(u)["id"])
		}
	}
	cc := loginAs(t, colEmail, "collector-pass-1")
	area := str(o.c.ok("POST", o.p("/areas"), map[string]any{"name": "South " + randHex(3), "collector_id": col}, 201).body["id"])
	cust := o.customer(t, "Field Customer", area)
	o.invoice(t, cust, today(), "100")
	// without a box of their own the collector still uses the shared box
	cc.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": today(), "currency": "USD",
		"tenders": []any{map[string]any{"method_id": usd, "amount": "10"}}}, 201)
	own := str(o.c.ok("POST", o.p("/payment-methods"), map[string]any{"name": "Rami USD", "kind": "cash", "currency": "USD",
		"account_id": o.accounts["531"], "user_id": col}, 201).body["id"])
	if r := cc.do("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": today(), "currency": "USD",
		"tenders": []any{map[string]any{"method_id": usd, "amount": "10"}}}); r.status != 400 {
		t.Errorf("collector with an own box used the shared box: %d", r.status)
	}
	for _, m := range cc.ok("GET", o.p("/payment-methods"), nil, 200).list {
		if id := str(obj(m)["id"]); id == usd {
			t.Error("the shared box is still listed to the collector")
		}
	}
	// offline payment: the device took it 3 hours ago (Beirut); synced now. Device times are kept at most 12 hours back
	// (audit P7-L2), so the working hours below are set around it: taken outside them, synced inside
	loc, _ := time.LoadLocation("Asia/Beirut")
	nowB := time.Now().In(loc)
	taken := nowB.Add(-3 * time.Hour).Truncate(time.Minute)
	ws, we := taken.Hour()+1, 24
	if nowB.Hour() < 3 {
		ws, we = 0, taken.Hour()
	}
	cc.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": today(), "currency": "USD", "client_ref": "pxv-" + randHex(8),
		"client_time": taken.Format(time.RFC3339), "tenders": []any{map[string]any{"method_id": own, "amount": "20"}}}, 201)
	if n := ownerQuery(t, `SELECT count(*)::text FROM payments WHERE customer_id = $1 AND client_time IS NOT NULL`, cust); n != "1" {
		t.Errorf("device time stored on %s payments", n)
	}
	// a device time in the future is ignored
	cc.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": today(), "currency": "USD", "client_ref": "pxv-" + randHex(8),
		"client_time": time.Now().Add(48 * time.Hour).Format(time.RFC3339), "tenders": []any{map[string]any{"method_id": own, "amount": "5"}}}, 201)
	if n := ownerQuery(t, `SELECT count(*)::text FROM payments WHERE customer_id = $1 AND client_time IS NOT NULL`, cust); n != "1" {
		t.Errorf("a future device time was kept (%s)", n)
	}

	// ---------- till: catalog prices lowered by hand ----------
	soap := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "SOAP", "name": "Soap", "kind": "service", "sale_price": "10", "sale_currency": "USD"}, 201).body["id"])
	reg := str(o.c.ok("POST", o.p("/pos/registers"), map[string]any{"name": "Till P", "currency": "USD", "method_ids": []any{usd}, "cashier_price_edit": true}, 201).body["id"])
	cashEmail := "cash" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": cashEmail, "full_name": "Cashier Lina", "role": "cashier", "password": "cashier-pass-1"}, 201)
	cs := loginAs(t, cashEmail, "cashier-pass-1")
	sess := str(obj(cs.ok("POST", o.p("/pos/sessions"), map[string]any{"register_id": reg}, 201).body["session"])["id"])
	for i := 0; i < 3; i++ {
		cs.ok("POST", o.p("/pos/orders"), map[string]any{"client_ref": "pxv-" + randHex(8), "session_id": sess,
			"lines": []any{map[string]any{"item_id": soap, "quantity": "2", "unit_price": "6"}}, "tenders": []any{map[string]any{"method_id": usd, "amount": "12"}}}, 201)
	}
	cs.ok("POST", o.p("/pos/orders"), map[string]any{"client_ref": "pxv-" + randHex(8), "session_id": sess,
		"lines": []any{map[string]any{"item_id": soap, "quantity": "1"}}, "tenders": []any{map[string]any{"method_id": usd, "amount": "10"}}}, 201)
	eqDec(t, "price cut recorded", ownerQuery(t, `SELECT sum(price_cut)::text FROM pos_orders WHERE session_id = $1`, sess), "24")
	o.c.ok("PUT", o.p("/smart/settings"), map[string]any{"params": map[string]any{"work_start": 0, "work_end": 24}}, 200)
	o.c.ok("POST", o.p("/smart/alerts/scan"), map[string]any{}, 200)
	as := alertsOf(t, o, "")
	pc := findAlert(as, "price_override")
	if pc == nil {
		t.Fatalf("no price_override alert: %v", as)
	}
	eqDec(t, "price cut value", pc["value"], "24")
	if str(obj(pc["detail"])["lines"]) != "3" || str(pc["subject_label"]) != "Cashier Lina" {
		t.Errorf("price_override alert: %v", pc)
	}
	// off hours: the device time is outside the working hours although the payment was synced inside them
	o.c.ok("PUT", o.p("/smart/settings"), map[string]any{"params": map[string]any{"work_start": ws, "work_end": we}}, 200)
	o.c.ok("POST", o.p("/smart/alerts/scan"), map[string]any{}, 200)
	found := false
	for _, a := range alertsOf(t, o, "") {
		if str(a["kind"]) == "off_hours" && str(a["subject_id"]) == col && str(obj(a["detail"])["first"]) == taken.Format("15:04") {
			found = true
		}
	}
	if !found {
		t.Errorf("the offline payment's device time was not used for the off-hours check")
	}

	// ---------- owner app ----------
	ot := o.c.ok("GET", o.p("/owner/today"), nil, 200).body
	if len(ot["collected"].([]any)) == 0 || len(ot["cash"].([]any)) == 0 {
		t.Errorf("owner today: %v", mustJSON(ot))
	}
	if dec(t, obj(ot["counts"])["alerts"]).IntPart() < 1 || len(ot["alerts"].([]any)) == 0 {
		t.Errorf("owner today alerts: %v", ot["counts"])
	}
	if r := cc.do("GET", o.p("/owner/today"), nil); r.status != 403 {
		t.Errorf("a collector reads the owner summary: %d", r.status)
	}

	// ---------- customer app (portal) ----------
	tok := str(o.c.ok("GET", o.p("/customers/"+cust+"/portal"), nil, 200).body["token"])
	more := pub.ok("GET", "/portal/"+tok+"/more", nil, 200).body
	if _, ok := more["tickets"]; !ok {
		t.Fatalf("portal more: %v", more)
	}
	nt := pub.ok("POST", "/portal/"+tok+"/tickets", map[string]any{"subject": "No power since morning", "category": "outage", "description": "Building B"}, 200).body
	if dec(t, nt["number"]).IntPart() < 1 {
		t.Fatalf("portal ticket: %v", nt)
	}
	if r := pub.do("POST", "/portal/"+tok+"/tickets", map[string]any{"subject": "", "category": "outage"}); r.status != 400 {
		t.Errorf("empty ticket: %d", r.status)
	}
	if r := pub.do("POST", "/portal/"+tok+"/tickets", map[string]any{"subject": "x", "category": "weather"}); r.status != 400 {
		t.Errorf("bad ticket category: %d", r.status)
	}
	more = pub.ok("GET", "/portal/"+tok+"/more", nil, 200).body
	tk := more["tickets"].([]any)
	if len(tk) != 1 || str(obj(tk[0])["subject"]) != "No power since morning" || str(obj(tk[0])["status"]) != "new" {
		t.Errorf("portal tickets: %v", tk)
	}
	if ch := ownerQuery(t, `SELECT channel FROM tickets WHERE customer_id = $1`, cust); ch != "portal" {
		t.Errorf("ticket channel %q", ch)
	}
	o.trialBalanced(t)
}
