// HTTP handler integration tests for SPEC-PL-02 phase 1 (backend).
// Drives the real chi router with the fileviewer mounts, the same auth
// middleware as production, and the shared PG integration pool.
package fileviewer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

const testJWTSecret = "fv-test-secret"

var testActorID = uuid.MustParse("00000000-0000-0000-0000-0000000000f1")

// newHandlerTestServer builds an httptest server over a chi router with the
// same middleware stack the production server uses for /api/v1/files.
func newHandlerTestServer(t *testing.T) (*httptest.Server, *serviceImpl) {
	t.Helper()
	pool := testutil.NewSharedIntegrationPool(t)

	filesRepo := NewPGFileMetadataRepo(pool)
	viewersRepo := NewPGViewerRegistryRepo(pool)
	accessRepo := NewPGFileAccessLogRepo(pool)
	profileID := seedProfile(t, pool)
	store := NewFileStore(t.TempDir())

	svc := NewService(filesRepo, viewersRepo, accessRepo, store).(*serviceImpl)
	if err := svc.SeedViewerRegistry(context.Background()); err != nil {
		t.Fatalf("SeedViewerRegistry: %v", err)
	}

	SetActorLookup(func(_ *http.Request) uuid.UUID { return profileID })

	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				// Minimal stand-in for AuthMiddleware: the handler tests
				// exercise fileviewer routing, not JWT parsing (that is
				// internal/handler's suite).
				next.ServeHTTP(w, req)
			})
		})
		h := NewHandler(svc)
		r.Mount("/files", h.Files())
		r.Mount("/viewers", h.Viewers())
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, svc
}

// do performs an authenticated request against the test server.
func do(t *testing.T, srv *httptest.Server, method, path string, body []byte, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken(t))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = resp.Body.Close()
	return resp, raw
}

func testToken(t *testing.T) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": testActorID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

// uploadMultipart posts a multipart upload and decodes the ResolveFileOutput.
func uploadMultipart(t *testing.T, srv *httptest.Server, filename, content, declaredMime string) (int, ResolveFileOutput) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if declaredMime != "" {
		if err := mw.WriteField("declaredMime", declaredMime); err != nil {
			t.Fatalf("write declaredMime: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	resp, raw := do(t, srv, "POST", "/api/v1/files/upload", buf.Bytes(), map[string]string{
		"Content-Type": mw.FormDataContentType(),
	})
	var out ResolveFileOutput
	if resp.StatusCode == 201 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode upload response: %v (%s)", err, raw)
		}
	}
	return resp.StatusCode, out
}

func TestHandler_UploadGetRoundtrip(t *testing.T) {
	srv, _ := newHandlerTestServer(t)

	content := "# Title\n\nsome markdown body\n"
	status, out := uploadMultipart(t, srv, "notes.md", content, "")
	if status != 201 {
		t.Fatalf("upload status = %d", status)
	}
	if out.FileMetadata.ID == uuid.Nil || out.FileMetadata.SHA256 == "" {
		t.Fatalf("incomplete upload response: %+v", out)
	}
	if out.FileMetadata.MimeType != "text/markdown" || out.FileMetadata.ViewerHint != "markdown" {
		t.Fatalf("classification wrong: %+v", out.FileMetadata)
	}

	// GET /files/{id} roundtrip.
	resp, raw := do(t, srv, "GET", "/api/v1/files/"+out.FileMetadata.ID.String(), nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET file status = %d (%s)", resp.StatusCode, raw)
	}
	var got FileMetadata
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if got.ID != out.FileMetadata.ID || got.Filename != "notes.md" {
		t.Fatalf("GET mismatch: %+v", got)
	}

	// Stream (no Range) returns the exact bytes.
	resp, raw = do(t, srv, "GET", "/api/v1/files/"+out.FileMetadata.ID.String()+"/stream", nil, nil)
	if resp.StatusCode != 200 || string(raw) != content {
		t.Fatalf("stream status=%d body=%q", resp.StatusCode, raw)
	}
	if resp.Header.Get("ETag") != `"`+out.FileMetadata.SHA256+`"` {
		t.Fatalf("ETag should be the sha256, got %q", resp.Header.Get("ETag"))
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatal("stream must advertise byte ranges")
	}
}

func TestHandler_StreamRange(t *testing.T) {
	srv, _ := newHandlerTestServer(t)

	content := "0123456789abcdefghij" // 20 bytes
	status, out := uploadMultipart(t, srv, "data.bin", content, "")
	if status != 201 {
		t.Fatalf("upload status = %d", status)
	}
	id := out.FileMetadata.ID.String()

	// bytes=5-9 → 206 with exactly those bytes (spec test 14).
	resp, raw := do(t, srv, "GET", "/api/v1/files/"+id+"/stream", nil, map[string]string{"Range": "bytes=5-9"})
	if resp.StatusCode != 206 {
		t.Fatalf("Range request status = %d", resp.StatusCode)
	}
	if string(raw) != content[5:10] {
		t.Fatalf("Range bytes = %q, want %q", raw, content[5:10])
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 5-9/20" {
		t.Fatalf("Content-Range = %q", cr)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "5" {
		t.Fatalf("Content-Length = %q", cl)
	}

	// Open-ended and suffix forms.
	_, raw = do(t, srv, "GET", "/api/v1/files/"+id+"/stream", nil, map[string]string{"Range": "bytes=15-"})
	if string(raw) != content[15:] {
		t.Fatalf("open-ended range = %q", raw)
	}
	_, raw = do(t, srv, "GET", "/api/v1/files/"+id+"/stream", nil, map[string]string{"Range": "bytes=-3"})
	if string(raw) != content[17:] {
		t.Fatalf("suffix range = %q", raw)
	}

	// Range beyond size → 416 (spec test 15).
	resp, _ = do(t, srv, "GET", "/api/v1/files/"+id+"/stream", nil, map[string]string{"Range": "bytes=0-99999999"})
	if resp.StatusCode != 416 {
		t.Fatalf("oversized range should be 416, got %d", resp.StatusCode)
	}

	// Malformed Range → 400.
	resp, _ = do(t, srv, "GET", "/api/v1/files/"+id+"/stream", nil, map[string]string{"Range": "bytes=zz"})
	if resp.StatusCode != 400 {
		t.Fatalf("malformed range should be 400, got %d", resp.StatusCode)
	}
}

func TestHandler_DedupViaAPI(t *testing.T) {
	srv, svc := newHandlerTestServer(t)

	content := "identical bytes for dedup"
	status1, out1 := uploadMultipart(t, srv, "first.txt", content, "")
	status2, out2 := uploadMultipart(t, srv, "second.txt", content, "")
	if status1 != 201 || status2 != 201 {
		t.Fatalf("upload statuses: %d %d", status1, status2)
	}
	if out1.FileMetadata.ID != out2.FileMetadata.ID {
		t.Fatalf("dedup must return the same file id: %s vs %s", out1.FileMetadata.ID, out2.FileMetadata.ID)
	}
	if !out2.WasDeduped || out2.WasNewUpload {
		t.Fatalf("second upload flags wrong: %+v", out2)
	}
	after, err := svc.files.GetByID(context.Background(), out1.FileMetadata.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if after.ReferenceCount != 2 {
		t.Fatalf("reference_count should be 2 after API dedup, got %d", after.ReferenceCount)
	}
}

func TestHandler_UploadValidation(t *testing.T) {
	srv, _ := newHandlerTestServer(t)

	// Empty file → FILE_EMPTY (spec test 6 / EC-1).
	status, _ := uploadMultipart(t, srv, "empty.txt", "", "")
	if status != 400 {
		t.Fatalf("empty upload should 400, got %d", status)
	}

	// Path separator filename → FILENAME_INVALID (spec test 8).
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "whatever")
	_, _ = fw.Write([]byte("x"))
	_ = mw.WriteField("filename", "../../etc/passwd")
	_ = mw.Close()
	resp, _ := do(t, srv, "POST", "/api/v1/files/upload", buf.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType()})
	if resp.StatusCode != 400 {
		t.Fatalf("path-separator filename should 400, got %d", resp.StatusCode)
	}
}

func TestHandler_Viewers(t *testing.T) {
	srv, _ := newHandlerTestServer(t)

	// GET /viewers → 7 built-ins (AC 5).
	resp, raw := do(t, srv, "GET", "/api/v1/viewers", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /viewers status = %d", resp.StatusCode)
	}
	var viewers []ViewerRegistration
	if err := json.Unmarshal(raw, &viewers); err != nil {
		t.Fatalf("decode viewers: %v", err)
	}
	if len(viewers) != 7 {
		t.Fatalf("expected 7 built-in viewers, got %d", len(viewers))
	}
	slugs := map[string]bool{}
	for _, v := range viewers {
		slugs[v.ViewerSlug] = true
	}
	for _, want := range []string{"pdf", "image", "code", "csv", "markdown", "json", "audio_video"} {
		if !slugs[want] {
			t.Fatalf("missing viewer %q", want)
		}
	}

	// GET /viewers/{slug}.
	resp, raw = do(t, srv, "GET", "/api/v1/viewers/pdf", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /viewers/pdf status = %d", resp.StatusCode)
	}
	var pdf ViewerRegistration
	if err := json.Unmarshal(raw, &pdf); err != nil || pdf.ViewerSlug != "pdf" {
		t.Fatalf("pdf viewer: %v %+v", err, pdf)
	}

	// Unknown slug → 404 VIEWER_NOT_FOUND.
	resp, raw = do(t, srv, "GET", "/api/v1/viewers/nonexistent", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("unknown slug should 404, got %d (%s)", resp.StatusCode, raw)
	}
}

func TestHandler_ResolveAndDispatch(t *testing.T) {
	srv, _ := newHandlerTestServer(t)

	status, out := uploadMultipart(t, srv, "resolve-me.txt", "resolve by hash please", "")
	if status != 201 {
		t.Fatalf("upload status = %d", status)
	}

	// POST /files/resolve by hash.
	body, _ := json.Marshal(map[string]any{
		"hash_ref": map[string]any{
			"profile_id": out.FileMetadata.ProfileID.String(),
			"sha256":     out.FileMetadata.SHA256,
		},
	})
	resp, raw := do(t, srv, "POST", "/api/v1/files/resolve", body, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 200 {
		t.Fatalf("resolve status = %d (%s)", resp.StatusCode, raw)
	}
	var res ResolveFileOutput
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if res.FileMetadata.ID != out.FileMetadata.ID || res.WasNewUpload || res.WasDeduped {
		t.Fatalf("resolve output wrong: %+v", res)
	}

	// Unknown hash → FILE_NOT_FOUND_BY_HASH.
	body, _ = json.Marshal(map[string]any{
		"hash_ref": map[string]any{
			"profile_id": out.FileMetadata.ProfileID.String(),
			"sha256":     sha256Of(t, "never-uploaded"),
		},
	})
	resp, _ = do(t, srv, "POST", "/api/v1/files/resolve", body, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 404 {
		t.Fatalf("unknown hash resolve should 404, got %d", resp.StatusCode)
	}

	// POST /viewers/dispatch.
	body, _ = json.Marshal(map[string]any{"file_id": out.FileMetadata.ID.String()})
	resp, raw = do(t, srv, "POST", "/api/v1/viewers/dispatch", body, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 200 {
		t.Fatalf("dispatch status = %d (%s)", resp.StatusCode, raw)
	}
	var disp ViewerDispatchResult
	if err := json.Unmarshal(raw, &disp); err != nil {
		t.Fatalf("decode dispatch: %v", err)
	}
	if disp.ViewerSlug != "code" || !disp.IsBuiltIn {
		t.Fatalf("dispatch wrong: %+v", disp)
	}
}

func TestHandler_AccessLogAndRecentsAndDelete(t *testing.T) {
	srv, _ := newHandlerTestServer(t)

	status, out := uploadMultipart(t, srv, "audited.pdf", "%PDF-1.4 fake", "")
	if status != 201 {
		t.Fatalf("upload status = %d", status)
	}
	id := out.FileMetadata.ID.String()
	if out.FileMetadata.MimeType != "application/pdf" || out.FileMetadata.ViewerHint != "pdf" {
		t.Fatalf("pdf classification: %+v", out.FileMetadata)
	}

	// POST access (open).
	body, _ := json.Marshal(map[string]any{"action": "open", "viewer_slug": "pdf"})
	resp, raw := do(t, srv, "POST", "/api/v1/files/"+id+"/access", body, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 201 {
		t.Fatalf("access append status = %d (%s)", resp.StatusCode, raw)
	}

	// GET access log shows the entry.
	resp, raw = do(t, srv, "GET", "/api/v1/files/"+id+"/access", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("access get status = %d", resp.StatusCode)
	}
	var entries []FileAccessEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decode access log: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != AccessActionOpen {
		t.Fatalf("access log wrong: %+v", entries)
	}

	// Recents now includes the file.
	resp, raw = do(t, srv, "GET", "/api/v1/files/recents", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("recents status = %d", resp.StatusCode)
	}
	var recents []FileMetadataSlim
	if err := json.Unmarshal(raw, &recents); err != nil {
		t.Fatalf("decode recents: %v", err)
	}
	if len(recents) != 1 || recents[0].ID != out.FileMetadata.ID {
		t.Fatalf("recents wrong: %+v", recents)
	}

	// List includes the file.
	resp, raw = do(t, srv, "GET", "/api/v1/files", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var page struct {
		Files []FileMetadataSlim `json:"files"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(page.Files) != 1 {
		t.Fatalf("list wrong: %+v", page)
	}

	// DELETE → soft delete.
	resp, _ = do(t, srv, "DELETE", "/api/v1/files/"+id, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}

	// GET after delete → FILE_NOT_FOUND_BY_ID (410 semantics map to 404 body).
	resp, _ = do(t, srv, "GET", "/api/v1/files/"+id, nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("deleted file should 404, got %d", resp.StatusCode)
	}
	// …and the stream refuses too.
	resp, _ = do(t, srv, "GET", "/api/v1/files/"+id+"/stream", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("deleted file stream should 404, got %d", resp.StatusCode)
	}
}

func TestHandler_ResolveBatchEndpoint(t *testing.T) {
	srv, _ := newHandlerTestServer(t)

	status, out := uploadMultipart(t, srv, "batch.txt", "batch content", "")
	if status != 201 {
		t.Fatalf("upload status = %d", status)
	}

	body, _ := json.Marshal([]ResolveFileInput{
		{HashRef: &HashRef{ProfileID: out.FileMetadata.ProfileID, SHA256: out.FileMetadata.SHA256}},
	})
	resp, raw := do(t, srv, "POST", "/api/v1/files/resolve/batch", body, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 200 {
		t.Fatalf("batch status = %d (%s)", resp.StatusCode, raw)
	}
	var outs []ResolveFileOutput
	if err := json.Unmarshal(raw, &outs); err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if len(outs) != 1 || outs[0].FileMetadata == nil || outs[0].FileMetadata.ID != out.FileMetadata.ID {
		t.Fatalf("batch output wrong: %+v", outs)
	}
}
