package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// E-sign (core, permission module "esign"): a quotation, a subscription agreement, a contract text or an uploaded PDF
// is sent for signature through a public link per signer (WhatsApp / SMS / e-mail). Signers sign in order (or all at
// once) by drawing or typing their signature; each signature keeps the IP, the time, the browser and the SHA-256 of
// the document, and every step is written to an append-only audit trail. When everyone has signed, a certificate PDF
// (document, hash, signatures, audit trail) is stored as an attachment; a quotation signed this way can confirm itself
// into a sales order. Reminders go out every N days until the request is signed, declined, cancelled or expired.

const signReqCols = `q.id, q.number, q.title, q.doc_type, q.sales_order_id, so.number AS quote_number, so.order_number, q.subscription_id, q.attachment_id,
	q.doc_hash, q.message, q.ordered, q.channel, q.status, q.expires_on, q.auto_confirm, q.remind_days, q.sent_at, q.completed_at, q.certificate_id,
	q.created_at, u.full_name AS created_by_name,
	(SELECT count(*) FROM sign_signers g WHERE g.request_id = q.id) AS signers,
	(SELECT count(*) FROM sign_signers g WHERE g.request_id = q.id AND g.status = 'signed') AS signed,
	(SELECT string_agg(g.name, ', ' ORDER BY g.seq) FROM sign_signers g WHERE g.request_id = q.id) AS signer_names`

const signReqFrom = ` FROM sign_requests q LEFT JOIN sales_orders so ON so.id = q.sales_order_id LEFT JOIN users u ON u.id = q.created_by`

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func signEvent(ctx context.Context, tx pgx.Tx, cid, req string, signer *string, event, ip, ua, detail string) error {
	if len(ua) > 400 {
		ua = ua[:400]
	}
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	_, err := tx.Exec(ctx, `INSERT INTO sign_events (company_id, request_id, signer_id, event, ip, user_agent, detail) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		cid, req, signer, event, ip, ua, detail)
	return err
}

// quoteSnapshot: the text of a quotation as it is signed (its hash must not change before signing).
func quoteSnapshot(ctx context.Context, tx pgx.Tx, cid, id string) (string, string, error) {
	var number int64
	var cust, cur, date, sub, tax, total, terms, status, company string
	var valid *string
	err := tx.QueryRow(ctx, `SELECT o.number, c.name, o.currency, o.quote_date::text, o.valid_until::text, o.subtotal::text, o.tax_total::text, o.total::text,
		o.terms, o.status, co.name_en FROM sales_orders o JOIN customers c ON c.id = o.customer_id JOIN companies co ON co.id = o.company_id
		WHERE o.id = $1 AND o.company_id = $2`, id, cid).Scan(&number, &cust, &cur, &date, &valid, &sub, &tax, &total, &terms, &status, &company)
	if err == pgx.ErrNoRows {
		return "", "", badRequest("invalid_order")
	}
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nQuotation #%d\nCustomer: %s\nDate: %s", company, number, cust, date)
	if valid != nil {
		fmt.Fprintf(&b, "  Valid until: %s", *valid)
	}
	b.WriteString("\n\n")
	rows, err := tx.Query(ctx, `SELECT l.line_no, l.description, trim_scale(l.quantity)::text, l.unit_price::text, trim_scale(l.discount_pct)::text, l.amount::text, l.tax_amount::text
		FROM sales_order_lines l WHERE l.order_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return "", "", err
	}
	for rows.Next() {
		var n int
		var d, q, p, disc, amt, ta string
		if err := rows.Scan(&n, &d, &q, &p, &disc, &amt, &ta); err != nil {
			rows.Close()
			return "", "", err
		}
		fmt.Fprintf(&b, "%d. %s — %s × %s", n, d, q, p)
		if disc != "0" {
			fmt.Fprintf(&b, " −%s%%", disc)
		}
		fmt.Fprintf(&b, " = %s", amt)
		if ta != "0.0000" && ta != "0" {
			fmt.Fprintf(&b, " (+%s tax)", ta)
		}
		b.WriteString("\n")
	}
	rows.Close()
	fmt.Fprintf(&b, "\nSubtotal: %s %s\nTax: %s %s\nTotal: %s %s\n", sub, cur, tax, cur, total, cur)
	if strings.TrimSpace(terms) != "" {
		fmt.Fprintf(&b, "\nTerms:\n%s\n", strings.TrimSpace(terms))
	}
	return b.String(), status, nil
}

// subscriptionText: a subscription agreement (customer, plan, price, start, the company's terms).
func subscriptionText(ctx context.Context, tx pgx.Tx, cid, id string) (string, error) {
	var cust, code, plan, start, meter, company, terms string
	var fee *string
	err := tx.QueryRow(ctx, `SELECT c.name, c.code, p.name, s.start_date::text, s.meter_no, co.name_en, co.doc_terms,
		CASE WHEN p.billing_type = 'metered' THEN coalesce(s.fee_override, p.base_fee)::text || ' ' || p.currency || ' + ' || p.unit_price::text || ' / ' || p.unit_label
		     ELSE coalesce(s.fee_override, p.base_fee)::text || ' ' || p.currency END
		FROM subscriptions s JOIN customers c ON c.id = s.customer_id JOIN plans p ON p.id = s.plan_id JOIN companies co ON co.id = s.company_id
		WHERE s.id = $1 AND s.company_id = $2`, id, cid).Scan(&cust, &code, &plan, &start, &meter, &company, &terms, &fee)
	if err == pgx.ErrNoRows {
		return "", badRequest("invalid_subscription")
	}
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nSubscription agreement\n\nCustomer: %s (%s)\nPlan: %s\nPrice: %s\nStart date: %s\n", company, cust, code, plan, deref(fee), start)
	if meter != "" {
		fmt.Fprintf(&b, "Meter: %s\n", meter)
	}
	if strings.TrimSpace(terms) != "" {
		fmt.Fprintf(&b, "\nTerms:\n%s\n", strings.TrimSpace(terms))
	}
	b.WriteString("\nBy signing, the customer accepts the plan, the price and the terms above.\n")
	return b.String(), nil
}

// ======================= staff API =======================

func (s *Server) esignRequests(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	if st != "" && !contains([]string{"draft", "sent", "completed", "declined", "cancelled", "expired"}, st) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+signReqCols+signReqFrom+` WHERE q.company_id = $1 AND ($2 = '' OR q.status = $2) ORDER BY q.created_at DESC LIMIT 1000`,
			r.PathValue("cid"), st)
	})
}

func loadSignRequest(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	q, err := queryJSONOne(ctx, tx, `SELECT `+signReqCols+`, q.body`+signReqFrom+` WHERE q.id = $1 AND q.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	signers, err := queryJSON(ctx, tx, `SELECT g.id, g.seq, g.name, g.email, g.phone, g.customer_id, g.status, g.viewed_at, g.signed_at, g.declined_at,
		g.decline_reason, g.sign_method, g.typed_name, g.signature, g.ip, g.user_agent, g.notified_at, g.reminders, $2 || g.token AS link
		FROM sign_signers g WHERE g.request_id = $1 ORDER BY g.seq`, id, publicURL()+"/#/sign/")
	if err != nil {
		return nil, err
	}
	events, err := queryJSON(ctx, tx, `SELECT e.event, e.created_at, e.ip, e.user_agent, e.detail, g.name AS signer, u.full_name AS by_user
		FROM sign_events e LEFT JOIN sign_signers g ON g.id = e.signer_id LEFT JOIN users u ON u.id = e.created_by WHERE e.request_id = $1 ORDER BY e.created_at`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"request": q, "signers": signers, "events": events}, nil
}

func (s *Server) esignGetRequest(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadSignRequest(ctx, tx, cid, id)
	})
}

type signerIn struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	CustomerID string `json:"customer_id"`
}

// POST /esign/requests {title, doc_type, sales_order_id | subscription_id | body | file {filename, mime, data}, message, ordered,
// channel, expires_on, auto_confirm, remind_days, signers: [{name, email, phone, customer_id}], send}
func (s *Server) esignCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 15<<20)
	var in struct {
		Title          string `json:"title"`
		DocType        string `json:"doc_type"`
		SalesOrderID   string `json:"sales_order_id"`
		SubscriptionID string `json:"subscription_id"`
		Body           string `json:"body"`
		File           *struct {
			Filename string `json:"filename"`
			Data     string `json:"data"`
		} `json:"file"`
		Message     string     `json:"message"`
		Ordered     *bool      `json:"ordered"`
		Channel     string     `json:"channel"`
		ExpiresOn   string     `json:"expires_on"`
		AutoConfirm bool       `json:"auto_confirm"`
		RequireOTP  bool       `json:"require_otp"` // the signer types a code sent to their phone / e-mail first (audit P7-L7)
		RemindDays  *int       `json:"remind_days"`
		Signers     []signerIn `json:"signers"`
		Send        bool       `json:"send"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Channel == "" {
		in.Channel = "whatsapp"
	}
	in.Title = strings.TrimSpace(in.Title)
	if len(in.Title) > 200 || !contains([]string{"quote", "contract", "subscription", "upload"}, in.DocType) ||
		!contains([]string{"whatsapp", "sms", "email", "none"}, in.Channel) || (in.ExpiresOn != "" && !validDate(in.ExpiresOn)) ||
		(in.RemindDays != nil && (*in.RemindDays < 0 || *in.RemindDays > 30)) || len(in.Signers) == 0 || len(in.Signers) > 10 || len(in.Message) > 2000 ||
		len(in.Body) > 60000 || (in.SalesOrderID != "" && !isUUID(in.SalesOrderID)) || (in.SubscriptionID != "" && !isUUID(in.SubscriptionID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for i := range in.Signers {
		g := &in.Signers[i]
		g.Name, g.Email, g.Phone = strings.TrimSpace(g.Name), strings.ToLower(strings.TrimSpace(g.Email)), strings.TrimSpace(g.Phone)
		if g.Name == "" || len(g.Name) > 160 || len(g.Email) > 200 || len(g.Phone) > 40 || (g.CustomerID != "" && !isUUID(g.CustomerID)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	var file []byte
	var fileName, fileMime string
	switch in.DocType {
	case "quote":
		if in.SalesOrderID == "" {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	case "subscription":
		if in.SubscriptionID == "" {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	case "contract":
		if strings.TrimSpace(in.Body) == "" {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	case "upload":
		if in.File == nil {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		data := in.File.Data
		if i := strings.Index(data, ";base64,"); strings.HasPrefix(data, "data:") && i > 0 {
			data = data[i+8:]
		}
		b, err := base64.StdEncoding.DecodeString(data)
		fileName = strings.TrimSpace(in.File.Filename)
		if err != nil || len(b) == 0 || len(b) > 10<<20 || fileName == "" || len(fileName) > 200 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		fileMime = http.DetectContentType(b)
		if fileMime != "application/pdf" && !(strings.HasPrefix(fileMime, "image/") && fileMime != "image/svg+xml") {
			writeErr(w, http.StatusBadRequest, "bad_file")
			return
		}
		file = b
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		body, hash := in.Body, ""
		var so, sub *string
		switch in.DocType {
		case "quote":
			text, status, err := quoteSnapshot(ctx, tx, cid, in.SalesOrderID)
			if err != nil {
				return nil, err
			}
			if status != "draft" && status != "sent" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			body, so = text, &in.SalesOrderID
			if in.Title == "" {
				in.Title = strings.SplitN(text, "\n", 3)[1]
			}
		case "subscription":
			if strings.TrimSpace(body) == "" {
				text, err := subscriptionText(ctx, tx, cid, in.SubscriptionID)
				if err != nil {
					return nil, err
				}
				body = text
			} else {
				var ok bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subscriptions WHERE id = $1 AND company_id = $2)`, in.SubscriptionID, cid).Scan(&ok); err != nil {
					return nil, err
				}
				if !ok {
					return nil, badRequest("invalid_subscription")
				}
			}
			sub = &in.SubscriptionID
		}
		if in.Title == "" {
			if in.DocType == "upload" {
				in.Title = fileName
			} else {
				return nil, badRequest("bad_request")
			}
		}
		if in.DocType == "upload" {
			hash = sha256Hex(file)
		} else {
			hash = sha256Hex([]byte(body))
		}
		for _, g := range in.Signers {
			if g.CustomerID != "" {
				var ok bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE id = $1 AND company_id = $2)`, g.CustomerID, cid).Scan(&ok); err != nil {
					return nil, err
				}
				if !ok {
					return nil, badRequest("invalid_customer")
				}
			}
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'sign_request')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		ordered := in.Ordered == nil || *in.Ordered
		remind := 3
		if in.RemindDays != nil {
			remind = *in.RemindDays
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO sign_requests (company_id, number, title, doc_type, sales_order_id, subscription_id, body, doc_hash, message, ordered,
			channel, expires_on, auto_confirm, remind_days, require_otp) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, trim($9), $10, $11, nullif($12, '')::date, $13, $14, $15) RETURNING id::text`,
			cid, n, in.Title, in.DocType, so, sub, body, hash, in.Message, ordered, in.Channel, in.ExpiresOn, in.AutoConfirm && in.DocType == "quote", remind, in.RequireOTP).Scan(&id); err != nil {
			return nil, err
		}
		if len(file) > 0 {
			if err := checkStorageQuota(ctx, tx, cid, len(file)); err != nil {
				return nil, err
			}
			var att string
			if err := tx.QueryRow(ctx, `INSERT INTO attachments (company_id, entity_type, entity_id, filename, mime, size, data, note)
				VALUES ($1, 'sign_request', $2, $3, $4, $5, $6, 'Document to sign') RETURNING id::text`, cid, id, fileName, fileMime, len(file), file).Scan(&att); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE sign_requests SET attachment_id = $2 WHERE id = $1`, id, att); err != nil {
				return nil, err
			}
		}
		for i, g := range in.Signers {
			if _, err := tx.Exec(ctx, `INSERT INTO sign_signers (company_id, request_id, seq, name, email, phone, customer_id, token)
				VALUES ($1, $2, $3, $4, $5, $6, nullif($7, '')::uuid, $8)`, cid, id, i+1, g.Name, g.Email, g.Phone, g.CustomerID, randToken(24)); err != nil {
				return nil, err
			}
		}
		if err := signEvent(ctx, tx, cid, id, nil, "created", clientIP(r), r.UserAgent(), "sha256 "+hash); err != nil {
			return nil, err
		}
		if in.Send {
			if err := sendSignRequest(ctx, tx, cid, id, clientIP(r), r.UserAgent()); err != nil {
				return nil, err
			}
		}
		return loadSignRequest(ctx, tx, cid, id)
	})
}

// notifySigner sends the signing link (or a reminder) on the request's channel, falling back to the other contact.
func notifySigner(ctx context.Context, tx pgx.Tx, cid, req, signer, key string) error {
	var name, email, phone, token, channel, title, company string
	var cust *string
	if err := tx.QueryRow(ctx, `SELECT g.name, g.email, g.phone, g.token, g.customer_id::text, q.channel, q.title, coalesce(nullif(c.name_ar, ''), c.name_en)
		FROM sign_signers g JOIN sign_requests q ON q.id = g.request_id JOIN companies c ON c.id = q.company_id WHERE g.id = $1`, signer).
		Scan(&name, &email, &phone, &token, &cust, &channel, &title, &company); err != nil {
		return err
	}
	if channel == "none" {
		return nil
	}
	body := growthText(ctx, tx, cid, key, company, name, title, publicURL()+"/#/sign/"+token)
	try := []string{channel}
	if channel == "email" {
		try = append(try, "whatsapp")
	} else {
		try = append(try, "email")
	}
	for _, ch := range try {
		id, _, err := queueOut(ctx, tx, cid, outMessage{CustomerID: cust, Channel: ch, Phone: phone, Email: email, Subject: company + " · " + title, Body: body, Event: "esign"})
		if err != nil {
			return err
		}
		if id != "" {
			_, err := tx.Exec(ctx, `UPDATE sign_signers SET notified_at = now() WHERE id = $1`, signer)
			return err
		}
	}
	return nil
}

// sendSignRequest: draft → sent; the first signer (or everyone when not ordered) is asked to sign.
func sendSignRequest(ctx context.Context, tx pgx.Tx, cid, id, ip, ua string) error {
	var st string
	var ordered bool
	if err := tx.QueryRow(ctx, `SELECT status, ordered FROM sign_requests WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st, &ordered); err != nil {
		if err == pgx.ErrNoRows {
			return notFound()
		}
		return err
	}
	if st != "draft" {
		return &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	if _, err := tx.Exec(ctx, `UPDATE sign_requests SET status = 'sent', sent_at = now(), updated_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	cond := ""
	if ordered {
		cond = " AND seq = (SELECT min(seq) FROM sign_signers WHERE request_id = $1)"
	}
	rows, err := tx.Query(ctx, `UPDATE sign_signers SET status = 'pending' WHERE request_id = $1`+cond+` RETURNING id::text`, id)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, g := range ids {
		if err := notifySigner(ctx, tx, cid, id, g, "sign_request"); err != nil {
			return err
		}
	}
	// a quotation sent for signature is "sent"
	if _, err := tx.Exec(ctx, `UPDATE sales_orders SET status = 'sent' WHERE id = (SELECT sales_order_id FROM sign_requests WHERE id = $1) AND status = 'draft'`, id); err != nil {
		return err
	}
	return signEvent(ctx, tx, cid, id, nil, "sent", ip, ua, "")
}

// POST /esign/requests/{id}/{action}: send | cancel | remind
func (s *Server) esignAction(w http.ResponseWriter, r *http.Request) {
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if !isUUID(id) || !contains([]string{"send", "cancel", "remind"}, action) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		switch action {
		case "send":
			if err := sendSignRequest(ctx, tx, cid, id, clientIP(r), r.UserAgent()); err != nil {
				return nil, err
			}
		case "cancel":
			tag, err := tx.Exec(ctx, `UPDATE sign_requests SET status = 'cancelled', updated_at = now() WHERE id = $1 AND company_id = $2 AND status IN ('draft', 'sent')`, id, cid)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			if err := signEvent(ctx, tx, cid, id, nil, "cancelled", clientIP(r), r.UserAgent(), ""); err != nil {
				return nil, err
			}
		case "remind":
			var st string
			if err := tx.QueryRow(ctx, `SELECT status FROM sign_requests WHERE id = $1 AND company_id = $2`, id, cid).Scan(&st); err != nil {
				if err == pgx.ErrNoRows {
					return nil, notFound()
				}
				return nil, err
			}
			if st != "sent" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			rows, err := tx.Query(ctx, `SELECT id::text FROM sign_signers WHERE request_id = $1 AND status IN ('pending', 'viewed')`, id)
			if err != nil {
				return nil, err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, err
			}
			for _, g := range ids {
				if err := notifySigner(ctx, tx, cid, id, g, "sign_reminder"); err != nil {
					return nil, err
				}
				if _, err := tx.Exec(ctx, `UPDATE sign_signers SET reminders = reminders + 1 WHERE id = $1`, g); err != nil {
					return nil, err
				}
				if err := signEvent(ctx, tx, cid, id, &g, "reminded", clientIP(r), r.UserAgent(), ""); err != nil {
					return nil, err
				}
			}
		}
		return loadSignRequest(ctx, tx, cid, id)
	})
}

// GET /esign/requests/{id}/certificate: the certificate PDF (also stored as an attachment once completed).
func (s *Server) esignCertificate(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var pdf []byte
	var number int64
	err := dbInScope(r.Context(), s, scope(r), func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT number FROM sign_requests WHERE id = $1 AND company_id = $2`, id, cid).Scan(&number); err != nil {
			return err
		}
		var err error
		pdf, err = signCertificate(ctx, tx, cid, id)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="signature-%d.pdf"`, number))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pdf)
}

// signCertificate renders the document (when it is text), the signatures and the audit trail as a PDF.
func signCertificate(ctx context.Context, tx pgx.Tx, cid, id string) ([]byte, error) {
	var number int64
	var title, docType, hash, body, status, company string
	var completed *time.Time
	var created time.Time
	if err := tx.QueryRow(ctx, `SELECT q.number, q.title, q.doc_type, q.doc_hash, q.body, q.status, q.completed_at, q.created_at, c.name_en
		FROM sign_requests q JOIN companies c ON c.id = q.company_id WHERE q.id = $1 AND q.company_id = $2`, id, cid).
		Scan(&number, &title, &docType, &hash, &body, &status, &completed, &created, &company); err != nil {
		return nil, err
	}
	p := newPDF()
	p.line(company, 11, true, 0)
	p.line(fmt.Sprintf("Signature certificate - request #%d", number), 16, true, 0)
	p.gap(4)
	p.line("Document: "+title, 11, false, 0)
	p.line("Type: "+docType+"     Status: "+status, 10, false, 0)
	p.line("SHA-256 of the signed document: "+hash, 9, false, 0)
	p.line("Created: "+created.UTC().Format("2006-01-02 15:04:05 UTC"), 9, false, 0)
	if completed != nil {
		p.line("Completed: "+completed.UTC().Format("2006-01-02 15:04:05 UTC"), 9, false, 0)
	}
	p.rule()
	if body != "" {
		latin := 0
		total := 0
		for _, r := range body {
			if r > ' ' {
				total++
				if r < 0x250 {
					latin++
				}
			}
		}
		p.line("Document text", 12, true, 0)
		if total == 0 || latin*10 >= total*9 {
			p.line(body, 9, false, 0)
		} else {
			p.line("The document is written in Arabic script: its exact text is kept in Paxve and identified by the SHA-256 above. "+
				"A transliterated copy follows.", 9, false, 0)
			p.gap(2)
			p.line(body, 8, false, 0)
		}
		p.rule()
	}
	p.line("Signers", 12, true, 0)
	rows, err := tx.Query(ctx, `SELECT seq, name, email, phone, status, signed_at, declined_at, decline_reason, sign_method, typed_name, signature, ip, user_agent
		FROM sign_signers WHERE request_id = $1 ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	type sg struct {
		seq                                               int
		name, email, phone, status, reason, method, typed string
		sig, ip, ua                                       string
		signed, declined                                  *time.Time
	}
	var list []sg
	for rows.Next() {
		var g sg
		if err := rows.Scan(&g.seq, &g.name, &g.email, &g.phone, &g.status, &g.signed, &g.declined, &g.reason, &g.method, &g.typed, &g.sig, &g.ip, &g.ua); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, g)
	}
	rows.Close()
	for _, g := range list {
		p.ensure(110)
		contact := strings.TrimSpace(g.email + "  " + g.phone)
		p.line(fmt.Sprintf("%d. %s   %s", g.seq, g.name, contact), 10, true, 0)
		switch {
		case g.signed != nil:
			p.line(fmt.Sprintf("Signed %s from IP %s (%s)", g.signed.UTC().Format("2006-01-02 15:04:05 UTC"), g.ip, g.method), 9, false, 12)
			if g.ua != "" {
				p.line("Browser: "+g.ua, 7, false, 12)
			}
			if _, data, ok := decodeDataURL(g.sig); ok {
				_ = p.pngImage(data, 12, 200, 70)
			}
		case g.declined != nil:
			p.line(fmt.Sprintf("Declined %s: %s", g.declined.UTC().Format("2006-01-02 15:04:05 UTC"), g.reason), 9, false, 12)
		default:
			p.line("Not signed ("+g.status+")", 9, false, 12)
		}
		p.gap(4)
	}
	p.rule()
	p.line("Audit trail", 12, true, 0)
	ev, err := tx.Query(ctx, `SELECT e.created_at, e.event, coalesce(g.name, u.full_name, ''), e.ip, e.detail FROM sign_events e
		LEFT JOIN sign_signers g ON g.id = e.signer_id LEFT JOIN users u ON u.id = e.created_by WHERE e.request_id = $1 ORDER BY e.created_at`, id)
	if err != nil {
		return nil, err
	}
	for ev.Next() {
		var at time.Time
		var event, who, ip, detail string
		if err := ev.Scan(&at, &event, &who, &ip, &detail); err != nil {
			ev.Close()
			return nil, err
		}
		line := fmt.Sprintf("%s  %-10s %s", at.UTC().Format("2006-01-02 15:04:05"), event, who)
		if ip != "" {
			line += "  IP " + ip
		}
		if detail != "" {
			line += "  " + detail
		}
		p.line(line, 8, false, 0)
	}
	ev.Close()
	return p.bytes(), nil
}

// ======================= public signing page =======================

// GET /api/v1/sign/{token}: what the signer sees (the first visit is recorded as "viewed").
func (s *Server) signInfo(w http.ResponseWriter, r *http.Request) {
	sc, cid, signer, req, ok := s.tokenScope(w, r, "sign")
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE sign_signers g SET status = 'viewed', viewed_at = now() FROM sign_requests q
			WHERE g.id = $1 AND q.id = g.request_id AND q.status = 'sent' AND g.status = 'pending'`, signer)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() > 0 {
			if err := signEvent(ctx, tx, cid, req, &signer, "viewed", clientIP(r), r.UserAgent(), ""); err != nil {
				return nil, err
			}
		}
		q, err := queryJSONOne(ctx, tx, `SELECT q.number, q.title, q.doc_type, q.body, q.doc_hash, q.message, q.status, q.expires_on, q.attachment_id IS NOT NULL AS has_file,
			a.mime AS file_mime, a.filename AS file_name, q.completed_at, q.certificate_id IS NOT NULL AS has_certificate,
			c.name_en AS company, c.name_ar AS company_ar, c.logo, c.default_locale AS locale
			FROM sign_requests q JOIN companies c ON c.id = q.company_id LEFT JOIN attachments a ON a.id = q.attachment_id WHERE q.id = $1 AND q.company_id = $2`, req, cid)
		if err != nil {
			return nil, err
		}
		me, err := queryJSONOne(ctx, tx, `SELECT g.name, g.status, g.signed_at, g.sign_method, g.typed_name, g.signature,
			q.require_otp AS otp_required, (g.otp_verified_at IS NOT NULL) AS otp_verified,
			(q.status = 'sent' AND g.status IN ('pending', 'viewed') AND (q.expires_on IS NULL OR q.expires_on >= current_date)) AS can_sign
			FROM sign_signers g JOIN sign_requests q ON q.id = g.request_id WHERE g.id = $1`, signer)
		if err != nil {
			return nil, err
		}
		others, err := queryJSON(ctx, tx, `SELECT g.seq, g.name, g.status, g.signed_at, CASE WHEN g.status = 'signed' THEN g.signature ELSE '' END AS signature
			FROM sign_signers g WHERE g.request_id = $1 ORDER BY g.seq`, req)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"request": q, "me": me, "signers": others}, nil
	})
}

// GET /api/v1/sign/{token}/file and /certificate: the uploaded document, or the certificate once completed.
func (s *Server) signFile(w http.ResponseWriter, r *http.Request) {
	sc, cid, _, req, ok := s.tokenScope(w, r, "sign")
	if !ok {
		return
	}
	which := "attachment_id"
	if strings.HasSuffix(r.URL.Path, "/certificate") {
		which = "certificate_id"
	}
	var data []byte
	var mime, name string
	err := dbInScope(r.Context(), s, sc, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT a.data, a.mime, a.filename FROM sign_requests q JOIN attachments a ON a.id = q.`+which+`
			WHERE q.id = $1 AND q.company_id = $2 AND a.deleted_at IS NULL`, req, cid).Scan(&data, &mime, &name)
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if mime != "application/pdf" && (!strings.HasPrefix(mime, "image/") || mime == "image/svg+xml") {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, strings.ReplaceAll(name, `"`, "")))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox") // audit P7-L7, as attachments
	_, _ = w.Write(data)
}

// POST /api/v1/sign/{token} {method: draw | type, signature: data:image/png;base64,…, typed_name, agree: true}
func (s *Server) signSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var in struct {
		Method    string `json:"method"`
		Signature string `json:"signature"`
		TypedName string `json:"typed_name"`
		Agree     bool   `json:"agree"`
		Website   string `json:"website"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	in.TypedName = strings.TrimSpace(in.TypedName)
	if !in.Agree || (in.Method != "draw" && in.Method != "type") || (in.Method == "type" && in.TypedName == "") || len(in.TypedName) > 160 ||
		!strings.HasPrefix(in.Signature, "data:image/png;base64,") || len(in.Signature) > 300000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	_, raw, ok := decodeDataURL(in.Signature)
	if !ok || len(raw) < 100 || !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		writeErr(w, http.StatusBadRequest, "bad_signature")
		return
	}
	ip := clientIP(r)
	if !publicWriteLimiter.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	publicWriteLimiter.fail(ip)
	sc, cid, signer, req, ok := s.tokenScope(w, r, "sign")
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var qst, gst, docType, hash, body string
		var ordered, autoConfirm, needOTP, otpOK bool
		var expires *string
		var so *string
		var seq int
		if err := tx.QueryRow(ctx, `SELECT q.status, g.status, q.doc_type, q.doc_hash, q.body, q.ordered, q.auto_confirm, q.expires_on::text, q.sales_order_id::text, g.seq,
			q.require_otp, coalesce(g.otp_verified_at > now() - interval '30 minutes', false)
			FROM sign_signers g JOIN sign_requests q ON q.id = g.request_id WHERE g.id = $1 FOR UPDATE OF q, g`, signer).
			Scan(&qst, &gst, &docType, &hash, &body, &ordered, &autoConfirm, &expires, &so, &seq, &needOTP, &otpOK); err != nil {
			return nil, err
		}
		if needOTP && !otpOK {
			return nil, &apiError{status: http.StatusForbidden, code: "otp_required"}
		}
		if qst != "sent" || (gst != "pending" && gst != "viewed") {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if expires != nil && *expires < today() {
			return nil, &apiError{status: http.StatusConflict, code: "expired"}
		}
		// the quotation must still be what was sent
		if docType == "quote" && so != nil {
			text, _, err := quoteSnapshot(ctx, tx, cid, *so)
			if err != nil {
				return nil, err
			}
			if sha256Hex([]byte(text)) != hash {
				return nil, &apiError{status: http.StatusConflict, code: "document_changed"}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sign_signers SET status = 'signed', signed_at = now(), sign_method = $2, typed_name = $3, signature = $4, ip = $5, user_agent = left($6, 400)
			WHERE id = $1`, signer, in.Method, in.TypedName, in.Signature, ip, r.UserAgent()); err != nil {
			return nil, err
		}
		if err := signEvent(ctx, tx, cid, req, &signer, "signed", ip, r.UserAgent(), "sha256 "+hash+" · "+in.Method); err != nil {
			return nil, err
		}
		var left int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM sign_signers WHERE request_id = $1 AND status <> 'signed'`, req).Scan(&left); err != nil {
			return nil, err
		}
		if left > 0 {
			if ordered {
				rows, err := tx.Query(ctx, `UPDATE sign_signers SET status = 'pending' WHERE request_id = $1 AND status = 'waiting'
					AND seq = (SELECT min(seq) FROM sign_signers WHERE request_id = $1 AND status = 'waiting') RETURNING id::text`, req)
				if err != nil {
					return nil, err
				}
				next, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return nil, err
				}
				for _, g := range next {
					if err := notifySigner(ctx, tx, cid, req, g, "sign_request"); err != nil {
						return nil, err
					}
				}
			}
			return map[string]any{"ok": true, "completed": false}, nil
		}
		if err := completeSignRequest(ctx, tx, cid, req, autoConfirm, ip, r.UserAgent()); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "completed": true}, nil
	})
}

// completeSignRequest: everyone signed → certificate attachment, quote accepted (and confirmed), people told.
func completeSignRequest(ctx context.Context, tx pgx.Tx, cid, req string, autoConfirm bool, ip, ua string) error {
	if _, err := tx.Exec(ctx, `UPDATE sign_requests SET status = 'completed', completed_at = now(), updated_at = now() WHERE id = $1`, req); err != nil {
		return err
	}
	if err := signEvent(ctx, tx, cid, req, nil, "completed", ip, ua, ""); err != nil {
		return err
	}
	pdf, err := signCertificate(ctx, tx, cid, req)
	if err != nil {
		return err
	}
	var number int64
	var title, docType string
	var so, sub *string
	if err := tx.QueryRow(ctx, `SELECT number, title, doc_type, sales_order_id::text, subscription_id::text FROM sign_requests WHERE id = $1`, req).
		Scan(&number, &title, &docType, &so, &sub); err != nil {
		return err
	}
	name := fmt.Sprintf("signed-%d.pdf", number)
	var att string
	if err := tx.QueryRow(ctx, `INSERT INTO attachments (company_id, entity_type, entity_id, filename, mime, size, data, note, created_by)
		VALUES ($1, 'sign_request', $2, $3, 'application/pdf', $4, $5, 'Signature certificate', NULL) RETURNING id::text`, cid, req, name, len(pdf), pdf).Scan(&att); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE sign_requests SET certificate_id = $2 WHERE id = $1`, req, att); err != nil {
		return err
	}
	// the signed copy also sits on the quotation / subscription
	if so != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO attachments (company_id, entity_type, entity_id, filename, mime, size, data, note, created_by)
			VALUES ($1, 'sales_order', $2, $3, 'application/pdf', $4, $5, 'Signed online', NULL)`, cid, *so, name, len(pdf), pdf); err != nil {
			return err
		}
		var signer string
		_ = tx.QueryRow(ctx, `SELECT string_agg(name, ', ' ORDER BY seq) FROM sign_signers WHERE request_id = $1`, req).Scan(&signer)
		if r := []rune(signer); len(r) > 120 {
			signer = string(r[:120])
		}
		if _, err := tx.Exec(ctx, `UPDATE sales_orders SET accepted_at = now(), accepted_name = $2 WHERE id = $1 AND status IN ('draft', 'sent') AND accepted_at IS NULL`,
			*so, signer); err != nil {
			return err
		}
		if autoConfirm {
			var st string
			if err := tx.QueryRow(ctx, `SELECT status FROM sales_orders WHERE id = $1`, *so).Scan(&st); err != nil {
				return err
			}
			if st == "draft" || st == "sent" {
				if err := confirmOrderTx(ctx, tx, cid, *so, today()); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE sales_orders SET source = 'esign' WHERE id = $1 AND source = 'manual'`, *so); err != nil {
					return err
				}
				if err := signEvent(ctx, tx, cid, req, nil, "confirmed", ip, ua, "sales order confirmed"); err != nil {
					return err
				}
			}
		}
	}
	if sub != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO attachments (company_id, entity_type, entity_id, filename, mime, size, data, note, created_by)
			VALUES ($1, 'subscription', $2, $3, 'application/pdf', $4, $5, 'Signed online', NULL)`, cid, *sub, name, len(pdf), pdf); err != nil {
			return err
		}
	}
	var creator *string
	_ = tx.QueryRow(ctx, `SELECT created_by::text FROM sign_requests WHERE id = $1`, req).Scan(&creator)
	if creator != nil {
		if err := notifyUsers(ctx, tx, cid, []string{*creator}, "esign", fmt.Sprintf("Signed: %s", title), fmt.Sprintf("Request #%d is signed by everyone", number), "sign_request", req); err != nil {
			return err
		}
	}
	// every signer gets the link to the signed copy
	rows, err := tx.Query(ctx, `SELECT id::text FROM sign_signers WHERE request_id = $1`, req)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, g := range ids {
		if err := notifySigner(ctx, tx, cid, req, g, "sign_done"); err != nil {
			return err
		}
	}
	return nil
}

// POST /api/v1/sign/{token}/decline {reason}
func (s *Server) signDecline(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Reason) > 1000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	sc, cid, signer, req, ok := s.tokenScope(w, r, "sign")
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE sign_signers g SET status = 'declined', declined_at = now(), decline_reason = trim($2), ip = $3, user_agent = left($4, 400)
			FROM sign_requests q WHERE g.id = $1 AND q.id = g.request_id AND q.status = 'sent' AND g.status IN ('pending', 'viewed')`, signer, in.Reason, clientIP(r), r.UserAgent())
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := tx.Exec(ctx, `UPDATE sign_requests SET status = 'declined', updated_at = now() WHERE id = $1`, req); err != nil {
			return nil, err
		}
		if err := signEvent(ctx, tx, cid, req, &signer, "declined", clientIP(r), r.UserAgent(), in.Reason); err != nil {
			return nil, err
		}
		var creator *string
		var title string
		_ = tx.QueryRow(ctx, `SELECT created_by::text, title FROM sign_requests WHERE id = $1`, req).Scan(&creator, &title)
		if creator != nil {
			if err := notifyUsers(ctx, tx, cid, []string{*creator}, "esign", "Declined: "+title, in.Reason, "sign_request", req); err != nil {
				return nil, err
			}
		}
		return map[string]bool{"ok": true}, nil
	})
}

// signMaintenance (worker): expired requests close, and pending signers get a reminder every remind_days (5 at most).
func signMaintenance(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, `UPDATE sign_requests SET status = 'expired', updated_at = now() WHERE status = 'sent' AND expires_on < current_date
		RETURNING id::text, company_id::text`)
	if err != nil {
		return 0, err
	}
	type rc struct{ id, cid string }
	var exp []rc
	for rows.Next() {
		var x rc
		if err := rows.Scan(&x.id, &x.cid); err != nil {
			rows.Close()
			return 0, err
		}
		exp = append(exp, x)
	}
	rows.Close()
	for _, x := range exp {
		if err := signEvent(ctx, tx, x.cid, x.id, nil, "expired", "", "", ""); err != nil {
			return 0, err
		}
	}
	rows, err = tx.Query(ctx, `SELECT g.id::text, g.request_id::text, g.company_id::text FROM sign_signers g JOIN sign_requests q ON q.id = g.request_id
		WHERE q.status = 'sent' AND q.remind_days > 0 AND g.status IN ('pending', 'viewed') AND g.reminders < 5
		  AND coalesce(g.notified_at, q.sent_at) < now() - make_interval(days => q.remind_days) LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type due struct{ id, req, cid string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.req, &d.cid); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()
	for _, d := range list {
		if err := notifySigner(ctx, tx, d.cid, d.req, d.id, "sign_reminder"); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE sign_signers SET reminders = reminders + 1, notified_at = now() WHERE id = $1`, d.id); err != nil {
			return 0, err
		}
		g := d.id
		if err := signEvent(ctx, tx, d.cid, d.req, &g, "reminded", "", "", "automatic"); err != nil {
			return 0, err
		}
	}
	return len(exp) + len(list), nil
}

// ======================= signing code (audit P7-L7) =======================

// POST /api/v1/sign/{token}/otp: when the request asks for it, a 6-digit code goes to the signer's phone (WhatsApp,
// then SMS) or e-mail; it is valid 10 minutes. At most 5 codes / checks an hour per signer link.
func (s *Server) signOTPSend(w http.ResponseWriter, r *http.Request) {
	if !otpLimiter.allow("send:" + r.PathValue("token")) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	otpLimiter.fail("send:" + r.PathValue("token"))
	sc, cid, signer, _, ok := s.tokenScope(w, r, "sign")
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var need bool
		var gst, qst, name, email, phone, title, company string
		var cust *string
		if err := tx.QueryRow(ctx, `SELECT q.require_otp, g.status, q.status, g.name, g.email, g.phone, g.customer_id::text, q.title,
			coalesce(nullif(c.name_ar, ''), c.name_en)
			FROM sign_signers g JOIN sign_requests q ON q.id = g.request_id JOIN companies c ON c.id = q.company_id WHERE g.id = $1 FOR UPDATE OF g`, signer).
			Scan(&need, &gst, &qst, &name, &email, &phone, &cust, &title, &company); err != nil {
			return nil, err
		}
		if !need || qst != "sent" || (gst != "pending" && gst != "viewed") {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		code := fmt.Sprintf("%06d", randIntn(1000000))
		if _, err := tx.Exec(ctx, `UPDATE sign_signers SET otp_hash = $2, otp_expires = now() + interval '10 minutes', otp_tries = 0 WHERE id = $1`,
			signer, sha256Hex([]byte(signer+":"+code))); err != nil {
			return nil, err
		}
		body := fmt.Sprintf("%s · %s\nرمز التوقيع / Signing code / Code de signature: %s", company, title, code)
		sentTo := ""
		for _, ch := range []string{"whatsapp", "sms", "email"} {
			id, _, err := queueOut(ctx, tx, cid, outMessage{CustomerID: cust, Channel: ch, Phone: phone, Email: email, Subject: company + " · " + title,
				Body: body, Event: "esign"})
			if err != nil {
				return nil, err
			}
			if id != "" {
				sentTo = ch
				break
			}
		}
		if sentTo == "" {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "no_contact"}
		}
		return map[string]any{"sent": true, "channel": sentTo, "to": maskContact(sentTo, phone, email)}, nil
	})
}

// POST /api/v1/sign/{token}/otp/verify {code}: right code (within 10 minutes, 5 tries) → the signer may sign for
// the next 30 minutes.
func (s *Server) signOTPVerify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Code = strings.TrimSpace(in.Code)
	if len(in.Code) != 6 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !otpLimiter.allow("check:" + r.PathValue("token")) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	otpLimiter.fail("check:" + r.PathValue("token"))
	sc, cid, signer, req, ok := s.tokenScope(w, r, "sign")
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var hash string
		var tries int
		var fresh bool
		if err := tx.QueryRow(ctx, `SELECT otp_hash, otp_tries, coalesce(otp_expires > now(), false) FROM sign_signers WHERE id = $1 FOR UPDATE`, signer).
			Scan(&hash, &tries, &fresh); err != nil {
			return nil, err
		}
		if hash == "" || !fresh || tries >= 5 {
			return map[string]any{"ok": false, "reason": "expired"}, nil
		}
		if !sameHex(hash, sha256Hex([]byte(signer+":"+in.Code))) {
			if _, err := tx.Exec(ctx, `UPDATE sign_signers SET otp_tries = otp_tries + 1 WHERE id = $1`, signer); err != nil {
				return nil, err
			}
			return map[string]any{"ok": false, "reason": "wrong_code"}, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE sign_signers SET otp_verified_at = now(), otp_hash = '' WHERE id = $1`, signer); err != nil {
			return nil, err
		}
		if err := signEvent(ctx, tx, cid, req, &signer, "verified", clientIP(r), r.UserAgent(), "signing code"); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})
}

// maskContact shows where a code went without revealing the whole number / address (+961 71 ••• 456).
func maskContact(channel, phone, email string) string {
	if channel == "email" {
		at := strings.Index(email, "@")
		if at <= 1 {
			return "•••" + email[max(at, 0):]
		}
		return email[:1] + "•••" + email[at:]
	}
	d := []rune(strings.TrimSpace(phone))
	if len(d) <= 4 {
		return "•••"
	}
	return string(d[:min(4, len(d)-3)]) + " ••• " + string(d[len(d)-3:])
}
