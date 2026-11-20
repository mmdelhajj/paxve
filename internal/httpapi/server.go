// Package httpapi is the JSON API (/api/v1) and the static web app server.
package httpapi

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmdelhajj/paxve/internal/db"
)

const (
	RoleAdmin      = "admin"
	RoleAccountant = "accountant"
	RoleDataEntry  = "data_entry"
	RoleReadOnly   = "read_only"
	RoleCollector  = "collector"
	RoleCashier    = "cashier"
)

var validRoles = map[string]bool{RoleAdmin: true, RoleAccountant: true, RoleDataEntry: true, RoleReadOnly: true, RoleCollector: true, RoleCashier: true}

// Languages the interface ships with. Adding one = adding a translation file in web/src/locales.
var supportedLocales = map[string]bool{"en": true, "ar": true, "fr": true,
	"tr": true, "es": true, "de": true, "fa": true, "ur": true, "ckb": true, "ru": true, "pt": true}

//go:embed openapi.yaml
var openapiSpec []byte

type Server struct {
	Pool     *pgxpool.Pool
	Sessions *scs.SessionManager
	WebDir   string
	// login limits (audit B-H3): a generous per-IP cap (an office behind one shared/CGNAT address is not locked
	// out by one user's typos), a per account+IP back-off, and a looser per-account back-off against guessing
	// one password from many IPs. Locks never grow while you wait.
	loginIP    *limiter
	loginPair  *backoff
	loginEmail *backoff
	loginTrust *trustSet // account+address pairs that signed in recently: not held by the per-account lock

	// OwnerPool connects as the database owner. Used ONLY for self sign-up (creating a new
	// organisation, before any tenant exists) and for the platform owner console.
	OwnerPool      *pgxpool.Pool
	platformAdmins map[string]bool
	signupLimiter  *limiter
	portalLimiter  *limiter
	apiKeyLimiter  *limiter       // failed API key attempts per IP
	resetLimiter   *limiter       // password reset e-mails per IP / per address
	mfaLimiter     *limiter       // wrong two-factor codes per user
	mux            *http.ServeMux // kept to replay approved requests
	protected      []string       // patterns registered with Handle (the authenticated routes and the web app)

	// AllowPrivateRouters lets routers point to private/loopback addresses (tests, VPN setups).
	// Off by default so tenants cannot use "test connection" to probe the server's internal network.
	AllowPrivateRouters bool
	// RateFeed overrides the published exchange-rate source (tests); nil = open.er-api.com.
	RateFeed RateFeed

	// shown to organisations on "My Paxve subscription" (PAXVE_SALES_PHONE / PAXVE_SALES_EMAIL); renewal
	// requests are also sent to this phone
	platformContactPhone, platformContactEmail string
}

func New(pool, ownerPool *pgxpool.Pool, sessions *scs.SessionManager, webDir string, platformAdmins []string) *Server {
	admins := map[string]bool{}
	for _, e := range platformAdmins {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			admins[e] = true
		}
	}
	return &Server{Pool: pool, OwnerPool: ownerPool, Sessions: sessions, WebDir: webDir,
		loginIP: newLimiter(100, 15*time.Minute), loginPair: newBackoff(5, 30*time.Second, 15*time.Minute, time.Hour),
		loginEmail: newBackoff(10, time.Minute, 15*time.Minute, time.Hour), loginTrust: newTrustSet(30 * 24 * time.Hour), signupLimiter: newLimiter(5, time.Hour), portalLimiter: newLimiter(120, 10*time.Minute), platformAdmins: admins,
		apiKeyLimiter: newLimiter(30, 15*time.Minute), resetLimiter: newLimiter(5, time.Hour), mfaLimiter: newLimiter(5, 15*time.Minute),
		platformContactPhone: strings.TrimSpace(os.Getenv("PAXVE_SALES_PHONE")), platformContactEmail: strings.TrimSpace(os.Getenv("PAXVE_SALES_EMAIL"))}
}

func (s *Server) Handler() http.Handler {
	base := http.NewServeMux()
	s.mux = base
	mux := &routeMux{ServeMux: base}
	const v = v1

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/health/ready", s.ready)
	mux.HandleFunc("GET "+v+"/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(openapiSpec)
	})

	// SaaS: self sign-up and platform owner console
	mux.HandleFunc("POST "+v+"/signup", s.signup)

	// customer portal (public, secret link) and dashboard
	mux.HandleFunc("GET "+v+"/portal/{token}", s.portal)
	mux.HandleFunc("GET "+v+"/portal/{token}/invoices/{id}", s.portalInvoice)
	mux.HandleFunc("GET "+v+"/portal/{token}/quotes/{id}", s.portalQuote)
	mux.HandleFunc("POST "+v+"/portal/{token}/quotes/{id}/accept", s.portalAcceptQuote)
	mux.HandleFunc("GET "+v+"/portal/{token}/statement/{from}/{to}", s.portalStatement)
	mux.Handle("GET "+v+"/companies/{cid}/customers/{id}/portal", s.field(s.customerPortal))
	mux.Handle("POST "+v+"/companies/{cid}/customers/{id}/portal", s.auth(s.customerPortal))
	mux.Handle("GET "+v+"/companies/{cid}/dashboard", s.auth(s.dashboard))
	mux.Handle("GET "+v+"/companies/{cid}/onboarding", s.auth(s.onboarding))
	mux.Handle("POST "+v+"/demo-data", s.auth(sessionOnly(s.loadDemoData)))
	mux.Handle("GET "+v+"/companies/{cid}/invoices/print", s.auth(s.printInvoices))
	mux.Handle("GET "+v+"/platform/tenants", s.auth(s.platformOnly(s.platformTenants)))
	mux.Handle("PATCH "+v+"/platform/tenants/{id}", s.auth(s.platformOnly(s.platformUpdateTenant)))
	mux.Handle("GET "+v+"/platform/summary", s.auth(s.platformOnly(s.platformSummary)))
	mux.Handle("GET "+v+"/platform/setup", s.auth(s.platformOnly(s.platformSetup)))
	mux.Handle("GET "+v+"/platform/tenants/{id}", s.auth(s.platformOnly(s.platformTenant)))
	mux.Handle("POST "+v+"/platform/tenants/{id}/{op}", s.auth(s.platformOnly(s.platformTenantAction)))
	mux.Handle("PATCH "+v+"/platform/tenants/{id}/users/{uid}", s.auth(s.platformOnly(s.platformUpdateUser)))
	mux.Handle("GET "+v+"/platform/payments", s.auth(s.platformOnly(s.platformPayments)))
	mux.Handle("GET "+v+"/platform/plans", s.auth(s.platformOnly(s.platformPlans)))
	mux.Handle("POST "+v+"/platform/plans", s.auth(s.platformOnly(s.platformCreatePlan)))
	mux.Handle("PATCH "+v+"/platform/plans/{code}", s.auth(s.platformOnly(s.platformUpdatePlan)))
	mux.Handle("GET "+v+"/platform/requests", s.auth(s.platformOnly(s.platformRequests)))
	mux.Handle("POST "+v+"/platform/requests/{id}/{op}", s.auth(s.platformOnly(s.platformRequestAction)))
	mux.Handle("POST "+v+"/platform/lifecycle/run", s.auth(s.platformOnly(s.platformRunLifecycle)))
	mux.Handle("POST "+v+"/platform/support/end", s.auth(s.endSupport))
	mux.Handle("GET "+v+"/subscription", s.auth(s.mySubscription))
	mux.Handle("PATCH "+v+"/subscription", s.auth(s.updateMySubscription))
	mux.Handle("POST "+v+"/subscription/requests", s.auth(s.createSubscriptionRequest))

	// session (browser) auth, two-factor, password reset
	mux.HandleFunc("POST "+v+"/auth/login", s.login)
	mux.HandleFunc("POST "+v+"/auth/mfa", s.loginMFA)
	mux.HandleFunc("POST "+v+"/auth/forgot", s.forgotPassword)
	mux.HandleFunc("GET "+v+"/auth/reset/{token}", s.checkResetToken)
	mux.HandleFunc("POST "+v+"/auth/reset", s.resetPassword)
	mux.Handle("POST "+v+"/auth/logout", s.field(s.logout))
	mux.Handle("GET "+v+"/me", s.field(s.me))
	mux.Handle("PATCH "+v+"/me", s.field(sessionOnly(s.updateMe)))
	mux.Handle("GET "+v+"/me/sessions", s.field(sessionOnly(s.mySessions)))
	mux.Handle("POST "+v+"/me/sessions/{id}/revoke", s.field(sessionOnly(s.revokeMySession)))
	mux.Handle("POST "+v+"/me/sessions/revoke-others", s.field(sessionOnly(s.revokeOtherSessions)))
	mux.Handle("POST "+v+"/me/mfa/setup", s.field(sessionOnly(s.mfaSetup)))
	mux.Handle("POST "+v+"/me/mfa/enable", s.field(sessionOnly(s.mfaEnable)))
	mux.Handle("POST "+v+"/me/mfa/disable", s.field(sessionOnly(s.mfaDisable)))
	mux.Handle("POST "+v+"/me/mfa/recovery-codes", s.field(sessionOnly(s.mfaRecoveryCodes)))
	mux.Handle("GET "+v+"/permissions", s.field(s.permissionCatalogue))

	// roles, security settings, user sessions, approvals
	mux.Handle("GET "+v+"/roles", s.auth(s.listRoles))
	mux.Handle("POST "+v+"/roles", s.auth(sessionOnly(s.saveRole)))
	mux.Handle("PATCH "+v+"/roles/{id}", s.auth(sessionOnly(s.saveRole)))
	mux.Handle("GET "+v+"/security/settings", s.auth(s.getSecuritySettings))
	mux.Handle("PUT "+v+"/security/settings", s.auth(sessionOnly(s.putSecuritySettings)))
	mux.Handle("GET "+v+"/users/{id}/sessions", s.auth(s.userSessions))
	mux.Handle("POST "+v+"/users/{id}/sessions/revoke", s.auth(sessionOnly(s.revokeUserSessions)))
	mux.Handle("POST "+v+"/users/{id}/reset-link", s.auth(sessionOnly(s.createResetLink)))
	mux.Handle("POST "+v+"/users/{id}/mfa/reset", s.auth(sessionOnly(s.resetUserMFA)))
	mux.Handle("GET "+v+"/companies/{cid}/approval-rules", s.auth(s.getApprovalRules))
	mux.Handle("PUT "+v+"/companies/{cid}/approval-rules", s.auth(sessionOnly(s.putApprovalRules)))
	mux.Handle("GET "+v+"/companies/{cid}/approvals", s.auth(s.listApprovals))
	mux.Handle("GET "+v+"/companies/{cid}/approvals/{id}", s.auth(s.getApproval))
	mux.Handle("POST "+v+"/companies/{cid}/approvals/{id}/approve", s.auth(sessionOnly(s.approveRequest)))
	mux.Handle("POST "+v+"/companies/{cid}/approvals/{id}/reject", s.auth(sessionOnly(s.rejectRequest)))
	mux.Handle("POST "+v+"/companies/{cid}/approvals/{id}/cancel", s.auth(s.cancelRequest))
	mux.Handle("GET "+v+"/reports/consolidated", s.auth(s.consolidatedReport))

	// outbound webhooks
	// (API keys may manage webhooks too, within their scopes: api.view / api.create)
	mux.Handle("GET "+v+"/companies/{cid}/webhooks", s.auth(s.listWebhooks))
	mux.Handle("POST "+v+"/companies/{cid}/webhooks", s.auth(s.saveWebhook))
	mux.Handle("PATCH "+v+"/companies/{cid}/webhooks/{id}", s.auth(s.saveWebhook))
	mux.Handle("POST "+v+"/companies/{cid}/webhooks/{id}/rotate", s.auth(s.rotateWebhookSecret))
	mux.Handle("POST "+v+"/companies/{cid}/webhooks/{id}/test", s.auth(s.testWebhook))
	mux.Handle("GET "+v+"/companies/{cid}/webhooks/{id}/deliveries", s.auth(s.listDeliveries))
	mux.Handle("POST "+v+"/companies/{cid}/webhook-deliveries/{id}/retry", s.auth(s.retryDelivery))

	// reference data
	mux.Handle("GET "+v+"/currencies", s.field(s.listCurrencies))
	mux.Handle("GET "+v+"/translations", s.field(s.listTranslations))
	mux.Handle("PUT "+v+"/translations", s.auth(s.saveTranslation))

	// organisation
	mux.Handle("GET "+v+"/companies", s.field(s.listCompanies))
	mux.Handle("POST "+v+"/companies", s.auth(s.createCompany))
	mux.Handle("PATCH "+v+"/companies/{cid}", s.auth(s.updateCompany))
	mux.Handle("PUT "+v+"/companies/{cid}/apps", s.auth(s.updateApps))
	mux.Handle("GET "+v+"/users", s.auth(s.listUsers))
	mux.Handle("POST "+v+"/users", s.auth(s.createUser))
	mux.Handle("PATCH "+v+"/users/{id}", s.auth(s.updateUser))
	mux.Handle("GET "+v+"/audit", s.auth(s.listAudit))

	// developer API keys (managed from the browser only)
	mux.Handle("GET "+v+"/api-keys", s.auth(sessionOnly(s.listAPIKeys)))
	mux.Handle("POST "+v+"/api-keys", s.auth(sessionOnly(s.createAPIKey)))
	mux.Handle("POST "+v+"/api-keys/{id}/revoke", s.auth(sessionOnly(s.revokeAPIKey)))

	// exchange rates
	mux.Handle("GET "+v+"/companies/{cid}/rates", s.auth(s.listRates))
	mux.Handle("GET "+v+"/companies/{cid}/rates/latest", s.field(s.latestRates))
	mux.Handle("POST "+v+"/companies/{cid}/rates", s.auth(s.upsertRate))
	mux.Handle("GET "+v+"/companies/{cid}/rates/settings", s.auth(s.rateSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/rates/settings", s.auth(s.saveRateSettings))
	mux.Handle("POST "+v+"/companies/{cid}/rates/fetch", s.auth(s.fetchRatesNow))

	// chart of accounts
	mux.Handle("GET "+v+"/companies/{cid}/accounts", s.auth(s.listAccounts))
	mux.Handle("POST "+v+"/companies/{cid}/accounts", s.auth(s.createAccount))
	mux.Handle("PATCH "+v+"/companies/{cid}/accounts/{id}", s.auth(s.updateAccount))
	mux.Handle("GET "+v+"/chart-templates", s.auth(s.listChartTemplates))
	mux.Handle("POST "+v+"/companies/{cid}/accounts/load-template", s.auth(s.loadChartTemplate))

	// journal
	mux.Handle("GET "+v+"/companies/{cid}/journal", s.auth(s.listEntries))
	mux.Handle("POST "+v+"/companies/{cid}/journal", s.auth(s.createEntry))
	mux.Handle("GET "+v+"/companies/{cid}/journal/{id}", s.auth(s.getEntry))
	mux.Handle("PUT "+v+"/companies/{cid}/journal/{id}", s.auth(s.replaceEntry))
	mux.Handle("DELETE "+v+"/companies/{cid}/journal/{id}", s.auth(s.deleteEntry))
	mux.Handle("POST "+v+"/companies/{cid}/journal/{id}/post", s.auth(s.postEntry))
	mux.Handle("POST "+v+"/companies/{cid}/journal/{id}/reverse", s.auth(s.approvable("void", s.reverseEntry)))

	// billing setup
	mux.Handle("GET "+v+"/companies/{cid}/billing/settings", s.auth(s.getBillingSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/billing/settings", s.auth(s.putBillingSettings))
	mux.Handle("GET "+v+"/companies/{cid}/payment-methods", s.field(s.listMethods))
	mux.Handle("POST "+v+"/companies/{cid}/payment-methods", s.auth(s.createMethod))
	mux.Handle("PATCH "+v+"/companies/{cid}/payment-methods/{id}", s.auth(s.updateMethod))
	mux.Handle("GET "+v+"/companies/{cid}/areas", s.field(s.listAreas))
	mux.Handle("POST "+v+"/companies/{cid}/areas", s.auth(s.createArea))
	mux.Handle("PATCH "+v+"/companies/{cid}/areas/{id}", s.auth(s.updateArea))
	mux.Handle("GET "+v+"/companies/{cid}/plans", s.auth(s.listPlans))
	mux.Handle("POST "+v+"/companies/{cid}/plans", s.auth(s.createPlan))
	mux.Handle("PATCH "+v+"/companies/{cid}/plans/{id}", s.auth(s.updatePlan))

	// customers, subscriptions, meter readings
	mux.Handle("GET "+v+"/companies/{cid}/customers", s.field(s.listCustomers))
	mux.Handle("POST "+v+"/companies/{cid}/customers", s.auth(s.createCustomer))
	mux.Handle("GET "+v+"/companies/{cid}/customers/{id}", s.field(s.getCustomer))
	mux.Handle("PATCH "+v+"/companies/{cid}/customers/{id}", s.auth(s.updateCustomer))
	mux.Handle("POST "+v+"/companies/{cid}/customers/{id}/subscriptions", s.auth(s.createSubscription))
	mux.Handle("PATCH "+v+"/companies/{cid}/subscriptions/{id}", s.auth(s.updateSubscription))
	mux.Handle("GET "+v+"/companies/{cid}/readings", s.field(s.readingSheet))
	mux.Handle("POST "+v+"/companies/{cid}/readings", s.field(s.saveReadings))

	// invoices and payments
	mux.Handle("POST "+v+"/companies/{cid}/billing/run", s.auth(s.runBilling))
	mux.Handle("GET "+v+"/companies/{cid}/invoices", s.auth(s.listInvoices))
	mux.Handle("POST "+v+"/companies/{cid}/invoices", s.auth(s.creditGate(s.createInvoice)))
	mux.Handle("GET "+v+"/companies/{cid}/invoices/{id}", s.auth(s.getInvoice))
	mux.Handle("POST "+v+"/companies/{cid}/invoices/{id}/void", s.auth(s.approvable("void", s.voidInvoice)))
	mux.Handle("GET "+v+"/companies/{cid}/payments", s.field(s.listPayments))
	mux.Handle("POST "+v+"/companies/{cid}/payments", s.field(s.createPayment))
	mux.Handle("GET "+v+"/companies/{cid}/payments/{id}", s.field(s.getPayment))
	mux.Handle("POST "+v+"/companies/{cid}/payments/{id}/void", s.auth(s.approvable("void", s.voidPayment)))
	mux.Handle("GET "+v+"/companies/{cid}/billing/jobs", s.auth(s.listBillingJobs))
	mux.Handle("GET "+v+"/companies/{cid}/billing/jobs/{id}", s.auth(s.getBillingJob))
	mux.Handle("POST "+v+"/companies/{cid}/invoices/{id}/credit-note", s.auth(s.approvable("credit_note", s.createCreditNote)))
	mux.Handle("POST "+v+"/companies/{cid}/customers/{id}/refunds", s.auth(s.approvable("credit_note,refund,payment", s.createRefund)))
	mux.Handle("POST "+v+"/companies/{cid}/bills/{id}/debit-note", s.auth(s.approvable("credit_note", s.createDebitNote)))
	mux.Handle("POST "+v+"/companies/{cid}/stock/docs/{id}/void", s.auth(s.approvable("void", s.voidStockDoc)))
	mux.Handle("GET "+v+"/companies/{cid}/stock/transfers", s.auth(s.listStockTransfers))
	mux.Handle("POST "+v+"/companies/{cid}/stock/transfers", s.auth(s.createStockTransfer))
	// professional stock (module T)
	mux.Handle("GET "+v+"/companies/{cid}/items/{id}/extras", s.auth(s.getItemExtras))
	mux.Handle("PUT "+v+"/companies/{cid}/items/{id}/stock-settings", s.auth(s.putItemStockSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/items/{id}/units", s.auth(s.putItemUnits))
	mux.Handle("PUT "+v+"/companies/{cid}/items/{id}/components", s.auth(s.putItemComponents))
	mux.Handle("GET "+v+"/companies/{cid}/items/{id}/price", s.field(s.itemPrice))
	mux.Handle("GET "+v+"/companies/{cid}/stock/lots", s.auth(s.listLots))
	mux.Handle("GET "+v+"/companies/{cid}/stock/lots/{id}/trace", s.auth(s.traceLot))
	mux.Handle("PUT "+v+"/companies/{cid}/stock/reorder-rules", s.auth(s.putReorderRule))
	mux.Handle("GET "+v+"/companies/{cid}/reports/stock-replenishment", s.auth(s.replenishment))
	mux.Handle("GET "+v+"/companies/{cid}/stock/counts", s.auth(s.listStockCounts))
	mux.Handle("POST "+v+"/companies/{cid}/stock/counts", s.auth(s.createStockCount))
	mux.Handle("GET "+v+"/companies/{cid}/stock/counts/{id}", s.auth(s.getStockCount))
	mux.Handle("PUT "+v+"/companies/{cid}/stock/counts/{id}/lines", s.auth(s.putStockCountLines))
	mux.Handle("POST "+v+"/companies/{cid}/stock/counts/{id}/post", s.auth(s.postStockCount))
	mux.Handle("POST "+v+"/companies/{cid}/stock/counts/{id}/cancel", s.auth(s.cancelStockCount))
	mux.Handle("GET "+v+"/companies/{cid}/bills/{id}/landed-costs", s.auth(s.listLandedCosts))
	mux.Handle("POST "+v+"/companies/{cid}/bills/{id}/landed-costs", s.auth(s.createLandedCost))
	mux.Handle("GET "+v+"/companies/{cid}/price-lists", s.auth(s.listPriceLists))
	mux.Handle("POST "+v+"/companies/{cid}/price-lists", s.auth(s.savePriceList))
	mux.Handle("PATCH "+v+"/companies/{cid}/price-lists/{id}", s.auth(s.savePriceList))
	mux.Handle("GET "+v+"/companies/{cid}/price-lists/{id}/items", s.auth(s.getPriceListItems))
	mux.Handle("PUT "+v+"/companies/{cid}/price-lists/{id}/items", s.auth(s.putPriceListItems))
	mux.Handle("PUT "+v+"/companies/{cid}/customers/{id}/price-list", s.auth(s.putCustomerPriceList))
	mux.Handle("POST "+v+"/companies/{cid}/stock/transfers/{id}/void", s.auth(s.approvable("void", s.voidStockTransfer)))
	mux.Handle("GET "+v+"/companies/{cid}/reports/stock-moves", s.auth(s.stockMovesReport))
	mux.Handle("POST "+v+"/export/xlsx", s.auth(s.exportXLSX))

	// reports
	mux.Handle("GET "+v+"/companies/{cid}/reports/trial-balance", s.auth(s.trialBalance))
	mux.Handle("GET "+v+"/companies/{cid}/reports/ledger/{account}", s.auth(s.accountLedger))
	mux.Handle("GET "+v+"/companies/{cid}/reports/profit-loss", s.auth(s.profitLoss))
	mux.Handle("GET "+v+"/companies/{cid}/reports/balance-sheet", s.auth(s.balanceSheet))
	mux.Handle("GET "+v+"/companies/{cid}/reports/aging", s.auth(s.agingReport))
	mux.Handle("GET "+v+"/companies/{cid}/reports/collections", s.auth(s.collectionsReport))
	mux.Handle("GET "+v+"/companies/{cid}/reports/vat", s.auth(s.vatReport))
	mux.Handle("GET "+v+"/companies/{cid}/reports/generators", s.auth(s.generatorReport))

	// network (generator / ISP), operations log, tariffs, taxes
	mux.Handle("GET "+v+"/companies/{cid}/stations", s.auth(s.listStations))
	mux.Handle("POST "+v+"/companies/{cid}/stations", s.auth(s.saveStation))
	mux.Handle("PATCH "+v+"/companies/{cid}/stations/{id}", s.auth(s.saveStation))
	mux.Handle("GET "+v+"/companies/{cid}/generators", s.auth(s.listGenerators))
	mux.Handle("POST "+v+"/companies/{cid}/generators", s.auth(s.saveGenerator))
	mux.Handle("PATCH "+v+"/companies/{cid}/generators/{id}", s.auth(s.saveGenerator))
	mux.Handle("GET "+v+"/companies/{cid}/generators/{id}/log", s.auth(s.listGeneratorLog))
	mux.Handle("POST "+v+"/companies/{cid}/generators/{id}/hours", s.auth(s.saveGeneratorHours))
	mux.Handle("POST "+v+"/companies/{cid}/generators/{id}/outages", s.auth(s.saveOutage))
	mux.Handle("PATCH "+v+"/companies/{cid}/generators/{id}/outages/{oid}", s.auth(s.saveOutage))
	mux.Handle("GET "+v+"/companies/{cid}/boxes", s.field(s.listBoxes))
	mux.Handle("POST "+v+"/companies/{cid}/boxes", s.auth(s.saveBox))
	mux.Handle("PATCH "+v+"/companies/{cid}/boxes/{id}", s.auth(s.saveBox))
	// ISP: MikroTik routers, auto-suspend / reconnect
	mux.Handle("GET "+v+"/companies/{cid}/routers", s.auth(s.listRouters))
	mux.Handle("POST "+v+"/companies/{cid}/routers", s.auth(sessionOnly(s.saveRouter)))
	mux.Handle("PATCH "+v+"/companies/{cid}/routers/{id}", s.auth(sessionOnly(s.saveRouter)))
	mux.Handle("POST "+v+"/companies/{cid}/routers/{id}/test", s.auth(s.testRouter))
	mux.Handle("GET "+v+"/companies/{cid}/isp/settings", s.auth(s.getISPSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/isp/settings", s.auth(s.putISPSettings))
	mux.Handle("POST "+v+"/companies/{cid}/isp/run", s.auth(s.runISPNow))
	mux.Handle("GET "+v+"/companies/{cid}/router-actions", s.auth(s.listRouterActions))
	mux.Handle("POST "+v+"/companies/{cid}/router-actions/{id}/retry", s.auth(s.retryRouterAction))
	mux.Handle("POST "+v+"/companies/{cid}/subscriptions/{id}/network", s.auth(s.subscriptionNetwork))

	mux.Handle("GET "+v+"/companies/{cid}/tariffs", s.auth(s.listTariffs))
	mux.Handle("POST "+v+"/companies/{cid}/tariffs", s.auth(s.saveTariff))
	mux.Handle("POST "+v+"/companies/{cid}/tariffs/{id}/apply", s.auth(s.applyTariff))
	mux.Handle("GET "+v+"/companies/{cid}/tax-rates", s.auth(s.listTaxRates))
	mux.Handle("POST "+v+"/companies/{cid}/tax-rates", s.auth(s.saveTaxRate))
	mux.Handle("PATCH "+v+"/companies/{cid}/tax-rates/{id}", s.auth(s.saveTaxRate))

	// cash boxes: balances and transfers (collector handover)
	mux.Handle("GET "+v+"/companies/{cid}/cash/balances", s.field(s.cashBalances))
	mux.Handle("GET "+v+"/companies/{cid}/cash/transfers", s.auth(s.listTransfers))
	mux.Handle("POST "+v+"/companies/{cid}/cash/transfers", s.auth(s.createTransfer))

	// bulk import (customers, subscriptions, opening balances, readings, accounts)
	mux.Handle("POST "+v+"/companies/{cid}/import/{type}", s.auth(s.importData))
	// migration from the legacy Magic system (zip made by tools/magic-export)
	mux.Handle("GET "+v+"/magic/imports", s.auth(sessionOnly(s.listMagicImports)))
	mux.Handle("POST "+v+"/magic/imports", s.auth(sessionOnly(s.createMagicImport)))
	mux.Handle("GET "+v+"/magic/imports/{id}", s.auth(sessionOnly(s.getMagicImport)))
	mux.Handle("POST "+v+"/magic/imports/{id}/chunk", s.auth(sessionOnly(s.magicChunk)))
	mux.Handle("POST "+v+"/magic/imports/{id}/analyze", s.auth(sessionOnly(s.analyzeMagicImport)))
	mux.Handle("POST "+v+"/magic/imports/{id}/commit", s.auth(sessionOnly(s.commitMagicImport)))

	// messaging (WhatsApp / SMS)
	mux.Handle("GET "+v+"/companies/{cid}/message-providers", s.auth(s.listProviders))
	mux.Handle("POST "+v+"/companies/{cid}/message-providers", s.auth(s.saveProvider))
	mux.Handle("PATCH "+v+"/companies/{cid}/message-providers/{id}", s.auth(s.saveProvider))
	mux.Handle("GET "+v+"/companies/{cid}/message-templates", s.auth(s.listTemplates))
	mux.Handle("POST "+v+"/companies/{cid}/message-templates", s.auth(s.saveTemplate))
	mux.Handle("PATCH "+v+"/companies/{cid}/message-templates/{id}", s.auth(s.saveTemplate))
	mux.Handle("GET "+v+"/companies/{cid}/messages/settings", s.auth(s.getMessageSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/messages/settings", s.auth(s.putMessageSettings))
	mux.Handle("POST "+v+"/companies/{cid}/messages/send", s.auth(s.sendMessages))
	mux.Handle("POST "+v+"/companies/{cid}/messages/link", s.auth(s.messageLink))
	mux.Handle("GET "+v+"/companies/{cid}/messages", s.auth(s.listMessages))
	mux.Handle("POST "+v+"/companies/{cid}/messages/{id}/retry", s.auth(s.retryMessage))

	// sales: quotations -> orders -> deliveries / invoices, statements, documents
	mux.Handle("GET "+v+"/companies/{cid}/sales/orders", s.auth(s.listOrders))
	mux.Handle("POST "+v+"/companies/{cid}/sales/orders", s.auth(s.createOrder))
	mux.Handle("GET "+v+"/companies/{cid}/sales/orders/{id}", s.auth(s.getOrder))
	mux.Handle("PUT "+v+"/companies/{cid}/sales/orders/{id}", s.auth(s.updateOrder))
	mux.Handle("POST "+v+"/companies/{cid}/sales/orders/{id}/deliver", s.auth(s.creditGate(s.deliverOrder)))
	mux.Handle("POST "+v+"/companies/{cid}/sales/orders/{id}/invoice", s.auth(s.creditGate(s.invoiceOrder)))
	mux.Handle("POST "+v+"/companies/{cid}/sales/orders/{id}/{action}", s.auth(s.creditGate(s.orderAction)))
	mux.Handle("POST "+v+"/companies/{cid}/sales/deliveries/{id}/void", s.auth(s.approvable("void", s.voidDelivery)))
	mux.Handle("GET "+v+"/companies/{cid}/reports/sales", s.auth(s.salesReport))
	mux.Handle("GET "+v+"/companies/{cid}/customers/{id}/statement", s.auth(s.getCustomerStatement))
	mux.Handle("GET "+v+"/companies/{cid}/statements", s.auth(s.listCustomerStatements))
	mux.Handle("POST "+v+"/companies/{cid}/statements/send", s.auth(s.sendStatements))
	mux.Handle("POST "+v+"/companies/{cid}/send-document", s.auth(s.sendDocument))
	mux.Handle("GET "+v+"/companies/{cid}/documents/settings", s.field(s.getDocSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/documents/settings", s.auth(s.putDocSettings))
	// CRM
	mux.Handle("GET "+v+"/companies/{cid}/crm/stages", s.auth(s.listStages))
	mux.Handle("POST "+v+"/companies/{cid}/crm/stages", s.auth(s.saveStage))
	mux.Handle("PATCH "+v+"/companies/{cid}/crm/stages/{id}", s.auth(s.saveStage))
	mux.Handle("GET "+v+"/companies/{cid}/crm/summary", s.auth(s.crmSummary))
	mux.Handle("GET "+v+"/companies/{cid}/crm/users", s.auth(s.crmUsers))
	mux.Handle("GET "+v+"/companies/{cid}/crm/leads", s.auth(s.listLeads))
	mux.Handle("POST "+v+"/companies/{cid}/crm/leads", s.auth(s.saveLead))
	mux.Handle("GET "+v+"/companies/{cid}/crm/leads/{id}", s.auth(s.getLead))
	mux.Handle("PATCH "+v+"/companies/{cid}/crm/leads/{id}", s.auth(s.saveLead))
	mux.Handle("POST "+v+"/companies/{cid}/crm/leads/{id}/convert", s.auth(s.convertLead))
	mux.Handle("GET "+v+"/companies/{cid}/crm/activities", s.auth(s.listActivities))
	mux.Handle("POST "+v+"/companies/{cid}/crm/activities", s.auth(s.saveActivity))
	mux.Handle("PATCH "+v+"/companies/{cid}/crm/activities/{id}", s.auth(s.saveActivity))
	mux.Handle("DELETE "+v+"/companies/{cid}/crm/activities/{id}", s.auth(s.deleteActivity))

	// point of sale (cashiers see only this)
	mux.Handle("GET "+v+"/companies/{cid}/pos/registers", s.field(s.listRegisters))
	mux.Handle("POST "+v+"/companies/{cid}/pos/registers", s.auth(s.saveRegister))
	mux.Handle("PATCH "+v+"/companies/{cid}/pos/registers/{id}", s.auth(s.saveRegister))
	mux.Handle("GET "+v+"/companies/{cid}/pos/categories", s.field(s.listCategories))
	mux.Handle("POST "+v+"/companies/{cid}/pos/categories", s.auth(s.saveCategory))
	mux.Handle("PATCH "+v+"/companies/{cid}/pos/categories/{id}", s.auth(s.saveCategory))
	mux.Handle("GET "+v+"/companies/{cid}/pos/catalog", s.field(s.posCatalog))
	mux.Handle("GET "+v+"/companies/{cid}/pos/price-list", s.field(s.posCustomerPrices))
	mux.Handle("GET "+v+"/companies/{cid}/pos/sessions", s.field(s.listSessions))
	mux.Handle("POST "+v+"/companies/{cid}/pos/sessions", s.field(s.openSession))
	mux.Handle("GET "+v+"/companies/{cid}/pos/sessions/{id}", s.field(s.getSession))
	mux.Handle("POST "+v+"/companies/{cid}/pos/sessions/{id}/close", s.field(s.closeSession))
	mux.Handle("GET "+v+"/companies/{cid}/pos/orders", s.field(s.listPosOrders))
	mux.Handle("POST "+v+"/companies/{cid}/pos/orders", s.field(s.creditGate(s.createPosOrder)))
	mux.Handle("GET "+v+"/companies/{cid}/pos/orders/{id}", s.field(s.getPosOrder))
	mux.Handle("POST "+v+"/companies/{cid}/pos/orders/{id}/refund", s.field(s.approvable("refund", s.refundPosOrder)))
	mux.Handle("POST "+v+"/companies/{cid}/pos/customers", s.field(s.posQuickCustomer))
	mux.Handle("GET "+v+"/companies/{cid}/reports/pos", s.auth(s.posReport))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, http.StatusNotFound, "not_found") })
	// purchasing & stock
	mux.Handle("GET "+v+"/companies/{cid}/purchasing/settings", s.auth(s.getPurchSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/purchasing/settings", s.auth(s.putPurchSettings))
	mux.Handle("GET "+v+"/companies/{cid}/suppliers", s.auth(s.listSuppliers))
	mux.Handle("POST "+v+"/companies/{cid}/suppliers", s.auth(s.saveSupplier))
	mux.Handle("GET "+v+"/companies/{cid}/suppliers/{id}", s.auth(s.getSupplier))
	mux.Handle("PATCH "+v+"/companies/{cid}/suppliers/{id}", s.auth(s.saveSupplier))
	mux.Handle("GET "+v+"/companies/{cid}/items", s.auth(s.listItems))
	mux.Handle("POST "+v+"/companies/{cid}/items", s.auth(s.saveItem))
	mux.Handle("GET "+v+"/companies/{cid}/items/{id}", s.auth(s.getItem))
	mux.Handle("PATCH "+v+"/companies/{cid}/items/{id}", s.auth(s.saveItem))
	mux.Handle("GET "+v+"/companies/{cid}/warehouses", s.auth(s.listWarehouses))
	mux.Handle("POST "+v+"/companies/{cid}/warehouses", s.auth(s.createWarehouse))
	mux.Handle("GET "+v+"/companies/{cid}/bills", s.auth(s.listBills))
	mux.Handle("POST "+v+"/companies/{cid}/bills", s.auth(s.createBill))
	mux.Handle("GET "+v+"/companies/{cid}/bills/{id}", s.auth(s.getBill))
	mux.Handle("POST "+v+"/companies/{cid}/bills/{id}/void", s.auth(s.approvable("void", s.voidBill)))
	mux.Handle("GET "+v+"/companies/{cid}/supplier-payments", s.auth(s.listSupplierPayments))
	mux.Handle("POST "+v+"/companies/{cid}/supplier-payments", s.auth(s.approvable("payment", s.createSupplierPayment)))
	mux.Handle("POST "+v+"/companies/{cid}/supplier-payments/{id}/void", s.auth(s.approvable("void", s.voidSupplierPayment)))
	mux.Handle("GET "+v+"/companies/{cid}/expenses", s.auth(s.listExpenses))
	mux.Handle("POST "+v+"/companies/{cid}/expenses", s.auth(s.createExpense))
	mux.Handle("POST "+v+"/companies/{cid}/expenses/{id}/void", s.auth(s.approvable("void", s.voidExpense)))
	mux.Handle("GET "+v+"/companies/{cid}/stock/docs", s.auth(s.listStockDocs))
	mux.Handle("POST "+v+"/companies/{cid}/stock/{kind}", s.auth(s.createStockDoc))
	mux.Handle("GET "+v+"/companies/{cid}/reports/ap-aging", s.auth(s.apAging))
	mux.Handle("GET "+v+"/companies/{cid}/reports/stock-valuation", s.auth(s.stockValuation))
	mux.Handle("GET "+v+"/companies/{cid}/reports/expenses", s.auth(s.expensesReport))

	// back office: settings, HR & payroll (salaries: admin/accountant only), assets, recurring, bank reconciliation
	mux.Handle("GET "+v+"/companies/{cid}/backoffice/settings", s.auth(s.getBOSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/backoffice/settings", s.auth(s.putBOSettings))
	mux.Handle("GET "+v+"/companies/{cid}/employees", s.auth(s.listEmployees))
	mux.Handle("POST "+v+"/companies/{cid}/employees", s.auth(s.saveEmployee))
	mux.Handle("GET "+v+"/companies/{cid}/employees/{id}", s.auth(s.getEmployee))
	mux.Handle("PATCH "+v+"/companies/{cid}/employees/{id}", s.auth(s.saveEmployee))
	mux.Handle("POST "+v+"/companies/{cid}/employees/{id}/advances", s.auth(s.createAdvance))
	mux.Handle("GET "+v+"/companies/{cid}/payroll", s.auth(s.listPayrollRuns))
	mux.Handle("POST "+v+"/companies/{cid}/payroll", s.auth(s.createPayrollRun))
	mux.Handle("GET "+v+"/companies/{cid}/payroll/{id}", s.auth(s.getPayrollRun))
	mux.Handle("DELETE "+v+"/companies/{cid}/payroll/{id}", s.auth(s.deletePayrollRun))
	mux.Handle("PATCH "+v+"/companies/{cid}/payroll/{id}/payslips/{pid}", s.auth(s.updatePayslip))
	mux.Handle("POST "+v+"/companies/{cid}/payroll/{id}/post", s.auth(s.postPayrollRun))
	mux.Handle("POST "+v+"/companies/{cid}/payroll/{id}/pay", s.auth(s.payPayrollRun))
	mux.Handle("POST "+v+"/companies/{cid}/payroll/{id}/void", s.auth(s.approvable("void", s.voidPayrollRun)))
	// module U: HR (employee file, documents, schedules, attendance, leave, rules, reports) and self-service
	mux.Handle("POST "+v+"/companies/{cid}/payroll/{id}/recalculate", s.auth(s.recalcPayrollRun))
	mux.Handle("POST "+v+"/companies/{cid}/payroll/{id}/approve", s.auth(s.approvePayrollRun))
	mux.Handle("POST "+v+"/companies/{cid}/payroll/{id}/unapprove", s.auth(s.approvePayrollRun))
	mux.Handle("GET "+v+"/companies/{cid}/payroll/{id}/payslips/{pid}", s.auth(s.getPayslip))
	mux.Handle("GET "+v+"/companies/{cid}/payroll/{id}/wps", s.auth(s.payrollWPS))
	mux.Handle("GET "+v+"/companies/{cid}/employees/{id}/file", s.auth(s.getEmployeeFile))
	mux.Handle("PUT "+v+"/companies/{cid}/employees/{id}/file", s.auth(s.putEmployeeFile))
	mux.Handle("GET "+v+"/companies/{cid}/employees/{id}/end-of-service", s.auth(s.endOfService))
	mux.Handle("POST "+v+"/companies/{cid}/employees/{id}/login", s.auth(s.createEmployeeLogin))
	// Odoo audit stage: purchase orders, dunning, FX revaluation, deferrals, attachments
	mux.Handle("GET "+v+"/companies/{cid}/purchase-orders", s.auth(s.listPOs))
	mux.Handle("POST "+v+"/companies/{cid}/purchase-orders", s.auth(s.createPO))
	mux.Handle("POST "+v+"/companies/{cid}/purchase-orders/from-reorder", s.auth(s.poFromReorder))
	mux.Handle("GET "+v+"/companies/{cid}/purchase-orders/{id}", s.auth(s.getPO))
	mux.Handle("PUT "+v+"/companies/{cid}/purchase-orders/{id}", s.auth(s.updatePO))
	mux.Handle("POST "+v+"/companies/{cid}/purchase-orders/{id}/bill", s.auth(s.billPO))
	mux.Handle("POST "+v+"/companies/{cid}/purchase-orders/{id}/{action}", s.auth(s.poAction))
	mux.Handle("GET "+v+"/companies/{cid}/dunning", s.auth(s.getDunning))
	mux.Handle("PUT "+v+"/companies/{cid}/dunning", s.auth(s.putDunning))
	mux.Handle("GET "+v+"/companies/{cid}/dunning/preview", s.auth(s.previewDunning))
	mux.Handle("POST "+v+"/companies/{cid}/dunning/run", s.auth(s.runDunningNow))
	mux.Handle("GET "+v+"/companies/{cid}/dunning/events", s.auth(s.listDunningEvents))
	mux.Handle("GET "+v+"/companies/{cid}/fx-revaluation", s.auth(s.listRevaluations))
	mux.Handle("GET "+v+"/companies/{cid}/fx-revaluation/preview", s.auth(s.previewRevaluation))
	mux.Handle("POST "+v+"/companies/{cid}/fx-revaluation", s.auth(s.runRevaluation))
	mux.Handle("GET "+v+"/companies/{cid}/deferrals", s.auth(s.listDeferrals))
	mux.Handle("POST "+v+"/companies/{cid}/deferrals", s.auth(s.createDeferral))
	mux.Handle("POST "+v+"/companies/{cid}/deferrals/run", s.auth(s.runDeferrals))
	mux.Handle("GET "+v+"/companies/{cid}/deferrals/{id}", s.auth(s.getDeferral))
	mux.Handle("POST "+v+"/companies/{cid}/deferrals/{id}/cancel", s.auth(s.cancelDeferral))
	mux.Handle("GET "+v+"/companies/{cid}/payment-terms", s.auth(s.getPaymentTerms))
	mux.Handle("PUT "+v+"/companies/{cid}/payment-terms", s.auth(s.putPaymentTerms))
	mux.Handle("GET "+v+"/companies/{cid}/attachments", s.auth(s.listAttachments))
	mux.Handle("POST "+v+"/companies/{cid}/attachments", s.auth(s.addAttachment))
	mux.Handle("GET "+v+"/companies/{cid}/attachments/{id}/file", s.auth(s.getAttachment))
	mux.Handle("DELETE "+v+"/companies/{cid}/attachments/{id}", s.auth(s.deleteAttachment))

	// finishing stage: proration, goods receipts / 3-way match / supplier prices, expense claims, custom fields,
	// manufacturing, HR (rotations, roster, accruals, clock rules, devices), online payments, e-invoicing
	mux.Handle("GET "+v+"/companies/{cid}/billing/proration", s.auth(s.getProration))
	mux.Handle("PUT "+v+"/companies/{cid}/billing/proration", s.auth(s.putProration))
	mux.Handle("POST "+v+"/companies/{cid}/subscriptions/{id}/change-plan", s.auth(s.changeSubscriptionPlan))
	mux.Handle("POST "+v+"/companies/{cid}/subscriptions/{id}/stop", s.auth(s.stopSubscription))
	mux.Handle("GET "+v+"/companies/{cid}/subscriptions/{id}/changes", s.auth(s.listSubscriptionChanges))
	mux.Handle("POST "+v+"/companies/{cid}/purchase-orders/{id}/receive", s.auth(s.receivePO))
	mux.Handle("GET "+v+"/companies/{cid}/purchase-orders/{id}/match", s.auth(s.matchPO))
	mux.Handle("POST "+v+"/companies/{cid}/goods-receipts/{id}/cancel", s.auth(s.cancelReceipt))
	mux.Handle("GET "+v+"/companies/{cid}/purchasing/matching", s.auth(s.getMatching))
	mux.Handle("PUT "+v+"/companies/{cid}/purchasing/matching", s.auth(s.putMatching))
	mux.Handle("GET "+v+"/companies/{cid}/supplier-prices", s.auth(s.listSupplierPrices))
	mux.Handle("POST "+v+"/companies/{cid}/supplier-prices", s.auth(s.saveSupplierPrice))
	mux.Handle("GET "+v+"/companies/{cid}/supplier-prices/best", s.auth(s.bestSupplierPrice))
	mux.Handle("PUT "+v+"/companies/{cid}/supplier-prices/{id}", s.auth(s.saveSupplierPrice))
	mux.Handle("DELETE "+v+"/companies/{cid}/supplier-prices/{id}", s.auth(s.deleteSupplierPrice))
	mux.Handle("GET "+v+"/companies/{cid}/self/claims", s.auth(s.selfClaims))
	mux.Handle("POST "+v+"/companies/{cid}/self/claims", s.auth(s.selfClaim))
	mux.Handle("POST "+v+"/companies/{cid}/self/claims/{id}/cancel", s.auth(s.cancelClaim))
	mux.Handle("GET "+v+"/companies/{cid}/hr/claims", s.auth(s.listClaims))
	mux.Handle("POST "+v+"/companies/{cid}/hr/claims", s.auth(s.createClaim))
	mux.Handle("POST "+v+"/companies/{cid}/hr/claims/{id}/decide", s.auth(s.decideClaim))
	mux.Handle("POST "+v+"/companies/{cid}/hr/claims/{id}/pay", s.auth(s.payClaim))
	mux.Handle("POST "+v+"/companies/{cid}/hr/claims/{id}/cancel", s.auth(s.cancelClaim))
	mux.Handle("GET "+v+"/companies/{cid}/hr/rotations", s.auth(s.listRotations))
	mux.Handle("POST "+v+"/companies/{cid}/hr/rotations", s.auth(s.saveRotation))
	mux.Handle("PUT "+v+"/companies/{cid}/hr/rotations/{id}", s.auth(s.saveRotation))
	mux.Handle("POST "+v+"/companies/{cid}/hr/rotations/{id}/assign", s.auth(s.assignRotation))
	mux.Handle("GET "+v+"/companies/{cid}/hr/roster", s.auth(s.roster))
	mux.Handle("PUT "+v+"/companies/{cid}/hr/roster", s.auth(s.putRoster))
	mux.Handle("POST "+v+"/companies/{cid}/hr/leave/accrue", s.auth(s.accrueLeave))
	mux.Handle("POST "+v+"/companies/{cid}/hr/leave/adjust", s.auth(s.adjustLeave))
	mux.Handle("GET "+v+"/companies/{cid}/hr/leave/accruals", s.auth(s.listAccruals))
	mux.Handle("GET "+v+"/companies/{cid}/hr/clock-settings", s.auth(s.getClockSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/hr/clock-settings", s.auth(s.putClockSettings))
	mux.Handle("GET "+v+"/companies/{cid}/hr/attendance/{id}/photo", s.auth(s.attendancePhoto))
	mux.Handle("GET "+v+"/companies/{cid}/hr/devices", s.auth(s.listDevices))
	mux.Handle("POST "+v+"/companies/{cid}/hr/devices", s.auth(s.saveDevice))
	mux.Handle("PATCH "+v+"/companies/{cid}/hr/devices/{id}", s.auth(s.saveDevice))
	mux.Handle("GET "+v+"/companies/{cid}/custom-fields", s.auth(s.listCustomFields))
	mux.Handle("POST "+v+"/companies/{cid}/custom-fields", s.auth(s.saveCustomField))
	mux.Handle("GET "+v+"/companies/{cid}/custom-fields/report", s.auth(s.customReport))
	mux.Handle("PUT "+v+"/companies/{cid}/custom-fields/{id}", s.auth(s.saveCustomField))
	mux.Handle("GET "+v+"/companies/{cid}/custom-values/{entity}/{id}", s.auth(s.getCustomValues))
	mux.Handle("PUT "+v+"/companies/{cid}/custom-values/{entity}/{id}", s.auth(s.putCustomValues))
	mux.Handle("GET "+v+"/companies/{cid}/mfg/boms", s.auth(s.listBOMs))
	mux.Handle("POST "+v+"/companies/{cid}/mfg/boms", s.auth(s.saveBOM))
	mux.Handle("GET "+v+"/companies/{cid}/mfg/boms/{id}", s.auth(s.getBOM))
	mux.Handle("PUT "+v+"/companies/{cid}/mfg/boms/{id}", s.auth(s.saveBOM))
	mux.Handle("GET "+v+"/companies/{cid}/mfg/orders", s.auth(s.listMfgOrders))
	mux.Handle("POST "+v+"/companies/{cid}/mfg/orders", s.auth(s.createMfgOrder))
	mux.Handle("GET "+v+"/companies/{cid}/mfg/orders/{id}", s.auth(s.getMfgOrder))
	mux.Handle("POST "+v+"/companies/{cid}/mfg/orders/{id}/produce", s.auth(s.produceMfgOrder))
	mux.Handle("POST "+v+"/companies/{cid}/mfg/orders/{id}/cancel", s.auth(s.cancelMfgOrder))
	mux.Handle("POST "+v+"/companies/{cid}/mfg/scrap", s.auth(s.scrapStock))
	// retail core
	mux.Handle("GET "+v+"/companies/{cid}/items/templates", s.auth(s.listItemTemplates))
	mux.Handle("PUT "+v+"/companies/{cid}/items/{id}/variant-setup", s.auth(s.putVariantSetup))
	mux.Handle("POST "+v+"/companies/{cid}/items/{id}/variants/generate", s.auth(s.generateVariants))
	mux.Handle("GET "+v+"/companies/{cid}/items/{id}/variants", s.auth(s.getVariants))
	mux.Handle("PUT "+v+"/companies/{cid}/items/{id}/variants", s.auth(s.putVariants))
	mux.Handle("PUT "+v+"/companies/{cid}/items/{id}/retail", s.auth(s.putItemRetail))
	mux.Handle("GET "+v+"/companies/{cid}/retail/promotions", s.auth(s.listPromotions))
	mux.Handle("POST "+v+"/companies/{cid}/retail/promotions", s.auth(s.savePromotion))
	mux.Handle("PUT "+v+"/companies/{cid}/retail/promotions/{id}", s.auth(s.savePromotion))
	mux.Handle("POST "+v+"/companies/{cid}/retail/promotions/evaluate", s.field(s.evaluatePromotions))
	mux.Handle("GET "+v+"/companies/{cid}/retail/loyalty", s.auth(s.getLoyalty))
	mux.Handle("PUT "+v+"/companies/{cid}/retail/loyalty", s.auth(s.putLoyalty))
	mux.Handle("GET "+v+"/companies/{cid}/retail/loyalty/lookup", s.field(s.loyaltyLookup))
	mux.Handle("GET "+v+"/companies/{cid}/retail/loyalty/members", s.auth(s.loyaltyMembers))
	mux.Handle("PUT "+v+"/companies/{cid}/retail/loyalty/customers/{id}", s.field(s.setLoyaltyCard))
	mux.Handle("POST "+v+"/companies/{cid}/retail/loyalty/customers/{id}/adjust", s.auth(s.adjustLoyalty))
	mux.Handle("GET "+v+"/companies/{cid}/retail/loyalty/customers/{id}/moves", s.auth(s.loyaltyMoves))
	mux.Handle("GET "+v+"/companies/{cid}/retail/gift-cards", s.auth(s.listGiftCards))
	mux.Handle("POST "+v+"/companies/{cid}/retail/gift-cards", s.auth(s.issueVoucher))
	mux.Handle("GET "+v+"/companies/{cid}/retail/gift-cards/lookup", s.field(s.giftLookup))
	mux.Handle("POST "+v+"/companies/{cid}/retail/gift-cards/{id}/void", s.auth(s.voidGiftCard))
	mux.Handle("GET "+v+"/companies/{cid}/retail/gift-cards/{id}/moves", s.auth(s.giftMoves))
	mux.Handle("GET "+v+"/companies/{cid}/retail/scale", s.auth(s.getScale))
	mux.Handle("PUT "+v+"/companies/{cid}/retail/scale", s.auth(s.putScale))
	mux.Handle("GET "+v+"/companies/{cid}/retail/scale/plu", s.auth(s.pluExport))
	mux.Handle("GET "+v+"/companies/{cid}/retail/labels", s.auth(s.labelData))
	mux.Handle("GET "+v+"/companies/{cid}/retail/expiry", s.auth(s.expiryReport))
	mux.Handle("POST "+v+"/companies/{cid}/retail/expiry/markdown", s.auth(s.expiryMarkdown))
	mux.Handle("GET "+v+"/companies/{cid}/retail/packs", s.auth(s.listPacks))
	mux.Handle("POST "+v+"/companies/{cid}/retail/packs/{key}", s.auth(s.applyPack))
	mux.Handle("GET "+v+"/companies/{cid}/reports/retail", s.auth(s.retailReport))
	mux.Handle("GET "+v+"/companies/{cid}/pos/held", s.field(s.listHeld))
	mux.Handle("POST "+v+"/companies/{cid}/pos/held", s.field(s.holdBasket))
	mux.Handle("DELETE "+v+"/companies/{cid}/pos/held/{id}", s.field(s.deleteHeld))
	mux.Handle("POST "+v+"/companies/{cid}/pos/quick-item", s.field(s.posQuickItem))
	mux.Handle("GET "+v+"/companies/{cid}/payment-gateways", s.auth(s.listGateways))
	mux.Handle("POST "+v+"/companies/{cid}/payment-gateways", s.auth(s.saveGateway))
	mux.Handle("PUT "+v+"/companies/{cid}/payment-gateways/{id}", s.auth(s.saveGateway))
	mux.Handle("GET "+v+"/companies/{cid}/payment-intents", s.auth(s.listIntents))
	mux.Handle("POST "+v+"/companies/{cid}/payment-intents/{id}/{action}", s.auth(s.decideIntent))
	mux.Handle("GET "+v+"/companies/{cid}/einvoice/config", s.auth(s.getEinvConfig))
	mux.Handle("PUT "+v+"/companies/{cid}/einvoice/config", s.auth(s.putEinvConfig))
	mux.Handle("POST "+v+"/companies/{cid}/einvoice/zatca/onboard", s.auth(s.zatcaOnboard))
	mux.Handle("GET "+v+"/companies/{cid}/invoices/{id}/einvoice", s.auth(s.getInvoiceEinvoice))
	mux.Handle("POST "+v+"/companies/{cid}/invoices/{id}/einvoice", s.auth(s.invoiceEinvoice))
	// public: customer portal payments, payment processor webhooks, test-mode simulator, fingerprint devices (ADMS)
	mux.HandleFunc("GET "+v+"/portal/{token}/pay-options", s.portalPayOptions)
	mux.HandleFunc("POST "+v+"/portal/{token}/invoices/{id}/pay", s.portalPay)
	mux.HandleFunc("POST "+v+"/pay/webhook/{gid}", s.payWebhook)
	mux.HandleFunc("GET "+v+"/pay/sim/{token}", s.paySimInfo)
	mux.HandleFunc("POST "+v+"/pay/sim/{token}", s.paySimComplete)
	// each device pushes either to its secret address /iclock/k/<token>/… or, from its registered IP, to /iclock/…
	for _, pre := range []string{"/iclock", "/iclock/k/{token}"} {
		for _, p := range []string{pre + "/cdata", pre + "/cdata.aspx"} {
			mux.HandleFunc("GET "+p, s.iclockHandshake)
			mux.HandleFunc("POST "+p, s.iclockPush)
		}
		for _, p := range []string{pre + "/getrequest", pre + "/getrequest.aspx", pre + "/devicecmd", pre + "/devicecmd.aspx"} {
			mux.HandleFunc("GET "+p, s.iclockPoll)
			mux.HandleFunc("POST "+p, s.iclockPoll)
		}
	}

	// Odoo gaps: credit limit, fiscal positions, quotation templates, instalments, chatter, activities, analysis, repairs
	mux.Handle("GET "+v+"/companies/{cid}/customers/{id}/credit", s.auth(s.getCustomerCredit))
	mux.Handle("PUT "+v+"/companies/{cid}/customers/{id}/credit", s.auth(s.putCustomerCredit))
	mux.Handle("GET "+v+"/companies/{cid}/credit-settings", s.auth(s.getCreditSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/credit-settings", s.auth(s.putCreditSettings))
	mux.Handle("GET "+v+"/companies/{cid}/credit/report", s.auth(s.creditReport))
	mux.Handle("GET "+v+"/companies/{cid}/fiscal-positions", s.auth(s.listFiscalPositions))
	mux.Handle("POST "+v+"/companies/{cid}/fiscal-positions", s.auth(s.saveFiscalPosition))
	mux.Handle("PUT "+v+"/companies/{cid}/fiscal-positions/{id}", s.auth(s.saveFiscalPosition))
	mux.Handle("PUT "+v+"/companies/{cid}/suppliers/{id}/fiscal-position", s.auth(s.putSupplierFiscalPosition))
	mux.Handle("GET "+v+"/companies/{cid}/quote-templates", s.auth(s.listQuoteTemplates))
	mux.Handle("POST "+v+"/companies/{cid}/quote-templates", s.auth(s.saveQuoteTemplate))
	mux.Handle("PUT "+v+"/companies/{cid}/quote-templates/{id}", s.auth(s.saveQuoteTemplate))
	mux.Handle("GET "+v+"/companies/{cid}/instalments", s.auth(s.listInstalments))
	mux.Handle("POST "+v+"/companies/{cid}/instalments", s.auth(s.creditGate(s.createInstalment)))
	mux.Handle("GET "+v+"/companies/{cid}/instalments/{id}", s.auth(s.getInstalment))
	mux.Handle("POST "+v+"/companies/{cid}/instalments/{id}/{action}", s.auth(s.instalmentAction))
	mux.Handle("GET "+v+"/companies/{cid}/chatter", s.auth(s.getChatter))
	mux.Handle("POST "+v+"/companies/{cid}/chatter", s.auth(s.postChatter))
	mux.Handle("DELETE "+v+"/companies/{cid}/chatter/{id}", s.auth(s.deleteChatter))
	mux.Handle("GET "+v+"/companies/{cid}/mentionable", s.auth(s.mentionable))
	mux.Handle("GET "+v+"/companies/{cid}/notifications", s.field(s.listNotifications))
	mux.Handle("POST "+v+"/companies/{cid}/notifications/read", s.field(s.readNotifications))
	mux.Handle("GET "+v+"/companies/{cid}/record-activities", s.auth(s.listRecordActivities))
	mux.Handle("POST "+v+"/companies/{cid}/record-activities", s.auth(s.createRecordActivity))
	mux.Handle("PATCH "+v+"/companies/{cid}/record-activities/{id}", s.auth(s.updateRecordActivity))
	mux.Handle("POST "+v+"/companies/{cid}/record-activities/{id}/done", s.auth(s.doneRecordActivity))
	mux.Handle("GET "+v+"/companies/{cid}/analysis/sources", s.auth(s.analysisSources))
	mux.Handle("POST "+v+"/companies/{cid}/analysis/run", s.auth(s.runAnalysis))
	mux.Handle("GET "+v+"/companies/{cid}/analysis/reports", s.auth(s.listAnalysisReports))
	mux.Handle("POST "+v+"/companies/{cid}/analysis/reports", s.auth(s.saveAnalysisReport))
	mux.Handle("PUT "+v+"/companies/{cid}/analysis/reports/{id}", s.auth(s.saveAnalysisReport))
	mux.Handle("GET "+v+"/companies/{cid}/repairs", s.auth(s.listRepairs))
	mux.Handle("POST "+v+"/companies/{cid}/repairs", s.auth(s.createRepair))
	mux.Handle("GET "+v+"/companies/{cid}/repairs/warranty", s.auth(s.repairWarranty))
	mux.Handle("GET "+v+"/companies/{cid}/repairs/{id}", s.auth(s.getRepair))
	mux.Handle("PATCH "+v+"/companies/{cid}/repairs/{id}", s.auth(s.updateRepair))
	mux.Handle("PUT "+v+"/companies/{cid}/repairs/{id}/lines", s.auth(s.putRepairLines))
	mux.Handle("POST "+v+"/companies/{cid}/repairs/{id}/invoice", s.auth(s.creditGate(s.invoiceRepair)))

	// service desk and generator maintenance (app "svc")
	mux.Handle("GET "+v+"/companies/{cid}/tickets", s.auth(s.listTickets))
	mux.Handle("POST "+v+"/companies/{cid}/tickets", s.auth(s.createTicket))
	mux.Handle("GET "+v+"/companies/{cid}/tickets/summary", s.auth(s.ticketSummary))
	mux.Handle("GET "+v+"/companies/{cid}/tickets/{id}", s.auth(s.getTicket))
	mux.Handle("PATCH "+v+"/companies/{cid}/tickets/{id}", s.auth(s.updateTicket))
	mux.Handle("POST "+v+"/companies/{cid}/tickets/{id}/notes", s.auth(s.addTicketNote))
	mux.Handle("GET "+v+"/companies/{cid}/maintenance", s.auth(s.listMaintenance))
	mux.Handle("POST "+v+"/companies/{cid}/maintenance/tasks", s.auth(s.saveMaintenancePlan))
	mux.Handle("PATCH "+v+"/companies/{cid}/maintenance/tasks/{id}", s.auth(s.saveMaintenancePlan))
	mux.Handle("GET "+v+"/companies/{cid}/maintenance/log", s.auth(s.listMaintenanceLog))
	mux.Handle("POST "+v+"/companies/{cid}/maintenance/log", s.auth(s.logMaintenance))
	mux.Handle("POST "+v+"/companies/{cid}/employees/{id}/documents", s.auth(s.addEmployeeDocument))
	mux.Handle("GET "+v+"/companies/{cid}/employees/{id}/documents/{did}", s.auth(s.getEmployeeDocument))
	mux.Handle("DELETE "+v+"/companies/{cid}/employees/{id}/documents/{did}", s.auth(s.deleteEmployeeDocument))
	mux.Handle("GET "+v+"/companies/{cid}/hr/overview", s.auth(s.hrOverview))
	mux.Handle("GET "+v+"/companies/{cid}/hr/schedules", s.auth(s.listSchedules))
	mux.Handle("POST "+v+"/companies/{cid}/hr/schedules", s.auth(s.saveSchedule))
	mux.Handle("PUT "+v+"/companies/{cid}/hr/schedules/{id}", s.auth(s.saveSchedule))
	mux.Handle("GET "+v+"/companies/{cid}/hr/holidays", s.auth(s.listHolidays))
	mux.Handle("POST "+v+"/companies/{cid}/hr/holidays", s.auth(s.saveHoliday))
	mux.Handle("DELETE "+v+"/companies/{cid}/hr/holidays/{id}", s.auth(s.deleteHoliday))
	mux.Handle("GET "+v+"/companies/{cid}/hr/attendance", s.auth(s.listAttendance))
	mux.Handle("PUT "+v+"/companies/{cid}/hr/attendance", s.auth(s.putAttendance))
	mux.Handle("POST "+v+"/companies/{cid}/hr/attendance/import", s.auth(s.importAttendance))
	mux.Handle("GET "+v+"/companies/{cid}/hr/leave-types", s.auth(s.listLeaveTypes))
	mux.Handle("POST "+v+"/companies/{cid}/hr/leave-types", s.auth(s.saveLeaveType))
	mux.Handle("PUT "+v+"/companies/{cid}/hr/leave-types/{id}", s.auth(s.saveLeaveType))
	mux.Handle("GET "+v+"/companies/{cid}/hr/leave", s.auth(s.listLeaveRequests))
	mux.Handle("POST "+v+"/companies/{cid}/hr/leave", s.auth(s.createLeaveRequest))
	mux.Handle("GET "+v+"/companies/{cid}/hr/leave/balances", s.auth(s.leaveBalanceReport))
	mux.Handle("POST "+v+"/companies/{cid}/hr/leave/{id}/decide", s.field(s.decideLeave))
	mux.Handle("POST "+v+"/companies/{cid}/hr/leave/{id}/cancel", s.auth(s.cancelLeave))
	mux.Handle("GET "+v+"/companies/{cid}/hr/payroll-params", s.auth(s.getPayrollParams))
	mux.Handle("POST "+v+"/companies/{cid}/hr/payroll-params", s.auth(s.putPayrollParam))
	mux.Handle("GET "+v+"/companies/{cid}/reports/hr/{kind}", s.auth(s.hrReport))
	mux.Handle("GET "+v+"/companies/{cid}/self", s.field(s.selfHome))
	mux.Handle("POST "+v+"/companies/{cid}/self/clock", s.field(s.selfClock))
	mux.Handle("POST "+v+"/companies/{cid}/self/leave", s.field(s.selfLeave))
	mux.Handle("POST "+v+"/companies/{cid}/self/leave/{id}/cancel", s.field(s.selfCancelLeave))
	mux.Handle("GET "+v+"/companies/{cid}/self/payslips/{pid}", s.field(s.selfPayslip))
	mux.Handle("GET "+v+"/companies/{cid}/self/team-leave", s.field(s.selfTeamLeave))
	mux.Handle("GET "+v+"/companies/{cid}/assets", s.auth(s.listAssets))
	mux.Handle("POST "+v+"/companies/{cid}/assets", s.auth(s.createAsset))
	mux.Handle("GET "+v+"/companies/{cid}/assets/{id}", s.auth(s.getAsset))
	mux.Handle("PATCH "+v+"/companies/{cid}/assets/{id}", s.auth(s.updateAsset))
	mux.Handle("POST "+v+"/companies/{cid}/assets/{id}/dispose", s.auth(s.approvable("void", s.disposeAsset)))
	mux.Handle("POST "+v+"/companies/{cid}/depreciation/run", s.auth(s.runDepreciation))
	mux.Handle("GET "+v+"/companies/{cid}/reports/assets", s.auth(s.assetRegister))
	mux.Handle("GET "+v+"/companies/{cid}/recurring", s.auth(s.listRecurring))
	mux.Handle("POST "+v+"/companies/{cid}/recurring", s.auth(s.saveRecurring))
	mux.Handle("PATCH "+v+"/companies/{cid}/recurring/{id}", s.auth(s.saveRecurring))
	mux.Handle("POST "+v+"/companies/{cid}/recurring/{id}/run", s.auth(s.runRecurringNow))
	mux.Handle("GET "+v+"/companies/{cid}/bank/statements", s.auth(s.listStatements))
	mux.Handle("POST "+v+"/companies/{cid}/bank/statements", s.auth(s.importStatement))
	mux.Handle("GET "+v+"/companies/{cid}/bank/statements/{id}", s.auth(s.getStatement))
	mux.Handle("POST "+v+"/companies/{cid}/bank/statements/{id}/auto-match", s.auth(s.autoMatchStatement))
	mux.Handle("POST "+v+"/companies/{cid}/bank/lines/{id}/match", s.auth(s.matchBankLine))
	mux.Handle("POST "+v+"/companies/{cid}/bank/lines/{id}/entry", s.auth(s.createBankLineEntry))
	mux.Handle("POST "+v+"/companies/{cid}/bank/lines/{id}/{action}", s.auth(s.setBankLineStatus))
	mux.Handle("GET "+v+"/companies/{cid}/bank/reconciliation", s.auth(s.bankReconciliation))

	// professional accounting (module S)
	mux.Handle("GET "+v+"/companies/{cid}/accounting/settings", s.auth(s.getAcctSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/accounting/settings", s.auth(s.putAcctSettings))
	mux.Handle("GET "+v+"/companies/{cid}/cost-centers", s.auth(s.listCostCenters))
	mux.Handle("POST "+v+"/companies/{cid}/cost-centers", s.auth(s.saveCostCenter))
	mux.Handle("PATCH "+v+"/companies/{cid}/cost-centers/{id}", s.auth(s.saveCostCenter))
	mux.Handle("PATCH "+v+"/companies/{cid}/journal-lines/{id}", s.auth(s.tagJournalLine))
	mux.Handle("GET "+v+"/companies/{cid}/reports/cost-centers", s.auth(s.costCenterReport))
	mux.Handle("GET "+v+"/companies/{cid}/budgets", s.auth(s.listBudget))
	mux.Handle("PUT "+v+"/companies/{cid}/budgets", s.auth(s.putBudget))
	mux.Handle("GET "+v+"/companies/{cid}/reports/budget", s.auth(s.budgetReport))
	mux.Handle("GET "+v+"/companies/{cid}/fiscal-years", s.auth(s.listFiscalYears))
	mux.Handle("POST "+v+"/companies/{cid}/fiscal-years/close", s.auth(s.closeFiscalYear))
	mux.Handle("POST "+v+"/companies/{cid}/fiscal-years/reopen", s.auth(s.reopenFiscalYear))
	mux.Handle("POST "+v+"/companies/{cid}/opening-balances", s.auth(s.postOpeningBalances))
	mux.Handle("GET "+v+"/companies/{cid}/exchanges", s.auth(s.listExchanges))
	mux.Handle("POST "+v+"/companies/{cid}/exchanges", s.auth(s.createExchange))
	mux.Handle("POST "+v+"/companies/{cid}/exchanges/{id}/void", s.auth(s.voidExchange))
	mux.Handle("GET "+v+"/companies/{cid}/cheques", s.auth(s.listCheques))
	mux.Handle("POST "+v+"/companies/{cid}/cheques", s.auth(s.createCheque))
	mux.Handle("GET "+v+"/companies/{cid}/cheques/{id}", s.auth(s.getCheque))
	mux.Handle("POST "+v+"/companies/{cid}/cheques/{id}/{op}", s.auth(s.chequeAction))
	mux.Handle("GET "+v+"/companies/{cid}/reports/cash-flow", s.auth(s.cashFlow))
	mux.Handle("GET "+v+"/companies/{cid}/reports/compare", s.auth(s.compareReport))
	mux.Handle("GET "+v+"/companies/{cid}/owner-summary", s.auth(s.ownerSummary))
	mux.Handle("GET "+v+"/companies/{cid}/journal/{id}/source", s.auth(s.entrySource))

	mux.Handle("/", spa(s.WebDir))

	s.protected = mux.patterns
	return securityHeaders(jsonOnly(s.Sessions.LoadAndSave(base)))
}

// routeMux records the patterns registered with Handle (every authenticated route goes through it),
// so a test can check that each one declares a permission.
type routeMux struct {
	*http.ServeMux
	patterns []string
}

func (m *routeMux) Handle(pattern string, h http.Handler) {
	m.patterns = append(m.patterns, pattern)
	m.ServeMux.Handle(pattern, h)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Pool.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ready: the database answers and its schema is at the migration version this binary expects.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	want := db.LatestVersion()
	if err := s.Pool.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "db": false})
		return
	}
	got := want // without an owner pool the version cannot be read; the ping above is all we can check
	if s.OwnerPool != nil {
		if err := s.OwnerPool.QueryRow(ctx, `SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&got); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "db": true, "migration": "unknown"})
			return
		}
	}
	status := http.StatusOK
	if got < want {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"ok": got >= want, "db": true, "migration": got, "expected": want})
}

// ---- principal / auth middleware ----

type Principal struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
	Email      string `json:"email"`
	FullName   string `json:"full_name"`
	Role       string `json:"role"` // built-in base role (admin, accountant, ..., collector, cashier)
	Locale     string `json:"locale"`
	APIKeyID   string `json:"api_key_id,omitempty"`
	Active     bool   `json:"-"`

	Plan            string     `json:"plan"`
	TrialEndsAt     *time.Time `json:"trial_ends_at"`
	IsPlatformAdmin bool       `json:"is_platform_admin"`
	// Paxve subscription of the organisation: trial, active, grace, expired (read-only) or blocked
	SubState  string       `json:"sub_state"`
	PaidUntil *string      `json:"paid_until"`
	GraceEnds *string      `json:"grace_ends"`
	Support   *SupportInfo `json:"support,omitempty"` // a platform owner looking at this organisation (read-only)
	// apps the organisation's plan allows besides the core apps (nil = every app), grandfathered apps included
	PlanApps []string `json:"plan_apps"`

	RoleID           *string  `json:"role_id"`   // custom role, if any
	RoleName         string   `json:"role_name"` // custom role name ("" = built-in)
	Permissions      []string `json:"permissions"`
	CompanyIDs       []string `json:"company_ids"` // nil = all companies
	MFAEnabled       bool     `json:"mfa_enabled"`
	MFARequired      bool     `json:"mfa_required"`       // the organisation requires two-factor sign-in
	MFASetupRequired bool     `json:"mfa_setup_required"` // required but not set up yet: only setup is allowed
	// platform owners without two-step sign-in: after this moment they must set it up (banner before)
	PlatformMFADeadline *time.Time `json:"platform_mfa_deadline,omitempty"`
	SessionID           string     `json:"-"`

	perms map[string]bool
}

type principalKey struct{}

// approvedExecKey marks a request replayed by an approver (see approvals.go). Only server code can set
// context values, so clients cannot forge it.
type approvedExecKey struct{}

// approvedExec: the stored call runs as the person who asked (requester), with all of their permission
// and company checks, and the approver is recorded next to them (audit log, documents).
type approvedExec struct {
	p          *Principal // requester, loaded fresh when the request is approved
	approverID string
	approvalID string
	pattern    string
}

func principal(r *http.Request) *Principal { return r.Context().Value(principalKey{}).(*Principal) }

func scope(r *http.Request) db.Scope {
	p := principal(r)
	sc := db.Scope{TenantID: p.TenantID, UserID: p.ID, APIKeyID: p.APIKeyID, IP: clientIP(r),
		CompanyID: urlCompany(r), CompanyIDs: p.CompanyIDs}
	if ex, ok := r.Context().Value(approvedExecKey{}).(*approvedExec); ok {
		sc.ApprovedBy, sc.ApprovalID = ex.approverID, ex.approvalID
	}
	return sc
}

// urlCompany: the {cid} of the route ("" when the route has none). A malformed id becomes the nil UUID,
// so row-level security shows nothing rather than everything.
func urlCompany(r *http.Request) string {
	cid := r.PathValue("cid")
	if cid == "" {
		return ""
	}
	if !isUUID(cid) {
		return "00000000-0000-0000-0000-000000000000"
	}
	return cid
}

func (s *Server) loadPrincipal(ctx context.Context, sc db.Scope) (*Principal, error) {
	p := &Principal{}
	var custom []string
	var roleActive *bool
	err := db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT u.id::text, u.tenant_id::text, t.name, u.email, u.full_name, u.role, u.locale,
			       (u.is_active AND t.is_active), t.plan, t.trial_ends_at,
			       u.role_id::text, coalesce(ro.name, ''), ro.permissions, ro.is_active,
			       u.company_ids::text[], coalesce(m.enabled_at IS NOT NULL, false), t.require_mfa,
			       tenant_state(t), t.paid_until::text, CASE WHEN t.paid_until IS NULL THEN NULL ELSE (t.paid_until + t.grace_days)::text END,
			       CASE WHEN pp.apps IS NULL THEN NULL ELSE pp.apps || t.grandfathered_apps END, u.platform_mfa_deadline
			FROM users u JOIN tenants t ON t.id = u.tenant_id
			LEFT JOIN platform_plans pp ON pp.code = t.plan
			LEFT JOIN roles ro ON ro.id = u.role_id
			LEFT JOIN user_mfa m ON m.user_id = u.id
			WHERE u.id = $1`, sc.UserID).
			Scan(&p.ID, &p.TenantID, &p.TenantName, &p.Email, &p.FullName, &p.Role, &p.Locale, &p.Active, &p.Plan,
				&p.TrialEndsAt, &p.RoleID, &p.RoleName, &custom, &roleActive, &p.CompanyIDs, &p.MFAEnabled, &p.MFARequired,
				&p.SubState, &p.PaidUntil, &p.GraceEnds, &p.PlanApps, &p.PlatformMFADeadline)
	})
	if err != nil {
		return p, err
	}
	p.IsPlatformAdmin = s.platformAdmins[strings.ToLower(p.Email)]
	if p.IsPlatformAdmin && !p.MFAEnabled {
		// audit C-H1: the platform owner sees every organisation, so two-step sign-in becomes mandatory. A 7-day
		// grace (counted from the first sign-in after this rule) shows a banner; after it only the setup is allowed.
		if p.PlatformMFADeadline == nil {
			_ = db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `UPDATE users SET platform_mfa_deadline = coalesce(platform_mfa_deadline, now() + interval '7 days')
					WHERE id = $1 RETURNING platform_mfa_deadline`, p.ID).Scan(&p.PlatformMFADeadline)
			})
		}
		if p.PlatformMFADeadline != nil && time.Now().After(*p.PlatformMFADeadline) {
			p.MFARequired = true
		}
	}
	if p.RoleID != nil {
		// a custom role: its own list; a deactivated role grants nothing
		if roleActive != nil && *roleActive {
			p.perms = permSet(custom)
		} else {
			p.perms = map[string]bool{}
		}
	} else {
		p.perms = permSet(presets[p.Role])
	}
	p.Permissions = sortedPerms(p.perms)
	p.MFASetupRequired = p.MFARequired && !p.MFAEnabled
	return p, nil
}

// companyAllowed: the user may open this company (users can be limited to some companies).
func companyAllowed(p *Principal, cid string) bool {
	if p.CompanyIDs == nil {
		return true
	}
	for _, c := range p.CompanyIDs {
		if strings.EqualFold(c, cid) {
			return true
		}
	}
	return false
}

// auth: any signed-in office user (collectors and cashiers are refused). field: also collectors and cashiers
// (each handler then decides what they may see). On top of that every route needs its permission (routePerms).
func (s *Server) auth(next http.HandlerFunc) http.Handler  { return s.authenticate(next, false) }
func (s *Server) field(next http.HandlerFunc) http.Handler { return s.authenticate(next, true) }

// routes a user may still call while the organisation requires two-factor sign-in that is not set up yet
var mfaSetupRoutes = map[string]bool{
	"GET /me": true, "PATCH /me": true, "POST /auth/logout": true, "GET /permissions": true,
	"POST /me/mfa/setup": true, "POST /me/mfa/enable": true, "GET /companies": true,
}

// authenticate accepts either a browser session cookie or "Authorization: Bearer pxv_..." API key.
func (s *Server) authenticate(handler http.HandlerFunc, allowCollector bool) http.Handler {
	next := func(w http.ResponseWriter, r *http.Request, p *Principal, approved bool) {
		if !allowCollector && (p.Role == RoleCollector || p.Role == RoleCashier) {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		method, path, _ := strings.Cut(r.Pattern, " ")
		route := method + " " + strings.TrimPrefix(path, v1)
		perm, declared := routePerms[route]
		if !declared {
			slog.Error("route without permission", "pattern", r.Pattern)
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		if p.MFASetupRequired && p.APIKeyID == "" && !mfaSetupRoutes[route] {
			writeErr(w, http.StatusForbidden, "mfa_setup_required")
			return
		}
		readOnly := r.Method == http.MethodGet || r.Method == http.MethodHead
		if p.Support != nil && !readOnly && !supportWriteRoutes[route] {
			writeErr(w, http.StatusForbidden, "support_read_only")
			return
		}
		if p.SubState == "expired" && !p.IsPlatformAdmin && !readOnly && !expiredWriteRoutes[route] {
			writeErr(w, http.StatusPaymentRequired, "subscription_expired")
			return
		}
		if !approved && perm != permAny && !p.perms[perm] {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		if cid := r.PathValue("cid"); cid != "" && !companyAllowed(p, cid) {
			writeErr(w, http.StatusNotFound, "not_found")
			return
		}
		if code := s.appBlocked(r, p); code != "" {
			writeErr(w, http.StatusForbidden, code)
			return
		}
		handler(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if ex, ok := ctx.Value(approvedExecKey{}).(*approvedExec); ok {
			// an approved request replayed by the approver: only the exact route that was approved
			if ex.pattern != r.Pattern {
				writeErr(w, http.StatusForbidden, "forbidden")
				return
			}
			next(w, r, ex.p, false) // the requester must still hold the permission and the company
			return
		}
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			ip := clientIP(r)
			if !s.apiKeyLimiter.allow(ip) {
				writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
				return
			}
			p, ok := s.authAPIKey(ctx, strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), ip)
			if !ok {
				s.apiKeyLimiter.fail(ip)
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next(w, r, p, false)
			return
		}
		uid, tid := s.Sessions.GetString(ctx, "uid"), s.Sessions.GetString(ctx, "tid")
		if uid == "" || tid == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// support view: a platform owner looks at an organisation as its admin, read-only
		support, err := s.checkSupport(ctx, r)
		if errors.Is(err, errSessionRevoked) {
			_ = s.Sessions.Destroy(ctx)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			serverErr(w, r, err)
			return
		}
		uid, tid = s.Sessions.GetString(ctx, "uid"), s.Sessions.GetString(ctx, "tid") // a support view that ran out is ended
		sc := db.Scope{TenantID: tid, UserID: uid, IP: clientIP(r)}
		p, err := s.loadPrincipal(ctx, sc)
		if support != nil && (errors.Is(err, pgx.ErrNoRows) || (err == nil && !p.Active)) {
			s.restoreSupport(ctx) // the organisation or its admin was switched off meanwhile: back to the console
			writeErr(w, http.StatusConflict, "support_ended")
			return
		}
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !p.Active) {
			_ = s.Sessions.Destroy(ctx)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			serverErr(w, r, err)
			return
		}
		if support != nil {
			p.Support = support
			p.IsPlatformAdmin = false
			next(w, r, p, false)
			return
		}
		sid, err := s.touchSession(ctx, sc, s.Sessions.GetString(ctx, "sid"), r)
		if errors.Is(err, errSessionRevoked) {
			_ = s.Sessions.Destroy(ctx)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			serverErr(w, r, err)
			return
		}
		if sid != s.Sessions.GetString(ctx, "sid") {
			s.Sessions.Put(ctx, "sid", sid)
		}
		p.SessionID = sid
		next(w, r, p, false)
	})
}

func isCollector(r *http.Request) bool { return principal(r).Role == RoleCollector }

// isFrontline: collectors and cashiers see only the money they took themselves.
func isFrontline(r *http.Request) bool {
	role := principal(r).Role
	return role == RoleCollector || role == RoleCashier
}

func sessionOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if principal(r).APIKeyID != "" {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r)
	}
}

// ---- middleware ----

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// jsonOnly rejects state-changing API calls that are not JSON. Browsers cannot
// send a cross-site JSON request without a CORS preflight, so this blocks CSRF.
func jsonOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// payment gateways post their callbacks as forms (audit C-M1); those routes need no cookie and verify the
		// gateway's signature themselves, so the JSON-only CSRF guard does not apply to them
		if strings.HasPrefix(r.URL.Path, v1+"/pay/webhook/") {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead &&
			r.Method != http.MethodDelete {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeErr(w, http.StatusUnsupportedMediaType, "bad_request")
				return
			}
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/") &&
			r.Header.Get("Authorization") == "" && r.Header.Get("X-Requested-With") != "paxve" {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the visitor's IP. In production Caddy is the only thing that can reach the
// app; it resolves the real client IP (trusting CF-Connecting-IP only from Cloudflare's ranges)
// and passes it as X-Client-IP, overwriting anything the client sent. Client-supplied
// CF-Connecting-IP / X-Forwarded-For are never trusted here, so rate limits cannot be dodged.
func clientIP(r *http.Request) string {
	ip := strings.TrimSpace(r.Header.Get("X-Client-IP"))
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

// spa serves the built web app, falling back to index.html.
func spa(dir string) http.Handler {
	root := http.Dir(dir)
	files := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if f, err := root.Open(p); err == nil {
			st, _ := f.Stat()
			f.Close()
			if st != nil && !st.IsDir() {
				if strings.HasPrefix(p, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index := filepath.Join(dir, "index.html")
		if _, err := os.Stat(index); err != nil {
			http.Error(w, "web app not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

func serverErr(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeErr(w, http.StatusInternalServerError, "server_error")
}
