package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Smart features, part 2: cash-flow forecast (13 weeks), late-payer / churn risk, smart reorder and the collector
// route. All of it reads the books; the only writes are the risk table, customer coordinates, reorder rules (the
// same rules MRP uses) and draft purchase orders (requests for quotation).

// ======================= cash-flow forecast =======================

type fcWeek struct {
	Start        string             `json:"start"`
	End          string             `json:"end"`
	In           map[string]float64 `json:"in"`
	Out          map[string]float64 `json:"out"`
	Net          float64            `json:"net"`
	Closing      float64            `json:"closing"`
	ClosingLocal *float64           `json:"closing_local"`
	Optimistic   float64            `json:"optimistic"`
	Pessimistic  float64            `json:"pessimistic"`
	BelowZero    bool               `json:"below_zero"`
	inTotal      float64
	outTotal     float64
}

// forecastData computes the 13-week cash forecast of a company in its base currency.
func forecastData(ctx context.Context, tx pgx.Tx, cid string) (map[string]any, error) {
	var base, local string
	if err := tx.QueryRow(ctx, `SELECT base_currency::text, local_currency::text FROM companies WHERE id = $1`, cid).Scan(&base, &local); err != nil {
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		return nil, err
	}
	start := time.Now().Truncate(24 * time.Hour)
	day0 := today()
	weeks := make([]*fcWeek, 13)
	for i := range weeks {
		s := start.AddDate(0, 0, 7*i)
		weeks[i] = &fcWeek{Start: s.Format(time.DateOnly), End: s.AddDate(0, 0, 6).Format(time.DateOnly), In: map[string]float64{}, Out: map[string]float64{}}
	}
	weekOf := func(d time.Time) int {
		i := int(math.Floor(d.Sub(start).Hours() / 24 / 7))
		if i < 0 {
			i = 0
		}
		return i
	}
	add := func(in bool, key string, d time.Time, amt float64) {
		i := weekOf(d)
		if i > 12 || amt == 0 || math.IsNaN(amt) {
			return
		}
		if in {
			weeks[i].In[key] += amt
		} else {
			weeks[i].Out[key] += amt
		}
	}
	// opening: the money in every till / bank of the company
	var opening float64
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(smart_to_base($1, t.currency::text, t.debit - t.credit)), 0)::float8
		FROM (SELECT DISTINCT account_id, currency FROM payment_methods WHERE company_id = $1 AND is_active) m
		JOIN account_totals($1, '0001-01-01'::date, current_date) t ON t.account_id = m.account_id AND t.currency = m.currency`, cid).Scan(&opening); err != nil {
		return nil, err
	}
	// how late customers pay (company-wide, and per customer)
	var companyLate float64
	if err := tx.QueryRow(ctx, `SELECT coalesce(avg(greatest(x.paid_on - x.due_date, 0)), 0)::float8 FROM (
		SELECT i.due_date, max(p.payment_date) AS paid_on FROM invoices i JOIN payment_allocations a ON a.invoice_id = i.id
		JOIN payments p ON p.id = a.payment_id AND p.status = 'posted'
		WHERE i.company_id = $1 AND i.status = 'paid' AND i.issue_date > current_date - 365 GROUP BY i.id, i.due_date) x`, cid).Scan(&companyLate); err != nil {
		return nil, err
	}
	// open receivables, each expected on its due date + the customer's usual delay, weighted by how old it is
	rows, err := tx.Query(ctx, `
		WITH late AS (
		  SELECT x.customer_id, avg(greatest(x.paid_on - x.due_date, 0))::float8 AS d FROM (
		    SELECT i.customer_id, i.due_date, max(p.payment_date) AS paid_on FROM invoices i JOIN payment_allocations a ON a.invoice_id = i.id
		    JOIN payments p ON p.id = a.payment_id AND p.status = 'posted'
		    WHERE i.company_id = $1 AND i.status = 'paid' AND i.issue_date > current_date - 365 GROUP BY i.id, i.customer_id, i.due_date) x
		  GROUP BY x.customer_id)
		SELECT i.due_date, coalesce(l.d, $2)::float8, (current_date - i.due_date)::int, coalesce(smart_to_base($1, i.currency::text, i.total - i.paid), 0)::float8
		FROM invoices i LEFT JOIN late l ON l.customer_id = i.customer_id
		WHERE i.company_id = $1 AND i.status IN ('open', 'partial')`, cid, companyLate)
	if err != nil {
		return nil, err
	}
	var arOpen, arWeighted float64
	for rows.Next() {
		var due time.Time
		var lateD float64
		var overdue int
		var amt float64
		if err := rows.Scan(&due, &lateD, &overdue, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		w := 0.95
		switch {
		case overdue > 180:
			w = 0.2
		case overdue > 90:
			w = 0.45
		case overdue > 30:
			w = 0.75
		}
		exp := due.AddDate(0, 0, int(math.Round(lateD)))
		if exp.Before(start) {
			// already late: expected over the next 4 weeks
			for k := 0; k < 4; k++ {
				add(true, "receivables", start.AddDate(0, 0, 7*k), amt*w/4)
			}
		} else {
			add(true, "receivables", exp, amt*w)
		}
		arOpen += amt
		arWeighted += amt * w
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// subscriptions: next months' bills (average of the last 3 months), collected as they usually are
	var subMonthly, collectRate, payDays float64
	var issueDay int
	if err := tx.QueryRow(ctx, `SELECT
		  coalesce((SELECT sum(smart_to_base($1, i.currency::text, i.total)) FROM invoices i WHERE i.company_id = $1 AND i.subscription_id IS NOT NULL
		            AND i.status <> 'void' AND i.issue_date >= date_trunc('month', current_date) - interval '3 months' AND i.issue_date < date_trunc('month', current_date)) / 3, 0)::float8,
		  coalesce((SELECT sum(i.paid) / nullif(sum(i.total), 0) FROM invoices i WHERE i.company_id = $1 AND i.subscription_id IS NOT NULL AND i.status <> 'void'
		            AND i.issue_date BETWEEN current_date - 150 AND current_date - 45), 0.85)::float8,
		  coalesce((SELECT avg(x.paid_on - x.issue_date) FROM (SELECT i.issue_date, max(p.payment_date) AS paid_on FROM invoices i
		            JOIN payment_allocations a ON a.invoice_id = i.id JOIN payments p ON p.id = a.payment_id AND p.status = 'posted'
		            WHERE i.company_id = $1 AND i.subscription_id IS NOT NULL AND i.status = 'paid' AND i.issue_date > current_date - 180 GROUP BY i.id, i.issue_date) x), 10)::float8,
		  coalesce((SELECT percentile_disc(0.5) WITHIN GROUP (ORDER BY extract(day FROM i.issue_date)::int) FROM invoices i
		            WHERE i.company_id = $1 AND i.subscription_id IS NOT NULL AND i.status <> 'void' AND i.issue_date > current_date - 120), 1)`, cid).
		Scan(&subMonthly, &collectRate, &payDays, &issueDay); err != nil {
		return nil, err
	}
	if collectRate <= 0 || collectRate > 1 {
		collectRate = 0.85
	}
	var billedThisMonth bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM invoices WHERE company_id = $1 AND subscription_id IS NOT NULL AND status <> 'void'
		AND period = to_char(current_date, 'YYYY-MM'))`, cid).Scan(&billedThisMonth); err != nil {
		return nil, err
	}
	if subMonthly > 0 {
		for m := 0; m <= 3; m++ {
			if m == 0 && billedThisMonth {
				continue
			}
			first := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, m, 0)
			d := issueDay
			if last := first.AddDate(0, 1, -1).Day(); d > last {
				d = last
			}
			issue := first.AddDate(0, 0, d-1)
			if issue.Before(start) {
				issue = start
			}
			// collected over the 4 weeks after the usual delay
			for k := 0; k < 4; k++ {
				add(true, "subscriptions", issue.AddDate(0, 0, int(payDays)+7*k), subMonthly*collectRate/4)
			}
		}
	}
	// point-of-sale takings: the weekly average of the last 8 weeks
	var posWeekly float64
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(smart_to_base($1, o.currency::text, CASE WHEN o.kind = 'sale' THEN o.paid ELSE -o.paid END)) / 8, 0)::float8
		FROM pos_orders o WHERE o.company_id = $1 AND o.order_date > current_date - 56 AND o.order_date <= current_date`, cid).Scan(&posWeekly); err != nil {
		return nil, err
	}
	for i := range weeks {
		if posWeekly > 0 {
			weeks[i].In["pos"] += posWeekly
		}
	}
	// supplier bills on their due date (late ones in the first week)
	rows, err = tx.Query(ctx, `SELECT b.due_date, coalesce(smart_to_base($1, b.currency::text, b.total - b.paid), 0)::float8
		FROM bills b WHERE b.company_id = $1 AND b.status IN ('open', 'partial')`, cid)
	if err != nil {
		return nil, err
	}
	var apOpen float64
	for rows.Next() {
		var due time.Time
		var amt float64
		if err := rows.Scan(&due, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		add(false, "payables", due, amt)
		apOpen += amt
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// payroll: salaries + transport of active employees at each month end
	var payroll float64
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(smart_to_base($1, e.currency::text, e.salary + e.transport)), 0)::float8
		FROM employees e WHERE e.company_id = $1 AND e.status = 'active'`, cid).Scan(&payroll); err != nil {
		return nil, err
	}
	if payroll > 0 {
		for m := 0; m <= 3; m++ {
			end := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, m+1, -1)
			if !end.Before(start) {
				add(false, "payroll", end, payroll)
			}
		}
	}
	// recurring expenses on their next dates; other expenses at their weekly average (without the recurring part)
	rows, err = tx.Query(ctx, `SELECT r.next_date, r.frequency, r.end_date, coalesce(smart_to_base($1, r.currency::text, r.amount), 0)::float8
		FROM recurring_templates r WHERE r.company_id = $1 AND r.is_active AND r.kind = 'expense'`, cid)
	if err != nil {
		return nil, err
	}
	var recurringWeekly float64
	horizonEnd := start.AddDate(0, 0, 7*13)
	for rows.Next() {
		var next time.Time
		var freq string
		var end *time.Time
		var amt float64
		if err := rows.Scan(&next, &freq, &end, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		step := map[string]int{"monthly": 1, "quarterly": 3, "yearly": 12}[freq]
		if step == 0 {
			step = 1
		}
		recurringWeekly += amt / (float64(step) * 4.33)
		for d := next; d.Before(horizonEnd); d = d.AddDate(0, step, 0) {
			if end != nil && d.After(*end) {
				break
			}
			if !d.Before(start) {
				add(false, "recurring", d, amt)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var expWeekly float64
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(smart_to_base($1, e.currency::text, e.amount)) / 12, 0)::float8 FROM expenses e
		WHERE e.company_id = $1 AND e.status = 'posted' AND e.expense_date > current_date - 84 AND e.expense_date <= current_date`, cid).Scan(&expWeekly); err != nil {
		return nil, err
	}
	expWeekly = math.Max(expWeekly-recurringWeekly, 0)
	for i := range weeks {
		if expWeekly > 0 {
			weeks[i].Out["expenses"] += expWeekly
		}
	}
	// totals, running balance and the two scenarios (collections ±, spending ±)
	localRate := 0.0
	if local != "" && local != base {
		if r, err := resolveRate(ctx, tx, cid, base, local, day0, ""); err == nil {
			localRate, _ = strconv.ParseFloat(r, 64)
		}
	}
	bal, opt, pes := opening, opening, opening
	firstNeg := ""
	lowest, lowestWeek := opening, ""
	for _, w := range weeks {
		for k, v := range w.In {
			w.In[k] = round2(v)
			w.inTotal += v
		}
		for k, v := range w.Out {
			w.Out[k] = round2(v)
			w.outTotal += v
		}
		w.Net = round2(w.inTotal - w.outTotal)
		bal += w.inTotal - w.outTotal
		opt += w.inTotal*1.1 - w.outTotal*0.95
		pes += w.inTotal*0.8 - w.outTotal*1.1
		w.Closing, w.Optimistic, w.Pessimistic = round2(bal), round2(opt), round2(pes)
		if localRate > 0 {
			v := round2(bal * localRate)
			w.ClosingLocal = &v
		}
		w.BelowZero = bal < 0
		if bal < 0 && firstNeg == "" {
			firstNeg = w.Start
		}
		if bal < lowest {
			lowest, lowestWeek = bal, w.Start
		}
	}
	// the same weeks grouped by month
	type fcMonth struct {
		Month   string  `json:"month"`
		In      float64 `json:"in"`
		Out     float64 `json:"out"`
		Net     float64 `json:"net"`
		Closing float64 `json:"closing"`
	}
	var months []*fcMonth
	for _, w := range weeks {
		m := w.Start[:7]
		if len(months) == 0 || months[len(months)-1].Month != m {
			months = append(months, &fcMonth{Month: m})
		}
		cur := months[len(months)-1]
		cur.In = round2(cur.In + w.inTotal)
		cur.Out = round2(cur.Out + w.outTotal)
		cur.Net = round2(cur.In - cur.Out)
		cur.Closing = w.Closing
	}
	var lr *float64
	if localRate > 0 {
		lr = &localRate
	}
	return map[string]any{
		"base_currency": base, "local_currency": local, "local_rate": lr, "opening": round2(opening), "weeks": weeks, "months": months,
		"first_negative_week": firstNeg, "lowest": round2(lowest), "lowest_week": lowestWeek,
		"assumptions": map[string]any{"ar_open": round2(arOpen), "ar_expected": round2(arWeighted), "avg_days_late": round2(companyLate),
			"subscriptions_monthly": round2(subMonthly), "collection_rate": round2(collectRate * 100), "days_to_pay": round2(payDays),
			"pos_weekly": round2(posWeekly), "ap_open": round2(apOpen), "payroll_monthly": round2(payroll), "expenses_weekly": round2(expWeekly),
			"optimistic": "collections +10%, spending -5%", "pessimistic": "collections -20%, spending +10%"},
	}, nil
}

// GET /smart/forecast
func (s *Server) smartForecast(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return forecastData(ctx, tx, cid)
	})
}

// ======================= late-payer / churn risk =======================

type riskFactor struct {
	Code   string  `json:"code"`
	Value  float64 `json:"value"`
	Points int     `json:"points"`
}

type riskRow struct {
	Customer           string
	Score, Late, Churn int
	Band               string
	AvgLate            *float64
	Overdue            float64
	Factors            []riskFactor
}

// computeRisk scores every customer with recent bills or a live subscription and stores the result. Returns how
// many customers were scored.
func computeRisk(ctx context.Context, tx pgx.Tx, cid string) (int, error) {
	rows, err := tx.Query(ctx, `
		WITH inv AS (
		  SELECT i.id, i.customer_id, i.issue_date, i.due_date, i.total, i.paid, i.status, i.currency,
		    (SELECT max(p.payment_date) FROM payment_allocations a JOIN payments p ON p.id = a.payment_id WHERE a.invoice_id = i.id AND p.status = 'posted') AS last_pay,
		    (SELECT count(DISTINCT a.payment_id) FROM payment_allocations a JOIN payments p ON p.id = a.payment_id
		     WHERE a.invoice_id = i.id AND p.status = 'posted' AND p.kind = 'payment') AS npay
		  FROM invoices i WHERE i.company_id = $1 AND i.status <> 'void' AND i.issue_date > current_date - 365)
		SELECT c.id::text, c.status,
		  count(inv.id)::int,
		  avg(greatest(inv.last_pay - inv.due_date, 0)) FILTER (WHERE inv.status = 'paid')::float8,
		  avg(greatest(inv.last_pay - inv.due_date, 0)) FILTER (WHERE inv.status = 'paid' AND inv.issue_date > current_date - 90)::float8,
		  avg(greatest(inv.last_pay - inv.due_date, 0)) FILTER (WHERE inv.status = 'paid' AND inv.issue_date <= current_date - 90)::float8,
		  count(inv.id) FILTER (WHERE inv.npay > 1 OR inv.status = 'partial')::int,
		  count(inv.id) FILTER (WHERE inv.status IN ('open', 'partial') AND inv.due_date < current_date)::int,
		  coalesce(max(current_date - inv.due_date) FILTER (WHERE inv.status IN ('open', 'partial') AND inv.due_date < current_date), 0)::int,
		  coalesce(sum(smart_to_base($1, inv.currency::text, inv.total - inv.paid)) FILTER (WHERE inv.status IN ('open', 'partial') AND inv.due_date < current_date), 0)::float8,
		  (current_date - least(c.created_at::date, coalesce((SELECT min(s.start_date) FROM subscriptions s WHERE s.customer_id = c.id), c.created_at::date)))::int,
		  (SELECT count(*) FROM subscriptions s WHERE s.customer_id = c.id AND s.status = 'suspended')::int,
		  (SELECT count(*) FROM subscriptions s WHERE s.customer_id = c.id AND s.status = 'active')::int,
		  (SELECT count(*) FROM subscriptions s WHERE s.customer_id = c.id AND s.status = 'ended' AND s.updated_at > now() - interval '90 days')::int,
		  ((SELECT count(*) FROM dunning_events d WHERE d.customer_id = c.id AND d.service_cut AND d.created_at > now() - interval '365 days')
		   + (SELECT count(*) FROM router_actions ra JOIN subscriptions s ON s.id = ra.subscription_id
		      WHERE s.customer_id = c.id AND ra.action = 'disable' AND ra.created_at > now() - interval '365 days'))::int,
		  coalesce((SELECT current_date - max(p.payment_date) FROM payments p WHERE p.customer_id = c.id AND p.status = 'posted' AND p.kind = 'payment'), -1)::int
		FROM customers c LEFT JOIN inv ON inv.customer_id = c.id
		WHERE c.company_id = $1 AND c.status <> 'closed'
		GROUP BY c.id, c.status, c.created_at
		HAVING count(inv.id) > 0 OR EXISTS (SELECT 1 FROM subscriptions s WHERE s.customer_id = c.id AND s.status <> 'ended')`, cid)
	if err != nil {
		return 0, err
	}
	var out []riskRow
	for rows.Next() {
		var id, status string
		var nInv, nPartial, nOverdue, maxOver, tenure, nSusp, nActive, nEnded, nCuts, sincePay int
		var avgLate, lateRecent, lateBefore *float64
		var overdue float64
		if err := rows.Scan(&id, &status, &nInv, &avgLate, &lateRecent, &lateBefore, &nPartial, &nOverdue, &maxOver, &overdue, &tenure,
			&nSusp, &nActive, &nEnded, &nCuts, &sincePay); err != nil {
			rows.Close()
			return 0, err
		}
		rr := riskRow{Customer: id, AvgLate: avgLate, Overdue: round2(overdue)}
		late, churn := 0.0, 0.0
		addF := func(toLate bool, code string, value, pts float64) {
			if pts <= 0 {
				return
			}
			if toLate {
				late += pts
			} else {
				churn += pts
			}
			rr.Factors = append(rr.Factors, riskFactor{Code: code, Value: round2(value), Points: int(math.Round(pts))})
		}
		if avgLate != nil {
			addF(true, "avg_days_late", *avgLate, math.Min(*avgLate/30, 1)*35)
		}
		rising := lateRecent != nil && lateBefore != nil && *lateRecent > *lateBefore+5
		if rising {
			addF(true, "lateness_rising", *lateRecent-*lateBefore, math.Min((*lateRecent-*lateBefore)/30, 1)*15)
		}
		if maxOver > 0 {
			addF(true, "days_overdue", float64(maxOver), math.Min(float64(maxOver)/60, 1)*25)
		}
		if nOverdue >= 2 {
			addF(true, "bills_overdue", float64(nOverdue), math.Min(float64(nOverdue-1)*3, 10))
		}
		if nInv > 0 && nPartial > 0 {
			addF(true, "partial_payments", float64(nPartial), float64(nPartial)/float64(nInv)*15)
		}
		if nCuts > 0 {
			addF(true, "service_cuts", float64(nCuts), math.Min(float64(nCuts), 3)*5)
		}
		if nEnded > 0 && nActive == 0 {
			addF(false, "subscription_ended", float64(nEnded), 40)
		}
		if nSusp > 0 {
			addF(false, "suspended", float64(nSusp), 25)
		}
		if status == "suspended" {
			addF(false, "customer_suspended", 1, 15)
		}
		if sincePay > 60 && overdue > 0 {
			addF(false, "no_payment_days", float64(sincePay), 20)
		}
		switch {
		case tenure < 60:
			addF(false, "new_customer", float64(tenure), 15)
		case tenure < 180:
			addF(false, "new_customer", float64(tenure), 8)
		}
		if rising {
			addF(false, "lateness_rising", *lateRecent-*lateBefore, 10)
		}
		late, churn = math.Min(late, 100), math.Min(churn, 100)
		score := math.Max(late, churn)*0.75 + math.Min(late, churn)*0.25
		rr.Late, rr.Churn, rr.Score = int(math.Round(late)), int(math.Round(churn)), int(math.Min(math.Round(score), 100))
		switch {
		case rr.Score >= 60:
			rr.Band = "high"
		case rr.Score >= 30:
			rr.Band = "medium"
		default:
			rr.Band = "low"
		}
		if rr.Factors == nil {
			rr.Factors = []riskFactor{}
		}
		sort.Slice(rr.Factors, func(i, j int) bool { return rr.Factors[i].Points > rr.Factors[j].Points })
		out = append(out, rr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	ids := make([]string, len(out))
	scores, lates, churns := make([]int32, len(out)), make([]int32, len(out)), make([]int32, len(out))
	bands, factors := make([]string, len(out)), make([]string, len(out))
	avgs := make([]*float64, len(out))
	overs := make([]float64, len(out))
	for i, rr := range out {
		fj, _ := json.Marshal(rr.Factors)
		ids[i], scores[i], lates[i], churns[i], bands[i], factors[i], avgs[i], overs[i] = rr.Customer, int32(rr.Score), int32(rr.Late), int32(rr.Churn),
			rr.Band, string(fj), rr.AvgLate, rr.Overdue
	}
	if _, err := tx.Exec(ctx, `DELETE FROM customer_risk WHERE company_id = $1 AND NOT (customer_id = ANY ($2::uuid[]))`, cid, ids); err != nil {
		return 0, err
	}
	if len(out) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO customer_risk (company_id, customer_id, score, band, late_score, churn_score, avg_days_late, overdue_base, factors, computed_at)
			SELECT $1, x.c, x.s, x.b, x.l, x.ch, x.a, x.o, x.f::jsonb, now()
			FROM unnest($2::uuid[], $3::int[], $4::text[], $5::int[], $6::int[], $7::numeric[], $8::numeric[], $9::text[]) AS x(c, s, b, l, ch, a, o, f)
			ON CONFLICT (company_id, customer_id) DO UPDATE SET score = EXCLUDED.score, band = EXCLUDED.band, late_score = EXCLUDED.late_score,
			  churn_score = EXCLUDED.churn_score, avg_days_late = EXCLUDED.avg_days_late, overdue_base = EXCLUDED.overdue_base, factors = EXCLUDED.factors,
			  computed_at = now()`, cid, ids, scores, bands, lates, churns, avgs, overs, factors); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO smart_settings (company_id, last_risk_at) VALUES ($1, now())
		ON CONFLICT (company_id) DO UPDATE SET last_risk_at = now()`, cid); err != nil {
		return 0, err
	}
	return len(out), nil
}

// GET /smart/risk?band=&area_id=&q=: the scored customers, riskiest first (scored now when never scored).
func (s *Server) smartRisk(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	band, area, search := q.Get("band"), q.Get("area_id"), strings.TrimSpace(q.Get("q"))
	if (band != "" && !contains([]string{"low", "medium", "high"}, band)) || (area != "" && !isUUID(area)) || len(search) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		var last *time.Time
		_ = tx.QueryRow(ctx, `SELECT last_risk_at FROM smart_settings WHERE company_id = $1`, cid).Scan(&last)
		if last == nil {
			if _, err := computeRisk(ctx, tx, cid); err != nil {
				return nil, err
			}
			_ = tx.QueryRow(ctx, `SELECT last_risk_at FROM smart_settings WHERE company_id = $1`, cid).Scan(&last)
		}
		rows, err := queryJSON(ctx, tx, `SELECT cr.customer_id, c.code, c.name, c.phone, ar.name AS area_name, cr.score, cr.band, cr.late_score, cr.churn_score,
			cr.avg_days_late::float8 AS avg_days_late, cr.overdue_base::float8 AS overdue_base, cr.factors, cr.computed_at, (c.lat IS NOT NULL) AS has_location
			FROM customer_risk cr JOIN customers c ON c.id = cr.customer_id LEFT JOIN areas ar ON ar.id = c.area_id
			WHERE cr.company_id = $1 AND ($2 = '' OR cr.band = $2) AND ($3 = '' OR c.area_id = $3::uuid)
			  AND ($4 = '' OR c.name ILIKE '%' || $4 || '%' OR c.code ILIKE $4 || '%' OR c.phone LIKE '%' || $4 || '%')
			ORDER BY cr.score DESC, cr.overdue_base DESC LIMIT 1000`, cid, band, area, search)
		if err != nil {
			return nil, err
		}
		summary, err := queryJSON(ctx, tx, `SELECT band, count(*) AS customers, coalesce(sum(overdue_base), 0)::float8 AS overdue_base
			FROM customer_risk WHERE company_id = $1 GROUP BY band`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"rows": rows, "summary": summary, "computed_at": last}, nil
	})
}

// POST /smart/risk/recompute
func (s *Server) smartRiskRecompute(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		n, err := computeRisk(ctx, tx, cid)
		return map[string]int{"customers": n}, err
	})
}

// ======================= smart reorder =======================

// normInv: the quantile of the standard normal distribution (Acklam's approximation), for the safety stock.
func normInv(p float64) float64 {
	if p <= 0 || p >= 1 {
		return 0
	}
	a := []float64{-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02, 1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00}
	b := []float64{-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02, 6.680131188771972e+01, -1.328068155288572e+01}
	c := []float64{-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00, -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00}
	d := []float64{7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00, 3.754408661907416e+00}
	pl := 0.02425
	switch {
	case p < pl:
		q := math.Sqrt(-2 * math.Log(p))
		return (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	case p <= 1-pl:
		q := p - 0.5
		rr := q * q
		return (((((a[0]*rr+a[1])*rr+a[2])*rr+a[3])*rr+a[4])*rr + a[5]) * q / (((((b[0]*rr+b[1])*rr+b[2])*rr+b[3])*rr+b[4])*rr + 1)
	default:
		q := math.Sqrt(-2 * math.Log(1-p))
		return -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	}
}

type reorderLine struct {
	ItemID        string   `json:"item_id"`
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	Unit          string   `json:"unit"`
	WarehouseID   string   `json:"warehouse_id"`
	WarehouseName string   `json:"warehouse_name"`
	OnHand        float64  `json:"on_hand"`
	OnOrder       float64  `json:"on_order"`
	WeeklyDemand  float64  `json:"weekly_demand"`
	Seasonal      *float64 `json:"seasonal_factor"`
	LeadDays      int      `json:"lead_days"`
	LeadSource    string   `json:"lead_source"` // supplier | default
	Safety        float64  `json:"safety_stock"`
	SuggestMin    float64  `json:"suggest_min"`
	SuggestMax    float64  `json:"suggest_max"`
	RuleMin       *float64 `json:"rule_min"`
	RuleMax       *float64 `json:"rule_max"`
	ToOrder       float64  `json:"to_order"`
	DaysLeft      *float64 `json:"days_left"`
	SupplierID    *string  `json:"supplier_id"`
	SupplierName  *string  `json:"supplier_name"`
	UnitPrice     *float64 `json:"unit_price"`
	Currency      *string  `json:"currency"`
	Status        string   `json:"status"` // order | watch | ok
	Weeks         int      `json:"weeks"`
}

func reorderCompute(ctx context.Context, tx pgx.Tx, cid, wh string, p smartParams) ([]*reorderLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT m.item_id::text, m.warehouse_id::text, ((current_date - m.move_date) / 7)::int, (-sum(m.quantity))::float8
		FROM stock_moves m JOIN items i ON i.id = m.item_id
		WHERE m.company_id = $1 AND m.quantity < 0 AND m.move_date > current_date - 7 * 57 AND m.move_date <= current_date
		  AND m.source IN ('sale', 'delivery', 'pos', 'fuel', 'mfg_out', 'mfg_sub_out') AND i.kind = 'stock' AND i.is_active
		  AND ($2 = '' OR m.warehouse_id = $2::uuid)
		GROUP BY 1, 2, 3`, cid, wh)
	if err != nil {
		return nil, err
	}
	type key struct{ item, wh string }
	series := map[key]map[int]float64{}
	for rows.Next() {
		var it, w string
		var b int
		var q float64
		if err := rows.Scan(&it, &w, &b, &q); err != nil {
			rows.Close()
			return nil, err
		}
		k := key{it, w}
		if series[k] == nil {
			series[k] = map[int]float64{}
		}
		series[k][b] += q
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// items that have a reorder rule but no recent use are shown too
	rows, err = tx.Query(ctx, `SELECT item_id::text, warehouse_id::text FROM reorder_rules WHERE company_id = $1 AND ($2 = '' OR warehouse_id = $2::uuid)`, cid, wh)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var it, w string
		if err := rows.Scan(&it, &w); err != nil {
			rows.Close()
			return nil, err
		}
		if series[key{it, w}] == nil {
			series[key{it, w}] = map[int]float64{}
		}
	}
	rows.Close()
	z := normInv(p.get("service_level") / 100)
	review := p.get("review_days")
	defLead := int(p.get("lead_days"))
	var out []*reorderLine
	for k, s := range series {
		l := &reorderLine{ItemID: k.item, WarehouseID: k.wh, LeadDays: defLead, LeadSource: "default"}
		var ruleMin, ruleMax *float64
		var ruleSup *string
		err := tx.QueryRow(ctx, `SELECT i.code, i.name, i.unit, w.name,
			  coalesce((SELECT quantity FROM stock WHERE item_id = i.id AND warehouse_id = w.id), 0)::float8,
			  coalesce((SELECT sum((pl.quantity - pl.billed_qty) * coalesce(un.factor, 1)) FROM purchase_order_lines pl JOIN purchase_orders o ON o.id = pl.order_id
			            LEFT JOIN item_units un ON un.id = pl.unit_id
			            WHERE pl.item_id = i.id AND coalesce(pl.warehouse_id, w.id) = w.id AND o.status IN ('rfq', 'sent', 'confirmed', 'partial')), 0)::float8,
			  r.min_qty::float8, r.max_qty::float8, r.supplier_id::text
			FROM items i JOIN warehouses w ON w.id = $2 LEFT JOIN reorder_rules r ON r.item_id = i.id AND r.warehouse_id = w.id
			WHERE i.id = $1 AND i.company_id = $3 AND w.company_id = $3`, k.item, k.wh, cid).
			Scan(&l.Code, &l.Name, &l.Unit, &l.WarehouseName, &l.OnHand, &l.OnOrder, &ruleMin, &ruleMax, &ruleSup)
		if err == pgx.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		l.RuleMin, l.RuleMax = ruleMin, ruleMax
		// supplier: the rule's, else the cheapest current price; its lead time
		var supID, supName, cur *string
		var price *float64
		var lead *int
		err = tx.QueryRow(ctx, `SELECT sp.supplier_id::text, su.name, sp.price::float8, sp.currency::text, sp.lead_days FROM supplier_prices sp
			JOIN suppliers su ON su.id = sp.supplier_id
			WHERE sp.company_id = $1 AND sp.item_id = $2 AND ($3::text IS NULL OR sp.supplier_id = $3::uuid)
			  AND (sp.valid_from IS NULL OR sp.valid_from <= current_date) AND (sp.valid_to IS NULL OR sp.valid_to >= current_date)
			ORDER BY sp.price, sp.lead_days LIMIT 1`, cid, k.item, ruleSup).Scan(&supID, &supName, &price, &cur, &lead)
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
		if supID == nil && ruleSup != nil {
			supID = ruleSup
			_ = tx.QueryRow(ctx, `SELECT name FROM suppliers WHERE id = $1`, *ruleSup).Scan(&supName)
		}
		l.SupplierID, l.SupplierName, l.UnitPrice, l.Currency = supID, supName, price, cur
		if lead != nil && *lead > 0 {
			l.LeadDays, l.LeadSource = *lead, "supplier"
		}
		// demand: weighted average of the last 4 weeks, adjusted by last year's season when a full year is known
		wk := func(b int) float64 { return s[b] }
		oldest := -1
		for b := range s {
			if b > oldest {
				oldest = b
			}
		}
		l.Weeks = oldest + 1
		wma := (4*wk(0) + 3*wk(1) + 2*wk(2) + wk(3)) / 10
		if oldest >= 52 {
			next := (wk(48) + wk(49) + wk(50) + wk(51)) / 4
			recentLY := (wk(52) + wk(53) + wk(54) + wk(55)) / 4
			if recentLY > 0 && next > 0 {
				f := math.Max(0.5, math.Min(2, next/recentLY))
				fr := round2(f)
				l.Seasonal = &fr
				wma *= f
			}
		}
		last12 := make([]float64, 12)
		for b := 0; b < 12; b++ {
			last12[b] = wk(b)
		}
		_, sdW := meanSD(last12)
		daily := wma / 7
		sdDaily := sdW / math.Sqrt(7)
		L := float64(l.LeadDays)
		l.WeeklyDemand = round2(wma)
		l.Safety = math.Ceil(z * sdDaily * math.Sqrt(math.Max(L, 1)))
		l.SuggestMin = math.Ceil(daily*L + l.Safety)
		l.SuggestMax = math.Ceil(l.SuggestMin + daily*review)
		if daily > 0 {
			dl := round2(l.OnHand / daily)
			l.DaysLeft = &dl
		}
		min, max := l.SuggestMin, l.SuggestMax
		if ruleMin != nil && ruleMax != nil && daily == 0 {
			min, max = *ruleMin, *ruleMax // no recent use: keep the owner's rule
		}
		avail := l.OnHand + l.OnOrder
		switch {
		case max > 0 && avail <= min:
			l.Status = "order"
			l.ToOrder = math.Ceil(max - avail)
		case max > 0 && avail <= min+daily*7:
			l.Status = "watch"
		default:
			l.Status = "ok"
		}
		if l.Status == "ok" && l.SuggestMin == 0 && ruleMin == nil {
			continue // nothing used and no rule: not worth a line
		}
		out = append(out, l)
	}
	rank := map[string]int{"order": 0, "watch": 1, "ok": 2}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Status] != rank[out[j].Status] {
			return rank[out[i].Status] < rank[out[j].Status]
		}
		di, dj := math.Inf(1), math.Inf(1)
		if out[i].DaysLeft != nil {
			di = *out[i].DaysLeft
		}
		if out[j].DaysLeft != nil {
			dj = *out[j].DaysLeft
		}
		if di != dj {
			return di < dj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// GET /smart/reorder?warehouse_id=
func (s *Server) smartReorder(w http.ResponseWriter, r *http.Request) {
	wh := r.URL.Query().Get("warehouse_id")
	if wh != "" && !isUUID(wh) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		st, err := loadSmartSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		lines, err := reorderCompute(ctx, tx, cid, wh, st.Params)
		if err != nil {
			return nil, err
		}
		if lines == nil {
			lines = []*reorderLine{}
		}
		return map[string]any{"lines": lines, "service_level": st.Params.get("service_level"), "review_days": st.Params.get("review_days"),
			"lead_days": st.Params.get("lead_days")}, nil
	})
}

type reorderPick struct {
	ItemID      string `json:"item_id"`
	WarehouseID string `json:"warehouse_id"`
	MinQty      string `json:"min_qty"`
	MaxQty      string `json:"max_qty"`
	Quantity    string `json:"quantity"`
	SupplierID  string `json:"supplier_id"`
}

// POST /smart/reorder/apply {lines: [{item_id, warehouse_id, min_qty, max_qty, supplier_id}]}: writes the suggested
// minimum / maximum into the reorder rules (the ones replenishment and MRP read).
func (s *Server) smartReorderApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines []reorderPick `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Lines) == 0 || len(in.Lines) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, l := range in.Lines {
		mn, ok1 := money(l.MinQty)
		mx, ok2 := money(l.MaxQty)
		if !isUUID(l.ItemID) || !isUUID(l.WarehouseID) || (l.SupplierID != "" && !isUUID(l.SupplierID)) || !ok1 || !ok2 || mn.IsNegative() || mx.LessThan(mn) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		n := 0
		for _, l := range in.Lines {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM items WHERE id = $1 AND company_id = $3 AND kind = 'stock')
				AND EXISTS (SELECT 1 FROM warehouses WHERE id = $2 AND company_id = $3)
				AND ($4 = '' OR EXISTS (SELECT 1 FROM suppliers WHERE id = nullif($4, '')::uuid AND company_id = $3))`, l.ItemID, l.WarehouseID, cid, l.SupplierID).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, notFound()
			}
			if _, err := tx.Exec(ctx, `INSERT INTO reorder_rules (company_id, item_id, warehouse_id, min_qty, max_qty, supplier_id)
				VALUES ($1, $2, $3, $4::numeric, $5::numeric, nullif($6, '')::uuid)
				ON CONFLICT (item_id, warehouse_id) DO UPDATE SET min_qty = EXCLUDED.min_qty, max_qty = EXCLUDED.max_qty,
				  supplier_id = coalesce(EXCLUDED.supplier_id, reorder_rules.supplier_id)`, cid, l.ItemID, l.WarehouseID, l.MinQty, l.MaxQty, l.SupplierID); err != nil {
				return nil, err
			}
			n++
		}
		return map[string]int{"rules": n}, nil
	})
}

// POST /smart/reorder/purchase {lines: [{item_id, warehouse_id, quantity, supplier_id}]}: one draft purchase order
// (request for quotation) per supplier, priced from the supplier's price list.
func (s *Server) smartReorderPurchase(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines []reorderPick `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Lines) == 0 || len(in.Lines) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, l := range in.Lines {
		q, ok := money(l.Quantity)
		if !isUUID(l.ItemID) || !isUUID(l.WarehouseID) || !isUUID(l.SupplierID) || !ok || !q.IsPositive() {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		bySup := map[string][]poLineIn{}
		order := []string{}
		for _, l := range in.Lines {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM suppliers WHERE id = $1 AND company_id = $2)`, l.SupplierID, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, notFound()
			}
			if _, ok := bySup[l.SupplierID]; !ok {
				order = append(order, l.SupplierID)
			}
			bySup[l.SupplierID] = append(bySup[l.SupplierID], poLineIn{ItemID: l.ItemID, WarehouseID: l.WarehouseID, Quantity: l.Quantity})
		}
		pos := []map[string]any{}
		for _, sup := range order {
			var cur string
			if err := tx.QueryRow(ctx, `SELECT coalesce(currency::text, $2) FROM suppliers WHERE id = $1`, sup, set.Base).Scan(&cur); err != nil {
				return nil, err
			}
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'purchase_order')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO purchase_orders (company_id, number, supplier_id, order_date, currency, notes)
				VALUES ($1, $2, $3, current_date, $4, 'Smart reorder') RETURNING id::text`, cid, n, sup, cur).Scan(&id); err != nil {
				return nil, err
			}
			if err := writePOLines(ctx, tx, cid, id, cur, bySup[sup]); err != nil {
				return nil, err
			}
			pos = append(pos, map[string]any{"id": id, "number": n, "supplier_id": sup, "lines": len(bySup[sup])})
		}
		return map[string]any{"orders": pos}, nil
	})
}

// ======================= collector route =======================

func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371.0
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat, dLng := toRad(lat2-lat1), toRad(lng2-lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * R * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

type geoPt struct{ lat, lng float64 }

// orderRoute: nearest neighbour from the start, then 2-opt until no swap shortens the (open) path.
// Returns the visiting order as indexes into pts.
func orderRoute(start *geoPt, pts []geoPt) []int {
	n := len(pts)
	if n == 0 {
		return nil
	}
	used := make([]bool, n)
	order := make([]int, 0, n)
	cur := geoPt{pts[0].lat, pts[0].lng}
	if start != nil {
		cur = *start
	}
	for len(order) < n {
		best, bd := -1, math.Inf(1)
		for i, p := range pts {
			if used[i] {
				continue
			}
			if d := haversineKm(cur.lat, cur.lng, p.lat, p.lng); d < bd {
				best, bd = i, d
			}
		}
		used[best] = true
		order = append(order, best)
		cur = pts[best]
	}
	dist := func(a, b int) float64 { // -1 = the start point
		pa := start
		if a >= 0 {
			pa = &pts[a]
		}
		if pa == nil {
			return 0
		}
		return haversineKm(pa.lat, pa.lng, pts[b].lat, pts[b].lng)
	}
	prev := func(i int) int {
		if i == 0 {
			if start == nil {
				return -2
			}
			return -1
		}
		return order[i-1]
	}
	for improved, rounds := true, 0; improved && rounds < 50; rounds++ {
		improved = false
		for i := 0; i < n-1; i++ {
			for j := i + 1; j < n; j++ {
				a := prev(i)
				// cost of the edges that change: (a → order[i]) and (order[j] → order[j+1])
				var before, after float64
				if a != -2 {
					before += dist(a, order[i])
					after += dist(a, order[j])
				}
				if j+1 < n {
					before += haversineKm(pts[order[j]].lat, pts[order[j]].lng, pts[order[j+1]].lat, pts[order[j+1]].lng)
					after += haversineKm(pts[order[i]].lat, pts[order[i]].lng, pts[order[j+1]].lat, pts[order[j+1]].lng)
				}
				if after < before-1e-9 {
					for l, r := i, j; l < r; l, r = l+1, r-1 {
						order[l], order[r] = order[r], order[l]
					}
					improved = true
				}
			}
		}
	}
	return order
}

type routeStop struct {
	Seq        int      `json:"seq"`
	CustomerID string   `json:"customer_id"`
	Code       string   `json:"code"`
	Name       string   `json:"name"`
	Phone      string   `json:"phone"`
	Address    string   `json:"address"`
	Area       string   `json:"area_name"`
	Lat        *float64 `json:"lat"`
	Lng        *float64 `json:"lng"`
	DueBase    float64  `json:"due_base"`
	Bills      int      `json:"bills"`
	OldestDue  string   `json:"oldest_due"`
	Score      int      `json:"risk_score"`
	Band       string   `json:"risk_band"`
	LegKm      float64  `json:"leg_km"`
	priority   float64
}

// GET /collect/route?lat=&lng=&area_id=&limit=: today's visits (customers with bills to collect; a collector sees
// their own areas), the ones with a location ordered into a short route, riskiest / biggest first when the list is
// cut; Google Maps directions links for every 10 stops.
func (s *Server) collectRoute(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	area := q.Get("area_id")
	limit := queryInt(r, "limit", 25, 80)
	var start *geoPt
	if q.Get("lat") != "" || q.Get("lng") != "" {
		lat, e1 := strconv.ParseFloat(q.Get("lat"), 64)
		lng, e2 := strconv.ParseFloat(q.Get("lng"), 64)
		if e1 != nil || e2 != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		start = &geoPt{lat, lng}
	}
	if area != "" && !isUUID(area) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `
			SELECT c.id::text, c.code, c.name, c.phone, c.address, coalesce(ar.name, ''), c.lat::float8, c.lng::float8,
			  coalesce(sum(smart_to_base($1, i.currency::text, i.total - i.paid)), 0)::float8, count(i.id)::int, min(i.due_date)::text,
			  coalesce(cr.score, 0), coalesce(cr.band, '')
			FROM customers c JOIN invoices i ON i.customer_id = c.id AND i.status IN ('open', 'partial')
			LEFT JOIN areas ar ON ar.id = c.area_id LEFT JOIN customer_risk cr ON cr.customer_id = c.id
			WHERE c.company_id = $1 AND c.status <> 'closed'
			  AND ($2 = '' OR c.area_id = $2::uuid)
			  AND (NOT $3 OR c.area_id IN (SELECT id FROM areas WHERE collector_id = app_user()))
			GROUP BY c.id, ar.name, cr.score, cr.band
			ORDER BY 9 DESC LIMIT 2000`, cid, area, isCollector(r))
		if err != nil {
			return nil, err
		}
		var with, without []*routeStop
		for rows.Next() {
			st := &routeStop{}
			if err := rows.Scan(&st.CustomerID, &st.Code, &st.Name, &st.Phone, &st.Address, &st.Area, &st.Lat, &st.Lng, &st.DueBase, &st.Bills,
				&st.OldestDue, &st.Score, &st.Band); err != nil {
				rows.Close()
				return nil, err
			}
			st.DueBase = round2(st.DueBase)
			st.priority = st.DueBase * (1 + float64(st.Score)/50)
			if st.Lat != nil && st.Lng != nil {
				with = append(with, st)
			} else {
				without = append(without, st)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		byPriority := func(l []*routeStop) {
			sort.SliceStable(l, func(i, j int) bool { return l[i].priority > l[j].priority })
		}
		byPriority(with)
		byPriority(without)
		if len(with) > limit {
			with = with[:limit]
		}
		pts := make([]geoPt, len(with))
		for i, st := range with {
			pts[i] = geoPt{*st.Lat, *st.Lng}
		}
		ord := orderRoute(start, pts)
		stops := make([]*routeStop, 0, len(ord))
		total := 0.0
		var prev *geoPt = start
		for k, idx := range ord {
			st := with[idx]
			st.Seq = k + 1
			if prev != nil {
				st.LegKm = round2(haversineKm(prev.lat, prev.lng, *st.Lat, *st.Lng))
				total += st.LegKm
			}
			p := pts[idx]
			prev = &p
			stops = append(stops, st)
		}
		// Google Maps directions, 10 stops per link
		links := []string{}
		for i := 0; i < len(stops); i += 10 {
			j := min(i+10, len(stops))
			chunk := stops[i:j]
			v := url.Values{}
			v.Set("api", "1")
			v.Set("travelmode", "driving")
			if i == 0 && start != nil {
				v.Set("origin", fmt.Sprintf("%.6f,%.6f", start.lat, start.lng))
			} else if i > 0 {
				p := stops[i-1]
				v.Set("origin", fmt.Sprintf("%.6f,%.6f", *p.Lat, *p.Lng))
			}
			last := chunk[len(chunk)-1]
			v.Set("destination", fmt.Sprintf("%.6f,%.6f", *last.Lat, *last.Lng))
			var wps []string
			for _, st := range chunk[:len(chunk)-1] {
				wps = append(wps, fmt.Sprintf("%.6f,%.6f", *st.Lat, *st.Lng))
			}
			if len(wps) > 0 {
				v.Set("waypoints", strings.Join(wps, "|"))
			}
			links = append(links, "https://www.google.com/maps/dir/?"+v.Encode())
		}
		if len(without) > 100 {
			without = without[:100]
		}
		if without == nil {
			without = []*routeStop{}
		}
		return map[string]any{"base_currency": base, "stops": stops, "total_km": round2(total), "missing_location": without, "maps_links": links,
			"start": start != nil}, nil
	})
}

// PUT /customers/{id}/location {lat, lng, source: gps|manual}; {lat: null} clears it. A collector may set it only for
// customers of their areas.
func (s *Server) setCustomerLocation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lat    *float64 `json:"lat"`
		Lng    *float64 `json:"lng"`
		Source string   `json:"source"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if (in.Lat == nil) != (in.Lng == nil) || (in.Lat != nil && (*in.Lat < -90 || *in.Lat > 90 || *in.Lng < -180 || *in.Lng > 180 ||
		(*in.Lat == 0 && *in.Lng == 0))) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	if in.Source != "gps" && in.Source != "manual" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var lat, lng *string
		if in.Lat != nil {
			a := decimal.NewFromFloat(*in.Lat).Round(6).String()
			b := decimal.NewFromFloat(*in.Lng).Round(6).String()
			lat, lng = &a, &b
		}
		tag, err := tx.Exec(ctx, `UPDATE customers SET lat = $3::numeric, lng = $4::numeric, geo_at = CASE WHEN $3::text IS NULL THEN NULL ELSE now() END,
			geo_source = CASE WHEN $3::text IS NULL THEN '' ELSE $5 END, updated_at = now()
			WHERE id = $1 AND company_id = $2 AND (NOT $6 OR area_id IN (SELECT id FROM areas WHERE collector_id = app_user()))`,
			id, cid, lat, lng, in.Source, isCollector(r))
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT id, lat::float8 AS lat, lng::float8 AS lng, geo_at, geo_source FROM customers WHERE id = $1`, id)
	})
}
