package httpapi

// Integration tests: run the real HTTP API against a real PostgreSQL 18.
//
//	TEST_DATABASE_URL=postgres://postgres:secret@localhost:5432/postgres?sslmode=disable go test ./...
//
// The URL must be a superuser on a THROWAWAY server (CI service container or a temporary docker
// container): the tests create a fresh database per run and set the password of the cluster-wide
// paxve_app role. Without TEST_DATABASE_URL the tests are skipped.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

var (
	itServer    *httptest.Server
	itOwnerPool *pgxpool.Pool
	itSeq       int
)

func TestMain(m *testing.M) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		fmt.Println("TEST_DATABASE_URL not set: integration tests skipped")
		os.Exit(m.Run())
	}
	ctx := context.Background()
	name := fmt.Sprintf("paxve_it_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, adminURL)
	must(err)
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	must(err)

	u, _ := url.Parse(adminURL)
	u.Path = "/" + name
	ownerURL := u.String()
	appPass := "it-" + randHex(12)
	must(db.Migrate(ctx, ownerURL, appPass))
	appU := *u
	appU.User = url.UserPassword("paxve_app", appPass)

	pool, err := pgxpool.New(ctx, appU.String())
	must(err)
	itOwnerPool, err = pgxpool.New(ctx, ownerURL)
	must(err)
	sm := scs.New()
	sm.Store = memstore.New()
	sm.Cookie.Secure = false
	srv := New(pool, itOwnerPool, sm, "", []string{"platform@example.com"})
	itServer = httptest.NewServer(srv.Handler())

	code := m.Run()

	itServer.Close()
	pool.Close()
	itOwnerPool.Close()
	_, _ = admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
	_ = admin.Close(ctx)
	os.Exit(code)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func needDB(t *testing.T) {
	t.Helper()
	if itServer == nil {
		t.Skip("TEST_DATABASE_URL not set")
	}
}

// ---- tiny HTTP client ----

type client struct {
	t  *testing.T
	hc *http.Client
	ip string
}

func newClient(t *testing.T) *client {
	jar, _ := cookiejar.New(nil)
	itSeq++
	// each client looks like a different visitor so per-IP limits do not interfere between tests
	return &client{t: t, hc: &http.Client{Jar: jar}, ip: fmt.Sprintf("10.0.%d.%d", itSeq/250, itSeq%250+1)}
}

type resp struct {
	status int
	body   map[string]any
	list   []any
	raw    string
}

func (c *client) do(method, path string, body any) resp {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, itServer.URL+"/api/v1"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "paxve")
	req.Header.Set("X-Client-IP", c.ip)
	res, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	_ = json.Unmarshal(raw, &out.list)
	return out
}

func (c *client) ok(method, path string, body any, want int) resp {
	c.t.Helper()
	r := c.do(method, path, body)
	if r.status != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, r.status, want, r.raw)
	}
	return r
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }

func dec(t *testing.T, v any) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(str(v))
	if err != nil {
		t.Fatalf("not a decimal: %v", v)
	}
	return d
}

func eqDec(t *testing.T, what string, got any, want string) {
	t.Helper()
	if !dec(t, got).Equal(decimal.RequireFromString(want)) {
		t.Fatalf("%s = %v, want %s", what, got, want)
	}
}

// ---- fixtures ----

type org struct {
	c        *client
	tenant   string
	company  string
	accounts map[string]string // code -> id
	methods  map[string]string // name -> id
}

func signup(t *testing.T, template string) *org {
	t.Helper()
	c := newClient(t)
	email := fmt.Sprintf("u%s@example.com", randHex(5))
	me := c.ok("POST", "/signup", map[string]any{
		"organisation": "Org " + randHex(3), "company_name": "Gen Co", "base_currency": "USD", "local_currency": "LBP",
		"template": template, "full_name": "Owner", "email": email, "password": "correct-horse-1", "locale": "en",
	}, 201)
	o := &org{c: c, tenant: str(me.body["tenant_id"]), accounts: map[string]string{}, methods: map[string]string{}}
	comps := c.ok("GET", "/companies", nil, 200)
	o.company = str(obj(comps.list[0])["id"])
	for _, a := range c.ok("GET", o.p("/accounts"), nil, 200).list {
		o.accounts[str(obj(a)["code"])] = str(obj(a)["id"])
	}
	for _, m := range c.ok("GET", o.p("/payment-methods"), nil, 200).list {
		o.methods[str(obj(m)["name"])] = str(obj(m)["id"])
	}
	return o
}

func (o *org) p(path string) string { return "/companies/" + o.company + path }

func (o *org) customer(t *testing.T, name, area string) string {
	t.Helper()
	body := map[string]any{"name": name}
	if area != "" {
		body["area_id"] = area
	}
	return str(o.c.ok("POST", o.p("/customers"), body, 201).body["id"])
}

func (o *org) plan(t *testing.T, name, fee string) string {
	t.Helper()
	return str(o.c.ok("POST", o.p("/plans"), map[string]any{"name": name, "billing_type": "fixed", "currency": "USD", "base_fee": fee}, 201).body["id"])
}

func (o *org) rate(t *testing.T, cur, date, rate string) {
	t.Helper()
	o.c.ok("POST", o.p("/rates"), map[string]any{"currency": cur, "date": date, "rate": rate}, 200)
}

func (o *org) trialBalanced(t *testing.T) {
	t.Helper()
	tb := o.c.ok("GET", o.p("/reports/trial-balance?from=2000-01-01&to=2100-01-01"), nil, 200)
	if !dec(t, tb.body["total_debit"]).Equal(dec(t, tb.body["total_credit"])) {
		t.Fatalf("trial balance not balanced: %v / %v", tb.body["total_debit"], tb.body["total_credit"])
	}
}

// ---- tests ----

func TestTenantIsolation(t *testing.T) {
	needDB(t)
	a, b := signup(t, "lebanon"), signup(t, "generic")
	cust := a.customer(t, "Alice", "")

	// B cannot see A's company or customer, even with the ids
	if r := b.c.do("GET", a.p("/customers/"+cust), nil); r.status != 404 {
		t.Fatalf("other tenant read customer: %d", r.status)
	}
	if r := b.c.do("GET", a.p("/customers"), nil); r.status == 200 && len(r.list) != 0 {
		t.Fatalf("other tenant listed %d customers", len(r.list))
	}
	if r := b.c.do("POST", a.p("/customers"), map[string]any{"name": "Intruder"}); r.status < 400 {
		t.Fatalf("other tenant created a customer in A's company: %d", r.status)
	}
	// and the database itself refuses, whatever the app does
	ctx := context.Background()
	var n int
	err := pgx.BeginFunc(ctx, itOwnerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, b.tenant); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM customers WHERE id = $1`, cust).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("RLS leak: n=%d err=%v", n, err)
	}
}

func TestJournalBalancedAndImmutable(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cash, rev := o.accounts["531"], o.accounts["701"]
	r := o.c.do("POST", o.p("/journal"), map[string]any{"date": "2026-11-10", "post": true, "lines": []any{
		map[string]any{"account_id": cash, "debit": "100"}, map[string]any{"account_id": rev, "credit": "90"}}})
	if r.status != 422 || str(r.body["error"]) != "unbalanced" {
		t.Fatalf("unbalanced entry accepted: %d %s", r.status, r.raw)
	}
	e := o.c.ok("POST", o.p("/journal"), map[string]any{"date": "2026-11-10", "post": true, "lines": []any{
		map[string]any{"account_id": cash, "debit": "100"}, map[string]any{"account_id": rev, "credit": "100"}}}, 201)
	id := str(e.body["id"])
	if str(e.body["number"]) != "1" {
		t.Fatalf("first number = %v", e.body["number"])
	}
	if r := o.c.do("PUT", o.p("/journal/"+id), map[string]any{"date": "2026-11-10", "lines": []any{
		map[string]any{"account_id": cash, "debit": "1"}, map[string]any{"account_id": rev, "credit": "1"}}}); r.status != 409 {
		t.Fatalf("posted entry edited: %d", r.status)
	}
	if r := o.c.do("DELETE", o.p("/journal/"+id), nil); r.status != 409 {
		t.Fatalf("posted entry deleted: %d", r.status)
	}
	o.c.ok("POST", o.p("/journal/"+id+"/reverse"), map[string]any{}, 201)
	if r := o.c.do("POST", o.p("/journal/"+id+"/reverse"), map[string]any{}); r.status != 409 {
		t.Fatalf("reversed twice: %d", r.status)
	}
	o.trialBalanced(t)
}

func TestBillingRunAndMixedCurrencyChange(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	plan := o.plan(t, "10A", "45")
	cust := o.customer(t, "Ahmad", "")
	o.c.ok("POST", o.p("/customers/"+cust+"/subscriptions"), map[string]any{"plan_id": plan, "start_date": "2026-01-01"}, 201)
	run := o.c.ok("POST", o.p("/billing/run"), map[string]any{"period": "2026-11", "issue_date": "2026-11-05"}, 200)
	if str(run.body["created"]) != "1" {
		t.Fatalf("billing run created %v", run.body["created"])
	}
	again := o.c.ok("POST", o.p("/billing/run"), map[string]any{"period": "2026-11", "issue_date": "2026-11-05"}, 200)
	if str(again.body["created"]) != "0" {
		t.Fatalf("second billing run created %v", again.body["created"])
	}
	// 45 USD paid with 20 USD + 2,500,000 LBP -> change 2.9330 USD = 262,503 LBP
	pay := o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-11-06", "currency": "USD", "amount": "45",
		"tenders":          []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "20"}, map[string]any{"method_id": o.methods["Cash LBP"], "amount": "2500000"}},
		"change_method_id": o.methods["Cash LBP"]}, 201)
	eqDec(t, "change", obj(pay.body["payment"])["change_amount"], "262503")
	eqDec(t, "applied", obj(pay.body["payment"])["amount"], "45")

	// with the LBP box rounding change down to 1,000
	o.c.ok("PATCH", o.p("/payment-methods/"+o.methods["Cash LBP"]), map[string]any{"change_rounding": "1000"}, 200)
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-11-06",
		"lines": []any{map[string]any{"description": "Repair", "quantity": "1", "unit_price": "45"}}}, 201)
	pay2 := o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-11-06", "currency": "USD", "amount": "45",
		"tenders":          []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "20"}, map[string]any{"method_id": o.methods["Cash LBP"], "amount": "2500000"}},
		"change_method_id": o.methods["Cash LBP"]}, 201)
	eqDec(t, "rounded change", obj(pay2.body["payment"])["change_amount"], "262000")
	o.trialBalanced(t)
}

func TestForeignCurrencyReceivableUsesInvoiceRate(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.rate(t, "LBP", "2026-01-01", "89500")
	cust := o.customer(t, "Rima", "")
	o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "LBP", "issue_date": "2026-03-01",
		"lines": []any{map[string]any{"description": "Monthly", "quantity": "1", "unit_price": "895000"}}}, 201) // = 10.0000 USD
	o.rate(t, "LBP", "2026-04-01", "100000")
	o.c.ok("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-04-02", "currency": "LBP", "amount": "895000",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash LBP"], "amount": "895000"}}}, 201) // = 8.9500 USD received

	// receivable for this customer is back to exactly zero in base currency; the 1.05 USD drop is a realized FX loss
	ctx := context.Background()
	var arBase, loss string
	err := pgx.BeginFunc(ctx, itOwnerPool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::text FROM journal_lines l
			JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
			WHERE e.company_id = $1 AND l.account_id = $2`, o.company, o.accounts["411"]).Scan(&arBase); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::text FROM journal_lines l
			JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
			WHERE e.company_id = $1 AND l.account_id = $2`, o.company, o.accounts["666"]).Scan(&loss)
	})
	if err != nil {
		t.Fatal(err)
	}
	eqDec(t, "receivable base balance", arBase, "0")
	eqDec(t, "realized FX loss", loss, "1.05")
	o.trialBalanced(t)
}

func TestCollectorScopingAndIdempotentPayments(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	o.c.ok("POST", "/users", map[string]any{"email": "col" + randHex(4) + "@example.com", "full_name": "Ali", "role": "collector",
		"locale": "ar", "password": "collector-pass-1"}, 201)
	var colID, colEmail string
	for _, u := range o.c.ok("GET", "/users", nil, 200).list {
		if str(obj(u)["role"]) == "collector" {
			colID, colEmail = str(obj(u)["id"]), str(obj(u)["email"])
		}
	}
	mine := str(o.c.ok("POST", o.p("/areas"), map[string]any{"name": "Mine", "collector_id": colID}, 201).body["id"])
	other := str(o.c.ok("POST", o.p("/areas"), map[string]any{"name": "Other"}, 201).body["id"])
	c1 := o.customer(t, "In my area", mine)
	c2 := o.customer(t, "Not mine", other)

	col := newClient(t)
	col.ok("POST", "/auth/login", map[string]any{"email": colEmail, "password": "collector-pass-1"}, 200)
	list := col.ok("GET", o.p("/customers"), nil, 200)
	if len(list.list) != 1 || str(obj(list.list[0])["id"]) != c1 {
		t.Fatalf("collector sees %d customers", len(list.list))
	}
	if r := col.do("GET", o.p("/customers/"+c2), nil); r.status != 404 {
		t.Fatalf("collector opened other area customer: %d", r.status)
	}
	for _, path := range []string{"/accounts", "/invoices", "/journal", "/reports/trial-balance", "/suppliers"} {
		if r := col.do("GET", o.p(path), nil); r.status != 403 {
			t.Fatalf("collector GET %s: %d", path, r.status)
		}
	}
	if r := col.do("POST", o.p("/payments"), map[string]any{"customer_id": c2, "currency": "USD",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "5"}}}); r.status != 403 {
		t.Fatalf("collector paid other area customer: %d", r.status)
	}
	body := map[string]any{"customer_id": c1, "currency": "USD", "client_ref": "pxv-" + randHex(8),
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "7"}}}
	p1 := col.ok("POST", o.p("/payments"), body, 201)
	p2 := col.ok("POST", o.p("/payments"), body, 201)
	if str(obj(p1.body["payment"])["id"]) != str(obj(p2.body["payment"])["id"]) {
		t.Fatal("same client_ref stored twice")
	}
	if got := len(col.ok("GET", o.p("/payments"), nil, 200).body["rows"].([]any)); got != 1 {
		t.Fatalf("collector payments list has %d rows", got)
	}
}

func TestPlanLimit(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	_, err := itOwnerPool.Exec(context.Background(), `UPDATE tenants SET max_subscribers = 1 WHERE id = $1`, o.tenant)
	must(err)
	plan := o.plan(t, "5A", "25")
	c1, c2 := o.customer(t, "One", ""), o.customer(t, "Two", "")
	o.c.ok("POST", o.p("/customers/"+c1+"/subscriptions"), map[string]any{"plan_id": plan}, 201)
	r := o.c.do("POST", o.p("/customers/"+c2+"/subscriptions"), map[string]any{"plan_id": plan})
	if r.status != 402 || str(r.body["error"]) != "plan_limit" {
		t.Fatalf("plan limit not enforced: %d %s", r.status, r.raw)
	}
}

func TestStockAverageCost(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Fuel Co", "currency": "USD"}, 201).body["id"])
	item := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "DSL", "name": "Diesel", "kind": "stock", "unit": "L"}, 201).body["id"])
	bill := func(qty, price string) {
		o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
			"lines": []any{map[string]any{"item_id": item, "quantity": qty, "unit_price": price}}}, 201)
	}
	bill("1000", "0.9")
	bill("500", "1.2")
	it := o.c.ok("GET", o.p("/items/"+item), nil, 200)
	st := obj(it.body["stock"].([]any)[0])
	eqDec(t, "quantity", st["quantity"], "1500")
	eqDec(t, "average cost", st["avg_cost"], "1")
	if r := o.c.do("POST", o.p("/stock/fuel"), map[string]any{"item_id": item, "quantity": "5000", "date": "2026-11-02"}); r.status < 400 {
		t.Fatalf("negative stock allowed: %d", r.status)
	}
	o.c.ok("POST", o.p("/stock/fuel"), map[string]any{"item_id": item, "quantity": "300", "date": "2026-11-02"}, 201)
	st = obj(o.c.ok("GET", o.p("/items/"+item), nil, 200).body["stock"].([]any)[0])
	eqDec(t, "quantity after fuel", st["quantity"], "1200")
	o.trialBalanced(t)
}

func TestPeriodLock(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cash, rev := o.accounts["531"], o.accounts["701"]
	e := o.c.ok("POST", o.p("/journal"), map[string]any{"date": "2026-01-15", "post": true, "lines": []any{
		map[string]any{"account_id": cash, "debit": "10"}, map[string]any{"account_id": rev, "credit": "10"}}}, 201)
	o.c.ok("PATCH", "/companies/"+o.company, map[string]any{"lock_date": "2026-01-31"}, 200)
	r := o.c.do("POST", o.p("/journal"), map[string]any{"date": "2026-01-20", "lines": []any{
		map[string]any{"account_id": cash, "debit": "10"}, map[string]any{"account_id": rev, "credit": "10"}}})
	if r.status != 409 || str(r.body["error"]) != "period_locked" {
		t.Fatalf("entry in locked period accepted: %d %s", r.status, r.raw)
	}
	cust := o.customer(t, "Late", "")
	if r := o.c.do("POST", o.p("/payments"), map[string]any{"customer_id": cust, "date": "2026-01-25", "currency": "USD",
		"tenders": []any{map[string]any{"method_id": o.methods["Cash USD"], "amount": "5"}}}); r.status != 409 {
		t.Fatalf("payment in locked period accepted: %d", r.status)
	}
	// reversing a locked entry lands after the lock date
	rev2 := o.c.ok("POST", o.p("/journal/"+str(e.body["id"])+"/reverse"), map[string]any{}, 201)
	if str(rev2.body["date"]) != "2026-02-01" {
		t.Fatalf("reversal dated %v", rev2.body["date"])
	}
	o.c.ok("POST", o.p("/journal"), map[string]any{"date": "2026-02-05", "post": true, "lines": []any{
		map[string]any{"account_id": cash, "debit": "10"}, map[string]any{"account_id": rev, "credit": "10"}}}, 201)
	o.trialBalanced(t)
}

func TestSecurityGuards(t *testing.T) {
	needDB(t)
	// a platform-owner address cannot be claimed by public sign-up or by a tenant admin
	c := newClient(t)
	if r := c.do("POST", "/signup", map[string]any{"organisation": "Evil", "template": "generic", "full_name": "x",
		"email": "platform@example.com", "password": "long-enough-pw"}); r.status != 409 {
		t.Fatalf("platform email claimed by signup: %d", r.status)
	}
	o := signup(t, "generic")
	if r := o.c.do("POST", "/users", map[string]any{"email": "platform@example.com", "full_name": "x", "role": "admin",
		"password": "long-enough-pw"}); r.status != 409 {
		t.Fatalf("platform email created by tenant admin: %d", r.status)
	}
	if r := o.c.do("GET", "/platform/tenants", nil); r.status != 403 {
		t.Fatalf("tenant admin opened platform console: %d", r.status)
	}
	// state-changing calls must be JSON (CSRF guard)
	req, _ := http.NewRequest("POST", itServer.URL+"/api/v1/auth/login", strings.NewReader("email=a&password=b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 415 {
		t.Fatalf("form POST accepted: %d", res.StatusCode)
	}
	// per-account login limit: 10 failures from different IPs lock the account for a while
	for i := 0; i < 10; i++ {
		newClient(t).do("POST", "/auth/login", map[string]any{"email": "victim@example.com", "password": "nope-" + randHex(2)})
	}
	if r := newClient(t).do("POST", "/auth/login", map[string]any{"email": "victim@example.com", "password": "x"}); r.status != 429 {
		t.Fatalf("per-email login limit not applied: %d", r.status)
	}
	// API keys cannot be created with the collector role
	if r := o.c.do("POST", "/api-keys", map[string]any{"name": "k", "role": "collector"}); r.status != 400 {
		t.Fatalf("collector API key created: %d", r.status)
	}
}

func TestHealthReady(t *testing.T) {
	needDB(t)
	res, err := http.Get(itServer.URL + "/api/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(b), `"ok":true`) {
		t.Fatalf("ready: %d %s", res.StatusCode, b)
	}
}
