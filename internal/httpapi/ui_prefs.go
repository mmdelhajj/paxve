package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Appearance of the app ("comfort for the eyes"). Only colours, text size and row spacing; the Magic layout never
// changes. Stored per user (users.ui_prefs); the organisation's default (tenants.ui_default) applies to users who
// have not chosen yet. "auto" follows the device's light / dark setting.
type uiPrefs struct {
	Theme   string `json:"theme"`   // comfort | classic | dark | contrast | auto
	Size    string `json:"size"`    // n | l | xl
	Density string `json:"density"` // compact | comfy
}

var (
	uiThemes    = map[string]bool{"comfort": true, "classic": true, "dark": true, "contrast": true, "auto": true}
	uiSizes     = map[string]bool{"n": true, "l": true, "xl": true}
	uiDensities = map[string]bool{"compact": true, "comfy": true}
)

// cleanUIPrefs validates a prefs object; empty fields are allowed (= not chosen). nil input → nil (clear).
func cleanUIPrefs(raw json.RawMessage) ([]byte, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var p uiPrefs
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, false
	}
	if (p.Theme != "" && !uiThemes[p.Theme]) || (p.Size != "" && !uiSizes[p.Size]) || (p.Density != "" && !uiDensities[p.Density]) {
		return nil, false
	}
	b, _ := json.Marshal(p)
	return b, true
}

// GET /org/ui-default: the organisation's default look for new users (any signed-in user may read it)
func (s *Server) getUIDefault(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var b []byte
		if err := tx.QueryRow(ctx, `SELECT ui_default FROM tenants WHERE id = app_tenant()`).Scan(&b); err != nil {
			return nil, err
		}
		if b == nil {
			return map[string]any{}, nil
		}
		return json.RawMessage(b), nil
	})
}

// PUT /org/ui-default {theme,size,density}: admins set the default look of users who have not chosen one
func (s *Server) putUIDefault(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if !decodeBody(w, r, &raw) {
		return
	}
	b, ok := cleanUIPrefs(raw)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE tenants SET ui_default = $1 WHERE id = app_tenant()`, b); err != nil {
			return nil, err
		}
		if b == nil {
			return map[string]any{}, nil
		}
		return json.RawMessage(b), nil
	})
}
