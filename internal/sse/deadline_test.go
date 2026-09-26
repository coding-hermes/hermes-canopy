package sse

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// deadlineRecorder implements ResponseWriter + SetWriteDeadline and records
// every deadline call, so the FrameWriter policy can be asserted directly.
type deadlineRecorder struct {
	mu    sync.Mutex
	calls []time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, t)
	return nil
}

func (d *deadlineRecorder) Header() http.Header { return http.Header{} }

func (d *deadlineRecorder) WriteHeader(int) {}

func (d *deadlineRecorder) Write(p []byte) (int, error) { return len(p), nil }

func (d *deadlineRecorder) recorded() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]time.Time(nil), d.calls...)
}

// errDeadliner refuses every deadline (the test-recorder shape: the policy
// must degrade to inert, not poison the stream).
type errDeadliner struct{ calls int }

func (e *errDeadliner) SetWriteDeadline(time.Time) error { e.calls++; return errors.New("unsupported") }
func (e *errDeadliner) Header() http.Header              { return http.Header{} }
func (e *errDeadliner) WriteHeader(int)                  {}
func (e *errDeadliner) Write(p []byte) (int, error)      { return len(p), nil }

// sseStreamHandler is the production handler idiom: SSE headers, initial
// flush, FrameWriter clear, then one frame every interval, each write
// preceded by a bounded re-arm.
func sseStreamHandler(fw *FrameWriter, frames int, interval time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		fw.ClearWriteDeadline()
		for i := 0; i < frames; i++ {
			time.Sleep(interval)
			fw.BeforeFrame()
			if _, err := fmt.Fprintf(w, "event: tick\ndata: frame-%d\n\n", i); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

// streamServer serves handler from a REAL http.Server (httptest.NewUnstartedServer
// + an explicit WriteTimeout) and is read by a real TCP/HTTP client on an
// allowlist SSE path. httptest.NewRecorder is useless here: a recorder has
// no transport deadline, so the server-side WriteTimeout could never be
// observed. marked=true replays the server's global marking middleware.
func newStreamServer(t *testing.T, marked bool, writeTimeout time.Duration, next http.Handler) *httptest.Server {
	t.Helper()
	var h http.Handler = next
	if marked {
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, MarkWriteDeadlineExempt(r))
		})
	}
	h = middleware.Timeout(60 * time.Second)(h)

	sst := httptest.NewUnstartedServer(h)
	sst.Config.WriteTimeout = writeTimeout
	sst.Start()
	t.Cleanup(func() {
		sst.CloseClientConnections()
		sst.Close()
	})
	return sst
}

func readTickFrames(t *testing.T, s *httptest.Server, path string, maxWait time.Duration) (int, time.Duration, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	client := &http.Client{Timeout: maxWait}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, time.Since(start), err
	}
	defer resp.Body.Close()
	frames := 0
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event: tick") {
			frames++
		}
	}
	return frames, time.Since(start), scanner.Err()
}

// TestFrameWriter_StreamOutlivesServerWriteTimeout is the GAP-100 regression
// test on a REAL http.Server with a real TCP client (1s WriteTimeout, a
// stream that would run 2.8s): a MARKED allowlist stream receives every
// frame and stays open well past the whole-response WriteTimeout, while the
// same handler UNMARKED (the pre-fix shape: no marker, no clear) is cut at
// the WriteTimeout boundary.
func TestFrameWriter_StreamOutlivesServerWriteTimeout(t *testing.T) {
	const (
		writeTimeout = 1 * time.Second
		frames       = 28
		interval     = 100 * time.Millisecond
		path         = "/api/v1/trees/t-1/events"
	)

	t.Run("marked stream survives past WriteTimeout", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The global middleware has already marked r; the handler's
			// FrameWriter clears the whole-response deadline and re-arms
			// per frame — the production mechanism, unmarked handlers
			// cannot do this.
			fw := NewFrameWriter(w, r)
			sseStreamHandler(fw, frames, interval)(w, r)
		})
		s := newStreamServer(t, true, writeTimeout, h)

		got, elapsed, err := readTickFrames(t, s, path, 10*time.Second)
		if err != nil {
			t.Fatalf("stream read error after %v: %v", elapsed, err)
		}
		if got != frames {
			t.Fatalf("frames = %d, want %d — the stream did not survive the server WriteTimeout", got, frames)
		}
		if elapsed < 2*writeTimeout {
			t.Fatalf("stream closed after %v; want it to stay open past 2×WriteTimeout (%v)", elapsed, 2*writeTimeout)
		}
	})

	t.Run("unmarked stream is still cut at WriteTimeout", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Unmarked request: NewFrameWriter is inert — the pre-fix shape.
			sseStreamHandler(NewFrameWriter(w, r), frames, interval)(w, r)
		})
		s := newStreamServer(t, false, writeTimeout, h)

		got, elapsed, _ := readTickFrames(t, s, path, 10*time.Second)
		if got >= frames {
			t.Fatalf("unmarked stream delivered all %d frames; want it cut at the WriteTimeout", got)
		}
		if elapsed > 2*writeTimeout {
			t.Fatalf("unmarked stream stayed open %v; want it cut around the %v WriteTimeout", elapsed, writeTimeout)
		}
	})
}

// TestFrameWriter_OrdinaryRequestKeepsServerWriteTimeout pins the bounded
// deadline for ordinary (non-stream) responses: a plain chunked response —
// no exemption, no FrameWriter — writing past the boundary is cut by the
// server's whole-response WriteTimeout. An absolute deadline only surfaces
// on the next write after expiry, so the handler must keep writing for the
// cut to be client-observable (the same physics the pre-fix streams died
// from).
func TestFrameWriter_OrdinaryRequestKeepsServerWriteTimeout(t *testing.T) {
	const (
		writeTimeout = 1 * time.Second
		lines        = 6
	)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ordinary chunked response on a non-stream path: no FrameWriter (an
		// unmarked request would make one inert anyway), no exemption.
		flusher := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for i := 0; i < lines; i++ {
			time.Sleep(200 * time.Millisecond)
			if _, err := fmt.Fprintf(w, "line-%d\n", i); err != nil {
				return
			}
			flusher.Flush()
		}
	})
	s := newStreamServer(t, false, writeTimeout, h)

	got, elapsed, _ := readTickFrames(t, s, "/api/v1/trees/t-1/nodes", 10*time.Second)
	if got >= lines {
		t.Fatalf("ordinary response delivered all %d lines; want it cut at the WriteTimeout", got)
	}
	if elapsed < writeTimeout-150*time.Millisecond {
		t.Fatalf("ordinary response died after %v; want survival to the %v WriteTimeout", elapsed, writeTimeout)
	}
	if elapsed > 2*writeTimeout {
		t.Fatalf("ordinary response ran %v; want it cut around the %v WriteTimeout", elapsed, writeTimeout)
	}
}

// TestFrameWriter_BoundedPerWriteDeadline asserts the helper contract
// directly: one zero-time clear after headers, then a strictly increasing,
// bounded deadline armed before every frame write; inert for unmarked
// requests and for writers that refuse deadlines.
func TestFrameWriter_BoundedPerWriteDeadline(t *testing.T) {
	t.Run("marked request: clear once, re-arm per frame, bounded", func(t *testing.T) {
		rec := &deadlineRecorder{}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/trees/t-1/events", nil)
		fw := NewFrameWriterWithDeadline(rec, MarkWriteDeadlineExempt(req), 50*time.Millisecond)

		fw.ClearWriteDeadline()
		for i := 0; i < 3; i++ {
			fw.BeforeFrame()
			if _, err := rec.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}

		calls := rec.recorded()
		if len(calls) != 4 {
			t.Fatalf("deadline calls = %d, want 4 (one clear + one per frame)", len(calls))
		}
		if !calls[0].IsZero() {
			t.Fatalf("first call armed %v; want the zero time (whole-response clear)", calls[0])
		}
		prev := time.Time{}
		for i, d := range calls[1:] {
			if !d.After(prev) {
				t.Fatalf("frame %d armed %v, not strictly increasing (prev %v)", i, d, prev)
			}
			bound := time.Now().Add(2 * time.Minute)
			if d.Before(time.Now().Add(-2*time.Second)) || d.After(bound) {
				t.Fatalf("frame %d armed %v; want a bounded near-term deadline (~50ms, before %v)", i, d, bound)
			}
			prev = d
		}
	})

	t.Run("unmarked request is inert", func(t *testing.T) {
		rec := &deadlineRecorder{}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/trees/t-1/nodes", nil)
		fw := NewFrameWriterWithDeadline(rec, req, 50*time.Millisecond)
		fw.ClearWriteDeadline()
		fw.BeforeFrame()
		fw.BeforeFrame()
		if calls := rec.recorded(); len(calls) != 0 {
			t.Fatalf("unmarked request touched the deadline %d times; want 0", len(calls))
		}
	})

	t.Run("writer that refuses deadlines degrades to inert", func(t *testing.T) {
		ed := &errDeadliner{}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/trees/t-1/events", nil)
		fw := NewFrameWriterWithDeadline(ed, MarkWriteDeadlineExempt(req), 50*time.Millisecond)
		fw.ClearWriteDeadline()
		for i := 0; i < 3; i++ {
			fw.BeforeFrame()
			_, _ = ed.Write([]byte("x"))
		}
		if ed.calls != 1 {
			t.Fatalf("deadline attempts = %d, want 1 (disarmed after the first refusal)", ed.calls)
		}
	})

	t.Run("zero value and nil receiver are safe", func(t *testing.T) {
		var fw *FrameWriter
		fw.ClearWriteDeadline()
		fw.BeforeFrame()
		(&FrameWriter{}).BeforeFrame()
	})
}

// TestSSEWriteDeadlineExemptMarker pins the marker round-trip.
func TestSSEWriteDeadlineExemptMarker(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees/t-1/events", nil)
	if WriteDeadlineExempt(req) {
		t.Fatal("plain request already marked")
	}
	marked := MarkWriteDeadlineExempt(req)
	if !WriteDeadlineExempt(marked) {
		t.Fatal("marked request lost the marker")
	}
	if WriteDeadlineExempt(req) {
		t.Fatal("marking mutated the original request context")
	}
}
