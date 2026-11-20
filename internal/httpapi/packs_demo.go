package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Demo data of the phase 6a business packs, created through the normal handlers (same checks and accounting as real
// input): electronics (serial-tracked phones with warranty, a trade-in item), furniture (a made-to-order sofa with
// options, a table priced per m², showroom), wholesale (cartons, wholesale / distributor price lists, customers on a
// route), restaurant (ingredients in stock, dishes as recipes, a drink for the bar), pharmacy (lots with expiry, an
// alternative brand, prescription and controlled drugs, an insurer) and factory (materials, a cabinet with its BoM and
// routing on the pack's work centers).

type demoItem struct {
	code, name, ar, cat, unit, price, cost, qty string
}

func (s *Server) verticalDemo(r *http.Request, cid, key, base string,
	step func(h http.HandlerFunc, path map[string]string, body any) json.RawMessage, id func(json.RawMessage) string,
	cats func([][3]string) map[string]string, add func(map[string]string, []demoItem) map[string]string,
	sql func(q string, args ...any), stock *[]map[string]any) (map[string]any, error) {
	out := map[string]any{}
	ctx := r.Context()
	query := func(q string, args ...any) []string {
		var vals []string
		_ = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, q, args...)
			if err != nil {
				return err
			}
			vals, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		return vals
	}
	customer := func(name, ar, phone string) string {
		_ = ar
		return id(step(s.createCustomer, nil, map[string]any{"name": name, "phone": phone}))
	}
	serialLine := func(item, cost, prefix string, n int) {
		if item == "" {
			return
		}
		lots := []map[string]any{}
		for i := 1; i <= n; i++ {
			lots = append(lots, map[string]any{"lot_no": fmt.Sprintf("%s-%s-%02d", prefix, randHex(3), i), "quantity": "1"})
		}
		*stock = append(*stock, map[string]any{"item_id": item, "quantity": fmt.Sprint(n), "unit_price": cost, "lots": lots})
	}
	switch key {
	case "electronics":
		c := cats([][3]string{{"Phones", "هواتف", "blue"}, {"Accessories", "إكسسوارات", "gray"}, {"Appliances", "أجهزة منزلية", "orange"}})
		ids := add(c, []demoItem{
			{"PH-A1", "Smartphone A1 128GB", "هاتف ذكي A1 سعة 128GB", "Phones", "pcs", "299", "230", "0"},
			{"PH-USED", "Used phone (trade-in)", "هاتف مستعمل (استبدال)", "Phones", "pcs", "120", "0", "0"},
			{"CHG", "Fast charger 25W", "شاحن سريع 25 واط", "Accessories", "pcs", "15", "6", "40"},
			{"CASE", "Phone case", "غطاء هاتف", "Accessories", "pcs", "8", "2", "60"},
			{"TV55", "Smart TV 55 inch", "تلفزيون ذكي 55 إنش", "Appliances", "pcs", "520", "410", "4"},
		})
		sql(`UPDATE items SET tracking = 'serial', warranty_months = 12 WHERE id = ANY ($1::uuid[])`, nonEmpty(ids["PH-A1"], ids["PH-USED"]))
		sql(`UPDATE items SET warranty_months = 24 WHERE id = ANY ($1::uuid[])`, nonEmpty(ids["TV55"]))
		serialLine(ids["PH-A1"], "230", "IMEI", 5)
		out["items"] = len(ids)
	case "furniture":
		c := cats([][3]string{{"Living room", "غرفة جلوس", "teal"}, {"Bedroom", "غرف نوم", "violet"}, {"Office", "مكاتب", "gray"}})
		ids := add(c, []demoItem{
			{"SOFA3", "Sofa 3 seats", "كنبة 3 مقاعد", "Living room", "pcs", "900", "0", "0"},
			{"DTBL", "Dining table (made to size)", "طاولة سفرة (حسب القياس)", "Living room", "pcs", "150", "0", "0"},
			{"BED160", "Double bed 160", "سرير مزدوج 160", "Bedroom", "pcs", "650", "380", "6"},
			{"CHAIR", "Office chair", "كرسي مكتب", "Office", "pcs", "120", "60", "15"},
		})
		opts, _ := json.Marshal([]map[string]any{
			{"name": "Fabric", "values": []map[string]string{{"label": "Linen", "extra": "0"}, {"label": "Velvet", "extra": "150"}, {"label": "Leather", "extra": "400"}}},
			{"name": "Colour", "values": []map[string]string{{"label": "Beige", "extra": "0"}, {"label": "Grey", "extra": "0"}, {"label": "Navy", "extra": "50"}}},
		})
		sql(`UPDATE items SET made_to_order = true, options = $2::jsonb WHERE id = $1`, ids["SOFA3"], string(opts))
		sql(`UPDATE items SET made_to_order = true, price_per_m2 = 250 WHERE id = $1`, ids["DTBL"])
		customer("Demo customer Rima", "ريما (زبونة تجريبية)", "70111222")
		out["items"] = len(ids)
	case "wholesale":
		c := cats([][3]string{{"Beverages", "مشروبات", "cyan"}, {"Snacks", "سناكات", "orange"}, {"Cleaning", "تنظيف", "grape"}})
		ids := add(c, []demoItem{
			{"WTR-6", "Water 1.5 L × 6", "مياه 1.5 ل × 6", "Beverages", "pcs", "2.40", "1.50", "400"},
			{"COLA-24", "Cola can × 24", "كولا علب × 24", "Beverages", "pcs", "9.00", "6.50", "200"},
			{"CHIPS-30", "Chips × 30", "شيبس × 30", "Snacks", "pcs", "7.50", "5.00", "150"},
			{"BLEACH-12", "Bleach 1 L × 12", "مبيض 1 ل × 12", "Cleaning", "pcs", "11.00", "7.80", "80"},
		})
		pl := map[string]string{}
		for _, p := range []struct{ name, pct string }{{"Wholesale", "0.92"}, {"Distributor", "0.85"}} {
			lid := id(step(s.savePriceList, nil, map[string]any{"name": p.name, "currency": base}))
			if lid == "" {
				continue
			}
			pl[p.name] = lid
			rows := []map[string]any{}
			for _, code := range []string{"WTR-6", "COLA-24", "CHIPS-30", "BLEACH-12"} {
				if ids[code] == "" {
					continue
				}
				price := map[string]string{"WTR-6": "2.40", "COLA-24": "9.00", "CHIPS-30": "7.50", "BLEACH-12": "11.00"}[code]
				pr, _ := money(price)
				f, _ := money(p.pct)
				rows = append(rows, map[string]any{"item_id": ids[code], "min_qty": "1", "price": pr.Mul(f).Round(2).String()},
					map[string]any{"item_id": ids[code], "min_qty": "10", "price": pr.Mul(f).Mul(decimal.RequireFromString("0.97")).Round(2).String()})
			}
			step(s.putPriceListItems, map[string]string{"id": lid}, map[string]any{"items": rows})
		}
		custs := []string{}
		for i, n := range [][3]string{{"Al Amal minimarket", "ميني ماركت الأمل", "70100200"}, {"Beirut corner shop", "دكان بيروت", "70100201"},
			{"Saida grocery", "سمانة صيدا", "70100202"}} {
			cu := customer(n[0], n[1], n[2])
			if cu == "" {
				continue
			}
			custs = append(custs, cu)
			list := pl["Wholesale"]
			if i == 2 {
				list = pl["Distributor"]
			}
			if list != "" {
				step(s.putCustomerPriceList, map[string]string{"id": cu}, map[string]any{"price_list_id": list})
			}
			sql(`UPDATE customers SET credit_limit = 500 WHERE id = $1`, cu)
		}
		if vans := query(`SELECT id::text FROM warehouses WHERE company_id = $1 AND is_van ORDER BY created_at LIMIT 1`, cid); len(vans) > 0 && len(custs) > 0 {
			var rt struct {
				Route idOnly `json:"route"`
			}
			_ = json.Unmarshal(step(s.vanSaveRoute, nil, map[string]any{"name": "Daily route", "warehouse_id": vans[0]}), &rt)
			rid := rt.Route.ID
			if rid != "" {
				step(s.vanRouteStops, map[string]string{"id": rid}, map[string]any{"customer_ids": custs})
			}
		}
		out["items"] = len(ids)
		out["customers"] = len(custs)
	case "restaurant":
		c := cats([][3]string{{"Ingredients", "مكونات", "gray"}, {"Mains", "أطباق رئيسية", "orange"}, {"Starters", "مقبلات", "green"}, {"Drinks", "مشروبات", "cyan"}})
		ing := add(c, []demoItem{
			{"ING-CHK", "Chicken (kg)", "دجاج (كغ)", "Ingredients", "kg", "0", "5.00", "40"},
			{"ING-BRD", "Arabic bread", "خبز عربي", "Ingredients", "pcs", "0", "0.20", "300"},
			{"ING-POT", "Potatoes (kg)", "بطاطا (كغ)", "Ingredients", "kg", "0", "0.80", "60"},
			{"ING-TOM", "Tomatoes (kg)", "بندورة (كغ)", "Ingredients", "kg", "0", "1.00", "30"},
			{"ING-GAR", "Garlic sauce (kg)", "ثومية (كغ)", "Ingredients", "kg", "0", "3.00", "10"},
			{"COLA", "Cola can", "كولا علبة", "Drinks", "pcs", "1.50", "0.50", "120"},
		})
		sql(`UPDATE items SET is_sellable = false WHERE id = ANY ($1::uuid[])`, nonEmpty(ing["ING-CHK"], ing["ING-BRD"], ing["ING-POT"], ing["ING-TOM"], ing["ING-GAR"]))
		sql(`UPDATE items SET kitchen_station = 'bar' WHERE id = $1`, ing["COLA"])
		dishes := 0
		for _, d := range []struct {
			code, name, ar, cat, price, station string
			parts                               [][2]string
		}{
			{"SHW", "Chicken shawarma plate", "صحن شاورما دجاج", "Mains", "8", "grill", [][2]string{{"ING-CHK", "0.25"}, {"ING-BRD", "1"}, {"ING-GAR", "0.05"}, {"ING-POT", "0.15"}}},
			{"FRIES", "French fries", "بطاطا مقلية", "Starters", "3", "kitchen", [][2]string{{"ING-POT", "0.3"}}},
			{"FTSH", "Fattoush", "فتوش", "Starters", "5", "kitchen", [][2]string{{"ING-TOM", "0.2"}, {"ING-BRD", "0.5"}}},
		} {
			did := id(step(s.saveItem, nil, map[string]any{"code": d.code, "name": d.name, "name_i18n": map[string]string{"ar": d.ar}, "kind": "stock",
				"sale_price": d.price, "sale_currency": base, "category_id": c[d.cat], "barcode": ""}))
			if did == "" {
				continue
			}
			comps := []map[string]string{}
			for _, p := range d.parts {
				if ing[p[0]] != "" {
					comps = append(comps, map[string]string{"component_id": ing[p[0]], "quantity": p[1]})
				}
			}
			step(s.putItemComponents, map[string]string{"id": did}, map[string]any{"components": comps})
			sql(`UPDATE items SET kitchen_station = $2 WHERE id = $1`, did, d.station)
			dishes++
		}
		out["items"] = len(ing)
		out["dishes"] = dishes
	case "pharmacy":
		c := cats([][3]string{{"Medicines", "أدوية", "red"}, {"Personal care", "عناية شخصية", "pink"}, {"Vitamins", "فيتامينات", "yellow"}})
		ids := add(c, []demoItem{
			{"PAN500", "Panadol 500 mg", "بنادول 500 ملغ", "Medicines", "pcs", "4.50", "0", "0"},
			{"FEV500", "Fevadol 500 mg", "فيفادول 500 ملغ", "Medicines", "pcs", "3.80", "0", "0"},
			{"AMOX500", "Amoxicillin 500 mg", "أموكسيسيلين 500 ملغ", "Medicines", "pcs", "7.00", "0", "0"},
			{"TRAM50", "Tramadol 50 mg", "ترامادول 50 ملغ", "Medicines", "pcs", "9.00", "0", "0"},
			{"VITC", "Vitamin C 1000 mg", "فيتامين سي 1000 ملغ", "Vitamins", "pcs", "6.00", "0", "0"},
			{"SUNB", "Sunblock SPF 50", "واقي شمس SPF 50", "Personal care", "pcs", "14.00", "8.00", "20"},
		})
		type drug struct {
			code, generic, ingredient, strength, form string
			rx, ctl                                   bool
		}
		for _, d := range []drug{
			{"PAN500", "Paracetamol", "paracetamol", "500 mg", "tablet", false, false},
			{"FEV500", "Paracetamol", "paracetamol", "500 mg", "tablet", false, false},
			{"AMOX500", "Amoxicillin", "amoxicillin", "500 mg", "capsule", true, false},
			{"TRAM50", "Tramadol", "tramadol", "50 mg", "capsule", true, true},
			{"VITC", "Ascorbic acid", "ascorbic acid", "1000 mg", "effervescent", false, false},
		} {
			sql(`UPDATE items SET tracking = 'lot', generic_name = $2, active_ingredient = $3, strength = $4, dosage_form = $5, rx_required = $6, controlled = $7
				WHERE id = $1`, ids[d.code], d.generic, d.ingredient, d.strength, d.form, d.rx, d.ctl)
		}
		lot := func(code, cost, qty, expiry string) {
			if ids[code] == "" {
				return
			}
			*stock = append(*stock, map[string]any{"item_id": ids[code], "quantity": qty, "unit_price": cost,
				"lots": []map[string]any{{"lot_no": code + "-" + randHex(2), "quantity": qty, "expiry": expiry}}})
		}
		lot("PAN500", "2.50", "50", dateIn(0, 18))
		lot("FEV500", "2.10", "40", dateIn(0, 2)) // near expiry: shows on the expiry board
		lot("AMOX500", "4.00", "30", dateIn(1, 0))
		lot("TRAM50", "5.50", "10", dateIn(1, 0))
		lot("VITC", "3.50", "25", dateIn(0, 9))
		customer("Demo Insurance Co.", "شركة تأمين تجريبية", "01999888")
		out["items"] = len(ids)
	case "factory":
		c := cats([][3]string{{"Raw materials", "مواد أولية", "gray"}, {"Finished goods", "منتجات جاهزة", "blue"}})
		ids := add(c, []demoItem{
			{"STEEL", "Steel sheet (kg)", "ألواح حديد (كغ)", "Raw materials", "kg", "0", "1.20", "500"},
			{"SCREW", "Screws (box of 100)", "براغي (علبة 100)", "Raw materials", "pcs", "0", "3.00", "40"},
			{"PAINT", "Paint (L)", "دهان (ل)", "Raw materials", "L", "0", "6.00", "60"},
			{"CAB", "Metal cabinet", "خزانة معدنية", "Finished goods", "pcs", "180", "0", "0"},
		})
		sql(`UPDATE items SET is_sellable = false WHERE id = ANY ($1::uuid[])`, nonEmpty(ids["STEEL"], ids["SCREW"], ids["PAINT"]))
		wcs := query(`SELECT id::text FROM mfg_workcenters WHERE company_id = $1 AND is_active ORDER BY code`, cid)
		routing := ""
		if len(wcs) >= 2 {
			ops := []map[string]any{{"name": "Cut the sheets", "workcenter_id": wcs[1], "minutes": "30"}, {"name": "Assemble", "workcenter_id": wcs[0], "minutes": "45"}}
			if len(wcs) >= 3 {
				ops = append(ops, map[string]any{"name": "Paint", "workcenter_id": wcs[2], "minutes": "20"})
			}
			routing = id(step(s.mfgSaveRouting, nil, map[string]any{"name": "Cabinet routing", "operations": ops}))
		}
		if ids["CAB"] != "" && ids["STEEL"] != "" {
			body := map[string]any{"item_id": ids["CAB"], "name": "Metal cabinet", "output_qty": "1", "labour_cost": "8", "overhead_cost": "4",
				"lines": []map[string]string{{"item_id": ids["STEEL"], "quantity": "18"}, {"item_id": ids["SCREW"], "quantity": "0.5"}, {"item_id": ids["PAINT"], "quantity": "1.5"}}}
			if routing != "" {
				body["routing_id"] = routing
			}
			step(s.saveBOM, nil, body)
		}
		out["items"] = len(ids)
	}
	return out, nil
}

func nonEmpty(ids ...string) []string {
	out := []string{}
	for _, v := range ids {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func dateIn(years, months int) string {
	return time.Now().In(beirut).AddDate(years, months, 0).Format("2006-01-02")
}
