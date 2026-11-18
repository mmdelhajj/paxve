// Package db holds database helpers: tenant-scoped transactions and migrations.
package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Scope identifies who is acting. Row-level security uses TenantID; the audit
// triggers record UserID and IP.
type Scope struct {
	TenantID string
	UserID   string
	APIKeyID string
	IP       string
	// CompanyID: the company of the request URL. When set, row-level security hides every other
	// company's rows (policy company_scope), whatever the query says.
	CompanyID string
	// CompanyIDs: the companies the user may open (nil = all). Enforced the same way.
	CompanyIDs []string
	// ApprovedBy / ApprovalID: set while an approved request is executed for its requester.
	ApprovedBy string
	ApprovalID string
}

// companyList renders CompanyIDs as a PostgreSQL array literal ("" = no limit).
func (s Scope) companyList() string {
	if s.CompanyIDs == nil {
		return ""
	}
	out := "{"
	for i, c := range s.CompanyIDs {
		if i > 0 {
			out += ","
		}
		out += c
	}
	return out + "}"
}

// InTenant runs fn in a transaction where every query only sees the rows of
// s.TenantID. The settings are transaction-local, so a pooled connection never
// carries one tenant's context into another request.
func InTenant(ctx context.Context, pool *pgxpool.Pool, s Scope, fn func(pgx.Tx) error) error {
	if s.TenantID == "" {
		return errors.New("db: missing tenant")
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`SELECT set_config('app.tenant_id', $1, true),
			        set_config('app.user_id', $2, true),
			        set_config('app.ip', $3, true),
			        set_config('app.api_key_id', $4, true),
			        set_config('app.company_id', $5, true),
			        set_config('app.company_ids', $6, true),
			        set_config('app.approved_by', $7, true),
			        set_config('app.approval_id', $8, true)`,
			s.TenantID, s.UserID, s.IP, s.APIKeyID, s.CompanyID, s.companyList(), s.ApprovedBy, s.ApprovalID)
		if err != nil {
			return err
		}
		return fn(tx)
	})
}
