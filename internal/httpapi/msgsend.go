package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- phone numbers ----

var nonDigits = regexp.MustCompile(`[^\d+]`)

// normalizePhone returns an E.164 number ("+9613123456") or "" when the number is not usable.
// Lebanese numbers: 03 123456 / 70 123456 / 71 / 76 / 78 / 79 / 81 mobiles, with or without the leading 0 or +961.
func normalizePhone(raw, country string) string {
	p := nonDigits.ReplaceAllString(strings.TrimSpace(raw), "")
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "00") {
		p = "+" + p[2:]
	}
	if strings.HasPrefix(p, "+") {
		d := strings.TrimPrefix(p, "+")
		if strings.Contains(d, "+") || len(d) < 8 || len(d) > 15 {
			return ""
		}
		return "+" + d
	}
	if strings.Contains(p, "+") {
		return ""
	}
	if country == "" {
		country = "961"
	}
	if strings.HasPrefix(p, country) && len(p) > len(country)+6 {
		return "+" + p
	}
	p = strings.TrimLeft(p, "0")
	if country == "961" {
		// 7 digits (3xxxxxx, 1xxxxxx landline) or 8 digits (70/71/76/78/79/81xxxxxx)
		if len(p) != 7 && len(p) != 8 {
			return ""
		}
	} else if len(p) < 6 || len(p) > 12 {
		return ""
	}
	return "+" + country + p
}

// ---- secrets in provider config ----

var secretKeys = map[string]bool{"token": true, "auth_token": true, "api_key": true, "bearer": true, "password": true, "secret": true}

const maskPrefix = "••••"

// maskConfig hides secret values (keeps the last 4 characters so the user can recognise them).
func maskConfig(cfg map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range cfg {
		if s, ok := v.(string); ok && secretKeys[k] && s != "" {
			s = openSecret(s) // stored encrypted: show the tail of the real value
			tail := ""
			if len(s) > 8 {
				tail = s[len(s)-4:]
			}
			out[k] = maskPrefix + tail
			continue
		}
		out[k] = v
	}
	return out
}

// mergeConfig applies an update: secret fields sent back masked or empty keep their stored value.
func mergeConfig(old, upd map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range upd {
		if s, ok := v.(string); ok && secretKeys[k] && (s == "" || strings.HasPrefix(s, maskPrefix)) {
			if ov, ok := old[k]; ok {
				out[k] = ov
			}
			continue
		}
		out[k] = v
	}
	return out
}

func cfgString(cfg map[string]any, k string) string {
	if s, ok := cfg[k].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// ---- providers ----

var httpClient = &http.Client{Timeout: 20 * time.Second}

type outMsg struct {
	Phone      string // E.164
	Body       string
	WATemplate string
	Locale     string
	Params     []string
}

// sendVia delivers one message. Returns the provider's message id.
func sendVia(ctx context.Context, kind string, cfg map[string]any, m outMsg) (string, error) {
	switch kind {
	case "log":
		slog.Info("message (log provider)", "to", m.Phone, "chars", len(m.Body))
		return fmt.Sprintf("log-%d", time.Now().UnixNano()), nil
	case "whatsapp_cloud":
		phoneID, token := cfgString(cfg, "phone_number_id"), cfgString(cfg, "token")
		if phoneID == "" || token == "" {
			return "", errors.New("whatsapp: phone_number_id and token are required")
		}
		ver := cfgString(cfg, "api_version")
		if ver == "" {
			ver = "v21.0"
		}
		payload := map[string]any{"messaging_product": "whatsapp", "to": strings.TrimPrefix(m.Phone, "+")}
		if m.WATemplate != "" {
			params := []map[string]string{}
			for _, p := range m.Params {
				params = append(params, map[string]string{"type": "text", "text": p})
			}
			tpl := map[string]any{"name": m.WATemplate, "language": map[string]string{"code": m.Locale}}
			if len(params) > 0 {
				tpl["components"] = []map[string]any{{"type": "body", "parameters": params}}
			}
			payload["type"] = "template"
			payload["template"] = tpl
		} else {
			payload["type"] = "text"
			payload["text"] = map[string]any{"body": m.Body, "preview_url": false}
		}
		var res struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
		}
		err := postJSON(ctx, "https://graph.facebook.com/"+url.PathEscape(ver)+"/"+url.PathEscape(phoneID)+"/messages",
			"Bearer "+token, payload, &res)
		if err != nil {
			return "", err
		}
		if len(res.Messages) > 0 {
			return res.Messages[0].ID, nil
		}
		return "", nil
	case "twilio_sms":
		sid, tok, from := cfgString(cfg, "account_sid"), cfgString(cfg, "auth_token"), cfgString(cfg, "from")
		if sid == "" || tok == "" || from == "" {
			return "", errors.New("twilio: account_sid, auth_token and from are required")
		}
		form := url.Values{"To": {m.Phone}, "From": {from}, "Body": {m.Body}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://api.twilio.com/2010-04-01/Accounts/"+url.PathEscape(sid)+"/Messages.json", strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(sid+":"+tok)))
		var res struct {
			SID string `json:"sid"`
		}
		if err := doJSON(req, &res); err != nil {
			return "", err
		}
		return res.SID, nil
	case "generic_http":
		u := cfgString(cfg, "url")
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return "", errors.New("generic: url is required")
		}
		auth := ""
		if b := cfgString(cfg, "bearer"); b != "" {
			auth = "Bearer " + b
		}
		var res map[string]any
		if err := postJSON(ctx, u, auth, map[string]string{"to": m.Phone, "text": m.Body}, &res); err != nil {
			return "", err
		}
		if id, ok := res["id"].(string); ok {
			return id, nil
		}
		return "", nil
	}
	return "", fmt.Errorf("unknown provider kind %q", kind)
}

func postJSON(ctx context.Context, u, auth string, payload, out any) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return doJSON(req, out)
}

func doJSON(req *http.Request, out any) error {
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, msg)
	}
	if out != nil && len(data) > 0 {
		_ = json.Unmarshal(data, out)
	}
	return nil
}

// ---- background sender ----

// StartMessageWorker sends queued messages for all tenants. It uses the owner pool (the only
// cross-tenant path besides sign-up), and sets app.tenant_id on every write it makes.
func (s *Server) StartMessageWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	// messages left in "sending" by a crash go back to the queue
	_, _ = s.OwnerPool.Exec(ctx, `UPDATE messages SET status = 'queued' WHERE status = 'sending' AND updated_at < now() - interval '5 minutes'`)
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for i := 0; i < 10; i++ { // up to 10 batches per tick when there is a backlog
					n, err := s.sendBatch(ctx)
					if err != nil {
						slog.Error("message worker", "err", err)
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

type queuedMsg struct {
	ID, Tenant, Phone, Body, Event string
	Channel, Email, Subject        string
	Attempts                       int
	Kind                           *string
	Config, Params                 []byte
	WATemplate, Locale             *string
}

func (s *Server) sendBatch(ctx context.Context) (int, error) {
	var batch []queuedMsg
	err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT m.id::text, m.tenant_id::text, m.phone, m.body, m.event, m.attempts, p.kind, p.config, m.params, t.wa_template, t.locale,
			       m.channel, m.email, m.subject
			FROM messages m
			LEFT JOIN message_providers p ON p.id = m.provider_id AND p.is_active
			LEFT JOIN message_templates t ON t.id = m.template_id
			WHERE m.status = 'queued' AND m.next_attempt_at <= now()
			ORDER BY m.next_attempt_at
			LIMIT 25
			FOR UPDATE OF m SKIP LOCKED`)
		if err != nil {
			return err
		}
		batch, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (queuedMsg, error) {
			var q queuedMsg
			err := row.Scan(&q.ID, &q.Tenant, &q.Phone, &q.Body, &q.Event, &q.Attempts, &q.Kind, &q.Config, &q.Params, &q.WATemplate, &q.Locale,
				&q.Channel, &q.Email, &q.Subject)
			return q, err
		})
		if err != nil || len(batch) == 0 {
			return err
		}
		ids := make([]string, len(batch))
		for i, q := range batch {
			ids[i] = q.ID
		}
		_, err = tx.Exec(ctx, `UPDATE messages SET status = 'sending' WHERE id = ANY($1::uuid[])`, ids)
		return err
	})
	if err != nil || len(batch) == 0 {
		return 0, err
	}
	for _, q := range batch {
		s.deliver(ctx, q)
	}
	return len(batch), nil
}

func (s *Server) deliver(ctx context.Context, q queuedMsg) {
	var providerID string
	var sendErr error
	if q.Kind == nil {
		sendErr = errors.New("no_provider: add a WhatsApp/SMS provider in Messages settings")
	} else if q.Channel == "email" || *q.Kind == "smtp" {
		if *q.Kind != "smtp" || q.Channel != "email" {
			sendErr = errors.New("no_provider: e-mail needs an SMTP provider, WhatsApp/SMS cannot use it")
		} else {
			cfg := map[string]any{}
			_ = json.Unmarshal(q.Config, &cfg)
			cfg = openConfig(cfg)
			sctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			providerID, sendErr = sendSMTP(sctx, cfg, mailMsg{To: q.Email, Subject: q.Subject, Body: q.Body}, s.AllowPrivateRouters)
			cancel()
		}
	} else {
		cfg := map[string]any{}
		_ = json.Unmarshal(q.Config, &cfg)
		cfg = openConfig(cfg)
		m := outMsg{Phone: q.Phone, Body: q.Body, Locale: "ar"}
		if q.WATemplate != nil && *q.WATemplate != "" {
			m.WATemplate = *q.WATemplate
			_ = json.Unmarshal(q.Params, &m.Params)
			if q.Locale != nil {
				m.Locale = *q.Locale
			}
		}
		sctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		providerID, sendErr = sendVia(sctx, *q.Kind, cfg, m)
		cancel()
	}
	err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, q.Tenant); err != nil {
			return err
		}
		if sendErr == nil {
			_, err := tx.Exec(ctx, `UPDATE messages SET status = 'sent', sent_at = now(), error = '', attempts = attempts + 1,
				provider_message_id = $2 WHERE id = $1`, q.ID, providerID)
			return err
		}
		attempts := q.Attempts + 1
		status := "queued"
		if attempts >= 5 || strings.HasPrefix(sendErr.Error(), "no_provider") {
			status = "failed"
		}
		backoff := time.Duration(30*(1<<attempts)) * time.Second // 1m, 2m, 4m, 8m
		msg := sendErr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		_, err := tx.Exec(ctx, `UPDATE messages SET status = $2, error = $3, attempts = $4, next_attempt_at = now() + $5::interval
			WHERE id = $1`, q.ID, status, msg, attempts, fmt.Sprintf("%d seconds", int(backoff.Seconds())))
		return err
	})
	if err != nil {
		slog.Error("message worker update", "id", q.ID, "err", err)
	}
}
