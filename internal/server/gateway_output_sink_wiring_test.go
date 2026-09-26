package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

const gatewayOutputSinkWiringRunID = "run_wiringtest"

func newGatewayOutputSinkWiringStub() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status":"ok"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `{"run_id":"run_wiringtest","status":"started"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_wiringtest/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w,
				"data: {\"event\":\"run.started\",\"run_id\":\"run_wiringtest\"}\n\n"+
					"data: {\"event\":\"run.completed\",\"run_id\":\"run_wiringtest\",\"output\":\"wiring proof output\"}\n\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"message":"not found"}}`)
		}
	}))
}

func gatewayOutputSinkWiringToken(t *testing.T, secret string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": db.DevJWTUserID,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	raw, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign dev JWT: %v", err)
	}
	return raw
}

func gatewayOutputSinkWiringRequest(t *testing.T, router http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestGatewayOutputSinkCompositionRootWiring proves the output sink through
// the REAL composition-root router seam. Unlike the direct sink integration
// test, this exercises newRouter's gateway service construction, the HTTP
// handler, the real gateway client/SSE observer, and the sink installed by
// server.go. Removing SetRunOutputSink from newRouter must make this test RED.
func TestGatewayOutputSinkCompositionRootWiring(t *testing.T) {
	testutil.SkipIfNoDB(t)
	ctx := context.Background()
	pool := testutil.NewSharedIntegrationPool(t)

	if _, err := db.EnsureDevJWTUser(ctx, pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	}
	if _, err := db.EnsureDevWorkspaceProfile(ctx, pool); err != nil {
		t.Fatalf("EnsureDevWorkspaceProfile: %v", err)
	}

	stub := newGatewayOutputSinkWiringStub()
	defer stub.Close()

	// Keep the composition root's persisted gateway registry hermetic.
	home := t.TempDir()
	t.Setenv("HOME", home)
	stateFile := filepath.Join(home, ".hermes", "canopy", "gateway", "runs.jsonl")
	t.Setenv("CANOPY_GATEWAY_STATE_FILE", stateFile)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatalf("create gateway state directory: %v", err)
	}

	cfg := config.Default()
	cfg.GatewayBaseURL = stub.URL
	cfg.GatewayAPIKey = "wiring-test-key"
	cfg.JWTSecret = db.DevJWTSecretDefault

	ownerID := uuid.MustParse(db.DevJWTUserID)
	treeSvc := service.NewTreeService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool,
	)
	tree, err := treeSvc.CreateTree(ctx, service.CreateTreeParams{
		OwnerID:       ownerID,
		Title:         "Gateway output sink composition-root wiring",
		RootContent:   "source",
		ContentFormat: service.FormatMarkdown,
		NodeType:      service.NodeTypeMessage,
	})
	if err != nil {
		t.Fatalf("CreateTree: %v", err)
	}
	nodeSvc := service.NewNodeService(db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool, nil)
	compiler := ctxpkg.NewCompiler(
		db.NewPGNodeRepo(pool),
		db.NewPGTopicRepo(pool),
		nil,
		ctxpkg.NewTokenEstimator(),
		cfg.ContextMaxRefs,
	)

	// Build the router exactly where server.New delegates composition-root
	// wiring. The DB pool is intentionally present: it is the dependency that
	// lets newRouter install newGatewayOutputSink for production services.
	router := newRouter(&routeDeps{
		jwtSecret:   db.DevJWTSecretDefault,
		treeSvc:     treeSvc,
		nodeSvc:     nodeSvc,
		ctxCompiler: compiler,
		cfg:         cfg,
		dbPool:      pool,
		connMgr:     transport.NewConnectionManager(nil),
	})
	token := gatewayOutputSinkWiringToken(t, cfg.JWTSecret)

	start := gatewayOutputSinkWiringRequest(t, router, http.MethodPost, "/api/v1/gateway/runs", token,
		fmt.Sprintf(`{"message":"composition-root wiring proof","node_id":%q}`, tree.RootNodeID.String()))
	if start.Code != http.StatusAccepted {
		t.Fatalf("start run status = %d, want 202: %s", start.Code, start.Body.String())
	}
	var started struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start response: %v; body=%s", err, start.Body.String())
	}
	if started.RunID != gatewayOutputSinkWiringRunID {
		t.Fatalf("run_id = %q, want %q", started.RunID, gatewayOutputSinkWiringRunID)
	}

	deadline := time.Now().Add(10 * time.Second)
	var runBody struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
		Output string `json:"output"`
	}
	for {
		poll := gatewayOutputSinkWiringRequest(t, router, http.MethodGet,
			"/api/v1/gateway/runs/"+started.RunID, token, "")
		if poll.Code == http.StatusOK {
			if err := json.Unmarshal(poll.Body.Bytes(), &runBody); err != nil {
				t.Fatalf("decode run response: %v; body=%s", err, poll.Body.String())
			}
			if runBody.Status == "completed" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not complete within 10s: status=%d body=%s", poll.Code, poll.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runBody.Output != "wiring proof output" {
		t.Fatalf("run output = %q, want wiring proof output", runBody.Output)
	}

	nodes, err := nodeSvc.ListByTree(ctx, tree.ID)
	if err != nil {
		t.Fatalf("ListByTree: %v", err)
	}
	var reply *service.NodeDetail
	for i := range nodes {
		if nodes[i].ParentID != nil && *nodes[i].ParentID == tree.RootNodeID && nodes[i].Content == "wiring proof output" {
			reply = &nodes[i]
			break
		}
	}
	if reply == nil {
		t.Fatalf("gateway reply node missing from normal node list: %+v", nodes)
	}
	var replyMetadata map[string]string
	if err := json.Unmarshal(reply.Metadata, &replyMetadata); err != nil {
		t.Fatalf("decode reply metadata: %v", err)
	}
	if replyMetadata["origin"] != "gateway_run" || replyMetadata["run_id"] != gatewayOutputSinkWiringRunID {
		t.Fatalf("reply metadata = %#v", replyMetadata)
	}

	root, err := nodeSvc.GetByID(ctx, tree.RootNodeID)
	if err != nil {
		t.Fatalf("GetByID root: %v", err)
	}
	var rootMetadata map[string]json.RawMessage
	if err := json.Unmarshal(root.Metadata, &rootMetadata); err != nil {
		t.Fatalf("decode root metadata: %v", err)
	}
	var lastRun map[string]string
	if err := json.Unmarshal(rootMetadata["last_gateway_run"], &lastRun); err != nil {
		t.Fatalf("decode root last_gateway_run: %v", err)
	}
	if lastRun["run_id"] != gatewayOutputSinkWiringRunID {
		t.Fatalf("root last_gateway_run = %#v", lastRun)
	}
}
