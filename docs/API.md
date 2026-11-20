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

## Warehouse pro (migration 00041)

All under `/companies/{cid}`; app **stk** (dropship: **pur**). Bins are physical: stock value never moves here.

| Method | Path | Notes |
|---|---|---|
| GET · PATCH | `/wh/warehouses` · `/wh/warehouses/{id}` | `{name, code, address, use_bins, reception_steps 1-3, delivery_steps 1-3, removal fifo\|fefo\|closest, crossdock, is_active}`; steps need bins |
| GET · POST · PATCH | `/wh/locations?warehouse_id=` · `/wh/locations` · `/wh/locations/{id}` | `{warehouse_id, parent_id, kind zone\|aisle\|shelf\|bin\|input\|qc\|output\|pack, code, name, barcode}` |
| POST | `/wh/locations/generate` | `{warehouse_id, zone, aisles, shelves, bins}` → zone Z, aisles Z-01, shelves Z-01-1, bins Z-01-1-01 |
| GET · POST · DELETE | `/wh/putaway-rules[/{id}]` | `{warehouse_id, item_id \| category_id, location_id, priority}` |
| GET | `/wh/bin-stock?warehouse_id&location_id&item_id&q` | `{bins, unbinned}` |
| POST | `/wh/move` | `{warehouse_id, item_id, lot_id?, quantity, from_location_id?, to_location_id?}` (empty = unbinned); recorded as a done transfer |
| POST | `/wh/count` | `{warehouse_id, location_id, lines [{item_id, lot_id?, counted}]}` physical count of a bin; returns `warnings` when bins exceed the books |
| GET | `/wh/scan?code=` | `{type: location\|item\|lot\|package\|op\|unknown, data}` (`PKG000012`, `OP000034`, unit barcodes give `factor`) |
| GET · POST | `/wh/ops?status=open\|ready\|done\|all&kind&warehouse_id&order_id` · `/wh/ops` | tasks; POST makes an internal transfer `{warehouse_id, lines [{item_id, quantity, from_location_id, to_location_id}]}` |
| GET · POST | `/wh/ops/{id}` · `/wh/ops/{id}/done` · `/wh/ops/{id}/cancel` | done `{date?, lines? [{line_id, done_qty, to_location_id?, from_location_id?, lot_id?, package_id?}]}` → `{next_op_id, backorder_id, delivery_id}` |
| GET · POST · PATCH · PUT | `/wh/packages[?status&order_id]` · `/wh/packages/{id}` · `/wh/packages/{id}/lines` | `{warehouse_id, kind box\|pallet, parent_id, sales_order_id, weight, length_cm, width_cm, height_cm, status open\|sealed\|cancelled}` |
| GET · POST | `/wh/batches` · `/wh/batches/{id}` · `/wh/batches/{id}/done` · `/wh/batches/{id}/cancel` | `{warehouse_id, kind batch\|wave, op_ids? \| up_to_date?, assigned_to?}`; one pick list in bin order |
| POST | `/wh/dropship` | `{order_id, supplier_id, lines? [{line_id, quantity, unit_price?}]}` → purchase order to a dropship warehouse; its bill delivers the order |
| GET | `/wh/reports/slow?days&dead_days` · `/abc?months` · `/ageing` · `/valuation` | stock analysis; valuation ties each inventory account to the ledger |
| GET · POST · PATCH | `/wh/vehicles[/{id}]` | `{name, plate, capacity_kg, driver_id, is_active}` |
| GET | `/wh/to-schedule` | posted deliveries and confirmed orders on no trip |
| GET · POST · PATCH | `/wh/trips[?date&status&mine=1]` · `/wh/trips/{id}` | `{trip_date, vehicle_id, driver_id, notes, stops [{sales_delivery_id \| sales_order_id \| customer_id, address?, phone?, note?}], status}`; drivers (also collectors) see `mine=1` and their own trips |
| POST · GET | `/wh/stops/{id}/pod` · `/wh/stops/{id}/photo` · `/wh/stops/{id}/signature` | `{status delivered\|failed, name, signature (PNG data URL), note, lat, lng}` · `{filename, data}` image |

A sales order in a 2/3-step warehouse refuses `/sales/orders/{id}/deliver` (`use_warehouse_steps`) while its tasks are open.

## Manufacturing pro (migration 00042)

All under `/companies/{cid}`; app **mfg**. Order states: `draft → confirmed → progress → done | cancelled`.

| Method | Path | Notes |
|---|---|---|
| POST · PUT | `/mfg/boms` · `/mfg/boms/{id}` | adds `{code, bom_type normal\|phantom\|subcontract, routing_id, subcontractor_id, subcontract_price, lead_days, version_note, byproducts [{item_id, quantity, cost_share}]}`; activating a version deactivates its family's others; a loop (`bom_cycle`) is refused |
| POST | `/mfg/boms/{id}/new-version` · `/mfg/boms/{id}/activate` · `/mfg/boms/{id}/set-standard` | copy as next version (inactive) · make active · item standard cost = rolled-up unit cost |
| GET | `/mfg/boms/{id}/structure?qty=` | multi-level tree: components (cost source stock\|standard\|bom), operations, by-products, `cost`, `unit_cost` |
| POST | `/mfg/standard-cost` | `{item_id, standard_cost}` |
| GET · POST · PATCH | `/mfg/workcenters[/{id}]` | `{code, name, capacity, hours_per_day, days_per_week, efficiency %, cost_per_hour, setup_min, notes, is_active}` |
| GET · POST · GET · PUT | `/mfg/routings` · `/mfg/routings/{id}` | `{name, notes, is_active, operations [{name, workcenter_id, minutes (per BoM batch), instructions}]}` |
| POST | `/mfg/orders` | adds `{deadline, origin, sales_order_id, confirm}`; phantom components are exploded |
| POST | `/mfg/orders/{id}/confirm` · `/reserve` · `/unreserve` | confirm: work orders + checks + reservation |
| POST | `/mfg/orders/{id}/produce` | `{date, quantity (now; less = backorder), scrap_qty, lots [{lot_no, expiry, quantity}], lines [{line_id, quantity, lots}], byproducts [{id, quantity}], bill (subcontracting, default true), supplier_ref}` → order + `backorder_id`; `quality_pending` / `components_not_sent` (409) |
| POST | `/mfg/orders/{id}/scrap` · `/mfg/orders/{id}/send-components` | `{item_id, quantity, reason, date}` · `{date}` (subcontracting) |
| GET | `/mfg/workorders?workcenter_id&status=open\|all\|…&order_id` · `/mfg/workorders/{id}` | with `running_since`, `worked_min`, `checks_todo`; detail adds checks, components, time logs |
| POST | `/mfg/workorders/{id}/start` · `/pause` · `/finish` | finish `{minutes?, qty_done?}` → `all_done`; refused while a check of the work order is to do |
| GET · POST · PATCH | `/mfg/quality-points[/{id}]` | `{name, trigger_on operation\|production\|receipt, item_id, workcenter_id, test_type passfail\|measure, norm, tol_min, tol_max, unit, instructions, is_active}` |
| GET · POST | `/mfg/quality-checks?status&order_id&workorder_id&receipt_id` · `/mfg/quality-checks/{id}` | record `{result pass\|fail}` or `{measured}` (judged against the tolerances), `note`; a failure opens an alert |
| GET · POST · PATCH | `/mfg/quality-alerts[?stage=open]` · `/mfg/quality-alerts/{id}` | `{title, item_id, order_id, priority 0-3, stage new\|progress\|done, root_cause, action, assigned_to}` |
| GET · POST | `/mfg/mrp?warehouse_id` · `/mfg/mrp/run` | lines `{level, on_hand, incoming, demand, projected, min_qty, max_qty, suggest, kind mo\|po, supplier, need_date, order_date, sources}` · run `{warehouse_id, item_ids?}` → `{mos, pos, skipped}` |
| GET | `/mfg/reports/status` · `/load?days` · `/variance?from&to` · `/shortages` | order status / late; work center load; planned vs actual vs standard; missing components |
| GET | `/mfg/trace?q=` · `/mfg/trace?lot_id=` | lots matching · `{backward, forward}` trees (orders, lots, bills, deliveries, invoices, POS) |

Accounts created on first use: production variance (5150 / 6089), subcontracting received not billed (2150 / 408).


## Projects, field service, SLA, automation, fleet (migration 00043)

All under `/companies/{cid}`. Apps: **prj** (`/prj/…`), **svc** (`/fs/…`, `/sla/…`), **flt** (`/fleet/…`); automation is core.
New permission modules: `projects.view|create|edit|manage`, `fleet.view|create|manage`.

| Method | Path | Notes |
|---|---|---|
| GET | `/prj/overview` | active projects, my open tasks, late tasks, hours this week, hours to invoice |
| GET · POST · GET · PATCH | `/prj/projects[?status=open\|all\|…&q]` · `/prj/projects/{id}` | `{name, code, customer_id, manager_id, billing tm\|fixed\|milestone\|none, currency, hourly_rate, fixed_price, budget, budget_hours, start_date, end_date, status, description, color}`; detail = project, stages, milestones, summary (profitability), links |
| PUT | `/prj/projects/{id}/stages` | `{stages: [{id?, name, is_done}]}` in order; a removed column must be empty (`stage_not_empty`) |
| GET · POST | `/prj/projects/{id}/to-invoice` · `/prj/projects/{id}/invoice` | preview `{time, milestones, fixed}` · `{date, kind time\|milestones\|fixed, percent, amount, timesheet_ids}` → invoice (`over_fixed_price`, `nothing_to_invoice`) |
| POST · DELETE · GET | `/prj/projects/{id}/link` · `/prj/project-links/{id}` · `/prj/linkable?doc_type=expense\|bill` | charge an expense / supplier bill to the project |
| POST · PATCH | `/prj/milestones` · `/prj/milestones/{id}` | `{project_id, name, due_date, amount, reached}` |
| GET · POST · GET · PATCH | `/prj/project-tasks[?project_id&mine=1&open=1&top=1&parent_id&q]` · `/prj/project-tasks/{id}` | `{project_id, stage_id, parent_id, milestone_id, ticket_id, title, description, assignee_id, priority, deadline, planned_hours, checklist [{text, done}], tags, seq, done}`; chatter / files type `project_task` |
| GET · POST · PATCH · DELETE | `/prj/timesheets[?project_id&employee_id&from&to&mine=1]` · `/prj/timesheets/{id}` | `{project_id \| task_id, employee_id (others: projects.manage), date, hours, description, billable}`; invoiced rows are locked (`timesheet_invoiced`); no employee file → `no_employee` |
| GET · POST | `/prj/timer` · `/prj/timer/start` · `/prj/timer/stop` | `{project_id \| task_id, description}` |
| GET | `/prj/reports/profitability?status` | per project: hours, to invoice, labour / expenses / bills cost, revenue, margin, margin %, budget used % (base currency) |
| GET · POST · GET · PATCH | `/fs/service-jobs[?status&technician_id&from&to&q&ticket_id]` · `/fs/service-jobs/{id}` | `{customer_id \| ticket_id, project_id, title, description, address, phone, technician_id, scheduled_start, duration_min, priority, warehouse_id, currency, labour_rate}`; the technician may read his own job |
| POST | `/fs/service-jobs/{id}/start` · `/finish` · `/cancel` · `/reopen` | finish `{report, signature (PNG data URL), signed_by, lat, lng, labour_hours (default: since start)}` |
| PUT · POST · GET | `/fs/service-jobs/{id}/parts` · `/photo` · `/signature` | `{parts: [{item_id, warehouse_id, quantity, unit_price}]}` (price default = sale price) |
| POST | `/fs/service-jobs/{id}/invoice` | `{date}`; job must be done |
| GET | `/fs/my-jobs` · `/fs/parts-catalog?q&warehouse_id` · `/fs/board?from&to` | technician's open jobs · part picker with on-hand · technicians, jobs in range, unplanned |
| GET · POST · PATCH | `/sla/sla-policies[/{id}]` | `{name, priority, category (empty = any), response_hours, resolution_hours, escalate_to, is_active}` |
| GET · POST | `/sla/inbound` · `/sla/inbound/rotate` | `{enabled}` → `{email_url, whatsapp_url, verify_token}` |
| GET | `/tickets/sla-report?from&to` | totals, by policy, by agent, open tickets past their deadline; tickets now carry `sla_state none\|ok\|at_risk\|breached`, deadlines, `rating`, `rating_token` |
| GET · POST · GET · PATCH · DELETE | `/automation/rules[/{id}]` | `{name, model invoice\|ticket\|subscription\|sales_order\|task\|service_job\|item_stock\|vehicle, trigger created\|updated\|scheduled, watch_fields, conditions [{field, op, value}], actions [{type, …}], repeat_days, is_active}` |
| POST | `/automation/rules/{id}/test` · `/automation/rules/{id}/run` | dry run → `{checked, matched [{record_id, label, actions}]}` · run a scheduled rule now |
| GET · POST · GET | `/automation/meta` · `/automation/templates/{key}` · `/automation/runs?rule_id&status` | fields / ops / actions / templates · add a ready-made rule · run log |
| GET | `/record-tags?type&id` | tags set by rules |
| GET · POST · GET · PATCH | `/fleet/vehicles[/{id}]` | `{name, plate, kind, make, model, year, vin, fuel_type, odometer, fuel_norm, purchase_date, license_expiry, insurance_expiry, inspection_expiry, capacity_kg, driver_id, cost_center_id, is_active, notes}` |
| GET · POST · POST | `/fleet/fuel[?vehicle_id&from&to&anomalies=1]` · `/fleet/fuel/{id}/void` | `{vehicle_id, date, odometer, liters, amount, method_id (posts the expense), account_id, full_tank, station, driver_id, notes}` → `consumption`, `anomaly high_consumption\|odometer_back` |
| GET · POST · PATCH | `/fleet/service-plans[/{id}]` | `{vehicle_id, task, every_km, every_days, last_km, last_date, is_active}` → `state due\|soon\|ok\|off` |
| GET · POST · POST | `/fleet/service-log` · `/fleet/service-log/{id}/void` | `{vehicle_id \| plan_id, date, odometer, task, amount, method_id, account_id, vendor, notes}`; the plan's last km / date move on |
| POST | `/fleet/vehicles/{id}/link-expense` | `{expense_id}` (tolls, washing…) |
| GET | `/fleet/alerts` · `/fleet/reports/costs?from&to` · `/fleet/settings` (PUT) | papers / services / fuel anomalies · fuel, service, other, total, km, cost per km · `{fuel_account_id, service_account_id}` |

Public (no sign-in): `POST /inbound/{token}/email`, `GET|POST /inbound/{token}/whatsapp`, `GET|POST /rate/{token}` (`{rating 1-5, comment}`).
Expenses accept `project_id` and `vehicle_id`; employees accept `hourly_cost`.

## Online store, marketing, recruitment, appraisals, e-sign (migration 00044)

All under `/companies/{cid}`. Apps: **web** (`/shop/…`, except `/shop/settings`), **mkt** (`/mkt/…`), **hr** (`/hr/job-positions`,
`/hr/applicants`, `/hr/interviews`, `/hr/appraisal…`, `/self/appraisals`); e-sign is core. New permission modules:
`website.view|manage`, `marketing.view|create|manage`, `talent.view|edit|manage`, `esign.view|create`.

| Method | Path | Notes |
|---|---|---|
| GET · PUT | `/shop/settings` | `{enabled, slug, title, tagline, banner (data URL), color, warehouse_id, price_list_id (sets the currency), currency, show_local, payments [cod pickup whish omt], pay_note, whatsapp, min_order, auto_confirm, hide_out_of_stock, featured [item ids], terms, careers_enabled, careers_intro}` (`slug_required`, `slug_taken`, `invalid_slug`) |
| GET · POST · PATCH · DELETE | `/shop/zones[/{id}]` | `{name, fee, free_from, eta, seq, is_active}` (a used zone is switched off instead of deleted) |
| GET · PATCH | `/shop/products[?q&category_id]` · `/shop/products/{id}` | `{shop_visible, shop_description}` (a template's variants follow) |
| GET · GET · POST | `/shop/orders[?state=open\|received\|preparing\|on_the_way\|delivered\|cancelled&q]` · `/shop/orders/{id}` · `/shop/orders/{id}/confirm\|cancel` | confirm needs `sales.create`, cancel `sales.void` |
| GET · POST · PATCH | `/mkt/overview` · `/mkt/lists[/{id}]` | `{name, description, is_active}` |
| GET · POST · DELETE | `/mkt/lists/{id}/members` · `/mkt/list-members/{id}` | `{customer_ids, contacts [{name, phone, email}], audience}` → `{added}` |
| GET · POST · DELETE | `/mkt/optouts[/{id}]` | `{customer_id \| phone \| email, channel all\|whatsapp\|sms\|email, reason}` |
| POST | `/mkt/audience` | `{channel, audience {all_customers, area_ids, statuses, balance_min, overdue_days, tags, bought_days, not_bought_days, has_subscription}, list_id}` → `{total, reach, skipped, sample}` |
| GET · POST · GET · PATCH | `/mkt/campaigns[/{id}]` | `{name, channel whatsapp\|sms\|email, audience, list_id, subject, body, link_url, promotion_id (coupon), scheduled_at (RFC 3339, "" clears), throttle_per_min}`; detail = campaign with `stats` + recipients |
| POST | `/mkt/campaigns/{id}/send\|cancel\|test` | send = now, or `scheduled` when `scheduled_at` is later; test `{to}`; cancel drops what is still queued |
| GET · POST · PATCH | `/hr/job-positions[/{id}]` | `{title, department, location, employment, description, requirements, salary_range, vacancies, is_published, status open\|closed}` |
| GET · POST · GET · PATCH | `/hr/applicants[?job_id&stage&q]` · `/hr/applicants/{id}` | `{job_id, name, email, phone, source, stage, rating 0-5, cover_letter, expected_salary, notes, offer_salary, offer_currency, offer_start, refuse_reason}`; files / chatter type `applicant` |
| POST | `/hr/applicants/{id}/offer\|hire\|refuse` | `{salary, currency, start, position, reason, notify}`; hire also needs `payroll.create` → employee (`already_hired`) |
| POST · PATCH | `/hr/interviews[/{id}]` | `{applicant_id, scheduled_at, duration_min, interviewer_id, kind phone\|video\|onsite, location, status, rating 1-5, feedback}` |
| GET | `/hr/recruitment-report` · `/hr/appraisal-report?cycle_id` | pipeline per position, sources, days to hire · scores, average, bands |
| GET · POST · PATCH · POST | `/hr/appraisal-cycles[/{id}]` · `/hr/appraisal-cycles/{id}/open\|close` | `{name, period_from, period_to, due_date, self_review}`; open `{reviewer_id, goals [{title, weight}]}` |
| GET · GET · PATCH | `/hr/appraisals[?cycle_id&employee_id]` · `/hr/appraisals/{id}` | reviewer: `{goals [{title, weight, target, manager 0-5, note}], reviewer_id, manager_comment, strengths, improvements, final_score, submit}` |
| GET · PATCH | `/self/appraisals` · `/self/appraisals/{id}` | the employee: `{goals [{title, self}], self_comment, submit}` |
| GET · POST · GET | `/esign/requests[?status]` · `/esign/requests/{id}` | create `{doc_type quote\|contract\|subscription\|upload, title, sales_order_id \| subscription_id \| body \| file {filename, data}, message, ordered, channel whatsapp\|sms\|email\|none, expires_on, auto_confirm, remind_days, signers [{name, email, phone, customer_id}], send}` |
| POST · GET | `/esign/requests/{id}/send\|cancel\|remind` · `/esign/requests/{id}/certificate` | certificate = PDF |

Public (no sign-in, rate limited per address): `GET /shop/{slug}`, `GET /shop/{slug}/items[?category&q&offset]`,
`GET /shop/{slug}/items/{id}[/image]`, `POST /shop/{slug}/cart {lines [{item_id, quantity}], zone_id, delivery, coupon}`,
`POST /shop/{slug}/orders {…cart, name, phone, email, address, payment, notes, website (honeypot)}` (`cart_changed` with the
problems, `invalid_coupon`, `min_order`, `zone_required`), `GET /shop-orders/{token}`, `GET /careers/{slug}`,
`POST /careers/{slug}/jobs/{id}/apply {name, email, phone, cover_letter, expected_salary, cv {filename, data}}`
(`already_applied`, `bad_cv`), `GET /sign/{token}`, `GET /sign/{token}/file|certificate`,
`POST /sign/{token} {method draw|type, signature (PNG data URL), typed_name, agree}` (`document_changed`, `expired`),
`POST /sign/{token}/decline {reason}`, `GET /r/{token}` (tracked link, 302), `GET|POST /optout/{token} {all, reason}`.
The WhatsApp inbound webhook also takes Cloud API `statuses` (delivered / read → the message is marked delivered).

## Smart features (migration 00045)

All under `/companies/{cid}`. App **ai** (`/smart/…`); `/collect/route` and `/customers/{id}/location` are core and also open to
collectors. Permission module `smart.view|ai|manage`. Amounts are in the company's base currency.

| Method | Path | Notes |
|---|---|---|
| GET · PUT | `/smart/settings` | `{params {z, min_history, diesel_min_pct, discount_min_pct, cash_limit, void_min, work_start, work_end, count_loss_limit, collector_drop_pct, lead_days, review_days, service_level}, kinds [...], digest_enabled, digest_hour, digest_phone}`; GET adds `defaults`, `all_kinds`, `last_scan_at`, `last_risk_at`, `last_digest_on` |
| GET | `/smart/alerts[?status=active\|open\|ack\|resolved\|dismissed\|all&kind&severity]` | `{alerts [{kind, severity, subject_label, period, value, expected, score, detail, status, note}], summary, last_scan_at}`; kinds: `diesel discount cash_diff cash_repeat voids off_hours count_loss collector_drop dup_bill fleet_fuel` |
| POST | `/smart/alerts/scan` · `/smart/alerts/{id}/ack\|resolve\|dismiss\|reopen` | scan → `{new, open}`; actions `{note}` (`wrong_status`) |
| GET | `/smart/forecast` | 13 weeks: `{opening, weeks [{start, in {receivables, subscriptions, pos}, out {payables, payroll, recurring, expenses}, net, closing, closing_local, optimistic, pessimistic}], months, lowest, first_negative_week, assumptions}` |
| GET · POST | `/smart/risk[?band&area_id&q]` · `/smart/risk/recompute` | `{rows [{customer_id, score, band, late_score, churn_score, avg_days_late, overdue_base, factors [{code, value, points}]}], summary, computed_at}` |
| GET | `/smart/reorder[?warehouse_id]` | `{lines [{item_id, warehouse_id, on_hand, on_order, weekly_demand, seasonal_factor, lead_days, lead_source, safety_stock, suggest_min, suggest_max, rule_min, rule_max, to_order, days_left, supplier_id, unit_price, status order\|watch\|ok}]}` |
| POST | `/smart/reorder/apply` · `/smart/reorder/purchase` | `{lines [{item_id, warehouse_id, min_qty, max_qty, supplier_id}]}` → reorder rules (`stock.manage`) · `{lines [{item_id, warehouse_id, quantity, supplier_id}]}` → draft purchase orders per supplier (`purchasing.create`) |
| GET | `/collect/route[?lat&lng&area_id&limit≤80]` | `{stops [{seq, customer_id, lat, lng, due_base, bills, risk_band, leg_km}], total_km, missing_location, maps_links}` (collectors: their areas) |
| PUT | `/customers/{id}/location` | `{lat, lng, source gps\|manual}`; `{lat: null, lng: null}` clears |
| GET | `/smart/ai/status` | `{enabled, model, used, limit, can_ask, can_scan}` |
| POST · GET · GET | `/smart/ai/ask` · `/smart/ai/answers/{id}` · `/smart/ai/history` | ask `{messages [{role user\|assistant, content}]}` (≤20 turns, last = user) → 202 `{id}`; poll the answer `{status running\|done\|error, answer, tools, error}` (only your own). Errors: `ai_not_configured` (409), `ai_limit_reached` (429) |
| GET · POST | `/smart/scans` · `/smart/scans {filename, data (base64 / data URL), read}` | JPG / PNG / WebP / GIF / PDF ≤ 10 MB (`unsupported_file`) |
| GET · GET · POST · POST · DELETE | `/smart/scans/{id}` · `/{id}/file` · `/{id}/read` · `/{id}/link {bill_id}` · `/{id}` | read (background) → `status read`, `extracted`, `draft {supplier_id, currency, bill_date, due_date, supplier_ref, lines [{item_id, description, quantity, unit_price}], duplicate_bill}`; link attaches the file to the bill |

Platform owner: `GET /platform/ai` → `{enabled, model, plans [{code, ai_monthly_limit}], usage [{tenant_name, requests, bills_read, errors, input_tokens, output_tokens}]}`;
plans take `ai_monthly_limit` (0 = unlimited). Server: `PAXVE_ANTHROPIC_API_KEY`, `PAXVE_AI_MODEL` (default `claude-opus-5-5`).

## Business packs (migration 00046)

All under `/companies/{cid}`. `POST /retail/packs/{key} {demo}` takes `supermarket clothing electronics furniture wholesale restaurant pharmacy factory`.
Apps: `/rest/` → pos, `/pharma/` `/van/` `/verticals/` → stk, `/furn/` → crm.

| Method | Path | Notes |
|---|---|---|
| GET · PUT | `/items/{id}/vertical` | `{made_to_order, options [{name, values [{label, extra}]}], price_per_m2, kitchen_station, generic_name, active_ingredient, strength, dosage_form, rx_required, controlled}` |
| GET · PUT | `/verticals/warehouses` · `/warehouses/{id}/vertical` | `{is_showroom, is_van, van_user_id}` |
| GET | `/stock/serial-history?serial=` · `/stock/serials?item_id=&warehouse_id=` | lots, moves (partner, document), warranty, repairs · serials in stock |
| POST | `/pos/orders` (new fields) | `lines[].serials`, `lines[].note`, `trade_in [{item_id, serial, value, note}]`, `rx {doctor, number, patient, date}`, `insurance {insurer_id, pct \| amount, policy_no, patient}`, `service_charge`, `tip`; the order returns `vertical`. Errors `serials_mismatch invalid_serial prescription_required invalid_trade_in invalid_insurer lot_expired` |
| POST | `/invoices` (new field) | `lines[].lots [{lot_no, quantity}]` |
| POST | `/furn/configure` | `{item_id, choices {name: label}, width_cm, depth_cm, currency}` → `{unit_price, description, area_m2}` |
| GET · PUT · POST | `/furn/orders` · `/furn/orders/{id}/deposit-pct {pct}` · `/furn/orders/{id}/deposit {method_id, amount}` · `/furn/orders/{id}/schedule {date, slot, vehicle_id, driver_id, assembly, technician_id, start}` | deposit required / paid, trip and assembly job (`order_not_confirmed already_scheduled`) |
| GET | `/furn/showroom` | stock in showroom warehouses with days on display |
| GET · POST · PATCH · PUT | `/van/routes` · `/van/routes/{id}` · `/van/routes/{id}/stops {customer_ids}` | `{name, user_id, warehouse_id, weekdays}` |
| GET | `/van/today[?warehouse_id]` | van, open day, today's stops with balance / credit, van stock with prices, boxes (`no_van not_your_van`) |
| GET · POST | `/van/days[?from&to]` · `/van/days {warehouse_id, source_warehouse_id, lines}` · `/van/days/{id}/load {lines}` · `/van/days/{id}/close {counted, unload}` | close = stock count of the van + unload; summary `{sales, sales_total, collected, differences, unloaded}` |
| POST | `/van/sales` | `{client_ref, customer_id, warehouse_id, lines [{item_id, quantity}], method_id, paid}` → `{invoice}`; customer price list prices, credit checked on the unpaid part (`van_day_not_open credit_limit`) |
| GET | `/van/commission?from&to` | per salesman and currency: invoices, net, collected, commission_pct, commission_on_sales, commission_on_collected |
| GET · POST · PATCH · DELETE | `/rest/tables[/{id}]` · `/rest/modifiers[/{id}]` | tables `{name, area, seats, x, y, shape}`; modifiers `{name, name_i18n, grp, price (base), item_ids, category_ids, sort, is_active}` |
| GET · PUT | `/rest/settings` | `{service_charge_pct}` |
| GET | `/rest/floor?register_id` · `/rest/checks?status&date` · `/rest/checks/{id}` | tables with their open check, takeaway / delivery checks |
| POST · PATCH | `/rest/checks` · `/rest/checks/{id}` | `{register_id, table_id, kind dine_in\|takeaway\|delivery, guests, customer_id, name, phone, address, notes}` (`table_busy`) |
| POST | `/rest/checks/{id}/lines {lines [{item_id, quantity, modifier_ids, note}], send}` · `/send` · `/split {line_ids}` · `/cancel` | kitchen status new → sent → cooking → ready → served |
| POST | `/rest/checks/{id}/pay` | `{session_id, line_ids, tenders, change_method_id, customer_id, on_account, service_charge, tip, client_ref}` → a till sale; check closes when nothing is left |
| POST | `/rest/check-lines/{id}/status {status}` · `/rest/check-lines/{id}/void` | |
| GET | `/rest/kds[?station]` · `/rest/food-cost?from&to` | kitchen lines by age, stations · days `{food_sales, food_cost, food_cost_pct}` and dishes |
| GET | `/pharma/search?q` · `/pharma/alternatives?item_id` · `/pharma/expiry[?days]` · `/pharma/controlled?from&to[&all=1]` | |
| GET · PUT | `/pharma/settings` | `{block_expired, expiry_alert_days}` |
| GET · POST | `/pharma/claims[?status&insurer_id]` · `/pharma/claims/{id}/submit\|settle\|reject` | settle `{method_id, date}` (same currency) moves the receivable to the box; reject writes it off |

## Installable apps, marketing site and leftovers (migration 00047)

Public (no sign-in):

| Method | Path | Notes |
|---|---|---|
| GET | `/public/site` | `{plans [{code, name_en, name_ar, name_fr, price_monthly, price_yearly, currency, max_subscribers, max_users, max_companies, apps, ai_monthly_limit}], phone, email}` (public, active plans; cached 5 min) |
| POST | `/public/demo-request` | `{name, company, phone, email, business, city, message, lang, website}` — phone or e-mail required; `business` one of the 12 types or `other`; `website` is a honeypot; 5 an hour per address (`rate_limited`) |
| GET | `/portal/{token}/more` | `{consumption [{meter_no, period, reading, used}], tickets [{number, subject, status, category, created_at, rating, rating_token}], orders [{number, total, currency, status, token}]}` |
| POST | `/portal/{token}/tickets` | `{subject, description, category}` → `{number}`; channel `portal`, SLA applied, at most 5 a day per customer |

Platform owner: `GET /platform/leads[?status=new|contacted|won|lost]`, `PATCH /platform/leads/{id} {status, notes}`.

Company (`/companies/{cid}`):

| Method | Path | Notes |
|---|---|---|
| GET | `/owner/today` | `reports.view`: `{day, base_currency, collected, sales, cash, receivable [{currency, amount, overdue, customers}], counts {alerts, alerts_high, approvals, orders_today}, alerts}` (alerts only with `smart.view`) |
| GET · PUT | `/payroll-options` | `{post_date: auto \| period_end}` (`payroll.view` / `payroll.edit`) |
| POST | `/import/supplier_openings` | rows `{supplier_code \| supplier_name, amount, currency, date, due_date, reference, description}`, options `{account_id, date}` — one opening supplier bill per row (Dr the equity / chosen account, Cr payables) |
| POST | `/payments` (new field) | `client_time` (RFC 3339, with `client_ref`): when the offline device took the payment; kept when between 45 days ago and now |

Smart alert kind `price_override`: per cashier and week, catalog prices lowered by hand at the till (`pos_orders.price_cut`,
`price_cut_lines`); detail `{lines, pct, weeks}`. A collector who has a cash box of their own (`payment_methods.user_id`) can
only use their own boxes (`invalid_tender`), and only those are listed to them.

## Phase 7 audit fixes (migration 00048)

- `POST /pos/orders/{id}/refund` — cash goes back only up to the real money the customer paid on the order (less earlier refunds). The insurer's share cancels the claim (`pharma_claims.status = cancelled`, `refunded`); trade-in / gift / loyalty shares stay as customer credit. `restock_dishes: true` puts a restaurant dish's ingredients back (default: wasted). Replays with the same `client_ref` wait for the first one and return it.
- `POST /furn/orders/{id}/deposit` — the deposit is reserved for the order (never applied to older invoices); `POST /sales/orders/{id}/invoice` applies it to that order's invoice. `POST /sales/orders/{id}/cancel` with a deposit → `409 order_has_deposit` unless `{deposit: "credit"}` or `{deposit: "refund", method_id}`.
- `POST /pharma/claims/{id}/settle` books FX at the claim's booking rate; `reject` writes off to the "Insurance claims written off" account (source `insurance_claim`).
- `POST /import/supplier_openings` — the same opening (supplier, currency, date, amount, reference) is refused (`already_imported` / `duplicate`); control accounts are refused as the opening account. Fleet accounts and landed-cost `account_id` refuse control accounts too.
- `GET /van/commission` — net of credit notes; collected = real payments only.
- `POST /van/days/{id}/close` — a shortage counted by a user without `stock.manage` returns `{pending_approval: true, count_id, differences}` (draft count, managers notified, the day stays open). Acting on another salesman's van needs `stock.manage`.
- Shop: customers matched on the full national number (`phone_key`), only when exactly one matches; auto-confirm and the WhatsApp tracking message only for known numbers; 3 orders a day per phone, 200 an hour per shop; 100 units a line, 50 lines; coupon tries 20/hour per visitor and shop. Reserved addresses (`slug_reserved`); `PUT /platform/companies/{id}/slug {slug}` (platform admins) assigns one.
- AI: usage reserved inside the request's transaction (organisation lock); per user 10/minute and 200/day; platform daily token ceiling `PAXVE_AI_DAILY_TOKENS` (default 30 M), `PAXVE_AI_OFF=1` pauses; at most 5 model rounds; every tool needs all the rights of its screen; tool results are fenced as data.
- Inbound: `POST /sla/inbound/rotate {whatsapp_secret, email_key, keep_address}` — with the Meta app secret set, WhatsApp posts need `X-Hub-Signature-256`; with the Mailgun key set, form posts need Mailgun's signature (15 minutes) and JSON posts `X-Paxve-Signature` (hex HMAC-SHA256 of the body).
- E-sign: `require_otp` on create; `POST /sign/{token}/otp` (code by WhatsApp → SMS → e-mail, 10 minutes, 5 an hour) and `POST /sign/{token}/otp/verify {code}`; links stop working 30 days after the request is finished; files are served sandboxed.
- Careers CVs: 500 MB in all, 100 a day, 30 a day per position. `fs/parts-catalog` needs service.view / stock.view or an open job. Till-order attachments (prescriptions, trade-in IDs) need `pos.void` or `reports.view`. Device `client_time` accepted up to 12 hours back.
## Phase 7 UX fixes (migration 00049)

- `POST /signup` takes `business_type` (one of the 12 types, default generator), `phone` (stored as the organisation's contact phone) and `rate` (local currency per 1 base currency, e.g. `89500`; `400 bad_rate` when not a positive number). The company starts with the apps of its business type only and in the owner's language. 20 sign-ups an hour per IP; a `429 too_many_attempts` carries the seconds to wait in `detail` and `Retry-After`.
- `POST /companies` takes `business_type`, `template` (chart template; default the organisation's country) and `locale` (default the user's); the chart is loaded in the same transaction. `no_chart: true` keeps an empty chart (imports).
- `GET /companies/{cid}/apps/plan?business_type=x` → `{target, add, remove: [{app, has_data}]}`: what choosing that type changes.
- `POST /retail/packs/{key}` takes `remove: [apps]` (apps the pack does not need that the user agreed to switch off; core apps are never removed) and `template` (used when the company has no chart yet: it is loaded first). A failing demo step puts the company's apps, business type and pack back and answers `422 pack_demo_failed` with the reason in `detail`.
- `POST /companies/{cid}/translate-defaults {dry_run, lang}` renames the names the system created in English (accounts, CRM stages, demo categories / items, the main warehouse, showroom, van, till, demo supplier, routes, price lists, work centers, table areas) to the company's language; records the user named are not touched. Answers `{changes: [{kind, from, to}]}`.
- `GET /van/today?soft=1` answers `{van: null}` instead of `422 no_van`; `GET /self/appraisals` answers `[]` for a user without an employee file.
- At start-up, live companies (trial / active / grace) that have no chart at all get the organisation's chart template.