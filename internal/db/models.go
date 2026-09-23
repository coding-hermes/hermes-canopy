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
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// ParentMode enumerates how a node's parents are declared. `lineage`
// keeps SPEC-DM-01's structural rule (at most one active incoming parent
// edge); `multi_reference` marks a message whose parent set is an atomic
// 2-20 `reference` edge set (SPEC-PL-06 §3.2, §3.3). Values mirror the
// chk_nodes_parent_mode constraint added by migration 000047.
type ParentMode string

const (
	ParentModeLineage        ParentMode = "lineage"
	ParentModeMultiReference ParentMode = "multi_reference"
)

// Valid reports whether the value is one of the enumerated parent modes.
func (m ParentMode) Valid() bool {
	switch m {
	case ParentModeLineage, ParentModeMultiReference:
		return true
	}
	return false
}

// Node represents a single message in a conversation tree. Maps to the
// nodes table. JSON tags match the wire format used by SPEC-API-03.
type Node struct {
	ID            uuid.UUID  `db:"id"             json:"id"`
	TreeID        uuid.UUID  `db:"tree_id"        json:"treeId"`
	ParentID      *uuid.UUID `db:"parent_id"      json:"parentId"` // display anchor; graph parents are incoming edges
	ParentMode    ParentMode `db:"parent_mode"    json:"parentMode"`
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

// MarshalJSON keeps persisted JSONB metadata as native JSON on every public
// response that serialises a db.Node. Storage and repository callers retain
// []byte; only the wire representation changes. Empty or invalid metadata is
// represented as an empty object, matching NodeDetail's public contract.
func (n Node) MarshalJSON() ([]byte, error) {
	metadata := bytes.TrimSpace(n.Metadata)
	if len(metadata) == 0 || !json.Valid(metadata) {
		metadata = []byte(`{}`)
	}

	type nodeJSON struct {
		ID            uuid.UUID       `json:"id"`
		TreeID        uuid.UUID       `json:"treeId"`
		ParentID      *uuid.UUID      `json:"parentId"`
		ParentMode    ParentMode      `json:"parentMode"`
		AuthorID      uuid.UUID       `json:"authorId"`
		Content       string          `json:"content"`
		ContentFormat string          `json:"contentFormat"`
		NodeType      string          `json:"nodeType"`
		SequenceNum   int64           `json:"sequenceNum"`
		Metadata      json.RawMessage `json:"metadata"`
		CreatedAt     time.Time       `json:"createdAt"`
		EditedAt      *time.Time      `json:"editedAt"`
		DeletedAt     *time.Time      `json:"deletedAt"`
	}
	return json.Marshal(nodeJSON{
		ID:            n.ID,
		TreeID:        n.TreeID,
		ParentID:      n.ParentID,
		ParentMode:    n.ParentMode,
		AuthorID:      n.AuthorID,
		Content:       n.Content,
		ContentFormat: n.ContentFormat,
		NodeType:      n.NodeType,
		SequenceNum:   n.SequenceNum,
		Metadata:      json.RawMessage(metadata),
		CreatedAt:     n.CreatedAt,
		EditedAt:      n.EditedAt,
		DeletedAt:     n.DeletedAt,
	})
}

// UnmarshalJSON accepts the native JSON metadata emitted by MarshalJSON and
// the legacy base64 string emitted by encoding/json for []byte metadata. The
// latter keeps older export files importable while new exports remain native.
func (n *Node) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID            uuid.UUID       `json:"id"`
		TreeID        uuid.UUID       `json:"treeId"`
		ParentID      *uuid.UUID      `json:"parentId"`
		ParentMode    ParentMode      `json:"parentMode"`
		AuthorID      uuid.UUID       `json:"authorId"`
		Content       string          `json:"content"`
		ContentFormat string          `json:"contentFormat"`
		NodeType      string          `json:"nodeType"`
		SequenceNum   int64           `json:"sequenceNum"`
		Metadata      json.RawMessage `json:"metadata"`
		CreatedAt     time.Time       `json:"createdAt"`
		EditedAt      *time.Time      `json:"editedAt"`
		DeletedAt     *time.Time      `json:"deletedAt"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	metadata, err := decodeNodeMetadata(wire.Metadata)
	if err != nil {
		return err
	}

	*n = Node{
		ID:            wire.ID,
		TreeID:        wire.TreeID,
		ParentID:      wire.ParentID,
		ParentMode:    wire.ParentMode,
		AuthorID:      wire.AuthorID,
		Content:       wire.Content,
		ContentFormat: wire.ContentFormat,
		NodeType:      wire.NodeType,
		SequenceNum:   wire.SequenceNum,
		Metadata:      append([]byte(nil), metadata...),
		CreatedAt:     wire.CreatedAt,
		EditedAt:      wire.EditedAt,
		DeletedAt:     wire.DeletedAt,
	}
	return nil
}

func decodeNodeMetadata(raw json.RawMessage) ([]byte, error) {
	metadata := bytes.TrimSpace(raw)
	if len(metadata) == 0 || bytes.Equal(metadata, []byte("null")) {
		return nil, nil
	}
	if metadata[0] == '"' {
		var encoded string
		if err := json.Unmarshal(metadata, &encoded); err != nil {
			return nil, err
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("node metadata is invalid legacy base64: %w", err)
		}
		metadata = decoded
	}
	if !json.Valid(metadata) {
		return nil, fmt.Errorf("node metadata is invalid JSON")
	}
	return append([]byte(nil), metadata...), nil
}

// MultiReferenceMetadata is the reserved `metadata.multi_reference`
// object stored on every parent_mode='multi_reference' node. It is a
// validated, server-built denormalized manifest — the active reference
// edges remain authoritative (SPEC-PL-06 §3.3, §3.5 invariant 5).
// JSON tags are camelCase because this object is stored inside the
// node's metadata document, not on an HTTP boundary.
type MultiReferenceMetadata struct {
	Version               int                 `json:"version"`
	PrimarySourceID       uuid.UUID           `json:"primarySourceId"`
	CanonicalSourceIDs    []uuid.UUID         `json:"canonicalSourceIds"` // denormalized audit order; edges remain authoritative
	IsSyntheticMergePoint bool                `json:"isSyntheticMergePoint"`
	BranchSpan            *BranchSpanMetadata `json:"branchSpan,omitempty"`
	ContextManifestHash   string              `json:"contextManifestHash"`
	ContextTokenBudget    int                 `json:"contextTokenBudget"`

	// RequestID / RequestHash implement the §13.1 step 8 idempotency
	// record. SPEC-PL-06's §3.1 DDL adds no idempotency table, so the
	// record lives in the reserved server-owned metadata key: a create
	// with the same caller + tree + request_id whose payload hash matches
	// returns the original result, and a mismatched hash is a
	// REFERENCE_REQUEST_ID_CONFLICT (§15 scenarios 19-20).
	RequestID   string `json:"requestId,omitempty"`
	RequestHash string `json:"requestHash,omitempty"`
}

// MultiReferenceMetadataVersion is the only metadata version this
// implementation writes (SPEC-PL-06 §3.3, §9.2 example).
const MultiReferenceMetadataVersion = 1

// BranchSpanMetadata records the nearest shared display ancestor and each
// source's first divergent child (SPEC-PL-06 §3.3, §8.1). Stored inside
// metadata.multi_reference.branchSpan (camelCase keys).
type BranchSpanMetadata struct {
	CommonAncestorID uuid.UUID               `json:"commonAncestorId"`
	SourceBranches   []ReferenceBranchSource `json:"sourceBranches"`
}

// ReferenceBranchSource is one source's divergence point inside a
// BranchSpanMetadata document.
type ReferenceBranchSource struct {
	SourceID         uuid.UUID `json:"sourceId"`
	BranchRootID     uuid.UUID `json:"branchRootId"`
	DistanceFromRoot int       `json:"distanceFromRoot"`
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

// Topic represents a named branch (topic) in a conversation tree.
// Maps to the topics table (migration 000020). Spec: SPEC-TM-01 §3.
type Topic struct {
	ID            uuid.UUID  `db:"id"              json:"id"`
	TreeID        uuid.UUID  `db:"tree_id"         json:"treeId"`
	RootNodeID    uuid.UUID  `db:"root_node_id"    json:"rootNodeId"`
	Title         string     `db:"title"           json:"title"`
	Description   string     `db:"description"     json:"description"`
	Slug          string     `db:"slug"            json:"slug"`
	ParentTopicID *uuid.UUID `db:"parent_topic_id" json:"parentTopicId"`
	Status        string     `db:"status"          json:"status"`
	TopicTags     []string   `db:"topic_tags"      json:"topicTags"`
	NodeCount     int32      `db:"node_count"      json:"nodeCount"`
	CreatedAt     time.Time  `db:"created_at"      json:"createdAt"`
	ArchivedAt    *time.Time `db:"archived_at"     json:"archivedAt"`
	DeletedAt     *time.Time `db:"deleted_at"      json:"-"`
}

// TopicMember represents a profile's membership in a topic.
type TopicMember struct {
	TopicID   uuid.UUID `db:"topic_id"   json:"topicId"`
	ProfileID uuid.UUID `db:"profile_id" json:"profileId"`
	Role      string    `db:"role"       json:"role"`
	JoinedAt  time.Time `db:"joined_at"  json:"joinedAt"`
}

// TopicSummary is a lightweight view of a topic for list responses.
type TopicSummary struct {
	ID          uuid.UUID  `json:"id"`
	TreeID      uuid.UUID  `json:"treeId"`
	Title       string     `json:"title"`
	Slug        string     `json:"slug"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	NodeCount   int32      `json:"nodeCount"`
	TopicTags   []string   `json:"topicTags"`
	CreatedAt   time.Time  `json:"createdAt"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
}

// TopicCreateInput is the request payload for creating a topic.
type TopicCreateInput struct {
	TreeID        uuid.UUID  `json:"treeId" validate:"required"`
	RootNodeID    uuid.UUID  `json:"rootNodeId" validate:"required"`
	Title         string     `json:"title" validate:"required,min=1,max=200"`
	Description   string     `json:"description,omitempty"`
	ParentTopicID *uuid.UUID `json:"parentTopicId,omitempty"`
	TopicTags     []string   `json:"topicTags,omitempty"`
}

// TopicUpdateInput is the request payload for updating a topic.
type TopicUpdateInput struct {
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	TopicTags   *[]string `json:"topicTags,omitempty"`
}

// ── Topic detection (SPEC-TM-02 §8.1) ───────────────────────────────────

// TopicProposal represents a pending, confirmed, dismissed, or expired
// auto-detected topic proposal. Maps to topic_proposals (migration 000030).
type TopicProposal struct {
	ID            uuid.UUID       `db:"id"             json:"id"`
	TreeID        uuid.UUID       `db:"tree_id"        json:"treeId"`
	RootNodeID    uuid.UUID       `db:"root_node_id"   json:"rootNodeId"`
	Title         string          `db:"title"          json:"title"`
	Description   string          `db:"description"    json:"description"`
	DetectionType string          `db:"detection_type" json:"detectionType"`
	Confidence    float32         `db:"confidence"     json:"confidence"`
	SubjectKey    string          `db:"subject_key"    json:"subjectKey"`
	Status        string          `db:"status"         json:"status"`
	ExpiresAt     time.Time       `db:"expires_at"     json:"expiresAt"`
	CreatedAt     time.Time       `db:"created_at"     json:"createdAt"`
	ResolvedAt    *time.Time      `db:"resolved_at"    json:"resolvedAt,omitempty"`
	Evidence      json.RawMessage `db:"evidence"       json:"evidence"`
}

// DetectionConfigRecord is the per-tree topic-detection configuration row.
// Maps to topic_detection_config (migration 000030).
type DetectionConfigRecord struct {
	TreeID                uuid.UUID `db:"tree_id"                 json:"treeId"`
	AutoCreate            bool      `db:"auto_create"             json:"autoCreate"`
	AlwaysAsk             bool      `db:"always_ask"              json:"alwaysAsk"`
	DetectionLevel        string    `db:"detection_level"         json:"detectionLevel"`
	MinMessagesPerTopic   int       `db:"min_messages_per_topic"  json:"minMessagesPerTopic"`
	ProposalCooldown      int       `db:"proposal_cooldown"       json:"proposalCooldown"`
	LastProposalSeq       int64     `db:"last_proposal_seq"       json:"lastProposalSeq"`
	MessagesSinceProposal int       `db:"messages_since_proposal" json:"messagesSinceProposal"`
	UpdatedAt             time.Time `db:"updated_at"              json:"updatedAt"`
}

// SubjectCooldown represents a rejection cooldown for a subject key in a tree.
type SubjectCooldown struct {
	TreeID        uuid.UUID `db:"tree_id"        json:"treeId"`
	SubjectKey    string    `db:"subject_key"    json:"subjectKey"`
	CooldownUntil time.Time `db:"cooldown_until" json:"cooldownUntil"`
	CreatedAt     time.Time `db:"created_at"     json:"createdAt"`
}
