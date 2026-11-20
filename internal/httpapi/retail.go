package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Retail core part 1.
//
// Variants: a "template" item carries attributes (Size: S/M/L, Colour: â€¦). Each combination is generated as a normal
// item linked to the template (template_id), with its own SKU, barcode, price (template price + attribute extras) and
// stock â€” so POS, invoices, purchase orders, price lists, stock counts and reports need no special case.
//
// Promotions: evaluated on a basket (POS live preview, POS sale, sales documents). Line promotions (discount, buy X get
// Y, quantity tiers, bundles) compete per line ("best deal") unless marked stackable; basket promotions apply on the
// discounted subtotal and are spread over the lines. The discount lowers each line's net amount, so revenue and VAT
// are booked net of the promotion (sales discounts are a reduction of revenue).

var beirut = func() *time.Location {
	if l, err := time.LoadLocation("Asia/Beirut"); err == nil {
		return l
	}
	return time.UTC
}()

// ensureRetailAccount returns the account in companies.<col>, creating it (and remembering it) when missing.
// Liabilities go next to the payables account, expenses next to the default expense account, under the same parent.
func ensureRetailAccount(ctx context.Context, tx pgx.Tx, cid, col string) (string, error) {
	type spec struct {
		typ, sibling, en, ar, fr string
		codes                    []string
	}
	specs := map[string]spec{
		"gift_liability_account_id":    {"liability", "ap_account_id", "Gift cards and vouchers", "بطاقات الهدايا والقسائم", "Cartes cadeaux et bons", []string{"4196", "2410", "2450"}},
		"loyalty_liability_account_id": {"liability", "ap_account_id", "Loyalty points to redeem", "نقاط الولاء المستحقة", "Points de fidélité à utiliser", []string{"4197", "2420", "2460"}},
		"loyalty_expense_account_id":   {"expense", "default_expense_account_id", "Loyalty and promotions", "مصاريف الولاء والعروض", "Fidélité et promotions", []string{"6238", "6950", "6960"}},
		"tips_liability_account_id":    {"liability", "ap_account_id", "Tips owed to staff", "إكراميات مستحقة للموظفين", "Pourboires dus au personnel", []string{"4198", "2430", "2470"}},
		"tradein_account_id":           {"liability", "ap_account_id", "Trade-ins clearing", "حساب وسيط للأجهزة المستبدلة", "Reprises (compte d'attente)", []string{"4199", "2440", "2480"}},
		"claims_writeoff_account_id":   {"expense", "default_expense_account_id", "Insurance claims written off", "مطالبات تأمين مشطوبة", "Créances d'assurance passées en perte", []string{"6541", "6970", "6980"}},
		"insurance_account_id":         {"asset", "ar_account_id", "Insurance claims receivable", "مطالبات التأمين المستحقة", "Créances sur assurances", []string{"4118", "1250", "1260"}},
	}
	sp, ok := specs[col]
	if !ok {
		return "", fmt.Errorf("unknown account role %s", col)
	}
	var id *string
	if err := tx.QueryRow(ctx, `SELECT `+col+`::text FROM companies WHERE id = $1`, cid).Scan(&id); err != nil {
		return "", err
	}
	if id != nil {
		return *id, nil
	}
	var parent *string
	_ = tx.QueryRow(ctx, `SELECT a.parent_id::text FROM companies c JOIN accounts a ON a.id = c.`+sp.sibling+` WHERE c.id = $1`, cid).Scan(&parent)
	code := ""
	for _, c := range sp.codes {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE company_id = $1 AND code = $2)`, cid, c).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			code = c
			break
		}
	}
	if code == "" {
		code = "R" + strings.ToUpper(randHex(3))
	}
	i18n, _ := json.Marshal(map[string]string{"en": sp.en, "ar": sp.ar, "fr": sp.fr})
	var nid string
	if err := tx.QueryRow(ctx, `INSERT INTO accounts (company_id, code, name, name_i18n, type, parent_id)
		VALUES ($1, $2, $3, $4, $5, $6::uuid) RETURNING id::text`, cid, code, sp.en, i18n, sp.typ, parent).Scan(&nid); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE companies SET `+col+` = $2 WHERE id = $1`, cid, nid); err != nil {
		return "", err
	}
	return nid, nil
}

// ensureSystemMethod: the "Gift card XXX" / "Loyalty points XXX" tender (kind gift / loyalty) whose account is the liability.
func ensureSystemMethod(ctx context.Context, tx pgx.Tx, cid, kind, cur string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM payment_methods WHERE company_id = $1 AND kind = $2 AND currency = $3 ORDER BY created_at LIMIT 1`,
		cid, kind, cur).Scan(&id)
	if err == nil || err != pgx.ErrNoRows {
		return id, err
	}
	col, name := "gift_liability_account_id", "Gift card "+cur
	switch kind {
	case "loyalty":
		col, name = "loyalty_liability_account_id", "Loyalty points "+cur
	case "tradein":
		col, name = "tradein_account_id", "Trade-in "+cur
	case "insurance":
		col, name = "insurance_account_id", "Insurance "+cur
	}
	acc, err := ensureRetailAccount(ctx, tx, cid, col)
	if err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, `INSERT INTO payment_methods (company_id, name, kind, currency, account_id, sort) VALUES ($1, $2, $3, $4, $5, 900)
		RETURNING id::text`, cid, name, kind, cur, acc).Scan(&id)
	return id, err
}

// ======================= variants =======================

type itemAttr struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// PUT /items/{id}/variant-setup {attributes, attr_extras}: turns the item into a template.
func (s *Server) putVariantSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Attributes []itemAttr        `json:"attributes"`
		Extras     map[string]string `json:"attr_extras"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Attributes) == 0 || len(in.Attributes) > 3 || len(in.Extras) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	n := 1
	for i := range in.Attributes {
		a := &in.Attributes[i]
		a.Name = strings.TrimSpace(a.Name)
		seen := map[string]bool{}
		vals := []string{}
		for _, v := range a.Values {
			v = strings.TrimSpace(v)
			if v != "" && !seen[strings.ToLower(v)] && len(v) <= 30 {
				seen[strings.ToLower(v)] = true
				vals = append(vals, v)
			}
		}
		a.Values = vals
		if a.Name == "" || len(a.Name) > 30 || len(vals) == 0 || len(vals) > 40 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		n *= len(vals)
	}
	if n > 400 {
		writeErr(w, http.StatusBadRequest, "too_many_variants")
		return
	}
	for k, v := range in.Extras {
		if _, ok := money(v); !ok || len(k) > 64 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var tpl *string
		var qty string
		err := tx.QueryRow(ctx, `SELECT it.template_id::text, coalesce((SELECT sum(quantity) FROM stock WHERE item_id = it.id), 0)::text
			FROM items it WHERE it.id = $1 AND it.company_id = $2 FOR UPDATE`, id, cid).Scan(&tpl, &qty)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if tpl != nil {
			return nil, badRequest("variant_cannot_be_template")
		}
		if q, _ := decimal.NewFromString(qty); !q.IsZero() {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "template_has_stock"}
		}
		ab, _ := json.Marshal(in.Attributes)
		if in.Extras == nil {
			in.Extras = map[string]string{}
		}
		eb, _ := json.Marshal(in.Extras)
		if _, err := tx.Exec(ctx, `UPDATE items SET is_template = true, attributes = $2, attr_extras = $3, pos_visible = false,
			is_purchasable = false WHERE id = $1`, id, ab, eb); err != nil {
			return nil, err
		}
		return loadVariants(ctx, tx, cid, id)
	})
}

func slugPart(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r > 127 && b.Len() < 6 {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		out = "X"
	}
	if len([]rune(out)) > 6 {
		out = string([]rune(out)[:6])
	}
	return out
}

// ean13 completes 12 digits with the check digit.
func ean13(d12 string) string {
	sum := 0
	for i, c := range d12 {
		n := int(c - '0')
		if i%2 == 1 {
			n *= 3
		}
		sum += n
	}
	return d12 + strconv.Itoa((10-sum%10)%10)
}

// POST /items/{id}/variants/generate: creates the missing combinations (existing variants are kept).
func (s *Server) generateVariants(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Barcodes *bool `json:"barcodes"` // default true: give each new variant an internal EAN-13
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var code, name, i18nS, kind, unit, price, attrsS, extrasS, brand, prefix string
		var saleCur, inv, exp, rev, tax, cat *string
		var isTpl bool
		err := tx.QueryRow(ctx, `SELECT it.code, it.name, it.name_i18n::text, it.kind, it.unit, it.sale_price::text, it.sale_currency::text,
			it.inventory_account_id::text, it.expense_account_id::text, it.revenue_account_id::text, it.sale_tax_rate_id::text,
			it.category_id::text, it.attributes::text, it.attr_extras::text, it.brand, it.is_template, c.variant_barcode_prefix
			FROM items it JOIN companies c ON c.id = it.company_id WHERE it.id = $1 AND it.company_id = $2 FOR UPDATE OF it`, id, cid).
			Scan(&code, &name, &i18nS, &kind, &unit, &price, &saleCur, &inv, &exp, &rev, &tax, &cat, &attrsS, &extrasS, &brand, &isTpl, &prefix)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if !isTpl {
			return nil, badRequest("not_a_template")
		}
		var attrs []itemAttr
		_ = json.Unmarshal([]byte(attrsS), &attrs)
		extras := map[string]string{}
		_ = json.Unmarshal([]byte(extrasS), &extras)
		i18n := map[string]string{}
		_ = json.Unmarshal([]byte(i18nS), &i18n)
		base, _ := decimal.NewFromString(price)
		// existing combinations
		have := map[string]bool{}
		rows, err := tx.Query(ctx, `SELECT variant_values::text FROM items WHERE template_id = $1`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			m := map[string]string{}
			_ = json.Unmarshal([]byte(v), &m)
			have[comboKey(attrs, m)] = true
		}
		rows.Close()
		combos := []map[string]string{{}}
		for _, a := range attrs {
			next := []map[string]string{}
			for _, c := range combos {
				for _, v := range a.Values {
					m := map[string]string{}
					for k, x := range c {
						m[k] = x
					}
					m[a.Name] = v
					next = append(next, m)
				}
			}
			combos = next
		}
		// next internal barcode number under the prefix
		var maxBC *string
		if err := tx.QueryRow(ctx, `SELECT max(substr(barcode, 1, 12)) FROM items WHERE company_id = $1 AND barcode ~ ('^' || $2 || '[0-9]+$') AND length(barcode) = 13`,
			cid, prefix).Scan(&maxBC); err != nil {
			return nil, err
		}
		next := int64(0)
		if maxBC != nil {
			next, _ = strconv.ParseInt(strings.TrimPrefix(*maxBC, prefix), 10, 64)
		}
		created := 0
		for _, c := range combos {
			key := comboKey(attrs, c)
			if have[key] {
				continue
			}
			label, codeSfx := []string{}, []string{}
			p := base
			for _, a := range attrs {
				label = append(label, c[a.Name])
				codeSfx = append(codeSfx, slugPart(c[a.Name]))
				if x, ok := extras[a.Name+"="+c[a.Name]]; ok {
					d, _ := decimal.NewFromString(x)
					p = p.Add(d)
				}
			}
			vName := name + " " + strings.Join(label, " / ")
			vI18n := map[string]string{}
			for l, n := range i18n {
				if strings.TrimSpace(n) != "" {
					vI18n[l] = n + " " + strings.Join(label, " / ")
				}
			}
			ib, _ := json.Marshal(vI18n)
			vb, _ := json.Marshal(c)
			vCode := code + "-" + strings.Join(codeSfx, "-")
			barcode := ""
			if in.Barcodes == nil || *in.Barcodes {
				next++
				digits := 12 - len(prefix)
				barcode = ean13(prefix + fmt.Sprintf("%0*d", digits, next))
			}
			var vid string
			for attempt := 0; ; attempt++ {
				c2 := vCode
				if attempt > 0 {
					c2 = fmt.Sprintf("%s-%d", vCode, attempt+1)
				}
				err = tx.QueryRow(ctx, `INSERT INTO items (company_id, code, name, name_i18n, kind, unit, sale_price, sale_currency,
					inventory_account_id, expense_account_id, revenue_account_id, template_id, variant_values, brand)
					VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9::uuid, $10::uuid, $11::uuid, $12, $13, $14) RETURNING id::text`,
					cid, c2, vName, ib, kind, unit, p.String(), saleCur, inv, exp, rev, id, vb, brand).Scan(&vid)
				if isUniqueViolation(err) && attempt < 5 {
					continue
				}
				break
			}
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE items SET sale_tax_rate_id = $2::uuid, category_id = $3::uuid, barcode = $4,
				is_sellable = true, is_purchasable = true, pos_visible = true WHERE id = $1`, vid, tax, cat, barcode); err != nil {
				if isUniqueViolation(err) {
					return nil, &apiError{status: http.StatusConflict, code: "barcode_taken"}
				}
				return nil, err
			}
			created++
		}
		out, err := loadVariants(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"created": created, "template": out}, nil
	})
}

func comboKey(attrs []itemAttr, m map[string]string) string {
	parts := []string{}
	for _, a := range attrs {
		parts = append(parts, strings.ToLower(m[a.Name]))
	}
	return strings.Join(parts, "|")
}

func loadVariants(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	tpl, err := queryJSONOne(ctx, tx, `SELECT it.id, it.code, it.name, it.name_i18n, it.attributes, it.attr_extras, it.is_template,
		it.sale_price::text AS sale_price, it.sale_currency, it.brand FROM items it WHERE it.id = $1 AND it.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	vars, err := queryJSON(ctx, tx, `SELECT it.id, it.code, it.name, it.barcode, it.variant_values, it.sale_price::text AS sale_price,
		it.is_active, coalesce((SELECT sum(st.quantity) FROM stock st WHERE st.item_id = it.id), 0)::text AS quantity,
		coalesce((SELECT json_object_agg(st.warehouse_id, st.quantity::text) FROM stock st WHERE st.item_id = it.id), '{}'::json) AS by_warehouse
		FROM items it WHERE it.template_id = $1 AND it.company_id = $2 ORDER BY it.code`, id, cid)
	if err != nil {
		return nil, err
	}
	whs, err := queryJSON(ctx, tx, `SELECT id, name FROM warehouses WHERE company_id = $1 AND is_active ORDER BY created_at`, cid)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"template": tpl, "variants": vars, "warehouses": whs}, nil
}

// GET /items/{id}/variants
func (s *Server) getVariants(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return loadVariants(ctx, tx, cid, id) })
}

// PUT /items/{id}/variants {variants: [{id, sale_price, barcode, is_active}]}: bulk edit of the variants of a template.
func (s *Server) putVariants(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Variants []struct {
			ID        string  `json:"id"`
			SalePrice *string `json:"sale_price"`
			Barcode   *string `json:"barcode"`
			IsActive  *bool   `json:"is_active"`
		} `json:"variants"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || len(in.Variants) > 400 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, v := range in.Variants {
		if !isUUID(v.ID) || (v.SalePrice != nil && *v.SalePrice != "" && !okMoney(*v.SalePrice)) || (v.Barcode != nil && len(*v.Barcode) > 64) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		for _, v := range in.Variants {
			var bc *string
			if v.Barcode != nil {
				b := strings.TrimSpace(*v.Barcode)
				bc = &b
			}
			tag, err := tx.Exec(ctx, `UPDATE items SET sale_price = coalesce(nullif($4, '')::numeric, sale_price), barcode = coalesce($5, barcode),
				is_active = coalesce($6, is_active) WHERE id = $1 AND template_id = $2 AND company_id = $3`,
				v.ID, id, cid, deref(v.SalePrice), bc, v.IsActive)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "barcode_taken"}
			}
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return loadVariants(ctx, tx, cid, id)
	})
}

func okMoney(s string) bool { _, ok := money(s); return ok }

// GET /items/templates: templates with their variant count (the variants screen list).
func (s *Server) listItemTemplates(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT it.id, it.code, it.name, it.name_i18n, it.attributes, it.brand, it.sale_price::text AS sale_price,
			it.sale_currency, (SELECT count(*) FROM items v WHERE v.template_id = it.id) AS variants,
			coalesce((SELECT sum(st.quantity) FROM items v JOIN stock st ON st.item_id = v.id WHERE v.template_id = it.id), 0)::text AS quantity
			FROM items it WHERE it.company_id = $1 AND it.is_template ORDER BY it.name`, r.PathValue("cid"))
	})
}

// ======================= promotions =======================

type promo struct {
	ID, Name, Kind, AppliesTo, ValueType, Coupon string
	NameI18n                                     json.RawMessage
	Items, Categories, Brands, Registers, Lists  []string
	Value, Buy, Get, MinAmount                   decimal.Decimal
	Currency                                     *string
	Tiers                                        []struct{ MinQty, Pct decimal.Decimal }
	Bundle                                       []struct {
		ItemID string
		Qty    decimal.Decimal
	}
	Stackable bool
	Priority  int
}

type promoLine struct {
	ItemID, TemplateID, CategoryID, Brand string
	Qty, Amount                           decimal.Decimal // amount = net of manual discount, before tax, till currency
}

type appliedPromo struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	NameI18n json.RawMessage `json:"name_i18n"`
	Discount decimal.Decimal `json:"discount"`
}

type promoResult struct {
	Lines       []decimal.Decimal `json:"lines"`        // discount per input line
	LinePromos  [][]string        `json:"line_promos"`  // names of the promotions on each line
	Applied     []appliedPromo    `json:"applied"`      // per promotion
	Total       decimal.Decimal   `json:"total"`        // total discount
	CouponValid *bool             `json:"coupon_valid"` // nil when no coupon was given
}

func loadActivePromos(ctx context.Context, tx pgx.Tx, cid, registerID, priceListID, coupon string, at time.Time) ([]promo, bool, error) {
	local := at.In(beirut)
	rows, err := tx.Query(ctx, `SELECT id::text, name, name_i18n::text, kind, applies_to, item_ids::text[], category_ids::text[], brands, value_type,
		value::text, buy_qty::text, get_qty::text, tiers::text, bundle::text, min_amount::text, currency::text, coupon_code,
		register_ids::text[], price_list_ids::text[], stackable, priority
		FROM promotions WHERE company_id = $1 AND is_active
		  AND (date_from IS NULL OR date_from <= $2::date) AND (date_to IS NULL OR date_to >= $2::date)
		  AND (cardinality(days) = 0 OR $3 = ANY (days))
		  AND (time_from IS NULL OR time_to IS NULL OR (time_from <= time_to AND $4::time BETWEEN time_from AND time_to)
		       OR (time_from > time_to AND ($4::time >= time_from OR $4::time <= time_to)))
		  AND (max_uses IS NULL OR uses < max_uses)
		  AND (coupon_code = '' OR coupon_code = upper($5))
		ORDER BY priority DESC, created_at`, cid, local.Format("2006-01-02"), int(local.Weekday()), local.Format("15:04:05"), strings.TrimSpace(coupon))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []promo{}
	couponHit := false
	for rows.Next() {
		var p promo
		var i18n, val, buy, get, tiers, bundle, minA string
		if err := rows.Scan(&p.ID, &p.Name, &i18n, &p.Kind, &p.AppliesTo, &p.Items, &p.Categories, &p.Brands, &p.ValueType, &val, &buy, &get,
			&tiers, &bundle, &minA, &p.Currency, &p.Coupon, &p.Registers, &p.Lists, &p.Stackable, &p.Priority); err != nil {
			return nil, false, err
		}
		if len(p.Registers) > 0 && !contains(p.Registers, registerID) {
			continue
		}
		if len(p.Lists) > 0 && !contains(p.Lists, priceListID) {
			continue
		}
		if p.Coupon != "" {
			couponHit = true
		}
		p.NameI18n = json.RawMessage(i18n)
		p.Value, _ = decimal.NewFromString(val)
		p.Buy, _ = decimal.NewFromString(buy)
		p.Get, _ = decimal.NewFromString(get)
		p.MinAmount, _ = decimal.NewFromString(minA)
		var tl []struct {
			MinQty json.Number `json:"min_qty"`
			Pct    json.Number `json:"pct"`
		}
		_ = json.Unmarshal([]byte(tiers), &tl)
		for _, t := range tl {
			mq, _ := decimal.NewFromString(t.MinQty.String())
			pc, _ := decimal.NewFromString(t.Pct.String())
			p.Tiers = append(p.Tiers, struct{ MinQty, Pct decimal.Decimal }{mq, pc})
		}
		var bl []struct {
			ItemID string      `json:"item_id"`
			Qty    json.Number `json:"qty"`
		}
		_ = json.Unmarshal([]byte(bundle), &bl)
		for _, b := range bl {
			q, _ := decimal.NewFromString(b.Qty.String())
			if isUUID(b.ItemID) && q.IsPositive() {
				p.Bundle = append(p.Bundle, struct {
					ItemID string
					Qty    decimal.Decimal
				}{b.ItemID, q})
			}
		}
		out = append(out, p)
	}
	return out, couponHit, rows.Err()
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (p *promo) matches(l promoLine) bool {
	switch p.AppliesTo {
	case "items":
		return contains(p.Items, l.ItemID) || (l.TemplateID != "" && contains(p.Items, l.TemplateID))
	case "categories":
		return l.CategoryID != "" && contains(p.Categories, l.CategoryID)
	case "brands":
		for _, b := range p.Brands {
			if l.Brand != "" && strings.EqualFold(b, l.Brand) {
				return true
			}
		}
		return false
	}
	return true
}

var hundred = decimal.NewFromInt(100)

// evalPromotions computes the promotion discount of each line (till currency, rounded to dec).
func evalPromotions(promos []promo, lines []promoLine, conv func(v decimal.Decimal, cur *string) (decimal.Decimal, error), dec int32) (*promoResult, error) {
	n := len(lines)
	res := &promoResult{Lines: make([]decimal.Decimal, n), LinePromos: make([][]string, n), Total: decimal.Zero}
	for i := range res.Lines {
		res.Lines[i] = decimal.Zero
		res.LinePromos[i] = []string{}
	}
	type cand struct {
		p   *promo
		per []decimal.Decimal
	}
	cands := []cand{}
	for pi := range promos {
		p := &promos[pi]
		if p.Kind == "basket" {
			continue
		}
		per := make([]decimal.Decimal, n)
		for i := range per {
			per[i] = decimal.Zero
		}
		idx := []int{}
		totalQty := decimal.Zero
		for i, l := range lines {
			if l.Qty.IsPositive() && l.Amount.IsPositive() && p.matches(l) {
				idx = append(idx, i)
				totalQty = totalQty.Add(l.Qty)
			}
		}
		switch p.Kind {
		case "discount":
			for _, i := range idx {
				l := lines[i]
				switch p.ValueType {
				case "pct":
					per[i] = l.Amount.Mul(decimal.Min(p.Value, hundred)).Div(hundred)
				case "amount": // off per unit
					v, err := conv(p.Value, p.Currency)
					if err != nil {
						return nil, err
					}
					per[i] = decimal.Min(l.Amount, v.Mul(l.Qty))
				case "price": // special unit price
					v, err := conv(p.Value, p.Currency)
					if err != nil {
						return nil, err
					}
					if d := l.Amount.Sub(v.Mul(l.Qty)); d.IsPositive() {
						per[i] = d
					}
				}
			}
		case "tiers":
			best := decimal.Zero
			for _, t := range p.Tiers {
				if totalQty.GreaterThanOrEqual(t.MinQty) && t.Pct.GreaterThan(best) {
					best = decimal.Min(t.Pct, hundred)
				}
			}
			if best.IsPositive() {
				for _, i := range idx {
					per[i] = lines[i].Amount.Mul(best).Div(hundred)
				}
			}
		case "bxgy":
			if !p.Buy.IsPositive() || !p.Get.IsPositive() {
				continue
			}
			sets := totalQty.Div(p.Buy.Add(p.Get)).Floor()
			free := sets.Mul(p.Get)
			pct := p.Value
			if !pct.IsPositive() || pct.GreaterThan(hundred) {
				pct = hundred
			}
			// the cheapest units are the free ones
			sorted := append([]int{}, idx...)
			sort.SliceStable(sorted, func(a, b int) bool {
				return lines[sorted[a]].Amount.Div(lines[sorted[a]].Qty).LessThan(lines[sorted[b]].Amount.Div(lines[sorted[b]].Qty))
			})
			for _, i := range sorted {
				if !free.IsPositive() {
					break
				}
				u := decimal.Min(free, lines[i].Qty)
				unit := lines[i].Amount.Div(lines[i].Qty)
				per[i] = u.Mul(unit).Mul(pct).Div(hundred)
				free = free.Sub(u)
			}
		case "bundle":
			if len(p.Bundle) == 0 {
				continue
			}
			count := decimal.NewFromInt(-1)
			has := map[string]decimal.Decimal{}
			for i, l := range lines {
				if l.Qty.IsPositive() {
					has[l.ItemID] = has[l.ItemID].Add(l.Qty)
					_ = i
				}
			}
			for _, b := range p.Bundle {
				c := has[b.ItemID].Div(b.Qty).Floor()
				if count.IsNegative() || c.LessThan(count) {
					count = c
				}
			}
			if !count.IsPositive() {
				continue
			}
			normal := decimal.Zero
			share := map[int]decimal.Decimal{}
			for _, b := range p.Bundle {
				need := b.Qty.Mul(count)
				for i, l := range lines {
					if l.ItemID != b.ItemID || !need.IsPositive() || !l.Qty.IsPositive() {
						continue
					}
					u := decimal.Min(need, l.Qty)
					v := u.Mul(l.Amount.Div(l.Qty))
					share[i] = share[i].Add(v)
					normal = normal.Add(v)
					need = need.Sub(u)
				}
			}
			disc := decimal.Zero
			switch p.ValueType {
			case "price":
				v, err := conv(p.Value, p.Currency)
				if err != nil {
					return nil, err
				}
				if d := normal.Sub(v.Mul(count)); d.IsPositive() {
					disc = d
				}
			case "pct":
				disc = normal.Mul(decimal.Min(p.Value, hundred)).Div(hundred)
			case "amount":
				v, err := conv(p.Value, p.Currency)
				if err != nil {
					return nil, err
				}
				disc = decimal.Min(normal, v.Mul(count))
			}
			if disc.IsPositive() && normal.IsPositive() {
				for i, sh := range share {
					per[i] = disc.Mul(sh).Div(normal)
				}
			}
		}
		gives := false
		for _, d := range per {
			if d.IsPositive() {
				gives = true
			}
		}
		if gives {
			cands = append(cands, cand{p, per})
		}
	}
	// best deal per line among the non-stackable promotions; stackable ones add up
	won := make([]int, n)
	for i := range won {
		won[i] = -1
	}
	for ci, c := range cands {
		if c.p.Stackable {
			continue
		}
		for i, d := range c.per {
			if d.IsPositive() && (won[i] < 0 || d.GreaterThan(cands[won[i]].per[i]) ||
				(d.Equal(cands[won[i]].per[i]) && c.p.Priority > cands[won[i]].p.Priority)) {
				won[i] = ci
			}
		}
	}
	byPromo := map[string]*appliedPromo{}
	order := []string{}
	add := func(p *promo, i int, d decimal.Decimal) {
		left := lines[i].Amount.Sub(res.Lines[i])
		d = decimal.Min(d, left).Round(dec)
		if !d.IsPositive() {
			return
		}
		res.Lines[i] = res.Lines[i].Add(d)
		res.LinePromos[i] = append(res.LinePromos[i], p.Name)
		ap, ok := byPromo[p.ID]
		if !ok {
			ap = &appliedPromo{ID: p.ID, Name: p.Name, NameI18n: p.NameI18n, Discount: decimal.Zero}
			byPromo[p.ID] = ap
			order = append(order, p.ID)
		}
		ap.Discount = ap.Discount.Add(d)
	}
	for i := range lines {
		if won[i] >= 0 {
			add(cands[won[i]].p, i, cands[won[i]].per[i])
		}
	}
	for _, c := range cands {
		if c.p.Stackable {
			for i, d := range c.per {
				add(c.p, i, d)
			}
		}
	}
	// basket promotions on the subtotal after line promotions
	net := decimal.Zero
	for i, l := range lines {
		net = net.Add(l.Amount.Sub(res.Lines[i]))
	}
	type bc struct {
		p *promo
		d decimal.Decimal
	}
	baskets := []bc{}
	for pi := range promos {
		p := &promos[pi]
		if p.Kind != "basket" {
			continue
		}
		minA, err := conv(p.MinAmount, p.Currency)
		if err != nil {
			return nil, err
		}
		if !net.IsPositive() || net.LessThan(minA) {
			continue
		}
		d := decimal.Zero
		if p.ValueType == "pct" {
			d = net.Mul(decimal.Min(p.Value, hundred)).Div(hundred)
		} else {
			v, err := conv(p.Value, p.Currency)
			if err != nil {
				return nil, err
			}
			d = decimal.Min(v, net)
		}
		if d.IsPositive() {
			baskets = append(baskets, bc{p, d})
		}
	}
	chosen := []bc{}
	bestI := -1
	for i, b := range baskets {
		if b.p.Stackable {
			chosen = append(chosen, b)
		} else if bestI < 0 || b.d.GreaterThan(baskets[bestI].d) {
			bestI = i
		}
	}
	if bestI >= 0 {
		chosen = append([]bc{baskets[bestI]}, chosen...)
	}
	for _, b := range chosen {
		netNow := decimal.Zero
		for i, l := range lines {
			netNow = netNow.Add(l.Amount.Sub(res.Lines[i]))
		}
		d := decimal.Min(b.d, netNow).Round(dec)
		if !d.IsPositive() || !netNow.IsPositive() {
			continue
		}
		// spread over the lines by their net amount; the rounding rest goes to the largest line
		left := d
		largest := -1
		parts := make([]decimal.Decimal, n)
		for i, l := range lines {
			ln := l.Amount.Sub(res.Lines[i])
			parts[i] = d.Mul(ln).Div(netNow).Round(dec)
			left = left.Sub(parts[i])
			if largest < 0 || ln.GreaterThan(lines[largest].Amount.Sub(res.Lines[largest])) {
				largest = i
			}
		}
		if largest >= 0 {
			parts[largest] = parts[largest].Add(left)
		}
		for i := range lines {
			add(b.p, i, parts[i])
		}
	}
	for _, id := range order {
		res.Applied = append(res.Applied, *byPromo[id])
		res.Total = res.Total.Add(byPromo[id].Discount)
	}
	return res, nil
}

// promoContext prepares the active promotions and the currency conversion for a basket in currency cur.
func promoContext(ctx context.Context, tx pgx.Tx, cid, cur, registerID, customerID, coupon string, at time.Time) ([]promo, bool, func(decimal.Decimal, *string) (decimal.Decimal, error), int32, error) {
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return nil, false, nil, 0, err
	}
	rc := newRateCache(ctx, tx, cid, set.Base, today())
	dec, err := rc.decimals(cur)
	if err != nil {
		return nil, false, nil, 0, err
	}
	pl := ""
	if isUUID(customerID) {
		var p *string
		_ = tx.QueryRow(ctx, `SELECT price_list_id::text FROM customers WHERE id = $1 AND company_id = $2`, customerID, cid).Scan(&p)
		pl = deref(p)
	}
	promos, hit, err := loadActivePromos(ctx, tx, cid, registerID, pl, coupon, at)
	if err != nil {
		return nil, false, nil, 0, err
	}
	conv := func(v decimal.Decimal, from *string) (decimal.Decimal, error) {
		if from == nil || *from == cur {
			return v, nil
		}
		return rc.convert(v, *from, cur)
	}
	return promos, hit, conv, dec, nil
}

// lineFacts loads category, brand and template of the items of a basket.
func lineFacts(ctx context.Context, tx pgx.Tx, cid string, ids []string) (map[string][3]string, error) {
	out := map[string][3]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, coalesce(category_id::text, ''), brand, coalesce(template_id::text, '')
		FROM items WHERE company_id = $1 AND id = ANY ($2::uuid[])`, cid, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, c, b, t string
		if err := rows.Scan(&id, &c, &b, &t); err != nil {
			return nil, err
		}
		out[id] = [3]string{c, b, t}
	}
	return out, rows.Err()
}

// POST /retail/promotions/evaluate {currency, register_id, customer_id, coupon, lines: [{item_id, quantity, amount}]}
func (s *Server) evaluatePromotions(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Currency   string `json:"currency"`
		RegisterID string `json:"register_id"`
		CustomerID string `json:"customer_id"`
		Coupon     string `json:"coupon"`
		Lines      []struct {
			ItemID   string `json:"item_id"`
			Quantity string `json:"quantity"`
			Amount   string `json:"amount"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !currencyRe.MatchString(in.Currency) || len(in.Lines) > 300 || len(in.Coupon) > 32 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		promos, hit, conv, dec, err := promoContext(ctx, tx, cid, in.Currency, in.RegisterID, in.CustomerID, in.Coupon, time.Now())
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for _, l := range in.Lines {
			if isUUID(l.ItemID) {
				ids = append(ids, l.ItemID)
			}
		}
		facts, err := lineFacts(ctx, tx, cid, ids)
		if err != nil {
			return nil, err
		}
		lines := []promoLine{}
		for _, l := range in.Lines {
			q, _ := money(l.Quantity)
			a, _ := money(l.Amount)
			f := facts[l.ItemID]
			lines = append(lines, promoLine{ItemID: l.ItemID, CategoryID: f[0], Brand: f[1], TemplateID: f[2], Qty: q, Amount: a})
		}
		res, err := evalPromotions(promos, lines, conv, dec)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Coupon) != "" {
			v := hit
			res.CouponValid = &v
		}
		return res, nil
	})
}

// ---- promotions CRUD ----

const promoCols = `p.id, p.name, p.name_i18n, p.kind, p.applies_to, p.item_ids, p.category_ids, p.brands, p.value_type, p.value::text AS value,
	p.buy_qty::text AS buy_qty, p.get_qty::text AS get_qty, p.tiers, p.bundle, p.min_amount::text AS min_amount, p.currency, p.coupon_code,
	p.date_from, p.date_to, p.days, to_char(p.time_from, 'HH24:MI') AS time_from, to_char(p.time_to, 'HH24:MI') AS time_to,
	p.register_ids, p.price_list_ids, p.stackable, p.priority, p.max_uses, p.uses, p.is_active, p.created_at,
	coalesce((SELECT sum(u.discount) FROM promotion_uses u WHERE u.promotion_id = p.id), 0)::text AS discount_given`

func (s *Server) listPromotions(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+promoCols+` FROM promotions p WHERE p.company_id = $1 ORDER BY p.is_active DESC, p.priority DESC, p.created_at DESC`,
			r.PathValue("cid"))
	})
}

type promoIn struct {
	Name         string            `json:"name"`
	NameI18n     map[string]string `json:"name_i18n"`
	Kind         string            `json:"kind"`
	AppliesTo    string            `json:"applies_to"`
	ItemIDs      []string          `json:"item_ids"`
	CategoryIDs  []string          `json:"category_ids"`
	Brands       []string          `json:"brands"`
	ValueType    string            `json:"value_type"`
	Value        string            `json:"value"`
	BuyQty       string            `json:"buy_qty"`
	GetQty       string            `json:"get_qty"`
	Tiers        []map[string]any  `json:"tiers"`
	Bundle       []map[string]any  `json:"bundle"`
	MinAmount    string            `json:"min_amount"`
	Currency     string            `json:"currency"`
	Coupon       string            `json:"coupon_code"`
	DateFrom     string            `json:"date_from"`
	DateTo       string            `json:"date_to"`
	Days         []int             `json:"days"`
	TimeFrom     string            `json:"time_from"`
	TimeTo       string            `json:"time_to"`
	RegisterIDs  []string          `json:"register_ids"`
	PriceListIDs []string          `json:"price_list_ids"`
	Stackable    bool              `json:"stackable"`
	Priority     int               `json:"priority"`
	MaxUses      *int              `json:"max_uses"`
	IsActive     *bool             `json:"is_active"`
}

func allUUID(list []string) bool {
	for _, x := range list {
		if !isUUID(x) {
			return false
		}
	}
	return len(list) <= 500
}

func validClock(s string) bool {
	if s == "" {
		return true
	}
	_, err := time.Parse("15:04", s)
	return err == nil
}

func (in *promoIn) valid() bool {
	in.Name = strings.TrimSpace(in.Name)
	in.Coupon = strings.ToUpper(strings.TrimSpace(in.Coupon))
	if in.AppliesTo == "" {
		in.AppliesTo = "all"
	}
	if in.ValueType == "" {
		in.ValueType = "pct"
	}
	for _, s := range []*string{&in.Value, &in.BuyQty, &in.GetQty, &in.MinAmount} {
		if strings.TrimSpace(*s) == "" {
			*s = "0"
		}
		if !okMoney(*s) {
			return false
		}
	}
	for _, d := range in.Days {
		if d < 0 || d > 6 {
			return false
		}
	}
	if in.ValueType == "pct" {
		if v, _ := decimal.NewFromString(in.Value); v.GreaterThan(hundred) {
			return false
		}
	}
	return in.Name != "" && len(in.Name) <= 120 && strings.Contains("|discount|bxgy|tiers|bundle|basket|", "|"+in.Kind+"|") &&
		strings.Contains("|all|items|categories|brands|", "|"+in.AppliesTo+"|") && strings.Contains("|pct|amount|price|", "|"+in.ValueType+"|") &&
		allUUID(in.ItemIDs) && allUUID(in.CategoryIDs) && allUUID(in.RegisterIDs) && allUUID(in.PriceListIDs) && len(in.Brands) <= 100 &&
		len(in.Tiers) <= 20 && len(in.Bundle) <= 20 && (in.Currency == "" || currencyRe.MatchString(in.Currency)) &&
		(in.DateFrom == "" || validDate(in.DateFrom)) && (in.DateTo == "" || validDate(in.DateTo)) && validClock(in.TimeFrom) && validClock(in.TimeTo) &&
		(in.MaxUses == nil || *in.MaxUses > 0) && (in.Coupon == "" || (len(in.Coupon) >= 3 && len(in.Coupon) <= 32))
}

func (s *Server) savePromotion(w http.ResponseWriter, r *http.Request) {
	var in promoIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if !create && !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	norm := func(v []map[string]any, keys ...string) []byte {
		out := []map[string]string{}
		for _, m := range v {
			x := map[string]string{}
			for _, k := range keys {
				x[k] = strings.TrimSpace(fmt.Sprint(m[k]))
			}
			out = append(out, x)
		}
		b, _ := json.Marshal(out)
		return b
	}
	tiers := norm(in.Tiers, "min_qty", "pct")
	bundle := norm(in.Bundle, "item_id", "qty")
	if in.NameI18n == nil {
		in.NameI18n = map[string]string{}
	}
	i18n, _ := json.Marshal(in.NameI18n)
	nz := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	days := in.Days
	if days == nil {
		days = []int{}
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		// referenced items, categories, registers and price lists must be of this company
		for _, chk := range []struct {
			table string
			ids   []string
		}{{"items", in.ItemIDs}, {"item_categories", in.CategoryIDs}, {"pos_registers", in.RegisterIDs}, {"price_lists", in.PriceListIDs}} {
			if len(chk.ids) == 0 {
				continue
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+chk.table+` WHERE company_id = $1 AND id = ANY ($2::uuid[])`, cid, chk.ids).Scan(&n); err != nil {
				return nil, err
			}
			if n != len(chk.ids) {
				return nil, badRequest("invalid_reference")
			}
		}
		args := []any{cid, in.Name, i18n, in.Kind, in.AppliesTo, nz(in.ItemIDs), nz(in.CategoryIDs), nz(in.Brands), in.ValueType, in.Value,
			in.BuyQty, in.GetQty, tiers, bundle, in.MinAmount, in.Currency, in.Coupon, in.DateFrom, in.DateTo, days, in.TimeFrom, in.TimeTo,
			nz(in.RegisterIDs), nz(in.PriceListIDs), in.Stackable, in.Priority, in.MaxUses}
		var pid string
		var err error
		if create {
			err = tx.QueryRow(ctx, `INSERT INTO promotions (company_id, name, name_i18n, kind, applies_to, item_ids, category_ids, brands, value_type, value,
				buy_qty, get_qty, tiers, bundle, min_amount, currency, coupon_code, date_from, date_to, days, time_from, time_to, register_ids,
				price_list_ids, stackable, priority, max_uses)
				VALUES ($1, $2, $3, $4, $5, $6::uuid[], $7::uuid[], $8, $9, $10::numeric, $11::numeric, $12::numeric, $13, $14, $15::numeric,
				  nullif($16, ''), $17, nullif($18, '')::date, nullif($19, '')::date, $20, nullif($21, '')::time, nullif($22, '')::time,
				  $23::uuid[], $24::uuid[], $25, $26, $27) RETURNING id::text`, args...).Scan(&pid)
		} else {
			args = append(args, id, in.IsActive)
			err = tx.QueryRow(ctx, `UPDATE promotions SET name = $2, name_i18n = $3, kind = $4, applies_to = $5, item_ids = $6::uuid[],
				category_ids = $7::uuid[], brands = $8, value_type = $9, value = $10::numeric, buy_qty = $11::numeric, get_qty = $12::numeric,
				tiers = $13, bundle = $14, min_amount = $15::numeric, currency = nullif($16, ''), coupon_code = $17,
				date_from = nullif($18, '')::date, date_to = nullif($19, '')::date, days = $20, time_from = nullif($21, '')::time,
				time_to = nullif($22, '')::time, register_ids = $23::uuid[], price_list_ids = $24::uuid[], stackable = $25, priority = $26,
				max_uses = $27, is_active = coalesce($29, is_active)
				WHERE id = $28 AND company_id = $1 RETURNING id::text`, args...).Scan(&pid)
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
		}
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "coupon_taken"}
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+promoCols+` FROM promotions p WHERE p.id = $1`, pid)
	})
}

// recordPromoUses stores what each promotion gave on a document and counts the use.
func recordPromoUses(ctx context.Context, tx pgx.Tx, cid, source, sourceID, cur string, applied []appliedPromo) error {
	for _, a := range applied {
		if !a.Discount.IsPositive() {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO promotion_uses (company_id, promotion_id, source, source_id, currency, discount)
			VALUES ($1, $2, $3, $4, $5, $6::numeric)`, cid, a.ID, source, sourceID, cur, a.Discount.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE promotions SET uses = uses + 1 WHERE id = $1`, a.ID); err != nil {
			return err
		}
	}
	return nil
}
