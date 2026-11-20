package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Branches (phase 8): a company can have branches. Each branch has its own cost center, so the income and expense
// lines of its documents carry it and the branch P&L comes straight from the ledger. Documents (invoices, bills,
// payments, orders, expenses, till orders) get the branch of the user who makes them (users.branch_ids, first =
// default) through a database trigger; a user limited to branches only sees those branches' documents (row-level
// security, policy branch_scope) plus the company-wide ones. An admin can move a document to another branch.

var branchCodeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)

const branchCols = `b.id, b.code, b.name, b.address, b.phone, b.cost_center_id, cc.code AS cost_center_code, b.is_active, b.created_at,
	(SELECT count(*) FROM users u WHERE b.id = ANY (u.branch_ids) AND u.is_active) AS users`

func (s *Server) listBranches(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+branchCols+` FROM branches b LEFT JOIN cost_centers cc ON cc.id = b.cost_center_id
			WHERE b.company_id = $1 ORDER BY b.is_active DESC, b.code`, r.PathValue("cid"))
	})
}

type branchIn struct {
	Code     *string `json:"code"`
	Name     *string `json:"name"`
	Address  *string `json:"address"`
	Phone    *string `json:"phone"`
	IsActive *bool   `json:"is_active"`
}

// POST /branches and PATCH /branches/{id}. A new branch gets its cost center (kind "branch", code BR-<code>).
func (s *Server) saveBranch(w http.ResponseWriter, r *http.Request) {
	var in branchIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	if (creating && (in.Code == nil || !branchCodeRe.MatchString(*in.Code) || strings.TrimSpace(deref(in.Name)) == "")) ||
		(in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 120)) || len(deref(in.Address)) > 300 || len(deref(in.Phone)) > 40 ||
		(!creating && (!isUUID(id) || in.Code != nil)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if creating {
			var cc string
			err := tx.QueryRow(ctx, `INSERT INTO cost_centers (company_id, code, name, kind) VALUES ($1, 'BR-' || $2, trim($3), 'branch') RETURNING id::text`,
				cid, *in.Code, *in.Name).Scan(&cc)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
			}
			if err != nil {
				return nil, err
			}
			err = tx.QueryRow(ctx, `INSERT INTO branches (company_id, code, name, address, phone, cost_center_id)
				VALUES ($1, $2, trim($3), coalesce($4, ''), coalesce($5, ''), $6) RETURNING id::text`,
				cid, *in.Code, *in.Name, in.Address, in.Phone, cc).Scan(&id)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
			}
			if err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE branches SET name = coalesce(trim($3), name), address = coalesce($4, address), phone = coalesce($5, phone),
				is_active = coalesce($6, is_active), updated_at = now() WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.Address, in.Phone, in.IsActive)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
			// the cost center follows the branch name / state
			if _, err := tx.Exec(ctx, `UPDATE cost_centers c SET name = b.name, is_active = b.is_active, updated_at = now()
				FROM branches b WHERE b.id = $1 AND c.id = b.cost_center_id`, id); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+branchCols+` FROM branches b LEFT JOIN cost_centers cc ON cc.id = b.cost_center_id WHERE b.id = $1`, id)
	})
}

// GET /branches/users: the users who can open this company, with their branches in it.
func (s *Server) branchUsers(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT u.id, u.full_name, u.email, u.role, u.is_active,
			coalesce((SELECT array_agg(b.id ORDER BY array_position(u.branch_ids, b.id)) FROM branches b
			          WHERE b.company_id = $1 AND b.id = ANY (u.branch_ids)), '{}') AS branch_ids
			FROM users u WHERE u.is_active AND (u.company_ids IS NULL OR $1::uuid = ANY (u.company_ids)) ORDER BY u.full_name`, cid)
	})
}

// PUT /branches/users/{uid} {branch_ids}: the user's branches in this company (first = default). Branches of other
// companies the user has stay as they are. An empty list = every branch of this company.
func (s *Server) setUserBranches(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BranchIDs []string `json:"branch_ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, uid := r.PathValue("cid"), r.PathValue("uid")
	if !isUUID(uid) || len(in.BranchIDs) > 50 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, b := range in.BranchIDs {
		if !isUUID(b) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM branches WHERE company_id = $1 AND id = ANY ($2::uuid[])`, cid, in.BranchIDs).Scan(&n); err != nil {
			return nil, err
		}
		if n != len(dedupe(in.BranchIDs)) {
			return nil, badRequest("invalid_branch")
		}
		tag, err := tx.Exec(ctx, `UPDATE users u SET branch_ids = nullif(
				array(SELECT x FROM unnest($3::uuid[]) WITH ORDINALITY t(x, o) ORDER BY o)
				|| array(SELECT x FROM unnest(coalesce(u.branch_ids, '{}')) x WHERE NOT EXISTS (SELECT 1 FROM branches b WHERE b.id = x AND b.company_id = $2)),
				'{}'), updated_at = now()
			WHERE u.id = $1 AND (u.company_ids IS NULL OR $2::uuid = ANY (u.company_ids))`, uid, cid, dedupe(in.BranchIDs))
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]any{"ok": true}, nil
	})
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, x := range in {
		x = strings.ToLower(x)
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// documents that carry a branch, and the column of their journal entry ("" = none)
var branchDocs = map[string]struct {
	table, je string
	numbered  bool
}{
	"invoice": {"invoices", "journal_entry_id", true}, "bill": {"bills", "journal_entry_id", true}, "payment": {"payments", "journal_entry_id", true},
	"sales_order": {"sales_orders", "", true}, "purchase_order": {"purchase_orders", "", true}, "expense": {"expenses", "journal_entry_id", true},
	"supplier_payment": {"supplier_payments", "journal_entry_id", true}, "pos_register": {"pos_registers", "", false}, "warehouse": {"warehouses", "", false},
}

// PUT /branches/assign {doc, id, branch_id}: move a document (or a till / warehouse) to a branch ("" = company-wide).
// The income and expense lines of its journal entry take the branch's cost center (only the tag changes).
func (s *Server) assignBranch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Doc      string `json:"doc"`
		ID       string `json:"id"`
		Number   int64  `json:"number"` // instead of id, for numbered documents
		BranchID string `json:"branch_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	d, ok := branchDocs[in.Doc]
	if !ok || (in.ID != "" && !isUUID(in.ID)) || (in.ID == "" && (in.Number <= 0 || !d.numbered)) || (in.BranchID != "" && !isUUID(in.BranchID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var cc *string
		if in.BranchID != "" {
			if err := tx.QueryRow(ctx, `SELECT cost_center_id::text FROM branches WHERE id = $1 AND company_id = $2 AND is_active`, in.BranchID, cid).Scan(&cc); err != nil {
				if err == pgx.ErrNoRows {
					return nil, badRequest("invalid_branch")
				}
				return nil, err
			}
		}
		jeSel := "NULL::uuid"
		if d.je != "" {
			jeSel = d.je
		}
		if in.ID == "" {
			if err := tx.QueryRow(ctx, `SELECT id::text FROM `+d.table+` WHERE company_id = $1 AND number = $2`, cid, in.Number).Scan(&in.ID); err != nil {
				if err == pgx.ErrNoRows {
					return nil, notFound()
				}
				return nil, err
			}
		}
		var je *string
		err := tx.QueryRow(ctx, `UPDATE `+d.table+` SET branch_id = nullif($3, '')::uuid WHERE id = $1 AND company_id = $2 RETURNING `+jeSel+`::text`,
			in.ID, cid, in.BranchID).Scan(&je)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		retagged := int64(0)
		if je != nil {
			tag, err := tx.Exec(ctx, `UPDATE journal_lines l SET cost_center_id = $2::uuid FROM accounts a
				WHERE l.entry_id = $1 AND a.id = l.account_id AND a.type IN ('income', 'expense')
				  AND l.cost_center_id IS DISTINCT FROM $2::uuid
				  AND (l.cost_center_id IS NULL OR l.cost_center_id IN (SELECT cost_center_id FROM branches WHERE company_id = $3))`, *je, cc, cid)
			if err != nil {
				return nil, err
			}
			retagged = tag.RowsAffected()
		}
		return map[string]any{"ok": true, "lines_retagged": retagged}, nil
	})
}

// GET /branches/report?from=&to=: per branch (and "no branch"): income, expense and result from the ledger (lines
// tagged with the branch's cost center, in the base currency), plus the documents of the period.
func (s *Server) branchReport(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, `
			WITH br AS (
			  SELECT b.id, b.code, b.name, b.cost_center_id FROM branches b WHERE b.company_id = $1
			  UNION ALL SELECT NULL, '', '', NULL
			), led AS (
			  SELECT l.cost_center_id AS cc,
			         sum(CASE WHEN a.type = 'income' THEN l.base_credit - l.base_debit ELSE 0 END) AS income,
			         sum(CASE WHEN a.type = 'expense' THEN l.base_debit - l.base_credit ELSE 0 END) AS expense
			  FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id JOIN accounts a ON a.id = l.account_id
			  WHERE e.company_id = $1 AND e.status = 'posted' AND e.entry_date BETWEEN $2::date AND $3::date AND a.type IN ('income', 'expense')
			  GROUP BY 1
			)
			SELECT br.id, br.code, br.name,
			  trim_scale(round(coalesce(CASE WHEN br.id IS NULL THEN (SELECT sum(income) FROM led WHERE cc IS NULL OR cc NOT IN (SELECT cost_center_id FROM branches WHERE company_id = $1 AND cost_center_id IS NOT NULL))
			                                 ELSE (SELECT income FROM led WHERE cc = br.cost_center_id) END, 0), 2))::text AS income,
			  trim_scale(round(coalesce(CASE WHEN br.id IS NULL THEN (SELECT sum(expense) FROM led WHERE cc IS NULL OR cc NOT IN (SELECT cost_center_id FROM branches WHERE company_id = $1 AND cost_center_id IS NOT NULL))
			                                 ELSE (SELECT expense FROM led WHERE cc = br.cost_center_id) END, 0), 2))::text AS expense,
			  (SELECT count(*) FROM invoices i WHERE i.company_id = $1 AND i.branch_id IS NOT DISTINCT FROM br.id AND i.status <> 'void'
			     AND i.issue_date BETWEEN $2::date AND $3::date) AS invoices,
			  (SELECT count(*) FROM payments p WHERE p.company_id = $1 AND p.branch_id IS NOT DISTINCT FROM br.id AND p.status <> 'void'
			     AND p.payment_date BETWEEN $2::date AND $3::date) AS payments,
			  (SELECT count(*) FROM bills b WHERE b.company_id = $1 AND b.branch_id IS NOT DISTINCT FROM br.id AND b.status <> 'void'
			     AND b.bill_date BETWEEN $2::date AND $3::date) AS bills,
			  (SELECT count(*) FROM pos_orders o WHERE o.company_id = $1 AND o.branch_id IS NOT DISTINCT FROM br.id
			     AND o.created_at::date BETWEEN $2::date AND $3::date) AS pos_orders
			FROM br ORDER BY br.id IS NULL, br.code`, cid, from, to)
		if err != nil {
			return nil, err
		}
		var base string
		if err := tx.QueryRow(ctx, `SELECT base_currency::text FROM companies WHERE id = $1`, cid).Scan(&base); err != nil {
			return nil, err
		}
		return map[string]any{"from": from, "to": to, "currency": base, "rows": json.RawMessage(rows)}, nil
	})
}
