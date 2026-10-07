package sqlite

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedCorruptibleStore builds a healthy store at path (via OpenRuntime, so the
// embedded schema is applied), inserts enough rows that real b-tree pages
// exist beyond the header page, and closes it with the WAL side files removed
// so the .sqlite file is self-contained for byte-level damage. It returns the
// page size in bytes.
func seedCorruptibleStore(t *testing.T, ctx context.Context, path string) int {
	t.Helper()
	store, err := OpenRuntime(ctx, path)
	if err != nil {
		t.Fatalf("seedCorruptibleStore: OpenRuntime(%s) = %v", path, err)
	}
	var pageSize int
	if err := store.DB().QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatalf("seedCorruptibleStore: PRAGMA page_size: %v", err)
	}
	owner := "33333333-3333-7333-8333-333333333333"
	if _, err := store.DB().Exec(
		`INSERT INTO users (id, hermes_user_id, email, display_name) VALUES (?, 'qa42', 'qa42@example.com', 'QA-42')`,
		owner); err != nil {
		t.Fatalf("seedCorruptibleStore: insert user: %v", err)
	}
	for i := 0; i < 200; i++ {
		if _, err := store.DB().Exec(
			`INSERT INTO trees (id, owner_id, title, description, metadata) VALUES (?, ?, ?, 'qa42 fixture', '{}')`,
			fmt.Sprintf("55555555-5555-7555-8555-%016x", i), owner, fmt.Sprintf("tree-%d", i)); err != nil {
			t.Fatalf("seedCorruptibleStore: insert tree %d: %v", i, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("seedCorruptibleStore: Close: %v", err)
	}
	for _, side := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + side)
	}
	return pageSize
}

// TestOpenRuntimeRefusesCorruptedDatabase covers the three corruption shapes a
// runtime DB file can be found in: a garbage file, a half-truncated file, and
// a file whose header page is intact but whose interior b-tree pages are
// zeroed. Every shape must produce a refusal (wrapped error naming the file)
// that IsCorruptionRefusal classifies.
//
// The interior-page case is the one that justifies the OpenRuntime integrity
// probe: without the QuickCheck guard the driver's open+ping path succeeds on
// such a file (only page 1 is touched) and boot silently continues against
// damaged data, so this subtest fails with "OpenRuntime(...) = nil error" when
// the guard is removed. The garbage and truncated shapes are refused by the
// driver at open time even without the guard; the test pins that the refusal
// survives and is classified either way.
func TestOpenRuntimeRefusesCorruptedDatabase(t *testing.T) {
	ctx := context.Background()
	base := filepath.Join(t.TempDir(), "seeded.sqlite")
	pageSize := seedCorruptibleStore(t, ctx, base)
	seeded, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	if pageSize <= 0 || len(seeded) < 4*pageSize {
		t.Fatalf("fixture: seeded store is %d bytes (page size %d); need at least 4 pages of real data", len(seeded), pageSize)
	}

	cases := []struct {
		name string
		file func(t *testing.T) string
	}{
		{
			name: "garbage bytes",
			file: func(t *testing.T) string {
				t.Helper()
				path := filepath.Join(t.TempDir(), "garbage.sqlite")
				junk := bytes.Repeat([]byte("this is not a sqlite database, just ascii junk padding\n"), 40)
				if err := os.WriteFile(path, junk, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "half-truncated",
			file: func(t *testing.T) string {
				t.Helper()
				path := filepath.Join(t.TempDir(), "truncated.sqlite")
				if err := os.WriteFile(path, seeded[:len(seeded)/2], 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "zeroed interior page",
			// Damage page 3 (0-indexed): header page 1 stays intact, so the
			// driver's open+ping path still succeeds and only a quick_check
			// can see the damage.
			file: func(t *testing.T) string {
				t.Helper()
				damaged := append([]byte(nil), seeded...)
				lo, hi := 3*pageSize, 4*pageSize
				for i := lo; i < hi && i < len(damaged); i++ {
					damaged[i] = 0x00
				}
				path := filepath.Join(t.TempDir(), "interior.sqlite")
				if err := os.WriteFile(path, damaged, 0o600); err != nil {
					t.Fatal(err)
				}
				// Premise: the damage is real (quick_check flags it) but
				// invisible to the open path. If either assumption breaks,
				// fail the fixture loudly instead of trusting the final
				// assertion below.
				store, err := Open(path)
				if err != nil {
					t.Fatalf("fixture: damaged file refused at open (expected silent open): %v", err)
				}
				defer store.Close()
				rows, err := store.DB().QueryContext(ctx, "PRAGMA quick_check")
				if err != nil {
					t.Fatalf("fixture: quick_check errored (want flagged-but-readable): %v", err)
				}
				var lines []string
				for rows.Next() {
					var line string
					_ = rows.Scan(&line)
					lines = append(lines, line)
				}
				rows.Close()
				if len(lines) != 0 && lines[0] == "ok" {
					t.Fatalf("fixture: zeroing page 3 produced no quick_check findings; damage fixture is inert")
				}
				return path
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.file(t)
			store, err := OpenRuntime(ctx, path)
			if err == nil {
				_ = store.Close()
				t.Fatalf("OpenRuntime(%s) = nil error, want corruption refusal naming the file", path)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("OpenRuntime(%s) error does not name the file: %v", path, err)
			}
			if !IsCorruptionRefusal(err) {
				t.Errorf("IsCorruptionRefusal(OpenRuntime(%s)) = false for %v", path, err)
			}
		})
	}
}

// TestOpenRuntimeRebuildsEmptyStoreCleanly pins the recover side of the
// acceptance: an empty (zero-byte) DB file is not corruption — OpenRuntime
// applies the embedded schema and yields a healthy store at the current
// embedded version, with no corruption refusal.
func TestOpenRuntimeRebuildsEmptyStoreCleanly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenRuntime(ctx, path)
	if err != nil {
		t.Fatalf("OpenRuntime(%s) on a zero-byte file = %v, want a clean rebuild", path, err)
	}
	defer store.Close()
	if IsCorruptionRefusal(err) {
		t.Fatalf("clean rebuild classified as corruption refusal: %v", err)
	}
	version, err := CoreSchemaVersion(ctx, store)
	if err != nil {
		t.Fatalf("CoreSchemaVersion() = %v", err)
	}
	if version != EmbeddedCoreVersion {
		t.Fatalf("CoreSchemaVersion() = %d, want %d (embedded core schema not rebuilt)", version, EmbeddedCoreVersion)
	}
	if err := CheckCoreSchema(ctx, store); err != nil {
		t.Fatalf("CheckCoreSchema() after rebuild = %v", err)
	}
}

// TestOpenRuntimeAcceptsHealthyDatabase guards the guard: a healthy store
// must still open, and QuickCheck on it must pass, so the corruption probe
// cannot false-positive on every legitimate boot.
func TestOpenRuntimeAcceptsHealthyDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "healthy.sqlite")
	seedCorruptibleStore(t, ctx, path)

	store, err := OpenRuntime(ctx, path)
	if err != nil {
		t.Fatalf("OpenRuntime(%s) = %v on a healthy database, want success", path, err)
	}
	defer store.Close()
	if err := store.QuickCheck(ctx); err != nil {
		t.Fatalf("QuickCheck() on a healthy database = %v, want nil", err)
	}
	if IsCorruptionRefusal(err) {
		t.Fatalf("healthy boot classified as corruption refusal: %v", err)
	}
}
