package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Point of sale.
//
// Accounting design (kept deliberately simple and exact): every POS sale IS a normal customer invoice, issued to the
// register's walk-in customer or to a named customer, plus the till payment taken for it:
//
//	invoice      Dr receivables / Cr revenue (per item or register account) / Cr VAT
//	cost         Dr cost of sales / Cr inventory, at weighted average cost, out of the register's warehouse
//	payment      Dr cash boxes (each tender, any currency) / Cr change box / Cr receivables
//
// so the VAT report, sales and stock reports, customer statements and the receivables ageing include POS sales
// with no special case. A return is a credit note on that invoice (by line: the goods of those lines come back at
// the cost they left with) and, when money is given back, a refund paid out of a till box. An "on account" sale for a
// named customer leaves the unpaid part on the customer's account.
//
// A session (one shift on one register) only counts money: opening float + every cash movement of its orders =
// expected; at closing the cashier counts each box and the difference is posted to the register's over/short
// account (Dr/Cr the cash box). POS orders carry a client_ref so a till that was offline can send them later
// exactly once.

type posRegister struct {
	ID, Name, Currency, Warehouse, Customer, OverShort string
	Revenue                                            *string
	Methods                                            []string
	CashierRefunds, CashierPriceEdit, Active           bool
	Footer                                             string
}

func loadRegister(ctx context.Context, tx pgx.Tx, cid, id string) (*posRegister, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	g := &posRegister{}
	err := tx.QueryRow(ctx, `SELECT id::text, name, currency::text, warehouse_id::text, customer_id::text, over_short_account_id::text,
		revenue_account_id::text, method_ids::text[], cashier_refunds, cashier_price_edit, is_active, receipt_footer
		FROM pos_registers WHERE id = $1 AND company_id = $2`, id, cid).
		Scan(&g.ID, &g.Name, &g.Currency, &g.Warehouse, &g.Customer, &g.OverShort, &g.Revenue, &g.Methods, &g.CashierRefunds,
			&g.CashierPriceEdit, &g.Active, &g.Footer)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	return g, err
}

func (g *posRegister) hasMethod(id string) bool {
	for _, m := range g.Methods {
		if m == id {
			return true
		}
	}
	return false
}

func isCashier(r *http.Request) bool { return principal(r).Role == RoleCashier }

func isNotFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == http.StatusNotFound
}

// ensureOverShortAccount finds (or creates) the account that receives cash differences found when a till is closed.
func ensureOverShortAccount(ctx context.Context, tx pgx.Tx, cid string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE company_id = $1 AND code IN ('658', '6900') AND is_postable AND is_active
		ORDER BY code LIMIT 1`, cid).Scan(&id)
	if err == nil || err != pgx.ErrNoRows {
		return id, err
	}
	var parent *string
	_ = tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE company_id = $1 AND code = '6' AND type = 'expense'`, cid).Scan(&parent)
	err = tx.QueryRow(ctx, `INSERT INTO accounts (company_id, code, name, name_i18n, type, parent_id)
		VALUES ($1, '658', 'Cash over / short', '{"ar": "فروقات الصندوق (زيادة / نقص)", "fr": "Écarts de caisse"}', 'expense', $2)
		RETURNING id::text`, cid, parent).Scan(&id)
	return id, err
}

// ---------------- registers ----------------

const registerCols = `g.id, g.name, g.currency, g.warehouse_id, w.name AS warehouse, g.customer_id, c.name AS customer_name,
	g.method_ids, g.revenue_account_id, g.over_short_account_id, g.cashier_refunds, g.cashier_price_edit, g.receipt_footer, g.is_active,
	(SELECT json_build_object('id', s.id, 'number', s.number, 'opened_at', s.opened_at, 'opened_by', u.full_name)
	   FROM pos_sessions s LEFT JOIN users u ON u.id = s.opened_by WHERE s.register_id = g.id AND s.status = 'open') AS open_session`

func (s *Server) listRegisters(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+registerCols+` FROM pos_registers g JOIN warehouses w ON w.id = g.warehouse_id
			JOIN customers c ON c.id = g.customer_id WHERE g.company_id = $1 AND (g.is_active OR NOT $2) ORDER BY g.created_at`,
			r.PathValue("cid"), isCashier(r))
	})
}

func (s *Server) saveRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name               *string   `json:"name"`
		Currency           *string   `json:"currency"`
		WarehouseID        *string   `json:"warehouse_id"`
		CustomerID         *string   `json:"customer_id"`
		MethodIDs          *[]string `json:"method_ids"`
		RevenueAccountID   *string   `json:"revenue_account_id"` // "" = item / company revenue account
		OverShortAccountID *string   `json:"over_short_account_id"`
		CashierRefunds     *bool     `json:"cashier_refunds"`
		CashierPriceEdit   *bool     `json:"cashier_price_edit"`
		ReceiptFooter      *string   `json:"receipt_footer"`
		IsActive           *bool     `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Name == nil || strings.TrimSpace(*in.Name) == "" || in.Currency == nil)) ||
		(in.Name != nil && strings.TrimSpace(*in.Name) == "") || (in.Currency != nil && !currencyRe.MatchString(*in.Currency)) ||
		!optUUID(in.WarehouseID) || !optUUID(in.CustomerID) || !optUUID(in.RevenueAccountID) || !optUUID(in.OverShortAccountID) ||
		(in.ReceiptFooter != nil && len(*in.ReceiptFooter) > 500) || (!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.MethodIDs != nil {
		if len(*in.MethodIDs) == 0 || len(*in.MethodIDs) > 20 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		for _, m := range *in.MethodIDs {
			if !isUUID(m) {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
		}
	} else if create {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if in.MethodIDs != nil {
			methods, err := loadMethods(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			for _, m := range *in.MethodIDs {
				if mm, ok := methods[m]; !ok || !mm.Active {
					return nil, badRequest("invalid_tender")
				}
			}
		}
		for _, a := range []*string{in.RevenueAccountID, in.OverShortAccountID} {
			if a != nil && *a != "" {
				if err := requirePostable(ctx, tx, cid, *a); err != nil {
					return nil, err
				}
			}
		}
		if in.WarehouseID != nil && *in.WarehouseID != "" {
			if _, err := checkWarehouse(ctx, tx, cid, *in.WarehouseID); err != nil {
				return nil, err
			}
		}
		if in.CustomerID != nil && *in.CustomerID != "" {
			if _, err := lockCustomer(ctx, tx, cid, *in.CustomerID); err != nil {
				return nil, err
			}
		}
		if create {
			wh, err := checkWarehouse(ctx, tx, cid, deref(in.WarehouseID))
			if err != nil {
				return nil, err
			}
			cust := deref(in.CustomerID)
			if cust == "" {
				var n int64
				if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'customer')`, cid).Scan(&n); err != nil {
					return nil, err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO customers (company_id, code, name, notes) VALUES ($1, $2, $3, 'Point of sale')
					RETURNING id::text`, cid, strconv.FormatInt(n, 10), "Walk-in · "+strings.TrimSpace(*in.Name)).Scan(&cust); err != nil {
					return nil, err
				}
			}
			os := deref(in.OverShortAccountID)
			if os == "" {
				if os, err = ensureOverShortAccount(ctx, tx, cid); err != nil {
					return nil, err
				}
			}
			if err := tx.QueryRow(ctx, `INSERT INTO pos_registers (company_id, name, currency, warehouse_id, customer_id, method_ids,
				revenue_account_id, over_short_account_id, cashier_refunds, cashier_price_edit, receipt_footer)
				VALUES ($1, $2, $3, $4, $5, $6::uuid[], nullif($7, '')::uuid, $8, coalesce($9, false), coalesce($10, false), coalesce($11, ''))
				RETURNING id::text`, cid, strings.TrimSpace(*in.Name), *in.Currency, wh, cust, *in.MethodIDs, deref(in.RevenueAccountID), os,
				in.CashierRefunds, in.CashierPriceEdit, in.ReceiptFooter).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			var methods any
			if in.MethodIDs != nil {
				methods = *in.MethodIDs
			}
			tag, err := tx.Exec(ctx, `UPDATE pos_registers SET name = coalesce(trim($3), name), currency = coalesce($4, currency),
				warehouse_id = coalesce(nullif($5, '')::uuid, warehouse_id), customer_id = coalesce(nullif($6, '')::uuid, customer_id),
				method_ids = coalesce($7::uuid[], method_ids),
				revenue_account_id = CASE WHEN $8::text IS NULL THEN revenue_account_id ELSE nullif($8, '')::uuid END,
				over_short_account_id = coalesce(nullif($9, '')::uuid, over_short_account_id),
				cashier_refunds = coalesce($10, cashier_refunds), cashier_price_edit = coalesce($11, cashier_price_edit),
				receipt_footer = coalesce($12, receipt_footer), is_active = coalesce($13, is_active)
				WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.Currency, in.WarehouseID, in.CustomerID, methods, in.RevenueAccountID,
				in.OverShortAccountID, in.CashierRefunds, in.CashierPriceEdit, in.ReceiptFooter, in.IsActive)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+registerCols+` FROM pos_registers g JOIN warehouses w ON w.id = g.warehouse_id
			JOIN customers c ON c.id = g.customer_id WHERE g.id = $1`, id)
	})
}

// ---------------- categories ----------------

func (s *Server) listCategories(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT c.id, c.name, c.name_i18n, c.color, c.seq, c.is_active,
			(SELECT count(*) FROM items it WHERE it.category_id = c.id) AS items
			FROM item_categories c WHERE c.company_id = $1 ORDER BY c.seq, c.name`, r.PathValue("cid"))
	})
}

func (s *Server) saveCategory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     *string           `json:"name"`
		NameI18n map[string]string `json:"name_i18n"`
		Color    *string           `json:"color"`
		Seq      *int              `json:"seq"`
		IsActive *bool             `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Name == nil || strings.TrimSpace(*in.Name) == "")) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") ||
		(!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var i18n []byte
	if in.NameI18n != nil {
		i18n, _ = json.Marshal(in.NameI18n)
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if create {
			if _, err := companyBase(ctx, tx, cid); err != nil {
				return nil, err
			}
			if i18n == nil {
				i18n = []byte("{}")
			}
			if err := tx.QueryRow(ctx, `INSERT INTO item_categories (company_id, name, name_i18n, color, seq)
				VALUES ($1, trim($2), $3, coalesce(nullif($4, ''), 'teal'), coalesce($5, 0)) RETURNING id::text`,
				cid, *in.Name, i18n, deref(in.Color), in.Seq).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE item_categories SET name = coalesce(trim($3), name), name_i18n = coalesce($4::jsonb, name_i18n),
				color = coalesce(nullif($5, ''), color), seq = coalesce($6, seq), is_active = coalesce($7, is_active)
				WHERE id = $1 AND company_id = $2`, id, cid, in.Name, i18n, deref(in.Color), in.Seq, in.IsActive)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT id, name, name_i18n, color, seq, is_active FROM item_categories WHERE id = $1`, id)
	})
}

// ---------------- catalog (everything a till needs, cached for offline use) ----------------

type rateCache struct {
	ctx        context.Context
	tx         pgx.Tx
	cid, base  string
	date       string
	rates      map[string]decimal.Decimal
	currencies map[string]int32
}

func newRateCache(ctx context.Context, tx pgx.Tx, cid, base, date string) *rateCache {
	return &rateCache{ctx: ctx, tx: tx, cid: cid, base: base, date: date, rates: map[string]decimal.Decimal{}, currencies: map[string]int32{}}
}

func (c *rateCache) rate(cur string) (decimal.Decimal, error) {
	if v, ok := c.rates[cur]; ok {
		return v, nil
	}
	s, err := resolveRate(c.ctx, c.tx, c.cid, c.base, cur, c.date, "")
	if err != nil {
		return decimal.Zero, err
	}
	v, _ := decimal.NewFromString(s)
	c.rates[cur] = v
	return v, nil
}

func (c *rateCache) decimals(cur string) (int32, error) {
	if v, ok := c.currencies[cur]; ok {
		return v, nil
	}
	d, err := currencyDecimals(c.ctx, c.tx, cur)
	c.currencies[cur] = d
	return d, err
}

// convert price from one currency to another through the base currency (rates are units per 1 base).
func (c *rateCache) convert(v decimal.Decimal, from, to string) (decimal.Decimal, error) {
	if from == to {
		return v, nil
	}
	rf, err := c.rate(from)
	if err != nil {
		return decimal.Zero, err
	}
	rt, err := c.rate(to)
	if err != nil {
		return decimal.Zero, err
	}
	d, err := c.decimals(to)
	if err != nil {
		return decimal.Zero, err
	}
	if rf.IsZero() {
		return decimal.Zero, &apiError{status: http.StatusUnprocessableEntity, code: "no_rate", detail: from}
	}
	return v.Mul(rt).Div(rf).Round(d), nil
}

// itemPriceIn: the item's price list entry for the currency, else its main sale price converted at today's rate.
func itemPriceIn(rc *rateCache, listPrice *string, salePrice, saleCurrency, cur string) (decimal.Decimal, error) {
	if listPrice != nil {
		return decimal.NewFromString(*listPrice)
	}
	p, _ := decimal.NewFromString(salePrice)
	return rc.convert(p, saleCurrency, cur)
}

func (s *Server) posCatalog(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		g, err := loadRegister(ctx, tx, cid, r.URL.Query().Get("register_id"))
		if err != nil {
			return nil, err
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rc := newRateCache(ctx, tx, cid, set.Base, today())
		rows, err := tx.Query(ctx, `SELECT it.id::text, it.code, it.name, it.name_i18n::text, it.kind, it.unit, it.barcode,
			it.category_id::text, it.image, ip.price::text, it.sale_price::text, coalesce(it.sale_currency, $3)::text,
			t.id::text, trim_scale(t.rate)::text, t.name, coalesce(st.quantity, 0)::text
			FROM items it
			LEFT JOIN item_prices ip ON ip.item_id = it.id AND ip.currency = $2
			LEFT JOIN tax_rates t ON t.id = it.sale_tax_rate_id AND t.is_active AND t.applies_to IN ('sales', 'both')
			LEFT JOIN stock st ON st.item_id = it.id AND st.warehouse_id = $4
			WHERE it.company_id = $1 AND it.is_active AND it.is_sellable AND it.pos_visible
			ORDER BY it.name LIMIT 5000`, cid, g.Currency, set.Base, g.Warehouse)
		if err != nil {
			return nil, err
		}
		type item struct {
			ID         string          `json:"id"`
			Code       string          `json:"code"`
			Name       string          `json:"name"`
			NameI18n   json.RawMessage `json:"name_i18n"`
			Kind       string          `json:"kind"`
			Unit       string          `json:"unit"`
			Barcode    string          `json:"barcode"`
			CategoryID *string         `json:"category_id"`
			Image      string          `json:"image"`
			Price      string          `json:"price"`
			TaxID      *string         `json:"tax_rate_id"`
			TaxRate    *string         `json:"tax_rate"`
			TaxName    *string         `json:"tax_name"`
			Stock      string          `json:"stock"`
		}
		items := []item{}
		type raw struct {
			it                  item
			list                *string
			sale, saleCur, i18n string
		}
		raws := []raw{}
		for rows.Next() {
			var x raw
			if err := rows.Scan(&x.it.ID, &x.it.Code, &x.it.Name, &x.i18n, &x.it.Kind, &x.it.Unit, &x.it.Barcode, &x.it.CategoryID,
				&x.it.Image, &x.list, &x.sale, &x.saleCur, &x.it.TaxID, &x.it.TaxRate, &x.it.TaxName, &x.it.Stock); err != nil {
				rows.Close()
				return nil, err
			}
			x.it.NameI18n = json.RawMessage(x.i18n)
			raws = append(raws, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, x := range raws {
			p, err := itemPriceIn(rc, x.list, x.sale, x.saleCur, g.Currency)
			if err != nil {
				return nil, err
			}
			x.it.Price = p.String()
			items = append(items, x.it)
		}
		cats, err := queryJSON(ctx, tx, `SELECT id, name, name_i18n, color FROM item_categories WHERE company_id = $1 AND is_active
			ORDER BY seq, name`, cid)
		if err != nil {
			return nil, err
		}
		methods, err := queryJSON(ctx, tx, `SELECT m.id, m.name, m.kind, m.currency, m.change_rounding
			FROM payment_methods m WHERE m.company_id = $1 AND m.id = ANY($2::uuid[]) AND m.is_active
			ORDER BY array_position($2::uuid[], m.id)`, cid, g.Methods)
		if err != nil {
			return nil, err
		}
		rates, err := queryJSON(ctx, tx, `SELECT DISTINCT ON (currency) currency, trim_scale(rate)::text AS rate, rate_date FROM exchange_rates
			WHERE company_id = $1 AND kind = 'official' AND rate_date <= current_date ORDER BY currency, rate_date DESC`, cid)
		if err != nil {
			return nil, err
		}
		reg, err := queryJSONOne(ctx, tx, `SELECT `+registerCols+` FROM pos_registers g JOIN warehouses w ON w.id = g.warehouse_id
			JOIN customers c ON c.id = g.customer_id WHERE g.id = $1`, g.ID)
		if err != nil {
			return nil, err
		}
		company, err := queryJSONOne(ctx, tx, `SELECT name_en, name_ar, base_currency, local_currency, tax_id, doc_header, doc_footer
			FROM companies WHERE id = $1`, cid)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(items)
		return map[string]json.RawMessage{"register": reg, "items": b, "categories": cats, "methods": methods, "rates": rates,
			"company": company}, nil
	})
}

// ---------------- sessions ----------------

type posAmount struct {
	MethodID string `json:"method_id"`
	Amount   string `json:"amount"`
}

func (s *Server) openSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RegisterID string      `json:"register_id"`
		Opening    []posAmount `json:"opening"`
		Notes      string      `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(in.RegisterID) || len(in.Opening) > 20 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		g, err := loadRegister(ctx, tx, cid, in.RegisterID)
		if err != nil {
			return nil, err
		}
		if !g.Active {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		opening := []posAmount{}
		for _, o := range in.Opening {
			a, ok := money(o.Amount)
			if !ok || a.IsNegative() || !g.hasMethod(o.MethodID) {
				return nil, badRequest("invalid_tender")
			}
			if a.IsPositive() {
				opening = append(opening, posAmount{o.MethodID, a.String()})
			}
		}
		ob, _ := json.Marshal(opening)
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'pos_session')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var id string
		err = tx.QueryRow(ctx, `INSERT INTO pos_sessions (company_id, register_id, number, opening, notes) VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text`, cid, g.ID, n, ob, strings.TrimSpace(in.Notes)).Scan(&id)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "session_open"}
		}
		if err != nil {
			return nil, err
		}
		return loadSession(ctx, tx, cid, id)
	})
}

type drawerLine struct {
	MethodID   string `json:"method_id"`
	Name       string `json:"name"`
	Currency   string `json:"currency"`
	Opening    string `json:"opening"`
	In         string `json:"in"`
	Out        string `json:"out"`
	Expected   string `json:"expected"`
	Counted    string `json:"counted,omitempty"`
	Difference string `json:"difference,omitempty"`
}

// drawer: per box of the register, opening float + money in / out of the session's orders = expected.
func drawer(ctx context.Context, tx pgx.Tx, cid, sessionID string) ([]drawerLine, error) {
	var regID, openingS string
	if err := tx.QueryRow(ctx, `SELECT register_id::text, opening::text FROM pos_sessions WHERE id = $1 AND company_id = $2`, sessionID, cid).
		Scan(&regID, &openingS); err != nil {
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		return nil, err
	}
	g, err := loadRegister(ctx, tx, cid, regID)
	if err != nil {
		return nil, err
	}
	var opening []posAmount
	_ = json.Unmarshal([]byte(openingS), &opening)
	open := map[string]decimal.Decimal{}
	for _, o := range opening {
		v, _ := decimal.NewFromString(o.Amount)
		open[o.MethodID] = open[o.MethodID].Add(v)
	}
	rows, err := tx.Query(ctx, `SELECT c->>'method_id', coalesce(sum((c->>'amount')::numeric) FILTER (WHERE (c->>'amount')::numeric > 0), 0)::text,
		coalesce(-sum((c->>'amount')::numeric) FILTER (WHERE (c->>'amount')::numeric < 0), 0)::text
		FROM pos_orders o, jsonb_array_elements(o.cash) c WHERE o.session_id = $1 GROUP BY 1`, sessionID)
	if err != nil {
		return nil, err
	}
	ins, outs := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	for rows.Next() {
		var m, a, b string
		if err := rows.Scan(&m, &a, &b); err != nil {
			rows.Close()
			return nil, err
		}
		ins[m], _ = decimal.NewFromString(a)
		outs[m], _ = decimal.NewFromString(b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	methods, err := loadMethods(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	// boxes of the register, plus any box an order used that was removed from the register since
	ids := append([]string{}, g.Methods...)
	for m := range ins {
		if !g.hasMethod(m) {
			ids = append(ids, m)
		}
	}
	for m := range outs {
		if _, ok := ins[m]; !ok && !g.hasMethod(m) {
			ids = append(ids, m)
		}
	}
	out := []drawerLine{}
	for _, id := range ids {
		m, ok := methods[id]
		if !ok {
			continue
		}
		exp := open[id].Add(ins[id]).Sub(outs[id])
		out = append(out, drawerLine{MethodID: id, Name: m.Name, Currency: m.Currency, Opening: open[id].String(),
			In: ins[id].String(), Out: outs[id].String(), Expected: exp.String()})
	}
	return out, nil
}

func loadSession(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	sess, err := queryJSONOne(ctx, tx, `SELECT s.id, s.number, s.status, s.register_id, g.name AS register_name, g.currency,
		s.opened_at, ou.full_name AS opened_by, s.closed_at, cu.full_name AS closed_by, s.opening, s.counted, s.over_short_entry_id, s.notes,
		co.name_en AS company_name, co.name_ar AS company_name_ar
		FROM pos_sessions s JOIN pos_registers g ON g.id = s.register_id JOIN companies co ON co.id = s.company_id
		LEFT JOIN users ou ON ou.id = s.opened_by LEFT JOIN users cu ON cu.id = s.closed_by
		WHERE s.id = $1 AND s.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	var status, countedS string
	if err := tx.QueryRow(ctx, `SELECT status, counted::text FROM pos_sessions WHERE id = $1`, id).Scan(&status, &countedS); err != nil {
		return nil, err
	}
	var lines []drawerLine
	if status == "closed" {
		_ = json.Unmarshal([]byte(countedS), &lines)
	} else if lines, err = drawer(ctx, tx, cid, id); err != nil {
		return nil, err
	}
	dl, _ := json.Marshal(lines)
	// Z report: sales / refunds / tax per currency, money by box, items sold and returned, per cashier
	summary, err := queryJSON(ctx, tx, `SELECT currency, count(*) FILTER (WHERE kind = 'sale') AS sales,
		count(*) FILTER (WHERE kind = 'refund') AS refunds,
		coalesce(sum(total) FILTER (WHERE kind = 'sale'), 0)::text AS sales_total,
		coalesce(sum(total) FILTER (WHERE kind = 'refund'), 0)::text AS refunds_total,
		(coalesce(sum(tax_total) FILTER (WHERE kind = 'sale'), 0) - coalesce(sum(tax_total) FILTER (WHERE kind = 'refund'), 0))::text AS tax,
		(coalesce(sum(total) FILTER (WHERE kind = 'sale'), 0) - coalesce(sum(total) FILTER (WHERE kind = 'refund'), 0))::text AS net,
		coalesce(sum(total - paid) FILTER (WHERE kind = 'sale'), 0)::text AS on_account
		FROM pos_orders WHERE session_id = $1 GROUP BY currency ORDER BY currency`, id)
	if err != nil {
		return nil, err
	}
	items, err := queryJSON(ctx, tx, `SELECT description, item_id, sum(qty)::text AS quantity, sum(amount)::text AS amount, currency FROM (
		  SELECT l.description, l.item_id, l.quantity AS qty, l.amount + l.tax_amount AS amount, o.currency
		  FROM pos_orders o JOIN invoice_lines l ON l.invoice_id = o.invoice_id WHERE o.session_id = $1 AND o.kind = 'sale'
		  UNION ALL
		  SELECT l.description, l.item_id, -cl.quantity, -(cl.amount + cl.tax_amount), o.currency
		  FROM pos_orders o JOIN credit_note_lines cl ON cl.payment_id = o.payment_id JOIN invoice_lines l ON l.id = cl.invoice_line_id
		  WHERE o.session_id = $1 AND o.kind = 'refund'
		) x GROUP BY description, item_id, currency ORDER BY sum(amount) DESC`, id)
	if err != nil {
		return nil, err
	}
	cashiers, err := queryJSON(ctx, tx, `SELECT u.full_name AS cashier, o.currency, count(*) FILTER (WHERE o.kind = 'sale') AS sales,
		(coalesce(sum(o.total) FILTER (WHERE o.kind = 'sale'), 0) - coalesce(sum(o.total) FILTER (WHERE o.kind = 'refund'), 0))::text AS net
		FROM pos_orders o LEFT JOIN users u ON u.id = o.cashier_id WHERE o.session_id = $1 GROUP BY u.full_name, o.currency ORDER BY 1`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"session": sess, "drawer": dl, "summary": summary, "items": items, "cashiers": cashiers}, nil
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadSession(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	reg, status := q.Get("register_id"), q.Get("status")
	if (reg != "" && !isUUID(reg)) || (status != "" && status != "open" && status != "closed") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	limit := queryInt(r, "limit", 100, 1000)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT s.id, s.number, s.status, s.register_id, g.name AS register_name, g.currency, s.opened_at,
			ou.full_name AS opened_by, s.closed_at, cu.full_name AS closed_by, s.counted,
			(SELECT count(*) FROM pos_orders o WHERE o.session_id = s.id AND o.kind = 'sale') AS sales,
			(SELECT coalesce(sum(CASE WHEN o.kind = 'sale' THEN o.total ELSE -o.total END), 0) FROM pos_orders o WHERE o.session_id = s.id)::text AS net
			FROM pos_sessions s JOIN pos_registers g ON g.id = s.register_id
			LEFT JOIN users ou ON ou.id = s.opened_by LEFT JOIN users cu ON cu.id = s.closed_by
			WHERE s.company_id = $1 AND ($2 = '' OR s.register_id = $2::uuid) AND ($3 = '' OR s.status = $3)
			ORDER BY s.opened_at DESC LIMIT $4`, r.PathValue("cid"), reg, status, limit)
	})
}

func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Counted []posAmount `json:"counted"`
		Notes   string      `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || len(in.Counted) > 40 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, regID string
		var number int64
		err := tx.QueryRow(ctx, `SELECT status, register_id::text, number FROM pos_sessions WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&status, &regID, &number)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "open" {
			return nil, &apiError{status: http.StatusConflict, code: "session_closed"}
		}
		g, err := loadRegister(ctx, tx, cid, regID)
		if err != nil {
			return nil, err
		}
		counted := map[string]decimal.Decimal{}
		for _, c := range in.Counted {
			v, ok := money(c.Amount)
			if !ok || v.IsNegative() {
				return nil, badRequest("invalid_amount")
			}
			counted[c.MethodID] = v
		}
		lines, err := drawer(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		methods, err := loadMethods(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		jl := []jline{}
		for i, l := range lines {
			exp, _ := decimal.NewFromString(l.Expected)
			c, ok := counted[l.MethodID]
			if !ok {
				c = exp // a box not counted (e.g. card / bank) is taken as expected
			}
			d := c.Sub(exp)
			lines[i].Counted, lines[i].Difference = c.String(), d.String()
			m := methods[l.MethodID]
			desc := fmt.Sprintf("Till %s · session #%d · %s", g.Name, number, m.Name)
			if d.IsPositive() {
				jl = append(jl, jline{Account: m.Account, Desc: desc, Currency: m.Currency, Debit: d},
					jline{Account: g.OverShort, Desc: desc, Currency: m.Currency, Credit: d})
			} else if d.IsNegative() {
				jl = append(jl, jline{Account: g.OverShort, Desc: desc, Currency: m.Currency, Debit: d.Neg()},
					jline{Account: m.Account, Desc: desc, Currency: m.Currency, Credit: d.Neg()})
			}
		}
		var entry *string
		if len(jl) > 0 {
			je, err := postJournal(ctx, tx, cid, set, today(), fmt.Sprintf("Cash over / short · %s · session #%d", g.Name, number),
				fmt.Sprintf("POS-S%d", number), "pos", jl)
			if err != nil {
				return nil, err
			}
			entry = &je
		}
		cb, _ := json.Marshal(lines)
		if _, err := tx.Exec(ctx, `UPDATE pos_sessions SET status = 'closed', closed_by = app_user(), closed_at = now(), counted = $2,
			over_short_entry_id = $3, notes = CASE WHEN $4 = '' THEN notes ELSE trim(notes || ' ' || $4) END WHERE id = $1`,
			id, cb, entry, strings.TrimSpace(in.Notes)); err != nil {
			return nil, err
		}
		return loadSession(ctx, tx, cid, id)
	})
}

// ---------------- orders ----------------

type posLineIn struct {
	ItemID      string `json:"item_id"`
	Quantity    string `json:"quantity"`
	UnitPrice   string `json:"unit_price"`   // "" = catalog price (changing it needs price-edit rights)
	DiscountPct string `json:"discount_pct"` // 0..100 (needs price-edit rights)
}

func (s *Server) createPosOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientRef      string      `json:"client_ref"`
		ClientTime     string      `json:"client_time"`
		SessionID      string      `json:"session_id"`
		CustomerID     string      `json:"customer_id"` // "" = the register's walk-in customer
		Lines          []posLineIn `json:"lines"`
		Tenders        []tenderIn  `json:"tenders"`
		ChangeMethodID string      `json:"change_method_id"`
		OnAccount      bool        `json:"on_account"` // named customer: the unpaid part stays on their account
		Notes          string      `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.ClientRef = strings.TrimSpace(in.ClientRef)
	if !isUUID(in.SessionID) || len(in.Lines) == 0 || len(in.Lines) > 200 || len(in.Tenders) > 10 ||
		(in.CustomerID != "" && !isUUID(in.CustomerID)) || (in.ChangeMethodID != "" && !isUUID(in.ChangeMethodID)) ||
		(in.ClientRef != "" && (len(in.ClientRef) < 8 || len(in.ClientRef) > 64)) || len(in.Notes) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var clientTime *time.Time
	if in.ClientTime != "" {
		if t, err := time.Parse(time.RFC3339, in.ClientTime); err == nil {
			clientTime = &t
		}
	}
	cid := r.PathValue("cid")
	cashier := isCashier(r)
	me := principal(r).ID
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.ClientRef != "" {
			var existing string
			err := tx.QueryRow(ctx, `SELECT id::text FROM pos_orders WHERE company_id = $1 AND client_ref = $2`, cid, in.ClientRef).Scan(&existing)
			if err == nil {
				return loadPosOrder(ctx, tx, cid, existing) // sent again by a till that was offline
			}
			if err != pgx.ErrNoRows {
				return nil, err
			}
		}
		g, err := lockOpenSession(ctx, tx, cid, in.SessionID)
		if err != nil {
			return nil, err
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if err := set.requireAR(); err != nil {
			return nil, err
		}
		rev := ""
		if g.Revenue != nil {
			rev = *g.Revenue
		} else if set.Revenue != nil {
			rev = *set.Revenue
		}
		if rev == "" {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "revenue"}
		}
		customer := g.Customer
		if in.CustomerID != "" {
			customer = in.CustomerID
		}
		walkIn := customer == g.Customer
		if walkIn && in.OnAccount {
			return nil, badRequest("on_account_needs_customer")
		}
		name, err := lockCustomer(ctx, tx, cid, customer)
		if err != nil {
			return nil, err
		}
		date := today()
		rc := newRateCache(ctx, tx, cid, set.Base, date)
		dec, err := rc.decimals(g.Currency)
		if err != nil {
			return nil, err
		}
		canEdit := !cashier || g.CashierPriceEdit
		lines := []invLine{}
		sales := []saleLine{}
		for _, l := range in.Lines {
			q, okq := money(l.Quantity)
			if !okq || !q.IsPositive() || !isUUID(l.ItemID) {
				return nil, badRequest("invalid_line")
			}
			var name, kind string
			var listPrice, taxID, itRev *string
			var salePrice, saleCur string
			var sellable bool
			err := tx.QueryRow(ctx, `SELECT it.name, it.kind, ip.price::text, it.sale_price::text, coalesce(it.sale_currency, $4)::text,
				it.sale_tax_rate_id::text, it.revenue_account_id::text, it.is_sellable AND it.is_active
				FROM items it LEFT JOIN item_prices ip ON ip.item_id = it.id AND ip.currency = $3
				WHERE it.id = $1 AND it.company_id = $2`, l.ItemID, cid, g.Currency, set.Base).
				Scan(&name, &kind, &listPrice, &salePrice, &saleCur, &taxID, &itRev, &sellable)
			if err == pgx.ErrNoRows || (err == nil && !sellable) {
				return nil, badRequest("invalid_item")
			}
			if err != nil {
				return nil, err
			}
			// a named customer's price list (best quantity break in the till's currency) wins over the catalog price
			var plPrice string
			if !walkIn {
				if err := tx.QueryRow(ctx, `SELECT x.price::text FROM customers c JOIN price_lists p ON p.id = c.price_list_id AND p.is_active
					JOIN price_list_items x ON x.list_id = p.id AND x.item_id = $2
					WHERE c.id = $1 AND p.currency = $3 AND x.min_qty <= $4::numeric ORDER BY x.min_qty DESC LIMIT 1`,
					customer, l.ItemID, g.Currency, q.String()).Scan(&plPrice); err != nil && err != pgx.ErrNoRows {
					return nil, err
				}
			}
			if plPrice != "" {
				listPrice = &plPrice
			}
			price, err := itemPriceIn(rc, listPrice, salePrice, saleCur, g.Currency)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(l.UnitPrice) != "" {
				p, ok := money(l.UnitPrice)
				if !ok || p.IsNegative() {
					return nil, badRequest("invalid_amount")
				}
				if !p.Equal(price) && !canEdit {
					return nil, &apiError{status: http.StatusForbidden, code: "price_edit_forbidden"}
				}
				price = p
			}
			disc := decimal.Zero
			if strings.TrimSpace(l.DiscountPct) != "" {
				d, ok := money(l.DiscountPct)
				if !ok || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(100)) {
					return nil, badRequest("invalid_amount")
				}
				if d.IsPositive() && !canEdit {
					return nil, &apiError{status: http.StatusForbidden, code: "price_edit_forbidden"}
				}
				disc = d
			}
			amt := q.Mul(price).Mul(decimal.NewFromInt(100).Sub(disc)).Div(decimal.NewFromInt(100)).Round(dec)
			tax, err := loadTaxRate(ctx, tx, cid, deref(taxID), "sales")
			if err != nil {
				tax = nil // tax rate switched off since: sell without it
			}
			desc := name
			if disc.IsPositive() {
				desc = fmt.Sprintf("%s (-%s%%)", name, disc.String())
			}
			lineRev := ""
			if itRev != nil && g.Revenue == nil {
				lineRev = *itRev
			}
			itemID := l.ItemID
			il := invLine{Kind: "other", Description: desc, Quantity: q, UnitPrice: price, Amount: amt, Tax: tax,
				TaxAmount: taxOn(amt, tax, dec), Revenue: lineRev, ItemID: &itemID}
			if kind == "stock" || kind == "kit" { // a kit takes its components out of stock
				wh := g.Warehouse
				il.WarehouseID = &wh
				sales = append(sales, saleLine{ItemID: l.ItemID, WarehouseID: wh, Qty: q})
			}
			lines = append(lines, il)
		}
		qctx := quietCtx(ctx)
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'pos_order')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		notes := strings.TrimSpace(fmt.Sprintf("POS %s #%d %s", g.Name, number, strings.TrimSpace(in.Notes)))
		invID, invNo, err := issueInvoiceFor(qctx, tx, cid, set, customer, name, nil, nil, nil, date, date, g.Currency, rev, notes, lines)
		if err != nil {
			return nil, err
		}
		if err := postSaleCOGS(ctx, tx, cid, set, invID, invNo, date, sales); err != nil {
			return nil, err
		}
		var totalS string
		if err := tx.QueryRow(ctx, `SELECT total::text FROM invoices WHERE id = $1`, invID).Scan(&totalS); err != nil {
			return nil, err
		}
		total, _ := decimal.NewFromString(totalS)
		taxTotal := decimal.Zero
		for _, l := range lines {
			taxTotal = taxTotal.Add(l.TaxAmount)
		}
		var paymentID *string
		paid := decimal.Zero
		cash := []map[string]string{}
		if len(in.Tenders) == 0 {
			if !in.OnAccount {
				return nil, badRequest("payment_required")
			}
		} else {
			for _, t := range in.Tenders {
				if !g.hasMethod(t.MethodID) {
					return nil, badRequest("invalid_tender")
				}
			}
			if in.ChangeMethodID != "" && !g.hasMethod(in.ChangeMethodID) {
				return nil, badRequest("change_method_required")
			}
			pin := paymentIn{CustomerID: customer, Date: date, Currency: g.Currency, Tenders: in.Tenders, ChangeMethodID: in.ChangeMethodID,
				Notes: fmt.Sprintf("POS %s #%d", g.Name, number)}
			if in.OnAccount {
				pin.upTo = total
			} else {
				pin.Amount = total.String()
			}
			pid, err := receivePayment(qctx, tx, cid, pin, false, me)
			if err != nil {
				return nil, err
			}
			paymentID = &pid
			var paidS string
			var chM, chCur *string
			var chAmt string
			if err := tx.QueryRow(ctx, `SELECT amount::text, change_method_id::text, change_currency::text, change_amount::text FROM payments WHERE id = $1`,
				pid).Scan(&paidS, &chM, &chCur, &chAmt); err != nil {
				return nil, err
			}
			paid, _ = decimal.NewFromString(paidS)
			rows, err := tx.Query(ctx, `SELECT method_id::text, currency::text, amount::text FROM payment_tenders WHERE payment_id = $1`, pid)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var m, c, a string
				if err := rows.Scan(&m, &c, &a); err != nil {
					rows.Close()
					return nil, err
				}
				cash = append(cash, map[string]string{"method_id": m, "currency": c, "amount": a})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			if ch, _ := decimal.NewFromString(chAmt); ch.IsPositive() && chM != nil {
				cash = append(cash, map[string]string{"method_id": *chM, "currency": deref(chCur), "amount": ch.Neg().String()})
			}
		}
		if walkIn && paid.LessThan(total) {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "insufficient_payment", detail: total.String() + " " + g.Currency}
		}
		cb, _ := json.Marshal(cash)
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO pos_orders (company_id, session_id, register_id, number, kind, order_date, customer_id, currency,
			total, tax_total, paid, invoice_id, payment_id, cash, client_ref, client_time, notes)
			VALUES ($1, $2, $3, $4, 'sale', $5::date, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11, $12, $13, nullif($14, ''), $15, $16)
			RETURNING id::text`, cid, in.SessionID, g.ID, number, date, customer, g.Currency, total.String(), taxTotal.String(), paid.String(),
			invID, paymentID, cb, in.ClientRef, clientTime, strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		return loadPosOrder(ctx, tx, cid, id)
	})
}

// lockOpenSession checks that the session is open (and keeps it open until this transaction ends).
func lockOpenSession(ctx context.Context, tx pgx.Tx, cid, sessionID string) (*posRegister, error) {
	var status, regID string
	err := tx.QueryRow(ctx, `SELECT status, register_id::text FROM pos_sessions WHERE id = $1 AND company_id = $2 FOR SHARE`, sessionID, cid).
		Scan(&status, &regID)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	if status != "open" {
		return nil, &apiError{status: http.StatusConflict, code: "session_closed"}
	}
	return loadRegister(ctx, tx, cid, regID)
}

func (s *Server) refundPosOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientRef string         `json:"client_ref"`
		SessionID string         `json:"session_id"`
		Lines     []creditLineIn `json:"lines"`     // empty = the whole order
		MethodID  string         `json:"method_id"` // box the money is paid back from ("" = keep as customer credit)
		Reason    string         `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.ClientRef = strings.TrimSpace(in.ClientRef)
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !isUUID(in.SessionID) || (in.MethodID != "" && !isUUID(in.MethodID)) || len(in.Lines) > 200 ||
		(in.ClientRef != "" && (len(in.ClientRef) < 8 || len(in.ClientRef) > 64)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cashier := isCashier(r)
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.ClientRef != "" {
			var existing string
			err := tx.QueryRow(ctx, `SELECT id::text FROM pos_orders WHERE company_id = $1 AND client_ref = $2`, cid, in.ClientRef).Scan(&existing)
			if err == nil {
				return loadPosOrder(ctx, tx, cid, existing)
			}
			if err != pgx.ErrNoRows {
				return nil, err
			}
		}
		g, err := lockOpenSession(ctx, tx, cid, in.SessionID)
		if err != nil {
			return nil, err
		}
		if cashier && !g.CashierRefunds {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		if in.MethodID != "" && !g.hasMethod(in.MethodID) {
			return nil, badRequest("invalid_tender")
		}
		var kind, customer, currency string
		var invID *string
		var number int64
		err = tx.QueryRow(ctx, `SELECT kind, customer_id::text, currency::text, invoice_id::text, number FROM pos_orders
			WHERE id = $1 AND company_id = $2`, id, cid).Scan(&kind, &customer, &currency, &invID, &number)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if kind != "sale" || invID == nil {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		reason := strings.TrimSpace(in.Reason)
		if reason == "" {
			reason = fmt.Sprintf("POS return · order #%d", number)
		}
		cn, err := creditInvoice(quietCtx(ctx), tx, cid, set, *invID, today(), reason, "", in.Lines, in.MethodID)
		if err != nil {
			return nil, err
		}
		cash := []map[string]string{}
		paid := decimal.Zero
		var refundDoc *string
		if cn.refundID != "" {
			refundDoc = &cn.refundID
			cash = append(cash, map[string]string{"method_id": in.MethodID, "currency": cn.refundCur, "amount": cn.refundPaid.Neg().String()})
			var t string
			if err := tx.QueryRow(ctx, `SELECT total::text FROM invoices WHERE id = $1`, cn.refundID).Scan(&t); err != nil {
				return nil, err
			}
			paid, _ = decimal.NewFromString(t)
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'pos_order')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		cb, _ := json.Marshal(cash)
		var rid string
		if err := tx.QueryRow(ctx, `INSERT INTO pos_orders (company_id, session_id, register_id, number, kind, order_date, customer_id, currency,
			total, tax_total, paid, invoice_id, payment_id, refund_doc_id, refund_of, cash, client_ref, notes)
			VALUES ($1, $2, $3, $4, 'refund', current_date, $5, $6, $7::numeric, $8::numeric, $9::numeric, $10, $11, $12, $13, $14, nullif($15, ''), $16)
			RETURNING id::text`, cid, in.SessionID, g.ID, n, customer, currency, cn.amount.String(), cn.tax.String(), paid.String(),
			*invID, cn.id, refundDoc, id, cb, in.ClientRef, reason).Scan(&rid); err != nil {
			return nil, err
		}
		return loadPosOrder(ctx, tx, cid, rid)
	})
}

const posOrderCols = `o.id, o.number, o.kind, o.order_date, o.session_id, s.number AS session_number, o.register_id, g.name AS register_name,
	o.customer_id, c.name AS customer_name, c.code AS customer_code, (o.customer_id = g.customer_id) AS walk_in,
	o.currency, o.total::text AS total, o.tax_total::text AS tax_total, o.paid::text AS paid, (o.total - o.paid)::text AS on_account,
	o.invoice_id, (SELECT i.number FROM invoices i WHERE i.id = o.invoice_id) AS invoice_number, o.payment_id, o.refund_doc_id, o.refund_of,
	(SELECT r.number FROM pos_orders r WHERE r.id = o.refund_of) AS refund_of_number, o.cash, o.client_ref, o.notes,
	u.full_name AS cashier, o.created_at`

const posOrderFrom = ` FROM pos_orders o JOIN pos_sessions s ON s.id = o.session_id JOIN pos_registers g ON g.id = o.register_id
	JOIN customers c ON c.id = o.customer_id LEFT JOIN users u ON u.id = o.cashier_id`

func loadPosOrder(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	order, err := queryJSONOne(ctx, tx, `SELECT `+posOrderCols+`, co.name_en AS company_name, co.name_ar AS company_name_ar,
		co.tax_id AS company_tax_id, co.doc_header AS company_header, g.receipt_footer`+posOrderFrom+`
		JOIN companies co ON co.id = o.company_id WHERE o.id = $1 AND o.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	var kind string
	var paymentID *string
	if err := tx.QueryRow(ctx, `SELECT kind, payment_id::text FROM pos_orders WHERE id = $1`, id).Scan(&kind, &paymentID); err != nil {
		return nil, err
	}
	var lines json.RawMessage
	if kind == "sale" {
		lines, err = queryJSON(ctx, tx, `SELECT l.line_no, l.item_id, l.description, l.quantity::text AS quantity, l.unit_price::text AS unit_price,
			l.amount::text AS amount, l.tax_amount::text AS tax_amount,
			(SELECT coalesce(sum(cl.quantity), 0) FROM credit_note_lines cl JOIN payments p ON p.id = cl.payment_id AND p.status = 'posted'
			  WHERE cl.invoice_line_id = l.id)::text AS returned
			FROM invoice_lines l JOIN pos_orders o ON o.invoice_id = l.invoice_id WHERE o.id = $1 ORDER BY l.line_no`, id)
	} else {
		lines, err = queryJSON(ctx, tx, `SELECT l.line_no, l.item_id, l.description, cl.quantity::text AS quantity, l.unit_price::text AS unit_price,
			cl.amount::text AS amount, cl.tax_amount::text AS tax_amount
			FROM credit_note_lines cl JOIN invoice_lines l ON l.id = cl.invoice_line_id WHERE cl.payment_id = $1 ORDER BY l.line_no`, paymentID)
	}
	if err != nil {
		return nil, err
	}
	tenders := json.RawMessage("[]")
	change := json.RawMessage("null")
	if kind == "sale" && paymentID != nil {
		if tenders, err = queryJSON(ctx, tx, `SELECT m.name AS method, t.method_id, t.currency, t.amount::text AS amount, trim_scale(t.rate)::text AS rate
			FROM payment_tenders t JOIN payment_methods m ON m.id = t.method_id WHERE t.payment_id = $1`, *paymentID); err != nil {
			return nil, err
		}
		if change, err = queryJSONOne(ctx, tx, `SELECT p.change_currency AS currency, p.change_amount::text AS amount, m.name AS method
			FROM payments p LEFT JOIN payment_methods m ON m.id = p.change_method_id WHERE p.id = $1 AND p.change_amount > 0`, *paymentID); err != nil {
			if !isNotFound(err) {
				return nil, err
			}
			change = json.RawMessage("null")
		}
	}
	return map[string]json.RawMessage{"order": order, "lines": lines, "tenders": tenders, "change": change}, nil
}

func (s *Server) getPosOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadPosOrder(ctx, tx, r.PathValue("cid"), id)
	})
}

func (s *Server) listPosOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sess, search := q.Get("session_id"), strings.TrimSpace(q.Get("q"))
	from, to := q.Get("from"), q.Get("to")
	if from == "" {
		from = "0001-01-01"
	}
	if to == "" {
		to = "9999-12-31"
	}
	if (sess != "" && !isUUID(sess)) || !validDate(from) || !validDate(to) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	limit := queryInt(r, "limit", 200, 2000)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+posOrderCols+posOrderFrom+`
			WHERE o.company_id = $1 AND ($2 = '' OR o.session_id = $2::uuid) AND o.order_date BETWEEN $3::date AND $4::date
			  AND ($5 = '' OR o.number::text = $5 OR c.name ILIKE '%' || $5 || '%')
			ORDER BY o.created_at DESC LIMIT $6`, r.PathValue("cid"), sess, from, to, search, limit)
	})
}

// posQuickCustomer: a cashier adds a customer at the till (name + phone).
func (s *Server) posQuickCustomer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || len(in.Phone) > 40 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'customer')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO customers (company_id, code, name, phone) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			cid, strconv.FormatInt(n, 10), strings.TrimSpace(in.Name), strings.TrimSpace(in.Phone)).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, code, name, phone FROM customers WHERE id = $1`, id)
	})
}

// ---------------- report ----------------

func (s *Server) posReport(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	group := r.URL.Query().Get("group")
	if group == "" {
		group = "day"
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	sums := `o.currency, count(*) FILTER (WHERE o.kind = 'sale') AS sales, count(*) FILTER (WHERE o.kind = 'refund') AS refunds,
		coalesce(sum(o.total) FILTER (WHERE o.kind = 'sale'), 0)::text AS sales_total,
		coalesce(sum(o.total) FILTER (WHERE o.kind = 'refund'), 0)::text AS refunds_total,
		(coalesce(sum(o.tax_total) FILTER (WHERE o.kind = 'sale'), 0) - coalesce(sum(o.tax_total) FILTER (WHERE o.kind = 'refund'), 0))::text AS tax,
		(coalesce(sum(o.total) FILTER (WHERE o.kind = 'sale'), 0) - coalesce(sum(o.total) FILTER (WHERE o.kind = 'refund'), 0))::text AS net`
	where := ` WHERE o.company_id = $1 AND o.order_date BETWEEN $2::date AND $3::date`
	var sql string
	switch group {
	case "day":
		sql = `SELECT o.order_date AS key, ` + sums + posOrderFrom + where + ` GROUP BY o.order_date, o.currency ORDER BY o.order_date DESC, o.currency`
	case "session":
		sql = `SELECT '#' || s.number || ' · ' || g.name AS key, min(s.opened_at) AS opened_at, ` + sums + posOrderFrom + where +
			` GROUP BY s.number, g.name, o.currency ORDER BY s.number DESC, o.currency`
	case "cashier":
		sql = `SELECT coalesce(u.full_name, '?') AS key, ` + sums + posOrderFrom + where + ` GROUP BY u.full_name, o.currency ORDER BY 1, o.currency`
	case "register":
		sql = `SELECT g.name AS key, ` + sums + posOrderFrom + where + ` GROUP BY g.name, o.currency ORDER BY 1, o.currency`
	case "product":
		sql = `SELECT description AS key, currency, sum(qty) FILTER (WHERE qty > 0)::text AS sold, coalesce(-sum(qty) FILTER (WHERE qty < 0), 0)::text AS returned,
			sum(qty)::text AS quantity, sum(amount)::text AS net FROM (
			  SELECT coalesce(it.name, l.description) AS description, l.quantity AS qty, l.amount + l.tax_amount AS amount, o.currency
			  FROM pos_orders o JOIN invoice_lines l ON l.invoice_id = o.invoice_id LEFT JOIN items it ON it.id = l.item_id` + where + ` AND o.kind = 'sale'
			  UNION ALL
			  SELECT coalesce(it.name, l.description), -cl.quantity, -(cl.amount + cl.tax_amount), o.currency
			  FROM pos_orders o JOIN credit_note_lines cl ON cl.payment_id = o.payment_id JOIN invoice_lines l ON l.id = cl.invoice_line_id
			  LEFT JOIN items it ON it.id = l.item_id` + where + ` AND o.kind = 'refund'
			) x GROUP BY description, currency ORDER BY sum(amount) DESC`
	case "method":
		sql = `SELECT m.name AS key, c->>'currency' AS currency,
			coalesce(sum((c->>'amount')::numeric) FILTER (WHERE (c->>'amount')::numeric > 0), 0)::text AS received,
			coalesce(-sum((c->>'amount')::numeric) FILTER (WHERE (c->>'amount')::numeric < 0), 0)::text AS paid_out,
			sum((c->>'amount')::numeric)::text AS net
			FROM pos_orders o CROSS JOIN LATERAL jsonb_array_elements(o.cash) c JOIN payment_methods m ON m.id = (c->>'method_id')::uuid` + where + `
			GROUP BY m.name, c->>'currency' ORDER BY 1`
	default:
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, sql, cid, from, to)
		if err != nil {
			return nil, err
		}
		totals, err := queryJSON(ctx, tx, `SELECT `+sums+posOrderFrom+where+` GROUP BY o.currency ORDER BY o.currency`, cid, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"from": from, "to": to, "group": group, "rows": rows, "totals": totals}, nil
	})
}
