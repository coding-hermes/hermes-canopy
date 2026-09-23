package service

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestNodeDetailUnmarshalJSONMetadata(t *testing.T) {
	legacyMetadata := []byte(`{"legacy":true}`)
	legacyWire := `{"metadata":"` + base64.StdEncoding.EncodeToString(legacyMetadata) + `"}`

	tests := []struct {
		name    string
		wire    string
		want    string
		wantNil bool
		wantErr bool
	}{
		{
			name: "native object",
			wire: `{"metadata":{"pinned":true,"labels":["important"]}}`,
			want: `{"pinned":true,"labels":["important"]}`,
		},
		{
			name: "legacy base64 string",
			wire: legacyWire,
			want: string(legacyMetadata),
		},
		{
			name:    "null",
			wire:    `{"metadata":null}`,
			wantNil: true,
		},
		{
			name:    "missing",
			wire:    `{}`,
			wantNil: true,
		},
		{
			name:    "invalid legacy metadata JSON",
			wire:    `{"metadata":"` + base64.StdEncoding.EncodeToString([]byte("not json")) + `"}`,
			wantErr: true,
		},
		{
			name:    "invalid legacy base64",
			wire:    `{"metadata":"not-base64"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got NodeDetail
			err := json.Unmarshal([]byte(tt.wire), &got)
			if (err != nil) != tt.wantErr {
				t.Fatalf("UnmarshalJSON error = %v, wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.wantNil {
				if got.Metadata != nil {
					t.Fatalf("Metadata = %s, want nil", got.Metadata)
				}
				return
			}
			if string(got.Metadata) != tt.want {
				t.Fatalf("Metadata = %s, want %s", got.Metadata, tt.want)
			}
		})
	}
}

func TestNodeDetailJSONMetadataInvalidAndNilMarshalAsObject(t *testing.T) {
	for name, metadata := range map[string][]byte{
		"nil":     nil,
		"invalid": []byte("not json"),
	} {
		t.Run(name, func(t *testing.T) {
			wire, err := json.Marshal(NodeDetail{Metadata: metadata})
			if err != nil {
				t.Fatalf("MarshalJSON: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(wire, &fields); err != nil {
				t.Fatalf("decode wire: %v", err)
			}
			if got := string(fields["metadata"]); got != `{}` {
				t.Fatalf("metadata wire = %s, want {}", got)
			}
		})
	}
}
