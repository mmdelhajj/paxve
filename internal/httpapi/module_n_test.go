package httpapi

import (
	"testing"
)

func TestCreditNoteByLine(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	vat := o.vat(t)
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Parts", "currency": "USD"}, 201).body["id"])
	lamp := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "LMP", "name": "Lamp", "kind": "stock"}, 201).body["id"])
	cable := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "CBL", "name": "Cable", "kind": "stock"}, 201).body["id"])
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
		"lines": []any{map[string]any{"item_id": lamp, "quantity": "10", "unit_price": "1"}, map[string]any{"item_id": cable, "quantity": "10", "unit_price": "3"}}}, 201)
	cust := o.customer(t, "Shop client", "")
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-02",
		"lines": []any{
			map[string]any{"item_id": lamp, "quantity": "4", "unit_price": "5"},
			map[string]any{"item_id": cable, "quantity": "2", "unit_price": "10", "tax_rate_id": vat},
		}}, 201)
	invID := str(obj(inv.body["invoice"])["id"])
	eqDec(t, "invoice total", obj(inv.body["invoice"])["total"], "42.2") // 20 + 20 + 11% of 20
	stock := func(item string) string {
		return str(obj(o.c.ok("GET", o.p("/items/"+item), nil, 200).body["stock"].([]any)[0])["quantity"])
	}
	// credit one cable only: 10 + 1.1 VAT; only the cable comes back
	cn := o.c.ok("POST", o.p("/invoices/"+invID+"/credit-note"), map[string]any{"date": "2026-11-03",
		"lines": []any{map[string]any{"line_no": 2, "quantity": "1"}}}, 201)
	eqDec(t, "credit note amount", obj(cn.body["payment"])["amount"], "11.1")
	eqDec(t, "lamps not returned", stock(lamp), "6")
	eqDec(t, "one cable back", stock(cable), "9")
	eqDec(t, "VAT collected after credit", o.baseBalance(t, "4427"), "-1.1")
	eqDec(t, "cost of sales", o.baseBalance(t, "607"), "7") // 4 lamps x 1 + 1 cable x 3
	// more than what is left on the line is refused
	if r := o.c.do("POST", o.p("/invoices/"+invID+"/credit-note"), map[string]any{"lines": []any{map[string]any{"line_no": 2, "quantity": "2"}}}); r.status != 422 {
		t.Fatalf("over-credit a line: %d %s", r.status, r.raw)
	}
	// the invoice shows what was credited per line
	lines := o.c.ok("GET", o.p("/invoices/"+invID), nil, 200).body["lines"].([]any)
	eqDec(t, "credited quantity", obj(lines[1])["credited_quantity"], "1")
	// the rest of the line: exact remainder, and the void puts the cable back out
	cn2 := o.c.ok("POST", o.p("/invoices/"+invID+"/credit-note"), map[string]any{"date": "2026-11-04",
		"lines": []any{map[string]any{"line_no": 2, "quantity": "1"}}}, 201)
	eqDec(t, "second credit", obj(cn2.body["payment"])["amount"], "11.1")
	eqDec(t, "VAT fully reversed", o.baseBalance(t, "4427"), "0")
	o.c.ok("POST", o.p("/payments/"+str(obj(cn2.body["payment"])["id"])+"/void"), map[string]any{}, 200)
	eqDec(t, "cable out again after void", stock(cable), "9")
	o.trialBalanced(t)
}

func TestPointOfSale(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Wholesale", "currency": "USD"}, 201).body["id"])
	cat := str(o.c.ok("POST", o.p("/pos/categories"), map[string]any{"name": "Cleaning", "color": "blue"}, 201).body["id"])
	soap := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "SOAP", "name": "Soap", "kind": "stock", "sale_price": "2.5",
		"sale_currency": "USD", "barcode": "6221234567890", "category_id": cat}, 201).body["id"])
	if r := o.c.do("POST", o.p("/items"), map[string]any{"name": "Copy", "kind": "service", "barcode": "6221234567890"}); r.status != 409 {
		t.Fatalf("duplicate barcode: %d %s", r.status, r.raw)
	}
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-01-02", "currency": "USD",
		"lines": []any{map[string]any{"item_id": soap, "quantity": "10", "unit_price": "1"}}}, 201)
	usd, lbp := o.methods["Cash USD"], o.methods["Cash LBP"]
	reg := o.c.ok("POST", o.p("/pos/registers"), map[string]any{"name": "Shop till", "currency": "USD", "method_ids": []any{usd, lbp}}, 201).body
	regID := str(reg["id"])
	o.accounts["658"] = str(reg["over_short_account_id"])
	if str(reg["customer_id"]) == "" {
		t.Fatal("register has no walk-in customer")
	}

	// a cashier sees the till and nothing of the back office
	email := "cash" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": email, "full_name": "Cashier", "role": "cashier", "locale": "ar", "password": "cashier-pass-1"}, 201)
	cs := newClient(t)
	cs.ok("POST", "/auth/login", map[string]any{"email": email, "password": "cashier-pass-1"}, 200)
	for _, path := range []string{"/accounts", "/invoices", "/items", "/reports/pos?from=2026-01-01&to=2100-01-01"} {
		if r := cs.do("GET", o.p(path), nil); r.status != 403 {
			t.Fatalf("cashier GET %s: %d", path, r.status)
		}
	}
	catalog := cs.ok("GET", o.p("/pos/catalog?register_id="+regID), nil, 200)
	items := catalog.body["items"].([]any)
	if len(items) != 1 || str(obj(items[0])["barcode"]) != "6221234567890" {
		t.Fatalf("catalog items: %v", items)
	}
	eqDec(t, "catalog price", obj(items[0])["price"], "2.5")

	sess := cs.ok("POST", o.p("/pos/sessions"), map[string]any{"register_id": regID,
		"opening": []any{map[string]any{"method_id": usd, "amount": "50"}}}, 201)
	sid := str(obj(sess.body["session"])["id"])
	if r := cs.do("POST", o.p("/pos/sessions"), map[string]any{"register_id": regID}); r.status != 409 {
		t.Fatalf("second open session: %d", r.status)
	}

	// 2 soaps = 5 USD paid with 500,000 LBP: change 52,500 LBP
	ref := "pxv-" + randHex(8)
	sale := map[string]any{"client_ref": ref, "session_id": sid, "lines": []any{map[string]any{"item_id": soap, "quantity": "2"}},
		"tenders": []any{map[string]any{"method_id": lbp, "amount": "500000"}}, "change_method_id": lbp}
	s1 := cs.ok("POST", o.p("/pos/orders"), sale, 201)
	ord := obj(s1.body["order"])
	eqDec(t, "sale total", ord["total"], "5")
	eqDec(t, "sale paid", ord["paid"], "5")
	eqDec(t, "change", obj(s1.body["change"])["amount"], "52500")
	if again := cs.ok("POST", o.p("/pos/orders"), sale, 201); str(obj(again.body["order"])["id"]) != str(ord["id"]) {
		t.Fatal("same client_ref stored twice")
	}
	// the cashier cannot change prices, and a walk-in sale must be paid in full
	if r := cs.do("POST", o.p("/pos/orders"), map[string]any{"session_id": sid, "lines": []any{map[string]any{"item_id": soap, "quantity": "1", "unit_price": "1"}},
		"tenders": []any{map[string]any{"method_id": usd, "amount": "1"}}}); r.status != 403 {
		t.Fatalf("cashier price change: %d %s", r.status, r.raw)
	}
	if r := cs.do("POST", o.p("/pos/orders"), map[string]any{"session_id": sid, "lines": []any{map[string]any{"item_id": soap, "quantity": "1"}},
		"tenders": []any{map[string]any{"method_id": usd, "amount": "1"}}}); r.status != 422 {
		t.Fatalf("walk-in sale not paid in full: %d %s", r.status, r.raw)
	}
	// a named customer can buy on account: pays 1 of 2.5
	cust := str(cs.ok("POST", o.p("/pos/customers"), map[string]any{"name": "Rami", "phone": "70123456"}, 201).body["id"])
	s2 := cs.ok("POST", o.p("/pos/orders"), map[string]any{"session_id": sid, "customer_id": cust, "on_account": true,
		"lines": []any{map[string]any{"item_id": soap, "quantity": "1"}}, "tenders": []any{map[string]any{"method_id": usd, "amount": "1"}}}, 201)
	eqDec(t, "on account", obj(s2.body["order"])["on_account"], "1.5")
	eqDec(t, "receivables", o.baseBalance(t, "411"), "1.5")

	// refunds: not allowed for cashiers on this till; the office refunds one soap in cash
	if r := cs.do("POST", o.p("/pos/orders/"+str(ord["id"])+"/refund"), map[string]any{"session_id": sid,
		"lines": []any{map[string]any{"line_no": 1, "quantity": "1"}}, "method_id": usd}); r.status != 403 {
		t.Fatalf("cashier refund: %d %s", r.status, r.raw)
	}
	rf := o.c.ok("POST", o.p("/pos/orders/"+str(ord["id"])+"/refund"), map[string]any{"session_id": sid,
		"lines": []any{map[string]any{"line_no": 1, "quantity": "1"}}, "method_id": usd}, 201)
	eqDec(t, "refund total", obj(rf.body["order"])["total"], "2.5")
	eqDec(t, "refund paid", obj(rf.body["order"])["paid"], "2.5")
	soapStock := obj(o.c.ok("GET", o.p("/items/"+soap), nil, 200).body["stock"].([]any)[0])
	eqDec(t, "stock after sales and return", soapStock["quantity"], "8")
	got := o.c.ok("GET", o.p("/pos/orders/"+str(ord["id"])), nil, 200)
	eqDec(t, "returned on line", obj(got.body["lines"].([]any)[0])["returned"], "1")

	// expected cash: USD 50 + 1 - 2.5 = 48.5; LBP 500,000 - 52,500 = 447,500
	z := cs.ok("GET", o.p("/pos/sessions/"+sid), nil, 200)
	exp := map[string]string{}
	for _, d := range z.body["drawer"].([]any) {
		exp[str(obj(d)["method_id"])] = str(obj(d)["expected"])
	}
	eqDec(t, "expected USD", exp[usd], "48.5")
	eqDec(t, "expected LBP", exp[lbp], "447500")
	closed := cs.ok("POST", o.p("/pos/sessions/"+sid+"/close"), map[string]any{"counted": []any{
		map[string]any{"method_id": usd, "amount": "48"}, map[string]any{"method_id": lbp, "amount": "447500"}}}, 200)
	if str(obj(closed.body["session"])["status"]) != "closed" {
		t.Fatalf("session not closed: %v", closed.body["session"])
	}
	eqDec(t, "cash short posted", o.baseBalance(t, "658"), "0.5")
	if r := cs.do("POST", o.p("/pos/orders"), map[string]any{"session_id": sid, "lines": []any{map[string]any{"item_id": soap, "quantity": "1"}},
		"tenders": []any{map[string]any{"method_id": usd, "amount": "5"}}, "change_method_id": usd}); r.status != 409 {
		t.Fatalf("sale on a closed session: %d", r.status)
	}
	for _, g := range []string{"day", "session", "cashier", "register", "product", "method"} {
		o.c.ok("GET", o.p("/reports/pos?from=2000-01-01&to=2100-01-01&group="+g), nil, 200)
	}
	val := o.c.ok("GET", o.p("/reports/stock-valuation"), nil, 200)
	eqDec(t, "stock = ledger", val.body["total"], str(val.body["ledger_total"]))
	o.trialBalanced(t)
}
