// Package webui serves the built Hermes Canopy single-page application from
// the canopyd binary itself, so one process serves both the API and the UI.
//
// The frontend build (frontend/dist) is compiled in only when the binary is
// built with `-tags embedui` (see spa_embed.go, staged by `make build-ui`).
// Without that tag distFS is nil: Embedded() reports false and NotFound
// returns the API router's own 404 handler untouched, so a plain
// `go build` / `make build` stays API-only, unchanged and fast.
package webui

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// distFS holds the embedded frontend/dist tree. spa_embed.go assigns it from
// //go:embed under the embedui build tag; it stays nil in a plain build, which
// is what makes the embed opt-in.
var distFS fs.FS

// reservedPrefixes are the path prefixes owned by the API server. The SPA must
// never shadow them: an unknown path under one of these is an API 404 (JSON),
// never the index.html history fallback.
var reservedPrefixes = []string{"/api", "/health", "/healthz", "/version", "/metrics", "/ws", "/mcp"}

// indexName is the SPA shell served for every application route.
const indexName = "index.html"

// indexCacheControl keeps the shell out of caches: it names the hashed asset
// files, so a stale copy pins the client to a superseded bundle.
const indexCacheControl = "no-cache"

// assetCacheControl is for Vite's content-hashed bundles: the file name changes
// whenever the bytes do, so it can be cached forever.
const assetCacheControl = "public, max-age=31536000, immutable"

// IsReserved reports whether urlPath belongs to the API surface. Matching is
// segment-boundary aware: "/api" and "/api/v1/trees" are reserved, "/apixyz"
// is not.
func IsReserved(urlPath string) bool {
	for _, prefix := range reservedPrefixes {
		if urlPath == prefix || strings.HasPrefix(urlPath, prefix+"/") {
			return true
		}
	}
	return false
}

// Embedded reports whether this binary carries the built UI.
func Embedded() bool { return distFS != nil }

// NotFound returns the router's catch-all handler: reserved (API) paths get
// apiFallback's JSON 404, every other GET/HEAD path serves the embedded SPA
// with index.html as the history fallback. When no UI is embedded it returns
// apiFallback unchanged, so the production router behaves exactly as before.
func NotFound(apiFallback http.Handler) http.Handler {
	return NewHandler(distFS, apiFallback)
}

// NewHandler builds the SPA handler from an explicit filesystem. prod passes
// the embedded dist; tests pass a small in-memory tree and a fake fallback.
// A nil dist (or one without a readable index.html) degrades to apiFallback.
func NewHandler(dist fs.FS, apiFallback http.Handler) http.Handler {
	if dist == nil {
		return apiFallback
	}
	index, err := fs.ReadFile(dist, indexName)
	if err != nil {
		return apiFallback
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsReserved(r.URL.Path) {
			// Never shadow the API, and keep its JSON error contract for
			// unknown API paths instead of returning an HTML shell.
			apiFallback.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		if name, ok := assetName(r.URL.Path); ok {
			if info, err := fs.Stat(dist, name); err == nil && info.Mode().IsRegular() {
				serveFile(w, r, dist, name, info)
				return
			}
		}
		// SPA history fallback: an unknown application route is the shell.
		writeIndex(w, r, index)
	})
}

// assetName maps a request path onto a valid fs.FS path. Anything that cannot
// name a file inside dist (empty, traversal, invalid) is reported as not found
// so the caller falls through to the SPA shell — never to a filesystem walk.
// A trailing slash marks a directory-shaped request: it is never a file, so it
// cannot serve one file's bytes at a directory URL.
func assetName(urlPath string) (string, bool) {
	if strings.HasSuffix(urlPath, "/") {
		return "", false
	}
	// path.Clean collapses "." and ".." segments; the leading "/" keeps the
	// result rooted so "../../etc/passwd" becomes "/etc/passwd" (still inside
	// dist, therefore not found) rather than escaping the FS.
	clean := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if clean == "" || !fs.ValidPath(clean) {
		return "", false
	}
	return clean, true
}

// extraContentTypes covers the SPA asset types Go's mime package does not know
// from its built-in table (it only consults /etc/mime.types, which is absent on
// minimal images): without these, a .webmanifest or a .woff2 would be sniffed
// and served as text/plain.
var extraContentTypes = map[string]string{
	".webmanifest": "application/manifest+json",
	".map":         "application/json",
	".woff2":       "font/woff2",
	".woff":        "font/woff",
	".ttf":         "font/ttf",
}

// contentType resolves an asset's type: the explicit table first, then Go's
// mime registry. An empty result lets http.ServeContent sniff the bytes.
func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if ct, ok := extraContentTypes[ext]; ok {
		return ct
	}
	return mime.TypeByExtension(ext)
}

// serveFile writes one static asset. Directories are never listed: the caller
// only reaches here for regular files (fs.Stat above rejects everything else),
// and fs.ReadDir is never used to enumerate a directory.
func serveFile(w http.ResponseWriter, r *http.Request, dist fs.FS, name string, info fs.FileInfo) {
	file, err := dist.Open(name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "ROUTE_NOT_FOUND", "route not found")
		return
	}
	defer func() { _ = file.Close() }()

	// Only regular files ever reach here, so nothing can be enumerated.
	if ct := contentType(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", assetCacheControl)
	} else {
		w.Header().Set("Cache-Control", indexCacheControl)
	}

	if seeker, ok := file.(io.ReadSeeker); ok {
		http.ServeContent(w, r, info.Name(), info.ModTime(), seeker)
		return
	}
	body, err := io.ReadAll(file)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(body))
}

// writeIndex writes the SPA shell with a 200 so client-side routes deep-link.
func writeIndex(w http.ResponseWriter, r *http.Request, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", indexCacheControl)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(index)
}

// writeJSONError matches the API's error envelope ({"error":{"code","message"}})
// so a UI-generated 4xx/405 is indistinguishable in shape from an API one.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
}
