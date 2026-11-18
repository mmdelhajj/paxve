package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

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

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, key)
}
