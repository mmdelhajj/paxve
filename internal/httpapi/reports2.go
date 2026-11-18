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

// Financial statements in the company base currency, from posted journal lines.

const postedLines = `journal_lines l
	JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
	JOIN accounts a ON a.id = l.account_id`

// profitLoss: income (credit - debit) and expenses (debit - credit) for a period.
func (s *Server) profitLoss(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rows, err := queryJSON(ctx, tx, `
			SELECT a.id, a.code, a.name, a.name_i18n, a.type,
			       (CASE WHEN a.type = 'income' THEN sum(l.base_credit - l.base_debit) ELSE sum(l.base_debit - l.base_credit) END)::text AS amount
			FROM `+postedLines+`
			WHERE e.company_id = $1 AND a.type IN ('income', 'expense') AND e.entry_date BETWEEN $2::date AND $3::date
			GROUP BY a.id ORDER BY a.code`, cid, from, to)
		if err != nil {
			return nil, err
		}
		var income, expense string
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(l.base_credit - l.base_debit) FILTER (WHERE a.type = 'income'), 0)::text,
			       coalesce(sum(l.base_debit - l.base_credit) FILTER (WHERE a.type = 'expense'), 0)::text
			FROM `+postedLines+` WHERE e.company_id = $1 AND e.entry_date BETWEEN $2::date AND $3::date`, cid, from, to).Scan(&income, &expense); err != nil {
			return nil, err
		}
		inc, _ := decimal.NewFromString(income)
		exp, _ := decimal.NewFromString(expense)
		return map[string]any{"base_currency": base, "from": from, "to": to, "rows": rows,
			"total_income": income, "total_expense": expense, "net": inc.Sub(exp).StringFixed(4)}, nil
	})
}

// balanceSheet: balances at a date; this year's not-yet-closed result is shown inside equity.
func (s *Server) balanceSheet(w http.ResponseWriter, r *http.Request) {
	to := r.URL.Query().Get("to")
	if to == "" {
		to = today()
	}
	if !validDate(to) {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rows, err := queryJSON(ctx, tx, `
			SELECT a.id, a.code, a.name, a.name_i18n, a.type,
			       (CASE WHEN a.type = 'asset' THEN sum(l.base_debit - l.base_credit) ELSE sum(l.base_credit - l.base_debit) END)::text AS amount
			FROM `+postedLines+`
			WHERE e.company_id = $1 AND a.type IN ('asset', 'liability', 'equity') AND e.entry_date <= $2::date
			GROUP BY a.id HAVING sum(l.base_debit - l.base_credit) <> 0 ORDER BY a.code`, cid, to)
		if err != nil {
			return nil, err
		}
		var assets, liab, equity, result string
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(l.base_debit - l.base_credit) FILTER (WHERE a.type = 'asset'), 0)::text,
			       coalesce(sum(l.base_credit - l.base_debit) FILTER (WHERE a.type = 'liability'), 0)::text,
			       coalesce(sum(l.base_credit - l.base_debit) FILTER (WHERE a.type = 'equity'), 0)::text,
			       coalesce(sum(l.base_credit - l.base_debit) FILTER (WHERE a.type IN ('income', 'expense')), 0)::text
			FROM `+postedLines+` WHERE e.company_id = $1 AND e.entry_date <= $2::date`, cid, to).Scan(&assets, &liab, &equity, &result); err != nil {
			return nil, err
		}
		l, _ := decimal.NewFromString(liab)
		eq, _ := decimal.NewFromString(equity)
		res, _ := decimal.NewFromString(result)
		return map[string]any{"base_currency": base, "to": to, "rows": rows, "total_assets": assets,
			"total_liabilities": liab, "total_equity": equity, "current_result": result,
			"total_liabilities_equity": l.Add(eq).Add(res).StringFixed(4)}, nil
	})
}

// agingReport: what each customer owes, by how late it is (days after the due date).
func (s *Server) agingReport(w http.ResponseWriter, r *http.Request) {
	asOf := r.URL.Query().Get("as_of")
	if asOf == "" {
		asOf = today()
	}
	area := r.URL.Query().Get("area_id")
	if !validDate(asOf) || (area != "" && !isUUID(area)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		bucket := `CASE WHEN $2::date - i.due_date <= 0 THEN 'current' WHEN $2::date - i.due_date <= 30 THEN 'd30'
			WHEN $2::date - i.due_date <= 60 THEN 'd60' WHEN $2::date - i.due_date <= 90 THEN 'd90' ELSE 'over90' END`
		sum := func(b string) string {
			return fmt.Sprintf("coalesce(sum(i.total - i.paid) FILTER (WHERE %s = '%s'), 0)::text AS %s", bucket, b, b)
		}
		cols := strings.Join([]string{sum("current"), sum("d30"), sum("d60"), sum("d90"), sum("over90")}, ", ")
		where := `i.company_id = $1 AND i.status IN ('open', 'partial') AND i.issue_date <= $2::date AND ($3 = '' OR c.area_id = $3::uuid)`
		rows, err := queryJSON(ctx, tx, `
			SELECT c.id AS customer_id, c.code, c.name, c.phone, ar.name AS area_name, i.currency, `+cols+`,
			       sum(i.total - i.paid)::text AS total, count(*) AS invoices, min(i.due_date) AS oldest_due
			FROM invoices i JOIN customers c ON c.id = i.customer_id LEFT JOIN areas ar ON ar.id = c.area_id
			WHERE `+where+`
			GROUP BY c.id, c.code, c.name, c.phone, ar.name, i.currency
			ORDER BY sum(i.total - i.paid) DESC`, cid, asOf, area)
		if err != nil {
			return nil, err
		}
		totals, err := queryJSON(ctx, tx, `SELECT i.currency, `+cols+`, sum(i.total - i.paid)::text AS total
			FROM invoices i JOIN customers c ON c.id = i.customer_id WHERE `+where+` GROUP BY i.currency ORDER BY i.currency`, cid, asOf, area)
		if err != nil {
			return nil, err
		}
		return map[string]any{"as_of": asOf, "rows": rows, "totals": totals}, nil
	})
}

// collectionsReport: money received per collector (user) and currency, and per day.
func (s *Server) collectionsReport(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		byUser, err := queryJSON(ctx, tx, `
			SELECT p.received_by AS user_id, coalesce(u.full_name, '—') AS user_name, p.currency, count(*) AS payments,
			       sum(p.amount)::text AS amount, count(DISTINCT p.customer_id) AS customers
			FROM payments p LEFT JOIN users u ON u.id = p.received_by
			WHERE p.company_id = $1 AND p.status = 'posted' AND p.kind = 'payment' AND p.payment_date BETWEEN $2::date AND $3::date
			GROUP BY p.received_by, u.full_name, p.currency ORDER BY u.full_name, p.currency`, cid, from, to)
		if err != nil {
			return nil, err
		}
		byDay, err := queryJSON(ctx, tx, `
			SELECT p.payment_date AS date, p.currency, count(*) AS payments, sum(p.amount)::text AS amount
			FROM payments p WHERE p.company_id = $1 AND p.status = 'posted' AND p.kind = 'payment' AND p.payment_date BETWEEN $2::date AND $3::date
			GROUP BY p.payment_date, p.currency ORDER BY p.payment_date DESC, p.currency`, cid, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"by_user": byUser, "by_day": byDay}, nil
	})
}

// ---- cash boxes ----

// cashBalances: what each box (payment method) holds, in its own currency, from the ledger.
func (s *Server) cashBalances(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `
			SELECT m.id, m.name, m.kind, m.currency, m.user_id, u.full_name AS user_name, a.code AS account_code,
			       coalesce((SELECT sum(l.debit - l.credit) FROM journal_lines l
			                 JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
			                 WHERE l.account_id = m.account_id AND l.currency = m.currency), 0)::text AS balance
			FROM payment_methods m JOIN accounts a ON a.id = m.account_id LEFT JOIN users u ON u.id = m.user_id
			WHERE m.company_id = $1 AND m.is_active AND (NOT $2 OR m.user_id = app_user())
			ORDER BY m.user_id NULLS FIRST, m.sort, m.name`, cid, isCollector(r))
	})
}

func (s *Server) listTransfers(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `
			SELECT t.id, t.number, t.transfer_date, fm.name AS from_method, tm.name AS to_method, t.currency,
			       t.amount::text AS amount, t.notes, u.full_name AS created_by, t.created_at
			FROM cash_transfers t JOIN payment_methods fm ON fm.id = t.from_method_id JOIN payment_methods tm ON tm.id = t.to_method_id
			LEFT JOIN users u ON u.id = t.created_by
			WHERE t.company_id = $1 AND t.transfer_date BETWEEN $2::date AND $3::date
			ORDER BY t.transfer_date DESC, t.number DESC LIMIT 500`, r.PathValue("cid"), from, to)
	})
}

// createTransfer moves money between two boxes of the same currency (e.g. collector hands cash to the office).
func (s *Server) createTransfer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date         string `json:"date"`
		FromMethodID string `json:"from_method_id"`
		ToMethodID   string `json:"to_method_id"`
		Amount       string `json:"amount"`
		Notes        string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = time.Now().Format(time.DateOnly)
	}
	amt, ok := money(in.Amount)
	if !validDate(in.Date) || !isUUID(in.FromMethodID) || !isUUID(in.ToMethodID) || in.FromMethodID == in.ToMethodID || !ok || !amt.IsPositive() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		methods, err := loadMethods(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		from, ok1 := methods[in.FromMethodID]
		to, ok2 := methods[in.ToMethodID]
		if !ok1 || !ok2 || !from.Active || !to.Active {
			return nil, badRequest("invalid_tender")
		}
		if from.Currency != to.Currency {
			return nil, badRequest("currency_mismatch")
		}
		rate, err := resolveRate(ctx, tx, cid, set.Base, from.Currency, in.Date, "")
		if err != nil {
			return nil, err
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'transfer')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		memo := fmt.Sprintf("Transfer #%d · %s → %s", number, from.Name, to.Name)
		je, err := postJournal(ctx, tx, cid, set, in.Date, memo, fmt.Sprintf("TRF-%d", number), "transfer", []jline{
			{Account: to.Account, Desc: to.Name, Currency: to.Currency, Debit: amt, Rate: rate},
			{Account: from.Account, Desc: from.Name, Currency: from.Currency, Credit: amt, Rate: rate},
		})
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO cash_transfers (company_id, number, transfer_date, from_method_id, to_method_id,
			currency, amount, journal_entry_id, notes) VALUES ($1, $2, $3::date, $4, $5, $6, $7::numeric, $8, $9) RETURNING id::text`,
			cid, number, in.Date, from.ID, to.ID, from.Currency, amt.String(), je, strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "number": number}, nil
	})
}
