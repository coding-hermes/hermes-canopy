package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

// TestSSERouteAllowlistMarkedForWriteDeadlineExemption extends
// TestSSERouteAllowlistMatchesRouter for GAP-100: every route on the live
// SSE allowlist — walked from the REAL production router — is actually
// marked by sseWriteDeadlineExemptMiddleware. The marker is the one
// mechanism whose presence lets a stream clear the server-level WriteTimeout
// (internal/sse.FrameWriter); an allowlist entry without the marker would
// still die at the server boundary. The census also fails when an ordinary
// GET route wrongly matches the stream suffixes (it would gain the
// exemption without a conscious decision).
//
// The federation stream cannot mount on the DB-free test router
// (federationSvc == nil), so its marking is pinned at the predicate level
// exactly like its allowlist entry — including the method rule: the GET
// stream is marked, the POST write is not.
func TestSSERouteAllowlistMarkedForWriteDeadlineExemption(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".hermes", "canopy", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}

	deps := &routeDeps{
		jwtSecret: "sse-deadline-test-secret",
		connMgr:   transport.NewConnectionManager(nil),
		cfg:       &config.Config{},
	}
	router := newRouter(deps)

	// Census: the GET routes the suffix predicate recognizes on the real
	// router must equal the known allowlist, both directions.
	found := map[string]bool{}
	err := chi.Walk(router, func(method, pattern string, h http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if method == "" {
			method = http.MethodGet
		}
		if method != http.MethodGet {
			return nil
		}
		normalized := normalizeChiPattern(pattern)
		for _, suffix := range sseStreamSuffixes {
			if strings.HasSuffix(normalized, suffix) {
				found[normalized] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}

	want := map[string]bool{
		"/api/v1/trees/{}/events":            true,
		"/api/v1/plugins/{}/events":          true,
		"/api/v1/cards/{}/events":            true,
		"/api/v1/cards/iteration/{}/events":  true,
		"/api/v1/workspace/channels/{}/feed": true,
		"/api/v1/gateway/runs/{}/events":     true,
		"/api/v1/workspaces/{}/mls/events":   true,
	}
	for pattern := range want {
		if !found[pattern] {
			t.Fatalf("known SSE route %s missing from the real router walk (found: %v)", pattern, found)
		}
	}
	for pattern := range found {
		if !want[pattern] {
			t.Fatalf("unexpected GET route %s matches the stream suffixes; decide whether it is a stream before it silently gains the write-deadline exemption", pattern)
		}
	}

	// Marking probe: replay the router's REAL global middleware chain over a
	// capture endpoint, so the assertion runs the production marking
	// middleware in its production position (not a test double of it).
	probeMarked := func(method, pattern string) bool {
		captured := false
		var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured = sse.WriteDeadlineExempt(r)
			w.WriteHeader(http.StatusOK)
		})
		mws := router.Middlewares()
		for i := len(mws) - 1; i >= 0; i-- {
			h = mws[i](h)
		}
		req := httptest.NewRequest(method, strings.ReplaceAll(pattern, "{}", "probe-"+uuid.NewString()[:8]), nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return captured
	}

	for pattern := range want {
		if !probeMarked(http.MethodGet, pattern) {
			t.Fatalf("SSE route %s is on the allowlist but sseWriteDeadlineExemptMiddleware does not mark it — the server WriteTimeout would still kill the stream (GAP-100)", pattern)
		}
	}

	// Ordinary routes must stay unmarked so the server WriteTimeout keeps
	// bounding them.
	for _, pattern := range []string{
		"/api/v1/trees/{}/nodes",
		"/api/v1/trees/{}/nodes/{}/reference-context",
		"/health",
	} {
		if probeMarked(http.MethodGet, pattern) {
			t.Fatalf("ordinary route %s was marked for the write-deadline exemption — the exemption leaked", pattern)
		}
	}

	// Federation stream: unmounted on this router (no federation service),
	// so pin the marking at the predicate level. Method matters.
	if !probeMarked(http.MethodGet, "/api/v1/federation/events") {
		t.Fatal("GET /api/v1/federation/events is a stream route but was not marked")
	}
	if probeMarked(http.MethodPost, "/api/v1/federation/events") {
		t.Fatal("POST /api/v1/federation/events is an ordinary write but was marked")
	}
}
