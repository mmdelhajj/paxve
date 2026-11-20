package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Attachments on any document: the scanned supplier bill, the signed contract, a photo of the meter, the bank slip…
// Stored in the database (10 MB per file), scoped to the company like the document itself.

// attachTables maps an entity type to its table; the document must exist in the company before a file is attached.
var attachTables = map[string]string{
	"invoice": "invoices", "bill": "bills", "expense": "expenses", "journal": "journal_entries", "customer": "customers",
	"supplier": "suppliers", "asset": "assets", "purchase_order": "purchase_orders", "sales_order": "sales_orders",
	"payment": "payments", "supplier_payment": "supplier_payments", "ticket": "tickets", "item": "items", "cheque": "cheques",
	"expense_claim": "expense_claims", "goods_receipt": "goods_receipts", "mfg_order": "mfg_orders", "employee": "employees",
	"subscription": "subscriptions", "repair": "repair_orders", "instalment": "instalment_plans", "lead": "crm_leads",
}

// attachModule: the permission module guarding the files of a document type (view to see, create to add / remove).
var attachModule = map[string]string{
	"invoice": "billing", "bill": "purchasing", "expense": "expenses", "journal": "accounting", "customer": "customers",
	"supplier": "suppliers", "asset": "assets", "purchase_order": "purchasing", "sales_order": "sales", "payment": "collections",
	"supplier_payment": "purchasing", "ticket": "service", "item": "stock", "cheque": "accounting",
	"expense_claim": "payroll", "goods_receipt": "purchasing", "mfg_order": "stock", "employee": "payroll",
	"subscription": "customers", "repair": "service", "instalment": "billing", "lead": "crm",
}

func attachAllowed(r *http.Request, typ string, write bool) bool {
	m, ok := attachModule[typ]
	if !ok {
		return false
	}
	if write {
		return can(r, m+".create") || can(r, m+".edit")
	}
	return can(r, m+".view")
}

// attachmentType returns the document type of an attachment (for the permission check).
func attachmentType(ctx context.Context, tx pgx.Tx, cid, id string) (string, error) {
	var t string
	err := tx.QueryRow(ctx, `SELECT entity_type FROM attachments WHERE id = $1 AND company_id = $2 AND deleted_at IS NULL`, id, cid).Scan(&t)
	if err == pgx.ErrNoRows {
		return "", notFound()
	}
	return t, err
}

func attachmentTarget(ctx context.Context, tx pgx.Tx, cid, typ, id string) error {
	table, ok := attachTables[typ]
	if !ok || !isUUID(id) {
		return badRequest("bad_request")
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFound()
	}
	return nil
}

// GET /companies/{cid}/attachments?type=&id=
func (s *Server) listAttachments(w http.ResponseWriter, r *http.Request) {
	cid, typ, id := r.PathValue("cid"), r.URL.Query().Get("type"), r.URL.Query().Get("id")
	if _, ok := attachModule[typ]; ok && !attachAllowed(r, typ, false) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := attachmentTarget(ctx, tx, cid, typ, id); err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT a.id, a.filename, a.mime, a.size, a.note, a.created_at, u.full_name AS created_by_name
			FROM attachments a LEFT JOIN users u ON u.id = a.created_by
			WHERE a.company_id = $1 AND a.entity_type = $2 AND a.entity_id = $3 AND a.deleted_at IS NULL ORDER BY a.created_at`, cid, typ, id)
	})
}

// POST /companies/{cid}/attachments {type, id, filename, mime, data (base64 or data: URL), note}
func (s *Server) addAttachment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 15<<20)
	var in struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Filename string `json:"filename"`
		Mime     string `json:"mime"`
		Data     string `json:"data"`
		Note     string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if i := strings.Index(in.Data, ";base64,"); strings.HasPrefix(in.Data, "data:") && i > 0 {
		if in.Mime == "" {
			in.Mime = in.Data[5:i]
		}
		in.Data = in.Data[i+8:]
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	name := strings.TrimSpace(in.Filename)
	if err != nil || len(data) == 0 || len(data) > 10<<20 || name == "" || len(name) > 200 || len(in.Note) > 1000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Mime == "" || len(in.Mime) > 120 {
		in.Mime = http.DetectContentType(data)
	}
	if _, ok := attachModule[in.Type]; ok && !attachAllowed(r, in.Type, true) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := attachmentTarget(ctx, tx, cid, in.Type, in.ID); err != nil {
			return nil, err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM attachments WHERE company_id = $1 AND entity_type = $2 AND entity_id = $3 AND deleted_at IS NULL`,
			cid, in.Type, in.ID).Scan(&n); err != nil {
			return nil, err
		}
		if n >= 50 {
			return nil, badRequest("too_many_files")
		}
		if err := checkStorageQuota(ctx, tx, cid, len(data)); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO attachments (company_id, entity_type, entity_id, filename, mime, size, data, note)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`, cid, in.Type, in.ID, name, in.Mime, len(data), data, strings.TrimSpace(in.Note)).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, filename, mime, size, note, created_at FROM attachments WHERE id = $1`, id)
	})
}

// GET /companies/{cid}/attachments/{id}/file (?inline=1 shows images / PDF in the browser)
func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var name, mime string
	var data []byte
	s.runRaw(w, r, func(ctx context.Context, tx pgx.Tx) error {
		t, err := attachmentType(ctx, tx, cid, id)
		if err != nil {
			return err
		}
		if !attachAllowed(r, t, false) {
			return &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		err = tx.QueryRow(ctx, `SELECT filename, mime, data FROM attachments WHERE id = $1 AND company_id = $2 AND deleted_at IS NULL`, id, cid).
			Scan(&name, &mime, &data)
		if err == pgx.ErrNoRows {
			return notFound()
		}
		return err
	}, func() {
		disp := "attachment"
		safe := strings.HasPrefix(mime, "image/") && mime != "image/svg+xml" || mime == "application/pdf"
		if r.URL.Query().Get("inline") == "1" && safe {
			disp = "inline"
		}
		if !safe {
			mime = "application/octet-stream"
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename=%q`, disp, name))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; sandbox")
		_, _ = w.Write(data)
	})
}

// DELETE /companies/{cid}/attachments/{id}: hidden, kept for the audit trail.
func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		t, err := attachmentType(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if !attachAllowed(r, t, true) {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		tag, err := tx.Exec(ctx, `UPDATE attachments SET deleted_at = now() WHERE id = $1 AND company_id = $2 AND deleted_at IS NULL`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]string{"deleted": id}, nil
	})
}
