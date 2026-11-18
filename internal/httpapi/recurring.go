package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
)

// Recurring entries: journal templates or expenses created automatically on their due date.

const recurringCols = `t.id, t.name, t.kind, t.frequency, t.next_date, t.end_date, t.is_active, t.currency, t.amount::text AS amount,
	t.account_id, a.code AS account_code, t.method_id, m.name AS method_name, t.lines, t.memo, t.runs, t.last_run_at,
	t.last_entry_id, t.last_error, t.created_at`

const recurringFrom = ` FROM recurring_templates t LEFT JOIN accounts a ON a.id = t.account_id LEFT JOIN payment_methods m ON m.id = t.method_id`

type recLine struct {
	AccountID   string `json:"account_id"`
	Description string `json:"description"`
	Debit       string `json:"debit"`
	Credit      string `json:"credit"`
}

type recurringIn struct {
	Name      *string    `json:"name"`
	Kind      *string    `json:"kind"`
	Frequency *string    `json:"frequency"`
	NextDate  *string    `json:"next_date"`
	EndDate   *string    `json:"end_date"` // "" clears
	IsActive  *bool      `json:"is_active"`
	Currency  *string    `json:"currency"`
	Amount    *string    `json:"amount"`
	AccountID *string    `json:"account_id"`
	MethodID  *string    `json:"method_id"`
	Lines     *[]recLine `json:"lines"`
	Memo      *string    `json:"memo"`
}

var frequencies = map[string]bool{"monthly": true, "quarterly": true, "yearly": true}

func (s *Server) listRecurring(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+recurringCols+recurringFrom+` WHERE t.company_id = $1 ORDER BY t.is_active DESC, t.next_date`,
			r.PathValue("cid"))
	})
}

// validateRecLines checks a journal template is balanced and uses postable accounts of the company.
func validateRecLines(ctx context.Context, tx pgx.Tx, cid string, lines []recLine) error {
	if len(lines) < 2 || len(lines) > 50 {
		return badRequest("need_two_lines")
	}
	dr, cr := decimal.Zero, decimal.Zero
	for _, l := range lines {
		d, ok1 := parseAmount(l.Debit)
		c, ok2 := parseAmount(l.Credit)
		if !ok1 || !ok2 || d.IsZero() == c.IsZero() || !isUUID(l.AccountID) {
			return badRequest("invalid_amount")
		}
		if err := requirePostable(ctx, tx, cid, l.AccountID); err != nil {
			return err
		}
		dr, cr = dr.Add(d), cr.Add(c)
	}
	if !dr.Equal(cr) {
		return &apiError{status: http.StatusUnprocessableEntity, code: "unbalanced"}
	}
	return nil
}

func (s *Server) saveRecurring(w http.ResponseWriter, r *http.Request) {
	var in recurringIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (!create && !isUUID(id)) || (create && (in.Name == nil || in.Kind == nil || in.Frequency == nil || in.NextDate == nil || in.Currency == nil)) ||
		(in.Name != nil && strings.TrimSpace(*in.Name) == "") || (in.Kind != nil && *in.Kind != "journal" && *in.Kind != "expense") ||
		(in.Frequency != nil && !frequencies[*in.Frequency]) || (in.NextDate != nil && !validDate(*in.NextDate)) ||
		(in.EndDate != nil && *in.EndDate != "" && !validDate(*in.EndDate)) || (in.Currency != nil && !currencyRe.MatchString(*in.Currency)) ||
		!optUUID(in.AccountID) || !optUUID(in.MethodID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Amount != nil && *in.Amount != "" {
		if _, ok := money(*in.Amount); !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		var linesJSON []byte
		if in.Lines != nil {
			if err := validateRecLines(ctx, tx, cid, *in.Lines); err != nil {
				return nil, err
			}
			linesJSON, _ = json.Marshal(*in.Lines)
		}
		if in.AccountID != nil && *in.AccountID != "" {
			if err := requirePostable(ctx, tx, cid, *in.AccountID); err != nil {
				return nil, err
			}
		}
		if in.MethodID != nil && *in.MethodID != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payment_methods WHERE id = $1 AND company_id = $2)`, *in.MethodID, cid).Scan(&ok); err != nil || !ok {
				return nil, badRequest("invalid_tender")
			}
		}
		var err error
		if create {
			if *in.Kind == "journal" && in.Lines == nil {
				return nil, badRequest("need_two_lines")
			}
			if linesJSON == nil {
				linesJSON = []byte("[]")
			}
			err = tx.QueryRow(ctx, `INSERT INTO recurring_templates (company_id, name, kind, frequency, next_date, end_date, currency, amount,
				account_id, method_id, lines, memo) VALUES ($1, $2, $3, $4, $5::date, nullif($6, '')::date, $7, nullif($8, '')::numeric,
				nullif($9, '')::uuid, nullif($10, '')::uuid, $11, $12) RETURNING id::text`,
				cid, strings.TrimSpace(*in.Name), *in.Kind, *in.Frequency, *in.NextDate, deref(in.EndDate), *in.Currency, deref(in.Amount),
				deref(in.AccountID), deref(in.MethodID), linesJSON, deref(in.Memo)).Scan(&id)
		} else {
			var tag pgconn.CommandTag
			tag, err = tx.Exec(ctx, `UPDATE recurring_templates SET name = coalesce(trim($3), name), frequency = coalesce($4, frequency),
				next_date = coalesce($5::date, next_date), end_date = CASE WHEN $6::text IS NULL THEN end_date ELSE nullif($6, '')::date END,
				is_active = coalesce($7, is_active), currency = coalesce($8, currency), amount = coalesce(nullif($9, '')::numeric, amount),
				account_id = coalesce(nullif($10, '')::uuid, account_id), method_id = coalesce(nullif($11, '')::uuid, method_id),
				lines = coalesce($12::jsonb, lines), memo = coalesce($13, memo), last_error = ''
				WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.Frequency, in.NextDate, in.EndDate, in.IsActive, in.Currency,
				in.Amount, in.AccountID, in.MethodID, linesJSON, in.Memo)
			if err == nil && tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		if err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23514" {
				return nil, badRequest("bad_request")
			}
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+recurringCols+recurringFrom+` WHERE t.id = $1`, id)
	})
}

// runRecurringNow creates the next occurrence immediately (dated at its next date) and moves the schedule on.
func (s *Server) runRecurringNow(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		entry, err := runRecurring(ctx, tx, cid, id, true)
		if err != nil {
			return nil, err
		}
		out, err := queryJSONOne(ctx, tx, `SELECT `+recurringCols+recurringFrom+` WHERE t.id = $1`, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"template": out, "journal_entry_id": entry}, nil
	})
}

// runRecurring executes one occurrence. With force=false it only runs when the template is due.
// Returns the created journal entry id ("" when nothing was due).
func runRecurring(ctx context.Context, tx pgx.Tx, cid, id string, force bool) (string, error) {
	var t struct {
		name, kind, freq, next, cur, memo string
		end, amount, account, method      *string
		active                            bool
		lines                             []byte
	}
	err := tx.QueryRow(ctx, `SELECT name, kind, frequency, next_date::text, end_date::text, is_active, currency::text, amount::text,
		account_id::text, method_id::text, lines, memo FROM recurring_templates WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
		Scan(&t.name, &t.kind, &t.freq, &t.next, &t.end, &t.active, &t.cur, &t.amount, &t.account, &t.method, &t.lines, &t.memo)
	if err == pgx.ErrNoRows {
		return "", notFound()
	}
	if err != nil {
		return "", err
	}
	if !t.active {
		return "", &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	if !force && t.next > today() {
		return "", nil
	}
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return "", err
	}
	memo := strings.TrimSpace(t.memo)
	if memo == "" {
		memo = t.name
	}
	memo = fmt.Sprintf("%s · %s", memo, t.next)
	var je string
	switch t.kind {
	case "journal":
		var lines []recLine
		if err := json.Unmarshal(t.lines, &lines); err != nil || len(lines) < 2 {
			return "", badRequest("need_two_lines")
		}
		jl := make([]jline, 0, len(lines))
		rate, err := resolveRate(ctx, tx, cid, set.Base, t.cur, t.next, "")
		if err != nil {
			return "", err
		}
		for _, l := range lines {
			d, _ := parseAmount(l.Debit)
			c, _ := parseAmount(l.Credit)
			jl = append(jl, jline{Account: l.AccountID, Desc: l.Description, Currency: t.cur, Debit: d, Credit: c, Rate: rate})
		}
		if je, err = postJournal(ctx, tx, cid, set, t.next, memo, "REC", "recurring", jl); err != nil {
			return "", err
		}
	case "expense":
		if t.amount == nil || t.account == nil || t.method == nil {
			return "", badRequest("bad_request")
		}
		methods, err := loadMethods(ctx, tx, cid)
		if err != nil {
			return "", err
		}
		m, ok := methods[*t.method]
		if !ok || !m.Active {
			return "", badRequest("invalid_tender")
		}
		amt, _ := decimal.NewFromString(*t.amount)
		rate, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, t.next, "")
		if err != nil {
			return "", err
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'expense')`, cid).Scan(&number); err != nil {
			return "", err
		}
		if je, err = postJournal(ctx, tx, cid, set, t.next, fmt.Sprintf("Expense #%d · %s", number, memo), fmt.Sprintf("EXP-%d", number), "expense",
			[]jline{
				{Account: *t.account, Desc: memo, Currency: m.Currency, Debit: amt, Rate: rate},
				{Account: m.Account, Desc: m.Name, Currency: m.Currency, Credit: amt, Rate: rate},
			}); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO expenses (company_id, number, expense_date, account_id, method_id, currency, amount, description, journal_entry_id)
			VALUES ($1, $2, $3::date, $4, $5, $6, $7::numeric, $8, $9)`, cid, number, t.next, *t.account, m.ID, m.Currency, amt.String(), memo, je); err != nil {
			return "", err
		}
	}
	nextT, _ := time.Parse(time.DateOnly, t.next)
	months := map[string]int{"monthly": 1, "quarterly": 3, "yearly": 12}[t.freq]
	next := addMonths(nextT, months, nextT.Day()).Format(time.DateOnly)
	active := t.end == nil || next <= *t.end
	if _, err := tx.Exec(ctx, `UPDATE recurring_templates SET next_date = $2::date, is_active = $3, runs = runs + 1, last_run_at = now(),
		last_entry_id = $4, last_error = '' WHERE id = $1`, id, next, active, je); err != nil {
		return "", err
	}
	return je, nil
}

// StartRecurringWorker posts due recurring entries for all tenants (hourly). It reads due templates
// with the owner pool, then runs each occurrence as the restricted app role inside the template's tenant,
// so row-level security and all accounting rules (including the period lock) apply.
func (s *Server) StartRecurringWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	go func() {
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if n, err := s.RunDueRecurring(ctx); err != nil {
					slog.Error("recurring worker", "err", err)
				} else if n > 0 {
					slog.Info("recurring entries posted", "count", n)
				}
				timer.Reset(time.Hour)
			}
		}
	}()
}

// RunDueRecurring posts every due occurrence (catching up missed ones, max 24 per template). Returns how many were posted.
func (s *Server) RunDueRecurring(ctx context.Context) (int, error) {
	type due struct{ id, tenant, company, user string }
	rows, err := s.OwnerPool.Query(ctx, `SELECT id::text, tenant_id::text, company_id::text, coalesce(created_by::text, '')
		FROM recurring_templates WHERE is_active AND next_date <= current_date AND last_error = '' ORDER BY next_date LIMIT 500`)
	if err != nil {
		return 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		err := row.Scan(&d.id, &d.tenant, &d.company, &d.user)
		return d, err
	})
	if err != nil {
		return 0, err
	}
	posted := 0
	asApp := func(d due, fn func(tx pgx.Tx) error) error {
		return pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true), set_config('app.user_id', $2, true)`, d.tenant, d.user); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
				return err
			}
			return fn(tx)
		})
	}
	for _, d := range list {
		for i := 0; i < 24; i++ {
			var je string
			err := asApp(d, func(tx pgx.Tx) error {
				var err error
				je, err = runRecurring(ctx, tx, d.company, d.id, false)
				return err
			})
			if err != nil {
				code := err.Error()
				var pg *pgconn.PgError
				if errors.As(err, &pg) {
					code = map[string]string{"PXLCK": "period_locked", "PXUNB": "unbalanced", "PXACC": "invalid_account"}[pg.Code]
					if code == "" {
						code = pg.Message
					}
				}
				var ae *apiError
				if errors.As(err, &ae) {
					code = ae.code
				}
				if len(code) > 200 {
					code = code[:200]
				}
				_ = asApp(d, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE recurring_templates SET last_error = $2 WHERE id = $1`, d.id, code)
					return err
				})
				break
			}
			if je == "" {
				break
			}
			posted++
		}
	}
	return posted, nil
}
