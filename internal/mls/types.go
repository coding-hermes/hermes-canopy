// Package mls owns Canopy's MLS (Messaging Layer Security, RFC 9420)
// encryption layer per SPEC-FTR-03. The domain types, error sentinels,
// event constants, and the two service interfaces below are copied
// verbatim from SPEC-FTR-03 §3.1 so that any future wire-format or
// audit mapping can rely on stable identifiers.
//
// Implementation note: until a pure-Go RFC 9420 implementation is
// selected and locked, the concrete *ServiceImpl and PGKeyPackageManager
// in this package use placeholder cryptographic operations. Wire types
// and error semantics are stable; ciphertext bytes are not. See
// SPEC-FTR-03 Design Decisions 2 and 10.
package mls

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Ed25519KeyPair holds one profile identity keypair. PrivateKey is never serialized.
type Ed25519KeyPair struct {
	PublicKey  ed25519.PublicKey  `json:"public_key"`
	PrivateKey ed25519.PrivateKey `json:"-"`
}

// MLSGroup is the persisted public summary of one workspace MLS group.
type MLSGroup struct {
	ID          []byte    `json:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	CipherSuite string    `json:"cipher_suite"`
	Epoch       uint64    `json:"epoch"`
	TreeHash    []byte    `json:"tree_hash"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MLSGroupState contains encrypted state required to resume an MLS group.
type MLSGroupState struct {
	Group              MLSGroup          `json:"group"`
	EncryptedState     []byte            `json:"-"`
	RatchetTree        []byte            `json:"ratchet_tree"`
	ExporterSecretHash []byte            `json:"exporter_secret_hash"`
	Members            []MLSGroupMember  `json:"members"`
}

// MLSGroupMember is the authenticated member representation at an MLS leaf.
type MLSGroupMember struct {
	ProfileID           uuid.UUID `json:"profile_id"`
	MLSIdentity         []byte    `json:"mls_identity"`
	EncryptionPublicKey []byte    `json:"encryption_public_key"`
	SignaturePublicKey  []byte    `json:"signature_public_key"`
	CredentialType      string    `json:"credential_type"`
	AddedAt             time.Time `json:"added_at"`
	LastActive          time.Time `json:"last_active"`
}

// MLSCiphertext is an opaque RFC 9420 application-message representation.
type MLSCiphertext struct {
	GroupID         []byte `json:"group_id"`
	Epoch           uint64 `json:"epoch"`
	ContentType     string `json:"content_type"`
	Ciphertext      []byte `json:"ciphertext"`
	Nonce           []byte `json:"nonce"`
	SenderLeafIndex uint32 `json:"sender_leaf_index"`
	WireFormat      string `json:"wire_format"` // "mls_ciphertext_v1"
}

// MLSCredential is the MLS credential data bound to a Canopy profile UUIDv7.
type MLSCredential struct {
	ProfileID          uuid.UUID `json:"profile_id"`
	Identity           []byte    `json:"identity"`
	SignaturePublicKey []byte    `json:"signature_public_key"`
	CredentialType     string    `json:"credential_type"`
}

// MLSKeyPackage is a short-lived RFC 9420 key package plus Canopy metadata.
type MLSKeyPackage struct {
	ID              uuid.UUID `json:"id"`
	ProfileID       uuid.UUID `json:"profile_id"`
	KeyPackageBytes []byte    `json:"key_package_bytes"`
	Hash            []byte    `json:"hash"`
	CipherSuite     string    `json:"cipher_suite"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// MLSService owns workspace group state and cryptographic transitions.
type MLSService interface {
	CreateGroup(ctx context.Context, workspaceID, creatorProfileID uuid.UUID, adminKeyPair Ed25519KeyPair) (*MLSGroup, error)
	JoinGroup(ctx context.Context, workspaceID, profileID uuid.UUID, keyPackage MLSKeyPackage, welcomeBytes []byte) error
	LeaveGroup(ctx context.Context, workspaceID, profileID uuid.UUID) error
	RemoveMember(ctx context.Context, workspaceID, profileID, callerProfileID uuid.UUID) error
	Encrypt(ctx context.Context, workspaceID, profileID uuid.UUID, plaintext []byte) (MLSCiphertext, error)
	Decrypt(ctx context.Context, workspaceID, profileID uuid.UUID, ciphertext MLSCiphertext) ([]byte, error)
	AddExternalProposal(ctx context.Context, workspaceID, profileID uuid.UUID, proposalBytes []byte) error
	CommitProposals(ctx context.Context, workspaceID, profileID uuid.UUID) ([]byte, error) // returns commit bytes
	GetEpochSecret(ctx context.Context, workspaceID uuid.UUID) ([]byte, error)            // exporter secret
	GetGroupState(ctx context.Context, workspaceID uuid.UUID) (*MLSGroupState, error)
}

// KeyPackageManager manages 24-hour member key packages.
type KeyPackageManager interface {
	GenerateKeyPackage(ctx context.Context, profileID uuid.UUID, credential MLSCredential, keyPair Ed25519KeyPair) (MLSKeyPackage, error)
	GetKeyPackage(ctx context.Context, profileID uuid.UUID) (MLSKeyPackage, error)
	ExpireKeyPackage(ctx context.Context, keyPackageID uuid.UUID) error
}

// Errors are stable service-level classifications for HTTP, SSE, and audit mapping.
var (
	ErrMLSGroupNotFound     = errors.New("mls: group not found")
	ErrNotGroupMember       = errors.New("mls: profile is not a group member")
	ErrEpochMismatch        = errors.New("mls: ciphertext epoch does not match group epoch")
	ErrKeyPackageExpired    = errors.New("mls: key package is expired")
	ErrProposalRejected     = errors.New("mls: proposal rejected")
	ErrCommitFailed         = errors.New("mls: commit failed")
	ErrDecryptionFailed     = errors.New("mls: decryption failed")
	ErrGroupStateCorrupt    = errors.New("mls: persisted group state is corrupt")
	ErrInvalidCredential    = errors.New("mls: credential is invalid")
	ErrMemberAlreadyInGroup = errors.New("mls: profile is already a group member")
	ErrMemberNotInGroup     = errors.New("mls: profile is not in the group")
	ErrUnauthorizedCommit   = errors.New("mls: caller is not authorized to commit proposals")
	ErrWelcomeUndelivered   = errors.New("mls: welcome has not been acknowledged")
)

// MLSEventType enumerates workspace-scoped SSE event types.
type MLSEventType string

const (
	EventGroupCreated       MLSEventType = "group_created"
	EventGroupJoined        MLSEventType = "group_joined"
	EventMemberAdded        MLSEventType = "member_added"
	EventMemberRemoved      MLSEventType = "member_removed"
	EventGroupEpochAdvanced MLSEventType = "group_epoch_advanced"
	EventKeyPackageExpiring MLSEventType = "key_package_expiring"
	EventWelcomeMessage     MLSEventType = "welcome_message"
)

// MLSEvent is delivered through the authenticated workspace SSE stream.
type MLSEvent struct {
	Type            MLSEventType    `json:"type"`
	WorkspaceID     uuid.UUID       `json:"workspace_id"`
	ActorProfileID  uuid.UUID       `json:"actor_profile_id"`
	TargetProfileID *uuid.UUID      `json:"target_profile_id,omitempty"`
	Timestamp       time.Time       `json:"timestamp"`
	Payload         json.RawMessage `json:"payload"`
}