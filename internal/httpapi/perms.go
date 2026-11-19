package httpapi

import (
	"net/http"
	"slices"
	"sort"
	"strings"
)

// Permissions are "<module>.<action>". Every protected API route declares exactly one permission
// (routePerms); authenticate() refuses a request whose principal does not hold it. The built-in roles
// are presets: fixed permission sets that reproduce the original role rules. Tenants can add custom
// roles (table roles) with any subset of the catalogue; a custom role keeps a built-in "base" role,
// which only decides whether the user is office staff, a collector or a cashier (screens and data
// scoping), never what they may do.

// permAny marks routes open to every signed-in user (profile, reference data). It is still declared
// explicitly, so a route can never be added without thinking about who may use it.
const permAny = "*"

// moduleActions is the catalogue shown in the role editor (order = display order).
var moduleActions = []struct {
	Module  string   `json:"module"`
	Actions []string `json:"actions"`
}{
	{"accounting", []string{"view", "create", "edit", "void", "manage", "approve"}},
	{"customers", []string{"view", "create", "edit"}},
	{"billing", []string{"view", "create", "void", "manage", "approve"}},
	{"collections", []string{"view", "create", "edit", "void", "approve"}},
	{"sales", []string{"view", "create", "void", "approve"}},
	{"crm", []string{"view", "create", "edit", "manage"}},
	{"service", []string{"view", "create", "edit", "manage"}},
	{"pos", []string{"view", "create", "void", "manage", "approve"}},
	{"suppliers", []string{"view", "create", "edit"}},
	{"purchasing", []string{"view", "create", "void", "manage", "approve"}},
	{"expenses", []string{"view", "create", "void", "approve"}},
	{"stock", []string{"view", "create", "void", "manage", "approve"}},
	{"assets", []string{"view", "create", "edit", "void", "approve"}},
	{"payroll", []string{"view", "create", "edit", "void", "approve"}},
	{"bank", []string{"view", "create", "edit"}},
	{"network", []string{"view", "create", "manage"}},
	{"messaging", []string{"view", "create", "manage"}},
	{"reports", []string{"view", "export"}},
	{"import", []string{"create", "manage"}},
	{"settings", []string{"view", "manage"}},
	{"users", []string{"view", "create", "edit"}},
	{"audit", []string{"view"}},
	{"api", []string{"view", "create", "void"}},
}

var allPerms = func() map[string]bool {
	m := map[string]bool{}
	for _, ma := range moduleActions {
		for _, a := range ma.Actions {
			m[ma.Module+"."+a] = true
		}
	}
	return m
}()

func validPerm(p string) bool { return allPerms[p] }

// ---- presets (built-in roles) ----

// The preset sets are written as unions of these groups. They match the role lists the routes used
// before permissions existed (TestPresetsMatchLegacyRoles checks every route).
var (
	// read access to the office modules (every office role, read-only included)
	officeView = []string{"accounting.view", "customers.view", "billing.view", "collections.view", "sales.view",
		"crm.view", "suppliers.view", "purchasing.view", "expenses.view", "stock.view", "assets.view", "bank.view",
		"network.view", "messaging.view", "reports.view", "reports.export", "settings.view", "service.view"}
	// day-to-day data entry
	entryPerms = []string{"accounting.create", "customers.create", "customers.edit", "collections.create",
		"sales.create", "crm.create", "crm.edit", "pos.view", "pos.create", "pos.void", "suppliers.create",
		"suppliers.edit", "expenses.create", "stock.create", "network.create", "messaging.create", "service.create", "service.edit"}
	// accountant: money, posting, voids, configuration of the modules
	writePerms = []string{"accounting.edit", "accounting.void", "accounting.manage", "billing.create", "billing.void",
		"billing.manage", "collections.edit", "collections.void", "sales.void", "crm.manage", "pos.manage",
		"purchasing.create", "purchasing.void", "purchasing.manage", "expenses.void", "stock.void", "stock.manage",
		"assets.create", "assets.edit", "assets.void", "payroll.view", "payroll.create", "payroll.edit", "payroll.void",
		"bank.create", "bank.edit", "network.manage", "messaging.manage", "import.create", "audit.view", "service.manage"}
)

var presets = func() map[string][]string {
	all := make([]string, 0, len(allPerms))
	for p := range allPerms {
		all = append(all, p)
	}
	join := func(groups ...[]string) []string {
		var out []string
		for _, g := range groups {
			out = append(out, g...)
		}
		sort.Strings(out)
		return slices.Compact(out)
	}
	sort.Strings(all)
	return map[string][]string{
		RoleAdmin:      all,
		RoleAccountant: join(officeView, entryPerms, writePerms),
		RoleDataEntry:  join(officeView, entryPerms),
		RoleReadOnly:   join(officeView),
		RoleCollector:  {"collections.create", "collections.view", "customers.view", "network.view"},
		RoleCashier:    {"collections.view", "customers.view", "network.view", "pos.create", "pos.view", "pos.void"},
	}
}()

// permSet turns a list into a lookup set, keeping only known permissions.
func permSet(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, p := range list {
		if allPerms[p] {
			m[p] = true
		}
	}
	return m
}

func sortedPerms(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// can reports whether the request's principal holds permission p.
func can(r *http.Request, p string) bool {
	pr := principal(r)
	return p == permAny || pr.perms[p]
}

// approvePermFor: the permission that approves a request guarded by perm (same module).
func approvePermFor(perm string) string {
	mod, _, _ := strings.Cut(perm, ".")
	return mod + ".approve"
}

// ---- route → permission ----

const v1 = "/api/v1"

// routePerms maps every protected route pattern (as registered on the mux) to its permission.
var routePerms = map[string]string{
	// profile, reference data, platform console (platformOnly checks the owner list itself)
	"POST /auth/logout":                            permAny,
	"GET /me":                                      permAny,
	"PATCH /me":                                    permAny,
	"GET /currencies":                              permAny,
	"GET /translations":                            permAny,
	"PUT /translations":                            "settings.manage",
	"GET /companies":                               permAny,
	"GET /companies/{cid}/rates/latest":            permAny,
	"GET /companies/{cid}/documents/settings":      permAny,
	"GET /platform/tenants":                        permAny,
	"PATCH /platform/tenants/{id}":                 permAny,
	"GET /platform/summary":                        permAny,
	"GET /platform/setup":                          permAny,
	"GET /platform/tenants/{id}":                   permAny,
	"POST /platform/tenants/{id}/{op}":             permAny,
	"PATCH /platform/tenants/{id}/users/{uid}":     permAny,
	"GET /platform/payments":                       permAny,
	"GET /platform/plans":                          permAny,
	"POST /platform/plans":                         permAny,
	"PATCH /platform/plans/{code}":                 permAny,
	"GET /platform/requests":                       permAny,
	"POST /platform/requests/{id}/{op}":            permAny,
	"POST /platform/lifecycle/run":                 permAny,
	"POST /platform/support/end":                   permAny, // only does something in a support view
	"GET /subscription":                            permAny,
	"PATCH /subscription":                          "settings.manage",
	"POST /subscription/requests":                  "settings.manage",
	"GET /me/sessions":                             permAny,
	"POST /me/sessions/{id}/revoke":                permAny,
	"POST /me/sessions/revoke-others":              permAny,
	"POST /me/mfa/setup":                           permAny,
	"POST /me/mfa/enable":                          permAny,
	"POST /me/mfa/disable":                         permAny,
	"POST /me/mfa/recovery-codes":                  permAny,
	"GET /permissions":                             permAny,
	"GET /companies/{cid}/approvals":               permAny, // each user sees what they asked for, approvers see the rest
	"GET /companies/{cid}/approvals/{id}":          permAny,
	"POST /companies/{cid}/approvals/{id}/cancel":  permAny,
	"POST /companies/{cid}/approvals/{id}/approve": permAny, // the module's .approve permission is checked inside
	"POST /companies/{cid}/approvals/{id}/reject":  permAny,

	// dashboard, onboarding, demo data, bulk print
	"GET /companies/{cid}/dashboard":      "reports.view",
	"GET /companies/{cid}/onboarding":     "settings.view",
	"POST /demo-data":                     "settings.manage",
	"GET /companies/{cid}/invoices/print": "billing.view",

	// organisation, users, roles, security
	"POST /companies":                     "settings.manage",
	"PATCH /companies/{cid}":              "settings.manage",
	"PUT /companies/{cid}/apps":           "settings.manage",
	"GET /companies/{cid}/approval-rules": "settings.view",
	"PUT /companies/{cid}/approval-rules": "settings.manage",
	"GET /security/settings":              "users.view",
	"PUT /security/settings":              "users.edit",
	"GET /users":                          "users.view",
	"POST /users":                         "users.create",
	"PATCH /users/{id}":                   "users.edit",
	"GET /users/{id}/sessions":            "users.view",
	"POST /users/{id}/sessions/revoke":    "users.edit",
	"POST /users/{id}/reset-link":         "users.edit",
	"POST /users/{id}/mfa/reset":          "users.edit",
	"GET /roles":                          "users.view",
	"POST /roles":                         "users.edit",
	"PATCH /roles/{id}":                   "users.edit",
	"GET /audit":                          "audit.view",

	// developer API keys and webhooks
	"GET /api-keys":                                       "api.view",
	"POST /api-keys":                                      "api.create",
	"POST /api-keys/{id}/revoke":                          "api.void",
	"GET /companies/{cid}/webhooks":                       "api.view",
	"POST /companies/{cid}/webhooks":                      "api.create",
	"PATCH /companies/{cid}/webhooks/{id}":                "api.create",
	"POST /companies/{cid}/webhooks/{id}/rotate":          "api.create",
	"POST /companies/{cid}/webhooks/{id}/test":            "api.create",
	"GET /companies/{cid}/webhooks/{id}/deliveries":       "api.view",
	"POST /companies/{cid}/webhook-deliveries/{id}/retry": "api.create",

	// accounting
	"GET /companies/{cid}/rates":                   "accounting.view",
	"POST /companies/{cid}/rates":                  "accounting.manage",
	"GET /companies/{cid}/rates/settings":          "accounting.view",
	"PUT /companies/{cid}/rates/settings":          "accounting.manage",
	"POST /companies/{cid}/rates/fetch":            "accounting.manage",
	"GET /companies/{cid}/accounts":                "accounting.view",
	"POST /companies/{cid}/accounts":               "accounting.manage",
	"PATCH /companies/{cid}/accounts/{id}":         "accounting.manage",
	"GET /chart-templates":                         "accounting.view",
	"POST /companies/{cid}/accounts/load-template": "accounting.manage",
	"GET /companies/{cid}/journal":                 "accounting.view",
	"POST /companies/{cid}/journal":                "accounting.create",
	"GET /companies/{cid}/journal/{id}":            "accounting.view",
	"PUT /companies/{cid}/journal/{id}":            "accounting.create",
	"DELETE /companies/{cid}/journal/{id}":         "accounting.create",
	"POST /companies/{cid}/journal/{id}/post":      "accounting.edit",
	"POST /companies/{cid}/journal/{id}/reverse":   "accounting.void",
	"GET /companies/{cid}/recurring":               "accounting.view",
	"POST /companies/{cid}/recurring":              "accounting.edit",
	"PATCH /companies/{cid}/recurring/{id}":        "accounting.edit",
	"POST /companies/{cid}/recurring/{id}/run":     "accounting.edit",
	"GET /companies/{cid}/backoffice/settings":     "accounting.view",
	"PUT /companies/{cid}/backoffice/settings":     "accounting.manage",

	// billing setup
	"GET /companies/{cid}/billing/settings":       "billing.view",
	"PUT /companies/{cid}/billing/settings":       "billing.manage",
	"GET /companies/{cid}/payment-methods":        "collections.view",
	"POST /companies/{cid}/payment-methods":       "billing.manage",
	"PATCH /companies/{cid}/payment-methods/{id}": "billing.manage",
	"GET /companies/{cid}/areas":                  "customers.view",
	"POST /companies/{cid}/areas":                 "customers.edit",
	"PATCH /companies/{cid}/areas/{id}":           "customers.edit",
	"GET /companies/{cid}/plans":                  "billing.view",
	"POST /companies/{cid}/plans":                 "billing.manage",
	"PATCH /companies/{cid}/plans/{id}":           "billing.manage",
	"GET /companies/{cid}/tariffs":                "billing.view",
	"POST /companies/{cid}/tariffs":               "billing.manage",
	"POST /companies/{cid}/tariffs/{id}/apply":    "billing.manage",
	"GET /companies/{cid}/tax-rates":              "billing.view",
	"POST /companies/{cid}/tax-rates":             "billing.manage",
	"PATCH /companies/{cid}/tax-rates/{id}":       "billing.manage",

	// customers, subscriptions, portal links, statements
	"GET /companies/{cid}/customers":                     "customers.view",
	"POST /companies/{cid}/customers":                    "customers.create",
	"GET /companies/{cid}/customers/{id}":                "customers.view",
	"PATCH /companies/{cid}/customers/{id}":              "customers.edit",
	"POST /companies/{cid}/customers/{id}/subscriptions": "customers.create",
	"PATCH /companies/{cid}/subscriptions/{id}":          "customers.edit",
	"GET /companies/{cid}/customers/{id}/portal":         "customers.view",
	"POST /companies/{cid}/customers/{id}/portal":        "customers.create",
	"GET /companies/{cid}/customers/{id}/statement":      "customers.view",
	"GET /companies/{cid}/statements":                    "customers.view",

	// meter readings, payments, cash boxes
	"GET /companies/{cid}/readings":            "collections.view",
	"POST /companies/{cid}/readings":           "collections.create",
	"GET /companies/{cid}/payments":            "collections.view",
	"POST /companies/{cid}/payments":           "collections.create",
	"GET /companies/{cid}/payments/{id}":       "collections.view",
	"POST /companies/{cid}/payments/{id}/void": "collections.void",
	"GET /companies/{cid}/cash/balances":       "collections.view",
	"GET /companies/{cid}/cash/transfers":      "collections.view",
	"POST /companies/{cid}/cash/transfers":     "collections.edit",

	// invoices
	"POST /companies/{cid}/billing/run":               "billing.create",
	"GET /companies/{cid}/billing/jobs":               "billing.view",
	"GET /companies/{cid}/billing/jobs/{id}":          "billing.view",
	"GET /companies/{cid}/invoices":                   "billing.view",
	"POST /companies/{cid}/invoices":                  "billing.create",
	"GET /companies/{cid}/invoices/{id}":              "billing.view",
	"POST /companies/{cid}/invoices/{id}/void":        "billing.void",
	"POST /companies/{cid}/invoices/{id}/credit-note": "billing.void",
	"POST /companies/{cid}/customers/{id}/refunds":    "billing.void",

	// reports
	"GET /companies/{cid}/reports/trial-balance":    "reports.view",
	"GET /companies/{cid}/reports/ledger/{account}": "reports.view",
	"GET /companies/{cid}/reports/profit-loss":      "reports.view",
	"GET /companies/{cid}/reports/balance-sheet":    "reports.view",
	"GET /companies/{cid}/reports/aging":            "reports.view",
	"GET /companies/{cid}/reports/collections":      "reports.view",
	"GET /companies/{cid}/reports/vat":              "reports.view",
	"GET /companies/{cid}/reports/generators":       "reports.view",
	"GET /companies/{cid}/reports/stock-moves":      "reports.view",
	"GET /companies/{cid}/reports/sales":            "reports.view",
	"GET /companies/{cid}/reports/pos":              "reports.view",
	"GET /companies/{cid}/reports/ap-aging":         "reports.view",
	"GET /companies/{cid}/reports/stock-valuation":  "reports.view",
	"GET /companies/{cid}/reports/expenses":         "reports.view",
	"GET /companies/{cid}/reports/assets":           "reports.view",
	"GET /reports/consolidated":                     "reports.view",
	"POST /export/xlsx":                             "reports.export",

	// network (generators / ISP)
	"GET /companies/{cid}/stations":                        "network.view",
	"POST /companies/{cid}/stations":                       "network.create",
	"PATCH /companies/{cid}/stations/{id}":                 "network.create",
	"GET /companies/{cid}/generators":                      "network.view",
	"POST /companies/{cid}/generators":                     "network.create",
	"PATCH /companies/{cid}/generators/{id}":               "network.create",
	"GET /companies/{cid}/generators/{id}/log":             "network.view",
	"POST /companies/{cid}/generators/{id}/hours":          "network.create",
	"POST /companies/{cid}/generators/{id}/outages":        "network.create",
	"PATCH /companies/{cid}/generators/{id}/outages/{oid}": "network.create",
	"GET /companies/{cid}/boxes":                           "network.view",
	"POST /companies/{cid}/boxes":                          "network.create",
	"PATCH /companies/{cid}/boxes/{id}":                    "network.create",
	"GET /companies/{cid}/routers":                         "network.manage",
	"POST /companies/{cid}/routers":                        "network.manage",
	"PATCH /companies/{cid}/routers/{id}":                  "network.manage",
	"POST /companies/{cid}/routers/{id}/test":              "network.manage",
	"GET /companies/{cid}/isp/settings":                    "network.view",
	"PUT /companies/{cid}/isp/settings":                    "network.manage",
	"POST /companies/{cid}/isp/run":                        "network.manage",
	"GET /companies/{cid}/router-actions":                  "network.view",
	"POST /companies/{cid}/router-actions/{id}/retry":      "network.manage",
	"POST /companies/{cid}/subscriptions/{id}/network":     "network.manage",

	// import
	"POST /companies/{cid}/import/{type}": "import.create",
	"GET /magic/imports":                  "import.manage",
	"POST /magic/imports":                 "import.manage",
	"GET /magic/imports/{id}":             "import.manage",
	"POST /magic/imports/{id}/chunk":      "import.manage",
	"POST /magic/imports/{id}/analyze":    "import.manage",
	"POST /magic/imports/{id}/commit":     "import.manage",

	// messaging
	"GET /companies/{cid}/message-providers":        "messaging.manage",
	"POST /companies/{cid}/message-providers":       "messaging.manage",
	"PATCH /companies/{cid}/message-providers/{id}": "messaging.manage",
	"GET /companies/{cid}/message-templates":        "messaging.view",
	"POST /companies/{cid}/message-templates":       "messaging.manage",
	"PATCH /companies/{cid}/message-templates/{id}": "messaging.manage",
	"GET /companies/{cid}/messages/settings":        "messaging.view",
	"PUT /companies/{cid}/messages/settings":        "messaging.manage",
	"POST /companies/{cid}/messages/send":           "messaging.create",
	"POST /companies/{cid}/messages/link":           "messaging.view",
	"GET /companies/{cid}/messages":                 "messaging.view",
	"POST /companies/{cid}/messages/{id}/retry":     "messaging.manage",
	"POST /companies/{cid}/statements/send":         "messaging.create",
	"POST /companies/{cid}/send-document":           "messaging.create",
	"PUT /companies/{cid}/documents/settings":       "settings.manage",

	// sales
	"GET /companies/{cid}/sales/orders":                "sales.view",
	"POST /companies/{cid}/sales/orders":               "sales.create",
	"GET /companies/{cid}/sales/orders/{id}":           "sales.view",
	"PUT /companies/{cid}/sales/orders/{id}":           "sales.create",
	"POST /companies/{cid}/sales/orders/{id}/deliver":  "sales.create",
	"POST /companies/{cid}/sales/orders/{id}/invoice":  "billing.create",
	"POST /companies/{cid}/sales/orders/{id}/{action}": "sales.create",
	"POST /companies/{cid}/sales/deliveries/{id}/void": "sales.void",

	// CRM
	"GET /companies/{cid}/crm/stages":              "crm.view",
	"POST /companies/{cid}/crm/stages":             "crm.manage",
	"PATCH /companies/{cid}/crm/stages/{id}":       "crm.manage",
	"GET /companies/{cid}/crm/summary":             "crm.view",
	"GET /companies/{cid}/crm/users":               "crm.view",
	"GET /companies/{cid}/crm/leads":               "crm.view",
	"POST /companies/{cid}/crm/leads":              "crm.create",
	"GET /companies/{cid}/crm/leads/{id}":          "crm.view",
	"PATCH /companies/{cid}/crm/leads/{id}":        "crm.edit",
	"POST /companies/{cid}/crm/leads/{id}/convert": "crm.create",
	"GET /companies/{cid}/crm/activities":          "crm.view",
	"POST /companies/{cid}/crm/activities":         "crm.create",
	"PATCH /companies/{cid}/crm/activities/{id}":   "crm.edit",
	"DELETE /companies/{cid}/crm/activities/{id}":  "crm.edit",

	// point of sale
	"GET /companies/{cid}/pos/registers":            "pos.view",
	"POST /companies/{cid}/pos/registers":           "pos.manage",
	"PATCH /companies/{cid}/pos/registers/{id}":     "pos.manage",
	"GET /companies/{cid}/pos/categories":           "pos.view",
	"POST /companies/{cid}/pos/categories":          "pos.manage",
	"PATCH /companies/{cid}/pos/categories/{id}":    "pos.manage",
	"GET /companies/{cid}/pos/catalog":              "pos.view",
	"GET /companies/{cid}/pos/price-list":           "pos.view",
	"GET /companies/{cid}/pos/sessions":             "pos.view",
	"POST /companies/{cid}/pos/sessions":            "pos.create",
	"GET /companies/{cid}/pos/sessions/{id}":        "pos.view",
	"POST /companies/{cid}/pos/sessions/{id}/close": "pos.create",
	"GET /companies/{cid}/pos/orders":               "pos.view",
	"POST /companies/{cid}/pos/orders":              "pos.create",
	"GET /companies/{cid}/pos/orders/{id}":          "pos.view",
	"POST /companies/{cid}/pos/orders/{id}/refund":  "pos.void",
	"POST /companies/{cid}/pos/customers":           "pos.create",

	// purchasing, expenses, stock
	"GET /companies/{cid}/purchasing/settings":          "purchasing.view",
	"PUT /companies/{cid}/purchasing/settings":          "purchasing.manage",
	"GET /companies/{cid}/suppliers":                    "suppliers.view",
	"POST /companies/{cid}/suppliers":                   "suppliers.create",
	"GET /companies/{cid}/suppliers/{id}":               "suppliers.view",
	"PATCH /companies/{cid}/suppliers/{id}":             "suppliers.edit",
	"GET /companies/{cid}/bills":                        "purchasing.view",
	"POST /companies/{cid}/bills":                       "purchasing.create",
	"GET /companies/{cid}/bills/{id}":                   "purchasing.view",
	"POST /companies/{cid}/bills/{id}/void":             "purchasing.void",
	"POST /companies/{cid}/bills/{id}/debit-note":       "purchasing.void",
	"GET /companies/{cid}/supplier-payments":            "purchasing.view",
	"POST /companies/{cid}/supplier-payments":           "purchasing.create",
	"POST /companies/{cid}/supplier-payments/{id}/void": "purchasing.void",
	"GET /companies/{cid}/expenses":                     "expenses.view",
	"POST /companies/{cid}/expenses":                    "expenses.create",
	"POST /companies/{cid}/expenses/{id}/void":          "expenses.void",
	"GET /companies/{cid}/items":                        "stock.view",
	"POST /companies/{cid}/items":                       "stock.manage",
	"GET /companies/{cid}/items/{id}":                   "stock.view",
	"PATCH /companies/{cid}/items/{id}":                 "stock.manage",
	"GET /companies/{cid}/warehouses":                   "stock.view",
	"POST /companies/{cid}/warehouses":                  "stock.manage",
	"GET /companies/{cid}/stock/docs":                   "stock.view",
	"POST /companies/{cid}/stock/{kind}":                "stock.create",
	"POST /companies/{cid}/stock/docs/{id}/void":        "stock.void",
	"GET /companies/{cid}/stock/transfers":              "stock.view",
	"POST /companies/{cid}/stock/transfers":             "stock.create",
	"GET /companies/{cid}/items/{id}/extras":            "stock.view",
	"PUT /companies/{cid}/items/{id}/stock-settings":    "stock.manage",
	"PUT /companies/{cid}/items/{id}/units":             "stock.manage",
	"PUT /companies/{cid}/items/{id}/components":        "stock.manage",
	"GET /companies/{cid}/items/{id}/price":             permAny,
	"GET /companies/{cid}/stock/lots":                   "stock.view",
	"GET /companies/{cid}/stock/lots/{id}/trace":        "stock.view",
	"PUT /companies/{cid}/stock/reorder-rules":          "stock.manage",
	"GET /companies/{cid}/reports/stock-replenishment":  "stock.view",
	"GET /companies/{cid}/stock/counts":                 "stock.view",
	"POST /companies/{cid}/stock/counts":                "stock.create",
	"GET /companies/{cid}/stock/counts/{id}":            "stock.view",
	"PUT /companies/{cid}/stock/counts/{id}/lines":      "stock.create",
	"POST /companies/{cid}/stock/counts/{id}/post":      "stock.manage",
	"POST /companies/{cid}/stock/counts/{id}/cancel":    "stock.create",
	"GET /companies/{cid}/bills/{id}/landed-costs":      "purchasing.view",
	"POST /companies/{cid}/bills/{id}/landed-costs":     "purchasing.create",
	"GET /companies/{cid}/price-lists":                  "stock.view",
	"POST /companies/{cid}/price-lists":                 "stock.manage",
	"PATCH /companies/{cid}/price-lists/{id}":           "stock.manage",
	"GET /companies/{cid}/price-lists/{id}/items":       "stock.view",
	"PUT /companies/{cid}/price-lists/{id}/items":       "stock.manage",
	"PUT /companies/{cid}/customers/{id}/price-list":    "customers.edit",
	"POST /companies/{cid}/stock/transfers/{id}/void":   "stock.void",

	// HR & payroll, fixed assets, bank reconciliation
	"GET /companies/{cid}/employees":                         "payroll.view",
	"POST /companies/{cid}/employees":                        "payroll.create",
	"GET /companies/{cid}/employees/{id}":                    "payroll.view",
	"PATCH /companies/{cid}/employees/{id}":                  "payroll.edit",
	"POST /companies/{cid}/employees/{id}/advances":          "payroll.create",
	"GET /companies/{cid}/payroll":                           "payroll.view",
	"POST /companies/{cid}/payroll":                          "payroll.create",
	"GET /companies/{cid}/payroll/{id}":                      "payroll.view",
	"DELETE /companies/{cid}/payroll/{id}":                   "payroll.void",
	"PATCH /companies/{cid}/payroll/{id}/payslips/{pid}":     "payroll.edit",
	"POST /companies/{cid}/payroll/{id}/post":                "payroll.edit",
	"POST /companies/{cid}/payroll/{id}/pay":                 "payroll.edit",
	"POST /companies/{cid}/payroll/{id}/void":                "payroll.void",
	"POST /companies/{cid}/payroll/{id}/recalculate":         "payroll.edit",
	"POST /companies/{cid}/payroll/{id}/approve":             "payroll.approve",
	"POST /companies/{cid}/payroll/{id}/unapprove":           "payroll.approve",
	"GET /companies/{cid}/payroll/{id}/payslips/{pid}":       "payroll.view",
	"GET /companies/{cid}/payroll/{id}/wps":                  "payroll.view",
	"GET /companies/{cid}/employees/{id}/file":               "payroll.view",
	"PUT /companies/{cid}/employees/{id}/file":               "payroll.edit",
	"GET /companies/{cid}/employees/{id}/end-of-service":     "payroll.view",
	"POST /companies/{cid}/employees/{id}/login":             "users.create", // payroll.edit checked inside
	"POST /companies/{cid}/employees/{id}/documents":         "payroll.edit",
	"GET /companies/{cid}/employees/{id}/documents/{did}":    "payroll.view",
	"DELETE /companies/{cid}/employees/{id}/documents/{did}": "payroll.edit",
	"GET /companies/{cid}/hr/overview":                       "payroll.view",
	"GET /companies/{cid}/hr/schedules":                      "payroll.view",
	"POST /companies/{cid}/hr/schedules":                     "payroll.edit",
	"PUT /companies/{cid}/hr/schedules/{id}":                 "payroll.edit",
	"GET /companies/{cid}/hr/holidays":                       "payroll.view",
	"POST /companies/{cid}/hr/holidays":                      "payroll.edit",
	"DELETE /companies/{cid}/hr/holidays/{id}":               "payroll.edit",
	"GET /companies/{cid}/hr/attendance":                     "payroll.view",
	"PUT /companies/{cid}/hr/attendance":                     "payroll.edit",
	"POST /companies/{cid}/hr/attendance/import":             "payroll.edit",
	"GET /companies/{cid}/hr/leave-types":                    "payroll.view",
	"POST /companies/{cid}/hr/leave-types":                   "payroll.edit",
	"PUT /companies/{cid}/hr/leave-types/{id}":               "payroll.edit",
	"GET /companies/{cid}/hr/leave":                          "payroll.view",
	"POST /companies/{cid}/hr/leave":                         "payroll.create",
	"GET /companies/{cid}/hr/leave/balances":                 "payroll.view",
	"POST /companies/{cid}/hr/leave/{id}/decide":             permAny, // payroll approvers or the employee's manager (checked inside)
	"POST /companies/{cid}/hr/leave/{id}/cancel":             "payroll.edit",
	"GET /companies/{cid}/hr/payroll-params":                 "payroll.view",
	"POST /companies/{cid}/hr/payroll-params":                "payroll.approve",
	"GET /companies/{cid}/reports/hr/{kind}":                 "payroll.view",
	"GET /companies/{cid}/self":                              permAny, // employee self-service: the signed-in user's own employee file
	"POST /companies/{cid}/self/clock":                       permAny,
	"POST /companies/{cid}/self/leave":                       permAny,
	"POST /companies/{cid}/self/leave/{id}/cancel":           permAny,
	"GET /companies/{cid}/self/payslips/{pid}":               permAny,
	"GET /companies/{cid}/self/team-leave":                   permAny,
	"GET /companies/{cid}/assets":                            "assets.view",
	"POST /companies/{cid}/assets":                           "assets.create",
	"GET /companies/{cid}/assets/{id}":                       "assets.view",
	"PATCH /companies/{cid}/assets/{id}":                     "assets.edit",
	"POST /companies/{cid}/assets/{id}/dispose":              "assets.void",
	"POST /companies/{cid}/depreciation/run":                 "assets.create",
	"GET /companies/{cid}/bank/statements":                   "bank.view",
	"POST /companies/{cid}/bank/statements":                  "bank.create",
	"GET /companies/{cid}/bank/statements/{id}":              "bank.view",
	"POST /companies/{cid}/bank/statements/{id}/auto-match":  "bank.edit",
	"POST /companies/{cid}/bank/lines/{id}/match":            "bank.edit",
	"POST /companies/{cid}/bank/lines/{id}/entry":            "bank.edit",
	"POST /companies/{cid}/bank/lines/{id}/{action}":         "bank.edit",
	"GET /companies/{cid}/bank/reconciliation":               "bank.view",

	// professional accounting
	"GET /companies/{cid}/accounting/settings":  "accounting.view",
	"PUT /companies/{cid}/accounting/settings":  "accounting.manage",
	"GET /companies/{cid}/cost-centers":         "accounting.view",
	"POST /companies/{cid}/cost-centers":        "accounting.manage",
	"PATCH /companies/{cid}/cost-centers/{id}":  "accounting.manage",
	"PATCH /companies/{cid}/journal-lines/{id}": "accounting.edit",
	"GET /companies/{cid}/reports/cost-centers": "reports.view",
	"GET /companies/{cid}/budgets":              "accounting.view",
	"PUT /companies/{cid}/budgets":              "accounting.manage",
	"GET /companies/{cid}/reports/budget":       "reports.view",
	"GET /companies/{cid}/fiscal-years":         "accounting.view",
	"POST /companies/{cid}/fiscal-years/close":  "accounting.manage",
	"POST /companies/{cid}/fiscal-years/reopen": "accounting.approve",
	"POST /companies/{cid}/opening-balances":    "accounting.manage",
	"GET /companies/{cid}/exchanges":            "collections.view",
	"POST /companies/{cid}/exchanges":           "collections.edit",
	"POST /companies/{cid}/exchanges/{id}/void": "collections.void",
	"GET /companies/{cid}/cheques":              "collections.view",
	"POST /companies/{cid}/cheques":             "collections.edit",
	"GET /companies/{cid}/cheques/{id}":         "collections.view",
	"POST /companies/{cid}/cheques/{id}/{op}":   "collections.edit",
	"GET /companies/{cid}/reports/cash-flow":    "reports.view",
	"GET /companies/{cid}/reports/compare":      "reports.view",
	"GET /companies/{cid}/owner-summary":        "reports.view",
	"GET /companies/{cid}/journal/{id}/source":  "accounting.view",

	// Odoo audit stage: purchase orders, dunning, FX revaluation, deferrals, attachments
	"GET /companies/{cid}/purchase-orders":                "purchasing.view",
	"POST /companies/{cid}/purchase-orders":               "purchasing.create",
	"POST /companies/{cid}/purchase-orders/from-reorder":  "purchasing.create",
	"GET /companies/{cid}/purchase-orders/{id}":           "purchasing.view",
	"PUT /companies/{cid}/purchase-orders/{id}":           "purchasing.create",
	"POST /companies/{cid}/purchase-orders/{id}/bill":     "purchasing.create",
	"POST /companies/{cid}/purchase-orders/{id}/{action}": "purchasing.create",
	"GET /companies/{cid}/dunning":                        "billing.view",
	"PUT /companies/{cid}/dunning":                        "billing.manage",
	"GET /companies/{cid}/dunning/preview":                "billing.view",
	"POST /companies/{cid}/dunning/run":                   "billing.manage",
	"GET /companies/{cid}/dunning/events":                 "billing.view",
	"GET /companies/{cid}/fx-revaluation":                 "accounting.view",
	"GET /companies/{cid}/fx-revaluation/preview":         "accounting.view",
	"POST /companies/{cid}/fx-revaluation":                "accounting.manage",
	"GET /companies/{cid}/deferrals":                      "accounting.view",
	"POST /companies/{cid}/deferrals":                     "accounting.edit",
	"POST /companies/{cid}/deferrals/run":                 "accounting.edit",
	"GET /companies/{cid}/deferrals/{id}":                 "accounting.view",
	"POST /companies/{cid}/deferrals/{id}/cancel":         "accounting.void",
	"GET /companies/{cid}/payment-terms":                  "billing.view",
	"PUT /companies/{cid}/payment-terms":                  "billing.manage",
	// finishing stage
	"GET /companies/{cid}/billing/proration":               "billing.view",
	"PUT /companies/{cid}/billing/proration":               "billing.manage",
	"POST /companies/{cid}/subscriptions/{id}/change-plan": "customers.edit",
	"POST /companies/{cid}/subscriptions/{id}/stop":        "customers.edit",
	"GET /companies/{cid}/subscriptions/{id}/changes":      "customers.view",
	"POST /companies/{cid}/purchase-orders/{id}/receive":   "purchasing.create",
	"GET /companies/{cid}/purchase-orders/{id}/match":      "purchasing.view",
	"POST /companies/{cid}/goods-receipts/{id}/cancel":     "purchasing.void",
	"GET /companies/{cid}/purchasing/matching":             "purchasing.view",
	"PUT /companies/{cid}/purchasing/matching":             "purchasing.manage",
	"GET /companies/{cid}/supplier-prices":                 "purchasing.view",
	"POST /companies/{cid}/supplier-prices":                "purchasing.manage",
	"GET /companies/{cid}/supplier-prices/best":            "purchasing.view",
	"PUT /companies/{cid}/supplier-prices/{id}":            "purchasing.manage",
	"DELETE /companies/{cid}/supplier-prices/{id}":         "purchasing.manage",
	"GET /companies/{cid}/self/claims":                     permAny,
	"POST /companies/{cid}/self/claims":                    permAny,
	"POST /companies/{cid}/self/claims/{id}/cancel":        permAny,
	"GET /companies/{cid}/hr/claims":                       "payroll.view",
	"POST /companies/{cid}/hr/claims":                      "payroll.create",
	"POST /companies/{cid}/hr/claims/{id}/decide":          permAny, // payroll approvers or the employee's manager (checked inside)
	"POST /companies/{cid}/hr/claims/{id}/pay":             "payroll.edit",
	"POST /companies/{cid}/hr/claims/{id}/cancel":          "payroll.edit",
	"GET /companies/{cid}/hr/rotations":                    "payroll.view",
	"POST /companies/{cid}/hr/rotations":                   "payroll.edit",
	"PUT /companies/{cid}/hr/rotations/{id}":               "payroll.edit",
	"POST /companies/{cid}/hr/rotations/{id}/assign":       "payroll.edit",
	"GET /companies/{cid}/hr/roster":                       "payroll.view",
	"PUT /companies/{cid}/hr/roster":                       "payroll.edit",
	"POST /companies/{cid}/hr/leave/accrue":                "payroll.edit",
	"POST /companies/{cid}/hr/leave/adjust":                "payroll.approve",
	"GET /companies/{cid}/hr/leave/accruals":               "payroll.view",
	"GET /companies/{cid}/hr/clock-settings":               "payroll.view",
	"PUT /companies/{cid}/hr/clock-settings":               "payroll.edit",
	"GET /companies/{cid}/hr/attendance/{id}/photo":        "payroll.view",
	"GET /companies/{cid}/hr/devices":                      "payroll.view",
	"POST /companies/{cid}/hr/devices":                     "payroll.edit",
	"PATCH /companies/{cid}/hr/devices/{id}":               "payroll.edit",
	"GET /companies/{cid}/custom-fields":                   permAny,
	"POST /companies/{cid}/custom-fields":                  "settings.manage",
	"GET /companies/{cid}/custom-fields/report":            "reports.view",
	"PUT /companies/{cid}/custom-fields/{id}":              "settings.manage",
	"GET /companies/{cid}/custom-values/{entity}/{id}":     permAny, // the record's own view permission is the screen's; values are not sensitive
	"PUT /companies/{cid}/custom-values/{entity}/{id}":     "customers.edit",
	"GET /companies/{cid}/mfg/boms":                        "stock.view",
	"POST /companies/{cid}/mfg/boms":                       "stock.manage",
	"GET /companies/{cid}/mfg/boms/{id}":                   "stock.view",
	"PUT /companies/{cid}/mfg/boms/{id}":                   "stock.manage",
	"GET /companies/{cid}/mfg/orders":                      "stock.view",
	"POST /companies/{cid}/mfg/orders":                     "stock.create",
	"GET /companies/{cid}/mfg/orders/{id}":                 "stock.view",
	"POST /companies/{cid}/mfg/orders/{id}/produce":        "stock.manage",
	"POST /companies/{cid}/mfg/orders/{id}/cancel":         "stock.void",
	"POST /companies/{cid}/mfg/scrap":                      "stock.manage",
	// retail core: variants, promotions, loyalty, gift cards, scales and labels, POS extras, packs
	"GET /companies/{cid}/items/templates":                       "stock.view",
	"PUT /companies/{cid}/items/{id}/variant-setup":              "stock.manage",
	"POST /companies/{cid}/items/{id}/variants/generate":         "stock.manage",
	"GET /companies/{cid}/items/{id}/variants":                   "stock.view",
	"PUT /companies/{cid}/items/{id}/variants":                   "stock.manage",
	"PUT /companies/{cid}/items/{id}/retail":                     "stock.manage",
	"GET /companies/{cid}/retail/promotions":                     "pos.view",
	"POST /companies/{cid}/retail/promotions":                    "pos.manage",
	"PUT /companies/{cid}/retail/promotions/{id}":                "pos.manage",
	"POST /companies/{cid}/retail/promotions/evaluate":           "pos.view",
	"GET /companies/{cid}/retail/loyalty":                        "pos.view",
	"PUT /companies/{cid}/retail/loyalty":                        "pos.manage",
	"GET /companies/{cid}/retail/loyalty/lookup":                 "pos.view",
	"GET /companies/{cid}/retail/loyalty/members":                "customers.view",
	"PUT /companies/{cid}/retail/loyalty/customers/{id}":         "pos.create",
	"POST /companies/{cid}/retail/loyalty/customers/{id}/adjust": "pos.manage",
	"GET /companies/{cid}/retail/loyalty/customers/{id}/moves":   "customers.view",
	"GET /companies/{cid}/retail/gift-cards":                     "pos.view",
	"POST /companies/{cid}/retail/gift-cards":                    "pos.manage",
	"GET /companies/{cid}/retail/gift-cards/lookup":              "pos.view",
	"POST /companies/{cid}/retail/gift-cards/{id}/void":          "pos.manage",
	"GET /companies/{cid}/retail/gift-cards/{id}/moves":          "pos.view",
	"GET /companies/{cid}/retail/scale":                          "stock.view",
	"PUT /companies/{cid}/retail/scale":                          "stock.manage",
	"GET /companies/{cid}/retail/scale/plu":                      "stock.view",
	"GET /companies/{cid}/retail/labels":                         "stock.view",
	"GET /companies/{cid}/retail/expiry":                         "stock.view",
	"POST /companies/{cid}/retail/expiry/markdown":               "pos.manage",
	"GET /companies/{cid}/retail/packs":                          "settings.view",
	"POST /companies/{cid}/retail/packs/{key}":                   "settings.manage",
	"GET /companies/{cid}/reports/retail":                        "reports.view",
	"GET /companies/{cid}/pos/held":                              "pos.view",
	"POST /companies/{cid}/pos/held":                             "pos.create",
	"DELETE /companies/{cid}/pos/held/{id}":                      "pos.create",
	"POST /companies/{cid}/pos/quick-item":                       "pos.create",
	"GET /companies/{cid}/payment-gateways":                      "settings.view",
	"POST /companies/{cid}/payment-gateways":                     "settings.manage",
	"PUT /companies/{cid}/payment-gateways/{id}":                 "settings.manage",
	"GET /companies/{cid}/payment-intents":                       "collections.view",
	"POST /companies/{cid}/payment-intents/{id}/{action}":        "collections.edit",
	"GET /companies/{cid}/einvoice/config":                       "settings.view",
	"PUT /companies/{cid}/einvoice/config":                       "settings.manage",
	"POST /companies/{cid}/einvoice/zatca/onboard":               "settings.manage",
	"GET /companies/{cid}/invoices/{id}/einvoice":                "billing.view",
	"POST /companies/{cid}/invoices/{id}/einvoice":               "billing.create",
	"GET /companies/{cid}/attachments":                           permAny,
	"POST /companies/{cid}/attachments":                          permAny,
	"GET /companies/{cid}/attachments/{id}/file":                 permAny,
	"DELETE /companies/{cid}/attachments/{id}":                   permAny,
	// service desk and generator maintenance
	"GET /companies/{cid}/tickets":                  "service.view",
	"POST /companies/{cid}/tickets":                 "service.create",
	"GET /companies/{cid}/tickets/summary":          "service.view",
	"GET /companies/{cid}/tickets/{id}":             "service.view",
	"PATCH /companies/{cid}/tickets/{id}":           "service.edit",
	"POST /companies/{cid}/tickets/{id}/notes":      "service.create",
	"GET /companies/{cid}/maintenance":              "service.view",
	"POST /companies/{cid}/maintenance/tasks":       "service.manage",
	"PATCH /companies/{cid}/maintenance/tasks/{id}": "service.manage",
	"GET /companies/{cid}/maintenance/log":          "service.view",
	"POST /companies/{cid}/maintenance/log":         "service.create",
}

// permForPattern returns the permission of a mux pattern ("" when the route is not declared).
func permForPattern(pattern string) string {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok || !strings.HasPrefix(path, v1) {
		return ""
	}
	return routePerms[method+" "+strings.TrimPrefix(path, v1)]
}
