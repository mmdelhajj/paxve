package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/auth"
	"github.com/mmdelhajj/paxve/internal/db"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
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
	// second limit per account, so guessing one user's password from many IPs is also slowed down
	emailKey := "email:" + strings.ToLower(strings.TrimSpace(in.Email))
	if !s.limiter.allow(emailKey) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}

	var uid, tid, hash string
	var active bool
	err := s.Pool.QueryRow(ctx,
		`SELECT user_id::text, tenant_id::text, password_hash, is_active FROM auth_lookup($1)`,
		strings.TrimSpace(in.Email)).Scan(&uid, &tid, &hash, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		auth.VerifyDummy(in.Password)
		s.limiter.fail(ip)
		s.limiter.fail(emailKey)
		writeErr(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if err != nil {
		serverErr(w, r, err)
		return
	}
	if !auth.Verify(in.Password, hash) || !active {
		s.limiter.fail(ip)
		s.limiter.fail(emailKey)
		writeErr(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	s.limiter.reset(ip)
	s.limiter.reset(emailKey)

	if err := s.Sessions.RenewToken(ctx); err != nil {
		serverErr(w, r, err)
		return
	}
	s.Sessions.Put(ctx, "uid", uid)
	s.Sessions.Put(ctx, "tid", tid)

	sc := db.Scope{TenantID: tid, UserID: uid, IP: ip}
	err = db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_log (tenant_id, user_id, action, entity, entity_id, ip)
			VALUES (app_tenant(), app_user(), 'login', 'users', app_user(), nullif($1, '')::inet)`, ip)
		return err
	})
	if err != nil {
		serverErr(w, r, err)
		return
	}
	p, err := s.loadPrincipal(ctx, sc)
	if err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.Sessions.Destroy(r.Context()); err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, principal(r))
}

// updateMe changes the user's own language or password.
func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Locale          *string `json:"locale"`
		CurrentPassword string  `json:"current_password"`
		NewPassword     *string `json:"new_password"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Locale != nil && !supportedLocales[*in.Locale] {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var newHash *string
	if in.NewPassword != nil {
		if len(*in.NewPassword) < auth.MinLength {
			writeErr(w, http.StatusBadRequest, "weak_password")
			return
		}
		var current string
		err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = app_user()`).Scan(&current)
		})
		if err != nil {
			serverErr(w, r, err)
			return
		}
		if !auth.Verify(in.CurrentPassword, current) {
			writeErr(w, http.StatusBadRequest, "wrong_password")
			return
		}
		h, err := auth.Hash(*in.NewPassword)
		if err != nil {
			serverErr(w, r, err)
			return
		}
		newHash = &h
	}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE users SET locale = coalesce($1, locale), password_hash = coalesce($2, password_hash)
			WHERE id = app_user()`, in.Locale, newHash)
		return err
	})
	if err != nil {
		serverErr(w, r, err)
		return
	}
	p, err := s.loadPrincipal(ctx, scope(r))
	if err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
