# Paxve API — quick start

Base URL: `https://paxve.com/api/v1` · Full spec: `https://paxve.com/api/v1/openapi.yaml` (import into Postman / Insomnia / Swagger).

## Authentication

An organisation admin creates a key in **Settings → Developers (API)** and chooses its role
(`read_only`, `data_entry`, `accountant`, `admin`). The key is shown once. Send it as a bearer token:

```sh
export PAXVE_KEY=pxv_xxxxxxxx_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
curl -s https://paxve.com/api/v1/me -H "Authorization: Bearer $PAXVE_KEY"
```

A key only ever sees its own organisation. Revoked keys stop working immediately.

## Conventions

- **Money** is a decimal string (`"125.5000"`), never a JSON number.
- **Dates** are `YYYY-MM-DD`; months (billing periods) are `YYYY-MM`.
- **Exchange rates**: `rate` = units of the currency for **1 unit of the company base currency**
  (base USD, LBP `89500` → 1 USD = 89,500 LBP).
- **Errors**: `{"error": "code", "detail": "…"}` with HTTP 4xx. Common codes: `unbalanced`, `posted_locked`,
  `period_locked`, `no_rate`, `insufficient_payment`, `plan_limit`, `forbidden`, `not_found`.
- Most resources live under a company: `/companies/{cid}/…`. Get the ids with `GET /companies`.

## Examples

```sh
H="Authorization: Bearer $PAXVE_KEY"; J="Content-Type: application/json"
CID=$(curl -s https://paxve.com/api/v1/companies -H "$H" | jq -r '.[0].id')

# today's LBP rate
curl -s -X POST https://paxve.com/api/v1/companies/$CID/rates -H "$H" -H "$J" \
  -d '{"currency":"LBP","rate":"89500"}'

# customers with unpaid bills
curl -s "https://paxve.com/api/v1/companies/$CID/customers?due=1" -H "$H"

# create a customer
curl -s -X POST https://paxve.com/api/v1/companies/$CID/customers -H "$H" -H "$J" \
  -d '{"name":"Ahmad","phone":"03123456"}'

# record a payment: 20 USD + 2,500,000 LBP on a 45 USD balance, change given from the LBP box
curl -s -X POST https://paxve.com/api/v1/companies/$CID/payments -H "$H" -H "$J" -d '{
  "customer_id":"<customer id>", "currency":"USD", "amount":"45",
  "tenders":[{"method_id":"<Cash USD id>","amount":"20"},{"method_id":"<Cash LBP id>","amount":"2500000"}],
  "change_method_id":"<Cash LBP id>", "client_ref":"my-system-000123"}'
```

`client_ref` makes a payment **idempotent**: sending the same request again (after a timeout, from an offline
device…) returns the first payment instead of creating a second one.

```sh
# monthly bills for November
curl -s -X POST https://paxve.com/api/v1/companies/$CID/billing/run -H "$H" -H "$J" -d '{"period":"2026-11"}'

# large companies: more than 1,500 subscriptions (or "async":true) returns 202 with a background job
curl -s https://paxve.com/api/v1/companies/$CID/billing/jobs/<job id> -H "$H"   # status, processed / total, created, totals

# credit note on an invoice (empty amount = everything not yet credited), optional immediate refund
curl -s -X POST https://paxve.com/api/v1/companies/$CID/invoices/<id>/credit-note -H "$H" -H "$J" -d '{"amount":"40","reason":"discount"}'
# pay a customer's credit back, and a supplier debit note (return goods)
curl -s -X POST https://paxve.com/api/v1/companies/$CID/customers/<id>/refunds -H "$H" -H "$J" -d '{"currency":"USD","amount":"20","method_id":"<cash box>"}'
curl -s -X POST https://paxve.com/api/v1/companies/$CID/bills/<id>/debit-note -H "$H" -H "$J" -d '{"amount":"55.5"}'

# stock: transfer between warehouses, void a fuel / adjustment document, movements report
curl -s -X POST https://paxve.com/api/v1/companies/$CID/stock/transfers -H "$H" -H "$J" -d '{"item_id":"<id>","from_warehouse_id":"<id>","to_warehouse_id":"<id>","quantity":"400"}'
curl -s -X POST https://paxve.com/api/v1/companies/$CID/stock/docs/<id>/void -H "$H" -H "$J" -d '{}'
curl -s "https://paxve.com/api/v1/companies/$CID/reports/stock-moves?from=2026-11-01&to=2026-11-30" -H "$H"

# any table to Excel: {title, columns:[{label,numeric}], rows:[[...]], footer:[[...]]} → .xlsx
curl -s -X POST https://paxve.com/api/v1/export/xlsx -H "$H" -H "$J" -o report.xlsx -d '{"title":"Test","columns":[{"label":"A"}],"rows":[["x"]]}'

# sales: quotation -> confirm (sales order) -> deliver (stock out, cost of sales) -> invoice (all or per line)
curl -s -X POST https://paxve.com/api/v1/companies/$CID/sales/orders -H "$H" -H "$J" -d '{"customer_id":"<id>","currency":"USD",
  "lines":[{"item_id":"<router>","quantity":"3","unit_price":"60","discount_pct":"5"},{"description":"Installation","quantity":"1","unit_price":"25","tax_rate_id":"<vat>"}]}'
curl -s -X POST https://paxve.com/api/v1/companies/$CID/sales/orders/<id>/confirm -H "$H" -H "$J" -d '{}'          # also send | cancel | reopen | done
curl -s -X POST https://paxve.com/api/v1/companies/$CID/sales/orders/<id>/deliver -H "$H" -H "$J" -d '{"lines":[{"line_id":"<id>","quantity":"2"}]}'
curl -s -X POST https://paxve.com/api/v1/companies/$CID/sales/orders/<id>/invoice -H "$H" -H "$J" -d '{}'          # {} = everything not yet invoiced
curl -s -X POST https://paxve.com/api/v1/companies/$CID/sales/deliveries/<id>/void -H "$H" -H "$J" -d '{}'

# customer statement (opening balance, movements, running balance, closing per currency) and bulk statements
curl -s "https://paxve.com/api/v1/companies/$CID/customers/<id>/statement?from=2026-11-01&to=2026-11-30" -H "$H"
curl -s "https://paxve.com/api/v1/companies/$CID/statements?from=2026-11-01&to=2026-11-30&only_balance=1" -H "$H"

# send a document to the customer: kind quote | order | invoice | statement; channel whatsapp | sms | email | link (text + wa.me link only)
curl -s -X POST https://paxve.com/api/v1/companies/$CID/send-document -H "$H" -H "$J" -d '{"kind":"invoice","id":"<invoice id>","channel":"email","locale":"fr"}'

# CRM: stages, leads (pipeline), activities / notes, lead -> customer
curl -s https://paxve.com/api/v1/companies/$CID/crm/leads?status=open -H "$H"
curl -s -X POST https://paxve.com/api/v1/companies/$CID/crm/leads -H "$H" -H "$J" -d '{"title":"Office network","organisation":"Cedar Clinic","phone":"03111222","expected_revenue":"500","currency":"USD"}'
curl -s -X POST https://paxve.com/api/v1/companies/$CID/crm/activities -H "$H" -H "$J" -d '{"lead_id":"<id>","kind":"call","summary":"Call back","due_date":"2026-11-20"}'
curl -s -X POST https://paxve.com/api/v1/companies/$CID/crm/leads/<id>/convert -H "$H" -H "$J" -d '{}'

# credit note by line: only the goods of these lines come back to stock
curl -s -X POST https://paxve.com/api/v1/companies/$CID/invoices/<id>/credit-note -H "$H" -H "$J" -d '{"lines":[{"line_no":2,"quantity":"1"}]}'

# point of sale: register (till), session, sale, return, close (cashier role: only these endpoints)
curl -s -X POST https://paxve.com/api/v1/companies/$CID/pos/registers -H "$H" -H "$J" -d '{"name":"Shop till","currency":"USD","method_ids":["<cash USD>","<cash LBP>"]}'
curl -s "https://paxve.com/api/v1/companies/$CID/pos/catalog?register_id=<id>" -H "$H"   # items + prices in the till currency, categories, boxes, rates
curl -s -X POST https://paxve.com/api/v1/companies/$CID/pos/sessions -H "$H" -H "$J" -d '{"register_id":"<id>","opening":[{"method_id":"<cash USD>","amount":"50"}]}'
curl -s -X POST https://paxve.com/api/v1/companies/$CID/pos/orders -H "$H" -H "$J" -d '{"session_id":"<id>","client_ref":"pxv-0001abcd",
  "lines":[{"item_id":"<id>","quantity":"2"}],"tenders":[{"method_id":"<cash LBP>","amount":"500000"}],"change_method_id":"<cash LBP>"}'
curl -s -X POST https://paxve.com/api/v1/companies/$CID/pos/orders/<id>/refund -H "$H" -H "$J" -d '{"session_id":"<id>","lines":[{"line_no":1,"quantity":"1"}],"method_id":"<cash USD>"}'
curl -s https://paxve.com/api/v1/companies/$CID/pos/sessions/<id> -H "$H"     # X / Z report: drawer per box (opening, in, out, expected), sales, tax, items
curl -s -X POST https://paxve.com/api/v1/companies/$CID/pos/sessions/<id>/close -H "$H" -H "$J" -d '{"counted":[{"method_id":"<cash USD>","amount":"48"}]}'
curl -s "https://paxve.com/api/v1/companies/$CID/reports/pos?from=2026-11-01&to=2026-11-30&group=product" -H "$H"   # day|session|cashier|register|product|method

# a balanced journal entry, posted immediately
curl -s -X POST https://paxve.com/api/v1/companies/$CID/journal -H "$H" -H "$J" -d '{
  "date":"2026-11-18", "memo":"Owner contribution", "post":true,
  "lines":[{"account_id":"<cash>","debit":"1000"},{"account_id":"<capital>","credit":"1000"}]}'

# reports
curl -s "https://paxve.com/api/v1/companies/$CID/reports/trial-balance?from=2026-01-01&to=2026-12-31" -H "$H"
curl -s "https://paxve.com/api/v1/companies/$CID/reports/aging" -H "$H"
```

## Permissions, keys and security

Every endpoint needs one permission `module.action` (see `GET /permissions`). Built-in roles are fixed
permission sets; an admin can make **custom roles** (Users → Roles & permissions) and limit users to some
companies. A request without the permission gets `403 forbidden`; a company you may not open answers `404`.

An API key acts with its role, optionally narrowed to **scopes** (a list of permissions), and never with more
than the person who created it can do today:

```sh
curl -s -X POST https://paxve.com/api/v1/api-keys -H "$J" -b cookies.txt \
  -d '{"name":"Website","role":"accountant","scopes":["customers.view","collections.view"]}'
```

When a company has **approval rules** (voids, credit notes, payments out above an amount), such a call by
someone without `<module>.approve` is stored and answers `202 {"approval_required":true,"approval_id":"…"}`;
it is done when an approver calls `POST /companies/{cid}/approvals/{id}/approve`.

## Webhooks

Register an URL per company (Developers page, or `POST /companies/{cid}/webhooks` with a session or an API key that holds
`api.create`; `api.view` lists them and their deliveries) for the events
`invoice.created`, `payment.created`, `customer.created`, `pos.order.created`. Paxve POSTs JSON:

```json
{"id":"…","event":"payment.created","created_at":"2026-11-18T10:00:00Z","company_id":"…","data":{ "...the record..." }}
```

Headers: `X-Paxve-Event`, `X-Paxve-Delivery` (unique id, use it to ignore duplicates) and
`X-Paxve-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>.<raw body>")>`.
Answer 2xx within 15 s. Failures are retried 8 times (2, 4, 8 … 128 minutes). Addresses on private networks
are refused.

```python
import hmac, hashlib, time
def verify(secret, header, body):
    t, v1 = [p.split("=", 1)[1] for p in header.split(",")]
    mac = hmac.new(secret.encode(), f"{t}.".encode() + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(mac, v1) and abs(time.time() - int(t)) < 300
```

## Rules the API cannot bypass

These are enforced inside PostgreSQL, so they hold for the API, the web app and any future client:
balanced and immutable journal entries, period lock, no deletion of issued documents (void = reversal),
invoices never paid above their total, stock never negative, tenant isolation (row-level security) **and company
isolation**: under `/companies/{cid}/…` only that company's rows exist for the query, and ids of another company
(in the path or in the body) answer `404 not_found` — also for users limited to some companies.

## Paging and large companies

- Lists take `limit` / `offset`: customers (100, max 1000), invoices and payments (200, max 2000),
  journal (100, max 500; also `from` / `to` / `status=draft|posted`).
- `GET /companies/{cid}/statements` returns 100 customers per call (`limit` up to 2000, `offset`) and `has_more`.
- `GET /companies/{cid}/reports/aging` returns the 500 largest balances (`limit` up to 5000, `offset`), `has_more`;
  `totals` always cover everyone.
- `GET /audit` takes `limit` (max 500), `before=<id>` (older page), `company_id`, `entity_id`; each entry has
  `company_id`, `approved_by` (name) and `approval_id` when the change came from an approved request.
- The dashboard's `summary_at` says when its counts and charts were computed (kept up to a minute for very large companies).

## Approvals

A call that needs approval answers `202 {"approval_required":true,"approval_id":…,"kind":…}` (kinds: `void`,
`credit_note`, `refund`, `payment`). Rules per company: `PUT /companies/{cid}/approval-rules`
`{"void":true,"credit_note":true,"refunds":true,"payment_over":"500"}` (amount in the base currency; a payment given
only as `paid_amount` from a cash box is converted from that box's currency). On approval the stored call runs **as
the requester** — their permissions and company access are checked again at that moment — and the audit log keeps
the approver.

## Professional accounting (cheques, exchange, years, cost centers, budgets)

```sh
# a customer cheque (pays their bills at once), then deposit, collect / bounce
curl -s -X POST $API/companies/$CID/cheques -H "$H" -d '{"direction":"in","cheque_no":"123456","bank_name":"BLOM","receive_date":"2026-11-02","due_date":"2026-12-10","currency":"USD","amount":"120","customer_id":"<customer>"}'
curl -s -X POST $API/companies/$CID/cheques/<id>/deposit -H "$H" -d '{"method_id":"<bank box>"}'
curl -s -X POST $API/companies/$CID/cheques/<id>/collect -H "$H" -d '{}'          # or /bounce, /endorse {supplier_id}, /partial_cash {method_id, amount}, /undo
# sell 100 USD for 9,000,000 LBP: both sides at the official rate, the difference is booked as exchange gain / loss
curl -s -X POST $API/companies/$CID/exchanges -H "$H" -d '{"from_method_id":"<cash USD>","to_method_id":"<cash LBP>","from_amount":"100","to_amount":"9000000"}'
# close / reopen a fiscal year (reopen needs accounting.approve)
curl -s -X POST $API/companies/$CID/fiscal-years/close -H "$H" -d '{"year":2025}'
# cost centers, budget, reports
curl -s -X POST $API/companies/$CID/cost-centers -H "$H" -d '{"code":"N","name":"North station","kind":"station","station_id":"<station>"}'
curl -s -X PUT  $API/companies/$CID/budgets -H "$H" -d '{"year":2026,"lines":[{"account_id":"<615>","month":11,"amount":"20"}]}'
curl -s "$API/companies/$CID/reports/budget?year=2026" -H "$H"
curl -s "$API/companies/$CID/reports/cost-centers?from=2026-01-01&to=2026-12-31" -H "$H"
curl -s "$API/companies/$CID/reports/cash-flow?from=2026-01-01&to=2026-12-31" -H "$H"
curl -s "$API/companies/$CID/reports/compare?kind=pl&p=2026-11-01:2026-11-30&p=2025-11-01:2025-11-30" -H "$H"
curl -s "$API/companies/$CID/journal/<entry>/source" -H "$H"          # the document behind an entry (drill-down)
curl -s "$API/companies/$CID/owner-summary" -H "$H"
```

- Journal lines carry an optional `cost_center_id` (manual entries accept it; documents tag income / expense lines from the
  station or generator they belong to). `PATCH /companies/{cid}/journal-lines/{id} {"cost_center_id": ""}` re-tags a posted
  line — the only change allowed on a posted entry, and it never touches an amount.
- Profit & loss, budget, comparison and cost-center reports leave the year-closing entries out, so a closed year still
  shows its real income and expenses; the balance sheet and trial balance include them.
- A payment created by a cheque can only be undone from the cheque (`409 cheque_payment` on `/payments/{id}/void`).
- `POST /payments` accepts `invoice_id` to settle that bill first (barcode collection).

## Module T endpoints

- `GET/PUT /companies/{cid}/rates/settings`, `POST /companies/{cid}/rates/fetch`; reports accept `?currency=XXX`.
- `GET /translations?lang=xx`, `PUT /translations {lang, key, value}`.
- `PATCH /companies/{cid}` also takes `default_locale`, `tax_id_label`, `invoice_title`, `legal_mentions`; customers take `locale`.
- Stock: `GET /items/{id}/extras`, `PUT /items/{id}/stock-settings {costing: avg|fifo, tracking: none|lot|serial}`,
  `PUT /items/{id}/units {units:[{name,factor}]}`, `PUT /items/{id}/components {components:[{component_id,quantity}]}`,
  `GET /items/{id}/price?customer_id&quantity&currency`, `GET /stock/lots`, `GET /stock/lots/{id}/trace`,
  `PUT /stock/reorder-rules`, `GET /reports/stock-replenishment`, `GET|POST /stock/counts`, `GET /stock/counts/{id}`,
  `PUT /stock/counts/{id}/lines`, `POST /stock/counts/{id}/post|cancel`, `GET|POST /bills/{id}/landed-costs`,
  `GET|POST /price-lists`, `PATCH /price-lists/{id}`, `GET|PUT /price-lists/{id}/items`, `PUT /customers/{id}/price-list`.
- Bill lines accept `unit_id` (quantity and price per that unit) and `lots: [{lot_no, expiry, quantity}]`.
## Module U: HR & payroll endpoints

All under `/api/v1/companies/{cid}` (permission in brackets).

| Route | |
|---|---|
| `GET/PUT /employees/{id}/file` (payroll.view / edit) | HR file: personal data, contract, second salary part, components, documents list, leave balances, recent attendance |
| `POST /employees/{id}/documents` · `GET/DELETE …/documents/{did}` | base64 upload (≤ 5 MB), download, delete |
| `GET /employees/{id}/end-of-service?date=&reason=` | gratuity by the country rule |
| `GET/POST /hr/schedules`, `PUT /hr/schedules/{id}`, `GET/POST /hr/holidays`, `DELETE /hr/holidays/{id}` | schedules and public holidays |
| `GET /hr/attendance?date=` · `PUT /hr/attendance` · `POST /hr/attendance/import {csv, dry_run}` | day sheet, manual correction, fingerprint CSV import |
| `GET/POST /hr/leave-types`, `PUT /hr/leave-types/{id}`, `GET/POST /hr/leave`, `POST /hr/leave/{id}/decide {decision, note}`, `POST /hr/leave/{id}/cancel`, `GET /hr/leave/balances?year=` | leave |
| `GET/POST /hr/payroll-params` (payroll.view / approve) | rules in force on a date, dated overrides, WPS employer id |
| `POST /payroll/{id}/recalculate`, `/approve`, `/unapprove` | draft workflow |
| `GET /payroll/{id}/payslips/{pid}` · `GET /payroll/{id}/wps` | payslip with lines, UAE SIF file |
| `GET /reports/hr/{register,social,tax,cost,attendance,leave}?period=&group=` | HR reports |
| `GET /self`, `POST /self/clock`, `POST /self/leave`, `POST /self/leave/{id}/cancel`, `GET /self/payslips/{pid}`, `GET /self/team-leave` (any signed-in user linked to an employee) | self-service |
## Final stage: service desk, maintenance, employee login, till price list

All under `/companies/{cid}`. The service routes belong to the `svc` app (403 `app_disabled` when it is off).

| Endpoint | Purpose |
|---|---|
| `GET /tickets?status=open\|all\|new\|in_progress\|waiting\|done\|cancelled&customer_id=&q=` (service.view) | service requests, urgent first |
| `POST /tickets {customer_id?, subscription_id?, subject, category, priority, channel, contact_phone, description, assigned_to, due_at}` (service.create) | new numbered request; category one of outage, no_internet, slow, no_signal, meter, billing, new_connection, move, disconnect, complaint, other |
| `GET /tickets/{id}` · `PATCH /tickets/{id}` (service.edit) · `POST /tickets/{id}/notes {body}` (service.create) | details with notes; status changes are written as notes; `resolved_at` is set on done / cancelled |
| `GET /tickets/summary` | open, overdue, urgent, by category, last 30 days opened / closed / average hours |
| `GET /maintenance` (service.view) | generator maintenance tasks with `hours_now`, `due_hours`, `due_date`, `hours_left`, `days_left`, `state` (due / soon / ok / off) |
| `POST /maintenance/tasks {generator_id, task, every_hours?, every_days?, last_done_hours?, last_done_date?}` · `PATCH /maintenance/tasks/{id}` (service.manage) | maintenance plan |
| `GET /maintenance/log?generator_id=` · `POST /maintenance/log {plan_id \| generator_id + task, done_date, hour_meter?, cost?, currency?, notes?}` (service.create) | work done; a plan's last-done moves forward |
| `POST /employees/{id}/login {email, locale}` (users.create + payroll.edit) | creates the employee's self-service user with the ready-made "Employee (self-service)" role, links it, returns a one-time `password`; 409 `already_linked` |
| `GET /pos/price-list?customer_id=&currency=` (pos.view) | the customer's active price list in the till currency (`name`, `items[] {item_id, min_qty, price}`) |

## Finishing stage endpoints (migration 00035)

| Area | Endpoints |
|---|---|
| Proration | GET/PUT /companies/{cid}/billing/proration · POST /subscriptions/{id}/change-plan {plan_id, date} · POST /subscriptions/{id}/stop {date} · GET /subscriptions/{id}/changes |
| Receipts, 3-way match | POST /purchase-orders/{id}/receive {date, delivery_ref, lines:[{po_line_id, quantity}]} · GET /purchase-orders/{id}/match · POST /goods-receipts/{id}/cancel · GET/PUT /purchasing/matching {mode: off\|warn\|block, tolerance}; POST /purchase-orders/{id}/bill answers warnings |
| Supplier prices | GET/POST /supplier-prices · PUT/DELETE /supplier-prices/{id} · GET /supplier-prices/best?supplier_id&item_id&currency&qty&date; a PO line with an empty unit_price is priced from the list |
| Expense claims | GET/POST /self/claims · POST /self/claims/{id}/cancel · GET/POST /hr/claims · POST /hr/claims/{id}/decide {decision, account_id, pay_via, note} · POST /hr/claims/{id}/pay {method_id, date} |
| Custom fields | GET/POST /custom-fields · PUT /custom-fields/{id} · GET /custom-fields/report?entity&key&value · GET/PUT /custom-values/{entity}/{id} {values} (entity: customers, suppliers, items, invoices, employees) |
| Manufacturing | GET/POST /mfg/boms · GET/PUT /mfg/boms/{id} · GET/POST /mfg/orders · GET /mfg/orders/{id} · POST /mfg/orders/{id}/produce {scrap_qty, lines} · POST /mfg/orders/{id}/cancel · POST /mfg/scrap |
| HR | GET/POST /hr/rotations · PUT /hr/rotations/{id} · POST /hr/rotations/{id}/assign · GET/PUT /hr/roster · POST /hr/leave/accrue {period} · POST /hr/leave/adjust · GET /hr/leave/accruals · GET/PUT /hr/clock-settings · GET /hr/attendance/{id}/photo · GET/POST /hr/devices · PATCH /hr/devices/{id}; POST /self/clock takes photo (base64) |
| Fingerprint push (public) | GET /iclock/cdata?SN= (handshake) · POST /iclock/cdata?SN=&table=ATTLOG · GET\|POST /iclock/getrequest, /iclock/devicecmd (also on plain HTTP port 80) |
| Online payments | GET/POST /payment-gateways · PUT /payment-gateways/{id} · GET /payment-intents?status= · POST /payment-intents/{id}/confirm\|reject; public: GET /portal/{token}/pay-options · POST /portal/{token}/invoices/{id}/pay {gateway_id, reference, phone} · POST /pay/webhook/{gid} (Stripe-Signature, or HMAC signature over order\|status\|amount\|currency\|ref) · GET/POST /pay/sim/{token} (test mode) |
| E-invoicing | GET/PUT /einvoice/config?kind=zatca\|eta · POST /einvoice/zatca/onboard {otp \| production: true} · POST /invoices/{id}/einvoice {submit} · GET /invoices/{id}/einvoice[?xml=1] |

## Platform console (migration 00036)

Only for the addresses in `PLATFORM_ADMINS` (browser session, never an API key); everyone else gets 403.

| Endpoint | What it does |
|---|---|
| GET /platform/summary | counts per state (trial, active, grace, expired, blocked), MRR / ARR, sign-ups this month, ending in 7 days, lost in 30 days, open requests, money received per month |
| GET /platform/tenants · GET /platform/tenants/{id} | every organisation with state, plan, ends_on, price, usage, country, last activity; the detail adds users (last sign-in), companies, usage, payments, events, requests |
| PATCH /platform/tenants/{id} | name, plan (brings its limits), trial_ends_at, paid_until, billing_cycle, price_override / price_currency, grace_days, max_subscribers / max_users / max_companies (0 = unlimited), contact_phone / contact_email, notes, is_active, grandfathered_apps ([] removes the kept apps) |
| POST /platform/tenants/{id}/renew | {period: month\|year\|custom, until?, plan?, amount?, currency?, method: cash\|whish\|omt\|bank\|card\|other, reference?, note?, request_id?} records a payment, extends paid_until from the current end (or today), lifts a block, closes the request |
| POST /platform/tenants/{id}/block {reason} · /unblock · /extend-trial {days\|until} · /reset-password {user_id?} | block ends open sessions and sign-in answers 403 org_blocked with the reason; reset returns a one-time password |
| POST /platform/tenants/{id}/support · POST /platform/support/end | read-only support view as the organisation's admin (2 hours at most, every write answers 403 support_read_only, audited) |
| PATCH /platform/tenants/{id}/users/{uid} {is_active} | switch one user on / off |
| GET /platform/payments?from&to · GET/POST /platform/plans · PATCH /platform/plans/{code} {…, apps, apply_limits} | payments received; editable plans, prices, limits and apps ([] = every app); narrowing a plan's apps keeps the apps each organisation already uses (grandfathered_apps) |
| GET /platform/setup | the messaging providers of the owner's own company (they send the renewal reminders; `log` only writes them to the server log) and PAXVE_SALES_PHONE / PAXVE_SALES_EMAIL |
| GET /platform/requests?status= · POST /platform/requests/{id}/done\|reject {answer} | renewal / upgrade requests sent by organisations |
| POST /platform/lifecycle/run | run the daily worker now (state changes, reminders 7 / 3 / 1 days before and on expiry) |

Organisation side: `GET /subscription` (plan, state, end date, usage against limits, payments, requests, plans offered),
`PATCH /subscription {contact_phone}` and `POST /subscription/requests {kind: renew|upgrade, plan, cycle, method, reference, note}`
(settings.manage). `GET /me` carries `sub_state`, `paid_until`, `grace_ends` and `support`. While the state is **expired** every
write answers **402 subscription_expired** (sign-out, profile, security and the renewal request still work); **blocked**
organisations cannot sign in.

**Plan apps (migration 00037).** `GET /me` carries `plan_apps` (null = every app): the plan's apps plus the organisation's
kept (grandfathered) apps; the core apps (gen, acc, col) are always allowed. A route of an app outside the plan answers
**403 app_not_in_plan**, and `PUT /companies/{cid}/apps` refuses to switch one on (403 app_not_in_plan, `detail` = the app;
an app that is already on may stay in the list). Moving an organisation to a smaller plan does not keep apps; only
narrowing a plan's list does.

**Background jobs.** Expired and blocked organisations run nothing on their own: billing jobs, dunning, recurring
entries, deferrals, leave accruals, automatic rates, ISP rules and router actions, queued messages and webhook deliveries
are skipped (`tenant_jobs_ok()`); queued work waits and goes out after a renewal or unblock. The state change is
recorded with `jobs: paused / resumed`, and the daily run logs how much is held. The platform owner's own organisation
is never skipped.

## Retail core (variants, promotions, loyalty, gift cards, scales, labels, packs)

All under `/api/v1/companies/{cid}`.

| Method | Path | Notes |
|---|---|---|
| GET | `/items/templates` | templates with their variant count and stock |
| PUT | `/items/{id}/variant-setup` | `{attributes: [{name, values}], attr_extras: {"Size=XL": "2"}}` (no stock on the item) |
| POST | `/items/{id}/variants/generate` | `{barcodes?: bool}` creates the missing combinations (internal EAN-13 under the variant prefix) |
| GET / PUT | `/items/{id}/variants` | variants with stock per warehouse / bulk `{variants: [{id, sale_price, barcode, is_active}]}` |
| PUT | `/items/{id}/retail` | `{plu, sold_by_weight, brand, content_qty, content_unit}` |
| GET / POST / PUT | `/retail/promotions[/{id}]` | kinds `discount`, `bxgy`, `tiers`, `bundle`, `basket`; scope, dates, days, hours, tills, coupon, stackable, priority, max_uses |
| POST | `/retail/promotions/evaluate` | `{currency, register_id?, customer_id?, coupon?, lines: [{item_id, quantity, amount}]}` → discount per line, applied promotions |
| GET / PUT | `/retail/loyalty` | program: `earn_per`, `point_value`, `min_redeem`, `expiry_months`, `tiers [{name, min_points, multiplier}]` |
| GET | `/retail/loyalty/lookup?code=` | card number, customer code or phone → points, tier, value |
| GET | `/retail/loyalty/members?q=` | members |
| PUT | `/retail/loyalty/customers/{id}` | `{card: "auto" \| "<number>" \| ""}` |
| POST | `/retail/loyalty/customers/{id}/adjust` | `{points: "+50" \| "-20", note}` (booked) |
| GET | `/retail/loyalty/customers/{id}/moves` | points history |
| GET / POST | `/retail/gift-cards` | list with outstanding totals / free voucher `{amount, currency, code?, customer_id?, expires_on?, note}` |
| GET | `/retail/gift-cards/lookup?code=` | balance, status, expiry |
| POST | `/retail/gift-cards/{id}/void` | releases the unused balance |
| GET | `/retail/gift-cards/{id}/moves` | card history |
| GET / PUT | `/retail/scale` | `{prefixes: [{prefix: "21", mode: "weight" \| "price", decimals}], variant_barcode_prefix}` |
| GET | `/retail/scale/plu?currency=` | PLU items and a CSV for the scale software |
| GET | `/retail/labels?ids=a,b&currency=` | price incl. VAT, promotion price, unit price per kg / L, barcode |
| GET | `/retail/expiry?days=30` · POST `/retail/expiry/markdown` | near-expiry lots with a suggested % · `{item_id, pct, days}` creates a dated promotion |
| GET | `/reports/retail?from&to&currency` | by category, by hour (till), margin by item, promotions given |
| GET / POST | `/retail/packs` · `/retail/packs/{supermarket\|clothing}` | `{demo: bool}` apps, business type, loyalty defaults, demo data |
| GET / POST / DELETE | `/pos/held[/{id}]` | held baskets per till (`?register_id=`) |
| POST | `/pos/quick-item` | `{register_id, name, price, barcode?, category_id?}` a non-stock item made at the till |

POS sale (`POST /pos/orders`) also takes `apply_promos`, `coupon`, `gift_sold [{code?, amount}]`, `gift_redeem [{code, amount}]`
and `redeem_points`; the order returns `promos`, `loyalty {earned, redeemed, balance}` and `gift`. Session close takes
`denominations {method_id: {"100": 3}}`. Gift card / points tenders (`payment-methods?all=1`, kinds `gift` and `loyalty`)
are refused outside the till.