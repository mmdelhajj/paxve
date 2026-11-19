package httpapi

import (
	"encoding/base64"
	"testing"
	"time"
)

// Odoo 19 audit stage: purchase orders, dunning, FX revaluation, deferrals, attachments, declining depreciation,
// payment terms, duplicate supplier references, goods revenue account, landed cost as a supplier bill.

func TestPurchaseOrderFlow(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Fuel Co", "currency": "USD"}, 201).body["id"])
	item := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "DSL", "name": "Diesel", "kind": "stock", "unit": "L"}, 201).body["id"])
	po := o.c.ok("POST", o.p("/purchase-orders"), map[string]any{"supplier_id": sup, "order_date": "2026-11-01", "expected_date": "2026-11-05",
		"currency": "USD", "lines": []any{map[string]any{"item_id": item, "quantity": "1000", "unit_price": "0.8"}}}, 201)
	id := str(obj(po.body["order"])["id"])
	eqDec(t, "po total", obj(po.body["order"])["total"], "800")
	if st := str(obj(po.body["order"])["status"]); st != "rfq" {
		t.Fatalf("new PO status %s", st)
	}
	// billing before confirmation is refused
	if r := o.c.do("POST", o.p("/purchase-orders/"+id+"/bill"), map[string]any{}); r.status != 409 {
		t.Fatalf("bill of unconfirmed PO: %d %s", r.status, r.raw)
	}
	sent := o.c.ok("POST", o.p("/purchase-orders/"+id+"/send"), map[string]any{}, 200)
	if str(sent.body["message"]) == "" {
		t.Fatalf("no RFQ message: %s", sent.raw)
	}
	o.c.ok("POST", o.p("/purchase-orders/"+id+"/confirm"), map[string]any{}, 200)
	// on order shows in the replenishment report
	o.c.ok("PUT", o.p("/stock/reorder-rules"), map[string]any{"item_id": item, "warehouse_id": defaultWH(t, o), "min_qty": "100", "max_qty": "2000", "supplier_id": sup}, 200)
	rep := o.c.ok("GET", o.p("/reports/stock-replenishment"), nil, 200)
	var found bool
	for _, x := range rep.body["rows"].([]any) {
		if str(obj(x)["item_id"]) == item {
			found = true
			eqDec(t, "on order", obj(x)["on_order"], "1000")
			eqDec(t, "to order after PO", obj(x)["to_order"], "1000")
		}
	}
	if !found {
		t.Fatalf("item not in replenishment: %s", rep.raw)
	}
	lines := o.c.ok("GET", o.p("/purchase-orders/"+id), nil, 200).body["lines"].([]any)
	lineID := str(obj(lines[0])["id"])
	// partial bill, then more than what is open is refused
	b1 := o.c.ok("POST", o.p("/purchase-orders/"+id+"/bill"), map[string]any{"bill_date": "2026-11-05", "supplier_ref": "F-1",
		"lines": []any{map[string]any{"po_line_id": lineID, "quantity": "600"}}}, 201)
	if st := str(obj(obj(b1.body["po"])["order"])["status"]); st != "partial" {
		t.Fatalf("status after partial bill: %s", st)
	}
	if r := o.c.do("POST", o.p("/purchase-orders/"+id+"/bill"), map[string]any{"lines": []any{map[string]any{"po_line_id": lineID, "quantity": "500"}}}); r.status != 422 {
		t.Fatalf("over-billing: %d %s", r.status, r.raw)
	}
	b2 := o.c.ok("POST", o.p("/purchase-orders/"+id+"/bill"), map[string]any{"bill_date": "2026-11-06", "supplier_ref": "F-2"}, 201)
	if st := str(obj(obj(b2.body["po"])["order"])["status"]); st != "billed" {
		t.Fatalf("status after full bill: %s", st)
	}
	st := obj(o.c.ok("GET", o.p("/items/"+item), nil, 200).body["stock"].([]any)[0])
	eqDec(t, "stock in from PO bills", st["quantity"], "1000")
	// voiding a bill re-opens the order
	o.c.ok("POST", o.p("/bills/"+str(obj(obj(b2.body["bill"])["bill"])["id"])+"/void"), map[string]any{"reason": "wrong"}, 200)
	if st := str(obj(o.c.ok("GET", o.p("/purchase-orders/"+id), nil, 200).body["order"])["status"]); st != "partial" {
		t.Fatalf("status after void: %s", st)
	}
	o.trialBalanced(t)
}

func defaultWH(t *testing.T, o *org) string {
	t.Helper()
	return str(obj(o.c.ok("GET", o.p("/warehouses"), nil, 200).list[0])["id"])
}

func TestDunningLevelsFeeAndCut(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Late Payer", "")
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "issue_date": "2026-09-01", "due_date": "2026-09-10", "currency": "USD",
		"lines": []any{map[string]any{"description": "Subscription", "quantity": "1", "unit_price": "100"}}}, 201)
	invID := str(obj(inv.body["invoice"])["id"])
	o.c.ok("PUT", o.p("/dunning"), map[string]any{"enabled": true, "levels": []any{
		map[string]any{"level": 1, "name": "Friendly reminder", "days_overdue": 3},
		map[string]any{"level": 2, "name": "Late fee", "days_overdue": 15, "fee_kind": "percent", "fee_amount": "5"},
		map[string]any{"level": 3, "name": "Cut", "days_overdue": 30, "fee_kind": "fixed", "fee_amount": "10", "fee_currency": "USD", "cut_service": true},
	}}, 200)
	// levels must be in increasing days
	if r := o.c.do("PUT", o.p("/dunning"), map[string]any{"enabled": true, "levels": []any{
		map[string]any{"level": 1, "name": "A", "days_overdue": 10}, map[string]any{"level": 2, "name": "B", "days_overdue": 5}}}); r.status != 400 {
		t.Fatalf("unordered levels: %d", r.status)
	}
	pv := o.c.ok("GET", o.p("/dunning/preview"), nil, 200)
	rows := pv.body["rows"].([]any)
	if len(rows) != 1 || str(obj(rows[0])["level"]) != "3" {
		t.Fatalf("preview: %s", pv.raw)
	}
	eqDec(t, "level 3 fee", obj(rows[0])["fee"], "10")
	run := o.c.ok("POST", o.p("/dunning/run"), map[string]any{}, 200)
	if str(run.body["applied"]) != "1" || str(run.body["fee_invoices"]) != "1" {
		t.Fatalf("run: %s", run.raw)
	}
	again := o.c.ok("POST", o.p("/dunning/run"), map[string]any{}, 200)
	if str(again.body["applied"]) != "0" {
		t.Fatalf("second run applied again: %s", again.raw)
	}
	ev := o.c.ok("GET", o.p("/dunning/events?customer_id="+cust), nil, 200)
	if len(ev.list) != 1 || str(obj(ev.list[0])["invoice_id"]) != invID {
		t.Fatalf("events: %s", ev.raw)
	}
	eqDec(t, "fee invoice", obj(ev.list[0])["fee_total"], "10")
	// the late fee is income on the late-fee account (77 other income)
	eqDec(t, "77 balance", tbBalance(t, o, "77"), "-10")
	o.trialBalanced(t)
}

func TestFXRevaluation(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-10-01", "89500")
	cust := o.customer(t, "LBP Customer", "")
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "issue_date": "2026-10-05", "currency": "LBP",
		"lines": []any{map[string]any{"description": "Subscription", "quantity": "1", "unit_price": "8950000"}}}, 201)
	o.rate(t, "LBP", "2026-10-31", "90000")
	pv := o.c.ok("GET", o.p("/fx-revaluation/preview?period=2026-10"), nil, 200)
	eqDec(t, "unrealised loss", pv.body["total_loss"], "0.56")
	run := o.c.ok("POST", o.p("/fx-revaluation"), map[string]any{"period": "2026-10"}, 201)
	if str(run.body["reversal_id"]) == "" {
		t.Fatalf("no reversal: %s", run.raw)
	}
	if r := o.c.do("POST", o.p("/fx-revaluation"), map[string]any{"period": "2026-10"}); r.status != 409 {
		t.Fatalf("second revaluation: %d", r.status)
	}
	// at the month end the receivable is worth 99.44; from the 1st it is back at its booked value (100)
	tb := o.c.ok("GET", o.p("/reports/trial-balance?from=2000-01-01&to=2026-10-31"), nil, 200)
	for _, r := range tb.body["rows"].([]any) {
		if str(obj(r)["code"]) == "411" {
			eqDec(t, "411 at month end", obj(r)["balance"], "99.44")
		}
	}
	eqDec(t, "411 after reversal", tbBalance(t, o, "411"), "100")
	o.trialBalanced(t)
}

func TestDeferredRevenueFromInvoice(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Yearly", "")
	start := time.Now().AddDate(0, -2, 0).Format("2006-01")
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "issue_date": start + "-01", "currency": "USD",
		"lines": []any{map[string]any{"description": "Internet 12 months", "quantity": "1", "unit_price": "1200"}}}, 201)
	d := o.c.ok("POST", o.p("/deferrals"), map[string]any{"kind": "revenue", "source_type": "invoice", "source_id": str(obj(inv.body["invoice"])["id"]),
		"months": 12}, 201)
	eqDec(t, "deferred amount", d.body["amount"], "1200")
	// this month and the two before are recognised at once
	eqDec(t, "recognised", d.body["recognised"], "300")
	eqDec(t, "487 deferred revenue", tbBalance(t, o, "487"), "-900")
	if r := o.c.do("POST", o.p("/deferrals"), map[string]any{"kind": "revenue", "source_type": "invoice", "source_id": str(obj(inv.body["invoice"])["id"]),
		"months": 12}); r.status != 409 {
		t.Fatalf("invoice deferred twice: %d", r.status)
	}
	o.c.ok("POST", o.p("/deferrals/run"), map[string]any{"period": time.Now().AddDate(0, 1, 0).Format("2006-01")}, 200)
	eqDec(t, "487 after next month", tbBalance(t, o, "487"), "-800")
	c := o.c.ok("POST", o.p("/deferrals/"+str(d.body["id"])+"/cancel"), map[string]any{}, 200)
	eqDec(t, "released", c.body["released"], "800")
	eqDec(t, "487 after cancel", tbBalance(t, o, "487"), "0")
	o.trialBalanced(t)
}

func TestAttachmentsDecliningAndPurchaseChecks(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Agent", "currency": "USD"}, 201).body["id"])
	o.c.ok("PUT", o.p("/payment-terms"), map[string]any{"supplier_id": sup, "due_days": 30}, 200)
	item := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "CBL", "name": "Cable", "kind": "stock"}, 201).body["id"])
	it := o.c.ok("GET", o.p("/items/"+item), nil, 200)
	if str(obj(it.body["item"])["revenue_account_id"]) != o.accounts["707"] {
		t.Fatalf("goods revenue default: %s", it.raw)
	}
	bill := o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "supplier_ref": "INV-77", "currency": "USD",
		"lines": []any{map[string]any{"item_id": item, "quantity": "10", "unit_price": "10"}}}, 201)
	b := obj(bill.body["bill"])
	if str(b["due_date"]) != "2026-12-01" {
		t.Fatalf("payment terms: due %v", b["due_date"])
	}
	dup := map[string]any{"supplier_id": sup, "bill_date": "2026-11-02", "supplier_ref": "inv-77", "currency": "USD",
		"lines": []any{map[string]any{"item_id": item, "quantity": "1", "unit_price": "10"}}}
	if r := o.c.do("POST", o.p("/bills"), dup); r.status != 409 {
		t.Fatalf("duplicate supplier ref: %d %s", r.status, r.raw)
	}
	dup["allow_duplicate"] = true
	o.c.ok("POST", o.p("/bills"), dup, 201)
	// landed cost from the freight agent: a supplier bill, so the supplier balance matches payables
	o.c.ok("POST", o.p("/bills/"+str(b["id"])+"/landed-costs"), map[string]any{"date": "2026-11-02", "currency": "USD", "amount": "20", "supplier_id": sup}, 201)
	sp := o.c.ok("GET", o.p("/suppliers/"+sup), nil, 200)
	if n := len(sp.body["bills"].([]any)); n != 3 {
		t.Fatalf("supplier bills incl. landed cost: %d", n)
	}
	eqDec(t, "401 = supplier balance", tbBalance(t, o, "401"), "-130")
	eqDec(t, "supplier own terms shown", obj(sp.body["supplier"])["due_days"], "30")

	// attachments
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 scanned bill"))
	a := o.c.ok("POST", o.p("/attachments"), map[string]any{"type": "bill", "id": str(b["id"]), "filename": "scan.pdf", "mime": "application/pdf", "data": data}, 201)
	list := o.c.ok("GET", o.p("/attachments?type=bill&id="+str(b["id"])), nil, 200)
	if len(list.list) != 1 {
		t.Fatalf("attachments: %s", list.raw)
	}
	if r := o.c.do("GET", o.p("/attachments/"+str(a.body["id"])+"/file"), nil); r.status != 200 {
		t.Fatalf("download: %d", r.status)
	}
	if r := o.c.do("POST", o.p("/attachments"), map[string]any{"type": "bill", "id": o.company, "filename": "x.pdf", "data": data}); r.status != 404 {
		t.Fatalf("attachment on a missing document: %d", r.status)
	}
	o.c.ok("DELETE", o.p("/attachments/"+str(a.body["id"])), nil, 200)
	if len(o.c.ok("GET", o.p("/attachments?type=bill&id="+str(b["id"])), nil, 200).list) != 0 {
		t.Fatal("deleted attachment still listed")
	}

	// declining balance: 12,000 over 60 months, double declining = 400 the first month, then on the lower book value
	as := o.c.ok("POST", o.p("/assets"), map[string]any{"name": "Truck", "purchase_date": "2026-01-01", "currency": "USD", "cost": "12000",
		"useful_life_months": 60, "asset_account_id": o.accounts["218"], "method": "declining", "declining_factor": "2"}, 201)
	if str(as.body["method"]) != "declining" {
		t.Fatalf("method: %s", as.raw)
	}
	eqDec(t, "declining month 1", o.c.ok("POST", o.p("/depreciation/run"), map[string]any{"period": "2026-01"}, 200).body["total"], "400")
	eqDec(t, "declining month 2", o.c.ok("POST", o.p("/depreciation/run"), map[string]any{"period": "2026-02"}, 200).body["total"], "386.67")
	o.trialBalanced(t)
}
