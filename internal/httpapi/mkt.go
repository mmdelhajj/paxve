package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Marketing (app "mkt"): mailing lists (customers or plain contacts), opt-outs, audiences built with filters on the
// customers (area, status, balance, overdue, tags, last purchase, subscription), and campaigns over WhatsApp / SMS /
// e-mail. A campaign goes through the normal message queue (throttled per minute, scheduled or at once); every
// recipient gets a tracked link and an opt-out link, and the stats come from the queue (sent / delivered / failed),
// the clicks and the opt-outs. Opted-out people are never messaged again.

const maxRecipients = 20000

type audience struct {
	AreaIDs     []string `json:"area_ids,omitempty"`
	Statuses    []string `json:"statuses,omitempty"`
	BalanceMin  string   `json:"balance_min,omitempty"`
	OverdueDays int      `json:"overdue_days,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	BoughtDays  int      `json:"bought_days,omitempty"`     // bought in the last N days
	NotBought   int      `json:"not_bought_days,omitempty"` // nothing bought in the last N days
	HasSub      *bool    `json:"has_subscription,omitempty"`
	AllCustomer bool     `json:"all_customers,omitempty"`
}

func (a *audience) valid() bool {
	if len(a.AreaIDs) > 200 || !allUUID(a.AreaIDs) || len(a.Statuses) > 5 || len(a.Tags) > 20 || a.OverdueDays < 0 || a.OverdueDays > 3650 ||
		a.BoughtDays < 0 || a.BoughtDays > 3650 || a.NotBought < 0 || a.NotBought > 3650 {
		return false
	}
	for _, s := range a.Statuses {
		if !contains([]string{"active", "suspended", "closed", "lead"}, s) {
			return false
		}
	}
	for _, t := range a.Tags {
		if len(t) > 40 {
			return false
		}
	}
	return a.BalanceMin == "" || okMoney(a.BalanceMin)
}

func (a *audience) empty() bool {
	return !a.AllCustomer && len(a.AreaIDs) == 0 && len(a.Statuses) == 0 && a.BalanceMin == "" && a.OverdueDays == 0 && len(a.Tags) == 0 &&
		a.BoughtDays == 0 && a.NotBought == 0 && a.HasSub == nil
}

// audienceSQL: the ids of the customers matching the filters ($1 = company).
func (a *audience) sql() (string, []any) {
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args)+1)
	}
	cond := []string{"c.company_id = $1"}
	if len(a.Statuses) > 0 {
		cond = append(cond, "c.status = ANY ("+arg(a.Statuses)+"::text[])")
	} else {
		cond = append(cond, "c.status <> 'closed'")
	}
	if len(a.AreaIDs) > 0 {
		cond = append(cond, "c.area_id = ANY ("+arg(a.AreaIDs)+"::uuid[])")
	}
	if a.BalanceMin != "" {
		cond = append(cond, "coalesce((SELECT sum(i.total - i.paid) FROM invoices i WHERE i.customer_id = c.id AND i.status IN ('open', 'partial')), 0) >= "+arg(a.BalanceMin)+"::numeric")
	}
	if a.OverdueDays > 0 {
		cond = append(cond, "EXISTS (SELECT 1 FROM invoices i WHERE i.customer_id = c.id AND i.status IN ('open', 'partial') AND i.due_date < current_date - "+arg(a.OverdueDays)+"::int)")
	}
	if len(a.Tags) > 0 {
		cond = append(cond, "EXISTS (SELECT 1 FROM record_tags g WHERE g.entity_type = 'customer' AND g.entity_id = c.id AND g.tag = ANY ("+arg(a.Tags)+"::text[]))")
	}
	if a.BoughtDays > 0 {
		cond = append(cond, "EXISTS (SELECT 1 FROM invoices i WHERE i.customer_id = c.id AND i.status <> 'void' AND i.issue_date >= current_date - "+arg(a.BoughtDays)+"::int)")
	}
	if a.NotBought > 0 {
		cond = append(cond, "NOT EXISTS (SELECT 1 FROM invoices i WHERE i.customer_id = c.id AND i.status <> 'void' AND i.issue_date >= current_date - "+arg(a.NotBought)+"::int)")
	}
	if a.HasSub != nil {
		not := ""
		if !*a.HasSub {
			not = "NOT "
		}
		cond = append(cond, not+"EXISTS (SELECT 1 FROM subscriptions s WHERE s.customer_id = c.id AND s.status = 'active')")
	}
	return "SELECT c.id FROM customers c WHERE " + strings.Join(cond, " AND "), args
}

type recipient struct {
	CustomerID     *string `json:"customer_id"`
	Name           string  `json:"name"`
	Phone          string  `json:"phone"`
	Email          string  `json:"email"`
	Skip           string  `json:"skip,omitempty"` // no_phone | no_email | opted_out
	phoneN, emailL string
}

// buildRecipients: the customers of the audience and the members of the list, without duplicates, each checked for
// a usable contact on the channel and against the opt-outs.
func buildRecipients(ctx context.Context, tx pgx.Tx, cid, channel string, aud *audience, listID *string) ([]recipient, error) {
	var country string
	if err := tx.QueryRow(ctx, `SELECT phone_country FROM companies WHERE id = $1`, cid).Scan(&country); err != nil {
		return nil, err
	}
	var out []recipient
	seen := map[string]bool{}
	add := func(rc recipient) {
		rc.phoneN = normalizePhone(rc.Phone, country)
		rc.emailL = strings.ToLower(strings.TrimSpace(rc.Email))
		key := ""
		switch {
		case rc.CustomerID != nil:
			key = "c:" + *rc.CustomerID
		case channel == "email":
			key = "e:" + rc.emailL
		default:
			key = "p:" + rc.phoneN
		}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, rc)
	}
	scan := func(sql string, args ...any) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rc recipient
			if err := rows.Scan(&rc.CustomerID, &rc.Name, &rc.Phone, &rc.Email); err != nil {
				return err
			}
			add(rc)
			if len(out) > maxRecipients {
				return &apiError{status: http.StatusBadRequest, code: "too_many_recipients"}
			}
		}
		return rows.Err()
	}
	if aud != nil && !aud.empty() {
		q, args := aud.sql()
		if err := scan(`SELECT c.id::text, c.name, c.phone, coalesce(c.email, '') FROM customers c WHERE c.id IN (`+q+`) ORDER BY c.name LIMIT `+
			strconv.Itoa(maxRecipients+1), append([]any{cid}, args...)...); err != nil {
			return nil, err
		}
	}
	if listID != nil {
		if err := scan(`SELECT m.customer_id::text, coalesce(c.name, m.name), coalesce(nullif(m.phone, ''), c.phone, ''), coalesce(nullif(m.email, ''), c.email, '')
			FROM mkt_list_members m LEFT JOIN customers c ON c.id = m.customer_id
			WHERE m.list_id = $1 AND m.company_id = $2 AND (c.id IS NULL OR c.status <> 'closed') ORDER BY m.created_at`, *listID, cid); err != nil {
			return nil, err
		}
	}
	// opt-outs of this company for this channel
	type opt struct{ cust, phone, email string }
	rows, err := tx.Query(ctx, `SELECT coalesce(customer_id::text, ''), phone, lower(email) FROM mkt_optouts WHERE company_id = $1 AND channel IN ('all', $2)`, cid, channel)
	if err != nil {
		return nil, err
	}
	custOut, phoneOut, emailOut := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var o opt
		if err := rows.Scan(&o.cust, &o.phone, &o.email); err != nil {
			rows.Close()
			return nil, err
		}
		if o.cust != "" {
			custOut[o.cust] = true
		}
		if p := normalizePhone(o.phone, country); p != "" {
			phoneOut[p] = true
		}
		if o.email != "" {
			emailOut[o.email] = true
		}
	}
	rows.Close()
	for i := range out {
		rc := &out[i]
		switch {
		case (rc.CustomerID != nil && custOut[*rc.CustomerID]) || (rc.phoneN != "" && phoneOut[rc.phoneN]) || (rc.emailL != "" && emailOut[rc.emailL]):
			rc.Skip = "opted_out"
		case channel == "email" && !strings.Contains(rc.emailL, "@"):
			rc.Skip = "no_email"
		case channel != "email" && rc.phoneN == "":
			rc.Skip = "no_phone"
		}
	}
	return out, nil
}

// ======================= lists =======================

func (s *Server) mktLists(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT l.id, l.name, l.description, l.is_active, l.created_at,
			(SELECT count(*) FROM mkt_list_members m WHERE m.list_id = l.id) AS members
			FROM mkt_lists l WHERE l.company_id = $1 ORDER BY l.is_active DESC, l.name`, r.PathValue("cid"))
	})
}

// POST /mkt/lists and PATCH /mkt/lists/{id} {name, description, is_active}
func (s *Server) mktSaveList(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		IsActive    *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	if (creating && strings.TrimSpace(deref(in.Name)) == "") || (in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 120)) ||
		len(deref(in.Description)) > 1000 || (!creating && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if creating {
			if err := tx.QueryRow(ctx, `INSERT INTO mkt_lists (company_id, name) VALUES ($1, trim($2)) RETURNING id::text`, cid, *in.Name).Scan(&id); err != nil {
				return nil, err
			}
		}
		return dmlJSONOne(ctx, tx, `UPDATE mkt_lists SET name = coalesce(trim($3), name), description = coalesce($4, description), is_active = coalesce($5, is_active)
			WHERE id = $1 AND company_id = $2 RETURNING id, name, description, is_active, created_at`, id, cid, in.Name, in.Description, in.IsActive)
	})
}

// GET /mkt/lists/{id}/members
func (s *Server) mktMembers(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mkt_lists WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			return nil, notFound()
		}
		return queryJSON(ctx, tx, `SELECT m.id, m.customer_id, c.code AS customer_code, coalesce(c.name, m.name) AS name,
			coalesce(nullif(m.phone, ''), c.phone, '') AS phone, coalesce(nullif(m.email, ''), c.email, '') AS email, m.created_at
			FROM mkt_list_members m LEFT JOIN customers c ON c.id = m.customer_id WHERE m.list_id = $1 ORDER BY m.created_at DESC LIMIT 5000`, id)
	})
}

// POST /mkt/lists/{id}/members {customer_ids, contacts: [{name, phone, email}], audience}: adds people (duplicates ignored).
func (s *Server) mktAddMembers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CustomerIDs []string `json:"customer_ids"`
		Contacts    []struct {
			Name  string `json:"name"`
			Phone string `json:"phone"`
			Email string `json:"email"`
		} `json:"contacts"`
		Audience *audience `json:"audience"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || len(in.CustomerIDs) > 5000 || !allUUID(in.CustomerIDs) || len(in.Contacts) > 5000 || (in.Audience != nil && !in.Audience.valid()) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, c := range in.Contacts {
		if len(c.Name) > 160 || len(c.Phone) > 40 || len(c.Email) > 200 || (strings.TrimSpace(c.Phone) == "" && strings.TrimSpace(c.Email) == "") {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mkt_lists WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			return nil, notFound()
		}
		ids := in.CustomerIDs
		if in.Audience != nil && !in.Audience.empty() {
			q, args := in.Audience.sql()
			rows, err := tx.Query(ctx, q+` LIMIT `+strconv.Itoa(maxRecipients), append([]any{cid}, args...)...)
			if err != nil {
				return nil, err
			}
			more, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, err
			}
			ids = append(ids, more...)
		}
		added := int64(0)
		if len(ids) > 0 {
			tag, err := tx.Exec(ctx, `INSERT INTO mkt_list_members (company_id, list_id, customer_id)
				SELECT $1, $2, c.id FROM customers c WHERE c.company_id = $1 AND c.id = ANY ($3::uuid[])
				ON CONFLICT (list_id, customer_id) WHERE customer_id IS NOT NULL DO NOTHING`, cid, id, ids)
			if err != nil {
				return nil, err
			}
			added += tag.RowsAffected()
		}
		for _, c := range in.Contacts {
			var dup bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mkt_list_members WHERE list_id = $1 AND customer_id IS NULL
				AND ((phone <> '' AND phone = trim($2)) OR (email <> '' AND lower(email) = lower(trim($3)))))`, id, c.Phone, c.Email).Scan(&dup); err != nil {
				return nil, err
			}
			if dup {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO mkt_list_members (company_id, list_id, name, phone, email) VALUES ($1, $2, trim($3), trim($4), trim($5))`,
				cid, id, c.Name, c.Phone, c.Email); err != nil {
				return nil, err
			}
			added++
		}
		return map[string]int64{"added": added}, nil
	})
}

// DELETE /mkt/list-members/{id}
func (s *Server) mktDeleteMember(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM mkt_list_members WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ======================= opt-outs =======================

func (s *Server) mktOptouts(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT o.id, o.channel, o.customer_id, c.name AS customer_name, o.phone, o.email, o.reason, o.source, o.created_at
			FROM mkt_optouts o LEFT JOIN customers c ON c.id = o.customer_id WHERE o.company_id = $1 ORDER BY o.created_at DESC LIMIT 5000`, r.PathValue("cid"))
	})
}

// POST /mkt/optouts {customer_id | phone | email, channel, reason}: someone asked (by phone, at the counter…) not to be messaged.
func (s *Server) mktAddOptout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CustomerID string `json:"customer_id"`
		Phone      string `json:"phone"`
		Email      string `json:"email"`
		Channel    string `json:"channel"`
		Reason     string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Channel == "" {
		in.Channel = "all"
	}
	if (in.CustomerID != "" && !isUUID(in.CustomerID)) || (in.CustomerID == "" && strings.TrimSpace(in.Phone) == "" && strings.TrimSpace(in.Email) == "") ||
		!contains([]string{"all", "whatsapp", "sms", "email"}, in.Channel) || len(in.Phone) > 40 || len(in.Email) > 200 || len(in.Reason) > 300 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if in.CustomerID != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE id = $1 AND company_id = $2)`, in.CustomerID, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_customer")
			}
		}
		return dmlJSONOne(ctx, tx, `INSERT INTO mkt_optouts (company_id, channel, customer_id, phone, email, reason, source)
			VALUES ($1, $2, nullif($3, '')::uuid, trim($4), lower(trim($5)), trim($6), 'manual') RETURNING id, channel, customer_id, phone, email, reason, source, created_at`,
			cid, in.Channel, in.CustomerID, in.Phone, in.Email, in.Reason)
	})
}

// DELETE /mkt/optouts/{id}: the person agreed to receive messages again.
func (s *Server) mktDeleteOptout(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM mkt_optouts WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// POST /mkt/audience {channel, audience, list_id}: how many people a campaign would reach, and a sample.
func (s *Server) mktAudience(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Channel  string   `json:"channel"`
		Audience audience `json:"audience"`
		ListID   string   `json:"list_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !contains([]string{"whatsapp", "sms", "email"}, in.Channel) || !in.Audience.valid() || (in.ListID != "" && !isUUID(in.ListID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		list, err := checkMktList(ctx, tx, cid, in.ListID)
		if err != nil {
			return nil, err
		}
		recs, err := buildRecipients(ctx, tx, cid, in.Channel, &in.Audience, list)
		if err != nil {
			return nil, err
		}
		reach, skips := 0, map[string]int{}
		sample := []recipient{}
		for _, rc := range recs {
			if rc.Skip != "" {
				skips[rc.Skip]++
			} else {
				reach++
			}
			if len(sample) < 25 {
				sample = append(sample, rc)
			}
		}
		return map[string]any{"total": len(recs), "reach": reach, "skipped": skips, "sample": sample}, nil
	})
}

func checkMktList(ctx context.Context, tx pgx.Tx, cid, id string) (*string, error) {
	if id == "" {
		return nil, nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mkt_lists WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, badRequest("invalid_list")
	}
	return &id, nil
}

// ======================= campaigns =======================

const campaignCols = `k.id, k.number, k.name, k.channel, k.audience, k.list_id, l.name AS list_name, k.subject, k.body, k.link_url, k.promotion_id,
	p.coupon_code AS coupon, k.scheduled_at, k.throttle_per_min, k.status, k.recipients, k.skipped, k.sent_at, k.created_at, u.full_name AS created_by_name`

const campaignFrom = ` FROM mkt_campaigns k LEFT JOIN mkt_lists l ON l.id = k.list_id LEFT JOIN promotions p ON p.id = k.promotion_id LEFT JOIN users u ON u.id = k.created_by`

const campaignStats = `(SELECT json_build_object('queued', count(*) FILTER (WHERE r.status = 'queued' AND m.status IN ('queued', 'sending')),
	'sent', count(*) FILTER (WHERE m.status IN ('sent', 'delivered')), 'delivered', count(*) FILTER (WHERE m.status = 'delivered'),
	'failed', count(*) FILTER (WHERE m.status = 'failed'), 'skipped', count(*) FILTER (WHERE r.status = 'skipped'),
	'clicked', count(*) FILTER (WHERE r.clicks > 0), 'clicks', coalesce(sum(r.clicks), 0), 'opted_out', count(*) FILTER (WHERE r.opted_out_at IS NOT NULL))
	FROM mkt_recipients r LEFT JOIN messages m ON m.id = r.message_id WHERE r.campaign_id = k.id) AS stats`

func (s *Server) mktCampaigns(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+campaignCols+`, `+campaignStats+campaignFrom+` WHERE k.company_id = $1 ORDER BY k.created_at DESC LIMIT 500`, r.PathValue("cid"))
	})
}

func (s *Server) mktGetCampaign(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		k, err := queryJSONOne(ctx, tx, `SELECT `+campaignCols+`, `+campaignStats+campaignFrom+` WHERE k.id = $1 AND k.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		recs, err := queryJSON(ctx, tx, `SELECT r.id, r.customer_id, r.name, r.phone, r.email, r.status, r.skip_reason, r.clicks, r.clicked_at, r.opted_out_at,
			m.status AS message_status, m.error AS message_error, m.sent_at
			FROM mkt_recipients r LEFT JOIN messages m ON m.id = r.message_id WHERE r.campaign_id = $1 ORDER BY r.created_at LIMIT 2000`, id)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"campaign": k, "recipients": recs}, nil
	})
}

type campaignIn struct {
	Name         *string   `json:"name"`
	Channel      *string   `json:"channel"`
	Audience     *audience `json:"audience"`
	ListID       *string   `json:"list_id"`
	Subject      *string   `json:"subject"`
	Body         *string   `json:"body"`
	LinkURL      *string   `json:"link_url"`
	PromotionID  *string   `json:"promotion_id"`
	ScheduledAt  *string   `json:"scheduled_at"`
	ThrottlePerM *int      `json:"throttle_per_min"`
}

// POST /mkt/campaigns and PATCH /mkt/campaigns/{id} (a draft or a scheduled campaign)
func (s *Server) mktSaveCampaign(w http.ResponseWriter, r *http.Request) {
	var in campaignIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	if (creating && (strings.TrimSpace(deref(in.Name)) == "" || in.Channel == nil || strings.TrimSpace(deref(in.Body)) == "")) ||
		(in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 160)) ||
		(in.Channel != nil && !contains([]string{"whatsapp", "sms", "email"}, *in.Channel)) || (in.Audience != nil && !in.Audience.valid()) ||
		!optUUID(in.ListID) || !optUUID(in.PromotionID) || len(deref(in.Subject)) > 200 || (in.Body != nil && (strings.TrimSpace(*in.Body) == "" || len(*in.Body) > 4000)) ||
		(in.LinkURL != nil && *in.LinkURL != "" && (len(*in.LinkURL) > 1000 || !(strings.HasPrefix(*in.LinkURL, "https://") || strings.HasPrefix(*in.LinkURL, "http://")))) ||
		(in.ThrottlePerM != nil && (*in.ThrottlePerM < 1 || *in.ThrottlePerM > 1000)) || (!creating && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var sched *time.Time
	if in.ScheduledAt != nil && *in.ScheduledAt != "" {
		t, err := time.Parse(time.RFC3339, *in.ScheduledAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		sched = &t
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if _, err := checkMktList(ctx, tx, cid, deref(in.ListID)); err != nil {
			return nil, err
		}
		if p := deref(in.PromotionID); p != "" {
			var code string
			if err := tx.QueryRow(ctx, `SELECT coupon_code FROM promotions WHERE id = $1 AND company_id = $2`, p, cid).Scan(&code); err != nil {
				if err == pgx.ErrNoRows {
					return nil, badRequest("invalid_promotion")
				}
				return nil, err
			}
			if code == "" {
				return nil, badRequest("promotion_without_coupon")
			}
		}
		if creating {
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'campaign')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO mkt_campaigns (company_id, number, name, channel, body) VALUES ($1, $2, trim($3), $4, $5) RETURNING id::text`,
				cid, n, *in.Name, *in.Channel, *in.Body).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			var st string
			if err := tx.QueryRow(ctx, `SELECT status FROM mkt_campaigns WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st); err != nil {
				if err == pgx.ErrNoRows {
					return nil, notFound()
				}
				return nil, err
			}
			if st != "draft" && st != "scheduled" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
		}
		var aud []byte
		if in.Audience != nil {
			aud, _ = json.Marshal(in.Audience)
		}
		clearSched := in.ScheduledAt != nil && *in.ScheduledAt == ""
		if _, err := tx.Exec(ctx, `UPDATE mkt_campaigns SET name = coalesce(trim($3), name), channel = coalesce($4, channel), audience = coalesce($5::jsonb, audience),
			list_id = CASE WHEN $6::text IS NULL THEN list_id ELSE nullif($6, '')::uuid END, subject = coalesce($7, subject), body = coalesce($8, body),
			link_url = coalesce(trim($9), link_url), promotion_id = CASE WHEN $10::text IS NULL THEN promotion_id ELSE nullif($10, '')::uuid END,
			scheduled_at = CASE WHEN $11 THEN NULL ELSE coalesce($12, scheduled_at) END, throttle_per_min = coalesce($13, throttle_per_min),
			status = CASE WHEN $11 AND status = 'scheduled' THEN 'draft' ELSE status END, updated_at = now()
			WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.Channel, aud, in.ListID, in.Subject, in.Body, in.LinkURL, in.PromotionID,
			clearSched, sched, in.ThrottlePerM); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+campaignCols+`, `+campaignStats+campaignFrom+` WHERE k.id = $1`, id)
	})
}

// POST /mkt/campaigns/{id}/{action}: send (now, or at its scheduled time), cancel, test {to}.
func (s *Server) mktCampaignAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		To string `json:"to"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if !isUUID(id) || !contains([]string{"send", "cancel", "test"}, action) || len(in.To) > 200 {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var st, channel string
		var sched *time.Time
		if err := tx.QueryRow(ctx, `SELECT status, channel, scheduled_at FROM mkt_campaigns WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st, &channel, &sched); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		switch action {
		case "test":
			if strings.TrimSpace(in.To) == "" {
				return nil, badRequest("bad_request")
			}
			k, err := loadCampaign(ctx, tx, cid, id)
			if err != nil {
				return nil, err
			}
			rc := recipient{Name: "Test"}
			if channel == "email" {
				rc.Email = in.To
			} else {
				rc.Phone = in.To
			}
			body, subject := k.render(rc, "test-"+randToken(10))
			mid, reason, err := queueOut(ctx, tx, cid, outMessage{Channel: channel, Phone: rc.Phone, Email: rc.Email, Subject: subject, Body: body, Event: "campaign_test"})
			if err != nil {
				return nil, err
			}
			if mid == "" {
				return nil, badRequest(reason)
			}
			return map[string]any{"ok": true, "body": body}, nil
		case "cancel":
			if st != "draft" && st != "scheduled" && st != "sent" && st != "sending" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			// messages still waiting in the queue are dropped
			if _, err := tx.Exec(ctx, `UPDATE messages m SET status = 'failed', error = 'cancelled' FROM mkt_recipients r
				WHERE r.campaign_id = $1 AND m.id = r.message_id AND m.status = 'queued'`, id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE mkt_campaigns SET status = 'cancelled', updated_at = now() WHERE id = $1`, id); err != nil {
				return nil, err
			}
		case "send":
			if st != "draft" && st != "scheduled" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			if sched != nil && sched.After(time.Now().Add(30*time.Second)) {
				if _, err := tx.Exec(ctx, `UPDATE mkt_campaigns SET status = 'scheduled', updated_at = now() WHERE id = $1`, id); err != nil {
					return nil, err
				}
			} else if err := dispatchCampaign(ctx, tx, cid, id); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+campaignCols+`, `+campaignStats+campaignFrom+` WHERE k.id = $1`, id)
	})
}

type campaignRow struct {
	id, name, channel, subject, body, link, coupon, company, shopLink string
	aud                                                               audience
	list                                                              *string
	throttle                                                          int
	locale                                                            string
}

func loadCampaign(ctx context.Context, tx pgx.Tx, cid, id string) (*campaignRow, error) {
	k := &campaignRow{id: id}
	var aud []byte
	var slug *string
	var shopOn bool
	err := tx.QueryRow(ctx, `SELECT k.name, k.channel, k.subject, k.body, k.link_url, coalesce(p.coupon_code, ''), k.audience, k.list_id::text, k.throttle_per_min,
		coalesce(nullif(c.name_ar, ''), c.name_en), CASE WHEN c.default_locale = 'ar' THEN coalesce(nullif(c.name_ar, ''), c.name_en) ELSE c.name_en END,
		c.public_slug, coalesce(s.enabled, false), coalesce(c.default_locale, 'ar')
		FROM mkt_campaigns k JOIN companies c ON c.id = k.company_id LEFT JOIN promotions p ON p.id = k.promotion_id LEFT JOIN shop_settings s ON s.company_id = k.company_id
		WHERE k.id = $1 AND k.company_id = $2`, id, cid).Scan(&k.name, &k.channel, &k.subject, &k.body, &k.link, &k.coupon, &aud, &k.list, &k.throttle,
		new(string), &k.company, &slug, &shopOn, &k.locale)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(aud, &k.aud)
	if slug != nil && shopOn {
		k.shopLink = publicURL() + "/#/shop/" + *slug
	}
	return k, nil
}

var optoutLine = map[string]string{
	"ar": "\n\nلإيقاف الرسائل: ",
	"en": "\n\nTo stop these messages: ",
	"fr": "\n\nPour ne plus recevoir ces messages : ",
}

// render fills the campaign text for one recipient: {name} {company} {coupon} {link} {shop} {optout}; an opt-out line
// is added when the text has none.
func (k *campaignRow) render(rc recipient, token string) (string, string) {
	link := ""
	if k.link != "" {
		link = publicURL() + "/api/v1/r/" + token
	}
	optout := publicURL() + "/#/optout/" + token
	name := strings.TrimSpace(rc.Name)
	vars := map[string]string{"name": name, "customer": name, "company": k.company, "coupon": k.coupon, "link": link, "shop": k.shopLink, "optout": optout}
	fill := func(s string) string {
		for key, v := range vars {
			s = strings.ReplaceAll(s, "{"+key+"}", v)
			s = strings.ReplaceAll(s, "{{"+key+"}}", v)
		}
		return s
	}
	body := fill(k.body)
	if !strings.Contains(k.body, "{optout}") && !strings.Contains(k.body, "{{optout}}") {
		line, ok := optoutLine[k.locale]
		if !ok {
			line = optoutLine["en"]
		}
		body += line + optout
	}
	return strings.TrimSpace(body), fill(k.subject)
}

// dispatchCampaign builds the recipients and queues one message each, spread over time by the throttle.
func dispatchCampaign(ctx context.Context, tx pgx.Tx, cid, id string) error {
	k, err := loadCampaign(ctx, tx, cid, id)
	if err != nil {
		return err
	}
	if k.aud.empty() && k.list == nil {
		return badRequest("empty_audience")
	}
	recs, err := buildRecipients(ctx, tx, cid, k.channel, &k.aud, k.list)
	if err != nil {
		return err
	}
	start := time.Now()
	sent, skipped := 0, 0
	for _, rc := range recs {
		tok := randToken(24)
		if rc.Skip != "" {
			skipped++
			if _, err := tx.Exec(ctx, `INSERT INTO mkt_recipients (company_id, campaign_id, customer_id, name, phone, email, token, status, skip_reason)
				VALUES ($1, $2, $3, $4, $5, $6, $7, 'skipped', $8)`, cid, id, rc.CustomerID, rc.Name, rc.Phone, rc.Email, tok, rc.Skip); err != nil {
				return err
			}
			continue
		}
		body, subject := k.render(rc, tok)
		at := start.Add(time.Duration(sent/k.throttle) * time.Minute)
		mid, reason, err := queueOut(ctx, tx, cid, outMessage{CustomerID: rc.CustomerID, Channel: k.channel, Phone: rc.Phone, Email: rc.Email, Subject: subject,
			Body: body, Event: "campaign", At: at})
		if err != nil {
			return err
		}
		status, skip := "queued", ""
		var midP *string
		if mid == "" {
			status, skip = "skipped", reason
			skipped++
		} else {
			midP = &mid
			sent++
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mkt_recipients (company_id, campaign_id, customer_id, name, phone, email, token, message_id, status, skip_reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, cid, id, rc.CustomerID, rc.Name, rc.Phone, rc.Email, tok, midP, status, skip); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE mkt_campaigns SET status = 'sent', recipients = $2, skipped = $3, sent_at = now(), updated_at = now() WHERE id = $1`, id, sent, skipped)
	return err
}

// dispatchDueCampaigns: the scheduled campaigns whose time has come (worker).
func dispatchDueCampaigns(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, `SELECT k.id::text, k.company_id::text FROM mkt_campaigns k JOIN companies c ON c.id = k.company_id
		WHERE k.status = 'scheduled' AND k.scheduled_at <= now() AND c.is_active AND 'mkt' = ANY (c.apps) ORDER BY k.scheduled_at LIMIT 20 FOR UPDATE OF k SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type due struct{ id, cid string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.cid); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()
	n := 0
	for _, d := range list {
		if err := dispatchCampaign(ctx, tx, d.cid, d.id); err != nil {
			var ae *apiError
			if !errors.As(err, &ae) {
				return n, err
			}
			// an empty audience or similar: the campaign stops instead of retrying every minute
			if _, err := tx.Exec(ctx, `UPDATE mkt_campaigns SET status = 'cancelled', updated_at = now() WHERE id = $1`, d.id); err != nil {
				return n, err
			}
			continue
		}
		n++
	}
	return n, nil
}

// GET /mkt/overview: totals for the marketing home.
func (s *Server) mktOverview(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT (SELECT count(*) FROM mkt_campaigns WHERE company_id = $1) AS campaigns,
			(SELECT count(*) FROM mkt_campaigns WHERE company_id = $1 AND status = 'scheduled') AS scheduled,
			(SELECT count(*) FROM mkt_lists WHERE company_id = $1 AND is_active) AS lists,
			(SELECT count(*) FROM mkt_optouts WHERE company_id = $1) AS optouts,
			(SELECT count(*) FROM mkt_recipients r JOIN messages m ON m.id = r.message_id WHERE r.company_id = $1 AND m.status IN ('sent', 'delivered')
			   AND m.sent_at > now() - interval '30 days') AS sent_30d,
			(SELECT count(*) FROM mkt_recipients r WHERE r.company_id = $1 AND r.clicked_at > now() - interval '30 days') AS clicked_30d,
			(SELECT count(*) FROM promotions WHERE company_id = $1 AND coupon_code <> '' AND is_active) AS coupons`, r.PathValue("cid"))
	})
}

// ======================= public: tracked link and opt-out =======================

// GET /api/v1/r/{token}: counts the click and goes on to the campaign's link.
func (s *Server) mktClick(w http.ResponseWriter, r *http.Request) {
	sc, cid, rid, _, ok := s.tokenScope(w, r, "mkt")
	if !ok {
		return
	}
	var link string
	err := dbInScope(r.Context(), s, sc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE mkt_recipients SET clicks = clicks + 1, clicked_at = coalesce(clicked_at, now()) WHERE id = $1 AND company_id = $2 AND clicks < 1000`,
			rid, cid); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT k.link_url FROM mkt_recipients r JOIN mkt_campaigns k ON k.id = r.campaign_id WHERE r.id = $1`, rid).Scan(&link)
	})
	if err != nil || (!strings.HasPrefix(link, "https://") && !strings.HasPrefix(link, "http://")) {
		link = publicURL()
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, link, http.StatusFound)
}

// GET /api/v1/optout/{token}: what the person is about to stop.
func (s *Server) optoutInfo(w http.ResponseWriter, r *http.Request) {
	sc, cid, rid, _, ok := s.tokenScope(w, r, "mkt")
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT c.name_en AS company, c.name_ar AS company_ar, c.logo, c.default_locale AS locale, k.channel,
			CASE WHEN k.channel = 'email' THEN regexp_replace(r.email, '^(.).*(@.*)$', '\1***\2') ELSE '***' || right(r.phone, 3) END AS contact,
			r.opted_out_at IS NOT NULL AS done
			FROM mkt_recipients r JOIN mkt_campaigns k ON k.id = r.campaign_id JOIN companies c ON c.id = r.company_id WHERE r.id = $1 AND r.company_id = $2`, rid, cid)
	})
}

// POST /api/v1/optout/{token} {all, reason}: no more marketing on this channel (or on any).
func (s *Server) optoutSubmit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		All    bool   `json:"all"`
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &in) || len(in.Reason) > 300 {
		if len(in.Reason) > 300 {
			writeErr(w, http.StatusBadRequest, "bad_request")
		}
		return
	}
	sc, cid, rid, _, ok := s.tokenScope(w, r, "mkt")
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var cust *string
		var phone, email, channel string
		var done bool
		if err := tx.QueryRow(ctx, `SELECT r.customer_id::text, r.phone, r.email, k.channel, r.opted_out_at IS NOT NULL FROM mkt_recipients r
			JOIN mkt_campaigns k ON k.id = r.campaign_id WHERE r.id = $1 AND r.company_id = $2 FOR UPDATE OF r`, rid, cid).Scan(&cust, &phone, &email, &channel, &done); err != nil {
			return nil, err
		}
		if in.All {
			channel = "all"
		}
		if !done || in.All {
			if _, err := tx.Exec(ctx, `INSERT INTO mkt_optouts (company_id, channel, customer_id, phone, email, reason, source)
				VALUES ($1, $2, $3, $4, lower($5), trim($6), 'link')`, cid, channel, cust, phone, email, in.Reason); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE mkt_recipients SET opted_out_at = coalesce(opted_out_at, now()) WHERE id = $1`, rid); err != nil {
				return nil, err
			}
		}
		return map[string]bool{"ok": true}, nil
	})
}

// markDelivered: a WhatsApp delivery receipt (status "delivered" / "read") for a message the company sent.
func markDelivered(ctx context.Context, tx pgx.Tx, cid, providerID string) error {
	if providerID == "" || len(providerID) > 200 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE messages SET status = 'delivered' WHERE company_id = $1 AND provider_message_id = $2 AND status = 'sent'`, cid, providerID)
	return err
}
