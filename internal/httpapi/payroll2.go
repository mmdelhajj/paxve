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

// Module U payroll engine: detailed payslips (payslip_lines) computed from the employee file, attendance,
// leave, commissions, advances and the country rules (hr_rules.go); salaries split in two currencies;
// draft → approved → posted → paid.

type empPay struct {
	ID, Name, Cur1, Marital, Nationality string
	Cur2, UserID, Schedule               *string
	Salary1, Salary2, Transport, Housing decimal.Decimal
	Commission, Open1, Inst1             decimal.Decimal
	TransportDaily, Enrolled, TaxExempt  bool
	Track, SpouseWork                    bool
	Children                             int
	Hire                                 time.Time
	End                                  *time.Time
}

const empPaySQL = `SELECT e.id::text, e.name, e.currency::text, e.marital_status, e.nationality, e.currency2::text, e.user_id::text,
	e.schedule_id::text, e.salary::text, e.salary2::text, e.transport::text, e.housing::text, e.commission_pct::text,
	coalesce((SELECT sum(a.amount - a.recovered) FROM employee_advances a WHERE a.employee_id = e.id AND a.currency = e.currency), 0)::text,
	coalesce((SELECT sum(least(a.installment, a.amount - a.recovered)) FROM employee_advances a
	          WHERE a.employee_id = e.id AND a.currency = e.currency AND a.recovered < a.amount), 0)::text,
	e.transport_daily, e.nssf_enrolled, e.tax_exempt, e.track_attendance, e.spouse_working, e.children, e.hire_date, e.end_date
	FROM employees e`

func scanEmpPay(row pgx.Row) (empPay, error) {
	var e empPay
	var s1, s2, tr, ho, co, op, in string
	err := row.Scan(&e.ID, &e.Name, &e.Cur1, &e.Marital, &e.Nationality, &e.Cur2, &e.UserID, &e.Schedule, &s1, &s2, &tr, &ho, &co, &op, &in,
		&e.TransportDaily, &e.Enrolled, &e.TaxExempt, &e.Track, &e.SpouseWork, &e.Children, &e.Hire, &e.End)
	d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
	e.Salary1, e.Salary2, e.Transport, e.Housing, e.Commission, e.Open1, e.Inst1 = d(s1), d(s2), d(tr), d(ho), d(co), d(op), d(in)
	return e, err
}

// periodDays: scheduled working days, paid / unpaid leave days, present days and overtime of an employee in [from, to].
type periodDays struct {
	Scheduled, PaidLeave, UnpaidLeave, Present int
	OvertimeMin                                int
}

func scheduleDays(ctx context.Context, tx pgx.Tx, schedule *string) ([]int32, error) {
	days := []int32{1, 2, 3, 4, 5, 6}
	if schedule != nil {
		if err := tx.QueryRow(ctx, `SELECT work_days FROM work_schedules WHERE id = $1`, *schedule).Scan(&days); err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
	}
	return days, nil
}

func countPeriodDays(ctx context.Context, tx pgx.Tx, cid, emp string, schedule *string, from, to string) (periodDays, error) {
	var p periodDays
	days, err := scheduleDays(ctx, tx, schedule)
	if err != nil {
		return p, err
	}
	err = tx.QueryRow(ctx, `WITH d AS (SELECT g::date AS day FROM generate_series($2::date, $3::date, interval '1 day') g),
		s AS (SELECT day FROM d WHERE extract(isodow FROM day)::int = ANY($4::int[])
		      AND NOT EXISTS (SELECT 1 FROM public_holidays h WHERE h.company_id = $1 AND h.day = d.day))
		SELECT (SELECT count(*) FROM s)::int,
		  (SELECT count(*) FROM s WHERE EXISTS (SELECT 1 FROM leave_requests r JOIN leave_types t ON t.id = r.type_id
		     WHERE r.employee_id = $5 AND r.status = 'approved' AND s.day BETWEEN r.date_from AND r.date_to AND t.paid))::int,
		  (SELECT count(*) FROM s WHERE EXISTS (SELECT 1 FROM leave_requests r JOIN leave_types t ON t.id = r.type_id
		     WHERE r.employee_id = $5 AND r.status = 'approved' AND s.day BETWEEN r.date_from AND r.date_to AND NOT t.paid))::int,
		  (SELECT count(*) FROM s WHERE EXISTS (SELECT 1 FROM attendance a WHERE a.employee_id = $5 AND a.work_date = s.day AND a.check_in IS NOT NULL))::int,
		  (SELECT coalesce(sum(overtime_minutes), 0) FROM attendance a WHERE a.employee_id = $5 AND a.work_date BETWEEN $2::date AND $3::date)::int`,
		cid, from, to, days, emp).Scan(&p.Scheduled, &p.PaidLeave, &p.UnpaidLeave, &p.Present, &p.OvertimeMin)
	return p, err
}

// rateBetween: units of `to` per 1 unit of `from`, through the base currency (official rates on date).
func rateBetween(ctx context.Context, tx pgx.Tx, cid, base, from, to, date string) (decimal.Decimal, error) {
	if from == to {
		return decimal.NewFromInt(1), nil
	}
	rf, err := resolveRate(ctx, tx, cid, base, from, date, "")
	if err != nil {
		return decimal.Zero, err
	}
	rt, err := resolveRate(ctx, tx, cid, base, to, date, "")
	if err != nil {
		return decimal.Zero, err
	}
	f, _ := decimal.NewFromString(rf)
	t, _ := decimal.NewFromString(rt)
	if f.IsZero() {
		return decimal.Zero, &apiError{status: http.StatusUnprocessableEntity, code: "no_rate", detail: from}
	}
	return t.Div(f), nil
}

type slipManual struct {
	Allow, Ded decimal.Decimal
	Recovery   *decimal.Decimal
}

// buildSlip computes and stores the detailed payslip of one employee (insert, or replace a draft's lines).
func buildSlip(ctx context.Context, tx pgx.Tx, cid, base, runID, period string, e empPay, params payrollParams, country string, man slipManual, slipID string) error {
	from, to := period+"-01", periodEnd(period)
	pStart, _ := time.Parse(time.DateOnly, from)
	pEnd, _ := time.Parse(time.DateOnly, to)
	s, en := pStart, pEnd
	if e.Hire.After(s) {
		s = e.Hire
	}
	if e.End != nil && e.End.Before(en) {
		en = *e.End
	}
	if en.Before(s) {
		return nil // not employed this month
	}
	monthLen := pEnd.Day()
	active := decimal.NewFromInt(int64(en.Sub(s).Hours()/24) + 1).Div(decimal.NewFromInt(int64(monthLen)))
	if active.GreaterThan(decimal.NewFromInt(1)) {
		active = decimal.NewFromInt(1)
	}
	days, err := countPeriodDays(ctx, tx, cid, e.ID, e.Schedule, s.Format(time.DateOnly), en.Format(time.DateOnly))
	if err != nil {
		return err
	}
	absent := 0
	if e.Track {
		absent = days.Scheduled - days.Present - days.PaidLeave - days.UnpaidLeave
		if absent < 0 {
			absent = 0
		}
	}
	worked := days.Scheduled - absent - days.PaidLeave - days.UnpaidLeave
	if worked < 0 {
		worked = 0
	}
	params = phasedSocial(params, country, e.Nationality, e.Hire, to)
	in := slipInput{Country: country, Params: params, Cur1: e.Cur1, Salary1: e.Salary1, Transport1: e.Transport, TransportDaily: e.TransportDaily,
		Housing1: e.Housing, Married: e.Marital == "married", SpouseWork: e.SpouseWork, Children: e.Children, Nationality: e.Nationality,
		SocialEnrolled: e.Enrolled, TaxExempt: e.TaxExempt, ActiveFactor: active, WorkedDays: decimal.NewFromInt(int64(worked)),
		AbsentDays: decimal.NewFromInt(int64(absent)), UnpaidDays: decimal.NewFromInt(int64(days.UnpaidLeave)),
		OvertimeHours: decimal.NewFromInt(int64(days.OvertimeMin)).Div(decimal.NewFromInt(60)).Round(2), ManualAllow1: man.Allow, ManualDed1: man.Ded}
	if in.Dec1, err = currencyDecimals(ctx, tx, e.Cur1); err != nil {
		return err
	}
	if e.Cur2 != nil && e.Salary2.IsPositive() && *e.Cur2 != e.Cur1 {
		in.Cur2, in.Salary2 = *e.Cur2, e.Salary2
		if in.Dec2, err = currencyDecimals(ctx, tx, *e.Cur2); err != nil {
			return err
		}
	}
	L := params["legal_currency"]
	if L == "" {
		L = e.Cur1
	}
	if in.RateL1, err = rateBetween(ctx, tx, cid, base, e.Cur1, L, to); err != nil {
		return err
	}
	if in.Cur2 != "" {
		if in.RateL2, err = rateBetween(ctx, tx, cid, base, in.Cur2, L, to); err != nil {
			return err
		}
	}
	// commission on the collections the employee's user received this month (converted to cur1)
	if e.Commission.IsPositive() && e.UserID != nil {
		rows, err := tx.Query(ctx, `SELECT currency::text, sum(amount)::text FROM payments WHERE company_id = $1 AND received_by = $2
			AND status = 'posted' AND kind = 'payment' AND payment_date BETWEEN $3::date AND $4::date GROUP BY 1`, cid, *e.UserID, from, to)
		if err != nil {
			return err
		}
		type cs struct{ cur, amt string }
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cs, error) {
			var x cs
			err := r.Scan(&x.cur, &x.amt)
			return x, err
		})
		if err != nil {
			return err
		}
		total := decimal.Zero
		for _, x := range list {
			a, _ := decimal.NewFromString(x.amt)
			rt, err := rateBetween(ctx, tx, cid, base, x.cur, e.Cur1, to)
			if err != nil {
				return err
			}
			total = total.Add(a.Mul(rt))
		}
		in.Commission1 = total.Mul(e.Commission).Div(pct100)
	}
	// fixed components
	rows, err := tx.Query(ctx, `SELECT kind, name, currency::text, amount::text, taxable, social FROM employee_components WHERE employee_id = $1 ORDER BY created_at`, e.ID)
	if err != nil {
		return err
	}
	comps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (slipComponent, error) {
		var c slipComponent
		var a string
		err := r.Scan(&c.Kind, &c.Name, &c.Currency, &a, &c.Taxable, &c.Social)
		c.Amount, _ = decimal.NewFromString(a)
		return c, err
	})
	if err != nil {
		return err
	}
	in.Components = comps
	// advance recovery: the manual amount, else the instalments due
	if man.Recovery != nil {
		if man.Recovery.GreaterThan(e.Open1) {
			return badRequest("recovery_too_high")
		}
		in.Recovery1 = *man.Recovery
	} else {
		in.Recovery1 = decimal.Min(e.Inst1, e.Open1)
	}
	res, err := computeSlip(in)
	if err != nil {
		if ae, ok := err.(*apiError); ok && ae.detail == "" {
			ae.detail = e.Name
		}
		return err
	}
	var cur2 *string
	if in.Cur2 != "" {
		cur2 = &in.Cur2
	}
	// approved expense claims to reimburse with this salary (in one of the salary currencies)
	claimIDs, err := payrollClaims(ctx, tx, e.ID, slipID, e.Cur1, in.Cur2, &res)
	if err != nil {
		return err
	}
	recovery := res.recovery()
	var manRec *string
	if man.Recovery != nil {
		v := man.Recovery.String()
		manRec = &v
	}
	if slipID == "" {
		err = tx.QueryRow(ctx, `INSERT INTO payslips (run_id, employee_id, currency, base, transport, allowances, deductions, nssf_employee, nssf_employer,
			advance_recovery, net, currency2, base2, allowances2, deductions2, net2, worked_days, absent_days, unpaid_days, overtime_hours, detailed,
			manual_allow, manual_ded, manual_recovery)
			VALUES ($1, $2, $3, $4::numeric, $5::numeric, $6::numeric, $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12,
			$13::numeric, $14::numeric, $15::numeric, $16::numeric, $17, $18, $19, $20::numeric, true, $21::numeric, $22::numeric, $23::numeric) RETURNING id::text`,
			runID, e.ID, e.Cur1, res.Base1.String(), res.Transport1.String(), res.Allow1.String(), res.Ded1.String(), res.SocialEE1.String(),
			res.SocialER1.String(), recovery.String(), res.Net1.String(), cur2, res.Base2.String(), res.Allow2.String(), res.Ded2.String(),
			res.Net2.String(), worked, absent, days.UnpaidLeave, in.OvertimeHours.String(), man.Allow.String(), man.Ded.String(), manRec).Scan(&slipID)
	} else {
		if _, err = tx.Exec(ctx, `DELETE FROM payslip_lines WHERE payslip_id = $1`, slipID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE payslips SET currency = $2, base = $3::numeric, transport = $4::numeric, allowances = $5::numeric,
			deductions = $6::numeric, nssf_employee = $7::numeric, nssf_employer = $8::numeric, advance_recovery = $9::numeric, net = $10::numeric,
			currency2 = $11, base2 = $12::numeric, allowances2 = $13::numeric, deductions2 = $14::numeric, net2 = $15::numeric, worked_days = $16,
			absent_days = $17, unpaid_days = $18, overtime_hours = $19::numeric, detailed = true, manual_allow = $20::numeric, manual_ded = $21::numeric,
			manual_recovery = $22::numeric, updated_at = now() WHERE id = $1`,
			slipID, e.Cur1, res.Base1.String(), res.Transport1.String(), res.Allow1.String(), res.Ded1.String(), res.SocialEE1.String(),
			res.SocialER1.String(), recovery.String(), res.Net1.String(), cur2, res.Base2.String(), res.Allow2.String(), res.Ded2.String(),
			res.Net2.String(), worked, absent, days.UnpaidLeave, in.OvertimeHours.String(), man.Allow.String(), man.Ded.String(), manRec)
	}
	if err != nil {
		return err
	}
	for i, l := range res.Lines {
		if _, err := tx.Exec(ctx, `INSERT INTO payslip_lines (company_id, payslip_id, seq, code, label, kind, role, currency, amount, basis)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10)`, cid, slipID, i+1, l.Code, l.Label, l.Kind, l.Role, l.Currency,
			l.Amount.String(), l.Basis); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE expense_claims SET payslip_id = NULL WHERE payslip_id = $1 AND NOT (id::text = ANY($2::text[]))`, slipID, claimIDs); err != nil {
		return err
	}
	if len(claimIDs) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE expense_claims SET payslip_id = $1 WHERE id::text = ANY($2::text[])`, slipID, claimIDs); err != nil {
			return err
		}
	}
	return nil
}

// buildRun (re)computes every payslip of a draft run.
func buildRun(ctx context.Context, tx pgx.Tx, cid, runID, period string) error {
	base, err := companyBase(ctx, tx, cid)
	if err != nil {
		return err
	}
	params, country, err := loadPayrollParams(ctx, tx, cid, periodEnd(period))
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, empPaySQL+` WHERE e.company_id = $1 AND e.status = 'active' AND e.hire_date <= $2::date
		AND (e.end_date IS NULL OR e.end_date >= $3::date) ORDER BY e.name`, cid, periodEnd(period), period+"-01")
	if err != nil {
		return err
	}
	emps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (empPay, error) { return scanEmpPay(r) })
	if err != nil {
		return err
	}
	for _, e := range emps {
		var slipID string
		var ma, md string
		var mr *string
		err := tx.QueryRow(ctx, `SELECT id::text, manual_allow::text, manual_ded::text, manual_recovery::text FROM payslips WHERE run_id = $1 AND employee_id = $2`,
			runID, e.ID).Scan(&slipID, &ma, &md, &mr)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		man := slipManual{}
		man.Allow, _ = decimal.NewFromString(ma)
		man.Ded, _ = decimal.NewFromString(md)
		if mr != nil {
			v, _ := decimal.NewFromString(*mr)
			man.Recovery = &v
		}
		if err := buildSlip(ctx, tx, cid, base, runID, period, e, params, country, man, slipID); err != nil {
			return err
		}
	}
	return nil
}

func lockDraftRun(ctx context.Context, tx pgx.Tx, cid, id string) (string, string, error) {
	if !isUUID(id) {
		return "", "", notFound()
	}
	var period, status string
	if err := tx.QueryRow(ctx, `SELECT period, status FROM payroll_runs WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&period, &status); err != nil {
		if err == pgx.ErrNoRows {
			return "", "", notFound()
		}
		return "", "", err
	}
	return period, status, nil
}

// POST /payroll/{id}/recalculate: recomputes a draft (attendance, leave, rules or the employee file changed).
func (s *Server) recalcPayrollRun(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		period, status, err := lockDraftRun(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if status != "draft" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if err := buildRun(ctx, tx, cid, id, period); err != nil {
			return nil, err
		}
		return loadRun(ctx, tx, cid, id)
	})
}

// POST /payroll/{id}/approve and /unapprove: an approved payroll is locked (no edits) until posted.
func (s *Server) approvePayrollRun(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	back := strings.HasSuffix(r.URL.Path, "/unapprove")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		_, status, err := lockDraftRun(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if (!back && status != "draft") || (back && status != "approved") {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if back {
			_, err = tx.Exec(ctx, `UPDATE payroll_runs SET status = 'draft', approved_by = NULL, approved_at = NULL WHERE id = $1`, id)
		} else {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM payslips WHERE run_id = $1`, id).Scan(&n); err != nil {
				return nil, err
			}
			if n == 0 {
				return nil, badRequest("payroll_empty")
			}
			_, err = tx.Exec(ctx, `UPDATE payroll_runs SET status = 'approved', approved_by = app_user(), approved_at = now() WHERE id = $1`, id)
		}
		if err != nil {
			return nil, err
		}
		return loadRun(ctx, tx, cid, id)
	})
}

// GET /payroll/{id}/payslips/{pid}: one payslip with its lines (print / WhatsApp).
func (s *Server) getPayslip(w http.ResponseWriter, r *http.Request) {
	cid, id, pid := r.PathValue("cid"), r.PathValue("id"), r.PathValue("pid")
	if !isUUID(id) || !isUUID(pid) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadPayslip(ctx, tx, cid, pid, id, "")
	})
}

// loadPayslip: the payslip (optionally restricted to a run or an employee) with lines, employee and company.
func loadPayslip(ctx context.Context, tx pgx.Tx, cid, pid, runID, empID string) (any, error) {
	slip, err := queryJSONOne(ctx, tx, `SELECT p.id, p.run_id, r.period, r.status AS run_status, p.employee_id, e.code, e.name, e.position, e.department,
		e.nssf_number, e.phone, p.currency, p.base::text AS base, p.transport::text AS transport, p.allowances::text AS allowances,
		p.deductions::text AS deductions, p.nssf_employee::text AS nssf_employee, p.nssf_employer::text AS nssf_employer,
		p.advance_recovery::text AS advance_recovery, p.net::text AS net, p.paid::text AS paid, p.currency2, p.base2::text AS base2,
		p.allowances2::text AS allowances2, p.deductions2::text AS deductions2, p.net2::text AS net2, p.paid2::text AS paid2,
		p.worked_days::text AS worked_days, p.absent_days::text AS absent_days, p.unpaid_days::text AS unpaid_days,
		p.overtime_hours::text AS overtime_hours, p.detailed, p.notes
		FROM payslips p JOIN payroll_runs r ON r.id = p.run_id JOIN employees e ON e.id = p.employee_id
		WHERE p.id = $1 AND r.company_id = $2 AND ($3 = '' OR p.run_id::text = $3) AND ($4 = '' OR p.employee_id::text = $4)
		AND r.status <> 'void'`, pid, cid, runID, empID)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT seq, code, label, kind, role, currency, amount::text AS amount, basis FROM payslip_lines
		WHERE payslip_id = $1 ORDER BY seq`, pid)
	if err != nil {
		return nil, err
	}
	company, err := queryJSONOne(ctx, tx, `SELECT name_en, name_ar, logo FROM companies WHERE id = $1`, cid)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"payslip": slip, "lines": lines, "company": company}, nil
}

// GET /payroll/{id}/wps: UAE Wage Protection System salary information file (SIF, CSV).
func (s *Server) payrollWPS(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var out strings.Builder
	var name string
	s.runRaw(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var period, status, empID, bankCode string
		if err := tx.QueryRow(ctx, `SELECT r.period, r.status, c.wps_employer_id, c.wps_bank_code FROM payroll_runs r JOIN companies c ON c.id = r.company_id
			WHERE r.id = $1 AND r.company_id = $2`, id, cid).Scan(&period, &status, &empID, &bankCode); err != nil {
			if err == pgx.ErrNoRows {
				return notFound()
			}
			return err
		}
		if status == "void" {
			return &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if empID == "" {
			return badRequest("wps_not_setup")
		}
		from, to := period+"-01", periodEnd(period)
		rows, err := tx.Query(ctx, `SELECT e.national_id, e.bank_routing, e.bank_account, p.worked_days::text, p.unpaid_days::text, p.currency::text,
			(p.base + p.transport)::text, (p.allowances)::text, p.net::text
			FROM payslips p JOIN employees e ON e.id = p.employee_id WHERE p.run_id = $1 ORDER BY e.code`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		total := decimal.Zero
		days := strings.TrimPrefix(to[8:], "0")
		for rows.Next() {
			var nid, routing, iban, worked, unpaid, cur, fixed, variable, net string
			if err := rows.Scan(&nid, &routing, &iban, &worked, &unpaid, &cur, &fixed, &variable, &net); err != nil {
				return err
			}
			f, _ := decimal.NewFromString(fixed)
			v, _ := decimal.NewFromString(variable)
			n, _ := decimal.NewFromString(net)
			if v.GreaterThan(n) {
				v = n
			}
			f = n.Sub(v)
			fmt.Fprintf(&out, "EDR,%s,%s,%s,%s,%s,%s,%s,%s,%s\r\n", nid, routing, iban, from, to, days, f.StringFixed(2), v.StringFixed(2),
				strings.TrimSuffix(strings.TrimSuffix(unpaid, ".00"), ".0"))
			count++
			total = total.Add(n)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		now := time.Now()
		fmt.Fprintf(&out, "SCR,%s,%s,%s,%s,%s%s,%d,%s,AED,%s\r\n", empID, bankCode, now.Format(time.DateOnly), now.Format("1504"),
			period[5:7], period[:4], count, total.StringFixed(2), "PAXVE-"+period)
		name = fmt.Sprintf("%s%s.SIF", empID, now.Format("060102150405"))
		return nil
	}, func() {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		_, _ = w.Write([]byte(out.String()))
	})
}

// GET /employees/{id}/end-of-service?date=&reason=resignation|termination
func (s *Server) endOfService(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	date := r.URL.Query().Get("date")
	if date == "" {
		date = today()
	}
	if !isUUID(id) || !validDate(date) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	resign := r.URL.Query().Get("reason") == "resignation"
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var hire time.Time
		var cur, sal string
		if err := tx.QueryRow(ctx, `SELECT hire_date, currency::text, salary::text FROM employees WHERE id = $1 AND company_id = $2`, id, cid).
			Scan(&hire, &cur, &sal); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		params, _, err := loadPayrollParams(ctx, tx, cid, date)
		if err != nil {
			return nil, err
		}
		end, _ := time.Parse(time.DateOnly, date)
		basic, _ := decimal.NewFromString(sal)
		mode := params["gratuity"]
		amount, basis := gratuity(mode, basic, hire, end, resign)
		out := map[string]any{"mode": mode, "currency": cur, "basic": basic.String(), "hire_date": hire.Format(time.DateOnly), "date": date,
			"basis": basis, "amount": amount.Round(2).String()}
		if mode == "nssf" { // Lebanon: contributions already booked for end of service
			var paid string
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(l.amount), 0)::text FROM payslip_lines l JOIN payslips p ON p.id = l.payslip_id
				JOIN payroll_runs r ON r.id = p.run_id WHERE p.employee_id = $1 AND r.status = 'posted' AND l.code = 'eos_er'`, id).Scan(&paid); err != nil {
				return nil, err
			}
			out["contributions"] = paid
		}
		return out, nil
	})
}

// taxPayableAccount: the salary-tax payable account (created on first use).
func taxPayableAccount(ctx context.Context, tx pgx.Tx, cid string) (string, error) {
	var id *string
	if err := tx.QueryRow(ctx, `SELECT income_tax_payable_account_id::text FROM companies WHERE id = $1`, cid).Scan(&id); err != nil {
		return "", err
	}
	if id != nil {
		return *id, nil
	}
	codes, parents := []string{"2240"}, []string{"22", "2"}
	if lebanesePCG(ctx, tx, cid) {
		codes, parents = []string{"4421"}, []string{"442", "44", "4"}
	}
	acc, err := ensureAccount(ctx, tx, cid, codes, "liability", "Salary tax withheld", "ضريبة الرواتب المقتطعة", "Impôt sur salaires retenu", parents)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `UPDATE companies SET income_tax_payable_account_id = $2 WHERE id = $1`, cid, acc)
	return acc, err
}

// payrollGroups: amounts to post per currency and cost center (detailed payslips from their lines,
// older payslips from their columns).
type payGroup struct {
	cur, cc                                     string
	salary, socialExp, socialPay, tax, adv, net decimal.Decimal
	claims                                      decimal.Decimal // expense claims reimbursed with the salary
}

func payrollGroups(ctx context.Context, tx pgx.Tx, runID string) ([]*payGroup, error) {
	groups := map[string]*payGroup{}
	order := []string{}
	get := func(cur, cc string) *payGroup {
		k := cur + "|" + cc
		if g, ok := groups[k]; ok {
			return g
		}
		g := &payGroup{cur: cur, cc: cc}
		groups[k] = g
		order = append(order, k)
		return g
	}
	d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
	// legacy payslips
	rows, err := tx.Query(ctx, `SELECT p.currency::text, coalesce(e.cost_center_id::text, ''),
		sum(p.base + p.transport + p.allowances - p.deductions)::text, sum(p.nssf_employer)::text,
		sum(p.nssf_employee + p.nssf_employer)::text, sum(p.advance_recovery)::text, sum(p.net)::text
		FROM payslips p JOIN employees e ON e.id = p.employee_id WHERE p.run_id = $1 AND NOT p.detailed GROUP BY 1, 2`, runID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cur, cc, ex, er, ns, rc, nt string
		if err := rows.Scan(&cur, &cc, &ex, &er, &ns, &rc, &nt); err != nil {
			rows.Close()
			return nil, err
		}
		g := get(cur, cc)
		g.salary, g.socialExp, g.socialPay = g.salary.Add(d(ex)), g.socialExp.Add(d(er)), g.socialPay.Add(d(ns))
		g.adv, g.net = g.adv.Add(d(rc)), g.net.Add(d(nt))
	}
	rows.Close()
	// detailed payslips: lines
	rows, err = tx.Query(ctx, `SELECT l.currency::text, coalesce(e.cost_center_id::text, ''), l.kind, l.role, sum(l.amount)::text
		FROM payslip_lines l JOIN payslips p ON p.id = l.payslip_id JOIN employees e ON e.id = p.employee_id
		WHERE p.run_id = $1 AND p.detailed GROUP BY 1, 2, 3, 4`, runID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cur, cc, kind, role, amt string
		if err := rows.Scan(&cur, &cc, &kind, &role, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		g, a := get(cur, cc), d(amt)
		switch {
		case kind == "earning" && role == "reimbursement":
			g.claims = g.claims.Add(a)
		case kind == "earning" && role == "allowance_social":
			g.socialPay = g.socialPay.Sub(a) // paid for the social fund: reduces what is owed to it
		case kind == "earning":
			g.salary = g.salary.Add(a)
		case kind == "deduction" && role == "social":
			g.socialPay = g.socialPay.Add(a)
		case kind == "deduction" && role == "tax":
			g.tax = g.tax.Add(a)
		case kind == "deduction" && role == "advance":
			g.adv = g.adv.Add(a)
		case kind == "deduction":
			g.salary = g.salary.Sub(a)
		case kind == "employer":
			g.socialExp = g.socialExp.Add(a)
			g.socialPay = g.socialPay.Add(a)
		}
		switch kind { // employer lines do not change the net
		case "earning":
			g.net = g.net.Add(a)
		case "deduction":
			g.net = g.net.Sub(a)
		}
	}
	rows.Close()
	out := make([]*payGroup, 0, len(order))
	for _, k := range order {
		out = append(out, groups[k])
	}
	return out, rows.Err()
}

// payrollLines turns the groups into journal lines.
func payrollLines(ctx context.Context, tx pgx.Tx, cid string, set *boSettings, groups []*payGroup, period, date string) ([]jline, error) {
	lines := []jline{}
	var taxAcc string
	for _, g := range groups {
		rate, err := resolveRate(ctx, tx, cid, set.Base, g.cur, date, "")
		if err != nil {
			return nil, err
		}
		add := func(acc, desc string, v decimal.Decimal, debit bool, cc string) {
			if v.IsZero() {
				return
			}
			if v.IsNegative() {
				v, debit = v.Neg(), !debit
			}
			l := jline{Account: acc, Desc: desc, Currency: g.cur, Rate: rate, CostCenter: cc}
			if debit {
				l.Debit = v
			} else {
				l.Credit = v
			}
			lines = append(lines, l)
		}
		add(*set.SalaryExp, "Salaries "+period, g.salary, true, g.cc)
		add(*set.NssfExp, "Social security employer "+period, g.socialExp, true, g.cc)
		add(*set.NssfPay, "Social security "+period, g.socialPay, false, "")
		if g.tax.IsPositive() {
			if taxAcc == "" {
				if taxAcc, err = taxPayableAccount(ctx, tx, cid); err != nil {
					return nil, err
				}
			}
			add(taxAcc, "Salary tax "+period, g.tax, false, "")
		}
		if g.claims.IsPositive() {
			acc, err := claimsPayableAccount(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			add(acc, "Expense claims reimbursed "+period, g.claims, true, "")
		}
		add(*set.Advances, "Advances recovered "+period, g.adv, false, "")
		add(*set.SalaryPay, "Net salaries "+period, g.net, false, "")
	}
	return lines, nil
}

// updateDetailedSlip stores the manual parts of a draft detailed payslip and recomputes it.
func updateDetailedSlip(ctx context.Context, tx pgx.Tx, cid, runID, slipID, period string, allow, ded, recovery, notes *string) (any, error) {
	var status, empID, ma, md string
	var mr *string
	if err := tx.QueryRow(ctx, `SELECT r.status, p.employee_id::text, p.manual_allow::text, p.manual_ded::text, p.manual_recovery::text
		FROM payslips p JOIN payroll_runs r ON r.id = p.run_id WHERE p.id = $1 FOR UPDATE OF p`, slipID).Scan(&status, &empID, &ma, &md, &mr); err != nil {
		return nil, err
	}
	if status != "draft" {
		return nil, &apiError{status: http.StatusConflict, code: "posted_locked"}
	}
	pick := func(p *string, cur string) (decimal.Decimal, error) {
		v := cur
		if p != nil {
			v = *p
		}
		d, ok := money(v)
		if !ok || d.IsNegative() {
			return d, badRequest("invalid_amount")
		}
		return d, nil
	}
	var man slipManual
	var err error
	if man.Allow, err = pick(allow, ma); err != nil {
		return nil, err
	}
	if man.Ded, err = pick(ded, md); err != nil {
		return nil, err
	}
	if recovery != nil {
		if strings.TrimSpace(*recovery) != "" {
			v, err := pick(recovery, "0")
			if err != nil {
				return nil, err
			}
			man.Recovery = &v
		}
	} else if mr != nil {
		v, _ := decimal.NewFromString(*mr)
		man.Recovery = &v
	}
	base, err := companyBase(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	params, country, err := loadPayrollParams(ctx, tx, cid, periodEnd(period))
	if err != nil {
		return nil, err
	}
	e, err := scanEmpPay(tx.QueryRow(ctx, empPaySQL+` WHERE e.id = $1`, empID))
	if err != nil {
		return nil, err
	}
	if err := buildSlip(ctx, tx, cid, base, runID, period, e, params, country, man, slipID); err != nil {
		return nil, err
	}
	if notes != nil {
		if _, err := tx.Exec(ctx, `UPDATE payslips SET notes = $2 WHERE id = $1`, slipID, strings.TrimSpace(*notes)); err != nil {
			return nil, err
		}
	}
	return loadRun(ctx, tx, cid, runID)
}
