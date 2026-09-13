package fileviewer

// Sentinel errors for the file viewer subsystem. HTTP handlers map these to
// the SPEC-PL-02 §10 error codes.
var (
	// ErrFileNotFound is returned when no file_metadata row matches an ID
	// (spec code FILE_NOT_FOUND_BY_ID, 404).
	ErrFileNotFound = errFileNotFound{}

	// ErrFileNotFoundByHash is returned when a hash reference does not
	// resolve to any file_metadata row (FILE_NOT_FOUND_BY_HASH, 404).
	ErrFileNotFoundByHash = errFileNotFoundByHash{}

	// ErrFileEmpty is returned for 0-byte uploads (FILE_EMPTY, 400).
	ErrFileEmpty = errFileEmpty{}

	// ErrFileTooLarge is returned when an upload exceeds the 500 MB limit
	// (FILE_TOO_LARGE, 413).
	ErrFileTooLarge = errFileTooLarge{}

	// ErrFilenameInvalid is returned when a filename is empty, over 1000
	// chars, or contains path separators (FILENAME_INVALID, 400).
	ErrFilenameInvalid = errFilenameInvalid{}

	// ErrProfileNotFound is returned when the profile referenced by a
	// HashRef/FileUpload does not exist (PROFILE_NOT_FOUND, 404).
	ErrProfileNotFound = errProfileNotFound{}

	// ErrFileSoftDeleted is returned when the addressed file row has been
	// soft-deleted; only metadata remains accessible (FILE_SOFT_DELETED, 410).
	ErrFileSoftDeleted = errFileSoftDeleted{}

	// ErrFileQuarantined is returned when a quarantined file cannot be
	// streamed (FILE_QUARANTINED, 423).
	ErrFileQuarantined = errFileQuarantined{}

	// ErrDuplicate is returned by FileMetadataRepo.Insert when a
	// (profile_id, sha256) row already exists — the resolver's dedup signal.
	ErrDuplicate = errDuplicate{}

	// ErrViewerNotFound is returned when no active viewer matches the
	// file's MIME/extension/hint (VIEWER_NOT_FOUND, 404).
	ErrViewerNotFound = errViewerNotFound{}

	// ErrInvalidViewerSlug is returned when a viewer slug contains invalid
	// characters (INVALID_VIEWER_SLUG, 400).
	ErrInvalidViewerSlug = errInvalidViewerSlug{}

	// ErrRangeNotSatisfiable is returned when a Range request exceeds the
	// file size (RANGE_NOT_SATISFIABLE, 416).
	ErrRangeNotSatisfiable = errRangeNotSatisfiable{}

	// ErrInvalidRangeHeader is returned when the HTTP Range header is
	// malformed (INVALID_RANGE_HEADER, 400).
	ErrInvalidRangeHeader = errInvalidRangeHeader{}

	// ErrStorageUnavailable is returned when the storage backend cannot be
	// reached (STREAM_INTERRUPTED, 503).
	ErrStorageUnavailable = errStorageUnavailable{}
)

// Each error is its own type so handlers can errors.Is-map it to a distinct
// SPEC-PL-02 §10 code; the type wrapper also makes the sentinel values
// printable without aliasing a shared error instance.
type (
	errFileNotFound        struct{}
	errFileNotFoundByHash  struct{}
	errFileEmpty           struct{}
	errFileTooLarge        struct{}
	errFilenameInvalid     struct{}
	errProfileNotFound     struct{}
	errFileSoftDeleted     struct{}
	errFileQuarantined     struct{}
	errDuplicate           struct{}
	errViewerNotFound      struct{}
	errInvalidViewerSlug   struct{}
	errRangeNotSatisfiable struct{}
	errInvalidRangeHeader  struct{}
	errStorageUnavailable  struct{}
)

func (errFileNotFound) Error() string       { return "fileviewer: file not found" }
func (errFileNotFoundByHash) Error() string { return "fileviewer: file not found by hash" }
func (errFileEmpty) Error() string          { return "fileviewer: uploaded file is empty" }
func (errFileTooLarge) Error() string       { return "fileviewer: file exceeds 500 MB limit" }
func (errFilenameInvalid) Error() string {
	return "fileviewer: filename is empty, too long, or contains path separators"
}
func (errProfileNotFound) Error() string   { return "fileviewer: profile not found" }
func (errFileSoftDeleted) Error() string   { return "fileviewer: file is soft-deleted" }
func (errFileQuarantined) Error() string   { return "fileviewer: file is quarantined" }
func (errDuplicate) Error() string         { return "fileviewer: duplicate file for profile and hash" }
func (errViewerNotFound) Error() string    { return "fileviewer: no active viewer matches this file" }
func (errInvalidViewerSlug) Error() string { return "fileviewer: invalid viewer slug" }
func (errRangeNotSatisfiable) Error() string {
	return "fileviewer: range not satisfiable"
}
func (errInvalidRangeHeader) Error() string { return "fileviewer: invalid range header" }
func (errStorageUnavailable) Error() string { return "fileviewer: storage backend unavailable" }
