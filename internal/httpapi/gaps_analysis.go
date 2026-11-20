package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Analysis (Odoo pivot / graph views): pick a data source, up to two row and one column groupings, one
// measure, a date range and filters; the server aggregates with whitelisted SQL only (no user SQL).
// Saved reports are personal or shared with the company.

type anSource struct {
	perm     string            // permission needed to read the source
	from     string            // FROM … JOIN … (aliases used by dims / measures)
	where    string            // extra conditions ($1 = company, $2/$3 = date range applied by the caller)
	date     string            // date column for the range and the date groupings
	currency string            // currency column ("" = base-currency amounts)
	dims     map[string]string // grouping name → SQL
	measures map[string]string // measure name → aggregate SQL
}

var anSources = map[string]anSource{
	"sales": {perm: "billing.view",
		from: `invoice_lines l JOIN invoices i ON i.id = l.invoice_id JOIN customers c ON c.id = i.customer_id LEFT JOIN items it ON it.id = l.item_id
			LEFT JOIN item_categories cat ON cat.id = it.category_id LEFT JOIN areas ar ON ar.id = c.area_id`,
		where: `i.company_id = $1 AND i.status <> 'void' AND i.kind = 'invoice'`, date: `i.issue_date`, currency: `i.currency`,
		dims: map[string]string{"customer": `c.name`, "item": `coalesce(it.name, l.description)`, "category": `coalesce(cat.name, '—')`,
			"brand": `coalesce(nullif(it.brand, ''), '—')`, "area": `coalesce(ar.name, '—')`, "currency": `i.currency::text`,
			"line_kind": `l.kind`},
		measures: map[string]string{"amount": `sum(l.amount)`, "tax": `sum(l.tax_amount)`, "quantity": `sum(l.quantity)`, "count": `count(DISTINCT i.id)`}},
	"invoices": {perm: "billing.view",
		from:  `invoices i JOIN customers c ON c.id = i.customer_id LEFT JOIN areas ar ON ar.id = c.area_id`,
		where: `i.company_id = $1 AND i.status <> 'void'`, date: `i.issue_date`, currency: `i.currency`,
		dims: map[string]string{"customer": `c.name`, "area": `coalesce(ar.name, '—')`, "status": `i.status`, "currency": `i.currency::text`,
			"source": `CASE WHEN i.subscription_id IS NOT NULL THEN 'subscription' WHEN i.sales_order_id IS NOT NULL THEN 'order' ELSE 'direct' END`,
			"kind":   `i.kind`},
		measures: map[string]string{"total": `sum(i.total)`, "paid": `sum(i.paid)`, "balance": `sum(i.total - i.paid)`, "count": `count(*)`}},
	"payments": {perm: "collections.view",
		from:  `payments p JOIN customers c ON c.id = p.customer_id LEFT JOIN users u ON u.id = p.received_by LEFT JOIN areas ar ON ar.id = c.area_id`,
		where: `p.company_id = $1 AND p.status = 'posted' AND p.kind = 'payment'`, date: `p.payment_date`, currency: `p.currency`,
		dims:     map[string]string{"customer": `c.name`, "collector": `coalesce(u.full_name, '—')`, "area": `coalesce(ar.name, '—')`, "currency": `p.currency::text`},
		measures: map[string]string{"amount": `sum(p.amount)`, "count": `count(*)`, "customers": `count(DISTINCT p.customer_id)`}},
	"purchases": {perm: "purchasing.view",
		from: `bill_lines l JOIN bills b ON b.id = l.bill_id JOIN suppliers s ON s.id = b.supplier_id LEFT JOIN items it ON it.id = l.item_id
			LEFT JOIN item_categories cat ON cat.id = it.category_id LEFT JOIN accounts a ON a.id = l.account_id`,
		where: `b.company_id = $1 AND b.status <> 'void'`, date: `b.bill_date`, currency: `b.currency`,
		dims: map[string]string{"supplier": `s.name`, "item": `coalesce(it.name, l.description)`, "category": `coalesce(cat.name, '—')`,
			"account": `a.code || ' ' || a.name`, "currency": `b.currency::text`},
		measures: map[string]string{"amount": `sum(l.amount)`, "tax": `sum(l.tax_amount)`, "quantity": `sum(l.quantity)`, "count": `count(DISTINCT b.id)`}},
	"stock": {perm: "stock.view",
		from: `stock_moves m JOIN items it ON it.id = m.item_id JOIN warehouses w ON w.id = m.warehouse_id
			LEFT JOIN item_categories cat ON cat.id = it.category_id`,
		where: `m.company_id = $1`, date: `m.move_date`,
		dims: map[string]string{"item": `it.name`, "category": `coalesce(cat.name, '—')`, "warehouse": `w.name`, "source": `m.source`,
			"direction": `CASE WHEN m.quantity > 0 THEN 'in' ELSE 'out' END`},
		measures: map[string]string{"quantity": `sum(m.quantity)`, "value": `sum(m.value)`, "count": `count(*)`}},
	"journal": {perm: "accounting.view",
		from: `journal_lines jl JOIN journal_entries e ON e.id = jl.entry_id JOIN accounts a ON a.id = jl.account_id
			LEFT JOIN cost_centers cc ON cc.id = jl.cost_center_id`,
		where: `e.company_id = $1 AND e.status = 'posted'`, date: `e.entry_date`,
		dims: map[string]string{"account": `a.code || ' ' || a.name`, "type": `a.type`, "cost_center": `coalesce(cc.name, '—')`, "source": `e.source`},
		measures: map[string]string{"debit": `sum(jl.base_debit)`, "credit": `sum(jl.base_credit)`, "net": `sum(jl.base_debit - jl.base_credit)`,
			"count": `count(*)`}},
	"subscriptions": {perm: "customers.view",
		from:  `subscriptions s JOIN customers c ON c.id = s.customer_id JOIN plans p ON p.id = s.plan_id LEFT JOIN areas ar ON ar.id = c.area_id`,
		where: `s.company_id = $1`, date: `s.start_date`,
		dims: map[string]string{"plan": `p.name`, "status": `s.status`, "area": `coalesce(ar.name, '—')`, "service": `coalesce(s.service_type, '—')`,
			"billing": `p.billing_type`},
		measures: map[string]string{"count": `count(*)`, "customers": `count(DISTINCT s.customer_id)`}},
	"payroll": {perm: "payroll.view",
		from:  `payslips ps JOIN payroll_runs pr ON pr.id = ps.run_id JOIN employees e ON e.id = ps.employee_id`,
		where: `pr.company_id = $1 AND pr.status IN ('approved', 'posted')`, date: `to_date(pr.period || '-01', 'YYYY-MM-DD')`, currency: `ps.currency`,
		dims: map[string]string{"employee": `e.name`, "department": `coalesce(nullif(e.department, ''), '—')`, "period": `pr.period`,
			"currency": `ps.currency::text`},
		measures: map[string]string{"base": `sum(ps.base)`, "net": `sum(ps.net)`, "allowances": `sum(ps.allowances)`, "deductions": `sum(ps.deductions)`,
			"employer": `sum(ps.nssf_employer)`, "count": `count(*)`}},
}

var anDateGroups = map[string]string{"day": `to_char(%s, 'YYYY-MM-DD')`, "week": `to_char(date_trunc('week', %s), 'IYYY-"W"IW')`,
	"month": `to_char(%s, 'YYYY-MM')`, "quarter": `to_char(%s, 'YYYY-"Q"Q')`, "year": `to_char(%s, 'YYYY')`}

type anSpec struct {
	Source   string            `json:"source"`
	Rows     []string          `json:"rows"`
	Cols     []string          `json:"cols"`
	Measure  string            `json:"measure"`
	From     string            `json:"from"`
	To       string            `json:"to"`
	Currency string            `json:"currency"`
	Filters  map[string]string `json:"filters"`
	Chart    string            `json:"chart"` // bar | line | pie (UI only)
}

func (sp *anSpec) dimSQL(src anSource, name string) (string, bool) {
	if g, ok := strings.CutPrefix(name, "date:"); ok {
		f, ok := anDateGroups[g]
		if !ok {
			return "", false
		}
		return fmt.Sprintf(f, src.date), true
	}
	s, ok := src.dims[name]
	return s, ok
}

func (sp *anSpec) validate() (anSource, error) {
	src, ok := anSources[sp.Source]
	if !ok || len(sp.Rows) > 2 || len(sp.Cols) > 1 || len(sp.Filters) > 6 {
		return src, badRequest("bad_request")
	}
	if _, ok := src.measures[sp.Measure]; !ok {
		return src, badRequest("bad_request")
	}
	for _, d := range append(append([]string{}, sp.Rows...), sp.Cols...) {
		if _, ok := sp.dimSQL(src, d); !ok {
			return src, badRequest("bad_request")
		}
	}
	for k, v := range sp.Filters {
		if _, ok := src.dims[k]; !ok || len(v) > 200 {
			return src, badRequest("bad_request")
		}
	}
	if (sp.From != "" && !validDate(sp.From)) || (sp.To != "" && !validDate(sp.To)) || (sp.Currency != "" && !currencyRe.MatchString(sp.Currency)) ||
		(sp.Chart != "" && sp.Chart != "bar" && sp.Chart != "line" && sp.Chart != "pie" && sp.Chart != "table") {
		return src, badRequest("bad_request")
	}
	return src, nil
}

// POST /companies/{cid}/analysis/run {spec} → {rows: [{r: [...], c: [...], v}], currency}
func (s *Server) runAnalysis(w http.ResponseWriter, r *http.Request) {
	var sp anSpec
	if !decodeBody(w, r, &sp) {
		return
	}
	src, err := sp.validate()
	if err != nil {
		fail(w, r, err)
		return
	}
	if !can(r, src.perm) && !can(r, "reports.view") {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if sp.From == "" {
		sp.From = "0001-01-01"
	}
	if sp.To == "" {
		sp.To = "9999-12-31"
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		args := []any{cid, sp.From, sp.To}
		where := []string{src.where, src.date + ` BETWEEN $2::date AND $3::date`}
		cur := sp.Currency
		if src.currency != "" {
			if cur == "" {
				var base string
				if err := tx.QueryRow(ctx, `SELECT base_currency::text FROM companies WHERE id = $1`, cid).Scan(&base); err != nil {
					return nil, err
				}
				cur = base
			}
			// amounts are never added across currencies: one currency at a time (or grouped by it)
			grouped := false
			for _, d := range append(append([]string{}, sp.Rows...), sp.Cols...) {
				if d == "currency" {
					grouped = true
				}
			}
			if !grouped && sp.Measure != "count" && sp.Measure != "customers" {
				args = append(args, cur)
				where = append(where, fmt.Sprintf(`%s = $%d`, src.currency, len(args)))
			}
		}
		for k, v := range sp.Filters {
			args = append(args, v)
			where = append(where, fmt.Sprintf(`(%s)::text = $%d`, src.dims[k], len(args)))
		}
		sel, group := []string{}, []string{}
		for i, d := range sp.Rows {
			e, _ := sp.dimSQL(src, d)
			sel = append(sel, fmt.Sprintf(`(%s)::text AS r%d`, e, i))
			group = append(group, fmt.Sprintf("r%d", i))
		}
		for i, d := range sp.Cols {
			e, _ := sp.dimSQL(src, d)
			sel = append(sel, fmt.Sprintf(`(%s)::text AS c%d`, e, i))
			group = append(group, fmt.Sprintf("c%d", i))
		}
		sel = append(sel, src.measures[sp.Measure]+`::text AS v`)
		q := `SELECT ` + strings.Join(sel, ", ") + ` FROM ` + src.from + ` WHERE ` + strings.Join(where, " AND ")
		if len(group) > 0 {
			q += ` GROUP BY ` + strings.Join(group, ", ") + ` ORDER BY ` + strings.Join(group, ", ")
		}
		q += ` LIMIT 5001`
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		nr, nc := len(sp.Rows), len(sp.Cols)
		for rows.Next() {
			vals := make([]*string, nr+nc+1)
			ptrs := make([]any, len(vals))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, err
			}
			rk, ck := []string{}, []string{}
			for i := 0; i < nr; i++ {
				rk = append(rk, deref(vals[i]))
			}
			for i := 0; i < nc; i++ {
				ck = append(ck, deref(vals[nr+i]))
			}
			out = append(out, map[string]any{"r": rk, "c": ck, "v": deref(vals[nr+nc])})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		truncated := len(out) > 5000
		if truncated {
			out = out[:5000]
		}
		if src.currency == "" {
			cur = ""
		}
		return map[string]any{"rows": out, "currency": cur, "truncated": truncated}, nil
	})
}

// GET /companies/{cid}/analysis/sources: what can be analysed (names only; the UI translates them).
func (s *Server) analysisSources(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	for k, src := range anSources {
		if !can(r, src.perm) && !can(r, "reports.view") {
			continue
		}
		dims, ms := []string{}, []string{}
		for d := range src.dims {
			dims = append(dims, d)
		}
		for m := range src.measures {
			ms = append(ms, m)
		}
		out[k] = map[string]any{"dims": dims, "measures": ms, "has_currency": src.currency != ""}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listAnalysisReports(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT a.id, a.name, a.spec, a.shared, a.owner_id, u.full_name AS owner_name, (a.owner_id = app_user()) AS mine, a.updated_at
			FROM analysis_reports a LEFT JOIN users u ON u.id = a.owner_id
			WHERE a.company_id = $1 AND a.deleted_at IS NULL AND (a.shared OR a.owner_id = app_user()) ORDER BY a.name`, r.PathValue("cid"))
	})
}

func (s *Server) saveAnalysisReport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string `json:"name"`
		Spec   anSpec `json:"spec"`
		Shared bool   `json:"shared"`
		Delete bool   `json:"delete"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if id != "" && !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !in.Delete {
		if _, err := in.Spec.validate(); err != nil || in.Name == "" || len(in.Name) > 120 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	spec, _ := json.Marshal(in.Spec)
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if id == "" {
			if in.Delete {
				return nil, badRequest("bad_request")
			}
			return dmlJSONOne(ctx, tx, `INSERT INTO analysis_reports (company_id, name, spec, shared) VALUES ($1, $2, $3, $4)
				RETURNING id, name, spec, shared, true AS mine`, cid, in.Name, spec, in.Shared)
		}
		var mine bool
		err := tx.QueryRow(ctx, `SELECT owner_id = app_user() FROM analysis_reports WHERE id = $1 AND company_id = $2 AND deleted_at IS NULL FOR UPDATE`,
			id, cid).Scan(&mine)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if !mine && !can(r, "settings.manage") {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		if in.Delete {
			_, err := tx.Exec(ctx, `UPDATE analysis_reports SET deleted_at = now() WHERE id = $1`, id)
			return map[string]string{"deleted": id}, err
		}
		return dmlJSONOne(ctx, tx, `UPDATE analysis_reports SET name = $2, spec = $3, shared = $4, updated_at = now() WHERE id = $1
			RETURNING id, name, spec, shared, true AS mine`, id, in.Name, spec, in.Shared)
	})
}
