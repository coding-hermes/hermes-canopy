package mls

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestErrorSentinelValues(t *testing.T) {
	tests := []struct {
		err error
		msg string
	}{
		{ErrMLSGroupNotFound, "mls: group not found"},
		{ErrNotGroupMember, "mls: profile is not a group member"},
		{ErrEpochMismatch, "mls: ciphertext epoch does not match group epoch"},
		{ErrKeyPackageExpired, "mls: key package is expired"},
		{ErrProposalRejected, "mls: proposal rejected"},
		{ErrCommitFailed, "mls: commit failed"},
		{ErrDecryptionFailed, "mls: decryption failed"},
		{ErrGroupStateCorrupt, "mls: persisted group state is corrupt"},
		{ErrInvalidCredential, "mls: credential is invalid"},
		{ErrMemberAlreadyInGroup, "mls: profile is already a group member"},
		{ErrMemberNotInGroup, "mls: profile is not in the group"},
		{ErrUnauthorizedCommit, "mls: caller is not authorized to commit proposals"},
		{ErrWelcomeUndelivered, "mls: welcome has not been acknowledged"},
	}

	for _, tc := range tests {
		t.Run(tc.err.Error(), func(t *testing.T) {
			if tc.err.Error() != tc.msg {
				t.Fatalf("Error() = %q, want %q", tc.err.Error(), tc.msg)
			}
			if !errors.Is(tc.err, tc.err) {
				t.Fatal("errors.Is(sentinel, sentinel) is false")
			}
			if tc.err == nil {
				t.Fatal("error sentinel is nil")
			}
		})
	}
}

func TestMLSEventTypeConstants(t *testing.T) {
	tests := []struct {
		eventType MLSEventType
		want      string
	}{
		{EventGroupCreated, "group_created"},
		{EventGroupJoined, "group_joined"},
		{EventMemberAdded, "member_added"},
		{EventMemberRemoved, "member_removed"},
		{EventGroupEpochAdvanced, "group_epoch_advanced"},
		{EventKeyPackageExpiring, "key_package_expiring"},
		{EventWelcomeMessage, "welcome_message"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if string(tc.eventType) != tc.want {
				t.Fatalf("MLSEventType = %q, want %q", string(tc.eventType), tc.want)
			}
		})
	}
}

func TestEd25519KeyPair_Serialization(t *testing.T) {
	kp := Ed25519KeyPair{
		PublicKey:  []byte("public-key-bytes-are-here!!!!!!"),
		PrivateKey: []byte("private-key-should-be-hidden!!!!!"),
	}

	data, err := json.Marshal(kp)
	if err != nil {
		t.Fatalf("json.Marshal(Ed25519KeyPair): %v", err)
	}

	// PublicKey should be present
	var decoded struct {
		PublicKey []byte `json:"public_key"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if string(decoded.PublicKey) != string(kp.PublicKey) {
		t.Fatalf("public_key = %q, want %q", string(decoded.PublicKey), string(kp.PublicKey))
	}

	// PrivateKey must NOT be in JSON output
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, exists := raw["private_key"]; exists {
		t.Fatal("private_key was serialized to JSON, but has json:\"-\" tag")
	}
	if _, exists := raw["PrivateKey"]; exists {
		t.Fatal("PrivateKey (Go field name) was serialized to JSON")
	}
}

func TestMLSGroup_JSONTags(t *testing.T) {
	g := MLSGroup{
		ID:          []byte("test-group-id"),
		WorkspaceID: uuid.MustParse("00000000-0000-7000-8000-000000000001"),
		CipherSuite: "MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519",
		Epoch:       42,
		TreeHash:    []byte("tree-hash-bytes"),
	}

	data, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("json.Marshal(MLSGroup): %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	expectedFields := []string{"id", "workspace_id", "cipher_suite", "epoch", "tree_hash", "created_at", "updated_at"}
	for _, f := range expectedFields {
		if _, exists := raw[f]; !exists {
			t.Errorf("MLSGroup missing JSON field %q", f)
		}
	}

	// Verify no Go-exported names leak
	for key := range raw {
		switch key {
		case "id", "workspace_id", "cipher_suite", "epoch", "tree_hash", "created_at", "updated_at":
			// ok
		default:
			t.Errorf("unexpected JSON key %q", key)
		}
	}
}

func TestMLSCiphertext_JSONTags(t *testing.T) {
	ct := MLSCiphertext{
		GroupID:         []byte("gid"),
		Epoch:           1,
		ContentType:     "application",
		Ciphertext:      []byte("encrypted"),
		Nonce:           []byte("nonce"),
		SenderLeafIndex: 5,
		WireFormat:      "mls_ciphertext_v1",
	}

	data, err := json.Marshal(ct)
	if err != nil {
		t.Fatalf("json.Marshal(MLSCiphertext): %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	expectedFields := []string{"group_id", "epoch", "content_type", "ciphertext", "nonce", "sender_leaf_index", "wire_format"}
	for _, f := range expectedFields {
		if _, exists := raw[f]; !exists {
			t.Errorf("MLSCiphertext missing JSON field %q", f)
		}
	}
}

func TestMLSEvent_JSONTags(t *testing.T) {
	evt := MLSEvent{
		Type:        EventGroupCreated,
		WorkspaceID: uuid.MustParse("00000000-0000-7000-8000-000000000001"),
		Timestamp:   time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC),
		Payload:     json.RawMessage(`{"k":"v"}`),
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("json.Marshal(MLSEvent): %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	expectedFields := []string{"type", "workspace_id", "actor_profile_id", "timestamp", "payload"}
	for _, f := range expectedFields {
		if _, exists := raw[f]; !exists {
			t.Errorf("MLSEvent missing JSON field %q", f)
		}
	}
}

func TestMLSEvent_TargetProfileJSON(t *testing.T) {
	targetID := uuid.MustParse("00000000-0000-7000-8000-000000000002")
	evt := MLSEvent{
		Type:            EventMemberRemoved,
		TargetProfileID: &targetID,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, exists := raw["target_profile_id"]; !exists {
		t.Fatal("target_profile_id missing when set")
	}

	// Without target, omitempty should suppress it
	evt2 := MLSEvent{Type: EventGroupCreated}
	data2, err := json.Marshal(evt2)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var raw2 map[string]any
	if err := json.Unmarshal(data2, &raw2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, exists := raw2["target_profile_id"]; exists {
		t.Fatal("target_profile_id should be omitted when nil")
	}
}
