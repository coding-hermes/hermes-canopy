// Package db provides the PostgreSQL data layer for Canopy: pool
// management, repository wiring, and migration execution.
//
// Migrations are embedded via the canopy package (embed.go at the
// project root) and applied through MigrateUp. Down migrations exist
// on disk for manual rollback via `make migrate-down` but are not
// exposed through this package.
package db

import (
	"context"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for migrate
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps the pgxpool with the repository handles attached.
type DB struct {
	Pool  *pgxpool.Pool
	Nodes NodeRepo
	Edges EdgeRepo
	Trees TreeRepo
}

// New creates a DB, migrates, and wires repositories.
func New(ctx context.Context, dbURL string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("db: pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}

	return &DB{
		Pool:  pool,
		Nodes: NewPGNodeRepo(pool),
		Edges: NewPGEdgeRepo(pool),
		Trees: NewPGTreeRepo(pool),
	}, nil
}

// Close shuts down the connection pool.
func (db *DB) Close() {
	db.Pool.Close()
}
