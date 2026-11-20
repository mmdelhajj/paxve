package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Field service (app "svc"): service jobs for a customer (often from a ticket), planned on a dispatch board per
// technician, done from the technician's phone (start, parts from the van's stock, report, customer signature,
// photo and location), then invoiced (labour hours × rate + parts, which leave stock with the invoice).
//
// Helpdesk SLA: policies by priority / category give each ticket a first-response and a resolution deadline;
// breaches are flagged, escalated to a manager and reported. Tickets also arrive by e-mail (an inbound-mail
// service posts to /inbound/{token}/email) and WhatsApp (the Cloud API webhook posts to /inbound/{token}/whatsapp).
// A closed ticket gets a rating link the customer opens without signing in.

var jobStatuses = []string{"new", "scheduled", "in_progress", "done", "cancelled"}

// ======================= service jobs =======================

const jobCols = `j.id, j.number, j.customer_id, c.name AS customer_name, c.code AS customer_code, j.ticket_id, tk.number AS ticket_number,
	j.project_id, p.name AS project_name, j.title, j.description, j.address, j.phone, j.technician_id, tu.full_name AS technician_name,
	j.scheduled_start, j.duration_min, j.priority, j.status, j.warehouse_id, w.name AS warehouse_name, j.currency, j.labour_rate::text AS labour_rate,
	trim_scale(j.labour_hours)::text AS labour_hours, j.started_at, j.finished_at, j.report, j.signed_by, (j.signature <> '') AS signed, j.lat, j.lng,
	j.invoice_id, i.number AS invoice_number, (j.invoice_id IS NOT NULL AND i.status <> 'void') AS invoiced, j.created_at,
	(SELECT count(*) FROM attachments a WHERE a.entity_type = 'service_job' AND a.entity_id = j.id AND a.deleted_at IS NULL) AS photos,
	(j.scheduled_start IS NOT NULL AND j.scheduled_start < now() AND j.status IN ('new', 'scheduled')) AS late`

const jobFrom = ` FROM service_jobs j JOIN customers c ON c.id = j.customer_id LEFT JOIN tickets tk ON tk.id = j.ticket_id
	LEFT JOIN projects p ON p.id = j.project_id LEFT JOIN users tu ON tu.id = j.technician_id LEFT JOIN warehouses w ON w.id = j.warehouse_id
	LEFT JOIN invoices i ON i.id = j.invoice_id`

// GET /fs/service-jobs?status=open|all|<status>&technician_id=&from=&to=&q=&ticket_id=
func (s *Server) fsJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, tech, from, to, text, ticket := q.Get("status"), q.Get("technician_id"), q.Get("from"), q.Get("to"), strings.TrimSpace(q.Get("q")), q.Get("ticket_id")
	if st == "" {
		st = "open"
	}
	if (st != "open" && st != "all" && !contains(jobStatuses, st)) || (tech != "" && !isUUID(tech)) || (from != "" && !validDate(from)) ||
		(to != "" && !validDate(to)) || len(text) > 100 || (ticket != "" && !isUUID(ticket)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+jobCols+jobFrom+` WHERE j.company_id = $1
			AND ($2 = 'all' OR ($2 = 'open' AND j.status IN ('new', 'scheduled', 'in_progress')) OR j.status = $2)
			AND ($3 = '' OR j.technician_id = nullif($3, '')::uuid)
			AND ($4 = '' OR j.scheduled_start >= $4::date) AND ($5 = '' OR j.scheduled_start < $5::date + 1)
			AND ($6 = '' OR j.title ILIKE '%' || $6 || '%' OR c.name ILIKE '%' || $6 || '%' OR j.number::text = $6)
			AND ($7 = '' OR j.ticket_id = nullif($7, '')::uuid)
			ORDER BY j.scheduled_start NULLS FIRST, j.number DESC LIMIT 1000`, r.PathValue("cid"), st, tech, from, to, text, ticket)
	})
}

func loadJob(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	j, err := queryJSONOne(ctx, tx, `SELECT `+jobCols+jobFrom+` WHERE j.id = $1 AND j.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	parts, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.item_id, it.code AS item_code, it.name AS item_name, it.unit, l.warehouse_id, l.description,
		trim_scale(l.quantity)::text AS quantity, l.unit_price::text AS unit_price, (l.quantity * l.unit_price)::text AS amount
		FROM service_job_parts l JOIN items it ON it.id = l.item_id WHERE l.job_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"job": j, "parts": parts}, nil
}

// lockJob loads a job for a change: the assigned technician may work on it, otherwise the given permission is needed.
func lockJob(ctx context.Context, tx pgx.Tx, r *http.Request, cid, id, perm string) (status string, mine bool, invoiced bool, err error) {
	if !isUUID(id) {
		return "", false, false, notFound()
	}
	err = tx.QueryRow(ctx, `SELECT j.status, coalesce(j.technician_id = app_user(), false), (j.invoice_id IS NOT NULL AND coalesce(i.status, '') <> 'void')
		FROM service_jobs j LEFT JOIN invoices i ON i.id = j.invoice_id WHERE j.id = $1 AND j.company_id = $2 FOR UPDATE OF j`, id, cid).Scan(&status, &mine, &invoiced)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, notFound()
	}
	if err != nil {
		return "", false, false, err
	}
	if !mine && (perm == "" || !can(r, perm)) {
		return "", false, false, &apiError{status: http.StatusForbidden, code: "forbidden"}
	}
	return status, mine, invoiced, nil
}

// GET /fs/service-jobs/{id}: the technician of the job, or anyone with service.view.
func (s *Server) fsGetJob(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var mine bool
		err := tx.QueryRow(ctx, `SELECT coalesce(technician_id = app_user(), false) FROM service_jobs WHERE id = $1 AND company_id = $2`, id, cid).Scan(&mine)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if !mine && !can(r, "service.view") {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		return loadJob(ctx, tx, cid, id)
	})
}

type jobIn struct {
	CustomerID     *string `json:"customer_id"`
	TicketID       *string `json:"ticket_id"`
	ProjectID      *string `json:"project_id"`
	Title          *string `json:"title"`
	Description    *string `json:"description"`
	Address        *string `json:"address"`
	Phone          *string `json:"phone"`
	TechnicianID   *string `json:"technician_id"`
	ScheduledStart *string `json:"scheduled_start"`
	DurationMin    *int    `json:"duration_min"`
	Priority       *string `json:"priority"`
	WarehouseID    *string `json:"warehouse_id"`
	Currency       *string `json:"currency"`
	LabourRate     *string `json:"labour_rate"`
}

func validTimestamp(s string) bool {
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	_, err := time.Parse("2006-01-02T15:04", s)
	return err == nil
}

func (in *jobIn) valid() bool {
	if in.ScheduledStart != nil && *in.ScheduledStart != "" && !validTimestamp(*in.ScheduledStart) {
		return false
	}
	if in.LabourRate != nil && *in.LabourRate != "" {
		if d, ok := money(*in.LabourRate); !ok || d.IsNegative() {
			return false
		}
	}
	return optUUID(in.CustomerID) && optUUID(in.TicketID) && optUUID(in.ProjectID) && optUUID(in.TechnicianID) && optUUID(in.WarehouseID) &&
		(in.Priority == nil || contains(taskPriorities, *in.Priority)) && (in.Currency == nil || currencyRe.MatchString(*in.Currency)) &&
		(in.DurationMin == nil || (*in.DurationMin >= 5 && *in.DurationMin <= 10080)) && len(deref(in.Title)) <= 300 && len(deref(in.Description)) <= 8000 &&
		len(deref(in.Address)) <= 500 && len(deref(in.Phone)) <= 40
}

func pgTimestamp(s string) *string {
	if s == "" {
		return nil
	}
	if t, err := time.Parse("2006-01-02T15:04", s); err == nil {
		x := t.Format("2006-01-02T15:04:00Z07:00")
		return &x
	}
	return &s
}

// POST /fs/service-jobs: a job for a customer, or from a ticket (customer, title and phone come from it).
func (s *Server) fsCreateJob(w http.ResponseWriter, r *http.Request) {
	var in jobIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid() || (deref(in.CustomerID) == "" && deref(in.TicketID) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		customer, title, desc, phone := deref(in.CustomerID), strings.TrimSpace(deref(in.Title)), deref(in.Description), deref(in.Phone)
		if t := deref(in.TicketID); t != "" {
			var tc *string
			var subj, tdesc, tphone string
			err := tx.QueryRow(ctx, `SELECT customer_id::text, subject, description, contact_phone FROM tickets WHERE id = $1 AND company_id = $2`, t, cid).
				Scan(&tc, &subj, &tdesc, &tphone)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, badRequest("invalid_ticket")
			}
			if err != nil {
				return nil, err
			}
			if customer == "" && tc != nil {
				customer = *tc
			}
			if title == "" {
				title = subj
			}
			if desc == "" {
				desc = tdesc
			}
			if phone == "" {
				phone = tphone
			}
		}
		if customer == "" {
			return nil, badRequest("job_needs_customer")
		}
		if title == "" {
			return nil, badRequest("bad_request")
		}
		if _, err := lockCustomer(ctx, tx, cid, customer); err != nil {
			return nil, err
		}
		if p := deref(in.ProjectID); p != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND company_id = $2)`, p, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_project")
			}
		}
		if t := deref(in.TechnicianID); t != "" {
			if err := checkCompanyUser(ctx, tx, cid, t); err != nil {
				return nil, err
			}
		}
		wh := deref(in.WarehouseID)
		if wh != "" {
			if _, err := checkWarehouse(ctx, tx, cid, wh); err != nil {
				return nil, err
			}
		}
		cur := deref(in.Currency)
		var addr, cphone, base string
		if err := tx.QueryRow(ctx, `SELECT coalesce(c.address, ''), coalesce(c.phone, ''), co.base_currency::text FROM customers c JOIN companies co ON co.id = c.company_id
			WHERE c.id = $1`, customer).Scan(&addr, &cphone, &base); err != nil {
			return nil, err
		}
		if cur == "" {
			cur = base
		}
		if phone == "" {
			phone = cphone
		}
		if a := strings.TrimSpace(deref(in.Address)); a != "" {
			addr = a
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'service_job')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		start := pgTimestamp(deref(in.ScheduledStart))
		status := "new"
		if start != nil && deref(in.TechnicianID) != "" {
			status = "scheduled"
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO service_jobs (company_id, number, customer_id, ticket_id, project_id, title, description, address, phone, technician_id,
			scheduled_start, duration_min, priority, status, warehouse_id, currency, labour_rate)
			VALUES ($1, $2, $3, nullif($4, '')::uuid, nullif($5, '')::uuid, $6, $7, $8, $9, nullif($10, '')::uuid, $11::timestamptz, coalesce($12, 60),
			coalesce($13, 'normal'), $14, nullif($15, '')::uuid, $16, coalesce($17::numeric, 0)) RETURNING id::text`,
			cid, n, customer, deref(in.TicketID), deref(in.ProjectID), title, desc, addr, phone, deref(in.TechnicianID), start, in.DurationMin, in.Priority,
			status, wh, cur, zeroIfBlank(in.LabourRate)).Scan(&id); err != nil {
			return nil, err
		}
		if t := deref(in.TicketID); t != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO ticket_notes (company_id, ticket_id, body) VALUES ($1, $2, $3)`, cid, t, fmt.Sprintf("service job #%d", n)); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE tickets SET status = CASE WHEN status = 'new' THEN 'in_progress' ELSE status END,
				first_response_at = coalesce(first_response_at, now()), updated_at = now() WHERE id = $1`, t); err != nil {
				return nil, err
			}
		}
		if t := deref(in.TechnicianID); t != "" {
			if err := notifyUsers(ctx, tx, cid, []string{t}, "service", fmt.Sprintf("#%d %s", n, title), principal(r).FullName, "service_job", id); err != nil {
				return nil, err
			}
		}
		return loadJob(ctx, tx, cid, id)
	})
}

// PATCH /fs/service-jobs/{id}: plan / assign / details (dispatch board drag & drop sends technician + start).
func (s *Server) fsUpdateJob(w http.ResponseWriter, r *http.Request) {
	var in jobIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !in.valid() || in.CustomerID != nil || in.TicketID != nil || (in.Title != nil && strings.TrimSpace(*in.Title) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		status, _, invoiced, err := lockJob(ctx, tx, r, cid, id, "service.edit")
		if err != nil {
			return nil, err
		}
		if !can(r, "service.edit") || invoiced || status == "cancelled" {
			if !can(r, "service.edit") {
				return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
			}
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		var oldTech *string
		var number int64
		var title string
		if err := tx.QueryRow(ctx, `SELECT technician_id::text, number, title FROM service_jobs WHERE id = $1`, id).Scan(&oldTech, &number, &title); err != nil {
			return nil, err
		}
		if t := deref(in.TechnicianID); t != "" {
			if err := checkCompanyUser(ctx, tx, cid, t); err != nil {
				return nil, err
			}
		}
		if wh := deref(in.WarehouseID); wh != "" {
			if _, err := checkWarehouse(ctx, tx, cid, wh); err != nil {
				return nil, err
			}
		}
		if p := deref(in.ProjectID); p != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND company_id = $2)`, p, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_project")
			}
		}
		var start *string
		clearStart := in.ScheduledStart != nil && *in.ScheduledStart == ""
		if in.ScheduledStart != nil {
			start = pgTimestamp(*in.ScheduledStart)
		}
		if _, err := tx.Exec(ctx, `UPDATE service_jobs SET title = coalesce(trim($3), title), description = coalesce($4, description),
			address = coalesce($5, address), phone = coalesce($6, phone),
			technician_id = CASE WHEN $7::text IS NULL THEN technician_id ELSE nullif($7, '')::uuid END,
			scheduled_start = CASE WHEN $15 THEN NULL WHEN $8::text IS NULL THEN scheduled_start ELSE $8::timestamptz END,
			duration_min = coalesce($9, duration_min), priority = coalesce($10, priority),
			warehouse_id = CASE WHEN $11::text IS NULL THEN warehouse_id ELSE nullif($11, '')::uuid END,
			currency = coalesce($12, currency), labour_rate = coalesce($13::numeric, labour_rate),
			project_id = CASE WHEN $14::text IS NULL THEN project_id ELSE nullif($14, '')::uuid END, updated_at = now()
			WHERE id = $1 AND company_id = $2`, id, cid, in.Title, in.Description, in.Address, in.Phone, in.TechnicianID, start, in.DurationMin,
			in.Priority, in.WarehouseID, in.Currency, zeroIfBlank(in.LabourRate), in.ProjectID, clearStart); err != nil {
			return nil, err
		}
		// new ⇄ scheduled follows the plan
		if _, err := tx.Exec(ctx, `UPDATE service_jobs SET status = CASE WHEN technician_id IS NOT NULL AND scheduled_start IS NOT NULL THEN 'scheduled' ELSE 'new' END
			WHERE id = $1 AND status IN ('new', 'scheduled')`, id); err != nil {
			return nil, err
		}
		if t := deref(in.TechnicianID); t != "" && (oldTech == nil || *oldTech != t) {
			if err := notifyUsers(ctx, tx, cid, []string{t}, "service", fmt.Sprintf("#%d %s", number, title), principal(r).FullName, "service_job", id); err != nil {
				return nil, err
			}
		}
		return loadJob(ctx, tx, cid, id)
	})
}

// POST /fs/service-jobs/{id}/{action}: start | finish {report, signature, signed_by, lat, lng, labour_hours} (the
// technician or service.edit), cancel | reopen (service.edit).
func (s *Server) fsJobAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Report      *string  `json:"report"`
		Signature   string   `json:"signature"`
		SignedBy    string   `json:"signed_by"`
		Lat         *float64 `json:"lat"`
		Lng         *float64 `json:"lng"`
		LabourHours *string  `json:"labour_hours"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	action := r.PathValue("action")
	if !contains([]string{"start", "finish", "cancel", "reopen"}, action) || len(deref(in.Report)) > 8000 || len(in.Signature) > 400000 || len(in.SignedBy) > 120 ||
		(in.Signature != "" && !strings.HasPrefix(in.Signature, "data:image/png;base64,")) ||
		(in.Lat != nil && (*in.Lat < -90 || *in.Lat > 90)) || (in.Lng != nil && (*in.Lng < -180 || *in.Lng > 180)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var hours *string
	if in.LabourHours != nil && *in.LabourHours != "" {
		h, ok := money(*in.LabourHours)
		if !ok || h.GreaterThan(decimal.NewFromInt(200)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		x := h.Round(2).String()
		hours = &x
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		perm := "service.edit"
		status, mine, invoiced, err := lockJob(ctx, tx, r, cid, id, perm)
		if err != nil {
			return nil, err
		}
		if (action == "cancel" || action == "reopen") && !can(r, perm) {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		_ = mine
		conflict := &apiError{status: http.StatusConflict, code: "wrong_status"}
		switch action {
		case "start":
			if status != "new" && status != "scheduled" {
				return nil, conflict
			}
			_, err = tx.Exec(ctx, `UPDATE service_jobs SET status = 'in_progress', started_at = coalesce(started_at, now()),
				technician_id = coalesce(technician_id, app_user()), lat = coalesce($2, lat), lng = coalesce($3, lng), updated_at = now() WHERE id = $1`, id, in.Lat, in.Lng)
		case "finish":
			if status != "in_progress" && status != "scheduled" && status != "new" {
				return nil, conflict
			}
			_, err = tx.Exec(ctx, `UPDATE service_jobs SET status = 'done', started_at = coalesce(started_at, now()), finished_at = now(),
				report = coalesce($2, report), signature = CASE WHEN $3 <> '' THEN $3 ELSE signature END, signed_by = CASE WHEN $4 <> '' THEN trim($4) ELSE signed_by END,
				lat = coalesce($5, lat), lng = coalesce($6, lng),
				labour_hours = coalesce($7::numeric, labour_hours, greatest(round((extract(epoch FROM now() - coalesce(started_at, now())) / 3600)::numeric, 2), 0)),
				updated_at = now() WHERE id = $1`, id, in.Report, in.Signature, in.SignedBy, in.Lat, in.Lng, hours)
			if err == nil {
				err = jobTicketNote(ctx, tx, cid, id, "done")
			}
		case "cancel":
			if status == "done" || status == "cancelled" || invoiced {
				return nil, conflict
			}
			_, err = tx.Exec(ctx, `UPDATE service_jobs SET status = 'cancelled', updated_at = now() WHERE id = $1`, id)
		case "reopen":
			if (status != "done" && status != "cancelled") || invoiced {
				return nil, conflict
			}
			_, err = tx.Exec(ctx, `UPDATE service_jobs SET status = CASE WHEN started_at IS NOT NULL THEN 'in_progress'
				WHEN technician_id IS NOT NULL AND scheduled_start IS NOT NULL THEN 'scheduled' ELSE 'new' END, finished_at = NULL, updated_at = now() WHERE id = $1`, id)
		}
		if err != nil {
			return nil, err
		}
		return loadJob(ctx, tx, cid, id)
	})
}

// jobTicketNote writes the job's outcome on its ticket.
func jobTicketNote(ctx context.Context, tx pgx.Tx, cid, id, what string) error {
	var ticket *string
	var n int64
	var report string
	if err := tx.QueryRow(ctx, `SELECT ticket_id::text, number, report FROM service_jobs WHERE id = $1`, id).Scan(&ticket, &n, &report); err != nil {
		return err
	}
	if ticket == nil {
		return nil
	}
	body := fmt.Sprintf("service job #%d: %s", n, what)
	if report != "" {
		body += " — " + report
	}
	if len(body) > 7000 {
		body = body[:7000]
	}
	_, err := tx.Exec(ctx, `INSERT INTO ticket_notes (company_id, ticket_id, body) VALUES ($1, $2, $3)`, cid, *ticket, body)
	return err
}

// PUT /fs/service-jobs/{id}/parts {parts: [{item_id, warehouse_id, quantity, unit_price, description}]} (until invoiced)
func (s *Server) fsPutParts(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Parts []struct {
			ItemID      string `json:"item_id"`
			WarehouseID string `json:"warehouse_id"`
			Quantity    string `json:"quantity"`
			UnitPrice   string `json:"unit_price"`
			Description string `json:"description"`
		} `json:"parts"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if len(in.Parts) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		status, _, invoiced, err := lockJob(ctx, tx, r, cid, id, "service.edit")
		if err != nil {
			return nil, err
		}
		if invoiced || status == "cancelled" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		var jobWh *string
		if err := tx.QueryRow(ctx, `SELECT warehouse_id::text FROM service_jobs WHERE id = $1`, id).Scan(&jobWh); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM service_job_parts WHERE job_id = $1`, id); err != nil {
			return nil, err
		}
		for i, p := range in.Parts {
			if !isUUID(p.ItemID) || len(p.Description) > 300 || (p.WarehouseID != "" && !isUUID(p.WarehouseID)) {
				return nil, badRequest("invalid_line")
			}
			q, ok := money(p.Quantity)
			if !ok || !q.IsPositive() {
				return nil, badRequest("invalid_line")
			}
			it, err := loadItem(ctx, tx, cid, p.ItemID)
			if err != nil {
				return nil, err
			}
			price := decimal.Zero
			if strings.TrimSpace(p.UnitPrice) != "" {
				pv, ok := money(p.UnitPrice)
				if !ok {
					return nil, badRequest("invalid_line")
				}
				price = pv
			} else {
				_ = tx.QueryRow(ctx, `SELECT coalesce(sale_price, 0)::text FROM items WHERE id = $1`, it.ID).Scan(&p.UnitPrice)
				price, _ = decimal.NewFromString(p.UnitPrice)
			}
			var wh *string
			if it.Kind == "stock" || it.Kind == "kit" {
				want := p.WarehouseID
				if want == "" && jobWh != nil {
					want = *jobWh
				}
				wv, err := checkWarehouse(ctx, tx, cid, want)
				if err != nil {
					return nil, err
				}
				wh = &wv
			}
			desc := strings.TrimSpace(p.Description)
			if desc == "" {
				desc = it.Name
			}
			if _, err := tx.Exec(ctx, `INSERT INTO service_job_parts (company_id, job_id, line_no, item_id, warehouse_id, description, quantity, unit_price)
				VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric)`, cid, id, i+1, it.ID, wh, desc, q.String(), price.String()); err != nil {
				return nil, err
			}
		}
		return loadJob(ctx, tx, cid, id)
	})
}

// POST /fs/service-jobs/{id}/invoice {date}: labour (hours × rate) and parts on one invoice; stock parts leave stock
// with it. The job must be done. Linked to the job's project when it has one.
func (s *Server) fsInvoiceJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date string `json:"date"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !validDate(in.Date) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		status, _, invoiced, err := lockJob(ctx, tx, r, cid, id, "service.manage")
		if err != nil {
			return nil, err
		}
		if status != "done" || invoiced {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		var customer, currency, title, rate string
		var hours *string
		var number int64
		var project *string
		if err := tx.QueryRow(ctx, `SELECT customer_id::text, currency::text, title, labour_rate::text, labour_hours::text, number, project_id::text FROM service_jobs WHERE id = $1`, id).
			Scan(&customer, &currency, &title, &rate, &hours, &number, &project); err != nil {
			return nil, err
		}
		label := fmt.Sprintf("Service job #%d · %s", number, title)
		bill := invoiceIn{CustomerID: customer, IssueDate: in.Date, Currency: currency, Notes: label}
		rd, _ := decimal.NewFromString(rate)
		if hours != nil && rd.IsPositive() {
			h, _ := decimal.NewFromString(*hours)
			if h.IsPositive() {
				bill.Lines = append(bill.Lines, invoiceLineIn{Description: label + " — labour (h)", Quantity: h.String(), UnitPrice: rd.String()})
			}
		}
		rows, err := tx.Query(ctx, `SELECT item_id::text, coalesce(warehouse_id::text, ''), description, quantity::text, unit_price::text FROM service_job_parts
			WHERE job_id = $1 ORDER BY line_no`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var l invoiceLineIn
			if err := rows.Scan(&l.ItemID, &l.WarehouseID, &l.Description, &l.Quantity, &l.UnitPrice); err != nil {
				rows.Close()
				return nil, err
			}
			bill.Lines = append(bill.Lines, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(bill.Lines) == 0 {
			return nil, badRequest("nothing_to_invoice")
		}
		inv, err := createInvoiceTx(ctx, tx, r, cid, &bill)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE service_jobs SET invoice_id = $2, updated_at = now() WHERE id = $1`, id, inv); err != nil {
			return nil, err
		}
		if project != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO project_links (company_id, project_id, doc_type, doc_id) VALUES ($1, $2, 'invoice', $3) ON CONFLICT DO NOTHING`,
				cid, *project, inv); err != nil {
				return nil, err
			}
		}
		return loadJob(ctx, tx, cid, id)
	})
}

// GET /fs/service-jobs/{id}/signature
func (s *Server) fsJobSignature(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT id, signed_by, signature, finished_at FROM service_jobs WHERE id = $1 AND company_id = $2`, id, cid)
	})
}

// POST /fs/service-jobs/{id}/photo {filename, data}: a photo from the site (the technician or service.create).
func (s *Server) fsJobPhoto(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Filename string `json:"filename"`
		Data     string `json:"data"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	mime := ""
	if i := strings.Index(in.Data, ";base64,"); strings.HasPrefix(in.Data, "data:") && i > 0 {
		mime = in.Data[5:i]
		in.Data = in.Data[i+8:]
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	name := strings.TrimSpace(in.Filename)
	if name == "" {
		name = "photo.jpg"
	}
	if err != nil || len(data) == 0 || len(data) > 10<<20 || len(name) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mime, "image/") {
		writeErr(w, http.StatusBadRequest, "not_an_image")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, _, _, err := lockJob(ctx, tx, r, cid, id, "service.create"); err != nil {
			return nil, err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM attachments WHERE company_id = $1 AND entity_type = 'service_job' AND entity_id = $2 AND deleted_at IS NULL`,
			cid, id).Scan(&n); err != nil {
			return nil, err
		}
		if n >= 20 {
			return nil, badRequest("too_many_files")
		}
		if err := checkStorageQuota(ctx, tx, cid, len(data)); err != nil {
			return nil, err
		}
		var aid string
		if err := tx.QueryRow(ctx, `INSERT INTO attachments (company_id, entity_type, entity_id, filename, mime, size, data, note)
			VALUES ($1, 'service_job', $2, $3, $4, $5, $6, 'site photo') RETURNING id::text`, cid, id, name, mime, len(data), data).Scan(&aid); err != nil {
			return nil, err
		}
		return map[string]string{"id": aid}, nil
	})
}

// GET /fs/my-jobs: the signed-in technician's open jobs and those finished today.
func (s *Server) fsMyJobs(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+jobCols+`, c.phone AS customer_phone`+jobFrom+` WHERE j.company_id = $1 AND j.technician_id = app_user()
			AND (j.status IN ('new', 'scheduled', 'in_progress') OR (j.status = 'done' AND j.finished_at > current_date))
			ORDER BY j.status = 'done', j.scheduled_start NULLS LAST, j.number LIMIT 200`, r.PathValue("cid"))
	})
}

// GET /fs/parts-catalog?q=&warehouse_id=: stock items with what is on hand (the technician's part picker).
func (s *Server) fsPartsCatalog(w http.ResponseWriter, r *http.Request) {
	q, wh := strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("warehouse_id")
	if len(q) > 100 || (wh != "" && !isUUID(wh)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT it.id, it.code, it.name, it.unit, it.kind, coalesce(it.sale_price, 0)::text AS sale_price, it.barcode,
			trim_scale(coalesce((SELECT sum(s.quantity) FROM stock s WHERE s.item_id = it.id AND ($3 = '' OR s.warehouse_id = nullif($3, '')::uuid)), 0))::text AS on_hand
			FROM items it WHERE it.company_id = $1 AND it.is_active AND it.kind IN ('stock', 'service', 'consumable')
			AND ($2 = '' OR it.name ILIKE '%' || $2 || '%' OR it.code ILIKE $2 || '%' OR it.barcode = $2)
			ORDER BY it.name LIMIT 300`, r.PathValue("cid"), q, wh)
	})
}

// GET /fs/board?from=&to=: the dispatch board (technicians and their jobs in the range, plus unplanned jobs).
func (s *Server) fsBoard(w http.ResponseWriter, r *http.Request) {
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" {
		from = today()
	}
	if to == "" {
		t, _ := time.Parse(time.DateOnly, from)
		to = t.AddDate(0, 0, 6).Format(time.DateOnly)
	}
	if !validDate(from) || !validDate(to) || to < from {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		techs, err := queryJSON(ctx, tx, `SELECT u.id, u.full_name, u.role,
			(SELECT count(*) FROM service_jobs j WHERE j.technician_id = u.id AND j.company_id = $1 AND j.status IN ('new', 'scheduled', 'in_progress')) AS open_jobs
			FROM users u WHERE u.is_active AND (u.company_ids IS NULL OR $1::uuid = ANY (u.company_ids)) ORDER BY u.full_name LIMIT 300`, cid)
		if err != nil {
			return nil, err
		}
		jobs, err := queryJSON(ctx, tx, `SELECT `+jobCols+jobFrom+` WHERE j.company_id = $1 AND j.status <> 'cancelled'
			AND j.scheduled_start >= $2::date AND j.scheduled_start < $3::date + 1 ORDER BY j.scheduled_start`, cid, from, to)
		if err != nil {
			return nil, err
		}
		unplanned, err := queryJSON(ctx, tx, `SELECT `+jobCols+jobFrom+` WHERE j.company_id = $1 AND j.status = 'new' AND (j.scheduled_start IS NULL OR j.technician_id IS NULL)
			ORDER BY CASE j.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, j.number LIMIT 300`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"from": from, "to": to, "technicians": techs, "jobs": jobs, "unplanned": unplanned}, nil
	})
}

// ======================= SLA =======================

const slaCols = `p.id, p.name, p.priority, p.category, trim_scale(p.response_hours)::text AS response_hours,
	trim_scale(p.resolution_hours)::text AS resolution_hours, p.escalate_to, u.full_name AS escalate_name, p.is_active,
	(SELECT count(*) FROM tickets t WHERE t.sla_policy_id = p.id) AS tickets`

func (s *Server) slaPolicies(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+slaCols+` FROM sla_policies p LEFT JOIN users u ON u.id = p.escalate_to WHERE p.company_id = $1
			ORDER BY p.is_active DESC, p.priority NULLS LAST, p.name`, r.PathValue("cid"))
	})
}

// POST /sla/sla-policies and PATCH /sla/sla-policies/{id}
func (s *Server) slaSavePolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name            *string `json:"name"`
		Priority        *string `json:"priority"`
		Category        *string `json:"category"`
		ResponseHours   *string `json:"response_hours"`
		ResolutionHours *string `json:"resolution_hours"`
		EscalateTo      *string `json:"escalate_to"`
		IsActive        *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	okHours := func(p *string) bool {
		if p == nil {
			return true
		}
		d, ok := money(*p)
		return ok && d.IsPositive() && d.LessThanOrEqual(decimal.NewFromInt(100000))
	}
	if (creating && (strings.TrimSpace(deref(in.Name)) == "" || in.ResponseHours == nil || in.ResolutionHours == nil)) || (!creating && !isUUID(id)) ||
		(in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 120)) || !okHours(in.ResponseHours) || !okHours(in.ResolutionHours) ||
		(in.Priority != nil && *in.Priority != "" && !contains(ticketPriorities, *in.Priority)) ||
		(in.Category != nil && *in.Category != "" && !contains(ticketCategories, *in.Category)) || !optUUID(in.EscalateTo) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if e := deref(in.EscalateTo); e != "" {
			if err := checkCompanyUser(ctx, tx, cid, e); err != nil {
				return nil, err
			}
		}
		if creating {
			if err := tx.QueryRow(ctx, `INSERT INTO sla_policies (company_id, name, priority, category, response_hours, resolution_hours, escalate_to)
				VALUES ($1, trim($2), nullif($3, ''), nullif($4, ''), $5::numeric, $6::numeric, nullif($7, '')::uuid) RETURNING id::text`,
				cid, *in.Name, deref(in.Priority), deref(in.Category), *in.ResponseHours, *in.ResolutionHours, deref(in.EscalateTo)).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE sla_policies SET name = coalesce(trim($3), name),
				priority = CASE WHEN $4::text IS NULL THEN priority ELSE nullif($4, '') END,
				category = CASE WHEN $5::text IS NULL THEN category ELSE nullif($5, '') END,
				response_hours = coalesce($6::numeric, response_hours), resolution_hours = coalesce($7::numeric, resolution_hours),
				escalate_to = CASE WHEN $8::text IS NULL THEN escalate_to ELSE nullif($8, '')::uuid END, is_active = coalesce($9, is_active)
				WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.Priority, in.Category, in.ResponseHours, in.ResolutionHours, in.EscalateTo, in.IsActive)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+slaCols+` FROM sla_policies p LEFT JOIN users u ON u.id = p.escalate_to WHERE p.id = $1`, id)
	})
}

// applySLA picks the most specific active policy (priority + category, then priority, then category, then the
// catch-all) and sets the ticket's deadlines from its creation time. Resolved tickets keep theirs.
func applySLA(ctx context.Context, tx pgx.Tx, cid, ticket string) error {
	_, err := tx.Exec(ctx, `UPDATE tickets t SET sla_policy_id = p.id,
			response_due = t.created_at + make_interval(secs => (p.response_hours * 3600)::double precision),
			resolution_due = t.created_at + make_interval(secs => (p.resolution_hours * 3600)::double precision)
		FROM (SELECT t2.id AS tid, (SELECT sp.id FROM sla_policies sp WHERE sp.company_id = t2.company_id AND sp.is_active
				AND (sp.priority IS NULL OR sp.priority = t2.priority) AND (sp.category IS NULL OR sp.category = t2.category)
				ORDER BY (sp.priority IS NOT NULL)::int + (sp.category IS NOT NULL)::int DESC, (sp.priority IS NOT NULL) DESC, sp.resolution_hours LIMIT 1) AS pid
			FROM tickets t2 WHERE t2.id = $1 AND t2.company_id = $2 AND t2.resolved_at IS NULL) x
		LEFT JOIN sla_policies p ON p.id = x.pid
		WHERE t.id = x.tid AND p.id IS NOT NULL`, ticket, cid)
	if err != nil {
		return err
	}
	// no policy matches any more: no deadlines
	_, err = tx.Exec(ctx, `UPDATE tickets t SET sla_policy_id = NULL, response_due = NULL, resolution_due = NULL WHERE t.id = $1 AND t.resolved_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM sla_policies sp WHERE sp.company_id = t.company_id AND sp.is_active
			AND (sp.priority IS NULL OR sp.priority = t.priority) AND (sp.category IS NULL OR sp.category = t.category))`, ticket)
	return err
}

// ticketClosed gives a closed ticket its rating link and sends it to the customer (WhatsApp, when messaging is on).
func ticketClosed(ctx context.Context, tx pgx.Tx, cid, ticket string) error {
	var token *string
	var status string
	var customer *string
	var number int64
	var msgApp bool
	var lang string
	if err := tx.QueryRow(ctx, `SELECT t.rating_token, t.status, t.customer_id::text, t.number, 'msg' = ANY (co.apps),
		coalesce(c.locale, co.default_locale, 'en') FROM tickets t JOIN companies co ON co.id = t.company_id LEFT JOIN customers c ON c.id = t.customer_id
		WHERE t.id = $1`, ticket).Scan(&token, &status, &customer, &number, &msgApp, &lang); err != nil {
		return err
	}
	if status != "done" || token != nil {
		return nil
	}
	tok := randToken(24)
	if _, err := tx.Exec(ctx, `UPDATE tickets SET rating_token = $2 WHERE id = $1`, ticket, tok); err != nil {
		return err
	}
	if customer == nil || !msgApp {
		return nil
	}
	link := ratingLink(tok)
	texts := map[string]string{
		"en": "Your request #%d is solved. How did we do? Rate us: %s",
		"ar": "تمّت معالجة طلبك رقم %d. ما رأيك بخدمتنا؟ قيّمنا: %s",
		"fr": "Votre demande n° %d est résolue. Votre avis compte : %s",
	}
	t, ok := texts[lang]
	if !ok {
		t = texts["en"]
	}
	rd, err := newRenderer(ctx, tx, cid, lang)
	if err != nil {
		return err
	}
	m, err := rd.customerContext(ctx, tx, *customer, nil, nil)
	if err != nil {
		return nil // no reachable customer: the link is still shown on the ticket
	}
	prov, err := pickProvider(ctx, tx, cid, "")
	if err != nil || prov == nil {
		return nil // no provider: the link is still shown on the ticket
	}
	_, _, _, err = queueMessage(ctx, tx, rd, m, "custom", nil, fmt.Sprintf(t, number, link), prov, "whatsapp")
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			return nil // customer without a phone etc.: not a reason to refuse closing the ticket
		}
	}
	return err
}

func ratingLink(token string) string { return publicURL() + "/#/rate/" + token }

// GET /tickets/sla-report?from=&to=: SLA results per policy and per agent, ratings, and the open tickets in breach.
func (s *Server) slaReport(w http.ResponseWriter, r *http.Request) {
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" {
		from = time.Now().AddDate(0, 0, -30).Format(time.DateOnly)
	}
	if to == "" {
		to = today()
	}
	if !validDate(from) || !validDate(to) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		const agg = `count(*) AS tickets,
			count(*) FILTER (WHERE t.response_due IS NOT NULL AND coalesce(t.first_response_at, now()) <= t.response_due) AS response_met,
			count(*) FILTER (WHERE t.response_due IS NOT NULL AND coalesce(t.first_response_at, now()) > t.response_due) AS response_breached,
			count(*) FILTER (WHERE t.resolution_due IS NOT NULL AND t.resolved_at IS NOT NULL AND t.resolved_at <= t.resolution_due) AS resolution_met,
			count(*) FILTER (WHERE t.resolution_due IS NOT NULL AND coalesce(t.resolved_at, now()) > t.resolution_due) AS resolution_breached,
			round(avg(extract(epoch FROM (t.first_response_at - t.created_at)) / 3600)::numeric, 1)::text AS avg_response_hours,
			round(avg(extract(epoch FROM (t.resolved_at - t.created_at)) / 3600)::numeric, 1)::text AS avg_resolution_hours,
			round(avg(t.rating)::numeric, 2)::text AS avg_rating, count(t.rating) AS ratings`
		byPolicy, err := queryJSON(ctx, tx, `SELECT t.sla_policy_id AS id, coalesce(p.name, '') AS name, `+agg+` FROM tickets t LEFT JOIN sla_policies p ON p.id = t.sla_policy_id
			WHERE t.company_id = $1 AND t.created_at >= $2::date AND t.created_at < $3::date + 1 GROUP BY 1, 2 ORDER BY 2`, cid, from, to)
		if err != nil {
			return nil, err
		}
		byAgent, err := queryJSON(ctx, tx, `SELECT t.assigned_to AS id, coalesce(u.full_name, '') AS name, `+agg+` FROM tickets t LEFT JOIN users u ON u.id = t.assigned_to
			WHERE t.company_id = $1 AND t.created_at >= $2::date AND t.created_at < $3::date + 1 GROUP BY 1, 2 ORDER BY 2`, cid, from, to)
		if err != nil {
			return nil, err
		}
		total, err := queryJSONOne(ctx, tx, `SELECT `+agg+` FROM tickets t WHERE t.company_id = $1 AND t.created_at >= $2::date AND t.created_at < $3::date + 1`, cid, from, to)
		if err != nil {
			return nil, err
		}
		breached, err := queryJSON(ctx, tx, `SELECT `+ticketCols+ticketFrom+` WHERE t.company_id = $1 AND t.status NOT IN ('done', 'cancelled')
			AND ((t.first_response_at IS NULL AND t.response_due < now()) OR t.resolution_due < now()) ORDER BY t.resolution_due LIMIT 300`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"from": from, "to": to, "total": total, "by_policy": byPolicy, "by_agent": byAgent, "breached_open": breached}, nil
	})
}

// RunSLAEscalations: open tickets past a deadline whose policy names a manager are escalated once (in-app
// notification + note on the ticket).
func (s *Server) RunSLAEscalations(ctx context.Context) error {
	if s.OwnerPool == nil {
		return nil
	}
	rows, err := s.OwnerPool.Query(ctx, `SELECT DISTINCT t.tenant_id::text FROM tickets t JOIN sla_policies p ON p.id = t.sla_policy_id
		WHERE t.escalated_at IS NULL AND t.status NOT IN ('done', 'cancelled') AND p.escalate_to IS NOT NULL
		AND ((t.first_response_at IS NULL AND t.response_due < now()) OR t.resolution_due < now()) AND tenant_jobs_ok(t.tenant_id)`)
	if err != nil {
		return err
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, tid := range tenants {
		err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
				return err
			}
			rs, err := tx.Query(ctx, `UPDATE tickets t SET escalated_at = now() FROM sla_policies p
				WHERE p.id = t.sla_policy_id AND t.escalated_at IS NULL AND t.status NOT IN ('done', 'cancelled') AND p.escalate_to IS NOT NULL
				AND ((t.first_response_at IS NULL AND t.response_due < now()) OR t.resolution_due < now())
				RETURNING t.company_id::text, t.id::text, t.number, t.subject, p.escalate_to::text`)
			if err != nil {
				return err
			}
			type esc struct {
				cid, id, subject, to string
				n                    int64
			}
			var list []esc
			for rs.Next() {
				var e esc
				if err := rs.Scan(&e.cid, &e.id, &e.n, &e.subject, &e.to); err != nil {
					rs.Close()
					return err
				}
				list = append(list, e)
			}
			rs.Close()
			for _, e := range list {
				if _, err := tx.Exec(ctx, `INSERT INTO ticket_notes (company_id, ticket_id, body) VALUES ($1, $2, 'SLA breached: escalated')`, e.cid, e.id); err != nil {
					return err
				}
				if err := notifyUsers(ctx, tx, e.cid, []string{e.to}, "sla", fmt.Sprintf("SLA · Ticket #%d · %s", e.n, e.subject), "SLA breached", "ticket", e.id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// ======================= inbound channels =======================

// GET /sla/inbound: the addresses an e-mail forwarding service and the WhatsApp Cloud API webhook post to.
func (s *Server) slaInbound(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var tok *string
		if err := tx.QueryRow(ctx, `SELECT inbound_token FROM companies WHERE id = $1`, r.PathValue("cid")).Scan(&tok); err != nil {
			return nil, err
		}
		return inboundInfo(tok), nil
	})
}

func inboundInfo(tok *string) map[string]any {
	if tok == nil {
		return map[string]any{"enabled": false}
	}
	base := publicURL() + v1 + "/inbound/" + *tok
	return map[string]any{"enabled": true, "email_url": base + "/email", "whatsapp_url": base + "/whatsapp", "verify_token": *tok}
}

// POST /sla/inbound/rotate {enabled}: a new secret address (the old one stops working); enabled:false switches it off.
func (s *Server) slaInboundRotate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var tok *string
		if in.Enabled == nil || *in.Enabled {
			t := randToken(24)
			tok = &t
		}
		if _, err := tx.Exec(ctx, `UPDATE companies SET inbound_token = $2 WHERE id = $1`, r.PathValue("cid"), tok); err != nil {
			return nil, err
		}
		return inboundInfo(tok), nil
	})
}

var inboundLimiter = newLimiter(600, time.Minute)

func (s *Server) inboundScope(w http.ResponseWriter, r *http.Request) (db.Scope, string, string, bool) {
	tok := r.PathValue("token")
	if len(tok) < 32 || len(tok) > 128 || !inboundLimiter.allow(tok) {
		if len(tok) >= 32 && len(tok) <= 128 {
			writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		} else {
			writeErr(w, http.StatusNotFound, "not_found")
		}
		return db.Scope{}, "", "", false
	}
	inboundLimiter.fail(tok)
	var tid, cid string
	err := s.Pool.QueryRow(r.Context(), `SELECT tenant_id::text, company_id::text FROM inbound_lookup($1)`, tok).Scan(&tid, &cid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", "", false
	}
	return db.Scope{TenantID: tid, CompanyID: cid, IP: clientIP(r)}, cid, tok, true
}

var ticketRefRe = regexp.MustCompile(`#(\d{1,9})\b`)

// inboundTicket creates a ticket from a message, or adds it to the sender's open ticket (e-mail: the "#number" in
// the subject; WhatsApp: the sender's open WhatsApp ticket of the last 7 days).
func inboundTicket(ctx context.Context, tx pgx.Tx, cid, channel, fromName, email, phone, subject, body string) (int64, bool, error) {
	email, phone = strings.ToLower(strings.TrimSpace(email)), strings.TrimSpace(phone)
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	var customer *string
	if email != "" {
		_ = tx.QueryRow(ctx, `SELECT id::text FROM customers WHERE company_id = $1 AND lower(email) = $2 AND status <> 'closed' ORDER BY created_at LIMIT 1`, cid, email).Scan(&customer)
	}
	if customer == nil && len(digits) >= 7 {
		tail := digits[len(digits)-7:]
		_ = tx.QueryRow(ctx, `SELECT id::text FROM customers WHERE company_id = $1 AND status <> 'closed'
			AND right(regexp_replace(phone, '[^0-9]', '', 'g'), 7) = $2 ORDER BY created_at LIMIT 1`, cid, tail).Scan(&customer)
	}
	// an existing open ticket?
	var existing *string
	var number int64
	if channel == "email" {
		if m := ticketRefRe.FindStringSubmatch(subject); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			_ = tx.QueryRow(ctx, `SELECT id::text, number FROM tickets WHERE company_id = $1 AND number = $2 AND status NOT IN ('done', 'cancelled')
				AND (contact_email = $3 OR ($4::uuid IS NOT NULL AND customer_id = $4::uuid))`, cid, n, email, customer).Scan(&existing, &number)
		}
	} else if len(digits) >= 7 {
		_ = tx.QueryRow(ctx, `SELECT id::text, number FROM tickets WHERE company_id = $1 AND channel = 'whatsapp' AND status NOT IN ('done', 'cancelled')
			AND right(regexp_replace(contact_phone, '[^0-9]', '', 'g'), 7) = $2 AND created_at > now() - interval '7 days'
			ORDER BY created_at DESC LIMIT 1`, cid, digits[len(digits)-7:]).Scan(&existing, &number)
	}
	if len(body) > 7500 {
		body = body[:7500]
	}
	if existing != nil {
		note := strings.TrimSpace(body)
		if note == "" {
			note = "(" + channel + ")"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_notes (company_id, ticket_id, body, created_by) VALUES ($1, $2, $3, NULL)`, cid, *existing, channel+": "+note); err != nil {
			return 0, false, err
		}
		_, err := tx.Exec(ctx, `UPDATE tickets SET updated_at = now(), status = CASE WHEN status = 'waiting' THEN 'in_progress' ELSE status END WHERE id = $1`, *existing)
		return number, false, err
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		subject = strings.TrimSpace(body)
		if i := strings.IndexAny(subject, "\r\n"); i > 0 {
			subject = subject[:i]
		}
	}
	if r := []rune(subject); len(r) > 120 {
		subject = string(r[:120])
	}
	if subject == "" {
		subject = channel
	}
	desc := strings.TrimSpace(body)
	if fromName != "" {
		desc = fromName + ":\n" + desc
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('tickets:' || $1))`, cid); err != nil {
		return 0, false, err
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO tickets (company_id, number, customer_id, subject, description, contact_phone, contact_email, channel, created_by)
		VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM tickets WHERE company_id = $1), $2::uuid, $3, $4, $5, $6, $7, NULL) RETURNING id::text, number`,
		cid, customer, subject, desc, phone, email, channel).Scan(&id, &number)
	if err != nil {
		return 0, false, err
	}
	return number, true, applySLA(ctx, tx, cid, id)
}

// POST /api/v1/inbound/{token}/email — JSON {from, from_name, subject, text} or a form post from an inbound-mail
// service (Mailgun: sender / from / subject / body-plain; SendGrid: from / subject / text).
func (s *Server) inboundEmail(w http.ResponseWriter, r *http.Request) {
	sc, cid, _, ok := s.inboundScope(w, r)
	if !ok {
		return
	}
	var from, name, subject, text string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var in struct {
			From     string `json:"from"`
			FromName string `json:"from_name"`
			Subject  string `json:"subject"`
			Text     string `json:"text"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&in) != nil {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		from, name, subject, text = in.From, in.FromName, in.Subject, in.Text
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		if err := r.ParseMultipartForm(10 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		if r.Form == nil {
			_ = r.ParseForm()
		}
		from = r.FormValue("sender")
		if from == "" {
			from = r.FormValue("from")
		}
		subject = r.FormValue("subject")
		text = r.FormValue("body-plain")
		if text == "" {
			text = r.FormValue("stripped-text")
		}
		if text == "" {
			text = r.FormValue("text")
		}
	}
	// "Name <mail@x>" → name + address
	if i := strings.LastIndex(from, "<"); i >= 0 && strings.HasSuffix(strings.TrimSpace(from), ">") {
		if name == "" {
			name = strings.Trim(strings.TrimSpace(from[:i]), `"`)
		}
		from = strings.TrimSuffix(strings.TrimSpace(from[i+1:]), ">")
	}
	if !strings.Contains(from, "@") || len(from) > 200 || len(subject) > 1000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var number int64
	var created bool
	err := db.InTenant(r.Context(), s.Pool, sc, func(tx pgx.Tx) error {
		var err error
		number, created, err = inboundTicket(r.Context(), tx, cid, "email", name, from, "", subject, text)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ticket": number, "created": created})
}

// /api/v1/inbound/{token}/whatsapp — GET: the Cloud API verification handshake (hub.verify_token = the token);
// POST: Cloud API message events, or a simple JSON {from, name, text}.
func (s *Server) inboundWhatsApp(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		if q.Get("hub.mode") == "subscribe" && q.Get("hub.verify_token") == r.PathValue("token") {
			if _, _, _, ok := s.inboundScope(w, r); !ok {
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, q.Get("hub.challenge"))
			return
		}
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	sc, cid, _, ok := s.inboundScope(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	type msg struct{ from, name, text string }
	var msgs []msg
	var cloud struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Contacts []struct {
						WaID    string `json:"wa_id"`
						Profile struct {
							Name string `json:"name"`
						} `json:"profile"`
					} `json:"contacts"`
					Messages []struct {
						From string `json:"from"`
						Type string `json:"type"`
						Text struct {
							Body string `json:"body"`
						} `json:"text"`
						Caption string `json:"caption"`
					} `json:"messages"`
					Statuses []struct {
						ID     string `json:"id"`
						Status string `json:"status"`
					} `json:"statuses"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	var simple struct {
		From string `json:"from"`
		Name string `json:"name"`
		Text string `json:"text"`
	}
	var receipts []string
	if json.Unmarshal(body, &cloud) == nil && len(cloud.Entry) > 0 {
		for _, e := range cloud.Entry {
			for _, c := range e.Changes {
				// delivery receipts of messages the company sent (campaign stats)
				for _, st := range c.Value.Statuses {
					if st.Status == "delivered" || st.Status == "read" {
						receipts = append(receipts, st.ID)
					}
				}
				names := map[string]string{}
				for _, ct := range c.Value.Contacts {
					names[ct.WaID] = ct.Profile.Name
				}
				for _, m := range c.Value.Messages {
					text := m.Text.Body
					if text == "" {
						text = "[" + m.Type + "]"
					}
					msgs = append(msgs, msg{from: m.From, name: names[m.From], text: text})
				}
			}
		}
	} else if json.Unmarshal(body, &simple) == nil && simple.From != "" {
		msgs = append(msgs, msg{from: simple.From, name: simple.Name, text: simple.Text})
	} else {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if len(msgs) > 50 {
		msgs = msgs[:50]
	}
	out := []map[string]any{}
	if len(receipts) > 200 {
		receipts = receipts[:200]
	}
	err = db.InTenant(r.Context(), s.Pool, sc, func(tx pgx.Tx) error {
		for _, id := range receipts {
			if err := markDelivered(r.Context(), tx, cid, id); err != nil {
				return err
			}
		}
		for _, m := range msgs {
			if len(m.from) > 30 || len(m.name) > 120 {
				continue
			}
			n, created, err := inboundTicket(r.Context(), tx, cid, "whatsapp", m.name, "", "+"+strings.TrimPrefix(m.from, "+"), "", m.text)
			if err != nil {
				return err
			}
			out = append(out, map[string]any{"ticket": n, "created": created})
		}
		return nil
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": out})
}

// ======================= customer rating (public) =======================

func (s *Server) ratingScope(w http.ResponseWriter, r *http.Request) (db.Scope, string, string, bool) {
	ip := clientIP(r)
	if !s.portalLimiter.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return db.Scope{}, "", "", false
	}
	s.portalLimiter.fail(ip)
	tok := r.PathValue("token")
	if len(tok) < 32 || len(tok) > 128 {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", "", false
	}
	var tid, cid, ticket string
	if err := s.Pool.QueryRow(r.Context(), `SELECT tenant_id::text, company_id::text, ticket_id::text FROM rating_lookup($1)`, tok).Scan(&tid, &cid, &ticket); err != nil {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", "", false
	}
	return db.Scope{TenantID: tid, CompanyID: cid, IP: ip}, cid, ticket, true
}

// GET /api/v1/rate/{token}
func (s *Server) rateInfo(w http.ResponseWriter, r *http.Request) {
	sc, cid, ticket, ok := s.ratingScope(w, r)
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT co.name_en AS company, co.name_ar AS company_ar, co.logo, t.number, t.subject, t.rating, t.rating_comment, t.rated_at,
			coalesce(c.locale, co.default_locale, 'en') AS locale
			FROM tickets t JOIN companies co ON co.id = t.company_id LEFT JOIN customers c ON c.id = t.customer_id WHERE t.id = $1 AND t.company_id = $2`, ticket, cid)
	})
}

// POST /api/v1/rate/{token} {rating 1-5, comment}: the customer's rating (can be changed for 30 days).
func (s *Server) rateSubmit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rating  int    `json:"rating"`
		Comment string `json:"comment"`
	}
	if err := decode(w, r, &in); err != nil || in.Rating < 1 || in.Rating > 5 || len(in.Comment) > 2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	sc, cid, ticket, ok := s.ratingScope(w, r)
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE tickets SET rating = $3, rating_comment = trim($4), rated_at = now()
			WHERE id = $1 AND company_id = $2 AND (rated_at IS NULL OR rated_at > now() - interval '30 days')`, ticket, cid, in.Rating, in.Comment)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "rating_closed"}
		}
		return map[string]any{"ok": true}, nil
	})
}
