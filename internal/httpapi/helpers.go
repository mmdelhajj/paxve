package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// run executes fn in a tenant transaction and writes its result as JSON.
func (s *Server) run(w http.ResponseWriter, r *http.Request, status int, fn func(ctx context.Context, tx pgx.Tx) (any, error)) {
	var out any
	err := db.InTenant(r.Context(), s.Pool, scope(r), func(tx pgx.Tx) error {
		var err error
		out, err = fn(r.Context(), tx)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	if raw, ok := out.(json.RawMessage); ok {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return
	}
	writeJSON(w, status, out)
}

// queryJSON returns the rows of a SELECT as a JSON array (money columns should be cast ::text).
func queryJSON(ctx context.Context, tx pgx.Tx, sql string, args ...any) (json.RawMessage, error) {
	var out []byte
	err := tx.QueryRow(ctx, `SELECT coalesce(json_agg(q), '[]'::json) FROM (`+sql+`) q`, args...).Scan(&out)
	return out, err
}

// queryJSONOne returns one row as a JSON object, or a not_found error.
func queryJSONOne(ctx context.Context, tx pgx.Tx, sql string, args ...any) (json.RawMessage, error) {
	var out []byte
	err := tx.QueryRow(ctx, `SELECT row_to_json(q) FROM (`+sql+`) q`, args...).Scan(&out)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	return out, err
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := decode(w, r, v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return false
	}
	return true
}

func validDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func validPeriod(s string) bool {
	_, err := time.Parse("2006-01", s)
	return err == nil && len(s) == 7
}

func queryInt(r *http.Request, key string, def, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || v < 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// money parses a non-negative decimal with at most 4 decimals.
func money(s string) (decimal.Decimal, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(s)
	if err != nil || d.IsNegative() || d.Exponent() < -4 {
		return decimal.Zero, false
	}
	return d, true
}

func optUUID(p *string) bool { return p == nil || *p == "" || isUUID(*p) }

func currencyDecimals(ctx context.Context, tx pgx.Tx, code string) (int32, error) {
	var d int16
	err := tx.QueryRow(ctx, `SELECT decimals FROM currencies WHERE code = $1`, code).Scan(&d)
	if err == pgx.ErrNoRows {
		return 0, badRequest("bad_request")
	}
	return int32(d), err
}

func today() string { return time.Now().Format(time.DateOnly) }
