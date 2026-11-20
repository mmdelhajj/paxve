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

## Manufacturing pro (BoM versions, routings, work orders, MRP, quality, subcontracting, traceability)

Ribbon tab **Manufacturing**. **Bills of materials** are versioned (versions of a BoM form a family; only one is active) and
typed: *made here* (with a **routing**: operations on **work centers** with capacity, efficiency and cost per hour), *kit /
phantom* (used as a component it is replaced by its own components) or *subcontracted* (a supplier makes it from components we
send, at a service price). BoMs can have **by-products** with a share of the cost; the **structure** view shows the multi-level
tree with the rolled-up cost, which can become the item's **standard cost**. **Manufacturing orders**: draft → confirmed (work
orders created from the routing, quality checks created, components reserved without touching what other orders reserved; bins
suggested by the warehouse's removal strategy) → in progress → done; **produce a part** and the rest becomes a **backorder**
with its own work orders and checks; component and finished **lots / serials**; **scrap** on the order. **Costing**: components
+ work center time (actual minutes, else planned) + labour + overhead + subcontracting, less the by-products' share; an item
with a standard cost enters stock at standard and the difference is posted to the **production variance** account.
**Subcontracting**: components move to the subcontractor's warehouse; receiving books the service to "received not billed" and
creates the subcontractor's bill that clears it. **MRP** per warehouse from confirmed sales orders, open orders' components and
the reorder rules, top-down by BoM level, creates the manufacturing orders and the requests for quotation per supplier in one
click. **Quality**: control points on an operation (per work center), on the finished product or on goods received; pass / fail
or a measure judged against tolerances; a work order cannot finish nor an order be produced while a check is to do; a failure
raises an **alert** followed to closure. **Traceability**: a lot or serial backward (what it is made of, which supplier) and
forward (what was made with it, which customer). **Reports**: order status and late orders, work center load against capacity,
cost variance (planned vs actual vs standard), missing components. **Shop floor** (`#/shopfloor`, tablet / phone): pick a work
center, start / pause / finish with a live timer, record the checks, scrap a component.
## Projects, field service, SLA, automation and fleet (phase 3)

**Projects** (app *prj*, ribbon tab **Projects**): each project has its own **kanban board** (columns are editable; a "done"
column closes its tasks), **tasks** with subtasks, a checklist, deadline, priority, planned hours, milestone, comments and files
(chatter), **milestones**, and **timesheets** typed in or from a **timer** (one running timer per employee; cost = hours ×
the employee's hourly cost, or salary / 208). **Billing**: by time (billable hours × the project rate, grouped per task), by
reached milestones, or a fixed price in parts (percent or amount, never more than what is left); voiding the invoice makes the
hours / milestones billable again. Expenses (`project_id` on the expense) and supplier bills can be charged to a project.
**Profitability** in the base currency from the journal: revenue (income lines of the project's invoices) − labour − expenses −
bills, margin, budget used. **Field service** (app *svc*): jobs from a ticket or a customer, a **dispatch board** (technicians ×
days, drag to plan), and the **technician phone view** (`#/tech`, also a "Jobs" tab in the collector app): start, add parts
from the van's stock, report, customer signature, photo and location, finish; then **invoice** labour hours × rate + parts (parts
leave stock with the invoice; the job's project gets the invoice). **Helpdesk SLA**: policies by priority and / or category
give each ticket a response and a resolution deadline (the most specific active policy); leaving "new" or the first note is the
first response; breaches are shown on the ticket and escalated once to the policy's manager; SLA report per policy and agent
with ratings. **Inbound**: a secret address per company for an e-mail forwarding service (JSON or Mailgun / SendGrid form) and
for the WhatsApp Cloud API webhook (verify handshake); a message becomes a ticket (customer matched by e-mail / phone) or a note
on the sender's open ticket. Closing a ticket creates a **rating link** (public page `#/rate/<token>`, sent by WhatsApp).
**Automation rules** (ribbon *Settings*): WHEN a record is created / changed (queued by database triggers) or ON A SCHEDULE
(also stock items and vehicles), IF field conditions hold, THEN create a to-do, message the customer, notify someone, set a field,
add a tag, call a webhook or make a purchase request. Run log, dry-run test (changes nothing), loop guard (changes made by rules
queue nothing), a rule pauses itself above 300 runs an hour, scheduled rules act once per record (or every N days); 8 ready-made
rules. **Fleet** (app *flt*): the delivery vehicles with make / model / VIN / papers (licence, insurance, inspection expiry), fuel
log (litres per 100 km between full tanks, flags for a consumption jump or an odometer going back), service plans by km and / or
days with the service log, costs posted as expenses (fuel / maintenance accounts in Fleet → Settings) plus any expense charged
to a vehicle, alerts and a cost report (cost per km).

## Online store, marketing, recruitment, appraisals and e-sign (phase 4)

**Online store** (app *web*, ribbon tab **Online store**): one public shop per company at `/#/shop/<address>` — Arabic-first,
mobile-first, in the company colour, with logo, banner and tagline. The catalog is the company's items (photo, shop description,
variants grouped under their template, categories, search), with the live stock of one warehouse and the prices of one price
list (else each item's own price in the shop currency, converted at today's rate); prices also show in LBP. The cart is priced
on the server exactly like the sales order: coupons and automatic promotions of the promotions engine, the item's sales tax,
a delivery fee per zone (free above an amount). Checkout: delivery or pickup, cash on delivery / pay at pickup / Whish / OMT
(with the company's payment note), minimum order, honeypot and per-address rate limits, or "order via WhatsApp" (a prefilled
`wa.me` message). An order finds the customer by phone (or creates one) and becomes a normal **sales order (source "online")**,
confirmed at once or by staff; it then follows the warehouse steps, the **delivery board** (send selected orders on a trip),
delivery, invoice and collection. The customer gets a WhatsApp message with a **tracking page** (`/#/order/<token>`:
received → preparing → on the way → delivered). Staff screens: online orders, what the shop shows (visible, featured,
description), delivery zones, settings (address, look, warehouse, price list, payments, terms, the careers page) with the links
and a catalog message to share on WhatsApp.

**Marketing** (app *mkt*): mailing lists (customers, or contacts pasted / typed), the opt-out register, audiences by filters on
the customers (area, status, balance, overdue days, tags, bought / not bought in N days, with / without subscription), campaigns
over WhatsApp / SMS / e-mail through the normal message queue with variables (`{name} {company} {coupon} {link} {shop} {optout}`),
a coupon from the promotions, a **tracked link** (`/api/v1/r/<token>`), an automatic **opt-out link** (`/#/optout/<token>`),
scheduled sending (background worker) and a throttle per minute; a test send; stats from the queue: sent, **delivered**
(WhatsApp Cloud delivery receipts through the inbound webhook), failed, skipped (opted out / no phone / no e-mail), clicked,
opted out. Opted-out people are never messaged again.

**Recruitment** (app *hr*, permission module *talent*): job positions published on a public **careers page**
(`/#/careers/<address>`, apply with a CV up to 5 MB, kept as an attachment), the applicant pipeline as a kanban (new →
screening → interview → offer → hired / refused), ratings, interviews with interviewer, feedback and score, the **offer**
(sent by WhatsApp or e-mail) and the **hire**, which creates the employee file (salary, currency, start date — payroll uses it
like any employee), copies the CV to it and closes a filled position. **Appraisals**: review cycles; opening one gives every
active employee an appraisal with weighted goals; employees score themselves on **My appraisal**, the reviewer scores and
comments, closing the cycle fixes the final scores; report with the average and the spread.

**E-sign** (core, ribbon *Daily* and *CRM*): a quotation, a subscription agreement (generated from the plan and the company's
terms), a contract text or an uploaded PDF / picture is sent for signature — one public link per signer (`/#/sign/<token>`),
in order or all at once, by WhatsApp / SMS / e-mail. Signers draw or type their signature (always stored as a picture) and
agree; each signature keeps the IP, time, browser and the document's **SHA-256** (a quotation changed after sending is refused).
Every step goes to an append-only audit trail; reminders every N days (5 at most), expiry, decline with a reason. When everyone
has signed, a **certificate PDF** (document text, hash, signatures, audit trail) is stored on the request, the quotation /
subscription and sent to the signers; a quotation signed online is accepted and can **confirm itself** into a sales order.

## Smart features: alerts, forecast, risk, reorder, route, AI assistant (phase 5)

App *ai* (ribbon tab **Smart**, on for every company; permission module *smart*: view / ai / manage).

**Alerts center** — deterministic checks, no AI key needed. Each one compares the last 7 days with the company's own weekly
history (z-score, at least 4 weeks) or a fixed limit, all configurable (Smart › Settings): generator **diesel per running
hour**, a cashier's **discount share** (own history, else colleagues), **till shortage / overage** at shift close and repeated
small shortages, **voids / credit notes / refunds** per user, **payments recorded outside working hours** (company time zone),
**stock count losses**, a **collector bringing in less** than usual, **duplicate supplier bills** (same supplier invoice number,
or same amount within a week) and the fleet's fuel flags. Alerts have a fingerprint (no repeats; dismissed / resolved ones stay
so), a level, a plain-language explanation, and ack / resolve / dismiss / reopen with a note. A background worker checks every
6 hours, re-scores risk daily, and sends a **daily digest** to the admins (in-app + e-mail, optionally WhatsApp) after the
chosen hour.

**Cash forecast (13 weeks)**: opening cash of every till / bank, open customer bills on their due date + the customer's usual
delay (older debt weighted down), next months' subscription billing collected as usual, till takings, supplier bills on their
due dates, salaries at month end, recurring and average expenses; weekly and monthly, good / bad scenarios, the lowest point and
the first week below zero, also in LBP.

**Late-payer / churn risk**: a 0–100 score per customer (late and leaving sub-scores) from average days late and its trend,
overdue days and bills, partial payments, service cuts, suspended / ended subscriptions, days since the last payment and tenure,
with the factors that explain it; feeds the collector route.

**Smart reorder**: weekly demand (weighted 4-week average, seasonal factor from last year when a full year is known), the
supplier's lead time (price list) or a default, safety stock for a service level → suggested min / max and quantity to order.
One click saves them as **reorder rules** (the same rules replenishment and MRP read) or creates **purchase requests** (draft
POs per supplier, priced from the supplier's price list).

**Collector route**: customers with bills to collect (collectors: their own areas) ordered nearest-neighbour + 2-opt from the
collector's position, riskiest / biggest first when the list is cut; an OpenStreetMap map (Leaflet), Google Maps directions per
10 stops, and the customers without a location. Locations are captured from the phone's GPS ("I am here") in the collector app
(**Route** tab) or clicked / typed in the office (`PUT /customers/{id}/location`).

**AI assistant and bill reading** (Claude, official Go SDK `anthropic-sdk-go/v2`, model `claude-opus-5-5` by default, server-side
refusal fallback): off until the platform owner sets `PAXVE_ANTHROPIC_API_KEY` (and optionally `PAXVE_AI_MODEL`) in `.env`.
The assistant answers in the user's language from **read-only tools** (late customers, sales, profit by month, expenses, aging,
cash boxes, cash forecast, stock, best sellers, suppliers, customer card, alerts) — the model never writes SQL; every tool runs in
the asking user's own transaction (row security) and checks their permission first. **Bill reading**: a photo / PDF of a
supplier bill → structured reading (supplier, number, dates, currency, lines, totals) → a draft matched to the company's
supplier (tax number / name), items and currency, with a duplicate check; the bill form opens filled in and the file is attached
to the saved bill. Both run in the background (the screen polls), are metered per organisation per month
(`platform_plans.ai_monthly_limit`), and the platform console shows the use per organisation (**AI** tab).

## Business packs: electronics, furniture, wholesale, restaurant, pharmacy, factory (phase 6a)

Choosing a business type (sign-up, Apps page or Retail › Business packs) applies its **pack**: the right apps, the defaults and
optional demo data, all through the normal handlers. Ribbon tab **Business** shows only the tiles of the company's type.

- **Electronics**: serials / IMEI scanned at the till (one per unit) and on office invoices (`lines[].lots`), serial history
  (bought from, sold to, repairs), warranty card print, **trade-in** at the till (a tender on a clearing account; the used device
  enters stock at the agreed value, Dr inventory / Cr clearing), repairs and instalments as before.
- **Furniture**: made-to-order items with **options** (extra price) and **price per m²** (configurator), **deposits** on sales
  orders (a customer payment on account linked to the order, used by its invoice), **delivery + assembly** scheduling (a
  delivery-board trip with the balance to collect + a service job for the fitter), showroom warehouses.
- **Wholesale**: price lists as price tiers, credit limit **blocked**, salesman **routes**, **vans** (warehouse per salesman):
  load in the morning, sell from the phone (`#/van`, offline queue, customer prices, credit checked on the unpaid part), close the
  day with a count (differences booked) and unload; `invoices.salesman_id` (van and till sales) feeds the **commission** report.
- **Restaurant**: tables / floor plan, modifiers, open checks (dine-in / takeaway / delivery) with kitchen lines per station,
  **kitchen display** (`#/kds`), split by lines, payment as a normal till sale with **service charge** (revenue) and **tip** (a
  liability), dishes = kits whose components are ingredients (taken out of stock on sale), daily **food cost %**.
- **Pharmacy**: drug fields (generic name, active ingredient, strength, form, prescription, controlled), search with
  alternatives, FEFO with **expired lots blocked** (`companies.block_expired`), prescription required at the till, controlled
  register, **insurance share** (a tender on "Insurance claims receivable" + a claim to submit / settle / reject).
- **Factory**: the manufacturing apps with starter work centers, BoM and routing.

## Installable apps and the marketing site (phase 6b)

- **Four installable web apps** (PWA) on one site, each with its own manifest and icons (`web/public/manifest-*.webmanifest`,
  `web/public/icons/`); `/pwa.js` (a plain same-origin script, allowed by the CSP) picks the manifest and keeps the browser's
  install offer for the in-app **Install** button (iPhone: an "Add to Home Screen" hint). The service worker caches the shell,
  the boot script, manifests and icons.
  - **Paxve** (`/`) — the full system.
  - **Paxve Field** (`/field` → `#/collect`) — collectors, drivers, technicians and van salesmen: tabs by what the user has
    (deliveries / jobs shown with a count), a "My van today" entry when the user drives a van, payments queued offline, and
    the app icon badge (deliveries + jobs + payments not sent).
  - **My account** (`/my`) — the customer portal remembered on the phone (`#/my` opens it), now with consumption per meter,
    tickets (report a problem → ticket channel `portal`, SLA applied, 5 a day per customer; rate a fixed one) and online orders
    with tracking (`GET /portal/{token}/more`, `POST /portal/{token}/tickets`).
  - **Paxve Owner** (`/owner` → `#/owner`) — today's collections and sales, money in tills / banks, what customers owe (and
    overdue), smart alerts, approvals waiting, in-app notifications and the AI assistant (`GET /owner/today`, `reports.view`;
    alerts only with `smart.view`). Refreshes every minute; icon badge = urgent alerts + approvals + unread notifications.
- **Marketing site** at `paxve.com` for signed-out visitors (Arabic first, EN / FR, `?lang=`): hero, the 12 business types with
  a card each, features, comparison with spreadsheets / old programs, plans from the platform (`GET /public/site`, monthly /
  yearly), FAQ, sales contact (`PAXVE_SALES_PHONE` / `PAXVE_SALES_EMAIL`, hidden when not set) and a **demo request** form
  (`POST /public/demo-request`, honeypot, 5 an hour per address) that lands in the platform console › **Demo requests**
  (status new / contacted / won / lost, notes, WhatsApp). SEO: meta + OpenGraph (`/og.png`), `robots.txt`, `sitemap.xml`.
- **Leftovers closed**: restaurant **split equally** (N guests, each pays a share; recorded as tenders of one sale), payroll
  **posting date** option (HR › Payroll: today while the month runs, or always the month end), **supplier opening balances**
  import (Import › Supplier balances: one opening supplier bill per row against an equity account), **collectors with a cash
  box of their own** can no longer use the shared office boxes, smart alert **price_override** (catalog prices lowered by hand
  at the till; `pos_orders.price_cut`), and offline payments carry the **device time** (`payments.client_time`), which the
  "payments at night" check uses.

## Branches, inter-company, layouts, dashboards, sign-in, tenders (phase 8)

- **Branches** (Accounting › Branches): a branch has its own cost center (`BR-<code>`), so the income and expense lines of its
  documents carry it and the **profit by branch** comes from the ledger. Documents (invoices, bills, payments, sales / purchase
  orders, expenses, supplier payments, till orders) get the branch of the user who makes them (`users.branch_ids`, first = default)
  through a database trigger. A user limited to branches only sees those branches' documents (row policy `branch_scope`) plus the
  company-wide ones. An admin can move a document to another branch (by number); only the tag changes.
- **Inter-company** (Accounting › Inter-company): link two companies of the organisation; each side gets the other as a customer
  and a supplier and a current account (A: asset, B: liability). A sale of one to the other is **mirrored as a supplier bill** on the
  other side (background job every 2 minutes, or "Mirror sales now"); a voided sale voids the unpaid bill. **Loans, transfers and
  repayments** post both sides at once. **Group accounts** (`GET /reports/consolidated?...&eliminate=1`): the current accounts, the
  open inter-company invoices and the inter-company sales against the buyer's expenses are taken out; a difference goes to an
  equity line so the balance sheet still balances. Goods still in the buyer's stock keep their margin.
- **Print layouts** (Settings › Print layouts): per document (invoice, receipt, quotation, sales order, purchase order, supplier
  bill): colour, logo and its size, title, header / footer text, fields shown, block order, second language for titles, QR code
  (document details or own text / link), paper (A4, A5, 80 / 58 mm), frame, text size; live preview; used by every print screen.
- **Dashboards**: *My dashboard* (Daily work) with widgets from a catalogue of fixed metrics (each with its own permission), drag
  to arrange, sizes and titles; *Role dashboards* (Settings) set the default of each role or of everyone.
- **Sign-in**: Google / Microsoft sign-in (OIDC code + PKCE) when the platform sets `PAXVE_GOOGLE_CLIENT_ID` / `_SECRET` and / or
  `PAXVE_MS_CLIENT_ID` / `_SECRET` (`PAXVE_MS_TENANT`, default `common`); only existing users, two-step sign-in still applies,
  the organisation can switch it off. Password **complexity** and **expiry** (after it only a password change is possible),
  **idle sign-out** per company (warning one minute before; the server also ends idle sessions), **sign-in history** (failed
  attempts included).
- **Tenders** (Purchases › Tenders): lines, bids per supplier (price per line, lead time), comparison with the best price per line,
  award per line into confirmed purchase orders (one per supplier) or into blanket agreements. **Blanket agreements**: price and
  quantity agreed for a period; purchase orders drawn at the agreed price, the remaining quantity followed and enforced.

## Bank files, card terminals, QR ordering, carriers, e-mail, timelines (phase 9)

- **Bank files** (Accounting › Bank files & matching): import CSV (column map, date format, decimal mark), OFX / QFX, MT940 and
  CAMT.053. Every line gets a fingerprint (the bank's id, or date + amount + text): a file imported twice is refused, an overlapping
  file only adds the new lines. **Matching rules** (text pattern, direction, amount range, bank box) book bank fees / interest on an
  account, settle a customer's invoices, pay a supplier's bills, or ignore. **Suggestions** score open invoices / bills (amount,
  number in the text, name). **Auto-reconcile**: exact journal matches, then the rules, then documents found without doubt; one click
  books the payment and matches the line.
- **Card terminals** (Till › Card terminals): a card box gets a terminal driver (`simulator` for training, `manual` for a stand-alone
  terminal: the cashier types the approval code; a real acquirer driver plugs into the same interface). The till charges the card
  first; a sale paid by that box needs its approved charge. Settlement of the batch: Dr bank (net) + Dr card fees, Cr the card box
  (clearing); the bank statement line of the net matches that entry.
- **QR table ordering** (restaurant): each table has a QR link to `#/menu/<token>`; the guest's order waits for staff, who accept it
  onto the table's check (sent to the kitchen screen). Limits: 5 waiting and 20 an hour per table, honeypot, per-IP limit.
- **Shipping** (Stock › Shipping & carriers): own / local courier (Paxve's label PDF, status by hand), Aramex and DHL Express
  (switched off until their keys are entered; rates, carrier label, tracking every 30 minutes). Online orders show the parcel.
- **E-mail from records**: the chatter's E-mail tab sends with a PDF of the document and chosen files; replies come back to the
  record through the subject mark `[PX-…]` or the plus address of `reply_to_email`, into the chatter.
- **Timelines**: project tasks (start dates, dependencies, shift dependants, loops refused), production (work orders per work
  center, plan an order in the center's hours, daily load vs capacity), technicians (jobs per technician, double bookings).
- **Delivery note** print in the "delivery" layout; **unrealized inter-company profit** removed from stock and profit in the group report.

## Appearance: comfort for the eyes (migration 00052)

Same Magic layout (ribbon, tiles, window tabs, grids, status bar, F-keys); only colours, sizes and spacing change.
- **Themes**: *Comfort* (default: warm off-white ground, softer ink #1f2937, calm navy bars, gentle lines, AA contrast),
  *Classic* (the previous look), *Dark* (low-glare navy grey #14181f, not black), *High contrast* (black on white, strong lines,
  yellow selection, for weaker eyes), *Automatic* (follows the device: dark when the phone/PC is dark, else Comfort).
- **Text size** normal / large (×1.125) / extra large (×1.25): the whole app area scales (CSS `zoom` on `#root`); Mantine
  pop-ups on the page body follow through `--mantine-scale`; pointer maths (timeline drag, signature pad) divide by the zoom.
- **Row spacing** comfortable (default) / compact (classic Magic rows).
- Where: the eye button in the title bar and in the phone apps (collector, till, owner, customer portal), and *My security →
  Appearance*. Saved at once on the device and on the account (`PATCH /me {ui_prefs}`), so it follows the user; admins set the
  organisation's default for users who never chose (`PUT /org/ui-default`). Public pages (site, shop, signing) keep their own design.
- Tokens: `web/src/magic.css` (`:root[data-mx-theme=…]`, `[data-mx-size]`, `[data-mx-density]`), logic `web/src/appearance.ts`,
  panel `web/src/components/AppearanceMenu.tsx`; browser check `web/e2e/comfort-ui.cjs`.
## How Paxve compares

See [docs/GAP_ANALYSIS.md](docs/GAP_ANALYSIS.md): feature-by-feature comparison with Odoo 19 and the usual SMB ERPs, and the
honest list of what is still open (purchase orders, late fees / dunning levels, FX revaluation, deferred revenue, online
portal payment, ZATCA phase 2 / Egypt ETA, expense claims, custom fields).
### Phase 7 audit fixes

Security and accounting audit findings fixed (migration 00048): money-only POS refunds (insurance / trade-in), deposits reserved for their order, commission net of returns, idempotent supplier openings, LBP claim FX, control-account guards, full-number phone matching, shop abuse limits, atomic AI quotas, van shortage approval, inbound webhook signatures, e-sign codes and link expiry, CV quota, cache cleared at sign-out, reserved shop addresses. See `docs/API.md` → "Phase 7 audit fixes".
### Phase 7 UX fixes

UX audit findings fixed (migration 00049): each business type starts with its own apps and ribbon (subscription, collector and
generator screens only for the businesses that use them; choosing another type previews the apps to add / switch off), a company added
from Settings gets its chart of accounts, language and apps at once and a failing pack demo rolls back, sign-up asks the phone and the
LBP rate (20 an hour per IP, the wait in seconds), missing exchange rates are flagged instead of "≈ 0 LBP", system-created names in the
company's language (plus a rename tool for existing companies), empty screens with a hint and an action, required-field and server
errors always shown, a back-to-system button on full-screen modes, phone ribbon arrows, Arabic month picker.