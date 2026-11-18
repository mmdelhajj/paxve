package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// Outbound webhooks: a company registers an URL and the events it wants. Database triggers queue one
// delivery per event at commit (webhook_deliveries); a background worker POSTs them as JSON with an
// HMAC-SHA256 signature and retries failures with growing delays (8 attempts over about 4 hours).
//
// Signature header: X-Paxve-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>

var webhookEvents = []string{"invoice.created", "payment.created", "customer.created", "pos.order.created"}

const webhookMaxAttempts = 8

func validWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && len(raw) <= 2000
}

func newWebhookSecret() string { return "whsec_" + randToken(24) }

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT h.id, h.url, h.description, h.events, h.is_active, h.created_at,
			(SELECT count(*) FROM webhook_deliveries d WHERE d.webhook_id = h.id AND d.status = 'pending') AS pending,
			(SELECT count(*) FROM webhook_deliveries d WHERE d.webhook_id = h.id AND d.status = 'failed') AS failed,
			(SELECT max(sent_at) FROM webhook_deliveries d WHERE d.webhook_id = h.id) AS last_sent_at
			FROM webhooks h WHERE h.company_id = $1 ORDER BY h.created_at`, cid)
	})
}

func (s *Server) saveWebhook(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if !create && !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		URL         *string   `json:"url"`
		Description *string   `json:"description"`
		Events      *[]string `json:"events"`
		IsActive    *bool     `json:"is_active"`
	}
	if err := decode(w, r, &in); err != nil || (create && (in.URL == nil || in.Events == nil)) ||
		(in.URL != nil && !validWebhookURL(strings.TrimSpace(*in.URL))) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Events != nil {
		if len(*in.Events) == 0 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		for _, e := range *in.Events {
			if !slices.Contains(webhookEvents, e) {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
		}
	}
	if in.URL != nil {
		u := strings.TrimSpace(*in.URL)
		in.URL = &u
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if create {
			secret := newWebhookSecret()
			sealed, err := sealSecret(secret)
			if err != nil {
				return nil, err
			}
			var nid string
			if err := tx.QueryRow(ctx, `INSERT INTO webhooks (company_id, url, description, secret_enc, events)
				VALUES ($1, $2, coalesce($3, ''), $4, $5) RETURNING id::text`, cid, *in.URL, in.Description, sealed, *in.Events).Scan(&nid); err != nil {
				return nil, err
			}
			// the secret is shown once; afterwards only rotate gives a new one
			return map[string]any{"id": nid, "secret": secret}, nil
		}
		tag, err := tx.Exec(ctx, `UPDATE webhooks SET url = coalesce($3, url), description = coalesce($4, description),
			events = coalesce($5, events), is_active = coalesce($6, is_active) WHERE id = $1 AND company_id = $2`,
			id, cid, in.URL, in.Description, in.Events, in.IsActive)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

func (s *Server) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		secret := newWebhookSecret()
		sealed, err := sealSecret(secret)
		if err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `UPDATE webhooks SET secret_enc = $3 WHERE id = $1 AND company_id = $2`, id, cid, sealed)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]string{"secret": secret}, nil
	})
}

// testWebhook queues a "ping" delivery.
func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var did string
		err := tx.QueryRow(ctx, `INSERT INTO webhook_deliveries (company_id, webhook_id, event, payload)
			SELECT h.company_id, h.id, 'ping', jsonb_build_object('id', gen_random_uuid(), 'event', 'ping', 'created_at', now(),
			       'company_id', h.company_id, 'data', jsonb_build_object('message', 'Paxve webhook test'))
			FROM webhooks h WHERE h.id = $1 AND h.company_id = $2 RETURNING id::text`, id, cid).Scan(&did)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		return map[string]string{"delivery_id": did}, err
	})
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var known bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM webhooks WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&known); err != nil {
			return nil, err
		}
		if !known {
			return nil, notFound()
		}
		return queryJSON(ctx, tx, `SELECT id, event, status, attempts, response_code, last_error, created_at, sent_at,
			next_attempt_at, payload FROM webhook_deliveries WHERE webhook_id = $1 AND company_id = $2
			ORDER BY created_at DESC LIMIT 100`, id, cid)
	})
}

func (s *Server) retryDelivery(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE webhook_deliveries SET status = 'pending', attempts = 0, next_attempt_at = now(), last_error = NULL
			WHERE id = $1 AND company_id = $2 AND status <> 'pending'`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ---- delivery worker ----

// SignWebhook returns the X-Paxve-Signature value for body (exported for receivers' tests and docs).
func SignWebhook(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(ts, 10) + "."))
	m.Write(body)
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

// webhookClient refuses private, loopback and link-local targets at connect time (after DNS), so a
// tenant cannot use webhooks to reach the server's internal network, even through DNS rebinding.
func webhookClient(allowPrivate bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
			return errors.New("private or local address not allowed")
		}
		return nil
	}}
	tr := &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second, MaxIdleConns: 10, IdleConnTimeout: 30 * time.Second, Proxy: nil}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// StartWebhookWorker sends due deliveries every few seconds (owner pool: crosses tenants, like the message worker).
func (s *Server) StartWebhookWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	client := webhookClient(s.AllowPrivateRouters)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for range 10 { // up to 10 batches per tick while there is work
					n, err := s.sendWebhookBatch(ctx, client)
					if err != nil {
						slog.Error("webhook worker", "err", err)
						break
					}
					if n == 0 {
						break
					}
				}
			}
		}
	}()
}

type dueDelivery struct {
	id, url, secretEnc, event string
	payload                   []byte
	attempts                  int
}

func (s *Server) sendWebhookBatch(ctx context.Context, client *http.Client) (int, error) {
	var due []dueDelivery
	err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT d.id::text, h.url, h.secret_enc, d.event, d.payload::text, d.attempts
			FROM webhook_deliveries d JOIN webhooks h ON h.id = d.webhook_id JOIN tenants t ON t.id = d.tenant_id
			WHERE d.status = 'pending' AND d.next_attempt_at <= now()
			  AND (h.is_active OR d.event = 'ping') AND t.is_active -- paused webhooks keep their queue until turned on again
			ORDER BY d.next_attempt_at LIMIT 20 FOR UPDATE OF d SKIP LOCKED`)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (dueDelivery, error) {
			var d dueDelivery
			var payload string
			err := r.Scan(&d.id, &d.url, &d.secretEnc, &d.event, &payload, &d.attempts)
			d.payload = []byte(payload)
			return d, err
		})
		if err != nil || len(due) == 0 {
			return err
		}
		// lease them for a minute so another worker does not pick them while they are being sent
		ids := make([]string, len(due))
		for i, d := range due {
			ids[i] = d.id
		}
		_, err = tx.Exec(ctx, `UPDATE webhook_deliveries SET next_attempt_at = now() + interval '1 minute' WHERE id = ANY ($1::uuid[])`, ids)
		return err
	})
	if err != nil || len(due) == 0 {
		return 0, err
	}
	for _, d := range due {
		code, sendErr := deliverWebhook(ctx, client, d)
		attempts := d.attempts + 1
		var status string
		var next time.Duration
		errText := ""
		switch {
		case sendErr == nil:
			status = "sent"
		case attempts >= webhookMaxAttempts:
			status, errText = "failed", sendErr.Error()
		default:
			status, errText = "pending", sendErr.Error()
			next = time.Duration(1<<attempts) * time.Minute // 2, 4, 8 ... 128 minutes
		}
		_, err := s.OwnerPool.Exec(ctx, `UPDATE webhook_deliveries SET status = $2, attempts = $3, response_code = $4,
			last_error = nullif(left($5, 500), ''), sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END,
			next_attempt_at = now() + make_interval(secs => $6) WHERE id = $1`,
			d.id, status, attempts, code, errText, next.Seconds())
		if err != nil {
			return 0, err
		}
	}
	return len(due), nil
}

func deliverWebhook(ctx context.Context, client *http.Client, d dueDelivery) (*int, error) {
	secret := openSecret(d.secretEnc)
	if secret == "" {
		return nil, errors.New("webhook secret cannot be decrypted")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, d.payload); err != nil {
		return nil, err
	}
	body := compact.Bytes()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Paxve-Webhooks/1")
	req.Header.Set("X-Paxve-Event", d.event)
	req.Header.Set("X-Paxve-Delivery", d.id)
	req.Header.Set("X-Paxve-Signature", SignWebhook(secret, time.Now().Unix(), body))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	code := resp.StatusCode
	if code < 200 || code > 299 {
		return &code, fmt.Errorf("HTTP %d", code)
	}
	return &code, nil
}
