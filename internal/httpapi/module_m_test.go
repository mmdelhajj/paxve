package httpapi

import (
	"testing"
)

func TestSupplierFXOnSettlement(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "LBP supplier", "currency": "LBP"}, 201).body["id"])
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-02-01", "currency": "LBP",
		"lines": []any{map[string]any{"description": "Maintenance", "quantity": "1", "unit_price": "8950000", "account_id": o.accounts["602"]}}}, 201) // 100 USD
	eqDec(t, "payables at bill", o.baseBalance(t, "401"), "-100")
	o.rate(t, "LBP", "2026-03-01", "100000")
	pay := o.c.ok("POST", o.p("/supplier-payments"), map[string]any{"supplier_id": sup, "date": "2026-03-05", "currency": "LBP",
		"method_id": o.methods["Cash LBP"], "paid_amount": "8950000"}, 201) // 89.5 USD
	eqDec(t, "payables base after payment", o.baseBalance(t, "401"), "0")
	eqDec(t, "payables LBP after payment", o.amountBalance(t, "401", "LBP"), "0")
	eqDec(t, "exchange gain", o.baseBalance(t, "766"), "-10.5")
	o.c.ok("POST", o.p("/supplier-payments/"+str(pay.body["id"])+"/void"), map[string]any{}, 200)
	eqDec(t, "payables after void", o.baseBalance(t, "401"), "-100")
	eqDec(t, "exchange gain after void", o.baseBalance(t, "766"), "0")

	// advance paid first (another supplier), bill later at another rate
	sup2 := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Second supplier", "currency": "LBP"}, 201).body["id"])
	o.c.ok("POST", o.p("/supplier-payments"), map[string]any{"supplier_id": sup2, "date": "2026-03-06", "currency": "LBP",
		"method_id": o.methods["Cash LBP"], "paid_amount": "1000000"}, 201) // 10 USD at 100000
	o.rate(t, "LBP", "2026-04-01", "80000")
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup2, "bill_date": "2026-04-02", "currency": "LBP",
		"lines": []any{map[string]any{"description": "Parts", "quantity": "1", "unit_price": "1000000", "account_id": o.accounts["602"]}}}, 201) // 12.5 USD
	eqDec(t, "payables: first bill open only", o.baseBalance(t, "401"), "-100")
	eqDec(t, "exchange gain on advance", o.baseBalance(t, "766"), "-2.5")
	o.trialBalanced(t)
}

func TestCreditNoteReturnsStock(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Parts", "currency": "USD"}, 201).body["id"])
	item := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "LMP", "name": "Lamp", "kind": "stock", "unit": "pcs"}, 201).body["id"])
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
		"lines": []any{map[string]any{"item_id": item, "quantity": "10", "unit_price": "1"}}}, 201)
	cust := o.customer(t, "Shop client", "")
	sell := func(q string) string {
		inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-02",
			"lines": []any{map[string]any{"item_id": item, "quantity": q, "unit_price": "5"}}}, 201)
		return str(obj(inv.body["invoice"])["id"])
	}
	stock := func() map[string]any {
		return obj(o.c.ok("GET", o.p("/items/"+item), nil, 200).body["stock"].([]any)[0])
	}
	inv := sell("4")
	eqDec(t, "cogs after sale", o.baseBalance(t, "607"), "4")
	cn := o.c.ok("POST", o.p("/invoices/"+inv+"/credit-note"), map[string]any{"date": "2026-11-03"}, 201)
	eqDec(t, "stock back", stock()["quantity"], "10")
	eqDec(t, "inventory back", o.baseBalance(t, "31"), "10")
	eqDec(t, "cogs after full credit", o.baseBalance(t, "607"), "0")
	// voiding the credit note takes the goods out again
	o.c.ok("POST", o.p("/payments/"+str(obj(cn.body["payment"])["id"])+"/void"), map[string]any{}, 200)
	eqDec(t, "stock after credit note void", stock()["quantity"], "6")
	eqDec(t, "cogs after credit note void", o.baseBalance(t, "607"), "4")
	// partial credit note: half the invoice -> half the goods
	o.c.ok("POST", o.p("/invoices/"+inv+"/credit-note"), map[string]any{"amount": "10", "date": "2026-11-04"}, 201)
	eqDec(t, "stock after half credit", stock()["quantity"], "8")
	o.c.ok("POST", o.p("/invoices/"+inv+"/credit-note"), map[string]any{"date": "2026-11-05"}, 201)
	eqDec(t, "stock after the rest", stock()["quantity"], "10")
	eqDec(t, "inventory", o.baseBalance(t, "31"), "10")
	o.trialBalanced(t)
}

func TestVATBasesInBaseCurrencyAndManualInvoicePeriod(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	vat := o.vat(t)
	o.rate(t, "LBP", "2026-01-01", "100000")
	cust := o.customer(t, "LBP client", "")
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "LBP", "issue_date": "2026-11-05",
		"lines": []any{map[string]any{"description": "Install", "quantity": "1", "unit_price": "1000000", "tax_rate_id": vat}}}, 201)
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-06",
		"lines": []any{map[string]any{"description": "Repair", "quantity": "1", "unit_price": "20", "tax_rate_id": vat}}}, 201)
	vr := o.c.ok("GET", o.p("/reports/vat?from=2026-11-01&to=2026-11-30"), nil, 200)
	eqDec(t, "taxable sales in USD", obj(vr.body["rows"].([]any)[0])["taxable_sales"], "30")
	list := o.c.ok("GET", o.p("/invoices?period=2026-11"), nil, 200)
	if n := len(list.body["rows"].([]any)); n != 2 {
		t.Fatalf("manual invoices in period list = %d", n)
	}
}

func TestSalesQuoteOrderDeliverInvoice(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	vat := o.vat(t)
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Net Co", "currency": "USD"}, 201).body["id"])
	router := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "RTR", "name": "Router", "kind": "stock", "sale_price": "40",
		"prices": []any{map[string]any{"currency": "LBP", "price": "3600000"}}}, 201).body["id"])
	install := o.c.ok("POST", o.p("/items"), map[string]any{"code": "INS", "name": "Installation", "kind": "service", "sale_price": "50",
		"sale_tax_rate_id": vat, "is_purchasable": false}, 201).body
	if len(install["prices"].([]any)) != 0 || str(install["sale_tax_rate_id"]) != vat || install["is_purchasable"] != false {
		t.Fatalf("catalog fields: %v", install)
	}
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
		"lines": []any{map[string]any{"item_id": router, "quantity": "10", "unit_price": "20"}}}, 201)
	cust := o.customer(t, "Buyer", "")
	lines := func(rq string) []any {
		return []any{
			map[string]any{"item_id": router, "quantity": rq, "unit_price": "40", "discount_pct": "10"},
			map[string]any{"item_id": str(install["id"]), "quantity": "1", "unit_price": "50", "tax_rate_id": vat},
		}
	}
	q := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": cust, "currency": "USD", "quote_date": "2026-11-02", "lines": lines("3")}, 201)
	ord := obj(q.body["order"])
	id := str(ord["id"])
	eqDec(t, "quote total", ord["total"], "163.5")
	if str(ord["status"]) != "draft" || ord["valid_until"] == nil {
		t.Fatalf("new quote = %v", ord)
	}
	q = o.c.ok("PUT", o.p("/sales/orders/"+id), map[string]any{"customer_id": cust, "currency": "USD", "quote_date": "2026-11-02", "lines": lines("4")}, 200)
	eqDec(t, "updated total", obj(q.body["order"])["total"], "199.5")
	if r := o.c.do("POST", o.p("/sales/orders/"+id+"/deliver"), map[string]any{}); r.status != 409 {
		t.Fatalf("deliver a quotation: %d", r.status)
	}
	q = o.c.ok("POST", o.p("/sales/orders/"+id+"/confirm"), map[string]any{"date": "2026-11-03"}, 200)
	if str(obj(q.body["order"])["status"]) != "confirmed" || str(obj(q.body["order"])["order_number"]) == "" {
		t.Fatalf("confirm = %v", q.body["order"])
	}
	if r := o.c.do("PUT", o.p("/sales/orders/"+id), map[string]any{"customer_id": cust, "currency": "USD", "lines": lines("1")}); r.status != 409 {
		t.Fatalf("edit a confirmed order: %d", r.status)
	}
	rl := str(obj(q.body["lines"].([]any)[0])["id"])
	o.c.ok("POST", o.p("/sales/orders/"+id+"/deliver"), map[string]any{"date": "2026-11-04", "lines": []any{map[string]any{"line_id": rl, "quantity": "1"}}}, 201)
	eqDec(t, "cogs after partial delivery", o.baseBalance(t, "607"), "20")
	if r := o.c.do("POST", o.p("/sales/orders/"+id+"/deliver"), map[string]any{"lines": []any{map[string]any{"line_id": rl, "quantity": "9"}}}); r.status != 422 {
		t.Fatalf("over-delivery: %d %s", r.status, r.raw)
	}
	if r := o.c.do("POST", o.p("/sales/orders/"+id+"/cancel"), map[string]any{}); r.status != 409 {
		t.Fatalf("cancel in progress: %d", r.status)
	}
	inv := o.c.ok("POST", o.p("/sales/orders/"+id+"/invoice"), map[string]any{"date": "2026-11-05"}, 201)
	invID := str(inv.body["invoice_id"])
	got := obj(o.c.ok("GET", o.p("/invoices/"+invID), nil, 200).body["invoice"])
	eqDec(t, "invoice total", got["total"], "199.5")
	eqDec(t, "receivable", o.baseBalance(t, "411"), "199.5")
	eqDec(t, "cogs unchanged by invoice", o.baseBalance(t, "607"), "20")
	q = o.c.ok("POST", o.p("/sales/orders/"+id+"/deliver"), map[string]any{"date": "2026-11-06"}, 201)
	if str(obj(q.body["order"])["status"]) != "done" {
		t.Fatalf("status after everything = %v", obj(q.body["order"])["status"])
	}
	eqDec(t, "cogs after full delivery", o.baseBalance(t, "607"), "80")
	// void the invoice: quantities come back, order is open again
	o.c.ok("POST", o.p("/invoices/"+invID+"/void"), map[string]any{}, 200)
	q = o.c.ok("GET", o.p("/sales/orders/"+id), nil, 200)
	if str(obj(q.body["order"])["status"]) != "confirmed" || str(obj(q.body["order"])["invoice_status"]) != "none" {
		t.Fatalf("after invoice void = %v", q.body["order"])
	}
	// two partial invoices add up to the line exactly
	o.c.ok("POST", o.p("/sales/orders/"+id+"/invoice"), map[string]any{"date": "2026-11-07", "lines": []any{map[string]any{"line_id": rl, "quantity": "3"}}}, 201)
	o.c.ok("POST", o.p("/sales/orders/"+id+"/invoice"), map[string]any{"date": "2026-11-08"}, 201)
	eqDec(t, "receivable after partial invoices", o.baseBalance(t, "411"), "199.5")
	q = o.c.ok("GET", o.p("/sales/orders/"+id), nil, 200)
	if str(obj(q.body["order"])["status"]) != "done" || len(q.body["invoices"].([]any)) != 3 || len(q.body["deliveries"].([]any)) != 2 {
		t.Fatalf("order at the end = %s", q.raw)
	}
	// void a delivery: goods back in stock, order reopened
	del := str(obj(q.body["deliveries"].([]any)[1])["id"])
	o.c.ok("POST", o.p("/sales/deliveries/"+del+"/void"), map[string]any{}, 200)
	eqDec(t, "cogs after delivery void", o.baseBalance(t, "607"), "20")
	st := obj(o.c.ok("GET", o.p("/items/"+router), nil, 200).body["stock"].([]any)[0])
	eqDec(t, "router stock", st["quantity"], "9")
	list := o.c.ok("GET", o.p("/sales/orders?kind=orders"), nil, 200)
	if len(list.body["rows"].([]any)) != 1 {
		t.Fatalf("orders list: %s", list.raw)
	}
	o.c.ok("GET", o.p("/reports/sales?from=2026-11-01&to=2026-11-30"), nil, 200)
	o.trialBalanced(t)
}

func TestCustomerStatement(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Statement client", "")
	inv := func(date, amt string) {
		o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": date,
			"lines": []any{map[string]any{"description": "Bill", "quantity": "1", "unit_price": amt}}}, 201)
	}
	pay := func(date, amt string) {
		o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": date, "currency": "USD",
			"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": amt}}}, 201)
	}
	inv("2026-10-05", "100")
	pay("2026-10-20", "30")
	inv("2026-11-05", "50")
	pay("2026-11-10", "20")
	st := o.c.ok("GET", o.p("/customers/"+cust+"/statement?from=2026-11-01&to=2026-11-30"), nil, 200)
	c := obj(st.body["currencies"].([]any)[0])
	eqDec(t, "opening", c["opening"], "70")
	eqDec(t, "debits", c["debits"], "50")
	eqDec(t, "credits", c["credits"], "20")
	eqDec(t, "closing", c["closing"], "100")
	if n := len(c["lines"].([]any)); n != 2 {
		t.Fatalf("lines = %d", n)
	}
	all := o.c.ok("GET", o.p("/statements?from=2026-11-01&to=2026-11-30&only_balance=1"), nil, 200)
	if n := len(all.body["rows"].([]any)); n != 1 {
		t.Fatalf("statements = %d", n)
	}
	lnk := o.c.ok("POST", o.p("/send-document"), map[string]any{"kind": "statement", "id": cust, "from": "2026-11-01", "to": "2026-11-30",
		"channel": "link", "locale": "en"}, 200)
	if str(lnk.body["link"]) == "" || str(lnk.body["wa_link"]) == "" {
		t.Fatalf("statement link: %s", lnk.raw)
	}
}

func TestCRMLeadToOrder(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	stages := o.c.ok("GET", o.p("/crm/stages"), nil, 200).list
	if len(stages) != 4 {
		t.Fatalf("default stages = %d", len(stages))
	}
	lead := o.c.ok("POST", o.p("/crm/leads"), map[string]any{"title": "Fiber for school", "contact_name": "Rami", "organisation": "Hope School",
		"phone": "03123456", "expected_revenue": "1200", "currency": "USD"}, 201)
	l := obj(lead.body["lead"])
	lid := str(l["id"])
	if str(l["stage_id"]) != str(obj(stages[0])["id"]) || str(l["status"]) != "open" {
		t.Fatalf("new lead = %v", l)
	}
	o.c.ok("PATCH", o.p("/crm/leads/"+lid), map[string]any{"stage_id": str(obj(stages[2])["id"])}, 200)
	act := o.c.ok("POST", o.p("/crm/activities"), map[string]any{"lead_id": lid, "kind": "call", "summary": "Call back", "due_date": "2026-11-20"}, 201)
	o.c.ok("POST", o.p("/crm/activities"), map[string]any{"lead_id": lid, "kind": "note", "summary": "Wants 100 Mbps"}, 201)
	mine := o.c.ok("GET", o.p("/crm/activities?mine=1"), nil, 200).list
	if len(mine) != 1 {
		t.Fatalf("my activities = %d", len(mine))
	}
	o.c.ok("PATCH", o.p("/crm/activities/"+str(act.body["id"])), map[string]any{"done": true}, 200)
	conv := o.c.ok("POST", o.p("/crm/leads/"+lid+"/convert"), map[string]any{}, 200)
	cust := str(obj(conv.body["lead"])["customer_id"])
	if cust == "" || str(obj(conv.body["lead"])["customer_name"]) != "Hope School" {
		t.Fatalf("convert = %v", conv.body["lead"])
	}
	acts := o.c.ok("GET", o.p("/crm/activities?customer_id="+cust), nil, 200).list
	if len(acts) != 2 {
		t.Fatalf("customer activities = %d", len(acts))
	}
	q := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": cust, "lead_id": lid, "currency": "USD",
		"lines": []any{map[string]any{"description": "Fiber setup", "quantity": "1", "unit_price": "1200"}}}, 201)
	qid := str(obj(q.body["order"])["id"])
	// sending the quotation (link) marks it sent; the customer accepts on the portal
	o.c.ok("POST", o.p("/send-document"), map[string]any{"kind": "quote", "id": qid, "channel": "link"}, 200)
	token := str(obj(q.body["order"])["portal_token"])
	pv := o.c.ok("GET", "/portal/"+token+"/quotes/"+qid, nil, 200)
	if str(obj(pv.body["order"])["status"]) != "sent" {
		t.Fatalf("portal quote = %v", pv.body["order"])
	}
	o.c.ok("POST", "/portal/"+token+"/quotes/"+qid+"/accept", map[string]any{"name": "Rami"}, 200)
	if r := o.c.do("POST", "/portal/"+token+"/quotes/"+qid+"/accept", map[string]any{"name": "Rami"}); r.status != 409 {
		t.Fatalf("accepted twice: %d", r.status)
	}
	portal := o.c.ok("GET", "/portal/"+token, nil, 200)
	if len(portal.body["quotes"].([]any)) != 1 {
		t.Fatalf("portal quotes: %s", portal.raw)
	}
	o.c.ok("POST", o.p("/sales/orders/"+qid+"/confirm"), map[string]any{}, 200)
	got := obj(o.c.ok("GET", o.p("/crm/leads/"+lid), nil, 200).body["lead"])
	if str(got["status"]) != "won" {
		t.Fatalf("lead after order = %v", got["status"])
	}
	sum := o.c.ok("GET", o.p("/crm/summary"), nil, 200)
	if sum.body["activities"] == nil {
		t.Fatalf("summary: %s", sum.raw)
	}
}

func TestEmailAndDocumentSettings(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := str(o.c.ok("POST", o.p("/customers"), map[string]any{"name": "Mail client", "email": "client@example.com"}, 201).body["id"])
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD",
		"lines": []any{map[string]any{"description": "Service", "quantity": "1", "unit_price": "10"}}}, 201)
	invID := str(obj(inv.body["invoice"])["id"])
	if r := o.c.do("POST", o.p("/send-document"), map[string]any{"kind": "invoice", "id": invID, "channel": "email"}); r.status != 422 {
		t.Fatalf("email without provider: %d %s", r.status, r.raw)
	}
	prov := o.c.ok("POST", o.p("/message-providers"), map[string]any{"kind": "smtp", "name": "Mail",
		"config": map[string]any{"host": "smtp.example.com", "port": "587", "username": "u", "password": "secret-password-1", "from_email": "billing@example.com"}}, 201)
	_ = prov
	list := o.c.ok("GET", o.p("/message-providers"), nil, 200).list
	if cfg := obj(obj(list[0])["config"]); str(cfg["password"]) == "secret-password-1" {
		t.Fatal("smtp password not masked")
	}
	r := o.c.ok("POST", o.p("/send-document"), map[string]any{"kind": "invoice", "id": invID, "channel": "email", "locale": "fr"}, 200)
	if str(r.body["to"]) != "client@example.com" {
		t.Fatalf("email to = %v", r.body["to"])
	}
	msgs := o.c.ok("GET", o.p("/messages"), nil, 200).body["rows"].([]any)
	if len(msgs) != 1 || str(obj(msgs[0])["channel"]) != "email" || str(obj(msgs[0])["subject"]) == "" {
		t.Fatalf("messages = %v", msgs)
	}
	// WhatsApp must not pick the SMTP provider
	o.c.ok("PATCH", o.p("/customers/"+cust), map[string]any{"phone": "03123456"}, 200)
	w := o.c.ok("POST", o.p("/send-document"), map[string]any{"kind": "invoice", "id": invID, "channel": "whatsapp"}, 200)
	if w.body["has_provider"] != false {
		t.Fatalf("whatsapp used smtp provider: %s", w.raw)
	}

	if r := o.c.do("PUT", o.p("/documents/settings"), map[string]any{"logo": "javascript:alert(1)"}); r.status != 400 {
		t.Fatalf("bad logo accepted: %d", r.status)
	}
	o.c.ok("PUT", o.p("/documents/settings"), map[string]any{"logo": "data:image/png;base64,iVBORw0KGgo=", "doc_footer": "Thank you",
		"tax_id": "123-456", "quote_valid_days": 15}, 200)
	ds := o.c.ok("GET", o.p("/documents/settings"), nil, 200)
	if str(ds.body["doc_footer"]) != "Thank you" || str(ds.body["quote_valid_days"]) != "15" {
		t.Fatalf("settings = %s", ds.raw)
	}
	got := obj(o.c.ok("GET", o.p("/invoices/"+invID), nil, 200).body["invoice"])
	if str(got["company_tax_id"]) != "123-456" || str(got["company_logo"]) == "" {
		t.Fatalf("invoice company fields = %v", got)
	}
}
