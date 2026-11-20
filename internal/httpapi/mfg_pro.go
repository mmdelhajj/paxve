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

// Manufacturing pro (phase 2): work centers and routings, BoM versions / structure / standard cost, order
// confirmation (work orders from the routing, quality checks, component reservation), work orders with operator
// timers (the shop-floor screen), scrap on an order, and subcontracting (components sent to the subcontractor).

// ---------- work centers ----------

const wcCols = `c.id, c.code, c.name, trim_scale(c.capacity)::text AS capacity, trim_scale(c.hours_per_day)::text AS hours_per_day, c.days_per_week,
	trim_scale(c.efficiency)::text AS efficiency, c.cost_per_hour::text AS cost_per_hour, trim_scale(c.setup_min)::text AS setup_min, c.notes, c.is_active,
	(SELECT count(*) FROM mfg_workorders x WHERE x.workcenter_id = c.id AND x.status IN ('ready', 'progress', 'paused', 'pending')) AS open_workorders,
	(SELECT count(*) FROM mfg_workorders x WHERE x.workcenter_id = c.id AND x.status = 'progress') AS running`

func (s *Server) mfgWorkcenters(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+wcCols+` FROM mfg_workcenters c WHERE c.company_id = $1 ORDER BY c.code`, r.PathValue("cid"))
	})
}

// POST /mfg/workcenters · PATCH /mfg/workcenters/{id}
func (s *Server) mfgSaveWorkcenter(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code        *string `json:"code"`
		Name        *string `json:"name"`
		Capacity    *string `json:"capacity"`
		HoursPerDay *string `json:"hours_per_day"`
		DaysPerWeek *int    `json:"days_per_week"`
		Efficiency  *string `json:"efficiency"`
		CostPerHour *string `json:"cost_per_hour"`
		SetupMin    *string `json:"setup_min"`
		Notes       *string `json:"notes"`
		IsActive    *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	nums := map[string]*string{"capacity": in.Capacity, "hours_per_day": in.HoursPerDay, "efficiency": in.Efficiency, "cost_per_hour": in.CostPerHour, "setup_min": in.SetupMin}
	for _, v := range nums {
		if v != nil {
			if d, ok := money(*v); !ok || d.IsNegative() {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
		}
	}
	if (id == "" && (in.Code == nil || in.Name == nil || strings.TrimSpace(*in.Code) == "" || strings.TrimSpace(*in.Name) == "")) || (id != "" && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	trim := func(p *string) *string {
		if p == nil {
			return nil
		}
		t := strings.TrimSpace(*p)
		return &t
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		var err error
		if id == "" {
			err = tx.QueryRow(ctx, `INSERT INTO mfg_workcenters (company_id, code, name, capacity, hours_per_day, days_per_week, efficiency, cost_per_hour, setup_min, notes)
				VALUES ($1, $2, $3, coalesce($4::numeric, 1), coalesce($5::numeric, 8), coalesce($6, 6), coalesce($7::numeric, 100), coalesce($8::numeric, 0),
				coalesce($9::numeric, 0), coalesce($10, '')) RETURNING id::text`, cid, trim(in.Code), trim(in.Name), in.Capacity, in.HoursPerDay, in.DaysPerWeek,
				in.Efficiency, in.CostPerHour, in.SetupMin, trim(in.Notes)).Scan(&id)
		} else {
			var tag interface{ RowsAffected() int64 }
			tag, err = tx.Exec(ctx, `UPDATE mfg_workcenters SET code = coalesce($3, code), name = coalesce($4, name), capacity = coalesce($5::numeric, capacity),
				hours_per_day = coalesce($6::numeric, hours_per_day), days_per_week = coalesce($7, days_per_week), efficiency = coalesce($8::numeric, efficiency),
				cost_per_hour = coalesce($9::numeric, cost_per_hour), setup_min = coalesce($10::numeric, setup_min), notes = coalesce($11, notes),
				is_active = coalesce($12, is_active) WHERE id = $1 AND company_id = $2`, id, cid, trim(in.Code), trim(in.Name), in.Capacity, in.HoursPerDay,
				in.DaysPerWeek, in.Efficiency, in.CostPerHour, in.SetupMin, trim(in.Notes), in.IsActive)
			if err == nil && tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
		}
		if err != nil {
			if strings.Contains(err.Error(), "check constraint") {
				return nil, badRequest("bad_request")
			}
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+wcCols+` FROM mfg_workcenters c WHERE c.id = $1`, id)
	})
}

// ---------- routings ----------

func (s *Server) mfgRoutings(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT r.id, r.name, r.notes, r.is_active, (SELECT count(*) FROM mfg_routing_ops o WHERE o.routing_id = r.id) AS operations,
			(SELECT trim_scale(coalesce(sum(o.minutes), 0))::text FROM mfg_routing_ops o WHERE o.routing_id = r.id) AS minutes,
			(SELECT count(*) FROM boms b WHERE b.routing_id = r.id) AS boms
			FROM mfg_routings r WHERE r.company_id = $1 ORDER BY r.name`, r.PathValue("cid"))
	})
}

func loadRouting(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	rt, err := queryJSONOne(ctx, tx, `SELECT r.id, r.name, r.notes, r.is_active FROM mfg_routings r WHERE r.id = $1 AND r.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	ops, err := queryJSON(ctx, tx, `SELECT o.id, o.seq, o.name, o.workcenter_id, c.code AS workcenter_code, c.name AS workcenter, trim_scale(o.minutes)::text AS minutes,
		o.instructions, round(o.minutes * 100 / c.efficiency * c.cost_per_hour / 60, 4)::text AS cost
		FROM mfg_routing_ops o JOIN mfg_workcenters c ON c.id = o.workcenter_id WHERE o.routing_id = $1 ORDER BY o.seq`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"routing": rt, "operations": ops}, nil
}

func (s *Server) mfgGetRouting(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadRouting(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// POST /mfg/routings · PUT /mfg/routings/{id} {name, notes, is_active, operations: [{name, workcenter_id, minutes, instructions}]}
func (s *Server) mfgSaveRouting(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string `json:"name"`
		Notes      string `json:"notes"`
		IsActive   *bool  `json:"is_active"`
		Operations []struct {
			Name         string `json:"name"`
			WorkcenterID string `json:"workcenter_id"`
			Minutes      string `json:"minutes"`
			Instructions string `json:"instructions"`
		} `json:"operations"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	ok := strings.TrimSpace(in.Name) != "" && len(in.Operations) > 0 && len(in.Operations) <= 50 && (id == "" || isUUID(id))
	for _, o := range in.Operations {
		m, okm := money(defaultStr(o.Minutes, "0"))
		ok = ok && okm && !m.IsNegative() && isUUID(o.WorkcenterID) && strings.TrimSpace(o.Name) != ""
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	active := in.IsActive == nil || *in.IsActive
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if id == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO mfg_routings (company_id, name, notes, is_active) VALUES ($1, $2, $3, $4) RETURNING id::text`,
				cid, strings.TrimSpace(in.Name), strings.TrimSpace(in.Notes), active).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE mfg_routings SET name = $3, notes = $4, is_active = $5 WHERE id = $1 AND company_id = $2`,
				id, cid, strings.TrimSpace(in.Name), strings.TrimSpace(in.Notes), active)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
			if _, err := tx.Exec(ctx, `DELETE FROM mfg_routing_ops WHERE routing_id = $1`, id); err != nil {
				return nil, err
			}
		}
		for i, o := range in.Operations {
			var okW bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mfg_workcenters WHERE id = $1 AND company_id = $2)`, o.WorkcenterID, cid).Scan(&okW); err != nil {
				return nil, err
			}
			if !okW {
				return nil, badRequest("invalid_workcenter")
			}
			m, _ := money(defaultStr(o.Minutes, "0"))
			if _, err := tx.Exec(ctx, `INSERT INTO mfg_routing_ops (company_id, routing_id, seq, name, workcenter_id, minutes, instructions)
				VALUES ($1, $2, $3, $4, $5, $6::numeric, $7)`, cid, id, (i+1)*10, strings.TrimSpace(o.Name), o.WorkcenterID, m.String(), strings.TrimSpace(o.Instructions)); err != nil {
				return nil, err
			}
		}
		return loadRouting(ctx, tx, cid, id)
	})
}

// ---------- BoM versions, structure and cost ----------

// POST /mfg/boms/{id}/new-version {version_note}: a copy of the BoM as the next version (inactive until activated).
func (s *Server) mfgBomNewVersion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		VersionNote string `json:"version_note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		var nid string
		err := tx.QueryRow(ctx, `INSERT INTO boms (company_id, item_id, name, output_qty, labour_cost, overhead_cost, notes, is_active, code, version, family_id,
			bom_type, routing_id, subcontractor_id, subcontract_price, lead_days, version_note)
			SELECT company_id, item_id, name, output_qty, labour_cost, overhead_cost, notes, false, code,
			(SELECT max(v.version) + 1 FROM boms v WHERE v.family_id = b.family_id), family_id, bom_type, routing_id, subcontractor_id, subcontract_price, lead_days, $3
			FROM boms b WHERE b.id = $1 AND b.company_id = $2 RETURNING id::text`, id, cid, strings.TrimSpace(in.VersionNote)).Scan(&nid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO bom_lines (company_id, bom_id, line_no, item_id, quantity) SELECT company_id, $2, line_no, item_id, quantity
			FROM bom_lines WHERE bom_id = $1`, id, nid); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO bom_byproducts (company_id, bom_id, item_id, quantity, cost_share) SELECT company_id, $2, item_id, quantity, cost_share
			FROM bom_byproducts WHERE bom_id = $1`, id, nid); err != nil {
			return nil, err
		}
		return loadBOM(ctx, tx, cid, nid)
	})
}

// POST /mfg/boms/{id}/activate: this version becomes the active one of its family.
func (s *Server) mfgBomActivate(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		var family string
		err := tx.QueryRow(ctx, `SELECT family_id::text FROM boms WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&family)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if err := bomSetActive(ctx, tx, id, family, true); err != nil {
			return nil, err
		}
		return loadBOM(ctx, tx, cid, id)
	})
}

type bomNode struct {
	BomID      string          `json:"bom_id"`
	BomName    string          `json:"bom_name"`
	BomType    string          `json:"bom_type"`
	Version    int             `json:"version"`
	ItemID     string          `json:"item_id"`
	ItemCode   string          `json:"item_code"`
	ItemName   string          `json:"item_name"`
	Unit       string          `json:"unit"`
	Quantity   decimal.Decimal `json:"quantity"`
	Components []bomCompNode   `json:"components"`
	Operations []bomOpNode     `json:"operations"`
	Byproducts []bomCompNode   `json:"byproducts"`
	Labour     decimal.Decimal `json:"labour"`
	Overhead   decimal.Decimal `json:"overhead"`
	Service    decimal.Decimal `json:"service"`
	Cost       decimal.Decimal `json:"cost"`
	UnitCost   decimal.Decimal `json:"unit_cost"`
}

type bomCompNode struct {
	ItemID   string          `json:"item_id"`
	ItemCode string          `json:"item_code"`
	ItemName string          `json:"item_name"`
	Unit     string          `json:"unit"`
	Quantity decimal.Decimal `json:"quantity"`
	UnitCost decimal.Decimal `json:"unit_cost"`
	Cost     decimal.Decimal `json:"cost"`
	Source   string          `json:"source"` // stock | standard | bom | none
	Share    decimal.Decimal `json:"cost_share,omitempty"`
	Sub      *bomNode        `json:"sub,omitempty"`
}

type bomOpNode struct {
	Name       string          `json:"name"`
	Workcenter string          `json:"workcenter"`
	Minutes    decimal.Decimal `json:"minutes"`
	Cost       decimal.Decimal `json:"cost"`
}

// bomTree: the multi-level structure of factor batches of a BoM with the rolled-up cost.
func bomTree(ctx context.Context, tx pgx.Tx, cid, bomID string, factor decimal.Decimal, depth int) (*bomNode, error) {
	if depth > 10 {
		return nil, &apiError{status: http.StatusUnprocessableEntity, code: "bom_cycle"}
	}
	n := &bomNode{BomID: bomID}
	var out, lab, ovh, sub string
	err := tx.QueryRow(ctx, `SELECT b.name, b.bom_type, b.version, b.item_id::text, it.code, it.name, it.unit, b.output_qty::text, b.labour_cost::text,
		b.overhead_cost::text, b.subcontract_price::text FROM boms b JOIN items it ON it.id = b.item_id WHERE b.id = $1 AND b.company_id = $2`, bomID, cid).
		Scan(&n.BomName, &n.BomType, &n.Version, &n.ItemID, &n.ItemCode, &n.ItemName, &n.Unit, &out, &lab, &ovh, &sub)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	o, _ := decimal.NewFromString(out)
	n.Quantity = o.Mul(factor)
	l, _ := decimal.NewFromString(lab)
	v, _ := decimal.NewFromString(ovh)
	sc, _ := decimal.NewFromString(sub)
	n.Labour, n.Overhead = l.Mul(factor).Round(4), v.Mul(factor).Round(4)
	if n.BomType == "subcontract" {
		n.Service = sc.Mul(factor).Round(4)
	}
	rows, err := tx.Query(ctx, `SELECT l.item_id::text, it.code, it.name, it.unit, l.quantity::text,
		coalesce((SELECT CASE WHEN sum(st.quantity) > 0 THEN sum(st.value) / sum(st.quantity) END FROM stock st WHERE st.item_id = l.item_id), 0)::numeric(20, 6)::text,
		it.standard_cost::text,
		(SELECT x.id::text FROM boms x WHERE x.item_id = l.item_id AND x.company_id = l.company_id AND x.is_active ORDER BY x.bom_type = 'phantom' DESC, x.created_at LIMIT 1)
		FROM bom_lines l JOIN items it ON it.id = l.item_id WHERE l.bom_id = $1 ORDER BY l.line_no`, bomID)
	if err != nil {
		return nil, err
	}
	type row struct {
		c           bomCompNode
		q, avg, std string
		sub         *string
	}
	rs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.c.ItemID, &x.c.ItemCode, &x.c.ItemName, &x.c.Unit, &x.q, &x.avg, &x.std, &x.sub)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	total := n.Labour.Add(n.Overhead).Add(n.Service)
	for _, x := range rs {
		c := x.c
		q, _ := decimal.NewFromString(x.q)
		c.Quantity = q.Mul(factor)
		avg, _ := decimal.NewFromString(x.avg)
		std, _ := decimal.NewFromString(x.std)
		if x.sub != nil {
			var so string
			if err := tx.QueryRow(ctx, `SELECT output_qty::text FROM boms WHERE id = $1`, *x.sub).Scan(&so); err != nil {
				return nil, err
			}
			sod, _ := decimal.NewFromString(so)
			if c.Sub, err = bomTree(ctx, tx, cid, *x.sub, c.Quantity.Div(sod), depth+1); err != nil {
				return nil, err
			}
		}
		switch {
		case avg.IsPositive():
			c.UnitCost, c.Source = avg, "stock"
		case std.IsPositive():
			c.UnitCost, c.Source = std, "standard"
		case c.Sub != nil && c.Quantity.IsPositive():
			c.UnitCost, c.Source = c.Sub.Cost.DivRound(c.Quantity, 6), "bom"
		default:
			c.Source = "none"
		}
		c.Cost = c.Quantity.Mul(c.UnitCost).Round(4)
		total = total.Add(c.Cost)
		n.Components = append(n.Components, c)
	}
	if n.BomType == "normal" {
		orows, err := tx.Query(ctx, `SELECT o.name, c.name, (o.minutes * 100 / c.efficiency)::text, c.cost_per_hour::text FROM boms b
			JOIN mfg_routing_ops o ON o.routing_id = b.routing_id JOIN mfg_workcenters c ON c.id = o.workcenter_id WHERE b.id = $1 ORDER BY o.seq`, bomID)
		if err != nil {
			return nil, err
		}
		ops, err := pgx.CollectRows(orows, func(r pgx.CollectableRow) (bomOpNode, error) {
			var x bomOpNode
			var m, cph string
			err := r.Scan(&x.Name, &x.Workcenter, &m, &cph)
			md, _ := decimal.NewFromString(m)
			cd, _ := decimal.NewFromString(cph)
			x.Minutes = md.Mul(factor).Round(2)
			x.Cost = md.Mul(factor).Mul(cd).Div(decimal.NewFromInt(60)).Round(4)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		for _, x := range ops {
			total = total.Add(x.Cost)
		}
		n.Operations = ops
	}
	brows, err := tx.Query(ctx, `SELECT y.item_id::text, it.code, it.name, it.unit, y.quantity::text, y.cost_share::text FROM bom_byproducts y
		JOIN items it ON it.id = y.item_id WHERE y.bom_id = $1 ORDER BY it.code`, bomID)
	if err != nil {
		return nil, err
	}
	bys, err := pgx.CollectRows(brows, func(r pgx.CollectableRow) (bomCompNode, error) {
		var x bomCompNode
		var q, s string
		err := r.Scan(&x.ItemID, &x.ItemCode, &x.ItemName, &x.Unit, &q, &s)
		qd, _ := decimal.NewFromString(q)
		x.Quantity = qd.Mul(factor)
		x.Share, _ = decimal.NewFromString(s)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	shareTotal := decimal.Zero
	for i := range bys {
		bys[i].Cost = total.Mul(bys[i].Share).Div(decimal.NewFromInt(100)).Round(4)
		shareTotal = shareTotal.Add(bys[i].Cost)
	}
	n.Byproducts = bys
	n.Cost = total.Sub(shareTotal)
	if n.Quantity.IsPositive() {
		n.UnitCost = n.Cost.DivRound(n.Quantity, 6)
	}
	return n, nil
}

// bomCost: rolled-up cost of one batch of a BoM (main product's part).
func bomCost(ctx context.Context, tx pgx.Tx, cid, bomID string, depth int) (decimal.Decimal, error) {
	n, err := bomTree(ctx, tx, cid, bomID, decimal.NewFromInt(1), depth)
	if err != nil {
		return decimal.Zero, err
	}
	return n.Cost, nil
}

// GET /mfg/boms/{id}/structure?qty=: the multi-level BoM with costs for qty units of the product.
func (s *Server) mfgBomStructure(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	qtyS := r.URL.Query().Get("qty")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		var out string
		err := tx.QueryRow(ctx, `SELECT output_qty::text FROM boms WHERE id = $1 AND company_id = $2`, id, cid).Scan(&out)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		o, _ := decimal.NewFromString(out)
		factor := decimal.NewFromInt(1)
		if qtyS != "" {
			q, ok := money(qtyS)
			if !ok || !q.IsPositive() {
				return nil, badRequest("invalid_amount")
			}
			factor = q.Div(o)
		}
		return bomTree(ctx, tx, cid, id, factor, 0)
	})
}

// POST /mfg/boms/{id}/set-standard: the product's standard cost becomes the BoM's rolled-up unit cost.
func (s *Server) mfgBomSetStandard(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		n, err := bomTree(ctx, tx, cid, id, decimal.NewFromInt(1), 0)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE items SET standard_cost = $3::numeric WHERE id = $1 AND company_id = $2`, n.ItemID, cid, n.UnitCost.String()); err != nil {
			return nil, err
		}
		return map[string]any{"item_id": n.ItemID, "standard_cost": n.UnitCost}, nil
	})
}

// POST /mfg/standard-cost {item_id, standard_cost}
func (s *Server) mfgSetStandardCost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ItemID       string `json:"item_id"`
		StandardCost string `json:"standard_cost"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	c, ok := money(in.StandardCost)
	if !ok || c.IsNegative() || !isUUID(in.ItemID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		it, err := loadItem(ctx, tx, cid, in.ItemID)
		if err != nil {
			return nil, err
		}
		if it.Kind != "stock" {
			return nil, badRequest("not_stock_item")
		}
		if _, err := tx.Exec(ctx, `UPDATE items SET standard_cost = $3::numeric WHERE id = $1 AND company_id = $2`, it.ID, cid, c.String()); err != nil {
			return nil, err
		}
		return map[string]any{"item_id": it.ID, "standard_cost": c}, nil
	})
}

// ---------- confirm, reserve ----------

// mfgWoReadyFirst: the first pending work order of an order (lowest sequence) becomes ready.
func mfgWoReadyFirst(ctx context.Context, tx pgx.Tx, order string) error {
	_, err := tx.Exec(ctx, `UPDATE mfg_workorders SET status = 'ready' WHERE id = (SELECT id FROM mfg_workorders WHERE order_id = $1 AND status = 'pending'
		ORDER BY seq LIMIT 1) AND NOT EXISTS (SELECT 1 FROM mfg_workorders WHERE order_id = $1 AND status IN ('ready', 'progress', 'paused'))`, order)
	return err
}

// mfgCreateChecks: quality checks of the order (production points) and of each work order (operation points).
func mfgCreateChecks(ctx context.Context, tx pgx.Tx, cid, order string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO mfg_quality_checks (company_id, point_id, order_id, item_id)
		SELECT $1, p.id, o.id, o.item_id FROM mfg_orders o JOIN mfg_quality_points p ON p.company_id = $1 AND p.is_active AND p.trigger_on = 'production'
		AND (p.item_id IS NULL OR p.item_id = o.item_id) WHERE o.id = $2`, cid, order); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO mfg_quality_checks (company_id, point_id, order_id, workorder_id, item_id)
		SELECT $1, p.id, o.id, x.id, o.item_id FROM mfg_workorders x JOIN mfg_orders o ON o.id = x.order_id
		JOIN mfg_quality_points p ON p.company_id = $1 AND p.is_active AND p.trigger_on = 'operation'
		AND (p.workcenter_id IS NULL OR p.workcenter_id = x.workcenter_id) AND (p.item_id IS NULL OR p.item_id = o.item_id)
		WHERE x.order_id = $2 AND x.status NOT IN ('done', 'cancelled')`, cid, order)
	return err
}

// mfgConfirm: draft → confirmed: work orders from the routing, quality checks, components reserved.
func mfgConfirm(ctx context.Context, tx pgx.Tx, cid, id string) error {
	var st, typ, qtyS, outS string
	var routing *string
	err := tx.QueryRow(ctx, `SELECT o.status, o.bom_type, o.routing_id::text, o.quantity::text, b.output_qty::text FROM mfg_orders o JOIN boms b ON b.id = o.bom_id
		WHERE o.id = $1 AND o.company_id = $2 FOR UPDATE OF o`, id, cid).Scan(&st, &typ, &routing, &qtyS, &outS)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return err
	}
	if st != "draft" {
		return &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	qty, _ := decimal.NewFromString(qtyS)
	out, _ := decimal.NewFromString(outS)
	if routing != nil && typ == "normal" {
		if _, err := tx.Exec(ctx, `INSERT INTO mfg_workorders (company_id, order_id, seq, name, workcenter_id, instructions, planned_min)
			SELECT $1, $2, ro.seq, ro.name, ro.workcenter_id, ro.instructions, round(c.setup_min + ro.minutes * $4::numeric * 100 / c.efficiency, 2)
			FROM mfg_routing_ops ro JOIN mfg_workcenters c ON c.id = ro.workcenter_id WHERE ro.routing_id = $3 ORDER BY ro.seq`,
			cid, id, *routing, qty.Div(out).String()); err != nil {
			return err
		}
		if err := mfgWoReadyFirst(ctx, tx, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE mfg_orders SET status = 'confirmed', confirmed_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	if err := mfgCreateChecks(ctx, tx, cid, id); err != nil {
		return err
	}
	return mfgReserve(ctx, tx, cid, id)
}

// mfgReserve: each component line reserves what the warehouse holds and other open orders have not reserved.
func mfgReserve(ctx context.Context, tx pgx.Tx, cid, id string) error {
	rows, err := tx.Query(ctx, `SELECT l.id::text, l.item_id::text, o.warehouse_id::text, (l.planned_qty - l.sent_qty)::text FROM mfg_order_lines l
		JOIN mfg_orders o ON o.id = l.order_id WHERE l.order_id = $1 AND o.status IN ('confirmed', 'progress') AND NOT o.components_sent ORDER BY l.line_no`, id)
	if err != nil {
		return err
	}
	type ln struct{ id, item, wh, need string }
	ls, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ln, error) {
		var x ln
		err := r.Scan(&x.id, &x.item, &x.wh, &x.need)
		return x, err
	})
	if err != nil {
		return err
	}
	for _, l := range ls {
		have, _, err := lockStock(ctx, tx, l.item, l.wh)
		if err != nil {
			return err
		}
		var otherS string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(l.reserved_qty), 0)::text FROM mfg_order_lines l JOIN mfg_orders o ON o.id = l.order_id
			WHERE l.item_id = $1 AND o.warehouse_id = $2 AND o.status IN ('confirmed', 'progress') AND l.id <> $3`, l.item, l.wh, l.id).Scan(&otherS); err != nil {
			return err
		}
		other, _ := decimal.NewFromString(otherS)
		need, _ := decimal.NewFromString(l.need)
		avail := decimal.Max(have.Sub(other), decimal.Zero)
		res := decimal.Max(decimal.Min(need, avail), decimal.Zero)
		if _, err := tx.Exec(ctx, `UPDATE mfg_order_lines SET reserved_qty = $2::numeric WHERE id = $1`, l.id, res.String()); err != nil {
			return err
		}
	}
	return nil
}

// POST /mfg/orders/{id}/confirm · /reserve
func (s *Server) mfgOrderAction(w http.ResponseWriter, r *http.Request) {
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if action != "confirm" && action != "reserve" && action != "unreserve" {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		var st string
		err := tx.QueryRow(ctx, `SELECT status FROM mfg_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		switch action {
		case "confirm":
			err = mfgConfirm(ctx, tx, cid, id)
		case "reserve":
			if st != "confirmed" && st != "progress" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			err = mfgReserve(ctx, tx, cid, id)
		case "unreserve":
			if st != "confirmed" && st != "progress" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			_, err = tx.Exec(ctx, `UPDATE mfg_order_lines SET reserved_qty = 0 WHERE order_id = $1`, id)
		}
		if err != nil {
			return nil, err
		}
		return loadMfgOrder(ctx, tx, cid, id)
	})
}

// POST /mfg/orders/{id}/scrap {item_id, quantity, reason, date}: scrap during production (components or the product)
func (s *Server) mfgOrderScrap(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ItemID   string `json:"item_id"`
		Quantity string `json:"quantity"`
		Reason   string `json:"reason"`
		Date     string `json:"date"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		var st, wh, item string
		var subWh *string
		err := tx.QueryRow(ctx, `SELECT status, warehouse_id::text, item_id::text, subcontract_wh_id::text FROM mfg_orders WHERE id = $1 AND company_id = $2`, id, cid).
			Scan(&st, &wh, &item, &subWh)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st == "cancelled" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.ItemID != item && subWh != nil {
			wh = *subWh // a component at the subcontractor's
		}
		docID, err := scrapCore(ctx, tx, cid, in.ItemID, wh, in.Quantity, in.Date, in.Reason, id)
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+stockDocCols+stockDocFrom+` WHERE d.id = $1`, docID)
	})
}

// ---------- subcontracting ----------

// subcontractorWarehouse: the warehouse holding our components at a subcontractor (created on first use).
func subcontractorWarehouse(ctx context.Context, tx pgx.Tx, cid, supplier string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM warehouses WHERE company_id = $1 AND subcontractor_id = $2`, cid, supplier).Scan(&id)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM suppliers WHERE id = $1 AND company_id = $2`, supplier, cid).Scan(&name); err != nil {
		return "", err
	}
	wname := "SUB · " + name
	for i := 2; i < 50; i++ {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM warehouses WHERE company_id = $1 AND name = $2)`, cid, wname).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			break
		}
		wname = fmt.Sprintf("SUB · %s (%d)", name, i)
	}
	err = tx.QueryRow(ctx, `INSERT INTO warehouses (company_id, name, code, subcontractor_id) VALUES ($1, $2, 'SUB', $3) RETURNING id::text`, cid, wname, supplier).Scan(&id)
	return id, err
}

// POST /mfg/orders/{id}/send-components {date}: the components move to the subcontractor's warehouse (same value).
func (s *Server) mfgSendComponents(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date string `json:"date"`
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
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		var st, typ, wh string
		var number int64
		var sent bool
		var sub *string
		err := tx.QueryRow(ctx, `SELECT o.status, o.bom_type, o.warehouse_id::text, o.number, o.components_sent, b.subcontractor_id::text
			FROM mfg_orders o JOIN boms b ON b.id = o.bom_id WHERE o.id = $1 AND o.company_id = $2 FOR UPDATE OF o`, id, cid).Scan(&st, &typ, &wh, &number, &sent, &sub)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if typ != "subcontract" || sub == nil {
			return nil, badRequest("not_subcontract")
		}
		if sent || (st != "confirmed" && st != "progress") {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		subWh, err := subcontractorWarehouse(ctx, tx, cid, *sub)
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, item_id::text, planned_qty::text FROM mfg_order_lines WHERE order_id = $1 ORDER BY line_no`, id)
		if err != nil {
			return nil, err
		}
		type ln struct{ id, item, q string }
		ls, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ln, error) {
			var x ln
			err := r.Scan(&x.id, &x.item, &x.q)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		memo := fmt.Sprintf("Subcontracting MO #%d · components sent", number)
		for _, l := range ls {
			q, _ := decimal.NewFromString(l.q)
			if !q.IsPositive() {
				continue
			}
			it, err := loadItem(ctx, tx, cid, l.item)
			if err != nil {
				return nil, err
			}
			have, avg, err := lockStock(ctx, tx, l.item, wh)
			if err != nil {
				return nil, err
			}
			if have.LessThan(q) {
				return nil, insufficientStock(it.Name, have)
			}
			v, err := outValue(ctx, tx, l.item, wh, have, avg, q)
			if err != nil {
				return nil, err
			}
			cctx := withLotCarry(ctx)
			if err := stockOut(cctx, tx, cid, l.item, wh, q, avg, v, in.Date, "mfg_sub_out", id, nil, memo); err != nil {
				return nil, err
			}
			if err := stockIn(cctx, tx, cid, l.item, subWh, q, v, in.Date, "mfg_sub_in", id, nil, memo); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE mfg_order_lines SET sent_qty = planned_qty, reserved_qty = 0 WHERE id = $1`, l.id); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE mfg_orders SET components_sent = true, subcontract_wh_id = $2, status = 'progress' WHERE id = $1`, id, subWh); err != nil {
			return nil, err
		}
		return loadMfgOrder(ctx, tx, cid, id)
	})
}

// ---------- work orders and the shop floor ----------

const woCols = `x.id, x.order_id, o.number AS order_number, x.seq, x.name, x.workcenter_id, c.code AS workcenter_code, c.name AS workcenter, x.instructions,
	trim_scale(x.planned_min)::text AS planned_min, trim_scale(x.actual_min)::text AS actual_min, x.status, trim_scale(x.qty_done)::text AS qty_done,
	x.cost::text AS cost, x.operator_id, (SELECT u.full_name FROM users u WHERE u.id = x.operator_id) AS operator, x.started_at, x.finished_at,
	o.item_id, it.code AS item_code, it.name AS item_name, it.unit, trim_scale(o.quantity)::text AS quantity, o.status AS order_status, o.deadline,
	(SELECT min(t.started_at) FROM mfg_wo_times t WHERE t.workorder_id = x.id AND t.ended_at IS NULL) AS running_since,
	(SELECT trim_scale(round(coalesce(sum(CASE WHEN t.ended_at IS NULL THEN extract(epoch FROM now() - t.started_at)::numeric / 60 ELSE t.minutes END), 0), 2))::text
		FROM mfg_wo_times t WHERE t.workorder_id = x.id) AS worked_min,
	(SELECT count(*) FROM mfg_quality_checks q WHERE q.workorder_id = x.id AND q.status = 'todo') AS checks_todo`

const woFrom = ` FROM mfg_workorders x JOIN mfg_orders o ON o.id = x.order_id JOIN mfg_workcenters c ON c.id = x.workcenter_id JOIN items it ON it.id = o.item_id`

// GET /mfg/workorders?workcenter_id=&status=open|all|<status>&order_id=
func (s *Server) mfgWorkorders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wc, st, ord := q.Get("workcenter_id"), defaultStr(q.Get("status"), "open"), q.Get("order_id")
	if (wc != "" && !isUUID(wc)) || (ord != "" && !isUUID(ord)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+woCols+woFrom+` WHERE x.company_id = $1 AND ($2 = '' OR x.workcenter_id = nullif($2, '')::uuid)
			AND ($4 = '' OR x.order_id = nullif($4, '')::uuid)
			AND ($3 = 'all' OR ($3 = 'open' AND x.status IN ('ready', 'progress', 'paused', 'pending')) OR x.status = $3)
			ORDER BY CASE x.status WHEN 'progress' THEN 0 WHEN 'paused' THEN 1 WHEN 'ready' THEN 2 WHEN 'pending' THEN 3 ELSE 4 END,
			o.deadline NULLS LAST, o.number, x.seq LIMIT 500`, r.PathValue("cid"), wc, st, ord)
	})
}

func loadWorkorder(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	wo, err := queryJSONOne(ctx, tx, `SELECT `+woCols+woFrom+` WHERE x.id = $1 AND x.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	checks, err := queryJSON(ctx, tx, `SELECT `+qcCols+qcFrom+` WHERE q.workorder_id = $1 ORDER BY q.created_at`, id)
	if err != nil {
		return nil, err
	}
	comps, err := queryJSON(ctx, tx, `SELECT l.id, l.item_id, it.code AS item_code, it.name AS item_name, it.unit, trim_scale(l.planned_qty)::text AS planned_qty,
		trim_scale(l.reserved_qty)::text AS reserved_qty FROM mfg_order_lines l JOIN items it ON it.id = l.item_id
		WHERE l.order_id = (SELECT order_id FROM mfg_workorders WHERE id = $1) ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	times, err := queryJSON(ctx, tx, `SELECT t.id, t.user_id, (SELECT u.full_name FROM users u WHERE u.id = t.user_id) AS user_name, t.started_at, t.ended_at,
		trim_scale(t.minutes)::text AS minutes FROM mfg_wo_times t WHERE t.workorder_id = $1 ORDER BY t.started_at`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"workorder": wo, "checks": checks, "components": comps, "times": times}, nil
}

func (s *Server) mfgGetWorkorder(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadWorkorder(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// POST /mfg/workorders/{id}/{start|pause|finish} (finish: {minutes, qty_done}: minutes typed by the operator replace the timer)
func (s *Server) mfgWorkorderAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Minutes string `json:"minutes"`
		QtyDone string `json:"qty_done"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if action != "start" && action != "pause" && action != "finish" {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	mins, ok1 := money(defaultStr(in.Minutes, "0"))
	qd, ok2 := money(defaultStr(in.QtyDone, "0"))
	if !ok1 || !ok2 || mins.IsNegative() || qd.IsNegative() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		var st, ost, order, oqty string
		err := tx.QueryRow(ctx, `SELECT x.status, o.status, o.id::text, o.quantity::text FROM mfg_workorders x JOIN mfg_orders o ON o.id = x.order_id
			WHERE x.id = $1 AND x.company_id = $2 FOR UPDATE OF x, o`, id, cid).Scan(&st, &ost, &order, &oqty)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if ost != "confirmed" && ost != "progress" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		closeTimes := func() error {
			_, err := tx.Exec(ctx, `UPDATE mfg_wo_times SET ended_at = now(), minutes = round(extract(epoch FROM now() - started_at)::numeric / 60, 2)
				WHERE workorder_id = $1 AND ended_at IS NULL`, id)
			return err
		}
		allDone := false
		switch action {
		case "start":
			if st != "pending" && st != "ready" && st != "paused" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO mfg_wo_times (company_id, workorder_id) VALUES ($1, $2)`, cid, id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE mfg_workorders SET status = 'progress', started_at = coalesce(started_at, now()), operator_id = app_user() WHERE id = $1`, id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE mfg_orders SET status = 'progress' WHERE id = $1`, order); err != nil {
				return nil, err
			}
		case "pause":
			if st != "progress" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			if err := closeTimes(); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE mfg_workorders SET status = 'paused' WHERE id = $1`, id); err != nil {
				return nil, err
			}
		case "finish":
			if st == "done" || st == "cancelled" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			var todo int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM mfg_quality_checks WHERE workorder_id = $1 AND status = 'todo'`, id).Scan(&todo); err != nil {
				return nil, err
			}
			if todo > 0 {
				return nil, &apiError{status: http.StatusConflict, code: "quality_pending", detail: fmt.Sprint(todo)}
			}
			if err := closeTimes(); err != nil {
				return nil, err
			}
			done := qd
			if !done.IsPositive() {
				done, _ = decimal.NewFromString(oqty)
			}
			if _, err := tx.Exec(ctx, `UPDATE mfg_workorders x SET status = 'done', finished_at = now(), qty_done = $2::numeric, started_at = coalesce(started_at, now()),
				operator_id = coalesce(operator_id, app_user()),
				actual_min = CASE WHEN $3::numeric > 0 THEN $3::numeric ELSE (SELECT coalesce(sum(t.minutes), 0) FROM mfg_wo_times t WHERE t.workorder_id = x.id) END
				WHERE x.id = $1`, id, done.String(), mins.String()); err != nil {
				return nil, err
			}
			if err := mfgWoReadyFirst(ctx, tx, order); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE mfg_orders SET status = 'progress' WHERE id = $1`, order); err != nil {
				return nil, err
			}
			if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM mfg_workorders WHERE order_id = $1 AND status NOT IN ('done', 'cancelled'))`, order).Scan(&allDone); err != nil {
				return nil, err
			}
		}
		wo, err := loadWorkorder(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		m := wo.(map[string]json.RawMessage)
		m["all_done"], _ = json.Marshal(allDone)
		return m, nil
	})
}
