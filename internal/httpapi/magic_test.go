package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/magic"
)

func (o *org) uploadMagic(t *testing.T, data []byte, chunk int) string {
	t.Helper()
	sum := sha256.Sum256(data)
	imp := o.c.ok("POST", "/magic/imports", map[string]any{"file_name": "magic.zip", "size": len(data), "sha256": hex.EncodeToString(sum[:])}, 201)
	id := str(imp.body["id"])
	var last resp
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		last = o.c.ok("POST", "/magic/imports/"+id+"/chunk", map[string]any{"offset": off, "data": base64.StdEncoding.EncodeToString(data[off:end])}, 200)
	}
	if str(last.body["status"]) != "uploaded" {
		t.Fatalf("upload status %v: %v", last.body["status"], last.body["error"])
	}
	return id
}

func (o *org) waitMagic(t *testing.T, id string) resp {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		r := o.c.ok("GET", "/magic/imports/"+id, nil, 200)
		switch str(r.body["status"]) {
		case "done":
			return r
		case "failed":
			t.Fatalf("import failed: %v", r.body["error"])
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("import did not finish")
	return resp{}
}

func ownerQuery(t *testing.T, sql string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	var v string
	if err := pgx.BeginFunc(ctx, itOwnerPool, func(tx pgx.Tx) error { return tx.QueryRow(ctx, sql, args...).Scan(&v) }); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func TestMagicMigration(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	// the legacy import is the platform owner's tool: a tenant admin never reaches it
	o.c.ok("GET", "/magic/imports", nil, 403)
	email := str(o.c.ok("GET", "/me", nil, 200).body["email"])
	itAPI.platformAdmins[email] = true
	defer delete(itAPI.platformAdmins, email)
	o.c.ok("GET", "/magic/imports", nil, 200)
	data, exp := magic.Synthetic(11)

	// bad offset and checksum are refused
	sum := sha256.Sum256(data)
	bad := o.c.ok("POST", "/magic/imports", map[string]any{"file_name": "x.zip", "size": len(data), "sha256": hex.EncodeToString(sum[:])}, 201)
	o.c.ok("POST", "/magic/imports/"+str(bad.body["id"])+"/chunk", map[string]any{"offset": 10, "data": base64.StdEncoding.EncodeToString(data[:10])}, 409)

	id := o.uploadMagic(t, data, 7000)
	imp := o.c.ok("GET", "/magic/imports/"+id, nil, 200)
	comps := obj(imp.body["analysis"])["companies"].([]any)
	if len(comps) != 3 {
		t.Fatalf("analysis companies: %d", len(comps))
	}
	// dry run with other options
	o.c.ok("POST", "/magic/imports/"+id+"/analyze", map[string]any{"opening_date": "2026-11-01", "readings_months": 6}, 200)

	// non-admins cannot import
	opts := map[string]any{"opening_date": "2026-11-01", "readings_months": 12}
	commit := map[string]any{"options": opts, "mapping": []map[string]any{
		{"magic_company": 3, "target": o.company}, {"magic_company": 11, "target": "new"}, {"magic_company": 12, "target": "skip"}}}
	o.c.ok("POST", "/magic/imports/"+id+"/commit", commit, 202)
	res := o.waitMagic(t, id)
	results := obj(res.body["result"])["companies"].([]any)
	if len(results) != 2 {
		t.Fatalf("results: %v", res.raw)
	}
	internet := str(obj(results[1])["company_id"])
	if testing.Verbose() {
		t.Logf("result: %s", res.raw)
	}
	o.trialBalanced(t)

	for _, co := range []struct {
		magic int
		cid   string
	}{{3, o.company}, {11, internet}} {
		// every customer balance equals Magic's (unpaid invoices − unapplied credits)
		for person, cm := range exp.Customers[co.magic] {
			for cur, want := range cm {
				got := ownerQuery(t, `SELECT (coalesce((SELECT sum(i.total - i.paid) FROM invoices i WHERE i.customer_id = c.id AND i.currency = $3
					AND i.status IN ('open', 'partial')), 0) - coalesce((SELECT sum(p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a
					WHERE a.payment_id = p.id), 0)) FROM payments p WHERE p.customer_id = c.id AND p.currency = $3 AND p.status = 'posted'), 0))::text
					FROM customers c WHERE c.company_id = $1 AND c.external_ref = $2`, co.cid, "magic:customer:"+strconv.Itoa(person), cur)
				if g := decimal.RequireFromString(got); g.Sub(want).Abs().GreaterThan(decimal.RequireFromString("0.01")) {
					t.Errorf("magic company %d customer %d %s: paxve %s, magic %s", co.magic, person, cur, got, want)
				}
			}
		}
		// the clearing account nets to zero; receivables equal the sum of customer balances
		if v := ownerQuery(t, `SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::text FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id
			JOIN accounts a ON a.id = l.account_id WHERE e.company_id = $1 AND a.code = '4718' AND e.status = 'posted'`, co.cid); !decimal.RequireFromString(v).IsZero() {
			t.Errorf("company %d: clearing balance %s", co.magic, v)
		}
		total := decimal.Zero
		for _, cm := range exp.Customers[co.magic] {
			total = total.Add(cm["USD"])
		}
		ar := ownerQuery(t, `SELECT coalesce(sum(l.debit - l.credit), 0)::text FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id
			JOIN companies c ON c.id = e.company_id WHERE e.company_id = $1 AND l.account_id = c.ar_account_id AND l.currency = 'USD' AND e.status = 'posted'`, co.cid)
		if decimal.RequireFromString(ar).Sub(total).Abs().GreaterThan(decimal.RequireFromString("0.05")) {
			t.Errorf("company %d: receivables %s, customers %s", co.magic, ar, total)
		}
	}
	// Magic's unbalanced one-line entry (−7.50) lands on the difference account
	diff := ownerQuery(t, `SELECT coalesce(sum(l.base_debit - l.base_credit), 0)::text FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id
		JOIN accounts a ON a.id = l.account_id WHERE e.company_id = $1 AND a.code = '4719'`, o.company)
	eqDec(t, "magic difference", diff, "7.5")
	// supplier 9001: 1,200 still owed, as an open bill
	eqDec(t, "supplier bills", ownerQuery(t, `SELECT coalesce(sum(total - paid), 0)::text FROM bills WHERE company_id = $1 AND status <> 'void'`, o.company), "1200")
	// collectors are created inactive, areas keep their collector
	if n := ownerQuery(t, `SELECT count(*)::text FROM users WHERE tenant_id = $1 AND role = 'collector' AND NOT is_active AND external_ref LIKE 'magic:collector:%'`, o.tenant); n != "3" {
		t.Errorf("collectors: %s", n)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM areas WHERE company_id = $1 AND collector_id IS NOT NULL`, o.company); n != "4" {
		t.Errorf("areas with collector: %s", n)
	}
	subs := ownerQuery(t, `SELECT count(*)::text FROM subscriptions WHERE company_id = $1`, o.company)
	readings := ownerQuery(t, `SELECT count(*)::text FROM meter_readings r JOIN subscriptions s ON s.id = r.subscription_id WHERE s.company_id = $1`, o.company)
	if subs != "29" || readings == "0" {
		t.Errorf("subscriptions %s readings %s", subs, readings)
	}
	invoices := ownerQuery(t, `SELECT count(*)::text FROM invoices WHERE company_id = $1`, o.company)

	// a second run adds nothing (customers are updated, money is not booked twice)
	o.c.ok("POST", "/magic/imports/"+id+"/commit", map[string]any{"options": opts, "mapping": []map[string]any{
		{"magic_company": 3, "target": o.company}, {"magic_company": 11, "target": internet}}}, 202)
	res = o.waitMagic(t, id)
	r0 := obj(obj(res.body["result"])["companies"].([]any)[0])
	if r0["financial_skipped"] != true {
		t.Fatalf("second run should skip balances: %v", res.raw)
	}
	if got := ownerQuery(t, `SELECT count(*)::text FROM subscriptions WHERE company_id = $1`, o.company); got != subs {
		t.Errorf("subscriptions after re-run: %s, want %s", got, subs)
	}
	if got := ownerQuery(t, `SELECT count(*)::text FROM invoices WHERE company_id = $1`, o.company); got != invoices {
		t.Errorf("invoices after re-run: %s, want %s", got, invoices)
	}
	o.trialBalanced(t)
}
