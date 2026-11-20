package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/auth"
	"github.com/mmdelhajj/paxve/internal/db"
)

// ---- tracked sessions ----

var errSessionRevoked = errors.New("session revoked")

// sessionWindow: sessions older than this have expired anyway (scs lifetime is 12 h).
const sessionWindow = "12 hours"

// touchSession checks that the signed-in session has not been revoked and refreshes last_seen_at
// (at most every 5 minutes). Sessions created before tracking existed get a row on first use.
func (s *Server) touchSession(ctx context.Context, sc db.Scope, sid string, r *http.Request) (string, error) {
	idleOut := false
	err := db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		if sid == "" || !isUUID(sid) {
			return tx.QueryRow(ctx, `INSERT INTO user_sessions (user_id, ip, user_agent)
				VALUES (app_user(), nullif($1, '')::inet, left($2, 300)) RETURNING id::text`, sc.IP, r.UserAgent()).Scan(&sid)
		}
		var revoked bool
		var stale bool
		var idle bool
		// idle logout (phase 8): a session not seen for longer than the strictest idle time of the user's companies
		// ends (last_seen_at is refreshed every 5 minutes, hence the margin)
		err := tx.QueryRow(ctx, `SELECT revoked_at IS NOT NULL, last_seen_at < now() - interval '5 minutes',
			coalesce((SELECT min(c.idle_logout_min) FROM companies c JOIN users u ON u.id = app_user()
			          WHERE c.is_active AND c.idle_logout_min > 0 AND (u.company_ids IS NULL OR c.id = ANY (u.company_ids))), 0) > 0
			  AND last_seen_at < now() - make_interval(mins => 5 + (SELECT min(c.idle_logout_min) FROM companies c JOIN users u ON u.id = app_user()
			          WHERE c.is_active AND c.idle_logout_min > 0 AND (u.company_ids IS NULL OR c.id = ANY (u.company_ids))))
			FROM user_sessions WHERE id = $1 AND user_id = app_user()`, sid).Scan(&revoked, &stale, &idle)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && revoked) {
			return errSessionRevoked
		}
		if err == nil && idle {
			if _, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE id = $1`, sid); err != nil {
				return err
			}
			idleOut = true
			return nil
		}
		if err != nil {
			return err
		}
		if stale {
			_, err = tx.Exec(ctx, `UPDATE user_sessions SET last_seen_at = now(), ip = coalesce(nullif($2, '')::inet, ip) WHERE id = $1`, sid, sc.IP)
		}
		return err
	})
	if err == nil && idleOut {
		return sid, errSessionRevoked
	}
	return sid, err
}

const sessionCols = `id, created_at, last_seen_at, host(ip) AS ip, user_agent, mfa`

func (s *Server) mySessions(w http.ResponseWriter, r *http.Request) {
	sid := principal(r).SessionID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+sessionCols+`, (id::text = $1) AS current FROM user_sessions
			WHERE user_id = app_user() AND revoked_at IS NULL AND created_at > now() - interval '`+sessionWindow+`'
			ORDER BY last_seen_at DESC LIMIT 50`, sid)
	})
}

func (s *Server) revokeMySession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now(), revoked_by = app_user()
			WHERE id = $1 AND user_id = app_user() AND revoked_at IS NULL`, id)
		if err == nil && tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, err
	})
}

func (s *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	sid := principal(r).SessionID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now(), revoked_by = app_user()
			WHERE user_id = app_user() AND revoked_at IS NULL AND id::text <> $1`, sid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "revoked": tag.RowsAffected()}, nil
	})
}

func (s *Server) userSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+sessionCols+` FROM user_sessions
			WHERE user_id = $1 AND revoked_at IS NULL AND created_at > now() - interval '`+sessionWindow+`'
			ORDER BY last_seen_at DESC LIMIT 50`, id)
	})
}

func (s *Server) revokeUserSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := mayManageUser(ctx, tx, principal(r), id); err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now(), revoked_by = app_user()
			WHERE user_id = $1 AND revoked_at IS NULL`, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "revoked": tag.RowsAffected()}, nil
	})
}

// ---- two-factor authentication ----

func (s *Server) mfaSetup(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	secret := auth.NewTOTPSecret()
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		sealed, err := sealSecret(secret)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_mfa (user_id, pending_enc) VALUES (app_user(), $1)
			ON CONFLICT (user_id) DO UPDATE SET pending_enc = EXCLUDED.pending_enc`, sealed); err != nil {
			return nil, err
		}
		return map[string]string{"secret": secret, "otpauth_uri": auth.TOTPURI(secret, "Paxve", p.Email)}, nil
	})
}

// mfaEnable confirms the pending secret with a code and returns one-time recovery codes.
func (s *Server) mfaEnable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var pending string
		err := tx.QueryRow(ctx, `SELECT pending_enc FROM user_mfa WHERE user_id = app_user() FOR UPDATE`).Scan(&pending)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && pending == "") {
			return nil, badRequest("mfa_not_started")
		}
		if err != nil {
			return nil, err
		}
		step, ok := auth.VerifyTOTP(openSecret(pending), in.Code, time.Now(), 0)
		if !ok {
			return nil, badRequest("invalid_code")
		}
		codes, hashes := auth.NewRecoveryCodes(10)
		if _, err := tx.Exec(ctx, `UPDATE user_mfa SET secret_enc = pending_enc, pending_enc = '', enabled_at = now(),
			last_step = $1, recovery_hashes = $2 WHERE user_id = app_user()`, step, hashes); err != nil {
			return nil, err
		}
		if p.SessionID != "" {
			if _, err := tx.Exec(ctx, `UPDATE user_sessions SET mfa = true WHERE id = $1`, p.SessionID); err != nil {
				return nil, err
			}
		}
		if err := auditEvent(ctx, tx, "mfa_enabled", "users", p.ID); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "recovery_codes": codes}, nil
	})
}

// mfaDisable needs the password and a current code (or recovery code). Refused when the organisation requires it.
func (s *Server) mfaDisable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password     string `json:"password"`
		Code         string `json:"code"`
		RecoveryCode string `json:"recovery_code"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	if p.MFARequired {
		writeErr(w, http.StatusConflict, "mfa_required_by_org")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := checkOwnPassword(ctx, tx, in.Password); err != nil {
			return nil, err
		}
		ok, err := checkSecondFactor(ctx, tx, in.Code, in.RecoveryCode)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, badRequest("invalid_code")
		}
		if _, err := tx.Exec(ctx, `UPDATE user_mfa SET secret_enc = '', pending_enc = '', enabled_at = NULL,
			recovery_hashes = '{}' WHERE user_id = app_user()`); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, auditEvent(ctx, tx, "mfa_disabled", "users", p.ID)
	})
}

func (s *Server) mfaRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		ok, err := checkSecondFactor(ctx, tx, in.Code, "")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, badRequest("invalid_code")
		}
		codes, hashes := auth.NewRecoveryCodes(10)
		_, err = tx.Exec(ctx, `UPDATE user_mfa SET recovery_hashes = $1 WHERE user_id = app_user()`, hashes)
		return map[string]any{"recovery_codes": codes}, err
	})
}

func (s *Server) resetUserMFA(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if strings.EqualFold(id, principal(r).ID) {
			return nil, badRequest("cannot_change_self")
		}
		if err := mayManageUser(ctx, tx, principal(r), id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE user_mfa SET secret_enc = '', pending_enc = '', enabled_at = NULL,
			recovery_hashes = '{}' WHERE user_id = $1`, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now(), revoked_by = app_user()
			WHERE user_id = $1 AND revoked_at IS NULL`, id); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, auditEvent(ctx, tx, "mfa_reset", "users", id)
	})
}

func checkOwnPassword(ctx context.Context, tx pgx.Tx, pw string) error {
	var hash string
	if err := tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = app_user()`).Scan(&hash); err != nil {
		return err
	}
	if !auth.Verify(pw, hash) {
		return badRequest("wrong_password")
	}
	return nil
}

func auditEvent(ctx context.Context, tx pgx.Tx, action, entity, id string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (tenant_id, user_id, api_key_id, action, entity, entity_id, ip)
		VALUES (app_tenant(), app_user(), nullif(current_setting('app.api_key_id', true), '')::uuid, $1, $2, $3::uuid,
		        nullif(current_setting('app.ip', true), '')::inet)`, action, entity, id)
	return err
}

// ---- password reset ----

func newResetToken() (string, []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	return tok, hashKey(tok)
}

func resetLink(tok string) string { return publicURL() + "/#/reset/" + tok }

// createResetLink: an admin makes a one-time link (24 h) for a user who forgot the password,
// to give them in person or by WhatsApp. It also works without any e-mail set up.
func (s *Server) createResetLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := mayManageUser(ctx, tx, principal(r), id); err != nil {
			return nil, err
		}
		tok, hash := newResetToken()
		var exp time.Time
		if err := tx.QueryRow(ctx, `INSERT INTO password_resets (user_id, token_hash, expires_at)
			VALUES ($1, $2, now() + interval '24 hours') RETURNING expires_at`, id, hash).Scan(&exp); err != nil {
			return nil, err
		}
		return map[string]any{"url": resetLink(tok), "expires_at": exp}, nil
	})
}

// forgotPassword e-mails a reset link (1 h) when the organisation has an e-mail (SMTP) provider.
// The answer is always the same, so it does not reveal whether an address exists.
func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	ip := clientIP(r)
	if !s.resetLimiter.allow(ip) || !s.resetLimiter.allow("email:"+email) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	s.resetLimiter.fail(ip)
	s.resetLimiter.fail("email:" + email)
	if emailRe.MatchString(email) {
		go s.sendResetMail(email, ip)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

var resetMailText = map[string][2]string{
	"en": {"Paxve password reset", "Hello %s,\n\nTo choose a new Paxve password, open this link within one hour:\n%s\n\nIf you did not ask for this, ignore this message; your password stays the same."},
	"ar": {"إعادة تعيين كلمة المرور في Paxve", "مرحباً %s،\n\nلاختيار كلمة مرور جديدة لحسابك في Paxve افتح هذا الرابط خلال ساعة:\n%s\n\nإذا لم تطلب ذلك فتجاهل هذه الرسالة، وتبقى كلمة المرور كما هي."},
	"fr": {"Réinitialisation du mot de passe Paxve", "Bonjour %s,\n\nPour choisir un nouveau mot de passe Paxve, ouvrez ce lien dans l'heure :\n%s\n\nSi vous n'êtes pas à l'origine de cette demande, ignorez ce message ; votre mot de passe ne change pas."},
}

func (s *Server) sendResetMail(email, ip string) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var uid, tid string
	var active bool
	err := s.Pool.QueryRow(ctx, `SELECT user_id::text, tenant_id::text, is_active FROM auth_lookup($1)`, email).Scan(&uid, &tid, &active)
	if err != nil || !active {
		return
	}
	sc := db.Scope{TenantID: tid, UserID: uid, IP: ip}
	var cfgRaw []byte
	var name, locale, tok string
	err = db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT config FROM message_providers WHERE kind = 'smtp' AND is_active
			ORDER BY is_default DESC, created_at LIMIT 1`).Scan(&cfgRaw)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT full_name, locale FROM users WHERE id = app_user()`).Scan(&name, &locale); err != nil {
			return err
		}
		var hash []byte
		tok, hash = newResetToken()
		_, err = tx.Exec(ctx, `INSERT INTO password_resets (user_id, token_hash, created_by, expires_at)
			VALUES (app_user(), $1, NULL, now() + interval '1 hour')`, hash)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Info("password reset requested but no e-mail provider", "tenant", tid)
		return
	}
	if err != nil {
		slog.Error("password reset", "err", err)
		return
	}
	cfg := map[string]any{}
	_ = json.Unmarshal(cfgRaw, &cfg)
	txt, ok := resetMailText[locale]
	if !ok {
		txt = resetMailText["en"]
	}
	body := strings.Replace(strings.Replace(txt[1], "%s", name, 1), "%s", resetLink(tok), 1)
	if _, err := sendSMTP(ctx, openConfig(cfg), mailMsg{To: email, Subject: txt[0], Body: body}, s.AllowPrivateRouters); err != nil {
		slog.Warn("password reset e-mail failed", "tenant", tid, "err", err)
	}
}

func (s *Server) lookupReset(ctx context.Context, tok string) (id, tid, uid, email string, valid bool, err error) {
	if len(tok) < 20 || len(tok) > 100 {
		return "", "", "", "", false, pgx.ErrNoRows
	}
	err = s.Pool.QueryRow(ctx, `SELECT id::text, tenant_id::text, user_id::text, email, valid FROM password_reset_lookup($1)`,
		hashKey(tok)).Scan(&id, &tid, &uid, &email, &valid)
	return
}

func maskEmail(e string) string {
	name, dom, ok := strings.Cut(e, "@")
	if !ok || len(name) == 0 {
		return ""
	}
	return name[:1] + strings.Repeat("•", max(len(name)-1, 2)) + "@" + dom
}

func (s *Server) checkResetToken(w http.ResponseWriter, r *http.Request) {
	if !s.portalLimiter.allow("reset:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	_, _, _, email, valid, err := s.lookupReset(r.Context(), r.PathValue("token"))
	if err != nil || !valid {
		s.portalLimiter.fail("reset:" + clientIP(r))
		writeErr(w, http.StatusNotFound, "reset_invalid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "email": maskEmail(email)})
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !s.portalLimiter.allow("reset:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	rid, tid, uid, email, valid, err := s.lookupReset(ctx, in.Token)
	if err != nil || !valid {
		s.portalLimiter.fail("reset:" + clientIP(r))
		writeErr(w, http.StatusNotFound, "reset_invalid")
		return
	}
	err = db.InTenant(ctx, s.Pool, db.Scope{TenantID: tid, UserID: uid, IP: clientIP(r)}, func(tx pgx.Tx) error {
		code, err := passwordProblem(ctx, tx, in.Password, email)
		if err != nil {
			return err
		}
		if code != "" {
			return badRequest(code)
		}
		h, err := auth.Hash(in.Password)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE password_resets SET used_at = now() WHERE id = $1 AND used_at IS NULL`, rid)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &apiError{status: http.StatusNotFound, code: "reset_invalid"}
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $1, password_changed_at = now() WHERE id = app_user()`, h); err != nil {
			return err
		}
		// other links of this user stop working, and every session signs out
		if _, err := tx.Exec(ctx, `UPDATE password_resets SET used_at = now() WHERE user_id = app_user() AND used_at IS NULL`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE user_id = app_user() AND revoked_at IS NULL`); err != nil {
			return err
		}
		return auditEvent(ctx, tx, "password_reset", "users", uid)
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- organisation security settings ----

func (s *Server) getSecuritySettings(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT require_mfa, password_min_length, password_complexity, password_expiry_days, sso_allowed,
			(SELECT count(*) FROM users u WHERE u.is_active) AS users,
			(SELECT count(*) FROM users u JOIN user_mfa m ON m.user_id = u.id WHERE u.is_active AND m.enabled_at IS NOT NULL) AS users_with_mfa
			FROM tenants WHERE id = app_tenant()`)
	})
}

func (s *Server) putSecuritySettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequireMFA         *bool `json:"require_mfa"`
		PasswordMinLength  *int  `json:"password_min_length"`
		PasswordComplexity *bool `json:"password_complexity"`
		PasswordExpiryDays *int  `json:"password_expiry_days"`
		SSOAllowed         *bool `json:"sso_allowed"`
	}
	if err := decode(w, r, &in); err != nil || (in.PasswordMinLength != nil && (*in.PasswordMinLength < 10 || *in.PasswordMinLength > 64)) ||
		(in.PasswordExpiryDays != nil && (*in.PasswordExpiryDays < 0 || *in.PasswordExpiryDays > 730)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	if in.RequireMFA != nil && *in.RequireMFA && !p.MFAEnabled {
		// whoever turns it on must already use it, so they cannot lock themselves out half way
		writeErr(w, http.StatusConflict, "mfa_enable_first")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE tenants SET require_mfa = coalesce($1, require_mfa),
			password_min_length = coalesce($2, password_min_length), password_complexity = coalesce($3, password_complexity),
			password_expiry_days = coalesce($4, password_expiry_days), sso_allowed = coalesce($5, sso_allowed) WHERE id = app_tenant()`,
			in.RequireMFA, in.PasswordMinLength, in.PasswordComplexity, in.PasswordExpiryDays, in.SSOAllowed); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT require_mfa, password_min_length, password_complexity, password_expiry_days, sso_allowed FROM tenants WHERE id = app_tenant()`)
	})
}

// permissionCatalogue: modules and actions for the role editor, plus the presets.
func (s *Server) permissionCatalogue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"modules": moduleActions, "presets": presets})
}
