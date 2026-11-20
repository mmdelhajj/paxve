package httpapi

import (
	"strings"
	"sync"
	"testing"
)

// Phase 7 audit regressions (accounting audit B probes, now with the expected results).

func countStatus(st []int, want int) int {
	n := 0
	for _, s := range st {
		if s == want {
			n++
		}
	}
	return n
}

// H1: an insured pharmacy sale refunded at the till pays back only the patient's share; the claim is cancelled.
func TestP7InsuredRefund(t *testing.T) {
	needDB(t)
	o := applyPack(t, "pharmacy")
	_, sess := o.packTill(t)
	usd := o.methods["Cash USD"]
	pan := o.itemByCode(t, "PAN500")
	insurer := ownerQuery(t, `SELECT id::text FROM customers WHERE company_id = $1 AND name LIKE 'Demo Insurance%'`, o.company)
	s := o.c.ok("POST", o.p("/pos/orders"), map[string]any{"session_id": sess, "lines": []any{map[string]any{"item_id": pan, "quantity": "2"}},
		"tenders": []any{map[string]any{"method_id": usd, "amount": "1.80"}}, "insurance": map[string]any{"insurer_id": insurer, "pct": "80"}}, 201)
	oid := str(obj(s.body["order"])["id"])
	o.c.ok("POST", o.p("/pos/orders/"+oid+"/refund"), map[string]any{"session_id": sess, "method_id": usd, "reason": "returned"}, 201)
	if cash := ownerQuery(t, `SELECT coalesce(sum(paid), 0)::text FROM pos_orders WHERE refund_of = $1`, oid); cash != "1.8000" && cash != "1.80" && cash != "1.8" {
		t.Fatalf("cash refunded %s, want 1.80 (the patient's share only)", cash)
	}
	if st := ownerQuery(t, `SELECT status FROM pharma_claims WHERE pos_order_id = $1`, oid); st != "cancelled" {
		t.Fatalf("claim status %s, want cancelled", st)
	}
	if ins := ownerQuery(t, `SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::numeric(20,2)::text FROM journal_lines l
		JOIN companies c ON c.insurance_account_id = l.account_id WHERE c.id = $1`, o.company); ins != "0.00" {
		t.Fatalf("insurance receivable %s, want 0", ins)
	}
	o.stockLedgerEqual(t)
	o.trialBalanced(t)
}

// M4: a trade-in is never paid out in cash on a refund: the cash part comes back, the device part stays as credit.
func TestP7TradeInRefund(t *testing.T) {
	needDB(t)
	o := applyPack(t, "electronics")
	_, sess := o.packTill(t)
	usd := o.methods["Cash USD"]
	used, charger := o.itemByCode(t, "PH-USED"), o.itemByCode(t, "CHG")
	cust := o.customer(t, "Rami", "")
	s := o.c.ok("POST", o.p("/pos/orders"), map[string]any{"session_id": sess, "customer_id": cust, "lines": []any{map[string]any{"item_id": charger, "quantity": "1"}},
		"trade_in": []any{map[string]any{"item_id": used, "serial": "USED-901", "value": "10"}},
		"tenders":  []any{map[string]any{"method_id": usd, "amount": "5"}}}, 201)
	oid := str(obj(s.body["order"])["id"])
	o.c.ok("POST", o.p("/pos/orders/"+oid+"/refund"), map[string]any{"session_id": sess, "method_id": usd}, 201)
	if cash := ownerQuery(t, `SELECT coalesce(sum(paid), 0)::numeric(20,2)::text FROM pos_orders WHERE refund_of = $1`, oid); cash != "5.00" {
		t.Fatalf("cash refunded %s, want 5 (the trade-in stays as credit)", cash)
	}
	if cr := ownerQuery(t, `SELECT coalesce(sum(p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0)), 0)::numeric(20,2)::text
		FROM payments p WHERE p.customer_id = $1 AND p.status = 'posted'`, cust); cr != "10.00" {
		t.Fatalf("customer credit %s, want 10", cr)
	}
	o.stockLedgerEqual(t)
	o.trialBalanced(t)
}

// Six tills sell the last unit of a lot at once: exactly one sale.
func TestP7LastUnitRace(t *testing.T) {
	needDB(t)
	o := applyPack(t, "pharmacy")
	_, sess := o.packTill(t)
	usd := o.methods["Cash USD"]
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Depot", "currency": "USD"}, 201).body["id"])
	it := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "ONE", "name": "One box", "kind": "stock", "sale_price": "3", "sale_currency": "USD"}, 201).body["id"])
	o.c.ok("PUT", o.p("/items/"+it+"/stock-settings"), map[string]any{"tracking": "lot"}, 200)
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "currency": "USD", "lines": []any{map[string]any{"item_id": it, "quantity": "1", "unit_price": "1",
		"lots": []any{map[string]any{"lot_no": "L1", "quantity": "1", "expiry": "2030-01-31"}}}}}, 201)
	var wg sync.WaitGroup
	st := make([]int, 6)
	for i := range st {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st[i] = o.c.do("POST", o.p("/pos/orders"), map[string]any{"session_id": sess, "lines": []any{map[string]any{"item_id": it, "quantity": "1"}},
				"tenders": []any{map[string]any{"method_id": usd, "amount": "3"}}}).status
		}(i)
	}
	wg.Wait()
	if countStatus(st, 201) != 1 {
		t.Fatalf("statuses %v: want exactly one sale", st)
	}
	o.stockLedgerEqual(t)
}

// M2: the supplier openings import never books the same opening twice.
func TestP7SupplierOpeningsReplay(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	sup := o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Old Supplier", "currency": "USD"}, 201).body
	rows := []any{map[string]any{"supplier_code": str(sup["code"]), "amount": "100", "currency": "USD", "reference": "OLD-1", "date": "2026-01-15"}}
	o.c.ok("POST", o.p("/import/supplier_openings"), map[string]any{"rows": rows}, 200)
	if r := o.c.do("POST", o.p("/import/supplier_openings"), map[string]any{"rows": rows}); r.status != 422 || !strings.Contains(r.raw, "already_imported") {
		t.Fatalf("second import %d %s: want 422 already_imported", r.status, r.raw)
	}
	twice := []any{rows[0], map[string]any{"supplier_code": str(sup["code"]), "amount": "7", "currency": "USD", "reference": "X", "date": "2026-01-16"},
		map[string]any{"supplier_code": str(sup["code"]), "amount": "7", "currency": "USD", "reference": "X", "date": "2026-01-16"}}
	if r := o.c.do("POST", o.p("/import/supplier_openings"), map[string]any{"rows": twice[1:]}); r.status != 422 || !strings.Contains(r.raw, "duplicate") {
		t.Fatalf("same line twice %d %s: want 422 duplicate", r.status, r.raw)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM bills WHERE supplier_id = $1 AND status <> 'void'`, str(sup["id"])); n != "1" {
		t.Fatalf("supplier has %s opening bills, want 1", n)
	}
	// payables cannot be the opening account (it would credit and debit the same control account)
	ap := ownerQuery(t, `SELECT ap_account_id::text FROM companies WHERE id = $1`, o.company)
	if r := o.c.do("POST", o.p("/import/supplier_openings"), map[string]any{"rows": []any{map[string]any{"supplier_code": str(sup["code"]), "amount": "3", "date": "2026-02-01"}},
		"options": map[string]any{"account_id": ap}}); r.status == 200 {
		t.Fatalf("payables accepted as the opening account: %s", r.raw)
	}
	o.c.ok("PATCH", "/companies/"+o.company, map[string]any{"lock_date": "2026-06-30"}, 200)
	if r := o.c.do("POST", o.p("/import/supplier_openings"), map[string]any{"rows": []any{map[string]any{"supplier_code": str(sup["code"]), "amount": "5", "date": "2026-02-01"}}}); r.status != 409 {
		t.Fatalf("import into a locked period: %d %s", r.status, r.raw)
	}
	o.trialBalanced(t)
}

// H2 / L4: a furniture deposit pays its own order's invoice, never the customer's older bills; cancelling an order
// with a deposit asks what to do with it.
func TestP7DepositAllocation(t *testing.T) {
	needDB(t)
	o := applyPack(t, "furniture")
	cust := o.customer(t, "Old debtor", "")
	old := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-10-01",
		"lines": []any{map[string]any{"description": "old", "quantity": "1", "unit_price": "200"}}}, 201)
	oldID := str(old.body["id"])
	if oldID == "" {
		oldID = str(obj(old.body["invoice"])["id"])
	}
	q := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": cust, "currency": "USD",
		"lines": []any{map[string]any{"description": "Sofa", "quantity": "1", "unit_price": "1000"}}}, 201)
	so := str(obj(q.body["order"])["id"])
	o.c.ok("POST", o.p("/sales/orders/"+so+"/confirm"), map[string]any{}, 200)
	o.c.ok("POST", o.p("/furn/orders/"+so+"/deposit"), map[string]any{"method_id": o.methods["Cash USD"], "amount": "300"}, 201)
	if p := ownerQuery(t, `SELECT paid::numeric(20,2)::text FROM invoices WHERE id = $1`, oldID); p != "0.00" {
		t.Fatalf("the older invoice took %s of the deposit", p)
	}
	inv := o.c.ok("POST", o.p("/sales/orders/"+so+"/invoice"), map[string]any{}, 201)
	if p := ownerQuery(t, `SELECT paid::numeric(20,2)::text FROM invoices WHERE id = $1`, str(inv.body["invoice_id"])); p != "300.00" {
		t.Fatalf("sofa invoice paid %s, want 300", p)
	}
	if p := ownerQuery(t, `SELECT paid::numeric(20,2)::text FROM invoices WHERE id = $1`, oldID); p != "0.00" {
		t.Fatalf("the older invoice took %s after invoicing", p)
	}
	// cancel an order that has a deposit
	c2 := o.customer(t, "C2", "")
	q2 := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": c2, "currency": "USD",
		"lines": []any{map[string]any{"description": "Bed", "quantity": "1", "unit_price": "500"}}}, 201)
	so2 := str(obj(q2.body["order"])["id"])
	o.c.ok("POST", o.p("/sales/orders/"+so2+"/confirm"), map[string]any{}, 200)
	o.c.ok("POST", o.p("/furn/orders/"+so2+"/deposit"), map[string]any{"method_id": o.methods["Cash USD"], "amount": "100"}, 201)
	if r := o.c.do("POST", o.p("/sales/orders/"+so2+"/cancel"), map[string]any{}); r.status != 409 || !strings.Contains(r.raw, "order_has_deposit") {
		t.Fatalf("cancel with a deposit: %d %s", r.status, r.raw)
	}
	o.c.ok("POST", o.p("/sales/orders/"+so2+"/cancel"), map[string]any{"deposit": "refund", "method_id": o.methods["Cash USD"]}, 200)
	if cr := ownerQuery(t, `SELECT coalesce(sum(p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0)), 0)::numeric(20,2)::text
		FROM payments p WHERE p.customer_id = $1 AND p.status = 'posted'`, c2); cr != "0.00" {
		t.Fatalf("refunded deposit left %s of credit", cr)
	}
	o.stockLedgerEqual(t)
	o.trialBalanced(t)
}

// L5: the van phone sends the same sale twice at once: one invoice, both answered with it.
func TestP7VanReplayRace(t *testing.T) {
	needDB(t)
	o := applyPack(t, "wholesale")
	me := str(o.c.ok("GET", "/me", nil, 200).body["id"])
	van := ownerQuery(t, `SELECT id::text FROM warehouses WHERE company_id = $1 AND is_van`, o.company)
	main := ownerQuery(t, `SELECT warehouse_id::text FROM stock s JOIN items i ON i.id = s.item_id WHERE i.company_id = $1 AND s.quantity > 0 LIMIT 1`, o.company)
	o.c.ok("PUT", o.p("/warehouses/"+van+"/vertical"), map[string]any{"van_user_id": me}, 200)
	water := o.itemByCode(t, "WTR-6")
	day := o.c.ok("POST", o.p("/van/days"), map[string]any{"warehouse_id": van, "source_warehouse_id": main, "lines": []any{map[string]any{"item_id": water, "quantity": "40"}}}, 201)
	amal := ownerQuery(t, `SELECT id::text FROM customers WHERE company_id = $1 AND name = 'Al Amal minimarket'`, o.company)
	ref := "van-" + randHex(6)
	sale := map[string]any{"client_ref": ref, "customer_id": amal, "lines": []any{map[string]any{"item_id": water, "quantity": "10"}}, "method_id": o.methods["Cash USD"], "paid": "10"}
	var wg sync.WaitGroup
	st := make([]int, 4)
	for i := range st {
		wg.Add(1)
		go func(i int) { defer wg.Done(); st[i] = o.c.do("POST", o.p("/van/sales"), sale).status }(i)
	}
	wg.Wait()
	if countStatus(st, 201) != len(st) {
		t.Fatalf("replay statuses %v: want all 201 (the same sale)", st)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM invoices WHERE customer_id = $1 AND status <> 'void'`, amal); n != "1" {
		t.Fatalf("%s invoices, want 1", n)
	}
	// a manager (stock.manage) closes with a shortage: posted at once
	o.c.ok("POST", o.p("/van/days/"+str(day.body["id"])+"/close"), map[string]any{"counted": []any{map[string]any{"item_id": water, "quantity": "25"}}, "unload": true}, 200)
	o.stockLedgerEqual(t)
}

// L5: a till sends the same order 5 times at once: one order, every answer 201.
func TestP7PosReplayRace(t *testing.T) {
	needDB(t)
	o := applyPack(t, "supermarket")
	_, sess := o.packTill(t)
	usd := o.methods["Cash USD"]
	it := ownerQuery(t, `SELECT i.id::text FROM items i JOIN stock s ON s.item_id = i.id WHERE i.company_id = $1 AND s.quantity > 5 AND i.sale_price > 0 LIMIT 1`, o.company)
	price := ownerQuery(t, `SELECT sale_price::text FROM items WHERE id = $1`, it)
	ref := "pos-" + randHex(6)
	body := map[string]any{"client_ref": ref, "session_id": sess, "lines": []any{map[string]any{"item_id": it, "quantity": "1"}},
		"tenders": []any{map[string]any{"method_id": usd, "amount": price}}}
	var wg sync.WaitGroup
	st := make([]int, 5)
	for i := range st {
		wg.Add(1)
		go func(i int) { defer wg.Done(); st[i] = o.c.do("POST", o.p("/pos/orders"), body).status }(i)
	}
	wg.Wait()
	if countStatus(st, 201) != len(st) {
		t.Fatalf("replay statuses %v: want all 201", st)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM pos_orders WHERE company_id = $1 AND client_ref = $2`, o.company, ref); n != "1" {
		t.Fatalf("%s orders, want 1", n)
	}
	o.stockLedgerEqual(t)
}

// A claim settled 4 times at once posts one entry; a rejected claim goes to its own write-off account.
func TestP7ClaimSettleRace(t *testing.T) {
	needDB(t)
	o := applyPack(t, "pharmacy")
	_, sess := o.packTill(t)
	usd := o.methods["Cash USD"]
	pan := o.itemByCode(t, "PAN500")
	insurer := ownerQuery(t, `SELECT id::text FROM customers WHERE company_id = $1 AND name LIKE 'Demo Insurance%'`, o.company)
	for i := 0; i < 2; i++ {
		o.c.ok("POST", o.p("/pos/orders"), map[string]any{"session_id": sess, "lines": []any{map[string]any{"item_id": pan, "quantity": "2"}},
			"tenders": []any{map[string]any{"method_id": usd, "amount": "1.80"}}, "insurance": map[string]any{"insurer_id": insurer, "pct": "80"}}, 201)
	}
	claims := o.c.ok("GET", o.p("/pharma/claims?status=open"), nil, 200).body["claims"].([]any)
	claim := str(obj(claims[0])["id"])
	var wg sync.WaitGroup
	st := make([]int, 4)
	for i := range st {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st[i] = o.c.do("POST", o.p("/pharma/claims/"+claim+"/settle"), map[string]any{"method_id": usd}).status
		}(i)
	}
	wg.Wait()
	if countStatus(st, 200) != 1 {
		t.Fatalf("settle statuses %v: want one success", st)
	}
	o.c.ok("POST", o.p("/pharma/claims/"+str(obj(claims[1])["id"])+"/reject"), map[string]any{"note": "not covered"}, 200)
	if src := ownerQuery(t, `SELECT string_agg(DISTINCT source, ',') FROM journal_entries WHERE company_id = $1 AND reference LIKE 'CLM-%'`, o.company); src != "insurance_claim" {
		t.Fatalf("claim entries source %q, want insurance_claim", src)
	}
	if wo := ownerQuery(t, `SELECT coalesce(sum(l.base_debit), 0)::numeric(20,2)::text FROM journal_lines l JOIN companies c ON c.claims_writeoff_account_id = l.account_id
		WHERE c.id = $1`, o.company); wo != "7.20" {
		t.Fatalf("written off %s, want 7.20 on the claims write-off account", wo)
	}
	o.trialBalanced(t)
}

func TestP7CouponRace(t *testing.T) {
	needDB(t)
	o := applyPack(t, "supermarket")
	_, sess := o.packTill(t)
	usd := o.methods["Cash USD"]
	o.c.ok("POST", o.p("/retail/promotions"), map[string]any{"name": "One", "kind": "basket", "value_type": "amount", "value": "1",
		"coupon_code": "one1", "stackable": true, "currency": "USD", "max_uses": 1}, 201)
	it := ownerQuery(t, `SELECT i.id::text FROM items i JOIN stock s ON s.item_id = i.id WHERE i.company_id = $1 AND s.quantity > 10 AND i.sale_price > 2 AND coalesce(i.sale_currency,'USD') = 'USD' LIMIT 1`, o.company)
	price := ownerQuery(t, `SELECT sale_price::text FROM items WHERE id = $1`, it)
	var wg sync.WaitGroup
	st := make([]int, 6)
	for i := range st {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st[i] = o.c.do("POST", o.p("/pos/orders"), map[string]any{"session_id": sess, "coupon": "ONE1", "apply_promos": true, "lines": []any{map[string]any{"item_id": it, "quantity": "1"}},
				"tenders": []any{map[string]any{"method_id": usd, "amount": price}}, "change_method_id": usd}).status
		}(i)
	}
	wg.Wait()
	if uses := ownerQuery(t, `SELECT count(*)::text FROM promotion_uses WHERE company_id = $1`, o.company); uses != "1" {
		t.Fatalf("coupon (max 1 use) used %s times; statuses %v", uses, st)
	}
	o.stockLedgerEqual(t)
}

func TestP7DropshipBillVoid(t *testing.T) {
	needDB(t)
	o := applyPack(t, "supermarket")
	cust := o.customer(t, "Dropship buyer", "")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Far supplier", "currency": "USD"}, 201).body["id"])
	it := ownerQuery(t, `SELECT i.id::text FROM items i WHERE i.company_id = $1 AND i.kind = 'stock' LIMIT 1`, o.company)
	so := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": cust, "currency": "USD", "lines": []any{
		map[string]any{"item_id": it, "description": "x", "quantity": "3", "unit_price": "20"}}}, 201)
	soID := str(obj(so.body["order"])["id"])
	o.c.ok("POST", o.p("/sales/orders/"+soID+"/confirm"), map[string]any{}, 200)
	line := str(obj(so.body["lines"].([]any)[0])["id"])
	ds := o.c.ok("POST", o.p("/wh/dropship"), map[string]any{"order_id": soID, "supplier_id": sup, "lines": []any{map[string]any{"line_id": line, "quantity": "3", "unit_price": "11"}}}, 201)
	po := str(obj(ds.body["order"])["id"])
	o.c.ok("POST", o.p("/purchase-orders/"+po+"/confirm"), map[string]any{}, 200)
	o.c.ok("POST", o.p("/purchase-orders/"+po+"/bill"), map[string]any{"bill_date": "2026-11-16", "supplier_ref": "DS-1", "confirm": true}, 201)
	bill := ownerQuery(t, `SELECT id::text FROM bills WHERE supplier_id = $1`, sup)
	if r := o.c.do("POST", o.p("/bills/"+bill+"/void"), map[string]any{"reason": "wrong"}); r.status < 400 {
		t.Fatalf("voiding a dropship bill after the automatic delivery: %d", r.status)
	}
	o.stockLedgerEqual(t)
}

// L3 / M1: a refunded restaurant dish does not put its cooked ingredients back; commission is net of the refund.
func TestP7RestaurantRefund(t *testing.T) {
	needDB(t)
	o := applyPack(t, "restaurant")
	reg, sess := o.packTill(t)
	usd := o.methods["Cash USD"]
	shw := o.itemByCode(t, "SHW")
	chk := o.itemByCode(t, "ING-CHK")
	k := o.c.ok("POST", o.p("/rest/checks"), map[string]any{"register_id": reg, "kind": "takeaway", "name": "W"}, 201)
	check := str(obj(k.body["check"])["id"])
	o.c.ok("POST", o.p("/rest/checks/"+check+"/lines"), map[string]any{"send": true, "lines": []any{map[string]any{"item_id": shw, "quantity": "2"}}}, 200)
	o.c.ok("PUT", o.p("/rest/settings"), map[string]any{"service_charge_pct": "10"}, 200)
	paid := o.c.ok("POST", o.p("/rest/checks/"+check+"/pay"), map[string]any{"session_id": sess, "service_charge": true, "tip": "3",
		"tenders": []any{map[string]any{"method_id": usd, "amount": "100"}}, "change_method_id": usd}, 200)
	po := str(obj(obj(paid.body["pos_order"])["order"])["id"])
	mid := ownerQuery(t, `SELECT sum(quantity)::text FROM stock WHERE item_id = $1`, chk)
	o.c.ok("POST", o.p("/pos/orders/"+po+"/refund"), map[string]any{"session_id": sess, "method_id": usd}, 201)
	if after := ownerQuery(t, `SELECT sum(quantity)::text FROM stock WHERE item_id = $1`, chk); after != mid {
		t.Fatalf("chicken %s after the refund, want %s (cooked food is not restocked)", after, mid)
	}
	com := o.c.ok("GET", o.p("/van/commission?from=2000-01-01&to=2100-01-01"), nil, 200)
	for _, row := range com.list {
		m := obj(row)
		if n := str(m["net"]); n != "" && n != "0" && !strings.HasPrefix(n, "0.00") {
			t.Fatalf("commission net %s after a full refund, want 0: %s", n, com.raw)
		}
		if c := str(m["collected"]); c != "" && c != "0" && !strings.HasPrefix(c, "0.00") {
			t.Fatalf("collected %s after a full refund, want 0", c)
		}
	}
	o.stockLedgerEqual(t)
	o.trialBalanced(t)
}

// M1 (security): phone numbers match on the full national number, not the last 7 digits.
func TestP7PhoneKey(t *testing.T) {
	needDB(t)
	cases := [][2]string{{"+961 3 123 456", "03123456"}, {"00961 71 123456", "71-123456"}, {"961 1 234567", "01/234567"}}
	for _, c := range cases {
		if v := ownerQuery(t, `SELECT (phone_key($1) = phone_key($2))::text`, c[0], c[1]); v != "true" {
			t.Fatalf("%s and %s should be the same number", c[0], c[1])
		}
	}
	if v := ownerQuery(t, `SELECT (phone_key('71 123456') = phone_key('81 123456'))::text`); v != "false" {
		t.Fatal("71 123456 and 81 123456 must not match")
	}
	if !reservedSlug("Paxve-Shop") || !reservedSlug("samsung") || reservedSlug("al-amal-store") {
		t.Fatal("reserved slugs")
	}
	if plainName("Ali <a href=x>http://evil.example</a>", 30) == "" || strings.ContainsAny(plainName("Ali http://x.y/z", 30), ":/.") {
		t.Fatal("plainName keeps letters only")
	}
}
