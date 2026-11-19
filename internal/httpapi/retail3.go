package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
		Demo bool `json:"demo"`
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
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		var cur []string
		if err := tx.QueryRow(ctx, `SELECT apps FROM companies WHERE id = $1`, cid).Scan(&cur); err != nil {
			return err
		}
		apps := append([]string{}, cur...)
		for _, a := range pk.Apps {
			if slices.Contains(apps, a) {
				continue
			}
			if !planAllows(p, a) {
				skipped = append(skipped, a)
				continue
			}
			apps = append(apps, a)
		}
		if _, err := tx.Exec(ctx, `UPDATE companies SET apps = $2, business_type = $3, pack = $4 WHERE id = $1`, cid, apps, pk.BusinessType, pk.Key); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO loyalty_programs (company_id, is_active, earn_per, point_value, min_redeem, tiers)
			VALUES ($1, true, 1, 0.01, 100, '[{"name": "Silver", "min_points": 0, "multiplier": 1}, {"name": "Gold", "min_points": 2000, "multiplier": 1.5}]')
			ON CONFLICT (company_id) DO NOTHING`, cid)
		return err
	})
	forgetApps(cid)
	if err == pgx.ErrNoRows {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		serverErr(w, r, err)
		return
	}
	demo := map[string]any{}
	if in.Demo {
		if demo, err = s.packDemo(r, cid, pk.Key); err != nil {
			serverErr(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pack": pk, "skipped_apps": skipped, "demo": demo})
}

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
	var base string
	var methods []string
	_ = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT base_currency::text FROM companies WHERE id = $1`, cid).Scan(&base); err != nil {
			return err
		}
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
	sup := id(step(s.saveSupplier, nil, map[string]any{"name": "Demo wholesaler", "currency": base}))
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
			body := map[string]any{"code": x.code, "name": x.name, "name_i18n": map[string]string{"ar": x.ar}, "kind": "stock", "unit": x.unit,
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
			m[n[0]] = id(step(s.saveCategory, nil, map[string]any{"name": n[0], "name_i18n": map[string]string{"ar": n[1]}, "color": n[2]}))
		}
		return m
	}
	promo := func(body map[string]any) {
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
		tpl := id(step(s.saveItem, nil, map[string]any{"code": "TSH", "name": "T-shirt", "name_i18n": map[string]string{"ar": "تي شيرت"}, "kind": "stock",
			"sale_price": "12", "sale_currency": base, "category_id": c["Summer"]}))
		jean := id(step(s.saveItem, nil, map[string]any{"code": "JNS", "name": "Jeans", "name_i18n": map[string]string{"ar": "جينز"}, "kind": "stock",
			"sale_price": "35", "sale_currency": base, "category_id": c["Men"]}))
		variants := 0
		for _, t := range []struct {
			id    string
			attrs []itemAttr
			extra map[string]string
		}{
			{tpl, []itemAttr{{"Size", []string{"S", "M", "L", "XL"}}, {"Colour", []string{"White", "Black", "Navy"}}}, map[string]string{"Size=XL": "2"}},
			{jean, []itemAttr{{"Size", []string{"30", "32", "34", "36"}}, {"Colour", []string{"Blue", "Black"}}}, map[string]string{}},
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
	}
	// opening stock through a supplier bill, then a till
	if len(stock) > 0 && sup != "" {
		step(s.createBill, nil, map[string]any{"supplier_id": sup, "supplier_ref": "DEMO-1", "currency": base, "lines": stock})
	}
	if len(methods) > 0 {
		var n int
		_ = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM pos_registers WHERE company_id = $1`, cid).Scan(&n)
		})
		if n == 0 {
			step(s.saveRegister, nil, map[string]any{"name": "Till 1", "currency": base, "method_ids": methods, "cashier_quick_item": true})
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}
