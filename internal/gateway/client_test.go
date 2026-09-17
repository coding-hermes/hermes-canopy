package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// testServer builds an httptest server that records requests and serves
// canned responses per path+method.
type testServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []requestRecord
	handler  func(w http.ResponseWriter, r *http.Request) (handled bool)
}

type requestRecord struct {
	method string
	path   string
	auth   string
	body   string
}

func newTestServer(handler func(w http.ResponseWriter, r *http.Request) bool) *testServer {
	ts := &testServer{handler: handler}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts.mu.Lock()
		ts.requests = append(ts.requests, requestRecord{
			method: r.Method,
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
			body:   string(body),
		})
		ts.mu.Unlock()
		if ts.handler != nil && ts.handler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	return ts
}

func (ts *testServer) requestsFor(method, path string) []requestRecord {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	var out []requestRecord
	for _, r := range ts.requests {
		if r.method == method && r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func TestNewClientValidatesBaseURL(t *testing.T) {
	if _, err := NewClient("", "k"); err != nil {
		t.Fatalf("empty base_url should default: %v", err)
	}
	if _, err := NewClient("file:///etc/passwd", "k"); err == nil {
		t.Fatal("non-http scheme should be rejected")
	}
	if _, err := NewClient("ftp://x", "k"); err == nil {
		t.Fatal("ftp scheme should be rejected")
	}
	c, err := NewClient("http://127.0.0.1:8642/", "k")
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL() != "http://127.0.0.1:8642" {
		t.Fatalf("trailing slash not trimmed: %q", c.BaseURL())
	}
}

func TestStartRunPostsInputAndBearer(t *testing.T) {
	ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"run_id":"run_abc","status":"started"}`))
		return true
	})
	defer ts.Close()

	c, err := NewClient(ts.URL, "sekret")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := c.StartRun(context.Background(), StartRunRequest{Input: "hello", SessionID: "s1", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if ref.RunID != "run_abc" || ref.Status != "started" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
	recs := ts.requestsFor("POST", "/v1/runs")
	if len(recs) != 1 {
		t.Fatalf("want 1 POST /v1/runs, got %d", len(recs))
	}
	if recs[0].auth != "Bearer sekret" {
		t.Fatalf("bad auth header %q", recs[0].auth)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(recs[0].body), &body); err != nil {
		t.Fatal(err)
	}
	if body["input"] != "hello" || body["session_id"] != "s1" || body["model"] != "m" {
		t.Fatalf("unexpected body: %v", body)
	}
}

func TestStartRunNon202ReturnsAPIError(t *testing.T) {
	ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
		return true
	})
	defer ts.Close()

	c, _ := NewClient(ts.URL, "bad")
	_, err := c.StartRun(context.Background(), StartRunRequest{Input: "x"})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401 in error, got %v", err)
	}
}

// TestListModels pins the GET /v1/models client added for GAP-080 phase 2a:
// auth is inherited from the client, the per-model context window is decoded,
// and all THREE response shapes are accepted — a bare array, an object wrapping
// the list under "models", and the OpenAI-style object wrapping it under
// "data" that the LIVE gateway api_server actually answers (pinned verbatim
// below). Anything else is an error — callers fall back from it.
func TestListModels(t *testing.T) {
	t.Run("bare array", func(t *testing.T) {
		ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/v1/models" {
				return false
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"big-model","context_length":200000},{"id":"small-model","context_length":4096}]`))
			return true
		})
		defer ts.Close()

		c, err := NewClient(ts.URL, "sekret")
		if err != nil {
			t.Fatal(err)
		}
		models, err := c.ListModels(context.Background())
		if err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if len(models) != 2 {
			t.Fatalf("want 2 models, got %d: %+v", len(models), models)
		}
		if models[0].ID != "big-model" || models[0].ContextLen != 200000 {
			t.Fatalf("first model = %+v", models[0])
		}
		if models[1].ID != "small-model" || models[1].ContextLen != 4096 {
			t.Fatalf("second model = %+v", models[1])
		}
		recs := ts.requestsFor(http.MethodGet, "/v1/models")
		if len(recs) != 1 {
			t.Fatalf("want 1 GET /v1/models, got %d", len(recs))
		}
		if recs[0].auth != "Bearer sekret" {
			t.Fatalf("model list must carry the client's auth header, got %q", recs[0].auth)
		}
	})

	t.Run("wrapped in a models field", func(t *testing.T) {
		ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"object":"list","models":[{"id":"big-model","context_length":200000}]}`))
			return true
		})
		defer ts.Close()

		c, _ := NewClient(ts.URL, "k")
		models, err := c.ListModels(context.Background())
		if err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if len(models) != 1 || models[0].ID != "big-model" || models[0].ContextLen != 200000 {
			t.Fatalf("wrapped shape decoded as %+v", models)
		}
	})

	t.Run("wrapped in an OpenAI data field", func(t *testing.T) {
		ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"object":"list","data":[{"id":"big-model","context_length":200000}]}`))
			return true
		})
		defer ts.Close()

		c, _ := NewClient(ts.URL, "k")
		models, err := c.ListModels(context.Background())
		if err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if len(models) != 1 || models[0].ID != "big-model" || models[0].ContextLen != 200000 {
			t.Fatalf("data envelope decoded as %+v", models)
		}
	})

	t.Run("data envelope carrying an empty list", func(t *testing.T) {
		ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"object":"list","data":[]}`))
			return true
		})
		defer ts.Close()

		c, _ := NewClient(ts.URL, "k")
		models, err := c.ListModels(context.Background())
		if err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if len(models) != 0 {
			t.Fatalf("empty data envelope decoded as %+v", models)
		}
	})

	// The exact bytes the deployed gateway api_server answers with
	// (GET http://127.0.0.1:8642/v1/models), verbatim: the list lives under
	// "data", every entry carries the OpenAI model fields, and NO entry carries
	// context_length — so the window is 0 and the budget must fall back rather
	// than be invented.
	t.Run("the live gateway payload", func(t *testing.T) {
		const livePayload = `{
    "object": "list",
    "data": [
        {
            "id": "Hermes Agent",
            "object": "model",
            "created": 1789682544,
            "owned_by": "hermes",
            "permission": [],
            "root": "Hermes Agent",
            "parent": null
        }
    ]
}`
		ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(livePayload))
			return true
		})
		defer ts.Close()

		c, _ := NewClient(ts.URL, "k")
		models, err := c.ListModels(context.Background())
		if err != nil {
			t.Fatalf("ListModels on the live payload: %v", err)
		}
		if len(models) != 1 {
			t.Fatalf("live payload decoded to %d models, want 1: %+v", len(models), models)
		}
		if models[0].ID != "Hermes Agent" {
			t.Fatalf("live payload model ID = %q, want %q", models[0].ID, "Hermes Agent")
		}
		if models[0].ContextLen != 0 {
			t.Fatalf("live payload reported a context window of %d; the gateway sends none", models[0].ContextLen)
		}
	})

	// Both keys present: "models" wins, so the outcome does not depend on map
	// iteration order.
	t.Run("both keys prefers models", func(t *testing.T) {
		ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"object":"list","models":[{"id":"from-models","context_length":8192}],"data":[{"id":"from-data","context_length":4096}]}`))
			return true
		})
		defer ts.Close()

		c, _ := NewClient(ts.URL, "k")
		models, err := c.ListModels(context.Background())
		if err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if len(models) != 1 || models[0].ID != "from-models" || models[0].ContextLen != 8192 {
			t.Fatalf("both-key object decoded as %+v, want the models key to win", models)
		}
	})

	t.Run("unusable shapes error", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			status int
			body   string
		}{
			{"non-2xx", http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`},
			{"object with neither models nor data", http.StatusOK, `{"object":"list","items":[]}`},
			{"not JSON at all", http.StatusOK, `<html>nope</html>`},
			{"models key present but not a list", http.StatusOK, `{"object":"list","models":"nope","data":[]}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					w.Write([]byte(tc.body))
					return true
				})
				defer ts.Close()
				c, _ := NewClient(ts.URL, "k")
				if _, err := c.ListModels(context.Background()); err == nil {
					t.Fatalf("want an error for %s", tc.name)
				}
			})
		}
	})
}

func TestGetRunMapsStatusAndNotFound(t *testing.T) {
	ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/runs/missing") {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"Run not found: missing","code":"run_not_found"}}`))
			return true
		}
		w.Write([]byte(`{"status":"completed","last_event":"run.completed","output":"hi","usage":{"total_tokens":12}}`))
		return true
	})
	defer ts.Close()

	c, _ := NewClient(ts.URL, "k")
	got, err := c.GetRun(context.Background(), "run_x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.RunID != "run_x" {
		t.Fatalf("unexpected: %+v", got)
	}
	missing, err := c.GetRun(context.Background(), "missing")
	if err != nil {
		t.Fatalf("404 should map to not_found status, got %v", err)
	}
	if missing.Status != "not_found" {
		t.Fatalf("want not_found, got %q", missing.Status)
	}
}

func TestStopRunAndRespondApproval(t *testing.T) {
	ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/runs/run_1/stop":
			w.Write([]byte(`{"run_id":"run_1","status":"stopping"}`))
		case "/v1/runs/run_1/approval":
			w.Write([]byte(`{"object":"hermes.run.approval_response","run_id":"run_1","choice":"once","resolved":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"nope"}}`))
		}
		return true
	})
	defer ts.Close()

	c, _ := NewClient(ts.URL, "k")
	ref, err := c.StopRun(context.Background(), "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Status != "stopping" {
		t.Fatalf("unexpected stop ref: %+v", ref)
	}
	if err := c.RespondApproval(context.Background(), "run_1", "appr-7", "once"); err != nil {
		t.Fatal(err)
	}
	stops := ts.requestsFor("POST", "/v1/runs/run_1/stop")
	if len(stops) != 1 {
		t.Fatalf("want 1 stop call, got %d", len(stops))
	}
	approvals := ts.requestsFor("POST", "/v1/runs/run_1/approval")
	if len(approvals) != 1 {
		t.Fatalf("want 1 approval call, got %d", len(approvals))
	}
	var ab map[string]any
	if err := json.Unmarshal([]byte(approvals[0].body), &ab); err != nil {
		t.Fatal(err)
	}
	if ab["choice"] != "once" || ab["approval_id"] != "appr-7" {
		t.Fatalf("unexpected approval body: %v", ab)
	}
}

func TestObserveRunOpensSSEStream(t *testing.T) {
	ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/v1/runs/run_9/events" {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(": keepalive\n\n"))
		w.Write([]byte(`data: {"event":"message.delta","run_id":"run_9","timestamp":1.5,"delta":"hi"}` + "\n\n"))
		return true
	})
	defer ts.Close()

	c, _ := NewClient(ts.URL, "k")
	body, err := c.ObserveRun(context.Background(), "run_9")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	raw, _ := io.ReadAll(body)
	if !strings.Contains(string(raw), "message.delta") {
		t.Fatalf("unexpected stream: %s", raw)
	}
}

func TestObserveRunError(t *testing.T) {
	ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"message":"Run not found: run_x","code":"run_not_found"}}`))
		return true
	})
	defer ts.Close()
	c, _ := NewClient(ts.URL, "k")
	if _, err := c.ObserveRun(context.Background(), "run_x"); err == nil {
		t.Fatal("want error for missing run")
	}
}

func TestHealth(t *testing.T) {
	ts := newTestServer(func(w http.ResponseWriter, r *http.Request) bool {
		w.Write([]byte(`{"status":"ok"}`))
		return true
	})
	defer ts.Close()
	c, _ := NewClient(ts.URL, "k")
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}
