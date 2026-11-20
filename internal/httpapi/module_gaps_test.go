package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Odoo gaps: credit limit (+ approval), chatter / mentions / notifications / history, activities, analysis (pivot),
// repair orders with warranty, quotation templates, fiscal positions, sales on instalments (aging / due date).

func (c *client) doH(method, path string, body any, hdr map[string]string) resp {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, itServer.URL+"/api/v1"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "paxve")
	req.Header.Set("X-Client-IP", c.ip)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	_ = json.Unmarshal(raw, &out.list)
	return out
}

func newStaff(t *testing.T, o *org, role string) (*client, string) {
	t.Helper()
	email := role + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": email, "full_name": "Staff " + role, "role": role, "password": "staff-secret-pass-9"}, 201)
	var id string
	for _, u := range o.c.ok("GET", "/users", nil, 200).list {
		if str(obj(u)["email"]) == email {
			id = str(obj(u)["id"])
		}
	}
	return loginAs(t, email, "staff-secret-pass-9"), id
}

func TestCreditLimitWarnBlockOverrideApproval(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Credit Customer", "")
	inv := func(c *client, amount string, hdr map[string]string) resp {
		return c.doH("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD",
			"lines": []any{map[string]any{"description": "Goods", "quantity": "1", "unit_price": amount}}}, hdr)
	}
	// policy off: no limit applies
	o.c.ok("PUT", o.p("/customers/"+cust+"/credit"), map[string]any{"credit_limit": "100"}, 200)
	if r := inv(o.c, "150", nil); r.status != 201 {
		t.Fatalf("policy off: %d %s", r.status, r.raw)
	}
	o.c.ok("PUT", o.p("/credit-settings"), map[string]any{"policy": "block"}, 200)
	cr := o.c.ok("GET", o.p("/customers/"+cust+"/credit"), nil, 200)
	eqDec(t, "exposure", obj(cr.body["credit"])["exposure"], "150")
	r := inv(o.c, "10", nil)
	if r.status != 409 || str(r.body["error"]) != "credit_limit" || !strings.Contains(str(r.body["detail"]), `"limit"`) {
		t.Fatalf("over limit not blocked: %d %s", r.status, r.raw)
	}
	// the owner may override (sales.credit_override)
	if r := inv(o.c, "10", map[string]string{"X-Credit-Override": "1"}); r.status != 201 {
		t.Fatalf("override: %d %s", r.status, r.raw)
	}
	// warn: confirm goes through
	o.c.ok("PUT", o.p("/credit-settings"), map[string]any{"policy": "warn"}, 200)
	if r := inv(o.c, "5", nil); r.status != 409 {
		t.Fatalf("warn without confirm: %d", r.status)
	}
	if r := inv(o.c, "5", map[string]string{"X-Credit-Confirm": "1"}); r.status != 201 {
		t.Fatalf("warn confirmed: %d %s", r.status, r.raw)
	}
	// a data-entry user (no override) asks for approval on an order; an approver lets it through
	o.c.ok("PUT", o.p("/credit-settings"), map[string]any{"policy": "block"}, 200)
	de, _ := newStaff(t, o, "data_entry")
	q := de.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": cust, "currency": "USD",
		"lines": []any{map[string]any{"description": "Big order", "quantity": "1", "unit_price": "500"}}}, 201)
	oid := str(obj(q.body["order"])["id"])
	if r := de.doH("POST", o.p("/sales/orders/"+oid+"/confirm"), map[string]any{}, map[string]string{"X-Credit-Override": "1"}); r.status != 409 {
		t.Fatalf("override without permission: %d %s", r.status, r.raw)
	}
	ap := de.doH("POST", o.p("/sales/orders/"+oid+"/confirm"), map[string]any{}, map[string]string{"X-Credit-Request-Approval": "1"})
	if ap.status != 202 || str(ap.body["kind"]) != "credit_limit" {
		t.Fatalf("approval request: %d %s", ap.status, ap.raw)
	}
	done := o.c.ok("POST", o.p("/approvals/"+str(ap.body["approval_id"])+"/approve"), map[string]any{}, 200)
	if str(done.body["status"]) != "approved" {
		t.Fatalf("approval replay: %s", done.raw)
	}
	if st := str(obj(o.c.ok("GET", o.p("/sales/orders/"+oid), nil, 200).body["order"])["status"]); st != "confirmed" {
		t.Fatalf("order after approval: %s", st)
	}
	// overdue rule: an invoice 60 days late blocks a customer with a 30-day rule, even without a limit
	late := o.customer(t, "Late Customer", "")
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": late, "currency": "USD", "issue_date": "2026-01-01", "due_date": "2026-01-10",
		"lines": []any{map[string]any{"description": "Old", "quantity": "1", "unit_price": "20"}}}, 201)
	o.c.ok("PUT", o.p("/customers/"+late+"/credit"), map[string]any{"credit_overdue_days": 30}, 200)
	r = o.c.do("POST", o.p("/invoices"), map[string]any{"customer_id": late, "currency": "USD",
		"lines": []any{map[string]any{"description": "New", "quantity": "1", "unit_price": "1"}}})
	if r.status != 409 || !strings.Contains(str(r.body["detail"]), "overdue") {
		t.Fatalf("overdue rule: %d %s", r.status, r.raw)
	}
	rep := o.c.ok("GET", o.p("/credit/report"), nil, 200)
	if len(rep.list) < 2 {
		t.Fatalf("credit report: %s", rep.raw)
	}
	o.trialBalanced(t)
}

func TestChatterMentionsHistoryActivities(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Chatty Customer", "")
	acc, accID := newStaff(t, o, "accountant")
	m := o.c.ok("POST", o.p("/chatter"), map[string]any{"type": "customer", "id": cust, "body": "Please call him back", "mentions": []any{accID}}, 201)
	msgID := str(m.body["id"])
	n := acc.ok("GET", o.p("/notifications"), nil, 200)
	if str(n.body["unread"]) != "1" || str(obj(n.body["items"].([]any)[0])["kind"]) != "mention" {
		t.Fatalf("mention notification: %s", n.raw)
	}
	acc.ok("POST", o.p("/notifications/read"), map[string]any{}, 200)
	if str(acc.ok("GET", o.p("/notifications"), nil, 200).body["unread"]) != "0" {
		t.Fatal("notifications not read")
	}
	// someone else's comment cannot be removed by an accountant
	if r := acc.do("DELETE", o.p("/chatter/"+msgID), nil); r.status != 403 {
		t.Fatalf("delete other's comment: %d", r.status)
	}
	// history: a field change shows up
	o.c.ok("PATCH", o.p("/customers/"+cust), map[string]any{"name": "Chatty Customer Renamed"}, 200)
	ch := o.c.ok("GET", o.p("/chatter?type=customer&id="+cust), nil, 200)
	found := false
	for _, e := range ch.body["history"].([]any) {
		for _, c := range obj(e)["changes"].([]any) {
			if str(obj(c)["field"]) == "name" && str(obj(c)["to"]) == "Chatty Customer Renamed" {
				found = true
			}
		}
	}
	if !found || len(ch.body["messages"].([]any)) != 1 {
		t.Fatalf("chatter: %s", ch.raw)
	}
	// unknown record type / other company's record
	if r := o.c.do("GET", o.p("/chatter?type=nope&id="+cust), nil); r.status != 403 {
		t.Fatalf("unknown type: %d", r.status)
	}
	// activities: assigned to the accountant, overdue, notified; done with a note
	y := time.Now().AddDate(0, 0, -1).Format(time.DateOnly)
	a := o.c.ok("POST", o.p("/record-activities"), map[string]any{"entity_type": "customer", "entity_id": cust, "kind": "call",
		"summary": "Call about the meter", "due_date": y, "assigned_to": accID}, 201)
	aid := str(a.body["id"])
	list := acc.ok("GET", o.p("/record-activities"), nil, 200)
	if len(list.list) != 1 || str(obj(list.list[0])["state"]) != "overdue" || !strings.Contains(str(obj(list.list[0])["record"]), "Chatty") {
		t.Fatalf("my activities: %s", list.raw)
	}
	if n := acc.ok("GET", o.p("/notifications"), nil, 200); str(obj(n.body["activities"])["overdue"]) != "1" {
		t.Fatalf("activity counts: %s", n.raw)
	}
	acc.ok("POST", o.p("/record-activities/"+aid+"/done"), map[string]any{"note": "Called, all good"}, 200)
	if r := acc.do("POST", o.p("/record-activities/"+aid+"/done"), map[string]any{}); r.status != 409 {
		t.Fatalf("done twice: %d", r.status)
	}
	// a free-standing to-do of my own
	o.c.ok("POST", o.p("/record-activities"), map[string]any{"summary": "Order diesel"}, 201)
	l := o.c.ok("GET", o.p("/record-activities"), nil, 200) // mine: assigned to me or created by me (the call is done)
	if len(l.list) != 1 || str(obj(l.list[0])["summary"]) != "Order diesel" || str(obj(l.list[0])["assigned_to"]) == accID {
		t.Fatalf("own todo: %s", l.raw)
	}
}

func TestAnalysisPivotAndSavedReports(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	a, b := o.customer(t, "Alpha", ""), o.customer(t, "Beta", "")
	for _, x := range []struct{ c, d, amt string }{{a, "2026-10-05", "100"}, {a, "2026-11-05", "50"}, {b, "2026-11-06", "70"}} {
		o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": x.c, "currency": "USD", "issue_date": x.d,
			"lines": []any{map[string]any{"description": "Sale", "quantity": "1", "unit_price": x.amt}}}, 201)
	}
	spec := map[string]any{"source": "sales", "rows": []any{"customer"}, "cols": []any{"date:month"}, "measure": "amount", "from": "2026-01-01", "to": "2026-12-31"}
	res := o.c.ok("POST", o.p("/analysis/run"), spec, 200)
	got := map[string]string{}
	for _, x := range res.body["rows"].([]any) {
		r := obj(x)
		got[str(r["r"].([]any)[0])+"|"+str(r["c"].([]any)[0])] = str(r["v"])
	}
	if got["Alpha|2026-10"] != "100.0000" && got["Alpha|2026-10"] != "100" {
		t.Fatalf("pivot: %s", res.raw)
	}
	eqDec(t, "Beta Nov", got["Beta|2026-11"], "70")
	if r := o.c.do("POST", o.p("/analysis/run"), map[string]any{"source": "sales", "rows": []any{"password"}, "measure": "amount"}); r.status != 400 {
		t.Fatalf("unknown dimension: %d", r.status)
	}
	if r := o.c.do("POST", o.p("/analysis/run"), map[string]any{"source": "sales", "measure": "drop table"}); r.status != 400 {
		t.Fatalf("unknown measure: %d", r.status)
	}
	sv := o.c.ok("POST", o.p("/analysis/reports"), map[string]any{"name": "Sales by month", "spec": spec, "shared": true}, 201)
	acc, _ := newStaff(t, o, "accountant")
	if l := acc.ok("GET", o.p("/analysis/reports"), nil, 200); len(l.list) != 1 {
		t.Fatalf("shared report not visible: %s", l.raw)
	}
	if r := acc.do("PUT", o.p("/analysis/reports/"+str(sv.body["id"])), map[string]any{"delete": true}); r.status != 403 {
		t.Fatalf("delete someone else's report: %d", r.status)
	}
	for _, src := range []string{"invoices", "payments", "purchases", "stock", "journal", "subscriptions", "payroll"} {
		srcs := o.c.ok("GET", o.p("/analysis/sources"), nil, 200)
		ms := obj(srcs.body[src])["measures"].([]any)
		o.c.ok("POST", o.p("/analysis/run"), map[string]any{"source": src, "measure": str(ms[0]), "rows": []any{"date:year"}}, 200)
	}
}

func TestRepairOrderWarrantyAndInvoice(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Repair Customer", "")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Parts Co", "currency": "USD"}, 201).body["id"])
	part := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "ADP", "name": "Adapter", "kind": "stock"}, 201).body["id"])
	dev := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "RTR", "name": "Router X", "kind": "stock"}, 201).body["id"])
	o.c.ok("PUT", o.p("/items/"+dev+"/stock-settings"), map[string]any{"warranty_months": 12}, 200)
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
		"lines": []any{map[string]any{"item_id": part, "quantity": "10", "unit_price": "6"}}}, 201)
	wh := defaultWH(t, o)
	rep := o.c.ok("POST", o.p("/repairs"), map[string]any{"customer_id": cust, "item_id": dev, "device": "Router X", "serial": "SN-123",
		"fault": "No power", "sale_date": "2026-06-01"}, 201)
	rid := str(obj(rep.body["repair"])["id"])
	if obj(rep.body["repair"])["under_warranty"] != true || str(obj(rep.body["repair"])["warranty_until"]) != "2027-06-01" {
		t.Fatalf("warranty: %s", rep.raw)
	}
	o.c.ok("PATCH", o.p("/repairs/"+rid), map[string]any{"status": "diagnosing", "diagnosis": "Adapter burnt"}, 200)
	o.c.ok("PUT", o.p("/repairs/"+rid+"/lines"), map[string]any{"lines": []any{
		map[string]any{"kind": "part", "item_id": part, "warehouse_id": wh, "quantity": "1", "unit_price": "15", "covered": true},
		map[string]any{"kind": "part", "item_id": part, "warehouse_id": wh, "quantity": "1", "unit_price": "15"},
		map[string]any{"kind": "labour", "description": "Bench work", "quantity": "1", "unit_price": "20"},
	}}, 200)
	if r := o.c.do("PATCH", o.p("/repairs/"+rid), map[string]any{"status": "delivered"}); r.status != 409 {
		t.Fatalf("deliver before invoice: %d %s", r.status, r.raw)
	}
	inv := o.c.ok("POST", o.p("/repairs/"+rid+"/invoice"), map[string]any{"date": "2026-11-20"}, 201)
	if str(obj(inv.body["repair"])["invoice_number"]) == "" {
		t.Fatalf("no invoice: %s", inv.raw)
	}
	invID := str(obj(inv.body["repair"])["invoice_id"])
	full := o.c.ok("GET", o.p("/invoices/"+invID), nil, 200)
	eqDec(t, "repair invoice", obj(full.body["invoice"])["total"], "35")
	st := obj(o.c.ok("GET", o.p("/items/"+part), nil, 200).body["stock"].([]any)[0])
	eqDec(t, "adapters left", st["quantity"], "8")
	if r := o.c.do("POST", o.p("/repairs/"+rid+"/invoice"), map[string]any{}); r.status != 409 {
		t.Fatalf("invoice twice: %d", r.status)
	}
	o.c.ok("PATCH", o.p("/repairs/"+rid), map[string]any{"status": "repaired"}, 200)
	o.c.ok("PATCH", o.p("/repairs/"+rid), map[string]any{"status": "delivered"}, 200)
	w := o.c.ok("GET", o.p("/repairs/warranty?serial=sn-123"), nil, 200)
	if len(w.body["repairs"].([]any)) != 1 {
		t.Fatalf("serial history: %s", w.raw)
	}
	if l := o.c.ok("GET", o.p("/repairs?status=open"), nil, 200); len(l.list) != 0 {
		t.Fatalf("open repairs: %s", l.raw)
	}
	o.trialBalanced(t)
}

func TestFiscalPositionTemplatesInstalments(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	vat := o.vat(t)
	cust := o.customer(t, "NGO", "")
	fp := o.c.ok("POST", o.p("/fiscal-positions"), map[string]any{"name": "VAT exempt", "kind": "exempt",
		"tax_map": []any{map[string]any{"from": vat, "to": ""}}}, 201)
	o.c.ok("PUT", o.p("/customers/"+cust+"/credit"), map[string]any{"fiscal_position_id": str(fp.body["id"])}, 200)
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD",
		"lines": []any{map[string]any{"description": "Service", "quantity": "1", "unit_price": "100", "tax_rate_id": vat}}}, 201)
	eqDec(t, "exempt total", obj(inv.body["invoice"])["total"], "100")
	other := o.customer(t, "Normal", "")
	inv2 := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": other, "currency": "USD",
		"lines": []any{map[string]any{"description": "Service", "quantity": "1", "unit_price": "100", "tax_rate_id": vat}}}, 201)
	eqDec(t, "taxed total", obj(inv2.body["invoice"])["total"], "111")
	if r := o.c.do("POST", o.p("/fiscal-positions"), map[string]any{"name": "bad", "tax_map": []any{map[string]any{"from": "0190a0a0-0000-7000-8000-000000000001", "to": ""}}}); r.status != 400 {
		t.Fatalf("unknown tax in map: %d", r.status)
	}

	// quotation template
	tp := o.c.ok("POST", o.p("/quote-templates"), map[string]any{"name": "Generator 60 kVA install", "currency": "USD", "validity_days": 15,
		"terms": "50% upfront", "lines": []any{map[string]any{"description": "Cabling", "quantity": "1", "unit_price": "200"}}}, 201)
	o.c.ok("PUT", o.p("/quote-templates/"+str(tp.body["id"])), map[string]any{"name": "Generator 60 kVA installation", "lines": []any{}}, 200)
	if l := o.c.ok("GET", o.p("/quote-templates"), nil, 200); len(l.list) != 1 || str(obj(l.list[0])["name"]) != "Generator 60 kVA installation" {
		t.Fatalf("templates: %s", l.raw)
	}

	// instalments: 1000 + 10 % fee on the 900 financed = 1090; 100 down + 3 × 330
	buyer := o.customer(t, "Buyer", "")
	pl := o.c.ok("POST", o.p("/instalments"), map[string]any{"customer_id": buyer, "currency": "USD", "issue_date": "2026-01-10",
		"lines": []any{map[string]any{"description": "TV", "quantity": "1", "unit_price": "1000"}}, "down_payment": "100", "months": 3, "fee_percent": "10"}, 201)
	plan := obj(pl.body["plan"])
	eqDec(t, "plan total", plan["total"], "1090")
	eqDec(t, "plan fee", plan["fee"], "90")
	lines := pl.body["lines"].([]any)
	if len(lines) != 4 || str(obj(lines[1])["due_date"]) != "2026-02-10" || str(obj(lines[3])["due_date"]) != "2026-04-10" {
		t.Fatalf("schedule: %s", pl.raw)
	}
	eqDec(t, "instalment", obj(lines[3])["amount"], "330")
	if str(plan["next_due"]) != "2026-01-10" {
		t.Fatalf("invoice due date = down payment date: %s", plan["next_due"])
	}
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": buyer, "date": "2026-01-10", "currency": "USD",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "300"}}}, 201)
	pid := str(plan["id"])
	g := o.c.ok("GET", o.p("/instalments/"+pid), nil, 200)
	if str(obj(g.body["plan"])["next_due"]) != "2026-02-10" || str(obj(g.body["lines"].([]any)[0])["state"]) != "paid" {
		t.Fatalf("after payment: %s", g.raw)
	}
	eqDec(t, "first instalment partly paid", obj(g.body["lines"].([]any)[1])["paid"], "200")
	// aging splits the invoice into its instalments
	ag := o.c.ok("GET", o.p("/reports/aging?as_of=2026-03-15"), nil, 200)
	var row map[string]any
	for _, x := range ag.body["rows"].([]any) {
		if str(obj(x)["customer_id"]) == buyer {
			row = obj(x)
		}
	}
	if row == nil {
		t.Fatalf("aging: %s", ag.raw)
	}
	eqDec(t, "aging total", row["total"], "790")
	eqDec(t, "aging current (April)", row["current"], "330")
	eqDec(t, "aging 1-30 (10 Mar)", row["d30"], "330")
	eqDec(t, "aging 31-60 (10 Feb rest)", row["d60"], "130")
	if r := o.c.do("POST", o.p("/instalments"), map[string]any{"invoice_id": str(plan["invoice_id"]), "months": 2}); r.status != 409 {
		t.Fatalf("second plan on the same invoice: %d %s", r.status, r.raw)
	}
	po := o.c.ok("POST", o.p("/instalments/"+pid+"/payoff"), map[string]any{}, 200)
	if str(obj(po.body["plan"])["next_due"]) > time.Now().Format(time.DateOnly) {
		t.Fatalf("payoff: %s", po.raw)
	}
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": buyer, "date": "2026-02-01", "currency": "USD",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "790"}}}, 201)
	if st := str(obj(o.c.ok("GET", o.p("/instalments/"+pid), nil, 200).body["plan"])["status"]); st != "paid" {
		t.Fatalf("plan after full payment: %s", st)
	}
	o.trialBalanced(t)
}
