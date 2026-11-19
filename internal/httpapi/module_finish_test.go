package httpapi

import (
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// Finishing stage: proration, goods receipts / 3-way match / supplier prices, expense claims, custom fields,
// manufacturing, rotations and accruals, clock rules, fingerprint push, online payments, e-invoicing.

func TestProrationPlanChangeAndStop(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.c.ok("PUT", o.p("/billing/proration"), map[string]any{"enabled": true}, 200)
	a, b := o.plan(t, "5A", "30"), o.plan(t, "10A", "60")
	cust := o.customer(t, "Half Month", "")
	sub := str(o.c.ok("POST", o.p("/customers/"+cust+"/subscriptions"), map[string]any{"plan_id": a, "start_date": "2026-11-16"}, 201).body["id"])
	o.c.ok("POST", o.p("/billing/run"), map[string]any{"period": "2026-11", "issue_date": "2026-11-16"}, 200)
	rows := o.c.ok("GET", o.p("/invoices?customer_id="+cust), nil, 200).body["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("invoices: %d", len(rows))
	}
	eqDec(t, "prorated first month (15/30 of 30)", obj(rows[0])["total"], "15")
	ch := o.c.ok("POST", o.p("/subscriptions/"+sub+"/change-plan"), map[string]any{"plan_id": b, "date": "2026-11-21"}, 200)
	eqDec(t, "upgrade difference (10 days × 30/30)", ch.body["amount"], "10")
	if ch.body["invoice_id"] == nil {
		t.Fatalf("no upgrade invoice: %s", ch.raw)
	}
	st := o.c.ok("POST", o.p("/subscriptions/"+sub+"/stop"), map[string]any{"date": "2026-11-25"}, 200)
	eqDec(t, "unused days credited (5 × 60/30)", st.body["amount"], "-10")
	if st.body["credit_note_id"] == nil {
		t.Fatalf("no credit note: %s", st.raw)
	}
	if n := len(o.c.ok("GET", o.p("/subscriptions/"+sub+"/changes"), nil, 200).list); n != 2 {
		t.Fatalf("changes: %d", n)
	}
	// December is not billed any more (ended in November)
	o.c.ok("POST", o.p("/billing/run"), map[string]any{"period": "2026-12", "issue_date": "2026-12-01"}, 200)
	if n := len(o.c.ok("GET", o.p("/invoices?customer_id="+cust+"&period=2026-12"), nil, 200).body["rows"].([]any)); n != 0 {
		t.Fatalf("billed after stop: %d", n)
	}
	o.trialBalanced(t)
}

func TestGoodsReceiptThreeWayAndSupplierPrices(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Diesel Co", "currency": "USD"}, 201).body["id"])
	item := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "DSL", "name": "Diesel", "kind": "stock", "unit": "L"}, 201).body["id"])
	o.c.ok("POST", o.p("/supplier-prices"), map[string]any{"supplier_id": sup, "item_id": item, "currency": "USD", "price": "0.75"}, 201)
	o.c.ok("POST", o.p("/supplier-prices"), map[string]any{"supplier_id": sup, "item_id": item, "currency": "USD", "price": "0.70", "min_qty": "500"}, 201)
	best := o.c.ok("GET", o.p("/supplier-prices/best?supplier_id="+sup+"&item_id="+item+"&currency=USD&qty=100"), nil, 200)
	eqDec(t, "price for 100", best.body["price"], "0.75")
	po := o.c.ok("POST", o.p("/purchase-orders"), map[string]any{"supplier_id": sup, "currency": "USD", "order_date": "2026-11-10",
		"lines": []any{map[string]any{"item_id": item, "quantity": "600", "unit_price": ""}}}, 201)
	id := str(obj(po.body["order"])["id"])
	eqDec(t, "PO priced from the price list (600 × 0.70)", obj(po.body["order"])["total"], "420")
	o.c.ok("POST", o.p("/purchase-orders/"+id+"/confirm"), map[string]any{}, 200)
	line := str(obj(po.body["lines"].([]any)[0])["id"])
	o.c.ok("POST", o.p("/purchase-orders/"+id+"/receive"), map[string]any{"delivery_ref": "DN-1", "lines": []any{map[string]any{"po_line_id": line, "quantity": "400"}}}, 201)
	if r := o.c.do("POST", o.p("/purchase-orders/"+id+"/receive"), map[string]any{"lines": []any{map[string]any{"po_line_id": line, "quantity": "300"}}}); r.status != 422 {
		t.Fatalf("over receipt: %d %s", r.status, r.raw)
	}
	bill := o.c.ok("POST", o.p("/purchase-orders/"+id+"/bill"), map[string]any{"bill_date": "2026-11-12", "supplier_ref": "F-9",
		"lines": []any{map[string]any{"po_line_id": line, "quantity": "600", "unit_price": "0.80"}}}, 201)
	codes := ""
	for _, w := range bill.body["warnings"].([]any) {
		codes += str(obj(w)["code"]) + ","
	}
	if !strings.Contains(codes, "not_received") || !strings.Contains(codes, "price_mismatch") {
		t.Fatalf("warnings: %s", bill.raw)
	}
	m := o.c.ok("GET", o.p("/purchase-orders/"+id+"/match"), nil, 200)
	ml := obj(m.body["lines"].([]any)[0])
	eqDec(t, "received", ml["received"], "400")
	eqDec(t, "billed", ml["billed"], "600")
	if ml["billed_not_received"] != true {
		t.Fatalf("match: %s", m.raw)
	}
	// block mode refuses a bill for goods not received
	o.c.ok("PUT", o.p("/purchasing/matching"), map[string]any{"mode": "block", "tolerance": "2"}, 200)
	po2 := o.c.ok("POST", o.p("/purchase-orders"), map[string]any{"supplier_id": sup, "currency": "USD",
		"lines": []any{map[string]any{"item_id": item, "quantity": "10", "unit_price": "1"}}}, 201)
	id2 := str(obj(po2.body["order"])["id"])
	o.c.ok("POST", o.p("/purchase-orders/"+id2+"/confirm"), map[string]any{}, 200)
	if r := o.c.do("POST", o.p("/purchase-orders/"+id2+"/bill"), map[string]any{}); r.status != 422 || !strings.Contains(r.raw, "not_received") {
		t.Fatalf("block mode: %d %s", r.status, r.raw)
	}
	o.trialBalanced(t)
}

func TestExpenseClaimsCashAndPayroll(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	usd := o.methods["Cash USD"]
	emp := str(o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Karim", "currency": "USD", "salary": "800", "hire_date": "2025-01-01"}, 201).body["id"])
	email := "karim" + randHex(4) + "@example.com"
	pw := str(o.c.ok("POST", o.p("/employees/"+emp+"/login"), map[string]any{"email": email, "locale": "en"}, 201).body["password"])
	k := loginAs(t, email, pw)
	receipt := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 fuel receipt"))
	c1 := k.ok("POST", o.p("/self/claims"), map[string]any{"category": "fuel", "description": "Fuel for site visit", "currency": "USD", "amount": "25",
		"filename": "receipt.pdf", "data": receipt}, 201)
	if str(c1.body["status"]) != "pending" || str(c1.body["attachments"]) != "1" {
		t.Fatalf("claim: %s", c1.raw)
	}
	if r := k.do("POST", o.p("/hr/claims/"+str(c1.body["id"])+"/decide"), map[string]any{"decision": "approve"}); r.status != 403 {
		t.Fatalf("self approval: %d", r.status)
	}
	o.c.ok("POST", o.p("/hr/claims/"+str(c1.body["id"])+"/decide"), map[string]any{"decision": "approve", "account_id": o.accounts["611"]}, 200)
	paid := o.c.ok("POST", o.p("/hr/claims/"+str(c1.body["id"])+"/pay"), map[string]any{"method_id": usd, "date": today()}, 200)
	if str(paid.body["status"]) != "paid" {
		t.Fatalf("pay: %s", paid.raw)
	}
	// a claim paid with the salary
	c2 := k.ok("POST", o.p("/self/claims"), map[string]any{"category": "phone", "description": "Phone card", "currency": "USD", "amount": "15", "pay_via": "payroll"}, 201)
	o.c.ok("POST", o.p("/hr/claims/"+str(c2.body["id"])+"/decide"), map[string]any{"decision": "approve", "account_id": o.accounts["611"]}, 200)
	o.rate(t, "LBP", "2026-01-01", "89500")
	run := o.c.ok("POST", o.p("/payroll"), map[string]any{"period": time.Now().Format("2006-01")}, 201)
	runID := str(run.body["id"])
	if runID == "" {
		runID = str(obj(run.body["run"])["id"])
	}
	found := false
	for _, x := range o.c.ok("GET", o.p("/hr/claims?employee_id="+emp), nil, 200).list {
		if str(obj(x)["id"]) == str(c2.body["id"]) && obj(x)["payslip_id"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("claim not on the payslip of run %s", runID)
	}
	if mine := k.ok("GET", o.p("/self/claims"), nil, 200); len(mine.list) != 2 {
		t.Fatalf("self claims: %s", mine.raw)
	}
	o.trialBalanced(t)
}

func TestCustomFields(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.c.ok("POST", o.p("/custom-fields"), map[string]any{"entity": "customers", "key": "phase", "label": "Phase", "type": "choice",
		"options": []string{"1-phase", "3-phase"}, "required": true, "label_i18n": map[string]string{"ar": "الطور"}}, 201)
	o.c.ok("POST", o.p("/custom-fields"), map[string]any{"entity": "customers", "key": "contract_end", "label": "Contract end", "type": "date"}, 201)
	if r := o.c.do("POST", o.p("/custom-fields"), map[string]any{"entity": "customers", "key": "phase", "label": "Dup", "type": "text"}); r.status != 409 {
		t.Fatalf("duplicate key: %d", r.status)
	}
	cust := o.customer(t, "Field Test", "")
	if r := o.c.do("PUT", o.p("/custom-values/customers/"+cust), map[string]any{"values": map[string]any{"phase": "2-phase"}}); r.status != 400 {
		t.Fatalf("bad choice: %d", r.status)
	}
	if r := o.c.do("PUT", o.p("/custom-values/customers/"+cust), map[string]any{"values": map[string]any{"contract_end": "2027-01-01"}}); r.status != 400 {
		t.Fatalf("required missing: %d", r.status)
	}
	o.c.ok("PUT", o.p("/custom-values/customers/"+cust), map[string]any{"values": map[string]any{"phase": "3-phase", "contract_end": "2027-01-31", "junk": 1}}, 200)
	v := o.c.ok("GET", o.p("/custom-values/customers/"+cust), nil, 200)
	vals := obj(v.body["values"])
	if str(vals["phase"]) != "3-phase" || vals["junk"] != nil {
		t.Fatalf("values: %s", v.raw)
	}
	rep := o.c.ok("GET", o.p("/custom-fields/report?entity=customers&key=phase&value=3-phase"), nil, 200)
	if len(rep.body["rows"].([]any)) != 1 || len(rep.body["fields"].([]any)) != 2 {
		t.Fatalf("report: %s", rep.raw)
	}
}

func TestManufacturingAndScrap(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	mk := func(code, name string) string {
		return str(o.c.ok("POST", o.p("/items"), map[string]any{"code": code, "name": name, "kind": "stock", "unit": "pc"}, 201).body["id"])
	}
	cable, breaker, kit := mk("CBL", "Cable 10m"), mk("BRK", "Breaker"), mk("KIT", "Connection kit")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Parts", "currency": "USD"}, 201).body["id"])
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD", "lines": []any{
		map[string]any{"item_id": cable, "quantity": "20", "unit_price": "5"}, map[string]any{"item_id": breaker, "quantity": "10", "unit_price": "8"}}}, 201)
	bom := o.c.ok("POST", o.p("/mfg/boms"), map[string]any{"item_id": kit, "output_qty": "1", "labour_cost": "2", "overhead_cost": "1",
		"lines": []any{map[string]any{"item_id": cable, "quantity": "2"}, map[string]any{"item_id": breaker, "quantity": "1"}}}, 201)
	order := o.c.ok("POST", o.p("/mfg/orders"), map[string]any{"bom_id": str(obj(bom.body["bom"])["id"]), "quantity": "5", "date": "2026-11-05"}, 201)
	oid := str(obj(order.body["order"])["id"])
	done := o.c.ok("POST", o.p("/mfg/orders/"+oid+"/produce"), map[string]any{"scrap_qty": "1"}, 200)
	ord := obj(done.body["order"])
	// components 5 × (2×5 + 8) = 90, labour 10, overhead 5 → 105 over 4 good kits
	eqDec(t, "components", ord["components_value"], "90")
	eqDec(t, "unit cost", ord["unit_cost"], "26.25")
	st := obj(o.c.ok("GET", o.p("/items/"+kit), nil, 200).body["stock"].([]any)[0])
	eqDec(t, "kits in stock", st["quantity"], "4")
	if r := o.c.do("POST", o.p("/mfg/orders/"+oid+"/produce"), map[string]any{}); r.status != 409 {
		t.Fatalf("produce twice: %d", r.status)
	}
	o.c.ok("POST", o.p("/mfg/scrap"), map[string]any{"item_id": cable, "quantity": "1", "reason": "cut wrong"}, 201)
	st = obj(o.c.ok("GET", o.p("/items/"+cable), nil, 200).body["stock"].([]any)[0])
	eqDec(t, "cable left", st["quantity"], "9")
	o.trialBalanced(t)
}

func TestRotationAccrualClockRulesAndDevicePush(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	day := str(o.c.ok("POST", o.p("/hr/schedules"), map[string]any{"name": "Day", "start_time": "08:00", "end_time": "16:00"}, 201).body["id"])
	night := str(o.c.ok("POST", o.p("/hr/schedules"), map[string]any{"name": "Night", "start_time": "20:00", "end_time": "23:59"}, 201).body["id"])
	emp := str(o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Guard", "currency": "USD", "salary": "600", "hire_date": "2025-01-01", "code": "G1"}, 201).body["id"])
	o.c.ok("PUT", o.p("/employees/"+emp+"/file"), map[string]any{"device_id": "77"}, 200)
	rot := str(o.c.ok("POST", o.p("/hr/rotations"), map[string]any{"name": "2 days, 2 nights, 2 off", "pattern": []any{day, day, night, night, "off", "off"}}, 201).body["id"])
	o.c.ok("POST", o.p("/hr/rotations/"+rot+"/assign"), map[string]any{"employee_ids": []string{emp}, "start": "2026-12-01"}, 200)
	ro := o.c.ok("GET", o.p("/hr/roster?from=2026-12-01&to=2026-12-06"), nil, 200)
	days := obj(ro.body["rows"].([]any)[0])["days"].([]any)
	if str(obj(days[2])["schedule"]) != "Night" || obj(days[4])["off"] != true {
		t.Fatalf("roster: %s", ro.raw)
	}
	o.c.ok("PUT", o.p("/hr/roster"), map[string]any{"employee_id": emp, "work_date": "2026-12-05", "schedule_id": day}, 200)
	// fingerprint machine pushes punches (ADMS)
	serial := "CQZ" + randHex(4)
	o.c.ok("POST", o.p("/hr/devices"), map[string]any{"serial": serial, "name": "Gate"}, 201)
	push := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, itServer.URL+path, strings.NewReader(body))
		req.Header.Set("X-Client-IP", "10.9.9.9")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := push("GET", "/iclock/cdata?SN="+serial+"&options=all", ""); code != 200 || !strings.Contains(body, "ATTLOGStamp") {
		t.Fatalf("handshake: %d %s", code, body)
	}
	if code, _ := push("GET", "/iclock/cdata?SN=UNKNOWN99", ""); code != 401 {
		t.Fatalf("unknown device: %d", code)
	}
	code, body := push("POST", "/iclock/cdata?SN="+serial+"&table=ATTLOG&Stamp=1", "77\t2026-12-03 19:58:00\t0\t1\n77\t2026-12-04 00:30:00\t1\t1\n77\t2026-12-05 08:20:00\t0\t1\n77\t2026-12-05 16:05:00\t1\t1\n")
	if code != 200 || !strings.HasPrefix(body, "OK") {
		t.Fatalf("push: %d %s", code, body)
	}
	att := o.c.ok("GET", o.p("/hr/attendance?from=2026-12-05&to=2026-12-05&employee_id="+emp), nil, 200)
	if len(att.list) != 1 || str(obj(att.list[0])["late_minutes"]) != "20" {
		t.Fatalf("roster override day (day shift, 20 min late): %s", att.raw)
	}
	// accrual history
	acc := o.c.ok("POST", o.p("/hr/leave/accrue"), map[string]any{"period": "2026-01"}, 200)
	if dec(t, acc.body["yearly"]).IntPart() < 1 {
		t.Fatalf("accrual: %s", acc.raw)
	}
	again := o.c.ok("POST", o.p("/hr/leave/accrue"), map[string]any{"period": "2026-01"}, 200)
	if dec(t, again.body["yearly"]).IntPart() != 0 {
		t.Fatalf("accrual twice: %s", again.raw)
	}
	if n := len(o.c.ok("GET", o.p("/hr/leave/accruals?employee_id="+emp+"&year=2026"), nil, 200).list); n < 1 {
		t.Fatalf("accrual rows: %d", n)
	}
	// clock rules: a selfie and the office area are required
	email := "guard" + randHex(4) + "@example.com"
	pw := str(o.c.ok("POST", o.p("/employees/"+emp+"/login"), map[string]any{"email": email}, 201).body["password"])
	g := loginAs(t, email, pw)
	o.c.ok("PUT", o.p("/hr/clock-settings"), map[string]any{"photo": true, "geofence": true, "lat": 33.8938, "lng": 35.5018, "radius_m": 150}, 200)
	if r := g.do("POST", o.p("/self/clock"), map[string]any{"lat": 33.8938, "lng": 35.5018}); r.status != 422 || !strings.Contains(r.raw, "photo_required") {
		t.Fatalf("photo rule: %d %s", r.status, r.raw)
	}
	jpeg := base64.StdEncoding.EncodeToString(append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, make([]byte, 64)...))
	if r := g.do("POST", o.p("/self/clock"), map[string]any{"lat": 33.90, "lng": 35.52, "photo": jpeg}); r.status != 422 || !strings.Contains(r.raw, "outside_area") {
		t.Fatalf("geofence: %d %s", r.status, r.raw)
	}
	g.ok("POST", o.p("/self/clock"), map[string]any{"lat": 33.8939, "lng": 35.5019, "photo": jpeg}, 200)
}

func TestOnlinePaymentsTransferSimulatorAndHostedWebhook(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	usd := o.methods["Cash USD"]
	cust := o.customer(t, "Online Payer", "")
	mkInv := func() string {
		inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": today(),
			"lines": []any{map[string]any{"description": "Subscription", "quantity": "1", "unit_price": "40"}}}, 201)
		return str(obj(inv.body["invoice"])["id"])
	}
	tok := str(o.c.ok("GET", o.p("/customers/"+cust+"/portal"), nil, 200).body["token"])
	whish := str(o.c.ok("POST", o.p("/payment-gateways"), map[string]any{"kind": "transfer", "name": "Whish", "method_id": usd,
		"config": map[string]any{"phone": "+961 70 000000"}}, 201).body["id"])
	card := o.c.ok("POST", o.p("/payment-gateways"), map[string]any{"kind": "stripe", "name": "Card", "method_id": usd, "test_mode": true}, 201)
	hosted := o.c.ok("POST", o.p("/payment-gateways"), map[string]any{"kind": "hosted", "name": "Bank page", "method_id": usd, "test_mode": false,
		"config": map[string]any{"checkout_url": "https://pay.example.com/checkout", "merchant_id": "M1", "shared_secret": "s3cret"}}, 201)
	if obj(hosted.body["config"])["has_shared_secret"] != true || strings.Contains(hosted.raw, "s3cret") {
		t.Fatalf("secret shown: %s", hosted.raw)
	}
	anon := newClient(t)
	if opts := anon.ok("GET", "/portal/"+tok+"/pay-options", nil, 200); len(opts.list) != 3 {
		t.Fatalf("options: %s", opts.raw)
	}
	// 1. Whish transfer: reference typed by the customer, confirmed by staff
	inv1 := mkInv()
	if r := anon.do("POST", "/portal/"+tok+"/invoices/"+inv1+"/pay", map[string]any{"gateway_id": whish}); r.status != 400 {
		t.Fatalf("transfer without reference: %d", r.status)
	}
	p1 := anon.ok("POST", "/portal/"+tok+"/invoices/"+inv1+"/pay", map[string]any{"gateway_id": whish, "reference": "WH-123456"}, 200)
	if str(p1.body["status"]) != "review" {
		t.Fatalf("transfer: %s", p1.raw)
	}
	o.c.ok("POST", o.p("/payment-intents/"+str(p1.body["intent"])+"/confirm"), map[string]any{}, 200)
	o.c.ok("POST", o.p("/payment-intents/"+str(p1.body["intent"])+"/confirm"), map[string]any{}, 200) // idempotent
	if st := str(obj(o.c.ok("GET", o.p("/invoices/"+inv1), nil, 200).body["invoice"])["status"]); st != "paid" {
		t.Fatalf("invoice 1 status %s", st)
	}
	// 2. card in test mode without keys: the simulator
	inv2 := mkInv()
	p2 := anon.ok("POST", "/portal/"+tok+"/invoices/"+inv2+"/pay", map[string]any{"gateway_id": str(card.body["id"])}, 200)
	u := str(p2.body["redirect_url"])
	simTok := u[strings.LastIndex(u, "/")+1:]
	if !strings.Contains(u, "/pay-sim/") {
		t.Fatalf("no simulator: %s", p2.raw)
	}
	anon.ok("GET", "/pay/sim/"+simTok, nil, 200)
	anon.ok("POST", "/pay/sim/"+simTok, map[string]any{"result": "paid"}, 200)
	anon.ok("POST", "/pay/sim/"+simTok, map[string]any{"result": "paid"}, 200) // replay: still one payment
	if st := str(obj(o.c.ok("GET", o.p("/invoices/"+inv2), nil, 200).body["invoice"])["status"]); st != "paid" {
		t.Fatalf("invoice 2 status %s", st)
	}
	// 3. hosted page: signed redirect, then a signed callback (a bad signature is refused)
	inv3 := mkInv()
	p3 := anon.ok("POST", "/portal/"+tok+"/invoices/"+inv3+"/pay", map[string]any{"gateway_id": str(hosted.body["id"])}, 200)
	ru := str(p3.body["redirect_url"])
	if !strings.HasPrefix(ru, "https://pay.example.com/checkout?") || !strings.Contains(ru, "signature=") {
		t.Fatalf("hosted redirect: %s", ru)
	}
	order := ru[strings.Index(ru, "order=")+6:]
	order = order[:strings.Index(order, "&")]
	gid := str(hosted.body["id"])
	cb := map[string]any{"order": order, "status": "paid", "amount": "40", "currency": "USD", "ref": "BANK-9", "signature": "00"}
	if r := anon.do("POST", "/pay/webhook/"+gid, cb); r.status != 401 {
		t.Fatalf("bad signature accepted: %d", r.status)
	}
	cb["signature"] = hostedSign("s3cret", order, "paid", "40", "USD", "BANK-9")
	anon.ok("POST", "/pay/webhook/"+gid, cb, 200)
	if st := str(obj(o.c.ok("GET", o.p("/invoices/"+inv3), nil, 200).body["invoice"])["status"]); st != "paid" {
		t.Fatalf("invoice 3 status %s", st)
	}
	n := 0
	for _, x := range o.c.ok("GET", o.p("/payment-intents?status=paid"), nil, 200).list {
		if obj(x)["payment_id"] != nil {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("paid intents: %d", n)
	}
	o.trialBalanced(t)
}

func TestStripeSignature(t *testing.T) {
	body := []byte(`{"type":"checkout.session.completed"}`)
	now := time.Unix(1790000000, 0)
	sig := hostedSignRaw("whsec_x", "1790000000."+string(body))
	if !verifyStripeSignature("t=1790000000,v1="+sig, body, "whsec_x", now) {
		t.Fatal("valid signature refused")
	}
	if verifyStripeSignature("t=1790000000,v1="+sig, body, "whsec_y", now) || verifyStripeSignature("t=1790000000,v1="+sig, body, "whsec_x", now.Add(time.Hour)) {
		t.Fatal("bad / old signature accepted")
	}
}

func hostedSignRaw(secret, msg string) string { return hostedSign(secret, msg) }

func TestZatcaSigningAndETASerialization(t *testing.T) {
	key, err := k1Generate()
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("invoice"))
	sig, err := key.sign(h[:])
	if err != nil || !k1Verify(key.pub, h[:], sig) {
		t.Fatalf("secp256k1 sign/verify: %v", err)
	}
	other, _ := k1Generate()
	if k1Verify(other.pub, h[:], sig) {
		t.Fatal("verified with the wrong key")
	}
	// known vector: private key 1 → public key = G
	one, _ := k1FromD(decimal.NewFromInt(1).BigInt())
	if hex.EncodeToString(one.pubBytes())[2:66] != "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798" {
		t.Fatal("G mismatch")
	}
	cfg := map[string]any{"seller_name": "Paxve Test Co", "vat_number": "300000000000003", "crn": "1010010000", "street": "King Fahd", "building": "1234",
		"district": "Olaya", "city": "Riyadh", "postal_code": "12345"}
	csr, err := zatcaCSR(key, "sandbox", cfg)
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Info asn1.RawValue
		Alg  asn1.RawValue
		Sig  asn1.BitString
	}
	if _, err := asn1.Unmarshal(csr, &req); err != nil {
		t.Fatalf("csr: %v", err)
	}
	ih := sha256.Sum256(req.Info.FullBytes)
	if !k1Verify(key.pub, ih[:], req.Sig.Bytes) {
		t.Fatal("CSR signature does not verify")
	}
	if !strings.Contains(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})), "BEGIN CERTIFICATE REQUEST") {
		t.Fatal("pem")
	}
	inv := &einvInvoice{Number: "INV-1", Date: "2026-11-19", Time: "10:00:00", Currency: "SAR", Customer: "Walk-in",
		Total: decimal.RequireFromString("115"), Tax: decimal.RequireFromString("15"), Net: decimal.RequireFromString("100"),
		Lines: []einvLine{{Desc: "Generator subscription", Qty: decimal.NewFromInt(1), Price: decimal.NewFromInt(100), Net: decimal.NewFromInt(100),
			Rate: decimal.NewFromInt(15), Tax: decimal.NewFromInt(15)}}}
	xml, hash, qr, err := zatcaBuild(inv, cfg, key, 1, zatcaFirstPIH, newUUIDv4(), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// the hash covers the document without extensions / signature / QR
	head, b1, b2 := zatcaXMLParts(inv, cfg, 1, zatcaFirstPIH, xml[strings.Index(xml, "<cbc:UUID>")+10:strings.Index(xml, "</cbc:UUID>")])
	hh := sha256.Sum256([]byte(head + b1 + b2 + "</Invoice>"))
	if base64.StdEncoding.EncodeToString(hh[:]) != hash || !strings.Contains(xml, "<ds:DigestValue>"+hash+"</ds:DigestValue>") {
		t.Fatal("invoice hash")
	}
	raw, _ := base64.StdEncoding.DecodeString(qr)
	tags := map[byte]bool{}
	for i := 0; i+1 < len(raw); {
		tags[raw[i]] = true
		i += 2 + int(raw[i+1])
	}
	for _, want := range []byte{1, 2, 3, 4, 5, 6, 7, 8} {
		if !tags[want] {
			t.Fatalf("QR tag %d missing", want)
		}
	}
	ser := etaSerialize(jsonRoundTrip(map[string]any{"issuer": map[string]any{"name": "A", "id": "1"}, "invoiceLines": []any{map[string]any{"total": "1.00000"}}}))
	if ser != `"INVOICELINES""INVOICELINES""TOTAL""1.00000""ISSUER""ID""1""NAME""A"` {
		t.Fatalf("eta serialization: %s", ser)
	}
}

func TestDeferralNameParts(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Yearly Payer", "")
	inv := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-01",
		"lines": []any{map[string]any{"description": "Year", "quantity": "1", "unit_price": "120"}}}, 201)
	d := o.c.ok("POST", o.p("/deferrals"), map[string]any{"kind": "revenue", "source_type": "invoice", "source_id": str(obj(inv.body["invoice"])["id"]), "months": 12}, 201)
	if str(d.body["party"]) != "Yearly Payer" || d.body["source_number"] == nil {
		t.Fatalf("deferral parts: %s", d.raw)
	}
}
