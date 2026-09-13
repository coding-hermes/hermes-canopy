package fileviewer

import (
	"context"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
	"github.com/google/uuid"
)

// newFixtures builds the repos over the shared integration pool plus one
// owner profile for FK targets. NewSharedIntegrationPool truncates all
// tables on every call — do NOT add per-test TruncateAll defers (regression
// trap: duplicated the reset and slowed the suite; see testutil TEST-004).
func newFixtures(t *testing.T) (*PGFileMetadataRepo, *PGViewerRegistryRepo, *PGFileAccessLogRepo, uuid.UUID, *FileStore) {
	t.Helper()
	pool := testutil.NewSharedIntegrationPool(t)

	fvFilesRepo := NewPGFileMetadataRepo(pool)
	fvViewersRepo := NewPGViewerRegistryRepo(pool)
	fvAccessRepo := NewPGFileAccessLogRepo(pool)

	profileID := seedProfile(t, pool)

	return fvFilesRepo, fvViewersRepo, fvAccessRepo, profileID, NewFileStore(t.TempDir())
}

func mkMeta(profileID uuid.UUID, shaHex string) *FileMetadata {
	return &FileMetadata{
		ProfileID:      profileID,
		SHA256:         shaHex,
		ByteSize:       11,
		MimeType:       "text/plain",
		DeclaredMime:   "",
		Filename:       "notes.txt",
		Extension:      "txt",
		StoragePath:    "files/no/" + shaHex,
		StorageKind:    StorageKindHermesKB,
		SourceKind:     SourceKindUpload,
		IsText:         true,
		IsBinary:       false,
		IsViewable:     true,
		ViewerHint:     "code",
		ReferenceCount: 1,
	}
}

// withName overrides the filename on a fixture row.
func withName(f *FileMetadata, name string) *FileMetadata {
	f.Filename = name
	f.Extension = NormalizeExtension(name)
	return f
}

func TestFileMetadataRepo_CRUD(t *testing.T) {
	files, _, _, profileID, _ := newFixtures(t)
	ctx := context.Background()

	sha := sha256Of(t, "hello world")

	got, err := files.Insert(ctx, mkMeta(profileID, sha))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if got.ID == uuid.Nil {
		t.Fatal("expected uuidv7 default to populate id")
	}
	if got.ReferenceCount != 1 || got.MimeType != "text/plain" || got.ViewerHint != "code" {
		t.Fatalf("unexpected row: %+v", got)
	}

	byID, err := files.GetByID(ctx, got.ID)
	if err != nil || byID.SHA256 != sha {
		t.Fatalf("GetByID: %v %+v", err, byID)
	}

	byHash, err := files.GetByProfileAndHash(ctx, profileID, sha)
	if err != nil || byHash.ID != got.ID {
		t.Fatalf("GetByProfileAndHash: %v %+v", err, byHash)
	}

	if _, err := files.GetByProfileAndHash(ctx, profileID, sha256Of(t, "other")); err != ErrFileNotFoundByHash {
		t.Fatalf("expected ErrFileNotFoundByHash, got %v", err)
	}
}

func TestFileMetadataRepo_DedupUniquePerProfileHash(t *testing.T) {
	files, _, _, profileID, _ := newFixtures(t)
	ctx := context.Background()

	sha := sha256Of(t, "duplicate bytes")
	if _, err := files.Insert(ctx, mkMeta(profileID, sha)); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := files.Insert(ctx, mkMeta(profileID, sha)); err != ErrDuplicate {
		t.Fatalf("expected ErrDuplicate on same (profile, sha), got %v", err)
	}

	// Same bytes, different profile → different row (spec §3.1).
	otherProfile := seedProfile(t, poolOf(t, files))
	if _, err := files.Insert(ctx, mkMeta(otherProfile, sha)); err != nil {
		t.Fatalf("insert for second profile: %v", err)
	}
}

func TestFileMetadataRepo_ReferenceCounting(t *testing.T) {
	files, _, _, profileID, _ := newFixtures(t)
	ctx := context.Background()

	row, err := files.Insert(ctx, mkMeta(profileID, sha256Of(t, "refcount")))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := files.IncrementReferenceCount(ctx, row.ID); err != nil {
		t.Fatalf("IncrementReferenceCount: %v", err)
	}
	got, _ := files.GetByID(ctx, row.ID)
	if got.ReferenceCount != 2 {
		t.Fatalf("expected refcount 2 after increment, got %d", got.ReferenceCount)
	}

	if err := files.DecrementReferenceCount(ctx, row.ID); err != nil {
		t.Fatalf("DecrementReferenceCount: %v", err)
	}
	if err := files.DecrementReferenceCount(ctx, row.ID); err != nil {
		t.Fatalf("DecrementReferenceCount: %v", err)
	}
	got, _ = files.GetByID(ctx, row.ID)
	if got.ReferenceCount != 0 {
		t.Fatalf("expected refcount floored at 0, got %d", got.ReferenceCount)
	}
}

func TestFileMetadataRepo_SoftDelete(t *testing.T) {
	files, _, _, profileID, _ := newFixtures(t)
	ctx := context.Background()

	row, err := files.Insert(ctx, mkMeta(profileID, sha256Of(t, "soft-deleted")))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := files.SoftDelete(ctx, row.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	// GetByID filters deleted rows.
	if _, err := files.GetByID(ctx, row.ID); err != ErrFileNotFound {
		t.Fatalf("expected ErrFileNotFound after soft delete, got %v", err)
	}
	// Hash lookups also filter deleted rows — a re-upload of the same
	// bytes creates a NEW row (spec §7.4).
	if _, err := files.GetByProfileAndHash(ctx, profileID, row.SHA256); err != ErrFileNotFoundByHash {
		t.Fatalf("expected ErrFileNotFoundByHash after soft delete, got %v", err)
	}
	if _, err := files.Insert(ctx, mkMeta(profileID, row.SHA256)); err != nil {
		t.Fatalf("re-insert after soft delete should succeed (partial unique index), got %v", err)
	}

	// PurgeDeleted removes rows soft-deleted before the cutoff. (The shared
	// pool truncates between tests, so only this test's soft-deleted row
	// exists.)
	n, err := files.PurgeDeleted(ctx, timeFuture())
	if err != nil {
		t.Fatalf("PurgeDeleted: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 purged row, got %d", n)
	}
}

func TestFileMetadataRepo_ListAndRecents(t *testing.T) {
	files, _, _, profileID, _ := newFixtures(t)
	ctx := context.Background()

	a, _ := files.Insert(ctx, withName(mkMeta(profileID, sha256Of(t, "file-a")), "file-a.txt"))
	b, _ := files.Insert(ctx, withName(mkMeta(profileID, sha256Of(t, "file-b")), "file-b.txt"))
	if _, err := files.Insert(ctx, withName(mkMeta(profileID, sha256Of(t, "file-c")), "file-c.txt")); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Recents only includes rows with last_accessed_at set.
	if err := files.UpdateLastAccessed(ctx, b.ID); err != nil {
		t.Fatalf("UpdateLastAccessed: %v", err)
	}
	recents, err := files.ListRecent(ctx, profileID, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(recents) != 1 || recents[0].ID != b.ID {
		t.Fatalf("expected only file-b in recents, got %+v", recents)
	}

	got, err := files.ListByProfile(ctx, profileID, ListFilesOpts{Limit: 2, Sort: "name_asc"})
	if err != nil {
		t.Fatalf("ListByProfile: %v", err)
	}
	if len(got) != 2 || got[0].Filename >= got[1].Filename {
		t.Fatalf("expected name_asc page of 2, got %+v", got)
	}

	// Cursor pagination: continue after a.
	page2, err := files.ListByProfile(ctx, profileID, ListFilesOpts{Limit: 10, Sort: "name_asc", Cursor: &a.ID})
	if err != nil {
		t.Fatalf("ListByProfile cursor: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("expected 2 rows after cursor, got %d", len(page2))
	}

	// Filter: extension exact match.
	png := mkMeta(profileID, sha256Of(t, "png-bytes"))
	png.Filename, png.Extension, png.MimeType, png.IsText, png.ViewerHint = "pic.png", "png", "image/png", false, "image"
	if _, err := files.Insert(ctx, png); err != nil {
		t.Fatalf("Insert png: %v", err)
	}
	byExt, err := files.ListByProfile(ctx, profileID, ListFilesOpts{ExtensionFilter: "png"})
	if err != nil || len(byExt) != 1 || byExt[0].Extension != "png" {
		t.Fatalf("extension filter: %v %+v", err, byExt)
	}
}

func TestViewerRegistryRepo_UpsertAllAndLookup(t *testing.T) {
	_, viewers, _, _, _ := newFixtures(t)
	ctx := context.Background()

	if err := viewers.UpsertAll(ctx, seededDescriptors()); err != nil {
		t.Fatalf("UpsertAll: %v", err)
	}
	list, err := viewers.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != len(AllBuiltInViewers) {
		t.Fatalf("expected %d active viewers, got %d", len(AllBuiltInViewers), len(list))
	}
	slugs := map[string]bool{}
	for _, v := range list {
		slugs[v.ViewerSlug] = true
		if !v.IsActive {
			t.Fatalf("viewer %s should be active", v.ViewerSlug)
		}
	}
	for _, want := range []string{"pdf", "image", "code", "csv", "markdown", "json", "audio_video"} {
		if !slugs[want] {
			t.Fatalf("missing built-in viewer %q", want)
		}
	}

	pdf, err := viewers.GetBySlug(ctx, "pdf")
	if err != nil || pdf.DisplayName != "PDF" {
		t.Fatalf("GetBySlug pdf: %v %+v", err, pdf)
	}

	// Idempotent: second boot keeps one active row per slug (no duplicates).
	if err := viewers.UpsertAll(ctx, seededDescriptors()); err != nil {
		t.Fatalf("UpsertAll again: %v", err)
	}
	list2, _ := viewers.List(ctx)
	if len(list2) != len(AllBuiltInViewers) {
		t.Fatalf("expected stable %d active viewers after reseed, got %d", len(AllBuiltInViewers), len(list2))
	}

	// ListByMime.
	byMime, err := viewers.ListByMime(ctx, "application/pdf")
	if err != nil || len(byMime) != 1 || byMime[0].ViewerSlug != "pdf" {
		t.Fatalf("ListByMime: %v %+v", err, byMime)
	}
}

func TestFileAccessLogRepo_AppendAndImmutability(t *testing.T) {
	files, _, access, profileID, _ := newFixtures(t)
	ctx := context.Background()

	row, err := files.Insert(ctx, mkMeta(profileID, sha256Of(t, "audited")))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	entry := &FileAccessEntry{
		FileID:     row.ID,
		ProfileID:  profileID,
		ViewerSlug: "pdf",
		Action:     AccessActionOpen,
		ClientInfo: []byte(`{"user_agent":"test"}`),
	}
	if err := access.Append(ctx, entry); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if entry.ID == uuid.Nil {
		t.Fatal("expected returned entry id")
	}

	byFile, err := access.GetByFile(ctx, row.ID, 10)
	if err != nil || len(byFile) != 1 || byFile[0].Action != AccessActionOpen {
		t.Fatalf("GetByFile: %v %+v", err, byFile)
	}
	byProfile, err := access.GetRecent(ctx, profileID, 10)
	if err != nil || len(byProfile) != 1 {
		t.Fatalf("GetRecent: %v %+v", err, byProfile)
	}
	stats, err := access.GetStats(ctx, profileID, timePast())
	if err != nil || stats["pdf"] != 1 {
		t.Fatalf("GetStats: %v %+v", err, stats)
	}

	// Append-only enforcement: UPDATE and DELETE must be rejected by trigger
	// (spec §3.6 / test 29).
	if _, err := poolOf(t, files).Exec(ctx,
		`UPDATE file_access_log SET viewer_slug = 'code' WHERE id = $1`, entry.ID); err == nil {
		t.Fatal("expected UPDATE on file_access_log to be rejected")
	}
	if _, err := poolOf(t, files).Exec(ctx,
		`DELETE FROM file_access_log WHERE id = $1`, entry.ID); err == nil {
		t.Fatal("expected DELETE on file_access_log to be rejected")
	}
}
