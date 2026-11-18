package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

var placeholderRe = regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)

var msgEvents = map[string]bool{"invoice_issued": true, "payment_received": true, "reminder": true, "custom": true}
var providerKinds = map[string]bool{"whatsapp_cloud": true, "twilio_sms": true, "generic_http": true, "log": true}

// Default templates created the first time a company opens Messages.
var defaultTemplates = []struct{ Event, Locale, Name, Body string }{
	{"reminder", "ar", "تذكير", "عزيزي {{customer}}، فاتورتك عن شهر {{period}} بقيمة {{amount}} {{currency}} مستحقة. الرصيد: {{balance}}. شكراً - {{company}}"},
	{"reminder", "en", "Reminder", "Dear {{customer}}, your bill for {{period}} of {{amount}} {{currency}} is due. Balance: {{balance}}. Thank you - {{company}}"},
	{"reminder", "fr", "Rappel", "Cher(e) {{customer}}, votre facture de {{period}} de {{amount}} {{currency}} est due. Solde : {{balance}}. Merci - {{company}}"},
	{"invoice_issued", "ar", "فاتورة جديدة", "عزيزي {{customer}}، صدرت فاتورتك رقم {{number}} عن شهر {{period}} بقيمة {{amount}} {{currency}}، تستحق في {{due_date}}. {{company}}"},
	{"invoice_issued", "en", "New bill", "Dear {{customer}}, your bill #{{number}} for {{period}} is {{amount}} {{currency}}, due {{due_date}}. {{company}}"},
	{"invoice_issued", "fr", "Nouvelle facture", "Cher(e) {{customer}}, votre facture n°{{number}} de {{period}} s'élève à {{amount}} {{currency}}, échéance {{due_date}}. {{company}}"},
	{"payment_received", "ar", "إيصال دفع", "شكراً {{customer}}، استلمنا دفعتك رقم {{number}} بقيمة {{amount}} {{currency}}. الرصيد المتبقي: {{balance}}. {{company}}"},
	{"payment_received", "en", "Payment receipt", "Thank you {{customer}}, we received your payment #{{number}} of {{amount}} {{currency}}. Remaining balance: {{balance}}. {{company}}"},
	{"payment_received", "fr", "Reçu de paiement", "Merci {{customer}}, nous avons reçu votre paiement n°{{number}} de {{amount}} {{currency}}. Solde restant : {{balance}}. {{company}}"},
}

func seedTemplates(ctx context.Context, tx pgx.Tx, cid string) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM message_templates WHERE company_id = $1`, cid).Scan(&n); err != nil || n > 0 {
		return err
	}
	for _, d := range defaultTemplates {
		if _, err := tx.Exec(ctx, `INSERT INTO message_templates (company_id, event, locale, name, body) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT DO NOTHING`, cid, d.Event, d.Locale, d.Name, d.Body); err != nil {
			return err
		}
	}
	return nil
}

// ---- formatting ----

func groupThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

func fmtMoney(s string, dec int32) string {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return s
	}
	return groupThousands(d.StringFixed(dec))
}

// ---- rendering one message ----

type msgCtx struct {
	CustomerID, Name, Phone string
	Vars                    map[string]string
	InvoiceID, PaymentID    *string
}

type renderer struct {
	cid, locale  string
	decs         map[string]int32
	companyEN    string
	companyAR    string
	phoneCountry string
}

func newRenderer(ctx context.Context, tx pgx.Tx, cid, locale string) (*renderer, error) {
	r := &renderer{cid: cid, locale: locale, decs: map[string]int32{}}
	if err := tx.QueryRow(ctx, `SELECT name_en, name_ar, phone_country FROM companies WHERE id = $1`, cid).
		Scan(&r.companyEN, &r.companyAR, &r.phoneCountry); err != nil {
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT code::text, decimals FROM currencies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var d int16
		if err := rows.Scan(&c, &d); err != nil {
			return nil, err
		}
		r.decs[c] = int32(d)
	}
	return r, rows.Err()
}

func (r *renderer) company() string {
	if r.locale == "ar" && r.companyAR != "" {
		return r.companyAR
	}
	return r.companyEN
}

// customerContext loads the values used by templates for one customer (and optional invoice/payment).
func (r *renderer) customerContext(ctx context.Context, tx pgx.Tx, customerID string, invoiceID, paymentID *string) (*msgCtx, error) {
	m := &msgCtx{CustomerID: customerID, Vars: map[string]string{}, InvoiceID: invoiceID, PaymentID: paymentID}
	if err := tx.QueryRow(ctx, `SELECT name, phone FROM customers WHERE id = $1 AND company_id = $2`, customerID, r.cid).
		Scan(&m.Name, &m.Phone); err != nil {
		if err == pgx.ErrNoRows {
			return nil, badRequest("invalid_customer")
		}
		return nil, err
	}
	m.Vars["customer"] = m.Name
	m.Vars["company"] = r.company()

	// balance per currency
	rows, err := tx.Query(ctx, `SELECT currency::text, sum(total - paid)::text FROM invoices
		WHERE customer_id = $1 AND status IN ('open', 'partial') GROUP BY currency ORDER BY currency`, customerID)
	if err != nil {
		return nil, err
	}
	var parts []string
	firstCur, firstAmt := "", ""
	for rows.Next() {
		var cur, amt string
		if err := rows.Scan(&cur, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		if firstCur == "" {
			firstCur, firstAmt = cur, amt
		}
		parts = append(parts, fmtMoney(amt, r.decs[cur])+" "+cur)
	}
	rows.Close()
	if len(parts) == 0 {
		m.Vars["balance"] = "0"
	} else {
		m.Vars["balance"] = strings.Join(parts, " + ")
	}
	if firstCur != "" {
		m.Vars["amount"] = fmtMoney(firstAmt, r.decs[firstCur])
		m.Vars["currency"] = firstCur
	}
	// latest open invoice → period / number / due date (for reminders)
	var num *int64
	var period, due *string
	_ = tx.QueryRow(ctx, `SELECT number, coalesce(period, to_char(issue_date, 'YYYY-MM')), due_date::text FROM invoices WHERE customer_id = $1 AND status IN ('open', 'partial')
		ORDER BY issue_date DESC, number DESC LIMIT 1`, customerID).Scan(&num, &period, &due)
	setOpt := func(k string, v *string) {
		if v != nil {
			m.Vars[k] = *v
		}
	}
	setOpt("period", period)
	setOpt("due_date", due)
	if num != nil {
		m.Vars["number"] = decimal.NewFromInt(*num).String()
	}

	if invoiceID != nil {
		var n int64
		var cur, total string
		var p *string
		var dd string
		if err := tx.QueryRow(ctx, `SELECT number, currency::text, total::text, coalesce(period, to_char(issue_date, 'YYYY-MM')), due_date::text FROM invoices
			WHERE id = $1 AND customer_id = $2`, *invoiceID, customerID).Scan(&n, &cur, &total, &p, &dd); err != nil {
			if err == pgx.ErrNoRows {
				return nil, badRequest("invalid_invoice")
			}
			return nil, err
		}
		m.Vars["number"] = decimal.NewFromInt(n).String()
		m.Vars["amount"] = fmtMoney(total, r.decs[cur])
		m.Vars["currency"] = cur
		m.Vars["due_date"] = dd
		if p != nil {
			m.Vars["period"] = *p
		} else {
			m.Vars["period"] = ""
		}
	}
	if paymentID != nil {
		var n int64
		var cur, amt string
		if err := tx.QueryRow(ctx, `SELECT number, currency::text, amount::text FROM payments WHERE id = $1 AND customer_id = $2`,
			*paymentID, customerID).Scan(&n, &cur, &amt); err != nil {
			if err == pgx.ErrNoRows {
				return nil, badRequest("invalid_payment")
			}
			return nil, err
		}
		m.Vars["number"] = decimal.NewFromInt(n).String()
		m.Vars["amount"] = fmtMoney(amt, r.decs[cur])
		m.Vars["currency"] = cur
	}
	return m, nil
}

// render fills {{placeholders}}; returns the text and the values in order of appearance (WhatsApp template params).
func render(body string, vars map[string]string) (string, []string) {
	var params []string
	text := placeholderRe.ReplaceAllStringFunc(body, func(tok string) string {
		k := placeholderRe.FindStringSubmatch(tok)[1]
		v := vars[k]
		params = append(params, v)
		return v
	})
	return strings.TrimSpace(text), params
}

type tplRow struct {
	ID, Body, Locale string
}

func pickTemplate(ctx context.Context, tx pgx.Tx, cid, event, templateID, locale string) (*tplRow, error) {
	t := &tplRow{}
	var err error
	if templateID != "" {
		err = tx.QueryRow(ctx, `SELECT id::text, body, locale FROM message_templates WHERE id = $1 AND company_id = $2`, templateID, cid).
			Scan(&t.ID, &t.Body, &t.Locale)
	} else {
		err = tx.QueryRow(ctx, `SELECT id::text, body, locale FROM message_templates
			WHERE company_id = $1 AND event = $2 AND is_active
			ORDER BY (locale = $3) DESC, (locale = 'ar') DESC, created_at LIMIT 1`, cid, event, locale).Scan(&t.ID, &t.Body, &t.Locale)
	}
	if err == pgx.ErrNoRows {
		return nil, badRequest("no_template")
	}
	return t, err
}

type providerRow struct {
	ID, Kind string
}

func pickProvider(ctx context.Context, tx pgx.Tx, cid, providerID string) (*providerRow, error) {
	p := &providerRow{}
	var err error
	if providerID != "" {
		err = tx.QueryRow(ctx, `SELECT id::text, kind FROM message_providers WHERE id = $1 AND company_id = $2 AND is_active`, providerID, cid).Scan(&p.ID, &p.Kind)
	} else {
		err = tx.QueryRow(ctx, `SELECT id::text, kind FROM message_providers WHERE company_id = $1 AND is_active
			ORDER BY is_default DESC, created_at LIMIT 1`, cid).Scan(&p.ID, &p.Kind)
	}
	if err == pgx.ErrNoRows {
		return nil, nil // queued without a provider; the worker marks it failed with a clear reason
	}
	return p, err
}

func channelFor(p *providerRow, requested string) string {
	if p != nil && p.Kind == "twilio_sms" {
		return "sms"
	}
	if p != nil && p.Kind == "whatsapp_cloud" {
		return "whatsapp"
	}
	if requested == "sms" {
		return "sms"
	}
	return "whatsapp"
}

// queueMessage inserts one queued message. Returns false + reason when the customer cannot be messaged.
func queueMessage(ctx context.Context, tx pgx.Tx, r *renderer, m *msgCtx, event string, tpl *tplRow, body string,
	prov *providerRow, channel string) (bool, string, string, error) {
	phone := normalizePhone(m.Phone, r.phoneCountry)
	if phone == "" {
		return false, "no_phone", "", nil
	}
	var text string
	var params []string
	var tplID *string
	if tpl != nil {
		text, params = render(tpl.Body, m.Vars)
		tplID = &tpl.ID
	} else {
		text, params = render(body, m.Vars)
	}
	if text == "" {
		return false, "empty", "", nil
	}
	pj, _ := json.Marshal(params)
	var provID *string
	if prov != nil {
		provID = &prov.ID
	}
	_, err := tx.Exec(ctx, `INSERT INTO messages (company_id, customer_id, phone, channel, provider_id, template_id, event, body, params,
		invoice_id, payment_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		r.cid, m.CustomerID, phone, channelFor(prov, channel), provID, tplID, event, text, pj, m.InvoiceID, m.PaymentID)
	return err == nil, "", text, err
}

// autoMessage is called inside invoice/payment transactions when the company enabled automatic sending.
func autoMessage(ctx context.Context, tx pgx.Tx, cid, event, customerID string, invoiceID, paymentID *string) error {
	var onPay, onInv bool
	if err := tx.QueryRow(ctx, `SELECT auto_send_on_payment, auto_send_on_invoice FROM companies WHERE id = $1`, cid).Scan(&onPay, &onInv); err != nil {
		return err
	}
	if (event == "payment_received" && !onPay) || (event == "invoice_issued" && !onInv) {
		return nil
	}
	if err := seedTemplates(ctx, tx, cid); err != nil {
		return err
	}
	r, err := newRenderer(ctx, tx, cid, "ar")
	if err != nil {
		return err
	}
	m, err := r.customerContext(ctx, tx, customerID, invoiceID, paymentID)
	if err != nil {
		return err
	}
	if normalizePhone(m.Phone, r.phoneCountry) == "" {
		return nil
	}
	tpl, err := pickTemplate(ctx, tx, cid, event, "", "ar")
	if err != nil {
		return nil // no template for this event: skip silently
	}
	r.locale = tpl.Locale
	m.Vars["company"] = r.company()
	prov, err := pickProvider(ctx, tx, cid, "")
	if err != nil {
		return err
	}
	_, _, _, err = queueMessage(ctx, tx, r, m, event, tpl, "", prov, "whatsapp")
	return err
}

// dmlJSONOne runs an INSERT/UPDATE ... RETURNING and returns the row as JSON (not_found when no row).
func dmlJSONOne(ctx context.Context, tx pgx.Tx, sql string, args ...any) (json.RawMessage, error) {
	var out []byte
	err := tx.QueryRow(ctx, `WITH q AS (`+sql+`) SELECT row_to_json(q) FROM q`, args...).Scan(&out)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	return out, err
}

// ---- API: providers ----

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT id::text, kind, name, config, is_default, is_active, created_at FROM message_providers
			WHERE company_id = $1 ORDER BY is_default DESC, created_at`, r.PathValue("cid"))
		if err != nil {
			return nil, err
		}
		type prov struct {
			ID        string         `json:"id"`
			Kind      string         `json:"kind"`
			Name      string         `json:"name"`
			Config    map[string]any `json:"config"`
			IsDefault bool           `json:"is_default"`
			IsActive  bool           `json:"is_active"`
			CreatedAt time.Time      `json:"created_at"`
		}
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (prov, error) {
			var p prov
			var cfg []byte
			err := row.Scan(&p.ID, &p.Kind, &p.Name, &cfg, &p.IsDefault, &p.IsActive, &p.CreatedAt)
			raw := map[string]any{}
			_ = json.Unmarshal(cfg, &raw)
			p.Config = maskConfig(raw)
			return p, err
		})
	})
}

type providerIn struct {
	Kind      *string        `json:"kind"`
	Name      *string        `json:"name"`
	Config    map[string]any `json:"config"`
	IsDefault *bool          `json:"is_default"`
	IsActive  *bool          `json:"is_active"`
}

func (s *Server) saveProvider(w http.ResponseWriter, r *http.Request) {
	var in providerIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Kind == nil || in.Name == nil)) || (in.Kind != nil && !providerKinds[*in.Kind]) ||
		(in.Name != nil && strings.TrimSpace(*in.Name) == "") || (!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if in.IsDefault != nil && *in.IsDefault {
			if _, err := tx.Exec(ctx, `UPDATE message_providers SET is_default = false WHERE company_id = $1 AND is_default AND id::text <> $2`, cid, id); err != nil {
				return nil, err
			}
		}
		if create {
			if in.Config == nil {
				in.Config = map[string]any{}
			}
			cfg, _ := json.Marshal(mergeConfig(map[string]any{}, in.Config))
			var n int
			_ = tx.QueryRow(ctx, `SELECT count(*) FROM message_providers WHERE company_id = $1`, cid).Scan(&n)
			def := n == 0 || (in.IsDefault != nil && *in.IsDefault)
			return dmlJSONOne(ctx, tx, `INSERT INTO message_providers (company_id, kind, name, config, is_default)
				VALUES ($1, $2, $3, $4, $5) RETURNING id, kind, name, is_default, is_active`, cid, *in.Kind, strings.TrimSpace(*in.Name), cfg, def)
		}
		var oldCfg []byte
		if err := tx.QueryRow(ctx, `SELECT config FROM message_providers WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&oldCfg); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		var cfg []byte
		if in.Config != nil {
			old := map[string]any{}
			_ = json.Unmarshal(oldCfg, &old)
			cfg, _ = json.Marshal(mergeConfig(old, in.Config))
		}
		return dmlJSONOne(ctx, tx, `UPDATE message_providers SET kind = coalesce($3, kind), name = coalesce(trim($4), name),
			config = coalesce($5::jsonb, config), is_default = coalesce($6, is_default), is_active = coalesce($7, is_active)
			WHERE id = $1 AND company_id = $2 RETURNING id, kind, name, is_default, is_active`,
			id, cid, in.Kind, in.Name, cfg, in.IsDefault, in.IsActive)
	})
}

// ---- API: templates ----

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if principal(r).Role == RoleAdmin || principal(r).Role == RoleAccountant {
			if err := seedTemplates(ctx, tx, cid); err != nil {
				return nil, err
			}
		}
		return queryJSON(ctx, tx, `SELECT id, event, locale, name, body, wa_template, is_active FROM message_templates
			WHERE company_id = $1 ORDER BY event, locale, name`, cid)
	})
}

type templateIn struct {
	Event      *string `json:"event"`
	Locale     *string `json:"locale"`
	Name       *string `json:"name"`
	Body       *string `json:"body"`
	WATemplate *string `json:"wa_template"`
	IsActive   *bool   `json:"is_active"`
}

var localeRe = regexp.MustCompile(`^[a-z]{2}$`)

func (s *Server) saveTemplate(w http.ResponseWriter, r *http.Request) {
	var in templateIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Event == nil || in.Name == nil || in.Body == nil)) || (in.Event != nil && !msgEvents[*in.Event]) ||
		(in.Locale != nil && !localeRe.MatchString(*in.Locale)) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") ||
		(in.Body != nil && strings.TrimSpace(*in.Body) == "") || (!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	cols := `id, event, locale, name, body, wa_template, is_active`
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if create {
			if _, err := companyBase(ctx, tx, cid); err != nil {
				return nil, err
			}
			loc := "ar"
			if in.Locale != nil {
				loc = *in.Locale
			}
			return dmlJSONOne(ctx, tx, `INSERT INTO message_templates (company_id, event, locale, name, body, wa_template)
				VALUES ($1, $2, $3, $4, $5, coalesce($6, '')) RETURNING `+cols, cid, *in.Event, loc, strings.TrimSpace(*in.Name),
				strings.TrimSpace(*in.Body), in.WATemplate)
		}
		return dmlJSONOne(ctx, tx, `UPDATE message_templates SET event = coalesce($3, event), locale = coalesce($4, locale),
			name = coalesce(trim($5), name), body = coalesce(trim($6), body), wa_template = coalesce(trim($7), wa_template),
			is_active = coalesce($8, is_active) WHERE id = $1 AND company_id = $2 RETURNING `+cols,
			id, cid, in.Event, in.Locale, in.Name, in.Body, in.WATemplate, in.IsActive)
	})
}

// ---- API: settings ----

func (s *Server) getMessageSettings(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT auto_send_on_payment, auto_send_on_invoice, phone_country FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}

func (s *Server) putMessageSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OnPayment    *bool   `json:"auto_send_on_payment"`
		OnInvoice    *bool   `json:"auto_send_on_invoice"`
		PhoneCountry *string `json:"phone_country"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.PhoneCountry != nil && !regexp.MustCompile(`^[1-9][0-9]{0,3}$`).MatchString(*in.PhoneCountry) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return dmlJSONOne(ctx, tx, `UPDATE companies SET auto_send_on_payment = coalesce($2, auto_send_on_payment),
			auto_send_on_invoice = coalesce($3, auto_send_on_invoice), phone_country = coalesce($4, phone_country)
			WHERE id = $1 RETURNING auto_send_on_payment, auto_send_on_invoice, phone_country`, r.PathValue("cid"), in.OnPayment, in.OnInvoice, in.PhoneCountry)
	})
}

// ---- API: send ----

type sendIn struct {
	CustomerIDs []string `json:"customer_ids"`
	InvoiceIDs  []string `json:"invoice_ids"`
	PaymentIDs  []string `json:"payment_ids"`
	Unpaid      *struct {
		AreaID      string `json:"area_id"`
		MinDaysLate int    `json:"min_days_late"`
	} `json:"unpaid"`
	Event      string `json:"event"`
	TemplateID string `json:"template_id"`
	ProviderID string `json:"provider_id"`
	Channel    string `json:"channel"`
	Locale     string `json:"locale"`
	Body       string `json:"body"` // for event "custom" without template
	DryRun     bool   `json:"dry_run"`
}

func (s *Server) sendMessages(w http.ResponseWriter, r *http.Request) {
	var in sendIn
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Event == "" {
		in.Event = "reminder"
	}
	if !localeRe.MatchString(in.Locale) {
		in.Locale = "ar"
	}
	all := append(append(append([]string{}, in.CustomerIDs...), in.InvoiceIDs...), in.PaymentIDs...)
	bad := !msgEvents[in.Event] || (in.TemplateID != "" && !isUUID(in.TemplateID)) || (in.ProviderID != "" && !isUUID(in.ProviderID)) ||
		(in.Event == "custom" && in.TemplateID == "" && strings.TrimSpace(in.Body) == "") || len(all) > 20000 ||
		(in.Unpaid != nil && in.Unpaid.AreaID != "" && !isUUID(in.Unpaid.AreaID))
	for _, id := range all {
		bad = bad || !isUUID(id)
	}
	if bad || (len(all) == 0 && in.Unpaid == nil) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := seedTemplates(ctx, tx, cid); err != nil {
			return nil, err
		}
		rd, err := newRenderer(ctx, tx, cid, in.Locale)
		if err != nil {
			return nil, err
		}
		var tpl *tplRow
		if in.TemplateID != "" || in.Event != "custom" {
			if tpl, err = pickTemplate(ctx, tx, cid, in.Event, in.TemplateID, in.Locale); err != nil {
				return nil, err
			}
			rd.locale = tpl.Locale
		}
		prov, err := pickProvider(ctx, tx, cid, in.ProviderID)
		if err != nil {
			return nil, err
		}

		type target struct{ customer, invoice, payment string }
		var targets []target
		for _, id := range in.CustomerIDs {
			targets = append(targets, target{customer: id})
		}
		for _, id := range in.InvoiceIDs {
			var c string
			if err := tx.QueryRow(ctx, `SELECT customer_id::text FROM invoices WHERE id = $1 AND company_id = $2`, id, cid).Scan(&c); err == nil {
				targets = append(targets, target{customer: c, invoice: id})
			}
		}
		for _, id := range in.PaymentIDs {
			var c string
			if err := tx.QueryRow(ctx, `SELECT customer_id::text FROM payments WHERE id = $1 AND company_id = $2`, id, cid).Scan(&c); err == nil {
				targets = append(targets, target{customer: c, payment: id})
			}
		}
		if in.Unpaid != nil {
			rows, err := tx.Query(ctx, `SELECT DISTINCT c.id::text FROM customers c JOIN invoices i ON i.customer_id = c.id
				WHERE c.company_id = $1 AND c.status <> 'closed' AND i.status IN ('open', 'partial')
				  AND current_date - i.due_date >= $2 AND ($3 = '' OR c.area_id = $3::uuid)`, cid, in.Unpaid.MinDaysLate, in.Unpaid.AreaID)
			if err != nil {
				return nil, err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				targets = append(targets, target{customer: id})
			}
		}

		type skip struct {
			Customer string `json:"customer"`
			Reason   string `json:"reason"`
		}
		type preview struct {
			Customer string `json:"customer"`
			Phone    string `json:"phone"`
			Text     string `json:"text"`
		}
		queued := 0
		skips := []skip{}
		previews := []preview{}
		seen := map[string]bool{}
		for _, tg := range targets {
			key := tg.customer + tg.invoice + tg.payment
			if seen[key] {
				continue
			}
			seen[key] = true
			var inv, pay *string
			if tg.invoice != "" {
				inv = &tg.invoice
			}
			if tg.payment != "" {
				pay = &tg.payment
			}
			m, err := rd.customerContext(ctx, tx, tg.customer, inv, pay)
			if err != nil {
				return nil, err
			}
			phone := normalizePhone(m.Phone, rd.phoneCountry)
			if phone == "" {
				skips = append(skips, skip{m.Name, "no_phone"})
				continue
			}
			if in.DryRun {
				body := in.Body
				if tpl != nil {
					body = tpl.Body
				}
				text, _ := render(body, m.Vars)
				if len(previews) < 5 {
					previews = append(previews, preview{m.Name, phone, text})
				}
				queued++
				continue
			}
			ok, reason, text, err := queueMessage(ctx, tx, rd, m, in.Event, tpl, in.Body, prov, in.Channel)
			if err != nil {
				return nil, err
			}
			if !ok {
				skips = append(skips, skip{m.Name, reason})
				continue
			}
			if len(previews) < 5 {
				previews = append(previews, preview{m.Name, phone, text})
			}
			queued++
		}
		return map[string]any{"queued": queued, "skipped": skips, "preview": previews, "dry_run": in.DryRun, "has_provider": prov != nil}, nil
	})
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, customer := q.Get("status"), q.Get("customer_id")
	if customer != "" && !isUUID(customer) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	limit, offset := queryInt(r, "limit", 200, 1000), queryInt(r, "offset", 0, 1<<30)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT m.id, m.customer_id, c.name AS customer_name, m.phone, m.channel, m.event, m.body,
			m.status, m.error, m.attempts, m.created_at, m.sent_at, p.name AS provider_name, u.full_name AS created_by
			FROM messages m LEFT JOIN customers c ON c.id = m.customer_id LEFT JOIN message_providers p ON p.id = m.provider_id
			LEFT JOIN users u ON u.id = m.created_by
			WHERE m.company_id = $1 AND ($2 = '' OR m.status = $2) AND ($3 = '' OR m.customer_id = $3::uuid)
			ORDER BY m.created_at DESC LIMIT $4 OFFSET $5`, r.PathValue("cid"), status, customer, limit, offset)
		if err != nil {
			return nil, err
		}
		counts, err := queryJSONOne(ctx, tx, `SELECT count(*) FILTER (WHERE status = 'queued') AS queued,
			count(*) FILTER (WHERE status = 'sending') AS sending, count(*) FILTER (WHERE status = 'sent') AS sent,
			count(*) FILTER (WHERE status = 'failed') AS failed FROM messages WHERE company_id = $1`, r.PathValue("cid"))
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows, "counts": counts}, nil
	})
}

func (s *Server) retryMessage(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		prov, err := pickProvider(ctx, tx, cid, "")
		if err != nil {
			return nil, err
		}
		var provID *string
		if prov != nil {
			provID = &prov.ID
		}
		return dmlJSONOne(ctx, tx, `UPDATE messages SET status = 'queued', attempts = 0, error = '', next_attempt_at = now(),
			provider_id = coalesce(provider_id, $3) WHERE id = $1 AND company_id = $2 AND status IN ('failed', 'queued')
			RETURNING id, status`, id, cid, provID)
	})
}

// messageLink renders a message and returns a wa.me link (works without any provider configured).
func (s *Server) messageLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CustomerID string `json:"customer_id"`
		InvoiceID  string `json:"invoice_id"`
		PaymentID  string `json:"payment_id"`
		Event      string `json:"event"`
		Locale     string `json:"locale"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Event == "" {
		in.Event = "reminder"
	}
	if !localeRe.MatchString(in.Locale) {
		in.Locale = "ar"
	}
	if !isUUID(in.CustomerID) || !msgEvents[in.Event] || in.Event == "custom" || (in.InvoiceID != "" && !isUUID(in.InvoiceID)) ||
		(in.PaymentID != "" && !isUUID(in.PaymentID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := seedTemplates(ctx, tx, cid); err != nil {
			return nil, err
		}
		rd, err := newRenderer(ctx, tx, cid, in.Locale)
		if err != nil {
			return nil, err
		}
		tpl, err := pickTemplate(ctx, tx, cid, in.Event, "", in.Locale)
		if err != nil {
			return nil, err
		}
		rd.locale = tpl.Locale
		var inv, pay *string
		if in.InvoiceID != "" {
			inv = &in.InvoiceID
		}
		if in.PaymentID != "" {
			pay = &in.PaymentID
		}
		m, err := rd.customerContext(ctx, tx, in.CustomerID, inv, pay)
		if err != nil {
			return nil, err
		}
		text, _ := render(tpl.Body, m.Vars)
		phone := normalizePhone(m.Phone, rd.phoneCountry)
		link := "https://wa.me/"
		if phone != "" {
			link += strings.TrimPrefix(phone, "+")
		}
		link += "?text=" + url.QueryEscape(text)
		return map[string]string{"phone": phone, "text": text, "wa_link": link}, nil
	})
}
