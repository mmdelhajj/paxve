package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Customer credit limit (Odoo gap): a limit in the company's base currency and/or a maximum number of days an
// invoice may be overdue, per customer (the company can set a default for the days). The company policy decides
// what happens when a new invoice, a confirmed order, a delivery or a sale on account would break it:
//
//	off   – nothing
//	warn  – the user is told and must confirm (header X-Credit-Confirm: 1)
//	block – refused; someone holding sales.credit_override may go on (X-Credit-Override: 1), anyone else can
//	        send the same call for approval (X-Credit-Request-Approval: 1, see creditGate).
//
// Exposure = what the customer owes on open invoices − money paid but not applied + confirmed sales orders not
// invoiced yet, every currency converted to the base currency at today's rate.

type creditInfo struct {
	Policy      string  `json:"policy"`
	Base        string  `json:"base_currency"`
	Limit       *string `json:"limit"`
	OverdueDays *int    `json:"overdue_days"` // the rule: the customer's, else the company default
	Hold        bool    `json:"hold"`
	Exposure    string  `json:"exposure"`
	Open        string  `json:"open_invoices"`
	Unapplied   string  `json:"unapplied_credits"`
	Orders      string  `json:"open_orders"`
	MaxOverdue  int     `json:"max_overdue_days"`
	Available   *string `json:"available"`
	NoRate      bool    `json:"no_rate,omitempty"` // a currency had no rate: left out of the total
	exposure    decimal.Decimal
	limit       *decimal.Decimal
}

func customerCreditInfo(ctx context.Context, tx pgx.Tx, cid, customerID string) (*creditInfo, error) {
	ci := &creditInfo{}
	var lim *string
	var custDays, compDays *int
	err := tx.QueryRow(ctx, `SELECT co.credit_policy, co.base_currency::text, c.credit_limit::text, c.credit_overdue_days, co.credit_overdue_days, c.credit_hold
		FROM customers c JOIN companies co ON co.id = c.company_id WHERE c.id = $1 AND c.company_id = $2`, customerID, cid).
		Scan(&ci.Policy, &ci.Base, &lim, &custDays, &compDays, &ci.Hold)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	ci.OverdueDays = custDays
	if ci.OverdueDays == nil {
		ci.OverdueDays = compDays
	}
	if lim != nil {
		d, _ := decimal.NewFromString(*lim)
		ci.limit = &d
		s := d.StringFixed(2)
		ci.Limit = &s
	}
	rows, err := tx.Query(ctx, `
		SELECT cur, sum(open)::text, sum(unapplied)::text, sum(orders)::text FROM (
		  SELECT currency::text AS cur, balance AS open, 0 AS unapplied, 0 AS orders FROM ar_open_items
		    WHERE company_id = $1 AND customer_id = $2 AND status IN ('open', 'partial')
		  UNION ALL
		  SELECT p.currency::text, 0, p.amount - coalesce((SELECT sum(pa.amount) FROM payment_allocations pa WHERE pa.payment_id = p.id), 0), 0
		    FROM payments p WHERE p.company_id = $1 AND p.customer_id = $2 AND p.status = 'posted'
		  UNION ALL
		  SELECT o.currency::text, 0, 0, (l.amount + l.tax_amount) * greatest(l.quantity - l.qty_invoiced, 0) / l.quantity
		    FROM sales_orders o JOIN sales_order_lines l ON l.order_id = o.id
		    WHERE o.company_id = $1 AND o.customer_id = $2 AND o.status = 'confirmed'
		) x GROUP BY cur`, cid, customerID)
	if err != nil {
		return nil, err
	}
	type part struct{ cur, open, unapplied, orders string }
	parts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (part, error) {
		var p part
		err := row.Scan(&p.cur, &p.open, &p.unapplied, &p.orders)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	open, unapplied, orders := decimal.Zero, decimal.Zero, decimal.Zero
	for _, p := range parts {
		o, _ := decimal.NewFromString(p.open)
		u, _ := decimal.NewFromString(p.unapplied)
		r, _ := decimal.NewFromString(p.orders)
		if p.cur != ci.Base {
			rate, err := resolveRate(ctx, tx, cid, ci.Base, p.cur, today(), "")
			rd, _ := decimal.NewFromString(rate)
			if err != nil || !rd.IsPositive() {
				ci.NoRate = true
				continue
			}
			o, u, r = o.Div(rd), u.Div(rd), r.Div(rd)
		}
		open, unapplied, orders = open.Add(o), unapplied.Add(u), orders.Add(r)
	}
	ci.exposure = open.Sub(unapplied).Add(orders)
	ci.Exposure, ci.Open, ci.Unapplied, ci.Orders = ci.exposure.StringFixed(2), open.StringFixed(2), unapplied.StringFixed(2), orders.StringFixed(2)
	if ci.limit != nil {
		s := ci.limit.Sub(ci.exposure).StringFixed(2)
		ci.Available = &s
	}
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(current_date - due_date), 0) FROM ar_open_items
		WHERE company_id = $1 AND customer_id = $2 AND status IN ('open', 'partial') AND kind = 'invoice' AND balance > 0 AND due_date < current_date`,
		cid, customerID).Scan(&ci.MaxOverdue); err != nil {
		return nil, err
	}
	return ci, nil
}

func isReplay(r *http.Request) bool {
	_, ok := r.Context().Value(approvedExecKey{}).(*approvedExec)
	return ok
}

// checkCredit refuses (409 credit_limit) a document of amount (in currency) for the customer when it breaks the
// credit rules and the caller did not confirm / override. r == nil (internal jobs) never checks.
func checkCredit(ctx context.Context, tx pgx.Tx, r *http.Request, cid, customerID, currency string, amount decimal.Decimal) error {
	if r == nil || isReplay(r) {
		return nil
	}
	var policy string
	if err := tx.QueryRow(ctx, `SELECT credit_policy FROM companies WHERE id = $1`, cid).Scan(&policy); err != nil {
		return err
	}
	if policy == "off" {
		return nil
	}
	ci, err := customerCreditInfo(ctx, tx, cid, customerID)
	if err != nil {
		return err
	}
	add := amount
	if amount.IsPositive() && currency != ci.Base {
		rate, err := resolveRate(ctx, tx, cid, ci.Base, currency, today(), "")
		rd, _ := decimal.NewFromString(rate)
		if err != nil || !rd.IsPositive() {
			add = decimal.Zero
		} else {
			add = amount.Div(rd)
		}
	}
	var reasons []string
	if ci.Hold {
		reasons = append(reasons, "hold")
	}
	if ci.limit != nil && ci.exposure.Add(add).GreaterThan(*ci.limit) {
		reasons = append(reasons, "limit")
	}
	if ci.OverdueDays != nil && ci.MaxOverdue > *ci.OverdueDays {
		reasons = append(reasons, "overdue")
	}
	if len(reasons) == 0 {
		return nil
	}
	if r.Header.Get("X-Credit-Override") == "1" && can(r, "sales.credit_override") {
		return nil
	}
	if policy == "warn" && r.Header.Get("X-Credit-Confirm") == "1" {
		return nil
	}
	after := ci.exposure.Add(add)
	detail, _ := json.Marshal(map[string]any{"policy": policy, "reasons": reasons, "limit": ci.Limit, "exposure": ci.Exposure,
		"amount": add.StringFixed(2), "after": after.StringFixed(2), "base_currency": ci.Base, "overdue_days": ci.OverdueDays,
		"max_overdue_days": ci.MaxOverdue, "can_override": can(r, "sales.credit_override")})
	return &apiError{status: http.StatusConflict, code: "credit_limit", detail: string(detail)}
}

// creditGate: a call refused by the credit rules can be sent again with X-Credit-Request-Approval: 1. It is then
// stored as an approval request (kind credit_limit) instead of run; the approver's decision replays it, and the
// replay skips the credit check (isReplay).
func (s *Server) creditGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Credit-Request-Approval") != "1" || isReplay(r) {
			next(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil || (len(bytes.TrimSpace(body)) > 0 && !json.Valid(body)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		var js []byte
		if len(bytes.TrimSpace(body)) > 0 {
			js = body
		}
		var hint struct {
			CustomerID string `json:"customer_id"`
		}
		_ = json.Unmarshal(body, &hint)
		cid, perm := r.PathValue("cid"), permForPattern(r.Pattern)
		if perm == "" {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		note := strings.TrimSpace(r.Header.Get("X-Credit-Note"))
		if len(note) > 200 {
			note = note[:200]
		}
		s.run(w, r, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx) (any, error) {
			summary := "Credit limit"
			if isUUID(hint.CustomerID) {
				var name string
				if err := tx.QueryRow(ctx, `SELECT name FROM customers WHERE id = $1 AND company_id = $2`, hint.CustomerID, cid).Scan(&name); err == nil {
					summary += " · " + name
				}
			}
			if note != "" {
				summary += " · " + note
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO approval_requests (company_id, kind, permission, method, pattern, path, body, summary)
				VALUES ($1, 'credit_limit', $2, $3, $4, $5, $6, left($7, 300)) RETURNING id::text`,
				cid, perm, r.Method, r.Pattern, r.URL.Path, js, summary).Scan(&id); err != nil {
				return nil, err
			}
			return map[string]any{"approval_required": true, "approval_id": id, "kind": "credit_limit"}, nil
		})
	}
}

// GET /companies/{cid}/customers/{id}/credit: the credit rules and the exposure of one customer.
func (s *Server) getCustomerCredit(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		ci, err := customerCreditInfo(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		var fp *string
		var custDays *int
		if err := tx.QueryRow(ctx, `SELECT fiscal_position_id::text, credit_overdue_days FROM customers WHERE id = $1`, id).Scan(&fp, &custDays); err != nil {
			return nil, err
		}
		return map[string]any{"credit": ci, "customer_overdue_days": custDays, "fiscal_position_id": fp}, nil
	})
}

// PUT /companies/{cid}/customers/{id}/credit {credit_limit ("" = none), credit_overdue_days (null = company default),
// credit_hold, fiscal_position_id ("" = none)}: every field optional.
func (s *Server) putCustomerCredit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Limit       *string `json:"credit_limit"`
		OverdueDays *int    `json:"credit_overdue_days"`
		ClearDays   bool    `json:"clear_overdue_days"`
		Hold        *bool   `json:"credit_hold"`
		FiscalPos   *string `json:"fiscal_position_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if in.Limit != nil && strings.TrimSpace(*in.Limit) != "" {
		if _, ok := money(*in.Limit); !ok {
			writeErr(w, http.StatusBadRequest, "invalid_amount")
			return
		}
	}
	if (in.OverdueDays != nil && (*in.OverdueDays < 0 || *in.OverdueDays > 3650)) || !optUUID(in.FiscalPos) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Limit != nil && !can(r, "sales.credit_override") && !can(r, "accounting.manage") {
		// raising one's own customers' limits is a control: only people who could override it may set it
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := lockCustomer(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		if in.FiscalPos != nil && *in.FiscalPos != "" {
			if err := checkFiscalPosition(ctx, tx, cid, *in.FiscalPos); err != nil {
				return nil, err
			}
		}
		var lim *string
		if in.Limit != nil {
			if v := strings.TrimSpace(*in.Limit); v != "" {
				lim = &v
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE customers SET
			credit_limit = CASE WHEN $3 THEN $4::numeric ELSE credit_limit END,
			credit_overdue_days = CASE WHEN $6 THEN NULL WHEN $5::int IS NOT NULL THEN $5::int ELSE credit_overdue_days END,
			credit_hold = coalesce($7, credit_hold),
			fiscal_position_id = CASE WHEN $8::text IS NULL THEN fiscal_position_id ELSE nullif($8, '')::uuid END
			WHERE id = $1 AND company_id = $2`, id, cid, in.Limit != nil, lim, in.OverdueDays, in.ClearDays, in.Hold, in.FiscalPos); err != nil {
			return nil, err
		}
		ci, err := customerCreditInfo(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"credit": ci}, nil
	})
}

// GET/PUT /companies/{cid}/credit-settings {policy, overdue_days}
func (s *Server) getCreditSettings(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT credit_policy AS policy, credit_overdue_days AS overdue_days, base_currency
			FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}

func (s *Server) putCreditSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Policy      string `json:"policy"`
		OverdueDays *int   `json:"overdue_days"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if (in.Policy != "off" && in.Policy != "warn" && in.Policy != "block") || (in.OverdueDays != nil && (*in.OverdueDays < 0 || *in.OverdueDays > 3650)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return dmlJSONOne(ctx, tx, `UPDATE companies SET credit_policy = $2, credit_overdue_days = $3 WHERE id = $1
			RETURNING credit_policy AS policy, credit_overdue_days AS overdue_days, base_currency`, r.PathValue("cid"), in.Policy, in.OverdueDays)
	})
}

// GET /companies/{cid}/credit/report: every customer with a limit, a hold or debt past the company's overdue rule.
func (s *Server) creditReport(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	var out []map[string]any
	err := db.InTenant(r.Context(), s.Pool, scope(r), func(tx pgx.Tx) error {
		ctx := r.Context()
		rows, err := tx.Query(ctx, `SELECT c.id::text, c.code, c.name FROM customers c JOIN companies co ON co.id = c.company_id
			WHERE c.company_id = $1 AND (c.credit_limit IS NOT NULL OR c.credit_hold OR c.credit_overdue_days IS NOT NULL
			  OR (co.credit_overdue_days IS NOT NULL AND EXISTS (SELECT 1 FROM ar_open_items o WHERE o.customer_id = c.id AND o.balance > 0
			      AND o.status IN ('open', 'partial') AND o.due_date < current_date - co.credit_overdue_days)))
			ORDER BY c.name LIMIT 1000`, cid)
		if err != nil {
			return err
		}
		type cust struct{ id, code, name string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (cust, error) {
			var c cust
			err := row.Scan(&c.id, &c.code, &c.name)
			return c, err
		})
		if err != nil {
			return err
		}
		for _, c := range list {
			ci, err := customerCreditInfo(ctx, tx, cid, c.id)
			if err != nil {
				return err
			}
			over := ci.limit != nil && ci.exposure.GreaterThan(*ci.limit)
			late := ci.OverdueDays != nil && ci.MaxOverdue > *ci.OverdueDays
			out = append(out, map[string]any{"customer_id": c.id, "code": c.code, "name": c.name, "credit": ci, "over_limit": over, "too_late": late})
		}
		return nil
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, out)
}
