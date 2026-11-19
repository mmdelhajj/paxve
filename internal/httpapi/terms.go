package httpapi

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Payment terms: how many days after the invoice / bill date it falls due. The company has a default; a customer or
// supplier can have its own ("" / null = use the default). An explicit due date on a document always wins.

// GET /companies/{cid}/payment-terms
func (s *Server) getPaymentTerms(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT default_due_days FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}

// PUT /companies/{cid}/payment-terms {default_due_days} or {customer_id | supplier_id, due_days (null = company default)}
func (s *Server) putPaymentTerms(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DefaultDueDays *int   `json:"default_due_days"`
		CustomerID     string `json:"customer_id"`
		SupplierID     string `json:"supplier_id"`
		DueDays        *int   `json:"due_days"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	okDays := func(p *int) bool { return p == nil || (*p >= 0 && *p <= 365) }
	if !okDays(in.DefaultDueDays) || !okDays(in.DueDays) || (in.CustomerID != "" && !isUUID(in.CustomerID)) ||
		(in.SupplierID != "" && !isUUID(in.SupplierID)) || (in.CustomerID != "" && in.SupplierID != "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.DefaultDueDays != nil {
			if _, err := tx.Exec(ctx, `UPDATE companies SET default_due_days = $2 WHERE id = $1`, cid, *in.DefaultDueDays); err != nil {
				return nil, err
			}
		}
		table, id := "", ""
		switch {
		case in.CustomerID != "":
			table, id = "customers", in.CustomerID
		case in.SupplierID != "":
			table, id = "suppliers", in.SupplierID
		}
		if table != "" {
			tag, err := tx.Exec(ctx, `UPDATE `+table+` SET due_days = $3 WHERE id = $1 AND company_id = $2`, id, cid, in.DueDays)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT default_due_days FROM companies WHERE id = $1`, cid)
	})
}
