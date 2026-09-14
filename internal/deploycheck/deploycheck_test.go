package deploycheck

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// TestHeadEmbedsLatestMigration is the GAP-069 deploy-parity regression: the
// max migration version THIS checkout would embed (derived from the
// migrations/*.up.sql files on disk) must equal the max embedded version the
// Go-compiled embed reports (db.EmbeddedMaxVersion, the same function the
// canopyd stale-build guard uses). If a future PR lands a migration without
// it being picked up by the embed — or the embed drifts from the tree — this
// fails with the offending file named.
//
// It is static (no database) and cheap, and runs in the normal non-handler
// go test sweep.
func TestHeadEmbedsLatestMigration(t *testing.T) {
	root, err := RepoRootFrom(".")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	treeMax, err := MaxMigrationFileVersion(filepath.Join(root, "migrations"))
	if err != nil {
		t.Fatalf("tree max version: %v", err)
	}
	embeddedMax, err := db.EmbeddedMaxVersion()
	if err != nil {
		t.Fatalf("embedded max version: %v", err)
	}
	if embeddedMax != treeMax {
		files, _ := sortedMigrationVersions(filepath.Join(root, "migrations"))
		t.Fatalf("deploy-vs-schema parity broken (GAP-069): migrations tree max = %d, embedded max = %d — a migration landed without being embedded (or the embed drifted). Migration files seen: %v",
			treeMax, embeddedMax, files)
	}
	if embeddedMax <= 0 {
		t.Fatalf("embedded max version must be positive, got %d", embeddedMax)
	}
}

// TestMaxMigrationFileVersion drives the tree parser against a synthetic
// migrations dir, including the error paths (missing dir, no migrations).
func TestMaxMigrationFileVersion(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := MaxMigrationFileVersion(dir); err == nil {
		t.Fatal("empty dir must error, got nil")
	}

	write("000001_a.up.sql")
	write("000002_b.up.sql")
	write("000046_viewer_config_overrides.up.sql")
	write("000046_viewer_config_overrides.down.sql") // down files never count
	write("not-a-number.up.sql")                     // unparseable, skipped

	got, err := MaxMigrationFileVersion(dir)
	if err != nil {
		t.Fatalf("MaxMigrationFileVersion: %v", err)
	}
	if got != 46 {
		t.Fatalf("MaxMigrationFileVersion = %d, want 46", got)
	}
}

// TestMaxMigrationFileVersion_MissingDir proves the missing-directory error
// instead of a silent zero.
func TestMaxMigrationFileVersion_MissingDir(t *testing.T) {
	if _, err := MaxMigrationFileVersion(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing dir must error, got nil")
	}
}

// TestCompareVersions pins the classification semantics shared with the
// runtime stale-build guard and --check-schema.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		embedded, other int64
		want            string
	}{
		{42, 46, "STALE"},
		{46, 46, "EQUAL"},
		{47, 46, "AHEAD"},
		{0, 0, "EQUAL"},
	}
	for _, c := range cases {
		if got := CompareVersions(c.embedded, c.other); got != c.want {
			t.Errorf("CompareVersions(%d, %d) = %q, want %q", c.embedded, c.other, got, c.want)
		}
	}
}

// TestRepoRootFrom walks up from a nested temp dir to find one holding a
// migrations/ folder.
func TestRepoRootFrom(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(base, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := RepoRootFrom(nested)
	if err != nil {
		t.Fatalf("RepoRootFrom: %v", err)
	}
	if got, _ = filepath.Abs(got); got != mustAbs(t, base) {
		t.Fatalf("RepoRootFrom = %q, want %q", got, mustAbs(t, base))
	}

	// No migrations dir anywhere above → error, not a wrong root.
	empty := filepath.Join(t.TempDir(), "x", "y")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := RepoRootFrom(empty); err == nil {
		t.Fatal("RepoRootFrom without a migrations dir above must error")
	}
}

// sortedMigrationVersions is used by tests to show the discovered versions
// in error messages (deterministic diagnostics beat "mismatch" alone).
func sortedMigrationVersions(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var vs []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			vs = append(vs, e.Name())
		}
	}
	sort.Strings(vs)
	return vs, nil
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestVersionParsingSanity keeps the string→int64 conversion honest for the
// zero-padded forms actually used in migrations/.
func TestVersionParsingSanity(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
	}{
		{"000046", 46},
		{"1", 1},
		{"000001", 1},
	} {
		v, err := strconv.ParseInt(c.in, 10, 64)
		if err != nil || v != c.want {
			t.Fatalf("ParseInt(%q) = %d, %v; want %d", c.in, v, err, c.want)
		}
	}
}
