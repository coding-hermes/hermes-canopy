package sync

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestComputeSnapshotHash_Deterministic(t *testing.T) {
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	n1 := NodeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), SeqNum: 1, CreatedAtEpoch: now.UnixMilli(), ParentID: "nil", ContentHash: "abc", ContentFormat: "text", NodeType: "message"}
	n2 := NodeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), SeqNum: 2, CreatedAtEpoch: now.UnixMilli(), ParentID: "00000000-0000-0000-0000-000000000001", ContentHash: "def", ContentFormat: "markdown", NodeType: "reply"}

	h1 := ComputeSnapshotHash([]NodeDigest{n1, n2}, nil)
	h2 := ComputeSnapshotHash([]NodeDigest{n2, n1}, nil)

	if h1 != h2 {
		t.Error("hash should be deterministic regardless of input order")
	}
}

func TestComputeSnapshotHash_Empty(t *testing.T) {
	h := ComputeSnapshotHash(nil, nil)
	if h == "" {
		t.Error("empty tree should still produce a hash")
	}
	if len(h) != 64 {
		t.Errorf("expected 64-char SHA256 hex, got %d chars: %q", len(h), h)
	}
}

func TestComputeSnapshotHash_WithEdges(t *testing.T) {
	n1 := NodeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), SeqNum: 1, ContentHash: "a", ContentFormat: "text", NodeType: "msg", ParentID: "nil"}
	e1 := EdgeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-00000000000a"), SourceID: n1.ID, TargetID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), EdgeType: "reply"}

	h := ComputeSnapshotHash([]NodeDigest{n1}, []EdgeDigest{e1})
	if h == "" {
		t.Error("hash with edges should not be empty")
	}
}

func TestNodeFromParts(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	now := time.Now()
	parent := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	n := NodeFromParts(id, 1, now, &parent, "hash123", "text", "message")

	if n.ID != id {
		t.Errorf("expected ID %v, got %v", id, n.ID)
	}
	if n.SeqNum != 1 {
		t.Errorf("expected SeqNum 1, got %d", n.SeqNum)
	}
	if n.CreatedAtEpoch != now.UnixMilli() {
		t.Errorf("expected CreatedAtEpoch %d, got %d", now.UnixMilli(), n.CreatedAtEpoch)
	}
	if n.ParentID != parent.String() {
		t.Errorf("expected ParentID %q, got %q", parent.String(), n.ParentID)
	}
	if n.ContentHash != "hash123" {
		t.Errorf("expected ContentHash 'hash123', got %q", n.ContentHash)
	}
	if n.ContentFormat != "text" {
		t.Errorf("expected ContentFormat 'text', got %q", n.ContentFormat)
	}
	if n.NodeType != "message" {
		t.Errorf("expected NodeType 'message', got %q", n.NodeType)
	}
}

func TestNodeFromParts_NilParent(t *testing.T) {
	n := NodeFromParts(uuid.Nil, 0, time.Time{}, nil, "", "", "")
	if n.ParentID != "nil" {
		t.Errorf("expected ParentID 'nil' for nil parent, got %q", n.ParentID)
	}
}

func TestEdgeFromParts(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	src := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	tgt := uuid.MustParse("00000000-0000-0000-0000-000000000003")

	e := EdgeFromParts(id, src, tgt, "reply")
	if e.ID != id || e.SourceID != src || e.TargetID != tgt || e.EdgeType != "reply" {
		t.Errorf("unexpected EdgeDigest: %+v", e)
	}
}
