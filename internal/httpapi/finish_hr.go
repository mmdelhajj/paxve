package httpapi

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// HR finishing stage: employee expense claims (self-service with a receipt photo → approval → booked → reimbursed in
// cash or with the next salary), rotating shifts and roster overrides, leave accrual history, clock-in photo and
// geofence, phased GOSI rates, and the ZKTeco "ADMS / iclock" push protocol (the fingerprint machine sends its punches
// to https://paxve.com/iclock/... by itself).

// ================= expense claims =================

const claimCols = `c.id, c.number, c.employee_id, e.code AS employee_code, e.name AS employee_name, c.claim_date, c.category, c.description,
	c.currency, c.amount::text AS amount, c.account_id, (SELECT a.code || ' · ' || a.name FROM accounts a WHERE a.id = c.account_id) AS account,
	c.pay_via, c.decision_note, c.decided_at, c.journal_entry_id, c.paid_entry_id, c.paid_at, c.created_at, c.payslip_id,
	CASE WHEN c.status = 'approved' AND c.pay_via = 'payroll' AND EXISTS (SELECT 1 FROM payslips p JOIN payroll_runs pr ON pr.id = p.run_id
	  WHERE p.id = c.payslip_id AND pr.status = 'posted') THEN 'paid' ELSE c.status END AS status,
	(SELECT pr.period FROM payslips p JOIN payroll_runs pr ON pr.id = p.run_id WHERE p.id = c.payslip_id AND pr.status <> 'void') AS payroll_period,
	(SELECT count(*) FROM attachments x WHERE x.entity_type = 'expense_claim' AND x.entity_id = c.id AND x.deleted_at IS NULL) AS attachments`

const claimFrom = ` FROM expense_claims c JOIN employees e ON e.id = c.employee_id`

var claimCategories = map[string]bool{"fuel": true, "transport": true, "meals": true, "lodging": true, "phone": true, "supplies": true,
	"repairs": true, "other": true}

type claimIn struct {
	EmployeeID  string `json:"employee_id"`
	Date        string `json:"date"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Currency    string `json:"currency"`
	Amount      string `json:"amount"`
	PayVia      string `json:"pay_via"`
	Filename    string `json:"filename"` // optional receipt (photo / PDF), base64 in data
	Data        string `json:"data"`
}

func (in *claimIn) valid(withEmp bool) bool {
	if in.Date == "" {
		in.Date = today()
	}
	if in.Category == "" {
		in.Category = "other"
	}
	if in.PayVia == "" {
		in.PayVia = "cash"
	}
	a, ok := money(in.Amount)
	return ok && a.IsPositive() && validDate(in.Date) && claimCategories[in.Category] && strings.TrimSpace(in.Description) != "" &&
		len(in.Description) <= 500 && currencyRe.MatchString(in.Currency) && (in.PayVia == "cash" || in.PayVia == "payroll") &&
		(!withEmp || isUUID(in.EmployeeID)) && len(in.Filename) <= 200
}

func insertClaim(ctx context.Context, tx pgx.Tx, cid, emp string, in claimIn) (json any, err error) {
	var n int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'expense_claim')`, cid).Scan(&n); err != nil {
		return nil, err
	}
	a, _ := money(in.Amount)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO expense_claims (company_id, number, employee_id, claim_date, category, description, currency, amount, pay_via)
		VALUES ($1, $2, $3, $4::date, $5, $6, $7, $8::numeric, $9) RETURNING id::text`, cid, n, emp, in.Date, in.Category, strings.TrimSpace(in.Description),
		in.Currency, a.String(), in.PayVia).Scan(&id); err != nil {
		return nil, err
	}
	if in.Data != "" {
		data, err := base64.StdEncoding.DecodeString(stripDataURL(in.Data))
		if err != nil || len(data) == 0 || len(data) > 10<<20 {
			return nil, badRequest("invalid_file")
		}
		name := strings.TrimSpace(in.Filename)
		if name == "" {
			name = fmt.Sprintf("claim-%d", n)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO attachments (company_id, entity_type, entity_id, filename, mime, size, data)
			VALUES ($1, 'expense_claim', $2, $3, $4, $5, $6)`, cid, id, name, http.DetectContentType(data), len(data), data); err != nil {
			return nil, err
		}
	}
	return queryJSONOne(ctx, tx, `SELECT `+claimCols+claimFrom+` WHERE c.id = $1`, id)
}

func stripDataURL(s string) string {
	if i := strings.Index(s, ";base64,"); i >= 0 && strings.HasPrefix(s, "data:") {
		return s[i+8:]
	}
	return s
}

// POST /companies/{cid}/self/claims (the employee) · POST /companies/{cid}/hr/claims {employee_id, ...} (HR)
func (s *Server) selfClaim(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 15<<20)
	var in claimIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid(false) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, me := r.PathValue("cid"), principal(r).ID
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := selfEmployee(ctx, tx, cid, me)
		if err != nil {
			return nil, err
		}
		return insertClaim(ctx, tx, cid, emp, in)
	})
}

func (s *Server) createClaim(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 15<<20)
	var in claimIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid(true) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM employees WHERE id = $1 AND company_id = $2)`, in.EmployeeID, cid).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, badRequest("invalid_employee")
		}
		return insertClaim(ctx, tx, cid, in.EmployeeID, in)
	})
}

// GET /companies/{cid}/self/claims
func (s *Server) selfClaims(w http.ResponseWriter, r *http.Request) {
	cid, me := r.PathValue("cid"), principal(r).ID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := selfEmployee(ctx, tx, cid, me)
		if err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT `+claimCols+claimFrom+` WHERE c.employee_id = $1 ORDER BY c.claim_date DESC, c.number DESC LIMIT 200`, emp)
	})
}

// GET /companies/{cid}/hr/claims?status=&employee_id= (HR sees all; a manager sees the team's)
func (s *Server) listClaims(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, emp := q.Get("status"), q.Get("employee_id")
	if emp != "" && !isUUID(emp) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT * FROM (SELECT `+claimCols+claimFrom+` WHERE c.company_id = $1 AND ($3 = '' OR c.employee_id = nullif($3, '')::uuid)) x
			WHERE ($2 = '' OR x.status = $2) ORDER BY x.claim_date DESC, x.number DESC LIMIT 2000`, r.PathValue("cid"), st, emp)
	})
}

// claimsPayableAccount: what the company owes employees for their expenses (created on first use).
func claimsPayableAccount(ctx context.Context, tx pgx.Tx, cid string) (string, error) {
	var id *string
	if err := tx.QueryRow(ctx, `SELECT claims_payable_account_id::text FROM companies WHERE id = $1`, cid).Scan(&id); err != nil {
		return "", err
	}
	if id != nil {
		return *id, nil
	}
	codes, parents := []string{"2260"}, []string{"22", "2"}
	if lebanesePCG(ctx, tx, cid) {
		codes, parents = []string{"4286"}, []string{"428", "42", "4"}
	}
	acc, err := ensureAccount(ctx, tx, cid, codes, "liability", "Employee expense claims payable", "مصاريف الموظفين المستحقة", "Notes de frais à rembourser", parents)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `UPDATE companies SET claims_payable_account_id = $2 WHERE id = $1`, cid, acc)
	return acc, err
}

// POST /companies/{cid}/hr/claims/{id}/decide {decision: approve|reject, account_id, pay_via, note}: approving books
// Dr expense / Cr claims payable on the claim date (payroll approvers, or the employee's manager; never one's own).
func (s *Server) decideClaim(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Decision  string `json:"decision"`
		AccountID string `json:"account_id"`
		PayVia    string `json:"pay_via"`
		Note      string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.Decision != "approve" && in.Decision != "reject") || (in.AccountID != "" && !isUUID(in.AccountID)) ||
		(in.PayVia != "" && in.PayVia != "cash" && in.PayVia != "payroll") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	approver := can(r, "payroll.approve")
	me := principal(r).ID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, cur, amt, date, desc, via string
		var number int64
		var mgrUser, empUser, acc *string
		err := tx.QueryRow(ctx, `SELECT c.status, c.currency, c.amount::text, c.claim_date::text, c.description, c.number, c.pay_via, c.account_id::text,
			(SELECT m.user_id::text FROM employees m WHERE m.id = e.manager_id), e.user_id::text
			FROM expense_claims c JOIN employees e ON e.id = c.employee_id WHERE c.id = $1 AND c.company_id = $2 FOR UPDATE OF c`, id, cid).
			Scan(&status, &cur, &amt, &date, &desc, &number, &via, &acc, &mgrUser, &empUser)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if !approver && (mgrUser == nil || *mgrUser != me) {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		if empUser != nil && *empUser == me {
			return nil, &apiError{status: http.StatusForbidden, code: "self_approval"}
		}
		if status != "pending" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.Decision == "reject" {
			_, err := tx.Exec(ctx, `UPDATE expense_claims SET status = 'rejected', decided_by = app_user(), decided_at = now(), decision_note = $2 WHERE id = $1`,
				id, strings.TrimSpace(in.Note))
			if err != nil {
				return nil, err
			}
			return queryJSONOne(ctx, tx, `SELECT `+claimCols+claimFrom+` WHERE c.id = $1`, id)
		}
		account := in.AccountID
		if account == "" && acc != nil {
			account = *acc
		}
		if account == "" {
			ps, err := loadPurchSettings(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			if ps.Expense == nil {
				return nil, notSetup("expense")
			}
			account = *ps.Expense
		}
		if err := requirePostable(ctx, tx, cid, account); err != nil {
			return nil, err
		}
		if in.PayVia != "" {
			via = in.PayVia
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		pay, err := claimsPayableAccount(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rate, err := resolveRate(ctx, tx, cid, set.Base, cur, date, "")
		if err != nil {
			return nil, err
		}
		a, _ := decimal.NewFromString(amt)
		memo := fmt.Sprintf("Expense claim #%d · %s", number, desc)
		je, err := postJournal(ctx, tx, cid, set, date, memo, fmt.Sprintf("CLM-%d", number), "expense_claim", []jline{
			{Account: account, Desc: memo, Currency: cur, Debit: a, Rate: rate},
			{Account: pay, Desc: memo, Currency: cur, Credit: a, Rate: rate},
		})
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE expense_claims SET status = 'approved', account_id = $2, pay_via = $3, decided_by = app_user(), decided_at = now(),
			decision_note = $4, journal_entry_id = $5 WHERE id = $1`, id, account, via, strings.TrimSpace(in.Note), je); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+claimCols+claimFrom+` WHERE c.id = $1`, id)
	})
}

// POST /companies/{cid}/hr/claims/{id}/pay {method_id, date}: reimburses an approved claim from a cash box / bank.
func (s *Server) payClaim(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MethodID string `json:"method_id"`
		Date     string `json:"date"`
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
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, cur, amt, desc, via string
		var number int64
		var slip *string
		err := tx.QueryRow(ctx, `SELECT status, currency, amount::text, description, number, pay_via, payslip_id::text FROM expense_claims
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&status, &cur, &amt, &desc, &number, &via, &slip)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "approved" || slip != nil {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		methods, err := loadMethods(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		m, ok := methods[in.MethodID]
		if !ok || !m.Active {
			return nil, badRequest("invalid_method")
		}
		if m.Currency != cur {
			return nil, badRequest("currency_mismatch")
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		pay, err := claimsPayableAccount(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rate, err := resolveRate(ctx, tx, cid, set.Base, cur, in.Date, "")
		if err != nil {
			return nil, err
		}
		a, _ := decimal.NewFromString(amt)
		memo := fmt.Sprintf("Expense claim #%d reimbursed · %s", number, desc)
		je, err := postJournal(ctx, tx, cid, set, in.Date, memo, fmt.Sprintf("CLM-%d-P", number), "expense_claim", []jline{
			{Account: pay, Desc: memo, Currency: cur, Debit: a, Rate: rate},
			{Account: m.Account, Desc: memo, Currency: cur, Credit: a, Rate: rate},
		})
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE expense_claims SET status = 'paid', pay_via = 'cash', paid_method_id = $2, paid_entry_id = $3, paid_at = now()
			WHERE id = $1`, id, in.MethodID, je); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+claimCols+claimFrom+` WHERE c.id = $1`, id)
	})
}

// POST /companies/{cid}/hr/claims/{id}/cancel (pending only) · POST /companies/{cid}/self/claims/{id}/cancel
func (s *Server) cancelClaim(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	self := strings.Contains(r.Pattern, "/self/")
	me := principal(r).ID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		emp := ""
		if self {
			e, err := selfEmployee(ctx, tx, cid, me)
			if err != nil {
				return nil, err
			}
			emp = e
		}
		tag, err := tx.Exec(ctx, `UPDATE expense_claims SET status = 'cancelled' WHERE id = $1 AND company_id = $2 AND status = 'pending'
			AND ($3 = '' OR employee_id = nullif($3, '')::uuid)`, id, cid, emp)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return map[string]string{"id": id, "status": "cancelled"}, nil
	})
}

// payrollClaims adds approved "pay with salary" claims of the employee to the payslip (earning lines, role
// reimbursement: not taxed, not social, booked against claims payable) and returns their ids.
func payrollClaims(ctx context.Context, tx pgx.Tx, emp, slipID, cur1, cur2 string, res *slipResult) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT c.id::text, c.number, c.currency::text, c.amount::text, c.description FROM expense_claims c
		WHERE c.employee_id = $1 AND c.status = 'approved' AND c.pay_via = 'payroll' AND c.currency IN ($3, $4)
		  AND (c.payslip_id IS NULL OR c.payslip_id::text = $2 OR NOT EXISTS (SELECT 1 FROM payslips p JOIN payroll_runs pr ON pr.id = p.run_id
		       WHERE p.id = c.payslip_id AND pr.status <> 'void'))
		ORDER BY c.number`, emp, slipID, cur1, defaultStr(cur2, cur1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id, cur, amt, desc string
		var n int64
		if err := rows.Scan(&id, &n, &cur, &amt, &desc); err != nil {
			return nil, err
		}
		a, _ := decimal.NewFromString(amt)
		res.Lines = append(res.Lines, slipLine{Code: "claim", Label: fmt.Sprintf("Expense claim #%d · %s", n, desc), Kind: "earning",
			Role: "reimbursement", Currency: cur, Amount: a})
		if cur == cur1 { // stored with the allowances (the payslip checks net = base + allowances − …); not taxed: tax was computed before
			res.Net1, res.Allow1 = res.Net1.Add(a), res.Allow1.Add(a)
		} else {
			res.Net2, res.Allow2 = res.Net2.Add(a), res.Allow2.Add(a)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// phasedSocial: GOSI annuities of Saudis who joined after the phase-in date rise by a step every July.
func phasedSocial(p payrollParams, country, nationality string, hire time.Time, date string) payrollParams {
	from := strings.TrimSpace(p["social_phased_from"])
	if from == "" || hire.Format(time.DateOnly) < from {
		return p
	}
	if nat := strings.TrimSpace(p["social_nationals"]); nat != "" && !strings.Contains(","+nat+",", ","+nationality+",") {
		return p
	}
	step := decimal.Zero
	for _, s := range strings.Split(p["social_phased_steps"], ",") {
		d, v, ok := strings.Cut(strings.TrimSpace(s), ":")
		if !ok || d > date {
			continue
		}
		if x, err := decimal.NewFromString(strings.TrimSpace(v)); err == nil {
			step = x
		}
	}
	if step.IsZero() {
		return p
	}
	out := payrollParams{}
	for k, v := range p {
		out[k] = v
	}
	out["social_ee_rate"] = p.dec("social_ee_rate").Add(step).String()
	out["social_er_rate"] = p.dec("social_er_rate").Add(step).String()
	_ = country
	return out
}

// ================= rotating shifts and roster =================

// scheduleFor: the schedule an employee works on day: a roster override, else the rotation of that day, else the
// employee's schedule. off = a rostered day off.
func scheduleFor(ctx context.Context, tx pgx.Tx, emp, day string) (string, bool, error) {
	var sched *string
	err := tx.QueryRow(ctx, `SELECT schedule_id::text FROM employee_shifts WHERE employee_id = $1 AND work_date = $2::date`, emp, day).Scan(&sched)
	if err == nil {
		if sched == nil {
			return "", true, nil
		}
		return *sched, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	var def, start *string
	var pattern []string
	if err := tx.QueryRow(ctx, `SELECT e.schedule_id::text, e.rotation_start::text, coalesce(r.pattern, '{}') FROM employees e
		LEFT JOIN shift_rotations r ON r.id = e.rotation_id WHERE e.id = $1`, emp).Scan(&def, &start, &pattern); err != nil {
		return "", false, err
	}
	if len(pattern) > 0 && start != nil {
		s, _ := time.Parse(time.DateOnly, *start)
		d, _ := time.Parse(time.DateOnly, day)
		if !d.Before(s) {
			i := int(d.Sub(s).Hours()/24) % len(pattern)
			if pattern[i] == "off" {
				return "", true, nil
			}
			return pattern[i], false, nil
		}
	}
	if def != nil {
		return *def, false, nil
	}
	return "", false, nil
}

func (s *Server) listRotations(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT r.id, r.name, r.pattern, (SELECT count(*) FROM employees e WHERE e.rotation_id = r.id) AS employees
			FROM shift_rotations r WHERE r.company_id = $1 ORDER BY r.name`, r.PathValue("cid"))
	})
}

// POST /hr/rotations · PUT /hr/rotations/{id} {name, pattern: [schedule id | "off", ...]}
func (s *Server) saveRotation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string   `json:"name"`
		Pattern []string `json:"pattern"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	ok := strings.TrimSpace(in.Name) != "" && len(in.Pattern) >= 1 && len(in.Pattern) <= 62 && (id == "" || isUUID(id))
	for _, p := range in.Pattern {
		ok = ok && (p == "off" || isUUID(p))
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		for _, p := range in.Pattern {
			if p == "off" {
				continue
			}
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM work_schedules WHERE id = $1 AND company_id = $2)`, p, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_schedule")
			}
		}
		if id == "" {
			err := tx.QueryRow(ctx, `INSERT INTO shift_rotations (company_id, name, pattern) VALUES ($1, $2, $3) RETURNING id::text`,
				cid, strings.TrimSpace(in.Name), in.Pattern).Scan(&id)
			return map[string]string{"id": id}, err
		}
		tag, err := tx.Exec(ctx, `UPDATE shift_rotations SET name = $3, pattern = $4 WHERE id = $1 AND company_id = $2`, id, cid, strings.TrimSpace(in.Name), in.Pattern)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]string{"id": id}, nil
	})
}

// POST /hr/rotations/{id}/assign {employee_ids, start}: these employees follow the rotation from start ("" id = none).
func (s *Server) assignRotation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		EmployeeIDs []string `json:"employee_ids"`
		Start       string   `json:"start"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	ok := isUUID(id) && validDate(in.Start) && len(in.EmployeeIDs) > 0 && len(in.EmployeeIDs) <= 1000
	for _, e := range in.EmployeeIDs {
		ok = ok && isUUID(e)
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM shift_rotations WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, notFound()
		}
		tag, err := tx.Exec(ctx, `UPDATE employees SET rotation_id = $3, rotation_start = $4::date WHERE company_id = $1 AND id::text = ANY($2::text[])`,
			cid, in.EmployeeIDs, id, in.Start)
		if err != nil {
			return nil, err
		}
		return map[string]int64{"assigned": tag.RowsAffected()}, nil
	})
}

// GET /hr/roster?from=&to= (at most 62 days): who works which schedule each day.
func (s *Server) roster(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if from == "" {
		from = today()
	}
	if to == "" {
		t, _ := time.Parse(time.DateOnly, from)
		to = t.AddDate(0, 0, 13).Format(time.DateOnly)
	}
	f, err1 := time.Parse(time.DateOnly, from)
	t, err2 := time.Parse(time.DateOnly, to)
	if err1 != nil || err2 != nil || t.Before(f) || t.Sub(f) > 62*24*time.Hour {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT e.id::text, e.code, e.name, e.department, coalesce((SELECT r.name FROM shift_rotations r WHERE r.id = e.rotation_id), '')
			FROM employees e WHERE e.company_id = $1 AND e.status = 'active' ORDER BY e.name`, cid)
		if err != nil {
			return nil, err
		}
		type emp struct{ id, code, name, dept, rot string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (emp, error) {
			var x emp
			err := row.Scan(&x.id, &x.code, &x.name, &x.dept, &x.rot)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		names := map[string]string{}
		srows, err := tx.Query(ctx, `SELECT id::text, name FROM work_schedules WHERE company_id = $1`, cid)
		if err != nil {
			return nil, err
		}
		for srows.Next() {
			var id, n string
			if err := srows.Scan(&id, &n); err != nil {
				srows.Close()
				return nil, err
			}
			names[id] = n
		}
		srows.Close()
		overrides := map[string]bool{}
		orows, err := tx.Query(ctx, `SELECT employee_id::text || work_date::text FROM employee_shifts WHERE company_id = $1 AND work_date BETWEEN $2::date AND $3::date`, cid, from, to)
		if err != nil {
			return nil, err
		}
		for orows.Next() {
			var k string
			if err := orows.Scan(&k); err != nil {
				orows.Close()
				return nil, err
			}
			overrides[k] = true
		}
		orows.Close()
		out := []map[string]any{}
		for _, e := range list {
			days := []map[string]any{}
			for d := f; !d.After(t); d = d.AddDate(0, 0, 1) {
				day := d.Format(time.DateOnly)
				sched, off, err := scheduleFor(ctx, tx, e.id, day)
				if err != nil {
					return nil, err
				}
				days = append(days, map[string]any{"date": day, "schedule_id": sched, "schedule": names[sched], "off": off, "override": overrides[e.id+day]})
			}
			out = append(out, map[string]any{"employee_id": e.id, "code": e.code, "name": e.name, "department": e.dept, "rotation": e.rot, "days": days})
		}
		return map[string]any{"from": from, "to": to, "rows": out}, nil
	})
}

// PUT /hr/roster {employee_id, work_date, schedule_id ("" = day off), clear}: one day's override.
func (s *Server) putRoster(w http.ResponseWriter, r *http.Request) {
	var in struct {
		EmployeeID string `json:"employee_id"`
		WorkDate   string `json:"work_date"`
		ScheduleID string `json:"schedule_id"`
		Clear      bool   `json:"clear"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(in.EmployeeID) || !validDate(in.WorkDate) || (in.ScheduleID != "" && !isUUID(in.ScheduleID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM employees WHERE id = $1 AND company_id = $2)`, in.EmployeeID, cid).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, notFound()
		}
		if in.Clear {
			_, err := tx.Exec(ctx, `DELETE FROM employee_shifts WHERE employee_id = $1 AND work_date = $2::date`, in.EmployeeID, in.WorkDate)
			return map[string]bool{"ok": err == nil}, err
		}
		if in.ScheduleID != "" {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM work_schedules WHERE id = $1 AND company_id = $2)`, in.ScheduleID, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_schedule")
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO employee_shifts (company_id, employee_id, work_date, schedule_id) VALUES ($1, $2, $3::date, nullif($4, '')::uuid)
			ON CONFLICT (employee_id, work_date) DO UPDATE SET schedule_id = excluded.schedule_id`, cid, in.EmployeeID, in.WorkDate, in.ScheduleID); err != nil {
			return nil, err
		}
		// a day already clocked is recomputed with its new schedule
		var att string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM attendance WHERE employee_id = $1 AND work_date = $2::date`, in.EmployeeID, in.WorkDate).Scan(&att); err == nil {
			if err := computeAttendance(ctx, tx, att); err != nil {
				return nil, err
			}
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ================= leave accrual history =================

// POST /hr/leave/accrue {period: YYYY-MM}: monthly types earn days_per_year / 12; yearly types get the year's days
// (prorated by hire month) with the first run of the year, plus the carry-over of last year's unused days (≤ max_carry).
// Running a period twice adds nothing.
func (s *Server) accrueLeave(w http.ResponseWriter, r *http.Request) {
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
		return runLeaveAccrual(ctx, tx, r.PathValue("cid"), in.Period)
	})
}

func runLeaveAccrual(ctx context.Context, tx pgx.Tx, cid, period string) (map[string]any, error) {
	if err := ensureLeaveTypes(ctx, tx, cid); err != nil {
		return nil, err
	}
	year, _ := strconv.Atoi(period[:4])
	yp := period[:4]
	// monthly accrual for employees hired by the end of the month
	t1, err := tx.Exec(ctx, `INSERT INTO leave_accruals (company_id, employee_id, type_id, year, period, kind, days, note)
		SELECT $1, e.id, t.id, $3::int, $2, 'accrual', round(t.days_per_year / 12, 2), 'monthly'
		FROM employees e JOIN leave_types t ON t.company_id = e.company_id
		WHERE e.company_id = $1 AND e.status = 'active' AND e.hire_date <= ($2 || '-01')::date + interval '1 month' - interval '1 day'
		  AND t.is_active AND t.accrual = 'monthly' AND t.days_per_year > 0
		ON CONFLICT DO NOTHING`, cid, period, year)
	if err != nil {
		return nil, err
	}
	// yearly grant (once per year), prorated by the hire month in the hire year
	t2, err := tx.Exec(ctx, `INSERT INTO leave_accruals (company_id, employee_id, type_id, year, period, kind, days, note)
		SELECT $1, e.id, t.id, $3::int, $4, 'accrual',
		  round(t.days_per_year * CASE WHEN extract(year FROM e.hire_date) = $3::int THEN (13 - extract(month FROM e.hire_date)) / 12.0 ELSE 1 END, 2), 'yearly'
		FROM employees e JOIN leave_types t ON t.company_id = e.company_id
		WHERE e.company_id = $1 AND e.status = 'active' AND e.hire_date <= ($2 || '-01')::date + interval '1 month' - interval '1 day'
		  AND t.is_active AND t.accrual = 'yearly' AND t.days_per_year > 0
		ON CONFLICT DO NOTHING`, cid, period, year, yp)
	if err != nil {
		return nil, err
	}
	// carry-over of last year's unused days (first run of the year)
	t3, err := tx.Exec(ctx, `INSERT INTO leave_accruals (company_id, employee_id, type_id, year, period, kind, days, note)
		SELECT $1, e.id, t.id, $2::int, $3, 'carry', least(t.max_carry, greatest(0,
		    coalesce((SELECT sum(a.days) FROM leave_accruals a WHERE a.employee_id = e.id AND a.type_id = t.id AND a.year = $2::int - 1),
		             CASE WHEN e.hire_date < make_date($2::int - 1, 12, 31) THEN t.days_per_year ELSE 0 END)
		    - coalesce((SELECT sum(r.days) FROM leave_requests r WHERE r.employee_id = e.id AND r.type_id = t.id AND r.status = 'approved'
		                AND extract(year FROM r.date_from) = $2::int - 1), 0))), 'carry-over'
		FROM employees e JOIN leave_types t ON t.company_id = e.company_id
		WHERE e.company_id = $1 AND e.status = 'active' AND e.hire_date < make_date($2::int, 1, 1) AND t.is_active AND t.accrual <> 'none' AND t.max_carry > 0
		ON CONFLICT DO NOTHING`, cid, year, yp)
	if err != nil {
		return nil, err
	}
	return map[string]any{"period": period, "monthly": t1.RowsAffected(), "yearly": t2.RowsAffected(), "carried": t3.RowsAffected()}, nil
}

// POST /hr/leave/adjust {employee_id, type_id, year, days (+/-), note}
func (s *Server) adjustLeave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		EmployeeID string `json:"employee_id"`
		TypeID     string `json:"type_id"`
		Year       int    `json:"year"`
		Days       string `json:"days"`
		Note       string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Year == 0 {
		in.Year = time.Now().Year()
	}
	d, err := decimal.NewFromString(strings.TrimSpace(in.Days))
	if !isUUID(in.EmployeeID) || !isUUID(in.TypeID) || err != nil || d.IsZero() || d.Abs().GreaterThan(decimal.NewFromInt(365)) ||
		in.Year < 2000 || in.Year > 2100 || strings.TrimSpace(in.Note) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM employees WHERE id = $1 AND company_id = $3)
			AND EXISTS (SELECT 1 FROM leave_types WHERE id = $2 AND company_id = $3)`, in.EmployeeID, in.TypeID, cid).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, badRequest("bad_request")
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO leave_accruals (company_id, employee_id, type_id, year, period, kind, days, note)
			VALUES ($1, $2, $3, $4, to_char(now(), 'YYYY-MM-DD HH24:MI:SS'), 'adjust', $5::numeric, $6) RETURNING id::text`,
			cid, in.EmployeeID, in.TypeID, in.Year, d.String(), strings.TrimSpace(in.Note)).Scan(&id)
		return map[string]string{"id": id}, err
	})
}

// GET /hr/leave/accruals?employee_id=&year=
func (s *Server) listAccruals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	emp := q.Get("employee_id")
	year, _ := strconv.Atoi(defaultStr(q.Get("year"), strconv.Itoa(time.Now().Year())))
	if (emp != "" && !isUUID(emp)) || year < 2000 || year > 2100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT a.id, a.employee_id, e.name AS employee_name, a.type_id, t.code AS type_code, t.name_en, t.name_ar, t.name_fr,
			a.year, a.period, a.kind, a.days::text AS days, a.note, a.created_at FROM leave_accruals a JOIN employees e ON e.id = a.employee_id
			JOIN leave_types t ON t.id = a.type_id WHERE a.company_id = $1 AND a.year = $2 AND ($3 = '' OR a.employee_id = nullif($3, '')::uuid)
			ORDER BY e.name, t.code, a.created_at LIMIT 5000`, r.PathValue("cid"), year, emp)
	})
}

// RunLeaveAccruals posts this month's accruals for every company that uses HR (daily worker; runs are idempotent).
func (s *Server) RunLeaveAccruals(ctx context.Context) error {
	if s.OwnerPool == nil {
		return nil
	}
	rows, err := s.OwnerPool.Query(ctx, `SELECT DISTINCT e.company_id::text, e.tenant_id::text FROM employees e JOIN companies c ON c.id = e.company_id
		WHERE c.is_active AND 'hr' = ANY(c.apps) AND tenant_jobs_ok(e.tenant_id) AND EXISTS (SELECT 1 FROM leave_accruals a WHERE a.company_id = e.company_id)`)
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
	period := time.Now().Format("2006-01")
	for _, c := range list { // only companies that started using the accrual history
		err := db.InTenant(ctx, s.Pool, db.Scope{TenantID: c.tenant, CompanyID: c.id}, func(tx pgx.Tx) error {
			_, err := runLeaveAccrual(ctx, tx, c.id, period)
			return err
		})
		if err != nil {
			slog.Warn("leave accrual skipped", "company", c.id, "err", err)
		}
	}
	return nil
}

// ================= clock-in photo and geofence =================

func decodePhoto(s string) ([]byte, string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, "", nil
	}
	b, err := base64.StdEncoding.DecodeString(stripDataURL(strings.TrimSpace(s)))
	if err != nil || len(b) == 0 || len(b) > 1<<20 {
		return nil, "", errors.New("invalid photo")
	}
	mime := http.DetectContentType(b)
	if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" {
		return nil, "", errors.New("not an image")
	}
	return b, mime, nil
}

// haversine distance in metres
func distanceM(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371000.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := (lat2-lat1)*math.Pi/180, (lng2-lng1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * R * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func checkClockRules(ctx context.Context, tx pgx.Tx, cid string, lat, lng *float64, hasPhoto bool) error {
	var photo, geo bool
	var clat, clng *float64
	var radius int
	if err := tx.QueryRow(ctx, `SELECT clock_photo, clock_geofence, clock_lat::float8, clock_lng::float8, clock_radius_m FROM companies WHERE id = $1`, cid).
		Scan(&photo, &geo, &clat, &clng, &radius); err != nil {
		return err
	}
	if photo && !hasPhoto {
		return &apiError{status: http.StatusUnprocessableEntity, code: "photo_required"}
	}
	if geo && clat != nil && clng != nil {
		if lat == nil || lng == nil {
			return &apiError{status: http.StatusUnprocessableEntity, code: "location_required"}
		}
		if d := distanceM(*lat, *lng, *clat, *clng); d > float64(radius) {
			return &apiError{status: http.StatusUnprocessableEntity, code: "outside_area", detail: fmt.Sprintf("%.0f m", d)}
		}
	}
	return nil
}

// GET / PUT /hr/clock-settings {photo, geofence, lat, lng, radius_m}
func (s *Server) getClockSettings(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT clock_photo AS photo, clock_geofence AS geofence, clock_lat::text AS lat, clock_lng::text AS lng,
			clock_radius_m AS radius_m FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}

func (s *Server) putClockSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Photo    bool     `json:"photo"`
		Geofence bool     `json:"geofence"`
		Lat      *float64 `json:"lat"`
		Lng      *float64 `json:"lng"`
		RadiusM  int      `json:"radius_m"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.RadiusM == 0 {
		in.RadiusM = 200
	}
	if in.RadiusM < 20 || in.RadiusM > 50000 || (in.Lat != nil && (*in.Lat < -90 || *in.Lat > 90)) || (in.Lng != nil && (*in.Lng < -180 || *in.Lng > 180)) ||
		(in.Geofence && (in.Lat == nil || in.Lng == nil)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE companies SET clock_photo = $2, clock_geofence = $3, clock_lat = $4, clock_lng = $5, clock_radius_m = $6 WHERE id = $1`,
			r.PathValue("cid"), in.Photo, in.Geofence, in.Lat, in.Lng, in.RadiusM); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

// GET /hr/attendance/{id}/photo?kind=in|out
func (s *Server) attendancePhoto(w http.ResponseWriter, r *http.Request) {
	cid, id, kind := r.PathValue("cid"), r.PathValue("id"), defaultStr(r.URL.Query().Get("kind"), "in")
	if !isUUID(id) || (kind != "in" && kind != "out") {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var data []byte
	var mime string
	s.runRaw(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT data, mime FROM attendance_photos WHERE attendance_id = $1 AND company_id = $2 AND kind = $3`, id, cid, kind).Scan(&data, &mime)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound()
		}
		return err
	}, func() {
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "private, max-age=300")
		_, _ = w.Write(data)
	})
}

// ================= fingerprint devices (ZKTeco ADMS push) =================

var deviceSerialRe = regexp.MustCompile(`^[A-Za-z0-9_-]{4,40}$`)

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT id, serial, name, allowed_ip, is_active, last_seen, last_ip, punches, created_at FROM attendance_devices
			WHERE company_id = $1 ORDER BY name, serial`, r.PathValue("cid"))
	})
}

// POST /hr/devices {serial, name, allowed_ip} · PATCH /hr/devices/{id} {name, allowed_ip, is_active}
func (s *Server) saveDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Serial    string `json:"serial"`
		Name      string `json:"name"`
		AllowedIP string `json:"allowed_ip"`
		IsActive  *bool  `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	in.Serial, in.AllowedIP = strings.TrimSpace(in.Serial), strings.TrimSpace(in.AllowedIP)
	if (id == "" && !deviceSerialRe.MatchString(in.Serial)) || (id != "" && !isUUID(id)) || len(in.AllowedIP) > 45 || len(in.Name) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if id == "" {
			err := tx.QueryRow(ctx, `INSERT INTO attendance_devices (company_id, serial, name, allowed_ip) VALUES ($1, $2, $3, $4) RETURNING id::text`,
				cid, in.Serial, strings.TrimSpace(in.Name), in.AllowedIP).Scan(&id)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "duplicate_serial"}
			}
			if err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE attendance_devices SET name = $3, allowed_ip = $4, is_active = coalesce($5, is_active) WHERE id = $1 AND company_id = $2`,
				id, cid, strings.TrimSpace(in.Name), in.AllowedIP, in.IsActive)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT id, serial, name, allowed_ip, is_active, last_seen, last_ip, punches FROM attendance_devices WHERE id = $1`, id)
	})
}

// iclockDevice finds the registered, active device of serial SN (any tenant) and checks its address.
func (s *Server) iclockDevice(r *http.Request) (db.Scope, string, bool) {
	sn := strings.TrimSpace(r.URL.Query().Get("SN"))
	if !deviceSerialRe.MatchString(sn) {
		return db.Scope{}, "", false
	}
	pool := s.OwnerPool
	if pool == nil {
		return db.Scope{}, "", false
	}
	var tid, cid, allowed, id string
	var active bool
	if err := pool.QueryRow(r.Context(), `SELECT tenant_id::text, company_id::text, allowed_ip, is_active, id::text FROM attendance_devices WHERE serial = $1`, sn).
		Scan(&tid, &cid, &allowed, &active, &id); err != nil || !active {
		return db.Scope{}, "", false
	}
	ip := clientIP(r)
	if allowed != "" && allowed != ip {
		slog.Warn("iclock: device from unexpected address", "serial", sn, "ip", ip)
		return db.Scope{}, "", false
	}
	_, _ = pool.Exec(r.Context(), `UPDATE attendance_devices SET last_seen = now(), last_ip = $2 WHERE id = $1`, id, ip)
	return db.Scope{TenantID: tid, CompanyID: cid, IP: ip}, id, true
}

func iclockText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// GET /iclock/cdata?SN=…&options=all: the device asks for its settings (handshake).
func (s *Server) iclockHandshake(w http.ResponseWriter, r *http.Request) {
	if !s.portalLimiter.allow(clientIP(r)) {
		iclockText(w, http.StatusTooManyRequests, "")
		return
	}
	if _, _, ok := s.iclockDevice(r); !ok {
		s.portalLimiter.fail(clientIP(r))
		iclockText(w, http.StatusUnauthorized, "")
		return
	}
	sn := r.URL.Query().Get("SN")
	iclockText(w, http.StatusOK, "GET OPTION FROM: "+sn+"\nATTLOGStamp=None\nOPERLOGStamp=9999\nATTPHOTOStamp=None\nErrorDelay=30\nDelay=10\n"+
		"TransTimes=00:00;14:05\nTransInterval=1\nTransFlag=TransData AttLog\nTimeZone=2\nRealtime=1\nEncrypt=None\n")
}

// GET /iclock/getrequest?SN=… and POST /iclock/devicecmd: no commands for the device.
func (s *Server) iclockPoll(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.iclockDevice(r); !ok {
		iclockText(w, http.StatusUnauthorized, "")
		return
	}
	iclockText(w, http.StatusOK, "OK")
}

// POST /iclock/cdata?SN=…&table=ATTLOG: punches, one per line "PIN<TAB>YYYY-MM-DD HH:MM:SS<TAB>status<TAB>verify…".
func (s *Server) iclockPush(w http.ResponseWriter, r *http.Request) {
	if !s.portalLimiter.allow(clientIP(r)) {
		iclockText(w, http.StatusTooManyRequests, "")
		return
	}
	sc, devID, ok := s.iclockDevice(r)
	if !ok {
		s.portalLimiter.fail(clientIP(r))
		iclockText(w, http.StatusUnauthorized, "")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		iclockText(w, http.StatusBadRequest, "")
		return
	}
	if !strings.EqualFold(r.URL.Query().Get("table"), "ATTLOG") {
		iclockText(w, http.StatusOK, "OK") // operation logs, photos, user info: accepted and ignored
		return
	}
	var punches []punch
	sc2 := bufio.NewScanner(strings.NewReader(string(body)))
	for sc2.Scan() {
		f := strings.Split(strings.TrimSpace(sc2.Text()), "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		at, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(f[1]))
		if err != nil {
			continue
		}
		punches = append(punches, punch{user: strings.TrimSpace(f[0]), at: at})
	}
	n := 0
	err = db.InTenant(r.Context(), s.Pool, sc, func(tx pgx.Tx) error {
		var err error
		n, err = applyPunches(r.Context(), tx, sc.CompanyID, punches)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE attendance_devices SET punches = punches + $2 WHERE id = $1`, devID, len(punches))
		return err
	})
	if err != nil {
		slog.Error("iclock push", "err", err)
		iclockText(w, http.StatusInternalServerError, "")
		return
	}
	iclockText(w, http.StatusOK, fmt.Sprintf("OK: %d", n))
}

// applyPunches merges punches into the day records (first punch = in, last = out) of the matched employees.
func applyPunches(ctx context.Context, tx pgx.Tx, cid string, punches []punch) (int, error) {
	if len(punches) == 0 {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, code, device_id FROM employees WHERE company_id = $1`, cid)
	if err != nil {
		return 0, err
	}
	byKey := map[string]string{}
	for rows.Next() {
		var id, code, dev string
		if err := rows.Scan(&id, &code, &dev); err != nil {
			rows.Close()
			return 0, err
		}
		byKey[strings.ToLower(code)] = id
		if dev != "" {
			byKey["#"+strings.ToLower(dev)] = id
		}
	}
	rows.Close()
	n := 0
	for _, p := range punches {
		emp := byKey["#"+strings.ToLower(p.user)]
		if emp == "" {
			emp = byKey[strings.ToLower(p.user)]
		}
		if emp == "" {
			continue
		}
		ts := p.at.Format("2006-01-02 15:04:05")
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO attendance (company_id, employee_id, work_date, check_in, source)
			VALUES ($1, $2, $3::date, ($4::timestamp AT TIME ZONE 'Asia/Beirut'), 'device')
			ON CONFLICT (tenant_id, employee_id, work_date) DO UPDATE SET
			  check_in = least(attendance.check_in, excluded.check_in),
			  check_out = CASE WHEN attendance.check_in IS NULL OR excluded.check_in = attendance.check_in THEN attendance.check_out
			                   WHEN excluded.check_in > attendance.check_in THEN greatest(attendance.check_out, excluded.check_in)
			                   ELSE greatest(attendance.check_out, attendance.check_in) END,
			  source = 'device', updated_at = now() RETURNING id::text`, cid, emp, p.at.Format(time.DateOnly), ts).Scan(&id); err != nil {
			return n, err
		}
		if err := computeAttendance(ctx, tx, id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
