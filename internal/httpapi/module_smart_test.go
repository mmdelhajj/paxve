package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Phase 5: smart features. Alerts center (diesel per hour, discounts, till shortage, voids, payments off hours,
// stock count loss, collector drop, duplicate bills; ack / dismiss / no repeat; digest), 13-week cash forecast,
// late-payer / churn risk, smart reorder (rules + purchase requests), collector route with GPS locations, and the
// AI assistant and bill reading against a fake Claude API (tools run with the user's permissions; quota; key off).

func daysAgo(n int) string { return time.Now().AddDate(0, 0, -n).Format(time.DateOnly) }

func alertsOf(t *testing.T, o *org, q string) []map[string]any {
	t.Helper()
	r := o.c.ok("GET", o.p("/smart/alerts"+q), nil, 200)
	var out []map[string]any
	for _, a := range r.body["alerts"].([]any) {
		out = append(out, obj(a))
	}
	return out
}

func findAlert(as []map[string]any, kind string) map[string]any {
	for _, a := range as {
		if str(a["kind"]) == kind {
			return a
		}
	}
	return nil
}

func TestSmartFlow(t *testing.T) {
	needDB(t)
	ctx := context.Background()
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	me := str(o.c.ok("GET", "/me", nil, 200).body["id"])
	usd, lbp, bank := o.methods["Cash USD"], o.methods["Cash LBP"], o.methods["Bank"]
	o.c.ok("POST", o.p("/message-providers"), map[string]any{"kind": "log", "name": "log", "is_default": true}, 201)

	// settings: defaults, validation, and working hours that make "now" outside them
	st := o.c.ok("GET", o.p("/smart/settings"), nil, 200).body
	if obj(st["params"])["z"] != 2.5 || len(st["kinds"].([]any)) != len(smartKinds) {
		t.Fatalf("default settings: %s", mustJSON(st))
	}
	if r := o.c.do("PUT", o.p("/smart/settings"), map[string]any{"params": map[string]any{"z": 0.1}}); r.status != 400 {
		t.Fatalf("z out of range: %d", r.status)
	}
	if r := o.c.do("PUT", o.p("/smart/settings"), map[string]any{"params": map[string]any{"work_start": 20, "work_end": 10}}); r.status != 400 {
		t.Fatalf("start after end: %d", r.status)
	}
	loc, _ := time.LoadLocation("Asia/Beirut")
	h := time.Now().In(loc).Hour()
	ws, we := h+1, 24
	if h >= 23 {
		ws, we = 1, 23
	}
	o.c.ok("PUT", o.p("/smart/settings"), map[string]any{"params": map[string]any{"work_start": ws, "work_end": we}, "digest_hour": 0,
		"digest_phone": "70111999"}, 200)

	// ---------- data ----------
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Diesel Co", "currency": "USD"}, 201).body["id"])
	diesel := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "DSL", "name": "Diesel", "kind": "stock", "unit": "L"}, 201).body["id"])
	soap := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "SOAP", "name": "Soap", "kind": "stock", "sale_price": "10", "sale_currency": "USD"}, 201).body["id"])
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": daysAgo(80), "currency": "USD",
		"lines": []any{map[string]any{"item_id": diesel, "quantity": "2000", "unit_price": "1"}, map[string]any{"item_id": soap, "quantity": "100", "unit_price": "4"}}}, 201)

	// generator: 50 running hours every week; 100 L a week for 6 weeks, 300 L this week (6 L/h instead of 2)
	station := str(o.c.ok("POST", o.p("/stations"), map[string]any{"name": "Main"}, 201).body["id"])
	gen := str(o.c.ok("POST", o.p("/generators"), map[string]any{"station_id": station, "name": "G1", "kva": "500", "fuel_item_id": diesel}, 201).body["id"])
	for k := 7; k >= 0; k-- {
		o.c.ok("POST", o.p("/generators/"+gen+"/hours"), map[string]any{"date": daysAgo(7 * k), "hour_meter": fmt.Sprint(1000 + (7-k)*50)}, 200)
	}
	for k := 6; k >= 1; k-- {
		o.c.ok("POST", o.p("/stock/fuel"), map[string]any{"item_id": diesel, "quantity": "100", "date": daysAgo(7*k + 3), "generator_id": gen}, 201)
	}
	o.c.ok("POST", o.p("/stock/fuel"), map[string]any{"item_id": diesel, "quantity": "300", "date": daysAgo(1), "generator_id": gen}, 201)

	// duplicate supplier bill (same supplier invoice number, entered twice on purpose)
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": daysAgo(5), "currency": "USD", "supplier_ref": "INV-77",
		"lines": []any{map[string]any{"description": "Transport", "quantity": "1", "unit_price": "40", "account_id": o.accounts["611"]}}}, 201)
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": daysAgo(4), "currency": "USD", "supplier_ref": "inv-77", "allow_duplicate": true,
		"lines": []any{map[string]any{"description": "Transport", "quantity": "1", "unit_price": "40", "account_id": o.accounts["611"]}}}, 201)

	// voids: four bills voided this week by the owner
	walk := o.customer(t, "Void Test", "")
	for i := 0; i < 4; i++ {
		inv := o.invoice(t, walk, today(), "5")
		o.c.ok("POST", o.p("/invoices/"+inv+"/void"), map[string]any{"reason": "test"}, 200)
	}

	// a payment now (outside the working hours set above)
	payer := o.customer(t, "Night Payer", "")
	o.invoice(t, payer, today(), "30")
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": payer, "date": today(), "currency": "USD",
		"tenders": []any{map[string]any{"method_id": usd, "amount": "30"}}}, 201)

	// POS: the owner gives 20% off on its till and closes it 10 USD short; a cashier sells at the full price
	reg := str(o.c.ok("POST", o.p("/pos/registers"), map[string]any{"name": "Till 1", "currency": "USD", "method_ids": []any{usd, lbp}}, 201).body["id"])
	reg2 := str(o.c.ok("POST", o.p("/pos/registers"), map[string]any{"name": "Till 2", "currency": "USD", "method_ids": []any{usd}}, 201).body["id"])
	sess := str(obj(o.c.ok("POST", o.p("/pos/sessions"), map[string]any{"register_id": reg, "opening": []any{map[string]any{"method_id": usd, "amount": "20"}}}, 201).body["session"])["id"])
	o.c.ok("POST", o.p("/pos/orders"), map[string]any{"client_ref": "pxv-" + randHex(8), "session_id": sess,
		"lines": []any{map[string]any{"item_id": soap, "quantity": "5", "discount_pct": "20"}}, "tenders": []any{map[string]any{"method_id": usd, "amount": "40"}}}, 201)
	o.c.ok("POST", o.p("/pos/sessions/"+sess+"/close"), map[string]any{"counted": []any{
		map[string]any{"method_id": usd, "amount": "50"}, map[string]any{"method_id": lbp, "amount": "0"}}}, 200)
	cashEmail := "cash" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": cashEmail, "full_name": "Cashier Nour", "role": "cashier", "password": "cashier-pass-1"}, 201)
	cs := loginAs(t, cashEmail, "cashier-pass-1")
	sess2 := str(obj(cs.ok("POST", o.p("/pos/sessions"), map[string]any{"register_id": reg2}, 201).body["session"])["id"])
	cs.ok("POST", o.p("/pos/orders"), map[string]any{"client_ref": "pxv-" + randHex(8), "session_id": sess2,
		"lines": []any{map[string]any{"item_id": soap, "quantity": "5"}}, "tenders": []any{map[string]any{"method_id": usd, "amount": "50"}}}, 201)

	// stock count: 20 soaps missing (value 80 > the 50 limit)
	wh := ownerQuery(t, `SELECT warehouse_id::text FROM stock WHERE item_id = $1 AND quantity > 0 LIMIT 1`, soap)
	cnt := str(obj(o.c.ok("POST", o.p("/stock/counts"), map[string]any{"warehouse_id": wh, "date": today()}, 201).body)["count"].(map[string]any)["id"])
	o.c.ok("PUT", o.p("/stock/counts/"+cnt+"/lines"), map[string]any{"lines": []any{map[string]any{"item_id": soap, "counted": "70"}}}, 200)
	o.c.ok("POST", o.p("/stock/counts/"+cnt+"/post"), map[string]any{}, 200)

	// collector: 10 USD every week for 8 weeks, nothing this week
	colEmail := "col" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": colEmail, "full_name": "Jaber", "role": "collector", "password": "collector-pass-1"}, 201)
	var col string
	for _, u := range o.c.ok("GET", "/users", nil, 200).list {
		if str(obj(u)["email"]) == colEmail {
			col = str(obj(u)["id"])
		}
	}
	cc := loginAs(t, colEmail, "collector-pass-1")
	area := str(o.c.ok("POST", o.p("/areas"), map[string]any{"name": "North", "collector_id": col}, 201).body["id"])
	sub := o.customer(t, "Subscriber Far", area)
	o.invoice(t, sub, daysAgo(70), "300")
	for k := 1; k <= 8; k++ {
		cc.ok("POST", o.p("/payments"), map[string]any{"customer_id": sub, "date": daysAgo(7*k + 1), "currency": "USD",
			"tenders": []any{map[string]any{"method_id": usd, "amount": "10"}}}, 201)
	}

	// ---------- scan ----------
	sc := o.c.ok("POST", o.p("/smart/alerts/scan"), map[string]any{}, 200).body
	as := alertsOf(t, o, "")
	kinds := map[string]bool{}
	for _, a := range as {
		kinds[str(a["kind"])] = true
	}
	for _, k := range []string{"diesel", "dup_bill", "voids", "off_hours", "discount", "cash_diff", "count_loss", "collector_drop"} {
		if !kinds[k] {
			t.Errorf("alert %s missing; got %v (scan %v)", k, kinds, sc)
		}
	}
	if d := findAlert(as, "diesel"); d != nil {
		eqDec(t, "diesel l/h", d["value"], "6")
		eqDec(t, "diesel normal", d["expected"], "2")
		if str(d["severity"]) != "high" || str(d["subject_label"]) != "G1" {
			t.Errorf("diesel alert: %v", d)
		}
	}
	if d := findAlert(as, "dup_bill"); d != nil && (str(d["severity"]) != "high" || obj(d["detail"])["same_ref"] != true) {
		t.Errorf("duplicate bill: %v", d)
	}
	if d := findAlert(as, "cash_diff"); d != nil {
		eqDec(t, "till short", d["value"], "-10")
	}
	if d := findAlert(as, "discount"); d != nil {
		eqDec(t, "discount %", d["value"], "20")
		if str(d["subject_id"]) != me {
			t.Errorf("discount by the owner: %v", d)
		}
	}
	if d := findAlert(as, "voids"); d != nil {
		eqDec(t, "voids", d["value"], "4")
	}
	if d := findAlert(as, "collector_drop"); d != nil && str(d["subject_id"]) != col {
		t.Errorf("collector drop: %v", d)
	}
	if d := findAlert(as, "count_loss"); d != nil {
		eqDec(t, "count loss", d["value"], "80")
	}
	if a := findAlert(as, "off_hours"); a != nil && dec(t, obj(a["detail"])["payments"]).IntPart() < 1 {
		t.Errorf("off hours: %v", a)
	}
	// a second scan raises nothing new; dismissed / resolved alerts stay so
	if n := o.c.ok("POST", o.p("/smart/alerts/scan"), map[string]any{}, 200).body["new"]; str(n) != "0" {
		t.Errorf("rescan created %v alerts", n)
	}
	dup := findAlert(as, "dup_bill")
	o.c.ok("POST", o.p("/smart/alerts/"+str(dup["id"])+"/ack"), map[string]any{"note": "checking with supplier"}, 200)
	if r := o.c.do("POST", o.p("/smart/alerts/"+str(dup["id"])+"/ack"), map[string]any{}); r.status != 409 {
		t.Errorf("ack twice: %d", r.status)
	}
	o.c.ok("POST", o.p("/smart/alerts/"+str(dup["id"])+"/dismiss"), map[string]any{"note": "two deliveries"}, 200)
	o.c.ok("POST", o.p("/smart/alerts/scan"), map[string]any{}, 200)
	if d := findAlert(alertsOf(t, o, "?status=dismissed"), "dup_bill"); d == nil || str(d["note"]) != "two deliveries" {
		t.Errorf("dismissed alert: %v", d)
	}
	if findAlert(alertsOf(t, o, ""), "dup_bill") != nil {
		t.Error("dismissed duplicate raised again")
	}
	// a check switched off raises nothing
	o.c.ok("PUT", o.p("/smart/settings"), map[string]any{"kinds": []string{"diesel"}}, 200)
	if got := o.c.ok("GET", o.p("/smart/settings"), nil, 200).body["kinds"].([]any); len(got) != 1 {
		t.Errorf("kinds: %v", got)
	}
	o.c.ok("PUT", o.p("/smart/settings"), map[string]any{"kinds": smartKinds}, 200)
	// read-only users see alerts but cannot act on them
	roEmail := "ro" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": roEmail, "full_name": "Viewer", "role": "read_only", "password": "viewer-pass-12"}, 201)
	ro := loginAs(t, roEmail, "viewer-pass-12")
	ro.ok("GET", o.p("/smart/alerts"), nil, 200)
	if r := ro.do("POST", o.p("/smart/alerts/scan"), map[string]any{}); r.status != 403 {
		t.Errorf("read-only scan: %d", r.status)
	}

	// digest: in-app (and the WhatsApp copy to the digest phone)
	if _, err := itAPI.RunSmartJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM user_notifications WHERE company_id = $1 AND kind = 'smart' AND user_id = $2`, o.company, me); n != "1" {
		t.Errorf("digest notifications: %s", n)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM messages WHERE company_id = $1 AND phone LIKE '%70111999' AND body LIKE 'Paxve:%'`, o.company); n != "1" {
		t.Errorf("digest WhatsApp: %s", n)
	}
	if _, err := itAPI.RunSmartJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM user_notifications WHERE company_id = $1 AND kind = 'smart' AND user_id = $2`, o.company, me); n != "1" {
		t.Errorf("digest sent twice the same day: %s", n)
	}

	// ---------- forecast ----------
	emp := str(o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Rami", "currency": "USD", "salary": "1000"}, 201).body["id"])
	_ = emp
	due := o.customer(t, "Due Soon", "")
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": due, "currency": "USD", "issue_date": today(), "due_date": time.Now().AddDate(0, 0, 10).Format(time.DateOnly),
		"lines": []any{map[string]any{"description": "Service", "quantity": "1", "unit_price": "200"}}}, 201)
	fc := o.c.ok("GET", o.p("/smart/forecast"), nil, 200).body
	weeks := fc["weeks"].([]any)
	if len(weeks) != 13 || len(fc["months"].([]any)) < 3 {
		t.Fatalf("forecast shape: %s", mustJSON(fc))
	}
	cash := 0.0
	for _, b := range o.c.ok("GET", o.p("/cash/balances"), nil, 200).list {
		if str(obj(b)["currency"]) == "USD" {
			cash += dec(t, obj(b)["balance"]).InexactFloat64()
		}
	}
	if op := dec(t, fc["opening"]).InexactFloat64(); op < cash-0.01 || op > cash+0.01+5 { // LBP boxes are empty here
		t.Errorf("opening %v, USD cash %v", op, cash)
	}
	var rec, pay float64
	for _, w := range weeks {
		rec += dec(t, zeroIfNil(obj(obj(w)["in"])["receivables"])).InexactFloat64()
		pay += dec(t, zeroIfNil(obj(obj(w)["out"])["payroll"])).InexactFloat64()
	}
	if rec < 190 { // 200 due in 10 days at 95% + the collector's subscriber
		t.Errorf("receivables in forecast: %v", rec)
	}
	if pay < 3000 || pay > 4000 {
		t.Errorf("payroll in forecast: %v", pay)
	}
	if w0 := obj(weeks[0]); dec(t, w0["closing"]).Sub(dec(t, fc["opening"]).Add(dec(t, w0["net"]))).Abs().InexactFloat64() > 0.02 {
		t.Errorf("week 1 closing: %v", w0)
	}
	if fc["local_currency"] != "LBP" || fc["local_rate"] == nil || obj(weeks[0])["closing_local"] == nil {
		t.Errorf("LBP view: %v %v", fc["local_currency"], fc["local_rate"])
	}

	// ---------- risk ----------
	good := o.customer(t, "Always On Time", "")
	gi := o.invoice(t, good, daysAgo(40), "50")
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": good, "date": daysAgo(39), "currency": "USD", "invoice_id": gi,
		"tenders": []any{map[string]any{"method_id": bank, "amount": "50"}}}, 201)
	o.c.ok("POST", o.p("/smart/risk/recompute"), map[string]any{}, 200)
	rk := o.c.ok("GET", o.p("/smart/risk"), nil, 200).body
	scores := map[string]map[string]any{}
	for _, x := range rk["rows"].([]any) {
		scores[str(obj(x)["name"])] = obj(x)
	}
	far := scores["Subscriber Far"]
	if far == nil || dec(t, far["late_score"]).IntPart() < 20 || str(far["band"]) == "low" {
		t.Errorf("late customer risk: %v", far)
	} else {
		codes := []string{}
		for _, f := range far["factors"].([]any) {
			codes = append(codes, str(obj(f)["code"]))
		}
		if !strings.Contains(strings.Join(codes, ","), "days_overdue") {
			t.Errorf("risk factors: %v", codes)
		}
	}
	if g := scores["Always On Time"]; g == nil || dec(t, g["late_score"]).IntPart() > 5 || str(g["band"]) != "low" {
		t.Errorf("good customer: %v", g)
	}
	if r := o.c.do("GET", o.p("/smart/risk?band=x"), nil); r.status != 400 {
		t.Errorf("bad band: %d", r.status)
	}

	// ---------- reorder ----------
	o.c.ok("POST", o.p("/supplier-prices"), map[string]any{"supplier_id": sup, "item_id": diesel, "currency": "USD", "price": "0.9", "lead_days": 60}, 201)
	ro2 := o.c.ok("GET", o.p("/smart/reorder"), nil, 200).body
	var dl map[string]any
	for _, l := range ro2["lines"].([]any) {
		if str(obj(l)["item_id"]) == diesel {
			dl = obj(l)
		}
	}
	if dl == nil || str(dl["status"]) != "order" || str(dl["lead_source"]) != "supplier" || dec(t, dl["lead_days"]).IntPart() != 60 ||
		str(dl["supplier_id"]) != sup || !dec(t, dl["to_order"]).IsPositive() {
		t.Fatalf("diesel reorder line: %v", dl)
	}
	eqDec(t, "weekly demand", dl["weekly_demand"], "180") // (4×300 + 3×100 + 2×100 + 100) / 10
	o.c.ok("POST", o.p("/smart/reorder/apply"), map[string]any{"lines": []any{map[string]any{"item_id": diesel, "warehouse_id": str(dl["warehouse_id"]),
		"min_qty": fmt.Sprint(dl["suggest_min"]), "max_qty": fmt.Sprint(dl["suggest_max"]), "supplier_id": sup}}}, 200)
	if v := ownerQuery(t, `SELECT trim_scale(min_qty)::text FROM reorder_rules WHERE item_id = $1`, diesel); v != fmt.Sprint(dl["suggest_min"]) {
		t.Errorf("rule min %s, want %v", v, dl["suggest_min"])
	}
	po := o.c.ok("POST", o.p("/smart/reorder/purchase"), map[string]any{"lines": []any{map[string]any{"item_id": diesel, "warehouse_id": str(dl["warehouse_id"]),
		"quantity": fmt.Sprint(dl["to_order"]), "supplier_id": sup}}}, 201).body
	orders := po["orders"].([]any)
	if len(orders) != 1 {
		t.Fatalf("purchase requests: %v", po)
	}
	pod := o.c.ok("GET", o.p("/purchase-orders/"+str(obj(orders[0])["id"])), nil, 200).body
	if str(obj(pod["order"])["status"]) != "rfq" {
		t.Errorf("purchase request status: %v", obj(pod["order"])["status"])
	}
	eqDec(t, "supplier price on the request", obj(pod["lines"].([]any)[0])["unit_price"], "0.9")
	// the request counts as on order now
	for _, l := range o.c.ok("GET", o.p("/smart/reorder"), nil, 200).body["lines"].([]any) {
		if str(obj(l)["item_id"]) == diesel && dec(t, obj(l)["on_order"]).IsZero() {
			t.Errorf("on order not counted: %v", l)
		}
	}

	// ---------- collector route ----------
	a1, a2, a3 := o.customer(t, "Stop A", area), o.customer(t, "Stop B", area), o.customer(t, "Stop C", area)
	other := o.customer(t, "Other Area", "")
	for _, c := range []string{a1, a2, a3, other} {
		o.invoice(t, c, today(), "15")
	}
	cc.ok("PUT", o.p("/customers/"+a3+"/location"), map[string]any{"lat": 33.94, "lng": 35.50, "source": "gps"}, 200)
	cc.ok("PUT", o.p("/customers/"+a1+"/location"), map[string]any{"lat": 33.90, "lng": 35.50, "source": "gps"}, 200)
	cc.ok("PUT", o.p("/customers/"+a2+"/location"), map[string]any{"lat": 33.92, "lng": 35.50, "source": "gps"}, 200)
	if r := cc.do("PUT", o.p("/customers/"+other+"/location"), map[string]any{"lat": 33.9, "lng": 35.5}); r.status != 404 {
		t.Errorf("collector sets location outside their areas: %d", r.status)
	}
	if r := cc.do("PUT", o.p("/customers/"+a1+"/location"), map[string]any{"lat": 95, "lng": 35.5}); r.status != 400 {
		t.Errorf("bad latitude: %d", r.status)
	}
	rt := cc.ok("GET", o.p("/collect/route?lat=33.89&lng=35.50"), nil, 200).body
	stops := rt["stops"].([]any)
	names := []string{}
	for _, s := range stops {
		names = append(names, str(obj(s)["name"]))
	}
	if strings.Join(names, ",") != "Stop A,Stop B,Stop C" {
		t.Errorf("route order: %v", names)
	}
	if km := dec(t, rt["total_km"]).InexactFloat64(); km < 5 || km > 6.5 { // 0.05° of latitude ≈ 5.6 km
		t.Errorf("route km: %v", km)
	}
	missing := []string{}
	for _, m := range rt["missing_location"].([]any) {
		missing = append(missing, str(obj(m)["name"]))
	}
	if strings.Contains(strings.Join(missing, ","), "Other Area") || !strings.Contains(strings.Join(missing, ","), "Subscriber Far") {
		t.Errorf("collector sees only their areas: %v", missing)
	}
	if links := rt["maps_links"].([]any); len(links) != 1 || !strings.HasPrefix(str(links[0]), "https://www.google.com/maps/dir/?") {
		t.Errorf("maps links: %v", links)
	}
	// the office sees every area
	all := o.c.ok("GET", o.p("/collect/route"), nil, 200).body
	if len(all["stops"].([]any)) != 3 || len(all["missing_location"].([]any)) < 3 {
		t.Errorf("office route: %d stops, %d missing", len(all["stops"].([]any)), len(all["missing_location"].([]any)))
	}

	// ---------- AI off ----------
	itAPI.SetAI("", "", "")
	stA := o.c.ok("GET", o.p("/smart/ai/status"), nil, 200).body
	if stA["enabled"] != false {
		t.Errorf("ai status: %v", stA)
	}
	if r := o.c.do("POST", o.p("/smart/ai/ask"), map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}); r.status != 409 ||
		!strings.Contains(r.raw, "ai_not_configured") {
		t.Errorf("ask without key: %d %s", r.status, r.raw)
	}
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	scan := o.c.ok("POST", o.p("/smart/scans"), map[string]any{"filename": "bill.png", "data": "data:image/png;base64," + png, "read": true}, 201).body
	if str(scan["status"]) != "new" || str(scan["mime"]) != "image/png" {
		t.Errorf("scan stored: %v", scan)
	}
	if r := o.c.do("POST", o.p("/smart/scans/"+str(scan["id"])+"/read"), map[string]any{}); r.status != 409 {
		t.Errorf("read without key: %d", r.status)
	}
	if r := o.c.do("POST", o.p("/smart/scans"), map[string]any{"filename": "x.txt", "data": "aGVsbG8="}); r.status != 400 {
		t.Errorf("text file accepted: %d", r.status)
	}

	o.trialBalanced(t)
}

func zeroIfNil(v any) any {
	if v == nil {
		return "0"
	}
	return v
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// fakeClaude answers like the Messages API: for the assistant it first asks for the late_customers tool, then
// answers with what the tool returned; for bill reading it returns the structured JSON.
func fakeClaude(t *testing.T, calls *atomic.Int32, lastBody *atomic.Value) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		lastBody.Store(string(raw))
		if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer test-key" ||
			!strings.Contains(r.Header.Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
			if r.Header.Get("Authorization") != "Bearer wrong-key" {
				t.Errorf("fake Claude refused: path %s key %q beta %q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("anthropic-beta"))
			}
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"bad"}}`))
			return
		}
		var req struct {
			Model        string `json:"model"`
			Fallbacks    any    `json:"fallbacks"`
			OutputConfig struct {
				Format any `json:"format"`
			} `json:"output_config"`
			Messages []struct {
				Role    string            `json:"role"`
				Content []json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		w.Header().Set("Content-Type", "application/json")
		msg := func(content string, stop string) {
			fmt.Fprintf(w, `{"id":"msg_%d","type":"message","role":"assistant","model":%q,"content":%s,"stop_reason":%q,"stop_sequence":null,
				"usage":{"input_tokens":120,"output_tokens":30,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`, calls.Load(), req.Model, content, stop)
		}
		if req.Fallbacks != "default" {
			t.Errorf("fallbacks not set: %v", req.Fallbacks)
		}
		if req.OutputConfig.Format != nil {
			bill := `{"supplier_name":"DIESEL CO","supplier_tax_id":"","invoice_number":"INV-900","invoice_date":"2026-11-01","due_date":"","currency":"$",` +
				`"subtotal":90,"tax":0,"total":90,"lines":[{"description":"Diesel","quantity":100,"unit_price":0.9,"amount":90}],"notes":""}`
			b, _ := json.Marshal(bill)
			msg(`[{"type":"text","text":`+string(b)+`}]`, "end_turn")
			return
		}
		last := req.Messages[len(req.Messages)-1]
		for _, c := range last.Content {
			var blk struct {
				Type    string `json:"type"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			_ = json.Unmarshal(c, &blk)
			if blk.Type == "tool_result" {
				text := ""
				if len(blk.Content) > 0 {
					text = blk.Content[0].Text
				}
				b, _ := json.Marshal("Top late payer: " + text)
				msg(`[{"type":"text","text":`+string(b)+`}]`, "end_turn")
				return
			}
		}
		msg(`[{"type":"thinking","thinking":"","signature":"sig"},{"type":"tool_use","id":"toolu_1","name":"late_customers","input":{"limit":3}}]`, "tool_use")
	}))
}

func TestSmartAIWithFakeClaude(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	var calls atomic.Int32
	var last atomic.Value
	srv := fakeClaude(t, &calls, &last)
	defer srv.Close()
	itAPI.SetAI("test-key", "", srv.URL)
	defer itAPI.SetAI("", "", "")

	late := o.customer(t, "Karim Late", "")
	o.invoice(t, late, daysAgo(90), "120")
	st := o.c.ok("GET", o.p("/smart/ai/status"), nil, 200).body
	if st["enabled"] != true || str(st["model"]) != "claude-opus-5-5" {
		t.Fatalf("status: %v", st)
	}
	bad := []map[string]any{{"messages": []any{}}, {"messages": []any{map[string]any{"role": "assistant", "content": "x"}}},
		{"messages": []any{map[string]any{"role": "user", "content": strings.Repeat("x", 4001)}}}}
	for _, b := range bad {
		if r := o.c.do("POST", o.p("/smart/ai/ask"), b); r.status != 400 {
			t.Errorf("bad ask %v: %d", b, r.status)
		}
	}
	wait := func(c *client, path string, done func(map[string]any) bool) map[string]any {
		t.Helper()
		for i := 0; i < 100; i++ {
			r := c.ok("GET", path, nil, 200).body
			if done(r) {
				return r
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s never finished", path)
		return nil
	}
	id := str(o.c.ok("POST", o.p("/smart/ai/ask"), map[string]any{"messages": []any{map[string]any{"role": "user", "content": "مين أكثر زبون متأخر؟"}}}, 202).body["id"])
	ans := wait(o.c, o.p("/smart/ai/answers/"+id), func(m map[string]any) bool { return str(m["status"]) != "running" })
	if str(ans["status"]) != "done" || !strings.Contains(str(ans["answer"]), "Karim Late") || !strings.Contains(str(ans["answer"]), "120") {
		t.Fatalf("answer: %v", ans)
	}
	if tools := ans["tools"].([]any); len(tools) != 1 || str(tools[0]) != "late_customers" {
		t.Errorf("tools used: %v", tools)
	}
	if !strings.Contains(last.Load().(string), "Karim Late") {
		t.Error("tool result not sent back to the model")
	}
	eqDec(t, "tokens in", ans["input_tokens"], "240")
	if h := o.c.ok("GET", o.p("/smart/ai/history"), nil, 200).list; len(h) != 1 {
		t.Errorf("history: %d", len(h))
	}

	// a user without the data permission: the tool answers "forbidden" to the model, never the data
	email := "col" + randHex(4) + "@example.com"
	roleID := str(o.c.ok("POST", "/roles", map[string]any{"name": "AI only", "base": "read_only", "permissions": []string{"smart.view", "smart.ai"}}, 201).body["id"])
	o.c.ok("POST", "/users", map[string]any{"email": email, "full_name": "Asker", "role_id": roleID, "locale": "en", "password": "asker-pass-123"}, 201)
	ask := loginAs(t, email, "asker-pass-123")
	id2 := str(ask.ok("POST", o.p("/smart/ai/ask"), map[string]any{"messages": []any{map[string]any{"role": "user", "content": "who is late?"}}}, 202).body["id"])
	a2 := wait(ask, o.p("/smart/ai/answers/"+id2), func(m map[string]any) bool { return str(m["status"]) != "running" })
	if strings.Contains(str(a2["answer"]), "Karim") || !strings.Contains(str(a2["answer"]), "forbidden") {
		t.Errorf("tool ran without permission: %v", a2)
	}
	// someone else's answer stays private
	if r := ask.do("GET", o.p("/smart/ai/answers/"+id), nil); r.status != 404 {
		t.Errorf("other user's answer: %d", r.status)
	}

	// bill reading: draft matched to the supplier, item and currency; then the bill is made and linked
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Diesel Co", "currency": "USD"}, 201).body["id"])
	item := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "DSL", "name": "Diesel", "kind": "stock", "unit": "L"}, 201).body["id"])
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	scan := o.c.ok("POST", o.p("/smart/scans"), map[string]any{"filename": "bill.png", "data": png, "read": true}, 201).body
	sid := str(scan["id"])
	got := wait(o.c, o.p("/smart/scans/"+sid), func(m map[string]any) bool { s := str(m["status"]); return s == "read" || s == "error" })
	d := obj(got["draft"])
	if str(got["status"]) != "read" || str(d["supplier_id"]) != sup || str(d["currency"]) != "USD" || str(d["supplier_ref"]) != "INV-900" || str(d["bill_date"]) != "2026-11-01" {
		t.Fatalf("draft: %v", got)
	}
	line := obj(d["lines"].([]any)[0])
	if str(line["item_id"]) != item {
		t.Errorf("item match: %v", line)
	}
	eqDec(t, "line qty", line["quantity"], "100")
	eqDec(t, "line price", line["unit_price"], "0.9")
	bill := o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD", "supplier_ref": "INV-900",
		"lines": []any{map[string]any{"item_id": item, "quantity": "100", "unit_price": "0.9"}}}, 201).body
	billID := str(obj(bill["bill"])["id"])
	if billID == "" {
		billID = str(bill["id"])
	}
	o.c.ok("POST", o.p("/smart/scans/"+sid+"/link"), map[string]any{"bill_id": billID}, 200)
	if n := len(o.c.ok("GET", o.p("/attachments?type=bill&id="+billID), nil, 200).list); n != 1 {
		t.Errorf("scan attached to the bill: %d", n)
	}
	if r := o.c.do("DELETE", o.p("/smart/scans/"+sid), nil); r.status != 409 {
		t.Errorf("delete a billed scan: %d", r.status)
	}
	// reading the same bill again shows it as a duplicate
	scan2 := str(o.c.ok("POST", o.p("/smart/scans"), map[string]any{"filename": "again.png", "data": png, "read": true}, 201).body["id"])
	got2 := wait(o.c, o.p("/smart/scans/"+scan2), func(m map[string]any) bool { s := str(m["status"]); return s == "read" || s == "error" })
	if obj(got2["draft"])["duplicate_bill"] == nil {
		t.Errorf("duplicate not flagged: %v", got2["draft"])
	}
	o.c.ok("DELETE", o.p("/smart/scans/"+scan2), nil, 200)

	// the monthly limit of the plan
	used := str(o.c.ok("GET", o.p("/smart/ai/status"), nil, 200).body["used"])
	if used != "4" {
		t.Errorf("used this month: %s", used)
	}
	ctx := context.Background()
	var plan string
	if err := itOwnerPool.QueryRow(ctx, `SELECT plan FROM tenants WHERE id = $1`, o.tenant).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	var old *int
	_ = itOwnerPool.QueryRow(ctx, `SELECT ai_monthly_limit FROM platform_plans WHERE code = $1`, plan).Scan(&old)
	if _, err := itOwnerPool.Exec(ctx, `UPDATE platform_plans SET ai_monthly_limit = 4 WHERE code = $1`, plan); err != nil {
		t.Fatal(err)
	}
	r := o.c.do("POST", o.p("/smart/ai/ask"), map[string]any{"messages": []any{map[string]any{"role": "user", "content": "again"}}})
	_, _ = itOwnerPool.Exec(ctx, `UPDATE platform_plans SET ai_monthly_limit = $2 WHERE code = $1`, plan, old)
	if r.status != 429 || !strings.Contains(r.raw, "ai_limit_reached") {
		t.Errorf("over the limit: %d %s", r.status, r.raw)
	}

	// a wrong key: the answer fails with a clear code
	itAPI.SetAI("wrong-key", "", srv.URL)
	id3 := str(o.c.ok("POST", o.p("/smart/ai/ask"), map[string]any{"messages": []any{map[string]any{"role": "user", "content": "x"}}}, 202).body["id"])
	a3 := wait(o.c, o.p("/smart/ai/answers/"+id3), func(m map[string]any) bool { return str(m["status"]) != "running" })
	if str(a3["status"]) != "error" || str(a3["error"]) != "ai_key_invalid" {
		t.Errorf("wrong key: %v", a3)
	}

	// the platform owner sees the use per organisation; other users may not
	if r := o.c.do("GET", "/platform/ai", nil); r.status != 403 {
		t.Errorf("platform AI page for a tenant: %d", r.status)
	}
	po := platformOwner(t)
	pa := po.c.ok("GET", "/platform/ai", nil, 200).body
	found := false
	for _, u := range pa["usage"].([]any) {
		if str(obj(u)["tenant_id"]) == o.tenant && str(obj(u)["requests"]) == "4" {
			found = true
		}
	}
	if pa["enabled"] != true || !found || len(pa["plans"].([]any)) == 0 {
		t.Errorf("platform AI: %s", mustJSON(pa))
	}
	o.trialBalanced(t)
}
