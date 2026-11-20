package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

type entrySummary struct {
	ID         string    `json:"id"`
	Number     *int64    `json:"number"`
	Date       string    `json:"date"`
	Memo       string    `json:"memo"`
	Reference  string    `json:"reference"`
	Status     string    `json:"status"`
	Source     string    `json:"source"`
	ReversalOf *string   `json:"reversal_of"`
	ReversedBy *string   `json:"reversed_by"`
	Total      string    `json:"total"`
	CreatedAt  time.Time `json:"created_at"`
}

type entryLine struct {
	ID          string  `json:"id"`
	LineNo      int     `json:"line_no"`
	AccountID   string  `json:"account_id"`
	AccountCode string  `json:"account_code"`
	AccountName string  `json:"account_name"`
	Description string  `json:"description"`
	Currency    string  `json:"currency"`
	Debit       string  `json:"debit"`
	Credit      string  `json:"credit"`
	Rate        string  `json:"rate"`
	BaseDebit   string  `json:"base_debit"`
	BaseCredit  string  `json:"base_credit"`
	CostCenter  *string `json:"cost_center_id"`
	CCName      *string `json:"cost_center_name"`
}

type entryDetail struct {
	entrySummary
	BaseCurrency string      `json:"base_currency"`
	CreatedBy    *string     `json:"created_by"`
	PostedBy     *string     `json:"posted_by"`
	PostedAt     *time.Time  `json:"posted_at"`
	Lines        []entryLine `json:"lines"`
	// ControlAccounts: the entry touches receivables, payables, stock or gift/loyalty liabilities directly, so
	// customer / supplier / stock balances will no longer match the ledger (shown as a warning; audit C-M4)
	ControlAccounts []string `json:"control_accounts,omitempty"`
}

// controlAccountsIn lists the codes of the control accounts used by a manual entry's lines. These accounts carry
// sub-ledgers (customer and supplier balances, stock value, gift cards, loyalty points) that only documents update.
func controlAccountsIn(ctx context.Context, tx pgx.Tx, cid, entryID string) ([]string, error) {
	var out []string
	rows, err := tx.Query(ctx, `
		WITH ctl AS (
			SELECT unnest(ARRAY[ar_account_id, ap_account_id, inventory_account_id, gift_liability_account_id, loyalty_liability_account_id]) AS id
			FROM companies WHERE id = $1
			UNION SELECT DISTINCT inventory_account_id FROM items WHERE company_id = $1 AND inventory_account_id IS NOT NULL)
		SELECT DISTINCT a.code FROM journal_lines l JOIN accounts a ON a.id = l.account_id
		WHERE l.entry_id = $2 AND l.account_id IN (SELECT id FROM ctl WHERE id IS NOT NULL) ORDER BY 1`, cid, entryID)
	if err != nil {
		return nil, err
	}
	out, err = pgx.CollectRows(rows, pgx.RowTo[string])
	return out, err
}

// guardControlAccounts: data-entry staff may not post manual entries to control accounts; an accountant
// (accounting.manage) may, and sees a warning.
func guardControlAccounts(ctx context.Context, tx pgx.Tx, r *http.Request, cid, entryID string) ([]string, error) {
	codes, err := controlAccountsIn(ctx, tx, cid, entryID)
	if err != nil || len(codes) == 0 {
		return nil, err
	}
	if !can(r, "accounting.manage") {
		return nil, &apiError{status: http.StatusForbidden, code: "control_account", detail: strings.Join(codes, ", ")}
	}
	return codes, nil
}

const entrySummaryCols = `e.id::text, e.number, e.entry_date::text, e.memo, e.reference, e.status, e.source,
	e.reversal_of::text, e.reversed_by::text,
	coalesce((SELECT sum(l.base_debit) FROM journal_lines l WHERE l.entry_id = e.id), 0)::text, e.created_at`

func (s *Server) listEntries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid := r.PathValue("cid")
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	status := q.Get("status")
	from, to := q.Get("from"), q.Get("to")
	if from == "" {
		from = "0001-01-01"
	}
	if to == "" {
		to = "9999-12-31"
	}
	if !validDate(from) || !validDate(to) || (status != "" && status != "draft" && status != "posted") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	out := []entrySummary{}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return err
		}
		// drafts first (newest first), then posted entries newest first; each part read through its own
		// index so a company with a million entries still answers at once
		if status == "" || status == "draft" {
			rows, err := tx.Query(ctx, `SELECT `+entrySummaryCols+` FROM journal_entries e
				WHERE e.company_id = $1 AND e.status = 'draft' AND e.entry_date BETWEEN $2::date AND $3::date
				ORDER BY e.entry_date DESC, e.created_at DESC LIMIT $4`, cid, from, to, offset+limit)
			if err != nil {
				return err
			}
			drafts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[entrySummary])
			if err != nil {
				return err
			}
			if offset < len(drafts) {
				out = append(out, drafts[offset:]...)
				offset = 0
			} else {
				offset -= len(drafts)
			}
		}
		if (status == "" || status == "posted") && len(out) < limit {
			rows, err := tx.Query(ctx, `SELECT `+entrySummaryCols+` FROM journal_entries e
				WHERE e.company_id = $1 AND e.status = 'posted' AND e.entry_date BETWEEN $2::date AND $3::date
				ORDER BY e.entry_date DESC, e.number DESC LIMIT $4 OFFSET $5`, cid, from, to, limit-len(out), offset)
			if err != nil {
				return err
			}
			posted, err := pgx.CollectRows(rows, pgx.RowToStructByPos[entrySummary])
			if err != nil {
				return err
			}
			out = append(out, posted...)
		}
		if len(out) > limit {
			out = out[:limit]
		}
		return nil
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func loadEntry(ctx context.Context, tx pgx.Tx, cid, id string) (*entryDetail, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	base, err := companyBase(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	e := &entryDetail{BaseCurrency: base}
	err = tx.QueryRow(ctx, `SELECT `+entrySummaryCols+`, cu.full_name, pu.full_name, e.posted_at
		FROM journal_entries e
		LEFT JOIN users cu ON cu.id = e.created_by
		LEFT JOIN users pu ON pu.id = e.posted_by
		WHERE e.id = $1 AND e.company_id = $2`, id, cid).Scan(
		&e.ID, &e.Number, &e.Date, &e.Memo, &e.Reference, &e.Status, &e.Source, &e.ReversalOf, &e.ReversedBy,
		&e.Total, &e.CreatedAt, &e.CreatedBy, &e.PostedBy, &e.PostedAt)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT l.id::text, l.line_no, l.account_id::text, a.code, a.name, l.description, l.currency::text,
		       l.debit::text, l.credit::text, trim_scale(l.rate)::text, l.base_debit::text, l.base_credit::text,
		       l.cost_center_id::text, cc.code || ' · ' || cc.name
		FROM journal_lines l JOIN accounts a ON a.id = l.account_id LEFT JOIN cost_centers cc ON cc.id = l.cost_center_id
		WHERE l.entry_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	e.Lines, err = pgx.CollectRows(rows, pgx.RowToStructByPos[entryLine])
	return e, err
}

func (s *Server) getEntry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var e *entryDetail
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		var err error
		e, err = loadEntry(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

type lineIn struct {
	AccountID   string `json:"account_id"`
	Description string `json:"description"`
	Currency    string `json:"currency"`
	Debit       string `json:"debit"`
	Credit      string `json:"credit"`
	Rate        string `json:"rate"`
	CostCenter  string `json:"cost_center_id"`
}

type entryIn struct {
	Date      string   `json:"date"`
	Memo      string   `json:"memo"`
	Reference string   `json:"reference"`
	Lines     []lineIn `json:"lines"`
	Post      bool     `json:"post"`
}

type parsedLine struct {
	lineIn
	debit, credit decimal.Decimal
	rate          *decimal.Decimal
}

func parseAmount(s string) (decimal.Decimal, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Zero, true
	}
	d, err := decimal.NewFromString(s)
	if err != nil || d.IsNegative() || d.Exponent() < -4 {
		return decimal.Zero, false
	}
	return d, true
}

func (in *entryIn) parse() ([]parsedLine, error) {
	if _, err := time.Parse(time.DateOnly, in.Date); err != nil {
		return nil, badRequest("invalid_date")
	}
	if len(in.Lines) < 2 || len(in.Lines) > 500 {
		return nil, badRequest("need_two_lines")
	}
	out := make([]parsedLine, 0, len(in.Lines))
	for _, l := range in.Lines {
		if !isUUID(l.AccountID) || (l.CostCenter != "" && !isUUID(l.CostCenter)) {
			return nil, badRequest("invalid_account")
		}
		dr, ok1 := parseAmount(l.Debit)
		cr, ok2 := parseAmount(l.Credit)
		if !ok1 || !ok2 || dr.IsZero() == cr.IsZero() {
			return nil, badRequest("invalid_amount")
		}
		l.Currency = strings.ToUpper(strings.TrimSpace(l.Currency))
		if l.Currency != "" && !currencyRe.MatchString(l.Currency) {
			return nil, badRequest("bad_request")
		}
		p := parsedLine{lineIn: l, debit: dr, credit: cr}
		if strings.TrimSpace(l.Rate) != "" {
			rt, err := decimal.NewFromString(strings.TrimSpace(l.Rate))
			if err != nil || !rt.IsPositive() {
				return nil, badRequest("invalid_rate")
			}
			p.rate = &rt
		}
		out = append(out, p)
	}
	return out, nil
}

// writeLines inserts the lines of a draft, resolving exchange rates when not given.
func writeLines(ctx context.Context, tx pgx.Tx, cid, base, entryID, date string, lines []parsedLine) error {
	for i, l := range lines {
		cur := l.Currency
		if cur == "" {
			cur = base
		}
		rate := "1"
		switch {
		case cur == base:
		case l.rate != nil:
			rate = l.rate.String()
		default:
			err := tx.QueryRow(ctx, `SELECT rate::text FROM exchange_rates
				WHERE company_id = $1 AND currency = $2 AND kind = 'official' AND rate_date <= $3::date
				ORDER BY rate_date DESC LIMIT 1`, cid, cur, date).Scan(&rate)
			if err == pgx.ErrNoRows {
				return &apiError{status: http.StatusUnprocessableEntity, code: "no_rate", detail: cur}
			}
			if err != nil {
				return err
			}
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $1 AND company_id = $2)`,
			l.AccountID, cid).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return badRequest("invalid_account")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO journal_lines (entry_id, line_no, account_id, description, currency, debit, credit, rate, cost_center_id)
			VALUES ($1, $2, $3, $4, $5, $6::numeric, $7::numeric, $8::numeric, nullif($9, '')::uuid)`,
			entryID, i+1, l.AccountID, strings.TrimSpace(l.Description), cur, l.debit.String(), l.credit.String(), rate, l.CostCenter); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) createEntry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid := r.PathValue("cid")
	var in entryIn
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	lines, err := in.parse()
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.Post && !canPost(r) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	var e *entryDetail
	err = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO journal_entries (company_id, entry_date, memo, reference)
			VALUES ($1, $2::date, $3, $4) RETURNING id::text`,
			cid, in.Date, strings.TrimSpace(in.Memo), strings.TrimSpace(in.Reference)).Scan(&id); err != nil {
			return err
		}
		if err := writeLines(ctx, tx, cid, base, id, in.Date, lines); err != nil {
			return err
		}
		ctl, err := guardControlAccounts(ctx, tx, r, cid, id)
		if err != nil {
			return err
		}
		if in.Post {
			if _, err := tx.Exec(ctx, `SELECT post_journal_entry($1)`, id); err != nil {
				return err
			}
		}
		e, err = loadEntry(ctx, tx, cid, id)
		if e != nil {
			e.ControlAccounts = ctl
		}
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

// replaceEntry rewrites a draft (header and all lines). Posted entries are refused by the database.
func (s *Server) replaceEntry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid, id := r.PathValue("cid"), r.PathValue("id")
	var in entryIn
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	lines, err := in.parse()
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.Post && !canPost(r) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	var e *entryDetail
	err = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		cur, err := loadEntry(ctx, tx, cid, id)
		if err != nil {
			return err
		}
		if cur.Status != "draft" {
			return &apiError{status: http.StatusConflict, code: "posted_locked"}
		}
		if _, err := tx.Exec(ctx, `UPDATE journal_entries SET entry_date = $2::date, memo = $3, reference = $4 WHERE id = $1`,
			id, in.Date, strings.TrimSpace(in.Memo), strings.TrimSpace(in.Reference)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM journal_lines WHERE entry_id = $1`, id); err != nil {
			return err
		}
		if err := writeLines(ctx, tx, cid, cur.BaseCurrency, id, in.Date, lines); err != nil {
			return err
		}
		ctl, err := guardControlAccounts(ctx, tx, r, cid, id)
		if err != nil {
			return err
		}
		if in.Post {
			if _, err := tx.Exec(ctx, `SELECT post_journal_entry($1)`, id); err != nil {
				return err
			}
		}
		e, err = loadEntry(ctx, tx, cid, id)
		if e != nil {
			e.ControlAccounts = ctl
		}
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) deleteEntry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid, id := r.PathValue("cid"), r.PathValue("id")
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		cur, err := loadEntry(ctx, tx, cid, id)
		if err != nil {
			return err
		}
		if cur.Status != "draft" {
			return &apiError{status: http.StatusConflict, code: "posted_locked"}
		}
		_, err = tx.Exec(ctx, `DELETE FROM journal_entries WHERE id = $1`, id)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) postEntry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid, id := r.PathValue("cid"), r.PathValue("id")
	var e *entryDetail
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		cur, err := loadEntry(ctx, tx, cid, id)
		if err != nil {
			return err
		}
		var ctl []string
		if cur.Source == "manual" {
			if ctl, err = guardControlAccounts(ctx, tx, r, cid, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `SELECT post_journal_entry($1)`, id); err != nil {
			return err
		}
		e, err = loadEntry(ctx, tx, cid, id)
		if e != nil {
			e.ControlAccounts = ctl
		}
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) reverseEntry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid, id := r.PathValue("cid"), r.PathValue("id")
	var in struct {
		Date string `json:"date"`
		Memo string `json:"memo"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Date != "" {
		if _, err := time.Parse(time.DateOnly, in.Date); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_date")
			return
		}
	}
	var e *entryDetail
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if _, err := loadEntry(ctx, tx, cid, id); err != nil {
			return err
		}
		var newID string
		if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, nullif($2, '')::date, $3)::text`,
			id, in.Date, strings.TrimSpace(in.Memo)).Scan(&newID); err != nil {
			return err
		}
		var err error
		e, err = loadEntry(ctx, tx, cid, newID)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func canPost(r *http.Request) bool { return can(r, "accounting.edit") }
