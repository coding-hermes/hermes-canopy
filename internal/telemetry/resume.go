package telemetry

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// DefaultResumeIdleGap is the silence that separates two resume attempts by the
// same user: a tree-scoped read (GET/HEAD) only opens a resume window when that
// user's previous tree-scoped read is at least this old. It is the production
// value; the tracker itself uses whatever gap it is constructed with.
const DefaultResumeIdleGap = 5 * time.Minute

// resumePruneBound is the tracker's prune trigger. Once this many keys are
// tracked, a read for a NEW key first drops every entry that is both idle (its
// last tree read is older than the idle gap) and windowless (it carries no open
// resume window) — an open window is evidence of an in-flight resume whose
// observation can still land, so it is never dropped.
//
// It is a trigger, not a hard cap: when every tracked key still carries an open
// window nothing is dropped and the map can exceed the bound.
const resumePruneBound = 256

// treeScopedReadPattern is the chi route pattern of the tree root. chi's
// RoutePattern() returns the FULL pattern including the mount prefix, so the
// tree root and everything mounted beneath it share this prefix
// (proven against this repo's Route + Mount shape).
//
// The prefix is NOT all reads: PATCH/DELETE /trees/{tree_id} and the POST
// routes registered under it (share, presence, presence/leave, topics/inject,
// references/resolve, references/inject, reference-selections,
// multi-reference-replies) match it too. IsTreeScopedRead is pattern-only by
// design; IsResumeReadMethod is what keeps those writes out of the metric.
const treeScopedReadPattern = "/api/v1/trees/{tree_id}"

// resumeMaxSuccessStatus is the inclusive upper bound of the HTTP 2xx range.
const resumeMaxSuccessStatus = 299

// contextCompilePattern is the chi route pattern of the compiled-context
// endpoint (GAP-001) — the point at which the server has handed a user their
// working context back.
const contextCompilePattern = "/api/v1/context/{node_id}"

// ResumeSink receives the resume-time signals. *Metrics satisfies it; a test
// double records calls without touching Prometheus, which is why the
// middleware talks to a sink instead of to *Metrics directly.
type ResumeSink interface {
	// IncResumeStarted records that a resume window opened.
	IncResumeStarted()
	// ObserveResumeDuration records one completed resume window in seconds.
	ObserveResumeDuration(seconds float64)
}

// resumeEntry is one tracked user's resume state.
type resumeEntry struct {
	// lastTreeRead is when this user last made a successful tree-scoped read
	// (GET/HEAD) — it is the clock the idle gap is measured against.
	lastTreeRead time.Time
	// windowOpen reports whether a started window is still awaiting its
	// compiled context. A window yields at most one observation.
	windowOpen bool
	// windowStart is when the open window was opened; the observed duration is
	// the wall-clock distance from here to the context compile.
	windowStart time.Time
}

// ResumeTracker remembers per-user tree-read history so resume windows can be
// opened and completed. It is safe for concurrent use.
//
// The zero value is NOT usable: construct with NewResumeTracker.
type ResumeTracker struct {
	idleGap time.Duration

	mu      sync.Mutex
	entries map[string]*resumeEntry
}

// NewResumeTracker returns a tracker that opens a window when a user's
// tree-scoped reads (GET/HEAD) are at least idleGap apart. idleGap is used
// verbatim; pass DefaultResumeIdleGap for the production gap.
func NewResumeTracker(idleGap time.Duration) *ResumeTracker {
	return &ResumeTracker{
		idleGap: idleGap,
		entries: make(map[string]*resumeEntry),
	}
}

// NoteTreeRead records a successful tree-scoped read (GET/HEAD — the method
// gate lives in ResumeMiddleware, which is its only caller) by key at at, and
// reports whether it OPENED a resume window.
//
// A window opens when the key is new, or when at is at least idleGap after the
// key's previous tree read. Successive reads inside the gap open nothing, so a
// user reading their tree repeatedly is not credited with a resume each time.
// An already-open window whose user idles for another full gap and reads again
// starts a NEW window: the earlier resume never completed, and the started
// counter is what makes that visible.
//
// An empty key is ignored (returns false).
func (t *ResumeTracker) NoteTreeRead(key string, at time.Time) (started bool) {
	if key == "" || t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.entries == nil {
		t.entries = make(map[string]*resumeEntry)
	}

	e, ok := t.entries[key]
	if !ok {
		if len(t.entries) >= resumePruneBound {
			t.pruneLocked(at)
		}
		t.entries[key] = &resumeEntry{
			lastTreeRead: at,
			windowOpen:   true,
			windowStart:  at,
		}
		return true
	}

	started = at.Sub(e.lastTreeRead) >= t.idleGap
	e.lastTreeRead = at
	if started {
		e.windowOpen = true
		e.windowStart = at
	}
	return started
}

// NoteContextCompile records a successful compiled-context read by key at at,
// and reports the completed window's duration in seconds.
//
// It yields at most one observation per window: a compile with no open window
// for that key (never started, or already completed) observes nothing. An
// empty key is ignored.
func (t *ResumeTracker) NoteContextCompile(key string, at time.Time) (seconds float64, observed bool) {
	if key == "" || t == nil {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	e, ok := t.entries[key]
	if !ok || !e.windowOpen {
		return 0, false
	}
	e.windowOpen = false
	return at.Sub(e.windowStart).Seconds(), true
}

// pruneLocked drops idle, windowless entries. The caller must hold t.mu.
// Returns the number of entries dropped.
func (t *ResumeTracker) pruneLocked(now time.Time) int {
	dropped := 0
	for key, e := range t.entries {
		if e.windowOpen {
			continue
		}
		if now.Sub(e.lastTreeRead) > t.idleGap {
			delete(t.entries, key)
			dropped++
		}
	}
	return dropped
}

// IsTreeScopedRead reports whether a chi route pattern is tree-scoped: the tree
// root itself, or anything mounted beneath it.
//
// This is pattern-based, not method-based, and the tree-scoped pattern set is
// NOT all reads: PATCH/DELETE /trees/{tree_id} and the POST routes registered
// under the same prefix are tree-scoped patterns too. A caller that means
// "read" pairs this predicate with IsResumeReadMethod — ResumeMiddleware does,
// so a 2xx write never opens, refreshes or completes a resume window.
func IsTreeScopedRead(pattern string) bool {
	if pattern == treeScopedReadPattern {
		return true
	}
	return strings.HasPrefix(pattern, treeScopedReadPattern+"/")
}

// IsResumeReadMethod reports whether an HTTP method is a READ for resume
// purposes: GET and HEAD, and nothing else (the empty string included).
//
// It exists because IsTreeScopedRead is pattern-based while the tree-scoped
// pattern set contains writes — PATCH/DELETE /trees/{tree_id} plus the POST
// routes beneath it (share, presence, presence/leave, topics/inject,
// references/resolve|inject, reference-selections, multi-reference-replies).
// The metric is documented as a resume READ, so ResumeMiddleware requires both
// predicates before it touches the tracker.
func IsResumeReadMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return true
	default:
		return false
	}
}

// IsContextCompile reports whether a chi route pattern is the compiled-context
// endpoint. Exact equality: no other pattern is a context compile.
func IsContextCompile(pattern string) bool {
	return pattern == contextCompilePattern
}

// ResumeMiddleware returns chi middleware that feeds resume-time metrics.
//
// Semantics (GAP-079) — the product contract:
//
//   - A resume window OPENS on the first successful (HTTP 2xx) tree-scoped read
//     (GET/HEAD) by an authenticated user after at least DefaultResumeIdleGap
//     with no tree-scoped read by that user.
//   - It COMPLETES when that same user's next successful
//     GET /api/v1/context/{node_id} returns — the compiled context, i.e. the
//     point where the server has handed the user back their working context.
//   - resume_duration_seconds observes the wall-clock seconds between those two
//     requests. That is the SERVER-OBSERVABLE part of the "<30s resume" claim:
//     browser render time is not included, and a resume performed entirely from
//     cache is invisible. resume_started_total counts windows opened, so a
//     window that never reaches a context compile shows up as
//     started-but-never-observed.
//
// Only reads reach the tracker. The tree-scoped PATTERN covers writes too —
// PATCH/DELETE /trees/{tree_id} and the POST routes registered under that
// prefix (share, presence, presence/leave, topics/inject,
// references/resolve|inject, reference-selections, multi-reference-replies) all
// resolve to it — so a request is ignored unless IsResumeReadMethod(r.Method)
// holds. A 2xx write therefore opens no window, does not move the idle-gap
// clock, and does not complete an open window.
//
// keyFn identifies the user. It runs AFTER the handler, so it reads the
// request context the auth middleware populated on the way in.
//
// Must be registered after the auth middleware on the same router, and must run
// its post-handler work after next.ServeHTTP because chi resolves
// RoutePattern() during routing. A nil sink, nil tracker, or nil keyFn makes
// this a no-op pass-through (the wrapped handler is returned untouched).
func ResumeMiddleware(sink ResumeSink, keyFn func(*http.Request) string, t *ResumeTracker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if sink == nil || t == nil || keyFn == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Wrap like MetricsMiddleware so the status the handler produced
			// is observable. A handler that never writes a header or a body
			// leaves Status() at 0, which is not treated as 2xx.
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			if ww.Status() < http.StatusOK || ww.Status() > resumeMaxSuccessStatus {
				return
			}

			key := keyFn(r)
			if key == "" {
				return
			}

			// The metric is a resume READ: a 2xx write on a tree-scoped
			// pattern (PATCH/DELETE /trees/{tree_id}, the POST routes
			// under that prefix) must not open a window, refresh the
			// idle clock, or complete one.
			if !IsResumeReadMethod(r.Method) {
				return
			}

			var pattern string
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				pattern = rctx.RoutePattern()
			}

			switch {
			case IsTreeScopedRead(pattern):
				if t.NoteTreeRead(key, time.Now()) {
					sink.IncResumeStarted()
				}
			case IsContextCompile(pattern):
				if seconds, observed := t.NoteContextCompile(key, time.Now()); observed {
					sink.ObserveResumeDuration(seconds)
				}
			}
		})
	}
}
