package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/auth"
	"github.com/mmdelhajj/paxve/internal/db"
)

// mfaPendingTTL: time allowed between the password step and the two-factor code.
const mfaPendingTTL = 5 * time.Minute

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := clientIP(r)
	if !s.loginIP.allow(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_attempts", "detail": "900"})
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	emailKey := "email:" + email
	pairKey := "pair:" + email + "|" + ip
	// the per-account lock (guessing one password from many addresses) does not hold back an address this account
	// signed in from recently, so an attacker cannot lock the real user out (audit C-L3)
	d := s.loginPair.wait(pairKey)
	if !s.loginTrust.has(pairKey) {
		d = max(d, s.loginEmail.wait(emailKey))
	}
	if d > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_attempts", "detail": strconv.Itoa(int(d.Seconds()) + 1)})
		return
	}
	failed := func() {
		s.loginIP.fail(ip)
		s.loginPair.fail(pairKey)
		s.loginEmail.fail(emailKey)
	}

	var uid, tid, hash string
	var active bool
	err := s.Pool.QueryRow(ctx,
		`SELECT user_id::text, tenant_id::text, password_hash, is_active FROM auth_lookup($1)`,
		strings.TrimSpace(in.Email)).Scan(&uid, &tid, &hash, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		auth.VerifyDummy(in.Password)
		failed()
		writeErr(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if err != nil {
		serverErr(w, r, err)
		return
	}
	if !auth.Verify(in.Password, hash) {
		failed()
		s.recordLoginFailure(ctx, tid, uid, email, r, "password", "wrong_password")
		writeErr(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if !active {
		// the password is right: say why when the whole organisation is blocked (an off user stays generic)
		if reason, blocked := s.tenantBlocked(ctx, tid, uid); blocked {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "org_blocked", "detail": reason})
			return
		}
		failed()
		writeErr(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	s.loginPair.reset(pairKey)
	s.loginEmail.reset(emailKey)
	s.loginTrust.add(pairKey)

	sc := db.Scope{TenantID: tid, UserID: uid, IP: ip}
	var mfaOn bool
	if err := db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT enabled_at IS NOT NULL FROM user_mfa WHERE user_id = app_user()`).Scan(&mfaOn)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}); err != nil {
		serverErr(w, r, err)
		return
	}
	if err := s.Sessions.RenewToken(ctx); err != nil {
		serverErr(w, r, err)
		return
	}
	if mfaOn {
		// the password is right; the session is not signed in until the second factor is given
		s.Sessions.Remove(ctx, "uid")
		s.Sessions.Remove(ctx, "tid")
		s.Sessions.Put(ctx, "mfa_uid", uid)
		s.Sessions.Put(ctx, "mfa_tid", tid)
		s.Sessions.Put(ctx, "mfa_at", strconv.FormatInt(time.Now().Unix(), 10))
		writeJSON(w, http.StatusOK, map[string]any{"mfa_required": true})
		return
	}
	s.completeLogin(w, r, sc, false)
}

// completeLogin signs the session in: tracked session row, audit entry, principal.
func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, sc db.Scope, mfa bool) {
	method := "password"
	if mfa {
		method = "mfa"
	}
	p, err := s.startSession(r, sc, mfa, method)
	if err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// startSession signs the browser session in: tracked session row, audit entry, sign-in history, principal.
func (s *Server) startSession(r *http.Request, sc db.Scope, mfa bool, method string) (*Principal, error) {
	ctx := r.Context()
	var sid string
	err := db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO user_sessions (user_id, ip, user_agent, mfa)
			VALUES (app_user(), nullif($1, '')::inet, left($2, 300), $3) RETURNING id::text`,
			sc.IP, r.UserAgent(), mfa).Scan(&sid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_log (tenant_id, user_id, action, entity, entity_id, ip, after)
			VALUES (app_tenant(), app_user(), 'login', 'users', app_user(), nullif($1, '')::inet, jsonb_build_object('mfa', $2::boolean))`,
			sc.IP, mfa)
		if err != nil {
			return err
		}
		return insertLoginEvent(ctx, tx, sc.UserID, "", r, method, true, "")
	})
	if err != nil {
		return nil, err
	}
	s.Sessions.Put(ctx, "uid", sc.UserID)
	s.Sessions.Put(ctx, "tid", sc.TenantID)
	s.Sessions.Put(ctx, "sid", sid)
	p, err := s.loadPrincipal(ctx, sc)
	if err != nil {
		return nil, err
	}
	p.SessionID = sid
	return p, nil
}

// loginMFA is the second sign-in step: a code from the authenticator app or a one-time recovery code.
func (s *Server) loginMFA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Code         string `json:"code"`
		RecoveryCode string `json:"recovery_code"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	uid, tid := s.Sessions.GetString(ctx, "mfa_uid"), s.Sessions.GetString(ctx, "mfa_tid")
	at, _ := strconv.ParseInt(s.Sessions.GetString(ctx, "mfa_at"), 10, 64)
	if uid == "" || tid == "" || time.Since(time.Unix(at, 0)) > mfaPendingTTL {
		writeErr(w, http.StatusUnauthorized, "mfa_expired")
		return
	}
	key := "mfa:" + uid
	if !s.mfaLimiter.allow(key) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	sc := db.Scope{TenantID: tid, UserID: uid, IP: clientIP(r)}
	var ok bool
	err := db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		var err error
		ok, err = checkSecondFactor(ctx, tx, in.Code, in.RecoveryCode)
		return err
	})
	if err != nil {
		serverErr(w, r, err)
		return
	}
	if !ok {
		s.mfaLimiter.fail(key)
		writeErr(w, http.StatusUnauthorized, "invalid_code")
		return
	}
	s.mfaLimiter.reset(key)
	for _, k := range []string{"mfa_uid", "mfa_tid", "mfa_at"} {
		s.Sessions.Remove(ctx, k)
	}
	if err := s.Sessions.RenewToken(ctx); err != nil {
		serverErr(w, r, err)
		return
	}
	s.completeLogin(w, r, sc, true)
}

// checkSecondFactor verifies a TOTP code (no replay) or consumes a recovery code of the current user.
func checkSecondFactor(ctx context.Context, tx pgx.Tx, code, recovery string) (bool, error) {
	var secretEnc string
	var lastStep int64
	var hashes []string
	err := tx.QueryRow(ctx, `SELECT secret_enc, last_step, recovery_hashes FROM user_mfa
		WHERE user_id = app_user() AND enabled_at IS NOT NULL FOR UPDATE`).Scan(&secretEnc, &lastStep, &hashes)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(recovery) != "" {
		h := auth.HashRecoveryCode(recovery)
		for i, x := range hashes {
			if x == h {
				rest := append(append([]string{}, hashes[:i]...), hashes[i+1:]...)
				_, err := tx.Exec(ctx, `UPDATE user_mfa SET recovery_hashes = $1 WHERE user_id = app_user()`, rest)
				return err == nil, err
			}
		}
		return false, nil
	}
	secret := openSecret(secretEnc)
	if secret == "" {
		return false, nil
	}
	step, ok := auth.VerifyTOTP(secret, code, time.Now(), lastStep)
	if !ok {
		return false, nil
	}
	_, err = tx.Exec(ctx, `UPDATE user_mfa SET last_step = $1 WHERE user_id = app_user()`, step)
	return err == nil, err
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if p := principal(r); p.SessionID != "" {
		_ = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now(), revoked_by = app_user()
				WHERE id = $1 AND revoked_at IS NULL`, p.SessionID)
			return err
		})
	}
	if err := s.Sessions.Destroy(ctx); err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, principal(r))
}

// passwordProblem applies the organisation's password policy ("" = fine).
func passwordProblem(ctx context.Context, tx pgx.Tx, pw, email string) (string, error) {
	var minLen int
	var complexity bool
	if err := tx.QueryRow(ctx, `SELECT password_min_length, password_complexity FROM tenants WHERE id = app_tenant()`).Scan(&minLen, &complexity); err != nil {
		return "", err
	}
	if code := auth.CheckPassword(pw, email, minLen); code != "" {
		return code, nil
	}
	if complexity && !complexEnough(pw) {
		return "weak_password_complexity", nil
	}
	return "", nil
}

// updateMe changes the user's own language or password. A new password ends the user's other sessions.
func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Locale          *string `json:"locale"`
		CurrentPassword string  `json:"current_password"`
		NewPassword     *string `json:"new_password"`
		// appearance: an object sets it, null clears it (back to the organisation's default); absent = unchanged
		UIPrefs json.RawMessage `json:"ui_prefs"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Locale != nil && !supportedLocales[*in.Locale] {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	setUI := in.UIPrefs != nil
	ui, okUI := cleanUIPrefs(in.UIPrefs)
	if !okUI {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		var newHash *string
		if in.NewPassword != nil {
			var current string
			if err := tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = app_user()`).Scan(&current); err != nil {
				return err
			}
			if !auth.Verify(in.CurrentPassword, current) {
				return badRequest("wrong_password")
			}
			code, err := passwordProblem(ctx, tx, *in.NewPassword, p.Email)
			if err != nil {
				return err
			}
			if code != "" {
				return badRequest(code)
			}
			h, err := auth.Hash(*in.NewPassword)
			if err != nil {
				return err
			}
			newHash = &h
		}
		if _, err := tx.Exec(ctx, `
			UPDATE users SET locale = coalesce($1, locale), password_hash = coalesce($2, password_hash),
			       password_changed_at = CASE WHEN $2::text IS NULL THEN password_changed_at ELSE now() END
			WHERE id = app_user()`, in.Locale, newHash); err != nil {
			return err
		}
		if setUI {
			if _, err := tx.Exec(ctx, `UPDATE users SET ui_prefs = $1 WHERE id = app_user()`, ui); err != nil {
				return err
			}
		}
		if newHash != nil {
			_, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now(), revoked_by = app_user()
				WHERE user_id = app_user() AND revoked_at IS NULL AND id::text <> $1`, p.SessionID)
			return err
		}
		return nil
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	np, err := s.loadPrincipal(ctx, scope(r))
	if err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, np)
}
