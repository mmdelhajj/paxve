package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/auth"
)

// employeeRoleName is the ready-made role for staff who only use self-service (clock in/out, leave,
// own payslips). It has no module permissions: a user without permissions lands on the self page.
const employeeRoleName = "Employee (self-service)"

// ensureEmployeeRole returns the id of the tenant's self-service role, creating it on first use.
func ensureEmployeeRole(ctx context.Context, tx pgx.Tx) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE name = $1`, employeeRoleName).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	err = tx.QueryRow(ctx, `INSERT INTO roles (name, base, permissions, description)
		VALUES ($1, $2, '{}', 'Self-service only: clock in/out, leave requests, own payslips and advances.') RETURNING id::text`,
		employeeRoleName, RoleReadOnly).Scan(&id)
	return id, err
}

// POST /companies/{cid}/employees/{id}/login {email, locale}: creates a self-service sign-in for an
// employee (ready-made "Employee" role, this company only) and links it to the employee file. The
// generated password is returned once so the manager can hand it over; the employee changes it later.
func (s *Server) createEmployeeLogin(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		Email  string `json:"email"`
		Locale string `json:"locale"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if in.Locale == "" || !supportedLocales[in.Locale] {
		in.Locale = "ar"
	}
	if !strings.Contains(in.Email, "@") || s.platformAdmins[in.Email] {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !can(r, "payroll.edit") {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var name string
		var linked *string
		err := tx.QueryRow(ctx, `SELECT name, user_id::text FROM employees WHERE id = $1 AND company_id = $2`, id, cid).Scan(&name, &linked)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if linked != nil {
			return nil, &apiError{status: http.StatusConflict, code: "already_linked"}
		}
		roleID, err := ensureEmployeeRole(ctx, tx)
		if err != nil {
			return nil, err
		}
		pw := auth.RandomPassword()
		hash, err := auth.Hash(pw)
		if err != nil {
			return nil, err
		}
		var uid string
		err = tx.QueryRow(ctx, `INSERT INTO users (email, full_name, role, role_id, company_ids, locale, password_hash, password_changed_at)
			VALUES ($1, $2, $3, $4, ARRAY[$5]::uuid[], $6, $7, now()) RETURNING id::text`,
			in.Email, name, RoleReadOnly, roleID, cid, in.Locale, hash).Scan(&uid)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "email_taken"}
		}
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE employees SET user_id = $2 WHERE id = $1`, id, uid); err != nil {
			return nil, err
		}
		return map[string]string{"user_id": uid, "email": in.Email, "password": pw, "role": employeeRoleName}, nil
	})
}
