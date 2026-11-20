package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Goods receipts and supplier price lists. A receipt records what the supplier actually delivered against a confirmed
// purchase order (quantities only; stock and its cost come in with the supplier bill, as before). Billing then checks
// the three documents (3-way match): billed ≤ received, bill price ≈ order price (tolerance %). Company setting
// three_way_match: off | warn (default: the bill is booked and the warnings are returned) | block (refused).

// POST /companies/{cid}/purchase-orders/{id}/receive {date, delivery_ref, notes, lines: [{po_line_id, quantity}]}
// (no lines = everything still to receive).
func (s *Server) receivePO(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date        string `json:"date"`
		DeliveryRef string `json:"delivery_ref"`
		Notes       string `json:"notes"`
		Lines       []struct {
			POLineID string `json:"po_line_id"`
			Quantity string `json:"quantity"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	if !validDate(in.Date) || len(in.Lines) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		st, err := lockPO(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if st != "confirmed" && st != "partial" && st != "billed" {
			return nil, &apiError{status: http.StatusConflict, code: "po_not_confirmed"}
		}
		type ln struct {
			id, desc     string
			qty, receive decimal.Decimal
		}
		rows, err := tx.Query(ctx, `SELECT id::text, description, quantity::text, received_qty::text FROM purchase_order_lines
			WHERE order_id = $1 ORDER BY line_no FOR UPDATE`, id)
		if err != nil {
			return nil, err
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ln, error) {
			var x ln
			var q, rc string
			err := row.Scan(&x.id, &x.desc, &q, &rc)
			x.qty, _ = decimal.NewFromString(q)
			rcv, _ := decimal.NewFromString(rc)
			x.receive = x.qty.Sub(rcv) // open to receive
			return x, err
		})
		if err != nil {
			return nil, err
		}
		want := map[string]decimal.Decimal{}
		if len(in.Lines) == 0 {
			for _, x := range list {
				if x.receive.IsPositive() {
					want[x.id] = x.receive
				}
			}
		}
		for _, l := range in.Lines {
			q, ok := money(l.Quantity)
			if !ok || q.IsNegative() || !isUUID(l.POLineID) {
				return nil, badRequest("invalid_amount")
			}
			if q.IsPositive() {
				want[l.POLineID] = want[l.POLineID].Add(q)
			}
		}
		if len(want) == 0 {
			return nil, badRequest("nothing_to_receive")
		}
		known := map[string]ln{}
		for _, x := range list {
			known[x.id] = x
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'goods_receipt')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var gid string
		if err := tx.QueryRow(ctx, `INSERT INTO goods_receipts (company_id, number, order_id, receipt_date, delivery_ref, notes)
			VALUES ($1, $2, $3, $4::date, $5, $6) RETURNING id::text`, cid, n, id, in.Date, strings.TrimSpace(in.DeliveryRef), strings.TrimSpace(in.Notes)).Scan(&gid); err != nil {
			return nil, err
		}
		for _, x := range list { // in line order
			q, ok := want[x.id]
			if !ok {
				continue
			}
			if q.GreaterThan(x.receive) {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "over_receipt", detail: x.desc}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO goods_receipt_lines (company_id, receipt_id, po_line_id, quantity) VALUES ($1, $2, $3, $4::numeric)`,
				cid, gid, x.id, q.String()); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE purchase_order_lines SET received_qty = received_qty + $2::numeric WHERE id = $1`, x.id, q.String()); err != nil {
				return nil, err
			}
			delete(want, x.id)
		}
		if len(want) > 0 {
			return nil, badRequest("invalid_line")
		}
		_ = known
		// warehouses with bins: the goods land in the input area / wait for putaway (QC first in 3 steps)
		if err := whOnReceipt(ctx, tx, cid, gid, in.Date); err != nil {
			return nil, err
		}
		// receipt quality checks (manufacturing pro)
		if err := mfgOnReceipt(ctx, tx, cid, gid); err != nil {
			return nil, err
		}
		return loadPO(ctx, tx, cid, id)
	})
}

// POST /companies/{cid}/goods-receipts/{id}/cancel: takes a receipt back (refused when the goods are already billed).
func (s *Server) cancelReceipt(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var order, st string
		err := tx.QueryRow(ctx, `SELECT order_id::text, status FROM goods_receipts WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&order, &st)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st != "done" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := lockPO(ctx, tx, cid, order); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE purchase_order_lines l SET received_qty = l.received_qty - g.quantity
			FROM goods_receipt_lines g WHERE g.receipt_id = $1 AND g.po_line_id = l.id`, id); err != nil {
			return nil, err
		}
		var over bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM purchase_order_lines WHERE order_id = $1 AND received_qty < 0)`, order).Scan(&over); err != nil {
			return nil, err
		}
		if over {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := tx.Exec(ctx, `UPDATE goods_receipts SET status = 'cancelled' WHERE id = $1`, id); err != nil {
			return nil, err
		}
		if err := whOnReceiptCancelled(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE mfg_quality_checks SET status = 'cancelled' WHERE receipt_id = $1 AND status = 'todo'`, id); err != nil {
			return nil, err
		}
		return loadPO(ctx, tx, cid, order)
	})
}

// GET /companies/{cid}/purchase-orders/{id}/match: ordered / received / billed per line, with the billed price.
func (s *Server) matchPO(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := lockPO(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		rows, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.description, trim_scale(l.quantity)::text AS ordered, trim_scale(l.received_qty)::text AS received,
			trim_scale(l.billed_qty)::text AS billed, trim_scale(l.unit_price)::text AS order_price,
			(SELECT trim_scale(round(sum(bl.quantity * bl.unit_price) / nullif(sum(bl.quantity), 0), 6))::text FROM bill_lines bl JOIN bills b ON b.id = bl.bill_id
			   WHERE bl.po_line_id = l.id AND b.status <> 'void') AS billed_price,
			(l.billed_qty > l.received_qty) AS billed_not_received, (l.received_qty > l.billed_qty) AS received_not_billed
			FROM purchase_order_lines l WHERE l.order_id = $1 ORDER BY l.line_no`, id)
		if err != nil {
			return nil, err
		}
		set, err := queryJSONOne(ctx, tx, `SELECT three_way_match AS mode, match_price_tolerance::text AS tolerance FROM companies WHERE id = $1`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"lines": rows, "settings": set}, nil
	})
}

// PUT /companies/{cid}/purchasing/matching {mode, tolerance}
func (s *Server) putMatching(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode      string `json:"mode"`
		Tolerance string `json:"tolerance"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	tol, ok := money(in.Tolerance)
	if (in.Mode != "off" && in.Mode != "warn" && in.Mode != "block") || !ok || tol.IsNegative() || tol.GreaterThan(decimal.NewFromInt(100)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE companies SET three_way_match = $2, match_price_tolerance = $3::numeric WHERE id = $1`,
			r.PathValue("cid"), in.Mode, tol.String()); err != nil {
			return nil, err
		}
		return map[string]string{"mode": in.Mode, "tolerance": tol.String()}, nil
	})
}

func (s *Server) getMatching(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT three_way_match AS mode, match_price_tolerance::text AS tolerance FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}

// ---------------- supplier price lists ----------------

// supplierPrice: the supplier's price for qty of item in currency on date (best matching break), 0 when none.
func supplierPrice(ctx context.Context, tx pgx.Tx, cid, supplier, item, currency, date string, qty decimal.Decimal) (decimal.Decimal, string, error) {
	var p, code string
	err := tx.QueryRow(ctx, `SELECT price::text, supplier_code FROM supplier_prices WHERE company_id = $1 AND supplier_id = $2 AND item_id = $3
		AND currency = $4 AND min_qty <= $5::numeric AND (valid_from IS NULL OR valid_from <= $6::date) AND (valid_to IS NULL OR valid_to >= $6::date)
		ORDER BY min_qty DESC, created_at DESC LIMIT 1`, cid, supplier, item, currency, qty.String(), date).Scan(&p, &code)
	if errors.Is(err, pgx.ErrNoRows) {
		return decimal.Zero, "", nil
	}
	d, _ := decimal.NewFromString(p)
	return d, code, err
}

const supplierPriceCols = `p.id, p.supplier_id, su.name AS supplier_name, p.item_id, it.code AS item_code, it.name AS item_name, it.unit,
	p.currency, trim_scale(p.price)::text AS price, trim_scale(p.min_qty)::text AS min_qty, p.supplier_code, p.lead_days, p.valid_from, p.valid_to`

// GET /companies/{cid}/supplier-prices?supplier_id=&item_id=
func (s *Server) listSupplierPrices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sup, item := q.Get("supplier_id"), q.Get("item_id")
	if (sup != "" && !isUUID(sup)) || (item != "" && !isUUID(item)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+supplierPriceCols+` FROM supplier_prices p JOIN suppliers su ON su.id = p.supplier_id JOIN items it ON it.id = p.item_id
			WHERE p.company_id = $1 AND ($2 = '' OR p.supplier_id = nullif($2, '')::uuid) AND ($3 = '' OR p.item_id = nullif($3, '')::uuid)
			ORDER BY it.name, su.name, p.currency, p.min_qty LIMIT 2000`, r.PathValue("cid"), sup, item)
	})
}

// POST /companies/{cid}/supplier-prices (new) · PUT /companies/{cid}/supplier-prices/{id}
func (s *Server) saveSupplierPrice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SupplierID   string `json:"supplier_id"`
		ItemID       string `json:"item_id"`
		Currency     string `json:"currency"`
		Price        string `json:"price"`
		MinQty       string `json:"min_qty"`
		SupplierCode string `json:"supplier_code"`
		LeadDays     int    `json:"lead_days"`
		ValidFrom    string `json:"valid_from"`
		ValidTo      string `json:"valid_to"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.MinQty == "" {
		in.MinQty = "0"
	}
	price, ok1 := money(in.Price)
	minq, ok2 := money(in.MinQty)
	id := r.PathValue("id")
	if !isUUID(in.SupplierID) || !isUUID(in.ItemID) || !currencyRe.MatchString(in.Currency) || !ok1 || !ok2 || price.IsNegative() || minq.IsNegative() ||
		in.LeadDays < 0 || in.LeadDays > 365 || (in.ValidFrom != "" && !validDate(in.ValidFrom)) || (in.ValidTo != "" && !validDate(in.ValidTo)) ||
		(id != "" && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := lockSupplier(ctx, tx, cid, in.SupplierID); err != nil {
			return nil, err
		}
		if _, err := loadItem(ctx, tx, cid, in.ItemID); err != nil {
			return nil, err
		}
		if id == "" {
			err := tx.QueryRow(ctx, `INSERT INTO supplier_prices (company_id, supplier_id, item_id, currency, price, min_qty, supplier_code, lead_days, valid_from, valid_to)
				VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7, $8, nullif($9, '')::date, nullif($10, '')::date) RETURNING id::text`,
				cid, in.SupplierID, in.ItemID, in.Currency, price.String(), minq.String(), strings.TrimSpace(in.SupplierCode), in.LeadDays, in.ValidFrom, in.ValidTo).Scan(&id)
			if err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE supplier_prices SET supplier_id = $3, item_id = $4, currency = $5, price = $6::numeric, min_qty = $7::numeric,
				supplier_code = $8, lead_days = $9, valid_from = nullif($10, '')::date, valid_to = nullif($11, '')::date WHERE id = $1 AND company_id = $2`,
				id, cid, in.SupplierID, in.ItemID, in.Currency, price.String(), minq.String(), strings.TrimSpace(in.SupplierCode), in.LeadDays, in.ValidFrom, in.ValidTo)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+supplierPriceCols+` FROM supplier_prices p JOIN suppliers su ON su.id = p.supplier_id
			JOIN items it ON it.id = p.item_id WHERE p.id = $1`, id)
	})
}

func (s *Server) deleteSupplierPrice(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		tag, err := tx.Exec(ctx, `DELETE FROM supplier_prices WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// GET /companies/{cid}/supplier-prices/best?supplier_id=&item_id=&currency=&qty=&date=
func (s *Server) bestSupplierPrice(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	qty, ok := money(defaultStr(q.Get("qty"), "1"))
	date := defaultStr(q.Get("date"), today())
	if !isUUID(q.Get("supplier_id")) || !isUUID(q.Get("item_id")) || !currencyRe.MatchString(q.Get("currency")) || !ok || !validDate(date) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		p, code, err := supplierPrice(ctx, tx, r.PathValue("cid"), q.Get("supplier_id"), q.Get("item_id"), q.Get("currency"), date, qty)
		if err != nil {
			return nil, err
		}
		return map[string]any{"price": p.String(), "found": p.IsPositive() || code != "", "supplier_code": code}, nil
	})
}
