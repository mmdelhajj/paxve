package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Business packs (Odoo-style): choosing "Supermarket" or "Clothing" (at sign-up or later) switches on the right apps,
// sets the business type and the retail defaults (loyalty program, scale prefixes) and can load demo data through the
// normal handlers (same validation and accounting as real input).

type retailPack struct {
	Key          string   `json:"key"`
	BusinessType string   `json:"business_type"`
	Apps         []string `json:"apps"`
}

var retailPacks = []retailPack{
	{Key: "supermarket", BusinessType: "supermarket", Apps: []string{"gen", "acc", "col", "msg", "stk", "pur", "pos", "hr", "crm"}},
	{Key: "clothing", BusinessType: "clothing", Apps: []string{"gen", "acc", "col", "msg", "stk", "pur", "pos", "hr", "crm"}},
	{Key: "electronics", BusinessType: "electronics", Apps: []string{"gen", "acc", "col", "msg", "stk", "pur", "pos", "hr", "crm", "svc"}},
	{Key: "furniture", BusinessType: "furniture", Apps: []string{"gen", "acc", "col", "msg", "stk", "pur", "pos", "hr", "crm", "svc"}},
	{Key: "wholesale", BusinessType: "wholesale", Apps: []string{"gen", "acc", "col", "msg", "stk", "pur", "pos", "hr", "crm"}},
	{Key: "restaurant", BusinessType: "restaurant", Apps: []string{"gen", "acc", "col", "msg", "stk", "pur", "pos", "hr"}},
	{Key: "pharmacy", BusinessType: "pharmacy", Apps: []string{"gen", "acc", "col", "msg", "stk", "pur", "pos", "hr", "crm"}},
	{Key: "factory", BusinessType: "factory", Apps: []string{"gen", "acc", "col", "msg", "stk", "pur", "mfg", "hr", "crm"}},
}

// packDefaults: the settings each business pack turns on (idempotent: applying a pack again changes nothing more).
func packDefaults(ctx context.Context, tx pgx.Tx, cid, key string) error {
	exec := func(q string, args ...any) error {
		_, err := tx.Exec(ctx, q, append([]any{cid}, args...)...)
		return err
	}
	switch key {
	case "supermarket", "clothing", "electronics", "pharmacy":
		if err := exec(`INSERT INTO loyalty_programs (company_id, is_active, earn_per, point_value, min_redeem, tiers)
			VALUES ($1, true, 1, 0.01, 100, '[{"name": "Silver", "min_points": 0, "multiplier": 1}, {"name": "Gold", "min_points": 2000, "multiplier": 1.5}]')
			ON CONFLICT (company_id) DO NOTHING`); err != nil {
			return err
		}
	}
	if key == "furniture" || key == "wholesale" { // the main warehouse comes first (a van / showroom is never the default)
		if _, err := defaultWarehouse(ctx, tx, cid); err != nil {
			return err
		}
	}
	lang := companyLang(ctx, tx, cid) // names in the company's language (audit P7-F7)
	L := func(en, ar, fr string) string { return pickLang(lang, en, ar, fr) }
	switch key {
	case "pharmacy": // FEFO with expired lots blocked, alerts 90 days before expiry
		return exec(`UPDATE companies SET block_expired = true WHERE id = $1`)
	case "furniture": // 30 % deposit and a showroom warehouse
		if err := exec(`UPDATE companies SET deposit_pct = CASE WHEN deposit_pct = 0 THEN 30 ELSE deposit_pct END WHERE id = $1`); err != nil {
			return err
		}
		return exec(`INSERT INTO warehouses (company_id, name, is_showroom)
			SELECT $1, $2, true WHERE NOT EXISTS (SELECT 1 FROM warehouses WHERE company_id = $1 AND is_showroom)
			ON CONFLICT (tenant_id, company_id, name) DO UPDATE SET is_showroom = true`, L("Showroom", "صالة العرض", "Salle d'exposition"))
	case "wholesale": // sell on account with the credit limit enforced, 30 days terms, one van
		if err := exec(`UPDATE companies SET credit_policy = 'block', default_due_days = CASE WHEN coalesce(default_due_days, 0) = 0 THEN 30 ELSE default_due_days END
			WHERE id = $1`); err != nil {
			return err
		}
		return exec(`INSERT INTO warehouses (company_id, name, is_van)
			SELECT $1, $2, true WHERE NOT EXISTS (SELECT 1 FROM warehouses WHERE company_id = $1 AND is_van)
			ON CONFLICT (tenant_id, company_id, name) DO UPDATE SET is_van = true`, L("Van 1", "السيارة 1", "Camionnette 1"))
	case "restaurant": // tables and the usual modifiers
		if err := exec(`INSERT INTO rest_tables (company_id, name, area, seats, x, y)
			SELECT $1, 'T' || n, CASE WHEN n <= 6 THEN $2 ELSE $3 END, CASE WHEN n % 3 = 0 THEN 6 ELSE 4 END, ((n - 1) % 4) * 25, ((n - 1) / 4) * 30
			FROM generate_series(1, 10) n WHERE NOT EXISTS (SELECT 1 FROM rest_tables WHERE company_id = $1)`,
			L("Hall", "الصالة", "Salle"), L("Terrace", "التراس", "Terrasse")); err != nil {
			return err
		}
		return exec(`INSERT INTO rest_modifiers (company_id, name, name_i18n, grp, price, sort)
			SELECT $1, m.name, jsonb_build_object('ar', m.ar), m.grp, m.price, m.sort FROM (VALUES
			  ('Extra cheese', 'جبنة إضافية', 'Extras', 1.0, 1), ('Extra sauce', 'صلصة إضافية', 'Extras', 0.5, 2),
			  ('No onion', 'بدون بصل', 'Without', 0, 3), ('No garlic', 'بدون ثوم', 'Without', 0, 4),
			  ('Spicy', 'حار', 'Taste', 0, 5), ('Large', 'كبير', 'Size', 1.5, 6)) AS m(name, ar, grp, price, sort)
			WHERE NOT EXISTS (SELECT 1 FROM rest_modifiers WHERE company_id = $1)`)
	case "factory": // the usual work centers
		return exec(`INSERT INTO mfg_workcenters (company_id, code, name, cost_per_hour)
			SELECT $1, w.code, w.name, w.cost FROM (VALUES ('CUT', $2, 12.0), ('ASM', $3, 10.0), ('PNT', $4, 9.0)) AS w(code, name, cost)
			WHERE NOT EXISTS (SELECT 1 FROM mfg_workcenters WHERE company_id = $1)`,
			L("Cutting", "القص", "Découpe"), L("Assembly", "التجميع", "Assemblage"), L("Painting", "الدهان", "Peinture"))
	}
	return nil
}

func (s *Server) listPacks(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var pack, bt string
		var apps []string
		if err := tx.QueryRow(ctx, `SELECT pack, business_type, apps FROM companies WHERE id = $1`, r.PathValue("cid")).Scan(&pack, &bt, &apps); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		return map[string]any{"packs": retailPacks, "current": pack, "business_type": bt, "apps": apps}, nil
	})
}

// POST /retail/packs/{key} {demo: bool}
func (s *Server) applyPack(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Demo     bool     `json:"demo"`
		Remove   []string `json:"remove"`   // apps the pack does not need that the user agreed to switch off
		Template string   `json:"template"` // chart template when the company has no chart yet (default: the organisation's)
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, key := r.PathValue("cid"), r.PathValue("key")
	i := slices.IndexFunc(retailPacks, func(p retailPack) bool { return p.Key == key })
	if i < 0 || !isUUID(cid) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	pk := retailPacks[i]
	p := principal(r)
	ctx := r.Context()
	var skipped []string
	var oldApps []string
	var oldBT, oldPack string
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT apps, business_type, pack FROM companies WHERE id = $1`, cid).Scan(&oldApps, &oldBT, &oldPack); err != nil {
			return err
		}
		// a company without a chart (added before the fix) gets one first: the pack's settings and demo need accounts
		if err := ensureChart(ctx, tx, cid, in.Template, ""); err != nil {
			return err
		}
		target := appsForBusiness(pk.BusinessType)
		for _, a := range target {
			if !slices.Contains(oldApps, a) && !planAllows(p, a) {
				skipped = append(skipped, a)
			}
		}
		apps := newApps(p, oldApps, target, in.Remove)
		if _, err := tx.Exec(ctx, `UPDATE companies SET apps = $2, business_type = $3, pack = $4 WHERE id = $1`, cid, apps, pk.BusinessType, pk.Key); err != nil {
			return err
		}
		return packDefaults(ctx, tx, cid, pk.Key)
	})
	forgetApps(cid)
	if err == pgx.ErrNoRows {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	demo := map[string]any{}
	if in.Demo {
		if demo, err = s.packDemo(r, cid, pk.Key); err != nil {
			// all or nothing for the company's setup (audit P7-F2): put its apps and business type back, say why
			_ = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `UPDATE companies SET apps = $2, business_type = $3, pack = $4 WHERE id = $1`, cid, oldApps, oldBT, oldPack)
				return e
			})
			forgetApps(cid)
			code := "server_error"
			if m := demoErrRe.FindStringSubmatch(err.Error()); m != nil {
				code = m[1]
			}
			slog.Warn("pack demo failed", "pack", pk.Key, "err", err)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "pack_demo_failed", "detail": code})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pack": pk, "skipped_apps": skipped, "demo": demo})
}

var demoErrRe = regexp.MustCompile(`"error":\s*"([a-z_]+)"`)

type idOnly struct {
	ID string `json:"id"`
}

func (s *Server) packDemo(r *http.Request, cid, key string) (map[string]any, error) {
	var err error
	step := func(h http.HandlerFunc, path map[string]string, body any) json.RawMessage {
		if err != nil {
			return nil
		}
		if path == nil {
			path = map[string]string{}
		}
		path["cid"] = cid
		var out json.RawMessage
		out, err = callInternal(r, h, path, body)
		return out
	}
	id := func(raw json.RawMessage) string {
		var x idOnly
		_ = json.Unmarshal(raw, &x)
		return x.ID
	}
	ctx := r.Context()
	var base, lang string
	var methods []string
	_ = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT base_currency::text FROM companies WHERE id = $1`, cid).Scan(&base); err != nil {
			return err
		}
		lang = companyLang(ctx, tx, cid)
		rows, err := tx.Query(ctx, `SELECT id::text FROM payment_methods WHERE company_id = $1 AND is_active AND kind = 'cash' ORDER BY sort, name LIMIT 3`, cid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				return err
			}
			methods = append(methods, m)
		}
		return rows.Err()
	})
	sql := func(q string, args ...any) {
		if err != nil {
			return
		}
		err = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, q, args...)
			return e
		})
	}
	// demo names in the company's language (audit P7-F7): Arabic companies get Arabic items, categories, supplier, till
	named := func(en, ar string) (string, map[string]string) {
		if lang == "ar" && ar != "" {
			return ar, map[string]string{"en": en, "ar": ar}
		}
		return en, map[string]string{"ar": ar}
	}
	sup := id(step(s.saveSupplier, nil, map[string]any{"name": pickLang(lang, "Demo wholesaler", "تاجر جملة (تجريبي)", "Grossiste (démo)"), "currency": base}))
	type it struct {
		code, name, ar, cat, unit, price, cost, qty, brand string
		plu                                                int
		weight                                             bool
		cq, cu                                             string
	}
	out := map[string]any{}
	stock := []map[string]any{}
	addItems := func(cats map[string]string, list []it) map[string]string {
		ids := map[string]string{}
		for _, x := range list {
			nm, i18n := named(x.name, x.ar)
			body := map[string]any{"code": x.code, "name": nm, "name_i18n": i18n, "kind": "stock", "unit": x.unit,
				"sale_price": x.price, "sale_currency": base, "category_id": cats[x.cat], "barcode": ""}
			iid := id(step(s.saveItem, nil, body))
			if iid == "" {
				continue
			}
			ids[x.code] = iid
			sql(`UPDATE items SET plu = $2, sold_by_weight = $3, brand = $4, content_qty = nullif($5, '')::numeric, content_unit = $6 WHERE id = $1`,
				iid, map[bool]any{true: x.plu, false: nil}[x.plu > 0], x.weight, x.brand, x.cq, x.cu)
			stock = append(stock, map[string]any{"item_id": iid, "quantity": x.qty, "unit_price": x.cost})
		}
		return ids
	}
	cats := func(names [][3]string) map[string]string {
		m := map[string]string{}
		for _, n := range names {
			nm, i18n := named(n[0], n[1])
			m[n[0]] = id(step(s.saveCategory, nil, map[string]any{"name": nm, "name_i18n": i18n, "color": n[2]}))
		}
		return m
	}
	promo := func(body map[string]any) {
		if en, ok := body["name"].(string); ok {
			if m, ok := body["name_i18n"].(map[string]string); ok {
				body["name"], body["name_i18n"] = named(en, m["ar"])
			}
		}
		step(s.savePromotion, nil, body)
	}
	switch key {
	case "supermarket":
		c := cats([][3]string{{"Fruits & vegetables", "خضار وفواكه", "green"}, {"Dairy", "ألبان وأجبان", "blue"}, {"Bakery", "مخبوزات", "orange"},
			{"Drinks", "مشروبات", "cyan"}, {"Cleaning", "تنظيف", "grape"}})
		ids := addItems(c, []it{
			{"BAN", "Bananas", "موز", "Fruits & vegetables", "kg", "1.20", "0.70", "80", "", 1, true, "", ""},
			{"TOM", "Tomatoes", "بندورة", "Fruits & vegetables", "kg", "1.50", "0.90", "60", "", 2, true, "", ""},
			{"CHS", "Akkawi cheese", "جبنة عكاوي", "Dairy", "kg", "9.00", "6.00", "15", "", 3, true, "", ""},
			{"MLK", "Fresh milk 1 L", "حليب طازج 1 ل", "Dairy", "pcs", "1.40", "0.95", "48", "Taanayel", 0, false, "1", "L"},
			{"LBN", "Labneh 500 g", "لبنة 500 غ", "Dairy", "pcs", "2.75", "1.80", "30", "Taanayel", 0, false, "500", "g"},
			{"BRD", "Arabic bread", "خبز عربي", "Bakery", "pcs", "0.90", "0.60", "100", "", 0, false, "", ""},
			{"WTR", "Water 1.5 L", "مياه 1.5 ل", "Drinks", "pcs", "0.50", "0.25", "120", "Sohat", 0, false, "1.5", "L"},
			{"JUC", "Orange juice 1 L", "عصير برتقال 1 ل", "Drinks", "pcs", "2.20", "1.40", "40", "", 0, false, "1", "L"},
			{"SOP", "Dish soap 750 ml", "سائل جلي 750 مل", "Cleaning", "pcs", "2.50", "1.50", "36", "", 0, false, "750", "ml"},
		})
		promo(map[string]any{"name": "Drinks −10%", "name_i18n": map[string]string{"ar": "المشروبات −10%"}, "kind": "discount", "applies_to": "categories",
			"category_ids": []string{c["Drinks"]}, "value_type": "pct", "value": "10"})
		if ids["WTR"] != "" {
			promo(map[string]any{"name": "Water: buy 2 get 1", "name_i18n": map[string]string{"ar": "مياه: اشترِ 2 واحصل على 1"}, "kind": "bxgy",
				"applies_to": "items", "item_ids": []string{ids["WTR"]}, "buy_qty": "2", "get_qty": "1", "value": "100", "priority": 10})
		}
		promo(map[string]any{"name": "Bakery happy hour −20%", "name_i18n": map[string]string{"ar": "ساعة المخبوزات −20%"}, "kind": "discount",
			"applies_to": "categories", "category_ids": []string{c["Bakery"]}, "value_type": "pct", "value": "20", "time_from": "19:00", "time_to": "21:00"})
		promo(map[string]any{"name": "5% off over 50", "name_i18n": map[string]string{"ar": "خصم 5% فوق 50"}, "kind": "basket", "value_type": "pct",
			"value": "5", "min_amount": "50", "currency": base})
		out["items"] = len(ids)
	case "clothing":
		c := cats([][3]string{{"Men", "رجالي", "blue"}, {"Women", "نسائي", "pink"}, {"Summer", "صيفي", "yellow"}, {"Accessories", "إكسسوارات", "gray"}})
		tn, ti := named("T-shirt", "تي شيرت")
		tpl := id(step(s.saveItem, nil, map[string]any{"code": "TSH", "name": tn, "name_i18n": ti, "kind": "stock",
			"sale_price": "12", "sale_currency": base, "category_id": c["Summer"]}))
		jn, ji := named("Jeans", "جينز")
		jean := id(step(s.saveItem, nil, map[string]any{"code": "JNS", "name": jn, "name_i18n": ji, "kind": "stock",
			"sale_price": "35", "sale_currency": base, "category_id": c["Men"]}))
		variants := 0
		size, colour := "Size", "Colour"
		col := func(v ...string) []string { return v }
		white, black, navy, blue := "White", "Black", "Navy", "Blue"
		if lang == "ar" {
			size, colour, white, black, navy, blue = "المقاس", "اللون", "أبيض", "أسود", "كحلي", "أزرق"
		}
		for _, t := range []struct {
			id    string
			attrs []itemAttr
			extra map[string]string
		}{
			{tpl, []itemAttr{{size, []string{"S", "M", "L", "XL"}}, {colour, col(white, black, navy)}}, map[string]string{size + "=XL": "2"}},
			{jean, []itemAttr{{size, []string{"30", "32", "34", "36"}}, {colour, col(blue, black)}}, map[string]string{}},
		} {
			if t.id == "" {
				continue
			}
			step(s.putVariantSetup, map[string]string{"id": t.id}, map[string]any{"attributes": t.attrs, "attr_extras": t.extra})
			var g struct {
				Created  int `json:"created"`
				Template struct {
					Variants []struct {
						ID string `json:"id"`
					} `json:"variants"`
				} `json:"template"`
			}
			_ = json.Unmarshal(step(s.generateVariants, map[string]string{"id": t.id}, map[string]any{}), &g)
			variants += g.Created
			for i, v := range g.Template.Variants {
				stock = append(stock, map[string]any{"item_id": v.ID, "quantity": fmt.Sprint(3 + i%4), "unit_price": map[bool]string{true: "5", false: "15"}[t.id == tpl]})
			}
		}
		ids := addItems(c, []it{{"SCK", "Socks", "جوارب", "Accessories", "pcs", "3", "1", "60", "", 0, false, "", ""},
			{"BLT", "Leather belt", "حزام جلد", "Accessories", "pcs", "15", "7", "20", "", 0, false, "", ""}})
		promo(map[string]any{"name": "Summer sale −20%", "name_i18n": map[string]string{"ar": "تخفيضات الصيف −20%"}, "kind": "discount",
			"applies_to": "categories", "category_ids": []string{c["Summer"]}, "value_type": "pct", "value": "20",
			"date_from": time.Now().Format("2006-01-02"), "date_to": time.Now().AddDate(0, 2, 0).Format("2006-01-02")})
		if ids["SCK"] != "" {
			promo(map[string]any{"name": "Socks: 3 for the price of 2", "name_i18n": map[string]string{"ar": "جوارب: 3 بسعر 2"}, "kind": "bxgy",
				"applies_to": "items", "item_ids": []string{ids["SCK"]}, "buy_qty": "2", "get_qty": "1", "value": "100"})
		}
		promo(map[string]any{"name": "10% off over 100", "name_i18n": map[string]string{"ar": "خصم 10% فوق 100"}, "kind": "basket", "value_type": "pct",
			"value": "10", "min_amount": "100", "currency": base})
		out["variants"] = variants
		out["items"] = len(ids) + 2
	default:
		vout, verr := s.verticalDemo(r, cid, key, base, step, id, cats, func(c map[string]string, list []demoItem) map[string]string {
			conv := make([]it, len(list))
			for i, x := range list {
				conv[i] = it{code: x.code, name: x.name, ar: x.ar, cat: x.cat, unit: x.unit, price: x.price, cost: x.cost, qty: x.qty}
			}
			return addItems(c, conv)
		}, sql, &stock)
		if verr != nil && err == nil {
			err = verr
		}
		for k, v := range vout {
			out[k] = v
		}
	}
	// stock lines with nothing to bring in (made to order, recipes, trade-in items) are dropped
	kept := stock[:0]
	for _, l := range stock {
		if q, _ := l["quantity"].(string); q != "" && q != "0" {
			kept = append(kept, l)
		}
	}
	stock = kept
	// opening stock through a supplier bill, then a till
	if len(stock) > 0 && sup != "" {
		step(s.createBill, nil, map[string]any{"supplier_id": sup, "supplier_ref": "DEMO-1", "currency": base, "lines": stock})
	}
	if len(methods) > 0 && key != "factory" {
		var n int
		_ = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM pos_registers WHERE company_id = $1`, cid).Scan(&n)
		})
		if n == 0 {
			step(s.saveRegister, nil, map[string]any{"name": pickLang(lang, "Till 1", "الصندوق 1", "Caisse 1"), "currency": base, "method_ids": methods, "cashier_quick_item": true})
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}
