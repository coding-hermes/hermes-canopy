package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/coding-hermes/hermes-canopy/internal/gateway"
)

// gatewayStub simulates the Hermes gateway api_server for handler tests.
type gatewayStub struct {
	*httptest.Server
	events []string
}

func newGatewayStub(events []string) *gatewayStub {
	g := &gatewayStub{events: events}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"run_id":"run_test","status":"started"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_test":
			fmt.Fprint(w, `{"status":"running","last_event":"message.delta"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_test/stop":
			fmt.Fprint(w, `{"run_id":"run_test","status":"stopping"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_test/approval":
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

func newGatewayTestRouter(stub *gatewayStub) (chi.Router, *gateway.Service) {
	c, _ := gateway.NewClient(stub.URL, "k")
	svc := gateway.NewService(c)
	r := chi.NewRouter()
	r.Mount("/", NewGatewayHandler(svc).Routes())
	return r, svc
}

func gwDoJSON(t *testing.T, r chi.Router, method, path, body string) (*http.Response, string) {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr.Result(), rr.Body.String()
}

func TestGatewayStatusOffline(t *testing.T) {
	// Gateway down: status must report connected=false, not error out.
	c, _ := gateway.NewClient("http://127.0.0.1:1", "k")
	svc := gateway.NewService(c)
	r := chi.NewRouter()
	r.Mount("/", NewGatewayHandler(svc).Routes())

	resp, body := gwDoJSON(t, r, http.MethodGet, "/status", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status should be 200 even when gateway is down, got %d: %s", resp.StatusCode, body)
	}
	var st statusResponse
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if st.Connected {
		t.Fatalf("connected should be false: %s", body)
	}
	if st.Error == "" {
		t.Fatal("expected an error string when gateway is down")
	}
}

func TestGatewayLifecycleEndpoints(t *testing.T) {
	stub := newGatewayStub([]string{
		`{"event":"message.delta","run_id":"run_test","timestamp":1.0,"delta":"hi"}`,
		`{"event":"run.completed","run_id":"run_test","timestamp":2.0,"output":"done"}`,
	})
	defer stub.Close()
	r, _ := newGatewayTestRouter(stub)

	// status
	resp, body := gwDoJSON(t, r, http.MethodGet, "/status", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"connected":true`) {
		t.Fatalf("expected connected:true: %s", body)
	}

	// start run
	resp, body = gwDoJSON(t, r, http.MethodPost, "/runs", `{"message":"hello","session_id":"s1"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"run_id":"run_test"`) {
		t.Fatalf("start body: %s", body)
	}

	// wait for events to land
	deadline := time.Now().Add(3 * time.Second)
	var recBody string
	for {
		resp, recBody = gwDoJSON(t, r, http.MethodGet, "/runs/run_test", "")
		if resp.StatusCode == http.StatusOK && strings.Contains(recBody, `"completed"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never completed: %s", recBody)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(recBody, `"output":"done"`) {
		t.Fatalf("output missing: %s", recBody)
	}

	// list
	resp, body = gwDoJSON(t, r, http.MethodGet, "/runs", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"run_id":"run_test"`) {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}

	// unknown run
	resp, _ = gwDoJSON(t, r, http.MethodGet, "/runs/nope", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run should 404, got %d", resp.StatusCode)
	}
}

func TestGatewayStartRunValidation(t *testing.T) {
	stub := newGatewayStub(nil)
	defer stub.Close()
	r, _ := newGatewayTestRouter(stub)

	resp, body := gwDoJSON(t, r, http.MethodPost, "/runs", `{}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty message should 400, got %d: %s", resp.StatusCode, body)
	}
	resp, _ = gwDoJSON(t, r, http.MethodPost, "/runs", `{"message":"  "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank message should 400, got %d", resp.StatusCode)
	}
}

func TestGatewayStopAndApproval(t *testing.T) {
	stub := newGatewayStub(nil)
	defer stub.Close()
	r, svc := newGatewayTestRouter(stub)

	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}

	resp, body := gwDoJSON(t, r, http.MethodPost, "/runs/run_test/stop", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"stopping"`) {
		t.Fatalf("stop: %d %s", resp.StatusCode, body)
	}

	resp, body = gwDoJSON(t, r, http.MethodPost, "/runs/run_test/approval", `{"choice":"once","approval_id":"appr-9"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"resolved":true`) {
		t.Fatalf("approval: %d %s", resp.StatusCode, body)
	}

	resp, body = gwDoJSON(t, r, http.MethodPost, "/runs/run_test/approval", `{"choice":"maybe"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad choice should 400, got %d: %s", resp.StatusCode, body)
	}

	resp, _ = gwDoJSON(t, r, http.MethodPost, "/runs/ghost/stop", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run stop should 404, got %d", resp.StatusCode)
	}
}

func TestGatewayRunEventsSSE(t *testing.T) {
	stub := newGatewayStub([]string{
		`{"event":"message.delta","run_id":"run_test","timestamp":1.0,"delta":"streaming"}`,
		`{"event":"run.completed","run_id":"run_test","timestamp":2.0,"output":"final"}`,
	})
	defer stub.Close()
	r, svc := newGatewayTestRouter(stub)
	if _, err := svc.StartRun(context.Background(), "hello", ""); err != nil {
		t.Fatal(err)
	}
	// Let the observer consume the stream.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if rec, ok := svc.Run("run_test"); ok && rec.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("observer never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}

	req := httptest.NewRequest(http.MethodGet, "/runs/run_test/events", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("events: %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type: %q", ct)
	}

	var deltas, completed int
	sc := bufio.NewScanner(strings.NewReader(rr.Body.String()))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev gateway.RunEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("bad SSE payload: %v", err)
		}
		switch ev.Event {
		case "message.delta":
			deltas++
			if ev.Delta != "streaming" {
				t.Fatalf("delta payload: %+v", ev)
			}
		case "run.completed":
			completed++
			if ev.Output != "final" {
				t.Fatalf("completed payload: %+v", ev)
			}
		}
	}
	if deltas != 1 || completed != 1 {
		t.Fatalf("want 1 delta + 1 completed in SSE replay, got %d/%d", deltas, completed)
	}
}
