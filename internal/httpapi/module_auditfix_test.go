package httpapi

import (
	"strings"
	"sync"
	"testing"
)

// Fixes from the 2026-11-20 audits (live end-to-end and technical).

func TestAuditLoginLockoutPerAccount(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	owner := str(o.c.ok("GET", "/me", nil, 200).body["email"])
	clerk := "clerk" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": clerk, "full_name": "Clerk", "role": "data_entry", "password": "clerk-pass-123", "locale": "en"}, 201)
	// one office address: the owner mistypes the password again and again
	office := newClient(t)
	var last int
	for i := 0; i < 7; i++ {
		last = office.do("POST", "/auth/login", map[string]any{"email": owner, "password": "wrong-" + randHex(2)}).status
	}
	if last != 429 {
		t.Fatalf("owner from that address should be held back: %d", last)
	}
	// another account from the SAME address still signs in (audit B-H3: the office is not locked out)
	colleague := newClient(t)
	colleague.ip = office.ip
	colleague.ok("POST", "/auth/login", map[string]any{"email": clerk, "password": "clerk-pass-123"}, 200)
	// and the owner from another address too (7 failures stay under the per-account lock)
	newClient(t).ok("POST", "/auth/login", map[string]any{"email": owner, "password": "correct-horse-1"}, 200)
}

func TestAuditValidationAndErrors(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	if r := o.c.do("POST", o.p("/customers"), map[string]any{"name": "A", "email": "not-an-email"}); r.status != 400 || str(r.body["error"]) != "invalid_email" {
		t.Fatalf("bad e-mail: %d %s", r.status, r.raw)
	}
	if r := o.c.do("POST", o.p("/customers"), map[string]any{"name": strings.Repeat("ب", 500)}); r.status != 400 || str(r.body["error"]) != "name_too_long" {
		t.Fatalf("long name: %d %s", r.status, r.raw)
	}
	o.c.ok("POST", o.p("/customers"), map[string]any{"name": "Good", "email": "good@example.com"}, 201)
	if r := o.c.do("POST", o.p("/suppliers"), map[string]any{"name": "S", "email": "x@"}); r.status != 400 || str(r.body["error"]) != "invalid_email" {
		t.Fatalf("supplier e-mail: %d %s", r.status, r.raw)
	}
	if r := o.c.do("POST", "/users", map[string]any{"email": "z" + randHex(3) + "@example.com", "full_name": "Z", "role": "read_only",
		"password": "zzzz-zzzz-1", "company_ids": []string{}}); r.status != 400 || str(r.body["error"]) != "no_companies_selected" {
		t.Fatalf("empty company list: %d %s", r.status, r.raw)
	}
	// company time zone: validated, stored, returned
	if r := o.c.do("PATCH", "/companies/"+o.company, map[string]any{"timezone": "Mars/Olympus"}); r.status != 400 {
		t.Fatalf("unknown zone accepted: %d", r.status)
	}
	if c := o.c.ok("PATCH", "/companies/"+o.company, map[string]any{"timezone": "Asia/Dubai"}, 200); str(c.body["timezone"]) != "Asia/Dubai" {
		t.Fatalf("zone: %s", c.raw)
	}
}

func TestAuditLeaveTypesSelfAndControlAccounts(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	// several screens opening the leave page at once all succeed (the defaults are seeded once)
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = o.c.do("GET", o.p("/hr/leave-types"), nil).status
		}(i)
	}
	wg.Wait()
	for _, c := range codes {
		if c != 200 {
			t.Fatalf("leave types: %v", codes)
		}
	}
	if n := len(o.c.ok("GET", o.p("/hr/leave-types"), nil, 200).list); n != 4 {
		t.Fatalf("leave types seeded %d times", n)
	}
	// "my page" without an employee file is not an error
	if h := o.c.ok("GET", o.p("/self"), nil, 200); h.body["no_employee_file"] != true {
		t.Fatalf("self: %s", h.raw)
	}
	if l := o.c.ok("GET", o.p("/self/team-leave"), nil, 200); len(l.list) != 0 {
		t.Fatalf("team: %s", l.raw)
	}
	// a manual entry on the receivables account: refused to data entry, allowed (with a warning) to the owner
	set := o.c.ok("GET", o.p("/billing/settings"), nil, 200)
	ar, rev := str(set.body["ar_account_id"]), str(set.body["default_revenue_account_id"])
	if ar == "" || rev == "" {
		t.Skipf("no default accounts: %s", set.raw)
	}
	entry := map[string]any{"date": "2026-11-20", "memo": "fix", "post": false, "lines": []any{
		map[string]any{"account_id": ar, "debit": "50", "credit": "0"}, map[string]any{"account_id": rev, "debit": "0", "credit": "50"}}}
	clerk := "de" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": clerk, "full_name": "DE", "role": "data_entry", "password": "data-entry-123", "locale": "en"}, 201)
	de := loginAs(t, clerk, "data-entry-123")
	if r := de.do("POST", o.p("/journal"), entry); r.status != 403 || str(r.body["error"]) != "control_account" {
		t.Fatalf("data entry on AR: %d %s", r.status, r.raw)
	}
	e := o.c.ok("POST", o.p("/journal"), entry, 201)
	if ctl, _ := e.body["control_accounts"].([]any); len(ctl) != 1 {
		t.Fatalf("owner warning: %s", e.raw)
	}
}
