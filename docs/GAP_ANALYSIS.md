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

## Closed in the finishing stage (migration 00035)

1. **Online payment on the customer portal**: pluggable gateways per company: Stripe Checkout (signed webhooks), any
   bank / processor *hosted payment page* (HMAC-signed redirect and callback: the adapter for Areeba / MEPS-style 3-D
   Secure pages), and Whish / OMT / bank *transfer reference* confirmed by staff. Idempotent posting into the chosen cash
   box / bank, applied to the invoice. Test mode with a built-in simulator page.
2. **ZATCA phase 2**: secp256k1 key + CSR with ZATCA extensions, compliance CSID (OTP) and production CSID, UBL 2.1
   XML with ICV / PIH chain, invoice hash, XAdES signature, phase-2 QR (tags 1–9), reporting (simplified) / clearance
   (standard). Checked against the ZATCA developer-portal sandbox on 19 Nov 2026: compliance CSID issued; the
   compliance invoice check answered REPORTED, XSD valid, no errors.
3. **Egypt ETA**: document JSON v1.0, ETA canonical serialization + SHA-256, signing through the company's own signing
   service (USB token / HSM: CAdES-BES is made by the token holder), submission with client id / secret (pre-production
   or production).
4. **Expense claims** (self-service with receipt photo → manager / HR approval → booked → reimbursed in cash or with
   the salary); **live fingerprint link**: ZKTeco ADMS push (/iclock/cdata, registered serials, optional IP lock).
5. **Custom fields** on customers, suppliers, items, invoices, employees (text, number, date, choice, yes/no, required,
   AR/FR labels) with a report grid (filter, Excel / CSV export). No drag-and-drop screen designer.
6. **Manufacturing app**: bills of materials, production orders (FIFO / average component cost + labour + overhead,
   scrap units), scrap of any stock item. **Water** as a business type (metered m³ on the same billing engine).
7. **Goods receipts and 3-way matching** (off / warn / block, price tolerance) and **supplier price lists**
   (quantity breaks, validity, supplier codes, lead days) that price purchase orders.
8. **Proration**: start / stop mid-month, plan change with a prorated charge or credit note, change history.
9. HR: **rotating shifts** and roster overrides, **leave accrual history** (monthly / yearly, carry-over,
   adjustments), **clock-in selfie + geofence**, **KSA GOSI phased rates** for Saudis joining after 3 July 2024.
10. Print an invoice in the **customer's language**; deferral schedule names follow the reader's language.
11. Machine translations: obvious accounting-term errors corrected in the 8 extra languages (balance, journal voucher,
    receipt / payment voucher, aging, leave accrual, expense claims…).

## Still open (honest list)

1. A native-speaker review of the 8 machine-translated languages (the obvious errors are fixed, the tone is not).
2. Real merchant accounts: a Lebanese acquirer's exact hosted-page field names (the generic adapter signs
   order|status|amount|currency|ref; a bank with another format needs a small mapping), live Stripe keys.
3. ZATCA production onboarding with the taxpayer's real OTP, and the full compliance-check set (credit / debit notes as
   ZATCA documents are not generated yet: only tax invoices).
4. ETA: the signing service on the taxpayer's computer (Paxve calls it; ETA's own signer or a small token helper).
5. Fingerprint devices: tested with the ADMS protocol, not yet with a physical ZKTeco unit.
6. Screen designer (moving fields around like Odoo Studio), work centers / routings / MRP planning in manufacturing.
7. Goods receipts do not move stock on their own (stock and its cost still come in with the bill; the receipt is the
   control document).