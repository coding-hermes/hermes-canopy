package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// *Metrics must satisfy the sink the middleware talks to.
var _ ResumeSink = (*Metrics)(nil)

// fakeSink records resume signals without touching Prometheus, so the
// middleware can be asserted on directly.
type fakeSink struct {
	mu        sync.Mutex
	started   int
	durations []float64
}

func (f *fakeSink) IncResumeStarted() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
}

func (f *fakeSink) ObserveResumeDuration(seconds float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.durations = append(f.durations, seconds)
}

func (f *fakeSink) snapshot() (int, []float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]float64, len(f.durations))
	copy(out, f.durations)
	return f.started, out
}

// resumeTestTrackedKeys reports how many keys the tracker holds. Test-only
// white-box view; no production API is added for it.
func resumeTestTrackedKeys(t *ResumeTracker) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// ---------------------------------------------------------------------------
// Predicates
// ---------------------------------------------------------------------------

func TestResumePredicates(t *testing.T) {
	treeReads := []string{
		"/api/v1/trees/{tree_id}",
		"/api/v1/trees/{tree_id}/events",
		"/api/v1/trees/{tree_id}/nodes/{node_id}",
		"/api/v1/trees/{tree_id}/export",
	}
	notTreeReads := []string{
		"",
		"/",
		"/api/v1/trees",
		"/api/v1/trees/import",
		"/api/v1/treesx/{tree_id}",
		"/api/v1/nodes/{node_id}",
		"/api/v1/context/{node_id}",
		"/api/v1/agents",
	}
	for _, p := range treeReads {
		if !IsTreeScopedRead(p) {
			t.Errorf("IsTreeScopedRead(%q) = false, want true", p)
		}
	}
	for _, p := range notTreeReads {
		if IsTreeScopedRead(p) {
			t.Errorf("IsTreeScopedRead(%q) = true, want false", p)
		}
	}

	if !IsContextCompile("/api/v1/context/{node_id}") {
		t.Error("IsContextCompile(/api/v1/context/{node_id}) = false, want true")
	}
	for _, p := range []string{
		"",
		"/api/v1/context",
		"/api/v1/context/{node_id}/x",
		"/api/v1/nodes/{node_id}/reference-context",
		"/api/v1/trees/{tree_id}",
	} {
		if IsContextCompile(p) {
			t.Errorf("IsContextCompile(%q) = true, want false", p)
		}
	}
}

// TestIsResumeReadMethodMethodsTable pins the method gate itself. Only GET and
// HEAD are resume reads, and every write the pattern predicate accepts would
// otherwise reach the tracker.
func TestIsResumeReadMethodMethodsTable(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if !IsResumeReadMethod(m) {
			t.Errorf("IsResumeReadMethod(%q) = false, want true", m)
		}
	}
	for _, m := range []string{
		http.MethodPost,
		http.MethodPatch,
		http.MethodPut,
		http.MethodDelete,
		http.MethodOptions,
		http.MethodConnect,
		http.MethodTrace,
		"",
	} {
		if IsResumeReadMethod(m) {
			t.Errorf("IsResumeReadMethod(%q) = true, want false", m)
		}
	}

	// The patterns the gate exists for: each is tree-scoped BY PATTERN, so the
	// pattern predicate alone would let the write through. They are the routes
	// server.go registers as POST/PATCH/DELETE under /api/v1/trees/{tree_id}.
	for _, p := range []string{
		"/api/v1/trees/{tree_id}",
		"/api/v1/trees/{tree_id}/share",
		"/api/v1/trees/{tree_id}/presence",
		"/api/v1/trees/{tree_id}/presence/leave",
		"/api/v1/trees/{tree_id}/topics/inject",
		"/api/v1/trees/{tree_id}/references/resolve",
		"/api/v1/trees/{tree_id}/references/inject",
		"/api/v1/trees/{tree_id}/reference-selections",
		"/api/v1/trees/{tree_id}/multi-reference-replies",
	} {
		if !IsTreeScopedRead(p) {
			t.Errorf("IsTreeScopedRead(%q) = false, want true: the pattern predicate is deliberately method-blind", p)
		}
	}
}

// ---------------------------------------------------------------------------
// Mini router: the real mounting shape, driven with real requests
// ---------------------------------------------------------------------------

// resumeTestUserKey is the context key the test auth stub populates, standing
// in for handler.AuthMiddleware's user id.
type resumeTestUserKey struct{}

func resumeTestOK(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
func resumeTestBad(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusServiceUnavailable)
}

// newResumeTestRouter builds a router mounted the way server.go mounts it:
// r.Route("/api/v1", ...) with the auth stub and the resume middleware
// registered via r.Use in that order, specific /trees/{tree_id}/... routes
// registered BEFORE the /trees mount, and /context/{node_id} at the top level.
func newResumeTestRouter(sink ResumeSink, t *ResumeTracker, user string) *chi.Mux {
	keyFn := func(r *http.Request) string {
		v, _ := r.Context().Value(resumeTestUserKey{}).(string)
		return v
	}
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		// Auth stub: mirrors authMW putting the user id in the request
		// context, and must run BEFORE the resume middleware.
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := context.WithValue(req.Context(), resumeTestUserKey{}, user)
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		r.Use(ResumeMiddleware(sink, keyFn, t))

		// Registered before the mount, like server.go's tree-scoped reads.
		r.Get("/trees/{tree_id}/events", resumeTestOK)
		r.Get("/trees/{tree_id}/broken", resumeTestBad)

		trees := chi.NewRouter()
		trees.Get("/{tree_id}", resumeTestOK)
		// The tree mount is NOT all reads: server.go's treeHandler.Routes()
		// registers these writes on the SAME mount, so they resolve to the same
		// /api/v1/trees/{tree_id}/... prefix.
		trees.Head("/{tree_id}", resumeTestOK)
		trees.Patch("/{tree_id}", resumeTestOK)
		trees.Delete("/{tree_id}", resumeTestOK)
		trees.Post("/{tree_id}/share", resumeTestOK)
		r.Mount("/trees", trees)

		r.Get("/context/{node_id}", resumeTestOK)
		// Non-GET on the compile path: server.go registers only the GET, so this
		// mirrors the mount shape while giving the method gate a non-read to
		// reject at the compile branch.
		r.Post("/context/{node_id}", resumeTestOK)
		// Non-tree-scoped 2xx control.
		r.Get("/nodes/{node_id}", resumeTestOK)
	})
	return r
}

func resumeTestGet(t *testing.T, url string, wantStatus int) {
	t.Helper()
	resumeTestDo(t, http.MethodGet, url, wantStatus)
}

// resumeTestDo drives a real request with an explicit method. HEAD needs the
// explicit form: http.Get cannot send it, and a HEAD response carries no body.
func resumeTestDo(t *testing.T, method, url string, wantStatus int) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("%s %s: building request: %v", method, url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: status %d, want %d", method, url, resp.StatusCode, wantStatus)
	}
}

// TestResumeMiddlewareMiniRouter drives REAL requests through a router mounted
// the way server.go mounts, and asserts the window lifecycle off the real
// RoutePattern() values.
func TestResumeMiddlewareMiniRouter(t *testing.T) {
	sink := &fakeSink{}
	tracker := NewResumeTracker(DefaultResumeIdleGap)
	srv := httptest.NewServer(newResumeTestRouter(sink, tracker, "user-a"))
	defer srv.Close()

	// (AC6a) The first tree-scoped read opens a window and increments the
	// started counter — on the mounted /api/v1/trees/{tree_id} pattern.
	resumeTestGet(t, srv.URL+"/api/v1/trees/t1", http.StatusOK)
	started, durations := sink.snapshot()
	if started != 1 || len(durations) != 0 {
		t.Fatalf("after first tree read: started=%d durations=%v, want started=1 durations=[]", started, durations)
	}

	// A sibling tree-scoped pattern fires too: /api/v1/trees/{tree_id}/events.
	resumeTestGet(t, srv.URL+"/api/v1/trees/t1/events", http.StatusOK)
	started, durations = sink.snapshot()
	if started != 1 || len(durations) != 0 {
		t.Fatalf("second read inside the idle gap must not open a window: started=%d durations=%v", started, durations)
	}

	// The compiled context completes it — exactly one observation, on the real
	// /api/v1/context/{node_id} pattern.
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
	started, durations = sink.snapshot()
	if started != 1 || len(durations) != 1 {
		t.Fatalf("after context compile: started=%d durations=%v, want started=1 durations=[1 value]", started, durations)
	}

	// (AC6c) A second compile with no open window observes nothing.
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
	_, durations = sink.snapshot()
	if len(durations) != 1 {
		t.Fatalf("compile with no open window must observe nothing: durations=%v", durations)
	}

	// A non-tree-scoped 2xx must not touch the tracker at all.
	resumeTestGet(t, srv.URL+"/api/v1/nodes/n1", http.StatusOK)
	started, _ = sink.snapshot()
	if started != 1 {
		t.Fatalf("non-tree-scoped route opened a window: started=%d, want 1", started)
	}

	// (AC6d) A non-2xx tree-scoped read opens nothing...
	resumeTestGet(t, srv.URL+"/api/v1/trees/t1/broken", http.StatusServiceUnavailable)
	started, durations = sink.snapshot()
	if started != 1 {
		t.Fatalf("non-2xx tree read opened a window: started=%d, want 1", started)
	}
	// ...and a context compile after it observes nothing, because no window is
	// open for this user.
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
	_, durations = sink.snapshot()
	if len(durations) != 1 {
		t.Fatalf("compile after a non-2xx tree read must observe nothing: durations=%v", durations)
	}
}

// TestResumeMiddlewareMiniRouterOpensNewWindowAfterIdle proves the tracker is
// reached with real request timing: a later tree read, with the previous window
// already completed, opens a fresh window on a sibling tree-scoped pattern.
func TestResumeMiddlewareMiniRouterOpensNewWindowAfterIdle(t *testing.T) {
	// A zero idle gap makes every read after the first an "idle gap" read, so
	// the real-time middleware path can be asserted without sleeping.
	sink := &fakeSink{}
	tracker := NewResumeTracker(0)
	srv := httptest.NewServer(newResumeTestRouter(sink, tracker, "user-b"))
	defer srv.Close()

	resumeTestGet(t, srv.URL+"/api/v1/trees/t1", http.StatusOK)
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
	resumeTestGet(t, srv.URL+"/api/v1/trees/t1/events", http.StatusOK)
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)

	started, durations := sink.snapshot()
	if started != 2 {
		t.Fatalf("started=%d, want 2 (two dispatched resume windows)", started)
	}
	if len(durations) != 2 {
		t.Fatalf("durations=%v, want 2 observations", durations)
	}
	for i, d := range durations {
		if d < 0 {
			t.Fatalf("durations[%d]=%v, want a non-negative wall-clock reading", i, d)
		}
	}
}

// TestResumeMiddlewareIgnoresTreeWrites pins the fixed GAP-079 contract through
// real requests: a 2xx WRITE whose route pattern is tree-scoped is not a resume
// read, so resume_started_total stays at 0 and the tracker is never reached.
func TestResumeMiddlewareIgnoresTreeWrites(t *testing.T) {
	// A one-hour idle gap is load-bearing. If a write counted as a tree read it
	// would seed the user's entry with lastTreeRead=now, so the GET milliseconds
	// later would fall INSIDE the gap and open nothing; with a zero gap the GET
	// would open a window either way and the two behaviours would be
	// indistinguishable.
	sink := &fakeSink{}
	tracker := NewResumeTracker(time.Hour)
	srv := httptest.NewServer(newResumeTestRouter(sink, tracker, "user-w"))
	defer srv.Close()

	// (a) 2xx writes on the tree mount open no window. fakeSink mirrors
	// resume_started_total/resume_duration_seconds without touching Prometheus.
	for _, w := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/trees/t1/share"},
		{http.MethodPatch, "/api/v1/trees/t1"},
		{http.MethodDelete, "/api/v1/trees/t1"},
	} {
		resumeTestDo(t, w.method, srv.URL+w.path, http.StatusOK)
		started, durations := sink.snapshot()
		if started != 0 || len(durations) != 0 {
			t.Fatalf("2xx %s %s acted: started=%d durations=%v, want 0 and []", w.method, w.path, started, durations)
		}
		if n := resumeTestTrackedKeys(tracker); n != 0 {
			t.Fatalf("2xx %s %s was tracked: %d keys, want 0", w.method, w.path, n)
		}
	}

	// (b) The writes did not move the idle-gap clock either: the next 2xx GET is
	// this user's FIRST tree read, so it opens the window.
	resumeTestGet(t, srv.URL+"/api/v1/trees/t1", http.StatusOK)
	started, durations := sink.snapshot()
	if started != 1 || len(durations) != 0 {
		t.Fatalf("write then read: started=%d durations=%v, want started=1 durations=[]", started, durations)
	}
	// The window that read opened is a working one.
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
	_, durations = sink.snapshot()
	if len(durations) != 1 {
		t.Fatalf("the window opened by the read did not complete: durations=%v", durations)
	}
}

// TestResumeMiddlewareHeadTreeReadOpensWindow pins that HEAD is a resume read:
// the gate is GET-or-HEAD, not GET-only.
func TestResumeMiddlewareHeadTreeReadOpensWindow(t *testing.T) {
	sink := &fakeSink{}
	srv := httptest.NewServer(newResumeTestRouter(sink, NewResumeTracker(time.Hour), "user-h"))
	defer srv.Close()

	resumeTestDo(t, http.MethodHead, srv.URL+"/api/v1/trees/t1", http.StatusOK)
	started, durations := sink.snapshot()
	if started != 1 || len(durations) != 0 {
		t.Fatalf("HEAD tree read: started=%d durations=%v, want started=1 durations=[]", started, durations)
	}

	// Its window completes on the compiled context exactly like a GET's.
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
	_, durations = sink.snapshot()
	if len(durations) != 1 {
		t.Fatalf("the window opened by HEAD did not complete: durations=%v", durations)
	}
}

// TestResumeMiddlewareIgnoresNonReadContextCompile pins the same gate on the
// compile branch: a non-GET on the compile path observes nothing and, crucially,
// leaves the open window OPEN (a window consumed by a write would be a resume
// that never reaches a context compile).
func TestResumeMiddlewareIgnoresNonReadContextCompile(t *testing.T) {
	sink := &fakeSink{}
	srv := httptest.NewServer(newResumeTestRouter(sink, NewResumeTracker(time.Hour), "user-c2"))
	defer srv.Close()

	// Open a window with a real read.
	resumeTestGet(t, srv.URL+"/api/v1/trees/t1", http.StatusOK)
	if started, _ := sink.snapshot(); started != 1 {
		t.Fatalf("setup: started=%d, want 1", started)
	}

	// A 2xx POST on the compile path must not complete it...
	resumeTestDo(t, http.MethodPost, srv.URL+"/api/v1/context/n1", http.StatusOK)
	if _, durations := sink.snapshot(); len(durations) != 0 {
		t.Fatalf("a POST on the compile path observed a resume: durations=%v", durations)
	}

	// ...so the GET still completes it, exactly once.
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
	_, durations := sink.snapshot()
	if len(durations) != 1 {
		t.Fatalf("the window was consumed by the write: durations=%v, want 1", durations)
	}
}

// TestResumeMiddlewareIgnoresNon2xxTreeReads pins AC6d. The tracker runs with a
// ZERO idle gap so any tree read would open a window: with the default 5-minute
// gap the second read is simply too soon to start one, and the test would pass
// without pinning the status check at all.
func TestResumeMiddlewareIgnoresNon2xxTreeReads(t *testing.T) {
	sink := &fakeSink{}
	srv := httptest.NewServer(newResumeTestRouter(sink, NewResumeTracker(0), "user-c"))
	defer srv.Close()

	// A non-2xx tree read opens nothing...
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
	resumeTestGet(t, srv.URL+"/api/v1/trees/t1/broken", http.StatusServiceUnavailable)
	started, durations := sink.snapshot()
	if started != 0 || len(durations) != 0 {
		t.Fatalf("a non-2xx tree read acted: started=%d durations=%v, want 0 and []", started, durations)
	}

	// ...so a context compile after it observes nothing either.
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
	started, durations = sink.snapshot()
	if started != 0 || len(durations) != 0 {
		t.Fatalf("compile after a non-2xx tree read observed something: started=%d durations=%v", started, durations)
	}

	// Control on the SAME router: a 2xx tree read does open a window, proving
	// the zero-gap harness is live and the silence above is meaningful.
	resumeTestGet(t, srv.URL+"/api/v1/trees/t1", http.StatusOK)
	started, _ = sink.snapshot()
	if started != 1 {
		t.Fatalf("control: a 2xx tree read gave started=%d, want 1", started)
	}
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
	_, durations = sink.snapshot()
	if len(durations) != 1 {
		t.Fatalf("control: durations=%v, want 1 observation", durations)
	}
}

// TestResumeMiddlewareSkipsEmptyKey pins AC6e at the middleware boundary: an
// unauthenticated request (no user id) contributes nothing.
func TestResumeMiddlewareSkipsEmptyKey(t *testing.T) {
	sink := &fakeSink{}
	tracker := NewResumeTracker(0)
	srv := httptest.NewServer(newResumeTestRouter(sink, tracker, ""))
	defer srv.Close()

	resumeTestGet(t, srv.URL+"/api/v1/trees/t1", http.StatusOK)
	resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)

	started, durations := sink.snapshot()
	if started != 0 || len(durations) != 0 {
		t.Fatalf("empty key must be skipped: started=%d durations=%v", started, durations)
	}
	if n := resumeTestTrackedKeys(tracker); n != 0 {
		t.Fatalf("empty key must not be tracked: %d keys", n)
	}
}

func TestResumeMiddlewareNilSinkAndTrackerAreNoOps(t *testing.T) {
	// Each case drives a REAL chi router so RoutePattern() resolves and the
	// middleware reaches the sink/tracker it was given — a raw httptest
	// request resolves no pattern and would pass vacuously.
	build := func(sink ResumeSink, tr *ResumeTracker, keyFn func(*http.Request) string) *chi.Mux {
		r := chi.NewRouter()
		r.Route("/api/v1", func(r chi.Router) {
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					ctx := context.WithValue(req.Context(), resumeTestUserKey{}, "user-a")
					next.ServeHTTP(w, req.WithContext(ctx))
				})
			})
			r.Use(ResumeMiddleware(sink, keyFn, tr))
			r.Get("/trees/{tree_id}", resumeTestOK)
			r.Get("/context/{node_id}", resumeTestOK)
		})
		return r
	}
	userKeyFn := func(r *http.Request) string {
		v, _ := r.Context().Value(resumeTestUserKey{}).(string)
		return v
	}

	cases := []struct {
		name    string
		sink    ResumeSink
		tracker *ResumeTracker
		keyFn   func(*http.Request) string
	}{
		{name: "nil sink", sink: nil, tracker: NewResumeTracker(0), keyFn: userKeyFn},
		{name: "nil tracker", sink: &fakeSink{}, tracker: nil, keyFn: userKeyFn},
		{name: "nil keyFn", sink: &fakeSink{}, tracker: NewResumeTracker(0), keyFn: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(build(tc.sink, tc.tracker, tc.keyFn))
			defer srv.Close()

			resumeTestGet(t, srv.URL+"/api/v1/trees/t1", http.StatusOK)
			resumeTestGet(t, srv.URL+"/api/v1/context/n1", http.StatusOK)
		})
	}
}

// ---------------------------------------------------------------------------
// Tracker: idle-gap boundary, single observation, empty key, race, prune
// ---------------------------------------------------------------------------

func TestResumeTrackerIdleGapBoundary(t *testing.T) {
	const gap = 5 * time.Minute
	t0 := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	t.Run("inside the gap opens nothing, at the boundary does", func(t *testing.T) {
		tr := NewResumeTracker(gap)
		if !tr.NoteTreeRead("u", t0) {
			t.Fatal("first read must open a window")
		}
		// Every read moves the idle-gap clock, so each assertion is measured
		// from the immediately preceding read.
		inside := t0.Add(1 * time.Minute)
		if tr.NoteTreeRead("u", inside) {
			t.Error("a read inside the idle gap must not open a window")
		}
		justShort := inside.Add(gap - time.Nanosecond)
		if tr.NoteTreeRead("u", justShort) {
			t.Error("a read one nanosecond short of the gap must not open a window")
		}
		if !tr.NoteTreeRead("u", justShort.Add(gap)) {
			t.Error("a read exactly one idle gap after the previous read must open a window")
		}
	})

	t.Run("a read exactly at the gap from the first read opens a window", func(t *testing.T) {
		tr := NewResumeTracker(gap)
		if !tr.NoteTreeRead("u", t0) {
			t.Fatal("first read must open a window")
		}
		if !tr.NoteTreeRead("u", t0.Add(gap)) {
			t.Error("\"at least the idle gap\" must include the exact boundary")
		}
	})

	t.Run("distinct keys track independently", func(t *testing.T) {
		tr := NewResumeTracker(gap)
		if !tr.NoteTreeRead("a", t0) || !tr.NoteTreeRead("b", t0) {
			t.Fatal("a first read by each of two users must open a window each")
		}
		if tr.NoteTreeRead("a", t0.Add(time.Second)) {
			t.Error("b's read must not satisfy a's idle gap")
		}
	})
}

func TestResumeTrackerObservesOncePerWindow(t *testing.T) {
	tr := NewResumeTracker(time.Minute)
	t0 := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	tr.NoteTreeRead("u", t0)
	seconds, observed := tr.NoteContextCompile("u", t0.Add(3*time.Second))
	if !observed || seconds != 3 {
		t.Fatalf("completing a window: seconds=%v observed=%v, want 3 true", seconds, observed)
	}

	if _, observed := tr.NoteContextCompile("u", t0.Add(4*time.Second)); observed {
		t.Error("a further compile with no open window must observe nothing")
	}
	if _, observed := tr.NoteContextCompile("stranger", t0); observed {
		t.Error("a compile for an untracked key must observe nothing")
	}

	// A compile does not reset the idle-gap clock, so a later read after the
	// gap opens a fresh window that observes exactly once more.
	if !tr.NoteTreeRead("u", t0.Add(2*time.Minute)) {
		t.Fatal("a read one idle gap after the last read must open a new window")
	}
	seconds, observed = tr.NoteContextCompile("u", t0.Add(2*time.Minute+1500*time.Millisecond))
	if !observed || seconds != 1.5 {
		t.Fatalf("second window: seconds=%v observed=%v, want 1.5 true", seconds, observed)
	}
	if _, observed := tr.NoteContextCompile("u", t0.Add(2*time.Minute+2*time.Second)); observed {
		t.Error("the second window must not yield a second observation")
	}
}

func TestResumeTrackerSkipsEmptyKey(t *testing.T) {
	tr := NewResumeTracker(time.Minute)
	t0 := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	if tr.NoteTreeRead("", t0) {
		t.Error("an empty key must not open a window")
	}
	if _, observed := tr.NoteContextCompile("", t0); observed {
		t.Error("an empty key must not observe a duration")
	}
	if n := resumeTestTrackedKeys(tr); n != 0 {
		t.Fatalf("empty keys must not be tracked: %d keys", n)
	}
}

func TestResumeTrackerConcurrentUse(t *testing.T) {
	tr := NewResumeTracker(time.Millisecond)
	const workers, iterations = 8, 200

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			key := fmt.Sprintf("user-%d", w%4)
			for i := 0; i < iterations; i++ {
				tr.NoteTreeRead(key, time.Now())
				tr.NoteContextCompile(key, time.Now())
			}
		}(w)
	}
	wg.Wait()
}

func TestResumeTrackerPruneBoundDropsStaleEntries(t *testing.T) {
	const gap = time.Minute
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	now := base.Add(10 * time.Minute)

	tr := NewResumeTracker(gap)

	// A key with an OPEN window whose read is already stale: the prune must
	// leave it alone, because its observation can still land.
	if !tr.NoteTreeRead("stuck", base) {
		t.Fatal("first read must open a window")
	}

	// Fill the tracker to the bound with idle, windowless (already completed)
	// entries.
	for i := 0; resumeTestTrackedKeys(tr) < resumePruneBound; i++ {
		key := fmt.Sprintf("stale-%d", i)
		tr.NoteTreeRead(key, base)
		if _, ok := tr.NoteContextCompile(key, base.Add(time.Second)); !ok {
			t.Fatalf("stale-%d: filling the tracker must complete its window", i)
		}
	}
	if got := resumeTestTrackedKeys(tr); got != resumePruneBound {
		t.Fatalf("tracked keys = %d, want %d", got, resumePruneBound)
	}

	// A read for a NEW key pushes to the bound and triggers the prune.
	if !tr.NoteTreeRead("fresh", now) {
		t.Fatal("a new key must open a window")
	}

	got := resumeTestTrackedKeys(tr)
	if got != 2 {
		t.Fatalf("tracked keys after prune = %d, want 2 (the stale closed entries dropped, both open windows kept)", got)
	}

	// The fresh open window survived and is still observable.
	seconds, observed := tr.NoteContextCompile("fresh", now.Add(2*time.Second))
	if !observed || seconds != 2 {
		t.Fatalf("fresh window after prune: seconds=%v observed=%v, want 2 true", seconds, observed)
	}
	// The stale-but-open window survived too.
	if _, observed := tr.NoteContextCompile("stuck", now); !observed {
		t.Error("a window that was open must never be pruned away")
	}
}

// ---------------------------------------------------------------------------
// Metrics methods
// ---------------------------------------------------------------------------

func TestMetricsResumeMethodsAreNilSafe(t *testing.T) {
	var nilMetrics *Metrics
	nilMetrics.IncResumeStarted()
	nilMetrics.ObserveResumeDuration(1)

	// A constructed-but-unpopulated Metrics must also be a no-op rather than a
	// nil-interface panic.
	(&Metrics{}).IncResumeStarted()
	(&Metrics{}).ObserveResumeDuration(1)
}

func TestNewMetricsRegistersResumeMetrics(t *testing.T) {
	m := NewMetrics()
	m.IncResumeStarted()
	m.ObserveResumeDuration(31)

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	var names []string
	var sawStarted bool
	var s30, sInf uint64
	var saw30Boundary bool
	for _, fam := range families {
		names = append(names, fam.GetName())
		switch fam.GetName() {
		case "resume_started_total":
			if fam.GetType().String() != "COUNTER" {
				t.Errorf("resume_started_total type = %s, want COUNTER", fam.GetType())
			}
			for _, mt := range fam.GetMetric() {
				if mt.GetCounter().GetValue() != 1 {
					t.Errorf("resume_started_total = %v, want 1 after one IncResumeStarted", mt.GetCounter().GetValue())
				}
				sawStarted = true
			}
		case "resume_duration_seconds":
			if fam.GetType().String() != "HISTOGRAM" {
				t.Errorf("resume_duration_seconds type = %s, want HISTOGRAM", fam.GetType())
			}
			for _, mt := range fam.GetMetric() {
				for _, b := range mt.GetHistogram().GetBucket() {
					if b.GetUpperBound() == 30 {
						saw30Boundary = true
						s30 = b.GetCumulativeCount()
					}
				}
				if mt.GetHistogram().GetSampleCount() != 1 {
					t.Errorf("sample count = %d, want 1", mt.GetHistogram().GetSampleCount())
				}
				if got := mt.GetHistogram().GetSampleSum(); got != 31 {
					t.Errorf("sample sum = %v, want 31", got)
				}
				sInf = mt.GetHistogram().GetSampleCount()
			}
		}
	}
	if !sawStarted {
		t.Errorf("resume_started_total not registered; registered: %v", names)
	}
	if !saw30Boundary {
		t.Fatalf("resume_duration_seconds must carry the 30s SLO bucket boundary; registered: %v", names)
	}
	if s30 != 0 {
		t.Errorf("le=30 cumulative count = %v, want 0: a 31s observation must not fall in the 30s bucket", s30)
	}
	if sInf != 1 {
		t.Errorf("total observations = %v, want 1", sInf)
	}
}
