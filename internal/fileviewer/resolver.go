// FileResolver — the two-path file attachment model (spec §7.1): by
// hash reference (file already known) and by upload (new bytes into the
// knowledge base). Both paths converge on a single file_metadata row per
// (profile_id, sha256).

package fileviewer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// resolveOutput assembles the ResolveFileOutput payload for a file row.
// Stream URLs in phase 1 are the authenticated API path (dev-JWT gated);
// HMAC-signed URLs arrive with the sandbox-host phase that needs them.
func resolveOutput(f *FileMetadata, wasNewUpload, wasDeduped bool) *ResolveFileOutput {
	return &ResolveFileOutput{
		FileMetadata: f,
		WasNewUpload: wasNewUpload,
		WasDeduped:   wasDeduped,
		StreamURL:    "/api/v1/files/" + f.ID.String() + "/stream",
		ExpiresAt:    time.Now().Add(streamURLTTL),
	}
}

// Resolve dispatches to the reference or upload path based on
// ResolveFileInput (spec §4.3). Exactly one of HashRef / Upload must be set.
func (s *serviceImpl) Resolve(ctx context.Context, input ResolveFileInput) (*ResolveFileOutput, error) {
	switch {
	case input.HashRef != nil && input.Upload != nil:
		return nil, errors.New("fileviewer: hash_ref and upload are mutually exclusive")
	case input.HashRef != nil:
		return s.ResolveByHash(ctx, input.HashRef.ProfileID, input.HashRef.SHA256)
	case input.Upload != nil:
		return s.ResolveByUpload(ctx, *input.Upload)
	default:
		return nil, errors.New("fileviewer: one of hash_ref or upload is required")
	}
}

// ResolveByHash implements the by-reference path (spec §7.2): SELECT by
// (profile_id, sha256) among live rows; miss → ErrFileNotFoundByHash.
func (s *serviceImpl) ResolveByHash(ctx context.Context, profileID uuid.UUID, sha256 string) (*ResolveFileOutput, error) {
	if !IsValidSHA256(sha256) {
		return nil, errors.New("fileviewer: sha256 must be a 64-char hex digest")
	}
	f, err := s.files.GetByProfileAndHash(ctx, profileID, sha256)
	if err != nil {
		return nil, err
	}
	return resolveOutput(f, false, false), nil
}

// ResolveByUpload implements the by-upload path (spec §7.3): stream bytes
// through a SHA-256 hasher into content-addressed storage, detect MIME,
// then dedup on (profile_id, sha256) — an existing live row gets its
// reference_count bumped and the duplicate storage discarded.
func (s *serviceImpl) ResolveByUpload(ctx context.Context, upload FileUpload) (*ResolveFileOutput, error) {
	if err := validateFilename(upload.Filename); err != nil {
		return nil, err
	}
	if upload.ProfileID == uuid.Nil {
		return nil, ErrProfileNotFound
	}
	if upload.ByteStream == nil {
		return nil, ErrFileEmpty
	}
	// Fail with the spec's PROFILE_NOT_FOUND (404) instead of an opaque FK
	// violation (500) when the profile row does not exist.
	if !s.profileExists(ctx, upload.ProfileID) {
		return nil, ErrProfileNotFound
	}

	// 1. Stream to content-addressed storage (hashes on the fly).
	res, err := s.store.Store(upload.ByteStream, fileMaxByteSize)
	if err != nil {
		return nil, err
	}

	// 2. Detect MIME from the stored head bytes (server detection wins
	// over the declared value — EC-16).
	mimeType := upload.DeclaredMime
	if head, headErr := readHead(s.store.contentPath(res.SHA256), maxSniff); headErr == nil {
		mimeType = DetectMIME(head, upload.Filename, upload.DeclaredMime)
	}

	// 3. Dedup on (profile, sha256) among live rows.
	existing, err := s.files.GetByProfileAndHash(ctx, upload.ProfileID, res.SHA256)
	switch {
	case err == nil:
		// Dedup: bump reference count; the disk copy we just wrote is a
		// no-op when another profile already held the bytes.
		if bumpErr := s.files.IncrementReferenceCount(ctx, existing.ID); bumpErr != nil {
			return nil, bumpErr
		}
		return resolveOutput(existing, false, true), nil
	case errors.Is(err, ErrFileNotFoundByHash):
		// Fall through: new row.
	default:
		return nil, err
	}

	// 4. New file: classify and insert.
	viewerHint := ViewerHintFor(mimeType)
	isText := IsTextFormat(mimeType)
	meta := &FileMetadata{
		ProfileID:       upload.ProfileID,
		SHA256:          res.SHA256,
		ByteSize:        res.ByteSize,
		MimeType:        mimeType,
		DeclaredMime:    strings.ToLower(stripMimeParams(upload.DeclaredMime)),
		Filename:        upload.Filename,
		Extension:       NormalizeExtension(upload.Filename),
		StoragePath:     filepath.ToSlash(res.StorageRel),
		StorageKind:     StorageKindHermesKB,
		SourceKind:      SourceKindUpload,
		SourceMessageID: upload.SourceMessageID,
		IsText:          isText,
		IsBinary:        !isText,
		IsViewable:      viewerHint != "",
		ViewerHint:      viewerHint,
		ReferenceCount:  1,
	}
	inserted, err := s.files.Insert(ctx, meta)
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			// Concurrent upload of identical bytes raced us (EC-11): the
			// winner's row exists now — dedup against it.
			existing, getErr := s.files.GetByProfileAndHash(ctx, upload.ProfileID, res.SHA256)
			if getErr != nil {
				return nil, getErr
			}
			if bumpErr := s.files.IncrementReferenceCount(ctx, existing.ID); bumpErr != nil {
				return nil, bumpErr
			}
			return resolveOutput(existing, false, true), nil
		}
		return nil, err
	}
	return resolveOutput(inserted, true, false), nil
}

// ResolveBatch resolves many files in one call (spec §4.3). Every input is
// resolved independently; a failing input produces an output with a nil
// File — callers match outputs to inputs positionally.
func (s *serviceImpl) ResolveBatch(ctx context.Context, inputs []ResolveFileInput) ([]ResolveFileOutput, error) {
	out := make([]ResolveFileOutput, len(inputs))
	for i, in := range inputs {
		res, err := s.Resolve(ctx, in)
		if err != nil {
			out[i] = ResolveFileOutput{ExpiresAt: time.Time{}}
			continue
		}
		out[i] = *res
	}
	return out, nil
}

// validateFilename enforces spec §10.1 FILENAME_INVALID: non-empty, ≤1000
// chars, no path separators.
func validateFilename(name string) error {
	if name == "" || len(name) > 1000 {
		return ErrFilenameInvalid
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, '\\') ||
		strings.Contains(name, "..") || filepath.IsAbs(name) {
		return ErrFilenameInvalid
	}
	return nil
}

// profileExists reports whether the profiles row exists. It is backed by
// an optional ExistenceChecker on the file repo (pgx implementation below)
// so the resolver can return PROFILE_NOT_FOUND rather than a raw FK error.
func (s *serviceImpl) profileExists(ctx context.Context, profileID uuid.UUID) bool {
	checker, ok := s.files.(interface {
		ProfileExists(ctx context.Context, id uuid.UUID) (bool, error)
	})
	if !ok {
		return true // checker unavailable: let the FK decide (500 path)
	}
	exists, err := checker.ProfileExists(ctx, profileID)
	if err != nil {
		return true // indeterminate: let the FK decide
	}
	return exists
}

// resolveProfileID is the optional interface on the file repo used by
// ResolveActorProfile (satisfied by PGFileMetadataRepo).
type resolveProfileID interface {
	ResolveActorProfile(ctx context.Context, actorID uuid.UUID) (uuid.UUID, error)
}

// ResolveActorProfile resolves the ACTING profile for an authenticated
// request once, for every /files and /viewers handler path: a profile with
// id == actorID wins; else the actor's newest owned live profile; else
// ErrProfileNotFound (the honest 404 for an actor with no profile at all).
// The identity of the actor is the JWT `sub` — a users.id — not a
// profiles.id; keeping this mapping in ONE place is the whole point.
func (s *serviceImpl) ResolveActorProfile(ctx context.Context, actorID uuid.UUID) (uuid.UUID, error) {
	resolver, ok := s.files.(resolveProfileID)
	if !ok {
		return actorID, nil // resolver unavailable: legacy pass-through
	}
	profileID, err := resolver.ResolveActorProfile(ctx, actorID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrProfileNotFound
		}
		return uuid.Nil, err
	}
	return profileID, nil
}

// readHead returns up to n leading bytes of the file at path.
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	got, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:got], nil
}
