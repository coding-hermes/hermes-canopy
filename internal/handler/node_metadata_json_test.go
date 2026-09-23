package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

func TestNodeResponseMetadataIsNativeJSON(t *testing.T) {
	nodeID := uuid.New()
	rr := httptest.NewRecorder()
	writeJSON(rr, 200, service.NodeDetail{
		ID:            nodeID,
		Content:       "message body",
		ContentFormat: "markdown",
		NodeType:      "message",
		SequenceNum:   7,
		Metadata:      []byte(`{"pinned":true,"label":"x"}`),
		Depth:         2,
		ChildCount:    1,
	})

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode node response: %v; body=%s", err, rr.Body.Bytes())
	}

	var metadata map[string]any
	if err := json.Unmarshal(body["metadata"], &metadata); err != nil {
		t.Fatalf("metadata is not a JSON object: %v; raw=%s", err, body["metadata"])
	}
	if metadata["pinned"] != true || metadata["label"] != "x" {
		t.Fatalf("metadata = %#v, want pinned=true and label=x", metadata)
	}
	if string(body["id"]) != `"`+nodeID.String()+`"` || string(body["content"]) != `"message body"` ||
		string(body["sequenceNum"]) != "7" || string(body["depth"]) != "2" || string(body["childCount"]) != "1" {
		t.Fatalf("non-metadata node fields changed: %s", rr.Body.Bytes())
	}
}

func TestNodeResponseMetadataPreservesJSONShapes(t *testing.T) {
	for name, raw := range map[string]string{
		"object": `{"nested":{"ok":true}}`,
		"array":  `[{"value":1}]`,
		"null":   `null`,
	} {
		t.Run(name, func(t *testing.T) {
			var body map[string]json.RawMessage
			rr := httptest.NewRecorder()
			writeJSON(rr, 200, service.NodeDetail{Metadata: []byte(raw)})
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode node response: %v; body=%s", err, rr.Body.Bytes())
			}
			if got := string(body["metadata"]); got != raw {
				t.Fatalf("metadata = %s, want %s", got, raw)
			}
		})
	}
}

func TestNodeResponseMetadataEmptyIsObject(t *testing.T) {
	for name, raw := range map[string][]byte{
		"nil":        nil,
		"empty":      {},
		"whitespace": []byte("   "),
	} {
		t.Run(name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			writeJSON(rr, 200, service.NodeDetail{Metadata: raw})

			var body map[string]json.RawMessage
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode node response: %v; body=%s", err, rr.Body.Bytes())
			}
			var metadata map[string]any
			if err := json.Unmarshal(body["metadata"], &metadata); err != nil {
				t.Fatalf("empty metadata is not a JSON object: %v; raw=%s", err, body["metadata"])
			}
			if metadata == nil {
				t.Fatal("empty metadata decoded as null, want an object")
			}
		})
	}
}

func TestNodeResponseMetadataInvalidJSONFailsMarshal(t *testing.T) {
	_, err := json.Marshal(service.NodeDetail{Metadata: []byte(`{"not-json"`)})
	if err == nil {
		t.Fatal("invalid stored metadata marshaled successfully")
	}
}
