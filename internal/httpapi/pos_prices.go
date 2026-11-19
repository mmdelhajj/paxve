package httpapi

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// GET /companies/{cid}/pos/price-list?customer_id=&currency= : the named customer's active price list in
// the till's currency (every quantity break), so the till shows the same price the server will charge.
func (s *Server) posCustomerPrices(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	cust, cur := r.URL.Query().Get("customer_id"), r.URL.Query().Get("currency")
	if !isUUID(cust) || !currencyRe.MatchString(cur) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var name *string
		if err := tx.QueryRow(ctx, `SELECT p.name FROM customers c JOIN price_lists p ON p.id = c.price_list_id AND p.is_active AND p.currency = $3
			WHERE c.id = $2 AND c.company_id = $1`, cid, cust, cur).Scan(&name); err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
		if name == nil {
			return map[string]any{"name": nil, "items": []any{}}, nil
		}
		items, err := queryJSON(ctx, tx, `SELECT x.item_id, x.min_qty::text AS min_qty, x.price::text AS price
			FROM customers c JOIN price_lists p ON p.id = c.price_list_id JOIN price_list_items x ON x.list_id = p.id
			WHERE c.id = $1 ORDER BY x.item_id, x.min_qty`, cust)
		if err != nil {
			return nil, err
		}
		return map[string]any{"name": *name, "items": items}, nil
	})
}
