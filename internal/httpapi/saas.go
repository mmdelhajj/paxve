package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/auth"
	"github.com/mmdelhajj/paxve/internal/charts"
	"github.com/mmdelhajj/paxve/internal/db"
)

// Plans, prices and limits live in platform_plans (edited from the platform console).
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
	// 20 sign-ups an hour per IP (an accountant or reseller onboarding several clients from one office), and the
	// answer says how long to wait (audit P7-F3)
	if !s.signupLimiter.allow(ip) {
		tooMany(w, s.signupLimiter, ip)
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
		BusinessType  string `json:"business_type"`
		Phone         string `json:"phone"`
		Rate          string `json:"rate"` // local currency per 1 base currency (e.g. 89500 LBP per USD)
	}
	if !decodeBody(w, r, &in) {
		return
	}
	s.signupLimiter.fail(ip) // every attempt counts
	in.Phone = strings.TrimSpace(in.Phone)
	if in.BusinessType == "" {
		in.BusinessType = "generator"
	}
	var rate decimal.Decimal
	if in.Rate = strings.TrimSpace(strings.ReplaceAll(in.Rate, ",", "")); in.Rate != "" {
		d, err := decimal.NewFromString(in.Rate)
		if err != nil || !d.IsPositive() {
			writeErr(w, http.StatusBadRequest, "bad_rate")
			return
		}
		rate = d
	}
	if !slices.Contains(businessTypes, in.BusinessType) || len(in.Phone) > 40 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
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
		if err := tx.QueryRow(ctx, `INSERT INTO tenants (name, slug, plan, trial_ends_at, max_subscribers, max_users, max_companies, contact_email, last_state)
			SELECT $1, $2, p.code, now() + make_interval(days => $3), p.max_subscribers, p.max_users, p.max_companies, $4, 'trial'
			FROM platform_plans p WHERE p.code = 'trial' RETURNING id::text`,
			in.Organisation, slug, trialDays, in.Email).Scan(&tid); err != nil {
			return err
		}
		if in.Phone != "" {
			if _, err := tx.Exec(ctx, `UPDATE tenants SET contact_phone = $2 WHERE id = $1`, tid, in.Phone); err != nil {
				return err
			}
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
		// the company starts with the apps of its business type only (audit P7-F1) and speaks the owner's language
		if err := tx.QueryRow(ctx, `INSERT INTO companies (name_en, name_ar, base_currency, local_currency, default_locale, business_type, apps)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`, in.CompanyName, nameAr, in.BaseCurrency, in.LocalCurrency,
			in.Locale, in.BusinessType, appsForBusiness(in.BusinessType)).Scan(&cid); err != nil {
			return err
		}
		if !rate.IsZero() && in.LocalCurrency != in.BaseCurrency {
			if _, err := tx.Exec(ctx, `INSERT INTO exchange_rates (company_id, currency, rate_date, kind, rate) VALUES ($1, $2, $3::date, 'official', $4::numeric)`,
				cid, in.LocalCurrency, time.Now().In(beirut).Format(time.DateOnly), rate.String()); err != nil {
				return err
			}
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
