# ISP automation (MikroTik)

Paxve can cut and restore the internet of a subscriber automatically:

* **Suspend** — every 15 minutes, for companies that enabled it, subscriptions linked to a router whose customer has
  a bill unpaid more than *N* days after its due date are set to `suspended` and the PPPoE secret (or hotspot user) is
  disabled on the router; the active session is removed so the cut is immediate.
* **Reconnect** — when a payment leaves the customer with no overdue bill, the subscriptions suspended for debt are
  re-activated and the user is enabled again.
* **Manual** — admins/accountants can Suspend / Reconnect from the customer page.

Router calls never run inside a database transaction. Rules, payments and buttons only add a row to
`router_actions`; the router worker (every 10 s) sends it, and on failure retries after 2, 4, 8 and 16 minutes,
then marks it `failed` (a failed action can be retried from Network → Routers). Newer actions cancel older
queued ones for the same subscription, so the last decision wins.

## Setup

1. **On the MikroTik** (RouterOS 6.43+ or 7):
   * `IP → Services`: enable `api` (port 8728) or, better, `api-ssl` (port 8729, needs a certificate).
   * Restrict the service to the Paxve server address (`Available From` = the server's public IP).
   * `System → Users`: create a group with `read, write, api` (plus `ssh` not needed) and a user in it.
   * Subscribers must exist as `PPP → Secrets` (PPPoE) or `IP → Hotspot → Users` with the same name you type in Paxve.
2. **In Paxve** → Billing → Network → **Routers**:
   * Add the router (public IP / host, port, API user, password, TLS on/off) and press **Test**: it shows the
     router identity and RouterOS version, or a clear error (`timeout`, `unreachable`, `login failed: …`).
   * Turn on **Suspend internet when bills are overdue**, choose the number of days, keep **Reconnect after payment** on.
3. On each internet subscription (customer page → Edit subscription) choose the **Router**, the **PPPoE / hotspot
   user** and the service.

Security:
* Router passwords (and WhatsApp/SMS tokens) are encrypted at rest with AES-256-GCM using `APP_SECRET_KEY`
  (server `.env`; a copy is kept in `/srv/paxve-secrets/app-secret-key.env`). **Back the key up** — without it the
  stored secrets cannot be decrypted. Secrets are never returned by the API or written to the audit log.
* Routers on private, loopback or link-local addresses are refused (so tenants cannot probe the server's internal
  network). If your routers are reached over a VPN, set `ALLOW_PRIVATE_ROUTERS=true` in `.env`.
* API-SSL uses the router's self-signed certificate; it is not verified (RouterOS default).

## Manual test with a real router (not available during development)

1. Create a PPPoE secret `test1` on the router and connect a client (or a second MikroTik) with it.
2. Add the router in Paxve and press **Test** → identity and version appear.
3. Create a customer with an internet subscription linked to the router, user `test1`.
4. Create a manual invoice dated in the past (due date older than the configured days).
5. Network → Routers → **Run now** → the subscription becomes *Suspended*; within ~10 s the router action shows
   *Done* and on the router `test1` is disabled and its session is gone (`/ppp active print`).
6. Receive a payment for the invoice → subscription *Active* again; action *Reconnect · Done*; `test1` enabled.
7. Switch the router off / block the port and press Suspend on the customer → the action shows the error and
   the number of attempts, and is retried automatically; after 5 attempts it is *Failed* and can be retried.

## Tests

* `internal/routeros`: length encoding (1–5 bytes), sentence round trip, client against an in-process fake
  RouterOS server (login, wrong password, identity, disable + kick session, enable, unknown user).
* `internal/httpapi/isp_test.go` (needs `TEST_DATABASE_URL`, see `make test`): router password encrypted at rest
  and never returned, test connection, masked update keeps the password, SSRF guard, tenant isolation, suspend
  rule picks only overdue customers, worker disables on the fake router, payment reconnects, unreachable router
  is retried with backoff, trial balance still balanced.
