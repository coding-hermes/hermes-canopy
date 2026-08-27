package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
}

func newGatewayStub(events []string) *gatewayStub {
	g := &gatewayStub{events: events}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			g.started.Add(1)
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"run_id":"run_test","status":"started"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_test":
			fmt.Fprint(w, `{"status":"running","last_event":"message.delta"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_test/stop":
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
