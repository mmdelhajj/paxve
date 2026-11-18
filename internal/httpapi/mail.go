package httpapi

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// E-mail through the company's own SMTP server (provider kind "smtp").
// config: host, port, username, password (secret, encrypted at rest), from_email, from_name,
// security: "starttls" (default, port 587) | "tls" (implicit TLS, port 465) | "none".

type mailMsg struct {
	To, Subject, Body string
}

func sendSMTP(ctx context.Context, cfg map[string]any, m mailMsg, allowPrivate bool) (string, error) {
	host, user, pass := cfgString(cfg, "host"), cfgString(cfg, "username"), cfgString(cfg, "password")
	from, fromName, security := cfgString(cfg, "from_email"), cfgString(cfg, "from_name"), cfgString(cfg, "security")
	if from == "" {
		from = user
	}
	if host == "" || !emailRe.MatchString(from) {
		return "", errors.New("smtp: host and from_email are required")
	}
	if !emailRe.MatchString(m.To) || strings.ContainsAny(m.To, "\r\n") {
		return "", errors.New("smtp: invalid recipient")
	}
	if security == "" {
		security = "starttls"
	}
	port := 587
	if security == "tls" {
		port = 465
	}
	if p := cfgString(cfg, "port"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 && v < 65536 {
			port = v
		}
	} else if v, ok := cfg["port"].(float64); ok && v > 0 && v < 65536 {
		port = int(v)
	}

	// resolve and refuse internal addresses (tenants must not reach the server's private network)
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return "", errors.New("smtp: host not found")
	}
	var ip net.IP
	for _, a := range addrs {
		if allowPrivate || !(a.IP.IsLoopback() || a.IP.IsPrivate() || a.IP.IsLinkLocalUnicast() || a.IP.IsUnspecified() || a.IP.IsMulticast()) {
			ip = a.IP
			break
		}
	}
	if ip == nil {
		return "", errors.New("smtp: private or local addresses are not allowed")
	}
	addr := net.JoinHostPort(ip.String(), strconv.Itoa(port))
	deadline := time.Now().Add(20 * time.Second)
	d := net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if security == "tls" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, tlsCfg)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return "", fmt.Errorf("smtp: cannot connect to %s:%d", host, port)
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("smtp: %v", err)
	}
	defer c.Close()
	if security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return "", errors.New("smtp: server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return "", fmt.Errorf("smtp: starttls: %v", err)
		}
	}
	if user != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			// PlainAuth refuses to send credentials without TLS unless the host is localhost
			if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
				return "", fmt.Errorf("smtp: login failed: %v", err)
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return "", fmt.Errorf("smtp: MAIL FROM: %v", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return "", fmt.Errorf("smtp: RCPT TO: %v", err)
	}
	wc, err := c.Data()
	if err != nil {
		return "", fmt.Errorf("smtp: DATA: %v", err)
	}
	msgID := fmt.Sprintf("<%d.paxve@%s>", time.Now().UnixNano(), strings.SplitN(from, "@", 2)[1])
	fromHdr := from
	if fromName != "" {
		fromHdr = mime.QEncoding.Encode("utf-8", fromName) + " <" + from + ">"
	}
	var b strings.Builder
	b.WriteString("From: " + fromHdr + "\r\n")
	b.WriteString("To: " + m.To + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", strings.ReplaceAll(m.Subject, "\n", " ")) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + msgID + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(m.Body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	if _, err := wc.Write([]byte(b.String())); err != nil {
		return "", fmt.Errorf("smtp: %v", err)
	}
	if err := wc.Close(); err != nil {
		return "", fmt.Errorf("smtp: %v", err)
	}
	_ = c.Quit()
	return msgID, nil
}
