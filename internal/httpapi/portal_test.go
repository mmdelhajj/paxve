package httpapi

import "testing"

// Module G: customer portal isolation and dashboard numbers.

func TestPortalTokenIsolation(t *testing.T) {
	needDB(t)
	a, b := signup(t, "lebanon"), signup(t, "lebanon")
	ca, cb := a.customer(t, "Alice Portal", ""), b.customer(t, "Bob Portal", "")

	plan := a.plan(t, "10A", "40")
	a.c.ok("POST", a.p("/customers/"+ca+"/subscriptions"), map[string]any{"plan_id": plan, "start_date": "2026-01-01"}, 201)
	a.c.ok("POST", a.p("/billing/run"), map[string]any{"period": "2026-11", "issue_date": "2026-11-05"}, 200)
	invA := str(obj(a.c.ok("GET", a.p("/invoices?customer_id="+ca), nil, 200).body["rows"].([]any)[0])["id"])

	tokA := str(a.c.ok("GET", a.p("/customers/"+ca+"/portal"), nil, 200).body["token"])
	tokB := str(b.c.ok("GET", b.p("/customers/"+cb+"/portal"), nil, 200).body["token"])
	if len(tokA) < 32 || tokA == tokB {
		t.Fatalf("bad tokens %q %q", tokA, tokB)
	}

	anon := newClient(t)
	pa := anon.ok("GET", "/portal/"+tokA, nil, 200)
	if str(obj(pa.body["customer"])["name"]) != "Alice Portal" || len(pa.body["open_invoices"].([]any)) != 1 {
		t.Fatalf("portal A: %s", pa.raw)
	}
	if pb := anon.ok("GET", "/portal/"+tokB, nil, 200); str(obj(pb.body["customer"])["name"]) != "Bob Portal" {
		t.Fatalf("portal B: %s", pb.raw)
	}
	anon.ok("GET", "/portal/"+tokA+"/invoices/"+invA, nil, 200)
	if r := anon.do("GET", "/portal/"+tokB+"/invoices/"+invA, nil); r.status != 404 {
		t.Fatalf("tenant B token opened tenant A invoice: %d", r.status)
	}
	if r := a.c.do("GET", a.p("/customers/"+cb+"/portal"), nil); r.status != 404 {
		t.Fatalf("tenant A read tenant B customer's portal token: %d", r.status)
	}
	if r := anon.do("GET", "/portal/not-a-token", nil); r.status != 404 {
		t.Fatalf("garbage token: %d", r.status)
	}

	newTok := str(a.c.ok("POST", a.p("/customers/"+ca+"/portal"), map[string]any{}, 200).body["token"])
	if newTok == tokA {
		t.Fatal("regenerate returned the same token")
	}
	if r := anon.do("GET", "/portal/"+tokA, nil); r.status != 404 {
		t.Fatalf("old token still works after regenerate: %d", r.status)
	}
	anon.ok("GET", "/portal/"+newTok, nil, 200)

	// dashboard matches what was billed
	d := a.c.ok("GET", a.p("/dashboard?month=2026-11"), nil, 200)
	k := obj(d.body["kpis"].([]any)[0])
	eqDec(t, "billed", k["billed"], "40")
	eqDec(t, "outstanding", k["outstanding"], "40")
	if str(obj(d.body["counts"])["active_subscriptions"]) != "1" {
		t.Fatalf("dashboard counts: %v", d.body["counts"])
	}
	if len(d.body["series"].([]any)) != 12 {
		t.Fatalf("series length %d", len(d.body["series"].([]any)))
	}
	// bulk print returns the bill with its portal token
	pr := a.c.ok("GET", a.p("/invoices/print?period=2026-11"), nil, 200)
	rows := pr.body["rows"].([]any)
	if len(rows) != 1 || str(obj(rows[0])["portal_token"]) != newTok {
		t.Fatalf("print rows: %s", pr.raw)
	}
}
