package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Suppliers, purchase bills, supplier payments, expenses, items and stock (weighted average cost in base currency).

type purchSettings struct {
	AP, Inventory, COGS, Fuel, Adjust, Expense *string
}

func loadPurchSettings(ctx context.Context, tx pgx.Tx, cid string) (*purchSettings, error) {
	p := &purchSettings{}
	err := tx.QueryRow(ctx, `SELECT ap_account_id::text, inventory_account_id::text, cogs_account_id::text, fuel_account_id::text,
		stock_adjust_account_id::text, default_expense_account_id::text FROM companies WHERE id = $1`, cid).
		Scan(&p.AP, &p.Inventory, &p.COGS, &p.Fuel, &p.Adjust, &p.Expense)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	return p, err
}

func notSetup(what string) error {
	return &apiError{status: http.StatusUnprocessableEntity, code: "purchasing_not_setup", detail: what}
}

func defaultWarehouse(ctx context.Context, tx pgx.Tx, cid string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM warehouses WHERE company_id = $1 AND is_active ORDER BY created_at LIMIT 1`, cid).Scan(&id)
	if err == pgx.ErrNoRows {
		err = tx.QueryRow(ctx, `INSERT INTO warehouses (company_id, name) VALUES ($1, 'Main') RETURNING id::text`, cid).Scan(&id)
	}
	return id, err
}

func checkWarehouse(ctx context.Context, tx pgx.Tx, cid, wh string) (string, error) {
	if wh == "" {
		return defaultWarehouse(ctx, tx, cid)
	}
	if !isUUID(wh) {
		return "", badRequest("invalid_warehouse")
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM warehouses WHERE id = $1 AND company_id = $2)`, wh, cid).Scan(&ok); err != nil {
		return "", err
	}
	if !ok {
		return "", badRequest("invalid_warehouse")
	}
	return wh, nil
}

type itemInfo struct {
	ID, Code, Name, Kind, Unit  string
	Inventory, Expense, Revenue *string
}

func loadItem(ctx context.Context, tx pgx.Tx, cid, id string) (*itemInfo, error) {
	if !isUUID(id) {
		return nil, badRequest("invalid_item")
	}
	it := &itemInfo{}
	err := tx.QueryRow(ctx, `SELECT id::text, code, name, kind, unit, inventory_account_id::text, expense_account_id::text,
		revenue_account_id::text FROM items WHERE id = $1 AND company_id = $2 AND is_active`, id, cid).
		Scan(&it.ID, &it.Code, &it.Name, &it.Kind, &it.Unit, &it.Inventory, &it.Expense, &it.Revenue)
	if err == pgx.ErrNoRows {
		return nil, badRequest("invalid_item")
	}
	return it, err
}

// lockStock reads (and locks) the stock row of an item in a warehouse.
func lockStock(ctx context.Context, tx pgx.Tx, item, wh string) (qty, avg decimal.Decimal, err error) {
	var q, a string
	err = tx.QueryRow(ctx, `SELECT quantity::text, avg_cost::text FROM stock WHERE item_id = $1 AND warehouse_id = $2 FOR UPDATE`, item, wh).Scan(&q, &a)
	if err == pgx.ErrNoRows {
		return decimal.Zero, decimal.Zero, nil
	}
	qty, _ = decimal.NewFromString(q)
	avg, _ = decimal.NewFromString(a)
	return qty, avg, err
}

func insufficientStock(name string, have decimal.Decimal) error {
	return &apiError{status: http.StatusUnprocessableEntity, code: "insufficient_stock", detail: name + " (" + have.String() + ")"}
}

// stockValueOf returns the exact value held for an item/warehouse (locked by lockStock first).
func stockValueOf(ctx context.Context, tx pgx.Tx, item, wh string) (decimal.Decimal, error) {
	var v string
	err := tx.QueryRow(ctx, `SELECT value::text FROM stock WHERE item_id = $1 AND warehouse_id = $2`, item, wh).Scan(&v)
	if err == pgx.ErrNoRows {
		return decimal.Zero, nil
	}
	d, _ := decimal.NewFromString(v)
	return d, err
}

// outValue: value leaving stock for qty. Taking everything takes the exact remaining value (no rounding residue).
func outValue(ctx context.Context, tx pgx.Tx, item, wh string, have, avg, qty decimal.Decimal) (decimal.Decimal, error) {
	held, err := stockValueOf(ctx, tx, item, wh)
	if err != nil {
		return decimal.Zero, err
	}
	if qty.Equal(have) {
		return held, nil
	}
	if costing, _, err := itemStockMode(ctx, tx, item); err != nil {
		return decimal.Zero, err
	} else if costing == "fifo" {
		v, err := fifoOutValue(ctx, tx, item, wh, qty)
		if err != nil {
			return decimal.Zero, err
		}
		return decimal.Min(v, held), nil
	}
	v := qty.Mul(avg).Round(4)
	if v.GreaterThan(held) {
		v = held
	}
	return v, nil
}

// setStock writes quantity and exact value; the average cost is derived from them.
func setStock(ctx context.Context, tx pgx.Tx, item, wh string, qty, value decimal.Decimal) error {
	if !qty.IsPositive() || value.IsNegative() {
		value = decimal.Zero
	}
	avg := decimal.Zero
	if qty.IsPositive() {
		avg = value.DivRound(qty, 6)
	}
	_, err := tx.Exec(ctx, `INSERT INTO stock (item_id, warehouse_id, quantity, avg_cost, value) VALUES ($1, $2, $3::numeric, $4::numeric, $5::numeric)
		ON CONFLICT (tenant_id, item_id, warehouse_id) DO UPDATE SET quantity = EXCLUDED.quantity, avg_cost = EXCLUDED.avg_cost,
		value = EXCLUDED.value, updated_at = now()`, item, wh, qty.String(), avg.String(), value.String())
	return err
}

// stockIn adds quantity at a total base value and recomputes the weighted average cost.
func stockIn(ctx context.Context, tx pgx.Tx, cid, item, wh string, qty, value decimal.Decimal, date, source, sourceID string, je *string, notes string) error {
	q, _, err := lockStock(ctx, tx, item, wh)
	if err != nil {
		return err
	}
	held, err := stockValueOf(ctx, tx, item, wh)
	if err != nil {
		return err
	}
	if err := setStock(ctx, tx, item, wh, q.Add(qty), held.Add(value)); err != nil {
		return err
	}
	unit := decimal.Zero
	if !qty.IsZero() {
		unit = value.DivRound(qty, 6)
	}
	var moveID string
	if err = tx.QueryRow(ctx, `INSERT INTO stock_moves (company_id, item_id, warehouse_id, move_date, quantity, unit_cost, value, source, source_id, journal_entry_id, notes)
		VALUES ($1, $2, $3, $4::date, $5::numeric, $6::numeric, $7::numeric, $8, nullif($9, '')::uuid, $10, $11) RETURNING id::text`,
		cid, item, wh, date, qty.String(), unit.String(), value.String(), source, sourceID, je, notes).Scan(&moveID); err != nil {
		return err
	}
	return stockHooks(ctx, tx, cid, moveID, item, wh, qty, value, date, source, sourceID)
}

// stockOut removes quantity at the current average cost (value already computed by the caller).
func stockOut(ctx context.Context, tx pgx.Tx, cid, item, wh string, qty, avg, value decimal.Decimal, date, source, sourceID string, je *string, notes string) error {
	q, _, err := lockStock(ctx, tx, item, wh)
	if err != nil {
		return err
	}
	held, err := stockValueOf(ctx, tx, item, wh)
	if err != nil {
		return err
	}
	if err := setStock(ctx, tx, item, wh, q.Sub(qty), held.Sub(value)); err != nil {
		return err
	}
	var moveID string
	if err = tx.QueryRow(ctx, `INSERT INTO stock_moves (company_id, item_id, warehouse_id, move_date, quantity, unit_cost, value, source, source_id, journal_entry_id, notes)
		VALUES ($1, $2, $3, $4::date, $5::numeric, $6::numeric, $7::numeric, $8, nullif($9, '')::uuid, $10, $11) RETURNING id::text`,
		cid, item, wh, date, qty.Neg().String(), avg.String(), value.Neg().String(), source, sourceID, je, notes).Scan(&moveID); err != nil {
		return err
	}
	return stockHooks(ctx, tx, cid, moveID, item, wh, qty.Neg(), value.Neg(), date, source, sourceID)
}

// ---- settings ----

const purchSettingsSQL = `SELECT ap_account_id, inventory_account_id, cogs_account_id, fuel_account_id, stock_adjust_account_id,
	default_expense_account_id FROM companies WHERE id = $1`

func (s *Server) getPurchSettings(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, purchSettingsSQL, r.PathValue("cid"))
	})
}

func (s *Server) putPurchSettings(w http.ResponseWriter, r *http.Request) {
	in := map[string]*string{}
	if !decodeBody(w, r, &in) {
		return
	}
	cols := map[string]string{"ap_account_id": "", "inventory_account_id": "", "cogs_account_id": "", "fuel_account_id": "",
		"stock_adjust_account_id": "", "default_expense_account_id": ""}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		for k, v := range in {
			if _, ok := cols[k]; !ok || v == nil || (*v != "" && !isUUID(*v)) {
				return nil, badRequest("bad_request")
			}
			if *v != "" {
				if err := requirePostable(ctx, tx, cid, *v); err != nil {
					return nil, err
				}
			}
			// k is whitelisted above
			if _, err := tx.Exec(ctx, `UPDATE companies SET `+k+` = nullif($2, '')::uuid WHERE id = $1`, cid, *v); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, purchSettingsSQL, cid)
	})
}

// ---- suppliers ----

const supplierBalances = `
	coalesce((SELECT json_agg(json_build_object('currency', x.currency, 'due', x.due::text) ORDER BY x.currency)
	          FROM (SELECT b.currency, sum(b.total - b.paid) AS due FROM bills b
	                WHERE b.supplier_id = su.id AND b.status IN ('open', 'partial') GROUP BY b.currency) x), '[]'::json) AS due,
	coalesce((SELECT json_agg(json_build_object('currency', y.currency, 'credit', y.credit::text) ORDER BY y.currency)
	          FROM (SELECT p.currency, sum(p.amount - coalesce((SELECT sum(a.amount) FROM supplier_allocations a WHERE a.payment_id = p.id), 0)) AS credit
	                FROM supplier_payments p WHERE p.supplier_id = su.id AND p.status = 'posted' GROUP BY p.currency
	                HAVING sum(p.amount - coalesce((SELECT sum(a.amount) FROM supplier_allocations a WHERE a.payment_id = p.id), 0)) > 0) y), '[]'::json) AS credit`

const supplierCols = `su.id, su.code, su.name, su.phone, su.email, su.address, su.tax_id, su.currency, su.notes, su.status, su.created_at, ` + supplierBalances

func (s *Server) listSuppliers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+supplierCols+` FROM suppliers su WHERE su.company_id = $1
			AND ($2 = '' OR su.name ILIKE '%' || $2 || '%' OR su.code ILIKE $2 || '%' OR su.phone LIKE '%' || $2 || '%')
			ORDER BY su.status, su.name LIMIT 1000`, r.PathValue("cid"), q)
	})
}

func (s *Server) saveSupplier(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     *string `json:"code"`
		Name     *string `json:"name"`
		Phone    *string `json:"phone"`
		Email    *string `json:"email"`
		Address  *string `json:"address"`
		TaxID    *string `json:"tax_id"`
		Currency *string `json:"currency"`
		Notes    *string `json:"notes"`
		Status   *string `json:"status"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Name == nil || strings.TrimSpace(*in.Name) == "")) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") ||
		(in.Currency != nil && *in.Currency != "" && !currencyRe.MatchString(*in.Currency)) ||
		(in.Status != nil && *in.Status != "active" && *in.Status != "inactive") || (!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if create {
			code := strings.TrimSpace(deref(in.Code))
			if code == "" {
				var n int64
				if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'supplier')`, cid).Scan(&n); err != nil {
					return nil, err
				}
				code = "S" + strconv.FormatInt(n, 10)
			}
			err := tx.QueryRow(ctx, `INSERT INTO suppliers (company_id, code, name, phone, email, address, tax_id, currency, notes)
				VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9) RETURNING id::text`,
				cid, code, strings.TrimSpace(*in.Name), strings.TrimSpace(deref(in.Phone)), strings.TrimSpace(deref(in.Email)),
				strings.TrimSpace(deref(in.Address)), strings.TrimSpace(deref(in.TaxID)), deref(in.Currency), deref(in.Notes)).Scan(&id)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "code_taken"}
			}
			if err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE suppliers SET code = coalesce(nullif(trim($3), ''), code), name = coalesce(trim($4), name),
				phone = coalesce(trim($5), phone), email = coalesce(trim($6), email), address = coalesce(trim($7), address),
				tax_id = coalesce(trim($8), tax_id), currency = CASE WHEN $9::text IS NULL THEN currency ELSE nullif($9, '') END,
				notes = coalesce($10, notes), status = coalesce($11, status) WHERE id = $1 AND company_id = $2`,
				id, cid, in.Code, in.Name, in.Phone, in.Email, in.Address, in.TaxID, in.Currency, in.Notes, in.Status)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "code_taken"}
			}
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+supplierCols+` FROM suppliers su WHERE su.id = $1`, id)
	})
}

func (s *Server) getSupplier(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		sup, err := queryJSONOne(ctx, tx, `SELECT `+supplierCols+` FROM suppliers su WHERE su.id = $1 AND su.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		bills, err := queryJSON(ctx, tx, `SELECT `+billCols+` FROM bills b JOIN suppliers su ON su.id = b.supplier_id
			WHERE b.supplier_id = $1 ORDER BY b.bill_date DESC, b.number DESC LIMIT 100`, id)
		if err != nil {
			return nil, err
		}
		pays, err := queryJSON(ctx, tx, `SELECT `+supPayCols+` FROM supplier_payments p JOIN suppliers su ON su.id = p.supplier_id
			JOIN payment_methods m ON m.id = p.method_id WHERE p.supplier_id = $1 ORDER BY p.payment_date DESC, p.number DESC LIMIT 100`, id)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"supplier": sup, "bills": bills, "payments": pays}, nil
	})
}

func lockSupplier(ctx context.Context, tx pgx.Tx, cid, id string) (string, error) {
	if !isUUID(id) {
		return "", badRequest("invalid_supplier")
	}
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM suppliers WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&name)
	if err == pgx.ErrNoRows {
		return "", badRequest("invalid_supplier")
	}
	return name, err
}

// autoAllocateSupplier applies unallocated supplier payments (and debit notes) to open bills of the same currency,
// oldest first. For a foreign currency, when the payment's payables debit used another rate than the bill's payables
// credit, the base-currency difference is booked as realized exchange gain/loss so payables keep no residue.
func autoAllocateSupplier(ctx context.Context, tx pgx.Tx, cid, supplierID, currency string) error {
	type open struct {
		id, date, rate string
		number         int64
		amount         decimal.Decimal
	}
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return err
	}
	ps, err := loadPurchSettings(ctx, tx, cid)
	if err != nil {
		return err
	}
	ap := ""
	if ps.AP != nil {
		ap = *ps.AP
	}
	collect := func(sql string) ([]open, error) {
		rows, err := tx.Query(ctx, sql, supplierID, currency, ap)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (open, error) {
			var o open
			var s string
			err := row.Scan(&o.id, &o.date, &o.rate, &o.number, &s)
			o.amount, _ = decimal.NewFromString(s)
			return o, err
		})
	}
	pays, err := collect(`SELECT p.id::text, p.payment_date::text,
		coalesce((SELECT trim_scale(l.rate)::text FROM journal_lines l WHERE l.entry_id = p.journal_entry_id
		          AND l.account_id::text = $3 AND l.debit > 0 LIMIT 1), ''), p.number,
		(p.amount - coalesce((SELECT sum(a.amount) FROM supplier_allocations a WHERE a.payment_id = p.id), 0))::text
		FROM supplier_payments p
		WHERE p.supplier_id = $1 AND p.currency = $2 AND p.status = 'posted'
		  AND p.amount - coalesce((SELECT sum(a.amount) FROM supplier_allocations a WHERE a.payment_id = p.id), 0) > 0
		ORDER BY p.payment_date, p.created_at`)
	if err != nil || len(pays) == 0 {
		return err
	}
	bills, err := collect(`SELECT b.id::text, b.bill_date::text,
		coalesce((SELECT trim_scale(l.rate)::text FROM journal_lines l WHERE l.entry_id = b.journal_entry_id
		          AND l.account_id::text = $3 AND l.credit > 0 LIMIT 1), ''), b.number, (b.total - b.paid)::text FROM bills b
		WHERE b.supplier_id = $1 AND b.currency = $2 AND b.status IN ('open', 'partial') ORDER BY b.bill_date, b.number`)
	if err != nil {
		return err
	}
	i, j := 0, 0
	for i < len(pays) && j < len(bills) {
		amt := decimal.Min(pays[i].amount, bills[j].amount)
		var fx *string
		if ap != "" && currency != set.Base && pays[i].rate != "" && bills[j].rate != "" {
			rp, _ := decimal.NewFromString(pays[i].rate)
			rb, _ := decimal.NewFromString(bills[j].rate)
			if rp.IsPositive() && rb.IsPositive() {
				residue := amt.DivRound(rb, 4).Sub(amt.DivRound(rp, 4)) // base credit left on payables minus base debit
				if !residue.IsZero() {
					date := pays[i].date
					if bills[j].date > date {
						date = bills[j].date
					}
					line := jline{Account: ap, Desc: "FX on settlement", Currency: set.Base, Rate: "1"}
					if residue.IsPositive() {
						line.Debit = residue
					} else {
						line.Credit = residue.Neg()
					}
					je, err := postJournal(ctx, tx, cid, set, date, fmt.Sprintf("Exchange difference - supplier payment #%d to bill #%d", pays[i].number, bills[j].number),
						fmt.Sprintf("SFX-%d-%d", pays[i].number, bills[j].number), "fx", []jline{line})
					if err != nil {
						return err
					}
					fx = &je
				}
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO supplier_allocations (payment_id, bill_id, amount, fx_entry_id) VALUES ($1, $2, $3::numeric, $4)`,
			pays[i].id, bills[j].id, amt.String(), fx); err != nil {
			return err
		}
		pays[i].amount = pays[i].amount.Sub(amt)
		bills[j].amount = bills[j].amount.Sub(amt)
		if pays[i].amount.IsZero() {
			i++
		}
		if bills[j].amount.IsZero() {
			j++
		}
	}
	return nil
}

// ---- items & warehouses ----

const itemCols = `it.id, it.code, it.name, it.name_i18n, it.kind, it.unit, it.sale_price::text AS sale_price, it.sale_currency,
	it.inventory_account_id, it.expense_account_id, it.revenue_account_id, it.is_active,
	it.is_sellable, it.is_purchasable, it.sale_tax_rate_id, it.description, it.barcode, it.category_id, it.image, it.pos_visible,
	coalesce((SELECT json_agg(json_build_object('currency', ip.currency, 'price', ip.price::text) ORDER BY ip.currency)
	          FROM item_prices ip WHERE ip.item_id = it.id), '[]'::json) AS prices,
	coalesce((SELECT sum(st.quantity) FROM stock st WHERE st.item_id = it.id), 0)::text AS quantity,
	coalesce((SELECT sum(st.value) FROM stock st WHERE st.item_id = it.id), 0)::text AS value,
	coalesce((SELECT CASE WHEN sum(st.quantity) > 0 THEN round(sum(st.value) / sum(st.quantity), 6) END
	          FROM stock st WHERE st.item_id = it.id), 0)::text AS avg_cost`

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+itemCols+` FROM items it WHERE it.company_id = $1
			AND ($2 = '' OR it.name ILIKE '%' || $2 || '%' OR it.code ILIKE $2 || '%' OR it.barcode = $2)
			ORDER BY it.is_active DESC, it.name LIMIT 2000`, r.PathValue("cid"), q)
	})
}

func (s *Server) saveItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code               *string           `json:"code"`
		Name               *string           `json:"name"`
		NameI18n           map[string]string `json:"name_i18n"`
		Kind               *string           `json:"kind"`
		Unit               *string           `json:"unit"`
		SalePrice          *string           `json:"sale_price"`
		SaleCurrency       *string           `json:"sale_currency"`
		InventoryAccountID *string           `json:"inventory_account_id"`
		ExpenseAccountID   *string           `json:"expense_account_id"`
		RevenueAccountID   *string           `json:"revenue_account_id"`
		IsActive           *bool             `json:"is_active"`
		IsSellable         *bool             `json:"is_sellable"`
		IsPurchasable      *bool             `json:"is_purchasable"`
		SaleTaxRateID      *string           `json:"sale_tax_rate_id"` // "" removes
		Description        *string           `json:"description"`
		Barcode            *string           `json:"barcode"`
		CategoryID         *string           `json:"category_id"` // "" removes
		Image              *string           `json:"image"`       // data:image/... URL, "" removes
		POSVisible         *bool             `json:"pos_visible"`
		Prices             *[]struct {
			Currency string `json:"currency"`
			Price    string `json:"price"`
		} `json:"prices"` // replaces the per-currency prices
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	okMoney := in.SalePrice == nil || *in.SalePrice == ""
	if !okMoney {
		_, okMoney = money(*in.SalePrice)
	}
	if in.Prices != nil {
		seen := map[string]bool{}
		for _, p := range *in.Prices {
			v, ok := money(p.Price)
			if !ok || v.IsNegative() || !currencyRe.MatchString(p.Currency) || seen[p.Currency] || len(*in.Prices) > 50 {
				okMoney = false
			}
			seen[p.Currency] = true
		}
	}
	if !optUUID(in.SaleTaxRateID) || !optUUID(in.CategoryID) || (in.Barcode != nil && len(strings.TrimSpace(*in.Barcode)) > 64) ||
		(in.Image != nil && *in.Image != "" && (!strings.HasPrefix(*in.Image, "data:image/") || len(*in.Image) > 120000)) {
		okMoney = false
	}
	if (create && (in.Name == nil || strings.TrimSpace(*in.Name) == "")) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") ||
		(in.Kind != nil && *in.Kind != "stock" && *in.Kind != "service" && *in.Kind != "consumable") || !okMoney ||
		(in.SaleCurrency != nil && *in.SaleCurrency != "" && !currencyRe.MatchString(*in.SaleCurrency)) ||
		!optUUID(in.InventoryAccountID) || !optUUID(in.ExpenseAccountID) || !optUUID(in.RevenueAccountID) || (!create && !isUUID(id)) {
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
		set, err := loadPurchSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		for _, p := range []*string{in.InventoryAccountID, in.ExpenseAccountID, in.RevenueAccountID} {
			if p != nil && *p != "" {
				if err := requirePostable(ctx, tx, cid, *p); err != nil {
					return nil, err
				}
			}
		}
		if create {
			kind := "stock"
			if in.Kind != nil {
				kind = *in.Kind
			}
			inv, exp := deref(in.InventoryAccountID), deref(in.ExpenseAccountID)
			if inv == "" && set.Inventory != nil && kind == "stock" {
				inv = *set.Inventory
			}
			if exp == "" {
				if kind == "stock" && set.COGS != nil {
					exp = *set.COGS
				} else if set.Expense != nil {
					exp = *set.Expense
				}
			}
			if kind == "stock" && inv == "" {
				return nil, notSetup("inventory")
			}
			code := strings.TrimSpace(deref(in.Code))
			if code == "" {
				var n int64
				if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'item')`, cid).Scan(&n); err != nil {
					return nil, err
				}
				code = "I" + strconv.FormatInt(n, 10)
			}
			if i18n == nil {
				i18n = []byte("{}")
			}
			err := tx.QueryRow(ctx, `INSERT INTO items (company_id, code, name, name_i18n, kind, unit, sale_price, sale_currency,
				inventory_account_id, expense_account_id, revenue_account_id)
				VALUES ($1, $2, $3, $4, $5, coalesce(nullif($6, ''), 'pcs'), coalesce(nullif($7, '')::numeric, 0), nullif($8, ''),
				        nullif($9, '')::uuid, nullif($10, '')::uuid, nullif($11, '')::uuid) RETURNING id::text`,
				cid, code, strings.TrimSpace(*in.Name), i18n, kind, strings.TrimSpace(deref(in.Unit)), deref(in.SalePrice),
				deref(in.SaleCurrency), inv, exp, deref(in.RevenueAccountID)).Scan(&id)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "code_taken"}
			}
			if err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE items SET code = coalesce(nullif(trim($3), ''), code), name = coalesce(trim($4), name),
				name_i18n = coalesce($5::jsonb, name_i18n), unit = coalesce(nullif(trim($6), ''), unit),
				sale_price = coalesce(nullif($7, '')::numeric, sale_price),
				sale_currency = CASE WHEN $8::text IS NULL THEN sale_currency ELSE nullif($8, '') END,
				inventory_account_id = coalesce(nullif($9, '')::uuid, inventory_account_id),
				expense_account_id = coalesce(nullif($10, '')::uuid, expense_account_id),
				revenue_account_id = CASE WHEN $11::text IS NULL THEN revenue_account_id ELSE nullif($11, '')::uuid END,
				is_active = coalesce($12, is_active) WHERE id = $1 AND company_id = $2`,
				id, cid, in.Code, in.Name, i18n, in.Unit, in.SalePrice, in.SaleCurrency, in.InventoryAccountID, in.ExpenseAccountID,
				in.RevenueAccountID, in.IsActive)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "code_taken"}
			}
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		if in.SaleTaxRateID != nil && *in.SaleTaxRateID != "" {
			if _, err := loadTaxRate(ctx, tx, cid, *in.SaleTaxRateID, "sales"); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE items SET is_sellable = coalesce($2, is_sellable), is_purchasable = coalesce($3, is_purchasable),
			sale_tax_rate_id = CASE WHEN $4::text IS NULL THEN sale_tax_rate_id ELSE nullif($4, '')::uuid END,
			description = coalesce(trim($5), description) WHERE id = $1`,
			id, in.IsSellable, in.IsPurchasable, in.SaleTaxRateID, in.Description); err != nil {
			return nil, err
		}
		if in.CategoryID != nil && *in.CategoryID != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM item_categories WHERE id = $1 AND company_id = $2)`, *in.CategoryID, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_category")
			}
		}
		var barcode *string
		if in.Barcode != nil {
			b := strings.TrimSpace(*in.Barcode)
			barcode = &b
		}
		_, err = tx.Exec(ctx, `UPDATE items SET barcode = coalesce($2, barcode),
			category_id = CASE WHEN $3::text IS NULL THEN category_id ELSE nullif($3, '')::uuid END,
			image = coalesce($4, image), pos_visible = coalesce($5, pos_visible) WHERE id = $1`,
			id, barcode, in.CategoryID, in.Image, in.POSVisible)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "barcode_taken"}
		}
		if err != nil {
			return nil, err
		}
		if in.Prices != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM item_prices WHERE item_id = $1`, id); err != nil {
				return nil, err
			}
			for _, p := range *in.Prices {
				if _, err := tx.Exec(ctx, `INSERT INTO item_prices (item_id, currency, price) VALUES ($1, $2, $3::numeric)`,
					id, p.Currency, p.Price); err != nil {
					return nil, err
				}
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+itemCols+` FROM items it WHERE it.id = $1`, id)
	})
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		item, err := queryJSONOne(ctx, tx, `SELECT `+itemCols+` FROM items it WHERE it.id = $1 AND it.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		stock, err := queryJSON(ctx, tx, `SELECT w.id AS warehouse_id, w.name AS warehouse, st.quantity::text AS quantity,
			st.avg_cost::text AS avg_cost, st.value::text AS value
			FROM stock st JOIN warehouses w ON w.id = st.warehouse_id WHERE st.item_id = $1 ORDER BY w.name`, id)
		if err != nil {
			return nil, err
		}
		moves, err := queryJSON(ctx, tx, `SELECT m.id, m.move_date, w.name AS warehouse, m.quantity::text AS quantity,
			m.unit_cost::text AS unit_cost, m.value::text AS value, m.source, m.source_id, m.notes, m.created_at
			FROM stock_moves m JOIN warehouses w ON w.id = m.warehouse_id WHERE m.item_id = $1
			ORDER BY m.move_date DESC, m.created_at DESC LIMIT 300`, id)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"item": item, "stock": stock, "moves": moves}, nil
	})
}

func (s *Server) listWarehouses(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := defaultWarehouse(ctx, tx, r.PathValue("cid")); err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT w.id, w.name, w.is_active,
			coalesce((SELECT sum(st.value) FROM stock st WHERE st.warehouse_id = w.id), 0)::text AS value
			FROM warehouses w WHERE w.company_id = $1 ORDER BY w.created_at`, r.PathValue("cid"))
	})
}

func (s *Server) createWarehouse(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, r.PathValue("cid")); err != nil {
			return nil, err
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO warehouses (company_id, name) VALUES ($1, $2) RETURNING id::text`,
			r.PathValue("cid"), strings.TrimSpace(in.Name)).Scan(&id)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
		}
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	})
}

// ---- purchase bills ----

const billCols = `b.id, b.number, b.supplier_id, su.code AS supplier_code, su.name AS supplier_name, b.supplier_ref, b.bill_date,
	b.due_date, b.currency, b.total::text AS total, b.paid::text AS paid, (b.total - b.paid)::text AS balance, b.status,
	b.journal_entry_id, b.notes, b.created_at`

type billLineIn struct {
	ItemID      string   `json:"item_id"`
	WarehouseID string   `json:"warehouse_id"`
	AccountID   string   `json:"account_id"`
	Description string   `json:"description"`
	Quantity    string   `json:"quantity"`
	UnitPrice   string   `json:"unit_price"`
	TaxRateID   string   `json:"tax_rate_id"`
	UnitID      string   `json:"unit_id"` // a purchase unit of the item (barrel = 200 L): quantity and price are per that unit
	Lots        []lotQty `json:"lots"`    // lot / serial numbers received (tracked items)
}

func (s *Server) createBill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SupplierID  string       `json:"supplier_id"`
		SupplierRef string       `json:"supplier_ref"`
		BillDate    string       `json:"bill_date"`
		DueDate     string       `json:"due_date"`
		Currency    string       `json:"currency"`
		Notes       string       `json:"notes"`
		Lines       []billLineIn `json:"lines"`
		CostCenter  string       `json:"cost_center_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.BillDate == "" {
		in.BillDate = today()
	}
	if in.DueDate == "" {
		in.DueDate = in.BillDate
	}
	if !validDate(in.BillDate) || !validDate(in.DueDate) || in.DueDate < in.BillDate || !currencyRe.MatchString(in.Currency) ||
		len(in.Lines) == 0 || len(in.Lines) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		ctx = withCostCenter(ctx, in.CostCenter)
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		ps, err := loadPurchSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if ps.AP == nil {
			return nil, notSetup("ap")
		}
		name, err := lockSupplier(ctx, tx, cid, in.SupplierID)
		if err != nil {
			return nil, err
		}
		dec, err := currencyDecimals(ctx, tx, in.Currency)
		if err != nil {
			return nil, err
		}
		rateS, err := resolveRate(ctx, tx, cid, set.Base, in.Currency, in.BillDate, "")
		if err != nil {
			return nil, err
		}
		rate, _ := decimal.NewFromString(rateS)

		type line struct {
			item          *itemInfo
			wh, account   string
			desc          string
			qty, price    decimal.Decimal
			amount, value decimal.Decimal
			tax           *taxInfo
			taxAmount     decimal.Decimal
			lots          []lotQty
		}
		lines := []line{}
		total := decimal.Zero
		for _, l := range in.Lines {
			qty, ok1 := money(l.Quantity)
			price, ok2 := money(l.UnitPrice)
			if !ok1 || !ok2 || !qty.IsPositive() {
				return nil, badRequest("invalid_amount")
			}
			ln := line{qty: qty, price: price, desc: strings.TrimSpace(l.Description)}
			if l.ItemID != "" {
				it, err := loadItem(ctx, tx, cid, l.ItemID)
				if err != nil {
					return nil, err
				}
				ln.item = it
				if ln.desc == "" {
					ln.desc = it.Name
				}
				if l.UnitID != "" { // bought in a bigger unit: same amount, base quantity and price stored
					f, uname, err := unitFactor(ctx, tx, it.ID, l.UnitID)
					if err != nil {
						return nil, err
					}
					ln.desc = fmt.Sprintf("%s (%s %s)", ln.desc, qty.String(), uname)
					ln.qty, ln.price = qty.Mul(f), price.DivRound(f, 6)
				}
				ln.lots = l.Lots
				if it.Kind == "stock" {
					if it.Inventory == nil {
						return nil, notSetup("inventory")
					}
					ln.account = *it.Inventory
					if ln.wh, err = checkWarehouse(ctx, tx, cid, l.WarehouseID); err != nil {
						return nil, err
					}
				} else if l.AccountID != "" {
					ln.account = l.AccountID
				} else if it.Expense != nil {
					ln.account = *it.Expense
				}
			} else {
				ln.account = l.AccountID
			}
			if ln.account == "" && ps.Expense != nil {
				ln.account = *ps.Expense
			}
			if ln.account == "" {
				return nil, notSetup("expense")
			}
			if !isUUID(ln.account) {
				return nil, badRequest("invalid_account")
			}
			if err := requirePostable(ctx, tx, cid, ln.account); err != nil {
				return nil, err
			}
			if ln.desc == "" {
				return nil, badRequest("bad_request")
			}
			ln.amount = qty.Mul(price).Round(dec)
			ln.value = ln.amount.DivRound(rate, 4) // base value, same rounding as the journal (tax excluded)
			if ln.tax, err = loadTaxRate(ctx, tx, cid, l.TaxRateID, "purchases"); err != nil {
				return nil, err
			}
			ln.taxAmount = taxOn(ln.amount, ln.tax, dec)
			total = total.Add(ln.amount).Add(ln.taxAmount)
			lines = append(lines, ln)
		}
		if !total.IsPositive() {
			return nil, badRequest("zero_invoice")
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'bill')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		jl := []jline{}
		for _, l := range lines {
			jl = append(jl, jline{Account: l.account, Desc: l.desc, Currency: in.Currency, Debit: l.amount, Rate: rateS})
			if l.tax != nil && l.taxAmount.IsPositive() {
				jl = append(jl, jline{Account: l.tax.Account, Desc: l.tax.Name + " · " + l.desc, Currency: in.Currency, Debit: l.taxAmount, Rate: rateS})
			}
		}
		jl = append(jl, jline{Account: *ps.AP, Desc: name, Currency: in.Currency, Credit: total, Rate: rateS})
		je, err := postJournal(ctx, tx, cid, set, in.BillDate, fmt.Sprintf("Bill #%d · %s", number, name), fmt.Sprintf("BILL-%d", number), "bill", jl)
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO bills (company_id, number, supplier_id, supplier_ref, bill_date, due_date, currency, total,
			journal_entry_id, notes) VALUES ($1, $2, $3, $4, $5::date, $6::date, $7, $8::numeric, $9, $10) RETURNING id::text`,
			cid, number, in.SupplierID, strings.TrimSpace(in.SupplierRef), in.BillDate, in.DueDate, in.Currency, total.String(), je,
			strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		for i, l := range lines {
			var itemID, wh *string
			if l.item != nil {
				itemID = &l.item.ID
			}
			if l.wh != "" {
				wh = &l.wh
			}
			var taxID *string
			if l.tax != nil {
				taxID = &l.tax.ID
			}
			if _, err := tx.Exec(ctx, `INSERT INTO bill_lines (bill_id, line_no, item_id, warehouse_id, account_id, description, quantity, unit_price, amount,
				tax_rate_id, tax_amount) VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9::numeric, $10, $11::numeric)`,
				id, i+1, itemID, wh, l.account, l.desc, l.qty.String(), l.price.String(), l.amount.String(), taxID, l.taxAmount.String()); err != nil {
				return nil, err
			}
			if l.item != nil && l.item.Kind == "stock" {
				if err := stockIn(withLots(ctx, l.item.ID, l.lots), tx, cid, l.item.ID, l.wh, l.qty, l.value, in.BillDate, "bill", id, &je, fmt.Sprintf("Bill #%d", number)); err != nil {
					return nil, err
				}
			}
		}
		if err := autoAllocateSupplier(ctx, tx, cid, in.SupplierID, in.Currency); err != nil {
			return nil, err
		}
		return loadBill(ctx, tx, cid, id)
	})
}

func loadBill(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	bill, err := queryJSONOne(ctx, tx, `SELECT `+billCols+`, su.phone AS supplier_phone, co.name_en AS company_name, co.name_ar AS company_name_ar
		FROM bills b JOIN suppliers su ON su.id = b.supplier_id JOIN companies co ON co.id = b.company_id
		WHERE b.id = $1 AND b.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.line_no, l.item_id, it.code AS item_code, it.unit, l.warehouse_id, l.account_id, a.code AS account_code,
		l.description, l.quantity::text AS quantity, l.unit_price::text AS unit_price, l.amount::text AS amount,
		l.tax_amount::text AS tax_amount, (SELECT t.name FROM tax_rates t WHERE t.id = l.tax_rate_id) AS tax_name
		FROM bill_lines l JOIN accounts a ON a.id = l.account_id LEFT JOIN items it ON it.id = l.item_id
		WHERE l.bill_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	allocs, err := queryJSON(ctx, tx, `SELECT a.payment_id, p.number AS payment_number, p.payment_date, a.amount::text AS amount
		FROM supplier_allocations a JOIN supplier_payments p ON p.id = a.payment_id WHERE a.bill_id = $1 ORDER BY a.created_at`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"bill": bill, "lines": lines, "payments": allocs}, nil
}

func (s *Server) getBill(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadBill(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

func (s *Server) listBills(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, supplier, search := q.Get("status"), q.Get("supplier_id"), strings.TrimSpace(q.Get("q"))
	if supplier != "" && !isUUID(supplier) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cond := `b.company_id = $1 AND b.bill_date BETWEEN $2::date AND $3::date AND ($4 = '' OR b.supplier_id = $4::uuid)
			AND ($5 = '' OR su.name ILIKE '%' || $5 || '%' OR b.number::text = $5 OR b.supplier_ref ILIKE $5 || '%')
			AND (CASE $6 WHEN '' THEN true WHEN 'unpaid' THEN b.status IN ('open', 'partial') ELSE b.status = $6 END)`
		rows, err := queryJSON(ctx, tx, `SELECT `+billCols+` FROM bills b JOIN suppliers su ON su.id = b.supplier_id WHERE `+cond+`
			ORDER BY b.bill_date DESC, b.number DESC LIMIT 1000`, r.PathValue("cid"), from, to, supplier, search, status)
		if err != nil {
			return nil, err
		}
		totals, err := queryJSON(ctx, tx, `SELECT b.currency, count(*) AS count, sum(b.total)::text AS total, sum(b.paid)::text AS paid,
			sum(b.total - b.paid)::text AS balance FROM bills b JOIN suppliers su ON su.id = b.supplier_id
			WHERE `+cond+` AND b.status <> 'void' GROUP BY b.currency ORDER BY b.currency`, r.PathValue("cid"), from, to, supplier, search, status)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows, "totals": totals}, nil
	})
}

// voidBill reverses the journal entry and takes the stock back out. Refused when paid or when the stock was already used.
func (s *Server) voidBill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
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
		var number int64
		var status, paid string
		var je *string
		err := tx.QueryRow(ctx, `SELECT number, status, paid::text, journal_entry_id::text FROM bills WHERE id = $1 AND company_id = $2 FOR UPDATE`,
			id, cid).Scan(&number, &status, &paid, &je)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if p, _ := decimal.NewFromString(paid); !p.IsZero() {
			return nil, &apiError{status: http.StatusConflict, code: "bill_has_payments"}
		}
		var rev *string
		if je != nil {
			var rid string
			if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`, *je,
				fmt.Sprintf("Void bill #%d %s", number, strings.TrimSpace(in.Reason))).Scan(&rid); err != nil {
				return nil, err
			}
			rev = &rid
		}
		if err := reverseStockMoves(ctx, tx, cid, "bill", "bill_void", id, rev, fmt.Sprintf("Void bill #%d", number)); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE bills SET status = 'void', void_entry_id = $2,
			notes = CASE WHEN $3 = '' THEN notes ELSE trim(notes || ' ' || $3) END WHERE id = $1`, id, rev, strings.TrimSpace(in.Reason)); err != nil {
			return nil, err
		}
		return loadBill(ctx, tx, cid, id)
	})
}

// reverseStockMoves undoes the stock moves of a document (incoming moves are taken out, outgoing put back),
// at the same unit cost, so the stock value matches the reversed journal entry.
func reverseStockMoves(ctx context.Context, tx pgx.Tx, cid, source, voidSource, sourceID string, je *string, notes string) error {
	type mv struct {
		id, item, wh, name string
		qty, unit, value   decimal.Decimal
	}
	rows, err := tx.Query(ctx, `SELECT m.id::text, m.item_id::text, m.warehouse_id::text, it.name, m.quantity::text, m.unit_cost::text, m.value::text
		FROM stock_moves m JOIN items it ON it.id = m.item_id WHERE m.source = $1 AND m.source_id = $2`, source, sourceID)
	if err != nil {
		return err
	}
	moves, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mv, error) {
		var m mv
		var q, u, v string
		err := row.Scan(&m.id, &m.item, &m.wh, &m.name, &q, &u, &v)
		m.qty, _ = decimal.NewFromString(q)
		m.unit, _ = decimal.NewFromString(u)
		m.value, _ = decimal.NewFromString(v)
		return m, err
	})
	if err != nil {
		return err
	}
	date := today()
	for _, m := range moves {
		lots, err := moveLots(ctx, tx, m.id)
		if err != nil {
			return err
		}
		ctx := withLots(ctx, m.item, lots) // the same lots / serials go back
		if m.qty.IsPositive() {            // was incoming: take it out at its own cost
			q, _, err := lockStock(ctx, tx, m.item, m.wh)
			if err != nil {
				return err
			}
			if q.LessThan(m.qty) {
				return insufficientStock(m.name, q)
			}
			held, err := stockValueOf(ctx, tx, m.item, m.wh)
			if err != nil {
				return err
			}
			if held.LessThan(m.value) {
				return insufficientStock(m.name, q)
			}
			if err := setStock(ctx, tx, m.item, m.wh, q.Sub(m.qty), held.Sub(m.value)); err != nil {
				return err
			}
			var moveID string
			if err := tx.QueryRow(ctx, `INSERT INTO stock_moves (company_id, item_id, warehouse_id, move_date, quantity, unit_cost, value, source, source_id, journal_entry_id, notes)
				VALUES ($1, $2, $3, $4::date, $5::numeric, $6::numeric, $7::numeric, $8, $9, $10, $11) RETURNING id::text`,
				cid, m.item, m.wh, date, m.qty.Neg().String(), m.unit.String(), m.value.Neg().String(), voidSource, sourceID, je, notes).Scan(&moveID); err != nil {
				return err
			}
			if err := stockHooks(ctx, tx, cid, moveID, m.item, m.wh, m.qty.Neg(), m.value.Neg(), date, voidSource, sourceID); err != nil {
				return err
			}
		} else { // was outgoing: put it back
			if err := stockIn(ctx, tx, cid, m.item, m.wh, m.qty.Neg(), m.value.Neg(), date, voidSource, sourceID, je, notes); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- supplier payments ----

const supPayCols = `p.id, p.number, p.supplier_id, su.name AS supplier_name, p.payment_date, p.currency, p.amount::text AS amount,
	p.method_id, m.name AS method, p.paid_currency, p.paid_amount::text AS paid_amount, trim_scale(p.paid_rate)::text AS paid_rate,
	p.status, p.kind, p.bill_id, (SELECT b.number FROM bills b WHERE b.id = p.bill_id) AS bill_number, p.journal_entry_id, p.notes, p.created_at`

type supPayIn struct {
	SupplierID string `json:"supplier_id"`
	Date       string `json:"date"`
	Currency   string `json:"currency"`
	Amount     string `json:"amount"`
	MethodID   string `json:"method_id"`
	PaidAmount string `json:"paid_amount"`
	PaidRate   string `json:"paid_rate"`
	Notes      string `json:"notes"`

	allowCheque bool // internal (cheques module): the method may be a cheque box
}

// paySupplier books money paid to a supplier from one box (Dr payables / Cr box) and applies it to open bills.
func paySupplier(ctx context.Context, tx pgx.Tx, cid string, in supPayIn) (string, error) {
	paidAmt, ok := money(in.PaidAmount)
	if !validDate(in.Date) || !currencyRe.MatchString(in.Currency) || !isUUID(in.MethodID) || !ok || !paidAmt.IsPositive() {
		return "", badRequest("bad_request")
	}
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return "", err
	}
	ps, err := loadPurchSettings(ctx, tx, cid)
	if err != nil {
		return "", err
	}
	if ps.AP == nil {
		return "", notSetup("ap")
	}
	name, err := lockSupplier(ctx, tx, cid, in.SupplierID)
	if err != nil {
		return "", err
	}
	methods, err := loadMethods(ctx, tx, cid)
	if err != nil {
		return "", err
	}
	m, ok := methods[in.MethodID]
	if !ok || (m.Kind == "cheque" && !in.allowCheque) || (m.Kind != "cheque" && !m.Active) {
		return "", badRequest("invalid_tender")
	}
	given := strings.TrimSpace(in.PaidRate)
	if given != "" {
		if rv, err := decimal.NewFromString(given); err != nil || !rv.IsPositive() {
			return "", badRequest("invalid_rate")
		}
	}
	mRateS, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, in.Date, given)
	if err != nil {
		return "", err
	}
	pRateS, err := resolveRate(ctx, tx, cid, set.Base, in.Currency, in.Date, "")
	if err != nil {
		return "", err
	}
	mRate, _ := decimal.NewFromString(mRateS)
	pRate, _ := decimal.NewFromString(pRateS)
	applied := paidAmt.Mul(pRate).DivRound(mRate, 4)
	if strings.TrimSpace(in.Amount) != "" {
		a, ok := money(in.Amount)
		if !ok || !a.IsPositive() {
			return "", badRequest("invalid_amount")
		}
		applied = a
	}
	var number int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'supplier_payment')`, cid).Scan(&number); err != nil {
		return "", err
	}
	je, err := postJournal(ctx, tx, cid, set, in.Date, fmt.Sprintf("Supplier payment #%d · %s", number, name),
		fmt.Sprintf("SPAY-%d", number), "supplier_payment", []jline{
			{Account: *ps.AP, Desc: name, Currency: in.Currency, Debit: applied, Rate: pRateS},
			{Account: m.Account, Desc: m.Name, Currency: m.Currency, Credit: paidAmt, Rate: mRateS},
		})
	if err != nil {
		return "", err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO supplier_payments (company_id, number, supplier_id, payment_date, currency, amount, method_id,
		paid_currency, paid_amount, paid_rate, journal_entry_id, notes)
		VALUES ($1, $2, $3, $4::date, $5, $6::numeric, $7, $8, $9::numeric, $10::numeric, $11, $12) RETURNING id::text`,
		cid, number, in.SupplierID, in.Date, in.Currency, applied.String(), m.ID, m.Currency, paidAmt.String(), mRateS, je,
		strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
		return "", err
	}
	return id, autoAllocateSupplier(ctx, tx, cid, in.SupplierID, in.Currency)
}

func (s *Server) createSupplierPayment(w http.ResponseWriter, r *http.Request) {
	var in supPayIn
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	paidAmt, ok := money(in.PaidAmount)
	if !validDate(in.Date) || !currencyRe.MatchString(in.Currency) || !isUUID(in.MethodID) || !ok || !paidAmt.IsPositive() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		id, err := paySupplier(ctx, tx, cid, in)
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+supPayCols+`,
			coalesce((SELECT json_agg(json_build_object('bill_number', b.number, 'amount', a.amount::text)) FROM supplier_allocations a
			          JOIN bills b ON b.id = a.bill_id WHERE a.payment_id = p.id), '[]'::json) AS allocations
			FROM supplier_payments p JOIN suppliers su ON su.id = p.supplier_id JOIN payment_methods m ON m.id = p.method_id WHERE p.id = $1`, id)
	})
}

func (s *Server) listSupplierPayments(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	supplier, kind := r.URL.Query().Get("supplier_id"), r.URL.Query().Get("kind")
	if !ok || (supplier != "" && !isUUID(supplier)) || (kind != "" && kind != "payment" && kind != "debit_note") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+supPayCols+` FROM supplier_payments p JOIN suppliers su ON su.id = p.supplier_id
			LEFT JOIN payment_methods m ON m.id = p.method_id
			WHERE p.company_id = $1 AND p.payment_date BETWEEN $2::date AND $3::date AND ($4 = '' OR p.supplier_id = $4::uuid)
			AND ($5 = '' OR p.kind = $5)
			ORDER BY p.payment_date DESC, p.created_at DESC LIMIT 1000`, r.PathValue("cid"), from, to, supplier, kind)
	})
}

func (s *Server) voidSupplierPayment(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var cheque bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cheques WHERE company_id = $2 AND (supplier_payment_id = $1
			OR id IN (SELECT cheque_id FROM cheque_events WHERE supplier_payment_id = $1 AND NOT undone)))`, id, cid).Scan(&cheque); err != nil {
			return nil, err
		}
		if cheque { // paid with a cheque: undone from the cheques register
			return nil, &apiError{status: http.StatusConflict, code: "cheque_payment"}
		}
		if err := voidSupplierPaymentTx(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

// voidSupplierPaymentTx reverses a supplier payment (or debit note) and un-applies it from its bills.
func voidSupplierPaymentTx(ctx context.Context, tx pgx.Tx, cid, id string) error {
	var number int64
	var status, supplier, kind string
	var je *string
	err := tx.QueryRow(ctx, `SELECT number, status, supplier_id::text, journal_entry_id::text, kind FROM supplier_payments
		WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&number, &status, &supplier, &je, &kind)
	if err == pgx.ErrNoRows {
		return notFound()
	}
	if err != nil {
		return err
	}
	if status == "void" {
		return &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	if _, err := lockSupplier(ctx, tx, cid, supplier); err != nil {
		return err
	}
	fxRows, err := tx.Query(ctx, `SELECT a.fx_entry_id::text FROM supplier_allocations a JOIN journal_entries e ON e.id = a.fx_entry_id
		WHERE a.payment_id = $1 AND e.reversed_by IS NULL AND e.status = 'posted'`, id)
	if err != nil {
		return err
	}
	fxs, err := pgx.CollectRows(fxRows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, fx := range fxs {
		if _, err := tx.Exec(ctx, `SELECT reverse_journal_entry($1, current_date, $2)`, fx, fmt.Sprintf("Void supplier payment #%d FX", number)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO supplier_allocations (payment_id, bill_id, amount)
		SELECT payment_id, bill_id, -sum(amount) FROM supplier_allocations WHERE payment_id = $1
		GROUP BY payment_id, bill_id HAVING sum(amount) <> 0`, id); err != nil {
		return err
	}
	var rev *string
	if je != nil {
		var rid string
		if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`, *je,
			fmt.Sprintf("Void %s #%d", map[string]string{"payment": "supplier payment", "debit_note": "debit note"}[kind], number)).Scan(&rid); err != nil {
			return err
		}
		rev = &rid
	}
	if kind == "debit_note" { // returned goods come back into stock
		if err := reverseStockMoves(ctx, tx, cid, "debit_note", "debit_note_void", id, rev, fmt.Sprintf("Void debit note #%d", number)); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE supplier_payments SET status = 'void', void_entry_id = $2 WHERE id = $1`, id, rev)
	return err
}

// ---- expenses ----

const expenseCols = `e.id, e.number, e.expense_date, e.account_id, a.code AS account_code, a.name AS account_name, a.name_i18n AS account_name_i18n,
	e.method_id, m.name AS method, e.currency, e.amount::text AS amount, e.tax_amount::text AS tax_amount,
	(SELECT t.name FROM tax_rates t WHERE t.id = e.tax_rate_id) AS tax_name, e.description, e.supplier_id, su.name AS supplier_name,
	e.status, e.journal_entry_id, u.full_name AS created_by, e.created_at`

const expenseFrom = ` FROM expenses e JOIN accounts a ON a.id = e.account_id JOIN payment_methods m ON m.id = e.method_id
	LEFT JOIN suppliers su ON su.id = e.supplier_id LEFT JOIN users u ON u.id = e.created_by`

func (s *Server) createExpense(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date        string `json:"date"`
		AccountID   string `json:"account_id"`
		MethodID    string `json:"method_id"`
		Amount      string `json:"amount"`
		Description string `json:"description"`
		SupplierID  string `json:"supplier_id"`
		TaxRateID   string `json:"tax_rate_id"`  // optional recoverable tax (input VAT)
		TaxIncluded bool   `json:"tax_included"` // amount already includes the tax (default: the tax is added)
		CostCenter  string `json:"cost_center_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	amt, ok := money(in.Amount)
	if !validDate(in.Date) || !isUUID(in.AccountID) || !isUUID(in.MethodID) || !ok || !amt.IsPositive() ||
		(in.SupplierID != "" && !isUUID(in.SupplierID)) || (in.TaxRateID != "" && !isUUID(in.TaxRateID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		ctx = withCostCenter(ctx, in.CostCenter)
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if err := requirePostable(ctx, tx, cid, in.AccountID); err != nil {
			return nil, err
		}
		methods, err := loadMethods(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		m, ok := methods[in.MethodID]
		if !ok || !m.Active {
			return nil, badRequest("invalid_tender")
		}
		if in.SupplierID != "" {
			if _, err := lockSupplier(ctx, tx, cid, in.SupplierID); err != nil {
				return nil, err
			}
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'expense')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		desc := strings.TrimSpace(in.Description)
		rate, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, in.Date, "")
		if err != nil {
			return nil, err
		}
		tax, err := loadTaxRate(ctx, tx, cid, in.TaxRateID, "purchases")
		if err != nil {
			return nil, err
		}
		net, taxAmt, gross := amt, decimal.Zero, amt
		if tax != nil {
			dec, err := currencyDecimals(ctx, tx, m.Currency)
			if err != nil {
				return nil, err
			}
			if in.TaxIncluded {
				net = amt.Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(100).Add(tax.Rate)).Round(dec)
				taxAmt = amt.Sub(net)
			} else {
				taxAmt = taxOn(amt, tax, dec)
				gross = amt.Add(taxAmt)
			}
		}
		jl := []jline{{Account: in.AccountID, Desc: desc, Currency: m.Currency, Debit: net, Rate: rate}}
		var taxID *string
		if tax != nil {
			taxID = &tax.ID
			jl = append(jl, jline{Account: tax.Account, Desc: tax.Name + " · " + desc, Currency: m.Currency, Debit: taxAmt, Rate: rate})
		}
		jl = append(jl, jline{Account: m.Account, Desc: m.Name, Currency: m.Currency, Credit: gross, Rate: rate})
		je, err := postJournal(ctx, tx, cid, set, in.Date, fmt.Sprintf("Expense #%d · %s", number, desc), fmt.Sprintf("EXP-%d", number), "expense", jl)
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO expenses (company_id, number, expense_date, account_id, method_id, currency, amount, description,
			supplier_id, journal_entry_id, tax_rate_id, tax_amount)
			VALUES ($1, $2, $3::date, $4, $5, $6, $7::numeric, $8, nullif($9, '')::uuid, $10, $11, $12::numeric) RETURNING id::text`,
			cid, number, in.Date, in.AccountID, m.ID, m.Currency, gross.String(), desc, in.SupplierID, je, taxID, taxAmt.String()).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+expenseCols+expenseFrom+` WHERE e.id = $1`, id)
	})
}

func (s *Server) listExpenses(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	account := r.URL.Query().Get("account_id")
	if !ok || (account != "" && !isUUID(account)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cond := ` WHERE e.company_id = $1 AND e.expense_date BETWEEN $2::date AND $3::date AND ($4 = '' OR e.account_id = $4::uuid)`
		rows, err := queryJSON(ctx, tx, `SELECT `+expenseCols+expenseFrom+cond+` ORDER BY e.expense_date DESC, e.number DESC LIMIT 2000`,
			r.PathValue("cid"), from, to, account)
		if err != nil {
			return nil, err
		}
		totals, err := queryJSON(ctx, tx, `SELECT e.account_id, a.code AS account_code, a.name AS account_name, a.name_i18n AS account_name_i18n,
			e.currency, count(*) AS count, sum(e.amount)::text AS total`+expenseFrom+cond+` AND e.status = 'posted'
			GROUP BY e.account_id, a.code, a.name, a.name_i18n, e.currency ORDER BY a.code`, r.PathValue("cid"), from, to, account)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows, "totals": totals}, nil
	})
}

func (s *Server) voidExpense(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var number int64
		var status string
		var je *string
		err := tx.QueryRow(ctx, `SELECT number, status, journal_entry_id::text FROM expenses WHERE id = $1 AND company_id = $2 FOR UPDATE`,
			id, cid).Scan(&number, &status, &je)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		var rev *string
		if je != nil {
			var rid string
			if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`, *je,
				fmt.Sprintf("Void expense #%d", number)).Scan(&rid); err != nil {
				return nil, err
			}
			rev = &rid
		}
		if _, err := tx.Exec(ctx, `UPDATE expenses SET status = 'void', void_entry_id = $2 WHERE id = $1`, id, rev); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ---- fuel usage and stock adjustments ----

func (s *Server) createStockDoc(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind") // fuel | adjust
	var in struct {
		ItemID      string `json:"item_id"`
		WarehouseID string `json:"warehouse_id"`
		Quantity    string `json:"quantity"`  // fuel: positive quantity used; adjust: signed (+ found, - lost)
		UnitCost    string `json:"unit_cost"` // adjust +: optional cost per unit in base currency (default: current average)
		AccountID   string `json:"account_id"`
		Date        string `json:"date"`
		Notes       string `json:"notes"`
		GeneratorID string `json:"generator_id"` // fuel: which generator used it
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	if in.GeneratorID != "" && !isUUID(in.GeneratorID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	qty, err := decimal.NewFromString(strings.TrimSpace(in.Quantity))
	if (kind != "fuel" && kind != "adjust") || err != nil || qty.IsZero() || qty.Exponent() < -4 || (kind == "fuel" && qty.IsNegative()) ||
		!validDate(in.Date) || (in.AccountID != "" && !isUUID(in.AccountID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		ctx = withCostCenter(ctx, generatorCostCenter(ctx, tx, in.GeneratorID))
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		ps, err := loadPurchSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		it, err := loadItem(ctx, tx, cid, in.ItemID)
		if err != nil {
			return nil, err
		}
		if it.Kind != "stock" || it.Inventory == nil {
			return nil, badRequest("not_stock_item")
		}
		wh, err := checkWarehouse(ctx, tx, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		account := in.AccountID
		if account == "" {
			if kind == "fuel" && ps.Fuel != nil {
				account = *ps.Fuel
			} else if kind == "adjust" && ps.Adjust != nil {
				account = *ps.Adjust
			}
		}
		if account == "" {
			return nil, notSetup(kind)
		}
		if err := requirePostable(ctx, tx, cid, account); err != nil {
			return nil, err
		}
		var genID *string
		if in.GeneratorID != "" {
			if err := checkGenerator(ctx, tx, cid, in.GeneratorID); err != nil {
				return nil, err
			}
			genID = &in.GeneratorID
		}
		have, avg, err := lockStock(ctx, tx, it.ID, wh)
		if err != nil {
			return nil, err
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, $2)`, cid, "stock_"+kind).Scan(&number); err != nil {
			return nil, err
		}
		label := map[string]string{"fuel": "Fuel used", "adjust": "Stock adjustment"}[kind]
		memo := fmt.Sprintf("%s #%d · %s %s %s", label, number, qty.Abs().String(), it.Unit, it.Name)
		var value decimal.Decimal
		var lines []jline
		out := qty.IsNegative() || kind == "fuel"
		amount := qty.Abs()
		if out {
			if have.LessThan(amount) {
				return nil, insufficientStock(it.Name, have)
			}
			if value, err = outValue(ctx, tx, it.ID, wh, have, avg, amount); err != nil {
				return nil, err
			}
			lines = []jline{
				{Account: account, Desc: memo, Currency: set.Base, Debit: value},
				{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Credit: value},
			}
		} else {
			cost := avg
			if strings.TrimSpace(in.UnitCost) != "" {
				c, ok := money(in.UnitCost)
				if !ok {
					return nil, badRequest("invalid_amount")
				}
				cost = c
			}
			value = amount.Mul(cost).Round(4)
			lines = []jline{
				{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Debit: value},
				{Account: account, Desc: memo, Currency: set.Base, Credit: value},
			}
		}
		var je *string
		if value.IsPositive() {
			id, err := postJournal(ctx, tx, cid, set, in.Date, memo, fmt.Sprintf("STK-%d", number), "stock", lines)
			if err != nil {
				return nil, err
			}
			je = &id
		}
		var docID string
		signedValue := value
		if out {
			signedValue = value.Neg()
		}
		if err := tx.QueryRow(ctx, `INSERT INTO stock_docs (company_id, number, kind, doc_date, item_id, warehouse_id, quantity, value, account_id,
			notes, journal_entry_id, generator_id) VALUES ($1, $2, $3, $4::date, $5, $6, $7::numeric, $8::numeric, $9, $10, $11, $12) RETURNING id::text`,
			cid, number, kind, in.Date, it.ID, wh, func() string {
				if out {
					return amount.Neg().String()
				}
				return amount.String()
			}(), signedValue.String(), account, strings.TrimSpace(in.Notes), je, genID).Scan(&docID); err != nil {
			return nil, err
		}
		if out {
			err = stockOut(ctx, tx, cid, it.ID, wh, amount, avg, value, in.Date, kind, docID, je, strings.TrimSpace(in.Notes))
		} else {
			err = stockIn(ctx, tx, cid, it.ID, wh, amount, value, in.Date, kind, docID, je, strings.TrimSpace(in.Notes))
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+stockDocCols+stockDocFrom+` WHERE d.id = $1`, docID)
	})
}

const stockDocCols = `d.id, d.number, d.kind, d.doc_date, d.item_id, it.code AS item_code, it.name AS item_name, it.unit, w.name AS warehouse,
	d.quantity::text AS quantity, d.value::text AS value, a.code AS account_code, a.name AS account_name, d.notes,
	d.generator_id, (SELECT g.name FROM generators g WHERE g.id = d.generator_id) AS generator_name,
	u.full_name AS created_by, d.created_at, d.status`

const stockDocFrom = ` FROM stock_docs d JOIN items it ON it.id = d.item_id JOIN warehouses w ON w.id = d.warehouse_id
	JOIN accounts a ON a.id = d.account_id LEFT JOIN users u ON u.id = d.created_by`

func (s *Server) listStockDocs(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	kind := r.URL.Query().Get("kind")
	if !ok || (kind != "" && kind != "fuel" && kind != "adjust") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT `+stockDocCols+stockDocFrom+`
			WHERE d.company_id = $1 AND d.doc_date BETWEEN $2::date AND $3::date AND ($4 = '' OR d.kind = $4)
			ORDER BY d.doc_date DESC, d.created_at DESC LIMIT 1000`, r.PathValue("cid"), from, to, kind)
		if err != nil {
			return nil, err
		}
		totals, err := queryJSON(ctx, tx, `SELECT it.name AS item_name, it.unit, d.kind, sum(d.quantity)::text AS quantity, sum(d.value)::text AS value
			FROM stock_docs d JOIN items it ON it.id = d.item_id
			WHERE d.company_id = $1 AND d.doc_date BETWEEN $2::date AND $3::date AND ($4 = '' OR d.kind = $4) AND d.status = 'posted'
			GROUP BY it.name, it.unit, d.kind ORDER BY it.name`, r.PathValue("cid"), from, to, kind)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows, "totals": totals}, nil
	})
}

// ---- selling stock items on customer invoices ----

type saleLine struct {
	ItemID, WarehouseID string
	Qty                 decimal.Decimal
}

// postSaleCOGS takes sold stock items out at average cost and books Dr cost of sales / Cr inventory.
func postSaleCOGS(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, invoiceID string, number int64, date string, lines []saleLine) error {
	if len(lines) == 0 {
		return nil
	}
	lines, err := expandKits(ctx, tx, cid, lines)
	if err != nil {
		return err
	}
	ps, err := loadPurchSettings(ctx, tx, cid)
	if err != nil {
		return err
	}
	type out struct {
		it       *itemInfo
		wh       string
		qty, avg decimal.Decimal
		value    decimal.Decimal
		cogsAcct string
	}
	outs := []out{}
	jl := []jline{}
	for _, l := range lines {
		it, err := loadItem(ctx, tx, cid, l.ItemID)
		if err != nil {
			return err
		}
		if it.Kind != "stock" {
			continue
		}
		wh, err := checkWarehouse(ctx, tx, cid, l.WarehouseID)
		if err != nil {
			return err
		}
		have, avg, err := lockStock(ctx, tx, it.ID, wh)
		if err != nil {
			return err
		}
		if have.LessThan(l.Qty) {
			return insufficientStock(it.Name, have)
		}
		cogs := ""
		if it.Expense != nil {
			cogs = *it.Expense
		} else if ps.COGS != nil {
			cogs = *ps.COGS
		}
		if cogs == "" {
			return notSetup("cogs")
		}
		v, err := outValue(ctx, tx, it.ID, wh, have, avg, l.Qty)
		if err != nil {
			return err
		}
		outs = append(outs, out{it, wh, l.Qty, avg, v, cogs})
		if v.IsPositive() {
			jl = append(jl, jline{Account: cogs, Desc: it.Name, Currency: set.Base, Debit: v},
				jline{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Credit: v})
		}
	}
	var je *string
	if len(jl) > 0 {
		id, err := postJournal(ctx, tx, cid, set, date, fmt.Sprintf("Cost of sales · invoice #%d", number), fmt.Sprintf("INV-%d", number), "sale", jl)
		if err != nil {
			return err
		}
		je = &id
	}
	for _, o := range outs {
		if err := stockOut(ctx, tx, cid, o.it.ID, o.wh, o.qty, o.avg, o.value, date, "sale", invoiceID, je, fmt.Sprintf("Invoice #%d", number)); err != nil {
			return err
		}
	}
	return nil
}

// reverseSaleCOGS puts sold stock back when an invoice is voided and reverses the cost-of-sales entry.
func reverseSaleCOGS(ctx context.Context, tx pgx.Tx, cid, invoiceID string, number int64) error {
	var je *string
	err := tx.QueryRow(ctx, `SELECT journal_entry_id::text FROM stock_moves WHERE source = 'sale' AND source_id = $1 AND journal_entry_id IS NOT NULL LIMIT 1`,
		invoiceID).Scan(&je)
	if err == pgx.ErrNoRows {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stock_moves WHERE source = 'sale' AND source_id = $1)`, invoiceID).Scan(&exists); err != nil || !exists {
			return err
		}
	} else if err != nil {
		return err
	}
	var rev *string
	if je != nil {
		var rid string
		if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`, *je,
			fmt.Sprintf("Void cost of sales · invoice #%d", number)).Scan(&rid); err != nil {
			return err
		}
		rev = &rid
	}
	return reverseStockMoves(ctx, tx, cid, "sale", "sale_void", invoiceID, rev, fmt.Sprintf("Void invoice #%d", number))
}

// ---- reports ----

func (s *Server) apAging(w http.ResponseWriter, r *http.Request) {
	asOf := r.URL.Query().Get("as_of")
	if asOf == "" {
		asOf = today()
	}
	if !validDate(asOf) {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		bucket := `CASE WHEN $2::date - b.due_date <= 0 THEN 'current' WHEN $2::date - b.due_date <= 30 THEN 'd30'
			WHEN $2::date - b.due_date <= 60 THEN 'd60' WHEN $2::date - b.due_date <= 90 THEN 'd90' ELSE 'over90' END`
		parts := []string{}
		for _, b := range []string{"current", "d30", "d60", "d90", "over90"} {
			parts = append(parts, fmt.Sprintf("coalesce(sum(b.total - b.paid) FILTER (WHERE %s = '%s'), 0)::text AS %s", bucket, b, b))
		}
		cols := strings.Join(parts, ", ")
		where := `b.company_id = $1 AND b.status IN ('open', 'partial') AND b.bill_date <= $2::date`
		rows, err := queryJSON(ctx, tx, `SELECT su.id AS supplier_id, su.code, su.name, su.phone, b.currency, `+cols+`,
			sum(b.total - b.paid)::text AS total, count(*) AS bills, min(b.due_date) AS oldest_due
			FROM bills b JOIN suppliers su ON su.id = b.supplier_id WHERE `+where+`
			GROUP BY su.id, su.code, su.name, su.phone, b.currency ORDER BY sum(b.total - b.paid) DESC`, cid, asOf)
		if err != nil {
			return nil, err
		}
		totals, err := queryJSON(ctx, tx, `SELECT b.currency, `+cols+`, sum(b.total - b.paid)::text AS total
			FROM bills b WHERE `+where+` GROUP BY b.currency ORDER BY b.currency`, cid, asOf)
		if err != nil {
			return nil, err
		}
		return map[string]any{"as_of": asOf, "rows": rows, "totals": totals}, nil
	})
}

func (s *Server) stockValuation(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rows, err := queryJSON(ctx, tx, `SELECT it.id AS item_id, it.code, it.name, it.name_i18n, it.unit, w.name AS warehouse,
			st.quantity::text AS quantity, st.avg_cost::text AS avg_cost, st.value::text AS value
			FROM stock st JOIN items it ON it.id = st.item_id JOIN warehouses w ON w.id = st.warehouse_id
			WHERE it.company_id = $1 AND st.quantity <> 0 ORDER BY it.name, w.name`, cid)
		if err != nil {
			return nil, err
		}
		var total, ledger string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(st.value), 0)::text
			FROM stock st JOIN items it ON it.id = st.item_id WHERE it.company_id = $1`, cid).Scan(&total); err != nil {
			return nil, err
		}
		// what the inventory accounts say in the ledger, to check the two agree
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::text FROM journal_lines l
			JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
			WHERE e.company_id = $1 AND l.account_id IN (SELECT DISTINCT inventory_account_id FROM items WHERE company_id = $1 AND kind = 'stock')`,
			cid).Scan(&ledger); err != nil {
			return nil, err
		}
		return map[string]any{"base_currency": base, "rows": rows, "total": total, "ledger_total": ledger}, nil
	})
}

// expensesReport: expense accounts per period from the ledger (bills, expenses, fuel, journal entries...).
func (s *Server) expensesReport(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rows, err := queryJSON(ctx, tx, `SELECT a.id, a.code, a.name, a.name_i18n, sum(l.base_debit - l.base_credit)::text AS amount,
			sum(l.entries) AS entries
			FROM `+ledgerTotals("$1", "$2", "$3")+`
			WHERE a.type = 'expense'
			GROUP BY a.id HAVING sum(l.base_debit - l.base_credit) <> 0 ORDER BY sum(l.base_debit - l.base_credit) DESC`, cid, from, to)
		if err != nil {
			return nil, err
		}
		var total string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::text FROM `+ledgerTotals("$1", "$2", "$3")+`
			WHERE a.type = 'expense'`, cid, from, to).Scan(&total); err != nil {
			return nil, err
		}
		return map[string]any{"base_currency": base, "from": from, "to": to, "rows": rows, "total": total}, nil
	})
}
