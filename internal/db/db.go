// Package db provides the Postgres connection used by every retail subcommand.
package db

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// URL returns the database connection string: DATABASE_URL (set by the nix
// devShell to the project-local Postgres socket), or failing that
// PRICE_SCRAPER_DB_CONNECTION_STRING, which the standalone price-scraper used
// and existing environment files may still set. It fails with a clear
// message if neither is set, since every subcommand needs it.
func URL() (string, error) {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url, nil
	}
	if url := os.Getenv("PRICE_SCRAPER_DB_CONNECTION_STRING"); url != "" {
		return url, nil
	}
	return "", fmt.Errorf("DATABASE_URL is not set; run inside `nix develop` (or export it yourself)")
}

// Connect opens a pool against URL().
func Connect(ctx context.Context) (*pgxpool.Pool, error) {
	url, err := URL()
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}

	return pool, nil
}
