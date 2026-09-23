package db

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestNodeJSONMetadataIsNativeAndRoundTrips(t *testing.T) {
	original := Node{
		ID:       uuid.New(),
		TreeID:   uuid.New(),
		Content:  "export me",
		Metadata: []byte(`{"pinned":true,"labels":["important"]}`),
	}

	wire, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal node: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatalf("decode node: %v; wire=%s", err, wire)
	}
	var metadata map[string]any
	if err := json.Unmarshal(fields["metadata"], &metadata); err != nil {
		t.Fatalf("metadata is not native JSON: %v; raw=%s", err, fields["metadata"])
	}
	if metadata["pinned"] != true {
		t.Fatalf("metadata = %#v, want pinned=true", metadata)
	}

	var decoded Node
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal node: %v", err)
	}
	if string(decoded.Metadata) != string(original.Metadata) {
		t.Fatalf("decoded metadata = %s, want %s", decoded.Metadata, original.Metadata)
	}
	if decoded.ID != original.ID || decoded.TreeID != original.TreeID || decoded.Content != original.Content {
		t.Fatalf("decoded node fields changed: %#v", decoded)
	}
}

func TestNodeJSONUnmarshalAcceptsLegacyBase64Metadata(t *testing.T) {
	metadata := []byte(`{"legacy":true}`)
	legacy := `{"id":"` + uuid.New().String() + `","metadata":"` +
		base64.StdEncoding.EncodeToString(metadata) + `"}`

	var decoded Node
	if err := json.Unmarshal([]byte(legacy), &decoded); err != nil {
		t.Fatalf("unmarshal legacy node: %v", err)
	}
	if string(decoded.Metadata) != string(metadata) {
		t.Fatalf("decoded legacy metadata = %s, want %s", decoded.Metadata, metadata)
	}
}
