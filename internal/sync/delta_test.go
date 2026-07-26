package sync

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

func TestComputeDelta_NilToSnapshot(t *testing.T) {
	_, err := ComputeDelta(nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil toSnapshot")
	}
}

func TestComputeDelta_FirstSync(t *testing.T) {
	now := time.Now()
	toHash := "abc123"
	toSnap := &db.TreeSnapshot{Hash: toHash, NodeCount: 2, EdgeCount: 1}

	n1 := NodeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), SeqNum: 1, CreatedAtEpoch: now.UnixMilli(), ParentID: "nil", ContentHash: "hash1", ContentFormat: "text", NodeType: "message"}
	n2 := NodeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), SeqNum: 2, CreatedAtEpoch: now.UnixMilli(), ParentID: "00000000-0000-0000-0000-000000000001", ContentHash: "hash2", ContentFormat: "markdown", NodeType: "reply"}
	toNodes := []NodeDigest{n1, n2}

	e1 := EdgeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-00000000000a"), SourceID: n1.ID, TargetID: n2.ID, EdgeType: "reply"}
	toEdges := []EdgeDigest{e1}

	delta, err := ComputeDelta(nil, toSnap, nil, toNodes, nil, toEdges)
	if err != nil {
		t.Fatalf("ComputeDelta failed: %v", err)
	}

	if delta.FromHash != "" {
		t.Errorf("expected empty FromHash for first sync, got %q", delta.FromHash)
	}
	if delta.ToHash != toHash {
		t.Errorf("expected ToHash %q, got %q", toHash, delta.ToHash)
	}
	if delta.NodeCount != 2 {
		t.Errorf("expected NodeCount 2, got %d", delta.NodeCount)
	}
	if delta.EdgeCount != 1 {
		t.Errorf("expected EdgeCount 1, got %d", delta.EdgeCount)
	}
	if len(delta.AddedNodes) != 2 {
		t.Errorf("expected 2 added nodes, got %d", len(delta.AddedNodes))
	}
	if len(delta.AddedEdges) != 1 {
		t.Errorf("expected 1 added edge, got %d", len(delta.AddedEdges))
	}
	if len(delta.RemovedNodeIDs) != 0 {
		t.Errorf("expected 0 removed nodes, got %d", len(delta.RemovedNodeIDs))
	}
	if len(delta.RemovedEdgeIDs) != 0 {
		t.Errorf("expected 0 removed edges, got %d", len(delta.RemovedEdgeIDs))
	}
}

func TestComputeDelta_AddedRemovedChanged(t *testing.T) {
	now := time.Now()
	fromHash := "fromhash"
	toHash := "tohash"

	keepNode := NodeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), SeqNum: 1, CreatedAtEpoch: now.UnixMilli(), ParentID: "nil", ContentHash: "same", ContentFormat: "text", NodeType: "message"}
	removedNode := NodeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), SeqNum: 2, CreatedAtEpoch: now.UnixMilli(), ParentID: "00000000-0000-0000-0000-000000000001", ContentHash: "gone", ContentFormat: "text", NodeType: "message"}
	changedNode := NodeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-000000000003"), SeqNum: 3, CreatedAtEpoch: now.UnixMilli(), ParentID: "nil", ContentHash: "oldhash", ContentFormat: "text", NodeType: "message"}
	addedNode := NodeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-000000000004"), SeqNum: 4, CreatedAtEpoch: now.UnixMilli(), ParentID: "00000000-0000-0000-0000-000000000001", ContentHash: "new", ContentFormat: "markdown", NodeType: "reply"}

	changedNodeNew := changedNode
	changedNodeNew.ContentHash = "newhash"

	fromSnap := &db.TreeSnapshot{Hash: fromHash}
	toSnap := &db.TreeSnapshot{Hash: toHash}
	fromNodes := []NodeDigest{keepNode, removedNode, changedNode}
	toNodes := []NodeDigest{keepNode, changedNodeNew, addedNode}

	keepEdge := EdgeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-00000000000a"), SourceID: keepNode.ID, TargetID: changedNode.ID, EdgeType: "reply"}
	removedEdge := EdgeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-00000000000b"), SourceID: keepNode.ID, TargetID: removedNode.ID, EdgeType: "reply"}
	addedEdge := EdgeDigest{ID: uuid.MustParse("00000000-0000-0000-0000-00000000000c"), SourceID: keepNode.ID, TargetID: addedNode.ID, EdgeType: "synthesis"}

	fromEdges := []EdgeDigest{keepEdge, removedEdge}
	toEdges := []EdgeDigest{keepEdge, addedEdge}

	delta, err := ComputeDelta(fromSnap, toSnap, fromNodes, toNodes, fromEdges, toEdges)
	if err != nil {
		t.Fatalf("ComputeDelta failed: %v", err)
	}

	if delta.FromHash != fromHash {
		t.Errorf("expected FromHash %q, got %q", fromHash, delta.FromHash)
	}
	if delta.ToHash != toHash {
		t.Errorf("expected ToHash %q, got %q", toHash, delta.ToHash)
	}

	// Check added nodes
	if len(delta.AddedNodes) != 1 {
		t.Errorf("expected 1 added node, got %d", len(delta.AddedNodes))
	} else if _, ok := delta.AddedNodes[addedNode.ID]; !ok {
		t.Error("added node not found in AddedNodes")
	}

	// Check removed nodes
	if len(delta.RemovedNodeIDs) != 1 {
		t.Errorf("expected 1 removed node, got %d", len(delta.RemovedNodeIDs))
	} else if delta.RemovedNodeIDs[0] != removedNode.ID {
		t.Errorf("expected removed node %v, got %v", removedNode.ID, delta.RemovedNodeIDs[0])
	}

	// Check changed nodes
	if len(delta.ChangedNodes) != 1 {
		t.Errorf("expected 1 changed node, got %d", len(delta.ChangedNodes))
	} else if cn, ok := delta.ChangedNodes[changedNodeNew.ID]; !ok {
		t.Error("changed node not found in ChangedNodes")
	} else if cn.ContentHash != "newhash" {
		t.Errorf("expected content hash 'newhash', got %q", cn.ContentHash)
	}

	// Check added edges
	if len(delta.AddedEdges) != 1 {
		t.Errorf("expected 1 added edge, got %d", len(delta.AddedEdges))
	} else if _, ok := delta.AddedEdges[addedEdge.ID]; !ok {
		t.Error("added edge not found in AddedEdges")
	}

	// Check removed edges
	if len(delta.RemovedEdgeIDs) != 1 {
		t.Errorf("expected 1 removed edge, got %d", len(delta.RemovedEdgeIDs))
	} else if delta.RemovedEdgeIDs[0] != removedEdge.ID {
		t.Errorf("expected removed edge %v, got %v", removedEdge.ID, delta.RemovedEdgeIDs[0])
	}

	// Keep node and edge should NOT be in any change set
	if _, ok := delta.AddedNodes[keepNode.ID]; ok {
		t.Error("keep node should not be in AddedNodes")
	}
	if _, ok := delta.ChangedNodes[keepNode.ID]; ok {
		t.Error("keep node should not be in ChangedNodes")
	}
}

func TestComputeDelta_EmptyTree(t *testing.T) {
	fromSnap := &db.TreeSnapshot{Hash: "from"}
	toSnap := &db.TreeSnapshot{Hash: "to", NodeCount: 0, EdgeCount: 0}

	delta, err := ComputeDelta(fromSnap, toSnap, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("ComputeDelta failed: %v", err)
	}

	if len(delta.AddedNodes) != 0 {
		t.Errorf("expected 0 added nodes, got %d", len(delta.AddedNodes))
	}
	if len(delta.RemovedNodeIDs) != 0 {
		t.Errorf("expected 0 removed nodes, got %d", len(delta.RemovedNodeIDs))
	}
	if len(delta.ChangedNodes) != 0 {
		t.Errorf("expected 0 changed nodes, got %d", len(delta.ChangedNodes))
	}
	if len(delta.AddedEdges) != 0 {
		t.Errorf("expected 0 added edges, got %d", len(delta.AddedEdges))
	}
}

func TestNodeChanged(t *testing.T) {
	tests := []struct {
		name string
		a, b NodeDigest
		want bool
	}{
		{"same hash and fields", NodeDigest{ContentHash: "a", ContentFormat: "text", NodeType: "msg", ParentID: "nil"}, NodeDigest{ContentHash: "a", ContentFormat: "text", NodeType: "msg", ParentID: "nil"}, false},
		{"different content hash", NodeDigest{ContentHash: "a"}, NodeDigest{ContentHash: "b"}, true},
		{"different format", NodeDigest{ContentHash: "a", ContentFormat: "text"}, NodeDigest{ContentHash: "a", ContentFormat: "markdown"}, true},
		{"different type", NodeDigest{ContentHash: "a", ContentFormat: "text", NodeType: "msg"}, NodeDigest{ContentHash: "a", ContentFormat: "text", NodeType: "card"}, true},
		{"different parent", NodeDigest{ContentHash: "a", ParentID: "nil"}, NodeDigest{ContentHash: "a", ParentID: "abc"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeChanged(tt.a, tt.b); got != tt.want {
				t.Errorf("nodeChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToCompactNode(t *testing.T) {
	n := NodeDigest{
		ID:             uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		SeqNum:         42,
		CreatedAtEpoch: 1000,
		ParentID:       "nil",
		ContentHash:    "abc",
		ContentFormat:  "text",
		NodeType:       "message",
	}
	cn := toCompactNode(n)
	if cn.SeqNum != 42 || cn.CreatedAt != "1000" || cn.ParentID != "nil" || cn.ContentHash != "abc" || cn.ContentFormat != "text" || cn.NodeType != "message" {
		t.Errorf("unexpected CompactNode: %+v", cn)
	}
}

func TestToCompactEdge(t *testing.T) {
	e := EdgeDigest{
		ID:       uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		SourceID: uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		TargetID: uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		EdgeType: "reply",
	}
	ce := toCompactEdge(e)
	if ce.SourceID != "00000000-0000-0000-0000-000000000002" || ce.TargetID != "00000000-0000-0000-0000-000000000003" || ce.EdgeType != "reply" {
		t.Errorf("unexpected CompactEdge: %+v", ce)
	}
}
