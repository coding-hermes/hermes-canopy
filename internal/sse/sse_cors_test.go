// Package sse_test — CORS regression tests for the SSE stream handler
// (RVW2-CANOPY-103): the handler must NOT hardcode
// Access-Control-Allow-Origin; CORS is owned by the server's corsMiddleware
// (config-driven, fail-closed when CORSOrigin is empty).
package sse_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

// newCORSSSETestServer wires the SSE handler behind a corsMiddleware-like
// middleware matching internal/server/server.go's wiring, with the given
// CORSOrigin configuration value.
func newCORSSSETestServer(t *testing.T, origin string) *httptest.Server {
	t.Helper()
	h := sse.NewHubWithConfig(sse.HubConfig{
		PruneInterval: -1,
		DrainTimeout:  200 * time.Millisecond,
	})
	r := chi.NewRouter()
	handler := sse.NewHandlerWithConfig(h, nil, 30*time.Millisecond, nil)
	// Mirror internal/server/server.go: corsMiddleware runs on the router the
	// SSE route registers on. An empty origin emits no header (fail-closed).
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/trees/{tree_id}/events", handler.HandleTreeEvents)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		_ = h.Shutdown(context.Background())
		srv.Close()
	})
	return srv
}

// assertSSECORS opens a REAL SSE stream (valid tree id) and asserts the
// exact Access-Control-Allow-Origin header on the committed stream response.
// The handler sets its headers just before WriteHeader(200), so this is the
// surface the wildcard used to overwrite.
func assertSSECORS(t *testing.T, srv *httptest.Server, want string) {
	t.Helper()
	tid := uuid.New()
	req, err := http.NewRequest("GET", srv.URL+"/trees/"+tid.String()+"/events?include_heartbeat=false", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != want {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, want)
	}
	// Server may write frames/heartbeats; reading is optional — closing the
	// request context tears the stream down.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				return
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
}

// TestSSEHandler_CORS_FailClosed: when CORSOrigin is empty the stream
// response must carry NO Access-Control-Allow-Origin header. This fails
// against the pre-fix handler, which hardcoded "*".
func TestSSEHandler_CORS_FailClosed(t *testing.T) {
	srv := newCORSSSETestServer(t, "")
	assertSSECORS(t, srv, "")
}

// TestSSEHandler_CORS_Configured: when CORSOrigin is set the stream response
// must carry exactly the configured value, untouched by the handler.
func TestSSEHandler_CORS_Configured(t *testing.T) {
	srv := newCORSSSETestServer(t, "http://localhost:5173")
	assertSSECORS(t, srv, "http://localhost:5173")
}
