package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// capturingGatewayStub records every POST /v1/runs body so a test can assert
// what the model call ACTUALLY carries (GAP-075). The service-level
// gatewayStub in service_test.go only counts starts.
type capturingGatewayStub struct {
	*httptest.Server

	mu     sync.Mutex
	bodies []map[string]any
}

func newCapturingGatewayStub() *capturingGatewayStub {
	g := &capturingGatewayStub{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"message":"bad body"}}`)
				return
			}
			g.mu.Lock()
			g.bodies = append(g.bodies, body)
			g.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"run_id":"run_ctx","status":"started"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": stream closed\n\n")
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/"):
			fmt.Fprint(w, `{"status":"running","last_event":"message.delta"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"message":"not found"}}`)
		}
	}))
	return g
}

// started returns a copy of the captured POST /v1/runs bodies.
func (g *capturingGatewayStub) started() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]map[string]any, len(g.bodies))
	copy(out, g.bodies)
	return out
}

// TestStartRunWithContextSendsCompiledInput proves the carrier: the gateway
// receives the COMPILED content, while the record keeps the raw message.
func TestStartRunWithContextSendsCompiledInput(t *testing.T) {
	stub := newCapturingGatewayStub()
	defer stub.Close()
	c, err := NewClient(stub.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(c)

	manifest := json.RawMessage(`{"requestId":"req-1","tokensUsed":42,"tokenBudget":8000}`)
	rec, err := svc.StartRunWithContext(context.Background(), StartRunInput{
		Message:       "hello",
		Input:         "COMPILED:hello",
		SessionID:     "sess-1",
		SourceNodeID:  "node-1",
		TokenBudget:   8000,
		ContextTokens: 42,
		ManifestJSON:  manifest,
	})
	if err != nil {
		t.Fatal(err)
	}

	bodies := stub.started()
	if len(bodies) != 1 {
		t.Fatalf("want 1 start request on the gateway, got %d", len(bodies))
	}
	if got := bodies[0]["input"]; got != "COMPILED:hello" {
		t.Fatalf("gateway input = %v, want the compiled content", got)
	}
	if got := bodies[0]["session_id"]; got != "sess-1" {
		t.Fatalf("gateway session_id = %v", got)
	}
	for _, key := range []string{"instructions", "model"} {
		if _, ok := bodies[0][key]; ok {
			t.Fatalf("unset StartRunRequest field %q must be omitted, body=%v", key, bodies[0])
		}
	}

	if rec.Message != "hello" {
		t.Fatalf("record message = %q, want the raw message", rec.Message)
	}
	if rec.SourceNodeID != "node-1" || rec.TokenBudget != 8000 || rec.ContextTokens != 42 {
		t.Fatalf("context provenance not recorded: %+v", rec)
	}
	if string(rec.Manifest) != string(manifest) {
		t.Fatalf("manifest = %s, want %s", rec.Manifest, manifest)
	}
}

// TestStartRunLegacyPathUnchanged proves AC3 at the service boundary: the
// one-line delegate keeps the pre-GAP-075 behaviour byte-for-byte — raw
// message as input, and a record whose JSON carries none of the new keys.
func TestStartRunLegacyPathUnchanged(t *testing.T) {
	stub := newCapturingGatewayStub()
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewService(c)

	rec, err := svc.StartRun(context.Background(), "raw text", "sess-2")
	if err != nil {
		t.Fatal(err)
	}
	bodies := stub.started()
	if len(bodies) != 1 || bodies[0]["input"] != "raw text" {
		t.Fatalf("legacy StartRun must send the raw message, got %v", bodies)
	}

	// Input ="" must fall back to Message through StartRunWithContext too.
	if _, err := svc.StartRunWithContext(context.Background(), StartRunInput{Message: "fallback"}); err != nil {
		t.Fatal(err)
	}
	bodies = stub.started()
	if len(bodies) != 2 || bodies[1]["input"] != "fallback" {
		t.Fatalf("Input=\"\" must fall back to Message, got %v", bodies)
	}

	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source_node_id", "token_budget", "context_tokens", "manifest"} {
		if _, ok := decoded[key]; ok {
			t.Fatalf("context-free record must omit %q: %s", key, raw)
		}
	}
}

// TestStartRunContextFieldsPersistRoundTrip proves AC6: a record carrying a
// manifest survives persist → load (Backfill path) with the bytes intact.
func TestStartRunContextFieldsPersistRoundTrip(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "runs.jsonl")
	stub := newCapturingGatewayStub()
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewServiceWithState(c, stateFile)

	manifest := json.RawMessage(`{"requestId":"req-rt","tokensUsed":123,"warnings":["5+ references"]}`)
	if _, err := svc.StartRunWithContext(context.Background(), StartRunInput{
		Message:       "hello",
		Input:         "COMPILED:hello",
		SourceNodeID:  "node-rt",
		TokenBudget:   4000,
		ContextTokens: 123,
		ManifestJSON:  manifest,
	}); err != nil {
		t.Fatal(err)
	}

	// Restart-style: a fresh service on the same state file.
	c2, _ := NewClient(stub.URL, "k")
	svc2 := NewServiceWithState(c2, stateFile)
	rec, ok := svc2.Run("run_ctx")
	if !ok {
		t.Fatal("run lost across restart")
	}
	if string(rec.Manifest) != string(manifest) {
		t.Fatalf("manifest after reload = %s, want %s", rec.Manifest, manifest)
	}
	if rec.SourceNodeID != "node-rt" || rec.TokenBudget != 4000 || rec.ContextTokens != 123 {
		t.Fatalf("context fields lost across reload: %+v", rec)
	}
	if rec.Message != "hello" {
		t.Fatalf("raw message lost across reload: %q", rec.Message)
	}
}

// TestStartRunEmptyManifestSurvivesPersist pins the manifest boundary
// invariant: a record is never left holding a NON-NIL zero-length
// json.RawMessage (invalid JSON for any consumer), and such a run still
// round-trips the persisted registry instead of being dropped.
func TestStartRunEmptyManifestSurvivesPersist(t *testing.T) {
	// Premise: a zero-length RawMessage really is JSON poison for a field
	// that is actually encoded. (RunRecord's manifest tag is omitempty, which
	// excludes an empty slice before any marshal can fail — hence the
	// normalisation to nil, which makes the invariant hold for the in-memory
	// record too, not just for the wire.)
	type plainManifest struct {
		Manifest json.RawMessage `json:"manifest"`
	}
	if _, err := json.Marshal(plainManifest{Manifest: json.RawMessage("")}); err == nil {
		t.Fatal("premise broken: an encoded zero-length RawMessage marshalled cleanly")
	}

	stateFile := filepath.Join(t.TempDir(), "runs.jsonl")
	stub := newCapturingGatewayStub()
	defer stub.Close()
	c, _ := NewClient(stub.URL, "k")
	svc := NewServiceWithState(c, stateFile)

	rec, err := svc.StartRunWithContext(context.Background(), StartRunInput{
		Message:      "hello",
		ManifestJSON: json.RawMessage(""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Manifest != nil {
		t.Fatalf("empty manifest must normalise to nil, got %q", rec.Manifest)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("record with an empty manifest must still marshal: %v", err)
	}
	if strings.Contains(string(raw), "manifest") {
		t.Fatalf("context-free record must omit the manifest key: %s", raw)
	}
	c2, _ := NewClient(stub.URL, "k")
	svc2 := NewServiceWithState(c2, stateFile)
	if _, ok := svc2.Run("run_ctx"); !ok {
		t.Fatal("record was dropped from the persisted registry")
	}
}
