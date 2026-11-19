package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Automatic daily exchange rates. A company opts in (companies.auto_rates); once a day the published
// rates are fetched and stored as the official rate of the day with source 'auto' for every currency the
// company works with. A rate typed by a user (source 'manual') is never overwritten, and currencies in
// companies.manual_rate_currencies (default LBP: the market rate is not the published one) are skipped.

// RateFeed returns units of each currency for 1 USD.
type RateFeed func(ctx context.Context) (map[string]decimal.Decimal, error)

// openERAPI is the default feed: https://open.er-api.com (free, no key, updated daily, base USD).
// Nothing about the tenant is sent.
func openERAPI(ctx context.Context) (map[string]decimal.Decimal, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://open.er-api.com/v6/latest/USD", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Paxve-rates/1.0")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rate feed: status %d", res.StatusCode)
	}
	var body struct {
		Result string                 `json:"result"`
		Rates  map[string]json.Number `json:"rates"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Result != "success" || len(body.Rates) == 0 {
		return nil, fmt.Errorf("rate feed: %s", body.Result)
	}
	out := make(map[string]decimal.Decimal, len(body.Rates))
	for k, v := range body.Rates {
		if d, err := decimal.NewFromString(v.String()); err == nil && d.IsPositive() && currencyRe.MatchString(k) {
			out[k] = d
		}
	}
	return out, nil
}

func (s *Server) rateFeed() RateFeed {
	if s.RateFeed != nil {
		return s.RateFeed
	}
	return openERAPI
}

// applyAutoRates stores today's published rates for company cid. Returns the currencies updated.
func applyAutoRates(ctx context.Context, tx pgx.Tx, cid, date string, feed map[string]decimal.Decimal) ([]string, error) {
	var base string
	var manual []string
	if err := tx.QueryRow(ctx, `SELECT base_currency::text, manual_rate_currencies FROM companies WHERE id = $1`, cid).
		Scan(&base, &manual); err != nil {
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		return nil, err
	}
	perUSDBase, ok := feed[base]
	if base == "USD" {
		perUSDBase, ok = decimal.NewFromInt(1), true
	}
	if !ok {
		return nil, badRequest("rate_feed_missing_base")
	}
	// the currencies this company works with: those it already has rates for, its cash boxes, plans, items
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT c FROM (
		  SELECT currency::text AS c FROM exchange_rates WHERE company_id = $1
		  UNION SELECT currency::text FROM payment_methods WHERE company_id = $1
		  UNION SELECT local_currency::text FROM companies WHERE id = $1 AND local_currency IS NOT NULL
		) x WHERE c IS NOT NULL AND c <> $2 ORDER BY c`, cid, base)
	if err != nil {
		return nil, err
	}
	curs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	done := []string{}
	for _, cur := range curs {
		if slices.Contains(manual, cur) {
			continue
		}
		perUSD, ok := feed[cur]
		if cur == "USD" {
			perUSD, ok = decimal.NewFromInt(1), true
		}
		if !ok {
			continue
		}
		rate := perUSD.DivRound(perUSDBase, 10)
		tag, err := tx.Exec(ctx, `
			INSERT INTO exchange_rates (tenant_id, company_id, currency, rate_date, kind, rate, source)
			SELECT tenant_id, id, $2, $3::date, 'official', $4::numeric, 'auto' FROM companies WHERE id = $1
			ON CONFLICT (tenant_id, company_id, currency, kind, rate_date)
			DO UPDATE SET rate = EXCLUDED.rate WHERE exchange_rates.source = 'auto'`, cid, cur, date, rate.String())
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() > 0 {
			done = append(done, cur)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE companies SET auto_rates_at = now() WHERE id = $1`, cid); err != nil {
		return nil, err
	}
	return done, nil
}

// GET /companies/{cid}/rates/settings
func (s *Server) rateSettings(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT auto_rates, manual_rate_currencies, auto_rates_at, base_currency,
			'open.er-api.com' AS source FROM companies WHERE id = $1`, cid)
	})
}

// PUT /companies/{cid}/rates/settings {auto_rates, manual_rate_currencies}
func (s *Server) saveRateSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AutoRates *bool    `json:"auto_rates"`
		Manual    []string `json:"manual_rate_currencies"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	for i, c := range in.Manual {
		in.Manual[i] = strings.ToUpper(strings.TrimSpace(c))
		if !currencyRe.MatchString(in.Manual[i]) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE companies SET auto_rates = coalesce($2, auto_rates),
			manual_rate_currencies = coalesce($3, manual_rate_currencies) WHERE id = $1`, cid, in.AutoRates, in.Manual); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT auto_rates, manual_rate_currencies, auto_rates_at, base_currency,
			'open.er-api.com' AS source FROM companies WHERE id = $1`, cid)
	})
}

// POST /companies/{cid}/rates/fetch: fetch today's published rates now.
func (s *Server) fetchRatesNow(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	feed, err := s.rateFeed()(r.Context())
	if err != nil {
		slog.Warn("rate feed", "err", err)
		writeErr(w, http.StatusBadGateway, "rate_feed_unavailable")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		done, err := applyAutoRates(ctx, tx, cid, today(), feed)
		if err != nil {
			return nil, err
		}
		return map[string]any{"date": today(), "updated": done}, nil
	})
}

// StartRatesWorker fetches the published rates every 6 hours (once per day per company) for companies
// that switched automatic rates on.
func (s *Server) StartRatesWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	go func() {
		t := time.NewTimer(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := s.RunAutoRates(ctx); err != nil {
					slog.Error("auto rates", "err", err)
				} else if n > 0 {
					slog.Info("auto rates updated", "companies", n)
				}
				t.Reset(6 * time.Hour)
			}
		}
	}()
}

// RunAutoRates updates every opted-in company whose rates were not fetched today.
func (s *Server) RunAutoRates(ctx context.Context) (int, error) {
	rows, err := s.OwnerPool.Query(ctx, `SELECT id::text, tenant_id::text FROM companies
		WHERE auto_rates AND is_active AND (auto_rates_at IS NULL OR auto_rates_at::date < current_date)`)
	if err != nil {
		return 0, err
	}
	type co struct{ id, tenant string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (co, error) {
		var x co
		err := r.Scan(&x.id, &x.tenant)
		return x, err
	})
	if err != nil || len(list) == 0 {
		return 0, err
	}
	feed, err := s.rateFeed()(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range list {
		err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, c.tenant); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
				return err
			}
			_, err := applyAutoRates(ctx, tx, c.id, today(), feed)
			return err
		})
		if err != nil {
			slog.Error("auto rates failed", "company", c.id, "err", err)
			continue
		}
		n++
	}
	return n, nil
}
