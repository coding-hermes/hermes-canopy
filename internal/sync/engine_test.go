package sync

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDefaultEngineConfig(t *testing.T) {
	cfg := DefaultEngineConfig()
	if !cfg.SnapshotOnCommit {
		t.Error("expected SnapshotOnCommit to default to true")
	}
}

func TestBuildNodeEventPayload_WithAllFields(t *testing.T) {
	now := time.Now().UTC()
	parentID := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	edgeID := uuid.MustParse("00000000-0000-0000-0000-00000000000a")

	m := NodeMutation{
		Type:          MutNodeAdded,
		TreeID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		NodeID:        uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		ActorID:       uuid.MustParse("00000000-0000-0000-0000-00000000000f"),
		Content:       "Hello world",
		ContentFormat: "text",
		NodeType:      "message",
		ParentID:      &parentID,
		EdgeID:        edgeID,
		EdgeType:      "reply",
		SequenceNum:   42,
		Timestamp:     now,
	}

	raw := buildNodeEventPayload(m)
	if raw == nil {
		t.Fatal("expected non-nil payload")
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if data["mutation_type"] != "node_added" {
		t.Errorf("expected mutation_type 'node_added', got %v", data["mutation_type"])
	}
	if data["node_id"] != "00000000-0000-0000-0000-000000000002" {
		t.Errorf("unexpected node_id: %v", data["node_id"])
	}
	if data["edge_id"] != "00000000-0000-0000-0000-00000000000a" {
		t.Errorf("unexpected edge_id: %v", data["edge_id"])
	}
	if data["parent_id"] != "00000000-0000-0000-0000-000000000003" {
		t.Errorf("unexpected parent_id: %v", data["parent_id"])
	}
	if data["content"] != "Hello world" {
		t.Errorf("unexpected content: %v", data["content"])
	}
	if _, ok := data["timestamp"]; !ok {
		t.Error("expected timestamp field")
	}
}

func TestBuildNodeEventPayload_Minimal(t *testing.T) {
	m := NodeMutation{
		Type:    MutNodeRemoved,
		NodeID:  uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		ActorID: uuid.MustParse("00000000-0000-0000-0000-000000000002"),
	}

	raw := buildNodeEventPayload(m)
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// Empty content and nil parent should be omitted
	if _, ok := data["content"]; ok {
		t.Error("content should be omitted when empty")
	}
	if _, ok := data["parent_id"]; ok {
		t.Error("parent_id should be omitted when nil")
	}
	if _, ok := data["edge_id"]; ok {
		t.Error("edge_id should be omitted when uuid.Nil")
	}
}

func TestEdgeIDPtr(t *testing.T) {
	m := NodeMutation{EdgeID: uuid.Nil}
	if ptr := edgeIDPtr(m); ptr != nil {
		t.Error("expected nil for uuid.Nil edge ID")
	}

	m.EdgeID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	if ptr := edgeIDPtr(m); ptr == nil || *ptr != m.EdgeID {
		t.Error("expected non-nil ptr to edge ID")
	}
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		input any
		want  int64
		ok    bool
	}{
		{float64(42), 42, true},
		{int64(99), 99, true},
		{int(7), 7, true},
		{"not a number", 0, false},
		{nil, 0, false},
	}
	for _, tt := range tests {
		got, ok := toInt64(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Errorf("toInt64(%v) = (%d, %v), want (%d, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}
