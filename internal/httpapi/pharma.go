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

// Pharmacy (business pack "pharmacy"): drug fields on items (generic / scientific name, active ingredient, strength,
// form, prescription and controlled flags), search by any of them with the alternatives (same active ingredient) in
// stock, lots with expiry sold first-expiry-first with expired lots blocked (companies.block_expired, in lotsOut), the
// expiry board, the controlled-drugs register (till sales with their prescription) and the insurance claims: the
// insurer's share of a till sale is a tender booked to "Insurance claims receivable"; settling a claim moves it to the
// cash / bank box, rejecting it writes it off.

// GET /pharma/search?q=&warehouse_id=
func (s *Server) pharmaSearch(w http.ResponseWriter, r *http.Request) {
	q, wh := strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("warehouse_id")
	if len(q) < 2 || len(q) > 100 || (wh != "" && !isUUID(wh)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT it.id, it.code, it.name, it.barcode, it.generic_name, it.active_ingredient, it.strength, it.dosage_form,
			it.rx_required, it.controlled, it.sale_price::text AS sale_price, it.sale_currency, it.tracking,
			coalesce((SELECT sum(s.quantity) FROM stock s WHERE s.item_id = it.id AND ($3 = '' OR s.warehouse_id = $3::uuid)), 0)::text AS in_stock,
			(SELECT min(l.expiry) FROM stock_lots l JOIN stock_lot_qty x ON x.lot_id = l.id AND x.quantity > 0
			  WHERE l.item_id = it.id AND l.expiry >= current_date AND ($3 = '' OR x.warehouse_id = $3::uuid)) AS next_expiry,
			(SELECT count(*) FROM items a WHERE a.company_id = it.company_id AND a.id <> it.id AND a.is_active AND it.active_ingredient <> ''
			  AND lower(a.active_ingredient) = lower(it.active_ingredient)) AS alternatives
			FROM items it WHERE it.company_id = $1 AND it.is_active AND it.is_sellable
			  AND (it.name ILIKE '%' || $2 || '%' OR it.code ILIKE $2 || '%' OR it.barcode = $2 OR it.generic_name ILIKE '%' || $2 || '%'
			       OR it.active_ingredient ILIKE '%' || $2 || '%')
			ORDER BY it.name LIMIT 60`, r.PathValue("cid"), q, wh)
	})
}

// GET /pharma/alternatives?item_id= : other items with the same active ingredient (same strength first), with stock.
func (s *Server) pharmaAlternatives(w http.ResponseWriter, r *http.Request) {
	item := r.URL.Query().Get("item_id")
	if !isUUID(item) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT a.id, a.code, a.name, a.generic_name, a.active_ingredient, a.strength, a.dosage_form, a.rx_required, a.controlled,
			a.sale_price::text AS sale_price, a.sale_currency, coalesce((SELECT sum(s.quantity) FROM stock s WHERE s.item_id = a.id), 0)::text AS in_stock,
			lower(a.strength) = lower(it.strength) AS same_strength
			FROM items it JOIN items a ON a.company_id = it.company_id AND a.id <> it.id AND a.is_active AND lower(a.active_ingredient) = lower(it.active_ingredient)
			WHERE it.id = $1 AND it.company_id = $2 AND it.active_ingredient <> ''
			ORDER BY lower(a.strength) = lower(it.strength) DESC, coalesce((SELECT sum(s.quantity) FROM stock s WHERE s.item_id = a.id), 0) > 0 DESC, a.name`,
			item, r.PathValue("cid"))
	})
}

// GET /pharma/expiry?days= : lots in stock that have expired or expire within days (default: the company's alert days).
func (s *Server) pharmaExpiry(w http.ResponseWriter, r *http.Request) {
	days := 0
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 730 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		days = n
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if days == 0 {
			if err := tx.QueryRow(ctx, `SELECT expiry_alert_days FROM companies WHERE id = $1`, cid).Scan(&days); err != nil {
				return nil, err
			}
		}
		lots, err := queryJSON(ctx, tx, `SELECT l.id AS lot_id, l.lot_no, l.expiry, l.expiry - current_date AS days_left, it.id AS item_id, it.code AS item_code,
			it.name AS item_name, w.id AS warehouse_id, w.name AS warehouse, x.quantity::text AS quantity,
			round(x.quantity * coalesce(s.avg_cost, 0), 2)::text AS value, l.expiry < current_date AS expired
			FROM stock_lot_qty x JOIN stock_lots l ON l.id = x.lot_id JOIN items it ON it.id = l.item_id JOIN warehouses w ON w.id = x.warehouse_id
			LEFT JOIN stock s ON s.item_id = it.id AND s.warehouse_id = x.warehouse_id
			WHERE l.company_id = $1 AND x.quantity > 0 AND l.expiry IS NOT NULL AND l.expiry <= current_date + $2::int
			ORDER BY l.expiry, it.name`, cid, days)
		if err != nil {
			return nil, err
		}
		var expired, soon int
		var block bool
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE l.expiry < current_date), count(*) FILTER (WHERE l.expiry >= current_date),
			(SELECT block_expired FROM companies WHERE id = $1)
			FROM stock_lot_qty x JOIN stock_lots l ON l.id = x.lot_id WHERE l.company_id = $1 AND x.quantity > 0 AND l.expiry <= current_date + $2::int`,
			cid, days).Scan(&expired, &soon, &block); err != nil {
			return nil, err
		}
		return map[string]any{"days": days, "lots": lots, "expired": expired, "expiring": soon, "block_expired": block}, nil
	})
}

// GET/PUT /pharma/settings {block_expired, expiry_alert_days}
func (s *Server) pharmaSettings(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	get := func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT block_expired, expiry_alert_days FROM companies WHERE id = $1`, cid)
	}
	if r.Method != http.MethodPut {
		s.run(w, r, http.StatusOK, get)
		return
	}
	var in struct {
		BlockExpired *bool `json:"block_expired"`
		AlertDays    *int  `json:"expiry_alert_days"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.AlertDays != nil && (*in.AlertDays < 1 || *in.AlertDays > 730) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE companies SET block_expired = coalesce($2, block_expired), expiry_alert_days = coalesce($3, expiry_alert_days) WHERE id = $1`,
			cid, in.BlockExpired, in.AlertDays); err != nil {
			return nil, err
		}
		return get(ctx, tx)
	})
}

// GET /pharma/controlled?from=&to=&all=1 : the register of controlled (all=1: and prescription) drugs sold at the till,
// with the prescription.
func (s *Server) pharmaControlled(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	all := r.URL.Query().Get("all") == "1"
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT po.id AS pos_order_id, po.number AS pos_number, po.order_date, po.created_at, c.name AS customer_name,
			it.id AS item_id, it.code AS item_code, it.name AS item_name, it.controlled, it.rx_required, l.quantity::text AS quantity, po.rx,
			u.full_name AS sold_by
			FROM pos_orders po JOIN invoice_lines l ON l.invoice_id = po.invoice_id JOIN items it ON it.id = l.item_id JOIN customers c ON c.id = po.customer_id
			LEFT JOIN invoices i ON i.id = po.invoice_id LEFT JOIN journal_entries je ON je.id = i.journal_entry_id LEFT JOIN users u ON u.id = je.created_by
			WHERE po.company_id = $1 AND po.kind = 'sale' AND po.order_date BETWEEN $2::date AND $3::date AND (it.controlled OR ($4 AND it.rx_required))
			ORDER BY po.created_at DESC LIMIT 2000`, r.PathValue("cid"), from, to, all)
	})
}

const rxClaimCols = `k.id, k.number, k.pos_order_id, po.number AS pos_number, po.order_date, k.insurer_id, c.name AS insurer, k.patient, k.policy_no,
	k.currency, k.amount::text AS amount, k.status, k.settled_at, k.note, k.created_at`

// GET /pharma/claims?status=&insurer_id=
func (s *Server) pharmaClaims(w http.ResponseWriter, r *http.Request) {
	status, ins := r.URL.Query().Get("status"), r.URL.Query().Get("insurer_id")
	if (status != "" && !contains([]string{"open", "pending", "submitted", "paid", "rejected"}, status)) || (ins != "" && !isUUID(ins)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT `+rxClaimCols+` FROM pharma_claims k JOIN customers c ON c.id = k.insurer_id JOIN pos_orders po ON po.id = k.pos_order_id
			WHERE k.company_id = $1 AND ($2 = '' OR ($2 = 'open' AND k.status IN ('pending', 'submitted')) OR k.status = $2)
			  AND ($3 = '' OR k.insurer_id = $3::uuid) ORDER BY k.created_at DESC LIMIT 1000`, r.PathValue("cid"), status, ins)
		if err != nil {
			return nil, err
		}
		totals, err := queryJSON(ctx, tx, `SELECT c.name AS insurer, k.currency, k.status, count(*) AS claims, sum(k.amount)::text AS amount
			FROM pharma_claims k JOIN customers c ON c.id = k.insurer_id WHERE k.company_id = $1 AND k.status IN ('pending', 'submitted')
			GROUP BY c.name, k.currency, k.status ORDER BY c.name`, r.PathValue("cid"))
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"claims": rows, "open_totals": totals}, nil
	})
}

// POST /pharma/claims/{id}/{action}: submit | settle {method_id, date} | reject {note}
func (s *Server) pharmaClaimAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MethodID string `json:"method_id"`
		Date     string `json:"date"`
		Note     string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	action := r.PathValue("action")
	if !validDate(in.Date) || len(in.Note) > 500 || (action == "settle" && !isUUID(in.MethodID)) || !contains([]string{"submit", "settle", "reject"}, action) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, cur, amount, insurer string
		var number int64
		err := tx.QueryRow(ctx, `SELECT k.status, k.currency::text, k.amount::text, c.name, k.number FROM pharma_claims k JOIN customers c ON c.id = k.insurer_id
			WHERE k.id = $1 AND k.company_id = $2 FOR UPDATE OF k`, id, cid).Scan(&status, &cur, &amount, &insurer, &number)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "pending" && status != "submitted" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		note := strings.TrimSpace(in.Note)
		switch action {
		case "submit":
			if _, err := tx.Exec(ctx, `UPDATE pharma_claims SET status = 'submitted', note = CASE WHEN $2 = '' THEN note ELSE $2 END WHERE id = $1`, id, note); err != nil {
				return nil, err
			}
		case "settle", "reject":
			set, err := loadBillingSettings(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			acc, err := ensureRetailAccount(ctx, tx, cid, "insurance_account_id")
			if err != nil {
				return nil, err
			}
			amt, _ := decimal.NewFromString(amount)
			var other, memo string
			if action == "settle" {
				methods, err := loadMethods(ctx, tx, cid)
				if err != nil {
					return nil, err
				}
				m, ok := methods[in.MethodID]
				if !ok || !m.Active || m.Kind == "gift" || m.Kind == "loyalty" || m.Kind == "tradein" || m.Kind == "insurance" {
					return nil, badRequest("invalid_method")
				}
				if m.Currency != cur {
					return nil, badRequest("currency_mismatch")
				}
				other, memo = m.Account, fmt.Sprintf("Insurance claim #%d paid · %s", number, insurer)
			} else {
				ps, err := loadPurchSettings(ctx, tx, cid)
				if err != nil {
					return nil, err
				}
				if ps.Expense == nil {
					return nil, notSetup("expense")
				}
				other, memo = *ps.Expense, fmt.Sprintf("Insurance claim #%d rejected · %s", number, insurer)
			}
			je, err := postJournal(ctx, tx, cid, set, in.Date, memo, fmt.Sprintf("CLM-%d", number), "manual", []jline{
				{Account: other, Desc: memo, Currency: cur, Debit: amt},
				{Account: acc, Desc: memo, Currency: cur, Credit: amt}})
			if err != nil {
				return nil, err
			}
			st := map[string]string{"settle": "paid", "reject": "rejected"}[action]
			if _, err := tx.Exec(ctx, `UPDATE pharma_claims SET status = $2, settled_entry_id = $3, settled_at = now(), note = CASE WHEN $4 = '' THEN note ELSE $4 END
				WHERE id = $1`, id, st, je, note); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+rxClaimCols+` FROM pharma_claims k JOIN customers c ON c.id = k.insurer_id JOIN pos_orders po ON po.id = k.pos_order_id
			WHERE k.id = $1`, id)
	})
}
