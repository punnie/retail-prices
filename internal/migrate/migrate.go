// Package migrate applies the numbered SQL files in db/migrations against a
// schema_migrations table, in order, exactly once each. That covers the raw
// `products` log (migration 008 adopts it with CREATE TABLE IF NOT EXISTS, so
// existing rows and indexes are never rewritten), the scraper's job records
// and the whole derived schema.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/db/migrations"
	"retail-prices/internal/db"
)

type migration struct {
	version  int
	name     string
	contents string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("reading embedded migrations: %w", err)
	}

	var result []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(e.Name(), "_", 2)
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("migration file %q does not start with a numeric version", e.Name())
		}
		contents, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, migration{version: version, name: e.Name(), contents: string(contents)})
	}

	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	return result, nil
}

func Run(ctx context.Context, args []string) error {
	pool, err := db.Connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := Apply(ctx, pool); err != nil {
		return err
	}
	fmt.Println("up to date")
	return nil
}

// Apply applies every migration not yet recorded in schema_migrations.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	if err := ensureMigrationsTable(ctx, pool); err != nil {
		return err
	}

	applied, err := appliedVersions(ctx, pool)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		fmt.Printf("applying %s\n", m.name)
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, m.contents); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("applying %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func ensureMigrationsTable(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	return err
}

func appliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[int]bool, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}
