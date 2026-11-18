package httpapi

import (
	"context"
	"testing"
)

// Module H: payroll, fixed assets, recurring entries, bank reconciliation.

func tbBalance(t *testing.T, o *org, code string) string {
	t.Helper()
	tb := o.c.ok("GET", o.p("/reports/trial-balance?from=2000-01-01&to=2100-01-01"), nil, 200)
	for _, r := range tb.body["rows"].([]any) {
		if str(obj(r)["code"]) == code {
			return str(obj(r)["balance"])
		}
	}
	return "0"
}

func TestPayrollPostAndPay(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	usd := str(o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Ali", "currency": "USD", "salary": "1000", "transport": "100",
		"hire_date": "2026-01-01"}, 201).body["id"])
	o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Rami", "currency": "LBP", "salary": "50000000", "nssf_enrolled": false,
		"hire_date": "2026-01-01"}, 201)
	o.c.ok("POST", o.p("/employees/"+usd+"/advances"), map[string]any{"date": "2026-10-05", "amount": "300", "installment": "100",
		"method_id": o.methods["Cash USD"]}, 201)

	run := o.c.ok("POST", o.p("/payroll"), map[string]any{"period": "2026-11"}, 201)
	runID := str(obj(run.body["run"])["id"])
	var slipUSD map[string]any
	for _, s := range run.body["payslips"].([]any) {
		if str(obj(s)["currency"]) == "USD" {
			slipUSD = obj(s)
		}
	}
	eqDec(t, "nssf employee", slipUSD["nssf_employee"], "30")
	eqDec(t, "nssf employer", slipUSD["nssf_employer"], "215")
	eqDec(t, "advance recovery", slipUSD["advance_recovery"], "100")
	eqDec(t, "net", slipUSD["net"], "970")

	// a second run for the same month is refused; editing recomputes the net
	if r := o.c.do("POST", o.p("/payroll"), map[string]any{"period": "2026-11"}); r.status != 409 {
		t.Fatalf("duplicate payroll: %d", r.status)
	}
	ed := o.c.ok("PATCH", o.p("/payroll/"+runID+"/payslips/"+str(slipUSD["id"])), map[string]any{"allowances": "50"}, 200)
	for _, s := range ed.body["payslips"].([]any) {
		if str(obj(s)["currency"]) == "USD" {
			eqDec(t, "net after allowance", obj(s)["net"], "1020")
		}
	}

	o.c.ok("POST", o.p("/payroll/"+runID+"/post"), map[string]any{}, 200)
	o.trialBalanced(t)
	if r := o.c.do("PATCH", o.p("/payroll/"+runID+"/payslips/"+str(slipUSD["id"])), map[string]any{"allowances": "0"}); r.status != 409 {
		t.Fatalf("posted payslip edited: %d", r.status)
	}
	o.c.ok("POST", o.p("/payroll/"+runID+"/pay"), map[string]any{"method_id": o.methods["Cash USD"], "date": "2026-12-01"}, 200)
	paid := o.c.ok("POST", o.p("/payroll/"+runID+"/pay"), map[string]any{"method_id": o.methods["Cash LBP"], "date": "2026-12-01"}, 200)
	for _, s := range paid.body["payslips"].([]any) {
		if !dec(t, obj(s)["paid"]).Equal(dec(t, obj(s)["net"])) {
			t.Fatalf("payslip not fully paid: %v", s)
		}
	}
	// salaries payable is cleared; only the unrecovered advance (300 - 100) stays on 421
	eqDec(t, "421 balance", tbBalance(t, o, "421"), "200")
	if r := o.c.do("POST", o.p("/payroll/"+runID+"/void"), map[string]any{}); r.status != 409 {
		t.Fatalf("paid payroll voided: %d", r.status)
	}
	o.trialBalanced(t)
}

func TestDepreciationIdempotentAndDisposal(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	a := o.c.ok("POST", o.p("/assets"), map[string]any{"name": "Generator 500kVA", "purchase_date": "2026-01-15", "currency": "USD",
		"cost": "30000", "useful_life_months": 120, "asset_account_id": o.accounts["213"], "paid_from_method_id": o.methods["Bank"]}, 201)
	eqDec(t, "monthly", a.body["monthly"], "250")
	r1 := o.c.ok("POST", o.p("/depreciation/run"), map[string]any{"period": "2026-01"}, 200)
	eqDec(t, "first run", r1.body["total"], "250")
	r2 := o.c.ok("POST", o.p("/depreciation/run"), map[string]any{"period": "2026-01"}, 200)
	if str(r2.body["assets"]) != "0" {
		t.Fatalf("second run depreciated again: %v", r2.body)
	}
	reg := o.c.ok("GET", o.p("/reports/assets?to=2026-01-31"), nil, 200)
	eqDec(t, "net book value", reg.body["total_net_book_value"], "29750")
	// asset frozen after depreciation started
	if r := o.c.do("PATCH", o.p("/assets/"+str(a.body["id"])), map[string]any{"useful_life_months": 60}); r.status != 409 {
		t.Fatalf("life changed after depreciation: %d %s", r.status, r.raw)
	}
	d := o.c.ok("POST", o.p("/assets/"+str(a.body["id"])+"/dispose"), map[string]any{"date": "2026-02-10", "proceeds": "29000",
		"method_id": o.methods["Cash USD"]}, 200)
	eqDec(t, "disposal result (loss)", d.body["result"], "-750")
	o.trialBalanced(t)
	eqDec(t, "asset account after disposal", tbBalance(t, o, "213"), "0")
}

func TestRecurringRespectsLock(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	tpl := o.c.ok("POST", o.p("/recurring"), map[string]any{"name": "Rent", "kind": "journal", "frequency": "monthly",
		"next_date": "2026-02-01", "currency": "USD", "lines": []any{
			map[string]any{"account_id": o.accounts["611"], "debit": "500"},
			map[string]any{"account_id": o.accounts["531"], "credit": "500"}}}, 201)
	id := str(tpl.body["id"])
	o.c.ok("PATCH", "/companies/"+o.company, map[string]any{"lock_date": "2026-02-28"}, 200)
	if r := o.c.do("POST", o.p("/recurring/"+id+"/run"), map[string]any{}); r.status < 400 || str(r.body["error"]) != "period_locked" {
		t.Fatalf("recurring posted into a locked period: %d %s", r.status, r.raw)
	}
	// the background worker must not post it either, and records why
	if n, err := itAPI.RunDueRecurring(context.Background()); err != nil {
		t.Fatal(err)
	} else {
		_ = n
	}
	for _, x := range o.c.ok("GET", o.p("/recurring"), nil, 200).list {
		if str(obj(x)["id"]) == id {
			if str(obj(x)["last_error"]) != "period_locked" || str(obj(x)["runs"]) != "0" {
				t.Fatalf("worker result: %v", x)
			}
		}
	}
	o.c.ok("PATCH", o.p("/recurring/"+id), map[string]any{"next_date": "2026-03-01"}, 200)
	run := o.c.ok("POST", o.p("/recurring/"+id+"/run"), map[string]any{}, 200)
	if str(obj(run.body["template"])["next_date"]) != "2026-04-01" {
		t.Fatalf("next date not advanced: %v", run.body["template"])
	}
	// an unbalanced template is refused
	if r := o.c.do("POST", o.p("/recurring"), map[string]any{"name": "Bad", "kind": "journal", "frequency": "monthly", "next_date": "2026-05-01",
		"currency": "USD", "lines": []any{map[string]any{"account_id": o.accounts["611"], "debit": "10"},
			map[string]any{"account_id": o.accounts["531"], "credit": "9"}}}); r.status != 422 {
		t.Fatalf("unbalanced template: %d", r.status)
	}
	o.trialBalanced(t)
}

func TestBankReconciliation(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	bank := o.methods["Bank"]
	o.c.ok("POST", o.p("/journal"), map[string]any{"date": "2026-03-01", "post": true, "memo": "capital", "lines": []any{
		map[string]any{"account_id": o.accounts["512"], "debit": "1000"}, map[string]any{"account_id": o.accounts["101"], "credit": "1000"}}}, 201)
	o.c.ok("POST", o.p("/expenses"), map[string]any{"date": "2026-03-05", "account_id": o.accounts["611"], "method_id": bank,
		"amount": "100", "description": "rent"}, 201)
	st := o.c.ok("POST", o.p("/bank/statements"), map[string]any{"method_id": bank, "name": "March", "lines": []any{
		map[string]any{"date": "2026-03-02", "description": "deposit", "amount": "1000"},
		map[string]any{"date": "2026-03-05", "description": "rent", "amount": "-100"},
		map[string]any{"date": "2026-03-06", "description": "fee", "amount": "-5"}}}, 201)
	sid := str(obj(st.body["statement"])["id"])
	var feeLine string
	for _, l := range st.body["lines"].([]any) {
		if str(obj(l)["description"]) == "fee" {
			feeLine = str(obj(l)["id"])
			if len(obj(l)["suggestions"].([]any)) != 0 {
				t.Fatalf("fee should have no suggestion")
			}
		}
	}
	am := o.c.ok("POST", o.p("/bank/statements/"+sid+"/auto-match"), map[string]any{}, 200)
	if str(am.body["matched"]) != "2" {
		t.Fatalf("auto-match: %v", am.body["matched"])
	}
	o.c.ok("POST", o.p("/bank/lines/"+feeLine+"/entry"), map[string]any{"account_id": o.accounts["627"], "description": "bank fee"}, 200)
	rec := o.c.ok("GET", o.p("/bank/reconciliation?method_id="+bank+"&to=2026-03-31"), nil, 200)
	eqDec(t, "book balance", rec.body["book_balance"], "895")
	eqDec(t, "statement balance", rec.body["statement_balance"], "895")
	eqDec(t, "difference", rec.body["difference"], "0")
	o.trialBalanced(t)
}
