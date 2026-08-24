package collaboration

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CollaborationService is the primary interface for multi-user
// collaboration (SPEC-FTR-01 §3.1). Implementations live in
// internal/service; the pgx data layer lives in internal/db.
type CollaborationService interface {
	// CreateWorkspace creates a new workspace with the caller as admin.
	CreateWorkspace(ctx context.Context, ownerID uuid.UUID, name string) (*Workspace, error)

	// GetWorkspace retrieves a workspace by ID. Returns ErrNotFound if
	// missing. Callers must be members (ErrNotWorkspaceMember otherwise).
	GetWorkspace(ctx context.Context, workspaceID uuid.UUID) (*Workspace, error)

	// UpdateWorkspace updates workspace metadata (name, description,
	// tree_id, approval_ttl). The caller must be admin.
	UpdateWorkspace(ctx context.Context, workspaceID uuid.UUID, name, description string, treeID *uuid.UUID, approvalTTL int64) (*Workspace, error)

	// DeleteWorkspace removes a workspace and all its memberships and
	// invitations. The caller must be admin.
	DeleteWorkspace(ctx context.Context, workspaceID uuid.UUID) error

	// JoinWorkspace adds a user to a workspace using a valid invitation
	// token. Returns ErrInvalidToken for unknown, used, or expired tokens.
	JoinWorkspace(ctx context.Context, workspaceID uuid.UUID, userID uuid.UUID, token string) error

	// LeaveWorkspace removes a user from a workspace. The owner cannot
	// leave (ErrPermissionDenied).
	LeaveWorkspace(ctx context.Context, workspaceID uuid.UUID, userID uuid.UUID) error

	// GetUserWorkspaces returns all workspaces a user belongs to, each
	// with its member list.
	GetUserWorkspaces(ctx context.Context, userID uuid.UUID) ([]*Workspace, error)

	// UpdateMemberRole changes a member's role. The caller must be admin.
	// The owner's role cannot be changed (ErrPermissionDenied).
	UpdateMemberRole(ctx context.Context, workspaceID, callerID, targetID uuid.UUID, newRole Role) error

	// RemoveMember removes a member from the workspace. The caller must be
	// admin. The owner cannot be removed (ErrPermissionDenied).
	RemoveMember(ctx context.Context, workspaceID, callerID, targetID uuid.UUID) error

	// GenerateInvitation creates a one-time invitation token for the
	// workspace. The caller must be admin. Tokens expire after
	// InvitationTTL (7 days).
	GenerateInvitation(ctx context.Context, workspaceID, callerID uuid.UUID) (token string, expiresAt time.Time, err error)
}
