# Paxve vs Odoo 19 and leading 2026 SMB ERPs — gap analysis

Reviewed 2026-11-19 against Odoo 19 (Community + Enterprise apps), and the feature sets small businesses usually compare
(Zoho Books / Zoho Inventory, QuickBooks Online Advanced, Xero, ERPNext 15, Splynx / UISP for ISPs). The question is not
"does Paxve copy Odoo" but "can a Lebanese generator / ISP / satellite business, or a regional SMB, run on it without missing
something they need every week".

Status: **Yes** = done and tested · **Partial** = the common case works, edge cases listed · **No** = not built ·
**N/A** = not useful for the target customers.

## Accounting

| Feature | Odoo 19 | Paxve | Status |
|---|---|---|---|
| Chart of accounts per country, multi-language names | Fiscal localisations (~90 countries) | 19 country packs + international template; names in any language | Partial (19 countries; others use the international pack) |
| Journal entries, posted = immutable, reversal | Yes | Yes, enforced by PostgreSQL triggers; gap-free numbers | Yes |
| Multi-currency, realised FX gain/loss | Yes | Yes, 156 ISO currencies, official / buy / sell rates, automatic daily rates (manual LBP rate never overwritten) | Yes |
| Unrealised FX revaluation at period end | Yes (Enterprise) | Month-end revaluation of cash, banks, AR, AP and foreign-currency accounts, reversed on the 1st | Yes (audit stage) |
| Period lock, fiscal year closing / reopening | Lock dates; closing is manual in Odoo | Period lock + one-click year close to retained earnings, reopen with approval, per-company year start month | Yes |
| Analytic accounting / cost centers | Analytic plans | Cost centers (branch, station, generator, project), auto-tagged, P&L per cost center | Yes |
| Budgets, budget vs actual | Yes (Enterprise) | Monthly budgets per account / cost center | Yes |
| Bank statement import & reconciliation | Yes + bank feeds (Plaid, Ponto…) | CSV import, auto-match ±3 days, quick entries | Partial (no live bank feeds — Lebanese banks offer none) |
| Cheques (post-dated, endorse, bounce) | Basic (via payments) | Full lifecycle with due-date register | Yes (better than Odoo for Lebanon) |
| Fixed assets & depreciation | Yes (Enterprise) | Straight-line or declining balance (factor, switch to straight line), disposal with gain/loss | Yes |
| Recurring entries | Yes | Yes | Yes |
| Financial reports: TB, GL, P&L, BS, cash flow, comparison, drill-down | Yes | Yes, plus an owner summary in plain sentences | Yes |
| Consolidation across companies | Yes (Enterprise) | Consolidated TB / P&L / BS in any currency | Yes |
| Tax returns: VAT report | Per localisation | VAT report per rate; ZATCA phase-1 QR | Partial (no ZATCA phase-2 / Egypt ETA e-invoicing upload) |
| Deferred revenue / prepaid expenses | Yes (Enterprise) | Schedules from an invoice, bill or amount; monthly recognition by the daily worker | Yes (audit stage) |
| Follow-up / dunning letters | Yes (Enterprise) | Levels by days overdue: message, fixed / % late-fee invoice, internet / power cut-off, daily run, history | Yes (audit stage) |
| OCR of supplier bills | Yes (IAP, paid) | No | **No** |

## Subscriptions & billing (the core of the target business)

| Feature | Odoo 19 | Paxve | Status |
|---|---|---|---|
| Plans, fixed monthly and metered (kWh) | Fixed recurring; metered needs custom work | Fixed + metered (base fee + kWh × price), tariffs per month | Yes (stronger than Odoo) |
| Monthly billing run, one click | Yes | Yes, background job with progress | Yes |
| Meter-reading sheets, collector app offline | No | Yes (PWA, offline queue, Bluetooth receipts) | Yes |
| Split payment USD + LBP with change | No | Yes, change rounded to 1,000 / 5,000 LBP | Yes |
| Customer portal: see bills, accept quotes | Yes | Portal link: statement, bills, quotes | Partial (no online card payment — no common Lebanese gateway) |
| Upgrade / downgrade with proration | Yes | Plan can be changed on the subscription; no automatic proration of the current month (use a manual invoice / credit note) | Partial |
| Network automation (cut / restore internet) | No | MikroTik RouterOS PPPoE / hotspot | Yes |
| **Service requests / complaints / faults** | Helpdesk app | **Added in this stage**: numbered tickets per customer, type, priority, assignee, deadline, notes, history, summary | Yes |
| **Generator maintenance by running hours** | Maintenance app (by date / counters) | **Added in this stage**: tasks every N hours and/or N days, due / soon from the hour-meter log, work log with cost | Yes |

## Sales, CRM, purchases

| Feature | Odoo 19 | Paxve | Status |
|---|---|---|---|
| Leads pipeline, activities | Yes | Yes (drag-and-drop stages) | Yes |
| Quotations → orders → deliveries → invoices | Yes | Yes, partial invoicing, quotes accepted on the portal | Yes |
| Price lists, quantity breaks | Yes | Yes, per currency and per customer; **the till now shows the list price live** | Yes |
| Credit notes / returns | Yes | By amount or by line, stock returned at cost, kit components returned | Yes |
| Purchase orders / RFQs before the bill | Yes | RFQ → send (WhatsApp) → confirm → receive & bill in parts; over-billing refused; RFQs from minimum stock | Yes (2-way match; receipt and bill are one step) |
| Supplier price comparison / vendor pricelists | Yes | No | **No** |
| E-commerce website | Yes | No | N/A for now |

## Stock

| Feature | Odoo 19 | Paxve | Status |
|---|---|---|---|
| Warehouses, transfers, adjustments | Yes | Yes | Yes |
| FIFO / average cost | Yes | Per item | Yes |
| Lots / serials / expiry, traceability | Yes | Yes (FEFO picking) | Yes |
| Units of measure | Yes | Purchase units (barrel → litre) | Yes |
| Kits / BoM | Kits + manufacturing | Kits (sale explodes components) | Partial (no manufacturing orders) |
| Reorder rules, replenishment report | Yes | Min / max, days of diesel left | Yes |
| Stock counts | Yes | Count sheet, differences posted | Yes |
| Landed costs | Yes | Yes | Yes |
| Multi-step routes (pick-pack-ship), barcode app | Yes | Single step; barcode in POS | N/A for the target size |

## Point of sale

| Feature | Odoo 19 | Paxve | Status |
|---|---|---|---|
| Touch till, offline, sessions, X/Z | Yes | Yes | Yes |
| Mixed currency payment | Limited | USD + LBP with change | Yes |
| Customer price list on the till | Yes | **Fixed in this stage** (shown live, server-checked) | Yes |
| Kits | Yes | Yes | Yes |
| Loyalty / gift cards / restaurant floor | Yes | No | **No** (not needed by the target businesses) |

## HR & payroll

| Feature | Odoo 19 | Paxve | Status |
|---|---|---|---|
| Employee file, contracts, documents with expiry | Yes | Yes | Yes |
| Attendance (kiosk / phone / devices) | Yes | Phone clock-in with location, fingerprint CSV | Partial (no live device link, no selfie) |
| Time off with approvals and accrual | Yes | Yes (monthly accrual, manager approval) | Partial (no accrual history / carry-over rules per type) |
| Payroll rules per country | Localisations (Enterprise) | Lebanon (NSSF, tax, family, transport), KSA (GOSI, EOSB), UAE (gratuity, WPS) | Partial (values need an accountant's confirmation) |
| Two-currency salary (USD + LBP) | No | Yes | Yes |
| Employee self-service | Yes | Yes; **ready-made "Employee" login created from the employee file in one click** | Yes |
| Expense claims by employees | Yes | No (use expense vouchers) | **No** |
| Recruitment, appraisals, fleet | Yes | No | N/A for now |

## Platform, settings, security

| Feature | Odoo 19 | Paxve | Status |
|---|---|---|---|
| Apps switched on / off | Yes | Yes (12 apps incl. the new Service app) | Yes |
| Languages | ~40 | 11 (8 machine-translated, need native review), per-tenant text overrides | Partial |
| Roles & permissions | Groups / record rules | Module × action permissions, custom roles, company limits, approvals | Yes |
| 2FA, sessions, audit log | Yes | Yes, audit enforced in the database | Yes |
| Multi-company with isolation | Yes | Yes, enforced by PostgreSQL row security on every route | Yes |
| REST API, webhooks | XML-RPC / JSON-RPC | REST + OpenAPI 3.1, signed webhooks | Yes |
| Studio (custom fields / screens) | Yes (Enterprise) | No | **No** |
| Document / attachment on any record | Yes | Attachments on invoices, bills, customers, tickets, POs, journal entries… (10 MB) | Yes (audit stage) |
| Native mobile app | Yes | PWA (collector, POS, self-service) | Partial |

## Closed in the Odoo audit stage (see ODOO_AUDIT.md)

1. Purchase orders / RFQs with partial billing, "on order" in replenishment, RFQs from minimum stock.
2. Dunning levels with late fees and service cut-off (internet and power), daily automatic run.
3. Month-end unrealised exchange revaluation with automatic reversal.
4. Deferred revenue and prepaid expense schedules.
5. Attachments on any document.
6. Declining-balance depreciation.
7. Fixes found by the side-by-side test: goods revenue account, landed cost as the agent's supplier bill, duplicate supplier-invoice check, payment terms.

## Closed in the final stage
1. Service desk app (tickets for cuts, faults, meters, bills, new connections) with history and summary.
2. Generator maintenance plans by running hours / days, with a work log.
3. POS shows the customer's price-list price live.
4. One-click self-service login for an employee with the ready-made "Employee" role.
5. Payroll with a missing exchange rate names the currency and links to Exchange rates.

## Still open (honest list, most useful first)

1. Online payment on the customer portal (needs a Lebanese gateway: Whish, OMT, card acquirer).
2. ZATCA phase 2 and Egypt ETA e-invoice clearance.
3. Employee expense claims; live fingerprint-device link.
4. Custom fields / screen designer (Odoo Studio).
5. Manufacturing orders (kits work; no production orders).
6. Separate receipt step and 3-way match (receipt and bill are one step today); vendor price lists.
7. Proration when a subscription changes plan mid-month.
8. Native review of the 8 machine-translated languages.