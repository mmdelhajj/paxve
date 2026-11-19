package httpapi

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// plTotals: like ledgerTotals, without the year-closing entries (profit & loss of a period, see pl_totals).
func plTotals(cid, from, to string) string {
	return `pl_totals(` + cid + `, ` + from + `::date, ` + to + `::date) l JOIN accounts a ON a.id = l.account_id`
}

// ledgerTotals: posted totals per account and currency of company cid between two dates (SQL arguments),
// joined to the account as "a" (see account_totals, migration 00023).
func ledgerTotals(cid, from, to string) string {
	return `account_totals(` + cid + `, ` + from + `::date, ` + to + `::date) l JOIN accounts a ON a.id = l.account_id`
}

type tbRow struct {
	AccountID string `json:"account_id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Debit     string `json:"debit"`
	Credit    string `json:"credit"`
	Balance   string `json:"balance"`
}

func dateRange(r *http.Request) (from, to string, ok bool) {
	q := r.URL.Query()
	from, to = q.Get("from"), q.Get("to")
	if to == "" {
		to = time.Now().Format(time.DateOnly)
	}
	if from == "" {
		from = "0001-01-01"
	}
	_, e1 := time.Parse(time.DateOnly, from)
	_, e2 := time.Parse(time.DateOnly, to)
	return from, to, e1 == nil && e2 == nil
}

// trialBalance: posted totals per account in the company base currency.
func (s *Server) trialBalance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid := r.PathValue("cid")
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	var base, totalDr, totalCr string
	var pres *presentation
	out := []tbRow{}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		var err error
		if base, err = companyBase(ctx, tx, cid); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT a.id::text, a.code, a.name, a.type,
			       sum(l.base_debit)::text, sum(l.base_credit)::text, (sum(l.base_debit) - sum(l.base_credit))::text
			FROM `+ledgerTotals("$1", "$2", "$3")+`
			GROUP BY a.id, a.code, a.name, a.type
			HAVING sum(l.base_debit) <> 0 OR sum(l.base_credit) <> 0
			ORDER BY a.code`, cid, from, to)
		if err != nil {
			return err
		}
		if out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[tbRow]); err != nil {
			return err
		}
		dr, cr := decimal.Zero, decimal.Zero
		for _, row := range out {
			d, _ := decimal.NewFromString(row.Debit)
			c, _ := decimal.NewFromString(row.Credit)
			dr, cr = dr.Add(d), cr.Add(c)
		}
		totalDr, totalCr = dr.StringFixed(4), cr.StringFixed(4)
		pres, err = presentationFor(ctx, tx, r, cid, base, to)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	res, err := pres.apply(map[string]any{
		"base_currency": base, "from": from, "to": to, "rows": out,
		"total_debit": totalDr, "total_credit": totalCr,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type ledgerRow struct {
	EntryID     string `json:"entry_id"`
	Number      int64  `json:"number"`
	Date        string `json:"date"`
	Memo        string `json:"memo"`
	Description string `json:"description"`
	Currency    string `json:"currency"`
	Debit       string `json:"debit"`
	Credit      string `json:"credit"`
	BaseDebit   string `json:"base_debit"`
	BaseCredit  string `json:"base_credit"`
	Balance     string `json:"balance"`
	Source      string `json:"source"`
	Reference   string `json:"reference"`
}

// accountLedger: posted movements of one account with running balance (base currency).
func (s *Server) accountLedger(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid, acc := r.PathValue("cid"), r.PathValue("account")
	if !isUUID(acc) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	var base, opening string
	out := []ledgerRow{}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		var err error
		if base, err = companyBase(ctx, tx, cid); err != nil {
			return err
		}
		var known bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $1 AND company_id = $2)`, acc, cid).Scan(&known); err != nil {
			return err
		}
		if !known {
			return notFound()
		}
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::text
			FROM account_totals($1, '0001-01-01'::date, $3::date - 1) l WHERE l.account_id = $2`, cid, acc, from).Scan(&opening); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT e.id::text, e.number, e.entry_date::text, e.memo, l.description, l.currency::text,
			       l.debit::text, l.credit::text, l.base_debit::text, l.base_credit::text,
			       ($4::numeric + sum(l.base_debit - l.base_credit) OVER (ORDER BY e.entry_date, e.number, l.line_no))::text,
			       e.source, e.reference
			FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
			WHERE e.company_id = $1 AND l.account_id = $2 AND e.entry_date BETWEEN $3::date AND $5::date
			ORDER BY e.entry_date, e.number, l.line_no
			LIMIT 5000`, cid, acc, from, opening, to)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[ledgerRow])
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"base_currency": base, "from": from, "to": to, "opening": opening, "rows": out})
}
