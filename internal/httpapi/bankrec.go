package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Bank statements and reconciliation. Statement lines are matched to posted journal lines on the
// bank's ledger account (same currency, same signed amount). Journal lines stay immutable; the
// match is recorded on the statement line (a journal line can be matched only once).

func loadBankMethod(ctx context.Context, tx pgx.Tx, cid, methodID string) (*method, error) {
	if !isUUID(methodID) {
		return nil, badRequest("invalid_tender")
	}
	methods, err := loadMethods(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	m, ok := methods[methodID]
	if !ok {
		return nil, badRequest("invalid_tender")
	}
	return &m, nil
}

// importStatement stores a bank statement (lines: date, description, amount signed, reference).
func (s *Server) importStatement(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	var in struct {
		MethodID string         `json:"method_id"`
		Name     string         `json:"name"`
		Format   string         `json:"format"` // phase 9: csv | ofx | mt940 | camt (lines parsed from a bank file) | manual
		Lines    []stmtFileLine `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Format == "" {
		in.Format = "manual"
	}
	if len(in.Lines) == 0 || len(in.Lines) > 5000 || !contains([]string{"manual", "csv", "ofx", "mt940", "camt"}, in.Format) || len(in.Name) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	type parsed struct {
		date, desc, ref, partner, fp string
		amount                       decimal.Decimal
	}
	for i := range in.Lines {
		in.Lines[i].Fingerprint = "" // computed here, never trusted from the client
		in.Lines[i].Amount = strings.ReplaceAll(strings.TrimSpace(in.Lines[i].Amount), ",", "")
		in.Lines[i].Date = strings.TrimSpace(in.Lines[i].Date)
		if len(in.Lines[i].BankID) > 100 || len(in.Lines[i].Description) > 2000 || len(in.Lines[i].Reference) > 200 || len(in.Lines[i].Partner) > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request", "detail": i + 1})
			return
		}
	}
	fingerprints(in.Lines)
	lines := make([]parsed, 0, len(in.Lines))
	for i, l := range in.Lines {
		a, err := decimal.NewFromString(l.Amount)
		if err != nil || a.IsZero() || a.Exponent() < -4 || !validDate(l.Date) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request", "detail": i + 1})
			return
		}
		lines = append(lines, parsed{l.Date, strings.TrimSpace(l.Description), strings.TrimSpace(l.Reference), strings.TrimSpace(l.Partner), l.Fingerprint, a})
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		m, err := loadBankMethod(ctx, tx, cid, in.MethodID)
		if err != nil {
			return nil, err
		}
		// one import of a bank box at a time (two people importing the same file at once)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('bankimport:' || $1))`, m.ID); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO bank_statements (company_id, method_id, name, format) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			cid, m.ID, strings.TrimSpace(in.Name), in.Format).Scan(&id); err != nil {
			return nil, err
		}
		skipped := 0
		for _, l := range lines {
			tag, err := tx.Exec(ctx, `INSERT INTO bank_statement_lines (statement_id, method_id, line_date, description, reference, amount, partner, fingerprint)
				VALUES ($1, $2, $3::date, $4, $5, $6::numeric, $7, $8) ON CONFLICT (method_id, fingerprint) WHERE fingerprint IS NOT NULL DO NOTHING`,
				id, m.ID, l.date, l.desc, l.ref, l.amount.String(), l.partner, l.fp)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				skipped++ // already imported (same bank id, or same date / amount / text)
			}
		}
		if skipped == len(lines) {
			return nil, &apiError{status: http.StatusConflict, code: "already_imported", detail: strconv.Itoa(skipped)}
		}
		if _, err := tx.Exec(ctx, `UPDATE bank_statements SET skipped = $2 WHERE id = $1`, id, skipped); err != nil {
			return nil, err
		}
		return loadStatement(ctx, tx, cid, id)
	})
}

func (s *Server) listStatements(w http.ResponseWriter, r *http.Request) {
	methodID := r.URL.Query().Get("method_id")
	if methodID != "" && !isUUID(methodID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT s.id, s.name, s.method_id, m.name AS method, m.currency, s.created_at, s.format, s.skipped,
			(SELECT count(*) FROM bank_statement_lines l WHERE l.statement_id = s.id) AS lines,
			(SELECT count(*) FROM bank_statement_lines l WHERE l.statement_id = s.id AND l.status = 'unmatched') AS unmatched,
			(SELECT min(line_date) FROM bank_statement_lines l WHERE l.statement_id = s.id) AS date_from,
			(SELECT max(line_date) FROM bank_statement_lines l WHERE l.statement_id = s.id) AS date_to,
			coalesce((SELECT sum(amount) FROM bank_statement_lines l WHERE l.statement_id = s.id), 0)::text AS total
			FROM bank_statements s JOIN payment_methods m ON m.id = s.method_id
			WHERE s.company_id = $1 AND ($2 = '' OR s.method_id = $2::uuid) ORDER BY s.created_at DESC LIMIT 200`, r.PathValue("cid"), methodID)
	})
}

// candidatesSQL: unmatched posted journal lines on the bank account with the same signed amount, within ±3 days.
const candidatesSQL = `
	SELECT jl.id, e.number, e.entry_date, e.memo, jl.description, (jl.debit - jl.credit)::text AS amount
	FROM journal_lines jl
	JOIN journal_entries e ON e.id = jl.entry_id AND e.status = 'posted'
	WHERE jl.account_id = m.account_id AND jl.currency = m.currency
	  AND (jl.debit - jl.credit) = l.amount
	  AND e.entry_date BETWEEN l.line_date - 3 AND l.line_date + 3
	  AND NOT EXISTS (SELECT 1 FROM bank_statement_lines x WHERE x.journal_line_id = jl.id)
	ORDER BY abs(e.entry_date - l.line_date), e.number LIMIT 5`

func loadStatement(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	st, err := queryJSONOne(ctx, tx, `SELECT s.id, s.name, s.method_id, m.name AS method, m.currency, s.created_at, s.format, s.skipped
		FROM bank_statements s JOIN payment_methods m ON m.id = s.method_id WHERE s.id = $1 AND s.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `
		SELECT l.id, l.line_date, l.description, l.reference, l.partner, l.amount::text AS amount, l.status, l.journal_line_id, l.payment_id, l.supplier_payment_id,
		       (SELECT name FROM bank_rules br WHERE br.id = l.rule_id) AS rule,
		       me.number AS matched_number, me.memo AS matched_memo, l.created_entry_id,
		       CASE WHEN l.status = 'unmatched' THEN coalesce((SELECT json_agg(c) FROM (`+candidatesSQL+`) c), '[]'::json) ELSE '[]'::json END AS suggestions
		FROM bank_statement_lines l
		JOIN payment_methods m ON m.id = l.method_id
		LEFT JOIN journal_lines mj ON mj.id = l.journal_line_id
		LEFT JOIN journal_entries me ON me.id = mj.entry_id
		WHERE l.statement_id = $1 ORDER BY l.line_date, l.amount`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"statement": st, "lines": lines}, nil
}

func (s *Server) getStatement(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadStatement(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// autoMatch matches every unmatched statement line that has exactly one candidate.
func (s *Server) autoMatchStatement(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bank_statements WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, notFound()
		}
		rows, err := tx.Query(ctx, `SELECT l.id::text, (SELECT array_agg(c.id::text) FROM (`+candidatesSQL+`) c)
			FROM bank_statement_lines l JOIN payment_methods m ON m.id = l.method_id
			WHERE l.statement_id = $1 AND l.status = 'unmatched' ORDER BY l.line_date`, id)
		if err != nil {
			return nil, err
		}
		type cand struct {
			line string
			ids  []string
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (cand, error) {
			var c cand
			err := row.Scan(&c.line, &c.ids)
			return c, err
		})
		if err != nil {
			return nil, err
		}
		used := map[string]bool{}
		matched := 0
		for _, c := range list {
			if len(c.ids) != 1 || used[c.ids[0]] {
				continue
			}
			used[c.ids[0]] = true
			if _, err := tx.Exec(ctx, `UPDATE bank_statement_lines SET journal_line_id = $2, status = 'matched', matched_by = app_user(), matched_at = now()
				WHERE id = $1 AND status = 'unmatched'`, c.line, c.ids[0]); err != nil {
				return nil, err
			}
			matched++
		}
		st, err := loadStatement(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"matched": matched, "statement": st}, nil
	})
}

func lockBankLine(ctx context.Context, tx pgx.Tx, cid, id string) (statement, status string, amount decimal.Decimal, m *method, date string, err error) {
	if !isUUID(id) {
		err = notFound()
		return
	}
	var methodID, amt string
	err = tx.QueryRow(ctx, `SELECT l.statement_id::text, l.status, l.amount::text, l.method_id::text, l.line_date::text
		FROM bank_statement_lines l JOIN bank_statements s ON s.id = l.statement_id
		WHERE l.id = $1 AND s.company_id = $2 FOR UPDATE OF l`, id, cid).Scan(&statement, &status, &amt, &methodID, &date)
	if err == pgx.ErrNoRows {
		err = notFound()
		return
	}
	if err != nil {
		return
	}
	amount, _ = decimal.NewFromString(amt)
	m, err = loadBankMethod(ctx, tx, cid, methodID)
	return
}

// matchLine links a statement line to a journal line (validated: same bank account, currency and amount; not matched elsewhere).
func (s *Server) matchBankLine(w http.ResponseWriter, r *http.Request) {
	var in struct {
		JournalLineID string `json:"journal_line_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(in.JournalLineID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		statement, status, amount, m, _, err := lockBankLine(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if status != "unmatched" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM journal_lines jl JOIN journal_entries e ON e.id = jl.entry_id AND e.status = 'posted'
			WHERE jl.id = $1 AND e.company_id = $2 AND jl.account_id = $3 AND jl.currency = $4 AND (jl.debit - jl.credit) = $5::numeric)`,
			in.JournalLineID, cid, m.Account, m.Currency, amount.String()).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, badRequest("match_mismatch")
		}
		_, err = tx.Exec(ctx, `UPDATE bank_statement_lines SET journal_line_id = $2, status = 'matched', matched_by = app_user(), matched_at = now()
			WHERE id = $1`, id, in.JournalLineID)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "already_matched"}
		}
		if err != nil {
			return nil, err
		}
		return loadStatement(ctx, tx, cid, statement)
	})
}

func (s *Server) setBankLineStatus(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action") // unmatch | ignore | unignore
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		statement, status, _, _, _, err := lockBankLine(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		switch {
		case action == "unmatch" && status == "matched":
			_, err = tx.Exec(ctx, `UPDATE bank_statement_lines SET journal_line_id = NULL, status = 'unmatched', matched_by = NULL, matched_at = NULL WHERE id = $1`, id)
		case action == "ignore" && status == "unmatched":
			_, err = tx.Exec(ctx, `UPDATE bank_statement_lines SET status = 'ignored' WHERE id = $1`, id)
		case action == "unignore" && status == "ignored":
			_, err = tx.Exec(ctx, `UPDATE bank_statement_lines SET status = 'unmatched' WHERE id = $1`, id)
		default:
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if err != nil {
			return nil, err
		}
		return loadStatement(ctx, tx, cid, statement)
	})
}

// createBankLineEntry books an unmatched statement line (bank fee, interest, transfer…) against an account and matches it.
func (s *Server) createBankLineEntry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountID   string `json:"account_id"`
		Description string `json:"description"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(in.AccountID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		statement, status, amount, m, date, err := lockBankLine(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if status != "unmatched" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if err := requirePostable(ctx, tx, cid, in.AccountID); err != nil {
			return nil, err
		}
		if in.AccountID == m.Account {
			return nil, badRequest("invalid_account")
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rate, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, date, "")
		if err != nil {
			return nil, err
		}
		desc := strings.TrimSpace(in.Description)
		if desc == "" {
			desc = "Bank"
		}
		abs := amount.Abs()
		bank := jline{Account: m.Account, Desc: desc, Currency: m.Currency, Rate: rate}
		other := jline{Account: in.AccountID, Desc: desc, Currency: m.Currency, Rate: rate}
		if amount.IsPositive() {
			bank.Debit, other.Credit = abs, abs
		} else {
			bank.Credit, other.Debit = abs, abs
		}
		je, err := postJournal(ctx, tx, cid, set, date, "Bank · "+desc, "BANK", "bank", []jline{bank, other})
		if err != nil {
			return nil, err
		}
		var jlID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM journal_lines WHERE entry_id = $1 AND account_id = $2 AND currency = $3
			AND (debit - credit) = $4::numeric LIMIT 1`, je, m.Account, m.Currency, amount.String()).Scan(&jlID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE bank_statement_lines SET journal_line_id = $2, status = 'matched', created_entry_id = $3,
			matched_by = app_user(), matched_at = now() WHERE id = $1`, id, jlID, je); err != nil {
			return nil, err
		}
		return loadStatement(ctx, tx, cid, statement)
	})
}

// reconciliation: book balance vs statement balance for a bank method, with the items explaining the difference.
func (s *Server) bankReconciliation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	methodID, to := q.Get("method_id"), q.Get("to")
	if to == "" {
		to = today()
	}
	if !validDate(to) {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		m, err := loadBankMethod(ctx, tx, cid, methodID)
		if err != nil {
			return nil, err
		}
		var book, stmt, matched string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(jl.debit - jl.credit), 0)::text FROM journal_lines jl
			JOIN journal_entries e ON e.id = jl.entry_id AND e.status = 'posted'
			WHERE jl.account_id = $1 AND jl.currency = $2 AND e.entry_date <= $3::date`, m.Account, m.Currency, to).Scan(&book); err != nil {
			return nil, err
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text, coalesce(sum(amount) FILTER (WHERE status = 'matched'), 0)::text
			FROM bank_statement_lines WHERE method_id = $1 AND line_date <= $2::date AND status <> 'ignored'`, m.ID, to).Scan(&stmt, &matched); err != nil {
			return nil, err
		}
		unmatchedStmt, err := queryJSON(ctx, tx, `SELECT l.id, l.statement_id, l.line_date, l.description, l.reference, l.amount::text AS amount
			FROM bank_statement_lines l WHERE l.method_id = $1 AND l.line_date <= $2::date AND l.status = 'unmatched'
			ORDER BY l.line_date LIMIT 500`, m.ID, to)
		if err != nil {
			return nil, err
		}
		unmatchedBook, err := queryJSON(ctx, tx, `SELECT jl.id, e.number, e.entry_date, e.memo, (jl.debit - jl.credit)::text AS amount
			FROM journal_lines jl JOIN journal_entries e ON e.id = jl.entry_id AND e.status = 'posted'
			WHERE jl.account_id = $1 AND jl.currency = $2 AND e.entry_date <= $3::date
			  AND NOT EXISTS (SELECT 1 FROM bank_statement_lines x WHERE x.journal_line_id = jl.id)
			ORDER BY e.entry_date LIMIT 500`, m.Account, m.Currency, to)
		if err != nil {
			return nil, err
		}
		b, _ := decimal.NewFromString(book)
		st, _ := decimal.NewFromString(stmt)
		return map[string]any{"method": m.Name, "currency": m.Currency, "to": to, "book_balance": book, "statement_balance": stmt,
			"matched_total": matched, "difference": b.Sub(st).StringFixed(4),
			"unmatched_statement": unmatchedStmt, "unreconciled_book": unmatchedBook}, nil
	})
}
