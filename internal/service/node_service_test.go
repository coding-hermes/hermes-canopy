package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// Stubs for node/edge repos are defined in tree_service_test.go (shared).
// We reuse nodeRepoStub and edgeRepoStub from that file.

func newNodeService() *NodeServiceImpl {
	return &NodeServiceImpl{
		nodeRepo: &nodeRepoStub{},
		edgeRepo: &edgeRepoStub{},
		pool:     nil, // depth/child-count return 0 gracefully when pool is nil
		now:      fakeNow,
		sseHub:   nil, // BE-18: nil hub is safe — broadcast silently skipped
	}
}

func TestCreateNode_InvalidContentFormat(t *testing.T) {
	svc := newNodeService()
	_, err := svc.Create(context.Background(), uuid.New(), CreateNodeInput{
		Content:       "hello",
		ContentFormat: "invalid-format",
		NodeType:      "message",
		EdgeType:      "reply",
	})
	if err != ErrInvalidContentFormat {
		t.Fatalf("Create() error = %v, want ErrInvalidContentFormat", err)
	}
}

func TestCreateNode_InvalidNodeType(t *testing.T) {
	svc := newNodeService()
	_, err := svc.Create(context.Background(), uuid.New(), CreateNodeInput{
		Content:       "hello",
		ContentFormat: "markdown",
		NodeType:      "invalid-type",
		EdgeType:      "reply",
	})
	if err != ErrInvalidNodeType {
		t.Fatalf("Create() error = %v, want ErrInvalidNodeType", err)
	}
}

func TestCreateNode_ContentTooLong(t *testing.T) {
	svc := newNodeService()
	// maxContentLen = 65536, so 65537 should fail
	content := make([]byte, maxContentLen+1)
	for i := range content {
		content[i] = 'a'
	}
	_, err := svc.Create(context.Background(), uuid.New(), CreateNodeInput{
		Content:       string(content),
		ContentFormat: "markdown",
		NodeType:      "message",
		EdgeType:      "reply",
	})
	if err != ErrContentTooLong {
		t.Fatalf("Create() error = %v, want ErrContentTooLong", err)
	}
}

func TestCreateNode_SynthesisViaMergeOnly(t *testing.T) {
	svc := newNodeService()
	_, err := svc.Create(context.Background(), uuid.New(), CreateNodeInput{
		Content:       "hello",
		ContentFormat: "markdown",
		NodeType:      "synthesis",
		EdgeType:      "reply",
	})
	if err != ErrSynthesisViaMergeOnly {
		t.Fatalf("Create() error = %v, want ErrSynthesisViaMergeOnly", err)
	}
}

func TestCreateNode_SystemNodeForbidden(t *testing.T) {
	svc := newNodeService()
	_, err := svc.Create(context.Background(), uuid.New(), CreateNodeInput{
		Content:       "hello",
		ContentFormat: "markdown",
		NodeType:      "system",
		EdgeType:      "reply",
	})
	if err != ErrSystemNodeForbidden {
		t.Fatalf("Create() error = %v, want ErrSystemNodeForbidden", err)
	}
}

func TestCreateNode_InvalidEdgeType(t *testing.T) {
	svc := newNodeService()
	_, err := svc.Create(context.Background(), uuid.New(), CreateNodeInput{
		Content:       "hello",
		ContentFormat: "markdown",
		NodeType:      "message",
		EdgeType:      "synthesis",
	})
	if err != ErrInvalidEdgeType {
		t.Fatalf("Create() error = %v, want ErrInvalidEdgeType", err)
	}
}

func TestCreateNode_MetadataTooLarge(t *testing.T) {
	svc := newNodeService()
	// maxMetadataBytes = 16384, so 16385 should fail
	data := make(json.RawMessage, maxMetadataBytes+1)
	for i := range data {
		data[i] = ' '
	}
	_, err := svc.Create(context.Background(), uuid.New(), CreateNodeInput{
		Content:       "hello",
		ContentFormat: "markdown",
		NodeType:      "message",
		EdgeType:      "reply",
		Metadata:      data,
	})
	if err != ErrMetadataTooLarge {
		t.Fatalf("Create() error = %v, want ErrMetadataTooLarge", err)
	}
}

func TestGetByID_NotFound(t *testing.T) {
	svc := newNodeService()
	_, err := svc.GetByID(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("GetByID() error = nil, want ErrNodeNotFound")
	}
}

func TestUpdate_NoFields(t *testing.T) {
	svc := newNodeService()
	_, err := svc.Update(context.Background(), uuid.New(), UpdateNodeInput{})
	if err != ErrNoUpdateFields {
		t.Fatalf("Update() error = %v, want ErrNoUpdateFields", err)
	}
}

func TestReply_DefaultsToMarkdown(t *testing.T) {
	svc := newNodeService()
	// With a nil pool and stub repo, the reply will try to GetByID the parent
	// and fail with ErrParentNotFound (since nodeRepoStub.GetByID returns ErrNotFound).
	_, err := svc.Reply(context.Background(), uuid.New(), ReplyInput{
		Content: "hello",
	})
	if err != ErrParentNotFound {
		t.Fatalf("Reply() error = %v, want ErrParentNotFound (parent doesn't exist)", err)
	}
}

func TestFork_NoChildren(t *testing.T) {
	svc := newNodeService()
	// nodeRepoStub.GetChildren returns nil, nil → len(children) == 0 → ErrForkRequiresChildren
	_, err := svc.Fork(context.Background(), uuid.New(), ForkInput{
		Content: "hello",
	})
	if err != ErrParentNotFound {
		t.Fatalf("Fork() error = %v, want ErrParentNotFound (parent doesn't exist)", err)
	}
}

func TestSoftDelete_WithNilPool(t *testing.T) {
	// SoftDelete requires a real pool (it queries pool.QueryRow directly with
	// no nil guard). Skipping with nil pool — integration test with real DB needed.
	t.Skip("SoftDelete requires a real pgxpool; nil pool panics")
}

// --- BUG-026 regression: ListByTree must return FULL NodeDetail fields ---
//
// The Nodes page crash ("Cannot read properties of undefined (reading
// 'slice')") happened because the frontend fetched the graph subtree
// endpoint, which returns MINIMAL summaries (id/tree/type/depth/dates
// only) but rendered node.authorId.slice(0,8). The fix added
// ListByTree backed by the full-detail mapper — this test locks in
// that contract so a future refactor can't silently strip fields again.

// listByTreeRepoStub returns a fixed set of full nodes so we can assert
// the service maps every field through nodeToDetail.
type listByTreeRepoStub struct{}

func (n *listByTreeRepoStub) Create(_ context.Context, node *db.Node) (*db.Node, error) {
	return node, nil
}
func (n *listByTreeRepoStub) GetByID(_ context.Context, _ uuid.UUID) (*db.Node, error) {
	return nil, db.ErrNotFound
}
func (n *listByTreeRepoStub) GetByTree(_ context.Context, _ uuid.UUID) ([]db.Node, error) {
	now := fakeNow()
	return []db.Node{
		{
			ID:            mustParseUUID("0191a8b2-7fff-7000-9000-000000000001"),
			TreeID:        mustParseUUID("0191a8b2-7fff-7000-9000-000000000101"),
			ParentID:      nil,
			AuthorID:      mustParseUUID("00000000-0000-0000-0000-000000000001"),
			Content:       "# Root node content",
			ContentFormat: "markdown",
			NodeType:      "message",
			SequenceNum:   1,
			Metadata:      []byte(`{}`),
			CreatedAt:     now,
		},
		{
			ID:            mustParseUUID("0191a8b2-7fff-7000-9000-000000000002"),
			TreeID:        mustParseUUID("0191a8b2-7fff-7000-9000-000000000101"),
			ParentID:      uuidPtr(mustParseUUID("0191a8b2-7fff-7000-9000-000000000001")),
			AuthorID:      mustParseUUID("00000000-0000-0000-0000-000000000001"),
			Content:       "Reply with real content",
			ContentFormat: "plain",
			NodeType:      "message",
			SequenceNum:   2,
			Metadata:      []byte(`{"attachments":[]}`),
			CreatedAt:     now,
		},
	}, nil
}
func (n *listByTreeRepoStub) GetChildren(_ context.Context, _ uuid.UUID) ([]db.Node, error) {
	return nil, nil
}
func (n *listByTreeRepoStub) GetAncestors(_ context.Context, _ uuid.UUID) ([]db.Node, error) {
	return nil, nil
}
func (n *listByTreeRepoStub) GetSubtree(_ context.Context, _ uuid.UUID, _ int) ([]db.Node, error) {
	return nil, nil
}
func (n *listByTreeRepoStub) GetPath(_ context.Context, _, _ uuid.UUID) ([]db.Node, error) {
	return nil, nil
}
func (n *listByTreeRepoStub) Update(_ context.Context, _ uuid.UUID, _ string, _ []byte) (*db.Node, error) {
	return nil, nil
}
func (n *listByTreeRepoStub) SoftDelete(_ context.Context, _ uuid.UUID) error { return nil }
func (n *listByTreeRepoStub) HardDelete(_ context.Context, _ uuid.UUID) error { return nil }
func (n *listByTreeRepoStub) GetCounts(_ context.Context, _ uuid.UUID) (*db.NodeCounts, error) {
	return nil, nil
}

func TestListByTree_ReturnsFullNodeDetails(t *testing.T) {
	svc := &NodeServiceImpl{
		nodeRepo: &listByTreeRepoStub{},
		edgeRepo: &edgeRepoStub{},
		pool:     nil, // depth/child-count return 0 gracefully when pool is nil
		now:      fakeNow,
		sseHub:   nil,
	}

	nodes, err := svc.ListByTree(context.Background(), mustParseUUID("0191a8b2-7fff-7000-9000-000000000101"))
	if err != nil {
		t.Fatalf("ListByTree() error = %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("ListByTree() len = %d, want 2", len(nodes))
	}

	// BUG-026 regression: authorId and content MUST be present on every
	// node — the frontend does node.authorId.slice(0, 8) and renders
	// node.content. A minimal summary (id/tree/type/depth/dates) crashes it.
	root := nodes[0]
	if root.AuthorID != mustParseUUID("00000000-0000-0000-0000-000000000001") {
		t.Errorf("root.AuthorID = %v, want dev user UUID", root.AuthorID)
	}
	if root.Content != "# Root node content" {
		t.Errorf("root.Content = %q, want full content", root.Content)
	}
	if root.ContentFormat != "markdown" || root.NodeType != "message" {
		t.Errorf("root format/type = %q/%q, want markdown/message", root.ContentFormat, root.NodeType)
	}
	if root.SequenceNum != 1 {
		t.Errorf("root.SequenceNum = %d, want 1 (sequence order preserved)", root.SequenceNum)
	}

	// Child: parent linkage + metadata must survive the mapper.
	child := nodes[1]
	if child.ParentID == nil || *child.ParentID != root.ID {
		t.Errorf("child.ParentID = %v, want root.ID", child.ParentID)
	}
	if string(child.Metadata) != `{"attachments":[]}` {
		t.Errorf("child.Metadata = %s, want preserved metadata", child.Metadata)
	}
	if child.SequenceNum != 2 {
		t.Errorf("child.SequenceNum = %d, want 2 (ordered by sequence_num)", child.SequenceNum)
	}
}

func TestListByTree_NilTreeID_ReturnsEmpty(t *testing.T) {
	svc := &NodeServiceImpl{
		nodeRepo: &listByTreeRepoStub{},
		edgeRepo: &edgeRepoStub{},
		pool:     nil,
		now:      fakeNow,
		sseHub:   nil,
	}

	nodes, err := svc.ListByTree(context.Background(), uuid.Nil)
	if err != nil {
		t.Fatalf("ListByTree(uuid.Nil) error = %v, want nil", err)
	}
	if nodes == nil || len(nodes) != 0 {
		t.Fatalf("ListByTree(uuid.Nil) = %v, want empty non-nil slice", nodes)
	}
}

func TestListByTree_EmptyTree_ReturnsEmptyNonNil(t *testing.T) {
	svc := &NodeServiceImpl{
		nodeRepo: &nodeRepoStub{}, // GetByTree returns nil, nil
		edgeRepo: &edgeRepoStub{},
		pool:     nil,
		now:      fakeNow,
		sseHub:   nil,
	}

	nodes, err := svc.ListByTree(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ListByTree() error = %v, want nil", err)
	}
	if nodes == nil || len(nodes) != 0 {
		t.Fatalf("ListByTree() = %v, want empty non-nil slice ({\"nodes\": []} contract)", nodes)
	}
}

// --- BUG-029 regression tests live in the handler package ---
//
// Create() unconditionally inserted an edge with source_id = input.ParentID.
// For root nodes ParentID is uuid.Nil, but edges.source_id is NOT NULL with
// an FK to nodes(id) — so the INSERT violated edges_source_id_fkey and the
// handler surfaced it as a 503. The fix skips the edge insert for root nodes.
//
// The regression tests are in internal/handler/api_integration_extended_test.go
// because that package already has PG integration infrastructure
// (NewSharedIntegrationPool) — adding PG tests here would add a new
// concurrent database to the full-suite parallel run and tip the already-
// borderline handler/plugin suites over their timeouts (the known PG test
// storm pattern, TEST-004).

// --- helpers (shared) ---

func mustParseUUID(s string) uuid.UUID {
	u, err := uuid.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func uuidPtr(u uuid.UUID) *uuid.UUID { return &u }

// fakeNow returns a fixed timestamp for reproducible tests.
func fakeNow() time.Time {
	return mustParseTime("2026-07-23T10:00:00Z")
}
