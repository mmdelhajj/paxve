package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// MRP (material requirements planning), manufacturing reports and lot traceability.
//
// MRP per warehouse: for each stock item, projected = on hand + incoming (open purchase orders not billed yet,
// open manufacturing orders of the item) − demand (confirmed sales orders not delivered, components of open
// manufacturing orders not consumed / sent yet). Below the reorder rule's minimum (or below zero without a
// rule) a replenishment is suggested up to the maximum: a manufacturing order when the item has an active BoM
// (its components then become demand of the next level), else a purchase order from the rule's supplier or the
// cheapest supplier price. Items are planned top-down by BoM level, so multi-level products plan their
// sub-assemblies and raw materials in one run.

type mrpLine struct {
	ItemID       string          `json:"item_id"`
	ItemCode     string          `json:"item_code"`
	ItemName     string          `json:"item_name"`
	Unit         string          `json:"unit"`
	Level        int             `json:"level"`
	OnHand       decimal.Decimal `json:"on_hand"`
	Incoming     decimal.Decimal `json:"incoming"`
	Demand       decimal.Decimal `json:"demand"`
	Projected    decimal.Decimal `json:"projected"`
	MinQty       *string         `json:"min_qty"`
	MaxQty       *string         `json:"max_qty"`
	Suggest      decimal.Decimal `json:"suggest"`
	Kind         string          `json:"kind"` // mo | po | ""
	BomID        string          `json:"bom_id,omitempty"`
	SupplierID   string          `json:"supplier_id,omitempty"`
	SupplierName string          `json:"supplier_name,omitempty"`
	UnitPrice    string          `json:"unit_price,omitempty"`
	Currency     string          `json:"currency,omitempty"`
	NeedDate     string          `json:"need_date"`
	OrderDate    string          `json:"order_date"`
	LeadDays     int             `json:"lead_days"`
	Sources      []string        `json:"sources"`
}

func mrpCompute(ctx context.Context, tx pgx.Tx, cid, wh string) ([]*mrpLine, error) {
	td := today()
	items := map[string]*mrpLine{}
	rows, err := tx.Query(ctx, `SELECT it.id::text, it.code, it.name, it.unit, coalesce((SELECT quantity::text FROM stock st WHERE st.item_id = it.id AND st.warehouse_id = $2), '0'),
		rr.min_qty::text, rr.max_qty::text, rr.supplier_id::text
		FROM items it LEFT JOIN reorder_rules rr ON rr.item_id = it.id AND rr.warehouse_id = $2
		WHERE it.company_id = $1 AND it.kind = 'stock' AND it.is_active`, cid, wh)
	if err != nil {
		return nil, err
	}
	ruleSup := map[string]string{}
	for rows.Next() {
		l := &mrpLine{Sources: []string{}, NeedDate: td}
		var oh string
		var sup *string
		if err := rows.Scan(&l.ItemID, &l.ItemCode, &l.ItemName, &l.Unit, &oh, &l.MinQty, &l.MaxQty, &sup); err != nil {
			rows.Close()
			return nil, err
		}
		l.OnHand, _ = decimal.NewFromString(oh)
		if sup != nil {
			ruleSup[l.ItemID] = *sup
		}
		items[l.ItemID] = l
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	addDemand := func(item string, q decimal.Decimal, date, src string) {
		l := items[item]
		if l == nil || !q.IsPositive() {
			return
		}
		l.Demand = l.Demand.Add(q)
		if date != "" && date < l.NeedDate && date >= td {
			l.NeedDate = date
		}
		if len(l.Sources) < 12 {
			l.Sources = append(l.Sources, src)
		}
	}
	type dq struct {
		item, q, date, src string
	}
	collect := func(sql string, args ...any) ([]dq, error) {
		rs, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rs, func(r pgx.CollectableRow) (dq, error) {
			var x dq
			err := r.Scan(&x.item, &x.q, &x.date, &x.src)
			return x, err
		})
	}
	// demand: confirmed sales orders not delivered (lines of this warehouse, or without one when it is the default)
	def, err := defaultWarehouse(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	so, err := collect(`SELECT l.item_id::text, (l.quantity - l.qty_delivered)::text, coalesce(o.order_date, o.quote_date)::text, 'SO-' || coalesce(o.order_number, o.number)
		FROM sales_order_lines l JOIN sales_orders o ON o.id = l.order_id
		WHERE o.company_id = $1 AND o.status = 'confirmed' AND l.item_id IS NOT NULL AND l.quantity > l.qty_delivered
		AND coalesce(l.warehouse_id, $3::uuid) = $2::uuid`, cid, wh, def)
	if err != nil {
		return nil, err
	}
	for _, d := range so {
		q, _ := decimal.NewFromString(d.q)
		addDemand(d.item, q, d.date, d.src)
	}
	// demand: components of open manufacturing orders
	mo, err := collect(`SELECT l.item_id::text, (l.planned_qty - l.sent_qty)::text, o.order_date::text, 'MO-' || o.number FROM mfg_order_lines l JOIN mfg_orders o ON o.id = l.order_id
		WHERE o.company_id = $1 AND o.warehouse_id = $2 AND o.status IN ('draft', 'confirmed', 'progress') AND l.planned_qty > l.sent_qty`, cid, wh)
	if err != nil {
		return nil, err
	}
	for _, d := range mo {
		q, _ := decimal.NewFromString(d.q)
		addDemand(d.item, q, d.date, d.src)
	}
	// incoming: open purchase orders (not billed yet) and open manufacturing orders
	in, err := collect(`SELECT l.item_id::text, greatest(l.quantity - l.billed_qty, 0)::text, coalesce(o.expected_date, o.order_date)::text, 'PO-' || o.number
		FROM purchase_order_lines l JOIN purchase_orders o ON o.id = l.order_id
		WHERE o.company_id = $1 AND o.status IN ('rfq', 'sent', 'confirmed', 'partial') AND l.item_id IS NOT NULL AND o.dropship_order_id IS NULL
		AND coalesce(l.warehouse_id, $3::uuid) = $2::uuid
		UNION ALL
		SELECT o.item_id::text, o.quantity::text, o.order_date::text, 'MO-' || o.number FROM mfg_orders o
		WHERE o.company_id = $1 AND o.warehouse_id = $2 AND o.status IN ('draft', 'confirmed', 'progress')`, cid, wh, def)
	if err != nil {
		return nil, err
	}
	for _, d := range in {
		if l := items[d.item]; l != nil {
			q, _ := decimal.NewFromString(d.q)
			l.Incoming = l.Incoming.Add(q)
		}
	}
	// BoMs: the active made-here / subcontracted BoM of each item, and BoM levels (a product before its components)
	type bomInfo struct {
		id    string
		out   decimal.Decimal
		lead  int
		comps []compQty
	}
	boms := map[string]*bomInfo{}
	brs, err := tx.Query(ctx, `SELECT DISTINCT ON (b.item_id) b.item_id::text, b.id::text, b.output_qty::text, b.lead_days FROM boms b
		WHERE b.company_id = $1 AND b.is_active AND b.bom_type IN ('normal', 'subcontract') ORDER BY b.item_id, b.bom_type = 'normal' DESC, b.created_at`, cid)
	if err != nil {
		return nil, err
	}
	for brs.Next() {
		var item, id, out string
		var lead int
		if err := brs.Scan(&item, &id, &out, &lead); err != nil {
			brs.Close()
			return nil, err
		}
		o, _ := decimal.NewFromString(out)
		boms[item] = &bomInfo{id: id, out: o, lead: lead}
	}
	brs.Close()
	for _, b := range boms {
		if err := bomExplode(ctx, tx, cid, b.id, decimal.NewFromInt(1), 0, &b.comps); err != nil {
			return nil, err
		}
	}
	level := map[string]int{}
	for i := 0; i < 25; i++ {
		changed := false
		for item, b := range boms {
			for _, c := range b.comps {
				if level[c.item] < level[item]+1 {
					level[c.item] = level[item] + 1
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	order := make([]*mrpLine, 0, len(items))
	for _, l := range items {
		l.Level = level[l.ItemID]
		order = append(order, l)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].Level != order[j].Level {
			return order[i].Level < order[j].Level
		}
		return order[i].ItemCode < order[j].ItemCode
	})
	addDays := func(date string, days int) string {
		t, err := time.Parse(time.DateOnly, date)
		if err != nil {
			return date
		}
		s := t.AddDate(0, 0, days).Format(time.DateOnly)
		if s < td {
			return td
		}
		return s
	}
	for _, l := range order {
		l.Projected = l.OnHand.Add(l.Incoming).Sub(l.Demand)
		minQ, maxQ := decimal.Zero, decimal.Zero
		hasRule := l.MinQty != nil
		if hasRule {
			minQ, _ = decimal.NewFromString(*l.MinQty)
			maxQ, _ = decimal.NewFromString(*l.MaxQty)
		}
		need := decimal.Zero
		if hasRule && l.Projected.LessThan(minQ) {
			need = maxQ.Sub(l.Projected)
		} else if !hasRule && l.Projected.IsNegative() {
			need = l.Projected.Neg()
		}
		if !need.IsPositive() {
			continue
		}
		l.Suggest = need
		if b := boms[l.ItemID]; b != nil {
			l.Kind, l.BomID, l.LeadDays = "mo", b.id, b.lead
			l.OrderDate = addDays(l.NeedDate, -b.lead)
			batches := need.Div(b.out)
			for _, c := range b.comps {
				addDemand(c.item, c.qty.Mul(batches).Round(4), l.OrderDate, "MRP "+l.ItemCode)
			}
			continue
		}
		l.Kind = "po"
		sup := ruleSup[l.ItemID]
		var price, cur *string
		var lead int
		var supName *string
		err := tx.QueryRow(ctx, `SELECT sp.supplier_id::text, su.name, sp.price::text, sp.currency, sp.lead_days FROM supplier_prices sp JOIN suppliers su ON su.id = sp.supplier_id
			WHERE sp.company_id = $1 AND sp.item_id = $2 AND ($3 = '' OR sp.supplier_id = nullif($3, '')::uuid) AND su.status = 'active'
			AND (sp.valid_from IS NULL OR sp.valid_from <= current_date) AND (sp.valid_to IS NULL OR sp.valid_to >= current_date)
			ORDER BY sp.price, sp.lead_days LIMIT 1`, cid, l.ItemID, sup).Scan(&sup, &supName, &price, &cur, &lead)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if sup != "" && supName == nil {
			_ = tx.QueryRow(ctx, `SELECT name FROM suppliers WHERE id = $1`, sup).Scan(&supName)
		}
		l.SupplierID = sup
		if supName != nil {
			l.SupplierName = *supName
		}
		if price != nil {
			l.UnitPrice, l.Currency = *price, *cur
		}
		l.LeadDays = lead
		l.OrderDate = addDays(l.NeedDate, -lead)
	}
	out := []*mrpLine{}
	for _, l := range order {
		if l.Suggest.IsPositive() || l.Demand.IsPositive() || l.MinQty != nil {
			out = append(out, l)
		}
	}
	return out, nil
}

// GET /mfg/mrp?warehouse_id=
func (s *Server) mfgMRP(w http.ResponseWriter, r *http.Request) {
	whIn := r.URL.Query().Get("warehouse_id")
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		wh, err := checkWarehouse(ctx, tx, cid, whIn)
		if err != nil {
			return nil, err
		}
		lines, err := mrpCompute(ctx, tx, cid, wh)
		if err != nil {
			return nil, err
		}
		return map[string]any{"warehouse_id": wh, "lines": lines}, nil
	})
}

// POST /mfg/mrp/run {warehouse_id, item_ids}: creates the suggested manufacturing orders (draft) and purchase
// orders (requests for quotation, one per supplier). Only the given items when item_ids is set.
func (s *Server) mfgRunMRP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WarehouseID string   `json:"warehouse_id"`
		ItemIDs     []string `json:"item_ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	for _, x := range in.ItemIDs {
		if !isUUID(x) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		wh, err := checkWarehouse(ctx, tx, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		lines, err := mrpCompute(ctx, tx, cid, wh)
		if err != nil {
			return nil, err
		}
		want := map[string]bool{}
		for _, x := range in.ItemIDs {
			want[x] = true
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		mos, pos, skipped := []map[string]any{}, []map[string]any{}, []string{}
		bySup := map[string][]*mrpLine{}
		supOrder := []string{}
		for _, l := range lines {
			if !l.Suggest.IsPositive() || (len(want) > 0 && !want[l.ItemID]) {
				continue
			}
			switch {
			case l.Kind == "mo":
				id, err := mfgCreate(ctx, tx, cid, mfgCreateIn{BomID: l.BomID, Quantity: l.Suggest.Round(4).String(), WarehouseID: wh, Date: l.OrderDate,
					Deadline: l.NeedDate, Origin: "MRP"})
				if err != nil {
					return nil, err
				}
				var n int64
				_ = tx.QueryRow(ctx, `SELECT number FROM mfg_orders WHERE id = $1`, id).Scan(&n)
				mos = append(mos, map[string]any{"id": id, "number": n, "item_code": l.ItemCode, "quantity": l.Suggest})
			case l.SupplierID != "":
				if _, ok := bySup[l.SupplierID]; !ok {
					supOrder = append(supOrder, l.SupplierID)
				}
				bySup[l.SupplierID] = append(bySup[l.SupplierID], l)
			default:
				skipped = append(skipped, l.ItemCode)
			}
		}
		for _, sup := range supOrder {
			ls := bySup[sup]
			var supCur string
			if err := tx.QueryRow(ctx, `SELECT coalesce(currency::text, $2) FROM suppliers WHERE id = $1`, sup, set.Base).Scan(&supCur); err != nil {
				return nil, err
			}
			cur := supCur
			if ls[0].Currency != "" {
				cur = ls[0].Currency
			}
			orderDate := ls[0].OrderDate
			for _, l := range ls {
				if l.OrderDate < orderDate {
					orderDate = l.OrderDate
				}
			}
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'purchase_order')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO purchase_orders (company_id, number, supplier_id, order_date, expected_date, currency, notes)
				VALUES ($1, $2, $3, $4::date, $5::date, $6, 'MRP') RETURNING id::text`, cid, n, sup, today(), ls[0].NeedDate, cur).Scan(&id); err != nil {
				return nil, err
			}
			pl := []poLineIn{}
			for _, l := range ls {
				pl = append(pl, poLineIn{ItemID: l.ItemID, WarehouseID: wh, Quantity: l.Suggest.Round(4).String()})
			}
			if err := writePOLines(ctx, tx, cid, id, cur, pl); err != nil {
				return nil, err
			}
			pos = append(pos, map[string]any{"id": id, "number": n, "supplier_id": sup, "lines": len(ls)})
		}
		return map[string]any{"mos": mos, "pos": pos, "skipped": skipped}, nil
	})
}

// ---------- reports ----------

// GET /mfg/reports/status: orders by status, late orders, open work orders.
func (s *Server) mfgReportStatus(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		counts, err := queryJSON(ctx, tx, `SELECT status, count(*) AS orders, trim_scale(sum(quantity))::text AS quantity FROM mfg_orders WHERE company_id = $1 GROUP BY status ORDER BY status`, cid)
		if err != nil {
			return nil, err
		}
		late, err := queryJSON(ctx, tx, `SELECT `+mfgCols+mfgFrom+` WHERE o.company_id = $1 AND o.status IN ('draft', 'confirmed', 'progress')
			AND o.deadline IS NOT NULL AND o.deadline < current_date ORDER BY o.deadline LIMIT 200`, cid)
		if err != nil {
			return nil, err
		}
		open, err := queryJSON(ctx, tx, `SELECT `+mfgCols+mfgFrom+` WHERE o.company_id = $1 AND o.status IN ('draft', 'confirmed', 'progress')
			ORDER BY o.deadline NULLS LAST, o.number LIMIT 500`, cid)
		if err != nil {
			return nil, err
		}
		var alerts, todo int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM mfg_quality_alerts WHERE company_id = $1 AND stage <> 'done'),
			(SELECT count(*) FROM mfg_quality_checks WHERE company_id = $1 AND status = 'todo')`, cid).Scan(&alerts, &todo); err != nil {
			return nil, err
		}
		return map[string]any{"counts": counts, "late": late, "open": open, "open_alerts": alerts, "checks_todo": todo}, nil
	})
}

// GET /mfg/reports/load?days=10: open work per work center against its capacity over the next days.
func (s *Server) mfgReportLoad(w http.ResponseWriter, r *http.Request) {
	days := 10
	if d := r.URL.Query().Get("days"); d != "" {
		if _, err := fmt.Sscanf(d, "%d", &days); err != nil || days < 1 || days > 365 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT c.id, c.code, c.name, trim_scale(c.capacity)::text AS capacity,
			round(coalesce(sum(greatest(x.planned_min - x.actual_min, 0)) FILTER (WHERE x.status IN ('pending', 'ready', 'progress', 'paused')), 0), 2)::text AS open_min,
			count(x.id) FILTER (WHERE x.status IN ('pending', 'ready', 'progress', 'paused')) AS open_workorders,
			round(c.capacity * c.hours_per_day * 60 * $2::numeric * c.days_per_week / 7, 2)::text AS capacity_min,
			round(100 * coalesce(sum(greatest(x.planned_min - x.actual_min, 0)) FILTER (WHERE x.status IN ('pending', 'ready', 'progress', 'paused')), 0)
				/ nullif(c.capacity * c.hours_per_day * 60 * $2::numeric * c.days_per_week / 7, 0), 1)::text AS load_pct,
			round(coalesce(sum(x.actual_min) FILTER (WHERE x.status = 'done' AND x.finished_at >= now() - interval '30 days'), 0), 2)::text AS done_min_30d
			FROM mfg_workcenters c LEFT JOIN mfg_workorders x ON x.workcenter_id = c.id
			WHERE c.company_id = $1 AND c.is_active GROUP BY c.id ORDER BY c.code`, r.PathValue("cid"), days)
	})
}

// GET /mfg/reports/variance?from=&to=: done orders, expected (planned) cost against actual, and the variance posted against standard.
func (s *Server) mfgReportVariance(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := defaultStr(q.Get("from"), "2000-01-01"), defaultStr(q.Get("to"), "2100-01-01")
	if !validDate(from) || !validDate(to) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT o.id, o.number, o.order_date, it.code AS item_code, it.name AS item_name, it.unit,
			trim_scale(o.quantity - o.scrap_qty)::text AS good_qty, trim_scale(o.scrap_qty)::text AS scrap_qty,
			o.components_value::text AS components, (o.labour + o.ops_cost)::text AS labour, o.overhead::text AS overhead, o.service_cost::text AS service,
			o.byproducts_value::text AS byproducts, (o.components_value + o.labour + o.ops_cost + o.overhead + o.service_cost - o.byproducts_value)::text AS actual,
			o.expected_cost::text AS expected, (o.components_value + o.labour + o.ops_cost + o.overhead + o.service_cost - o.byproducts_value - o.expected_cost)::text AS diff_expected,
			trim_scale(o.standard_cost)::text AS standard_cost, o.variance::text AS variance, trim_scale(o.unit_cost)::text AS unit_cost
			FROM mfg_orders o JOIN items it ON it.id = o.item_id WHERE o.company_id = $1 AND o.status = 'done' AND o.order_date BETWEEN $2::date AND $3::date
			ORDER BY o.order_date DESC, o.number DESC LIMIT 2000`, r.PathValue("cid"), from, to)
	})
}

// GET /mfg/reports/shortages: components of open orders that are not reserved, with what is on hand and on order.
func (s *Server) mfgReportShortages(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT o.id AS order_id, o.number AS order_number, o.status, o.order_date, o.deadline, w.name AS warehouse,
			l.item_id, it.code AS item_code, it.name AS item_name, it.unit, trim_scale(l.planned_qty - l.sent_qty)::text AS required,
			trim_scale(l.reserved_qty)::text AS reserved, trim_scale(greatest(l.planned_qty - l.sent_qty - l.reserved_qty, 0))::text AS missing,
			coalesce((SELECT trim_scale(st.quantity)::text FROM stock st WHERE st.item_id = l.item_id AND st.warehouse_id = o.warehouse_id), '0') AS on_hand,
			(SELECT trim_scale(coalesce(sum(greatest(pl.quantity - pl.billed_qty, 0)), 0))::text FROM purchase_order_lines pl JOIN purchase_orders po ON po.id = pl.order_id
				WHERE pl.item_id = l.item_id AND po.company_id = o.company_id AND po.status IN ('rfq', 'sent', 'confirmed', 'partial')) AS on_order
			FROM mfg_order_lines l JOIN mfg_orders o ON o.id = l.order_id JOIN items it ON it.id = l.item_id JOIN warehouses w ON w.id = o.warehouse_id
			WHERE o.company_id = $1 AND o.status IN ('draft', 'confirmed', 'progress') AND NOT o.components_sent
			AND l.planned_qty - l.sent_qty > l.reserved_qty
			AND (o.status <> 'draft' OR l.planned_qty > coalesce((SELECT st.quantity FROM stock st WHERE st.item_id = l.item_id AND st.warehouse_id = o.warehouse_id), 0))
			ORDER BY o.deadline NULLS LAST, o.number, l.line_no LIMIT 2000`, r.PathValue("cid"))
	})
}

// ---------- traceability ----------

type traceNode struct {
	LotID    string        `json:"lot_id"`
	LotNo    string        `json:"lot_no"`
	ItemID   string        `json:"item_id"`
	ItemCode string        `json:"item_code"`
	ItemName string        `json:"item_name"`
	Expiry   *string       `json:"expiry"`
	Events   []traceEvent  `json:"events"`
	Orders   []traceOrder  `json:"orders"`
	Untraced []traceUnlotd `json:"untraced,omitempty"`
}

type traceEvent struct {
	Date     string `json:"date"`
	Source   string `json:"source"`
	Ref      string `json:"ref"`
	Partner  string `json:"partner"`
	Quantity string `json:"quantity"`
}

type traceOrder struct {
	OrderID string       `json:"order_id"`
	Number  int64        `json:"number"`
	Date    string       `json:"date"`
	Lots    []*traceNode `json:"lots"`
}

type traceUnlotd struct {
	ItemCode string `json:"item_code"`
	ItemName string `json:"item_name"`
	Quantity string `json:"quantity"`
}

func traceLot(ctx context.Context, tx pgx.Tx, cid, lot string, forward bool, depth int, seen map[string]bool) (*traceNode, error) {
	n := &traceNode{LotID: lot, Events: []traceEvent{}, Orders: []traceOrder{}}
	if err := tx.QueryRow(ctx, `SELECT l.lot_no, l.item_id::text, it.code, it.name, l.expiry::text FROM stock_lots l JOIN items it ON it.id = l.item_id
		WHERE l.id = $1 AND l.company_id = $2`, lot, cid).Scan(&n.LotNo, &n.ItemID, &n.ItemCode, &n.ItemName, &n.Expiry); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		return nil, err
	}
	if seen[lot] || depth > 8 {
		return n, nil
	}
	seen[lot] = true
	// where this lot came from (backward) or went (forward), outside manufacturing
	dir := "m.quantity > 0"
	if forward {
		dir = "m.quantity < 0"
	}
	evs, err := tx.Query(ctx, `SELECT m.move_date::text, m.source, coalesce(CASE m.source
			WHEN 'bill' THEN (SELECT 'BILL-' || b.number FROM bills b WHERE b.id = m.source_id)
			WHEN 'sale' THEN (SELECT 'INV-' || i.number FROM invoices i WHERE i.id = m.source_id)
			WHEN 'delivery' THEN (SELECT 'DEL-' || d.number FROM sales_deliveries d WHERE d.id = m.source_id)
			WHEN 'pos' THEN (SELECT 'POS-' || p.number FROM pos_orders p WHERE p.id = m.source_id) END, ''),
		coalesce(CASE m.source
			WHEN 'bill' THEN (SELECT s.name FROM bills b JOIN suppliers s ON s.id = b.supplier_id WHERE b.id = m.source_id)
			WHEN 'sale' THEN (SELECT c.name FROM invoices i JOIN customers c ON c.id = i.customer_id WHERE i.id = m.source_id)
			WHEN 'delivery' THEN (SELECT c.name FROM sales_deliveries d JOIN sales_orders o ON o.id = d.order_id JOIN customers c ON c.id = o.customer_id WHERE d.id = m.source_id)
			WHEN 'pos' THEN (SELECT c.name FROM pos_orders p JOIN customers c ON c.id = p.customer_id WHERE p.id = m.source_id) END, ''),
		trim_scale(abs(ml.quantity))::text
		FROM stock_move_lots ml JOIN stock_moves m ON m.id = ml.move_id
		WHERE ml.lot_id = $1 AND `+dir+` AND m.source NOT IN ('mfg_in', 'mfg_out', 'mfg_sub_in', 'mfg_sub_out', 'transfer') ORDER BY m.move_date, m.created_at`, lot)
	if err != nil {
		return nil, err
	}
	n.Events, err = pgx.CollectRows(evs, func(r pgx.CollectableRow) (traceEvent, error) {
		var e traceEvent
		err := r.Scan(&e.Date, &e.Source, &e.Ref, &e.Partner, &e.Quantity)
		return e, err
	})
	if err != nil {
		return nil, err
	}
	// manufacturing orders that made this lot (backward) / consumed it (forward)
	src := "mfg_in"
	if forward {
		src = "mfg_out"
	}
	ors, err := tx.Query(ctx, `SELECT DISTINCT o.id::text, o.number, o.order_date::text FROM stock_move_lots ml JOIN stock_moves m ON m.id = ml.move_id
		JOIN mfg_orders o ON o.id = m.source_id WHERE ml.lot_id = $1 AND m.source = $2 ORDER BY o.number`, lot, src)
	if err != nil {
		return nil, err
	}
	type ord struct {
		id, date string
		n        int64
	}
	os, err := pgx.CollectRows(ors, func(r pgx.CollectableRow) (ord, error) {
		var x ord
		err := r.Scan(&x.id, &x.n, &x.date)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	other := "mfg_out"
	if forward {
		other = "mfg_in"
	}
	for _, o := range os {
		to := traceOrder{OrderID: o.id, Number: o.n, Date: o.date, Lots: []*traceNode{}}
		lrs, err := tx.Query(ctx, `SELECT DISTINCT ml.lot_id::text FROM stock_move_lots ml JOIN stock_moves m ON m.id = ml.move_id
			WHERE m.source = $2 AND m.source_id = $1`, o.id, other)
		if err != nil {
			return nil, err
		}
		lots, err := pgx.CollectRows(lrs, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		for _, l := range lots {
			c, err := traceLot(ctx, tx, cid, l, forward, depth+1, seen)
			if err != nil {
				return nil, err
			}
			to.Lots = append(to.Lots, c)
		}
		if !forward { // components without lots, for the full picture
			urs, err := tx.Query(ctx, `SELECT it.code, it.name, trim_scale(abs(m.quantity))::text FROM stock_moves m JOIN items it ON it.id = m.item_id
				WHERE m.source = 'mfg_out' AND m.source_id = $1 AND NOT EXISTS (SELECT 1 FROM stock_move_lots ml WHERE ml.move_id = m.id) ORDER BY it.code`, o.id)
			if err != nil {
				return nil, err
			}
			un, err := pgx.CollectRows(urs, func(r pgx.CollectableRow) (traceUnlotd, error) {
				var u traceUnlotd
				err := r.Scan(&u.ItemCode, &u.ItemName, &u.Quantity)
				return u, err
			})
			if err != nil {
				return nil, err
			}
			n.Untraced = append(n.Untraced, un...)
		}
		n.Orders = append(n.Orders, to)
	}
	return n, nil
}

// GET /mfg/trace?q=lot-or-serial · ?lot_id=: lots matching q, or the backward (what it is made of, where from)
// and forward (what was made with it, who received it) trace of one lot.
func (s *Server) mfgTrace(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lot, text := q.Get("lot_id"), strings.TrimSpace(q.Get("q"))
	if lot != "" && !isUUID(lot) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if lot == "" {
			if text == "" {
				return map[string]any{"matches": []any{}}, nil
			}
			m, err := queryJSON(ctx, tx, `SELECT l.id, l.lot_no, l.expiry, it.code AS item_code, it.name AS item_name, it.tracking FROM stock_lots l
				JOIN items it ON it.id = l.item_id WHERE l.company_id = $1 AND (l.lot_no ILIKE '%' || $2 || '%') ORDER BY l.lot_no LIMIT 50`, cid, text)
			if err != nil {
				return nil, err
			}
			return map[string]json.RawMessage{"matches": m}, nil
		}
		back, err := traceLot(ctx, tx, cid, lot, false, 0, map[string]bool{})
		if err != nil {
			return nil, err
		}
		fwd, err := traceLot(ctx, tx, cid, lot, true, 0, map[string]bool{})
		if err != nil {
			return nil, err
		}
		return map[string]any{"backward": back, "forward": fwd}, nil
	})
}
