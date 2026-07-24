package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- Repository Interfaces ---

// MLSGroupRepo persists MLS groups.
type MLSGroupRepo interface {
	Create(ctx context.Context, group *MLSGroup) error
	GetByWorkspace(ctx context.Context, workspaceID uuid.UUID) (*MLSGroup, error)
	UpdateEpoch(ctx context.Context, groupID []byte, epoch uint64, treeHash []byte) error
	Delete(ctx context.Context, groupID []byte) error
}

// MLSMemberRepo persists MLS group members.
type MLSMemberRepo interface {
	Add(ctx context.Context, groupID []byte, member *MLSGroupMember) error
	Remove(ctx context.Context, groupID []byte, profileID uuid.UUID) error
	ListByGroup(ctx context.Context, groupID []byte) ([]MLSGroupMember, error)
	GetByProfile(ctx context.Context, groupID []byte, profileID uuid.UUID) (*MLSGroupMember, error)
}

// MLSKeyPackageRepo persists MLS key packages.
type MLSKeyPackageRepo interface {
	Create(ctx context.Context, kp *MLSKeyPackage) error
	GetLatest(ctx context.Context, profileID uuid.UUID) (*MLSKeyPackage, error)
	Expire(ctx context.Context, id uuid.UUID) error
}

// MLSPendingProposalRepo persists pending MLS proposals.
type MLSPendingProposalRepo interface {
	Create(ctx context.Context, groupID []byte, proposalType string, proposerID uuid.UUID, proposalBytes []byte) error
	ListByGroup(ctx context.Context, groupID []byte) ([]MLSPendingProposal, error)
	DeleteAll(ctx context.Context, groupID []byte) error
}

// --- In-memory types (shared with internal/mls) ---

// MLSGroup mirrors internal/mls.MLSGroup for the data layer.
type MLSGroup struct {
	ID          []byte    `db:"group_id"`
	WorkspaceID uuid.UUID `db:"workspace_id"`
	CipherSuite string    `db:"cipher_suite"`
	Epoch       uint64    `db:"epoch"`
	TreeHash    []byte    `db:"tree_hash_bytes"`
	EncryptedState []byte `db:"encrypted_state"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// MLSGroupMember mirrors internal/mls.MLSGroupMember for the data layer.
type MLSGroupMember struct {
	ProfileID           uuid.UUID `db:"profile_id"`
	GroupID             []byte    `db:"group_id"`
	MLSIdentity         []byte    `db:"mls_identity"`
	EncryptionPublicKey []byte    `db:"encryption_pubkey"`
	SignaturePublicKey  []byte    `db:"signature_pubkey"`
	CredentialType      string    `db:"credential_type"`
	LeafIndex           int       `db:"leaf_index"`
	AddedAt             time.Time `db:"added_at"`
	LastActive          time.Time `db:"last_active"`
}

// MLSKeyPackage mirrors internal/mls.MLSKeyPackage for the data layer.
type MLSKeyPackage struct {
	ID              uuid.UUID `db:"id"`
	ProfileID       uuid.UUID `db:"profile_id"`
	KeyPackageBytes []byte    `db:"key_package_bytes"`
	Hash            []byte    `db:"hash_unique"`
	CipherSuite     string    `db:"ciphersuite"`
	CreatedAt       time.Time `db:"created_at"`
	ExpiresAt       time.Time `db:"expires_at"`
}

// MLSPendingProposal represents a pending MLS proposal.
type MLSPendingProposal struct {
	ID            uuid.UUID `db:"id"`
	GroupID       []byte    `db:"group_id"`
	ProposalBytes []byte    `db:"proposal_bytes"`
	ProposalType  string    `db:"proposal_type"`
	ProposerID    uuid.UUID `db:"proposer_id"`
	CreatedAt     time.Time `db:"created_at"`
}

// --- PG Implementations ---

// PGMLSGroupRepo is the pgx implementation of MLSGroupRepo.
type PGMLSGroupRepo struct {
	pool *pgxpool.Pool
}

func NewPGMLSGroupRepo(pool *pgxpool.Pool) *PGMLSGroupRepo {
	return &PGMLSGroupRepo{pool: pool}
}

func (r *PGMLSGroupRepo) Create(ctx context.Context, group *MLSGroup) error {
	stateJSON, err := json.Marshal(group.EncryptedState)
	if err != nil {
		return fmt.Errorf("mls_group: marshal state: %w", err)
	}

	_, err = r.pool.Exec(ctx,
		`INSERT INTO mls_groups (group_id, workspace_id, cipher_suite, epoch, tree_hash_bytes, encrypted_state, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		group.ID, group.WorkspaceID, group.CipherSuite, group.Epoch, group.TreeHash, stateJSON, group.CreatedAt, group.UpdatedAt)
	if err != nil {
		return fmt.Errorf("mls_group: create: %w", err)
	}
	return nil
}

func (r *PGMLSGroupRepo) GetByWorkspace(ctx context.Context, workspaceID uuid.UUID) (*MLSGroup, error) {
	var stateJSON []byte
	g := &MLSGroup{}
	err := r.pool.QueryRow(ctx,
		`SELECT group_id, workspace_id, cipher_suite, epoch, tree_hash_bytes, encrypted_state, created_at, updated_at
		 FROM mls_groups WHERE workspace_id = $1`, workspaceID).
		Scan(&g.ID, &g.WorkspaceID, &g.CipherSuite, &g.Epoch, &g.TreeHash, &stateJSON, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("mls_group: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("mls_group: get by workspace: %w", err)
	}
	if len(stateJSON) > 0 {
		g.EncryptedState = stateJSON
	}
	return g, nil
}

func (r *PGMLSGroupRepo) UpdateEpoch(ctx context.Context, groupID []byte, epoch uint64, treeHash []byte) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE mls_groups SET epoch = $1, tree_hash_bytes = $2, updated_at = now() WHERE group_id = $3`,
		epoch, treeHash, groupID)
	if err != nil {
		return fmt.Errorf("mls_group: update epoch: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mls_group: %w", ErrNotFound)
	}
	return nil
}

func (r *PGMLSGroupRepo) Delete(ctx context.Context, groupID []byte) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mls_groups WHERE group_id = $1`, groupID)
	if err != nil {
		return fmt.Errorf("mls_group: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mls_group: %w", ErrNotFound)
	}
	return nil
}

// PGMLSMemberRepo is the pgx implementation of MLSMemberRepo.
type PGMLSMemberRepo struct {
	pool *pgxpool.Pool
}

func NewPGMLSMemberRepo(pool *pgxpool.Pool) *PGMLSMemberRepo {
	return &PGMLSMemberRepo{pool: pool}
}

func (r *PGMLSMemberRepo) Add(ctx context.Context, groupID []byte, member *MLSGroupMember) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO mls_group_members (group_id, profile_id, mls_identity, encryption_pubkey, signature_pubkey, credential_type, leaf_index, added_at, last_active)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (group_id, profile_id) DO UPDATE SET
		   mls_identity = EXCLUDED.mls_identity,
		   last_active = now()`,
		groupID, member.ProfileID, member.MLSIdentity, member.EncryptionPublicKey,
		member.SignaturePublicKey, member.CredentialType, 0, member.AddedAt, member.LastActive)
	if err != nil {
		return fmt.Errorf("mls_member: add: %w", err)
	}
	return nil
}

func (r *PGMLSMemberRepo) Remove(ctx context.Context, groupID []byte, profileID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM mls_group_members WHERE group_id = $1 AND profile_id = $2`,
		groupID, profileID)
	if err != nil {
		return fmt.Errorf("mls_member: remove: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mls_member: %w", ErrNotFound)
	}
	return nil
}

func (r *PGMLSMemberRepo) ListByGroup(ctx context.Context, groupID []byte) ([]MLSGroupMember, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT group_id, profile_id, mls_identity, encryption_pubkey, signature_pubkey, credential_type, leaf_index, added_at, last_active
		 FROM mls_group_members WHERE group_id = $1
		 ORDER BY leaf_index`, groupID)
	if err != nil {
		return nil, fmt.Errorf("mls_member: list: %w", err)
	}
	defer rows.Close()

	var members []MLSGroupMember
	for rows.Next() {
		var m MLSGroupMember
		if err := rows.Scan(&m.GroupID, &m.ProfileID, &m.MLSIdentity, &m.EncryptionPublicKey,
			&m.SignaturePublicKey, &m.CredentialType, &m.LeafIndex, &m.AddedAt, &m.LastActive); err != nil {
			return nil, fmt.Errorf("mls_member: scan: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (r *PGMLSMemberRepo) GetByProfile(ctx context.Context, groupID []byte, profileID uuid.UUID) (*MLSGroupMember, error) {
	var m MLSGroupMember
	err := r.pool.QueryRow(ctx,
		`SELECT group_id, profile_id, mls_identity, encryption_pubkey, signature_pubkey, credential_type, leaf_index, added_at, last_active
		 FROM mls_group_members WHERE group_id = $1 AND profile_id = $2`,
		groupID, profileID).
		Scan(&m.GroupID, &m.ProfileID, &m.MLSIdentity, &m.EncryptionPublicKey,
			&m.SignaturePublicKey, &m.CredentialType, &m.LeafIndex, &m.AddedAt, &m.LastActive)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("mls_member: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("mls_member: get by profile: %w", err)
	}
	return &m, nil
}

// PGMLSKeyPackageRepo is the pgx implementation of MLSKeyPackageRepo.
type PGMLSKeyPackageRepo struct {
	pool *pgxpool.Pool
}

func NewPGMLSKeyPackageRepo(pool *pgxpool.Pool) *PGMLSKeyPackageRepo {
	return &PGMLSKeyPackageRepo{pool: pool}
}

func (r *PGMLSKeyPackageRepo) Create(ctx context.Context, kp *MLSKeyPackage) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO mls_key_packages (id, profile_id, key_package_bytes, hash_unique, ciphersuite, created_at, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		kp.ID, kp.ProfileID, kp.KeyPackageBytes, kp.Hash, kp.CipherSuite, kp.CreatedAt, kp.ExpiresAt)
	if err != nil {
		return fmt.Errorf("mls_key_package: create: %w", err)
	}
	return nil
}

func (r *PGMLSKeyPackageRepo) GetLatest(ctx context.Context, profileID uuid.UUID) (*MLSKeyPackage, error) {
	kp := &MLSKeyPackage{}
	err := r.pool.QueryRow(ctx,
		`SELECT id, profile_id, key_package_bytes, hash_unique, ciphersuite, created_at, expires_at
		 FROM mls_key_packages WHERE profile_id = $1 AND expires_at > now()
		 ORDER BY created_at DESC LIMIT 1`, profileID).
		Scan(&kp.ID, &kp.ProfileID, &kp.KeyPackageBytes, &kp.Hash, &kp.CipherSuite, &kp.CreatedAt, &kp.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("mls_key_package: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("mls_key_package: get latest: %w", err)
	}
	return kp, nil
}

func (r *PGMLSKeyPackageRepo) Expire(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE mls_key_packages SET expires_at = now() WHERE id = $1 AND expires_at > now()`, id)
	if err != nil {
		return fmt.Errorf("mls_key_package: expire: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mls_key_package: %w", ErrNotFound)
	}
	return nil
}

// PGMLSPendingProposalRepo is the pgx implementation of MLSPendingProposalRepo.
type PGMLSPendingProposalRepo struct {
	pool *pgxpool.Pool
}

func NewPGMLSPendingProposalRepo(pool *pgxpool.Pool) *PGMLSPendingProposalRepo {
	return &PGMLSPendingProposalRepo{pool: pool}
}

func (r *PGMLSPendingProposalRepo) Create(ctx context.Context, groupID []byte, proposalType string, proposerID uuid.UUID, proposalBytes []byte) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO mls_pending_proposals (group_id, proposal_bytes, proposal_type, proposer_id)
		 VALUES ($1,$2,$3,$4)`,
		groupID, proposalBytes, proposalType, proposerID)
	if err != nil {
		return fmt.Errorf("mls_proposal: create: %w", err)
	}
	return nil
}

func (r *PGMLSPendingProposalRepo) ListByGroup(ctx context.Context, groupID []byte) ([]MLSPendingProposal, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, group_id, proposal_bytes, proposal_type, proposer_id, created_at
		 FROM mls_pending_proposals WHERE group_id = $1 ORDER BY created_at`, groupID)
	if err != nil {
		return nil, fmt.Errorf("mls_proposal: list: %w", err)
	}
	defer rows.Close()

	var props []MLSPendingProposal
	for rows.Next() {
		var p MLSPendingProposal
		if err := rows.Scan(&p.ID, &p.GroupID, &p.ProposalBytes, &p.ProposalType, &p.ProposerID, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("mls_proposal: scan: %w", err)
		}
		props = append(props, p)
	}
	return props, rows.Err()
}

func (r *PGMLSPendingProposalRepo) DeleteAll(ctx context.Context, groupID []byte) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mls_pending_proposals WHERE group_id = $1`, groupID)
	if err != nil {
		return fmt.Errorf("mls_proposal: delete all: %w", err)
	}
	return nil
}
