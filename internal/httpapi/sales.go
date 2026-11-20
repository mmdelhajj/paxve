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

// Sales: quotation -> sales order -> deliveries (stock out at average cost, Dr cost of sales / Cr inventory)
// and invoices (full or partial, per line quantity). One document (sales_orders) carries the whole flow:
//
//	draft -> sent (quotation sent to the customer) -> confirmed (sales order) -> done (everything delivered and invoiced)
//	draft / sent / confirmed (nothing delivered or invoiced) -> cancelled
//
// Invoices created from an order never move stock: goods leave stock with the delivery.

const orderCols = `o.id, o.number, o.order_number, o.customer_id, c.code AS customer_code, c.name AS customer_name, c.phone AS customer_phone,
	c.email AS customer_email, c.address AS customer_address, o.lead_id, o.currency, o.quote_date, o.valid_until, o.order_date, o.status,
	o.subtotal::text AS subtotal, o.tax_total::text AS tax_total, o.total::text AS total, o.revenue_account_id, o.salesperson_id,
	sp.full_name AS salesperson_name, o.notes, o.terms, o.accepted_at, o.accepted_name, o.created_at, o.updated_at,
	(SELECT CASE WHEN bool_and(l.qty_delivered >= l.quantity) THEN 'full' WHEN bool_or(l.qty_delivered > 0) THEN 'partial' ELSE 'none' END
	   FROM sales_order_lines l LEFT JOIN items it ON it.id = l.item_id WHERE l.order_id = o.id AND it.kind = 'stock') AS delivery_status,
	(SELECT CASE WHEN bool_and(l.qty_invoiced >= l.quantity) THEN 'full' WHEN bool_or(l.qty_invoiced > 0) THEN 'partial' ELSE 'none' END
	   FROM sales_order_lines l WHERE l.order_id = o.id) AS invoice_status`

const orderFrom = ` FROM sales_orders o JOIN customers c ON c.id = o.customer_id LEFT JOIN users sp ON sp.id = o.salesperson_id`

type orderLineIn struct {
	ItemID      string `json:"item_id"`
	Description string `json:"description"`
	Quantity    string `json:"quantity"`
	UnitPrice   string `json:"unit_price"`
	DiscountPct string `json:"discount_pct"`
	TaxRateID   string `json:"tax_rate_id"`
	WarehouseID string `json:"warehouse_id"`
}

type orderIn struct {
	CustomerID       string        `json:"customer_id"`
	LeadID           string        `json:"lead_id"`
	Currency         string        `json:"currency"`
	QuoteDate        string        `json:"quote_date"`
	ValidUntil       string        `json:"valid_until"`
	RevenueAccountID string        `json:"revenue_account_id"`
	SalespersonID    string        `json:"salesperson_id"`
	Notes            string        `json:"notes"`
	Terms            *string       `json:"terms"`
	Lines            []orderLineIn `json:"lines"`
}

func (in *orderIn) valid() bool {
	if in.QuoteDate == "" {
		in.QuoteDate = today()
	}
	if !validDate(in.QuoteDate) || (in.ValidUntil != "" && (!validDate(in.ValidUntil) || in.ValidUntil < in.QuoteDate)) ||
		!currencyRe.MatchString(in.Currency) || !isUUID(in.CustomerID) || len(in.Lines) == 0 || len(in.Lines) > 200 ||
		(in.LeadID != "" && !isUUID(in.LeadID)) || (in.RevenueAccountID != "" && !isUUID(in.RevenueAccountID)) ||
		(in.SalespersonID != "" && !isUUID(in.SalespersonID)) {
		return false
	}
	for _, l := range in.Lines {
		if (l.ItemID != "" && !isUUID(l.ItemID)) || (l.TaxRateID != "" && !isUUID(l.TaxRateID)) || (l.WarehouseID != "" && !isUUID(l.WarehouseID)) {
			return false
		}
	}
	return true
}

// writeOrderLines validates and stores the lines of a draft order and its totals.
func writeOrderLines(ctx context.Context, tx pgx.Tx, cid, orderID, currency string, lines []orderLineIn) error {
	dec, err := currencyDecimals(ctx, tx, currency)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sales_order_lines WHERE order_id = $1`, orderID); err != nil {
		return err
	}
	var customer string
	if err := tx.QueryRow(ctx, `SELECT customer_id::text FROM sales_orders WHERE id = $1`, orderID).Scan(&customer); err != nil {
		return err
	}
	fp, err := partnerFiscalPosition(ctx, tx, cid, "customers", customer)
	if err != nil {
		return err
	}
	sub, taxT := decimal.Zero, decimal.Zero
	for i, l := range lines {
		q, ok1 := money(l.Quantity)
		p, ok2 := money(l.UnitPrice)
		d := decimal.Zero
		ok3 := true
		if strings.TrimSpace(l.DiscountPct) != "" {
			d, ok3 = money(l.DiscountPct)
		}
		desc := strings.TrimSpace(l.Description)
		var item, wh *string
		if l.ItemID != "" {
			it, err := loadItem(ctx, tx, cid, l.ItemID)
			if err != nil {
				return err
			}
			if desc == "" {
				desc = it.Name
			}
			item = &it.ID
			if it.Kind == "stock" {
				w, err := checkWarehouse(ctx, tx, cid, l.WarehouseID)
				if err != nil {
					return err
				}
				wh = &w
			}
		}
		if !ok1 || !ok2 || !ok3 || !q.IsPositive() || p.IsNegative() || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(100)) || desc == "" {
			return badRequest("invalid_line")
		}
		tax, err := loadTaxRate(ctx, tx, cid, fp.tax(l.TaxRateID), "sales")
		if err != nil {
			return err
		}
		amt := q.Mul(p).Mul(decimal.NewFromInt(100).Sub(d)).Div(decimal.NewFromInt(100)).Round(dec)
		ta := taxOn(amt, tax, dec)
		var taxID *string
		if tax != nil {
			taxID = &tax.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sales_order_lines (order_id, line_no, item_id, description, quantity, unit_price, discount_pct,
			amount, tax_rate_id, tax_amount, warehouse_id) VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, $8::numeric, $9, $10::numeric, $11)`,
			orderID, i+1, item, desc, q.String(), p.String(), d.String(), amt.String(), taxID, ta.String(), wh); err != nil {
			return err
		}
		sub = sub.Add(amt)
		taxT = taxT.Add(ta)
	}
	_, err = tx.Exec(ctx, `UPDATE sales_orders SET subtotal = $2::numeric, tax_total = $3::numeric, total = $4::numeric WHERE id = $1`,
		orderID, sub.String(), taxT.String(), sub.Add(taxT).String())
	return err
}

func checkOrderRefs(ctx context.Context, tx pgx.Tx, cid string, in *orderIn) error {
	if _, err := lockCustomer(ctx, tx, cid, in.CustomerID); err != nil {
		return err
	}
	if in.RevenueAccountID != "" {
		if err := requirePostable(ctx, tx, cid, in.RevenueAccountID); err != nil {
			return err
		}
	}
	if in.LeadID != "" {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_leads WHERE id = $1 AND company_id = $2)`, in.LeadID, cid).Scan(&ok); err != nil || !ok {
			if err != nil {
				return err
			}
			return badRequest("invalid_lead")
		}
	}
	if in.SalespersonID != "" {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND is_active)`, in.SalespersonID).Scan(&ok); err != nil || !ok {
			if err != nil {
				return err
			}
			return badRequest("invalid_user")
		}
	}
	return nil
}

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	var in orderIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := checkOrderRefs(ctx, tx, cid, &in); err != nil {
			return nil, err
		}
		var validDays int
		var terms string
		if err := tx.QueryRow(ctx, `SELECT quote_valid_days, doc_terms FROM companies WHERE id = $1`, cid).Scan(&validDays, &terms); err != nil {
			return nil, err
		}
		if in.ValidUntil == "" {
			t, _ := time.Parse(time.DateOnly, in.QuoteDate)
			in.ValidUntil = t.AddDate(0, 0, validDays).Format(time.DateOnly)
		}
		if in.Terms != nil {
			terms = strings.TrimSpace(*in.Terms)
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'quotation')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO sales_orders (company_id, number, customer_id, lead_id, currency, quote_date, valid_until,
			revenue_account_id, salesperson_id, notes, terms)
			VALUES ($1, $2, $3, nullif($4, '')::uuid, $5, $6::date, $7::date, nullif($8, '')::uuid, coalesce(nullif($9, '')::uuid, app_user()), $10, $11)
			RETURNING id::text`, cid, number, in.CustomerID, in.LeadID, in.Currency, in.QuoteDate, in.ValidUntil, in.RevenueAccountID,
			in.SalespersonID, strings.TrimSpace(in.Notes), terms).Scan(&id); err != nil {
			return nil, err
		}
		if err := writeOrderLines(ctx, tx, cid, id, in.Currency, in.Lines); err != nil {
			return nil, err
		}
		return loadOrder(ctx, tx, cid, id)
	})
}

func (s *Server) updateOrder(w http.ResponseWriter, r *http.Request) {
	var in orderIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !in.valid() || !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		status, err := lockOrder(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if status != "draft" && status != "sent" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if err := checkOrderRefs(ctx, tx, cid, &in); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE sales_orders SET customer_id = $2, lead_id = nullif($3, '')::uuid, currency = $4, quote_date = $5::date,
			valid_until = nullif($6, '')::date, revenue_account_id = nullif($7, '')::uuid,
			salesperson_id = coalesce(nullif($8, '')::uuid, salesperson_id), notes = $9, terms = coalesce(trim($10), terms),
			accepted_at = NULL, accepted_name = '' WHERE id = $1`,
			id, in.CustomerID, in.LeadID, in.Currency, in.QuoteDate, in.ValidUntil, in.RevenueAccountID, in.SalespersonID,
			strings.TrimSpace(in.Notes), in.Terms); err != nil {
			return nil, err
		}
		if err := writeOrderLines(ctx, tx, cid, id, in.Currency, in.Lines); err != nil {
			return nil, err
		}
		return loadOrder(ctx, tx, cid, id)
	})
}

func lockOrder(ctx context.Context, tx pgx.Tx, cid, id string) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM sales_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&status)
	if err == pgx.ErrNoRows {
		return "", notFound()
	}
	return status, err
}

func loadOrder(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	o, err := queryJSONOne(ctx, tx, `SELECT `+orderCols+`, c.portal_token, co.name_en AS company_name, co.name_ar AS company_name_ar,
		co.logo AS company_logo, co.doc_header AS company_header, co.doc_footer AS company_footer, co.tax_id AS company_tax_id`+orderFrom+`
		JOIN companies co ON co.id = o.company_id WHERE o.id = $1 AND o.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.item_id, it.code AS item_code, it.kind AS item_kind, it.unit, l.description,
		l.quantity::text AS quantity, l.unit_price::text AS unit_price, l.discount_pct::text AS discount_pct, l.amount::text AS amount,
		l.tax_rate_id, t.name AS tax_name, l.tax_amount::text AS tax_amount, l.warehouse_id, w.name AS warehouse_name,
		l.qty_delivered::text AS qty_delivered, l.qty_invoiced::text AS qty_invoiced
		FROM sales_order_lines l LEFT JOIN items it ON it.id = l.item_id LEFT JOIN tax_rates t ON t.id = l.tax_rate_id
		LEFT JOIN warehouses w ON w.id = l.warehouse_id WHERE l.order_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	dels, err := queryJSON(ctx, tx, `SELECT d.id, d.number, d.delivery_date, d.status, d.lines, d.notes, d.journal_entry_id, d.created_at
		FROM sales_deliveries d WHERE d.order_id = $1 ORDER BY d.created_at`, id)
	if err != nil {
		return nil, err
	}
	invs, err := queryJSON(ctx, tx, `SELECT i.id, i.number, i.issue_date, i.total::text AS total, i.paid::text AS paid, i.status
		FROM invoices i WHERE i.sales_order_id = $1 ORDER BY i.created_at`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"order": o, "lines": lines, "deliveries": dels, "invoices": invs}, nil
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadOrder(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

func (s *Server) listOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, customer, lead, search, kind := q.Get("status"), q.Get("customer_id"), q.Get("lead_id"), strings.TrimSpace(q.Get("q")), q.Get("kind")
	from, to := q.Get("from"), q.Get("to")
	if (customer != "" && !isUUID(customer)) || (lead != "" && !isUUID(lead)) || (from != "" && !validDate(from)) || (to != "" && !validDate(to)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cond := ` WHERE o.company_id = $1 AND ($2 = '' OR o.status = $2) AND ($3 = '' OR o.customer_id = $3::uuid)
			AND ($4 = '' OR c.name ILIKE '%' || $4 || '%' OR c.code ILIKE $4 || '%' OR o.number::text = $4 OR o.order_number::text = $4)
			AND (CASE $5 WHEN 'quotes' THEN o.status IN ('draft', 'sent') WHEN 'orders' THEN o.status IN ('confirmed', 'done') ELSE true END)
			AND ($6 = '' OR o.quote_date >= $6::date) AND ($7 = '' OR o.quote_date <= $7::date) AND ($8 = '' OR o.lead_id = $8::uuid)`
		rows, err := queryJSON(ctx, tx, `SELECT `+orderCols+orderFrom+cond+` ORDER BY o.quote_date DESC, o.number DESC LIMIT 1000`,
			r.PathValue("cid"), status, customer, search, kind, from, to, lead)
		if err != nil {
			return nil, err
		}
		sums, err := queryJSON(ctx, tx, `SELECT o.status, o.currency, count(*) AS count, sum(o.total)::text AS total`+orderFrom+cond+`
			GROUP BY o.status, o.currency ORDER BY o.status, o.currency`, r.PathValue("cid"), status, customer, search, kind, from, to, lead)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows, "totals": sums}, nil
	})
}

// orderAction: send / confirm / cancel / reopen (back to draft) / done.
func (s *Server) orderAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date     string `json:"date"`
		Deposit  string `json:"deposit"`   // cancel with a deposit: refund | credit
		MethodID string `json:"method_id"` // refund: the box the deposit is paid back from
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if !isUUID(id) || (in.Date != "" && !validDate(in.Date)) || (in.MethodID != "" && !isUUID(in.MethodID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		status, err := lockOrder(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		wrong := &apiError{status: http.StatusConflict, code: "wrong_status"}
		switch action {
		case "send":
			if status != "draft" && status != "sent" {
				return nil, wrong
			}
			_, err = tx.Exec(ctx, `UPDATE sales_orders SET status = 'sent' WHERE id = $1`, id)
		case "confirm":
			if status != "draft" && status != "sent" {
				return nil, wrong
			}
			var cust, cur, tot string
			if err := tx.QueryRow(ctx, `SELECT customer_id::text, currency::text, total::text FROM sales_orders WHERE id = $1`, id).Scan(&cust, &cur, &tot); err != nil {
				return nil, err
			}
			amt, _ := decimal.NewFromString(tot)
			if err := checkCredit(ctx, tx, r, cid, cust, cur, amt); err != nil {
				return nil, err
			}
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'sales_order')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			_, err = tx.Exec(ctx, `UPDATE sales_orders SET status = 'confirmed', order_number = $2, order_date = $3::date WHERE id = $1`, id, n, in.Date)
			if err == nil {
				// a lead quoted and confirmed is won
				_, err = tx.Exec(ctx, `UPDATE crm_leads SET status = 'won', closed_at = now(),
					stage_id = coalesce((SELECT s.id FROM crm_stages s WHERE s.company_id = crm_leads.company_id AND s.is_won AND s.is_active
					                     ORDER BY s.seq LIMIT 1), stage_id)
					WHERE id = (SELECT lead_id FROM sales_orders WHERE id = $1) AND status = 'open'`, id)
			}
			if err == nil {
				// warehouses with 2/3-step delivery: the pick (→ pack) → ship operations are prepared now
				err = whOnOrderConfirmed(ctx, tx, cid, id)
			}
		case "cancel":
			var busy bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sales_order_lines WHERE order_id = $1 AND (qty_delivered > 0 OR qty_invoiced > 0))`,
				id).Scan(&busy); err != nil {
				return nil, err
			}
			if busy {
				return nil, &apiError{status: http.StatusConflict, code: "order_in_progress"}
			}
			if status == "cancelled" || status == "done" {
				return nil, wrong
			}
			// a deposit is never left behind silently (audit P7-L4): refund it or keep it as customer credit
			if err := cancelOrderDeposits(ctx, tx, cid, id, in.Deposit, in.MethodID); err != nil {
				return nil, err
			}
			_, err = tx.Exec(ctx, `UPDATE sales_orders SET status = 'cancelled' WHERE id = $1`, id)
			if err == nil {
				err = whOnOrderCancelled(ctx, tx, cid, id)
			}
		case "reopen":
			if status != "cancelled" && status != "sent" {
				return nil, wrong
			}
			_, err = tx.Exec(ctx, `UPDATE sales_orders SET status = CASE WHEN order_number IS NULL THEN 'draft' ELSE 'confirmed' END WHERE id = $1`, id)
		case "done":
			if status != "confirmed" {
				return nil, wrong
			}
			_, err = tx.Exec(ctx, `UPDATE sales_orders SET status = 'done' WHERE id = $1`, id)
			if err == nil { // a finished order's unused deposit becomes ordinary customer credit
				var set *billingSettings
				var cust string
				if set, err = loadBillingSettings(ctx, tx, cid); err == nil {
					if err = tx.QueryRow(ctx, `SELECT customer_id::text FROM sales_orders WHERE id = $1`, id).Scan(&cust); err == nil {
						_, err = releaseOrderDeposits(ctx, tx, cid, set, id, cust, "credit")
					}
				}
			}
		default:
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		return loadOrder(ctx, tx, cid, id)
	})
}

// refreshOrderStatus marks a confirmed order done when every line is invoiced and every stock line delivered
// (and back to confirmed when a delivery or invoice is voided).
func refreshOrderStatus(ctx context.Context, tx pgx.Tx, orderID string) error {
	_, err := tx.Exec(ctx, `UPDATE sales_orders o SET status = CASE WHEN NOT EXISTS (
			SELECT 1 FROM sales_order_lines l LEFT JOIN items it ON it.id = l.item_id
			WHERE l.order_id = o.id AND (l.qty_invoiced < l.quantity OR (it.kind = 'stock' AND l.qty_delivered < l.quantity)))
		THEN 'done' ELSE 'confirmed' END
		WHERE o.id = $1 AND o.status IN ('confirmed', 'done')`, orderID)
	return err
}

type qtyIn struct {
	LineID   string `json:"line_id"`
	Quantity string `json:"quantity"`
}

type ordLine struct {
	id, desc, kind     string
	item, wh, tax      *string
	inv, exp, revenue  *string
	itemName           string
	qty, price, amount decimal.Decimal
	disc, taxAmt       decimal.Decimal
	delivered, billed  decimal.Decimal
}

func orderLines(ctx context.Context, tx pgx.Tx, orderID string) ([]ordLine, error) {
	rows, err := tx.Query(ctx, `SELECT l.id::text, l.description, coalesce(it.kind, ''), l.item_id::text, l.warehouse_id::text, l.tax_rate_id::text,
		it.inventory_account_id::text, it.expense_account_id::text, it.revenue_account_id::text, coalesce(it.name, ''),
		l.quantity::text, l.unit_price::text, l.amount::text, l.discount_pct::text, l.tax_amount::text, l.qty_delivered::text, l.qty_invoiced::text
		FROM sales_order_lines l LEFT JOIN items it ON it.id = l.item_id WHERE l.order_id = $1 ORDER BY l.line_no FOR UPDATE OF l`, orderID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ordLine, error) {
		var l ordLine
		var q, p, a, d, t, dl, bl string
		err := row.Scan(&l.id, &l.desc, &l.kind, &l.item, &l.wh, &l.tax, &l.inv, &l.exp, &l.revenue, &l.itemName, &q, &p, &a, &d, &t, &dl, &bl)
		l.qty, _ = decimal.NewFromString(q)
		l.price, _ = decimal.NewFromString(p)
		l.amount, _ = decimal.NewFromString(a)
		l.disc, _ = decimal.NewFromString(d)
		l.taxAmt, _ = decimal.NewFromString(t)
		l.delivered, _ = decimal.NewFromString(dl)
		l.billed, _ = decimal.NewFromString(bl)
		return l, err
	})
}

// pickQuantities: requested quantity per line (empty request = everything remaining). remaining(l) gives what is left.
func pickQuantities(lines []ordLine, req []qtyIn, remaining func(ordLine) decimal.Decimal) (map[string]decimal.Decimal, error) {
	out := map[string]decimal.Decimal{}
	if len(req) == 0 {
		for _, l := range lines {
			if rm := remaining(l); rm.IsPositive() {
				out[l.id] = rm
			}
		}
		return out, nil
	}
	byID := map[string]ordLine{}
	for _, l := range lines {
		byID[l.id] = l
	}
	for _, q := range req {
		l, ok := byID[q.LineID]
		v, okv := money(q.Quantity)
		if !ok || !okv || v.IsNegative() {
			return nil, badRequest("invalid_line")
		}
		if v.IsZero() {
			continue
		}
		if v.GreaterThan(remaining(l)) {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "quantity_exceeds_order", detail: l.desc}
		}
		out[l.id] = out[l.id].Add(v)
	}
	return out, nil
}

// deliverOrder: POST /sales/orders/{id}/deliver — stock lines leave stock at average cost.
func (s *Server) deliverOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date  string  `json:"date"`
		Notes string  `json:"notes"`
		Lines []qtyIn `json:"lines"`
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
		if err := whRefuseDirectDelivery(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		if _, err := deliverOrderTx(ctx, tx, r, cid, id, in.Date, in.Notes, in.Lines); err != nil {
			return nil, err
		}
		return loadOrder(ctx, tx, cid, id)
	})
}

// deliverOrderTx books a delivery of an order (stock lines leave stock at average cost) inside the caller's
// transaction and returns the delivery id. Used by the deliver route, the warehouse "ship" step and dropship.
func deliverOrderTx(ctx context.Context, tx pgx.Tx, r *http.Request, cid, id, date, notes string, req []qtyIn) (string, error) {
	in := struct {
		Date, Notes string
		Lines       []qtyIn
	}{date, notes, req}
	did := ""
	_, err := func() (any, error) {
		status, err := lockOrder(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if status != "confirmed" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		var dcust string
		if err := tx.QueryRow(ctx, `SELECT customer_id::text FROM sales_orders WHERE id = $1`, id).Scan(&dcust); err != nil {
			return nil, err
		}
		if err := checkCredit(ctx, tx, r, cid, dcust, "", decimal.Zero); err != nil { // the order itself is already counted
			return nil, err
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		ps, err := loadPurchSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		lines, err := orderLines(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		qty, err := pickQuantities(lines, in.Lines, func(l ordLine) decimal.Decimal { return l.qty.Sub(l.delivered) })
		if err != nil {
			return nil, err
		}
		if len(qty) == 0 {
			return nil, badRequest("nothing_to_deliver")
		}
		var orderNo int64
		_ = tx.QueryRow(ctx, `SELECT coalesce(order_number, number) FROM sales_orders WHERE id = $1`, id).Scan(&orderNo)
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'delivery')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		type out struct {
			l        ordLine
			qty, avg decimal.Decimal
			value    decimal.Decimal
		}
		outs := []out{}
		jl := []jline{}
		done := []map[string]string{}
		for _, l := range lines {
			q, ok := qty[l.id]
			if !ok {
				continue
			}
			done = append(done, map[string]string{"line_id": l.id, "quantity": q.String(), "description": l.desc})
			if l.kind != "stock" || l.item == nil || l.wh == nil {
				continue
			}
			have, avg, err := lockStock(ctx, tx, *l.item, *l.wh)
			if err != nil {
				return nil, err
			}
			if have.LessThan(q) {
				return nil, insufficientStock(l.itemName, have)
			}
			cogs := ""
			if l.exp != nil {
				cogs = *l.exp
			} else if ps.COGS != nil {
				cogs = *ps.COGS
			}
			if cogs == "" {
				return nil, notSetup("cogs")
			}
			v, err := outValue(ctx, tx, *l.item, *l.wh, have, avg, q)
			if err != nil {
				return nil, err
			}
			outs = append(outs, out{l, q, avg, v})
			if v.IsPositive() {
				jl = append(jl, jline{Account: cogs, Desc: l.itemName, Currency: set.Base, Debit: v},
					jline{Account: *l.inv, Desc: l.itemName, Currency: set.Base, Credit: v})
			}
		}
		var je *string
		if len(jl) > 0 {
			e, err := postJournal(ctx, tx, cid, set, in.Date, fmt.Sprintf("Delivery #%d - order #%d", number, orderNo), fmt.Sprintf("DEL-%d", number), "delivery", jl)
			if err != nil {
				return nil, err
			}
			je = &e
		}
		lj, _ := json.Marshal(done)
		if err := tx.QueryRow(ctx, `INSERT INTO sales_deliveries (company_id, number, order_id, delivery_date, lines, journal_entry_id, notes)
			VALUES ($1, $2, $3, $4::date, $5, $6, $7) RETURNING id::text`, cid, number, id, in.Date, lj, je, strings.TrimSpace(in.Notes)).Scan(&did); err != nil {
			return nil, err
		}
		for _, o := range outs {
			if err := stockOut(ctx, tx, cid, *o.l.item, *o.l.wh, o.qty, o.avg, o.value, in.Date, "delivery", did, je,
				fmt.Sprintf("Delivery #%d (order #%d)", number, orderNo)); err != nil {
				return nil, err
			}
		}
		for lid, q := range qty {
			if _, err := tx.Exec(ctx, `UPDATE sales_order_lines SET qty_delivered = qty_delivered + $2::numeric WHERE id = $1`, lid, q.String()); err != nil {
				return nil, err
			}
		}
		if err := refreshOrderStatus(ctx, tx, id); err != nil {
			return nil, err
		}
		return nil, nil
	}()
	return did, err
}

func (s *Server) voidDelivery(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var number int64
		var status, order string
		var je *string
		var lines []byte
		err := tx.QueryRow(ctx, `SELECT number, status, order_id::text, journal_entry_id::text, lines FROM sales_deliveries
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&number, &status, &order, &je, &lines)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := lockOrder(ctx, tx, cid, order); err != nil {
			return nil, err
		}
		var rev *string
		if je != nil {
			var rid string
			if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`, *je, fmt.Sprintf("Void delivery #%d", number)).Scan(&rid); err != nil {
				return nil, err
			}
			rev = &rid
		}
		if err := reverseStockMoves(ctx, tx, cid, "delivery", "delivery_void", id, rev, fmt.Sprintf("Void delivery #%d", number)); err != nil {
			return nil, err
		}
		var done []struct {
			LineID   string `json:"line_id"`
			Quantity string `json:"quantity"`
		}
		_ = json.Unmarshal(lines, &done)
		for _, d := range done {
			if _, err := tx.Exec(ctx, `UPDATE sales_order_lines SET qty_delivered = greatest(qty_delivered - $2::numeric, 0) WHERE id = $1`,
				d.LineID, d.Quantity); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sales_deliveries SET status = 'void', void_entry_id = $2 WHERE id = $1`, id, rev); err != nil {
			return nil, err
		}
		if err := refreshOrderStatus(ctx, tx, order); err != nil {
			return nil, err
		}
		return loadOrder(ctx, tx, cid, order)
	})
}

// invoiceOrder: POST /sales/orders/{id}/invoice — bills ordered quantities not yet invoiced (all, or the given lines).
func (s *Server) invoiceOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date    string  `json:"date"`
		DueDate string  `json:"due_date"`
		Notes   string  `json:"notes"`
		Lines   []qtyIn `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	if in.DueDate == "" {
		in.DueDate = in.Date
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !validDate(in.Date) || !validDate(in.DueDate) || in.DueDate < in.Date {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		status, err := lockOrder(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if status != "confirmed" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var customer, currency string
		var orderNo int64
		var rev *string
		if err := tx.QueryRow(ctx, `SELECT customer_id::text, currency::text, order_number, revenue_account_id::text FROM sales_orders WHERE id = $1`, id).
			Scan(&customer, &currency, &orderNo, &rev); err != nil {
			return nil, err
		}
		name, err := lockCustomer(ctx, tx, cid, customer)
		if err != nil {
			return nil, err
		}
		if err := checkCredit(ctx, tx, r, cid, customer, currency, decimal.Zero); err != nil { // the order is already counted
			return nil, err
		}
		fp, err := partnerFiscalPosition(ctx, tx, cid, "customers", customer)
		if err != nil {
			return nil, err
		}
		revenue := ""
		if rev != nil {
			revenue = *rev
		} else if set.Revenue != nil {
			revenue = *set.Revenue
		}
		if revenue == "" {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "revenue"}
		}
		revenue = fp.account(revenue)
		dec, err := currencyDecimals(ctx, tx, currency)
		if err != nil {
			return nil, err
		}
		lines, err := orderLines(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		qty, err := pickQuantities(lines, in.Lines, func(l ordLine) decimal.Decimal { return l.qty.Sub(l.billed) })
		if err != nil {
			return nil, err
		}
		if len(qty) == 0 {
			return nil, badRequest("nothing_to_invoice")
		}
		inv := []invLine{}
		for _, l := range lines {
			q, ok := qty[l.id]
			if !ok {
				continue
			}
			var amt, ta decimal.Decimal
			if q.Add(l.billed).Equal(l.qty) {
				// last part of the line: whatever is left of the line amount (no rounding residue)
				var billedAmt, billedTax string
				if err := tx.QueryRow(ctx, `SELECT coalesce(sum(il.amount), 0)::text, coalesce(sum(il.tax_amount), 0)::text FROM invoice_lines il
					JOIN invoices i ON i.id = il.invoice_id WHERE il.order_line_id = $1 AND i.status <> 'void'`, l.id).Scan(&billedAmt, &billedTax); err != nil {
					return nil, err
				}
				ba, _ := decimal.NewFromString(billedAmt)
				bt, _ := decimal.NewFromString(billedTax)
				amt, ta = l.amount.Sub(ba), l.taxAmt.Sub(bt)
			} else {
				amt = l.amount.Mul(q).Div(l.qty).Round(dec)
				ta = l.taxAmt.Mul(q).Div(l.qty).Round(dec)
			}
			var tax *taxInfo
			if l.tax != nil {
				if tax, err = loadTaxRate(ctx, tx, cid, *l.tax, "sales"); err != nil {
					return nil, err
				}
			}
			lineRev := ""
			if l.revenue != nil && rev == nil {
				lineRev = fp.account(*l.revenue)
			}
			unit := l.price
			if !l.disc.IsZero() && q.IsPositive() {
				unit = amt.Div(q).Round(4)
			}
			lid := l.id
			inv = append(inv, invLine{Kind: "other", Description: l.desc, Quantity: q, UnitPrice: unit, Amount: amt, Tax: tax, TaxAmount: ta,
				Revenue: lineRev, OrderLineID: &lid, Meta: map[string]any{"order_number": orderNo}})
		}
		notes := strings.TrimSpace(in.Notes)
		if notes == "" {
			notes = fmt.Sprintf("Sales order #%d", orderNo)
		}
		invID, _, err := issueInvoiceFor(ctx, tx, cid, set, customer, name, nil, nil, &id, in.Date, in.DueDate, currency, revenue, notes, inv)
		if err != nil {
			return nil, err
		}
		for lid, q := range qty {
			if _, err := tx.Exec(ctx, `UPDATE sales_order_lines SET qty_invoiced = qty_invoiced + $2::numeric WHERE id = $1`, lid, q.String()); err != nil {
				return nil, err
			}
		}
		if err := refreshOrderStatus(ctx, tx, id); err != nil {
			return nil, err
		}
		// the order's own deposits settle its invoice (audit P7-H2), never the customer's older invoices
		if err := applyOrderDeposits(ctx, tx, cid, set, id); err != nil {
			return nil, err
		}
		return map[string]any{"invoice_id": invID}, nil
	})
}

// releaseOrderInvoice gives the quantities of a voided invoice back to its sales order lines.
func releaseOrderInvoice(ctx context.Context, tx pgx.Tx, invoiceID string) error {
	var order *string
	if err := tx.QueryRow(ctx, `SELECT sales_order_id::text FROM invoices WHERE id = $1`, invoiceID).Scan(&order); err != nil || order == nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM sales_orders WHERE id = $1 FOR UPDATE`, *order); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE sales_order_lines l SET qty_invoiced = greatest(l.qty_invoiced - x.q, 0)
		FROM (SELECT order_line_id, sum(quantity) AS q FROM invoice_lines WHERE invoice_id = $1 AND order_line_id IS NOT NULL GROUP BY order_line_id) x
		WHERE l.id = x.order_line_id`, invoiceID); err != nil {
		return err
	}
	return refreshOrderStatus(ctx, tx, *order)
}

// salesReport: quotations / orders / invoiced per salesperson and per product for a period.
func (s *Server) salesReport(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		people, err := queryJSON(ctx, tx, `SELECT coalesce(u.full_name, '-') AS salesperson, o.currency,
			count(*) FILTER (WHERE o.status <> 'cancelled') AS quotes,
			count(*) FILTER (WHERE o.status IN ('confirmed', 'done')) AS orders,
			coalesce(sum(o.total) FILTER (WHERE o.status IN ('confirmed', 'done')), 0)::text AS ordered,
			coalesce(sum(o.total) FILTER (WHERE o.status IN ('draft', 'sent')), 0)::text AS open_quotes
			FROM sales_orders o LEFT JOIN users u ON u.id = o.salesperson_id
			WHERE o.company_id = $1 AND o.quote_date BETWEEN $2::date AND $3::date
			GROUP BY u.full_name, o.currency ORDER BY 5 DESC`, cid, from, to)
		if err != nil {
			return nil, err
		}
		products, err := queryJSON(ctx, tx, `SELECT coalesce(it.code, '') AS code, l.description AS name, o.currency,
			sum(l.quantity)::text AS quantity, sum(l.amount)::text AS amount, sum(l.qty_delivered)::text AS delivered, sum(l.qty_invoiced)::text AS invoiced
			FROM sales_order_lines l JOIN sales_orders o ON o.id = l.order_id LEFT JOIN items it ON it.id = l.item_id
			WHERE o.company_id = $1 AND o.status IN ('confirmed', 'done') AND coalesce(o.order_date, o.quote_date) BETWEEN $2::date AND $3::date
			GROUP BY it.code, l.description, o.currency ORDER BY sum(l.amount) DESC LIMIT 200`, cid, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"from": from, "to": to, "salespeople": people, "products": products}, nil
	})
}
