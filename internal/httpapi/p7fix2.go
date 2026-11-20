package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/charts"
	"github.com/mmdelhajj/paxve/internal/db"
)

// Phase 7 audit C fixes (UX): the apps of each business type, the chart loaded with every new company, defaults in
// the company's language and a tool that renames the system-created defaults of existing companies.

var businessTypes = []string{"generator", "internet", "satellite", "water", "supermarket", "clothing", "electronics", "furniture",
	"wholesale", "restaurant", "pharmacy", "factory"}

// subscription businesses (monthly bills, meters, collectors); the packs come from retailPacks
var subscriptionApps = map[string][]string{
	"generator": {"msg", "stk", "pur", "hr", "svc", "ai"},
	"internet":  {"msg", "stk", "pur", "net", "hr", "svc", "ai"},
	"satellite": {"msg", "stk", "pur", "sat", "hr", "svc", "ai"},
	"water":     {"msg", "stk", "pur", "hr", "svc", "ai"},
}

// appsForBusiness: the apps a company of this business type starts with (core apps first). Other apps are one click
// away on the Apps page (audit P7-F1: a pharmacy no longer sees generator, network, manufacturing or fleet).
func appsForBusiness(bt string) []string {
	out := append([]string{}, coreApps...)
	add := func(list []string) {
		for _, a := range list {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	if list, ok := subscriptionApps[bt]; ok {
		add(list)
		return out
	}
	for _, p := range retailPacks {
		if p.BusinessType == bt {
			add(p.Apps)
			add([]string{"ai"})
			return out
		}
	}
	add(subscriptionApps["generator"])
	return out
}

// appDataTables: a company "uses" an app when one of these tables has its rows (asked before switching it off).
var appDataTables = map[string][]string{
	"msg": {"messages"}, "stk": {"items"}, "pur": {"bills", "purchase_orders"}, "net": {"routers"}, "pos": {"pos_orders"},
	"hr": {"employees"}, "crm": {"crm_leads", "sales_orders"}, "svc": {"tickets"}, "mfg": {"mfg_orders"}, "prj": {"projects"},
	"flt": {"vehicles"}, "web": {"shop_orders"}, "mkt": {"mkt_campaigns"},
}

func appHasData(ctx context.Context, tx pgx.Tx, cid, app string) bool {
	for _, t := range appDataTables[app] {
		sp, err := tx.Begin(ctx) // a savepoint: a missing table must not abort the caller's transaction
		if err != nil {
			return false
		}
		var ok bool
		err = sp.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+pgx.Identifier{t}.Sanitize()+` WHERE company_id = $1)`, cid).Scan(&ok)
		if err != nil {
			_ = sp.Rollback(ctx)
			continue
		}
		_ = sp.Commit(ctx)
		if ok {
			return true
		}
	}
	return false
}

type appChange struct {
	App     string `json:"app"`
	HasData bool   `json:"has_data"`
}

// appPlan: what choosing a business type changes: apps to add (allowed by the plan) and apps it does not need.
func appPlan(ctx context.Context, tx pgx.Tx, p *Principal, cid, bt string) (target, add []string, remove []appChange, err error) {
	var cur []string
	if err = tx.QueryRow(ctx, `SELECT apps FROM companies WHERE id = $1`, cid).Scan(&cur); err != nil {
		return
	}
	target = appsForBusiness(bt)
	add, remove = []string{}, []appChange{}
	for _, a := range target {
		if !slices.Contains(cur, a) && planAllows(p, a) {
			add = append(add, a)
		}
	}
	for _, a := range cur {
		if !slices.Contains(target, a) && !slices.Contains(coreApps, a) {
			remove = append(remove, appChange{a, appHasData(ctx, tx, cid, a)})
		}
	}
	return
}

// GET /companies/{cid}/apps/plan?business_type=pharmacy
func (s *Server) appsPlan(w http.ResponseWriter, r *http.Request) {
	bt := r.URL.Query().Get("business_type")
	if !slices.Contains(businessTypes, bt) || !isUUID(r.PathValue("cid")) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		target, add, remove, err := appPlan(ctx, tx, principal(r), r.PathValue("cid"), bt)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"business_type": bt, "target": target, "add": add, "remove": remove}, nil
	})
}

// newApps: current apps + the target's (allowed by the plan), minus the ones the user agreed to switch off.
func newApps(p *Principal, cur, target, off []string) []string {
	out := []string{}
	for _, a := range cur {
		if slices.Contains(off, a) && !slices.Contains(target, a) && !slices.Contains(coreApps, a) {
			continue
		}
		out = append(out, a)
	}
	for _, a := range target {
		if !slices.Contains(out, a) && planAllows(p, a) {
			out = append(out, a)
		}
	}
	return out
}

// ---------------- the company's language ----------------

// companyLang: the language system-created records of the company are named in (ar, fr or en).
func companyLang(ctx context.Context, tx pgx.Tx, cid string) string {
	var l string
	if err := tx.QueryRow(ctx, `SELECT default_locale FROM companies WHERE id = $1`, cid).Scan(&l); err != nil {
		return "en"
	}
	return normLang(l)
}

func normLang(l string) string {
	switch l {
	case "ar", "fa", "ur", "ckb":
		return "ar"
	case "fr":
		return "fr"
	}
	return "en"
}

func pickLang(lang, en, ar, fr string) string {
	switch {
	case lang == "ar" && ar != "":
		return ar
	case lang == "fr" && fr != "":
		return fr
	}
	return en
}

// ---------------- the chart of a company ----------------

// tenantTemplate: the chart template a new company of the organisation should use (that of its first company).
func tenantTemplate(ctx context.Context, tx pgx.Tx) *charts.Template {
	var key string
	_ = tx.QueryRow(ctx, `SELECT l10n FROM companies WHERE l10n <> '' ORDER BY created_at LIMIT 1`).Scan(&key)
	if tpl := charts.Find(key); tpl != nil {
		return tpl
	}
	return charts.Find("lebanon")
}

// ensureChart loads the chart template into a company whose chart is empty (audit P7-F2: a company added from
// Settings → Companies used to start with no accounts, so every pack and posting failed).
func ensureChart(ctx context.Context, tx pgx.Tx, cid, template, lang string) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE company_id = $1`, cid).Scan(&n); err != nil || n > 0 {
		return err
	}
	tpl := charts.Find(template)
	if tpl == nil {
		tpl = tenantTemplate(ctx, tx)
	}
	if tpl == nil {
		return &apiError{status: http.StatusUnprocessableEntity, code: "chart_missing"}
	}
	if lang == "" {
		lang = companyLang(ctx, tx, cid)
	}
	return loadTemplate(ctx, tx, cid, tpl, lang)
}

// RepairEmptyCharts loads the chart of live companies created without one (before the fix). Idempotent: it only
// touches companies with no account at all, in organisations that are in trial, active or grace.
func (s *Server) RepairEmptyCharts(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	type row struct{ tid, cid, lang string }
	var list []row
	rows, err := s.OwnerPool.Query(ctx, `SELECT c.tenant_id::text, c.id::text, c.default_locale FROM companies c JOIN tenants t ON t.id = c.tenant_id
		WHERE c.is_active AND t.last_state IN ('trial', 'active', 'grace')
		  AND NOT EXISTS (SELECT 1 FROM accounts a WHERE a.company_id = c.id)`)
	if err != nil {
		slog.Warn("repair charts", "err", err)
		return
	}
	for rows.Next() {
		var x row
		if rows.Scan(&x.tid, &x.cid, &x.lang) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	for _, x := range list {
		err := db.InTenant(ctx, s.Pool, db.Scope{TenantID: x.tid}, func(tx pgx.Tx) error {
			return ensureChart(ctx, tx, x.cid, "", normLang(x.lang))
		})
		slog.Info("repaired empty chart", "company", x.cid, "err", err)
	}
}

// ---------------- rename the system-created defaults ----------------

// translatable defaults (English name as created before → names per language)
var defaultNames = []struct{ table, en, ar, fr string }{
	{"warehouses", "Main", "المستودع الرئيسي", "Dépôt principal"},
	{"warehouses", "Showroom", "صالة العرض", "Salle d'exposition"},
	{"warehouses", "Van 1", "السيارة 1", "Camionnette 1"},
	{"pos_registers", "Till 1", "الصندوق 1", "Caisse 1"},
	{"suppliers", "Demo wholesaler", "تاجر جملة (تجريبي)", "Grossiste (démo)"},
	{"plans", "Metered", "حسب العداد", "Au compteur"},
	{"sales_routes", "Daily route", "الجولة اليومية", "Tournée du jour"},
	{"rest_tables_area", "Hall", "الصالة", "Salle"},
	{"rest_tables_area", "Terrace", "التراس", "Terrasse"},
	{"mfg_workcenters", "Cutting", "القص", "Découpe"},
	{"mfg_workcenters", "Assembly", "التجميع", "Assemblage"},
	{"mfg_workcenters", "Painting", "الدهان", "Peinture"},
	{"price_lists", "Wholesale", "سعر الجملة", "Prix de gros"},
	{"price_lists", "Distributor", "سعر الموزع", "Prix distributeur"},
}

var demoItemCodes = []string{"BAN", "TOM", "CHS", "MLK", "LBN", "BRD", "WTR", "JUC", "SOP", "TSH", "JNS", "SCK", "BLT",
	"PH-A1", "PH-USED", "CHG", "CASE", "TV55", "SOFA3", "DTBL", "BED160", "CHAIR", "WTR-6", "COLA-24", "CHIPS-30", "BLEACH-12",
	"ING-CHK", "ING-BRD", "ING-POT", "ING-TOM", "ING-GAR", "COLA", "SHW", "FRIES", "FTSH", "PAN500", "FEV500", "AMOX500", "TRAM50",
	"VITC", "SUNB", "STEEL", "SCREW", "PAINT", "CAB"}

var demoCategories = []string{"Fruits & vegetables", "Dairy", "Bakery", "Drinks", "Cleaning", "Men", "Women", "Summer", "Accessories",
	"Phones", "Appliances", "Living room", "Bedroom", "Office", "Beverages", "Snacks", "Ingredients", "Mains", "Starters", "Medicines",
	"Personal care", "Vitamins", "Raw materials", "Finished goods"}

var nameTargetRe = regexp.MustCompile(`^[a-z_]+$`)

// POST /companies/{cid}/translate-defaults {dry_run} — renames only records still carrying the English name the
// system gave them (accounts, CRM stages, the main warehouse, the till, demo items…); answers what it (would) rename.
func (s *Server) translateDefaults(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DryRun bool   `json:"dry_run"`
		Lang   string `json:"lang"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid := r.PathValue("cid")
	if !isUUID(cid) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	ctx := r.Context()
	type change struct {
		Kind string `json:"kind"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	var out []change
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return err
		}
		lang := normLang(in.Lang)
		if in.Lang == "" {
			lang = companyLang(ctx, tx, cid)
		}
		if lang == "en" {
			return nil
		}
		collect := func(kind, q string, args ...any) error {
			rows, err := tx.Query(ctx, q, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var c change
				if err := rows.Scan(&c.From, &c.To); err != nil {
					return err
				}
				c.Kind = kind
				out = append(out, c)
			}
			return rows.Err()
		}
		// accounts and CRM stages carry their translations in name_i18n: use them when the name is still the English one
		set := func(table, kind, english string, extra ...any) error {
			args := append([]any{cid, lang}, extra...)
			q := `SELECT name, name_i18n->>$2 FROM ` + table + ` WHERE company_id = $1 AND coalesce(name_i18n->>$2, '') <> '' AND name <> name_i18n->>$2 AND ` + english
			if err := collect(kind, q, args...); err != nil {
				return err
			}
			if in.DryRun {
				return nil
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			if _, err := sp.Exec(ctx, `UPDATE `+table+` SET name_i18n = name_i18n || jsonb_build_object('en', name), name = name_i18n->>$2
				WHERE company_id = $1 AND coalesce(name_i18n->>$2, '') <> '' AND name <> name_i18n->>$2 AND `+english, args...); err != nil {
				return sp.Rollback(ctx) // e.g. the translated name is already taken: leave these as they are
			}
			return sp.Commit(ctx)
		}
		if err := set("accounts", "account", `(name = name_i18n->>'en' OR name_i18n->>'en' IS NULL)`); err != nil {
			return err
		}
		stages := []string{}
		for _, st := range defaultStages {
			stages = append(stages, "'"+st.Name+"'")
		}
		if err := set("crm_stages", "stage", `name IN (`+strings.Join(stages, ",")+`)`); err != nil {
			return err
		}
		if err := set("items", "item", `code = ANY ('{`+strings.Join(demoItemCodes, ",")+`}'::text[])`); err != nil {
			return err
		}
		if err := set("item_categories", "category", `name = ANY ($3::text[])`, demoCategories); err != nil {
			return err
		}
		for _, d := range defaultNames {
			to := pickLang(lang, d.en, d.ar, d.fr)
			table, col := d.table, "name"
			if table == "rest_tables_area" {
				table, col = "rest_tables", "area"
			}
			if !nameTargetRe.MatchString(table) {
				continue
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			var n int
			if err := sp.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE company_id = $1 AND `+col+` = $2`, cid, d.en).Scan(&n); err != nil {
				_ = sp.Rollback(ctx) // table not there (older schema): skip
				continue
			}
			if n > 0 && !in.DryRun {
				if _, err := sp.Exec(ctx, `UPDATE `+table+` SET `+col+` = $3 WHERE company_id = $1 AND `+col+` = $2`, cid, d.en, to); err != nil {
					_ = sp.Rollback(ctx) // e.g. a record already has the translated name: leave it
					continue
				}
			}
			if err := sp.Commit(ctx); err != nil {
				return err
			}
			for i := 0; i < n; i++ {
				out = append(out, change{table, d.en, to})
			}
		}
		return nil
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	if out == nil {
		out = []change{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dry_run": in.DryRun, "changes": out})
}
