package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/db"
)

type auditEntry struct {
	ID       int64           `json:"id"`
	At       time.Time       `json:"at"`
	Action   string          `json:"action"`
	Entity   string          `json:"entity"`
	EntityID *string         `json:"entity_id"`
	Actor    *string         `json:"actor"`
	Before   json.RawMessage `json:"before"`
	After    json.RawMessage `json:"after"`
	IP       *string         `json:"ip"`
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []auditEntry{}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id, a.at, a.action, a.entity, a.entity_id::text, u.full_name,
			       a.before::text, a.after::text, host(a.ip)
			FROM audit_log a
			LEFT JOIN users u ON u.id = a.user_id
			ORDER BY a.id DESC
			LIMIT 200`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e auditEntry
			var before, after *string
			if err := rows.Scan(&e.ID, &e.At, &e.Action, &e.Entity, &e.EntityID, &e.Actor, &before, &after, &e.IP); err != nil {
				return err
			}
			if before != nil {
				e.Before = json.RawMessage(*before)
			}
			if after != nil {
				e.After = json.RawMessage(*after)
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
