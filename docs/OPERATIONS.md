# Paxve — operations

## Production layout (current server)

- Ubuntu 24.04, Docker Compose project in `/srv/paxve` (git checkout of `github.com/mmdelhajj/paxve`, branch `main`).
- Containers: `db` (postgres:18, volume `pgdata`), `app` (Go binary + web build, distroless, non-root), `caddy`
  (ports 80/443, Let's Encrypt certificates, volume `caddy_data`).
- Cloudflare proxies `paxve.com` / `www.paxve.com` to the server. Caddy trusts `CF-Connecting-IP` only from
  Cloudflare's IP ranges and passes the real client IP to the app as `X-Client-IP`.
- Firewall (ufw): 22, 80, 443 only. PostgreSQL is not exposed.
- Logs: Docker json-file, 20 MB × 5 files per container. `docker compose logs -f app`.

## Environment (`/srv/paxve/.env`, chmod 600)

| Variable | Meaning |
|---|---|
| `DB_OWNER_PASSWORD` | password of the `paxve` database owner (migrations, sign-up, platform console, message worker) |
| `DB_APP_PASSWORD` | password of `paxve_app`, the restricted role the API uses (set by the app at each migration) |
| `PLATFORM_ADMINS` | comma-separated emails allowed to open the platform owner console; these emails cannot be registered through sign-up or by tenant admins |
| `APP_SECRET_KEY` | 32-byte key (`openssl rand -hex 32`) encrypting stored secrets (WhatsApp/SMS tokens, router passwords) with AES-256-GCM. Existing plaintext secrets are encrypted at startup. **Back it up** (copy in `/srv/paxve-secrets/app-secret-key.env`): a database backup restored without this key loses those secrets. |
| `ALLOW_PRIVATE_ROUTERS` | `true` only if MikroTik routers are reached over a VPN on private addresses (default `false`: blocks internal addresses). See `docs/ISP.md`. |

The app also reads `AUTO_MIGRATE` (true in compose), `COOKIE_SECURE`, `ADDR`, `WEB_DIR`.

## Deploy / upgrade

```sh
cd /srv/paxve
git pull
docker compose up -d --build app      # migrations run automatically at start (AUTO_MIGRATE=true)
curl -s https://paxve.com/api/health/ready   # {"ok":true,"migration":N,"expected":N}
```

Migrations are forward-only in practice (`internal/db/migrations/NNNNN_name.sql`, goose). Take a backup first for
risky releases: `/usr/local/bin/paxve-backup`.

After changing `deploy/Caddyfile`: `docker compose restart caddy`.

## Background workers

The app runs these in-process (owner connection): message sender, recurring entries (hourly), ISP router actions,
webhook deliveries, and the **ledger summary** (every 10 s). The ledger worker adds newly posted journal entries to
`ledger_daily` in journal-number order and stores how far it got in `ledger_watermark`; reports read
`account_totals()` = summary + entries posted since, so they are exact even if the worker is behind or stopped.
After a restore or a manual data fix, rebuild the summary for one company:

```sh
docker compose exec -T db psql -U paxve -d paxve -c "DELETE FROM ledger_daily WHERE company_id = '<cid>'" \
  -c "DELETE FROM ledger_watermark WHERE company_id = '<cid>'"     # the worker refills it within seconds
```

## Performance (measured 2026-11-18, this 4 vCPU / 3 GB server)

A throwaway company with 50,000 customers, 600,000 bills, 500,000 payments and 2.2 million journal lines:
trial balance 0.1 s (was 5.2 s), balance sheet / P&L 0.16 s (5.9 / 4.8 s), journal list < 0.1 s (5 s), cash boxes 0.1 s,
dashboard 0.35 s (4.4 s; its charts are kept for a minute for such large companies), statements page 0.45 s
(100 customers per page), aging 1 s (500 rows per page). JIT is turned off for `paxve_app` (it cost up to 1 s per
report query). Unfiltered payment lists (API only, the screen always sends a date range) still take ~4 s on that size.
To repeat: `tools/perf/` (README there) — it uses its own postgres container on 127.0.0.1:55433, never production.

## Health checks

- `GET /api/health` — process up and database reachable.
- `GET /api/health/ready` — database reachable **and** schema at the migration version the binary expects (503 otherwise).

## Backups

- `/usr/local/bin/paxve-backup` runs daily at 03:30 (`/etc/cron.d/paxve-backup`), writes
  `/srv/backups/paxve-YYYYMMDD-HHMM.dump` (pg_dump custom format) and keeps 14 days. Log: `/var/log/paxve-backup.log`.
- **Copy backups off the server** (another machine / object storage) — a server loss would take local backups with it.
  Example: `rsync -a /srv/backups/ backup-host:/paxve/` from cron.

### Restore

Test restore into a scratch database (safe, does not touch production) — verified 2026-11-18, identical counts:

```sh
cd /srv/paxve
f=$(ls -t /srv/backups/paxve-*.dump | head -1)
docker compose exec -T db psql -U paxve -d postgres -c "CREATE DATABASE paxve_restore_test"
docker compose exec -T db pg_restore -U paxve -d paxve_restore_test --no-owner < "$f"
docker compose exec -T db psql -U paxve -d paxve_restore_test -c "select count(*) from journal_lines"
docker compose exec -T db psql -U paxve -d postgres -c "DROP DATABASE paxve_restore_test"
```

Full disaster restore (replaces production data):

```sh
cd /srv/paxve
docker compose stop app
docker compose exec -T db psql -U paxve -d postgres -c "DROP DATABASE paxve WITH (FORCE)"
docker compose exec -T db psql -U paxve -d postgres -c "CREATE DATABASE paxve"
docker compose exec -T db pg_restore -U paxve -d paxve --no-owner < /srv/backups/paxve-YYYYMMDD-HHMM.dump
docker compose start app      # re-applies grants/app password via migrations on start
```

The `paxve_app` role is cluster-wide and survives a database drop; its grants are restored from the dump.

## Tests

`make test` on any machine with Docker and Go: starts a throwaway `postgres:18` on 127.0.0.1:55432, runs the
integration tests, removes the container. **Never** point `TEST_DATABASE_URL` at the production cluster: the tests
create databases and set the `paxve_app` password.

## Tenants

- New organisations sign up at `/#/signup` (14-day trial, 50 subscriptions).
- Platform owner console: `/#/platform` (emails in `PLATFORM_ADMINS`): change plan / limit / trial end, suspend.
- CLI alternative: `docker compose run --rm app create-tenant -name … -slug … -email … -admin …` (prints a one-time password).

## Security notes

- Sessions: HttpOnly, Secure, SameSite=Lax cookies; state-changing API calls must be JSON (CSRF guard); DELETE requires a custom header.
- Login rate limit: 10 failures / 15 min per IP **and** per account. Sign-up: 5 / hour per IP.
- API keys are stored as SHA-256 hashes; message-provider secrets and router passwords are encrypted at rest
  (AES-256-GCM, `APP_SECRET_KEY`), masked in API responses and excluded from the audit log.
- Router "test connection" refuses private/loopback/link-local addresses unless `ALLOW_PRIVATE_ROUTERS=true`.
- CSP `default-src 'self'`, HSTS, X-Frame-Options DENY, nosniff.
- Company isolation is enforced by PostgreSQL (policy `company_scope` + same-company foreign keys), not only by
  the handlers; `TestCompanyIsolationEveryRoute` calls every id route of the API with another company's ids.
- Approved requests run in the requester's name with the requester's current permissions; the audit log records
  the approver (`approved_by`) and the approval request.
