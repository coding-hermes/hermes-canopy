package mls

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
)

// Compile-time assertion that MLSEventBridge satisfies MLSService.
var _ MLSService = (*MLSEventBridge)(nil)

// MLSEventBridge wraps an MLSService and broadcasts workspace-scoped MLS
// events through the SSE hub after mutating operations per SPEC-FTR-03.
//
// The bridge is a transparent decorator: it forwards every call to the
// underlying delegate and additionally broadcasts an MLSEvent through the
// SSE hub for the five state-changing operations (CreateGroup, JoinGroup,
// LeaveGroup, RemoveMember, CommitProposals). Non-mutating operations
// (Encrypt, Decrypt, GetGroupState, etc.) are forwarded without broadcast.
//
// MLS operations are workspace-scoped, so the workspace UUID is used as the
// SSE broadcast TreeID — this lets workspace event subscribers receive MLS
// events through the same SSE infrastructure.
type MLSEventBridge struct {
	delegate MLSService
	hub      sse.SSEHub
}

// NewMLSEventBridge wraps delegate so that MLS events are broadcast through
// hub after each mutating operation. The returned bridge satisfies the
// MLSService interface.
func NewMLSEventBridge(delegate MLSService, hub sse.SSEHub) *MLSEventBridge {
	return &MLSEventBridge{delegate: delegate, hub: hub}
}

// Hub returns the SSE hub used by the bridge. The MLS events SSE endpoint
// uses this to subscribe clients to workspace-scoped events.
func (b *MLSEventBridge) Hub() sse.SSEHub {
	return b.hub
}

// MLSEventToSSE converts an MLSEvent into an SSEEvent suitable for
// broadcasting through the SSE hub. The MLSEvent is JSON-marshalled into
// Data, the workspace ID becomes the SSE TreeID (routing key), and the
// event type is prefixed with "mls:" to avoid collisions with tree-scoped
// SSE event types.
func MLSEventToSSE(evt MLSEvent) sse.SSEEvent {
	data, err := json.Marshal(evt)
	if err != nil {
		data = json.RawMessage(`{"error":"failed to marshal mls event"}`)
	}
	return sse.SSEEvent{
		Type:      "mls:" + string(evt.Type),
		Data:      data,
		Timestamp: evt.Timestamp,
		TreeID:    evt.WorkspaceID,
		ActorID:   evt.ActorProfileID,
	}
}

// broadcast constructs an MLSEvent and sends it through the SSE hub.
func (b *MLSEventBridge) broadcast(
	evtType MLSEventType,
	workspaceID, actorID uuid.UUID,
	targetID *uuid.UUID,
	payload any,
) {
	var payloadRaw json.RawMessage
	if payload != nil {
		if raw, err := json.Marshal(payload); err == nil {
			payloadRaw = raw
		}
	}
	evt := MLSEvent{
		Type:            evtType,
		WorkspaceID:     workspaceID,
		ActorProfileID:  actorID,
		TargetProfileID: targetID,
		Timestamp:       time.Now().UTC(),
		Payload:         payloadRaw,
	}
	b.hub.Broadcast(workspaceID, MLSEventToSSE(evt))
}

// --- Broadcasting MLSService methods ----------------------------------------

// CreateGroup delegates to the underlying service, then broadcasts
// EventGroupCreated with the created group as payload.
func (b *MLSEventBridge) CreateGroup(ctx context.Context, workspaceID, creatorProfileID uuid.UUID, adminKeyPair Ed25519KeyPair) (*MLSGroup, error) {
	group, err := b.delegate.CreateGroup(ctx, workspaceID, creatorProfileID, adminKeyPair)
	if err != nil {
		return nil, err
	}
	b.broadcast(EventGroupCreated, workspaceID, creatorProfileID, nil, group)
	return group, nil
}

// JoinGroup delegates to the underlying service, then broadcasts
// EventGroupJoined.
func (b *MLSEventBridge) JoinGroup(ctx context.Context, workspaceID, profileID uuid.UUID, keyPackage MLSKeyPackage, welcomeBytes []byte) error {
	if err := b.delegate.JoinGroup(ctx, workspaceID, profileID, keyPackage, welcomeBytes); err != nil {
		return err
	}
	target := profileID
	b.broadcast(EventGroupJoined, workspaceID, profileID, &target, nil)
	return nil
}

// LeaveGroup delegates to the underlying service, then broadcasts
// EventMemberRemoved.
func (b *MLSEventBridge) LeaveGroup(ctx context.Context, workspaceID, profileID uuid.UUID) error {
	if err := b.delegate.LeaveGroup(ctx, workspaceID, profileID); err != nil {
		return err
	}
	target := profileID
	b.broadcast(EventMemberRemoved, workspaceID, profileID, &target, nil)
	return nil
}

// RemoveMember delegates to the underlying service, then broadcasts
// EventMemberRemoved with the caller as actor and the removed profile as
// target.
func (b *MLSEventBridge) RemoveMember(ctx context.Context, workspaceID, profileID, callerProfileID uuid.UUID) error {
	if err := b.delegate.RemoveMember(ctx, workspaceID, profileID, callerProfileID); err != nil {
		return err
	}
	target := profileID
	b.broadcast(EventMemberRemoved, workspaceID, callerProfileID, &target, nil)
	return nil
}

// CommitProposals delegates to the underlying service, then broadcasts
// EventGroupEpochAdvanced.
func (b *MLSEventBridge) CommitProposals(ctx context.Context, workspaceID, profileID uuid.UUID) ([]byte, error) {
	commitBytes, err := b.delegate.CommitProposals(ctx, workspaceID, profileID)
	if err != nil {
		return nil, err
	}
	b.broadcast(EventGroupEpochAdvanced, workspaceID, profileID, nil, nil)
	return commitBytes, nil
}

// --- Non-broadcasting delegates ---------------------------------------------

func (b *MLSEventBridge) Encrypt(ctx context.Context, workspaceID, profileID uuid.UUID, plaintext []byte) (MLSCiphertext, error) {
	return b.delegate.Encrypt(ctx, workspaceID, profileID, plaintext)
}

func (b *MLSEventBridge) Decrypt(ctx context.Context, workspaceID, profileID uuid.UUID, ciphertext MLSCiphertext) ([]byte, error) {
	return b.delegate.Decrypt(ctx, workspaceID, profileID, ciphertext)
}

func (b *MLSEventBridge) AddExternalProposal(ctx context.Context, workspaceID, profileID uuid.UUID, proposalBytes []byte) error {
	return b.delegate.AddExternalProposal(ctx, workspaceID, profileID, proposalBytes)
}

func (b *MLSEventBridge) GetEpochSecret(ctx context.Context, workspaceID uuid.UUID) ([]byte, error) {
	return b.delegate.GetEpochSecret(ctx, workspaceID)
}

func (b *MLSEventBridge) GetGroupState(ctx context.Context, workspaceID uuid.UUID) (*MLSGroupState, error) {
	return b.delegate.GetGroupState(ctx, workspaceID)
}
