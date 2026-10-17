// DF-HERMES-CANOPY-84: the flat /api/v1/nodes mount (PATCH/DELETE/reply/fork
// by bare node id) was wired WITHOUT NodeAccessMiddleware in production —
// only handler-package tests mounted the middleware on their own private
// routers, so the production surface let any authenticated viewer (or
// non-member) mutate any node. The tree-scoped mount was closed by a831e83d
// (DF-HERMES-CANOPY-79); this file proves the flat mount carries the same
// gate on the REAL production router.
//
// Two layers:
//
//  1. TestRouteParityFlatNodeWritesCarryMembershipGate — chi.Walk over
//     newRouter asserting every flat node write endpoint carries the SAME
//     inline middleware class as the tree-scoped node PATCH control (which
//     has always been gated). Structural, DB-free.
//
//  2. TestFlatNodesViewerGateIsEnforcedOnProductionRouter — live HTTP probe
//     against newRouter with stub services: a viewer's PATCH/DELETE/reply/
//     fork must answer 403 VIEWER_READ_ONLY WITHOUT reaching the node
//     service, a non-member's PATCH must answer 403 NOT_TREE_MEMBER, and
//     member/owner writes must still succeed (no regression).
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

// df84TreeID is the tree every df84 stub node lives in.
var df84TreeID = uuid.MustParse("df840000-0000-0000-0000-000000000001")

// df84NodeID is the node the probes target.
var df84NodeID = uuid.MustParse("df840000-0000-0000-0000-000000000002")

// df84NodeStub implements service.NodeService for wiring probes. GetByID
// always resolves df84NodeID into df84TreeID (that is the resolution the
// NodeAccessMiddleware needs for bare-id paths); the write methods record
// their calls so a test can prove the middleware rejected BEFORE the
// service was reached.
type df84NodeStub struct {
	writes []string
}

func (s *df84NodeStub) record(op string) { s.writes = append(s.writes, op) }

func (s *df84NodeStub) Create(_ context.Context, _ uuid.UUID, _ service.CreateNodeInput) (*service.CreateNodeResult, error) {
	s.record("create")
	return &service.CreateNodeResult{}, nil
}

func (s *df84NodeStub) GetByID(_ context.Context, _ uuid.UUID) (*service.NodeDetail, error) {
	return &service.NodeDetail{ID: df84NodeID, TreeID: df84TreeID}, nil
}

func (s *df84NodeStub) ListByTree(_ context.Context, _ uuid.UUID) ([]service.NodeDetail, error) {
	return []service.NodeDetail{}, nil
}

func (s *df84NodeStub) Update(_ context.Context, _ uuid.UUID, _ service.UpdateNodeInput) (*service.NodeDetail, error) {
	s.record("update")
	return &service.NodeDetail{ID: df84NodeID, TreeID: df84TreeID, Content: "updated"}, nil
}

func (s *df84NodeStub) SoftDelete(_ context.Context, _ uuid.UUID) (*service.DeleteNodeResult, error) {
	s.record("soft-delete")
	return &service.DeleteNodeResult{ID: df84NodeID, TreeID: df84TreeID, DeletedAt: time.Now().UTC()}, nil
}

func (s *df84NodeStub) Reply(_ context.Context, _ uuid.UUID, _ service.ReplyInput) (*service.CreateNodeResult, error) {
	s.record("reply")
	return &service.CreateNodeResult{Node: &service.NodeDetail{ID: df84NodeID, TreeID: df84TreeID}}, nil
}

func (s *df84NodeStub) Fork(_ context.Context, _ uuid.UUID, _ service.ForkInput) (*service.CreateNodeResult, error) {
	s.record("fork")
	return &service.CreateNodeResult{Node: &service.NodeDetail{ID: df84NodeID, TreeID: df84TreeID}}, nil
}

func (s *df84NodeStub) CreateMultiReferenceReply(_ context.Context, _ uuid.UUID, _ service.CreateMultiReferenceReplyInput) (*service.CreateMultiReferenceReplyResult, error) {
	s.record("multi-ref-reply")
	return &service.CreateMultiReferenceReplyResult{}, nil
}

// df84MemberStub implements db.TreeMemberRepo with a role map keyed by
// (tree, user). Unknown pairs are NON-members (the default that must answer
// 403 NOT_TREE_MEMBER).
type df84MemberStub struct {
	roles map[df84MembershipKey]string
}

type df84MembershipKey struct {
	tree, user uuid.UUID
}

func (s *df84MemberStub) role(treeID, userID uuid.UUID) (string, bool) {
	role, ok := s.roles[df84MembershipKey{treeID, userID}]
	return role, ok
}

func (s *df84MemberStub) Add(_ context.Context, _ *db.TreeMember) (*db.TreeMember, error) {
	return nil, nil
}

func (s *df84MemberStub) GetByTree(_ context.Context, _ uuid.UUID) ([]db.TreeMember, error) {
	return nil, nil
}

func (s *df84MemberStub) GetByUser(_ context.Context, _ uuid.UUID) ([]db.TreeMember, error) {
	return nil, nil
}

func (s *df84MemberStub) UpdateRole(_ context.Context, _ uuid.UUID, _ string) (*db.TreeMember, error) {
	return nil, nil
}

func (s *df84MemberStub) Remove(_ context.Context, _ uuid.UUID) error { return nil }

func (s *df84MemberStub) IsMember(_ context.Context, treeID, userID uuid.UUID) (string, bool, error) {
	role, ok := s.role(treeID, userID)
	return role, ok, nil
}

func (s *df84MemberStub) GetRole(_ context.Context, treeID, userID uuid.UUID) (string, error) {
	role, _ := s.role(treeID, userID)
	return role, nil
}

func (s *df84MemberStub) IsTreeDeleted(_ context.Context, _ uuid.UUID) (bool, error) {
	return false, nil
}

// df84Router builds the REAL production router (the newRouter seam) with
// stub node/membership services and a database-free auth middleware (any
// valid HS256 token with a UUID sub passes). Hermetic gateway state file
// points at an empty temp HOME, mirroring route_parity_test.go.
func df84Router(t *testing.T, nodes service.NodeService, members db.TreeMemberRepo) *chi.Mux {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".hermes", "canopy", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}
	return newRouter(&routeDeps{
		jwtSecret:   "df84-wiring-test-secret",
		nodeSvc:     nodes,
		membersRepo: members,
		connMgr:     transport.NewConnectionManager(nil),
		cfg:         &config.Config{},
	})
}

// df84Token mints an HS256 bearer token for a user UUID.
func df84Token(t *testing.T, sub uuid.UUID) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": sub.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	raw, err := tok.SignedString([]byte("df84-wiring-test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return raw
}

// TestRouteParityFlatNodeWritesCarryMembershipGate walks the real production
// router and asserts every flat /nodes write endpoint carries the same inline
// middleware class as the tree-scoped node PATCH control. Before DF-84 the
// flat endpoints carried ONLY the auth middleware — one class fewer than the
// control — which is exactly the missing-gate shape this test pins.
func TestRouteParityFlatNodeWritesCarryMembershipGate(t *testing.T) {
	router := df84Router(t, &df84NodeStub{}, &df84MemberStub{})

	type route struct{ method, pattern string }
	mwCount := map[route]int{}
	err := chi.Walk(router, func(method, pattern string, _ http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if method == "" {
			method = http.MethodGet
		}
		mwCount[route{method, normalizeChiPattern(pattern)}] = len(middlewares)
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}

	// Control: the tree-scoped node write route, membership-gated since
	// DF-HERMES-CANOPY-79 (a831e83d).
	control := route{http.MethodPatch, "/api/v1/trees/{}/nodes/{}"}
	controlCount, ok := mwCount[control]
	if !ok {
		t.Fatalf("control route %s %s is not mounted — cannot compare middleware classes",
			control.method, control.pattern)
	}

	flat := []route{
		{http.MethodPatch, "/api/v1/nodes/{}"},
		{http.MethodDelete, "/api/v1/nodes/{}"},
		{http.MethodPost, "/api/v1/nodes/{}/reply"},
		{http.MethodPost, "/api/v1/nodes/{}/fork"},
	}
	for _, r := range flat {
		n, ok := mwCount[r]
		if !ok {
			t.Errorf("flat node write %s %s is NOT mounted on the real router", r.method, r.pattern)
			continue
		}
		if n != controlCount {
			t.Errorf("flat node write %s %s has %d inline middleware, want %d (same class as %s) — its membership/viewer gate is missing",
				r.method, r.pattern, n, controlCount, control.pattern)
		}
	}
}

// TestFlatNodesViewerGateIsEnforcedOnProductionRouter probes the REAL
// production router over HTTP: viewer writes → 403 VIEWER_READ_ONLY with the
// service untouched; non-member writes → 403 NOT_TREE_MEMBER; member and
// owner writes still succeed.
func TestFlatNodesViewerGateIsEnforcedOnProductionRouter(t *testing.T) {
	viewer := uuid.MustParse("df840000-0000-0000-0000-0000000000a1")
	editor := uuid.MustParse("df840000-0000-0000-0000-0000000000a2")
	owner := uuid.MustParse("df840000-0000-0000-0000-0000000000a3")
	stranger := uuid.MustParse("df840000-0000-0000-0000-0000000000a4")

	nodes := &df84NodeStub{}
	members := &df84MemberStub{roles: map[df84MembershipKey]string{
		{df84TreeID, viewer}: db.TreeRoleViewer,
		{df84TreeID, editor}: db.TreeRoleMember,
		{df84TreeID, owner}:  db.TreeRoleOwner,
		// stranger intentionally absent — non-member.
	}}

	srv := httptest.NewServer(df84Router(t, nodes, members))
	defer srv.Close()

	do := func(t *testing.T, method, path string, user uuid.UUID, body string) (int, apiErrorShape) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		req.Header.Set("Authorization", "Bearer "+df84Token(t, user))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
			req.Body = io.NopCloser(strings.NewReader(body))
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		var shape apiErrorShape
		_ = json.NewDecoder(resp.Body).Decode(&shape)
		return resp.StatusCode, shape
	}

	nodePath := "/api/v1/nodes/" + df84NodeID.String()

	t.Run("viewer writes are rejected before the service", func(t *testing.T) {
		for _, probe := range []struct {
			method, path, body string
		}{
			{http.MethodPatch, nodePath, `{"content":"tampered by viewer"}`},
			{http.MethodDelete, nodePath, ""},
			{http.MethodPost, nodePath + "/reply", `{"content":"viewer reply"}`},
			{http.MethodPost, nodePath + "/fork", `{"content":"viewer fork"}`},
		} {
			status, shape := do(t, probe.method, probe.path, viewer, probe.body)
			if status != http.StatusForbidden {
				t.Errorf("viewer %s %s: status=%d, want 403", probe.method, probe.path, status)
			}
			if shape.Error.Code != "VIEWER_READ_ONLY" {
				t.Errorf("viewer %s %s: error code=%q, want VIEWER_READ_ONLY", probe.method, probe.path, shape.Error.Code)
			}
		}
		if len(nodes.writes) != 0 {
			t.Errorf("viewer write reached the node service: %v — the middleware must reject BEFORE the handler", nodes.writes)
		}
	})

	t.Run("non-member write is rejected with NOT_TREE_MEMBER", func(t *testing.T) {
		status, shape := do(t, http.MethodPatch, nodePath, stranger, `{"content":"stranger write"}`)
		if status != http.StatusForbidden {
			t.Errorf("non-member PATCH: status=%d, want 403", status)
		}
		if shape.Error.Code != "NOT_TREE_MEMBER" {
			t.Errorf("non-member PATCH: error code=%q, want NOT_TREE_MEMBER", shape.Error.Code)
		}
		if len(nodes.writes) != 0 {
			t.Errorf("non-member write reached the node service: %v", nodes.writes)
		}
	})

	t.Run("member and owner writes still succeed", func(t *testing.T) {
		// Editor (member role) — every flat write surface stays open.
		status, _ := do(t, http.MethodPatch, nodePath, editor, `{"content":"editor update"}`)
		if status != http.StatusOK {
			t.Errorf("editor PATCH: status=%d, want 200", status)
		}
		status, _ = do(t, http.MethodDelete, nodePath, editor, "")
		if status != http.StatusOK {
			t.Errorf("editor DELETE: status=%d, want 200", status)
		}
		status, _ = do(t, http.MethodPost, nodePath+"/reply", editor, `{"content":"editor reply"}`)
		if status != http.StatusCreated {
			t.Errorf("editor reply: status=%d, want 201", status)
		}
		status, _ = do(t, http.MethodPost, nodePath+"/fork", editor, `{"content":"editor fork"}`)
		if status != http.StatusCreated {
			t.Errorf("editor fork: status=%d, want 201", status)
		}

		// Owner — same class.
		status, _ = do(t, http.MethodPatch, nodePath, owner, `{"content":"owner update"}`)
		if status != http.StatusOK {
			t.Errorf("owner PATCH: status=%d, want 200", status)
		}

		want := []string{"update", "soft-delete", "reply", "fork", "update"}
		if len(nodes.writes) != len(want) {
			t.Fatalf("service calls = %v, want %v", nodes.writes, want)
		}
		for i, op := range want {
			if nodes.writes[i] != op {
				t.Fatalf("service calls = %v, want %v", nodes.writes, want)
			}
		}
	})
}

// apiErrorShape mirrors handler's error envelope ({"error":{"code","message"}})
// without importing the handler test helpers.
type apiErrorShape struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
