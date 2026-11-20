package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Module Q: company isolation inside one organisation, checked on EVERY route.
//
// Company B is filled with one (or more) of everything. Then, for every registered route that takes an
// id, the request is sent under company A's URL with company B's ids, by the unlimited owner and by a
// user limited to company A. Nothing may succeed, no response may contain company B's data, and company
// B must be byte-for-byte unchanged afterwards. The same calls are then made legitimately under B to show
// which of them really do something (so a 4xx in the attack is not just a validation error).

const secretB = "ZZSECRETB"

// seedEverything creates at least one row of every company table in o's company. Failures are reported
// but do not stop the test (the route sweep still runs with a random id for what is missing).
func seedEverything(t *testing.T, o *org) {
	t.Helper()
	c := o.c
	step := func(want int, method, path string, body any) resp {
		t.Helper()
		r := c.do(method, o.p(path), body)
		if r.status != want {
			t.Errorf("seed %s %s: %d (want %d) %s", method, path, r.status, want, r.raw)
		}
		return r
	}
	id := func(r resp, keys ...string) string {
		m := r.body
		for _, k := range keys[:len(keys)-1] {
			m = obj(m[k])
		}
		return str(m[keys[len(keys)-1]])
	}
	usd, lbp, bank := o.methods["Cash USD"], o.methods["Cash LBP"], o.methods["Bank"]
	o.rate(t, "LBP", "2026-01-01", "89500")
	vat := o.vat(t)
	step(201, "POST", "/tax-rates", map[string]any{"name": secretB + " tax", "rate": "5", "account_id": o.accounts["4427"], "purchase_account_id": o.accounts["4426"]})
	area := id(step(201, "POST", "/areas", map[string]any{"name": secretB + " area"}), "id")
	plan := o.plan(t, secretB+" plan", "20")
	metered := id(step(201, "POST", "/plans", map[string]any{"name": secretB + " meter", "billing_type": "metered", "currency": "USD",
		"base_fee": "5", "unit_price": "0.5"}), "id")
	station := id(step(201, "POST", "/stations", map[string]any{"name": secretB + " station"}), "id")
	box := id(step(201, "POST", "/boxes", map[string]any{"station_id": station, "code": secretB + "-1"}), "id")
	gen := id(step(201, "POST", "/generators", map[string]any{"station_id": station, "name": secretB + " gen", "kva": "500"}), "id")
	step(200, "POST", "/generators/"+gen+"/hours", map[string]any{"date": "2026-11-01", "hour_meter": "1000"})
	step(200, "POST", "/generators/"+gen+"/outages", map[string]any{"started_at": "2026-11-10T10:00:00Z", "ended_at": "2026-11-10T12:30:00Z", "reason": secretB})
	router := id(step(201, "POST", "/routers", map[string]any{"name": secretB + " router", "host": "203.0.113.9", "port": 8728,
		"username": "admin", "password": "secret-pw", "enabled": false}), "id")
	cust := id(step(201, "POST", "/customers", map[string]any{"name": secretB + " customer", "area_id": area, "phone": "70111222", "email": "b@example.com"}), "id")
	cust2 := o.customer(t, secretB+" second", "")
	_ = id(step(201, "POST", "/customers/"+cust+"/subscriptions", map[string]any{"plan_id": plan, "start_date": "2026-01-01", "box_id": box}), "id")
	msub := id(step(201, "POST", "/customers/"+cust2+"/subscriptions", map[string]any{"plan_id": metered, "start_date": "2026-01-01", "router_id": router,
		"network_username": "bnet"}), "id")
	step(200, "POST", "/readings", map[string]any{"period": "2026-11", "readings": []any{map[string]any{"subscription_id": msub, "reading": "100"}}})
	step(200, "POST", "/billing/run", map[string]any{"period": "2026-11", "issue_date": "2026-11-05"})
	step(200, "POST", "/tariffs", map[string]any{"period": "2026-08", "currency": "USD", "price_per_kwh": "0.4"})

	inv := step(201, "POST", "/invoices", map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-02",
		"lines": []any{map[string]any{"description": secretB + " line", "quantity": "1", "unit_price": "100", "tax_rate_id": vat}}})
	invID := id(inv, "invoice", "id")
	step(201, "POST", "/payments", map[string]any{"customer_id": cust, "date": "2026-11-03", "currency": "USD",
		"tenders": []any{map[string]any{"method_id": usd, "amount": "30"}}})
	step(201, "POST", "/invoices/"+invID+"/credit-note", map[string]any{"date": "2026-11-04", "amount": "5", "reason": secretB})
	step(201, "POST", "/cash/transfers", map[string]any{"from_method_id": usd, "to_method_id": bank, "amount": "10", "date": "2026-11-04"})
	step(201, "POST", "/journal", map[string]any{"date": "2026-11-01", "memo": secretB + " draft", "lines": []any{
		map[string]any{"account_id": o.accounts["512"], "debit": "1"}, map[string]any{"account_id": o.accounts["101"], "credit": "1"}}})
	step(201, "POST", "/journal", map[string]any{"date": "2026-11-01", "post": true, "memo": secretB + " capital", "lines": []any{
		map[string]any{"account_id": o.accounts["512"], "debit": "1000"}, map[string]any{"account_id": o.accounts["101"], "credit": "1000"}}})

	sup := id(step(201, "POST", "/suppliers", map[string]any{"name": secretB + " supplier", "currency": "USD"}), "id")
	cat := id(step(201, "POST", "/pos/categories", map[string]any{"name": secretB + " cat", "color": "blue"}), "id")
	item := id(step(201, "POST", "/items", map[string]any{"code": "BITM", "name": secretB + " item", "kind": "stock", "sale_price": "5",
		"sale_currency": "USD", "barcode": "6229999999999", "category_id": cat, "prices": []any{map[string]any{"currency": "LBP", "price": "450000"}}}), "id")
	wh2 := id(step(201, "POST", "/warehouses", map[string]any{"name": secretB + " wh"}), "id")
	bill := step(201, "POST", "/bills", map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
		"lines": []any{map[string]any{"item_id": item, "quantity": "100", "unit_price": "1"}}})
	billID := id(bill, "bill", "id")
	if billID == "" {
		billID = id(bill, "id")
	}
	step(201, "POST", "/supplier-payments", map[string]any{"supplier_id": sup, "date": "2026-11-02", "method_id": usd, "paid_amount": "10", "currency": "USD"})
	step(201, "POST", "/expenses", map[string]any{"date": "2026-11-05", "account_id": o.accounts["611"], "method_id": bank, "amount": "7", "description": secretB})
	step(201, "POST", "/stock/fuel", map[string]any{"item_id": item, "quantity": "3", "date": "2026-11-02", "generator_id": gen})
	step(201, "POST", "/stock/adjust", map[string]any{"item_id": item, "quantity": "2", "date": "2026-11-03"})
	main := ownerQuery(t, `SELECT warehouse_id::text FROM stock WHERE item_id = $1 AND quantity > 0 LIMIT 1`, item)
	if main == wh2 {
		wh2 = id(step(201, "POST", "/warehouses", map[string]any{"name": secretB + " wh2"}), "id")
	}
	step(201, "POST", "/stock/transfers", map[string]any{"item_id": item, "from_warehouse_id": main, "to_warehouse_id": wh2, "quantity": "5", "date": "2026-11-04"})
	step(201, "POST", "/bills/"+billID+"/debit-note", map[string]any{"date": "2026-11-06", "amount": "1", "reason": secretB})

	q := step(201, "POST", "/sales/orders", map[string]any{"customer_id": cust, "currency": "USD", "quote_date": "2026-11-02",
		"lines": []any{map[string]any{"item_id": item, "quantity": "2", "unit_price": "5"}}})
	oid := id(q, "order", "id")
	step(200, "POST", "/sales/orders/"+oid+"/confirm", map[string]any{"date": "2026-11-03"})
	step(201, "POST", "/sales/orders/"+oid+"/deliver", map[string]any{"date": "2026-11-04"})
	stages := c.ok("GET", o.p("/crm/stages"), nil, 200).list
	_ = stages
	lead := id(step(201, "POST", "/crm/leads", map[string]any{"title": secretB + " lead", "contact_name": "B", "expected_revenue": "10", "currency": "USD"}), "lead", "id")
	step(201, "POST", "/crm/activities", map[string]any{"lead_id": lead, "kind": "call", "summary": secretB + " call", "due_date": "2026-11-20"})
	step(201, "POST", "/crm/stages", map[string]any{"name": secretB + " stage"})

	reg := id(step(201, "POST", "/pos/registers", map[string]any{"name": secretB + " till", "currency": "USD", "method_ids": []any{usd, lbp}}), "id")
	sess := id(step(201, "POST", "/pos/sessions", map[string]any{"register_id": reg, "opening": []any{map[string]any{"method_id": usd, "amount": "50"}}}), "session", "id")
	step(201, "POST", "/pos/orders", map[string]any{"client_ref": "pxv-" + randHex(8), "session_id": sess,
		"lines": []any{map[string]any{"item_id": item, "quantity": "1"}}, "tenders": []any{map[string]any{"method_id": usd, "amount": "10"}}, "change_method_id": usd})

	emp := id(step(201, "POST", "/employees", map[string]any{"name": secretB + " emp", "currency": "USD", "salary": "1000", "hire_date": "2026-01-01"}), "id")
	step(201, "POST", "/employees/"+emp+"/advances", map[string]any{"date": "2026-10-05", "amount": "300", "installment": "100", "method_id": usd})
	step(201, "POST", "/payroll", map[string]any{"period": "2026-11"})
	step(201, "POST", "/assets", map[string]any{"name": secretB + " asset", "purchase_date": "2026-01-15", "currency": "USD",
		"cost": "1200", "useful_life_months": 12, "asset_account_id": o.accounts["213"], "paid_from_method_id": bank})
	step(200, "POST", "/depreciation/run", map[string]any{"period": "2026-01"})
	step(201, "POST", "/recurring", map[string]any{"name": secretB + " rent", "kind": "journal", "frequency": "monthly", "next_date": "2027-02-01",
		"currency": "USD", "lines": []any{map[string]any{"account_id": o.accounts["611"], "debit": "5"}, map[string]any{"account_id": o.accounts["531"], "credit": "5"}}})
	step(201, "POST", "/bank/statements", map[string]any{"method_id": bank, "name": secretB + " stmt", "lines": []any{
		map[string]any{"date": "2026-11-02", "description": secretB + " fee", "amount": "-5"}}})

	step(201, "POST", "/message-providers", map[string]any{"kind": "log", "name": secretB + " log", "is_default": true})
	tpl := id(step(201, "POST", "/message-templates", map[string]any{"event": "custom", "locale": "en", "name": secretB + " tpl", "body": secretB + " hello"}), "id")
	step(200, "POST", "/messages/send", map[string]any{"customer_ids": []any{cust}, "template_id": tpl})
	step(201, "POST", "/webhooks", map[string]any{"url": "https://hooks.example.com/" + secretB, "events": []string{"customer.created"}})
	// an approval request: approvals required for voids, asked by a data-entry user
	step(200, "PUT", "/approval-rules", map[string]any{"void": true})
	email := "de" + randHex(4) + "@example.com"
	c.ok("POST", "/users", map[string]any{"email": email, "full_name": "DE", "role": "accountant", "password": "de-secret-pass-9"}, 201)
	de := loginAs(t, email, "de-secret-pass-9")
	if r := de.do("POST", o.p("/invoices/"+invID+"/void"), map[string]any{"reason": secretB}); r.status != 202 {
		t.Errorf("seed approval request: %d %s", r.status, r.raw)
	}
	step(200, "PUT", "/approval-rules", map[string]any{"void": false})

	// module S: cost center, budget, currency exchange, cheque
	cc := id(step(201, "POST", "/cost-centers", map[string]any{"code": "BCC", "name": secretB + " cc", "station_id": station}), "id")
	step(200, "PUT", "/budgets", map[string]any{"year": 2026, "cost_center_id": cc, "lines": []any{
		map[string]any{"account_id": o.accounts["701"], "month": 11, "amount": "50"}}})
	step(201, "POST", "/exchanges", map[string]any{"date": "2026-11-05", "from_method_id": usd, "to_method_id": lbp, "from_amount": "10",
		"to_amount": "900000", "counterparty": secretB})
	step(201, "POST", "/cheques", map[string]any{"direction": "in", "cheque_no": "B1", "bank_name": secretB, "receive_date": "2026-11-05",
		"due_date": "2026-12-05", "currency": "USD", "amount": "5", "customer_id": cust})

	// final stage: service desk ticket and generator maintenance task
	step(201, "POST", "/tickets", map[string]any{"customer_id": cust, "subject": secretB + " outage", "category": "outage"})
	mt := id(step(201, "POST", "/maintenance/tasks", map[string]any{"generator_id": gen, "task": secretB + " oil", "every_hours": "250"}), "id")
	step(201, "POST", "/maintenance/log", map[string]any{"plan_id": mt, "done_date": "2026-11-05", "hour_meter": "100"})

	// Odoo audit stage: purchase order, deferral, attachment, dunning level
	po := id(step(201, "POST", "/purchase-orders", map[string]any{"supplier_id": sup, "currency": "USD", "notes": secretB,
		"lines": []any{map[string]any{"item_id": item, "quantity": "5", "unit_price": "1"}}}), "order", "id")
	step(201, "POST", "/deferrals", map[string]any{"kind": "revenue", "name": secretB + " yearly", "amount": "120", "months": 12,
		"start_period": "2026-11", "target_account_id": o.accounts["701"], "deferral_account_id": o.accounts["487"]})
	step(201, "POST", "/attachments", map[string]any{"type": "invoice", "id": invID, "filename": secretB + ".txt", "data": "aGVsbG8="})
	step(200, "PUT", "/dunning", map[string]any{"enabled": false, "levels": []any{map[string]any{"level": 1, "name": secretB, "days_overdue": 3}}})

	// finishing stage: goods receipt, supplier price, expense claim, custom field, BOM + manufacturing order, rotation,
	// device, payment gateway, e-invoice settings
	step(200, "POST", "/purchase-orders/"+po+"/confirm", map[string]any{})
	step(201, "POST", "/purchase-orders/"+po+"/receive", map[string]any{"delivery_ref": secretB, "lines": []any{}})
	step(201, "POST", "/supplier-prices", map[string]any{"supplier_id": sup, "item_id": item, "currency": "USD", "price": "0.9", "supplier_code": secretB})
	step(201, "POST", "/hr/claims", map[string]any{"employee_id": emp, "description": secretB + " taxi", "currency": "USD", "amount": "12"})
	step(201, "POST", "/custom-fields", map[string]any{"entity": "customers", "key": "meter_color", "label": secretB, "type": "text"})
	comp := id(step(201, "POST", "/items", map[string]any{"code": "BCMP", "name": secretB + " part", "kind": "stock"}), "id")
	bom := id(step(201, "POST", "/mfg/boms", map[string]any{"item_id": item, "name": secretB + " bom", "lines": []any{map[string]any{"item_id": comp, "quantity": "2"}}}), "bom", "id")
	step(201, "POST", "/mfg/orders", map[string]any{"bom_id": bom, "quantity": "1", "notes": secretB})
	// manufacturing pro: work center, routing, quality point, BoM with routing and by-product, confirmed order (work order,
	// check, reservation), a running timer and a quality alert
	bwc := id(step(201, "POST", "/mfg/workcenters", map[string]any{"code": "BWC", "name": secretB + " cell", "cost_per_hour": "10"}), "id")
	brt := id(step(201, "POST", "/mfg/routings", map[string]any{"name": secretB + " route", "operations": []any{
		map[string]any{"name": secretB + " op", "workcenter_id": bwc, "minutes": "5"}}}), "routing", "id")
	step(201, "POST", "/mfg/quality-points", map[string]any{"name": secretB + " point", "trigger_on": "operation", "workcenter_id": bwc})
	bfg := id(step(201, "POST", "/items", map[string]any{"code": "BFG", "name": secretB + " product", "kind": "stock"}), "id")
	bbyp := id(step(201, "POST", "/items", map[string]any{"code": "BBYP", "name": secretB + " offcut", "kind": "stock"}), "id")
	rbom := id(step(201, "POST", "/mfg/boms", map[string]any{"item_id": bfg, "name": secretB + " routed", "routing_id": brt,
		"lines": []any{map[string]any{"item_id": comp, "quantity": "1"}}, "byproducts": []any{map[string]any{"item_id": bbyp, "quantity": "1", "cost_share": "5"}}}), "bom", "id")
	rmo := step(201, "POST", "/mfg/orders", map[string]any{"bom_id": rbom, "quantity": "2", "notes": secretB, "confirm": true})
	if wos, ok := rmo.body["workorders"].([]any); ok && len(wos) > 0 {
		step(200, "POST", "/mfg/workorders/"+str(obj(wos[0])["id"])+"/start", map[string]any{})
	}
	step(201, "POST", "/mfg/quality-alerts", map[string]any{"title": secretB + " alert", "root_cause": secretB})
	sch := id(step(201, "POST", "/hr/schedules", map[string]any{"name": secretB + " shift", "start_time": "08:00", "end_time": "16:00"}), "id")
	step(201, "POST", "/hr/rotations", map[string]any{"name": secretB + " rota", "pattern": []any{sch, "off"}})
	step(201, "POST", "/hr/devices", map[string]any{"serial": "BDEV" + randHex(4), "name": secretB})
	step(201, "POST", "/payment-gateways", map[string]any{"kind": "transfer", "name": secretB + " whish", "method_id": usd})
	step(200, "PUT", "/einvoice/config", map[string]any{"kind": "zatca", "env": "sandbox", "config": map[string]any{"seller_name": secretB}})

	// retail core: promotion, voucher, held basket, loyalty program and card, variant template
	step(201, "POST", "/retail/promotions", map[string]any{"name": secretB + " promo", "kind": "discount", "value": "10"})
	step(201, "POST", "/retail/gift-cards", map[string]any{"amount": "5", "currency": "USD", "note": secretB})
	step(201, "POST", "/pos/held", map[string]any{"register_id": reg, "label": secretB, "payload": map[string]any{"note": secretB}})
	step(200, "PUT", "/retail/loyalty", map[string]any{"is_active": true, "earn_per": "1", "point_value": "0.01"})
	step(200, "PUT", "/retail/loyalty/customers/"+cust, map[string]any{"card": "B" + randHex(5)})
	vtpl := id(step(201, "POST", "/items", map[string]any{"code": "BTPL", "name": secretB + " shirt", "kind": "stock"}), "id")
	step(200, "PUT", "/items/"+vtpl+"/variant-setup", map[string]any{"attributes": []any{map[string]any{"name": "Size", "values": []any{"S", "M"}}}})
	step(200, "POST", "/items/"+vtpl+"/variants/generate", map[string]any{})

	// Odoo gaps: repair order, instalment plan, quotation template, fiscal position, chatter comment, activity, saved analysis
	rep := id(step(201, "POST", "/repairs", map[string]any{"customer_id": cust, "device": secretB + " phone", "fault": secretB + " broken"}), "repair", "id")
	step(200, "PUT", "/repairs/"+rep+"/lines", map[string]any{"lines": []any{map[string]any{"kind": "labour", "description": secretB, "quantity": "1", "unit_price": "5"}}})
	step(201, "POST", "/instalments", map[string]any{"customer_id": cust, "currency": "USD", "months": 2, "notes": secretB,
		"lines": []any{map[string]any{"description": secretB + " tv", "quantity": "1", "unit_price": "100"}}})
	step(201, "POST", "/quote-templates", map[string]any{"name": secretB + " tpl", "lines": []any{}})
	step(201, "POST", "/fiscal-positions", map[string]any{"name": secretB + " export", "kind": "export"})
	step(201, "POST", "/chatter", map[string]any{"type": "customer", "id": cust, "body": secretB + " note"})
	step(201, "POST", "/record-activities", map[string]any{"entity_type": "customer", "entity_id": cust, "summary": secretB + " call"})
	step(201, "POST", "/analysis/reports", map[string]any{"name": secretB + " pivot", "spec": map[string]any{"source": "sales", "measure": "amount"}})

	// warehouse pro: bins, putaway rule, operation, package, batch, vehicle, trip and stop
	bwh := str(obj(o.c.ok("GET", o.p("/wh/warehouses"), nil, 200).list[0])["id"])
	step(200, "PATCH", "/wh/warehouses/"+bwh, map[string]any{"use_bins": true, "address": secretB})
	step(201, "POST", "/wh/locations/generate", map[string]any{"warehouse_id": bwh, "zone": "S", "aisles": 1, "shelves": 1, "bins": 1})
	var bbin string
	for _, l := range o.c.ok("GET", o.p("/wh/locations?warehouse_id="+bwh), nil, 200).list {
		if str(obj(l)["code"]) == "S-01-1-01" {
			bbin = str(obj(l)["id"])
		}
	}
	step(201, "POST", "/wh/putaway-rules", map[string]any{"warehouse_id": bwh, "item_id": item, "location_id": bbin})
	bop := id(step(201, "POST", "/wh/ops", map[string]any{"warehouse_id": bwh, "notes": secretB,
		"lines": []any{map[string]any{"item_id": item, "quantity": "1", "to_location_id": bbin}}}), "op", "id")
	_ = bop
	step(201, "POST", "/wh/packages", map[string]any{"warehouse_id": bwh, "kind": "pallet", "notes": secretB})
	// a pick operation to batch: an order confirmed in a 2-step delivery warehouse
	step(200, "PATCH", "/wh/warehouses/"+bwh, map[string]any{"delivery_steps": 2})
	bso := id(step(201, "POST", "/sales/orders", map[string]any{"customer_id": cust, "currency": "USD",
		"lines": []any{map[string]any{"item_id": item, "description": secretB, "quantity": "1", "unit_price": "1", "warehouse_id": bwh}}}), "order", "id")
	step(200, "POST", "/sales/orders/"+bso+"/confirm", map[string]any{})
	for _, op := range o.c.ok("GET", o.p("/wh/ops?kind=pick"), nil, 200).list {
		step(201, "POST", "/wh/batches", map[string]any{"warehouse_id": bwh, "op_ids": []string{str(obj(op)["id"])}, "notes": secretB})
	}
	bveh := id(step(201, "POST", "/wh/vehicles", map[string]any{"name": secretB + " van"}), "id")
	step(201, "POST", "/wh/trips", map[string]any{"vehicle_id": bveh, "notes": secretB, "stops": []any{map[string]any{"address": secretB + " street"}}})
}

// companyIDs: one id of company cid for each company table (owner connection, no row security).
func companyIDs(t *testing.T, cid string) map[string]string {
	t.Helper()
	ctx := context.Background()
	rows, err := itOwnerPool.Query(ctx, `SELECT c.relname FROM pg_class c JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE c.relkind = 'r' AND a.attname = 'company_id' AND c.relnamespace = 'public'::regnamespace
		  AND EXISTS (SELECT 1 FROM pg_attribute i WHERE i.attrelid = c.oid AND i.attname = 'id')`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	out := map[string]string{}
	for _, tb := range tables {
		var id string
		// prefer a row that can still be acted on (not voided/closed) so the legit replay shows coverage
		_ = itOwnerPool.QueryRow(ctx, fmt.Sprintf(`SELECT id::text FROM %q WHERE company_id = $1 ORDER BY id LIMIT 1`, tb), cid).Scan(&id)
		if id != "" {
			out[tb] = id
		}
	}
	return out
}

// fingerprint of everything company cid owns (row security does not apply to the owner connection).
func companyFingerprint(t *testing.T, cid string) string {
	t.Helper()
	ctx := context.Background()
	rows, err := itOwnerPool.Query(ctx, `SELECT c.relname FROM pg_class c JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE c.relkind = 'r' AND a.attname = 'company_id' AND c.relnamespace = 'public'::regnamespace ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var sb strings.Builder
	for _, tb := range tables {
		var n int
		var h string
		if err := itOwnerPool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*), coalesce(md5(string_agg(to_jsonb(t)::text, '|' ORDER BY to_jsonb(t)::text)), '')
			FROM %q t WHERE company_id = $1`, tb), cid).Scan(&n, &h); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "%s:%d:%s\n", tb, n, h)
	}
	var h string
	_ = itOwnerPool.QueryRow(ctx, `SELECT md5(to_jsonb(c)::text) FROM companies c WHERE id = $1`, cid).Scan(&h)
	sb.WriteString("companies:" + h)
	return sb.String()
}

// segment of the URL (just before a parameter) -> table holding that id
var idTable = map[string]string{
	"accounts": "accounts", "journal": "journal_entries", "payment-methods": "payment_methods", "areas": "areas",
	"plans": "plans", "customers": "customers", "subscriptions": "subscriptions", "invoices": "invoices",
	"payments": "payments", "jobs": "billing_jobs", "bills": "bills", "docs": "stock_docs", "transfers": "stock_transfers",
	"stations": "stations", "generators": "generators", "outages": "outages", "boxes": "boxes", "routers": "routers",
	"router-actions": "router_actions", "tariffs": "tariffs", "tax-rates": "tax_rates", "message-providers": "message_providers",
	"message-templates": "message_templates", "messages": "messages", "orders": "sales_orders", "deliveries": "sales_deliveries",
	"stages": "crm_stages", "leads": "crm_leads", "activities": "crm_activities", "registers": "pos_registers",
	"categories": "item_categories", "sessions": "pos_sessions", "suppliers": "suppliers", "items": "items",
	"supplier-payments": "supplier_payments", "expenses": "expenses", "employees": "employees", "payroll": "payroll_runs",
	"payslips": "payslips", "assets": "assets", "recurring": "recurring_templates", "statements": "bank_statements",
	"lines": "bank_statement_lines", "approvals": "approval_requests", "webhooks": "webhooks",
	"webhook-deliveries": "webhook_deliveries", "ledger": "accounts",
	"cost-centers": "cost_centers", "journal-lines": "journal_lines", "exchanges": "currency_exchanges", "cheques": "cheques",
	"lots": "stock_lots", "counts": "stock_counts", "price-lists": "price_lists",
	"documents": "employee_documents", "schedules": "work_schedules", "holidays": "public_holidays", "leave-types": "leave_types",
	"leave": "leave_requests", "tickets": "tickets", "tasks": "maintenance_plans",
	"purchase-orders": "purchase_orders", "deferrals": "deferral_schedules", "attachments": "attachments",
	"goods-receipts": "goods_receipts", "supplier-prices": "supplier_prices", "claims": "expense_claims", "rotations": "shift_rotations",
	"devices": "attendance_devices", "custom-fields": "custom_fields", "boms": "boms", "payment-gateways": "payment_gateways",
	"payment-intents": "payment_intents", "attendance": "attendance",
	"promotions": "promotions", "gift-cards": "gift_cards", "held": "pos_held_baskets",
	"repairs": "repair_orders", "instalments": "instalment_plans", "quote-templates": "quote_templates", "fiscal-positions": "fiscal_positions",
	"chatter": "record_messages", "record-activities": "record_activities", "reports": "analysis_reports",
	"warehouses": "warehouses", "locations": "wh_locations", "putaway-rules": "wh_putaway_rules", "ops": "wh_ops", "packages": "wh_packages", "batches": "wh_batches",
	"vehicles": "vehicles", "trips": "delivery_trips", "stops": "trip_stops",
	"workcenters": "mfg_workcenters", "routings": "mfg_routings", "workorders": "mfg_workorders", "quality-points": "mfg_quality_points",
	"quality-checks": "mfg_quality_checks", "quality-alerts": "mfg_quality_alerts",
}

var paramRe = regexp.MustCompile(`\{([a-z]+)\}`)

var paramValues = map[string][]string{
	"type":   {"customers", "subscriptions", "opening_balances", "readings", "accounts"},
	"kind":   {"fuel", "adjust"},
	"action": {"confirm", "cancel", "send", "reopen", "unmatch", "ignore", "unignore"},
	"op":     {"freeze", "unfreeze"},
	"entity": {"customers", "suppliers", "items", "invoices", "employees"},
	"key":    {"none"}, // business packs act on the caller's own company: only an unknown pack is swept
}

type sweepCall struct{ method, path, pattern string }

// sweepCalls: every route with {cid} and at least one id parameter, filled with company cid and the ids
// of company idsOf (pos orders live in "orders" under /pos/, so that segment is resolved by prefix).
func sweepCalls(t *testing.T, cid string, ids map[string]string) []sweepCall {
	t.Helper()
	var out []sweepCall
	for _, pat := range itAPI.protected {
		method, path, ok := strings.Cut(pat, " ")
		if !ok || !strings.Contains(path, "{cid}") {
			continue
		}
		params := paramRe.FindAllStringSubmatch(path, -1)
		if len(params) < 2 {
			continue // only {cid}: covered by the list scan
		}
		path = strings.TrimPrefix(path, v1)
		paths := []string{strings.Replace(path, "{cid}", cid, 1)}
		for _, p := range params[1:] {
			name := p[1]
			var next []string
			for _, cur := range paths {
				if vals, ok := paramValues[name]; ok {
					for _, v := range vals {
						next = append(next, strings.Replace(cur, "{"+name+"}", v, 1))
					}
					continue
				}
				segs := strings.Split(cur[:strings.Index(cur, "{"+name+"}")], "/")
				seg := segs[len(segs)-2]
				tb := idTable[seg]
				if seg == "orders" && strings.Contains(cur, "/pos/") {
					tb = "pos_orders"
				}
				if seg == "orders" && strings.Contains(cur, "/mfg/") {
					tb = "mfg_orders"
				}
				if name == "oid" {
					tb = "outages"
				}
				if name == "pid" {
					tb = "payslips"
				}
				if name == "account" {
					tb = "accounts"
				}
				v := ids[tb]
				if v == "" {
					if tb == "" {
						t.Errorf("route %s: no table known for {%s} after %q", pat, name, seg)
					} else {
						t.Logf("route %s: company B has no %s row, random id used", pat, tb)
					}
					v = "0190a0a0-0000-7000-8000-" + randHex(6)
				}
				next = append(next, strings.Replace(cur, "{"+name+"}", v, 1))
			}
			paths = next
		}
		for _, p := range paths {
			out = append(out, sweepCall{method, p, pat})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// sweepBody: a body each id route accepts (decoding is strict, so only known fields), built with company
// B's own ids: the attack sends B's ids everywhere, the legit replay under B shows the call really works.
func sweepBody(c sweepCall, ids map[string]string) any {
	if c.method == "GET" || c.method == "DELETE" {
		return nil
	}
	route := c.pattern[strings.Index(c.pattern, "{cid}")+len("{cid}"):]
	m := ids["payment_methods"]
	switch route {
	case "/customers/{id}/portal", "/journal/{id}/post", "/invoices/{id}/void", "/payments/{id}/void", "/stock/docs/{id}/void",
		"/stock/transfers/{id}/void", "/sales/deliveries/{id}/void", "/bills/{id}/void", "/supplier-payments/{id}/void",
		"/expenses/{id}/void", "/payroll/{id}/post", "/payroll/{id}/void", "/recurring/{id}/run", "/bank/statements/{id}/auto-match",
		"/routers/{id}/test", "/router-actions/{id}/retry", "/messages/{id}/retry", "/webhooks/{id}/rotate", "/webhooks/{id}/test",
		"/webhook-deliveries/{id}/retry", "/crm/leads/{id}/convert", "/approvals/{id}/approve", "/approvals/{id}/reject",
		"/approvals/{id}/cancel", "/sales/orders/{id}/deliver", "/sales/orders/{id}/invoice", "/sales/orders/{id}/{action}",
		"/bank/lines/{id}/{action}", "/journal/{id}/reverse", "/exchanges/{id}/void", "/cheques/{id}/{op}":
		return map[string]any{}
	case "/journal-lines/{id}":
		return map[string]any{"cost_center_id": ""}
	case "/customers/{id}/subscriptions":
		return map[string]any{"plan_id": ids["plans"]}
	case "/customers/{id}/refunds":
		return map[string]any{"amount": "1", "method_id": m}
	case "/invoices/{id}/credit-note":
		return map[string]any{"amount": "1"}
	case "/bills/{id}/debit-note":
		return map[string]any{"amount": "1"}
	case "/subscriptions/{id}/network":
		return map[string]any{"action": "disable"}
	case "/generators/{id}/hours":
		return map[string]any{"date": "2026-11-20", "hour_meter": "5000"}
	case "/generators/{id}/outages", "/generators/{id}/outages/{oid}":
		return map[string]any{"started_at": "2026-11-21T10:00:00Z"}
	case "/tariffs/{id}/apply":
		return map[string]any{"plan_ids": []any{}}
	case "/employees/{id}/advances":
		return map[string]any{"date": "2026-11-05", "amount": "1", "method_id": m}
	case "/payroll/{id}/pay":
		return map[string]any{"method_id": m}
	case "/payroll/{id}/payslips/{pid}":
		return map[string]any{"allowances": "1"}
	case "/assets/{id}/dispose":
		return map[string]any{"date": "2026-11-30", "proceeds": "0"}
	case "/bank/lines/{id}/entry":
		return map[string]any{"account_id": ids["accounts"]}
	case "/bank/lines/{id}/match":
		return map[string]any{"journal_line_id": ids["journal_lines"]}
	case "/pos/sessions/{id}/close":
		return map[string]any{"counted": []any{}}
	case "/pos/orders/{id}/refund":
		return map[string]any{"session_id": ids["pos_sessions"], "lines": []any{map[string]any{"line_no": 1, "quantity": "1"}}, "method_id": m}
	case "/journal/{id}":
		return map[string]any{"date": "2026-11-01", "lines": []any{map[string]any{"account_id": ids["accounts"], "debit": "2"},
			map[string]any{"account_id": ids["accounts"], "credit": "2"}}}
	case "/sales/orders/{id}":
		return map[string]any{"customer_id": ids["customers"], "currency": "USD", "lines": []any{map[string]any{"description": "x", "quantity": "1", "unit_price": "1"}}}
	case "/crm/leads/{id}":
		return map[string]any{"title": "x"}
	case "/crm/activities/{id}":
		return map[string]any{"done": true}
	case "/tickets/{id}/notes":
		return map[string]any{"body": "x"}
	case "/repairs/{id}/lines", "/wh/packages/{id}/lines":
		return map[string]any{"lines": []any{}}
	case "/wh/stops/{id}/pod":
		return map[string]any{"status": "failed", "note": "x"}
	case "/wh/stops/{id}/photo":
		return map[string]any{"data": "iVBORw0KGgo="}
	case "/mfg/quality-checks/{id}":
		return map[string]any{"result": "pass"}
	case "/mfg/routings/{id}":
		return map[string]any{"name": "x", "operations": []any{map[string]any{"name": "x", "workcenter_id": ids["mfg_workcenters"], "minutes": "1"}}}
	case "/mfg/orders/{id}/scrap":
		return map[string]any{"item_id": ids["items"], "quantity": "1", "reason": "x"}
	case "/quote-templates/{id}", "/fiscal-positions/{id}":
		return map[string]any{"name": "x"}
	case "/analysis/reports/{id}":
		return map[string]any{"name": "x", "spec": map[string]any{"source": "sales", "measure": "amount"}}
	case "/record-activities/{id}":
		return map[string]any{"summary": "x"}
	}
	return map[string]any{} // PATCH routes: every field is optional
}
func TestCompanyIsolationEveryRoute(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	a := o.company
	bID := str(o.c.ok("POST", "/companies", map[string]any{"name_en": secretB + " company"}, 201).body["id"])
	o.c.ok("POST", "/companies/"+bID+"/accounts/load-template", map[string]any{"template": "lebanon"}, 200)
	b := &org{c: o.c, tenant: o.tenant, company: bID, accounts: map[string]string{}, methods: map[string]string{}}
	for _, x := range o.c.ok("GET", b.p("/accounts"), nil, 200).list {
		b.accounts[str(obj(x)["code"])] = str(obj(x)["id"])
	}
	for _, m := range o.c.ok("GET", b.p("/payment-methods"), nil, 200).list {
		b.methods[str(obj(m)["name"])] = str(obj(m)["id"])
	}
	seedEverything(t, b)
	ids := companyIDs(t, bID)
	t.Logf("company B: %d tables with rows", len(ids))

	// a user limited to company A, with every permission (admin base)
	email := "lim" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": email, "full_name": "Limited", "role": "admin", "password": "limited-pass-9"}, 201)
	var limID string
	for _, u := range o.c.ok("GET", "/users", nil, 200).list {
		if str(obj(u)["email"]) == email {
			limID = str(obj(u)["id"])
		}
	}
	o.c.ok("PATCH", "/users/"+limID, map[string]any{"company_ids": []string{a}}, 200)
	lim := loginAs(t, email, "limited-pass-9")

	before := companyFingerprint(t, bID)
	calls := sweepCalls(t, a, ids)
	if len(calls) < 100 {
		t.Fatalf("only %d id routes swept", len(calls))
	}
	leaks := func(who string, c sweepCall, r resp) {
		t.Helper()
		if r.status >= 200 && r.status < 300 {
			t.Errorf("%s %s %s with company B ids under A: %d %s", who, c.method, c.path, r.status, r.raw)
		}
		if r.status >= 500 {
			t.Errorf("%s %s %s: server error %s", who, c.method, c.path, r.raw)
		}
		if strings.Contains(r.raw, secretB) {
			t.Errorf("%s %s %s: response contains company B data: %s", who, c.method, c.path, r.raw)
		}
	}
	for _, c := range calls {
		body := sweepBody(c, ids)
		leaks("owner", c, o.c.do(c.method, c.path, body))
		leaks("limited", c, lim.do(c.method, c.path, body))
	}

	// lists and reports of A, with B's ids in every filter: nothing of B may show
	filters := url.Values{}
	for k, tb := range map[string]string{"customer_id": "customers", "method_id": "payment_methods", "register_id": "pos_registers",
		"session_id": "pos_sessions", "account_id": "accounts", "item_id": "items", "warehouse_id": "warehouses", "supplier_id": "suppliers",
		"area_id": "areas", "station_id": "stations", "box_id": "boxes", "generator_id": "generators", "lead_id": "crm_leads",
		"employee_id": "employees", "subscription_id": "subscriptions", "invoice_id": "invoices"} {
		filters.Set(k, ids[tb])
	}
	filters.Set("from", "2000-01-01")
	filters.Set("to", "2100-01-01")
	filters.Set("period", "2026-11")
	lists := 0
	for _, pat := range itAPI.protected {
		method, path, _ := strings.Cut(pat, " ")
		if method != "GET" || !strings.Contains(path, "{cid}") || len(paramRe.FindAllString(path, -1)) != 1 {
			continue
		}
		p := strings.Replace(strings.TrimPrefix(path, v1), "{cid}", a, 1) + "?" + filters.Encode()
		for who, cl := range map[string]*client{"owner": o.c, "limited": lim} {
			r := cl.do("GET", p, nil)
			if r.status >= 500 {
				t.Errorf("%s GET %s: server error %s", who, p, r.raw)
			}
			if strings.Contains(r.raw, secretB) {
				t.Errorf("%s GET %s: company B data in company A list: %.300s", who, p, r.raw)
			}
		}
		lists++
	}
	// creates in A that point to B's records (ids in the body) are refused or ignore them
	refs := map[string]any{"customer_id": ids["customers"], "account_id": ids["accounts"], "method_id": ids["payment_methods"],
		"item_id": ids["items"], "warehouse_id": ids["warehouses"], "supplier_id": ids["suppliers"], "plan_id": ids["plans"],
		"area_id": ids["areas"], "box_id": ids["boxes"], "station_id": ids["stations"], "generator_id": ids["generators"],
		"router_id": ids["routers"], "tax_rate_id": ids["tax_rates"], "subscription_id": ids["subscriptions"], "invoice_id": ids["invoices"],
		"lead_id": ids["crm_leads"], "stage_id": ids["crm_stages"], "register_id": ids["pos_registers"], "session_id": ids["pos_sessions"],
		"employee_id": ids["employees"], "category_id": ids["item_categories"], "provider_id": ids["message_providers"],
		"template_id": ids["message_templates"], "from_method_id": ids["payment_methods"], "to_method_id": ids["payment_methods"],
		"change_method_id": ids["payment_methods"], "paid_from_method_id": ids["payment_methods"], "asset_account_id": ids["accounts"],
		"from_warehouse_id": ids["warehouses"], "to_warehouse_id": ids["warehouses"], "fuel_item_id": ids["items"],
		"sale_tax_rate_id": ids["tax_rates"], "revenue_account_id": ids["accounts"], "collector_id": nil,
		"customer_ids": []any{ids["customers"]}, "method_ids": []any{ids["payment_methods"]}, "plan_ids": []any{ids["plans"]},
	}
	creates := 0
	for _, pat := range itAPI.protected {
		method, path, _ := strings.Cut(pat, " ")
		if (method != "POST" && method != "PUT") || !strings.Contains(path, "{cid}") || len(paramRe.FindAllString(path, -1)) != 1 {
			continue
		}
		p := strings.Replace(strings.TrimPrefix(path, v1), "{cid}", a, 1)
		body := map[string]any{}
		for k, v := range refs {
			body[k] = v
		}
		line := map[string]any{"account_id": ids["accounts"], "item_id": ids["items"], "debit": "1", "quantity": "1", "unit_price": "1",
			"subscription_id": ids["subscriptions"], "reading": "5", "method_id": ids["payment_methods"], "amount": "1", "tax_rate_id": ids["tax_rates"]}
		body["lines"] = []any{line, map[string]any{"account_id": ids["accounts"], "credit": "1"}}
		body["tenders"] = []any{map[string]any{"method_id": ids["payment_methods"], "amount": "1"}}
		body["readings"] = []any{map[string]any{"subscription_id": ids["subscriptions"], "reading": "999"}}
		body["opening"] = []any{map[string]any{"method_id": ids["payment_methods"], "amount": "1"}}
		r := o.c.do(method, p, body)
		if r.status >= 500 {
			t.Errorf("owner %s %s with company B references: server error %s", method, p, r.raw)
		}
		if strings.Contains(r.raw, secretB) {
			t.Errorf("owner %s %s with company B references: response shows company B data: %.300s", method, p, r.raw)
		}
		creates++
	}

	if after := companyFingerprint(t, bID); after != before {
		t.Errorf("company B changed during the sweep:\nbefore %s\nafter  %s", before, after)
	}
	// the database itself: no row of A points to a row of B (same-company foreign keys)
	var n int
	if err := itOwnerPool.QueryRow(context.Background(), `SELECT count(*) FROM pg_constraint WHERE conname LIKE '%\_same\_company'`).Scan(&n); err != nil || n < 100 {
		t.Errorf("same-company foreign keys: %d %v", n, err)
	}

	// the same id calls made legitimately under B: how many really act (so the refusals above mean something)
	proven := 0
	bcalls := sweepCalls(t, bID, ids)
	for _, c := range bcalls {
		if r := o.c.do(c.method, c.path, sweepBody(c, ids)); r.status >= 200 && r.status < 300 {
			proven++
		} else {
			t.Logf("legit under B: %s %s -> %d %.150s", c.method, c.pattern, r.status, r.raw)
		}
	}
	t.Logf("swept %d id calls x 2 users, %d lists/reports, %d creates with foreign references; %d of the id calls succeed for company B itself",
		len(calls), lists, creates, proven)
	if proven < len(bcalls)/3 {
		t.Errorf("only %d of %d legit calls succeeded: the sweep bodies are too weak to prove much", proven, len(bcalls))
	}
}

func TestApprovalRunsAsRequesterAndRefundRules(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	cust := o.customer(t, "Approve Q", "")
	newInv := func(amt string) string {
		r := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-06",
			"lines": []any{map[string]any{"description": "Work", "quantity": "1", "unit_price": amt}}}, 201)
		return str(obj(r.body["invoice"])["id"])
	}
	o.c.ok("PUT", o.p("/approval-rules"), map[string]any{"void": true, "refunds": true, "payment_over": "100"}, 200)
	email := "acq" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": email, "full_name": "Requester Q", "role": "accountant", "password": "acct-pass-word-1"}, 201)
	a := loginAs(t, email, "acct-pass-word-1")
	var accID string
	for _, u := range o.c.ok("GET", "/users", nil, 200).list {
		if str(obj(u)["email"]) == email {
			accID = str(obj(u)["id"])
		}
	}

	// the void is made in the requester's name, the approver is recorded beside them
	inv := newInv("100")
	req := str(a.ok("POST", o.p("/invoices/"+inv+"/void"), map[string]any{"reason": "typo"}, 202).body["approval_id"])
	o.c.ok("POST", o.p("/approvals/"+req+"/approve"), map[string]any{}, 200)
	found := false
	for _, e := range o.c.ok("GET", "/audit?entity_id="+inv, nil, 200).list {
		m := obj(e)
		if str(m["entity"]) == "invoices" && str(m["action"]) == "update" && str(obj(m["after"])["status"]) == "void" {
			found = true
			if str(m["actor"]) != "Requester Q" || str(m["approved_by"]) != "Owner" || str(m["approval_id"]) != req || str(m["company_id"]) != o.company {
				t.Fatalf("audit of the approved void: %v", m)
			}
		}
	}
	if !found {
		t.Fatal("no audit entry for the approved void")
	}

	// the requester lost the permission before the approval: the call fails, nothing happens
	inv2 := newInv("50")
	req2 := str(a.ok("POST", o.p("/invoices/"+inv2+"/void"), map[string]any{"reason": "x"}, 202).body["approval_id"])
	o.c.ok("PATCH", "/users/"+accID, map[string]any{"role": "read_only"}, 200)
	if r := o.c.ok("POST", o.p("/approvals/"+req2+"/approve"), map[string]any{}, 200); str(r.body["status"]) != "failed" {
		t.Fatalf("approval for a requester without the permission: %s", r.raw)
	}
	if st := str(obj(o.c.ok("GET", o.p("/invoices/"+inv2), nil, 200).body["invoice"])["status"]); st == "void" {
		t.Fatal("voided although the requester lost the permission")
	}
	o.c.ok("PATCH", "/users/"+accID, map[string]any{"role": "accountant"}, 200)

	// supplier payment given only as cash taken from an LBP box: compared in LBP, not as dollars
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Fuel Q", "currency": "USD"}, 201).body["id"])
	small := a.do("POST", o.p("/supplier-payments"), map[string]any{"supplier_id": sup, "date": "2026-11-07", "currency": "USD",
		"method_id": o.methods["Cash LBP"], "paid_amount": "1000000"})
	if small.status != 201 {
		t.Fatalf("1,000,000 LBP (about 11 USD) needed approval: %d %s", small.status, small.raw)
	}
	if big := a.do("POST", o.p("/supplier-payments"), map[string]any{"supplier_id": sup, "date": "2026-11-07", "currency": "USD",
		"method_id": o.methods["Cash LBP"], "paid_amount": "20000000"}); big.status != 202 {
		t.Fatalf("20,000,000 LBP (about 223 USD) did not need approval: %d %s", big.status, big.raw)
	}

	// refunds rule: paying a customer's credit back waits for approval
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-11-08", "currency": "USD",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "40"}}}, 201)
	if r := a.do("POST", o.p("/customers/"+cust+"/refunds"), map[string]any{"amount": "5", "method_id": o.methods["Cash USD"]}); r.status != 202 || str(r.body["kind"]) != "refund" {
		t.Fatalf("refund without approval: %d %s", r.status, r.raw)
	}

	// API keys can manage webhooks within their scopes
	key := str(o.c.ok("POST", "/api-keys", map[string]any{"name": "Hooks", "role": "admin", "scopes": []string{"api.view", "api.create"}}, 201).body["key"])
	weak := str(o.c.ok("POST", "/api-keys", map[string]any{"name": "Read", "role": "admin", "scopes": []string{"customers.view"}}, 201).body["key"])
	call := func(k, method, path string, body string) int {
		req, _ := http.NewRequest(method, itServer.URL+"/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := call(key, "POST", o.p("/webhooks"), `{"url":"https://hooks.example.com/q","events":["invoice.created"]}`); c != 201 {
		t.Fatalf("scoped key creating a webhook: %d", c)
	}
	if c := call(key, "GET", o.p("/webhooks"), ""); c != 200 {
		t.Fatalf("scoped key listing webhooks: %d", c)
	}
	if c := call(weak, "POST", o.p("/webhooks"), `{"url":"https://hooks.example.com/q2","events":["invoice.created"]}`); c != 403 {
		t.Fatalf("key without api.create made a webhook: %d", c)
	}
}

func TestLedgerDailyTotalsMatchLive(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	cust := o.customer(t, "Totals", "")
	for i, d := range []string{"2026-10-05", "2026-11-01", "2026-11-15"} {
		o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": d,
			"lines": []any{map[string]any{"description": "Fee", "quantity": "1", "unit_price": fmt.Sprint(10 * (i + 1))}}}, 201)
	}
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-11-16", "currency": "LBP",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash LBP"], "amount": "1790000"}}}, 201)
	reports := []string{"/reports/trial-balance?from=2026-11-01&to=2026-11-30", "/reports/trial-balance?from=2026-11-10&to=2026-11-20",
		"/reports/profit-loss?from=2026-10-01&to=2026-11-30", "/reports/balance-sheet?to=2026-11-15", "/cash/balances",
		"/reports/expenses?from=2026-01-01&to=2026-12-31", "/reports/vat?from=2026-01-01&to=2026-12-31"}
	snap := func() []string {
		var out []string
		for _, p := range reports {
			out = append(out, o.c.ok("GET", o.p(p), nil, 200).raw)
		}
		return out
	}
	live := snap()
	if n, err := itAPI.CatchUpLedger(context.Background()); err != nil || n == 0 {
		t.Fatalf("catch-up: %d %v", n, err)
	}
	summed := snap()
	for i := range reports {
		if live[i] != summed[i] {
			t.Errorf("%s differs after catch-up:\nlive   %s\nsummed %s", reports[i], live[i], summed[i])
		}
	}
	// new postings after the catch-up are included at once (live part), and again after the next catch-up
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-12",
		"lines": []any{map[string]any{"description": "Late", "quantity": "1", "unit_price": "7"}}}, 201)
	tb := o.c.ok("GET", o.p("/reports/trial-balance?from=2026-11-01&to=2026-11-30"), nil, 200)
	eqDec(t, "debits incl. the new bill", tb.body["total_debit"], dec(t, obj(o.c.ok("GET", o.p(reports[0]), nil, 200).body)["total_debit"]).String())
	before := o.c.ok("GET", o.p(reports[0]), nil, 200).raw
	if _, err := itAPI.CatchUpLedger(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := o.c.ok("GET", o.p(reports[0]), nil, 200).raw; after != before {
		t.Errorf("trial balance changed by the second catch-up:\n%s\n%s", before, after)
	}
	o.trialBalanced(t)
}
