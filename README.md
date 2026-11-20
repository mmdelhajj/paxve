# Paxve

Cloud accounting and billing for subscription businesses — generator electricity, internet, satellite TV, VOD —
built for Lebanon and the region, usable anywhere: **any currency, 11 languages (Arabic / Persian / Urdu / Kurdish RTL),
19 country packs, multi-tenant SaaS, open REST API.**

Live: https://paxve.com

## What it does

| Area | Features |
|---|---|
| **Magic-style desk** | The office UI works like the legacy Magic desktop program: collapsible ribbon of tabs with picture tiles (Magic's Arabic names), several screens open at once as tabs that keep their state, status bar (user, cash box, rate, today's collections). Dense grids everywhere: filter row, sort, footer totals, right-click copy / Excel / CSV / print, double-click to open. **الجباية** collection screen driven by the keyboard (filter → ↓ → Enter → amount → Enter = saved and printed; F2 / F9 / Esc), barcode collection, meter readings saved on Enter, late payers, collector hand-over, "correct payment" (void by reversal and re-enter); paper-style cash receipt / expense / payment vouchers with the amount in Arabic words |
| **Apps** | Odoo-style modules switched on or off per company (subscriptions, accounting, collection always on; WhatsApp/SMS, stock, purchases, internet, satellite, POS, payroll, sales & CRM, service & maintenance optional). Menus follow, and the API answers `403 app_disabled` for a switched-off app. Business type (generator / internet / satellite) tints the interface |
| **Accounting** | Chart of accounts with templates (Lebanese PCG, international), names in any language; journal entries in any currency, always balanced; posted entries immutable (reversal only); gap-free numbering; period lock; trial balance, ledger, profit & loss, balance sheet |
| **Professional accounting** | Cheques received and issued with every step (receive, deposit, collect, endorse to a supplier, returned by the supplier, part paid in cash, give back, bounce, freeze, bank paid, cancel, undo) and a due-date register; buy / sell dollars between cash boxes with the exchange gain or loss; fiscal year closing (result → retained earnings, year locked) and reopening; cost centers (branch, station, generator, project) tagged automatically from bills, diesel and expenses, re-taggable on posted lines, profit per cost center; monthly budgets with budget-vs-actual; opening balances wizard; cash flow statement; period comparison (month / same month last year / year / last 6 months); drill-down from every report line to the ledger, the journal entry and the source document; a "?" help panel on every accounting screen and an owner summary in plain sentences on the dashboard |
| **Currencies** | All 156 ISO currencies, base + second currency per company, daily official / buy / sell rates, realized FX gain/loss booked automatically |
| **Billing** | Customers, areas, plans (fixed monthly or metered: base fee + kWh × price), subscriptions, meter-reading sheets, one-click monthly billing run, manual invoices, printable invoices |
| **Payments** | Pay in several currencies at once (e.g. USD + LBP), automatic change (rounded to 1,000 / 5,000 LBP if wanted), oldest invoices paid first, customer balances per currency, aging report |
| **Collectors** | Collector role limited to their areas; mobile collector mode (PWA, works offline, payments queued and synced once — idempotent); Bluetooth ESC/POS receipts (Arabic rendered as image); personal cash boxes and handover |
| **Sales & CRM** | Product / service catalogue with prices per currency and default tax; quotations → sales orders → deliveries (stock out at average cost) → invoices (full or partial); quotes accepted by the customer on their portal link; customer statements (one or many, print / WhatsApp / SMS / e-mail); leads pipeline (drag-and-drop stages), activities with due dates and owners, notes; logo, header, footer and terms on every printed document |
| **Credit notes & returns** | Credit notes by amount or by line (stock comes back at its original cost), refunds from a cash box, returns to suppliers, VAT reversed; cancelling any of them undoes it |
| **Reports & export** | Every report (trial balance, ledger, P&L, balance sheet, aging, VAT, stock, POS, sales…) exports to Excel (RTL in Arabic), CSV or print / PDF; financial reports read daily ledger totals, so they stay fast on millions of journal lines |
| **Purchases & stock** | Suppliers, purchase bills (AP), supplier payments, expenses, items and warehouses with weighted-average cost, fuel consumption, stock adjustments, stock valuation, AP aging |
| **Point of sale** | Touch / tablet / phone till: product grid with categories and photos, search, barcode (USB/Bluetooth scanner or camera), discounts, walk-in or named customer, on-account sales, mixed USD/LBP payment with change, offline sales sent once when back online; cashier role, sessions with opening float and counted closing (over/short posted), X/Z reports, returns by line, Bluetooth or printer receipts, POS report by day / session / cashier / product / payment method |
| **HR & payroll** | Employee file (personal data, photo, documents with expiry, contract with end reminder, manager, bank / IBAN), salary split in two currencies, housing / commission / fixed components, advances with monthly recovery, schedules, holidays, attendance (phone clock-in with location, fingerprint CSV import, lateness / overtime), leave (types per country, monthly accrual, balances, phone requests, manager approval), detailed payslips with country rules (Lebanon NSSF branches and ceilings, family allowances, salary tax brackets and family deductions, daily transport; KSA GOSI and gratuity; UAE gratuity and WPS file), draft → approved → posted → paid per currency, payslip print / WhatsApp, employee self-service page with a one-click login (ready-made "Employee" role), HR reports. See `docs/PAYROLL.md` |
| **Service & maintenance** | Service desk app: numbered customer requests (power cut, no internet, meter, bill, new connection, complaint…) with priority, assignee, deadline, notes and status history, open / urgent / late summary; generator maintenance plans every N running hours and/or N days, due / soon from the hour-meter log, work log with cost |
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

## Module T: countries, currencies, languages, professional stock

- Country packs for 19 countries (chart, account roles, taxes, invoice rules, ZATCA QR for Saudi Arabia) — see docs/LOCALIZATION.md.
- All ISO 4217 currencies; automatic daily rates (opt-in, never over a typed rate); statements in any presentation currency.
- 11 interface languages (8 machine-translated, RTL for Persian / Urdu / Kurdish), customer language, per-tenant text corrections.
- Stock: FIFO or average costing per item, lots / serial numbers with expiry and traceability, units of measure on purchases,
  kits, reorder rules with replenishment and days-left, stock counts, landed costs, price lists with quantity breaks
  (shown live on the POS till for a named customer).

## Finishing stage

Online payment on the customer portal (Stripe, any bank hosted payment page, Whish / OMT transfer reference), ZATCA
phase 2 (checked against the ZATCA sandbox) and Egypt ETA e-invoicing, employee expense claims, custom fields,
manufacturing (bills of materials, production orders, scrap), water utilities, goods receipts with 3-way matching
and supplier price lists, subscription proration, rotating shifts, leave accrual history, clock-in selfie and
geofence, ZKTeco fingerprint push (ADMS), invoices printed in the customer's language. See docs/GAP_ANALYSIS.md.

## Retail core part 1 (shops, supermarkets, clothing)

Ribbon tab **Retail**: sizes & colours (an item becomes a template; each combination is a normal item with its own
barcode, price and stock; stock grid that creates a purchase order), promotions (discount, buy X get Y, quantity tiers,
bundles, basket discount, coupons, dates / week days / hours / tills, best deal or stackable; booked net of the discount,
live on the till and with "Apply promotions" on quotes / orders), loyalty points (tiers, card by barcode or phone, points
as a cost and a liability, used at the till), gift cards and free vouchers (liability), scale barcodes (prefix 20–29 with
weight or price, PLU, PLU export), shelf / barcode labels (A4, small, ZPL / TSPL), near-expiry markdowns, receive by
scanning, retail report (category, hour, margin by item, promotions) and business packs (Supermarket, Clothing — also
chosen at sign-up, with demo data). The till adds held baskets, quick items, F2/F4/F7/F8/F9, a customer display window
(`#/pos-display`), the cash drawer kick and a denomination count at closing. See docs/VERTICALS.md.

## Platform console (the Paxve owner's back-office)

Ribbon tab **Platform** (only for `PLATFORM_ADMINS`): overview (states, MRR / ARR, sign-ups, ending soon, churn, money
per month), every organisation with search / filters / export, detail (users with last sign-in, companies, usage, payments,
history), renew with payment (cash, Whish, OMT, bank, card), block with a reason / unblock, extend a trial, change plan,
price and limits, reset the admin password (one-time), read-only support view, editable plans and prices, renewal requests.
Lifecycle: trial → active → grace (N days, banner) → expired (read-only) → blocked (no sign-in); a worker records state
changes and sends reminders 7 / 3 / 1 days before and on expiry through the owner's own WhatsApp / e-mail settings.
Organisations see **Settings → My Paxve subscription** (plan, end date, usage against limits, payments) and send renewal
requests from there. Optional `PAXVE_SALES_PHONE` / `PAXVE_SALES_EMAIL` are shown to them as where to pay.

## Warehouse pro (bins, steps, packages, picking, dropship, delivery board)

Ribbon tab **Stock**: per warehouse, **bins** (zone → aisle → shelf → bin, generated in one click, barcode labels), **receipt in
1/2/3 steps** (receive → QC → put away) and **delivery in 1/2/3 steps** (pick → pack → ship), removal strategy (FIFO, FEFO,
closest bin), **putaway rules** by item or category and **cross-dock** (goods open orders wait for go straight to the shipping
area). Each step is a task (`wh_ops`); a task done in part leaves a backorder; the next step is created with what was really
done; the ship step books the delivery. Bins are the physical place only: value stays per item and warehouse, so every existing
flow keeps working and a trigger takes stock that leaves a warehouse out of the unbinned part first, then the bins (shipping area
first). Also: **packages and pallets** (content, weight, dimensions, label with barcode, shipped with the delivery), **batch /
wave picking** (one pick list for many orders in bin order), **dropship** (a sales order makes a purchase order shipped to the
customer; the supplier bill delivers the order through a dropship warehouse), stock **reports** (by bin, slow / dead stock, ABC,
ageing, value per warehouse and per inventory account against the ledger), and the **delivery board** (vehicles, trips with
stops, driver, route sheet; the driver confirms each stop from the phone with the receiver's name, a signature, a photo and the
place). Phone apps: `#/scan` (camera via BarcodeDetector or a Bluetooth / USB scanner; look up, receive a PO, do tasks, move,
count a bin; moves, counts and tasks made offline are queued) and `#/driver` (also the **Deliveries** tab of the collector app).

## How Paxve compares

See [docs/GAP_ANALYSIS.md](docs/GAP_ANALYSIS.md): feature-by-feature comparison with Odoo 19 and the usual SMB ERPs, and the
honest list of what is still open (purchase orders, late fees / dunning levels, FX revaluation, deferred revenue, online
portal payment, ZATCA phase 2 / Egypt ETA, expense claims, custom fields).