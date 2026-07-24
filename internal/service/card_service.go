// Package service contains the business logic layer. CardService defines
// the stub interface for BE-15 (Cards Endpoints). Full implementation
// deferred to a dedicated worker tick.
package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CardType enumerates the three Canopy card types per SPEC-PL-03 §1.
type CardType string

const (
	CardTypeCompact   CardType = "compact"
	CardTypeExpanded  CardType = "expanded"
	CardTypeIteration CardType = "iteration"
)

// CardSummary is a lightweight view of a card for list responses.
type CardSummary struct {
	ID           uuid.UUID `json:"id"`
	TreeID       uuid.UUID `json:"tree_id"`
	NodeID       uuid.UUID `json:"node_id"`
	AppID        string    `json:"app_id"`
	Type         CardType  `json:"type"`
	Status       string    `json:"status"` // active | dismissed | archived
	ContextHash  string    `json:"context_hash"`
	Data         any       `json:"data"`    // type-specific JSON payload
	Actions      []any     `json:"actions"` // declared action descriptors
	LastEventSeq int64     `json:"last_event_seq"`
	CreatedAt    time.Time `json:"created_at"`
}

// CardService defines the contract for card CRUD, events, and lifecycle.
// Spec: SPEC-PL-03.
type CardService interface {
	// CreateCard creates a new card.
	CreateCard(ctx context.Context, treeID, nodeID uuid.UUID, appID string, cardType CardType, data any) (*CardSummary, error)

	// GetCard retrieves a single card by ID.
	GetCard(ctx context.Context, cardID uuid.UUID) (*CardSummary, error)

	// ListCards lists cards for a tree or node.
	ListCards(ctx context.Context, treeID, nodeID *uuid.UUID, cardType *CardType, limit, offset int) ([]CardSummary, error)

	// UpdateCardData updates the card's JSON data payload.
	UpdateCardData(ctx context.Context, cardID uuid.UUID, data any) (*CardSummary, error)

	// ArchiveCard dismisses/archives a card.
	ArchiveCard(ctx context.Context, cardID uuid.UUID) error
}
