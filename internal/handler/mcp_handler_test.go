package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// newMCPTestRouter mounts the MCP handler exactly as production does
// (internal/server/server.go: r.Mount("/mcp", mcpHandler.Routes()) inside the
// /api/v1 group) so the tests exercise the real mount point, not a
// hand-written path. Services are nil: initialize/ping/tools/list are
// DB-free and never dereference them.
func newMCPTestRouter() *chi.Mux {
	h := NewMCPHandler(nil, nil, nil, nil, nil, nil, nil)
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Mount("/mcp", h.Routes())
	})
	return r
}

// postMCP posts raw to the mounted MCP endpoint and returns the recorder.
func postMCP(t *testing.T, router http.Handler, path, raw string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// decodeRPCBody decodes a JSON-RPC response body, failing the test on invalid
// JSON. It deliberately decodes into a permissive map: a MISSING key and a
// null key must be distinguishable when asserting the wire shape.
func decodeRPCBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("response body is not a JSON object: %v (body=%q)", err, rr.Body.String())
	}
	return out
}

// TestMCPInitializeHandshake is AC1: `initialize` returns HTTP 200 with a
// negotiated protocolVersion, the tools capability, and serverInfo (AC1).
func TestMCPInitializeHandshake(t *testing.T) {
	router := newMCPTestRouter()

	tests := []struct {
		name      string
		requested string
		want      string
	}{
		{"newest supported echoed", "2025-06-18", "2025-06-18"},
		{"middle supported echoed", "2025-03-26", "2025-03-26"},
		{"oldest supported echoed", "2024-11-05", "2024-11-05"},
		// A revision the server does not know (newer or older than anything
		// listed) is answered with the server's NEWEST supported revision —
		// the client decides whether it can speak it.
		{"unsupported newer negotiates down", "2099-01-01", "2025-06-18"},
		{"unsupported older negotiates down", "2024-10-07", "2025-06-18"},
		{"garbage negotiates down", "not-a-revision", "2025-06-18"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
				`"protocolVersion":"` + tt.requested + `",` +
				`"capabilities":{"roots":{"listChanged":true}},` +
				`"clientInfo":{"name":"probe-client","version":"4.2.0"},` +
				`"unknownFutureField":"ignored"}}`
			rr := postMCP(t, router, "/api/v1/mcp", body)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
			}
			raw := decodeRPCBody(t, rr)
			if _, ok := raw["error"]; ok {
				t.Fatalf("initialize answered with an error: %s", rr.Body.String())
			}

			var result struct {
				ProtocolVersion string `json:"protocolVersion"`
				Capabilities    struct {
					Tools *struct {
						ListChanged bool `json:"listChanged"`
					} `json:"tools"`
				} `json:"capabilities"`
				ServerInfo struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"serverInfo"`
			}
			if err := json.Unmarshal(raw["result"], &result); err != nil {
				t.Fatalf("decode result: %v (body=%s)", err, rr.Body.String())
			}

			if result.ProtocolVersion != tt.want {
				t.Errorf("protocolVersion = %q, want %q", result.ProtocolVersion, tt.want)
			}
			if result.Capabilities.Tools == nil {
				t.Fatalf("capabilities.tools missing: %s", rr.Body.String())
			}
			if result.Capabilities.Tools.ListChanged {
				t.Errorf("capabilities.tools.listChanged = true, want false (no change notifications are sent)")
			}
			if result.ServerInfo.Name != "canopyd-canopy" {
				t.Errorf("serverInfo.name = %q, want %q", result.ServerInfo.Name, "canopyd-canopy")
			}
			if result.ServerInfo.Version != MCPVersion {
				t.Errorf("serverInfo.version = %q, want the build version %q", result.ServerInfo.Version, MCPVersion)
			}
			if result.ServerInfo.Version == "" {
				t.Error("serverInfo.version is empty")
			}
		})
	}
}

// TestMCPInitializeReportsInjectedBuildVersion proves serverInfo.version is
// read from the package var cmd/canopyd assigns (never a hardcoded literal
// that could drift from `canopyd -version`).
func TestMCPInitializeReportsInjectedBuildVersion(t *testing.T) {
	old := MCPVersion
	MCPVersion = "v9.9.9-injected"
	t.Cleanup(func() { MCPVersion = old })

	rr := postMCP(t, newMCPTestRouter(), "/api/v1/mcp",
		`{"jsonrpc":"2.0","id":"v","method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	raw := decodeRPCBody(t, rr)

	if !strings.Contains(string(raw["result"]), `"version":"v9.9.9-injected"`) {
		t.Fatalf("serverInfo.version did not follow the injected build version: %s", rr.Body.String())
	}
}

// TestMCPInitializeWithoutParamsNegotiatesNewest asserts a missing params
// object is tolerated (not a 400/-32602): the client gets the newest revision.
func TestMCPInitializeWithoutParamsNegotiatesNewest(t *testing.T) {
	rr := postMCP(t, newMCPTestRouter(), "/api/v1/mcp", `{"jsonrpc":"2.0","id":7,"method":"initialize"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	raw := decodeRPCBody(t, rr)
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(raw["result"], &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.ProtocolVersion != supportedProtocolVersions[0] {
		t.Errorf("protocolVersion = %q, want newest supported %q", result.ProtocolVersion, supportedProtocolVersions[0])
	}
}

// TestMCPInitializeRejectsNonObjectParams asserts a params value that is not
// an object is reported as Invalid params instead of being silently ignored.
func TestMCPInitializeRejectsNonObjectParams(t *testing.T) {
	rr := postMCP(t, newMCPTestRouter(), "/api/v1/mcp",
		`{"jsonrpc":"2.0","id":8,"method":"initialize","params":"2025-06-18"}`)

	raw := decodeRPCBody(t, rr)
	if _, ok := raw["error"]; !ok {
		t.Fatalf("non-object params accepted: %s", rr.Body.String())
	}
	var rpcErr rpcError
	if err := json.Unmarshal(raw["error"], &rpcErr); err != nil {
		t.Fatalf("decode error member: %v", err)
	}
	if rpcErr.Code != -32602 {
		t.Errorf("error code = %d, want -32602", rpcErr.Code)
	}
}

// TestMCPNotificationsAnswer202WithEmptyBody is AC2: notifications/initialized
// (and every other notification) is acknowledged with HTTP 202 and NO body —
// an error object here makes a standards-compliant client abort the handshake.
func TestMCPNotificationsAnswer202WithEmptyBody(t *testing.T) {
	router := newMCPTestRouter()

	tests := []struct {
		name string
		body string
	}{
		{"notifications/initialized", `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		{"notifications/initialized with params", `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`},
		{"notifications/cancelled", `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`},
		{"unknown notification", `{"jsonrpc":"2.0","method":"notifications/totally-made-up"}`},
		{"notification with explicit null id", `{"jsonrpc":"2.0","id":null,"method":"notifications/initialized"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := postMCP(t, router, "/api/v1/mcp", tt.body)

			if rr.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202 (body=%q)", rr.Code, rr.Body.String())
			}
			if got := rr.Body.Len(); got != 0 {
				t.Fatalf("body length = %d, want 0 (body=%q)", got, rr.Body.String())
			}
			for _, banned := range []string{"error", "result", "jsonrpc"} {
				if strings.Contains(rr.Body.String(), banned) {
					t.Errorf("notification response body contains %q: %q", banned, rr.Body.String())
				}
			}
		})
	}
}

// TestMCPPingReturnsEmptyResultObject is AC3 (part 1): `ping` answers with an
// EMPTY result object. The raw JSON is asserted because an empty
// map[string]any would be dropped entirely by an omitempty result field.
func TestMCPPingReturnsEmptyResultObject(t *testing.T) {
	rr := postMCP(t, newMCPTestRouter(), "/api/v1/mcp", `{"jsonrpc":"2.0","id":3,"method":"ping"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	raw := decodeRPCBody(t, rr)
	result, ok := raw["result"]
	if !ok {
		t.Fatalf("ping response has no result member: %s", rr.Body.String())
	}
	if string(result) != "{}" {
		t.Errorf(`ping result = %s, want {}`, result)
	}
	var id any
	if err := json.Unmarshal(raw["id"], &id); err != nil {
		t.Fatalf("decode id: %v", err)
	}
	if id != float64(3) {
		t.Errorf("ping id = %v, want 3", id)
	}
}

// TestMCPUnknownMethodStillMethodNotFound is AC3 (part 2): a method this
// endpoint does not implement keeps answering -32601.
func TestMCPUnknownMethodStillMethodNotFound(t *testing.T) {
	router := newMCPTestRouter()
	for _, method := range []string{"bogus/method", "resources/list", "prompts/list", "completion/complete"} {
		t.Run(method, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":4,"method":"` + method + `"}`
			rr := postMCP(t, router, "/api/v1/mcp", body)

			raw := decodeRPCBody(t, rr)
			var rpcErr rpcError
			if err := json.Unmarshal(raw["error"], &rpcErr); err != nil {
				t.Fatalf("decode error member: %v (body=%s)", err, rr.Body.String())
			}
			if rpcErr.Code != -32601 {
				t.Errorf("error code = %d, want -32601", rpcErr.Code)
			}
			if !strings.Contains(rpcErr.Message, method) {
				t.Errorf("error message %q does not name the method", rpcErr.Message)
			}
		})
	}
}

// TestMCPToolsListStillAdvertisesSevenTools is AC3 (part 3): tools/list is
// unchanged and needs no preceding initialize (the endpoint is stateless).
func TestMCPToolsListStillAdvertisesSevenTools(t *testing.T) {
	rr := postMCP(t, newMCPTestRouter(), "/api/v1/mcp", `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	raw := decodeRPCBody(t, rr)
	var result struct {
		Tools []toolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw["result"], &result); err != nil {
		t.Fatalf("decode result: %v (body=%s)", err, rr.Body.String())
	}

	want := []string{"list_trees", "get_tree", "create_node", "list_topics", "get_graph_stats", "list_approvals", "list_cards"}
	if len(result.Tools) != len(want) {
		t.Fatalf("tools/list returned %d tools, want %d: %s", len(result.Tools), len(want), rr.Body.String())
	}
	for i, name := range want {
		if result.Tools[i].Name != name {
			t.Errorf("tool[%d] = %q, want %q", i, result.Tools[i].Name, name)
		}
		if result.Tools[i].InputSchema.Type != "object" {
			t.Errorf("tool %q inputSchema.type = %q, want object", name, result.Tools[i].InputSchema.Type)
		}
	}
}

// TestMCPInvalidRequestAndParseErrorUnchanged locks the two pre-existing
// error paths so adding the handshake cannot silently relax them.
func TestMCPInvalidRequestAndParseErrorUnchanged(t *testing.T) {
	t.Run("wrong jsonrpc version", func(t *testing.T) {
		rr := postMCP(t, newMCPTestRouter(), "/api/v1/mcp", `{"jsonrpc":"1.0","id":6,"method":"tools/list"}`)
		raw := decodeRPCBody(t, rr)
		var rpcErr rpcError
		if err := json.Unmarshal(raw["error"], &rpcErr); err != nil {
			t.Fatalf("decode error member: %v", err)
		}
		if rpcErr.Code != -32600 {
			t.Errorf("error code = %d, want -32600", rpcErr.Code)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		rr := postMCP(t, newMCPTestRouter(), "/api/v1/mcp", `{"jsonrpc":`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
		raw := decodeRPCBody(t, rr)
		var rpcErr rpcError
		if err := json.Unmarshal(raw["error"], &rpcErr); err != nil {
			t.Fatalf("decode error member: %v", err)
		}
		if rpcErr.Code != -32700 {
			t.Errorf("error code = %d, want -32700", rpcErr.Code)
		}
	})
}

// TestMCPEndpointAcceptsTrailingSlash asserts the advertised path works with
// and without the trailing slash through the REAL mount expression, and that
// a full handshake (initialize → notifications/initialized → tools/list)
// succeeds against it (AC7).
func TestMCPEndpointAcceptsTrailingSlash(t *testing.T) {
	router := newMCPTestRouter()

	for _, path := range []string{"/api/v1/mcp", "/api/v1/mcp/"} {
		t.Run(path, func(t *testing.T) {
			init := postMCP(t, router, path,
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"probe","version":"1"}}}`)
			if init.Code != http.StatusOK {
				t.Fatalf("initialize status = %d, want 200 (body=%s)", init.Code, init.Body.String())
			}
			if !strings.Contains(init.Body.String(), `"name":"canopyd-canopy"`) {
				t.Fatalf("initialize result missing serverInfo.name: %s", init.Body.String())
			}

			notif := postMCP(t, router, path, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
			if notif.Code != http.StatusAccepted || notif.Body.Len() != 0 {
				t.Fatalf("notifications/initialized = %d with body %q, want 202 and empty", notif.Code, notif.Body.String())
			}

			list := postMCP(t, router, path, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
			if list.Code != http.StatusOK {
				t.Fatalf("tools/list status = %d, want 200", list.Code)
			}
			if got := strings.Count(list.Body.String(), `"name":"`); got < len(tools) {
				t.Fatalf("tools/list advertised %d tools, want %d: %s", got, len(tools), list.Body.String())
			}
		})
	}
}

// TestMCPNegotiateProtocolVersionUnit covers the negotiation helper directly,
// including the empty-requested case the handler passes through when params
// are absent.
func TestMCPNegotiateProtocolVersionUnit(t *testing.T) {
	if got := negotiateProtocolVersion(""); got != supportedProtocolVersions[0] {
		t.Errorf("negotiateProtocolVersion(%q) = %q, want %q", "", got, supportedProtocolVersions[0])
	}
	for _, v := range supportedProtocolVersions {
		if got := negotiateProtocolVersion(v); got != v {
			t.Errorf("negotiateProtocolVersion(%q) = %q, want the requested revision", v, got)
		}
	}
	if got := negotiateProtocolVersion("1999-01-01"); got != supportedProtocolVersions[0] {
		t.Errorf("negotiateProtocolVersion(unsupported) = %q, want newest %q", got, supportedProtocolVersions[0])
	}
}

// TestMCPRoutesOnlyAcceptsPOST asserts the mount still rejects other verbs at
// the handler router (chi answers 405), so the handshake work did not open the
// endpoint to GET.
func TestMCPRoutesOnlyAcceptsPOST(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcp", nil)
	rr := httptest.NewRecorder()
	newMCPTestRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rr.Code)
	}
}

func TestMCPCallToolResultEnvelope(t *testing.T) {
	result, err := newMCPCallToolResult(map[string]any{})
	if err != nil {
		t.Fatalf("newMCPCallToolResult: %v", err)
	}
	if result.IsError {
		t.Fatal("successful tool result has isError=true")
	}
	if len(result.Content) != 1 {
		t.Fatalf("content length = %d, want 1", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Errorf("content[0].type = %q, want text", result.Content[0].Type)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("content[0].text is not JSON: %v", err)
	}
	if payload == nil {
		t.Fatal("content[0].text decoded to nil, want an empty JSON object")
	}
}

type mcpCheckerStub struct {
	member       bool
	deleted      bool
	memberErr    error
	deletedErr   error
	memberCalls  int
	deletedCalls int
}

func (s *mcpCheckerStub) IsMember(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	s.memberCalls++
	return s.member, s.memberErr
}

func (s *mcpCheckerStub) IsTreeDeleted(context.Context, uuid.UUID) (bool, error) {
	s.deletedCalls++
	return s.deleted, s.deletedErr
}

type mcpNodeServiceStub struct {
	service.NodeService
	calls int
}

func (s *mcpNodeServiceStub) Create(context.Context, uuid.UUID, service.CreateNodeInput) (*service.CreateNodeResult, error) {
	s.calls++
	return &service.CreateNodeResult{Node: &service.NodeDetail{ID: uuid.New()}}, nil
}

func postMCPAsUser(t *testing.T, router http.Handler, userID uuid.UUID, raw string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, userID))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func TestMCPCreateNodeMembershipGate(t *testing.T) {
	const createNodeRequest = `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"create_node","arguments":{"tree_id":"00000000-0000-0000-0000-000000000001","content":"hello"}}}`
	checkerErr := errors.New("membership backend unavailable")
	for _, tt := range []struct {
		name             string
		member           bool
		deleted          bool
		memberErr        error
		wantMessage      string
		wantNodeCalls    int
		wantDeletedCalls int
	}{
		{name: "nonexistent tree", wantMessage: "tree not found or you are not a member"},
		{name: "valid non-member tree", wantMessage: "tree not found or you are not a member"},
		{name: "soft-deleted member tree", member: true, deleted: true, wantMessage: "tree has been deleted", wantDeletedCalls: 1},
		{name: "live member tree", member: true, wantNodeCalls: 1, wantDeletedCalls: 1},
		{name: "membership checker error", memberErr: checkerErr, wantMessage: "could not verify membership"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checker := &mcpCheckerStub{member: tt.member, deleted: tt.deleted, memberErr: tt.memberErr}
			nodes := &mcpNodeServiceStub{}
			h := NewMCPHandler(nil, nodes, nil, nil, nil, nil, checker)
			router := chi.NewRouter()
			router.Route("/api/v1", func(r chi.Router) { r.Mount("/mcp", h.Routes()) })

			rr := postMCPAsUser(t, router, uuid.New(), createNodeRequest)
			raw := decodeRPCBody(t, rr)
			var rpcErr rpcError
			if tt.wantMessage == "" {
				if err := json.Unmarshal(raw["result"], &map[string]any{}); err != nil {
					t.Fatalf("success response result: %v (body=%s)", err, rr.Body.String())
				}
				if _, ok := raw["error"]; ok {
					t.Fatalf("live member rejected: %s", rr.Body.String())
				}
			} else {
				if err := json.Unmarshal(raw["error"], &rpcErr); err != nil {
					t.Fatalf("decode error: %v (body=%s)", err, rr.Body.String())
				}
				if rpcErr.Code != -32000 || !strings.Contains(rpcErr.Message, tt.wantMessage) {
					t.Fatalf("error = %#v, want -32000 containing %q", rpcErr, tt.wantMessage)
				}
				for _, banned := range []string{"database unavailable", "SQLSTATE", "ERROR:", "fk_"} {
					if strings.Contains(rpcErr.Message, banned) && strings.Contains(tt.wantMessage, "tree not found") {
						t.Fatalf("error message contains banned %q: %q", banned, rpcErr.Message)
					}
				}
			}
			if nodes.calls != tt.wantNodeCalls {
				t.Errorf("node service calls = %d, want %d", nodes.calls, tt.wantNodeCalls)
			}
			if checker.memberCalls != 1 {
				t.Errorf("membership checks = %d, want 1", checker.memberCalls)
			}
			if checker.deletedCalls != tt.wantDeletedCalls {
				t.Errorf("deleted checks = %d, want %d", checker.deletedCalls, tt.wantDeletedCalls)
			}
		})
	}
}

func TestMCPCreateNodeCheckerDeletedErrorIsInternal(t *testing.T) {
	checker := &mcpCheckerStub{member: true, deletedErr: errors.New("tree state backend failed")}
	nodes := &mcpNodeServiceStub{}
	h := NewMCPHandler(nil, nodes, nil, nil, nil, nil, checker)
	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) { r.Mount("/mcp", h.Routes()) })
	rr := postMCPAsUser(t, router, uuid.New(), `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"create_node","arguments":{"tree_id":"00000000-0000-0000-0000-000000000001","content":"hello"}}}`)
	var rpcErr rpcError
	if err := json.Unmarshal(decodeRPCBody(t, rr)["error"], &rpcErr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rpcErr.Message, "could not verify tree state") || strings.Contains(rpcErr.Message, "database unavailable") {
		t.Fatalf("checker error = %q, want internal tree-state wording", rpcErr.Message)
	}
}

func newMCPDBTestRouter(t *testing.T, pool *pgxpool.Pool) *chi.Mux {
	t.Helper()
	treeSvc := service.NewTreeService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool),
		db.NewPGEdgeRepo(pool),
		pool,
	)
	h := NewMCPHandler(treeSvc, nil, nil, nil, nil, nil, nil)
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Mount("/mcp", h.Routes())
	})
	return r
}

func TestMCPToolsCallListTreesUsesCallToolResultEnvelope(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	router := newMCPDBTestRouter(t, pool)

	rr := postMCP(t, router, "/api/v1/mcp", `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"list_trees","arguments":{}}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}

	raw := decodeRPCBody(t, rr)
	var result mcpCallToolResult
	if err := json.Unmarshal(raw["result"], &result); err != nil {
		t.Fatalf("decode CallToolResult: %v (body=%s)", err, rr.Body.String())
	}
	if result.IsError {
		t.Fatal("list_trees success has isError=true")
	}
	if len(result.Content) < 1 {
		t.Fatalf("content = %#v, want at least one item", result.Content)
	}
	if result.Content[0].Type != "text" {
		t.Errorf("content[0].type = %q, want text", result.Content[0].Type)
	}
	var payload struct {
		Trees []json.RawMessage `json:"trees"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("content[0].text is not the marshaled tool payload: %v", err)
	}
	if payload.Trees == nil {
		t.Fatalf("content[0].text = %q, want a trees array", result.Content[0].Text)
	}
}

func TestMCPToolsCallExecutionErrorRemainsJSONRPCError(t *testing.T) {
	rr := postMCP(t, newMCPTestRouter(), "/api/v1/mcp", `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"not_a_tool","arguments":{}}}`)
	raw := decodeRPCBody(t, rr)
	if _, ok := raw["result"]; ok {
		t.Fatalf("execution error unexpectedly returned a result: %s", rr.Body.String())
	}
	var rpcErr rpcError
	if err := json.Unmarshal(raw["error"], &rpcErr); err != nil {
		t.Fatalf("decode error member: %v", err)
	}
	if rpcErr.Code != -32000 {
		t.Errorf("error code = %d, want existing -32000", rpcErr.Code)
	}
}
