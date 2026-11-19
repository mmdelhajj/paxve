package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Unrealised exchange revaluation. At a month end, money held or owed in a foreign currency (cash boxes and banks,
// customer receivables, supplier payables, accounts kept in a foreign currency) is valued again at that day's rate.
// The difference is booked as unrealised exchange gain / loss on the last day of the month and reversed on the
// first day of the next month, so realised differences keep being booked normally when bills are paid.

type revalRow struct {
	AccountID   string `json:"account_id"`
	AccountCode string `json:"account_code"`
	AccountName string `json:"account_name"`
	Currency    string `json:"currency"`
	Balance     string `json:"balance"`     // in the currency
	BookedBase  string `json:"booked_base"` // base value as booked
	Rate        string `json:"rate"`        // month-end rate (units of currency per 1 base)
	RevalBase   string `json:"revalued_base"`
	Difference  string `json:"difference"` // > 0 gain, < 0 loss (for an asset; the sign is handled per account side)
}

// revaluation computes the rows for a month end (no posting).
func revaluation(ctx context.Context, tx pgx.Tx, cid, period string) ([]revalRow, string, error) {
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return nil, "", err
	}
	end := periodEnd(period)
	rows, err := tx.Query(ctx, `
		WITH monetary AS (
		  SELECT ar_account_id AS id FROM companies WHERE id = $1 AND ar_account_id IS NOT NULL
		  UNION SELECT ap_account_id FROM companies WHERE id = $1 AND ap_account_id IS NOT NULL
		  UNION SELECT account_id FROM payment_methods WHERE company_id = $1
		  UNION SELECT id FROM accounts WHERE company_id = $1 AND currency IS NOT NULL AND currency <> $2 AND type IN ('asset', 'liability')),
		reval AS (
		  SELECT e.id FROM journal_entries e WHERE e.company_id = $1 AND e.source = 'fx_reval'
		  UNION SELECT e.reversed_by FROM journal_entries e WHERE e.company_id = $1 AND e.source = 'fx_reval' AND e.reversed_by IS NOT NULL),
		fxcur AS ( -- settlement differences are kept in the base currency; give them back to the foreign currency they belong to
		  SELECT a.fx_entry_id AS entry_id, p.currency::text AS cur FROM payment_allocations a JOIN payments p ON p.id = a.payment_id
		  WHERE a.fx_entry_id IS NOT NULL AND p.company_id = $1
		  UNION SELECT a.fx_entry_id, p.currency::text FROM supplier_allocations a JOIN supplier_payments p ON p.id = a.payment_id
		  WHERE a.fx_entry_id IS NOT NULL AND p.company_id = $1)
		SELECT a.id::text, a.code, a.name, coalesce(f.cur, l.currency::text) AS cur,
		  sum(CASE WHEN l.currency::text = $2 THEN 0 ELSE l.debit - l.credit END)::text,
		  sum(l.base_debit - l.base_credit)::text
		FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id JOIN accounts a ON a.id = l.account_id
		LEFT JOIN fxcur f ON f.entry_id = e.id AND l.currency::text = $2
		WHERE e.company_id = $1 AND e.status = 'posted' AND e.entry_date <= $3::date AND l.account_id IN (SELECT id FROM monetary)
		  AND e.id NOT IN (SELECT id FROM reval WHERE id IS NOT NULL)
		  AND coalesce(f.cur, l.currency::text) <> $2
		GROUP BY a.id, a.code, a.name, 4
		HAVING sum(CASE WHEN l.currency::text = $2 THEN 0 ELSE l.debit - l.credit END) <> 0 OR sum(l.base_debit - l.base_credit) <> 0
		ORDER BY a.code, 4`, cid, set.Base, end)
	if err != nil {
		return nil, "", err
	}
	type raw struct{ id, code, name, cur, bal, base string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (raw, error) {
		var x raw
		err := row.Scan(&x.id, &x.code, &x.name, &x.cur, &x.bal, &x.base)
		return x, err
	})
	if err != nil {
		return nil, "", err
	}
	out := []revalRow{}
	for _, x := range list {
		rate, err := resolveRate(ctx, tx, cid, set.Base, x.cur, end, "")
		if err != nil {
			return nil, "", err
		}
		rt, _ := decimal.NewFromString(rate)
		bal, _ := decimal.NewFromString(x.bal)
		booked, _ := decimal.NewFromString(x.base)
		reval := bal.DivRound(rt, 4)
		diff := reval.Sub(booked).Round(2)
		if diff.IsZero() {
			continue
		}
		out = append(out, revalRow{AccountID: x.id, AccountCode: x.code, AccountName: x.name, Currency: x.cur, Balance: bal.String(),
			BookedBase: booked.StringFixed(2), Rate: rate, RevalBase: reval.StringFixed(2), Difference: diff.StringFixed(2)})
	}
	return out, set.Base, nil
}

// GET /companies/{cid}/fx-revaluation/preview?period=YYYY-MM
func (s *Server) previewRevaluation(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = time.Now().AddDate(0, -1, 0).Format("2006-01")
	}
	if !validPeriod(period) {
		writeErr(w, http.StatusBadRequest, "invalid_period")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, base, err := revaluation(ctx, tx, cid, period)
		if err != nil {
			return nil, err
		}
		gain, loss := decimal.Zero, decimal.Zero
		for _, x := range rows {
			d, _ := decimal.NewFromString(x.Difference)
			if d.IsPositive() {
				gain = gain.Add(d)
			} else {
				loss = loss.Add(d.Neg())
			}
		}
		var done json.RawMessage
		_ = tx.QueryRow(ctx, `SELECT row_to_json(x) FROM (SELECT id, entry_id, reversal_id, created_at FROM fx_revaluations
			WHERE company_id = $1 AND period = $2) x`, cid, period).Scan(&done)
		return map[string]any{"period": period, "date": periodEnd(period), "base_currency": base, "rows": rows,
			"total_gain": gain.StringFixed(2), "total_loss": loss.StringFixed(2), "net": gain.Sub(loss).StringFixed(2), "posted": done}, nil
	})
}

// POST /companies/{cid}/fx-revaluation {period}: posts the revaluation on the month end and its reversal on the 1st.
func (s *Server) runRevaluation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Period string `json:"period"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !validPeriod(in.Period) {
		writeErr(w, http.StatusBadRequest, "invalid_period")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('fxreval:' || $1))`, cid); err != nil {
			return nil, err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM fx_revaluations WHERE company_id = $1 AND period = $2)`, cid, in.Period).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, &apiError{status: http.StatusConflict, code: "already_revalued"}
		}
		rows, base, err := revaluation(ctx, tx, cid, in.Period)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, badRequest("nothing_to_revalue")
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if set.FXGain == nil || set.FXLoss == nil {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "fx_gain"}
		}
		lines := []jline{}
		gain, loss := decimal.Zero, decimal.Zero
		for _, x := range rows {
			d, _ := decimal.NewFromString(x.Difference)
			desc := "Unrealised FX آ· " + x.Currency + " @ " + x.Rate
			if d.IsPositive() { // the account is worth more in base currency
				lines = append(lines, jline{Account: x.AccountID, Desc: desc, Currency: base, Debit: d, Rate: "1"})
				gain = gain.Add(d)
			} else {
				lines = append(lines, jline{Account: x.AccountID, Desc: desc, Currency: base, Credit: d.Neg(), Rate: "1"})
				loss = loss.Add(d.Neg())
			}
		}
		if gain.IsPositive() {
			lines = append(lines, jline{Account: *set.FXGain, Desc: "Unrealised exchange gain " + in.Period, Currency: base, Credit: gain, Rate: "1"})
		}
		if loss.IsPositive() {
			lines = append(lines, jline{Account: *set.FXLoss, Desc: "Unrealised exchange loss " + in.Period, Currency: base, Debit: loss, Rate: "1"})
		}
		end := periodEnd(in.Period)
		je, err := postJournal(ctx, tx, cid, set, end, "Exchange revaluation "+in.Period, "FXR-"+in.Period, "fx_reval", lines)
		if err != nil {
			return nil, err
		}
		t, _ := time.Parse(time.DateOnly, end)
		var rev string
		if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, $2::date, $3)::text`, je, t.AddDate(0, 0, 1).Format(time.DateOnly),
			"Reversal of exchange revaluation "+in.Period).Scan(&rev); err != nil {
			return nil, err
		}
		det, _ := json.Marshal(rows)
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO fx_revaluations (company_id, period, entry_id, reversal_id, total_gain, total_loss, details)
			VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7) RETURNING id::text`, cid, in.Period, je, rev, gain.String(), loss.String(), det).Scan(&id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "period": in.Period, "entry_id": je, "reversal_id": rev, "total_gain": gain.StringFixed(2),
			"total_loss": loss.StringFixed(2), "rows": rows}, nil
	})
}

// GET /companies/{cid}/fx-revaluation: past revaluations.
func (s *Server) listRevaluations(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT f.id, f.period, f.entry_id, e.number AS entry_number, f.reversal_id, re.number AS reversal_number,
			f.total_gain::text AS total_gain, f.total_loss::text AS total_loss, f.details, f.created_at
			FROM fx_revaluations f JOIN journal_entries e ON e.id = f.entry_id LEFT JOIN journal_entries re ON re.id = f.reversal_id
			WHERE f.company_id = $1 ORDER BY f.period DESC`, r.PathValue("cid"))
	})
}
