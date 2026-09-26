package sse

import (
	"context"
	"net/http"
	"time"
)

// GAP-100: http.Server's WriteTimeout is ONE absolute write deadline set at
// request-read time over the whole response — it is not per-write, and
// Flush() does not extend it — so every long-lived SSE stream was killed
// server-side at the server's 30s boundary (the client saw a clean EOF and
// only survived via reconnect + Last-Event-ID replay).
//
// The exemption is one mechanism in two halves:
//
//   - internal/server mounts sseWriteDeadlineMiddleware, which marks every
//     request the SSE predicate recognizes (the same allowlist
//     TestSSERouteAllowlistMatchesRouter pins against the real router).
//   - a marked stream clears the whole-response deadline after its headers
//     are committed via FrameWriter, and every subsequent frame write runs
//     under a BOUNDED, re-armed deadline: clearing alone would let a stuck
//     peer pin the connection's goroutine forever. A timed-out write fails,
//     the handler returns and the connection is released.
//
// Unmarked requests — every ordinary route — never clear the server's
// whole-response WriteTimeout: a FrameWriter built from an unmarked request
// is fully inert.

// WriteDeadline is the bounded per-write deadline every stream frame runs
// under after the whole-response server WriteTimeout is cleared. It is twice
// HeartbeatInterval — an idle-but-healthy stream always writes a heartbeat
// well inside the bound, while a peer that stops accepting writes is dropped
// on the next frame instead of pinning the goroutine.
const WriteDeadline = 2 * HeartbeatInterval

// writeDeadlineExemptKey marks a request as a long-lived SSE stream whose
// handler owns the write deadline (set by the server's middleware).
type writeDeadlineExemptKey struct{}

// MarkWriteDeadlineExempt flags r as a long-lived SSE stream exempt from the
// server-level whole-response WriteTimeout. Called by the server middleware
// for exactly the requests isSSEStreamRequest recognizes.
func MarkWriteDeadlineExempt(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), writeDeadlineExemptKey{}, true))
}

// WriteDeadlineExempt reports whether r was marked as a stream exempt from
// the server-level WriteTimeout.
func WriteDeadlineExempt(r *http.Request) bool {
	v, _ := r.Context().Value(writeDeadlineExemptKey{}).(bool)
	return v
}

// FrameWriter carries the GAP-100 deadline policy for one SSE stream: clear
// the whole-response server WriteDeadline once headers are committed, then
// arm a bounded deadline before every subsequent frame write.
//
// http.NewResponseController unwraps through chi middleware and hlog writer
// proxies (both implement Unwrap() http.ResponseWriter), so the clear and
// the re-arms reach the real connection even under the full server stack.
//
// A FrameWriter built from an unmarked request, or from a writer that does
// not support deadlines (test recorders), is fully inert: SetWriteDeadline
// reports a protocol error on such writers and the policy degrades to a
// no-op instead of poisoning the stream.
type FrameWriter struct {
	rc       *http.ResponseController
	deadline time.Duration
	armed    bool
}

// NewFrameWriter builds the deadline policy for the stream request r. For an
// unmarked (ordinary) request the policy is inert — the server's
// whole-response WriteTimeout keeps covering the response.
func NewFrameWriter(w http.ResponseWriter, r *http.Request) *FrameWriter {
	return NewFrameWriterWithDeadline(w, r, WriteDeadline)
}

// NewFrameWriterWithDeadline builds the policy with an explicit bounded
// per-write deadline (tests inject a short one). A non-positive deadline
// falls back to the production bound: a non-positive bound would arm an
// already-expired deadline on every frame and silently close streams.
func NewFrameWriterWithDeadline(w http.ResponseWriter, r *http.Request, deadline time.Duration) *FrameWriter {
	if !WriteDeadlineExempt(r) {
		return &FrameWriter{}
	}
	if deadline <= 0 {
		deadline = WriteDeadline
	}
	return &FrameWriter{
		rc:       http.NewResponseController(w),
		deadline: deadline,
		armed:    true,
	}
}

// ClearWriteDeadline lifts the whole-response server write deadline after
// the stream's headers are committed. Call it once, right after the initial
// WriteHeader + Flush.
func (fw *FrameWriter) ClearWriteDeadline() {
	if fw == nil || !fw.armed {
		return
	}
	// An error here means deadlines are unsupported after all (test
	// recorders) — degrade to inert rather than warn per frame.
	if err := fw.rc.SetWriteDeadline(time.Time{}); err != nil {
		fw.armed = false
	}
}

// BeforeFrame arms the bounded write deadline for the frame write that
// follows. Call it immediately before every write on the stream after the
// headers are committed (snapshot, replay, live frames, heartbeats).
func (fw *FrameWriter) BeforeFrame() {
	if fw == nil || !fw.armed {
		return
	}
	if err := fw.rc.SetWriteDeadline(time.Now().Add(fw.deadline)); err != nil {
		fw.armed = false
	}
}
