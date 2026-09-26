package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gatewayStub simulates the Hermes gateway api_server for service tests: it
// accepts POST /v1/runs, streams a scripted SSE sequence on /events, and
// answers status/stop/approval calls.
type gatewayStub struct {
	*httptest.Server

	events    []string // SSE data payloads to stream per run
	started   atomic.Int64
	stopped   atomic.Int64
	approvals atomic.Int64
	// getStatus overrides GET /v1/runs/{id} per run; runs absent from the
	// map 404 (simulating swept/unknown runs).
	getStatus map[string]string
	// stopNotFound makes POST /v1/runs/{id}/stop answer 404 (swept race).
	stopNotFound bool
}

func newGatewayStub(events []string) *gatewayStub {
	g := &gatewayStub{
		events:    events,
		getStatus: map[string]string{"run_test": "running"},
	}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			g.started.Add(1)
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"run_id":"run_test","status":"started"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/v1/runs/"), "/"):
			runID := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
			if st, ok := g.getStatus[runID]; ok {
				fmt.Fprintf(w, `{"status":%q,"last_event":"message.delta"}`, st)
				break
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"message":"not found"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_test/stop":
			if g.stopNotFound {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":{"message":"run_not_found"}}`)
				break
			}
			g.stopped.Add(1)
			fmt.Fprint(w, `{"run_id":"run_test","status":"stopping"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_test/approval":
			g.approvals.Add(1)
			fmt.Fprint(w, `{"object":"hermes.run.approval_response","run_id":"run_test","choice":"once","resolved":1}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_test/events":
			w.Header().Set("Content-Type", "text/event-stream")
			for _, payload := range g.events {
				fmt.Fprintf(w, "data: %s\n\n", payload)
			}
			fmt.Fprint(w, ": stream closed\n\n")
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"message":"not found"}}`)
		}
	}))
	return g
}

func TestServiceStartRunObservesEvents(t *testing.T) {
	stub := newGatewayStub([]string{
		`{"event":"message.delta","run_id":"run_test","timestamp":1.0,"delta":"hi"}`,
		`{"event":"tool.started","run_id":"run_test","timestamp":2.0,"tool":"terminal","preview":"ls"}`,
		`{"event":"run.completed","run_id":"run_test","timestamp":3.0,"output":"done","usage":{"total_tokens":7}}`,
	})
	defer stub.Close()

	c, err := NewClient(stub.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(c)
	t.Cleanup(svc.Close)

	rec, err := svc.StartRun(context.Background(), "hello", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.RunID != "run_test" || rec.Status != "started" {
		t.Fatalf("unexpected initial record: %+v", rec)
	}

	// Wait for the observer to consume the stream.
	deadline := time.Now().Add(3 * time.Second)
	for {
		r, ok := svc.Run("run_test")
		if ok && r.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			r, _ := svc.Run("run_test")
			t.Fatalf("observer did not reach completed; status=%q events=%d", r.Status, len(r.Events))
		}
		time.Sleep(20 * time.Millisecond)
	}

	r, _ := svc.Run("run_test")
	if r.Output != "done" || r.LastEvent != "run.completed" {
		t.Fatalf("unexpected record: %+v", r)
	}
	if len(r.Events) != 3 {
		t.Fatalf("want 3 events, got %d", len(r.Events))
	}
	if r.Usage["total_tokens"] != float64(7) {
		t.Fatalf("usage not captured: %+v", r.Usage)
	}

	list := svc.ListRuns(context.Background())
	if len(list) != 1 || list[0].RunID != "run_test" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

type runOutputSinkStub struct {
	mu     sync.Mutex
	err    error
	inputs []PersistRunOutputInput
}

func (s *runOutputSinkStub) PersistRunOutput(_ context.Context, in PersistRunOutputInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, in)
	return s.err
}

func (s *runOutputSinkStub) snapshot() []PersistRunOutputInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PersistRunOutputInput(nil), s.inputs...)
}

func TestServiceCompletedRunPersistsOutputOnlyForContextRuns(t *testing.T) {
	tests := []struct {
		name       string
		sourceNode string
		withSink   bool
		wantCalls  int
	}{
		{name: "context run with sink", sourceNode: "source-node", withSink: true, wantCalls: 1},
		{name: "context-free run with sink", withSink: true, wantCalls: 0},
		{name: "context run without sink", sourceNode: "source-node", wantCalls: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newGatewayStub([]string{
				`{"event":"run.completed","run_id":"run_test","timestamp":1.0,"output":"done"}`,
			})
			defer stub.Close()
			client, err := NewClient(stub.URL, "k")
			if err != nil {
				t.Fatal(err)
			}
			svc := NewService(client)
			t.Cleanup(svc.Close)
			sink := &runOutputSinkStub{}
			if tt.withSink {
				svc.SetRunOutputSink(sink)
			}

			_, err = svc.StartRunWithContext(context.Background(), StartRunInput{
				Message:      "hello",
				SourceNodeID: tt.sourceNode,
			})
			if err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, svc, "run_test", "completed")

			inputs := sink.snapshot()
			if len(inputs) != tt.wantCalls {
				t.Fatalf("sink calls = %d, want %d: %+v", len(inputs), tt.wantCalls, inputs)
			}
			if tt.wantCalls == 1 {
				if inputs[0].RunID != "run_test" || inputs[0].SourceNodeID != tt.sourceNode || inputs[0].Output != "done" {
					t.Fatalf("unexpected persistence input: %+v", inputs[0])
				}
			}
		})
	}
}

func TestServiceOutputSinkFailureKeepsRunCompleted(t *testing.T) {
	stub := newGatewayStub([]string{
		`{"event":"run.completed","run_id":"run_test","timestamp":1.0,"output":"done"}`,
	})
	defer stub.Close()
	client, err := NewClient(stub.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(client)
	t.Cleanup(svc.Close)
	sink := &runOutputSinkStub{err: errors.New("node store unavailable")}
	svc.SetRunOutputSink(sink)
	if _, err := svc.StartRunWithContext(context.Background(), StartRunInput{
		Message:      "hello",
		SourceNodeID: "source-node",
	}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, svc, "run_test", "completed")
	rec, ok := svc.Run("run_test")
	if !ok || rec.Status != "completed" {
		t.Fatalf("run status = %q (ok=%v), want completed", rec.Status, ok)
	}
	if rec.OutputPersistWarning != "node store unavailable" {
		t.Fatalf("warning = %q, want sink error", rec.OutputPersistWarning)
	}
}

func TestServiceStartRunGatewayDown(t *testing.T) {
	// Point at a closed port: connection refused.
	c, _ := NewClient("http://127.0.0.1:1", "k")
	svc := NewService(c)
	t.Cleanup(svc.Close)
	if _, err := svc.StartRun(context.Background(), "x", ""); err == nil {
		t.Fatal("want error when gateway is down")
	}
	if err := svc.Connected(context.Background()); err == nil {
		t.Fatal("want health error when gateway is down")
	}
}

func TestServiceStopAndApproval(t *testing.T) {
	stub := newGatewayStub(nil)
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewService(c)
	t.Cleanup(svc.Close)

	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopRun(context.Background(), "run_test"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RespondApproval(context.Background(), "run_test", "appr-1", "once"); err != nil {
		t.Fatal(err)
	}
	if stub.stopped.Load() != 1 || stub.approvals.Load() != 1 {
		t.Fatalf("want 1 stop + 1 approval on gateway, got %d/%d", stub.stopped.Load(), stub.approvals.Load())
	}
	r, ok := svc.Run("run_test")
	if !ok || r.Status != "stopping" {
		t.Fatalf("record should be stopping: %+v ok=%v", r, ok)
	}
}

func TestServiceFanoutDeliversLiveEvents(t *testing.T) {
	stub := newGatewayStub([]string{
		`{"event":"message.delta","run_id":"run_test","timestamp":1.0,"delta":"a"}`,
		`{"event":"run.completed","run_id":"run_test","timestamp":2.0,"output":"ok"}`,
	})
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewService(c)
	t.Cleanup(svc.Close)

	rec, err := svc.StartRun(context.Background(), "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel, err := svc.Subscribe(rec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	var got []RunEvent
	deadline := time.Now().Add(3 * time.Second)
	for len(got) < 2 {
		select {
		case se := <-ch:
			got = append(got, se.Event)
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timed out; got %d events", len(got))
		}
	}
	if got[0].Event != "message.delta" || got[0].Delta != "a" {
		t.Fatalf("unexpected fan-out: %+v", got)
	}
	if got[1].Event != "run.completed" {
		t.Fatalf("unexpected fan-out: %+v", got)
	}
}

func TestServiceEventsHistory(t *testing.T) {
	stub := newGatewayStub([]string{
		`{"event":"run.failed","run_id":"run_test","timestamp":1.0,"error":"boom"}`,
	})
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewService(c)
	t.Cleanup(svc.Close)
	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		evs, ok := svc.Events("run_test")
		if ok && len(evs) == 1 {
			if evs[0].Error != "boom" {
				t.Fatalf("unexpected event: %+v", evs[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("event history never populated")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServiceSubscribeUnknownRun(t *testing.T) {
	c, _ := NewClient("http://127.0.0.1:1", "k")
	svc := NewService(c)
	t.Cleanup(svc.Close)
	if _, _, err := svc.Subscribe("nope"); err == nil {
		t.Fatal("want error for unknown run")
	}
}

// ─── persistence + backfill + idempotent stop (GAP-054) ──────────────────

// seedStateFile writes RunRecords as one JSONL line each (the same shape
// persistLocked produces) so tests can simulate a previous canopyd process.
func seedStateFile(t *testing.T, path string, recs ...RunRecord) {
	t.Helper()
	var sb strings.Builder
	for _, rec := range recs {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForStatus(t *testing.T, svc *Service, runID, status string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if rec, ok := svc.Run(runID); ok && rec.Status == status {
			return
		}
		if time.Now().After(deadline) {
			rec, _ := svc.Run(runID)
			t.Fatalf("run %s never reached %s; status=%q", runID, status, rec.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestServicePersistRestoreTerminalRun proves AC1 restart-style: a run that
// reached a terminal status before "restart" is still listed (with its
// terminal status) after a fresh service is built on the same state file.
func TestServicePersistRestoreTerminalRun(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "runs.jsonl")
	stub := newGatewayStub([]string{
		`{"event":"message.delta","run_id":"run_test","timestamp":1.0,"delta":"hi"}`,
		`{"event":"run.completed","run_id":"run_test","timestamp":2.0,"output":"done"}`,
	})
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewServiceWithState(c, stateFile)
	t.Cleanup(svc.Close) // LIFO: before t.TempDir's RemoveAll

	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, svc, "run_test", "completed")

	// The state file must exist and round-trip the terminal status.
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(stateFile)
		if err == nil && strings.Contains(string(raw), `"status":"completed"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state file never persisted terminal status (err=%v)", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Restart-style: a FRESH service on the same file, backed by a stub
	// whose GET /v1/runs/{id} 404s. The terminal run must still be listed,
	// never missing (AC1).
	stub2 := newGatewayStub(nil)
	defer stub2.Close()
	c2, _ := NewClient(stub2.URL, "k")
	svc2 := NewServiceWithState(c2, stateFile)
	t.Cleanup(svc2.Close)
	list := svc2.ListRuns(context.Background())
	if len(list) != 1 || list[0].RunID != "run_test" {
		t.Fatalf("run lost across restart: %+v", list)
	}
	if list[0].Status != "completed" {
		t.Fatalf("restored status = %q, want completed", list[0].Status)
	}
}

// TestServiceBackfillRefreshesNonTerminal proves the per-run status refresh
// on startup: a persisted non-terminal record is refreshed via the existing
// GET /v1/runs/{id} (no list endpoint is invented).
func TestServiceBackfillRefreshesNonTerminal(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "runs.jsonl")
	seedStateFile(t, stateFile, RunRecord{
		RunID:     "run_test",
		Status:    "started",
		CreatedAt: time.Now().UTC(),
	})
	stub := newGatewayStub(nil) // GET /v1/runs/run_test -> "running"
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewServiceWithState(c, stateFile)
	t.Cleanup(svc.Close)

	// Run() returns the registry record without live refresh, so the
	// refreshed status proves Backfill wrote it back.
	rec, ok := svc.Run("run_test")
	if !ok {
		t.Fatal("restored run missing")
	}
	if rec.Status != "running" {
		t.Fatalf("backfill did not refresh status: %q, want running", rec.Status)
	}
}

// TestServiceBackfillSweptRunNotMissing proves AC1 swept semantics: a
// non-terminal persisted record whose gateway 404s after restart is shown
// as terminal 'not_found', still present in the registry.
func TestServiceBackfillSweptRunNotMissing(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "runs.jsonl")
	seedStateFile(t, stateFile, RunRecord{
		RunID:     "run_test",
		Status:    "started",
		CreatedAt: time.Now().UTC(),
	})
	stub := newGatewayStub(nil)
	delete(stub.getStatus, "run_test") // run_test absent -> GET 404
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewServiceWithState(c, stateFile)
	t.Cleanup(svc.Close)

	rec, ok := svc.Run("run_test")
	if !ok {
		t.Fatal("swept run missing from registry after backfill")
	}
	if rec.Status != "not_found" {
		t.Fatalf("swept run status = %q, want not_found", rec.Status)
	}
}

// TestServiceBackfillGatewayDownNonFatal proves AC3: a down gateway at
// startup must not panic or lose records — persisted statuses are kept.
func TestServiceBackfillGatewayDownNonFatal(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "runs.jsonl")
	seedStateFile(t, stateFile, RunRecord{
		RunID:     "run_test",
		Status:    "running",
		CreatedAt: time.Now().UTC(),
	})
	c, _ := NewClient("http://127.0.0.1:1", "k") // connection refused
	svc := NewServiceWithState(c, stateFile)     // must not panic
	t.Cleanup(svc.Close)                         // and must be safe to close

	rec, ok := svc.Run("run_test")
	if !ok {
		t.Fatal("record lost when gateway is down")
	}
	if rec.Status != "running" {
		t.Fatalf("persisted status should be kept, got %q", rec.Status)
	}
}

// TestServiceStopRunTerminalIdempotent proves AC2: stopping an
// already-terminal run returns nil WITHOUT calling the gateway, and the
// terminal status is untouched. A genuinely unknown run still 404s.
func TestServiceStopRunTerminalIdempotent(t *testing.T) {
	stub := newGatewayStub([]string{
		`{"event":"run.completed","run_id":"run_test","timestamp":1.0,"output":"done"}`,
	})
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewService(c)
	t.Cleanup(svc.Close)

	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, svc, "run_test", "completed")

	if err := svc.StopRun(context.Background(), "run_test"); err != nil {
		t.Fatalf("stop on terminal run should be nil: %v", err)
	}
	if stub.stopped.Load() != 0 {
		t.Fatalf("gateway stop called on terminal run: %d", stub.stopped.Load())
	}
	rec, _ := svc.Run("run_test")
	if rec.Status != "completed" {
		t.Fatalf("terminal status regressed: %q", rec.Status)
	}

	// Genuinely unknown run (absent from registry, gateway 404s): still
	// ErrRunNotFound.
	if err := svc.StopRun(context.Background(), "ghost"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("unknown run stop = %v, want ErrRunNotFound", err)
	}
}

// TestServiceStopRunSweptRaceMarksNotFound proves AC2's swept race: a
// non-terminal registry run whose gateway stop 404s is marked 'not_found'
// and StopRun returns nil (not ErrRunNotFound).
func TestServiceStopRunSweptRaceMarksNotFound(t *testing.T) {
	stub := newGatewayStub(nil)
	stub.stopNotFound = true // POST /v1/runs/run_test/stop -> 404
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewService(c)
	t.Cleanup(svc.Close)
	svc.mu.Lock()
	svc.runs["run_test"] = &RunRecord{RunID: "run_test", Status: "running", CreatedAt: time.Now().UTC()}
	svc.mu.Unlock()

	if err := svc.StopRun(context.Background(), "run_test"); err != nil {
		t.Fatalf("swept-race stop should be nil: %v", err)
	}
	rec, _ := svc.Run("run_test")
	if rec.Status != "not_found" {
		t.Fatalf("status = %q, want not_found", rec.Status)
	}
}

// TestServiceStopRunNonTerminalStillCallsGateway is the regression guard:
// stopping a live run still forwards to the gateway and marks 'stopping'.
func TestServiceStopRunNonTerminalStillCallsGateway(t *testing.T) {
	stub := newGatewayStub(nil)
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewService(c)
	t.Cleanup(svc.Close)

	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopRun(context.Background(), "run_test"); err != nil {
		t.Fatal(err)
	}
	if stub.stopped.Load() != 1 {
		t.Fatalf("gateway stop not forwarded: %d", stub.stopped.Load())
	}
	rec, _ := svc.Run("run_test")
	if rec.Status != "stopping" {
		t.Fatalf("status = %q, want stopping", rec.Status)
	}
}

// ─── lifecycle: Close stops background writes (CI-006) ───────────────────
//
// The flake this guards: a state-file test ends, its bounded stub cleanup
// closes the SSE connection, and persist() used to drop a .runs-*.jsonl.tmp into the
// test's t.TempDir() exactly while t.TempDir()'s cleanup os.RemoveAll is
// between "remove children" and "unlinkat(dir)" — "directory not empty". The
// tests below make that window deterministic instead of timing-dependent.

// liveGatewayStub is a gateway whose /events endpoint holds the SSE connection
// OPEN: it streams one event, flushes it, then blocks until the client hangs
// up. That is the state a real in-flight run leaves observe() in — parked in a
// body read — which is what makes the teardown race reproducible on demand
// (the client, not the server, ends the read).
type liveGatewayStub struct {
	*httptest.Server

	stopped          atomic.Int64
	firstEventDelay  time.Duration
	eventStarted     chan struct{}
	eventStartedOnce sync.Once
}

func newLiveGatewayStub(firstEvent string) *liveGatewayStub {
	return newLiveGatewayStubWithDelay(firstEvent, 0)
}

func newLiveGatewayStubWithDelay(firstEvent string, delay time.Duration) *liveGatewayStub {
	g := &liveGatewayStub{
		firstEventDelay: delay,
		eventStarted:    make(chan struct{}),
	}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"run_id":"run_test","status":"started"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_test/events":
			g.eventStartedOnce.Do(func() { close(g.eventStarted) })
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if g.firstEventDelay > 0 {
				timer := time.NewTimer(g.firstEventDelay)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					return
				}
			}
			// A test stub must not let a full socket buffer turn cleanup into
			// an unbounded wait. The request context covers the delay and the
			// response deadline covers the write itself.
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			if _, err := fmt.Fprintf(w, "data: %s\n\n", firstEvent); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// No close sentinel and no EOF: hold the stream until the client
			// goes away (or the test tears the stub down).
			<-r.Context().Done()
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_test":
			fmt.Fprint(w, `{"status":"running","last_event":"message.delta"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_test/stop":
			g.stopped.Add(1)
			fmt.Fprint(w, `{"run_id":"run_test","status":"stopping"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"message":"not found"}}`)
		}
	}))
	return g
}

const liveGatewayStubCloseTimeout = time.Second

func closeLiveGatewayStub(t *testing.T, stub *liveGatewayStub) {
	t.Helper()
	// Force active connections closed before waiting for httptest.Server.Close;
	// this makes the handler's request context observable even if the client
	// transport has not closed its side yet.
	stub.CloseClientConnections()
	done := make(chan struct{})
	go func() {
		stub.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(liveGatewayStubCloseTimeout):
		t.Errorf("live gateway stub did not close within %s", liveGatewayStubCloseTimeout)
	}
}

// dirEntry is one entry of a temp dir, captured so a test can prove that
// nothing was written after a point in time. The content hash is the load
// bearing part: a state file rewritten with different bytes must never compare
// equal, whatever the filesystem's mtime granularity is.
type dirEntry struct {
	name    string
	size    int64
	modTime time.Time
	sha256  string
}

func (e dirEntry) String() string {
	return fmt.Sprintf("%s size=%d mtime=%s sha256=%s",
		e.name, e.size, e.modTime.Format(time.RFC3339Nano), e.sha256)
}

func snapshotDir(t *testing.T, dir string) []dirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir) // sorted by name: deterministic order
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	out := make([]dirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		sum := ""
		if !e.IsDir() {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			h := sha256.Sum256(raw)
			sum = hex.EncodeToString(h[:])
		}
		out = append(out, dirEntry{name: e.Name(), size: info.Size(), modTime: info.ModTime(), sha256: sum})
	}
	return out
}

func dirSignature(entries []dirEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, e.String())
	}
	return strings.Join(parts, "\n")
}

func assertSameDir(t *testing.T, when string, want, got []dirEntry) {
	t.Helper()
	if dirSignature(want) != dirSignature(got) {
		t.Fatalf("state dir changed %s\nbefore:\n%s\nafter:\n%s", when, dirSignature(want), dirSignature(got))
	}
}

// TestServiceCloseStopsWritesAfterTeardown is the CI-006 regression guard: the
// moment a test calls Close, the service must stop touching its state
// directory — because the very next thing a test does is t.TempDir()'s
// RemoveAll, and a temp file landing in that window is the flake.
//
// Deterministic by construction: the stub holds the SSE connection open, so
// the observer is parked in a body read when Close cancels the service
// context. The cancellation drives observe's error path — the one that
// persists — strictly inside the Close call, so a missing closed-guard shows
// up here on every run instead of one in ten.
func TestServiceCloseStopsWritesAfterTeardown(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "runs.jsonl")

	stub := newLiveGatewayStub(`{"event":"message.delta","run_id":"run_test","timestamp":1.0,"delta":"hi"}`)
	t.Cleanup(func() { closeLiveGatewayStub(t, stub) })
	c, _ := NewClient(stub.URL, "k")
	svc := NewServiceWithState(c, stateFile)
	// LIFO: this runs BEFORE t.TempDir's RemoveAll, which is exactly the
	// ordering the callers in this file rely on.
	t.Cleanup(svc.Close)

	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}
	// Synchronise on "the observer is live and parked in the body read" — the
	// first event reached the record, which is what makes teardown a race.
	waitForStatus(t, svc, "run_test", "running")

	// Premise: the legitimate persist path really wrote the file, otherwise
	// "nothing changed" below would prove nothing.
	raw, err := os.ReadFile(stateFile)
	if err != nil || !strings.Contains(string(raw), `"status":"running"`) {
		t.Fatalf("premise broken: state file not written before Close (err=%v body=%q)", err, raw)
	}

	before := snapshotDir(t, dir)

	svc.Close()

	afterClose := snapshotDir(t, dir)
	// Close must be idempotent: the second call returns without panicking.
	svc.Close()

	settled := snapshotDir(t, dir)

	assertSameDir(t, "as Close returned", before, afterClose)
	assertSameDir(t, "after the second Close returned", before, settled)
	for _, e := range settled {
		if strings.Contains(e.name, ".tmp") {
			t.Fatalf("temp state file left behind after Close: %s", e.String())
		}
	}
}

// TestServiceCloseCancelsSlowSSEHandler is the short stress variant for
// CI-006. The stub holds the first event before its write for five seconds;
// Close must cancel that request instead of waiting for the client transport's
// 30-second timeout or for the delayed handler to finish.
func TestServiceCloseCancelsSlowSSEHandler(t *testing.T) {
	stub := newLiveGatewayStubWithDelay(`{"event":"message.delta","run_id":"run_test","timestamp":1.0,"delta":"hi"}`, 5*time.Second)
	t.Cleanup(func() { closeLiveGatewayStub(t, stub) })
	c, _ := NewClient(stub.URL, "k")
	svc := NewService(c)
	t.Cleanup(svc.Close)

	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stub.eventStarted:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not start within 1s")
	}

	started := time.Now()
	svc.Close()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Close waited %s for a cancelled slow SSE handler", elapsed)
	}
}

// TestServiceCloseIdempotentWithoutStatePath pins the two shapes every test
// cleanup relies on: Close on a service with persistence DISABLED, and Close
// called more than once (including on a service that never started a run).
func TestServiceCloseIdempotentWithoutStatePath(t *testing.T) {
	stub := newLiveGatewayStub(`{"event":"message.delta","run_id":"run_test","timestamp":1.0,"delta":"hi"}`)
	t.Cleanup(func() { closeLiveGatewayStub(t, stub) })
	c, _ := NewClient(stub.URL, "k")

	svc := NewService(c) // no state path at all
	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, svc, "run_test", "running")
	svc.Close()
	svc.Close()

	// Never started a run, and statePath explicitly empty.
	idle := NewService(c)
	idle.Close()
	idle.Close()

	stateless := NewServiceWithState(c, "")
	stateless.Close()
	stateless.Close()
}

// TestServicePersistAfterCloseIsNoOp pins the half that cancellation alone
// cannot cover: observe's disconnect path persists AFTER the context is
// already cancelled, so the closed flag — not the context — is what stops the
// write. Both drivers below change the record in memory (asserted: the path
// really ran) and must leave the file untouched.
func TestServicePersistAfterCloseIsNoOp(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "runs.jsonl")

	stub := newLiveGatewayStub(`{"event":"message.delta","run_id":"run_test","timestamp":1.0,"delta":"hi"}`)
	t.Cleanup(func() { closeLiveGatewayStub(t, stub) })
	c, _ := NewClient(stub.URL, "k")
	svc := NewServiceWithState(c, stateFile)
	t.Cleanup(svc.Close)

	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, svc, "run_test", "running")
	svc.Close()

	before := snapshotDir(t, dir)

	// Driver 1: the normal UI path for stopping a live run. It forwards to the
	// gateway and then persists the "stopping" transition.
	if err := svc.StopRun(context.Background(), "run_test"); err != nil {
		t.Fatalf("stop after close: %v", err)
	}
	if stub.stopped.Load() != 1 {
		t.Fatalf("premise broken: stop was not forwarded to the gateway")
	}
	if rec, _ := svc.Run("run_test"); rec.Status != "stopping" {
		t.Fatalf("premise broken: StopRun did not update the record (status=%q)", rec.Status)
	}

	// Driver 2: a terminal event applied straight to the record.
	svc.noteEvent("run_test", RunEvent{Event: "run.completed", RunID: "run_test", Output: "done", Timestamp: 2})
	if rec, _ := svc.Run("run_test"); rec.Status != "completed" {
		t.Fatalf("premise broken: noteEvent did not update the record (status=%q)", rec.Status)
	}

	assertSameDir(t, "after post-Close persist attempts", before, snapshotDir(t, dir))
}

// TestServiceCloseConcurrentWithPersistIsRaceFree drives Close against every
// path that writes the registry — StartRun's spawn+persist, StopRun, noteEvent
// — so the race detector sees the lifecycle fields under the contention
// production shutdown actually has: a service being closed while runs are
// still arriving and finishing. The WaitGroup contract is the point: a
// goroutine may only register while mu is held and the service is not closed,
// so an Add can never race the Wait inside Close.
func TestServiceCloseConcurrentWithPersistIsRaceFree(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "runs.jsonl")

	stub := newLiveGatewayStub(`{"event":"message.delta","run_id":"run_test","timestamp":1.0,"delta":"hi"}`)
	t.Cleanup(func() { closeLiveGatewayStub(t, stub) })
	c, _ := NewClient(stub.URL, "k")
	svc := NewServiceWithState(c, stateFile)
	t.Cleanup(svc.Close)

	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, _ = svc.StartRun(context.Background(), "hello", "")
			svc.noteEvent("run_test", RunEvent{Event: "run.completed", RunID: "run_test", Output: "done"})
			_ = svc.StopRun(context.Background(), "run_test")
		}()
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		svc.Close()
	}()

	workers.Wait()
	<-closed
	svc.Close() // idempotent, now under real contention history

	// Whatever the interleaving, the service is quiescent: a last write
	// attempt after everything settled must still be a no-op.
	after := snapshotDir(t, dir)
	svc.noteEvent("run_test", RunEvent{Event: "run.failed", RunID: "run_test", Error: "late"})
	assertSameDir(t, "after a post-Close write attempt", after, snapshotDir(t, dir))
}

// TestStartRunInputModelForwarded proves the plumbing half of GAP-080 phase 2a:
// a non-empty StartRunInput.Model reaches the gateway's POST /v1/runs body
// verbatim, and an empty one leaves the key out of that body entirely
// (omitempty) — so a context-free run's request is byte-identical to before.
func TestStartRunInputModelForwarded(t *testing.T) {
	ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"run_id":"run_model","status":"started"}`))
			return true
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte(": stream closed\n\n"))
			return true
		}
		return false
	})
	defer ts.Close()

	c, err := NewClient(ts.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(c)
	t.Cleanup(svc.Close)

	if _, err := svc.StartRunWithContext(context.Background(), StartRunInput{
		Message: "hello", Input: "hello", Model: "deepseek/deepseek-chat",
	}); err != nil {
		t.Fatal(err)
	}
	// The model-less run: exactly today's shape.
	if _, err := svc.StartRunWithContext(context.Background(), StartRunInput{
		Message: "hello", Input: "hello",
	}); err != nil {
		t.Fatal(err)
	}

	recs := ts.requestsFor(http.MethodPost, "/v1/runs")
	if len(recs) != 2 {
		t.Fatalf("want 2 POST /v1/runs, got %d", len(recs))
	}
	var withModel map[string]any
	if err := json.Unmarshal([]byte(recs[0].body), &withModel); err != nil {
		t.Fatalf("first body not decodable (%v): %s", err, recs[0].body)
	}
	if withModel["model"] != "deepseek/deepseek-chat" {
		t.Fatalf("model not forwarded to the gateway: %v", withModel)
	}
	var withoutModel map[string]any
	if err := json.Unmarshal([]byte(recs[1].body), &withoutModel); err != nil {
		t.Fatalf("second body not decodable (%v): %s", err, recs[1].body)
	}
	if _, present := withoutModel["model"]; present {
		t.Fatalf("an empty model must stay out of the request body: %v", withoutModel)
	}
}
