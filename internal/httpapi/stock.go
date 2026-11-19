package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Void of fuel / adjustment documents, warehouse transfers and the stock movements report.

func (s *Server) voidStockDoc(w http.ResponseWriter, r *http.Request) {
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
		var kind, status string
		var je *string
		err := tx.QueryRow(ctx, `SELECT number, kind, status, journal_entry_id::text FROM stock_docs WHERE id = $1 AND company_id = $2 FOR UPDATE`,
			id, cid).Scan(&number, &kind, &status, &je)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		label := map[string]string{"fuel": "fuel used", "adjust": "stock adjustment"}[kind]
		reason := strings.TrimSpace(in.Reason)
		var rev *string
		if je != nil {
			var rid string
			if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`, *je,
				strings.TrimSpace(fmt.Sprintf("Void %s #%d %s", label, number, reason))).Scan(&rid); err != nil {
				return nil, err
			}
			rev = &rid
		}
		if err := reverseStockMoves(ctx, tx, cid, kind, "stock_void", id, rev, fmt.Sprintf("Void %s #%d", label, number)); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_docs SET status = 'void', void_entry_id = $2,
			notes = CASE WHEN $3 = '' THEN notes ELSE trim(notes || ' ' || $3) END WHERE id = $1`, id, rev, reason); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+stockDocCols+stockDocFrom+` WHERE d.id = $1`, id)
	})
}

// ---- warehouse transfers ----

const transferCols = `t.id, t.number, t.transfer_date, t.item_id, it.code AS item_code, it.name AS item_name, it.unit,
	t.from_warehouse, wf.name AS from_name, t.to_warehouse, wt.name AS to_name, t.quantity::text AS quantity, t.value::text AS value,
	t.status, t.notes, u.full_name AS created_by, t.created_at`

const transferFrom = ` FROM stock_transfers t JOIN items it ON it.id = t.item_id JOIN warehouses wf ON wf.id = t.from_warehouse
	JOIN warehouses wt ON wt.id = t.to_warehouse LEFT JOIN users u ON u.id = t.created_by`

func (s *Server) createStockTransfer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ItemID   string `json:"item_id"`
		From     string `json:"from_warehouse_id"`
		To       string `json:"to_warehouse_id"`
		Quantity string `json:"quantity"`
		Date     string `json:"date"`
		Notes    string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	qty, ok := money(in.Quantity)
	if !ok || !qty.IsPositive() || !validDate(in.Date) || !isUUID(in.From) || !isUUID(in.To) || in.From == in.To {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT coalesce(lock_date >= $2::date, false) FROM companies WHERE id = $1`, cid, in.Date).Scan(&locked); err != nil {
			return nil, err
		}
		if locked {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "period_locked"}
		}
		it, err := loadItem(ctx, tx, cid, in.ItemID)
		if err != nil {
			return nil, err
		}
		if it.Kind != "stock" {
			return nil, badRequest("not_stock_item")
		}
		for _, wh := range []string{in.From, in.To} {
			if _, err := checkWarehouse(ctx, tx, cid, wh); err != nil {
				return nil, err
			}
		}
		// lock both stock rows in a fixed order (two opposite transfers at once cannot deadlock)
		first, second := in.From, in.To
		if second < first {
			first, second = second, first
		}
		if _, _, err := lockStock(ctx, tx, it.ID, first); err != nil {
			return nil, err
		}
		if _, _, err := lockStock(ctx, tx, it.ID, second); err != nil {
			return nil, err
		}
		have, avg, err := lockStock(ctx, tx, it.ID, in.From)
		if err != nil {
			return nil, err
		}
		if have.LessThan(qty) {
			return nil, insufficientStock(it.Name, have)
		}
		value, err := outValue(ctx, tx, it.ID, in.From, have, avg, qty)
		if err != nil {
			return nil, err
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'stock_transfer')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		var id string
		notes := strings.TrimSpace(in.Notes)
		if err := tx.QueryRow(ctx, `INSERT INTO stock_transfers (company_id, number, transfer_date, item_id, from_warehouse, to_warehouse, quantity, value, notes)
			VALUES ($1, $2, $3::date, $4, $5, $6, $7::numeric, $8::numeric, $9) RETURNING id::text`,
			cid, number, in.Date, it.ID, in.From, in.To, qty.String(), value.String(), notes).Scan(&id); err != nil {
			return nil, err
		}
		memo := fmt.Sprintf("Transfer #%d", number)
		if notes != "" {
			memo += " · " + notes
		}
		lctx := withLotCarry(ctx) // the lots taken out arrive in the other warehouse
		if err := stockOut(lctx, tx, cid, it.ID, in.From, qty, avg, value, in.Date, "transfer", id, nil, memo); err != nil {
			return nil, err
		}
		if err := stockIn(lctx, tx, cid, it.ID, in.To, qty, value, in.Date, "transfer", id, nil, memo); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+transferCols+transferFrom+` WHERE t.id = $1`, id)
	})
}

func (s *Server) listStockTransfers(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+transferCols+transferFrom+`
			WHERE t.company_id = $1 AND t.transfer_date BETWEEN $2::date AND $3::date
			ORDER BY t.transfer_date DESC, t.number DESC LIMIT 1000`, r.PathValue("cid"), from, to)
	})
}

func (s *Server) voidStockTransfer(w http.ResponseWriter, r *http.Request) {
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
		var status, item, fromWh, toWh string
		err := tx.QueryRow(ctx, `SELECT number, status, item_id::text, from_warehouse::text, to_warehouse::text FROM stock_transfers
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&number, &status, &item, &fromWh, &toWh)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		first, second := fromWh, toWh
		if second < first {
			first, second = second, first
		}
		if _, _, err := lockStock(ctx, tx, item, first); err != nil {
			return nil, err
		}
		if _, _, err := lockStock(ctx, tx, item, second); err != nil {
			return nil, err
		}
		if err := reverseStockMoves(ctx, tx, cid, "transfer", "transfer_void", id, nil, fmt.Sprintf("Void transfer #%d", number)); err != nil {
			return nil, err
		}
		reason := strings.TrimSpace(in.Reason)
		if _, err := tx.Exec(ctx, `UPDATE stock_transfers SET status = 'void',
			notes = CASE WHEN $2 = '' THEN notes ELSE trim(notes || ' ' || $2) END WHERE id = $1`, id, reason); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+transferCols+transferFrom+` WHERE t.id = $1`, id)
	})
}

// stockMovesReport: every stock movement in the period with the running quantity and value per item and warehouse,
// plus the opening and closing balance of each item/warehouse.
func (s *Server) stockMovesReport(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	q := r.URL.Query()
	item, wh := q.Get("item_id"), q.Get("warehouse_id")
	if !ok || (item != "" && !isUUID(item)) || (wh != "" && !isUUID(wh)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		const filter = `m.company_id = $1 AND ($4 = '' OR m.item_id = $4::uuid) AND ($5 = '' OR m.warehouse_id = $5::uuid)`
		rows, err := queryJSON(ctx, tx, `SELECT * FROM (
			SELECT m.id, m.move_date, m.source, m.source_id, m.item_id, it.code AS item_code, it.name AS item_name, it.unit,
			  m.warehouse_id, w.name AS warehouse, m.quantity::text AS quantity, trim_scale(m.unit_cost)::text AS unit_cost, m.value::text AS value,
			  (sum(m.quantity) OVER run)::text AS balance_qty, (sum(m.value) OVER run)::text AS balance_value, m.notes, m.created_at
			FROM stock_moves m JOIN items it ON it.id = m.item_id JOIN warehouses w ON w.id = m.warehouse_id
			WHERE `+filter+` AND m.move_date <= $3::date
			WINDOW run AS (PARTITION BY m.item_id, m.warehouse_id ORDER BY m.move_date, m.created_at, m.id)) x
			WHERE x.move_date >= $2::date ORDER BY x.item_name, x.warehouse, x.move_date, x.created_at LIMIT 5000`,
			cid, from, to, item, wh)
		if err != nil {
			return nil, err
		}
		summary, err := queryJSON(ctx, tx, `SELECT m.item_id, it.code AS item_code, it.name AS item_name, it.unit, m.warehouse_id, w.name AS warehouse,
			coalesce(sum(m.quantity) FILTER (WHERE m.move_date < $2::date), 0)::text AS opening_qty,
			coalesce(sum(m.value) FILTER (WHERE m.move_date < $2::date), 0)::text AS opening_value,
			coalesce(sum(m.quantity) FILTER (WHERE m.move_date >= $2::date AND m.quantity > 0), 0)::text AS in_qty,
			coalesce(-sum(m.quantity) FILTER (WHERE m.move_date >= $2::date AND m.quantity < 0), 0)::text AS out_qty,
			sum(m.quantity)::text AS closing_qty, sum(m.value)::text AS closing_value
			FROM stock_moves m JOIN items it ON it.id = m.item_id JOIN warehouses w ON w.id = m.warehouse_id
			WHERE `+filter+` AND m.move_date <= $3::date
			GROUP BY m.item_id, it.code, it.name, it.unit, m.warehouse_id, w.name ORDER BY it.name, w.name`, cid, from, to, item, wh)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(base)
		return map[string]json.RawMessage{"base_currency": b, "rows": rows, "summary": summary}, nil
	})
}
