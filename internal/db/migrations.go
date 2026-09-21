package db

import (
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/coding-hermes/hermes-canopy"
)

// MigrateUp runs all pending migrations against the given database URL.
// Recovers from a dirty migration state (dirty=true left by a hard kill
// mid-migration) via the shared repair helper, mirroring MigrateWith.
func MigrateUp(dbURL string) error {
	src, err := iofs.New(canopy.MigrationFiles, "migrations")
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	m, err := migrate.NewWithSourceInstance("iofs", src, dbURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()

	if err := runMigrateWithRepair(m); err != nil {
		return err
	}
	return nil
}
