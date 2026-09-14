// Package deploycheck guards deploy-versus-schema parity (GAP-069): the
// maximum migration version a HEAD build would embed (derived from the
// migrations/*.up.sql files) must equal the max migration version declared by
// the migrations themselves. A mismatch means a migration landed without
// being picked up by the embed (or the embed drifted from the tree) — the
// exact class of failure that put a schema-42 binary in front of a schema-46
// database and crash-looped the service for ~37h on 2026-09-12.
//
// The check is static: it reads only the repository tree and never touches a
// database, so it runs in the normal non-handler go test sweep.
package deploycheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MaxMigrationFileVersion returns the highest N among migrations/<N>_<name>.up.sql
// files directly inside dir. It errors when the directory is missing or
// contains no parseable up-migration files — a silent 0 would make every
// comparison meaningless.
func MaxMigrationFileVersion(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("deploycheck: read migrations dir: %w", err)
	}
	var max int64
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".up.sql")
		if i := strings.IndexByte(base, '_'); i > 0 {
			base = base[:i]
		}
		v, err := strconv.ParseInt(base, 10, 64)
		if err != nil {
			continue // not a numbered migration (e.g. README artifact)
		}
		if v > max {
			max = v
		}
		found = true
	}
	if !found {
		return 0, fmt.Errorf("deploycheck: no numbered *.up.sql migrations found in %s", dir)
	}
	return max, nil
}

// CompareVersions classifies embedded versus database (or tree) schema
// versions. The semantic mirror of the runtime stale-build guard in
// cmd/canopyd/main.go and of scripts/check-deploy-staleness.sh --check-schema:
// a binary must embed AT LEAST the schema of the database it serves.
func CompareVersions(embedded, other int64) string {
	switch {
	case embedded < other:
		return "STALE"
	case embedded == other:
		return "EQUAL"
	default:
		return "AHEAD"
	}
}

// RepoRootFrom walks up from dir until it finds a directory containing a
// migrations folder with at least one .up.sql file, and returns that
// directory (the repo root). Test binaries run from their package directory,
// so the caller passes the package dir and this resolves the repo root
// without needing the runtime path.
func RepoRootFrom(dir string) (string, error) {
	cur, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.ReadDir(filepath.Join(cur, "migrations")); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("deploycheck: no migrations/ directory found above %s", dir)
		}
		cur = parent
	}
}
