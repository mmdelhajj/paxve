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
var supportedLocales = map[string]bool{"en": true, "ar": true, "fr": true}

//go:embed openapi.yaml
var openapiSpec []byte

type Server struct {
	Pool     *pgxpool.Pool
	Sessions *scs.SessionManager
	WebDir   string
	limiter  *limiter

	// OwnerPool connects as the database owner. Used ONLY for self sign-up (creating a new
	// organisation, before any tenant exists) and for the platform owner console.
	OwnerPool      *pgxpool.Pool
	platformAdmins map[string]bool
	signupLimiter  *limiter
	portalLimiter  *limiter

	// AllowPrivateRouters lets routers point to private/loopback addresses (tests, VPN setups).
	// Off by default so tenants cannot use "test connection" to probe the server's internal network.
	AllowPrivateRouters bool
}

func New(pool, ownerPool *pgxpool.Pool, sessions *scs.SessionManager, webDir string, platformAdmins []string) *Server {
	admins := map[string]bool{}
	for _, e := range platformAdmins {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			admins[e] = true
		}
	}
	return &Server{Pool: pool, OwnerPool: ownerPool, Sessions: sessions, WebDir: webDir,
		limiter: newLimiter(10, 15*time.Minute), signupLimiter: newLimiter(5, time.Hour), portalLimiter: newLimiter(120, 10*time.Minute), platformAdmins: admins}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	const v = "/api/v1"
	write := []string{RoleAdmin, RoleAccountant}
	entry := []string{RoleAdmin, RoleAccountant, RoleDataEntry}
	field := []string{RoleAdmin, RoleAccountant, RoleDataEntry, RoleCollector}

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
	mux.Handle("POST "+v+"/companies/{cid}/customers/{id}/portal", s.auth(only(s.customerPortal, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/dashboard", s.auth(s.dashboard))
	mux.Handle("GET "+v+"/companies/{cid}/onboarding", s.auth(s.onboarding))
	mux.Handle("POST "+v+"/demo-data", s.auth(sessionOnly(only(s.loadDemoData, RoleAdmin))))
	mux.Handle("GET "+v+"/companies/{cid}/invoices/print", s.auth(s.printInvoices))
	mux.Handle("GET "+v+"/platform/tenants", s.auth(s.platformOnly(s.platformTenants)))
	mux.Handle("PATCH "+v+"/platform/tenants/{id}", s.auth(s.platformOnly(s.platformUpdateTenant)))

	// session (browser) auth
	mux.HandleFunc("POST "+v+"/auth/login", s.login)
	mux.Handle("POST "+v+"/auth/logout", s.field(s.logout))
	mux.Handle("GET "+v+"/me", s.field(s.me))
	mux.Handle("PATCH "+v+"/me", s.field(sessionOnly(s.updateMe)))

	// reference data
	mux.Handle("GET "+v+"/currencies", s.field(s.listCurrencies))

	// organisation
	mux.Handle("GET "+v+"/companies", s.field(s.listCompanies))
	mux.Handle("POST "+v+"/companies", s.auth(only(s.createCompany, RoleAdmin)))
	mux.Handle("PATCH "+v+"/companies/{cid}", s.auth(only(s.updateCompany, RoleAdmin)))
	mux.Handle("GET "+v+"/users", s.auth(only(s.listUsers, RoleAdmin)))
	mux.Handle("POST "+v+"/users", s.auth(only(s.createUser, RoleAdmin)))
	mux.Handle("PATCH "+v+"/users/{id}", s.auth(only(s.updateUser, RoleAdmin)))
	mux.Handle("GET "+v+"/audit", s.auth(only(s.listAudit, RoleAdmin, RoleAccountant)))

	// developer API keys (managed from the browser only)
	mux.Handle("GET "+v+"/api-keys", s.auth(sessionOnly(only(s.listAPIKeys, RoleAdmin))))
	mux.Handle("POST "+v+"/api-keys", s.auth(sessionOnly(only(s.createAPIKey, RoleAdmin))))
	mux.Handle("POST "+v+"/api-keys/{id}/revoke", s.auth(sessionOnly(only(s.revokeAPIKey, RoleAdmin))))

	// exchange rates
	mux.Handle("GET "+v+"/companies/{cid}/rates", s.auth(s.listRates))
	mux.Handle("GET "+v+"/companies/{cid}/rates/latest", s.field(s.latestRates))
	mux.Handle("POST "+v+"/companies/{cid}/rates", s.auth(only(s.upsertRate, write...)))

	// chart of accounts
	mux.Handle("GET "+v+"/companies/{cid}/accounts", s.auth(s.listAccounts))
	mux.Handle("POST "+v+"/companies/{cid}/accounts", s.auth(only(s.createAccount, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/accounts/{id}", s.auth(only(s.updateAccount, write...)))
	mux.Handle("GET "+v+"/chart-templates", s.auth(s.listChartTemplates))
	mux.Handle("POST "+v+"/companies/{cid}/accounts/load-template", s.auth(only(s.loadChartTemplate, RoleAdmin, RoleAccountant)))

	// journal
	mux.Handle("GET "+v+"/companies/{cid}/journal", s.auth(s.listEntries))
	mux.Handle("POST "+v+"/companies/{cid}/journal", s.auth(only(s.createEntry, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/journal/{id}", s.auth(s.getEntry))
	mux.Handle("PUT "+v+"/companies/{cid}/journal/{id}", s.auth(only(s.replaceEntry, entry...)))
	mux.Handle("DELETE "+v+"/companies/{cid}/journal/{id}", s.auth(only(s.deleteEntry, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/journal/{id}/post", s.auth(only(s.postEntry, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/journal/{id}/reverse", s.auth(only(s.reverseEntry, write...)))

	// billing setup
	mux.Handle("GET "+v+"/companies/{cid}/billing/settings", s.auth(s.getBillingSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/billing/settings", s.auth(only(s.putBillingSettings, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/payment-methods", s.field(s.listMethods))
	mux.Handle("POST "+v+"/companies/{cid}/payment-methods", s.auth(only(s.createMethod, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/payment-methods/{id}", s.auth(only(s.updateMethod, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/areas", s.field(s.listAreas))
	mux.Handle("POST "+v+"/companies/{cid}/areas", s.auth(only(s.createArea, entry...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/areas/{id}", s.auth(only(s.updateArea, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/plans", s.auth(s.listPlans))
	mux.Handle("POST "+v+"/companies/{cid}/plans", s.auth(only(s.createPlan, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/plans/{id}", s.auth(only(s.updatePlan, write...)))

	// customers, subscriptions, meter readings
	mux.Handle("GET "+v+"/companies/{cid}/customers", s.field(s.listCustomers))
	mux.Handle("POST "+v+"/companies/{cid}/customers", s.auth(only(s.createCustomer, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/customers/{id}", s.field(s.getCustomer))
	mux.Handle("PATCH "+v+"/companies/{cid}/customers/{id}", s.auth(only(s.updateCustomer, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/customers/{id}/subscriptions", s.auth(only(s.createSubscription, entry...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/subscriptions/{id}", s.auth(only(s.updateSubscription, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/readings", s.field(s.readingSheet))
	mux.Handle("POST "+v+"/companies/{cid}/readings", s.field(only(s.saveReadings, field...)))

	// invoices and payments
	mux.Handle("POST "+v+"/companies/{cid}/billing/run", s.auth(only(s.runBilling, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/invoices", s.auth(s.listInvoices))
	mux.Handle("POST "+v+"/companies/{cid}/invoices", s.auth(only(s.createInvoice, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/invoices/{id}", s.auth(s.getInvoice))
	mux.Handle("POST "+v+"/companies/{cid}/invoices/{id}/void", s.auth(only(s.voidInvoice, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/payments", s.field(s.listPayments))
	mux.Handle("POST "+v+"/companies/{cid}/payments", s.field(only(s.createPayment, field...)))
	mux.Handle("GET "+v+"/companies/{cid}/payments/{id}", s.field(s.getPayment))
	mux.Handle("POST "+v+"/companies/{cid}/payments/{id}/void", s.auth(only(s.voidPayment, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/billing/jobs", s.auth(s.listBillingJobs))
	mux.Handle("GET "+v+"/companies/{cid}/billing/jobs/{id}", s.auth(s.getBillingJob))
	mux.Handle("POST "+v+"/companies/{cid}/invoices/{id}/credit-note", s.auth(only(s.createCreditNote, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/customers/{id}/refunds", s.auth(only(s.createRefund, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/bills/{id}/debit-note", s.auth(only(s.createDebitNote, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/stock/docs/{id}/void", s.auth(only(s.voidStockDoc, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/stock/transfers", s.auth(s.listStockTransfers))
	mux.Handle("POST "+v+"/companies/{cid}/stock/transfers", s.auth(only(s.createStockTransfer, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/stock/transfers/{id}/void", s.auth(only(s.voidStockTransfer, write...)))
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
	mux.Handle("POST "+v+"/companies/{cid}/stations", s.auth(only(s.saveStation, entry...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/stations/{id}", s.auth(only(s.saveStation, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/generators", s.auth(s.listGenerators))
	mux.Handle("POST "+v+"/companies/{cid}/generators", s.auth(only(s.saveGenerator, entry...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/generators/{id}", s.auth(only(s.saveGenerator, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/generators/{id}/log", s.auth(s.listGeneratorLog))
	mux.Handle("POST "+v+"/companies/{cid}/generators/{id}/hours", s.auth(only(s.saveGeneratorHours, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/generators/{id}/outages", s.auth(only(s.saveOutage, entry...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/generators/{id}/outages/{oid}", s.auth(only(s.saveOutage, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/boxes", s.field(s.listBoxes))
	mux.Handle("POST "+v+"/companies/{cid}/boxes", s.auth(only(s.saveBox, entry...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/boxes/{id}", s.auth(only(s.saveBox, entry...)))
	// ISP: MikroTik routers, auto-suspend / reconnect
	mux.Handle("GET "+v+"/companies/{cid}/routers", s.auth(only(s.listRouters, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/routers", s.auth(sessionOnly(only(s.saveRouter, write...))))
	mux.Handle("PATCH "+v+"/companies/{cid}/routers/{id}", s.auth(sessionOnly(only(s.saveRouter, write...))))
	mux.Handle("POST "+v+"/companies/{cid}/routers/{id}/test", s.auth(only(s.testRouter, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/isp/settings", s.auth(s.getISPSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/isp/settings", s.auth(only(s.putISPSettings, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/isp/run", s.auth(only(s.runISPNow, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/router-actions", s.auth(s.listRouterActions))
	mux.Handle("POST "+v+"/companies/{cid}/router-actions/{id}/retry", s.auth(only(s.retryRouterAction, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/subscriptions/{id}/network", s.auth(only(s.subscriptionNetwork, write...)))

	mux.Handle("GET "+v+"/companies/{cid}/tariffs", s.auth(s.listTariffs))
	mux.Handle("POST "+v+"/companies/{cid}/tariffs", s.auth(only(s.saveTariff, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/tariffs/{id}/apply", s.auth(only(s.applyTariff, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/tax-rates", s.auth(s.listTaxRates))
	mux.Handle("POST "+v+"/companies/{cid}/tax-rates", s.auth(only(s.saveTaxRate, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/tax-rates/{id}", s.auth(only(s.saveTaxRate, write...)))

	// cash boxes: balances and transfers (collector handover)
	mux.Handle("GET "+v+"/companies/{cid}/cash/balances", s.field(s.cashBalances))
	mux.Handle("GET "+v+"/companies/{cid}/cash/transfers", s.auth(s.listTransfers))
	mux.Handle("POST "+v+"/companies/{cid}/cash/transfers", s.auth(only(s.createTransfer, write...)))

	// bulk import (customers, subscriptions, opening balances, readings, accounts)
	mux.Handle("POST "+v+"/companies/{cid}/import/{type}", s.auth(only(s.importData, write...)))
	// migration from the legacy Magic system (zip made by tools/magic-export)
	mux.Handle("GET "+v+"/magic/imports", s.auth(sessionOnly(only(s.listMagicImports, RoleAdmin))))
	mux.Handle("POST "+v+"/magic/imports", s.auth(sessionOnly(only(s.createMagicImport, RoleAdmin))))
	mux.Handle("GET "+v+"/magic/imports/{id}", s.auth(sessionOnly(only(s.getMagicImport, RoleAdmin))))
	mux.Handle("POST "+v+"/magic/imports/{id}/chunk", s.auth(sessionOnly(only(s.magicChunk, RoleAdmin))))
	mux.Handle("POST "+v+"/magic/imports/{id}/analyze", s.auth(sessionOnly(only(s.analyzeMagicImport, RoleAdmin))))
	mux.Handle("POST "+v+"/magic/imports/{id}/commit", s.auth(sessionOnly(only(s.commitMagicImport, RoleAdmin))))

	// messaging (WhatsApp / SMS)
	mux.Handle("GET "+v+"/companies/{cid}/message-providers", s.auth(only(s.listProviders, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/message-providers", s.auth(only(s.saveProvider, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/message-providers/{id}", s.auth(only(s.saveProvider, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/message-templates", s.auth(s.listTemplates))
	mux.Handle("POST "+v+"/companies/{cid}/message-templates", s.auth(only(s.saveTemplate, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/message-templates/{id}", s.auth(only(s.saveTemplate, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/messages/settings", s.auth(s.getMessageSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/messages/settings", s.auth(only(s.putMessageSettings, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/messages/send", s.auth(only(s.sendMessages, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/messages/link", s.auth(s.messageLink))
	mux.Handle("GET "+v+"/companies/{cid}/messages", s.auth(s.listMessages))
	mux.Handle("POST "+v+"/companies/{cid}/messages/{id}/retry", s.auth(only(s.retryMessage, write...)))

	// sales: quotations -> orders -> deliveries / invoices, statements, documents
	mux.Handle("GET "+v+"/companies/{cid}/sales/orders", s.auth(s.listOrders))
	mux.Handle("POST "+v+"/companies/{cid}/sales/orders", s.auth(only(s.createOrder, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/sales/orders/{id}", s.auth(s.getOrder))
	mux.Handle("PUT "+v+"/companies/{cid}/sales/orders/{id}", s.auth(only(s.updateOrder, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/sales/orders/{id}/deliver", s.auth(only(s.deliverOrder, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/sales/orders/{id}/invoice", s.auth(only(s.invoiceOrder, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/sales/orders/{id}/{action}", s.auth(only(s.orderAction, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/sales/deliveries/{id}/void", s.auth(only(s.voidDelivery, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/reports/sales", s.auth(s.salesReport))
	mux.Handle("GET "+v+"/companies/{cid}/customers/{id}/statement", s.auth(s.getCustomerStatement))
	mux.Handle("GET "+v+"/companies/{cid}/statements", s.auth(s.listCustomerStatements))
	mux.Handle("POST "+v+"/companies/{cid}/statements/send", s.auth(only(s.sendStatements, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/send-document", s.auth(only(s.sendDocument, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/documents/settings", s.field(s.getDocSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/documents/settings", s.auth(only(s.putDocSettings, RoleAdmin)))
	// CRM
	mux.Handle("GET "+v+"/companies/{cid}/crm/stages", s.auth(s.listStages))
	mux.Handle("POST "+v+"/companies/{cid}/crm/stages", s.auth(only(s.saveStage, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/crm/stages/{id}", s.auth(only(s.saveStage, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/crm/summary", s.auth(s.crmSummary))
	mux.Handle("GET "+v+"/companies/{cid}/crm/users", s.auth(s.crmUsers))
	mux.Handle("GET "+v+"/companies/{cid}/crm/leads", s.auth(s.listLeads))
	mux.Handle("POST "+v+"/companies/{cid}/crm/leads", s.auth(only(s.saveLead, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/crm/leads/{id}", s.auth(s.getLead))
	mux.Handle("PATCH "+v+"/companies/{cid}/crm/leads/{id}", s.auth(only(s.saveLead, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/crm/leads/{id}/convert", s.auth(only(s.convertLead, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/crm/activities", s.auth(s.listActivities))
	mux.Handle("POST "+v+"/companies/{cid}/crm/activities", s.auth(only(s.saveActivity, entry...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/crm/activities/{id}", s.auth(only(s.saveActivity, entry...)))
	mux.Handle("DELETE "+v+"/companies/{cid}/crm/activities/{id}", s.auth(only(s.deleteActivity, entry...)))

	// point of sale (cashiers see only this)
	pos := []string{RoleAdmin, RoleAccountant, RoleDataEntry, RoleCashier}
	mux.Handle("GET "+v+"/companies/{cid}/pos/registers", s.field(only(s.listRegisters, pos...)))
	mux.Handle("POST "+v+"/companies/{cid}/pos/registers", s.auth(only(s.saveRegister, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/pos/registers/{id}", s.auth(only(s.saveRegister, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/pos/categories", s.field(only(s.listCategories, pos...)))
	mux.Handle("POST "+v+"/companies/{cid}/pos/categories", s.auth(only(s.saveCategory, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/pos/categories/{id}", s.auth(only(s.saveCategory, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/pos/catalog", s.field(only(s.posCatalog, pos...)))
	mux.Handle("GET "+v+"/companies/{cid}/pos/sessions", s.field(only(s.listSessions, pos...)))
	mux.Handle("POST "+v+"/companies/{cid}/pos/sessions", s.field(only(s.openSession, pos...)))
	mux.Handle("GET "+v+"/companies/{cid}/pos/sessions/{id}", s.field(only(s.getSession, pos...)))
	mux.Handle("POST "+v+"/companies/{cid}/pos/sessions/{id}/close", s.field(only(s.closeSession, pos...)))
	mux.Handle("GET "+v+"/companies/{cid}/pos/orders", s.field(only(s.listPosOrders, pos...)))
	mux.Handle("POST "+v+"/companies/{cid}/pos/orders", s.field(only(s.createPosOrder, pos...)))
	mux.Handle("GET "+v+"/companies/{cid}/pos/orders/{id}", s.field(only(s.getPosOrder, pos...)))
	mux.Handle("POST "+v+"/companies/{cid}/pos/orders/{id}/refund", s.field(only(s.refundPosOrder, pos...)))
	mux.Handle("POST "+v+"/companies/{cid}/pos/customers", s.field(only(s.posQuickCustomer, pos...)))
	mux.Handle("GET "+v+"/companies/{cid}/reports/pos", s.auth(s.posReport))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, http.StatusNotFound, "not_found") })
	// purchasing & stock
	mux.Handle("GET "+v+"/companies/{cid}/purchasing/settings", s.auth(s.getPurchSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/purchasing/settings", s.auth(only(s.putPurchSettings, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/suppliers", s.auth(s.listSuppliers))
	mux.Handle("POST "+v+"/companies/{cid}/suppliers", s.auth(only(s.saveSupplier, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/suppliers/{id}", s.auth(s.getSupplier))
	mux.Handle("PATCH "+v+"/companies/{cid}/suppliers/{id}", s.auth(only(s.saveSupplier, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/items", s.auth(s.listItems))
	mux.Handle("POST "+v+"/companies/{cid}/items", s.auth(only(s.saveItem, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/items/{id}", s.auth(s.getItem))
	mux.Handle("PATCH "+v+"/companies/{cid}/items/{id}", s.auth(only(s.saveItem, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/warehouses", s.auth(s.listWarehouses))
	mux.Handle("POST "+v+"/companies/{cid}/warehouses", s.auth(only(s.createWarehouse, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/bills", s.auth(s.listBills))
	mux.Handle("POST "+v+"/companies/{cid}/bills", s.auth(only(s.createBill, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/bills/{id}", s.auth(s.getBill))
	mux.Handle("POST "+v+"/companies/{cid}/bills/{id}/void", s.auth(only(s.voidBill, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/supplier-payments", s.auth(s.listSupplierPayments))
	mux.Handle("POST "+v+"/companies/{cid}/supplier-payments", s.auth(only(s.createSupplierPayment, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/supplier-payments/{id}/void", s.auth(only(s.voidSupplierPayment, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/expenses", s.auth(s.listExpenses))
	mux.Handle("POST "+v+"/companies/{cid}/expenses", s.auth(only(s.createExpense, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/expenses/{id}/void", s.auth(only(s.voidExpense, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/stock/docs", s.auth(s.listStockDocs))
	mux.Handle("POST "+v+"/companies/{cid}/stock/{kind}", s.auth(only(s.createStockDoc, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/reports/ap-aging", s.auth(s.apAging))
	mux.Handle("GET "+v+"/companies/{cid}/reports/stock-valuation", s.auth(s.stockValuation))
	mux.Handle("GET "+v+"/companies/{cid}/reports/expenses", s.auth(s.expensesReport))

	// back office: settings, HR & payroll (salaries: admin/accountant only), assets, recurring, bank reconciliation
	mux.Handle("GET "+v+"/companies/{cid}/backoffice/settings", s.auth(s.getBOSettings))
	mux.Handle("PUT "+v+"/companies/{cid}/backoffice/settings", s.auth(only(s.putBOSettings, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/employees", s.auth(only(s.listEmployees, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/employees", s.auth(only(s.saveEmployee, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/employees/{id}", s.auth(only(s.getEmployee, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/employees/{id}", s.auth(only(s.saveEmployee, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/employees/{id}/advances", s.auth(only(s.createAdvance, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/payroll", s.auth(only(s.listPayrollRuns, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/payroll", s.auth(only(s.createPayrollRun, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/payroll/{id}", s.auth(only(s.getPayrollRun, write...)))
	mux.Handle("DELETE "+v+"/companies/{cid}/payroll/{id}", s.auth(only(s.deletePayrollRun, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/payroll/{id}/payslips/{pid}", s.auth(only(s.updatePayslip, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/payroll/{id}/post", s.auth(only(s.postPayrollRun, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/payroll/{id}/pay", s.auth(only(s.payPayrollRun, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/payroll/{id}/void", s.auth(only(s.voidPayrollRun, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/assets", s.auth(s.listAssets))
	mux.Handle("POST "+v+"/companies/{cid}/assets", s.auth(only(s.createAsset, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/assets/{id}", s.auth(s.getAsset))
	mux.Handle("PATCH "+v+"/companies/{cid}/assets/{id}", s.auth(only(s.updateAsset, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/assets/{id}/dispose", s.auth(only(s.disposeAsset, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/depreciation/run", s.auth(only(s.runDepreciation, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/reports/assets", s.auth(s.assetRegister))
	mux.Handle("GET "+v+"/companies/{cid}/recurring", s.auth(s.listRecurring))
	mux.Handle("POST "+v+"/companies/{cid}/recurring", s.auth(only(s.saveRecurring, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/recurring/{id}", s.auth(only(s.saveRecurring, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/recurring/{id}/run", s.auth(only(s.runRecurringNow, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/bank/statements", s.auth(s.listStatements))
	mux.Handle("POST "+v+"/companies/{cid}/bank/statements", s.auth(only(s.importStatement, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/bank/statements/{id}", s.auth(s.getStatement))
	mux.Handle("POST "+v+"/companies/{cid}/bank/statements/{id}/auto-match", s.auth(only(s.autoMatchStatement, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/bank/lines/{id}/match", s.auth(only(s.matchBankLine, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/bank/lines/{id}/entry", s.auth(only(s.createBankLineEntry, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/bank/lines/{id}/{action}", s.auth(only(s.setBankLineStatus, write...)))
	mux.Handle("GET "+v+"/companies/{cid}/bank/reconciliation", s.auth(s.bankReconciliation))

	mux.Handle("/", spa(s.WebDir))

	return securityHeaders(jsonOnly(s.Sessions.LoadAndSave(mux)))
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
	Role       string `json:"role"`
	Locale     string `json:"locale"`
	APIKeyID   string `json:"api_key_id,omitempty"`
	Active     bool   `json:"-"`

	Plan            string     `json:"plan"`
	TrialEndsAt     *time.Time `json:"trial_ends_at"`
	IsPlatformAdmin bool       `json:"is_platform_admin"`
}

type principalKey struct{}

func principal(r *http.Request) *Principal { return r.Context().Value(principalKey{}).(*Principal) }

func scope(r *http.Request) db.Scope {
	p := principal(r)
	return db.Scope{TenantID: p.TenantID, UserID: p.ID, APIKeyID: p.APIKeyID, IP: clientIP(r)}
}

func (s *Server) loadPrincipal(ctx context.Context, sc db.Scope) (*Principal, error) {
	p := &Principal{}
	err := db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT u.id::text, u.tenant_id::text, t.name, u.email, u.full_name, u.role, u.locale,
			       (u.is_active AND t.is_active), t.plan, t.trial_ends_at
			FROM users u JOIN tenants t ON t.id = u.tenant_id
			WHERE u.id = $1`, sc.UserID).
			Scan(&p.ID, &p.TenantID, &p.TenantName, &p.Email, &p.FullName, &p.Role, &p.Locale, &p.Active, &p.Plan, &p.TrialEndsAt)
	})
	p.IsPlatformAdmin = err == nil && s.platformAdmins[strings.ToLower(p.Email)]
	return p, err
}

// auth: any signed-in office user (collectors and cashiers are refused). field: also collectors and cashiers
// (each handler then decides what they may see, e.g. with only(...)).
func (s *Server) auth(next http.HandlerFunc) http.Handler  { return s.authenticate(next, false) }
func (s *Server) field(next http.HandlerFunc) http.Handler { return s.authenticate(next, true) }

// authenticate accepts either a browser session cookie or "Authorization: Bearer pxv_..." API key.
func (s *Server) authenticate(handler http.HandlerFunc, allowCollector bool) http.Handler {
	next := func(w http.ResponseWriter, r *http.Request) {
		if role := principal(r).Role; !allowCollector && (role == RoleCollector || role == RoleCashier) {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		handler(w, r)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			p, ok := s.authAPIKey(ctx, strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), clientIP(r))
			if !ok {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next(w, r.WithContext(context.WithValue(ctx, principalKey{}, p)))
			return
		}
		uid, tid := s.Sessions.GetString(ctx, "uid"), s.Sessions.GetString(ctx, "tid")
		if uid == "" || tid == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		p, err := s.loadPrincipal(ctx, db.Scope{TenantID: tid, UserID: uid, IP: clientIP(r)})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !p.Active) {
			_ = s.Sessions.Destroy(ctx)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			serverErr(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(ctx, principalKey{}, p)))
	})
}

func isCollector(r *http.Request) bool { return principal(r).Role == RoleCollector }

// isFrontline: collectors and cashiers see only the money they took themselves.
func isFrontline(r *http.Request) bool {
	role := principal(r).Role
	return role == RoleCollector || role == RoleCashier
}

func only(next http.HandlerFunc, roles ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role := principal(r).Role
		for _, allowed := range roles {
			if role == allowed {
				next(w, r)
				return
			}
		}
		writeErr(w, http.StatusForbidden, "forbidden")
	}
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
