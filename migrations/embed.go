// Package migrations embeds the SQL migration files so the canopyd
// binary ships with its schema. Consumers (cmd/canopyd, internal/db)
// obtain an iofs-compatible fs.FS via the FS() function.
//
// The directory contains both .sql artefacts (consumed by
// golang-migrate at runtime) and this single Go file (compiled into
// the binary). See README.md for ordering and design rationale.
package migrations

import "embed"

//go:embed *.sql
var fs embed.FS

// FS returns the embedded filesystem rooted at migrations/.
func FS() embed.FS { return fs }
