// Package collaboration defines the multi-user collaboration core for
// Canopy OS: workspaces, membership, and invitations (SPEC-FTR-01 Phase
// P1). The service interface, role model, and error catalogue live here;
// the pgx-backed data layer lives in internal/db and the business-logic
// implementation in internal/service.
//
// IDENTITY DEVIATION (SPEC-FTR-01 §6.1): the spec DDL references
// profiles(id), but the authenticated identity in this codebase is the
// users table (JWT sub = user UUID; tree_members.user_id; multi-user
// integration tests use db.User). Workspace owner_id and
// workspace_members.user_id therefore reference users(id), NOT
// profiles(id). See migration 000032 for the matching DDL comment.
package collaboration

import (
	"time"

	"github.com/google/uuid"
)

// Role defines the permission level for a user within a workspace.
type Role int

const (
	RoleViewer Role = iota
	RoleEditor
	RoleAdmin
)

// String returns the human-readable role name.
func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleEditor:
		return "editor"
	case RoleAdmin:
		return "admin"
	default:
		return "unknown"
	}
}

// Workspace represents a collaborative tree shared by multiple users.
type Workspace struct {
	ID          uuid.UUID     `json:"id"`
	OwnerID     uuid.UUID     `json:"owner_id"`
	Name        string        `json:"name"`
	TreeID      *uuid.UUID    `json:"tree_id,omitempty"` // nil = not yet bound to a tree
	Members     []Member      `json:"members"`
	ApprovalTTL time.Duration `json:"approval_ttl"` // default 5m
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// Member associates a user with a role inside a workspace.
type Member struct {
	UserID   uuid.UUID `json:"user_id"`
	Handle   string    `json:"handle"`
	Role     Role      `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}
