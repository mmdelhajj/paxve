// Command paxve runs the Paxve server and admin tasks.
//
//	paxve serve          start the web server (runs migrations first if AUTO_MIGRATE=true)
//	paxve migrate        apply database migrations
//	paxve create-tenant  create an organisation with its first admin user
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmdelhajj/paxve/internal/auth"
	"github.com/mmdelhajj/paxve/internal/db"
	"github.com/mmdelhajj/paxve/internal/httpapi"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx)
	case "migrate":
		err = db.Migrate(ctx, mustEnv("MIGRATE_DATABASE_URL"), os.Getenv("APP_DB_PASSWORD"))
	case "create-tenant":
		err = createTenant(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: paxve serve | migrate | create-tenant -name NAME -slug SLUG -email EMAIL -admin NAME")
	os.Exit(2)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("missing environment variable", "key", key)
		os.Exit(2)
	}
	return v
}

func serve(ctx context.Context) error {
	if env("AUTO_MIGRATE", "false") == "true" {
		if err := db.Migrate(ctx, mustEnv("MIGRATE_DATABASE_URL"), os.Getenv("APP_DB_PASSWORD")); err != nil {
			return err
		}
	}

	pool, err := pgxpool.New(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database: %w", err)
	}

	// Small owner-role pool: only for self sign-up and the platform owner console.
	ownerCfg, err := pgxpool.ParseConfig(mustEnv("MIGRATE_DATABASE_URL"))
	if err != nil {
		return err
	}
	ownerCfg.MaxConns = 3
	ownerPool, err := pgxpool.NewWithConfig(ctx, ownerCfg)
	if err != nil {
		return err
	}
	defer ownerPool.Close()

	sm := scs.New()
	sm.Store = pgxstore.New(pool)
	sm.Lifetime = 12 * time.Hour
	sm.IdleTimeout = 2 * time.Hour
	sm.Cookie.Name = "paxve_session"
	sm.Cookie.HttpOnly = true
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Secure = env("COOKIE_SECURE", "true") == "true"

	addr := env("ADDR", ":8080")
	hs := &http.Server{
		Addr:              addr,
		Handler:           httpapi.New(pool, ownerPool, sm, env("WEB_DIR", "web/dist"), strings.Split(os.Getenv("PLATFORM_ADMINS"), ",")).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		errc <- hs.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return hs.Shutdown(shutdownCtx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

func createTenant(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("create-tenant", flag.ExitOnError)
	name := fs.String("name", "", "organisation name")
	slug := fs.String("slug", "", "short id, e.g. my-company")
	email := fs.String("email", "", "admin email")
	admin := fs.String("admin", "", "admin full name")
	companyEn := fs.String("company", "", "first company name in English (default: organisation name)")
	companyAr := fs.String("company-ar", "", "first company name in Arabic")
	_ = fs.Parse(args)

	*email = strings.ToLower(strings.TrimSpace(*email))
	if *name == "" || !slugRe.MatchString(*slug) || !strings.Contains(*email, "@") || *admin == "" {
		fs.Usage()
		os.Exit(2)
	}
	if *companyEn == "" {
		*companyEn = *name
	}

	if err := db.Migrate(ctx, mustEnv("MIGRATE_DATABASE_URL"), os.Getenv("APP_DB_PASSWORD")); err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, mustEnv("MIGRATE_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	password := auth.RandomPassword()
	hash, err := auth.Hash(password)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var tid string
		if err := tx.QueryRow(ctx,
			`INSERT INTO tenants (name, slug) VALUES ($1, $2) RETURNING id::text`, *name, *slug).Scan(&tid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO companies (tenant_id, name_en, name_ar) VALUES ($1, $2, $3)`, tid, *companyEn, *companyAr); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO users (tenant_id, email, full_name, password_hash, role, locale) VALUES ($1, $2, $3, $4, 'admin', 'ar')`,
			tid, *email, *admin, hash)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Printf("Organisation %q created.\n  login:    %s\n  password: %s\n(This password is shown only once. Change it after the first login.)\n",
		*name, *email, password)
	return nil
}
