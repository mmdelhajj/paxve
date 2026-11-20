package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// ---------- back-office settings (HR + assets accounts, NSSF rates) ----------

type boSettings struct {
	*billingSettings
	SalaryExp, NssfExp, SalaryPay, NssfPay, Advances, DepExp, Accum, Gain, Loss *string
	EmpRate, ErRate                                                             decimal.Decimal
}

// default account codes per setting (Lebanon PCG first, then the international chart)
var boDefaults = []struct{ col, codes string }{
	{"salary_expense_account_id", "641,6200"},
	{"nssf_expense_account_id", "645,6200"},
	{"salary_payable_account_id", "421,2300"},
	{"nssf_payable_account_id", "431,2200"},
	{"advances_account_id", "421,2300"},
	{"depreciation_expense_account_id", "68,6600"},
	{"accum_depreciation_account_id", "28,1750"},
	{"asset_gain_account_id", "77,4900"},
	{"asset_loss_account_id", "68,6900"},
}

const boSettingsSQL = `SELECT salary_expense_account_id, nssf_expense_account_id, salary_payable_account_id, nssf_payable_account_id,
	advances_account_id, depreciation_expense_account_id, accum_depreciation_account_id, asset_gain_account_id, asset_loss_account_id,
	trim_scale(nssf_employee_rate)::text AS nssf_employee_rate, trim_scale(nssf_employer_rate)::text AS nssf_employer_rate
	FROM companies WHERE id = $1`

// fillBODefaults points empty settings at the chart's usual accounts (companies created after this module).
func fillBODefaults(ctx context.Context, tx pgx.Tx, cid string) error {
	var missing bool
	if err := tx.QueryRow(ctx, `SELECT salary_expense_account_id IS NULL OR salary_payable_account_id IS NULL
		OR accum_depreciation_account_id IS NULL OR depreciation_expense_account_id IS NULL OR nssf_payable_account_id IS NULL
		OR advances_account_id IS NULL OR asset_gain_account_id IS NULL OR asset_loss_account_id IS NULL OR nssf_expense_account_id IS NULL
		FROM companies WHERE id = $1`, cid).Scan(&missing); err != nil {
		if err == pgx.ErrNoRows {
			return notFound()
		}
		return err
	}
	if !missing {
		return nil
	}
	for _, d := range boDefaults {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE companies SET %[1]s = (SELECT a.id FROM accounts a
			WHERE a.company_id = $1 AND a.code = ANY(string_to_array($2, ',')) AND a.is_postable
			ORDER BY array_position(string_to_array($2, ','), a.code) LIMIT 1)
			WHERE id = $1 AND %[1]s IS NULL`, d.col), cid, d.codes); err != nil {
			return err
		}
	}
	return nil
}

func loadBOSettings(ctx context.Context, tx pgx.Tx, cid string) (*boSettings, error) {
	bs, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if err := fillBODefaults(ctx, tx, cid); err != nil {
		return nil, err
	}
	s := &boSettings{billingSettings: bs}
	var emp, er string
	err = tx.QueryRow(ctx, `SELECT salary_expense_account_id::text, nssf_expense_account_id::text, salary_payable_account_id::text,
		nssf_payable_account_id::text, advances_account_id::text, depreciation_expense_account_id::text, accum_depreciation_account_id::text,
		asset_gain_account_id::text, asset_loss_account_id::text, nssf_employee_rate::text, nssf_employer_rate::text
		FROM companies WHERE id = $1`, cid).Scan(&s.SalaryExp, &s.NssfExp, &s.SalaryPay, &s.NssfPay, &s.Advances, &s.DepExp,
		&s.Accum, &s.Gain, &s.Loss, &emp, &er)
	s.EmpRate, _ = decimal.NewFromString(emp)
	s.ErRate, _ = decimal.NewFromString(er)
	return s, err
}

func need(p *string, what string) error {
	if p == nil {
		return notSetup(what)
	}
	return nil
}

func (s *Server) getBOSettings(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := fillBODefaults(ctx, tx, cid); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, boSettingsSQL, cid)
	})
}

func (s *Server) putBOSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]*string
	if !decodeBody(w, r, &in) {
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		for _, d := range boDefaults {
			v, ok := in[d.col]
			if !ok || v == nil {
				continue
			}
			if *v != "" {
				if !isUUID(*v) {
					return nil, badRequest("bad_request")
				}
				if err := requirePostable(ctx, tx, cid, *v); err != nil {
					return nil, err
				}
			}
			if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE companies SET %s = nullif($2, '')::uuid WHERE id = $1`, d.col), cid, *v); err != nil {
				return nil, err
			}
		}
		for _, k := range []string{"nssf_employee_rate", "nssf_employer_rate"} {
			if v, ok := in[k]; ok && v != nil {
				d, err := decimal.NewFromString(strings.TrimSpace(*v))
				if err != nil || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(100)) {
					return nil, badRequest("bad_request")
				}
				if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE companies SET %s = $2::numeric WHERE id = $1`, k), cid, d.String()); err != nil {
					return nil, err
				}
			}
		}
		return queryJSONOne(ctx, tx, boSettingsSQL, cid)
	})
}

// ---------- employees ----------

const employeeCols = `e.id, e.code, e.name, e.phone, e.position, e.hire_date, e.status, e.currency, e.salary::text AS salary,
	e.transport::text AS transport, e.nssf_enrolled, e.method_id, m.name AS method_name, e.user_id, e.notes, e.created_at, e.cost_center_id,
	coalesce((SELECT sum(a.amount - a.recovered) FROM employee_advances a WHERE a.employee_id = e.id), 0)::text AS advances_open`

const employeeFrom = ` FROM employees e LEFT JOIN payment_methods m ON m.id = e.method_id`

func (s *Server) listEmployees(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+employeeCols+employeeFrom+` WHERE e.company_id = $1 AND ($2 = '' OR e.status = $2)
			ORDER BY e.status, e.name`, r.PathValue("cid"), status)
	})
}

type employeeIn struct {
	Code         *string `json:"code"`
	Name         *string `json:"name"`
	Phone        *string `json:"phone"`
	Position     *string `json:"position"`
	HireDate     *string `json:"hire_date"`
	Status       *string `json:"status"`
	Currency     *string `json:"currency"`
	Salary       *string `json:"salary"`
	Transport    *string `json:"transport"`
	NssfEnrolled *bool   `json:"nssf_enrolled"`
	MethodID     *string `json:"method_id"`
	UserID       *string `json:"user_id"`
	Notes        *string `json:"notes"`
	CostCenterID *string `json:"cost_center_id"`
}

func (in *employeeIn) valid(create bool) bool {
	if create && (in.Name == nil || in.Currency == nil) {
		return false
	}
	okMoney := func(p *string) bool {
		if p == nil || *p == "" {
			return true
		}
		_, ok := money(*p)
		return ok
	}
	return (in.Name == nil || strings.TrimSpace(*in.Name) != "") && (in.Currency == nil || currencyRe.MatchString(*in.Currency)) &&
		(in.HireDate == nil || validDate(*in.HireDate)) && (in.Status == nil || *in.Status == "active" || *in.Status == "inactive") &&
		okMoney(in.Salary) && okMoney(in.Transport) && optUUID(in.MethodID) && optUUID(in.UserID) && optUUID(in.CostCenterID)
}

func (s *Server) saveEmployee(w http.ResponseWriter, r *http.Request) {
	var in employeeIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if !in.valid(create) || (!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[create], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if create {
			code := strings.TrimSpace(deref(in.Code))
			if code == "" {
				var n int64
				if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'employee')`, cid).Scan(&n); err != nil {
					return nil, err
				}
				code = "E" + strconv.FormatInt(n, 10)
			}
			hire := deref(in.HireDate)
			if hire == "" {
				hire = today()
			}
			enrolled := in.NssfEnrolled == nil || *in.NssfEnrolled
			err := tx.QueryRow(ctx, `INSERT INTO employees (company_id, code, name, phone, position, hire_date, currency, salary, transport,
				nssf_enrolled, method_id, user_id, notes) VALUES ($1, $2, $3, $4, $5, $6::date, $7, coalesce(nullif($8, '')::numeric, 0),
				coalesce(nullif($9, '')::numeric, 0), $10, nullif($11, '')::uuid, nullif($12, '')::uuid, $13) RETURNING id::text`,
				cid, code, strings.TrimSpace(*in.Name), strings.TrimSpace(deref(in.Phone)), strings.TrimSpace(deref(in.Position)), hire,
				*in.Currency, deref(in.Salary), deref(in.Transport), enrolled, deref(in.MethodID), deref(in.UserID), deref(in.Notes)).Scan(&id)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "code_taken"}
			}
			if err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE employees SET code = coalesce(nullif(trim($3), ''), code), name = coalesce(trim($4), name),
				phone = coalesce(trim($5), phone), position = coalesce(trim($6), position), hire_date = coalesce($7::date, hire_date),
				status = coalesce($8, status), currency = coalesce($9, currency), salary = coalesce(nullif($10, '')::numeric, salary),
				transport = coalesce(nullif($11, '')::numeric, transport), nssf_enrolled = coalesce($12, nssf_enrolled),
				method_id = CASE WHEN $13::text IS NULL THEN method_id ELSE nullif($13, '')::uuid END,
				user_id = CASE WHEN $14::text IS NULL THEN user_id ELSE nullif($14, '')::uuid END, notes = coalesce($15, notes)
				WHERE id = $1 AND company_id = $2`, id, cid, in.Code, in.Name, in.Phone, in.Position, in.HireDate, in.Status, in.Currency,
				in.Salary, in.Transport, in.NssfEnrolled, in.MethodID, in.UserID, in.Notes)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "code_taken"}
			}
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		if in.CostCenterID != nil {
			if _, err := tx.Exec(ctx, `UPDATE employees SET cost_center_id = nullif($3, '')::uuid WHERE id = $1 AND company_id = $2`,
				id, cid, *in.CostCenterID); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+employeeCols+employeeFrom+` WHERE e.id = $1`, id)
	})
}

func (s *Server) getEmployee(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := queryJSONOne(ctx, tx, `SELECT `+employeeCols+employeeFrom+` WHERE e.id = $1 AND e.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		adv, err := queryJSON(ctx, tx, `SELECT a.id, a.advance_date, a.currency, a.amount::text AS amount, a.installment::text AS installment,
			a.recovered::text AS recovered, (a.amount - a.recovered)::text AS remaining, m.name AS method, a.notes, a.created_at
			FROM employee_advances a JOIN payment_methods m ON m.id = a.method_id WHERE a.employee_id = $1 ORDER BY a.advance_date DESC`, id)
		if err != nil {
			return nil, err
		}
		slips, err := queryJSON(ctx, tx, `SELECT p.id, p.run_id, r.period, r.status AS run_status, p.currency, p.base::text AS base,
			p.net::text AS net, p.paid::text AS paid FROM payslips p JOIN payroll_runs r ON r.id = p.run_id
			WHERE p.employee_id = $1 AND r.status <> 'void' ORDER BY r.period DESC LIMIT 36`, id)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"employee": emp, "advances": adv, "payslips": slips}, nil
	})
}

// createAdvance lends money to an employee (Dr advances / Cr cash box); recovered in later payrolls.
func (s *Server) createAdvance(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date        string `json:"date"`
		Amount      string `json:"amount"`
		Installment string `json:"installment"`
		MethodID    string `json:"method_id"`
		Notes       string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	amt, ok1 := money(in.Amount)
	inst, ok2 := money(in.Installment)
	if !ok2 || inst.IsZero() {
		inst, ok2 = amt, ok1
	}
	cid, emp := r.PathValue("cid"), r.PathValue("id")
	if !validDate(in.Date) || !ok1 || !amt.IsPositive() || !ok2 || !isUUID(in.MethodID) || !isUUID(emp) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBOSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if err := need(set.Advances, "advances"); err != nil {
			return nil, err
		}
		var name, cur string
		if err := tx.QueryRow(ctx, `SELECT name, currency::text FROM employees WHERE id = $1 AND company_id = $2 FOR UPDATE`, emp, cid).
			Scan(&name, &cur); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		methods, err := loadMethods(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		m, ok := methods[in.MethodID]
		if !ok || !m.Active {
			return nil, badRequest("invalid_tender")
		}
		if m.Currency != cur {
			return nil, badRequest("currency_mismatch")
		}
		rate, err := resolveRate(ctx, tx, cid, set.Base, cur, in.Date, "")
		if err != nil {
			return nil, err
		}
		je, err := postJournal(ctx, tx, cid, set.billingSettings, in.Date, "Advance · "+name, "ADV", "advance", []jline{
			{Account: *set.Advances, Desc: name, Currency: cur, Debit: amt, Rate: rate},
			{Account: m.Account, Desc: m.Name, Currency: cur, Credit: amt, Rate: rate},
		})
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO employee_advances (company_id, employee_id, advance_date, currency, amount, installment,
			method_id, journal_entry_id, notes) VALUES ($1, $2, $3::date, $4, $5::numeric, $6::numeric, $7, $8, $9) RETURNING id::text`,
			cid, emp, in.Date, cur, amt.String(), decimal.Min(inst, amt).String(), m.ID, je, strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		return map[string]string{"id": id, "journal_entry_id": je}, nil
	})
}

// ---------- payroll ----------

const runCols = `r.id, r.period, r.status, r.posting_date, r.journal_entry_id, r.void_entry_id,
	trim_scale(r.nssf_employee_rate)::text AS nssf_employee_rate, trim_scale(r.nssf_employer_rate)::text AS nssf_employer_rate,
	r.created_at, r.approved_at, (SELECT full_name FROM users u WHERE u.id = r.approved_by) AS approved_by_name,
	(SELECT count(*) FROM payslips p WHERE p.run_id = r.id) AS employees,
	coalesce((SELECT json_agg(json_build_object('currency', x.currency, 'gross', x.gross::text, 'net', x.net::text, 'paid', x.paid::text,
	  'nssf', x.nssf::text, 'tax', x.tax::text) ORDER BY x.currency)
	  FROM (SELECT y.currency, sum(y.gross) AS gross, sum(y.net) AS net, sum(y.paid) AS paid, sum(y.nssf) AS nssf, sum(y.tax) AS tax FROM (
	          SELECT p.currency, p.base + p.transport + p.allowances AS gross, p.net, p.paid, p.nssf_employee + p.nssf_employer AS nssf, 0 AS tax
	            FROM payslips p WHERE p.run_id = r.id AND NOT p.detailed
	          UNION ALL SELECT p.currency, p.base + p.transport + p.allowances, p.net, p.paid, 0, 0 FROM payslips p WHERE p.run_id = r.id AND p.detailed
	          UNION ALL SELECT p.currency2, p.base2 + p.allowances2, p.net2, p.paid2, 0, 0 FROM payslips p
	            WHERE p.run_id = r.id AND p.detailed AND p.currency2 IS NOT NULL AND p.currency2 <> p.currency
	          UNION ALL SELECT l.currency, 0, 0, 0, CASE WHEN l.role = 'social' THEN l.amount ELSE 0 END, CASE WHEN l.role = 'tax' THEN l.amount ELSE 0 END
	            FROM payslip_lines l JOIN payslips p ON p.id = l.payslip_id WHERE p.run_id = r.id AND l.role IN ('social', 'tax')
	        ) y GROUP BY y.currency) x), '[]'::json) AS totals`

func (s *Server) listPayrollRuns(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+runCols+` FROM payroll_runs r WHERE r.company_id = $1 ORDER BY r.period DESC, r.created_at DESC LIMIT 120`,
			r.PathValue("cid"))
	})
}

func loadRun(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	run, err := queryJSONOne(ctx, tx, `SELECT `+runCols+` FROM payroll_runs r WHERE r.id = $1 AND r.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	slips, err := queryJSON(ctx, tx, `SELECT p.id, p.employee_id, e.code, e.name, e.position, e.nssf_enrolled, p.currency,
		p.base::text AS base, p.transport::text AS transport, p.allowances::text AS allowances, p.deductions::text AS deductions,
		p.nssf_employee::text AS nssf_employee, p.nssf_employer::text AS nssf_employer, p.advance_recovery::text AS advance_recovery,
		p.net::text AS net, p.paid::text AS paid, p.notes, p.detailed, p.currency2, p.base2::text AS base2, p.allowances2::text AS allowances2,
		p.deductions2::text AS deductions2, p.net2::text AS net2, p.paid2::text AS paid2, p.worked_days::text AS worked_days,
		p.absent_days::text AS absent_days, p.unpaid_days::text AS unpaid_days, p.overtime_hours::text AS overtime_hours,
		p.manual_allow::text AS manual_allow, p.manual_ded::text AS manual_ded, p.manual_recovery::text AS manual_recovery,
		coalesce((SELECT sum(l.amount) FROM payslip_lines l WHERE l.payslip_id = p.id AND l.role = 'tax'), 0)::text AS income_tax,
		coalesce((SELECT json_agg(json_build_object('code', l.code, 'label', l.label, 'kind', l.kind, 'role', l.role, 'currency', l.currency,
		  'amount', l.amount::text, 'basis', l.basis) ORDER BY l.seq) FROM payslip_lines l WHERE l.payslip_id = p.id), '[]'::json) AS lines,
		coalesce((SELECT sum(a.amount - a.recovered) FROM employee_advances a WHERE a.employee_id = p.employee_id AND a.currency = p.currency), 0)::text AS advances_open
		FROM payslips p JOIN employees e ON e.id = p.employee_id WHERE p.run_id = $1 ORDER BY e.name`, id)
	if err != nil {
		return nil, err
	}
	pays, err := queryJSON(ctx, tx, `SELECT pp.id, pp.payment_date, m.name AS method, pp.currency, pp.amount::text AS amount, pp.journal_entry_id
		FROM payroll_payments pp JOIN payment_methods m ON m.id = pp.method_id WHERE pp.run_id = $1 ORDER BY pp.created_at`, id)
	if err != nil {
		return nil, err
	}
	company, err := queryJSONOne(ctx, tx, `SELECT name_en, name_ar FROM companies WHERE id = $1`, cid)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"run": run, "payslips": slips, "payments": pays, "company": company}, nil
}

func (s *Server) getPayrollRun(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadRun(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

type slipCalc struct {
	base, transport, allowances, deductions, nssfEmp, nssfEr, recovery, net decimal.Decimal
}

func calcSlip(c slipCalc, enrolled bool, empRate, erRate decimal.Decimal, dec int32, autoRecovery *decimal.Decimal, advancesOpen decimal.Decimal) (slipCalc, error) {
	hundred := decimal.NewFromInt(100)
	if enrolled {
		c.nssfEmp = c.base.Mul(empRate).Div(hundred).Round(dec)
		c.nssfEr = c.base.Mul(erRate).Div(hundred).Round(dec)
	} else {
		c.nssfEmp, c.nssfEr = decimal.Zero, decimal.Zero
	}
	before := c.base.Add(c.transport).Add(c.allowances).Sub(c.deductions).Sub(c.nssfEmp)
	if autoRecovery != nil {
		c.recovery = decimal.Max(decimal.Zero, decimal.Min(*autoRecovery, before))
	}
	if c.recovery.GreaterThan(advancesOpen) {
		return c, badRequest("recovery_too_high")
	}
	c.net = before.Sub(c.recovery)
	if c.net.IsNegative() {
		return c, badRequest("negative_net")
	}
	return c, nil
}

// createPayrollRun prepares a draft payslip for every active employee.
func (s *Server) createPayrollRun(w http.ResponseWriter, r *http.Request) {
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
		set, err := loadBOSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var id string
		err = tx.QueryRow(ctx, `INSERT INTO payroll_runs (company_id, period, nssf_employee_rate, nssf_employer_rate)
			VALUES ($1, $2, $3::numeric, $4::numeric) RETURNING id::text`, cid, in.Period, set.EmpRate.String(), set.ErRate.String()).Scan(&id)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "payroll_exists"}
		}
		if err != nil {
			return nil, err
		}
		// detailed payslips from the employee file, attendance, leave and the country rules (payroll2.go)
		if err := buildRun(ctx, tx, cid, id, in.Period); err != nil {
			return nil, err
		}
		return loadRun(ctx, tx, cid, id)
	})
}

// updatePayslip edits a draft payslip; NSSF and net are always recomputed by the server.
func (s *Server) updatePayslip(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Base            *string `json:"base"`
		Transport       *string `json:"transport"`
		Allowances      *string `json:"allowances"`
		Deductions      *string `json:"deductions"`
		AdvanceRecovery *string `json:"advance_recovery"`
		Notes           *string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, runID, slipID := r.PathValue("cid"), r.PathValue("id"), r.PathValue("pid")
	if !isUUID(runID) || !isUUID(slipID) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var detailed bool
		var period string
		if err := tx.QueryRow(ctx, `SELECT p.detailed, r.period FROM payslips p JOIN payroll_runs r ON r.id = p.run_id
			WHERE p.id = $1 AND p.run_id = $2 AND r.company_id = $3`, slipID, runID, cid).Scan(&detailed, &period); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if detailed { // module U payslip: manual extra allowance / deduction / advance recovery, then recompute
			return updateDetailedSlip(ctx, tx, cid, runID, slipID, period, in.Allowances, in.Deductions, in.AdvanceRecovery, in.Notes)
		}
		var status, cur, empID, empRate, erRate string
		var enrolled bool
		var vals [5]string
		err := tx.QueryRow(ctx, `SELECT r.status, p.currency::text, p.employee_id::text, e.nssf_enrolled, r.nssf_employee_rate::text,
			r.nssf_employer_rate::text, p.base::text, p.transport::text, p.allowances::text, p.deductions::text, p.advance_recovery::text
			FROM payslips p JOIN payroll_runs r ON r.id = p.run_id JOIN employees e ON e.id = p.employee_id
			WHERE p.id = $1 AND p.run_id = $2 AND r.company_id = $3 FOR UPDATE OF p`, slipID, runID, cid).
			Scan(&status, &cur, &empID, &enrolled, &empRate, &erRate, &vals[0], &vals[1], &vals[2], &vals[3], &vals[4])
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
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
			if !ok {
				return d, badRequest("invalid_amount")
			}
			return d, nil
		}
		var c slipCalc
		fields := []*decimal.Decimal{&c.base, &c.transport, &c.allowances, &c.deductions, &c.recovery}
		inputs := []*string{in.Base, in.Transport, in.Allowances, in.Deductions, in.AdvanceRecovery}
		for i := range fields {
			if *fields[i], err = pick(inputs[i], vals[i]); err != nil {
				return nil, err
			}
		}
		var openS string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount - recovered), 0)::text FROM employee_advances WHERE employee_id = $1 AND currency = $2`,
			empID, cur).Scan(&openS); err != nil {
			return nil, err
		}
		open, _ := decimal.NewFromString(openS)
		dec, err := currencyDecimals(ctx, tx, cur)
		if err != nil {
			return nil, err
		}
		er, _ := decimal.NewFromString(erRate)
		em, _ := decimal.NewFromString(empRate)
		if c, err = calcSlip(c, enrolled, em, er, dec, nil, open); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE payslips SET base = $2::numeric, transport = $3::numeric, allowances = $4::numeric,
			deductions = $5::numeric, nssf_employee = $6::numeric, nssf_employer = $7::numeric, advance_recovery = $8::numeric,
			net = $9::numeric, notes = coalesce($10, notes) WHERE id = $1`, slipID, c.base.String(), c.transport.String(),
			c.allowances.String(), c.deductions.String(), c.nssfEmp.String(), c.nssfEr.String(), c.recovery.String(), c.net.String(),
			in.Notes); err != nil {
			return nil, err
		}
		return loadRun(ctx, tx, cid, runID)
	})
}

func (s *Server) deletePayrollRun(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM payroll_runs WHERE id = $1 AND company_id = $2 AND status = 'draft'`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return map[string]bool{"ok": true}, nil
	})
}

// postPayrollRun books the payroll: Dr salaries (+ employer NSSF) / Cr NSSF payable, advances, salaries payable.
func (s *Server) postPayrollRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date string `json:"date"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.Date != "" && !validDate(in.Date)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBOSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		for _, p := range []struct {
			v    *string
			what string
		}{{set.SalaryExp, "salary_expense"}, {set.NssfExp, "nssf_expense"}, {set.SalaryPay, "salary_payable"},
			{set.NssfPay, "nssf_payable"}, {set.Advances, "advances"}} {
			if err := need(p.v, p.what); err != nil {
				return nil, err
			}
		}
		var period, status string
		if err := tx.QueryRow(ctx, `SELECT period, status FROM payroll_runs WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&period, &status); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if status != "draft" && status != "approved" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		date := in.Date
		if date == "" {
			// audit B-M6: by default post on today's date (company time zone) when today falls inside the payroll
			// month, so this month's P&L and owner summary show the salaries at once; the company can choose to
			// always post on the period end instead. A month already over posts on its last day.
			var mode, today string
			if err := tx.QueryRow(ctx, `SELECT payroll_post_date, current_date::text FROM companies WHERE id = $1`, cid).Scan(&mode, &today); err != nil {
				return nil, err
			}
			date = periodEnd(period)
			if mode != "period_end" && today >= period+"-01" && today <= date {
				date = today
			}
		}
		// one group per currency and cost center: salary costs carry the employee's cost center
		groups, err := payrollGroups(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if len(groups) == 0 {
			return nil, badRequest("payroll_empty")
		}
		d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
		lines, err := payrollLines(ctx, tx, cid, set, groups, period, date)
		if err != nil {
			return nil, err
		}
		je, err := postJournal(ctx, tx, cid, set.billingSettings, date, "Payroll "+period, "PAYROLL-"+period, "payroll", lines)
		if err != nil {
			return nil, err
		}
		// apply recoveries to the employees' open advances, oldest first
		type rec struct{ emp, cur, amount string }
		rrows, err := tx.Query(ctx, `SELECT employee_id::text, currency::text, advance_recovery::text FROM payslips WHERE run_id = $1 AND advance_recovery > 0`, id)
		if err != nil {
			return nil, err
		}
		recs, err := pgx.CollectRows(rrows, func(row pgx.CollectableRow) (rec, error) {
			var x rec
			err := row.Scan(&x.emp, &x.cur, &x.amount)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		for _, x := range recs {
			if err := moveAdvances(ctx, tx, x.emp, x.cur, d(x.amount)); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE payroll_runs SET status = 'posted', journal_entry_id = $2, posting_date = $3::date, posted_by = app_user(),
			approved_by = coalesce(approved_by, app_user()), approved_at = coalesce(approved_at, now())
			WHERE id = $1`, id, je, date); err != nil {
			return nil, err
		}
		return loadRun(ctx, tx, cid, id)
	})
}

// moveAdvances recovers (amount > 0, oldest first) or un-recovers (amount < 0, newest first) employee advances.
func moveAdvances(ctx context.Context, tx pgx.Tx, emp, cur string, amount decimal.Decimal) error {
	order := "advance_date, created_at"
	if amount.IsNegative() {
		order = "advance_date DESC, created_at DESC"
	}
	rows, err := tx.Query(ctx, `SELECT id::text, amount::text, recovered::text FROM employee_advances
		WHERE employee_id = $1 AND currency = $2 ORDER BY `+order+` FOR UPDATE`, emp, cur)
	if err != nil {
		return err
	}
	type adv struct {
		id                string
		amount, recovered decimal.Decimal
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adv, error) {
		var a adv
		var am, rc string
		err := row.Scan(&a.id, &am, &rc)
		a.amount, _ = decimal.NewFromString(am)
		a.recovered, _ = decimal.NewFromString(rc)
		return a, err
	})
	if err != nil {
		return err
	}
	left := amount.Abs()
	for _, a := range list {
		if !left.IsPositive() {
			break
		}
		var step decimal.Decimal
		if amount.IsPositive() {
			step = decimal.Min(left, a.amount.Sub(a.recovered))
		} else {
			step = decimal.Min(left, a.recovered).Neg()
		}
		if step.IsZero() {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE employee_advances SET recovered = recovered + $2::numeric WHERE id = $1`, a.id, step.String()); err != nil {
			return err
		}
		left = left.Sub(step.Abs())
	}
	if left.IsPositive() {
		return badRequest("recovery_too_high")
	}
	return nil
}

// payPayrollRun pays the unpaid net salaries in the method's currency (Dr salaries payable / Cr cash box).
func (s *Server) payPayrollRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MethodID   string   `json:"method_id"`
		Date       string   `json:"date"`
		PayslipIDs []string `json:"payslip_ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !isUUID(in.MethodID) || !validDate(in.Date) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, p := range in.PayslipIDs {
		if !isUUID(p) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBOSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if err := need(set.SalaryPay, "salary_payable"); err != nil {
			return nil, err
		}
		var period, status string
		if err := tx.QueryRow(ctx, `SELECT period, status FROM payroll_runs WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&period, &status); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if status != "posted" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		methods, err := loadMethods(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		m, ok := methods[in.MethodID]
		if !ok || !m.Active {
			return nil, badRequest("invalid_tender")
		}
		var totalS string
		var count int
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(CASE WHEN currency = $2 THEN net - paid ELSE 0 END)
			  + sum(CASE WHEN currency2 = $2 AND currency2 <> currency THEN net2 - paid2 ELSE 0 END), 0)::text,
			count(*) FILTER (WHERE (currency = $2 AND paid < net) OR (currency2 = $2 AND currency2 <> currency AND paid2 < net2))::int FROM payslips
			WHERE run_id = $1 AND (coalesce(cardinality($3::uuid[]), 0) = 0 OR id = ANY($3::uuid[]))`,
			id, m.Currency, in.PayslipIDs).Scan(&totalS, &count); err != nil {
			return nil, err
		}
		total, _ := decimal.NewFromString(totalS)
		if !total.IsPositive() {
			return nil, badRequest("nothing_to_pay")
		}
		rate, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, in.Date, "")
		if err != nil {
			return nil, err
		}
		je, err := postJournal(ctx, tx, cid, set.billingSettings, in.Date, fmt.Sprintf("Salaries paid %s (%d)", period, count),
			"PAYROLL-"+period, "payroll_payment", []jline{
				{Account: *set.SalaryPay, Desc: "Salaries " + period, Currency: m.Currency, Debit: total, Rate: rate},
				{Account: m.Account, Desc: m.Name, Currency: m.Currency, Credit: total, Rate: rate},
			})
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE payslips SET paid = net WHERE run_id = $1 AND currency = $2 AND paid < net
			AND (coalesce(cardinality($3::uuid[]), 0) = 0 OR id = ANY($3::uuid[]))`, id, m.Currency, in.PayslipIDs); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE payslips SET paid2 = net2 WHERE run_id = $1 AND currency2 = $2 AND currency2 <> currency AND paid2 < net2
			AND (coalesce(cardinality($3::uuid[]), 0) = 0 OR id = ANY($3::uuid[]))`, id, m.Currency, in.PayslipIDs); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO payroll_payments (company_id, run_id, payment_date, method_id, currency, amount, journal_entry_id)
			VALUES ($1, $2, $3::date, $4, $5, $6::numeric, $7)`, cid, id, in.Date, m.ID, m.Currency, total.String(), je); err != nil {
			return nil, err
		}
		return loadRun(ctx, tx, cid, id)
	})
}

// voidPayrollRun reverses a posted payroll that has not been paid yet, and gives back the advance recoveries.
func (s *Server) voidPayrollRun(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var period, status string
		var je *string
		if err := tx.QueryRow(ctx, `SELECT period, status, journal_entry_id::text FROM payroll_runs WHERE id = $1 AND company_id = $2 FOR UPDATE`,
			id, cid).Scan(&period, &status, &je); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if status != "posted" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		var paid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payroll_payments WHERE run_id = $1)`, id).Scan(&paid); err != nil {
			return nil, err
		}
		if paid {
			return nil, &apiError{status: http.StatusConflict, code: "payroll_paid"}
		}
		rows, err := tx.Query(ctx, `SELECT employee_id::text, currency::text, advance_recovery::text FROM payslips WHERE run_id = $1 AND advance_recovery > 0`, id)
		if err != nil {
			return nil, err
		}
		type rec struct{ emp, cur, amount string }
		recs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (rec, error) {
			var x rec
			err := row.Scan(&x.emp, &x.cur, &x.amount)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		for _, x := range recs {
			a, _ := decimal.NewFromString(x.amount)
			if err := moveAdvances(ctx, tx, x.emp, x.cur, a.Neg()); err != nil {
				return nil, err
			}
		}
		var rev string
		if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`, *je, "Void payroll "+period).Scan(&rev); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE payroll_runs SET status = 'void', void_entry_id = $2 WHERE id = $1`, id, rev); err != nil {
			return nil, err
		}
		return loadRun(ctx, tx, cid, id)
	})
}

// addMonths moves a date by n months, keeping the day of month but clamping to the month's last day.
func addMonths(d time.Time, n int, day int) time.Time {
	y, m := d.Year(), int(d.Month())-1+n
	y += m / 12
	m = m % 12
	if m < 0 {
		m += 12
		y--
	}
	first := time.Date(y, time.Month(m+1), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	if day > last {
		day = last
	}
	return time.Date(y, time.Month(m+1), day, 0, 0, 0, 0, time.UTC)
}
