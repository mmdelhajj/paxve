package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Phase 4: online store (catalog, variants, cart with coupon and delivery zone, order → sales order → delivery →
// invoice, tracking link, staff confirm / cancel, honeypot, stock and minimum checks), marketing (lists, opt-outs,
// audience, campaign with throttle, tracked link, opt-out link, delivery receipts), recruitment (careers page with CV,
// interview, offer, hire → employee), appraisals (cycle, self + manager review, close, report) and e-sign (quote signed
// in order with draw / type, document hash check, certificate PDF, auto-confirm; decline; upload; expiry). Ends with a
// balanced trial balance.

func testSignature(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 220, 70))
	for x := 10; x < 210; x++ {
		y := 35 + (x%40-20)*(x%7)/6
		for d := -2; d <= 2; d++ {
			img.Set(x, y+d, color.RGBA{10, 20, 120, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func tokenOf(link string) string {
	i := strings.LastIndex(link, "/")
	return link[i+1:]
}

func TestGrowthFlow(t *testing.T) {
	needDB(t)
	ctx := context.Background()
	o := signup(t, "lebanon")
	me := str(o.c.ok("GET", "/me", nil, 200).body["id"])
	o.c.ok("POST", o.p("/message-providers"), map[string]any{"kind": "log", "name": "log", "is_default": true}, 201)
	anon := newClient(t)
	jobs, signer := newClient(t), newClient(t) // other visitors (the public pages are limited per address)
	anon.hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	// ---------- catalog ----------
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Gear Co", "currency": "USD"}, 201).body["id"])
	cat := str(o.c.ok("POST", o.p("/pos/categories"), map[string]any{"name": "Network", "color": "blue"}, 201).body["id"])
	router := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "RTR", "name": "Router", "kind": "stock", "sale_price": "20", "sale_currency": "USD",
		"category_id": cat}, 201).body["id"])
	cable := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "CBL", "name": "Cable", "kind": "stock", "sale_price": "5", "sale_currency": "USD"}, 201).body["id"])
	o.c.ok("POST", o.p("/items"), map[string]any{"code": "INS", "name": "Installation", "kind": "service", "sale_price": "15", "sale_currency": "USD"}, 201)
	shirt := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "TSH", "name": "Shirt", "kind": "stock", "sale_price": "12", "sale_currency": "USD"}, 201).body["id"])
	o.c.ok("PUT", o.p("/items/"+shirt+"/variant-setup"), map[string]any{"attributes": []any{map[string]any{"name": "Size", "values": []any{"S", "M"}}}}, 200)
	o.c.ok("POST", o.p("/items/"+shirt+"/variants/generate"), map[string]any{}, 200)
	var variants []string
	for _, v := range o.c.ok("GET", o.p("/items/"+shirt+"/variants"), nil, 200).body["variants"].([]any) {
		variants = append(variants, str(obj(v)["id"]))
	}
	if len(variants) != 2 {
		t.Fatalf("variants: %v", variants)
	}
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD", "lines": []any{
		map[string]any{"item_id": router, "quantity": "10", "unit_price": "12"}, map[string]any{"item_id": cable, "quantity": "50", "unit_price": "1"},
		map[string]any{"item_id": variants[0], "quantity": "5", "unit_price": "4"}}}, 201)

	// ---------- shop settings ----------
	if r := o.c.do("PUT", o.p("/shop/settings"), map[string]any{"enabled": true}); r.status != 400 || !strings.Contains(r.raw, "slug_required") {
		t.Fatalf("shop without an address: %d %s", r.status, r.raw)
	}
	slug := "shop-" + randHex(4)
	o.c.ok("PUT", o.p("/shop/settings"), map[string]any{"enabled": true, "slug": slug, "title": "Gear shop", "payments": []any{"cod", "pickup", "whish"},
		"featured": []any{router}, "careers_enabled": true}, 200)
	other := signup(t, "generic")
	if r := other.c.do("PUT", other.p("/shop/settings"), map[string]any{"slug": slug}); r.status != 409 {
		t.Fatalf("slug of another company: %d %s", r.status, r.raw)
	}
	beirut := str(o.c.ok("POST", o.p("/shop/zones"), map[string]any{"name": "Beirut", "fee": "3", "free_from": "100", "eta": "same day"}, 201).body["id"])
	o.c.ok("POST", o.p("/shop/zones"), map[string]any{"name": "Mount Lebanon", "fee": "5"}, 201)
	coupon := str(o.c.ok("POST", o.p("/retail/promotions"), map[string]any{"name": "Welcome", "kind": "basket", "value_type": "amount", "value": "5",
		"coupon_code": "welcome5", "stackable": true, "currency": "USD"}, 201).body["id"])

	// ---------- public catalog ----------
	home := anon.ok("GET", "/shop/"+slug, nil, 200)
	if len(home.body["zones"].([]any)) != 2 || len(home.body["featured"].([]any)) != 1 || len(home.body["payments"].([]any)) != 3 {
		t.Fatalf("shop home: %s", home.raw)
	}
	cat1 := anon.ok("GET", "/shop/"+slug+"/items", nil, 200)
	names := map[string]map[string]any{}
	for _, it := range cat1.body["items"].([]any) {
		names[str(obj(it)["name"])] = obj(it)
	}
	if len(names) != 4 || names["Shirt"]["is_template"] != true || names["Installation"]["in_stock"] != true || names["Router"]["in_stock"] != true {
		t.Fatalf("catalog (variants grouped under the shirt): %s", cat1.raw)
	}
	eqDec(t, "router price", names["Router"]["price"], "20")
	if r := anon.ok("GET", "/shop/"+slug+"/items?q=cab", nil, 200); len(r.body["items"].([]any)) != 1 {
		t.Fatalf("search: %s", r.raw)
	}
	if r := anon.ok("GET", "/shop/"+slug+"/items?category="+cat, nil, 200); len(r.body["items"].([]any)) != 1 {
		t.Fatalf("category: %s", r.raw)
	}
	sh := anon.ok("GET", "/shop/"+slug+"/items/"+shirt, nil, 200)
	vs := obj(sh.body["item"])["variants"].([]any)
	if len(vs) != 2 || obj(vs[0])["in_stock"] == obj(vs[1])["in_stock"] {
		t.Fatalf("shirt variants (one in stock): %s", sh.raw)
	}
	if r := anon.do("GET", "/shop/"+slug+"/items/"+variants[0], nil); r.status != 404 {
		t.Fatalf("a variant alone is not a product page: %d", r.status)
	}

	// ---------- cart ----------
	cart := map[string]any{"lines": []any{map[string]any{"item_id": router, "quantity": "2"}, map[string]any{"item_id": cable, "quantity": "3"}},
		"zone_id": beirut, "coupon": "WELCOME5"}
	pc := anon.ok("POST", "/shop/"+slug+"/cart", cart, 200)
	if pc.body["coupon_valid"] != true || len(pc.body["problems"].([]any)) != 0 {
		t.Fatalf("cart: %s", pc.raw)
	}
	eqDec(t, "cart subtotal (55 - 5)", pc.body["subtotal"], "50")
	eqDec(t, "cart fee", pc.body["fee"], "3")
	eqDec(t, "cart total", pc.body["total"], "53")
	big := anon.ok("POST", "/shop/"+slug+"/cart", map[string]any{"lines": []any{map[string]any{"item_id": router, "quantity": "11"}}}, 200)
	if p := big.body["problems"].([]any); len(p) != 1 || str(obj(p[0])["code"]) != "out_of_stock" {
		t.Fatalf("out of stock: %s", big.raw)
	}
	order := map[string]any{"name": "Lina Saad", "phone": "03 123456", "address": "Hamra street", "payment": "cod",
		"lines": cart["lines"], "zone_id": beirut, "coupon": "NOPE"}
	if r := anon.do("POST", "/shop/"+slug+"/orders", order); r.status != 400 || !strings.Contains(r.raw, "invalid_coupon") {
		t.Fatalf("wrong coupon: %d %s", r.status, r.raw)
	}
	order["coupon"] = "WELCOME5"
	order["payment"] = "omt"
	if r := anon.do("POST", "/shop/"+slug+"/orders", order); r.status != 400 {
		t.Fatalf("payment the shop does not take: %d %s", r.status, r.raw)
	}
	order["payment"] = "cod"
	bot := map[string]any{}
	for k, v := range order {
		bot[k] = v
	}
	bot["website"] = "http://spam"
	anon.ok("POST", "/shop/"+slug+"/orders", bot, 200)
	placed := anon.ok("POST", "/shop/"+slug+"/orders", order, 200)
	if str(placed.body["status"]) != "confirmed" {
		t.Fatalf("auto-confirmed: %s", placed.raw)
	}
	eqDec(t, "order total", placed.body["total"], "53")
	tok := str(placed.body["token"])
	list := o.c.ok("GET", o.p("/shop/orders"), nil, 200).list
	if len(list) != 1 {
		t.Fatalf("the bot order is not stored: %d", len(list))
	}
	so := str(obj(list[0])["sales_order_id"])
	if str(obj(list[0])["state"]) != "preparing" || str(obj(list[0])["order_status"]) != "confirmed" {
		t.Fatalf("online order: %v", list[0])
	}
	if src := ownerQuery(t, `SELECT source FROM sales_orders WHERE id = $1`, so); src != "online" {
		t.Fatalf("sales order source: %s", src)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM promotion_uses WHERE source = 'shop' AND source_id = $1`, so); n != "1" {
		t.Fatalf("coupon use recorded: %s", n)
	}
	tr := anon.ok("GET", "/shop-orders/"+tok, nil, 200)
	if str(obj(tr.body["order"])["state"]) != "preparing" || len(tr.body["lines"].([]any)) != 3 {
		t.Fatalf("tracking: %s", tr.raw)
	}
	// the customer is found by phone the next time
	order2 := map[string]any{"name": "Lina", "phone": "+961 3 123 456", "address": "Hamra", "payment": "cod", "zone_id": beirut,
		"lines": []any{map[string]any{"item_id": cable, "quantity": "1"}}}
	anon.ok("POST", "/shop/"+slug+"/orders", order2, 200)
	list = o.c.ok("GET", o.p("/shop/orders"), nil, 200).list
	if len(list) != 2 || str(obj(list[0])["customer_id"]) != str(obj(list[1])["customer_id"]) {
		t.Fatalf("same customer: %v", list)
	}
	// deliver and invoice the first order through the normal flow
	o.c.ok("POST", o.p("/sales/orders/"+so+"/deliver"), map[string]any{}, 201)
	o.c.ok("POST", o.p("/sales/orders/"+so+"/invoice"), map[string]any{}, 201)
	if r := anon.ok("GET", "/shop-orders/"+tok, nil, 200); str(obj(r.body["order"])["state"]) != "delivered" {
		t.Fatalf("delivered: %s", r.raw)
	}
	// orders waiting for staff, cancel, minimum, closed shop
	o.c.ok("PUT", o.p("/shop/settings"), map[string]any{"auto_confirm": false}, 200)
	w1 := anon.ok("POST", "/shop/"+slug+"/orders", map[string]any{"name": "Karim", "phone": "71222333", "payment": "pickup", "delivery": "pickup",
		"lines": []any{map[string]any{"item_id": variants[0], "quantity": "1"}}}, 200)
	if str(w1.body["status"]) != "new" {
		t.Fatalf("waiting for staff: %s", w1.raw)
	}
	var waiting, waiting2 string
	for _, x := range o.c.ok("GET", o.p("/shop/orders?state=received"), nil, 200).list {
		waiting = str(obj(x)["id"])
	}
	c1 := o.c.ok("POST", o.p("/shop/orders/"+waiting+"/confirm"), map[string]any{}, 200)
	if str(c1.body["status"]) != "confirmed" || str(c1.body["order_status"]) != "confirmed" {
		t.Fatalf("staff confirm: %s", c1.raw)
	}
	anon.ok("POST", "/shop/"+slug+"/orders", map[string]any{"name": "Karim", "phone": "71222333", "payment": "pickup", "delivery": "pickup",
		"lines": []any{map[string]any{"item_id": cable, "quantity": "1"}}}, 200)
	for _, x := range o.c.ok("GET", o.p("/shop/orders?state=received"), nil, 200).list {
		waiting2 = str(obj(x)["id"])
	}
	cn := o.c.ok("POST", o.p("/shop/orders/"+waiting2+"/cancel"), map[string]any{}, 200)
	if str(cn.body["state"]) != "cancelled" || str(cn.body["order_status"]) != "cancelled" {
		t.Fatalf("cancel: %s", cn.raw)
	}
	o.c.ok("PUT", o.p("/shop/settings"), map[string]any{"min_order": "1000"}, 200)
	if r := anon.do("POST", "/shop/"+slug+"/orders", order2); r.status != 400 || !strings.Contains(r.raw, "min_order") {
		t.Fatalf("minimum order: %d %s", r.status, r.raw)
	}
	o.c.ok("PUT", o.p("/shop/settings"), map[string]any{"min_order": "0", "auto_confirm": true}, 200)

	// ---------- marketing ----------
	area := str(o.c.ok("POST", o.p("/areas"), map[string]any{"name": "North"}, 201).body["id"])
	rima := str(o.c.ok("POST", o.p("/customers"), map[string]any{"name": "Rima", "phone": "70123456", "area_id": area}, 201).body["id"])
	o.c.ok("POST", o.p("/customers"), map[string]any{"name": "Sami", "phone": "71111111", "email": "sami@example.com"}, 201)
	o.c.ok("POST", o.p("/customers"), map[string]any{"name": "Nada", "email": "nada@example.com"}, 201)
	vip := str(o.c.ok("POST", o.p("/mkt/lists"), map[string]any{"name": "VIP"}, 201).body["id"])
	add := o.c.ok("POST", o.p("/mkt/lists/"+vip+"/members"), map[string]any{"customer_ids": []any{rima}, "contacts": []any{map[string]any{"name": "Walk-in", "phone": "76 555 444"}}}, 200)
	eqDec(t, "members added", add.body["added"], "2")
	add = o.c.ok("POST", o.p("/mkt/lists/"+vip+"/members"), map[string]any{"customer_ids": []any{rima}, "contacts": []any{map[string]any{"name": "Walk-in", "phone": "76 555 444"}}}, 200)
	eqDec(t, "duplicates ignored", add.body["added"], "0")
	o.c.ok("POST", o.p("/mkt/optouts"), map[string]any{"phone": "71 111 111", "reason": "asked by phone"}, 201)
	aud := o.c.ok("POST", o.p("/mkt/audience"), map[string]any{"channel": "whatsapp", "audience": map[string]any{"all_customers": true}, "list_id": vip}, 200)
	// Lina + Karim (shop), Rima, Sami (opted out), Nada (no phone), Walk-in
	eqDec(t, "audience total", aud.body["total"], "6")
	eqDec(t, "audience reach", aud.body["reach"], "4")
	if sk := obj(aud.body["skipped"]); str(sk["opted_out"]) != "1" || str(sk["no_phone"]) != "1" {
		t.Fatalf("skipped: %s", aud.raw)
	}
	if r := o.c.ok("POST", o.p("/mkt/audience"), map[string]any{"channel": "whatsapp", "audience": map[string]any{"area_ids": []any{area}}}, 200); str(r.body["total"]) != "1" {
		t.Fatalf("area filter: %s", r.raw)
	}
	kp := o.c.ok("POST", o.p("/mkt/campaigns"), map[string]any{"name": "Eid offer", "channel": "whatsapp", "audience": map[string]any{"all_customers": true},
		"list_id": vip, "body": "Hi {name}, use {coupon}: {link}", "link_url": "https://example.com/eid", "promotion_id": coupon, "throttle_per_min": 2}, 201)
	camp := str(kp.body["id"])
	if r := o.c.ok("POST", o.p("/mkt/campaigns/"+camp+"/test"), map[string]any{"to": "70999888"}, 200); !strings.Contains(str(r.body["body"]), "WELCOME5") ||
		!strings.Contains(str(r.body["body"]), "/#/optout/") {
		t.Fatalf("test message: %s", r.raw)
	}
	sent := o.c.ok("POST", o.p("/mkt/campaigns/"+camp+"/send"), map[string]any{}, 200)
	if str(sent.body["status"]) != "sent" || str(sent.body["recipients"]) != "4" || str(sent.body["skipped"]) != "2" {
		t.Fatalf("campaign sent: %s", sent.raw)
	}
	if later := ownerQuery(t, `SELECT count(*)::text FROM mkt_recipients r JOIN messages m ON m.id = r.message_id WHERE r.campaign_id = $1
		AND m.next_attempt_at > now() + interval '30 seconds'`, camp); later != "2" {
		t.Fatalf("throttled (2 per minute): %s later", later)
	}
	if r := o.c.do("PATCH", o.p("/mkt/campaigns/"+camp), map[string]any{"name": "x"}); r.status != 409 {
		t.Fatalf("edit a sent campaign: %d", r.status)
	}
	// the worker sends the first ones; a WhatsApp receipt marks one delivered
	if _, err := itAPI.sendBatch(ctx); err != nil {
		t.Fatal(err)
	}
	pmid := ownerQuery(t, `SELECT m.provider_message_id FROM mkt_recipients r JOIN messages m ON m.id = r.message_id WHERE r.campaign_id = $1 AND m.status = 'sent' LIMIT 1`, camp)
	itok := str(o.c.ok("POST", o.p("/sla/inbound/rotate"), map[string]any{}, 200).body["verify_token"])
	anon.ok("POST", "/inbound/"+itok+"/whatsapp", map[string]any{"entry": []any{map[string]any{"changes": []any{map[string]any{"value": map[string]any{
		"statuses": []any{map[string]any{"id": pmid, "status": "delivered"}}}}}}}}, 200)
	rtok := ownerQuery(t, `SELECT token FROM mkt_recipients WHERE campaign_id = $1 AND status = 'queued' ORDER BY created_at LIMIT 1`, camp)
	if r := anon.do("GET", "/r/"+rtok, nil); r.status != http.StatusFound {
		t.Fatalf("tracked link: %d %s", r.status, r.raw)
	}
	if r := anon.ok("GET", "/optout/"+rtok, nil, 200); r.body["done"] != false {
		t.Fatalf("opt-out page: %s", r.raw)
	}
	anon.ok("POST", "/optout/"+rtok, map[string]any{"reason": "too many"}, 200)
	st := obj(o.c.ok("GET", o.p("/mkt/campaigns/"+camp), nil, 200).body["campaign"])["stats"]
	if s := obj(st); str(s["clicked"]) != "1" || str(s["opted_out"]) != "1" || str(s["delivered"]) != "1" || str(s["skipped"]) != "2" {
		t.Fatalf("campaign stats: %v", st)
	}
	// opted-out people are left out of the next campaign
	aud2 := o.c.ok("POST", o.p("/mkt/audience"), map[string]any{"channel": "whatsapp", "audience": map[string]any{"all_customers": true}, "list_id": vip}, 200)
	eqDec(t, "reach after opt-out", aud2.body["reach"], "3")
	// a scheduled campaign is sent by the worker
	k2 := str(o.c.ok("POST", o.p("/mkt/campaigns"), map[string]any{"name": "Later", "channel": "sms", "list_id": vip, "body": "Hello {name}",
		"scheduled_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}, 201).body["id"])
	o.c.ok("PATCH", o.p("/mkt/campaigns/"+k2), map[string]any{"scheduled_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, 200)
	if r := o.c.ok("POST", o.p("/mkt/campaigns/"+k2+"/send"), map[string]any{}, 200); str(r.body["status"]) != "scheduled" {
		t.Fatalf("scheduled: %s", r.raw)
	}
	if _, err := itOwnerPool.Exec(ctx, `UPDATE mkt_campaigns SET scheduled_at = now() - interval '1 minute' WHERE id = $1`, k2); err != nil {
		t.Fatal(err)
	}
	if _, err := itAPI.RunGrowthJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if r := o.c.ok("GET", o.p("/mkt/campaigns/"+k2), nil, 200); str(obj(r.body["campaign"])["status"]) != "sent" {
		t.Fatalf("worker sent the scheduled campaign: %s", r.raw)
	}

	// ---------- recruitment ----------
	job := str(o.c.ok("POST", o.p("/hr/job-positions"), map[string]any{"title": "Sales rep", "department": "Sales", "is_published": true, "vacancies": 1}, 201).body["id"])
	o.c.ok("POST", o.p("/hr/job-positions"), map[string]any{"title": "Hidden"}, 201)
	car := jobs.ok("GET", "/careers/"+slug, nil, 200)
	if len(car.body["jobs"].([]any)) != 1 {
		t.Fatalf("careers page shows published jobs only: %s", car.raw)
	}
	cv := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 curriculum vitae of Omar"))
	app := map[string]any{"name": "Omar Khalil", "email": "omar@example.com", "phone": "70111222", "cover_letter": "I sell well", "cv": map[string]any{"filename": "cv.pdf", "data": cv}}
	jobs.ok("POST", "/careers/"+slug+"/jobs/"+job+"/apply", app, 200)
	if r := jobs.do("POST", "/careers/"+slug+"/jobs/"+job+"/apply", app); r.status != 409 {
		t.Fatalf("applied twice: %d %s", r.status, r.raw)
	}
	app["website"] = "x"
	app["email"] = "bot@example.com"
	jobs.ok("POST", "/careers/"+slug+"/jobs/"+job+"/apply", app, 200)
	apps := o.c.ok("GET", o.p("/hr/applicants?job_id="+job), nil, 200).list
	if len(apps) != 1 || str(obj(apps[0])["files"]) != "1" || str(obj(apps[0])["source"]) != "careers" {
		t.Fatalf("applicants: %v", apps)
	}
	omar := str(obj(apps[0])["id"])
	iv := o.c.ok("POST", o.p("/hr/interviews"), map[string]any{"applicant_id": omar, "scheduled_at": "2026-11-25T10:00:00Z", "interviewer_id": me, "kind": "onsite"}, 201)
	if str(obj(iv.body["applicant"])["stage"]) != "interview" {
		t.Fatalf("interview moves the stage: %s", iv.raw)
	}
	ivID := str(obj(iv.body["interviews"].([]any)[0])["id"])
	o.c.ok("PATCH", o.p("/hr/interviews/"+ivID), map[string]any{"status": "done", "rating": 4, "feedback": "Good"}, 200)
	o.c.ok("POST", o.p("/hr/applicants/"+omar+"/offer"), map[string]any{"salary": "800", "currency": "USD", "start": "2026-12-01"}, 200)
	hired := o.c.ok("POST", o.p("/hr/applicants/"+omar+"/hire"), map[string]any{}, 200)
	ha := obj(hired.body["applicant"])
	emp := str(ha["hired_employee_id"])
	if str(ha["stage"]) != "hired" || emp == "" {
		t.Fatalf("hire: %s", hired.raw)
	}
	if n := ownerQuery(t, `SELECT salary::text || ' ' || currency || ' ' || hire_date::text FROM employees WHERE id = $1`, emp); n != "800.0000 USD 2026-12-01" {
		t.Fatalf("employee from the offer: %s", n)
	}
	if r := o.c.ok("GET", o.p("/attachments?type=employee&id="+emp), nil, 200); len(r.list) != 1 {
		t.Fatalf("CV copied to the employee: %s", r.raw)
	}
	if r := o.c.do("PATCH", o.p("/hr/applicants/"+omar), map[string]any{"stage": "new"}); r.status != 409 {
		t.Fatalf("hired applicant moved: %d", r.status)
	}
	for _, j := range o.c.ok("GET", o.p("/hr/job-positions"), nil, 200).list {
		if str(obj(j)["id"]) == job && (str(obj(j)["status"]) != "closed" || obj(j)["is_published"] != false) {
			t.Fatalf("filled position closes: %v", j)
		}
	}
	if r := o.c.ok("GET", o.p("/hr/recruitment-report"), nil, 200); !strings.Contains(r.raw, `"hired":1`) {
		t.Fatalf("recruitment report: %s", r.raw)
	}

	// ---------- appraisals ----------
	o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Owner Emp", "currency": "USD", "salary": "1000", "user_id": me}, 201)
	cy := str(o.c.ok("POST", o.p("/hr/appraisal-cycles"), map[string]any{"name": "2026 H2", "period_from": "2026-07-01", "period_to": "2026-12-31"}, 201).body["id"])
	op := o.c.ok("POST", o.p("/hr/appraisal-cycles/"+cy+"/open"), map[string]any{"goals": []any{map[string]any{"title": "Sales target", "weight": 60},
		map[string]any{"title": "Customer care", "weight": 40}}}, 200)
	eqDec(t, "appraisals opened", op.body["appraisals"], "2")
	mine := o.c.ok("GET", o.p("/self/appraisals"), nil, 200).list
	if len(mine) != 1 {
		t.Fatalf("self appraisals: %v", mine)
	}
	ap := str(obj(mine[0])["id"])
	sr := o.c.ok("PATCH", o.p("/self/appraisals/"+ap), map[string]any{"goals": []any{map[string]any{"title": "Sales target", "self": 4},
		map[string]any{"title": "Customer care", "self": 5}}, "self_comment": "Good year", "submit": true}, 200)
	eqDec(t, "self score", sr.body["self_score"], "4.4")
	mr := o.c.ok("PATCH", o.p("/hr/appraisals/"+ap), map[string]any{"goals": []any{map[string]any{"title": "Sales target", "weight": 60, "manager": 3},
		map[string]any{"title": "Customer care", "weight": 40, "manager": 4}}, "manager_comment": "Solid", "submit": true}, 200)
	eqDec(t, "manager score", mr.body["manager_score"], "3.4")
	eqDec(t, "self score kept", mr.body["self_score"], "4.4")
	if str(mr.body["status"]) != "reviewed" {
		t.Fatalf("reviewed: %s", mr.raw)
	}
	o.c.ok("POST", o.p("/hr/appraisal-cycles/"+cy+"/close"), map[string]any{}, 200)
	if r := o.c.do("PATCH", o.p("/self/appraisals/"+ap), map[string]any{"self_comment": "late"}); r.status != 409 {
		t.Fatalf("closed cycle: %d", r.status)
	}
	rep := o.c.ok("GET", o.p("/hr/appraisal-report?cycle_id="+cy), nil, 200)
	sum := obj(rep.body["summary"])
	if str(sum["meets"]) != "1" || str(sum["pending"]) != "1" {
		t.Fatalf("appraisal report: %s", rep.raw)
	}
	eqDec(t, "average", sum["average"], "3.4")

	// ---------- e-sign: a quotation signed in order, then confirmed ----------
	q := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": rima, "currency": "USD",
		"lines": []any{map[string]any{"item_id": router, "quantity": "1", "unit_price": "20"}}}, 201)
	qid := str(obj(q.body["order"])["id"])
	qvalid := str(obj(q.body["order"])["valid_until"])
	sreq := o.c.ok("POST", o.p("/esign/requests"), map[string]any{"doc_type": "quote", "sales_order_id": qid, "auto_confirm": true, "send": true,
		"signers": []any{map[string]any{"name": "Rima", "phone": "70123456", "customer_id": rima}, map[string]any{"name": "Manager", "email": "boss@example.com"}}}, 201)
	reqID := str(obj(sreq.body["request"])["id"])
	sg := sreq.body["signers"].([]any)
	t1, t2 := tokenOf(str(obj(sg[0])["link"])), tokenOf(str(obj(sg[1])["link"]))
	if str(obj(sreq.body["request"])["status"]) != "sent" || str(obj(sg[0])["status"]) != "pending" || str(obj(sg[1])["status"]) != "waiting" {
		t.Fatalf("sent in order: %s", sreq.raw)
	}
	v1 := signer.ok("GET", "/sign/"+t1, nil, 200)
	if obj(v1.body["me"])["can_sign"] != true || !strings.Contains(str(obj(v1.body["request"])["body"]), "Quotation #") {
		t.Fatalf("signer 1 page: %s", v1.raw)
	}
	if r := signer.ok("GET", "/sign/"+t2, nil, 200); obj(r.body["me"])["can_sign"] != false {
		t.Fatalf("signer 2 waits: %s", r.raw)
	}
	sig := testSignature(t)
	if r := signer.do("POST", "/sign/"+t2, map[string]any{"method": "draw", "signature": sig, "agree": true}); r.status != 409 {
		t.Fatalf("out of order: %d %s", r.status, r.raw)
	}
	// the quote changes after sending: the signature is refused until it is back as sent
	o.c.ok("PUT", o.p("/sales/orders/"+qid), map[string]any{"customer_id": rima, "currency": "USD", "valid_until": qvalid,
		"lines": []any{map[string]any{"item_id": router, "quantity": "1", "unit_price": "18"}}}, 200)
	if r := signer.do("POST", "/sign/"+t1, map[string]any{"method": "draw", "signature": sig, "agree": true}); r.status != 409 || !strings.Contains(r.raw, "document_changed") {
		t.Fatalf("changed document: %d %s", r.status, r.raw)
	}
	o.c.ok("PUT", o.p("/sales/orders/"+qid), map[string]any{"customer_id": rima, "currency": "USD", "valid_until": qvalid,
		"lines": []any{map[string]any{"item_id": router, "quantity": "1", "unit_price": "20"}}}, 200)
	if r := signer.do("POST", "/sign/"+t1, map[string]any{"method": "draw", "signature": "data:image/png;base64,AAAA", "agree": true}); r.status != 400 {
		t.Fatalf("not a picture: %d", r.status)
	}
	if r := signer.ok("POST", "/sign/"+t1, map[string]any{"method": "draw", "signature": sig, "agree": true}, 200); r.body["completed"] != false {
		t.Fatalf("first signature: %s", r.raw)
	}
	signer.ok("GET", "/sign/"+t2, nil, 200) // the second signer opens the link now
	if r := signer.ok("POST", "/sign/"+t2, map[string]any{"method": "type", "typed_name": "The Manager", "signature": sig, "agree": true}, 200); r.body["completed"] != true {
		t.Fatalf("last signature: %s", r.raw)
	}
	done := o.c.ok("GET", o.p("/esign/requests/"+reqID), nil, 200)
	dr := obj(done.body["request"])
	if str(dr["status"]) != "completed" || dr["certificate_id"] == nil {
		t.Fatalf("completed: %s", done.raw)
	}
	evs := map[string]int{}
	for _, e := range done.body["events"].([]any) {
		evs[str(obj(e)["event"])]++
	}
	if evs["created"] != 1 || evs["sent"] != 1 || evs["viewed"] != 2 || evs["signed"] != 2 || evs["completed"] != 1 || evs["confirmed"] != 1 {
		t.Fatalf("audit trail: %v", evs)
	}
	qo := obj(o.c.ok("GET", o.p("/sales/orders/"+qid), nil, 200).body["order"])
	if str(qo["status"]) != "confirmed" || !strings.Contains(str(qo["accepted_name"]), "Rima") {
		t.Fatalf("quote confirmed by the signature: %v", qo)
	}
	if r := o.c.do("GET", o.p("/esign/requests/"+reqID+"/certificate"), nil); r.status != 200 || !strings.HasPrefix(r.raw, "%PDF-1.4") {
		t.Fatalf("certificate: %d %.40s", r.status, r.raw)
	}
	if r := signer.do("GET", "/sign/"+t1+"/certificate", nil); r.status != 200 || !strings.HasPrefix(r.raw, "%PDF") {
		t.Fatalf("signer's copy: %d", r.status)
	}
	if r := o.c.ok("GET", o.p("/attachments?type=sales_order&id="+qid), nil, 200); len(r.list) != 1 {
		t.Fatalf("signed copy on the quotation: %s", r.raw)
	}
	if r := signer.do("POST", "/sign/"+t1, map[string]any{"method": "draw", "signature": sig, "agree": true}); r.status != 409 {
		t.Fatalf("signed twice: %d", r.status)
	}

	// a contract declined; an uploaded file; a request that expires
	ct := o.c.ok("POST", o.p("/esign/requests"), map[string]any{"doc_type": "contract", "title": "Service contract", "body": "The provider installs the network.",
		"send": true, "signers": []any{map[string]any{"name": "Sami", "phone": "71111111"}}}, 201)
	ctTok := tokenOf(str(obj(ct.body["signers"].([]any)[0])["link"]))
	signer.ok("POST", "/sign/"+ctTok+"/decline", map[string]any{"reason": "price too high"}, 200)
	if r := o.c.ok("GET", o.p("/esign/requests/"+str(obj(ct.body["request"])["id"])), nil, 200); str(obj(r.body["request"])["status"]) != "declined" {
		t.Fatalf("declined: %s", r.raw)
	}
	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n% lease agreement\n"))
	up := o.c.ok("POST", o.p("/esign/requests"), map[string]any{"doc_type": "upload", "file": map[string]any{"filename": "lease.pdf", "data": pdf},
		"signers": []any{map[string]any{"name": "Nada", "email": "nada@example.com"}}}, 201)
	upID := str(obj(up.body["request"])["id"])
	if str(obj(up.body["request"])["status"]) != "draft" || str(obj(up.body["request"])["title"]) != "lease.pdf" {
		t.Fatalf("upload draft: %s", up.raw)
	}
	o.c.ok("POST", o.p("/esign/requests/"+upID+"/send"), map[string]any{}, 200)
	upTok := tokenOf(str(obj(up.body["signers"].([]any)[0])["link"]))
	if r := signer.do("GET", "/sign/"+upTok+"/file", nil); r.status != 200 || !strings.HasPrefix(r.raw, "%PDF-1.4") {
		t.Fatalf("file to sign: %d", r.status)
	}
	rm := o.c.ok("POST", o.p("/esign/requests/"+upID+"/remind"), map[string]any{}, 200)
	if !strings.Contains(rm.raw, `"reminded"`) {
		t.Fatalf("reminder: %s", rm.raw)
	}
	ex := o.c.ok("POST", o.p("/esign/requests"), map[string]any{"doc_type": "contract", "title": "Old offer", "body": "x", "expires_on": "2026-01-01", "send": true,
		"signers": []any{map[string]any{"name": "Rima", "phone": "70123456"}}}, 201)
	if _, err := itAPI.RunGrowthJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if r := o.c.ok("GET", o.p("/esign/requests/"+str(obj(ex.body["request"])["id"])), nil, 200); str(obj(r.body["request"])["status"]) != "expired" {
		t.Fatalf("expired: %s", r.raw)
	}

	// the shop closes
	o.c.ok("PUT", o.p("/shop/settings"), map[string]any{"enabled": false}, 200)
	if r := anon.do("GET", "/shop/"+slug, nil); r.status != 404 {
		t.Fatalf("closed shop: %d", r.status)
	}
	if r := anon.do("GET", "/shop-orders/"+tok, nil); r.status != 200 {
		t.Fatalf("tracking still works: %d", r.status)
	}
	o.trialBalanced(t)
}
