package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Phase 4 (growth and sales) shared pieces: the public address of a company (shop + careers), the rate limits of
// the public pages, the outbound message helper used by the shop, campaigns and e-sign, staff notifications, and
// the background worker (scheduled campaigns, signature reminders and expiry).

var (
	publicReadLimiter  = newLimiter(600, 10*time.Minute) // page views per IP
	publicWriteLimiter = newLimiter(30, time.Hour)       // orders / applications / signatures per IP
)

// publicScope resolves /{slug} to the company (404 when unknown, shop and careers flags for the caller).
func (s *Server) publicScope(w http.ResponseWriter, r *http.Request, write bool) (db.Scope, string, bool, bool, bool) {
	ip := clientIP(r)
	lim := publicReadLimiter
	if write {
		lim = publicWriteLimiter
	}
	if !lim.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return db.Scope{}, "", false, false, false
	}
	lim.fail(ip)
	slug := strings.ToLower(r.PathValue("slug"))
	if len(slug) < 3 || len(slug) > 41 {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", false, false, false
	}
	var tid, cid string
	var shop, careers bool
	if err := s.Pool.QueryRow(r.Context(), `SELECT tenant_id::text, company_id::text, shop_on, careers_on FROM public_lookup($1)`, slug).
		Scan(&tid, &cid, &shop, &careers); err != nil {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", false, false, false
	}
	return db.Scope{TenantID: tid, CompanyID: cid, IP: ip}, cid, shop, careers, true
}

// tokenScope resolves a public token through one of the lookup functions (rate limited per IP).
func (s *Server) tokenScope(w http.ResponseWriter, r *http.Request, fn string) (db.Scope, string, string, string, bool) {
	ip := clientIP(r)
	if !publicReadLimiter.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return db.Scope{}, "", "", "", false
	}
	publicReadLimiter.fail(ip)
	tok := r.PathValue("token")
	if len(tok) < 32 || len(tok) > 128 {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", "", "", false
	}
	var tid, cid, a, b string
	var err error
	switch fn {
	case "shop_order":
		err = s.Pool.QueryRow(r.Context(), `SELECT tenant_id::text, company_id::text, order_id::text, '' FROM shop_order_lookup($1)`, tok).Scan(&tid, &cid, &a, &b)
	case "sign":
		err = s.Pool.QueryRow(r.Context(), `SELECT tenant_id::text, company_id::text, signer_id::text, request_id::text FROM sign_lookup($1)`, tok).Scan(&tid, &cid, &a, &b)
	case "mkt":
		err = s.Pool.QueryRow(r.Context(), `SELECT tenant_id::text, company_id::text, recipient_id::text, '' FROM mkt_lookup($1)`, tok).Scan(&tid, &cid, &a, &b)
	default:
		err = errors.New("unknown lookup")
	}
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found")
		return db.Scope{}, "", "", "", false
	}
	return db.Scope{TenantID: tid, CompanyID: cid, IP: ip}, cid, a, b, true
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,40}$`)

// dbInScope runs fn in a transaction of the given (public) scope.
func dbInScope(ctx context.Context, s *Server, sc db.Scope, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error { return fn(ctx, tx) })
}

// growthTexts: the automatic messages of phase 4 in the company's language ({1}, {2}… are the arguments).
var growthTexts = map[string]map[string]string{
	"shop_order": {
		"ar": "{1}: شكراً {2}، استلمنا طلبك رقم {3} ({4}). تابع طلبك من هنا: {5}",
		"en": "{1}: thank you {2}, we received your order #{3} ({4}). Follow it here: {5}",
		"fr": "{1} : merci {2}, nous avons reçu votre commande n°{3} ({4}). Suivez-la ici : {5}",
	},
	"sign_request": {
		"ar": "{1}: مرحباً {2}، نرجو توقيع المستند «{3}». افتح الرابط ووقّع من هاتفك: {4}",
		"en": "{1}: hello {2}, please sign «{3}». Open the link and sign from your phone: {4}",
		"fr": "{1} : bonjour {2}, merci de signer « {3} ». Ouvrez le lien et signez depuis votre téléphone : {4}",
	},
	"sign_reminder": {
		"ar": "{1}: تذكير — المستند «{3}» ما زال بانتظار توقيعك يا {2}: {4}",
		"en": "{1}: reminder — «{3}» is still waiting for your signature, {2}: {4}",
		"fr": "{1} : rappel — « {3} » attend toujours votre signature, {2} : {4}",
	},
	"sign_done": {
		"ar": "{1}: تم توقيع «{3}» من جميع الأطراف. شكراً {2}. نسخة المستند: {4}",
		"en": "{1}: «{3}» is signed by everyone. Thank you {2}. Your copy: {4}",
		"fr": "{1} : « {3} » est signé par tous. Merci {2}. Votre copie : {4}",
	},
	"offer": {
		"ar": "{1}: مبروك {2}! يسعدنا أن نعرض عليك وظيفة «{3}». سنتواصل معك لتأكيد التفاصيل.",
		"en": "{1}: congratulations {2}! We are happy to offer you the position «{3}». We will contact you to confirm the details.",
		"fr": "{1} : félicitations {2} ! Nous avons le plaisir de vous proposer le poste « {3} ». Nous vous contacterons pour les détails.",
	},
	"applied": {
		"ar": "{1}: شكراً {2}، استلمنا طلبك لوظيفة «{3}». سنتواصل معك قريباً.",
		"en": "{1}: thank you {2}, we received your application for «{3}». We will be in touch soon.",
		"fr": "{1} : merci {2}, nous avons reçu votre candidature pour « {3} ». Nous vous recontacterons bientôt.",
	},
}

// growthText fills one of the texts above in the company's default language.
func growthText(ctx context.Context, tx pgx.Tx, cid, key string, args ...string) string {
	var lang string
	_ = tx.QueryRow(ctx, `SELECT coalesce(default_locale, 'ar') FROM companies WHERE id = $1`, cid).Scan(&lang)
	t, ok := growthTexts[key][lang]
	if !ok {
		t = growthTexts[key]["ar"]
		if lang != "ar" && lang != "" {
			t = growthTexts[key]["en"]
		}
	}
	for i, a := range args {
		t = strings.ReplaceAll(t, "{"+strconv.Itoa(i+1)+"}", a)
	}
	return t
}

// outMessage is one message for the queue (WhatsApp / SMS / e-mail) to a customer or to any address.
type outMessage struct {
	CustomerID *string
	Channel    string // whatsapp | sms | email
	Phone      string
	Email      string
	Subject    string
	Body       string
	Event      string
	At         time.Time // not before (throttling / schedule); zero = now
}

// queueOut stores the message in the queue the message worker sends. Returns the message id, or "" and the reason
// it cannot be sent (no_phone / no_email). A missing provider still queues: the worker marks it failed with a reason.
func queueOut(ctx context.Context, tx pgx.Tx, cid string, m outMessage) (string, string, error) {
	if strings.TrimSpace(m.Body) == "" {
		return "", "empty", nil
	}
	if m.Event == "" {
		m.Event = "custom"
	}
	at := m.At
	if at.IsZero() {
		at = time.Now()
	}
	var id string
	if m.Channel == "email" {
		email := strings.TrimSpace(m.Email)
		if !strings.Contains(email, "@") || len(email) > 200 {
			return "", "no_email", nil
		}
		prov, err := pickEmailProvider(ctx, tx, cid)
		if err != nil {
			return "", "", err
		}
		var pid *string
		if prov != nil {
			pid = &prov.ID
		}
		subject := m.Subject
		if r := []rune(subject); len(r) > 200 {
			subject = string(r[:200])
		}
		err = tx.QueryRow(ctx, `INSERT INTO messages (company_id, customer_id, phone, email, subject, channel, provider_id, event, body, next_attempt_at)
			VALUES ($1, $2, '', $3, $4, 'email', $5, $6, $7, $8) RETURNING id::text`, cid, m.CustomerID, email, subject, pid, m.Event, m.Body, at).Scan(&id)
		return id, "", err
	}
	var country string
	if err := tx.QueryRow(ctx, `SELECT phone_country FROM companies WHERE id = $1`, cid).Scan(&country); err != nil {
		return "", "", err
	}
	phone := normalizePhone(m.Phone, country)
	if phone == "" {
		return "", "no_phone", nil
	}
	prov, err := pickProvider(ctx, tx, cid, "")
	if err != nil {
		return "", "", err
	}
	var pid *string
	if prov != nil {
		pid = &prov.ID
	}
	err = tx.QueryRow(ctx, `INSERT INTO messages (company_id, customer_id, phone, channel, provider_id, event, body, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`, cid, m.CustomerID, phone, channelFor(prov, m.Channel), pid, m.Event, m.Body, at).Scan(&id)
	return id, "", err
}

// notifyStaff tells the office users of the company (admins, accountants, data entry, or holders of perm) in the app.
func notifyStaff(ctx context.Context, tx pgx.Tx, cid, perm, kind, title, body, typ, id string) error {
	rows, err := tx.Query(ctx, `SELECT u.id::text FROM users u LEFT JOIN roles ro ON ro.id = u.role_id
		WHERE u.is_active AND (u.company_ids IS NULL OR $1::uuid = ANY (u.company_ids))
		  AND (u.role = 'admin' OR ($2 <> '' AND ro.id IS NOT NULL AND $2 = ANY (ro.permissions))
		       OR ($2 <> '' AND ro.id IS NULL AND u.role IN ('accountant', 'data_entry')))
		ORDER BY u.created_at LIMIT 30`, cid, perm)
	if err != nil {
		return err
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	return notifyUsers(ctx, tx, cid, users, kind, title, body, typ, id)
}

// ======================= background worker =======================

// inJobTx runs fn for one tenant as the application role (no user, row security on).
func (s *Server) inJobTx(ctx context.Context, tenant string, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
			return err
		}
		return fn(tx)
	})
}

// RunGrowthJobs sends the campaigns that are due and handles signature reminders and expiry. Returns the number of
// campaigns started and signature requests touched.
func (s *Server) RunGrowthJobs(ctx context.Context) (int, error) {
	if s.OwnerPool == nil {
		return 0, nil
	}
	total := 0
	rows, err := s.OwnerPool.Query(ctx, `SELECT DISTINCT tenant_id::text FROM mkt_campaigns WHERE status = 'scheduled' AND scheduled_at <= now() AND tenant_jobs_ok(tenant_id)
		UNION SELECT DISTINCT tenant_id::text FROM sign_requests WHERE status = 'sent' AND tenant_jobs_ok(tenant_id)`)
	if err != nil {
		return 0, err
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, t := range tenants {
		err := s.inJobTx(ctx, t, func(tx pgx.Tx) error {
			n, err := dispatchDueCampaigns(ctx, tx)
			total += n
			if err != nil {
				return err
			}
			n, err = signMaintenance(ctx, tx)
			total += n
			return err
		})
		if err != nil {
			slog.Error("growth jobs", "tenant", t, "err", err)
		}
	}
	return total, nil
}

// StartGrowthWorker: every minute.
func (s *Server) StartGrowthWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, err := s.RunGrowthJobs(ctx); err != nil {
					slog.Error("growth worker", "err", err)
				}
			}
		}
	}()
}
