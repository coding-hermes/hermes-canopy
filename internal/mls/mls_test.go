package mls

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// ---------------------------------------------------------------------------
// In-memory stubs for the four DB repo interfaces
// ---------------------------------------------------------------------------

// groupRepoStub is an in-memory MLSGroupRepo keyed by workspaceID.
type groupRepoStub struct {
	groups map[uuid.UUID]*db.MLSGroup
}

func newGroupRepoStub() *groupRepoStub {
	return &groupRepoStub{groups: make(map[uuid.UUID]*db.MLSGroup)}
}

func (s *groupRepoStub) Create(_ context.Context, group *db.MLSGroup) error {
	s.groups[group.WorkspaceID] = group
	return nil
}

func (s *groupRepoStub) GetByWorkspace(_ context.Context, workspaceID uuid.UUID) (*db.MLSGroup, error) {
	g, ok := s.groups[workspaceID]
	if !ok {
		return nil, db.ErrNotFound
	}
	return g, nil
}

func (s *groupRepoStub) UpdateEpoch(_ context.Context, groupID []byte, epoch uint64, treeHash []byte) error {
	for _, g := range s.groups {
		if hex.EncodeToString(g.ID) == hex.EncodeToString(groupID) {
			g.Epoch = epoch
			g.TreeHash = treeHash
			g.UpdatedAt = time.Now().UTC()
			return nil
		}
	}
	return db.ErrNotFound
}

func (s *groupRepoStub) Delete(_ context.Context, groupID []byte) error {
	for wid, g := range s.groups {
		if hex.EncodeToString(g.ID) == hex.EncodeToString(groupID) {
			delete(s.groups, wid)
			return nil
		}
	}
	return db.ErrNotFound
}

// memberRepoStub is an in-memory MLSMemberRepo keyed by hex(groupID).
type memberRepoStub struct {
	members map[string][]*db.MLSGroupMember
}

func newMemberRepoStub() *memberRepoStub {
	return &memberRepoStub{members: make(map[string][]*db.MLSGroupMember)}
}

func (s *memberRepoStub) Add(_ context.Context, groupID []byte, member *db.MLSGroupMember) error {
	key := hex.EncodeToString(groupID)
	s.members[key] = append(s.members[key], member)
	return nil
}

func (s *memberRepoStub) Remove(_ context.Context, groupID []byte, profileID uuid.UUID) error {
	key := hex.EncodeToString(groupID)
	members := s.members[key]
	found := false
	for i, m := range members {
		if m.ProfileID == profileID {
			s.members[key] = append(members[:i], members[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return db.ErrNotFound
	}
	return nil
}

func (s *memberRepoStub) ListByGroup(_ context.Context, groupID []byte) ([]db.MLSGroupMember, error) {
	key := hex.EncodeToString(groupID)
	members, ok := s.members[key]
	if !ok {
		return nil, nil
	}
	out := make([]db.MLSGroupMember, len(members))
	for i, m := range members {
		out[i] = *m
	}
	return out, nil
}

func (s *memberRepoStub) GetByProfile(_ context.Context, groupID []byte, profileID uuid.UUID) (*db.MLSGroupMember, error) {
	key := hex.EncodeToString(groupID)
	for _, m := range s.members[key] {
		if m.ProfileID == profileID {
			return m, nil
		}
	}
	return nil, db.ErrNotFound
}

// keyPackageRepoStub is a no-op stub for MLSKeyPackageRepo (not used by service methods).
type keyPackageRepoStub struct{}

func newKeyPackageRepoStub() *keyPackageRepoStub {
	return &keyPackageRepoStub{}
}

func (s *keyPackageRepoStub) Create(_ context.Context, kp *db.MLSKeyPackage) error {
	return nil
}
func (s *keyPackageRepoStub) GetLatest(_ context.Context, _ uuid.UUID) (*db.MLSKeyPackage, error) {
	return nil, db.ErrNotFound
}
func (s *keyPackageRepoStub) Expire(_ context.Context, _ uuid.UUID) error {
	return nil
}

// proposalRepoStub is an in-memory MLSPendingProposalRepo keyed by hex(groupID).
type proposalRepoStub struct {
	proposals map[string][]*db.MLSPendingProposal
}

func newProposalRepoStub() *proposalRepoStub {
	return &proposalRepoStub{proposals: make(map[string][]*db.MLSPendingProposal)}
}

func (s *proposalRepoStub) Create(_ context.Context, groupID []byte, proposalType string, proposerID uuid.UUID, proposalBytes []byte) error {
	key := hex.EncodeToString(groupID)
	p := &db.MLSPendingProposal{
		ID:            uuid.New(),
		GroupID:       groupID,
		ProposalBytes: proposalBytes,
		ProposalType:  proposalType,
		ProposerID:    proposerID,
		CreatedAt:     time.Now().UTC(),
	}
	s.proposals[key] = append(s.proposals[key], p)
	return nil
}

func (s *proposalRepoStub) ListByGroup(_ context.Context, groupID []byte) ([]db.MLSPendingProposal, error) {
	key := hex.EncodeToString(groupID)
	props, ok := s.proposals[key]
	if !ok {
		return nil, nil
	}
	out := make([]db.MLSPendingProposal, len(props))
	for i, p := range props {
		out[i] = *p
	}
	return out, nil
}

func (s *proposalRepoStub) DeleteAll(_ context.Context, groupID []byte) error {
	key := hex.EncodeToString(groupID)
	delete(s.proposals, key)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestService() *MLSServiceImpl {
	return &MLSServiceImpl{
		pool:    nil,
		groups:  newGroupRepoStub(),
		members: newMemberRepoStub(),
		kps:     newKeyPackageRepoStub(),
		props:   newProposalRepoStub(),
	}
}

func mustHaveGroup(t *testing.T, svc *MLSServiceImpl, workspaceID uuid.UUID) *MLSGroup {
	t.Helper()
	g, err := svc.GetGroupState(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("GetGroupState(%v): %v", workspaceID, err)
	}
	return &g.Group
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCreateGroup(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("public-key-32bytes-test-data!!")}

	group, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	if group.WorkspaceID != wsID {
		t.Fatalf("WorkspaceID = %v, want %v", group.WorkspaceID, wsID)
	}
	if group.CipherSuite != "MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519" {
		t.Fatalf("CipherSuite = %q, want MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519", group.CipherSuite)
	}
	if group.Epoch != 0 {
		t.Fatalf("Epoch = %d, want 0", group.Epoch)
	}
	if len(group.ID) != 32 {
		t.Fatalf("group ID length = %d, want 32", len(group.ID))
	}
	if group.TreeHash == nil {
		t.Fatal("TreeHash is nil")
	}
	if group.CreatedAt.IsZero() {
		t.Fatal("CreatedAt is zero")
	}
}

func TestCreateGroup_AddsCreatorAsMember(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("creator-public-key!!!!")}

	group, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	// Verify creator is a member by checking the group state
	state, err := svc.GetGroupState(ctx, wsID)
	if err != nil {
		t.Fatalf("GetGroupState() error = %v", err)
	}

	found := false
	for _, m := range state.Members {
		if m.ProfileID == creatorID {
			found = true
			if len(m.MLSIdentity) == 0 {
				t.Error("MLSIdentity is empty for creator member")
			}
			break
		}
	}
	if !found {
		t.Fatal("creator not found in group members after CreateGroup")
	}

	// Verify group ID matches between return value and stored state
	if hex.EncodeToString(state.Group.ID) != hex.EncodeToString(group.ID) {
		t.Fatal("stored group ID does not match returned group ID")
	}
}

func TestJoinGroup(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	joinerID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("creator-pk")}

	group, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if group.Epoch != 0 {
		t.Fatalf("CreateGroup epoch = %d, want 0", group.Epoch)
	}

	kp := MLSKeyPackage{
		ID:        uuid.New(),
		ProfileID: joinerID,
	}
	err = svc.JoinGroup(ctx, wsID, joinerID, kp, []byte("welcome"))
	if err != nil {
		t.Fatalf("JoinGroup() error = %v", err)
	}

	// Verify epoch incremented
	g, err := svc.groups.GetByWorkspace(ctx, wsID)
	if err != nil {
		t.Fatalf("GetByWorkspace() error = %v", err)
	}
	if g.Epoch != 1 {
		t.Fatalf("epoch after join = %d, want 1", g.Epoch)
	}

	// Verify both members exist
	state, err := svc.GetGroupState(ctx, wsID)
	if err != nil {
		t.Fatalf("GetGroupState() error = %v", err)
	}
	if len(state.Members) != 2 {
		t.Fatalf("member count = %d, want 2", len(state.Members))
	}
}

func TestJoinGroup_GroupNotFound(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	err := svc.JoinGroup(ctx, uuid.New(), uuid.New(), MLSKeyPackage{}, nil)
	if err == nil {
		t.Fatal("JoinGroup() expected error for non-existent workspace")
	}
}

func TestLeaveGroup(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("pk")}

	_, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	err = svc.LeaveGroup(ctx, wsID, creatorID)
	if err != nil {
		t.Fatalf("LeaveGroup() error = %v", err)
	}

	// Verify epoch incremented
	g, err := svc.groups.GetByWorkspace(ctx, wsID)
	if err != nil {
		t.Fatalf("GetByWorkspace() error = %v", err)
	}
	if g.Epoch != 1 {
		t.Fatalf("epoch after leave = %d, want 1", g.Epoch)
	}

	// Verify member removed
	_, err = svc.members.GetByProfile(ctx, g.ID, creatorID)
	if !errors.Is(err, db.ErrNotFound) {
		t.Fatal("creator still a member after LeaveGroup")
	}
}

func TestRemoveMember(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	memberID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("pk")}

	_, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	// Add a second member
	err = svc.JoinGroup(ctx, wsID, memberID, MLSKeyPackage{}, nil)
	if err != nil {
		t.Fatalf("JoinGroup() error = %v", err)
	}

	// Remove the second member
	err = svc.RemoveMember(ctx, wsID, memberID, creatorID)
	if err != nil {
		t.Fatalf("RemoveMember() error = %v", err)
	}

	// Verify epoch incremented
	g, err := svc.groups.GetByWorkspace(ctx, wsID)
	if err != nil {
		t.Fatalf("GetByWorkspace() error = %v", err)
	}
	if g.Epoch != 2 {
		t.Fatalf("epoch after remove = %d, want 2", g.Epoch)
	}

	// Verify member removed
	_, err = svc.members.GetByProfile(ctx, g.ID, memberID)
	if !errors.Is(err, db.ErrNotFound) {
		t.Fatal("member still present after RemoveMember")
	}
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("pk")}

	_, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	original := []byte("hello mls encryption roundtrip")
	ct, err := svc.Encrypt(ctx, wsID, creatorID, original)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	plaintext, err := svc.Decrypt(ctx, wsID, creatorID, ct)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}

	if string(plaintext) != string(original) {
		t.Fatalf("Decrypt() = %q, want %q", string(plaintext), string(original))
	}
}

func TestDecrypt_EpochMismatch(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("pk")}

	group, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	// Ciphertext with wrong epoch
	ct := MLSCiphertext{
		GroupID: group.ID,
		Epoch:   999,
	}
	_, err = svc.Decrypt(ctx, wsID, creatorID, ct)
	if !errors.Is(err, ErrEpochMismatch) {
		t.Fatalf("Decrypt() error = %v, want ErrEpochMismatch", err)
	}
}

func TestDecrypt_NotGroupMember(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	nonMemberID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("pk")}

	group, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	ct := MLSCiphertext{
		GroupID: group.ID,
		Epoch:   0,
	}
	_, err = svc.Decrypt(ctx, wsID, nonMemberID, ct)
	if !errors.Is(err, ErrNotGroupMember) {
		t.Fatalf("Decrypt() error = %v, want ErrNotGroupMember", err)
	}
}

func TestAddExternalProposal(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("pk")}

	_, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	proposalBytes := []byte(`{"add":{"member":"alice"}}`)
	err = svc.AddExternalProposal(ctx, wsID, uuid.New(), proposalBytes)
	if err != nil {
		t.Fatalf("AddExternalProposal() error = %v", err)
	}

	// Verify proposal was stored
	group := mustHaveGroup(t, svc, wsID)
	props, err := svc.props.ListByGroup(ctx, group.ID)
	if err != nil {
		t.Fatalf("ListByGroup() error = %v", err)
	}
	if len(props) != 1 {
		t.Fatalf("proposal count = %d, want 1", len(props))
	}
}

func TestCommitProposals_Empty(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("pk")}

	_, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	_, err = svc.CommitProposals(ctx, wsID, creatorID)
	if !errors.Is(err, ErrProposalRejected) {
		t.Fatalf("CommitProposals() error = %v, want ErrProposalRejected", err)
	}
}

func TestCommitProposals_Success(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("pk")}

	_, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	// Add two proposals
	for i := 0; i < 2; i++ {
		err := svc.AddExternalProposal(ctx, wsID, uuid.New(), []byte(`{"add":{"member":"user-`+string(rune('0'+i))+`"}}`))
		if err != nil {
			t.Fatalf("AddExternalProposal(%d) error = %v", i, err)
		}
	}

	// Commit
	commitBytes, err := svc.CommitProposals(ctx, wsID, creatorID)
	if err != nil {
		t.Fatalf("CommitProposals() error = %v", err)
	}
	if len(commitBytes) == 0 {
		t.Fatal("commit bytes are empty")
	}
	if string(commitBytes) != "placeholder-commit-bytes" {
		t.Fatalf("commit bytes = %q, want %q", string(commitBytes), "placeholder-commit-bytes")
	}

	// Verify epoch incremented
	g, err := svc.groups.GetByWorkspace(ctx, wsID)
	if err != nil {
		t.Fatalf("GetByWorkspace() error = %v", err)
	}
	if g.Epoch != 1 {
		t.Fatalf("epoch after commit = %d, want 1", g.Epoch)
	}

	// Verify proposals deleted
	group := mustHaveGroup(t, svc, wsID)
	props, err := svc.props.ListByGroup(ctx, group.ID)
	if err != nil {
		t.Fatalf("ListByGroup() error = %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("proposals remaining = %d, want 0", len(props))
	}
}

func TestGetEpochSecret(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("pk")}

	_, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	secret, err := svc.GetEpochSecret(ctx, wsID)
	if err != nil {
		t.Fatalf("GetEpochSecret() error = %v", err)
	}
	if len(secret) != 32 {
		t.Fatalf("secret length = %d, want 32", len(secret))
	}
}

func TestGetGroupState(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := Ed25519KeyPair{PublicKey: []byte("pk")}

	group, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}

	state, err := svc.GetGroupState(ctx, wsID)
	if err != nil {
		t.Fatalf("GetGroupState() error = %v", err)
	}
	if state.Group.ID == nil {
		t.Fatal("state.Group.ID is nil")
	}
	if hex.EncodeToString(state.Group.ID) != hex.EncodeToString(group.ID) {
		t.Fatal("state.Group.ID does not match")
	}
	if len(state.Members) != 1 {
		t.Fatalf("member count = %d, want 1", len(state.Members))
	}
	if state.Members[0].ProfileID != creatorID {
		t.Fatalf("member ProfileID = %v, want %v", state.Members[0].ProfileID, creatorID)
	}
}

func TestGetGroupState_NotFound(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	_, err := svc.GetGroupState(ctx, uuid.New())
	if err == nil {
		t.Fatal("GetGroupState() expected error for non-existent workspace")
	}
}
