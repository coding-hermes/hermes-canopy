package server

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

// TestIsSSEStreamRequest pins the predicate: every production SSE route is
// exempt; every look-alike ordinary request keeps the 60s cap.
func TestIsSSEStreamRequest(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		// The six production stream routes (all GET).
		{http.MethodGet, "/api/v1/trees/t-1/events", true},
		{http.MethodGet, "/api/v1/plugins/t-1/events", true},
		{http.MethodGet, "/api/v1/federation/events", true},
		{http.MethodGet, "/api/v1/cards/c-1/events", true},
		{http.MethodGet, "/api/v1/workspace/channels/ch-1/feed", true},
		{http.MethodGet, "/api/v1/gateway/runs/r-1/events", true},
		{http.MethodGet, "/api/v1/workspaces/w-1/mls/events", true},
		// Ordinary requests that share a suffix or prefix must stay capped:
		// the federation write is a POST on an "/events" path, and the
		// replay is a bounded GET under the same prefix.
		{http.MethodPost, "/api/v1/federation/events", false},
		{http.MethodGet, "/api/v1/federation/events/replay", false},
		{http.MethodGet, "/api/v1/trees/t-1/nodes", false},
		{http.MethodGet, "/api/v1/trees/t-1", false},
		{http.MethodGet, "/health", false},
		{http.MethodPost, "/api/v1/trees/t-1/nodes/n-1/reply", false},
		{http.MethodGet, "/api/v1/gateway/models", false},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if got := isSSEStreamRequest(r); got != tc.want {
				t.Fatalf("isSSEStreamRequest(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

// TestRequestTimeoutExemptSSE_StreamSurvives is the DF-HERMES-CANOPY-33
// regression test: with a 150ms request timeout (standing in for the global
// 60s cap), an SSE-marked handler streaming for ~450ms delivers every frame
// past the timeout boundary and its context is never deadline-cancelled,
// while an ordinary handler on the same router is still cut off at the
// boundary (504, context.DeadlineExceeded observed inside the handler).
func TestRequestTimeoutExemptSSE_StreamSurvives(t *testing.T) {
	const timeout = 150 * time.Millisecond

	r := chi.NewRouter()
	r.Use(requestTimeoutExemptSSE(timeout))

	streamCtxErr := make(chan error, 1)
	r.Get("/stream/events", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// 5 frames at 100ms intervals: total ~500ms, well past the 150ms
		// timeout. With the old global middleware the request context would
		// be cancelled at 150ms and the loop would end early.
		for i := 0; i < 5; i++ {
			select {
			case <-req.Context().Done():
				streamCtxErr <- req.Context().Err()
				return
			case <-time.After(100 * time.Millisecond):
			}
			fmt.Fprintf(w, "data: frame-%d\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
		}
		streamCtxErr <- nil
	})

	ordinaryCtxErr := make(chan error, 1)
	r.Get("/ordinary", func(w http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
		ordinaryCtxErr <- req.Context().Err()
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	// --- SSE route: stream must survive past the timeout boundary ---
	resp, err := http.Get(srv.URL + "/stream/events")
	if err != nil {
		t.Fatalf("stream GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}
	frames := 0
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: frame-") {
			frames++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("stream read error: %v", err)
	}
	if frames != 5 {
		t.Fatalf("frames = %d, want 5 — the stream died at the timeout boundary", frames)
	}
	if ctxErr := <-streamCtxErr; ctxErr != nil {
		t.Fatalf("stream handler context cancelled: %v", ctxErr)
	}

	// --- Ordinary route: the timeout must still bite ---
	resp2, err := http.Get(srv.URL + "/ordinary")
	if err != nil {
		t.Fatalf("ordinary GET failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusGatewayTimeout {
		dump, _ := httputil.DumpResponse(resp2, false)
		t.Fatalf("ordinary status = %d, want 504 (%s)", resp2.StatusCode, dump)
	}
	if ctxErr := <-ordinaryCtxErr; ctxErr == nil {
		t.Fatal("ordinary handler context was never cancelled — the timeout exemption leaked")
	}
}

// TestSSERouteAllowlistMatchesRouter walks the REAL production router (the
// same newRouter seam the server uses, DB-free like the other parity tests)
// and asserts the set of GET routes the predicate would exempt equals the
// known SSE allowlist. A future ordinary GET route ending in "/events" or
// "/feed" — or a renamed SSE route — fails here instead of silently gaining
// or losing the timeout exemption.
//
// The federation stream (GET /api/v1/federation/events) mounts only when
// federationSvc != nil, so it cannot appear in this walk (the test router
// has no federation service); its path shape is pinned by the predicate
// table test instead.
func TestSSERouteAllowlistMatchesRouter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".hermes", "canopy", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}

	deps := &routeDeps{
		jwtSecret: "sse-allowlist-test-secret",
		connMgr:   transport.NewConnectionManager(nil),
		cfg:       &config.Config{},
	}
	router := newRouter(deps)

	got := map[string]bool{}
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
				got[normalized] = true
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
		"/api/v1/workspace/channels/{}/feed": true,
		"/api/v1/gateway/runs/{}/events":     true,
		"/api/v1/workspaces/{}/mls/events":   true,
	}
	if len(got) != len(want) {
		t.Fatalf("exempt GET route set mismatch: got %v, want %v", got, want)
	}
	for pattern := range want {
		if !got[pattern] {
			t.Fatalf("known SSE route %s not matched by the predicate on the real router (set: %v)", pattern, got)
		}
	}
}
