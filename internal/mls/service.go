package mls

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/db"
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

	// Derive distinct encryption and signing keys from the admin identity
	// key pair using domain separation. Until a real MLS library provides
	// HPKE/DHKEM key material, this ensures cryptographic separation
	// (BUG-013).
	encKey := deriveKey("mls-encryption-v1", adminKeyPair.PublicKey)
	sigKey := deriveKey("mls-signature-v1", adminKeyPair.PublicKey)

	member := &db.MLSGroupMember{
		ProfileID:           creatorProfileID,
		GroupID:             groupID,
		MLSIdentity:         []byte(creatorProfileID.String()),
		EncryptionPublicKey: encKey,
		SignaturePublicKey:  sigKey,
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

// deriveKey derives a 32-byte key from src material using domain separation.
// The domain tag ensures encryption and signing keys are cryptographically
// distinct even when derived from the same source material (BUG-013).
func deriveKey(domain string, src []byte) []byte {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write(src)
	return h.Sum(nil)
}

func (s *MLSServiceImpl) JoinGroup(ctx context.Context, workspaceID, profileID uuid.UUID, keyPackage MLSKeyPackage, welcomeBytes []byte) error {
	grp, err := s.groups.GetByWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}

	// Use stub keys to satisfy NOT NULL constraints until a real MLS
	// library provides actual key material from the KeyPackage/Welcome.
	// Derive distinct encryption and signing keys using domain separation
	// to ensure cryptographic separation (BUG-013).
	stubKey := make([]byte, 32)
	if len(keyPackage.KeyPackageBytes) > 0 {
		copy(stubKey, keyPackage.KeyPackageBytes[:min(32, len(keyPackage.KeyPackageBytes))])
	}

	member := &db.MLSGroupMember{
		ProfileID:           profileID,
		GroupID:             grp.ID,
		MLSIdentity:         []byte(profileID.String()),
		EncryptionPublicKey: deriveKey("mls-encryption-v1", stubKey),
		SignaturePublicKey:  deriveKey("mls-signature-v1", stubKey),
		CredentialType:      "basic",
		AddedAt:             time.Now().UTC(),
		LastActive:          time.Now().UTC(),
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

	member, err := s.members.GetByProfile(ctx, grp.ID, profileID)
	if err != nil {
		return MLSCiphertext{}, ErrNotGroupMember
	}

	// Derive AES-256 key from the group's encryption key material.
	// In a full MLS implementation, this would use the group's epoch secret.
	aesKey := sha256.Sum256(append(grp.ID, member.EncryptionPublicKey...))

	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return MLSCiphertext{}, fmt.Errorf("mls: new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return MLSCiphertext{}, fmt.Errorf("mls: new gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return MLSCiphertext{}, err
	}

	// Encrypt: ciphertext = nonce || AES-GCM(plaintext, associated-data=groupID+epoch)
	aad := append(grp.ID, byte(grp.Epoch>>24), byte(grp.Epoch>>16), byte(grp.Epoch>>8), byte(grp.Epoch))
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)

	// Prepend nonce to ciphertext for storage/transmission
	out := make([]byte, len(nonce)+len(ciphertext))
	copy(out, nonce)
	copy(out[len(nonce):], ciphertext)

	return MLSCiphertext{
		GroupID:         grp.ID,
		Epoch:           grp.Epoch,
		ContentType:     "application",
		Ciphertext:      out,
		SenderLeafIndex: uint32(member.LeafIndex),
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

	member, err := s.members.GetByProfile(ctx, grp.ID, profileID)
	if err != nil {
		return nil, ErrNotGroupMember
	}

	// Derive the same AES-256 key from the group key material.
	aesKey := sha256.Sum256(append(grp.ID, member.EncryptionPublicKey...))

	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return nil, fmt.Errorf("mls: new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("mls: new gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext.Ciphertext) < nonceSize {
		return nil, fmt.Errorf("mls: ciphertext too short: %d bytes, need at least %d", len(ciphertext.Ciphertext), nonceSize)
	}

	nonce, ct := ciphertext.Ciphertext[:nonceSize], ciphertext.Ciphertext[nonceSize:]

	aad := append(grp.ID, byte(grp.Epoch>>24), byte(grp.Epoch>>16), byte(grp.Epoch>>8), byte(grp.Epoch))
	plaintext, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("mls: gcm open: %w", err)
	}

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
