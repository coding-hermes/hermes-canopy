package fileviewer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// fakeStore is a FileStore pointed at a temp dir (created per test).
func testStore(t *testing.T) *FileStore {
	t.Helper()
	return NewFileStore(t.TempDir())
}

func TestFileStore_StoreAndDedup(t *testing.T) {
	store := testStore(t)

	res, err := store.Store(strings.NewReader("hello storage"), 1024)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if res.Deduped {
		t.Fatal("first store must not be deduped")
	}
	if res.SHA256 != sha256Of(t, "hello storage") || res.ByteSize != 13 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !store.Has(res.SHA256) {
		t.Fatal("content should exist after store")
	}

	// Same bytes → deduped at the storage layer.
	res2, err := store.Store(strings.NewReader("hello storage"), 1024)
	if err != nil || !res2.Deduped || res2.SHA256 != res.SHA256 {
		t.Fatalf("second store should dedup: %v %+v", err, res2)
	}
}

func TestFileStore_Limits(t *testing.T) {
	store := testStore(t)

	if _, err := store.Store(strings.NewReader(""), 1024); !errors.Is(err, ErrFileEmpty) {
		t.Fatalf("empty upload should be ErrFileEmpty, got %v", err)
	}
	if _, err := store.Store(strings.NewReader("0123456789"), 5); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized upload should be ErrFileTooLarge, got %v", err)
	}
	// Exactly at the limit passes.
	if _, err := store.Store(strings.NewReader("12345"), 5); err != nil {
		t.Fatalf("max-size upload should pass, got %v", err)
	}
	// Failed uploads must not leave content behind.
	if store.Has(sha256Of(t, "0123456789")) {
		t.Fatal("aborted upload leaked content bytes")
	}
}

// newTestService wires a service over the shared pool with a temp store and
// seeded viewer registry.
func newTestService(t *testing.T) (*serviceImpl, uuid.UUID) {
	t.Helper()
	files, viewers, access, profileID, store := newFixtures(t)
	svc := NewService(files, viewers, access, store).(*serviceImpl)
	if err := svc.SeedViewerRegistry(context.Background()); err != nil {
		t.Fatalf("SeedViewerRegistry: %v", err)
	}
	return svc, profileID
}

func TestResolver_UploadAndDedup(t *testing.T) {
	svc, profileID := newTestService(t)
	ctx := context.Background()

	content := "name,age\nalice,42\n"
	out, err := svc.ResolveByUpload(ctx, FileUpload{
		ProfileID:  profileID,
		Filename:   "table.csv",
		ByteStream: strings.NewReader(content),
	})
	if err != nil {
		t.Fatalf("ResolveByUpload: %v", err)
	}
	if !out.WasNewUpload || out.WasDeduped {
		t.Fatalf("first upload flags wrong: %+v", out)
	}
	if out.FileMetadata.MimeType != "text/csv" || out.FileMetadata.Extension != "csv" {
		t.Fatalf("classification wrong: %+v", out.FileMetadata)
	}
	if out.FileMetadata.ViewerHint != "csv" || !out.FileMetadata.IsViewable {
		t.Fatalf("viewer hint wrong: %+v", out.FileMetadata)
	}
	if out.FileMetadata.ReferenceCount != 1 {
		t.Fatalf("reference_count should start at 1")
	}

	// Same bytes again → dedup, refcount bumped, same file id (spec test 4).
	out2, err := svc.ResolveByUpload(ctx, FileUpload{
		ProfileID:  profileID,
		Filename:   "table-copy.csv", // different name, same bytes
		ByteStream: strings.NewReader(content),
	})
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if out2.WasNewUpload || !out2.WasDeduped {
		t.Fatalf("second upload flags wrong: %+v", out2)
	}
	if out2.FileMetadata.ID != out.FileMetadata.ID {
		t.Fatalf("dedup must return the same file id")
	}
	after, _ := svc.files.GetByID(ctx, out.FileMetadata.ID)
	if after.ReferenceCount != 2 {
		t.Fatalf("reference_count should be 2 after dedup, got %d", after.ReferenceCount)
	}
}

func TestResolver_UploadValidation(t *testing.T) {
	svc, profileID := newTestService(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		upload  FileUpload
		wantErr error
	}{
		{"empty file", FileUpload{ProfileID: profileID, Filename: "e.txt", ByteStream: strings.NewReader("")}, ErrFileEmpty},
		{"path separator", FileUpload{ProfileID: profileID, Filename: "../../etc/passwd", ByteStream: strings.NewReader("x")}, ErrFilenameInvalid},
		{"too long name", FileUpload{ProfileID: profileID, Filename: strings.Repeat("a", 1001), ByteStream: strings.NewReader("x")}, ErrFilenameInvalid},
		{"missing profile", FileUpload{Filename: "x.txt", ByteStream: strings.NewReader("x")}, ErrProfileNotFound},
		{"no stream", FileUpload{ProfileID: profileID, Filename: "x.txt"}, ErrFileEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.ResolveByUpload(ctx, tc.upload); !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestResolver_ByHash(t *testing.T) {
	svc, profileID := newTestService(t)
	ctx := context.Background()

	// Miss → FILE_NOT_FOUND_BY_HASH (spec test 2).
	if _, err := svc.ResolveByHash(ctx, profileID, sha256Of(t, "missing")); !errors.Is(err, ErrFileNotFoundByHash) {
		t.Fatalf("want ErrFileNotFoundByHash, got %v", err)
	}

	// Upload then resolve by hash.
	up, err := svc.ResolveByUpload(ctx, FileUpload{ProfileID: profileID, Filename: "doc.md", ByteStream: strings.NewReader("# hi\n")})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	res, err := svc.ResolveByHash(ctx, profileID, up.FileMetadata.SHA256)
	if err != nil {
		t.Fatalf("ResolveByHash: %v", err)
	}
	if res.WasNewUpload || res.WasDeduped || res.FileMetadata.ID != up.FileMetadata.ID {
		t.Fatalf("unexpected by-hash output: %+v", res)
	}
	if res.StreamURL == "" {
		t.Fatal("stream_url must be set")
	}
}

func TestResolver_ResolveBatch(t *testing.T) {
	svc, profileID := newTestService(t)
	ctx := context.Background()

	up, err := svc.ResolveByUpload(ctx, FileUpload{ProfileID: profileID, Filename: "b.txt", ByteStream: strings.NewReader("batch")})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	inputs := []ResolveFileInput{
		{HashRef: &HashRef{ProfileID: profileID, SHA256: up.FileMetadata.SHA256}},
		{HashRef: &HashRef{ProfileID: profileID, SHA256: sha256Of(t, "nope")}},
	}
	outs, err := svc.ResolveBatch(ctx, inputs)
	if err != nil {
		t.Fatalf("ResolveBatch: %v", err)
	}
	if len(outs) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(outs))
	}
	if outs[0].FileMetadata == nil || outs[0].FileMetadata.ID != up.FileMetadata.ID {
		t.Fatalf("first batch output should resolve: %+v", outs[0])
	}
	if outs[1].FileMetadata != nil {
		t.Fatalf("missing hash should produce an empty output, got %+v", outs[1])
	}
}

func TestService_ResolveViewerDispatch(t *testing.T) {
	svc, profileID := newTestService(t)
	ctx := context.Background()

	up, err := svc.ResolveByUpload(ctx, FileUpload{ProfileID: profileID, Filename: "song-notes.txt", ByteStream: strings.NewReader("plain text")})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	res, err := svc.ResolveViewer(ctx, up.FileMetadata, profileID, nil)
	if err != nil {
		t.Fatalf("ResolveViewer: %v", err)
	}
	if res.ViewerSlug != "code" || !res.IsBuiltIn || res.RenderType != ViewerRenderFullscreen {
		t.Fatalf("unexpected dispatch: %+v", res)
	}

	// Unsupported binary → VIEWER_NOT_FOUND (tier 7 → download-only).
	bin := &FileMetadata{ID: uuid.New(), MimeType: "application/octet-stream", Extension: "", ViewerHint: ""}
	if _, err := svc.ResolveViewer(ctx, bin, profileID, nil); !errors.Is(err, ErrViewerNotFound) {
		t.Fatalf("want ErrViewerNotFound, got %v", err)
	}
}

func TestService_SoftDeleteThenReupload(t *testing.T) {
	svc, profileID := newTestService(t)
	ctx := context.Background()

	up, err := svc.ResolveByUpload(ctx, FileUpload{ProfileID: profileID, Filename: "gone.txt", ByteStream: strings.NewReader("delete me")})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if err := svc.SoftDeleteFile(ctx, up.FileMetadata.ID, profileID); err != nil {
		t.Fatalf("SoftDeleteFile: %v", err)
	}
	if _, err := svc.files.GetByID(ctx, up.FileMetadata.ID); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("deleted file must 404, got %v", err)
	}
	// Re-upload creates a NEW row (spec §7.4 "soft-deleted hash" row).
	up2, err := svc.ResolveByUpload(ctx, FileUpload{ProfileID: profileID, Filename: "gone.txt", ByteStream: strings.NewReader("delete me")})
	if err != nil {
		t.Fatalf("re-upload: %v", err)
	}
	if up2.FileMetadata.ID == up.FileMetadata.ID || !up2.WasNewUpload {
		t.Fatalf("re-upload should create a new row, got %+v", up2)
	}
}
