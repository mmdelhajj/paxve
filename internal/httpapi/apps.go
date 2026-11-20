package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Apps are Odoo-style modules a company switches on or off (companies.apps). The core apps
// (gen = subscriptions & billing, acc = accounting, col = collections) are always on. A route that
// belongs to a switched-off app answers 403 "app_disabled", so the API and the UI agree.

var knownApps = []string{"gen", "acc", "col", "msg", "stk", "pur", "net", "sat", "pos", "hr", "crm", "svc", "mfg"}
var coreApps = []string{"gen", "acc", "col"}

// appRoutes: path fragment (after /companies/{cid}) → app. Checked in order; first match wins.
// Reference lookups other modules also use (items for invoice lines, tax rates…) stay open for GET.
var appRoutes = []struct {
	prefix string
	app    string
	getsOK bool // GET stays allowed when the app is off (shared reference data)
}{
	{"/pos/", "pos", false},
	{"/reports/pos", "pos", false},
	{"/retail/packs", "", false}, // choosing a business pack switches apps on itself
	{"/retail/scale", "stk", false},
	{"/retail/labels", "stk", false},
	{"/retail/expiry", "stk", false},
	{"/retail/", "pos", false},
	{"/reports/retail", "pos", false},
	{"/employees", "hr", false},
	{"/payroll", "hr", false},
	{"/hr/", "hr", false},
	{"/self", "hr", false},
	{"/reports/hr", "hr", false},
	{"/crm/", "crm", false},
	{"/sales/", "crm", false},
	{"/reports/sales", "crm", false},
	{"/routers", "net", false},
	{"/isp/", "net", false},
	{"/router-actions", "net", false},
	{"/items", "stk", true},
	{"/price-lists", "stk", true},
	{"/warehouses", "stk", true},
	{"/stock/", "stk", false},
	{"/wh/dropship", "pur", false},
	{"/wh/", "stk", false},
	{"/reports/stock-", "stk", false},
	{"/suppliers", "pur", true},
	{"/bills", "pur", false},
	{"/purchase-orders", "pur", false},
	{"/goods-receipts", "pur", false},
	{"/supplier-prices", "pur", false},
	{"/purchasing/matching", "pur", false},
	{"/mfg/", "mfg", false},
	{"/supplier-payments", "pur", false},
	{"/expenses", "pur", false},
	{"/purchasing/", "pur", true},
	{"/reports/ap-aging", "pur", false},
	{"/reports/expenses", "pur", false},
	{"/tickets", "svc", false},
	{"/repairs", "svc", false},
	{"/quote-templates", "crm", false},
	{"/maintenance", "svc", false},
	{"/message-providers", "msg", false},
	{"/message-templates", "msg", false},
	{"/messages", "msg", false},
	{"/statements/send", "msg", false},
	{"/send-document", "msg", false},
}

// appForRoute returns the app a mux pattern belongs to ("" = core, always allowed).
func appForRoute(method, path string) string {
	_, rest, ok := strings.Cut(path, "/companies/{cid}")
	if !ok {
		return ""
	}
	for _, a := range appRoutes {
		if strings.HasPrefix(rest, a.prefix) {
			if a.getsOK && method == http.MethodGet {
				return ""
			}
			return a.app
		}
	}
	return ""
}

type appCacheEntry struct {
	apps []string
	exp  time.Time
}

var appCache sync.Map // company id → appCacheEntry

func forgetApps(cid string) { appCache.Delete(cid) }

// companyApps reads the enabled apps of a company of this tenant (cached 30 s).
func (s *Server) companyApps(ctx context.Context, tenantID, cid string) ([]string, bool) {
	if v, ok := appCache.Load(cid); ok {
		e := v.(appCacheEntry)
		if time.Now().Before(e.exp) {
			return e.apps, true
		}
	}
	pool := s.OwnerPool
	if pool == nil {
		pool = s.Pool
	}
	var apps []string
	err := pool.QueryRow(ctx, `SELECT apps FROM companies WHERE id = $1::uuid AND tenant_id = $2::uuid`, cid, tenantID).Scan(&apps)
	if err != nil {
		return nil, false
	}
	appCache.Store(cid, appCacheEntry{apps: apps, exp: time.Now().Add(30 * time.Second)})
	return apps, true
}

// planAllows: the organisation's Paxve plan includes the app (core apps always; nil plan list = every app).
func planAllows(p *Principal, app string) bool {
	return p.PlanApps == nil || slices.Contains(coreApps, app) || slices.Contains(p.PlanApps, app)
}

// appBlocked: "app_not_in_plan" when the route belongs to an app the organisation's plan does not include,
// "app_disabled" when the company switched the app off, "" when the route may run.
func (s *Server) appBlocked(r *http.Request, p *Principal) string {
	cid := r.PathValue("cid")
	if cid == "" || !isUUID(cid) {
		return ""
	}
	method, path, _ := strings.Cut(r.Pattern, " ")
	app := appForRoute(method, strings.TrimPrefix(path, v1))
	if app == "" {
		return ""
	}
	if !planAllows(p, app) {
		return "app_not_in_plan"
	}
	apps, ok := s.companyApps(r.Context(), p.TenantID, cid)
	if !ok || slices.Contains(apps, app) {
		return "" // an unknown company answers 404 in the handler, as before
	}
	return "app_disabled"
}

// PUT /companies/{cid}/apps {apps: [...], business_type}
func (s *Server) updateApps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid := r.PathValue("cid")
	if !isUUID(cid) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		Apps         []string `json:"apps"`
		BusinessType *string  `json:"business_type"`
	}
	if err := decode(w, r, &in); err != nil || in.Apps == nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	set := append([]string{}, coreApps...)
	for _, a := range in.Apps {
		if !slices.Contains(knownApps, a) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		if !slices.Contains(set, a) {
			set = append(set, a)
		}
	}
	// an app outside the plan cannot be switched on (one that is already on, e.g. after a plan change, may stay)
	cur, _ := s.companyApps(ctx, principal(r).TenantID, cid)
	for _, a := range set {
		if !planAllows(principal(r), a) && !slices.Contains(cur, a) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "app_not_in_plan", "detail": a})
			return
		}
	}
	if in.BusinessType != nil && !slices.Contains([]string{"generator", "internet", "satellite", "water", "supermarket", "clothing"}, *in.BusinessType) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var c company
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE companies SET apps = $2, business_type = coalesce($3, business_type)
			WHERE id = $1 RETURNING `+companyCols, cid, set, in.BusinessType)
		if err != nil {
			return err
		}
		c, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[company])
		return err
	})
	forgetApps(cid)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeErr(w, http.StatusNotFound, "not_found")
	case err != nil:
		serverErr(w, r, err)
	default:
		writeJSON(w, http.StatusOK, c)
	}
}
