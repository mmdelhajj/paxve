package httpapi

import (
	"strings"
	"testing"
)

// Phase 6a: business packs and their vertical features (electronics, furniture, wholesale / van sales, restaurant,
// pharmacy, factory). Every flow ends with stock = ledger and a balanced trial balance.

func (o *org) itemByCode(t *testing.T, code string) string {
	t.Helper()
	id := ownerQuery(t, `SELECT id::text FROM items WHERE company_id = $1 AND code = $2`, o.company, code)
	if id == "" {
		t.Fatalf("item %s not found", code)
	}
	return id
}

func (o *org) stockLedgerEqual(t *testing.T) {
	t.Helper()
	val := o.c.ok("GET", o.p("/wh/reports/valuation"), nil, 200)
	for _, a := range val.body["accounts"].([]any) {
		eqDec(t, "stock = ledger on "+str(obj(a)["code"]), obj(a)["difference"], "0")
	}
	o.trialBalanced(t)
}

// packTill: the demo till of a pack and an open session on it.
func (o *org) packTill(t *testing.T) (reg, sess string) {
	t.Helper()
	regs := o.c.ok("GET", o.p("/pos/registers"), nil, 200).list
	if len(regs) == 0 {
		t.Fatal("pack demo made no till")
	}
	reg = str(obj(regs[0])["id"])
	sess = str(obj(o.c.ok("POST", o.p("/pos/sessions"), map[string]any{"register_id": reg}, 201).body["session"])["id"])
	return reg, sess
}

func applyPack(t *testing.T, key string) *org {
	t.Helper()
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	p := o.c.ok("POST", o.p("/retail/packs/"+key), map[string]any{"demo": true}, 200)
	if str(obj(p.body["pack"])["business_type"]) != key {
		t.Fatalf("pack %s: %s", key, p.raw)
	}
	o.reloadAccounts(t)
	for _, m := range o.c.ok("GET", o.p("/payment-methods"), nil, 200).list {
		o.methods[str(obj(m)["name"])] = str(obj(m)["id"])
	}
	return o
}

func TestBusinessPackList(t *testing.T) {
	needDB(t)
	o := signup(t, "generic")
	packs := o.c.ok("GET", o.p("/retail/packs"), nil, 200)
	got := map[string]bool{}
	for _, p := range packs.body["packs"].([]any) {
		got[str(obj(p)["key"])] = true
	}
	for _, k := range []string{"supermarket", "clothing", "electronics", "furniture", "wholesale", "restaurant", "pharmacy", "factory"} {
		if !got[k] {
			t.Fatalf("pack %s missing: %s", k, packs.raw)
		}
	}
	// every business type can be chosen on the apps page too
	o.c.ok("PUT", o.p("/apps"), map[string]any{"apps": []string{"stk", "pur", "pos"}, "business_type": "pharmacy"}, 200)
	// factory pack: work centers, the cabinet's BoM and routing
	f := applyPack(t, "factory")
	if n := len(f.c.ok("GET", f.p("/mfg/workcenters"), nil, 200).list); n != 3 {
		t.Fatalf("factory work centers: %d", n)
	}
	if n := len(f.c.ok("GET", f.p("/mfg/boms"), nil, 200).list); n < 1 {
		t.Fatal("factory BoM missing")
	}
	if regs := f.c.ok("GET", f.p("/pos/registers"), nil, 200).list; len(regs) != 0 {
		t.Fatal("factory pack should not open a till")
	}
	f.stockLedgerEqual(t)
}

func TestElectronicsPack(t *testing.T) {
	needDB(t)
	o := applyPack(t, "electronics")
	phone, used, charger := o.itemByCode(t, "PH-A1"), o.itemByCode(t, "PH-USED"), o.itemByCode(t, "CHG")
	_, sess := o.packTill(t)
	usd := o.methods["Cash USD"]
	sns := o.c.ok("GET", o.p("/stock/serials?item_id="+phone), nil, 200).list
	if len(sns) != 5 {
		t.Fatalf("serials in stock: %d", len(sns))
	}
	sn1, sn2 := str(obj(sns[0])["serial"]), str(obj(sns[1])["serial"])
	// the serial must match the quantity
	if r := o.c.do("POST", o.p("/pos/orders"), map[string]any{"session_id": sess, "lines": []any{map[string]any{"item_id": phone, "quantity": "2", "serials": []any{sn1}}},
		"tenders": []any{map[string]any{"method_id": usd, "amount": "598"}}}); r.status != 422 || !strings.Contains(r.raw, "serials_mismatch") {
		t.Fatalf("serial count: %d %s", r.status, r.raw)
	}
	cust := o.customer(t, "Rami", "")
	o.c.ok("POST", o.p("/pos/orders"), map[string]any{"session_id": sess, "customer_id": cust,
		"lines": []any{map[string]any{"item_id": phone, "quantity": "1", "serials": []any{sn1}}}, "tenders": []any{map[string]any{"method_id": usd, "amount": "299"}}}, 201)
	// the same serial cannot be sold twice
	if r := o.c.do("POST", o.p("/pos/orders"), map[string]any{"session_id": sess, "lines": []any{map[string]any{"item_id": phone, "quantity": "1", "serials": []any{sn1}}},
		"tenders": []any{map[string]any{"method_id": usd, "amount": "299"}}}); r.status != 422 {
		t.Fatalf("serial sold twice: %d %s", r.status, r.raw)
	}
	h := o.c.ok("GET", o.p("/stock/serial-history?serial="+sn1), nil, 200)
	w := obj(h.body["warranty"])
	if w["under_warranty"] != true || str(w["customer_id"]) != cust || int(fl(t, obj(h.body["lots"].([]any)[0])["in_stock"])) != 0 {
		t.Fatalf("serial history: %s", h.raw)
	}
	sold := false
	for _, m := range h.body["moves"].([]any) {
		if str(obj(m)["source"]) == "sale" && str(obj(m)["partner"]) == "Rami" {
			sold = true
		}
	}
	if !sold {
		t.Fatalf("sale not in serial history: %s", h.raw)
	}
	// office invoice with the serial chosen
	wh := ownerQuery(t, `SELECT warehouse_id::text FROM stock WHERE item_id = $1 AND quantity > 0 LIMIT 1`, phone)
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "lines": []any{map[string]any{"item_id": phone, "quantity": "1",
		"unit_price": "299", "warehouse_id": wh, "lots": []any{map[string]any{"lot_no": sn2, "quantity": "1"}}}}}, 201)
	if h2 := o.c.ok("GET", o.p("/stock/serial-history?serial="+sn2), nil, 200); int(fl(t, obj(h2.body["lots"].([]any)[0])["in_stock"])) != 0 {
		t.Fatalf("invoice serial: %s", h2.raw)
	}
	// trade-in: a used phone (serial) taken at 10 as part payment of a charger (15)
	s := o.c.ok("POST", o.p("/pos/orders"), map[string]any{"session_id": sess, "customer_id": cust, "lines": []any{map[string]any{"item_id": charger, "quantity": "1"}},
		"trade_in": []any{map[string]any{"item_id": used, "serial": "USED-777", "value": "10", "note": "screen scratched"}},
		"tenders":  []any{map[string]any{"method_id": usd, "amount": "5"}}}, 201)
	if v := obj(s.body["vertical"]); len(v["tradein"].([]any)) != 1 {
		t.Fatalf("trade-in not stored: %s", s.raw)
	}
	if h3 := o.c.ok("GET", o.p("/stock/serial-history?serial=USED-777"), nil, 200); int(fl(t, obj(h3.body["lots"].([]any)[0])["in_stock"])) != 1 {
		t.Fatalf("trade-in stock: %s", h3.raw)
	}
	// a trade-in worth more than the sale is refused
	if r := o.c.do("POST", o.p("/pos/orders"), map[string]any{"session_id": sess, "customer_id": cust, "lines": []any{map[string]any{"item_id": charger, "quantity": "1"}},
		"trade_in": []any{map[string]any{"item_id": used, "serial": "USED-778", "value": "50"}}}); r.status != 422 {
		t.Fatalf("trade-in over total: %d %s", r.status, r.raw)
	}
	o.stockLedgerEqual(t)
}

func TestFurniturePack(t *testing.T) {
	needDB(t)
	o := applyPack(t, "furniture")
	sofa, table := o.itemByCode(t, "SOFA3"), o.itemByCode(t, "DTBL")
	cfg := o.c.ok("POST", o.p("/furn/configure"), map[string]any{"item_id": sofa, "choices": map[string]any{"Fabric": "Velvet", "Colour": "Navy"}}, 200)
	eqDec(t, "sofa price", cfg.body["unit_price"], "1100")
	if !strings.Contains(str(cfg.body["description"]), "Velvet") {
		t.Fatalf("description: %s", cfg.raw)
	}
	tb := o.c.ok("POST", o.p("/furn/configure"), map[string]any{"item_id": table, "width_cm": "200", "depth_cm": "100"}, 200)
	eqDec(t, "table price", tb.body["unit_price"], "650")
	if r := o.c.do("POST", o.p("/furn/configure"), map[string]any{"item_id": sofa, "choices": map[string]any{"Fabric": "Silk"}}); r.status != 422 {
		t.Fatalf("unknown option: %d", r.status)
	}
	cust := ownerQuery(t, `SELECT id::text FROM customers WHERE company_id = $1 AND name LIKE 'Demo customer%'`, o.company)
	q := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": cust, "currency": "USD",
		"lines": []any{map[string]any{"description": str(cfg.body["description"]), "quantity": "1", "unit_price": "1100"}}}, 201)
	so := str(obj(q.body["order"])["id"])
	o.c.ok("POST", o.p("/sales/orders/"+so+"/confirm"), map[string]any{}, 200)
	list := o.c.ok("GET", o.p("/furn/orders"), nil, 200).list
	if len(list) != 1 {
		t.Fatalf("furniture orders: %d", len(list))
	}
	eqDec(t, "deposit required (30 %)", obj(list[0])["deposit_required"], "330")
	d := o.c.ok("POST", o.p("/furn/orders/"+so+"/deposit"), map[string]any{"method_id": o.methods["Cash USD"], "amount": "400"}, 201)
	eqDec(t, "deposit paid", d.body["deposit_paid"], "400")
	sch := o.c.ok("POST", o.p("/furn/orders/"+so+"/schedule"), map[string]any{"date": "2026-12-01", "slot": "Morning", "assembly": true, "start": "10:00"}, 200)
	if str(sch.body["trip_id"]) == "" || str(sch.body["service_job_id"]) == "" {
		t.Fatalf("schedule: %s", sch.raw)
	}
	if r := o.c.do("POST", o.p("/furn/orders/"+so+"/schedule"), map[string]any{"date": "2026-12-02"}); r.status != 409 {
		t.Fatalf("scheduled twice: %d", r.status)
	}
	stop := ownerQuery(t, `SELECT note FROM trip_stops WHERE trip_id = $1`, str(sch.body["trip_id"]))
	if !strings.Contains(stop, "700.00") {
		t.Fatalf("balance on the stop: %q", stop)
	}
	// the invoice uses the deposit: 700 left
	inv := o.c.ok("POST", o.p("/sales/orders/"+so+"/invoice"), map[string]any{}, 201)
	invID := str(inv.body["invoice_id"])
	eqDec(t, "invoice paid by the deposit", ownerQuery(t, `SELECT paid::text FROM invoices WHERE id = $1`, invID), "400")
	o.c.ok("GET", o.p("/furn/showroom"), nil, 200)
	if n := len(o.c.ok("GET", o.p("/verticals/warehouses"), nil, 200).list); n < 2 {
		t.Fatalf("showroom warehouse: %d", n)
	}
	o.stockLedgerEqual(t)
}

func TestWholesaleVanSales(t *testing.T) {
	needDB(t)
	o := applyPack(t, "wholesale")
	me := str(o.c.ok("GET", "/me", nil, 200).body["id"])
	van := ownerQuery(t, `SELECT id::text FROM warehouses WHERE company_id = $1 AND is_van`, o.company)
	main := ownerQuery(t, `SELECT warehouse_id::text FROM stock s JOIN items i ON i.id = s.item_id WHERE i.company_id = $1 AND s.quantity > 0 LIMIT 1`, o.company)
	o.c.ok("PUT", o.p("/warehouses/"+van+"/vertical"), map[string]any{"van_user_id": me}, 200)
	water, cola := o.itemByCode(t, "WTR-6"), o.itemByCode(t, "COLA-24")
	if r := o.c.do("POST", o.p("/van/sales"), map[string]any{"customer_id": ownerQuery(t, `SELECT id::text FROM customers WHERE company_id = $1 LIMIT 1`, o.company),
		"lines": []any{map[string]any{"item_id": water, "quantity": "1"}}}); r.status != 409 {
		t.Fatalf("sale before the day is open: %d %s", r.status, r.raw)
	}
	day := o.c.ok("POST", o.p("/van/days"), map[string]any{"warehouse_id": van, "source_warehouse_id": main, "lines": []any{
		map[string]any{"item_id": water, "quantity": "40"}, map[string]any{"item_id": cola, "quantity": "20"}}}, 201)
	dayID := str(day.body["id"])
	if r := o.c.do("POST", o.p("/van/days"), map[string]any{"warehouse_id": van, "source_warehouse_id": main, "lines": []any{}}); r.status != 409 {
		t.Fatalf("second open day: %d", r.status)
	}
	routes := o.c.ok("GET", o.p("/van/routes"), nil, 200)
	if len(routes.list) != 1 || fl(t, obj(routes.list[0])["stops"]) != 3 || str(obj(routes.list[0])["warehouse_id"]) != van {
		t.Fatalf("demo route: %s", routes.raw)
	}
	today := o.c.ok("GET", o.p("/van/today"), nil, 200)
	if len(today.body["stops"].([]any)) != 3 || len(today.body["stock"].([]any)) != 2 {
		t.Fatalf("van today: %s", today.raw)
	}
	amal := ownerQuery(t, `SELECT id::text FROM customers WHERE company_id = $1 AND name = 'Al Amal minimarket'`, o.company)
	ref := "van-" + randHex(6)
	sale := map[string]any{"client_ref": ref, "customer_id": amal, "lines": []any{map[string]any{"item_id": water, "quantity": "10"}},
		"method_id": o.methods["Cash USD"], "paid": "10"}
	s1 := o.c.ok("POST", o.p("/van/sales"), sale, 201)
	inv := obj(obj(s1.body["invoice"])["invoice"])
	eqDec(t, "wholesale price, 10+ break", inv["total"], "21.40") // 2.40 × 0.92 × 0.97 = 2.14
	eqDec(t, "paid at the door", inv["paid"], "10")
	s2 := o.c.ok("POST", o.p("/van/sales"), sale, 201) // sent again by the phone
	if str(obj(obj(s2.body["invoice"])["invoice"])["id"]) != str(inv["id"]) {
		t.Fatal("van sale replay created a second invoice")
	}
	// credit limit: 50 → a 160 sale on account is blocked
	o.c.ok("PUT", o.p("/customers/"+amal+"/credit"), map[string]any{"credit_limit": "50"}, 200)
	if r := o.c.do("POST", o.p("/van/sales"), map[string]any{"customer_id": amal, "lines": []any{map[string]any{"item_id": cola, "quantity": "20"}}}); r.status != 409 ||
		!strings.Contains(r.raw, "credit_limit") {
		t.Fatalf("credit limit: %d %s", r.status, r.raw)
	}
	// close: one carton of water missing, the rest goes back
	closed := o.c.ok("POST", o.p("/van/days/"+dayID+"/close"), map[string]any{"counted": []any{map[string]any{"item_id": water, "quantity": "29"},
		map[string]any{"item_id": cola, "quantity": "20"}}, "unload": true}, 200)
	sum := obj(closed.body["summary"])
	if fl(t, sum["sales"]) != 1 || len(sum["differences"].([]any)) != 1 || len(sum["unloaded"].([]any)) != 2 {
		t.Fatalf("van close: %s", closed.raw)
	}
	if left := ownerQuery(t, `SELECT coalesce(sum(quantity), 0)::text FROM stock WHERE warehouse_id = $1`, van); dec(t, left).Sign() != 0 {
		t.Fatalf("van not empty after unload: %s", left)
	}
	com := o.c.ok("GET", o.p("/van/commission?from=2000-01-01&to=2100-01-01"), nil, 200).list
	if len(com) != 1 || str(obj(com[0])["user_id"]) != me {
		t.Fatalf("commission: %v", com)
	}
	eqDec(t, "commission net", obj(com[0])["net"], "21.40")
	o.stockLedgerEqual(t)
}

func TestRestaurantPack(t *testing.T) {
	needDB(t)
	o := applyPack(t, "restaurant")
	reg, sess := o.packTill(t)
	usd := o.methods["Cash USD"]
	floor := o.c.ok("GET", o.p("/rest/floor?register_id="+reg), nil, 200)
	tables := floor.body["tables"].([]any)
	if len(tables) != 10 {
		t.Fatalf("tables: %d", len(tables))
	}
	t1 := str(obj(tables[0])["id"])
	shw, cola := o.itemByCode(t, "SHW"), o.itemByCode(t, "COLA")
	var cheese string
	for _, m := range o.c.ok("GET", o.p("/rest/modifiers?item_id="+shw), nil, 200).list {
		if str(obj(m)["name"]) == "Extra cheese" {
			cheese = str(obj(m)["id"])
		}
	}
	k := o.c.ok("POST", o.p("/rest/checks"), map[string]any{"register_id": reg, "table_id": t1, "guests": 2}, 201)
	check := str(obj(k.body["check"])["id"])
	if r := o.c.do("POST", o.p("/rest/checks"), map[string]any{"register_id": reg, "table_id": t1}); r.status != 409 {
		t.Fatalf("busy table: %d", r.status)
	}
	o.c.ok("POST", o.p("/rest/checks/"+check+"/lines"), map[string]any{"lines": []any{
		map[string]any{"item_id": shw, "quantity": "2", "modifier_ids": []any{cheese}, "note": "well done"},
		map[string]any{"item_id": cola, "quantity": "2"}}}, 200)
	c1 := o.c.ok("POST", o.p("/rest/checks/"+check+"/send"), map[string]any{}, 200)
	eqDec(t, "check total", obj(c1.body["check"])["total"], "21") // (8 + 1) × 2 + 1.5 × 2
	kds := o.c.ok("GET", o.p("/rest/kds?station=grill"), nil, 200)
	lines := kds.body["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("grill KDS: %s", kds.raw)
	}
	gl := str(obj(lines[0])["id"])
	if r := o.c.do("POST", o.p("/rest/check-lines/"+gl+"/status"), map[string]any{"status": "served"}); r.status != 409 {
		t.Fatalf("sent → served: %d", r.status)
	}
	for _, st := range []string{"cooking", "ready", "served"} {
		o.c.ok("POST", o.p("/rest/check-lines/"+gl+"/status"), map[string]any{"status": st}, 200)
	}
	if n := len(o.c.ok("GET", o.p("/rest/kds?station=grill"), nil, 200).body["lines"].([]any)); n != 0 {
		t.Fatalf("served line still on the KDS: %d", n)
	}
	// split the drinks to a second check and pay it
	var colaLine string
	for _, l := range c1.body["lines"].([]any) {
		if str(obj(l)["item_id"]) == cola {
			colaLine = str(obj(l)["id"])
		}
	}
	sp := o.c.ok("POST", o.p("/rest/checks/"+check+"/split"), map[string]any{"line_ids": []any{colaLine}}, 201)
	check2 := str(obj(sp.body["check"])["id"])
	o.c.ok("POST", o.p("/rest/checks/"+check2+"/pay"), map[string]any{"session_id": sess, "tenders": []any{map[string]any{"method_id": usd, "amount": "3"}}}, 200)
	// main check: 10 % service and a 2 tip → 18 + 1.80 + 2 = 21.80
	o.c.ok("PUT", o.p("/rest/settings"), map[string]any{"service_charge_pct": "10"}, 200)
	paid := o.c.ok("POST", o.p("/rest/checks/"+check+"/pay"), map[string]any{"session_id": sess, "service_charge": true, "tip": "2",
		"client_ref": "chk-" + randHex(6), "tenders": []any{map[string]any{"method_id": usd, "amount": "21.80"}}}, 200)
	if str(obj(paid.body["check"])["status"]) != "paid" {
		t.Fatalf("check not closed: %s", paid.raw)
	}
	eqDec(t, "till total", obj(obj(paid.body["pos_order"])["order"])["total"], "21.80")
	if r := o.c.do("POST", o.p("/rest/checks/"+check+"/lines"), map[string]any{"lines": []any{map[string]any{"item_id": cola, "quantity": "1"}}}); r.status != 409 {
		t.Fatalf("line on a paid check: %d", r.status)
	}
	// ingredients left stock: 2 plates × 0.25 kg chicken
	chk := o.itemByCode(t, "ING-CHK")
	eqDec(t, "chicken left", ownerQuery(t, `SELECT sum(quantity)::text FROM stock WHERE item_id = $1`, chk), "39.5")
	fc := o.c.ok("GET", o.p("/rest/food-cost?from=2000-01-01&to=2100-01-01"), nil, 200)
	days := fc.body["days"].([]any)
	if len(days) != 1 || fl(t, obj(days[0])["food_cost"]) <= 0 {
		t.Fatalf("food cost: %s", fc.raw)
	}
	// tips are owed to the staff, not revenue
	tips := ownerQuery(t, `SELECT coalesce(sum(l.base_credit - l.base_debit), 0)::text FROM journal_lines l JOIN companies c ON c.tips_liability_account_id = l.account_id
		WHERE c.id = $1`, o.company)
	eqDec(t, "tips owed", tips, "2")
	// cancel an empty check, void a sent line
	k3 := o.c.ok("POST", o.p("/rest/checks"), map[string]any{"register_id": reg, "kind": "takeaway", "name": "Walk-in"}, 201)
	c3 := str(obj(k3.body["check"])["id"])
	v := o.c.ok("POST", o.p("/rest/checks/"+c3+"/lines"), map[string]any{"send": true, "lines": []any{map[string]any{"item_id": cola, "quantity": "1"}}}, 200)
	o.c.ok("POST", o.p("/rest/check-lines/"+str(obj(v.body["lines"].([]any)[0])["id"])+"/void"), map[string]any{"reason": "changed mind"}, 200)
	o.c.ok("POST", o.p("/rest/checks/"+c3+"/cancel"), map[string]any{}, 200)
	o.stockLedgerEqual(t)
}

func TestPharmacyPack(t *testing.T) {
	needDB(t)
	o := applyPack(t, "pharmacy")
	_, sess := o.packTill(t)
	usd := o.methods["Cash USD"]
	pan, amox, tram := o.itemByCode(t, "PAN500"), o.itemByCode(t, "AMOX500"), o.itemByCode(t, "TRAM50")
	if n := len(o.c.ok("GET", o.p("/pharma/search?q=paracetamol"), nil, 200).list); n != 2 {
		t.Fatalf("search by ingredient: %d", n)
	}
	alt := o.c.ok("GET", o.p("/pharma/alternatives?item_id="+pan), nil, 200).list
	if len(alt) != 1 || str(obj(alt[0])["name"]) != "Fevadol 500 mg" {
		t.Fatalf("alternatives: %v", alt)
	}
	// prescription drugs need the prescription
	sale := func(item, qty, amount string, extra map[string]any) resp {
		body := map[string]any{"session_id": sess, "lines": []any{map[string]any{"item_id": item, "quantity": qty}},
			"tenders": []any{map[string]any{"method_id": usd, "amount": amount}}}
		for k, v := range extra {
			body[k] = v
		}
		return o.c.do("POST", o.p("/pos/orders"), body)
	}
	if r := sale(amox, "1", "7", nil); r.status != 422 || !strings.Contains(r.raw, "prescription_required") {
		t.Fatalf("rx refusal: %d %s", r.status, r.raw)
	}
	rx := map[string]any{"doctor": "Dr. Haddad", "number": "RX-1", "patient": "Nour"}
	if r := sale(amox, "1", "7", map[string]any{"rx": rx}); r.status != 201 {
		t.Fatalf("rx sale: %d %s", r.status, r.raw)
	}
	if r := sale(tram, "1", "9", map[string]any{"rx": rx}); r.status != 201 {
		t.Fatalf("controlled sale: %d %s", r.status, r.raw)
	}
	if n := len(o.c.ok("GET", o.p("/pharma/controlled?from=2000-01-01&to=2100-01-01"), nil, 200).list); n != 1 {
		t.Fatalf("controlled register: %d", n)
	}
	// insurance pays 80 % of 9.00
	insurer := ownerQuery(t, `SELECT id::text FROM customers WHERE company_id = $1 AND name LIKE 'Demo Insurance%'`, o.company)
	if r := sale(pan, "2", "1.80", map[string]any{"insurance": map[string]any{"insurer_id": insurer, "pct": "80", "policy_no": "P-9"}}); r.status != 201 {
		t.Fatalf("insured sale: %d %s", r.status, r.raw)
	}
	cl := o.c.ok("GET", o.p("/pharma/claims?status=open"), nil, 200)
	claims := cl.body["claims"].([]any)
	if len(claims) != 1 {
		t.Fatalf("claims: %s", cl.raw)
	}
	eqDec(t, "claim", obj(claims[0])["amount"], "7.20")
	claim := str(obj(claims[0])["id"])
	o.c.ok("POST", o.p("/pharma/claims/"+claim+"/submit"), map[string]any{}, 200)
	if st := str(o.c.ok("POST", o.p("/pharma/claims/"+claim+"/settle"), map[string]any{"method_id": usd}, 200).body["status"]); st != "paid" {
		t.Fatalf("claim status %s", st)
	}
	ins := ownerQuery(t, `SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::text FROM journal_lines l JOIN companies c ON c.insurance_account_id = l.account_id
		WHERE c.id = $1`, o.company)
	eqDec(t, "insurance receivable after payment", ins, "0")
	// an expired lot is never sold
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Pharma depot", "currency": "USD"}, 201).body["id"])
	old := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "OLD", "name": "Old syrup", "kind": "stock", "sale_price": "3", "sale_currency": "USD"}, 201).body["id"])
	o.c.ok("PUT", o.p("/items/"+old+"/stock-settings"), map[string]any{"tracking": "lot"}, 200)
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "currency": "USD", "lines": []any{map[string]any{"item_id": old, "quantity": "5", "unit_price": "1",
		"lots": []any{map[string]any{"lot_no": "EXP1", "quantity": "5", "expiry": "2025-01-31"}}}}}, 201)
	if r := sale(old, "1", "3", nil); r.status != 422 {
		t.Fatalf("expired lot sold: %d %s", r.status, r.raw)
	}
	ex := o.c.ok("GET", o.p("/pharma/expiry"), nil, 200)
	if fl(t, ex.body["expired"]) != 1 || fl(t, ex.body["expiring"]) < 1 || ex.body["block_expired"] != true {
		t.Fatalf("expiry board: %s", ex.raw)
	}
	o.stockLedgerEqual(t)
}
