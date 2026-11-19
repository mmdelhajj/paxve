package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Professional stock (module T): FIFO cost layers, lots / serial numbers, units of measure, kits,
// reorder rules and replenishment, stock counts, landed costs and price lists.
//
// Every stock movement goes through stockIn / stockOut (purchasing.go), which call stockHooks: for FIFO
// items the movement adds or consumes cost layers, for tracked items it records which lots moved.

// ---------- lots carried by the request ----------

type lotQty struct {
	Lot    string          `json:"lot_no"`
	Expiry string          `json:"expiry"`
	Qty    decimal.Decimal `json:"quantity"`
}

type lotsKey struct{}
type carryKey struct{}

// withLots: the lots / serials an incoming (or outgoing) movement of item uses, given by the user.
func withLots(ctx context.Context, item string, lots []lotQty) context.Context {
	if len(lots) == 0 {
		return ctx
	}
	m := map[string][]lotQty{}
	if old, ok := ctx.Value(lotsKey{}).(map[string][]lotQty); ok {
		for k, v := range old {
			m[k] = v
		}
	}
	m[item] = lots
	return context.WithValue(ctx, lotsKey{}, m)
}

func ctxLots(ctx context.Context, item string) []lotQty {
	m, _ := ctx.Value(lotsKey{}).(map[string][]lotQty)
	return m[item]
}

// withLotCarry: lots taken out by a stockOut are put back in by the next stockIn of the same item
// (a transfer keeps its lots).
func withLotCarry(ctx context.Context) context.Context {
	return context.WithValue(ctx, carryKey{}, &map[string][]lotQty{})
}

func lotCarry(ctx context.Context) *map[string][]lotQty {
	c, _ := ctx.Value(carryKey{}).(*map[string][]lotQty)
	return c
}

func itemStockMode(ctx context.Context, tx pgx.Tx, item string) (costing, tracking string, err error) {
	err = tx.QueryRow(ctx, `SELECT costing, tracking FROM items WHERE id = $1`, item).Scan(&costing, &tracking)
	return
}

// ---------- FIFO layers ----------

// fifoOutValue: the value of taking qty out of the oldest layers (nothing changes).
func fifoOutValue(ctx context.Context, tx pgx.Tx, item, wh string, qty decimal.Decimal) (decimal.Decimal, error) {
	rows, err := tx.Query(ctx, `SELECT qty_left::text, value_left::text FROM stock_layers
		WHERE item_id = $1 AND warehouse_id = $2 AND qty_left > 0 ORDER BY layer_date, created_at, id`, item, wh)
	if err != nil {
		return decimal.Zero, err
	}
	defer rows.Close()
	left, value := qty, decimal.Zero
	for rows.Next() && left.IsPositive() {
		var qs, vs string
		if err := rows.Scan(&qs, &vs); err != nil {
			return decimal.Zero, err
		}
		q, _ := decimal.NewFromString(qs)
		v, _ := decimal.NewFromString(vs)
		take := decimal.Min(q, left)
		if take.Equal(q) {
			value = value.Add(v)
		} else {
			value = value.Add(v.Mul(take).DivRound(q, 4))
		}
		left = left.Sub(take)
	}
	return value, rows.Err()
}

// layersTake consumes qty from the oldest layers, removing exactly value in total (the caller computed it).
func layersTake(ctx context.Context, tx pgx.Tx, item, wh string, qty, value decimal.Decimal) error {
	rows, err := tx.Query(ctx, `SELECT id::text, qty_left::text, value_left::text FROM stock_layers
		WHERE item_id = $1 AND warehouse_id = $2 AND qty_left > 0 ORDER BY layer_date, created_at, id FOR UPDATE`, item, wh)
	if err != nil {
		return err
	}
	type layer struct {
		id   string
		q, v decimal.Decimal
	}
	ls, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (layer, error) {
		var l layer
		var qs, vs string
		err := r.Scan(&l.id, &qs, &vs)
		l.q, _ = decimal.NewFromString(qs)
		l.v, _ = decimal.NewFromString(vs)
		return l, err
	})
	if err != nil {
		return err
	}
	leftQ, leftV := qty, value
	for i, l := range ls {
		if !leftQ.IsPositive() {
			break
		}
		take := decimal.Min(l.q, leftQ)
		tv := l.v
		if !take.Equal(l.q) {
			tv = l.v.Mul(take).DivRound(l.q, 4)
		}
		last := take.Equal(leftQ) || i == len(ls)-1
		if last { // the last layer touched absorbs any rounding difference
			tv = decimal.Min(leftV, l.v)
			if tv.IsNegative() {
				tv = decimal.Zero
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_layers SET qty_left = qty_left - $2::numeric, value_left = greatest(value_left - $3::numeric, 0)
			WHERE id = $1`, l.id, take.String(), tv.String()); err != nil {
			return err
		}
		leftQ, leftV = leftQ.Sub(take), leftV.Sub(tv)
	}
	return nil
}

// ---------- lots ----------

func lotID(ctx context.Context, tx pgx.Tx, cid, item string, l lotQty) (string, error) {
	var id string
	var exp *string
	if l.Expiry != "" {
		if !validDate(l.Expiry) {
			return "", badRequest("invalid_expiry")
		}
		exp = &l.Expiry
	}
	err := tx.QueryRow(ctx, `INSERT INTO stock_lots (company_id, item_id, lot_no, expiry) VALUES ($1, $2, $3, $4::date)
		ON CONFLICT (company_id, item_id, lot_no) DO UPDATE SET expiry = coalesce(EXCLUDED.expiry, stock_lots.expiry)
		RETURNING id::text`, cid, item, strings.TrimSpace(l.Lot), exp).Scan(&id)
	return id, err
}

func lotMove(ctx context.Context, tx pgx.Tx, cid, moveID, lot, wh string, qty decimal.Decimal) error {
	if qty.IsPositive() {
		if _, err := tx.Exec(ctx, `INSERT INTO stock_lot_qty (company_id, lot_id, warehouse_id, quantity) VALUES ($1, $2, $3, $4::numeric)
			ON CONFLICT (lot_id, warehouse_id) DO UPDATE SET quantity = stock_lot_qty.quantity + EXCLUDED.quantity`, cid, lot, wh, qty.String()); err != nil {
			return err
		}
	} else {
		tag, err := tx.Exec(ctx, `UPDATE stock_lot_qty SET quantity = quantity - $3::numeric WHERE lot_id = $1 AND warehouse_id = $2 AND quantity >= $3::numeric`,
			lot, wh, qty.Neg().String())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &apiError{status: http.StatusUnprocessableEntity, code: "lot_insufficient"}
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO stock_move_lots (company_id, move_id, lot_id, quantity) VALUES ($1, $2, $3, $4::numeric)`,
		cid, moveID, lot, qty.String())
	return err
}

// lotsIn records which lots / serials came in. Lot items without lots get a lot of the day; serial items need
// one serial per unit.
func lotsIn(ctx context.Context, tx pgx.Tx, cid, moveID, item, wh, tracking, date string, qty decimal.Decimal) error {
	lots := ctxLots(ctx, item)
	if len(lots) == 0 {
		if c := lotCarry(ctx); c != nil {
			lots = (*c)[item]
			delete(*c, item)
		}
	}
	if len(lots) == 0 {
		if tracking == "serial" {
			return &apiError{status: http.StatusUnprocessableEntity, code: "serials_required"}
		}
		lots = []lotQty{{Lot: "L-" + strings.ReplaceAll(date, "-", ""), Qty: qty}}
	}
	sum := decimal.Zero
	for _, l := range lots {
		if strings.TrimSpace(l.Lot) == "" || !l.Qty.IsPositive() || (tracking == "serial" && !l.Qty.Equal(decimal.NewFromInt(1))) {
			return badRequest("invalid_lot")
		}
		sum = sum.Add(l.Qty)
	}
	if !sum.Equal(qty) {
		return &apiError{status: http.StatusUnprocessableEntity, code: "lots_mismatch", detail: sum.String() + " ≠ " + qty.String()}
	}
	for _, l := range lots {
		id, err := lotID(ctx, tx, cid, item, l)
		if err != nil {
			return err
		}
		if tracking == "serial" { // a serial number exists once
			var held string
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(quantity), 0)::text FROM stock_lot_qty WHERE lot_id = $1`, id).Scan(&held); err != nil {
				return err
			}
			if h, _ := decimal.NewFromString(held); h.IsPositive() {
				return &apiError{status: http.StatusConflict, code: "serial_in_stock", detail: l.Lot}
			}
		}
		if err := lotMove(ctx, tx, cid, moveID, id, wh, l.Qty); err != nil {
			return err
		}
	}
	return nil
}

// lotsOut takes the given lots, or the first to expire first (FEFO), then the oldest.
func lotsOut(ctx context.Context, tx pgx.Tx, cid, moveID, item, wh string, qty decimal.Decimal) error {
	type pick struct {
		id, no, exp string
		q           decimal.Decimal
	}
	picks := []pick{}
	if want := ctxLots(ctx, item); len(want) > 0 {
		for _, l := range want {
			var p pick
			var q string
			if err := tx.QueryRow(ctx, `SELECT l.id::text, l.lot_no, coalesce(l.expiry::text, ''), coalesce(lq.quantity, 0)::text FROM stock_lots l
				LEFT JOIN stock_lot_qty lq ON lq.lot_id = l.id AND lq.warehouse_id = $3 WHERE l.item_id = $1 AND l.lot_no = $2`,
				item, strings.TrimSpace(l.Lot), wh).Scan(&p.id, &p.no, &p.exp, &q); err != nil {
				if err == pgx.ErrNoRows {
					return &apiError{status: http.StatusUnprocessableEntity, code: "lot_insufficient", detail: l.Lot}
				}
				return err
			}
			p.q = l.Qty
			picks = append(picks, p)
		}
	} else {
		rows, err := tx.Query(ctx, `SELECT l.id::text, l.lot_no, coalesce(l.expiry::text, ''), lq.quantity::text FROM stock_lot_qty lq
			JOIN stock_lots l ON l.id = lq.lot_id WHERE l.item_id = $1 AND lq.warehouse_id = $2 AND lq.quantity > 0
			ORDER BY l.expiry NULLS LAST, l.created_at, l.lot_no`, item, wh)
		if err != nil {
			return err
		}
		avail, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pick, error) {
			var p pick
			var q string
			err := r.Scan(&p.id, &p.no, &p.exp, &q)
			p.q, _ = decimal.NewFromString(q)
			return p, err
		})
		if err != nil {
			return err
		}
		left := qty
		for _, a := range avail {
			if !left.IsPositive() {
				break
			}
			take := decimal.Min(a.q, left)
			a.q = take
			picks = append(picks, a)
			left = left.Sub(take)
		}
		if left.IsPositive() {
			return &apiError{status: http.StatusUnprocessableEntity, code: "lot_insufficient"}
		}
	}
	carry := lotCarry(ctx)
	sum := decimal.Zero
	for _, p := range picks {
		if err := lotMove(ctx, tx, cid, moveID, p.id, wh, p.q.Neg()); err != nil {
			return err
		}
		sum = sum.Add(p.q)
		if carry != nil {
			(*carry)[item] = append((*carry)[item], lotQty{Lot: p.no, Expiry: p.exp, Qty: p.q})
		}
	}
	if !sum.Equal(qty) {
		return &apiError{status: http.StatusUnprocessableEntity, code: "lots_mismatch", detail: sum.String() + " ≠ " + qty.String()}
	}
	return nil
}

// stockHooks runs after a stock move was written: FIFO layers and lots. qty and value are signed (in > 0).
func stockHooks(ctx context.Context, tx pgx.Tx, cid, moveID, item, wh string, qty, value decimal.Decimal, date, source, sourceID string) error {
	costing, tracking, err := itemStockMode(ctx, tx, item)
	if err != nil {
		return err
	}
	if costing == "fifo" {
		if qty.IsPositive() {
			unit := decimal.Zero
			if !qty.IsZero() {
				unit = value.DivRound(qty, 6)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO stock_layers (company_id, item_id, warehouse_id, layer_date, source, source_id, qty_left, value_left, unit_cost)
				VALUES ($1, $2, $3, $4::date, $5, nullif($6, '')::uuid, $7::numeric, $8::numeric, $9::numeric)`,
				cid, item, wh, date, source, sourceID, qty.String(), value.String(), unit.String()); err != nil {
				return err
			}
		} else if err := layersTake(ctx, tx, item, wh, qty.Neg(), value.Neg()); err != nil {
			return err
		}
	}
	if tracking != "none" {
		if qty.IsPositive() {
			return lotsIn(ctx, tx, cid, moveID, item, wh, tracking, date, qty)
		}
		return lotsOut(ctx, tx, cid, moveID, item, wh, qty.Neg())
	}
	return nil
}

// moveLots: the lots of an earlier move (to reverse it exactly).
func moveLots(ctx context.Context, tx pgx.Tx, moveID string) ([]lotQty, error) {
	rows, err := tx.Query(ctx, `SELECT l.lot_no, coalesce(l.expiry::text, ''), abs(m.quantity)::text FROM stock_move_lots m
		JOIN stock_lots l ON l.id = m.lot_id WHERE m.move_id = $1`, moveID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (lotQty, error) {
		var l lotQty
		var q string
		err := r.Scan(&l.Lot, &l.Expiry, &q)
		l.Qty, _ = decimal.NewFromString(q)
		return l, err
	})
}

// ---------- kits ----------

// expandKits replaces kit lines by their components (same warehouse) and merges lines of the same item.
func expandKits(ctx context.Context, tx pgx.Tx, cid string, lines []saleLine) ([]saleLine, error) {
	type key struct{ item, wh string }
	sum := map[key]decimal.Decimal{}
	order := []key{}
	add := func(k key, q decimal.Decimal) {
		if _, ok := sum[k]; !ok {
			order = append(order, k)
		}
		sum[k] = sum[k].Add(q)
	}
	for _, l := range lines {
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM items WHERE id = $1 AND company_id = $2`, l.ItemID, cid).Scan(&kind); err != nil {
			if err == pgx.ErrNoRows {
				return nil, badRequest("invalid_item")
			}
			return nil, err
		}
		if kind != "kit" {
			add(key{l.ItemID, l.WarehouseID}, l.Qty)
			continue
		}
		rows, err := tx.Query(ctx, `SELECT component_id::text, quantity::text FROM item_components WHERE kit_id = $1`, l.ItemID)
		if err != nil {
			return nil, err
		}
		type comp struct {
			id string
			q  decimal.Decimal
		}
		comps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (comp, error) {
			var c comp
			var q string
			err := r.Scan(&c.id, &q)
			c.q, _ = decimal.NewFromString(q)
			return c, err
		})
		if err != nil {
			return nil, err
		}
		for _, c := range comps {
			add(key{c.id, l.WarehouseID}, c.q.Mul(l.Qty))
		}
	}
	out := make([]saleLine, 0, len(order))
	for _, k := range order {
		out = append(out, saleLine{ItemID: k.item, WarehouseID: k.wh, Qty: sum[k]})
	}
	return out, nil
}

// ---------- item settings: costing, tracking, units, components ----------

// PUT /items/{id}/stock-settings {costing, tracking}
func (s *Server) putItemStockSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Costing  *string `json:"costing"`
		Tracking *string `json:"tracking"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if (in.Costing != nil && *in.Costing != "avg" && *in.Costing != "fifo") ||
		(in.Tracking != nil && *in.Tracking != "none" && *in.Tracking != "lot" && *in.Tracking != "serial") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var costing, tracking string
		if err := tx.QueryRow(ctx, `SELECT costing, tracking FROM items WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&costing, &tracking); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if in.Tracking != nil && *in.Tracking != tracking {
			var held bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stock WHERE item_id = $1 AND quantity > 0)`, id).Scan(&held); err != nil {
				return nil, err
			}
			if held { // lots must cover the whole stock: change tracking only when nothing is held
				return nil, &apiError{status: http.StatusConflict, code: "stock_not_empty"}
			}
		}
		if in.Costing != nil && *in.Costing != costing {
			// leave old layers empty, then open one layer per warehouse from what is held
			if _, err := tx.Exec(ctx, `UPDATE stock_layers SET qty_left = 0, value_left = 0 WHERE item_id = $1 AND qty_left > 0`, id); err != nil {
				return nil, err
			}
			if *in.Costing == "fifo" {
				if _, err := tx.Exec(ctx, `INSERT INTO stock_layers (company_id, item_id, warehouse_id, layer_date, source, qty_left, value_left, unit_cost)
					SELECT $2, s.item_id, s.warehouse_id, current_date, 'opening', s.quantity, s.value, s.avg_cost FROM stock s
					WHERE s.item_id = $1 AND s.quantity > 0`, id, cid); err != nil {
					return nil, err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE items SET costing = coalesce($2, costing), tracking = coalesce($3, tracking) WHERE id = $1`,
			id, in.Costing, in.Tracking); err != nil {
			return nil, err
		}
		return itemExtras(ctx, tx, cid, id)
	})
}

func itemExtras(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	item, err := queryJSONOne(ctx, tx, `SELECT id, code, name, kind, unit, costing, tracking FROM items WHERE id = $1 AND company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	units, err := queryJSON(ctx, tx, `SELECT id, name, trim_scale(factor)::text AS factor, barcode FROM item_units WHERE item_id = $1 ORDER BY factor`, id)
	if err != nil {
		return nil, err
	}
	comps, err := queryJSON(ctx, tx, `SELECT c.id, c.component_id, i.code, i.name, i.unit, trim_scale(c.quantity)::text AS quantity
		FROM item_components c JOIN items i ON i.id = c.component_id WHERE c.kit_id = $1 ORDER BY i.name`, id)
	if err != nil {
		return nil, err
	}
	rules, err := queryJSON(ctx, tx, `SELECT r.id, r.warehouse_id, w.name AS warehouse_name, trim_scale(r.min_qty)::text AS min_qty,
		trim_scale(r.max_qty)::text AS max_qty, r.supplier_id FROM reorder_rules r JOIN warehouses w ON w.id = r.warehouse_id WHERE r.item_id = $1`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"item": item, "units": units, "components": comps, "reorder_rules": rules}, nil
}

// GET /items/{id}/extras
func (s *Server) getItemExtras(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return itemExtras(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// PUT /items/{id}/units {units: [{name, factor, barcode}]} replaces the item's extra units.
func (s *Server) putItemUnits(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Units []struct {
			Name    string `json:"name"`
			Factor  string `json:"factor"`
			Barcode string `json:"barcode"`
		} `json:"units"`
	}
	if !decodeBody(w, r, &in) || len(in.Units) > 20 {
		if len(in.Units) > 20 {
			writeErr(w, http.StatusBadRequest, "bad_request")
		}
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := loadItem(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM item_units WHERE item_id = $1`, id); err != nil {
			return nil, err
		}
		for _, u := range in.Units {
			f, err := decimal.NewFromString(strings.TrimSpace(u.Factor))
			if err != nil || !f.IsPositive() || strings.TrimSpace(u.Name) == "" {
				return nil, badRequest("bad_request")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO item_units (company_id, item_id, name, factor, barcode) VALUES ($1, $2, $3, $4::numeric, nullif($5, ''))`,
				cid, id, strings.TrimSpace(u.Name), f.String(), strings.TrimSpace(u.Barcode)); err != nil {
				if isUniqueViolation(err) {
					return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
				}
				return nil, err
			}
		}
		return itemExtras(ctx, tx, cid, id)
	})
}

// PUT /items/{id}/components {components: [{component_id, quantity}]}: the item becomes a kit.
func (s *Server) putItemComponents(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Components []struct {
			ComponentID string `json:"component_id"`
			Quantity    string `json:"quantity"`
		} `json:"components"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		it, err := loadItem(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if it.Kind == "stock" {
			var held bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stock WHERE item_id = $1 AND quantity > 0)`, id).Scan(&held); err != nil {
				return nil, err
			}
			if held {
				return nil, &apiError{status: http.StatusConflict, code: "stock_not_empty"}
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM item_components WHERE kit_id = $1`, id); err != nil {
			return nil, err
		}
		for _, c := range in.Components {
			q, ok := money(c.Quantity)
			if !ok || !q.IsPositive() || !isUUID(c.ComponentID) || c.ComponentID == id {
				return nil, badRequest("bad_request")
			}
			var kind string
			if err := tx.QueryRow(ctx, `SELECT kind FROM items WHERE id = $1 AND company_id = $2`, c.ComponentID, cid).Scan(&kind); err != nil || kind != "stock" {
				return nil, badRequest("component_not_stock")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO item_components (company_id, kit_id, component_id, quantity) VALUES ($1, $2, $3, $4::numeric)`,
				cid, id, c.ComponentID, q.String()); err != nil {
				return nil, err
			}
		}
		kind := "kit"
		if len(in.Components) == 0 {
			kind = "consumable"
		}
		if _, err := tx.Exec(ctx, `UPDATE items SET kind = $2 WHERE id = $1`, id, kind); err != nil {
			return nil, err
		}
		return itemExtras(ctx, tx, cid, id)
	})
}

// unitFactor: base units in one unit_id of the item (1 when unit_id is empty).
func unitFactor(ctx context.Context, tx pgx.Tx, item, unitID string) (decimal.Decimal, string, error) {
	if unitID == "" {
		return decimal.NewFromInt(1), "", nil
	}
	if !isUUID(unitID) {
		return decimal.Zero, "", badRequest("invalid_unit")
	}
	var f, name string
	err := tx.QueryRow(ctx, `SELECT factor::text, name FROM item_units WHERE id = $1 AND item_id = $2`, unitID, item).Scan(&f, &name)
	if err == pgx.ErrNoRows {
		return decimal.Zero, "", badRequest("invalid_unit")
	}
	d, _ := decimal.NewFromString(f)
	return d, name, err
}

// ---------- lots: list and traceability ----------

// GET /stock/lots?item_id=&q=
func (s *Server) listLots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	item, search := q.Get("item_id"), strings.TrimSpace(q.Get("q"))
	if item != "" && !isUUID(item) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT l.id, l.lot_no, l.expiry, l.item_id, i.code AS item_code, i.name AS item_name, i.tracking,
			coalesce((SELECT sum(lq.quantity) FROM stock_lot_qty lq WHERE lq.lot_id = l.id), 0)::text AS quantity,
			coalesce((SELECT json_agg(json_build_object('warehouse', w.name, 'quantity', lq.quantity::text)) FROM stock_lot_qty lq
			  JOIN warehouses w ON w.id = lq.warehouse_id WHERE lq.lot_id = l.id AND lq.quantity > 0), '[]') AS locations,
			(l.expiry IS NOT NULL AND l.expiry < current_date) AS expired
			FROM stock_lots l JOIN items i ON i.id = l.item_id
			WHERE l.company_id = $1 AND ($2 = '' OR l.item_id = $2::uuid) AND ($3 = '' OR l.lot_no ILIKE '%' || $3 || '%')
			ORDER BY l.expiry NULLS LAST, l.lot_no LIMIT 1000`, r.PathValue("cid"), item, search)
	})
}

// GET /stock/lots/{id}/trace: every movement of a lot / serial number, with its document.
func (s *Server) traceLot(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		lot, err := queryJSONOne(ctx, tx, `SELECT l.id, l.lot_no, l.expiry, i.code AS item_code, i.name AS item_name, i.tracking
			FROM stock_lots l JOIN items i ON i.id = l.item_id WHERE l.id = $1 AND l.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		moves, err := queryJSON(ctx, tx, `SELECT m.move_date, m.source, m.source_id, w.name AS warehouse, ml.quantity::text AS quantity, m.notes,
			m.journal_entry_id FROM stock_move_lots ml JOIN stock_moves m ON m.id = ml.move_id JOIN warehouses w ON w.id = m.warehouse_id
			WHERE ml.lot_id = $1 ORDER BY m.move_date, m.created_at`, id)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"lot": lot, "moves": moves}, nil
	})
}

// ---------- reorder rules and replenishment ----------

// PUT /stock/reorder-rules {item_id, warehouse_id, min_qty, max_qty, supplier_id}; min = max = "" removes the rule.
func (s *Server) putReorderRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ItemID      string `json:"item_id"`
		WarehouseID string `json:"warehouse_id"`
		Min         string `json:"min_qty"`
		Max         string `json:"max_qty"`
		SupplierID  string `json:"supplier_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(in.ItemID) || !isUUID(in.WarehouseID) || (in.SupplierID != "" && !isUUID(in.SupplierID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.Min == "" && in.Max == "" {
			_, err := tx.Exec(ctx, `DELETE FROM reorder_rules WHERE item_id = $1 AND warehouse_id = $2 AND company_id = $3`, in.ItemID, in.WarehouseID, cid)
			return map[string]bool{"ok": true}, err
		}
		mn, ok1 := money(in.Min)
		mx, ok2 := money(in.Max)
		if !ok1 || !ok2 || mx.LessThan(mn) {
			return nil, badRequest("bad_request")
		}
		return dmlJSONOne(ctx, tx, `INSERT INTO reorder_rules (company_id, item_id, warehouse_id, min_qty, max_qty, supplier_id)
			VALUES ($1, $2, $3, $4::numeric, $5::numeric, nullif($6, '')::uuid)
			ON CONFLICT (item_id, warehouse_id) DO UPDATE SET min_qty = EXCLUDED.min_qty, max_qty = EXCLUDED.max_qty, supplier_id = EXCLUDED.supplier_id
			RETURNING id, item_id, warehouse_id, trim_scale(min_qty)::text AS min_qty, trim_scale(max_qty)::text AS max_qty, supplier_id`,
			cid, in.ItemID, in.WarehouseID, mn.String(), mx.String(), in.SupplierID)
	})
}

// GET /reports/stock-replenishment: items under their minimum (with the quantity to order up to the maximum)
// and, for every stock item used in the last 30 days, how many days the stock lasts at that pace.
func (s *Server) replenishment(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, `
			WITH usage AS (
			  SELECT item_id, warehouse_id, -sum(quantity) AS used FROM stock_moves
			  WHERE company_id = $1 AND quantity < 0 AND move_date > current_date - 30
			    AND source IN ('fuel', 'sale', 'delivery', 'pos', 'adjust', 'count') GROUP BY 1, 2)
			SELECT i.id AS item_id, i.code, i.name, i.unit, w.id AS warehouse_id, w.name AS warehouse_name,
			  coalesce(s.quantity, 0)::text AS quantity, trim_scale(r.min_qty)::text AS min_qty, trim_scale(r.max_qty)::text AS max_qty,
			  CASE WHEN r.id IS NOT NULL AND coalesce(s.quantity, 0) <= r.min_qty THEN trim_scale(r.max_qty - coalesce(s.quantity, 0))::text END AS to_order,
			  round(coalesce(u.used, 0) / 30.0, 4)::text AS daily_use,
			  CASE WHEN coalesce(u.used, 0) > 0 THEN floor(coalesce(s.quantity, 0) / (u.used / 30.0))::int END AS days_left,
			  sp.name AS supplier_name, r.supplier_id
			FROM items i
			CROSS JOIN warehouses w
			LEFT JOIN stock s ON s.item_id = i.id AND s.warehouse_id = w.id
			LEFT JOIN reorder_rules r ON r.item_id = i.id AND r.warehouse_id = w.id
			LEFT JOIN usage u ON u.item_id = i.id AND u.warehouse_id = w.id
			LEFT JOIN suppliers sp ON sp.id = r.supplier_id
			WHERE i.company_id = $1 AND w.company_id = $1 AND i.kind = 'stock' AND i.is_active
			  AND (r.id IS NOT NULL OR u.used > 0)
			ORDER BY (r.id IS NOT NULL AND coalesce(s.quantity, 0) <= r.min_qty) DESC, days_left NULLS LAST, i.name`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows}, nil
	})
}

// ---------- stock counts ----------

// POST /stock/counts {warehouse_id, date, notes}: a count sheet with what the system expects (all stock items).
func (s *Server) createStockCount(w http.ResponseWriter, r *http.Request) {
	var in struct {
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
	if !validDate(in.Date) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		wh, err := checkWarehouse(ctx, tx, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'stock_count')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO stock_counts (company_id, number, warehouse_id, count_date, notes) VALUES ($1, $2, $3, $4::date, $5)
			RETURNING id::text`, cid, n, wh, in.Date, strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stock_count_lines (company_id, count_id, item_id, expected)
			SELECT $1, $2, i.id, coalesce(s.quantity, 0) FROM items i LEFT JOIN stock s ON s.item_id = i.id AND s.warehouse_id = $3
			WHERE i.company_id = $1 AND i.kind = 'stock' AND i.is_active`, cid, id, wh); err != nil {
			return nil, err
		}
		return loadStockCount(ctx, tx, cid, id)
	})
}

func loadStockCount(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	c, err := queryJSONOne(ctx, tx, `SELECT c.id, c.number, c.warehouse_id, w.name AS warehouse_name, c.count_date, c.status, c.notes,
		c.journal_entry_id, c.created_at, c.posted_at FROM stock_counts c JOIN warehouses w ON w.id = c.warehouse_id
		WHERE c.id = $1 AND c.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.item_id, i.code, i.name, i.unit, trim_scale(l.expected)::text AS expected,
		trim_scale(l.counted)::text AS counted, trim_scale(l.counted - l.expected)::text AS difference
		FROM stock_count_lines l JOIN items i ON i.id = l.item_id WHERE l.count_id = $1 ORDER BY i.name`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"count": c, "lines": lines}, nil
}

// GET /stock/counts
func (s *Server) listStockCounts(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT c.id, c.number, w.name AS warehouse_name, c.count_date, c.status, c.posted_at,
			(SELECT count(*) FROM stock_count_lines l WHERE l.count_id = c.id AND l.counted IS NOT NULL AND l.counted <> l.expected) AS differences
			FROM stock_counts c JOIN warehouses w ON w.id = c.warehouse_id WHERE c.company_id = $1 ORDER BY c.number DESC LIMIT 200`, r.PathValue("cid"))
	})
}

// GET /stock/counts/{id}
func (s *Server) getStockCount(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadStockCount(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// PUT /stock/counts/{id}/lines {lines: [{item_id, counted}]} ("" clears a count)
func (s *Server) putStockCountLines(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines []struct {
			ItemID  string `json:"item_id"`
			Counted string `json:"counted"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM stock_counts WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&status); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if status != "draft" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		for _, l := range in.Lines {
			var counted *string
			if strings.TrimSpace(l.Counted) != "" {
				q, ok := money(l.Counted)
				if !ok {
					return nil, badRequest("invalid_amount")
				}
				v := q.String()
				counted = &v
			}
			if !isUUID(l.ItemID) {
				return nil, badRequest("bad_request")
			}
			if _, err := tx.Exec(ctx, `UPDATE stock_count_lines SET counted = $3::numeric WHERE count_id = $1 AND item_id = $2`, id, l.ItemID, counted); err != nil {
				return nil, err
			}
		}
		return loadStockCount(ctx, tx, cid, id)
	})
}

// POST /stock/counts/{id}/post: differences become stock adjustments (found: in at average cost; missing:
// out at cost) booked Dr/Cr inventory against the stock adjustment account. Quantities moved since the
// count sheet was made are respected: the difference is counted − expected.
func (s *Server) postStockCount(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, wh, date string
		var number int64
		if err := tx.QueryRow(ctx, `SELECT status, warehouse_id::text, count_date::text, number FROM stock_counts WHERE id = $1 AND company_id = $2 FOR UPDATE`,
			id, cid).Scan(&status, &wh, &date, &number); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if status != "draft" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		ps, err := loadPurchSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if ps.Adjust == nil {
			return nil, notSetup("stock_adjust")
		}
		rows, err := tx.Query(ctx, `SELECT item_id::text, (counted - expected)::text FROM stock_count_lines
			WHERE count_id = $1 AND counted IS NOT NULL AND counted <> expected`, id)
		if err != nil {
			return nil, err
		}
		type diff struct {
			item string
			d    decimal.Decimal
		}
		diffs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (diff, error) {
			var x diff
			var d string
			err := r.Scan(&x.item, &d)
			x.d, _ = decimal.NewFromString(d)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		type mv struct {
			it         *itemInfo
			qty, value decimal.Decimal
			avg        decimal.Decimal
		}
		moves := []mv{}
		jl := []jline{}
		memo := fmt.Sprintf("Stock count #%d", number)
		for _, d := range diffs {
			it, err := loadItem(ctx, tx, cid, d.item)
			if err != nil {
				return nil, err
			}
			have, avg, err := lockStock(ctx, tx, it.ID, wh)
			if err != nil {
				return nil, err
			}
			var value decimal.Decimal
			if d.d.IsNegative() {
				q := d.d.Neg()
				if have.LessThan(q) {
					return nil, insufficientStock(it.Name, have)
				}
				if value, err = outValue(ctx, tx, it.ID, wh, have, avg, q); err != nil {
					return nil, err
				}
				jl = append(jl, jline{Account: *ps.Adjust, Desc: it.Name, Currency: set.Base, Debit: value, Rate: "1"},
					jline{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Credit: value, Rate: "1"})
			} else {
				value = d.d.Mul(avg).Round(4)
				jl = append(jl, jline{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Debit: value, Rate: "1"},
					jline{Account: *ps.Adjust, Desc: it.Name, Currency: set.Base, Credit: value, Rate: "1"})
			}
			moves = append(moves, mv{it, d.d, value, avg})
		}
		var je *string
		if len(jl) > 0 {
			e, err := postJournal(ctx, tx, cid, set, date, memo, fmt.Sprintf("CNT-%d", number), "stock", jl)
			if err != nil {
				return nil, err
			}
			je = &e
		}
		for _, m := range moves {
			if m.qty.IsNegative() {
				err = stockOut(ctx, tx, cid, m.it.ID, wh, m.qty.Neg(), m.avg, m.value, date, "count", id, je, memo)
			} else {
				err = stockIn(ctx, tx, cid, m.it.ID, wh, m.qty, m.value, date, "count", id, je, memo)
			}
			if err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_counts SET status = 'posted', posted_at = now(), journal_entry_id = $2 WHERE id = $1`, id, je); err != nil {
			return nil, err
		}
		return loadStockCount(ctx, tx, cid, id)
	})
}

// POST /stock/counts/{id}/cancel (draft only)
func (s *Server) cancelStockCount(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE stock_counts SET status = 'cancelled' WHERE id = $1 AND company_id = $2 AND status = 'draft'`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return loadStockCount(ctx, tx, cid, id)
	})
}

// ---------- landed costs ----------

// POST /bills/{id}/landed-costs {date, currency, amount, method: value|quantity, description, method_id | account_id}
// Freight / customs added to the goods of a supplier bill. The part matching goods still in stock raises
// their value (and their FIFO layer); the part of goods already sold goes to cost of sales. Credit: the cash
// box / bank it was paid from (method_id) or any account (account_id, e.g. a forwarder payable).
func (s *Server) createLandedCost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date        string `json:"date"`
		Currency    string `json:"currency"`
		Amount      string `json:"amount"`
		Method      string `json:"method"`
		Description string `json:"description"`
		MethodID    string `json:"method_id"`
		AccountID   string `json:"account_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	if in.Method == "" {
		in.Method = "value"
	}
	amount, ok := money(in.Amount)
	if !ok || !amount.IsPositive() || !validDate(in.Date) || !currencyRe.MatchString(in.Currency) ||
		(in.Method != "value" && in.Method != "quantity") || (in.MethodID == "") == (in.AccountID == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, billID := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		var billNo int64
		if err := tx.QueryRow(ctx, `SELECT status, number FROM bills WHERE id = $1 AND company_id = $2`, billID, cid).Scan(&status, &billNo); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if status == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		ps, err := loadPurchSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		credit := in.AccountID
		creditCur := in.Currency
		if in.MethodID != "" {
			methods, err := loadMethods(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			pm, ok := methods[in.MethodID]
			if !ok || !pm.Active {
				return nil, badRequest("invalid_tender")
			}
			if pm.Currency != in.Currency {
				return nil, badRequest("currency_mismatch")
			}
			credit, creditCur = pm.Account, pm.Currency
		} else if err := requirePostable(ctx, tx, cid, credit); err != nil {
			return nil, err
		}
		rateS, err := resolveRate(ctx, tx, cid, set.Base, in.Currency, in.Date, "")
		if err != nil {
			return nil, err
		}
		rt, _ := decimal.NewFromString(rateS)
		base := amount.DivRound(rt, 4)
		// the bill's goods, by item and warehouse
		rows, err := tx.Query(ctx, `SELECT m.item_id::text, m.warehouse_id::text, sum(m.quantity)::text, sum(m.value)::text
			FROM stock_moves m WHERE m.source = 'bill' AND m.source_id = $1 GROUP BY 1, 2 ORDER BY 1, 2`, billID)
		if err != nil {
			return nil, err
		}
		type good struct {
			item, wh string
			q, v     decimal.Decimal
		}
		goods, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (good, error) {
			var g good
			var q, v string
			err := r.Scan(&g.item, &g.wh, &q, &v)
			g.q, _ = decimal.NewFromString(q)
			g.v, _ = decimal.NewFromString(v)
			return g, err
		})
		if err != nil {
			return nil, err
		}
		if len(goods) == 0 {
			return nil, badRequest("bill_without_stock")
		}
		weight := decimal.Zero
		for _, g := range goods {
			if in.Method == "quantity" {
				weight = weight.Add(g.q)
			} else {
				weight = weight.Add(g.v)
			}
		}
		if !weight.IsPositive() {
			return nil, badRequest("bill_without_stock")
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'landed_cost')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		memo := fmt.Sprintf("Landed cost #%d · bill #%d", n, billNo)
		if d := strings.TrimSpace(in.Description); d != "" {
			memo += " · " + d
		}
		jl := []jline{}
		type bump struct {
			item, wh string
			v        decimal.Decimal
		}
		bumps := []bump{}
		left := base
		for i, g := range goods {
			w := g.v
			if in.Method == "quantity" {
				w = g.q
			}
			share := base.Mul(w).DivRound(weight, 4)
			if i == len(goods)-1 {
				share = left
			}
			left = left.Sub(share)
			it, err := loadItem(ctx, tx, cid, g.item)
			if err != nil {
				return nil, err
			}
			have, _, err := lockStock(ctx, tx, g.item, g.wh)
			if err != nil {
				return nil, err
			}
			inStock := share
			if have.LessThan(g.q) {
				inStock = share.Mul(have).DivRound(g.q, 4)
			}
			sold := share.Sub(inStock)
			if inStock.IsPositive() {
				jl = append(jl, jline{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Debit: inStock, Rate: "1"})
				bumps = append(bumps, bump{g.item, g.wh, inStock})
			}
			if sold.IsPositive() {
				cogs := ps.COGS
				if it.Expense != nil {
					cogs = it.Expense
				}
				if cogs == nil {
					return nil, notSetup("cogs")
				}
				jl = append(jl, jline{Account: *cogs, Desc: it.Name, Currency: set.Base, Debit: sold, Rate: "1"})
			}
		}
		jl = append(jl, jline{Account: credit, Desc: memo, Currency: creditCur, Credit: amount, Rate: rateS})
		je, err := postJournal(ctx, tx, cid, set, in.Date, memo, fmt.Sprintf("LC-%d", n), "landed_cost", jl)
		if err != nil {
			return nil, err
		}
		for _, b := range bumps {
			q, _, err := lockStock(ctx, tx, b.item, b.wh)
			if err != nil {
				return nil, err
			}
			held, err := stockValueOf(ctx, tx, b.item, b.wh)
			if err != nil {
				return nil, err
			}
			if err := setStock(ctx, tx, b.item, b.wh, q, held.Add(b.v)); err != nil {
				return nil, err
			}
			// FIFO: raise the bill's own layer if it is still there, else the newest open layer
			if _, err := tx.Exec(ctx, `UPDATE stock_layers SET value_left = value_left + $3::numeric,
				unit_cost = CASE WHEN qty_left > 0 THEN (value_left + $3::numeric) / qty_left ELSE unit_cost END
				WHERE id = (SELECT id FROM stock_layers WHERE item_id = $1 AND warehouse_id = $2 AND qty_left > 0
				  ORDER BY (source = 'bill' AND source_id = $4::uuid) DESC, layer_date DESC, created_at DESC LIMIT 1)`,
				b.item, b.wh, b.v.String(), billID); err != nil {
				return nil, err
			}
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO landed_costs (company_id, number, bill_id, cost_date, currency, amount, amount_base, method, description, journal_entry_id)
			VALUES ($1, $2, $3, $4::date, $5, $6::numeric, $7::numeric, $8, $9, $10) RETURNING id::text`,
			cid, n, billID, in.Date, in.Currency, amount.String(), base.String(), in.Method, strings.TrimSpace(in.Description), je).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, number, bill_id, cost_date, currency, amount::text, amount_base::text, method, description,
			journal_entry_id FROM landed_costs WHERE id = $1`, id)
	})
}

// GET /bills/{id}/landed-costs
func (s *Server) listLandedCosts(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bills WHERE id = $1 AND company_id = $2)`, r.PathValue("id"), r.PathValue("cid")).Scan(&ok); err != nil || !ok {
			if err == nil {
				err = notFound()
			}
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT id, number, cost_date, currency, amount::text, amount_base::text, method, description, journal_entry_id
			FROM landed_costs WHERE bill_id = $1 AND company_id = $2 ORDER BY number`, r.PathValue("id"), r.PathValue("cid"))
	})
}

// ---------- price lists ----------

// GET /price-lists
func (s *Server) listPriceLists(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT p.id, p.name, p.currency, p.is_active,
			(SELECT count(*) FROM price_list_items x WHERE x.list_id = p.id) AS items,
			(SELECT count(*) FROM customers c WHERE c.price_list_id = p.id) AS customers
			FROM price_lists p WHERE p.company_id = $1 ORDER BY p.name`, r.PathValue("cid"))
	})
}

// POST /price-lists {name, currency}; PATCH /price-lists/{id} {name, is_active}
func (s *Server) savePriceList(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     *string `json:"name"`
		Currency *string `json:"currency"`
		IsActive *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Name == nil || in.Currency == nil)) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") ||
		(in.Currency != nil && !currencyRe.MatchString(*in.Currency)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[create], func(ctx context.Context, tx pgx.Tx) (any, error) {
		var out json.RawMessage
		var err error
		if create {
			out, err = dmlJSONOne(ctx, tx, `INSERT INTO price_lists (company_id, name, currency) VALUES ($1, $2, $3) RETURNING id, name, currency, is_active`,
				cid, strings.TrimSpace(*in.Name), *in.Currency)
		} else {
			out, err = dmlJSONOne(ctx, tx, `UPDATE price_lists SET name = coalesce(trim($3), name), is_active = coalesce($4, is_active)
				WHERE id = $1 AND company_id = $2 RETURNING id, name, currency, is_active`, id, cid, in.Name, in.IsActive)
		}
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "name_taken"}
		}
		return out, err
	})
}

// GET /price-lists/{id}/items
func (s *Server) getPriceListItems(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM price_lists WHERE id = $1 AND company_id = $2)`, r.PathValue("id"), r.PathValue("cid")).Scan(&ok); err != nil || !ok {
			if err == nil {
				err = notFound()
			}
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT x.id, x.item_id, i.code, i.name, i.unit, trim_scale(x.min_qty)::text AS min_qty, x.price::text AS price
			FROM price_list_items x JOIN items i ON i.id = x.item_id JOIN price_lists p ON p.id = x.list_id
			WHERE x.list_id = $1 AND p.company_id = $2 ORDER BY i.name, x.min_qty`, r.PathValue("id"), r.PathValue("cid"))
	})
}

// PUT /price-lists/{id}/items {items: [{item_id, min_qty, price}]} replaces the list's prices.
func (s *Server) putPriceListItems(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items []struct {
			ItemID string `json:"item_id"`
			MinQty string `json:"min_qty"`
			Price  string `json:"price"`
		} `json:"items"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM price_lists WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok); err != nil || !ok {
			if err == nil {
				err = notFound()
			}
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM price_list_items WHERE list_id = $1`, id); err != nil {
			return nil, err
		}
		for _, x := range in.Items {
			price, ok1 := money(x.Price)
			mq := decimal.Zero
			ok2 := true
			if strings.TrimSpace(x.MinQty) != "" {
				mq, ok2 = money(x.MinQty)
			}
			if !ok1 || !ok2 || !isUUID(x.ItemID) {
				return nil, badRequest("bad_request")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO price_list_items (company_id, list_id, item_id, min_qty, price) VALUES ($1, $2, $3, $4::numeric, $5::numeric)`,
				cid, id, x.ItemID, mq.String(), price.String()); err != nil {
				if isUniqueViolation(err) {
					return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
				}
				return nil, err
			}
		}
		return queryJSON(ctx, tx, `SELECT x.id, x.item_id, i.code, i.name, trim_scale(x.min_qty)::text AS min_qty, x.price::text AS price
			FROM price_list_items x JOIN items i ON i.id = x.item_id WHERE x.list_id = $1 ORDER BY i.name, x.min_qty`, id)
	})
}

// PUT /customers/{id}/price-list {price_list_id} ("" = none)
func (s *Server) putCustomerPriceList(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PriceListID string `json:"price_list_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.PriceListID != "" && !isUUID(in.PriceListID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return dmlJSONOne(ctx, tx, `UPDATE customers SET price_list_id = nullif($3, '')::uuid WHERE id = $1 AND company_id = $2
			RETURNING id, price_list_id`, id, cid, in.PriceListID)
	})
}

// GET /items/{id}/price?customer_id=&quantity=&currency= : the customer's price list (best quantity break)
// in that currency, else the item's price for the currency, else its sale price.
func (s *Server) itemPrice(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cust, cur := q.Get("customer_id"), q.Get("currency")
	qty := decimal.NewFromInt(1)
	if v := q.Get("quantity"); v != "" {
		d, ok := money(v)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		qty = d
	}
	if (cust != "" && !isUUID(cust)) || (cur != "" && !currencyRe.MatchString(cur)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var price, currency, source string
		err := tx.QueryRow(ctx, `SELECT x.price::text, p.currency::text, 'price_list:' || p.name FROM customers c
			JOIN price_lists p ON p.id = c.price_list_id AND p.is_active JOIN price_list_items x ON x.list_id = p.id AND x.item_id = $2
			WHERE c.id = nullif($3, '')::uuid AND c.company_id = $1 AND x.min_qty <= $4::numeric AND ($5 = '' OR p.currency = $5)
			ORDER BY x.min_qty DESC LIMIT 1`, cid, id, cust, qty.String(), cur).Scan(&price, &currency, &source)
		if err == pgx.ErrNoRows {
			err = tx.QueryRow(ctx, `SELECT price::text, currency::text, 'item_price' FROM item_prices WHERE item_id = $1 AND currency = $2`, id, cur).
				Scan(&price, &currency, &source)
		}
		if err == pgx.ErrNoRows {
			err = tx.QueryRow(ctx, `SELECT sale_price::text, coalesce(sale_currency::text, ''), 'sale_price' FROM items WHERE id = $1 AND company_id = $2`, id, cid).
				Scan(&price, &currency, &source)
		}
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"price": price, "currency": currency, "source": source, "quantity": qty.String(), "at": time.Now().UTC()}, nil
	})
}
