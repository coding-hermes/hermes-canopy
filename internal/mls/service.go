package mls

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
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
}

// NewMLSService creates a new MLSServiceImpl with the supplied dependencies.
func NewMLSService(pool *pgxpool.Pool, groups db.MLSGroupRepo, members db.MLSMemberRepo, kps db.MLSKeyPackageRepo, props db.MLSPendingProposalRepo) *MLSServiceImpl {
	return &MLSServiceImpl{pool: pool, groups: groups, members: members, kps: kps, props: props}
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

	return s.groups.UpdateEpoch(ctx, grp.ID, grp.Epoch+1, grp.TreeHash)
}

func (s *MLSServiceImpl) LeaveGroup(ctx context.Context, workspaceID, profileID uuid.UUID) error {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}

	if err := s.members.Remove(ctx, grp.ID, profileID); err != nil {
		return err
	}

	return s.groups.UpdateEpoch(ctx, grp.ID, grp.Epoch+1, grp.TreeHash)
}

func (s *MLSServiceImpl) RemoveMember(ctx context.Context, workspaceID, profileID, callerProfileID uuid.UUID) error {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}

	if err := s.members.Remove(ctx, grp.ID, profileID); err != nil {
		return err
	}

	return s.groups.UpdateEpoch(ctx, grp.ID, grp.Epoch+1, grp.TreeHash)
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
