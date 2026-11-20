package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Module S: professional accounting — cost centers, budgets, fiscal years, opening balances,
// currency exchange vouchers and the accounts these features need.

// ---------------- accounts used by cheques and year closing ----------------

// ensureAccount returns the first postable account of the company with one of codes; when there is none
// it creates codes[0] under the first existing parent of parents.
func ensureAccount(ctx context.Context, tx pgx.Tx, cid string, codes []string, typ, en, ar, fr string, parents []string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE company_id = $1 AND code = ANY($2::text[]) AND is_postable AND is_active
		ORDER BY array_position($2::text[], code) LIMIT 1`, cid, codes).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	var parent *string
	if len(parents) > 0 {
		var p string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE company_id = $1 AND code = ANY($2::text[])
			ORDER BY array_position($2::text[], code) LIMIT 1`, cid, parents).Scan(&p); err == nil {
			parent = &p
		}
	}
	i18n, _ := json.Marshal(map[string]string{"en": en, "ar": ar, "fr": fr})
	code := codes[0]
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE company_id = $1 AND code = $2)`, cid, code).Scan(&taken); err != nil {
		return "", err
	}
	if taken { // the code exists but cannot be posted to: use a free sub-code
		for i := 1; i < 100; i++ {
			c := fmt.Sprintf("%s%d", codes[0], i)
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE company_id = $1 AND code = $2)`, cid, c).Scan(&taken); err != nil {
				return "", err
			}
			if !taken {
				code = c
				break
			}
		}
	}
	// named in the company's language, like the chart it joins (audit P7-F7); the others stay in name_i18n
	err = tx.QueryRow(ctx, `INSERT INTO accounts (company_id, code, name, name_i18n, type, parent_id)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, cid, code, pickLang(companyLang(ctx, tx, cid), en, ar, fr), i18n, typ, parent).Scan(&id)
	return id, err
}

type acctSettings struct {
	CIH, CUC, CP, RE string
}

// lebanesePCG: the company uses the Lebanese chart (3-digit classes) rather than the international one.
func lebanesePCG(ctx context.Context, tx pgx.Tx, cid string) bool {
	var ok bool
	_ = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE company_id = $1 AND code IN ('411', '401', '531'))`, cid).Scan(&ok)
	return ok
}

// loadAcctSettings reads the cheque / retained earnings accounts, creating and saving the defaults on first use.
func loadAcctSettings(ctx context.Context, tx pgx.Tx, cid string) (*acctSettings, error) {
	if !isUUID(cid) {
		return nil, notFound()
	}
	var cih, cuc, cp, re *string
	err := tx.QueryRow(ctx, `SELECT cheque_in_hand_account_id::text, cheque_collection_account_id::text, cheque_payable_account_id::text,
		retained_earnings_account_id::text FROM companies WHERE id = $1`, cid).Scan(&cih, &cuc, &cp, &re)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	leb := lebanesePCG(ctx, tx, cid)
	pick := func(lb, gen string) string {
		if leb {
			return lb
		}
		return gen
	}
	out := &acctSettings{}
	changed := false
	get := func(cur *string, dst *string, codes []string, typ, en, ar, fr string, parents []string) error {
		if cur != nil {
			*dst = *cur
			return nil
		}
		id, err := ensureAccount(ctx, tx, cid, codes, typ, en, ar, fr, parents)
		*dst = id
		changed = true
		return err
	}
	if err := get(cih, &out.CIH, []string{pick("5113", "1150")}, "asset", "Cheques in hand", "شيكات في الصندوق", "Chèques en caisse",
		[]string{"51", "5", "1000"}); err != nil {
		return nil, err
	}
	if err := get(cuc, &out.CUC, []string{pick("5112", "1160")}, "asset", "Cheques under collection", "شيكات برسم التحصيل",
		"Chèques à l'encaissement", []string{"51", "5", "1000"}); err != nil {
		return nil, err
	}
	if err := get(cp, &out.CP, []string{pick("403", "2150")}, "liability", "Cheques payable (issued)", "شيكات صادرة برسم الدفع",
		"Chèques émis à payer", []string{"40", "4", "2000"}); err != nil {
		return nil, err
	}
	if err := get(re, &out.RE, []string{pick("12", "3200"), "12", "3200", "120", "3300"}, "equity", "Retained earnings",
		"النتائج المدورة", "Report à nouveau", []string{"1", "3000"}); err != nil {
		return nil, err
	}
	if changed {
		if _, err := tx.Exec(ctx, `UPDATE companies SET cheque_in_hand_account_id = $2, cheque_collection_account_id = $3,
			cheque_payable_account_id = $4, retained_earnings_account_id = $5 WHERE id = $1`, cid, out.CIH, out.CUC, out.CP, out.RE); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GET /companies/{cid}/accounting/settings
func (s *Server) getAcctSettings(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		a, err := loadAcctSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		return map[string]string{"cheque_in_hand_account_id": a.CIH, "cheque_collection_account_id": a.CUC,
			"cheque_payable_account_id": a.CP, "retained_earnings_account_id": a.RE}, nil
	})
}

// PUT /companies/{cid}/accounting/settings
func (s *Server) putAcctSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CIH string `json:"cheque_in_hand_account_id"`
		CUC string `json:"cheque_collection_account_id"`
		CP  string `json:"cheque_payable_account_id"`
		RE  string `json:"retained_earnings_account_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	for _, v := range []string{in.CIH, in.CUC, in.CP, in.RE} {
		if !isUUID(v) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE company_id = $1 AND is_postable AND id = ANY($2::uuid[])`,
			cid, []string{in.CIH, in.CUC, in.CP, in.RE}).Scan(&n); err != nil {
			return nil, err
		}
		distinct := map[string]bool{in.CIH: true, in.CUC: true, in.CP: true, in.RE: true}
		if n != len(distinct) {
			return nil, badRequest("invalid_account")
		}
		tag, err := tx.Exec(ctx, `UPDATE companies SET cheque_in_hand_account_id = $2, cheque_collection_account_id = $3,
			cheque_payable_account_id = $4, retained_earnings_account_id = $5 WHERE id = $1`, cid, in.CIH, in.CUC, in.CP, in.RE)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ---------------- cost centers ----------------

const costCenterCols = `cc.id, cc.code, cc.name, cc.name_i18n, cc.kind, cc.station_id, st.name AS station_name, cc.generator_id,
	g.name AS generator_name, cc.is_active, cc.created_at`

const costCenterFrom = ` FROM cost_centers cc LEFT JOIN stations st ON st.id = cc.station_id LEFT JOIN generators g ON g.id = cc.generator_id`

func (s *Server) listCostCenters(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+costCenterCols+costCenterFrom+` WHERE cc.company_id = $1 ORDER BY cc.code`, r.PathValue("cid"))
	})
}

// POST /companies/{cid}/cost-centers, PATCH /companies/{cid}/cost-centers/{id}
func (s *Server) saveCostCenter(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code        *string            `json:"code"`
		Name        *string            `json:"name"`
		NameI18n    *map[string]string `json:"name_i18n"`
		Kind        *string            `json:"kind"`
		StationID   *string            `json:"station_id"`
		GeneratorID *string            `json:"generator_id"`
		IsActive    *bool              `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	create := id == ""
	if (!create && !isUUID(id)) || !optUUID(in.StationID) || !optUUID(in.GeneratorID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if create && (in.Code == nil || in.Name == nil) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Kind != nil && !strings.Contains(" branch station generator project department other ", " "+*in.Kind+" ") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	nz := func(p *string) any {
		if p == nil {
			return nil
		}
		return strings.TrimSpace(*p)
	}
	var i18n []byte
	if in.NameI18n != nil {
		i18n, _ = json.Marshal(in.NameI18n)
	}
	cid := r.PathValue("cid")
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if create {
			kind := "other"
			if in.Kind != nil {
				kind = *in.Kind
			}
			if i18n == nil {
				i18n = []byte("{}")
			}
			if err := tx.QueryRow(ctx, `INSERT INTO cost_centers (company_id, code, name, name_i18n, kind, station_id, generator_id)
				VALUES ($1, $2, $3, $4, $5, nullif($6, '')::uuid, nullif($7, '')::uuid) RETURNING id::text`,
				cid, strings.TrimSpace(*in.Code), strings.TrimSpace(*in.Name), i18n, kind, deref(in.StationID), deref(in.GeneratorID)).Scan(&id); err != nil {
				if isUniqueViolation(err) {
					return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
				}
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE cost_centers SET code = coalesce($3, code), name = coalesce($4, name),
				name_i18n = coalesce($5, name_i18n), kind = coalesce($6, kind),
				station_id = CASE WHEN $7::text IS NULL THEN station_id ELSE nullif($7, '')::uuid END,
				generator_id = CASE WHEN $8::text IS NULL THEN generator_id ELSE nullif($8, '')::uuid END,
				is_active = coalesce($9, is_active) WHERE id = $1 AND company_id = $2`,
				id, cid, nz(in.Code), nz(in.Name), i18n, in.Kind, in.StationID, in.GeneratorID, in.IsActive)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+costCenterCols+costCenterFrom+` WHERE cc.id = $1 AND cc.company_id = $2`, id, cid)
	})
}

// subscriptionCostCenter: the cost center of the station (or generator) a subscription's box belongs to ("" = none).
func subscriptionCostCenter(ctx context.Context, tx pgx.Tx, subscriptionID string) string {
	var id string
	_ = tx.QueryRow(ctx, `SELECT cc.id::text FROM subscriptions s JOIN boxes b ON b.id = s.box_id
		JOIN cost_centers cc ON cc.company_id = s.company_id AND cc.is_active AND cc.station_id = b.station_id
		WHERE s.id = $1 ORDER BY cc.generator_id NULLS FIRST LIMIT 1`, subscriptionID).Scan(&id)
	return id
}

// generatorCostCenter: the cost center of a generator, else of its station ("" = none).
func generatorCostCenter(ctx context.Context, tx pgx.Tx, generatorID string) string {
	if !isUUID(generatorID) {
		return ""
	}
	var id string
	_ = tx.QueryRow(ctx, `SELECT cc.id::text FROM generators g
		JOIN cost_centers cc ON cc.company_id = g.company_id AND cc.is_active AND (cc.generator_id = g.id OR (cc.generator_id IS NULL AND cc.station_id = g.station_id))
		WHERE g.id = $1 ORDER BY cc.generator_id NULLS LAST LIMIT 1`, generatorID).Scan(&id)
	return id
}

// PATCH /companies/{cid}/journal-lines/{id} {cost_center_id}: re-tag one line (amounts never change)
func (s *Server) tagJournalLine(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CostCenterID *string `json:"cost_center_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) || in.CostCenterID == nil || !optUUID(in.CostCenterID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE journal_lines SET cost_center_id = nullif($3, '')::uuid WHERE id = $1 AND company_id = $2`,
			id, cid, *in.CostCenterID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// GET /companies/{cid}/reports/cost-centers?from&to: income, expenses and result per cost center
func (s *Server) costCenterReport(w http.ResponseWriter, r *http.Request) {
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
		lines := `SELECT l.cost_center_id, a.id AS account_id, a.code, a.name, a.name_i18n, a.type,
				CASE WHEN a.type = 'income' THEN l.base_credit - l.base_debit ELSE l.base_debit - l.base_credit END AS amount
			FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id JOIN accounts a ON a.id = l.account_id
			WHERE e.company_id = $1 AND e.status = 'posted' AND e.entry_date BETWEEN $2::date AND $3::date AND a.type IN ('income', 'expense')
			  AND e.source <> 'year_close'
			  AND (e.reversal_of IS NULL OR e.reversal_of NOT IN (SELECT c.id FROM journal_entries c WHERE c.company_id = $1 AND c.source = 'year_close'))`
		centers, err := queryJSON(ctx, tx, `SELECT x.cost_center_id, cc.code, cc.name, cc.name_i18n, cc.kind,
				coalesce(sum(x.amount) FILTER (WHERE x.type = 'income'), 0)::text AS income,
				coalesce(sum(x.amount) FILTER (WHERE x.type = 'expense'), 0)::text AS expense,
				(coalesce(sum(x.amount) FILTER (WHERE x.type = 'income'), 0) - coalesce(sum(x.amount) FILTER (WHERE x.type = 'expense'), 0))::text AS net
			FROM (`+lines+`) x LEFT JOIN cost_centers cc ON cc.id = x.cost_center_id
			GROUP BY x.cost_center_id, cc.code, cc.name, cc.name_i18n, cc.kind ORDER BY cc.code NULLS LAST`, cid, from, to)
		if err != nil {
			return nil, err
		}
		detail, err := queryJSON(ctx, tx, `SELECT x.cost_center_id, x.account_id, x.code, x.name, x.name_i18n, x.type, sum(x.amount)::text AS amount
			FROM (`+lines+`) x GROUP BY x.cost_center_id, x.account_id, x.code, x.name, x.name_i18n, x.type
			HAVING sum(x.amount) <> 0 ORDER BY x.code`, cid, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"base_currency": base, "from": from, "to": to, "centers": centers, "detail": detail}, nil
	})
}

// ---------------- budgets ----------------

// GET /companies/{cid}/budgets?year&cost_center_id
func (s *Server) listBudget(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	cc := r.URL.Query().Get("cost_center_id")
	if err != nil || year < 2000 || year > 2200 || (cc != "" && !isUUID(cc)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT b.account_id, b.month, b.amount::text AS amount FROM budget_lines b
			WHERE b.company_id = $1 AND b.year = $2 AND b.cost_center_id IS NOT DISTINCT FROM nullif($3, '')::uuid
			ORDER BY b.account_id, b.month`, r.PathValue("cid"), year, cc)
	})
}

// PUT /companies/{cid}/budgets {year, cost_center_id, lines: [{account_id, month, amount}]}: replaces that year's budget
func (s *Server) putBudget(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Year         int    `json:"year"`
		CostCenterID string `json:"cost_center_id"`
		Lines        []struct {
			AccountID string `json:"account_id"`
			Month     int    `json:"month"`
			Amount    string `json:"amount"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Year < 2000 || in.Year > 2200 || (in.CostCenterID != "" && !isUUID(in.CostCenterID)) || len(in.Lines) > 12*2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `DELETE FROM budget_lines WHERE company_id = $1 AND year = $2
			AND cost_center_id IS NOT DISTINCT FROM nullif($3, '')::uuid`, cid, in.Year, in.CostCenterID); err != nil {
			return nil, err
		}
		n := 0
		for _, l := range in.Lines {
			amt, ok := money(l.Amount)
			if !isUUID(l.AccountID) || l.Month < 1 || l.Month > 12 || !ok {
				return nil, badRequest("bad_request")
			}
			if amt.IsZero() {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO budget_lines (company_id, year, month, account_id, cost_center_id, amount)
				VALUES ($1, $2, $3, $4, nullif($5, '')::uuid, $6::numeric)
				ON CONFLICT (company_id, year, month, account_id, cost_center_id) DO UPDATE SET amount = excluded.amount, updated_at = now()`,
				cid, in.Year, l.Month, l.AccountID, in.CostCenterID, amt.String()); err != nil {
				return nil, err
			}
			n++
		}
		return map[string]int{"lines": n}, nil
	})
}

// GET /companies/{cid}/reports/budget?year&cost_center_id: budget against actual, per account and month (base currency)
func (s *Server) budgetReport(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	cc := r.URL.Query().Get("cost_center_id")
	if err != nil || year < 2000 || year > 2200 || (cc != "" && !isUUID(cc)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rows, err := queryJSON(ctx, tx, `
			WITH act AS (
				SELECT l.account_id, extract(month FROM e.entry_date)::int AS month,
				       sum(CASE WHEN a.type = 'income' THEN l.base_credit - l.base_debit ELSE l.base_debit - l.base_credit END) AS amount
				FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id JOIN accounts a ON a.id = l.account_id
				WHERE e.company_id = $1 AND e.status = 'posted' AND e.entry_date BETWEEN make_date($2, 1, 1) AND make_date($2, 12, 31)
				  AND a.type IN ('income', 'expense') AND e.source <> 'year_close'
				  AND (e.reversal_of IS NULL OR e.reversal_of NOT IN (SELECT c.id FROM journal_entries c WHERE c.company_id = $1 AND c.source = 'year_close'))
				  AND ($3 = '' OR l.cost_center_id = $3::uuid)
				GROUP BY 1, 2),
			bud AS (
				SELECT account_id, month, amount FROM budget_lines
				WHERE company_id = $1 AND year = $2 AND cost_center_id IS NOT DISTINCT FROM nullif($3, '')::uuid),
			accs AS (SELECT account_id FROM act UNION SELECT account_id FROM bud)
			SELECT a.id AS account_id, a.code, a.name, a.name_i18n, a.type,
			       (SELECT coalesce(json_agg(coalesce((SELECT b.amount FROM bud b WHERE b.account_id = a.id AND b.month = m), 0)::text ORDER BY m), '[]')
			          FROM generate_series(1, 12) m) AS budget,
			       (SELECT coalesce(json_agg(coalesce((SELECT x.amount FROM act x WHERE x.account_id = a.id AND x.month = m), 0)::text ORDER BY m), '[]')
			          FROM generate_series(1, 12) m) AS actual
			FROM accs JOIN accounts a ON a.id = accs.account_id
			ORDER BY a.type DESC, a.code`, cid, year, cc)
		if err != nil {
			return nil, err
		}
		return map[string]any{"base_currency": base, "year": year, "rows": rows}, nil
	})
}

// ---------------- fiscal years ----------------

// GET /companies/{cid}/fiscal-years: every year with entries, its result and whether it is closed
func (s *Server) listFiscalYears(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		mm, dd, err := fiscalStart(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		// fiscal year N starts on MM-DD of calendar year N (01-01: the calendar year)
		years, err := queryJSON(ctx, tx, `
			WITH fy AS (SELECT make_interval(months => $2::int - 1, days => $3::int - 1) AS shift),
			ys AS (
				SELECT generate_series(coalesce((SELECT extract(year FROM min(entry_date) - (SELECT shift FROM fy))::int FROM journal_entries WHERE company_id = $1 AND status = 'posted'),
				                                extract(year FROM current_date - (SELECT shift FROM fy))::int),
				                       extract(year FROM current_date - (SELECT shift FROM fy))::int) AS year),
			yr AS (SELECT year, make_date(year, $2::int, $3::int) AS s, (make_date(year, $2::int, $3::int) + interval '1 year' - interval '1 day')::date AS e FROM ys)
			SELECT yr.year, yr.s AS start_date, yr.e AS end_date, coalesce(f.status, 'open') AS status, f.closing_entry_id, f.closed_at, cu.full_name AS closed_by,
			       f.reopened_at, f.result::text AS closed_result,
			       (SELECT coalesce(sum(l.base_credit - l.base_debit), 0)::text FROM pl_totals($1, yr.s, yr.e) l
			          JOIN accounts a ON a.id = l.account_id WHERE a.type IN ('income', 'expense')) AS result,
			       (SELECT count(*) FROM journal_entries d WHERE d.company_id = $1 AND d.status = 'draft'
			          AND d.entry_date BETWEEN yr.s AND yr.e) AS drafts
			FROM yr LEFT JOIN fiscal_years f ON f.company_id = $1 AND f.year = yr.year LEFT JOIN users cu ON cu.id = f.closed_by
			ORDER BY yr.year DESC`, cid, mm, dd)
		if err != nil {
			return nil, err
		}
		var lock *string
		if err := tx.QueryRow(ctx, `SELECT lock_date::text FROM companies WHERE id = $1`, cid).Scan(&lock); err != nil {
			return nil, err
		}
		return map[string]any{"base_currency": base, "lock_date": lock, "years": years}, nil
	})
}

// POST /companies/{cid}/fiscal-years/close {year}: the year's result goes to retained earnings (closing entry
// dated 31 December) and the books are locked up to that date.
func (s *Server) closeFiscalYear(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Year int `json:"year"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Year < 2000 || in.Year > time.Now().Year() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		start, end, err := fiscalRange(ctx, tx, cid, in.Year)
		if err != nil {
			return nil, err
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		as, err := loadAcctSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var status *string
		if err := tx.QueryRow(ctx, `SELECT status FROM fiscal_years WHERE company_id = $1 AND year = $2 FOR UPDATE`, cid, in.Year).Scan(&status); err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
		if status != nil && *status == "closed" {
			return nil, &apiError{status: http.StatusConflict, code: "year_closed"}
		}
		var drafts int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM journal_entries WHERE company_id = $1 AND status = 'draft'
			AND entry_date BETWEEN $2::date AND $3::date`, cid, start, end).Scan(&drafts); err != nil {
			return nil, err
		}
		if drafts > 0 {
			return nil, &apiError{status: http.StatusConflict, code: "drafts_pending", detail: strconv.Itoa(drafts)}
		}
		var prevLock *string
		if err := tx.QueryRow(ctx, `SELECT lock_date::text FROM companies WHERE id = $1 FOR UPDATE`, cid).Scan(&prevLock); err != nil {
			return nil, err
		}
		// balances of the year's income and expense accounts (base currency; per currency for single-currency accounts)
		type bal struct {
			account, typ, cur string
			acur              *string
			amount, baseAmt   decimal.Decimal
		}
		rows, err := tx.Query(ctx, `SELECT l.account_id::text, a.type, l.currency::text, a.currency::text,
				sum(l.debit - l.credit)::text, sum(l.base_debit - l.base_credit)::text
			FROM pl_totals($1, $2::date, $3::date) l JOIN accounts a ON a.id = l.account_id
			WHERE a.type IN ('income', 'expense') GROUP BY 1, 2, 3, 4 HAVING sum(l.base_debit - l.base_credit) <> 0 OR sum(l.debit - l.credit) <> 0`,
			cid, start, end)
		if err != nil {
			return nil, err
		}
		bals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (bal, error) {
			var b bal
			var a, ba string
			err := row.Scan(&b.account, &b.typ, &b.cur, &b.acur, &a, &ba)
			b.amount, _ = decimal.NewFromString(a)
			b.baseAmt, _ = decimal.NewFromString(ba)
			return b, err
		})
		if err != nil {
			return nil, err
		}
		// one base-currency line per account (accounts tied to another currency keep their currency)
		perAcc := map[string]decimal.Decimal{}
		order := []string{}
		var lines []jline
		result := decimal.Zero // credit balance = profit
		for _, b := range bals {
			result = result.Sub(b.baseAmt)
			if b.acur != nil && *b.acur != set.Base {
				if b.amount.IsZero() || b.baseAmt.IsZero() {
					continue
				}
				rate := b.amount.Div(b.baseAmt).Abs().Round(10).String()
				l := jline{Account: b.account, Desc: fmt.Sprintf("Closing %d", in.Year), Currency: b.cur, Rate: rate}
				if b.amount.IsPositive() {
					l.Credit = b.amount
				} else {
					l.Debit = b.amount.Neg()
				}
				lines = append(lines, l)
				continue
			}
			if _, ok := perAcc[b.account]; !ok {
				order = append(order, b.account)
			}
			perAcc[b.account] = perAcc[b.account].Add(b.baseAmt)
		}
		for _, acc := range order {
			v := perAcc[acc]
			if v.IsZero() {
				continue
			}
			l := jline{Account: acc, Desc: fmt.Sprintf("Closing %d", in.Year), Currency: set.Base, Rate: "1"}
			if v.IsPositive() {
				l.Credit = v
			} else {
				l.Debit = v.Neg()
			}
			lines = append(lines, l)
		}
		var je *string
		if len(lines) > 0 {
			re := jline{Account: as.RE, Desc: fmt.Sprintf("Result %d", in.Year), Currency: set.Base, Rate: "1"}
			if result.IsPositive() {
				re.Credit = result
			} else if result.IsNegative() {
				re.Debit = result.Neg()
			}
			if !result.IsZero() {
				lines = append(lines, re)
			}
			// the closing entry is dated 31 December even when the books are already locked beyond it
			if prevLock != nil && *prevLock >= end {
				if _, err := tx.Exec(ctx, `UPDATE companies SET lock_date = $2::date - 1 WHERE id = $1`, cid, end); err != nil {
					return nil, err
				}
			}
			id, err := postJournal(ctx, tx, cid, set, end, fmt.Sprintf("Year-end closing %d", in.Year), fmt.Sprintf("CLOSE-%d", in.Year), "year_close", lines)
			if err != nil {
				return nil, err
			}
			je = &id
		}
		if _, err := tx.Exec(ctx, `UPDATE companies SET lock_date = greatest(coalesce($3::date, $2::date), $2::date) WHERE id = $1`, cid, end, prevLock); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO fiscal_years (company_id, year, start_date, end_date, status, result, closing_entry_id, previous_lock, closed_by, closed_at)
			VALUES ($1, $2, $3::date, $4::date, 'closed', $5::numeric, $6, $7::date, app_user(), now())
			ON CONFLICT (company_id, year) DO UPDATE SET status = 'closed', result = excluded.result, closing_entry_id = excluded.closing_entry_id,
			  previous_lock = excluded.previous_lock, closed_by = excluded.closed_by, closed_at = excluded.closed_at, reopened_by = NULL, reopened_at = NULL`,
			cid, in.Year, start, end, result.String(), je, prevLock); err != nil {
			return nil, err
		}
		return map[string]any{"year": in.Year, "result": result.StringFixed(4), "closing_entry_id": je, "lock_date": end}, nil
	})
}

// POST /companies/{cid}/fiscal-years/reopen {year}: reverses the closing entry and unlocks the year (latest closed year first).
func (s *Server) reopenFiscalYear(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Year int `json:"year"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Year < 2000 || in.Year > 2200 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		start, end, err := fiscalRange(ctx, tx, cid, in.Year)
		if err != nil {
			return nil, err
		}
		var status string
		var je, prevLock *string
		err = tx.QueryRow(ctx, `SELECT status, closing_entry_id::text, previous_lock::text FROM fiscal_years
			WHERE company_id = $1 AND year = $2 FOR UPDATE`, cid, in.Year).Scan(&status, &je, &prevLock)
		if err == pgx.ErrNoRows || (err == nil && status != "closed") {
			return nil, &apiError{status: http.StatusConflict, code: "year_not_closed"}
		}
		if err != nil {
			return nil, err
		}
		var later bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM fiscal_years WHERE company_id = $1 AND year > $2 AND status = 'closed')`,
			cid, in.Year).Scan(&later); err != nil {
			return nil, err
		}
		if later {
			return nil, &apiError{status: http.StatusConflict, code: "later_year_closed"}
		}
		// unlock: back to the lock before the closing, but never inside the reopened year
		if _, err := tx.Exec(ctx, `UPDATE companies SET lock_date = CASE WHEN $3::date IS NULL THEN NULL ELSE least($3::date, $2::date - 1) END
			WHERE id = $1`, cid, start, prevLock); err != nil {
			return nil, err
		}
		if je != nil {
			if _, err := tx.Exec(ctx, `SELECT reverse_journal_entry($1, $2::date, $3)`, *je, end, fmt.Sprintf("Reopen year %d", in.Year)); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE fiscal_years SET status = 'reopened', reopened_by = app_user(), reopened_at = now()
			WHERE company_id = $1 AND year = $2`, cid, in.Year); err != nil {
			return nil, err
		}
		return map[string]any{"year": in.Year, "status": "reopened"}, nil
	})
}

// ---------------- opening balances ----------------

// POST /companies/{cid}/opening-balances {date, memo, lines: [{account_id, currency, debit, credit}]}: one posted entry;
// whatever does not balance goes to retained earnings (opening equity).
func (s *Server) postOpeningBalances(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date  string `json:"date"`
		Memo  string `json:"memo"`
		Lines []struct {
			AccountID string `json:"account_id"`
			Currency  string `json:"currency"`
			Debit     string `json:"debit"`
			Credit    string `json:"credit"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !validDate(in.Date) || len(in.Lines) == 0 || len(in.Lines) > 2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		as, err := loadAcctSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		// audit C-M5: receivables / payables / stock / gift-card / points accounts carry sub-ledgers. An opening amount
		// typed straight on them would never show on a customer, supplier or item, so they are refused here:
		// customers' openings go through Import → opening balances, suppliers' as an opening supplier bill.
		var ctl []string
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(id::text), '{}') FROM (
				SELECT unnest(ARRAY[ar_account_id, ap_account_id, inventory_account_id, gift_liability_account_id, loyalty_liability_account_id]) AS id
				FROM companies WHERE id = $1
				UNION SELECT inventory_account_id FROM items WHERE company_id = $1) x WHERE id IS NOT NULL`, cid).Scan(&ctl); err != nil {
			return nil, err
		}
		var lines []jline
		diff := decimal.Zero // base debit - credit
		for _, l := range in.Lines {
			if slices.Contains(ctl, l.AccountID) && (strings.TrimSpace(l.Debit) != "" && strings.TrimSpace(l.Debit) != "0" ||
				strings.TrimSpace(l.Credit) != "" && strings.TrimSpace(l.Credit) != "0") {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "opening_control_account"}
			}
			dr, ok1 := parseAmount(l.Debit)
			cr, ok2 := parseAmount(l.Credit)
			cur := strings.ToUpper(strings.TrimSpace(l.Currency))
			if cur == "" {
				cur = set.Base
			}
			if !isUUID(l.AccountID) || !ok1 || !ok2 || !currencyRe.MatchString(cur) {
				return nil, badRequest("bad_request")
			}
			if dr.IsZero() && cr.IsZero() {
				continue
			}
			var typ string
			if err := tx.QueryRow(ctx, `SELECT type FROM accounts WHERE id = $1 AND company_id = $2 AND is_postable`, l.AccountID, cid).Scan(&typ); err != nil {
				if err == pgx.ErrNoRows {
					return nil, badRequest("invalid_account")
				}
				return nil, err
			}
			rateS, err := resolveRate(ctx, tx, cid, set.Base, cur, in.Date, "")
			if err != nil {
				return nil, err
			}
			rate, _ := decimal.NewFromString(rateS)
			net := dr.Sub(cr)
			diff = diff.Add(net.DivRound(rate, 4))
			jl := jline{Account: l.AccountID, Desc: "Opening balance", Currency: cur, Rate: rateS}
			if net.IsPositive() {
				jl.Debit = net
			} else if net.IsNegative() {
				jl.Credit = net.Neg()
			} else {
				continue
			}
			lines = append(lines, jl)
		}
		if len(lines) == 0 {
			return nil, badRequest("invalid_amount")
		}
		if !diff.IsZero() {
			bl := jline{Account: as.RE, Desc: "Opening balance difference", Currency: set.Base, Rate: "1"}
			if diff.IsPositive() {
				bl.Credit = diff
			} else {
				bl.Debit = diff.Neg()
			}
			lines = append(lines, bl)
		}
		memo := strings.TrimSpace(in.Memo)
		if memo == "" {
			memo = "Opening balances"
		}
		id, err := postJournal(ctx, tx, cid, set, in.Date, memo, "OPENING", "opening", lines)
		if err != nil {
			return nil, err
		}
		return map[string]any{"journal_entry_id": id, "difference": diff.StringFixed(4)}, nil
	})
}

// ---------------- currency exchange vouchers ----------------

const exchangeCols = `x.id, x.number, x.exchange_date, x.from_method_id, fm.name AS from_method, x.from_currency, x.from_amount::text AS from_amount,
	x.to_method_id, tm.name AS to_method, x.to_currency, x.to_amount::text AS to_amount, trim_scale(x.rate)::text AS rate, x.counterparty,
	x.status, x.journal_entry_id, x.notes, u.full_name AS created_by, x.created_at,
	(SELECT coalesce(sum(l.base_credit - l.base_debit), 0)::text FROM journal_lines l JOIN accounts a ON a.id = l.account_id
	   WHERE l.entry_id = x.journal_entry_id AND a.type IN ('income', 'expense')) AS gain`

const exchangeFrom = ` FROM currency_exchanges x JOIN payment_methods fm ON fm.id = x.from_method_id JOIN payment_methods tm ON tm.id = x.to_method_id
	LEFT JOIN users u ON u.id = x.created_by`

func (s *Server) listExchanges(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+exchangeCols+exchangeFrom+`
			WHERE x.company_id = $1 AND x.exchange_date BETWEEN $2::date AND $3::date ORDER BY x.exchange_date DESC, x.number DESC LIMIT 1000`,
			r.PathValue("cid"), from, to)
	})
}

// POST /companies/{cid}/exchanges {date, from_method_id, from_amount, to_method_id, to_amount | rate, counterparty, notes}
// Money leaves one box in one currency and enters another box in another currency (buying or selling dollars).
// Both sides are booked at the official rate; the difference is the exchange gain or loss.
func (s *Server) createExchange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date         string `json:"date"`
		FromMethodID string `json:"from_method_id"`
		FromAmount   string `json:"from_amount"`
		ToMethodID   string `json:"to_method_id"`
		ToAmount     string `json:"to_amount"`
		Rate         string `json:"rate"` // units of the received currency per 1 unit given (used when to_amount is empty)
		Counterparty string `json:"counterparty"`
		Notes        string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	fromAmt, ok := money(in.FromAmount)
	if !validDate(in.Date) || !isUUID(in.FromMethodID) || !isUUID(in.ToMethodID) || in.FromMethodID == in.ToMethodID || !ok || !fromAmt.IsPositive() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		methods, err := loadMethods(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		fm, ok1 := methods[in.FromMethodID]
		tm, ok2 := methods[in.ToMethodID]
		if !ok1 || !ok2 || !fm.Active || !tm.Active || fm.Kind == "cheque" || tm.Kind == "cheque" {
			return nil, badRequest("invalid_tender")
		}
		if fm.Currency == tm.Currency {
			return nil, badRequest("same_currency")
		}
		toDec, err := currencyDecimals(ctx, tx, tm.Currency)
		if err != nil {
			return nil, err
		}
		var toAmt decimal.Decimal
		if strings.TrimSpace(in.ToAmount) != "" {
			a, ok := money(in.ToAmount)
			if !ok || !a.IsPositive() {
				return nil, badRequest("invalid_amount")
			}
			toAmt = a
		} else {
			rt, err := decimal.NewFromString(strings.TrimSpace(in.Rate))
			if err != nil || !rt.IsPositive() {
				return nil, badRequest("invalid_rate")
			}
			toAmt = fromAmt.Mul(rt).Round(toDec)
			if !toAmt.IsPositive() {
				return nil, badRequest("invalid_amount")
			}
		}
		rate := toAmt.DivRound(fromAmt, 10)
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'exchange')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		memo := fmt.Sprintf("Exchange #%d · %s %s → %s %s", number, fromAmt.String(), fm.Currency, toAmt.String(), tm.Currency)
		je, err := postJournal(ctx, tx, cid, set, in.Date, memo, fmt.Sprintf("FX-%d", number), "exchange", []jline{
			{Account: tm.Account, Desc: tm.Name, Currency: tm.Currency, Debit: toAmt},
			{Account: fm.Account, Desc: fm.Name, Currency: fm.Currency, Credit: fromAmt},
		})
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO currency_exchanges (company_id, number, exchange_date, from_method_id, to_method_id,
			from_currency, from_amount, to_currency, to_amount, rate, counterparty, journal_entry_id, notes)
			VALUES ($1, $2, $3::date, $4, $5, $6, $7::numeric, $8, $9::numeric, $10::numeric, $11, $12, $13) RETURNING id::text`,
			cid, number, in.Date, fm.ID, tm.ID, fm.Currency, fromAmt.String(), tm.Currency, toAmt.String(), rate.String(),
			strings.TrimSpace(in.Counterparty), je, strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+exchangeCols+exchangeFrom+` WHERE x.id = $1`, id)
	})
}

// POST /companies/{cid}/exchanges/{id}/void
func (s *Server) voidExchange(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		var number int64
		var je *string
		err := tx.QueryRow(ctx, `SELECT status, number, journal_entry_id::text FROM currency_exchanges WHERE id = $1 AND company_id = $2 FOR UPDATE`,
			id, cid).Scan(&status, &number, &je)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "posted" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		var rev *string
		if je != nil {
			var rid string
			if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, NULL, $2)::text`, *je, fmt.Sprintf("Void exchange #%d", number)).Scan(&rid); err != nil {
				return nil, err
			}
			rev = &rid
		}
		if _, err := tx.Exec(ctx, `UPDATE currency_exchanges SET status = 'void', void_entry_id = $2 WHERE id = $1`, id, rev); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+exchangeCols+exchangeFrom+` WHERE x.id = $1`, id)
	})
}

// fiscalStart: the company's fiscal year start (month, day); default 1 January.
func fiscalStart(ctx context.Context, tx pgx.Tx, cid string) (int, int, error) {
	var fs string
	if err := tx.QueryRow(ctx, `SELECT fiscal_year_start FROM companies WHERE id = $1`, cid).Scan(&fs); err != nil {
		if err == pgx.ErrNoRows {
			return 0, 0, notFound()
		}
		return 0, 0, err
	}
	t, err := time.Parse("01-02", fs)
	if err != nil || (t.Month() == 2 && t.Day() > 28) {
		return 1, 1, nil
	}
	return int(t.Month()), t.Day(), nil
}

// fiscalRange: first and last day of fiscal year y (the fiscal year that starts in calendar year y).
func fiscalRange(ctx context.Context, tx pgx.Tx, cid string, y int) (string, string, error) {
	mm, dd, err := fiscalStart(ctx, tx, cid)
	if err != nil {
		return "", "", err
	}
	s := time.Date(y, time.Month(mm), dd, 0, 0, 0, 0, time.UTC)
	return s.Format("2006-01-02"), s.AddDate(1, 0, -1).Format("2006-01-02"), nil
}
