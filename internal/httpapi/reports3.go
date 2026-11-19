package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Module S reports: cash flow, period comparison, the owner's summary in plain words, and the source
// document behind any journal entry (drill-down).

// GET /companies/{cid}/reports/cash-flow?from&to
//
// Direct method: every posted entry that moves a cash box or bank is classified by its main other account —
// income / expenses / customers / suppliers / stock / taxes = operating, fixed assets = investing,
// capital / partners / long-term loans = financing. Moves between two boxes are internal transfers.
func (s *Server) cashFlow(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		cash := `(SELECT account_id FROM payment_methods WHERE company_id = $1 AND kind <> 'cheque')`
		rows, err := queryJSON(ctx, tx, `
			WITH ent AS (
				SELECT DISTINCT l.entry_id FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id
				WHERE e.company_id = $1 AND e.status = 'posted' AND e.entry_date BETWEEN $2::date AND $3::date AND l.account_id IN `+cash+`),
			cl AS (
				SELECT l.entry_id, sum(l.base_debit - l.base_credit) AS cash FROM journal_lines l JOIN ent ON ent.entry_id = l.entry_id
				WHERE l.account_id IN `+cash+` GROUP BY l.entry_id),
			dom AS (
				SELECT DISTINCT ON (l.entry_id) l.entry_id, l.account_id FROM journal_lines l JOIN ent ON ent.entry_id = l.entry_id
				WHERE l.account_id NOT IN `+cash+` ORDER BY l.entry_id, abs(l.base_debit - l.base_credit) DESC, l.line_no),
			fa AS (SELECT DISTINCT asset_account_id AS account_id FROM assets WHERE company_id = $1)
			SELECT CASE
			         WHEN d.account_id IS NULL THEN 'transfer'
			         WHEN a.type IN ('income', 'expense') THEN 'operating'
			         WHEN a.type = 'equity' THEN 'financing'
			         WHEN a.type = 'asset' AND (a.id IN (SELECT account_id FROM fa) OR a.code ~ '^2' OR a.code ~ '^17') THEN 'investing'
			         WHEN a.type = 'liability' AND (a.code ~ '^16' OR a.code ~ '^455' OR a.code ~ '^2[5-9]') THEN 'financing'
			         ELSE 'operating' END AS section,
			       a.id AS account_id, a.code, a.name, a.name_i18n, sum(cl.cash)::text AS amount, count(*) AS entries
			FROM cl LEFT JOIN dom d ON d.entry_id = cl.entry_id LEFT JOIN accounts a ON a.id = d.account_id
			GROUP BY 1, a.id, a.code, a.name, a.name_i18n
			HAVING sum(cl.cash) <> 0
			ORDER BY 1, a.code`, cid, from, to)
		if err != nil {
			return nil, err
		}
		var opening, closing string
		if err := tx.QueryRow(ctx, `SELECT
				coalesce((SELECT sum(t.base_debit - t.base_credit) FROM account_totals($1, '0001-01-01'::date, $2::date - 1) t WHERE t.account_id IN `+cash+`), 0)::text,
				coalesce((SELECT sum(t.base_debit - t.base_credit) FROM account_totals($1, '0001-01-01'::date, $3::date) t WHERE t.account_id IN `+cash+`), 0)::text`,
			cid, from, to).Scan(&opening, &closing); err != nil {
			return nil, err
		}
		return map[string]any{"base_currency": base, "from": from, "to": to, "rows": rows, "opening": opening, "closing": closing}, nil
	})
}

// GET /companies/{cid}/reports/compare?kind=pl|bs&p=from:to&p=from:to… (bs: p=date). Up to 6 periods side by side.
func (s *Server) compareReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	periods := q["p"]
	if (kind != "pl" && kind != "bs") || len(periods) == 0 || len(periods) > 6 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	type period struct{ From, To string }
	var ps []period
	for _, p := range periods {
		f, t, two := strings.Cut(p, ":")
		if kind == "bs" {
			if two || !validDate(f) {
				writeErr(w, http.StatusBadRequest, "invalid_date")
				return
			}
			ps = append(ps, period{"0001-01-01", f})
			continue
		}
		if !two || !validDate(f) || !validDate(t) || t < f {
			writeErr(w, http.StatusBadRequest, "invalid_date")
			return
		}
		ps = append(ps, period{f, t})
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		types := `('income', 'expense')`
		sign := `CASE WHEN a.type = 'income' THEN sum(l.base_credit - l.base_debit) ELSE sum(l.base_debit - l.base_credit) END`
		src := plTotals
		if kind == "bs" {
			types = `('asset', 'liability', 'equity', 'income', 'expense')`
			sign = `CASE WHEN a.type = 'asset' THEN sum(l.base_debit - l.base_credit) ELSE sum(l.base_credit - l.base_debit) END`
			src = ledgerTotals
		}
		type acc struct {
			ID     string          `json:"account_id"`
			Code   string          `json:"code"`
			Name   string          `json:"name"`
			I18n   json.RawMessage `json:"name_i18n"`
			Type   string          `json:"type"`
			Values []string        `json:"values"`
		}
		byID := map[string]*acc{}
		var order []string
		for i, p := range ps {
			rows, err := tx.Query(ctx, `SELECT a.id::text, a.code, a.name, a.name_i18n, a.type, (`+sign+`)::text
				FROM `+src("$1", "$2", "$3")+` WHERE a.type IN `+types+`
				GROUP BY a.id HAVING sum(l.base_debit - l.base_credit) <> 0`, cid, p.From, p.To)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var x acc
				var v string
				if err := rows.Scan(&x.ID, &x.Code, &x.Name, &x.I18n, &x.Type, &v); err != nil {
					rows.Close()
					return nil, err
				}
				if kind == "bs" && (x.Type == "income" || x.Type == "expense") {
					x.ID, x.Code, x.Type, x.Name, x.I18n = "result", "", "equity", "Result not yet closed", json.RawMessage(`{"ar":"النتيجة غير المقفلة","fr":"Résultat non clôturé"}`)
					d, _ := decimal.NewFromString(v)
					if p := byID["result"]; p != nil && p.Values[i] != "" {
						e, _ := decimal.NewFromString(p.Values[i])
						d = d.Add(e)
					}
					v = d.String()
				}
				a := byID[x.ID]
				if a == nil {
					x.Values = make([]string, len(ps))
					a = &x
					byID[x.ID] = a
					order = append(order, x.ID)
				}
				a.Values[i] = v
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
		out := make([]*acc, 0, len(order))
		for _, id := range order {
			a := byID[id]
			for i := range a.Values {
				if a.Values[i] == "" {
					a.Values[i] = "0"
				}
			}
			out = append(out, a)
		}
		// accounts in code order, the unclosed result at the end of equity
		for i := 0; i < len(out); i++ {
			for j := i + 1; j < len(out); j++ {
				ki, kj := out[i].Code, out[j].Code
				if out[i].ID == "result" {
					ki = "~"
				}
				if out[j].ID == "result" {
					kj = "~"
				}
				if kj < ki {
					out[i], out[j] = out[j], out[i]
				}
			}
		}
		return map[string]any{"base_currency": base, "kind": kind, "periods": ps, "rows": out}, nil
	})
}

// GET /companies/{cid}/owner-summary: the numbers the owner looks at every day, ready to be said in plain words.
func (s *Server) ownerSummary(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		prevStart := monthStart.AddDate(0, -1, 0)
		prevSameDay := prevStart.AddDate(0, 0, now.Day()-1)
		if prevSameDay.Month() != prevStart.Month() {
			prevSameDay = monthStart.AddDate(0, 0, -1)
		}
		d := func(t time.Time) string { return t.Format(time.DateOnly) }
		out := map[string]any{"base_currency": base, "today": d(now)}
		q := func(key, sql string, args ...any) error {
			v, err := queryJSON(ctx, tx, sql, args...)
			out[key] = v
			return err
		}
		if err := q("collected_today", `SELECT p.currency, count(*) AS payments, sum(p.amount)::text AS amount FROM payments p
			WHERE p.company_id = $1 AND p.status = 'posted' AND p.kind = 'payment' AND p.payment_date = current_date
			GROUP BY p.currency ORDER BY p.currency`, cid); err != nil {
			return nil, err
		}
		boxes := `SELECT m.id, m.name, m.currency, m.user_id, u.full_name AS user_name, coalesce(t.balance, 0)::text AS balance
			FROM payment_methods m LEFT JOIN users u ON u.id = m.user_id
			LEFT JOIN (SELECT account_id, currency, sum(debit - credit) AS balance FROM account_totals($1, '0001-01-01'::date, '9999-12-31'::date)
			           GROUP BY account_id, currency) t ON t.account_id = m.account_id AND t.currency = m.currency
			WHERE m.company_id = $1 AND m.is_active AND m.kind <> 'cheque'`
		if err := q("collectors_cash", boxes+` AND m.user_id IS NOT NULL AND coalesce(t.balance, 0) <> 0 ORDER BY u.full_name, m.currency`, cid); err != nil {
			return nil, err
		}
		if err := q("company_cash", boxes+` AND m.user_id IS NULL ORDER BY m.sort, m.name`, cid); err != nil {
			return nil, err
		}
		if err := q("customers_owe", `SELECT i.currency, count(DISTINCT i.customer_id) AS customers, sum(i.total - i.paid)::text AS amount,
				coalesce(sum(i.total - i.paid) FILTER (WHERE i.due_date < current_date - 60), 0)::text AS over60,
				count(DISTINCT i.customer_id) FILTER (WHERE i.due_date < current_date - 60) AS customers_over60
			FROM invoices i WHERE i.company_id = $1 AND i.status IN ('open', 'partial') GROUP BY i.currency ORDER BY i.currency`, cid); err != nil {
			return nil, err
		}
		if err := q("suppliers_owed", `SELECT b.currency, count(DISTINCT b.supplier_id) AS suppliers, sum(b.total - b.paid)::text AS amount
			FROM bills b WHERE b.company_id = $1 AND b.status IN ('open', 'partial') GROUP BY b.currency ORDER BY b.currency`, cid); err != nil {
			return nil, err
		}
		pl := func(from, to string) (inc, exp string, err error) {
			err = tx.QueryRow(ctx, `SELECT coalesce(sum(l.base_credit - l.base_debit) FILTER (WHERE a.type = 'income'), 0)::text,
				coalesce(sum(l.base_debit - l.base_credit) FILTER (WHERE a.type = 'expense'), 0)::text
				FROM `+plTotals("$1", "$2", "$3"), cid, from, to).Scan(&inc, &exp)
			return
		}
		ti, te, err := pl(d(monthStart), d(now))
		if err != nil {
			return nil, err
		}
		pi, pe, err := pl(d(prevStart), d(prevSameDay))
		if err != nil {
			return nil, err
		}
		fi, fe, err := pl(d(prevStart), d(monthStart.AddDate(0, 0, -1)))
		if err != nil {
			return nil, err
		}
		out["month"] = map[string]string{"from": d(monthStart), "to": d(now), "income": ti, "expense": te}
		out["prev_same_period"] = map[string]string{"from": d(prevStart), "to": d(prevSameDay), "income": pi, "expense": pe}
		out["prev_month"] = map[string]string{"from": d(prevStart), "to": d(monthStart.AddDate(0, 0, -1)), "income": fi, "expense": fe}
		// diesel / fuel: stock on hand and average daily use over the last 30 days
		if err := q("fuel", `SELECT it.id, it.name, it.unit, coalesce((SELECT sum(st.quantity) FROM stock st WHERE st.item_id = it.id), 0)::text AS on_hand,
				(sum(-m.quantity) / 30.0)::numeric(20, 3)::text AS per_day,
				CASE WHEN sum(-m.quantity) > 0 THEN floor(coalesce((SELECT sum(st.quantity) FROM stock st WHERE st.item_id = it.id), 0) / (sum(-m.quantity) / 30.0))::int END AS days_left
			FROM stock_moves m JOIN items it ON it.id = m.item_id
			WHERE m.company_id = $1 AND m.source = 'fuel' AND m.move_date > current_date - 30
			GROUP BY it.id, it.name, it.unit ORDER BY days_left NULLS LAST`, cid); err != nil {
			return nil, err
		}
		var attn struct {
			Approvals, ChequesDue, ChequesBounced, Drafts, Unlocked int
			ChequesDueAmount                                        string
		}
		if err := tx.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM approval_requests WHERE company_id = $1 AND status = 'pending'),
				(SELECT count(*) FROM cheques WHERE company_id = $1 AND status IN ('in_hand', 'deposited', 'issued') AND due_date <= current_date + 7),
				(SELECT count(*) FROM cheques WHERE company_id = $1 AND status = 'bounced' AND updated_at > now() - interval '30 days'),
				(SELECT count(*) FROM journal_entries WHERE company_id = $1 AND status = 'draft'),
				(SELECT CASE WHEN lock_date IS NULL OR lock_date < (date_trunc('month', current_date) - interval '1 month')::date THEN 1 ELSE 0 END
				   FROM companies WHERE id = $1)`, cid).Scan(&attn.Approvals, &attn.ChequesDue, &attn.ChequesBounced, &attn.Drafts, &attn.Unlocked); err != nil {
			return nil, err
		}
		out["attention"] = map[string]int{"approvals": attn.Approvals, "cheques_due": attn.ChequesDue, "cheques_bounced": attn.ChequesBounced,
			"drafts": attn.Drafts, "books_unlocked": attn.Unlocked}
		return out, nil
	})
}

// GET /companies/{cid}/journal/{id}/source: the document that created an entry, so a report number leads to the
// receipt, bill or voucher behind it.
func (s *Server) entrySource(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var source, ref string
		var reversalOf *string
		err := tx.QueryRow(ctx, `SELECT source, reference, reversal_of::text FROM journal_entries WHERE id = $1 AND company_id = $2`, id, cid).
			Scan(&source, &ref, &reversalOf)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		look := id
		if reversalOf != nil { // a reversal leads to what it reversed
			look = *reversalOf
		}
		doc, err := queryJSON(ctx, tx, `
			SELECT 'invoice' AS kind, i.id, i.number, i.customer_id AS party_id, NULL::uuid AS other_id FROM invoices i WHERE i.company_id = $1 AND $2::uuid IN (i.journal_entry_id, i.void_entry_id)
			UNION ALL SELECT 'payment', p.id, p.number, p.customer_id, NULL FROM payments p WHERE p.company_id = $1 AND $2::uuid IN (p.journal_entry_id, p.void_entry_id)
			UNION ALL SELECT 'bill', b.id, b.number, b.supplier_id, NULL FROM bills b WHERE b.company_id = $1 AND $2::uuid IN (b.journal_entry_id, b.void_entry_id)
			UNION ALL SELECT 'supplier_payment', p.id, p.number, p.supplier_id, NULL FROM supplier_payments p WHERE p.company_id = $1 AND $2::uuid IN (p.journal_entry_id, p.void_entry_id)
			UNION ALL SELECT 'expense', x.id, x.number, x.supplier_id, NULL FROM expenses x WHERE x.company_id = $1 AND $2::uuid IN (x.journal_entry_id, x.void_entry_id)
			UNION ALL SELECT 'transfer', t.id, t.number, NULL, NULL FROM cash_transfers t WHERE t.company_id = $1 AND t.journal_entry_id = $2::uuid
			UNION ALL SELECT 'stock', d.id, d.number, NULL, NULL FROM stock_docs d WHERE d.company_id = $1 AND $2::uuid IN (d.journal_entry_id, d.void_entry_id)
			UNION ALL SELECT 'cheque', c.id, c.number, NULL, NULL FROM cheques c WHERE c.company_id = $1 AND c.journal_entry_id = $2::uuid
			UNION ALL SELECT 'cheque', c.id, c.number, NULL, NULL FROM cheque_events e JOIN cheques c ON c.id = e.cheque_id
			  WHERE e.company_id = $1 AND $2::uuid IN (e.journal_entry_id, e.undo_entry_id)
			UNION ALL SELECT 'exchange', x.id, x.number, NULL, NULL FROM currency_exchanges x WHERE x.company_id = $1 AND $2::uuid IN (x.journal_entry_id, x.void_entry_id)
			UNION ALL SELECT 'payroll', p.id, NULL, NULL, NULL FROM payroll_runs p WHERE p.company_id = $1 AND $2::uuid IN (p.journal_entry_id, p.void_entry_id)
			UNION ALL SELECT 'payroll', p.run_id, NULL, NULL, NULL FROM payroll_payments p WHERE p.company_id = $1 AND p.journal_entry_id = $2::uuid
			UNION ALL SELECT 'asset', a.id, NULL, NULL, NULL FROM assets a WHERE a.company_id = $1 AND $2::uuid IN (a.purchase_entry_id, a.disposal_entry_id)
			UNION ALL SELECT 'asset', d.asset_id, NULL, NULL, NULL FROM depreciation_lines d WHERE d.company_id = $1 AND d.journal_entry_id = $2::uuid
			UNION ALL SELECT 'advance', v.id, NULL, v.employee_id, NULL FROM employee_advances v WHERE v.company_id = $1 AND v.journal_entry_id = $2::uuid
			UNION ALL SELECT 'delivery', d.id, d.number, NULL, d.order_id FROM sales_deliveries d WHERE d.company_id = $1 AND $2::uuid IN (d.journal_entry_id, d.void_entry_id)
			UNION ALL SELECT 'fiscal_year', f.id, f.year, NULL, NULL FROM fiscal_years f WHERE f.company_id = $1 AND f.closing_entry_id = $2::uuid
			UNION ALL SELECT 'pos_session', p.id, p.number, NULL, NULL FROM pos_sessions p WHERE p.company_id = $1 AND p.over_short_entry_id = $2::uuid
			LIMIT 1`, cid, look)
		if err != nil {
			return nil, err
		}
		var list []json.RawMessage
		_ = json.Unmarshal(doc, &list)
		if len(list) == 0 && strings.HasPrefix(ref, "INV-") { // cost of sales and other entries that only carry the invoice number
			if d2, err := queryJSON(ctx, tx, `SELECT 'invoice' AS kind, i.id, i.number, i.customer_id AS party_id, NULL::uuid AS other_id
				FROM invoices i WHERE i.company_id = $1 AND i.number::text = $2 LIMIT 1`, cid, strings.TrimPrefix(ref, "INV-")); err == nil {
				_ = json.Unmarshal(d2, &list)
			}
		}
		out := map[string]any{"entry_id": id, "source": source, "reference": ref}
		if len(list) > 0 {
			out["document"] = list[0]
		}
		return out, nil
	})
}
