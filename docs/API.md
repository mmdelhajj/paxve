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

Register an URL per company (Developers page or `POST /companies/{cid}/webhooks`) for the events
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
invoices never paid above their total, stock never negative, tenant isolation (row-level security).
