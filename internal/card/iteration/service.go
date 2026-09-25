package iteration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

var (
	ErrAgentNotRegistered = errors.New("iteration: agent is not registered for card")
	ErrCardNotActive      = errors.New("iteration: card is not active")
	ErrTerminalCard       = errors.New("iteration: card is terminal")
	ErrPatchForbidden     = errors.New("iteration: patch is not permitted")
)

// IterationCardServiceImpl composes the existing card repository and adds the
// iteration domain boundary. The process registry is deliberately in-memory;
// crash detection and replacement replay are later-phase hooks.
type IterationCardServiceImpl struct {
	dbMgr *card.CardDBManager

	mu        sync.RWMutex
	agents    map[uuid.UUID]string
	processes map[uuid.UUID]AgentProcess
	events    *eventHub
	feedback  *feedbackHub

	// materializeHook is a test seam for proving rollback after event INSERT.
	// Production callers leave it nil.
	materializeHook func(*sql.Tx) error
}

// NewIterationCardService creates the phase-one engine over a CardDBManager.
func NewIterationCardService(dbMgr *card.CardDBManager) *IterationCardServiceImpl {
	return &IterationCardServiceImpl{
		dbMgr: dbMgr, agents: make(map[uuid.UUID]string), processes: make(map[uuid.UUID]AgentProcess),
		events: newEventHub(), feedback: newFeedbackHub(),
	}
}

func newUUIDv7() (uuid.UUID, error) { return uuid.NewV7() }

func defaultData(cardID uuid.UUID, subtype IterationSubtype, agentID string) (json.RawMessage, error) {
	sessionID, err := newUUIDv7()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	progressType := map[IterationSubtype]string{IterationSubtypeSearch: "search", IterationSubtypeCodeExec: "code_exec", IterationSubtypeFileRead: "file_read", IterationSubtypeThinking: "thinking", IterationSubtypeToolCall: "tool_call"}[subtype]
	data := map[string]any{
		"subtype": string(subtype), "title": string(subtype), "state": string(IterationStateRunning), "agentId": agentID,
		"sessionId": sessionID.String(),
		"progress":  map[string]any{"cardId": cardID.String(), "type": progressType, "title": string(subtype), "current": 0, "total": 0, "status": string(ProgressStatusRunning), "updatedAt": now.Format(time.RFC3339Nano)},
	}
	switch subtype {
	case IterationSubtypeSearch:
		data["urlsSearched"] = []string{}
		data["currentBatch"] = []any{}
		data["searchProgress"] = map[string]any{"completed": 0, "total": 0}
		data["focusUrls"] = nil
	case IterationSubtypeThinking:
		data["steps"] = []any{}
		data["currentStepId"] = nil
	}
	return json.Marshal(data)
}

// CreateCard creates a minimal valid iteration card and registers agentID.
func (s *IterationCardServiceImpl) CreateCard(ctx context.Context, subtype IterationSubtype, agentID string) (*card.Card, error) {
	if err := ValidateSubtype(subtype); err != nil {
		return nil, err
	}
	if agentID == "" || !agentIDPattern.MatchString(agentID) {
		return nil, fmt.Errorf("iteration: invalid agent ID")
	}
	return s.CreateCardWithInput(ctx, CreateIterationCardInput{Subtype: subtype, AgentID: agentID, AppID: "canopy.agent"})
}

// CreateCardWithInput creates a base iteration card and its initial lifecycle
// event. It is useful to adapters that already have tree/node metadata.
func (s *IterationCardServiceImpl) CreateCardWithInput(ctx context.Context, input CreateIterationCardInput) (*card.Card, error) {
	if err := ValidateSubtype(input.Subtype); err != nil {
		return nil, err
	}
	if input.AgentID == "" || !agentIDPattern.MatchString(input.AgentID) {
		return nil, fmt.Errorf("iteration: invalid agent ID")
	}
	if input.Process != nil && input.Process.ID() != input.AgentID {
		return nil, fmt.Errorf("iteration: process ID does not match agent ID")
	}
	id := input.ID
	if id == uuid.Nil {
		var err error
		id, err = newUUIDv7()
		if err != nil {
			return nil, err
		}
	}
	data := input.Data
	var err error
	if len(data) == 0 {
		data, err = defaultData(id, input.Subtype, input.AgentID)
		if err != nil {
			return nil, err
		}
	}
	parsed, err := ValidateCardData(data)
	if err != nil {
		return nil, err
	}
	if parsed.Subtype != input.Subtype || parsed.AgentID != input.AgentID {
		return nil, fmt.Errorf("iteration: card data subtype/agent mismatch")
	}

	repo, err := s.dbMgr.Repository(card.CardTypeIteration)
	if err != nil {
		return nil, err
	}
	created, err := repo.Create(ctx, card.CreateCardInput{ID: id, TreeID: input.TreeID, NodeID: input.NodeID, AppID: input.AppID, CardType: card.CardTypeIteration, Data: data, Actions: input.Actions, ContextHash: input.ContextHash})
	if err != nil {
		return nil, fmt.Errorf("iteration: create card: %w", err)
	}
	if _, err := repo.AppendEvent(ctx, id, card.AppendEventInput{EventID: mustUUIDv7(), EventType: card.CardEventType(EventCardCreated), ActorKind: card.ActorAgent, ActorID: input.AgentID, Payload: data}); err != nil {
		return nil, fmt.Errorf("iteration: create event: %w", err)
	}
	s.mu.Lock()
	s.agents[id] = input.AgentID
	if input.Process != nil {
		s.processes[id] = input.Process
	}
	s.mu.Unlock()
	return created, nil
}

func mustUUIDv7() uuid.UUID {
	id, err := newUUIDv7()
	if err != nil {
		return uuid.New()
	}
	return id
}

// RegisterProcess attaches a live process to an existing card. Its ID must be
// the persisted agent ID; this is the authentication seam for future recovery.
func (s *IterationCardServiceImpl) RegisterProcess(ctx context.Context, cardID uuid.UUID, process AgentProcess) error {
	if process == nil {
		return fmt.Errorf("iteration: nil process")
	}
	cardData, err := s.getIterationCard(ctx, cardID)
	if err != nil {
		return err
	}
	if process.ID() != cardData.AgentID {
		return ErrAgentNotRegistered
	}
	s.mu.Lock()
	s.agents[cardID] = cardData.AgentID
	s.processes[cardID] = process
	s.mu.Unlock()
	return nil
}

// UnregisterProcess removes only the live process; the durable agent binding
// remains, so stale events cannot be accepted after a replacement.
func (s *IterationCardServiceImpl) UnregisterProcess(cardID uuid.UUID) {
	s.mu.Lock()
	delete(s.processes, cardID)
	s.mu.Unlock()
}

// AppendEvent validates, materializes, and publishes one agent event. The event
// INSERT and cards.data UPDATE share one transaction and publication is strictly
// after commit.
func (s *IterationCardServiceImpl) AppendEvent(ctx context.Context, cardID uuid.UUID, event IterationEvent) error {
	current, repo, err := s.findCard(ctx, cardID)
	if err != nil {
		return err
	}
	if current.Status != card.CardStatusActive {
		return ErrCardNotActive
	}
	data, err := ValidateCardData(current.Data)
	if err != nil {
		return err
	}
	if isTerminalState(data.State) {
		return ErrTerminalCard
	}
	if event.Subtype != data.Subtype {
		return fmt.Errorf("iteration: event subtype %q does not match card subtype %q", event.Subtype, data.Subtype)
	}
	s.mu.RLock()
	registered := s.agents[cardID]
	s.mu.RUnlock()
	if registered == "" {
		registered = data.AgentID
	}
	if event.AgentID != "" && event.AgentID != registered {
		return ErrAgentNotRegistered
	}
	if event.AgentID == "" {
		event.AgentID = registered
	}
	if event.AgentSessionID == uuid.Nil {
		event.AgentSessionID, _ = uuid.Parse(cardSessionID(current.Data))
	}
	if err := validateUUIDv7(event.AgentSessionID, "agent session"); err != nil {
		return err
	}
	var persistedSession string
	var raw map[string]any
	_ = json.Unmarshal(current.Data, &raw)
	persistedSession, _ = raw["sessionId"].(string)
	if event.AgentSessionID.String() != persistedSession {
		return fmt.Errorf("iteration: agent session does not match card")
	}
	if err := ValidateEventPair(event.Subtype, event.EventType, event.Data); err != nil {
		return err
	}
	if event.CardID != uuid.Nil && event.CardID != cardID {
		return fmt.Errorf("iteration: event card ID mismatch")
	}
	if event.EventType == EventCardCancelled || event.EventType == EventCardSteered {
		// Lifecycle events still carry the typed payload envelope so replay can
		// identify the owning subtype; their data shape is intentionally small.
		if _, err := decodeObject(event.Data, "event data"); err != nil {
			return err
		}
	}
	materialized, err := reduceEvent(current.Data, event.Subtype, event.EventType, event.Data)
	if err != nil {
		return err
	}
	envelope := PayloadEnvelope{Subtype: event.Subtype, Data: event.Data, AgentSessionID: event.AgentSessionID, TraceID: event.TraceID}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if _, err := ValidatePayloadEnvelope(payload); err != nil {
		return err
	}

	db, err := s.dbMgr.Database(card.CardTypeIteration)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	createdAt := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO events (event_id, card_id, event_type, actor_kind, actor_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, mustUUIDv7().String(), cardID.String(), event.EventType, string(card.ActorAgent), event.AgentID, string(payload), createdAt.Format(time.RFC3339Nano))
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("iteration: insert event: %w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if s.materializeHook != nil {
		if err := s.materializeHook(tx); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	update, err := tx.ExecContext(ctx, `UPDATE cards SET data = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?`, string(materialized), createdAt.Format(time.RFC3339Nano), cardID.String(), current.Revision)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("iteration: materialize: %w", err)
	}
	affected, _ := update.RowsAffected()
	if affected != 1 {
		_ = tx.Rollback()
		return fmt.Errorf("iteration: materialize card %s: revision conflict", cardID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("iteration: commit: %w", err)
	}
	event.Sequence, event.CardID, event.CreatedAt = sequence, cardID, createdAt
	s.events.publish(cardID, event)
	_ = repo
	return nil
}

// GetCard returns an iteration card after validating its typed data.
func (s *IterationCardServiceImpl) GetCard(ctx context.Context, cardID uuid.UUID) (*card.Card, error) {
	value, err := s.getIterationCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	stored, _, err := s.findCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	_ = value
	return stored, nil
}

// PatchCard applies an authorization-aware materialized iteration patch.
func (s *IterationCardServiceImpl) PatchCard(ctx context.Context, cardID uuid.UUID, expectedRevision int64, input IterationPatchInput) (*card.Card, error) {
	current, repo, err := s.findCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	data, err := ValidateCardData(current.Data)
	if err != nil {
		return nil, err
	}
	if isTerminalState(data.State) {
		return nil, ErrTerminalCard
	}

	var patchObject map[string]any
	actorKind := card.ActorUser
	actorID := "user"
	if input.Agent {
		if len(input.Data) == 0 {
			return nil, ErrPatchForbidden
		}
		patchObject, err = decodeObject(input.Data, "patch data")
		if err != nil {
			return nil, err
		}
		s.mu.RLock()
		process := s.processes[cardID]
		s.mu.RUnlock()
		if process == nil || process.ID() != data.AgentID {
			return nil, ErrAgentNotRegistered
		}
		for _, field := range []string{"agentId", "sessionId", "subtype", "command", "path", "absolutePath", "params", "result"} {
			if _, present := patchObject[field]; present {
				return nil, ErrPatchForbidden
			}
		}
		var currentObject map[string]any
		if err := json.Unmarshal(current.Data, &currentObject); err != nil {
			return nil, err
		}
		for key, value := range patchObject {
			currentObject[key] = value
		}
		if input.Presentation != nil {
			return nil, ErrPatchForbidden
		}
		input.Data, err = json.Marshal(currentObject)
		if err != nil {
			return nil, err
		}
		actorKind = card.ActorAgent
		actorID = data.AgentID
	} else {
		if len(input.Presentation) == 0 {
			return nil, ErrPatchForbidden
		}
		var presentation any
		if err := json.Unmarshal(input.Presentation, &presentation); err != nil {
			return nil, fmt.Errorf("iteration: presentation must be valid JSON: %w", err)
		}
		var currentObject map[string]any
		if err := json.Unmarshal(current.Data, &currentObject); err != nil {
			return nil, err
		}
		currentObject["presentation"] = presentation
		input.Data, err = json.Marshal(currentObject)
		if err != nil {
			return nil, err
		}
	}
	if _, err := ValidateCardData(input.Data); err != nil {
		return nil, err
	}
	updated, err := repo.Patch(ctx, cardID, expectedRevision, card.PatchCardInput{Data: &input.Data})
	if err != nil {
		return nil, err
	}
	if _, err := repo.AppendEvent(ctx, cardID, card.AppendEventInput{
		EventID: mustUUIDv7(), EventType: card.CardEventType(card.EventCardUpdated),
		ActorKind: actorKind, ActorID: actorID, Payload: input.Data,
	}); err != nil {
		return nil, err
	}
	return updated, nil
}

// SubmitFeedbackEvent persists feedback and returns the durable feedback record
// so an HTTP adapter can return its generated ID to the caller.
func (s *IterationCardServiceImpl) SubmitFeedbackEvent(ctx context.Context, cardID uuid.UUID, input FeedbackInput) (FeedbackEvent, error) {
	data, repo, err := s.findCard(ctx, cardID)
	if err != nil {
		return FeedbackEvent{}, err
	}
	if data.Status != card.CardStatusActive {
		return FeedbackEvent{}, ErrCardNotActive
	}
	parsed, err := ValidateCardData(data.Data)
	if err != nil {
		return FeedbackEvent{}, err
	}
	if input.CardID != uuid.Nil && input.CardID != cardID {
		return FeedbackEvent{}, fmt.Errorf("iteration: feedback card ID mismatch")
	}
	if input.Subtype != parsed.Subtype {
		return FeedbackEvent{}, fmt.Errorf("iteration: feedback subtype mismatch")
	}
	if input.SessionID.String() != cardSessionID(data.Data) {
		return FeedbackEvent{}, fmt.Errorf("iteration: feedback session does not match card")
	}
	if err := validateFeedback(input, parsed); err != nil {
		return FeedbackEvent{}, err
	}
	feedbackID, err := newUUIDv7()
	if err != nil {
		return FeedbackEvent{}, err
	}
	createdAt := input.Timestamp
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	feedback := FeedbackEvent{ID: feedbackID, CardID: cardID, Subtype: input.Subtype, FeedbackKind: input.FeedbackKind, Target: input.Target, Note: input.Note, ActorID: input.ActorID, SessionID: input.SessionID, CreatedAt: createdAt}
	payload, err := json.Marshal(feedback)
	if err != nil {
		return FeedbackEvent{}, err
	}
	stored, err := repo.AppendEvent(ctx, cardID, card.AppendEventInput{EventID: mustUUIDv7(), EventType: card.CardEventType(EventUserFeedback), ActorKind: card.ActorUser, ActorID: input.ActorID, Payload: payload})
	if err != nil {
		return FeedbackEvent{}, err
	}
	s.feedback.publish(feedback)
	s.events.publish(cardID, fromCardEvent(stored, parsed.Subtype))
	return feedback, nil
}

func (s *IterationCardServiceImpl) SubmitFeedback(ctx context.Context, cardID uuid.UUID, input FeedbackInput) error {
	_, err := s.SubmitFeedbackEvent(ctx, cardID, input)
	return err
}

func cardSessionID(raw json.RawMessage) string {
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	value, _ := object["sessionId"].(string)
	return value
}

// Acknowledge persists the acknowledgement and removes the feedback from the
// in-memory at-least-once queue only after the event append succeeds.
func (s *IterationCardServiceImpl) Acknowledge(ctx context.Context, cardID, feedbackID uuid.UUID) error {
	_, repo, err := s.findCard(ctx, cardID)
	if err != nil {
		return err
	}
	events, err := repo.ListEvents(ctx, cardID, 0, 10000)
	if err != nil {
		return err
	}
	found, acknowledged := false, false
	feedbackSubtype := IterationSubtype("")
	for _, event := range events {
		if event.EventType == card.CardEventType(EventUserFeedback) {
			var feedback FeedbackEvent
			if json.Unmarshal(event.Payload, &feedback) == nil && feedback.ID == feedbackID {
				found = true
				feedbackSubtype = feedback.Subtype
			}
		}
		if event.EventType == card.CardEventType(EventFeedbackAck) {
			var ack struct {
				FeedbackID uuid.UUID `json:"feedback_id"`
			}
			if json.Unmarshal(event.Payload, &ack) == nil && ack.FeedbackID == feedbackID {
				acknowledged = true
			}
		}
	}
	if !found {
		return fmt.Errorf("iteration: feedback %s not found", feedbackID)
	}
	if acknowledged {
		return nil
	}
	payload, _ := json.Marshal(map[string]uuid.UUID{"feedback_id": feedbackID})
	stored, err := repo.AppendEvent(ctx, cardID, card.AppendEventInput{EventID: mustUUIDv7(), EventType: card.CardEventType(EventFeedbackAck), ActorKind: card.ActorAgent, ActorID: "agent", Payload: payload})
	if err != nil {
		return err
	}
	s.feedback.acknowledge(feedbackID)
	s.events.publish(cardID, fromCardEvent(stored, feedbackSubtype))
	return nil
}

// SubscribeFeedback replays durable unacknowledged feedback before live values.
func (s *IterationCardServiceImpl) SubscribeFeedback(ctx context.Context, cardID uuid.UUID) (<-chan FeedbackEvent, func(), error) {
	_, repo, err := s.findCard(ctx, cardID)
	if err != nil {
		return nil, nil, err
	}
	events, err := repo.ListEvents(ctx, cardID, 0, 10000)
	if err != nil {
		return nil, nil, err
	}
	pending := durablePending(events)
	return s.feedback.subscribe(ctx, cardID, pending)
}

// CancelCard is cooperative. A terminal card is an idempotent no-op; a live
// process must accept cancellation before card_cancelled is materialized.
func (s *IterationCardServiceImpl) CancelCard(ctx context.Context, cardID uuid.UUID) error {
	current, _, err := s.findCard(ctx, cardID)
	if err != nil {
		return err
	}
	data, err := ValidateCardData(current.Data)
	if err != nil {
		return err
	}
	if data.State == IterationStateCompleted || data.State == IterationStateFailed || data.State == IterationStateCancelled {
		return nil
	}
	payload, _ := json.Marshal(map[string]string{"action": "cancel"})
	repo, _ := s.dbMgr.Repository(card.CardTypeIteration)
	if _, err := repo.AppendEvent(ctx, cardID, card.AppendEventInput{EventID: mustUUIDv7(), EventType: card.CardEventType(EventActionRequested), ActorKind: card.ActorUser, ActorID: "user", Payload: payload}); err != nil {
		return err
	}
	input := FeedbackInput{CardID: cardID, Subtype: data.Subtype, FeedbackKind: FeedbackKindCancel, ActorID: "user", SessionID: mustParseUUID(cardSessionID(current.Data)), Timestamp: time.Now().UTC()}
	if err := s.SubmitFeedback(ctx, cardID, input); err != nil {
		return err
	}
	s.mu.RLock()
	process := s.processes[cardID]
	s.mu.RUnlock()
	if process == nil {
		return fmt.Errorf("iteration: cancellation process is unavailable")
	}
	if err := process.Cancel(ctx, cardID); err != nil {
		return fmt.Errorf("iteration: cancellation rejected: %w", err)
	}
	return s.AppendEvent(ctx, cardID, IterationEvent{Subtype: data.Subtype, EventType: EventCardCancelled, Data: json.RawMessage(`{"reason":"accepted"}`), AgentID: data.AgentID, AgentSessionID: mustParseUUID(cardSessionID(current.Data))})
}

func mustParseUUID(value string) uuid.UUID { id, _ := uuid.Parse(value); return id }

// ListActiveCards returns active base cards whose materialized state is active.
func (s *IterationCardServiceImpl) ListActiveCards(ctx context.Context) ([]card.Card, error) {
	repo, err := s.dbMgr.Repository(card.CardTypeIteration)
	if err != nil {
		return nil, err
	}
	active := card.CardStatusActive
	cards, err := repo.List(ctx, card.ListCardsOptions{CardType: func() *card.CardType { v := card.CardTypeIteration; return &v }(), Status: &active, Limit: 10000})
	if err != nil {
		return nil, err
	}
	type ranked struct {
		value    card.Card
		sequence int64
	}
	rankedCards := make([]ranked, 0, len(cards))
	for _, value := range cards {
		data, err := ValidateCardData(value.Data)
		if err != nil {
			continue
		}
		if data.State != IterationStateRunning && data.State != IterationStateWaitingForUser && data.State != IterationStateInterrupted {
			continue
		}
		sequence, err := repo.MaxSequence(ctx, value.ID)
		if err != nil {
			return nil, err
		}
		rankedCards = append(rankedCards, ranked{value: value, sequence: sequence})
	}
	sort.SliceStable(rankedCards, func(i, j int) bool {
		if rankedCards[i].sequence != rankedCards[j].sequence {
			return rankedCards[i].sequence > rankedCards[j].sequence
		}
		return rankedCards[i].value.UpdatedAt.After(rankedCards[j].value.UpdatedAt)
	})
	result := make([]card.Card, len(rankedCards))
	for i := range rankedCards {
		result[i] = rankedCards[i].value
	}
	return result, nil
}

// GetProgress reads the current materialized data only; it never replays events.
func (s *IterationCardServiceImpl) GetProgress(ctx context.Context) ([]CardProgress, error) {
	cards, err := s.ListActiveCards(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]CardProgress, 0, len(cards))
	for _, value := range cards {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(value.Data, &object); err != nil {
			return nil, err
		}
		var progress CardProgress
		if err := json.Unmarshal(object["progress"], &progress); err != nil {
			return nil, err
		}
		result = append(result, progress)
	}
	return result, nil
}

// SubscribeEvents is the post-commit event fan-out hook.
func (s *IterationCardServiceImpl) SubscribeEvents(ctx context.Context, cardID uuid.UUID) (<-chan IterationEvent, func(), error) {
	if _, err := s.getIterationCard(ctx, cardID); err != nil {
		return nil, nil, err
	}
	return s.events.subscribe(ctx, cardID)
}

// ListEvents returns the durable event stream after sequence for SSE replay.
func (s *IterationCardServiceImpl) ListEvents(ctx context.Context, cardID uuid.UUID, afterSequence int64, limit int) ([]IterationEvent, error) {
	data, repo, err := s.findCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	parsed, err := ValidateCardData(data.Data)
	if err != nil {
		return nil, err
	}
	stored, err := repo.ListEvents(ctx, cardID, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	result := make([]IterationEvent, 0, len(stored))
	for _, event := range stored {
		payload := event.Payload
		var envelope PayloadEnvelope
		if json.Unmarshal(event.Payload, &envelope) == nil && len(envelope.Data) > 0 {
			payload = envelope.Data
		}
		result = append(result, IterationEvent{
			Sequence: event.Sequence, CardID: event.CardID, Subtype: parsed.Subtype,
			EventType: string(event.EventType), Data: payload, AgentID: event.ActorID,
			CreatedAt: event.CreatedAt,
		})
	}
	return result, nil
}

func (s *IterationCardServiceImpl) getIterationCard(ctx context.Context, cardID uuid.UUID) (IterationCardData, error) {
	value, _, err := s.findCard(ctx, cardID)
	if err != nil {
		return IterationCardData{}, err
	}
	return ValidateCardData(value.Data)
}

func (s *IterationCardServiceImpl) findCard(ctx context.Context, cardID uuid.UUID) (*card.Card, card.CardRepository, error) {
	repo, err := s.dbMgr.Repository(card.CardTypeIteration)
	if err != nil {
		return nil, nil, err
	}
	value, err := repo.Get(ctx, cardID)
	if err != nil {
		return nil, nil, fmt.Errorf("iteration: card %s: %w", cardID, err)
	}
	return value, repo, nil
}

func fromCardEvent(event *card.CardEvent, subtype IterationSubtype) IterationEvent {
	return IterationEvent{Sequence: event.Sequence, CardID: event.CardID, Subtype: subtype, EventType: string(event.EventType), Data: event.Payload, CreatedAt: event.CreatedAt}
}
