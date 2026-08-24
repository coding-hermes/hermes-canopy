// Package service contains the business logic layer.
// CollaborationService implements the SPEC-FTR-01 §3.1 collaboration
// contract (workspace CRUD, membership, invitations) on top of the
// pgx-backed WorkspaceRepo. Identity = users (see internal/collaboration
// package doc comment for the deviation rationale).
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/collaboration"
	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// Invitation TTL per SPEC-FTR-01 §2 decision 19 (one-time link) and the
// worker brief: tokens expire 7 days after generation.
const invitationTTL = 7 * 24 * time.Hour

// DefaultApprovalTTL is the workspace approval-gate timeout in seconds
// (SPEC-FTR-01 §2 decision 12: 5-minute default).
const DefaultApprovalTTL = int64(300)

// collaborationServiceImpl is the real implementation of
// collaboration.CollaborationService.
type collaborationServiceImpl struct {
	repo db.WorkspaceRepo
	now  func() time.Time
}

// NewCollaborationService creates a CollaborationService backed by the
// given WorkspaceRepo.
func NewCollaborationService(repo db.WorkspaceRepo) collaboration.CollaborationService {
	return &collaborationServiceImpl{
		repo: repo,
		now:  time.Now,
	}
}

// CreateWorkspace creates a new workspace with the caller as admin.
// The slug is derived from the name with a short random suffix to
// satisfy the existing UNIQUE slug constraint (migration 000018).
func (s *collaborationServiceImpl) CreateWorkspace(ctx context.Context, ownerID uuid.UUID, name string) (*collaboration.Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("collaboration: workspace name is required")
	}
	if len(name) > 128 {
		return nil, errors.New("collaboration: workspace name must be 128 characters or fewer")
	}

	wsID := uuid.New()
	slug := slugify(name) + "-" + randomSuffix(4)
	now := s.now().UTC()

	row, err := s.repo.CreateWorkspace(ctx, &db.WorkspaceRow{
		ID:          wsID,
		OwnerID:     ownerID,
		Name:        name,
		Slug:        slug,
		Description: "",
		TreeID:      nil,
		ApprovalTTL: DefaultApprovalTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("collaboration: create workspace: %w", err)
	}

	// Caller becomes the workspace admin (SPEC-FTR-01 §5.1).
	if err := s.repo.AddMember(ctx, wsID, ownerID, int(collaboration.RoleAdmin)); err != nil {
		return nil, fmt.Errorf("collaboration: add owner member: %w", err)
	}

	return &collaboration.Workspace{
		ID:          row.ID,
		OwnerID:     row.OwnerID,
		Name:        row.Name,
		TreeID:      row.TreeID,
		ApprovalTTL: time.Duration(row.ApprovalTTL) * time.Second,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		Members: []collaboration.Member{{
			UserID:   ownerID,
			Handle:   ownerID.String(),
			Role:     collaboration.RoleAdmin,
			JoinedAt: now,
		}},
	}, nil
}

// GetWorkspace retrieves a workspace by ID. Only members may read
// (ErrNotWorkspaceMember → handler maps to 403).
func (s *collaborationServiceImpl) GetWorkspace(ctx context.Context, workspaceID uuid.UUID) (*collaboration.Workspace, error) {
	row, err := s.repo.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, collaboration.ErrNotFound
		}
		return nil, fmt.Errorf("collaboration: get workspace: %w", err)
	}

	members, err := s.membersWithHandles(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	return &collaboration.Workspace{
		ID:          row.ID,
		OwnerID:     row.OwnerID,
		Name:        row.Name,
		TreeID:      row.TreeID,
		Members:     members,
		ApprovalTTL: time.Duration(row.ApprovalTTL) * time.Second,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

// UpdateWorkspace updates workspace metadata. The handler enforces the
// admin-only rule before calling (SPEC-FTR-01 §5.1).
func (s *collaborationServiceImpl) UpdateWorkspace(ctx context.Context, workspaceID uuid.UUID, name, description string, treeID *uuid.UUID, approvalTTL int64) (*collaboration.Workspace, error) {
	row, err := s.repo.UpdateWorkspace(ctx, workspaceID, name, description, treeID, approvalTTL)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, collaboration.ErrNotFound
		}
		return nil, fmt.Errorf("collaboration: update workspace: %w", err)
	}

	members, err := s.membersWithHandles(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	return &collaboration.Workspace{
		ID:          row.ID,
		OwnerID:     row.OwnerID,
		Name:        row.Name,
		TreeID:      row.TreeID,
		Members:     members,
		ApprovalTTL: time.Duration(row.ApprovalTTL) * time.Second,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

// DeleteWorkspace removes a workspace and all its memberships and
// invitations. The handler enforces the admin-only rule before calling
// (SPEC-FTR-01 §5.1).
func (s *collaborationServiceImpl) DeleteWorkspace(ctx context.Context, workspaceID uuid.UUID) error {
	if err := s.repo.DeleteWorkspace(ctx, workspaceID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return collaboration.ErrNotFound
		}
		return fmt.Errorf("collaboration: delete workspace: %w", err)
	}
	return nil
}

// JoinWorkspace adds a user to a workspace using a valid invitation
// token. The token must exist, be unused, and be unexpired
// (ErrInvalidToken). Membership is added with RoleEditor (spec default)
// and the token is marked used atomically (single-use).
func (s *collaborationServiceImpl) JoinWorkspace(ctx context.Context, workspaceID, userID uuid.UUID, token string) error {
	hash := hashToken(token)
	inv, err := s.repo.GetInvitationByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return collaboration.ErrInvalidToken
		}
		return fmt.Errorf("collaboration: get invitation: %w", err)
	}
	if inv.Used || !inv.ExpiresAt.After(s.now().UTC()) {
		return collaboration.ErrInvalidToken
	}
	if inv.WorkspaceID != workspaceID {
		return collaboration.ErrInvalidToken
	}

	// Single-use: the atomic UPDATE ... WHERE used = false guarantees
	// only one concurrent join can consume the token.
	consumed, err := s.repo.ConsumeInvitation(ctx, inv.ID)
	if err != nil {
		return fmt.Errorf("collaboration: consume invitation: %w", err)
	}
	if !consumed {
		return collaboration.ErrInvalidToken
	}

	if err := s.repo.AddMember(ctx, workspaceID, userID, int(collaboration.RoleEditor)); err != nil {
		if errors.Is(err, db.ErrDuplicated) {
			return collaboration.ErrDuplicated
		}
		return fmt.Errorf("collaboration: add member: %w", err)
	}
	return nil
}

// LeaveWorkspace removes a user from a workspace. The owner cannot
// leave (ErrPermissionDenied).
func (s *collaborationServiceImpl) LeaveWorkspace(ctx context.Context, workspaceID, userID uuid.UUID) error {
	row, err := s.repo.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return collaboration.ErrNotFound
		}
		return fmt.Errorf("collaboration: get workspace: %w", err)
	}
	if row.OwnerID == userID {
		return collaboration.ErrPermissionDenied
	}

	if err := s.repo.RemoveMember(ctx, workspaceID, userID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return collaboration.ErrNotWorkspaceMember
		}
		return fmt.Errorf("collaboration: remove member: %w", err)
	}
	return nil
}

// GetUserWorkspaces returns all workspaces a user belongs to, each with
// its member list.
func (s *collaborationServiceImpl) GetUserWorkspaces(ctx context.Context, userID uuid.UUID) ([]*collaboration.Workspace, error) {
	rows, err := s.repo.ListWorkspacesForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("collaboration: list workspaces: %w", err)
	}

	out := make([]*collaboration.Workspace, 0, len(rows))
	for i := range rows {
		row := rows[i]
		members, err := s.membersWithHandles(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, &collaboration.Workspace{
			ID:          row.ID,
			OwnerID:     row.OwnerID,
			Name:        row.Name,
			TreeID:      row.TreeID,
			Members:     members,
			ApprovalTTL: time.Duration(row.ApprovalTTL) * time.Second,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
		})
	}
	return out, nil
}

// UpdateMemberRole changes a member's role. The caller must be admin;
// the owner's role cannot be changed (ErrPermissionDenied).
func (s *collaborationServiceImpl) UpdateMemberRole(ctx context.Context, workspaceID, callerID, targetID uuid.UUID, newRole collaboration.Role) error {
	if newRole < collaboration.RoleViewer || newRole > collaboration.RoleAdmin {
		return errors.New("collaboration: invalid role")
	}

	row, err := s.repo.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return collaboration.ErrNotFound
		}
		return fmt.Errorf("collaboration: get workspace: %w", err)
	}
	if row.OwnerID == targetID {
		return collaboration.ErrPermissionDenied
	}

	callerMember, err := s.repo.GetMember(ctx, workspaceID, callerID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return collaboration.ErrNotWorkspaceMember
		}
		return fmt.Errorf("collaboration: get caller member: %w", err)
	}
	if collaboration.Role(callerMember.Role) != collaboration.RoleAdmin {
		return collaboration.ErrPermissionDenied
	}

	if err := s.repo.UpdateMemberRole(ctx, workspaceID, targetID, int(newRole)); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return collaboration.ErrNotWorkspaceMember
		}
		return fmt.Errorf("collaboration: update member role: %w", err)
	}
	return nil
}

// RemoveMember removes a member from the workspace. The caller must be
// admin; the owner cannot be removed (ErrPermissionDenied).
func (s *collaborationServiceImpl) RemoveMember(ctx context.Context, workspaceID, callerID, targetID uuid.UUID) error {
	row, err := s.repo.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return collaboration.ErrNotFound
		}
		return fmt.Errorf("collaboration: get workspace: %w", err)
	}
	if row.OwnerID == targetID {
		return collaboration.ErrPermissionDenied
	}

	callerMember, err := s.repo.GetMember(ctx, workspaceID, callerID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return collaboration.ErrNotWorkspaceMember
		}
		return fmt.Errorf("collaboration: get caller member: %w", err)
	}
	if collaboration.Role(callerMember.Role) != collaboration.RoleAdmin {
		return collaboration.ErrPermissionDenied
	}

	if err := s.repo.RemoveMember(ctx, workspaceID, targetID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return collaboration.ErrNotWorkspaceMember
		}
		return fmt.Errorf("collaboration: remove member: %w", err)
	}
	return nil
}

// GenerateInvitation creates a one-time invitation token for the
// workspace. The caller must be admin. The raw token is a random 32-byte
// base64url string; only its SHA-256 hash is stored (token_hash).
func (s *collaborationServiceImpl) GenerateInvitation(ctx context.Context, workspaceID, callerID uuid.UUID) (string, time.Time, error) {
	callerMember, err := s.repo.GetMember(ctx, workspaceID, callerID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// Distinguish "workspace missing" from "caller not a member".
			if _, gerr := s.repo.GetWorkspaceByID(ctx, workspaceID); gerr != nil {
				if errors.Is(gerr, db.ErrNotFound) {
					return "", time.Time{}, collaboration.ErrNotFound
				}
				return "", time.Time{}, fmt.Errorf("collaboration: get workspace: %w", gerr)
			}
			return "", time.Time{}, collaboration.ErrNotWorkspaceMember
		}
		return "", time.Time{}, fmt.Errorf("collaboration: get caller member: %w", err)
	}
	if collaboration.Role(callerMember.Role) != collaboration.RoleAdmin {
		return "", time.Time{}, collaboration.ErrPermissionDenied
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("collaboration: generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	expiresAt := s.now().UTC().Add(invitationTTL)
	if _, err := s.repo.CreateInvitation(ctx, &db.InvitationRow{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		CreatedBy:   callerID,
		TokenHash:   hashToken(token),
		ExpiresAt:   expiresAt,
	}); err != nil {
		return "", time.Time{}, fmt.Errorf("collaboration: create invitation: %w", err)
	}
	return token, expiresAt, nil
}

// membersWithHandles loads a workspace's members and resolves each
// user's display name as the handle (users table; there is no separate
// handle column — see internal/collaboration package doc comment).
func (s *collaborationServiceImpl) membersWithHandles(ctx context.Context, workspaceID uuid.UUID) ([]collaboration.Member, error) {
	rows, err := s.repo.ListMembers(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("collaboration: list members: %w", err)
	}

	members := make([]collaboration.Member, 0, len(rows))
	for _, m := range rows {
		members = append(members, collaboration.Member{
			UserID:   m.UserID,
			Handle:   m.UserID.String(),
			Role:     collaboration.Role(m.Role),
			JoinedAt: m.JoinedAt,
		})
	}
	return members, nil
}

// slugify converts a name into a URL-safe slug (lowercase, hyphens).
func slugify(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ' || r == '-' || r == '_':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			// Drop non-ASCII / punctuation characters.
			lastDash = false
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "workspace"
	}
	return slug
}

// randomSuffix returns n random lowercase alphanumeric characters.
func randomSuffix(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// rand.Read failure is practically unreachable; fall back to a
		// time-based suffix so slug uniqueness is still preserved.
		return fmt.Sprintf("%d", time.Now().UnixNano()%100000000)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

// hashToken returns the SHA-256 hex digest of a raw invitation token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}
