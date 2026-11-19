package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Module U: HR — employee file, documents, pay components, schedules, holidays, attendance (phone clock-in,
// fingerprint CSV import), leave (types, balances, requests, approval), payroll rules, employee self-service,
// HR overview and reports.

var timeRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// ---------------- employee file ----------------

const empFileCols = `e.id, e.code, e.name, e.phone, e.email, e.position, e.department, e.hire_date, e.end_date, e.status, e.currency,
	e.salary::text AS salary, e.currency2, e.salary2::text AS salary2, e.transport::text AS transport, e.transport_daily,
	e.housing::text AS housing, e.commission_pct::text AS commission_pct, e.nssf_enrolled, e.nssf_number, e.tax_exempt, e.track_attendance,
	e.national_id, e.nationality, e.birth_date, e.gender, e.marital_status, e.spouse_working, e.children, e.address, e.manager_id,
	(SELECT m.name FROM employees m WHERE m.id = e.manager_id) AS manager_name, e.contract_type, e.contract_start, e.contract_end,
	e.bank_name, e.bank_account, e.bank_routing, e.photo, e.schedule_id, e.device_id, e.method_id, e.user_id, e.cost_center_id, e.notes,
	(SELECT u.email FROM users u WHERE u.id = e.user_id) AS user_email`

func (s *Server) getEmployeeFile(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	year := time.Now().Year()
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := queryJSONOne(ctx, tx, `SELECT `+empFileCols+` FROM employees e WHERE e.id = $1 AND e.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		comps, err := queryJSON(ctx, tx, `SELECT id, kind, name, currency, amount::text AS amount, taxable, social FROM employee_components
			WHERE employee_id = $1 ORDER BY created_at`, id)
		if err != nil {
			return nil, err
		}
		docs, err := queryJSON(ctx, tx, `SELECT id, name, kind, mime, size, expires_on, created_at FROM employee_documents
			WHERE employee_id = $1 ORDER BY created_at DESC`, id)
		if err != nil {
			return nil, err
		}
		bal, err := leaveBalances(ctx, tx, cid, id, year)
		if err != nil {
			return nil, err
		}
		leave, err := queryJSON(ctx, tx, `SELECT r.id, r.date_from, r.date_to, r.days::text AS days, r.status, r.reason, t.code AS type_code, t.name_en,
			t.name_ar, t.name_fr FROM leave_requests r JOIN leave_types t ON t.id = r.type_id WHERE r.employee_id = $1 ORDER BY r.date_from DESC LIMIT 50`, id)
		if err != nil {
			return nil, err
		}
		att, err := queryJSON(ctx, tx, `SELECT work_date, check_in, check_out, source, worked_minutes, late_minutes, overtime_minutes, notes
			FROM attendance WHERE employee_id = $1 AND work_date > current_date - 45 ORDER BY work_date DESC`, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"employee": emp, "components": comps, "documents": docs, "leave_balances": bal, "leave": leave, "attendance": att}, nil
	})
}

type empFileIn struct {
	Email, NationalID, NssfNumber, Nationality, BirthDate, Gender, Marital, Address, Department *string
	ManagerID, ContractType, ContractStart, ContractEnd, EndDate, BankName, BankAccount         *string
	BankRouting, Photo, Currency2, Salary2, Housing, Commission, ScheduleID, DeviceID           *string
	SpouseWorking, TransportDaily, TaxExempt, Track                                             *bool
	Children                                                                                    *int
	Components                                                                                  *[]struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		Currency string `json:"currency"`
		Amount   string `json:"amount"`
		Taxable  bool   `json:"taxable"`
		Social   bool   `json:"social"`
	} `json:"components"`
}

func (in *empFileIn) UnmarshalJSON(b []byte) error {
	type alias struct {
		Email          *string         `json:"email"`
		NationalID     *string         `json:"national_id"`
		NssfNumber     *string         `json:"nssf_number"`
		Nationality    *string         `json:"nationality"`
		BirthDate      *string         `json:"birth_date"`
		Gender         *string         `json:"gender"`
		Marital        *string         `json:"marital_status"`
		Address        *string         `json:"address"`
		Department     *string         `json:"department"`
		ManagerID      *string         `json:"manager_id"`
		ContractType   *string         `json:"contract_type"`
		ContractStart  *string         `json:"contract_start"`
		ContractEnd    *string         `json:"contract_end"`
		EndDate        *string         `json:"end_date"`
		BankName       *string         `json:"bank_name"`
		BankAccount    *string         `json:"bank_account"`
		BankRouting    *string         `json:"bank_routing"`
		Photo          *string         `json:"photo"`
		Currency2      *string         `json:"currency2"`
		Salary2        *string         `json:"salary2"`
		Housing        *string         `json:"housing"`
		Commission     *string         `json:"commission_pct"`
		ScheduleID     *string         `json:"schedule_id"`
		DeviceID       *string         `json:"device_id"`
		SpouseWorking  *bool           `json:"spouse_working"`
		TransportDaily *bool           `json:"transport_daily"`
		TaxExempt      *bool           `json:"tax_exempt"`
		Track          *bool           `json:"track_attendance"`
		Children       *int            `json:"children"`
		Components     json.RawMessage `json:"components"`
	}
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*in = empFileIn{Email: a.Email, NationalID: a.NationalID, NssfNumber: a.NssfNumber, Nationality: a.Nationality, BirthDate: a.BirthDate,
		Gender: a.Gender, Marital: a.Marital, Address: a.Address, Department: a.Department, ManagerID: a.ManagerID, ContractType: a.ContractType,
		ContractStart: a.ContractStart, ContractEnd: a.ContractEnd, EndDate: a.EndDate, BankName: a.BankName, BankAccount: a.BankAccount,
		BankRouting: a.BankRouting, Photo: a.Photo, Currency2: a.Currency2, Salary2: a.Salary2, Housing: a.Housing, Commission: a.Commission,
		ScheduleID: a.ScheduleID, DeviceID: a.DeviceID, SpouseWorking: a.SpouseWorking, TransportDaily: a.TransportDaily, TaxExempt: a.TaxExempt,
		Track: a.Track, Children: a.Children}
	if len(a.Components) > 0 && string(a.Components) != "null" {
		in.Components = &[]struct {
			Kind     string `json:"kind"`
			Name     string `json:"name"`
			Currency string `json:"currency"`
			Amount   string `json:"amount"`
			Taxable  bool   `json:"taxable"`
			Social   bool   `json:"social"`
		}{}
		if err := json.Unmarshal(a.Components, in.Components); err != nil {
			return err
		}
	}
	return nil
}

// PUT /employees/{id}/file: the HR part of the employee file (pay basics stay on PATCH /employees/{id}).
func (s *Server) putEmployeeFile(w http.ResponseWriter, r *http.Request) {
	var in empFileIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	optDate := func(p *string) bool { return p == nil || *p == "" || validDate(*p) }
	okMoney := func(p *string) bool {
		if p == nil || *p == "" {
			return true
		}
		d, ok := money(*p)
		return ok && !d.IsNegative()
	}
	if !isUUID(id) || !optDate(in.BirthDate) || !optDate(in.ContractStart) || !optDate(in.ContractEnd) || !optDate(in.EndDate) ||
		!optUUID(in.ManagerID) || !optUUID(in.ScheduleID) || !okMoney(in.Salary2) || !okMoney(in.Housing) || !okMoney(in.Commission) ||
		(in.Currency2 != nil && *in.Currency2 != "" && !currencyRe.MatchString(*in.Currency2)) ||
		(in.Photo != nil && *in.Photo != "" && (!strings.HasPrefix(*in.Photo, "data:image/") || len(*in.Photo) > 400000)) ||
		(in.Children != nil && (*in.Children < 0 || *in.Children > 20)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.ManagerID != nil && *in.ManagerID == id {
			return nil, badRequest("bad_request")
		}
		tag, err := tx.Exec(ctx, `UPDATE employees SET email = coalesce(trim($3), email), national_id = coalesce(trim($4), national_id),
			nssf_number = coalesce(trim($5), nssf_number), nationality = coalesce(upper(trim($6)), nationality),
			birth_date = CASE WHEN $7::text IS NULL THEN birth_date ELSE nullif($7, '')::date END,
			gender = coalesce($8, gender), marital_status = coalesce($9, marital_status), address = coalesce($10, address),
			department = coalesce(trim($11), department),
			manager_id = CASE WHEN $12::text IS NULL THEN manager_id ELSE nullif($12, '')::uuid END,
			contract_type = coalesce($13, contract_type),
			contract_start = CASE WHEN $14::text IS NULL THEN contract_start ELSE nullif($14, '')::date END,
			contract_end = CASE WHEN $15::text IS NULL THEN contract_end ELSE nullif($15, '')::date END,
			end_date = CASE WHEN $16::text IS NULL THEN end_date ELSE nullif($16, '')::date END,
			bank_name = coalesce(trim($17), bank_name), bank_account = coalesce(upper(replace($18, ' ', '')), bank_account),
			bank_routing = coalesce(trim($19), bank_routing), photo = coalesce($20, photo),
			currency2 = CASE WHEN $21::text IS NULL THEN currency2 ELSE nullif($21, '')::char(3) END,
			salary2 = CASE WHEN $21::text = '' THEN 0 ELSE coalesce(nullif($22, '')::numeric, salary2) END,
			housing = coalesce(nullif($23, '')::numeric, housing), commission_pct = coalesce(nullif($24, '')::numeric, commission_pct),
			schedule_id = CASE WHEN $25::text IS NULL THEN schedule_id ELSE nullif($25, '')::uuid END,
			device_id = coalesce(trim($26), device_id), spouse_working = coalesce($27, spouse_working),
			transport_daily = coalesce($28, transport_daily), tax_exempt = coalesce($29, tax_exempt), track_attendance = coalesce($30, track_attendance),
			children = coalesce($31, children), updated_at = now()
			WHERE id = $1 AND company_id = $2`, id, cid, in.Email, in.NationalID, in.NssfNumber, in.Nationality, in.BirthDate, in.Gender, in.Marital,
			in.Address, in.Department, in.ManagerID, in.ContractType, in.ContractStart, in.ContractEnd, in.EndDate, in.BankName, in.BankAccount,
			in.BankRouting, in.Photo, in.Currency2, in.Salary2, in.Housing, in.Commission, in.ScheduleID, in.DeviceID, in.SpouseWorking,
			in.TransportDaily, in.TaxExempt, in.Track, in.Children)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		if in.Components != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM employee_components WHERE employee_id = $1`, id); err != nil {
				return nil, err
			}
			for _, c := range *in.Components {
				a, ok := money(c.Amount)
				if !ok || !a.IsPositive() || (c.Kind != "allowance" && c.Kind != "deduction") || strings.TrimSpace(c.Name) == "" || !currencyRe.MatchString(c.Currency) {
					return nil, badRequest("invalid_line")
				}
				if _, err := tx.Exec(ctx, `INSERT INTO employee_components (company_id, employee_id, kind, name, currency, amount, taxable, social)
					VALUES ($1, $2, $3, $4, $5, $6::numeric, $7, $8)`, cid, id, c.Kind, strings.TrimSpace(c.Name), c.Currency, a.String(), c.Taxable, c.Social); err != nil {
					return nil, err
				}
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+empFileCols+` FROM employees e WHERE e.id = $1`, id)
	})
}

// ---------------- documents ----------------

func (s *Server) addEmployeeDocument(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	var in struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Mime      string `json:"mime"`
		Data      string `json:"data"` // base64 (a data: URL is accepted too)
		ExpiresOn string `json:"expires_on"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if i := strings.Index(in.Data, ";base64,"); strings.HasPrefix(in.Data, "data:") && i > 0 {
		if in.Mime == "" {
			in.Mime = in.Data[5:i]
		}
		in.Data = in.Data[i+8:]
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if err != nil || len(data) == 0 || len(data) > 5<<20 || strings.TrimSpace(in.Name) == "" || !isUUID(id) || (in.ExpiresOn != "" && !validDate(in.ExpiresOn)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Mime == "" {
		in.Mime = http.DetectContentType(data)
	}
	if in.Kind == "" {
		in.Kind = "other"
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM employees WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			return nil, notFound()
		}
		var did string
		if err := tx.QueryRow(ctx, `INSERT INTO employee_documents (company_id, employee_id, name, kind, mime, size, data, expires_on)
			VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, '')::date) RETURNING id::text`,
			cid, id, strings.TrimSpace(in.Name), in.Kind, in.Mime, len(data), data, in.ExpiresOn).Scan(&did); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, name, kind, mime, size, expires_on, created_at FROM employee_documents WHERE id = $1`, did)
	})
}

func (s *Server) getEmployeeDocument(w http.ResponseWriter, r *http.Request) {
	cid, id, did := r.PathValue("cid"), r.PathValue("id"), r.PathValue("did")
	if !isUUID(id) || !isUUID(did) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var name, mime string
	var data []byte
	s.runRaw(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT name, mime, data FROM employee_documents WHERE id = $1 AND employee_id = $2 AND company_id = $3`, did, id, cid).
			Scan(&name, &mime, &data)
		if err == pgx.ErrNoRows {
			return notFound()
		}
		return err
	}, func() {
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	})
}

func (s *Server) deleteEmployeeDocument(w http.ResponseWriter, r *http.Request) {
	cid, id, did := r.PathValue("cid"), r.PathValue("id"), r.PathValue("did")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) || !isUUID(did) {
			return nil, notFound()
		}
		tag, err := tx.Exec(ctx, `DELETE FROM employee_documents WHERE id = $1 AND employee_id = $2 AND company_id = $3`, did, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ---------------- schedules & holidays ----------------

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT id, name, to_char(start_time, 'HH24:MI') AS start_time, to_char(end_time, 'HH24:MI') AS end_time,
			break_minutes, grace_minutes, work_days, (SELECT count(*) FROM employees e WHERE e.schedule_id = w.id) AS employees
			FROM work_schedules w WHERE company_id = $1 ORDER BY name`, r.PathValue("cid"))
	})
}

func (s *Server) saveSchedule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Start    string `json:"start_time"`
		End      string `json:"end_time"`
		Break    int    `json:"break_minutes"`
		Grace    int    `json:"grace_minutes"`
		WorkDays []int  `json:"work_days"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	ok := strings.TrimSpace(in.Name) != "" && timeRe.MatchString(in.Start) && timeRe.MatchString(in.End) && in.Break >= 0 && in.Break <= 600 &&
		in.Grace >= 0 && in.Grace <= 240 && (id == "" || isUUID(id))
	for _, d := range in.WorkDays {
		ok = ok && d >= 1 && d <= 7
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.WorkDays == nil {
		in.WorkDays = []int{1, 2, 3, 4, 5, 6}
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if id == "" {
			var nid string
			err := tx.QueryRow(ctx, `INSERT INTO work_schedules (company_id, name, start_time, end_time, break_minutes, grace_minutes, work_days)
				VALUES ($1, $2, $3::time, $4::time, $5, $6, $7) RETURNING id::text`, cid, strings.TrimSpace(in.Name), in.Start, in.End, in.Break, in.Grace, in.WorkDays).Scan(&nid)
			return map[string]string{"id": nid}, err
		}
		tag, err := tx.Exec(ctx, `UPDATE work_schedules SET name = $3, start_time = $4::time, end_time = $5::time, break_minutes = $6,
			grace_minutes = $7, work_days = $8 WHERE id = $1 AND company_id = $2`, id, cid, strings.TrimSpace(in.Name), in.Start, in.End, in.Break, in.Grace, in.WorkDays)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]string{"id": id}, nil
	})
}

func (s *Server) listHolidays(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT id, day, name FROM public_holidays WHERE company_id = $1 AND day >= current_date - 400 ORDER BY day`, r.PathValue("cid"))
	})
}

func (s *Server) saveHoliday(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Day  string `json:"day"`
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !validDate(in.Day) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO public_holidays (company_id, day, name) VALUES ($1, $2::date, $3)
			ON CONFLICT (tenant_id, company_id, day) DO UPDATE SET name = excluded.name RETURNING id::text`, r.PathValue("cid"), in.Day, strings.TrimSpace(in.Name)).Scan(&id)
		return map[string]string{"id": id, "day": in.Day}, err
	})
}

func (s *Server) deleteHoliday(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		tag, err := tx.Exec(ctx, `DELETE FROM public_holidays WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ---------------- attendance ----------------

// computeAttendance fills worked / late / overtime minutes from the employee's schedule.
func computeAttendance(ctx context.Context, tx pgx.Tx, attID string) error {
	var emp, day string
	if err := tx.QueryRow(ctx, `SELECT employee_id::text, work_date::text FROM attendance WHERE id = $1`, attID).Scan(&emp, &day); err != nil {
		return err
	}
	sched, off, err := scheduleFor(ctx, tx, emp, day)
	if err != nil {
		return err
	}
	if off {
		sched = "" // a rostered day off: everything worked is overtime
	}
	_, err = tx.Exec(ctx, `UPDATE attendance a SET
		worked_minutes = CASE WHEN a.check_in IS NOT NULL AND a.check_out IS NOT NULL
		  THEN greatest(0, (extract(epoch FROM a.check_out - a.check_in) / 60)::int - coalesce(w.break_minutes, 0)) ELSE 0 END,
		late_minutes = CASE WHEN a.check_in IS NOT NULL AND w.id IS NOT NULL
		  AND (a.check_in AT TIME ZONE 'Asia/Beirut')::time > w.start_time + make_interval(mins => w.grace_minutes)
		  THEN (extract(epoch FROM (a.check_in AT TIME ZONE 'Asia/Beirut')::time - w.start_time) / 60)::int ELSE 0 END,
		overtime_minutes = CASE WHEN a.check_in IS NOT NULL AND a.check_out IS NOT NULL
		  THEN greatest(0, (extract(epoch FROM a.check_out - a.check_in) / 60)::int - coalesce(w.break_minutes, 0)
		       - CASE WHEN $3 THEN 0 ELSE (extract(epoch FROM coalesce(w.end_time - w.start_time, interval '8 hours')) / 60)::int END) ELSE 0 END
		FROM employees e LEFT JOIN work_schedules w ON w.id = nullif($2, '')::uuid
		WHERE a.id = $1 AND e.id = a.employee_id`, attID, sched, off)
	return err
}

// GET /hr/attendance?date= (one day, every employee) or ?from=&to=&employee_id=
func (s *Server) listAttendance(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	date, from, to, emp := q.Get("date"), q.Get("from"), q.Get("to"), q.Get("employee_id")
	if date == "" && from == "" {
		date = today()
	}
	if (date != "" && !validDate(date)) || (from != "" && (!validDate(from) || !validDate(to))) || (emp != "" && !isUUID(emp)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if date != "" { // the day sheet: every active employee, with or without a record
			return queryJSON(ctx, tx, `SELECT e.id AS employee_id, e.code, e.name, e.department, $2::date AS work_date, a.id, a.check_in, a.check_out,
				a.source, coalesce(a.worked_minutes, 0) AS worked_minutes, coalesce(a.late_minutes, 0) AS late_minutes,
				coalesce(a.overtime_minutes, 0) AS overtime_minutes, coalesce(a.notes, '') AS notes, a.in_lat, a.in_lng,
				(SELECT t.code FROM leave_requests lr JOIN leave_types t ON t.id = lr.type_id WHERE lr.employee_id = e.id AND lr.status = 'approved'
				   AND $2::date BETWEEN lr.date_from AND lr.date_to LIMIT 1) AS on_leave,
				EXISTS (SELECT 1 FROM public_holidays h WHERE h.company_id = $1 AND h.day = $2::date) AS holiday
				FROM employees e LEFT JOIN attendance a ON a.employee_id = e.id AND a.work_date = $2::date
				WHERE e.company_id = $1 AND e.status = 'active' ORDER BY e.name`, cid, date)
		}
		return queryJSON(ctx, tx, `SELECT a.id, a.employee_id, e.code, e.name, a.work_date, a.check_in, a.check_out, a.source, a.worked_minutes,
			a.late_minutes, a.overtime_minutes, a.notes FROM attendance a JOIN employees e ON e.id = a.employee_id
			WHERE a.company_id = $1 AND a.work_date BETWEEN $2::date AND $3::date AND ($4 = '' OR a.employee_id::text = $4)
			ORDER BY a.work_date DESC, e.name LIMIT 5000`, cid, from, to, emp)
	})
}

// PUT /hr/attendance {employee_id, work_date, check_in "HH:MM" | "", check_out, notes}: manual entry or correction.
func (s *Server) putAttendance(w http.ResponseWriter, r *http.Request) {
	var in struct {
		EmployeeID string `json:"employee_id"`
		WorkDate   string `json:"work_date"`
		CheckIn    string `json:"check_in"`
		CheckOut   string `json:"check_out"`
		Notes      string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(in.EmployeeID) || !validDate(in.WorkDate) || (in.CheckIn != "" && !timeRe.MatchString(in.CheckIn)) || (in.CheckOut != "" && !timeRe.MatchString(in.CheckOut)) {
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
		ts := func(t string) *string {
			if t == "" {
				return nil
			}
			v := in.WorkDate + " " + t
			return &v
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO attendance (company_id, employee_id, work_date, check_in, check_out, source, notes)
			VALUES ($1, $2, $3::date, ($4::timestamp AT TIME ZONE 'Asia/Beirut'), ($5::timestamp AT TIME ZONE 'Asia/Beirut'), 'manual', $6)
			ON CONFLICT (tenant_id, employee_id, work_date) DO UPDATE SET check_in = excluded.check_in, check_out = excluded.check_out,
			  notes = excluded.notes, source = 'manual', updated_by = app_user(), updated_at = now() RETURNING id::text`,
			cid, in.EmployeeID, in.WorkDate, ts(in.CheckIn), ts(in.CheckOut), strings.TrimSpace(in.Notes)).Scan(&id)
		if err != nil {
			if strings.Contains(err.Error(), "check") {
				return nil, badRequest("invalid_time")
			}
			return nil, err
		}
		if err := computeAttendance(ctx, tx, id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, employee_id, work_date, check_in, check_out, worked_minutes, late_minutes, overtime_minutes FROM attendance WHERE id = $1`, id)
	})
}

// POST /hr/attendance/import {csv}: fingerprint device export (ZKTeco style: a user number column + date/time
// columns, one row per punch). First punch of the day = in, last = out. Employees are matched on their
// device number, else their code.
func (s *Server) importAttendance(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 6<<20)
	var in struct {
		CSV    string `json:"csv"`
		DryRun bool   `json:"dry_run"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	punches, unknownCols, err := parsePunches(in.CSV)
	if err != nil || unknownCols {
		writeErr(w, http.StatusBadRequest, "invalid_csv")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT id::text, code, device_id FROM employees WHERE company_id = $1`, cid)
		if err != nil {
			return nil, err
		}
		byKey := map[string]string{}
		for rows.Next() {
			var id, code, dev string
			if err := rows.Scan(&id, &code, &dev); err != nil {
				rows.Close()
				return nil, err
			}
			byKey[strings.ToLower(code)] = id
			if dev != "" {
				byKey["#"+strings.ToLower(dev)] = id
			}
		}
		rows.Close()
		type key struct{ emp, day string }
		first, last := map[key]time.Time{}, map[key]time.Time{}
		unknown := map[string]bool{}
		order := []key{}
		for _, p := range punches {
			emp := byKey["#"+strings.ToLower(p.user)]
			if emp == "" {
				emp = byKey[strings.ToLower(p.user)]
			}
			if emp == "" {
				unknown[p.user] = true
				continue
			}
			k := key{emp, p.at.Format(time.DateOnly)}
			if f, ok := first[k]; !ok || p.at.Before(f) {
				if !ok {
					order = append(order, k)
				}
				first[k] = p.at
			}
			if l, ok := last[k]; !ok || p.at.After(l) {
				last[k] = p.at
			}
		}
		unk := []string{}
		for u := range unknown {
			unk = append(unk, u)
		}
		if in.DryRun {
			return map[string]any{"punches": len(punches), "days": len(order), "unknown_users": unk}, nil
		}
		for _, k := range order {
			f, l := first[k], last[k]
			var out *string
			if l.After(f) {
				v := l.Format("2006-01-02 15:04:05")
				out = &v
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO attendance (company_id, employee_id, work_date, check_in, check_out, source)
				VALUES ($1, $2, $3::date, ($4::timestamp AT TIME ZONE 'Asia/Beirut'), ($5::timestamp AT TIME ZONE 'Asia/Beirut'), 'device')
				ON CONFLICT (tenant_id, employee_id, work_date) DO UPDATE SET
				  check_in = least(attendance.check_in, excluded.check_in), check_out = greatest(attendance.check_out, excluded.check_out),
				  source = 'device', updated_at = now() RETURNING id::text`,
				cid, k.emp, k.day, f.Format("2006-01-02 15:04:05"), out).Scan(&id); err != nil {
				return nil, err
			}
			if err := computeAttendance(ctx, tx, id); err != nil {
				return nil, err
			}
		}
		return map[string]any{"punches": len(punches), "days": len(order), "unknown_users": unk}, nil
	})
}

type punch struct {
	user string
	at   time.Time
}

var punchLayouts = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006/01/02 15:04:05", "2006/01/02 15:04", "02/01/2006 15:04:05",
	"02/01/2006 15:04", "1/2/2006 15:04:05", "1/2/2006 3:04:05 PM", "1/2/2006 3:04 PM", "02-01-2006 15:04", "2006-01-02T15:04:05"}

// parsePunches reads a device export: comma, semicolon or tab separated, with a header row.
func parsePunches(text string) ([]punch, bool, error) {
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\xef\xbb\xbf")
	firstLine, _, _ := strings.Cut(text, "\n")
	sep := ','
	switch {
	case strings.Count(firstLine, "\t") > strings.Count(firstLine, ","):
		sep = '\t'
	case strings.Count(firstLine, ";") > strings.Count(firstLine, ","):
		sep = ';'
	}
	rd := csv.NewReader(strings.NewReader(text))
	rd.Comma, rd.FieldsPerRecord, rd.LazyQuotes, rd.TrimLeadingSpace = sep, -1, true, true
	head, err := rd.Read()
	if err != nil {
		return nil, false, err
	}
	col := func(names ...string) int {
		for i, h := range head {
			h = strings.ToLower(strings.TrimSpace(h))
			for _, n := range names {
				if h == n {
					return i
				}
			}
		}
		return -1
	}
	cu := col("ac-no.", "ac-no", "no.", "user id", "userid", "user_id", "enroll no", "enrollno", "id", "badge", "code", "employee", "emp no")
	ct := col("time", "datetime", "date time", "date/time", "checktime", "check time", "punch time", "timestamp")
	cd := col("date")
	if cu < 0 || (ct < 0 && cd < 0) {
		return nil, true, nil
	}
	var out []punch
	for {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		get := func(i int) string {
			if i < 0 || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		v := get(ct)
		if cd >= 0 && (ct < 0 || !strings.Contains(v, " ")) {
			v = strings.TrimSpace(get(cd) + " " + v)
		}
		var at time.Time
		var ok bool
		for _, l := range punchLayouts {
			if t, err := time.Parse(l, v); err == nil {
				at, ok = t, true
				break
			}
		}
		if !ok || get(cu) == "" {
			continue
		}
		out = append(out, punch{user: get(cu), at: at})
	}
	return out, false, nil
}

// ---------------- leave ----------------

type leaveSeed struct {
	code, en, ar, fr string
	paid             bool
	days             float64
	accrual          string
}

func defaultLeaveTypes(country string) []leaveSeed {
	annual, maternity := 15.0, 70.0 // Lebanon: 15 working days a year, 10 weeks maternity
	switch country {
	case "SA":
		annual, maternity = 21, 84
	case "AE":
		annual, maternity = 30, 60
	case "KW", "QA", "BH", "OM":
		annual, maternity = 30, 60
	case "FR":
		annual, maternity = 25, 112
	case "":
		annual = 15
	default:
		annual = 21
	}
	return []leaveSeed{
		{"annual", "Annual leave", "إجازة سنوية", "Congé annuel", true, annual, "monthly"},
		{"sick", "Sick leave", "إجازة مرضية", "Congé maladie", true, 0, "none"},
		{"maternity", "Maternity leave", "إجازة أمومة", "Congé maternité", true, maternity, "yearly"},
		{"unpaid", "Unpaid leave", "إجازة غير مدفوعة", "Congé sans solde", false, 0, "none"},
	}
}

func ensureLeaveTypes(ctx context.Context, tx pgx.Tx, cid string) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM leave_types WHERE company_id = $1`, cid).Scan(&n); err != nil || n > 0 {
		return err
	}
	var country string
	if err := tx.QueryRow(ctx, `SELECT country FROM companies WHERE id = $1`, cid).Scan(&country); err != nil {
		return err
	}
	for _, l := range defaultLeaveTypes(country) {
		if _, err := tx.Exec(ctx, `INSERT INTO leave_types (company_id, code, name_en, name_ar, name_fr, paid, days_per_year, accrual)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, cid, l.code, l.en, l.ar, l.fr, l.paid, l.days, l.accrual); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) listLeaveTypes(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := ensureLeaveTypes(ctx, tx, cid); err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT id, code, name_en, name_ar, name_fr, paid, days_per_year::text AS days_per_year, accrual,
			max_carry::text AS max_carry, is_active FROM leave_types WHERE company_id = $1 ORDER BY code`, cid)
	})
}

func (s *Server) saveLeaveType(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string `json:"code"`
		NameEn   string `json:"name_en"`
		NameAr   string `json:"name_ar"`
		NameFr   string `json:"name_fr"`
		Paid     bool   `json:"paid"`
		Days     string `json:"days_per_year"`
		Accrual  string `json:"accrual"`
		MaxCarry string `json:"max_carry"`
		Active   *bool  `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	days, ok1 := money(defaultStr(in.Days, "0"))
	carry, ok2 := money(defaultStr(in.MaxCarry, "0"))
	if !ok1 || !ok2 || days.IsNegative() || carry.IsNegative() || strings.TrimSpace(in.NameEn) == "" ||
		(in.Accrual != "yearly" && in.Accrual != "monthly" && in.Accrual != "none") || (id == "" && !regexp.MustCompile(`^[a-z0-9_]{2,30}$`).MatchString(in.Code)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	active := in.Active == nil || *in.Active
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if id == "" {
			var nid string
			err := tx.QueryRow(ctx, `INSERT INTO leave_types (company_id, code, name_en, name_ar, name_fr, paid, days_per_year, accrual, max_carry, is_active)
				VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9::numeric, $10) RETURNING id::text`, cid, in.Code, strings.TrimSpace(in.NameEn),
				strings.TrimSpace(in.NameAr), strings.TrimSpace(in.NameFr), in.Paid, days.String(), in.Accrual, carry.String(), active).Scan(&nid)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "code_taken"}
			}
			return map[string]string{"id": nid}, err
		}
		if !isUUID(id) {
			return nil, notFound()
		}
		tag, err := tx.Exec(ctx, `UPDATE leave_types SET name_en = $3, name_ar = $4, name_fr = $5, paid = $6, days_per_year = $7::numeric,
			accrual = $8, max_carry = $9::numeric, is_active = $10 WHERE id = $1 AND company_id = $2`, id, cid, strings.TrimSpace(in.NameEn),
			strings.TrimSpace(in.NameAr), strings.TrimSpace(in.NameFr), in.Paid, days.String(), in.Accrual, carry.String(), active)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]string{"id": id}, nil
	})
}

func defaultStr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// leaveBalances: per leave type, entitlement for the year (prorated from the hire date; monthly accrual counts
// the months elapsed), carried over from last year (up to max_carry), taken, pending and remaining.
func leaveBalances(ctx context.Context, tx pgx.Tx, cid, emp string, year int) (json.RawMessage, error) {
	if err := ensureLeaveTypes(ctx, tx, cid); err != nil {
		return nil, err
	}
	return queryJSON(ctx, tx, `
		WITH e AS (SELECT hire_date FROM employees WHERE id = $2),
		yr AS (SELECT make_date($3, 1, 1) AS ys, make_date($3, 12, 31) AS ye),
		ent AS (SELECT t.id, t.accrual, t.days_per_year, t.max_carry,
		  -- months of service in the year (hire date) and, for monthly accrual, months elapsed
		  greatest(0, least(12, 12 - (CASE WHEN (SELECT hire_date FROM e) > (SELECT ys FROM yr)
		      THEN extract(month FROM (SELECT hire_date FROM e))::int - 1 ELSE 0 END))) AS months_service,
		  greatest(0, least(12, (CASE WHEN current_date > (SELECT ye FROM yr) THEN 12
		      WHEN current_date < (SELECT ys FROM yr) THEN 0 ELSE extract(month FROM current_date)::int END)
		    - (CASE WHEN (SELECT hire_date FROM e) > (SELECT ys FROM yr) THEN extract(month FROM (SELECT hire_date FROM e))::int - 1 ELSE 0 END))) AS months_accrued
		  FROM leave_types t WHERE t.company_id = $1 AND t.is_active)
		SELECT t.id AS type_id, t.code, t.name_en, t.name_ar, t.name_fr, t.paid, t.accrual,
		  CASE WHEN EXISTS (SELECT 1 FROM leave_accruals la WHERE la.employee_id = $2 AND la.type_id = t.id AND la.year = $3)
		       THEN (SELECT sum(la.days) FROM leave_accruals la WHERE la.employee_id = $2 AND la.type_id = t.id AND la.year = $3 AND la.kind <> 'carry')
		       ELSE CASE ent.accrual WHEN 'yearly' THEN round(t.days_per_year * ent.months_service / 12, 1)
		                   WHEN 'monthly' THEN round(t.days_per_year * ent.months_accrued / 12, 1) END END::text AS entitled,
		  CASE WHEN EXISTS (SELECT 1 FROM leave_accruals la WHERE la.employee_id = $2 AND la.type_id = t.id AND la.year = $3)
		       THEN coalesce((SELECT sum(la.days) FROM leave_accruals la WHERE la.employee_id = $2 AND la.type_id = t.id AND la.year = $3 AND la.kind = 'carry'), 0)
		  WHEN t.accrual <> 'none' AND t.max_carry > 0 THEN least(t.max_carry, greatest(0, t.days_per_year - coalesce((SELECT sum(r.days) FROM leave_requests r
		      WHERE r.employee_id = $2 AND r.type_id = t.id AND r.status = 'approved' AND extract(year FROM r.date_from) = $3 - 1), 0))) ELSE 0 END::text AS carried,
		  coalesce((SELECT sum(r.days) FROM leave_requests r WHERE r.employee_id = $2 AND r.type_id = t.id AND r.status = 'approved'
		      AND extract(year FROM r.date_from) = $3), 0)::text AS taken,
		  coalesce((SELECT sum(r.days) FROM leave_requests r WHERE r.employee_id = $2 AND r.type_id = t.id AND r.status = 'pending'
		      AND extract(year FROM r.date_from) = $3), 0)::text AS pending
		FROM leave_types t JOIN ent ON ent.id = t.id ORDER BY t.code`, cid, emp, year)
}

// workingDays counts the employee's scheduled working days in [from, to] (holidays excluded).
func workingDays(ctx context.Context, tx pgx.Tx, cid, emp, from, to string) (int, error) {
	var sched *string
	if err := tx.QueryRow(ctx, `SELECT schedule_id::text FROM employees WHERE id = $1`, emp).Scan(&sched); err != nil {
		return 0, err
	}
	p, err := countPeriodDays(ctx, tx, cid, emp, sched, from, to)
	return p.Scheduled, err
}

// createLeave records a leave request (status pending, or approved when entered by HR with approve=true).
func createLeave(ctx context.Context, tx pgx.Tx, cid, emp, typeID, from, to, reason string, approve bool) (json.RawMessage, error) {
	if to < from {
		return nil, badRequest("invalid_date")
	}
	var accrual, daysPerYear string
	if err := tx.QueryRow(ctx, `SELECT accrual, days_per_year::text FROM leave_types WHERE id = $1 AND company_id = $2 AND is_active`, typeID, cid).
		Scan(&accrual, &daysPerYear); err != nil {
		if err == pgx.ErrNoRows {
			return nil, badRequest("invalid_leave_type")
		}
		return nil, err
	}
	n, err := workingDays(ctx, tx, cid, emp, from, to)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, badRequest("no_working_days")
	}
	var overlap bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM leave_requests WHERE employee_id = $1 AND status IN ('pending', 'approved')
		AND daterange(date_from, date_to, '[]') && daterange($2::date, $3::date, '[]'))`, emp, from, to).Scan(&overlap); err != nil {
		return nil, err
	}
	if overlap {
		return nil, &apiError{status: http.StatusConflict, code: "leave_overlap"}
	}
	if accrual != "none" { // limited types: the request must fit in the remaining balance
		year, _ := time.Parse(time.DateOnly, from)
		raw, err := leaveBalances(ctx, tx, cid, emp, year.Year())
		if err != nil {
			return nil, err
		}
		var bals []struct {
			TypeID   string `json:"type_id"`
			Entitled string `json:"entitled"`
			Carried  string `json:"carried"`
			Taken    string `json:"taken"`
			Pending  string `json:"pending"`
		}
		_ = json.Unmarshal(raw, &bals)
		for _, b := range bals {
			if b.TypeID != typeID {
				continue
			}
			d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
			left := d(b.Entitled).Add(d(b.Carried)).Sub(d(b.Taken)).Sub(d(b.Pending))
			if decimal.NewFromInt(int64(n)).GreaterThan(left) {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "leave_balance", detail: left.String()}
			}
		}
	}
	status := "pending"
	if approve {
		status = "approved"
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO leave_requests (company_id, employee_id, type_id, date_from, date_to, days, status, reason, decided_by, decided_at)
		VALUES ($1, $2, $3, $4::date, $5::date, $6, $7, $8, CASE WHEN $7 = 'approved' THEN app_user() END, CASE WHEN $7 = 'approved' THEN now() END)
		RETURNING id::text`, cid, emp, typeID, from, to, n, status, strings.TrimSpace(reason)).Scan(&id); err != nil {
		return nil, err
	}
	return queryJSONOne(ctx, tx, `SELECT id, days::text AS days, status FROM leave_requests WHERE id = $1`, id)
}

type leaveIn struct {
	EmployeeID string `json:"employee_id"`
	TypeID     string `json:"type_id"`
	From       string `json:"date_from"`
	To         string `json:"date_to"`
	Reason     string `json:"reason"`
	Approve    bool   `json:"approve"`
}

func (in leaveIn) valid(withEmp bool) bool {
	return (!withEmp || isUUID(in.EmployeeID)) && isUUID(in.TypeID) && validDate(in.From) && validDate(in.To) && len(in.Reason) <= 500
}

// POST /hr/leave: HR records a request for an employee (approve=true records it as approved).
func (s *Server) createLeaveRequest(w http.ResponseWriter, r *http.Request) {
	var in leaveIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid(true) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	approve := in.Approve && can(r, "payroll.approve")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM employees WHERE id = $1 AND company_id = $2)`, in.EmployeeID, cid).Scan(&ok); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			return nil, notFound()
		}
		return createLeave(ctx, tx, cid, in.EmployeeID, in.TypeID, in.From, in.To, in.Reason, approve)
	})
}

// GET /hr/leave?status=&year=
func (s *Server) listLeaveRequests(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	year := queryInt(r, "year", time.Now().Year(), 2200)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT r.id, r.employee_id, e.code, e.name, e.department, r.type_id, t.code AS type_code, t.name_en, t.name_ar, t.name_fr,
			t.paid, r.date_from, r.date_to, r.days::text AS days, r.status, r.reason, r.decision_note, r.created_at, r.decided_at,
			(SELECT full_name FROM users u WHERE u.id = r.decided_by) AS decided_by
			FROM leave_requests r JOIN employees e ON e.id = r.employee_id JOIN leave_types t ON t.id = r.type_id
			WHERE r.company_id = $1 AND ($2 = '' OR r.status = $2) AND (extract(year FROM r.date_from) = $3 OR r.status = 'pending')
			ORDER BY (r.status = 'pending') DESC, r.date_from DESC LIMIT 2000`, r.PathValue("cid"), status, year)
	})
}

// POST /hr/leave/{id}/decide {decision: approve|reject, note}: payroll approvers, or the employee's manager.
func (s *Server) decideLeave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.Decision != "approve" && in.Decision != "reject") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	approver := can(r, "payroll.approve")
	me := principal(r).ID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		var mgrUser, empUser *string
		err := tx.QueryRow(ctx, `SELECT r.status, (SELECT m.user_id::text FROM employees m WHERE m.id = e.manager_id), e.user_id::text
			FROM leave_requests r JOIN employees e ON e.id = r.employee_id WHERE r.id = $1 AND r.company_id = $2 FOR UPDATE OF r`, id, cid).
			Scan(&status, &mgrUser, &empUser)
		if err == pgx.ErrNoRows {
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
		next := map[string]string{"approve": "approved", "reject": "rejected"}[in.Decision]
		if _, err := tx.Exec(ctx, `UPDATE leave_requests SET status = $2, decided_by = app_user(), decided_at = now(), decision_note = $3 WHERE id = $1`,
			id, next, strings.TrimSpace(in.Note)); err != nil {
			return nil, err
		}
		return map[string]string{"id": id, "status": next}, nil
	})
}

// POST /hr/leave/{id}/cancel (HR): a pending or approved request is cancelled.
func (s *Server) cancelLeave(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		tag, err := tx.Exec(ctx, `UPDATE leave_requests SET status = 'cancelled', decided_by = app_user(), decided_at = now()
			WHERE id = $1 AND company_id = $2 AND status IN ('pending', 'approved')`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return map[string]bool{"ok": true}, nil
	})
}

// GET /hr/leave/balances?year=: every active employee × leave type.
func (s *Server) leaveBalanceReport(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	year := queryInt(r, "year", time.Now().Year(), 2200)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT id::text, code, name FROM employees WHERE company_id = $1 AND status = 'active' ORDER BY name`, cid)
		if err != nil {
			return nil, err
		}
		type emp struct{ id, code, name string }
		emps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (emp, error) {
			var e emp
			err := r.Scan(&e.id, &e.code, &e.name)
			return e, err
		})
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, e := range emps {
			b, err := leaveBalances(ctx, tx, cid, e.id, year)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"employee_id": e.id, "code": e.code, "name": e.name, "balances": b})
		}
		return out, nil
	})
}

// ---------------- payroll rules ----------------

// GET /hr/payroll-params?date=: values in force (country defaults + overrides) and the override history.
func (s *Server) getPayrollParams(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = today()
	}
	if !validDate(date) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		p, country, err := loadPayrollParams(ctx, tx, cid, date)
		if err != nil {
			return nil, err
		}
		over, err := queryJSON(ctx, tx, `SELECT id, key, value, effective_from, created_at FROM payroll_params WHERE company_id = $1
			ORDER BY key, effective_from DESC`, cid)
		if err != nil {
			return nil, err
		}
		defaults := []map[string]string{}
		for _, d := range append(append([]ruleParam{}, genericDefaults...), payrollDefaults[country]...) {
			defaults = append(defaults, map[string]string{"key": d.key, "value": d.value, "effective_from": d.from})
		}
		var wpsID, wpsBank string
		if err := tx.QueryRow(ctx, `SELECT wps_employer_id, wps_bank_code FROM companies WHERE id = $1`, cid).Scan(&wpsID, &wpsBank); err != nil {
			return nil, err
		}
		return map[string]any{"country": country, "date": date, "values": p, "keys": payrollParamKeys, "defaults": defaults, "overrides": over,
			"wps_employer_id": wpsID, "wps_bank_code": wpsBank}, nil
	})
}

// POST /hr/payroll-params {key, value, effective_from} (value "" removes that dated override)
func (s *Server) putPayrollParam(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key     string  `json:"key"`
		Value   string  `json:"value"`
		From    string  `json:"effective_from"`
		WpsID   *string `json:"wps_employer_id"`
		WpsBank *string `json:"wps_bank_code"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid := r.PathValue("cid")
	known := false
	for _, k := range payrollParamKeys {
		known = known || k == in.Key
	}
	if in.Key != "" && (!known || !validDate(in.From) || len(in.Value) > 500) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Key != "" && in.Value != "" && in.Key != "legal_currency" && in.Key != "social_label" && in.Key != "social_nationals" &&
		in.Key != "tax_brackets" && in.Key != "gratuity" {
		if _, ok := money(in.Value); !ok {
			writeErr(w, http.StatusBadRequest, "invalid_amount")
			return
		}
	}
	if in.Key == "tax_brackets" && in.Value != "" && !regexp.MustCompile(`^(\d+(\.\d+)?:\d+(\.\d+)?,)*:?\d+(\.\d+)?$|^(\d+:\d+(\.\d+)?,)*:\d+(\.\d+)?$`).MatchString(strings.ReplaceAll(in.Value, " ", "")) {
		writeErr(w, http.StatusBadRequest, "invalid_brackets")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.WpsID != nil || in.WpsBank != nil {
			if _, err := tx.Exec(ctx, `UPDATE companies SET wps_employer_id = coalesce(trim($2), wps_employer_id), wps_bank_code = coalesce(trim($3), wps_bank_code)
				WHERE id = $1`, cid, in.WpsID, in.WpsBank); err != nil {
				return nil, err
			}
		}
		if in.Key != "" {
			if in.Value == "" {
				if _, err := tx.Exec(ctx, `DELETE FROM payroll_params WHERE company_id = $1 AND key = $2 AND effective_from = $3::date`, cid, in.Key, in.From); err != nil {
					return nil, err
				}
			} else if _, err := tx.Exec(ctx, `INSERT INTO payroll_params (company_id, key, value, effective_from) VALUES ($1, $2, $3, $4::date)
				ON CONFLICT (tenant_id, company_id, key, effective_from) DO UPDATE SET value = excluded.value`, cid, in.Key, strings.TrimSpace(in.Value), in.From); err != nil {
				return nil, err
			}
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ---------------- overview / alerts ----------------

func (s *Server) hrOverview(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		counts, err := queryJSONOne(ctx, tx, `SELECT
			(SELECT count(*) FROM employees WHERE company_id = $1 AND status = 'active') AS active,
			(SELECT count(*) FROM attendance a JOIN employees e ON e.id = a.employee_id WHERE a.company_id = $1 AND a.work_date = current_date AND a.check_in IS NOT NULL) AS present_today,
			(SELECT count(*) FROM attendance WHERE company_id = $1 AND work_date = current_date AND late_minutes > 0) AS late_today,
			(SELECT count(*) FROM leave_requests WHERE company_id = $1 AND status = 'approved' AND current_date BETWEEN date_from AND date_to) AS on_leave_today,
			(SELECT count(*) FROM leave_requests WHERE company_id = $1 AND status = 'pending') AS pending_leave,
			(SELECT count(*) FROM attendance WHERE company_id = $1 AND work_date < current_date AND work_date > current_date - 14 AND check_in IS NOT NULL AND check_out IS NULL) AS missing_out`, cid)
		if err != nil {
			return nil, err
		}
		contracts, err := queryJSON(ctx, tx, `SELECT id, code, name, contract_type, contract_end FROM employees WHERE company_id = $1 AND status = 'active'
			AND contract_end IS NOT NULL AND contract_end <= current_date + 45 ORDER BY contract_end`, cid)
		if err != nil {
			return nil, err
		}
		docs, err := queryJSON(ctx, tx, `SELECT d.id, d.employee_id, e.name, d.name AS document, d.expires_on FROM employee_documents d
			JOIN employees e ON e.id = d.employee_id WHERE d.company_id = $1 AND e.status = 'active' AND d.expires_on IS NOT NULL
			AND d.expires_on <= current_date + 45 ORDER BY d.expires_on`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"counts": counts, "contracts_ending": contracts, "documents_expiring": docs}, nil
	})
}

// ---------------- employee self-service ----------------

// selfEmployee: the employee record linked to the signed-in user in this company.
func selfEmployee(ctx context.Context, tx pgx.Tx, cid, user string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM employees WHERE company_id = $1 AND user_id = $2 AND status = 'active'`, cid, user).Scan(&id)
	if err == pgx.ErrNoRows {
		return "", &apiError{status: http.StatusNotFound, code: "no_employee_file"}
	}
	return id, err
}

func (s *Server) selfHome(w http.ResponseWriter, r *http.Request) {
	cid, me := r.PathValue("cid"), principal(r).ID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := selfEmployee(ctx, tx, cid, me)
		if err != nil {
			return nil, err
		}
		info, err := queryJSONOne(ctx, tx, `SELECT e.id, e.code, e.name, e.position, e.department, e.hire_date, e.photo, e.currency, e.currency2,
			(SELECT m.name FROM employees m WHERE m.id = e.manager_id) AS manager_name,
			coalesce((SELECT sum(a.amount - a.recovered) FROM employee_advances a WHERE a.employee_id = e.id), 0)::text AS advances_open
			FROM employees e WHERE e.id = $1`, emp)
		if err != nil {
			return nil, err
		}
		todayRow, err := queryJSON(ctx, tx, `SELECT work_date, check_in, check_out, worked_minutes, late_minutes FROM attendance
			WHERE employee_id = $1 AND work_date >= current_date - 7 ORDER BY work_date DESC`, emp)
		if err != nil {
			return nil, err
		}
		bal, err := leaveBalances(ctx, tx, cid, emp, time.Now().Year())
		if err != nil {
			return nil, err
		}
		leave, err := queryJSON(ctx, tx, `SELECT r.id, r.date_from, r.date_to, r.days::text AS days, r.status, r.reason, t.code AS type_code, t.name_en,
			t.name_ar, t.name_fr FROM leave_requests r JOIN leave_types t ON t.id = r.type_id WHERE r.employee_id = $1 ORDER BY r.date_from DESC LIMIT 20`, emp)
		if err != nil {
			return nil, err
		}
		types, err := queryJSON(ctx, tx, `SELECT id, code, name_en, name_ar, name_fr FROM leave_types WHERE company_id = $1 AND is_active ORDER BY code`, cid)
		if err != nil {
			return nil, err
		}
		slips, err := queryJSON(ctx, tx, `SELECT p.id, r.period, p.currency, p.net::text AS net, p.paid::text AS paid, p.currency2, p.net2::text AS net2
			FROM payslips p JOIN payroll_runs r ON r.id = p.run_id WHERE p.employee_id = $1 AND r.status = 'posted' ORDER BY r.period DESC LIMIT 24`, emp)
		if err != nil {
			return nil, err
		}
		adv, err := queryJSON(ctx, tx, `SELECT advance_date, currency, amount::text AS amount, recovered::text AS recovered, installment::text AS installment
			FROM employee_advances WHERE employee_id = $1 ORDER BY advance_date DESC LIMIT 20`, emp)
		if err != nil {
			return nil, err
		}
		return map[string]any{"employee": info, "attendance": todayRow, "leave_balances": bal, "leave": leave, "leave_types": types,
			"payslips": slips, "advances": adv}, nil
	})
}

// POST /self/clock {lat, lng}: first call of the day = check in, next = check out.
func (s *Server) selfClock(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var in struct {
		Lat   *float64 `json:"lat"`
		Lng   *float64 `json:"lng"`
		Photo string   `json:"photo"` // optional selfie (base64 JPEG/PNG, data URL allowed)
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if (in.Lat != nil && (*in.Lat < -90 || *in.Lat > 90)) || (in.Lng != nil && (*in.Lng < -180 || *in.Lng > 180)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	photo, mime, perr := decodePhoto(in.Photo)
	if perr != nil {
		writeErr(w, http.StatusBadRequest, "invalid_photo")
		return
	}
	cid, me := r.PathValue("cid"), principal(r).ID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := selfEmployee(ctx, tx, cid, me)
		if err != nil {
			return nil, err
		}
		if err := checkClockRules(ctx, tx, cid, in.Lat, in.Lng, len(photo) > 0); err != nil {
			return nil, err
		}
		var id string
		var in1 *time.Time
		err = tx.QueryRow(ctx, `SELECT id::text, check_in FROM attendance WHERE employee_id = $1 AND work_date = (now() AT TIME ZONE 'Asia/Beirut')::date FOR UPDATE`, emp).
			Scan(&id, &in1)
		action := "in"
		switch {
		case err == pgx.ErrNoRows:
			err = tx.QueryRow(ctx, `INSERT INTO attendance (company_id, employee_id, work_date, check_in, source, in_lat, in_lng)
				VALUES ($1, $2, (now() AT TIME ZONE 'Asia/Beirut')::date, now(), 'phone', $3, $4) RETURNING id::text`, cid, emp, in.Lat, in.Lng).Scan(&id)
		case err != nil:
		case in1 == nil:
			_, err = tx.Exec(ctx, `UPDATE attendance SET check_in = now(), source = 'phone', in_lat = $2, in_lng = $3, updated_at = now() WHERE id = $1`, id, in.Lat, in.Lng)
		default:
			action = "out"
			_, err = tx.Exec(ctx, `UPDATE attendance SET check_out = now(), out_lat = $2, out_lng = $3, updated_at = now() WHERE id = $1`, id, in.Lat, in.Lng)
		}
		if err != nil {
			return nil, err
		}
		if err := computeAttendance(ctx, tx, id); err != nil {
			return nil, err
		}
		if len(photo) > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO attendance_photos (company_id, attendance_id, kind, mime, data) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (attendance_id, kind) DO UPDATE SET data = excluded.data, mime = excluded.mime, created_at = now()`, cid, id, action, mime, photo); err != nil {
				return nil, err
			}
		}
		row, err := queryJSONOne(ctx, tx, `SELECT work_date, check_in, check_out, worked_minutes, late_minutes FROM attendance WHERE id = $1`, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"action": action, "attendance": row}, nil
	})
}

// POST /self/leave {type_id, date_from, date_to, reason}
func (s *Server) selfLeave(w http.ResponseWriter, r *http.Request) {
	var in leaveIn
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
		return createLeave(ctx, tx, cid, emp, in.TypeID, in.From, in.To, in.Reason, false)
	})
}

// POST /self/leave/{id}/cancel: the employee withdraws a pending request.
func (s *Server) selfCancelLeave(w http.ResponseWriter, r *http.Request) {
	cid, id, me := r.PathValue("cid"), r.PathValue("id"), principal(r).ID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := selfEmployee(ctx, tx, cid, me)
		if err != nil {
			return nil, err
		}
		if !isUUID(id) {
			return nil, notFound()
		}
		tag, err := tx.Exec(ctx, `UPDATE leave_requests SET status = 'cancelled' WHERE id = $1 AND employee_id = $2 AND status = 'pending'`, id, emp)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return map[string]bool{"ok": true}, nil
	})
}

// GET /self/payslips/{pid}
func (s *Server) selfPayslip(w http.ResponseWriter, r *http.Request) {
	cid, pid, me := r.PathValue("cid"), r.PathValue("pid"), principal(r).ID
	if !isUUID(pid) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := selfEmployee(ctx, tx, cid, me)
		if err != nil {
			return nil, err
		}
		var posted bool
		if err := tx.QueryRow(ctx, `SELECT r.status = 'posted' FROM payslips p JOIN payroll_runs r ON r.id = p.run_id WHERE p.id = $1 AND p.employee_id = $2`,
			pid, emp).Scan(&posted); err != nil || !posted {
			return nil, notFound()
		}
		return loadPayslip(ctx, tx, cid, pid, "", emp)
	})
}

// ---------------- reports ----------------

// GET /reports/hr/{kind}?period=YYYY-MM | from=&to= | year=&group=
// kinds: register, social, tax, cost, attendance, leave
func (s *Server) hrReport(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	q := r.URL.Query()
	period, from, to, group := q.Get("period"), q.Get("from"), q.Get("to"), q.Get("group")
	if period != "" && !validPeriod(period) {
		writeErr(w, http.StatusBadRequest, "invalid_period")
		return
	}
	if period == "" && from == "" {
		period = time.Now().Format("2006-01")
	}
	if period != "" {
		from, to = period+"-01", periodEnd(period)
	}
	if !validDate(from) || !validDate(to) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		runs := `SELECT id FROM payroll_runs WHERE company_id = $1 AND status = 'posted' AND period BETWEEN to_char($2::date, 'YYYY-MM') AND to_char($3::date, 'YYYY-MM')`
		switch kind {
		case "register": // one row per payslip, the lines as columns on the client
			return queryJSON(ctx, tx, `SELECT p.id, r.period, e.code, e.name, e.department, e.nssf_number, p.currency, p.net::text AS net, p.currency2,
				p.net2::text AS net2, p.worked_days::text AS worked_days, p.absent_days::text AS absent_days, p.overtime_hours::text AS overtime_hours,
				coalesce((SELECT json_agg(json_build_object('code', l.code, 'label', l.label, 'kind', l.kind, 'currency', l.currency, 'amount', l.amount::text)
				  ORDER BY l.seq) FROM payslip_lines l WHERE l.payslip_id = p.id), '[]'::json) AS lines,
				p.base::text AS base, p.transport::text AS transport, p.allowances::text AS allowances, p.deductions::text AS deductions,
				p.nssf_employee::text AS nssf_employee, p.nssf_employer::text AS nssf_employer, p.advance_recovery::text AS advance_recovery
				FROM payslips p JOIN payroll_runs r ON r.id = p.run_id JOIN employees e ON e.id = p.employee_id
				WHERE p.run_id IN (`+runs+`) ORDER BY r.period, e.name`, cid, from, to)
		case "social": // social security declaration: base, employee and employer shares per employee
			return queryJSON(ctx, tx, `SELECT e.code, e.name, e.nssf_number, e.national_id, l.currency,
				sum(l.amount) FILTER (WHERE l.code = 'social_ee')::text AS employee_share,
				sum(l.amount) FILTER (WHERE l.code = 'social_er')::text AS employer_share,
				sum(l.amount) FILTER (WHERE l.code = 'family_er')::text AS family_employer,
				sum(l.amount) FILTER (WHERE l.code = 'eos_er')::text AS eos_employer,
				sum(l.amount) FILTER (WHERE l.code = 'family_allow')::text AS family_allowance_paid,
				sum(l.amount) FILTER (WHERE l.role = 'social' OR l.code = 'family_allow')::text AS total,
				min(l.basis) FILTER (WHERE l.code = 'social_ee') AS basis
				FROM payslip_lines l JOIN payslips p ON p.id = l.payslip_id JOIN employees e ON e.id = p.employee_id
				WHERE p.run_id IN (`+runs+`) AND (l.role = 'social' OR l.code = 'family_allow')
				GROUP BY e.code, e.name, e.nssf_number, e.national_id, l.currency ORDER BY e.name`, cid, from, to)
		case "tax":
			return queryJSON(ctx, tx, `SELECT e.code, e.name, e.national_id, e.marital_status, e.children, l.currency, sum(l.amount)::text AS tax,
				string_agg(DISTINCT l.basis, ' | ') AS basis
				FROM payslip_lines l JOIN payslips p ON p.id = l.payslip_id JOIN employees e ON e.id = p.employee_id
				WHERE p.run_id IN (`+runs+`) AND l.role = 'tax' GROUP BY e.code, e.name, e.national_id, e.marital_status, e.children, l.currency ORDER BY e.name`, cid, from, to)
		case "cost": // earnings + employer contributions per employee / department / cost center
			g := "e.name"
			switch group {
			case "department":
				g = "coalesce(nullif(e.department, ''), '—')"
			case "cost_center":
				g = "coalesce((SELECT c.name FROM cost_centers c WHERE c.id = e.cost_center_id), '—')"
			}
			return queryJSON(ctx, tx, `SELECT `+g+` AS label, x.currency, sum(x.earnings)::text AS earnings, sum(x.employer)::text AS employer,
				sum(x.earnings + x.employer)::text AS total, count(DISTINCT x.employee_id) AS employees FROM (
				  SELECT p.employee_id, l.currency,
				    CASE WHEN l.kind = 'earning' THEN l.amount WHEN l.kind = 'deduction' AND l.role = 'salary' THEN -l.amount ELSE 0 END AS earnings,
				    CASE WHEN l.kind = 'employer' THEN l.amount ELSE 0 END AS employer
				  FROM payslip_lines l JOIN payslips p ON p.id = l.payslip_id WHERE p.run_id IN (`+runs+`)
				  UNION ALL
				  SELECT p.employee_id, p.currency, p.base + p.transport + p.allowances - p.deductions, p.nssf_employer
				  FROM payslips p WHERE p.run_id IN (`+runs+`) AND NOT p.detailed) x
				JOIN employees e ON e.id = x.employee_id GROUP BY 1, 2 ORDER BY 1, 2`, cid, from, to)
		case "attendance":
			return queryJSON(ctx, tx, `SELECT e.code, e.name, e.department, count(a.id) FILTER (WHERE a.check_in IS NOT NULL) AS present_days,
				coalesce(sum(a.worked_minutes), 0) AS worked_minutes, count(a.id) FILTER (WHERE a.late_minutes > 0) AS late_days,
				coalesce(sum(a.late_minutes), 0) AS late_minutes, coalesce(sum(a.overtime_minutes), 0) AS overtime_minutes,
				count(a.id) FILTER (WHERE a.check_in IS NOT NULL AND a.check_out IS NULL) AS missing_out
				FROM employees e LEFT JOIN attendance a ON a.employee_id = e.id AND a.work_date BETWEEN $2::date AND $3::date
				WHERE e.company_id = $1 AND e.status = 'active' GROUP BY e.code, e.name, e.department ORDER BY e.name`, cid, from, to)
		case "leave":
			return queryJSON(ctx, tx, `SELECT e.code, e.name, t.code AS type_code, t.name_en, t.name_ar, t.name_fr, r.date_from, r.date_to,
				r.days::text AS days, r.status FROM leave_requests r JOIN employees e ON e.id = r.employee_id JOIN leave_types t ON t.id = r.type_id
				WHERE r.company_id = $1 AND r.date_from <= $3::date AND r.date_to >= $2::date AND r.status IN ('approved', 'pending')
				ORDER BY r.date_from, e.name`, cid, from, to)
		}
		return nil, notFound()
	})
}

// GET /self/team-leave: pending requests of the employees the signed-in user manages (approve from the phone).
func (s *Server) selfTeamLeave(w http.ResponseWriter, r *http.Request) {
	cid, me := r.PathValue("cid"), principal(r).ID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		mgr, err := selfEmployee(ctx, tx, cid, me)
		if err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT r.id, e.name, t.code AS type_code, t.name_en, t.name_ar, t.name_fr, r.date_from, r.date_to,
			r.days::text AS days, r.reason FROM leave_requests r JOIN employees e ON e.id = r.employee_id JOIN leave_types t ON t.id = r.type_id
			WHERE r.company_id = $1 AND e.manager_id = $2 AND r.status = 'pending' ORDER BY r.date_from`, cid, mgr)
	})
}
