package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Deferred revenue and prepaid expenses. A yearly subscription paid up front (or a yearly insurance / rent paid
// in advance) is income / expense of 12 months, not of the month it was invoiced. A schedule moves the amount to
// "deferred revenue" (a liability) or "prepaid expenses" (an asset) and recognises one month at a time.

const deferralCols = `d.id, d.number, d.kind, d.name, d.source_type, d.source_id, d.deferral_account_id, da.code AS deferral_account_code,
	da.name AS deferral_account_name, d.target_account_id, ta.code AS target_account_code, ta.name AS target_account_name,
	d.amount_base::text AS amount, d.months, d.start_period, d.cost_center_id, d.status, d.setup_entry_id, d.notes, d.created_at,
	coalesce((SELECT sum(l.amount) FROM deferral_lines l WHERE l.schedule_id = d.id), 0)::text AS recognised,
	(d.amount_base - coalesce((SELECT sum(l.amount) FROM deferral_lines l WHERE l.schedule_id = d.id), 0))::text AS remaining,
	(SELECT count(*) FROM deferral_lines l WHERE l.schedule_id = d.id) AS months_done,
	(SELECT max(l.period) FROM deferral_lines l WHERE l.schedule_id = d.id) AS last_period`

const deferralFrom = ` FROM deferral_schedules d JOIN accounts da ON da.id = d.deferral_account_id JOIN accounts ta ON ta.id = d.target_account_id`

func (s *Server) listDeferrals(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT `+deferralCols+deferralFrom+` WHERE d.company_id = $1 AND ($2 = '' OR d.status = $2)
			ORDER BY d.status, d.number DESC`, cid, st)
		if err != nil {
			return nil, err
		}
		set, err := queryJSONOne(ctx, tx, `SELECT base_currency::text AS base_currency, deferred_revenue_account_id, prepaid_expense_account_id
			FROM companies WHERE id = $1`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"rows": rows, "settings": set}, nil
	})
}

func (s *Server) getDeferral(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		d, err := queryJSONOne(ctx, tx, `SELECT `+deferralCols+deferralFrom+` WHERE d.id = $1 AND d.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		lines, err := queryJSON(ctx, tx, `SELECT l.period, l.amount::text AS amount, l.entry_id, e.number AS entry_number FROM deferral_lines l
			JOIN journal_entries e ON e.id = l.entry_id WHERE l.schedule_id = $1 ORDER BY l.period`, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"schedule": d, "lines": lines, "plan": deferralPlanJSON(ctx, tx, id)}, nil
	})
}

// deferralPlanJSON: the full month-by-month plan (done and to come).
func deferralPlanJSON(ctx context.Context, tx pgx.Tx, id string) []map[string]any {
	var amt, start string
	var months int
	if err := tx.QueryRow(ctx, `SELECT amount_base::text, months, start_period FROM deferral_schedules WHERE id = $1`, id).Scan(&amt, &months, &start); err != nil {
		return nil
	}
	total, _ := decimal.NewFromString(amt)
	out := []map[string]any{}
	for i, p := range deferralPlan(total, months, start) {
		out = append(out, map[string]any{"n": i + 1, "period": p.period, "amount": p.amount.StringFixed(2)})
	}
	return out
}

type planMonth struct {
	period string
	amount decimal.Decimal
}

// deferralPlan splits total over months starting at start; the last month takes the rounding difference.
func deferralPlan(total decimal.Decimal, months int, start string) []planMonth {
	t, _ := time.Parse("2006-01", start)
	each := total.DivRound(decimal.NewFromInt(int64(months)), 2)
	out := make([]planMonth, 0, months)
	done := decimal.Zero
	for i := 0; i < months; i++ {
		a := each
		if i == months-1 {
			a = total.Sub(done)
		}
		done = done.Add(a)
		out = append(out, planMonth{period: t.AddDate(0, i, 0).Format("2006-01"), amount: a})
	}
	return out
}

// POST /companies/{cid}/deferrals {kind, name, source_type, source_id, amount, months, start_period, target_account_id,
// deferral_account_id, cost_center_id, notes}. From an invoice / bill, the amount (before tax, in base currency) and the
// income / expense account are taken from the document when not given. The setup entry moves the amount to the
// deferral account on the document date; months are then recognised by the run (or the daily worker).
func (s *Server) createDeferral(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind              string `json:"kind"`
		Name              string `json:"name"`
		SourceType        string `json:"source_type"`
		SourceID          string `json:"source_id"`
		Amount            string `json:"amount"`
		Months            int    `json:"months"`
		StartPeriod       string `json:"start_period"`
		TargetAccountID   string `json:"target_account_id"`
		DeferralAccountID string `json:"deferral_account_id"`
		CostCenterID      string `json:"cost_center_id"`
		Notes             string `json:"notes"`
		Date              string `json:"date"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.SourceType == "" {
		in.SourceType = "manual"
	}
	if (in.Kind != "revenue" && in.Kind != "expense") || in.Months < 2 || in.Months > 120 ||
		(in.SourceType != "manual" && in.SourceType != "invoice" && in.SourceType != "bill" && in.SourceType != "expense") ||
		(in.SourceType != "manual" && !isUUID(in.SourceID)) || (in.StartPeriod != "" && !validPeriod(in.StartPeriod)) ||
		(in.TargetAccountID != "" && !isUUID(in.TargetAccountID)) || (in.DeferralAccountID != "" && !isUUID(in.DeferralAccountID)) ||
		(in.CostCenterID != "" && !isUUID(in.CostCenterID)) || (in.Date != "" && !validDate(in.Date)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		amount := decimal.Zero
		if strings.TrimSpace(in.Amount) != "" {
			a, ok := money(in.Amount)
			if !ok || !a.IsPositive() {
				return nil, badRequest("invalid_amount")
			}
			amount = a
		}
		date := in.Date
		name := strings.TrimSpace(in.Name)
		// defaults from the source document
		if in.SourceType != "manual" {
			var docAmt, docAcc, docDate, docName string
			var q string
			switch in.SourceType {
			case "invoice":
				if in.Kind != "revenue" {
					return nil, badRequest("bad_request")
				}
				q = `SELECT coalesce(sum(jl.base_credit - jl.base_debit), 0)::text, coalesce((SELECT il.revenue_account_id::text FROM invoice_lines il
					WHERE il.invoice_id = i.id ORDER BY il.line_no LIMIT 1), ''), i.issue_date::text, 'Invoice #' || i.number || ' · ' || c.name
					FROM invoices i JOIN customers c ON c.id = i.customer_id
					LEFT JOIN journal_lines jl ON jl.entry_id = i.journal_entry_id AND jl.account_id IN (SELECT il.revenue_account_id FROM invoice_lines il WHERE il.invoice_id = i.id)
					WHERE i.id = $1 AND i.company_id = $2 AND i.status <> 'void' GROUP BY i.id, c.name`
			case "bill":
				if in.Kind != "expense" {
					return nil, badRequest("bad_request")
				}
				q = `SELECT coalesce(sum(jl.base_debit - jl.base_credit), 0)::text, coalesce((SELECT bl.account_id::text FROM bill_lines bl
					WHERE bl.bill_id = b.id ORDER BY bl.line_no LIMIT 1), ''), b.bill_date::text, 'Bill #' || b.number || ' · ' || su.name
					FROM bills b JOIN suppliers su ON su.id = b.supplier_id
					LEFT JOIN journal_lines jl ON jl.entry_id = b.journal_entry_id AND jl.account_id IN (SELECT bl.account_id FROM bill_lines bl WHERE bl.bill_id = b.id)
					WHERE b.id = $1 AND b.company_id = $2 AND b.status <> 'void' GROUP BY b.id, su.name`
			case "expense":
				if in.Kind != "expense" {
					return nil, badRequest("bad_request")
				}
				q = `SELECT coalesce(sum(jl.base_debit), 0)::text, x.account_id::text, x.expense_date::text, 'Expense · ' || x.description
					FROM expenses x LEFT JOIN journal_lines jl ON jl.entry_id = x.journal_entry_id AND jl.account_id = x.account_id
					WHERE x.id = $1 AND x.company_id = $2 GROUP BY x.id`
			}
			err := tx.QueryRow(ctx, q, in.SourceID, cid).Scan(&docAmt, &docAcc, &docDate, &docName)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, badRequest("invalid_source")
			}
			if err != nil {
				return nil, err
			}
			var dup bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deferral_schedules WHERE company_id = $1 AND source_type = $2 AND source_id = $3
				AND status <> 'cancelled')`, cid, in.SourceType, in.SourceID).Scan(&dup); err != nil {
				return nil, err
			}
			if dup {
				return nil, &apiError{status: http.StatusConflict, code: "already_deferred"}
			}
			if amount.IsZero() {
				amount, _ = decimal.NewFromString(docAmt)
			}
			if in.TargetAccountID == "" {
				in.TargetAccountID = docAcc
			}
			if date == "" {
				date = docDate
			}
			if name == "" {
				name = docName
			}
		}
		if date == "" {
			date = today()
		}
		if in.StartPeriod == "" {
			in.StartPeriod = date[:7]
		}
		if name == "" || !amount.IsPositive() || in.TargetAccountID == "" {
			return nil, badRequest("bad_request")
		}
		if in.DeferralAccountID == "" {
			var def *string
			col := map[string]string{"revenue": "deferred_revenue_account_id", "expense": "prepaid_expense_account_id"}[in.Kind]
			if err := tx.QueryRow(ctx, `SELECT `+col+`::text FROM companies WHERE id = $1`, cid).Scan(&def); err != nil {
				return nil, err
			}
			if def == nil {
				return nil, notSetup(map[string]string{"revenue": "deferred_revenue", "expense": "prepaid_expense"}[in.Kind])
			}
			in.DeferralAccountID = *def
		} else { // remember the account the first time it is chosen
			col := map[string]string{"revenue": "deferred_revenue_account_id", "expense": "prepaid_expense_account_id"}[in.Kind]
			if _, err := tx.Exec(ctx, `UPDATE companies SET `+col+` = coalesce(`+col+`, $2::uuid) WHERE id = $1`, cid, in.DeferralAccountID); err != nil {
				return nil, err
			}
		}
		var tType, dType string
		for acc, t := range map[string]*string{in.TargetAccountID: &tType, in.DeferralAccountID: &dType} {
			if err := requirePostable(ctx, tx, cid, acc); err != nil {
				return nil, err
			}
			if err := tx.QueryRow(ctx, `SELECT type FROM accounts WHERE id = $1`, acc).Scan(t); err != nil {
				return nil, err
			}
		}
		if (in.Kind == "revenue" && (tType != "income" || dType != "liability")) || (in.Kind == "expense" && (tType != "expense" || dType != "asset")) {
			return nil, badRequest("wrong_account_type")
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'deferral')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		ctx = withCostCenter(ctx, in.CostCenterID)
		lines := []jline{}
		if in.Kind == "revenue" {
			lines = append(lines, jline{Account: in.TargetAccountID, Desc: name, Currency: set.Base, Debit: amount, Rate: "1"},
				jline{Account: in.DeferralAccountID, Desc: name, Currency: set.Base, Credit: amount, Rate: "1"})
		} else {
			lines = append(lines, jline{Account: in.DeferralAccountID, Desc: name, Currency: set.Base, Debit: amount, Rate: "1"},
				jline{Account: in.TargetAccountID, Desc: name, Currency: set.Base, Credit: amount, Rate: "1"})
		}
		je, err := postJournal(ctx, tx, cid, set, date, fmt.Sprintf("Deferral #%d · %s (%d months)", n, name, in.Months),
			fmt.Sprintf("DEF-%d", n), "deferral", lines)
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO deferral_schedules (company_id, number, kind, name, source_type, source_id, deferral_account_id,
			target_account_id, amount_base, months, start_period, cost_center_id, setup_entry_id, notes)
			VALUES ($1, $2, $3, $4, $5, nullif($6, '')::uuid, $7, $8, $9::numeric, $10, $11, nullif($12, '')::uuid, $13, $14) RETURNING id::text`,
			cid, n, in.Kind, name, in.SourceType, in.SourceID, in.DeferralAccountID, in.TargetAccountID, amount.String(), in.Months, in.StartPeriod,
			in.CostCenterID, je, strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		// months already passed are recognised right away (up to the current month)
		if _, err := recogniseDeferrals(ctx, tx, cid, time.Now().Format("2006-01"), id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+deferralCols+deferralFrom+` WHERE d.id = $1`, id)
	})
}

// POST /companies/{cid}/deferrals/run {period}: recognises every month due up to period (catching up).
func (s *Server) runDeferrals(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Period string `json:"period"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Period == "" {
		in.Period = time.Now().Format("2006-01")
	}
	if !validPeriod(in.Period) {
		writeErr(w, http.StatusBadRequest, "invalid_period")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return recogniseDeferrals(ctx, tx, r.PathValue("cid"), in.Period, "")
	})
}

// recogniseDeferrals posts one entry per schedule and month due up to period (only schedule `only` when given).
func recogniseDeferrals(ctx context.Context, tx pgx.Tx, cid, upTo, only string) (map[string]any, error) {
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text, number, kind, name, deferral_account_id::text, target_account_id::text, amount_base::text, months,
		start_period, coalesce(cost_center_id::text, '') FROM deferral_schedules
		WHERE company_id = $1 AND status = 'active' AND start_period <= $2 AND ($3 = '' OR id = nullif($3, '')::uuid) ORDER BY number FOR UPDATE`,
		cid, upTo, only)
	if err != nil {
		return nil, err
	}
	type sch struct {
		id, kind, name, def, target, start, cc string
		number                                 int64
		months                                 int
		total                                  decimal.Decimal
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sch, error) {
		var x sch
		var t string
		err := row.Scan(&x.id, &x.number, &x.kind, &x.name, &x.def, &x.target, &t, &x.months, &x.start, &x.cc)
		x.total, _ = decimal.NewFromString(t)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	posted, total := 0, decimal.Zero
	for _, x := range list {
		donePeriods := map[string]bool{}
		prow, err := tx.Query(ctx, `SELECT period FROM deferral_lines WHERE schedule_id = $1`, x.id)
		if err != nil {
			return nil, err
		}
		ps, err := pgx.CollectRows(prow, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			donePeriods[p] = true
		}
		plan := deferralPlan(x.total, x.months, x.start)
		for _, m := range plan {
			if m.period > upTo || donePeriods[m.period] || !m.amount.IsPositive() {
				continue
			}
			var lines []jline
			if x.kind == "revenue" {
				lines = []jline{{Account: x.def, Desc: x.name, Currency: set.Base, Debit: m.amount, Rate: "1"},
					{Account: x.target, Desc: x.name, Currency: set.Base, Credit: m.amount, Rate: "1", CostCenter: x.cc}}
			} else {
				lines = []jline{{Account: x.target, Desc: x.name, Currency: set.Base, Debit: m.amount, Rate: "1", CostCenter: x.cc},
					{Account: x.def, Desc: x.name, Currency: set.Base, Credit: m.amount, Rate: "1"}}
			}
			je, err := postJournal(ctx, tx, cid, set, periodEnd(m.period), fmt.Sprintf("Deferral #%d · %s · %s", x.number, x.name, m.period),
				fmt.Sprintf("DEF-%d-%s", x.number, m.period), "deferral", lines)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO deferral_lines (company_id, schedule_id, period, amount, entry_id) VALUES ($1, $2, $3, $4::numeric, $5)`,
				cid, x.id, m.period, m.amount.String(), je); err != nil {
				return nil, err
			}
			posted++
			total = total.Add(m.amount)
		}
		if _, err := tx.Exec(ctx, `UPDATE deferral_schedules SET status = 'done' WHERE id = $1
			AND (SELECT count(*) FROM deferral_lines l WHERE l.schedule_id = $1) >= months`, x.id); err != nil {
			return nil, err
		}
	}
	return map[string]any{"period": upTo, "entries": posted, "total": total.StringFixed(2)}, nil
}

// POST /companies/{cid}/deferrals/{id}/cancel: stops a schedule; what is not recognised yet goes back to the income /
// expense account today.
func (s *Server) cancelDeferral(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var kind, name, def, target, st, cc, rem string
		var n int64
		err = tx.QueryRow(ctx, `SELECT kind, name, deferral_account_id::text, target_account_id::text, status, coalesce(cost_center_id::text, ''), number,
			(amount_base - coalesce((SELECT sum(l.amount) FROM deferral_lines l WHERE l.schedule_id = d.id), 0))::text
			FROM deferral_schedules d WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&kind, &name, &def, &target, &st, &cc, &n, &rem)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st != "active" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		left, _ := decimal.NewFromString(rem)
		var je *string
		if left.IsPositive() {
			var lines []jline
			if kind == "revenue" {
				lines = []jline{{Account: def, Desc: name, Currency: set.Base, Debit: left, Rate: "1"},
					{Account: target, Desc: name, Currency: set.Base, Credit: left, Rate: "1", CostCenter: cc}}
			} else {
				lines = []jline{{Account: target, Desc: name, Currency: set.Base, Debit: left, Rate: "1", CostCenter: cc},
					{Account: def, Desc: name, Currency: set.Base, Credit: left, Rate: "1"}}
			}
			id2, err := postJournal(ctx, tx, cid, set, today(), fmt.Sprintf("Deferral #%d cancelled · %s", n, name), fmt.Sprintf("DEF-%d-X", n), "deferral", lines)
			if err != nil {
				return nil, err
			}
			je = &id2
		}
		if _, err := tx.Exec(ctx, `UPDATE deferral_schedules SET status = 'cancelled' WHERE id = $1`, id); err != nil {
			return nil, err
		}
		return map[string]any{"cancelled": id, "released": left.StringFixed(2), "entry_id": je}, nil
	})
}

// RunDeferrals recognises the months up to the previous month for every company with active schedules (daily worker).
func (s *Server) RunDeferrals(ctx context.Context) error {
	rows, err := s.OwnerPool.Query(ctx, `SELECT DISTINCT d.company_id::text, d.tenant_id::text FROM deferral_schedules d
		JOIN companies c ON c.id = d.company_id WHERE d.status = 'active' AND c.is_active`)
	if err != nil {
		return err
	}
	type co struct{ id, tenant string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (co, error) {
		var x co
		err := r.Scan(&x.id, &x.tenant)
		return x, err
	})
	if err != nil {
		return err
	}
	prev := time.Now().AddDate(0, -1, 0).Format("2006-01")
	for _, c := range list {
		err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, c.tenant); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
				return err
			}
			_, err := recogniseDeferrals(ctx, tx, c.id, prev, "")
			return err
		})
		if err != nil {
			slog.Warn("deferrals skipped", "company", c.id, "err", err)
		}
	}
	return nil
}
