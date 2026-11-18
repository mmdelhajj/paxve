package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Customer statements, company document settings (logo / header / footer / terms) and sending a document
// (quotation, invoice, statement) to the customer by WhatsApp, SMS or e-mail as a link to the customer portal.

type stmtLine struct {
	Date    string `json:"date"`
	Kind    string `json:"kind"` // invoice | refund | payment | credit_note
	Number  int64  `json:"number"`
	Ref     string `json:"ref"`
	Debit   string `json:"debit"`
	Credit  string `json:"credit"`
	Balance string `json:"balance"`
	DocID   string `json:"doc_id"`
}

type stmtCurrency struct {
	Currency string     `json:"currency"`
	Opening  string     `json:"opening"`
	Debits   string     `json:"debits"`
	Credits  string     `json:"credits"`
	Closing  string     `json:"closing"`
	Lines    []stmtLine `json:"lines"`
}

// customerStatement: per currency, opening balance before from, every invoice / refund (debit) and payment /
// credit note (credit) in the period with the running balance, and the closing balance. Voided documents are left out.
func customerStatement(ctx context.Context, tx pgx.Tx, customer, from, to, onlyCurrency string) ([]stmtCurrency, error) {
	rows, err := tx.Query(ctx, `
		WITH docs AS (
		  SELECT i.issue_date AS d, i.kind, i.number, coalesce(i.period, '') AS ref, i.currency::text AS cur, i.total AS debit, 0::numeric AS credit,
		         i.id::text AS id, i.created_at AS ts
		  FROM invoices i WHERE i.customer_id = $1 AND i.status <> 'void'
		  UNION ALL
		  SELECT p.payment_date, p.kind, p.number, p.notes, p.currency::text, 0, p.amount, p.id::text, p.created_at
		  FROM payments p WHERE p.customer_id = $1 AND p.status = 'posted')
		SELECT d::text, kind, number, ref, cur, debit::text, credit::text, id, (d < $2::date) AS before
		FROM docs WHERE d <= $3::date AND ($4 = '' OR cur = $4)
		ORDER BY cur, d, ts`, customer, from, to, onlyCurrency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []stmtCurrency{}
	idx := map[string]int{}
	type acc struct{ open, dr, cr, bal decimal.Decimal }
	accs := map[string]*acc{}
	for rows.Next() {
		var l stmtLine
		var cur, dr, cr string
		var before bool
		if err := rows.Scan(&l.Date, &l.Kind, &l.Number, &l.Ref, &cur, &dr, &cr, &l.DocID, &before); err != nil {
			return nil, err
		}
		if _, ok := idx[cur]; !ok {
			idx[cur] = len(out)
			out = append(out, stmtCurrency{Currency: cur, Lines: []stmtLine{}})
			accs[cur] = &acc{}
		}
		a := accs[cur]
		d, _ := decimal.NewFromString(dr)
		c, _ := decimal.NewFromString(cr)
		a.bal = a.bal.Add(d).Sub(c)
		if before {
			a.open = a.bal
			continue
		}
		a.dr, a.cr = a.dr.Add(d), a.cr.Add(c)
		if len(l.Ref) > 80 {
			l.Ref = l.Ref[:80]
		}
		l.Debit, l.Credit, l.Balance = d.StringFixed(4), c.StringFixed(4), a.bal.StringFixed(4)
		out[idx[cur]].Lines = append(out[idx[cur]].Lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		a := accs[out[i].Currency]
		out[i].Opening, out[i].Debits, out[i].Credits, out[i].Closing = a.open.StringFixed(4), a.dr.StringFixed(4), a.cr.StringFixed(4), a.bal.StringFixed(4)
	}
	return out, nil
}

func statementRange(r *http.Request) (from, to string, ok bool) {
	q := r.URL.Query()
	from, to = q.Get("from"), q.Get("to")
	if to == "" {
		to = today()
	}
	if from == "" {
		t, _ := time.Parse(time.DateOnly, to)
		from = t.AddDate(0, -3, 0).Format(time.DateOnly)
	}
	return from, to, validDate(from) && validDate(to) && from <= to
}

func (s *Server) getCustomerStatement(w http.ResponseWriter, r *http.Request) {
	from, to, ok := statementRange(r)
	cid, id := r.PathValue("cid"), r.PathValue("id")
	cur := r.URL.Query().Get("currency")
	if !ok || !isUUID(id) || (cur != "" && !currencyRe.MatchString(cur)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cust, err := queryJSONOne(ctx, tx, `SELECT c.id, c.code, c.name, c.phone, c.email, c.address, c.portal_token, ar.name AS area_name
			FROM customers c LEFT JOIN areas ar ON ar.id = c.area_id WHERE c.id = $1 AND c.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		st, err := customerStatement(ctx, tx, id, from, to, cur)
		if err != nil {
			return nil, err
		}
		return map[string]any{"customer": cust, "from": from, "to": to, "currencies": st}, nil
	})
}

// listStatements: customers with a balance or movements in the period (for bulk printing / sending).
// ?area_id= &only_balance=1
func (s *Server) listCustomerStatements(w http.ResponseWriter, r *http.Request) {
	from, to, ok := statementRange(r)
	q := r.URL.Query()
	area := q.Get("area_id")
	if !ok || (area != "" && !isUUID(area)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	onlyBalance := q.Get("only_balance") == "1"
	// one page of customers at a time (100 by default; each statement is a few queries); has_more says if there are others
	limit, offset := queryInt(r, "limit", 100, 2000), queryInt(r, "offset", 0, 1<<30)
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT c.id::text FROM customers c WHERE c.company_id = $1 AND c.status <> 'closed'
			AND ($2 = '' OR c.area_id = $2::uuid)
			AND (EXISTS (SELECT 1 FROM invoices i WHERE i.customer_id = c.id AND i.status IN ('open', 'partial'))
			     OR (NOT $5 AND (EXISTS (SELECT 1 FROM invoices i WHERE i.customer_id = c.id AND i.status <> 'void' AND i.issue_date BETWEEN $3::date AND $4::date)
			                     OR EXISTS (SELECT 1 FROM payments p WHERE p.customer_id = c.id AND p.status = 'posted' AND p.payment_date BETWEEN $3::date AND $4::date))))
			ORDER BY c.name, c.id LIMIT $6 OFFSET $7`, cid, area, from, to, onlyBalance, limit+1, offset)
		if err != nil {
			return nil, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		hasMore := len(ids) > limit
		if hasMore {
			ids = ids[:limit]
		}
		out := []map[string]any{}
		for _, id := range ids {
			cust, err := queryJSONOne(ctx, tx, `SELECT c.id, c.code, c.name, c.phone, c.email, c.address, ar.name AS area_name
				FROM customers c LEFT JOIN areas ar ON ar.id = c.area_id WHERE c.id = $1`, id)
			if err != nil {
				return nil, err
			}
			st, err := customerStatement(ctx, tx, id, from, to, "")
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"customer": cust, "currencies": st})
		}
		return map[string]any{"from": from, "to": to, "rows": out, "has_more": hasMore, "offset": offset}, nil
	})
}

// ---- company document settings ----

var logoRe = regexp.MustCompile(`^data:image/(png|jpeg|webp|svg\+xml|gif);base64,[A-Za-z0-9+/=]+$`)

const docSettingsCols = `name_en, name_ar, logo, doc_header, doc_footer, doc_terms, tax_id, quote_valid_days, base_currency::text AS base_currency`

func (s *Server) getDocSettings(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(r.PathValue("cid")) {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+docSettingsCols+` FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}

func (s *Server) putDocSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Logo           *string `json:"logo"` // data URL, "" removes
		DocHeader      *string `json:"doc_header"`
		DocFooter      *string `json:"doc_footer"`
		DocTerms       *string `json:"doc_terms"`
		TaxID          *string `json:"tax_id"`
		QuoteValidDays *int    `json:"quote_valid_days"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid := r.PathValue("cid")
	if !isUUID(cid) || (in.Logo != nil && *in.Logo != "" && (len(*in.Logo) > 400000 || !logoRe.MatchString(*in.Logo))) ||
		(in.QuoteValidDays != nil && (*in.QuoteValidDays < 1 || *in.QuoteValidDays > 365)) ||
		(in.DocHeader != nil && len(*in.DocHeader) > 2000) || (in.DocFooter != nil && len(*in.DocFooter) > 2000) ||
		(in.DocTerms != nil && len(*in.DocTerms) > 4000) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return dmlJSONOne(ctx, tx, `UPDATE companies SET logo = coalesce($2, logo), doc_header = coalesce($3, doc_header),
			doc_footer = coalesce($4, doc_footer), doc_terms = coalesce($5, doc_terms), tax_id = coalesce(trim($6), tax_id),
			quote_valid_days = coalesce($7, quote_valid_days) WHERE id = $1 RETURNING `+docSettingsCols,
			cid, in.Logo, in.DocHeader, in.DocFooter, in.DocTerms, in.TaxID, in.QuoteValidDays)
	})
}

// ---- sending a document to the customer ----

var docTexts = map[string]map[string]string{
	"quote": {
		"en": "Dear {{customer}}, please find our quotation #{{number}} of {{amount}} {{currency}}: {{link}} - {{company}}",
		"ar": "عزيزي {{customer}}، تجد عرض السعر رقم {{number}} بقيمة {{amount}} {{currency}} على الرابط: {{link}} - {{company}}",
		"fr": "Cher(e) {{customer}}, voici notre devis n°{{number}} de {{amount}} {{currency}} : {{link}} - {{company}}",
	},
	"order": {
		"en": "Dear {{customer}}, thank you for your order #{{number}} of {{amount}} {{currency}}: {{link}} - {{company}}",
		"ar": "عزيزي {{customer}}، شكراً لطلبك رقم {{number}} بقيمة {{amount}} {{currency}}: {{link}} - {{company}}",
		"fr": "Cher(e) {{customer}}, merci pour votre commande n°{{number}} de {{amount}} {{currency}} : {{link}} - {{company}}",
	},
	"invoice": {
		"en": "Dear {{customer}}, your invoice #{{number}} of {{amount}} {{currency}} is available: {{link}} - {{company}}",
		"ar": "عزيزي {{customer}}، فاتورتك رقم {{number}} بقيمة {{amount}} {{currency}} متوفرة على الرابط: {{link}} - {{company}}",
		"fr": "Cher(e) {{customer}}, votre facture n°{{number}} de {{amount}} {{currency}} est disponible : {{link}} - {{company}}",
	},
	"statement": {
		"en": "Dear {{customer}}, your account statement from {{from}} to {{to}} (balance {{balance}}): {{link}} - {{company}}",
		"ar": "عزيزي {{customer}}، كشف حسابك من {{from}} إلى {{to}} (الرصيد {{balance}}): {{link}} - {{company}}",
		"fr": "Cher(e) {{customer}}, votre relevé de compte du {{from}} au {{to}} (solde {{balance}}) : {{link}} - {{company}}",
	},
}

var docSubjects = map[string]map[string]string{
	"quote":     {"en": "Quotation #{{number}} - {{company}}", "ar": "عرض سعر رقم {{number}} - {{company}}", "fr": "Devis n°{{number}} - {{company}}"},
	"order":     {"en": "Order #{{number}} - {{company}}", "ar": "طلب رقم {{number}} - {{company}}", "fr": "Commande n°{{number}} - {{company}}"},
	"invoice":   {"en": "Invoice #{{number}} - {{company}}", "ar": "فاتورة رقم {{number}} - {{company}}", "fr": "Facture n°{{number}} - {{company}}"},
	"statement": {"en": "Account statement - {{company}}", "ar": "كشف حساب - {{company}}", "fr": "Relevé de compte - {{company}}"},
}

var emailRe = regexp.MustCompile(`^[^@\s<>"]+@[^@\s<>"]+\.[^@\s<>"]+$`)

type docTarget struct {
	customer, name, phone, email, token string
	vars                                map[string]string
	link                                string
	invoiceID                           *string
}

// documentTarget resolves the customer, the portal link and the template values of one document.
func documentTarget(ctx context.Context, tx pgx.Tx, rd *renderer, cid, kind, id, from, to string) (*docTarget, error) {
	t := &docTarget{vars: map[string]string{}}
	var number int64
	var cur, amount string
	switch kind {
	case "quote", "order":
		var orderNo *int64
		err := tx.QueryRow(ctx, `SELECT customer_id::text, number, order_number, currency::text, total::text FROM sales_orders
			WHERE id = $1 AND company_id = $2 AND status <> 'cancelled'`, id, cid).Scan(&t.customer, &number, &orderNo, &cur, &amount)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if kind == "order" && orderNo != nil {
			number = *orderNo
		}
	case "invoice":
		err := tx.QueryRow(ctx, `SELECT customer_id::text, number, currency::text, total::text FROM invoices
			WHERE id = $1 AND company_id = $2 AND status <> 'void'`, id, cid).Scan(&t.customer, &number, &cur, &amount)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		t.invoiceID = &id
	case "statement":
		t.customer = id
	default:
		return nil, badRequest("bad_request")
	}
	err := tx.QueryRow(ctx, `SELECT name, phone, email, portal_token FROM customers WHERE id = $1 AND company_id = $2`, t.customer, cid).
		Scan(&t.name, &t.phone, &t.email, &t.token)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	base := portalLink(t.token)
	switch kind {
	case "quote", "order":
		t.link = base + "/quote/" + id
	case "invoice":
		t.link = base + "/invoice/" + id
	case "statement":
		t.link = base + "/statement/" + from + "/" + to
		st, err := customerStatement(ctx, tx, t.customer, from, to, "")
		if err != nil {
			return nil, err
		}
		parts := []string{}
		for _, c := range st {
			parts = append(parts, fmtMoney(c.Closing, rd.decs[c.Currency])+" "+c.Currency)
		}
		if len(parts) == 0 {
			parts = []string{"0"}
		}
		t.vars["balance"] = strings.Join(parts, " + ")
		t.vars["from"], t.vars["to"] = from, to
	}
	t.vars["customer"] = t.name
	t.vars["company"] = rd.company()
	t.vars["link"] = t.link
	t.vars["number"] = fmt.Sprint(number)
	if cur != "" {
		t.vars["amount"] = fmtMoney(amount, rd.decs[cur])
		t.vars["currency"] = cur
	}
	return t, nil
}

func pickLocaleText(m map[string]string, locale string) string {
	if v, ok := m[locale]; ok {
		return v
	}
	return m["en"]
}

// sendDocument: POST /companies/{cid}/send-document
// {kind: quote|order|invoice|statement, id (document id; customer id for a statement), from, to,
//
//	channel: whatsapp|sms|email|link, to_address (optional phone / e-mail override), locale, message (optional custom text)}
//
// channel "link" only returns the text and a wa.me link (works without any provider).
func (s *Server) sendDocument(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind      string `json:"kind"`
		ID        string `json:"id"`
		From      string `json:"from"`
		To        string `json:"to"`
		Channel   string `json:"channel"`
		ToAddress string `json:"to_address"`
		Locale    string `json:"locale"`
		Message   string `json:"message"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !localeRe.MatchString(in.Locale) {
		in.Locale = "en"
	}
	if in.Kind == "statement" {
		if in.To == "" {
			in.To = today()
		}
		if in.From == "" {
			t, _ := time.Parse(time.DateOnly, in.To)
			in.From = t.AddDate(0, -3, 0).Format(time.DateOnly)
		}
	}
	if docTexts[in.Kind] == nil || !isUUID(in.ID) || (in.Kind == "statement" && (!validDate(in.From) || !validDate(in.To) || in.From > in.To)) ||
		(in.Channel != "whatsapp" && in.Channel != "sms" && in.Channel != "email" && in.Channel != "link") || len(in.Message) > 4000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rd, err := newRenderer(ctx, tx, cid, in.Locale)
		if err != nil {
			return nil, err
		}
		t, err := documentTarget(ctx, tx, rd, cid, in.Kind, in.ID, in.From, in.To)
		if err != nil {
			return nil, err
		}
		body := pickLocaleText(docTexts[in.Kind], in.Locale)
		if strings.TrimSpace(in.Message) != "" {
			body = in.Message
			if !strings.Contains(body, "{{link}}") {
				body += "\n{{link}}"
			}
		}
		text, _ := render(body, t.vars)
		subject, _ := render(pickLocaleText(docSubjects[in.Kind], in.Locale), t.vars)
		event := "doc_" + in.Kind
		if in.Kind == "quote" {
			// sending a draft quotation marks it sent
			if _, err := tx.Exec(ctx, `UPDATE sales_orders SET status = 'sent' WHERE id = $1 AND status = 'draft'`, in.ID); err != nil {
				return nil, err
			}
		}
		switch in.Channel {
		case "link":
			phone := normalizePhone(firstNonEmpty(in.ToAddress, t.phone), rd.phoneCountry)
			wa := "https://wa.me/"
			if phone != "" {
				wa += strings.TrimPrefix(phone, "+")
			}
			wa += "?text=" + url.QueryEscape(text)
			return map[string]string{"text": text, "link": t.link, "wa_link": wa, "phone": phone, "subject": subject}, nil
		case "email":
			addr := strings.TrimSpace(firstNonEmpty(in.ToAddress, t.email))
			if !emailRe.MatchString(addr) {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "no_email"}
			}
			prov, err := pickEmailProvider(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			if prov == nil {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "no_email_provider"}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO messages (company_id, customer_id, phone, email, subject, channel, provider_id, event, body, invoice_id)
				VALUES ($1, $2, '', $3, $4, 'email', $5, $6, $7, $8)`, cid, t.customer, addr, subject, prov.ID, event, text, t.invoiceID); err != nil {
				return nil, err
			}
			return map[string]any{"queued": 1, "to": addr, "text": text, "link": t.link}, nil
		default:
			phone := normalizePhone(firstNonEmpty(in.ToAddress, t.phone), rd.phoneCountry)
			if phone == "" {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "no_phone"}
			}
			prov, err := pickProvider(ctx, tx, cid, "")
			if err != nil {
				return nil, err
			}
			var provID *string
			if prov != nil {
				provID = &prov.ID
			}
			if _, err := tx.Exec(ctx, `INSERT INTO messages (company_id, customer_id, phone, channel, provider_id, event, body, invoice_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, cid, t.customer, phone, channelFor(prov, in.Channel), provID, event, text, t.invoiceID); err != nil {
				return nil, err
			}
			return map[string]any{"queued": 1, "to": phone, "text": text, "link": t.link, "has_provider": prov != nil}, nil
		}
	})
}

// sendStatements: POST /companies/{cid}/statements/send {from, to, customer_ids, channel: whatsapp|sms|email, locale}
func (s *Server) sendStatements(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From        string   `json:"from"`
		To          string   `json:"to"`
		CustomerIDs []string `json:"customer_ids"`
		Channel     string   `json:"channel"`
		Locale      string   `json:"locale"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !localeRe.MatchString(in.Locale) {
		in.Locale = "en"
	}
	bad := !validDate(in.From) || !validDate(in.To) || in.From > in.To || len(in.CustomerIDs) == 0 || len(in.CustomerIDs) > 5000 ||
		(in.Channel != "whatsapp" && in.Channel != "sms" && in.Channel != "email")
	for _, id := range in.CustomerIDs {
		bad = bad || !isUUID(id)
	}
	if bad {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rd, err := newRenderer(ctx, tx, cid, in.Locale)
		if err != nil {
			return nil, err
		}
		var prov *providerRow
		if in.Channel == "email" {
			prov, err = pickEmailProvider(ctx, tx, cid)
			if err == nil && prov == nil {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "no_email_provider"}
			}
		} else {
			prov, err = pickProvider(ctx, tx, cid, "")
		}
		if err != nil {
			return nil, err
		}
		var provID *string
		if prov != nil {
			provID = &prov.ID
		}
		queued := 0
		skipped := []map[string]string{}
		for _, id := range in.CustomerIDs {
			t, err := documentTarget(ctx, tx, rd, cid, "statement", id, in.From, in.To)
			if err != nil {
				if ae, ok := err.(*apiError); ok && ae.status == http.StatusNotFound {
					continue
				}
				return nil, err
			}
			text, _ := render(pickLocaleText(docTexts["statement"], in.Locale), t.vars)
			if in.Channel == "email" {
				if !emailRe.MatchString(strings.TrimSpace(t.email)) {
					skipped = append(skipped, map[string]string{"customer": t.name, "reason": "no_email"})
					continue
				}
				subject, _ := render(pickLocaleText(docSubjects["statement"], in.Locale), t.vars)
				_, err = tx.Exec(ctx, `INSERT INTO messages (company_id, customer_id, phone, email, subject, channel, provider_id, event, body)
					VALUES ($1, $2, '', $3, $4, 'email', $5, 'doc_statement', $6)`, cid, t.customer, strings.TrimSpace(t.email), subject, provID, text)
			} else {
				phone := normalizePhone(t.phone, rd.phoneCountry)
				if phone == "" {
					skipped = append(skipped, map[string]string{"customer": t.name, "reason": "no_phone"})
					continue
				}
				_, err = tx.Exec(ctx, `INSERT INTO messages (company_id, customer_id, phone, channel, provider_id, event, body)
					VALUES ($1, $2, $3, $4, $5, 'doc_statement', $6)`, cid, t.customer, phone, channelFor(prov, in.Channel), provID, text)
			}
			if err != nil {
				return nil, err
			}
			queued++
		}
		return map[string]any{"queued": queued, "skipped": skipped, "has_provider": prov != nil}, nil
	})
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func pickEmailProvider(ctx context.Context, tx pgx.Tx, cid string) (*providerRow, error) {
	p := &providerRow{}
	err := tx.QueryRow(ctx, `SELECT id::text, kind FROM message_providers WHERE company_id = $1 AND is_active AND kind = 'smtp'
		ORDER BY is_default DESC, created_at LIMIT 1`, cid).Scan(&p.ID, &p.Kind)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// ---- portal: quotations, invoices and statement of the link's customer ----

func (s *Server) portalQuote(w http.ResponseWriter, r *http.Request) {
	sc, cid, customer, ok := s.portalScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var mine bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sales_orders WHERE id = $1 AND customer_id = $2 AND status NOT IN ('draft', 'cancelled'))`,
			id, customer).Scan(&mine); err != nil {
			return nil, err
		}
		if !mine {
			return nil, notFound()
		}
		o, err := loadOrder(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		m := o.(map[string]json.RawMessage)
		delete(m, "deliveries")
		delete(m, "invoices")
		return m, nil
	})
}

// portalAcceptQuote: the customer accepts a sent quotation on the portal (typed name as signature).
func (s *Server) portalAcceptQuote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 120 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	sc, _, customer, ok := s.portalScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE sales_orders SET accepted_at = now(), accepted_name = $3
			WHERE id = $1 AND customer_id = $2 AND status = 'sent' AND accepted_at IS NULL
			  AND (valid_until IS NULL OR valid_until >= current_date)`, id, customer, name)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		// the salesperson gets a follow-up task
		if _, err := tx.Exec(ctx, `INSERT INTO crm_activities (company_id, customer_id, lead_id, kind, summary, due_date, assigned_to)
			SELECT o.company_id, o.customer_id, o.lead_id, 'task', 'Quotation #' || o.number || ' accepted by ' || $2 || ' - confirm the order',
			       current_date, o.salesperson_id FROM sales_orders o WHERE o.id = $1`, id, name); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

func (s *Server) portalStatement(w http.ResponseWriter, r *http.Request) {
	from, to := r.PathValue("from"), r.PathValue("to")
	if !validDate(from) || !validDate(to) || from > to {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	sc, _, customer, ok := s.portalScope(w, r)
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		st, err := customerStatement(ctx, tx, customer, from, to, "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"from": from, "to": to, "currencies": st}, nil
	})
}
