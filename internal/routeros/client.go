// Package routeros is a minimal MikroTik RouterOS API client (the binary API on port 8728,
// or 8729 with TLS). It covers what Paxve needs: login, reading identity/version, and
// enabling/disabling PPPoE secrets or hotspot users (kicking the active session on disable).
//
// Protocol: a sentence is a list of words ended by an empty word; each word is prefixed by its
// length encoded in 1–5 bytes. Replies are sentences starting with !re, !done, !trap, !fatal or !empty.
package routeros

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned when the PPPoE secret / hotspot user does not exist on the router.
var ErrNotFound = errors.New("routeros: user not found on router")

// Error is a !trap or !fatal reply from the router.
type Error struct{ Message string }

func (e *Error) Error() string { return "routeros: " + e.Message }

// ---- length encoding ----

// EncodeLength encodes a word length as RouterOS expects.
func EncodeLength(n int) []byte {
	switch {
	case n < 0x80:
		return []byte{byte(n)}
	case n < 0x4000:
		n |= 0x8000
		return []byte{byte(n >> 8), byte(n)}
	case n < 0x200000:
		n |= 0xC00000
		return []byte{byte(n >> 16), byte(n >> 8), byte(n)}
	case n < 0x10000000:
		n |= 0xE0000000
		return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	default:
		return []byte{0xF0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}

// ReadLength decodes a word length.
func ReadLength(r io.ByteReader) (int, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	c := int(b)
	extra := 0
	switch {
	case c&0x80 == 0x00:
		return c, nil
	case c&0xC0 == 0x80:
		c &= ^0xC0
		extra = 1
	case c&0xE0 == 0xC0:
		c &= ^0xE0
		extra = 2
	case c&0xF0 == 0xE0:
		c &= ^0xF0
		extra = 3
	case c == 0xF0:
		c = 0
		extra = 4
	default:
		return 0, fmt.Errorf("routeros: bad length byte %#x", b)
	}
	for i := 0; i < extra; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		c = c<<8 | int(b)
	}
	return c, nil
}

// EncodeSentence returns the wire bytes of a sentence.
func EncodeSentence(words ...string) []byte {
	var out []byte
	for _, w := range words {
		out = append(out, EncodeLength(len(w))...)
		out = append(out, w...)
	}
	return append(out, 0)
}

// ReadSentence reads one sentence (words until the empty word).
func ReadSentence(r *bufio.Reader) ([]string, error) {
	var words []string
	for {
		n, err := ReadLength(r)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return words, nil
		}
		if n > 1<<24 {
			return nil, fmt.Errorf("routeros: word too long (%d)", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		words = append(words, string(buf))
	}
}

// Attrs parses "=key=value" words of a reply sentence.
func Attrs(words []string) map[string]string {
	m := map[string]string{}
	for _, w := range words {
		if strings.HasPrefix(w, "=") {
			if k, v, ok := strings.Cut(w[1:], "="); ok {
				m[k] = v
			}
		}
	}
	return m
}

// ---- client ----

type Client struct {
	conn    net.Conn
	r       *bufio.Reader
	timeout time.Duration
}

// Dial connects to the router. With TLS the router certificate is usually self-signed, so it is not verified.
func Dial(ctx context.Context, host string, port int, useTLS bool, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	d := &net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	if useTLS {
		conn, err = (&tls.Dialer{NetDialer: d, Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", addr) // #nosec G402 -- RouterOS uses self-signed certs
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, r: bufio.NewReader(conn), timeout: timeout}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Run sends a command and returns the attributes of every !re reply. !trap / !fatal become *Error.
func (c *Client) Run(words ...string) ([]map[string]string, error) {
	_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	if _, err := c.conn.Write(EncodeSentence(words...)); err != nil {
		return nil, err
	}
	var out []map[string]string
	var trap error
	for {
		s, err := ReadSentence(c.r)
		if err != nil {
			return nil, err
		}
		if len(s) == 0 {
			continue
		}
		switch s[0] {
		case "!re":
			out = append(out, Attrs(s[1:]))
		case "!trap":
			msg := Attrs(s[1:])["message"]
			if msg == "" {
				msg = "trap"
			}
			trap = &Error{Message: msg}
		case "!fatal":
			msg := strings.Join(s[1:], " ")
			return nil, &Error{Message: "fatal: " + msg}
		case "!done", "!empty":
			if s[0] == "!done" {
				if trap != nil {
					return nil, trap
				}
				// pre-6.43 login challenge comes back in !done
				if a := Attrs(s[1:]); a["ret"] != "" {
					out = append(out, map[string]string{"ret": a["ret"]})
				}
				return out, nil
			}
			// !empty is followed by !done
		}
	}
}

// Login authenticates (RouterOS 6.43+ plain login, with fallback to the old MD5 challenge).
func (c *Client) Login(user, pass string) error {
	res, err := c.Run("/login", "=name="+user, "=password="+pass)
	if err != nil {
		return err
	}
	if len(res) == 1 && res[0]["ret"] != "" {
		chal, err := hex.DecodeString(res[0]["ret"])
		if err != nil {
			return errors.New("routeros: bad login challenge")
		}
		h := md5.New() // #nosec G401 -- legacy RouterOS challenge protocol
		h.Write([]byte{0})
		h.Write([]byte(pass))
		h.Write(chal)
		_, err = c.Run("/login", "=name="+user, "=response=00"+hex.EncodeToString(h.Sum(nil)))
		return err
	}
	return nil
}

// Identity returns the router name and RouterOS version.
func (c *Client) Identity() (identity, version string, err error) {
	id, err := c.Run("/system/identity/print")
	if err != nil {
		return "", "", err
	}
	if len(id) > 0 {
		identity = id[0]["name"]
	}
	res, err := c.Run("/system/resource/print")
	if err != nil {
		return identity, "", err
	}
	if len(res) > 0 {
		version = res[0]["version"]
	}
	return identity, version, nil
}

// SetUserDisabled enables or disables a PPPoE secret ("pppoe") or hotspot user ("hotspot").
// When disabling, the user's active session is removed so the change takes effect at once.
func (c *Client) SetUserDisabled(service, username string, disabled bool) error {
	userPath, activePath, activeKey := "/ppp/secret", "/ppp/active", "name"
	if service == "hotspot" {
		userPath, activePath, activeKey = "/ip/hotspot/user", "/ip/hotspot/active", "user"
	}
	found, err := c.Run(userPath+"/print", "?name="+username, "=.proplist=.id,name,disabled")
	if err != nil {
		return err
	}
	if len(found) == 0 || found[0][".id"] == "" {
		return ErrNotFound
	}
	flag := "no"
	if disabled {
		flag = "yes"
	}
	if _, err := c.Run(userPath+"/set", "=.id="+found[0][".id"], "=disabled="+flag); err != nil {
		return err
	}
	if disabled {
		active, err := c.Run(activePath+"/print", "?"+activeKey+"="+username, "=.proplist=.id")
		if err != nil {
			return err
		}
		for _, a := range active {
			if a[".id"] == "" {
				continue
			}
			if _, err := c.Run(activePath+"/remove", "=.id="+a[".id"]); err != nil {
				return err
			}
		}
	}
	return nil
}
