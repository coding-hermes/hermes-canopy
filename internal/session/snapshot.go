package session

// GAP-077 — Hermes state snapshot source.
//
// The Hermes gateway keeps ~/.hermes/state.db open for writes at all times.
// Reading the LIVE file (even read-only) couples Canopy to the gateway's
// locks and to whatever half-written state is in flight. Hermes already
// produces periodic binary snapshots of that database, so Canopy reads the
// snapshot instead:
//
//	~/.hermes/state-backups/state_<YYYYmmdd>-<HHMMSS>.db.zst   (zstd, 6-hourly)
//	~/.hermes/state-backups/state_<YYYYmmdd>-<HHMMSS>.db.gz    (gzip, legacy)
//	~/.hermes/state-backups/state_<YYYYmmdd>-<HHMMSS>.db       (plain, if ever)
//
// Contract:
//
//   - SELECTION is a pure read of that one directory: regular files whose
//     name matches state_<8 digits>[-_]<6 digits>.db[.zst|.gz] exactly.
//     Symlinks, directories, temp files and every other name are ignored, so
//     the chosen file always lives inside the configured snapshot directory —
//     no traversal, no globbing outside the directory, no "nearest random
//     state.db" fallback.
//   - ORDER is the newest snapshot by its embedded filename stamp (the
//     producer writes `date +%Y%m%d-%H%M%S` host-local time, so the stamp is
//     parsed in time.Local), then by file modification time, then by name —
//     fully deterministic for a given directory listing.
//   - RECENCY is bounded: a snapshot older than the configured maximum age
//     (DefaultSnapshotMaxAge = 24h, four 6-hourly cycles) is REJECTED with
//     ErrSnapshotStale rather than silently imported. A caller that wants
//     ancient data must say so (MaxAge = 0 disables the bound).
//   - FAILURE IS LOUD. A missing directory, no matching file, or a stale
//     newest file is an error. Canopy never "falls back" to the live
//     ~/.hermes/state.db; the only way to read a live database is the
//     explicit --db override, which stays read-only.
//   - COMPRESSED snapshots are decompressed into a private temporary copy
//     (mode 0444) that nothing else can write; the copy is removed when the
//     reader closes. Plain .db snapshots are read in place, read-only. The
//     live state.db is never opened, so no Canopy read can ever take a lock
//     on (or write to) the gateway's file.
//
// The materialized copy is the file Canopy ATTACHes (see OpenSnapshotReader):
// ATTACH carries the read-only URI options, which is what makes the
// read-only-ness of the attached database provable rather than promised.

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	// DefaultSnapshotMaxAge is how old the newest snapshot may be before the
	// snapshot source refuses to use it. Snapshots are produced every 6
	// hours, so 24h tolerates three consecutive failed backup cycles while
	// still refusing to import arbitrarily old data.
	DefaultSnapshotMaxAge = 24 * time.Hour

	// snapshotTempPrefix names the private temporary directories the reader
	// materializes compressed snapshots into. The prefix is how stale copies
	// from a killed import are recognized (and only ours are ever removed).
	snapshotTempPrefix = "canopy-snapshot-"

	// staleCopyMaxAge is how old an abandoned temporary copy must be before
	// a later import removes it.
	staleCopyMaxAge = 12 * time.Hour
)

// SnapshotEncoding describes how a snapshot file is stored on disk.
type SnapshotEncoding string

const (
	// SnapshotPlain is an uncompressed SQLite database file.
	SnapshotPlain SnapshotEncoding = "plain"
	// SnapshotZstd is a zstd-compressed SQLite database file (current producer).
	SnapshotZstd SnapshotEncoding = "zstd"
	// SnapshotGzip is a gzip-compressed SQLite database file (legacy producer).
	SnapshotGzip SnapshotEncoding = "gzip"
)

// snapshotNamePattern is the complete set of filenames that count as Hermes
// state snapshots: state_<YYYYmmdd><sep><HHMMSS>.db with an optional
// .zst/.gz suffix. Both separators appear in the wild (the current producer
// uses "-", an older script used "_").
var snapshotNamePattern = regexp.MustCompile(`^state_(\d{8})[-_](\d{6})\.db(\.zst|\.gz)?$`)

// snapshotStampLayout is the layout of the stamp embedded in the filename.
const snapshotStampLayout = "20060102-150405"

// Sentinel errors for the snapshot source. They are wrapped (never replaced)
// so callers can classify a failure with errors.Is while still printing the
// path/age detail.
var (
	// ErrSnapshotDir means the snapshot directory does not exist (or is not
	// readable). Canopy does not create it and does not substitute another
	// directory.
	ErrSnapshotDir = errors.New("session: snapshot directory unavailable")
	// ErrNoSnapshot means the directory exists but holds no file matching the
	// snapshot name pattern.
	ErrNoSnapshot = errors.New("session: no Hermes state snapshot found")
	// ErrSnapshotStale means the newest matching snapshot is older than the
	// allowed age.
	ErrSnapshotStale = errors.New("session: newest Hermes state snapshot is too old")
	// ErrSnapshotInvalid means the selected snapshot could not be turned into
	// a readable SQLite database (torn write, truncated archive, not a DB).
	ErrSnapshotInvalid = errors.New("session: snapshot is not a readable SQLite database")
)

// SnapshotSpec identifies one resolved snapshot file. Stamps are the snapshot
// producer's local wall-clock label (parsed in time.Local); ModTime is the
// file's own timestamp and is used as the tiebreak.
type SnapshotSpec struct {
	Path     string           // absolute path to the snapshot file
	Name     string           // base name
	Stamp    time.Time        // from the filename, host-local
	ModTime  time.Time        // file modification time
	Size     int64            // file size in bytes
	Encoding SnapshotEncoding // plain / zstd / gzip
}

// Age reports how old the snapshot is relative to now, using the stamp
// embedded in its filename.
func (s SnapshotSpec) Age(now time.Time) time.Duration { return now.Sub(s.Stamp) }

// SnapshotOptions configures snapshot resolution and materialization.
type SnapshotOptions struct {
	// Dir is the snapshot directory. Empty means DefaultSnapshotDir()
	// (~/.hermes/state-backups).
	Dir string
	// MaxAge rejects a newest snapshot older than this. Zero disables the
	// age bound. Zero value of the struct therefore means "24h" only when
	// DefaultSnapshotOptions is used; pass DefaultSnapshotMaxAge explicitly
	// to get the bounded behaviour with a hand-built struct.
	MaxAge time.Duration
	// TempDir is where a compressed snapshot is decompressed. Empty means
	// os.TempDir().
	TempDir string
	// Now overrides the clock (tests).
	Now func() time.Time
	// Warn receives non-fatal notes (removing an abandoned temp copy).
	Warn *log.Logger
}

// DefaultSnapshotDir returns the directory Hermes writes its state snapshots
// into: $HOME/.hermes/state-backups.
func DefaultSnapshotDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("session: snapshot dir: %w", err)
	}
	return filepath.Join(home, ".hermes", "state-backups"), nil
}

// DefaultSnapshotOptions returns the production source configuration:
// $HOME/.hermes/state-backups with a 24h age bound.
func DefaultSnapshotOptions() (SnapshotOptions, error) {
	dir, err := DefaultSnapshotDir()
	if err != nil {
		return SnapshotOptions{}, err
	}
	return SnapshotOptions{Dir: dir, MaxAge: DefaultSnapshotMaxAge}, nil
}

// Now returns the effective clock for these options.
func (o SnapshotOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// resolveDir returns the effective snapshot directory.
func (o SnapshotOptions) resolveDir() (string, error) {
	if o.Dir != "" {
		return o.Dir, nil
	}
	return DefaultSnapshotDir()
}

// warnf emits a snapshot-source note when a logger is configured.
func (o SnapshotOptions) warnf(format string, args ...any) {
	if o.Warn != nil {
		o.Warn.Printf(format, args...)
	}
}

// DescribeSnapshot parses a snapshot filename. ok is false when the name is
// not a snapshot name at all, so callers can report "the directory holds N
// files, none of them snapshots" instead of a bare "nothing found".
func DescribeSnapshot(path string, info fs.FileInfo) (SnapshotSpec, bool) {
	base := filepath.Base(path)
	m := snapshotNamePattern.FindStringSubmatch(base)
	if m == nil {
		return SnapshotSpec{}, false
	}
	stamp, err := time.ParseInLocation(snapshotStampLayout, m[1]+"-"+m[2], time.Local)
	if err != nil {
		return SnapshotSpec{}, false
	}
	spec := SnapshotSpec{
		Path:     path,
		Name:     base,
		Stamp:    stamp,
		Encoding: SnapshotPlain,
	}
	if info != nil {
		spec.ModTime = info.ModTime()
		spec.Size = info.Size()
	}
	switch m[3] {
	case ".zst":
		spec.Encoding = SnapshotZstd
	case ".gz":
		spec.Encoding = SnapshotGzip
	}
	return spec, true
}

// ResolveSnapshot selects the newest usable snapshot in opts.Dir.
//
// It reads only that directory: entries that are not regular files (symlinks
// included) and names that do not match snapshotNamePattern are skipped. The
// newest candidate by (Stamp, ModTime, Name) descending is returned; when its
// age exceeds opts.MaxAge the call fails with ErrSnapshotStale naming the file
// and its age — it never quietly returns an older snapshot instead.
func ResolveSnapshot(opts SnapshotOptions) (SnapshotSpec, error) {
	dir, err := opts.resolveDir()
	if err != nil {
		return SnapshotSpec{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SnapshotSpec{}, fmt.Errorf("%w: %s does not exist (use --db to read a specific file read-only)", ErrSnapshotDir, dir)
		}
		return SnapshotSpec{}, fmt.Errorf("%w: %s: %w", ErrSnapshotDir, dir, err)
	}

	var candidates []SnapshotSpec
	var skipped []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() {
			if snapshotNamePattern.MatchString(name) {
				skipped = append(skipped, name+" (not a regular file)")
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		spec, ok := DescribeSnapshot(filepath.Join(dir, name), info)
		if !ok {
			continue
		}
		candidates = append(candidates, spec)
	}
	if len(candidates) == 0 {
		detail := fmt.Sprintf("%d entries, none named state_<YYYYMMDD>-<HHMMSS>.db[.zst|.gz]", len(entries))
		if len(skipped) > 0 {
			detail += "; ignored: " + strings.Join(skipped, ", ")
		}
		return SnapshotSpec{}, fmt.Errorf("%w in %s (%s)", ErrNoSnapshot, dir, detail)
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.Stamp.Equal(b.Stamp) {
			return a.Stamp.After(b.Stamp)
		}
		if !a.ModTime.Equal(b.ModTime) {
			return a.ModTime.After(b.ModTime)
		}
		return a.Name > b.Name
	})
	newest := candidates[0]
	if opts.MaxAge > 0 {
		if age := newest.Age(opts.now()); age > opts.MaxAge {
			return SnapshotSpec{}, fmt.Errorf("%w: %s was captured %s ago (max %s); check the snapshot timer or pass --max-snapshot-age 0",
				ErrSnapshotStale, newest.Path, age.Round(time.Minute), opts.MaxAge)
		}
	}
	return newest, nil
}

// snapshotSource is a snapshot prepared for reading: either the snapshot file
// itself (plain) or a private decompressed copy (Removable).
type snapshotSource struct {
	Spec SnapshotSpec
	// Path is the SQLite file to ATTACH.
	Path string
	// Immutable is true only for a private copy that no other process can
	// write, which is what allows SQLite's immutable=1 (zero-lock) mode.
	Immutable bool
	// TempDir is non-empty when Path is a private copy living in it.
	TempDir string
	done    bool
}

// Close removes the private copy, if any. Safe to call more than once.
func (s *snapshotSource) Close() error {
	if s == nil || s.done || s.TempDir == "" {
		if s != nil {
			s.done = true
		}
		return nil
	}
	s.done = true
	if err := os.RemoveAll(s.TempDir); err != nil {
		return fmt.Errorf("session: remove snapshot copy %s: %w", s.TempDir, err)
	}
	return nil
}

// Materialize prepares spec for reading. A plain .db is used where it lives
// (opened read-only, never immutable, since the snapshot directory is not ours
// to assume frozen); a compressed snapshot is decompressed into a private
// 0444 copy under opts.TempDir and reused only by this process.
func (s SnapshotSpec) Materialize(opts SnapshotOptions) (*snapshotSource, error) {
	if s.Encoding == SnapshotPlain {
		return &snapshotSource{Spec: s, Path: s.Path}, nil
	}
	root := opts.TempDir
	if root == "" {
		root = os.TempDir()
	}
	sweepStaleCopies(root, opts.now(), opts.warnf)

	dir, err := os.MkdirTemp(root, snapshotTempPrefix+s.Stamp.Format(snapshotStampLayout)+"-")
	if err != nil {
		return nil, fmt.Errorf("session: create snapshot copy dir under %s: %w", root, err)
	}
	dst := filepath.Join(dir, "state.db")
	if err := decompress(s, dst); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &snapshotSource{Spec: s, Path: dst, Immutable: true, TempDir: dir}, nil
}

// decompress writes the decompressed bytes of s to dst and makes dst
// read-only. A truncated or torn snapshot surfaces as ErrSnapshotInvalid —
// the archive failed to decode, so no partial database is ever handed on.
func decompress(s SnapshotSpec, dst string) error {
	in, err := os.Open(s.Path)
	if err != nil {
		return fmt.Errorf("session: open snapshot %s: %w", s.Path, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("session: create snapshot copy %s: %w", dst, err)
	}

	var reader io.Reader
	switch s.Encoding {
	case SnapshotZstd:
		dec, err := zstd.NewReader(in)
		if err != nil {
			_ = out.Close()
			return fmt.Errorf("%w: %s: %v", ErrSnapshotInvalid, s.Path, err)
		}
		defer dec.Close()
		reader = dec
	case SnapshotGzip:
		gz, err := gzip.NewReader(in)
		if err != nil {
			_ = out.Close()
			return fmt.Errorf("%w: %s: %v", ErrSnapshotInvalid, s.Path, err)
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	default:
		_ = out.Close()
		return fmt.Errorf("session: snapshot %s: unknown encoding %q", s.Path, s.Encoding)
	}

	if _, err := io.Copy(out, reader); err != nil {
		_ = out.Close()
		return fmt.Errorf("%w: %s: decompress: %v", ErrSnapshotInvalid, s.Path, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("session: write snapshot copy %s: %w", dst, err)
	}
	// The copy is private and static: drop write bits so even a bug that
	// bypassed the SQL-level read-only guarantee cannot mutate it.
	if err := os.Chmod(dst, 0o444); err != nil {
		return fmt.Errorf("session: mark snapshot copy read-only %s: %w", dst, err)
	}
	return nil
}

// sweepStaleCopies removes temporary copies left behind by a killed import.
// Only directories directly under root whose name carries snapshotTempPrefix
// and whose mtime is older than staleCopyMaxAge are touched; everything else
// is left alone. Failures are advisory (the import proceeds).
func sweepStaleCopies(root string, now time.Time, warnf func(string, ...any)) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), snapshotTempPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < staleCopyMaxAge {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if err := os.RemoveAll(path); err == nil {
			warnf("session: removed abandoned snapshot copy %s", path)
		}
	}
}

// SnapshotURI builds the SQLite URI for a snapshot file. Only the characters
// that would change how SQLite parses the URI are percent-encoded, and the URI
// options are appended by the caller-facing constructor, so a path containing
// spaces, '?' or '%' cannot smuggle in different options.
func SnapshotURI(path string) string {
	var b strings.Builder
	b.WriteString("file:")
	for _, r := range path {
		switch r {
		case '%':
			b.WriteString("%25")
		case '?':
			b.WriteString("%3F")
		case '#':
			b.WriteString("%23")
		case ' ':
			b.WriteString("%20")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
