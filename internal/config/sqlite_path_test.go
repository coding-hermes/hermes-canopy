package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultSQLitePathShape pins the exported SQLite runtime location helper
// (QA-HERMES-CANOPY-42): the conventional path is <home>/.canopy/canopy.sqlite
// and it never embeds CANOPY_SQLITE_PATH, so Default() stays a stable baseline
// and FromEnv remains the only override path. The battery's corruption cell
// resolves the live DB location through this function (or CANOPY_SQLITE_PATH),
// so its shape is contract.
func TestDefaultSQLitePathShape(t *testing.T) {
	path := DefaultSQLitePath()
	if filepath.Base(path) != "canopy.sqlite" {
		t.Fatalf("DefaultSQLitePath() = %q, want basename canopy.sqlite", path)
	}
	if !strings.HasSuffix(filepath.ToSlash(path), "/.canopy/canopy.sqlite") {
		t.Fatalf("DefaultSQLitePath() = %q, want it below a .canopy/ data directory", path)
	}
	if got := Default(); got.SQLitePath != path {
		t.Fatalf("Default().SQLitePath = %q, want DefaultSQLitePath() = %q", got.SQLitePath, path)
	}
}
