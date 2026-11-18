package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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
}

const companyCols = `id::text, name_en, name_ar, base_currency::text, local_currency::text, is_active, created_at`

func (s *Server) listCompanies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out []company
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+companyCols+` FROM companies ORDER BY name_en`)
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
	}
	if err := decode(w, r, &in); err != nil || strings.TrimSpace(in.NameEn) == "" {
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
		rows, err := tx.Query(ctx,
			`INSERT INTO companies (name_en, name_ar, base_currency, local_currency) VALUES ($1, $2, $3, $4) RETURNING `+companyCols,
			strings.TrimSpace(in.NameEn), strings.TrimSpace(in.NameAr), in.BaseCurrency, in.LocalCurrency)
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
	}
	if err := decode(w, r, &in); err != nil || (in.NameEn != nil && strings.TrimSpace(*in.NameEn) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var c company
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE companies
			SET name_en = coalesce(trim($2), name_en),
			    name_ar = coalesce(trim($3), name_ar),
			    is_active = coalesce($4, is_active)
			WHERE id = $1
			RETURNING `+companyCols, id, in.NameEn, in.NameAr, in.IsActive)
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
