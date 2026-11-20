package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Online store (app "web"): one public shop per company at /#/shop/<slug>. The catalog is the company's items
// (photos, variants grouped under their template, a description for the shop), with the live stock of one warehouse
// and the prices of one price list (else each item's own price, converted). The cart is priced on the server, with
// the coupons and automatic promotions of the promotions engine and a delivery fee per zone. An order becomes a normal
// sales order (source "online"), confirmed at once or by staff; it then follows the warehouse steps, the delivery
// board and the normal invoicing / collection. The customer follows it with a public tracking link.

type shopSet struct {
	Enabled     bool
	Warehouse   *string
	PriceList   *string
	Currency    string
	ShowLocal   bool
	Payments    []string
	MinOrder    decimal.Decimal
	AutoConfirm bool
	HideOOS     bool
	Featured    []string
	Base, Local string
}

func loadShop(ctx context.Context, tx pgx.Tx, cid string) (*shopSet, error) {
	s := &shopSet{Payments: []string{"cod", "pickup"}, AutoConfirm: true, ShowLocal: true}
	if err := tx.QueryRow(ctx, `SELECT base_currency::text, local_currency::text FROM companies WHERE id = $1`, cid).Scan(&s.Base, &s.Local); err != nil {
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		return nil, err
	}
	var cur *string
	var minOrder string
	err := tx.QueryRow(ctx, `SELECT enabled, warehouse_id::text, price_list_id::text, currency::text, show_local, payments, min_order::text,
		auto_confirm, hide_out_of_stock, featured::text[] FROM shop_settings WHERE company_id = $1`, cid).
		Scan(&s.Enabled, &s.Warehouse, &s.PriceList, &cur, &s.ShowLocal, &s.Payments, &minOrder, &s.AutoConfirm, &s.HideOOS, &s.Featured)
	if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	s.MinOrder, _ = decimal.NewFromString(minOrder)
	s.Currency = s.Base
	if cur != nil {
		s.Currency = *cur
	}
	if s.Featured == nil {
		s.Featured = []string{}
	}
	return s, nil
}

// shopWarehouse: the shop's warehouse, else the company's default one.
func shopWarehouse(ctx context.Context, tx pgx.Tx, cid string, set *shopSet) (string, error) {
	if set.Warehouse != nil {
		return *set.Warehouse, nil
	}
	return defaultWarehouse(ctx, tx, cid)
}

// ---------- pricing ----------

type shopPricer struct {
	rc        *rateCache
	set       *shopSet
	dec       int32
	localRate *decimal.Decimal // local currency per 1 unit of the shop currency (nil = not shown)
	localDec  int32
}

func newShopPricer(ctx context.Context, tx pgx.Tx, cid string, set *shopSet) (*shopPricer, error) {
	p := &shopPricer{rc: newRateCache(ctx, tx, cid, set.Base, today()), set: set}
	d, err := p.rc.decimals(set.Currency)
	if err != nil {
		return nil, err
	}
	p.dec = d
	if set.ShowLocal && set.Local != "" && set.Local != set.Currency {
		rf, err1 := p.rc.rate(set.Currency)
		rt, err2 := p.rc.rate(set.Local)
		if err1 == nil && err2 == nil && rf.IsPositive() && rt.IsPositive() {
			v := rt.Div(rf).Round(6)
			p.localRate = &v
			p.localDec, _ = p.rc.decimals(set.Local)
		}
	}
	return p, nil
}

// price of an item for a quantity in the shop currency: price list break, else the item's price in that currency,
// else its main sale price converted at today's rate.
func (p *shopPricer) price(ctx context.Context, tx pgx.Tx, cid, item string, qty decimal.Decimal) (decimal.Decimal, error) {
	if p.set.PriceList != nil {
		var lp *string
		if err := tx.QueryRow(ctx, `SELECT x.price::text FROM price_list_items x JOIN price_lists l ON l.id = x.list_id AND l.is_active AND l.currency = $4
			WHERE x.list_id = $1 AND x.item_id = $2 AND x.min_qty <= $3::numeric ORDER BY x.min_qty DESC LIMIT 1`,
			*p.set.PriceList, item, qty.String(), p.set.Currency).Scan(&lp); err != nil && err != pgx.ErrNoRows {
			return decimal.Zero, err
		}
		if lp != nil {
			return decimal.NewFromString(*lp)
		}
	}
	var ip *string
	var sale, saleCur string
	if err := tx.QueryRow(ctx, `SELECT ip.price::text, it.sale_price::text, coalesce(it.sale_currency, $3)::text FROM items it
		LEFT JOIN item_prices ip ON ip.item_id = it.id AND ip.currency = $4 WHERE it.id = $1 AND it.company_id = $2`,
		item, cid, p.set.Base, p.set.Currency).Scan(&ip, &sale, &saleCur); err != nil {
		if err == pgx.ErrNoRows {
			return decimal.Zero, notFound()
		}
		return decimal.Zero, err
	}
	return itemPriceIn(p.rc, ip, sale, saleCur, p.set.Currency)
}

func (p *shopPricer) local(v decimal.Decimal) *string {
	if p.localRate == nil {
		return nil
	}
	s := v.Mul(*p.localRate).Round(p.localDec).String()
	return &s
}

// ---------- catalog rows ----------

type shopItem struct {
	ID          string          `json:"id"`
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	NameI18n    json.RawMessage `json:"name_i18n"`
	Unit        string          `json:"unit"`
	Kind        string          `json:"kind"`
	HasImage    bool            `json:"has_image"`
	CategoryID  *string         `json:"category_id"`
	IsTemplate  bool            `json:"is_template"`
	Variant     json.RawMessage `json:"variant_values"`
	Brand       string          `json:"brand"`
	Description string          `json:"description,omitempty"`
	Price       string          `json:"price"`
	PriceLocal  *string         `json:"price_local"`
	FromPrice   bool            `json:"from_price"` // a template: the lowest price of its variants
	InStock     bool            `json:"in_stock"`
	Stock       *string         `json:"stock"` // shown only when low (≤ 5)
	Variants    []shopItem      `json:"variants,omitempty"`
}

const shopItemCols = `it.id::text, it.code, it.name, it.name_i18n::text, it.unit, it.kind, it.image <> '', it.category_id::text, it.is_template,
	it.variant_values::text, it.brand, it.shop_description, coalesce(st.quantity, 0)::text`

func scanShopItems(rows pgx.Rows) ([]shopItem, []decimal.Decimal, error) {
	defer rows.Close()
	var out []shopItem
	var stock []decimal.Decimal
	for rows.Next() {
		var it shopItem
		var i18n, variant, st string
		if err := rows.Scan(&it.ID, &it.Code, &it.Name, &i18n, &it.Unit, &it.Kind, &it.HasImage, &it.CategoryID, &it.IsTemplate, &variant, &it.Brand,
			&it.Description, &st); err != nil {
			return nil, nil, err
		}
		it.NameI18n, it.Variant = json.RawMessage(i18n), json.RawMessage(variant)
		q, _ := decimal.NewFromString(st)
		out = append(out, it)
		stock = append(stock, q)
	}
	return out, stock, rows.Err()
}

// fillShopItems prices the items, works out the stock state, and expands templates with their variants.
func fillShopItems(ctx context.Context, tx pgx.Tx, cid, wh string, p *shopPricer, items []shopItem, stock []decimal.Decimal, withVariants, withDesc bool) ([]shopItem, error) {
	five := decimal.NewFromInt(5)
	for i := range items {
		it := &items[i]
		if !withDesc {
			it.Description = ""
		}
		if it.IsTemplate {
			rows, err := tx.Query(ctx, `SELECT `+shopItemCols+` FROM items it LEFT JOIN stock st ON st.item_id = it.id AND st.warehouse_id = $3
				WHERE it.company_id = $1 AND it.template_id = $2 AND it.is_active AND it.is_sellable AND it.shop_visible ORDER BY it.name LIMIT 100`, cid, it.ID, wh)
			if err != nil {
				return nil, err
			}
			vars, vstock, err := scanShopItems(rows)
			if err != nil {
				return nil, err
			}
			vars, err = fillShopItems(ctx, tx, cid, wh, p, vars, vstock, false, false)
			if err != nil {
				return nil, err
			}
			var low *decimal.Decimal
			for _, v := range vars {
				pv, _ := decimal.NewFromString(v.Price)
				if low == nil || pv.LessThan(*low) {
					low = &pv
				}
				if v.InStock {
					it.InStock = true
				}
				if !it.HasImage && v.HasImage {
					it.HasImage = true
				}
			}
			if low != nil {
				it.Price = low.StringFixed(p.dec)
				it.PriceLocal = p.local(*low)
				it.FromPrice = len(vars) > 1
			} else {
				it.Price = "0"
			}
			if withVariants {
				it.Variants = vars
			}
			continue
		}
		pr, err := p.price(ctx, tx, cid, it.ID, decimal.NewFromInt(1))
		if err != nil {
			return nil, err
		}
		it.Price = pr.StringFixed(p.dec)
		it.PriceLocal = p.local(pr)
		if it.Kind != "stock" {
			it.InStock = true
		} else {
			it.InStock = stock[i].IsPositive()
			if it.InStock && stock[i].LessThanOrEqual(five) {
				s := stock[i].String()
				it.Stock = &s
			}
		}
	}
	return items, nil
}

// ======================= public API =======================

// GET /api/v1/shop/{slug}: the shop's front page (company, look, payment options, zones, categories, featured items).
func (s *Server) shopHome(w http.ResponseWriter, r *http.Request) {
	sc, cid, on, _, ok := s.publicScope(w, r, false)
	if !ok {
		return
	}
	if !on {
		writeErr(w, http.StatusNotFound, "shop_closed")
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadShop(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		p, err := newShopPricer(ctx, tx, cid, set)
		if err != nil {
			return nil, err
		}
		wh, err := shopWarehouse(ctx, tx, cid, set)
		if err != nil {
			return nil, err
		}
		info, err := queryJSONOne(ctx, tx, `SELECT c.name_en, c.name_ar, c.logo, c.default_locale, c.public_slug, s.title, s.tagline, s.banner, s.color,
			s.pay_note, s.whatsapp, s.min_order::text AS min_order, s.terms, s.careers_enabled AND 'hr' = ANY (c.apps) AS careers
			FROM companies c JOIN shop_settings s ON s.company_id = c.id WHERE c.id = $1`, cid)
		if err != nil {
			return nil, err
		}
		cats, err := queryJSON(ctx, tx, `SELECT cat.id, cat.name, cat.name_i18n, cat.color,
			(SELECT count(*) FROM items it WHERE it.category_id = cat.id AND it.is_active AND it.is_sellable AND it.shop_visible AND it.template_id IS NULL) AS items
			FROM item_categories cat WHERE cat.company_id = $1 AND cat.is_active ORDER BY cat.seq, cat.name`, cid)
		if err != nil {
			return nil, err
		}
		zones, err := queryJSON(ctx, tx, `SELECT id, name, fee::text AS fee, free_from::text AS free_from, eta FROM shop_zones
			WHERE company_id = $1 AND is_active ORDER BY seq, name`, cid)
		if err != nil {
			return nil, err
		}
		featured := []shopItem{}
		if len(set.Featured) > 0 {
			rows, err := tx.Query(ctx, `SELECT `+shopItemCols+` FROM items it LEFT JOIN stock st ON st.item_id = it.id AND st.warehouse_id = $3
				WHERE it.company_id = $1 AND it.id = ANY ($2::uuid[]) AND it.is_active AND it.is_sellable AND it.shop_visible
				ORDER BY array_position($2::uuid[], it.id)`, cid, set.Featured, wh)
			if err != nil {
				return nil, err
			}
			items, stock, err := scanShopItems(rows)
			if err != nil {
				return nil, err
			}
			if featured, err = fillShopItems(ctx, tx, cid, wh, p, items, stock, false, false); err != nil {
				return nil, err
			}
		}
		var lr *string
		if p.localRate != nil {
			v := p.localRate.String()
			lr = &v
		}
		return map[string]any{"shop": info, "currency": set.Currency, "local_currency": set.Local, "local_rate": lr, "decimals": p.dec,
			"payments": set.Payments, "zones": zones, "categories": cats, "featured": featured}, nil
	})
}

// GET /api/v1/shop/{slug}/items?category=&q=&offset=: one page (48) of the catalog.
func (s *Server) shopItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cat, search := q.Get("category"), strings.TrimSpace(q.Get("q"))
	offset := queryInt(r, "offset", 0, 100000)
	if (cat != "" && !isUUID(cat)) || len(search) > 80 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	sc, cid, on, _, ok := s.publicScope(w, r, false)
	if !ok {
		return
	}
	if !on {
		writeErr(w, http.StatusNotFound, "shop_closed")
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadShop(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		p, err := newShopPricer(ctx, tx, cid, set)
		if err != nil {
			return nil, err
		}
		wh, err := shopWarehouse(ctx, tx, cid, set)
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT `+shopItemCols+` FROM items it LEFT JOIN stock st ON st.item_id = it.id AND st.warehouse_id = $2
			WHERE it.company_id = $1 AND it.is_active AND it.is_sellable AND it.shop_visible AND it.template_id IS NULL
			  AND ($3 = '' OR it.category_id = nullif($3, '')::uuid)
			  AND ($4 = '' OR it.name ILIKE '%' || $4 || '%' OR it.code ILIKE $4 || '%' OR it.barcode = $4 OR it.brand ILIKE $4 || '%'
			       OR it.name_i18n::text ILIKE '%' || $4 || '%')
			  AND (NOT $5 OR it.kind <> 'stock' OR (NOT it.is_template AND coalesce(st.quantity, 0) > 0)
			       OR (it.is_template AND EXISTS (SELECT 1 FROM items v JOIN stock vs ON vs.item_id = v.id AND vs.warehouse_id = $2
			                                      WHERE v.template_id = it.id AND v.is_active AND v.shop_visible AND vs.quantity > 0)))
			  AND (NOT it.is_template OR EXISTS (SELECT 1 FROM items v WHERE v.template_id = it.id AND v.is_active AND v.is_sellable AND v.shop_visible))
			ORDER BY it.name, it.id OFFSET $6 LIMIT 49`, cid, wh, cat, search, set.HideOOS, offset)
		if err != nil {
			return nil, err
		}
		items, stock, err := scanShopItems(rows)
		if err != nil {
			return nil, err
		}
		more := len(items) > 48
		if more {
			items, stock = items[:48], stock[:48]
		}
		items, err = fillShopItems(ctx, tx, cid, wh, p, items, stock, false, false)
		if err != nil {
			return nil, err
		}
		if items == nil {
			items = []shopItem{}
		}
		return map[string]any{"items": items, "more": more}, nil
	})
}

// GET /api/v1/shop/{slug}/items/{id}: one product (a template comes with its variants).
func (s *Server) shopItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	sc, cid, on, _, ok := s.publicScope(w, r, false)
	if !ok {
		return
	}
	if !on {
		writeErr(w, http.StatusNotFound, "shop_closed")
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadShop(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		p, err := newShopPricer(ctx, tx, cid, set)
		if err != nil {
			return nil, err
		}
		wh, err := shopWarehouse(ctx, tx, cid, set)
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT `+shopItemCols+` FROM items it LEFT JOIN stock st ON st.item_id = it.id AND st.warehouse_id = $3
			WHERE it.company_id = $1 AND it.id = $2 AND it.is_active AND it.is_sellable AND it.shop_visible AND it.template_id IS NULL`, cid, id, wh)
		if err != nil {
			return nil, err
		}
		items, stock, err := scanShopItems(rows)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, notFound()
		}
		items, err = fillShopItems(ctx, tx, cid, wh, p, items, stock, true, true)
		if err != nil {
			return nil, err
		}
		var attrs json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT attributes::text FROM items WHERE id = $1`, id).Scan(&attrs); err != nil {
			return nil, err
		}
		return map[string]any{"item": items[0], "attributes": attrs}, nil
	})
}

// GET /api/v1/shop/{slug}/items/{id}/image: the product photo (a template without one shows its first variant's).
func (s *Server) shopItemImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	sc, cid, on, _, ok := s.publicScope(w, r, false)
	if !ok {
		return
	}
	if !on {
		writeErr(w, http.StatusNotFound, "shop_closed")
		return
	}
	var img string
	err := dbInScope(r.Context(), s, sc, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(nullif(it.image, ''), (SELECT v.image FROM items v WHERE v.template_id = it.id AND v.image <> '' AND v.is_active
			ORDER BY v.name LIMIT 1), '') FROM items it WHERE it.id = $1 AND it.company_id = $2 AND it.is_active AND it.shop_visible`, id, cid).Scan(&img)
	})
	if err != nil || img == "" {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	mime, data, ok := decodeDataURL(img)
	if !ok || !strings.HasPrefix(mime, "image/") || mime == "image/svg+xml" {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// decodeDataURL splits "data:<mime>;base64,<data>".
func decodeDataURL(s string) (string, []byte, bool) {
	if !strings.HasPrefix(s, "data:") {
		return "", nil, false
	}
	i := strings.Index(s, ";base64,")
	if i < 0 {
		return "", nil, false
	}
	b, err := base64.StdEncoding.DecodeString(s[i+8:])
	if err != nil {
		return "", nil, false
	}
	return s[5:i], b, true
}

// ---------- the cart ----------

type cartLineIn struct {
	ItemID   string `json:"item_id"`
	Quantity string `json:"quantity"`
}

type cartIn struct {
	Lines    []cartLineIn `json:"lines"`
	ZoneID   string       `json:"zone_id"`
	Delivery string       `json:"delivery"` // delivery | pickup
	Coupon   string       `json:"coupon"`
}

func (in *cartIn) valid() bool {
	// audit P7-M2: an online cart is a household order, not a way to lock up the stock (100 units a line, 50 lines)
	if len(in.Lines) == 0 || len(in.Lines) > 50 || (in.ZoneID != "" && !isUUID(in.ZoneID)) || len(in.Coupon) > 32 {
		return false
	}
	if in.Delivery == "" {
		in.Delivery = "delivery"
	}
	if in.Delivery != "delivery" && in.Delivery != "pickup" {
		return false
	}
	for _, l := range in.Lines {
		q, ok := money(l.Quantity)
		if !isUUID(l.ItemID) || !ok || !q.IsPositive() || q.GreaterThan(decimal.NewFromInt(100)) {
			return false
		}
	}
	return true
}

type cartLine struct {
	ItemID      string          `json:"item_id"`
	Name        string          `json:"name"`
	NameI18n    json.RawMessage `json:"name_i18n"`
	Variant     json.RawMessage `json:"variant_values"`
	Quantity    string          `json:"quantity"`
	UnitPrice   string          `json:"unit_price"`
	Discount    string          `json:"discount"`
	Amount      string          `json:"amount"`
	Tax         string          `json:"tax"`
	Promos      []string        `json:"promos"`
	discountPct string
	taxRateID   string
}

type cartProblem struct {
	ItemID    string `json:"item_id"`
	Code      string `json:"code"`
	Available string `json:"available,omitempty"`
}

type cartResult struct {
	Lines       []cartLine     `json:"lines"`
	Problems    []cartProblem  `json:"problems"`
	Currency    string         `json:"currency"`
	Subtotal    string         `json:"subtotal"` // after discounts, before tax
	Discount    string         `json:"discount"`
	Tax         string         `json:"tax"`
	Fee         string         `json:"fee"`
	Total       string         `json:"total"`
	TotalLocal  *string        `json:"total_local"`
	CouponValid *bool          `json:"coupon_valid"`
	Applied     []appliedPromo `json:"promotions"`
	MinOrder    string         `json:"min_order"`
	zoneName    string
	feeDec      decimal.Decimal
}

// priceCart prices a cart exactly the way the sales order will (same rounding, same promotion engine).
func priceCart(ctx context.Context, tx pgx.Tx, cid string, set *shopSet, p *shopPricer, in *cartIn) (*cartResult, error) {
	wh, err := shopWarehouse(ctx, tx, cid, set)
	if err != nil {
		return nil, err
	}
	res := &cartResult{Lines: []cartLine{}, Problems: []cartProblem{}, Currency: set.Currency, Applied: []appliedPromo{}, MinOrder: set.MinOrder.String()}
	// the same item twice counts once
	qty := map[string]decimal.Decimal{}
	order := []string{}
	for _, l := range in.Lines {
		q, _ := money(l.Quantity)
		if _, seen := qty[l.ItemID]; !seen {
			order = append(order, l.ItemID)
		}
		qty[l.ItemID] = qty[l.ItemID].Add(q)
	}
	type row struct {
		kind, name, i18n, variant, tax, cat, brand, tmpl string
		stock                                            decimal.Decimal
	}
	rows := map[string]row{}
	for _, id := range order {
		var x row
		var st string
		var tax, cat, tmpl *string
		err := tx.QueryRow(ctx, `SELECT it.kind, it.name, it.name_i18n::text, it.variant_values::text, it.sale_tax_rate_id::text, it.category_id::text,
			it.brand, it.template_id::text, coalesce(st.quantity, 0)::text
			FROM items it LEFT JOIN stock st ON st.item_id = it.id AND st.warehouse_id = $3
			WHERE it.id = $1 AND it.company_id = $2 AND it.is_active AND it.is_sellable AND it.shop_visible AND NOT it.is_template`, id, cid, wh).
			Scan(&x.kind, &x.name, &x.i18n, &x.variant, &tax, &cat, &x.brand, &tmpl, &st)
		if err == pgx.ErrNoRows {
			res.Problems = append(res.Problems, cartProblem{ItemID: id, Code: "unavailable"})
			continue
		}
		if err != nil {
			return nil, err
		}
		x.tax, x.cat, x.tmpl = deref(tax), deref(cat), deref(tmpl)
		x.stock, _ = decimal.NewFromString(st)
		if x.kind == "stock" && qty[id].GreaterThan(x.stock) {
			res.Problems = append(res.Problems, cartProblem{ItemID: id, Code: "out_of_stock", Available: x.stock.String()})
		}
		rows[id] = x
	}
	// base amounts, then the promotions on them
	var plines []promoLine
	var ids []string
	prices := map[string]decimal.Decimal{}
	for _, id := range order {
		x, ok := rows[id]
		if !ok {
			continue
		}
		pr, err := p.price(ctx, tx, cid, id, qty[id])
		if err != nil {
			return nil, err
		}
		prices[id] = pr
		ids = append(ids, id)
		plines = append(plines, promoLine{ItemID: id, CategoryID: x.cat, Brand: x.brand, TemplateID: x.tmpl, Qty: qty[id], Amount: qty[id].Mul(pr).Round(p.dec)})
	}
	promos, hit, conv, _, err := promoContext(ctx, tx, cid, set.Currency, "", "", in.Coupon, time.Now())
	if err != nil {
		return nil, err
	}
	pres, err := evalPromotions(promos, plines, conv, p.dec)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Coupon) != "" {
		v := hit
		res.CouponValid = &v
	}
	res.Applied = pres.Applied
	sub, disc, taxT := decimal.Zero, decimal.Zero, decimal.Zero
	for i, id := range ids {
		x := rows[id]
		q, pr := qty[id], prices[id]
		base := plines[i].Amount
		d := decimal.Zero
		if base.IsPositive() && pres.Lines[i].IsPositive() {
			d = pres.Lines[i].Mul(hundred).Div(base).Round(4)
			if d.GreaterThan(hundred) {
				d = hundred
			}
		}
		amt := q.Mul(pr).Mul(hundred.Sub(d)).Div(hundred).Round(p.dec)
		taxID := x.tax
		tax, err := loadTaxRate(ctx, tx, cid, taxID, "sales")
		if err != nil {
			tax, taxID = nil, "" // an inactive tax rate on the item: sold without it (the order is written the same way)
		}
		ta := taxOn(amt, tax, p.dec)
		res.Lines = append(res.Lines, cartLine{ItemID: id, Name: x.name, NameI18n: json.RawMessage(x.i18n), Variant: json.RawMessage(x.variant),
			Quantity: q.String(), UnitPrice: pr.StringFixed(p.dec), Discount: base.Sub(amt).StringFixed(p.dec), Amount: amt.StringFixed(p.dec),
			Tax: ta.StringFixed(p.dec), Promos: pres.LinePromos[i], discountPct: d.String(), taxRateID: taxID})
		sub, disc, taxT = sub.Add(amt), disc.Add(base.Sub(amt)), taxT.Add(ta)
	}
	fee := decimal.Zero
	if in.Delivery == "delivery" && in.ZoneID != "" {
		var f string
		var free *string
		err := tx.QueryRow(ctx, `SELECT name, fee::text, free_from::text FROM shop_zones WHERE id = $1 AND company_id = $2 AND is_active`, in.ZoneID, cid).
			Scan(&res.zoneName, &f, &free)
		if err == pgx.ErrNoRows {
			return nil, badRequest("invalid_zone")
		}
		if err != nil {
			return nil, err
		}
		fee, _ = decimal.NewFromString(f)
		if free != nil {
			if ff, _ := decimal.NewFromString(*free); sub.GreaterThanOrEqual(ff) {
				fee = decimal.Zero
			}
		}
		fee = fee.Round(p.dec)
	}
	res.feeDec = fee
	total := sub.Add(taxT).Add(fee)
	res.Subtotal, res.Discount, res.Tax, res.Fee, res.Total = sub.StringFixed(p.dec), disc.StringFixed(p.dec), taxT.StringFixed(p.dec), fee.StringFixed(p.dec), total.StringFixed(p.dec)
	res.TotalLocal = p.local(total)
	return res, nil
}

// POST /api/v1/shop/{slug}/cart {lines, zone_id, delivery, coupon}: the priced cart (nothing is stored).
func (s *Server) shopCart(w http.ResponseWriter, r *http.Request) {
	var in cartIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	// audit P7-L6: trying coupon codes is limited to 20 an hour per visitor and shop
	if strings.TrimSpace(in.Coupon) != "" {
		key := clientIP(r) + "|" + strings.ToLower(r.PathValue("slug"))
		if !couponLimiter.allow(key) {
			writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
			return
		}
		couponLimiter.fail(key)
	}
	sc, cid, on, _, ok := s.publicScope(w, r, false)
	if !ok {
		return
	}
	if !on {
		writeErr(w, http.StatusNotFound, "shop_closed")
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadShop(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		p, err := newShopPricer(ctx, tx, cid, set)
		if err != nil {
			return nil, err
		}
		return priceCart(ctx, tx, cid, set, p, &in)
	})
}

type shopOrderIn struct {
	cartIn
	Name    string `json:"name"`
	Phone   string `json:"phone"`
	Email   string `json:"email"`
	Address string `json:"address"`
	Payment string `json:"payment"`
	Notes   string `json:"notes"`
	Website string `json:"website"` // honeypot: people never fill it
}

// POST /api/v1/shop/{slug}/orders: place an order → customer (found by phone or created), sales order, tracking link.
func (s *Server) shopPlaceOrder(w http.ResponseWriter, r *http.Request) {
	var in shopOrderIn
	if !decodeBody(w, r, &in) {
		return
	}
	in.Name, in.Phone, in.Email, in.Address, in.Notes = strings.TrimSpace(in.Name), strings.TrimSpace(in.Phone), strings.TrimSpace(in.Email),
		strings.TrimSpace(in.Address), strings.TrimSpace(in.Notes)
	if !in.valid() || in.Name == "" || len(in.Name) > 160 || len(in.Phone) < 6 || len(in.Phone) > 40 || len(in.Email) > 200 ||
		(in.Email != "" && !strings.Contains(in.Email, "@")) || len(in.Address) > 500 || len(in.Notes) > 1000 ||
		!contains([]string{"cod", "pickup", "whish", "omt"}, in.Payment) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Delivery == "delivery" && in.Address == "" {
		writeErr(w, http.StatusBadRequest, "address_required")
		return
	}
	sc, cid, on, _, ok := s.publicScope(w, r, true)
	if !ok {
		return
	}
	if !on {
		writeErr(w, http.StatusNotFound, "shop_closed")
		return
	}
	if in.Website != "" { // a bot filled the hidden field: answer like a success, store nothing
		writeJSON(w, http.StatusOK, map[string]any{"number": 0, "token": randToken(24)})
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadShop(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if !contains(set.Payments, in.Payment) {
			return nil, badRequest("bad_request")
		}
		if in.Delivery == "delivery" && in.ZoneID == "" {
			var zones bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM shop_zones WHERE company_id = $1 AND is_active)`, cid).Scan(&zones); err != nil {
				return nil, err
			}
			if zones {
				return nil, badRequest("zone_required")
			}
		}
		p, err := newShopPricer(ctx, tx, cid, set)
		if err != nil {
			return nil, err
		}
		cart, err := priceCart(ctx, tx, cid, set, p, &in.cartIn)
		if err != nil {
			return nil, err
		}
		if len(cart.Problems) > 0 {
			b, _ := json.Marshal(cart.Problems)
			return nil, &apiError{status: http.StatusConflict, code: "cart_changed", detail: string(b)}
		}
		if cart.CouponValid != nil && !*cart.CouponValid {
			return nil, badRequest("invalid_coupon")
		}
		if sub, _ := decimal.NewFromString(cart.Subtotal); sub.LessThan(set.MinOrder) {
			return nil, &apiError{status: http.StatusBadRequest, code: "min_order", detail: set.MinOrder.String() + " " + set.Currency}
		}
		wh, err := shopWarehouse(ctx, tx, cid, set)
		if err != nil {
			return nil, err
		}
		// audit P7-M2: a phone number places at most 3 orders a day, a shop at most 200 an hour
		var byPhone, byShop int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE phone_key(phone) = phone_key($2) AND created_at > now() - interval '1 day'),
			count(*) FILTER (WHERE created_at > now() - interval '1 hour') FROM shop_orders WHERE company_id = $1 AND created_at > now() - interval '1 day'`,
			cid, in.Phone).Scan(&byPhone, &byShop); err != nil {
			return nil, err
		}
		if byPhone >= 3 || byShop >= 200 {
			return nil, &apiError{status: http.StatusTooManyRequests, code: "too_many_orders"}
		}
		known, err := customerByPhone(ctx, tx, cid, in.Phone)
		if err != nil {
			return nil, err
		}
		customer, err := shopCustomer(ctx, tx, cid, in.Name, in.Phone, in.Email, in.Address)
		if err != nil {
			return nil, err
		}
		// the sales order (a quotation until confirmed)
		var qn int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'quotation')`, cid).Scan(&qn); err != nil {
			return nil, err
		}
		notes := "Online order"
		if in.Notes != "" {
			notes += ": " + in.Notes
		}
		var so string
		if err := tx.QueryRow(ctx, `INSERT INTO sales_orders (company_id, number, customer_id, currency, quote_date, valid_until, notes, terms, status, source, salesperson_id)
			VALUES ($1, $2, $3, $4, current_date, current_date + 7, $5, '', 'sent', 'online', NULL) RETURNING id::text`,
			cid, qn, customer, set.Currency, notes).Scan(&so); err != nil {
			return nil, err
		}
		var lines []orderLineIn
		for _, l := range cart.Lines {
			lines = append(lines, orderLineIn{ItemID: l.ItemID, Description: l.Name, Quantity: l.Quantity, UnitPrice: l.UnitPrice, DiscountPct: l.discountPct,
				TaxRateID: l.taxRateID, WarehouseID: wh})
		}
		if cart.feeDec.IsPositive() {
			lines = append(lines, orderLineIn{Description: "Delivery: " + cart.zoneName, Quantity: "1", UnitPrice: cart.feeDec.String()})
		}
		if err := writeOrderLines(ctx, tx, cid, so, set.Currency, lines); err != nil {
			return nil, err
		}
		if len(cart.Applied) > 0 {
			if err := recordPromoUses(ctx, tx, cid, "shop", so, set.Currency, cart.Applied); err != nil {
				return nil, err
			}
		}
		var sub, taxT, total string
		if err := tx.QueryRow(ctx, `SELECT subtotal::text, tax_total::text, total::text FROM sales_orders WHERE id = $1`, so).Scan(&sub, &taxT, &total); err != nil {
			return nil, err
		}
		status := "new"
		// auto-confirm (which reserves stock) only for a customer the shop already knows; a first order from an
		// unknown number waits for staff (audit P7-M2)
		if set.AutoConfirm && known != nil {
			if err := confirmOrderTx(ctx, tx, cid, so, today()); err != nil {
				return nil, err
			}
			status = "confirmed"
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'shop_order')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		tok := randToken(24)
		var zone *string
		if in.Delivery == "delivery" {
			zone = &in.ZoneID
		}
		var oid string
		if err := tx.QueryRow(ctx, `INSERT INTO shop_orders (company_id, number, token, sales_order_id, customer_id, name, phone, email, address, zone_id,
			delivery, payment, coupon, currency, subtotal, discount, fee, total, notes, status, ip)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, upper(trim($13)), $14, $15::numeric, $16::numeric, $17::numeric, $18::numeric, $19, $20, $21)
			RETURNING id::text`, cid, n, tok, so, customer, in.Name, in.Phone, in.Email, in.Address, zone, in.Delivery, in.Payment, in.Coupon,
			set.Currency, sub, cart.Discount, cart.feeDec.String(), total, in.Notes, status, clientIP(r)).Scan(&oid); err != nil {
			return nil, err
		}
		link := publicURL() + "/#/order/" + tok
		if err := notifyStaff(ctx, tx, cid, "sales.view", "shop", fmt.Sprintf("Online order #%d · %s", n, in.Name),
			fmt.Sprintf("%s %s · %s", total, set.Currency, in.Phone), "sales_order", so); err != nil {
			return nil, err
		}
		// the customer gets the tracking link (WhatsApp when the messaging app is on)
		var msgApp bool
		var company string
		if err := tx.QueryRow(ctx, `SELECT 'msg' = ANY (apps), coalesce(nullif(name_ar, ''), name_en) FROM companies WHERE id = $1`, cid).Scan(&msgApp, &company); err != nil {
			return nil, err
		}
		// audit P7-M2: no message to a number the company doesn't know (the shop must not relay text to strangers),
		// and the visitor's free text never goes into it beyond a short, plain first name
		if msgApp && known != nil {
			body := growthText(ctx, tx, cid, "shop_order", company, plainName(in.Name, 30), strconv.FormatInt(n, 10), total+" "+set.Currency, link)
			if _, _, err := queueOut(ctx, tx, cid, outMessage{CustomerID: &customer, Channel: "whatsapp", Phone: in.Phone, Body: body, Event: "shop_order"}); err != nil {
				return nil, err
			}
		}
		return map[string]any{"number": n, "token": tok, "link": link, "total": total, "currency": set.Currency, "status": status}, nil
	})
}

// shopCustomer finds the customer by phone (last 7 digits) or creates one.
func shopCustomer(ctx context.Context, tx pgx.Tx, cid, name, phone, email, address string) (string, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if len(digits) < 6 {
		return "", badRequest("invalid_phone")
	}
	// audit P7-M1: the full national number, and only one customer with it (71 123456 ≠ 81 123456)
	found, err := customerByPhone(ctx, tx, cid, phone)
	if err != nil {
		return "", err
	}
	if found != nil {
		return *found, nil
	}
	var id string
	var n int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'customer')`, cid).Scan(&n); err != nil {
		return "", err
	}
	code := strconv.FormatInt(n, 10)
	for i := 0; i < 50; i++ {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE company_id = $1 AND code = $2)`, cid, code).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			break
		}
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'customer')`, cid).Scan(&n); err != nil {
			return "", err
		}
		code = strconv.FormatInt(n, 10)
	}
	err = tx.QueryRow(ctx, `INSERT INTO customers (company_id, code, name, phone, email, address, notes) VALUES ($1, $2, $3, $4, $5, $6, 'Online shop')
		RETURNING id::text`, cid, code, name, phone, email, address).Scan(&id)
	return id, err
}

// confirmOrderTx turns a quotation into a sales order (as the "confirm" action, for orders placed without a user).
func confirmOrderTx(ctx context.Context, tx pgx.Tx, cid, id, date string) error {
	status, err := lockOrder(ctx, tx, cid, id)
	if err != nil {
		return err
	}
	if status != "draft" && status != "sent" {
		return &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	var n int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'sales_order')`, cid).Scan(&n); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE sales_orders SET status = 'confirmed', order_number = $2, order_date = $3::date WHERE id = $1`, id, n, date); err != nil {
		return err
	}
	return whOnOrderConfirmed(ctx, tx, cid, id)
}

// the customer-facing state of an online order
const shopTrackState = `CASE WHEN o.status = 'cancelled' OR so.status = 'cancelled' THEN 'cancelled'
	WHEN EXISTS (SELECT 1 FROM trip_stops ts WHERE ts.sales_order_id = so.id AND ts.status = 'delivered')
	  OR NOT EXISTS (SELECT 1 FROM sales_order_lines l JOIN items it ON it.id = l.item_id WHERE l.order_id = so.id AND it.kind = 'stock' AND l.qty_delivered < l.quantity)
	     AND EXISTS (SELECT 1 FROM sales_deliveries d WHERE d.order_id = so.id AND d.status <> 'void') THEN 'delivered'
	WHEN EXISTS (SELECT 1 FROM trip_stops ts JOIN delivery_trips t ON t.id = ts.trip_id WHERE ts.sales_order_id = so.id AND ts.status = 'pending' AND t.status = 'out') THEN 'on_the_way'
	WHEN so.status IN ('confirmed', 'done') THEN 'preparing'
	ELSE 'received' END`

// GET /api/v1/shop-orders/{token}: the order as its customer sees it.
func (s *Server) shopTrack(w http.ResponseWriter, r *http.Request) {
	sc, cid, oid, _, ok := s.tokenScope(w, r, "shop_order")
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		o, err := queryJSONOne(ctx, tx, `SELECT o.number, o.name, o.phone, o.address, o.delivery, o.payment, o.coupon, o.currency, o.subtotal::text AS subtotal,
			o.discount::text AS discount, o.fee::text AS fee, so.tax_total::text AS tax, so.total::text AS total, o.created_at, z.name AS zone, z.eta,
			`+shopTrackState+` AS state, c.name_en AS company, c.name_ar AS company_ar, c.logo, c.default_locale AS locale, c.public_slug AS slug,
			s.color, s.whatsapp, s.pay_note
			FROM shop_orders o JOIN sales_orders so ON so.id = o.sales_order_id JOIN companies c ON c.id = o.company_id
			LEFT JOIN shop_zones z ON z.id = o.zone_id LEFT JOIN shop_settings s ON s.company_id = o.company_id
			WHERE o.id = $1 AND o.company_id = $2`, oid, cid)
		if err != nil {
			return nil, err
		}
		lines, err := queryJSON(ctx, tx, `SELECT l.description, trim_scale(l.quantity)::text AS quantity, l.unit_price::text AS unit_price, l.amount::text AS amount,
			trim_scale(l.qty_delivered)::text AS delivered
			FROM shop_orders o JOIN sales_order_lines l ON l.order_id = o.sales_order_id WHERE o.id = $1 ORDER BY l.line_no`, oid)
		if err != nil {
			return nil, err
		}
		// phase 9: the parcel with the carrier, when the order went by Aramex / DHL / a courier
		ships, err := queryJSON(ctx, tx, `SELECT sh.carrier, sh.tracking_no, sh.status, sh.events, sh.updated_at FROM shipments sh
			WHERE sh.shop_order_id = $1 AND sh.status <> 'cancelled' AND sh.tracking_no <> '' ORDER BY sh.created_at`, oid)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"order": o, "lines": lines, "shipments": ships}, nil
	})
}

// ======================= staff API (app "web") =======================

const shopSettingsCols = `s.enabled, s.title, s.tagline, s.banner, s.color, s.warehouse_id, s.price_list_id, s.currency, s.show_local, s.payments,
	s.pay_note, s.whatsapp, s.min_order::text AS min_order, s.auto_confirm, s.hide_out_of_stock, s.featured, s.terms, s.careers_enabled, s.careers_intro, s.updated_at`

// GET /companies/{cid}/shop/settings
func (s *Server) shopGetSettings(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO shop_settings (company_id) VALUES ($1) ON CONFLICT (company_id) DO NOTHING`, cid); err != nil {
			return nil, err
		}
		set, err := queryJSONOne(ctx, tx, `SELECT `+shopSettingsCols+`, c.public_slug, c.base_currency, c.local_currency, c.name_en, c.name_ar
			FROM shop_settings s JOIN companies c ON c.id = s.company_id WHERE s.company_id = $1`, cid)
		if err != nil {
			return nil, err
		}
		stats, err := queryJSONOne(ctx, tx, `SELECT count(*) FILTER (WHERE o.created_at::date = current_date) AS today,
			count(*) FILTER (WHERE o.status = 'new') AS waiting, count(*) AS total,
			coalesce(sum(o.total) FILTER (WHERE o.created_at > now() - interval '30 days' AND o.status <> 'cancelled'), 0)::text AS sales_30d
			FROM shop_orders o WHERE o.company_id = $1`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"settings": set, "stats": stats, "public_url": publicURL()}, nil
	})
}

type shopSettingsIn struct {
	Enabled        *bool     `json:"enabled"`
	Slug           *string   `json:"slug"`
	Title          *string   `json:"title"`
	Tagline        *string   `json:"tagline"`
	Banner         *string   `json:"banner"`
	Color          *string   `json:"color"`
	WarehouseID    *string   `json:"warehouse_id"`
	PriceListID    *string   `json:"price_list_id"`
	Currency       *string   `json:"currency"`
	ShowLocal      *bool     `json:"show_local"`
	Payments       *[]string `json:"payments"`
	PayNote        *string   `json:"pay_note"`
	WhatsApp       *string   `json:"whatsapp"`
	MinOrder       *string   `json:"min_order"`
	AutoConfirm    *bool     `json:"auto_confirm"`
	HideOutOfStock *bool     `json:"hide_out_of_stock"`
	Featured       *[]string `json:"featured"`
	Terms          *string   `json:"terms"`
	CareersEnabled *bool     `json:"careers_enabled"`
	CareersIntro   *string   `json:"careers_intro"`
}

// PUT /companies/{cid}/shop/settings (any subset of the fields)
func (s *Server) shopPutSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var in shopSettingsIn
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if (in.Currency != nil && *in.Currency != "" && !currencyRe.MatchString(*in.Currency)) || !optUUID(in.WarehouseID) || !optUUID(in.PriceListID) ||
		(in.MinOrder != nil && *in.MinOrder != "" && !okMoney(*in.MinOrder)) || (in.Featured != nil && (len(*in.Featured) > 24 || !allUUID(*in.Featured))) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Slug != nil {
		v := strings.ToLower(strings.TrimSpace(*in.Slug))
		in.Slug = &v
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO shop_settings (company_id) VALUES ($1) ON CONFLICT (company_id) DO NOTHING`, cid); err != nil {
			return nil, err
		}
		if wid := deref(in.WarehouseID); wid != "" {
			if _, err := checkWarehouse(ctx, tx, cid, wid); err != nil {
				return nil, err
			}
		}
		if pl := deref(in.PriceListID); pl != "" {
			var cur string
			if err := tx.QueryRow(ctx, `SELECT currency::text FROM price_lists WHERE id = $1 AND company_id = $2`, pl, cid).Scan(&cur); err != nil {
				if err == pgx.ErrNoRows {
					return nil, badRequest("invalid_price_list")
				}
				return nil, err
			}
			in.Currency = &cur // a price list sets the shop's currency
		}
		if in.Featured != nil && len(*in.Featured) > 0 {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM items WHERE company_id = $1 AND id = ANY ($2::uuid[])`, cid, *in.Featured).Scan(&n); err != nil {
				return nil, err
			}
			if n != len(*in.Featured) {
				return nil, badRequest("invalid_item")
			}
		}
		if in.Slug != nil {
			if *in.Slug != "" && !slugRe.MatchString(*in.Slug) {
				return nil, badRequest("invalid_slug")
			}
			// audit P7-L10: names of the platform and of well-known brands are kept for their owners (a platform
			// admin assigns them, PUT /platform/tenants/{id}/slug)
			if *in.Slug != "" && reservedSlug(*in.Slug) {
				return nil, &apiError{status: http.StatusConflict, code: "slug_reserved"}
			}
			_, err := tx.Exec(ctx, `UPDATE companies SET public_slug = nullif($2, '') WHERE id = $1`, cid, *in.Slug)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "slug_taken"}
			}
			if err != nil {
				return nil, err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE shop_settings SET enabled = coalesce($2, enabled), title = coalesce(trim($3), title), tagline = coalesce(trim($4), tagline),
			banner = coalesce($5, banner), color = coalesce($6, color),
			warehouse_id = CASE WHEN $7::text IS NULL THEN warehouse_id ELSE nullif($7, '')::uuid END,
			price_list_id = CASE WHEN $8::text IS NULL THEN price_list_id ELSE nullif($8, '')::uuid END,
			currency = CASE WHEN $9::text IS NULL THEN currency ELSE nullif($9, '')::char(3) END,
			show_local = coalesce($10, show_local), payments = coalesce($11, payments), pay_note = coalesce(trim($12), pay_note),
			whatsapp = coalesce(trim($13), whatsapp), min_order = coalesce(nullif($14, '')::numeric, min_order), auto_confirm = coalesce($15, auto_confirm),
			hide_out_of_stock = coalesce($16, hide_out_of_stock), featured = coalesce($17::uuid[], featured), terms = coalesce($18, terms),
			careers_enabled = coalesce($19, careers_enabled), careers_intro = coalesce($20, careers_intro), updated_at = now()
			WHERE company_id = $1`, cid, in.Enabled, in.Title, in.Tagline, in.Banner, in.Color, in.WarehouseID, in.PriceListID, in.Currency, in.ShowLocal,
			in.Payments, in.PayNote, in.WhatsApp, in.MinOrder, in.AutoConfirm, in.HideOutOfStock, in.Featured, in.Terms, in.CareersEnabled, in.CareersIntro)
		if err != nil {
			return nil, err
		}
		// a shop or careers page that is on needs its address
		var on bool
		if err := tx.QueryRow(ctx, `SELECT (s.enabled OR s.careers_enabled) AND c.public_slug IS NULL FROM shop_settings s JOIN companies c ON c.id = s.company_id
			WHERE s.company_id = $1`, cid).Scan(&on); err != nil {
			return nil, err
		}
		if on {
			return nil, badRequest("slug_required")
		}
		return queryJSONOne(ctx, tx, `SELECT `+shopSettingsCols+`, c.public_slug FROM shop_settings s JOIN companies c ON c.id = s.company_id WHERE s.company_id = $1`, cid)
	})
}

// ---------- zones ----------

func (s *Server) shopZones(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT z.id, z.name, z.fee::text AS fee, z.free_from::text AS free_from, z.eta, z.seq, z.is_active,
			(SELECT count(*) FROM shop_orders o WHERE o.zone_id = z.id) AS orders
			FROM shop_zones z WHERE z.company_id = $1 ORDER BY z.seq, z.name`, r.PathValue("cid"))
	})
}

// POST /shop/zones and PATCH /shop/zones/{id} {name, fee, free_from, eta, seq, is_active}
func (s *Server) shopSaveZone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     *string `json:"name"`
		Fee      *string `json:"fee"`
		FreeFrom *string `json:"free_from"`
		ETA      *string `json:"eta"`
		Seq      *int    `json:"seq"`
		IsActive *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	if (creating && strings.TrimSpace(deref(in.Name)) == "") || (in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 80)) ||
		(in.Fee != nil && !okMoney(*in.Fee)) || (in.FreeFrom != nil && *in.FreeFrom != "" && !okMoney(*in.FreeFrom)) || len(deref(in.ETA)) > 60 ||
		(!creating && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if creating {
			if err := tx.QueryRow(ctx, `INSERT INTO shop_zones (company_id, name) VALUES ($1, trim($2)) RETURNING id::text`, cid, *in.Name).Scan(&id); err != nil {
				return nil, err
			}
		}
		return dmlJSONOne(ctx, tx, `UPDATE shop_zones SET name = coalesce(trim($3), name), fee = coalesce($4::numeric, fee),
			free_from = CASE WHEN $5::text IS NULL THEN free_from ELSE nullif($5, '')::numeric END, eta = coalesce(trim($6), eta),
			seq = coalesce($7, seq), is_active = coalesce($8, is_active)
			WHERE id = $1 AND company_id = $2 RETURNING id, name, fee::text AS fee, free_from::text AS free_from, eta, seq, is_active`,
			id, cid, in.Name, in.Fee, in.FreeFrom, in.ETA, in.Seq, in.IsActive)
	})
}

// DELETE /shop/zones/{id} (a zone already used by orders is switched off instead)
func (s *Server) shopDeleteZone(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE shop_zones SET is_active = false WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		if _, err := tx.Exec(ctx, `DELETE FROM shop_zones z WHERE z.id = $1 AND z.company_id = $2 AND NOT EXISTS (SELECT 1 FROM shop_orders o WHERE o.zone_id = z.id)`,
			id, cid); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ---------- products (what the shop shows) ----------

// GET /shop/products?q=&category=: the sellable items with their shop flags, price and stock in the shop.
func (s *Server) shopProducts(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	cat := r.URL.Query().Get("category_id")
	if len(q) > 80 || (cat != "" && !isUUID(cat)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadShop(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		wh, err := shopWarehouse(ctx, tx, cid, set)
		if err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT it.id, it.code, it.name, it.kind, it.is_template, it.image <> '' AS has_image, it.shop_visible, it.shop_description,
			cat.name AS category, it.sale_price::text AS sale_price, coalesce(it.sale_currency, c.base_currency) AS sale_currency,
			CASE WHEN it.is_template THEN (SELECT trim_scale(coalesce(sum(st2.quantity), 0))::text FROM items v JOIN stock st2 ON st2.item_id = v.id AND st2.warehouse_id = $2 WHERE v.template_id = it.id)
			     ELSE trim_scale(coalesce(st.quantity, 0))::text END AS stock,
			(it.id = ANY ($5::uuid[])) AS featured,
			(SELECT count(*) FROM items v WHERE v.template_id = it.id AND v.is_active) AS variants
			FROM items it JOIN companies c ON c.id = it.company_id LEFT JOIN item_categories cat ON cat.id = it.category_id
			LEFT JOIN stock st ON st.item_id = it.id AND st.warehouse_id = $2
			WHERE it.company_id = $1 AND it.is_active AND it.is_sellable AND it.template_id IS NULL
			  AND ($3 = '' OR it.name ILIKE '%' || $3 || '%' OR it.code ILIKE $3 || '%' OR it.barcode = $3)
			  AND ($4 = '' OR it.category_id = nullif($4, '')::uuid)
			ORDER BY it.name LIMIT 1000`, cid, wh, q, cat, set.Featured)
	})
}

// PATCH /shop/products/{id} {shop_visible, shop_description}: the item (and a template's variants) on / off the shop.
func (s *Server) shopPatchProduct(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Visible     *bool   `json:"shop_visible"`
		Description *string `json:"shop_description"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || len(deref(in.Description)) > 4000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE items SET shop_visible = coalesce($3, shop_visible), shop_description = coalesce($4, shop_description)
			WHERE id = $1 AND company_id = $2`, id, cid, in.Visible, in.Description)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		if in.Visible != nil {
			if _, err := tx.Exec(ctx, `UPDATE items SET shop_visible = $3 WHERE template_id = $1 AND company_id = $2`, id, cid, *in.Visible); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT id, shop_visible, shop_description FROM items WHERE id = $1`, id)
	})
}

// ---------- online orders ----------

const shopOrderCols = `o.id, o.number, o.token, o.sales_order_id, so.number AS quote_number, so.order_number, so.status AS order_status, o.customer_id,
	o.name, o.phone, o.email, o.address, o.zone_id, z.name AS zone, o.delivery, o.payment, o.coupon, o.currency, o.subtotal::text AS subtotal,
	o.discount::text AS discount, o.fee::text AS fee, so.total::text AS total, o.notes, o.status, o.created_at, ` + shopTrackState + ` AS state,
	(SELECT t.number FROM trip_stops ts JOIN delivery_trips t ON t.id = ts.trip_id WHERE ts.sales_order_id = so.id AND t.status <> 'cancelled' ORDER BY t.created_at DESC LIMIT 1) AS trip_number,
	(SELECT i.number FROM invoices i WHERE i.sales_order_id = so.id AND i.status <> 'void' ORDER BY i.created_at LIMIT 1) AS invoice_number,
	(SELECT json_build_object('id', sh.id, 'carrier', sh.carrier, 'tracking_no', sh.tracking_no, 'status', sh.status) FROM shipments sh
	  WHERE sh.shop_order_id = o.id AND sh.status <> 'cancelled' ORDER BY sh.created_at DESC LIMIT 1) AS shipment`

const shopOrderFrom = ` FROM shop_orders o JOIN sales_orders so ON so.id = o.sales_order_id LEFT JOIN shop_zones z ON z.id = o.zone_id`

// GET /shop/orders?state=&q=
func (s *Server) shopOrders(w http.ResponseWriter, r *http.Request) {
	state, q := r.URL.Query().Get("state"), strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 80 || (state != "" && !contains([]string{"received", "preparing", "on_the_way", "delivered", "cancelled", "open"}, state)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT * FROM (SELECT `+shopOrderCols+shopOrderFrom+` WHERE o.company_id = $1
			  AND ($3 = '' OR o.name ILIKE '%' || $3 || '%' OR o.phone LIKE '%' || $3 || '%' OR o.number::text = $3)) x
			WHERE ($2 = '' OR x.state = $2 OR ($2 = 'open' AND x.state NOT IN ('delivered', 'cancelled')))
			ORDER BY x.created_at DESC LIMIT 500`, r.PathValue("cid"), state, q)
	})
}

// GET /shop/orders/{id}: the online order with its sales order.
func (s *Server) shopGetOrder(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		o, err := queryJSONOne(ctx, tx, `SELECT `+shopOrderCols+shopOrderFrom+` WHERE o.id = $1 AND o.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		var so string
		if err := tx.QueryRow(ctx, `SELECT sales_order_id::text FROM shop_orders WHERE id = $1`, id).Scan(&so); err != nil {
			return nil, err
		}
		full, err := loadOrder(ctx, tx, cid, so)
		if err != nil {
			return nil, err
		}
		return map[string]any{"shop_order": o, "sales_order": full, "link": publicURL() + "/#/order/"}, nil
	})
}

// POST /shop/orders/{id}/{action}: confirm (the order waits for staff) or cancel.
func (s *Server) shopOrderAction(w http.ResponseWriter, r *http.Request) {
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if !isUUID(id) || (action != "confirm" && action != "cancel") {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if (action == "confirm" && !can(r, "sales.create")) || (action == "cancel" && !can(r, "sales.void")) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var so, status string
		if err := tx.QueryRow(ctx, `SELECT sales_order_id::text, status FROM shop_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&so, &status); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		switch action {
		case "confirm":
			if status != "new" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			if err := confirmOrderTx(ctx, tx, cid, so, today()); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE shop_orders SET status = 'confirmed', updated_at = now() WHERE id = $1`, id); err != nil {
				return nil, err
			}
		case "cancel":
			if status == "cancelled" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			var busy bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sales_order_lines WHERE order_id = $1 AND (qty_delivered > 0 OR qty_invoiced > 0))`, so).Scan(&busy); err != nil {
				return nil, err
			}
			if busy {
				return nil, &apiError{status: http.StatusConflict, code: "order_in_progress"}
			}
			if _, err := lockOrder(ctx, tx, cid, so); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE sales_orders SET status = 'cancelled' WHERE id = $1 AND status NOT IN ('cancelled', 'done')`, so); err != nil {
				return nil, err
			}
			if err := whOnOrderCancelled(ctx, tx, cid, so); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE shop_orders SET status = 'cancelled', updated_at = now() WHERE id = $1`, id); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+shopOrderCols+shopOrderFrom+` WHERE o.id = $1`, id)
	})
}
