// FileViewerService — the top-level service for the file viewer subsystem
// (spec §4.3). Phase 1 implements resolve (by-hash / by-upload / batch),
// stream, access logging, recents and soft delete. Thumbnail generation,
// preview-text extraction and SSE broadcast are later phases: the
// interface methods exist and return the spec's not-available errors.

package fileviewer

import (
	"context"
	"errors"
	"io"
	"log"
	"time"

	"github.com/google/uuid"
)

// FileViewerService is the phase-1 service surface.
type FileViewerService interface {
	// ResolveViewer picks the correct viewer for a file based on its
	// MIME / extension / hint. Overrides apply once the overrides surface lands.
	ResolveViewer(ctx context.Context, file *FileMetadata, profileID uuid.UUID, treeID *uuid.UUID) (*ViewerDispatchResult, error)

	// StreamFile opens a streaming response for the file content.
	// Honors HTTP Range requests.
	StreamFile(ctx context.Context, req FileStreamRequest) (io.ReadCloser, *StreamFileMeta, error)

	// GenerateThumbnail produces a 200 px WebP thumbnail (later phase).
	GenerateThumbnail(ctx context.Context, fileID uuid.UUID) (*ThumbnailResult, error)

	// ExtractPreviewText reads the first 8 KB of a text file (later phase).
	ExtractPreviewText(ctx context.Context, fileID uuid.UUID) (string, error)

	// LogAccess appends an entry to file_access_log. Best-effort.
	LogAccess(ctx context.Context, entry *FileAccessEntry) error

	// Recents returns the most recently accessed files for a profile.
	Recents(ctx context.Context, profileID uuid.UUID, limit int) ([]FileMetadataSlim, error)

	// SoftDeleteFile marks a file as deleted.
	SoftDeleteFile(ctx context.Context, fileID uuid.UUID, actorID uuid.UUID) error

	// BroadcastFileEvent publishes an SSE event (later phase; the event
	// shape is already defined in models.go).
	BroadcastFileEvent(ctx context.Context, event *FileViewerSSEEvent) error

	// SeedViewerRegistry upserts the built-in viewers on boot (spec §6.2).
	SeedViewerRegistry(ctx context.Context) error
}

// StreamFileMeta describes a streaming response. The resolved byte window
// (nil when no Range header was present) rides along for the handler to
// build 206 headers; it is never serialized.
type StreamFileMeta struct {
	ByteSize           int64     `json:"byteSize"`
	MimeType           string    `json:"mimeType"`
	ETag               string    `json:"etag"` // SHA-256 hex; strong validator
	LastModified       time.Time `json:"lastModified"`
	AcceptRanges       bool      `json:"acceptRanges"`
	ContentDisposition string    `json:"contentDisposition"` // 'inline' or 'attachment'
	ViewerHint         string    `json:"viewerHint"`

	ResolvedRange *byteRange `json:"-"` // nil = full-body 200 response
}

// ThumbnailResult is the output of GenerateThumbnail (later phase).
type ThumbnailResult struct {
	ThumbnailPath   string `json:"thumbnailPath"`
	ThumbnailSHA256 string `json:"thumbnailSha256"`
	ThumbnailURL    string `json:"thumbnailUrl"`
	WidthPx         int    `json:"widthPx"`
	HeightPx        int    `json:"heightPx"`
	ByteSize        int    `json:"byteSize"`
}

// ErrThumbnailNotAvailable is returned for non-image files (spec:
// THUMBNAIL_NOT_AVAILABLE, 404) until the thumbnail phase lands.
var ErrThumbnailNotAvailable = errors.New("fileviewer: thumbnail generation not available (phase 2)")

// ErrPreviewNotAvailable is returned until the preview phase lands.
var ErrPreviewNotAvailable = errors.New("fileviewer: preview text extraction not available (phase 2)")

// ErrBroadcastUnavailable is returned until the SSE phase lands.
var ErrBroadcastUnavailable = errors.New("fileviewer: SSE event broadcast not available (phase 2)")

// fileMaxByteSize is the spec's 500 MB hard limit (chk_byte_size).
const fileMaxByteSize = 500 * 1024 * 1024

// serviceImpl wires the repos, store and dispatch table.
type serviceImpl struct {
	files    FileMetadataRepo
	viewers  ViewerRegistryRepo
	access   FileAccessLogRepo
	store    *FileStore
	registry *ViewerRegistry
}

// NewService builds the FileViewerService.
func NewService(files FileMetadataRepo, viewers ViewerRegistryRepo, access FileAccessLogRepo, store *FileStore) FileViewerService {
	return &serviceImpl{
		files:    files,
		viewers:  viewers,
		access:   access,
		store:    store,
		registry: NewViewerRegistry(viewers),
	}
}

// Registry exposes the dispatch table for the handlers.
func (s *serviceImpl) Registry() *ViewerRegistry { return s.registry }

// Access exposes the access-log repo for the handlers.
func (s *serviceImpl) Access() FileAccessLogRepo { return s.access }

// Files exposes the file-metadata repo for the handlers.
func (s *serviceImpl) Files() FileMetadataRepo { return s.files }

// SeedViewerRegistry upserts the built-in viewers; safe to call on every
// boot (idempotent transaction per spec §6.2).
func (s *serviceImpl) SeedViewerRegistry(ctx context.Context) error {
	return s.viewers.UpsertAll(ctx, seededDescriptors())
}

// ResolveViewer picks the viewer for a file via the dispatch table.
func (s *serviceImpl) ResolveViewer(ctx context.Context, file *FileMetadata, profileID uuid.UUID, treeID *uuid.UUID) (*ViewerDispatchResult, error) {
	return s.registry.ResolveViewer(ctx, file, profileID, treeID)
}

// StreamFile opens the file content for streaming, applying the Range
// window and returning the response metadata (ETag = sha256, strong
// validator). Quarantined files never stream (spec §10.1).
func (s *serviceImpl) StreamFile(ctx context.Context, req FileStreamRequest) (io.ReadCloser, *StreamFileMeta, error) {
	file, err := s.files.GetByID(ctx, req.FileID)
	if err != nil {
		return nil, nil, err
	}
	if file.Quarantined {
		return nil, nil, ErrFileQuarantined
	}

	// Validate the Range window against the real file size.
	br, err := parseRangeHeader(req.RangeHeader, file.ByteSize)
	if err != nil {
		return nil, nil, err
	}

	f, err := s.store.Open(file.SHA256)
	if err != nil {
		return nil, nil, errors.Join(ErrStorageUnavailable, err)
	}

	seekOffset := int64(0)
	if br != nil {
		seekOffset = br.start
	}
	if _, err := f.Seek(seekOffset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, nil, errors.Join(ErrStorageUnavailable, err)
	}

	var reader io.ReadCloser = f
	if br != nil {
		reader = &limitedReadCloser{Reader: io.LimitReader(f, br.length()), Closer: f}
	}

	disposition := "inline"
	if !file.IsViewable {
		disposition = "attachment"
	}

	return reader, &StreamFileMeta{
		ByteSize:           file.ByteSize,
		MimeType:           file.MimeType,
		ETag:               file.SHA256,
		LastModified:       file.UpdatedAt,
		AcceptRanges:       true,
		ContentDisposition: disposition,
		ViewerHint:         file.ViewerHint,
		ResolvedRange:      br,
	}, nil
}

// limitedReadCloser couples a limit reader with the underlying file closer.
type limitedReadCloser struct {
	io.Reader
	io.Closer
}

// LogAccess appends an access-log entry; failures are logged, never fatal
// (spec: "Always succeeds (best-effort)").
func (s *serviceImpl) LogAccess(ctx context.Context, entry *FileAccessEntry) error {
	if err := s.access.Append(ctx, entry); err != nil {
		log.Printf("fileviewer: access log append failed (best-effort): %v", err)
	}
	return nil
}

// Recents returns recently accessed files for a profile.
func (s *serviceImpl) Recents(ctx context.Context, profileID uuid.UUID, limit int) ([]FileMetadataSlim, error) {
	return s.files.ListRecent(ctx, profileID, limit)
}

// SoftDeleteFile marks a file deleted. Row preserved for audit; the
// (profile, sha256) partial-unique index only covers live rows, so a later
// re-upload of the same bytes creates a fresh row (spec §7.4).
func (s *serviceImpl) SoftDeleteFile(ctx context.Context, fileID uuid.UUID, _ uuid.UUID) error {
	return s.files.SoftDelete(ctx, fileID)
}

// GenerateThumbnail and ExtractPreviewText are phase-2 (thumbnails.go /
// preview extraction) per the phased plan; the phase-1 endpoints that would
// call them are not mounted, so these only satisfy the interface.
func (s *serviceImpl) GenerateThumbnail(_ context.Context, _ uuid.UUID) (*ThumbnailResult, error) {
	return nil, ErrThumbnailNotAvailable
}

func (s *serviceImpl) ExtractPreviewText(_ context.Context, _ uuid.UUID) (string, error) {
	return "", ErrPreviewNotAvailable
}

func (s *serviceImpl) BroadcastFileEvent(_ context.Context, _ *FileViewerSSEEvent) error {
	return ErrBroadcastUnavailable
}
