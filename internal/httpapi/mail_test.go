package httpapi

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"
)

// fakeSMTP accepts one message (no TLS, no auth) and returns what it received.
func fakeSMTP(t *testing.T) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		defer ln.Close()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		w("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					w("250 queued")
					continue
				}
				data.WriteString(line)
				continue
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250 fake")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				w("250 ok")
			case cmd == "DATA":
				inData = true
				w("354 go")
			case cmd == "QUIT":
				w("221 bye")
				got <- data.String()
				return
			default:
				w("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestSendSMTP(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	cfg := map[string]any{"host": host, "port": port, "security": "none", "from_email": "billing@example.com", "from_name": "شركة"}
	id, err := sendSMTP(context.Background(), cfg, mailMsg{To: "client@example.com", Subject: "Facture n°7", Body: "مرحبا https://paxve.com/#/p/x"}, true)
	if err != nil || id == "" {
		t.Fatalf("send: %v %q", err, id)
	}
	msg := <-got
	if !strings.Contains(msg, "To: client@example.com") || !strings.Contains(msg, "=?utf-8?q?") {
		t.Fatalf("headers: %s", msg)
	}
	body := msg[strings.Index(msg, "\r\n\r\n")+4:]
	dec, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(body), "\r\n", ""))
	if !strings.Contains(string(dec), "مرحبا") {
		t.Fatalf("body: %q", dec)
	}
	// private addresses are refused unless allowed
	if _, err := sendSMTP(context.Background(), cfg, mailMsg{To: "client@example.com", Subject: "x", Body: "y"}, false); err == nil {
		t.Fatal("private SMTP host accepted")
	}
	if _, err := sendSMTP(context.Background(), cfg, mailMsg{To: "bad\r\nBcc: x@y.z", Subject: "x", Body: "y"}, true); err == nil {
		t.Fatal("header injection accepted")
	}
}
