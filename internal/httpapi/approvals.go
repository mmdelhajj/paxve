package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Approvals (optional, per company): voids, credit notes and payments above an amount can require a
// second person. When a rule applies and the user cannot approve the action themselves, the request is
// stored instead of executed (202 approval_required). Someone holding "<module>.approve" then approves
// it, which replays the exact same API call under the approver's name, or rejects it.

type approvalRules struct {
	Void        bool    `json:"void"`
	CreditNote  bool    `json:"credit_note"`
	Refunds     bool    `json:"refunds"`      // money paid back to customers: refunds and point-of-sale returns
	PaymentOver *string `json:"payment_over"` // amount in the company base currency; null = no limit
}

func loadApprovalRules(ctx context.Context, tx pgx.Tx, cid string) (approvalRules, string, error) {
	var raw []byte
	var base string
	var rules approvalRules
	err := tx.QueryRow(ctx, `SELECT approval_rules, base_currency::text FROM companies WHERE id = $1`, cid).Scan(&raw, &base)
	if errors.Is(err, pgx.ErrNoRows) {
		return rules, "", notFound()
	}
	if err != nil {
		return rules, "", err
	}
	_ = json.Unmarshal(raw, &rules)
	return rules, base, nil
}

func (s *Server) getApprovalRules(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rules, base, err := loadApprovalRules(ctx, tx, cid)
		return map[string]any{"rules": rules, "base_currency": base}, err
	})
}

func (s *Server) putApprovalRules(w http.ResponseWriter, r *http.Request) {
	var in approvalRules
	if err := decode(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.PaymentOver != nil {
		if *in.PaymentOver == "" {
			in.PaymentOver = nil
		} else if _, ok := money(*in.PaymentOver); !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	cid := r.PathValue("cid")
	b, _ := json.Marshal(in)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE companies SET approval_rules = $2 WHERE id = $1`, cid, b); err != nil {
			return nil, err
		}
		return map[string]any{"rules": in}, nil
	})
}

// approvable wraps an action. kinds: "void", "credit_note", "refund", "payment" or several separated by commas.
func (s *Server) approvable(kinds string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, replay := r.Context().Value(approvedExecKey{}).(*approvedExec); replay {
			next(w, r)
			return
		}
		perm := permForPattern(r.Pattern)
		if perm == "" || can(r, approvePermFor(perm)) {
			next(w, r) // approvers act directly
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var fields struct {
			Amount     string `json:"amount"`
			PaidAmount string `json:"paid_amount"`
			Currency   string `json:"currency"`
			MethodID   string `json:"method_id"`
			Reason     string `json:"reason"`
		}
		_ = json.Unmarshal(body, &fields)
		cid := r.PathValue("cid")

		var need string
		var amount *decimal.Decimal
		var currency *string
		err = db.InTenant(r.Context(), s.Pool, scope(r), func(tx pgx.Tx) error {
			rules, base, err := loadApprovalRules(r.Context(), tx, cid)
			if err != nil {
				return err
			}
			for _, k := range strings.Split(kinds, ",") {
				switch k {
				case "void":
					if rules.Void {
						need = "void"
					}
				case "credit_note":
					if rules.CreditNote {
						need = "credit_note"
					}
				case "refund":
					if rules.Refunds {
						need = "refund"
					}
				case "payment":
					if rules.PaymentOver == nil {
						continue
					}
					limit, _ := decimal.NewFromString(*rules.PaymentOver)
					amt, ok := money(fields.Amount)
					cur := strings.ToUpper(fields.Currency)
					if !ok {
						// only the amount taken out of the cash box is given: it is in that box's currency
						amt, ok = money(fields.PaidAmount)
						if ok && isUUID(fields.MethodID) {
							var mc string
							if err := tx.QueryRow(r.Context(), `SELECT currency::text FROM payment_methods WHERE id = $1 AND company_id = $2`,
								fields.MethodID, cid).Scan(&mc); err == nil && mc != "" {
								cur = mc
							}
						}
					}
					if !ok || !currencyRe.MatchString(cur) {
						continue // the action itself will refuse a bad amount
					}
					amount, currency = &amt, &cur
					inBase := amt
					if cur != base {
						rate, err := resolveRate(r.Context(), tx, cid, base, cur, today(), "")
						if err != nil {
							need = "payment" // no rate to compare with: ask, to be safe
							continue
						}
						rd, _ := decimal.NewFromString(rate)
						if rd.IsPositive() {
							inBase = amt.Div(rd)
						}
					}
					if inBase.GreaterThan(limit) {
						need = "payment"
					}
				}
				if need != "" {
					break
				}
			}
			return nil
		})
		if err != nil {
			fail(w, r, err)
			return
		}
		if need == "" {
			next(w, r)
			return
		}
		var js []byte
		if len(bytes.TrimSpace(body)) > 0 {
			if !json.Valid(body) {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
			js = body
		}
		s.run(w, r, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx) (any, error) {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO approval_requests (company_id, kind, permission, method, pattern, path, body,
				summary, amount, currency) VALUES ($1, $2, $3, $4, $5, $6, $7, left($8, 300), $9, $10) RETURNING id::text`,
				cid, need, perm, r.Method, r.Pattern, r.URL.Path, js, fields.Reason, amount, currency).Scan(&id); err != nil {
				return nil, err
			}
			return map[string]any{"approval_required": true, "approval_id": id, "kind": need}, nil
		})
	}
}

const approvalCols = `a.id, a.kind, a.permission, a.method, a.path, a.body, a.summary, a.amount::text AS amount, a.currency,
	a.status, a.requested_at, a.requested_by, ru.full_name AS requested_by_name, a.decided_at, du.full_name AS decided_by_name,
	a.note, a.result, a.error`

const approvalFrom = ` FROM approval_requests a LEFT JOIN users ru ON ru.id = a.requested_by LEFT JOIN users du ON du.id = a.decided_by`

// approverPerms: the ".approve" permissions the user holds (as a SQL array argument).
func approverPerms(p *Principal) []string {
	var out []string
	for perm := range p.perms {
		if strings.HasSuffix(perm, ".approve") {
			out = append(out, perm)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	status := r.URL.Query().Get("status")
	if status != "" && status != "pending" && status != "approved" && status != "rejected" && status != "cancelled" && status != "failed" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		// approvers see the requests they can decide; everyone sees their own
		return queryJSON(ctx, tx, `SELECT `+approvalCols+`,
			(split_part(a.permission, '.', 1) || '.approve') = ANY ($3::text[]) AS can_decide`+approvalFrom+`
			WHERE a.company_id = $1 AND ($2 = '' OR a.status = $2)
			  AND (a.requested_by = app_user() OR (split_part(a.permission, '.', 1) || '.approve') = ANY ($3::text[]))
			ORDER BY a.requested_at DESC LIMIT 200`, cid, status, approverPerms(p))
	})
}

func (s *Server) getApproval(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	p := principal(r)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT `+approvalCols+`,
			(split_part(a.permission, '.', 1) || '.approve') = ANY ($3::text[]) AS can_decide`+approvalFrom+`
			WHERE a.id = $1 AND a.company_id = $2
			  AND (a.requested_by = app_user() OR (split_part(a.permission, '.', 1) || '.approve') = ANY ($3::text[]))`,
			id, cid, approverPerms(p))
	})
}

type pendingApproval struct {
	permission, method, pattern, path, requestedBy string
	body                                           []byte
}

// lockPending loads a pending request of this company (row locked).
func lockPending(ctx context.Context, tx pgx.Tx, cid, id string) (*pendingApproval, error) {
	a := &pendingApproval{}
	var status string
	err := tx.QueryRow(ctx, `SELECT permission, method, pattern, path, coalesce(body::text, ''), requested_by::text, status
		FROM approval_requests WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
		Scan(&a.permission, &a.method, &a.pattern, &a.path, &a.body, &a.requestedBy, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" {
		return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	return a, nil
}

func (s *Server) approveRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := decode(w, r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	var a *pendingApproval
	err := db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		var err error
		if a, err = lockPending(ctx, tx, cid, id); err != nil {
			return err
		}
		if !p.perms[approvePermFor(a.permission)] {
			return &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		if strings.EqualFold(a.requestedBy, p.ID) {
			return &apiError{status: http.StatusForbidden, code: "cannot_approve_own"}
		}
		// claim it, so two approvers cannot run it twice
		_, err = tx.Exec(ctx, `UPDATE approval_requests SET status = 'approved', decided_by = app_user(), decided_at = now(),
			note = left($2, 500) WHERE id = $1`, id, in.Note)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}

	// replay the stored call for the requester (their permissions and companies are checked again, as of
	// now), with the approver recorded beside them
	rq, err := s.loadPrincipal(ctx, db.Scope{TenantID: p.TenantID, UserID: a.requestedBy, IP: clientIP(r)})
	if err != nil || !rq.Active {
		s.finishApproval(ctx, r, id, "failed", nil, "requester no longer active")
		writeJSON(w, http.StatusOK, map[string]any{"status": "failed", "http_status": http.StatusForbidden, "result": nil})
		return
	}
	req, err := http.NewRequestWithContext(context.WithValue(context.WithoutCancel(ctx), approvedExecKey{},
		&approvedExec{p: rq, approverID: p.ID, approvalID: id, pattern: a.pattern}), a.method, a.path, bytes.NewReader(a.body))
	if err != nil {
		serverErr(w, r, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-IP", clientIP(r))
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	status, result, errText := "approved", rec.Body.Bytes(), ""
	if rec.Code < 200 || rec.Code > 299 {
		status, errText = "failed", strings.TrimSpace(rec.Body.String())
	}
	if !json.Valid(result) {
		result = nil
	}
	if err := s.finishApproval(ctx, r, id, status, result, errText); err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "http_status": rec.Code, "result": json.RawMessage(nonEmptyJSON(result))})
}

func (s *Server) finishApproval(ctx context.Context, r *http.Request, id, status string, result []byte, errText string) error {
	return db.InTenant(ctx, s.Pool, scope(r), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE approval_requests SET status = $2, result = $3, error = nullif(left($4, 1000), '') WHERE id = $1`,
			id, status, result, errText)
		return err
	})
}

func nonEmptyJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}

func (s *Server) rejectRequest(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := decode(w, r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		a, err := lockPending(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if !p.perms[approvePermFor(a.permission)] {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		_, err = tx.Exec(ctx, `UPDATE approval_requests SET status = 'rejected', decided_by = app_user(), decided_at = now(),
			note = left($2, 500) WHERE id = $1`, id, in.Note)
		return map[string]string{"status": "rejected"}, err
	})
}

func (s *Server) cancelRequest(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		a, err := lockPending(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(a.requestedBy, principal(r).ID) {
			return nil, notFound()
		}
		_, err = tx.Exec(ctx, `UPDATE approval_requests SET status = 'cancelled', decided_at = now() WHERE id = $1`, id)
		return map[string]string{"status": "cancelled"}, err
	})
}
