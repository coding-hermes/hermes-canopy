package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// --- Stub repos for export tests -------------------------------------------

type stubExportTreeRepo struct {
	trees map[uuid.UUID]*db.Tree
}

func newStubExportTreeRepo() *stubExportTreeRepo {
	return &stubExportTreeRepo{trees: make(map[uuid.UUID]*db.Tree)}
}

func (s *stubExportTreeRepo) GetByID(_ context.Context, id uuid.UUID) (*db.Tree, error) {
	t, ok := s.trees[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	return t, nil
}

func (s *stubExportTreeRepo) Create(_ context.Context, _ *db.Tree) (*db.Tree, error) {
	panic("not used")
}
func (s *stubExportTreeRepo) GetByOwner(_ context.Context, _ uuid.UUID) ([]db.Tree, error) {
	panic("not used")
}
func (s *stubExportTreeRepo) Update(_ context.Context, _ *db.Tree) (*db.Tree, error) {
	panic("not used")
}
func (s *stubExportTreeRepo) SoftDelete(_ context.Context, _ uuid.UUID) error { panic("not used") }
func (s *stubExportTreeRepo) List(_ context.Context, _, _ int) ([]db.Tree, error) {
	panic("not used")
}
func (s *stubExportTreeRepo) Search(_ context.Context, _ string, _, _ int) ([]db.Tree, error) {
	panic("not used")
}
func (s *stubExportTreeRepo) Count(_ context.Context) (int, error) {
	panic("not used")
}
func (s *stubExportTreeRepo) ListKeyset(_ context.Context, _ *uuid.UUID, _ int) ([]db.Tree, error) {
	panic("not used")
}

type stubExportNodeRepo struct {
	nodesByTree map[uuid.UUID][]db.Node
}

func newStubExportNodeRepo() *stubExportNodeRepo {
	return &stubExportNodeRepo{nodesByTree: make(map[uuid.UUID][]db.Node)}
}

func (s *stubExportNodeRepo) GetByTree(_ context.Context, treeID uuid.UUID) ([]db.Node, error) {
	return s.nodesByTree[treeID], nil
}
func (s *stubExportNodeRepo) Create(_ context.Context, _ *db.Node) (*db.Node, error) {
	panic("not used")
}
func (s *stubExportNodeRepo) GetByID(_ context.Context, _ uuid.UUID) (*db.Node, error) {
	panic("not used")
}
func (s *stubExportNodeRepo) GetChildren(_ context.Context, _ uuid.UUID) ([]db.Node, error) {
	panic("not used")
}
func (s *stubExportNodeRepo) GetAncestors(_ context.Context, _ uuid.UUID) ([]db.Node, error) {
	panic("not used")
}
func (s *stubExportNodeRepo) GetSubtree(_ context.Context, _ uuid.UUID, _ int) ([]db.Node, error) {
	panic("not used")
}
func (s *stubExportNodeRepo) GetPath(_ context.Context, _, _ uuid.UUID) ([]db.Node, error) {
	panic("not used")
}
func (s *stubExportNodeRepo) Update(_ context.Context, _ uuid.UUID, _ string, _ []byte) (*db.Node, error) {
	panic("not used")
}
func (s *stubExportNodeRepo) SoftDelete(_ context.Context, _ uuid.UUID) error { panic("not used") }
func (s *stubExportNodeRepo) HardDelete(_ context.Context, _ uuid.UUID) error { panic("not used") }
func (s *stubExportNodeRepo) GetCounts(_ context.Context, _ uuid.UUID) (*db.NodeCounts, error) {
	panic("not used")
}

type stubExportEdgeRepo struct {
	edgesByTree map[uuid.UUID][]db.Edge
}

func newStubExportEdgeRepo() *stubExportEdgeRepo {
	return &stubExportEdgeRepo{edgesByTree: make(map[uuid.UUID][]db.Edge)}
}

func (s *stubExportEdgeRepo) GetByTree(_ context.Context, treeID uuid.UUID) ([]db.Edge, error) {
	return s.edgesByTree[treeID], nil
}
func (s *stubExportEdgeRepo) Create(_ context.Context, _ *db.Edge) (*db.Edge, error) {
	panic("not used")
}
func (s *stubExportEdgeRepo) GetByID(_ context.Context, _ uuid.UUID) (*db.Edge, error) {
	panic("not used")
}
func (s *stubExportEdgeRepo) GetBySource(_ context.Context, _ uuid.UUID) ([]db.Edge, error) {
	panic("not used")
}
func (s *stubExportEdgeRepo) GetByTarget(_ context.Context, _ uuid.UUID) ([]db.Edge, error) {
	panic("not used")
}
func (s *stubExportEdgeRepo) SoftDelete(_ context.Context, _ uuid.UUID) error { panic("not used") }
func (s *stubExportEdgeRepo) GetParents(_ context.Context, _ uuid.UUID) ([]db.Node, error) {
	panic("not used")
}
func (s *stubExportEdgeRepo) GetSiblings(_ context.Context, _, _ uuid.UUID) ([]db.Node, error) {
	panic("not used")
}
func (s *stubExportEdgeRepo) GetEdgeCounts(_ context.Context, _ uuid.UUID) (*db.EdgeCounts, error) {
	panic("not used")
}
func (s *stubExportEdgeRepo) Move(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*db.Edge, error) {
	panic("not used")
}

// --- Helpers ----------------------------------------------------------------

func makeExportService() (*service.ExportServiceImpl, *stubExportTreeRepo, *stubExportNodeRepo, *stubExportEdgeRepo) {
	treeRepo := newStubExportTreeRepo()
	nodeRepo := newStubExportNodeRepo()
	edgeRepo := newStubExportEdgeRepo()
	svc := &service.ExportServiceImpl{}
	// We use reflection-free injection: the service struct has unexported
	// fields, so we construct via NewExportService to get the wiring.
	// Since there's no pool, ImportTree will fail — that's fine for the
	// handler-level tests that only test the HTTP layer validation.
	return svc, treeRepo, nodeRepo, edgeRepo
}

func seedExportTree(repos ...any) (treeID, ownerID, rootNodeID uuid.UUID) {
	treeID = uuid.New()
	ownerID = uuid.New()
	rootNodeID = uuid.New()
	now := time.Now().UTC()
	rootID := rootNodeID // non-nil copy

	for _, r := range repos {
		switch r := r.(type) {
		case *stubExportTreeRepo:
			r.trees[treeID] = &db.Tree{
				ID: treeID, OwnerID: ownerID, Title: "Test Tree",
				Description: "A test tree", RootNodeID: &rootID,
				CreatedAt: now,
			}
		case *stubExportNodeRepo:
			r.nodesByTree[treeID] = []db.Node{
				{
					ID: rootNodeID, TreeID: treeID, AuthorID: ownerID,
					Content: "Hello", ContentFormat: "markdown",
					NodeType: "message", CreatedAt: now,
				},
			}
		case *stubExportEdgeRepo:
			r.edgesByTree[treeID] = nil
		}
	}
	return
}

// --- Tests ------------------------------------------------------------------

func TestExportTreeRoundtrip(t *testing.T) {
	treeRepo := newStubExportTreeRepo()
	nodeRepo := newStubExportNodeRepo()
	edgeRepo := newStubExportEdgeRepo()

	treeID, _, _ := seedExportTree(treeRepo, nodeRepo, edgeRepo)

	svc := service.NewExportService(treeRepo, nodeRepo, edgeRepo, nil)

	data, err := svc.ExportTree(context.Background(), treeID)
	if err != nil {
		t.Fatalf("ExportTree failed: %v", err)
	}
	if data.Tree.Title != "Test Tree" {
		t.Errorf("expected title 'Test Tree', got %q", data.Tree.Title)
	}
	if len(data.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(data.Nodes))
	}
	if data.Nodes[0].Content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", data.Nodes[0].Content)
	}
	if data.Version != 1 {
		t.Errorf("expected version 1, got %d", data.Version)
	}
}

func TestExportTreeNotFound(t *testing.T) {
	treeRepo := newStubExportTreeRepo()
	nodeRepo := newStubExportNodeRepo()
	edgeRepo := newStubExportEdgeRepo()

	svc := service.NewExportService(treeRepo, nodeRepo, edgeRepo, nil)
	_, err := svc.ExportTree(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error for non-existent tree, got nil")
	}
}

func TestExportHandlerSuccess(t *testing.T) {
	treeRepo := newStubExportTreeRepo()
	nodeRepo := newStubExportNodeRepo()
	edgeRepo := newStubExportEdgeRepo()

	treeID, ownerID, _ := seedExportTree(treeRepo, nodeRepo, edgeRepo)

	svc := service.NewExportService(treeRepo, nodeRepo, edgeRepo, nil)
	handler := NewExportHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/trees/"+treeID.String()+"/export", nil)

	// Chi's parseTreeID reads from the route context; we need to set the
	// URL param before the handler sees the request.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tree_id", treeID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, userIDContextKey{}, ownerID)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ExportTree(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var data service.ExportData
	if err := json.NewDecoder(rec.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if data.Tree.Title != "Test Tree" {
		t.Errorf("title = %q, want 'Test Tree'", data.Tree.Title)
	}
}

func TestExportHandlerInvalidUUID(t *testing.T) {
	treeRepo := newStubExportTreeRepo()
	nodeRepo := newStubExportNodeRepo()
	edgeRepo := newStubExportEdgeRepo()

	svc := service.NewExportService(treeRepo, nodeRepo, edgeRepo, nil)
	handler := NewExportHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/trees/not-a-uuid/export", nil)
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, uuid.New()))
	rec := httptest.NewRecorder()

	handler.ExportTree(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid UUID, got %d", rec.Code)
	}
}

func TestExportHandlerUnauthenticated(t *testing.T) {
	treeRepo := newStubExportTreeRepo()
	nodeRepo := newStubExportNodeRepo()
	edgeRepo := newStubExportEdgeRepo()

	treeID, _, _ := seedExportTree(treeRepo, nodeRepo, edgeRepo)

	svc := service.NewExportService(treeRepo, nodeRepo, edgeRepo, nil)
	handler := NewExportHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/trees/"+treeID.String()+"/export", nil)

	// Set URL param so parseTreeID succeeds; no user in context should
	// trigger auth check failure.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tree_id", treeID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ExportTree(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", rec.Code)
	}
}

func TestImportHandlerInvalidJSON(t *testing.T) {
	svc := service.NewExportService(
		newStubExportTreeRepo(), newStubExportNodeRepo(), newStubExportEdgeRepo(), nil,
	)
	handler := NewExportHandler(svc)

	body := bytes.NewBufferString(`{not valid json`)
	req := httptest.NewRequest(http.MethodPost, "/trees/import", body)
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, uuid.New()))
	rec := httptest.NewRecorder()

	handler.ImportTree(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportHandlerMissingAuth(t *testing.T) {
	svc := service.NewExportService(
		newStubExportTreeRepo(), newStubExportNodeRepo(), newStubExportEdgeRepo(), nil,
	)
	handler := NewExportHandler(svc)

	payload := service.ExportData{
		Tree: service.ExportTree{Title: "Test"},
		Nodes: []db.Node{
			{ID: uuid.New(), Content: "hi", ContentFormat: "markdown", NodeType: "message"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/trees/import", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()

	handler.ImportTree(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportHandlerMissingTreeTitle(t *testing.T) {
	svc := service.NewExportService(
		newStubExportTreeRepo(), newStubExportNodeRepo(), newStubExportEdgeRepo(), nil,
	)
	handler := NewExportHandler(svc)

	payload := service.ExportData{Tree: service.ExportTree{Title: ""}}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/trees/import", bytes.NewBuffer(body))
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, uuid.New()))
	rec := httptest.NewRecorder()

	handler.ImportTree(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing title, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportHandlerExtraFieldsRejected(t *testing.T) {
	svc := service.NewExportService(
		newStubExportTreeRepo(), newStubExportNodeRepo(), newStubExportEdgeRepo(), nil,
	)
	handler := NewExportHandler(svc)

	// Using unknown field to trigger DisallowUnknownFields.
	body := bytes.NewBufferString(`{"tree":{"title":"Test"},"nodes":[{"id":"` +
		uuid.New().String() + `","content":"hi","contentFormat":"markdown","nodeType":"message"}],
		"edges":[],"version":1,"unknownField":"boom"}`)
	req := httptest.NewRequest(http.MethodPost, "/trees/import", body)
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, uuid.New()))
	rec := httptest.NewRecorder()

	handler.ImportTree(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown field, got %d: %s", rec.Code, rec.Body.String())
	}
}
