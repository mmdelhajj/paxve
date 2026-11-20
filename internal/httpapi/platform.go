package httpapi

// Platform owner console: every organisation that signed up to Paxve, its users and companies, its plan,
// paid-until date and payments; renew, block / unblock, extend a trial, reset the admin password, and a
// read-only support view. Organisations see their own plan under "My Paxve subscription" and can ask
// for a renewal or an upgrade from there.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/auth"
	"github.com/mmdelhajj/paxve/internal/db"
)

// SupportInfo is shown in the banner while a platform owner looks at an organisation.
type SupportInfo struct {
	By     string    `json:"by"`
	ByName string    `json:"by_name"`
	Since  time.Time `json:"since"`
}

const supportTTL = 2 * time.Hour

// writes allowed during a support view (everything else is read-only)
var supportWriteRoutes = map[string]bool{"POST /platform/support/end": true, "POST /auth/logout": true}

// writes an expired organisation may still do: sign out, its profile and security, and ask to renew
var expiredWriteRoutes = map[string]bool{
	"POST /auth/logout": true, "PATCH /me": true, "POST /me/mfa/setup": true, "POST /me/mfa/enable": true,
	"POST /me/mfa/disable": true, "POST /me/mfa/recovery-codes": true, "POST /me/sessions/{id}/revoke": true,
	"POST /me/sessions/revoke-others": true, "POST /subscription/requests": true, "PATCH /subscription": true,
	"POST /platform/support/end": true,
}

var planCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,30}$`)

var platformMethods = map[string]bool{"cash": true, "whish": true, "omt": true, "bank": true, "card": true, "other": true}

const platformTenantCols = `t.id, t.name, t.slug, t.plan, t.trial_ends_at, t.max_subscribers, t.max_users, t.max_companies, t.is_active,
	t.suspended_reason, t.blocked_at, t.blocked_reason, t.created_at, t.paid_until, t.billing_cycle, t.price_override, t.price_currency,
	t.grace_days, t.notes, t.contact_phone, t.contact_email, tenant_state(t) AS state, t.grandfathered_apps, p.apps AS plan_apps,
	CASE WHEN t.plan = 'trial' THEN t.trial_ends_at::date ELSE t.paid_until END AS ends_on,
	coalesce(t.price_override, CASE WHEN t.billing_cycle = 'yearly' THEN p.price_yearly ELSE p.price_monthly END) AS price,
	coalesce(t.price_currency, p.currency) AS currency,
	(SELECT count(*) FROM users u WHERE u.tenant_id = t.id) AS users,
	(SELECT count(*) FROM companies c WHERE c.tenant_id = t.id) AS companies,
	(SELECT count(*) FROM subscriptions s WHERE s.tenant_id = t.id AND s.status = 'active') AS active_subscriptions,
	(SELECT min(u.email) FROM users u WHERE u.tenant_id = t.id AND u.role = 'admin') AS admin_email,
	(SELECT max(us.last_seen_at) FROM user_sessions us WHERE us.tenant_id = t.id) AS last_activity_at,
	(SELECT c.country FROM companies c WHERE c.tenant_id = t.id ORDER BY c.created_at LIMIT 1) AS country,
	(SELECT c.business_type FROM companies c WHERE c.tenant_id = t.id ORDER BY c.created_at LIMIT 1) AS business_type,
	(SELECT count(*) FROM platform_requests q WHERE q.tenant_id = t.id AND q.status = 'open') AS open_requests`

const platformTenantFrom = ` FROM tenants t JOIN platform_plans p ON p.code = t.plan`

// platformEvent records an owner action (or a lifecycle change found by the worker).
func platformEvent(ctx context.Context, tx pgx.Tx, r *http.Request, tenantID, action string, detail map[string]any) error {
	actor, ip := "system", ""
	if r != nil {
		actor, ip = principal(r).Email, clientIP(r)
	}
	if detail == nil {
		detail = map[string]any{}
	}
	b, _ := json.Marshal(detail)
	var tid *string
	if tenantID != "" {
		tid = &tenantID
	}
	_, err := tx.Exec(ctx, `INSERT INTO platform_events (tenant_id, actor_email, action, detail, ip) VALUES ($1, $2, $3, $4, nullif($5, '')::inet)`,
		tid, actor, action, b, ip)
	return err
}

type planLimitsRow struct{ subs, users, companies *int }

func loadPlanLimits(ctx context.Context, tx pgx.Tx, code string) (*planLimitsRow, error) {
	p := &planLimitsRow{}
	err := tx.QueryRow(ctx, `SELECT max_subscribers, max_users, max_companies FROM platform_plans WHERE code = $1 AND is_active`, code).
		Scan(&p.subs, &p.users, &p.companies)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, badRequest("unknown_plan")
	}
	return p, err
}

// checkTenantLimit refuses one more active user or company over the plan's limit.
func checkTenantLimit(ctx context.Context, tx pgx.Tx, kind string) error {
	var max *int
	var n int
	q := `SELECT max_users, (SELECT count(*) FROM users WHERE is_active) FROM tenants WHERE id = app_tenant()`
	if kind == "companies" {
		q = `SELECT max_companies, (SELECT count(*) FROM companies WHERE is_active) FROM tenants WHERE id = app_tenant()`
	}
	if err := tx.QueryRow(ctx, q).Scan(&max, &n); err != nil {
		return err
	}
	if max != nil && n >= *max {
		return &apiError{status: http.StatusPaymentRequired, code: "plan_limit_" + kind, detail: strconv.Itoa(*max)}
	}
	return nil
}

// tenantBlocked: the organisation is blocked (and the user itself is on), with the reason given by the owner.
func (s *Server) tenantBlocked(ctx context.Context, tid, uid string) (string, bool) {
	if s.OwnerPool == nil {
		return "", false
	}
	var tActive, uActive bool
	var reason string
	err := s.OwnerPool.QueryRow(ctx, `SELECT t.is_active, u.is_active, t.blocked_reason FROM tenants t JOIN users u ON u.tenant_id = t.id
		WHERE t.id = $1 AND u.id = $2`, tid, uid).Scan(&tActive, &uActive, &reason)
	if err != nil || tActive || !uActive {
		return "", false
	}
	return reason, true
}

// ---------- support view (session) ----------

// checkSupport: when the session is a support view, check the platform owner behind it is still valid and
// return who it is. A view older than supportTTL is ended (the session goes back to the owner).
func (s *Server) checkSupport(ctx context.Context, r *http.Request) (*SupportInfo, error) {
	ouid := s.Sessions.GetString(ctx, "sup_uid")
	if ouid == "" {
		return nil, nil
	}
	otid := s.Sessions.GetString(ctx, "sup_tid")
	at, _ := strconv.ParseInt(s.Sessions.GetString(ctx, "sup_at"), 10, 64)
	osc := db.Scope{TenantID: otid, UserID: ouid, IP: clientIP(r)}
	op, err := s.loadPrincipal(ctx, osc)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!op.Active || !op.IsPlatformAdmin)) {
		return nil, errSessionRevoked
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.touchSession(ctx, osc, s.Sessions.GetString(ctx, "sup_sid"), r); err != nil {
		return nil, err
	}
	since := time.Unix(at, 0)
	if time.Since(since) > supportTTL {
		s.restoreSupport(ctx)
		return nil, nil
	}
	return &SupportInfo{By: op.Email, ByName: op.FullName, Since: since}, nil
}

func (s *Server) restoreSupport(ctx context.Context) {
	s.Sessions.Put(ctx, "uid", s.Sessions.GetString(ctx, "sup_uid"))
	s.Sessions.Put(ctx, "tid", s.Sessions.GetString(ctx, "sup_tid"))
	s.Sessions.Put(ctx, "sid", s.Sessions.GetString(ctx, "sup_sid"))
	for _, k := range []string{"sup_uid", "sup_tid", "sup_sid", "sup_at", "sup_target"} {
		s.Sessions.Remove(ctx, k)
	}
}

func (s *Server) startSupport(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	me := principal(r)
	if s.Sessions.GetString(ctx, "sup_uid") != "" {
		writeErr(w, http.StatusConflict, "support_active")
		return
	}
	if id == me.TenantID {
		writeErr(w, http.StatusBadRequest, "cannot_change_self")
		return
	}
	var target, email string
	err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT is_active FROM tenants WHERE id = $1`, id).Scan(&active); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound()
			}
			return err
		}
		if !active {
			return &apiError{status: http.StatusConflict, code: "org_blocked"}
		}
		err := tx.QueryRow(ctx, `SELECT id::text, email FROM users WHERE tenant_id = $1 AND is_active AND role = 'admin'
			ORDER BY (role_id IS NULL) DESC, created_at LIMIT 1`, id).Scan(&target, &email)
		if errors.Is(err, pgx.ErrNoRows) {
			return &apiError{status: http.StatusConflict, code: "no_admin"}
		}
		if err != nil {
			return err
		}
		return platformEvent(ctx, tx, r, id, "support_start", map[string]any{"as": email})
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := s.Sessions.RenewToken(ctx); err != nil {
		serverErr(w, r, err)
		return
	}
	s.Sessions.Put(ctx, "sup_uid", me.ID)
	s.Sessions.Put(ctx, "sup_tid", me.TenantID)
	s.Sessions.Put(ctx, "sup_sid", me.SessionID)
	s.Sessions.Put(ctx, "sup_at", strconv.FormatInt(time.Now().Unix(), 10))
	s.Sessions.Put(ctx, "sup_target", id)
	s.Sessions.Put(ctx, "uid", target)
	s.Sessions.Put(ctx, "tid", id)
	s.Sessions.Remove(ctx, "sid")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "as": email})
}

// endSupport (POST /platform/support/end): back to the platform console.
func (s *Server) endSupport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.Sessions.GetString(ctx, "sup_uid") == "" {
		writeErr(w, http.StatusConflict, "no_support")
		return
	}
	target := s.Sessions.GetString(ctx, "sup_target")
	by := ""
	if p := principal(r); p.Support != nil {
		by = p.Support.By
	}
	s.restoreSupport(ctx)
	if err := s.Sessions.RenewToken(ctx); err != nil {
		serverErr(w, r, err)
		return
	}
	if s.OwnerPool != nil && isUUID(target) {
		_, _ = s.OwnerPool.Exec(ctx, `INSERT INTO platform_events (tenant_id, actor_email, action, ip) VALUES ($1, $2, 'support_end', nullif($3, '')::inet)`,
			target, by, clientIP(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- console: summary, organisations ----------

func (s *Server) platformSummary(w http.ResponseWriter, r *http.Request) {
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `
			WITH t AS (SELECT t.*, tenant_state(t) AS state,
			             CASE WHEN t.plan = 'trial' THEN t.trial_ends_at::date ELSE t.paid_until END AS ends_on,
			             coalesce(t.price_override, CASE WHEN t.billing_cycle = 'yearly' THEN p.price_yearly / 12 ELSE p.price_monthly END) AS monthly
			           FROM tenants t JOIN platform_plans p ON p.code = t.plan)
			SELECT count(*) AS organisations,
			  count(*) FILTER (WHERE state = 'trial') AS trial,
			  count(*) FILTER (WHERE state = 'active') AS active,
			  count(*) FILTER (WHERE state = 'grace') AS grace,
			  count(*) FILTER (WHERE state = 'expired') AS expired,
			  count(*) FILTER (WHERE state = 'blocked') AS blocked,
			  round(coalesce(sum(monthly) FILTER (WHERE state IN ('active', 'grace')), 0), 2) AS mrr,
			  round(coalesce(sum(monthly) FILTER (WHERE state IN ('active', 'grace')), 0) * 12, 2) AS arr,
			  count(*) FILTER (WHERE created_at >= date_trunc('month', now())) AS signups_month,
			  count(*) FILTER (WHERE state IN ('trial', 'active') AND ends_on BETWEEN current_date AND current_date + 7) AS expiring_7d,
			  (SELECT count(DISTINCT e.tenant_id) FROM platform_events e WHERE e.action = 'state' AND e.at > now() - interval '30 days'
			     AND e.detail ->> 'to' IN ('expired', 'blocked')) AS churn_30d,
			  (SELECT count(*) FROM platform_requests WHERE status = 'open') AS open_requests,
			  (SELECT coalesce(json_agg(m ORDER BY m.month), '[]'::json) FROM (
			     SELECT to_char(date_trunc('month', pp.created_at), 'YYYY-MM') AS month, pp.currency, sum(pp.amount) AS amount, count(*) AS payments
			     FROM platform_payments pp WHERE pp.created_at >= date_trunc('month', now()) - interval '11 months'
			     GROUP BY 1, 2) m) AS revenue
			FROM t`)
	})
}

// GET /platform/setup: what the platform itself relies on — the messaging providers of the owner's own company
// (they send the renewal reminders; "log" only writes them to the log) and the contact organisations are shown
// for paying (PAXVE_SALES_PHONE / PAXVE_SALES_EMAIL in the server .env).
func (s *Server) platformSetup(w http.ResponseWriter, r *http.Request) {
	tid := s.platformAdminTenant(r.Context())
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		providers, err := queryJSON(ctx, tx, `SELECT p.id, p.kind, p.name, p.is_default, p.is_active, c.name_en AS company
			FROM message_providers p JOIN companies c ON c.id = p.company_id
			WHERE c.id = (SELECT id FROM companies WHERE tenant_id = nullif($1, '')::uuid AND is_active ORDER BY created_at LIMIT 1)
			ORDER BY p.is_active DESC, p.is_default DESC, p.created_at`, tid)
		if err != nil {
			return nil, err
		}
		var company *string
		_ = tx.QueryRow(ctx, `SELECT name_en FROM companies WHERE tenant_id = nullif($1, '')::uuid AND is_active ORDER BY created_at LIMIT 1`, tid).Scan(&company)
		return map[string]any{"owner_company": company, "providers": providers,
			"sales_phone": s.platformContactPhone, "sales_email": s.platformContactEmail}, nil
	})
}

func (s *Server) platformTenants(w http.ResponseWriter, r *http.Request) {
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+platformTenantCols+platformTenantFrom+` ORDER BY t.created_at DESC`)
	})
}

func (s *Server) platformTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		t, err := queryJSONOne(ctx, tx, `SELECT `+platformTenantCols+platformTenantFrom+` WHERE t.id = $1`, id)
		if err != nil {
			return nil, err
		}
		users, err := queryJSON(ctx, tx, `SELECT u.id, u.email, u.full_name, u.role, coalesce(ro.name, '') AS role_name, u.is_active, u.created_at,
			(SELECT max(us.created_at) FROM user_sessions us WHERE us.user_id = u.id) AS last_login,
			(SELECT max(us.last_seen_at) FROM user_sessions us WHERE us.user_id = u.id) AS last_seen
			FROM users u LEFT JOIN roles ro ON ro.id = u.role_id WHERE u.tenant_id = $1 ORDER BY u.created_at`, id)
		if err != nil {
			return nil, err
		}
		companies, err := queryJSON(ctx, tx, `SELECT c.id, c.name_en, c.name_ar, c.country, c.business_type, c.apps, c.base_currency, c.local_currency,
			c.is_active, c.created_at FROM companies c WHERE c.tenant_id = $1 ORDER BY c.created_at`, id)
		if err != nil {
			return nil, err
		}
		usage, err := queryJSONOne(ctx, tx, `SELECT
			(SELECT count(*) FROM customers WHERE tenant_id = $1) AS customers,
			(SELECT count(*) FROM subscriptions WHERE tenant_id = $1 AND status = 'active') AS active_subscriptions,
			(SELECT count(*) FROM invoices WHERE tenant_id = $1 AND created_at >= date_trunc('month', now())) AS invoices_month,
			(SELECT count(*) FROM payments WHERE tenant_id = $1 AND created_at >= date_trunc('month', now())) AS payments_month,
			(SELECT count(*) FROM journal_entries WHERE tenant_id = $1) AS journal_entries,
			(SELECT coalesce(sum(size), 0) FROM attachments WHERE tenant_id = $1 AND deleted_at IS NULL) AS storage_bytes,
			(SELECT count(*) FROM users WHERE tenant_id = $1 AND is_active) AS active_users,
			(SELECT count(*) FROM companies WHERE tenant_id = $1 AND is_active) AS active_companies`, id)
		if err != nil {
			return nil, err
		}
		payments, err := queryJSON(ctx, tx, `SELECT id, amount, currency, method, reference, plan, cycle, period_from, period_to, note,
			recorded_by_email, created_at FROM platform_payments WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 200`, id)
		if err != nil {
			return nil, err
		}
		events, err := queryJSON(ctx, tx, `SELECT id, at, actor_email, action, detail, host(ip) AS ip FROM platform_events
			WHERE tenant_id = $1 ORDER BY id DESC LIMIT 200`, id)
		if err != nil {
			return nil, err
		}
		requests, err := queryJSON(ctx, tx, `SELECT q.id, q.kind, q.plan, q.cycle, q.method, q.reference, q.note, q.status, q.created_at,
			q.handled_at, q.handled_by, q.answer, u.full_name AS created_by_name FROM platform_requests q LEFT JOIN users u ON u.id = q.created_by
			WHERE q.tenant_id = $1 ORDER BY q.created_at DESC LIMIT 100`, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tenant": t, "users": users, "companies": companies, "usage": usage, "payments": payments,
			"events": events, "requests": requests}, nil
	})
}

func (s *Server) platformUpdateTenant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name            *string `json:"name"`
		Plan            *string `json:"plan"`
		IsActive        *bool   `json:"is_active"`
		TrialEndsAt     *string `json:"trial_ends_at"`   // YYYY-MM-DD, "" clears
		MaxSubscribers  *int    `json:"max_subscribers"` // 0 = unlimited
		MaxUsers        *int    `json:"max_users"`       // 0 = unlimited
		MaxCompanies    *int    `json:"max_companies"`   // 0 = unlimited
		SuspendedReason *string `json:"suspended_reason"`
		PaidUntil       *string `json:"paid_until"` // YYYY-MM-DD, "" clears
		BillingCycle    *string `json:"billing_cycle"`
		PriceOverride   *string `json:"price_override"` // "" = the plan's price
		PriceCurrency   *string `json:"price_currency"`
		GraceDays       *int    `json:"grace_days"`
		Notes           *string `json:"notes"`
		ContactPhone    *string `json:"contact_phone"`
		ContactEmail    *string `json:"contact_email"`
		// apps kept from before the plan narrowed its list ([] removes them all)
		GrandfatheredApps *[]string `json:"grandfathered_apps"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	var gf []string
	if in.GrandfatheredApps != nil {
		gf = []string{}
		for _, a := range *in.GrandfatheredApps {
			if !slices.Contains(knownApps, a) {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
			if !slices.Contains(gf, a) && !slices.Contains(coreApps, a) {
				gf = append(gf, a)
			}
		}
	}
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	bad := func() bool {
		for _, d := range []*string{in.TrialEndsAt, in.PaidUntil} {
			if d != nil && *d != "" && !validDate(*d) {
				return true
			}
		}
		for _, n := range []*int{in.MaxSubscribers, in.MaxUsers, in.MaxCompanies} {
			if n != nil && *n < 0 {
				return true
			}
		}
		if in.BillingCycle != nil && *in.BillingCycle != "monthly" && *in.BillingCycle != "yearly" {
			return true
		}
		if in.PriceOverride != nil && *in.PriceOverride != "" {
			if d, err := decimal.NewFromString(*in.PriceOverride); err != nil || d.IsNegative() {
				return true
			}
		}
		if in.PriceCurrency != nil && *in.PriceCurrency != "" && !currencyRe.MatchString(*in.PriceCurrency) {
			return true
		}
		if in.GraceDays != nil && (*in.GraceDays < 0 || *in.GraceDays > 90) {
			return true
		}
		if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
			return true
		}
		return in.Plan != nil && !planCodeRe.MatchString(*in.Plan)
	}
	if bad() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.IsActive != nil && !*in.IsActive && id == principal(r).TenantID {
		writeErr(w, http.StatusBadRequest, "cannot_change_self")
		return
	}
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var before json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT row_to_json(x) FROM (SELECT plan, paid_until, billing_cycle, price_override, max_subscribers, max_users,
			max_companies, trial_ends_at::date AS trial_ends_at, grace_days FROM tenants WHERE id = $1 FOR UPDATE) x`, id).Scan(&before); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		var lim *planLimitsRow
		if in.Plan != nil {
			var err error
			if lim, err = loadPlanLimits(ctx, tx, *in.Plan); err != nil {
				return nil, err
			}
		}
		// a limit: given (0 = unlimited), or the new plan's own limit, or unchanged
		pick := func(given *int, fromPlan func(*planLimitsRow) *int) (bool, *int) {
			if given != nil {
				if *given == 0 {
					return true, nil
				}
				return true, given
			}
			if lim != nil {
				return true, fromPlan(lim)
			}
			return false, nil
		}
		setS, valS := pick(in.MaxSubscribers, func(p *planLimitsRow) *int { return p.subs })
		setU, valU := pick(in.MaxUsers, func(p *planLimitsRow) *int { return p.users })
		setC, valC := pick(in.MaxCompanies, func(p *planLimitsRow) *int { return p.companies })
		var name *string
		if in.Name != nil {
			n := strings.TrimSpace(*in.Name)
			name = &n
		}
		if _, err := tx.Exec(ctx, `UPDATE tenants SET
			plan = coalesce($2, plan),
			trial_ends_at = CASE WHEN $3::text IS NULL THEN trial_ends_at WHEN $3 = '' THEN NULL ELSE ($3::date + interval '1 day' - interval '1 second') END,
			max_subscribers = CASE WHEN $4 THEN $5::int ELSE max_subscribers END,
			max_users = CASE WHEN $6 THEN $7::int ELSE max_users END,
			max_companies = CASE WHEN $8 THEN $9::int ELSE max_companies END,
			paid_until = CASE WHEN $10::text IS NULL THEN paid_until WHEN $10 = '' THEN NULL ELSE $10::date END,
			billing_cycle = coalesce($11, billing_cycle),
			price_override = CASE WHEN $12::text IS NULL THEN price_override WHEN $12 = '' THEN NULL ELSE $12::numeric END,
			price_currency = CASE WHEN $13::text IS NULL THEN price_currency WHEN $13 = '' THEN NULL ELSE $13 END,
			grace_days = coalesce($14, grace_days),
			notes = coalesce($15, notes),
			contact_phone = coalesce($16, contact_phone),
			contact_email = coalesce($17, contact_email),
			name = coalesce($18, name),
			grandfathered_apps = coalesce($19::text[], grandfathered_apps)
			WHERE id = $1`, id, in.Plan, in.TrialEndsAt, setS, valS, setU, valU, setC, valC, in.PaidUntil, in.BillingCycle,
			in.PriceOverride, in.PriceCurrency, in.GraceDays, in.Notes, in.ContactPhone, in.ContactEmail, name, gf); err != nil {
			return nil, err
		}
		if in.IsActive != nil {
			reason := ""
			if in.SuspendedReason != nil {
				reason = *in.SuspendedReason
			}
			if err := setBlocked(ctx, tx, r, id, !*in.IsActive, reason); err != nil {
				return nil, err
			}
		} else if in.SuspendedReason != nil {
			if _, err := tx.Exec(ctx, `UPDATE tenants SET suspended_reason = $2, blocked_reason = $2 WHERE id = $1`, id, *in.SuspendedReason); err != nil {
				return nil, err
			}
		}
		var after json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT row_to_json(x) FROM (SELECT plan, paid_until, billing_cycle, price_override, max_subscribers, max_users,
			max_companies, trial_ends_at::date AS trial_ends_at, grace_days FROM tenants WHERE id = $1) x`, id).Scan(&after); err != nil {
			return nil, err
		}
		if string(before) != string(after) {
			if err := platformEvent(ctx, tx, r, id, "update", map[string]any{"before": before, "after": after}); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+platformTenantCols+platformTenantFrom+` WHERE t.id = $1`, id)
	})
}

// setBlocked blocks (login refused, open sessions ended) or unblocks an organisation.
func setBlocked(ctx context.Context, tx pgx.Tx, r *http.Request, id string, block bool, reason string) error {
	var err error
	if block {
		_, err = tx.Exec(ctx, `UPDATE tenants SET is_active = false, blocked_at = now(), blocked_reason = $2, suspended_reason = $2 WHERE id = $1`, id, reason)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE tenant_id = $1 AND revoked_at IS NULL`, id)
		}
		if err == nil {
			err = platformEvent(ctx, tx, r, id, "block", map[string]any{"reason": reason, "jobs": "paused"})
		}
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE tenants SET is_active = true, blocked_at = NULL, blocked_reason = '', suspended_reason = '' WHERE id = $1 AND NOT is_active`, id)
	if err == nil && tag.RowsAffected() > 0 {
		err = platformEvent(ctx, tx, r, id, "unblock", map[string]any{"jobs": "resumed"})
	}
	return err
}

// platformTenantAction: POST /platform/tenants/{id}/{op}
//
//	renew          {period: month|year|custom, until?, plan?, amount?, currency?, method, reference?, note?, request_id?}
//	block          {reason}
//	unblock        {}
//	extend-trial   {days? | until?}
//	reset-password {user_id?}   → {email, password} shown once
//	support        {}           → the session becomes a read-only view of the organisation
func (s *Server) platformTenantAction(w http.ResponseWriter, r *http.Request) {
	id, op := r.PathValue("id"), r.PathValue("op")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		Period    string `json:"period"`
		Until     string `json:"until"`
		Plan      string `json:"plan"`
		Amount    string `json:"amount"`
		Currency  string `json:"currency"`
		Method    string `json:"method"`
		Reference string `json:"reference"`
		Note      string `json:"note"`
		RequestID string `json:"request_id"`
		Reason    string `json:"reason"`
		Days      int    `json:"days"`
		UserID    string `json:"user_id"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &in) {
		return
	}
	switch op {
	case "support":
		s.startSupport(w, r, id)
		return
	case "block":
		if id == principal(r).TenantID {
			writeErr(w, http.StatusBadRequest, "cannot_change_self")
			return
		}
		s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
			if err := tenantExists(ctx, tx, id); err != nil {
				return nil, err
			}
			if err := setBlocked(ctx, tx, r, id, true, strings.TrimSpace(in.Reason)); err != nil {
				return nil, err
			}
			return queryJSONOne(ctx, tx, `SELECT `+platformTenantCols+platformTenantFrom+` WHERE t.id = $1`, id)
		})
	case "unblock":
		s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
			if err := tenantExists(ctx, tx, id); err != nil {
				return nil, err
			}
			if err := setBlocked(ctx, tx, r, id, false, ""); err != nil {
				return nil, err
			}
			return queryJSONOne(ctx, tx, `SELECT `+platformTenantCols+platformTenantFrom+` WHERE t.id = $1`, id)
		})
	case "extend-trial":
		if (in.Until != "" && !validDate(in.Until)) || in.Days < 0 || in.Days > 365 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		if in.Until == "" && in.Days == 0 {
			in.Days = 7
		}
		s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
			var plan string
			if err := tx.QueryRow(ctx, `SELECT plan FROM tenants WHERE id = $1 FOR UPDATE`, id).Scan(&plan); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, notFound()
				}
				return nil, err
			}
			if plan != "trial" {
				return nil, badRequest("not_trial")
			}
			var ends string
			if err := tx.QueryRow(ctx, `UPDATE tenants SET trial_ends_at = CASE WHEN $2 <> '' THEN ($2::date + interval '1 day' - interval '1 second')
				ELSE greatest(coalesce(trial_ends_at, now()), now()) + make_interval(days => $3) END
				WHERE id = $1 RETURNING trial_ends_at::date::text`, id, in.Until, in.Days).Scan(&ends); err != nil {
				return nil, err
			}
			if err := platformEvent(ctx, tx, r, id, "extend_trial", map[string]any{"until": ends}); err != nil {
				return nil, err
			}
			return queryJSONOne(ctx, tx, `SELECT `+platformTenantCols+platformTenantFrom+` WHERE t.id = $1`, id)
		})
	case "renew":
		s.platformRenew(w, r, id, in.Period, in.Until, in.Plan, in.Amount, in.Currency, in.Method, in.Reference, in.Note, in.RequestID)
	case "reset-password":
		if in.UserID != "" && !isUUID(in.UserID) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		pw := auth.RandomPassword()
		hash, err := auth.Hash(pw)
		if err != nil {
			serverErr(w, r, err)
			return
		}
		s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
			var uid, email string
			err := tx.QueryRow(ctx, `SELECT id::text, email FROM users WHERE tenant_id = $1 AND ($2 = '' OR id = nullif($2, '')::uuid)
				ORDER BY (role = 'admin') DESC, (role_id IS NULL) DESC, created_at LIMIT 1`, id, in.UserID).Scan(&uid, &email)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			if err != nil {
				return nil, err
			}
			if s.platformAdmins[email] {
				return nil, badRequest("cannot_change_self")
			}
			if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, password_changed_at = now() WHERE id = $1`, uid, hash); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, uid); err != nil {
				return nil, err
			}
			if err := platformEvent(ctx, tx, r, id, "reset_password", map[string]any{"email": email}); err != nil {
				return nil, err
			}
			return map[string]any{"email": email, "password": pw}, nil
		})
	default:
		writeErr(w, http.StatusNotFound, "not_found")
	}
}

func tenantExists(ctx context.Context, tx pgx.Tx, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenants WHERE id = $1)`, id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return notFound()
	}
	return nil
}

// renewPeriod: the next paid period after paidUntil (or from today when it already ran out).
func renewPeriod(paidUntil *time.Time, today time.Time, period, until string) (from, to time.Time, err error) {
	from = today
	if paidUntil != nil && !paidUntil.Before(today) {
		from = paidUntil.AddDate(0, 0, 1)
	}
	switch period {
	case "month", "":
		to = from.AddDate(0, 1, -1)
	case "year":
		to = from.AddDate(1, 0, -1)
	case "custom":
		to, err = time.Parse(time.DateOnly, until)
		if err != nil || to.Before(today) {
			return from, to, badRequest("bad_date")
		}
		if to.Before(from) { // shortening a period paid ahead: the new period starts today
			from = today
		}
	default:
		return from, to, badRequest("bad_request")
	}
	return from, to, nil
}

func (s *Server) platformRenew(w http.ResponseWriter, r *http.Request, id, period, until, plan, amount, currency, method, reference, note, requestID string) {
	if method == "" {
		method = "cash"
	}
	if !platformMethods[method] || (plan != "" && !planCodeRe.MatchString(plan)) || (currency != "" && !currencyRe.MatchString(currency)) ||
		(requestID != "" && !isUUID(requestID)) || len(reference) > 120 || len(note) > 1000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var amt *decimal.Decimal
	if amount != "" {
		d, err := decimal.NewFromString(amount)
		if err != nil || d.IsNegative() {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		amt = &d
	}
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var cur, cycle string
		var paid *time.Time
		var overrideTxt, overrideCur *string
		if err := tx.QueryRow(ctx, `SELECT plan, paid_until, billing_cycle, price_override::text, price_currency::text FROM tenants WHERE id = $1 FOR UPDATE`, id).
			Scan(&cur, &paid, &cycle, &overrideTxt, &overrideCur); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		var override *decimal.Decimal
		if overrideTxt != nil {
			if d, err := decimal.NewFromString(*overrideTxt); err == nil {
				override = &d
			}
		}
		newPlan := plan
		if newPlan == "" {
			newPlan = cur
			if cur == "trial" {
				newPlan = "starter" // the first paid period after a trial
			}
		}
		if newPlan == "trial" {
			return nil, badRequest("unknown_plan")
		}
		lim, err := loadPlanLimits(ctx, tx, newPlan)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		if cur == "trial" {
			paid = nil // a trial has no paid days
		}
		from, to, err := renewPeriod(paid, today, period, until)
		if err != nil {
			return nil, err
		}
		switch period {
		case "year":
			cycle = "yearly"
		case "month", "":
			cycle = "monthly"
		}
		// default amount: the price for that period (the organisation's own price, or the plan's)
		var pmTxt, pyTxt, pcur string
		if err := tx.QueryRow(ctx, `SELECT price_monthly::text, price_yearly::text, currency::text FROM platform_plans WHERE code = $1`, newPlan).
			Scan(&pmTxt, &pyTxt, &pcur); err != nil {
			return nil, err
		}
		pm, _ := decimal.NewFromString(pmTxt)
		py, _ := decimal.NewFromString(pyTxt)
		if currency == "" {
			currency = pcur
			if overrideCur != nil && *overrideCur != "" {
				currency = *overrideCur
			}
		}
		if amt == nil {
			var d decimal.Decimal
			switch {
			case override != nil && newPlan == cur:
				d = *override
				if period == "year" && cycle == "yearly" && py.IsZero() {
					d = override.Mul(decimal.NewFromInt(12))
				}
			case period == "year":
				d = py
			case period == "custom":
				months := int64((to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month()) + 1)
				if months < 1 {
					months = 1
				}
				d = pm.Mul(decimal.NewFromInt(months))
			default:
				d = pm
			}
			amt = &d
		}
		changed := newPlan != cur
		if _, err := tx.Exec(ctx, `UPDATE tenants SET plan = $2, paid_until = $3, billing_cycle = $4, trial_ends_at = CASE WHEN $5 THEN NULL ELSE trial_ends_at END,
			max_subscribers = CASE WHEN $6 THEN $7::int ELSE max_subscribers END,
			max_users = CASE WHEN $6 THEN $8::int ELSE max_users END,
			max_companies = CASE WHEN $6 THEN $9::int ELSE max_companies END
			WHERE id = $1`, id, newPlan, to, cycle, cur == "trial", changed, lim.subs, lim.users, lim.companies); err != nil {
			return nil, err
		}
		if err := setBlocked(ctx, tx, r, id, false, ""); err != nil { // a renewal lifts a block
			return nil, err
		}
		var pid string
		if err := tx.QueryRow(ctx, `INSERT INTO platform_payments (tenant_id, amount, currency, method, reference, plan, cycle, period_from, period_to,
			note, recorded_by_email) VALUES ($1, $2::numeric, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id::text`,
			id, amt.String(), currency, method, strings.TrimSpace(reference), newPlan, cycle, from, to, strings.TrimSpace(note), principal(r).Email).Scan(&pid); err != nil {
			return nil, err
		}
		if requestID != "" {
			if _, err := tx.Exec(ctx, `UPDATE platform_requests SET status = 'done', handled_at = now(), handled_by = $3
				WHERE id = $1 AND tenant_id = $2 AND status = 'open'`, requestID, id, principal(r).Email); err != nil {
				return nil, err
			}
		}
		if err := platformEvent(ctx, tx, r, id, "renew", map[string]any{"plan": newPlan, "from": from.Format(time.DateOnly),
			"to": to.Format(time.DateOnly), "amount": amt.String(), "currency": currency, "method": method, "payment_id": pid}); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE tenants t SET last_state = tenant_state(t) WHERE id = $1`, id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+platformTenantCols+platformTenantFrom+` WHERE t.id = $1`, id)
	})
}

// PATCH /platform/tenants/{id}/users/{uid} {is_active}
func (s *Server) platformUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, uid := r.PathValue("id"), r.PathValue("uid")
	var in struct {
		IsActive *bool `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(id) || !isUUID(uid) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if in.IsActive == nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if uid == principal(r).ID {
		writeErr(w, http.StatusBadRequest, "cannot_change_self")
		return
	}
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var email string
		err := tx.QueryRow(ctx, `UPDATE users SET is_active = $3 WHERE id = $1 AND tenant_id = $2 RETURNING email`, uid, id, *in.IsActive).Scan(&email)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if !*in.IsActive {
			if _, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, uid); err != nil {
				return nil, err
			}
		}
		action := "user_enable"
		if !*in.IsActive {
			action = "user_disable"
		}
		if err := platformEvent(ctx, tx, r, id, action, map[string]any{"email": email}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})
}

// ---------- payments, plans, requests ----------

func (s *Server) platformPayments(w http.ResponseWriter, r *http.Request) {
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if (from != "" && !validDate(from)) || (to != "" && !validDate(to)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT pp.id, pp.tenant_id, t.name AS tenant_name, pp.amount, pp.currency, pp.method, pp.reference, pp.plan, pp.cycle,
			pp.period_from, pp.period_to, pp.note, pp.recorded_by_email, pp.created_at
			FROM platform_payments pp JOIN tenants t ON t.id = pp.tenant_id
			WHERE ($1 = '' OR pp.created_at >= $1::date) AND ($2 = '' OR pp.created_at < $2::date + 1)
			ORDER BY pp.created_at DESC LIMIT 2000`, from, to)
	})
}

const platformPlanCols = `code, name_en, name_ar, name_fr, price_monthly, price_yearly, currency, max_subscribers, max_users, max_companies, apps, ai_monthly_limit,
	is_public, is_active, sort, (SELECT count(*) FROM tenants t WHERE t.plan = platform_plans.code) AS organisations`

func (s *Server) platformPlans(w http.ResponseWriter, r *http.Request) {
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+platformPlanCols+` FROM platform_plans ORDER BY sort, code`)
	})
}

type planInput struct {
	Code           string    `json:"code"`
	NameEn         *string   `json:"name_en"`
	NameAr         *string   `json:"name_ar"`
	NameFr         *string   `json:"name_fr"`
	PriceMonthly   *string   `json:"price_monthly"`
	PriceYearly    *string   `json:"price_yearly"`
	Currency       *string   `json:"currency"`
	MaxSubscribers *int      `json:"max_subscribers"` // 0 = unlimited
	MaxUsers       *int      `json:"max_users"`
	MaxCompanies   *int      `json:"max_companies"`
	AIMonthly      *int      `json:"ai_monthly_limit"` // AI requests per month, 0 = unlimited
	Apps           *[]string `json:"apps"`             // [] = every app
	IsPublic       *bool     `json:"is_public"`
	IsActive       *bool     `json:"is_active"`
	Sort           *int      `json:"sort"`
	ApplyLimits    bool      `json:"apply_limits"` // also give the new limits to organisations on this plan
}

func (p *planInput) valid() bool {
	for _, v := range []*string{p.PriceMonthly, p.PriceYearly} {
		if v != nil {
			if d, err := decimal.NewFromString(*v); err != nil || d.IsNegative() {
				return false
			}
		}
	}
	for _, n := range []*int{p.MaxSubscribers, p.MaxUsers, p.MaxCompanies, p.AIMonthly} {
		if n != nil && *n < 0 {
			return false
		}
	}
	if p.Currency != nil && !currencyRe.MatchString(*p.Currency) {
		return false
	}
	if p.NameEn != nil && strings.TrimSpace(*p.NameEn) == "" {
		return false
	}
	if p.Apps != nil {
		for _, a := range *p.Apps {
			if !slices.Contains(knownApps, a) {
				return false
			}
		}
	}
	return true
}

func zeroNil(n *int) (bool, *int) {
	if n == nil {
		return false, nil
	}
	if *n == 0 {
		return true, nil
	}
	return true, n
}

// sameApps: two plan app lists hold the same apps (nil = every app).
func sameApps(a, b []string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

func appsArg(a *[]string) (bool, []string) {
	if a == nil {
		return false, nil
	}
	if len(*a) == 0 {
		return true, nil
	}
	return true, *a
}

func (s *Server) platformCreatePlan(w http.ResponseWriter, r *http.Request) {
	var in planInput
	if !decodeBody(w, r, &in) {
		return
	}
	if !planCodeRe.MatchString(in.Code) || in.NameEn == nil || !in.valid() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	_, subs := zeroNil(in.MaxSubscribers)
	_, users := zeroNil(in.MaxUsers)
	_, comps := zeroNil(in.MaxCompanies)
	_, apps := appsArg(in.Apps)
	_, aiLimit := zeroNil(in.AIMonthly)
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		_, err := tx.Exec(ctx, `INSERT INTO platform_plans (code, name_en, name_ar, name_fr, price_monthly, price_yearly, currency, max_subscribers,
			max_users, max_companies, apps, is_public, is_active, sort, ai_monthly_limit)
			VALUES ($1, $2, coalesce($3, ''), coalesce($4, ''), coalesce($5::numeric, 0), coalesce($6::numeric, 0), coalesce($7, 'USD'), $8, $9, $10, $11,
			coalesce($12, true), coalesce($13, true), coalesce($14, 0), $15)`,
			in.Code, strings.TrimSpace(*in.NameEn), in.NameAr, in.NameFr, in.PriceMonthly, in.PriceYearly, in.Currency, subs, users, comps, apps,
			in.IsPublic, in.IsActive, in.Sort, aiLimit)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
		}
		if err != nil {
			return nil, err
		}
		if err := platformEvent(ctx, tx, r, "", "plan_create", map[string]any{"code": in.Code}); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+platformPlanCols+` FROM platform_plans WHERE code = $1`, in.Code)
	})
}

func (s *Server) platformUpdatePlan(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	var in planInput
	if !decodeBody(w, r, &in) {
		return
	}
	if !planCodeRe.MatchString(code) || !in.valid() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if code == "trial" && in.IsActive != nil && !*in.IsActive {
		writeErr(w, http.StatusBadRequest, "plan_required")
		return
	}
	setS, subs := zeroNil(in.MaxSubscribers)
	setU, users := zeroNil(in.MaxUsers)
	setC, comps := zeroNil(in.MaxCompanies)
	setA, apps := appsArg(in.Apps)
	setAI, aiLimit := zeroNil(in.AIMonthly)
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var oldApps []string
		if err := tx.QueryRow(ctx, `SELECT apps FROM platform_plans WHERE code = $1 FOR UPDATE`, code).Scan(&oldApps); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		appsChanged := setA && !sameApps(oldApps, apps)
		tag, err := tx.Exec(ctx, `UPDATE platform_plans SET
			name_en = coalesce($2, name_en), name_ar = coalesce($3, name_ar), name_fr = coalesce($4, name_fr),
			price_monthly = coalesce($5::numeric, price_monthly), price_yearly = coalesce($6::numeric, price_yearly), currency = coalesce($7, currency),
			max_subscribers = CASE WHEN $8 THEN $9::int ELSE max_subscribers END,
			max_users = CASE WHEN $10 THEN $11::int ELSE max_users END,
			max_companies = CASE WHEN $12 THEN $13::int ELSE max_companies END,
			apps = CASE WHEN $14 THEN $15::text[] ELSE apps END,
			is_public = coalesce($16, is_public), is_active = coalesce($17, is_active), sort = coalesce($18, sort),
			ai_monthly_limit = CASE WHEN $19 THEN $20::int ELSE ai_monthly_limit END
			WHERE code = $1`, code, in.NameEn, in.NameAr, in.NameFr, in.PriceMonthly, in.PriceYearly, in.Currency,
			setS, subs, setU, users, setC, comps, setA, apps, in.IsPublic, in.IsActive, in.Sort, setAI, aiLimit)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		applied := int64(0)
		if in.ApplyLimits {
			tag, err := tx.Exec(ctx, `UPDATE tenants t SET max_subscribers = p.max_subscribers, max_users = p.max_users, max_companies = p.max_companies
				FROM platform_plans p WHERE p.code = $1 AND t.plan = p.code`, code)
			if err != nil {
				return nil, err
			}
			applied = tag.RowsAffected()
		}
		// organisations on this plan keep the apps they already use that the new list leaves out
		grandfathered := 0
		if appsChanged {
			if err := tx.QueryRow(ctx, `SELECT grandfather_plan_apps($1)`, code).Scan(&grandfathered); err != nil {
				return nil, err
			}
		}
		if err := platformEvent(ctx, tx, r, "", "plan_update", map[string]any{"code": code, "applied_to": applied, "grandfathered": grandfathered}); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+platformPlanCols+` FROM platform_plans WHERE code = $1`, code)
	})
}

func (s *Server) platformRequests(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT q.id, q.tenant_id, t.name AS tenant_name, q.kind, q.plan, q.cycle, q.method, q.reference, q.note, q.status,
			q.created_at, q.handled_at, q.handled_by, q.answer, u.full_name AS created_by_name, u.email AS created_by_email, tenant_state(t) AS state,
			t.paid_until
			FROM platform_requests q JOIN tenants t ON t.id = q.tenant_id LEFT JOIN users u ON u.id = q.created_by
			WHERE ($1 = 'all' OR q.status = $1) ORDER BY q.created_at DESC LIMIT 500`, status)
	})
}

// POST /platform/requests/{id}/{op}: done | reject {answer}
func (s *Server) platformRequestAction(w http.ResponseWriter, r *http.Request) {
	id, op := r.PathValue("id"), r.PathValue("op")
	var in struct {
		Answer string `json:"answer"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(id) || (op != "done" && op != "reject") {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	status := map[string]string{"done": "done", "reject": "rejected"}[op]
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var tid string
		err := tx.QueryRow(ctx, `UPDATE platform_requests SET status = $2, answer = $3, handled_at = now(), handled_by = $4
			WHERE id = $1 AND status = 'open' RETURNING tenant_id::text`, id, status, strings.TrimSpace(in.Answer), principal(r).Email).Scan(&tid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if err := platformEvent(ctx, tx, r, tid, "request_"+status, map[string]any{"request_id": id, "answer": in.Answer}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})
}

func (s *Server) platformRunLifecycle(w http.ResponseWriter, r *http.Request) {
	res, err := s.RunPlatformLifecycle(r.Context())
	if err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------- the organisation's side: My Paxve subscription ----------

func (s *Server) mySubscription(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		sub, err := queryJSONOne(ctx, tx, `SELECT t.name, t.plan, tenant_state(t) AS state, t.trial_ends_at, t.paid_until, t.billing_cycle, t.grace_days,
			CASE WHEN t.paid_until IS NULL THEN NULL ELSE t.paid_until + t.grace_days END AS grace_ends,
			CASE WHEN t.plan = 'trial' THEN t.trial_ends_at::date ELSE t.paid_until END AS ends_on,
			(CASE WHEN t.plan = 'trial' THEN t.trial_ends_at::date ELSE t.paid_until END) - current_date AS days_left,
			coalesce(t.price_override, CASE WHEN t.billing_cycle = 'yearly' THEN p.price_yearly ELSE p.price_monthly END) AS price,
			coalesce(t.price_currency, p.currency) AS currency, p.name_en AS plan_name_en, p.name_ar AS plan_name_ar, p.name_fr AS plan_name_fr,
			t.max_subscribers, t.max_users, t.max_companies, t.contact_phone, t.contact_email, t.blocked_reason,
			(SELECT count(*) FROM subscriptions WHERE status = 'active') AS active_subscriptions,
			(SELECT count(*) FROM users WHERE is_active) AS active_users,
			(SELECT count(*) FROM companies WHERE is_active) AS active_companies
			FROM tenants t JOIN platform_plans p ON p.code = t.plan WHERE t.id = app_tenant()`)
		if err != nil {
			return nil, err
		}
		plans, err := queryJSON(ctx, tx, `SELECT code, name_en, name_ar, name_fr, price_monthly, price_yearly, currency, max_subscribers, max_users,
			max_companies FROM platform_plans WHERE is_active AND is_public ORDER BY sort, code`)
		if err != nil {
			return nil, err
		}
		payments, err := queryJSON(ctx, tx, `SELECT id, amount, currency, method, reference, plan, cycle, period_from, period_to, created_at
			FROM platform_payments ORDER BY created_at DESC LIMIT 100`)
		if err != nil {
			return nil, err
		}
		requests, err := queryJSON(ctx, tx, `SELECT id, kind, plan, cycle, method, reference, note, status, answer, created_at, handled_at
			FROM platform_requests ORDER BY created_at DESC LIMIT 50`)
		if err != nil {
			return nil, err
		}
		return map[string]any{"subscription": sub, "plans": plans, "payments": payments, "requests": requests,
			"contact": map[string]string{"phone": s.platformContactPhone, "email": s.platformContactEmail}}, nil
	})
}

// PATCH /subscription {contact_phone}: where the renewal reminders go
func (s *Server) updateMySubscription(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ContactPhone string `json:"contact_phone"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.ContactPhone) > 40 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE tenants SET contact_phone = $1 WHERE id = app_tenant()`, strings.TrimSpace(in.ContactPhone)); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})
}

// POST /subscription/requests {kind, plan, cycle, method, reference, note}
func (s *Server) createSubscriptionRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind      string `json:"kind"`
		Plan      string `json:"plan"`
		Cycle     string `json:"cycle"`
		Method    string `json:"method"`
		Reference string `json:"reference"`
		Note      string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Cycle == "" {
		in.Cycle = "monthly"
	}
	if in.Method == "" {
		in.Method = "other"
	}
	if (in.Kind != "renew" && in.Kind != "upgrade") || !planCodeRe.MatchString(in.Plan) || in.Plan == "trial" ||
		(in.Cycle != "monthly" && in.Cycle != "yearly") || !platformMethods[in.Method] || len(in.Reference) > 120 || len(in.Note) > 1000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var tenantName string
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform_plans WHERE code = $1 AND is_active AND is_public)`, in.Plan).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, badRequest("unknown_plan")
		}
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform_requests WHERE status = 'open'`).Scan(&open); err != nil {
			return nil, err
		}
		if open >= 3 {
			return nil, &apiError{status: http.StatusConflict, code: "request_open"}
		}
		if err := tx.QueryRow(ctx, `SELECT name FROM tenants WHERE id = app_tenant()`).Scan(&tenantName); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO platform_requests (kind, plan, cycle, method, reference, note) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id::text`, in.Kind, in.Plan, in.Cycle, in.Method, strings.TrimSpace(in.Reference), strings.TrimSpace(in.Note)).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, kind, plan, cycle, method, reference, note, status, created_at FROM platform_requests WHERE id = $1`, id)
	})
	// tell the platform owner (best effort, after the request is saved)
	if tenantName != "" && s.OwnerPool != nil && s.platformContactPhone != "" {
		text := "Paxve: " + tenantName + " asked to " + in.Kind + " (" + in.Plan + ", " + in.Cycle + ")"
		if in.Reference != "" {
			text += " ref " + in.Reference
		}
		go s.notifyFromOwner(context.WithoutCancel(r.Context()), s.platformContactPhone, "", "", text)
	}
}

// ---------- lifecycle worker: state changes and reminders ----------

func (s *Server) StartPlatformWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	go func() {
		timer := time.NewTimer(3 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if _, err := s.RunPlatformLifecycle(ctx); err != nil {
					slog.Error("platform lifecycle worker", "err", err)
				}
				timer.Reset(3 * time.Hour)
			}
		}
	}()
}

var reminderTexts = map[string]map[string]string{
	"en": {
		"d7":      "Paxve: the plan of {org} ends on {date} (in {days} days). Renew from Settings → My Paxve subscription.",
		"d3":      "Paxve: the plan of {org} ends on {date} (in {days} days). Renew now to keep working without interruption.",
		"d1":      "Paxve: the plan of {org} ends on {date}. Renew today to keep working.",
		"expired": "Paxve: the plan of {org} ended on {date}. Your data is safe; renew from Settings → My Paxve subscription.",
	},
	"ar": {
		"d7":      "Paxve: ينتهي اشتراك {org} بتاريخ {date} (بعد {days} أيام). جدّد من الإعدادات ← اشتراكي في Paxve.",
		"d3":      "Paxve: ينتهي اشتراك {org} بتاريخ {date} (بعد {days} أيام). جدّد الآن لتستمر بالعمل دون توقف.",
		"d1":      "Paxve: ينتهي اشتراك {org} بتاريخ {date}. جدّد اليوم لتستمر بالعمل.",
		"expired": "Paxve: انتهى اشتراك {org} بتاريخ {date}. بياناتك محفوظة؛ جدّد من الإعدادات ← اشتراكي في Paxve.",
	},
	"fr": {
		"d7":      "Paxve : l'abonnement de {org} se termine le {date} (dans {days} jours). Renouvelez depuis Paramètres → Mon abonnement Paxve.",
		"d3":      "Paxve : l'abonnement de {org} se termine le {date} (dans {days} jours). Renouvelez maintenant pour continuer sans interruption.",
		"d1":      "Paxve : l'abonnement de {org} se termine le {date}. Renouvelez aujourd'hui pour continuer.",
		"expired": "Paxve : l'abonnement de {org} a pris fin le {date}. Vos données sont conservées ; renouvelez depuis Paramètres → Mon abonnement Paxve.",
	},
}

// jobsStopped: background jobs (billing, dunning, recurring entries, messages, ISP actions, webhooks …) do not
// run for an expired or blocked organisation; its queued work waits and goes out after a renewal / unblock.
func jobsStopped(state string) bool { return state == "expired" || state == "blocked" }

// heldJobs counts the background work waiting because its organisation is expired or blocked.
func (s *Server) heldJobs(ctx context.Context) (map[string]int, error) {
	var msgs, acts, hooks, recur, comps int
	err := s.OwnerPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM messages WHERE status = 'queued' AND NOT tenant_jobs_ok(tenant_id)),
		(SELECT count(*) FROM router_actions WHERE status = 'queued' AND NOT tenant_jobs_ok(tenant_id)),
		(SELECT count(*) FROM webhook_deliveries WHERE status = 'pending' AND NOT tenant_jobs_ok(tenant_id)),
		(SELECT count(*) FROM recurring_templates WHERE is_active AND next_date <= current_date AND NOT tenant_jobs_ok(tenant_id)),
		(SELECT count(*) FROM companies WHERE is_active AND NOT tenant_jobs_ok(tenant_id))`).Scan(&msgs, &acts, &hooks, &recur, &comps)
	return map[string]int{"messages": msgs, "router_actions": acts, "webhooks": hooks, "recurring": recur, "companies": comps}, err
}

// RunPlatformLifecycle records state changes (trial → expired, active → grace → expired …) and sends the
// renewal reminders 7, 3 and 1 days before the end date and once on expiry (each only once per end date).
func (s *Server) RunPlatformLifecycle(ctx context.Context) (map[string]int, error) {
	res := map[string]int{"state_changes": 0, "reminders": 0}
	type row struct {
		id, name, state, last, email, phone, locale string
		ends                                        *time.Time
		days                                        *int
	}
	rows, err := s.OwnerPool.Query(ctx, `SELECT t.id::text, t.name, tenant_state(t), t.last_state, t.contact_email, t.contact_phone,
		coalesce((SELECT u.locale FROM users u WHERE u.tenant_id = t.id AND u.role = 'admin' ORDER BY u.created_at LIMIT 1), 'en'),
		CASE WHEN t.plan = 'trial' THEN t.trial_ends_at::date ELSE t.paid_until END,
		(CASE WHEN t.plan = 'trial' THEN t.trial_ends_at::date ELSE t.paid_until END) - current_date
		FROM tenants t`)
	if err != nil {
		return res, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.name, &x.state, &x.last, &x.email, &x.phone, &x.locale, &x.ends, &x.days)
		return x, err
	})
	if err != nil {
		return res, err
	}
	own := s.platformAdminTenant(ctx)
	if _, err := s.OwnerPool.Exec(ctx, `UPDATE tenants SET platform_owner = (id::text = $1) WHERE platform_owner <> (id::text = $1)`, own); err != nil {
		return res, err
	}
	for _, t := range list {
		if own == t.id {
			continue // the platform owner's own organisation
		}
		if t.state != t.last {
			err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE tenants SET last_state = $2 WHERE id = $1`, t.id, t.state); err != nil {
					return err
				}
				detail := map[string]any{"from": t.last, "to": t.state}
				if jobsStopped(t.state) != jobsStopped(t.last) {
					detail["jobs"] = map[bool]string{true: "paused", false: "resumed"}[jobsStopped(t.state)]
					slog.Info("platform: background jobs "+detail["jobs"].(string), "tenant", t.id, "state", t.state)
				}
				return platformEvent(ctx, tx, nil, t.id, "state", detail)
			})
			if err != nil {
				return res, err
			}
			res["state_changes"]++
		}
		if t.ends == nil || t.days == nil || t.state == "blocked" {
			continue
		}
		kind := ""
		switch d := *t.days; {
		case d < 0 && (t.state == "expired" || t.state == "grace"):
			kind = "expired"
		case d >= 0 && d <= 1:
			kind = "d1"
		case d > 1 && d <= 3:
			kind = "d3"
		case d > 3 && d <= 7:
			kind = "d7"
		}
		if kind == "" {
			continue
		}
		var inserted bool
		if err := s.OwnerPool.QueryRow(ctx, `WITH i AS (INSERT INTO platform_reminders (tenant_id, kind, for_date) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING RETURNING 1) SELECT EXISTS (SELECT 1 FROM i)`, t.id, kind, *t.ends).Scan(&inserted); err != nil {
			return res, err
		}
		if !inserted {
			continue
		}
		texts, ok := reminderTexts[t.locale]
		if !ok {
			texts = reminderTexts["en"]
		}
		days := *t.days
		if days < 0 {
			days = -days
		}
		text := strings.NewReplacer("{org}", t.name, "{date}", t.ends.Format(time.DateOnly), "{days}", strconv.Itoa(days)).Replace(texts[kind])
		channels := s.notifyFromOwner(ctx, t.phone, t.email, "Paxve", text)
		_, _ = s.OwnerPool.Exec(ctx, `UPDATE platform_reminders SET channels = $4 WHERE tenant_id = $1 AND kind = $2 AND for_date = $3`,
			t.id, kind, *t.ends, channels)
		_, _ = s.OwnerPool.Exec(ctx, `INSERT INTO platform_events (tenant_id, actor_email, action, detail) VALUES ($1, 'system', 'reminder', $2)`,
			t.id, map[string]any{"kind": kind, "for": t.ends.Format(time.DateOnly), "channels": channels})
		res["reminders"]++
	}
	held, err := s.heldJobs(ctx)
	if err != nil {
		return res, err
	}
	if held["companies"] > 0 {
		slog.Info("platform: background jobs skipped for expired / blocked organisations", "companies", held["companies"],
			"queued_messages", held["messages"], "router_actions", held["router_actions"], "webhooks", held["webhooks"], "recurring_due", held["recurring"])
	}
	res["held_companies"] = held["companies"]
	return res, nil
}

// platformAdminTenant: the organisation of the platform owner (its messaging settings send the reminders).
func (s *Server) platformAdminTenant(ctx context.Context) string {
	if len(s.platformAdmins) == 0 {
		return ""
	}
	emails := make([]string, 0, len(s.platformAdmins))
	for e := range s.platformAdmins {
		emails = append(emails, e)
	}
	var tid string
	_ = s.OwnerPool.QueryRow(ctx, `SELECT tenant_id::text FROM users WHERE email = ANY ($1) ORDER BY created_at LIMIT 1`, emails).Scan(&tid)
	return tid
}

// notifyFromOwner queues a WhatsApp/SMS (phone) and/or an e-mail through the messaging providers of the platform
// owner's first company. Returns the channels used ("" when none is set up).
func (s *Server) notifyFromOwner(ctx context.Context, phone, email, subject, text string) string {
	tid := s.platformAdminTenant(ctx)
	if tid == "" {
		return ""
	}
	var used []string
	err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
			return err
		}
		var cid, country string
		if err := tx.QueryRow(ctx, `SELECT id::text, coalesce(nullif(country, ''), 'LB') FROM companies WHERE is_active ORDER BY created_at LIMIT 1`).
			Scan(&cid, &country); err != nil {
			return err
		}
		if p := normalizePhone(phone, country); p != "" {
			prov, err := pickProvider(ctx, tx, cid, "")
			if err != nil {
				return err
			}
			if prov != nil {
				if _, err := tx.Exec(ctx, `INSERT INTO messages (company_id, phone, channel, provider_id, event, body) VALUES ($1, $2, $3, $4, 'platform_notice', $5)`,
					cid, p, channelFor(prov, "whatsapp"), prov.ID, text); err != nil {
					return err
				}
				used = append(used, channelFor(prov, "whatsapp"))
			}
		}
		if email = strings.TrimSpace(email); email != "" && emailRe.MatchString(email) {
			var pid string
			err := tx.QueryRow(ctx, `SELECT id::text FROM message_providers WHERE company_id = $1 AND kind = 'smtp' AND is_active
				ORDER BY is_default DESC, created_at LIMIT 1`, cid).Scan(&pid)
			if err == nil {
				if _, err := tx.Exec(ctx, `INSERT INTO messages (company_id, phone, email, subject, channel, provider_id, event, body)
					VALUES ($1, '', $2, $3, 'email', $4, 'platform_notice', $5)`, cid, email, subject, pid, text); err != nil {
					return err
				}
				used = append(used, "email")
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		slog.Warn("platform notice not queued", "err", err)
		return ""
	}
	return strings.Join(used, ",")
}
