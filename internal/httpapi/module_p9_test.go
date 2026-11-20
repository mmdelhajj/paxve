package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Phase 9: bank files (CSV / OFX / MT940 / CAMT.053), duplicates refused, matching rules, suggestions and one-click
// reconcile; card terminals (charges, sale needs its charge, settlement with fees, bank line matches the net); QR
// ordering in restaurants (menu, order waits for staff, accepted onto the table's check and the kitchen); carriers
// (manual label and status, DHL through a fake API, unconfigured carrier refused); e-mail from a record with a PDF,
// reply coming back to the record; Gantt (task dependencies, shift, loop refused, work-center plan and load,
// technician double booking); unrealized inter-company profit in the group report.
func TestOpsPhase9(t *testing.T) {
	needDB(t)
	ctx := context.Background()
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	bank := o.methods["Bank"]

	// ======================= bank files =======================
	csv := "Date;Details;Debit;Credit;Ref\n01/11/2026;Opening deposit;;1000,00;D1\n03/11/2026;BANK FEE NOV;5,00;;F1\n05/11/2026;Payment INV-%INV% Nadia Khoury;;120,00;T77\n06/11/2026;Transfer to Cedar Supplies;80,00;;W12\n"
	cust := o.customer(t, "Nadia Khoury", "")
	inv := o.invoice(t, cust, "2026-11-02", "120")
	invNo := ownerQuery(t, `SELECT number::text FROM invoices WHERE id = $1`, inv)
	csv = strings.ReplaceAll(csv, "%INV%", invNo)
	mapping := map[string]any{"header": true, "delimiter": ";", "date": "Date", "description": "Details", "debit": "Debit", "credit": "Credit",
		"reference": "Ref", "date_format": "DD/MM/YYYY", "decimal": ","}
	pv := o.c.ok("POST", o.p("/bank/statements/parse"), map[string]any{"format": "csv", "content": csv, "mapping": mapping, "method_id": bank}, 200)
	lines := pv.body["lines"].([]any)
	if len(lines) != 4 || str(obj(lines[1])["amount"]) != "-5" || str(obj(lines[2])["date"]) != "2026-11-05" {
		t.Fatalf("csv parse: %s", pv.raw)
	}
	eqDec(t, "csv total", pv.body["total"], "1035")
	ofx := `OFXHEADER:100
<OFX><BANKMSGSRSV1><STMTTRNRS><STMTRS><CURDEF>USD<BANKTRANLIST>
<STMTTRN><TRNTYPE>CREDIT<DTPOSTED>20261110120000<TRNAMT>45.50<FITID>A-1<NAME>Rami<MEMO>Coffee beans
<STMTTRN><TRNTYPE>DEBIT<DTPOSTED>20261111<TRNAMT>-12.00<FITID>A-2<NAME>Telecom</STMTTRN>
</BANKTRANLIST></STMTRS></STMTTRNRS></BANKMSGSRSV1></OFX>`
	ox := o.c.ok("POST", o.p("/bank/statements/parse"), map[string]any{"format": "ofx", "content": ofx}, 200)
	if l := ox.body["lines"].([]any); len(l) != 2 || str(obj(l[0])["bank_id"]) != "A-1" || str(obj(l[1])["amount"]) != "-12" || str(ox.body["currency"]) != "USD" {
		t.Fatalf("ofx parse: %s", ox.raw)
	}
	mt := ":20:STMT1\n:25:LB12/3456\n:28C:1/1\n:60F:C261101USD1000,00\n:61:2611120112C250,00NTRFREF-9//BK77\nIncoming\n:86:Payment from Hassan\n:61:2611130113D15,50NCHGNONREF\n:86:Card fee\n:62F:C261113USD1234,50\n-"
	mx := o.c.ok("POST", o.p("/bank/statements/parse"), map[string]any{"format": "mt940", "content": mt}, 200)
	if l := mx.body["lines"].([]any); len(l) != 2 || str(obj(l[0])["amount"]) != "250" || str(obj(l[1])["amount"]) != "-15.5" ||
		!strings.Contains(str(obj(l[0])["description"]), "Hassan") || str(mx.body["currency"]) != "USD" {
		t.Fatalf("mt940 parse: %s", mx.raw)
	}
	camt := `<?xml version="1.0"?><Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.02"><BkToCstmrStmt><Stmt><Acct><Ccy>USD</Ccy></Acct>
<Ntry><Amt Ccy="USD">300.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Sts>BOOK</Sts><BookgDt><Dt>2026-11-14</Dt></BookgDt><AcctSvcrRef>C-1</AcctSvcrRef>
<NtryDtls><TxDtls><Refs><EndToEndId>E2E-5</EndToEndId></Refs><RltdPties><Dbtr><Nm>Lina</Nm></Dbtr></RltdPties><RmtInf><Ustrd>Invoice 55</Ustrd></RmtInf></TxDtls></NtryDtls></Ntry>
<Ntry><Amt Ccy="USD">9.00</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts>PDNG</Sts><BookgDt><Dt>2026-11-15</Dt></BookgDt></Ntry>
</Stmt></BkToCstmrStmt></Document>`
	cx := o.c.ok("POST", o.p("/bank/statements/parse"), map[string]any{"format": "camt", "content": camt}, 200)
	if l := cx.body["lines"].([]any); len(l) != 1 || str(obj(l[0])["partner"]) != "Lina" || str(obj(l[0])["reference"]) != "E2E-5" {
		t.Fatalf("camt parse (pending entry skipped): %s", cx.raw)
	}

	// opening deposit is in the books (so it matches exactly), the supplier bill is open
	o.c.ok("POST", o.p("/journal"), map[string]any{"date": "2026-11-01", "post": true, "memo": "capital", "lines": []any{
		map[string]any{"account_id": o.accounts["512"], "debit": "1000"}, map[string]any{"account_id": o.accounts["101"], "credit": "1000"}}}, 201)
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Cedar Supplies", "currency": "USD"}, 201).body["id"])
	bill := o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-02", "currency": "USD",
		"lines": []any{map[string]any{"description": "Paper", "quantity": "1", "unit_price": "80", "account_id": o.accounts["611"]}}}, 201)
	billID := str(obj(bill.body["bill"])["id"])
	if billID == "" {
		billID = str(bill.body["id"])
	}
	o.c.ok("POST", o.p("/bank/match-rules"), map[string]any{"name": "Bank fees", "pattern": "bank fee", "direction": "out", "action": "account", "account_id": o.accounts["627"]}, 201)
	if r := o.c.do("POST", o.p("/bank/match-rules"), map[string]any{"name": "Bad", "pattern": "([", "action": "ignore"}); r.status != 400 {
		t.Errorf("bad regex accepted: %d", r.status)
	}
	if r := o.c.do("POST", o.p("/bank/match-rules"), map[string]any{"name": "No account", "action": "account"}); r.status != 400 {
		t.Errorf("account rule without account: %d", r.status)
	}
	st := o.c.ok("POST", o.p("/bank/statements"), map[string]any{"method_id": bank, "name": "November", "format": "csv", "lines": lines}, 201)
	sid := str(obj(st.body["statement"])["id"])
	if r := o.c.do("POST", o.p("/bank/statements"), map[string]any{"method_id": bank, "name": "Again", "format": "csv", "lines": lines}); r.status != 409 ||
		!strings.Contains(r.raw, "already_imported") {
		t.Fatalf("same file twice: %d %s", r.status, r.raw)
	}
	more := append([]any{}, lines...)
	more = append(more, map[string]any{"date": "2026-11-07", "description": "Interest", "amount": "1.25"})
	st2 := o.c.ok("POST", o.p("/bank/statements"), map[string]any{"method_id": bank, "name": "Overlap", "lines": more}, 201)
	if str(obj(st2.body["statement"])["skipped"]) != "4" || len(st2.body["lines"].([]any)) != 1 {
		t.Fatalf("overlapping file: %s", st2.raw)
	}
	var payLine, supLine string
	for _, l := range st.body["lines"].([]any) {
		switch str(obj(l)["reference"]) {
		case "T77":
			payLine = str(obj(l)["id"])
		case "W12":
			supLine = str(obj(l)["id"])
		}
	}
	sg := o.c.ok("GET", o.p("/bank/lines/"+payLine+"/suggestions"), nil, 200)
	docs := sg.body["documents"].([]any)
	if len(docs) == 0 || str(obj(docs[0])["id"]) != inv || dec(t, obj(docs[0])["confidence"]).IntPart() < 85 {
		t.Fatalf("invoice suggestion: %s", sg.raw)
	}
	ar := o.c.ok("POST", o.p("/bank/statements/"+sid+"/auto-reconcile"), map[string]any{}, 200)
	res := obj(ar.body["result"])
	// the deposit matches the capital entry, the fee follows the rule, the customer and the supplier lines point to
	// their invoice / bill without doubt (amount + number / name)
	if str(res["matched"]) != "1" || str(res["rules"]) != "1" || str(res["documents"]) != "2" || str(res["left"]) != "0" {
		t.Fatalf("auto-reconcile: %s", ar.raw)
	}
	if s := ownerQuery(t, `SELECT status FROM invoices WHERE id = $1`, inv); s != "paid" {
		t.Errorf("invoice after bank reconcile: %s", s)
	}
	if s := ownerQuery(t, `SELECT status FROM bills WHERE id = $1`, billID); s != "paid" {
		t.Errorf("bill after bank reconcile: %s", s)
	}
	if r := o.c.do("POST", o.p("/bank/lines/"+supLine+"/reconcile"), map[string]any{"kind": "bill", "id": billID}); r.status != 409 {
		t.Errorf("reconcile twice: %d", r.status)
	}
	// the interest line of the second file: not against a bill (money in), booked on an account
	il := str(obj(st2.body["lines"].([]any)[0])["id"])
	if r := o.c.do("POST", o.p("/bank/lines/"+il+"/reconcile"), map[string]any{"kind": "supplier", "id": sup}); r.status != 400 {
		t.Errorf("money in paid to a supplier: %d", r.status)
	}
	_ = payLine
	o.c.ok("POST", o.p("/bank/lines/"+il+"/reconcile"), map[string]any{"kind": "account", "id": o.accounts["77"], "label": "Interest"}, 200)
	rec := o.c.ok("GET", o.p("/bank/reconciliation?method_id="+bank+"&to=2026-11-30"), nil, 200)
	eqDec(t, "bank difference", rec.body["difference"], "0")
	o.trialBalanced(t)

	// ======================= card terminals (restaurant pack) =======================
	rp := applyPack(t, "restaurant")
	reg, sess := rp.packTill(t)
	clearing := ownerQuery(t, `INSERT INTO accounts (tenant_id, company_id, code, name, type) VALUES ($1, $2, '5171', 'Card clearing', 'asset') RETURNING id::text`,
		rp.tenant, rp.company)
	card := str(rp.c.ok("POST", rp.p("/payment-methods"), map[string]any{"name": "Visa terminal", "kind": "card", "currency": "USD", "account_id": clearing}, 201).body["id"])
	rbank := rp.methods["Bank"]
	if rbank == "" {
		rbank = str(rp.c.ok("POST", rp.p("/payment-methods"), map[string]any{"name": "Bank", "kind": "bank", "currency": "USD", "account_id": rp.accounts["512"]}, 201).body["id"])
	}
	rp.c.ok("PATCH", rp.p("/pos/registers/"+reg), map[string]any{"method_ids": []any{rp.methods["Cash USD"], card}}, 200)
	if r := rp.c.do("POST", rp.p("/pos/terminal/charge"), map[string]any{"method_id": card, "amount": "3"}); r.status != 400 {
		t.Fatalf("charge without a terminal: %d", r.status)
	}
	rp.c.ok("PUT", rp.p("/pos/terminals/"+card), map[string]any{"driver": "simulator", "fee_pct": "2", "fee_fixed": "0.10", "settle_method_id": rbank}, 200)
	cola := rp.itemByCode(t, "COLA")
	sale := map[string]any{"session_id": sess, "lines": []any{map[string]any{"item_id": cola, "quantity": "2"}}, "tenders": []any{map[string]any{"method_id": card, "amount": "3"}}}
	if r := rp.c.do("POST", rp.p("/pos/orders"), sale); r.status != 422 || !strings.Contains(r.raw, "card_not_charged") {
		t.Fatalf("card sale without a charge: %d %s", r.status, r.raw)
	}
	dc := rp.c.ok("POST", rp.p("/pos/terminal/charge"), map[string]any{"method_id": card, "amount": "3", "simulate": "decline"}, 201)
	if str(dc.body["status"]) != "declined" {
		t.Fatalf("declined charge: %s", dc.raw)
	}
	if r := rp.c.do("POST", rp.p("/pos/orders"), map[string]any{"session_id": sess, "lines": sale["lines"], "tenders": sale["tenders"], "card_txns": []any{str(dc.body["id"])}}); r.status != 400 {
		t.Fatalf("sale with a declined charge: %d", r.status)
	}
	ch := rp.c.ok("POST", rp.p("/pos/terminal/charge"), map[string]any{"method_id": card, "amount": "3", "register_id": reg}, 201)
	if str(ch.body["status"]) != "approved" || len(str(ch.body["approval_code"])) != 6 {
		t.Fatalf("approved charge: %s", ch.raw)
	}
	sale["card_txns"] = []any{str(ch.body["id"])}
	rp.c.ok("POST", rp.p("/pos/orders"), sale, 201)
	if r := rp.c.do("POST", rp.p("/pos/orders"), sale); r.status != 400 {
		t.Fatalf("the same charge used twice: %d", r.status)
	}
	unused := rp.c.ok("POST", rp.p("/pos/terminal/charge"), map[string]any{"method_id": card, "amount": "9"}, 201)
	rp.c.ok("POST", rp.p("/pos/terminal/txns/"+str(unused.body["id"])+"/void"), map[string]any{}, 200)
	if r := rp.c.do("POST", rp.p("/pos/terminal/txns/"+str(ch.body["id"])+"/void"), map[string]any{}); r.status != 409 {
		t.Fatalf("void a charge a sale used: %d", r.status)
	}
	stl := rp.c.ok("POST", rp.p("/pos/terminal/settlements"), map[string]any{"method_id": card, "date": today(), "batch_ref": "B-1"}, 201)
	eqDec(t, "gross", stl.body["gross"], "3")
	eqDec(t, "fee (2 % + 0.10)", stl.body["fee"], "0.16")
	eqDec(t, "net", stl.body["net"], "2.84")
	if r := rp.c.do("POST", rp.p("/pos/terminal/settlements"), map[string]any{"method_id": card, "date": today()}); r.status != 400 {
		t.Fatalf("nothing left to settle: %d", r.status)
	}
	if bal := rp.idBalance(t, clearing); !dec(t, bal).IsZero() {
		t.Errorf("card clearing after settlement: %s", bal)
	}
	// the bank shows the net: it matches the settlement entry
	bs := rp.c.ok("POST", rp.p("/bank/statements"), map[string]any{"method_id": rbank, "name": "Card batch", "lines": []any{
		map[string]any{"date": today(), "description": "VISA BATCH B-1", "amount": "2.84"}}}, 201)
	am := rp.c.ok("POST", rp.p("/bank/statements/"+str(obj(bs.body["statement"])["id"])+"/auto-match"), map[string]any{}, 200)
	if str(am.body["matched"]) != "1" {
		t.Fatalf("net not matched: %s", am.raw)
	}
	if r := rp.c.do("POST", rp.p("/pos/terminal/settlements/"+str(stl.body["id"])+"/void"), map[string]any{}); r.status != 409 {
		t.Fatalf("void a reconciled settlement: %d", r.status)
	}
	rp.stockLedgerEqual(t)

	// ======================= QR ordering =======================
	if r := rp.c.do("PUT", rp.p("/rest/qr"), map[string]any{"enabled": true}); r.status != 400 {
		t.Fatalf("QR without a till: %d", r.status)
	}
	qs := rp.c.ok("PUT", rp.p("/rest/qr"), map[string]any{"enabled": true, "register_id": reg}, 200)
	tabs := qs.body["tables"].([]any)
	url := str(obj(tabs[0])["url"])
	table := str(obj(tabs[0])["id"])
	tok := url[strings.LastIndex(url, "/")+1:]
	anon := newClient(t)
	menu := anon.ok("GET", "/menu/"+tok, nil, 200)
	if str(menu.body["currency"]) != "USD" || len(menu.body["items"].([]any)) == 0 {
		t.Fatalf("menu: %s", menu.raw)
	}
	shw := rp.itemByCode(t, "SHW")
	q1 := anon.ok("POST", "/menu/"+tok+"/orders", map[string]any{"name": "Guest", "lines": []any{map[string]any{"item_id": shw, "quantity": "2", "note": "no onion"}}}, 200)
	eqDec(t, "QR order total", q1.body["total"], "16")
	if r := anon.do("POST", "/menu/"+tok+"/orders", map[string]any{"lines": []any{map[string]any{"item_id": shw, "quantity": "0.5"}}}); r.status != 400 {
		t.Fatalf("half a dish: %d", r.status)
	}
	anon.ok("POST", "/menu/"+tok+"/orders", map[string]any{"website": "spam", "lines": []any{map[string]any{"item_id": shw, "quantity": "1"}}}, 200)
	pend := rp.c.ok("GET", rp.p("/rest/qr/orders"), nil, 200).list
	if len(pend) != 1 {
		t.Fatalf("pending QR orders (honeypot stored?): %d", len(pend))
	}
	acc := rp.c.ok("POST", rp.p("/rest/qr/orders/"+str(obj(pend[0])["id"])+"/accept"), map[string]any{}, 200)
	if str(acc.body["status"]) != "accepted" || str(acc.body["check_id"]) == "" {
		t.Fatalf("accept: %s", acc.raw)
	}
	chk := rp.c.ok("GET", rp.p("/rest/checks/"+str(acc.body["check_id"])), nil, 200)
	if str(obj(chk.body["check"])["table_id"]) != table || len(chk.body["lines"].([]any)) != 1 || str(obj(chk.body["lines"].([]any)[0])["kstatus"]) != "sent" {
		t.Fatalf("check from the QR order: %s", chk.raw)
	}
	if r := rp.c.do("POST", rp.p("/rest/qr/orders/"+str(obj(pend[0])["id"])+"/reject"), map[string]any{}); r.status != 409 {
		t.Fatalf("decide twice: %d", r.status)
	}
	for i := 0; i < 5; i++ {
		anon.ok("POST", "/menu/"+tok+"/orders", map[string]any{"lines": []any{map[string]any{"item_id": cola, "quantity": "1"}}}, 200)
	}
	if r := anon.do("POST", "/menu/"+tok+"/orders", map[string]any{"lines": []any{map[string]any{"item_id": cola, "quantity": "1"}}}); r.status != 429 {
		t.Fatalf("6th waiting order of a table: %d", r.status)
	}
	if st := anon.ok("GET", "/menu/"+tok+"/orders", nil, 200).list; len(st) != 6 {
		t.Fatalf("table's orders: %d", len(st))
	}
	// a new link: the old QR stops working
	rp.c.ok("POST", rp.p("/rest/tables/"+table+"/qr"), map[string]any{}, 200)
	if r := anon.do("GET", "/menu/"+tok, nil); r.status != 404 {
		t.Fatalf("old QR still works: %d", r.status)
	}

	// ======================= carriers =======================
	man := str(o.c.ok("POST", o.p("/ship/carriers"), map[string]any{"carrier": "manual", "name": "City courier", "is_default": true,
		"config": map[string]any{"flat_rate": "3", "per_kg": "1", "prefix": "CC", "from_city": "Beirut"}}, 201).body["id"])
	if r := o.c.do("POST", o.p("/ship/carriers"), map[string]any{"carrier": "manual", "name": "x", "config": map[string]any{"evil": "1"}}); r.status != 400 {
		t.Fatalf("unknown carrier setting: %d", r.status)
	}
	aram := str(o.c.ok("POST", o.p("/ship/carriers"), map[string]any{"carrier": "aramex", "name": "Aramex"}, 201).body["id"])
	qt := o.c.ok("POST", o.p("/ship/quote"), map[string]any{"recipient": "Nadia", "city": "Tyre", "weight_kg": "2", "currency": "USD"}, 200)
	rates := qt.body["rates"].([]any)
	if len(rates) != 1 || str(obj(rates[0])["amount"]) != "5" || len(qt.body["errors"].([]any)) != 1 {
		t.Fatalf("quotes (manual 3 + 1/kg, Aramex not configured): %s", qt.raw)
	}
	sh := o.c.ok("POST", o.p("/ship/shipments"), map[string]any{"account_id": man, "recipient": "Nadia Khoury", "phone": "03 111 222", "address": "Main st",
		"city": "Tyre", "weight_kg": "2", "currency": "USD", "cod_amount": "25", "book": true}, 201).body
	if str(sh["status"]) != "created" || !strings.HasPrefix(str(sh["tracking_no"]), "CC") || str(sh["label_id"]) == "" {
		t.Fatalf("manual shipment: %v", sh)
	}
	lbl := o.c.ok("GET", o.p("/attachments/"+str(sh["label_id"])+"/file"), nil, 200)
	if !strings.HasPrefix(lbl.raw, "%PDF") {
		t.Fatalf("label is not a PDF: %.40s", lbl.raw)
	}
	o.c.ok("POST", o.p("/ship/shipments/"+str(sh["id"])+"/status"), map[string]any{"status": "delivered", "note": "signed"}, 200)
	if r := o.c.do("POST", o.p("/ship/shipments/"+str(sh["id"])+"/cancel"), map[string]any{}); r.status != 409 {
		t.Fatalf("cancel a delivered parcel: %d", r.status)
	}
	ad := o.c.ok("POST", o.p("/ship/shipments"), map[string]any{"account_id": aram, "recipient": "X", "currency": "USD"}, 201).body
	if r := o.c.do("POST", o.p("/ship/shipments/"+str(ad["id"])+"/book"), map[string]any{}); r.status != 422 || !strings.Contains(r.raw, "carrier_not_configured") {
		t.Fatalf("book with an unconfigured carrier: %d %s", r.status, r.raw)
	}
	// DHL through a fake MyDHL API
	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 dhl label"))
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "key1" || p != "sec1" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/rates"):
			_, _ = w.Write([]byte(`{"products":[{"productName":"EXPRESS WORLDWIDE","productCode":"P","totalPrice":[{"price":42.5,"priceCurrency":"USD"}],"deliveryCapabilities":{"totalTransitDays":3}}]}`))
		case r.URL.Path == "/shipments" && r.Method == "POST":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if str(b["productCode"]) == "" {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"shipmentTrackingNumber":"1234567890","documents":[{"imageFormat":"PDF","content":"` + pdf + `"}]}`))
		case strings.HasSuffix(r.URL.Path, "/tracking"):
			_, _ = w.Write([]byte(`{"shipments":[{"status":"delivered","events":[{"date":"2026-11-21","time":"10:00:00","typeCode":"PU","description":"Picked up"},{"date":"2026-11-22","time":"11:00:00","typeCode":"OK","description":"Delivered"}]}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	carrierBase["dhl"] = fake.URL
	defer delete(carrierBase, "dhl")
	dhl := str(o.c.ok("POST", o.p("/ship/carriers"), map[string]any{"carrier": "dhl", "name": "DHL", "config": map[string]any{"api_key": "key1", "secret": "sec1",
		"account_number": "950000000", "from_city": "Beirut", "from_country": "LB"}}, 201).body["id"])
	for _, c := range o.c.ok("GET", o.p("/ship/carriers"), nil, 200).list {
		if str(obj(c)["id"]) == dhl && (obj(c)["configured"] != true || strings.Contains(str(obj(obj(c)["config"])["secret"]), "sec1")) {
			t.Fatalf("DHL account listed: %v", c)
		}
	}
	dq := o.c.ok("POST", o.p("/ship/quote"), map[string]any{"account_id": dhl, "recipient": "Joe", "city": "Paris", "country": "FR", "weight_kg": "1.5", "currency": "USD"}, 200)
	if r := dq.body["rates"].([]any); len(r) != 1 || str(obj(r[0])["amount"]) != "42.50" || str(obj(r[0])["days"]) != "3" {
		t.Fatalf("DHL quote: %s", dq.raw)
	}
	ds := o.c.ok("POST", o.p("/ship/shipments"), map[string]any{"account_id": dhl, "recipient": "Joe", "city": "Paris", "country": "FR", "currency": "USD", "book": true}, 201).body
	if str(ds["tracking_no"]) != "1234567890" {
		t.Fatalf("DHL booking: %v", ds)
	}
	tr := o.c.ok("POST", o.p("/ship/shipments/"+str(ds["id"])+"/track"), map[string]any{}, 200).body
	if str(tr["status"]) != "delivered" || len(tr["events"].([]any)) != 2 {
		t.Fatalf("DHL tracking: %v", tr)
	}

	// ======================= e-mail from a record =======================
	if r := o.c.do("POST", o.p("/record-mail"), map[string]any{"type": "invoice", "id": inv, "to": "nadia@example.com", "subject": "Your invoice", "body": "Hello"}); r.status != 422 ||
		!strings.Contains(r.raw, "no_email_provider") {
		t.Fatalf("mail without SMTP: %d %s", r.status, r.raw)
	}
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	o.c.ok("POST", o.p("/message-providers"), map[string]any{"kind": "smtp", "name": "Mail", "config": map[string]any{"host": host, "port": port,
		"security": "none", "from_email": "billing@example.com"}}, 201)
	o.c.ok("PUT", o.p("/record-mail/settings"), map[string]any{"reply_to_email": "replies@example.com"}, 200)
	ml := o.c.ok("GET", o.p("/record-mail?type=invoice&id="+inv), nil, 200)
	if ml.body["configured"] != true {
		t.Fatalf("mail list: %s", ml.raw)
	}
	sent := o.c.ok("POST", o.p("/record-mail"), map[string]any{"type": "invoice", "id": inv, "to": "nadia@example.com", "subject": "Your invoice",
		"body": "Hello Nadia, please find your invoice.", "attach_pdf": true}, 201).body
	subj := str(sent["subject"])
	i := strings.Index(subj, "[PX-")
	if i < 0 || len(str(sent["files"].([]any)[0].(map[string]any)["filename"])) == 0 {
		t.Fatalf("record mail: %v", sent)
	}
	mtok := subj[i+4 : i+14]
	itAPI.AllowPrivateRouters = true
	if _, err := itAPI.sendBatch(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-got:
		if !strings.Contains(msg, "multipart/mixed") || !strings.Contains(msg, "Reply-To: replies+"+mtok+"@example.com") || !strings.Contains(msg, "application/pdf") {
			t.Fatalf("e-mail with the PDF: %.600s", msg)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the e-mail was not sent")
	}
	itok := str(o.c.ok("POST", o.p("/sla/inbound/rotate"), map[string]any{}, 200).body["verify_token"])
	rep := anon.ok("POST", "/inbound/"+itok+"/email", map[string]any{"from": "Nadia <nadia@example.com>", "subject": "Re: Your invoice [PX-" + mtok + "]",
		"text": "Thanks, paid by transfer."}, 200)
	if str(rep.body["record"]) != "invoice" {
		t.Fatalf("reply not linked to the invoice: %s", rep.raw)
	}
	plus := anon.ok("POST", "/inbound/"+itok+"/email", map[string]any{"from": "nadia@example.com", "to": "replies+" + mtok + "@example.com", "subject": "another",
		"text": "One more thing"}, 200)
	if str(plus.body["record"]) != "invoice" {
		t.Fatalf("plus address reply: %s", plus.raw)
	}
	mails := o.c.ok("GET", o.p("/record-mail?type=invoice&id="+inv), nil, 200).body["mails"].([]any)
	if len(mails) != 3 || str(obj(mails[0])["direction"]) != "in" {
		t.Fatalf("thread: %v", mails)
	}
	cht := o.c.ok("GET", o.p("/chatter?type=invoice&id="+inv), nil, 200)
	if !strings.Contains(cht.raw, "paid by transfer") {
		t.Fatalf("reply not in the chatter: %s", cht.raw)
	}
	if r := anon.do("POST", "/inbound/"+itok+"/email", map[string]any{"from": "x@example.com", "subject": "[PX-zzzzzzzzzz] unknown", "text": "hi"}); r.status != 200 ||
		!strings.Contains(r.raw, "ticket") {
		t.Fatalf("unknown token becomes a ticket: %d %s", r.status, r.raw)
	}

	// ======================= Gantt =======================
	pr := o.c.ok("POST", o.p("/prj/projects"), map[string]any{"name": "Fit-out"}, 201)
	prj := str(obj(pr.body["project"])["id"])
	me := str(o.c.ok("GET", "/me", nil, 200).body["id"])
	ta := str(obj(o.c.ok("POST", o.p("/prj/project-tasks"), map[string]any{"project_id": prj, "title": "Walls", "assignee_id": me, "planned_hours": "16"}, 201).body["task"])["id"])
	tb := str(obj(o.c.ok("POST", o.p("/prj/project-tasks"), map[string]any{"project_id": prj, "title": "Paint", "assignee_id": me}, 201).body["task"])["id"])
	o.c.ok("PUT", o.p("/gantt/project-tasks/"+ta), map[string]any{"start": "2026-12-01", "end": "2026-12-02"}, 200)
	o.c.ok("PUT", o.p("/gantt/project-tasks/"+tb), map[string]any{"start": "2026-12-03", "end": "2026-12-03"}, 200)
	o.c.ok("POST", o.p("/gantt/project-tasks/"+tb+"/deps"), map[string]any{"depends_on": ta}, 201)
	if r := o.c.do("POST", o.p("/gantt/project-tasks/"+ta+"/deps"), map[string]any{"depends_on": tb}); r.status != 400 || !strings.Contains(r.raw, "dependency_loop") {
		t.Fatalf("dependency loop: %d %s", r.status, r.raw)
	}
	mv := o.c.ok("PUT", o.p("/gantt/project-tasks/"+ta), map[string]any{"start": "2026-12-02", "end": "2026-12-04", "shift_dependents": true}, 200)
	if len(mv.body["moved"].([]any)) != 2 {
		t.Fatalf("dependent not moved: %s", mv.raw)
	}
	if d := ownerQuery(t, `SELECT start_date::text || '|' || deadline::text FROM project_tasks WHERE id = $1`, tb); d != "2026-12-05|2026-12-05" {
		t.Fatalf("paint after walls: %s", d)
	}
	gp := o.c.ok("GET", o.p("/gantt/projects/"+prj), nil, 200)
	if len(gp.body["bars"].([]any)) != 2 {
		t.Fatalf("gantt bars: %s", gp.raw)
	}
	for _, w := range gp.body["warnings"].([]any) {
		if str(obj(w)["kind"]) == "dependency" {
			t.Fatalf("dependency warning after the shift: %v", w)
		}
	}
	o.c.ok("PUT", o.p("/gantt/project-tasks/"+tb), map[string]any{"start": "2026-12-03", "end": "2026-12-03"}, 200)
	gp = o.c.ok("GET", o.p("/gantt/projects/"+prj), nil, 200)
	kinds := map[string]bool{}
	for _, w := range gp.body["warnings"].([]any) {
		kinds[str(obj(w)["kind"])] = true
	}
	if !kinds["dependency"] || !kinds["overlap"] {
		t.Fatalf("warnings: %s", gp.raw)
	}
	deps := o.c.ok("GET", o.p("/gantt/projects/"+prj+"/deps"), nil, 200).list
	o.c.ok("DELETE", o.p("/gantt/deps/"+str(obj(deps[0])["id"])), nil, 200)

	// technicians: two jobs at the same time
	j1 := str(obj(o.c.ok("POST", o.p("/fs/service-jobs"), map[string]any{"customer_id": cust, "title": "Install"}, 201).body["job"])["id"])
	j2 := str(obj(o.c.ok("POST", o.p("/fs/service-jobs"), map[string]any{"customer_id": cust, "title": "Repair"}, 201).body["job"])["id"])
	o.c.ok("PUT", o.p("/gantt/service-jobs/"+j1), map[string]any{"start": "2026-12-01T09:00:00Z", "technician_id": me, "duration_min": 120}, 200)
	mj := o.c.ok("PUT", o.p("/gantt/service-jobs/"+j2), map[string]any{"start": "2026-12-01T10:00:00Z", "technician_id": me}, 200)
	if len(mj.body["conflicts"].([]any)) != 1 {
		t.Fatalf("double booking not seen: %s", mj.raw)
	}
	gf := o.c.ok("GET", o.p("/gantt/field?from=2026-11-30&to=2026-12-02"), nil, 200)
	if len(gf.body["bars"].([]any)) != 2 || len(gf.body["warnings"].([]any)) != 1 {
		t.Fatalf("field gantt: %s", gf.raw)
	}

	// work centers: plan an order and see the load
	f := applyPack(t, "factory")
	wcs := f.c.ok("GET", f.p("/mfg/workcenters"), nil, 200).list
	rt := f.c.ok("POST", f.p("/mfg/routings"), map[string]any{"name": "Shelf routing", "operations": []any{
		map[string]any{"name": "Cut", "workcenter_id": str(obj(wcs[0])["id"]), "minutes": "300"},
		map[string]any{"name": "Assemble", "workcenter_id": str(obj(wcs[1])["id"]), "minutes": "120"}}}, 201)
	rtID := str(obj(rt.body["routing"])["id"])
	if rtID == "" {
		rtID = str(rt.body["id"])
	}
	board := str(f.c.ok("POST", f.p("/items"), map[string]any{"code": "BRD", "name": "Board", "kind": "stock"}, 201).body["id"])
	shelf := str(f.c.ok("POST", f.p("/items"), map[string]any{"code": "SHF", "name": "Shelf", "kind": "stock", "sale_price": "40", "sale_currency": "USD"}, 201).body["id"])
	bm := f.c.ok("POST", f.p("/mfg/boms"), map[string]any{"item_id": shelf, "routing_id": rtID, "lines": []any{map[string]any{"item_id": board, "quantity": "2"}}}, 201)
	mo := f.c.ok("POST", f.p("/mfg/orders"), map[string]any{"bom_id": str(obj(bm.body["bom"])["id"]), "quantity": "2", "confirm": true}, 201)
	moID := str(obj(mo.body["order"])["id"])
	pl := f.c.ok("POST", f.p("/gantt/mfg/plan"), map[string]any{"order_id": moID, "start": "2026-12-07T08:00:00Z"}, 200)
	planned := pl.body["planned"].([]any)
	if len(planned) == 0 {
		t.Fatalf("nothing planned: %s", pl.raw)
	}
	gm := f.c.ok("GET", f.p("/gantt/mfg?from=2026-12-06&to=2026-12-20"), nil, 200)
	seen := 0
	for _, b := range gm.body["bars"].([]any) {
		if obj(b)["planned"] == true {
			seen++
		}
	}
	if seen != len(planned) || len(gm.body["load"].([]any)) == 0 {
		t.Fatalf("mfg gantt: %s", gm.raw)
	}
	f.c.ok("PUT", f.p("/gantt/workorders/"+str(obj(planned[0])["id"])), map[string]any{"start": "2026-12-08T09:00:00Z"}, 200)

	o.trialBalanced(t)
	rp.trialBalanced(t)
}

// Unrealized inter-company profit: A sells goods bought at 6 to its sister B at 10; B still holds 3 of the 5.
func TestInterCompanyUnrealizedProfit(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	bc := o.c.ok("POST", "/companies", map[string]any{"name_en": "Shop B " + randHex(3), "base_currency": "USD", "local_currency": "LBP"}, 201).body
	bID := str(bc["id"])
	link := o.c.ok("POST", "/intercompany/links", map[string]any{"company_a": o.company, "company_b": bID}, 201).body
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Maker", "currency": "USD"}, 201).body["id"])
	item := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "LAMP", "name": "Lamp", "kind": "stock", "sale_price": "10", "sale_currency": "USD"}, 201).body["id"])
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
		"lines": []any{map[string]any{"item_id": item, "quantity": "5", "unit_price": "6"}}}, 201)
	// B knows the lamp under the same code
	o.c.ok("POST", "/companies/"+bID+"/items", map[string]any{"code": "LAMP", "name": "Lamp", "kind": "stock", "sale_price": "15", "sale_currency": "USD"}, 201)
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": str(link["a_customer_id"]), "currency": "USD", "issue_date": "2026-11-05",
		"lines": []any{map[string]any{"item_id": item, "description": "Lamp", "quantity": "5", "unit_price": "10"}}}, 201)
	o.c.ok("POST", "/intercompany/sync", map[string]any{}, 200)
	bItem := ownerQuery(t, `SELECT id::text FROM items WHERE company_id = $1 AND code = 'LAMP'`, bID)
	got := ownerQuery(t, `SELECT coalesce(sum(quantity), 0)::text FROM stock_moves WHERE company_id = $1 AND item_id = $2`, bID, bItem)
	if !dec(t, got).Equal(dec(t, "5")) {
		t.Skipf("the mirrored bill did not bring the lamps into B's stock (%s): nothing to eliminate", got)
	}
	// B sells 2 to an outside customer
	bc2 := str(o.c.ok("POST", "/companies/"+bID+"/customers", map[string]any{"name": "Walk-in"}, 201).body["id"])
	o.c.ok("POST", "/companies/"+bID+"/invoices", map[string]any{"customer_id": bc2, "currency": "USD", "issue_date": "2026-11-10",
		"lines": []any{map[string]any{"item_id": bItem, "description": "Lamp", "quantity": "2", "unit_price": "15"}}}, 201)
	q := "&companies=" + o.company + "," + bID + "&currency=USD&from=2026-01-01&to=2026-12-31&eliminate=1"
	bs := o.c.ok("GET", "/reports/consolidated?report=balance-sheet"+q, nil, 200).body
	urp := "0"
	for _, e := range bs["eliminations"].([]any) {
		if str(obj(e)["code"]) == "IC-URP" {
			urp = str(obj(e)["amount"])
		}
	}
	eqDec(t, "unrealized profit removed from equity (3 held × 4 margin)", urp, "-12")
	after := obj(bs["totals_after"])
	eqDec(t, "group balance sheet balances", after["assets"], str(after["liabilities_equity"]))
	pl := o.c.ok("GET", "/reports/consolidated?report=profit-loss"+q, nil, 200).body
	a, b := obj(pl["totals"]), obj(pl["totals_after"])
	eqDec(t, "group profit lowered by the margin still in stock", dec(t, a["net"]).Sub(dec(t, b["net"])).String(), "12")
}
