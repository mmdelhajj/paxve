package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/charts"
)

// Module T: S leftovers (cost centers on payroll / depreciation, cheque debits), world currencies,
// automatic rates, presentation currency, country packs, languages and tenant translations.

func TestCostCenterOnPayrollAndDepreciation(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cc := str(o.c.ok("POST", o.p("/cost-centers"), map[string]any{"code": "ST1", "name": "Station 1", "kind": "station"}, 201).body["id"])
	emp := o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Ali", "currency": "USD", "salary": "500", "nssf_enrolled": false,
		"hire_date": "2026-01-01", "cost_center_id": cc}, 201).body
	if str(emp["cost_center_id"]) != cc {
		t.Fatalf("employee cost center: %v", emp)
	}
	run := o.c.ok("POST", o.p("/payroll"), map[string]any{"period": "2026-10"}, 201).body
	runID := str(obj(run["run"])["id"])
	o.c.ok("POST", o.p("/payroll/"+runID+"/post"), map[string]any{}, 200)
	asset := o.c.ok("POST", o.p("/assets"), map[string]any{"name": "Generator 2", "purchase_date": "2026-01-10", "currency": "USD", "cost": "1200",
		"useful_life_months": 12, "asset_account_id": o.accounts["213"], "cost_center_id": cc}, 201).body
	if str(asset["cost_center_id"]) != cc {
		t.Fatalf("asset cost center: %v", asset)
	}
	o.c.ok("POST", o.p("/depreciation/run"), map[string]any{"period": "2026-10"}, 200)
	// both expense lines carry the cost center
	var tagged int
	ctx := context.Background()
	if err := itOwnerPool.QueryRow(ctx, `SELECT count(*) FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id
		WHERE e.company_id = $1 AND l.cost_center_id = $2 AND e.source IN ('payroll', 'depreciation')`, o.company, cc).Scan(&tagged); err != nil {
		t.Fatal(err)
	}
	if tagged < 2 {
		t.Fatalf("tagged expense lines = %d, want salary + depreciation", tagged)
	}
	o.trialBalanced(t)
}

func TestBouncedChequeDebitIsLabelled(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Cheque payer", "")
	c := chequeOf(o.c.ok("POST", o.p("/cheques"), map[string]any{"direction": "in", "cheque_no": "42", "receive_date": "2026-11-02",
		"due_date": "2026-11-20", "currency": "USD", "amount": "60", "customer_id": cust}, 201))
	o.c.ok("POST", o.p("/cheques/"+str(c["id"])+"/deposit"), map[string]any{"date": "2026-11-03", "method_id": o.methods["Bank"]}, 200)
	o.c.ok("POST", o.p("/cheques/"+str(c["id"])+"/bounce"), map[string]any{"date": "2026-11-10"}, 200)
	inv := o.c.ok("GET", o.p("/invoices?customer_id="+cust), nil, 200)
	var debit map[string]any
	for _, x := range inv.body["rows"].([]any) {
		if str(obj(x)["cheque_id"]) == str(c["id"]) {
			debit = obj(x)
		}
	}
	if debit == nil {
		t.Fatalf("no cheque debit found: %s", inv.raw)
	}
	if r := o.c.do("POST", o.p("/invoices/"+str(debit["id"])+"/void"), map[string]any{"reason": "x"}); r.status != 409 || str(r.body["error"]) != "cheque_debit" {
		t.Fatalf("void cheque debit: %d %s", r.status, r.raw)
	}
	st := o.c.ok("GET", "/companies/"+o.company+"/customers/"+cust+"/statement?from=2026-01-01&to=2026-12-31", nil, 200)
	if !strings.Contains(st.raw, `"cheque_return"`) {
		t.Fatalf("statement does not label the returned cheque: %s", st.raw)
	}
}

func TestWorldCurrencies(t *testing.T) {
	needDB(t)
	o := signup(t, "generic")
	list := o.c.ok("GET", "/currencies", nil, 200).list
	if len(list) < 150 {
		t.Fatalf("currencies = %d, want every ISO 4217 currency", len(list))
	}
	want := map[string]string{"KWD": "3", "BHD": "3", "OMR": "3", "JOD": "3", "TND": "3", "LYD": "3", "IQD": "3", "JPY": "0", "KRW": "0", "LBP": "0", "EUR": "2"}
	for _, x := range list {
		m := obj(x)
		if d, ok := want[str(m["code"])]; ok && str(m["decimals"]) != d {
			t.Fatalf("%s decimals = %v, want %s", m["code"], m["decimals"], d)
		}
		if str(m["code"]) == "SAR" && str(m["name_ar"]) == "" {
			t.Fatalf("SAR without an Arabic name: %v", m)
		}
	}
}

func TestAutoRatesNeverOverwriteManual(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon") // base USD, local LBP, boxes in USD and LBP
	o.rate(t, "EUR", "2026-01-01", "0.9")
	o.rate(t, "LBP", today(), "89500") // typed by hand today
	itAPI.RateFeed = func(ctx context.Context) (map[string]decimal.Decimal, error) {
		return map[string]decimal.Decimal{"USD": decimal.NewFromInt(1), "EUR": decimal.RequireFromString("0.92"),
			"LBP": decimal.RequireFromString("89700"), "SAR": decimal.RequireFromString("3.75")}, nil
	}
	defer func() { itAPI.RateFeed = nil }()
	r := o.c.ok("POST", o.p("/rates/fetch"), map[string]any{}, 200).body
	upd := str(r["updated"])
	if !strings.Contains(upd, "EUR") || strings.Contains(upd, "LBP") {
		t.Fatalf("updated = %v, want EUR and never LBP (kept by hand)", r["updated"])
	}
	latest := o.c.ok("GET", o.p("/rates/latest"), nil, 200).body
	for _, x := range latest["rates"].([]any) {
		m := obj(x)
		switch str(m["currency"]) {
		case "EUR":
			eqDec(t, "EUR auto rate", m["rate"], "0.92")
			if str(m["source"]) != "auto" {
				t.Fatalf("EUR source = %v", m["source"])
			}
		case "LBP":
			eqDec(t, "LBP manual rate", m["rate"], "89500")
		}
	}
	// a rate typed afterwards wins and is never overwritten by the next fetch
	o.rate(t, "EUR", today(), "0.95")
	o.c.ok("POST", o.p("/rates/fetch"), map[string]any{}, 200)
	for _, x := range o.c.ok("GET", o.p("/rates/latest"), nil, 200).body["rates"].([]any) {
		if m := obj(x); str(m["currency"]) == "EUR" {
			eqDec(t, "EUR after manual", m["rate"], "0.95")
		}
	}
	// settings
	s := o.c.ok("PUT", o.p("/rates/settings"), map[string]any{"auto_rates": true, "manual_rate_currencies": []string{"lbp", "SYP"}}, 200).body
	if s["auto_rates"] != true || !strings.Contains(str(s["manual_rate_currencies"]), "SYP") {
		t.Fatalf("rate settings: %v", s)
	}
}

func TestPresentationCurrency(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	cust := o.customer(t, "P", "")
	o.invoice(t, cust, "2026-03-01", "10")
	base := o.c.ok("GET", o.p("/reports/profit-loss?from=2026-01-01&to=2026-12-31"), nil, 200).body
	lbp := o.c.ok("GET", o.p("/reports/profit-loss?from=2026-01-01&to=2026-12-31&currency=LBP"), nil, 200).body
	eqDec(t, "income USD", base["total_income"], "10")
	eqDec(t, "income LBP", lbp["total_income"], "895000")
	if str(lbp["presentation_currency"]) != "LBP" {
		t.Fatalf("presentation: %v", lbp)
	}
	tb := o.c.ok("GET", o.p("/reports/trial-balance?from=2026-01-01&to=2026-12-31&currency=LBP"), nil, 200).body
	if !dec(t, tb["total_debit"]).Equal(dec(t, tb["total_credit"])) {
		t.Fatalf("translated trial balance not balanced: %v", tb)
	}
	o.c.ok("GET", o.p("/reports/balance-sheet?to=2026-12-31&currency=LBP"), nil, 200)
}

func TestCountryPacks(t *testing.T) {
	needDB(t)
	for _, tpl := range charts.All {
		tpl := tpl
		t.Run(tpl.Key, func(t *testing.T) {
			o := signup(t, tpl.Key)
			co := obj(o.c.ok("GET", "/companies", nil, 200).list[0])
			if tpl.Country != "" && str(co["country"]) != tpl.Country {
				t.Fatalf("country = %v, want %s", co["country"], tpl.Country)
			}
			if len(o.accounts) != len(tpl.Accounts) {
				t.Fatalf("accounts = %d, want %d", len(o.accounts), len(tpl.Accounts))
			}
			// every role points at an account of the chart
			for role, code := range tpl.Roles {
				if !strings.HasPrefix(role, "_") && o.accounts[code] == "" {
					t.Fatalf("role %s → %s: no such account", role, code)
				}
			}
			taxes := o.c.ok("GET", o.p("/tax-rates"), nil, 200).list
			if len(tpl.Taxes) > 0 && len(taxes) != len(tpl.Taxes) {
				t.Fatalf("taxes = %d, want %d", len(taxes), len(tpl.Taxes))
			}
			// the accounting settings use the pack's accounts (not accounts created later)
			st := o.c.ok("GET", o.p("/accounting/settings"), nil, 200).body
			if re := tpl.Roles["retained_earnings"]; re != "" && str(st["retained_earnings_account_id"]) != o.accounts[re] {
				t.Fatalf("retained earnings = %v, want %s", st["retained_earnings_account_id"], re)
			}
			// sell and collect: the books balance
			o.rate(t, "LBP", "2026-01-01", "89500")
			cust := o.customer(t, "C", "")
			o.invoice(t, cust, "2026-05-02", "100")
			var cash string
			for name, id := range o.methods {
				if strings.Contains(strings.ToLower(name), "cash") || strings.Contains(name, "USD") {
					cash = id
				}
			}
			if cash == "" {
				for _, id := range o.methods {
					cash = id
				}
			}
			o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-05-03", "currency": "USD",
				"tenders": []any{map[string]any{"method_id": cash, "amount": "100"}}}, 201)
			o.trialBalanced(t)
		})
	}
}

func TestZatcaFieldsOnInvoice(t *testing.T) {
	needDB(t)
	o := signup(t, "sa")
	o.c.ok("PUT", o.p("/documents/settings"), map[string]any{"tax_id": "300000000000003"}, 200)
	cust := o.customer(t, "Buyer", "")
	id := o.invoice(t, cust, "2026-06-01", "115")
	inv := obj(o.c.ok("GET", o.p("/invoices/"+id), nil, 200).body["invoice"])
	if str(inv["company_einvoice"]) != "zatca" || str(inv["company_tax_id"]) != "300000000000003" || str(inv["company_invoice_title"]) == "" {
		t.Fatalf("zatca fields: %v", inv)
	}
}

func TestLanguagesAndTranslations(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.c.ok("PATCH", "/me", map[string]any{"locale": "ckb"}, 200)
	if me := o.c.ok("GET", "/me", nil, 200).body; str(me["locale"]) != "ckb" {
		t.Fatalf("user locale: %v", me)
	}
	cust := str(o.c.ok("POST", o.p("/customers"), map[string]any{"name": "Tr", "locale": "tr"}, 201).body["id"])
	if c := o.c.ok("GET", o.p("/customers/"+cust), nil, 200).raw; !strings.Contains(c, `"locale":"tr"`) {
		t.Fatalf("customer locale: %s", c)
	}
	if r := o.c.do("POST", o.p("/customers"), map[string]any{"name": "Bad", "locale": "x1"}); r.status != 400 {
		t.Fatalf("bad locale accepted: %d", r.status)
	}
	o.c.ok("PUT", "/translations", map[string]any{"lang": "ar", "key": "mx.j.save", "value": "حفظ وطباعة الإيصال"}, 200)
	got := o.c.ok("GET", "/translations?lang=ar", nil, 200).body
	if str(got["mx.j.save"]) != "حفظ وطباعة الإيصال" {
		t.Fatalf("translations: %v", got)
	}
	o.c.ok("PUT", "/translations", map[string]any{"lang": "ar", "key": "mx.j.save", "value": ""}, 200)
	if got := o.c.ok("GET", "/translations?lang=ar", nil, 200).body; len(got) != 0 {
		t.Fatalf("translation not removed: %v", got)
	}
	// another tenant does not see this tenant's wording
	o.c.ok("PUT", "/translations", map[string]any{"lang": "en", "key": "common.save", "value": "Keep"}, 200)
	o2 := signup(t, "generic")
	if got := o2.c.ok("GET", "/translations?lang=en", nil, 200).body; len(got) != 0 {
		t.Fatalf("translations leak across tenants: %v", got)
	}
	o.c.ok("PATCH", o.p(""), map[string]any{"default_locale": "fr", "invoice_title": "Facture"}, 200)
}
