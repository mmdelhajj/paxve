package httpapi

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Module S: cheques, currency exchange, fiscal years, cost centers, budgets, opening balances, cash flow,
// comparison, owner summary, drill-down to the source document, barcode payment of the scanned bill first.

// idBalance: debit − credit in base currency of one account (by id), posted entries.
func (o *org) idBalance(t *testing.T, accountID string) string {
	t.Helper()
	ctx := context.Background()
	var v string
	if err := pgx.BeginFunc(ctx, itOwnerPool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::text FROM journal_lines l
			JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted' WHERE e.company_id = $1 AND l.account_id = $2`,
			o.company, accountID).Scan(&v)
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

func (o *org) invoice(t *testing.T, customer, date, amount string) string {
	t.Helper()
	r := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": customer, "currency": "USD", "issue_date": date,
		"lines": []any{map[string]any{"description": "Service", "quantity": "1", "unit_price": amount}}}, 201)
	return str(obj(r.body["invoice"])["id"])
}

func chequeOf(r resp) map[string]any { return obj(r.body["cheque"]) }

func TestChequeLifecycle(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	st := o.c.ok("GET", o.p("/accounting/settings"), nil, 200).body
	cih, cuc, cp := str(st["cheque_in_hand_account_id"]), str(st["cheque_collection_account_id"]), str(st["cheque_payable_account_id"])
	if cih == "" || cuc == "" || cp == "" || str(st["retained_earnings_account_id"]) != o.accounts["12"] {
		t.Fatalf("accounting settings: %v", st)
	}
	bank, cash := o.methods["Bank"], o.methods["Cash USD"]
	cust := o.customer(t, "Cheque payer", "")
	inv := o.invoice(t, cust, "2026-11-01", "100")

	// a customer cheque settles the invoice like cash, through "cheques in hand"
	c1 := chequeOf(o.c.ok("POST", o.p("/cheques"), map[string]any{"direction": "in", "cheque_no": "000123", "bank_name": "BLOM",
		"receive_date": "2026-11-02", "due_date": "2026-12-15", "currency": "USD", "amount": "100", "customer_id": cust}, 201))
	id1 := str(c1["id"])
	eqDec(t, "cheques in hand", o.idBalance(t, cih), "100")
	eqDec(t, "receivable after cheque", o.baseBalance(t, "411"), "0")
	if r := o.c.do("GET", o.p("/invoices/"+inv), nil); str(obj(r.body["invoice"])["status"]) != "paid" {
		t.Fatalf("invoice not paid by the cheque: %s", r.raw)
	}
	// its payment cannot be voided directly
	if r := o.c.do("POST", o.p("/payments/"+str(c1["payment_id"])+"/void"), map[string]any{}); r.status != 409 {
		t.Fatalf("void cheque payment: %d %s", r.status, r.raw)
	}
	// frozen cheques cannot be deposited
	o.c.ok("POST", o.p("/cheques/"+id1+"/freeze"), map[string]any{}, 200)
	if r := o.c.do("POST", o.p("/cheques/"+id1+"/deposit"), map[string]any{"method_id": bank}); r.status != 409 {
		t.Fatalf("deposit frozen: %d", r.status)
	}
	o.c.ok("POST", o.p("/cheques/"+id1+"/unfreeze"), map[string]any{}, 200)
	d := chequeOf(o.c.ok("POST", o.p("/cheques/"+id1+"/deposit"), map[string]any{"date": "2026-11-03", "method_id": bank}, 200))
	if str(d["status"]) != "deposited" {
		t.Fatalf("deposit: %v", d)
	}
	eqDec(t, "in hand after deposit", o.idBalance(t, cih), "0")
	eqDec(t, "under collection", o.idBalance(t, cuc), "100")
	// the bank returns it: the customer owes the amount again (a debit on the statement)
	b := chequeOf(o.c.ok("POST", o.p("/cheques/"+id1+"/bounce"), map[string]any{"date": "2026-11-10"}, 200))
	if str(b["status"]) != "bounced" {
		t.Fatalf("bounce: %v", b)
	}
	eqDec(t, "under collection after bounce", o.idBalance(t, cuc), "0")
	eqDec(t, "receivable after bounce", o.baseBalance(t, "411"), "100")
	if r := o.c.do("POST", o.p("/cheques/"+id1+"/deposit"), map[string]any{"method_id": bank}); r.status != 409 {
		t.Fatalf("deposit bounced: %d", r.status)
	}

	// cash on a cheque, then cashed at the counter, then undone
	c2 := chequeOf(o.c.ok("POST", o.p("/cheques"), map[string]any{"direction": "in", "cheque_no": "777", "receive_date": "2026-11-04",
		"due_date": "2026-11-20", "currency": "USD", "amount": "50", "customer_id": cust}, 201))
	id2 := str(c2["id"])
	p := chequeOf(o.c.ok("POST", o.p("/cheques/"+id2+"/partial_cash"), map[string]any{"date": "2026-11-05", "method_id": cash, "amount": "20"}, 200))
	eqDec(t, "remaining after cash", p["remaining"], "30")
	eqDec(t, "in hand after cash", o.idBalance(t, cih), "30")
	o.c.ok("POST", o.p("/cheques/"+id2+"/collect"), map[string]any{"date": "2026-11-06", "method_id": bank}, 200)
	eqDec(t, "in hand after collect", o.idBalance(t, cih), "0")
	u := chequeOf(o.c.ok("POST", o.p("/cheques/"+id2+"/undo"), map[string]any{}, 200))
	if str(u["status"]) != "in_hand" {
		t.Fatalf("undo collect: %v", u)
	}
	eqDec(t, "in hand after undo", o.idBalance(t, cih), "30")

	// endorsed to a supplier, then given back by the supplier
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Fuel Co", "currency": "USD"}, 201).body["id"])
	e := chequeOf(o.c.ok("POST", o.p("/cheques/"+id2+"/endorse"), map[string]any{"date": "2026-11-07", "supplier_id": sup}, 200))
	if str(e["status"]) != "endorsed" {
		t.Fatalf("endorse: %v", e)
	}
	eqDec(t, "in hand after endorse", o.idBalance(t, cih), "0")
	eqDec(t, "payables after endorse", o.baseBalance(t, "401"), "30")
	o.c.ok("POST", o.p("/cheques/"+id2+"/endorse_return"), map[string]any{"date": "2026-11-08"}, 200)
	eqDec(t, "in hand after endorse return", o.idBalance(t, cih), "30")
	eqDec(t, "payables after endorse return", o.baseBalance(t, "401"), "0")

	// cheques we write: paid by the bank, or cancelled
	w1 := chequeOf(o.c.ok("POST", o.p("/cheques"), map[string]any{"direction": "out", "cheque_no": "9001", "receive_date": "2026-11-09",
		"due_date": "2026-12-01", "currency": "USD", "amount": "40", "supplier_id": sup}, 201))
	eqDec(t, "cheques payable", o.idBalance(t, cp), "-40")
	o.c.ok("POST", o.p("/cheques/"+str(w1["id"])+"/pay"), map[string]any{"date": "2026-12-01", "method_id": bank}, 200)
	eqDec(t, "cheques payable after bank paid", o.idBalance(t, cp), "0")
	w2 := chequeOf(o.c.ok("POST", o.p("/cheques"), map[string]any{"direction": "out", "cheque_no": "9002", "receive_date": "2026-11-09",
		"due_date": "2026-12-01", "currency": "USD", "amount": "10", "supplier_id": sup}, 201))
	o.c.ok("POST", o.p("/cheques/"+str(w2["id"])+"/cancel"), map[string]any{}, 200)
	eqDec(t, "cheques payable after cancel", o.idBalance(t, cp), "0")
	eqDec(t, "payables: paid 40 only", o.baseBalance(t, "401"), "40")

	list := o.c.ok("GET", o.p("/cheques?direction=in"), nil, 200).body
	if n := len(list["rows"].([]any)); n != 2 {
		t.Fatalf("cheques listed: %d", n)
	}
	got := o.c.ok("GET", o.p("/cheques/"+id1), nil, 200).body
	if n := len(got["events"].([]any)); n < 5 {
		t.Fatalf("events of cheque 1: %d", n)
	}
	o.trialBalanced(t)
}

func TestCurrencyExchangeVoucher(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-11-01", "89500")
	// selling 100 dollars for 9,000,000 LBP while the official rate is 89,500: 0.5587 gained
	x := o.c.ok("POST", o.p("/exchanges"), map[string]any{"date": "2026-11-05", "from_method_id": o.methods["Cash USD"],
		"to_method_id": o.methods["Cash LBP"], "from_amount": "100", "to_amount": "9000000", "counterparty": "Exchange shop"}, 201).body
	eqDec(t, "exchange gain", x["gain"], "0.5587")
	eqDec(t, "LBP box", o.amountBalance(t, "532", "LBP"), "9000000")
	eqDec(t, "USD box", o.amountBalance(t, "531", "USD"), "-100")
	// by rate
	o.c.ok("POST", o.p("/exchanges"), map[string]any{"date": "2026-11-06", "from_method_id": o.methods["Cash LBP"],
		"to_method_id": o.methods["Cash USD"], "from_amount": "895000", "rate": "0.0000111732"}, 201)
	o.c.ok("POST", o.p("/exchanges/"+str(x["id"])+"/void"), map[string]any{}, 200)
	if r := o.c.do("POST", o.p("/exchanges/"+str(x["id"])+"/void"), map[string]any{}); r.status != 409 {
		t.Fatalf("void twice: %d", r.status)
	}
	if r := o.c.do("POST", o.p("/exchanges"), map[string]any{"from_method_id": o.methods["Cash USD"], "to_method_id": o.methods["Bank"],
		"from_amount": "1", "to_amount": "1"}); r.status != 400 {
		t.Fatalf("same currency exchange: %d %s", r.status, r.raw)
	}
	o.trialBalanced(t)
}

func TestFiscalYearCloseReopen(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	post := func(date, dr, cr, amt string) resp {
		return o.c.do("POST", o.p("/journal"), map[string]any{"date": date, "post": true, "memo": "t", "lines": []any{
			map[string]any{"account_id": o.accounts[dr], "debit": amt}, map[string]any{"account_id": o.accounts[cr], "credit": amt}}})
	}
	if r := post("2025-03-01", "531", "701", "100"); r.status != 201 {
		t.Fatalf("post: %d %s", r.status, r.raw)
	}
	post("2025-05-01", "611", "531", "30")
	c := o.c.ok("POST", o.p("/fiscal-years/close"), map[string]any{"year": 2025}, 200).body
	eqDec(t, "2025 result", c["result"], "70")
	eqDec(t, "retained earnings", o.baseBalance(t, "12"), "-70")
	eqDec(t, "revenue closed", o.baseBalance(t, "701"), "0")
	pl := o.c.ok("GET", o.p("/reports/profit-loss?from=2025-01-01&to=2025-12-31"), nil, 200).body
	eqDec(t, "P&L 2025 still shows the year", pl["net"], "70")
	if r := post("2025-06-01", "611", "531", "1"); r.status != 409 {
		t.Fatalf("posting into a closed year: %d %s", r.status, r.raw)
	}
	if r := o.c.do("POST", o.p("/fiscal-years/close"), map[string]any{"year": 2025}); r.status != 409 {
		t.Fatalf("close twice: %d", r.status)
	}
	o.c.ok("POST", o.p("/fiscal-years/reopen"), map[string]any{"year": 2025}, 200)
	eqDec(t, "retained earnings after reopen", o.baseBalance(t, "12"), "0")
	pl = o.c.ok("GET", o.p("/reports/profit-loss?from=2025-01-01&to=2025-12-31"), nil, 200).body
	eqDec(t, "P&L after reopen", pl["net"], "70")
	if r := post("2025-06-01", "611", "531", "1"); r.status != 201 {
		t.Fatalf("posting after reopen: %d %s", r.status, r.raw)
	}
	o.c.ok("POST", o.p("/fiscal-years/close"), map[string]any{"year": 2025}, 200)
	eqDec(t, "retained earnings after second close", o.baseBalance(t, "12"), "-69")
	ys := o.c.ok("GET", o.p("/fiscal-years"), nil, 200).body
	found := false
	for _, y := range ys["years"].([]any) {
		if str(obj(y)["year"]) == "2025" && str(obj(y)["status"]) == "closed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fiscal years: %v", ys)
	}
	o.trialBalanced(t)
}

func TestCostCentersBudgetsOpeningAndReports(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	station := str(o.c.ok("POST", o.p("/stations"), map[string]any{"name": "North"}, 201).body["id"])
	box := str(o.c.ok("POST", o.p("/boxes"), map[string]any{"station_id": station, "code": "N-1"}, 201).body["id"])
	cc := str(o.c.ok("POST", o.p("/cost-centers"), map[string]any{"code": "N", "name": "North station", "kind": "station", "station_id": station}, 201).body["id"])
	if r := o.c.do("POST", o.p("/cost-centers"), map[string]any{"code": "N", "name": "dup"}); r.status != 409 {
		t.Fatalf("duplicate cost center: %d", r.status)
	}
	plan := o.plan(t, "5A", "40")
	cust := o.customer(t, "Subscriber", "")
	o.c.ok("POST", o.p("/customers/"+cust+"/subscriptions"), map[string]any{"plan_id": plan, "start_date": "2026-01-01", "box_id": box}, 201)
	o.c.ok("POST", o.p("/billing/run"), map[string]any{"period": "2026-11", "issue_date": "2026-11-05"}, 200)
	o.c.ok("POST", o.p("/expenses"), map[string]any{"date": "2026-11-06", "account_id": o.accounts["615"], "method_id": o.methods["Cash USD"],
		"amount": "15", "description": "Repair", "cost_center_id": cc}, 201)
	rep := o.c.ok("GET", o.p("/reports/cost-centers?from=2026-11-01&to=2026-11-30"), nil, 200).body
	var north map[string]any
	for _, c := range rep["centers"].([]any) {
		if str(obj(c)["cost_center_id"]) == cc {
			north = obj(c)
		}
	}
	if north == nil {
		t.Fatalf("cost center report: %v", rep)
	}
	eqDec(t, "north income", north["income"], "40")
	eqDec(t, "north expense", north["expense"], "15")
	eqDec(t, "north result", north["net"], "25")

	// budget against actual
	o.c.ok("PUT", o.p("/budgets"), map[string]any{"year": 2026, "lines": []any{
		map[string]any{"account_id": o.accounts["701"], "month": 11, "amount": "50"},
		map[string]any{"account_id": o.accounts["615"], "month": 11, "amount": "10"}}}, 200)
	bud := o.c.ok("GET", o.p("/reports/budget?year=2026"), nil, 200).body
	ok := false
	for _, r := range bud["rows"].([]any) {
		if str(obj(r)["account_id"]) == o.accounts["701"] {
			eqDec(t, "budget nov", obj(r)["budget"].([]any)[10], "50")
			eqDec(t, "actual nov", obj(r)["actual"].([]any)[10], "40")
			ok = true
		}
	}
	if !ok {
		t.Fatalf("budget report: %v", bud)
	}
	if n := len(o.c.ok("GET", o.p("/budgets?year=2026"), nil, 200).list); n != 2 {
		t.Fatalf("budget lines: %d", n)
	}

	// opening balances: the difference goes to retained earnings
	ob := o.c.ok("POST", o.p("/opening-balances"), map[string]any{"date": "2026-01-01", "lines": []any{
		map[string]any{"account_id": o.accounts["512"], "debit": "1000"},
		map[string]any{"account_id": o.accounts["421"], "credit": "300"}}}, 201).body
	eqDec(t, "opening difference", ob["difference"], "700")
	eqDec(t, "retained earnings from opening", o.baseBalance(t, "12"), "-700")
	// suppliers' balances are opened with an opening bill, never typed on the payables account (audit C-M5)
	if r := o.c.do("POST", o.p("/opening-balances"), map[string]any{"date": "2026-01-01", "lines": []any{
		map[string]any{"account_id": o.accounts["401"], "credit": "300"}}}); r.status != 422 || str(r.body["error"]) != "opening_control_account" {
		if o.accounts["401"] != "" {
			t.Fatalf("opening on payables: %d %s", r.status, r.raw)
		}
	}

	// a payment, then drill-down from its entry to the receipt
	pay := o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-11-07", "currency": "USD",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "40"}}}, 201).body
	je := str(obj(pay["payment"])["journal_entry_id"])
	src := o.c.ok("GET", o.p("/journal/"+je+"/source"), nil, 200).body
	if str(obj(src["document"])["kind"]) != "payment" || str(obj(src["document"])["party_id"]) != cust {
		t.Fatalf("source of payment entry: %v", src)
	}
	// the cost center of a posted line can be corrected; amounts cannot
	entry := o.c.ok("GET", o.p("/journal/"+je), nil, 200).body
	line := str(obj(entry["lines"].([]any)[0])["id"])
	o.c.ok("PATCH", o.p("/journal-lines/"+line), map[string]any{"cost_center_id": cc}, 200)
	o.c.ok("PATCH", o.p("/journal-lines/"+line), map[string]any{"cost_center_id": ""}, 200)

	// capital paid in: financing; the receipt: operating; boxes add up
	o.c.ok("POST", o.p("/journal"), map[string]any{"date": "2026-11-08", "post": true, "memo": "capital", "lines": []any{
		map[string]any{"account_id": o.accounts["531"], "debit": "500"}, map[string]any{"account_id": o.accounts["101"], "credit": "500"}}}, 201)
	cf := o.c.ok("GET", o.p("/reports/cash-flow?from=2026-11-01&to=2026-11-30"), nil, 200).body
	sections := map[string]string{}
	for _, r := range cf["rows"].([]any) {
		x := obj(r)
		v := dec(t, x["amount"])
		if prev, ok := sections[str(x["section"])]; ok {
			v = v.Add(dec(t, prev))
		}
		sections[str(x["section"])] = v.String()
	}
	eqDec(t, "financing", sections["financing"], "500")
	eqDec(t, "operating", sections["operating"], "25") // 40 received − 15 repair
	net := dec(t, cf["closing"]).Sub(dec(t, cf["opening"]))
	eqDec(t, "cash flow adds up", net.String(), "525")

	cmp := o.c.ok("GET", o.p("/reports/compare?kind=pl&p=2026-11-01:2026-11-30&p=2026-10-01:2026-10-31"), nil, 200).body
	if len(cmp["rows"].([]any)) == 0 {
		t.Fatalf("compare: %v", cmp)
	}
	o.c.ok("GET", o.p("/reports/compare?kind=bs&p=2026-11-30&p=2026-10-31"), nil, 200)
	sum := o.c.ok("GET", o.p("/owner-summary"), nil, 200).body
	for _, k := range []string{"collected_today", "collectors_cash", "company_cash", "customers_owe", "month", "attention", "fuel"} {
		if _, ok := sum[k]; !ok {
			t.Fatalf("owner summary misses %s: %v", k, sum)
		}
	}
	o.trialBalanced(t)
}

func TestBarcodePaysScannedBillFirst(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Two bills", "")
	oct := o.invoice(t, cust, "2026-10-05", "20")
	nov := o.invoice(t, cust, "2026-11-05", "20")
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-11-10", "currency": "USD", "invoice_id": nov,
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "20"}}}, 201)
	st := func(id string) string {
		return str(obj(o.c.ok("GET", o.p("/invoices/"+id), nil, 200).body["invoice"])["status"])
	}
	if st(nov) != "paid" || st(oct) != "open" {
		t.Fatalf("scanned bill first: nov %s, oct %s", st(nov), st(oct))
	}
}
