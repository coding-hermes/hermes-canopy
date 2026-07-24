// Package card implements the App Card System per SPEC-PL-03.
// Cards are structured, interactive graph nodes owned by an app and stored
// in a local SQLite database selected by card type.
package card

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// ── Enumerations ──────────────────────────────────────────────────────

// CardType enumerates the three Canopy card types per SPEC-PL-03 §1.
type CardType string

const (
	CardTypeCompact   CardType = "compact"
	CardTypeExpanded  CardType = "expanded"
	CardTypeIteration CardType = "iteration"
)

// ValidCardTypes is the set of recognised card types.
var ValidCardTypes = map[CardType]bool{
	CardTypeCompact:   true,
	CardTypeExpanded:  true,
	CardTypeIteration: true,
}

// CardStatus represents the lifecycle status of a card.
type CardStatus string

const (
	CardStatusActive    CardStatus = "active"
	CardStatusDismissed CardStatus = "dismissed"
	CardStatusArchived  CardStatus = "archived"
)

// CardEventType enumerates the kinds of events that can be recorded against a card.
type CardEventType string

const (
	EventCardCreated     CardEventType = "card_created"
	EventCardUpdated     CardEventType = "card_updated"
	EventCardDismissed   CardEventType = "card_dismissed"
	EventCardRestored    CardEventType = "card_restored"
	EventCardArchived    CardEventType = "card_archived"
	EventAgentProgress   CardEventType = "agent_progress"
	EventAgentOutput     CardEventType = "agent_output"
	EventAgentError      CardEventType = "agent_error"
	EventUserFeedback    CardEventType = "user_feedback"
	EventActionRequested CardEventType = "action_requested"
	EventActionCompleted CardEventType = "action_completed"
	EventContextRebound  CardEventType = "context_rebound"
	EventSyncApplied     CardEventType = "sync_applied"
	EventSyncConflict    CardEventType = "sync_conflict"
)

// CardActorKind records who or what produced a card event.
type CardActorKind string

const (
	ActorAgent  CardActorKind = "agent"
	ActorUser   CardActorKind = "user"
	ActorSystem CardActorKind = "system"
	ActorSync   CardActorKind = "sync"
)

// ── Domain structs ────────────────────────────────────────────────────

// CardAction declares an interaction a renderer may expose.
type CardAction struct {
	Label   string `json:"label"`
	Handler string `json:"handler"`
}

// Card is the materialised current state of an app card.
type Card struct {
	ID          uuid.UUID       `json:"id"`
	TreeID      uuid.UUID       `json:"tree_id"`
	NodeID      uuid.UUID       `json:"node_id"`
	AppID       string          `json:"app_id"`
	CardType    CardType        `json:"card_type"`
	Data        json.RawMessage `json:"data"`
	Actions     []CardAction    `json:"actions"`
	Status      CardStatus      `json:"status"`
	ContextHash string          `json:"context_hash"`
	Revision    int64           `json:"revision"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	DismissedAt *time.Time      `json:"dismissed_at,omitempty"`
	ArchivedAt  *time.Time      `json:"archived_at,omitempty"`
}

// CardEvent is an append-only activity record for a card.
type CardEvent struct {
	Sequence  int64           `json:"sequence"`
	EventID   uuid.UUID       `json:"event_id"`
	CardID    uuid.UUID       `json:"card_id"`
	EventType CardEventType   `json:"event_type"`
	ActorKind CardActorKind   `json:"actor_kind"`
	ActorID   string          `json:"actor_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// ── Input / output DTOs ───────────────────────────────────────────────

// CreateCardInput carries the data needed to create a new card.
type CreateCardInput struct {
	ID          uuid.UUID       `json:"id"`
	TreeID      uuid.UUID       `json:"tree_id"`
	NodeID      uuid.UUID       `json:"node_id"`
	AppID       string          `json:"app_id"`
	CardType    CardType        `json:"card_type"`
	Data        json.RawMessage `json:"data"`
	Actions     []CardAction    `json:"actions"`
	ContextHash string          `json:"context_hash"`
}

// PatchCardInput carries the fields that may be updated on an existing card.
type PatchCardInput struct {
	Data        *json.RawMessage `json:"data,omitempty"`
	Actions     *[]CardAction    `json:"actions,omitempty"`
	Status      *CardStatus      `json:"status,omitempty"`
	ContextHash *string          `json:"context_hash,omitempty"`
}

// AppendEventInput carries the data for appending an event to a card.
type AppendEventInput struct {
	EventID   uuid.UUID       `json:"event_id"`
	EventType CardEventType   `json:"event_type"`
	ActorKind CardActorKind   `json:"actor_kind"`
	ActorID   string          `json:"actor_id"`
	Payload   json.RawMessage `json:"payload"`
}

// ListCardsOptions filters and paginates card list queries.
type ListCardsOptions struct {
	TreeID   *uuid.UUID
	NodeID   *uuid.UUID
	AppID    string
	CardType *CardType
	Status   *CardStatus
	Limit    int
	Offset   int
}

// CardToSummary converts a domain Card into a service.CardSummary.
func CardToSummary(c *Card) *service.CardSummary {
	actions := make([]any, len(c.Actions))
	for i, a := range c.Actions {
		actions[i] = a
	}
	var data any
	if len(c.Data) > 0 {
		_ = json.Unmarshal(c.Data, &data)
	}
	return &service.CardSummary{
		ID:          c.ID,
		TreeID:      c.TreeID,
		NodeID:      c.NodeID,
		AppID:       c.AppID,
		Type:        service.CardType(c.CardType),
		Status:      string(c.Status),
		ContextHash: c.ContextHash,
		Data:        data,
		Actions:     actions,
		CreatedAt:   c.CreatedAt,
	}
}

// ── Validation ────────────────────────────────────────────────────────

// IsValidCardType returns true when ct is a recognised CardType.
func IsValidCardType(ct CardType) bool {
	return ValidCardTypes[ct]
}
