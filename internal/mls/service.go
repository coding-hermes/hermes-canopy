package mls

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
)

// MLSServiceImpl implements MLSService using PostgreSQL-backed repos.
// Cryptographic operations use placeholders until a pure-Go RFC 9420
// library is selected (SPE-FTR-03 Design Decision 2).
type MLSServiceImpl struct {
	pool    *pgxpool.Pool
	groups  db.MLSGroupRepo
	members db.MLSMemberRepo
	kps     db.MLSKeyPackageRepo
	props   db.MLSPendingProposalRepo
	sseHub  sse.SSEHub
}

// NewMLSService creates a new MLSServiceImpl with the supplied dependencies.
func NewMLSService(pool *pgxpool.Pool, groups db.MLSGroupRepo, members db.MLSMemberRepo, kps db.MLSKeyPackageRepo, props db.MLSPendingProposalRepo, hub sse.SSEHub) *MLSServiceImpl {
	return &MLSServiceImpl{pool: pool, groups: groups, members: members, kps: kps, props: props, sseHub: hub}
}

// broadcastMLSEvent converts an mLSEvent to an SSE hub broadcast on the
// workspace stream. The workspaceID is used as the treeID for SSE routing
// (one workspace → one tree per SPEC-FTR-03 Design Decision 3). Nil-safe.
func (s *MLSServiceImpl) broadcastMLSEvent(ctx context.Context, ev MLSEvent) {
	if s.sseHub == nil {
		return
	}
	data, err := json.Marshal(ev.Payload)
	if err != nil {
		log.Ctx(ctx).Warn().Err(err).Str("type", string(ev.Type)).Msg("mls: marshal SSE payload")
		return
	}
	_ = s.sseHub.Broadcast(ev.WorkspaceID, sse.SSEEvent{
		TreeID:    ev.WorkspaceID,
		Type:      string(ev.Type),
		Data:      data,
		Timestamp: ev.Timestamp,
		ActorID:   ev.ActorProfileID,
	})
}

// mustMarshalPayload marshals a map to json.RawMessage for use as MLSEvent payload.
// Panics on marshal error (programming error if it fails).
func mustMarshalPayload(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("mls: marshal event payload: " + err.Error())
	}
	return json.RawMessage(b)
}

func (s *MLSServiceImpl) CreateGroup(ctx context.Context, workspaceID, creatorProfileID uuid.UUID, adminKeyPair Ed25519KeyPair) (*MLSGroup, error) {
	groupID := make([]byte, 32)
	if _, err := rand.Read(groupID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	group := &db.MLSGroup{
		ID:          groupID,
		WorkspaceID: workspaceID,
		CipherSuite: "MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519",
		Epoch:       0,
		TreeHash:    groupID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.groups.Create(ctx, group); err != nil {
		return nil, err
	}

	member := &db.MLSGroupMember{
		ProfileID:           creatorProfileID,
		GroupID:             groupID,
		MLSIdentity:         []byte(creatorProfileID.String()),
		EncryptionPublicKey: adminKeyPair.PublicKey,
		SignaturePublicKey:  adminKeyPair.PublicKey,
		CredentialType:      "basic",
		AddedAt:             now,
		LastActive:          now,
	}

	if err := s.members.Add(ctx, groupID, member); err != nil {
		return nil, err
	}

	// SSE broadcast — group_created
	s.broadcastMLSEvent(ctx, MLSEvent{
		Type:           EventGroupCreated,
		WorkspaceID:    workspaceID,
		ActorProfileID: creatorProfileID,
		Timestamp:      now,
		Payload:        mustMarshalPayload(map[string]any{"group_id": groupID}),
	})

	return &MLSGroup{
		ID:          groupID,
		WorkspaceID: workspaceID,
		CipherSuite: group.CipherSuite,
		Epoch:       0,
		TreeHash:    groupID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (s *MLSServiceImpl) JoinGroup(ctx context.Context, workspaceID, profileID uuid.UUID, keyPackage MLSKeyPackage, welcomeBytes []byte) error {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}

	member := &db.MLSGroupMember{
		ProfileID:  profileID,
		GroupID:    grp.ID,
		MLSIdentity: []byte(profileID.String()),
		AddedAt:    time.Now().UTC(),
		LastActive: time.Now().UTC(),
	}

	if err := s.members.Add(ctx, grp.ID, member); err != nil {
		return err
	}

	if err := s.groups.UpdateEpoch(ctx, grp.ID, grp.Epoch+1, grp.TreeHash); err != nil {
		return err
	}

	now := time.Now().UTC()

	// SSE broadcast — member_added + welcome_message
	s.broadcastMLSEvent(ctx, MLSEvent{
		Type:            EventMemberAdded,
		WorkspaceID:     workspaceID,
		ActorProfileID:  profileID,
		TargetProfileID: &profileID,
		Timestamp:       now,
		Payload:         mustMarshalPayload(map[string]any{"profile_id": profileID.String()}),
	})
	// Welcome delivery per SPEC-FTR-03 Design Decision 16
	if len(welcomeBytes) > 0 {
		s.broadcastMLSEvent(ctx, MLSEvent{
			Type:            EventWelcomeMessage,
			WorkspaceID:     workspaceID,
			ActorProfileID:  profileID,
			TargetProfileID: &profileID,
			Timestamp:       now,
			Payload:         mustMarshalPayload(map[string]any{"welcome_bytes": welcomeBytes}),
		})
	}

	return nil
}

func (s *MLSServiceImpl) LeaveGroup(ctx context.Context, workspaceID, profileID uuid.UUID) error {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}

	if err := s.members.Remove(ctx, grp.ID, profileID); err != nil {
		return err
	}

	if err := s.groups.UpdateEpoch(ctx, grp.ID, grp.Epoch+1, grp.TreeHash); err != nil {
		return err
	}

	// SSE broadcast — member_removed
	now := time.Now().UTC()
	s.broadcastMLSEvent(ctx, MLSEvent{
		Type:            EventMemberRemoved,
		WorkspaceID:     workspaceID,
		ActorProfileID:  profileID,
		TargetProfileID: &profileID,
		Timestamp:       now,
		Payload:         mustMarshalPayload(map[string]any{"profile_id": profileID.String(), "reason": "left"}),
	})

	return nil
}

func (s *MLSServiceImpl) RemoveMember(ctx context.Context, workspaceID, profileID, callerProfileID uuid.UUID) error {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}

	if err := s.members.Remove(ctx, grp.ID, profileID); err != nil {
		return err
	}

	if err := s.groups.UpdateEpoch(ctx, grp.ID, grp.Epoch+1, grp.TreeHash); err != nil {
		return err
	}

	// SSE broadcast — member_removed (by admin)
	now := time.Now().UTC()
	s.broadcastMLSEvent(ctx, MLSEvent{
		Type:            EventMemberRemoved,
		WorkspaceID:     workspaceID,
		ActorProfileID:  callerProfileID,
		TargetProfileID: &profileID,
		Timestamp:       now,
		Payload:         mustMarshalPayload(map[string]any{"profile_id": profileID.String(), "reason": "removed_by_admin"}),
	})

	return nil
}

func (s *MLSServiceImpl) Encrypt(ctx context.Context, workspaceID, profileID uuid.UUID, plaintext []byte) (MLSCiphertext, error) {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return MLSCiphertext{}, err
	}

	if _, err := s.members.GetByProfile(ctx, grp.ID, profileID); err != nil {
		return MLSCiphertext{}, ErrNotGroupMember
	}

	// Placeholder — no real MLS library available yet
	ciphertext := make([]byte, len(plaintext))
	copy(ciphertext, plaintext)
	none := make([]byte, 12)
	_, _ = rand.Read(none)

	return MLSCiphertext{
		GroupID:         grp.ID,
		Epoch:           grp.Epoch,
		ContentType:     "application",
		Ciphertext:      ciphertext,
		Nonce:           none,
		SenderLeafIndex: 0,
		WireFormat:      "mls_ciphertext_v1",
	}, nil
}

func (s *MLSServiceImpl) Decrypt(ctx context.Context, workspaceID, profileID uuid.UUID, ciphertext MLSCiphertext) ([]byte, error) {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	if ciphertext.Epoch != grp.Epoch {
		return nil, ErrEpochMismatch
	}

	if _, err := s.members.GetByProfile(ctx, grp.ID, profileID); err != nil {
		return nil, ErrNotGroupMember
	}

	plaintext := make([]byte, len(ciphertext.Ciphertext))
	copy(plaintext, ciphertext.Ciphertext)
	return plaintext, nil
}

func (s *MLSServiceImpl) AddExternalProposal(ctx context.Context, workspaceID, profileID uuid.UUID, proposalBytes []byte) error {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	return s.props.Create(ctx, grp.ID, "external_add", profileID, proposalBytes)
}

func (s *MLSServiceImpl) CommitProposals(ctx context.Context, workspaceID, profileID uuid.UUID) ([]byte, error) {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	props, err := s.props.ListByGroup(ctx, grp.ID)
	if err != nil {
		return nil, err
	}

	if len(props) == 0 {
		return nil, ErrProposalRejected
	}

	if err := s.groups.UpdateEpoch(ctx, grp.ID, grp.Epoch+1, grp.TreeHash); err != nil {
		return nil, err
	}

	if err := s.props.DeleteAll(ctx, grp.ID); err != nil {
		return nil, err
	}

	// SSE broadcast — group_epoch_advanced
	s.broadcastMLSEvent(ctx, MLSEvent{
		Type:           EventGroupEpochAdvanced,
		WorkspaceID:    workspaceID,
		ActorProfileID: profileID,
		Timestamp:      time.Now().UTC(),
		Payload:        mustMarshalPayload(map[string]any{"new_epoch": grp.Epoch + 1, "committed_proposals": len(props)}),
	})

	return []byte("placeholder-commit-bytes"), nil
}

func (s *MLSServiceImpl) GetEpochSecret(ctx context.Context, workspaceID uuid.UUID) ([]byte, error) {
	_, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return append([]byte(nil), secret...), nil
}

func (s *MLSServiceImpl) GetGroupState(ctx context.Context, workspaceID uuid.UUID) (*MLSGroupState, error) {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	members, err := s.members.ListByGroup(ctx, grp.ID)
	if err != nil {
		return nil, err
	}

	domainMembers := make([]MLSGroupMember, len(members))
	for i, m := range members {
		domainMembers[i] = MLSGroupMember{
			ProfileID:           m.ProfileID,
			MLSIdentity:         m.MLSIdentity,
			EncryptionPublicKey: m.EncryptionPublicKey,
			SignaturePublicKey:  m.SignaturePublicKey,
			CredentialType:      m.CredentialType,
			AddedAt:             m.AddedAt,
			LastActive:          m.LastActive,
		}
	}

	return &MLSGroupState{
		Group: MLSGroup{
			ID:          grp.ID,
			WorkspaceID: grp.WorkspaceID,
			CipherSuite: grp.CipherSuite,
			Epoch:       grp.Epoch,
			TreeHash:    grp.TreeHash,
			CreatedAt:   grp.CreatedAt,
			UpdatedAt:   grp.UpdatedAt,
		},
		Members: domainMembers,
	}, nil
}
