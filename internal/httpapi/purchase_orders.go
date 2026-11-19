package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Purchase orders: a request for quotation (rfq) is sent to the supplier, confirmed into an order, then billed
// (fully or in parts). Billing a PO line creates the normal supplier bill (stock comes in with the bill), and the
// order remembers what was billed, so nothing is billed twice and the open quantity "on order" is known.

const poCols = `o.id, o.number, o.supplier_id, su.name AS supplier_name, su.phone AS supplier_phone, o.order_date, o.expected_date,
	o.currency, o.status, o.supplier_ref, o.notes, o.total::text AS total, o.cost_center_id, o.confirmed_at, o.created_at,
	(SELECT count(*) FROM bills b WHERE b.purchase_order_id = o.id AND b.status <> 'void') AS bills,
	(SELECT coalesce(sum(b.total), 0) FROM bills b WHERE b.purchase_order_id = o.id AND b.status <> 'void')::text AS billed_total,
	(o.expected_date IS NOT NULL AND o.expected_date < current_date AND o.status IN ('confirmed', 'partial')) AS late`

const poFrom = ` FROM purchase_orders o JOIN suppliers su ON su.id = o.supplier_id`

type poLineIn struct {
	ItemID      string `json:"item_id"`
	WarehouseID string `json:"warehouse_id"`
	AccountID   string `json:"account_id"`
	UnitID      string `json:"unit_id"`
	Description string `json:"description"`
	Quantity    string `json:"quantity"`
	UnitPrice   string `json:"unit_price"`
	TaxRateID   string `json:"tax_rate_id"`
}

type poIn struct {
	SupplierID   string     `json:"supplier_id"`
	OrderDate    string     `json:"order_date"`
	ExpectedDate string     `json:"expected_date"`
	Currency     string     `json:"currency"`
	SupplierRef  string     `json:"supplier_ref"`
	Notes        string     `json:"notes"`
	CostCenterID string     `json:"cost_center_id"`
	Lines        []poLineIn `json:"lines"`
}

func (in *poIn) valid() bool {
	if in.OrderDate == "" {
		in.OrderDate = today()
	}
	if !isUUID(in.SupplierID) || !validDate(in.OrderDate) || (in.ExpectedDate != "" && !validDate(in.ExpectedDate)) ||
		!currencyRe.MatchString(in.Currency) || len(in.Lines) == 0 || len(in.Lines) > 200 || (in.CostCenterID != "" && !isUUID(in.CostCenterID)) {
		return false
	}
	for _, l := range in.Lines {
		for _, u := range []string{l.ItemID, l.WarehouseID, l.AccountID, l.UnitID, l.TaxRateID} {
			if u != "" && !isUUID(u) {
				return false
			}
		}
		q, ok1 := money(l.Quantity)
		p, ok2 := money(l.UnitPrice)
		if strings.TrimSpace(l.UnitPrice) == "" && l.ItemID != "" {
			ok2, p = true, decimal.Zero // filled from the supplier's price list
		}
		if !ok1 || !ok2 || !q.IsPositive() || p.IsNegative() || (l.ItemID == "" && strings.TrimSpace(l.Description) == "") {
			return false
		}
	}
	return true
}

// writePOLines (re)writes the lines of an order and its total (lines + tax).
func writePOLines(ctx context.Context, tx pgx.Tx, cid, id, currency string, lines []poLineIn) error {
	if _, err := tx.Exec(ctx, `DELETE FROM purchase_order_lines WHERE order_id = $1 AND company_id = $2`, id, cid); err != nil {
		return err
	}
	dec, err := currencyDecimals(ctx, tx, currency)
	if err != nil {
		return err
	}
	total := decimal.Zero
	var supplier, orderDate string
	if err := tx.QueryRow(ctx, `SELECT supplier_id::text, order_date::text FROM purchase_orders WHERE id = $1`, id).Scan(&supplier, &orderDate); err != nil {
		return err
	}
	for i, l := range lines {
		q, _ := money(l.Quantity)
		p, _ := money(l.UnitPrice)
		if strings.TrimSpace(l.UnitPrice) == "" && l.ItemID != "" {
			sp, _, err := supplierPrice(ctx, tx, cid, supplier, l.ItemID, currency, orderDate, q)
			if err != nil {
				return err
			}
			p = sp
		}
		desc := strings.TrimSpace(l.Description)
		if l.ItemID != "" {
			it, err := loadItem(ctx, tx, cid, l.ItemID)
			if err != nil {
				return err
			}
			if desc == "" {
				desc = it.Name
			}
			if l.UnitID != "" {
				if _, _, err := unitFactor(ctx, tx, it.ID, l.UnitID); err != nil {
					return err
				}
			}
			if it.Kind == "stock" && l.WarehouseID != "" {
				if _, err := checkWarehouse(ctx, tx, cid, l.WarehouseID); err != nil {
					return err
				}
			}
		}
		if l.AccountID != "" {
			if err := requirePostable(ctx, tx, cid, l.AccountID); err != nil {
				return err
			}
		}
		tax, err := loadTaxRate(ctx, tx, cid, l.TaxRateID, "purchases")
		if err != nil {
			return err
		}
		amt := q.Mul(p).Round(dec)
		total = total.Add(amt).Add(taxOn(amt, tax, dec))
		if _, err := tx.Exec(ctx, `INSERT INTO purchase_order_lines (company_id, order_id, line_no, item_id, warehouse_id, account_id, unit_id,
			description, quantity, unit_price, tax_rate_id) VALUES ($1, $2, $3, nullif($4, '')::uuid, nullif($5, '')::uuid, nullif($6, '')::uuid,
			nullif($7, '')::uuid, $8, $9::numeric, $10::numeric, nullif($11, '')::uuid)`,
			cid, id, i+1, l.ItemID, l.WarehouseID, l.AccountID, l.UnitID, desc, q.String(), p.String(), l.TaxRateID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE purchase_orders SET total = $2::numeric, updated_at = now() WHERE id = $1`, id, total.String())
	return err
}

func loadPO(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	po, err := queryJSONOne(ctx, tx, `SELECT `+poCols+`, co.name_en AS company_name, co.name_ar AS company_name_ar`+poFrom+`
		JOIN companies co ON co.id = o.company_id WHERE o.id = $1 AND o.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.item_id, it.code AS item_code, it.unit, it.kind AS item_kind, l.warehouse_id,
		l.account_id, l.unit_id, u.name AS unit_name, l.description, trim_scale(l.quantity)::text AS quantity, trim_scale(l.unit_price)::text AS unit_price,
		l.tax_rate_id, (SELECT t.name FROM tax_rates t WHERE t.id = l.tax_rate_id) AS tax_name,
		trim_scale(l.billed_qty)::text AS billed_qty, trim_scale(greatest(l.quantity - l.billed_qty, 0))::text AS open_qty,
		trim_scale(l.received_qty)::text AS received_qty, trim_scale(greatest(l.quantity - l.received_qty, 0))::text AS to_receive
		FROM purchase_order_lines l LEFT JOIN items it ON it.id = l.item_id LEFT JOIN item_units u ON u.id = l.unit_id
		WHERE l.order_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	bills, err := queryJSON(ctx, tx, `SELECT id, number, bill_date, total::text AS total, status FROM bills WHERE purchase_order_id = $1 ORDER BY number`, id)
	if err != nil {
		return nil, err
	}
	receipts, err := queryJSON(ctx, tx, `SELECT g.id, g.number, g.receipt_date, g.delivery_ref, g.status,
		(SELECT trim_scale(sum(gl.quantity))::text FROM goods_receipt_lines gl WHERE gl.receipt_id = g.id) AS quantity
		FROM goods_receipts g WHERE g.order_id = $1 ORDER BY g.number`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"order": po, "lines": lines, "bills": bills, "receipts": receipts}, nil
}

// GET /companies/{cid}/purchase-orders?status=open|all|<status>&supplier_id=
func (s *Server) listPOs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, sup := q.Get("status"), q.Get("supplier_id")
	if st == "" {
		st = "all"
	}
	if sup != "" && !isUUID(sup) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+poCols+poFrom+` WHERE o.company_id = $1
			AND ($2 = 'all' OR ($2 = 'open' AND o.status IN ('rfq', 'sent', 'confirmed', 'partial')) OR o.status = $2)
			AND ($3 = '' OR o.supplier_id = nullif($3, '')::uuid)
			ORDER BY o.order_date DESC, o.number DESC LIMIT 1000`, r.PathValue("cid"), st, sup)
	})
}

func (s *Server) getPO(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadPO(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// POST /companies/{cid}/purchase-orders: a new request for quotation.
func (s *Server) createPO(w http.ResponseWriter, r *http.Request) {
	var in poIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := lockSupplier(ctx, tx, cid, in.SupplierID); err != nil {
			return nil, err
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'purchase_order')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO purchase_orders (company_id, number, supplier_id, order_date, expected_date, currency, supplier_ref,
			notes, cost_center_id) VALUES ($1, $2, $3, $4::date, nullif($5, '')::date, $6, $7, $8, nullif($9, '')::uuid) RETURNING id::text`,
			cid, n, in.SupplierID, in.OrderDate, in.ExpectedDate, in.Currency, strings.TrimSpace(in.SupplierRef), strings.TrimSpace(in.Notes),
			in.CostCenterID).Scan(&id); err != nil {
			return nil, err
		}
		if err := writePOLines(ctx, tx, cid, id, in.Currency, in.Lines); err != nil {
			return nil, err
		}
		return loadPO(ctx, tx, cid, id)
	})
}

// lockPO returns the status of an order, locked for update.
func lockPO(ctx context.Context, tx pgx.Tx, cid, id string) (string, error) {
	if !isUUID(id) {
		return "", notFound()
	}
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM purchase_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notFound()
	}
	return st, err
}

// PUT /companies/{cid}/purchase-orders/{id}: edit an order that is not billed yet.
func (s *Server) updatePO(w http.ResponseWriter, r *http.Request) {
	var in poIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		st, err := lockPO(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if st != "rfq" && st != "sent" && st != "confirmed" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := lockSupplier(ctx, tx, cid, in.SupplierID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE purchase_orders SET supplier_id = $3, order_date = $4::date, expected_date = nullif($5, '')::date,
			currency = $6, supplier_ref = $7, notes = $8, cost_center_id = nullif($9, '')::uuid WHERE id = $1 AND company_id = $2`,
			id, cid, in.SupplierID, in.OrderDate, in.ExpectedDate, in.Currency, strings.TrimSpace(in.SupplierRef), strings.TrimSpace(in.Notes),
			in.CostCenterID); err != nil {
			return nil, err
		}
		if err := writePOLines(ctx, tx, cid, id, in.Currency, in.Lines); err != nil {
			return nil, err
		}
		return loadPO(ctx, tx, cid, id)
	})
}

// POST /companies/{cid}/purchase-orders/{id}/{action}: send (rfq → sent, returns the message text), confirm, cancel.
func (s *Server) poAction(w http.ResponseWriter, r *http.Request) {
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	allowed := map[string][]string{"send": {"rfq", "sent"}, "confirm": {"rfq", "sent"}, "cancel": {"rfq", "sent", "confirmed"}}
	from, ok := allowed[action]
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		st, err := lockPO(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		okFrom := false
		for _, f := range from {
			okFrom = okFrom || f == st
		}
		if !okFrom {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		switch action {
		case "send":
			_, err = tx.Exec(ctx, `UPDATE purchase_orders SET status = 'sent', updated_at = now() WHERE id = $1`, id)
		case "confirm":
			_, err = tx.Exec(ctx, `UPDATE purchase_orders SET status = 'confirmed', confirmed_at = now(), updated_at = now() WHERE id = $1`, id)
		case "cancel":
			_, err = tx.Exec(ctx, `UPDATE purchase_orders SET status = 'cancelled', updated_at = now() WHERE id = $1`, id)
		}
		if err != nil {
			return nil, err
		}
		out, err := loadPO(ctx, tx, cid, id)
		if err != nil || action != "send" {
			return out, err
		}
		text, err := poMessage(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"po": out, "message": text}, nil
	})
}

// poMessage is a plain text request for quotation / order, ready for WhatsApp.
func poMessage(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var number int64
	var company, supplier, cur, date string
	var expected *string
	if err := tx.QueryRow(ctx, `SELECT o.number, co.name_en, su.name, o.currency, o.order_date::text, o.expected_date::text
		FROM purchase_orders o JOIN companies co ON co.id = o.company_id JOIN suppliers su ON su.id = o.supplier_id WHERE o.id = $1`, id).
		Scan(&number, &company, &supplier, &cur, &date, &expected); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, `SELECT l.description, trim_scale(l.quantity)::text, coalesce(u.name, it.unit, '') FROM purchase_order_lines l
		LEFT JOIN items it ON it.id = l.item_id LEFT JOIN item_units u ON u.id = l.unit_id WHERE l.order_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "%s — PO #%d (%s)\n%s\n", company, number, date, supplier)
	for rows.Next() {
		var d, q, u string
		if err := rows.Scan(&d, &q, &u); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "• %s × %s %s\n", d, q, u)
	}
	if expected != nil {
		fmt.Fprintf(&b, "Needed by: %s\n", *expected)
	}
	fmt.Fprintf(&b, "Currency: %s", cur)
	return b.String(), rows.Err()
}

// POST /companies/{cid}/purchase-orders/{id}/bill {bill_date, due_date, supplier_ref, notes, lines: [{po_line_id, quantity, unit_price,
// warehouse_id, lots}]}: bills the order (no lines = everything still open). Billing more than ordered is refused.
func (s *Server) billPO(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BillDate    string `json:"bill_date"`
		DueDate     string `json:"due_date"`
		SupplierRef string `json:"supplier_ref"`
		Notes       string `json:"notes"`
		Lines       []struct {
			POLineID    string   `json:"po_line_id"`
			Quantity    string   `json:"quantity"`
			UnitPrice   string   `json:"unit_price"`
			WarehouseID string   `json:"warehouse_id"`
			Lots        []lotQty `json:"lots"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		st, err := lockPO(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if st != "confirmed" && st != "partial" {
			return nil, &apiError{status: http.StatusConflict, code: "po_not_confirmed"}
		}
		var sup, cur, cc, ref string
		if err := tx.QueryRow(ctx, `SELECT supplier_id::text, currency, coalesce(cost_center_id::text, ''), supplier_ref FROM purchase_orders WHERE id = $1`, id).
			Scan(&sup, &cur, &cc, &ref); err != nil {
			return nil, err
		}
		type pol struct {
			id, item, wh, acc, unit, desc, tax string
			qty, billed, price, received       decimal.Decimal
		}
		var matchMode, tolS string
		var anyReceipt bool
		if err := tx.QueryRow(ctx, `SELECT three_way_match, match_price_tolerance::text,
			EXISTS (SELECT 1 FROM goods_receipts g WHERE g.order_id = $2 AND g.status = 'done') FROM companies WHERE id = $1`, cid, id).
			Scan(&matchMode, &tolS, &anyReceipt); err != nil {
			return nil, err
		}
		tol, _ := decimal.NewFromString(tolS)
		warnings := []map[string]string{}
		rows, err := tx.Query(ctx, `SELECT id::text, coalesce(item_id::text, ''), coalesce(warehouse_id::text, ''), coalesce(account_id::text, ''),
			coalesce(unit_id::text, ''), description, coalesce(tax_rate_id::text, ''), quantity::text, billed_qty::text, unit_price::text, received_qty::text
			FROM purchase_order_lines WHERE order_id = $1 ORDER BY line_no FOR UPDATE`, id)
		if err != nil {
			return nil, err
		}
		pls, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pol, error) {
			var p pol
			var q, b, pr, rc string
			err := row.Scan(&p.id, &p.item, &p.wh, &p.acc, &p.unit, &p.desc, &p.tax, &q, &b, &pr, &rc)
			p.qty, _ = decimal.NewFromString(q)
			p.billed, _ = decimal.NewFromString(b)
			p.price, _ = decimal.NewFromString(pr)
			p.received, _ = decimal.NewFromString(rc)
			return p, err
		})
		if err != nil {
			return nil, err
		}
		byID := map[string]pol{}
		for _, p := range pls {
			byID[p.id] = p
		}
		bill := billInput{SupplierID: sup, SupplierRef: strings.TrimSpace(in.SupplierRef), BillDate: in.BillDate, DueDate: in.DueDate,
			Currency: cur, Notes: strings.TrimSpace(in.Notes), CostCenter: cc, poID: id}
		if bill.SupplierRef == "" {
			bill.SupplierRef = ref
		}
		add := func(p pol, qty, price decimal.Decimal, wh string, lots []lotQty) error {
			if !qty.IsPositive() {
				return nil
			}
			if qty.GreaterThan(p.qty.Sub(p.billed)) {
				return &apiError{status: http.StatusUnprocessableEntity, code: "over_billing", detail: p.desc}
			}
			// 3-way match: what is billed should have been received, at the ordered price
			if matchMode != "off" {
				if (anyReceipt || matchMode == "block") && qty.GreaterThan(p.received.Sub(p.billed)) {
					if matchMode == "block" {
						return &apiError{status: http.StatusUnprocessableEntity, code: "not_received", detail: p.desc}
					}
					warnings = append(warnings, map[string]string{"code": "not_received", "line": p.desc,
						"billed": qty.String(), "received": decimal.Max(p.received.Sub(p.billed), decimal.Zero).String()})
				}
				if p.price.IsPositive() && price.Sub(p.price).Abs().Mul(pct100).Div(p.price).GreaterThan(tol) {
					if matchMode == "block" {
						return &apiError{status: http.StatusUnprocessableEntity, code: "price_mismatch", detail: p.desc}
					}
					warnings = append(warnings, map[string]string{"code": "price_mismatch", "line": p.desc, "ordered": p.price.String(), "billed": price.String()})
				}
			}
			if wh == "" {
				wh = p.wh
			}
			bill.Lines = append(bill.Lines, billLineIn{ItemID: p.item, WarehouseID: wh, AccountID: p.acc, Description: p.desc,
				Quantity: qty.String(), UnitPrice: price.String(), TaxRateID: p.tax, UnitID: p.unit, Lots: lots, poLineID: p.id})
			_, err := tx.Exec(ctx, `UPDATE purchase_order_lines SET billed_qty = billed_qty + $2::numeric WHERE id = $1`, p.id, qty.String())
			return err
		}
		if len(in.Lines) == 0 {
			for _, p := range pls {
				if err := add(p, p.qty.Sub(p.billed), p.price, "", nil); err != nil {
					return nil, err
				}
			}
		}
		for _, l := range in.Lines {
			p, ok := byID[l.POLineID]
			if !ok {
				return nil, badRequest("invalid_line")
			}
			q, ok1 := money(l.Quantity)
			price := p.price
			if strings.TrimSpace(l.UnitPrice) != "" {
				var ok2 bool
				if price, ok2 = money(l.UnitPrice); !ok2 || price.IsNegative() {
					return nil, badRequest("invalid_amount")
				}
			}
			if !ok1 || q.IsNegative() || (l.WarehouseID != "" && !isUUID(l.WarehouseID)) {
				return nil, badRequest("invalid_amount")
			}
			if err := add(p, q, price, l.WarehouseID, l.Lots); err != nil {
				return nil, err
			}
		}
		if len(bill.Lines) == 0 {
			return nil, badRequest("nothing_to_bill")
		}
		if !bill.normalize() {
			return nil, badRequest("bad_request")
		}
		billID, err := createBillTx(ctx, tx, cid, bill)
		if err != nil {
			return nil, err
		}
		if err := refreshPOBilled(ctx, tx, id); err != nil {
			return nil, err
		}
		b, err := loadBill(ctx, tx, cid, billID)
		if err != nil {
			return nil, err
		}
		po, err := loadPO(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"bill": b, "po": po, "warnings": warnings}, nil
	})
}

// refreshPOBilled recomputes billed quantities from the order's non-void bills (in the order line's unit) and its status.
func refreshPOBilled(ctx context.Context, tx pgx.Tx, poID string) error {
	if _, err := tx.Exec(ctx, `UPDATE purchase_order_lines l SET billed_qty = coalesce((
			SELECT sum(bl.quantity / coalesce((SELECT u.factor FROM item_units u WHERE u.id = l.unit_id), 1))
			FROM bill_lines bl JOIN bills b ON b.id = bl.bill_id WHERE bl.po_line_id = l.id AND b.status <> 'void'), 0)
		WHERE l.order_id = $1`, poID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE purchase_orders o SET status = CASE
			WHEN NOT EXISTS (SELECT 1 FROM purchase_order_lines l WHERE l.order_id = o.id AND l.billed_qty < l.quantity) THEN 'billed'
			WHEN EXISTS (SELECT 1 FROM purchase_order_lines l WHERE l.order_id = o.id AND l.billed_qty > 0) THEN 'partial'
			ELSE 'confirmed' END, updated_at = now()
		WHERE o.id = $1 AND o.status IN ('confirmed', 'partial', 'billed')`, poID)
	return err
}

// POST /companies/{cid}/purchase-orders/from-reorder {warehouse_id?}: one request for quotation per supplier for every
// item at or below its minimum (quantity = up to the maximum, minus what is already on order), priced at the last purchase price.
func (s *Server) poFromReorder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WarehouseID string `json:"warehouse_id"`
		Currency    string `json:"currency"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if (in.WarehouseID != "" && !isUUID(in.WarehouseID)) || (in.Currency != "" && !currencyRe.MatchString(in.Currency)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		cur := in.Currency
		if cur == "" {
			cur = base
		}
		rows, err := tx.Query(ctx, `
			SELECT r.supplier_id::text, r.item_id::text, r.warehouse_id::text,
			  (r.max_qty - coalesce(s.quantity, 0) - coalesce((SELECT sum((l.quantity - l.billed_qty) * coalesce(u.factor, 1))
			     FROM purchase_order_lines l JOIN purchase_orders o ON o.id = l.order_id LEFT JOIN item_units u ON u.id = l.unit_id
			     WHERE l.item_id = r.item_id AND coalesce(l.warehouse_id, r.warehouse_id) = r.warehouse_id
			       AND o.status IN ('rfq', 'sent', 'confirmed', 'partial')), 0))::text,
			  coalesce((SELECT bl.unit_price FROM bill_lines bl JOIN bills b ON b.id = bl.bill_id
			     WHERE bl.item_id = r.item_id AND b.status <> 'void' AND b.currency = $3 ORDER BY b.bill_date DESC, b.number DESC LIMIT 1), 0)::text
			FROM reorder_rules r JOIN items i ON i.id = r.item_id
			LEFT JOIN stock s ON s.item_id = r.item_id AND s.warehouse_id = r.warehouse_id
			WHERE r.company_id = $1 AND r.supplier_id IS NOT NULL AND i.is_active AND coalesce(s.quantity, 0) <= r.min_qty
			  AND ($2 = '' OR r.warehouse_id = nullif($2, '')::uuid)
			ORDER BY r.supplier_id, i.name`, cid, in.WarehouseID, cur)
		if err != nil {
			return nil, err
		}
		type want struct{ sup, item, wh, qty, price string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (want, error) {
			var x want
			err := row.Scan(&x.sup, &x.item, &x.wh, &x.qty, &x.price)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		bySup := map[string][]poLineIn{}
		order := []string{}
		for _, x := range list {
			q, _ := decimal.NewFromString(x.qty)
			if !q.IsPositive() {
				continue
			}
			if _, ok := bySup[x.sup]; !ok {
				order = append(order, x.sup)
			}
			bySup[x.sup] = append(bySup[x.sup], poLineIn{ItemID: x.item, WarehouseID: x.wh, Quantity: q.String(), UnitPrice: x.price})
		}
		created := []any{}
		for _, sup := range order {
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'purchase_order')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO purchase_orders (company_id, number, supplier_id, order_date, currency, notes)
				VALUES ($1, $2, $3, current_date, $4, 'Reorder') RETURNING id::text`, cid, n, sup, cur).Scan(&id); err != nil {
				return nil, err
			}
			if err := writePOLines(ctx, tx, cid, id, cur, bySup[sup]); err != nil {
				return nil, err
			}
			created = append(created, map[string]any{"id": id, "number": n, "supplier_id": sup, "lines": len(bySup[sup])})
		}
		return map[string]any{"created": created}, nil
	})
}
