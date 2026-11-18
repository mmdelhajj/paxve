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
			        set_config('app.api_key_id', $4, true)`,
			s.TenantID, s.UserID, s.IP, s.APIKeyID)
		if err != nil {
			return err
		}
		return fn(tx)
	})
}
