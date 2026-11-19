package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Interface translations a tenant corrects for itself (Settings → Translations). The shipped
// translations live in the web app; these rows override single keys for every user of the tenant.

var (
	langRe = regexp.MustCompile(`^[a-z]{2,3}$`)
	tkeyRe = regexp.MustCompile(`^[A-Za-z0-9_.]{1,200}$`)
)

// GET /translations?lang=xx → {key: value}
func (s *Server) listTranslations(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	if !langRe.MatchString(lang) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT key, value FROM ui_translations WHERE lang = $1 ORDER BY key`, lang)
		if err != nil {
			return nil, err
		}
		out := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, rows.Err()
	})
}

// PUT /translations {lang, key, value}; an empty value removes the correction.
func (s *Server) saveTranslation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lang  string `json:"lang"`
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !langRe.MatchString(in.Lang) || !tkeyRe.MatchString(in.Key) || len(in.Value) > 4000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if strings.TrimSpace(in.Value) == "" {
			if _, err := tx.Exec(ctx, `DELETE FROM ui_translations WHERE lang = $1 AND key = $2`, in.Lang, in.Key); err != nil {
				return nil, err
			}
			return map[string]any{"lang": in.Lang, "key": in.Key, "value": nil}, nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ui_translations (lang, key, value) VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, lang, key) DO UPDATE SET value = EXCLUDED.value, updated_by = app_user()`,
			in.Lang, in.Key, in.Value); err != nil {
			return nil, err
		}
		return map[string]any{"lang": in.Lang, "key": in.Key, "value": in.Value}, nil
	})
}
