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

# a balanced journal entry, posted immediately
curl -s -X POST https://paxve.com/api/v1/companies/$CID/journal -H "$H" -H "$J" -d '{
  "date":"2026-11-18", "memo":"Owner contribution", "post":true,
  "lines":[{"account_id":"<cash>","debit":"1000"},{"account_id":"<capital>","credit":"1000"}]}'

# reports
curl -s "https://paxve.com/api/v1/companies/$CID/reports/trial-balance?from=2026-01-01&to=2026-12-31" -H "$H"
curl -s "https://paxve.com/api/v1/companies/$CID/reports/aging" -H "$H"
```

## Rules the API cannot bypass

These are enforced inside PostgreSQL, so they hold for the API, the web app and any future client:
balanced and immutable journal entries, period lock, no deletion of issued documents (void = reversal),
invoices never paid above their total, stock never negative, tenant isolation (row-level security).
