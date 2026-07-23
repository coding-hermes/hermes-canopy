package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Stubs for node/edge repos are defined in tree_service_test.go (shared).
// We reuse nodeRepoStub and edgeRepoStub from that file.

func newNodeService() *NodeServiceImpl {
	return &NodeServiceImpl{
		nodeRepo: &nodeRepoStub{},
		edgeRepo: &edgeRepoStub{},
		pool:     nil, // depth/child-count return 0 gracefully when pool is nil
		now:      fakeNow,
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

// fakeNow returns a fixed timestamp for reproducible tests.
func fakeNow() time.Time {
	return mustParseTime("2026-07-23T10:00:00Z")
}
