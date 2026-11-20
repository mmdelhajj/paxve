package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/charts"

	"github.com/mmdelhajj/paxve/internal/db"
)

type company struct {
	ID            string    `json:"id"`
	NameEn        string    `json:"name_en"`
	NameAr        string    `json:"name_ar"`
	BaseCurrency  string    `json:"base_currency"`
	LocalCurrency string    `json:"local_currency"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
	LockDate      *string   `json:"lock_date"`     // books locked up to this date (inclusive)
	Apps          []string  `json:"apps"`          // enabled apps (Odoo-style modules), see apps.go
	BusinessType  string    `json:"business_type"` // generator | internet | satellite (tints the UI)
	// localization (country pack, see internal/charts/countries.go)
	Country       string `json:"country"`
	L10n          string `json:"l10n"`
	EInvoice      string `json:"einvoice"` // "" | zatca | eta
	DefaultLocale string `json:"default_locale"`
	FiscalStart   string `json:"fiscal_year_start"`
	TaxIDLabel    string `json:"tax_id_label"`
	InvoiceTitle  string `json:"invoice_title"`
	LegalMentions string `json:"legal_mentions"`
	// "today", report default dates and the status bar follow this zone, not the device's (audit B-M2)
	Timezone string `json:"timezone"`
}

const companyCols = `id::text, name_en, name_ar, base_currency::text, local_currency::text, is_active, created_at, lock_date::text,
	apps, business_type, country, l10n, einvoice, default_locale, fiscal_year_start, tax_id_label, invoice_title, legal_mentions, timezone`

func (s *Server) listCompanies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out []company
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		// users limited to some companies only see those
		rows, err := tx.Query(ctx, `SELECT `+companyCols+` FROM companies
			WHERE $1::uuid[] IS NULL OR id = ANY ($1::uuid[]) ORDER BY name_en`, principal(r).CompanyIDs)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[company])
		return err
	})
	if err != nil {
		serverErr(w, r, err)
		return
	}
	if out == nil {
		out = []company{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createCompany(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		NameEn        string `json:"name_en"`
		NameAr        string `json:"name_ar"`
		BaseCurrency  string `json:"base_currency"`
		LocalCurrency string `json:"local_currency"`
		// audit P7-F2: the new company gets its chart (the organisation's country template unless one is chosen),
		// its language and the apps of its business type, like a sign-up; no_chart keeps an empty chart (imports)
		Template     string `json:"template"`
		Locale       string `json:"locale"`
		BusinessType string `json:"business_type"`
		NoChart      bool   `json:"no_chart"`
	}
	if err := decode(w, r, &in); err != nil || strings.TrimSpace(in.NameEn) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Locale == "" {
		in.Locale = principal(r).Locale
	}
	if !supportedLocales[in.Locale] {
		in.Locale = "en"
	}
	if in.BusinessType == "" {
		in.BusinessType = "generator"
	}
	if !slices.Contains(businessTypes, in.BusinessType) || (in.Template != "" && charts.Find(in.Template) == nil) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.BaseCurrency == "" {
		in.BaseCurrency = "USD"
	}
	if in.LocalCurrency == "" {
		in.LocalCurrency = in.BaseCurrency
	}
	if !currencyRe.MatchString(in.BaseCurrency) || !currencyRe.MatchString(in.LocalCurrency) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var c company
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if err := checkTenantLimit(ctx, tx, "companies"); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`INSERT INTO companies (name_en, name_ar, base_currency, local_currency, default_locale, business_type, apps)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+companyCols,
			strings.TrimSpace(in.NameEn), strings.TrimSpace(in.NameAr), in.BaseCurrency, in.LocalCurrency, in.Locale, in.BusinessType,
			appsForBusiness(in.BusinessType))
		if err != nil {
			return err
		}
		c, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[company])
		if err != nil || in.NoChart {
			return err
		}
		if err := ensureChart(ctx, tx, c.ID, in.Template, normLang(in.Locale)); err != nil {
			return err
		}
		// the chart sets the country pack (l10n, country…): answer the company as it is now
		rows, err = tx.Query(ctx, `SELECT `+companyCols+` FROM companies WHERE id = $1`, c.ID)
		if err != nil {
			return err
		}
		c, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[company])
		return err
	})
	if isUniqueViolation(err) {
		writeErr(w, http.StatusConflict, "name_taken")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) updateCompany(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("cid")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		NameEn   *string `json:"name_en"`
		NameAr   *string `json:"name_ar"`
		IsActive *bool   `json:"is_active"`
		LockDate *string `json:"lock_date"` // "" removes the lock
		// localization
		DefaultLocale *string `json:"default_locale"`
		TaxIDLabel    *string `json:"tax_id_label"`
		InvoiceTitle  *string `json:"invoice_title"`
		LegalMentions *string `json:"legal_mentions"`
		Timezone      *string `json:"timezone"`
	}
	if err := decode(w, r, &in); err != nil || (in.NameEn != nil && strings.TrimSpace(*in.NameEn) == "") ||
		(in.LockDate != nil && *in.LockDate != "" && !validDate(*in.LockDate)) ||
		(in.DefaultLocale != nil && !supportedLocales[*in.DefaultLocale]) || (in.Timezone != nil && !validTZ(*in.Timezone)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var c company
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE companies
			SET name_en = coalesce(trim($2), name_en),
			    name_ar = coalesce(trim($3), name_ar),
			    is_active = coalesce($4, is_active),
			    lock_date = CASE WHEN $5::text IS NULL THEN lock_date ELSE nullif($5, '')::date END,
			    default_locale = coalesce($6, default_locale),
			    tax_id_label = coalesce(trim($7), tax_id_label),
			    invoice_title = coalesce(trim($8), invoice_title),
			    legal_mentions = coalesce(trim($9), legal_mentions),
			    timezone = coalesce($10, timezone)
			WHERE id = $1
			RETURNING `+companyCols, id, in.NameEn, in.NameAr, in.IsActive, in.LockDate,
			in.DefaultLocale, in.TaxIDLabel, in.InvoiceTitle, in.LegalMentions, in.Timezone)
		if err != nil {
			return err
		}
		c, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[company])
		return err
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeErr(w, http.StatusNotFound, "not_found")
	case isUniqueViolation(err):
		writeErr(w, http.StatusConflict, "name_taken")
	case err != nil:
		serverErr(w, r, err)
	default:
		writeJSON(w, http.StatusOK, c)
	}
}
