package session

import (
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// --- GAP-077 snapshot fixtures ----------------------------------------------

// snapshotFileName builds the producer's filename for a stamp.
func snapshotFileName(stamp time.Time, enc SnapshotEncoding) string {
	suffix := ""
	switch enc {
	case SnapshotZstd:
		suffix = ".zst"
	case SnapshotGzip:
		suffix = ".gz"
	}
	return "state_" + stamp.Format(snapshotStampLayout) + ".db" + suffix
}

// writeSnapshot encodes src into dir under the snapshot filename for stamp and
// pins the file mtime to the stamp (which is what the real producer's
// timestamp means).
func writeSnapshot(t *testing.T, dir string, stamp time.Time, enc SnapshotEncoding, src string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	path := filepath.Join(dir, snapshotFileName(stamp, enc))
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	switch enc {
	case SnapshotZstd:
		enc, err := zstd.NewWriter(out)
		if err != nil {
			t.Fatalf("zstd writer: %v", err)
		}
		if _, err := io.Copy(enc, in); err != nil {
			t.Fatalf("zstd copy: %v", err)
		}
		if err := enc.Close(); err != nil {
			t.Fatalf("zstd close: %v", err)
		}
	case SnapshotGzip:
		gz := gzip.NewWriter(out)
		if _, err := io.Copy(gz, in); err != nil {
			t.Fatalf("gzip copy: %v", err)
		}
		if err := gz.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
	default:
		if _, err := io.Copy(out, in); err != nil {
			t.Fatalf("copy: %v", err)
		}
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close snapshot: %v", err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

// hashFile returns the SHA-256 of a file.
func hashFile(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sha256.Sum256(b)
}

// --- Resolution: selection, ordering, boundaries ----------------------------

// TestResolveSnapshotPicksNewestAndIgnoresNoise pins the selection contract:
// only regular files whose name matches the snapshot pattern can be chosen,
// and the newest by embedded stamp wins even when a non-matching name sorts
// after it lexically.
func TestResolveSnapshotPicksNewestAndIgnoresNoise(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	payload := buildFixture(t, nil)

	writeSnapshot(t, dir, now.Add(-24*time.Hour), SnapshotZstd, payload)
	writeSnapshot(t, dir, now.Add(-6*time.Hour), SnapshotGzip, payload)
	newest := writeSnapshot(t, dir, now.Add(-90*time.Minute), SnapshotZstd, payload)

	// Noise that must never be selected: future-dated temp/partial names, a
	// foreign database, a directory wearing a snapshot name, a bare file.
	noise := []string{
		"state_" + now.Add(6*time.Hour).Format(snapshotStampLayout) + ".db.zst.tmp",
		"state_" + now.Add(6*time.Hour).Format(snapshotStampLayout) + ".db.zst.partial",
		"state_" + now.Add(6*time.Hour).Format(snapshotStampLayout) + ".db.zst.1",
		"state_" + now.Add(6*time.Hour).Format(snapshotStampLayout) + ".db.backup",
		"other_" + now.Add(6*time.Hour).Format(snapshotStampLayout) + ".db.zst",
		"state_bogus.db.zst",
		"README",
	}
	for _, name := range noise {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("noise"), 0o644); err != nil {
			t.Fatalf("write noise %s: %v", name, err)
		}
	}
	dirWearingSnapshotName := filepath.Join(dir, "state_"+now.Add(6*time.Hour).Format(snapshotStampLayout)+".db.zst")
	if err := os.Mkdir(dirWearingSnapshotName, 0o755); err != nil {
		t.Fatalf("mkdir noisy dir: %v", err)
	}

	opts := SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		spec, err := ResolveSnapshot(opts)
		if err != nil {
			t.Fatalf("resolve (attempt %d): %v", i, err)
		}
		if spec.Path != newest {
			t.Fatalf("resolved %s, want the newest snapshot %s", spec.Path, newest)
		}
		if spec.Encoding != SnapshotZstd {
			t.Errorf("Encoding = %q, want zstd", spec.Encoding)
		}
		if want := now.Add(-90 * time.Minute); !spec.Stamp.Equal(want) {
			t.Errorf("Stamp = %v, want %v (parsed from the filename in local time)", spec.Stamp, want)
		}
		if spec.Name != filepath.Base(newest) {
			t.Errorf("Name = %q, want %q", spec.Name, filepath.Base(newest))
		}
		if spec.Size <= 0 {
			t.Errorf("Size = %d, want > 0", spec.Size)
		}
		if age := spec.Age(now); age <= 0 || age > 2*time.Hour {
			t.Errorf("Age = %v, want ~90m", age)
		}
	}
}

// TestResolveSnapshotAcceptsLegacyUnderscoreAndPlainNames covers the producer
// variants: the older "_" separator and an uncompressed .db.
func TestResolveSnapshotAcceptsLegacyUnderscoreAndPlainNames(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	stamp := now.Add(-2 * time.Hour)
	path := filepath.Join(dir, "state_"+stamp.Format(snapshotStampLayout)+".db")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write legacy name: %v", err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// A gz snapshot with the same stamp would win on the name tiebreak; use a
	// newer gz one to prove the "gzip is read too" branch.
	gz := writeSnapshot(t, dir, now.Add(-time.Hour), SnapshotGzip, path)

	spec, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if spec.Path != gz || spec.Encoding != SnapshotGzip {
		t.Fatalf("resolved %s (%s), want the gz snapshot %s", spec.Path, spec.Encoding, gz)
	}
	// Drop the gz file: the underscore/plain name is then the only candidate
	// and must still resolve.
	if err := os.Remove(gz); err != nil {
		t.Fatalf("remove gz: %v", err)
	}
	spec, err = ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("resolve legacy: %v", err)
	}
	if spec.Encoding != SnapshotPlain {
		t.Errorf("Encoding = %q, want plain for a bare .db", spec.Encoding)
	}
}

// TestResolveSnapshotRejectsSymlinkCandidates proves selection cannot be
// redirected out of the snapshot directory: a symlink wearing a snapshot name
// pointing at a file elsewhere is skipped, and the directory ends up with no
// candidates rather than resolving to the foreign target.
func TestResolveSnapshotRejectsSymlinkCandidates(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	foreign := filepath.Join(elsewhere, "state.db")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o644); err != nil {
		t.Fatalf("write foreign: %v", err)
	}
	link := filepath.Join(dir, "state_"+time.Now().Format(snapshotStampLayout)+".db")
	if err := os.Symlink(foreign, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge})
	if !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("err = %v, want ErrNoSnapshot (symlink candidate must be skipped)", err)
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("err = %v, want the ignored-symlink note", err)
	}
}

// TestResolveSnapshotErrorsAreLoudAndClassified pins every failure mode: a
// missing directory, an empty directory, a directory with no snapshot names,
// and a stale newest snapshot. None of them degrade into a usable reader.
func TestResolveSnapshotErrorsAreLoudAndClassified(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	t.Run("missing directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist")
		_, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }})
		if !errors.Is(err, ErrSnapshotDir) {
			t.Fatalf("err = %v, want ErrSnapshotDir", err)
		}
		if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "--db") {
			t.Errorf("err = %v, want it to name the directory and the --db override", err)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		dir := t.TempDir()
		_, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }})
		if !errors.Is(err, ErrNoSnapshot) {
			t.Fatalf("err = %v, want ErrNoSnapshot", err)
		}
		if !strings.Contains(err.Error(), "state_<YYYYMMDD>-<HHMMSS>.db") {
			t.Errorf("err = %v, want the expected filename pattern in the message", err)
		}
	})

	t.Run("only unrelated files", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"state.db", "backup.tar.gz", "state_20260918.db"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
		_, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }})
		if !errors.Is(err, ErrNoSnapshot) {
			t.Fatalf("err = %v, want ErrNoSnapshot", err)
		}
		if !strings.Contains(err.Error(), "3 entries") {
			t.Errorf("err = %v, want the entry count for diagnosis", err)
		}
	})

	t.Run("stale newest snapshot", func(t *testing.T) {
		dir := t.TempDir()
		stale := writeSnapshot(t, dir, now.Add(-48*time.Hour), SnapshotZstd, buildFixture(t, nil))
		_, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }})
		if !errors.Is(err, ErrSnapshotStale) {
			t.Fatalf("err = %v, want ErrSnapshotStale", err)
		}
		if !strings.Contains(err.Error(), stale) || !strings.Contains(err.Error(), "48h0m") {
			t.Errorf("err = %v, want it to name %s and its age", err, stale)
		}
	})

	t.Run("no silent fallback to an older snapshot", func(t *testing.T) {
		dir := t.TempDir()
		payload := buildFixture(t, nil)
		writeSnapshot(t, dir, now.Add(-48*time.Hour), SnapshotZstd, payload)
		writeSnapshot(t, dir, now.Add(-72*time.Hour), SnapshotZstd, payload)
		_, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }})
		if !errors.Is(err, ErrSnapshotStale) {
			t.Fatalf("err = %v, want ErrSnapshotStale (never an older file)", err)
		}
	})

	t.Run("age bound disabled", func(t *testing.T) {
		dir := t.TempDir()
		want := writeSnapshot(t, dir, now.Add(-30*24*time.Hour), SnapshotZstd, buildFixture(t, nil))
		spec, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: 0, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatalf("resolve with MaxAge=0: %v", err)
		}
		if spec.Path != want {
			t.Errorf("resolved %s, want %s", spec.Path, want)
		}
	})
}

// TestDefaultSnapshotOptionsTargetsHermesStateBackups pins the documented
// default location and recency bound.
func TestDefaultSnapshotOptionsTargetsHermesStateBackups(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := DefaultSnapshotDir()
	if err != nil {
		t.Fatalf("DefaultSnapshotDir: %v", err)
	}
	if want := filepath.Join(home, ".hermes", "state-backups"); dir != want {
		t.Errorf("DefaultSnapshotDir = %q, want %q", dir, want)
	}
	opts, err := DefaultSnapshotOptions()
	if err != nil {
		t.Fatalf("DefaultSnapshotOptions: %v", err)
	}
	if opts.Dir != dir {
		t.Errorf("opts.Dir = %q, want %q", opts.Dir, dir)
	}
	if opts.MaxAge != DefaultSnapshotMaxAge {
		t.Errorf("opts.MaxAge = %v, want %v", opts.MaxAge, DefaultSnapshotMaxAge)
	}
}

// --- Materialization --------------------------------------------------------

// TestMaterializeRoundTripsAndCleansUp proves a compressed snapshot is turned
// into a byte-identical private read-only copy outside the snapshot directory,
// and that the copy is removed on Close.
func TestMaterializeRoundTripsAndCleansUp(t *testing.T) {
	for _, enc := range []SnapshotEncoding{SnapshotZstd, SnapshotGzip} {
		t.Run(string(enc), func(t *testing.T) {
			now := time.Now().Truncate(time.Second)
			src := buildFixture(t, func(conn *sql.DB) {
				insertSession(t, conn, fixtureSession{id: "sess_snap", source: "cli", title: "Snapshot Session", model: "m", startedAt: 1000})
				insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "sess_snap", role: "user", content: "hello snapshot", ts: 1000})
			})
			wantHash := hashFile(t, src)

			dir := t.TempDir()
			snapPath := writeSnapshot(t, dir, now.Add(-time.Hour), enc, src)
			spec, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			tempRoot := t.TempDir()
			source, err := spec.Materialize(SnapshotOptions{TempDir: tempRoot, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatalf("materialize: %v", err)
			}
			if source.Path == snapPath {
				t.Fatalf("Path = %q, want a private copy distinct from the snapshot", source.Path)
			}
			if !source.Immutable {
				t.Error("Immutable = false for a private static copy; want true (zero-lock reads)")
			}
			if filepath.Dir(filepath.Dir(source.Path)) != tempRoot {
				t.Errorf("copy %q is not under the configured temp root %q", source.Path, tempRoot)
			}
			if !strings.HasPrefix(filepath.Base(source.TempDir), snapshotTempPrefix) {
				t.Errorf("copy dir %q lacks the %q prefix", source.TempDir, snapshotTempPrefix)
			}
			if got := hashFile(t, source.Path); got != wantHash {
				t.Error("decompressed copy is not byte-identical to the source database")
			}
			info, err := os.Stat(source.Path)
			if err != nil {
				t.Fatalf("stat copy: %v", err)
			}
			if perm := info.Mode().Perm(); perm != 0o444 {
				t.Errorf("copy mode = %o, want 0444 (read-only)", perm)
			}

			if err := source.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if _, err := os.Stat(source.TempDir); !os.IsNotExist(err) {
				t.Errorf("temp copy %s survived Close (err=%v)", source.TempDir, err)
			}
			if _, err := os.Stat(snapPath); err != nil {
				t.Errorf("Close removed the snapshot itself: %v", err)
			}
			if err := source.Close(); err != nil {
				t.Errorf("second close = %v, want nil", err)
			}
		})
	}
}

// TestMaterializePlainSnapshotIsReadInPlace proves the plain case does not
// copy (and therefore does not delete anything on Close).
func TestMaterializePlainSnapshotIsReadInPlace(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	path := writeSnapshot(t, dir, now.Add(-time.Hour), SnapshotPlain, buildFixture(t, nil))

	spec, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	tempRoot := t.TempDir()
	source, err := spec.Materialize(SnapshotOptions{TempDir: tempRoot, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if source.Path != path {
		t.Errorf("Path = %q, want the snapshot itself %q (plain snapshots are not copied)", source.Path, path)
	}
	if source.Immutable {
		t.Error("Immutable = true for an in-place snapshot: the snapshot directory is not ours to assume frozen")
	}
	if source.TempDir != "" {
		t.Errorf("TempDir = %q, want empty", source.TempDir)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Close removed the snapshot itself: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// TestMaterializeTornSnapshotFailsLoud proves a truncated archive never
// yields a half-read database and leaves no copy behind.
func TestMaterializeTornSnapshotFailsLoud(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	full := writeSnapshot(t, dir, now.Add(-time.Hour), SnapshotZstd, buildFixture(t, nil))
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if len(data) < 8 {
		t.Fatalf("snapshot unexpectedly small (%d bytes)", len(data))
	}
	if err := os.WriteFile(full, data[:len(data)/2], 0o644); err != nil {
		t.Fatalf("truncate snapshot: %v", err)
	}

	spec, err := ResolveSnapshot(SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	tempRoot := t.TempDir()
	_, err = spec.Materialize(SnapshotOptions{TempDir: tempRoot, Now: func() time.Time { return now }})
	if !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("err = %v, want ErrSnapshotInvalid for a torn snapshot", err)
	}
	if !strings.Contains(err.Error(), full) {
		t.Errorf("err = %v, want it to name the snapshot", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(tempRoot, snapshotTempPrefix+"*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("leftover copies after a failed materialize: %v", leftovers)
	}
}

// TestSweepStaleCopiesOnlyTouchesOurStaleCopies proves the abandoned-copy
// cleanup is bounded to this feature's own temporary namespace.
func TestSweepStaleCopiesOnlyTouchesOurStaleCopies(t *testing.T) {
	root := t.TempDir()
	now := time.Now().Truncate(time.Second)

	staleOurs := filepath.Join(root, snapshotTempPrefix+"20260916-060052-abcd")
	freshOurs := filepath.Join(root, snapshotTempPrefix+"20260918-120005-efgh")
	foreignDir := filepath.Join(root, "someone-elses-work")
	ourFile := filepath.Join(root, snapshotTempPrefix+"not-a-directory")
	for _, dir := range []string{staleOurs, freshOurs, foreignDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(ourFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", ourFile, err)
	}
	setMtime := func(path string, when time.Time) {
		t.Helper()
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
	setMtime(staleOurs, now.Add(-13*time.Hour))
	setMtime(freshOurs, now.Add(-time.Hour))
	setMtime(foreignDir, now.Add(-40*time.Hour))
	setMtime(ourFile, now.Add(-40*time.Hour))

	var notes strings.Builder
	sweepStaleCopies(root, now, func(format string, args ...any) {
		notes.WriteString(strings.TrimSpace(logLine(format, args...)) + "\n")
	})

	if _, err := os.Stat(staleOurs); !os.IsNotExist(err) {
		t.Errorf("stale copy %s was not removed (err=%v)", staleOurs, err)
	}
	for _, keep := range []string{freshOurs, foreignDir, ourFile} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was removed but should have been kept: %v", keep, err)
		}
	}
	if !strings.Contains(notes.String(), staleOurs) {
		t.Errorf("notes = %q, want the removed path", notes.String())
	}
}

// logLine renders a log-style line for the sweep note assertion.
func logLine(format string, args ...any) string {
	var b strings.Builder
	log.New(&b, "", 0).Printf(format, args...)
	return b.String()
}

// TestSnapshotURIEncodesOptionCharacters proves a path cannot smuggle extra
// SQLite URI options (or a different file) into the attach string.
func TestSnapshotURIEncodesOptionCharacters(t *testing.T) {
	got := SnapshotURI("/tmp/a b%c?d#e.db")
	want := "file:/tmp/a%20b%25c%3Fd%23e.db"
	if got != want {
		t.Fatalf("SnapshotURI = %q, want %q", got, want)
	}
	if strings.ContainsAny(strings.TrimPrefix(got, "file:"), "?#") {
		t.Errorf("SnapshotURI = %q leaks a URI delimiter", got)
	}
	if plain := SnapshotURI("/tmp/plain.db"); plain != "file:/tmp/plain.db" {
		t.Errorf("SnapshotURI = %q, want it unchanged for a plain path", plain)
	}
}
