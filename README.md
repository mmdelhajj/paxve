# Paxve

Cloud accounting and billing for subscription businesses — generator electricity, internet, satellite TV, VOD —
built for Lebanon and the region, usable anywhere: **any currency, English / Arabic (RTL) / French, multi-tenant SaaS,
open REST API.**

Live: https://paxve.com

## What it does

| Area | Features |
|---|---|
| **Magic-style desk** | The office UI works like the legacy Magic desktop program: collapsible ribbon of tabs with picture tiles (Magic's Arabic names), several screens open at once as tabs that keep their state, status bar (user, cash box, rate, today's collections). Dense grids everywhere: filter row, sort, footer totals, right-click copy / Excel / CSV / print, double-click to open. **الجباية** collection screen driven by the keyboard (filter → ↓ → Enter → amount → Enter = saved and printed; F2 / F9 / Esc), barcode collection, meter readings saved on Enter, late payers, collector hand-over, "correct payment" (void by reversal and re-enter); paper-style cash receipt / expense / payment vouchers with the amount in Arabic words |
| **Apps** | Odoo-style modules switched on or off per company (subscriptions, accounting, collection always on; WhatsApp/SMS, stock, purchases, internet, satellite, POS, payroll, sales & CRM optional). Menus follow, and the API answers `403 app_disabled` for a switched-off app. Business type (generator / internet / satellite) tints the interface |
| **Accounting** | Chart of accounts with templates (Lebanese PCG, international), names in any language; journal entries in any currency, always balanced; posted entries immutable (reversal only); gap-free numbering; period lock; trial balance, ledger, profit & loss, balance sheet |
| **Professional accounting** | Cheques received and issued with every step (receive, deposit, collect, endorse to a supplier, returned by the supplier, part paid in cash, give back, bounce, freeze, bank paid, cancel, undo) and a due-date register; buy / sell dollars between cash boxes with the exchange gain or loss; fiscal year closing (result → retained earnings, year locked) and reopening; cost centers (branch, station, generator, project) tagged automatically from bills, diesel and expenses, re-taggable on posted lines, profit per cost center; monthly budgets with budget-vs-actual; opening balances wizard; cash flow statement; period comparison (month / same month last year / year / last 6 months); drill-down from every report line to the ledger, the journal entry and the source document; a "?" help panel on every accounting screen and an owner summary in plain sentences on the dashboard |
| **Currencies** | 65+ ISO currencies, base + second currency per company, daily official / buy / sell rates, realized FX gain/loss booked automatically |
| **Billing** | Customers, areas, plans (fixed monthly or metered: base fee + kWh × price), subscriptions, meter-reading sheets, one-click monthly billing run, manual invoices, printable invoices |
| **Payments** | Pay in several currencies at once (e.g. USD + LBP), automatic change (rounded to 1,000 / 5,000 LBP if wanted), oldest invoices paid first, customer balances per currency, aging report |
| **Collectors** | Collector role limited to their areas; mobile collector mode (PWA, works offline, payments queued and synced once — idempotent); Bluetooth ESC/POS receipts (Arabic rendered as image); personal cash boxes and handover |
| **Sales & CRM** | Product / service catalogue with prices per currency and default tax; quotations → sales orders → deliveries (stock out at average cost) → invoices (full or partial); quotes accepted by the customer on their portal link; customer statements (one or many, print / WhatsApp / SMS / e-mail); leads pipeline (drag-and-drop stages), activities with due dates and owners, notes; logo, header, footer and terms on every printed document |
| **Credit notes & returns** | Credit notes by amount or by line (stock comes back at its original cost), refunds from a cash box, returns to suppliers, VAT reversed; cancelling any of them undoes it |
| **Reports & export** | Every report (trial balance, ledger, P&L, balance sheet, aging, VAT, stock, POS, sales…) exports to Excel (RTL in Arabic), CSV or print / PDF; financial reports read daily ledger totals, so they stay fast on millions of journal lines |
| **Purchases & stock** | Suppliers, purchase bills (AP), supplier payments, expenses, items and warehouses with weighted-average cost, fuel consumption, stock adjustments, stock valuation, AP aging |
| **Point of sale** | Touch / tablet / phone till: product grid with categories and photos, search, barcode (USB/Bluetooth scanner or camera), discounts, walk-in or named customer, on-account sales, mixed USD/LBP payment with change, offline sales sent once when back online; cashier role, sessions with opening float and counted closing (over/short posted), X/Z reports, returns by line, Bluetooth or printer receipts, POS report by day / session / cashier / product / payment method |
| **HR & payroll** | Employees (USD or LBP salaries, transport, NSSF flag), advances with monthly recovery, monthly payroll (editable drafts, configurable NSSF rates, posted to 641/645/421/431), salary payment per cash box / bank, payslip print |
| **Fixed assets** | Asset register, straight-line monthly depreciation (idempotent per month), disposal with gain/loss, net book value report |
| **Recurring & bank** | Recurring journal entries / expenses posted automatically on due dates (respecting the period lock); bank statement import, auto-matching (amount ± 3 days), quick entries for fees, reconciliation report |
| **ISP automation** | MikroTik RouterOS API (PPPoE secrets / hotspot users): internet cut automatically when bills are overdue > N days and restored after payment, manual suspend / reconnect, queued router actions with retries, test connection (see docs/ISP.md) |
| **Messaging** | WhatsApp Cloud API, Twilio SMS, generic HTTP or wa.me links; templates in EN/AR/FR; reminders for unpaid bills; automatic receipt / bill messages |
| **Import** | CSV / Excel paste wizard for customers, subscriptions, opening balances, meter readings, accounts (validated dry run, all-or-nothing) |
| **Migration from Magic** | Read-only export script for the legacy Magic database (tools/magic-export), upload with dry run per company, then customers, subscriptions, plans, boxes, collectors, meter readings, unpaid bills, customer / supplier balances and one opening entry imported in one transaction; re-runnable without duplicates (see docs/MAGIC_MIGRATION.md) |
| **SaaS** | Public landing page, self sign-up with 14-day trial, plans with subscriber limits, platform owner console |
| **Security & permissions** | Permissions per module and action (view, create, edit, cancel, configure, approve, export) on every API route; built-in roles plus custom roles; users limited to chosen companies; two-step sign-in (authenticator app, recovery codes, can be required for the whole organisation); signed-in devices with remote sign-out; password reset by e-mail or admin link; password policy; optional approval of voids, credit notes and large payments; consolidated trial balance / P&L / balance sheet across companies |
| **Developers** | REST API `/api/v1` with API keys (hashed, per role, optional permission scopes, revocable), signed outbound webhooks with retries (invoice, payment, customer, POS order), OpenAPI 3.1 spec at `/api/v1/openapi.yaml` |

## Architecture

```
Browser / phone (React 19 + Mantine, PWA)  ──HTTPS──>  Caddy (TLS, HSTS)  ──>  Go app (net/http)  ──>  PostgreSQL 18
                                                         Cloudflare in front           │ background message sender
```

- **Go** (`net/http`, pgx v5, goose migrations, scs sessions, argon2id). One binary serves the API and the web app.
- **PostgreSQL 18 enforces the business rules**, not only the app:
  - every business table has `tenant_id` + forced row-level security; the app connects as `paxve_app` (no superuser, no BYPASSRLS, no DELETE on business tables);
  - posted journals balance (deferred constraint trigger) and are immutable; invoices/payments/bills cannot be edited or deleted, only voided (reversal);
  - `paid <= total`, stock never negative, gap-free document numbers, period lock;
  - every change is written to an append-only `audit_log` (user, API key, IP, company, before/after, and the approver when the change came from an approved request);
  - **company isolation**: each request under `/companies/{cid}/…` sets `app.company_id` (and a user limited to some companies sets `app.company_ids`); a restrictive row-level policy then hides and refuses every row of any other company, and every foreign key between company tables also matches on `company_id`, so no document can point to another company's customer, account or item (checked route by route by `TestCompanyIsolationEveryRoute`);
  - `ledger_daily` keeps posted totals per day / account / currency (filled by a background worker in journal-number order); `account_totals()` adds the few entries posted since, so reports are exact and fast.
- Money is `numeric` everywhere (decimal strings in JSON), never floating point.

## Layout

```
cmd/paxve/              entry point: serve, migrate, create-tenant, magic-sample
internal/db/            tenant-scoped transactions, migrations (embedded SQL, 00001…)
internal/auth/          argon2id passwords
internal/charts/        chart-of-accounts templates
internal/crypto/        AES-256-GCM encryption of stored secrets (APP_SECRET_KEY)
internal/routeros/      minimal MikroTik RouterOS API client (+ fakerouter for tests)
internal/magic/         reads a Magic export and computes the migration plan (pure Go, unit tested)
internal/httpapi/       JSON API, posting engine, reports, workers, OpenAPI spec, integration tests
web/                    React app (locales in web/src/locales: en, ar, fr)
deploy/Caddyfile        HTTPS reverse proxy
docs/                   API quick start, operations, ISP automation, migration from Magic, Arabic user guide
tools/magic-export/     read-only PowerShell export of a Magic database (SELECT only)
```

## Run

```sh
cp .env.example .env            # fill DB_OWNER_PASSWORD, DB_APP_PASSWORD (openssl rand -hex 24), PLATFORM_ADMINS
docker compose up -d --build
docker compose run --rm app create-tenant -name "My Company" -slug my-company -email admin@example.com -admin "Admin"
```

Or let customers sign up themselves at `/#/signup`. See [docs/OPERATIONS.md](docs/OPERATIONS.md) for deployment, backups and upgrades.

## Develop & test

```sh
make test        # starts a throwaway postgres:18 container, runs go test ./... (integration tests), removes it
cd web && npm ci && npm run dev    # http://localhost:5173, proxies /api to :8080
```

CI (GitHub Actions) runs gofmt, go vet, the integration tests against PostgreSQL 18, and the web build on every push.

## API

See [docs/API.md](docs/API.md) and the OpenAPI spec at `https://paxve.com/api/v1/openapi.yaml`.

## Languages

`web/src/locales/{en,ar,fr}.ts`. To add one: copy `en.ts`, translate, register it in `web/src/i18n.ts` and in
`supportedLocales` (`internal/httpapi/server.go`).
