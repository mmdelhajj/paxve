package httpapi

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// Phase 7 audit C (UX) regressions.

func appsOf(t *testing.T, o *org, cid string) []string {
	t.Helper()
	for _, c := range o.c.ok("GET", "/companies", nil, 200).list {
		if str(obj(c)["id"]) == cid {
			out := []string{}
			for _, a := range obj(c)["apps"].([]any) {
				out = append(out, str(a))
			}
			return out
		}
	}
	t.Fatalf("company %s not listed", cid)
	return nil
}

// F1 + F3 + F6: sign-up with a business type starts with that type's apps, the owner's language, phone and LBP rate.
func TestP7SignupBusinessType(t *testing.T) {
	needDB(t)
	c := newClient(t)
	email := fmt.Sprintf("u%s@example.com", randHex(5))
	c.ok("POST", "/signup", map[string]any{"organisation": "Pharma " + randHex(3), "base_currency": "USD", "local_currency": "LBP",
		"template": "lebanon", "full_name": "Owner", "email": email, "password": "correct-horse-1", "locale": "ar",
		"business_type": "pharmacy", "phone": "+961 70 123 456", "rate": "89,500"}, 201)
	comp := obj(c.ok("GET", "/companies", nil, 200).list[0])
	cid := str(comp["id"])
	apps := []string{}
	for _, a := range comp["apps"].([]any) {
		apps = append(apps, str(a))
	}
	for _, want := range []string{"gen", "acc", "stk", "pos", "pur"} {
		if !slices.Contains(apps, want) {
			t.Fatalf("pharmacy apps miss %s: %v", want, apps)
		}
	}
	for _, not := range []string{"net", "mfg", "prj", "flt", "sat"} {
		if slices.Contains(apps, not) {
			t.Fatalf("pharmacy starts with %s: %v", not, apps)
		}
	}
	if bt := str(comp["business_type"]); bt != "pharmacy" {
		t.Fatalf("business type %s", bt)
	}
	if l := ownerQuery(t, `SELECT default_locale FROM companies WHERE id = $1`, cid); l != "ar" {
		t.Fatalf("company language %s", l)
	}
	if ph := ownerQuery(t, `SELECT t.contact_phone FROM tenants t JOIN companies c ON c.tenant_id = t.id WHERE c.id = $1`, cid); ph != "+961 70 123 456" {
		t.Fatalf("contact phone %q", ph)
	}
	if r := ownerQuery(t, `SELECT rate::numeric(20,0)::text FROM exchange_rates WHERE company_id = $1 AND currency = 'LBP'`, cid); r != "89500" {
		t.Fatalf("LBP rate %q", r)
	}
	// bad rate refused
	if r := newClient(t).do("POST", "/signup", map[string]any{"organisation": "X", "template": "lebanon", "full_name": "x",
		"email": fmt.Sprintf("x%s@example.com", randHex(4)), "password": "correct-horse-1", "rate": "-3"}); r.status != 400 {
		t.Fatalf("negative rate: %d", r.status)
	}
	// the default warehouse and the CRM stages are created in Arabic
	c.ok("GET", "/companies/"+cid+"/crm/stages", nil, 200)
	if n := ownerQuery(t, `SELECT name FROM crm_stages WHERE company_id = $1 ORDER BY seq LIMIT 1`, cid); n != "جديد" {
		t.Fatalf("first stage %q", n)
	}
}

// F3: the sign-up limit answers with the seconds to wait.
func TestP7LimiterRetryAfter(t *testing.T) {
	l := newLimiter(2, time.Hour)
	l.fail("ip")
	if d := l.retryAfter("ip"); d != 0 {
		t.Fatalf("retry after 1 of 2: %v", d)
	}
	l.fail("ip")
	if d := l.retryAfter("ip"); d < 59*time.Minute {
		t.Fatalf("retry after: %v", d)
	}
}

// F2: a company added from Settings gets its chart, language and apps; a pack on an old empty company loads the chart.
func TestP7CompanyChartAndPack(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	nc := o.c.ok("POST", "/companies", map[string]any{"name_en": "Shop " + randHex(3), "base_currency": "USD", "local_currency": "LBP",
		"business_type": "restaurant", "locale": "ar"}, 201)
	cid := str(nc.body["id"])
	if n := ownerQuery(t, `SELECT count(*)::text FROM accounts WHERE company_id = $1`, cid); n == "0" {
		t.Fatal("new company has no chart")
	}
	if l := str(nc.body["l10n"]); l != "lebanon" {
		t.Fatalf("template %q (the organisation's)", l)
	}
	apps := appsOf(t, o, cid)
	if !slices.Contains(apps, "pos") || slices.Contains(apps, "net") {
		t.Fatalf("restaurant apps %v", apps)
	}
	o.c.ok("POST", "/companies/"+cid+"/rates", map[string]any{"currency": "LBP", "rate": "89500"}, 200)
	o.c.ok("POST", "/companies/"+cid+"/retail/packs/restaurant", map[string]any{"demo": true}, 200)
	if n := ownerQuery(t, `SELECT name FROM pos_registers WHERE company_id = $1 LIMIT 1`, cid); n != "الصندوق 1" {
		t.Fatalf("till name %q", n)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM warehouses WHERE company_id = $1 AND name = 'المستودع الرئيسي'`, cid); n != "1" {
		t.Fatalf("main warehouse in Arabic: %s", n)
	}
	if n := ownerQuery(t, `SELECT name FROM items WHERE company_id = $1 AND code = 'SHW'`, cid); n != "صحن شاورما دجاج" {
		t.Fatalf("dish name %q", n)
	}
	// a company created before the fix (empty chart): the pack loads the chart first, then the demo works
	old := str(o.c.ok("POST", "/companies", map[string]any{"name_en": "Old " + randHex(3), "base_currency": "USD", "local_currency": "LBP",
		"no_chart": true}, 201).body["id"])
	o.c.ok("POST", "/companies/"+old+"/rates", map[string]any{"currency": "LBP", "rate": "89500"}, 200)
	o.c.ok("POST", "/companies/"+old+"/retail/packs/supermarket", map[string]any{"demo": true}, 200)
	if n := ownerQuery(t, `SELECT count(*)::text FROM items WHERE company_id = $1`, old); n == "0" {
		t.Fatal("supermarket demo not loaded on a company that had no chart")
	}
	// an unknown business type is refused
	if r := o.c.do("POST", "/companies", map[string]any{"name_en": "Bad " + randHex(3), "business_type": "casino"}); r.status != 400 {
		t.Fatalf("bad business type: %d", r.status)
	}
}

// F1: choosing a pack shows the apps it does not need (and which hold data), and switches off only those agreed to.
func TestP7PackAppPlan(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon") // the fixture switches every app on
	o.c.ok("POST", o.p("/fleet/vehicles"), map[string]any{"name": "Truck " + randHex(2), "plate": "B " + randHex(3), "kind": "truck"}, 201)
	plan := o.c.ok("GET", o.p("/apps/plan?business_type=pharmacy"), nil, 200)
	remove := map[string]bool{}
	for _, x := range plan.body["remove"].([]any) {
		remove[str(obj(x)["app"])] = obj(x)["has_data"] == true
	}
	if _, ok := remove["mfg"]; !ok {
		t.Fatalf("plan does not offer to switch off manufacturing: %s", plan.raw)
	}
	if !remove["flt"] {
		t.Fatalf("fleet holds a vehicle but has_data is false: %s", plan.raw)
	}
	if _, ok := remove["gen"]; ok {
		t.Fatal("a core app offered for removal")
	}
	if r := o.c.do("GET", o.p("/apps/plan?business_type=casino"), nil); r.status != 400 {
		t.Fatalf("bad type: %d", r.status)
	}
	// switch off manufacturing and projects, keep fleet (it has data)
	o.c.ok("POST", o.p("/retail/packs/pharmacy"), map[string]any{"demo": false, "remove": []string{"mfg", "prj", "gen"}}, 200)
	apps := appsOf(t, o, o.company)
	if slices.Contains(apps, "mfg") || slices.Contains(apps, "prj") {
		t.Fatalf("mfg/prj still on: %v", apps)
	}
	if !slices.Contains(apps, "flt") || !slices.Contains(apps, "gen") || !slices.Contains(apps, "pos") {
		t.Fatalf("kept apps missing: %v", apps)
	}
	// the switched-off app's routes answer app_disabled
	if r := o.c.do("GET", o.p("/mfg/orders"), nil); r.status != 403 {
		t.Fatalf("mfg after switching off: %d", r.status)
	}
}

// F7: the translate-defaults tool renames untouched system names only, after a preview.
func TestP7TranslateDefaults(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon") // English company
	o.c.ok("GET", o.p("/crm/stages"), nil, 200)
	wh := str(o.c.ok("POST", o.p("/warehouses"), map[string]any{"name": "Main"}, 201).body["id"])
	o.c.ok("POST", o.p("/warehouses"), map[string]any{"name": "My own store"}, 201)
	o.c.ok("PATCH", o.p(""), map[string]any{"default_locale": "ar"}, 200)
	pre := o.c.ok("POST", o.p("/translate-defaults"), map[string]any{"dry_run": true}, 200)
	if !strings.Contains(pre.raw, "المستودع الرئيسي") || !strings.Contains(pre.raw, "مؤهل") {
		t.Fatalf("preview misses warehouse / stage: %s", pre.raw)
	}
	if n := ownerQuery(t, `SELECT name FROM warehouses WHERE id = $1`, wh); n != "Main" {
		t.Fatalf("dry run renamed: %s", n)
	}
	o.c.ok("POST", o.p("/translate-defaults"), map[string]any{}, 200)
	if n := ownerQuery(t, `SELECT name FROM warehouses WHERE id = $1`, wh); n != "المستودع الرئيسي" {
		t.Fatalf("warehouse after: %s", n)
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM warehouses WHERE company_id = $1 AND name = 'My own store'`, o.company); n != "1" {
		t.Fatal("a user-named warehouse was renamed")
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM crm_stages WHERE company_id = $1 AND name = 'Qualified'`, o.company); n != "0" {
		t.Fatal("stage not translated")
	}
	if n := ownerQuery(t, `SELECT count(*)::text FROM accounts WHERE company_id = $1 AND name ~ '[ء-ي]'`, o.company); n == "0" {
		t.Fatal("no account renamed to Arabic")
	}
	// English UI still has the English name
	if n := ownerQuery(t, `SELECT name_i18n->>'en' FROM crm_stages WHERE company_id = $1 AND name = 'مؤهل'`, o.company); n != "Qualified" {
		t.Fatalf("english kept as %q", n)
	}
}

// F12: no noise 4xx for a user without a van or an employee file.
func TestP7QuietEmpty(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	if r := o.c.ok("GET", o.p("/van/today?soft=1"), nil, 200); !strings.Contains(r.raw, `"van":null`) {
		t.Fatalf("soft van: %s", r.raw)
	}
	if r := o.c.do("GET", o.p("/van/today"), nil); r.status != 422 {
		t.Fatalf("van/today without soft: %d", r.status)
	}
	if r := o.c.ok("GET", o.p("/self/appraisals"), nil, 200); len(r.list) != 0 {
		t.Fatalf("self appraisals: %s", r.raw)
	}
}
