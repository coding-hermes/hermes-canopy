// Package canopy contains top-level assets for the Hermes Canopy server.
package canopy

import "embed"

//go:embed migrations/*.sql
var MigrationFiles embed.FS
