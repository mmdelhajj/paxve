package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for goose
	"github.com/pressly/goose/v3"

	"github.com/mmdelhajj/paxve/internal/db/migrations"
)

// LatestVersion is the highest migration number embedded in this binary (e.g. 10 for 00010_x.sql).
func LatestVersion() int64 {
	entries, _ := fs.ReadDir(migrations.FS, ".")
	var max int64
	for _, e := range entries {
		name := e.Name()
		if i := strings.IndexByte(name, '_'); i > 0 && strings.HasSuffix(name, ".sql") {
			if v, err := strconv.ParseInt(name[:i], 10, 64); err == nil && v > max {
				max = v
			}
		}
	}
	return max
}

// Migrate applies pending migrations as the database owner, then sets the
// password of the restricted application role.
func Migrate(ctx context.Context, ownerURL, appPassword string) error {
	sqldb, err := sql.Open("pgx", ownerURL)
	if err != nil {
		return err
	}
	defer sqldb.Close()

	p, err := goose.NewProvider(goose.DialectPostgres, sqldb, migrations.FS)
	if err != nil {
		return fmt.Errorf("goose: %w", err)
	}
	results, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	for _, r := range results {
		slog.Info("migration applied", "file", r.Source.Path, "took", r.Duration.String())
	}

	if appPassword != "" {
		var stmt string
		if err := sqldb.QueryRowContext(ctx,
			`SELECT format('ALTER ROLE paxve_app WITH LOGIN PASSWORD %L', $1::text)`, appPassword,
		).Scan(&stmt); err != nil {
			return err
		}
		if _, err := sqldb.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("set app role password: %w", err)
		}
	}
	return nil
}
