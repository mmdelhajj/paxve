package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Document layouts and dashboards (phase 8).
//
// A layout is a small set of choices per document type (colours, logo, header / footer text, which fields show and in
// which order the blocks come, a second language for titles, a QR code, paper width for receipts); the print screens
// apply it, so no code or HTML is written by the customer.
//
// A dashboard is a list of widgets picked from a catalogue of fixed metrics (each with its own permission, computed by
// fixed queries of the company); a user can arrange their own, and an admin sets the default of a role or of everyone.

var docTypes = []string{"invoice", "receipt", "quote", "order", "delivery", "po", "bill"}

var layoutFields = []string{"logo", "tax_id", "customer_phone", "customer_address", "customer_code", "due_date", "period", "meter",
	"qty", "unit_price", "discount", "tax", "local_total", "paid", "balance", "terms", "footer", "legal", "signature", "cashier", "points"}
var layoutBlocks = []string{"header", "customer", "lines", "totals", "notes", "footer"}
var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type docLayout struct {
	Accent    string   `json:"accent,omitempty"`
	ShowLogo  *bool    `json:"show_logo,omitempty"`
	LogoSize  string   `json:"logo_size,omitempty"`
	Title     string   `json:"title,omitempty"`
	Header    string   `json:"header,omitempty"`
	Footer    string   `json:"footer,omitempty"`
	Hide      []string `json:"hide,omitempty"`
	Order     []string `json:"order,omitempty"`
	Bilingual string   `json:"bilingual,omitempty"`
	QR        string   `json:"qr,omitempty"`
	QRText    string   `json:"qr_text,omitempty"`
	Paper     string   `json:"paper,omitempty"`
	FontSize  int      `json:"font_size,omitempty"`
	Align     string   `json:"align,omitempty"`
	Border    *bool    `json:"border,omitempty"`
}

func (l *docLayout) valid() bool {
	if l.Accent != "" && !hexColor.MatchString(l.Accent) {
		return false
	}
	if !contains([]string{"", "sm", "md", "lg"}, l.LogoSize) || !contains([]string{"", "none", "doc", "custom"}, l.QR) ||
		!contains([]string{"", "a4", "a5", "58", "80"}, l.Paper) || !contains([]string{"", "start", "center"}, l.Align) ||
		(l.Bilingual != "" && !supportedLocales[l.Bilingual]) || (l.FontSize != 0 && (l.FontSize < 9 || l.FontSize > 18)) ||
		len(l.Title) > 80 || len(l.Header) > 600 || len(l.Footer) > 600 || len(l.QRText) > 300 || len(l.Hide) > len(layoutFields) || len(l.Order) > len(layoutBlocks) {
		return false
	}
	for _, f := range l.Hide {
		if !slices.Contains(layoutFields, f) {
			return false
		}
	}
	for _, b := range l.Order {
		if !slices.Contains(layoutBlocks, b) {
			return false
		}
	}
	return true
}

// GET /doc-layouts: every document type with its layout ({} = the standard look).
func (s *Server) listDocLayouts(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT doc_type, settings FROM doc_layouts WHERE company_id = $1`, r.PathValue("cid"))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]json.RawMessage{}
		for _, t := range docTypes {
			out[t] = json.RawMessage(`{}`)
		}
		for rows.Next() {
			var t string
			var raw []byte
			if err := rows.Scan(&t, &raw); err != nil {
				return nil, err
			}
			out[t] = raw
		}
		return out, rows.Err()
	})
}

// PUT /doc-layouts/{doc}: save the layout of one document type ({} = back to the standard look).
func (s *Server) putDocLayout(w http.ResponseWriter, r *http.Request) {
	var in docLayout
	if !decodeBody(w, r, &in) {
		return
	}
	doc := r.PathValue("doc")
	if !slices.Contains(docTypes, doc) || !in.valid() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	raw, _ := json.Marshal(in)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `INSERT INTO doc_layouts (company_id, doc_type, settings) VALUES ($1, $2, $3)
			ON CONFLICT (company_id, doc_type) DO UPDATE SET settings = EXCLUDED.settings, updated_by = app_user(), updated_at = now()`,
			r.PathValue("cid"), doc, raw); err != nil {
			return nil, err
		}
		return json.RawMessage(raw), nil
	})
}

// ======================= dashboards =======================

type metricDef struct {
	Key, Kind, Perm string
	run             func(ctx context.Context, tx pgx.Tx, cid string) (any, error)
}

type widget struct {
	ID     string `json:"id"`
	Metric string `json:"metric"`
	Size   int    `json:"size"`
	Title  string `json:"title,omitempty"`
}

func kpi(v decimal.Decimal, cur string) map[string]any {
	return map[string]any{"value": v.Round(2).String(), "currency": cur}
}

func companyCur(ctx context.Context, tx pgx.Tx, cid string) (string, error) {
	var c string
	err := tx.QueryRow(ctx, `SELECT base_currency::text FROM companies WHERE id = $1`, cid).Scan(&c)
	return c, err
}

// accountBalance: balance of one company account (debit - credit) up to today, base currency
func accountBalance(ctx context.Context, tx pgx.Tx, cid string, accs []string) (decimal.Decimal, error) {
	var v string
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(t.base_debit - t.base_credit), 0)::text FROM account_totals($1, '0001-01-01', current_date) t
		WHERE t.account_id = ANY ($2::uuid[])`, cid, accs).Scan(&v)
	d, _ := decimal.NewFromString(v)
	return d, err
}

const monthStart = `date_trunc('month', current_date)::date`

// paymentsBase: amounts of customer payments converted to the base currency with the latest official rate
const paymentsBase = `coalesce(sum(CASE WHEN p.currency = c.base_currency THEN p.amount ELSE p.amount / nullif((SELECT x.rate FROM exchange_rates x
	WHERE x.company_id = p.company_id AND x.currency = p.currency AND x.kind = 'official' ORDER BY x.rate_date DESC LIMIT 1), 0) END), 0)`

var metrics = []metricDef{
	{"revenue_today", "kpi", "reports.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return ledgerKPI(ctx, tx, cid, "income", "today")
	}},
	{"revenue_month", "kpi", "reports.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return ledgerKPI(ctx, tx, cid, "income", "month")
	}},
	{"expenses_month", "kpi", "reports.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return ledgerKPI(ctx, tx, cid, "expense", "month")
	}},
	{"profit_month", "kpi", "reports.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		cur, err := companyCur(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var v string
		err = tx.QueryRow(ctx, `SELECT coalesce(sum(CASE WHEN a.type = 'income' THEN l.base_credit - l.base_debit ELSE l.base_credit - l.base_debit END), 0)::text
			FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id JOIN accounts a ON a.id = l.account_id
			WHERE e.company_id = $1 AND e.status = 'posted' AND a.type IN ('income', 'expense') AND e.entry_date BETWEEN `+monthStart+` AND current_date`, cid).Scan(&v)
		d, _ := decimal.NewFromString(v)
		return kpi(d, cur), err
	}},
	{"collections_today", "kpi", "collections.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return collectionsKPI(ctx, tx, cid, "current_date")
	}},
	{"collections_month", "kpi", "collections.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return collectionsKPI(ctx, tx, cid, monthStart)
	}},
	{"receivables", "kpi", "billing.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return controlKPI(ctx, tx, cid, "ar_account_id", false)
	}},
	{"payables", "kpi", "purchasing.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return controlKPI(ctx, tx, cid, "ap_account_id", true)
	}},
	{"cash", "kpi", "bank.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		cur, err := companyCur(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var accs []string
		rows, err := tx.Query(ctx, `SELECT DISTINCT account_id::text FROM payment_methods WHERE company_id = $1 AND account_id IS NOT NULL`, cid)
		if err != nil {
			return nil, err
		}
		if accs, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return nil, err
		}
		d, err := accountBalance(ctx, tx, cid, accs)
		return kpi(d, cur), err
	}},
	{"stock_value", "kpi", "stock.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		cur, err := companyCur(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var v string
		err = tx.QueryRow(ctx, `SELECT coalesce(sum(value), 0)::text FROM stock WHERE company_id = $1`, cid).Scan(&v)
		d, _ := decimal.NewFromString(v)
		return kpi(d, cur), err
	}},
	{"overdue_invoices", "count", "billing.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return countOf(ctx, tx, `SELECT count(*) FROM invoices WHERE company_id = $1 AND kind = 'invoice' AND status <> 'void' AND total > paid AND due_date < current_date`, cid)
	}},
	{"open_tickets", "count", "service.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return countOf(ctx, tx, `SELECT count(*) FROM tickets WHERE company_id = $1 AND resolved_at IS NULL AND status NOT IN ('resolved', 'closed', 'cancelled')`, cid)
	}},
	{"open_alerts", "count", "smart.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return countOf(ctx, tx, `SELECT count(*) FROM smart_alerts WHERE company_id = $1 AND status = 'open'`, cid)
	}},
	{"pending_approvals", "count", permAny, func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return countOf(ctx, tx, `SELECT count(*) FROM approval_requests WHERE company_id = $1 AND status = 'pending'`, cid)
	}},
	{"new_customers", "count", "customers.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return countOf(ctx, tx, `SELECT count(*) FROM customers WHERE company_id = $1 AND created_at >= `+monthStart, cid)
	}},
	{"revenue_12m", "chart", "reports.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return monthlyChart(ctx, tx, cid, "income")
	}},
	{"expenses_12m", "chart", "reports.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		return monthlyChart(ctx, tx, cid, "expense")
	}},
	{"collections_30d", "chart", "collections.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		cur, err := companyCur(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		series, err := queryJSON(ctx, tx, `SELECT to_char(d, 'MM-DD') AS label, trim_scale(round((SELECT `+paymentsBase+` FROM payments p JOIN companies c ON c.id = p.company_id
			WHERE p.company_id = $1 AND p.status <> 'void' AND p.payment_date = d::date), 2))::text AS value
			FROM generate_series(current_date - 29, current_date, interval '1 day') d ORDER BY d`, cid)
		return map[string]any{"series": series, "currency": cur}, err
	}},
	{"top_customers", "list", "billing.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT cu.name AS label, i.currency AS sub, trim_scale(round(sum(i.total), 2))::text AS value
			FROM invoices i JOIN customers cu ON cu.id = i.customer_id WHERE i.company_id = $1 AND i.kind = 'invoice' AND i.status <> 'void'
			AND i.issue_date >= current_date - 90 GROUP BY cu.name, i.currency ORDER BY sum(i.total) DESC LIMIT 8`, cid)
		return map[string]any{"rows": rows}, err
	}},
	{"top_debtors", "list", "billing.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT cu.name AS label, i.currency AS sub, trim_scale(round(sum(i.total - i.paid), 2))::text AS value
			FROM invoices i JOIN customers cu ON cu.id = i.customer_id WHERE i.company_id = $1 AND i.kind = 'invoice' AND i.status <> 'void'
			AND i.total > i.paid AND i.due_date < current_date GROUP BY cu.name, i.currency ORDER BY sum(i.total - i.paid) DESC LIMIT 8`, cid)
		return map[string]any{"rows": rows}, err
	}},
	{"recent_invoices", "list", "billing.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT '#' || i.number || ' ' || cu.name AS label, i.currency || ' · ' || to_char(i.issue_date, 'YYYY-MM-DD') AS sub,
			trim_scale(i.total)::text AS value FROM invoices i JOIN customers cu ON cu.id = i.customer_id
			WHERE i.company_id = $1 AND i.status <> 'void' ORDER BY i.created_at DESC LIMIT 8`, cid)
		return map[string]any{"rows": rows}, err
	}},
	{"alerts_list", "list", "smart.view", func(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT kind AS label, subject_label AS sub, severity AS value, kind AS key FROM smart_alerts
			WHERE company_id = $1 AND status = 'open' ORDER BY score DESC NULLS LAST, created_at DESC LIMIT 8`, cid)
		return map[string]any{"rows": rows}, err
	}},
}

func ledgerKPI(ctx context.Context, tx pgx.Tx, cid, typ, per string) (any, error) {
	cur, err := companyCur(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	f := "current_date"
	if per == "month" {
		f = monthStart
	}
	var v string
	expr := "l.base_debit - l.base_credit"
	if typ == "income" {
		expr = "l.base_credit - l.base_debit"
	}
	err = tx.QueryRow(ctx, `SELECT coalesce(sum(`+expr+`), 0)::text FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id
		JOIN accounts a ON a.id = l.account_id WHERE e.company_id = $1 AND e.status = 'posted' AND a.type = $2
		AND e.entry_date BETWEEN `+f+` AND current_date`, cid, typ).Scan(&v)
	d, _ := decimal.NewFromString(v)
	return kpi(d, cur), err
}

func collectionsKPI(ctx context.Context, tx pgx.Tx, cid, from string) (any, error) {
	cur, err := companyCur(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	var v string
	err = tx.QueryRow(ctx, `SELECT `+paymentsBase+`::text FROM payments p JOIN companies c ON c.id = p.company_id
		WHERE p.company_id = $1 AND p.status <> 'void' AND p.kind IS DISTINCT FROM 'refund' AND p.payment_date BETWEEN `+from+` AND current_date`, cid).Scan(&v)
	d, _ := decimal.NewFromString(v)
	return kpi(d, cur), err
}

func controlKPI(ctx context.Context, tx pgx.Tx, cid, col string, credit bool) (any, error) {
	cur, err := companyCur(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	var acc *string
	if err := tx.QueryRow(ctx, `SELECT `+col+`::text FROM companies WHERE id = $1`, cid).Scan(&acc); err != nil {
		return nil, err
	}
	if acc == nil {
		return kpi(decimal.Zero, cur), nil
	}
	d, err := accountBalance(ctx, tx, cid, []string{*acc})
	if credit {
		d = d.Neg()
	}
	return kpi(d, cur), err
}

func countOf(ctx context.Context, tx pgx.Tx, sql, cid string) (any, error) {
	var n int64
	err := tx.QueryRow(ctx, sql, cid).Scan(&n)
	return map[string]any{"value": n}, err
}

func monthlyChart(ctx context.Context, tx pgx.Tx, cid, typ string) (any, error) {
	cur, err := companyCur(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	expr := "l.base_debit - l.base_credit"
	if typ == "income" {
		expr = "l.base_credit - l.base_debit"
	}
	series, err := queryJSON(ctx, tx, `SELECT to_char(m, 'YYYY-MM') AS label, trim_scale(round(coalesce((SELECT sum(`+expr+`) FROM journal_lines l
		JOIN journal_entries e ON e.id = l.entry_id JOIN accounts a ON a.id = l.account_id WHERE e.company_id = $1 AND e.status = 'posted'
		AND a.type = $2 AND e.entry_date >= m AND e.entry_date < m + interval '1 month'), 0), 2))::text AS value
		FROM generate_series(date_trunc('month', current_date) - interval '11 months', date_trunc('month', current_date), interval '1 month') m ORDER BY m`, cid, typ)
	return map[string]any{"series": series, "currency": cur}, err
}

func metricByKey(k string) *metricDef {
	for i := range metrics {
		if metrics[i].Key == k {
			return &metrics[i]
		}
	}
	return nil
}

// GET /dashboards/metrics: the catalogue (only the metrics this user may see).
func (s *Server) dashboardMetrics(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	out := []map[string]string{}
	for _, m := range metrics {
		if m.Perm == permAny || p.perms[m.Perm] {
			out = append(out, map[string]string{"key": m.Key, "kind": m.Kind})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// the built-in default when nobody set one
func builtinWidgets(role string) []widget {
	keys := []string{"revenue_month", "collections_today", "receivables", "cash", "profit_month", "revenue_12m", "top_debtors", "alerts_list"}
	switch role {
	case RoleCollector, RoleCashier:
		keys = []string{"collections_today", "collections_month", "collections_30d"}
	case RoleReadOnly:
		keys = []string{"revenue_month", "revenue_12m", "top_customers"}
	}
	out := make([]widget, 0, len(keys))
	for i, k := range keys {
		size := 1
		if m := metricByKey(k); m != nil && (m.Kind == "chart" || m.Kind == "list") {
			size = 2
		}
		out = append(out, widget{ID: "w" + string(rune('a'+i)), Metric: k, Size: size})
	}
	return out
}

func roleKey(p *Principal) string {
	if p.RoleID != nil {
		return strings.ReplaceAll(*p.RoleID, "-", "")[:32]
	}
	return p.Role
}

func userKey(p *Principal) string { return "user:" + strings.ReplaceAll(p.ID, "-", "")[:32] }

// GET /dashboards/mine: the user's own dashboard, else their role's, else the company's, else the built-in one.
func (s *Server) myDashboard(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		for _, sc := range []string{userKey(p), "role:" + roleKey(p), "company"} {
			var raw []byte
			err := tx.QueryRow(ctx, `SELECT widgets FROM dashboards WHERE company_id = $1 AND scope = $2`, cid, sc).Scan(&raw)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			src := strings.SplitN(sc, ":", 2)[0]
			return map[string]any{"source": src, "widgets": json.RawMessage(raw), "role_key": roleKey(p)}, nil
		}
		return map[string]any{"source": "builtin", "widgets": builtinWidgets(p.Role), "role_key": roleKey(p)}, nil
	})
}

func parseWidgets(in []widget) ([]widget, bool) {
	if len(in) > 24 {
		return nil, false
	}
	seen := map[string]bool{}
	for i := range in {
		w := &in[i]
		if metricByKey(w.Metric) == nil || w.Size < 1 || w.Size > 3 || len(w.Title) > 60 || len(w.ID) == 0 || len(w.ID) > 20 || seen[w.ID] {
			return nil, false
		}
		seen[w.ID] = true
	}
	return in, true
}

// PUT /dashboards/mine {widgets}: save the user's own dashboard. DELETE: back to the role's / company's default.
func (s *Server) saveMyDashboard(w http.ResponseWriter, r *http.Request) {
	s.saveDashboard(w, r, userKey(principal(r)))
}

func (s *Server) resetMyDashboard(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		_, err := tx.Exec(ctx, `DELETE FROM dashboards WHERE company_id = $1 AND scope = $2`, r.PathValue("cid"), userKey(principal(r)))
		return map[string]bool{"ok": true}, err
	})
}

var roleKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{2,40}$`)

// PUT /dashboards/roles/{role} {widgets}: the default of a role ("company" = everyone). DELETE removes it.
func (s *Server) saveRoleDashboard(w http.ResponseWriter, r *http.Request) {
	role := r.PathValue("role")
	if role != "company" && !roleKeyRe.MatchString(role) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	sc := "company"
	if role != "company" {
		sc = "role:" + role
	}
	if r.Method == http.MethodDelete {
		s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
			_, err := tx.Exec(ctx, `DELETE FROM dashboards WHERE company_id = $1 AND scope = $2`, r.PathValue("cid"), sc)
			return map[string]bool{"ok": true}, err
		})
		return
	}
	s.saveDashboard(w, r, sc)
}

func (s *Server) saveDashboard(w http.ResponseWriter, r *http.Request, scopeKey string) {
	var in struct {
		Widgets []widget `json:"widgets"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	ws, ok := parseWidgets(in.Widgets)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	raw, _ := json.Marshal(ws)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `INSERT INTO dashboards (company_id, scope, widgets) VALUES ($1, $2, $3)
			ON CONFLICT (company_id, scope) DO UPDATE SET widgets = EXCLUDED.widgets, updated_by = app_user(), updated_at = now()`,
			r.PathValue("cid"), scopeKey, raw); err != nil {
			return nil, err
		}
		return map[string]any{"widgets": json.RawMessage(raw)}, nil
	})
}

// GET /dashboards/roles: the saved role / company defaults.
func (s *Server) roleDashboards(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT CASE WHEN scope = 'company' THEN 'company' ELSE substr(scope, 6) END AS role, widgets, updated_at
			FROM dashboards WHERE company_id = $1 AND scope NOT LIKE 'user:%' ORDER BY scope`, r.PathValue("cid"))
	})
}

// POST /dashboards/data {metrics: [...]}: the values, each checked against the user's permissions.
func (s *Server) dashboardData(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Metrics []string `json:"metrics"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Metrics) > 24 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out := map[string]any{}
		for _, k := range in.Metrics {
			m := metricByKey(k)
			if m == nil {
				return nil, badRequest("bad_request")
			}
			if m.Perm != permAny && !p.perms[m.Perm] {
				out[k] = map[string]any{"kind": m.Kind, "denied": true}
				continue
			}
			v, err := m.run(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			out[k] = map[string]any{"kind": m.Kind, "data": v}
		}
		return out, nil
	})
}
