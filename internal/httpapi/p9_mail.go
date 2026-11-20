package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Phase 9 — e-mail from any record (ticket, quotation, invoice, purchase order…): the company's SMTP sends it with a
// PDF of the document and chosen files attached; every record has a thread token, so the customer's reply (subject
// "[PX-xxxxxxxxxx]", or the plus address name+token@ when the company routes a mailbox to Paxve) comes back to the
// same record and shows in its chatter.

var mailTokenRe = regexp.MustCompile(`(?i)\[PX-([a-z0-9]{10})\]`)
var plusTokenRe = regexp.MustCompile(`\+([a-z0-9]{10})@`)

func randLower(n int) string {
	const abc = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		v, _ := rand.Int(rand.Reader, big.NewInt(int64(len(abc))))
		out[i] = abc[v.Int64()]
	}
	return string(out)
}

func mailThreadToken(ctx context.Context, tx pgx.Tx, cid, typ, id string) (string, error) {
	var tok string
	err := tx.QueryRow(ctx, `SELECT token FROM record_mail_threads WHERE company_id = $1 AND entity_type = $2 AND entity_id = $3`, cid, typ, id).Scan(&tok)
	if err == nil {
		return tok, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	for i := 0; i < 5; i++ {
		tok = randLower(10)
		tag, err := tx.Exec(ctx, `INSERT INTO record_mail_threads (company_id, entity_type, entity_id, token) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, cid, typ, id, tok)
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() == 1 {
			return tok, nil
		}
		if err := tx.QueryRow(ctx, `SELECT token FROM record_mail_threads WHERE company_id = $1 AND entity_type = $2 AND entity_id = $3`, cid, typ, id).Scan(&tok); err == nil {
			return tok, nil
		}
	}
	return "", errors.New("no mail token")
}

// recordPDF: a PDF of the document (number, party, lines, totals). Like the e-sign certificate it uses the PDF base
// fonts: Arabic text is written in Latin letters; the e-mail text and the portal link keep the exact wording.
func recordPDF(ctx context.Context, tx pgx.Tx, cid, typ, id string) ([]byte, string, error) {
	var company string
	_ = tx.QueryRow(ctx, `SELECT coalesce(nullif(name_en, ''), name_ar) FROM companies WHERE id = $1`, cid).Scan(&company)
	p := newPDF()
	p.newPage()
	p.line(company, 16, true, 0)
	p.rule()
	title := recordTitle(ctx, tx, typ, id)
	type ln struct{ desc, qty, price, amount string }
	var lines []ln
	var total, cur, date string
	var q string
	switch typ {
	case "invoice":
		_ = tx.QueryRow(ctx, `SELECT issue_date::text, currency::text, total::text FROM invoices WHERE id = $1`, id).Scan(&date, &cur, &total)
		q = `SELECT description, trim_scale(quantity)::text, unit_price::text, amount::text FROM invoice_lines WHERE invoice_id = $1 ORDER BY line_no`
	case "sales_order":
		var on *int64
		var qn int64
		_ = tx.QueryRow(ctx, `SELECT quote_date::text, currency::text, total::text, number, order_number FROM sales_orders WHERE id = $1`, id).Scan(&date, &cur, &total, &qn, &on)
		if on != nil {
			title = fmt.Sprintf("Order #%d", *on)
		}
		q = `SELECT description, trim_scale(quantity)::text, unit_price::text, amount::text FROM sales_order_lines WHERE order_id = $1 ORDER BY line_no`
	case "purchase_order":
		_ = tx.QueryRow(ctx, `SELECT order_date::text, currency::text, total::text FROM purchase_orders WHERE id = $1`, id).Scan(&date, &cur, &total)
		q = `SELECT description, trim_scale(quantity)::text, unit_price::text, (quantity * unit_price)::text FROM purchase_order_lines WHERE order_id = $1 ORDER BY line_no`
	case "bill":
		_ = tx.QueryRow(ctx, `SELECT bill_date::text, currency::text, total::text FROM bills WHERE id = $1`, id).Scan(&date, &cur, &total)
	case "ticket":
		var desc, status string
		_ = tx.QueryRow(ctx, `SELECT created_at::date::text, description, status FROM tickets WHERE id = $1`, id).Scan(&date, &desc, &status)
		p.line(title, 14, true, 0)
		p.line(date+" · "+status, 10, false, 0)
		p.gap(6)
		p.line(desc, 11, false, 0)
		return p.bytes(), strings.ReplaceAll(strings.SplitN(title, " · ", 2)[0], "#", "") + ".pdf", nil
	}
	p.line(title, 14, true, 0)
	if date != "" {
		p.line(date, 10, false, 0)
	}
	if q != "" {
		rows, err := tx.Query(ctx, q, id)
		if err != nil {
			return nil, "", err
		}
		for rows.Next() {
			var l ln
			if err := rows.Scan(&l.desc, &l.qty, &l.price, &l.amount); err != nil {
				rows.Close()
				return nil, "", err
			}
			lines = append(lines, l)
		}
		rows.Close()
		p.gap(8)
		for _, l := range lines {
			p.line(fmt.Sprintf("%s  ×%s  @ %s  =  %s %s", l.desc, l.qty, l.price, l.amount, cur), 10, false, 0)
		}
	}
	if total != "" {
		p.rule()
		p.line("TOTAL: "+total+" "+cur, 13, true, 0)
	}
	name := strings.ReplaceAll(strings.SplitN(title, " · ", 2)[0], "#", "")
	name = strings.Join(strings.Fields(name), "-") + ".pdf"
	return p.bytes(), name, nil
}

// POST /companies/{cid}/record-mail {type, id, to, cc, subject, body, attach_pdf, attachment_ids}
func (s *Server) sendRecordMail(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type          string   `json:"type"`
		ID            string   `json:"id"`
		To            string   `json:"to"`
		Cc            string   `json:"cc"`
		Subject       string   `json:"subject"`
		Body          string   `json:"body"`
		AttachPDF     bool     `json:"attach_pdf"`
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.To, in.Subject, in.Body = strings.TrimSpace(in.To), strings.TrimSpace(in.Subject), strings.TrimSpace(in.Body)
	var ccs []string
	for _, c := range strings.FieldsFunc(in.Cc, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if !emailRe.MatchString(c) {
			writeErr(w, http.StatusBadRequest, "bad_email")
			return
		}
		ccs = append(ccs, c)
	}
	if !emailRe.MatchString(in.To) || strings.ContainsAny(in.To, "\r\n") || len(ccs) > 5 || in.Subject == "" || len(in.Subject) > 250 || in.Body == "" ||
		len(in.Body) > 20000 || len(in.AttachmentIDs) > 10 || !allUUID(in.AttachmentIDs) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !chatterAllowed(r, in.Type, true) || !attachAllowed(r, in.Type, true) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := attachmentTarget(ctx, tx, cid, in.Type, in.ID); err != nil {
			return nil, err
		}
		prov, err := pickEmailProvider(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if prov == nil {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "no_email_provider"}
		}
		tok, err := mailThreadToken(ctx, tx, cid, in.Type, in.ID)
		if err != nil {
			return nil, err
		}
		subject := in.Subject
		if !strings.Contains(subject, "[PX-"+tok+"]") {
			subject += " [PX-" + tok + "]"
		}
		var replyTo string
		if err := tx.QueryRow(ctx, `SELECT reply_to_email FROM companies WHERE id = $1`, cid).Scan(&replyTo); err != nil {
			return nil, err
		}
		if at := strings.LastIndex(replyTo, "@"); at > 0 && emailRe.MatchString(replyTo) {
			local := replyTo[:at]
			if i := strings.Index(local, "+"); i >= 0 {
				local = local[:i]
			}
			replyTo = local + "+" + tok + replyTo[at:]
		} else {
			replyTo = ""
		}
		files := []map[string]string{}
		ids := []string{}
		total := 0
		for _, a := range in.AttachmentIDs {
			var name string
			var size int
			err := tx.QueryRow(ctx, `SELECT filename, size FROM attachments WHERE id = $1 AND company_id = $2 AND entity_type = $3 AND entity_id = $4 AND deleted_at IS NULL`,
				a, cid, in.Type, in.ID).Scan(&name, &size)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, badRequest("invalid_attachment")
			}
			if err != nil {
				return nil, err
			}
			total += size
			ids = append(ids, a)
			files = append(files, map[string]string{"id": a, "filename": name})
		}
		if in.AttachPDF {
			pdf, name, err := recordPDF(ctx, tx, cid, in.Type, in.ID)
			if err != nil {
				return nil, err
			}
			if err := checkStorageQuota(ctx, tx, cid, len(pdf)); err != nil {
				return nil, err
			}
			var aid string
			if err := tx.QueryRow(ctx, `INSERT INTO attachments (company_id, entity_type, entity_id, filename, mime, size, data, note)
				VALUES ($1, $2, $3, $4, 'application/pdf', $5, $6, 'e-mail') RETURNING id::text`, cid, in.Type, in.ID, name, len(pdf), pdf).Scan(&aid); err != nil {
				return nil, err
			}
			total += len(pdf)
			ids = append(ids, aid)
			files = append(files, map[string]string{"id": aid, "filename": name})
		}
		if total > 15<<20 {
			return nil, badRequest("attachments_too_large")
		}
		var customer *string
		if in.Type == "customer" {
			customer = &in.ID
		}
		var mid string
		for i, to := range append([]string{in.To}, ccs...) {
			var m string
			if err := tx.QueryRow(ctx, `INSERT INTO messages (company_id, customer_id, phone, email, subject, channel, provider_id, event, body, params,
				attachment_ids, reply_to, entity_type, entity_id)
				VALUES ($1, $2, '', $3, left($4, 200), 'email', $5, 'custom', $6, '[]', $7::uuid[], $8, $9, $10) RETURNING id::text`,
				cid, customer, to, subject, prov.ID, in.Body, ids, replyTo, in.Type, in.ID).Scan(&m); err != nil {
				return nil, err
			}
			if i == 0 {
				mid = m
			}
		}
		fb, _ := json.Marshal(files)
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO record_mails (company_id, entity_type, entity_id, direction, address, cc, subject, body, files, message_id)
			VALUES ($1, $2, $3, 'out', $4, $5, left($6, 300), $7, $8, $9) RETURNING id::text`, cid, in.Type, in.ID, in.To, strings.Join(ccs, ", "),
			subject, in.Body, fb, mid).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, recordMailSel+` WHERE rm.id = $1`, id)
	})
}

const recordMailSel = `SELECT rm.id, rm.direction, rm.address, rm.cc, rm.subject, rm.body, rm.files, rm.created_at, u.full_name AS author,
	m.status AS delivery, m.error AS delivery_error FROM record_mails rm LEFT JOIN users u ON u.id = rm.created_by LEFT JOIN messages m ON m.id = rm.message_id`

// GET /companies/{cid}/record-mail?type=&id=: the e-mails of a record, both ways, newest first.
func (s *Server) listRecordMail(w http.ResponseWriter, r *http.Request) {
	cid, typ, id := r.PathValue("cid"), r.URL.Query().Get("type"), r.URL.Query().Get("id")
	if !chatterAllowed(r, typ, false) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := attachmentTarget(ctx, tx, cid, typ, id); err != nil {
			return nil, err
		}
		mails, err := queryJSON(ctx, tx, recordMailSel+` WHERE rm.company_id = $1 AND rm.entity_type = $2 AND rm.entity_id = $3 ORDER BY rm.created_at DESC LIMIT 200`,
			cid, typ, id)
		if err != nil {
			return nil, err
		}
		var configured bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM message_providers WHERE company_id = $1 AND is_active AND kind = 'smtp')`, cid).Scan(&configured); err != nil {
			return nil, err
		}
		var to string
		_ = tx.QueryRow(ctx, recordEmailSQL(typ), id).Scan(&to)
		var replyTo string
		_ = tx.QueryRow(ctx, `SELECT reply_to_email FROM companies WHERE id = $1`, cid).Scan(&replyTo)
		return map[string]any{"mails": mails, "configured": configured, "default_to": to, "default_subject": recordTitle(ctx, tx, typ, id),
			"reply_to": replyTo}, nil
	})
}

// recordEmailSQL: the e-mail address of the record's customer / supplier / contact.
func recordEmailSQL(typ string) string {
	switch typ {
	case "invoice":
		return `SELECT c.email FROM invoices x JOIN customers c ON c.id = x.customer_id WHERE x.id = $1`
	case "sales_order":
		return `SELECT c.email FROM sales_orders x JOIN customers c ON c.id = x.customer_id WHERE x.id = $1`
	case "ticket":
		return `SELECT coalesce(nullif(x.contact_email, ''), c.email, '') FROM tickets x LEFT JOIN customers c ON c.id = x.customer_id WHERE x.id = $1`
	case "customer":
		return `SELECT email FROM customers WHERE id = $1`
	case "supplier":
		return `SELECT email FROM suppliers WHERE id = $1`
	case "bill":
		return `SELECT s.email FROM bills x JOIN suppliers s ON s.id = x.supplier_id WHERE x.id = $1`
	case "purchase_order":
		return `SELECT s.email FROM purchase_orders x JOIN suppliers s ON s.id = x.supplier_id WHERE x.id = $1`
	case "lead":
		return `SELECT email FROM crm_leads WHERE id = $1`
	case "applicant":
		return `SELECT email FROM job_applicants WHERE id = $1`
	}
	return `SELECT ''::text WHERE $1::uuid IS NULL`
}

// PUT /companies/{cid}/record-mail/settings {reply_to_email}: the mailbox whose mail the provider forwards to Paxve.
func (s *Server) putMailSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReplyTo string `json:"reply_to_email"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.ReplyTo = strings.TrimSpace(strings.ToLower(in.ReplyTo))
	if in.ReplyTo != "" && (!emailRe.MatchString(in.ReplyTo) || len(in.ReplyTo) > 200) {
		writeErr(w, http.StatusBadRequest, "bad_email")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE companies SET reply_to_email = $2 WHERE id = $1`, cid, in.ReplyTo); err != nil {
			return nil, err
		}
		return map[string]string{"reply_to_email": in.ReplyTo}, nil
	})
}

// inboundRecordReply: an inbound e-mail carrying a thread token is added to its record (chatter + mail list).
// Returns the record type ("" when the mail belongs to no record).
func inboundRecordReply(ctx context.Context, tx pgx.Tx, cid, from, name, to, subject, text string) (string, string, error) {
	tok := ""
	if m := mailTokenRe.FindStringSubmatch(subject); m != nil {
		tok = strings.ToLower(m[1])
	} else if m := plusTokenRe.FindStringSubmatch(strings.ToLower(to)); m != nil {
		tok = m[1]
	}
	if tok == "" {
		return "", "", nil
	}
	var typ, id string
	err := tx.QueryRow(ctx, `SELECT entity_type, entity_id::text FROM record_mail_threads WHERE company_id = $1 AND token = $2`, cid, tok).Scan(&typ, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	body := strings.TrimSpace(text)
	if len(body) > 15000 {
		body = body[:15000]
	}
	if _, err := tx.Exec(ctx, `INSERT INTO record_mails (company_id, entity_type, entity_id, direction, address, subject, body, created_by)
		VALUES ($1, $2, $3, 'in', left($4, 200), left($5, 300), $6, NULL)`, cid, typ, id, from, subject, body); err != nil {
		return "", "", err
	}
	who := strings.TrimSpace(name + " <" + from + ">")
	short := body
	if len([]rune(short)) > 4000 {
		short = string([]rune(short)[:4000]) + "…"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO record_messages (company_id, entity_type, entity_id, body, mentions, created_by)
		VALUES ($1, $2, $3, $4, '{}', NULL)`, cid, typ, id, "✉ "+who+"\n"+short); err != nil {
		return "", "", err
	}
	if typ == "ticket" {
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_notes (company_id, ticket_id, body, created_by) VALUES ($1, $2, $3, NULL)`, cid, id, "email: "+short); err != nil {
			return "", "", err
		}
		if _, err := tx.Exec(ctx, `UPDATE tickets SET updated_at = now(), status = CASE WHEN status = 'waiting' THEN 'in_progress' ELSE status END WHERE id = $1`, id); err != nil {
			return "", "", err
		}
	}
	// the people who wrote from this record are told
	rows, err := tx.Query(ctx, `SELECT DISTINCT created_by::text FROM record_mails WHERE company_id = $1 AND entity_type = $2 AND entity_id = $3
		AND direction = 'out' AND created_by IS NOT NULL AND created_at > now() - interval '180 days'`, cid, typ, id)
	if err != nil {
		return "", "", err
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", "", err
	}
	if err := notifyUsers(ctx, tx, cid, users, "mail_reply", recordTitle(ctx, tx, typ, id), who+": "+short, typ, id); err != nil {
		return "", "", err
	}
	return typ, id, nil
}
