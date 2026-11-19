package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Service desk (app "svc"): customer requests, complaints and faults as numbered tickets with notes,
// plus generator maintenance plans (every N running hours and/or every N days) and a maintenance log.

var (
	ticketCategories = []string{"outage", "no_internet", "slow", "no_signal", "meter", "billing", "new_connection", "move", "disconnect", "complaint", "other"}
	ticketPriorities = []string{"low", "normal", "high", "urgent"}
	ticketStatuses   = []string{"new", "in_progress", "waiting", "done", "cancelled"}
	ticketChannels   = []string{"phone", "whatsapp", "office", "portal", "field", "other"}
)

const ticketCols = `t.id, t.number, t.customer_id, c.name AS customer_name, c.code AS customer_code, t.subscription_id, t.category,
	t.priority, t.status, t.subject, t.description, t.contact_phone, t.channel, t.assigned_to, au.full_name AS assigned_name, t.due_at,
	t.resolved_at, t.resolution, cu.full_name AS created_by_name, t.created_at, t.updated_at,
	(t.due_at IS NOT NULL AND t.due_at < now() AND t.status NOT IN ('done', 'cancelled')) AS overdue,
	round(extract(epoch FROM (coalesce(t.resolved_at, now()) - t.created_at)) / 3600, 1) AS age_hours`

const ticketFrom = ` FROM tickets t LEFT JOIN customers c ON c.id = t.customer_id LEFT JOIN users au ON au.id = t.assigned_to
	LEFT JOIN users cu ON cu.id = t.created_by`

// GET /companies/{cid}/tickets?status=open|all|<status>&customer_id=&q=
func (s *Server) listTickets(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	q := r.URL.Query()
	st, cust, text := q.Get("status"), q.Get("customer_id"), strings.TrimSpace(q.Get("q"))
	if st == "" {
		st = "open"
	}
	if (st != "open" && st != "all" && !slices.Contains(ticketStatuses, st)) || (cust != "" && !isUUID(cust)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+ticketCols+ticketFrom+` WHERE t.company_id = $1
			AND ($2 = 'all' OR ($2 = 'open' AND t.status NOT IN ('done', 'cancelled')) OR t.status = $2)
			AND ($3 = '' OR t.customer_id = nullif($3, '')::uuid)
			AND ($4 = '' OR t.subject ILIKE '%' || $4 || '%' OR c.name ILIKE '%' || $4 || '%' OR c.code = $4 OR t.number::text = $4 OR t.contact_phone LIKE '%' || $4 || '%')
			ORDER BY CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, t.created_at DESC LIMIT 1000`,
			cid, st, cust, text)
	})
}

type ticketIn struct {
	CustomerID     *string `json:"customer_id"`
	SubscriptionID *string `json:"subscription_id"`
	Category       *string `json:"category"`
	Priority       *string `json:"priority"`
	Status         *string `json:"status"`
	Subject        *string `json:"subject"`
	Description    *string `json:"description"`
	ContactPhone   *string `json:"contact_phone"`
	Channel        *string `json:"channel"`
	AssignedTo     *string `json:"assigned_to"`
	DueAt          *string `json:"due_at"`
	Resolution     *string `json:"resolution"`
}

func (in *ticketIn) valid() bool {
	okUUID := func(p *string) bool { return p == nil || *p == "" || isUUID(*p) }
	okIn := func(p *string, list []string) bool { return p == nil || slices.Contains(list, *p) }
	if in.DueAt != nil && *in.DueAt != "" {
		if _, err := time.Parse(time.RFC3339, *in.DueAt); err != nil {
			if _, err := time.Parse(time.DateOnly, *in.DueAt); err != nil {
				return false
			}
		}
	}
	return okUUID(in.CustomerID) && okUUID(in.SubscriptionID) && okUUID(in.AssignedTo) && okIn(in.Category, ticketCategories) &&
		okIn(in.Priority, ticketPriorities) && okIn(in.Status, ticketStatuses) && okIn(in.Channel, ticketChannels)
}

// POST /companies/{cid}/tickets
func (s *Server) createTicket(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	var in ticketIn
	if err := decode(w, r, &in); err != nil || !in.valid() || in.Subject == nil || strings.TrimSpace(*in.Subject) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('tickets:' || $1))`, cid); err != nil {
			return nil, err
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO tickets (company_id, number, customer_id, subscription_id, category, priority, subject, description,
				contact_phone, channel, assigned_to, due_at)
			VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM tickets WHERE company_id = $1), nullif($2, '')::uuid, nullif($3, '')::uuid,
				coalesce($4, 'other'), coalesce($5, 'normal'), trim($6), coalesce($7, ''),
				coalesce($8, (SELECT phone FROM customers WHERE id = nullif($2, '')::uuid), ''), coalesce($9, 'phone'), nullif($10, '')::uuid,
				nullif($11, '')::timestamptz)
			RETURNING id::text`, cid, deref(in.CustomerID), deref(in.SubscriptionID), in.Category, in.Priority, *in.Subject, in.Description,
			in.ContactPhone, in.Channel, deref(in.AssignedTo), deref(in.DueAt)).Scan(&id)
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+ticketCols+ticketFrom+` WHERE t.id = $1`, id)
	})
}

// GET /companies/{cid}/tickets/{id}
func (s *Server) getTicket(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		t, err := queryJSONOne(ctx, tx, `SELECT `+ticketCols+ticketFrom+` WHERE t.id = $1 AND t.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		notes, err := queryJSON(ctx, tx, `SELECT n.id, n.body, u.full_name AS created_by_name, n.created_at FROM ticket_notes n
			LEFT JOIN users u ON u.id = n.created_by WHERE n.ticket_id = $1 ORDER BY n.created_at`, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ticket": t, "notes": notes}, nil
	})
}

// PATCH /companies/{cid}/tickets/{id}: status / priority / assignment / details. Every status change
// is written as a note so the history reads like a conversation.
func (s *Server) updateTicket(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in ticketIn
	if err := decode(w, r, &in); err != nil || !in.valid() || (in.Subject != nil && strings.TrimSpace(*in.Subject) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var old string
		err := tx.QueryRow(ctx, `SELECT status FROM tickets WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&old)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		clearAssign := in.AssignedTo != nil && *in.AssignedTo == ""
		clearDue := in.DueAt != nil && *in.DueAt == ""
		if _, err := tx.Exec(ctx, `UPDATE tickets SET
				customer_id = CASE WHEN $3::text IS NULL THEN customer_id ELSE nullif($3, '')::uuid END,
				category = coalesce($4, category), priority = coalesce($5, priority), status = coalesce($6, status),
				subject = coalesce(trim($7), subject), description = coalesce($8, description), contact_phone = coalesce($9, contact_phone),
				channel = coalesce($10, channel),
				assigned_to = CASE WHEN $13 THEN NULL WHEN $11::text IS NULL THEN assigned_to ELSE $11::uuid END,
				due_at = CASE WHEN $14 THEN NULL WHEN $12::text IS NULL THEN due_at ELSE $12::timestamptz END,
				resolution = coalesce($15, resolution),
				resolved_at = CASE WHEN coalesce($6, status) IN ('done', 'cancelled') THEN coalesce(resolved_at, now()) ELSE NULL END,
				updated_at = now()
			WHERE id = $1 AND company_id = $2`, id, cid, in.CustomerID, in.Category, in.Priority, in.Status, in.Subject, in.Description,
			in.ContactPhone, in.Channel, nilIfEmpty(in.AssignedTo), nilIfEmpty(in.DueAt), clearAssign, clearDue, in.Resolution); err != nil {
			return nil, err
		}
		if in.Status != nil && *in.Status != old {
			if _, err := tx.Exec(ctx, `INSERT INTO ticket_notes (company_id, ticket_id, body) VALUES ($1, $2, $3)`,
				cid, id, "status: "+old+" → "+*in.Status); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+ticketCols+ticketFrom+` WHERE t.id = $1`, id)
	})
}

func nilIfEmpty(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}

// POST /companies/{cid}/tickets/{id}/notes {body}
func (s *Server) addTicketNote(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	var in struct {
		Body string `json:"body"`
	}
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if err := decode(w, r, &in); err != nil || strings.TrimSpace(in.Body) == "" || len(in.Body) > 8000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tickets WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, notFound()
		}
		if _, err := tx.Exec(ctx, `UPDATE tickets SET updated_at = now() WHERE id = $1`, id); err != nil {
			return nil, err
		}
		var nid string
		if err := tx.QueryRow(ctx, `INSERT INTO ticket_notes (company_id, ticket_id, body) VALUES ($1, $2, trim($3)) RETURNING id::text`,
			cid, id, in.Body).Scan(&nid); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, body, created_at FROM ticket_notes WHERE id = $1`, nid)
	})
}

// GET /companies/{cid}/tickets/summary: open counts by status / category / priority, overdue, and the
// last 30 days (opened, closed, average hours to close).
func (s *Server) ticketSummary(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT
			(SELECT count(*) FROM tickets WHERE company_id = $1 AND status NOT IN ('done', 'cancelled')) AS open,
			(SELECT count(*) FROM tickets WHERE company_id = $1 AND status NOT IN ('done', 'cancelled') AND due_at < now()) AS overdue,
			(SELECT count(*) FROM tickets WHERE company_id = $1 AND status NOT IN ('done', 'cancelled') AND priority = 'urgent') AS urgent,
			(SELECT coalesce(jsonb_object_agg(category, n), '{}') FROM (SELECT category, count(*) n FROM tickets
				WHERE company_id = $1 AND status NOT IN ('done', 'cancelled') GROUP BY 1) x) AS by_category,
			(SELECT count(*) FROM tickets WHERE company_id = $1 AND created_at > now() - interval '30 days') AS opened_30d,
			(SELECT count(*) FROM tickets WHERE company_id = $1 AND resolved_at > now() - interval '30 days' AND status = 'done') AS closed_30d,
			(SELECT round(avg(extract(epoch FROM (resolved_at - created_at)) / 3600)::numeric, 1) FROM tickets
				WHERE company_id = $1 AND resolved_at > now() - interval '30 days' AND status = 'done') AS avg_hours_30d`, cid)
	})
}

// ---------- generator maintenance ----------

// GET /companies/{cid}/maintenance: every plan with the generator's current hour meter and what is due.
// state: due (passed), soon (within 10 % of the interval or 7 days), ok.
func (s *Server) listMaintenance(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `
			WITH p AS (
				SELECT m.*, g.name AS generator_name, st.name AS station_name,
					(SELECT h.hour_meter FROM generator_hours h WHERE h.generator_id = m.generator_id ORDER BY h.log_date DESC LIMIT 1) AS hours_now
				FROM maintenance_plans m JOIN generators g ON g.id = m.generator_id JOIN stations st ON st.id = g.station_id
				WHERE m.company_id = $1)
			SELECT id, generator_id, generator_name, station_name, task, every_hours::text AS every_hours, every_days, last_done_date,
				last_done_hours::text AS last_done_hours, is_active, notes, hours_now::text AS hours_now,
				CASE WHEN every_hours IS NOT NULL THEN (coalesce(last_done_hours, 0) + every_hours)::text END AS due_hours,
				CASE WHEN every_days IS NOT NULL THEN coalesce(last_done_date, created_at::date) + every_days END AS due_date,
				CASE WHEN every_hours IS NOT NULL AND hours_now IS NOT NULL THEN (coalesce(last_done_hours, 0) + every_hours - hours_now)::text END AS hours_left,
				CASE WHEN every_days IS NOT NULL THEN coalesce(last_done_date, created_at::date) + every_days - current_date END AS days_left,
				CASE
					WHEN NOT is_active THEN 'off'
					WHEN (every_hours IS NOT NULL AND hours_now IS NOT NULL AND hours_now >= coalesce(last_done_hours, 0) + every_hours)
					  OR (every_days IS NOT NULL AND current_date >= coalesce(last_done_date, created_at::date) + every_days) THEN 'due'
					WHEN (every_hours IS NOT NULL AND hours_now IS NOT NULL AND hours_now >= coalesce(last_done_hours, 0) + every_hours * 0.9)
					  OR (every_days IS NOT NULL AND current_date >= coalesce(last_done_date, created_at::date) + every_days - 7) THEN 'soon'
					ELSE 'ok' END AS state
			FROM p ORDER BY CASE WHEN NOT is_active THEN 3 ELSE 0 END, generator_name, task`, cid)
	})
}

// POST /companies/{cid}/maintenance/plans and PATCH .../plans/{id}
func (s *Server) saveMaintenancePlan(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if !create && !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		GeneratorID   *string `json:"generator_id"`
		Task          *string `json:"task"`
		EveryHours    *string `json:"every_hours"`
		EveryDays     *int    `json:"every_days"`
		LastDoneDate  *string `json:"last_done_date"`
		LastDoneHours *string `json:"last_done_hours"`
		IsActive      *bool   `json:"is_active"`
		Notes         *string `json:"notes"`
	}
	if err := decode(w, r, &in); err != nil || (create && (in.GeneratorID == nil || !isUUID(*in.GeneratorID) || in.Task == nil)) ||
		(in.Task != nil && strings.TrimSpace(*in.Task) == "") || (in.LastDoneDate != nil && *in.LastDoneDate != "" && !validDate(*in.LastDoneDate)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, p := range []*string{in.EveryHours, in.LastDoneHours} {
		if p != nil && *p != "" {
			if d, ok := money(*p); !ok || d.IsNegative() {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
		}
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[create], func(ctx context.Context, tx pgx.Tx) (any, error) {
		var out string
		var err error
		if create {
			if in.EveryHours == nil && in.EveryDays == nil {
				return nil, badRequest("bad_request")
			}
			err = tx.QueryRow(ctx, `INSERT INTO maintenance_plans (company_id, generator_id, task, every_hours, every_days, last_done_date, last_done_hours, notes)
				VALUES ($1, $2, trim($3), nullif($4, '')::numeric, $5, nullif($6, '')::date, nullif($7, '')::numeric, coalesce($8, '')) RETURNING id::text`,
				cid, *in.GeneratorID, *in.Task, deref(in.EveryHours), in.EveryDays, deref(in.LastDoneDate), deref(in.LastDoneHours), in.Notes).Scan(&out)
		} else {
			err = tx.QueryRow(ctx, `UPDATE maintenance_plans SET task = coalesce(trim($3), task),
				every_hours = CASE WHEN $4::text IS NULL THEN every_hours ELSE nullif($4, '')::numeric END,
				every_days = CASE WHEN $5::int IS NULL THEN every_days WHEN $5 = 0 THEN NULL ELSE $5 END,
				last_done_date = CASE WHEN $6::text IS NULL THEN last_done_date ELSE nullif($6, '')::date END,
				last_done_hours = CASE WHEN $7::text IS NULL THEN last_done_hours ELSE nullif($7, '')::numeric END,
				is_active = coalesce($8, is_active), notes = coalesce($9, notes), updated_at = now()
				WHERE id = $1 AND company_id = $2 RETURNING id::text`,
				id, cid, in.Task, in.EveryHours, in.EveryDays, in.LastDoneDate, in.LastDoneHours, in.IsActive, in.Notes).Scan(&out)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, generator_id, task, every_hours::text AS every_hours, every_days, last_done_date, last_done_hours::text AS last_done_hours, is_active, notes FROM maintenance_plans WHERE id = $1`, out)
	})
}

// POST /companies/{cid}/maintenance/log: record work done; a plan's "last done" moves forward.
func (s *Server) logMaintenance(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	var in struct {
		GeneratorID string `json:"generator_id"`
		PlanID      string `json:"plan_id"`
		DoneDate    string `json:"done_date"`
		HourMeter   string `json:"hour_meter"`
		Task        string `json:"task"`
		Cost        string `json:"cost"`
		Currency    string `json:"currency"`
		Notes       string `json:"notes"`
	}
	if err := decode(w, r, &in); err != nil || (in.PlanID != "" && !isUUID(in.PlanID)) || (in.GeneratorID != "" && !isUUID(in.GeneratorID)) ||
		(in.PlanID == "" && (in.GeneratorID == "" || strings.TrimSpace(in.Task) == "")) || (in.Currency != "" && !currencyRe.MatchString(in.Currency)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.DoneDate == "" {
		in.DoneDate = time.Now().Format(time.DateOnly)
	}
	cost := decimal.Zero
	if in.Cost != "" {
		d, ok := money(in.Cost)
		if !ok || d.IsNegative() {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		cost = d
	}
	if !validDate(in.DoneDate) || (in.HourMeter != "" && !validHours(in.HourMeter)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.PlanID != "" {
			var gen, task string
			err := tx.QueryRow(ctx, `SELECT generator_id::text, task FROM maintenance_plans WHERE id = $1 AND company_id = $2`, in.PlanID, cid).Scan(&gen, &task)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			if err != nil {
				return nil, err
			}
			in.GeneratorID = gen
			if strings.TrimSpace(in.Task) == "" {
				in.Task = task
			}
		}
		if in.HourMeter == "" { // default: the generator's latest hour meter
			var h *string
			_ = tx.QueryRow(ctx, `SELECT hour_meter::text FROM generator_hours WHERE generator_id = $1 ORDER BY log_date DESC LIMIT 1`, in.GeneratorID).Scan(&h)
			if h != nil {
				in.HourMeter = *h
			}
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO maintenance_log (company_id, generator_id, plan_id, done_date, hour_meter, task, cost, currency, notes)
			VALUES ($1, $2, nullif($3, '')::uuid, $4, nullif($5, '')::numeric, trim($6), $7, nullif($8, ''), $9) RETURNING id::text`,
			cid, in.GeneratorID, in.PlanID, in.DoneDate, in.HourMeter, in.Task, cost.String(), in.Currency, in.Notes).Scan(&id)
		if err != nil {
			return nil, err
		}
		if in.PlanID != "" {
			if _, err := tx.Exec(ctx, `UPDATE maintenance_plans SET last_done_date = greatest(coalesce(last_done_date, $2::date), $2::date),
				last_done_hours = coalesce(nullif($3, '')::numeric, last_done_hours), updated_at = now() WHERE id = $1`,
				in.PlanID, in.DoneDate, in.HourMeter); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT id, generator_id, plan_id, done_date, hour_meter::text AS hour_meter, task, cost::text AS cost, currency, notes FROM maintenance_log WHERE id = $1`, id)
	})
}

// GET /companies/{cid}/maintenance/log?generator_id=
func (s *Server) listMaintenanceLog(w http.ResponseWriter, r *http.Request) {
	cid, gen := r.PathValue("cid"), r.URL.Query().Get("generator_id")
	if gen != "" && !isUUID(gen) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT l.id, l.generator_id, g.name AS generator_name, l.plan_id, l.done_date, l.hour_meter::text AS hour_meter, l.task,
				l.cost::text AS cost, l.currency, l.notes, u.full_name AS created_by_name, l.created_at
			FROM maintenance_log l JOIN generators g ON g.id = l.generator_id LEFT JOIN users u ON u.id = l.created_by
			WHERE l.company_id = $1 AND ($2 = '' OR l.generator_id = nullif($2, '')::uuid)
			ORDER BY l.done_date DESC, l.created_at DESC LIMIT 1000`, cid, gen)
	})
}

func validHours(s string) bool {
	d, ok := money(s)
	return ok && !d.IsNegative()
}
