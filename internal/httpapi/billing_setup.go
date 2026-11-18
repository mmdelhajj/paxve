package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ---- billing settings (default accounts) ----

func (s *Server) getBillingSettings(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT base_currency, local_currency, ar_account_id, fx_gain_account_id,
			fx_loss_account_id, default_revenue_account_id, show_local_currency_on_bills FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}

func (s *Server) putBillingSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AR        *string `json:"ar_account_id"`
		FXGain    *string `json:"fx_gain_account_id"`
		FXLoss    *string `json:"fx_loss_account_id"`
		Revenue   *string `json:"default_revenue_account_id"`
		ShowLocal *bool   `json:"show_local_currency_on_bills"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	for _, p := range []*string{in.AR, in.FXGain, in.FXLoss, in.Revenue} {
		if !optUUID(p) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		for _, p := range []*string{in.AR, in.FXGain, in.FXLoss, in.Revenue} {
			if p != nil && *p != "" {
				if err := requirePostable(ctx, tx, cid, *p); err != nil {
					return nil, err
				}
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE companies SET
			ar_account_id = CASE WHEN $2::text IS NULL THEN ar_account_id ELSE nullif($2, '')::uuid END,
			fx_gain_account_id = CASE WHEN $3::text IS NULL THEN fx_gain_account_id ELSE nullif($3, '')::uuid END,
			fx_loss_account_id = CASE WHEN $4::text IS NULL THEN fx_loss_account_id ELSE nullif($4, '')::uuid END,
			default_revenue_account_id = CASE WHEN $5::text IS NULL THEN default_revenue_account_id ELSE nullif($5, '')::uuid END,
			show_local_currency_on_bills = coalesce($6, show_local_currency_on_bills)
			WHERE id = $1`, cid, in.AR, in.FXGain, in.FXLoss, in.Revenue, in.ShowLocal)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT base_currency, local_currency, ar_account_id, fx_gain_account_id,
			fx_loss_account_id, default_revenue_account_id, show_local_currency_on_bills FROM companies WHERE id = $1`, cid)
	})
}

func requirePostable(ctx context.Context, tx pgx.Tx, cid, account string) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $1 AND company_id = $2 AND is_postable AND is_active)`,
		account, cid).Scan(&ok)
	if err == nil && !ok {
		return badRequest("invalid_account")
	}
	return err
}

// ---- payment methods ----

const methodCols = `m.id, m.name, m.kind, m.currency, m.account_id, a.code AS account_code, a.name AS account_name, m.is_active, m.sort,
	m.user_id, (SELECT full_name FROM users WHERE id = m.user_id) AS user_name, trim_scale(m.change_rounding)::text AS change_rounding`

func (s *Server) listMethods(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+methodCols+` FROM payment_methods m JOIN accounts a ON a.id = m.account_id
			WHERE m.company_id = $1 AND (NOT $2 OR m.user_id IS NULL OR m.user_id = app_user())
			ORDER BY m.is_active DESC, m.sort, m.name`, r.PathValue("cid"), isCollector(r))
	})
}

type methodIn struct {
	Name      *string `json:"name"`
	Kind      *string `json:"kind"`
	Currency  *string `json:"currency"`
	AccountID *string `json:"account_id"`
	IsActive  *bool   `json:"is_active"`
	Sort      *int    `json:"sort"`
	UserID    *string `json:"user_id"` // "" = shared box
	// ChangeRounding: change given from this box is rounded down to this step (e.g. 1000 or 5000 LBP); "" = none
	ChangeRounding *string `json:"change_rounding"`
}

var methodKinds = map[string]bool{"cash": true, "wallet": true, "bank": true, "card": true}

func (in *methodIn) valid(create bool) bool {
	if create && (in.Name == nil || in.Currency == nil || in.AccountID == nil) {
		return false
	}
	return (in.Name == nil || strings.TrimSpace(*in.Name) != "") &&
		(in.Kind == nil || methodKinds[*in.Kind]) &&
		(in.Currency == nil || currencyRe.MatchString(*in.Currency)) &&
		(in.AccountID == nil || isUUID(*in.AccountID)) && optUUID(in.UserID) && validStep(in.ChangeRounding)
}

func validStep(p *string) bool {
	if p == nil || *p == "" {
		return true
	}
	d, ok := money(*p)
	return ok && d.IsPositive()
}

func (s *Server) createMethod(w http.ResponseWriter, r *http.Request) {
	var in methodIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid(true) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	kind := "cash"
	if in.Kind != nil {
		kind = *in.Kind
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := requirePostable(ctx, tx, cid, *in.AccountID); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO payment_methods (company_id, name, kind, currency, account_id, sort, user_id, change_rounding)
			VALUES ($1, $2, $3, $4, $5, coalesce($6, 0), nullif($7, '')::uuid, nullif($8, '')::numeric) RETURNING id::text`,
			cid, strings.TrimSpace(*in.Name), kind, *in.Currency, *in.AccountID, in.Sort, deref(in.UserID), deref(in.ChangeRounding)).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+methodCols+` FROM payment_methods m JOIN accounts a ON a.id = m.account_id WHERE m.id = $1`, id)
	})
}

func (s *Server) updateMethod(w http.ResponseWriter, r *http.Request) {
	var in methodIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !in.valid(false) || !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.AccountID != nil {
			if err := requirePostable(ctx, tx, cid, *in.AccountID); err != nil {
				return nil, err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE payment_methods SET name = coalesce(trim($3), name), kind = coalesce($4, kind),
			currency = coalesce($5, currency), account_id = coalesce($6::uuid, account_id),
			is_active = coalesce($7, is_active), sort = coalesce($8, sort),
			user_id = CASE WHEN $9::text IS NULL THEN user_id ELSE nullif($9, '')::uuid END,
			change_rounding = CASE WHEN $10::text IS NULL THEN change_rounding ELSE nullif($10, '')::numeric END
			WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.Kind, in.Currency, in.AccountID, in.IsActive, in.Sort, in.UserID, in.ChangeRounding)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+methodCols+` FROM payment_methods m JOIN accounts a ON a.id = m.account_id WHERE m.id = $1`, id)
	})
}

// ---- areas ----

const areaCols = `ar.id, ar.name, ar.collector_id, u.full_name AS collector_name, ar.is_active,
	(SELECT count(*) FROM customers c WHERE c.area_id = ar.id AND c.status <> 'closed') AS customers`

func (s *Server) listAreas(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+areaCols+` FROM areas ar LEFT JOIN users u ON u.id = ar.collector_id
			WHERE ar.company_id = $1 ORDER BY ar.is_active DESC, ar.name`, r.PathValue("cid"))
	})
}

type areaIn struct {
	Name        *string `json:"name"`
	CollectorID *string `json:"collector_id"`
	IsActive    *bool   `json:"is_active"`
}

func (s *Server) createArea(w http.ResponseWriter, r *http.Request) {
	var in areaIn
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" || !optUUID(in.CollectorID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO areas (company_id, name, collector_id) VALUES ($1, $2, nullif($3, '')::uuid) RETURNING id::text`,
			cid, strings.TrimSpace(*in.Name), deref(in.CollectorID)).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+areaCols+` FROM areas ar LEFT JOIN users u ON u.id = ar.collector_id WHERE ar.id = $1`, id)
	})
}

func (s *Server) updateArea(w http.ResponseWriter, r *http.Request) {
	var in areaIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") || !optUUID(in.CollectorID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE areas SET name = coalesce(trim($3), name),
			collector_id = CASE WHEN $4::text IS NULL THEN collector_id ELSE nullif($4, '')::uuid END,
			is_active = coalesce($5, is_active) WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.CollectorID, in.IsActive)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+areaCols+` FROM areas ar LEFT JOIN users u ON u.id = ar.collector_id WHERE ar.id = $1`, id)
	})
}

// ---- plans ----

const planCols = `p.id, p.name, p.billing_type, p.currency, p.base_fee::text AS base_fee, p.unit_price::text AS unit_price,
	p.unit_label, p.revenue_account_id, a.code AS revenue_account_code, p.is_active, p.tax_rate_id,
	(SELECT t.name FROM tax_rates t WHERE t.id = p.tax_rate_id) AS tax_rate_name,
	(SELECT count(*) FROM subscriptions s WHERE s.plan_id = p.id AND s.status = 'active') AS active_subscriptions`

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+planCols+` FROM plans p JOIN accounts a ON a.id = p.revenue_account_id
			WHERE p.company_id = $1 ORDER BY p.is_active DESC, p.name`, r.PathValue("cid"))
	})
}

type planIn struct {
	Name             *string `json:"name"`
	BillingType      *string `json:"billing_type"`
	Currency         *string `json:"currency"`
	BaseFee          *string `json:"base_fee"`
	UnitPrice        *string `json:"unit_price"`
	UnitLabel        *string `json:"unit_label"`
	RevenueAccountID *string `json:"revenue_account_id"`
	IsActive         *bool   `json:"is_active"`
	TaxRateID        *string `json:"tax_rate_id"` // "" = no tax
}

func (in *planIn) valid(create bool) bool {
	if create && (in.Name == nil || in.BillingType == nil || in.Currency == nil) {
		return false
	}
	okMoney := func(p *string) bool {
		if p == nil {
			return true
		}
		_, ok := money(*p)
		return ok
	}
	return (in.Name == nil || strings.TrimSpace(*in.Name) != "") &&
		(in.BillingType == nil || *in.BillingType == "fixed" || *in.BillingType == "metered") &&
		(in.Currency == nil || currencyRe.MatchString(*in.Currency)) &&
		okMoney(in.BaseFee) && okMoney(in.UnitPrice) && optUUID(in.RevenueAccountID) && optUUID(in.TaxRateID)
}

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	var in planIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid(true) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rev := deref(in.RevenueAccountID)
		if rev == "" && set.Revenue != nil {
			rev = *set.Revenue
		}
		if rev == "" {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "revenue"}
		}
		if err := requirePostable(ctx, tx, cid, rev); err != nil {
			return nil, err
		}
		if err := checkTaxRate(ctx, tx, cid, deref(in.TaxRateID), "sales"); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO plans (company_id, name, billing_type, currency, base_fee, unit_price, unit_label, revenue_account_id, tax_rate_id)
			VALUES ($1, $2, $3, $4, coalesce(nullif($5, '')::numeric, 0), coalesce(nullif($6, '')::numeric, 0), coalesce(nullif($7, ''), 'kWh'), $8, nullif($9, '')::uuid)
			RETURNING id::text`, cid, strings.TrimSpace(*in.Name), *in.BillingType, *in.Currency,
			deref(in.BaseFee), deref(in.UnitPrice), deref(in.UnitLabel), rev, deref(in.TaxRateID)).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+planCols+` FROM plans p JOIN accounts a ON a.id = p.revenue_account_id WHERE p.id = $1`, id)
	})
}

func (s *Server) updatePlan(w http.ResponseWriter, r *http.Request) {
	var in planIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !in.valid(false) || !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.RevenueAccountID != nil && *in.RevenueAccountID != "" {
			if err := requirePostable(ctx, tx, cid, *in.RevenueAccountID); err != nil {
				return nil, err
			}
		}
		if err := checkTaxRate(ctx, tx, cid, deref(in.TaxRateID), "sales"); err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `UPDATE plans SET tax_rate_id = CASE WHEN $11::text IS NULL THEN tax_rate_id ELSE nullif($11, '')::uuid END, name = coalesce(trim($3), name), billing_type = coalesce($4, billing_type),
			currency = coalesce($5, currency), base_fee = coalesce($6::numeric, base_fee), unit_price = coalesce($7::numeric, unit_price),
			unit_label = coalesce(nullif($8, ''), unit_label), revenue_account_id = coalesce(nullif($9, '')::uuid, revenue_account_id),
			is_active = coalesce($10, is_active) WHERE id = $1 AND company_id = $2`,
			id, cid, in.Name, in.BillingType, in.Currency, in.BaseFee, in.UnitPrice, in.UnitLabel, in.RevenueAccountID, in.IsActive, in.TaxRateID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+planCols+` FROM plans p JOIN accounts a ON a.id = p.revenue_account_id WHERE p.id = $1`, id)
	})
}
