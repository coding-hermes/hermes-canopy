// Package iteration implements the self-contained iteration-card engine core.
package iteration

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

// IterationSubtype identifies the renderer and event vocabulary for a card.
type IterationSubtype string

const (
	IterationSubtypeSearch   IterationSubtype = "iteration_search"
	IterationSubtypeCodeExec IterationSubtype = "iteration_code_exec"
	IterationSubtypeFileRead IterationSubtype = "iteration_file_read"
	IterationSubtypeThinking IterationSubtype = "iteration_thinking"
	IterationSubtypeToolCall IterationSubtype = "iteration_tool_call"
)

// IterationState is the common lifecycle state stored in card data.
type IterationState string

const (
	IterationStateRunning        IterationState = "running"
	IterationStateWaitingForUser IterationState = "waiting_for_user"
	IterationStateCompleted      IterationState = "completed"
	IterationStateFailed         IterationState = "failed"
	IterationStateCancelled      IterationState = "cancelled"
	IterationStateInterrupted    IterationState = "interrupted"
)

// FeedbackKind is the user-to-agent feedback vocabulary.
type FeedbackKind string

const (
	FeedbackKindRelevance  FeedbackKind = "relevance"
	FeedbackKindCorrection FeedbackKind = "correction"
	FeedbackKindSteer      FeedbackKind = "steer"
	FeedbackKindCancel     FeedbackKind = "cancel"
	FeedbackKindHighlight  FeedbackKind = "highlight"
	FeedbackKindApprove    FeedbackKind = "approve"
	FeedbackKindReject     FeedbackKind = "reject"
)

// ProgressType identifies the normalized progress renderer.
type ProgressType string

const (
	ProgressTypeSearch   ProgressType = "search"
	ProgressTypeCodeExec ProgressType = "code_exec"
	ProgressTypeFileRead ProgressType = "file_read"
	ProgressTypeThinking ProgressType = "thinking"
	ProgressTypeToolCall ProgressType = "tool_call"
)

// ProgressStatus is the status used by the normalized progress projection.
type ProgressStatus string

const (
	ProgressStatusRunning   ProgressStatus = "running"
	ProgressStatusCompleted ProgressStatus = "completed"
	ProgressStatusFailed    ProgressStatus = "failed"
	ProgressStatusCancelled ProgressStatus = "cancelled"
)

// Event names are deliberately closed. The card database has no event_type
// CHECK constraint, so this vocabulary is enforced at the domain boundary.
const (
	EventSearchStarted    = "search_started"
	EventURLDiscovered    = "url_discovered"
	EventSnippetRetrieved = "snippet_retrieved"
	EventSearchCompleted  = "search_completed"
	EventSearchError      = "search_error"
	EventExecStart        = "exec_start"
	EventExecOutput       = "exec_output"
	EventExecComplete     = "exec_complete"
	EventExecError        = "exec_error"
	EventFileReadOpened   = "file_read_opened"
	EventFileReadContent  = "file_read_content"
	EventFileReadError    = "file_read_error"
	EventThoughtStarted   = "thought_started"
	EventThoughtProgress  = "thought_progress"
	EventThoughtComplete  = "thought_complete"
	EventThoughtError     = "thought_error"
	EventToolCallStarted  = "tool_call_started"
	EventToolCallResult   = "tool_call_result"
	EventToolCallError    = "tool_call_error"
	EventCardCancelled    = "card_cancelled"
	EventCardSteered      = "card_steered"
	EventFeedbackAck      = "feedback_acknowledged"
)

// Base event names retained by SPEC-PL-03 and used by the engine lifecycle.
const (
	EventCardCreated     = "card_created"
	EventAgentProgress   = "agent_progress"
	EventAgentOutput     = "agent_output"
	EventAgentError      = "agent_error"
	EventUserFeedback    = "user_feedback"
	EventActionRequested = "action_requested"
	EventActionCompleted = "action_completed"
)

// CardProgress is the normalized current progress projection.
type CardProgress struct {
	CardID       uuid.UUID      `json:"cardId"`
	ParentCardID *uuid.UUID     `json:"parentCardId,omitempty"`
	Type         ProgressType   `json:"type"`
	Title        string         `json:"title"`
	Current      int            `json:"current"`
	Total        int            `json:"total"`
	Status       ProgressStatus `json:"status"`
	Phase        string         `json:"phase,omitempty"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

// SearchBatchItem is one bounded search result.
type SearchBatchItem struct {
	URL       string `json:"url"`
	Snippet   string `json:"snippet"`
	SnippetID string `json:"snippetId"`
	Status    string `json:"status"`
}

// SearchCardData is the typed search projection.
type SearchCardData struct {
	URLsSearched []string          `json:"urlsSearched"`
	CurrentBatch []SearchBatchItem `json:"currentBatch"`
	Progress     struct {
		Completed int `json:"completed"`
		Total     int `json:"total"`
	} `json:"searchProgress"`
	FocusURLs []string `json:"focusUrls"`
}

// ThoughtStep is a visible, user-facing reasoning digest step.
type ThoughtStep struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	Content    *string `json:"content"`
	DurationMS *int64  `json:"durationMs"`
	Error      *string `json:"error"`
}

// ThinkingData is the typed thinking projection.
type ThinkingData struct {
	Steps         []ThoughtStep `json:"steps"`
	CurrentStepID *string       `json:"currentStepId"`
}

// CodeExecData reserves the code-execution projection for later phases.
type CodeExecData struct {
	Command string `json:"command"`
}

// FileReadData reserves the file-read projection for later phases.
type FileReadData struct {
	Path string `json:"path"`
}

// ToolCallData reserves the tool-call projection for later phases.
type ToolCallData struct {
	ToolName string `json:"toolName"`
}

// IterationCardData is the common envelope plus typed projections. The
// subtype-specific projections are populated by callers that need typed access;
// persistence uses the flattened JSON wire shape from the spec.
type IterationCardData struct {
	Subtype   IterationSubtype `json:"subtype"`
	Title     string           `json:"title"`
	State     IterationState   `json:"state"`
	AgentID   string           `json:"agentId"`
	SessionID uuid.UUID        `json:"sessionId"`
	TopicID   *uuid.UUID       `json:"topicId,omitempty"`
	Progress  CardProgress     `json:"progress"`
	Search    *SearchCardData  `json:"-"`
	CodeExec  *CodeExecData    `json:"-"`
	FileRead  *FileReadData    `json:"-"`
	Thinking  *ThinkingData    `json:"-"`
	ToolCall  *ToolCallData    `json:"-"`
}

// PayloadEnvelope is the immutable event payload stored in events.payload.
type PayloadEnvelope struct {
	Subtype        IterationSubtype `json:"subtype"`
	Data           json.RawMessage  `json:"data"`
	AgentSessionID uuid.UUID        `json:"agent_session_id"`
	TraceID        *uuid.UUID       `json:"trace_id,omitempty"`
}

// IterationEvent is the typed service boundary for an agent event.
type IterationEvent struct {
	Sequence       int64            `json:"sequence"`
	Subtype        IterationSubtype `json:"subtype"`
	EventType      string           `json:"eventType"`
	Data           json.RawMessage  `json:"data"`
	CardID         uuid.UUID        `json:"cardId"`
	AgentID        string           `json:"agentId,omitempty"`
	AgentSessionID uuid.UUID        `json:"agentSessionId"`
	TraceID        *uuid.UUID       `json:"traceId,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
}

// FeedbackInput is a user feedback request. ActorID is normally derived by a
// future HTTP adapter; it is explicit here so the engine remains testable.
type FeedbackInput struct {
	CardID       uuid.UUID        `json:"cardId"`
	Subtype      IterationSubtype `json:"subtype"`
	FeedbackKind FeedbackKind     `json:"feedbackKind"`
	Target       json.RawMessage  `json:"target,omitempty"`
	Note         string           `json:"note,omitempty"`
	ActorID      string           `json:"actorId"`
	SessionID    uuid.UUID        `json:"sessionId"`
	Timestamp    time.Time        `json:"timestamp"`
}

// FeedbackEvent is the durable feedback record delivered by the hub.
type FeedbackEvent struct {
	ID           uuid.UUID        `json:"id"`
	CardID       uuid.UUID        `json:"cardId"`
	Subtype      IterationSubtype `json:"subtype"`
	FeedbackKind FeedbackKind     `json:"feedbackKind"`
	Target       json.RawMessage  `json:"target,omitempty"`
	Note         string           `json:"note,omitempty"`
	ActorID      string           `json:"actorId"`
	SessionID    uuid.UUID        `json:"sessionId"`
	CreatedAt    time.Time        `json:"createdAt"`
	Acknowledged bool             `json:"acknowledged"`
}

// CreateIterationCardInput carries optional base-card metadata and a complete
// validated envelope. Data may be omitted when using CreateCard's default.
type CreateIterationCardInput struct {
	ID          uuid.UUID         `json:"id"`
	TreeID      uuid.UUID         `json:"treeId"`
	NodeID      uuid.UUID         `json:"nodeId"`
	AppID       string            `json:"appId"`
	Subtype     IterationSubtype  `json:"subtype"`
	Data        json.RawMessage   `json:"data"`
	Actions     []card.CardAction `json:"actions"`
	ContextHash string            `json:"contextHash"`
	AgentID     string            `json:"agentId"`
	Process     AgentProcess      `json:"-"`
}

// AgentProcess is the cancellation and feedback bridge owned by a process
// manager. Crash detection and replacement replay are intentionally deferred.
type AgentProcess interface {
	ID() string
	SubscribeFeedback(ctx context.Context, cardID uuid.UUID) (<-chan FeedbackEvent, error)
	Cancel(ctx context.Context, cardID uuid.UUID) error
	Status() AgentProcessStatus
}

type AgentProcessStatus string

const (
	AgentProcessStatusStarting AgentProcessStatus = "starting"
	AgentProcessStatusRunning  AgentProcessStatus = "running"
	AgentProcessStatusStopping AgentProcessStatus = "stopping"
	AgentProcessStatusStopped  AgentProcessStatus = "stopped"
	AgentProcessStatusCrashed  AgentProcessStatus = "crashed"
)

// CancelInput is retained for callers that want to carry cancellation context.
type CancelInput struct {
	Reason  string    `json:"reason,omitempty"`
	ActorID string    `json:"actorId"`
	At      time.Time `json:"at"`
}

// IterationPatchInput is the authorization-aware materialized-state patch
// accepted by the HTTP adapter. Agent patches merge only mutable iteration data;
// browser patches store presentation metadata separately.
type IterationPatchInput struct {
	Data         json.RawMessage
	Presentation json.RawMessage
	Agent        bool
}

// IterationCardService is the phase-one service contract.
type IterationCardService interface {
	CreateCard(ctx context.Context, subtype IterationSubtype, agentID string) (*card.Card, error)
	AppendEvent(ctx context.Context, cardID uuid.UUID, event IterationEvent) error
	SubmitFeedback(ctx context.Context, cardID uuid.UUID, input FeedbackInput) error
	SubscribeEvents(ctx context.Context, cardID uuid.UUID) (<-chan IterationEvent, func(), error)
	CancelCard(ctx context.Context, cardID uuid.UUID) error
	ListActiveCards(ctx context.Context) ([]card.Card, error)
	GetProgress(ctx context.Context) ([]CardProgress, error)
}
