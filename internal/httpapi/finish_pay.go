package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Online payments on the customer portal. A gateway is one way to pay, per company:
//   - stripe: card payment on a Stripe Checkout page (secret key + webhook signing secret);
//   - hosted: any bank / processor hosted payment page (Areeba, MEPS-style 3-D Secure pages…): the customer is sent to
//     checkout_url with a signed order, and the processor calls our webhook with a signed result (HMAC-SHA256 with the
//     shared secret);
//   - transfer: Whish / OMT / bank transfer: the customer types the transfer reference, staff confirm it.
// A payment intent is created per attempt; when it is paid the normal customer payment is booked into the gateway's
// cash box / bank account and applied to the invoice. Posting is idempotent (one payment per intent). In test mode,
// without real merchant credentials, a built-in simulator stands in for the processor page.

var gatewaySecretKeys = map[string]bool{"secret_key": true, "webhook_secret": true, "shared_secret": true}

type gatewayIn struct {
	Kind         string            `json:"kind"`
	Name         string            `json:"name"`
	MethodID     string            `json:"method_id"`
	TestMode     *bool             `json:"test_mode"`
	Instructions string            `json:"instructions"`
	IsActive     *bool             `json:"is_active"`
	Config       map[string]string `json:"config"`
}

var gatewayConfigKeys = map[string][]string{
	"stripe":   {"secret_key", "webhook_secret", "publishable_key"},
	"hosted":   {"checkout_url", "merchant_id", "shared_secret", "provider"},
	"transfer": {"account_name", "account_number", "phone"},
}

// gatewayJSON hides the secrets (only whether they are set).
func gatewayJSON(id, kind, name, method string, test bool, instr string, active bool, cfg map[string]any) map[string]any {
	pub := map[string]any{}
	for k, v := range cfg {
		if gatewaySecretKeys[k] {
			pub["has_"+k] = v != nil && v != ""
			continue
		}
		pub[k] = v
	}
	return map[string]any{"id": id, "kind": kind, "name": name, "method_id": method, "test_mode": test, "instructions": instr, "is_active": active,
		"config": pub, "webhook_url": publicURL() + "/api/v1/pay/webhook/" + id}
}

func (s *Server) listGateways(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT id::text, kind, name, method_id::text, test_mode, instructions, is_active, config FROM payment_gateways
			WHERE company_id = $1 ORDER BY name`, r.PathValue("cid"))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, kind, name, method, instr string
			var test, active bool
			var cfg map[string]any
			if err := rows.Scan(&id, &kind, &name, &method, &test, &instr, &active, &cfg); err != nil {
				return nil, err
			}
			out = append(out, gatewayJSON(id, kind, name, method, test, instr, active, cfg))
		}
		return out, rows.Err()
	})
}

// POST /companies/{cid}/payment-gateways · PUT /companies/{cid}/payment-gateways/{id} (a secret left empty keeps the old one)
func (s *Server) saveGateway(w http.ResponseWriter, r *http.Request) {
	var in gatewayIn
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	keys, ok := gatewayConfigKeys[in.Kind]
	if !ok || strings.TrimSpace(in.Name) == "" || !isUUID(in.MethodID) || (id != "" && !isUUID(id)) || len(in.Instructions) > 2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	allowed := map[string]bool{}
	for _, k := range keys {
		allowed[k] = true
	}
	for k, v := range in.Config {
		if !allowed[k] || len(v) > 500 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	if u := in.Config["checkout_url"]; u != "" {
		if p, err := url.Parse(u); err != nil || p.Scheme != "https" {
			writeErr(w, http.StatusBadRequest, "invalid_url")
			return
		}
	}
	test := in.TestMode == nil || *in.TestMode
	active := in.IsActive == nil || *in.IsActive
	cid := r.PathValue("cid")
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		methods, err := loadMethods(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if m, ok := methods[in.MethodID]; !ok || !m.Active {
			return nil, badRequest("invalid_method")
		}
		cfg := map[string]any{}
		if id != "" {
			var old map[string]any
			err := tx.QueryRow(ctx, `SELECT config FROM payment_gateways WHERE id = $1 AND company_id = $2`, id, cid).Scan(&old)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			if err != nil {
				return nil, err
			}
			for k, v := range old {
				cfg[k] = v
			}
		}
		for k, v := range in.Config {
			v = strings.TrimSpace(v)
			if gatewaySecretKeys[k] {
				if v == "" {
					continue // keep the stored secret
				}
				sealed, err := sealSecret(v)
				if err != nil {
					return nil, err
				}
				cfg[k] = sealed
				continue
			}
			cfg[k] = v
		}
		b, _ := json.Marshal(cfg)
		if id == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO payment_gateways (company_id, kind, name, method_id, test_mode, config, instructions, is_active)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`, cid, in.Kind, strings.TrimSpace(in.Name), in.MethodID, test, b,
				strings.TrimSpace(in.Instructions), active).Scan(&id); err != nil {
				return nil, err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE payment_gateways SET kind = $3, name = $4, method_id = $5, test_mode = $6, config = $7, instructions = $8,
			is_active = $9 WHERE id = $1 AND company_id = $2`, id, cid, in.Kind, strings.TrimSpace(in.Name), in.MethodID, test, b,
			strings.TrimSpace(in.Instructions), active); err != nil {
			return nil, err
		}
		return gatewayJSON(id, in.Kind, strings.TrimSpace(in.Name), in.MethodID, test, strings.TrimSpace(in.Instructions), active, cfg), nil
	})
}

const intentCols = `pi.id, pi.gateway_id, g.name AS gateway_name, g.kind AS gateway_kind, pi.customer_id, c.name AS customer_name, c.code AS customer_code,
	pi.invoice_id, (SELECT number FROM invoices WHERE id = pi.invoice_id) AS invoice_number, pi.currency, pi.amount::text AS amount, pi.status,
	pi.provider_ref, pi.reference, pi.payer_phone, pi.payment_id, (SELECT number FROM payments WHERE id = pi.payment_id) AS payment_number,
	pi.error, pi.created_at, pi.paid_at, g.test_mode`

// GET /companies/{cid}/payment-intents?status=
func (s *Server) listIntents(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+intentCols+` FROM payment_intents pi JOIN payment_gateways g ON g.id = pi.gateway_id
			JOIN customers c ON c.id = pi.customer_id WHERE pi.company_id = $1 AND ($2 = '' OR pi.status = $2) ORDER BY pi.created_at DESC LIMIT 1000`,
			r.PathValue("cid"), st)
	})
}

// markIntentPaid books the customer payment of an intent once (idempotent: a second call returns the same payment).
func markIntentPaid(ctx context.Context, tx pgx.Tx, cid, intentID, providerRef string, amount *decimal.Decimal) (string, error) {
	var status, customer, cur, amt, method, token string
	var inv, payment *string
	err := tx.QueryRow(ctx, `SELECT pi.status, pi.customer_id::text, pi.currency, pi.amount::text, g.method_id::text, pi.token, pi.invoice_id::text, pi.payment_id::text
		FROM payment_intents pi JOIN payment_gateways g ON g.id = pi.gateway_id WHERE pi.id = $1 AND pi.company_id = $2 FOR UPDATE OF pi`, intentID, cid).
		Scan(&status, &customer, &cur, &amt, &method, &token, &inv, &payment)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notFound()
	}
	if err != nil {
		return "", err
	}
	if status == "paid" && payment != nil {
		return *payment, nil
	}
	if status != "pending" && status != "review" && status != "failed" {
		return "", &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	paid, _ := decimal.NewFromString(amt)
	if amount != nil && amount.IsPositive() {
		paid = *amount // what the processor reports as captured
	}
	in := paymentIn{CustomerID: customer, Date: today(), Currency: cur, Tenders: []tenderIn{{MethodID: method, Amount: paid.String()}},
		Notes: "Online payment " + providerRef, ClientRef: clientRefFor(token)}
	if inv != nil {
		in.InvoiceID = *inv
	}
	pid, err := receivePayment(ctx, tx, cid, in, false, "")
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE payment_intents SET status = 'paid', payment_id = $2, provider_ref = CASE WHEN $3 = '' THEN provider_ref ELSE $3 END,
		paid_at = now(), decided_by = app_user(), error = '' WHERE id = $1`, intentID, pid, providerRef); err != nil {
		return "", err
	}
	return pid, nil
}

// POST /companies/{cid}/payment-intents/{id}/confirm (a transfer reference checked by staff) · /reject {reason}
func (s *Server) decideIntent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if !isUUID(id) || (action != "confirm" && action != "reject") {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if action == "confirm" {
			if _, err := markIntentPaid(ctx, tx, cid, id, "", nil); err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE payment_intents SET status = 'rejected', error = $3, decided_by = app_user() WHERE id = $1 AND company_id = $2
				AND status IN ('pending', 'review')`, id, cid, strings.TrimSpace(in.Reason))
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+intentCols+` FROM payment_intents pi JOIN payment_gateways g ON g.id = pi.gateway_id
			JOIN customers c ON c.id = pi.customer_id WHERE pi.id = $1`, id)
	})
}

// ---------------- customer side (portal link) ----------------

// GET /api/v1/portal/{token}/pay-options
func (s *Server) portalPayOptions(w http.ResponseWriter, r *http.Request) {
	sc, cid, _, ok := s.portalScope(w, r)
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT g.id, g.kind, g.name, g.instructions, g.test_mode, m.currency, g.config ->> 'account_name' AS account_name,
			g.config ->> 'account_number' AS account_number, g.config ->> 'phone' AS phone
			FROM payment_gateways g JOIN payment_methods m ON m.id = g.method_id WHERE g.company_id = $1 AND g.is_active ORDER BY g.name`, cid)
	})
}

// POST /api/v1/portal/{token}/invoices/{id}/pay {gateway_id, reference, phone}: starts paying the invoice's balance.
// Answers {intent, redirect_url} (card / hosted page, or the simulator in test mode) or {intent, status: review}.
func (s *Server) portalPay(w http.ResponseWriter, r *http.Request) {
	sc, cid, customer, ok := s.portalScope(w, r)
	if !ok {
		return
	}
	var in struct {
		GatewayID string `json:"gateway_id"`
		Reference string `json:"reference"`
		Phone     string `json:"phone"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	invID := r.PathValue("id")
	if !isUUID(invID) || !isUUID(in.GatewayID) || len(in.Reference) > 100 || len(in.Phone) > 30 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	portalTok := r.PathValue("token")
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var cur, bal string
		var number int64
		err := tx.QueryRow(ctx, `SELECT currency, (total - paid)::text, number FROM invoices WHERE id = $1 AND customer_id = $2 AND status IN ('open', 'partial')`,
			invID, customer).Scan(&cur, &bal, &number)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		var kind, name, mcur, company string
		var test bool
		var cfg map[string]any
		err = tx.QueryRow(ctx, `SELECT g.kind, g.name, m.currency, g.test_mode, g.config, co.name_en FROM payment_gateways g JOIN payment_methods m ON m.id = g.method_id
			JOIN companies co ON co.id = g.company_id WHERE g.id = $1 AND g.company_id = $2 AND g.is_active`, in.GatewayID, cid).Scan(&kind, &name, &mcur, &test, &cfg, &company)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, badRequest("invalid_gateway")
		}
		if err != nil {
			return nil, err
		}
		if mcur != cur {
			return nil, badRequest("currency_not_supported")
		}
		amount, _ := decimal.NewFromString(bal)
		if !amount.IsPositive() {
			return nil, badRequest("nothing_to_pay")
		}
		if kind == "transfer" && strings.TrimSpace(in.Reference) == "" {
			return nil, badRequest("reference_required")
		}
		token := randToken(32)
		status := "pending"
		if kind == "transfer" {
			status = "review"
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO payment_intents (company_id, gateway_id, customer_id, invoice_id, currency, amount, status, reference, payer_phone, token)
			VALUES ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9, $10) RETURNING id::text`, cid, in.GatewayID, customer, invID, cur, amount.String(), status,
			strings.TrimSpace(in.Reference), strings.TrimSpace(in.Phone), token).Scan(&id); err != nil {
			return nil, err
		}
		out := map[string]any{"intent": id, "status": status, "amount": amount.String(), "currency": cur}
		if kind == "transfer" {
			return out, nil
		}
		back := portalLink(portalTok)
		sim := publicURL() + "/#/pay-sim/" + token
		switch kind {
		case "stripe":
			secret := openSecret(str2(cfg["secret_key"]))
			if secret == "" {
				if !test {
					return nil, notSetup("gateway")
				}
				out["redirect_url"] = sim
				return out, nil
			}
			dec, err := currencyDecimals(ctx, tx, cur)
			if err != nil {
				return nil, err
			}
			minor := amount.Shift(dec).Round(0).IntPart()
			form := url.Values{}
			form.Set("mode", "payment")
			form.Set("success_url", back+"?paid=1")
			form.Set("cancel_url", back)
			form.Set("client_reference_id", token)
			form.Set("metadata[intent]", token)
			form.Set("line_items[0][quantity]", "1")
			form.Set("line_items[0][price_data][currency]", strings.ToLower(cur))
			form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(minor, 10))
			form.Set("line_items[0][price_data][product_data][name]", fmt.Sprintf("%s — invoice #%d", company, number))
			sess, err := stripeCreateSession(ctx, secret, form)
			if err != nil {
				_, _ = tx.Exec(ctx, `UPDATE payment_intents SET status = 'failed', error = $2 WHERE id = $1`, id, err.Error())
				return nil, &apiError{status: http.StatusBadGateway, code: "gateway_error", detail: err.Error()}
			}
			if _, err := tx.Exec(ctx, `UPDATE payment_intents SET provider_ref = $2 WHERE id = $1`, id, sess.ID); err != nil {
				return nil, err
			}
			out["redirect_url"] = sess.URL
		case "hosted":
			checkout, merchant := str2(cfg["checkout_url"]), str2(cfg["merchant_id"])
			secret := openSecret(str2(cfg["shared_secret"]))
			if checkout == "" || secret == "" {
				if !test {
					return nil, notSetup("gateway")
				}
				out["redirect_url"] = sim
				return out, nil
			}
			q := url.Values{}
			q.Set("merchant", merchant)
			q.Set("order", token)
			q.Set("amount", amount.String())
			q.Set("currency", cur)
			q.Set("description", fmt.Sprintf("%s invoice #%d", company, number))
			q.Set("return_url", back)
			q.Set("callback_url", publicURL()+"/api/v1/pay/webhook/"+in.GatewayID)
			q.Set("signature", hostedSign(secret, merchant, token, amount.String(), cur))
			sep := "?"
			if strings.Contains(checkout, "?") {
				sep = "&"
			}
			out["redirect_url"] = checkout + sep + q.Encode()
		}
		_ = name
		return out, nil
	})
}

func str2(v any) string {
	s, _ := v.(string)
	return s
}

// hostedSign: hex HMAC-SHA256 over the pipe-joined fields (the processor signs its callback the same way).
func hostedSign(secret string, parts ...string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(m.Sum(nil))
}

type stripeSession struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

var stripeAPI = "https://api.stripe.com/v1"

func stripeCreateSession(ctx context.Context, secret string, form url.Values) (*stripeSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, stripeAPI+"/checkout/sessions", strings.NewReader(form.Encode()))
	req.SetBasicAuth(secret, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return nil, fmt.Errorf("stripe %d: %s", res.StatusCode, e.Error.Message)
	}
	var s stripeSession
	if err := json.Unmarshal(body, &s); err != nil || s.URL == "" {
		return nil, errors.New("stripe: no checkout url")
	}
	return &s, nil
}

// verifyStripeSignature checks the Stripe-Signature header (t=…,v1=…) against the endpoint secret, 5 minutes tolerance.
func verifyStripeSignature(header string, body []byte, secret string, now time.Time) bool {
	var ts string
	var sigs []string
	for _, p := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 || now.Sub(time.Unix(t, 0)).Abs() > 5*time.Minute {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	want := hex.EncodeToString(m.Sum(nil))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return true
		}
	}
	return false
}

// gatewayScope finds a gateway by id in any tenant (webhooks are not signed in).
func (s *Server) gatewayScope(ctx context.Context, gid string) (db.Scope, string, map[string]any, bool, bool) {
	if !isUUID(gid) || s.OwnerPool == nil {
		return db.Scope{}, "", nil, false, false
	}
	var tid, cid, kind string
	var cfg map[string]any
	var test bool
	if err := s.OwnerPool.QueryRow(ctx, `SELECT tenant_id::text, company_id::text, kind, config, test_mode FROM payment_gateways WHERE id = $1 AND is_active`, gid).
		Scan(&tid, &cid, &kind, &cfg, &test); err != nil {
		return db.Scope{}, "", nil, false, false
	}
	cfg["_kind"] = kind
	return db.Scope{TenantID: tid, CompanyID: cid}, cid, cfg, test, true
}

// POST /api/v1/pay/webhook/{gid}: the processor tells us the result (signed).
func (s *Server) payWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	sc, cid, cfg, _, ok := s.gatewayScope(r.Context(), r.PathValue("gid"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	sc.IP = clientIP(r)
	var token, ref, result string
	var amount *decimal.Decimal
	switch cfg["_kind"] {
	case "stripe":
		secret := openSecret(str2(cfg["webhook_secret"]))
		if secret == "" || !verifyStripeSignature(r.Header.Get("Stripe-Signature"), body, secret, time.Now()) {
			writeErr(w, http.StatusUnauthorized, "bad_signature")
			return
		}
		var ev struct {
			Type string `json:"type"`
			Data struct {
				Object struct {
					ID                string `json:"id"`
					ClientReferenceID string `json:"client_reference_id"`
					PaymentStatus     string `json:"payment_status"`
				} `json:"object"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &ev) != nil {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		switch {
		case ev.Type == "checkout.session.completed" && ev.Data.Object.PaymentStatus == "paid",
			ev.Type == "checkout.session.async_payment_succeeded":
			result = "paid"
		case ev.Type == "checkout.session.async_payment_failed", ev.Type == "checkout.session.expired":
			result = "failed"
		default:
			writeJSON(w, http.StatusOK, map[string]string{"ignored": ev.Type})
			return
		}
		token, ref = ev.Data.Object.ClientReferenceID, ev.Data.Object.ID
	case "hosted":
		secret := openSecret(str2(cfg["shared_secret"]))
		var f struct {
			Order     string `json:"order"`
			Status    string `json:"status"`
			Amount    string `json:"amount"`
			Currency  string `json:"currency"`
			Ref       string `json:"ref"`
			Signature string `json:"signature"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if json.Unmarshal(body, &f) != nil {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
		} else {
			v, _ := url.ParseQuery(string(body))
			f.Order, f.Status, f.Amount, f.Currency, f.Ref, f.Signature = v.Get("order"), v.Get("status"), v.Get("amount"), v.Get("currency"), v.Get("ref"), v.Get("signature")
		}
		want := hostedSign(secret, f.Order, f.Status, f.Amount, f.Currency, f.Ref)
		if secret == "" || !hmac.Equal([]byte(strings.ToLower(f.Signature)), []byte(want)) {
			writeErr(w, http.StatusUnauthorized, "bad_signature")
			return
		}
		token, ref = f.Order, f.Ref
		if a, ok := money(f.Amount); ok {
			amount = &a
		}
		result = map[bool]string{true: "paid", false: "failed"}[strings.EqualFold(f.Status, "paid") || strings.EqualFold(f.Status, "approved") || f.Status == "00"]
	default:
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if err := s.applyIntentResult(r.Context(), sc, cid, token, result, ref, amount); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": result})
}

func (s *Server) applyIntentResult(ctx context.Context, sc db.Scope, cid, token, result, ref string, amount *decimal.Decimal) error {
	return db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		var id, cur, amt string
		err := tx.QueryRow(ctx, `SELECT id::text, currency, amount::text FROM payment_intents WHERE token = $1 AND company_id = $2`, token, cid).Scan(&id, &cur, &amt)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if result != "paid" {
			_, err := tx.Exec(ctx, `UPDATE payment_intents SET status = 'failed', provider_ref = $2 WHERE id = $1 AND status = 'pending'`, id, ref)
			return err
		}
		if amount != nil {
			want, _ := decimal.NewFromString(amt)
			if amount.GreaterThan(want) {
				amount = &want
			}
		}
		_, err = markIntentPaid(ctx, tx, cid, id, ref, amount)
		_ = cur
		return err
	})
}

// GET /api/v1/pay/sim/{token}: the test-mode processor page (only for gateways in test mode).
func (s *Server) paySimInfo(w http.ResponseWriter, r *http.Request) {
	sc, cid, ok := s.simScope(w, r)
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT pi.amount::text AS amount, pi.currency, pi.status, g.name AS gateway, g.kind, co.name_en AS company,
			(SELECT number FROM invoices WHERE id = pi.invoice_id) AS invoice_number
			FROM payment_intents pi JOIN payment_gateways g ON g.id = pi.gateway_id JOIN companies co ON co.id = pi.company_id
			WHERE pi.token = $1 AND pi.company_id = $2`, r.PathValue("token"), cid)
	})
}

// POST /api/v1/pay/sim/{token} {result: paid|failed}
func (s *Server) paySimComplete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Result string `json:"result"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Result != "paid" && in.Result != "failed" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	sc, cid, ok := s.simScope(w, r)
	if !ok {
		return
	}
	if err := s.applyIntentResult(r.Context(), sc, cid, r.PathValue("token"), in.Result, "SIM-"+randHexStr(6), nil); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": in.Result})
}

func (s *Server) simScope(w http.ResponseWriter, r *http.Request) (db.Scope, string, bool) {
	if !s.portalLimiter.allow(clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return db.Scope{}, "", false
	}
	s.portalLimiter.fail(clientIP(r))
	tok := r.PathValue("token")
	if len(tok) < 20 || len(tok) > 128 || s.OwnerPool == nil {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", false
	}
	var tid, cid string
	var test bool
	err := s.OwnerPool.QueryRow(r.Context(), `SELECT pi.tenant_id::text, pi.company_id::text, g.test_mode FROM payment_intents pi
		JOIN payment_gateways g ON g.id = pi.gateway_id WHERE pi.token = $1`, tok).Scan(&tid, &cid, &test)
	if err != nil || !test {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", false
	}
	return db.Scope{TenantID: tid, CompanyID: cid, IP: clientIP(r)}, cid, true
}

func randHexStr(n int) string {
	t := randToken(n)
	if len(t) > n*2 {
		t = t[:n*2]
	}
	return strings.ToUpper(t)
}

func init() {
	if v := strings.TrimSpace(os.Getenv("STRIPE_API_BASE")); v != "" {
		stripeAPI = v // tests point this at a local fake
	}
}

func clientRefFor(token string) string {
	ref := "pi-" + token
	if len(ref) > 64 {
		ref = ref[:64]
	}
	return ref
}
