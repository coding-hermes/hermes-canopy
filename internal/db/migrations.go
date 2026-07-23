package db

import (
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/totalwindupflightsystems/hermes-canopy"
)

// MigrateUp runs all pending migrations against the given database URL.
func MigrateUp(dbURL string) error {
	src, err := iofs.New(canopy.MigrationFiles, "migrations")
	if err != nil {
		return err
	}
	defer src.Close()

	m, err := migrate.NewWithSourceInstance("iofs", src, dbURL)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}
