// Package migrations embeds the SQL migration files so the canopyd
// binary ships with its schema. Consumers (cmd/canopyd, internal/db)
// obtain an iofs-compatible fs.FS via the FS() function.
//
// The directory contains both .sql artefacts (consumed by
// golang-migrate at runtime) and this single Go file (compiled into
// the binary). See README.md for ordering and design rationale.
//
// sqlite/ carries the SQLite translation of the same migration numbers
// (GAP-076 wave 1). It is a subdirectory, so the "*.sql" pattern above
// still matches exactly the PostgreSQL files at the root — the two sets
// never mix in one filesystem view.
package migrations

import (
	"embed"
	iofs "io/fs"
)

//go:embed *.sql
var fs embed.FS

//go:embed sqlite/*.sql
var sqliteFS embed.FS

// FS returns the embedded filesystem rooted at migrations/.
func FS() embed.FS { return fs }

// SQLiteFS returns the embedded SQLite migration filesystem, rooted at sqlite/ so that
// names match FS()'s shape ("000002_trees.up.sql"). The sub-filesystem cannot fail: the
// go:embed directive above is unsatisfiable at build time unless sqlite/ contains
// matching files.
func SQLiteFS() iofs.FS {
	sub, err := iofs.Sub(sqliteFS, "sqlite")
	if err != nil {
		panic("migrations: embedded sqlite/ sub-filesystem is missing: " + err.Error())
	}
	return sub
}
