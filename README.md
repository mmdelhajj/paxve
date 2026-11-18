# Paxve

Cloud accounting and billing for subscription businesses (generators, internet, satellite, VOD) — USD and LBP, English and Arabic.

## Stack

- **Backend:** Go, `net/http`, pgx, goose migrations, argon2id + scs sessions
- **Database:** PostgreSQL 18 with row-level security (every business table has `tenant_id`)
- **Web:** React + Vite + Mantine, English / Arabic (RTL)
- **Run:** Docker Compose (Postgres + app + Caddy for HTTPS)

## Rules enforced in the database

- Every business table has `tenant_id`; row-level security means one organisation never sees another's rows. The app connects as `paxve_app`, which cannot bypass RLS.
- Every insert/update on tenants, companies and users is written to `audit_log` (user, time, IP, before/after) by triggers. `audit_log` is append-only.
- The app role has no `DELETE` on business tables: records are deactivated, never deleted.
- Coming with the ledger: balanced double-entry (debits = credits) checked by a deferred constraint trigger, `numeric` money (never float), posted entries immutable and only reversed.

## Run on a server

```sh
cp .env.example .env    # fill both passwords: openssl rand -hex 24
docker compose up -d --build
docker compose run --rm app create-tenant -name "My Company" -slug my-company -email admin@example.com -admin "Admin Name"
```

Edit `deploy/Caddyfile` with your domain; Caddy gets the HTTPS certificate automatically.

## Develop

```sh
docker compose up -d db
export DATABASE_URL=postgres://paxve_app:...@localhost:5432/paxve MIGRATE_DATABASE_URL=... APP_DB_PASSWORD=... AUTO_MIGRATE=true COOKIE_SECURE=false
go run ./cmd/paxve serve
cd web && npm install && npm run dev   # http://localhost:5173, proxies /api to :8080
```

## Layout

```
cmd/paxve/            entry point (serve, migrate, create-tenant)
internal/db/          tenant-scoped transactions, migrations (embedded SQL)
internal/auth/        argon2id password hashing
internal/httpapi/     JSON API + static web app
web/                  React app
deploy/Caddyfile      HTTPS reverse proxy
```
