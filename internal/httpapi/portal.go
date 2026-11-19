package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Customer portal: a secret link per customer (no login) showing their bills and payments,
// plus the staff endpoints to read / regenerate the link.

var portalTokenRe = regexp.MustCompile(`^[0-9a-f]{32,128}$`)

// publicURL is the address customers open (PUBLIC_URL env, default https://paxve.com).
func publicURL() string {
	if u := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"); u != "" {
		return u
	}
	return "https://paxve.com"
}

func portalLink(token string) string { return publicURL() + "/#/p/" + token }

// portalScope resolves a token to its tenant/company/customer (rate limited, 404 for anything invalid).
func (s *Server) portalScope(w http.ResponseWriter, r *http.Request) (db.Scope, string, string, bool) {
	ip := clientIP(r)
	if !s.portalLimiter.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return db.Scope{}, "", "", false
	}
	s.portalLimiter.fail(ip) // every request counts towards the per-IP budget
	token := r.PathValue("token")
	if !portalTokenRe.MatchString(token) {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", "", false
	}
	var tid, cid, customer string
	err := s.Pool.QueryRow(r.Context(), `SELECT tenant_id::text, company_id::text, customer_id::text FROM portal_lookup($1)`, token).
		Scan(&tid, &cid, &customer)
	if err == pgx.ErrNoRows {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", "", false
	}
	if err != nil {
		serverErr(w, r, err)
		return db.Scope{}, "", "", false
	}
	return db.Scope{TenantID: tid, IP: ip, CompanyID: cid}, cid, customer, true
}

func (s *Server) runScoped(w http.ResponseWriter, r *http.Request, sc db.Scope, fn func(ctx context.Context, tx pgx.Tx) (any, error)) {
	var out any
	err := db.InTenant(r.Context(), s.Pool, sc, func(tx pgx.Tx) error {
		var err error
		out, err = fn(r.Context(), tx)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, http.StatusOK, out)
}

const portalInvoiceCols = `i.id, i.number, i.period, i.issue_date, i.due_date, i.currency, i.total::text AS total, i.paid::text AS paid,
	(i.total - i.paid)::text AS balance, i.status, i.local_currency, trim_scale(i.local_rate)::text AS local_rate,
	i.local_currency_total::text AS local_currency_total`

// portal: GET /api/v1/portal/{token} — only safe fields of that one customer.
func (s *Server) portal(w http.ResponseWriter, r *http.Request) {
	sc, cid, customer, ok := s.portalScope(w, r)
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		company, err := queryJSONOne(ctx, tx, `SELECT name_en, name_ar, base_currency, local_currency, logo, doc_header, doc_footer, tax_id
			FROM companies WHERE id = $1`, cid)
		if err != nil {
			return nil, err
		}
		quotes, err := queryJSON(ctx, tx, `SELECT o.id, o.number, o.order_number, o.status, o.quote_date, o.valid_until, o.currency,
			o.total::text AS total, o.accepted_at FROM sales_orders o
			WHERE o.customer_id = $1 AND o.status IN ('sent', 'confirmed', 'done') ORDER BY o.quote_date DESC LIMIT 20`, customer)
		if err != nil {
			return nil, err
		}
		cust, err := queryJSONOne(ctx, tx, `SELECT c.name, c.code, ar.name AS area_name,
			coalesce(c.locale, (SELECT co.default_locale FROM companies co WHERE co.id = c.company_id)) AS locale, `+customerBalances+`
			FROM customers c LEFT JOIN areas ar ON ar.id = c.area_id WHERE c.id = $1`, customer)
		if err != nil {
			return nil, err
		}
		open, err := queryJSON(ctx, tx, `SELECT `+portalInvoiceCols+` FROM invoices i
			WHERE i.customer_id = $1 AND i.status IN ('open', 'partial') ORDER BY i.issue_date, i.number`, customer)
		if err != nil {
			return nil, err
		}
		paid, err := queryJSON(ctx, tx, `SELECT `+portalInvoiceCols+` FROM invoices i
			WHERE i.customer_id = $1 AND i.status = 'paid' ORDER BY i.issue_date DESC, i.number DESC LIMIT 12`, customer)
		if err != nil {
			return nil, err
		}
		pays, err := queryJSON(ctx, tx, `SELECT p.number, p.payment_date, p.currency, p.amount::text AS amount FROM payments p
			WHERE p.customer_id = $1 AND p.status = 'posted' ORDER BY p.payment_date DESC, p.number DESC LIMIT 12`, customer)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"company": company, "customer": cust, "open_invoices": open, "paid_invoices": paid, "payments": pays,
			"quotes": quotes}, nil
	})
}

// portalInvoice: GET /api/v1/portal/{token}/invoices/{id} — printable invoice, only if it belongs to that customer.
func (s *Server) portalInvoice(w http.ResponseWriter, r *http.Request) {
	sc, cid, customer, ok := s.portalScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var mine bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM invoices WHERE id = $1 AND customer_id = $2 AND status <> 'void')`,
			id, customer).Scan(&mine); err != nil {
			return nil, err
		}
		if !mine {
			return nil, notFound()
		}
		return loadInvoice(ctx, tx, cid, id)
	})
}

// customerPortal: GET (read) or POST (regenerate) /companies/{cid}/customers/{id}/portal.
func (s *Server) customerPortal(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	regenerate := r.Method == http.MethodPost
	if regenerate && isCollector(r) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var token string
		sql := `SELECT portal_token FROM customers c WHERE c.id = $1 AND c.company_id = $2
			AND (NOT $3 OR c.area_id IN (SELECT id FROM areas WHERE collector_id = app_user()))`
		if regenerate {
			sql = `UPDATE customers c SET portal_token = replace(gen_random_uuid()::text, '-', '') || replace(gen_random_uuid()::text, '-', '')
				WHERE c.id = $1 AND c.company_id = $2 AND NOT $3 RETURNING portal_token`
		}
		err := tx.QueryRow(ctx, sql, id, cid, isCollector(r)).Scan(&token)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		return map[string]string{"token": token, "url": portalLink(token)}, nil
	})
}

// ---- dashboard ----

// dashboard: everything the home page needs for one company in one call.
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	month := r.URL.Query().Get("month")
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	if !validPeriod(month) {
		writeErr(w, http.StatusBadRequest, "invalid_period")
		return
	}
	start, end := month+"-01", periodEnd(month)
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		kpis, err := queryJSON(ctx, tx, `
			-- the currencies used, by skipping through the (company_id, currency) indexes
			WITH RECURSIVE ic AS (
			    SELECT (SELECT min(currency) FROM invoices WHERE company_id = $1) AS c
			    UNION ALL SELECT (SELECT min(currency) FROM invoices WHERE company_id = $1 AND currency > ic.c) FROM ic WHERE ic.c IS NOT NULL),
			  pc AS (
			    SELECT (SELECT min(currency) FROM payments WHERE company_id = $1) AS c
			    UNION ALL SELECT (SELECT min(currency) FROM payments WHERE company_id = $1 AND currency > pc.c) FROM pc WHERE pc.c IS NOT NULL),
			  cur AS (SELECT c FROM ic WHERE c IS NOT NULL UNION SELECT c FROM pc WHERE c IS NOT NULL)
			SELECT cur.c AS currency,
			  coalesce((SELECT sum(total) FROM invoices WHERE company_id = $1 AND currency = cur.c AND status <> 'void' AND kind = 'invoice'
			            AND issue_date BETWEEN $2::date AND $3::date), 0)::text AS billed,
			  coalesce((SELECT sum(paid) FROM invoices WHERE company_id = $1 AND currency = cur.c AND status <> 'void' AND kind = 'invoice'
			            AND issue_date BETWEEN $2::date AND $3::date), 0)::text AS billed_paid,
			  coalesce((SELECT sum(amount) FROM payments WHERE company_id = $1 AND currency = cur.c AND status = 'posted' AND kind = 'payment'
			            AND payment_date BETWEEN $2::date AND $3::date), 0)::text AS collected,
			  coalesce((SELECT sum(total - paid) FROM invoices WHERE company_id = $1 AND currency = cur.c
			            AND status IN ('open', 'partial')), 0)::text AS outstanding
			FROM cur ORDER BY (cur.c = $4) DESC, cur.c`, cid, start, end, base)
		if err != nil {
			return nil, err
		}
		key := cid + "|" + month
		if hv, ok := dashCache.get(key); ok {
			return map[string]any{"month": month, "base_currency": base, "kpis": kpis, "counts": hv.counts,
				"series": hv.series, "collectors": hv.collectors, "outstanding_by_area": hv.areas, "summary_at": hv.at}, nil
		}
		started := time.Now()
		counts, err := queryJSONOne(ctx, tx, `SELECT
			(SELECT count(*) FROM subscriptions WHERE company_id = $1 AND status = 'active') AS active_subscriptions,
			(SELECT count(*) FROM customers WHERE company_id = $1 AND status = 'active') AS active_customers,
			(SELECT count(DISTINCT customer_id) FROM invoices WHERE company_id = $1 AND status IN ('open', 'partial')) AS customers_owing,
			(SELECT count(DISTINCT customer_id) FROM invoices WHERE company_id = $1 AND status IN ('open', 'partial')
			   AND due_date < current_date - 60) AS customers_late_60,
			(SELECT count(*) FROM invoices WHERE company_id = $1 AND status IN ('open', 'partial')) AS open_invoices`, cid)
		if err != nil {
			return nil, err
		}
		series, err := queryJSON(ctx, tx, `
			WITH g AS (SELECT generate_series(date_trunc('month', $2::date) - interval '11 months', date_trunc('month', $2::date), interval '1 month') AS m),
			b AS (SELECT date_trunc('month', issue_date) AS m, currency AS c, sum(total) AS v FROM invoices
			      WHERE company_id = $1 AND status <> 'void' AND kind = 'invoice' AND issue_date >= date_trunc('month', $2::date) - interval '11 months'
			        AND issue_date <= $3::date GROUP BY 1, 2),
			p AS (SELECT date_trunc('month', payment_date) AS m, currency AS c, sum(amount) AS v FROM payments
			      WHERE company_id = $1 AND status = 'posted' AND kind = 'payment' AND payment_date >= date_trunc('month', $2::date) - interval '11 months'
			        AND payment_date <= $3::date GROUP BY 1, 2),
			cur AS (SELECT c FROM b UNION SELECT c FROM p)
			SELECT to_char(g.m, 'YYYY-MM') AS month, cur.c AS currency, coalesce(b.v, 0)::text AS billed, coalesce(p.v, 0)::text AS collected
			FROM g CROSS JOIN cur LEFT JOIN b ON b.m = g.m AND b.c = cur.c LEFT JOIN p ON p.m = g.m AND p.c = cur.c
			ORDER BY cur.c, g.m`, cid, start, end)
		if err != nil {
			return nil, err
		}
		collectors, err := queryJSON(ctx, tx, `SELECT coalesce(u.full_name, '—') AS name, p.currency, count(*) AS payments,
			sum(p.amount)::text AS amount FROM payments p LEFT JOIN users u ON u.id = p.received_by
			WHERE p.company_id = $1 AND p.status = 'posted' AND p.kind = 'payment' AND p.payment_date BETWEEN $2::date AND $3::date
			GROUP BY u.full_name, p.currency ORDER BY sum(p.amount) DESC LIMIT 20`, cid, start, end)
		if err != nil {
			return nil, err
		}
		areas, err := queryJSON(ctx, tx, `SELECT coalesce(ar.name, '—') AS area, i.currency, sum(i.total - i.paid)::text AS amount,
			count(DISTINCT i.customer_id) AS customers
			FROM invoices i JOIN customers c ON c.id = i.customer_id LEFT JOIN areas ar ON ar.id = c.area_id
			WHERE i.company_id = $1 AND i.status IN ('open', 'partial')
			GROUP BY ar.name, i.currency ORDER BY sum(i.total - i.paid) DESC LIMIT 20`, cid)
		if err != nil {
			return nil, err
		}
		hv := dashHeavy{counts: counts, series: series, collectors: collectors, areas: areas, at: time.Now()}
		if time.Since(started) > 300*time.Millisecond {
			dashCache.put(key, hv) // large company: the charts and counts are reused for a minute
		}
		return map[string]any{"month": month, "base_currency": base, "kpis": kpis, "counts": counts,
			"series": series, "collectors": collectors, "outstanding_by_area": areas, "summary_at": hv.at}, nil
	})
}

// printInvoices: all bills of a period (filters: area, box, station, unpaid) with lines, for bulk printing.
func (s *Server) printInvoices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	period, area, box, station := q.Get("period"), q.Get("area_id"), q.Get("box_id"), q.Get("station_id")
	unpaid := q.Get("unpaid") == "1"
	for _, u := range []string{area, box, station} {
		if u != "" && !isUUID(u) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	if period != "" && !validPeriod(period) {
		writeErr(w, http.StatusBadRequest, "invalid_period")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		company, err := queryJSONOne(ctx, tx, `SELECT name_en, name_ar, base_currency, local_currency FROM companies WHERE id = $1`, cid)
		if err != nil {
			return nil, err
		}
		rows, err := queryJSON(ctx, tx, `
			SELECT `+portalInvoiceCols+`, c.code AS customer_code, c.name AS customer_name, c.phone AS customer_phone,
			       c.address AS customer_address, c.portal_token, ar.name AS area_name, s.meter_no, bx.code AS box_code,
			       (SELECT json_agg(json_build_object('kind', l.kind, 'description', l.description, 'amount', l.amount::text,
			               'unit_price', l.unit_price::text, 'tax_amount', l.tax_amount::text, 'meta', l.meta) ORDER BY l.line_no)
			        FROM invoice_lines l WHERE l.invoice_id = i.id) AS lines,
			       coalesce((SELECT sum(i2.total - i2.paid) FROM invoices i2 WHERE i2.customer_id = i.customer_id
			                 AND i2.currency = i.currency AND i2.status IN ('open', 'partial') AND i2.id <> i.id), 0)::text AS previous_balance
			FROM invoices i JOIN customers c ON c.id = i.customer_id
			LEFT JOIN areas ar ON ar.id = c.area_id LEFT JOIN subscriptions s ON s.id = i.subscription_id
			LEFT JOIN boxes bx ON bx.id = s.box_id
			WHERE i.company_id = $1 AND i.status <> 'void' AND ($2 = '' OR i.period = $2)
			  AND ($3 = '' OR c.area_id = $3::uuid) AND ($4 = '' OR s.box_id = $4::uuid) AND ($5 = '' OR bx.station_id = $5::uuid)
			  AND (NOT $6 OR i.status IN ('open', 'partial'))
			ORDER BY ar.name NULLS LAST, bx.code NULLS LAST, c.name, i.number
			LIMIT 3000`, cid, period, area, box, station, unpaid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"company": company, "rows": rows, "portal_base": publicURL() + "/#/p/"}, nil
	})
}

// dashHeavy: the slow, slowly-changing parts of the dashboard (counts, 12-month charts, rankings).
// For large companies (those parts take > 300 ms) they are kept for a minute per company and month;
// the money figures at the top (kpis) are always read fresh.
type dashHeavy struct {
	counts, series, collectors, areas json.RawMessage
	at                                time.Time
}

type dashStore struct {
	mu sync.Mutex
	m  map[string]dashHeavy
}

var dashCache = &dashStore{m: map[string]dashHeavy{}}

func (d *dashStore) get(key string) (dashHeavy, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.m[key]
	if !ok || time.Since(v.at) > time.Minute {
		return dashHeavy{}, false
	}
	return v, true
}

func (d *dashStore) put(key string, v dashHeavy) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.m) > 5000 {
		d.m = map[string]dashHeavy{}
	}
	d.m[key] = v
}
