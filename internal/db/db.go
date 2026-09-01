// Pool + migration runner. The migrations directory ships as
// github.com/coding-hermes/hermes-canopy/migrations, which
// embed.FS's the SQL files at compile time (see migrations/embed.go).
// No sidecar migrations folder is required at runtime.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for migrate

	// Imported for its side-effect of publishing an embed.FS via FS().
	"github.com/coding-hermes/hermes-canopy/migrations"

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
	Approvals ApprovalRepo
	AuditLog  AuditRepo
	Users     UserRepo
	Profiles  ProfileRepo
	Members   TreeMemberRepo
	// Transport adapter repositories (SPEC-FTR-04 §4).
	TransportConnections TransportConnectionRepo
	TransportConfigs     TransportConfigRepo
	TransportEvents      TransportEventRepo
	// MLS encryption layer repos (SPE-FTR-03 §4).
	MLSGroups           MLSGroupRepo
	MLSMembers          MLSMemberRepo
	MLSKeyPackages      MLSKeyPackageRepo
	MLSPendingProposals MLSPendingProposalRepo
	// Topic system repos (SPEC-TM-01 §4, migration 000020).
	Topics       TopicRepo
	TopicMembers TopicMemberRepo
	migrated     bool
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
		Pool:                 pool,
		Nodes:                NewPGNodeRepo(pool),
		Edges:                NewPGEdgeRepo(pool),
		Trees:                NewPGTreeRepo(pool),
		Snapshots:            NewSnapshotRepo(pool),
		Events:               NewEventRepo(pool),
		Approvals:            NewPGApprovalRepo(pool),
		AuditLog:             NewPGAuditRepo(pool),
		Users:                NewPGUserRepo(pool),
		Profiles:             NewPGProfileRepo(pool),
		Members:              NewPGTreeMemberRepo(pool),
		TransportConnections: NewPGTransportConnectionRepo(pool),
		TransportConfigs:     NewPGTransportConfigRepo(pool),
		TransportEvents:      NewPGTransportEventRepo(pool),
		MLSGroups:            NewPGMLSGroupRepo(pool),
		MLSMembers:           NewPGMLSMemberRepo(pool),
		MLSKeyPackages:       NewPGMLSKeyPackageRepo(pool),
		MLSPendingProposals:  NewPGMLSPendingProposalRepo(pool),
		Topics:               NewPGTopicRepo(pool),
		TopicMembers:         NewPGTopicMemberRepo(pool),
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
	defer func() { _ = iofsSrc.Close() }()

	drv, err := postgres.WithInstance(sqlDB, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("db: postgres driver: %w", err)
	}
	defer func() { _ = drv.Close() }()

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

// EmbeddedMaxVersion returns the highest migration version compiled into
// this binary (parsed from the embedded migrations FS). Used by the
// stale-build guard in main(): a database schema NEWER than this value
// means the running binary predates the schema and must be rebuilt.
func EmbeddedMaxVersion() (int64, error) {
	entries, err := fs.ReadDir(MigrateSource(), ".")
	if err != nil {
		return 0, fmt.Errorf("db: read embedded migrations: %w", err)
	}
	var max int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		base := strings.TrimSuffix(name, ".up.sql")
		if i := strings.IndexByte(base, '_'); i > 0 {
			base = base[:i]
		}
		v, err := strconv.ParseInt(base, 10, 64)
		if err != nil {
			continue
		}
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return 0, fmt.Errorf("db: no embedded migrations found")
	}
	return max, nil
}

// SchemaVersion returns the database's current schema_migrations version
// (0 when the table does not exist yet, i.e. before the first migration).
func (db *DB) SchemaVersion(ctx context.Context) (int64, error) {
	if db == nil || db.Pool == nil {
		return 0, fmt.Errorf("db: pool not initialized")
	}
	var v int64
	err := db.Pool.QueryRow(ctx,
		`SELECT COALESCE((SELECT max(version) FROM schema_migrations), 0)`).Scan(&v)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return 0, nil // relation does not exist: fresh database
		}
		return 0, fmt.Errorf("db: schema version: %w", err)
	}
	return v, nil
}

// Close releases the underlying pool. Always call via defer in main().
func (db *DB) Close() {
	if db == nil || db.Pool == nil {
		return
	}
	db.Pool.Close()
}

// IsConnectError reports whether err represents a network-level connection
// failure (host unreachable, port closed, connection refused). It is used by
// main() to print a friendly startup message guiding the user to start
// PostgreSQL. Non-connection errors (bad migrations, auth failures, etc.)
// return false and stay on the existing log.Fatal path.
func IsConnectError(err error) bool {
	if err == nil {
		return false
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		// ConnectError wraps the underlying dial/network error.
		var netErr *net.OpError
		return errors.As(err, &netErr)
	}
	// Also catch bare net.OpError (e.g. database/sql path in Migrate).
	var netErr *net.OpError
	return errors.As(err, &netErr)
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
