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

func TestStockPro(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Fuel Co", "currency": "USD"}, 201).body["id"])
	main := str(obj(o.c.ok("GET", o.p("/warehouses"), nil, 200).list[0])["id"])
	bill := func(lines ...any) string {
		return str(obj(o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD", "lines": lines}, 201).body["bill"])["id"])
	}
	stockOf := func(item string) map[string]any {
		for _, s := range o.c.ok("GET", o.p("/items/"+item), nil, 200).body["stock"].([]any) {
			if str(obj(s)["warehouse_id"]) == main {
				return obj(s)
			}
		}
		return map[string]any{"quantity": "0", "value": "0"}
	}

	// units: diesel bought by the barrel (200 L), FIFO costing
	dsl := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "DSL", "name": "Diesel", "kind": "stock", "unit": "L"}, 201).body["id"])
	o.c.ok("PUT", o.p("/items/"+dsl+"/stock-settings"), map[string]any{"costing": "fifo"}, 200)
	ex := o.c.ok("PUT", o.p("/items/"+dsl+"/units"), map[string]any{"units": []any{map[string]any{"name": "Barrel", "factor": "200"}}}, 200).body
	barrel := str(obj(ex["units"].([]any)[0])["id"])
	bill(map[string]any{"item_id": dsl, "unit_id": barrel, "quantity": "2", "unit_price": "180"}) // 400 L at 0.90
	bill(map[string]any{"item_id": dsl, "quantity": "400", "unit_price": "1.10"})                 // 400 L at 1.10
	st := stockOf(dsl)
	eqDec(t, "diesel qty (barrels → litres)", st["quantity"], "800")
	eqDec(t, "diesel value", st["value"], "800")
	// FIFO: 500 L used = 400 × 0.90 + 100 × 1.10 = 470
	o.c.ok("POST", o.p("/stock/fuel"), map[string]any{"item_id": dsl, "quantity": "500", "date": "2026-11-05"}, 201)
	eqDec(t, "diesel value after FIFO use", stockOf(dsl)["value"], "330")
	eqDec(t, "inventory = stock value", o.baseBalance(t, "31"), "330")

	// serial numbers: routers
	rtr := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "RTR", "name": "Router", "kind": "stock", "unit": "pcs"}, 201).body["id"])
	o.c.ok("PUT", o.p("/items/"+rtr+"/stock-settings"), map[string]any{"tracking": "serial"}, 200)
	if r := o.c.do("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
		"lines": []any{map[string]any{"item_id": rtr, "quantity": "2", "unit_price": "30"}}}); r.status != 422 {
		t.Fatalf("serials required: %d %s", r.status, r.raw)
	}
	bill(map[string]any{"item_id": rtr, "quantity": "2", "unit_price": "30", "lots": []any{
		map[string]any{"lot_no": "SN-001", "quantity": "1"}, map[string]any{"lot_no": "SN-002", "quantity": "1"}}})
	lots := o.c.ok("GET", o.p("/stock/lots?item_id="+rtr), nil, 200).list
	if len(lots) != 2 {
		t.Fatalf("lots: %v", lots)
	}
	// a stock count finds one router missing: the oldest serial leaves
	cnt := o.c.ok("POST", o.p("/stock/counts"), map[string]any{"warehouse_id": main, "date": "2026-11-06"}, 201).body
	cid := str(obj(cnt["count"])["id"])
	o.c.ok("PUT", o.p("/stock/counts/"+cid+"/lines"), map[string]any{"lines": []any{map[string]any{"item_id": rtr, "counted": "1"}}}, 200)
	o.c.ok("POST", o.p("/stock/counts/"+cid+"/post"), map[string]any{}, 200)
	eqDec(t, "routers after count", stockOf(rtr)["quantity"], "1")
	tr := o.c.ok("GET", o.p("/stock/lots/"+str(obj(lots[0])["id"])+"/trace"), nil, 200).body
	if len(tr["moves"].([]any)) != 2 {
		t.Fatalf("trace of %v: %v", obj(lots[0])["lot_no"], tr["moves"])
	}
	if r := o.c.do("POST", o.p("/stock/counts/"+cid+"/post"), map[string]any{}); r.status != 409 {
		t.Fatalf("count posted twice: %d", r.status)
	}

	// landed cost on the router bill: freight 10 → the router still in stock carries half, the missing one went to the count
	rbill := ""
	for _, b := range o.c.ok("GET", o.p("/bills"), nil, 200).body["rows"].([]any) {
		if str(obj(b)["total"]) == "60.0000" {
			rbill = str(obj(b)["id"])
		}
	}
	if rbill == "" {
		t.Fatal("router bill not found")
	}
	o.c.ok("POST", o.p("/bills/"+rbill+"/landed-costs"), map[string]any{"currency": "USD", "amount": "10", "method_id": o.methods["Cash USD"], "date": "2026-11-07"}, 201)
	eqDec(t, "router value with freight", stockOf(rtr)["value"], "35")

	// kit: installation kit = 1 router + 20 L diesel (stand-in component)
	kit := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "KIT", "name": "Install kit", "kind": "consumable", "unit": "kit"}, 201).body["id"])
	o.c.ok("PUT", o.p("/items/"+kit+"/components"), map[string]any{"components": []any{
		map[string]any{"component_id": rtr, "quantity": "1"}, map[string]any{"component_id": dsl, "quantity": "20"}}}, 200)
	cust := o.customer(t, "Kit buyer", "")
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-08",
		"lines": []any{map[string]any{"item_id": kit, "description": "Kit", "quantity": "1", "unit_price": "80"}}}, 201)
	eqDec(t, "routers after kit sale", stockOf(rtr)["quantity"], "0")
	eqDec(t, "diesel after kit sale", stockOf(dsl)["quantity"], "280")

	// reorder rule and replenishment
	o.c.ok("PUT", o.p("/stock/reorder-rules"), map[string]any{"item_id": dsl, "warehouse_id": main, "min_qty": "300", "max_qty": "1000", "supplier_id": sup}, 200)
	rep := o.c.ok("GET", o.p("/reports/stock-replenishment"), nil, 200).body["rows"].([]any)
	found := false
	for _, x := range rep {
		if m := obj(x); str(m["item_id"]) == dsl {
			found = true
			eqDec(t, "to order", m["to_order"], "720")
			if m["days_left"] == nil {
				t.Fatalf("days left missing: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("replenishment: %v", rep)
	}

	// price list with a quantity break
	pl := str(o.c.ok("POST", o.p("/price-lists"), map[string]any{"name": "Wholesale", "currency": "USD"}, 201).body["id"])
	o.c.ok("PUT", o.p("/price-lists/"+pl+"/items"), map[string]any{"items": []any{
		map[string]any{"item_id": dsl, "min_qty": "0", "price": "1.30"}, map[string]any{"item_id": dsl, "min_qty": "100", "price": "1.20"}}}, 200)
	o.c.ok("PUT", o.p("/customers/"+cust+"/price-list"), map[string]any{"price_list_id": pl}, 200)
	eqDec(t, "price 10 L", o.c.ok("GET", o.p("/items/"+dsl+"/price?customer_id="+cust+"&quantity=10&currency=USD"), nil, 200).body["price"], "1.3")
	eqDec(t, "price 150 L", o.c.ok("GET", o.p("/items/"+dsl+"/price?customer_id="+cust+"&quantity=150&currency=USD"), nil, 200).body["price"], "1.2")

	o.trialBalanced(t)
	// valuation equals the inventory ledger
	var val string
	if err := itOwnerPool.QueryRow(context.Background(), `SELECT coalesce(sum(s.value), 0)::text FROM stock s JOIN items i ON i.id = s.item_id WHERE i.company_id = $1`, o.company).Scan(&val); err != nil {
		t.Fatal(err)
	}
	eqDec(t, "stock valuation = inventory ledger", val, o.baseBalance(t, "31"))
}
