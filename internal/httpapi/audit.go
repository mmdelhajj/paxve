package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
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
	// CompanyID: the company the change belongs to (null for organisation-level records)
	CompanyID  *string `json:"company_id"`
	ApprovedBy *string `json:"approved_by"` // approver name when the change came from an approved request
	ApprovalID *string `json:"approval_id"`
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	company, entity := q.Get("company_id"), q.Get("entity_id")
	beforeID, _ := strconv.ParseInt(q.Get("before"), 10, 64) // page: entries older than this id
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if (company != "" && !isUUID(company)) || (entity != "" && !isUUID(entity)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	limited := p.CompanyIDs != nil
	allowed := p.CompanyIDs
	if allowed == nil {
		allowed = []string{}
	}
	out := []auditEntry{}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id, a.at, a.action, a.entity, a.entity_id::text, u.full_name,
			       a.before::text, a.after::text, host(a.ip), a.company_id::text, ap.full_name, a.approval_id::text
			FROM audit_log a
			LEFT JOIN users u ON u.id = a.user_id
			LEFT JOIN users ap ON ap.id = a.approved_by
			-- someone limited to some companies sees those companies' changes and what they did themselves
			WHERE (NOT $1 OR a.company_id = ANY ($2::uuid[]) OR a.user_id = app_user())
			  AND ($3 = '' OR a.company_id = nullif($3, '')::uuid)
			  AND ($4 = '' OR a.entity_id = nullif($4, '')::uuid)
			  AND ($5 = 0 OR a.id < $5)
			ORDER BY a.id DESC
			LIMIT $6`, limited, allowed, company, entity, beforeID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e auditEntry
			var before, after *string
			if err := rows.Scan(&e.ID, &e.At, &e.Action, &e.Entity, &e.EntityID, &e.Actor, &before, &after, &e.IP, &e.CompanyID, &e.ApprovedBy, &e.ApprovalID); err != nil {
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
