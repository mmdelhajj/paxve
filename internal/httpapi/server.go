// Package httpapi is the JSON API (/api/v1) and the static web app server.
package httpapi

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmdelhajj/paxve/internal/db"
)

const (
	RoleAdmin      = "admin"
	RoleAccountant = "accountant"
	RoleDataEntry  = "data_entry"
	RoleReadOnly   = "read_only"
)

var validRoles = map[string]bool{RoleAdmin: true, RoleAccountant: true, RoleDataEntry: true, RoleReadOnly: true}

// Languages the interface ships with. Adding one = adding a translation file in web/src/locales.
var supportedLocales = map[string]bool{"en": true, "ar": true, "fr": true}

//go:embed openapi.yaml
var openapiSpec []byte

type Server struct {
	Pool     *pgxpool.Pool
	Sessions *scs.SessionManager
	WebDir   string
	limiter  *limiter
}

func New(pool *pgxpool.Pool, sessions *scs.SessionManager, webDir string) *Server {
	return &Server{Pool: pool, Sessions: sessions, WebDir: webDir, limiter: newLimiter(10, 15*time.Minute)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	const v = "/api/v1"
	write := []string{RoleAdmin, RoleAccountant}
	entry := []string{RoleAdmin, RoleAccountant, RoleDataEntry}

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET "+v+"/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(openapiSpec)
	})

	// session (browser) auth
	mux.HandleFunc("POST "+v+"/auth/login", s.login)
	mux.Handle("POST "+v+"/auth/logout", s.auth(s.logout))
	mux.Handle("GET "+v+"/me", s.auth(s.me))
	mux.Handle("PATCH "+v+"/me", s.auth(sessionOnly(s.updateMe)))

	// reference data
	mux.Handle("GET "+v+"/currencies", s.auth(s.listCurrencies))

	// organisation
	mux.Handle("GET "+v+"/companies", s.auth(s.listCompanies))
	mux.Handle("POST "+v+"/companies", s.auth(only(s.createCompany, RoleAdmin)))
	mux.Handle("PATCH "+v+"/companies/{cid}", s.auth(only(s.updateCompany, RoleAdmin)))
	mux.Handle("GET "+v+"/users", s.auth(only(s.listUsers, RoleAdmin)))
	mux.Handle("POST "+v+"/users", s.auth(only(s.createUser, RoleAdmin)))
	mux.Handle("PATCH "+v+"/users/{id}", s.auth(only(s.updateUser, RoleAdmin)))
	mux.Handle("GET "+v+"/audit", s.auth(only(s.listAudit, RoleAdmin, RoleAccountant)))

	// developer API keys (managed from the browser only)
	mux.Handle("GET "+v+"/api-keys", s.auth(sessionOnly(only(s.listAPIKeys, RoleAdmin))))
	mux.Handle("POST "+v+"/api-keys", s.auth(sessionOnly(only(s.createAPIKey, RoleAdmin))))
	mux.Handle("POST "+v+"/api-keys/{id}/revoke", s.auth(sessionOnly(only(s.revokeAPIKey, RoleAdmin))))

	// exchange rates
	mux.Handle("GET "+v+"/companies/{cid}/rates", s.auth(s.listRates))
	mux.Handle("GET "+v+"/companies/{cid}/rates/latest", s.auth(s.latestRates))
	mux.Handle("POST "+v+"/companies/{cid}/rates", s.auth(only(s.upsertRate, write...)))

	// chart of accounts
	mux.Handle("GET "+v+"/companies/{cid}/accounts", s.auth(s.listAccounts))
	mux.Handle("POST "+v+"/companies/{cid}/accounts", s.auth(only(s.createAccount, write...)))
	mux.Handle("PATCH "+v+"/companies/{cid}/accounts/{id}", s.auth(only(s.updateAccount, write...)))
	mux.Handle("GET "+v+"/chart-templates", s.auth(s.listChartTemplates))
	mux.Handle("POST "+v+"/companies/{cid}/accounts/load-template", s.auth(only(s.loadChartTemplate, RoleAdmin, RoleAccountant)))

	// journal
	mux.Handle("GET "+v+"/companies/{cid}/journal", s.auth(s.listEntries))
	mux.Handle("POST "+v+"/companies/{cid}/journal", s.auth(only(s.createEntry, entry...)))
	mux.Handle("GET "+v+"/companies/{cid}/journal/{id}", s.auth(s.getEntry))
	mux.Handle("PUT "+v+"/companies/{cid}/journal/{id}", s.auth(only(s.replaceEntry, entry...)))
	mux.Handle("DELETE "+v+"/companies/{cid}/journal/{id}", s.auth(only(s.deleteEntry, entry...)))
	mux.Handle("POST "+v+"/companies/{cid}/journal/{id}/post", s.auth(only(s.postEntry, write...)))
	mux.Handle("POST "+v+"/companies/{cid}/journal/{id}/reverse", s.auth(only(s.reverseEntry, write...)))

	// reports
	mux.Handle("GET "+v+"/companies/{cid}/reports/trial-balance", s.auth(s.trialBalance))
	mux.Handle("GET "+v+"/companies/{cid}/reports/ledger/{account}", s.auth(s.accountLedger))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, http.StatusNotFound, "not_found") })
	mux.Handle("/", spa(s.WebDir))

	return securityHeaders(jsonOnly(s.Sessions.LoadAndSave(mux)))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Pool.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- principal / auth middleware ----

type Principal struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
	Email      string `json:"email"`
	FullName   string `json:"full_name"`
	Role       string `json:"role"`
	Locale     string `json:"locale"`
	APIKeyID   string `json:"api_key_id,omitempty"`
	Active     bool   `json:"-"`
}

type principalKey struct{}

func principal(r *http.Request) *Principal { return r.Context().Value(principalKey{}).(*Principal) }

func scope(r *http.Request) db.Scope {
	p := principal(r)
	return db.Scope{TenantID: p.TenantID, UserID: p.ID, APIKeyID: p.APIKeyID, IP: clientIP(r)}
}

func (s *Server) loadPrincipal(ctx context.Context, sc db.Scope) (*Principal, error) {
	p := &Principal{}
	err := db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT u.id::text, u.tenant_id::text, t.name, u.email, u.full_name, u.role, u.locale,
			       (u.is_active AND t.is_active)
			FROM users u JOIN tenants t ON t.id = u.tenant_id
			WHERE u.id = $1`, sc.UserID).
			Scan(&p.ID, &p.TenantID, &p.TenantName, &p.Email, &p.FullName, &p.Role, &p.Locale, &p.Active)
	})
	return p, err
}

// auth accepts either a browser session cookie or "Authorization: Bearer pxv_..." API key.
func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			p, ok := s.authAPIKey(ctx, strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), clientIP(r))
			if !ok {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next(w, r.WithContext(context.WithValue(ctx, principalKey{}, p)))
			return
		}
		uid, tid := s.Sessions.GetString(ctx, "uid"), s.Sessions.GetString(ctx, "tid")
		if uid == "" || tid == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		p, err := s.loadPrincipal(ctx, db.Scope{TenantID: tid, UserID: uid, IP: clientIP(r)})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !p.Active) {
			_ = s.Sessions.Destroy(ctx)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			serverErr(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(ctx, principalKey{}, p)))
	})
}

func only(next http.HandlerFunc, roles ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role := principal(r).Role
		for _, allowed := range roles {
			if role == allowed {
				next(w, r)
				return
			}
		}
		writeErr(w, http.StatusForbidden, "forbidden")
	}
}

func sessionOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if principal(r).APIKeyID != "" {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r)
	}
}

// ---- middleware ----

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// jsonOnly rejects state-changing API calls that are not JSON. Browsers cannot
// send a cross-site JSON request without a CORS preflight, so this blocks CSRF.
func jsonOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead &&
			r.Method != http.MethodDelete {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeErr(w, http.StatusUnsupportedMediaType, "bad_request")
				return
			}
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/") &&
			r.Header.Get("Authorization") == "" && r.Header.Get("X-Requested-With") != "paxve" {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	ip := r.Header.Get("CF-Connecting-IP")
	if ip == "" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			ip = strings.TrimSpace(parts[len(parts)-1])
		}
	}
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

// spa serves the built web app, falling back to index.html.
func spa(dir string) http.Handler {
	root := http.Dir(dir)
	files := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if f, err := root.Open(p); err == nil {
			st, _ := f.Stat()
			f.Close()
			if st != nil && !st.IsDir() {
				if strings.HasPrefix(p, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index := filepath.Join(dir, "index.html")
		if _, err := os.Stat(index); err != nil {
			http.Error(w, "web app not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

func serverErr(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeErr(w, http.StatusInternalServerError, "server_error")
}
