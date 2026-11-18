package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/db"
	"github.com/mmdelhajj/paxve/internal/routeros"
)

// ISP automation: MikroTik routers, automatic suspend of overdue internet subscriptions and
// reconnect after payment. Router calls never run inside a database transaction: rules and
// payments only queue router_actions; the router worker sends them and retries on failure.

const routerTimeout = 8 * time.Second

var hostRe = regexp.MustCompile(`^[A-Za-z0-9.\-:]{1,253}$`)

const routerCols = `r.id, r.name, r.host, r.port, r.username, (r.password_enc <> '') AS has_password, r.use_tls, r.enabled,
	r.last_check_at, r.last_status, r.identity, r.version, r.created_at,
	(SELECT count(*) FROM subscriptions s WHERE s.router_id = r.id) AS subscriptions,
	(SELECT count(*) FROM subscriptions s WHERE s.router_id = r.id AND s.status = 'suspended') AS suspended`

func (s *Server) listRouters(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+routerCols+` FROM routers r WHERE r.company_id = $1 ORDER BY r.name`, r.PathValue("cid"))
	})
}

type routerIn struct {
	Name     *string `json:"name"`
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	Username *string `json:"username"`
	Password *string `json:"password"` // "" or masked keeps the stored password
	UseTLS   *bool   `json:"use_tls"`
	Enabled  *bool   `json:"enabled"`
}

func (s *Server) saveRouter(w http.ResponseWriter, r *http.Request) {
	var in routerIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Name == nil || in.Host == nil || in.Username == nil)) || (!create && !isUUID(id)) ||
		(in.Name != nil && strings.TrimSpace(*in.Name) == "") || (in.Host != nil && !hostRe.MatchString(strings.TrimSpace(*in.Host))) ||
		(in.Port != nil && (*in.Port < 1 || *in.Port > 65535)) || (in.Username != nil && strings.TrimSpace(*in.Username) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var pw *string
	if in.Password != nil && *in.Password != "" && !strings.HasPrefix(*in.Password, maskPrefix) {
		sealed, err := sealSecret(*in.Password)
		if err != nil {
			serverErr(w, r, err)
			return
		}
		pw = &sealed
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		trim := func(p *string) *string {
			if p == nil {
				return nil
			}
			v := strings.TrimSpace(*p)
			return &v
		}
		if create {
			port := 8728
			if in.UseTLS != nil && *in.UseTLS {
				port = 8729
			}
			if in.Port != nil {
				port = *in.Port
			}
			err := tx.QueryRow(ctx, `INSERT INTO routers (company_id, name, host, port, username, password_enc, use_tls)
				VALUES ($1, $2, $3, $4, $5, coalesce($6, ''), coalesce($7, false)) RETURNING id::text`,
				cid, *trim(in.Name), *trim(in.Host), port, *trim(in.Username), pw, in.UseTLS).Scan(&id)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
			}
			if err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE routers SET name = coalesce($3, name), host = coalesce($4, host), port = coalesce($5, port),
				username = coalesce($6, username), password_enc = coalesce($7, password_enc), use_tls = coalesce($8, use_tls),
				enabled = coalesce($9, enabled) WHERE id = $1 AND company_id = $2`,
				id, cid, trim(in.Name), trim(in.Host), in.Port, trim(in.Username), pw, in.UseTLS, in.Enabled)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
			}
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+routerCols+` FROM routers r WHERE r.id = $1`, id)
	})
}

type routerConn struct {
	host, user, pw string
	port           int
	tls, enabled   bool
}

// dialRouter resolves the host, refuses internal addresses (unless allowed), connects and logs in.
func (s *Server) dialRouter(ctx context.Context, rc routerConn) (*routeros.Client, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, rc.host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("unreachable: host not found")
	}
	var ip net.IP
	for _, a := range ips {
		if s.AllowPrivateRouters || !(a.IP.IsLoopback() || a.IP.IsPrivate() || a.IP.IsLinkLocalUnicast() ||
			a.IP.IsLinkLocalMulticast() || a.IP.IsUnspecified() || a.IP.IsMulticast()) {
			ip = a.IP
			break
		}
	}
	if ip == nil {
		return nil, errors.New("blocked: private or local addresses are not allowed")
	}
	c, err := routeros.Dial(ctx, ip.String(), rc.port, rc.tls, routerTimeout)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, errors.New("timeout: router did not answer")
		}
		return nil, errors.New("unreachable: connection refused or network error")
	}
	if err := c.Login(rc.user, rc.pw); err != nil {
		c.Close()
		var re *routeros.Error
		if errors.As(err, &re) {
			return nil, fmt.Errorf("login failed: %s", re.Message)
		}
		return nil, errors.New("login failed: no answer from the API service")
	}
	return c, nil
}

func loadRouterConn(ctx context.Context, tx pgx.Tx, cid, id string) (routerConn, error) {
	var rc routerConn
	var pw string
	err := tx.QueryRow(ctx, `SELECT host, port, username, password_enc, use_tls, enabled FROM routers WHERE id = $1 AND company_id = $2`,
		id, cid).Scan(&rc.host, &rc.port, &rc.user, &pw, &rc.tls, &rc.enabled)
	if err == pgx.ErrNoRows {
		return rc, notFound()
	}
	rc.pw = openSecret(pw)
	return rc, err
}

// testRouter connects to the router and stores identity/version or the error.
func (s *Server) testRouter(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	ctx := r.Context()
	var rc routerConn
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		var err error
		rc, err = loadRouterConn(ctx, tx, cid, id)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	identity, version, status := "", "", "ok"
	dctx, cancel := context.WithTimeout(ctx, 2*routerTimeout)
	c, derr := s.dialRouter(dctx, rc)
	if derr == nil {
		identity, version, derr = c.Identity()
		c.Close()
	}
	cancel()
	if derr != nil {
		status = derr.Error()
	}
	err = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE routers SET last_check_at = now(), last_status = $3,
			identity = CASE WHEN $4 = '' THEN identity ELSE $4 END, version = CASE WHEN $5 = '' THEN version ELSE $5 END
			WHERE id = $1 AND company_id = $2`, id, cid, status, identity, version)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": derr == nil, "identity": identity, "version": version, "error": errString(derr)})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---- settings ----

func (s *Server) getISPSettings(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT auto_suspend_enabled, suspend_after_days, reconnect_on_payment FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}

func (s *Server) putISPSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AutoSuspend *bool `json:"auto_suspend_enabled"`
		Days        *int  `json:"suspend_after_days"`
		Reconnect   *bool `json:"reconnect_on_payment"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Days != nil && (*in.Days < 0 || *in.Days > 365) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return dmlJSONOne(ctx, tx, `UPDATE companies SET auto_suspend_enabled = coalesce($2, auto_suspend_enabled),
			suspend_after_days = coalesce($3, suspend_after_days), reconnect_on_payment = coalesce($4, reconnect_on_payment)
			WHERE id = $1 RETURNING auto_suspend_enabled, suspend_after_days, reconnect_on_payment`, r.PathValue("cid"), in.AutoSuspend, in.Days, in.Reconnect)
	})
}

// ---- actions ----

func (s *Server) listRouterActions(w http.ResponseWriter, r *http.Request) {
	sub := r.URL.Query().Get("subscription_id")
	if sub != "" && !isUUID(sub) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT a.id, a.action, a.reason, a.status, a.attempts, a.error, a.created_at, a.sent_at, a.next_attempt_at,
			a.subscription_id, s.network_username, ro.name AS router_name, c.id AS customer_id, c.name AS customer_name, u.full_name AS created_by
			FROM router_actions a JOIN subscriptions s ON s.id = a.subscription_id JOIN customers c ON c.id = s.customer_id
			JOIN routers ro ON ro.id = a.router_id LEFT JOIN users u ON u.id = a.created_by
			WHERE a.company_id = $1 AND ($2 = '' OR a.subscription_id = $2::uuid)
			ORDER BY a.created_at DESC LIMIT 300`, r.PathValue("cid"), sub)
	})
}

// queueRouterAction cancels older pending actions for the subscription and queues a new one.
func queueRouterAction(ctx context.Context, tx pgx.Tx, cid, subID, routerID, action, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE router_actions SET status = 'cancelled' WHERE subscription_id = $1 AND status = 'queued'`, subID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO router_actions (company_id, router_id, subscription_id, action, reason) VALUES ($1, $2, $3, $4, $5)`,
		cid, routerID, subID, action, reason)
	return err
}

// subscriptionNetwork: manual suspend / reconnect from the customer page.
func (s *Server) subscriptionNetwork(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.Action != "disable" && in.Action != "enable") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var router *string
		var user, status string
		err := tx.QueryRow(ctx, `SELECT router_id::text, network_username, status FROM subscriptions WHERE id = $1 AND company_id = $2 FOR UPDATE`,
			id, cid).Scan(&router, &user, &status)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if router == nil || user == "" {
			return nil, badRequest("no_router")
		}
		if status == "ended" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		newStatus, reason := "suspended", "manual"
		if in.Action == "enable" {
			newStatus, reason = "active", ""
			if status != "active" {
				if err := checkPlanLimit(ctx, tx); err != nil {
					return nil, err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET status = $2, suspended_reason = $3 WHERE id = $1`, id, newStatus, reason); err != nil {
			return nil, err
		}
		if err := queueRouterAction(ctx, tx, cid, id, *router, in.Action, "manual"); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+subscriptionCols+` FROM subscriptions s JOIN plans p ON p.id = s.plan_id WHERE s.id = $1`, id)
	})
}

// ---- rules ----

// runSuspendRule suspends internet subscriptions whose customer has bills overdue for more than
// the company's suspend_after_days. Returns how many were suspended.
func runSuspendRule(ctx context.Context, tx pgx.Tx, cid string) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.router_id::text FROM subscriptions s JOIN companies co ON co.id = s.company_id
		JOIN routers ro ON ro.id = s.router_id AND ro.enabled
		WHERE s.company_id = $1 AND s.network_username <> '' AND s.status = 'active'
		  AND EXISTS (SELECT 1 FROM invoices i WHERE i.customer_id = s.customer_id AND i.status IN ('open', 'partial')
		              AND i.due_date < current_date - co.suspend_after_days)
		ORDER BY s.id FOR UPDATE OF s`, cid)
	if err != nil {
		return 0, err
	}
	type sub struct{ id, router string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sub, error) {
		var x sub
		err := r.Scan(&x.id, &x.router)
		return x, err
	})
	if err != nil {
		return 0, err
	}
	for _, x := range list {
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET status = 'suspended', suspended_reason = 'overdue' WHERE id = $1`, x.id); err != nil {
			return 0, err
		}
		if err := queueRouterAction(ctx, tx, cid, x.id, x.router, "disable", "overdue"); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// reconnectIfCleared re-enables subscriptions suspended for debt once the customer has no
// overdue bills left (called inside the payment transaction; only queues router actions).
func reconnectIfCleared(ctx context.Context, tx pgx.Tx, cid, customerID string) error {
	var enabled, stillOverdue bool
	err := tx.QueryRow(ctx, `SELECT co.reconnect_on_payment,
		EXISTS (SELECT 1 FROM invoices i WHERE i.customer_id = $2 AND i.status IN ('open', 'partial')
		        AND i.due_date < current_date - co.suspend_after_days)
		FROM companies co WHERE co.id = $1`, cid, customerID).Scan(&enabled, &stillOverdue)
	if err != nil || !enabled || stillOverdue {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text, router_id::text FROM subscriptions
		WHERE customer_id = $1 AND status = 'suspended' AND suspended_reason = 'overdue' AND router_id IS NOT NULL AND network_username <> ''
		FOR UPDATE`, customerID)
	if err != nil {
		return err
	}
	type sub struct{ id, router string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sub, error) {
		var x sub
		err := r.Scan(&x.id, &x.router)
		return x, err
	})
	if err != nil {
		return err
	}
	for _, x := range list {
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET status = 'active', suspended_reason = '' WHERE id = $1`, x.id); err != nil {
			return err
		}
		if err := queueRouterAction(ctx, tx, cid, x.id, x.router, "enable", "payment"); err != nil {
			return err
		}
	}
	return nil
}

// runISPNow: admin runs the suspend rule for this company immediately.
func (s *Server) runISPNow(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, r.PathValue("cid")); err != nil {
			return nil, err
		}
		n, err := runSuspendRule(ctx, tx, r.PathValue("cid"))
		return map[string]int{"suspended": n}, err
	})
}

// ---- background worker ----

// StartISPWorker runs the suspend rule every 15 minutes for companies that enabled it, and sends
// queued router actions every 10 seconds (retrying with backoff, up to 5 attempts).
func (s *Server) StartISPWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	_, _ = s.OwnerPool.Exec(ctx, `UPDATE router_actions SET status = 'queued' WHERE status = 'sending' AND updated_at < now() - interval '5 minutes'`)
	go func() {
		send := time.NewTicker(10 * time.Second)
		rule := time.NewTimer(time.Minute)
		defer send.Stop()
		defer rule.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-send.C:
				if _, err := s.ProcessRouterActions(ctx); err != nil {
					slog.Error("router actions", "err", err)
				}
			case <-rule.C:
				if n, err := s.RunISPRules(ctx); err != nil {
					slog.Error("isp rules", "err", err)
				} else if n > 0 {
					slog.Info("isp rules: subscriptions suspended", "count", n)
				}
				rule.Reset(15 * time.Minute)
			}
		}
	}()
}

// RunISPRules applies the suspend rule to every company that enabled it, as the restricted app
// role inside each tenant (row-level security applies). Returns how many subscriptions were suspended.
func (s *Server) RunISPRules(ctx context.Context) (int, error) {
	rows, err := s.OwnerPool.Query(ctx, `SELECT id::text, tenant_id::text FROM companies c
		WHERE auto_suspend_enabled AND is_active AND EXISTS (SELECT 1 FROM routers r WHERE r.company_id = c.id AND r.enabled)`)
	if err != nil {
		return 0, err
	}
	type co struct{ id, tenant string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (co, error) {
		var x co
		err := r.Scan(&x.id, &x.tenant)
		return x, err
	})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, c := range list {
		err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, c.tenant); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
				return err
			}
			n, err := runSuspendRule(ctx, tx, c.id)
			total += n
			return err
		})
		if err != nil {
			slog.Error("isp rule failed", "company", c.id, "err", err)
		}
	}
	return total, nil
}

type queuedAction struct {
	ID, Tenant, Sub, Action, User, Service string
	Attempts                               int
	rc                                     routerConn
}

// ProcessRouterActions sends due queued actions. Returns how many were handled.
func (s *Server) ProcessRouterActions(ctx context.Context) (int, error) {
	var batch []queuedAction
	err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id::text, a.tenant_id::text, a.subscription_id::text, a.action, a.attempts, s.network_username, s.service_type,
			       r.host, r.port, r.username, r.password_enc, r.use_tls, r.enabled
			FROM router_actions a JOIN subscriptions s ON s.id = a.subscription_id JOIN routers r ON r.id = a.router_id
			WHERE a.status = 'queued' AND a.next_attempt_at <= now()
			ORDER BY a.next_attempt_at LIMIT 20
			FOR UPDATE OF a SKIP LOCKED`)
		if err != nil {
			return err
		}
		batch, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (queuedAction, error) {
			var q queuedAction
			var pw string
			err := row.Scan(&q.ID, &q.Tenant, &q.Sub, &q.Action, &q.Attempts, &q.User, &q.Service,
				&q.rc.host, &q.rc.port, &q.rc.user, &pw, &q.rc.tls, &q.rc.enabled)
			q.rc.pw = openSecret(pw)
			return q, err
		})
		if err != nil || len(batch) == 0 {
			return err
		}
		ids := make([]string, len(batch))
		for i, q := range batch {
			ids[i] = q.ID
		}
		_, err = tx.Exec(ctx, `UPDATE router_actions SET status = 'sending' WHERE id = ANY($1::uuid[])`, ids)
		return err
	})
	if err != nil || len(batch) == 0 {
		return 0, err
	}
	for _, q := range batch {
		s.sendRouterAction(ctx, q)
	}
	return len(batch), nil
}

func (s *Server) sendRouterAction(ctx context.Context, q queuedAction) {
	var sendErr error
	if !q.rc.enabled {
		sendErr = errors.New("router_disabled")
	} else {
		dctx, cancel := context.WithTimeout(ctx, 3*routerTimeout)
		c, err := s.dialRouter(dctx, q.rc)
		if err == nil {
			err = c.SetUserDisabled(q.Service, q.User, q.Action == "disable")
			c.Close()
			if errors.Is(err, routeros.ErrNotFound) {
				err = errors.New("user not found on router: " + q.User)
			}
		}
		cancel()
		sendErr = err
	}
	err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, q.Tenant); err != nil {
			return err
		}
		if sendErr == nil {
			if _, err := tx.Exec(ctx, `UPDATE router_actions SET status = 'sent', sent_at = now(), error = '', attempts = attempts + 1 WHERE id = $1`, q.ID); err != nil {
				return err
			}
			ns := "enabled"
			if q.Action == "disable" {
				ns = "disabled"
			}
			_, err := tx.Exec(ctx, `UPDATE subscriptions SET network_status = $2 WHERE id = $1`, q.Sub, ns)
			return err
		}
		attempts := q.Attempts + 1
		status := "queued"
		if attempts >= 5 {
			status = "failed"
		}
		msg := sendErr.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
		backoff := time.Duration(1<<attempts) * time.Minute // 2, 4, 8, 16 min
		_, err := tx.Exec(ctx, `UPDATE router_actions SET status = $2, attempts = $3, error = $4,
			next_attempt_at = now() + $5::interval WHERE id = $1`, q.ID, status, attempts, msg, fmt.Sprintf("%d seconds", int(backoff.Seconds())))
		return err
	})
	if err != nil {
		slog.Error("router action update", "id", q.ID, "err", err)
	}
}

// retryRouterAction puts a failed action back in the queue.
func (s *Server) retryRouterAction(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE router_actions SET status = 'queued', attempts = 0, next_attempt_at = now(), error = ''
			WHERE id = $1 AND company_id = $2 AND status = 'failed'`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return map[string]bool{"ok": true}, nil
	})
}

// checkRouter verifies a router belongs to the company ("" = none).
func checkRouter(ctx context.Context, tx pgx.Tx, cid, id string) error {
	if id == "" {
		return nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM routers WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok)
	if err == nil && !ok {
		return badRequest("invalid_router")
	}
	return err
}
