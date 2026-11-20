package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Vertical features of the business packs (phase 6a), built on the shared engine:
//   electronics: serial / IMEI history (bought from, sold to, repairs, warranty); serials and trade-ins at the till
//                live in createPosOrderTx.
//   furniture:   options and price per m² on made-to-order items (configurator), deposits on sales orders (customer
//                credit used by the invoice), delivery + assembly scheduling (a delivery-board trip and a service job),
//                showroom stock.
//   wholesale:   salesman routes, vans (a warehouse per salesman): load, sell from the phone (offline queue, price
//                lists, credit limit), close the day with a count and unload; commission report.
// Item and warehouse fields of every vertical are edited through /items/{id}/vertical and /warehouses/{id}/vertical.

// ======================= shared: vertical fields of items and warehouses =======================

const itemVerticalCols = `id, made_to_order, options, price_per_m2::text AS price_per_m2, kitchen_station, generic_name, active_ingredient,
	strength, dosage_form, rx_required, controlled, warranty_months, tracking`

func (s *Server) getItemVertical(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT `+itemVerticalCols+` FROM items WHERE id = $1 AND company_id = $2`, id, cid)
	})
}

type itemOption struct {
	Name   string `json:"name"`
	Values []struct {
		Label string `json:"label"`
		Extra string `json:"extra"` // extra price in the base currency
	} `json:"values"`
}

// PUT /items/{id}/vertical {made_to_order, options, price_per_m2, kitchen_station, generic_name, active_ingredient,
// strength, dosage_form, rx_required, controlled} (every field optional)
func (s *Server) putItemVertical(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MadeToOrder      *bool         `json:"made_to_order"`
		Options          *[]itemOption `json:"options"`
		PricePerM2       *string       `json:"price_per_m2"` // "" = none
		KitchenStation   *string       `json:"kitchen_station"`
		GenericName      *string       `json:"generic_name"`
		ActiveIngredient *string       `json:"active_ingredient"`
		Strength         *string       `json:"strength"`
		DosageForm       *string       `json:"dosage_form"`
		RxRequired       *bool         `json:"rx_required"`
		Controlled       *bool         `json:"controlled"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	ok := len(deref(in.KitchenStation)) <= 40 && len(deref(in.GenericName)) <= 200 && len(deref(in.ActiveIngredient)) <= 200 &&
		len(deref(in.Strength)) <= 60 && len(deref(in.DosageForm)) <= 60
	var opts []byte
	if in.Options != nil {
		if len(*in.Options) > 20 {
			ok = false
		}
		for _, o := range *in.Options {
			if strings.TrimSpace(o.Name) == "" || len(o.Name) > 60 || len(o.Values) == 0 || len(o.Values) > 50 {
				ok = false
			}
			for _, v := range o.Values {
				if strings.TrimSpace(v.Label) == "" || len(v.Label) > 80 || (v.Extra != "" && !okMoney(v.Extra)) {
					ok = false
				}
			}
		}
		opts, _ = json.Marshal(*in.Options)
	}
	var ppm *string
	if in.PricePerM2 != nil && *in.PricePerM2 != "" {
		if d, good := money(*in.PricePerM2); !good || d.IsNegative() {
			ok = false
		}
	}
	if in.PricePerM2 != nil {
		ppm = in.PricePerM2
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		trim := func(p *string) *string {
			if p == nil {
				return nil
			}
			v := strings.TrimSpace(*p)
			return &v
		}
		tag, err := tx.Exec(ctx, `UPDATE items SET made_to_order = coalesce($3, made_to_order), options = coalesce($4::jsonb, options),
			price_per_m2 = CASE WHEN $5::boolean THEN nullif($6, '')::numeric ELSE price_per_m2 END,
			kitchen_station = coalesce($7, kitchen_station), generic_name = coalesce($8, generic_name),
			active_ingredient = coalesce($9, active_ingredient), strength = coalesce($10, strength), dosage_form = coalesce($11, dosage_form),
			rx_required = coalesce($12, rx_required), controlled = coalesce($13, controlled)
			WHERE id = $1 AND company_id = $2`, id, cid, in.MadeToOrder, nullJSON(opts), ppm != nil, deref(ppm), trim(in.KitchenStation),
			trim(in.GenericName), trim(in.ActiveIngredient), trim(in.Strength), trim(in.DosageForm), in.RxRequired, in.Controlled)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+itemVerticalCols+` FROM items WHERE id = $1`, id)
	})
}

func nullJSON(b []byte) *string {
	if b == nil {
		return nil
	}
	s := string(b)
	return &s
}

// PUT /warehouses/{id}/vertical {is_showroom, is_van, van_user_id ("" = none)}
func (s *Server) putWarehouseVertical(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IsShowroom *bool   `json:"is_showroom"`
		IsVan      *bool   `json:"is_van"`
		VanUserID  *string `json:"van_user_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !optUUID(in.VanUserID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if u := deref(in.VanUserID); u != "" {
			if err := checkCompanyUser(ctx, tx, cid, u); err != nil {
				return nil, err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE warehouses SET is_showroom = coalesce($3, is_showroom), is_van = coalesce($4, is_van),
			van_user_id = CASE WHEN $5::boolean THEN nullif($6, '')::uuid ELSE van_user_id END WHERE id = $1 AND company_id = $2`,
			id, cid, in.IsShowroom, in.IsVan, in.VanUserID != nil, deref(in.VanUserID))
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT w.id, w.name, w.is_showroom, w.is_van, w.van_user_id, u.full_name AS van_user_name
			FROM warehouses w LEFT JOIN users u ON u.id = w.van_user_id WHERE w.id = $1`, id)
	})
}

// GET /verticals/warehouses: every warehouse with its vertical flags (showroom, van and its salesman).
func (s *Server) listWarehouseVertical(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT w.id, w.name, w.is_active, w.is_showroom, w.is_van, w.van_user_id, u.full_name AS van_user_name,
			coalesce((SELECT sum(s.value) FROM stock s WHERE s.warehouse_id = w.id), 0)::text AS value
			FROM warehouses w LEFT JOIN users u ON u.id = w.van_user_id WHERE w.company_id = $1 ORDER BY w.name`, r.PathValue("cid"))
	})
}

// ======================= electronics =======================

// GET /stock/serial-history?serial= : where a serial / IMEI came from, where it went, its warranty and repairs.
func (s *Server) serialHistory(w http.ResponseWriter, r *http.Request) {
	serial := strings.TrimSpace(r.URL.Query().Get("serial"))
	if serial == "" || len(serial) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		lots, err := queryJSON(ctx, tx, `SELECT l.id, l.lot_no, l.expiry, it.id AS item_id, it.code AS item_code, it.name AS item_name, it.tracking,
			it.warranty_months, coalesce((SELECT sum(q.quantity) FROM stock_lot_qty q WHERE q.lot_id = l.id), 0)::text AS in_stock,
			(SELECT w.name FROM stock_lot_qty q JOIN warehouses w ON w.id = q.warehouse_id WHERE q.lot_id = l.id AND q.quantity > 0 LIMIT 1) AS warehouse
			FROM stock_lots l JOIN items it ON it.id = l.item_id WHERE l.company_id = $1 AND lower(l.lot_no) = lower($2) ORDER BY l.created_at`, cid, serial)
		if err != nil {
			return nil, err
		}
		moves, err := queryJSON(ctx, tx, `SELECT m.move_date, m.source, m.source_id, ml.quantity::text AS quantity, w.name AS warehouse, m.notes,
			CASE WHEN m.source IN ('sale', 'sale_void') THEN (SELECT i.number FROM invoices i WHERE i.id = m.source_id)
			     WHEN m.source IN ('bill', 'bill_void') THEN (SELECT b.number FROM bills b WHERE b.id = m.source_id)
			     WHEN m.source = 'tradein' THEN (SELECT o.number FROM pos_orders o WHERE o.id = m.source_id) END AS doc_number,
			CASE WHEN m.source IN ('sale', 'sale_void') THEN (SELECT c.name FROM invoices i JOIN customers c ON c.id = i.customer_id WHERE i.id = m.source_id)
			     WHEN m.source IN ('bill', 'bill_void') THEN (SELECT su.name FROM bills b JOIN suppliers su ON su.id = b.supplier_id WHERE b.id = m.source_id)
			     WHEN m.source = 'tradein' THEN (SELECT c.name FROM pos_orders o JOIN customers c ON c.id = o.customer_id WHERE o.id = m.source_id) END AS partner
			FROM stock_move_lots ml JOIN stock_lots l ON l.id = ml.lot_id JOIN stock_moves m ON m.id = ml.move_id JOIN warehouses w ON w.id = m.warehouse_id
			WHERE l.company_id = $1 AND lower(l.lot_no) = lower($2) ORDER BY m.move_date, m.created_at`, cid, serial)
		if err != nil {
			return nil, err
		}
		warranty, err := warrantyLookup(ctx, tx, cid, serial)
		if err != nil {
			return nil, err
		}
		repairs, err := queryJSON(ctx, tx, `SELECT id, number, status, fault, under_warranty, received_at FROM repair_orders
			WHERE company_id = $1 AND lower(serial) = lower($2) ORDER BY received_at DESC LIMIT 50`, cid, serial)
		if err != nil {
			return nil, err
		}
		var company string
		if err := tx.QueryRow(ctx, `SELECT name_en FROM companies WHERE id = $1`, cid).Scan(&company); err != nil {
			return nil, err
		}
		wb, _ := json.Marshal(warranty)
		return map[string]any{"serial": serial, "lots": lots, "moves": moves, "warranty": json.RawMessage(wb), "repairs": repairs, "company_name": company}, nil
	})
}

// GET /stock/serials?item_id= : the serials of an item that are in stock (the till lists them to pick from).
func (s *Server) serialsInStock(w http.ResponseWriter, r *http.Request) {
	item, wh := r.URL.Query().Get("item_id"), r.URL.Query().Get("warehouse_id")
	if !isUUID(item) || (wh != "" && !isUUID(wh)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT l.lot_no AS serial, q.warehouse_id, w.name AS warehouse, l.created_at FROM stock_lots l
			JOIN stock_lot_qty q ON q.lot_id = l.id AND q.quantity > 0 JOIN warehouses w ON w.id = q.warehouse_id
			WHERE l.company_id = $1 AND l.item_id = $2 AND ($3 = '' OR q.warehouse_id = $3::uuid) ORDER BY l.created_at LIMIT 500`,
			r.PathValue("cid"), item, wh)
	})
}

// ======================= furniture =======================

// POST /furn/configure {item_id, choices: {"Fabric": "Velvet"}, width_cm, depth_cm, currency}: the unit price and line
// text of a made-to-order item (base price + option extras + area × price per m²).
func (s *Server) furnConfigure(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ItemID   string            `json:"item_id"`
		Choices  map[string]string `json:"choices"`
		WidthCM  string            `json:"width_cm"`
		DepthCM  string            `json:"depth_cm"`
		Currency string            `json:"currency"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(in.ItemID) || len(in.Choices) > 20 || (in.Currency != "" && !currencyRe.MatchString(in.Currency)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return configureItem(ctx, tx, cid, in.ItemID, in.Choices, in.WidthCM, in.DepthCM, in.Currency)
	})
}

func configureItem(ctx context.Context, tx pgx.Tx, cid, itemID string, choices map[string]string, width, depth, cur string) (map[string]any, error) {
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if cur == "" {
		cur = set.Base
	}
	var name, salePrice, saleCur string
	var listPrice, ppmS *string
	var optsRaw []byte
	err = tx.QueryRow(ctx, `SELECT it.name, ip.price::text, it.sale_price::text, coalesce(it.sale_currency, $3)::text, it.price_per_m2::text, it.options
		FROM items it LEFT JOIN item_prices ip ON ip.item_id = it.id AND ip.currency = $4 WHERE it.id = $1 AND it.company_id = $2`,
		itemID, cid, set.Base, cur).Scan(&name, &listPrice, &salePrice, &saleCur, &ppmS, &optsRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, badRequest("invalid_item")
	}
	if err != nil {
		return nil, err
	}
	rc := newRateCache(ctx, tx, cid, set.Base, today())
	dec, err := rc.decimals(cur)
	if err != nil {
		return nil, err
	}
	price, err := itemPriceIn(rc, listPrice, salePrice, saleCur, cur)
	if err != nil {
		return nil, err
	}
	var opts []itemOption
	_ = json.Unmarshal(optsRaw, &opts)
	parts := []string{name}
	for _, o := range opts {
		pick, ok := choices[o.Name]
		if !ok {
			continue
		}
		found := false
		for _, v := range o.Values {
			if v.Label == pick {
				found = true
				if v.Extra != "" {
					x, _ := money(v.Extra)
					xc, err := rc.convert(x, set.Base, cur)
					if err != nil {
						return nil, err
					}
					price = price.Add(xc)
				}
			}
		}
		if !found {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "invalid_option", detail: o.Name}
		}
		parts = append(parts, o.Name+": "+pick)
	}
	for k := range choices {
		known := false
		for _, o := range opts {
			known = known || o.Name == k
		}
		if !known {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "invalid_option", detail: k}
		}
	}
	area := decimal.Zero
	if strings.TrimSpace(width) != "" || strings.TrimSpace(depth) != "" {
		wd, ok1 := money(width)
		dp, ok2 := money(depth)
		if !ok1 || !ok2 || !wd.IsPositive() || !dp.IsPositive() || wd.GreaterThan(decimal.NewFromInt(2000)) || dp.GreaterThan(decimal.NewFromInt(2000)) {
			return nil, badRequest("invalid_size")
		}
		area = wd.Mul(dp).Div(decimal.NewFromInt(10000)).Round(4)
		parts = append(parts, fmt.Sprintf("%s×%s cm", wd.String(), dp.String()))
		if ppmS != nil {
			ppm, _ := decimal.NewFromString(*ppmS)
			v, err := rc.convert(area.Mul(ppm), set.Base, cur)
			if err != nil {
				return nil, err
			}
			price = price.Add(v)
		}
	}
	return map[string]any{"item_id": itemID, "unit_price": price.Round(dec).String(), "currency": cur, "description": strings.Join(parts, " · "),
		"area_m2": area.String()}, nil
}

const furnOrderCols = `o.id, o.number, o.order_number, o.status, o.customer_id, c.name AS customer_name, c.phone AS customer_phone, c.address AS customer_address,
	o.currency, o.total::text AS total, o.quote_date, o.order_date, coalesce(o.deposit_pct, co.deposit_pct)::text AS deposit_pct,
	round(o.total * coalesce(o.deposit_pct, co.deposit_pct) / 100, 2)::text AS deposit_required,
	coalesce((SELECT sum(d.amount) FROM so_deposits d JOIN payments p ON p.id = d.payment_id AND p.status = 'posted' WHERE d.order_id = o.id), 0)::text AS deposit_paid,
	o.delivery_date, o.delivery_slot, o.assembly, o.service_job_id, sj.number AS job_number, sj.status AS job_status, o.trip_id, t.number AS trip_number,
	t.status AS trip_status, coalesce((SELECT bool_or(it.made_to_order) FROM sales_order_lines l JOIN items it ON it.id = l.item_id WHERE l.order_id = o.id), false) AS made_to_order`

const furnOrderFrom = ` FROM sales_orders o JOIN customers c ON c.id = o.customer_id JOIN companies co ON co.id = o.company_id
	LEFT JOIN service_jobs sj ON sj.id = o.service_job_id LEFT JOIN delivery_trips t ON t.id = o.trip_id`

// GET /furn/orders?status=open|all : orders with their deposit, delivery and assembly.
func (s *Server) furnOrders(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+furnOrderCols+furnOrderFrom+` WHERE o.company_id = $1
			AND ($2 = 'all' OR o.status IN ('draft', 'sent', 'confirmed')) ORDER BY o.created_at DESC LIMIT 300`, r.PathValue("cid"), status)
	})
}

func loadFurnOrder(ctx context.Context, tx pgx.Tx, cid, id string) (json.RawMessage, error) {
	o, err := queryJSONOne(ctx, tx, `SELECT `+furnOrderCols+furnOrderFrom+` WHERE o.id = $1 AND o.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	deps, err := queryJSON(ctx, tx, `SELECT d.id, d.amount::text AS amount, d.currency, d.created_at, p.number AS payment_number, p.status AS payment_status
		FROM so_deposits d JOIN payments p ON p.id = d.payment_id WHERE d.order_id = $1 ORDER BY d.created_at`, id)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(o, &m)
	m["deposits"] = deps
	b, _ := json.Marshal(m)
	return b, nil
}

// PUT /furn/orders/{id}/deposit-pct {pct ("" = the company's)}
func (s *Server) furnDepositPct(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Pct string `json:"pct"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Pct != "" {
		if p, ok := money(in.Pct); !ok || p.IsNegative() || p.GreaterThan(decimal.NewFromInt(100)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE sales_orders SET deposit_pct = nullif($3, '')::numeric WHERE id = $1 AND company_id = $2`, id, cid, in.Pct)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return loadFurnOrder(ctx, tx, cid, id)
	})
}

// POST /furn/orders/{id}/deposit {method_id, amount, client_ref}: a customer payment on account linked to the order
// (the order's invoice uses it automatically as customer credit).
func (s *Server) furnDeposit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MethodID  string `json:"method_id"`
		Amount    string `json:"amount"`
		ClientRef string `json:"client_ref"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	amt, ok := money(in.Amount)
	if !ok || !amt.IsPositive() || !isUUID(in.MethodID) || len(in.ClientRef) > 64 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	me := principal(r).ID
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, customer, cur string
		var number int64
		if err := tx.QueryRow(ctx, `SELECT status, customer_id::text, currency::text, number FROM sales_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`,
			id, cid).Scan(&status, &customer, &cur, &number); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		if status == "cancelled" || status == "done" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		pid, err := receivePayment(ctx, tx, cid, paymentIn{CustomerID: customer, Date: today(), Currency: cur,
			Tenders: []tenderIn{{MethodID: in.MethodID, Amount: amt.String()}}, Notes: fmt.Sprintf("Deposit · order #%d", number),
			ClientRef: strings.TrimSpace(in.ClientRef), reserve: true}, false, me)
		if err != nil {
			return nil, err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM so_deposits WHERE payment_id = $1)`, pid).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			var paid string
			if err := tx.QueryRow(ctx, `SELECT amount::text FROM payments WHERE id = $1`, pid).Scan(&paid); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO so_deposits (company_id, order_id, payment_id, currency, amount) VALUES ($1, $2, $3, $4, $5::numeric)`,
				cid, id, pid, cur, paid); err != nil {
				return nil, err
			}
		}
		return loadFurnOrder(ctx, tx, cid, id)
	})
}

// POST /furn/orders/{id}/schedule {date, slot, vehicle_id, driver_id, assembly, technician_id, start ("HH:MM")}: a
// delivery-board trip with the order as its stop (the balance to collect in the note) and, with assembly, a service
// job for the fitters.
func (s *Server) furnSchedule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date         string  `json:"date"`
		Slot         string  `json:"slot"`
		VehicleID    *string `json:"vehicle_id"`
		DriverID     *string `json:"driver_id"`
		Assembly     bool    `json:"assembly"`
		TechnicianID *string `json:"technician_id"`
		Start        string  `json:"start"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Start == "" {
		in.Start = "09:00"
	}
	if _, err := time.Parse("15:04", in.Start); err != nil || !validDate(in.Date) || len(in.Slot) > 40 || !optUUID(in.VehicleID) || !optUUID(in.DriverID) ||
		!optUUID(in.TechnicianID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, customer, cur, total, dep string
		var number int64
		var orderNo *int64
		var tripID *string
		if err := tx.QueryRow(ctx, `SELECT o.status, o.customer_id::text, o.currency::text, o.total::text, o.number, o.order_number, o.trip_id::text,
			(SELECT coalesce(sum(d.amount), 0) FROM so_deposits d JOIN payments p ON p.id = d.payment_id AND p.status = 'posted' WHERE d.order_id = o.id)::text
			FROM sales_orders o WHERE o.id = $1 AND o.company_id = $2 FOR UPDATE OF o`, id, cid).
			Scan(&status, &customer, &cur, &total, &number, &orderNo, &tripID, &dep); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		if status != "confirmed" {
			return nil, &apiError{status: http.StatusConflict, code: "order_not_confirmed"}
		}
		if tripID != nil {
			var ts string
			if err := tx.QueryRow(ctx, `SELECT status FROM delivery_trips WHERE id = $1`, *tripID).Scan(&ts); err != nil {
				return nil, err
			}
			if ts != "cancelled" {
				return nil, &apiError{status: http.StatusConflict, code: "already_scheduled"}
			}
		}
		ref := number
		if orderNo != nil {
			ref = *orderNo
		}
		t, _ := decimal.NewFromString(total)
		d, _ := decimal.NewFromString(dep)
		note := fmt.Sprintf("Order #%d", ref)
		if bal := t.Sub(d); bal.IsPositive() {
			note += fmt.Sprintf(" · balance to collect %s %s", bal.StringFixed(2), cur)
		}
		if in.Slot != "" {
			note += " · " + strings.TrimSpace(in.Slot)
		}
		veh, drv, err := tripRefs(ctx, tx, cid, in.VehicleID, in.DriverID)
		if err != nil {
			return nil, err
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'delivery_trip')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var trip string
		if err := tx.QueryRow(ctx, `INSERT INTO delivery_trips (company_id, number, trip_date, vehicle_id, driver_id, notes)
			VALUES ($1, $2, $3::date, $4, $5, $6) RETURNING id::text`, cid, n, in.Date, veh, drv, note).Scan(&trip); err != nil {
			return nil, err
		}
		if err := writeStops(ctx, tx, cid, trip, []stopIn{{SalesOrderID: &id, Note: note}}); err != nil {
			return nil, err
		}
		var job *string
		if in.Assembly {
			tech := deref(in.TechnicianID)
			if tech != "" {
				if err := checkCompanyUser(ctx, tx, cid, tech); err != nil {
					return nil, err
				}
			}
			var addr, phone string
			if err := tx.QueryRow(ctx, `SELECT address, phone FROM customers WHERE id = $1`, customer).Scan(&addr, &phone); err != nil {
				return nil, err
			}
			var jn int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'service_job')`, cid).Scan(&jn); err != nil {
				return nil, err
			}
			jstatus := "new"
			if tech != "" {
				jstatus = "scheduled"
			}
			var jid string
			if err := tx.QueryRow(ctx, `INSERT INTO service_jobs (company_id, number, customer_id, title, description, address, phone, technician_id,
				scheduled_start, duration_min, priority, status, currency)
				VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, '')::uuid, ($9 || ' ' || $10)::timestamp AT TIME ZONE coalesce((SELECT timezone FROM companies WHERE id = $1), 'UTC'),
				120, 'normal', $11, $12) RETURNING id::text`, cid, jn, customer, fmt.Sprintf("Assembly · order #%d", ref), note, addr, phone, tech,
				in.Date, in.Start, jstatus, cur).Scan(&jid); err != nil {
				return nil, err
			}
			job = &jid
			if tech != "" {
				if err := notifyUsers(ctx, tx, cid, []string{tech}, "service", fmt.Sprintf("#%d Assembly · order #%d", jn, ref), principal(r).FullName, "service_job", jid); err != nil {
					return nil, err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sales_orders SET delivery_date = $2::date, delivery_slot = $3, assembly = $4, service_job_id = $5, trip_id = $6,
			updated_at = now() WHERE id = $1`, id, in.Date, strings.TrimSpace(in.Slot), in.Assembly, job, trip); err != nil {
			return nil, err
		}
		return loadFurnOrder(ctx, tx, cid, id)
	})
}

// GET /furn/showroom: what stands in the showroom warehouses, its value and how long it has been on display.
func (s *Server) furnShowroom(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT w.id AS warehouse_id, w.name AS warehouse, it.id AS item_id, it.code AS item_code, it.name AS item_name,
			s.quantity::text AS quantity, s.value::text AS value, it.sale_price::text AS sale_price, it.sale_currency,
			current_date - (SELECT max(m.move_date) FROM stock_moves m WHERE m.item_id = s.item_id AND m.warehouse_id = s.warehouse_id AND m.quantity > 0) AS days
			FROM stock s JOIN warehouses w ON w.id = s.warehouse_id AND w.is_showroom JOIN items it ON it.id = s.item_id
			WHERE w.company_id = $1 AND s.quantity > 0 ORDER BY days DESC NULLS LAST, it.name`, r.PathValue("cid"))
	})
}

// ======================= wholesale: routes, vans, commission =======================

const routeCols = `r.id, r.name, r.user_id, u.full_name AS user_name, r.warehouse_id, w.name AS warehouse, r.weekdays, r.is_active,
	(SELECT count(*) FROM sales_route_stops x WHERE x.route_id = r.id) AS stops`

const routeFrom = ` FROM sales_routes r LEFT JOIN users u ON u.id = r.user_id LEFT JOIN warehouses w ON w.id = r.warehouse_id`

func (s *Server) vanRoutes(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+routeCols+routeFrom+` WHERE r.company_id = $1 ORDER BY r.is_active DESC, r.name`, r.PathValue("cid"))
	})
}

func loadRoute(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	rt, err := queryJSONOne(ctx, tx, `SELECT `+routeCols+routeFrom+` WHERE r.id = $1 AND r.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	stops, err := queryJSON(ctx, tx, `SELECT x.seq, c.id AS customer_id, c.code, c.name, c.phone, c.address, c.lat, c.lng,
		coalesce((SELECT sum(i.total - i.paid) FROM invoices i WHERE i.customer_id = c.id AND i.status IN ('open', 'partial')), 0)::text AS balance
		FROM sales_route_stops x JOIN customers c ON c.id = x.customer_id WHERE x.route_id = $1 ORDER BY x.seq`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"route": rt, "stops": stops}, nil
}

func (s *Server) vanGetRoute(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return loadRoute(ctx, tx, cid, id) })
}

// POST /van/routes, PATCH /van/routes/{id} {name, user_id, warehouse_id, weekdays, is_active}
func (s *Server) vanSaveRoute(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        *string `json:"name"`
		UserID      *string `json:"user_id"`
		WarehouseID *string `json:"warehouse_id"`
		Weekdays    *[]int  `json:"weekdays"`
		IsActive    *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	ok := optUUID(in.UserID) && optUUID(in.WarehouseID) && (id == "" || isUUID(id)) && (in.Name == nil || (strings.TrimSpace(*in.Name) != "" && len(*in.Name) <= 120)) &&
		(id != "" || in.Name != nil)
	if in.Weekdays != nil {
		if len(*in.Weekdays) > 7 {
			ok = false
		}
		for _, d := range *in.Weekdays {
			if d < 0 || d > 6 {
				ok = false
			}
		}
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if u := deref(in.UserID); u != "" {
			if err := checkCompanyUser(ctx, tx, cid, u); err != nil {
				return nil, err
			}
		}
		if wh := deref(in.WarehouseID); wh != "" {
			if _, err := checkWarehouse(ctx, tx, cid, wh); err != nil {
				return nil, err
			}
		}
		var name *string
		if in.Name != nil {
			n := strings.TrimSpace(*in.Name)
			name = &n
		}
		if id == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO sales_routes (company_id, name, user_id, warehouse_id, weekdays)
				VALUES ($1, $2, nullif($3, '')::uuid, nullif($4, '')::uuid, coalesce($5::int[], '{}')) RETURNING id::text`,
				cid, *name, deref(in.UserID), deref(in.WarehouseID), in.Weekdays).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE sales_routes SET name = coalesce($3, name),
				user_id = CASE WHEN $4::boolean THEN nullif($5, '')::uuid ELSE user_id END,
				warehouse_id = CASE WHEN $6::boolean THEN nullif($7, '')::uuid ELSE warehouse_id END,
				weekdays = coalesce($8::int[], weekdays), is_active = coalesce($9, is_active) WHERE id = $1 AND company_id = $2`,
				id, cid, name, in.UserID != nil, deref(in.UserID), in.WarehouseID != nil, deref(in.WarehouseID), in.Weekdays, in.IsActive)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return loadRoute(ctx, tx, cid, id)
	})
}

// PUT /van/routes/{id}/stops {customer_ids: [...]} (in visiting order)
func (s *Server) vanRouteStops(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CustomerIDs []string `json:"customer_ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.CustomerIDs) > 500 || !allUUID(in.CustomerIDs) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sales_routes WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, notFound()
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sales_route_stops WHERE route_id = $1`, id); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for i, c := range in.CustomerIDs {
			if seen[c] {
				continue
			}
			seen[c] = true
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE id = $1 AND company_id = $2)`, c, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_customer")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO sales_route_stops (company_id, route_id, customer_id, seq) VALUES ($1, $2, $3, $4)`, cid, id, c, i+1); err != nil {
				return nil, err
			}
		}
		return loadRoute(ctx, tx, cid, id)
	})
}

// resolveVan: the van warehouse the caller works with: the one given (managers: stock.view) or the one assigned to them.
func resolveVan(ctx context.Context, tx pgx.Tx, r *http.Request, cid, given string) (id, name string, err error) {
	me := principal(r).ID
	if given != "" {
		if !isUUID(given) {
			return "", "", badRequest("bad_request")
		}
		var user *string
		var isVan bool
		err = tx.QueryRow(ctx, `SELECT name, is_van, van_user_id::text FROM warehouses WHERE id = $1 AND company_id = $2`, given, cid).Scan(&name, &isVan, &user)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !isVan) {
			return "", "", badRequest("not_a_van")
		}
		if err != nil {
			return "", "", err
		}
		// audit P7-M5: another salesman's van may be looked at with stock.view, but selling from it or closing its
		// day needs stock.manage
		need := "stock.view"
		if r.Method != http.MethodGet {
			need = "stock.manage"
		}
		if (user == nil || *user != me) && !can(r, need) {
			return "", "", &apiError{status: http.StatusForbidden, code: "not_your_van"}
		}
		return given, name, nil
	}
	err = tx.QueryRow(ctx, `SELECT id::text, name FROM warehouses WHERE company_id = $1 AND is_van AND is_active AND van_user_id = $2 ORDER BY name LIMIT 1`,
		cid, me).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", &apiError{status: http.StatusUnprocessableEntity, code: "no_van"}
	}
	return id, name, err
}

// GET /van/today?warehouse_id= : the salesman's day: van, open day, today's customers (route of the weekday) with
// balance and credit, the van's stock with prices, the cash boxes they can use.
func (s *Server) vanToday(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		van, name, err := resolveVan(ctx, tx, r, cid, r.URL.Query().Get("warehouse_id"))
		if err != nil {
			return nil, err
		}
		var base string
		if err := tx.QueryRow(ctx, `SELECT base_currency::text FROM companies WHERE id = $1`, cid).Scan(&base); err != nil {
			return nil, err
		}
		day, err := queryJSONOne(ctx, tx, `SELECT id, number, day, status, opened_at, loaded FROM van_days WHERE warehouse_id = $1 AND status = 'open'`, van)
		if err != nil && !isNotFound(err) {
			return nil, err
		}
		if day == nil {
			day = json.RawMessage("null")
		}
		dow := int(time.Now().In(beirut).Weekday())
		stops, err := queryJSON(ctx, tx, `SELECT DISTINCT ON (c.id) c.id AS customer_id, c.code, c.name, c.phone, c.address, c.lat, c.lng, x.seq, rt.name AS route,
			c.credit_limit::text AS credit_limit, c.credit_hold, c.price_list_id,
			coalesce((SELECT sum(i.total - i.paid) FROM invoices i WHERE i.customer_id = c.id AND i.status IN ('open', 'partial')), 0)::text AS balance
			FROM sales_routes rt JOIN sales_route_stops x ON x.route_id = rt.id JOIN customers c ON c.id = x.customer_id
			WHERE rt.company_id = $1 AND rt.is_active AND (rt.warehouse_id = $2 OR rt.user_id = app_user())
			  AND (cardinality(rt.weekdays) = 0 OR $3 = ANY (rt.weekdays))
			ORDER BY c.id, x.seq`, cid, van, dow)
		if err != nil {
			return nil, err
		}
		stock, err := queryJSON(ctx, tx, `SELECT it.id AS item_id, it.code, it.name, it.unit, it.barcode, s.quantity::text AS quantity,
			coalesce((SELECT ip.price FROM item_prices ip WHERE ip.item_id = it.id AND ip.currency = $3), it.sale_price)::text AS price,
			coalesce(it.sale_currency::text, $3) AS price_currency
			FROM stock s JOIN items it ON it.id = s.item_id WHERE s.warehouse_id = $2 AND it.company_id = $1 AND s.quantity > 0 ORDER BY it.name`, cid, van, base)
		if err != nil {
			return nil, err
		}
		methods, err := queryJSON(ctx, tx, `SELECT id, name, currency, kind FROM payment_methods WHERE company_id = $1 AND is_active
			AND kind IN ('cash', 'wallet', 'card', 'bank') AND (user_id IS NULL OR user_id = app_user()) ORDER BY user_id IS NULL, sort, name`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"van": map[string]string{"id": van, "name": name}, "currency": base, "day": day, "stops": stops, "stock": stock, "methods": methods}, nil
	})
}

type vanLineIn struct {
	ItemID   string `json:"item_id"`
	Quantity string `json:"quantity"`
}

// POST /van/days {warehouse_id, source_warehouse_id, lines}: open the van's day and load it from the main warehouse.
func (s *Server) vanOpenDay(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WarehouseID       string      `json:"warehouse_id"`
		SourceWarehouseID string      `json:"source_warehouse_id"`
		Lines             []vanLineIn `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(in.WarehouseID) || !isUUID(in.SourceWarehouseID) || in.WarehouseID == in.SourceWarehouseID || len(in.Lines) > 300 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		van, _, err := resolveVan(ctx, tx, r, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		if _, err := checkWarehouse(ctx, tx, cid, in.SourceWarehouseID); err != nil {
			return nil, err
		}
		var user *string
		if err := tx.QueryRow(ctx, `SELECT van_user_id::text FROM warehouses WHERE id = $1`, van).Scan(&user); err != nil {
			return nil, err
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'van_day')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var id string
		err = tx.QueryRow(ctx, `INSERT INTO van_days (company_id, number, warehouse_id, source_warehouse_id, user_id) VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text`, cid, n, van, in.SourceWarehouseID, user).Scan(&id)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "van_day_open"}
		}
		if err != nil {
			return nil, err
		}
		if err := vanLoad(ctx, tx, cid, id, van, in.SourceWarehouseID, n, in.Lines); err != nil {
			return nil, err
		}
		return loadVanDay(ctx, tx, cid, id)
	})
}

func vanLoad(ctx context.Context, tx pgx.Tx, cid, day, van, src string, number int64, lines []vanLineIn) error {
	loaded := []map[string]string{}
	for _, l := range lines {
		q, ok := money(l.Quantity)
		if !ok || !q.IsPositive() || !isUUID(l.ItemID) {
			return badRequest("invalid_line")
		}
		if _, err := transferTx(ctx, tx, cid, l.ItemID, src, van, q, today(), fmt.Sprintf("Van day #%d · load", number)); err != nil {
			return err
		}
		loaded = append(loaded, map[string]string{"item_id": l.ItemID, "quantity": q.String()})
	}
	b, _ := json.Marshal(loaded)
	_, err := tx.Exec(ctx, `UPDATE van_days SET loaded = loaded || $2::jsonb WHERE id = $1`, day, string(b))
	return err
}

// POST /van/days/{id}/load {lines}: load more during the day.
func (s *Server) vanLoadMore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines []vanLineIn `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Lines) == 0 || len(in.Lines) > 300 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		van, src, n, err := lockVanDay(ctx, tx, r, cid, id)
		if err != nil {
			return nil, err
		}
		if err := vanLoad(ctx, tx, cid, id, van, src, n, in.Lines); err != nil {
			return nil, err
		}
		return loadVanDay(ctx, tx, cid, id)
	})
}

func lockVanDay(ctx context.Context, tx pgx.Tx, r *http.Request, cid, id string) (van, src string, number int64, err error) {
	if !isUUID(id) {
		return "", "", 0, notFound()
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT warehouse_id::text, source_warehouse_id::text, number, status FROM van_days WHERE id = $1 AND company_id = $2 FOR UPDATE`,
		id, cid).Scan(&van, &src, &number, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", 0, notFound()
	}
	if err != nil {
		return "", "", 0, err
	}
	if status != "open" {
		return "", "", 0, &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	if _, _, err := resolveVan(ctx, tx, r, cid, van); err != nil {
		return "", "", 0, err
	}
	return van, src, number, nil
}

func loadVanDay(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	return queryJSONOne(ctx, tx, `SELECT d.id, d.number, d.day, d.status, d.warehouse_id, w.name AS warehouse, d.source_warehouse_id, sw.name AS source_warehouse,
		d.user_id, u.full_name AS user_name, d.loaded, d.summary, d.count_id, d.opened_at, d.closed_at
		FROM van_days d JOIN warehouses w ON w.id = d.warehouse_id JOIN warehouses sw ON sw.id = d.source_warehouse_id LEFT JOIN users u ON u.id = d.user_id
		WHERE d.id = $1 AND d.company_id = $2`, id, cid)
}

// GET /van/days?from=&to=
func (s *Server) vanDays(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT d.id, d.number, d.day, d.status, w.name AS warehouse, u.full_name AS user_name, d.summary, d.opened_at, d.closed_at
			FROM van_days d JOIN warehouses w ON w.id = d.warehouse_id LEFT JOIN users u ON u.id = d.user_id
			WHERE d.company_id = $1 AND d.day BETWEEN $2::date AND $3::date ORDER BY d.day DESC, d.number DESC LIMIT 500`, r.PathValue("cid"), from, to)
	})
}

// vanPrice: the customer's price list (best quantity break) in cur, else the item's price in cur, else its sale price.
func vanPrice(ctx context.Context, tx pgx.Tx, rc *rateCache, cid, customer, item, cur string, qty decimal.Decimal) (decimal.Decimal, error) {
	var pl string
	err := tx.QueryRow(ctx, `SELECT x.price::text FROM customers c JOIN price_lists p ON p.id = c.price_list_id AND p.is_active
		JOIN price_list_items x ON x.list_id = p.id AND x.item_id = $2 WHERE c.id = $1 AND p.currency = $3 AND x.min_qty <= $4::numeric
		ORDER BY x.min_qty DESC LIMIT 1`, customer, item, cur, qty.String()).Scan(&pl)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return decimal.Zero, err
	}
	var listPrice *string
	var salePrice, saleCur string
	if err := tx.QueryRow(ctx, `SELECT (SELECT price::text FROM item_prices WHERE item_id = it.id AND currency = $3), it.sale_price::text,
		coalesce(it.sale_currency::text, $3) FROM items it WHERE it.id = $1 AND it.company_id = $2`, item, cid, cur).Scan(&listPrice, &salePrice, &saleCur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return decimal.Zero, badRequest("invalid_item")
		}
		return decimal.Zero, err
	}
	if pl != "" {
		listPrice = &pl
	}
	return itemPriceIn(rc, listPrice, salePrice, saleCur, cur)
}

// POST /van/sales {client_ref, customer_id, warehouse_id, lines: [{item_id, quantity}], method_id, paid, notes}: an
// invoice out of the van at the customer's prices; what is not paid stays on account (credit limit checked). Sent
// again with the same client_ref (phone back online) it returns the first one.
func (s *Server) vanSale(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientRef   string      `json:"client_ref"`
		CustomerID  string      `json:"customer_id"`
		WarehouseID string      `json:"warehouse_id"`
		Lines       []vanLineIn `json:"lines"`
		MethodID    string      `json:"method_id"`
		Paid        string      `json:"paid"`
		Notes       string      `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.ClientRef = strings.TrimSpace(in.ClientRef)
	paid := decimal.Zero
	if strings.TrimSpace(in.Paid) != "" {
		p, ok := money(in.Paid)
		if !ok || p.IsNegative() {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		paid = p
	}
	if !isUUID(in.CustomerID) || len(in.Lines) == 0 || len(in.Lines) > 100 || (in.ClientRef != "" && (len(in.ClientRef) < 8 || len(in.ClientRef) > 64)) ||
		(paid.IsPositive() && !isUUID(in.MethodID)) || len(in.Notes) > 300 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	me := principal(r).ID
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.ClientRef != "" {
			var existing string
			if err := lockClientRef(ctx, tx, cid, in.ClientRef); err != nil {
				return nil, err
			}
			err := tx.QueryRow(ctx, `SELECT id::text FROM invoices WHERE company_id = $1 AND external_ref = $2`, cid, "van:"+in.ClientRef).Scan(&existing)
			if err == nil {
				return vanSaleResult(ctx, tx, cid, existing)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
		}
		van, vanName, err := resolveVan(ctx, tx, r, cid, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		var open bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM van_days WHERE warehouse_id = $1 AND status = 'open')`, van).Scan(&open); err != nil {
			return nil, err
		}
		if !open {
			return nil, &apiError{status: http.StatusConflict, code: "van_day_not_open"}
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rc := newRateCache(ctx, tx, cid, set.Base, today())
		inv := invoiceIn{CustomerID: in.CustomerID, IssueDate: today(), Currency: set.Base, Notes: strings.TrimSpace("Van sale · " + vanName + " " + in.Notes)}
		for _, l := range in.Lines {
			q, ok := money(l.Quantity)
			if !ok || !q.IsPositive() || !isUUID(l.ItemID) {
				return nil, badRequest("invalid_line")
			}
			p, err := vanPrice(ctx, tx, rc, cid, in.CustomerID, l.ItemID, set.Base, q)
			if err != nil {
				return nil, err
			}
			inv.Lines = append(inv.Lines, invoiceLineIn{ItemID: l.ItemID, Quantity: q.String(), UnitPrice: p.String(), WarehouseID: van})
		}
		ictx := withSalesman(ctx, me)
		if in.ClientRef != "" {
			ictx = withExternalRef(ictx, "van:"+in.ClientRef)
		}
		invID, err := createInvoiceTx(ictx, tx, nil, cid, &inv) // credit is checked below, on what stays unpaid
		if err != nil {
			return nil, err
		}
		var totalS string
		if err := tx.QueryRow(ctx, `SELECT total::text FROM invoices WHERE id = $1`, invID).Scan(&totalS); err != nil {
			return nil, err
		}
		total, _ := decimal.NewFromString(totalS)
		if paid.IsPositive() {
			pin := paymentIn{CustomerID: in.CustomerID, Date: today(), Currency: set.Base, Tenders: []tenderIn{{MethodID: in.MethodID, Amount: paid.String()}},
				InvoiceID: invID, Notes: "Van sale · " + vanName}
			if in.ClientRef != "" {
				pin.ClientRef = "van:" + in.ClientRef
			}
			if _, err := receivePayment(ctx, tx, cid, pin, false, me); err != nil {
				return nil, err
			}
		}
		var paidS string
		if err := tx.QueryRow(ctx, `SELECT paid::text FROM invoices WHERE id = $1`, invID).Scan(&paidS); err != nil {
			return nil, err
		}
		pd, _ := decimal.NewFromString(paidS)
		if rest := total.Sub(pd); rest.IsPositive() {
			if err := checkCredit(ctx, tx, r, cid, in.CustomerID, set.Base, rest); err != nil {
				return nil, err
			}
		}
		return vanSaleResult(ctx, tx, cid, invID)
	})
}

func vanSaleResult(ctx context.Context, tx pgx.Tx, cid, invoiceID string) (any, error) {
	inv, err := loadInvoice(ctx, tx, cid, invoiceID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"invoice": inv}, nil
}

// POST /van/days/{id}/close {counted: [{item_id, quantity}], unload}: the salesman's count becomes a stock count of the
// van (differences booked), then what is left goes back to the main warehouse; the day's summary is kept.
func (s *Server) vanCloseDay(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Counted []vanLineIn `json:"counted"`
		Unload  bool        `json:"unload"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Counted) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		van, src, number, err := lockVanDay(ctx, tx, r, cid, id)
		if err != nil {
			return nil, err
		}
		var opened time.Time
		if err := tx.QueryRow(ctx, `SELECT opened_at FROM van_days WHERE id = $1`, id).Scan(&opened); err != nil {
			return nil, err
		}
		date := today()
		// a count left waiting for approval at an earlier attempt is replaced by this one
		if _, err := tx.Exec(ctx, `UPDATE stock_counts SET status = 'cancelled' WHERE id = (SELECT count_id FROM van_days WHERE id = $1) AND status = 'draft'`, id); err != nil {
			return nil, err
		}
		var cn int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'stock_count')`, cid).Scan(&cn); err != nil {
			return nil, err
		}
		var count string
		if err := tx.QueryRow(ctx, `INSERT INTO stock_counts (company_id, number, warehouse_id, count_date, notes) VALUES ($1, $2, $3, $4::date, $5)
			RETURNING id::text`, cid, cn, van, date, fmt.Sprintf("Van day #%d", number)).Scan(&count); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stock_count_lines (company_id, count_id, item_id, expected)
			SELECT $1, $2, s.item_id, s.quantity FROM stock s JOIN items i ON i.id = s.item_id WHERE s.warehouse_id = $3 AND s.quantity <> 0 AND i.kind = 'stock'`,
			cid, count, van); err != nil {
			return nil, err
		}
		for _, c := range in.Counted {
			q, ok := money(c.Quantity)
			if !ok || q.IsNegative() || !isUUID(c.ItemID) {
				return nil, badRequest("invalid_line")
			}
			if _, err := loadItem(ctx, tx, cid, c.ItemID); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO stock_count_lines (company_id, count_id, item_id, expected, counted)
				SELECT $1, $2, $3, 0, $4::numeric WHERE NOT EXISTS (SELECT 1 FROM stock_count_lines WHERE count_id = $2 AND item_id = $3)`,
				cid, count, c.ItemID, q.String()); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE stock_count_lines SET counted = $3::numeric WHERE count_id = $1 AND item_id = $2`, count, c.ItemID, q.String()); err != nil {
				return nil, err
			}
		}
		diffs, err := queryJSON(ctx, tx, `SELECT l.item_id, it.name AS item_name, l.expected::text AS expected, l.counted::text AS counted,
			(l.counted - l.expected)::text AS difference FROM stock_count_lines l JOIN items it ON it.id = l.item_id
			WHERE l.count_id = $1 AND l.counted IS NOT NULL AND l.counted <> l.expected ORDER BY it.name`, count)
		if err != nil {
			return nil, err
		}
		// audit P7-M5: a shortage the salesman counted himself is not written off by him. The count stays a draft
		// for a stock manager (notified), and the day stays open until a manager closes it.
		var short bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stock_count_lines WHERE count_id = $1 AND counted IS NOT NULL AND counted < expected)`,
			count).Scan(&short); err != nil {
			return nil, err
		}
		if short && !can(r, "stock.manage") {
			if err := notifyStaff(ctx, tx, cid, "stock.manage", "system", fmt.Sprintf("Van day #%d: shortage to approve", number),
				fmt.Sprintf("Stock count #%d", cn), "stock_count", count); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE van_days SET count_id = $2 WHERE id = $1`, id, count); err != nil {
				return nil, err
			}
			day, err := loadVanDay(ctx, tx, cid, id)
			if err != nil {
				return nil, err
			}
			return map[string]any{"day": day, "pending_approval": true, "count_id": count, "differences": diffs}, nil
		}
		if err := postCountTx(ctx, tx, cid, count, van, date, cn); err != nil {
			return nil, err
		}
		unloaded := []map[string]string{}
		if in.Unload {
			rows, err := tx.Query(ctx, `SELECT s.item_id::text, s.quantity::text FROM stock s JOIN items i ON i.id = s.item_id
				WHERE s.warehouse_id = $1 AND s.quantity > 0 AND i.kind = 'stock' ORDER BY i.name`, van)
			if err != nil {
				return nil, err
			}
			type left struct{ item, qty string }
			lefts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (left, error) {
				var x left
				err := r.Scan(&x.item, &x.qty)
				return x, err
			})
			if err != nil {
				return nil, err
			}
			for _, l := range lefts {
				q, _ := decimal.NewFromString(l.qty)
				if _, err := transferTx(ctx, tx, cid, l.item, van, src, q, date, fmt.Sprintf("Van day #%d · unload", number)); err != nil {
					return nil, err
				}
				unloaded = append(unloaded, map[string]string{"item_id": l.item, "quantity": q.String()})
			}
		}
		var sales int
		var salesTotal, collected string
		if err := tx.QueryRow(ctx, `WITH v AS (SELECT i.id, i.total FROM invoices i WHERE i.company_id = $1 AND i.status <> 'void' AND i.external_ref LIKE 'van:%'
			  AND i.created_at >= $3 AND EXISTS (SELECT 1 FROM invoice_lines l WHERE l.invoice_id = i.id AND l.warehouse_id = $2))
			SELECT count(*), coalesce(sum(total), 0)::text, coalesce((SELECT sum(p.amount) FROM payments p WHERE p.invoice_id IN (SELECT id FROM v) AND p.status = 'posted'), 0)::text
			FROM v`, cid, van, opened).Scan(&sales, &salesTotal, &collected); err != nil {
			return nil, err
		}
		sum, _ := json.Marshal(map[string]any{"sales": sales, "sales_total": salesTotal, "collected": collected, "differences": diffs, "unloaded": unloaded})
		if _, err := tx.Exec(ctx, `UPDATE van_days SET status = 'closed', closed_at = now(), summary = $2, count_id = $3 WHERE id = $1`, id, sum, count); err != nil {
			return nil, err
		}
		return loadVanDay(ctx, tx, cid, id)
	})
}

// GET /van/commission?from=&to= : sales per salesman (van sales and orders of their own) and the commission at the
// employee's commission %, on what was sold and on what was collected.
func (s *Server) vanCommission(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `WITH x AS (
			  -- audit P7-M1: returns (credit notes) come off the sale, and only real payments count as collected
			  SELECT y.user_id, y.currency, y.total - y.credited AS total, greatest(least(y.paid, y.total - y.credited), 0) AS paid,
			    CASE WHEN y.total > 0 THEN y.tax * (y.total - y.credited) / y.total ELSE 0 END AS tax
			  FROM (
			    SELECT coalesce(i.salesman_id, so.salesperson_id) AS user_id, i.currency, i.total,
			      (SELECT coalesce(sum(l.tax_amount), 0) FROM invoice_lines l WHERE l.invoice_id = i.id) AS tax,
			      (SELECT coalesce(sum(p.amount), 0) FROM payments p WHERE p.invoice_id = i.id AND p.kind = 'credit_note' AND p.status = 'posted') AS credited,
			      (SELECT coalesce(sum(a.amount), 0) FROM payment_allocations a JOIN payments p ON p.id = a.payment_id
			        WHERE a.invoice_id = i.id AND p.kind = 'payment' AND p.status = 'posted') AS paid
			    FROM invoices i LEFT JOIN sales_orders so ON so.id = i.sales_order_id
			    WHERE i.company_id = $1 AND i.kind = 'invoice' AND i.status <> 'void' AND i.issue_date BETWEEN $2::date AND $3::date
			      AND coalesce(i.salesman_id, so.salesperson_id) IS NOT NULL) y)
			SELECT u.id AS user_id, u.full_name, x.currency, count(*) AS invoices, sum(x.total - x.tax)::text AS net, sum(x.paid)::text AS collected,
			  coalesce((SELECT e.commission_pct FROM employees e WHERE e.user_id = u.id AND e.company_id = $1 ORDER BY e.status = 'active' DESC LIMIT 1), 0)::text AS commission_pct,
			  round(sum(x.total - x.tax) * coalesce((SELECT e.commission_pct FROM employees e WHERE e.user_id = u.id AND e.company_id = $1
			    ORDER BY e.status = 'active' DESC LIMIT 1), 0) / 100, 2)::text AS commission_on_sales,
			  round(sum(x.paid * CASE WHEN x.total > 0 THEN (x.total - x.tax) / x.total ELSE 0 END) * coalesce((SELECT e.commission_pct FROM employees e
			    WHERE e.user_id = u.id AND e.company_id = $1 ORDER BY e.status = 'active' DESC LIMIT 1), 0) / 100, 2)::text AS commission_on_collected
			FROM x JOIN users u ON u.id = x.user_id GROUP BY u.id, u.full_name, x.currency ORDER BY sum(x.total) DESC`, r.PathValue("cid"), from, to)
	})
}
