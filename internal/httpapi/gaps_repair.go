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

// Repair orders (electronics / mobile shops, generator and router repairs): the device or serial number, the fault,
// a diagnosis, parts from stock and labour. A serial sold by us is checked against its sale date and the item's
// warranty months. Status: received → diagnosing → waiting_parts → repaired → delivered (or cancelled). The
// customer gets a WhatsApp message on each step (if messaging is on). Invoicing bills the chargeable lines; parts
// covered by the warranty leave stock as a stock adjustment (warranty cost), never on the customer's invoice.

var repairStatuses = []string{"received", "diagnosing", "waiting_parts", "repaired", "delivered", "cancelled"}

const repairCols = `r.id, r.number, r.customer_id, c.code AS customer_code, c.name AS customer_name, c.phone AS customer_phone, r.item_id,
	it.name AS item_name, r.device, r.serial, r.accessories, r.fault, r.diagnosis, r.status, r.sale_date, r.warranty_until, r.under_warranty,
	r.promised_date, r.currency, r.notify, r.invoice_id, inv.number AS invoice_number, inv.status AS invoice_status, r.assigned_to,
	u.full_name AS assigned_name, r.received_at, r.repaired_at, r.delivered_at, r.created_at,
	(SELECT coalesce(sum(l.quantity * l.unit_price) FILTER (WHERE NOT l.covered), 0) FROM repair_lines l WHERE l.repair_id = r.id)::text AS to_charge,
	(r.promised_date IS NOT NULL AND r.promised_date < current_date AND r.status NOT IN ('repaired', 'delivered', 'cancelled')) AS late`

const repairFrom = ` FROM repair_orders r JOIN customers c ON c.id = r.customer_id LEFT JOIN items it ON it.id = r.item_id
	LEFT JOIN invoices inv ON inv.id = r.invoice_id LEFT JOIN users u ON u.id = r.assigned_to`

// warrantyLookup: what we know about a serial number we sold (item, sale date, warranty).
func warrantyLookup(ctx context.Context, tx pgx.Tx, cid, serial string) (map[string]any, error) {
	var itemID, itemName string
	var months int
	var sold *time.Time
	var customer *string
	err := tx.QueryRow(ctx, `SELECT l.item_id::text, it.name, it.warranty_months,
		(SELECT m.move_date FROM stock_move_lots ml JOIN stock_moves m ON m.id = ml.move_id
		  WHERE ml.lot_id = l.id AND m.quantity < 0 AND m.source = 'sale' ORDER BY m.move_date DESC, m.created_at DESC LIMIT 1),
		(SELECT i.customer_id::text FROM stock_move_lots ml JOIN stock_moves m ON m.id = ml.move_id JOIN invoices i ON i.id = m.source_id
		  WHERE ml.lot_id = l.id AND m.quantity < 0 AND m.source = 'sale' ORDER BY m.move_date DESC, m.created_at DESC LIMIT 1)
		FROM stock_lots l JOIN items it ON it.id = l.item_id WHERE l.company_id = $1 AND lower(l.lot_no) = lower($2) LIMIT 1`, cid, strings.TrimSpace(serial)).
		Scan(&itemID, &itemName, &months, &sold, &customer)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"found": false}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{"found": true, "item_id": itemID, "item_name": itemName, "warranty_months": months, "customer_id": customer}
	if sold != nil {
		until := addMonthsClamped(*sold, months)
		out["sale_date"] = sold.Format(time.DateOnly)
		out["warranty_until"] = until.Format(time.DateOnly)
		out["under_warranty"] = months > 0 && !until.Before(time.Now().Truncate(24*time.Hour))
	}
	return out, nil
}

// GET /companies/{cid}/repairs/warranty?serial=
func (s *Server) repairWarranty(w http.ResponseWriter, r *http.Request) {
	serial := strings.TrimSpace(r.URL.Query().Get("serial"))
	if serial == "" || len(serial) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		info, err := warrantyLookup(ctx, tx, r.PathValue("cid"), serial)
		if err != nil {
			return nil, err
		}
		hist, err := queryJSON(ctx, tx, `SELECT id, number, status, fault, received_at FROM repair_orders WHERE company_id = $1 AND lower(serial) = lower($2)
			ORDER BY received_at DESC LIMIT 20`, r.PathValue("cid"), serial)
		if err != nil {
			return nil, err
		}
		info["repairs"] = hist
		return info, nil
	})
}

func (s *Server) listRepairs(w http.ResponseWriter, r *http.Request) {
	status, q := r.URL.Query().Get("status"), strings.TrimSpace(r.URL.Query().Get("q"))
	if status != "" && status != "open" && status != "all" && !contains(repairStatuses, status) || len(q) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+repairCols+repairFrom+` WHERE r.company_id = $1
			AND ($2 IN ('', 'all') OR ($2 = 'open' AND r.status NOT IN ('delivered', 'cancelled')) OR r.status = $2)
			AND ($3 = '' OR r.serial ILIKE '%' || $3 || '%' OR c.name ILIKE '%' || $3 || '%' OR c.phone ILIKE '%' || $3 || '%' OR r.device ILIKE '%' || $3 || '%'
			     OR r.number::text = $3)
			ORDER BY r.status IN ('delivered', 'cancelled'), r.received_at DESC LIMIT 500`, r.PathValue("cid"), status, q)
	})
}

func loadRepair(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	rep, err := queryJSONOne(ctx, tx, `SELECT `+repairCols+repairFrom+` WHERE r.id = $1 AND r.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.kind, l.item_id, it.code AS item_code, l.warehouse_id, l.description,
		l.quantity::text AS quantity, l.unit_price::text AS unit_price, l.covered, (l.quantity * l.unit_price)::text AS amount
		FROM repair_lines l LEFT JOIN items it ON it.id = l.item_id WHERE l.repair_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"repair": rep, "lines": lines}, nil
}

func (s *Server) getRepair(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return loadRepair(ctx, tx, cid, id) })
}

type repairIn struct {
	CustomerID   *string `json:"customer_id"`
	ItemID       *string `json:"item_id"`
	Device       *string `json:"device"`
	Serial       *string `json:"serial"`
	Accessories  *string `json:"accessories"`
	Fault        *string `json:"fault"`
	Diagnosis    *string `json:"diagnosis"`
	Status       *string `json:"status"`
	PromisedDate *string `json:"promised_date"`
	SaleDate     *string `json:"sale_date"`
	Currency     *string `json:"currency"`
	Notify       *bool   `json:"notify"`
	AssignedTo   *string `json:"assigned_to"`
}

func (in *repairIn) valid() bool {
	for _, p := range []*string{in.PromisedDate, in.SaleDate} {
		if p != nil && *p != "" && !validDate(*p) {
			return false
		}
	}
	return optUUID(in.CustomerID) && optUUID(in.ItemID) && optUUID(in.AssignedTo) && len(deref(in.Device)) <= 200 && len(deref(in.Serial)) <= 100 &&
		len(deref(in.Accessories)) <= 500 && len(deref(in.Fault)) <= 2000 && len(deref(in.Diagnosis)) <= 4000 &&
		(in.Status == nil || contains(repairStatuses, *in.Status)) && (in.Currency == nil || *in.Currency == "" || currencyRe.MatchString(*in.Currency))
}

// applyWarranty sets sale date / warranty from the serial (sold by us) or the given sale date + the item's months.
func applyWarranty(ctx context.Context, tx pgx.Tx, cid, id string) error {
	var serial string
	var itemID *string
	var sale *time.Time
	var received time.Time
	if err := tx.QueryRow(ctx, `SELECT serial, item_id::text, sale_date, received_at FROM repair_orders WHERE id = $1`, id).Scan(&serial, &itemID, &sale, &received); err != nil {
		return err
	}
	if serial != "" {
		info, err := warrantyLookup(ctx, tx, cid, serial)
		if err != nil {
			return err
		}
		if info["found"] == true {
			if itemID == nil {
				it := info["item_id"].(string)
				itemID = &it
				if _, err := tx.Exec(ctx, `UPDATE repair_orders SET item_id = $2 WHERE id = $1`, id, it); err != nil {
					return err
				}
			}
			if sd, ok := info["sale_date"].(string); ok && sale == nil {
				t, _ := time.Parse(time.DateOnly, sd)
				sale = &t
			}
		}
	}
	months := 0
	if itemID != nil {
		if err := tx.QueryRow(ctx, `SELECT warranty_months FROM items WHERE id = $1`, *itemID).Scan(&months); err != nil {
			return err
		}
	}
	var until *string
	under := false
	var saleS *string
	if sale != nil {
		ss := sale.Format(time.DateOnly)
		saleS = &ss
		if months > 0 {
			u := addMonthsClamped(*sale, months)
			us := u.Format(time.DateOnly)
			until = &us
			under = !u.Before(received.Truncate(24 * time.Hour))
		}
	}
	_, err := tx.Exec(ctx, `UPDATE repair_orders SET sale_date = $2::date, warranty_until = $3::date, under_warranty = $4 WHERE id = $1`, id, saleS, until, under)
	return err
}

func (s *Server) createRepair(w http.ResponseWriter, r *http.Request) {
	var in repairIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid() || deref(in.CustomerID) == "" || strings.TrimSpace(deref(in.Fault)) == "" || (in.Status != nil && *in.Status != "received") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := lockCustomer(ctx, tx, cid, *in.CustomerID); err != nil {
			return nil, err
		}
		if in.ItemID != nil && *in.ItemID != "" {
			if _, err := loadItem(ctx, tx, cid, *in.ItemID); err != nil {
				return nil, err
			}
		}
		if a := deref(in.AssignedTo); a != "" {
			if err := checkCompanyUser(ctx, tx, cid, a); err != nil {
				return nil, err
			}
		}
		cur := deref(in.Currency)
		if cur == "" {
			if err := tx.QueryRow(ctx, `SELECT base_currency::text FROM companies WHERE id = $1`, cid).Scan(&cur); err != nil {
				return nil, err
			}
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'repair')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		notify := true
		if in.Notify != nil {
			notify = *in.Notify
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO repair_orders (company_id, number, customer_id, item_id, device, serial, accessories, fault,
			promised_date, sale_date, currency, notify, assigned_to)
			VALUES ($1, $2, $3, nullif($4, '')::uuid, $5, $6, $7, $8, nullif($9, '')::date, nullif($10, '')::date, $11, $12, nullif($13, '')::uuid)
			RETURNING id::text`, cid, number, *in.CustomerID, deref(in.ItemID), strings.TrimSpace(deref(in.Device)), strings.TrimSpace(deref(in.Serial)),
			strings.TrimSpace(deref(in.Accessories)), strings.TrimSpace(deref(in.Fault)), deref(in.PromisedDate), deref(in.SaleDate), cur, notify,
			deref(in.AssignedTo)).Scan(&id); err != nil {
			return nil, err
		}
		if err := applyWarranty(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		if err := repairMessage(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		return loadRepair(ctx, tx, cid, id)
	})
}

func (s *Server) updateRepair(w http.ResponseWriter, r *http.Request) {
	var in repairIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !in.valid() || in.CustomerID != nil || (in.Fault != nil && strings.TrimSpace(*in.Fault) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM repair_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if (status == "delivered" || status == "cancelled") && (in.Status == nil || *in.Status == status) && in.Diagnosis == nil {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.ItemID != nil && *in.ItemID != "" {
			if _, err := loadItem(ctx, tx, cid, *in.ItemID); err != nil {
				return nil, err
			}
		}
		if a := deref(in.AssignedTo); a != "" {
			if err := checkCompanyUser(ctx, tx, cid, a); err != nil {
				return nil, err
			}
		}
		if in.Status != nil && *in.Status == "delivered" {
			var toCharge string
			var inv *string
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(quantity * unit_price) FILTER (WHERE NOT covered), 0)::text,
				(SELECT invoice_id::text FROM repair_orders WHERE id = $1) FROM repair_lines WHERE repair_id = $1`, id).Scan(&toCharge, &inv); err != nil {
				return nil, err
			}
			if d, _ := decimal.NewFromString(toCharge); d.IsPositive() && inv == nil {
				return nil, &apiError{status: http.StatusConflict, code: "repair_not_invoiced"}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE repair_orders SET item_id = CASE WHEN $3::text IS NULL THEN item_id ELSE nullif($3, '')::uuid END,
			device = coalesce(trim($4), device), serial = coalesce(trim($5), serial), accessories = coalesce(trim($6), accessories),
			fault = coalesce(trim($7), fault), diagnosis = coalesce($8, diagnosis), status = coalesce($9, status),
			promised_date = CASE WHEN $10::text IS NULL THEN promised_date ELSE nullif($10, '')::date END,
			sale_date = CASE WHEN $11::text IS NULL THEN sale_date ELSE nullif($11, '')::date END,
			notify = coalesce($12, notify), assigned_to = CASE WHEN $13::text IS NULL THEN assigned_to ELSE nullif($13, '')::uuid END,
			repaired_at = CASE WHEN $9 = 'repaired' AND repaired_at IS NULL THEN now() ELSE repaired_at END,
			delivered_at = CASE WHEN $9 = 'delivered' THEN now() ELSE delivered_at END, updated_at = now()
			WHERE id = $1 AND company_id = $2`, id, cid, in.ItemID, in.Device, in.Serial, in.Accessories, in.Fault, in.Diagnosis, in.Status,
			in.PromisedDate, in.SaleDate, in.Notify, in.AssignedTo); err != nil {
			return nil, err
		}
		if in.Serial != nil || in.SaleDate != nil || in.ItemID != nil {
			if err := applyWarranty(ctx, tx, cid, id); err != nil {
				return nil, err
			}
		}
		if in.Status != nil && *in.Status != status {
			if err := repairMessage(ctx, tx, cid, id); err != nil {
				return nil, err
			}
		}
		return loadRepair(ctx, tx, cid, id)
	})
}

// PUT /companies/{cid}/repairs/{id}/lines {lines: [...]}: parts and labour (until invoiced).
func (s *Server) putRepairLines(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines []struct {
			Kind        string `json:"kind"`
			ItemID      string `json:"item_id"`
			WarehouseID string `json:"warehouse_id"`
			Description string `json:"description"`
			Quantity    string `json:"quantity"`
			UnitPrice   string `json:"unit_price"`
			Covered     bool   `json:"covered"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || len(in.Lines) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		var inv, wdoc *string
		err := tx.QueryRow(ctx, `SELECT status, invoice_id::text, warranty_doc::text FROM repair_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&status, &inv, &wdoc)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "delivered" || status == "cancelled" || wdoc != nil {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if inv != nil {
			var ist string
			if err := tx.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, *inv).Scan(&ist); err != nil {
				return nil, err
			}
			if ist != "void" {
				return nil, &apiError{status: http.StatusConflict, code: "repair_invoiced"}
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM repair_lines WHERE repair_id = $1`, id); err != nil {
			return nil, err
		}
		for i, l := range in.Lines {
			q, ok1 := money(l.Quantity)
			p, ok2 := money(l.UnitPrice)
			if !ok2 && strings.TrimSpace(l.UnitPrice) == "" {
				p, ok2 = decimal.Zero, true
			}
			desc := strings.TrimSpace(l.Description)
			var item, wh *string
			if l.Kind != "part" && l.Kind != "labour" {
				return nil, badRequest("invalid_line")
			}
			if l.ItemID != "" {
				it, err := loadItem(ctx, tx, cid, l.ItemID)
				if err != nil {
					return nil, err
				}
				item = &it.ID
				if desc == "" {
					desc = it.Name
				}
				if it.Kind == "stock" {
					wv, err := checkWarehouse(ctx, tx, cid, l.WarehouseID)
					if err != nil {
						return nil, err
					}
					wh = &wv
				}
			}
			if !ok1 || !ok2 || !q.IsPositive() || desc == "" || len(desc) > 300 {
				return nil, badRequest("invalid_line")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO repair_lines (company_id, repair_id, line_no, kind, item_id, warehouse_id, description, quantity, unit_price, covered)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10)`, cid, id, i+1, l.Kind, item, wh, desc, q.String(), p.String(), l.Covered); err != nil {
				return nil, err
			}
		}
		return loadRepair(ctx, tx, cid, id)
	})
}

// POST /companies/{cid}/repairs/{id}/invoice {date}: bills the chargeable lines (a normal invoice: stock parts leave
// stock with it) and takes warranty-covered parts out of stock as a warranty cost.
func (s *Server) invoiceRepair(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date string `json:"date"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !validDate(in.Date) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, customer, currency, device, serial string
		var number int64
		var inv, wdoc *string
		err := tx.QueryRow(ctx, `SELECT status, customer_id::text, currency::text, number, device, serial, invoice_id::text, warranty_doc::text
			FROM repair_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&status, &customer, &currency, &number, &device, &serial, &inv, &wdoc)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "cancelled" || status == "delivered" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if inv != nil {
			var ist string
			if err := tx.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, *inv).Scan(&ist); err != nil {
				return nil, err
			}
			if ist != "void" {
				return nil, &apiError{status: http.StatusConflict, code: "repair_invoiced"}
			}
		}
		rows, err := tx.Query(ctx, `SELECT kind, coalesce(item_id::text, ''), coalesce(warehouse_id::text, ''), description, quantity::text, unit_price::text, covered
			FROM repair_lines WHERE repair_id = $1 ORDER BY line_no`, id)
		if err != nil {
			return nil, err
		}
		type rl struct {
			kind, item, wh, desc, qty, price string
			covered                          bool
		}
		lines, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (rl, error) {
			var x rl
			err := row.Scan(&x.kind, &x.item, &x.wh, &x.desc, &x.qty, &x.price, &x.covered)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		if len(lines) == 0 {
			return nil, badRequest("nothing_to_invoice")
		}
		label := fmt.Sprintf("Repair #%d", number)
		if device != "" {
			label += " · " + device
		}
		if serial != "" {
			label += " · S/N " + serial
		}
		bill := invoiceIn{CustomerID: customer, IssueDate: in.Date, Currency: currency, Notes: label}
		for _, l := range lines {
			p, _ := decimal.NewFromString(l.price)
			if l.covered || !p.IsPositive() {
				continue
			}
			bill.Lines = append(bill.Lines, invoiceLineIn{Description: l.desc, Quantity: l.qty, UnitPrice: l.price, ItemID: l.item, WarehouseID: l.wh})
		}
		var invID *string
		if len(bill.Lines) > 0 {
			iid, err := createInvoiceTx(ctx, tx, r, cid, &bill)
			if err != nil {
				return nil, err
			}
			invID = &iid
		}
		// warranty parts (and free parts) leave stock as a warranty cost
		if wdoc == nil {
			var first *string
			for _, l := range lines {
				p, _ := decimal.NewFromString(l.price)
				if l.kind != "part" || l.item == "" || l.wh == "" || (!l.covered && p.IsPositive()) {
					continue
				}
				q, _ := decimal.NewFromString(l.qty)
				doc, err := warrantyStockOut(ctx, tx, cid, id, l.item, l.wh, q, in.Date, label)
				if err != nil {
					return nil, err
				}
				if doc != "" && first == nil {
					first = &doc
				}
			}
			if first != nil {
				if _, err := tx.Exec(ctx, `UPDATE repair_orders SET warranty_doc = $2 WHERE id = $1`, id, *first); err != nil {
					return nil, err
				}
			}
		}
		if invID == nil && wdoc != nil {
			return nil, badRequest("nothing_to_invoice")
		}
		if _, err := tx.Exec(ctx, `UPDATE repair_orders SET invoice_id = coalesce($2::uuid, invoice_id), updated_at = now() WHERE id = $1`, id, invID); err != nil {
			return nil, err
		}
		return loadRepair(ctx, tx, cid, id)
	})
}

// warrantyStockOut: one part covered by the warranty leaves stock (Dr stock adjustments / Cr inventory).
func warrantyStockOut(ctx context.Context, tx pgx.Tx, cid, repairID, itemID, wh string, qty decimal.Decimal, date, label string) (string, error) {
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return "", err
	}
	ps, err := loadPurchSettings(ctx, tx, cid)
	if err != nil {
		return "", err
	}
	it, err := loadItem(ctx, tx, cid, itemID)
	if err != nil {
		return "", err
	}
	if it.Kind != "stock" || it.Inventory == nil {
		return "", nil // services / consumables have no stock to take out
	}
	if ps.Adjust == nil {
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
	var number int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'stock_adjust')`, cid).Scan(&number); err != nil {
		return "", err
	}
	memo := fmt.Sprintf("Warranty · %s · %s %s %s", label, qty.String(), it.Unit, it.Name)
	var je *string
	if value.IsPositive() {
		id, err := postJournal(ctx, tx, cid, set, date, memo, fmt.Sprintf("STK-%d", number), "stock", []jline{
			{Account: *ps.Adjust, Desc: memo, Currency: set.Base, Debit: value},
			{Account: *it.Inventory, Desc: it.Name, Currency: set.Base, Credit: value}})
		if err != nil {
			return "", err
		}
		je = &id
	}
	var docID string
	if err := tx.QueryRow(ctx, `INSERT INTO stock_docs (company_id, number, kind, doc_date, item_id, warehouse_id, quantity, value, account_id, notes,
		journal_entry_id, repair_id) VALUES ($1, $2, 'adjust', $3::date, $4, $5, $6::numeric, $7::numeric, $8, $9, $10, $11) RETURNING id::text`,
		cid, number, date, it.ID, wh, qty.Neg().String(), value.Neg().String(), *ps.Adjust, memo, je, repairID).Scan(&docID); err != nil {
		return "", err
	}
	if err := stockOut(ctx, tx, cid, it.ID, wh, qty, avg, value, date, "adjust", docID, je, memo); err != nil {
		return "", err
	}
	return docID, nil
}

// repairMessage queues the WhatsApp / SMS status update for the customer (messaging app on, customer reachable).
func repairMessage(ctx context.Context, tx pgx.Tx, cid, id string) error {
	var status, customer, device, lang string
	var number int64
	var notify, msgApp bool
	if err := tx.QueryRow(ctx, `SELECT r.status, r.customer_id::text, r.device, r.number, r.notify, 'msg' = ANY (co.apps), coalesce(c.locale, co.default_locale)
		FROM repair_orders r JOIN companies co ON co.id = r.company_id JOIN customers c ON c.id = r.customer_id WHERE r.id = $1`, id).
		Scan(&status, &customer, &device, &number, &notify, &msgApp, &lang); err != nil {
		return err
	}
	if !notify || !msgApp {
		return nil
	}
	texts := map[string]map[string]string{
		"en": {"received": "We received your device %s (repair #%d). We will keep you informed.", "diagnosing": "Your device %s (repair #%d) is being checked.",
			"waiting_parts": "Your device %s (repair #%d) is waiting for a part.", "repaired": "Good news: your device %s (repair #%d) is ready to collect.",
			"delivered": "Your device %s (repair #%d) was delivered. Thank you!", "cancelled": "Repair #%d of %s was cancelled."},
		"ar": {"received": "استلمنا جهازك %s (تصليح رقم %d). سنبقيك على اطلاع.", "diagnosing": "جهازك %s (تصليح رقم %d) قيد الفحص.",
			"waiting_parts": "جهازك %s (تصليح رقم %d) بانتظار قطعة.", "repaired": "جهازك %s (تصليح رقم %d) جاهز للاستلام.",
			"delivered": "تم تسليم جهازك %s (تصليح رقم %d). شكراً لك!", "cancelled": "أُلغي التصليح رقم %d للجهاز %s."},
		"fr": {"received": "Nous avons reçu votre appareil %s (réparation n° %d). Nous vous tiendrons informé.", "diagnosing": "Votre appareil %s (réparation n° %d) est en diagnostic.",
			"waiting_parts": "Votre appareil %s (réparation n° %d) attend une pièce.", "repaired": "Votre appareil %s (réparation n° %d) est prêt.",
			"delivered": "Votre appareil %s (réparation n° %d) a été remis. Merci !", "cancelled": "La réparation n° %d de %s est annulée."},
	}
	set, ok := texts[lang]
	if !ok {
		set = texts["en"]
	}
	if device == "" {
		device = "—"
	}
	var body string
	if status == "cancelled" {
		body = fmt.Sprintf(set[status], number, device)
	} else {
		body = fmt.Sprintf(set[status], device, number)
	}
	rd, err := newRenderer(ctx, tx, cid, lang)
	if err != nil {
		return err
	}
	m, err := rd.customerContext(ctx, tx, customer, nil, nil)
	if err != nil {
		return err
	}
	prov, err := pickProvider(ctx, tx, cid, "")
	if err != nil {
		return err
	}
	// braces in device names must not be read as template fields
	body = strings.NewReplacer("{", "(", "}", ")").Replace(body)
	_, _, _, err = queueMessage(ctx, tx, rd, m, "custom", nil, body, prov, "whatsapp")
	return err
}
