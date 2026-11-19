package httpapi

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// Module U: HR & payroll — detailed payslips with Lebanese rules, split salary, leave, approval,
// self-service, attendance import, WPS file.

func slipOf(t *testing.T, body map[string]any, name string) map[string]any {
	t.Helper()
	for _, s := range body["payslips"].([]any) {
		if str(obj(s)["name"]) == name {
			return obj(s)
		}
	}
	t.Fatalf("no payslip for %s", name)
	return nil
}

func lineAmount(slip map[string]any, code string) decimal.Decimal {
	total := decimal.Zero
	for _, l := range slip["lines"].([]any) {
		if str(obj(l)["code"]) == code {
			v, _ := decimal.NewFromString(str(obj(l)["amount"]))
			total = total.Add(v)
		}
	}
	return total
}

func TestPayrollLebanonSplitSalary(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	sched := str(o.c.ok("POST", o.p("/hr/schedules"), map[string]any{"name": "Office", "start_time": "08:00", "end_time": "16:00",
		"break_minutes": 30, "grace_minutes": 10, "work_days": []int{1, 2, 3, 4, 5}}, 201).body["id"])
	id := str(o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Ali", "currency": "USD", "salary": "1000", "hire_date": "2026-01-01"}, 201).body["id"])
	o.c.ok("PUT", o.p("/employees/"+id+"/file"), map[string]any{"currency2": "LBP", "salary2": "10000000", "marital_status": "married",
		"children": 2, "transport_daily": true, "schedule_id": sched, "nssf_number": "123456", "national_id": "784-1990-1234567-1",
		"bank_account": "AE07 0331 2345 6789 0123 456", "bank_routing": "803320101", "device_id": "7",
		"components": []map[string]any{{"kind": "allowance", "name": "Phone", "currency": "USD", "amount": "20", "taxable": true}}}, 200)
	file := o.c.ok("GET", o.p("/employees/"+id+"/file"), nil, 200)
	if str(obj(file.body["employee"])["currency2"]) != "LBP" || len(file.body["components"].([]any)) != 1 {
		t.Fatalf("file: %s", file.raw)
	}

	// one day of unpaid leave in November (a Monday), approved by HR
	types := o.c.ok("GET", o.p("/hr/leave-types"), nil, 200)
	var unpaid, annual string
	for _, x := range types.list {
		switch str(obj(x)["code"]) {
		case "unpaid":
			unpaid = str(obj(x)["id"])
		case "annual":
			annual = str(obj(x)["id"])
		}
	}
	if unpaid == "" || annual == "" {
		t.Fatalf("leave types: %s", types.raw)
	}
	o.c.ok("POST", o.p("/hr/leave"), map[string]any{"employee_id": id, "type_id": unpaid, "date_from": "2026-11-02", "date_to": "2026-11-02", "approve": true}, 201)
	// more annual leave than accrued is refused
	if r := o.c.do("POST", o.p("/hr/leave"), map[string]any{"employee_id": id, "type_id": annual, "date_from": "2026-11-09", "date_to": "2026-12-31"}); r.status != 422 {
		t.Fatalf("leave balance: %d %s", r.status, r.raw)
	}

	run := o.c.ok("POST", o.p("/payroll"), map[string]any{"period": "2026-11"}, 201)
	runID := str(obj(run.body["run"])["id"])
	s := slipOf(t, run.body, "Ali")
	if str(s["currency2"]) != "LBP" || s["detailed"] != true {
		t.Fatalf("slip: %v", s)
	}
	// NSSF base = 1000 × 89,500 + 10,000,000 − absence (1/26 of each part) ; employee share 3 % in LBP (part 2)
	base := decimal.NewFromInt(89500000 + 10000000)
	absUSD := decimal.NewFromInt(1000).Div(decimal.NewFromInt(26)).Round(2)
	absLBP := decimal.NewFromInt(10000000).Div(decimal.NewFromInt(26)).Round(0)
	base = base.Sub(absUSD.Mul(decimal.NewFromInt(89500))).Sub(absLBP)
	if got, want := lineAmount(s, "social_ee"), base.Mul(decimal.NewFromInt(3)).Div(decimal.NewFromInt(100)).Round(0); !got.Equal(want) {
		t.Fatalf("social_ee = %s, want %s", got, want)
	}
	eqDec(t, "family allowance", lineAmount(s, "family_allow").String(), "4410000") // spouse 2,100,000 + 2 × 1,155,000
	if !lineAmount(s, "income_tax").IsPositive() || !lineAmount(s, "transport_daily").IsPositive() || !lineAmount(s, "absence").Equal(absUSD) {
		t.Fatalf("lines: %v", s["lines"])
	}
	eqDec(t, "phone", lineAmount(s, "component").String(), "20")

	// approval locks the payroll; back to draft unlocks it
	o.c.ok("POST", o.p("/payroll/"+runID+"/approve"), map[string]any{}, 200)
	if r := o.c.do("PATCH", o.p("/payroll/"+runID+"/payslips/"+str(s["id"])), map[string]any{"allowances": "10"}); r.status != 409 {
		t.Fatalf("approved payslip edited: %d", r.status)
	}
	o.c.ok("POST", o.p("/payroll/"+runID+"/unapprove"), map[string]any{}, 200)
	ed := o.c.ok("PATCH", o.p("/payroll/"+runID+"/payslips/"+str(s["id"])), map[string]any{"allowances": "10"}, 200)
	s2 := slipOf(t, ed.body, "Ali")
	if !dec(t, s2["net"]).GreaterThan(dec(t, s["net"])) {
		t.Fatalf("manual allowance not applied: %v → %v", s["net"], s2["net"])
	}
	o.c.ok("POST", o.p("/payroll/"+runID+"/approve"), map[string]any{}, 200)
	o.c.ok("POST", o.p("/payroll/"+runID+"/post"), map[string]any{}, 200)
	o.trialBalanced(t)
	o.c.ok("POST", o.p("/payroll/"+runID+"/pay"), map[string]any{"method_id": o.methods["Cash USD"], "date": "2026-12-01"}, 200)
	paid := o.c.ok("POST", o.p("/payroll/"+runID+"/pay"), map[string]any{"method_id": o.methods["Cash LBP"], "date": "2026-12-01"}, 200)
	p := slipOf(t, paid.body, "Ali")
	eqDec(t, "paid", p["paid"], str(p["net"]))
	eqDec(t, "paid2", p["paid2"], str(p["net2"]))
	o.trialBalanced(t)

	// payslip detail, reports, WPS (needs the employer id), end of service
	o.c.ok("GET", o.p("/payroll/"+runID+"/payslips/"+str(s["id"])), nil, 200)
	soc := o.c.ok("GET", o.p("/reports/hr/social?period=2026-11"), nil, 200)
	if len(soc.list) == 0 {
		t.Fatalf("social report: %s", soc.raw)
	}
	o.c.ok("GET", o.p("/reports/hr/tax?period=2026-11"), nil, 200)
	o.c.ok("GET", o.p("/reports/hr/cost?period=2026-11&group=department"), nil, 200)
	if r := o.c.do("GET", o.p("/payroll/"+runID+"/wps"), nil); r.status != 400 {
		t.Fatalf("wps without employer id: %d", r.status)
	}
	o.c.ok("POST", o.p("/hr/payroll-params"), map[string]any{"wps_employer_id": "0000123456789", "wps_bank_code": "803320101"}, 200)
	if r := o.c.do("GET", o.p("/payroll/"+runID+"/wps"), nil); r.status != 200 || !strings.Contains(r.raw, "SCR,0000123456789") || !strings.Contains(r.raw, "EDR,784") {
		t.Fatalf("wps: %d %s", r.status, r.raw)
	}
	eos := o.c.ok("GET", o.p("/employees/"+id+"/end-of-service?date=2026-12-31"), nil, 200)
	if str(eos.body["mode"]) != "nssf" || !dec(t, eos.body["contributions"]).IsPositive() {
		t.Fatalf("eos: %s", eos.raw)
	}

	// a dated rule override changes the next payroll (Lebanese tax off from December)
	o.c.ok("POST", o.p("/hr/payroll-params"), map[string]any{"key": "tax_brackets", "value": ":0", "effective_from": "2026-12-01"}, 200)
	dec2 := o.c.ok("POST", o.p("/payroll"), map[string]any{"period": "2026-12"}, 201)
	if lineAmount(slipOf(t, dec2.body, "Ali"), "income_tax").IsPositive() {
		t.Fatalf("tax override ignored")
	}
}

func TestSelfServiceAttendanceLeave(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	role := o.c.ok("POST", "/roles", map[string]any{"name": "Employee", "base": "read_only", "permissions": []string{}}, 201)
	email := "emp" + randHex(4) + "@example.com"
	user := o.c.ok("POST", "/users", map[string]any{"email": email, "full_name": "Sara", "role_id": str(role.body["id"]), "locale": "en",
		"password": "sara-secret-99"}, 201)
	mgrEmail := "mgr" + randHex(4) + "@example.com"
	mgrUser := o.c.ok("POST", "/users", map[string]any{"email": mgrEmail, "full_name": "Mona", "role_id": str(role.body["id"]), "locale": "en",
		"password": "mona-secret-99"}, 201)
	mgr := str(o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Mona", "currency": "USD", "salary": "1500", "hire_date": "2025-01-01",
		"user_id": str(mgrUser.body["id"])}, 201).body["id"])
	emp := str(o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Sara", "currency": "USD", "salary": "800", "hire_date": "2025-01-01",
		"user_id": str(user.body["id"]), "code": "S1"}, 201).body["id"])
	o.c.ok("PUT", o.p("/employees/"+emp+"/file"), map[string]any{"manager_id": mgr, "device_id": "42"}, 200)

	sara := loginAs(t, email, "sara-secret-99")
	home := sara.ok("GET", o.p("/self"), nil, 200)
	if str(obj(home.body["employee"])["name"]) != "Sara" {
		t.Fatalf("self: %s", home.raw)
	}
	// the employee role cannot read other employees
	if r := sara.do("GET", o.p("/employees"), nil); r.status != 403 {
		t.Fatalf("employee list as employee: %d", r.status)
	}
	in := sara.ok("POST", o.p("/self/clock"), map[string]any{"lat": 33.89, "lng": 35.5}, 200)
	out := sara.ok("POST", o.p("/self/clock"), map[string]any{"lat": 33.89, "lng": 35.5}, 200)
	if str(in.body["action"]) != "in" || str(out.body["action"]) != "out" {
		t.Fatalf("clock: %s / %s", in.raw, out.raw)
	}
	var sick string
	for _, x := range home.body["leave_types"].([]any) {
		if str(obj(x)["code"]) == "sick" {
			sick = str(obj(x)["id"])
		}
	}
	req := sara.ok("POST", o.p("/self/leave"), map[string]any{"type_id": sick, "date_from": "2026-12-14", "date_to": "2026-12-15", "reason": "flu"}, 201)
	if str(req.body["status"]) != "pending" {
		t.Fatalf("leave: %s", req.raw)
	}
	// the employee cannot approve their own request; the manager can
	if r := sara.do("POST", o.p("/hr/leave/"+str(req.body["id"])+"/decide"), map[string]any{"decision": "approve"}); r.status != 403 {
		t.Fatalf("self approval: %d", r.status)
	}
	mona := loginAs(t, mgrEmail, "mona-secret-99")
	team := mona.ok("GET", o.p("/self/team-leave"), nil, 200)
	if len(team.list) != 1 {
		t.Fatalf("team leave: %s", team.raw)
	}
	mona.ok("POST", o.p("/hr/leave/"+str(req.body["id"])+"/decide"), map[string]any{"decision": "approve"}, 200)

	// fingerprint export: device user 42 → Sara (first punch in, last out)
	csv := "AC-No.,Name,Time,State\n42,Sara,2026-12-01 08:25:00,C/In\n42,Sara,2026-12-01 13:00:00,C/Out\n42,Sara,2026-12-01 18:10:00,C/Out\n99,Ghost,2026-12-01 08:00:00,C/In\n"
	imp := o.c.ok("POST", o.p("/hr/attendance/import"), map[string]any{"csv": csv}, 200)
	if dec(t, imp.body["days"]).IntPart() != 1 || len(imp.body["unknown_users"].([]any)) != 1 {
		t.Fatalf("import: %s", imp.raw)
	}
	day := o.c.ok("GET", o.p("/hr/attendance?date=2026-12-01"), nil, 200)
	for _, x := range day.list {
		if str(obj(x)["name"]) == "Sara" && obj(x)["check_out"] == nil {
			t.Fatalf("attendance: %s", day.raw)
		}
	}
	o.c.ok("GET", o.p("/hr/overview"), nil, 200)
	o.c.ok("GET", o.p("/hr/leave/balances?year=2026"), nil, 200)
	o.c.ok("GET", o.p("/reports/hr/attendance?period=2026-12"), nil, 200)
}

// The ready-made self-service login: one call creates the user with the Employee role and links it.
func TestEmployeeLoginPreset(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	emp := str(o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Rami", "currency": "USD", "salary": "700", "hire_date": "2025-01-01"}, 201).body["id"])
	email := "rami" + randHex(4) + "@example.com"
	res := o.c.ok("POST", o.p("/employees/"+emp+"/login"), map[string]any{"email": email, "locale": "en"}, 201)
	if r := o.c.do("POST", o.p("/employees/"+emp+"/login"), map[string]any{"email": "x" + email}); r.status != 409 {
		t.Fatalf("second login: %d", r.status)
	}
	rami := loginAs(t, email, str(res.body["password"]))
	if home := rami.ok("GET", o.p("/self"), nil, 200); str(obj(home.body["employee"])["name"]) != "Rami" {
		t.Fatalf("self: %s", home.raw)
	}
	if r := rami.do("GET", o.p("/customers"), nil); r.status != 403 {
		t.Fatalf("employee sees customers: %d", r.status)
	}
	// a second employee reuses the same role
	emp2 := str(o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Lina", "currency": "USD", "salary": "700", "hire_date": "2025-01-01"}, 201).body["id"])
	o.c.ok("POST", o.p("/employees/"+emp2+"/login"), map[string]any{"email": "lina" + randHex(4) + "@example.com"}, 201)
	roles := o.c.ok("GET", "/roles", nil, 200)
	n := 0
	for _, x := range roles.list {
		if str(obj(x)["name"]) == employeeRoleName {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("employee roles: %d (%s)", n, roles.raw)
	}
}
