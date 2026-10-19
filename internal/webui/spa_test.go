package webui

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// indexMarker is what the tests look for to prove "the SPA shell was served"
// rather than an asset or a JSON error.
const indexMarker = `id="root"`

// testDist is a miniature frontend/dist: the shell, one hashed bundle under
// assets/, and a nested directory whose listing must never be exposed.
func testDist() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte(`<!doctype html><html><body><div id="root"></div></body></html>`)},
		"favicon.svg":       {Data: []byte(`<svg></svg>`)},
		"assets/app-abc.js": {Data: []byte(`console.log("canopy")`)},
		"nested/inner.txt":  {Data: []byte("inner")},
	}
}

// recordingFallback mimics the API's canonical 404 envelope and records that
// the SPA handler delegated instead of answering with HTML.
type recordingFallback struct {
	called bool
}

func (f *recordingFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.called = true
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":"ROUTE_NOT_FOUND","message":"route not found"}}`))
}

func newTestServer(dist fs.FS) (*recordingFallback, http.Handler) {
	fallback := &recordingFallback{}
	return fallback, NewHandler(dist, fallback)
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestServesStaticFile proves a real file in dist is served with its bytes,
// its content type and the immutable cache policy used for hashed bundles.
func TestServesStaticFile(t *testing.T) {
	_, h := newTestServer(testDist())

	rec := get(t, h, "/assets/app-abc.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), `console.log("canopy")`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type = %q, want a javascript type", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != assetCacheControl {
		t.Fatalf("cache-control = %q, want %q", cc, assetCacheControl)
	}
}

// TestRootServesIndex proves GET / returns the shell (today this is a 404).
func TestRootServesIndex(t *testing.T) {
	_, h := newTestServer(testDist())

	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), indexMarker) {
		t.Fatalf("body = %q, want the index shell", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != indexCacheControl {
		t.Fatalf("cache-control = %q, want %q", cc, indexCacheControl)
	}
}

// TestSPAFallbackServesIndex proves an unknown application route (a client-side
// deep link) returns the shell with 200 instead of a 404.
func TestSPAFallbackServesIndex(t *testing.T) {
	fallback, h := newTestServer(testDist())

	for _, target := range []string{"/trees", "/tree/8f14e45f-ceea-467a", "/trees/123/topic/search?q=x"} {
		rec := get(t, h, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", target, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), indexMarker) {
			t.Fatalf("%s: body = %q, want the index shell", target, rec.Body.String())
		}
	}
	if fallback.called {
		t.Fatal("fallback was called for an application route; the SPA must own it")
	}
}

// TestUnknownAPIPathReturnsJSON404 proves an unknown /api/* path never gets the
// HTML shell: it keeps the API's JSON error contract.
func TestUnknownAPIPathReturnsJSON404(t *testing.T) {
	fallback, h := newTestServer(testDist())

	rec := get(t, h, "/api/v9/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !fallback.called {
		t.Fatal("fallback was not called for an unknown API path")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	if strings.Contains(rec.Body.String(), indexMarker) {
		t.Fatalf("body leaked the HTML shell: %q", rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body.Error.Code != "ROUTE_NOT_FOUND" {
		t.Fatalf("error.code = %q, want ROUTE_NOT_FOUND", body.Error.Code)
	}
}

// TestReservedPrefixesAreNeverShadowed pins every prefix the API owns: each one
// must delegate to the fallback and none may return the shell.
func TestReservedPrefixesAreNeverShadowed(t *testing.T) {
	targets := []string{
		"/api", "/api/", "/api/v1/trees", "/api/unknown",
		"/health", "/health/relay", "/healthz",
		"/version", "/metrics",
		"/ws", "/ws/socket",
		"/mcp", "/mcp/rpc",
	}
	for _, target := range targets {
		if !IsReserved(target) {
			t.Fatalf("IsReserved(%q) = false, want true", target)
		}
		fallback, h := newTestServer(testDist())
		rec := get(t, h, target)
		if !fallback.called {
			t.Fatalf("%s: reserved path did not delegate to the API fallback", target)
		}
		if strings.Contains(rec.Body.String(), indexMarker) {
			t.Fatalf("%s: reserved path was shadowed by the SPA shell", target)
		}
	}

	// Segment-boundary check: a path that merely starts with the same letters
	// is an application route, not an API one.
	for _, target := range []string{"/apixyz", "/healthcare", "/versions"} {
		if IsReserved(target) {
			t.Fatalf("IsReserved(%q) = true, want false", target)
		}
	}
}

// TestBogusPathDoesNotListDirectories is the leak test: a directory-shaped
// request, a traversal attempt and a missing file must all resolve to the shell
// (or a JSON error) — never to a directory listing.
func TestBogusPathDoesNotListDirectories(t *testing.T) {
	for _, target := range []string{
		"/assets/",
		"/nested/",
		"/nested/inner.txt/", // trailing slash on a file
		"/../../etc/passwd",
		"/assets/../../../../etc/hosts",
		"/does-not-exist.js",
	} {
		_, h := newTestServer(testDist())
		rec := get(t, h, target)
		body := rec.Body.String()
		for _, leak := range []string{"inner.txt", "<pre>", "Directory listing", "Index of"} {
			if strings.Contains(body, leak) {
				t.Fatalf("%s: body leaked %q: %q", target, leak, body)
			}
		}
		if rec.Code == http.StatusOK && !strings.Contains(body, indexMarker) {
			t.Fatalf("%s: 200 body is not the index shell: %q", target, body)
		}
	}
}

// TestMethodNotAllowed covers mutating verbs on non-API paths: the SPA is
// read-only, and the refusal keeps the JSON envelope.
func TestMethodNotAllowed(t *testing.T) {
	fallback, h := newTestServer(testDist())

	req := httptest.NewRequest(http.MethodPost, "/trees", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if fallback.called {
		t.Fatal("a non-API mutation must not reach the API fallback")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("allow = %q, want %q", allow, "GET, HEAD")
	}
}

// TestManifestContentType covers the asset type Go's mime table lacks: a
// .webmanifest must not be sniffed into text/plain.
func TestManifestContentType(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":           {Data: []byte(`<div id="root"></div>`)},
		"manifest.webmanifest": {Data: []byte(`{"name":"Hermes Canopy"}`)},
	}
	_, h := newTestServer(dist)

	rec := get(t, h, "/manifest.webmanifest")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("content-type = %q, want application/manifest+json", ct)
	}
}

// TestHeadHasNoBody keeps HEAD honest: headers, no body.
func TestHeadHasNoBody(t *testing.T) {
	_, h := newTestServer(testDist())

	req := httptest.NewRequest(http.MethodHead, "/trees", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rec.Body.String())
	}
}

// TestNotEmbeddedDegradesToAPIHandler proves the non-tagged stub: with no
// embedded dist, NotFound is the API 404 handler and nothing serves HTML.
func TestNotEmbeddedDegradesToAPIHandler(t *testing.T) {
	if Embedded() {
		t.Skip("built with -tags embedui: the embedded dist is present")
	}
	fallback := &recordingFallback{}
	h := NotFound(fallback)

	rec := get(t, h, "/trees")
	if !fallback.called {
		t.Fatal("NotFound did not delegate to the API fallback without an embedded UI")
	}
	if strings.Contains(rec.Body.String(), indexMarker) {
		t.Fatal("HTML shell served without -tags embedui")
	}
}

// TestMissingIndexDegradesToAPIHandler keeps a broken staging step from
// producing a 200 with an empty body: no index.html means API-only.
func TestMissingIndexDegradesToAPIHandler(t *testing.T) {
	dist := fstest.MapFS{"assets/app-abc.js": {Data: []byte("x")}}
	fallback := &recordingFallback{}
	h := NewHandler(dist, fallback)

	rec := get(t, h, "/")
	if !fallback.called {
		t.Fatal("handler without index.html did not delegate to the API fallback")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
