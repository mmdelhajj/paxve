package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/charts"
	"github.com/mmdelhajj/paxve/internal/db"
)

var accountCodeRe = regexp.MustCompile(`^[0-9A-Za-z.\-]{1,20}$`)
var accountTypes = map[string]bool{"asset": true, "liability": true, "equity": true, "income": true, "expense": true}

type account struct {
	ID         string            `json:"id"`
	Code       string            `json:"code"`
	Name       string            `json:"name"`
	NameI18n   map[string]string `json:"name_i18n"`
	Type       string            `json:"type"`
	ParentID   *string           `json:"parent_id"`
	IsPostable bool              `json:"is_postable"`
	Currency   *string           `json:"currency"`
	IsActive   bool              `json:"is_active"`
}

const accountCols = `id::text, code, name, name_i18n, type, parent_id::text, is_postable, currency::text, is_active`

func scanAccounts(rows pgx.Rows) ([]account, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (account, error) {
		var a account
		var i18n []byte
		err := row.Scan(&a.ID, &a.Code, &a.Name, &i18n, &a.Type, &a.ParentID, &a.IsPostable, &a.Currency, &a.IsActive)
		if err == nil {
			err = json.Unmarshal(i18n, &a.NameI18n)
		}
		return a, err
	})
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid := r.PathValue("cid")
	out := []account{}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+accountCols+` FROM accounts WHERE company_id = $1 ORDER BY code`, cid)
		if err != nil {
			return err
		}
		out, err = scanAccounts(rows)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type accountIn struct {
	Code       *string           `json:"code"`
	Name       *string           `json:"name"`
	NameI18n   map[string]string `json:"name_i18n"`
	Type       *string           `json:"type"`
	ParentID   *string           `json:"parent_id"`
	IsPostable *bool             `json:"is_postable"`
	Currency   *string           `json:"currency"`
	IsActive   *bool             `json:"is_active"`
}

func (in *accountIn) validate(create bool) error {
	if create && (in.Code == nil || in.Name == nil || in.Type == nil) {
		return badRequest("bad_request")
	}
	if in.Code != nil && !accountCodeRe.MatchString(*in.Code) {
		return badRequest("bad_request")
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return badRequest("bad_request")
	}
	if in.Type != nil && !accountTypes[*in.Type] {
		return badRequest("bad_request")
	}
	if in.ParentID != nil && *in.ParentID != "" && !isUUID(*in.ParentID) {
		return badRequest("bad_request")
	}
	if in.Currency != nil && *in.Currency != "" && !currencyRe.MatchString(*in.Currency) {
		return badRequest("bad_request")
	}
	for k := range in.NameI18n {
		if !regexp.MustCompile(`^[a-z]{2}$`).MatchString(k) {
			return badRequest("bad_request")
		}
	}
	return nil
}

func checkParent(r *http.Request, tx pgx.Tx, cid string, parent *string) error {
	if parent == nil || *parent == "" {
		return nil
	}
	var ok bool
	err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $1 AND company_id = $2)`, *parent, cid).Scan(&ok)
	if err == nil && !ok {
		return badRequest("invalid_parent")
	}
	return err
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid := r.PathValue("cid")
	var in accountIn
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := in.validate(true); err != nil {
		fail(w, r, err)
		return
	}
	if in.NameI18n == nil {
		in.NameI18n = map[string]string{}
	}
	i18n, _ := json.Marshal(in.NameI18n)
	postable := in.IsPostable == nil || *in.IsPostable
	var out account
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return err
		}
		if err := checkParent(r, tx, cid, in.ParentID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			INSERT INTO accounts (company_id, code, name, name_i18n, type, parent_id, is_postable, currency)
			VALUES ($1, $2, $3, $4, $5, nullif($6, '')::uuid, $7, nullif($8, ''))
			RETURNING `+accountCols,
			cid, *in.Code, strings.TrimSpace(*in.Name), i18n, *in.Type, deref(in.ParentID), postable, deref(in.Currency))
		if err != nil {
			return err
		}
		list, err := scanAccounts(rows)
		if err == nil {
			out = list[0]
		}
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in accountIn
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := in.validate(false); err != nil {
		fail(w, r, err)
		return
	}
	if in.ParentID != nil && *in.ParentID == id {
		writeErr(w, http.StatusBadRequest, "invalid_parent")
		return
	}
	var i18n []byte
	if in.NameI18n != nil {
		i18n, _ = json.Marshal(in.NameI18n)
	}
	var out account
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if err := checkParent(r, tx, cid, in.ParentID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			UPDATE accounts SET
			  code = coalesce($3, code),
			  name = coalesce(trim($4), name),
			  name_i18n = coalesce($5::jsonb, name_i18n),
			  type = coalesce($6, type),
			  parent_id = CASE WHEN $7::text IS NULL THEN parent_id ELSE nullif($7, '')::uuid END,
			  is_postable = coalesce($8, is_postable),
			  currency = CASE WHEN $9::text IS NULL THEN currency ELSE nullif($9, '') END,
			  is_active = coalesce($10, is_active)
			WHERE id = $1 AND company_id = $2
			RETURNING `+accountCols,
			id, cid, in.Code, in.Name, i18n, in.Type, in.ParentID, in.IsPostable, in.Currency, in.IsActive)
		if err != nil {
			return err
		}
		list, err := scanAccounts(rows)
		if err == nil && len(list) == 0 {
			return notFound()
		}
		if err == nil {
			out = list[0]
		}
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listChartTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, charts.All)
}

// loadChartTemplate fills an empty chart of accounts from a template.
func (s *Server) loadChartTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid := r.PathValue("cid")
	var in struct {
		Template string `json:"template"`
		Language string `json:"language"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	tpl := charts.Find(in.Template)
	if tpl == nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !supportedLocales[in.Language] {
		in.Language = "en"
	}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE company_id = $1`, cid).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &apiError{status: http.StatusConflict, code: "chart_not_empty"}
		}
		ids := map[string]string{}
		for _, acc := range tpl.Accounts {
			i18n, _ := json.Marshal(acc.Names)
			var id string
			if err := tx.QueryRow(ctx, `
				INSERT INTO accounts (company_id, code, name, name_i18n, type, parent_id, is_postable)
				VALUES ($1, $2, $3, $4, $5, nullif($6, '')::uuid, $7) RETURNING id::text`,
				cid, acc.Code, acc.Names[in.Language], i18n, acc.Type, ids[acc.Parent], acc.Postable).Scan(&id); err != nil {
				return err
			}
			ids[acc.Code] = id
		}
		return applyBillingDefaults(ctx, tx, cid, tpl, ids, in.Language)
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": len(tpl.Accounts)})
}

// applyBillingDefaults points the company's billing settings at the template's
// accounts and creates the default payment methods (cash boxes, wallet, bank).
func applyBillingDefaults(ctx context.Context, tx pgx.Tx, cid string, tpl *charts.Template, ids map[string]string, lang string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE companies SET
		  ar_account_id = coalesce(ar_account_id, nullif($2, '')::uuid),
		  fx_gain_account_id = coalesce(fx_gain_account_id, nullif($3, '')::uuid),
		  fx_loss_account_id = coalesce(fx_loss_account_id, nullif($4, '')::uuid),
		  default_revenue_account_id = coalesce(default_revenue_account_id, nullif($5, '')::uuid)
		WHERE id = $1`, cid, ids[tpl.AR], ids[tpl.FXGain], ids[tpl.FXLoss], ids[tpl.Revenue]); err != nil {
		return err
	}
	var base string
	if err := tx.QueryRow(ctx, `SELECT base_currency::text FROM companies WHERE id = $1`, cid).Scan(&base); err != nil {
		return err
	}
	for i, pm := range tpl.Methods {
		cur := pm.Currency
		if cur == "" {
			cur = base
		}
		if ids[pm.Code] == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_methods (company_id, name, kind, currency, account_id, sort)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`,
			cid, pm.Names[lang], pm.Kind, cur, ids[pm.Code], i); err != nil {
			return err
		}
	}
	return nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
