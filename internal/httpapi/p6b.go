package httpapi

// Phase 6b: the owner's phone app (today at a glance), the public marketing site (plans, contact, demo requests →
// platform leads), payroll posting-date option, the smart "price lowered by hand" alert, and the supplier opening
// balances import (one opening supplier bill per row, against the opening equity account).

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

func init() {
	smartKindLabels["price_override"] = [2]string{"Prices lowered by hand", "تخفيض أسعار يدوياً"}
}

// ======================= smart: catalog prices lowered by hand at the till =======================

// checkPriceCuts: a cashier who lowered catalog prices by hand on many lines this week, far above their own history
// (or, without history, at least 3 lines and 3 % of their sales).
func checkPriceCuts(ctx context.Context, tx pgx.Tx, cid string, p smartParams) ([]alertCand, error) {
	rows, err := tx.Query(ctx, `
		SELECT o.cashier_id::text, coalesce(u.full_name, ''), ((current_date - o.order_date) / 7)::int,
		  coalesce(sum(smart_to_base($1, o.currency, o.price_cut)), 0)::float8,
		  coalesce(sum(o.price_cut_lines), 0)::float8,
		  coalesce(sum(smart_to_base($1, o.currency, o.total)), 0)::float8
		FROM pos_orders o LEFT JOIN users u ON u.id = o.cashier_id
		WHERE o.company_id = $1 AND o.kind = 'sale' AND o.order_date > current_date - 91 AND o.order_date <= current_date AND o.cashier_id IS NOT NULL
		GROUP BY 1, 2, 3 ORDER BY 1, 3`, cid)
	if err != nil {
		return nil, err
	}
	series, order, err := collectBuckets(rows, 3)
	if err != nil {
		return nil, err
	}
	var out []alertCand
	for _, id := range order {
		s := series[id]
		v0 := s.vals[0]
		if v0 == nil || v0[1] < 3 || v0[0] <= 0 {
			continue
		}
		pct := 0.0
		if v0[2] > 0 {
			pct = v0[0] / (v0[0] + v0[2]) * 100
		}
		var hist []float64
		for b := 1; b <= 12; b++ {
			if v := s.vals[b]; v != nil {
				hist = append(hist, v[0])
			}
		}
		var z, expected float64
		if len(hist) >= int(p.get("min_history")) {
			m, sd := meanSD(hist)
			z, expected = zOf(v0[0], m, sd), m
			if z < p.get("z") {
				continue
			}
		} else if pct < 3 {
			continue
		}
		sev := "medium"
		if pct >= 10 {
			sev = "high"
		}
		out = append(out, alertCand{Kind: "price_override", Severity: sev, Fingerprint: "price_override:" + id + ":" + weekKey(), SubjectType: "user",
			SubjectID: sp(id), SubjectLabel: s.label, Period: periodLast7(), Value: fp(v0[0]), Expected: fp(expected), Score: fp(z),
			Detail: map[string]any{"lines": v0[1], "pct": round2(pct), "weeks": len(hist)}})
	}
	return out, nil
}

// ======================= owner app: today at a glance =======================

// GET /companies/{cid}/owner/today: today's collections and sales per currency, the money in tills and banks,
// open smart alerts, approvals waiting, receivables and the 5 newest alerts.
func (s *Server) ownerToday(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var day string
		if err := tx.QueryRow(ctx, `SELECT (now() AT TIME ZONE timezone)::date::text FROM companies WHERE id = $1`, cid).Scan(&day); err != nil {
			return nil, err
		}
		collected, err := queryJSON(ctx, tx, `SELECT currency::text AS currency, sum(amount)::text AS amount, count(*) AS payments
			FROM payments WHERE company_id = $1 AND status = 'posted' AND kind = 'payment' AND payment_date = $2::date
			GROUP BY currency ORDER BY (currency = $3) DESC, currency`, cid, day, base)
		if err != nil {
			return nil, err
		}
		sales, err := queryJSON(ctx, tx, `SELECT currency::text AS currency, sum(total)::text AS amount, count(*) AS invoices
			FROM invoices WHERE company_id = $1 AND status <> 'void' AND kind = 'invoice' AND issue_date = $2::date
			GROUP BY currency ORDER BY (currency = $3) DESC, currency`, cid, day, base)
		if err != nil {
			return nil, err
		}
		cash, err := queryJSON(ctx, tx, `SELECT t.currency::text AS currency, sum(t.debit - t.credit)::text AS amount
			FROM (SELECT DISTINCT account_id, currency FROM payment_methods WHERE company_id = $1 AND is_active AND kind IN ('cash', 'bank', 'wallet', 'card')) m
			JOIN account_totals($1, '0001-01-01'::date, current_date) t ON t.account_id = m.account_id AND t.currency = m.currency
			GROUP BY t.currency ORDER BY (t.currency = $2) DESC, t.currency`, cid, base)
		if err != nil {
			return nil, err
		}
		receivable, err := queryJSON(ctx, tx, `SELECT currency::text AS currency, sum(total - paid)::text AS amount,
			count(DISTINCT customer_id) AS customers,
			coalesce(sum(total - paid) FILTER (WHERE due_date < current_date), 0)::text AS overdue
			FROM invoices WHERE company_id = $1 AND status IN ('open', 'partial') GROUP BY currency ORDER BY (currency = $2) DESC, currency`, cid, base)
		if err != nil {
			return nil, err
		}
		counts, err := queryJSONOne(ctx, tx, `SELECT
			(SELECT count(*) FROM smart_alerts WHERE company_id = $1 AND status IN ('open', 'ack')) AS alerts,
			(SELECT count(*) FROM smart_alerts WHERE company_id = $1 AND status IN ('open', 'ack') AND severity = 'high') AS alerts_high,
			(SELECT count(*) FROM approval_requests WHERE company_id = $1 AND status = 'pending') AS approvals,
			(SELECT count(*) FROM sales_orders WHERE company_id = $1 AND status IN ('draft', 'confirmed') AND created_at::date = $2::date) AS orders_today`, cid, day)
		if err != nil {
			return nil, err
		}
		alerts, err := queryJSON(ctx, tx, `SELECT id, kind, severity, subject_label, period, value::float8 AS value, expected::float8 AS expected,
			detail, created_at FROM smart_alerts WHERE company_id = $1 AND status IN ('open', 'ack') AND $2
			ORDER BY (severity = 'high') DESC, created_at DESC LIMIT 5`, cid, can(r, "smart.view"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"day": day, "base_currency": base, "collected": collected, "sales": sales, "cash": cash,
			"receivable": receivable, "counts": counts, "alerts": alerts}, nil
	})
}

// ======================= payroll posting date option =======================

// GET / PUT /companies/{cid}/payroll-options {post_date: auto | period_end}
func (s *Server) payrollOptions(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	var in struct {
		PostDate string `json:"post_date"`
	}
	if r.Method == http.MethodPut {
		if !decodeBody(w, r, &in) {
			return
		}
		if in.PostDate != "auto" && in.PostDate != "period_end" {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if r.Method == http.MethodPut {
			tag, err := tx.Exec(ctx, `UPDATE companies SET payroll_post_date = $2 WHERE id = $1`, cid, in.PostDate)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		var mode string
		if err := tx.QueryRow(ctx, `SELECT payroll_post_date FROM companies WHERE id = $1`, cid).Scan(&mode); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		return map[string]string{"post_date": mode}, nil
	})
}

// ======================= public site: plans, contact, demo requests =======================

// GET /api/v1/public/site: the public plans (prices and limits) and the sales contact, for the marketing site.
func (s *Server) publicSite(w http.ResponseWriter, r *http.Request) {
	var plans json.RawMessage
	err := pgx.BeginFunc(r.Context(), s.OwnerPool, func(tx pgx.Tx) error {
		var err error
		plans, err = queryJSON(r.Context(), tx, `SELECT code, name_en, name_ar, name_fr, price_monthly::text AS price_monthly,
			price_yearly::text AS price_yearly, currency::text AS currency, max_subscribers, max_users, max_companies, apps, ai_monthly_limit
			FROM platform_plans WHERE is_public AND is_active ORDER BY sort, code`)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans, "phone": s.platformContactPhone, "email": s.platformContactEmail})
}

var leadBusinesses = []string{"generator", "internet", "satellite", "water", "supermarket", "clothing", "electronics", "furniture",
	"wholesale", "restaurant", "pharmacy", "factory", "other"}

// POST /api/v1/public/demo-request {name, company, phone, email, business, city, message, lang, website(honeypot)}
func (s *Server) publicDemoRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Company  string `json:"company"`
		Phone    string `json:"phone"`
		Email    string `json:"email"`
		Business string `json:"business"`
		City     string `json:"city"`
		Message  string `json:"message"`
		Lang     string `json:"lang"`
		Website  string `json:"website"` // honeypot
	}
	if !decodeBody(w, r, &in) {
		return
	}
	t := strings.TrimSpace
	in.Name, in.Company, in.Phone, in.Email, in.City, in.Message = t(in.Name), t(in.Company), t(in.Phone), t(in.Email), t(in.City), t(in.Message)
	if in.Business == "" {
		in.Business = "other"
	}
	if in.Name == "" || len(in.Name) > 120 || len(in.Company) > 160 || len(in.Phone) > 40 || len(in.Email) > 200 || len(in.City) > 80 ||
		len(in.Message) > 2000 || (in.Phone == "" && in.Email == "") || (in.Email != "" && !strings.Contains(in.Email, "@")) ||
		(in.Phone != "" && len(in.Phone) < 6) || !contains(leadBusinesses, in.Business) || len(in.Lang) > 5 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	ip := clientIP(r)
	if !s.signupLimiter.allow("lead:" + ip) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	s.signupLimiter.fail("lead:" + ip) // every request counts (5 an hour per address)
	if in.Website != "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	err := pgx.BeginFunc(r.Context(), s.OwnerPool, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `INSERT INTO platform_leads (name, company, phone, email, business, city, message, lang, ip)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, in.Name, in.Company, in.Phone, in.Email, in.Business, in.City, in.Message, in.Lang, ip)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// GET /platform/leads?status= : demo requests from the marketing site.
func (s *Server) platformLeads(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	if st != "" && !contains([]string{"new", "contacted", "won", "lost"}, st) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT id, name, company, phone, email, business, city, message, lang, status, notes, created_at, updated_at
			FROM platform_leads WHERE ($1 = '' OR status = $1) ORDER BY created_at DESC LIMIT 500`, st)
	})
}

// PATCH /platform/leads/{id} {status, notes}
func (s *Server) platformUpdateLead(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status *string `json:"status"`
		Notes  *string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) || (in.Status != nil && !contains([]string{"new", "contacted", "won", "lost"}, *in.Status)) || (in.Notes != nil && len(*in.Notes) > 2000) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE platform_leads SET status = coalesce($2, status), notes = coalesce($3, notes), updated_at = now()
			WHERE id = $1`, id, in.Status, in.Notes)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT id, name, company, phone, email, business, city, message, lang, status, notes, created_at, updated_at
			FROM platform_leads WHERE id = $1`, id)
	})
}

// ======================= import: supplier opening balances =======================

// supplierOpenings: what the company owed each supplier when it moved to Paxve. One posted supplier bill per row
// (Dr opening equity / chosen account, Cr payables), so the balance shows on the supplier and can be paid normally.
func (im *importer) supplierOpenings() (func() error, error) {
	account := im.opts["account_id"]
	if account == "" {
		_ = im.tx.QueryRow(im.ctx, `SELECT id::text FROM accounts WHERE company_id = $1 AND is_postable AND type = 'equity'
			ORDER BY (code IN ('12', '3200', '110', '120')) DESC, code LIMIT 1`, im.cid).Scan(&account)
	}
	if !isUUID(account) {
		return nil, &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "opening_account"}
	}
	if err := requirePostable(im.ctx, im.tx, im.cid, account); err != nil {
		return nil, err
	}
	sups, err := im.lookup(`SELECT code, id::text FROM suppliers WHERE company_id = $1`, im.cid)
	if err != nil {
		return nil, err
	}
	byName, err := im.lookup(`SELECT name, id::text FROM suppliers WHERE company_id = $1`, im.cid)
	if err != nil {
		return nil, err
	}
	base, err := companyBase(im.ctx, im.tx, im.cid)
	if err != nil {
		return nil, err
	}
	currencies, err := im.lookup(`SELECT code, decimals::text FROM currencies`)
	if err != nil {
		return nil, err
	}
	defaultDate := time.Now().Format(time.DateOnly)
	if v, ok := date(im.opts["date"]); ok {
		defaultDate = v
	}
	bills := make([]billInput, len(im.req.Rows))
	needRate := map[string]string{}
	for i, row := range im.req.Rows {
		n := i + 1
		sid, ok := sups[strings.ToLower(cell(row, "supplier_code"))]
		if !ok {
			sid, ok = byName[strings.ToLower(cell(row, "supplier_name"))]
		}
		if !ok {
			im.fail(n, "supplier_code", "not_found", cell(row, "supplier_code")+cell(row, "supplier_name"))
		}
		cur := strings.ToUpper(cell(row, "currency"))
		if cur == "" {
			cur = base
		}
		decS, ok := currencies[strings.ToLower(cur)]
		if !ok {
			im.fail(n, "currency", "invalid", cur)
		}
		amt, ok := num(cell(row, "amount"))
		if !ok || !amt.IsPositive() {
			im.fail(n, "amount", "invalid_number", cell(row, "amount"))
		}
		d, _ := strconv.Atoi(decS)
		bd := defaultDate
		if v := cell(row, "date"); v != "" {
			if bd, ok = date(v); !ok {
				im.fail(n, "date", "invalid_date", v)
			}
		}
		due := ""
		if v := cell(row, "due_date"); v != "" {
			if due, ok = date(v); !ok || due < bd {
				im.fail(n, "due_date", "invalid_date", v)
			}
		}
		desc := cell(row, "description")
		if desc == "" {
			desc = "Opening balance"
		}
		if cur != base {
			if old, ok := needRate[cur]; !ok || bd < old {
				needRate[cur] = bd
			}
		}
		bills[i] = billInput{SupplierID: sid, SupplierRef: cell(row, "reference"), BillDate: bd, DueDate: due, Currency: cur,
			Notes: "Opening balance (import)", AllowDuplicate: true,
			Lines: []billLineIn{{AccountID: account, Description: desc, Quantity: "1", UnitPrice: amt.Round(int32(d)).String()}}}
	}
	for cur, d := range needRate {
		if _, err := resolveRate(im.ctx, im.tx, im.cid, base, cur, d, ""); err != nil {
			im.fail(0, "currency", "no_rate", cur+" "+d)
		}
	}
	return func() error {
		for _, b := range bills {
			if _, err := createBillTx(im.ctx, im.tx, im.cid, b); err != nil {
				return err
			}
			im.res.Created++
		}
		return nil
	}, nil
}

// ======================= customer app ("My account"): more from the portal =======================

// GET /api/v1/portal/{token}/more: meter consumption (last 13 readings per meter), the customer's tickets (with the
// rating link of a closed, unrated one) and online orders with their tracking links.
func (s *Server) portalMore(w http.ResponseWriter, r *http.Request) {
	sc, cid, customer, ok := s.portalScope(w, r)
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		use, err := queryJSON(ctx, tx, `SELECT x.meter_no, x.period, x.reading::text AS reading, (x.reading - x.prev)::text AS used FROM (
			SELECT s.meter_no, m.period, m.reading, lag(m.reading) OVER (PARTITION BY m.subscription_id ORDER BY m.period) AS prev,
			  row_number() OVER (PARTITION BY m.subscription_id ORDER BY m.period DESC) AS rn
			FROM meter_readings m JOIN subscriptions s ON s.id = m.subscription_id
			WHERE s.customer_id = $1 AND s.company_id = $2) x WHERE x.rn <= 13 ORDER BY x.meter_no, x.period`, customer, cid)
		if err != nil {
			return nil, err
		}
		tickets, err := queryJSON(ctx, tx, `SELECT t.number, t.subject, t.status, t.category, t.created_at, t.resolved_at, t.rating,
			CASE WHEN t.status = 'done' AND t.rating IS NULL THEN t.rating_token END AS rating_token
			FROM tickets t WHERE t.customer_id = $1 AND t.company_id = $2 ORDER BY t.created_at DESC LIMIT 20`, customer, cid)
		if err != nil {
			return nil, err
		}
		orders, err := queryJSON(ctx, tx, `SELECT o.number, o.created_at, o.currency, o.total::text AS total, o.status, o.token,
			so.status AS order_status FROM shop_orders o LEFT JOIN sales_orders so ON so.id = o.sales_order_id
			WHERE o.customer_id = $1 AND o.company_id = $2 ORDER BY o.created_at DESC LIMIT 20`, customer, cid)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"consumption": use, "tickets": tickets, "orders": orders}, nil
	})
}

var portalTicketCats = []string{"outage", "no_internet", "slow", "no_signal", "meter", "billing", "new_connection", "move", "disconnect", "complaint", "other"}

// POST /api/v1/portal/{token}/tickets {subject, description, category}: the customer reports a problem (channel portal).
func (s *Server) portalNewTicket(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Subject     string `json:"subject"`
		Description string `json:"description"`
		Category    string `json:"category"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Subject, in.Description = strings.TrimSpace(in.Subject), strings.TrimSpace(in.Description)
	if in.Category == "" {
		in.Category = "other"
	}
	if in.Subject == "" || len([]rune(in.Subject)) > 120 || len(in.Description) > 4000 || !contains(portalTicketCats, in.Category) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	sc, cid, customer, ok := s.portalScope(w, r)
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var openN int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE customer_id = $1 AND channel = 'portal' AND created_at > now() - interval '1 day'`,
			customer).Scan(&openN); err != nil {
			return nil, err
		}
		if openN >= 5 { // a customer link cannot flood the help desk
			return nil, &apiError{status: http.StatusTooManyRequests, code: "too_many_attempts"}
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('tickets:' || $1))`, cid); err != nil {
			return nil, err
		}
		var id string
		var number int
		if err := tx.QueryRow(ctx, `INSERT INTO tickets (company_id, number, customer_id, category, subject, description, contact_phone, channel, created_by)
			VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM tickets WHERE company_id = $1), $2, $3, $4, $5,
			  coalesce((SELECT phone FROM customers WHERE id = $2), ''), 'portal', NULL) RETURNING id::text, number`,
			cid, customer, in.Category, in.Subject, in.Description).Scan(&id, &number); err != nil {
			return nil, err
		}
		if err := applySLA(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		return map[string]int{"number": number}, nil
	})
}

// keep the imports used when a build drops one of the helpers above
var _ = decimal.Zero
var _ = math.Abs
