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

func TestNodeJSONUnmarshalPreservesNullAndRejectsInvalidMetadata(t *testing.T) {
	tests := []struct {
		name    string
		wire    string
		wantNil bool
		wantErr bool
	}{
		{name: "null", wire: `{"metadata":null}`, wantNil: true},
		{name: "missing", wire: `{}`, wantNil: true},
		{name: "invalid base64", wire: `{"metadata":"not-base64"}`, wantErr: true},
		{name: "invalid decoded JSON", wire: `{"metadata":"` + base64.StdEncoding.EncodeToString([]byte("not json")) + `"}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var decoded Node
			err := json.Unmarshal([]byte(tt.wire), &decoded)
			if (err != nil) != tt.wantErr {
				t.Fatalf("UnmarshalJSON error = %v, wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.wantNil && decoded.Metadata != nil {
				t.Fatalf("decoded metadata = %s, want nil", decoded.Metadata)
			}
		})
	}
}
