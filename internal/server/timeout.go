package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

// DF-HERMES-CANOPY-33: the global r.Use(middleware.Timeout(60s)) wrapped EVERY
// request context in context.WithTimeout(60s). Both SSE event loops
// (internal/sse/sse_handler.go, internal/handler/card_events_handler.go, and
// the federation/workspace/gateway streams) return on ctx.Done, so every
// long-lived stream ended cleanly at ~60s. The shipped client surfaces a
// clean close as onClose (NOT an error), so its bounded retry never fired
// and the panel silently stopped updating — the same user-visible defect
// class as DF-HERMES-CANOPY-31 (transport WriteTimeout), one boundary later.
//
// The fix keeps the 60s cap for ordinary requests and skips the context
// wrap for long-lived streams.

// sseStreamSuffixes are the path suffixes of every long-lived SSE route.
// All seven production stream routes end in "/events" or "/feed":
//
//	GET /api/v1/trees/{tree_id}/events                 (SPEC-API-01)
//	GET /api/v1/plugins/{tree_id}/events               (plugin tree events)
//	GET /api/v1/federation/events                      (federation stream;
//		mounted only when federationSvc != nil)
//	GET /api/v1/cards/{card_id}/events                 (SPEC-PL-03 §9)
//	GET /api/v1/workspace/channels/{channel_id}/feed   (channel stream)
//	GET /api/v1/gateway/runs/{run_id}/events           (gateway run stream)
//	GET /api/v1/workspaces/{workspace_id}/mls/events   (SPEC-FTR-03 MLS stream)
//
// Pinned by TestSSERouteAllowlistMatchesRouter, which walks the real router:
// adding an ordinary GET route with one of these suffixes fails that test,
// forcing a conscious decision instead of a silent timeout exemption.
var sseStreamSuffixes = []string{"/events", "/feed"}

// isSSEStreamRequest reports whether r targets a long-lived SSE stream.
// Method matters: POST /api/v1/federation/events is an ordinary write that
// shares the "/events" suffix and must keep the 60s cap.
func isSSEStreamRequest(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	path := r.URL.Path
	for _, suffix := range sseStreamSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

// requestTimeoutExemptSSE applies chi's middleware.Timeout(d) to ordinary
// requests and skips the context wrap entirely for SSE stream requests.
// A skipped stream keeps the parent (server) context, so shutdown and
// client-disconnect cancellation still propagate; only the artificial 60s
// deadline is removed. Per-frame write deadlines on the stream itself
// (DF-HERMES-CANOPY-31) bound each write independently.
func requestTimeoutExemptSSE(d time.Duration) func(http.Handler) http.Handler {
	timeout := middleware.Timeout(d)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSSEStreamRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			timeout(next).ServeHTTP(w, r)
		})
	}
}

// sseWriteDeadlineExemptMiddleware marks an SSE stream request so its
// handler can clear the http.Server-level WriteTimeout (GAP-100).
//
// http.Server's WriteTimeout is a single absolute write deadline set at
// request-read time over the WHOLE response — it is not per-write and
// Flush() does not extend it — so every stream route was killed server-side
// at the server's 30s boundary and survived only via reconnect +
// Last-Event-ID replay. The marker (internal/sse) lets the handler lift that
// deadline via sse.NewFrameWriter once its headers are committed; each frame
// write then runs under a bounded, re-armed deadline so a stuck peer still
// drops the connection instead of pinning a goroutine. Ordinary requests
// never receive the marker and keep the server's whole-response
// WriteTimeout untouched.
//
// Mounted globally right beside requestTimeoutExemptSSE so the exemption
// covers every route on the predicate's allowlist — the same set
// TestSSERouteAllowlistMatchesRouter pins against the real router — with one
// mechanism and no per-handler copies.
func sseWriteDeadlineExemptMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSSEStreamRequest(r) {
				r = sse.MarkWriteDeadlineExempt(r)
			}
			next.ServeHTTP(w, r)
		})
	}
}
