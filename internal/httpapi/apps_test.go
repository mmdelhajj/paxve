package httpapi

import (
	"slices"
	"testing"
)

func TestAppForRoute(t *testing.T) {
	cases := map[string]string{
		"GET /api/v1/companies/{cid}/pos/registers":   "pos",
		"POST /api/v1/companies/{cid}/payroll":        "hr",
		"GET /api/v1/companies/{cid}/items":           "", // shared reference data
		"POST /api/v1/companies/{cid}/items":          "stk",
		"GET /api/v1/companies/{cid}/customers":       "",
		"POST /api/v1/companies/{cid}/payments":       "",
		"GET /api/v1/companies/{cid}/reports/sales":   "crm",
		"POST /api/v1/companies/{cid}/sales/orders":   "crm",
		"GET /api/v1/companies/{cid}/routers":         "net",
		"POST /api/v1/companies/{cid}/messages/send":  "msg",
		"GET /api/v1/companies/{cid}/reports/vat":     "",
		"GET /api/v1/companies/{cid}/stock/transfers": "stk",
	}
	for pattern, want := range cases {
		m, p, _ := cut(pattern)
		if got := appForRoute(m, p[len(v1):]); got != want {
			t.Errorf("%s: app %q, want %q", pattern, got, want)
		}
	}
}

func cut(s string) (string, string, bool) {
	for i := range s {
		if s[i] == ' ' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func TestAppsSwitchOff(t *testing.T) {
	o := signup(t, "lebanon")
	c := o.c
	comps := c.ok("GET", "/companies", nil, 200)
	apps := obj(comps.list[0])["apps"].([]any)
	if len(apps) != len(knownApps) {
		t.Fatalf("new company should start with every app, got %v", apps)
	}
	// switch POS and payroll off
	r := c.ok("PUT", o.p("/apps"), map[string]any{"apps": []string{"msg", "stk", "pur", "crm"}, "business_type": "internet"}, 200)
	got := []string{}
	for _, a := range r.body["apps"].([]any) {
		got = append(got, str(a))
	}
	for _, want := range []string{"gen", "acc", "col", "msg", "stk", "pur", "crm"} {
		if !slices.Contains(got, want) {
			t.Fatalf("apps %v missing %s (core apps are always on)", got, want)
		}
	}
	if slices.Contains(got, "pos") || str(r.body["business_type"]) != "internet" {
		t.Fatalf("unexpected %v", r.body)
	}
	if x := c.do("GET", o.p("/pos/registers"), nil); x.status != 403 || str(x.body["error"]) != "app_disabled" {
		t.Fatalf("POS with the app off: %d %s", x.status, x.raw)
	}
	if x := c.do("GET", o.p("/employees"), nil); x.status != 403 {
		t.Fatalf("payroll with the app off: %d", x.status)
	}
	c.ok("GET", o.p("/customers"), nil, 200) // core stays
	c.ok("GET", o.p("/items"), nil, 200)     // stock on
	if x := c.do("PUT", o.p("/apps"), map[string]any{"apps": []string{"bogus"}}); x.status != 400 {
		t.Fatalf("unknown app accepted: %d", x.status)
	}
	// back on
	c.ok("PUT", o.p("/apps"), map[string]any{"apps": knownApps}, 200)
	c.ok("GET", o.p("/pos/registers"), nil, 200)
}
