package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/auth"
	"github.com/mmdelhajj/paxve/internal/db"
)

type user struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	FullName   string     `json:"full_name"`
	Role       string     `json:"role"`
	Locale     string     `json:"locale"`
	IsActive   bool       `json:"is_active"`
	CreatedAt  time.Time  `json:"created_at"`
	RoleID     *string    `json:"role_id"`
	RoleName   *string    `json:"role_name"`
	CompanyIDs []string   `json:"company_ids"`
	MFAEnabled bool       `json:"mfa_enabled"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

const userCols = `u.id::text, u.email, u.full_name, u.role, u.locale, u.is_active, u.created_at,
	u.role_id::text, ro.name, u.company_ids::text[], coalesce(m.enabled_at IS NOT NULL, false),
	(SELECT max(last_seen_at) FROM user_sessions us WHERE us.user_id = u.id)`

const userFrom = ` FROM users u LEFT JOIN roles ro ON ro.id = u.role_id LEFT JOIN user_mfa m ON m.user_id = u.id`

func getUser(ctx context.Context, tx pgx.Tx, id string) (user, error) {
	rows, err := tx.Query(ctx, `SELECT `+userCols+userFrom+` WHERE u.id = $1`, id)
	if err != nil {
		return user{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[user])
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out []user
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+userCols+userFrom+` ORDER BY u.full_name`)
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

// grantedPerms: what a user would be allowed to do with this base role / custom role.
func grantedPerms(ctx context.Context, tx pgx.Tx, base string, roleID *string) (map[string]bool, string, error) {
	if roleID == nil || *roleID == "" {
		if _, ok := presets[base]; !ok {
			return nil, "", badRequest("bad_request")
		}
		return permSet(presets[base]), base, nil
	}
	if !isUUID(*roleID) {
		return nil, "", badRequest("bad_request")
	}
	var perms []string
	var rbase string
	var active bool
	err := tx.QueryRow(ctx, `SELECT permissions, base, is_active FROM roles WHERE id = $1`, *roleID).Scan(&perms, &rbase, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", badRequest("role_not_found")
	}
	if err != nil {
		return nil, "", err
	}
	if !active {
		return map[string]bool{}, rbase, nil
	}
	return permSet(perms), rbase, nil
}

// noEscalation: nobody can hand out permissions or companies they do not have themselves.
func noEscalation(actor *Principal, perms map[string]bool, companies []string) error {
	for p := range perms {
		if !actor.perms[p] {
			return &apiError{status: http.StatusForbidden, code: "cannot_grant", detail: p}
		}
	}
	if actor.CompanyIDs != nil {
		if companies == nil {
			return &apiError{status: http.StatusForbidden, code: "cannot_grant", detail: "companies"}
		}
		for _, c := range companies {
			if !companyAllowed(actor, c) {
				return &apiError{status: http.StatusForbidden, code: "cannot_grant", detail: "companies"}
			}
		}
	}
	return nil
}

// mayManageUser: the actor holds every permission of the target user (so editing, resetting the
// password or signing out someone more powerful is refused).
func mayManageUser(ctx context.Context, tx pgx.Tx, actor *Principal, id string) error {
	var base string
	var roleID *string
	var companies []string
	err := tx.QueryRow(ctx, `SELECT role, role_id::text, company_ids::text[] FROM users WHERE id = $1`, id).Scan(&base, &roleID, &companies)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return err
	}
	perms, _, err := grantedPerms(ctx, tx, base, roleID)
	if err != nil {
		return err
	}
	return noEscalation(actor, perms, companies)
}

// checkCompanies validates a company list (nil = all companies).
func checkCompanies(ctx context.Context, tx pgx.Tx, ids []string) error {
	if ids == nil {
		return nil
	}
	if len(ids) == 0 || len(ids) > 500 {
		return badRequest("bad_request")
	}
	for _, id := range ids {
		if !isUUID(id) {
			return badRequest("bad_request")
		}
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM companies WHERE id = ANY ($1::uuid[])`, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(slices.Compact(slices.Sorted(slices.Values(ids)))) {
		return badRequest("bad_request")
	}
	return nil
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email      string   `json:"email"`
		FullName   string   `json:"full_name"`
		Role       string   `json:"role"`
		RoleID     *string  `json:"role_id"`
		CompanyIDs []string `json:"company_ids"`
		Locale     string   `json:"locale"`
		Password   string   `json:"password"`
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
	if in.RoleID != nil && *in.RoleID == "" {
		in.RoleID = nil
	}
	if !strings.Contains(in.Email, "@") || in.FullName == "" || (in.RoleID == nil && !validRoles[in.Role]) || !supportedLocales[in.Locale] {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if s.platformAdmins[in.Email] { // platform-owner addresses cannot be created inside an organisation
		writeErr(w, http.StatusConflict, "email_taken")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		perms, base, err := grantedPerms(ctx, tx, in.Role, in.RoleID)
		if err != nil {
			return nil, err
		}
		if err := checkCompanies(ctx, tx, in.CompanyIDs); err != nil {
			return nil, err
		}
		if err := noEscalation(principal(r), perms, in.CompanyIDs); err != nil {
			return nil, err
		}
		code, err := passwordProblem(ctx, tx, in.Password, in.Email)
		if err != nil {
			return nil, err
		}
		if code != "" {
			return nil, badRequest(code)
		}
		hash, err := auth.Hash(in.Password)
		if err != nil {
			return nil, err
		}
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO users (email, full_name, role, role_id, company_ids, locale, password_hash, password_changed_at)
			VALUES ($1, $2, $3, $4, $5::uuid[], $6, $7, now()) RETURNING id::text`,
			in.Email, in.FullName, base, in.RoleID, in.CompanyIDs, in.Locale, hash).Scan(&id)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "email_taken"}
		}
		if err != nil {
			return nil, err
		}
		return getUser(ctx, tx, id)
	})
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		FullName     *string   `json:"full_name"`
		Role         *string   `json:"role"`
		RoleID       *string   `json:"role_id"` // "" = back to the built-in role
		CompanyIDs   *[]string `json:"company_ids"`
		AllCompanies bool      `json:"all_companies"` // true = remove the company limit
		IsActive     *bool     `json:"is_active"`
		Password     *string   `json:"password"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Role != nil && !validRoles[*in.Role] {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	self := strings.EqualFold(id, principal(r).ID)
	if self && (in.Role != nil || in.RoleID != nil || in.IsActive != nil || in.CompanyIDs != nil || in.AllCompanies) {
		writeErr(w, http.StatusBadRequest, "cannot_change_self")
		return
	}
	actor := principal(r)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cur, err := getUser(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !self {
			if err := mayManageUser(ctx, tx, actor, id); err != nil {
				return nil, err
			}
		}
		// the role and companies the user will have after this change
		base, roleID, companies := cur.Role, cur.RoleID, cur.CompanyIDs
		if in.Role != nil {
			base = *in.Role
			if in.RoleID == nil {
				roleID = nil
			}
		}
		if in.RoleID != nil {
			if *in.RoleID == "" {
				roleID = nil
			} else {
				roleID = in.RoleID
			}
		}
		if in.AllCompanies {
			companies = nil
		} else if in.CompanyIDs != nil {
			companies = *in.CompanyIDs
			if err := checkCompanies(ctx, tx, companies); err != nil {
				return nil, err
			}
		}
		perms, rbase, err := grantedPerms(ctx, tx, base, roleID)
		if err != nil {
			return nil, err
		}
		if roleID != nil {
			base = rbase
		}
		if !self {
			if err := noEscalation(actor, perms, companies); err != nil {
				return nil, err
			}
		}
		var hash *string
		if in.Password != nil {
			code, err := passwordProblem(ctx, tx, *in.Password, cur.Email)
			if err != nil {
				return nil, err
			}
			if code != "" {
				return nil, badRequest(code)
			}
			h, err := auth.Hash(*in.Password)
			if err != nil {
				return nil, err
			}
			hash = &h
		}
		if _, err := tx.Exec(ctx, `
			UPDATE users
			SET full_name = coalesce(nullif(trim($2), ''), full_name),
			    role = $3, role_id = $4::uuid, company_ids = $5::uuid[],
			    is_active = coalesce($6, is_active),
			    password_hash = coalesce($7, password_hash),
			    password_changed_at = CASE WHEN $7::text IS NULL THEN password_changed_at ELSE now() END
			WHERE id = $1`, id, in.FullName, base, roleID, companies, in.IsActive, hash); err != nil {
			return nil, err
		}
		// a new password set by an admin, or a deactivated account, signs the user out everywhere
		if hash != nil || (in.IsActive != nil && !*in.IsActive) {
			if _, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at = now(), revoked_by = app_user()
				WHERE user_id = $1 AND revoked_at IS NULL`, id); err != nil {
				return nil, err
			}
		}
		return getUser(ctx, tx, id)
	})
}

// ---- custom roles ----

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT ro.id, ro.name, ro.base, ro.permissions, ro.description, ro.is_active, ro.created_at,
			(SELECT count(*) FROM users u WHERE u.role_id = ro.id) AS users
			FROM roles ro ORDER BY ro.name`)
	})
}

func (s *Server) saveRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	create := id == ""
	if !create && !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		Name        *string   `json:"name"`
		Base        *string   `json:"base"`
		Permissions *[]string `json:"permissions"`
		Description *string   `json:"description"`
		IsActive    *bool     `json:"is_active"`
	}
	if err := decode(w, r, &in); err != nil || (create && (in.Name == nil || in.Base == nil || in.Permissions == nil)) ||
		(in.Base != nil && !validRoles[*in.Base]) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var perms []string
	if in.Permissions != nil {
		for _, p := range *in.Permissions {
			if !validPerm(p) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": p})
				return
			}
		}
		perms = slices.Compact(slices.Sorted(slices.Values(*in.Permissions)))
		if perms == nil {
			perms = []string{}
		}
	}
	actor := principal(r)
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[create], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !create {
			// editing a role changes everyone who has it: the actor must hold its current and new permissions
			var current []string
			if err := tx.QueryRow(ctx, `SELECT permissions FROM roles WHERE id = $1`, id).Scan(&current); err != nil {
				return nil, err
			}
			if err := noEscalation(actor, permSet(current), actor.CompanyIDs); err != nil {
				return nil, err
			}
		}
		if perms != nil {
			if err := noEscalation(actor, permSet(perms), actor.CompanyIDs); err != nil {
				return nil, err
			}
		}
		var out string
		var err error
		if create {
			err = tx.QueryRow(ctx, `INSERT INTO roles (name, base, permissions, description) VALUES (trim($1), $2, $3, coalesce($4, ''))
				RETURNING id::text`, *in.Name, *in.Base, perms, in.Description).Scan(&out)
		} else {
			err = tx.QueryRow(ctx, `UPDATE roles SET name = coalesce(trim($2), name), base = coalesce($3, base),
				permissions = coalesce($4, permissions), description = coalesce($5, description), is_active = coalesce($6, is_active)
				WHERE id = $1 RETURNING id::text`, id, in.Name, in.Base, perms, in.Description, in.IsActive).Scan(&out)
			if err == nil && in.Base != nil {
				// users keep the role's base so screens and scoping follow it
				_, err = tx.Exec(ctx, `UPDATE users SET role = $2 WHERE role_id = $1`, id, *in.Base)
			}
		}
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "name_taken"}
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, name, base, permissions, description, is_active, created_at FROM roles WHERE id = $1`, out)
	})
}
