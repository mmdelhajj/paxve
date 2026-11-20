package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Warehouse pro (continued): dropship, stock reports (bins, slow / dead stock, ABC, ageing, valuation tie-out),
// vehicles, delivery trips (the delivery board) and proof of delivery from the driver's phone.

// ======================= dropship =======================

// transitWarehouse: the company's "Dropship" warehouse (goods bought for a customer pass through it on paper only).
func transitWarehouse(ctx context.Context, tx pgx.Tx, cid string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM warehouses WHERE company_id = $1 AND is_transit ORDER BY created_at LIMIT 1`, cid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := defaultWarehouse(ctx, tx, cid); err != nil { // the main warehouse stays the first one
			return "", err
		}
		err = tx.QueryRow(ctx, `INSERT INTO warehouses (company_id, name, code, is_transit) VALUES ($1, 'Dropship', 'DS', true)
			ON CONFLICT (tenant_id, company_id, name) DO UPDATE SET is_transit = true RETURNING id::text`, cid).Scan(&id)
	}
	return id, err
}

// POST /wh/dropship {order_id, supplier_id, lines: [{line_id, quantity, unit_price}]}: a purchase order shipped by
// the supplier straight to the customer. When the supplier bills it, the sales order is delivered automatically.
func (s *Server) whDropship(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OrderID    string `json:"order_id"`
		SupplierID string `json:"supplier_id"`
		Currency   string `json:"currency"`
		Notes      string `json:"notes"`
		Lines      []struct {
			LineID    string `json:"line_id"`
			Quantity  string `json:"quantity"`
			UnitPrice string `json:"unit_price"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(in.OrderID) || !isUUID(in.SupplierID) || len(in.Lines) > 200 || len(in.Notes) > 2000 || (in.Currency != "" && !currencyRe.MatchString(in.Currency)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		st, err := lockOrder(ctx, tx, cid, in.OrderID)
		if err != nil {
			return nil, err
		}
		if st != "confirmed" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := lockSupplier(ctx, tx, cid, in.SupplierID); err != nil {
			return nil, err
		}
		if err := whRefuseDirectDelivery(ctx, tx, cid, in.OrderID); err != nil {
			return nil, err
		}
		tr, err := transitWarehouse(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var cur, addr, cust string
		var soN int64
		if err := tx.QueryRow(ctx, `SELECT o.currency, c.name, c.address, coalesce(o.order_number, o.number) FROM sales_orders o
			JOIN customers c ON c.id = o.customer_id WHERE o.id = $1`, in.OrderID).Scan(&cur, &cust, &addr, &soN); err != nil {
			return nil, err
		}
		if in.Currency != "" {
			cur = in.Currency
		}
		type sl struct {
			id, item, desc string
			open           decimal.Decimal
		}
		rows, err := tx.Query(ctx, `SELECT l.id::text, l.item_id::text, l.description, (l.quantity - l.qty_delivered)::text FROM sales_order_lines l
			JOIN items it ON it.id = l.item_id WHERE l.order_id = $1 AND it.kind = 'stock' ORDER BY l.line_no FOR UPDATE OF l`, in.OrderID)
		if err != nil {
			return nil, err
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sl, error) {
			var x sl
			var q string
			err := row.Scan(&x.id, &x.item, &x.desc, &q)
			x.open, _ = decimal.NewFromString(q)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		byID := map[string]sl{}
		for _, x := range list {
			byID[x.id] = x
		}
		want := in.Lines
		if len(want) == 0 {
			for _, x := range list {
				if x.open.IsPositive() {
					want = append(want, struct {
						LineID    string `json:"line_id"`
						Quantity  string `json:"quantity"`
						UnitPrice string `json:"unit_price"`
					}{x.id, x.open.String(), ""})
				}
			}
		}
		if len(want) == 0 {
			return nil, badRequest("nothing_to_order")
		}
		lines := []poLineIn{}
		soLines := []string{}
		for _, l := range want {
			x, ok := byID[l.LineID]
			q, okq := money(l.Quantity)
			if !ok || !okq || !q.IsPositive() {
				return nil, badRequest("invalid_line")
			}
			if q.GreaterThan(x.open) {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "quantity_exceeds_order", detail: x.desc}
			}
			if strings.TrimSpace(l.UnitPrice) != "" {
				if p, ok := money(l.UnitPrice); !ok || p.IsNegative() {
					return nil, badRequest("invalid_amount")
				}
			}
			lines = append(lines, poLineIn{ItemID: x.item, WarehouseID: tr, Description: x.desc, Quantity: q.String(), UnitPrice: strings.TrimSpace(l.UnitPrice)})
			soLines = append(soLines, x.id)
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'purchase_order')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		notes := strings.TrimSpace(in.Notes)
		if notes == "" {
			notes = fmt.Sprintf("Dropship for SO #%d", soN)
		}
		var po string
		if err := tx.QueryRow(ctx, `INSERT INTO purchase_orders (company_id, number, supplier_id, order_date, currency, notes, dropship_order_id, ship_to)
			VALUES ($1, $2, $3, current_date, $4, $5, $6, $7) RETURNING id::text`, cid, n, in.SupplierID, cur, notes, in.OrderID,
			strings.TrimSpace(cust+"\n"+addr)).Scan(&po); err != nil {
			return nil, err
		}
		if err := writePOLines(ctx, tx, cid, po, cur, lines); err != nil {
			return nil, err
		}
		for i, so := range soLines {
			if _, err := tx.Exec(ctx, `UPDATE purchase_order_lines SET so_line_id = $3 WHERE order_id = $1 AND line_no = $2`, po, i+1, so); err != nil {
				return nil, err
			}
			// the order line is delivered from the dropship warehouse
			if _, err := tx.Exec(ctx, `UPDATE sales_order_lines SET warehouse_id = $2 WHERE id = $1`, so, tr); err != nil {
				return nil, err
			}
		}
		return loadPO(ctx, tx, cid, po)
	})
}

// whDropshipAfterBill: the supplier billed a dropship order — what is now in the dropship warehouse goes to the customer.
func whDropshipAfterBill(ctx context.Context, tx pgx.Tx, r *http.Request, cid, po, date string) error {
	var so *string
	if err := tx.QueryRow(ctx, `SELECT dropship_order_id::text FROM purchase_orders WHERE id = $1`, po).Scan(&so); err != nil || so == nil {
		return err
	}
	if date == "" {
		date = today()
	}
	rows, err := tx.Query(ctx, `SELECT pl.so_line_id::text, least(pl.billed_qty * coalesce(u.factor, 1), sl.quantity - sl.qty_delivered,
		coalesce((SELECT st.quantity FROM stock st WHERE st.item_id = pl.item_id AND st.warehouse_id = pl.warehouse_id), 0))::text
		FROM purchase_order_lines pl JOIN sales_order_lines sl ON sl.id = pl.so_line_id LEFT JOIN item_units u ON u.id = pl.unit_id
		WHERE pl.order_id = $1 AND pl.so_line_id IS NOT NULL`, po)
	if err != nil {
		return err
	}
	type x struct{ line, q string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (x, error) {
		var v x
		err := row.Scan(&v.line, &v.q)
		return v, err
	})
	if err != nil {
		return err
	}
	req := []qtyIn{}
	for _, v := range list {
		if d, _ := decimal.NewFromString(v.q); d.IsPositive() {
			req = append(req, qtyIn{LineID: v.line, Quantity: d.String()})
		}
	}
	if len(req) == 0 {
		return nil
	}
	var st string
	if err := tx.QueryRow(ctx, `SELECT status FROM sales_orders WHERE id = $1`, *so).Scan(&st); err != nil || st != "confirmed" {
		return err
	}
	_, err = deliverOrderTx(ctx, tx, r, cid, *so, date, "Dropship", req)
	return err
}

// ======================= reports =======================

// GET /wh/reports/slow?days=90&dead_days=365&warehouse_id=: stock that does not move.
func (s *Server) whSlowStock(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days, dead := atoiDef(q.Get("days"), 90), atoiDef(q.Get("dead_days"), 365)
	wh := q.Get("warehouse_id")
	if days < 1 || days > 3650 || dead < days || dead > 7300 || (wh != "" && !isUUID(wh)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT * FROM (
			SELECT it.id AS item_id, it.code, it.name, it.unit, w.id AS warehouse_id, w.name AS warehouse, trim_scale(st.quantity)::text AS quantity,
			st.value::text AS value, mv.last_out, mv.last_in, (current_date - coalesce(mv.last_out, mv.first_in, current_date))::int AS idle_days
			FROM stock st JOIN items it ON it.id = st.item_id JOIN warehouses w ON w.id = st.warehouse_id
			LEFT JOIN LATERAL (SELECT max(m.move_date) FILTER (WHERE m.quantity < 0 AND m.source NOT IN ('transfer', 'bill_void')) AS last_out,
				max(m.move_date) FILTER (WHERE m.quantity > 0) AS last_in, min(m.move_date) AS first_in
				FROM stock_moves m WHERE m.item_id = st.item_id AND m.warehouse_id = st.warehouse_id) mv ON true
			WHERE w.company_id = $1 AND st.quantity > 0 AND ($2 = '' OR w.id = nullif($2, '')::uuid)) x
			WHERE idle_days >= $3 ORDER BY idle_days DESC, value::numeric DESC LIMIT 2000`, r.PathValue("cid"), wh, days)
	})
}

// GET /wh/reports/abc?months=12: items ranked by what they consumed (A = 80 % of the value, B = next 15 %, C = the rest).
func (s *Server) whABC(w http.ResponseWriter, r *http.Request) {
	months := atoiDef(r.URL.Query().Get("months"), 12)
	if months < 1 || months > 60 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `WITH c AS (
			SELECT m.item_id, sum(-m.value) AS used_value, sum(-m.quantity) AS used_qty, count(*) AS moves
			FROM stock_moves m WHERE m.company_id = $1 AND m.quantity < 0 AND m.source IN ('sale', 'delivery', 'pos', 'fuel')
			AND m.move_date > current_date - make_interval(months => $2) GROUP BY m.item_id HAVING sum(-m.value) > 0),
			r AS (SELECT c.*, sum(c.used_value) OVER (ORDER BY c.used_value DESC, c.item_id) AS cum, sum(c.used_value) OVER () AS total FROM c)
			SELECT it.id AS item_id, it.code, it.name, it.unit, r.used_value::text AS used_value, trim_scale(r.used_qty)::text AS used_qty, r.moves,
			round(100 * r.used_value / r.total, 2)::text AS share, round(100 * r.cum / r.total, 2)::text AS cum_share,
			CASE WHEN r.cum - r.used_value < 0.8 * r.total THEN 'A' WHEN r.cum - r.used_value < 0.95 * r.total THEN 'B' ELSE 'C' END AS class,
			(SELECT trim_scale(coalesce(sum(st.quantity), 0))::text FROM stock st WHERE st.item_id = it.id) AS on_hand
			FROM r JOIN items it ON it.id = r.item_id ORDER BY r.used_value DESC LIMIT 5000`, r.PathValue("cid"), months)
	})
}

// GET /wh/reports/ageing?warehouse_id=: how old the stock held is (newest receipts are assumed still held — FIFO).
func (s *Server) whAgeing(w http.ResponseWriter, r *http.Request) {
	wh := r.URL.Query().Get("warehouse_id")
	if wh != "" && !isUUID(wh) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `WITH st AS (
			SELECT s.item_id, s.warehouse_id, s.quantity, s.value FROM stock s JOIN warehouses w ON w.id = s.warehouse_id
			WHERE w.company_id = $1 AND s.quantity > 0 AND ($2 = '' OR w.id = nullif($2, '')::uuid)),
			ins AS (SELECT m.item_id, m.warehouse_id, m.move_date, m.quantity,
				sum(m.quantity) OVER (PARTITION BY m.item_id, m.warehouse_id ORDER BY m.move_date DESC, m.created_at DESC) AS cum
				FROM stock_moves m JOIN st USING (item_id, warehouse_id) WHERE m.quantity > 0 AND m.source NOT LIKE '%\_void' AND m.source <> 'transfer'),
			alloc AS (SELECT st.item_id, st.warehouse_id, i.move_date, least(i.quantity, st.quantity - (i.cum - i.quantity)) AS q
				FROM st JOIN ins i USING (item_id, warehouse_id) WHERE i.cum - i.quantity < st.quantity),
			b AS (SELECT item_id, warehouse_id,
				coalesce(sum(q) FILTER (WHERE current_date - move_date <= 30), 0) AS d30,
				coalesce(sum(q) FILTER (WHERE current_date - move_date BETWEEN 31 AND 90), 0) AS d90,
				coalesce(sum(q) FILTER (WHERE current_date - move_date BETWEEN 91 AND 180), 0) AS d180,
				coalesce(sum(q) FILTER (WHERE current_date - move_date BETWEEN 181 AND 365), 0) AS d365,
				coalesce(sum(q) FILTER (WHERE current_date - move_date > 365), 0) AS older,
				coalesce(sum(q), 0) AS placed, min(move_date) AS oldest FROM alloc GROUP BY item_id, warehouse_id)
			SELECT it.id AS item_id, it.code, it.name, it.unit, w.name AS warehouse, trim_scale(st.quantity)::text AS quantity, st.value::text AS value,
			trim_scale(coalesce(b.d30, 0))::text AS d30, trim_scale(coalesce(b.d90, 0))::text AS d90, trim_scale(coalesce(b.d180, 0))::text AS d180,
			trim_scale(coalesce(b.d365, 0))::text AS d365, trim_scale(coalesce(b.older, 0) + greatest(st.quantity - coalesce(b.placed, 0), 0))::text AS older,
			b.oldest, (current_date - coalesce(b.oldest, current_date))::int AS oldest_days
			FROM st JOIN items it ON it.id = st.item_id JOIN warehouses w ON w.id = st.warehouse_id LEFT JOIN b USING (item_id, warehouse_id)
			ORDER BY oldest_days DESC, it.code LIMIT 5000`, r.PathValue("cid"), wh)
	})
}

// GET /wh/reports/valuation: value per warehouse, and per inventory account against the ledger (they must agree).
func (s *Server) whValuation(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cid := r.PathValue("cid")
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		whs, err := queryJSON(ctx, tx, `SELECT w.id, w.name, w.is_transit, count(st.item_id) FILTER (WHERE st.quantity > 0) AS items,
			coalesce(sum(st.value), 0)::text AS value FROM warehouses w LEFT JOIN stock st ON st.warehouse_id = w.id
			WHERE w.company_id = $1 GROUP BY w.id ORDER BY w.is_transit, w.created_at`, cid)
		if err != nil {
			return nil, err
		}
		accs, err := queryJSON(ctx, tx, `WITH sv AS (
			SELECT it.inventory_account_id AS account_id, sum(st.value) AS stock_value FROM stock st JOIN items it ON it.id = st.item_id
			WHERE it.company_id = $1 AND it.inventory_account_id IS NOT NULL GROUP BY it.inventory_account_id),
			gl AS (SELECT l.account_id, sum(l.base_debit - l.base_credit) AS ledger FROM journal_lines l
			JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
			WHERE e.company_id = $1 AND l.account_id IN (SELECT DISTINCT inventory_account_id FROM items WHERE company_id = $1 AND inventory_account_id IS NOT NULL)
			GROUP BY l.account_id)
			SELECT a.id, a.code, a.name, a.name_i18n, coalesce(sv.stock_value, 0)::text AS stock_value, coalesce(gl.ledger, 0)::text AS ledger,
			(coalesce(sv.stock_value, 0) - coalesce(gl.ledger, 0))::text AS difference
			FROM accounts a LEFT JOIN sv ON sv.account_id = a.id LEFT JOIN gl ON gl.account_id = a.id
			WHERE a.id IN (SELECT account_id FROM sv UNION SELECT account_id FROM gl) ORDER BY a.code`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"base_currency": base, "warehouses": whs, "accounts": accs}, nil
	})
}

func atoiDef(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// ======================= vehicles =======================

func (s *Server) whVehicles(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT v.id, v.name, v.plate, trim_scale(v.capacity_kg)::text AS capacity_kg, v.driver_id, u.full_name AS driver_name,
			v.is_active, v.notes FROM vehicles v LEFT JOIN users u ON u.id = v.driver_id WHERE v.company_id = $1 ORDER BY v.is_active DESC, v.name`,
			r.PathValue("cid"))
	})
}

type vehicleIn struct {
	Name       *string `json:"name"`
	Plate      *string `json:"plate"`
	CapacityKg *string `json:"capacity_kg"`
	DriverID   *string `json:"driver_id"`
	IsActive   *bool   `json:"is_active"`
	Notes      *string `json:"notes"`
}

func checkUser(ctx context.Context, tx pgx.Tx, id *string) (*string, error) {
	u := nilBlank(id)
	if u == nil {
		return nil, nil
	}
	if !isUUID(*u) {
		return nil, badRequest("invalid_user")
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, *u).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, badRequest("invalid_user")
	}
	return u, nil
}

func (s *Server) whSaveVehicle(w http.ResponseWriter, r *http.Request) {
	var in vehicleIn
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	creating := id == ""
	if (creating && (in.Name == nil || strings.TrimSpace(*in.Name) == "")) || (in.Name != nil && len(*in.Name) > 120) ||
		(in.Plate != nil && len(*in.Plate) > 30) || (in.Notes != nil && len(*in.Notes) > 1000) || (!creating && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var capS *string
	if in.CapacityKg != nil {
		v := strings.TrimSpace(*in.CapacityKg)
		if v == "" {
			v = "0"
		}
		d, ok := money(v)
		if !ok || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(1000000)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		x := d.String()
		capS = &x
	}
	cid := r.PathValue("cid")
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		driverSet := in.DriverID != nil
		drv, err := checkUser(ctx, tx, in.DriverID)
		if err != nil {
			return nil, err
		}
		trim := func(p *string) *string {
			if p == nil {
				return nil
			}
			v := strings.TrimSpace(*p)
			return &v
		}
		if creating {
			err = tx.QueryRow(ctx, `INSERT INTO vehicles (company_id, name, plate, capacity_kg, driver_id, notes) VALUES ($1, $2, coalesce($3, ''),
				coalesce($4::numeric, 0), $5, coalesce($6, '')) RETURNING id::text`, cid, trim(in.Name), trim(in.Plate), capS, drv, trim(in.Notes)).Scan(&id)
		} else {
			var tag interface{ RowsAffected() int64 }
			t, e := tx.Exec(ctx, `UPDATE vehicles SET name = coalesce($3, name), plate = coalesce($4, plate), capacity_kg = coalesce($5::numeric, capacity_kg),
				driver_id = CASE WHEN $6 THEN $7::uuid ELSE driver_id END, is_active = coalesce($8, is_active), notes = coalesce($9, notes)
				WHERE id = $1 AND company_id = $2`, id, cid, trim(in.Name), trim(in.Plate), capS, driverSet, drv, in.IsActive, trim(in.Notes))
			tag, err = t, e
			if err == nil && tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, name, plate, trim_scale(capacity_kg)::text AS capacity_kg, driver_id, is_active, notes FROM vehicles WHERE id = $1`, id)
	})
}

// ======================= delivery board and trips =======================

// GET /wh/to-schedule: deliveries (and confirmed orders with stock still to deliver) that are on no trip yet.
func (s *Server) whToSchedule(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cid := r.PathValue("cid")
		dels, err := queryJSON(ctx, tx, `SELECT d.id, d.number, d.delivery_date, d.order_id, coalesce(o.order_number, o.number) AS order_number,
			o.customer_id, c.name AS customer_name, c.address, c.phone,
			(SELECT count(*) FROM wh_packages p WHERE p.delivery_id = d.id) AS packages,
			(SELECT coalesce(sum(p.weight), 0)::text FROM wh_packages p WHERE p.delivery_id = d.id) AS weight
			FROM sales_deliveries d JOIN sales_orders o ON o.id = d.order_id JOIN customers c ON c.id = o.customer_id
			WHERE d.company_id = $1 AND d.status = 'posted' AND d.delivery_date >= current_date - 30
			AND NOT EXISTS (SELECT 1 FROM trip_stops s JOIN delivery_trips t ON t.id = s.trip_id WHERE s.sales_delivery_id = d.id AND t.status <> 'cancelled')
			ORDER BY d.delivery_date, d.number LIMIT 500`, cid)
		if err != nil {
			return nil, err
		}
		orders, err := queryJSON(ctx, tx, `SELECT o.id, coalesce(o.order_number, o.number) AS order_number, o.order_date, o.customer_id,
			c.name AS customer_name, c.address, c.phone
			FROM sales_orders o JOIN customers c ON c.id = o.customer_id
			WHERE o.company_id = $1 AND o.status = 'confirmed'
			AND EXISTS (SELECT 1 FROM sales_order_lines l JOIN items it ON it.id = l.item_id WHERE l.order_id = o.id AND it.kind = 'stock' AND l.qty_delivered < l.quantity)
			AND NOT EXISTS (SELECT 1 FROM trip_stops s JOIN delivery_trips t ON t.id = s.trip_id WHERE s.sales_order_id = o.id AND s.sales_delivery_id IS NULL
			                AND t.status IN ('planned', 'out'))
			ORDER BY o.order_date, o.order_number LIMIT 500`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"deliveries": dels, "orders": orders}, nil
	})
}

const tripCols = `t.id, t.number, t.trip_date, t.vehicle_id, v.name AS vehicle_name, v.plate, t.driver_id, u.full_name AS driver_name, t.status,
	t.notes, t.started_at, t.finished_at, t.created_at,
	(SELECT count(*) FROM trip_stops s WHERE s.trip_id = t.id) AS stops,
	(SELECT count(*) FROM trip_stops s WHERE s.trip_id = t.id AND s.status = 'delivered') AS delivered,
	(SELECT count(*) FROM trip_stops s WHERE s.trip_id = t.id AND s.status = 'failed') AS failed`

const tripFrom = ` FROM delivery_trips t LEFT JOIN vehicles v ON v.id = t.vehicle_id LEFT JOIN users u ON u.id = t.driver_id`

// GET /wh/trips?date=&status=open|all|<status>&mine=1
func (s *Server) whTrips(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	date, st, mine := q.Get("date"), q.Get("status"), q.Get("mine") == "1"
	if st == "" {
		st = "all"
	}
	if (date != "" && !validDate(date)) || len(st) > 20 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !mine && !can(r, "stock.view") {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+tripCols+tripFrom+` WHERE t.company_id = $1 AND ($2 = '' OR t.trip_date = nullif($2, '')::date)
			AND ($3 = 'all' OR ($3 = 'open' AND t.status IN ('planned', 'out')) OR t.status = $3)
			AND (NOT $4 OR (t.driver_id = app_user() AND t.status IN ('planned', 'out') AND t.trip_date >= current_date - 1))
			ORDER BY t.trip_date DESC, t.number DESC LIMIT 500`, r.PathValue("cid"), date, st, mine)
	})
}

func loadTrip(ctx context.Context, tx pgx.Tx, r *http.Request, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	var driver *string
	err := tx.QueryRow(ctx, `SELECT driver_id::text FROM delivery_trips WHERE id = $1 AND company_id = $2`, id, cid).Scan(&driver)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	if !can(r, "stock.view") {
		var me *string
		_ = tx.QueryRow(ctx, `SELECT app_user()::text`).Scan(&me)
		if driver == nil || me == nil || *driver != *me {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
	}
	t, err := queryJSONOne(ctx, tx, `SELECT `+tripCols+tripFrom+` WHERE t.id = $1`, id)
	if err != nil {
		return nil, err
	}
	stops, err := queryJSON(ctx, tx, `SELECT s.id, s.seq, s.sales_delivery_id, d.number AS delivery_number, s.sales_order_id,
		coalesce(o.order_number, o.number) AS order_number, s.customer_id, c.name AS customer_name, s.address, s.phone, s.note, s.status,
		s.pod_name, (s.pod_signature <> '') AS signed, s.pod_note, s.lat, s.lng, s.delivered_at,
		(SELECT count(*) FROM wh_packages p WHERE p.delivery_id = s.sales_delivery_id) AS packages,
		(SELECT json_agg(json_build_object('code', 'PKG' || lpad(p.number::text, 6, '0'), 'kind', p.kind, 'weight', trim_scale(p.weight)::text))
		 FROM wh_packages p WHERE p.delivery_id = s.sales_delivery_id) AS package_list,
		(SELECT count(*) FROM attachments a WHERE a.entity_type = 'trip_stop' AND a.entity_id = s.id AND a.deleted_at IS NULL) AS photos
		FROM trip_stops s LEFT JOIN sales_deliveries d ON d.id = s.sales_delivery_id LEFT JOIN sales_orders o ON o.id = s.sales_order_id
		LEFT JOIN customers c ON c.id = s.customer_id WHERE s.trip_id = $1 ORDER BY s.seq`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"trip": t, "stops": stops}, nil
}

func (s *Server) whGetTrip(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadTrip(ctx, tx, r, r.PathValue("cid"), r.PathValue("id"))
	})
}

type stopIn struct {
	SalesDeliveryID *string `json:"sales_delivery_id"`
	SalesOrderID    *string `json:"sales_order_id"`
	CustomerID      *string `json:"customer_id"`
	Address         string  `json:"address"`
	Phone           string  `json:"phone"`
	Note            string  `json:"note"`
}

// writeStops replaces the stops of a planned trip (in the given order).
func writeStops(ctx context.Context, tx pgx.Tx, cid, trip string, stops []stopIn) error {
	if _, err := tx.Exec(ctx, `DELETE FROM trip_stops WHERE trip_id = $1 AND status = 'pending'`, trip); err != nil {
		return err
	}
	var seq int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(seq), 0) FROM trip_stops WHERE trip_id = $1`, trip).Scan(&seq); err != nil {
		return err
	}
	for _, s := range stops {
		if len(s.Address) > 500 || len(s.Phone) > 40 || len(s.Note) > 1000 {
			return badRequest("bad_request")
		}
		del, so, cust := nilBlank(s.SalesDeliveryID), nilBlank(s.SalesOrderID), nilBlank(s.CustomerID)
		var addr, phone string
		switch {
		case del != nil:
			if !isUUID(*del) {
				return badRequest("invalid_delivery")
			}
			var o, c, a, p string
			err := tx.QueryRow(ctx, `SELECT o.id::text, c.id::text, c.address, c.phone FROM sales_deliveries d JOIN sales_orders o ON o.id = d.order_id
				JOIN customers c ON c.id = o.customer_id WHERE d.id = $1 AND d.company_id = $2 AND d.status = 'posted'`, *del, cid).Scan(&o, &c, &a, &p)
			if errors.Is(err, pgx.ErrNoRows) {
				return badRequest("invalid_delivery")
			}
			if err != nil {
				return err
			}
			so, cust, addr, phone = &o, &c, a, p
		case so != nil:
			if !isUUID(*so) {
				return badRequest("invalid_order")
			}
			var c, a, p string
			err := tx.QueryRow(ctx, `SELECT c.id::text, c.address, c.phone FROM sales_orders o JOIN customers c ON c.id = o.customer_id
				WHERE o.id = $1 AND o.company_id = $2`, *so, cid).Scan(&c, &a, &p)
			if errors.Is(err, pgx.ErrNoRows) {
				return badRequest("invalid_order")
			}
			if err != nil {
				return err
			}
			cust, addr, phone = &c, a, p
		case cust != nil:
			if !isUUID(*cust) {
				return badRequest("invalid_customer")
			}
			err := tx.QueryRow(ctx, `SELECT address, phone FROM customers WHERE id = $1 AND company_id = $2`, *cust, cid).Scan(&addr, &phone)
			if errors.Is(err, pgx.ErrNoRows) {
				return badRequest("invalid_customer")
			}
			if err != nil {
				return err
			}
		default:
			if strings.TrimSpace(s.Address) == "" {
				return badRequest("stop_needs_address")
			}
		}
		if strings.TrimSpace(s.Address) != "" {
			addr = strings.TrimSpace(s.Address)
		}
		if strings.TrimSpace(s.Phone) != "" {
			phone = strings.TrimSpace(s.Phone)
		}
		seq++
		if _, err := tx.Exec(ctx, `INSERT INTO trip_stops (company_id, trip_id, seq, sales_delivery_id, sales_order_id, customer_id, address, phone, note)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, cid, trip, seq, del, so, cust, addr, phone, strings.TrimSpace(s.Note)); err != nil {
			return err
		}
	}
	return nil
}

// POST /wh/trips {trip_date, vehicle_id, driver_id, notes, stops: [...]}
func (s *Server) whCreateTrip(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TripDate  string   `json:"trip_date"`
		VehicleID *string  `json:"vehicle_id"`
		DriverID  *string  `json:"driver_id"`
		Notes     string   `json:"notes"`
		Stops     []stopIn `json:"stops"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.TripDate == "" {
		in.TripDate = today()
	}
	if !validDate(in.TripDate) || len(in.Stops) > 200 || len(in.Notes) > 2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		veh, drv, err := tripRefs(ctx, tx, cid, in.VehicleID, in.DriverID)
		if err != nil {
			return nil, err
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'delivery_trip')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO delivery_trips (company_id, number, trip_date, vehicle_id, driver_id, notes)
			VALUES ($1, $2, $3::date, $4, $5, $6) RETURNING id::text`, cid, n, in.TripDate, veh, drv, strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		if err := writeStops(ctx, tx, cid, id, in.Stops); err != nil {
			return nil, err
		}
		return loadTrip(ctx, tx, r, cid, id)
	})
}

// tripRefs: the vehicle (and its usual driver when none is given) of a trip.
func tripRefs(ctx context.Context, tx pgx.Tx, cid string, vehicle, driver *string) (*string, *string, error) {
	veh := nilBlank(vehicle)
	drv, err := checkUser(ctx, tx, driver)
	if err != nil {
		return nil, nil, err
	}
	if veh != nil {
		if !isUUID(*veh) {
			return nil, nil, badRequest("invalid_vehicle")
		}
		var vd *string
		err := tx.QueryRow(ctx, `SELECT driver_id::text FROM vehicles WHERE id = $1 AND company_id = $2 AND is_active`, *veh, cid).Scan(&vd)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, badRequest("invalid_vehicle")
		}
		if err != nil {
			return nil, nil, err
		}
		if drv == nil {
			drv = vd
		}
	}
	return veh, drv, nil
}

// PATCH /wh/trips/{id} {trip_date, vehicle_id, driver_id, notes, status: out|done|cancelled|planned, stops}
func (s *Server) whUpdateTrip(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TripDate  *string   `json:"trip_date"`
		VehicleID *string   `json:"vehicle_id"`
		DriverID  *string   `json:"driver_id"`
		Notes     *string   `json:"notes"`
		Status    *string   `json:"status"`
		Stops     *[]stopIn `json:"stops"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if (in.TripDate != nil && !validDate(*in.TripDate)) || (in.Status != nil && !contains([]string{"planned", "out", "done", "cancelled"}, *in.Status)) ||
		(in.Notes != nil && len(*in.Notes) > 2000) || (in.Stops != nil && len(*in.Stops) > 200) {
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
		err := tx.QueryRow(ctx, `SELECT status FROM delivery_trips WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st == "done" || st == "cancelled" {
			if in.Status == nil || *in.Status != "planned" || st != "cancelled" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
		}
		vehSet, drvSet := in.VehicleID != nil, in.DriverID != nil
		veh, drv, err := tripRefs(ctx, tx, cid, in.VehicleID, in.DriverID)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE delivery_trips SET trip_date = coalesce($3::date, trip_date), notes = coalesce($4, notes),
			vehicle_id = CASE WHEN $5 THEN $6::uuid ELSE vehicle_id END, driver_id = CASE WHEN $7 OR ($5 AND $8::uuid IS NOT NULL) THEN $8::uuid ELSE driver_id END,
			status = coalesce($9, status),
			started_at = CASE WHEN $9 = 'out' AND started_at IS NULL THEN now() ELSE started_at END,
			finished_at = CASE WHEN $9 IN ('done', 'cancelled') THEN now() ELSE finished_at END WHERE id = $1 AND company_id = $2`,
			id, cid, in.TripDate, in.Notes, vehSet, veh, drvSet, drv, in.Status); err != nil {
			return nil, err
		}
		if in.Stops != nil {
			if err := writeStops(ctx, tx, cid, id, *in.Stops); err != nil {
				return nil, err
			}
		}
		return loadTrip(ctx, tx, r, cid, id)
	})
}

// POST /wh/stops/{id}/pod {status: delivered|failed, name, signature (data:image/png;base64,…), note, lat, lng}
// — the driver of the trip (or anyone with stock rights) records the proof of delivery.
func (s *Server) whStopPOD(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status    string   `json:"status"`
		Name      string   `json:"name"`
		Signature string   `json:"signature"`
		Note      string   `json:"note"`
		Lat       *float64 `json:"lat"`
		Lng       *float64 `json:"lng"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if (in.Status != "delivered" && in.Status != "failed") || len(in.Name) > 120 || len(in.Note) > 1000 || len(in.Signature) > 400000 ||
		(in.Signature != "" && !strings.HasPrefix(in.Signature, "data:image/png;base64,")) ||
		(in.Lat != nil && (*in.Lat < -90 || *in.Lat > 90)) || (in.Lng != nil && (*in.Lng < -180 || *in.Lng > 180)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var trip, tst string
		var driver *string
		var mine bool
		err := tx.QueryRow(ctx, `SELECT s.trip_id::text, t.status, t.driver_id::text, coalesce(t.driver_id = app_user(), false) FROM trip_stops s
			JOIN delivery_trips t ON t.id = s.trip_id WHERE s.id = $1 AND s.company_id = $2 FOR UPDATE OF s`, id, cid).Scan(&trip, &tst, &driver, &mine)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if !mine && !can(r, "stock.create") {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		if tst != "planned" && tst != "out" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.Status == "delivered" && strings.TrimSpace(in.Name) == "" && in.Signature == "" {
			return nil, badRequest("pod_needs_name_or_signature")
		}
		if _, err := tx.Exec(ctx, `UPDATE trip_stops SET status = $2, pod_name = $3, pod_signature = $4, pod_note = $5, lat = $6, lng = $7,
			delivered_at = now() WHERE id = $1`, id, in.Status, strings.TrimSpace(in.Name), in.Signature, strings.TrimSpace(in.Note), in.Lat, in.Lng); err != nil {
			return nil, err
		}
		// the trip is under way once a stop is done, and done when every stop is
		if _, err := tx.Exec(ctx, `UPDATE delivery_trips SET status = CASE WHEN NOT EXISTS (SELECT 1 FROM trip_stops WHERE trip_id = $1 AND status = 'pending')
			THEN 'done' ELSE 'out' END, started_at = coalesce(started_at, now()),
			finished_at = CASE WHEN NOT EXISTS (SELECT 1 FROM trip_stops WHERE trip_id = $1 AND status = 'pending') THEN now() ELSE finished_at END
			WHERE id = $1`, trip); err != nil {
			return nil, err
		}
		return loadTrip(ctx, tx, r, cid, trip)
	})
}

// GET /wh/stops/{id}/signature: the signature image of a stop (data URL).
func (s *Server) whStopSignature(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT s.id, s.pod_name, s.pod_signature, s.delivered_at FROM trip_stops s WHERE s.id = $1 AND s.company_id = $2`, id, cid)
	})
}
