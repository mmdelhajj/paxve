package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Sign-in hardening (phase 8):
//   - Google and Microsoft sign-in (OpenID Connect, authorization code + PKCE). Offered only when the platform set the
//     client id / secret (PAXVE_GOOGLE_CLIENT_ID / _SECRET, PAXVE_MS_CLIENT_ID / _SECRET, PAXVE_MS_TENANT) and the
//     organisation allows it. Only existing, active users can sign in this way (matched on the verified e-mail); the
//     organisation's two-step sign-in still applies after it.
//     The ID token comes straight from the provider's token endpoint over TLS, so its issuer is the provider
//     (OpenID Connect Core 3.1.3.7); audience, issuer, expiry and nonce are checked.
//   - password policy: complexity (3 of: lower case, upper case, digit, symbol) and expiry in days.
//   - sign-in history: every sign-in and every wrong password of a known account.
//   - idle logout per company (companies.idle_logout_min): the app signs out after that much inactivity, and the
//     server ends sessions not seen for that long.

type oidcProvider struct {
	Key, AuthURL, TokenURL, ClientID, Secret string
	issuerOK                                 func(string) bool
}

func oidcProviders() map[string]oidcProvider {
	out := map[string]oidcProvider{}
	if id, sec := os.Getenv("PAXVE_GOOGLE_CLIENT_ID"), os.Getenv("PAXVE_GOOGLE_CLIENT_SECRET"); id != "" && sec != "" {
		out["google"] = oidcProvider{Key: "google", AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token",
			ClientID: id, Secret: sec, issuerOK: func(iss string) bool { return iss == "https://accounts.google.com" || iss == "accounts.google.com" }}
	}
	if id, sec := os.Getenv("PAXVE_MS_CLIENT_ID"), os.Getenv("PAXVE_MS_CLIENT_SECRET"); id != "" && sec != "" {
		t := os.Getenv("PAXVE_MS_TENANT")
		if t == "" {
			t = "common"
		}
		out["microsoft"] = oidcProvider{Key: "microsoft", AuthURL: "https://login.microsoftonline.com/" + url.PathEscape(t) + "/oauth2/v2.0/authorize",
			TokenURL: "https://login.microsoftonline.com/" + url.PathEscape(t) + "/oauth2/v2.0/token", ClientID: id, Secret: sec,
			issuerOK: func(iss string) bool {
				return strings.HasPrefix(iss, "https://login.microsoftonline.com/") && strings.HasSuffix(iss, "/v2.0")
			}}
	}
	return out
}

// tests replace the provider endpoints
var oidcHTTP = &http.Client{Timeout: 15 * time.Second}
var oidcOverride func(map[string]oidcProvider)

func activeProviders() map[string]oidcProvider {
	p := oidcProviders()
	if oidcOverride != nil {
		oidcOverride(p)
	}
	return p
}

func oidcRedirect(p string) string { return publicURL() + v1 + "/auth/oidc/" + p + "/callback" }

// GET /auth/oidc/providers: the providers this server offers (the sign-in page shows their buttons)
func (s *Server) oidcList(w http.ResponseWriter, r *http.Request) {
	out := []string{}
	for _, k := range []string{"google", "microsoft"} {
		if _, ok := activeProviders()[k]; ok {
			out = append(out, k)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func oidcRand(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// GET /auth/oidc/{provider}/start: to the provider's sign-in page
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	p, ok := activeProviders()[r.PathValue("provider")]
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if !s.oidcLimiter.allow(clientIP(r)) {
		http.Redirect(w, r, publicURL()+"/#/login?sso_error=too_many_attempts", http.StatusFound)
		return
	}
	ctx := r.Context()
	state, nonce, verifier := oidcRand(24), oidcRand(24), oidcRand(48)
	sum := sha256.Sum256([]byte(verifier))
	s.Sessions.Put(ctx, "oidc_state", state)
	s.Sessions.Put(ctx, "oidc_nonce", nonce)
	s.Sessions.Put(ctx, "oidc_verifier", verifier)
	s.Sessions.Put(ctx, "oidc_provider", p.Key)
	s.Sessions.Put(ctx, "oidc_at", strconv.FormatInt(time.Now().Unix(), 10))
	q := url.Values{"client_id": {p.ClientID}, "response_type": {"code"}, "scope": {"openid email profile"}, "redirect_uri": {oidcRedirect(p.Key)},
		"state": {state}, "nonce": {nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"prompt": {"select_account"}}
	http.Redirect(w, r, p.AuthURL+"?"+q.Encode(), http.StatusFound)
}

type idClaims struct {
	Iss           string          `json:"iss"`
	Aud           json.RawMessage `json:"aud"`
	Exp           int64           `json:"exp"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified any             `json:"email_verified"`
	PreferredName string          `json:"preferred_username"`
}

func (c *idClaims) audIs(id string) bool {
	var one string
	if json.Unmarshal(c.Aud, &one) == nil {
		return one == id
	}
	var many []string
	if json.Unmarshal(c.Aud, &many) == nil {
		for _, a := range many {
			if a == id {
				return true
			}
		}
	}
	return false
}

// GET /auth/oidc/{provider}/callback?code&state: the provider sends the browser back here.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	back := func(code string) {
		http.Redirect(w, r, publicURL()+"/#/login?sso_error="+url.QueryEscape(code), http.StatusFound)
	}
	p, ok := activeProviders()[r.PathValue("provider")]
	if !ok {
		back("not_found")
		return
	}
	ip := clientIP(r)
	if !s.oidcLimiter.allow(ip) {
		back("too_many_attempts")
		return
	}
	state, nonce, verifier := s.Sessions.GetString(ctx, "oidc_state"), s.Sessions.GetString(ctx, "oidc_nonce"), s.Sessions.GetString(ctx, "oidc_verifier")
	at, _ := strconv.ParseInt(s.Sessions.GetString(ctx, "oidc_at"), 10, 64)
	prov := s.Sessions.GetString(ctx, "oidc_provider")
	for _, k := range []string{"oidc_state", "oidc_nonce", "oidc_verifier", "oidc_provider", "oidc_at"} {
		s.Sessions.Remove(ctx, k)
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		back("sso_cancelled")
		return
	}
	if state == "" || q.Get("state") != state || prov != p.Key || time.Since(time.Unix(at, 0)) > 10*time.Minute {
		s.oidcLimiter.fail(ip)
		back("sso_state")
		return
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {oidcRedirect(p.Key)},
		"client_id": {p.ClientID}, "client_secret": {p.Secret}, "code_verifier": {verifier}}
	resp, err := oidcHTTP.PostForm(p.TokenURL, form)
	if err != nil {
		slog.Error("oidc token", "provider", p.Key, "err", err)
		back("sso_failed")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &tok) != nil || tok.IDToken == "" {
		s.oidcLimiter.fail(ip)
		back("sso_failed")
		return
	}
	parts := strings.Split(tok.IDToken, ".")
	var cl idClaims
	if len(parts) != 3 {
		back("sso_failed")
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(raw, &cl) != nil || !p.issuerOK(cl.Iss) || !cl.audIs(p.ClientID) || cl.Nonce != nonce || time.Now().Unix() > cl.Exp {
		s.oidcLimiter.fail(ip)
		back("sso_failed")
		return
	}
	email := strings.ToLower(strings.TrimSpace(cl.Email))
	if p.Key == "google" && cl.EmailVerified != true && cl.EmailVerified != "true" {
		email = ""
	}
	if email == "" && p.Key == "microsoft" {
		email = strings.ToLower(strings.TrimSpace(cl.PreferredName))
	}
	if email == "" || !strings.Contains(email, "@") {
		back("sso_no_email")
		return
	}
	var uid, tid, hash string
	var active bool
	err = s.Pool.QueryRow(ctx, `SELECT user_id::text, tenant_id::text, password_hash, is_active FROM auth_lookup($1)`, email).Scan(&uid, &tid, &hash, &active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		s.oidcLimiter.fail(ip)
		back("sso_unknown_user")
		return
	}
	if err != nil {
		slog.Error("oidc lookup", "err", err)
		back("sso_failed")
		return
	}
	sc := db.Scope{TenantID: tid, UserID: uid, IP: ip}
	var allowed, mfaOn bool
	if err := db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT sso_allowed, coalesce((SELECT enabled_at IS NOT NULL FROM user_mfa WHERE user_id = app_user()), false)
			FROM tenants WHERE id = app_tenant()`).Scan(&allowed, &mfaOn); err != nil {
			return err
		}
		if !allowed {
			return insertLoginEvent(ctx, tx, uid, email, r, p.Key, false, "sso_not_allowed")
		}
		return nil
	}); err != nil {
		slog.Error("oidc tenant", "err", err)
		back("sso_failed")
		return
	}
	if !allowed {
		back("sso_not_allowed")
		return
	}
	if err := s.Sessions.RenewToken(ctx); err != nil {
		back("sso_failed")
		return
	}
	if mfaOn {
		s.Sessions.Remove(ctx, "uid")
		s.Sessions.Remove(ctx, "tid")
		s.Sessions.Put(ctx, "mfa_uid", uid)
		s.Sessions.Put(ctx, "mfa_tid", tid)
		s.Sessions.Put(ctx, "mfa_at", strconv.FormatInt(time.Now().Unix(), 10))
		http.Redirect(w, r, publicURL()+"/#/login?sso=mfa", http.StatusFound)
		return
	}
	if _, err := s.startSession(r, sc, false, p.Key); err != nil {
		slog.Error("oidc session", "err", err)
		back("sso_failed")
		return
	}
	http.Redirect(w, r, publicURL()+"/#/dashboard", http.StatusFound)
}

// ---------- password policy ----------

func complexEnough(pw string) bool {
	var lower, upper, digit, other bool
	for _, c := range pw {
		switch {
		case unicode.IsLower(c):
			lower = true
		case unicode.IsUpper(c):
			upper = true
		case unicode.IsDigit(c):
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			n++
		}
	}
	return n >= 3
}

// ---------- sign-in history ----------

func insertLoginEvent(ctx context.Context, tx pgx.Tx, uid, email string, r *http.Request, method string, ok bool, reason string) error {
	_, err := tx.Exec(ctx, `INSERT INTO login_events (user_id, email, ip, user_agent, method, success, reason)
		VALUES (nullif($1, '')::uuid, coalesce(nullif($2, ''), (SELECT email FROM users WHERE id = nullif($1, '')::uuid), ''),
		nullif($3, '')::inet, left($4, 300), $5, $6, $7)`, uid, email, clientIP(r), r.UserAgent(), method, ok, reason)
	return err
}

func (s *Server) recordLoginFailure(ctx context.Context, tid, uid, email string, r *http.Request, method, reason string) {
	if err := db.InTenant(ctx, s.Pool, db.Scope{TenantID: tid, IP: clientIP(r)}, func(tx pgx.Tx) error {
		return insertLoginEvent(ctx, tx, uid, email, r, method, false, reason)
	}); err != nil {
		slog.Warn("login event", "err", err)
	}
}

const loginEventCols = `e.id, e.at, e.email, host(e.ip) AS ip, e.user_agent, e.method, e.success, e.reason, e.user_id, u.full_name`

// GET /me/login-history: the user's own last sign-ins
func (s *Server) myLoginHistory(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+loginEventCols+` FROM login_events e LEFT JOIN users u ON u.id = e.user_id
			WHERE e.user_id = app_user() ORDER BY e.at DESC LIMIT 50`)
	})
}

// GET /security/login-history?user_id=&failed=1: the organisation's sign-ins (admins)
func (s *Server) orgLoginHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	uid := q.Get("user_id")
	if uid != "" && !isUUID(uid) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+loginEventCols+` FROM login_events e LEFT JOIN users u ON u.id = e.user_id
			WHERE ($1 = '' OR e.user_id = nullif($1, '')::uuid) AND ($2 <> '1' OR NOT e.success) ORDER BY e.at DESC LIMIT 300`, uid, q.Get("failed"))
	})
}

// GET /security/sso: which providers the platform configured and whether the organisation allows them
func (s *Server) ssoStatus(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var allowed bool
		if err := tx.QueryRow(ctx, `SELECT sso_allowed FROM tenants WHERE id = app_tenant()`).Scan(&allowed); err != nil {
			return nil, err
		}
		prov := map[string]bool{}
		for _, k := range []string{"google", "microsoft"} {
			_, ok := activeProviders()[k]
			prov[k] = ok
		}
		return map[string]any{"allowed": allowed, "providers": prov, "redirect_google": oidcRedirect("google"), "redirect_microsoft": oidcRedirect("microsoft")}, nil
	})
}

// PUT /companies/{cid}/idle-logout {minutes}: 0 = never, else 5..1440
func (s *Server) putIdleLogout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Minutes int `json:"minutes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Minutes != 0 && (in.Minutes < 5 || in.Minutes > 1440) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE companies SET idle_logout_min = $2, updated_at = now() WHERE id = $1`, r.PathValue("cid"), in.Minutes)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]int{"minutes": in.Minutes}, nil
	})
}

func (s *Server) getIdleLogout(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT idle_logout_min AS minutes FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}
