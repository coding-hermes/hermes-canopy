package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

type handleWorkspaceRepo struct {
	members []db.WorkspaceMemberRow
}

func (r *handleWorkspaceRepo) CreateWorkspace(context.Context, *db.WorkspaceRow) (*db.WorkspaceRow, error) {
	return nil, errors.New("not implemented")
}
func (r *handleWorkspaceRepo) GetWorkspaceByID(context.Context, uuid.UUID) (*db.WorkspaceRow, error) {
	return nil, errors.New("not implemented")
}
func (r *handleWorkspaceRepo) UpdateWorkspace(context.Context, uuid.UUID, string, string, *uuid.UUID, int64) (*db.WorkspaceRow, error) {
	return nil, errors.New("not implemented")
}
func (r *handleWorkspaceRepo) DeleteWorkspace(context.Context, uuid.UUID) error {
	return errors.New("not implemented")
}
func (r *handleWorkspaceRepo) ListWorkspacesForUser(context.Context, uuid.UUID) ([]db.WorkspaceRow, error) {
	return nil, errors.New("not implemented")
}
func (r *handleWorkspaceRepo) AddMember(context.Context, uuid.UUID, uuid.UUID, int) error {
	return errors.New("not implemented")
}
func (r *handleWorkspaceRepo) GetMember(context.Context, uuid.UUID, uuid.UUID) (*db.WorkspaceMemberRow, error) {
	return nil, errors.New("not implemented")
}
func (r *handleWorkspaceRepo) ListMembers(context.Context, uuid.UUID) ([]db.WorkspaceMemberRow, error) {
	return r.members, nil
}
func (r *handleWorkspaceRepo) UpdateMemberRole(context.Context, uuid.UUID, uuid.UUID, int) error {
	return errors.New("not implemented")
}
func (r *handleWorkspaceRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error {
	return errors.New("not implemented")
}
func (r *handleWorkspaceRepo) CreateInvitation(context.Context, *db.InvitationRow) (*db.InvitationRow, error) {
	return nil, errors.New("not implemented")
}
func (r *handleWorkspaceRepo) GetInvitationByHash(context.Context, string) (*db.InvitationRow, error) {
	return nil, errors.New("not implemented")
}
func (r *handleWorkspaceRepo) ConsumeInvitation(context.Context, uuid.UUID) (bool, error) {
	return false, errors.New("not implemented")
}

type handleUserReader struct {
	users map[uuid.UUID]*db.User
	errs  map[uuid.UUID]error
}

func (r *handleUserReader) GetByID(_ context.Context, id uuid.UUID) (*db.User, error) {
	if err := r.errs[id]; err != nil {
		return nil, err
	}
	return r.users[id], nil
}

func TestCollaborationMembersWithHandlesDisplayNameAndUUIDFallback(t *testing.T) {
	displayNameID := uuid.New()
	emptyNameID := uuid.New()
	missingUserID := uuid.New()
	reader := &handleUserReader{
		users: map[uuid.UUID]*db.User{
			displayNameID: {ID: displayNameID, DisplayName: "Ada Lovelace"},
			emptyNameID:   {ID: emptyNameID, DisplayName: ""},
		},
		errs: map[uuid.UUID]error{missingUserID: db.ErrNotFound},
	}
	repo := &handleWorkspaceRepo{members: []db.WorkspaceMemberRow{
		{UserID: displayNameID, Role: 1, JoinedAt: time.Unix(1, 0)},
		{UserID: emptyNameID, Role: 1, JoinedAt: time.Unix(2, 0)},
		{UserID: missingUserID, Role: 1, JoinedAt: time.Unix(3, 0)},
	}}
	svc := &collaborationServiceImpl{repo: repo, userReader: reader}

	members, err := svc.membersWithHandles(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("membersWithHandles: %v", err)
	}
	if got, want := members[0].Handle, "Ada Lovelace"; got != want {
		t.Errorf("display-name handle = %q, want %q", got, want)
	}
	if got, want := members[1].Handle, emptyNameID.String(); got != want {
		t.Errorf("empty-name handle = %q, want UUID %q", got, want)
	}
	if got, want := members[2].Handle, missingUserID.String(); got != want {
		t.Errorf("missing-user handle = %q, want UUID %q", got, want)
	}
}

func TestCollaborationMembersWithHandlesNilReaderUsesUUIDs(t *testing.T) {
	userID := uuid.New()
	svc := &collaborationServiceImpl{
		repo: &handleWorkspaceRepo{members: []db.WorkspaceMemberRow{{UserID: userID}}},
	}
	members, err := svc.membersWithHandles(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("membersWithHandles: %v", err)
	}
	if got := members[0].Handle; got != userID.String() {
		t.Fatalf("nil-reader handle = %q, want UUID %q", got, userID)
	}
}
