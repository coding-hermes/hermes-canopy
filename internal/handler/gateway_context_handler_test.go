package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/gateway"
)

// capturingCompiler is a ContextCompiler that records every compile request
// and answers with a scripted result, so a test can assert BOTH what the
// compiler was asked for and what the model then received. (stubCompiler in
// context_handler_test.go scripts a result without recording.)
type capturingCompiler struct {
	mu       sync.Mutex
	requests []ctxpkg.CompileRequest
	content  string
	manifest *ctxpkg.Manifest
	err      error
}

func (c *capturingCompiler) Compile(_ context.Context, req ctxpkg.CompileRequest) (*ctxpkg.CompiledContext, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	return &ctxpkg.CompiledContext{Content: c.content, Manifest: c.manifest}, nil
}

func (c *capturingCompiler) calls() []ctxpkg.CompileRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ctxpkg.CompileRequest, len(c.requests))
	copy(out, c.requests)
	return out
}

// contextGatewayStub simulates the Hermes gateway api_server and records every
// POST /v1/runs body (the handler-level gatewayStub only counts stops).
type contextGatewayStub struct {
	*httptest.Server

	mu     sync.Mutex
	bodies []map[string]any
}

func newContextGatewayStub() *contextGatewayStub {
	g := &contextGatewayStub{}
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

func (g *contextGatewayStub) started() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]map[string]any, len(g.bodies))
	copy(out, g.bodies)
	return out
}

func newContextGatewayRouter(t *testing.T, stub *contextGatewayStub, opts ...GatewayHandlerOption) (chi.Router, *gateway.Service) {
	t.Helper()
	c, err := gateway.NewClient(stub.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	svc := gateway.NewService(c)
	r := chi.NewRouter()
	r.Mount("/", NewGatewayHandler(svc, opts...).Routes())
	return r, svc
}

// assertRunProvenance asserts the audit fields a compiled run must carry.
func assertRunProvenance(t *testing.T, rec gateway.RunRecord, wantNode string, wantManifest *ctxpkg.Manifest, wantBudget int) {
	t.Helper()
	if rec.SourceNodeID != wantNode {
		t.Fatalf("source_node_id = %q, want %q", rec.SourceNodeID, wantNode)
	}
	if rec.TokenBudget != wantBudget {
		t.Fatalf("token_budget = %d, want %d", rec.TokenBudget, wantBudget)
	}
	if rec.ContextTokens != wantManifest.TokensUsed {
		t.Fatalf("context_tokens = %d, want %d", rec.ContextTokens, wantManifest.TokensUsed)
	}
	if len(rec.Manifest) == 0 {
		t.Fatal("manifest missing from the run record")
	}
	var got ctxpkg.Manifest
	if err := json.Unmarshal(rec.Manifest, &got); err != nil {
		t.Fatalf("manifest not decodable (%v): %s", err, rec.Manifest)
	}
	if !reflect.DeepEqual(&got, wantManifest) {
		t.Fatalf("manifest deep-equal failed:\n got %+v\nwant %+v", &got, wantManifest)
	}
}

func decodeRunEnvelope(t *testing.T, body string) gateway.RunRecord {
	t.Helper()
	var env struct {
		RunID  string            `json:"run_id"`
		Status string            `json:"status"`
		Run    gateway.RunRecord `json:"run"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("start response not decodable (%v): %s", err, body)
	}
	return env.Run
}

func listRunCount(t *testing.T, r chi.Router) int {
	t.Helper()
	resp, body := gwDoJSON(t, r, http.MethodGet, "/runs", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list runs: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Runs []gateway.RunRecord `json:"runs"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return len(out.Runs)
}

func decodeAPIError(t *testing.T, body string) apiErrorBody {
	t.Helper()
	var e apiErrorBody
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("error body not decodable (%v): %s", err, body)
	}
	return e
}

// TestGatewayContextRunSendsCompiledInput proves AC1 + AC2: with a node_id the
// gateway receives the COMPILED content (not the raw message) and the manifest
// is retrievable from GET /gateway/runs/{run_id}.
func TestGatewayContextRunSendsCompiledInput(t *testing.T) {
	stub := newContextGatewayStub()
	defer stub.Close()
	nodeID := uuid.New()
	manifest := &ctxpkg.Manifest{
		RequestID:   "req-1",
		NodeID:      nodeID,
		CompiledAt:  time.Now().UTC(),
		TokenBudget: 8000,
		TokensUsed:  42,
	}
	comp := &capturingCompiler{content: "COMPILED:hello", manifest: manifest}
	r, _ := newContextGatewayRouter(t, stub, WithContextCompiler(comp, 8000))

	resp, body := gwDoJSON(t, r, http.MethodPost, "/runs",
		fmt.Sprintf(`{"message":"hello","node_id":%q}`, nodeID.String()))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}

	// AC1: the model call carries the compiled payload.
	bodies := stub.started()
	if len(bodies) != 1 {
		t.Fatalf("want 1 gateway start request, got %d", len(bodies))
	}
	if got := bodies[0]["input"]; got != "COMPILED:hello" {
		t.Fatalf("gateway input = %v, want the compiled content", got)
	}
	if bodies[0]["input"] == "hello" {
		t.Fatal("gateway received the RAW message: the compiler output is not wired")
	}

	// The compiler was asked for exactly the documented shape.
	reqs := comp.calls()
	if len(reqs) != 1 {
		t.Fatalf("want 1 compile call, got %d", len(reqs))
	}
	got := reqs[0]
	if got.NodeID != nodeID {
		t.Fatalf("compile node = %s, want %s", got.NodeID, nodeID)
	}
	if got.TokenBudget != 8000 {
		t.Fatalf("compile budget = %d, want the handler default 8000", got.TokenBudget)
	}
	if got.MaxAncestors != 0 || got.IncludeCards || !got.ResolveRefs {
		t.Fatalf("unexpected compile flags: %+v", got)
	}
	if got.MultiReference != nil {
		t.Fatalf("multi-reference selection must be nil: %+v", got.MultiReference)
	}
	if got.TreeID != uuid.Nil {
		t.Fatalf("TreeID must stay zero (the manifest exposes no tree id): %s", got.TreeID)
	}

	// The 202 body already carries the provenance.
	assertRunProvenance(t, decodeRunEnvelope(t, body), nodeID.String(), manifest, 8000)

	// AC2: GET /gateway/runs/{run_id} exposes the manifest.
	resp, recBody := gwDoJSON(t, r, http.MethodGet, "/runs/run_ctx", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get run: %d %s", resp.StatusCode, recBody)
	}
	var rec gateway.RunRecord
	if err := json.Unmarshal([]byte(recBody), &rec); err != nil {
		t.Fatal(err)
	}
	assertRunProvenance(t, rec, nodeID.String(), manifest, 8000)
	if rec.Message != "hello" {
		t.Fatalf("run record must keep the raw message, got %q", rec.Message)
	}
}

// TestGatewayContextRunTokenBudgetOverride proves token_budget overrides the
// handler default for both the compile request and the recorded provenance.
func TestGatewayContextRunTokenBudgetOverride(t *testing.T) {
	stub := newContextGatewayStub()
	defer stub.Close()
	nodeID := uuid.New()
	manifest := &ctxpkg.Manifest{RequestID: "req-2", NodeID: nodeID, CompiledAt: time.Now().UTC(), TokenBudget: 1234, TokensUsed: 7}
	comp := &capturingCompiler{content: "COMPILED", manifest: manifest}
	r, _ := newContextGatewayRouter(t, stub, WithContextCompiler(comp, 8000))

	resp, body := gwDoJSON(t, r, http.MethodPost, "/runs",
		fmt.Sprintf(`{"message":"hello","node_id":%q,"token_budget":1234}`, nodeID.String()))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	reqs := comp.calls()
	if len(reqs) != 1 || reqs[0].TokenBudget != 1234 {
		t.Fatalf("compile budget = %+v, want 1234", reqs)
	}
	assertRunProvenance(t, decodeRunEnvelope(t, body), nodeID.String(), manifest, 1234)
}

// TestGatewayContextRunLegacyUnchanged proves AC3 through the HTTP surface: no
// node_id => raw message to the gateway, no compile call, and no context keys
// on the run object (the legacy JSON shape).
func TestGatewayContextRunLegacyUnchanged(t *testing.T) {
	stub := newContextGatewayStub()
	defer stub.Close()
	comp := &capturingCompiler{content: "COMPILED:hello", manifest: &ctxpkg.Manifest{RequestID: "unused"}}
	r, _ := newContextGatewayRouter(t, stub, WithContextCompiler(comp, 8000))

	resp, body := gwDoJSON(t, r, http.MethodPost, "/runs", `{"message":"hello","session_id":"s1"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	bodies := stub.started()
	if len(bodies) != 1 || bodies[0]["input"] != "hello" {
		t.Fatalf("legacy start must send the raw message, got %v", bodies)
	}
	if len(comp.calls()) != 0 {
		t.Fatalf("compiler must not run without a node_id, got %d calls", len(comp.calls()))
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	runObj, ok := decoded["run"].(map[string]any)
	if !ok {
		t.Fatalf("no run object in %s", body)
	}
	for _, key := range []string{"source_node_id", "token_budget", "context_tokens", "manifest"} {
		if _, present := runObj[key]; present {
			t.Fatalf("context-free run must not carry %q: %s", key, body)
		}
	}
}

// TestGatewayContextRunMalformedNodeID proves a non-UUID node_id fails before
// any compiler or gateway call.
func TestGatewayContextRunMalformedNodeID(t *testing.T) {
	stub := newContextGatewayStub()
	defer stub.Close()
	comp := &capturingCompiler{content: "COMPILED", manifest: &ctxpkg.Manifest{RequestID: "unused"}}
	r, _ := newContextGatewayRouter(t, stub, WithContextCompiler(comp, 8000))

	resp, body := gwDoJSON(t, r, http.MethodPost, "/runs", `{"message":"hello","node_id":"not-a-uuid"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed node_id: %d %s", resp.StatusCode, body)
	}
	e := decodeAPIError(t, body)
	if e.Error.Code != "invalid_request" {
		t.Fatalf("code = %q, want invalid_request: %s", e.Error.Code, body)
	}
	if !strings.Contains(e.Error.Message, "node_id") {
		t.Fatalf("error must name node_id: %s", body)
	}
	if len(comp.calls()) != 0 || len(stub.started()) != 0 {
		t.Fatalf("no compile or gateway call is allowed: compile=%d gateway=%d", len(comp.calls()), len(stub.started()))
	}
}

// TestGatewayContextRunCompileFailures proves AC4: the two failure classes are
// distinguished and NEITHER falls back to the raw message or touches the
// gateway, and no run enters the registry.
func TestGatewayContextRunCompileFailures(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"node_not_found", ctxpkg.ErrNodeNotFound, http.StatusNotFound, "node_not_found"},
		{"other", errors.New("compiler exploded"), http.StatusUnprocessableEntity, "context_compile_failed"},
		{"db_unavailable_is_not_a_fallback", ctxpkg.ErrDatabaseUnavailable, http.StatusUnprocessableEntity, "context_compile_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newContextGatewayStub()
			defer stub.Close()
			nodeID := uuid.New()
			comp := &capturingCompiler{err: tc.err}
			r, _ := newContextGatewayRouter(t, stub, WithContextCompiler(comp, 8000))

			resp, body := gwDoJSON(t, r, http.MethodPost, "/runs",
				fmt.Sprintf(`{"message":"hello","node_id":%q}`, nodeID.String()))

			// The load-bearing claim: a failed compile never falls back to
			// the raw message, so the gateway is never called at all.
			if started := stub.started(); len(started) != 0 {
				t.Fatalf("a failed compile must never reach the gateway: %v", started)
			}
			if n := listRunCount(t, r); n != 0 {
				t.Fatalf("registry gained %d runs on a failed compile", n)
			}
			if resp, _ := gwDoJSON(t, r, http.MethodGet, "/runs/run_ctx", ""); resp.StatusCode != http.StatusNotFound {
				t.Fatalf("no run record may exist: %d", resp.StatusCode)
			}
			if len(comp.calls()) != 1 {
				t.Fatalf("want 1 compile attempt, got %d", len(comp.calls()))
			}
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.wantStatus, body)
			}
			if got := decodeAPIError(t, body).Error; got.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q: %s", got.Code, tc.wantCode, body)
			}
		})
	}
}

// TestGatewayContextRunCompilerUnavailable proves AC5: a node-scoped request
// against a handler with no compiler fails closed instead of sending raw text.
func TestGatewayContextRunCompilerUnavailable(t *testing.T) {
	stub := newContextGatewayStub()
	defer stub.Close()
	nodeID := uuid.New()
	r, _ := newContextGatewayRouter(t, stub) // no WithContextCompiler

	resp, body := gwDoJSON(t, r, http.MethodPost, "/runs",
		fmt.Sprintf(`{"message":"hello","node_id":%q}`, nodeID.String()))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", resp.StatusCode, body)
	}
	if got := decodeAPIError(t, body).Error.Code; got != "context_compiler_unavailable" {
		t.Fatalf("code = %q: %s", got, body)
	}
	if len(stub.started()) != 0 {
		t.Fatalf("no gateway call allowed without a compiler: %v", stub.started())
	}
	if n := listRunCount(t, r); n != 0 {
		t.Fatalf("registry gained %d runs", n)
	}
}
