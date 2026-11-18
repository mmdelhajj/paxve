package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5"

	pxcrypto "github.com/mmdelhajj/paxve/internal/crypto"
)

// secretBox encrypts secrets at rest (provider tokens, router passwords). It is nil when
// APP_SECRET_KEY is not configured; values are then stored as plain text (development only).
var secretBox *pxcrypto.Box

// SetSecretBox enables encryption of stored secrets.
func (s *Server) SetSecretBox(b *pxcrypto.Box) { secretBox = b }

func sealSecret(v string) (string, error) {
	if secretBox == nil {
		return v, nil
	}
	return secretBox.Seal(v)
}

// openSecret returns the plain value ("" if it cannot be decrypted).
func openSecret(v string) string {
	if !pxcrypto.IsSealed(v) {
		return v
	}
	if secretBox == nil {
		return ""
	}
	p, err := secretBox.Open(v)
	if err != nil {
		return ""
	}
	return p
}

// sealConfig encrypts the secret fields of a provider config.
func sealConfig(cfg map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range cfg {
		if s, ok := v.(string); ok && secretKeys[k] && s != "" {
			sealed, err := sealSecret(s)
			if err != nil {
				return nil, err
			}
			out[k] = sealed
			continue
		}
		out[k] = v
	}
	return out, nil
}

// openConfig decrypts the secret fields of a provider config.
func openConfig(cfg map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range cfg {
		if s, ok := v.(string); ok && secretKeys[k] {
			out[k] = openSecret(s)
			continue
		}
		out[k] = v
	}
	return out
}

// EncryptLegacySecrets seals secrets that were stored before encryption was enabled.
// Idempotent: values already sealed are left alone.
func (s *Server) EncryptLegacySecrets(ctx context.Context) error {
	if secretBox == nil || s.OwnerPool == nil {
		return nil
	}
	n := 0
	err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, tenant_id::text, config FROM message_providers FOR UPDATE`)
		if err != nil {
			return err
		}
		type row struct {
			id, tenant string
			cfg        []byte
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			err := r.Scan(&x.id, &x.tenant, &x.cfg)
			return x, err
		})
		if err != nil {
			return err
		}
		for _, x := range list {
			cfg := map[string]any{}
			_ = json.Unmarshal(x.cfg, &cfg)
			changed := false
			for k, v := range cfg {
				if str, ok := v.(string); ok && secretKeys[k] && str != "" && !pxcrypto.IsSealed(str) {
					changed = true
				}
			}
			if !changed {
				continue
			}
			sealed, err := sealConfig(cfg)
			if err != nil {
				return err
			}
			b, _ := json.Marshal(sealed)
			if _, err := tx.Exec(ctx, `UPDATE message_providers SET config = $2 WHERE id = $1`, x.id, b); err != nil {
				return err
			}
			n++
		}
		rrows, err := tx.Query(ctx, `SELECT id::text, password_enc FROM routers WHERE password_enc <> '' AND password_enc NOT LIKE 'enc:%' FOR UPDATE`)
		if err != nil {
			return err
		}
		type rr struct{ id, pw string }
		rlist, err := pgx.CollectRows(rrows, func(r pgx.CollectableRow) (rr, error) {
			var x rr
			err := r.Scan(&x.id, &x.pw)
			return x, err
		})
		if err != nil {
			return err
		}
		for _, x := range rlist {
			sealed, err := sealSecret(x.pw)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE routers SET password_enc = $2 WHERE id = $1`, x.id, sealed); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err == nil && n > 0 {
		slog.Info("encrypted legacy secrets", "rows", n)
	}
	return err
}
