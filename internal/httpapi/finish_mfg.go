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

// Manufacturing: a bill of materials says which components (and how much labour / overhead, which routing,
// which by-products) make one batch of a finished item. BoMs are versioned: versions of one BoM form a family,
// and only one version of a family is active. A BoM is "normal" (made here, with the routing's work orders),
// "phantom" (a kit: used as a component it is replaced by its own components) or "subcontract" (a supplier makes
// it from components we send). A manufacturing order consumes the components from stock at their cost (FIFO or
// average, per item) and brings the finished item in at the rolled-up cost: components + work center time +
// labour + overhead + subcontracting, less the by-products' share (scrapped units carry no value of their own:
// their cost stays in the good ones). An item with a standard cost enters stock at standard; the difference is
// the production variance. Scrap of any stock item is booked to the scrap account.

const bomCols = `b.id, b.item_id, it.code AS item_code, it.name AS item_name, it.unit, b.name, trim_scale(b.output_qty)::text AS output_qty,
	b.labour_cost::text AS labour_cost, b.overhead_cost::text AS overhead_cost, b.notes, b.is_active,
	(SELECT count(*) FROM bom_lines l WHERE l.bom_id = b.id) AS components,
	b.code, b.version, b.family_id, b.bom_type, b.routing_id, (SELECT r.name FROM mfg_routings r WHERE r.id = b.routing_id) AS routing_name,
	b.subcontractor_id, (SELECT s.name FROM suppliers s WHERE s.id = b.subcontractor_id) AS subcontractor_name,
	b.subcontract_price::text AS subcontract_price, b.lead_days, b.version_note, trim_scale(it.standard_cost)::text AS standard_cost,
	(SELECT count(*) FROM boms v WHERE v.family_id = b.family_id) AS versions`

type bomLineIn struct {
	ItemID   string `json:"item_id"`
	Quantity string `json:"quantity"`
}

type bomByIn struct {
	ItemID    string `json:"item_id"`
	Quantity  string `json:"quantity"`
	CostShare string `json:"cost_share"`
}

func (s *Server) listBOMs(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+bomCols+` FROM boms b JOIN items it ON it.id = b.item_id WHERE b.company_id = $1
			ORDER BY it.name, b.name, b.version`, r.PathValue("cid"))
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
		coalesce((SELECT CASE WHEN sum(st.quantity) > 0 THEN sum(st.value) / sum(st.quantity) END FROM stock st WHERE st.item_id = l.item_id), 0)::numeric(20, 6)::text AS unit_cost,
		(SELECT x.bom_type FROM boms x WHERE x.item_id = l.item_id AND x.company_id = l.company_id AND x.is_active ORDER BY x.bom_type = 'phantom' DESC, x.created_at LIMIT 1) AS sub_bom
		FROM bom_lines l JOIN items it ON it.id = l.item_id WHERE l.bom_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	bys, err := queryJSON(ctx, tx, `SELECT y.id, y.item_id, it.code AS item_code, it.name AS item_name, it.unit, trim_scale(y.quantity)::text AS quantity,
		trim_scale(y.cost_share)::text AS cost_share FROM bom_byproducts y JOIN items it ON it.id = y.item_id WHERE y.bom_id = $1 ORDER BY it.code`, id)
	if err != nil {
		return nil, err
	}
	versions, err := queryJSON(ctx, tx, `SELECT v.id, v.version, v.name, v.is_active, v.version_note, v.created_at FROM boms v
		WHERE v.family_id = (SELECT family_id FROM boms WHERE id = $1) ORDER BY v.version`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"bom": b, "lines": lines, "byproducts": bys, "versions": versions}, nil
}

func (s *Server) getBOM(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadBOM(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// bomReaches: is target made (directly or deeper) from item through any BoM of the company? (cycle guard)
func bomReaches(ctx context.Context, tx pgx.Tx, cid, item, target string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE r(item, depth) AS (
		SELECT $1::uuid, 0
		UNION
		SELECT l.item_id, r.depth + 1 FROM r JOIN boms b ON b.item_id = r.item AND b.company_id = $3 JOIN bom_lines l ON l.bom_id = b.id WHERE r.depth < 30)
		SELECT EXISTS (SELECT 1 FROM r WHERE item = $2::uuid)`, item, target, cid).Scan(&ok)
	return ok, err
}

// POST /companies/{cid}/mfg/boms · PUT /companies/{cid}/mfg/boms/{id}
func (s *Server) saveBOM(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ItemID           string      `json:"item_id"`
		Name             string      `json:"name"`
		Code             string      `json:"code"`
		OutputQty        string      `json:"output_qty"`
		LabourCost       string      `json:"labour_cost"`
		OverheadCost     string      `json:"overhead_cost"`
		Notes            string      `json:"notes"`
		IsActive         *bool       `json:"is_active"`
		BomType          string      `json:"bom_type"`
		RoutingID        string      `json:"routing_id"`
		SubcontractorID  string      `json:"subcontractor_id"`
		SubcontractPrice string      `json:"subcontract_price"`
		LeadDays         int         `json:"lead_days"`
		VersionNote      string      `json:"version_note"`
		Lines            []bomLineIn `json:"lines"`
		Byproducts       []bomByIn   `json:"byproducts"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	out, ok1 := money(defaultStr(in.OutputQty, "1"))
	lab, ok2 := money(defaultStr(in.LabourCost, "0"))
	ovh, ok3 := money(defaultStr(in.OverheadCost, "0"))
	sub, ok4 := money(defaultStr(in.SubcontractPrice, "0"))
	typ := defaultStr(in.BomType, "normal")
	ok := ok1 && ok2 && ok3 && ok4 && out.IsPositive() && !lab.IsNegative() && !ovh.IsNegative() && !sub.IsNegative() && isUUID(in.ItemID) &&
		len(in.Lines) > 0 && len(in.Lines) <= 200 && len(in.Byproducts) <= 20 && (id == "" || isUUID(id)) &&
		(typ == "normal" || typ == "phantom" || typ == "subcontract") && in.LeadDays >= 0 && in.LeadDays <= 365 &&
		(in.RoutingID == "" || isUUID(in.RoutingID)) && (in.SubcontractorID == "" || isUUID(in.SubcontractorID)) &&
		(typ != "subcontract" || in.SubcontractorID != "") && len(in.Code) <= 40
	for _, l := range in.Lines {
		q, okq := money(l.Quantity)
		ok = ok && okq && q.IsPositive() && isUUID(l.ItemID) && l.ItemID != in.ItemID
	}
	shares := decimal.Zero
	for _, y := range in.Byproducts {
		q, okq := money(y.Quantity)
		sh, oks := money(defaultStr(y.CostShare, "0"))
		ok = ok && okq && oks && q.IsPositive() && !sh.IsNegative() && isUUID(y.ItemID) && y.ItemID != in.ItemID
		shares = shares.Add(sh)
	}
	if !ok || shares.GreaterThanOrEqual(decimal.NewFromInt(100)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if typ != "normal" {
		in.RoutingID = ""
	}
	if typ != "subcontract" {
		in.SubcontractorID, sub = "", decimal.Zero
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
		if in.RoutingID != "" {
			var okR bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mfg_routings WHERE id = $1 AND company_id = $2)`, in.RoutingID, cid).Scan(&okR); err != nil {
				return nil, err
			}
			if !okR {
				return nil, badRequest("invalid_routing")
			}
		}
		if in.SubcontractorID != "" {
			if _, err := lockSupplier(ctx, tx, cid, in.SubcontractorID); err != nil {
				return nil, err
			}
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = it.Name
		}
		var family string
		if id == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO boms (company_id, item_id, name, output_qty, labour_cost, overhead_cost, notes, is_active, code, bom_type,
				routing_id, subcontractor_id, subcontract_price, lead_days, version_note)
				VALUES ($1, $2, $3, $4::numeric, $5::numeric, $6::numeric, $7, false, $8, $9, nullif($10, '')::uuid, nullif($11, '')::uuid, $12::numeric, $13, $14)
				RETURNING id::text`, cid, in.ItemID, name, out.String(), lab.String(), ovh.String(), strings.TrimSpace(in.Notes), strings.TrimSpace(in.Code), typ,
				in.RoutingID, in.SubcontractorID, sub.String(), in.LeadDays, strings.TrimSpace(in.VersionNote)).Scan(&id); err != nil {
				return nil, err
			}
			family = id
			if _, err := tx.Exec(ctx, `UPDATE boms SET family_id = id WHERE id = $1`, id); err != nil {
				return nil, err
			}
		} else {
			err := tx.QueryRow(ctx, `UPDATE boms SET item_id = $3, name = $4, output_qty = $5::numeric, labour_cost = $6::numeric, overhead_cost = $7::numeric,
				notes = $8, code = $9, bom_type = $10, routing_id = nullif($11, '')::uuid, subcontractor_id = nullif($12, '')::uuid, subcontract_price = $13::numeric,
				lead_days = $14, version_note = $15 WHERE id = $1 AND company_id = $2 RETURNING family_id::text`, id, cid, in.ItemID, name, out.String(), lab.String(), ovh.String(),
				strings.TrimSpace(in.Notes), strings.TrimSpace(in.Code), typ, in.RoutingID, in.SubcontractorID, sub.String(), in.LeadDays, strings.TrimSpace(in.VersionNote)).Scan(&family)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM bom_lines WHERE bom_id = $1`, id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM bom_byproducts WHERE bom_id = $1`, id); err != nil {
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
			if loop, err := bomReaches(ctx, tx, cid, l.ItemID, in.ItemID); err != nil {
				return nil, err
			} else if loop {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "bom_cycle", detail: c.Name}
			}
			q, _ := money(l.Quantity)
			if _, err := tx.Exec(ctx, `INSERT INTO bom_lines (company_id, bom_id, line_no, item_id, quantity) VALUES ($1, $2, $3, $4, $5::numeric)`,
				cid, id, i+1, l.ItemID, q.String()); err != nil {
				return nil, err
			}
		}
		for _, y := range in.Byproducts {
			c, err := loadItem(ctx, tx, cid, y.ItemID)
			if err != nil {
				return nil, err
			}
			if c.Kind != "stock" || c.Inventory == nil {
				return nil, badRequest("not_stock_item")
			}
			q, _ := money(y.Quantity)
			sh, _ := money(defaultStr(y.CostShare, "0"))
			if _, err := tx.Exec(ctx, `INSERT INTO bom_byproducts (company_id, bom_id, item_id, quantity, cost_share) VALUES ($1, $2, $3, $4::numeric, $5::numeric)`,
				cid, id, y.ItemID, q.String(), sh.String()); err != nil {
				return nil, err
			}
		}
		if err := bomSetActive(ctx, tx, id, family, active); err != nil {
			return nil, err
		}
		return loadBOM(ctx, tx, cid, id)
	})
}

// bomSetActive: activating a version deactivates the other versions of its family.
func bomSetActive(ctx context.Context, tx pgx.Tx, id, family string, active bool) error {
	if active {
		if _, err := tx.Exec(ctx, `UPDATE boms SET is_active = false WHERE family_id = $1 AND id <> $2 AND is_active`, family, id); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE boms SET is_active = $2 WHERE id = $1`, id, active)
	return err
}

// ---------- manufacturing orders ----------

const mfgCols = `o.id, o.number, o.bom_id, b.name AS bom_name, b.version AS bom_version, o.item_id, it.code AS item_code, it.name AS item_name, it.unit,
	trim_scale(o.quantity)::text AS quantity, o.warehouse_id, w.name AS warehouse, o.order_date, o.status, o.components_value::text AS components_value,
	o.labour::text AS labour, o.overhead::text AS overhead, trim_scale(o.scrap_qty)::text AS scrap_qty, trim_scale(o.unit_cost)::text AS unit_cost,
	o.journal_entry_id, o.notes, o.created_at, o.done_at, o.bom_type, o.routing_id, o.deadline, o.origin, o.backorder_of,
	(SELECT x.number FROM mfg_orders x WHERE x.id = o.backorder_of) AS backorder_of_number, o.parent_id, o.sales_order_id,
	o.ops_cost::text AS ops_cost, o.service_cost::text AS service_cost, o.byproducts_value::text AS byproducts_value,
	o.expected_cost::text AS expected_cost, trim_scale(o.standard_cost)::text AS standard_cost, o.variance::text AS variance,
	o.subcontract_wh_id, o.components_sent, o.service_bill_id, o.confirmed_at,
	(o.deadline IS NOT NULL AND o.deadline < current_date AND o.status IN ('draft', 'confirmed', 'progress')) AS late,
	CASE WHEN o.status IN ('confirmed', 'progress') THEN CASE
		WHEN NOT EXISTS (SELECT 1 FROM mfg_order_lines l WHERE l.order_id = o.id AND l.reserved_qty + l.sent_qty < l.planned_qty) THEN 'ready'
		WHEN EXISTS (SELECT 1 FROM mfg_order_lines l WHERE l.order_id = o.id AND l.reserved_qty > 0) THEN 'partial' ELSE 'waiting' END END AS availability,
	(SELECT count(*) FROM mfg_workorders x WHERE x.order_id = o.id AND x.status <> 'cancelled') AS workorders,
	(SELECT count(*) FROM mfg_workorders x WHERE x.order_id = o.id AND x.status = 'done') AS workorders_done,
	(SELECT count(*) FROM mfg_quality_checks q WHERE q.order_id = o.id AND q.status = 'todo') AS checks_todo`

const mfgFrom = ` FROM mfg_orders o JOIN boms b ON b.id = o.bom_id JOIN items it ON it.id = o.item_id JOIN warehouses w ON w.id = o.warehouse_id`

func loadMfgOrder(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	o, err := queryJSONOne(ctx, tx, `SELECT `+mfgCols+mfgFrom+` WHERE o.id = $1 AND o.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.item_id, it.code AS item_code, it.name AS item_name, it.unit, it.tracking, trim_scale(l.planned_qty)::text AS planned_qty,
		trim_scale(l.consumed_qty)::text AS consumed_qty, l.value::text AS value, trim_scale(l.reserved_qty)::text AS reserved_qty, trim_scale(l.sent_qty)::text AS sent_qty,
		coalesce((SELECT trim_scale(st.quantity)::text FROM stock st WHERE st.item_id = l.item_id AND st.warehouse_id = coalesce(o.subcontract_wh_id, o.warehouse_id)), '0') AS in_stock,
		(SELECT coalesce(json_agg(json_build_object('code', x.code, 'quantity', trim_scale(x.quantity)::text, 'lot_no', x.lot_no)), '[]'::json) FROM (
			SELECT loc.code, bs.quantity, sl.lot_no FROM wh_bin_stock bs JOIN wh_locations loc ON loc.id = bs.location_id JOIN warehouses ww ON ww.id = bs.warehouse_id
			LEFT JOIN stock_lots sl ON sl.id = bs.lot_id
			WHERE bs.warehouse_id = o.warehouse_id AND bs.item_id = l.item_id AND bs.quantity > 0
			ORDER BY CASE WHEN ww.removal = 'fefo' THEN sl.expiry END NULLS LAST, CASE WHEN ww.removal = 'closest' THEN loc.sort_key END, bs.in_date, bs.id LIMIT 5) x) AS bins
		FROM mfg_order_lines l JOIN items it ON it.id = l.item_id JOIN mfg_orders o ON o.id = l.order_id WHERE l.order_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	bys, err := queryJSON(ctx, tx, `SELECT y.id, y.item_id, it.code AS item_code, it.name AS item_name, it.unit, trim_scale(y.planned_qty)::text AS planned_qty,
		trim_scale(y.produced_qty)::text AS produced_qty, trim_scale(y.cost_share)::text AS cost_share, y.value::text AS value
		FROM mfg_order_byproducts y JOIN items it ON it.id = y.item_id WHERE y.order_id = $1 ORDER BY it.code`, id)
	if err != nil {
		return nil, err
	}
	wos, err := queryJSON(ctx, tx, `SELECT `+woCols+woFrom+` WHERE x.order_id = $1 ORDER BY x.seq`, id)
	if err != nil {
		return nil, err
	}
	checks, err := queryJSON(ctx, tx, `SELECT `+qcCols+qcFrom+` WHERE q.order_id = $1 ORDER BY q.created_at`, id)
	if err != nil {
		return nil, err
	}
	related, err := queryJSON(ctx, tx, `SELECT x.id, x.number, x.status, trim_scale(x.quantity)::text AS quantity,
		CASE WHEN x.backorder_of = $1 THEN 'backorder' ELSE 'child' END AS relation
		FROM mfg_orders x WHERE x.backorder_of = $1 OR x.parent_id = $1 ORDER BY x.number`, id)
	if err != nil {
		return nil, err
	}
	scraps, err := queryJSON(ctx, tx, `SELECT `+stockDocCols+stockDocFrom+` WHERE d.mfg_order_id = $1 ORDER BY d.number`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"order": o, "lines": lines, "byproducts": bys, "workorders": wos, "checks": checks, "related": related, "scraps": scraps}, nil
}

func (s *Server) listMfgOrders(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+mfgCols+mfgFrom+` WHERE o.company_id = $1
			AND ($2 = '' OR ($2 = 'open' AND o.status IN ('draft', 'confirmed', 'progress')) OR o.status = $2)
			ORDER BY o.order_date DESC, o.number DESC LIMIT 1000`, r.PathValue("cid"), st)
	})
}

func (s *Server) getMfgOrder(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadMfgOrder(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

type mfgCreateIn struct {
	BomID        string `json:"bom_id"`
	Quantity     string `json:"quantity"`
	WarehouseID  string `json:"warehouse_id"`
	Date         string `json:"date"`
	Deadline     string `json:"deadline"`
	Notes        string `json:"notes"`
	Origin       string `json:"origin"`
	SalesOrderID string `json:"sales_order_id"`
	Confirm      bool   `json:"confirm"`
	parentID     string
}

type compQty struct {
	item string
	qty  decimal.Decimal
}

// bomExplode: the components of factor batches of a BoM; a component that has an active phantom BoM (a kit) is
// replaced by its own components. Same items are merged, first appearance order kept.
func bomExplode(ctx context.Context, tx pgx.Tx, cid, bomID string, factor decimal.Decimal, depth int, acc *[]compQty) error {
	if depth > 10 {
		return &apiError{status: http.StatusUnprocessableEntity, code: "bom_cycle"}
	}
	rows, err := tx.Query(ctx, `SELECT l.item_id::text, l.quantity::text,
		(SELECT p.id::text FROM boms p WHERE p.item_id = l.item_id AND p.company_id = $2 AND p.is_active AND p.bom_type = 'phantom' ORDER BY p.created_at LIMIT 1),
		(SELECT p.output_qty::text FROM boms p WHERE p.item_id = l.item_id AND p.company_id = $2 AND p.is_active AND p.bom_type = 'phantom' ORDER BY p.created_at LIMIT 1)
		FROM bom_lines l WHERE l.bom_id = $1 ORDER BY l.line_no`, bomID, cid)
	if err != nil {
		return err
	}
	type ln struct {
		item, q   string
		ph, phOut *string
	}
	ls, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ln, error) {
		var x ln
		err := r.Scan(&x.item, &x.q, &x.ph, &x.phOut)
		return x, err
	})
	if err != nil {
		return err
	}
	for _, l := range ls {
		q, _ := decimal.NewFromString(l.q)
		need := q.Mul(factor)
		if l.ph != nil {
			po, _ := decimal.NewFromString(*l.phOut)
			if err := bomExplode(ctx, tx, cid, *l.ph, need.Div(po), depth+1, acc); err != nil {
				return err
			}
			continue
		}
		found := false
		for i := range *acc {
			if (*acc)[i].item == l.item {
				(*acc)[i].qty = (*acc)[i].qty.Add(need)
				found = true
			}
		}
		if !found {
			*acc = append(*acc, compQty{l.item, need})
		}
	}
	return nil
}

// mfgCreate makes a draft manufacturing order (components exploded through phantom BoMs, by-products planned).
func mfgCreate(ctx context.Context, tx pgx.Tx, cid string, in mfgCreateIn) (string, error) {
	qty, ok := money(in.Quantity)
	if !isUUID(in.BomID) || !ok || !qty.IsPositive() || (in.Date != "" && !validDate(in.Date)) || (in.Deadline != "" && !validDate(in.Deadline)) ||
		(in.WarehouseID != "" && !isUUID(in.WarehouseID)) || (in.SalesOrderID != "" && !isUUID(in.SalesOrderID)) {
		return "", badRequest("bad_request")
	}
	if in.Date == "" {
		in.Date = today()
	}
	var item, outS, labS, ovhS, typ string
	var routing *string
	var active bool
	err := tx.QueryRow(ctx, `SELECT item_id::text, output_qty::text, labour_cost::text, overhead_cost::text, is_active, bom_type, routing_id::text
		FROM boms WHERE id = $1 AND company_id = $2`, in.BomID, cid).Scan(&item, &outS, &labS, &ovhS, &active, &typ, &routing)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return "", badRequest("invalid_bom")
	}
	if err != nil {
		return "", err
	}
	if typ == "phantom" {
		return "", badRequest("phantom_bom")
	}
	wh, err := checkWarehouse(ctx, tx, cid, in.WarehouseID)
	if err != nil {
		return "", err
	}
	if in.SalesOrderID != "" {
		var okS bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sales_orders WHERE id = $1 AND company_id = $2)`, in.SalesOrderID, cid).Scan(&okS); err != nil {
			return "", err
		}
		if !okS {
			return "", badRequest("invalid_order")
		}
	}
	out, _ := decimal.NewFromString(outS)
	lab, _ := decimal.NewFromString(labS)
	ovh, _ := decimal.NewFromString(ovhS)
	batches := qty.Div(out)
	unitCost, err := bomCost(ctx, tx, cid, in.BomID, 0)
	if err != nil {
		return "", err
	}
	var n int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'mfg_order')`, cid).Scan(&n); err != nil {
		return "", err
	}
	if typ != "normal" {
		routing = nil
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO mfg_orders (company_id, number, bom_id, item_id, quantity, warehouse_id, order_date, labour, overhead, notes,
		bom_type, routing_id, deadline, origin, sales_order_id, parent_id, expected_cost)
		VALUES ($1, $2, $3, $4, $5::numeric, $6, $7::date, $8::numeric, $9::numeric, $10, $11, $12, nullif($13, '')::date, $14, nullif($15, '')::uuid,
		nullif($16, '')::uuid, $17::numeric) RETURNING id::text`, cid, n, in.BomID, item, qty.String(), wh, in.Date,
		lab.Mul(batches).Round(4).String(), ovh.Mul(batches).Round(4).String(), strings.TrimSpace(in.Notes), typ, routing, in.Deadline,
		strings.TrimSpace(in.Origin), in.SalesOrderID, in.parentID, unitCost.Mul(batches).Round(4).String()).Scan(&id); err != nil {
		return "", err
	}
	comps := []compQty{}
	if err := bomExplode(ctx, tx, cid, in.BomID, batches, 0, &comps); err != nil {
		return "", err
	}
	for i, c := range comps {
		q := c.qty.Round(4)
		if _, err := tx.Exec(ctx, `INSERT INTO mfg_order_lines (company_id, order_id, line_no, item_id, planned_qty, consumed_qty)
			VALUES ($1, $2, $3, $4, $5::numeric, $5::numeric)`, cid, id, i+1, c.item, q.String()); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mfg_order_byproducts (company_id, order_id, item_id, planned_qty, produced_qty, cost_share)
		SELECT $1, $2, y.item_id, round(y.quantity * $3::numeric, 4), round(y.quantity * $3::numeric, 4), y.cost_share FROM bom_byproducts y WHERE y.bom_id = $4`,
		cid, id, batches.String(), in.BomID); err != nil {
		return "", err
	}
	if in.Confirm {
		if err := mfgConfirm(ctx, tx, cid, id); err != nil {
			return "", err
		}
	}
	return id, nil
}

// POST /companies/{cid}/mfg/orders {bom_id, quantity, warehouse_id, date, deadline, notes, origin, sales_order_id, confirm}
func (s *Server) createMfgOrder(w http.ResponseWriter, r *http.Request) {
	var in mfgCreateIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		id, err := mfgCreate(ctx, tx, cid, in)
		if err != nil {
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

// mfgVarianceAccount: production cost variance (actual vs standard), created on first use.
func mfgVarianceAccount(ctx context.Context, tx pgx.Tx, cid string) (string, error) {
	var id *string
	if err := tx.QueryRow(ctx, `SELECT mfg_variance_account_id::text FROM companies WHERE id = $1`, cid).Scan(&id); err != nil {
		return "", err
	}
	if id != nil {
		return *id, nil
	}
	codes, parents := []string{"5150"}, []string{"5"}
	if lebanesePCG(ctx, tx, cid) {
		codes, parents = []string{"6089"}, []string{"608", "60", "6"}
	}
	acc, err := ensureAccount(ctx, tx, cid, codes, "expense", "Production cost variance", "فروقات تكلفة الإنتاج", "Écarts sur coûts de production", parents)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `UPDATE companies SET mfg_variance_account_id = $2::uuid WHERE id = $1`, cid, acc)
	return acc, err
}

// mfgSubcontractAccount: subcontracting received, not billed yet (the subcontractor's bill clears it).
func mfgSubcontractAccount(ctx context.Context, tx pgx.Tx, cid string) (string, error) {
	var id *string
	if err := tx.QueryRow(ctx, `SELECT mfg_subcontract_account_id::text FROM companies WHERE id = $1`, cid).Scan(&id); err != nil {
		return "", err
	}
	if id != nil {
		return *id, nil
	}
	codes, parents := []string{"2150"}, []string{"21", "2"}
	if lebanesePCG(ctx, tx, cid) {
		codes, parents = []string{"408"}, []string{"40", "4"}
	}
	acc, err := ensureAccount(ctx, tx, cid, codes, "liability", "Subcontracting received not billed", "تصنيع خارجي مستلم غير مفوتر", "Sous-traitance reçue non facturée", parents)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `UPDATE companies SET mfg_subcontract_account_id = $2::uuid WHERE id = $1`, cid, acc)
	return acc, err
}

type produceIn struct {
	Date     string   `json:"date"`
	ScrapQty string   `json:"scrap_qty"`
	Quantity string   `json:"quantity"` // processed now (good + scrap); less than the order = the rest goes to a backorder
	Lots     []lotQty `json:"lots"`     // lots / serials of the finished item
	Lines    []struct {
		LineID   string   `json:"line_id"`
		Quantity string   `json:"quantity"`
		Lots     []lotQty `json:"lots"`
	} `json:"lines"`
	Byproducts []struct {
		ID       string `json:"id"`
		Quantity string `json:"quantity"`
	} `json:"byproducts"`
	Bill        *bool  `json:"bill"` // subcontracting: book the subcontractor's bill now (default yes)
	SupplierRef string `json:"supplier_ref"`
}

// POST /companies/{cid}/mfg/orders/{id}/produce: consumes the components (the quantities actually used, default = planned)
// and brings the good units of the finished item (and the by-products) in. A quantity below the order's makes a backorder.
func (s *Server) produceMfgOrder(w http.ResponseWriter, r *http.Request) {
	var in produceIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		back, err := mfgProduce(ctx, tx, cid, id, in)
		if err != nil {
			return nil, err
		}
		o, err := loadMfgOrder(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		m := o.(map[string]json.RawMessage)
		b, _ := json.Marshal(back)
		m["backorder_id"] = b
		return m, nil
	})
}

// mfgSplit keeps part of an order (keep of qty) and moves the rest into a new backorder; returns the backorder id.
func mfgSplit(ctx context.Context, tx pgx.Tx, cid, id string, qty, keep decimal.Decimal) (string, error) {
	rest := qty.Sub(keep)
	ratio := keep.Div(qty)
	var n int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'mfg_order')`, cid).Scan(&n); err != nil {
		return "", err
	}
	var bid, st string
	if err := tx.QueryRow(ctx, `INSERT INTO mfg_orders (company_id, number, bom_id, item_id, quantity, warehouse_id, order_date, status, labour, overhead,
		notes, bom_type, routing_id, deadline, origin, backorder_of, parent_id, sales_order_id, expected_cost, subcontract_wh_id, components_sent, confirmed_at)
		SELECT company_id, $2, bom_id, item_id, $3::numeric, warehouse_id, order_date, CASE WHEN status = 'draft' THEN 'draft' ELSE 'confirmed' END,
		labour - round(labour * $4::numeric, 4), overhead - round(overhead * $4::numeric, 4), notes, bom_type, routing_id, deadline, origin, id, parent_id,
		sales_order_id, expected_cost - round(expected_cost * $4::numeric, 4), subcontract_wh_id, components_sent, confirmed_at
		FROM mfg_orders WHERE id = $1 RETURNING id::text, status`, id, n, rest.String(), ratio.String()).Scan(&bid, &st); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE mfg_orders SET quantity = $2::numeric, labour = round(labour * $3::numeric, 4), overhead = round(overhead * $3::numeric, 4),
		expected_cost = round(expected_cost * $3::numeric, 4) WHERE id = $1`, id, keep.String(), ratio.String()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mfg_order_lines (company_id, order_id, line_no, item_id, planned_qty, consumed_qty, sent_qty)
		SELECT company_id, $2, line_no, item_id, planned_qty - round(planned_qty * $3::numeric, 4), planned_qty - round(planned_qty * $3::numeric, 4),
		sent_qty - round(sent_qty * $3::numeric, 4) FROM mfg_order_lines WHERE order_id = $1`, id, bid, ratio.String()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE mfg_order_lines SET planned_qty = round(planned_qty * $2::numeric, 4), consumed_qty = round(planned_qty * $2::numeric, 4),
		sent_qty = round(sent_qty * $2::numeric, 4), reserved_qty = 0 WHERE order_id = $1`, id, ratio.String()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mfg_order_byproducts (company_id, order_id, item_id, planned_qty, produced_qty, cost_share)
		SELECT company_id, $2, item_id, planned_qty - round(planned_qty * $3::numeric, 4), planned_qty - round(planned_qty * $3::numeric, 4), cost_share
		FROM mfg_order_byproducts WHERE order_id = $1`, id, bid, ratio.String()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE mfg_order_byproducts SET planned_qty = round(planned_qty * $2::numeric, 4), produced_qty = round(planned_qty * $2::numeric, 4)
		WHERE order_id = $1`, id, ratio.String()); err != nil {
		return "", err
	}
	// the backorder gets its own work orders (the rest of the planned time) and quality checks
	if _, err := tx.Exec(ctx, `INSERT INTO mfg_workorders (company_id, order_id, seq, name, workcenter_id, instructions, planned_min, status)
		SELECT company_id, $2, seq, name, workcenter_id, instructions, planned_min - round(planned_min * $3::numeric, 2), 'pending'
		FROM mfg_workorders WHERE order_id = $1 AND status <> 'cancelled'`, id, bid, ratio.String()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE mfg_workorders SET planned_min = round(planned_min * $2::numeric, 2) WHERE order_id = $1`, id, ratio.String()); err != nil {
		return "", err
	}
	if st != "draft" {
		if err := mfgWoReadyFirst(ctx, tx, bid); err != nil {
			return "", err
		}
		if err := mfgCreateChecks(ctx, tx, cid, bid); err != nil {
			return "", err
		}
		if err := mfgReserve(ctx, tx, cid, bid); err != nil {
			return "", err
		}
	}
	return bid, nil
}

// mfgProduce finishes (part of) an order; returns the backorder id when a part was left.
func mfgProduce(ctx context.Context, tx pgx.Tx, cid, id string, in produceIn) (*string, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	scrap, ok := money(defaultStr(in.ScrapQty, "0"))
	if !ok || scrap.IsNegative() || (in.Date != "" && !validDate(in.Date)) {
		return nil, badRequest("bad_request")
	}
	var st, item, wh, qtyS, date, bomID, typ string
	var subWh *string
	var sent bool
	var number int64
	err := tx.QueryRow(ctx, `SELECT status, item_id::text, warehouse_id::text, quantity::text, order_date::text, number, bom_id::text, bom_type,
		subcontract_wh_id::text, components_sent FROM mfg_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
		Scan(&st, &item, &wh, &qtyS, &date, &number, &bomID, &typ, &subWh, &sent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	if st != "draft" && st != "confirmed" && st != "progress" {
		return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	if typ == "subcontract" && !sent {
		return nil, &apiError{status: http.StatusConflict, code: "components_not_sent"}
	}
	var todo int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM mfg_quality_checks WHERE order_id = $1 AND status = 'todo'`, id).Scan(&todo); err != nil {
		return nil, err
	}
	if todo > 0 {
		return nil, &apiError{status: http.StatusConflict, code: "quality_pending", detail: fmt.Sprint(todo)}
	}
	if in.Date != "" {
		date = in.Date
	}
	qty, _ := decimal.NewFromString(qtyS)
	proc := qty
	if strings.TrimSpace(in.Quantity) != "" {
		p, ok := money(in.Quantity)
		if !ok || !p.IsPositive() || p.GreaterThan(qty) {
			return nil, badRequest("invalid_amount")
		}
		proc = p
	}
	good := proc.Sub(scrap)
	if !good.IsPositive() {
		return nil, badRequest("invalid_amount")
	}
	var back *string
	if proc.LessThan(qty) {
		b, err := mfgSplit(ctx, tx, cid, id, qty, proc)
		if err != nil {
			return nil, err
		}
		back = &b
	}
	var labS, ovhS, outS, subPriceS string
	var subcontractor *string
	if err := tx.QueryRow(ctx, `SELECT o.labour::text, o.overhead::text, b.output_qty::text, b.subcontract_price::text, b.subcontractor_id::text
		FROM mfg_orders o JOIN boms b ON b.id = o.bom_id WHERE o.id = $1`, id).Scan(&labS, &ovhS, &outS, &subPriceS, &subcontractor); err != nil {
		return nil, err
	}
	lab, _ := decimal.NewFromString(labS)
	ovh, _ := decimal.NewFromString(ovhS)
	out, _ := decimal.NewFromString(outS)
	subPrice, _ := decimal.NewFromString(subPriceS)
	service := decimal.Zero
	if typ == "subcontract" {
		service = subPrice.Mul(proc).Div(out).Round(4)
	}
	ctx2 := ctx
	for _, l := range in.Lines {
		q, ok := money(l.Quantity)
		if !ok || q.IsNegative() || !isUUID(l.LineID) {
			return nil, badRequest("invalid_amount")
		}
		var lineItem string
		err := tx.QueryRow(ctx, `UPDATE mfg_order_lines SET consumed_qty = $3::numeric WHERE id = $1 AND order_id = $2 RETURNING item_id::text`, l.LineID, id, q.String()).Scan(&lineItem)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, badRequest("invalid_line")
		}
		if err != nil {
			return nil, err
		}
		ctx2 = withLots(ctx2, lineItem, l.Lots)
	}
	// work orders: stop the timers, close what is open, charge the work centers' time
	if _, err := tx.Exec(ctx, `UPDATE mfg_wo_times t SET ended_at = now(), minutes = round(extract(epoch FROM now() - t.started_at)::numeric / 60, 2)
		FROM mfg_workorders x WHERE x.id = t.workorder_id AND x.order_id = $1 AND t.ended_at IS NULL`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE mfg_workorders x SET status = 'done', finished_at = coalesce(finished_at, now()), qty_done = $2::numeric,
		actual_min = CASE WHEN x.actual_min > 0 THEN x.actual_min ELSE (SELECT coalesce(sum(t.minutes), 0) FROM mfg_wo_times t WHERE t.workorder_id = x.id) END
		WHERE x.order_id = $1 AND x.status NOT IN ('done', 'cancelled')`, id, good.String()); err != nil {
		return nil, err
	}
	var opsS string
	if _, err := tx.Exec(ctx, `UPDATE mfg_workorders x SET cost = round(CASE WHEN x.actual_min > 0 THEN x.actual_min ELSE x.planned_min END * c.cost_per_hour / 60, 4)
		FROM mfg_workcenters c WHERE c.id = x.workcenter_id AND x.order_id = $1 AND x.status = 'done'`, id); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(cost), 0)::text FROM mfg_workorders WHERE order_id = $1 AND status = 'done'`, id).Scan(&opsS); err != nil {
		return nil, err
	}
	opsCost, _ := decimal.NewFromString(opsS)

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
	consumeWh := wh
	if typ == "subcontract" && subWh != nil {
		consumeWh = *subWh
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
	credits := []jline{}
	type outMove struct {
		item            string
		qty, avg, value decimal.Decimal
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
		have, avg, err := lockStock(ctx, tx, c.item, consumeWh)
		if err != nil {
			return nil, err
		}
		if have.LessThan(c.qty) {
			return nil, insufficientStock(it.Name, have)
		}
		v, err := outValue(ctx, tx, c.item, consumeWh, have, avg, c.qty)
		if err != nil {
			return nil, err
		}
		compTotal = compTotal.Add(v)
		moves = append(moves, outMove{c.item, c.qty, avg, v})
		if _, err := tx.Exec(ctx, `UPDATE mfg_order_lines SET value = $2::numeric WHERE id = $1`, c.id, v.String()); err != nil {
			return nil, err
		}
		if v.IsPositive() {
			credits = append(credits, jline{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Credit: v, Rate: "1"})
		}
	}
	total := compTotal.Add(lab).Add(ovh).Add(opsCost).Add(service)
	if lab.Add(opsCost).IsPositive() {
		acc, err := mfgAbsorbAccount(ctx, tx, cid, "mfg_labour_account_id")
		if err != nil {
			return nil, err
		}
		credits = append(credits, jline{Account: acc, Desc: memo + " · labour", Currency: set.Base, Credit: lab.Add(opsCost), Rate: "1"})
	}
	if ovh.IsPositive() {
		acc, err := mfgAbsorbAccount(ctx, tx, cid, "mfg_overhead_account_id")
		if err != nil {
			return nil, err
		}
		credits = append(credits, jline{Account: acc, Desc: memo + " · overhead", Currency: set.Base, Credit: ovh, Rate: "1"})
	}
	var clearing string
	if service.IsPositive() {
		if clearing, err = mfgSubcontractAccount(ctx, tx, cid); err != nil {
			return nil, err
		}
		credits = append(credits, jline{Account: clearing, Desc: memo + " · subcontracting", Currency: set.Base, Credit: service, Rate: "1"})
	}
	// by-products take their share of the cost
	type byp struct {
		id, item string
		qty      decimal.Decimal
		share    decimal.Decimal
		value    decimal.Decimal
		inv      string
		name     string
	}
	brows, err := tx.Query(ctx, `SELECT id::text, item_id::text, produced_qty::text, cost_share::text FROM mfg_order_byproducts WHERE order_id = $1`, id)
	if err != nil {
		return nil, err
	}
	bps, err := pgx.CollectRows(brows, func(row pgx.CollectableRow) (byp, error) {
		var x byp
		var q, s string
		err := row.Scan(&x.id, &x.item, &q, &s)
		x.qty, _ = decimal.NewFromString(q)
		x.share, _ = decimal.NewFromString(s)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	for _, o := range in.Byproducts {
		q, ok := money(o.Quantity)
		if !ok || q.IsNegative() {
			return nil, badRequest("invalid_amount")
		}
		found := false
		for i := range bps {
			if bps[i].id == o.ID {
				bps[i].qty, found = q, true
			}
		}
		if !found {
			return nil, badRequest("invalid_line")
		}
	}
	bpTotal := decimal.Zero
	debits := []jline{}
	for i := range bps {
		if !bps[i].qty.IsPositive() {
			continue
		}
		it, err := loadItem(ctx, tx, cid, bps[i].item)
		if err != nil {
			return nil, err
		}
		if it.Inventory == nil {
			return nil, badRequest("not_stock_item")
		}
		bps[i].inv, bps[i].name = *it.Inventory, it.Name
		bps[i].value = total.Mul(bps[i].share).Div(decimal.NewFromInt(100)).Round(4)
		bpTotal = bpTotal.Add(bps[i].value)
		if bps[i].value.IsPositive() {
			debits = append(debits, jline{Account: bps[i].inv, Desc: memo + " · " + it.Name, Currency: set.Base, Debit: bps[i].value, Rate: "1"})
		}
	}
	fgValue := total.Sub(bpTotal)
	var stdS string
	if err := tx.QueryRow(ctx, `SELECT standard_cost::text FROM items WHERE id = $1`, item).Scan(&stdS); err != nil {
		return nil, err
	}
	std, _ := decimal.NewFromString(stdS)
	fgIn, variance := fgValue, decimal.Zero
	if std.IsPositive() {
		fgIn = good.Mul(std).Round(4)
		variance = fgValue.Sub(fgIn)
	}
	if !variance.IsZero() {
		acc, err := mfgVarianceAccount(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if variance.IsPositive() {
			debits = append(debits, jline{Account: acc, Desc: memo + " · variance", Currency: set.Base, Debit: variance, Rate: "1"})
		} else {
			credits = append(credits, jline{Account: acc, Desc: memo + " · variance", Currency: set.Base, Credit: variance.Neg(), Rate: "1"})
		}
	}
	var je *string
	if total.IsPositive() || fgIn.IsPositive() {
		lines := []jline{}
		if fgIn.IsPositive() {
			lines = append(lines, jline{Account: *fin.Inventory, Desc: memo, Currency: set.Base, Debit: fgIn, Rate: "1"})
		}
		lines = append(append(lines, debits...), credits...)
		j, err := postJournal(ctx, tx, cid, set, date, memo, fmt.Sprintf("MFG-%d", number), "manufacturing", lines)
		if err != nil {
			return nil, err
		}
		je = &j
	}
	for _, m := range moves {
		if err := stockOut(ctx2, tx, cid, m.item, consumeWh, m.qty, m.avg, m.value, date, "mfg_out", id, je, memo); err != nil {
			return nil, err
		}
	}
	if err := stockIn(withLots(ctx, item, in.Lots), tx, cid, item, wh, good, fgIn, date, "mfg_in", id, je, memo); err != nil {
		return nil, err
	}
	for _, b := range bps {
		if _, err := tx.Exec(ctx, `UPDATE mfg_order_byproducts SET produced_qty = $2::numeric, value = $3::numeric WHERE id = $1`, b.id, b.qty.String(), b.value.String()); err != nil {
			return nil, err
		}
		if !b.qty.IsPositive() {
			continue
		}
		if err := stockIn(ctx, tx, cid, b.item, wh, b.qty, b.value, date, "mfg_in", id, je, memo+" · "+b.name); err != nil {
			return nil, err
		}
	}
	unit := fgIn.DivRound(good, 6)
	if _, err := tx.Exec(ctx, `UPDATE mfg_orders SET status = 'done', order_date = $2::date, components_value = $3::numeric, scrap_qty = $4::numeric,
		unit_cost = $5::numeric, journal_entry_id = $6, done_at = now(), ops_cost = $7::numeric, service_cost = $8::numeric, byproducts_value = $9::numeric,
		standard_cost = $10::numeric, variance = $11::numeric WHERE id = $1`, id, date, compTotal.String(), scrap.String(), unit.String(), je,
		opsCost.String(), service.String(), bpTotal.String(), std.String(), variance.String()); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE mfg_order_lines SET reserved_qty = 0 WHERE order_id = $1`, id); err != nil {
		return nil, err
	}
	if service.IsPositive() && subcontractor != nil && (in.Bill == nil || *in.Bill) {
		billID, err := createBillTx(ctx, tx, cid, billInput{SupplierID: *subcontractor, BillDate: date, Currency: set.Base, SupplierRef: in.SupplierRef,
			Notes: fmt.Sprintf("MO #%d", number), Lines: []billLineIn{{AccountID: clearing, Description: fmt.Sprintf("Subcontracting MO #%d · %s %s", number, good.String(), fin.Name),
				Quantity: "1", UnitPrice: service.String()}}})
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE mfg_orders SET service_bill_id = $2 WHERE id = $1`, id, billID); err != nil {
			return nil, err
		}
	}
	return back, nil
}

// POST /companies/{cid}/mfg/orders/{id}/cancel (not produced yet; components sent to a subcontractor must come back first)
func (s *Server) cancelMfgOrder(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		var st string
		var sent bool
		err := tx.QueryRow(ctx, `SELECT status, components_sent FROM mfg_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st, &sent)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st != "draft" && st != "confirmed" && st != "progress" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if sent {
			return nil, &apiError{status: http.StatusConflict, code: "components_sent"}
		}
		for _, q := range []string{
			`UPDATE mfg_orders SET status = 'cancelled' WHERE id = $1`,
			`UPDATE mfg_order_lines SET reserved_qty = 0 WHERE order_id = $1`,
			`UPDATE mfg_wo_times t SET ended_at = now(), minutes = round(extract(epoch FROM now() - t.started_at)::numeric / 60, 2)
				FROM mfg_workorders x WHERE x.id = t.workorder_id AND x.order_id = $1 AND t.ended_at IS NULL`,
			`UPDATE mfg_workorders SET status = 'cancelled' WHERE order_id = $1 AND status <> 'done'`,
			`UPDATE mfg_quality_checks SET status = 'cancelled' WHERE order_id = $1 AND status = 'todo'`,
		} {
			if _, err := tx.Exec(ctx, q, id); err != nil {
				return nil, err
			}
		}
		return loadMfgOrder(ctx, tx, cid, id)
	})
}

// scrapCore: damaged / lost goods out of stock, at cost, to the scrap account (else the stock adjustment account).
func scrapCore(ctx context.Context, tx pgx.Tx, cid, itemID, whIn, qtyS, date, reason, mfgOrder string) (string, error) {
	qty, ok := money(qtyS)
	if !ok || !qty.IsPositive() || !validDate(date) || !isUUID(itemID) || strings.TrimSpace(reason) == "" {
		return "", badRequest("bad_request")
	}
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return "", err
	}
	it, err := loadItem(ctx, tx, cid, itemID)
	if err != nil {
		return "", err
	}
	if it.Kind != "stock" || it.Inventory == nil {
		return "", badRequest("not_stock_item")
	}
	wh, err := checkWarehouse(ctx, tx, cid, whIn)
	if err != nil {
		return "", err
	}
	var acc *string
	if err := tx.QueryRow(ctx, `SELECT coalesce(scrap_account_id, stock_adjust_account_id)::text FROM companies WHERE id = $1`, cid).Scan(&acc); err != nil {
		return "", err
	}
	if acc == nil {
		return "", notSetup("adjust")
	}
	have, avg, err := lockStock(ctx, tx, it.ID, wh)
	if err != nil {
		return "", err
	}
	if have.LessThan(qty) {
		return "", insufficientStock(it.Name, have)
	}
	value, err := outValue(ctx, tx, it.ID, wh, have, avg, qty)
	if err != nil {
		return "", err
	}
	var n int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'stock_scrap')`, cid).Scan(&n); err != nil {
		return "", err
	}
	reason = strings.TrimSpace(reason)
	memo := fmt.Sprintf("Scrap #%d · %s %s %s · %s", n, qty.String(), it.Unit, it.Name, reason)
	var je *string
	if value.IsPositive() {
		j, err := postJournal(ctx, tx, cid, set, date, memo, fmt.Sprintf("SCR-%d", n), "stock", []jline{
			{Account: *acc, Desc: memo, Currency: set.Base, Debit: value},
			{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Credit: value},
		})
		if err != nil {
			return "", err
		}
		je = &j
	}
	var docID string
	if err := tx.QueryRow(ctx, `INSERT INTO stock_docs (company_id, number, kind, doc_date, item_id, warehouse_id, quantity, value, account_id, notes, journal_entry_id, mfg_order_id)
		VALUES ($1, $2, 'scrap', $3::date, $4, $5, $6::numeric, $7::numeric, $8, $9, $10, nullif($11, '')::uuid) RETURNING id::text`,
		cid, n, date, it.ID, wh, qty.Neg().String(), value.Neg().String(), *acc, reason, je, mfgOrder).Scan(&docID); err != nil {
		return "", err
	}
	if err := stockOut(ctx, tx, cid, it.ID, wh, qty, avg, value, date, "scrap", docID, je, reason); err != nil {
		return "", err
	}
	return docID, nil
}

// POST /companies/{cid}/mfg/scrap {item_id, warehouse_id, quantity, date, reason}
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
	if (in.WarehouseID != "" && !isUUID(in.WarehouseID)) || !isUUID(in.ItemID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		docID, err := scrapCore(ctx, tx, cid, in.ItemID, in.WarehouseID, in.Quantity, in.Date, in.Reason, "")
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+stockDocCols+stockDocFrom+` WHERE d.id = $1`, docID)
	})
}
