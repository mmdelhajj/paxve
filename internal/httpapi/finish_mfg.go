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

// Manufacturing (basic): a bill of materials says which components (and how much labour / overhead) make one batch of
// a finished item. A manufacturing order consumes the components from stock at their cost (FIFO or average, per item)
// and brings the finished item in at the rolled-up cost: components + labour + overhead, spread over the good units
// (scrapped units carry no value of their own: their cost stays in the good ones). Scrap of any stock item is booked
// to the scrap account.

const bomCols = `b.id, b.item_id, it.code AS item_code, it.name AS item_name, it.unit, b.name, trim_scale(b.output_qty)::text AS output_qty,
	b.labour_cost::text AS labour_cost, b.overhead_cost::text AS overhead_cost, b.notes, b.is_active,
	(SELECT count(*) FROM bom_lines l WHERE l.bom_id = b.id) AS components`

type bomLineIn struct {
	ItemID   string `json:"item_id"`
	Quantity string `json:"quantity"`
}

func (s *Server) listBOMs(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+bomCols+` FROM boms b JOIN items it ON it.id = b.item_id WHERE b.company_id = $1 ORDER BY it.name, b.name`, r.PathValue("cid"))
	})
}

func loadBOM(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	b, err := queryJSONOne(ctx, tx, `SELECT `+bomCols+` FROM boms b JOIN items it ON it.id = b.item_id WHERE b.id = $1 AND b.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.item_id, it.code AS item_code, it.name AS item_name, it.unit, trim_scale(l.quantity)::text AS quantity,
		coalesce((SELECT CASE WHEN sum(st.quantity) > 0 THEN sum(st.value) / sum(st.quantity) END FROM stock st WHERE st.item_id = l.item_id), 0)::numeric(20, 6)::text AS unit_cost
		FROM bom_lines l JOIN items it ON it.id = l.item_id WHERE l.bom_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"bom": b, "lines": lines}, nil
}

func (s *Server) getBOM(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadBOM(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// POST /companies/{cid}/mfg/boms · PUT /companies/{cid}/mfg/boms/{id}
func (s *Server) saveBOM(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ItemID       string      `json:"item_id"`
		Name         string      `json:"name"`
		OutputQty    string      `json:"output_qty"`
		LabourCost   string      `json:"labour_cost"`
		OverheadCost string      `json:"overhead_cost"`
		Notes        string      `json:"notes"`
		IsActive     *bool       `json:"is_active"`
		Lines        []bomLineIn `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	out, ok1 := money(defaultStr(in.OutputQty, "1"))
	lab, ok2 := money(defaultStr(in.LabourCost, "0"))
	ovh, ok3 := money(defaultStr(in.OverheadCost, "0"))
	ok := ok1 && ok2 && ok3 && out.IsPositive() && !lab.IsNegative() && !ovh.IsNegative() && isUUID(in.ItemID) && len(in.Lines) > 0 && len(in.Lines) <= 200 &&
		(id == "" || isUUID(id))
	for _, l := range in.Lines {
		q, okq := money(l.Quantity)
		ok = ok && okq && q.IsPositive() && isUUID(l.ItemID) && l.ItemID != in.ItemID
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	active := in.IsActive == nil || *in.IsActive
	cid := r.PathValue("cid")
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		it, err := loadItem(ctx, tx, cid, in.ItemID)
		if err != nil {
			return nil, err
		}
		if it.Kind != "stock" || it.Inventory == nil {
			return nil, badRequest("not_stock_item")
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = it.Name
		}
		if id == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO boms (company_id, item_id, name, output_qty, labour_cost, overhead_cost, notes, is_active)
				VALUES ($1, $2, $3, $4::numeric, $5::numeric, $6::numeric, $7, $8) RETURNING id::text`, cid, in.ItemID, name, out.String(), lab.String(), ovh.String(),
				strings.TrimSpace(in.Notes), active).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE boms SET item_id = $3, name = $4, output_qty = $5::numeric, labour_cost = $6::numeric, overhead_cost = $7::numeric,
				notes = $8, is_active = $9 WHERE id = $1 AND company_id = $2`, id, cid, in.ItemID, name, out.String(), lab.String(), ovh.String(), strings.TrimSpace(in.Notes), active)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
			if _, err := tx.Exec(ctx, `DELETE FROM bom_lines WHERE bom_id = $1`, id); err != nil {
				return nil, err
			}
		}
		for i, l := range in.Lines {
			c, err := loadItem(ctx, tx, cid, l.ItemID)
			if err != nil {
				return nil, err
			}
			if c.Kind != "stock" {
				return nil, badRequest("not_stock_item")
			}
			q, _ := money(l.Quantity)
			if _, err := tx.Exec(ctx, `INSERT INTO bom_lines (company_id, bom_id, line_no, item_id, quantity) VALUES ($1, $2, $3, $4, $5::numeric)`,
				cid, id, i+1, l.ItemID, q.String()); err != nil {
				return nil, err
			}
		}
		return loadBOM(ctx, tx, cid, id)
	})
}

const mfgCols = `o.id, o.number, o.bom_id, b.name AS bom_name, o.item_id, it.code AS item_code, it.name AS item_name, it.unit,
	trim_scale(o.quantity)::text AS quantity, o.warehouse_id, w.name AS warehouse, o.order_date, o.status, o.components_value::text AS components_value,
	o.labour::text AS labour, o.overhead::text AS overhead, trim_scale(o.scrap_qty)::text AS scrap_qty, trim_scale(o.unit_cost)::text AS unit_cost,
	o.journal_entry_id, o.notes, o.created_at, o.done_at`

const mfgFrom = ` FROM mfg_orders o JOIN boms b ON b.id = o.bom_id JOIN items it ON it.id = o.item_id JOIN warehouses w ON w.id = o.warehouse_id`

func loadMfgOrder(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	o, err := queryJSONOne(ctx, tx, `SELECT `+mfgCols+mfgFrom+` WHERE o.id = $1 AND o.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.item_id, it.code AS item_code, it.name AS item_name, it.unit, trim_scale(l.planned_qty)::text AS planned_qty,
		trim_scale(l.consumed_qty)::text AS consumed_qty, l.value::text AS value,
		coalesce((SELECT trim_scale(st.quantity)::text FROM stock st JOIN mfg_orders o ON o.id = l.order_id WHERE st.item_id = l.item_id AND st.warehouse_id = o.warehouse_id), '0') AS in_stock
		FROM mfg_order_lines l JOIN items it ON it.id = l.item_id WHERE l.order_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"order": o, "lines": lines}, nil
}

func (s *Server) listMfgOrders(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+mfgCols+mfgFrom+` WHERE o.company_id = $1 AND ($2 = '' OR o.status = $2) ORDER BY o.order_date DESC, o.number DESC LIMIT 1000`,
			r.PathValue("cid"), st)
	})
}

func (s *Server) getMfgOrder(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadMfgOrder(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// POST /companies/{cid}/mfg/orders {bom_id, quantity, warehouse_id, date, notes}: a draft order with the planned components.
func (s *Server) createMfgOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BomID       string `json:"bom_id"`
		Quantity    string `json:"quantity"`
		WarehouseID string `json:"warehouse_id"`
		Date        string `json:"date"`
		Notes       string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	qty, ok := money(in.Quantity)
	if !isUUID(in.BomID) || !ok || !qty.IsPositive() || !validDate(in.Date) || (in.WarehouseID != "" && !isUUID(in.WarehouseID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var item, outS, labS, ovhS string
		var active bool
		err := tx.QueryRow(ctx, `SELECT item_id::text, output_qty::text, labour_cost::text, overhead_cost::text, is_active FROM boms WHERE id = $1 AND company_id = $2`,
			in.BomID, cid).Scan(&item, &outS, &labS, &ovhS, &active)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
			return nil, badRequest("invalid_bom")
		}
		if err != nil {
			return nil, err
		}
		wh, err := checkWarehouse(ctx, tx, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		out, _ := decimal.NewFromString(outS)
		lab, _ := decimal.NewFromString(labS)
		ovh, _ := decimal.NewFromString(ovhS)
		batches := qty.Div(out)
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'mfg_order')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO mfg_orders (company_id, number, bom_id, item_id, quantity, warehouse_id, order_date, labour, overhead, notes)
			VALUES ($1, $2, $3, $4, $5::numeric, $6, $7::date, $8::numeric, $9::numeric, $10) RETURNING id::text`, cid, n, in.BomID, item, qty.String(), wh, in.Date,
			lab.Mul(batches).Round(4).String(), ovh.Mul(batches).Round(4).String(), strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mfg_order_lines (company_id, order_id, line_no, item_id, planned_qty, consumed_qty)
			SELECT $1, $2, l.line_no, l.item_id, round(l.quantity * $3::numeric, 4), round(l.quantity * $3::numeric, 4) FROM bom_lines l WHERE l.bom_id = $4`,
			cid, id, batches.String(), in.BomID); err != nil {
			return nil, err
		}
		return loadMfgOrder(ctx, tx, cid, id)
	})
}

// mfgAbsorbAccount: where labour / overhead charged to production is credited (created on first use).
func mfgAbsorbAccount(ctx context.Context, tx pgx.Tx, cid, col string) (string, error) {
	var id *string
	if err := tx.QueryRow(ctx, `SELECT `+col+`::text FROM companies WHERE id = $1`, cid).Scan(&id); err != nil {
		return "", err
	}
	if id != nil {
		return *id, nil
	}
	codes, parents := []string{"4950"}, []string{"4900", "4"}
	if lebanesePCG(ctx, tx, cid) {
		codes, parents = []string{"791"}, []string{"79", "7"}
	}
	acc, err := ensureAccount(ctx, tx, cid, codes, "income", "Production costs absorbed", "تكاليف إنتاج محمّلة على المخزون", "Transferts de charges de production", parents)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `UPDATE companies SET mfg_labour_account_id = coalesce(mfg_labour_account_id, $2::uuid),
		mfg_overhead_account_id = coalesce(mfg_overhead_account_id, $2::uuid) WHERE id = $1`, cid, acc)
	return acc, err
}

// POST /companies/{cid}/mfg/orders/{id}/produce {date, scrap_qty, lines: [{line_id, quantity}]}: consumes the components
// (the quantities actually used, default = planned) and brings the good units of the finished item in.
func (s *Server) produceMfgOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date     string `json:"date"`
		ScrapQty string `json:"scrap_qty"`
		Lines    []struct {
			LineID   string `json:"line_id"`
			Quantity string `json:"quantity"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	scrap, ok := money(defaultStr(in.ScrapQty, "0"))
	if !ok || scrap.IsNegative() || (in.Date != "" && !validDate(in.Date)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		var st, item, wh, qtyS, labS, ovhS, date string
		var number int64
		err := tx.QueryRow(ctx, `SELECT status, item_id::text, warehouse_id::text, quantity::text, labour::text, overhead::text, order_date::text, number
			FROM mfg_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st, &item, &wh, &qtyS, &labS, &ovhS, &date, &number)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st != "draft" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.Date != "" {
			date = in.Date
		}
		qty, _ := decimal.NewFromString(qtyS)
		lab, _ := decimal.NewFromString(labS)
		ovh, _ := decimal.NewFromString(ovhS)
		good := qty.Sub(scrap)
		if !good.IsPositive() {
			return nil, badRequest("invalid_amount")
		}
		for _, l := range in.Lines {
			q, ok := money(l.Quantity)
			if !ok || q.IsNegative() || !isUUID(l.LineID) {
				return nil, badRequest("invalid_amount")
			}
			tag, err := tx.Exec(ctx, `UPDATE mfg_order_lines SET consumed_qty = $3::numeric WHERE id = $1 AND order_id = $2`, l.LineID, id, q.String())
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, badRequest("invalid_line")
			}
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		fin, err := loadItem(ctx, tx, cid, item)
		if err != nil {
			return nil, err
		}
		if fin.Inventory == nil {
			return nil, badRequest("not_stock_item")
		}
		type cl struct {
			id, item string
			qty      decimal.Decimal
		}
		rows, err := tx.Query(ctx, `SELECT id::text, item_id::text, consumed_qty::text FROM mfg_order_lines WHERE order_id = $1 ORDER BY line_no`, id)
		if err != nil {
			return nil, err
		}
		comps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (cl, error) {
			var x cl
			var q string
			err := row.Scan(&x.id, &x.item, &q)
			x.qty, _ = decimal.NewFromString(q)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		memo := fmt.Sprintf("Manufacturing #%d · %s %s %s", number, good.String(), fin.Unit, fin.Name)
		lines := []jline{}
		type outMove struct {
			item     string
			qty, avg decimal.Decimal
			value    decimal.Decimal
		}
		moves := []outMove{}
		compTotal := decimal.Zero
		for _, c := range comps {
			if !c.qty.IsPositive() {
				continue
			}
			it, err := loadItem(ctx, tx, cid, c.item)
			if err != nil {
				return nil, err
			}
			if it.Inventory == nil {
				return nil, badRequest("not_stock_item")
			}
			have, avg, err := lockStock(ctx, tx, c.item, wh)
			if err != nil {
				return nil, err
			}
			if have.LessThan(c.qty) {
				return nil, insufficientStock(it.Name, have)
			}
			v, err := outValue(ctx, tx, c.item, wh, have, avg, c.qty)
			if err != nil {
				return nil, err
			}
			compTotal = compTotal.Add(v)
			moves = append(moves, outMove{c.item, c.qty, avg, v})
			if _, err := tx.Exec(ctx, `UPDATE mfg_order_lines SET value = $2::numeric WHERE id = $1`, c.id, v.String()); err != nil {
				return nil, err
			}
			if v.IsPositive() {
				lines = append(lines, jline{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Credit: v, Rate: "1"})
			}
		}
		total := compTotal.Add(lab).Add(ovh)
		if lab.IsPositive() {
			acc, err := mfgAbsorbAccount(ctx, tx, cid, "mfg_labour_account_id")
			if err != nil {
				return nil, err
			}
			lines = append(lines, jline{Account: acc, Desc: memo + " · labour", Currency: set.Base, Credit: lab, Rate: "1"})
		}
		if ovh.IsPositive() {
			acc, err := mfgAbsorbAccount(ctx, tx, cid, "mfg_overhead_account_id")
			if err != nil {
				return nil, err
			}
			lines = append(lines, jline{Account: acc, Desc: memo + " · overhead", Currency: set.Base, Credit: ovh, Rate: "1"})
		}
		var je *string
		if total.IsPositive() {
			lines = append([]jline{{Account: *fin.Inventory, Desc: memo, Currency: set.Base, Debit: total, Rate: "1"}}, lines...)
			j, err := postJournal(ctx, tx, cid, set, date, memo, fmt.Sprintf("MFG-%d", number), "manufacturing", lines)
			if err != nil {
				return nil, err
			}
			je = &j
		}
		for _, m := range moves {
			if err := stockOut(ctx, tx, cid, m.item, wh, m.qty, m.avg, m.value, date, "mfg_out", id, je, memo); err != nil {
				return nil, err
			}
		}
		if err := stockIn(ctx, tx, cid, item, wh, good, total, date, "mfg_in", id, je, memo); err != nil {
			return nil, err
		}
		unit := decimal.Zero
		if good.IsPositive() {
			unit = total.DivRound(good, 6)
		}
		if _, err := tx.Exec(ctx, `UPDATE mfg_orders SET status = 'done', order_date = $2::date, components_value = $3::numeric, scrap_qty = $4::numeric,
			unit_cost = $5::numeric, journal_entry_id = $6, done_at = now() WHERE id = $1`, id, date, compTotal.String(), scrap.String(), unit.String(), je); err != nil {
			return nil, err
		}
		return loadMfgOrder(ctx, tx, cid, id)
	})
}

// POST /companies/{cid}/mfg/orders/{id}/cancel (draft only)
func (s *Server) cancelMfgOrder(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		tag, err := tx.Exec(ctx, `UPDATE mfg_orders SET status = 'cancelled' WHERE id = $1 AND company_id = $2 AND status = 'draft'`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return loadMfgOrder(ctx, tx, cid, id)
	})
}

// POST /companies/{cid}/mfg/scrap {item_id, warehouse_id, quantity, date, reason}: damaged / lost goods out of stock,
// at cost, to the scrap account (else the stock adjustment account).
func (s *Server) scrapStock(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ItemID      string `json:"item_id"`
		WarehouseID string `json:"warehouse_id"`
		Quantity    string `json:"quantity"`
		Date        string `json:"date"`
		Reason      string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	qty, ok := money(in.Quantity)
	if !ok || !qty.IsPositive() || !validDate(in.Date) || !isUUID(in.ItemID) || (in.WarehouseID != "" && !isUUID(in.WarehouseID)) || strings.TrimSpace(in.Reason) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
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
		var acc *string
		if err := tx.QueryRow(ctx, `SELECT coalesce(scrap_account_id, stock_adjust_account_id)::text FROM companies WHERE id = $1`, cid).Scan(&acc); err != nil {
			return nil, err
		}
		if acc == nil {
			return nil, notSetup("adjust")
		}
		have, avg, err := lockStock(ctx, tx, it.ID, wh)
		if err != nil {
			return nil, err
		}
		if have.LessThan(qty) {
			return nil, insufficientStock(it.Name, have)
		}
		value, err := outValue(ctx, tx, it.ID, wh, have, avg, qty)
		if err != nil {
			return nil, err
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'stock_scrap')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		memo := fmt.Sprintf("Scrap #%d · %s %s %s · %s", n, qty.String(), it.Unit, it.Name, strings.TrimSpace(in.Reason))
		var je *string
		if value.IsPositive() {
			j, err := postJournal(ctx, tx, cid, set, in.Date, memo, fmt.Sprintf("SCR-%d", n), "stock", []jline{
				{Account: *acc, Desc: memo, Currency: set.Base, Debit: value},
				{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Credit: value},
			})
			if err != nil {
				return nil, err
			}
			je = &j
		}
		var docID string
		if err := tx.QueryRow(ctx, `INSERT INTO stock_docs (company_id, number, kind, doc_date, item_id, warehouse_id, quantity, value, account_id, notes, journal_entry_id)
			VALUES ($1, $2, 'scrap', $3::date, $4, $5, $6::numeric, $7::numeric, $8, $9, $10) RETURNING id::text`,
			cid, n, in.Date, it.ID, wh, qty.Neg().String(), value.Neg().String(), *acc, strings.TrimSpace(in.Reason), je).Scan(&docID); err != nil {
			return nil, err
		}
		if err := stockOut(ctx, tx, cid, it.ID, wh, qty, avg, value, in.Date, "scrap", docID, je, strings.TrimSpace(in.Reason)); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+stockDocCols+stockDocFrom+` WHERE d.id = $1`, docID)
	})
}
