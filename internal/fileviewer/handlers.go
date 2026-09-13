// HTTP handlers for the phase-1 SPEC-PL-02 API: /api/v1/files and
// /api/v1/viewers. Error responses use the repo's apiError envelope with
// the spec §10 codes.

package fileviewer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Handler serves the /files and /viewers route groups.
type Handler struct {
	svc FileViewerService
}

// NewHandler wires the handler to the service.
func NewHandler(svc FileViewerService) *Handler { return &Handler{svc: svc} }

// impl is the concrete service behind the interface (handlers need the
// repo/store accessors). The assertion always succeeds: NewHandler only
// accepts the internal implementation.
func (h *Handler) impl() *serviceImpl {
	impl, _ := h.svc.(*serviceImpl)
	return impl
}

// Files returns the /files router (mount under /api/v1/files).
func (h *Handler) Files() chi.Router {
	r := chi.NewRouter()
	r.Post("/upload", h.Upload)
	r.Post("/resolve", h.Resolve)
	r.Post("/resolve/batch", h.ResolveBatch)
	r.Get("/recents", h.Recents)
	r.Get("/", h.List)
	r.Get("/{id}/stream", h.Stream)
	r.Get("/{id}/access", h.GetAccessLog)
	r.Post("/{id}/access", h.PostAccessLog)
	r.Get("/{id}", h.Get)
	r.Delete("/{id}", h.Delete)
	return r
}

// Viewers returns the /viewers router (mount under /api/v1/viewers).
func (h *Handler) Viewers() chi.Router {
	r := chi.NewRouter()
	r.Post("/dispatch", h.Dispatch)
	r.Get("/", h.ListViewers)
	r.Get("/{slug}", h.GetViewer)
	return r
}

// ── Error mapping (SPEC-PL-02 §10) ──────────────────────────

func (h *Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrFileNotFound):
		writeErr(w, 404, "FILE_NOT_FOUND_BY_ID", "file not found")
	case errors.Is(err, ErrFileNotFoundByHash):
		writeErr(w, 404, "FILE_NOT_FOUND_BY_HASH", "no file with that hash for this profile")
	case errors.Is(err, ErrFileEmpty):
		writeErr(w, 400, "FILE_EMPTY", "uploaded file is empty")
	case errors.Is(err, ErrFileTooLarge):
		writeErr(w, 413, "FILE_TOO_LARGE", "file exceeds the 500 MB limit")
	case errors.Is(err, ErrFilenameInvalid):
		writeErr(w, 400, "FILENAME_INVALID", "filename is empty, over 1000 chars, or contains path separators")
	case errors.Is(err, ErrProfileNotFound):
		writeErr(w, 404, "PROFILE_NOT_FOUND", "profile does not exist")
	case errors.Is(err, ErrFileSoftDeleted):
		writeErr(w, 410, "FILE_SOFT_DELETED", "file has been soft-deleted")
	case errors.Is(err, ErrFileQuarantined):
		writeErr(w, 423, "FILE_QUARANTINED", "file is quarantined and cannot be streamed")
	case errors.Is(err, ErrViewerNotFound):
		writeErr(w, 404, "VIEWER_NOT_FOUND", "no active viewer matches this file")
	case errors.Is(err, ErrInvalidViewerSlug):
		writeErr(w, 400, "INVALID_VIEWER_SLUG", "viewer slug contains invalid characters")
	case errors.Is(err, ErrInvalidRangeHeader):
		writeErr(w, 400, "INVALID_RANGE_HEADER", "Range header is malformed")
	case errors.Is(err, ErrRangeNotSatisfiable):
		writeErr(w, 416, "RANGE_NOT_SATISFIABLE", "range request exceeds file size")
	default:
		writeErr(w, 500, "INTERNAL_ERROR", "internal server error")
	}
}

// errorBody mirrors the repo-wide error envelope (SPEC-API-07 style).
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code, b.Error.Message = code, msg
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ── Request parsing ─────────────────────────────────────────

func parseFileID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, 400, "INVALID_FILE_ID", "file id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

// validateSlug guards the {slug} URL parameter.
func validateSlug(slug string) error {
	if !IsValidViewerSlug(slug) {
		return ErrInvalidViewerSlug
	}
	return nil
}

// ── Files endpoints ─────────────────────────────────────────

// Upload handles POST /files/upload (multipart form: file, filename,
// declaredMime, sourceMessageId). Multipart envelope spec §7.3.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, "INVALID_MULTIPART", "expected a multipart/form-data body with a 'file' part")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "INVALID_MULTIPART", "multipart form must include a 'file' part")
		return
	}
	defer func() { _ = file.Close() }()

	filename := r.FormValue("filename")
	if filename == "" && header != nil {
		filename = header.Filename
	}

	var sourceMessageID *uuid.UUID
	if raw := r.FormValue("sourceMessageId"); raw != "" {
		id, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			writeErr(w, 400, "INVALID_REQUEST", "sourceMessageId must be a valid UUID")
			return
		}
		sourceMessageID = &id
	}

	profileID, ok := h.actingProfile(w, r)
	if !ok {
		return
	}
	out, err := h.impl().ResolveByUpload(r.Context(), FileUpload{
		ProfileID:       profileID,
		Filename:        filename,
		DeclaredMime:    firstNonEmpty(r.FormValue("declaredMime"), partContentType(header.Header)),
		ByteStream:      file,
		SourceMessageID: sourceMessageID,
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 201, out)
}

// partContentType extracts the declared mime from the multipart part's
// Content-Type header ("" when absent or unparseable).
func partContentType(h textHeaders) string {
	if h == nil {
		return ""
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return mt
}

// textHeaders is the minimal header accessor used by partContentType.
type textHeaders interface{ Get(string) string }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// actorIDFn is injected by the server wiring (handler.UserIDFromContext) so
// this package does not import the handler package.
var actorIDFn = func(_ *http.Request) uuid.UUID { return uuid.Nil }

// SetActorLookup installs the authenticated-user extractor. Called by
// internal/server wiring; not concurrency-safe after init by design
// (wiring happens before the listener starts).
func SetActorLookup(fn func(*http.Request) uuid.UUID) { actorIDFn = fn }

func actorID(r *http.Request) uuid.UUID { return actorIDFn(r) }

// actingProfile resolves the authenticated actor's ACTING profile once per
// request — the single seam every /files and /viewers handler path uses to
// turn the JWT `sub` (a users.id) into a profiles.id (a profile owned by
// that user, or a legacy row whose id equals the actor id). On failure it
// writes the error response (PROFILE_NOT_FOUND for an actor with no
// profile) and returns ok=false.
func (h *Handler) actingProfile(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	profileID, err := h.impl().ResolveActorProfile(r.Context(), actorID(r))
	if err != nil {
		h.fail(w, err)
		return uuid.Nil, false
	}
	return profileID, true
}

// resolveRequest is the JSON body of POST /files/resolve.
type resolveRequest struct {
	HashRef *struct {
		ProfileID uuid.UUID `json:"profile_id"`
		SHA256    string    `json:"sha256"`
	} `json:"hash_ref,omitempty"`
	Upload *struct {
		ProfileID       uuid.UUID  `json:"profile_id"`
		Filename        string     `json:"filename"`
		DeclaredMime    string     `json:"declared_mime,omitempty"`
		SourceMessageID *uuid.UUID `json:"source_message_id,omitempty"`
	} `json:"upload,omitempty"`
}

// Resolve handles POST /files/resolve (JSON; hash-reference resolution —
// the upload variant needs bytes and is served by POST /files/upload).
func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	var q resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		writeErr(w, 400, "INVALID_REQUEST", "request body must be valid JSON")
		return
	}
	switch {
	case q.HashRef != nil && q.Upload != nil:
		writeErr(w, 400, "INVALID_REQUEST", "hash_ref and upload are mutually exclusive")
		return
	case q.HashRef != nil:
		// An explicit profile_id is honored; omitted (zero) means the
		// ACTING profile — never the zero UUID.
		profileID := q.HashRef.ProfileID
		if profileID == uuid.Nil {
			var ok bool
			if profileID, ok = h.actingProfile(w, r); !ok {
				return
			}
		}
		out, err := h.impl().ResolveByHash(r.Context(), profileID, q.HashRef.SHA256)
		if err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, out)
	case q.Upload != nil:
		writeErr(w, 400, "INVALID_REQUEST", "JSON resolve cannot carry bytes; use POST /files/upload for the upload path")
	default:
		writeErr(w, 400, "INVALID_REQUEST", "one of hash_ref or upload is required")
	}
}

// ResolveBatch handles POST /files/resolve/batch (hash-ref entries only in
// phase 1; upload entries are rejected — bytes ride the upload endpoint).
func (h *Handler) ResolveBatch(w http.ResponseWriter, r *http.Request) {
	var inputs []ResolveFileInput
	if err := json.NewDecoder(r.Body).Decode(&inputs); err != nil {
		writeErr(w, 400, "INVALID_REQUEST", "request body must be a JSON array of resolve inputs")
		return
	}
	if len(inputs) > 200 {
		writeErr(w, 400, "INVALID_REQUEST", "batch limited to 200 inputs")
		return
	}
	for i, in := range inputs {
		if in.Upload != nil {
			writeErr(w, 400, "INVALID_REQUEST", fmt.Sprintf("input %d: batch resolve accepts hash_ref only; use POST /files/upload", i))
			return
		}
	}
	profileID, ok := h.actingProfile(w, r)
	if !ok {
		return
	}
	for i := range inputs {
		if inputs[i].HashRef != nil && inputs[i].HashRef.ProfileID == uuid.Nil {
			inputs[i].HashRef.ProfileID = profileID
		}
	}
	outputs, err := h.impl().ResolveBatch(r.Context(), inputs)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, outputs)
}

// Get handles GET /files/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseFileID(w, r)
	if !ok {
		return
	}
	f, err := h.impl().files.GetByID(r.Context(), id)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, f)
}

// List handles GET /files (paginated list for the current profile).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	opts, ok := parseListOpts(w, r)
	if !ok {
		return
	}
	profileID, ok := h.actingProfile(w, r)
	if !ok {
		return
	}
	files, err := h.impl().files.ListByProfile(r.Context(), profileID, opts)
	if err != nil {
		h.fail(w, err)
		return
	}
	if files == nil {
		files = []FileMetadataSlim{}
	}
	writeJSON(w, 200, map[string]any{"files": files, "pagination": map[string]any{"count": len(files)}})
}

// parseListOpts reads the ListFilesOpts query parameters with spec bounds.
func parseListOpts(w http.ResponseWriter, r *http.Request) (ListFilesOpts, bool) {
	var opts ListFilesOpts
	q := r.URL.Query()
	opts.Sort = q.Get("sort")
	opts.MimeFilter = q.Get("mimeFilter")
	opts.ExtensionFilter = q.Get("extensionFilter")
	opts.ViewableOnly = q.Get("viewableOnly") != "false"
	opts.ExcludeQuarantined = q.Get("excludeQuarantined") != "false"
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeErr(w, 400, "INVALID_REQUEST", "limit must be an integer between 1 and 200")
			return opts, false
		}
		opts.Limit = n
	}
	if raw := q.Get("cursor"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeErr(w, 400, "INVALID_REQUEST", "cursor must be a valid UUID")
			return opts, false
		}
		opts.Cursor = &id
	}
	return opts, true
}

// Recents handles GET /files/recents.
func (h *Handler) Recents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeErr(w, 400, "INVALID_REQUEST", "limit must be an integer between 1 and 200")
			return
		}
		limit = n
	}
	profileID, ok := h.actingProfile(w, r)
	if !ok {
		return
	}
	files, err := h.impl().files.ListRecent(r.Context(), profileID, limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	if files == nil {
		files = []FileMetadataSlim{}
	}
	writeJSON(w, 200, files)
}

// Stream handles GET /files/{id}/stream with Range support.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	id, ok := parseFileID(w, r)
	if !ok {
		return
	}

	profileID, ok := h.actingProfile(w, r)
	if !ok {
		return
	}
	reader, meta, err := h.impl().StreamFile(r.Context(), FileStreamRequest{
		FileID:      id,
		ProfileID:   profileID,
		RangeHeader: r.Header.Get("Range"),
		IfNoneMatch: r.Header.Get("If-None-Match"),
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", meta.MimeType)
	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Disposition", meta.ContentDisposition)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Viewer-Hint", meta.ViewerHint)

	if r.Header.Get("If-None-Match") == `"`+meta.ETag+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if br := meta.ResolvedRange; br != nil {
		length := br.length()
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.start, br.end, meta.ByteSize))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.CopyN(w, reader, length)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(meta.ByteSize, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}

// getAccessEntry is the POST /files/{id}/access body.
type postAccessRequest struct {
	Action     string    `json:"action"`
	ViewerSlug string    `json:"viewer_slug"`
	TreeID     uuid.UUID `json:"tree_id,omitempty"`
	NodeID     uuid.UUID `json:"node_id,omitempty"`
	DurationMs *int      `json:"duration_ms,omitempty"`
	ByteOffset *int64    `json:"byte_offset,omitempty"`
	ErrorCode  *string   `json:"error_code,omitempty"`
}

// GetAccessLog handles GET /files/{id}/access.
func (h *Handler) GetAccessLog(w http.ResponseWriter, r *http.Request) {
	id, ok := parseFileID(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeErr(w, 400, "INVALID_REQUEST", "limit must be an integer between 1 and 200")
			return
		}
		limit = n
	}
	entries, err := h.impl().access.GetByFile(r.Context(), id, limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	if entries == nil {
		entries = []FileAccessEntry{}
	}
	writeJSON(w, 200, entries)
}

var validActions = map[string]FileAccessAction{
	"open": AccessActionOpen, "download": AccessActionDownload,
	"thumbnail_fetch": AccessActionThumbnail, "preview_text": AccessActionPreviewText,
	"stream_start": AccessActionStreamStart, "stream_end": AccessActionStreamEnd,
	"error": AccessActionError,
}

// PostAccessLog handles POST /files/{id}/access — append one audit entry.
func (h *Handler) PostAccessLog(w http.ResponseWriter, r *http.Request) {
	id, ok := parseFileID(w, r)
	if !ok {
		return
	}
	var q postAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		writeErr(w, 400, "INVALID_REQUEST", "request body must be valid JSON")
		return
	}
	action, ok := validActions[q.Action]
	if !ok {
		writeErr(w, 400, "INVALID_REQUEST", "action must be one of open|download|thumbnail_fetch|preview_text|stream_start|stream_end|error")
		return
	}
	if q.ViewerSlug == "" || !IsValidViewerSlug(q.ViewerSlug) {
		writeErr(w, 400, "INVALID_VIEWER_SLUG", "viewer_slug is required and must be a valid slug")
		return
	}

	// The file must exist (FK enforces it too; validate first for a 404).
	if _, err := h.impl().files.GetByID(r.Context(), id); err != nil {
		h.fail(w, err)
		return
	}

	profileID, ok := h.actingProfile(w, r)
	if !ok {
		return
	}
	entry := &FileAccessEntry{
		FileID:     id,
		ProfileID:  profileID,
		ViewerSlug: q.ViewerSlug,
		Action:     action,
		DurationMs: q.DurationMs,
		ByteOffset: q.ByteOffset,
		ClientInfo: []byte("{}"),
		ErrorCode:  q.ErrorCode,
	}
	if q.TreeID != uuid.Nil {
		entry.TreeID = &q.TreeID
	}
	if q.NodeID != uuid.Nil {
		entry.NodeID = &q.NodeID
	}
	if err := h.impl().access.Append(r.Context(), entry); err != nil {
		writeErr(w, 500, "ACCESS_LOG_APPEND_FAILED", "could not append access log entry")
		return
	}

	// File open counts as an access for the recents list.
	_ = h.impl().files.UpdateLastAccessed(r.Context(), id)

	writeJSON(w, 201, entry)
}

// Delete handles DELETE /files/{id} — soft delete.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseFileID(w, r)
	if !ok {
		return
	}
	profileID, ok := h.actingProfile(w, r)
	if !ok {
		return
	}
	if err := h.impl().SoftDeleteFile(r.Context(), id, profileID); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{})
}

// ── Viewers endpoints ───────────────────────────────────────

// ListViewers handles GET /viewers.
func (h *Handler) ListViewers(w http.ResponseWriter, r *http.Request) {
	registrations, err := h.impl().registry.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	if registrations == nil {
		registrations = []ViewerRegistration{}
	}
	writeJSON(w, 200, registrations)
}

// GetViewer handles GET /viewers/{slug}.
func (h *Handler) GetViewer(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if err := validateSlug(slug); err != nil {
		h.fail(w, err)
		return
	}
	reg, err := h.impl().registry.Get(r.Context(), slug)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, reg)
}

// dispatchRequest is the POST /viewers/dispatch body: a file id plus
// optional tree context (overrides apply once that phase lands).
type dispatchRequest struct {
	FileID uuid.UUID  `json:"file_id"`
	TreeID *uuid.UUID `json:"tree_id,omitempty"`
}

// Dispatch handles POST /viewers/dispatch — resolve the viewer for a file
// without opening it (spec §14).
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	var q dispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		writeErr(w, 400, "INVALID_REQUEST", "request body must be valid JSON")
		return
	}
	file, err := h.impl().files.GetByID(r.Context(), q.FileID)
	if err != nil {
		h.fail(w, err)
		return
	}
	profileID, ok := h.actingProfile(w, r)
	if !ok {
		return
	}
	result, err := h.impl().ResolveViewer(r.Context(), file, profileID, q.TreeID)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, result)
}
