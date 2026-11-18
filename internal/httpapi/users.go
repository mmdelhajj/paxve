package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/auth"
	"github.com/mmdelhajj/paxve/internal/db"
)

type user struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	FullName  string    `json:"full_name"`
	Role      string    `json:"role"`
	Locale    string    `json:"locale"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

const userCols = `id::text, email, full_name, role, locale, is_active, created_at`

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out []user
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY full_name`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[user])
		return err
	})
	if err != nil {
		serverErr(w, r, err)
		return
	}
	if out == nil {
		out = []user{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Email    string `json:"email"`
		FullName string `json:"full_name"`
		Role     string `json:"role"`
		Locale   string `json:"locale"`
		Password string `json:"password"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.FullName = strings.TrimSpace(in.FullName)
	if in.Locale == "" {
		in.Locale = "ar"
	}
	if !strings.Contains(in.Email, "@") || in.FullName == "" || !validRoles[in.Role] || !supportedLocales[in.Locale] {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if len(in.Password) < auth.MinLength {
		writeErr(w, http.StatusBadRequest, "weak_password")
		return
	}
	hash, err := auth.Hash(in.Password)
	if err != nil {
		serverErr(w, r, err)
		return
	}
	var u user
	err = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			INSERT INTO users (email, full_name, role, locale, password_hash)
			VALUES ($1, $2, $3, $4, $5) RETURNING `+userCols,
			in.Email, in.FullName, in.Role, in.Locale, hash)
		if err != nil {
			return err
		}
		u, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[user])
		return err
	})
	if isUniqueViolation(err) {
		writeErr(w, http.StatusConflict, "email_taken")
		return
	}
	if err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		FullName *string `json:"full_name"`
		Role     *string `json:"role"`
		IsActive *bool   `json:"is_active"`
		Password *string `json:"password"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Role != nil && !validRoles[*in.Role] {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if strings.EqualFold(id, principal(r).ID) && (in.Role != nil || in.IsActive != nil) {
		writeErr(w, http.StatusBadRequest, "cannot_change_self")
		return
	}
	var hash *string
	if in.Password != nil {
		if len(*in.Password) < auth.MinLength {
			writeErr(w, http.StatusBadRequest, "weak_password")
			return
		}
		h, err := auth.Hash(*in.Password)
		if err != nil {
			serverErr(w, r, err)
			return
		}
		hash = &h
	}
	var u user
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE users
			SET full_name = coalesce(nullif(trim($2), ''), full_name),
			    role = coalesce($3, role),
			    is_active = coalesce($4, is_active),
			    password_hash = coalesce($5, password_hash)
			WHERE id = $1
			RETURNING `+userCols, id, in.FullName, in.Role, in.IsActive, hash)
		if err != nil {
			return err
		}
		u, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[user])
		return err
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeErr(w, http.StatusNotFound, "not_found")
	case err != nil:
		serverErr(w, r, err)
	default:
		writeJSON(w, http.StatusOK, u)
	}
}
