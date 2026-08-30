package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestServiceStartRunGatewayDown(t *testing.T) {
	// Point at a closed port: connection refused.
	c, _ := NewClient("http://127.0.0.1:1", "k")
	svc := NewService(c)
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
