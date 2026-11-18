package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/auth"
	"github.com/mmdelhajj/paxve/internal/charts"
	"github.com/mmdelhajj/paxve/internal/db"
)

// Plan limits on active subscriptions (nil = unlimited).
var planLimits = map[string]*int{"trial": ptr(50), "starter": ptr(300), "business": ptr(1500), "pro": nil}

const trialDays = 14

func ptr(i int) *int { return &i }

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugBase(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 30 {
		s = strings.Trim(s[:30], "-")
	}
	if len(s) < 2 {
		s = "org"
	}
	return s
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// signup creates a new organisation (tenant), its first company and admin user, loads a chart of
// accounts, starts a 14-day trial and signs the user in. It runs on the owner pool because no
// tenant exists yet; every row it writes carries the new tenant's id.
func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.signupLimiter.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	var in struct {
		Organisation  string `json:"organisation"`
		CompanyName   string `json:"company_name"`
		BaseCurrency  string `json:"base_currency"`
		LocalCurrency string `json:"local_currency"`
		Template      string `json:"template"`
		FullName      string `json:"full_name"`
		Email         string `json:"email"`
		Password      string `json:"password"`
		Locale        string `json:"locale"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	s.signupLimiter.fail(ip) // every attempt counts
	in.Organisation = strings.TrimSpace(in.Organisation)
	in.CompanyName = strings.TrimSpace(in.CompanyName)
	in.FullName = strings.TrimSpace(in.FullName)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if in.CompanyName == "" {
		in.CompanyName = in.Organisation
	}
	if in.BaseCurrency == "" {
		in.BaseCurrency = "USD"
	}
	if in.LocalCurrency == "" {
		in.LocalCurrency = in.BaseCurrency
	}
	if !supportedLocales[in.Locale] {
		in.Locale = "en"
	}
	tpl := charts.Find(in.Template)
	if in.Organisation == "" || in.FullName == "" || !strings.Contains(in.Email, "@") || len(in.Email) > 200 ||
		!currencyRe.MatchString(in.BaseCurrency) || !currencyRe.MatchString(in.LocalCurrency) || tpl == nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if len(in.Password) < auth.MinLength {
		writeErr(w, http.StatusBadRequest, "weak_password")
		return
	}
	// Platform-owner emails can never be claimed through public sign-up (otherwise anyone could
	// register a listed address that has no account yet and get the platform console).
	if s.platformAdmins[in.Email] {
		writeErr(w, http.StatusConflict, "email_taken")
		return
	}
	hash, err := auth.Hash(in.Password)
	if err != nil {
		serverErr(w, r, err)
		return
	}
	ctx := r.Context()
	var tid, uid string
	err = pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)`, in.Email).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return &apiError{status: http.StatusConflict, code: "email_taken"}
		}
		base := slugBase(in.Organisation)
		slug := base
		for i := 0; ; i++ {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenants WHERE slug = $1)`, slug).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				break
			}
			if i > 5 {
				return &apiError{status: http.StatusConflict, code: "duplicate"}
			}
			slug = base + "-" + randHex(2)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tenants (name, slug, plan, trial_ends_at, max_subscribers)
			VALUES ($1, $2, 'trial', now() + make_interval(days => $3), $4) RETURNING id::text`,
			in.Organisation, slug, trialDays, *planLimits["trial"]).Scan(&tid); err != nil {
			return err
		}
		// From here on, column defaults (tenant_id = app_tenant()) and audit triggers see the new tenant.
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true), set_config('app.ip', $2, true)`, tid, ip); err != nil {
			return err
		}
		var cid string
		nameAr := ""
		if in.Locale == "ar" {
			nameAr = in.CompanyName
		}
		if err := tx.QueryRow(ctx, `INSERT INTO companies (name_en, name_ar, base_currency, local_currency)
			VALUES ($1, $2, $3, $4) RETURNING id::text`, in.CompanyName, nameAr, in.BaseCurrency, in.LocalCurrency).Scan(&cid); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO users (email, full_name, password_hash, role, locale)
			VALUES ($1, $2, $3, 'admin', $4) RETURNING id::text`, in.Email, in.FullName, hash, in.Locale).Scan(&uid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.user_id', $1, true)`, uid); err != nil {
			return err
		}
		return loadTemplate(ctx, tx, cid, tpl, in.Locale)
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := s.Sessions.RenewToken(ctx); err != nil {
		serverErr(w, r, err)
		return
	}
	s.Sessions.Put(ctx, "uid", uid)
	s.Sessions.Put(ctx, "tid", tid)
	p, err := s.loadPrincipal(ctx, db.Scope{TenantID: tid, UserID: uid, IP: ip})
	if err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// checkPlanLimit refuses a new active subscription when the organisation's plan is full or its trial ended.
func checkPlanLimit(ctx context.Context, tx pgx.Tx) error {
	var plan string
	var max *int
	var trialEnds *time.Time
	if err := tx.QueryRow(ctx, `SELECT plan, max_subscribers, trial_ends_at FROM tenants WHERE id = app_tenant()`).
		Scan(&plan, &max, &trialEnds); err != nil {
		return err
	}
	if plan == "trial" && trialEnds != nil && trialEnds.Before(time.Now()) {
		return &apiError{status: http.StatusPaymentRequired, code: "trial_expired"}
	}
	if max == nil {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE status = 'active'`).Scan(&n); err != nil {
		return err
	}
	if n >= *max {
		return &apiError{status: http.StatusPaymentRequired, code: "plan_limit", detail: strconv.Itoa(*max)}
	}
	return nil
}

// ---- platform owner console ----

func (s *Server) platformOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		if !p.IsPlatformAdmin || p.APIKeyID != "" {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r)
	}
}

// ownerRun is like run but on the owner pool (sees every tenant). Only for platform admins.
func (s *Server) ownerRun(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, tx pgx.Tx) (any, error)) {
	var out any
	err := pgx.BeginFunc(r.Context(), s.OwnerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT set_config('app.user_id', $1, true), set_config('app.ip', $2, true)`,
			principal(r).ID, clientIP(r)); err != nil {
			return err
		}
		var err error
		out, err = fn(r.Context(), tx)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

const platformTenantCols = `t.id, t.name, t.slug, t.plan, t.trial_ends_at, t.max_subscribers, t.is_active, t.suspended_reason, t.created_at,
	(SELECT count(*) FROM users u WHERE u.tenant_id = t.id) AS users,
	(SELECT count(*) FROM companies c WHERE c.tenant_id = t.id) AS companies,
	(SELECT count(*) FROM subscriptions s WHERE s.tenant_id = t.id AND s.status = 'active') AS active_subscriptions,
	(SELECT min(u.email) FROM users u WHERE u.tenant_id = t.id AND u.role = 'admin') AS admin_email`

func (s *Server) platformTenants(w http.ResponseWriter, r *http.Request) {
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+platformTenantCols+` FROM tenants t ORDER BY t.created_at DESC`)
	})
}

func (s *Server) platformUpdateTenant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Plan            *string `json:"plan"`
		IsActive        *bool   `json:"is_active"`
		TrialEndsAt     *string `json:"trial_ends_at"`   // YYYY-MM-DD, "" clears
		MaxSubscribers  *int    `json:"max_subscribers"` // 0 = unlimited
		SuspendedReason *string `json:"suspended_reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if in.Plan != nil {
		if _, ok := planLimits[*in.Plan]; !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	if in.TrialEndsAt != nil && *in.TrialEndsAt != "" && !validDate(*in.TrialEndsAt) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.MaxSubscribers != nil && *in.MaxSubscribers < 0 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.IsActive != nil && !*in.IsActive && id == principal(r).TenantID {
		writeErr(w, http.StatusBadRequest, "cannot_change_self")
		return
	}
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		// changing the plan without an explicit limit applies that plan's standard limit
		setMax, maxVal := false, (*int)(nil)
		if in.MaxSubscribers != nil {
			setMax = true
			if *in.MaxSubscribers > 0 {
				maxVal = in.MaxSubscribers
			}
		} else if in.Plan != nil {
			setMax, maxVal = true, planLimits[*in.Plan]
		}
		tag, err := tx.Exec(ctx, `UPDATE tenants SET
			plan = coalesce($2, plan),
			is_active = coalesce($3, is_active),
			trial_ends_at = CASE WHEN $4::text IS NULL THEN trial_ends_at WHEN $4 = '' THEN NULL ELSE ($4::date + interval '1 day' - interval '1 second') END,
			max_subscribers = CASE WHEN $5 THEN $6::int ELSE max_subscribers END,
			suspended_reason = coalesce($7, suspended_reason)
			WHERE id = $1`, id, in.Plan, in.IsActive, in.TrialEndsAt, setMax, maxVal, in.SuspendedReason)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+platformTenantCols+` FROM tenants t WHERE t.id = $1`, id)
	})
}
