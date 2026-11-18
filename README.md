# Paxve

Cloud accounting and billing for subscription businesses — generator electricity, internet, satellite TV, VOD —
built for Lebanon and the region, usable anywhere: **any currency, English / Arabic (RTL) / French, multi-tenant SaaS,
open REST API.**

Live: https://paxve.com

## What it does

| Area | Features |
|---|---|
| **Accounting** | Chart of accounts with templates (Lebanese PCG, international), names in any language; journal entries in any currency, always balanced; posted entries immutable (reversal only); gap-free numbering; period lock; trial balance, ledger, profit & loss, balance sheet |
| **Currencies** | 65+ ISO currencies, base + second currency per company, daily official / buy / sell rates, realized FX gain/loss booked automatically |
| **Billing** | Customers, areas, plans (fixed monthly or metered: base fee + kWh × price), subscriptions, meter-reading sheets, one-click monthly billing run, manual invoices, printable invoices |
| **Payments** | Pay in several currencies at once (e.g. USD + LBP), automatic change (rounded to 1,000 / 5,000 LBP if wanted), oldest invoices paid first, customer balances per currency, aging report |
| **Collectors** | Collector role limited to their areas; mobile collector mode (PWA, works offline, payments queued and synced once — idempotent); Bluetooth ESC/POS receipts (Arabic rendered as image); personal cash boxes and handover |
| **Purchases & stock** | Suppliers, purchase bills (AP), supplier payments, expenses, items and warehouses with weighted-average cost, fuel consumption, stock adjustments, stock valuation, AP aging |
| **Messaging** | WhatsApp Cloud API, Twilio SMS, generic HTTP or wa.me links; templates in EN/AR/FR; reminders for unpaid bills; automatic receipt / bill messages |
| **Import** | CSV / Excel paste wizard for customers, subscriptions, opening balances, meter readings, accounts (validated dry run, all-or-nothing) |
| **SaaS** | Public landing page, self sign-up with 14-day trial, plans with subscriber limits, platform owner console |
| **Developers** | REST API `/api/v1` with API keys (hashed, per role, revocable), OpenAPI 3.1 spec at `/api/v1/openapi.yaml` |

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
  - every change is written to an append-only `audit_log` (user, API key, IP, before/after).
- Money is `numeric` everywhere (decimal strings in JSON), never floating point.

## Layout

```
cmd/paxve/              entry point: serve, migrate, create-tenant
internal/db/            tenant-scoped transactions, migrations (embedded SQL, 00001…)
internal/auth/          argon2id passwords
internal/charts/        chart-of-accounts templates
internal/httpapi/       JSON API, posting engine, reports, workers, OpenAPI spec, integration tests
web/                    React app (locales in web/src/locales: en, ar, fr)
deploy/Caddyfile        HTTPS reverse proxy
docs/                   API quick start, operations, Arabic user guide
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
