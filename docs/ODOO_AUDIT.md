# Paxve vs Odoo 19: side-by-side audit (2026-11-19)

Odoo 19 Community (`odoo:19`, build 19.0-20261112) runs on a separate audit server, 199.47.240.105:8069. The database is `audit`. The admin password is kept only in `/root/odoo-secrets` on that server.

The installed apps are:
- Invoicing/Accounting with the Lebanese chart `l10n_lb_account`
- Sales, Purchase, Inventory with stock accounting and landed costs
- Point of Sale
- Employees, Attendance, Time Off, Expenses
- CRM, Maintenance, Manufacturing (kits)

The following are Enterprise-only and **not available in Community**:
- Subscriptions
- Helpdesk
- Assets
- Follow-up (dunning)
- Payroll
- Automatic currency revaluation
- Full accounting reports

The same scenario was entered in Odoo, company "Audit LB2", and in a fresh Paxve organisation on paxve.com. Both use the Lebanese chart in USD, with LBP rates of 89,500 on 2026-11-01 and 90,000 on 2026-11-19. Odoo was driven by XML-RPC with the scripts in the scratchpad, Paxve by its REST API.

## Scenario

1. Invoice: installation, 100 USD + 11 % VAT.
2. Invoice in LBP: subscription, 4,475,000 LBP (50 USD).
3. Supplier bill: 1,000 L diesel at 0.80 + 11 % VAT. The item uses FIFO costing.
4. Supplier bill: 10 cables at 10 and 10 breakers at 15. These are the components of a "connection kit".
5. Landed cost: 50 USD freight on bill 3.
6. Supplier bill: 500 L diesel at 0.90.
7. Sale: 1,200 L diesel at 1.20 + VAT, plus 1 kit at 60.
8. Credit note: 100 L returned.
9. Payments on 2026-11-19:
   - 111 USD in cash.
   - 4,475,000 LBP in cash, at the new rate of 90,000.
10. Open LBP invoice: 8,950,000 LBP, used for the month-end revaluation.

## Journal entries, line by line

| Step | Odoo 19 | Paxve | Verdict |
|---|---|---|---|
| Invoice with VAT | AR 111 / revenue 100 / VAT 11 | Same | ✔ match |
| LBP invoice | AR 50 (4,475,000 LBP) / revenue 50 | Same | ✔ match |
| Diesel bill | Stock 800 + VAT 88 / AP 888 | Same | ✔ match |
| Kit components bill | Stock 250 / AP 250 | Same | ✔ match |
| Landed cost 50 | Freight bill expense 50 → reclassified to stock 50; AP owed to the freight supplier | Stock 50 / credit **401 control account, no supplier document** | ✘ **Paxve bug, fixed**. The landed cost can now be booked as the agent's supplier bill, so payables equal the supplier balances. |
| FIFO cost of 1,200 L | 1,030 (1,000 × 0.85 + 200 × 0.90) | 1,030 | ✔ match |
| Kit cost of sale | **nothing booked**; the components left stock but stock value was not reduced | 25 (cable 10 + breaker 15) | ✔ Paxve right. Odoo 19 "perpetual at invoicing" books no COGS for phantom-kit components. |
| Credit note, 100 L | Revenue 120 + VAT 13.20 / AR 133.20; stock back at **88.96** (current product cost) | Same sales lines; stock back at **85.83** (the sale's own unit cost, 1,030 / 1,200) | ≈ Both methods are accepted. Paxve keeps stock value = ledger. |
| LBP payment at 90,000 | Cash 49.72 / AR 49.72; separate exchange entry, loss 0.28 | Cash 49.72 + loss 0.28 / AR 50, in one entry | ✔ same result |
| Revenue account of goods | 701000 Sales of merchandise | **701 Subscription revenue (electricity)** | ✘ **Paxve bug, fixed**. Stock and consumable items now default to the "sales of goods" account (707 / 4100). |
| Month-end revaluation of the open LBP invoice | Enterprise only | **Missing** | ✘ **Gap, built.** The result is a loss of 0.56 on 2026-11-30, reversed on 12-01. |

### Trial balance after the scenario (USD)

| | Odoo | Paxve | Difference |
|---|---|---|---|
| Receivables | 1,625.20 | 1,625.20 | – |
| Payables | 1,638.00 | 1,638.00 | – |
| VAT recoverable / collected | 88.00 / 156.20 | 88.00 / 156.20 | – |
| Revenue | 1,630.00 | 1,630.00 | – |
| Inventory | 608.96 | 580.83 | Kit 25 + return valuation 3.13 (see above) |
| Cost of sales | 941.04 | 969.17 | The same two items |
| Cash received | 160.72 (outstanding receipts) | 160.72 (cash USD + cash LBP) | – |

The Paxve stock valuation report is 580.83:
- diesel: 400 L = 355.83;
- cables: 9 = 90;
- breakers: 9 = 135.

It equals the ledger. Odoo's ledger (608.96) does not match its own stock on hand, because the kit components were never costed.

Not compared in Odoo:
- POS: needs a session through the UI.
- Asset depreciation, payroll and year close: Enterprise only, or no closing in Community.

Paxve's figures for these were checked by hand:
- straight line 12,000 / 60 = 200 per month;
- double declining 400, then 386.67;
- the year close was tested in the final stage.

## Usability

| Task | Odoo 19 | Paxve (after this stage) |
|---|---|---|
| Buy stock | 3 documents: RFQ → order → receipt → bill | Direct bill (1 screen). New optional RFQ → confirm → "Receive & bill" in one step. |
| Duplicate supplier invoice | Warns on the same vendor reference | **Was missing.** Now refused (409) and the screen asks before booking it twice. |
| Due dates | Payment terms on partner / company | **Was missing.** Default days per company, plus per customer or supplier. |
| Overdue customers | Follow-up levels (Enterprise) | **Was:** reminders + internet cut only. **Now:** levels with message, late fee (fixed / %), cut-off for internet *and* power subscriptions, a daily automatic run, and history. |
| Documents | Attachments on every record (chatter) | **Was:** employee files only. **Now:** invoices, bills, customers, tickets, POs, expenses, journal entries and more. |
| Prepaid yearly subscriptions | Deferred revenue (Enterprise) | **Built.** A schedule from an invoice in 1 click; months recognised automatically. |
| Declining depreciation | Enterprise | **Built.** |
| Replenishment | Shows quantities already on order | **Built.** "On order" counts open POs, and RFQs can be created from minimum stock per supplier. |
| Two-currency collection, collectors, meters, MikroTik cut | Not available | Paxve only |

## What Paxve does better (kept)

- USD + LBP split payments with change.
- Offline collector app and meter sheets.
- Metered generator billing.
- Router cut and restore.
- Cheque lifecycle.
- Owner summary in plain words.
- Stock value always equals the ledger, kits included.
- Company isolation enforced by the database.
