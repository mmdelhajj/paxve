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

// Warehouse pro (phase 1). Bins are the physical place of stock; value stays per item and warehouse (see
// migration 00041). Operations (wh_ops) are the transfer documents of each step:
//
//	receipt 1 step:  goods receipt → putaway (arrival → bin)
//	receipt 2 steps: goods receipt (→ INPUT) → putaway (INPUT → bin)
//	receipt 3 steps: goods receipt (→ INPUT) → qc (INPUT → QC) → putaway (QC → bin)
//	delivery 2 steps: pick (bin → OUTPUT) → ship (the delivery: stock leaves, OUTPUT first)
//	delivery 3 steps: pick (bin → PACK) → pack (PACK → OUTPUT, into packages) → ship
//
// The next step is created when a step is done, with the quantities really done; a partly done step leaves a
// backorder of the same kind. A warehouse without bins (the default) works exactly as before.

var whKinds = []string{"zone", "aisle", "shelf", "bin", "input", "qc", "output", "pack"}
var whSpecialCode = map[string]string{"input": "INPUT", "qc": "QC", "output": "OUTPUT", "pack": "PACK"}

type whCfg struct {
	ID, Name, Removal        string
	Bins, Crossdock, Transit bool
	RecSteps, DelSteps       int
}

func loadWhCfg(ctx context.Context, tx pgx.Tx, cid, wh string) (*whCfg, error) {
	if !isUUID(wh) {
		return nil, badRequest("invalid_warehouse")
	}
	c := &whCfg{}
	err := tx.QueryRow(ctx, `SELECT id::text, name, removal, use_bins, crossdock, is_transit, reception_steps, delivery_steps
		FROM warehouses WHERE id = $1 AND company_id = $2`, wh, cid).
		Scan(&c.ID, &c.Name, &c.Removal, &c.Bins, &c.Crossdock, &c.Transit, &c.RecSteps, &c.DelSteps)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, badRequest("invalid_warehouse")
	}
	return c, err
}

// whSpecial returns (creating it when missing) the INPUT / QC / OUTPUT / PACK area of a warehouse.
func whSpecial(ctx context.Context, tx pgx.Tx, cid, wh, kind string) (string, error) {
	code := whSpecialCode[kind]
	if _, err := tx.Exec(ctx, `INSERT INTO wh_locations (company_id, warehouse_id, kind, code, name, barcode, sort_key)
		VALUES ($1, $2, $3, $4, $4, '', '0/' || $4) ON CONFLICT (warehouse_id, code) DO NOTHING`, cid, wh, kind, code); err != nil {
		return "", err
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM wh_locations WHERE warehouse_id = $1 AND code = $2`, wh, code).Scan(&id)
	return id, err
}

// checkLoc: a location of that warehouse (nil = unbinned).
func checkLoc(ctx context.Context, tx pgx.Tx, cid, wh string, loc *string) error {
	if loc == nil {
		return nil
	}
	if !isUUID(*loc) {
		return badRequest("invalid_location")
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wh_locations WHERE id = $1 AND company_id = $2 AND warehouse_id = $3)`,
		*loc, cid, wh).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return badRequest("invalid_location")
	}
	return nil
}

func checkLot(ctx context.Context, tx pgx.Tx, cid, item string, lot *string) error {
	if lot == nil {
		return nil
	}
	if !isUUID(*lot) {
		return badRequest("invalid_lot")
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stock_lots WHERE id = $1 AND company_id = $2 AND item_id = $3)`, *lot, cid, item).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return badRequest("invalid_lot")
	}
	return nil
}

// binPut adds quantity of an item (lot) to a location.
func binPut(ctx context.Context, tx pgx.Tx, cid, wh, loc, item string, lot *string, qty decimal.Decimal) error {
	if !qty.IsPositive() {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO wh_bin_stock (company_id, warehouse_id, location_id, item_id, lot_id, quantity)
		VALUES ($1, $2, $3, $4, $5, $6::numeric)
		ON CONFLICT (location_id, item_id, (coalesce(lot_id, '00000000-0000-0000-0000-000000000000'::uuid)))
		DO UPDATE SET quantity = wh_bin_stock.quantity + EXCLUDED.quantity, updated_at = now()`, cid, wh, loc, item, lot, qty.String())
	return err
}

type lotTaken struct {
	lot *string
	qty decimal.Decimal
}

// binTake removes quantity of an item (any lot unless one is given) from a location, oldest first.
func binTake(ctx context.Context, tx pgx.Tx, loc, item string, lot *string, qty decimal.Decimal, strict bool) ([]lotTaken, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, lot_id::text, quantity::text FROM wh_bin_stock
		WHERE location_id = $1 AND item_id = $2 AND ($3::uuid IS NULL OR lot_id = $3) AND quantity > 0 ORDER BY in_date, id FOR UPDATE`, loc, item, lot)
	if err != nil {
		return nil, err
	}
	type row struct {
		id  string
		lot *string
		q   decimal.Decimal
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		var q string
		err := r.Scan(&x.id, &x.lot, &q)
		x.q, _ = decimal.NewFromString(q)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	have := decimal.Zero
	for _, x := range list {
		have = have.Add(x.q)
	}
	if strict && have.LessThan(qty) {
		var code string
		_ = tx.QueryRow(ctx, `SELECT code FROM wh_locations WHERE id = $1`, loc).Scan(&code)
		return nil, &apiError{status: http.StatusUnprocessableEntity, code: "not_enough_in_bin", detail: code + " (" + have.String() + ")"}
	}
	out := []lotTaken{}
	left := qty
	for _, x := range list {
		if !left.IsPositive() {
			break
		}
		t := decimal.Min(x.q, left)
		if _, err := tx.Exec(ctx, `UPDATE wh_bin_stock SET quantity = quantity - $2::numeric, updated_at = now() WHERE id = $1`, x.id, t.String()); err != nil {
			return nil, err
		}
		out = append(out, lotTaken{x.lot, t})
		left = left.Sub(t)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM wh_bin_stock WHERE location_id = $1 AND item_id = $2 AND quantity = 0`, loc, item); err != nil {
		return nil, err
	}
	return out, nil
}

// unbinned: what the warehouse holds of an item that no bin holds.
func unbinned(ctx context.Context, tx pgx.Tx, wh, item string) (decimal.Decimal, error) {
	var s string
	err := tx.QueryRow(ctx, `SELECT (coalesce((SELECT quantity FROM stock WHERE warehouse_id = $1 AND item_id = $2), 0)
		- coalesce((SELECT sum(quantity) FROM wh_bin_stock WHERE warehouse_id = $1 AND item_id = $2), 0))::text`, wh, item).Scan(&s)
	d, _ := decimal.NewFromString(s)
	return decimal.Max(d, decimal.Zero), err
}

// binMove moves an item between two places of a warehouse (nil = unbinned). arrival: goods arriving from a receipt
// (not yet in the warehouse's stock), so nothing is taken from the unbinned part.
func binMove(ctx context.Context, tx pgx.Tx, cid, wh, item string, lot, from, to *string, qty decimal.Decimal, arrival bool) error {
	if !qty.IsPositive() {
		return nil
	}
	if from != nil && to != nil && *from == *to {
		return nil
	}
	if from != nil {
		taken, err := binTake(ctx, tx, *from, item, lot, qty, true)
		if err != nil {
			return err
		}
		if to != nil {
			for _, t := range taken {
				if err := binPut(ctx, tx, cid, wh, *to, item, t.lot, t.qty); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if !arrival {
		have, err := unbinned(ctx, tx, wh, item)
		if err != nil {
			return err
		}
		if have.LessThan(qty) {
			return &apiError{status: http.StatusUnprocessableEntity, code: "not_enough_unbinned", detail: have.String()}
		}
	}
	if to != nil {
		return binPut(ctx, tx, cid, wh, *to, item, lot, qty)
	}
	return nil
}

// suggestPutaway: where an arriving item goes (rule for the item, then its category, then the bin already holding most of it).
func suggestPutaway(ctx context.Context, tx pgx.Tx, wh, item string) (*string, error) {
	var loc string
	err := tx.QueryRow(ctx, `SELECT r.location_id::text FROM wh_putaway_rules r JOIN items it ON it.id = $2
		WHERE r.warehouse_id = $1 AND (r.item_id = it.id OR (r.item_id IS NULL AND r.category_id = it.category_id))
		ORDER BY (r.item_id IS NULL), r.priority, r.created_at LIMIT 1`, wh, item).Scan(&loc)
	if err == nil {
		return &loc, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	err = tx.QueryRow(ctx, `SELECT b.location_id::text FROM wh_bin_stock b JOIN wh_locations l ON l.id = b.location_id
		WHERE b.warehouse_id = $1 AND b.item_id = $2 AND l.kind IN ('bin', 'shelf', 'aisle', 'zone')
		GROUP BY b.location_id ORDER BY sum(b.quantity) DESC LIMIT 1`, wh, item).Scan(&loc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &loc, nil
}

type pickPart struct {
	loc, lot *string
	qty      decimal.Decimal
}

// suggestPick: where to take qty of an item from (storage bins by the removal strategy, then the unbinned part).
func suggestPick(ctx context.Context, tx pgx.Tx, wh, removal, item string, qty decimal.Decimal) ([]pickPart, error) {
	rows, err := tx.Query(ctx, `SELECT b.location_id::text, b.lot_id::text, sum(b.quantity)::text FROM wh_bin_stock b
		JOIN wh_locations l ON l.id = b.location_id LEFT JOIN stock_lots sl ON sl.id = b.lot_id
		WHERE b.warehouse_id = $1 AND b.item_id = $2 AND b.quantity > 0 AND l.kind IN ('bin', 'shelf', 'aisle', 'zone')
		GROUP BY b.location_id, b.lot_id, l.sort_key
		ORDER BY CASE WHEN $3 = 'fefo' THEN min(sl.expiry) END NULLS LAST, CASE WHEN $3 = 'closest' THEN l.sort_key END, min(b.in_date), l.sort_key`,
		wh, item, removal)
	if err != nil {
		return nil, err
	}
	parts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pickPart, error) {
		var p pickPart
		var loc, q string
		err := r.Scan(&loc, &p.lot, &q)
		p.loc = &loc
		p.qty, _ = decimal.NewFromString(q)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	out := []pickPart{}
	left := qty
	for _, p := range parts {
		if !left.IsPositive() {
			break
		}
		t := decimal.Min(p.qty, left)
		out = append(out, pickPart{p.loc, p.lot, t})
		left = left.Sub(t)
	}
	if left.IsPositive() {
		out = append(out, pickPart{nil, nil, left}) // from the unbinned stock
	}
	return out, nil
}

type opRefs struct {
	po, receipt, so, prev *string
	origin, date          string
}

func newOp(ctx context.Context, tx pgx.Tx, cid, wh, kind, status string, ref opRefs) (string, error) {
	var n int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'wh_op')`, cid).Scan(&n); err != nil {
		return "", err
	}
	if ref.date == "" {
		ref.date = today()
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO wh_ops (company_id, number, warehouse_id, kind, status, origin, purchase_order_id, receipt_id, sales_order_id,
		prev_op_id, scheduled_date) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date) RETURNING id::text`,
		cid, n, wh, kind, status, ref.origin, ref.po, ref.receipt, ref.so, ref.prev, ref.date).Scan(&id)
	return id, err
}

type opLine struct {
	item          string
	lot, soLine   *string
	from, to, pkg *string
	qty           decimal.Decimal
}

func addOpLines(ctx context.Context, tx pgx.Tx, cid, op string, lines []opLine) error {
	var start int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(line_no), 0) FROM wh_op_lines WHERE op_id = $1`, op).Scan(&start); err != nil {
		return err
	}
	for i, l := range lines {
		if !l.qty.IsPositive() {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wh_op_lines (company_id, op_id, line_no, item_id, lot_id, so_line_id, quantity, from_location_id,
			to_location_id, package_id) VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9, $10)`,
			cid, op, start+i+1, l.item, l.lot, l.soLine, l.qty.String(), l.from, l.to, l.pkg); err != nil {
			return err
		}
	}
	return nil
}

func sptr(s string) *string { return &s }

// ======================= hooks from purchasing and sales =======================

// whOnReceipt: a goods receipt was recorded; warehouses with bins get their putaway (or QC) step.
func whOnReceipt(ctx context.Context, tx pgx.Tx, cid, receiptID, date string) error {
	def, err := defaultWarehouse(ctx, tx, cid)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT pl.item_id::text, coalesce(pl.warehouse_id::text, $2), (gl.quantity * coalesce(u.factor, 1))::text,
		po.id::text, po.number, g.number
		FROM goods_receipt_lines gl JOIN goods_receipts g ON g.id = gl.receipt_id JOIN purchase_order_lines pl ON pl.id = gl.po_line_id
		JOIN purchase_orders po ON po.id = pl.order_id JOIN items it ON it.id = pl.item_id LEFT JOIN item_units u ON u.id = pl.unit_id
		WHERE gl.receipt_id = $1 AND it.kind = 'stock' AND po.dropship_order_id IS NULL ORDER BY pl.line_no`, receiptID, def)
	if err != nil {
		return err
	}
	type rl struct {
		item, wh, po string
		qty          decimal.Decimal
		poN, gN      int64
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rl, error) {
		var x rl
		var q string
		err := r.Scan(&x.item, &x.wh, &q, &x.po, &x.poN, &x.gN)
		x.qty, _ = decimal.NewFromString(q)
		return x, err
	})
	if err != nil {
		return err
	}
	byWh := map[string][]rl{}
	order := []string{}
	for _, x := range list {
		if _, ok := byWh[x.wh]; !ok {
			order = append(order, x.wh)
		}
		byWh[x.wh] = append(byWh[x.wh], x)
	}
	for _, wh := range order {
		cfg, err := loadWhCfg(ctx, tx, cid, wh)
		if err != nil {
			return err
		}
		if !cfg.Bins || cfg.Transit {
			continue
		}
		xs := byWh[wh]
		ref := opRefs{po: &xs[0].po, receipt: &receiptID, origin: fmt.Sprintf("PO #%d · GR #%d", xs[0].poN, xs[0].gN), date: date}
		var src *string
		if cfg.RecSteps >= 2 {
			in, err := whSpecial(ctx, tx, cid, wh, "input")
			if err != nil {
				return err
			}
			src = &in
			for _, x := range xs {
				if err := binPut(ctx, tx, cid, wh, in, x.item, nil, x.qty); err != nil {
					return err
				}
			}
		}
		if cfg.RecSteps == 3 {
			qc, err := whSpecial(ctx, tx, cid, wh, "qc")
			if err != nil {
				return err
			}
			op, err := newOp(ctx, tx, cid, wh, "qc", "ready", ref)
			if err != nil {
				return err
			}
			lines := []opLine{}
			for _, x := range xs {
				lines = append(lines, opLine{item: x.item, from: src, to: &qc, qty: x.qty})
			}
			if err := addOpLines(ctx, tx, cid, op, lines); err != nil {
				return err
			}
			continue
		}
		op, err := newOp(ctx, tx, cid, wh, "putaway", "ready", ref)
		if err != nil {
			return err
		}
		parts := []itemQty{}
		for _, x := range xs {
			parts = append(parts, itemQty{item: x.item, qty: x.qty})
		}
		lines, err := putawayLines(ctx, tx, cid, cfg, parts, src)
		if err != nil {
			return err
		}
		if err := addOpLines(ctx, tx, cid, op, lines); err != nil {
			return err
		}
	}
	return nil
}

type itemQty struct {
	item string
	lot  *string
	qty  decimal.Decimal
}

// putawayLines: where each arriving item goes (cross-dock: straight to OUTPUT what open sales orders wait for).
func putawayLines(ctx context.Context, tx pgx.Tx, cid string, cfg *whCfg, parts []itemQty, src *string) ([]opLine, error) {
	lines := []opLine{}
	for _, p := range parts {
		qty := p.qty
		if cfg.Crossdock && qty.IsPositive() {
			var dem string
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(l.quantity - l.qty_delivered), 0)::text FROM sales_order_lines l
				JOIN sales_orders o ON o.id = l.order_id WHERE o.company_id = $1 AND o.status = 'confirmed' AND l.item_id = $2
				AND coalesce(l.warehouse_id, (SELECT id FROM warehouses WHERE company_id = $1 AND is_active ORDER BY created_at LIMIT 1)) = $3`,
				cid, p.item, cfg.ID).Scan(&dem); err != nil {
				return nil, err
			}
			d, _ := decimal.NewFromString(dem)
			if d.IsPositive() {
				out, err := whSpecial(ctx, tx, cid, cfg.ID, "output")
				if err != nil {
					return nil, err
				}
				t := decimal.Min(d, qty)
				lines = append(lines, opLine{item: p.item, lot: p.lot, from: src, to: &out, qty: t})
				qty = qty.Sub(t)
			}
		}
		if qty.IsPositive() {
			to, err := suggestPutaway(ctx, tx, cfg.ID, p.item)
			if err != nil {
				return nil, err
			}
			lines = append(lines, opLine{item: p.item, lot: p.lot, from: src, to: to, qty: qty})
		}
	}
	return lines, nil
}

// whOnReceiptCancelled: the putaway / QC steps of a cancelled receipt are cancelled; what waited in INPUT leaves it.
func whOnReceiptCancelled(ctx context.Context, tx pgx.Tx, cid, receiptID string) error {
	rows, err := tx.Query(ctx, `SELECT o.warehouse_id::text, l.item_id::text, l.from_location_id::text, (l.quantity - l.done_qty)::text
		FROM wh_ops o JOIN wh_op_lines l ON l.op_id = o.id
		WHERE o.receipt_id = $1 AND o.company_id = $2 AND o.status IN ('ready', 'waiting') AND l.from_location_id IS NOT NULL`, receiptID, cid)
	if err != nil {
		return err
	}
	type x struct {
		wh, item, loc string
		q             decimal.Decimal
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (x, error) {
		var v x
		var q string
		err := r.Scan(&v.wh, &v.item, &v.loc, &q)
		v.q, _ = decimal.NewFromString(q)
		return v, err
	})
	if err != nil {
		return err
	}
	for _, v := range list {
		if _, err := binTake(ctx, tx, v.loc, v.item, nil, v.q, false); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE wh_ops SET status = 'cancelled' WHERE receipt_id = $1 AND company_id = $2 AND status IN ('ready', 'waiting')`, receiptID, cid)
	return err
}

// whOnOrderConfirmed: warehouses with 2/3-step delivery get a pick operation for the order's stock lines.
func whOnOrderConfirmed(ctx context.Context, tx pgx.Tx, cid, so string) error {
	def, err := defaultWarehouse(ctx, tx, cid)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT l.id::text, l.item_id::text, coalesce(l.warehouse_id::text, $2), (l.quantity - l.qty_delivered)::text,
		coalesce(o.order_number, o.number)
		FROM sales_order_lines l JOIN sales_orders o ON o.id = l.order_id JOIN items it ON it.id = l.item_id
		WHERE l.order_id = $1 AND it.kind = 'stock' AND l.quantity > l.qty_delivered ORDER BY l.line_no`, so, def)
	if err != nil {
		return err
	}
	type sl struct {
		id, item, wh string
		qty          decimal.Decimal
		n            int64
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sl, error) {
		var x sl
		var q string
		err := r.Scan(&x.id, &x.item, &x.wh, &q, &x.n)
		x.qty, _ = decimal.NewFromString(q)
		return x, err
	})
	if err != nil {
		return err
	}
	byWh := map[string][]sl{}
	order := []string{}
	for _, x := range list {
		if _, ok := byWh[x.wh]; !ok {
			order = append(order, x.wh)
		}
		byWh[x.wh] = append(byWh[x.wh], x)
	}
	for _, wh := range order {
		cfg, err := loadWhCfg(ctx, tx, cid, wh)
		if err != nil {
			return err
		}
		if !cfg.Bins || cfg.DelSteps < 2 || cfg.Transit {
			continue
		}
		dest := "output"
		if cfg.DelSteps == 3 {
			dest = "pack"
		}
		to, err := whSpecial(ctx, tx, cid, wh, dest)
		if err != nil {
			return err
		}
		xs := byWh[wh]
		op, err := newOp(ctx, tx, cid, wh, "pick", "ready", opRefs{so: &so, origin: fmt.Sprintf("SO #%d", xs[0].n)})
		if err != nil {
			return err
		}
		lines := []opLine{}
		for _, x := range xs {
			parts, err := suggestPick(ctx, tx, wh, cfg.Removal, x.item, x.qty)
			if err != nil {
				return err
			}
			for _, p := range parts {
				lines = append(lines, opLine{item: x.item, lot: p.lot, soLine: sptr(x.id), from: p.loc, to: &to, qty: p.qty})
			}
		}
		if err := addOpLines(ctx, tx, cid, op, lines); err != nil {
			return err
		}
	}
	return nil
}

func whOnOrderCancelled(ctx context.Context, tx pgx.Tx, cid, so string) error {
	_, err := tx.Exec(ctx, `UPDATE wh_ops SET status = 'cancelled' WHERE sales_order_id = $1 AND company_id = $2 AND status IN ('ready', 'waiting')`, so, cid)
	return err
}

// whRefuseDirectDelivery: an order that goes through warehouse steps is delivered by its "ship" step.
func whRefuseDirectDelivery(ctx context.Context, tx pgx.Tx, cid, so string) error {
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wh_ops WHERE sales_order_id = $1 AND company_id = $2 AND status IN ('ready', 'waiting'))`,
		so, cid).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return &apiError{status: http.StatusConflict, code: "use_warehouse_steps"}
	}
	return nil
}

// ======================= operations =======================

const whOpCols = `o.id, o.number, o.warehouse_id, w.name AS warehouse, o.kind, o.status, o.origin, o.purchase_order_id, o.receipt_id,
	o.sales_order_id, o.batch_id, o.prev_op_id, o.delivery_id, o.scheduled_date, o.notes, o.done_at, o.created_at,
	(SELECT c.name FROM sales_orders so JOIN customers c ON c.id = so.customer_id WHERE so.id = o.sales_order_id) AS customer_name,
	(SELECT count(*) FROM wh_op_lines l WHERE l.op_id = o.id) AS lines,
	(SELECT trim_scale(coalesce(sum(l.quantity), 0))::text FROM wh_op_lines l WHERE l.op_id = o.id) AS quantity,
	(SELECT b.number FROM wh_batches b WHERE b.id = o.batch_id) AS batch_number`

const whOpFrom = ` FROM wh_ops o JOIN warehouses w ON w.id = o.warehouse_id`

// GET /wh/ops?status=open|all|<status>&kind=&warehouse_id=&order_id=
func (s *Server) whListOps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, kind, wh, so := q.Get("status"), q.Get("kind"), q.Get("warehouse_id"), q.Get("order_id")
	if st == "" {
		st = "open"
	}
	if (wh != "" && !isUUID(wh)) || (so != "" && !isUUID(so)) || len(kind) > 20 || len(st) > 20 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+whOpCols+whOpFrom+` WHERE o.company_id = $1
			AND ($2 = 'all' OR ($2 = 'open' AND o.status IN ('ready', 'waiting')) OR o.status = $2)
			AND ($3 = '' OR o.kind = $3) AND ($4 = '' OR o.warehouse_id = nullif($4, '')::uuid)
			AND ($5 = '' OR o.sales_order_id = nullif($5, '')::uuid)
			ORDER BY o.status IN ('done', 'cancelled'), o.scheduled_date, o.number LIMIT 1000`, r.PathValue("cid"), st, kind, wh, so)
	})
}

func loadWhOp(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	op, err := queryJSONOne(ctx, tx, `SELECT `+whOpCols+whOpFrom+` WHERE o.id = $1 AND o.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.item_id, it.code AS item_code, it.name AS item_name, it.unit, it.barcode,
		l.lot_id, sl.lot_no, sl.expiry, l.so_line_id, trim_scale(l.quantity)::text AS quantity, trim_scale(l.done_qty)::text AS done_qty,
		l.from_location_id, fl.code AS from_code, l.to_location_id, tl.code AS to_code, l.package_id, p.number AS package_number
		FROM wh_op_lines l JOIN items it ON it.id = l.item_id LEFT JOIN stock_lots sl ON sl.id = l.lot_id
		LEFT JOIN wh_locations fl ON fl.id = l.from_location_id LEFT JOIN wh_locations tl ON tl.id = l.to_location_id
		LEFT JOIN wh_packages p ON p.id = l.package_id
		WHERE l.op_id = $1 ORDER BY coalesce(fl.sort_key, ''), l.line_no`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"op": op, "lines": lines}, nil
}

func (s *Server) whGetOp(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadWhOp(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

type opLineIn struct {
	ItemID         string  `json:"item_id"`
	LotID          *string `json:"lot_id"`
	Quantity       string  `json:"quantity"`
	FromLocationID *string `json:"from_location_id"`
	ToLocationID   *string `json:"to_location_id"`
}

func nilBlank(p *string) *string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil
	}
	v := strings.TrimSpace(*p)
	return &v
}

// POST /wh/ops: an internal transfer between places of one warehouse (ready, done later by the scanner).
func (s *Server) whCreateOp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WarehouseID   string     `json:"warehouse_id"`
		Notes         string     `json:"notes"`
		ScheduledDate string     `json:"scheduled_date"`
		Lines         []opLineIn `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Lines) == 0 || len(in.Lines) > 500 || (in.ScheduledDate != "" && !validDate(in.ScheduledDate)) || len(in.Notes) > 2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cfg, err := loadWhCfg(ctx, tx, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		lines := []opLine{}
		for _, l := range in.Lines {
			ol, err := checkOpLineIn(ctx, tx, cid, cfg.ID, l)
			if err != nil {
				return nil, err
			}
			lines = append(lines, ol)
		}
		op, err := newOp(ctx, tx, cid, cfg.ID, "internal", "ready", opRefs{origin: "", date: in.ScheduledDate})
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE wh_ops SET notes = $2 WHERE id = $1`, op, strings.TrimSpace(in.Notes)); err != nil {
			return nil, err
		}
		if err := addOpLines(ctx, tx, cid, op, lines); err != nil {
			return nil, err
		}
		return loadWhOp(ctx, tx, cid, op)
	})
}

func checkOpLineIn(ctx context.Context, tx pgx.Tx, cid, wh string, l opLineIn) (opLine, error) {
	q, ok := money(l.Quantity)
	if !ok || !q.IsPositive() {
		return opLine{}, badRequest("invalid_amount")
	}
	it, err := loadItem(ctx, tx, cid, l.ItemID)
	if err != nil {
		return opLine{}, err
	}
	if it.Kind != "stock" {
		return opLine{}, badRequest("not_a_stock_item")
	}
	ol := opLine{item: it.ID, lot: nilBlank(l.LotID), from: nilBlank(l.FromLocationID), to: nilBlank(l.ToLocationID), qty: q}
	if err := checkLot(ctx, tx, cid, it.ID, ol.lot); err != nil {
		return opLine{}, err
	}
	if err := checkLoc(ctx, tx, cid, wh, ol.from); err != nil {
		return opLine{}, err
	}
	if err := checkLoc(ctx, tx, cid, wh, ol.to); err != nil {
		return opLine{}, err
	}
	if ol.from == nil && ol.to == nil {
		return opLine{}, badRequest("invalid_location")
	}
	return ol, nil
}

type opDoneLine struct {
	LineID         string  `json:"line_id"`
	DoneQty        string  `json:"done_qty"`
	FromLocationID *string `json:"from_location_id"`
	ToLocationID   *string `json:"to_location_id"`
	PackageID      *string `json:"package_id"`
	LotID          *string `json:"lot_id"`
}

type opDoneIn struct {
	Date  string       `json:"date"`
	Notes string       `json:"notes"`
	Lines []opDoneLine `json:"lines"` // none = everything as planned
}

// POST /wh/ops/{id}/done
func (s *Server) whOpDone(w http.ResponseWriter, r *http.Request) {
	var in opDoneIn
	if !decodeBody(w, r, &in) {
		return
	}
	if (in.Date != "" && !validDate(in.Date)) || len(in.Lines) > 1000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		next, err := opDone(ctx, tx, r, cid, id, in)
		if err != nil {
			return nil, err
		}
		op, err := loadWhOp(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"op": op, "next": next}, nil
	})
}

type doneResult struct {
	NextOp     *string `json:"next_op_id"`
	Backorder  *string `json:"backorder_id"`
	DeliveryID *string `json:"delivery_id"`
}

// opDone carries out an operation (all lines as planned unless given) and prepares what follows.
func opDone(ctx context.Context, tx pgx.Tx, r *http.Request, cid, id string, in opDoneIn) (*doneResult, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	var wh, kind, status, origin string
	var po, receipt, so *string
	err := tx.QueryRow(ctx, `SELECT warehouse_id::text, kind, status, origin, purchase_order_id::text, receipt_id::text, sales_order_id::text
		FROM wh_ops WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&wh, &kind, &status, &origin, &po, &receipt, &so)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	if status != "ready" {
		return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	cfg, err := loadWhCfg(ctx, tx, cid, wh)
	if err != nil {
		return nil, err
	}
	if in.Date == "" {
		in.Date = today()
	}
	type ln struct {
		id, item              string
		lot, soLine, from, to *string
		pkg                   *string
		qty, done             decimal.Decimal
	}
	rows, err := tx.Query(ctx, `SELECT id::text, item_id::text, lot_id::text, so_line_id::text, from_location_id::text, to_location_id::text,
		package_id::text, quantity::text FROM wh_op_lines WHERE op_id = $1 ORDER BY line_no FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	lines, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ln, error) {
		var x ln
		var q string
		err := row.Scan(&x.id, &x.item, &x.lot, &x.soLine, &x.from, &x.to, &x.pkg, &q)
		x.qty, _ = decimal.NewFromString(q)
		x.done = x.qty
		return x, err
	})
	if err != nil {
		return nil, err
	}
	byID := map[string]int{}
	for i, x := range lines {
		byID[x.id] = i
	}
	if len(in.Lines) > 0 {
		for i := range lines {
			lines[i].done = decimal.Zero // only the lines given are done
		}
	}
	for _, d := range in.Lines {
		i, ok := byID[d.LineID]
		if !ok {
			return nil, badRequest("invalid_line")
		}
		q, ok := money(d.DoneQty)
		if !ok || q.IsNegative() || q.GreaterThan(lines[i].qty) {
			return nil, badRequest("invalid_amount")
		}
		lines[i].done = q
		if f := nilBlank(d.FromLocationID); f != nil {
			if err := checkLoc(ctx, tx, cid, wh, f); err != nil {
				return nil, err
			}
			lines[i].from = f
		}
		if t := nilBlank(d.ToLocationID); t != nil {
			if err := checkLoc(ctx, tx, cid, wh, t); err != nil {
				return nil, err
			}
			lines[i].to = t
		}
		if l := nilBlank(d.LotID); l != nil {
			if err := checkLot(ctx, tx, cid, lines[i].item, l); err != nil {
				return nil, err
			}
			lines[i].lot = l
		}
		if p := nilBlank(d.PackageID); p != nil {
			lines[i].pkg = p
		}
	}
	anyDone := false
	for _, x := range lines {
		anyDone = anyDone || x.done.IsPositive()
	}
	if !anyDone {
		return nil, badRequest("nothing_done")
	}
	res := &doneResult{}
	ship := []qtyIn{}
	for _, x := range lines {
		if !x.done.IsPositive() {
			continue
		}
		switch kind {
		case "ship":
			if x.soLine != nil {
				ship = append(ship, qtyIn{LineID: *x.soLine, Quantity: x.done.String()})
			}
		default:
			arrival := kind == "putaway" && receipt != nil && x.from == nil
			if err := binMove(ctx, tx, cid, wh, x.item, x.lot, x.from, x.to, x.done, arrival); err != nil {
				return nil, err
			}
		}
		if x.pkg != nil {
			if err := packageAdd(ctx, tx, cid, wh, *x.pkg, x.item, x.lot, x.done); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE wh_op_lines SET done_qty = $2::numeric, from_location_id = $3, to_location_id = $4, lot_id = $5, package_id = $6
			WHERE id = $1`, x.id, x.done.String(), x.from, x.to, x.lot, x.pkg); err != nil {
			return nil, err
		}
	}
	if kind == "ship" {
		if so == nil || len(ship) == 0 {
			return nil, badRequest("nothing_done")
		}
		var did string
		if did, err = deliverOrderTx(ctx, tx, r, cid, *so, in.Date, in.Notes, ship); err != nil {
			return nil, err
		}
		res.DeliveryID = &did
		if _, err := tx.Exec(ctx, `UPDATE wh_ops SET delivery_id = $2 WHERE id = $1`, id, did); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE wh_packages SET status = 'shipped', delivery_id = $2 WHERE sales_order_id = $1 AND status IN ('open', 'sealed')`,
			*so, did); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE wh_ops SET status = 'done', done_at = now(), done_by = app_user(),
		notes = CASE WHEN $2 = '' THEN notes ELSE $2 END WHERE id = $1`, id, strings.TrimSpace(in.Notes)); err != nil {
		return nil, err
	}
	ref := opRefs{po: po, receipt: receipt, so: so, prev: &id, origin: origin, date: in.Date}
	// what was not done waits in a backorder of the same step
	back := []opLine{}
	for _, x := range lines {
		if rest := x.qty.Sub(x.done); rest.IsPositive() {
			back = append(back, opLine{item: x.item, lot: x.lot, soLine: x.soLine, from: x.from, to: x.to, qty: rest})
		}
	}
	if len(back) > 0 {
		var prev *string
		_ = tx.QueryRow(ctx, `SELECT prev_op_id::text FROM wh_ops WHERE id = $1`, id).Scan(&prev)
		bref := ref
		bref.prev = prev
		b, err := newOp(ctx, tx, cid, wh, kind, "ready", bref)
		if err != nil {
			return nil, err
		}
		if err := addOpLines(ctx, tx, cid, b, back); err != nil {
			return nil, err
		}
		res.Backorder = &b
	}
	// the next step, with what was really done
	nextKind := ""
	var nextFrom, nextTo *string
	switch {
	case kind == "qc":
		nextKind = "putaway"
	case kind == "pick" && so != nil && cfg.DelSteps == 3:
		nextKind = "pack"
		p, err := whSpecial(ctx, tx, cid, wh, "pack")
		if err != nil {
			return nil, err
		}
		o, err := whSpecial(ctx, tx, cid, wh, "output")
		if err != nil {
			return nil, err
		}
		nextFrom, nextTo = &p, &o
	case (kind == "pick" || kind == "pack") && so != nil:
		nextKind = "ship"
		o, err := whSpecial(ctx, tx, cid, wh, "output")
		if err != nil {
			return nil, err
		}
		nextFrom = &o
	}
	if nextKind != "" {
		nl := []opLine{}
		if nextKind == "putaway" {
			parts := []itemQty{}
			for _, x := range lines {
				if x.done.IsPositive() {
					parts = append(parts, itemQty{x.item, x.lot, x.done})
				}
			}
			src := lines[0].to
			pl, err := putawayLines(ctx, tx, cid, cfg, parts, src)
			if err != nil {
				return nil, err
			}
			nl = pl
		} else {
			for _, x := range lines {
				if x.done.IsPositive() {
					nl = append(nl, opLine{item: x.item, lot: x.lot, soLine: x.soLine, from: nextFrom, to: nextTo, pkg: x.pkg, qty: x.done})
				}
			}
		}
		n, err := newOp(ctx, tx, cid, wh, nextKind, "ready", ref)
		if err != nil {
			return nil, err
		}
		if err := addOpLines(ctx, tx, cid, n, nl); err != nil {
			return nil, err
		}
		res.NextOp = &n
	}
	return res, nil
}

// POST /wh/ops/{id}/cancel (an operation not done yet).
func (s *Server) whCancelOp(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE wh_ops SET status = 'cancelled', batch_id = NULL WHERE id = $1 AND company_id = $2 AND status IN ('ready', 'waiting')`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			var ok bool
			_ = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wh_ops WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok)
			if !ok {
				return nil, notFound()
			}
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return loadWhOp(ctx, tx, cid, id)
	})
}

// POST /wh/move: an immediate move (scanner: putaway, bin to bin), recorded as a done internal operation.
func (s *Server) whMove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WarehouseID string `json:"warehouse_id"`
		opLineIn
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cfg, err := loadWhCfg(ctx, tx, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		ol, err := checkOpLineIn(ctx, tx, cid, cfg.ID, in.opLineIn)
		if err != nil {
			return nil, err
		}
		op, err := newOp(ctx, tx, cid, cfg.ID, "internal", "ready", opRefs{origin: "scan"})
		if err != nil {
			return nil, err
		}
		if err := addOpLines(ctx, tx, cid, op, []opLine{ol}); err != nil {
			return nil, err
		}
		if _, err := opDone(ctx, tx, r, cid, op, opDoneIn{}); err != nil {
			return nil, err
		}
		return loadWhOp(ctx, tx, cid, op)
	})
}

// POST /wh/count {warehouse_id, location_id, lines: [{item_id, lot_id, counted}]}: what a bin really holds
// (physical; a difference with the books is shown, the stock count corrects the books).
func (s *Server) whCount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WarehouseID string `json:"warehouse_id"`
		LocationID  string `json:"location_id"`
		Lines       []struct {
			ItemID  string  `json:"item_id"`
			LotID   *string `json:"lot_id"`
			Counted string  `json:"counted"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Lines) == 0 || len(in.Lines) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cfg, err := loadWhCfg(ctx, tx, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		loc := &in.LocationID
		if err := checkLoc(ctx, tx, cid, cfg.ID, loc); err != nil {
			return nil, err
		}
		warnings := []map[string]string{}
		op, err := newOp(ctx, tx, cid, cfg.ID, "internal", "ready", opRefs{origin: "count"})
		if err != nil {
			return nil, err
		}
		lines := []opLine{}
		for _, l := range in.Lines {
			c, ok := money(l.Counted)
			if !ok || c.IsNegative() {
				return nil, badRequest("invalid_amount")
			}
			it, err := loadItem(ctx, tx, cid, l.ItemID)
			if err != nil {
				return nil, err
			}
			lot := nilBlank(l.LotID)
			if err := checkLot(ctx, tx, cid, it.ID, lot); err != nil {
				return nil, err
			}
			var hs string
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(quantity), 0)::text FROM wh_bin_stock WHERE location_id = $1 AND item_id = $2
				AND ($3::uuid IS NULL OR lot_id = $3)`, *loc, it.ID, lot).Scan(&hs); err != nil {
				return nil, err
			}
			have, _ := decimal.NewFromString(hs)
			diff := c.Sub(have)
			switch {
			case diff.IsPositive():
				if err := binPut(ctx, tx, cid, cfg.ID, *loc, it.ID, lot, diff); err != nil {
					return nil, err
				}
				lines = append(lines, opLine{item: it.ID, lot: lot, to: loc, qty: diff})
			case diff.IsNegative():
				if _, err := binTake(ctx, tx, *loc, it.ID, lot, diff.Neg(), false); err != nil {
					return nil, err
				}
				lines = append(lines, opLine{item: it.ID, lot: lot, from: loc, qty: diff.Neg()})
			}
			var over string
			if err := tx.QueryRow(ctx, `SELECT (coalesce((SELECT sum(quantity) FROM wh_bin_stock WHERE warehouse_id = $1 AND item_id = $2), 0)
				- coalesce((SELECT quantity FROM stock WHERE warehouse_id = $1 AND item_id = $2), 0))::text`, cfg.ID, it.ID).Scan(&over); err != nil {
				return nil, err
			}
			if o, _ := decimal.NewFromString(over); o.IsPositive() {
				warnings = append(warnings, map[string]string{"code": "bins_exceed_stock", "item": it.Code + " " + it.Name, "quantity": o.String()})
			}
		}
		if len(lines) == 0 {
			if _, err := tx.Exec(ctx, `UPDATE wh_ops SET status = 'cancelled' WHERE id = $1`, op); err != nil {
				return nil, err
			}
			return map[string]any{"op_id": nil, "warnings": warnings}, nil
		}
		if err := addOpLines(ctx, tx, cid, op, lines); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE wh_op_lines SET done_qty = quantity WHERE op_id = $1`, op); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE wh_ops SET status = 'done', done_at = now(), done_by = app_user() WHERE id = $1`, op); err != nil {
			return nil, err
		}
		return map[string]any{"op_id": op, "warnings": warnings}, nil
	})
}

// ======================= warehouses, locations, rules =======================

// GET /wh/warehouses
func (s *Server) whWarehouses(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := defaultWarehouse(ctx, tx, r.PathValue("cid")); err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT w.id, w.name, w.code, w.is_active, w.use_bins, w.reception_steps, w.delivery_steps, w.removal,
			w.crossdock, w.is_transit, w.address,
			(SELECT count(*) FROM wh_locations l WHERE l.warehouse_id = w.id) AS locations,
			(SELECT count(*) FROM wh_ops o WHERE o.warehouse_id = w.id AND o.status = 'ready') AS ops_ready,
			coalesce((SELECT sum(st.value) FROM stock st WHERE st.warehouse_id = w.id), 0)::text AS value
			FROM warehouses w WHERE w.company_id = $1 ORDER BY w.is_transit, w.created_at`, r.PathValue("cid"))
	})
}

// PATCH /wh/warehouses/{id}
func (s *Server) whUpdateWarehouse(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name           *string `json:"name"`
		Code           *string `json:"code"`
		Address        *string `json:"address"`
		UseBins        *bool   `json:"use_bins"`
		ReceptionSteps *int    `json:"reception_steps"`
		DeliverySteps  *int    `json:"delivery_steps"`
		Removal        *string `json:"removal"`
		Crossdock      *bool   `json:"crossdock"`
		IsActive       *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	bad := (in.ReceptionSteps != nil && (*in.ReceptionSteps < 1 || *in.ReceptionSteps > 3)) ||
		(in.DeliverySteps != nil && (*in.DeliverySteps < 1 || *in.DeliverySteps > 3)) ||
		(in.Removal != nil && !contains([]string{"fifo", "fefo", "closest"}, *in.Removal)) ||
		(in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 120)) || (in.Code != nil && len(*in.Code) > 20) ||
		(in.Address != nil && len(*in.Address) > 500)
	if bad {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cfg, err := loadWhCfg(ctx, tx, cid, id)
		if err != nil {
			return nil, notFound()
		}
		bins := cfg.Bins
		if in.UseBins != nil {
			bins = *in.UseBins
		}
		rs, ds := cfg.RecSteps, cfg.DelSteps
		if in.ReceptionSteps != nil {
			rs = *in.ReceptionSteps
		}
		if in.DeliverySteps != nil {
			ds = *in.DeliverySteps
		}
		if (rs > 1 || ds > 1) && !bins {
			return nil, badRequest("steps_need_bins")
		}
		if !bins && cfg.Bins {
			var held bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wh_bin_stock WHERE warehouse_id = $1)
				OR EXISTS (SELECT 1 FROM wh_ops WHERE warehouse_id = $1 AND status IN ('ready', 'waiting'))`, id).Scan(&held); err != nil {
				return nil, err
			}
			if held {
				return nil, &apiError{status: http.StatusConflict, code: "bins_not_empty"}
			}
		}
		trim := func(p *string) *string {
			if p == nil {
				return nil
			}
			v := strings.TrimSpace(*p)
			return &v
		}
		_, err = tx.Exec(ctx, `UPDATE warehouses SET name = coalesce($3, name), code = coalesce($4, code), address = coalesce($5, address),
			use_bins = $6, reception_steps = $7, delivery_steps = $8, removal = coalesce($9, removal), crossdock = coalesce($10, crossdock),
			is_active = coalesce($11, is_active) WHERE id = $1 AND company_id = $2`,
			id, cid, trim(in.Name), trim(in.Code), trim(in.Address), bins, rs, ds, in.Removal, in.Crossdock, in.IsActive)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
		}
		if err != nil {
			return nil, err
		}
		if bins {
			need := []string{}
			if rs >= 2 {
				need = append(need, "input")
			}
			if rs == 3 {
				need = append(need, "qc")
			}
			if ds >= 2 {
				need = append(need, "output")
			}
			if ds == 3 {
				need = append(need, "pack")
			}
			for _, k := range need {
				if _, err := whSpecial(ctx, tx, cid, id, k); err != nil {
					return nil, err
				}
			}
		}
		return queryJSONOne(ctx, tx, `SELECT id, name, code, use_bins, reception_steps, delivery_steps, removal, crossdock, is_transit, address, is_active
			FROM warehouses WHERE id = $1`, id)
	})
}

// GET /wh/locations?warehouse_id=
func (s *Server) whLocations(w http.ResponseWriter, r *http.Request) {
	wh := r.URL.Query().Get("warehouse_id")
	if wh != "" && !isUUID(wh) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT l.id, l.warehouse_id, w.name AS warehouse, l.parent_id, l.kind, l.code, l.name, l.barcode, l.sort_key,
			l.is_active, (SELECT count(DISTINCT b.item_id) FROM wh_bin_stock b WHERE b.location_id = l.id) AS items,
			(SELECT trim_scale(coalesce(sum(b.quantity), 0))::text FROM wh_bin_stock b WHERE b.location_id = l.id) AS quantity
			FROM wh_locations l JOIN warehouses w ON w.id = l.warehouse_id
			WHERE l.company_id = $1 AND ($2 = '' OR l.warehouse_id = nullif($2, '')::uuid) ORDER BY w.created_at, l.sort_key LIMIT 5000`,
			r.PathValue("cid"), wh)
	})
}

type locIn struct {
	WarehouseID string  `json:"warehouse_id"`
	ParentID    *string `json:"parent_id"`
	Kind        string  `json:"kind"`
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Barcode     string  `json:"barcode"`
}

func insertLoc(ctx context.Context, tx pgx.Tx, cid string, in locIn) (string, error) {
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	if in.Code == "" || len(in.Code) > 60 || !contains(whKinds, in.Kind) || len(in.Name) > 120 || len(in.Barcode) > 80 {
		return "", badRequest("bad_request")
	}
	parentKey := ""
	if p := nilBlank(in.ParentID); p != nil {
		if err := checkLoc(ctx, tx, cid, in.WarehouseID, p); err != nil {
			return "", err
		}
		if err := tx.QueryRow(ctx, `SELECT sort_key FROM wh_locations WHERE id = $1`, *p).Scan(&parentKey); err != nil {
			return "", err
		}
		in.ParentID = p
	} else {
		in.ParentID = nil
	}
	key := in.Code
	if parentKey != "" {
		key = parentKey + "/" + in.Code
	}
	bc := strings.TrimSpace(in.Barcode)
	if bc == "" {
		bc = in.Code
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO wh_locations (company_id, warehouse_id, parent_id, kind, code, name, barcode, sort_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`, cid, in.WarehouseID, in.ParentID, in.Kind, in.Code,
		strings.TrimSpace(in.Name), bc, key).Scan(&id)
	if isUniqueViolation(err) {
		return "", &apiError{status: http.StatusConflict, code: "duplicate", detail: in.Code}
	}
	return id, err
}

// POST /wh/locations
func (s *Server) whCreateLocation(w http.ResponseWriter, r *http.Request) {
	var in locIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := loadWhCfg(ctx, tx, cid, in.WarehouseID); err != nil {
			return nil, err
		}
		id, err := insertLoc(ctx, tx, cid, in)
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	})
}

// POST /wh/locations/generate {warehouse_id, zone, aisles, shelves, bins}: zone Z → aisles Z-01… → shelves Z-01-1… → bins Z-01-1-01…
func (s *Server) whGenerateLocations(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WarehouseID string `json:"warehouse_id"`
		Zone        string `json:"zone"`
		Aisles      int    `json:"aisles"`
		Shelves     int    `json:"shelves"`
		Bins        int    `json:"bins"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Zone = strings.ToUpper(strings.TrimSpace(in.Zone))
	if in.Zone == "" || len(in.Zone) > 10 || in.Aisles < 1 || in.Aisles > 50 || in.Shelves < 1 || in.Shelves > 20 || in.Bins < 0 || in.Bins > 50 ||
		in.Aisles*in.Shelves*max(in.Bins, 1) > 3000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cfg, err := loadWhCfg(ctx, tx, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		if !cfg.Bins {
			if _, err := tx.Exec(ctx, `UPDATE warehouses SET use_bins = true WHERE id = $1`, cfg.ID); err != nil {
				return nil, err
			}
		}
		var zone string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM wh_locations WHERE warehouse_id = $1 AND code = $2`, cfg.ID, in.Zone).Scan(&zone); errors.Is(err, pgx.ErrNoRows) {
			if zone, err = insertLoc(ctx, tx, cid, locIn{WarehouseID: cfg.ID, Kind: "zone", Code: in.Zone, Name: in.Zone}); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		n := 0
		for a := 1; a <= in.Aisles; a++ {
			ac := fmt.Sprintf("%s-%02d", in.Zone, a)
			aid, err := insertLoc(ctx, tx, cid, locIn{WarehouseID: cfg.ID, ParentID: &zone, Kind: "aisle", Code: ac})
			if err != nil {
				return nil, err
			}
			n++
			for sh := 1; sh <= in.Shelves; sh++ {
				sc := fmt.Sprintf("%s-%d", ac, sh)
				kind := "shelf"
				if in.Bins == 0 {
					kind = "bin" // shelves are the storage places
				}
				sid, err := insertLoc(ctx, tx, cid, locIn{WarehouseID: cfg.ID, ParentID: &aid, Kind: kind, Code: sc})
				if err != nil {
					return nil, err
				}
				n++
				for b := 1; b <= in.Bins; b++ {
					if _, err := insertLoc(ctx, tx, cid, locIn{WarehouseID: cfg.ID, ParentID: &sid, Kind: "bin", Code: fmt.Sprintf("%s-%02d", sc, b)}); err != nil {
						return nil, err
					}
					n++
				}
			}
		}
		return map[string]int{"created": n}, nil
	})
}

// PATCH /wh/locations/{id} {name, barcode, is_active}
func (s *Server) whUpdateLocation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     *string `json:"name"`
		Barcode  *string `json:"barcode"`
		IsActive *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if (in.Name != nil && len(*in.Name) > 120) || (in.Barcode != nil && len(*in.Barcode) > 80) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE wh_locations SET name = coalesce($3, name), barcode = coalesce($4, barcode), is_active = coalesce($5, is_active)
			WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.Barcode, in.IsActive)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
		}
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT id, code, name, barcode, is_active FROM wh_locations WHERE id = $1`, id)
	})
}

// GET /wh/putaway-rules
func (s *Server) whRules(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT p.id, p.warehouse_id, w.name AS warehouse, p.item_id, it.code AS item_code, it.name AS item_name,
			p.category_id, c.name AS category_name, p.location_id, l.code AS location_code, p.priority
			FROM wh_putaway_rules p JOIN warehouses w ON w.id = p.warehouse_id JOIN wh_locations l ON l.id = p.location_id
			LEFT JOIN items it ON it.id = p.item_id LEFT JOIN item_categories c ON c.id = p.category_id
			WHERE p.company_id = $1 ORDER BY w.name, p.priority, l.sort_key`, r.PathValue("cid"))
	})
}

// POST /wh/putaway-rules {warehouse_id, item_id | category_id, location_id, priority}
func (s *Server) whCreateRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WarehouseID string  `json:"warehouse_id"`
		ItemID      *string `json:"item_id"`
		CategoryID  *string `json:"category_id"`
		LocationID  string  `json:"location_id"`
		Priority    int     `json:"priority"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cfg, err := loadWhCfg(ctx, tx, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		if err := checkLoc(ctx, tx, cid, cfg.ID, &in.LocationID); err != nil {
			return nil, err
		}
		item, cat := nilBlank(in.ItemID), nilBlank(in.CategoryID)
		if item == nil && cat == nil {
			return nil, badRequest("bad_request")
		}
		if item != nil {
			if _, err := loadItem(ctx, tx, cid, *item); err != nil {
				return nil, err
			}
			cat = nil
		} else {
			var ok bool
			if !isUUID(*cat) {
				return nil, badRequest("bad_request")
			}
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM item_categories WHERE id = $1 AND company_id = $2)`, *cat, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("bad_request")
			}
		}
		if in.Priority == 0 {
			in.Priority = 10
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO wh_putaway_rules (company_id, warehouse_id, item_id, category_id, location_id, priority)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, cid, cfg.ID, item, cat, in.LocationID, in.Priority).Scan(&id); err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	})
}

// DELETE /wh/putaway-rules/{id}
func (s *Server) whDeleteRule(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM wh_putaway_rules WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// GET /wh/bin-stock?warehouse_id=&location_id=&item_id=&q=
func (s *Server) whBinStock(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wh, loc, item, txt := q.Get("warehouse_id"), q.Get("location_id"), q.Get("item_id"), strings.TrimSpace(q.Get("q"))
	if (wh != "" && !isUUID(wh)) || (loc != "" && !isUUID(loc)) || (item != "" && !isUUID(item)) || len(txt) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cid := r.PathValue("cid")
		rows, err := queryJSON(ctx, tx, `SELECT b.id, b.warehouse_id, w.name AS warehouse, b.location_id, l.code AS location_code, l.kind AS location_kind,
			l.sort_key, b.item_id, it.code AS item_code, it.name AS item_name, it.unit, b.lot_id, sl.lot_no, sl.expiry,
			trim_scale(b.quantity)::text AS quantity, b.in_date
			FROM wh_bin_stock b JOIN warehouses w ON w.id = b.warehouse_id JOIN wh_locations l ON l.id = b.location_id
			JOIN items it ON it.id = b.item_id LEFT JOIN stock_lots sl ON sl.id = b.lot_id
			WHERE b.company_id = $1 AND ($2 = '' OR b.warehouse_id = nullif($2, '')::uuid) AND ($3 = '' OR b.location_id = nullif($3, '')::uuid)
			AND ($4 = '' OR b.item_id = nullif($4, '')::uuid)
			AND ($5 = '' OR it.code ILIKE '%' || $5 || '%' OR it.name ILIKE '%' || $5 || '%' OR l.code ILIKE '%' || $5 || '%' OR coalesce(sl.lot_no, '') ILIKE '%' || $5 || '%')
			ORDER BY w.name, l.sort_key, it.code LIMIT 3000`, cid, wh, loc, item, txt)
		if err != nil {
			return nil, err
		}
		// what no bin holds, per item and warehouse (only in warehouses with bins)
		free, err := queryJSON(ctx, tx, `SELECT st.warehouse_id, w.name AS warehouse, st.item_id, it.code AS item_code, it.name AS item_name, it.unit,
			trim_scale(st.quantity - coalesce((SELECT sum(b.quantity) FROM wh_bin_stock b WHERE b.warehouse_id = st.warehouse_id AND b.item_id = st.item_id), 0))::text AS quantity
			FROM stock st JOIN warehouses w ON w.id = st.warehouse_id JOIN items it ON it.id = st.item_id
			WHERE w.company_id = $1 AND w.use_bins AND st.quantity > 0 AND ($2 = '' OR st.warehouse_id = nullif($2, '')::uuid)
			AND ($3 = '' OR st.item_id = nullif($3, '')::uuid)
			AND st.quantity > coalesce((SELECT sum(b.quantity) FROM wh_bin_stock b WHERE b.warehouse_id = st.warehouse_id AND b.item_id = st.item_id), 0)
			AND ($4 = '' OR it.code ILIKE '%' || $4 || '%' OR it.name ILIKE '%' || $4 || '%')
			ORDER BY w.name, it.code LIMIT 3000`, cid, wh, item, txt)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"bins": rows, "unbinned": free}, nil
	})
}

// ======================= packages =======================

func packageAdd(ctx context.Context, tx pgx.Tx, cid, wh, pkg, item string, lot *string, qty decimal.Decimal) error {
	if !isUUID(pkg) {
		return badRequest("invalid_package")
	}
	var st, pwh string
	err := tx.QueryRow(ctx, `SELECT status, warehouse_id::text FROM wh_packages WHERE id = $1 AND company_id = $2 FOR UPDATE`, pkg, cid).Scan(&st, &pwh)
	if errors.Is(err, pgx.ErrNoRows) {
		return badRequest("invalid_package")
	}
	if err != nil {
		return err
	}
	if st != "open" || pwh != wh {
		return &apiError{status: http.StatusConflict, code: "package_closed"}
	}
	tag, err := tx.Exec(ctx, `UPDATE wh_package_lines SET quantity = quantity + $4::numeric WHERE package_id = $1 AND item_id = $2
		AND lot_id IS NOT DISTINCT FROM $3::uuid`, pkg, item, lot, qty.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO wh_package_lines (company_id, package_id, item_id, lot_id, quantity) VALUES ($1, $2, $3, $4, $5::numeric)`,
			cid, pkg, item, lot, qty.String())
	}
	return err
}

const pkgCols = `p.id, p.number, 'PKG' || lpad(p.number::text, 6, '0') AS code, p.warehouse_id, w.name AS warehouse, p.kind, p.parent_id,
	(SELECT 'PKG' || lpad(pp.number::text, 6, '0') FROM wh_packages pp WHERE pp.id = p.parent_id) AS parent_code,
	p.sales_order_id, (SELECT coalesce(so.order_number, so.number) FROM sales_orders so WHERE so.id = p.sales_order_id) AS order_number,
	(SELECT c.name FROM sales_orders so JOIN customers c ON c.id = so.customer_id WHERE so.id = p.sales_order_id) AS customer_name,
	p.delivery_id, p.status, trim_scale(p.weight)::text AS weight, trim_scale(p.length_cm)::text AS length_cm, trim_scale(p.width_cm)::text AS width_cm,
	trim_scale(p.height_cm)::text AS height_cm, p.notes, p.created_at,
	(SELECT count(*) FROM wh_package_lines l WHERE l.package_id = p.id) AS lines,
	(SELECT count(*) FROM wh_packages c2 WHERE c2.parent_id = p.id) AS children`

// GET /wh/packages?status=open|all|<status>&order_id=
func (s *Server) whPackages(w http.ResponseWriter, r *http.Request) {
	st, so := r.URL.Query().Get("status"), r.URL.Query().Get("order_id")
	if st == "" {
		st = "all"
	}
	if (so != "" && !isUUID(so)) || len(st) > 20 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+pkgCols+` FROM wh_packages p JOIN warehouses w ON w.id = p.warehouse_id WHERE p.company_id = $1
			AND ($2 = 'all' OR ($2 = 'open' AND p.status IN ('open', 'sealed')) OR p.status = $2)
			AND ($3 = '' OR p.sales_order_id = nullif($3, '')::uuid) ORDER BY p.number DESC LIMIT 1000`, r.PathValue("cid"), st, so)
	})
}

func loadPackage(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	p, err := queryJSONOne(ctx, tx, `SELECT `+pkgCols+`, co.name_en AS company_name, co.name_ar AS company_name_ar,
		(SELECT c.address FROM sales_orders so JOIN customers c ON c.id = so.customer_id WHERE so.id = p.sales_order_id) AS customer_address
		FROM wh_packages p JOIN warehouses w ON w.id = p.warehouse_id JOIN companies co ON co.id = p.company_id WHERE p.id = $1 AND p.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.item_id, it.code AS item_code, it.name AS item_name, it.unit, l.lot_id, sl.lot_no,
		trim_scale(l.quantity)::text AS quantity FROM wh_package_lines l JOIN items it ON it.id = l.item_id LEFT JOIN stock_lots sl ON sl.id = l.lot_id
		WHERE l.package_id = $1 ORDER BY it.code`, id)
	if err != nil {
		return nil, err
	}
	kids, err := queryJSON(ctx, tx, `SELECT id, number, 'PKG' || lpad(number::text, 6, '0') AS code, kind, status FROM wh_packages WHERE parent_id = $1 ORDER BY number`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"package": p, "lines": lines, "children": kids}, nil
}

func (s *Server) whGetPackage(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadPackage(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

type pkgIn struct {
	WarehouseID  *string `json:"warehouse_id"`
	Kind         *string `json:"kind"`
	ParentID     *string `json:"parent_id"`
	SalesOrderID *string `json:"sales_order_id"`
	Weight       *string `json:"weight"`
	LengthCm     *string `json:"length_cm"`
	WidthCm      *string `json:"width_cm"`
	HeightCm     *string `json:"height_cm"`
	Notes        *string `json:"notes"`
	Status       *string `json:"status"`
}

func (in *pkgIn) nums() (map[string]*string, bool) {
	out := map[string]*string{}
	for k, p := range map[string]*string{"weight": in.Weight, "length_cm": in.LengthCm, "width_cm": in.WidthCm, "height_cm": in.HeightCm} {
		if p == nil {
			continue
		}
		v := strings.TrimSpace(*p)
		if v == "" {
			v = "0"
		}
		d, ok := money(v)
		if !ok || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(1000000)) {
			return nil, false
		}
		s := d.String()
		out[k] = &s
	}
	return out, true
}

func checkPkgRefs(ctx context.Context, tx pgx.Tx, cid, wh string, in *pkgIn) error {
	if p := nilBlank(in.ParentID); p != nil {
		var kind, pwh string
		if !isUUID(*p) {
			return badRequest("invalid_package")
		}
		err := tx.QueryRow(ctx, `SELECT kind, warehouse_id::text FROM wh_packages WHERE id = $1 AND company_id = $2`, *p, cid).Scan(&kind, &pwh)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (kind != "pallet" || pwh != wh)) {
			return badRequest("invalid_package")
		}
		if err != nil {
			return err
		}
		in.ParentID = p
	} else {
		in.ParentID = nil
	}
	if so := nilBlank(in.SalesOrderID); so != nil {
		var ok bool
		if !isUUID(*so) {
			return badRequest("invalid_order")
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sales_orders WHERE id = $1 AND company_id = $2)`, *so, cid).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return badRequest("invalid_order")
		}
		in.SalesOrderID = so
	} else {
		in.SalesOrderID = nil
	}
	return nil
}

// POST /wh/packages
func (s *Server) whCreatePackage(w http.ResponseWriter, r *http.Request) {
	var in pkgIn
	if !decodeBody(w, r, &in) {
		return
	}
	nums, ok := in.nums()
	kind := "box"
	if in.Kind != nil {
		kind = *in.Kind
	}
	if !ok || in.WarehouseID == nil || (kind != "box" && kind != "pallet") || (in.Notes != nil && len(*in.Notes) > 1000) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cfg, err := loadWhCfg(ctx, tx, cid, *in.WarehouseID)
		if err != nil {
			return nil, err
		}
		if err := checkPkgRefs(ctx, tx, cid, cfg.ID, &in); err != nil {
			return nil, err
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'wh_package')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		notes := ""
		if in.Notes != nil {
			notes = strings.TrimSpace(*in.Notes)
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO wh_packages (company_id, number, warehouse_id, kind, parent_id, sales_order_id, weight, length_cm,
			width_cm, height_cm, notes) VALUES ($1, $2, $3, $4, $5, $6, coalesce($7::numeric, 0), coalesce($8::numeric, 0), coalesce($9::numeric, 0),
			coalesce($10::numeric, 0), $11) RETURNING id::text`, cid, n, cfg.ID, kind, in.ParentID, in.SalesOrderID, nums["weight"], nums["length_cm"],
			nums["width_cm"], nums["height_cm"], notes).Scan(&id); err != nil {
			return nil, err
		}
		return loadPackage(ctx, tx, cid, id)
	})
}

// PATCH /wh/packages/{id} {weight, dims, notes, parent_id, status: sealed|open|cancelled}
func (s *Server) whUpdatePackage(w http.ResponseWriter, r *http.Request) {
	var in pkgIn
	if !decodeBody(w, r, &in) {
		return
	}
	nums, ok := in.nums()
	if !ok || (in.Status != nil && !contains([]string{"open", "sealed", "cancelled"}, *in.Status)) || (in.Notes != nil && len(*in.Notes) > 1000) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var st, wh string
		err := tx.QueryRow(ctx, `SELECT status, warehouse_id::text FROM wh_packages WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st, &wh)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st == "shipped" || st == "cancelled" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		parentSet := in.ParentID != nil
		soSet := in.SalesOrderID != nil
		if err := checkPkgRefs(ctx, tx, cid, wh, &in); err != nil {
			return nil, err
		}
		if parentSet && in.ParentID != nil && *in.ParentID == id {
			return nil, badRequest("invalid_package")
		}
		if _, err := tx.Exec(ctx, `UPDATE wh_packages SET weight = coalesce($3::numeric, weight), length_cm = coalesce($4::numeric, length_cm),
			width_cm = coalesce($5::numeric, width_cm), height_cm = coalesce($6::numeric, height_cm), notes = coalesce($7, notes),
			status = coalesce($8, status), parent_id = CASE WHEN $9 THEN $10::uuid ELSE parent_id END,
			sales_order_id = CASE WHEN $11 THEN $12::uuid ELSE sales_order_id END WHERE id = $1 AND company_id = $2`,
			id, cid, nums["weight"], nums["length_cm"], nums["width_cm"], nums["height_cm"], in.Notes, in.Status, parentSet, in.ParentID, soSet, in.SalesOrderID); err != nil {
			return nil, err
		}
		return loadPackage(ctx, tx, cid, id)
	})
}

// PUT /wh/packages/{id}/lines {lines: [{item_id, lot_id, quantity}]}: the content of an open package.
func (s *Server) whPackageLines(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines []struct {
			ItemID   string  `json:"item_id"`
			LotID    *string `json:"lot_id"`
			Quantity string  `json:"quantity"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Lines) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var st, wh string
		err := tx.QueryRow(ctx, `SELECT status, warehouse_id::text FROM wh_packages WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st, &wh)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st != "open" {
			return nil, &apiError{status: http.StatusConflict, code: "package_closed"}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM wh_package_lines WHERE package_id = $1`, id); err != nil {
			return nil, err
		}
		for _, l := range in.Lines {
			q, ok := money(l.Quantity)
			if !ok || q.IsNegative() {
				return nil, badRequest("invalid_amount")
			}
			if q.IsZero() {
				continue
			}
			it, err := loadItem(ctx, tx, cid, l.ItemID)
			if err != nil {
				return nil, err
			}
			lot := nilBlank(l.LotID)
			if err := checkLot(ctx, tx, cid, it.ID, lot); err != nil {
				return nil, err
			}
			if err := packageAdd(ctx, tx, cid, wh, id, it.ID, lot, q); err != nil {
				return nil, err
			}
		}
		return loadPackage(ctx, tx, cid, id)
	})
}

// ======================= batch / wave picking =======================

// GET /wh/batches?status=open|all
func (s *Server) whBatches(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	if st == "" {
		st = "open"
	}
	if len(st) > 20 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT b.id, b.number, b.warehouse_id, w.name AS warehouse, b.kind, b.status, b.assigned_to, u.full_name AS assigned_name,
			b.notes, b.created_at, b.done_at, (SELECT count(*) FROM wh_ops o WHERE o.batch_id = b.id) AS ops,
			(SELECT count(*) FROM wh_ops o WHERE o.batch_id = b.id AND o.status = 'ready') AS ops_ready
			FROM wh_batches b JOIN warehouses w ON w.id = b.warehouse_id LEFT JOIN users u ON u.id = b.assigned_to
			WHERE b.company_id = $1 AND ($2 = 'all' OR b.status = $2) ORDER BY b.number DESC LIMIT 500`, r.PathValue("cid"), st)
	})
}

// POST /wh/batches {warehouse_id, kind: batch|wave, op_ids: [...] | (wave) up_to_date, assigned_to, notes}
func (s *Server) whCreateBatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WarehouseID string   `json:"warehouse_id"`
		Kind        string   `json:"kind"`
		OpIDs       []string `json:"op_ids"`
		UpToDate    string   `json:"up_to_date"`
		AssignedTo  *string  `json:"assigned_to"`
		Notes       string   `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Kind == "" {
		in.Kind = "batch"
	}
	if (in.Kind != "batch" && in.Kind != "wave") || len(in.OpIDs) > 300 || (in.UpToDate != "" && !validDate(in.UpToDate)) || len(in.Notes) > 1000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cfg, err := loadWhCfg(ctx, tx, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		as := nilBlank(in.AssignedTo)
		if as != nil {
			var ok bool
			if !isUUID(*as) {
				return nil, badRequest("invalid_user")
			}
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND is_active)`, *as).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_user")
			}
		}
		ids := []string{}
		if in.Kind == "wave" && len(in.OpIDs) == 0 {
			d := in.UpToDate
			if d == "" {
				d = today()
			}
			rows, err := tx.Query(ctx, `SELECT id::text FROM wh_ops WHERE company_id = $1 AND warehouse_id = $2 AND kind = 'pick' AND status = 'ready'
				AND batch_id IS NULL AND scheduled_date <= $3::date ORDER BY scheduled_date, number`, cid, cfg.ID, d)
			if err != nil {
				return nil, err
			}
			if ids, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
				return nil, err
			}
		} else {
			for _, id := range in.OpIDs {
				if !isUUID(id) {
					return nil, badRequest("invalid_op")
				}
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return nil, badRequest("nothing_to_pick")
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'wh_batch')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var bid string
		if err := tx.QueryRow(ctx, `INSERT INTO wh_batches (company_id, number, warehouse_id, kind, assigned_to, notes) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id::text`, cid, n, cfg.ID, in.Kind, as, strings.TrimSpace(in.Notes)).Scan(&bid); err != nil {
			return nil, err
		}
		for _, id := range ids {
			tag, err := tx.Exec(ctx, `UPDATE wh_ops SET batch_id = $3 WHERE id = $1 AND company_id = $2 AND warehouse_id = $4 AND kind = 'pick'
				AND status = 'ready' AND batch_id IS NULL`, id, cid, bid, cfg.ID)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, &apiError{status: http.StatusConflict, code: "op_not_pickable"}
			}
		}
		return loadBatch(ctx, tx, cid, bid)
	})
}

func loadBatch(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	b, err := queryJSONOne(ctx, tx, `SELECT b.id, b.number, b.warehouse_id, w.name AS warehouse, b.kind, b.status, b.assigned_to, u.full_name AS assigned_name,
		b.notes, b.created_at, b.done_at FROM wh_batches b JOIN warehouses w ON w.id = b.warehouse_id LEFT JOIN users u ON u.id = b.assigned_to
		WHERE b.id = $1 AND b.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	ops, err := queryJSON(ctx, tx, `SELECT `+whOpCols+whOpFrom+` WHERE o.batch_id = $1 ORDER BY o.number`, id)
	if err != nil {
		return nil, err
	}
	// one pick list for the whole batch, in walking order (bin path)
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.op_id, o.number AS op_number, o.origin, l.item_id, it.code AS item_code, it.name AS item_name, it.unit,
		it.barcode, l.lot_id, sl.lot_no, trim_scale(l.quantity)::text AS quantity, trim_scale(l.done_qty)::text AS done_qty, o.status AS op_status,
		l.from_location_id, fl.code AS from_code, l.to_location_id, tl.code AS to_code
		FROM wh_op_lines l JOIN wh_ops o ON o.id = l.op_id JOIN items it ON it.id = l.item_id LEFT JOIN stock_lots sl ON sl.id = l.lot_id
		LEFT JOIN wh_locations fl ON fl.id = l.from_location_id LEFT JOIN wh_locations tl ON tl.id = l.to_location_id
		WHERE o.batch_id = $1 ORDER BY coalesce(fl.sort_key, '~'), it.code, o.number`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"batch": b, "ops": ops, "lines": lines}, nil
}

func (s *Server) whGetBatch(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadBatch(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// POST /wh/batches/{id}/done {lines: [{line_id, done_qty}]} (none = everything): every pick of the batch is done.
func (s *Server) whBatchDone(w http.ResponseWriter, r *http.Request) {
	var in opDoneIn
	if !decodeBody(w, r, &in) {
		return
	}
	if (in.Date != "" && !validDate(in.Date)) || len(in.Lines) > 3000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var st string
		err := tx.QueryRow(ctx, `SELECT status FROM wh_batches WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st != "open" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		rows, err := tx.Query(ctx, `SELECT o.id::text, l.id::text FROM wh_ops o JOIN wh_op_lines l ON l.op_id = o.id
			WHERE o.batch_id = $1 AND o.status = 'ready' ORDER BY o.number, l.line_no`, id)
		if err != nil {
			return nil, err
		}
		type ol struct{ op, line string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ol, error) {
			var x ol
			err := row.Scan(&x.op, &x.line)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		given := map[string]opDoneLine{}
		for _, l := range in.Lines {
			given[l.LineID] = l
		}
		perOp := map[string][]opDoneLine{}
		order := []string{}
		for _, x := range list {
			if _, ok := perOp[x.op]; !ok {
				order = append(order, x.op)
				perOp[x.op] = []opDoneLine{}
			}
			if g, ok := given[x.line]; ok {
				perOp[x.op] = append(perOp[x.op], g)
				delete(given, x.line)
			}
		}
		if len(given) > 0 {
			return nil, badRequest("invalid_line")
		}
		for _, op := range order {
			lines := perOp[op]
			if len(in.Lines) > 0 && len(lines) == 0 {
				continue // nothing of this pick was given: left for later
			}
			if _, err := opDone(ctx, tx, r, cid, op, opDoneIn{Date: in.Date, Notes: in.Notes, Lines: lines}); err != nil {
				return nil, err
			}
		}
		// backorders stay in the batch? no: they are new picks, free to batch again
		var left bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wh_ops WHERE batch_id = $1 AND status = 'ready')`, id).Scan(&left); err != nil {
			return nil, err
		}
		if !left {
			if _, err := tx.Exec(ctx, `UPDATE wh_batches SET status = 'done', done_at = now() WHERE id = $1`, id); err != nil {
				return nil, err
			}
		}
		return loadBatch(ctx, tx, cid, id)
	})
}

// POST /wh/batches/{id}/cancel: the picks are released (not cancelled).
func (s *Server) whBatchCancel(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE wh_batches SET status = 'cancelled' WHERE id = $1 AND company_id = $2 AND status = 'open'`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := tx.Exec(ctx, `UPDATE wh_ops SET batch_id = NULL WHERE batch_id = $1 AND status <> 'done'`, id); err != nil {
			return nil, err
		}
		return loadBatch(ctx, tx, cid, id)
	})
}

// ======================= scanner lookup =======================

// GET /wh/scan?code=: what a scanned code is (location, package, item / unit barcode, lot or serial, operation).
func (s *Server) whScan(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" || len(code) > 120 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		try := func(typ, sql string, args ...any) (map[string]any, error) {
			js, err := queryJSONOne(ctx, tx, sql, args...)
			if err != nil {
				var ae *apiError
				if errors.As(err, &ae) && ae.status == http.StatusNotFound {
					return nil, nil
				}
				return nil, err
			}
			return map[string]any{"type": typ, "data": js}, nil
		}
		up := strings.ToUpper(code)
		if strings.HasPrefix(up, "PKG") {
			if res, err := try("package", `SELECT id, number, status, warehouse_id FROM wh_packages WHERE company_id = $1 AND number::text = ltrim(substr($2, 4), '0')`, cid, up); res != nil || err != nil {
				return res, err
			}
		}
		if strings.HasPrefix(up, "OP") {
			if res, err := try("op", `SELECT id, number, kind, status, warehouse_id FROM wh_ops WHERE company_id = $1 AND number::text = ltrim(substr($2, 3), '0')`, cid, up); res != nil || err != nil {
				return res, err
			}
		}
		if res, err := try("location", `SELECT l.id, l.code, l.kind, l.warehouse_id, w.name AS warehouse FROM wh_locations l JOIN warehouses w ON w.id = l.warehouse_id
			WHERE l.company_id = $1 AND (l.barcode = $2 OR upper(l.code) = $3) ORDER BY (l.barcode = $2) DESC LIMIT 1`, cid, code, up); res != nil || err != nil {
			return res, err
		}
		if res, err := try("item", `SELECT it.id, it.code, it.name, it.unit, it.barcode, it.tracking, 1::numeric AS factor, NULL::text AS unit_name
			FROM items it WHERE it.company_id = $1 AND it.is_active AND (it.barcode = $2 OR upper(it.code) = $3) LIMIT 1`, cid, code, up); res != nil || err != nil {
			return res, err
		}
		if res, err := try("item", `SELECT it.id, it.code, it.name, it.unit, it.barcode, it.tracking, trim_scale(u.factor)::text AS factor, u.name AS unit_name
			FROM item_units u JOIN items it ON it.id = u.item_id WHERE u.company_id = $1 AND u.barcode = $2 LIMIT 1`, cid, code); res != nil || err != nil {
			return res, err
		}
		if res, err := try("lot", `SELECT sl.id, sl.lot_no, sl.expiry, sl.item_id, it.code AS item_code, it.name AS item_name, it.unit
			FROM stock_lots sl JOIN items it ON it.id = sl.item_id WHERE sl.company_id = $1 AND lower(sl.lot_no) = lower($2) LIMIT 1`, cid, code); res != nil || err != nil {
			return res, err
		}
		return map[string]any{"type": "unknown", "code": code}, nil
	})
}
