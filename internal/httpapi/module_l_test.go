package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// baseBalance: debit − credit in base currency of one account (posted entries).
func (o *org) baseBalance(t *testing.T, code string) string {
	t.Helper()
	ctx := context.Background()
	var v string
	if err := pgx.BeginFunc(ctx, itOwnerPool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::text FROM journal_lines l
			JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted' WHERE e.company_id = $1 AND l.account_id = $2`,
			o.company, o.accounts[code]).Scan(&v)
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

// amountBalance: debit − credit in one currency of one account.
func (o *org) amountBalance(t *testing.T, code, cur string) string {
	t.Helper()
	ctx := context.Background()
	var v string
	if err := pgx.BeginFunc(ctx, itOwnerPool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(sum(l.debit - l.credit), 0)::text FROM journal_lines l
			JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted' WHERE e.company_id = $1 AND l.account_id = $2 AND l.currency = $3`,
			o.company, o.accounts[code], cur).Scan(&v)
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

func (o *org) vat(t *testing.T) string {
	t.Helper()
	return str(obj(o.c.ok("GET", o.p("/tax-rates"), nil, 200).list[0])["id"])
}

func TestExpenseInputVAT(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	vat := o.vat(t)
	e := o.c.ok("POST", o.p("/expenses"), map[string]any{"date": "2026-11-03", "account_id": o.accounts["611"], "method_id": o.methods["Cash USD"],
		"amount": "100", "description": "Rent", "tax_rate_id": vat}, 201)
	eqDec(t, "expense total", e.body["amount"], "111")
	eqDec(t, "expense tax", e.body["tax_amount"], "11")
	e2 := o.c.ok("POST", o.p("/expenses"), map[string]any{"date": "2026-11-04", "account_id": o.accounts["611"], "method_id": o.methods["Cash USD"],
		"amount": "111", "description": "Rent incl.", "tax_rate_id": vat, "tax_included": true}, 201)
	eqDec(t, "tax included", e2.body["tax_amount"], "11")
	eqDec(t, "rent expense", o.baseBalance(t, "611"), "200")
	vr := o.c.ok("GET", o.p("/reports/vat?from=2026-11-01&to=2026-11-30"), nil, 200)
	eqDec(t, "recoverable", vr.body["recoverable"], "22")
	eqDec(t, "net payable", vr.body["net"], "-22")
	eqDec(t, "taxable purchases", obj(vr.body["rows"].([]any)[0])["taxable_purchases"], "200")
	o.c.ok("POST", o.p("/expenses/"+str(e.body["id"])+"/void"), map[string]any{}, 200)
	vr = o.c.ok("GET", o.p("/reports/vat?from=2026-11-01&to=2100-01-01"), nil, 200)
	eqDec(t, "recoverable after void", vr.body["recoverable"], "11")
	o.trialBalanced(t)
}

func TestStockVoidAndTransfers(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Fuel Co", "currency": "USD"}, 201).body["id"])
	item := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "DSL", "name": "Diesel", "kind": "stock", "unit": "L"}, 201).body["id"])
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
		"lines": []any{map[string]any{"item_id": item, "quantity": "1000", "unit_price": "0.9"}}}, 201)
	main := str(obj(o.c.ok("GET", o.p("/warehouses"), nil, 200).list[0])["id"])
	st2 := str(o.c.ok("POST", o.p("/warehouses"), map[string]any{"name": "Station 2"}, 201).body["id"])
	stock := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, s := range o.c.ok("GET", o.p("/items/"+item), nil, 200).body["stock"].([]any) {
			out[str(obj(s)["warehouse_id"])] = obj(s)
		}
		return out
	}
	if r := o.c.do("POST", o.p("/stock/transfers"), map[string]any{"item_id": item, "from_warehouse_id": main, "to_warehouse_id": st2, "quantity": "5000"}); r.status != 422 {
		t.Fatalf("transfer beyond stock: %d %s", r.status, r.raw)
	}
	tr := o.c.ok("POST", o.p("/stock/transfers"), map[string]any{"item_id": item, "from_warehouse_id": main, "to_warehouse_id": st2,
		"quantity": "400", "date": "2026-11-02"}, 201)
	eqDec(t, "transfer value", tr.body["value"], "360")
	s := stock()
	eqDec(t, "main qty", s[main]["quantity"], "600")
	eqDec(t, "station qty", s[st2]["quantity"], "400")
	eqDec(t, "station avg", s[st2]["avg_cost"], "0.9")
	eqDec(t, "inventory (transfer has no ledger effect)", o.baseBalance(t, "31"), "900")

	fuel := o.c.ok("POST", o.p("/stock/fuel"), map[string]any{"item_id": item, "warehouse_id": st2, "quantity": "100", "date": "2026-11-03"}, 201)
	if r := o.c.do("POST", o.p("/stock/transfers/"+str(tr.body["id"])+"/void"), map[string]any{}); r.status != 422 {
		t.Fatalf("void transfer whose stock was used: %d %s", r.status, r.raw)
	}
	o.c.ok("POST", o.p("/stock/docs/"+str(fuel.body["id"])+"/void"), map[string]any{"reason": "typo"}, 200)
	eqDec(t, "fuel expense after void", o.baseBalance(t, "601"), "0")
	eqDec(t, "station qty after void", stock()[st2]["quantity"], "400")
	if r := o.c.do("POST", o.p("/stock/docs/"+str(fuel.body["id"])+"/void"), map[string]any{}); r.status != 409 {
		t.Fatalf("voided twice: %d", r.status)
	}
	o.c.ok("POST", o.p("/stock/transfers/"+str(tr.body["id"])+"/void"), map[string]any{}, 200)
	s = stock()
	eqDec(t, "main qty back", s[main]["quantity"], "1000")
	eqDec(t, "main value back", s[main]["value"], "900")

	// adjustment in, voided
	adj := o.c.ok("POST", o.p("/stock/adjust"), map[string]any{"item_id": item, "quantity": "50", "unit_cost": "1.2", "date": "2026-11-04"}, 201)
	eqDec(t, "inventory after adjust", o.baseBalance(t, "31"), "960")
	o.c.ok("POST", o.p("/stock/docs/"+str(adj.body["id"])+"/void"), map[string]any{}, 200)
	eqDec(t, "inventory after adjust void", o.baseBalance(t, "31"), "900")
	eqDec(t, "main qty after adjust void", stock()[main]["quantity"], "1000")

	mv := o.c.ok("GET", o.p("/reports/stock-moves?from=2026-11-01&to=2026-11-30&item_id="+item), nil, 200)
	if n := len(mv.body["rows"].([]any)); n < 8 {
		t.Fatalf("stock moves report rows = %d", n)
	}
	val := o.c.ok("GET", o.p("/reports/stock-valuation"), nil, 200)
	_ = val
	o.trialBalanced(t)
}

func TestAdvanceAppliedLaterLeavesNoFXResidue(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	cust := o.customer(t, "Early payer", "")
	pay := o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-02-01", "currency": "LBP",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash LBP"], "amount": "895000"}}}, 201) // advance = 10 USD
	o.rate(t, "LBP", "2026-03-01", "100000")
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "LBP", "issue_date": "2026-03-05",
		"lines": []any{map[string]any{"description": "Monthly", "quantity": "1", "unit_price": "895000"}}}, 201) // = 8.95 USD
	if str(obj(inv.body["invoice"])["status"]) != "paid" {
		t.Fatalf("advance not applied: %v", obj(inv.body["invoice"])["status"])
	}
	eqDec(t, "receivable base", o.baseBalance(t, "411"), "0")
	eqDec(t, "receivable LBP", o.amountBalance(t, "411", "LBP"), "0")
	eqDec(t, "exchange gain", o.baseBalance(t, "766"), "-1.05")
	// voiding the payment reopens the invoice at its own value and reverses the FX entry
	o.c.ok("POST", o.p("/payments/"+str(obj(pay.body["payment"])["id"])+"/void"), map[string]any{}, 200)
	eqDec(t, "receivable after void", o.baseBalance(t, "411"), "8.95")
	eqDec(t, "exchange gain after void", o.baseBalance(t, "766"), "0")
	o.trialBalanced(t)
}

func TestCreditNotesAndRefunds(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	vat := o.vat(t)
	cust := o.customer(t, "Refunded", "")
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-05",
		"lines": []any{map[string]any{"description": "Installation", "quantity": "1", "unit_price": "100", "tax_rate_id": vat}}}, 201)
	invID := str(obj(inv.body["invoice"])["id"])
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-11-05", "currency": "USD",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "111"}}}, 201)
	if r := o.c.do("POST", o.p("/invoices/"+invID+"/credit-note"), map[string]any{"amount": "200"}); r.status != 422 {
		t.Fatalf("over-credit accepted: %d %s", r.status, r.raw)
	}
	cn := o.c.ok("POST", o.p("/invoices/"+invID+"/credit-note"), map[string]any{"date": "2026-11-06", "reason": "cancelled",
		"refund_method_id": o.methods["Cash USD"]}, 201)
	if str(obj(cn.body["payment"])["kind"]) != "credit_note" {
		t.Fatalf("kind = %v", obj(cn.body["payment"])["kind"])
	}
	eqDec(t, "revenue", o.baseBalance(t, "701"), "0")
	eqDec(t, "cash", o.baseBalance(t, "531"), "0")
	eqDec(t, "receivable", o.baseBalance(t, "411"), "0")
	vr := o.c.ok("GET", o.p("/reports/vat?from=2026-11-01&to=2026-11-30"), nil, 200)
	eqDec(t, "VAT collected after credit note", vr.body["collected"], "0")
	c := o.c.ok("GET", o.p("/customers/"+cust), nil, 200)
	if credit := obj(c.body["customer"])["credit"]; credit != nil && len(credit.([]any)) != 0 {
		t.Fatalf("credit left after refund: %v", credit)
	}

	// partial credit note on an unpaid invoice
	inv2 := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-07",
		"lines": []any{map[string]any{"description": "Repair", "quantity": "1", "unit_price": "100"}}}, 201)
	inv2ID := str(obj(inv2.body["invoice"])["id"])
	cn2 := o.c.ok("POST", o.p("/invoices/"+inv2ID+"/credit-note"), map[string]any{"amount": "40", "date": "2026-11-08"}, 201)
	got := obj(o.c.ok("GET", o.p("/invoices/"+inv2ID), nil, 200).body["invoice"])
	eqDec(t, "balance after partial credit", got["balance"], "60")
	if r := o.c.do("POST", o.p("/invoices/"+inv2ID+"/credit-note"), map[string]any{"amount": "70"}); r.status != 422 {
		t.Fatalf("credit beyond what is left accepted: %d", r.status)
	}
	o.c.ok("POST", o.p("/payments/"+str(obj(cn2.body["payment"])["id"])+"/void"), map[string]any{}, 200)
	got = obj(o.c.ok("GET", o.p("/invoices/"+inv2ID), nil, 200).body["invoice"])
	eqDec(t, "balance after credit note void", got["balance"], "100")

	// advance → refund → void refund puts the credit back
	cust2 := o.customer(t, "Advance", "")
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust2, "date": "2026-11-05", "currency": "USD",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "20"}}}, 201)
	if r := o.c.do("POST", o.p("/customers/"+cust2+"/refunds"), map[string]any{"currency": "USD", "amount": "25", "method_id": o.methods["Cash USD"]}); r.status != 422 {
		t.Fatalf("refund above credit: %d %s", r.status, r.raw)
	}
	ref := o.c.ok("POST", o.p("/customers/"+cust2+"/refunds"), map[string]any{"currency": "USD", "amount": "20", "method_id": o.methods["Cash USD"], "date": "2026-11-06"}, 201)
	ri := obj(ref.body["invoice"])
	if str(ri["kind"]) != "refund" || str(ri["status"]) != "paid" {
		t.Fatalf("refund = %v / %v", ri["kind"], ri["status"])
	}
	o.c.ok("POST", o.p("/invoices/"+str(ri["id"])+"/void"), map[string]any{}, 200)
	c2 := o.c.ok("GET", o.p("/customers/"+cust2), nil, 200)
	cr := obj(c2.body["customer"])["credit"].([]any)
	eqDec(t, "credit back after refund void", obj(cr[0])["credit"], "20")
	o.trialBalanced(t)
}

func TestSupplierDebitNoteReturnsStock(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	vat := o.vat(t)
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Parts Co", "currency": "USD"}, 201).body["id"])
	item := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "CBL", "name": "Cable", "kind": "stock", "unit": "m"}, 201).body["id"])
	b := o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
		"lines": []any{map[string]any{"item_id": item, "quantity": "100", "unit_price": "1", "tax_rate_id": vat}}}, 201)
	billID := str(obj(b.body["bill"])["id"])
	dn := o.c.ok("POST", o.p("/bills/"+billID+"/debit-note"), map[string]any{"amount": "55.5", "date": "2026-11-02", "reason": "half returned"}, 201)
	eqDec(t, "bill balance", obj(dn.body["bill"])["balance"], "55.5")
	st := obj(o.c.ok("GET", o.p("/items/"+item), nil, 200).body["stock"].([]any)[0])
	eqDec(t, "stock after return", st["quantity"], "50")
	eqDec(t, "inventory", o.baseBalance(t, "31"), "50")
	eqDec(t, "VAT recoverable", o.baseBalance(t, "4426"), "5.5")
	eqDec(t, "payables", o.baseBalance(t, "401"), "-55.5")
	list := o.c.ok("GET", o.p("/supplier-payments?kind=debit_note&from=2026-01-01&to=2026-12-31"), nil, 200).list
	if len(list) != 1 {
		t.Fatalf("debit notes listed: %d", len(list))
	}
	o.c.ok("POST", o.p("/supplier-payments/"+str(obj(list[0])["id"])+"/void"), map[string]any{}, 200)
	st = obj(o.c.ok("GET", o.p("/items/"+item), nil, 200).body["stock"].([]any)[0])
	eqDec(t, "stock after debit note void", st["quantity"], "100")
	eqDec(t, "inventory after void", o.baseBalance(t, "31"), "100")
	o.trialBalanced(t)
}

func TestBillingRunInBackground(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	plan := o.plan(t, "5A", "20")
	for _, n := range []string{"A", "B", "C", "D", "E"} {
		c := o.customer(t, "Sub "+n, "")
		o.c.ok("POST", o.p("/customers/"+c+"/subscriptions"), map[string]any{"plan_id": plan, "start_date": "2026-01-01"}, 201)
	}
	old := billingChunk
	billingChunk = 2
	defer func() { billingChunk = old }()
	wait := func(id string) map[string]any {
		for i := 0; i < 100; i++ {
			j := o.c.ok("GET", o.p("/billing/jobs/"+id), nil, 200).body
			if str(j["status"]) != "running" {
				return j
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("billing job did not finish")
		return nil
	}
	job := o.c.ok("POST", o.p("/billing/run"), map[string]any{"period": "2026-11", "issue_date": "2026-11-05", "async": true}, 202)
	if str(job.body["total"]) != "5" {
		t.Fatalf("job total = %v", job.body["total"])
	}
	j := wait(str(job.body["id"]))
	if str(j["status"]) != "done" || str(j["created"]) != "5" || str(j["processed"]) != "5" {
		t.Fatalf("job = %v", j)
	}
	eqDec(t, "job total USD", obj(j["totals"])["USD"], "100")
	again := o.c.ok("POST", o.p("/billing/run"), map[string]any{"period": "2026-11", "issue_date": "2026-11-05", "async": true}, 202)
	j = wait(str(again.body["id"]))
	if str(j["created"]) != "0" {
		t.Fatalf("re-run created %v", j["created"])
	}
	inv := o.c.ok("GET", o.p("/invoices?period=2026-11"), nil, 200)
	if n := len(inv.body["rows"].([]any)); n != 5 {
		t.Fatalf("invoices = %d", n)
	}
	o.trialBalanced(t)
}

func TestExportXLSX(t *testing.T) {
	needDB(t)
	o := signup(t, "generic")
	body, _ := json.Marshal(map[string]any{"title": "Trial balance", "rtl": true,
		"columns": []any{map[string]any{"label": "Account"}, map[string]any{"label": "Debit", "numeric": true}},
		"rows":    []any{[]any{"Cash", "1,250.50"}, []any{"Revenue", 3}}, "footer": []any{[]any{"Total", "1253.5"}}})
	req, _ := http.NewRequest("POST", itServer.URL+"/api/v1/export/xlsx", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "paxve")
	req.Header.Set("X-Client-IP", o.c.ip)
	res, err := o.c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
		len(raw) < 1000 || string(raw[:2]) != "PK" {
		t.Fatalf("export: %d %s %d bytes", res.StatusCode, res.Header.Get("Content-Type"), len(raw))
	}
}

// TestBillingRunLargePerf bills PAXVE_PERF_SUBS subscriptions (e.g. 50000) in the background and reports the speed.
// Skipped unless the variable is set: PAXVE_PERF_SUBS=50000 make test
func TestBillingRunLargePerf(t *testing.T) {
	needDB(t)
	n, _ := strconv.Atoi(os.Getenv("PAXVE_PERF_SUBS"))
	if n <= 0 {
		t.Skip("PAXVE_PERF_SUBS not set")
	}
	o := signup(t, "lebanon")
	plan := o.plan(t, "Perf", "25")
	ctx := context.Background()
	// bulk-create customers and subscriptions directly (the import API is tested elsewhere)
	if err := pgx.BeginFunc(ctx, itOwnerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE tenants SET plan = 'pro' WHERE id = $1`, o.tenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO customers (tenant_id, company_id, code, name)
			SELECT $1, $2, 'P' || g, 'Perf customer ' || lpad(g::text, 6, '0') FROM generate_series(1, $3) g`, o.tenant, o.company, n); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO subscriptions (tenant_id, company_id, customer_id, plan_id, start_date)
			SELECT $1, $2, c.id, $3, '2026-01-01' FROM customers c WHERE c.company_id = $2`, o.tenant, o.company, plan)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	job := o.c.ok("POST", o.p("/billing/run"), map[string]any{"period": "2026-11", "issue_date": "2026-11-05"}, 202)
	id := str(job.body["id"])
	var j map[string]any
	for {
		j = o.c.ok("GET", o.p("/billing/jobs/"+id), nil, 200).body
		if str(j["status"]) != "running" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	took := time.Since(start)
	if str(j["status"]) != "done" || str(j["created"]) != strconv.Itoa(n) {
		t.Fatalf("job = %v", j)
	}
	t.Logf("billed %d subscriptions in %s (%.0f invoices/s)", n, took.Round(time.Second), float64(n)/took.Seconds())
	o.trialBalanced(t)
}
