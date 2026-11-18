package httpapi

import "testing"

// Module F integration tests: network filters, tariffs, VAT, local-currency snapshot, generator report.

func TestNetworkFiltersAndGeneratorReport(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	st := str(o.c.ok("POST", o.p("/stations"), map[string]any{"name": "North"}, 201).body["id"])
	st2 := str(o.c.ok("POST", o.p("/stations"), map[string]any{"name": "South"}, 201).body["id"])
	box := str(o.c.ok("POST", o.p("/boxes"), map[string]any{"station_id": st, "code": "B-1"}, 201).body["id"])
	box2 := str(o.c.ok("POST", o.p("/boxes"), map[string]any{"station_id": st2, "code": "B-2"}, 201).body["id"])
	if r := o.c.do("POST", o.p("/boxes"), map[string]any{"station_id": st, "code": "B-1"}); r.status != 409 {
		t.Fatalf("duplicate box code accepted: %d", r.status)
	}
	gen := str(o.c.ok("POST", o.p("/generators"), map[string]any{"station_id": st, "name": "G1", "kva": "500"}, 201).body["id"])

	metered := str(o.c.ok("POST", o.p("/plans"), map[string]any{"name": "Meter", "billing_type": "metered", "currency": "USD",
		"base_fee": "5", "unit_price": "0.5"}, 201).body["id"])
	c1, c2 := o.customer(t, "North One", ""), o.customer(t, "South One", "")
	s1 := str(o.c.ok("POST", o.p("/customers/"+c1+"/subscriptions"), map[string]any{"plan_id": metered, "start_date": "2026-01-01", "box_id": box}, 201).body["id"])
	s2 := str(o.c.ok("POST", o.p("/customers/"+c2+"/subscriptions"), map[string]any{"plan_id": metered, "start_date": "2026-01-01", "box_id": box2}, 201).body["id"])

	if l := o.c.ok("GET", o.p("/customers?station_id="+st), nil, 200).list; len(l) != 1 || str(obj(l[0])["name"]) != "North One" {
		t.Fatalf("station filter returned %v", l)
	}
	if l := o.c.ok("GET", o.p("/readings?period=2026-11&box_id="+box2), nil, 200).list; len(l) != 1 {
		t.Fatalf("box filter on readings returned %d rows", len(l))
	}
	o.c.ok("POST", o.p("/readings"), map[string]any{"period": "2026-11", "readings": []any{
		map[string]any{"subscription_id": s1, "reading": "100"}, map[string]any{"subscription_id": s2, "reading": "50"}}}, 200)
	run := o.c.ok("POST", o.p("/billing/run"), map[string]any{"period": "2026-11", "issue_date": "2026-11-05", "station_id": st}, 200)
	if str(run.body["created"]) != "1" {
		t.Fatalf("station-filtered billing run created %v", run.body["created"])
	}

	o.c.ok("POST", o.p("/generators/"+gen+"/hours"), map[string]any{"date": "2026-11-01", "hour_meter": "1000"}, 200)
	o.c.ok("POST", o.p("/generators/"+gen+"/hours"), map[string]any{"date": "2026-11-30", "hour_meter": "1300"}, 200)
	if r := o.c.do("POST", o.p("/generators/"+gen+"/hours"), map[string]any{"date": "2026-12-01", "hour_meter": "900"}); r.status != 422 {
		t.Fatalf("hour meter going backwards accepted: %d", r.status)
	}
	o.c.ok("POST", o.p("/generators/"+gen+"/outages"), map[string]any{"started_at": "2026-11-10T10:00:00Z", "ended_at": "2026-11-10T12:30:00Z", "reason": "oil"}, 200)
	rep := o.c.ok("GET", o.p("/reports/generators?from=2026-11-01&to=2026-11-30"), nil, 200)
	g := obj(rep.body["generators"].([]any)[0])
	eqDec(t, "hours", g["hours"], "300")
	if str(g["outage_minutes"]) != "150" {
		t.Fatalf("outage minutes = %v", g["outage_minutes"])
	}
	stations := rep.body["stations"].([]any)
	for _, s := range stations {
		if str(obj(s)["name"]) == "North" {
			eqDec(t, "kWh billed", obj(s)["kwh_billed"], "100")
		}
	}
	o.trialBalanced(t)
}

func TestTariffApply(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	metered := str(o.c.ok("POST", o.p("/plans"), map[string]any{"name": "Meter LBP", "billing_type": "metered", "currency": "LBP",
		"base_fee": "0", "unit_price": "40000"}, 201).body["id"])
	fixed := str(o.c.ok("POST", o.p("/plans"), map[string]any{"name": "5A LBP", "billing_type": "fixed", "currency": "LBP", "base_fee": "300000"}, 201).body["id"])
	usd := o.plan(t, "USD plan", "10")
	tar := o.c.ok("POST", o.p("/tariffs"), map[string]any{"period": "2026-08", "currency": "LBP", "price_per_kwh": "48241",
		"fixed_fees": map[string]any{fixed: "385000"}}, 200)
	tid := str(tar.body["id"])
	if r := o.c.do("POST", o.p("/tariffs/"+tid+"/apply"), map[string]any{"plan_ids": []any{usd}}); r.status != 422 {
		t.Fatalf("tariff applied to a plan in another currency: %d", r.status)
	}
	ap := o.c.ok("POST", o.p("/tariffs/"+tid+"/apply"), map[string]any{"plan_ids": []any{metered, fixed}}, 200)
	if str(ap.body["updated"]) != "2" {
		t.Fatalf("tariff updated %v plans", ap.body["updated"])
	}
	for _, p := range o.c.ok("GET", o.p("/plans"), nil, 200).list {
		m := obj(p)
		switch str(m["id"]) {
		case metered:
			eqDec(t, "unit price", m["unit_price"], "48241")
		case fixed:
			eqDec(t, "fixed fee", m["base_fee"], "385000")
		}
	}
	if l := o.c.ok("GET", o.p("/tariffs"), nil, 200).list; len(l) != 1 || obj(l[0])["applied_at"] == nil {
		t.Fatalf("tariff history wrong: %v", l)
	}
}

func TestVATInvoiceAndLocalCurrencySnapshot(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	taxes := o.c.ok("GET", o.p("/tax-rates"), nil, 200).list
	if len(taxes) != 1 || str(obj(taxes[0])["rate"]) != "11" {
		t.Fatalf("default VAT not created: %v", taxes)
	}
	vat := str(obj(taxes[0])["id"])
	cust := o.customer(t, "Taxed", "")
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-05",
		"lines": []any{map[string]any{"description": "Installation", "quantity": "1", "unit_price": "100", "tax_rate_id": vat},
			map[string]any{"description": "Cable", "quantity": "2", "unit_price": "10"}}}, 201)
	iv := obj(inv.body["invoice"])
	eqDec(t, "total with VAT", iv["total"], "131")                   // 100 + 11 + 20
	eqDec(t, "LBP snapshot", iv["local_currency_total"], "11724500") // 131 × 89,500
	if str(iv["local_currency"]) != "LBP" {
		t.Fatalf("local currency = %v", iv["local_currency"])
	}
	// tax on a plan applies in the billing run
	plan := str(o.c.ok("POST", o.p("/plans"), map[string]any{"name": "Taxed plan", "billing_type": "fixed", "currency": "USD", "base_fee": "50", "tax_rate_id": vat}, 201).body["id"])
	c2 := o.customer(t, "Plan taxed", "")
	o.c.ok("POST", o.p("/customers/"+c2+"/subscriptions"), map[string]any{"plan_id": plan, "start_date": "2026-01-01"}, 201)
	run := o.c.ok("POST", o.p("/billing/run"), map[string]any{"period": "2026-11", "issue_date": "2026-11-05"}, 200)
	eqDec(t, "run total", obj(run.body["totals"])["USD"], "55.5")

	vr := o.c.ok("GET", o.p("/reports/vat?from=2026-11-01&to=2026-11-30"), nil, 200)
	eqDec(t, "VAT collected", vr.body["collected"], "16.5")
	o.trialBalanced(t)
}
