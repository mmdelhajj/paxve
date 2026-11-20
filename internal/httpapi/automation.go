package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// No-code automation rules: WHEN a record is created / updated (database triggers queue the event) or ON A
// SCHEDULE (the worker scans the open records), IF every condition holds (field comparisons), THEN run the actions
// (activity, message to the customer, in-app notice, set a field, tag, webhook, purchase request). Every run is
// logged. Loop guard: changes made by a rule never queue new events; a rule that fires more than
// automationMaxPerHour times in an hour pauses itself; scheduled rules act once per record (or every repeat_days).

const automationMaxPerHour = 300

type autoField struct {
	Name string   `json:"name"`
	Kind string   `json:"kind"` // text | number | date | bool | user | enum
	Enum []string `json:"enum,omitempty"`
}

type autoModel struct {
	Entity   string      // chatter / activity entity type ("" = none)
	View     string      // one record as a row: $1 = record id, $2 = company id
	Scan     string      // scheduled candidates of company $1 (ids)
	Fields   []autoField // what conditions and placeholders can use
	Settable map[string][]string
	Triggers []string
}

const autoCollector = `(SELECT ar.collector_id FROM areas ar WHERE ar.id = c.area_id)`

var autoModels = map[string]autoModel{
	"invoice": {Entity: "invoice", Triggers: []string{"created", "updated", "scheduled"},
		View: `SELECT i.id, i.number, i.customer_id, c.name AS customer_name, c.phone, i.currency, i.total::float8 AS total, i.paid::float8 AS paid,
			(i.total - i.paid)::float8 AS balance, i.status, i.issue_date, i.due_date,
			CASE WHEN i.status IN ('open', 'partial') THEN greatest(current_date - i.due_date, 0) ELSE 0 END AS days_overdue,
			` + autoCollector + ` AS assignee_id, i.created_by AS creator_id, 'Invoice #' || i.number || ' · ' || c.name AS label
			FROM invoices i JOIN customers c ON c.id = i.customer_id WHERE i.id = $1 AND i.company_id = $2`,
		Scan: `SELECT id FROM invoices WHERE company_id = $1 AND status IN ('open', 'partial') ORDER BY due_date LIMIT 2000`,
		Fields: []autoField{{"number", "number", nil}, {"customer_name", "text", nil}, {"currency", "text", nil}, {"total", "number", nil},
			{"paid", "number", nil}, {"balance", "number", nil}, {"status", "enum", []string{"open", "partial", "paid", "void"}}, {"issue_date", "date", nil},
			{"due_date", "date", nil}, {"days_overdue", "number", nil}}},
	"ticket": {Entity: "ticket", Triggers: []string{"created", "updated", "scheduled"},
		View: `SELECT t.id, t.number, t.subject, t.category, t.priority, t.status, t.channel, t.customer_id, coalesce(c.name, '') AS customer_name,
			t.contact_phone AS phone, t.assigned_to AS assignee_id, t.created_by AS creator_id,
			round(extract(epoch FROM (coalesce(t.resolved_at, now()) - t.created_at)) / 3600, 1)::float8 AS age_hours,
			CASE WHEN t.sla_policy_id IS NULL THEN 'none' WHEN coalesce(t.first_response_at, CASE WHEN t.resolved_at IS NULL THEN now() END) > t.response_due
			  OR coalesce(t.resolved_at, now()) > t.resolution_due THEN 'breached' ELSE 'ok' END AS sla_state,
			(t.escalated_at IS NOT NULL) AS escalated, t.rating, 'Ticket #' || t.number || ' · ' || t.subject AS label
			FROM tickets t LEFT JOIN customers c ON c.id = t.customer_id WHERE t.id = $1 AND t.company_id = $2`,
		Scan: `SELECT id FROM tickets WHERE company_id = $1 AND status NOT IN ('done', 'cancelled') ORDER BY created_at LIMIT 2000`,
		Fields: []autoField{{"number", "number", nil}, {"subject", "text", nil}, {"category", "enum", ticketCategories}, {"priority", "enum", ticketPriorities},
			{"status", "enum", ticketStatuses}, {"channel", "enum", ticketChannels}, {"customer_name", "text", nil}, {"age_hours", "number", nil},
			{"sla_state", "enum", []string{"none", "ok", "breached"}}, {"escalated", "bool", nil}, {"rating", "number", nil}, {"assignee_id", "user", nil}},
		Settable: map[string][]string{"priority": ticketPriorities, "status": {"new", "in_progress", "waiting"}, "assigned_to": nil}},
	"subscription": {Entity: "subscription", Triggers: []string{"created", "updated", "scheduled"},
		View: `SELECT s.id, s.customer_id, c.name AS customer_name, c.phone, p.name AS plan_name, s.status, s.start_date,
			(current_date - s.start_date) AS age_days, ` + autoCollector + ` AS assignee_id, NULL::uuid AS creator_id, c.name || ' · ' || p.name AS label
			FROM subscriptions s JOIN customers c ON c.id = s.customer_id JOIN plans p ON p.id = s.plan_id WHERE s.id = $1 AND s.company_id = $2`,
		Scan: `SELECT id FROM subscriptions WHERE company_id = $1 AND status <> 'ended' ORDER BY start_date LIMIT 5000`,
		Fields: []autoField{{"customer_name", "text", nil}, {"plan_name", "text", nil}, {"status", "enum", []string{"active", "suspended", "ended"}},
			{"start_date", "date", nil}, {"age_days", "number", nil}}},
	"sales_order": {Entity: "sales_order", Triggers: []string{"created", "updated", "scheduled"},
		View: `SELECT o.id, coalesce(o.order_number, o.number) AS number, o.customer_id, c.name AS customer_name, c.phone, o.currency, o.total::float8 AS total,
			o.status, o.quote_date, (current_date - o.quote_date) AS age_days, coalesce(o.salesperson_id, o.created_by) AS assignee_id, o.created_by AS creator_id,
			'Order #' || coalesce(o.order_number, o.number) || ' · ' || c.name AS label
			FROM sales_orders o JOIN customers c ON c.id = o.customer_id WHERE o.id = $1 AND o.company_id = $2`,
		Scan: `SELECT id FROM sales_orders WHERE company_id = $1 AND status IN ('draft', 'sent', 'confirmed') ORDER BY quote_date LIMIT 2000`,
		Fields: []autoField{{"number", "number", nil}, {"customer_name", "text", nil}, {"currency", "text", nil}, {"total", "number", nil},
			{"status", "enum", []string{"draft", "sent", "confirmed", "done", "cancelled"}}, {"quote_date", "date", nil}, {"age_days", "number", nil}}},
	"task": {Entity: "project_task", Triggers: []string{"created", "updated", "scheduled"},
		View: `SELECT t.id, t.number, t.title, p.name AS project_name, t.priority, t.deadline, st.name AS stage_name, (t.done_at IS NOT NULL) AS done,
			CASE WHEN t.done_at IS NULL AND t.deadline IS NOT NULL THEN greatest(current_date - t.deadline, 0) ELSE 0 END AS days_overdue,
			t.assignee_id, t.created_by AS creator_id, p.customer_id, '#' || t.number || ' ' || t.title AS label
			FROM project_tasks t JOIN projects p ON p.id = t.project_id LEFT JOIN project_stages st ON st.id = t.stage_id WHERE t.id = $1 AND t.company_id = $2`,
		Scan: `SELECT id FROM project_tasks WHERE company_id = $1 AND done_at IS NULL ORDER BY deadline NULLS LAST LIMIT 5000`,
		Fields: []autoField{{"number", "number", nil}, {"title", "text", nil}, {"project_name", "text", nil}, {"priority", "enum", taskPriorities},
			{"deadline", "date", nil}, {"stage_name", "text", nil}, {"done", "bool", nil}, {"days_overdue", "number", nil}, {"assignee_id", "user", nil}},
		Settable: map[string][]string{"priority": taskPriorities, "assignee_id": nil}},
	"service_job": {Entity: "service_job", Triggers: []string{"created", "updated", "scheduled"},
		View: `SELECT j.id, j.number, j.title, j.customer_id, c.name AS customer_name, j.phone, j.status, j.priority, j.scheduled_start,
			CASE WHEN j.status IN ('new', 'scheduled') AND j.scheduled_start < now() THEN round(extract(epoch FROM now() - j.scheduled_start) / 3600, 1) ELSE 0 END::float8 AS hours_late,
			(j.invoice_id IS NOT NULL) AS invoiced, j.technician_id AS assignee_id, j.created_by AS creator_id, 'Job #' || j.number || ' · ' || j.title AS label
			FROM service_jobs j JOIN customers c ON c.id = j.customer_id WHERE j.id = $1 AND j.company_id = $2`,
		Scan: `SELECT id FROM service_jobs WHERE company_id = $1 AND (status IN ('new', 'scheduled', 'in_progress') OR (status = 'done' AND invoice_id IS NULL)) LIMIT 2000`,
		Fields: []autoField{{"number", "number", nil}, {"title", "text", nil}, {"customer_name", "text", nil},
			{"status", "enum", jobStatuses}, {"priority", "enum", taskPriorities}, {"hours_late", "number", nil}, {"invoiced", "bool", nil}, {"assignee_id", "user", nil}},
		Settable: map[string][]string{"priority": taskPriorities, "technician_id": nil}},
	"item_stock": {Entity: "item", Triggers: []string{"scheduled"},
		View: `SELECT it.id, it.code, it.name, it.unit,
			coalesce((SELECT sum(s.quantity) FROM stock s WHERE s.item_id = it.id), 0)::float8 AS on_hand,
			coalesce((SELECT sum(rr.min_qty) FROM reorder_rules rr WHERE rr.item_id = it.id), 0)::float8 AS min_qty,
			coalesce((SELECT sum(rr.max_qty) FROM reorder_rules rr WHERE rr.item_id = it.id), 0)::float8 AS max_qty,
			(EXISTS (SELECT 1 FROM reorder_rules rr WHERE rr.item_id = it.id AND coalesce((SELECT sum(s.quantity) FROM stock s WHERE s.item_id = it.id AND s.warehouse_id = rr.warehouse_id), 0) < rr.min_qty)) AS below_min,
			NULL::uuid AS assignee_id, NULL::uuid AS creator_id, NULL::uuid AS customer_id, it.code || ' · ' || it.name AS label
			FROM items it WHERE it.id = $1 AND it.company_id = $2`,
		Scan: `SELECT DISTINCT it.id FROM items it JOIN reorder_rules rr ON rr.item_id = it.id WHERE it.company_id = $1 AND it.is_active LIMIT 5000`,
		Fields: []autoField{{"code", "text", nil}, {"name", "text", nil}, {"on_hand", "number", nil}, {"min_qty", "number", nil}, {"max_qty", "number", nil},
			{"below_min", "bool", nil}}},
	"vehicle": {Entity: "vehicle", Triggers: []string{"scheduled"},
		View: `SELECT v.id, v.name, v.plate, v.odometer::float8 AS odometer, v.license_expiry, v.insurance_expiry, v.inspection_expiry,
			(v.license_expiry - current_date) AS license_days, (v.insurance_expiry - current_date) AS insurance_days, (v.inspection_expiry - current_date) AS inspection_days,
			least(v.license_expiry, v.insurance_expiry, v.inspection_expiry) - current_date AS min_doc_days,
			EXISTS (SELECT 1 FROM fleet_service_plans sp WHERE sp.vehicle_id = v.id AND sp.is_active AND
				((sp.every_km IS NOT NULL AND v.odometer >= coalesce(sp.last_km, 0) + sp.every_km)
				 OR (sp.every_days IS NOT NULL AND current_date >= coalesce(sp.last_date, sp.created_at::date) + sp.every_days))) AS service_due,
			v.driver_id AS assignee_id, NULL::uuid AS creator_id, NULL::uuid AS customer_id, v.name || coalesce(' · ' || nullif(v.plate, ''), '') AS label
			FROM vehicles v WHERE v.id = $1 AND v.company_id = $2`,
		Scan: `SELECT id FROM vehicles WHERE company_id = $1 AND is_active LIMIT 2000`,
		Fields: []autoField{{"name", "text", nil}, {"plate", "text", nil}, {"odometer", "number", nil}, {"license_days", "number", nil},
			{"insurance_days", "number", nil}, {"inspection_days", "number", nil}, {"min_doc_days", "number", nil}, {"service_due", "bool", nil}}},
}

var autoOps = []string{"eq", "ne", "gt", "gte", "lt", "lte", "contains", "in", "empty", "not_empty", "changed"}
var autoActions = []string{"activity", "message", "notify", "set_field", "tag", "webhook", "purchase_request"}

type autoCond struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type autoAction struct {
	Type      string `json:"type"`
	Summary   string `json:"summary,omitempty"`    // activity
	Kind      string `json:"kind,omitempty"`       // activity kind
	Days      int    `json:"days,omitempty"`       // activity due in N days
	Assign    string `json:"assign,omitempty"`     // activity / notify: assignee | collector | creator | <user id>
	Channel   string `json:"channel,omitempty"`    // message: whatsapp | sms | email
	Body      string `json:"body,omitempty"`       // message / notify text, with {field} placeholders
	Field     string `json:"field,omitempty"`      // set_field
	Value     string `json:"value,omitempty"`      // set_field
	Tag       string `json:"tag,omitempty"`        // tag
	WebhookID string `json:"webhook_id,omitempty"` // webhook
}

type autoRule struct {
	ID, Company, Name, Model, Trigger string
	Watch                             []string
	Conds                             []autoCond
	Actions                           []autoAction
	Repeat                            int
	Creator                           *string
}

// validateRule checks the model, trigger, conditions and actions (users / webhooks are checked in the transaction).
func validateRule(model, trigger string, conds []autoCond, acts []autoAction) error {
	m, ok := autoModels[model]
	if !ok || !contains(m.Triggers, trigger) {
		return badRequest("invalid_trigger")
	}
	fields := map[string]autoField{}
	for _, f := range m.Fields {
		fields[f.Name] = f
	}
	if len(conds) > 20 || len(acts) == 0 || len(acts) > 10 {
		return badRequest("bad_request")
	}
	for _, c := range conds {
		if _, ok := fields[c.Field]; !ok || !contains(autoOps, c.Op) || len(c.Value) > 200 || (c.Op == "changed" && trigger != "updated") {
			return badRequest("invalid_condition")
		}
	}
	for _, a := range acts {
		if !contains(autoActions, a.Type) || len(a.Body) > 2000 || len(a.Summary) > 300 || a.Days < 0 || a.Days > 365 {
			return badRequest("invalid_action")
		}
		switch a.Type {
		case "activity":
			if strings.TrimSpace(a.Summary) == "" || (a.Kind != "" && !recActivityKinds[a.Kind]) || m.Entity == "" {
				return badRequest("invalid_action")
			}
		case "message":
			if strings.TrimSpace(a.Body) == "" || (a.Channel != "" && !contains([]string{"whatsapp", "sms", "email"}, a.Channel)) || model == "item_stock" || model == "vehicle" {
				return badRequest("invalid_action")
			}
		case "notify":
			if strings.TrimSpace(a.Body) == "" {
				return badRequest("invalid_action")
			}
		case "set_field":
			vals, ok := m.Settable[a.Field]
			if !ok || (vals != nil && !contains(vals, a.Value)) || (vals == nil && a.Value != "" && !isUUID(a.Value)) {
				return badRequest("invalid_action")
			}
		case "tag":
			if t := strings.TrimSpace(a.Tag); t == "" || len(t) > 40 || m.Entity == "" {
				return badRequest("invalid_action")
			}
		case "webhook":
			if !isUUID(a.WebhookID) {
				return badRequest("invalid_action")
			}
		case "purchase_request":
			if model != "item_stock" {
				return badRequest("invalid_action")
			}
		}
		if a.Assign != "" && a.Assign != "assignee" && a.Assign != "collector" && a.Assign != "creator" && !isUUID(a.Assign) {
			return badRequest("invalid_action")
		}
	}
	return nil
}

// ---------- evaluation ----------

func autoValue(rec map[string]any, field string) (string, bool) {
	v, ok := rec[field]
	if !ok || v == nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	case json.Number:
		return x.String(), true
	}
	b, _ := json.Marshal(v)
	return string(b), true
}

func autoMatch(rec map[string]any, changed []string, c autoCond) bool {
	v, has := autoValue(rec, c.Field)
	switch c.Op {
	case "empty":
		return !has || v == ""
	case "not_empty":
		return has && v != ""
	case "changed":
		return contains(changed, c.Field) || (c.Field == "assignee_id" && (contains(changed, "assigned_to") || contains(changed, "technician_id")))
	case "contains":
		return has && strings.Contains(strings.ToLower(v), strings.ToLower(c.Value))
	case "in":
		for _, x := range strings.Split(c.Value, ",") {
			if strings.EqualFold(strings.TrimSpace(x), v) {
				return true
			}
		}
		return false
	}
	if !has {
		return c.Op == "ne"
	}
	// numbers compare as numbers, everything else as text (ISO dates sort as text)
	a, e1 := decimal.NewFromString(v)
	b, e2 := decimal.NewFromString(c.Value)
	cmp := 0
	if e1 == nil && e2 == nil {
		cmp = a.Cmp(b)
	} else {
		v10, c10 := v, c.Value
		if len(v10) > 10 && len(c10) == 10 && v10[4] == '-' {
			v10 = v10[:10] // timestamp vs date
		}
		cmp = strings.Compare(strings.ToLower(v10), strings.ToLower(c10))
	}
	switch c.Op {
	case "eq":
		return cmp == 0
	case "ne":
		return cmp != 0
	case "gt":
		return cmp > 0
	case "gte":
		return cmp >= 0
	case "lt":
		return cmp < 0
	case "lte":
		return cmp <= 0
	}
	return false
}

var autoPlaceholderRe = regexp.MustCompile(`\{([a-z_]{1,40})\}`)

func autoFill(text string, rec map[string]any) string {
	return autoPlaceholderRe.ReplaceAllStringFunc(text, func(m string) string {
		v, ok := autoValue(rec, m[1:len(m)-1])
		if !ok {
			return m
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil && strings.Contains(v, ".") {
			return strconv.FormatFloat(f, 'f', 2, 64)
		}
		return v
	})
}

func autoRecord(ctx context.Context, tx pgx.Tx, model, cid, id string) (map[string]any, error) {
	m := autoModels[model]
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT row_to_json(q) FROM (`+m.View+`) q`, id, cid).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rec := map[string]any{}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func ruleMatches(rule *autoRule, rec map[string]any, changed []string) bool {
	if rule.Trigger == "updated" && len(rule.Watch) > 0 {
		hit := false
		for _, f := range rule.Watch {
			if contains(changed, f) {
				hit = true
			}
		}
		if !hit {
			return false
		}
	}
	for _, c := range rule.Conds {
		if !autoMatch(rec, changed, c) {
			return false
		}
	}
	return true
}

// ---------- actions ----------

func autoUser(rule *autoRule, rec map[string]any, who string) string {
	switch who {
	case "", "assignee":
		if v, ok := autoValue(rec, "assignee_id"); ok && isUUID(v) {
			return v
		}
	case "collector":
		if v, ok := autoValue(rec, "assignee_id"); ok && isUUID(v) && rule.Model != "ticket" {
			return v
		}
	case "creator":
	default:
		if isUUID(who) {
			return who
		}
	}
	if v, ok := autoValue(rec, "creator_id"); ok && isUUID(v) && who != "creator" {
		return v
	}
	if rule.Creator != nil {
		return *rule.Creator
	}
	return ""
}

// runActions performs the rule's actions on one record; it returns what was done (for the run log).
func runActions(ctx context.Context, tx pgx.Tx, rule *autoRule, rec map[string]any, dry bool) ([]map[string]any, error) {
	m := autoModels[rule.Model]
	cid := rule.Company
	id, _ := autoValue(rec, "id")
	label, _ := autoValue(rec, "label")
	out := []map[string]any{}
	for _, a := range rule.Actions {
		res := map[string]any{"type": a.Type}
		switch a.Type {
		case "activity":
			who := autoUser(rule, rec, a.Assign)
			summary := autoFill(a.Summary, rec)
			if len(summary) > 300 {
				summary = summary[:300]
			}
			res["summary"], res["user"] = summary, who
			if who == "" {
				res["skipped"] = "no_user"
				break
			}
			if !dry {
				kind := a.Kind
				if kind == "" {
					kind = "todo"
				}
				var aid string
				if err := tx.QueryRow(ctx, `INSERT INTO record_activities (company_id, entity_type, entity_id, kind, summary, note, due_date, assigned_to, created_by)
					VALUES ($1, $2, $3, $4, $5, $6, current_date + $7::int, $8, NULL) RETURNING id::text`, cid, m.Entity, id, kind, summary,
					"automation: "+rule.Name, a.Days, who).Scan(&aid); err != nil {
					return out, err
				}
				if err := notifyUsers(ctx, tx, cid, []string{who}, "automation", label+" · "+summary, rule.Name, m.Entity, id); err != nil {
					return out, err
				}
			}
		case "notify":
			who := autoUser(rule, rec, a.Assign)
			text := autoFill(a.Body, rec)
			res["user"], res["text"] = who, text
			if who == "" {
				res["skipped"] = "no_user"
				break
			}
			if !dry {
				var et, eid string
				if m.Entity != "" {
					et, eid = m.Entity, id
				}
				if err := notifyUsers(ctx, tx, cid, []string{who}, "automation", label, text, et, eid); err != nil {
					return out, err
				}
			}
		case "message":
			cust, _ := autoValue(rec, "customer_id")
			body := autoFill(a.Body, rec)
			res["text"] = body
			if !isUUID(cust) {
				res["skipped"] = "no_customer"
				break
			}
			if !dry {
				sent, err := autoMessage2(ctx, tx, cid, cust, body, a.Channel)
				if err != nil {
					return out, err
				}
				if !sent {
					res["skipped"] = "not_reachable"
				}
			}
		case "set_field":
			res["field"], res["value"] = a.Field, a.Value
			if !dry {
				table := map[string]string{"ticket": "tickets", "task": "project_tasks", "service_job": "service_jobs"}[rule.Model]
				if table == "" {
					res["skipped"] = "not_settable"
					break
				}
				if m.Settable[a.Field] == nil && a.Value != "" {
					if err := checkCompanyUser(ctx, tx, cid, a.Value); err != nil {
						res["skipped"] = "invalid_user"
						break
					}
				}
				if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = nullif($3, '')::%s, updated_at = now() WHERE id = $1 AND company_id = $2`,
					table, a.Field, map[bool]string{true: "uuid", false: "text"}[m.Settable[a.Field] == nil]), id, cid, a.Value); err != nil {
					return out, err
				}
			}
		case "tag":
			tag := strings.TrimSpace(autoFill(a.Tag, rec))
			res["tag"] = tag
			if !dry {
				if _, err := tx.Exec(ctx, `INSERT INTO record_tags (company_id, entity_type, entity_id, tag) VALUES ($1, $2, $3, left($4, 40)) ON CONFLICT DO NOTHING`,
					cid, m.Entity, id, tag); err != nil {
					return out, err
				}
			}
		case "webhook":
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM webhooks WHERE id = $1 AND company_id = $2 AND is_active)`, a.WebhookID, cid).Scan(&ok); err != nil {
				return out, err
			}
			if !ok {
				res["skipped"] = "no_webhook"
				break
			}
			if !dry {
				payload, _ := json.Marshal(map[string]any{"event": "automation." + rule.Model, "rule": map[string]string{"id": rule.ID, "name": rule.Name},
					"record": rec, "at": time.Now().UTC().Format(time.RFC3339)})
				if _, err := tx.Exec(ctx, `INSERT INTO webhook_deliveries (company_id, webhook_id, event, payload) VALUES ($1, $2, $3, $4::jsonb)`,
					cid, a.WebhookID, "automation."+rule.Model, string(payload)); err != nil {
					return out, err
				}
			}
		case "purchase_request":
			if dry {
				break
			}
			po, err := autoPurchaseRequest(ctx, tx, cid, id)
			if err != nil {
				return out, err
			}
			if po == "" {
				res["skipped"] = "no_supplier_or_enough"
			} else {
				res["purchase_order_id"] = po
			}
		}
		out = append(out, res)
	}
	return out, nil
}

// autoMessage2 queues one message to the customer (default WhatsApp); false when the customer cannot be reached.
func autoMessage2(ctx context.Context, tx pgx.Tx, cid, customer, body, channel string) (bool, error) {
	var lang string
	if err := tx.QueryRow(ctx, `SELECT coalesce(c.locale, co.default_locale, 'en') FROM customers c JOIN companies co ON co.id = c.company_id WHERE c.id = $1`, customer).Scan(&lang); err != nil {
		return false, nil
	}
	rd, err := newRenderer(ctx, tx, cid, lang)
	if err != nil {
		return false, err
	}
	m, err := rd.customerContext(ctx, tx, customer, nil, nil)
	if err != nil {
		return false, nil
	}
	prov, err := pickProvider(ctx, tx, cid, "")
	if err != nil || prov == nil {
		return false, nil
	}
	if channel == "" {
		channel = "whatsapp"
	}
	body = strings.NewReplacer("{", "(", "}", ")").Replace(body)
	if _, _, _, err := queueMessage(ctx, tx, rd, m, "custom", nil, body, prov, channel); err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// autoPurchaseRequest: a request for quotation for an item below its reorder minimum (to its maximum, minus what
// is already on order), from the reorder rule's supplier or the cheapest supplier price. "" when nothing to do.
func autoPurchaseRequest(ctx context.Context, tx pgx.Tx, cid, item string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT rr.warehouse_id::text, rr.min_qty::text, rr.max_qty::text, coalesce(rr.supplier_id::text, ''),
		coalesce((SELECT sum(s.quantity) FROM stock s WHERE s.item_id = rr.item_id AND s.warehouse_id = rr.warehouse_id), 0)::text,
		coalesce((SELECT sum(greatest(l.quantity - l.billed_qty, 0)) FROM purchase_order_lines l JOIN purchase_orders o ON o.id = l.order_id
			WHERE l.item_id = rr.item_id AND coalesce(l.warehouse_id, rr.warehouse_id) = rr.warehouse_id AND o.status IN ('rfq', 'sent', 'confirmed', 'partial')), 0)::text
		FROM reorder_rules rr WHERE rr.item_id = $1 AND rr.company_id = $2`, item, cid)
	if err != nil {
		return "", err
	}
	type need struct {
		wh, sup string
		qty     decimal.Decimal
	}
	var needs []need
	for rows.Next() {
		var wh, mn, mx, sup, have, incoming string
		if err := rows.Scan(&wh, &mn, &mx, &sup, &have, &incoming); err != nil {
			rows.Close()
			return "", err
		}
		d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
		proj := d(have).Add(d(incoming))
		if proj.LessThan(d(mn)) {
			needs = append(needs, need{wh: wh, sup: sup, qty: d(mx).Sub(proj)})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(needs) == 0 {
		return "", nil
	}
	n0 := needs[0]
	sup := n0.sup
	if sup == "" {
		_ = tx.QueryRow(ctx, `SELECT sp.supplier_id::text FROM supplier_prices sp JOIN suppliers su ON su.id = sp.supplier_id WHERE sp.company_id = $1 AND sp.item_id = $2
			AND su.status = 'active' ORDER BY sp.price LIMIT 1`, cid, item).Scan(&sup)
	}
	if sup == "" {
		return "", nil
	}
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return "", err
	}
	var cur string
	if err := tx.QueryRow(ctx, `SELECT coalesce(currency::text, $2) FROM suppliers WHERE id = $1`, sup, set.Base).Scan(&cur); err != nil {
		return "", err
	}
	var n int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'purchase_order')`, cid).Scan(&n); err != nil {
		return "", err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO purchase_orders (company_id, number, supplier_id, order_date, currency, notes)
		VALUES ($1, $2, $3, current_date, $4, 'Automation: below minimum') RETURNING id::text`, cid, n, sup, cur).Scan(&id); err != nil {
		return "", err
	}
	lines := []poLineIn{}
	for _, x := range needs {
		lines = append(lines, poLineIn{ItemID: item, WarehouseID: x.wh, Quantity: x.qty.Round(4).String()})
	}
	if err := writePOLines(ctx, tx, cid, id, cur, lines); err != nil {
		return "", err
	}
	return id, nil
}

// ---------- running ----------

func loadRules(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]*autoRule, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, company_id::text, name, model, trigger, watch_fields, conditions::text, actions::text, repeat_days, created_by::text
		FROM automation_rules WHERE `+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*autoRule
	for rows.Next() {
		var r autoRule
		var conds, acts string
		if err := rows.Scan(&r.ID, &r.Company, &r.Name, &r.Model, &r.Trigger, &r.Watch, &conds, &acts, &r.Repeat, &r.Creator); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(conds), &r.Conds)
		_ = json.Unmarshal([]byte(acts), &r.Actions)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// fire runs one rule on one record inside a savepoint and logs the run. It returns false when the rule hit its
// hourly limit (it is then paused).
func fire(ctx context.Context, tx pgx.Tx, rule *autoRule, rec map[string]any) (bool, error) {
	var recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM automation_runs WHERE rule_id = $1 AND status = 'done' AND created_at > now() - interval '1 hour'`, rule.ID).Scan(&recent); err != nil {
		return false, err
	}
	if recent >= automationMaxPerHour {
		if _, err := tx.Exec(ctx, `UPDATE automation_rules SET is_active = false, paused_reason = 'rate_limit', updated_at = now() WHERE id = $1`, rule.ID); err != nil {
			return false, err
		}
		if rule.Creator != nil {
			_ = notifyUsers(ctx, tx, rule.Company, []string{*rule.Creator}, "automation", rule.Name, "paused: more than 300 runs in an hour", "", "")
		}
		return false, nil
	}
	id, _ := autoValue(rec, "id")
	label, _ := autoValue(rec, "label")
	status, detail := "done", map[string]any{}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	done, aerr := runActions(ctx, sp, rule, rec, false)
	if aerr != nil {
		_ = sp.Rollback(ctx)
		status = "error"
		msg := aerr.Error()
		var ae *apiError
		if errors.As(aerr, &ae) {
			msg = ae.code
		}
		detail["error"] = msg
	} else if err := sp.Commit(ctx); err != nil {
		return false, err
	}
	detail["actions"] = done
	dj, _ := json.Marshal(detail)
	if _, err := tx.Exec(ctx, `INSERT INTO automation_runs (company_id, rule_id, record_id, record_label, status, detail) VALUES ($1, $2, $3, left($4, 300), $5, $6::jsonb)`,
		rule.Company, rule.ID, nilIfBlankStr(id), label, status, string(dj)); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE automation_rules SET run_count = run_count + 1, last_run_at = now() WHERE id = $1`, rule.ID)
	return true, err
}

func nilIfBlankStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// runScheduled evaluates one scheduled rule on its candidates (once per record, or again after repeat_days).
func runScheduled(ctx context.Context, tx pgx.Tx, rule *autoRule) (int, error) {
	m := autoModels[rule.Model]
	rows, err := tx.Query(ctx, m.Scan, rule.Company)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
		var s string
		err := r.Scan(&s)
		return s, err
	})
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, id := range ids {
		var seen bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM automation_runs WHERE rule_id = $1 AND record_id = $2 AND status IN ('done', 'error')
			AND ($3 = 0 OR created_at > now() - make_interval(days => $3)))`, rule.ID, id, rule.Repeat).Scan(&seen); err != nil {
			return fired, err
		}
		if seen {
			continue
		}
		rec, err := autoRecord(ctx, tx, rule.Model, rule.Company, id)
		if err != nil {
			return fired, err
		}
		if rec == nil || !ruleMatches(rule, rec, nil) {
			continue
		}
		ok, err := fire(ctx, tx, rule, rec)
		if err != nil {
			return fired, err
		}
		if !ok {
			break
		}
		fired++
		if fired >= 500 {
			break
		}
	}
	return fired, nil
}

// runEvents processes the queued created / updated events of the current tenant.
func runEvents(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id, company_id::text, model, event, record_id::text, changed FROM automation_events
		WHERE processed_at IS NULL ORDER BY id LIMIT 300 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type ev struct {
		id                        int64
		cid, model, event, record string
		changed                   []string
	}
	var evs []ev
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.id, &e.cid, &e.model, &e.event, &e.record, &e.changed); err != nil {
			rows.Close()
			return 0, err
		}
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	fired := 0
	cache := map[string][]*autoRule{}
	for _, e := range evs {
		key := e.cid + "|" + e.model + "|" + e.event
		rules, ok := cache[key]
		if !ok {
			rules, err = loadRules(ctx, tx, `company_id = $1 AND model = $2 AND trigger = $3 AND is_active`, e.cid, e.model, e.event)
			if err != nil {
				return fired, err
			}
			cache[key] = rules
		}
		if len(rules) > 0 {
			rec, err := autoRecord(ctx, tx, e.model, e.cid, e.record)
			if err != nil {
				return fired, err
			}
			for _, rule := range rules {
				if rec == nil || !ruleMatches(rule, rec, e.changed) {
					continue
				}
				ok, err := fire(ctx, tx, rule, rec)
				if err != nil {
					return fired, err
				}
				if ok {
					fired++
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE automation_events SET processed_at = now() WHERE id = $1`, e.id); err != nil {
			return fired, err
		}
	}
	// keep the queue small
	if _, err := tx.Exec(ctx, `DELETE FROM automation_events WHERE processed_at < now() - interval '7 days'`); err != nil {
		return fired, err
	}
	return fired, nil
}

// inAutomationTx runs fn for one tenant as the application role with the loop guard on.
func (s *Server) inAutomationTx(ctx context.Context, tenant string, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true), set_config('paxve.automation', 'on', true)`, tenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
			return err
		}
		return fn(tx)
	})
}

// RunAutomations processes queued events (and, when scheduled is true, the scheduled rules and SLA escalations).
func (s *Server) RunAutomations(ctx context.Context, scheduled bool) (int, error) {
	if s.OwnerPool == nil {
		return 0, nil
	}
	total := 0
	rows, err := s.OwnerPool.Query(ctx, `SELECT DISTINCT tenant_id::text FROM automation_events WHERE processed_at IS NULL AND tenant_jobs_ok(tenant_id)`)
	if err != nil {
		return 0, err
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, t := range tenants {
		err := s.inAutomationTx(ctx, t, func(tx pgx.Tx) error {
			n, err := runEvents(ctx, tx)
			total += n
			return err
		})
		if err != nil {
			slog.Error("automation events", "tenant", t, "err", err)
		}
	}
	if !scheduled {
		return total, nil
	}
	rows, err = s.OwnerPool.Query(ctx, `SELECT DISTINCT r.tenant_id::text FROM automation_rules r JOIN companies c ON c.id = r.company_id
		WHERE r.is_active AND r.trigger = 'scheduled' AND c.is_active AND tenant_jobs_ok(r.tenant_id)`)
	if err != nil {
		return total, err
	}
	tenants, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return total, err
	}
	for _, t := range tenants {
		err := s.inAutomationTx(ctx, t, func(tx pgx.Tx) error {
			rules, err := loadRules(ctx, tx, `trigger = 'scheduled' AND is_active`)
			if err != nil {
				return err
			}
			for _, rule := range rules {
				n, err := runScheduled(ctx, tx, rule)
				total += n
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			slog.Error("automation schedule", "tenant", t, "err", err)
		}
	}
	if err := s.RunSLAEscalations(ctx); err != nil {
		slog.Error("sla escalations", "err", err)
	}
	return total, nil
}

// StartAutomationWorker: events every 20 seconds, scheduled rules and SLA escalations every 10 minutes.
func (s *Server) StartAutomationWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	go func() {
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		last := time.Now().Add(-9 * time.Minute)
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				sched := time.Since(last) >= 10*time.Minute
				if sched {
					last = time.Now()
				}
				if _, err := s.RunAutomations(ctx, sched); err != nil {
					slog.Error("automation worker", "err", err)
				}
			}
		}
	}()
}

// ======================= API =======================

const ruleCols = `r.id, r.name, r.model, r.trigger, r.watch_fields, r.conditions, r.actions, r.repeat_days, r.template_key, r.is_active, r.paused_reason,
	r.run_count, r.last_run_at, r.created_at, u.full_name AS created_by_name,
	(SELECT count(*) FROM automation_runs x WHERE x.rule_id = r.id AND x.status = 'error' AND x.created_at > now() - interval '7 days') AS errors_7d`

// GET /automation/meta: models with their fields, triggers, operators, actions and the templates.
func (s *Server) autoMeta(w http.ResponseWriter, r *http.Request) {
	models := map[string]any{}
	for k, m := range autoModels {
		settable := map[string][]string{}
		for f, v := range m.Settable {
			if v == nil {
				v = []string{}
			}
			settable[f] = v
		}
		models[k] = map[string]any{"fields": m.Fields, "triggers": m.Triggers, "settable": settable, "entity": m.Entity}
	}
	tpls := []map[string]any{}
	for _, t := range autoTemplates {
		tpls = append(tpls, map[string]any{"key": t.Key, "model": t.Model, "trigger": t.Trigger})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "ops": autoOps, "actions": autoActions, "templates": tpls})
}

func (s *Server) autoRules(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+ruleCols+` FROM automation_rules r LEFT JOIN users u ON u.id = r.created_by WHERE r.company_id = $1
			ORDER BY r.is_active DESC, r.created_at`, r.PathValue("cid"))
	})
}

func (s *Server) autoGetRule(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rule, err := queryJSONOne(ctx, tx, `SELECT `+ruleCols+` FROM automation_rules r LEFT JOIN users u ON u.id = r.created_by WHERE r.id = $1 AND r.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		runs, err := queryJSON(ctx, tx, `SELECT id, record_id, record_label, status, detail, created_at FROM automation_runs WHERE rule_id = $1 ORDER BY created_at DESC LIMIT 200`, id)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rule": rule, "runs": runs}, nil
	})
}

type ruleIn struct {
	Name        *string       `json:"name"`
	Model       *string       `json:"model"`
	Trigger     *string       `json:"trigger"`
	WatchFields *[]string     `json:"watch_fields"`
	Conditions  *[]autoCond   `json:"conditions"`
	Actions     *[]autoAction `json:"actions"`
	RepeatDays  *int          `json:"repeat_days"`
	IsActive    *bool         `json:"is_active"`
}

// checkRuleRefs: users and webhooks named by the actions belong to the company.
func checkRuleRefs(ctx context.Context, tx pgx.Tx, cid string, acts []autoAction) error {
	for _, a := range acts {
		if isUUID(a.Assign) {
			if err := checkCompanyUser(ctx, tx, cid, a.Assign); err != nil {
				return err
			}
		}
		if a.Type == "set_field" && isUUID(a.Value) {
			if err := checkCompanyUser(ctx, tx, cid, a.Value); err != nil {
				return err
			}
		}
		if a.Type == "webhook" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM webhooks WHERE id = $1 AND company_id = $2)`, a.WebhookID, cid).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return badRequest("invalid_action")
			}
		}
	}
	return nil
}

func saveRuleTx(ctx context.Context, tx pgx.Tx, cid, id string, in *ruleIn, template string) (string, error) {
	if id == "" {
		if in.Name == nil || in.Model == nil || in.Trigger == nil || in.Actions == nil {
			return "", badRequest("bad_request")
		}
		conds := []autoCond{}
		if in.Conditions != nil {
			conds = *in.Conditions
		}
		if err := validateRule(*in.Model, *in.Trigger, conds, *in.Actions); err != nil {
			return "", err
		}
		if err := checkRuleRefs(ctx, tx, cid, *in.Actions); err != nil {
			return "", err
		}
		watch := []string{}
		if in.WatchFields != nil {
			watch = *in.WatchFields
		}
		cj, _ := json.Marshal(conds)
		aj, _ := json.Marshal(*in.Actions)
		rep := 0
		if in.RepeatDays != nil {
			rep = *in.RepeatDays
		}
		active := true
		if in.IsActive != nil {
			active = *in.IsActive
		}
		err := tx.QueryRow(ctx, `INSERT INTO automation_rules (company_id, name, model, trigger, watch_fields, conditions, actions, repeat_days, template_key, is_active)
			VALUES ($1, trim($2), $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10) RETURNING id::text`,
			cid, *in.Name, *in.Model, *in.Trigger, watch, string(cj), string(aj), rep, template, active).Scan(&id)
		return id, err
	}
	var model, trigger, conds, acts string
	err := tx.QueryRow(ctx, `SELECT model, trigger, conditions::text, actions::text FROM automation_rules WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
		Scan(&model, &trigger, &conds, &acts)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notFound()
	}
	if err != nil {
		return "", err
	}
	if in.Model != nil {
		model = *in.Model
	}
	if in.Trigger != nil {
		trigger = *in.Trigger
	}
	var c []autoCond
	var a []autoAction
	_ = json.Unmarshal([]byte(conds), &c)
	_ = json.Unmarshal([]byte(acts), &a)
	if in.Conditions != nil {
		c = *in.Conditions
	}
	if in.Actions != nil {
		a = *in.Actions
	}
	if c == nil {
		c = []autoCond{}
	}
	if err := validateRule(model, trigger, c, a); err != nil {
		return "", err
	}
	if err := checkRuleRefs(ctx, tx, cid, a); err != nil {
		return "", err
	}
	cj, _ := json.Marshal(c)
	aj, _ := json.Marshal(a)
	_, err = tx.Exec(ctx, `UPDATE automation_rules SET name = coalesce(trim($3), name), model = $4, trigger = $5, watch_fields = coalesce($6, watch_fields),
		conditions = $7::jsonb, actions = $8::jsonb, repeat_days = coalesce($9, repeat_days), is_active = coalesce($10, is_active),
		paused_reason = CASE WHEN $10 THEN '' ELSE paused_reason END, updated_at = now() WHERE id = $1 AND company_id = $2`,
		id, cid, in.Name, model, trigger, in.WatchFields, string(cj), string(aj), in.RepeatDays, in.IsActive)
	return id, err
}

// POST /automation/rules and PATCH /automation/rules/{id}
func (s *Server) autoSaveRule(w http.ResponseWriter, r *http.Request) {
	var in ruleIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if (id != "" && !isUUID(id)) || (in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 160)) ||
		(in.RepeatDays != nil && (*in.RepeatDays < 0 || *in.RepeatDays > 365)) || (in.WatchFields != nil && len(*in.WatchFields) > 20) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.WatchFields != nil {
		for _, f := range *in.WatchFields {
			if !regexp.MustCompile(`^[a-z_]{1,40}$`).MatchString(f) {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
		}
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rid, err := saveRuleTx(ctx, tx, cid, id, &in, "")
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+ruleCols+` FROM automation_rules r LEFT JOIN users u ON u.id = r.created_by WHERE r.id = $1`, rid)
	})
}

// DELETE /automation/rules/{id}
func (s *Server) autoDeleteRule(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM automation_rules WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]string{"deleted": id}, nil
	})
}

func ruleByID(ctx context.Context, tx pgx.Tx, cid, id string) (*autoRule, error) {
	rules, err := loadRules(ctx, tx, `id = $1 AND company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, notFound()
	}
	return rules[0], nil
}

// POST /automation/rules/{id}/test: dry run. Scheduled rules check their current candidates; event rules check the
// latest records of their model. Nothing is changed; the result lists which records match and what would happen.
func (s *Server) autoTestRule(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rule, err := ruleByID(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		m := autoModels[rule.Model]
		var ids []string
		if rule.Trigger == "scheduled" {
			rows, err := tx.Query(ctx, m.Scan, cid)
			if err != nil {
				return nil, err
			}
			ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, err
			}
		} else {
			table := map[string]string{"invoice": "invoices", "ticket": "tickets", "subscription": "subscriptions", "sales_order": "sales_orders",
				"task": "project_tasks", "service_job": "service_jobs"}[rule.Model]
			rows, err := tx.Query(ctx, `SELECT id::text FROM `+table+` WHERE company_id = $1 ORDER BY created_at DESC LIMIT 20`, cid)
			if err != nil {
				return nil, err
			}
			ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, err
			}
		}
		matched := []map[string]any{}
		checked := 0
		for _, rid := range ids {
			if checked >= 500 {
				break
			}
			checked++
			rec, err := autoRecord(ctx, tx, rule.Model, cid, rid)
			if err != nil {
				return nil, err
			}
			if rec == nil {
				continue
			}
			ok := true
			for _, c := range rule.Conds {
				if c.Op == "changed" {
					continue // cannot be known in a dry run
				}
				if !autoMatch(rec, nil, c) {
					ok = false
				}
			}
			if !ok {
				continue
			}
			acts, err := runActions(ctx, tx, rule, rec, true)
			if err != nil {
				return nil, err
			}
			label, _ := autoValue(rec, "label")
			if len(matched) < 50 {
				matched = append(matched, map[string]any{"record_id": rid, "label": label, "actions": acts})
			}
		}
		dj, _ := json.Marshal(map[string]any{"checked": checked, "matched": len(matched)})
		if _, err := tx.Exec(ctx, `INSERT INTO automation_runs (company_id, rule_id, status, record_label, detail) VALUES ($1, $2, 'dry', 'test', $3::jsonb)`, cid, id, string(dj)); err != nil {
			return nil, err
		}
		return map[string]any{"checked": checked, "matched": matched}, nil
	})
}

// POST /automation/rules/{id}/run: runs a scheduled rule now (as the worker would).
func (s *Server) autoRunRule(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rule, err := ruleByID(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if rule.Trigger != "scheduled" {
			return nil, &apiError{status: http.StatusConflict, code: "not_scheduled"}
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('paxve.automation', 'on', true)`); err != nil {
			return nil, err
		}
		n, err := runScheduled(ctx, tx, rule)
		if err != nil {
			return nil, err
		}
		return map[string]int{"fired": n}, nil
	})
}

// GET /automation/runs?rule_id=&status=
func (s *Server) autoRuns(w http.ResponseWriter, r *http.Request) {
	rule, st := r.URL.Query().Get("rule_id"), r.URL.Query().Get("status")
	if (rule != "" && !isUUID(rule)) || (st != "" && !contains([]string{"done", "skipped", "error", "dry"}, st)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT x.id, x.rule_id, r.name AS rule_name, r.model, x.record_id, x.record_label, x.status, x.detail, x.created_at
			FROM automation_runs x JOIN automation_rules r ON r.id = x.rule_id WHERE x.company_id = $1
			AND ($2 = '' OR x.rule_id = nullif($2, '')::uuid) AND ($3 = '' OR x.status = $3) ORDER BY x.created_at DESC LIMIT 500`, r.PathValue("cid"), rule, st)
	})
}

// ---------- ready-made rules ----------

type autoTemplate struct {
	Key, Model, Trigger string
	Watch               []string
	Conds               []autoCond
	Repeat              int
	Name                map[string]string
	Actions             func(lang string) []autoAction
}

func tr3(lang, en, ar, fr string) string {
	switch lang {
	case "ar":
		return ar
	case "fr":
		return fr
	}
	return en
}

var autoTemplates = []autoTemplate{
	{Key: "overdue_invoice", Model: "invoice", Trigger: "scheduled", Repeat: 7,
		Conds: []autoCond{{"days_overdue", "gte", "7"}, {"balance", "gt", "0"}},
		Name:  map[string]string{"en": "Overdue invoice: reminder + collector task", "ar": "فاتورة متأخرة: تذكير + مهمة للجابي", "fr": "Facture en retard : rappel + tâche"},
		Actions: func(l string) []autoAction {
			return []autoAction{
				{Type: "message", Channel: "whatsapp", Body: tr3(l, "Dear {customer_name}, invoice #{number} ({balance} {currency}) is {days_overdue} days overdue. Please settle it soon. Thank you.",
					"عزيزنا {customer_name}، الفاتورة رقم {number} بقيمة {balance} {currency} متأخرة {days_overdue} يوماً. نرجو التسديد قريباً. شكراً.",
					"Cher {customer_name}, la facture n° {number} ({balance} {currency}) a {days_overdue} jours de retard. Merci de la régler.")},
				{Type: "activity", Kind: "call", Assign: "collector", Summary: tr3(l, "Collect overdue invoice #{number} · {customer_name}",
					"تحصيل الفاتورة المتأخرة رقم {number} · {customer_name}", "Encaisser la facture n° {number} · {customer_name}")},
			}
		}},
	{Key: "low_stock", Model: "item_stock", Trigger: "scheduled", Repeat: 3,
		Conds: []autoCond{{"below_min", "eq", "true"}},
		Name:  map[string]string{"en": "Low stock: purchase request", "ar": "مخزون منخفض: طلب شراء", "fr": "Stock bas : demande d'achat"},
		Actions: func(l string) []autoAction {
			return []autoAction{{Type: "purchase_request"}}
		}},
	{Key: "ticket_breach", Model: "ticket", Trigger: "scheduled", Repeat: 0,
		Conds: []autoCond{{"sla_state", "eq", "breached"}},
		Name:  map[string]string{"en": "SLA breached: escalate", "ar": "تجاوز مهلة الخدمة: تصعيد", "fr": "SLA dépassé : escalade"},
		Actions: func(l string) []autoAction {
			return []autoAction{
				{Type: "set_field", Field: "priority", Value: "urgent"},
				{Type: "notify", Assign: "assignee", Body: tr3(l, "SLA breached on ticket #{number}: {subject}", "تم تجاوز مهلة الطلب رقم {number}: {subject}",
					"SLA dépassé sur le ticket n° {number} : {subject}")},
				{Type: "tag", Tag: "sla-breach"},
			}
		}},
	{Key: "welcome_subscriber", Model: "subscription", Trigger: "created",
		Name: map[string]string{"en": "New subscriber: welcome message", "ar": "مشترك جديد: رسالة ترحيب", "fr": "Nouvel abonné : message de bienvenue"},
		Actions: func(l string) []autoAction {
			return []autoAction{{Type: "message", Channel: "whatsapp", Body: tr3(l, "Welcome {customer_name}! Your subscription ({plan_name}) is active. Thank you for choosing us.",
				"أهلاً بك {customer_name}! اشتراكك ({plan_name}) أصبح فعّالاً. شكراً لاختيارك لنا.", "Bienvenue {customer_name} ! Votre abonnement ({plan_name}) est actif. Merci de votre confiance.")}}
		}},
	{Key: "task_overdue", Model: "task", Trigger: "scheduled", Repeat: 1,
		Conds: []autoCond{{"days_overdue", "gte", "1"}},
		Name:  map[string]string{"en": "Late task: remind the assignee", "ar": "مهمة متأخرة: تذكير المسؤول", "fr": "Tâche en retard : rappel"},
		Actions: func(l string) []autoAction {
			return []autoAction{{Type: "notify", Assign: "assignee", Body: tr3(l, "Task #{number} {title} ({project_name}) is {days_overdue} days late",
				"المهمة رقم {number} {title} ({project_name}) متأخرة {days_overdue} يوماً", "La tâche n° {number} {title} ({project_name}) a {days_overdue} jours de retard")}}
		}},
	{Key: "big_order", Model: "sales_order", Trigger: "updated", Watch: []string{"status"},
		Conds: []autoCond{{"status", "eq", "confirmed"}, {"total", "gte", "1000"}},
		Name:  map[string]string{"en": "Big order confirmed: tell the manager", "ar": "طلب كبير مؤكد: إبلاغ المدير", "fr": "Grosse commande confirmée : prévenir"},
		Actions: func(l string) []autoAction {
			return []autoAction{
				{Type: "notify", Assign: "creator", Body: tr3(l, "Order #{number} confirmed: {total} {currency} · {customer_name}", "تم تأكيد الطلب رقم {number}: {total} {currency} · {customer_name}",
					"Commande n° {number} confirmée : {total} {currency} · {customer_name}")},
				{Type: "tag", Tag: "big-order"},
			}
		}},
	{Key: "fleet_papers", Model: "vehicle", Trigger: "scheduled", Repeat: 7,
		Conds: []autoCond{{"min_doc_days", "lte", "30"}},
		Name:  map[string]string{"en": "Vehicle papers expiring: reminder", "ar": "أوراق مركبة تنتهي قريباً: تذكير", "fr": "Papiers du véhicule : rappel"},
		Actions: func(l string) []autoAction {
			return []autoAction{{Type: "notify", Assign: "creator", Body: tr3(l, "{label}: licence / insurance / inspection expires in {min_doc_days} days",
				"{label}: الترخيص أو التأمين أو المعاينة تنتهي خلال {min_doc_days} يوماً", "{label} : permis / assurance / contrôle expire dans {min_doc_days} jours")}}
		}},
	{Key: "job_done_invoice", Model: "service_job", Trigger: "updated", Watch: []string{"status"},
		Conds: []autoCond{{"status", "eq", "done"}, {"invoiced", "eq", "false"}},
		Name:  map[string]string{"en": "Job done: invoice it", "ar": "مهمة ميدانية منجزة: أصدر الفاتورة", "fr": "Intervention terminée : facturer"},
		Actions: func(l string) []autoAction {
			return []autoAction{{Type: "activity", Kind: "todo", Assign: "creator", Summary: tr3(l, "Invoice job #{number} · {customer_name}",
				"إصدار فاتورة المهمة رقم {number} · {customer_name}", "Facturer l'intervention n° {number} · {customer_name}")}}
		}},
}

// POST /automation/templates/{key}: creates the ready-made rule (inactive until switched on when ?active=0).
func (s *Server) autoFromTemplate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var tpl *autoTemplate
	for i := range autoTemplates {
		if autoTemplates[i].Key == key {
			tpl = &autoTemplates[i]
		}
	}
	if tpl == nil {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	cid := r.PathValue("cid")
	active := r.URL.Query().Get("active") != "0"
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var lang string
		if err := tx.QueryRow(ctx, `SELECT coalesce(default_locale, 'en') FROM companies WHERE id = $1`, cid).Scan(&lang); err != nil {
			return nil, err
		}
		name := tpl.Name[lang]
		if name == "" {
			name = tpl.Name["en"]
		}
		acts := tpl.Actions(lang)
		conds := append([]autoCond{}, tpl.Conds...)
		watch := append([]string{}, tpl.Watch...)
		in := ruleIn{Name: &name, Model: &tpl.Model, Trigger: &tpl.Trigger, Conditions: &conds, Actions: &acts, RepeatDays: &tpl.Repeat, WatchFields: &watch, IsActive: &active}
		id, err := saveRuleTx(ctx, tx, cid, "", &in, tpl.Key)
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+ruleCols+` FROM automation_rules r LEFT JOIN users u ON u.id = r.created_by WHERE r.id = $1`, id)
	})
}

// GET /record-tags?type=&id= and the tag list of a record type (for filters).
func (s *Server) recordTags(w http.ResponseWriter, r *http.Request) {
	typ, id := r.URL.Query().Get("type"), r.URL.Query().Get("id")
	if (typ != "" && !regexp.MustCompile(`^[a-z_]{2,40}$`).MatchString(typ)) || (id != "" && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if (typ == "" && !can(r, "settings.view")) || (typ != "" && !chatterAllowed(r, typ, false)) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT entity_type, entity_id, tag, created_at FROM record_tags WHERE company_id = $1 AND ($2 = '' OR entity_type = $2)
			AND ($3 = '' OR entity_id = nullif($3, '')::uuid) ORDER BY created_at DESC LIMIT 2000`, r.PathValue("cid"), typ, id)
	})
}
