// Package service contains the business logic layer. CardService defines
// the stub interface for BE-15 (Cards Endpoints). Full implementation
// deferred to a dedicated worker tick.
package service

import (
	"context"
	"encoding/json"
	"errors"
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

// Card action outcome statuses reported by SubmitCardAction.
const (
	// CardActionStatusCompleted means the action ran and action_completed was
	// appended to the card's event log.
	CardActionStatusCompleted = "completed"
	// CardActionStatusError means execution failed and agent_error was appended
	// to the card's event log.
	CardActionStatusError = "error"
)

// Card action errors. The HTTP layer maps them onto 400/404/422/500 responses
// for POST /api/v1/cards/{card_id}/actions.
var (
	// ErrCardActionHandlerRequired is returned when the requested action names
	// no handler.
	ErrCardActionHandlerRequired = errors.New("card service: action handler is required")
	// ErrCardActionPayloadInvalid is returned when an action payload is not a
	// JSON object (a null or absent payload is normalized to {}).
	ErrCardActionPayloadInvalid = errors.New("card service: action payload must be a JSON object")
	// ErrCardNotFound is returned when no card store holds the card.
	ErrCardNotFound = errors.New("card service: card not found")
	// ErrCardActionNotDeclared is returned when the handler is not one of the
	// card's declared actions. Nothing is appended in that case.
	ErrCardActionNotDeclared = errors.New("card service: action handler is not declared on card")
	// ErrCardActionFailed is returned when a declared action's execution
	// boundary failed. The outcome returned alongside it is non-nil so callers
	// can read the durable agent_error event it recorded.
	ErrCardActionFailed = errors.New("card service: action execution failed")
)

// CardActionOutcome is the durable result of a submitted card action: the
// action_requested event that opened it and the terminal event that closed it.
// Sequences are the card's monotonic event sequence, so the terminal event is
// always the requested event's immediate successor.
type CardActionOutcome struct {
	CardID          uuid.UUID       `json:"card_id"`
	Handler         string          `json:"handler"`
	Status          string          `json:"status"` // completed | error
	RequestedSeq    int64           `json:"requested_seq"`
	ResultSeq       int64           `json:"result_seq"`
	ResultEventID   uuid.UUID       `json:"result_event_id"`
	ResultEventType string          `json:"result_event_type"` // action_completed | agent_error
	Payload         json.RawMessage `json:"payload"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           string          `json:"error,omitempty"`
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

	// SubmitCardAction submits a declared action on a card: it validates the
	// handler against the card's declared actions, appends action_requested,
	// invokes the action's execution boundary, and appends action_completed
	// (success) or agent_error (execution failure).
	//
	// Errors: ErrCardActionHandlerRequired (no handler name),
	// ErrCardActionPayloadInvalid (payload is not a JSON object), ErrCardNotFound,
	// ErrCardActionNotDeclared (handler is not on the card — nothing is
	// appended), or ErrCardActionFailed (execution failed; the returned outcome
	// is non-nil so callers can read the durable agent_error event).
	SubmitCardAction(ctx context.Context, cardID uuid.UUID, handler string, payload json.RawMessage) (*CardActionOutcome, error)
}
