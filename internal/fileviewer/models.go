// Package fileviewer implements the backend of SPEC-PL-02 (built-in file
// viewers): file storage/dedup, MIME detection, viewer registry + dispatch,
// and the phase-1 /files + /viewers HTTP API. Frontend viewers, thumbnails,
// previews, bundle download, overrides HTTP endpoints and SSE events are
// later phases of the spec rollout and intentionally absent here.
package fileviewer

import (
	"io"
	"time"

	"github.com/google/uuid"
)

// ── Storage & Source ────────────────────────────────────────

// FileStorageKind identifies how the file bytes are stored.
type FileStorageKind string

const (
	StorageKindHermesKB FileStorageKind = "hermes_kb"     // Bytes in hermes-kb storage root
	StorageKindHermesFS FileStorageKind = "hermes_fs_ref" // Reference to existing file in hermes-fs
	StorageKindExternal FileStorageKind = "external"      // URL reference (rare; future federation)
)

// FileSourceKind identifies the origin of the file in the system.
type FileSourceKind string

const (
	SourceKindUpload       FileSourceKind = "upload"
	SourceKindReference    FileSourceKind = "reference"
	SourceKindAgentMessage FileSourceKind = "agent_message"
	SourceKindImport       FileSourceKind = "import"
)

// ── File Metadata ───────────────────────────────────────────

// FileMetadata is the canonical record for a unique file content (per profile).
// One row per (profile_id, sha256); reference_count tracks how many messages point at it.
type FileMetadata struct {
	ID                uuid.UUID       `db:"id"                  json:"id"`
	ProfileID         uuid.UUID       `db:"profile_id"          json:"profileId"`
	SHA256            string          `db:"sha256"              json:"sha256"`
	ByteSize          int64           `db:"byte_size"           json:"byteSize"`
	MimeType          string          `db:"mime_type"           json:"mimeType"`
	DeclaredMime      string          `db:"declared_mime"       json:"declaredMime"`
	Filename          string          `db:"filename"            json:"filename"`
	Extension         string          `db:"extension"           json:"extension"`
	StoragePath       string          `db:"storage_path"        json:"-"` // Internal; not exposed via API
	StorageKind       FileStorageKind `db:"storage_kind"        json:"storageKind"`
	SourceKind        FileSourceKind  `db:"source_kind"         json:"sourceKind"`
	SourceMessageID   *uuid.UUID      `db:"source_message_id"   json:"sourceMessageId,omitempty"`
	SourceExternalURL string          `db:"source_external_url" json:"sourceExternalUrl,omitempty"`
	IsText            bool            `db:"is_text"             json:"isText"`
	IsBinary          bool            `db:"is_binary"           json:"isBinary"`
	IsViewable        bool            `db:"is_viewable"         json:"isViewable"`
	ViewerHint        string          `db:"viewer_hint"         json:"viewerHint"`
	ThumbnailPath     string          `db:"thumbnail_path"      json:"thumbnailPath,omitempty"`
	ThumbnailSHA256   string          `db:"thumbnail_sha256"    json:"thumbnailSha256,omitempty"`
	PreviewText       string          `db:"preview_text"        json:"previewText,omitempty"`
	MetadataJSON      []byte          `db:"metadata_json"       json:"metadata"` // Type-specific (PDF pages, image dims, etc.)
	ReferenceCount    int             `db:"reference_count"     json:"referenceCount"`
	LastAccessedAt    *time.Time      `db:"last_accessed_at"    json:"lastAccessedAt,omitempty"`
	AccessCount       int             `db:"access_count"        json:"accessCount"`
	Quarantined       bool            `db:"quarantined"         json:"quarantined"`
	CreatedAt         time.Time       `db:"created_at"          json:"createdAt"`
	UpdatedAt         time.Time       `db:"updated_at"          json:"updatedAt"`
	DeletedAt         *time.Time      `db:"deleted_at"          json:"deletedAt,omitempty"`
}

// FileMetadataSlim is a lighter view used in list endpoints (no preview_text, no metadata_json).
type FileMetadataSlim struct {
	ID             uuid.UUID       `json:"id"`
	ProfileID      uuid.UUID       `json:"profileId"`
	SHA256         string          `json:"sha256"`
	ByteSize       int64           `json:"byteSize"`
	MimeType       string          `json:"mimeType"`
	Filename       string          `json:"filename"`
	Extension      string          `json:"extension"`
	StorageKind    FileStorageKind `json:"storageKind"`
	IsText         bool            `json:"isText"`
	IsViewable     bool            `json:"isViewable"`
	ViewerHint     string          `json:"viewerHint"`
	ThumbnailPath  string          `json:"thumbnailPath,omitempty"`
	ReferenceCount int             `json:"referenceCount"`
	LastAccessedAt *time.Time      `json:"lastAccessedAt,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// ── Viewer Registry ─────────────────────────────────────────

// ViewerRenderType for built-in viewers. Includes 'fullscreen' (new for SPEC-PL-02).
// card/embed are reused from SPEC-PL-01 for future extensibility.
type ViewerRenderType string

const (
	ViewerRenderFullscreen ViewerRenderType = "fullscreen"
	ViewerRenderEmbed      ViewerRenderType = "embed"
	ViewerRenderCard       ViewerRenderType = "card"
)

// ViewerRegistration is one row in the viewer_registry table.
// One row per built-in viewer compiled into canopyd.
type ViewerRegistration struct {
	ID                   uuid.UUID        `db:"id"                  json:"id"`
	ViewerSlug           string           `db:"viewer_slug"         json:"viewerSlug"`
	Version              string           `db:"version"             json:"version"`
	CanopydVersion       string           `db:"canopyd_version"     json:"canopydVersion"`
	DisplayName          string           `db:"display_name"        json:"displayName"`
	Description          string           `db:"description"         json:"description"`
	IconURL              string           `db:"icon_url"            json:"iconUrl"`
	RenderType           ViewerRenderType `db:"render_type"         json:"renderType"`
	SupportsMime         []string         `db:"supports_mime"       json:"supportsMime"`
	SupportsExtensions   []string         `db:"supports_extensions" json:"supportsExtensions"`
	SupportsViewerHint   []string         `db:"supports_viewer_hint" json:"supportsViewerHint"`
	RequiredCapabilities []string         `db:"required_capabilities" json:"requiredCapabilities"`
	BundlePath           string           `db:"bundle_path"         json:"bundlePath"`
	BundleByteSize       int              `db:"bundle_byte_size"    json:"bundleByteSize"`
	BundleSHA256         string           `db:"bundle_sha256"       json:"bundleSha256"`
	MinCanopydVersion    string           `db:"min_canopyd_version" json:"minCanopydVersion"`
	DeprecationNotice    string           `db:"deprecation_notice"  json:"deprecationNotice,omitempty"`
	IsActive             bool             `db:"is_active"           json:"isActive"`
	InstalledAt          time.Time        `db:"installed_at"        json:"installedAt"`
}

// BuiltInViewerDescriptor is the compile-time definition of a built-in viewer.
// Used to seed viewer_registry on first boot. The Go map in builtin_viewers.go is
// the source of truth; the table is a runtime cache.
type BuiltInViewerDescriptor struct {
	ViewerSlug         string
	Version            string // pinned to canopyd build at compile time
	CanopydVersion     string // canopyd version this viewer was compiled into
	DisplayName        string
	Description        string
	IconURL            string
	RenderType         ViewerRenderType
	SupportsMime       []string
	SupportsExtensions []string
	BundlePath         string // relative to web root; empty for compile-time-inlined viewers
	BundleByteSize     int
	BundleSHA256       string
	MinCanopydVersion  string
}

// ── Viewer Config Override ──────────────────────────────────

// ViewerConfigOverride is a per-profile or per-tree override of the default viewer dispatch.
type ViewerConfigOverride struct {
	ID               uuid.UUID  `db:"id"                 json:"id"`
	ProfileID        *uuid.UUID `db:"profile_id"         json:"profileId,omitempty"`
	TreeID           *uuid.UUID `db:"tree_id"            json:"treeId,omitempty"`
	MimePattern      string     `db:"mime_pattern"       json:"mimePattern"`
	ExtensionPattern string     `db:"extension_pattern"  json:"extensionPattern"`
	OverrideViewer   string     `db:"override_viewer"    json:"overrideViewer"` // '' disables
	Priority         int        `db:"priority"           json:"priority"`
	CreatedBy        uuid.UUID  `db:"created_by"         json:"createdBy"`
	CreatedAt        time.Time  `db:"created_at"         json:"createdAt"`
	ExpiresAt        *time.Time `db:"expires_at"         json:"expiresAt,omitempty"`
}

// ── File Access Log ─────────────────────────────────────────

// FileAccessAction enumerates the kinds of viewer interactions logged.
type FileAccessAction string

const (
	AccessActionOpen        FileAccessAction = "open"
	AccessActionDownload    FileAccessAction = "download"
	AccessActionThumbnail   FileAccessAction = "thumbnail_fetch"
	AccessActionPreviewText FileAccessAction = "preview_text"
	AccessActionStreamStart FileAccessAction = "stream_start"
	AccessActionStreamEnd   FileAccessAction = "stream_end"
	AccessActionError       FileAccessAction = "error"
)

// FileAccessEntry is one row in file_access_log.
type FileAccessEntry struct {
	ID         uuid.UUID        `db:"id"               json:"id"`
	FileID     uuid.UUID        `db:"file_id"          json:"fileId"`
	ProfileID  uuid.UUID        `db:"profile_id"       json:"profileId"`
	TreeID     *uuid.UUID       `db:"tree_id"          json:"treeId,omitempty"`
	NodeID     *uuid.UUID       `db:"node_id"          json:"nodeId,omitempty"`
	ViewerSlug string           `db:"viewer_slug"      json:"viewerSlug"`
	Action     FileAccessAction `db:"action"           json:"action"`
	DurationMs *int             `db:"duration_ms"      json:"durationMs,omitempty"`
	ByteOffset *int64           `db:"byte_offset"      json:"byteOffset,omitempty"`
	ClientInfo []byte           `db:"client_info"      json:"clientInfo"`
	ErrorCode  *string          `db:"error_code"       json:"errorCode,omitempty"`
	CreatedAt  time.Time        `db:"created_at"       json:"createdAt"`
}

// ── Service Input/Output Structs ────────────────────────────

// ResolveFileInput is the input to FileResolver.Resolve.
// Either HashRef or Upload must be set, never both.
type ResolveFileInput struct {
	// Reference mode: file already in Hermes filesystem; reference by hash.
	HashRef *HashRef `json:"hash_ref,omitempty"`

	// Upload mode: new file bytes being attached.
	Upload *FileUpload `json:"upload,omitempty"`
}

// HashRef identifies an existing file by content hash + profile scope.
type HashRef struct {
	ProfileID uuid.UUID `json:"profile_id"`
	SHA256    string    `json:"sha256"`
}

// FileUpload carries new file bytes for upload into Hermes KB.
type FileUpload struct {
	ProfileID       uuid.UUID  `json:"profile_id"`
	Filename        string     `json:"filename"`
	DeclaredMime    string     `json:"declared_mime,omitempty"`
	ByteStream      io.Reader  `json:"-"` // populated from multipart form
	SourceMessageID *uuid.UUID `json:"source_message_id,omitempty"`
}

// ResolveFileOutput is the result of FileResolver.Resolve.
type ResolveFileOutput struct {
	FileMetadata *FileMetadata `json:"file"`
	WasNewUpload bool          `json:"was_new_upload"` // true if upload path was taken
	WasDeduped   bool          `json:"was_deduped"`    // true if upload bytes matched existing hash
	StreamURL    string        `json:"stream_url"`     // signed URL for the viewer to fetch content
	ThumbnailURL string        `json:"thumbnail_url,omitempty"`
	ExpiresAt    time.Time     `json:"expires_at"` // signed URLs expire
}

// FileStreamRequest is the input to the streaming endpoint. The Range
// header is carried raw: it is validated against the file's real size
// inside StreamFile (the spec's RangeStart/RangeEnd pair cannot express
// the suffix form "bytes=-N" without the size).
type FileStreamRequest struct {
	FileID      uuid.UUID
	ProfileID   uuid.UUID
	RangeHeader string // raw HTTP Range header ("" = full body)
	IfNoneMatch string // ETag for caching
}

// ViewerDispatchResult is the result of ViewerRegistry.Resolve.
type ViewerDispatchResult struct {
	ViewerSlug   string           `json:"viewerSlug"`
	RenderType   ViewerRenderType `json:"renderType"`
	BundlePath   string           `json:"bundlePath"`
	BundleSHA256 string           `json:"bundleSha256"`
	DisplayName  string           `json:"displayName"`
	IconURL      string           `json:"iconUrl"`
	Config       map[string]any   `json:"config"` // Viewer-specific configuration
	IsBuiltIn    bool             `json:"isBuiltIn"`
}

// FileViewerSSEEvent is the payload for viewer-related SSE events.
// Emission is a later phase; the shape is defined here so callers can
// construct events without another struct churn when SSE lands.
type FileViewerSSEEvent struct {
	EventType string `json:"event_type"`
	// 'file_uploaded' | 'file_resolved' | 'file_deleted' | 'file_thumbnail_ready'
	// 'viewer_registered' | 'viewer_config_changed' | 'file_access_logged'
	FileID     *uuid.UUID     `json:"file_id,omitempty"`
	ViewerSlug string         `json:"viewer_slug,omitempty"`
	ProfileID  uuid.UUID      `json:"profile_id"`
	TreeID     *uuid.UUID     `json:"tree_id,omitempty"`
	NodeID     *uuid.UUID     `json:"node_id,omitempty"`
	SHA256     string         `json:"sha256,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}
