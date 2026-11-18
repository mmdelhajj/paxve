package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CRM light: a pipeline of leads / opportunities in stages (kanban), activities with due dates and an
// assigned user (call, meeting, task, …), notes on leads and customers, and lead -> customer -> quotation.

var defaultStages = []struct {
	Name        string
	I18n        map[string]string
	Probability int
	Won         bool
}{
	{"New", map[string]string{"ar": "جديد", "fr": "Nouveau"}, 10, false},
	{"Qualified", map[string]string{"ar": "مؤهل", "fr": "Qualifié"}, 30, false},
	{"Proposal", map[string]string{"ar": "عرض سعر", "fr": "Proposition"}, 60, false},
	{"Won", map[string]string{"ar": "تم الفوز", "fr": "Gagné"}, 100, true},
}

func seedStages(ctx context.Context, tx pgx.Tx, cid string) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM crm_stages WHERE company_id = $1`, cid).Scan(&n); err != nil || n > 0 {
		return err
	}
	for i, st := range defaultStages {
		j, _ := json.Marshal(st.I18n)
		if _, err := tx.Exec(ctx, `INSERT INTO crm_stages (company_id, name, name_i18n, seq, probability, is_won) VALUES ($1, $2, $3, $4, $5, $6)`,
			cid, st.Name, j, (i+1)*10, st.Probability, st.Won); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) listStages(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if err := seedStages(ctx, tx, cid); err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT id, name, name_i18n, seq, probability, is_won, is_active FROM crm_stages
			WHERE company_id = $1 ORDER BY seq, created_at`, cid)
	})
}

func (s *Server) saveStage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        *string           `json:"name"`
		NameI18n    map[string]string `json:"name_i18n"`
		Seq         *int              `json:"seq"`
		Probability *int              `json:"probability"`
		IsWon       *bool             `json:"is_won"`
		IsActive    *bool             `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Name == nil || strings.TrimSpace(*in.Name) == "")) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") ||
		(in.Probability != nil && (*in.Probability < 0 || *in.Probability > 100)) || (!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var i18n []byte
	if in.NameI18n != nil {
		i18n, _ = json.Marshal(in.NameI18n)
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if create {
			if _, err := companyBase(ctx, tx, cid); err != nil {
				return nil, err
			}
			return dmlJSONOne(ctx, tx, `INSERT INTO crm_stages (company_id, name, name_i18n, seq, probability, is_won)
				VALUES ($1, trim($2), coalesce($3::jsonb, '{}'), coalesce($4, (SELECT coalesce(max(seq), 0) + 10 FROM crm_stages WHERE company_id = $1)),
				coalesce($5, 50), coalesce($6, false)) RETURNING id, name, name_i18n, seq, probability, is_won, is_active`,
				cid, *in.Name, i18n, in.Seq, in.Probability, in.IsWon)
		}
		return dmlJSONOne(ctx, tx, `UPDATE crm_stages SET name = coalesce(trim($3), name), name_i18n = coalesce($4::jsonb, name_i18n),
			seq = coalesce($5, seq), probability = coalesce($6, probability), is_won = coalesce($7, is_won), is_active = coalesce($8, is_active)
			WHERE id = $1 AND company_id = $2 RETURNING id, name, name_i18n, seq, probability, is_won, is_active`,
			id, cid, in.Name, i18n, in.Seq, in.Probability, in.IsWon, in.IsActive)
	})
}

const leadCols = `l.id, l.number, l.title, l.contact_name, l.organisation, l.phone, l.email, l.address, l.source, l.stage_id,
	st.name AS stage_name, st.name_i18n AS stage_name_i18n, l.status, l.lost_reason, l.expected_revenue::text AS expected_revenue,
	l.currency, l.probability, l.expected_close, l.assigned_to, u.full_name AS assigned_name, l.customer_id, c.name AS customer_name,
	l.notes, l.created_at, l.updated_at, l.closed_at,
	(SELECT min(a.due_date) FROM crm_activities a WHERE a.lead_id = l.id AND a.done_at IS NULL AND a.kind <> 'note') AS next_activity,
	(SELECT count(*) FROM sales_orders o WHERE o.lead_id = l.id AND o.status <> 'cancelled') AS quotes`

const leadFrom = ` FROM crm_leads l LEFT JOIN crm_stages st ON st.id = l.stage_id LEFT JOIN users u ON u.id = l.assigned_to
	LEFT JOIN customers c ON c.id = l.customer_id`

func (s *Server) listLeads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, assigned, search, customer := q.Get("status"), q.Get("assigned_to"), strings.TrimSpace(q.Get("q")), q.Get("customer_id")
	if assigned == "me" {
		assigned = principal(r).ID
	}
	if (assigned != "" && !isUUID(assigned)) || (customer != "" && !isUUID(customer)) ||
		(status != "" && status != "open" && status != "won" && status != "lost") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := seedStages(ctx, tx, cid); err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT `+leadCols+leadFrom+` WHERE l.company_id = $1 AND ($2 = '' OR l.status = $2)
			AND ($3 = '' OR l.assigned_to = $3::uuid) AND ($5 = '' OR l.customer_id = $5::uuid)
			AND ($4 = '' OR l.title ILIKE '%' || $4 || '%' OR l.contact_name ILIKE '%' || $4 || '%' OR l.organisation ILIKE '%' || $4 || '%'
			     OR l.phone ILIKE '%' || $4 || '%' OR l.number::text = $4)
			ORDER BY l.status = 'open' DESC, l.updated_at DESC LIMIT 2000`, cid, status, assigned, search, customer)
	})
}

type leadIn struct {
	Title           *string `json:"title"`
	ContactName     *string `json:"contact_name"`
	Organisation    *string `json:"organisation"`
	Phone           *string `json:"phone"`
	Email           *string `json:"email"`
	Address         *string `json:"address"`
	Source          *string `json:"source"`
	StageID         *string `json:"stage_id"`
	ExpectedRevenue *string `json:"expected_revenue"`
	Currency        *string `json:"currency"`
	Probability     *int    `json:"probability"`
	ExpectedClose   *string `json:"expected_close"` // "" clears
	AssignedTo      *string `json:"assigned_to"`    // "" clears
	CustomerID      *string `json:"customer_id"`    // "" clears
	Notes           *string `json:"notes"`
	Status          *string `json:"status"` // open | won | lost
	LostReason      *string `json:"lost_reason"`
}

func (in *leadIn) valid(create bool) bool {
	if create && (in.Title == nil || strings.TrimSpace(*in.Title) == "") {
		return false
	}
	if in.Title != nil && strings.TrimSpace(*in.Title) == "" {
		return false
	}
	if in.ExpectedRevenue != nil && *in.ExpectedRevenue != "" {
		if v, ok := money(*in.ExpectedRevenue); !ok || v.IsNegative() {
			return false
		}
	}
	return (in.Currency == nil || *in.Currency == "" || currencyRe.MatchString(*in.Currency)) &&
		(in.Probability == nil || (*in.Probability >= 0 && *in.Probability <= 100)) &&
		(in.ExpectedClose == nil || *in.ExpectedClose == "" || validDate(*in.ExpectedClose)) &&
		optUUID(in.StageID) && optUUID(in.AssignedTo) && optUUID(in.CustomerID) &&
		(in.Status == nil || *in.Status == "open" || *in.Status == "won" || *in.Status == "lost")
}

func (s *Server) saveLead(w http.ResponseWriter, r *http.Request) {
	var in leadIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if !in.valid(create) || (!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if err := seedStages(ctx, tx, cid); err != nil {
			return nil, err
		}
		if in.StageID != nil && *in.StageID != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_stages WHERE id = $1 AND company_id = $2)`, *in.StageID, cid).Scan(&ok); err != nil || !ok {
				if err != nil {
					return nil, err
				}
				return nil, badRequest("invalid_stage")
			}
		}
		if in.CustomerID != nil && *in.CustomerID != "" {
			if _, err := lockCustomer(ctx, tx, cid, *in.CustomerID); err != nil {
				return nil, err
			}
		}
		if create {
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'lead')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO crm_leads (company_id, number, title, stage_id, assigned_to)
				VALUES ($1, $2, trim($3), coalesce(nullif($4, '')::uuid, (SELECT id FROM crm_stages WHERE company_id = $1 AND is_active AND NOT is_won ORDER BY seq LIMIT 1)),
				        coalesce(nullif($5, '')::uuid, app_user())) RETURNING id::text`,
				cid, n, *in.Title, deref(in.StageID), deref(in.AssignedTo)).Scan(&id); err != nil {
				return nil, err
			}
		}
		// moving to a "won" stage wins the lead; status changes record the closing time
		tag, err := tx.Exec(ctx, `UPDATE crm_leads SET title = coalesce(trim($3), title), contact_name = coalesce(trim($4), contact_name),
			organisation = coalesce(trim($5), organisation), phone = coalesce(trim($6), phone), email = coalesce(lower(trim($7)), email),
			address = coalesce(trim($8), address), source = coalesce(trim($9), source), stage_id = coalesce(nullif($10, '')::uuid, stage_id),
			expected_revenue = coalesce(nullif($11, '')::numeric, expected_revenue),
			currency = CASE WHEN $12::text IS NULL THEN currency ELSE nullif($12, '') END,
			probability = coalesce($13, (SELECT st.probability FROM crm_stages st WHERE st.id = nullif($10, '')::uuid), probability),
			expected_close = CASE WHEN $14::text IS NULL THEN expected_close ELSE nullif($14, '')::date END,
			assigned_to = CASE WHEN $15::text IS NULL THEN assigned_to ELSE nullif($15, '')::uuid END,
			customer_id = CASE WHEN $16::text IS NULL THEN customer_id ELSE nullif($16, '')::uuid END,
			notes = coalesce($17, notes),
			status = coalesce($18, CASE WHEN (SELECT st.is_won FROM crm_stages st WHERE st.id = nullif($10, '')::uuid) THEN 'won' END, status),
			lost_reason = coalesce(trim($19), lost_reason)
			WHERE id = $1 AND company_id = $2`,
			id, cid, in.Title, in.ContactName, in.Organisation, in.Phone, in.Email, in.Address, in.Source, deref(in.StageID),
			deref(in.ExpectedRevenue), in.Currency, in.Probability, in.ExpectedClose, in.AssignedTo, in.CustomerID, in.Notes, in.Status, in.LostReason)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		if _, err := tx.Exec(ctx, `UPDATE crm_leads SET closed_at = CASE WHEN status = 'open' THEN NULL ELSE coalesce(closed_at, now()) END,
			probability = CASE status WHEN 'won' THEN 100 WHEN 'lost' THEN 0 ELSE probability END WHERE id = $1`, id); err != nil {
			return nil, err
		}
		return loadLead(ctx, tx, cid, id)
	})
}

func loadLead(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	lead, err := queryJSONOne(ctx, tx, `SELECT `+leadCols+leadFrom+` WHERE l.id = $1 AND l.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	acts, err := queryJSON(ctx, tx, `SELECT `+activityCols+activityFrom+` WHERE a.lead_id = $1 ORDER BY a.done_at IS NULL DESC, a.due_date NULLS LAST, a.created_at DESC`, id)
	if err != nil {
		return nil, err
	}
	quotes, err := queryJSON(ctx, tx, `SELECT o.id, o.number, o.order_number, o.status, o.currency, o.total::text AS total, o.quote_date
		FROM sales_orders o WHERE o.lead_id = $1 ORDER BY o.created_at DESC`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"lead": lead, "activities": acts, "quotes": quotes}, nil
}

func (s *Server) getLead(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return loadLead(ctx, tx, cid, id) })
}

// convertLead creates (or links) the customer of a lead. Body: {customer_id?} — without it a new customer is
// created from the lead's contact details.
func (s *Server) convertLead(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CustomerID string `json:"customer_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.CustomerID != "" && !isUUID(in.CustomerID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var title, contact, org, phone, email, address, notes string
		var existing *string
		err := tx.QueryRow(ctx, `SELECT title, contact_name, organisation, phone, email, address, notes, customer_id::text FROM crm_leads
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&title, &contact, &org, &phone, &email, &address, &notes, &existing)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		customer := in.CustomerID
		if customer == "" && existing != nil {
			customer = *existing
		}
		if customer != "" {
			if _, err := lockCustomer(ctx, tx, cid, customer); err != nil {
				return nil, err
			}
		} else {
			name := strings.TrimSpace(org)
			if name == "" {
				name = strings.TrimSpace(contact)
			}
			if name == "" {
				name = title
			}
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'customer')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			code := strconv.FormatInt(n, 10)
			for i := 0; i < 50; i++ { // skip codes already taken by hand
				var taken bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE company_id = $1 AND code = $2)`, cid, code).Scan(&taken); err != nil {
					return nil, err
				}
				if !taken {
					break
				}
				if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'customer')`, cid).Scan(&n); err != nil {
					return nil, err
				}
				code = strconv.FormatInt(n, 10)
			}
			memo := notes
			if contact != "" && org != "" {
				memo = strings.TrimSpace("Contact: " + contact + "\n" + notes)
			}
			if err := tx.QueryRow(ctx, `INSERT INTO customers (company_id, code, name, phone, email, address, notes)
				VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`, cid, code, name, phone, email, address, memo).Scan(&customer); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE crm_leads SET customer_id = $2 WHERE id = $1`, id, customer); err != nil {
			return nil, err
		}
		// activities of the lead show on the customer too
		if _, err := tx.Exec(ctx, `UPDATE crm_activities SET customer_id = $2 WHERE lead_id = $1 AND customer_id IS NULL`, id, customer); err != nil {
			return nil, err
		}
		return loadLead(ctx, tx, cid, id)
	})
}

// ---- activities & notes ----

const activityCols = `a.id, a.lead_id, l.title AS lead_title, a.customer_id, c.name AS customer_name, a.kind, a.summary, a.details,
	a.due_date, a.assigned_to, u.full_name AS assigned_name, a.done_at, du.full_name AS done_by_name, cu.full_name AS created_by_name, a.created_at`

const activityFrom = ` FROM crm_activities a LEFT JOIN crm_leads l ON l.id = a.lead_id LEFT JOIN customers c ON c.id = a.customer_id
	LEFT JOIN users u ON u.id = a.assigned_to LEFT JOIN users du ON du.id = a.done_by LEFT JOIN users cu ON cu.id = a.created_by`

var activityKinds = map[string]bool{"note": true, "call": true, "meeting": true, "task": true, "whatsapp": true, "email": true, "visit": true}

// listActivities: ?lead_id= | ?customer_id= | ?mine=1 (open activities assigned to me) | ?open=1 (all open, by due date)
func (s *Server) listActivities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lead, customer := q.Get("lead_id"), q.Get("customer_id")
	if (lead != "" && !isUUID(lead)) || (customer != "" && !isUUID(customer)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	mine, open := q.Get("mine") == "1", q.Get("open") == "1"
	me := principal(r).ID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+activityCols+activityFrom+` WHERE a.company_id = $1
			AND ($2 = '' OR a.lead_id = $2::uuid) AND ($3 = '' OR a.customer_id = $3::uuid)
			AND (NOT $4 OR (a.assigned_to = $6::uuid AND a.done_at IS NULL AND a.kind <> 'note'))
			AND (NOT $5 OR (a.done_at IS NULL AND a.kind <> 'note'))
			ORDER BY CASE WHEN $4 OR $5 THEN a.due_date END NULLS LAST, a.created_at DESC LIMIT 1000`,
			r.PathValue("cid"), lead, customer, mine, open, me)
	})
}

func (s *Server) saveActivity(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeadID     *string `json:"lead_id"`
		CustomerID *string `json:"customer_id"`
		Kind       *string `json:"kind"`
		Summary    *string `json:"summary"`
		Details    *string `json:"details"`
		DueDate    *string `json:"due_date"`    // "" clears
		AssignedTo *string `json:"assigned_to"` // "" clears
		Done       *bool   `json:"done"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Summary == nil || strings.TrimSpace(*in.Summary) == "")) || (in.Summary != nil && strings.TrimSpace(*in.Summary) == "") ||
		(in.Kind != nil && !activityKinds[*in.Kind]) || (in.DueDate != nil && *in.DueDate != "" && !validDate(*in.DueDate)) ||
		!optUUID(in.LeadID) || !optUUID(in.CustomerID) || !optUUID(in.AssignedTo) || (!create && !isUUID(id)) ||
		(create && deref(in.LeadID) == "" && deref(in.CustomerID) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if lead := deref(in.LeadID); lead != "" {
			var ok bool
			var cust *string
			if err := tx.QueryRow(ctx, `SELECT true, customer_id::text FROM crm_leads WHERE id = $1 AND company_id = $2`, lead, cid).Scan(&ok, &cust); err != nil {
				if err == pgx.ErrNoRows {
					return nil, badRequest("invalid_lead")
				}
				return nil, err
			}
			if cust != nil && in.CustomerID == nil {
				in.CustomerID = cust
			}
		}
		if c := deref(in.CustomerID); c != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE id = $1 AND company_id = $2)`, c, cid).Scan(&ok); err != nil || !ok {
				if err != nil {
					return nil, err
				}
				return nil, badRequest("invalid_customer")
			}
		}
		if create {
			kind := "task"
			if in.Kind != nil {
				kind = *in.Kind
			}
			if err := tx.QueryRow(ctx, `INSERT INTO crm_activities (company_id, lead_id, customer_id, kind, summary, assigned_to)
				VALUES ($1, nullif($2, '')::uuid, nullif($3, '')::uuid, $4, trim($5), CASE WHEN $4 = 'note' THEN NULL ELSE app_user() END) RETURNING id::text`,
				cid, deref(in.LeadID), deref(in.CustomerID), kind, *in.Summary).Scan(&id); err != nil {
				return nil, err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE crm_activities SET kind = coalesce($3, kind), summary = coalesce(trim($4), summary),
			details = coalesce($5, details), due_date = CASE WHEN $6::text IS NULL THEN due_date ELSE nullif($6, '')::date END,
			assigned_to = CASE WHEN $7::text IS NULL THEN assigned_to ELSE nullif($7, '')::uuid END,
			done_at = CASE WHEN $8::boolean IS NULL THEN done_at WHEN $8 THEN coalesce(done_at, now()) ELSE NULL END,
			done_by = CASE WHEN $8::boolean IS NULL THEN done_by WHEN $8 THEN coalesce(done_by, app_user()) ELSE NULL END
			WHERE id = $1 AND company_id = $2`, id, cid, in.Kind, in.Summary, in.Details, in.DueDate, in.AssignedTo, in.Done)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		// touching a lead's activity bumps the lead in the pipeline
		if _, err := tx.Exec(ctx, `UPDATE crm_leads SET updated_at = now() WHERE id = (SELECT lead_id FROM crm_activities WHERE id = $1)`, id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+activityCols+activityFrom+` WHERE a.id = $1`, id)
	})
}

func (s *Server) deleteActivity(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		// only the author (or an admin) removes an activity
		tag, err := tx.Exec(ctx, `DELETE FROM crm_activities WHERE id = $1 AND company_id = $2 AND (created_by = app_user() OR $3)`,
			id, cid, can(r, "crm.manage"))
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// crmUsers: active users of the organisation (id + name) to assign leads and activities; any role may read it.
func (s *Server) crmUsers(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT id, full_name, true AS is_active FROM users WHERE is_active AND role <> 'collector' ORDER BY full_name`)
	})
}

// crmSummary: pipeline totals per stage and activity counters (overdue / today / mine) for the CRM page header.
func (s *Server) crmSummary(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	me := principal(r).ID
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := seedStages(ctx, tx, cid); err != nil {
			return nil, err
		}
		stages, err := queryJSON(ctx, tx, `SELECT st.id, l.currency, count(l.id) AS count, coalesce(sum(l.expected_revenue), 0)::text AS revenue,
			coalesce(sum(l.expected_revenue * l.probability / 100), 0)::text AS weighted
			FROM crm_stages st LEFT JOIN crm_leads l ON l.stage_id = st.id AND l.status = 'open'
			WHERE st.company_id = $1 GROUP BY st.id, l.currency ORDER BY st.id`, cid)
		if err != nil {
			return nil, err
		}
		acts, err := queryJSONOne(ctx, tx, `SELECT
			count(*) FILTER (WHERE due_date < current_date) AS overdue,
			count(*) FILTER (WHERE due_date = current_date) AS today,
			count(*) FILTER (WHERE assigned_to = $2::uuid) AS mine
			FROM crm_activities WHERE company_id = $1 AND done_at IS NULL AND kind <> 'note'`, cid, me)
		if err != nil {
			return nil, err
		}
		won, err := queryJSONOne(ctx, tx, `SELECT count(*) FILTER (WHERE status = 'won' AND closed_at > now() - interval '30 days') AS won_30d,
			count(*) FILTER (WHERE status = 'lost' AND closed_at > now() - interval '30 days') AS lost_30d,
			count(*) FILTER (WHERE status = 'open') AS open FROM crm_leads WHERE company_id = $1`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"stages": stages, "activities": acts, "leads": won}, nil
	})
}
