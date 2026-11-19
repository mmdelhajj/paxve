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

// Fixed assets, straight-line monthly depreciation (in base currency) and disposal.

const assetCols = `a.id, a.code, a.name, a.category, a.purchase_date, a.currency, a.cost::text AS cost, a.cost_base::text AS cost_base,
	a.salvage_base::text AS salvage_base, a.useful_life_months, a.start_period, a.asset_account_id, aa.code AS asset_account_code,
	a.accum_account_id, a.expense_account_id, a.status, a.disposal_date, a.disposal_proceeds_base::text AS disposal_proceeds_base,
	a.notes, a.created_at, a.cost_center_id, a.method, trim_scale(a.declining_factor)::text AS declining_factor,
	coalesce((SELECT sum(d.amount) FROM depreciation_lines d WHERE d.asset_id = a.id), 0)::text AS accumulated,
	(a.cost_base - coalesce((SELECT sum(d.amount) FROM depreciation_lines d WHERE d.asset_id = a.id), 0))::text AS net_book_value,
	round((a.cost_base - a.salvage_base) / a.useful_life_months, 2)::text AS monthly,
	(SELECT max(d.period) FROM depreciation_lines d WHERE d.asset_id = a.id) AS last_period,
	(SELECT count(*) FROM depreciation_lines d WHERE d.asset_id = a.id) AS months_done`

const assetFrom = ` FROM assets a JOIN accounts aa ON aa.id = a.asset_account_id`

func (s *Server) listAssets(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, r.PathValue("cid"))
		if err != nil {
			return nil, err
		}
		rows, err := queryJSON(ctx, tx, `SELECT `+assetCols+assetFrom+` WHERE a.company_id = $1 AND ($2 = '' OR a.status = $2)
			ORDER BY a.status, a.code`, r.PathValue("cid"), status)
		if err != nil {
			return nil, err
		}
		return map[string]any{"base_currency": base, "rows": rows}, nil
	})
}

func (s *Server) getAsset(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		a, err := queryJSONOne(ctx, tx, `SELECT `+assetCols+assetFrom+` WHERE a.id = $1 AND a.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		lines, err := queryJSON(ctx, tx, `SELECT period, amount::text AS amount, journal_entry_id FROM depreciation_lines
			WHERE asset_id = $1 ORDER BY period`, id)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"asset": a, "depreciation": lines}, nil
	})
}

type assetIn struct {
	Code             *string `json:"code"`
	Name             *string `json:"name"`
	Category         *string `json:"category"`
	PurchaseDate     *string `json:"purchase_date"`
	Currency         *string `json:"currency"`
	Cost             *string `json:"cost"`
	Salvage          *string `json:"salvage"` // base currency
	UsefulLifeMonths *int    `json:"useful_life_months"`
	StartPeriod      *string `json:"start_period"`
	AssetAccountID   *string `json:"asset_account_id"`
	AccumAccountID   *string `json:"accum_account_id"`
	ExpenseAccountID *string `json:"expense_account_id"`
	PaidFromMethodID *string `json:"paid_from_method_id"` // create only: record the purchase (Dr asset / Cr box)
	Notes            *string `json:"notes"`
	CostCenterID     *string `json:"cost_center_id"`   // depreciation expense is tagged with it
	Method           *string `json:"method"`           // straight_line (default) | declining
	DecliningFactor  *string `json:"declining_factor"` // declining balance: 2 = double declining
}

func (in *assetIn) validMethod() bool {
	if in.Method != nil && *in.Method != "" && *in.Method != "straight_line" && *in.Method != "declining" {
		return false
	}
	if in.DecliningFactor != nil && *in.DecliningFactor != "" {
		f, ok := money(*in.DecliningFactor)
		return ok && f.IsPositive() && f.LessThanOrEqual(decimal.NewFromInt(5))
	}
	return true
}

// createAsset registers an asset; optionally books its purchase from a cash box / bank.
func (s *Server) createAsset(w http.ResponseWriter, r *http.Request) {
	var in assetIn
	if !decodeBody(w, r, &in) {
		return
	}
	cost, okCost := money(deref(in.Cost))
	salvage := decimal.Zero
	if strings.TrimSpace(deref(in.Salvage)) != "" {
		var ok bool
		if salvage, ok = money(*in.Salvage); !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" || in.PurchaseDate == nil || !validDate(*in.PurchaseDate) ||
		in.Currency == nil || !currencyRe.MatchString(*in.Currency) || !okCost || !cost.IsPositive() ||
		in.UsefulLifeMonths == nil || *in.UsefulLifeMonths < 1 || *in.UsefulLifeMonths > 1200 ||
		in.AssetAccountID == nil || !isUUID(*in.AssetAccountID) || !optUUID(in.AccumAccountID) || !optUUID(in.ExpenseAccountID) ||
		!optUUID(in.PaidFromMethodID) || !optUUID(in.CostCenterID) || !in.validMethod() || (in.StartPeriod != nil && *in.StartPeriod != "" && !validPeriod(*in.StartPeriod)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBOSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		accum, expense := deref(in.AccumAccountID), deref(in.ExpenseAccountID)
		if accum == "" && set.Accum != nil {
			accum = *set.Accum
		}
		if expense == "" && set.DepExp != nil {
			expense = *set.DepExp
		}
		if accum == "" || expense == "" {
			return nil, notSetup("depreciation")
		}
		for _, a := range []string{*in.AssetAccountID, accum, expense} {
			if err := requirePostable(ctx, tx, cid, a); err != nil {
				return nil, err
			}
		}
		rate, err := resolveRate(ctx, tx, cid, set.Base, *in.Currency, *in.PurchaseDate, "")
		if err != nil {
			return nil, err
		}
		rt, _ := decimal.NewFromString(rate)
		costBase := cost.DivRound(rt, 4)
		if !salvage.LessThan(costBase) {
			return nil, badRequest("salvage_too_high")
		}
		start := deref(in.StartPeriod)
		if start == "" {
			start = (*in.PurchaseDate)[:7]
		}
		code := strings.TrimSpace(deref(in.Code))
		if code == "" {
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'asset')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			code = "FA-" + strconv.FormatInt(n, 10)
		}
		var je *string
		if m := deref(in.PaidFromMethodID); m != "" {
			methods, err := loadMethods(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			pm, ok := methods[m]
			if !ok || !pm.Active {
				return nil, badRequest("invalid_tender")
			}
			if pm.Currency != *in.Currency {
				return nil, badRequest("currency_mismatch")
			}
			id, err := postJournal(ctx, tx, cid, set.billingSettings, *in.PurchaseDate, "Asset purchase · "+strings.TrimSpace(*in.Name),
				code, "asset", []jline{
					{Account: *in.AssetAccountID, Desc: strings.TrimSpace(*in.Name), Currency: *in.Currency, Debit: cost, Rate: rate},
					{Account: pm.Account, Desc: pm.Name, Currency: pm.Currency, Credit: cost, Rate: rate},
				})
			if err != nil {
				return nil, err
			}
			je = &id
		}
		var id string
		err = tx.QueryRow(ctx, `INSERT INTO assets (company_id, code, name, category, purchase_date, currency, cost, cost_base, salvage_base,
			useful_life_months, start_period, asset_account_id, accum_account_id, expense_account_id, purchase_entry_id, notes)
			VALUES ($1, $2, $3, $4, $5::date, $6, $7::numeric, $8::numeric, $9::numeric, $10, $11, $12, $13, $14, $15, $16) RETURNING id::text`,
			cid, code, strings.TrimSpace(*in.Name), strings.TrimSpace(deref(in.Category)), *in.PurchaseDate, *in.Currency, cost.String(),
			costBase.String(), salvage.String(), *in.UsefulLifeMonths, start, *in.AssetAccountID, accum, expense, je, deref(in.Notes)).Scan(&id)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "code_taken"}
		}
		if err != nil {
			return nil, err
		}
		if c := deref(in.CostCenterID); c != "" {
			if _, err := tx.Exec(ctx, `UPDATE assets SET cost_center_id = $2::uuid WHERE id = $1`, id, c); err != nil {
				return nil, err
			}
		}
		if m := deref(in.Method); m != "" {
			if _, err := tx.Exec(ctx, `UPDATE assets SET method = $2, declining_factor = coalesce(nullif($3, '')::numeric, declining_factor) WHERE id = $1`,
				id, m, deref(in.DecliningFactor)); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+assetCols+assetFrom+` WHERE a.id = $1`, id)
	})
}

// updateAsset edits descriptive fields; the database refuses money/life/account changes once depreciation started.
func (s *Server) updateAsset(w http.ResponseWriter, r *http.Request) {
	var in assetIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") || !optUUID(in.CostCenterID) || !in.validMethod() ||
		(in.UsefulLifeMonths != nil && (*in.UsefulLifeMonths < 1 || *in.UsefulLifeMonths > 1200)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var salvage *string
	if in.Salvage != nil && strings.TrimSpace(*in.Salvage) != "" {
		if _, ok := money(*in.Salvage); !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		salvage = in.Salvage
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE assets SET name = coalesce(trim($3), name), category = coalesce(trim($4), category),
			notes = coalesce($5, notes), useful_life_months = coalesce($6, useful_life_months),
			salvage_base = coalesce($7::numeric, salvage_base),
			cost_center_id = CASE WHEN $8::text IS NULL THEN cost_center_id ELSE nullif($8, '')::uuid END,
			method = coalesce(nullif($9, ''), method), declining_factor = coalesce(nullif($10, '')::numeric, declining_factor)
			WHERE id = $1 AND company_id = $2`,
			id, cid, in.Name, in.Category, in.Notes, in.UsefulLifeMonths, salvage, in.CostCenterID, deref(in.Method), deref(in.DecliningFactor))
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+assetCols+assetFrom+` WHERE a.id = $1`, id)
	})
}

// runDepreciation books one month of depreciation for every active asset. Running the same month again does nothing.
func (s *Server) runDepreciation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Period string `json:"period"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !validPeriod(in.Period) {
		writeErr(w, http.StatusBadRequest, "invalid_period")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return depreciate(ctx, tx, cid, in.Period)
	})
}

func depreciate(ctx context.Context, tx pgx.Tx, cid, period string) (map[string]any, error) {
	set, err := loadBOSettings(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	type due struct {
		id, name, accum, expense, cc string
		amount                       decimal.Decimal
	}
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, a.name, a.accum_account_id::text, a.expense_account_id::text, coalesce(a.cost_center_id::text, ''),
		       (CASE WHEN a.method = 'declining' THEN
		          -- declining balance: net book value × factor / life, switching to straight line on what is left when that is more
		          least(greatest(round((a.cost_base - x.acc) * a.declining_factor / a.useful_life_months, 2),
		                         round((a.cost_base - a.salvage_base - x.acc) / greatest(a.useful_life_months - x.done, 1), 2)),
		                a.cost_base - a.salvage_base - x.acc)
		        ELSE least(round((a.cost_base - a.salvage_base) / a.useful_life_months, 2), a.cost_base - a.salvage_base - x.acc) END)::text
		FROM assets a
		CROSS JOIN LATERAL (SELECT coalesce(sum(d.amount), 0) AS acc, count(*) AS done FROM depreciation_lines d WHERE d.asset_id = a.id) x
		WHERE a.company_id = $1 AND a.status = 'active' AND a.start_period <= $2
		  AND NOT EXISTS (SELECT 1 FROM depreciation_lines d WHERE d.asset_id = a.id AND d.period = $2)
		ORDER BY a.code
		FOR UPDATE OF a`, cid, period)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		var amt string
		err := row.Scan(&d.id, &d.name, &d.accum, &d.expense, &d.cc, &amt)
		d.amount, _ = decimal.NewFromString(amt)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	lines := []jline{}
	todo := []due{}
	total := decimal.Zero
	for _, d := range list {
		if !d.amount.IsPositive() {
			continue // fully depreciated
		}
		todo = append(todo, d)
		total = total.Add(d.amount)
		lines = append(lines,
			jline{Account: d.expense, Desc: d.name, Currency: set.Base, Debit: d.amount, Rate: "1", CostCenter: d.cc},
			jline{Account: d.accum, Desc: d.name, Currency: set.Base, Credit: d.amount, Rate: "1"})
	}
	if len(todo) == 0 {
		return map[string]any{"period": period, "assets": 0, "total": "0", "journal_entry_id": nil}, nil
	}
	je, err := postJournal(ctx, tx, cid, set.billingSettings, periodEnd(period), "Depreciation "+period, "DEP-"+period, "depreciation", lines)
	if err != nil {
		return nil, err
	}
	for _, d := range todo {
		if _, err := tx.Exec(ctx, `INSERT INTO depreciation_lines (asset_id, period, amount, journal_entry_id) VALUES ($1, $2, $3::numeric, $4)`,
			d.id, period, d.amount.String(), je); err != nil {
			return nil, err
		}
	}
	return map[string]any{"period": period, "assets": len(todo), "total": total.StringFixed(2), "journal_entry_id": je}, nil
}

// disposeAsset sells or scraps an asset: removes cost and accumulated depreciation, books proceeds and gain/loss.
func (s *Server) disposeAsset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date     string `json:"date"`
		Proceeds string `json:"proceeds"`
		MethodID string `json:"method_id"`
		Notes    string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	proceeds := decimal.Zero
	if strings.TrimSpace(in.Proceeds) != "" {
		var ok bool
		if proceeds, ok = money(in.Proceeds); !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !validDate(in.Date) || (proceeds.IsPositive() && !isUUID(in.MethodID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBOSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var name, assetAcct, accumAcct, status, costS, accS string
		err = tx.QueryRow(ctx, `SELECT a.name, a.asset_account_id::text, a.accum_account_id::text, a.status, a.cost_base::text,
			coalesce((SELECT sum(d.amount) FROM depreciation_lines d WHERE d.asset_id = a.id), 0)::text
			FROM assets a WHERE a.id = $1 AND a.company_id = $2 FOR UPDATE`, id, cid).Scan(&name, &assetAcct, &accumAcct, &status, &costS, &accS)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "active" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		cost, _ := decimal.NewFromString(costS)
		acc, _ := decimal.NewFromString(accS)
		lines := []jline{
			{Account: accumAcct, Desc: name, Currency: set.Base, Debit: acc, Rate: "1"},
			{Account: assetAcct, Desc: name, Currency: set.Base, Credit: cost, Rate: "1"},
		}
		proceedsBase := decimal.Zero
		if proceeds.IsPositive() {
			methods, err := loadMethods(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			m, ok := methods[in.MethodID]
			if !ok || !m.Active {
				return nil, badRequest("invalid_tender")
			}
			rate, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, in.Date, "")
			if err != nil {
				return nil, err
			}
			rt, _ := decimal.NewFromString(rate)
			proceedsBase = proceeds.DivRound(rt, 4)
			lines = append(lines, jline{Account: m.Account, Desc: m.Name, Currency: m.Currency, Debit: proceeds, Rate: rate})
		}
		result := proceedsBase.Add(acc).Sub(cost) // > 0 gain, < 0 loss
		switch {
		case result.IsPositive():
			if err := need(set.Gain, "asset_gain"); err != nil {
				return nil, err
			}
			lines = append(lines, jline{Account: *set.Gain, Desc: "Gain on disposal · " + name, Currency: set.Base, Credit: result, Rate: "1"})
		case result.IsNegative():
			if err := need(set.Loss, "asset_loss"); err != nil {
				return nil, err
			}
			lines = append(lines, jline{Account: *set.Loss, Desc: "Loss on disposal · " + name, Currency: set.Base, Debit: result.Neg(), Rate: "1"})
		}
		je, err := postJournal(ctx, tx, cid, set.billingSettings, in.Date, "Disposal · "+name, "DISP", "asset_disposal", lines)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE assets SET status = 'disposed', disposal_date = $2::date, disposal_proceeds_base = $3::numeric,
			disposal_entry_id = $4, notes = CASE WHEN $5 = '' THEN notes ELSE trim(notes || ' ' || $5) END WHERE id = $1`,
			id, in.Date, proceedsBase.String(), je, strings.TrimSpace(in.Notes)); err != nil {
			return nil, err
		}
		out, err := queryJSONOne(ctx, tx, `SELECT `+assetCols+assetFrom+` WHERE a.id = $1`, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"asset": out, "result": result.StringFixed(2), "journal_entry_id": je}, nil
	})
}

// assetRegister: cost, accumulated depreciation and net book value at a date.
func (s *Server) assetRegister(w http.ResponseWriter, r *http.Request) {
	to := r.URL.Query().Get("to")
	if to == "" {
		to = today()
	}
	if !validDate(to) {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		toPeriod := to[:7]
		rows, err := queryJSON(ctx, tx, `
			SELECT a.id, a.code, a.name, a.category, a.purchase_date, a.status, a.cost_base::text AS cost,
			       coalesce(sum(d.amount) FILTER (WHERE d.period <= $2), 0)::text AS accumulated,
			       (a.cost_base - coalesce(sum(d.amount) FILTER (WHERE d.period <= $2), 0))::text AS net_book_value,
			       a.useful_life_months, count(d.id) FILTER (WHERE d.period <= $2) AS months_done
			FROM assets a LEFT JOIN depreciation_lines d ON d.asset_id = a.id
			WHERE a.company_id = $1 AND a.purchase_date <= $3::date AND (a.status = 'active' OR a.disposal_date > $3::date)
			GROUP BY a.id ORDER BY a.code`, cid, toPeriod, to)
		if err != nil {
			return nil, err
		}
		var cost, acc string
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(a.cost_base), 0)::text,
			       coalesce(sum((SELECT coalesce(sum(d.amount), 0) FROM depreciation_lines d WHERE d.asset_id = a.id AND d.period <= $2)), 0)::text
			FROM assets a WHERE a.company_id = $1 AND a.purchase_date <= $3::date AND (a.status = 'active' OR a.disposal_date > $3::date)`,
			cid, toPeriod, to).Scan(&cost, &acc); err != nil {
			return nil, err
		}
		c, _ := decimal.NewFromString(cost)
		a, _ := decimal.NewFromString(acc)
		return map[string]any{"base_currency": base, "to": to, "rows": rows, "total_cost": cost, "total_accumulated": acc,
			"total_net_book_value": c.Sub(a).StringFixed(4)}, nil
	})
}
