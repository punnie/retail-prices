// Package testdb gives database-backed tests a freshly migrated, private
// schema. Tests using it are skipped unless TEST_DATABASE_URL points at a
// Postgres database the tests may create schemas in.
package testdb

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/internal/migrate"
)

// New returns a pool whose search_path is a new schema with every migration
// applied, dropped again when the test ends.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	// pg_trgm lives in public so its operator classes resolve from any
	// test schema; migration 001's CREATE EXTENSION IF NOT EXISTS is then a
	// no-op.
	if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("dropping %s: %v", schema, err)
		}
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ", public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if err := migrate.Apply(ctx, pool); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return pool
}
