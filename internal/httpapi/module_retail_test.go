package httpapi

import (
	"strings"
	"testing"
)

// Retail core part 1: variants, promotions, loyalty, gift cards, scale / labels, POS extras, packs.

func (o *org) reloadAccounts(t *testing.T) {
	t.Helper()
	for _, a := range o.c.ok("GET", o.p("/accounts"), nil, 200).list {
		o.accounts[str(obj(a)["code"])] = str(obj(a)["id"])
	}
}

func fl(t *testing.T, v any) float64 { t.Helper(); return dec(t, v).InexactFloat64() }

type retailFix struct {
	o                 *org
	reg, sess, usd    string
	water, milk, soap string
}

func retailSetup(t *testing.T) *retailFix {
	t.Helper()
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	f := &retailFix{o: o, usd: o.methods["Cash USD"]}
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Wholesale", "currency": "USD"}, 201).body["id"])
	cat := str(o.c.ok("POST", o.p("/pos/categories"), map[string]any{"name": "Drinks", "color": "blue"}, 201).body["id"])
	vat := ""
	for _, tr := range o.c.ok("GET", o.p("/tax-rates"), nil, 200).list {
		vat = str(obj(tr)["id"])
	}
	mk := func(code, name, price string, withVat bool) string {
		body := map[string]any{"code": code, "name": name, "kind": "stock", "sale_price": price, "sale_currency": "USD", "category_id": cat}
		if withVat && vat != "" {
			body["sale_tax_rate_id"] = vat
		}
		return str(o.c.ok("POST", o.p("/items"), body, 201).body["id"])
	}
	f.water, f.milk, f.soap = mk("WTR", "Water", "1", false), mk("MLK", "Milk", "2", false), mk("SOAP", "Soap", "10", true)
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-01-02", "currency": "USD",
		"lines": []any{map[string]any{"item_id": f.water, "quantity": "100", "unit_price": "0.4"}, map[string]any{"item_id": f.milk, "quantity": "50", "unit_price": "1"},
			map[string]any{"item_id": f.soap, "quantity": "20", "unit_price": "5"}}}, 201)
	f.reg = str(o.c.ok("POST", o.p("/pos/registers"), map[string]any{"name": "Till", "currency": "USD", "method_ids": []any{f.usd, o.methods["Cash LBP"]}}, 201).body["id"])
	f.sess = str(obj(o.c.ok("POST", o.p("/pos/sessions"), map[string]any{"register_id": f.reg}, 201).body["session"])["id"])
	return f
}

func (f *retailFix) sale(t *testing.T, body map[string]any, want int) resp {
	t.Helper()
	body["session_id"] = f.sess
	return f.o.c.ok("POST", f.o.p("/pos/orders"), body, want)
}

func TestRetailVariants(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	tpl := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "TSH", "name": "T-shirt", "kind": "stock", "sale_price": "12", "sale_currency": "USD"}, 201).body["id"])
	o.c.ok("PUT", o.p("/items/"+tpl+"/variant-setup"), map[string]any{"attributes": []any{
		map[string]any{"name": "Size", "values": []any{"S", "M", "XL"}}, map[string]any{"name": "Colour", "values": []any{"Red", "Blue"}}},
		"attr_extras": map[string]any{"Size=XL": "2"}}, 200)
	g := o.c.ok("POST", o.p("/items/"+tpl+"/variants/generate"), map[string]any{}, 200)
	if n := g.body["created"]; n != float64(6) {
		t.Fatalf("created %v: %s", n, g.raw)
	}
	again := o.c.ok("POST", o.p("/items/"+tpl+"/variants/generate"), map[string]any{}, 200)
	if again.body["created"] != float64(0) {
		t.Fatalf("second generate: %s", again.raw)
	}
	vars := obj(g.body["template"])["variants"].([]any)
	var xl string
	seen := map[string]bool{}
	for _, v := range vars {
		m := obj(v)
		bc := str(m["barcode"])
		if len(bc) != 13 || !strings.HasPrefix(bc, "29") || seen[bc] {
			t.Fatalf("variant barcode %q", bc)
		}
		seen[bc] = true
		if strings.Contains(str(m["name"]), "XL") && strings.Contains(str(m["name"]), "Red") {
			xl = str(m["id"])
			eqDec(t, "XL price = 12 + 2", m["sale_price"], "14")
		}
	}
	if xl == "" {
		t.Fatal("no XL / Red variant")
	}
	// a variant is a normal item: bill it in, sell it at the till
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Textile", "currency": "USD"}, 201).body["id"])
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "currency": "USD", "lines": []any{map[string]any{"item_id": xl, "quantity": "4", "unit_price": "5"}}}, 201)
	v := o.c.ok("GET", o.p("/items/"+tpl+"/variants"), nil, 200)
	total := 0.0
	for _, x := range v.body["variants"].([]any) {
		total += fl(t, obj(x)["quantity"])
	}
	if total != 4 {
		t.Fatalf("variant stock %v", total)
	}
	// the template itself cannot get stock once it has variants (it is not purchasable / not on the till)
	if r := o.c.do("PUT", o.p("/items/"+xl+"/variant-setup"), map[string]any{"attributes": []any{map[string]any{"name": "Fit", "values": []any{"Slim"}}}}); r.status != 400 {
		t.Fatalf("variant as template: %d %s", r.status, r.raw)
	}
	o.c.ok("PUT", o.p("/items/"+tpl+"/variants"), map[string]any{"variants": []any{map[string]any{"id": xl, "sale_price": "15"}}}, 200)
	if n := len(o.c.ok("GET", o.p("/items/templates"), nil, 200).list); n != 1 {
		t.Fatalf("templates: %d", n)
	}
}

func TestRetailPromotionsAtTheTill(t *testing.T) {
	needDB(t)
	f := retailSetup(t)
	o := f.o
	// buy 2 get 1 water, 10% off milk, 5 off a basket over 30
	o.c.ok("POST", o.p("/retail/promotions"), map[string]any{"name": "Water 2+1", "kind": "bxgy", "applies_to": "items", "item_ids": []any{f.water},
		"buy_qty": "2", "get_qty": "1"}, 201)
	o.c.ok("POST", o.p("/retail/promotions"), map[string]any{"name": "Milk -10%", "kind": "discount", "applies_to": "items", "item_ids": []any{f.milk},
		"value_type": "pct", "value": "10"}, 201)
	o.c.ok("POST", o.p("/retail/promotions"), map[string]any{"name": "5 off over 30", "kind": "basket", "value_type": "amount", "value": "5",
		"min_amount": "30", "currency": "USD"}, 201)
	o.c.ok("POST", o.p("/retail/promotions"), map[string]any{"name": "Coupon SAVE2", "kind": "basket", "value_type": "amount", "value": "2",
		"coupon_code": "save2", "stackable": true, "currency": "USD"}, 201)
	// preview: 3 waters (1 free), 5 milks (10% of 10 = 1), 2 soaps (20, VAT) → line promos 2; basket 33−2 = 31 ≥ 30 → 5
	ev := o.c.ok("POST", o.p("/retail/promotions/evaluate"), map[string]any{"currency": "USD", "register_id": f.reg, "lines": []any{
		map[string]any{"item_id": f.water, "quantity": "3", "amount": "3"}, map[string]any{"item_id": f.milk, "quantity": "5", "amount": "10"},
		map[string]any{"item_id": f.soap, "quantity": "2", "amount": "20"}}}, 200)
	eqDec(t, "preview total discount", ev.body["total"], "7")
	// a wrong coupon is reported, the right one stacks
	bad := o.c.ok("POST", o.p("/retail/promotions/evaluate"), map[string]any{"currency": "USD", "coupon": "NOPE", "lines": []any{}}, 200)
	if bad.body["coupon_valid"] != false {
		t.Fatalf("coupon: %s", bad.raw)
	}
	s := f.sale(t, map[string]any{"apply_promos": true, "coupon": "SAVE2", "lines": []any{
		map[string]any{"item_id": f.water, "quantity": "3"}, map[string]any{"item_id": f.milk, "quantity": "5"}, map[string]any{"item_id": f.soap, "quantity": "2"}},
		"tenders": []any{map[string]any{"method_id": f.usd, "amount": "50"}}, "change_method_id": f.usd}, 201)
	ord := obj(s.body["order"])
	// net 33 − 2 (lines) − 5 (basket) − 2 (coupon) = 24; soap's share of the basket discounts lowers its 11% VAT
	lines := s.body["lines"].([]any)
	net := 0.0
	tax := 0.0
	for _, l := range lines {
		net += fl(t, obj(l)["amount"])
		tax += fl(t, obj(l)["tax_amount"])
	}
	if net < 23.99 || net > 24.01 {
		t.Fatalf("net after promotions %v: %s", net, s.raw)
	}
	if tax <= 0 || tax >= 2.2 {
		t.Fatalf("VAT on the discounted soap %v", tax)
	}
	if len(ord["promos"].([]any)) != 4 {
		t.Fatalf("promos on the order: %v", ord["promos"])
	}
	// uses are counted
	for _, p := range o.c.ok("GET", o.p("/retail/promotions"), nil, 200).list {
		if fl(t, obj(p)["uses"]) != 1 {
			t.Fatalf("uses: %v", p)
		}
	}
	// without apply_promos (offline till) the catalog prices stand
	s2 := f.sale(t, map[string]any{"lines": []any{map[string]any{"item_id": f.water, "quantity": "3"}},
		"tenders": []any{map[string]any{"method_id": f.usd, "amount": "3"}}}, 201)
	eqDec(t, "no promotions offline", obj(s2.body["order"])["total"], "3")
	// happy hour outside its window does nothing; date windows, max uses
	o.c.ok("POST", o.p("/retail/promotions"), map[string]any{"name": "Old", "kind": "discount", "value": "50", "date_to": "2020-01-01"}, 201)
	ev2 := o.c.ok("POST", o.p("/retail/promotions/evaluate"), map[string]any{"currency": "USD", "lines": []any{
		map[string]any{"item_id": f.milk, "quantity": "1", "amount": "2"}}}, 200)
	eqDec(t, "expired promotion ignored", ev2.body["total"], "0.2")
	o.trialBalanced(t)
}

func TestRetailLoyaltyAndGiftCards(t *testing.T) {
	needDB(t)
	f := retailSetup(t)
	o := f.o
	o.c.ok("PUT", o.p("/retail/loyalty"), map[string]any{"is_active": true, "earn_per": "1", "point_value": "0.05", "min_redeem": "10",
		"tiers": []any{map[string]any{"name": "Gold", "min_points": "100", "multiplier": "2"}}}, 200)
	cust := str(o.c.ok("POST", o.p("/pos/customers"), map[string]any{"name": "Rami", "phone": "70123456"}, 201).body["id"])
	card := o.c.ok("PUT", o.p("/retail/loyalty/customers/"+cust), map[string]any{"card": "auto"}, 200)
	code := str(card.body["loyalty_card"])
	if len(code) != 13 {
		t.Fatalf("card %q", code)
	}
	look := o.c.ok("GET", o.p("/retail/loyalty/lookup?code="+code), nil, 200)
	if str(look.body["id"]) != cust {
		t.Fatalf("lookup: %s", look.raw)
	}
	if o.c.ok("GET", o.p("/retail/loyalty/lookup?code=70123456"), nil, 200).body["id"] != cust {
		t.Fatal("lookup by phone")
	}
	// 20 spent → 20 points (worth 1.00)
	f.sale(t, map[string]any{"customer_id": cust, "lines": []any{map[string]any{"item_id": f.milk, "quantity": "10"}},
		"tenders": []any{map[string]any{"method_id": f.usd, "amount": "20"}}}, 201)
	look = o.c.ok("GET", o.p("/retail/loyalty/lookup?code="+code), nil, 200)
	eqDec(t, "points earned", look.body["points"], "20")
	o.reloadAccounts(t)
	eqDec(t, "points liability 20 × 0.05", o.baseBalance(t, "4197"), "-1")
	// pay 2 of 4 with 20 points (= 1 USD) and cash 3: the points tender is not money in the drawer
	s := f.sale(t, map[string]any{"customer_id": cust, "redeem_points": "20", "lines": []any{map[string]any{"item_id": f.milk, "quantity": "2"}},
		"tenders": []any{map[string]any{"method_id": f.usd, "amount": "3"}}}, 201)
	ord := obj(s.body["order"])
	eqDec(t, "paid with points + cash", ord["paid"], "4")
	if len(ord["cash"].([]any)) != 1 {
		t.Fatalf("drawer movements: %v", ord["cash"])
	}
	loy := obj(ord["loyalty"])
	eqDec(t, "earned on the cash part only", loy["earned"], "3")
	eqDec(t, "balance", loy["balance"], "3")
	if r := o.c.do("POST", o.p("/pos/orders"), map[string]any{"session_id": f.sess, "customer_id": cust, "redeem_points": "500",
		"lines": []any{map[string]any{"item_id": f.milk, "quantity": "1"}}, "tenders": []any{map[string]any{"method_id": f.usd, "amount": "2"}}}); r.status != 422 {
		t.Fatalf("too many points: %d %s", r.status, r.raw)
	}
	// the gift card / points tenders cannot be used outside the till
	all := o.c.ok("GET", o.p("/payment-methods?all=1"), nil, 200).list
	var pointsMethod string
	for _, m := range all {
		if str(obj(m)["kind"]) == "loyalty" {
			pointsMethod = str(obj(m)["id"])
		}
	}
	if pointsMethod == "" || len(o.c.ok("GET", o.p("/payment-methods"), nil, 200).list) >= len(all) {
		t.Fatal("system tenders listed for the office")
	}
	if r := o.c.do("POST", o.p("/payments"), map[string]any{"customer_id": cust, "currency": "USD", "tenders": []any{map[string]any{"method_id": pointsMethod, "amount": "1"}}}); r.status != 400 {
		t.Fatalf("points tender outside the till: %d %s", r.status, r.raw)
	}

	// gift card: sold at the till (liability, no VAT), then used to pay
	g := f.sale(t, map[string]any{"gift_sold": []any{map[string]any{"code": "GIFT-1001", "amount": "25"}},
		"tenders": []any{map[string]any{"method_id": f.usd, "amount": "25"}}}, 201)
	eqDec(t, "gift card sale total", obj(g.body["order"])["total"], "25")
	o.reloadAccounts(t)
	eqDec(t, "gift card liability", o.baseBalance(t, "4196"), "-25")
	if r := o.c.do("POST", o.p("/pos/orders"), map[string]any{"session_id": f.sess, "gift_sold": []any{map[string]any{"code": "GIFT-1001", "amount": "5"}},
		"tenders": []any{map[string]any{"method_id": f.usd, "amount": "5"}}}); r.status != 409 {
		t.Fatalf("same code twice: %d", r.status)
	}
	f.sale(t, map[string]any{"gift_redeem": []any{map[string]any{"code": "gift-1001", "amount": "10"}},
		"lines": []any{map[string]any{"item_id": f.soap, "quantity": "1"}}, "tenders": []any{map[string]any{"method_id": f.usd, "amount": "1.1"}}}, 201)
	gl := o.c.ok("GET", o.p("/retail/gift-cards/lookup?code=GIFT-1001"), nil, 200)
	eqDec(t, "card balance", gl.body["balance"], "15")
	eqDec(t, "liability after use", o.baseBalance(t, "4196"), "-15")
	if r := o.c.do("POST", o.p("/pos/orders"), map[string]any{"session_id": f.sess, "gift_redeem": []any{map[string]any{"code": "GIFT-1001", "amount": "16"}},
		"lines": []any{map[string]any{"item_id": f.soap, "quantity": "2"}}, "tenders": []any{map[string]any{"method_id": f.usd, "amount": "10"}}}); r.status != 422 {
		t.Fatalf("over the balance: %d %s", r.status, r.raw)
	}
	// a free voucher (marketing expense), voided: back out of the expense
	v := o.c.ok("POST", o.p("/retail/gift-cards"), map[string]any{"amount": "5", "currency": "USD", "note": "Birthday"}, 201)
	o.c.ok("POST", o.p("/retail/gift-cards/"+str(v.body["id"])+"/void"), map[string]any{}, 200)
	eqDec(t, "liability unchanged by the voided voucher", o.baseBalance(t, "4196"), "-15")
	if len(o.c.ok("GET", o.p("/retail/gift-cards/"+str(gl.body["id"])+"/moves"), nil, 200).list) != 2 {
		t.Fatal("card moves")
	}
	o.trialBalanced(t)
}

func TestRetailScaleLabelsAndTillExtras(t *testing.T) {
	needDB(t)
	f := retailSetup(t)
	o := f.o
	o.c.ok("PUT", o.p("/items/"+f.milk+"/retail"), map[string]any{"plu": 7, "sold_by_weight": false, "brand": "Taanayel", "content_qty": "1", "content_unit": "L"}, 200)
	o.c.ok("PUT", o.p("/items/"+f.water+"/retail"), map[string]any{"plu": 8, "sold_by_weight": true}, 200)
	if r := o.c.do("PUT", o.p("/items/"+f.soap+"/retail"), map[string]any{"plu": 7}); r.status != 409 {
		t.Fatalf("PLU twice: %d", r.status)
	}
	sc := o.c.ok("PUT", o.p("/retail/scale"), map[string]any{"prefixes": []any{map[string]any{"prefix": "21", "mode": "weight", "decimals": 3}}}, 200)
	if !strings.Contains(sc.raw, `"21"`) {
		t.Fatalf("scale: %s", sc.raw)
	}
	if r := o.c.do("PUT", o.p("/retail/scale"), map[string]any{"prefixes": []any{map[string]any{"prefix": "13", "mode": "weight"}}}); r.status != 400 {
		t.Fatalf("bad prefix: %d", r.status)
	}
	plu := o.c.ok("GET", o.p("/retail/scale/plu"), nil, 200)
	if !strings.Contains(str(plu.body["csv"]), "7;MLK;Milk;2.00") {
		t.Fatalf("PLU csv: %s", plu.raw)
	}
	o.c.ok("POST", o.p("/retail/promotions"), map[string]any{"name": "Milk -25%", "kind": "discount", "applies_to": "items", "item_ids": []any{f.milk}, "value": "25"}, 201)
	lab := o.c.ok("GET", o.p("/retail/labels?ids="+f.milk+","+f.soap), nil, 200)
	for _, l := range lab.body["labels"].([]any) {
		m := obj(l)
		switch str(m["id"]) {
		case f.milk:
			eqDec(t, "label promo price", m["promo_price"], "1.5")
			eqDec(t, "unit price per L", m["unit_price"], "2")
		case f.soap:
			eqDec(t, "label price incl. VAT", m["price"], "11.1")
		}
	}
	// held baskets
	h := o.c.ok("POST", o.p("/pos/held"), map[string]any{"register_id": f.reg, "label": "Mrs Nadia", "payload": map[string]any{"lines": []any{1, 2}}}, 201)
	if len(o.c.ok("GET", o.p("/pos/held?register_id="+f.reg), nil, 200).list) != 1 {
		t.Fatal("held list")
	}
	o.c.ok("DELETE", o.p("/pos/held/"+str(h.body["id"])), nil, 200)
	// quick item: office yes; a cashier only when the till allows it
	qi := o.c.ok("POST", o.p("/pos/quick-item"), map[string]any{"register_id": f.reg, "name": "Batteries AA", "price": "3", "barcode": "6291234500011"}, 201)
	f.sale(t, map[string]any{"lines": []any{map[string]any{"item_id": str(qi.body["id"]), "quantity": "2"}},
		"tenders": []any{map[string]any{"method_id": f.usd, "amount": "6"}}}, 201)
	email := "cash" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": email, "full_name": "Cashier", "role": "cashier", "locale": "en", "password": "cashier-pass-1"}, 201)
	cs := loginAs(t, email, "cashier-pass-1")
	if r := cs.do("POST", o.p("/pos/quick-item"), map[string]any{"register_id": f.reg, "name": "X", "price": "1"}); r.status != 403 {
		t.Fatalf("cashier quick item: %d", r.status)
	}
	o.c.ok("PATCH", o.p("/pos/registers/"+f.reg), map[string]any{"cashier_quick_item": true}, 200)
	cs.ok("POST", o.p("/pos/quick-item"), map[string]any{"register_id": f.reg, "name": "Y", "price": "1"}, 201)
	// close with a denomination count
	reg := o.c.ok("GET", o.p("/pos/sessions/"+f.sess), nil, 200)
	_ = reg
	o.c.ok("POST", o.p("/pos/sessions/"+f.sess+"/close"), map[string]any{"counted": []any{map[string]any{"method_id": f.usd, "amount": "6"}},
		"denominations": map[string]any{f.usd: map[string]any{"5": 1, "1": 1}}}, 200)
	// retail report
	rep := o.c.ok("GET", o.p("/reports/retail?from=2000-01-01&to=2100-01-01"), nil, 200)
	if len(rep.body["by_item"].([]any)) == 0 || len(rep.body["by_hour"].([]any)) == 0 {
		t.Fatalf("retail report: %s", rep.raw)
	}
	o.trialBalanced(t)
}

func TestRetailExpiryAndPacks(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Dairy Co", "currency": "USD"}, 201).body["id"])
	it := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "YOG", "name": "Yogurt", "kind": "stock", "sale_price": "1", "sale_currency": "USD"}, 201).body["id"])
	o.c.ok("PUT", o.p("/items/"+it+"/stock-settings"), map[string]any{"tracking": "lot"}, 200)
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "currency": "USD", "lines": []any{map[string]any{"item_id": it, "quantity": "10", "unit_price": "0.5",
		"lots": []any{map[string]any{"lot_no": "L1", "quantity": "10", "expiry": today()}}}}}, 201)
	ex := o.c.ok("GET", o.p("/retail/expiry?days=7"), nil, 200)
	if len(ex.list) != 1 || fl(t, obj(ex.list[0])["suggested_pct"]) != 50 {
		t.Fatalf("expiry: %s", ex.raw)
	}
	o.c.ok("POST", o.p("/retail/expiry/markdown"), map[string]any{"item_id": it, "pct": "50", "days": 2}, 201)
	if obj(o.c.ok("GET", o.p("/retail/expiry?days=7"), nil, 200).list[0])["has_promo"] != true {
		t.Fatal("markdown not linked")
	}
	// supermarket pack with demo data
	p := o.c.ok("POST", o.p("/retail/packs/supermarket"), map[string]any{"demo": true}, 200)
	if fl(t, obj(p.body["demo"])["items"]) < 5 {
		t.Fatalf("pack demo: %s", p.raw)
	}
	packs := o.c.ok("GET", o.p("/retail/packs"), nil, 200)
	if str(packs.body["current"]) != "supermarket" || str(packs.body["business_type"]) != "supermarket" {
		t.Fatalf("packs: %s", packs.raw)
	}
	if len(o.c.ok("GET", o.p("/retail/promotions"), nil, 200).list) < 4 {
		t.Fatal("pack promotions")
	}
	if r := o.c.do("POST", o.p("/retail/packs/none"), map[string]any{}); r.status != 404 {
		t.Fatalf("unknown pack: %d", r.status)
	}
	// clothing pack: variants with stock
	o2 := signup(t, "generic")
	c := o2.c.ok("POST", o2.p("/retail/packs/clothing"), map[string]any{"demo": true}, 200)
	if fl(t, obj(c.body["demo"])["variants"]) != 20 {
		t.Fatalf("clothing demo: %s", c.raw)
	}
	o.trialBalanced(t)
	o2.trialBalanced(t)
}
