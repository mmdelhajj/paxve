package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr sends an error code; the web app translates codes into English/Arabic.
func writeErr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// apiError is returned from inside transactions to send a specific error code.
type apiError struct {
	status int
	code   string
	detail string
}

func (e *apiError) Error() string { return e.code }

func badRequest(code string) error { return &apiError{status: http.StatusBadRequest, code: code} }
func notFound() error              { return &apiError{status: http.StatusNotFound, code: "not_found"} }

// fail maps an error to a response: apiError, accounting rule violations
// raised by the database, constraint errors, or a 500.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		if ae.detail != "" {
			writeJSON(w, ae.status, map[string]string{"error": ae.code, "detail": ae.detail})
		} else {
			writeErr(w, ae.status, ae.code)
		}
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "PXUNB":
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "unbalanced", "detail": pg.Message})
			return
		case "PXACC":
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_account", "detail": pg.Message})
			return
		case "PXIMM":
			writeJSON(w, http.StatusConflict, map[string]string{"error": "posted_locked", "detail": pg.Message})
			return
		case "PXLCK":
			writeJSON(w, http.StatusConflict, map[string]string{"error": "period_locked", "detail": pg.Message})
			return
		case "PXSTA":
			writeJSON(w, http.StatusConflict, map[string]string{"error": "wrong_status", "detail": pg.Message})
			return
		case "PXNFD", "42501":
			// a referenced document of another company (hidden by company_scope), or a write refused by it
			if pg.Code == "PXNFD" || strings.Contains(pg.Message, "row-level security") {
				writeErr(w, http.StatusNotFound, "not_found")
				return
			}
		case "23505":
			writeErr(w, http.StatusConflict, "duplicate")
			return
		case "22003":
			// numeric field overflow: never leak the database text (audit B-L1)
			writeErr(w, http.StatusBadRequest, "amount_too_large")
			return
		case "22001":
			writeErr(w, http.StatusBadRequest, "text_too_long")
			return
		case "23503", "23514", "22P02", "22007", "22008":
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": pg.Message})
			return
		}
	}
	serverErr(w, r, err)
}

// limiter counts failed logins per IP.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	m      map[string]*bucket
}

type bucket struct {
	n     int
	until time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, m: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.m[key]
	return b == nil || time.Now().After(b.until) || b.n < l.max
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.m[key]
	if b == nil || now.After(b.until) {
		b = &bucket{until: now.Add(l.window)}
		l.m[key] = b
	}
	b.n++
	if len(l.m) > 10000 {
		for k, v := range l.m {
			if now.After(v.until) {
				delete(l.m, k)
			}
		}
	}
}

// retryAfter: how long until the key may try again (0 when it may now).
func (l *limiter) retryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.m[key]
	if b == nil || b.n < l.max {
		return 0
	}
	if d := time.Until(b.until); d > 0 {
		return d
	}
	return 0
}

// tooMany answers 429 with the seconds to wait in detail (the UI shows "try again in N seconds").
func tooMany(w http.ResponseWriter, l *limiter, key string) {
	secs := int(l.retryAfter(key).Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_attempts", "detail": strconv.Itoa(secs)})
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, key)
}

// backoff locks a key after `free` failures for base·2^(extra) (capped), counted from the failure that
// triggered it. Attempts made while locked are refused before fail() is called, so retrying does NOT extend
// the lock. Failures are forgotten `forget` after the last one.
type backoff struct {
	mu     sync.Mutex
	free   int
	base   time.Duration
	max    time.Duration
	forget time.Duration
	m      map[string]*boBucket
}

type boBucket struct {
	n           int
	last        time.Time
	lockedUntil time.Time
}

func newBackoff(free int, base, max, forget time.Duration) *backoff {
	return &backoff{free: free, base: base, max: max, forget: forget, m: map[string]*boBucket{}}
}

// wait returns how long the key is still locked (0 = allowed).
func (b *backoff) wait(key string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := b.m[key]
	if k == nil {
		return 0
	}
	if d := time.Until(k.lockedUntil); d > 0 {
		return d
	}
	return 0
}

func (b *backoff) fail(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	k := b.m[key]
	if k == nil || now.Sub(k.last) > b.forget {
		k = &boBucket{}
		b.m[key] = k
	}
	k.n++
	k.last = now
	if k.n >= b.free {
		d := b.base << min(k.n-b.free, 10)
		if d > b.max || d <= 0 {
			d = b.max
		}
		k.lockedUntil = now.Add(d)
	}
	if len(b.m) > 20000 {
		for key, v := range b.m {
			if now.Sub(v.last) > b.forget {
				delete(b.m, key)
			}
		}
	}
}

func (b *backoff) reset(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, key)
}

// trustSet remembers keys (an account + the address it signed in from) for a while.
type trustSet struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]time.Time
}

func newTrustSet(ttl time.Duration) *trustSet { return &trustSet{ttl: ttl, m: map[string]time.Time{}} }

func (t *trustSet) add(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.m[key] = now
	if len(t.m) > 50000 {
		for k, v := range t.m {
			if now.Sub(v) > t.ttl {
				delete(t.m, k)
			}
		}
	}
}

func (t *trustSet) has(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.m[key]
	return ok && time.Since(v) < t.ttl
}
