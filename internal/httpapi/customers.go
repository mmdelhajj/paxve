package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Balance per currency: due = open invoices, credit = payments not yet applied.
const customerBalances = `
	coalesce((SELECT json_agg(json_build_object('currency', x.currency, 'due', x.due::text) ORDER BY x.currency)
	          FROM (SELECT i.currency, sum(i.total - i.paid) AS due FROM invoices i
	                WHERE i.customer_id = c.id AND i.status IN ('open', 'partial') GROUP BY i.currency) x), '[]'::json) AS due,
	coalesce((SELECT json_agg(json_build_object('currency', y.currency, 'credit', y.credit::text) ORDER BY y.currency)
	          FROM (SELECT p.currency, sum(p.amount - coalesce((SELECT sum(pa.amount) FROM payment_allocations pa WHERE pa.payment_id = p.id), 0)) AS credit
	                FROM payments p WHERE p.customer_id = c.id AND p.status = 'posted' GROUP BY p.currency
	                HAVING sum(p.amount - coalesce((SELECT sum(pa.amount) FROM payment_allocations pa WHERE pa.payment_id = p.id), 0)) > 0) y), '[]'::json) AS credit`

const customerCols = `c.id, c.code, c.name, c.phone, c.phone2, c.email, c.address, c.area_id, ar.name AS area_name,
	c.notes, c.status, c.created_at,
	(SELECT count(*) FROM subscriptions s WHERE s.customer_id = c.id AND s.status = 'active') AS active_subscriptions,
	` + customerBalances

func (s *Server) listCustomers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("q"))
	area := q.Get("area_id")
	status := q.Get("status")
	onlyDue := q.Get("due") == "1"
	if area != "" && !isUUID(area) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	limit, offset := queryInt(r, "limit", 100, 1000), queryInt(r, "offset", 0, 1<<30)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+customerCols+` FROM customers c LEFT JOIN areas ar ON ar.id = c.area_id
			WHERE c.company_id = $1
			  AND ($2 = '' OR c.name ILIKE '%' || $2 || '%' OR c.code ILIKE $2 || '%' OR c.phone LIKE '%' || $2 || '%'
			       OR EXISTS (SELECT 1 FROM subscriptions s WHERE s.customer_id = c.id AND s.meter_no = $2))
			  AND ($3 = '' OR c.area_id = $3::uuid)
			  AND ($4 = '' OR c.status = $4)
			  AND (NOT $5 OR EXISTS (SELECT 1 FROM invoices i WHERE i.customer_id = c.id AND i.status IN ('open', 'partial')))
			  AND (NOT $8 OR c.area_id IN (SELECT id FROM areas WHERE collector_id = app_user()))
			ORDER BY c.name LIMIT $6 OFFSET $7`, r.PathValue("cid"), search, area, status, onlyDue, limit, offset, isCollector(r))
	})
}

type customerIn struct {
	Code    *string `json:"code"`
	Name    *string `json:"name"`
	Phone   *string `json:"phone"`
	Phone2  *string `json:"phone2"`
	Email   *string `json:"email"`
	Address *string `json:"address"`
	AreaID  *string `json:"area_id"`
	Notes   *string `json:"notes"`
	Status  *string `json:"status"`
}

func (in *customerIn) valid(create bool) bool {
	if create && in.Name == nil {
		return false
	}
	return (in.Name == nil || strings.TrimSpace(*in.Name) != "") && optUUID(in.AreaID) &&
		(in.Status == nil || *in.Status == "active" || *in.Status == "suspended" || *in.Status == "closed") &&
		(in.Code == nil || len(strings.TrimSpace(*in.Code)) <= 30)
}

func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request) {
	var in customerIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid(true) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		code := strings.TrimSpace(deref(in.Code))
		if code == "" {
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'customer')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			code = strconv.FormatInt(n, 10)
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO customers (company_id, code, name, phone, phone2, email, address, area_id, notes)
			VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, '')::uuid, $9) RETURNING id::text`,
			cid, code, strings.TrimSpace(*in.Name), strings.TrimSpace(deref(in.Phone)), strings.TrimSpace(deref(in.Phone2)),
			strings.TrimSpace(deref(in.Email)), strings.TrimSpace(deref(in.Address)), deref(in.AreaID), deref(in.Notes)).Scan(&id)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "code_taken"}
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+customerCols+` FROM customers c LEFT JOIN areas ar ON ar.id = c.area_id WHERE c.id = $1`, id)
	})
}

func (s *Server) updateCustomer(w http.ResponseWriter, r *http.Request) {
	var in customerIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !in.valid(false) || !isUUID(id) || (in.Code != nil && strings.TrimSpace(*in.Code) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE customers SET code = coalesce(trim($3), code), name = coalesce(trim($4), name),
			phone = coalesce(trim($5), phone), phone2 = coalesce(trim($6), phone2), email = coalesce(trim($7), email),
			address = coalesce(trim($8), address),
			area_id = CASE WHEN $9::text IS NULL THEN area_id ELSE nullif($9, '')::uuid END,
			notes = coalesce($10, notes), status = coalesce($11, status)
			WHERE id = $1 AND company_id = $2`,
			id, cid, in.Code, in.Name, in.Phone, in.Phone2, in.Email, in.Address, in.AreaID, in.Notes, in.Status)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "code_taken"}
		}
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+customerCols+` FROM customers c LEFT JOIN areas ar ON ar.id = c.area_id WHERE c.id = $1`, id)
	})
}

const subscriptionCols = `s.id, s.customer_id, s.plan_id, p.name AS plan_name, p.billing_type, p.currency,
	p.base_fee::text AS base_fee, p.unit_price::text AS unit_price, p.unit_label, s.meter_no, s.start_date, s.status,
	s.fee_override::text AS fee_override, s.initial_reading::text AS initial_reading, s.notes,
	(SELECT r.reading::text FROM meter_readings r WHERE r.subscription_id = s.id ORDER BY r.period DESC LIMIT 1) AS last_reading,
	(SELECT r.period FROM meter_readings r WHERE r.subscription_id = s.id ORDER BY r.period DESC LIMIT 1) AS last_period`

// getCustomer: profile + subscriptions + recent invoices and payments.
func (s *Server) getCustomer(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cust, err := queryJSONOne(ctx, tx, `SELECT `+customerCols+` FROM customers c LEFT JOIN areas ar ON ar.id = c.area_id
			WHERE c.id = $1 AND c.company_id = $2
			  AND (NOT $3 OR c.area_id IN (SELECT id FROM areas WHERE collector_id = app_user()))`, id, cid, isCollector(r))
		if err != nil {
			return nil, err
		}
		subs, err := queryJSON(ctx, tx, `SELECT `+subscriptionCols+` FROM subscriptions s JOIN plans p ON p.id = s.plan_id
			WHERE s.customer_id = $1 ORDER BY s.status = 'ended', s.start_date`, id)
		if err != nil {
			return nil, err
		}
		invs, err := queryJSON(ctx, tx, `SELECT `+invoiceCols+` FROM invoices i JOIN customers c ON c.id = i.customer_id
			WHERE i.customer_id = $1 ORDER BY i.issue_date DESC, i.number DESC LIMIT 60`, id)
		if err != nil {
			return nil, err
		}
		pays, err := queryJSON(ctx, tx, `SELECT `+paymentCols+` FROM payments p JOIN customers c ON c.id = p.customer_id
			LEFT JOIN users u ON u.id = p.received_by WHERE p.customer_id = $1 ORDER BY p.payment_date DESC, p.number DESC LIMIT 60`, id)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"customer": cust, "subscriptions": subs, "invoices": invs, "payments": pays}, nil
	})
}

type subscriptionIn struct {
	PlanID         *string `json:"plan_id"`
	MeterNo        *string `json:"meter_no"`
	StartDate      *string `json:"start_date"`
	Status         *string `json:"status"`
	FeeOverride    *string `json:"fee_override"` // "" clears
	InitialReading *string `json:"initial_reading"`
	Notes          *string `json:"notes"`
}

func (in *subscriptionIn) valid(create bool) bool {
	if create && in.PlanID == nil {
		return false
	}
	okNum := func(p *string, allowEmpty bool) bool {
		if p == nil || (allowEmpty && *p == "") {
			return true
		}
		_, ok := money(*p)
		return ok
	}
	return optUUID(in.PlanID) && (in.StartDate == nil || validDate(*in.StartDate)) &&
		(in.Status == nil || *in.Status == "active" || *in.Status == "suspended" || *in.Status == "ended") &&
		okNum(in.FeeOverride, true) && okNum(in.InitialReading, false)
}

func checkPlan(ctx context.Context, tx pgx.Tx, cid, plan string) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plans WHERE id = $1 AND company_id = $2 AND is_active)`, plan, cid).Scan(&ok)
	if err == nil && !ok {
		return badRequest("invalid_plan")
	}
	return err
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request) {
	var in subscriptionIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, customer := r.PathValue("cid"), r.PathValue("id")
	if !in.valid(true) || !isUUID(customer) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := lockCustomer(ctx, tx, cid, customer); err != nil {
			return nil, err
		}
		if err := checkPlan(ctx, tx, cid, *in.PlanID); err != nil {
			return nil, err
		}
		start := deref(in.StartDate)
		if start == "" {
			start = today()
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO subscriptions (company_id, customer_id, plan_id, meter_no, start_date, fee_override, initial_reading, notes)
			VALUES ($1, $2, $3, $4, $5::date, nullif($6, '')::numeric, coalesce(nullif($7, '')::numeric, 0), $8) RETURNING id::text`,
			cid, customer, *in.PlanID, strings.TrimSpace(deref(in.MeterNo)), start, deref(in.FeeOverride),
			deref(in.InitialReading), deref(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+subscriptionCols+` FROM subscriptions s JOIN plans p ON p.id = s.plan_id WHERE s.id = $1`, id)
	})
}

func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request) {
	var in subscriptionIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !in.valid(false) || !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.PlanID != nil {
			if err := checkPlan(ctx, tx, cid, *in.PlanID); err != nil {
				return nil, err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE subscriptions SET plan_id = coalesce($3::uuid, plan_id), meter_no = coalesce(trim($4), meter_no),
			start_date = coalesce($5::date, start_date), status = coalesce($6, status),
			fee_override = CASE WHEN $7::text IS NULL THEN fee_override ELSE nullif($7, '')::numeric END,
			initial_reading = coalesce($8::numeric, initial_reading), notes = coalesce($9, notes)
			WHERE id = $1 AND company_id = $2`,
			id, cid, in.PlanID, in.MeterNo, in.StartDate, in.Status, in.FeeOverride, in.InitialReading, in.Notes)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+subscriptionCols+` FROM subscriptions s JOIN plans p ON p.id = s.plan_id WHERE s.id = $1`, id)
	})
}

// ---- meter readings ----

// readingSheet lists active metered subscriptions with previous and current reading for a period.
func (s *Server) readingSheet(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	area := r.URL.Query().Get("area_id")
	if !validPeriod(period) || (area != "" && !isUUID(area)) {
		writeErr(w, http.StatusBadRequest, "invalid_period")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `
			SELECT s.id AS subscription_id, c.id AS customer_id, c.code, c.name, ar.name AS area_name, s.meter_no,
			       p.name AS plan_name, p.unit_label,
			       coalesce((SELECT r.reading FROM meter_readings r WHERE r.subscription_id = s.id AND r.period < $2
			                 ORDER BY r.period DESC LIMIT 1), s.initial_reading)::text AS previous,
			       (SELECT r.reading::text FROM meter_readings r WHERE r.subscription_id = s.id AND r.period = $2) AS current,
			       EXISTS (SELECT 1 FROM invoices i WHERE i.subscription_id = s.id AND i.period = $2 AND i.status <> 'void') AS invoiced
			FROM subscriptions s
			JOIN plans p ON p.id = s.plan_id AND p.billing_type = 'metered'
			JOIN customers c ON c.id = s.customer_id
			LEFT JOIN areas ar ON ar.id = c.area_id
			WHERE s.company_id = $1 AND s.status = 'active' AND c.status = 'active'
			  AND ($3 = '' OR c.area_id = $3::uuid)
			  AND (NOT $4 OR ar.collector_id = app_user())
			ORDER BY ar.name NULLS LAST, c.name`, r.PathValue("cid"), period, area, isCollector(r))
	})
}

func (s *Server) saveReadings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Period   string `json:"period"`
		Readings []struct {
			SubscriptionID string `json:"subscription_id"`
			Reading        string `json:"reading"`
		} `json:"readings"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !validPeriod(in.Period) || len(in.Readings) == 0 || len(in.Readings) > 5000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		saved := 0
		for _, rd := range in.Readings {
			val, ok := money(rd.Reading)
			if !isUUID(rd.SubscriptionID) || !ok {
				return nil, badRequest("bad_request")
			}
			var prevS, meter string
			var invoiced bool
			err := tx.QueryRow(ctx, `
				SELECT coalesce((SELECT r.reading FROM meter_readings r WHERE r.subscription_id = s.id AND r.period < $3
				                 ORDER BY r.period DESC LIMIT 1), s.initial_reading)::text,
				       coalesce(nullif(s.meter_no, ''), c.name),
				       EXISTS (SELECT 1 FROM invoices i WHERE i.subscription_id = s.id AND i.period = $3 AND i.status <> 'void')
				FROM subscriptions s JOIN customers c ON c.id = s.customer_id
				WHERE s.id = $1 AND s.company_id = $2
				  AND (NOT $4 OR c.area_id IN (SELECT id FROM areas WHERE collector_id = app_user()))`, rd.SubscriptionID, cid, in.Period, isCollector(r)).Scan(&prevS, &meter, &invoiced)
			if err == pgx.ErrNoRows {
				return nil, badRequest("bad_request")
			}
			if err != nil {
				return nil, err
			}
			if invoiced {
				return nil, &apiError{status: http.StatusConflict, code: "already_invoiced", detail: meter}
			}
			prev, _ := decimal.NewFromString(prevS)
			if val.LessThan(prev) {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "reading_below_previous", detail: meter}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO meter_readings (subscription_id, period, reading) VALUES ($1, $2, $3::numeric)
				ON CONFLICT (tenant_id, subscription_id, period) DO UPDATE SET reading = EXCLUDED.reading, read_at = now(), read_by = app_user()`,
				rd.SubscriptionID, in.Period, val.String()); err != nil {
				return nil, err
			}
			saved++
		}
		return map[string]int{"saved": saved}, nil
	})
}
