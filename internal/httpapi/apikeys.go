package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/db"
)

// API keys look like pxv_<prefix>_<secret>. Only SHA-256(key) is stored.

var keyEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(keyEnc.EncodeToString(b))
}

func hashKey(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return h[:]
}

func (s *Server) authAPIKey(ctx context.Context, key, ip string) (*Principal, bool) {
	parts := strings.Split(key, "_")
	if len(parts) != 3 || parts[0] != "pxv" || len(parts[1]) < 8 {
		return nil, false
	}
	var id, tid, name, role, createdBy string
	var hash []byte
	var active bool
	err := s.Pool.QueryRow(ctx,
		`SELECT id::text, tenant_id::text, name, key_hash, role, created_by::text, active FROM api_key_lookup($1)`,
		parts[1]).Scan(&id, &tid, &name, &hash, &role, &createdBy, &active)
	if err != nil || !active || subtle.ConstantTimeCompare(hash, hashKey(key)) != 1 {
		return nil, false
	}
	p, err := s.loadPrincipal(ctx, db.Scope{TenantID: tid, UserID: createdBy, IP: ip})
	if err != nil || !p.Active {
		return nil, false
	}
	p.Role = role // the key's role, never more than set when it was created
	p.APIKeyID = id
	p.FullName = "API · " + name
	p.IsPlatformAdmin = false
	_ = db.InTenant(ctx, s.Pool, db.Scope{TenantID: tid, UserID: createdBy, APIKeyID: id, IP: ip}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE api_keys SET last_used_at = now()
			WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, id)
		return err
	})
	return p, true
}

type apiKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Role       string     `json:"role"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

const apiKeyCols = `k.id::text, k.name, k.prefix, k.role, u.full_name, k.created_at, k.last_used_at, k.revoked_at`

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []apiKey{}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+apiKeyCols+` FROM api_keys k JOIN users u ON u.id = k.created_by
			ORDER BY k.revoked_at IS NOT NULL, k.created_at DESC`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[apiKey])
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	// collector scoping follows the signed-in person's areas, which has no meaning for a key
	if err := decode(w, r, &in); err != nil || strings.TrimSpace(in.Name) == "" || !validRoles[in.Role] || in.Role == RoleCollector || in.Role == RoleCashier {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	prefix := randToken(5) // 8 chars
	key := "pxv_" + prefix + "_" + randToken(20)
	var k apiKey
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO api_keys (name, prefix, key_hash, role, created_by)
			VALUES ($1, $2, $3, $4, app_user()) RETURNING id::text`,
			strings.TrimSpace(in.Name), prefix, hashKey(key), in.Role).Scan(&id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+apiKeyCols+` FROM api_keys k JOIN users u ON u.id = k.created_by WHERE k.id = $1`, id)
		if err != nil {
			return err
		}
		k, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[apiKey])
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "api_key": k})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
		if err == nil && tag.RowsAffected() == 0 {
			return notFound()
		}
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
