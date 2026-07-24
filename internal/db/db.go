// Pool + migration runner. The migrations directory ships as
// github.com/totalwindupflightsystems/hermes-canopy/migrations, which
// embed.FS's the SQL files at compile time (see migrations/embed.go).
// No sidecar migrations folder is required at runtime.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for migrate

	// Imported for its side-effect of publishing an embed.FS via FS().
	"github.com/totalwindupflightsystems/hermes-canopy/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrateSource returns an fs.FS rooted at the embedded migrations
// directory. Useful for tests that need to inject custom migration
// directories.
func MigrateSource() fs.FS {
	return migrations.FS()
}

// DB wraps the pgxpool with the repository handles attached.
type DB struct {
	Pool      *pgxpool.Pool
	Nodes     NodeRepo
	Edges     EdgeRepo
	Trees     TreeRepo
	Snapshots SnapshotRepo
	Events    EventRepo
	// Approval system repositories (SPEC-DM-03, SPEC-DM-04).
	Approvals  ApprovalRepo
	AuditLog   AuditRepo
	Users      UserRepo
	Profiles   ProfileRepo
	Members    TreeMemberRepo
	// Transport adapter repositories (SPEC-FTR-04 §4).
	TransportConnections TransportConnectionRepo
	TransportConfigs     TransportConfigRepo
	TransportEvents      TransportEventRepo
	// MLS encryption layer repos (SPE-FTR-03 §4).
	MLSGroups            MLSGroupRepo
	MLSMembers           MLSMemberRepo
	MLSKeyPackages       MLSKeyPackageRepo
	MLSPendingProposals  MLSPendingProposalRepo
	migrated             bool
}

// PoolConfig is the minimal pgxpool configuration. Fields are populated
// by the caller (typically from internal/config).
type PoolConfig struct {
	DSN         string
	MaxConns    int32
	MinConns    int32
	MaxConnIdle string // reserved for future tuning
}

// New constructs a pool, pings the database, wires the repositories,
// and returns a *DB ready for use.
func New(ctx context.Context, cfg PoolConfig) (*DB, error) {
	if cfg.DSN == "" {
		return nil, errors.New("db: empty DSN")
	}
	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("db: parse DSN: %w", err)
	}
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	} else {
		pcfg.MaxConns = 25
	}
	if cfg.MinConns > 0 {
		pcfg.MinConns = cfg.MinConns
	} else {
		pcfg.MinConns = 5
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}

	return &DB{
		Pool:      pool,
		Nodes:     NewPGNodeRepo(pool),
		Edges:     NewPGEdgeRepo(pool),
		Trees:     NewPGTreeRepo(pool),
		Snapshots: NewSnapshotRepo(pool),
		Events:    NewEventRepo(pool),
		Approvals:             NewPGApprovalRepo(pool),
		AuditLog:              NewPGAuditRepo(pool),
		Users:                 NewPGUserRepo(pool),
		Profiles:              NewPGProfileRepo(pool),
		Members:               NewPGTreeMemberRepo(pool),
		TransportConnections:  NewPGTransportConnectionRepo(pool),
		TransportConfigs:      NewPGTransportConfigRepo(pool),
		TransportEvents:       NewPGTransportEventRepo(pool),
		MLSGroups:             NewPGMLSGroupRepo(pool),
		MLSMembers:            NewPGMLSMemberRepo(pool),
		MLSKeyPackages:        NewPGMLSKeyPackageRepo(pool),
		MLSPendingProposals:   NewPGMLSPendingProposalRepo(pool),
	}, nil
}

// Migrate applies every pending up migration from the embedded
// migrations directory. Idempotent: safe to call on every startup.
// Returns nil immediately if the database is already at the latest
// version.
func (db *DB) Migrate(ctx context.Context) error {
	return db.MigrateWith(ctx, MigrateSource(), ".")
}

// MigrateWith runs migrations from the supplied fs.FS rooted at dir.
// Equivalent to Migrate() with the default source; useful for tests.
func (db *DB) MigrateWith(ctx context.Context, src fs.FS, dir string) error {
	if db.migrated {
		return nil
	}
	if dir == "" {
		dir = "."
	}
	dsn := db.dsn()
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("db: open sql: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("db: sql ping: %w", err)
	}

	iofsSrc, err := iofs.New(src, dir)
	if err != nil {
		return fmt.Errorf("db: iofs source: %w", err)
	}
	defer iofsSrc.Close()

	drv, err := postgres.WithInstance(sqlDB, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("db: postgres driver: %w", err)
	}
	defer drv.Close()

	m, err := migrate.NewWithInstance("iofs", iofsSrc, "postgres", drv)
	if err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("db: migrate up: %w", err)
	}
	db.migrated = true
	return nil
}

// Close releases the underlying pool. Always call via defer in main().
func (db *DB) Close() {
	if db == nil || db.Pool == nil {
		return
	}
	db.Pool.Close()
}

// dsn extracts the DSN from the pool config so that golang-migrate can
// obtain it for its database/sql driver. pgxpool does not expose the
// raw connection string directly.
func (db *DB) dsn() string {
	if db.Pool == nil {
		return ""
	}
	return db.Pool.Config().ConnConfig.ConnString()
}
