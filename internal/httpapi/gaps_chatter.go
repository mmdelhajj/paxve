package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Chatter (Odoo gap): one panel on any record with comments (@mentions notify the people named), the
// automatic change history (read from the audit trail), its activities and its files. Notifications are
// in-app (bell), and also e-mailed when the company has an SMTP provider.

// chatterAllowed: the record type is known and the user may see (write = comment on) it.
func chatterAllowed(r *http.Request, typ string, write bool) bool {
	if _, ok := attachTables[typ]; !ok {
		return false
	}
	// anyone who may see the record may discuss it (files keep the stricter create / edit rule)
	_ = write
	return attachAllowed(r, typ, false)
}

type chatterChange struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

type chatterEvent struct {
	At      time.Time       `json:"at"`
	Actor   *string         `json:"actor"`
	Action  string          `json:"action"`
	Changes []chatterChange `json:"changes"`
}

// noisy columns never shown as changes
var historySkip = map[string]bool{"updated_at": true, "created_at": true, "tenant_id": true, "company_id": true, "id": true,
	"created_by": true, "portal_token": true, "photo": true, "image": true, "logo": true, "custom": false, "search": true}

func recordHistory(ctx context.Context, tx pgx.Tx, cid, id string) ([]chatterEvent, error) {
	rows, err := tx.Query(ctx, `SELECT a.at, u.full_name, a.action, a.before::text, a.after::text FROM audit_log a
		LEFT JOIN users u ON u.id = a.user_id WHERE a.entity_id = $1 AND (a.company_id IS NULL OR a.company_id = $2)
		ORDER BY a.id DESC LIMIT 200`, id, cid)
	if err != nil {
		return nil, err
	}
	out := []chatterEvent{}
	for rows.Next() {
		var e chatterEvent
		var before, after *string
		if err := rows.Scan(&e.At, &e.Actor, &e.Action, &before, &after); err != nil {
			rows.Close()
			return nil, err
		}
		var b, a map[string]any
		if before != nil {
			_ = json.Unmarshal([]byte(*before), &b)
		}
		if after != nil {
			_ = json.Unmarshal([]byte(*after), &a)
		}
		e.Changes = []chatterChange{}
		if b != nil && a != nil {
			keys := make([]string, 0, len(a))
			for k := range a {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if historySkip[k] || strings.HasSuffix(k, "_hash") || strings.HasPrefix(k, "search") {
					continue
				}
				if !reflect.DeepEqual(b[k], a[k]) {
					e.Changes = append(e.Changes, chatterChange{Field: k, From: shortVal(b[k]), To: shortVal(a[k])})
				}
			}
			if len(e.Changes) == 0 {
				continue // only noise changed
			}
		}
		out = append(out, e)
	}
	rows.Close()
	return out, rows.Err()
}

func shortVal(v any) any {
	if s, ok := v.(string); ok && len(s) > 120 {
		return s[:120] + "…"
	}
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		b, _ := json.Marshal(m)
		if len(b) > 120 {
			return string(b[:120]) + "…"
		}
		return string(b)
	}
	return v
}

// GET /companies/{cid}/chatter?type=&id=: comments, history and activities of one record.
func (s *Server) getChatter(w http.ResponseWriter, r *http.Request) {
	cid, typ, id := r.PathValue("cid"), r.URL.Query().Get("type"), r.URL.Query().Get("id")
	if !chatterAllowed(r, typ, false) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := attachmentTarget(ctx, tx, cid, typ, id); err != nil {
			return nil, err
		}
		msgs, err := queryJSON(ctx, tx, `SELECT m.id, m.body, m.mentions, m.created_at, m.created_by, u.full_name AS author,
			(m.created_by = app_user()) AS mine,
			(SELECT coalesce(json_agg(json_build_object('id', x.id, 'name', x.full_name)), '[]') FROM users x WHERE x.id = ANY (m.mentions)) AS mentioned
			FROM record_messages m LEFT JOIN users u ON u.id = m.created_by
			WHERE m.company_id = $1 AND m.entity_type = $2 AND m.entity_id = $3 AND m.deleted_at IS NULL ORDER BY m.created_at DESC LIMIT 300`, cid, typ, id)
		if err != nil {
			return nil, err
		}
		acts, err := queryJSON(ctx, tx, activitySelect+` WHERE a.company_id = $1 AND a.entity_type = $2 AND a.entity_id = $3
			ORDER BY a.done_at IS NOT NULL, a.due_date, a.created_at LIMIT 200`, cid, typ, id)
		if err != nil {
			return nil, err
		}
		var hist []chatterEvent
		if can(r, "audit.view") || can(r, attachModule[typ]+".view") {
			if hist, err = recordHistory(ctx, tx, cid, id); err != nil {
				return nil, err
			}
		}
		if hist == nil {
			hist = []chatterEvent{}
		}
		var files int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM attachments WHERE company_id = $1 AND entity_type = $2 AND entity_id = $3 AND deleted_at IS NULL`,
			cid, typ, id).Scan(&files); err != nil {
			return nil, err
		}
		return map[string]any{"messages": msgs, "activities": acts, "history": hist, "files": files}, nil
	})
}

// recordTitle: a short human label of a record for notifications ("Invoice #12 · Rima Haddad").
func recordTitle(ctx context.Context, tx pgx.Tx, typ, id string) string {
	q := map[string]string{
		"invoice":        `SELECT 'Invoice #' || i.number || ' · ' || c.name FROM invoices i JOIN customers c ON c.id = i.customer_id WHERE i.id = $1`,
		"bill":           `SELECT 'Bill #' || b.number || ' · ' || s.name FROM bills b JOIN suppliers s ON s.id = b.supplier_id WHERE b.id = $1`,
		"customer":       `SELECT code || ' · ' || name FROM customers WHERE id = $1`,
		"supplier":       `SELECT name FROM suppliers WHERE id = $1`,
		"item":           `SELECT code || ' · ' || name FROM items WHERE id = $1`,
		"employee":       `SELECT name FROM employees WHERE id = $1`,
		"ticket":         `SELECT 'Ticket #' || number || ' · ' || subject FROM tickets WHERE id = $1`,
		"sales_order":    `SELECT 'Quotation #' || number FROM sales_orders WHERE id = $1`,
		"purchase_order": `SELECT 'PO #' || number FROM purchase_orders WHERE id = $1`,
		"asset":          `SELECT name FROM assets WHERE id = $1`,
		"repair":         `SELECT 'Repair #' || number || ' · ' || device FROM repair_orders WHERE id = $1`,
		"subscription":   `SELECT c.name || ' · ' || p.name FROM subscriptions s JOIN customers c ON c.id = s.customer_id JOIN plans p ON p.id = s.plan_id WHERE s.id = $1`,
		"instalment":     `SELECT 'Instalments #' || p.number || ' · ' || c.name FROM instalment_plans p JOIN customers c ON c.id = p.customer_id WHERE p.id = $1`,
		"lead":           `SELECT title FROM crm_leads WHERE id = $1`,
		"project":        `SELECT 'Project #' || number || ' · ' || name FROM projects WHERE id = $1`,
		"project_task":   `SELECT 'Task #' || number || ' · ' || title FROM project_tasks WHERE id = $1`,
		"service_job":    `SELECT 'Job #' || number || ' · ' || title FROM service_jobs WHERE id = $1`,
		"vehicle":        `SELECT name || coalesce(' · ' || nullif(plate, ''), '') FROM vehicles WHERE id = $1`,
		"applicant":      `SELECT 'Applicant #' || number || ' - ' || name FROM job_applicants WHERE id = $1`,
		"sign_request":   `SELECT 'Signature #' || number || ' - ' || title FROM sign_requests WHERE id = $1`,
		"job":            `SELECT title FROM job_positions WHERE id = $1`,
		"campaign":       `SELECT 'Campaign #' || number || ' - ' || name FROM mkt_campaigns WHERE id = $1`,
	}[typ]
	if q == "" {
		return typ
	}
	var t string
	if err := tx.QueryRow(ctx, q, id).Scan(&t); err != nil {
		return typ
	}
	return t
}

// notifyUsers stores one in-app notification per user (not the actor) and e-mails them when the company has SMTP.
func notifyUsers(ctx context.Context, tx pgx.Tx, cid string, users []string, kind, title, body, typ, id string) error {
	if len(users) == 0 {
		return nil
	}
	var smtp *string
	_ = tx.QueryRow(ctx, `SELECT id::text FROM message_providers WHERE company_id = $1 AND is_active AND kind = 'smtp' ORDER BY is_default DESC LIMIT 1`, cid).Scan(&smtp)
	seen := map[string]bool{}
	for _, u := range users {
		if seen[u] || !isUUID(u) {
			continue
		}
		seen[u] = true
		var email string
		var ok bool
		// only people who exist, are active, are not the author and may see this company
		if err := tx.QueryRow(ctx, `SELECT email, true FROM users WHERE id = $1 AND is_active AND id <> coalesce(app_user(), '00000000-0000-0000-0000-000000000000'::uuid)
			AND (company_ids IS NULL OR $2::uuid = ANY (company_ids))`, u, cid).Scan(&email, &ok); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return err
		}
		var et, eid *string
		if typ != "" {
			et, eid = &typ, &id
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_notifications (company_id, user_id, kind, title, body, entity_type, entity_id)
			VALUES ($1, $2, $3, left($4, 300), left($5, 1000), $6, $7)`, cid, u, kind, title, body, et, eid); err != nil {
			return err
		}
		if smtp != nil && email != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO messages (company_id, phone, channel, provider_id, event, body, params, email, subject)
				VALUES ($1, '', 'email', $2, 'custom', $3, '[]', $4, left($5, 200))`, cid, *smtp, body, email, "Paxve · "+title); err != nil {
				return err
			}
		}
	}
	return nil
}

// POST /companies/{cid}/chatter {type, id, body, mentions: [user ids]}
func (s *Server) postChatter(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type     string   `json:"type"`
		ID       string   `json:"id"`
		Body     string   `json:"body"`
		Mentions []string `json:"mentions"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" || len(in.Body) > 5000 || len(in.Mentions) > 20 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, m := range in.Mentions {
		if !isUUID(m) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	if !chatterAllowed(r, in.Type, true) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := attachmentTarget(ctx, tx, cid, in.Type, in.ID); err != nil {
			return nil, err
		}
		mentions := []string{}
		for _, m := range in.Mentions {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND is_active)`, m).Scan(&ok); err != nil {
				return nil, err
			}
			if ok {
				mentions = append(mentions, m)
			}
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO record_messages (company_id, entity_type, entity_id, body, mentions) VALUES ($1, $2, $3, $4, $5::uuid[])
			RETURNING id::text`, cid, in.Type, in.ID, in.Body, mentions).Scan(&id); err != nil {
			return nil, err
		}
		title := recordTitle(ctx, tx, in.Type, in.ID)
		me := principal(r).FullName
		if err := notifyUsers(ctx, tx, cid, mentions, "mention", title, me+": "+in.Body, in.Type, in.ID); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT m.id, m.body, m.mentions, m.created_at, u.full_name AS author, true AS mine FROM record_messages m
			LEFT JOIN users u ON u.id = m.created_by WHERE m.id = $1`, id)
	})
}

// DELETE /companies/{cid}/chatter/{id}: the author hides their comment (kept for the audit trail).
func (s *Server) deleteChatter(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var typ string
		var mine bool
		err := tx.QueryRow(ctx, `SELECT entity_type, created_by = app_user() FROM record_messages WHERE id = $1 AND company_id = $2 AND deleted_at IS NULL`,
			id, cid).Scan(&typ, &mine)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if !mine && !can(r, "settings.manage") {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		if _, err := tx.Exec(ctx, `UPDATE record_messages SET deleted_at = now() WHERE id = $1`, id); err != nil {
			return nil, err
		}
		return map[string]string{"deleted": id}, nil
	})
}

// GET /companies/{cid}/mentionable: people who can be @mentioned in this company.
func (s *Server) mentionable(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT id, full_name FROM users WHERE is_active AND (company_ids IS NULL OR $1::uuid = ANY (company_ids))
			ORDER BY full_name LIMIT 500`, r.PathValue("cid"))
	})
}

// GET /companies/{cid}/notifications: the signed-in user's latest notifications and counts.
func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		items, err := queryJSON(ctx, tx, `SELECT n.id, n.kind, n.title, n.body, n.entity_type, n.entity_id, n.read_at, n.created_at, u.full_name AS actor
			FROM user_notifications n LEFT JOIN users u ON u.id = n.actor_id
			WHERE n.company_id = $1 AND n.user_id = app_user() ORDER BY n.created_at DESC LIMIT 100`, cid)
		if err != nil {
			return nil, err
		}
		var unread, overdue, todayN, planned int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM user_notifications WHERE company_id = $1 AND user_id = app_user() AND read_at IS NULL),
			count(*) FILTER (WHERE due_date < current_date), count(*) FILTER (WHERE due_date = current_date), count(*) FILTER (WHERE due_date > current_date)
			FROM record_activities WHERE company_id = $1 AND assigned_to = app_user() AND done_at IS NULL`, cid).Scan(&unread, &overdue, &todayN, &planned); err != nil {
			return nil, err
		}
		return map[string]any{"items": items, "unread": unread, "activities": map[string]int{"overdue": overdue, "today": todayN, "planned": planned}}, nil
	})
}

// POST /companies/{cid}/notifications/read {ids: [...]} (none = all)
func (s *Server) readNotifications(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	for _, id := range in.IDs {
		if !isUUID(id) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	if in.IDs == nil {
		in.IDs = []string{}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE user_notifications SET read_at = now() WHERE company_id = $1 AND user_id = app_user() AND read_at IS NULL
			AND (cardinality($2::uuid[]) = 0 OR id = ANY ($2::uuid[]))`, r.PathValue("cid"), in.IDs)
		if err != nil {
			return nil, err
		}
		return map[string]int64{"read": tag.RowsAffected()}, nil
	})
}

// ======================= activities =======================

const activitySelect = `SELECT a.id, a.entity_type, a.entity_id, a.kind, a.summary, a.note, a.due_date, a.assigned_to, u.full_name AS assigned_name,
	a.done_at, a.done_note, d.full_name AS done_by_name, cb.full_name AS created_by_name, a.created_at,
	CASE WHEN a.done_at IS NOT NULL THEN 'done' WHEN a.due_date < current_date THEN 'overdue' WHEN a.due_date = current_date THEN 'today' ELSE 'planned' END AS state
	FROM record_activities a LEFT JOIN users u ON u.id = a.assigned_to LEFT JOIN users d ON d.id = a.done_by LEFT JOIN users cb ON cb.id = a.created_by`

var recActivityKinds = map[string]bool{"call": true, "meeting": true, "todo": true, "followup": true, "email": true, "visit": true}

// GET /companies/{cid}/record-activities?scope=mine|all&state=open|done|all
func (s *Server) listRecordActivities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scopeQ, state := q.Get("scope"), q.Get("state")
	if scopeQ == "" {
		scopeQ = "mine"
	}
	if state == "" {
		state = "open"
	}
	if (scopeQ != "mine" && scopeQ != "all") || (state != "open" && state != "done" && state != "all") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	all := scopeQ == "all" && (can(r, "users.view") || can(r, "settings.manage"))
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, activitySelect+` WHERE a.company_id = $1 AND ($2 OR a.assigned_to = app_user() OR a.created_by = app_user())
			AND ($3 = 'all' OR ($3 = 'open' AND a.done_at IS NULL) OR ($3 = 'done' AND a.done_at IS NOT NULL))
			ORDER BY a.done_at IS NOT NULL, a.due_date, a.created_at LIMIT 1000`, r.PathValue("cid"), all, state)
		if err != nil {
			return nil, err
		}
		// a readable label for each linked record
		var list []map[string]any
		_ = json.Unmarshal(rows, &list)
		for _, a := range list {
			if t, ok := a["entity_type"].(string); ok && t != "" {
				a["record"] = recordTitle(ctx, tx, t, fmt.Sprint(a["entity_id"]))
			}
		}
		if list == nil {
			list = []map[string]any{}
		}
		return list, nil
	})
}

type activityIn struct {
	EntityType *string `json:"entity_type"`
	EntityID   *string `json:"entity_id"`
	Kind       *string `json:"kind"`
	Summary    *string `json:"summary"`
	Note       *string `json:"note"`
	DueDate    *string `json:"due_date"`
	AssignedTo *string `json:"assigned_to"`
}

func (s *Server) createRecordActivity(w http.ResponseWriter, r *http.Request) {
	var in activityIn
	if !decodeBody(w, r, &in) {
		return
	}
	summary, kind, due := strings.TrimSpace(deref(in.Summary)), deref(in.Kind), deref(in.DueDate)
	if kind == "" {
		kind = "todo"
	}
	if due == "" {
		due = today()
	}
	if summary == "" || len(summary) > 300 || !recActivityKinds[kind] || !validDate(due) || len(deref(in.Note)) > 4000 ||
		(in.AssignedTo != nil && *in.AssignedTo != "" && !isUUID(*in.AssignedTo)) || (deref(in.EntityType) == "") != (deref(in.EntityID) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	typ, eid := deref(in.EntityType), deref(in.EntityID)
	if typ != "" && !chatterAllowed(r, typ, false) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if typ != "" {
			if err := attachmentTarget(ctx, tx, cid, typ, eid); err != nil {
				return nil, err
			}
		}
		assignee := principal(r).ID
		if a := deref(in.AssignedTo); a != "" {
			if err := checkCompanyUser(ctx, tx, cid, a); err != nil {
				return nil, err
			}
			assignee = a
		}
		var et, ei *string
		if typ != "" {
			et, ei = &typ, &eid
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO record_activities (company_id, entity_type, entity_id, kind, summary, note, due_date, assigned_to)
			VALUES ($1, $2, $3, $4, $5, $6, $7::date, $8) RETURNING id::text`, cid, et, ei, kind, summary, strings.TrimSpace(deref(in.Note)), due, assignee).Scan(&id); err != nil {
			return nil, err
		}
		if assignee != principal(r).ID {
			title := summary
			if typ != "" {
				title = recordTitle(ctx, tx, typ, eid) + " · " + summary
			}
			if err := notifyUsers(ctx, tx, cid, []string{assignee}, "activity", title, principal(r).FullName+" → "+due, typ, eid); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, activitySelect+` WHERE a.id = $1`, id)
	})
}

func checkCompanyUser(ctx context.Context, tx pgx.Tx, cid, uid string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND is_active AND (company_ids IS NULL OR $2::uuid = ANY (company_ids)))`,
		uid, cid).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return badRequest("invalid_user")
	}
	return nil
}

// lockActivity: the activity, if the user may change it (assignee, author, or a manager).
func lockActivity(ctx context.Context, tx pgx.Tx, r *http.Request, cid, id string) (bool, error) {
	var mine, done bool
	err := tx.QueryRow(ctx, `SELECT (assigned_to = app_user() OR created_by = app_user()), done_at IS NOT NULL FROM record_activities
		WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&mine, &done)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, notFound()
	}
	if err != nil {
		return false, err
	}
	if !mine && !can(r, "settings.manage") && !can(r, "users.edit") {
		return false, &apiError{status: http.StatusForbidden, code: "forbidden"}
	}
	return done, nil
}

// PATCH /companies/{cid}/record-activities/{id}
func (s *Server) updateRecordActivity(w http.ResponseWriter, r *http.Request) {
	var in activityIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.Kind != nil && !recActivityKinds[*in.Kind]) || (in.DueDate != nil && !validDate(*in.DueDate)) ||
		(in.Summary != nil && (strings.TrimSpace(*in.Summary) == "" || len(*in.Summary) > 300)) || (in.AssignedTo != nil && !isUUID(*in.AssignedTo)) ||
		len(deref(in.Note)) > 4000 || in.EntityType != nil || in.EntityID != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := lockActivity(ctx, tx, r, cid, id); err != nil {
			return nil, err
		}
		if in.AssignedTo != nil {
			if err := checkCompanyUser(ctx, tx, cid, *in.AssignedTo); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE record_activities SET kind = coalesce($2, kind), summary = coalesce(trim($3), summary), note = coalesce($4, note),
			due_date = coalesce($5::date, due_date), assigned_to = coalesce($6::uuid, assigned_to), updated_at = now() WHERE id = $1`,
			id, in.Kind, in.Summary, in.Note, in.DueDate, in.AssignedTo); err != nil {
			return nil, err
		}
		if in.AssignedTo != nil && *in.AssignedTo != principal(r).ID {
			var summary string
			var typ, eid *string
			_ = tx.QueryRow(ctx, `SELECT summary, entity_type, entity_id::text FROM record_activities WHERE id = $1`, id).Scan(&summary, &typ, &eid)
			if err := notifyUsers(ctx, tx, cid, []string{*in.AssignedTo}, "activity", summary, principal(r).FullName, deref(typ), deref(eid)); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, activitySelect+` WHERE a.id = $1`, id)
	})
}

// POST /companies/{cid}/record-activities/{id}/done {note}: marks it done (or reopens with {"reopen": true}).
func (s *Server) doneRecordActivity(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note   string `json:"note"`
		Reopen bool   `json:"reopen"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || len(in.Note) > 4000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		done, err := lockActivity(ctx, tx, r, cid, id)
		if err != nil {
			return nil, err
		}
		if in.Reopen {
			if !done {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			_, err = tx.Exec(ctx, `UPDATE record_activities SET done_at = NULL, done_by = NULL, updated_at = now() WHERE id = $1`, id)
		} else {
			if done {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			_, err = tx.Exec(ctx, `UPDATE record_activities SET done_at = now(), done_by = app_user(), done_note = $2, updated_at = now() WHERE id = $1`,
				id, strings.TrimSpace(in.Note))
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, activitySelect+` WHERE a.id = $1`, id)
	})
}

// RunActivityReminders: one in-app reminder per activity when it falls due (again if its date is moved).
func (s *Server) RunActivityReminders(ctx context.Context) error {
	rows, err := s.OwnerPool.Query(ctx, `SELECT DISTINCT a.tenant_id::text FROM record_activities a
		WHERE a.done_at IS NULL AND a.due_date <= current_date AND (a.reminded_on IS NULL OR a.reminded_on < a.due_date) AND tenant_jobs_ok(a.tenant_id)`)
	if err != nil {
		return err
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, t := range tenants {
		err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, t); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `WITH due AS (
					UPDATE record_activities SET reminded_on = current_date
					WHERE done_at IS NULL AND due_date <= current_date AND (reminded_on IS NULL OR reminded_on < due_date)
					RETURNING company_id, assigned_to, summary, due_date, entity_type, entity_id)
				INSERT INTO user_notifications (company_id, user_id, kind, title, body, entity_type, entity_id, actor_id)
				SELECT company_id, assigned_to, 'activity_due', left(summary, 300), due_date::text, entity_type, entity_id, NULL FROM due`)
			return err
		})
		if err != nil {
			slog.Error("activity reminders", "tenant", t, "err", err)
		}
	}
	return nil
}
