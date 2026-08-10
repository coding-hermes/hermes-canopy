// Package service contains the business logic layer. TopicService defines
// the stub interface for BE-14 (Topics Endpoints). Full implementation
// deferred to a dedicated worker tick.
package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// TopicSummary is a lightweight view of a topic for list responses.
type TopicSummary struct {
	ID          uuid.UUID `json:"id"`
	TreeID      uuid.UUID `json:"tree_id"`
	RootNodeID  uuid.UUID `json:"root_node_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Slug        string    `json:"slug"`
	Status      string    `json:"status"`
	NodeCount   int       `json:"node_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// TopicProposal is the service-level representation of an auto-detected
// topic proposal. Returned by AutoDetect and PreviewProposal. The persisted
// form lives in db.TopicProposal.
type TopicProposal struct {
	ID            uuid.UUID    `json:"id"`
	TreeID        uuid.UUID    `json:"treeId"`
	RootNodeID    uuid.UUID    `json:"rootNodeId"`
	Title         string       `json:"title"`
	Description   string       `json:"description"`
	DetectionType DetectionType `json:"detectionType"`
	Confidence    float32      `json:"confidence"`
	SubjectKey    string       `json:"subjectKey"`
	Status        string       `json:"status"`
	ExpiresAt     time.Time    `json:"expiresAt"`
}

// TopicService defines the contract for topic CRUD, search, lifecycle,
// and auto-detection. Spec: SPEC-TM-01, SPEC-TM-02, SPEC-TM-03, SPEC-TM-05.
type TopicService interface {
	// CreateTopic creates a new topic rooted at the given node.
	CreateTopic(ctx context.Context, treeID uuid.UUID, rootNodeID uuid.UUID, title, description string) (*TopicSummary, error)

	// GetTopic retrieves a single topic by ID.
	GetTopic(ctx context.Context, topicID uuid.UUID) (*TopicSummary, error)

	// ListTopics lists topics for a tree with optional status filter.
	ListTopics(ctx context.Context, treeID uuid.UUID, status string, limit, offset int) ([]TopicSummary, error)

	// UpdateTopic updates topic metadata (title, description, status).
	UpdateTopic(ctx context.Context, topicID uuid.UUID, title, description, status *string) (*TopicSummary, error)

	// ArchiveTopic soft-deletes (archives) a topic.
	ArchiveTopic(ctx context.Context, topicID uuid.UUID) error

	// ── Auto-detection (SPEC-TM-02) ──────────────────────────────────

	// AutoDetect evaluates signals for a persisted node and emits a proposal
	// or auto-creates a topic per the tree's DetectionConfig. Returns the
	// resulting TopicProposal (or nil if no detection fired). Detection must
	// never fail the calling node-creation path — callers handle errors
	// non-fatally.
	AutoDetect(ctx context.Context, node db.Node, contextNodes []db.Node) (*TopicProposal, error)

	// ConfirmProposal accepts a pending proposal and creates a topic. If
	// titleOverride is non-empty it replaces the generated title. Rechecks
	// title uniqueness, root validity, expiry, and status. Concurrent
	// confirms are idempotent — both return the same created topic.
	ConfirmProposal(ctx context.Context, proposalID uuid.UUID, titleOverride string) (*db.Topic, error)

	// DismissProposal rejects a pending proposal and records a subject-key
	// cooldown so repeated matches are suppressed.
	DismissProposal(ctx context.Context, proposalID uuid.UUID) error

	// ListPendingProposals returns all pending proposals for a tree.
	ListPendingProposals(ctx context.Context, treeID uuid.UUID) ([]db.TopicProposal, error)

	// GetDetectionConfig returns the per-tree detection configuration.
	GetDetectionConfig(ctx context.Context, treeID uuid.UUID) (DetectionConfig, error)

	// UpdateDetectionConfig updates the per-tree detection configuration.
	UpdateDetectionConfig(ctx context.Context, treeID uuid.UUID, cfg DetectionConfig) (DetectionConfig, error)

	// PreviewProposal runs detection for a node without persisting a proposal.
	// Used by the CLI `canopyd topic detect` command.
	PreviewProposal(ctx context.Context, treeID, nodeID uuid.UUID) (*TopicProposal, error)
}
