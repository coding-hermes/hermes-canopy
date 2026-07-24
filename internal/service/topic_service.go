// Package service contains the business logic layer. TopicService defines
// the stub interface for BE-14 (Topics Endpoints). Full implementation
// deferred to a dedicated worker tick.
package service

import (
	"context"
	"time"

	"github.com/google/uuid"
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

// TopicService defines the contract for topic CRUD, search, and lifecycle.
// Spec: SPEC-TM-01, SPEC-TM-03, SPEC-TM-05.
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
}
