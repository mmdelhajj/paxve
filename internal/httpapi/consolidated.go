package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// consolidatedReport adds up the books of several companies of the organisation in one currency:
// trial balance, profit & loss or balance sheet. Accounts are matched by code. Each company's base
// currency amounts are converted at one rate per company: its official rate on the end date (units of
// the report currency per 1 base), or the inverse rate kept by a company whose base is the report
// currency, or a rate given in the query (rate_<BASE>=...). Same rate for every line keeps each
// company balanced, so the consolidation is balanced too.
//
//	GET /reports/consolidated?report=trial-balance|profit-loss|balance-sheet&companies=<id,id>&currency=USD&from=&to=
func (s *Server) consolidatedReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	report := q.Get("report")
	if report != "trial-balance" && report != "profit-loss" && report != "balance-sheet" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	target := strings.ToUpper(q.Get("currency"))
	if target != "" && !currencyRe.MatchString(target) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var ids []string
	for _, id := range strings.Split(q.Get("companies"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			if !isUUID(id) {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
			ids = append(ids, id)
		}
	}
	p := principal(r)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		type comp struct {
			ID, Name, Base string
			Rate           decimal.Decimal
		}
		rows, err := tx.Query(ctx, `SELECT id::text, name_en, base_currency::text FROM companies
			WHERE is_active AND (coalesce(cardinality($1::uuid[]), 0) = 0 OR id = ANY ($1::uuid[])) ORDER BY name_en`, ids)
		if err != nil {
			return nil, err
		}
		var comps []comp
		for rows.Next() {
			var c comp
			if err := rows.Scan(&c.ID, &c.Name, &c.Base); err != nil {
				return nil, err
			}
			if companyAllowed(p, c.ID) {
				comps = append(comps, c)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(comps) == 0 {
			return nil, notFound()
		}
		if target == "" {
			target = comps[0].Base
		}
		// one rate per company
		for i := range comps {
			c := &comps[i]
			if c.Base == target {
				c.Rate = decimal.NewFromInt(1)
				continue
			}
			if v := q.Get("rate_" + c.Base); v != "" {
				d, ok := money(v)
				if !ok || !d.IsPositive() {
					return nil, badRequest("bad_request")
				}
				c.Rate = d
				continue
			}
			rate, err := resolveRate(ctx, tx, c.ID, c.Base, target, to, "")
			if err == nil {
				c.Rate, _ = decimal.NewFromString(rate)
				continue
			}
			var ae *apiError
			if !errors.As(err, &ae) {
				return nil, err
			}
			// inverse: a company keeping the report currency as base knows how much of c.Base is 1 target
			var inv string
			ierr := tx.QueryRow(ctx, `SELECT x.rate::text FROM exchange_rates x JOIN companies k ON k.id = x.company_id
				WHERE k.base_currency = $1 AND x.currency = $2 AND x.kind = 'official' AND x.rate_date <= $3::date
				ORDER BY x.rate_date DESC LIMIT 1`, target, c.Base, to).Scan(&inv)
			if ierr != nil {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "no_rate", detail: c.Base + "→" + target}
			}
			d, _ := decimal.NewFromString(inv)
			if !d.IsPositive() {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "no_rate", detail: c.Base + "→" + target}
			}
			c.Rate = decimal.NewFromInt(1).DivRound(d, 12)
		}

		type line struct {
			Code, Name, Type string
			Amounts          map[string]decimal.Decimal
			Debit, Credit    decimal.Decimal
		}
		byCode := map[string]*line{}
		var types []string
		dateSQL := `e.entry_date BETWEEN $2::date AND $3::date`
		switch report {
		case "profit-loss":
			types = []string{"income", "expense"}
		case "balance-sheet":
			types = []string{"asset", "liability", "equity", "income", "expense"}
			dateSQL = `e.entry_date <= $3::date AND $2::date IS NOT NULL`
		default:
			types = []string{"asset", "liability", "equity", "income", "expense"}
		}
		results := map[string]decimal.Decimal{} // balance sheet: current result per company
		for _, c := range comps {
			rows, err := tx.Query(ctx, `
				SELECT a.code, a.name, a.type, sum(l.base_debit)::text, sum(l.base_credit)::text
				FROM journal_lines l
				JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
				JOIN accounts a ON a.id = l.account_id
				WHERE e.company_id = $1 AND `+dateSQL+` AND a.type = ANY ($4::text[])
				GROUP BY a.code, a.name, a.type`, c.ID, from, to, types)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var code, name, typ, dr, cr string
				if err := rows.Scan(&code, &name, &typ, &dr, &cr); err != nil {
					rows.Close()
					return nil, err
				}
				d, _ := decimal.NewFromString(dr)
				k, _ := decimal.NewFromString(cr)
				d, k = d.Mul(c.Rate), k.Mul(c.Rate)
				if report == "balance-sheet" && (typ == "income" || typ == "expense") {
					results[c.ID] = results[c.ID].Add(k.Sub(d))
					continue
				}
				amt := d.Sub(k) // trial balance: debit - credit; assets and expenses too
				if (report == "profit-loss" && typ == "income") ||
					(report == "balance-sheet" && (typ == "liability" || typ == "equity")) {
					amt = k.Sub(d)
				}
				key := code + "|" + typ
				ln := byCode[key]
				if ln == nil {
					ln = &line{Code: code, Name: name, Type: typ, Amounts: map[string]decimal.Decimal{}}
					byCode[key] = ln
				}
				ln.Amounts[c.ID] = ln.Amounts[c.ID].Add(amt)
				ln.Debit, ln.Credit = ln.Debit.Add(d), ln.Credit.Add(k)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}

		keys := make([]string, 0, len(byCode))
		for k := range byCode {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []map[string]any{}
		sums := map[string]decimal.Decimal{}
		for _, k := range keys {
			ln := byCode[k]
			total := decimal.Zero
			amounts := map[string]string{}
			for _, c := range comps {
				a := ln.Amounts[c.ID]
				total = total.Add(a)
				amounts[c.ID] = a.StringFixed(2)
			}
			if report == "balance-sheet" && total.IsZero() {
				continue
			}
			row := map[string]any{"code": ln.Code, "name": ln.Name, "type": ln.Type, "amounts": amounts, "total": total.StringFixed(2)}
			if report == "trial-balance" {
				row["debit"], row["credit"] = ln.Debit.StringFixed(2), ln.Credit.StringFixed(2)
				sums["debit"] = sums["debit"].Add(ln.Debit)
				sums["credit"] = sums["credit"].Add(ln.Credit)
			}
			sums[ln.Type] = sums[ln.Type].Add(total)
			out = append(out, row)
		}
		companies := []map[string]any{}
		for _, c := range comps {
			companies = append(companies, map[string]any{"id": c.ID, "name": c.Name, "base_currency": c.Base, "rate": c.Rate.String()})
		}
		totals := map[string]string{}
		switch report {
		case "trial-balance":
			totals["debit"], totals["credit"] = sums["debit"].StringFixed(2), sums["credit"].StringFixed(2)
		case "profit-loss":
			totals["income"], totals["expense"] = sums["income"].StringFixed(2), sums["expense"].StringFixed(2)
			totals["net"] = sums["income"].Sub(sums["expense"]).StringFixed(2)
		case "balance-sheet":
			res := decimal.Zero
			resBy := map[string]string{}
			for _, c := range comps {
				res = res.Add(results[c.ID])
				resBy[c.ID] = results[c.ID].StringFixed(2)
			}
			totals["assets"] = sums["asset"].StringFixed(2)
			totals["liabilities"] = sums["liability"].StringFixed(2)
			totals["equity"] = sums["equity"].StringFixed(2)
			totals["current_result"] = res.StringFixed(2)
			totals["liabilities_equity"] = sums["liability"].Add(sums["equity"]).Add(res).StringFixed(2)
			return map[string]any{"report": report, "currency": target, "from": from, "to": to, "companies": companies,
				"rows": out, "totals": totals, "current_result": resBy}, nil
		}
		return map[string]any{"report": report, "currency": target, "from": from, "to": to, "companies": companies,
			"rows": out, "totals": totals}, nil
	})
}
