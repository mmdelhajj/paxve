package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Presentation currency: the financial statements are kept in the company base currency; with
// ?currency=XXX they are shown in XXX, every amount converted at the official rate of the report date
// (closing-rate translation). The base figures are unchanged; only the display is converted.

var presentKeys = map[string]bool{
	"debit": true, "credit": true, "balance": true, "amount": true, "opening": true, "closing": true,
	"total_debit": true, "total_credit": true, "total_income": true, "total_expense": true, "net": true,
	"total_assets": true, "total_liabilities": true, "total_equity": true, "current_result": true,
	"total_liabilities_equity": true,
}

type presentation struct {
	cur  string
	rate decimal.Decimal
	dec  int32
}

// presentationFor reads ?currency and the rate at date. nil = show in base currency.
func presentationFor(ctx context.Context, tx pgx.Tx, r *http.Request, cid, base, date string) (*presentation, error) {
	cur := r.URL.Query().Get("currency")
	if cur == "" || cur == base {
		return nil, nil
	}
	if !currencyRe.MatchString(cur) {
		return nil, badRequest("bad_currency")
	}
	rs, err := resolveRate(ctx, tx, cid, base, cur, date, "")
	if err != nil {
		return nil, err
	}
	rate, _ := decimal.NewFromString(rs)
	dec, err := currencyDecimals(ctx, tx, cur)
	if err != nil {
		return nil, err
	}
	return &presentation{cur: cur, rate: rate, dec: dec}, nil
}

// apply converts the money fields of a statement result (any JSON-able value) and labels it.
func (p *presentation) apply(v map[string]any) (map[string]any, error) {
	if p == nil {
		return v, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	p.walk(generic)
	generic["presentation_currency"] = p.cur
	generic["presentation_rate"] = p.rate.String()
	return generic, nil
}

func (p *presentation) walk(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if presentKeys[k] {
				if s, ok := val.(string); ok {
					if d, err := decimal.NewFromString(s); err == nil {
						x[k] = d.Mul(p.rate).StringFixed(p.dec)
						continue
					}
				}
			}
			p.walk(val)
		}
	case []any:
		for _, e := range x {
			p.walk(e)
		}
	}
}
