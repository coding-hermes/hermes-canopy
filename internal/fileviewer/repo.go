// Repository implementations for SPEC-PL-02: file_metadata, viewer_registry
// and file_access_log, backed by pgx (pgxpool). Interface shapes follow
// spec §4.3; scanning and error-mapping conventions follow internal/db.

package fileviewer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ListFilesOpts controls file listing behavior.
type ListFilesOpts struct {
	Cursor             *uuid.UUID
	Limit              int    // 1-200; default 50
	Sort               string // "created_desc" | "created_asc" | "name_asc" | "size_desc" | "last_accessed_desc"
	MimeFilter         string // substring match on mime_type
	ExtensionFilter    string // exact match on extension
	ViewableOnly       bool   // if true, exclude is_viewable = false
	ExcludeQuarantined bool   // default true
}

// defaultListLimit mirrors the spec's list default.
const defaultListLimit = 50

// maxListLimit is the spec's page cap.
const maxListLimit = 200

// clampListOpts applies spec defaults/bounds to a ListFilesOpts.
func clampListOpts(opts ListFilesOpts) ListFilesOpts {
	if opts.Limit <= 0 {
		opts.Limit = defaultListLimit
	}
	if opts.Limit > maxListLimit {
		opts.Limit = maxListLimit
	}
	return opts
}

// ── File Metadata Repo ──────────────────────────────────────

// FileMetadataRepo is the persistence interface for file_metadata.
type FileMetadataRepo interface {
	// Insert creates a new file_metadata row. If (profile_id, sha256)
	// already exists (among live rows), returns ErrDuplicate.
	Insert(ctx context.Context, f *FileMetadata) (*FileMetadata, error)

	// GetByID retrieves a non-deleted file_metadata row by ID.
	GetByID(ctx context.Context, id uuid.UUID) (*FileMetadata, error)

	// GetByProfileAndHash retrieves a non-deleted file by (profile_id,
	// sha256). The lookup path for HashRef resolution.
	GetByProfileAndHash(ctx context.Context, profileID uuid.UUID, sha256 string) (*FileMetadata, error)

	// ListByProfile returns files owned by a profile, paginated.
	ListByProfile(ctx context.Context, profileID uuid.UUID, opts ListFilesOpts) ([]FileMetadataSlim, error)

	// ListByTree returns files referenced by any node in a tree. Phase 1
	// has no file-node join table yet; it resolves to ListByProfile scope
	// via the tree's owner profile when wired (kept for interface parity).
	ListByTree(ctx context.Context, treeID uuid.UUID, opts ListFilesOpts) ([]FileMetadataSlim, error)

	// ListRecent returns recently accessed files for a profile.
	// Powers the "Recents" list.
	ListRecent(ctx context.Context, profileID uuid.UUID, limit int) ([]FileMetadataSlim, error)

	// IncrementReferenceCount atomically bumps reference_count.
	IncrementReferenceCount(ctx context.Context, id uuid.UUID) error

	// DecrementReferenceCount atomically decrements reference_count.
	DecrementReferenceCount(ctx context.Context, id uuid.UUID) error

	// UpdateLastAccessed stamps last_accessed_at = now() and bumps access_count.
	UpdateLastAccessed(ctx context.Context, id uuid.UUID) error

	// UpdateThumbnail sets the thumbnail_path and thumbnail_sha256.
	UpdateThumbnail(ctx context.Context, id uuid.UUID, path, sha256 string) error

	// SoftDelete sets deleted_at = now(). Row preserved for audit.
	SoftDelete(ctx context.Context, id uuid.UUID) error

	// PurgeDeleted hard-deletes rows with deleted_at < cutoff. Called by a janitor job.
	PurgeDeleted(ctx context.Context, cutoff time.Time) (int, error)

	// WithTx runs fn inside a database transaction.
	WithTx(ctx context.Context, fn func(repo FileMetadataRepo) error) error
}

// PGFileMetadataRepo is the pgx-backed FileMetadataRepo.
type PGFileMetadataRepo struct {
	pool *pgxpool.Pool
	tx   pgx.Tx
}

// NewPGFileMetadataRepo wires the repo to a pgxpool.
func NewPGFileMetadataRepo(pool *pgxpool.Pool) *PGFileMetadataRepo {
	return &PGFileMetadataRepo{pool: pool}
}

// fileMetadataColumns is the canonical column order for full-row scans.
const fileMetadataColumns = `id, profile_id, sha256, byte_size, mime_type,
	declared_mime, filename, extension, storage_path, storage_kind,
	source_kind, source_message_id, source_external_url,
	is_text, is_binary, is_viewable, viewer_hint,
	thumbnail_path, thumbnail_sha256, preview_text, metadata_json,
	reference_count, last_accessed_at, access_count, quarantined,
	created_at, updated_at, deleted_at`

func scanFileMetadata(row pgx.Row, f *FileMetadata) error {
	return row.Scan(
		&f.ID, &f.ProfileID, &f.SHA256, &f.ByteSize, &f.MimeType,
		&f.DeclaredMime, &f.Filename, &f.Extension, &f.StoragePath, &f.StorageKind,
		&f.SourceKind, &f.SourceMessageID, &f.SourceExternalURL,
		&f.IsText, &f.IsBinary, &f.IsViewable, &f.ViewerHint,
		&f.ThumbnailPath, &f.ThumbnailSHA256, &f.PreviewText, &f.MetadataJSON,
		&f.ReferenceCount, &f.LastAccessedAt, &f.AccessCount, &f.Quarantined,
		&f.CreatedAt, &f.UpdatedAt, &f.DeletedAt,
	)
}

// Insert creates a new file_metadata row. A live duplicate
// (profile_id, sha256) maps to ErrDuplicate (the resolver's dedup signal);
// every other error is wrapped with context.
func (r *PGFileMetadataRepo) Insert(ctx context.Context, f *FileMetadata) (*FileMetadata, error) {
	q := queryer(r)
	row := q.QueryRow(ctx, `
        INSERT INTO file_metadata
            (profile_id, sha256, byte_size, mime_type, declared_mime,
             filename, extension, storage_path, storage_kind, source_kind,
             source_message_id, source_external_url,
             is_text, is_binary, is_viewable, viewer_hint,
             thumbnail_path, thumbnail_sha256, preview_text, metadata_json,
             reference_count, quarantined)
        VALUES ($1, $2, $3, $4, $5,
                $6, $7, $8, $9, $10,
                $11, $12,
                $13, $14, $15, $16,
                $17, $18, $19, COALESCE($20, '{}'::jsonb),
                $21, $22)
        RETURNING `+fileMetadataColumns,
		f.ProfileID, f.SHA256, f.ByteSize, f.MimeType, f.DeclaredMime,
		f.Filename, f.Extension, f.StoragePath, string(f.StorageKind), string(f.SourceKind),
		f.SourceMessageID, f.SourceExternalURL,
		f.IsText, f.IsBinary, f.IsViewable, f.ViewerHint,
		f.ThumbnailPath, f.ThumbnailSHA256, f.PreviewText, f.MetadataJSON,
		f.ReferenceCount, f.Quarantined,
	)
	var out FileMetadata
	if err := scanFileMetadata(row, &out); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicate
		}
		return nil, fmt.Errorf("fileviewer: insert file_metadata: %w", err)
	}
	return &out, nil
}

// GetByID returns the non-deleted file with the given ID.
func (r *PGFileMetadataRepo) GetByID(ctx context.Context, id uuid.UUID) (*FileMetadata, error) {
	row := queryer(r).QueryRow(ctx,
		`SELECT `+fileMetadataColumns+` FROM file_metadata WHERE id = $1 AND deleted_at IS NULL`, id)
	var f FileMetadata
	if err := scanFileMetadata(row, &f); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFileNotFound
		}
		return nil, fmt.Errorf("fileviewer: select file_metadata by id: %w", err)
	}
	return &f, nil
}

// GetByProfileAndHash returns the non-deleted file for (profile, sha256).
func (r *PGFileMetadataRepo) GetByProfileAndHash(ctx context.Context, profileID uuid.UUID, sha256 string) (*FileMetadata, error) {
	row := queryer(r).QueryRow(ctx,
		`SELECT `+fileMetadataColumns+` FROM file_metadata
         WHERE profile_id = $1 AND sha256 = $2 AND deleted_at IS NULL`,
		profileID, sha256)
	var f FileMetadata
	if err := scanFileMetadata(row, &f); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFileNotFoundByHash
		}
		return nil, fmt.Errorf("fileviewer: select file_metadata by hash: %w", err)
	}
	return &f, nil
}

// slimColumns is the canonical column order for list scans.
const slimColumns = `id, profile_id, sha256, byte_size, mime_type,
	filename, extension, storage_kind, is_text, is_viewable,
	viewer_hint, thumbnail_path, reference_count, last_accessed_at, created_at`

func scanSlim(row pgx.Row, s *FileMetadataSlim) error {
	return row.Scan(
		&s.ID, &s.ProfileID, &s.SHA256, &s.ByteSize, &s.MimeType,
		&s.Filename, &s.Extension, &s.StorageKind, &s.IsText, &s.IsViewable,
		&s.ViewerHint, &s.ThumbnailPath, &s.ReferenceCount, &s.LastAccessedAt, &s.CreatedAt,
	)
}

// orderBy maps a ListFilesOpts.Sort to SQL. Whitelist — never interpolate
// user input into ORDER BY.
func orderBy(sort string) string {
	switch sort {
	case "created_asc":
		return "created_at ASC, id ASC"
	case "name_asc":
		return "filename ASC, id ASC"
	case "size_desc":
		return "byte_size DESC, id ASC"
	case "last_accessed_desc":
		return "last_accessed_at DESC NULLS LAST, id ASC"
	default:
		return "created_at DESC, id ASC" // created_desc (spec default)
	}
}

// listFiles runs a profile-scoped paginated list.
func (r *PGFileMetadataRepo) listFiles(ctx context.Context, profileID uuid.UUID, opts ListFilesOpts) ([]FileMetadataSlim, error) {
	opts = clampListOpts(opts)

	var (
		conds = []string{"f.deleted_at IS NULL", "f.profile_id = $1"}
		args  = []any{profileID}
	)
	if opts.Cursor != nil {
		args = append(args, *opts.Cursor)
		conds = append(conds, fmt.Sprintf("f.id > $%d", len(args)))
	}
	if opts.MimeFilter != "" {
		args = append(args, "%"+opts.MimeFilter+"%")
		conds = append(conds, fmt.Sprintf("f.mime_type ILIKE $%d", len(args)))
	}
	if opts.ExtensionFilter != "" {
		args = append(args, opts.ExtensionFilter)
		conds = append(conds, fmt.Sprintf("f.extension = $%d", len(args)))
	}
	if opts.ViewableOnly {
		conds = append(conds, "f.is_viewable = true")
	}
	if opts.ExcludeQuarantined {
		conds = append(conds, "f.quarantined = false")
	}

	sql := `SELECT ` + slimColumns + ` FROM file_metadata f
        WHERE ` + strings.Join(conds, " AND ") + `
        ORDER BY ` + orderBy(opts.Sort) + `
        LIMIT ` + fmt.Sprint(opts.Limit)

	rows, err := queryer(r).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("fileviewer: list files: %w", err)
	}
	defer rows.Close()
	return collectSlim(rows)
}

// ListByProfile returns a paginated, filtered file list for a profile.
func (r *PGFileMetadataRepo) ListByProfile(ctx context.Context, profileID uuid.UUID, opts ListFilesOpts) ([]FileMetadataSlim, error) {
	return r.listFiles(ctx, profileID, opts)
}

// ListByTree returns files referenced by any node in a tree. Phase 1
// implements it over the tree's owning profile scope once the file-node
// join lands (BE-04); until then it is profile-unsupported and returns
// the tree-agnostic error, which the phase-1 API does not expose.
func (r *PGFileMetadataRepo) ListByTree(ctx context.Context, treeID uuid.UUID, opts ListFilesOpts) ([]FileMetadataSlim, error) {
	_ = treeID
	_ = opts
	return nil, errors.New("fileviewer: ListByTree requires the file-node join (phase 2, BE-04)")
}

// ListRecent returns the profile's most recently accessed non-deleted files.
func (r *PGFileMetadataRepo) ListRecent(ctx context.Context, profileID uuid.UUID, limit int) ([]FileMetadataSlim, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	rows, err := queryer(r).Query(ctx,
		`SELECT `+slimColumns+` FROM file_metadata f
         WHERE f.profile_id = $1 AND f.deleted_at IS NULL AND f.last_accessed_at IS NOT NULL
         ORDER BY f.last_accessed_at DESC NULLS LAST, f.id ASC
         LIMIT $2`, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("fileviewer: list recent files: %w", err)
	}
	defer rows.Close()
	return collectSlim(rows)
}

func collectSlim(rows pgx.Rows) ([]FileMetadataSlim, error) {
	var out []FileMetadataSlim
	for rows.Next() {
		var s FileMetadataSlim
		if err := scanSlim(rows, &s); err != nil {
			return nil, fmt.Errorf("fileviewer: scan file_metadata slim: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// IncrementReferenceCount atomically bumps reference_count.
func (r *PGFileMetadataRepo) IncrementReferenceCount(ctx context.Context, id uuid.UUID) error {
	return r.bumpRefCount(ctx, id, 1)
}

// DecrementReferenceCount atomically decrements reference_count, floored at 0.
func (r *PGFileMetadataRepo) DecrementReferenceCount(ctx context.Context, id uuid.UUID) error {
	return r.bumpRefCount(ctx, id, -1)
}

func (r *PGFileMetadataRepo) bumpRefCount(ctx context.Context, id uuid.UUID, delta int) error {
	tag, err := queryer(r).Exec(ctx,
		`UPDATE file_metadata
         SET reference_count = GREATEST(0, reference_count + $2),
             updated_at = clock_timestamp()
         WHERE id = $1 AND deleted_at IS NULL`,
		id, delta)
	if err != nil {
		return fmt.Errorf("fileviewer: bump reference_count: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFileNotFound
	}
	return nil
}

// UpdateLastAccessed stamps last_accessed_at and bumps access_count.
func (r *PGFileMetadataRepo) UpdateLastAccessed(ctx context.Context, id uuid.UUID) error {
	tag, err := queryer(r).Exec(ctx,
		`UPDATE file_metadata
         SET last_accessed_at = clock_timestamp(),
             access_count = access_count + 1
         WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("fileviewer: update last_accessed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFileNotFound
	}
	return nil
}

// UpdateThumbnail sets the thumbnail columns (thumbnail generation is a
// later phase; the repo method is part of the spec §4.3 interface).
func (r *PGFileMetadataRepo) UpdateThumbnail(ctx context.Context, id uuid.UUID, path, sha256 string) error {
	tag, err := queryer(r).Exec(ctx,
		`UPDATE file_metadata
         SET thumbnail_path = $2, thumbnail_sha256 = $3, updated_at = clock_timestamp()
         WHERE id = $1 AND deleted_at IS NULL`, id, path, sha256)
	if err != nil {
		return fmt.Errorf("fileviewer: update thumbnail: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFileNotFound
	}
	return nil
}

// SoftDelete sets deleted_at = now(). Row preserved for audit.
func (r *PGFileMetadataRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := queryer(r).Exec(ctx,
		`UPDATE file_metadata
         SET deleted_at = clock_timestamp(), updated_at = clock_timestamp()
         WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("fileviewer: soft-delete file_metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFileNotFound
	}
	return nil
}

// PurgeDeleted hard-deletes rows soft-deleted before the cutoff. Returns
// the number of rows removed.
func (r *PGFileMetadataRepo) PurgeDeleted(ctx context.Context, cutoff time.Time) (int, error) {
	tag, err := queryer(r).Exec(ctx,
		`DELETE FROM file_metadata WHERE deleted_at IS NOT NULL AND deleted_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("fileviewer: purge deleted files: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ProfileExists reports whether the profiles row exists (used by the
// resolver to return PROFILE_NOT_FOUND instead of a raw FK violation).
func (r *PGFileMetadataRepo) ProfileExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool
	err := queryer(r).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM profiles WHERE id = $1 AND deleted_at IS NULL)`, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("fileviewer: profile exists check: %w", err)
	}
	return exists, nil
}

// ResolveActorProfile maps an authenticated actor id (the JWT `sub`) to
// the profile id the API acts as. JWT `sub` is a users.id and a user's
// profile is a separate row (profiles.owner_id = users.id), but some
// callers historically passed a profile id directly. Deterministic rule,
// live rows only: (a) a profile whose id EQUALS the actor id wins;
// (b) otherwise the actor's newest owned profile (same ordering as
// PGProfileRepo.GetByOwner); (c) no row → pgx.ErrNoRows, which the
// service maps to ErrProfileNotFound.
func (r *PGFileMetadataRepo) ResolveActorProfile(ctx context.Context, actorID uuid.UUID) (uuid.UUID, error) {
	var profileID uuid.UUID
	err := queryer(r).QueryRow(ctx, `
        SELECT id FROM profiles
        WHERE deleted_at IS NULL AND (id = $1 OR owner_id = $1)
        ORDER BY (id = $1) DESC, created_at DESC
        LIMIT 1`, actorID).Scan(&profileID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("fileviewer: resolve actor profile: %w", err)
	}
	return profileID, nil
}

// WithTx runs fn inside a database transaction. The nested repo sees the
// transaction; the outer repo remains pool-bound.
func (r *PGFileMetadataRepo) WithTx(ctx context.Context, fn func(repo FileMetadataRepo) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("fileviewer: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(&PGFileMetadataRepo{pool: r.pool, tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// queryer returns the transaction when the repo is tx-bound, else the pool.
func queryer(r *PGFileMetadataRepo) interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
} {
	if r.tx != nil {
		return r.tx
	}
	return r.pool
}

// ── Viewer Registry Repo ────────────────────────────────────

// ViewerRegistryRepo is the persistence interface for viewer_registry.
type ViewerRegistryRepo interface {
	// UpsertAll replaces the entire active viewer set with the provided
	// descriptors. Called once per canopyd boot. Deactivates any viewer
	// not in the new list (is_active = false).
	UpsertAll(ctx context.Context, descriptors []BuiltInViewerDescriptor) error

	// GetBySlug returns the active viewer for a slug.
	GetBySlug(ctx context.Context, slug string) (*ViewerRegistration, error)

	// GetByID retrieves a viewer by ID.
	GetByID(ctx context.Context, id uuid.UUID) (*ViewerRegistration, error)

	// List returns all active viewers.
	List(ctx context.Context) ([]ViewerRegistration, error)

	// ListByMime returns active viewers that declare support for a MIME type.
	ListByMime(ctx context.Context, mime string) ([]ViewerRegistration, error)
}

// PGViewerRegistryRepo is the pgx-backed ViewerRegistryRepo.
type PGViewerRegistryRepo struct {
	pool *pgxpool.Pool
}

// NewPGViewerRegistryRepo wires the repo to a pgxpool.
func NewPGViewerRegistryRepo(pool *pgxpool.Pool) *PGViewerRegistryRepo {
	return &PGViewerRegistryRepo{pool: pool}
}

// viewerColumns is the canonical column order for viewer_registry scans.
const viewerColumns = `id, viewer_slug, version, canopyd_version, display_name,
	description, icon_url, render_type, supports_mime, supports_extensions,
	supports_viewer_hint, required_capabilities, bundle_path, bundle_byte_size,
	bundle_sha256, min_canopyd_version, deprecation_notice, is_active, installed_at`

func scanViewer(row pgx.Row, v *ViewerRegistration) error {
	return row.Scan(
		&v.ID, &v.ViewerSlug, &v.Version, &v.CanopydVersion, &v.DisplayName,
		&v.Description, &v.IconURL, &v.RenderType, &v.SupportsMime, &v.SupportsExtensions,
		&v.SupportsViewerHint, &v.RequiredCapabilities, &v.BundlePath, &v.BundleByteSize,
		&v.BundleSHA256, &v.MinCanopydVersion, &v.DeprecationNotice, &v.IsActive, &v.InstalledAt,
	)
}

// UpsertAll replaces the active viewer set with the provided descriptors,
// per spec §6.2: deactivate everything, upsert each descriptor, then
// deactivate stale same-slug rows — all in one transaction.
func (r *PGViewerRegistryRepo) UpsertAll(ctx context.Context, descriptors []BuiltInViewerDescriptor) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("fileviewer: begin viewer upsert tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Deactivate all currently-active viewers.
	if _, err := tx.Exec(ctx, `UPDATE viewer_registry SET is_active = false WHERE is_active = true`); err != nil {
		return fmt.Errorf("fileviewer: deactivate viewers: %w", err)
	}

	for _, d := range descriptors {
		// 2. Upsert each descriptor.
		if _, err := tx.Exec(ctx, `
            INSERT INTO viewer_registry
                (viewer_slug, version, canopyd_version, display_name, description,
                 icon_url, render_type, supports_mime, supports_extensions,
                 required_capabilities, bundle_path, bundle_byte_size, bundle_sha256,
                 min_canopyd_version, is_active)
            VALUES ($1, $2, $3, $4, $5,
                    $6, $7, $8, $9,
                    $10, $11, $12, $13,
                    $14, true)
            ON CONFLICT (viewer_slug, version) DO UPDATE SET
                display_name = EXCLUDED.display_name,
                description = EXCLUDED.description,
                supports_mime = EXCLUDED.supports_mime,
                supports_extensions = EXCLUDED.supports_extensions,
                bundle_sha256 = EXCLUDED.bundle_sha256,
                bundle_byte_size = EXCLUDED.bundle_byte_size,
                bundle_path = EXCLUDED.bundle_path,
                icon_url = EXCLUDED.icon_url,
                is_active = true,
                canopyd_version = EXCLUDED.canopyd_version`,
			d.ViewerSlug, d.Version, d.CanopydVersion, d.DisplayName, d.Description,
			d.IconURL, string(d.RenderType), d.SupportsMime, d.SupportsExtensions,
			[]string{}, d.BundlePath, d.BundleByteSize, d.BundleSHA256,
			d.MinCanopydVersion,
		); err != nil {
			return fmt.Errorf("fileviewer: upsert viewer %s: %w", d.ViewerSlug, err)
		}

		// 3. If the slug has a different version row active, deactivate it.
		if _, err := tx.Exec(ctx,
			`UPDATE viewer_registry SET is_active = false
             WHERE viewer_slug = $1 AND version != $2 AND is_active = true`,
			d.ViewerSlug, d.Version,
		); err != nil {
			return fmt.Errorf("fileviewer: deactivate stale viewer %s: %w", d.ViewerSlug, err)
		}
	}

	return tx.Commit(ctx)
}

// GetBySlug returns the active viewer for a slug.
func (r *PGViewerRegistryRepo) GetBySlug(ctx context.Context, slug string) (*ViewerRegistration, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+viewerColumns+` FROM viewer_registry
         WHERE viewer_slug = $1 AND is_active = true`, slug)
	var v ViewerRegistration
	if err := scanViewer(row, &v); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrViewerNotFound
		}
		return nil, fmt.Errorf("fileviewer: select viewer by slug: %w", err)
	}
	return &v, nil
}

// GetByID retrieves a viewer by ID.
func (r *PGViewerRegistryRepo) GetByID(ctx context.Context, id uuid.UUID) (*ViewerRegistration, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+viewerColumns+` FROM viewer_registry WHERE id = $1`, id)
	var v ViewerRegistration
	if err := scanViewer(row, &v); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrViewerNotFound
		}
		return nil, fmt.Errorf("fileviewer: select viewer by id: %w", err)
	}
	return &v, nil
}

// List returns all active viewers.
func (r *PGViewerRegistryRepo) List(ctx context.Context) ([]ViewerRegistration, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+viewerColumns+` FROM viewer_registry
         WHERE is_active = true ORDER BY viewer_slug ASC`)
	if err != nil {
		return nil, fmt.Errorf("fileviewer: list viewers: %w", err)
	}
	defer rows.Close()
	var out []ViewerRegistration
	for rows.Next() {
		var v ViewerRegistration
		if err := scanViewer(rows, &v); err != nil {
			return nil, fmt.Errorf("fileviewer: scan viewer: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListByMime returns active viewers that declare support for a MIME type.
func (r *PGViewerRegistryRepo) ListByMime(ctx context.Context, mime string) ([]ViewerRegistration, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+viewerColumns+` FROM viewer_registry
         WHERE is_active = true AND $1 = ANY(supports_mime)
         ORDER BY viewer_slug ASC`, mime)
	if err != nil {
		return nil, fmt.Errorf("fileviewer: list viewers by mime: %w", err)
	}
	defer rows.Close()
	var out []ViewerRegistration
	for rows.Next() {
		var v ViewerRegistration
		if err := scanViewer(rows, &v); err != nil {
			return nil, fmt.Errorf("fileviewer: scan viewer: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ── File Access Log Repo ────────────────────────────────────

// FileAccessLogRepo is the append-only interface for file_access_log.
type FileAccessLogRepo interface {
	// Append inserts a new access log entry. UPDATE and DELETE are
	// forbidden by trigger (migration 000045).
	Append(ctx context.Context, entry *FileAccessEntry) error

	// GetRecent returns recent access entries for a profile.
	GetRecent(ctx context.Context, profileID uuid.UUID, limit int) ([]FileAccessEntry, error)

	// GetByFile returns access entries for a specific file.
	GetByFile(ctx context.Context, fileID uuid.UUID, limit int) ([]FileAccessEntry, error)

	// GetStats returns viewer usage stats (which viewers are used, how often).
	GetStats(ctx context.Context, profileID uuid.UUID, since time.Time) (map[string]int, error)
}

// PGFileAccessLogRepo is the pgx-backed FileAccessLogRepo.
type PGFileAccessLogRepo struct {
	pool *pgxpool.Pool
}

// NewPGFileAccessLogRepo wires the repo to a pgxpool.
func NewPGFileAccessLogRepo(pool *pgxpool.Pool) *PGFileAccessLogRepo {
	return &PGFileAccessLogRepo{pool: pool}
}

// Append inserts one access-log row. The row ID (uuidv7) is written back
// into the entry.
func (r *PGFileAccessLogRepo) Append(ctx context.Context, entry *FileAccessEntry) error {
	if entry == nil {
		return errors.New("fileviewer: access log entry is nil")
	}
	var clientInfo []byte
	if len(entry.ClientInfo) == 0 {
		clientInfo = []byte("{}")
	} else {
		clientInfo = entry.ClientInfo
	}
	return r.pool.QueryRow(ctx, `
        INSERT INTO file_access_log
            (file_id, profile_id, tree_id, node_id, viewer_slug, action,
             duration_ms, byte_offset, client_info, error_code)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
        RETURNING id, created_at`,
		entry.FileID, entry.ProfileID, entry.TreeID, entry.NodeID,
		entry.ViewerSlug, string(entry.Action),
		entry.DurationMs, entry.ByteOffset, clientInfo, entry.ErrorCode,
	).Scan(&entry.ID, &entry.CreatedAt)
}

func scanAccessEntry(row pgx.Row, e *FileAccessEntry) error {
	return row.Scan(
		&e.ID, &e.FileID, &e.ProfileID, &e.TreeID, &e.NodeID,
		&e.ViewerSlug, &e.Action, &e.DurationMs, &e.ByteOffset,
		&e.ClientInfo, &e.ErrorCode, &e.CreatedAt,
	)
}

const accessEntryColumns = `id, file_id, profile_id, tree_id, node_id,
	viewer_slug, action, duration_ms, byte_offset, client_info, error_code, created_at`

// GetRecent returns recent access entries for a profile, newest first.
func (r *PGFileAccessLogRepo) GetRecent(ctx context.Context, profileID uuid.UUID, limit int) ([]FileAccessEntry, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+accessEntryColumns+` FROM file_access_log
         WHERE profile_id = $1
         ORDER BY created_at DESC
         LIMIT $2`, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("fileviewer: access log by profile: %w", err)
	}
	defer rows.Close()
	return collectAccessEntries(rows)
}

// GetByFile returns access entries for a specific file, newest first.
func (r *PGFileAccessLogRepo) GetByFile(ctx context.Context, fileID uuid.UUID, limit int) ([]FileAccessEntry, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+accessEntryColumns+` FROM file_access_log
         WHERE file_id = $1
         ORDER BY created_at DESC
         LIMIT $2`, fileID, limit)
	if err != nil {
		return nil, fmt.Errorf("fileviewer: access log by file: %w", err)
	}
	defer rows.Close()
	return collectAccessEntries(rows)
}

// GetStats returns viewer usage counts since the given time for a profile.
func (r *PGFileAccessLogRepo) GetStats(ctx context.Context, profileID uuid.UUID, since time.Time) (map[string]int, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT viewer_slug, count(*)::int FROM file_access_log
         WHERE profile_id = $1 AND created_at >= $2
         GROUP BY viewer_slug`, profileID, since)
	if err != nil {
		return nil, fmt.Errorf("fileviewer: access log stats: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var slug string
		var n int
		if err := rows.Scan(&slug, &n); err != nil {
			return nil, fmt.Errorf("fileviewer: scan access stats: %w", err)
		}
		out[slug] = n
	}
	return out, rows.Err()
}

func collectAccessEntries(rows pgx.Rows) ([]FileAccessEntry, error) {
	var out []FileAccessEntry
	for rows.Next() {
		var e FileAccessEntry
		if err := scanAccessEntry(rows, &e); err != nil {
			return nil, fmt.Errorf("fileviewer: scan access entry: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
