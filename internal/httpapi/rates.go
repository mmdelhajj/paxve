package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

type currency struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Symbol   string `json:"symbol"`
	Decimals int16  `json:"decimals"`
	NameAr   string `json:"name_ar"`
	NameFr   string `json:"name_fr"`
}

func (s *Server) listCurrencies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out []currency
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT code::text, name, symbol, decimals, name_ar, name_fr FROM currencies ORDER BY code`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[currency])
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// companyBase checks the company exists for this tenant and returns its base currency.
func companyBase(ctx context.Context, tx pgx.Tx, cid string) (string, error) {
	if !isUUID(cid) {
		return "", notFound()
	}
	var base string
	err := tx.QueryRow(ctx, `SELECT base_currency::text FROM companies WHERE id = $1`, cid).Scan(&base)
	if err == pgx.ErrNoRows {
		return "", notFound()
	}
	return base, err
}

type rate struct {
	ID       string    `json:"id"`
	Currency string    `json:"currency"`
	Date     string    `json:"date"`
	Kind     string    `json:"kind"`
	Rate     string    `json:"rate"`
	Updated  time.Time `json:"updated_at"`
	Source   string    `json:"source"` // manual | auto
}

const rateCols = `id::text, currency::text, rate_date::text, kind, trim_scale(rate)::text, updated_at, source`

func (s *Server) listRates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid := r.PathValue("cid")
	cur := r.URL.Query().Get("currency")
	out := []rate{}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+rateCols+` FROM exchange_rates
			WHERE company_id = $1 AND ($2 = '' OR currency = $2)
			ORDER BY rate_date DESC, currency LIMIT 500`, cid, cur)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[rate])
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// latestRates returns the newest official rate per currency on or before ?date (default today).
func (s *Server) latestRates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid := r.PathValue("cid")
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format(time.DateOnly)
	} else if _, err := time.Parse(time.DateOnly, date); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var base string
	out := []rate{}
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		var err error
		if base, err = companyBase(ctx, tx, cid); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (currency) `+rateCols+` FROM exchange_rates
			WHERE company_id = $1 AND kind = 'official' AND rate_date <= $2::date
			ORDER BY currency, rate_date DESC`, cid, date)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[rate])
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"base_currency": base, "date": date, "rates": out})
}

// upsertRate sets the rate of a currency for a date (correcting it if it already exists).
func (s *Server) upsertRate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid := r.PathValue("cid")
	var in struct {
		Currency string `json:"currency"`
		Date     string `json:"date"`
		Kind     string `json:"kind"`
		Rate     string `json:"rate"`
	}
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Kind == "" {
		in.Kind = "official"
	}
	if in.Date == "" {
		in.Date = time.Now().Format(time.DateOnly)
	}
	d, err := decimal.NewFromString(in.Rate)
	if _, perr := time.Parse(time.DateOnly, in.Date); err != nil || perr != nil || !d.IsPositive() ||
		!currencyRe.MatchString(in.Currency) || (in.Kind != "official" && in.Kind != "buy" && in.Kind != "sell") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var out rate
	err = db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return err
		}
		if base == in.Currency {
			return badRequest("same_as_base")
		}
		rows, err := tx.Query(ctx, `
			INSERT INTO exchange_rates (company_id, currency, rate_date, kind, rate)
			VALUES ($1, $2, $3::date, $4, $5::numeric)
			ON CONFLICT (tenant_id, company_id, currency, kind, rate_date) DO UPDATE SET rate = EXCLUDED.rate, source = 'manual'
			RETURNING `+rateCols, cid, in.Currency, in.Date, in.Kind, d.String())
		if err != nil {
			return err
		}
		out, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[rate])
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
