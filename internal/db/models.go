// Package db provides the PostgreSQL data layer for Canopy: types,
// repository interfaces, and pgxpool connection management.
//
// Migrations live under ../../migrations and are applied by Migrate (see
// db.go) using golang-migrate's iofs source. Down migrations are paired
// with each up migration but are NOT exposed via this package — rewind
// is performed exclusively by the `make migrate-down` target, which uses
// the migrate CLI directly against the DSN.
package db

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ContentFormat enumerates the supported node content formats. Mirrors
// the CHECK constraint defined in 000003_nodes.up.sql.
const (
	ContentFormatMarkdown = "markdown"
	ContentFormatPlain    = "plain"
	ContentFormatRich     = "rich"
)

// NodeType enumerates the kinds of nodes a conversation DAG can hold.
const (
	NodeTypeMessage   = "message"
	NodeTypeSynthesis = "synthesis"
	NodeTypeSystem    = "system"
)

// EdgeType enumerates the kinds of directed edges between nodes.
const (
	EdgeTypeReply     = "reply"
	EdgeTypeFork      = "fork"
	EdgeTypeSynthesis = "synthesis"
	EdgeTypeReference = "reference"
)

// Node represents a single message in a conversation tree. Maps to the
// nodes table. JSON tags match the wire format used by SPEC-API-03.
type Node struct {
	ID            uuid.UUID  `db:"id"             json:"id"`
	TreeID        uuid.UUID  `db:"tree_id"        json:"treeId"`
	ParentID      *uuid.UUID `db:"parent_id"      json:"parentId"`
	AuthorID      uuid.UUID  `db:"author_id"      json:"authorId"`
	Content       string     `db:"content"        json:"content"`
	ContentFormat string     `db:"content_format" json:"contentFormat"`
	NodeType      string     `db:"node_type"      json:"nodeType"`
	SequenceNum   int64      `db:"sequence_num"   json:"sequenceNum"`
	Metadata      []byte     `db:"metadata"       json:"metadata"`
	CreatedAt     time.Time  `db:"created_at"     json:"createdAt"`
	EditedAt      *time.Time `db:"edited_at"      json:"editedAt"`
	DeletedAt     *time.Time `db:"deleted_at"     json:"deletedAt"`
}

// Edge represents a typed directed edge between two nodes. Maps to the
// edges table.
type Edge struct {
	ID          uuid.UUID  `db:"id"           json:"id"`
	TreeID      uuid.UUID  `db:"tree_id"      json:"treeId"`
	SourceID    uuid.UUID  `db:"source_id"    json:"sourceId"`
	TargetID    uuid.UUID  `db:"target_id"    json:"targetId"`
	EdgeType    string     `db:"edge_type"    json:"edgeType"`
	SequenceNum int64      `db:"sequence_num" json:"sequenceNum"`
	Metadata    []byte     `db:"metadata"     json:"metadata"`
	CreatedAt   time.Time  `db:"created_at"   json:"createdAt"`
	DeletedAt   *time.Time `db:"deleted_at"   json:"deletedAt"`
}

// Tree represents a conversation tree container. Maps to the trees
// table.
type Tree struct {
	ID          uuid.UUID  `db:"id"            json:"id"`
	OwnerID     uuid.UUID  `db:"owner_id"      json:"ownerId"`
	Title       string     `db:"title"         json:"title"`
	Description string     `db:"description"   json:"description"`
	RootNodeID  *uuid.UUID `db:"root_node_id"  json:"rootNodeId"`
	Metadata    []byte     `db:"metadata"      json:"metadata"`
	CreatedAt   time.Time  `db:"created_at"    json:"createdAt"`
	EditedAt    *time.Time `db:"edited_at"     json:"editedAt"`
	DeletedAt   *time.Time `db:"deleted_at"    json:"deletedAt"`
}

// TreeSnapshot represents a point-in-time hash-verified state of a tree.
// Maps to the tree_snapshots table (migration 000005) and is defined in
// SPEC-DM-02 §4.2.
type TreeSnapshot struct {
	ID           uuid.UUID `db:"id"            json:"id"`
	TreeID       uuid.UUID `db:"tree_id"       json:"treeId"`
	ParentHash   *string   `db:"parent_hash"   json:"parentHash"`
	Hash         string    `db:"hash"          json:"hash"`
	NodeCount    int       `db:"node_count"    json:"nodeCount"`
	EdgeCount    int       `db:"edge_count"    json:"edgeCount"`
	SnapshotData []byte    `db:"snapshot_data" json:"snapshotData"`
	CreatedAt    time.Time `db:"created_at"    json:"createdAt"`
}

// TreeEvent represents a single change event in a tree.
// Maps to the tree_events table (migration 000007) and is defined in
// SPEC-DM-02 §4.2.
type TreeEvent struct {
	ID          uuid.UUID  `db:"id"            json:"id"`
	TreeID      uuid.UUID  `db:"tree_id"       json:"treeId"`
	SnapshotID  *uuid.UUID `db:"snapshot_id"   json:"snapshotId"`
	EventType   string     `db:"event_type"    json:"eventType"`
	NodeID      *uuid.UUID `db:"node_id"       json:"nodeId"`
	EdgeID      *uuid.UUID `db:"edge_id"       json:"edgeId"`
	Payload     []byte     `db:"payload"       json:"payload"`
	SequenceNum int64      `db:"sequence_num"  json:"sequenceNum"`
	CreatedAt   time.Time  `db:"created_at"    json:"createdAt"`
}

// NodeCounts provides aggregate counts for a tree, returned by
// NodeRepo.GetCounts. All counts are pure SQL aggregates; nothing
// here is application-derived.
type NodeCounts struct {
	TreeID      uuid.UUID `json:"treeId"`
	TotalNodes  int64     `json:"totalNodes"`
	ActiveNodes int64     `json:"activeNodes"`
	TotalEdges  int64     `json:"totalEdges"`
	ActiveEdges int64     `json:"activeEdges"`
	MaxDepth    int       `json:"maxDepth"`
}

// ============================================================
// Approval system constants (SPEC-DM-03 §3, SPEC-DM-04 §3)
// ============================================================

// ApprovalStatus enumerates the lifecycle states of an Approval.
// Maps to the approval_status enum (migration 000009).
const (
	ApprovalStatusPending  = "pending"
	ApprovalStatusApproved = "approved"
	ApprovalStatusDenied   = "denied"
	ApprovalStatusExpired  = "expired"
)

// ProfileType enumerates whether a Profile is a human or a Hermes
// agent profile. Maps to the profile_type enum (migration 000008).
const (
	ProfileTypeHuman         = "human"
	ProfileTypeHermesProfile = "hermes-profile"
)

// TreeRole enumerates the access roles a TreeMember may hold.
// Maps to the tree_role enum (migration 000008).
const (
	TreeRoleOwner  = "owner"
	TreeRoleAdmin  = "admin"
	TreeRoleMember = "member"
	TreeRoleViewer = "viewer"
)

// InviteStatus enumerates the lifecycle of a ProfileInvite.
// Maps to the invite_status enum (migration 000008).
const (
	InviteStatusPending  = "pending"
	InviteStatusAccepted = "accepted"
	InviteStatusDeclined = "declined"
	InviteStatusExpired  = "expired"
)

// RuleScopeType enumerates the scopes an ApprovalRule may target.
// Maps to the rule_scope_type enum (migration 000009).
const (
	RuleScopeThread     = "thread"
	RuleScopeUser       = "user"
	RuleScopeProfile    = "profile"
	RuleScopeActionType = "action_type"
)

// AuditAction enumerates the actions captured by the immutable
// approval_audit_log. Maps to the audit_action enum (migration 000009).
const (
	AuditActionApprovalRequested = "approval_requested"
	AuditActionApprovalGranted   = "approval_granted"
	AuditActionApprovalDenied    = "approval_denied"
	AuditActionApprovalExpired   = "approval_expired"
	AuditActionRuleCreated       = "rule_created"
	AuditActionRuleUpdated       = "rule_updated"
	AuditActionRuleDeleted       = "rule_deleted"
	AuditActionRuleAutoApproved  = "rule_auto_approved"
	AuditActionRuleAutoDenied    = "rule_auto_denied"
)

// ============================================================
// Approval system domain types (SPEC-DM-03 §4, SPEC-DM-04 §4)
// ============================================================

// Approval represents a pending or decided agent action. Maps to
// the approvals table (migration 000009).
type Approval struct {
	ID           uuid.UUID  `db:"id"            json:"id"`
	TreeID       uuid.UUID  `db:"tree_id"       json:"treeId"`
	NodeID       uuid.UUID  `db:"node_id"       json:"nodeId"`
	OwnerID      uuid.UUID  `db:"owner_id"      json:"ownerId"`
	RequestedBy  uuid.UUID  `db:"requested_by"  json:"requestedBy"`
	Status       string     `db:"status"        json:"status"`
	DeniedReason *string    `db:"denied_reason" json:"deniedReason"`
	AutoRuleID   *uuid.UUID `db:"auto_rule_id"  json:"autoRuleId"`
	DecidedBy    *uuid.UUID `db:"decided_by"    json:"decidedBy"`
	CreatedAt    time.Time  `db:"created_at"    json:"createdAt"`
	DecidedAt    *time.Time `db:"decided_at"    json:"decidedAt"`
	ExpiresAt    time.Time  `db:"expires_at"    json:"expiresAt"`
}

// ApprovalRule defines an auto-approval or auto-denial rule. Maps
// to the approval_rules table (migration 000009).
type ApprovalRule struct {
	ID          uuid.UUID `db:"id"           json:"id"`
	TreeID      uuid.UUID `db:"tree_id"      json:"treeId"`
	OwnerID     uuid.UUID `db:"owner_id"     json:"ownerId"`
	ScopeType   string    `db:"scope_type"   json:"scopeType"`
	ScopeTarget uuid.UUID `db:"scope_target" json:"scopeTarget"`
	Decision    string    `db:"decision"     json:"decision"`
	Priority    int       `db:"priority"     json:"priority"`
	IsActive    bool      `db:"is_active"    json:"isActive"`
	CreatedAt   time.Time `db:"created_at"   json:"createdAt"`
	UpdatedAt   time.Time `db:"updated_at"   json:"updatedAt"`
}

// AuditEntry is an immutable record of one action taken against an
// Approval. Maps to the approval_audit_log table (migration 000009).
// The repository layer MUST NOT expose Update or Delete — the table
// has REVOKE UPDATE, DELETE at the database level.
type AuditEntry struct {
	ID             uuid.UUID       `db:"id"              json:"id"`
	ApprovalID     uuid.UUID       `db:"approval_id"     json:"approvalId"`
	Action         string          `db:"action"          json:"action"`
	Actor          *uuid.UUID      `db:"actor"           json:"actor"`
	PreviousStatus *string         `db:"previous_status" json:"previousStatus"`
	NewStatus      *string         `db:"new_status"      json:"newStatus"`
	Details        json.RawMessage `db:"details"         json:"details"`
	CreatedAt      time.Time       `db:"created_at"      json:"createdAt"`
}

// User represents a human Canopy user account. Maps to the users
// table (migration 000008). DeletedAt uses `json:"-"` because the
// field is internal-only (SPEC-DM-04 §4.1).
type User struct {
	ID           uuid.UUID  `db:"id"             json:"id"`
	HermesUserID string     `db:"hermes_user_id" json:"hermesUserId"`
	Email        *string    `db:"email"          json:"email"`
	DisplayName  string     `db:"display_name"   json:"displayName"`
	AvatarURL    *string    `db:"avatar_url"     json:"avatarUrl"`
	CreatedAt    time.Time  `db:"created_at"     json:"createdAt"`
	UpdatedAt    time.Time  `db:"updated_at"     json:"updatedAt"`
	LastSeenAt   *time.Time `db:"last_seen_at"   json:"lastSeenAt"`
	IsActive     bool       `db:"is_active"      json:"isActive"`
	DeletedAt    *time.Time `db:"deleted_at"     json:"-"`
}

// Profile represents a Hermes agent profile owned by a User. Maps
// to the profiles table (migration 000008). DeletedAt is internal.
type Profile struct {
	ID                uuid.UUID       `db:"id"                  json:"id"`
	OwnerID           uuid.UUID       `db:"owner_id"            json:"ownerId"`
	ProfileType       string          `db:"profile_type"        json:"profileType"`
	Name              string          `db:"name"                json:"name"`
	DisplayName       string          `db:"display_name"        json:"displayName"`
	Description       *string         `db:"description"         json:"description"`
	ConfigJSON        json.RawMessage `db:"config_json"         json:"configJson"`
	CanAutoRespond    bool            `db:"can_auto_respond"    json:"canAutoRespond"`
	ContextWindowSize int             `db:"context_window_size" json:"contextWindowSize"`
	IsPublic          bool            `db:"is_public"           json:"isPublic"`
	CreatedAt         time.Time       `db:"created_at"          json:"createdAt"`
	UpdatedAt         time.Time       `db:"updated_at"          json:"updatedAt"`
	DeletedAt         *time.Time      `db:"deleted_at"          json:"-"`
}

// TreeMember is a row in the tree_members table (migration 000008)
// granting a User or Profile access to a Tree. Exactly one of
// UserID / ProfileID must be set (enforced by CHECK constraint).
type TreeMember struct {
	ID           uuid.UUID  `db:"id"            json:"id"`
	TreeID       uuid.UUID  `db:"tree_id"       json:"treeId"`
	UserID       *uuid.UUID `db:"user_id"       json:"userId"`
	ProfileID    *uuid.UUID `db:"profile_id"    json:"profileId"`
	Role         string     `db:"role"          json:"role"`
	IsVisible    bool       `db:"is_visible"    json:"isVisible"`
	AutoApproved bool       `db:"auto_approved" json:"autoApproved"`
	JoinedAt     time.Time  `db:"joined_at"     json:"joinedAt"`
	InvitedBy    *uuid.UUID `db:"invited_by"    json:"invitedBy"`
}

// ProfileInvite is a row in the profile_invites table
// (migration 000008) inviting a Profile into a Tree.
type ProfileInvite struct {
	ID           uuid.UUID  `db:"id"            json:"id"`
	TreeID       uuid.UUID  `db:"tree_id"       json:"treeId"`
	ProfileID    uuid.UUID  `db:"profile_id"    json:"profileId"`
	InvitedBy    uuid.UUID  `db:"invited_by"    json:"invitedBy"`
	InviteToken  string     `db:"invite_token"  json:"-"`
	Status       string     `db:"status"        json:"status"`
	ProposedRole string     `db:"proposed_role" json:"proposedRole"`
	CreatedAt    time.Time  `db:"created_at"    json:"createdAt"`
	ExpiresAt    time.Time  `db:"expires_at"    json:"expiresAt"`
	AcceptedAt   *time.Time `db:"accepted_at"   json:"acceptedAt"`
	DeclinedAt   *time.Time `db:"declined_at"   json:"declinedAt"`
}
