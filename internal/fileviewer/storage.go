// Content-addressed byte storage for uploaded files.
//
// Storage root (spec §7.6 "hermes_kb" layout):
//
//	<CANOPY_FILE_ROOT>/files/<aa>/<sha256>            canonical content bytes
//	<CANOPY_FILE_ROOT>/tmp/<uuid>                     in-flight upload spill
//
// <aa> is the first two hex chars of the SHA-256 (256-way shard so no
// directory grows unbounded). CANOPY_FILE_ROOT defaults to ~/.canopy/files
// and is documented on Config.FileRoot in internal/config.
//
// Cross-profile dedup (spec §7.4): content is written once per hash; rows
// stay per-(profile, sha256) and share the same storage_path.

package fileviewer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// FileStore persists file bytes under the hermes-kb storage root.
type FileStore struct {
	root string
}

// NewFileStore creates the store rooted at root (directories are created
// lazily on first write).
func NewFileStore(root string) *FileStore {
	return &FileStore{root: root}
}

// DefaultFileRoot returns ~/.canopy/files (the CANOPY_FILE_ROOT default).
func DefaultFileRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".canopy/files"
	}
	return filepath.Join(home, ".canopy", "files")
}

// contentPath maps a hex sha256 to its sharded path under the root.
func (s *FileStore) contentPath(shaHex string) string {
	shaHex = strings.ToLower(shaHex)
	shard := "00"
	if len(shaHex) >= 2 {
		shard = shaHex[:2]
	}
	return filepath.Join(s.root, "files", shard, shaHex)
}

// Has reports whether the canonical content bytes exist on disk.
func (s *FileStore) Has(shaHex string) bool {
	_, err := os.Stat(s.contentPath(shaHex))
	return err == nil
}

// Open returns a reader over the canonical content bytes.
func (s *FileStore) Open(shaHex string) (*os.File, error) {
	f, err := os.Open(s.contentPath(shaHex))
	if err != nil {
		return nil, fmt.Errorf("fileviewer: open content %s: %w", shaHex, err)
	}
	return f, nil
}

// StoreResult reports what a Store call did.
type StoreResult struct {
	SHA256     string
	ByteSize   int64
	Deduped    bool // content already existed on disk under this hash
	StorageRel string
}

// Store streams r to disk under its SHA-256, enforcing the size limits.
// maxSize is the hard cap (spec: 500 MB); oversized content aborts with
// ErrFileTooLarge and the spill file is removed. Returns ErrFileEmpty for
// zero bytes (spec EC-1: the DB CHECK would reject it anyway).
func (s *FileStore) Store(r io.Reader, maxSize int64) (*StoreResult, error) {
	if err := os.MkdirAll(filepath.Join(s.root, "tmp"), 0o755); err != nil {
		return nil, fmt.Errorf("fileviewer: create tmp dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(s.root, "files"), 0o755); err != nil {
		return nil, fmt.Errorf("fileviewer: create files dir: %w", err)
	}

	spillPath := filepath.Join(s.root, "tmp", uuid.NewString())
	spill, err := os.Create(spillPath)
	if err != nil {
		return nil, fmt.Errorf("fileviewer: create spill file: %w", err)
	}
	defer func() {
		_ = spill.Close()
		_ = os.Remove(spillPath) // no-op after a successful promote
	}()

	var hasher = sha256.New()
	// Count against maxSize + 1 so an exactly-max file passes and the
	// first over-quota byte aborts the copy.
	limited := io.LimitReader(r, maxSize+1)
	n, err := io.Copy(io.MultiWriter(spill, hasher), limited)
	if err != nil {
		return nil, fmt.Errorf("fileviewer: store stream: %w", err)
	}
	if n == 0 {
		return nil, ErrFileEmpty
	}
	if n > maxSize {
		return nil, ErrFileTooLarge
	}
	if err := spill.Close(); err != nil {
		return nil, fmt.Errorf("fileviewer: close spill file: %w", err)
	}

	shaHex := hex.EncodeToString(hasher.Sum(nil))
	dst := s.contentPath(shaHex)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, fmt.Errorf("fileviewer: create shard dir: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		// Content-addressed dedup: another upload (or profile) already
		// persisted these bytes; drop the spill copy.
		return &StoreResult{SHA256: shaHex, ByteSize: n, Deduped: true, StorageRel: dst}, nil
	}
	if err := os.Rename(spillPath, dst); err != nil {
		return nil, fmt.Errorf("fileviewer: promote content %s: %w", shaHex, err)
	}
	return &StoreResult{SHA256: shaHex, ByteSize: n, Deduped: false, StorageRel: dst}, nil
}

// StorageRoot returns the configured root (exposed for tests).
func (s *FileStore) StorageRoot() string { return s.root }
