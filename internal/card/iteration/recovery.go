package iteration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

// recoveryState is in-memory authorization state for one replacement process.
// Durable feedback acknowledgements are checked again at resume time, so a
// process cannot satisfy recovery merely by opening a subscription.
type recoveryState struct {
	agentID    string
	replaySent bool
}

// RecoverySession is the replacement process's durable feedback replay. The
// caller must consume the channel and acknowledge every item before calling
// ResumeProcess. Cleanup should be deferred by the caller.
type RecoverySession struct {
	Feedback <-chan FeedbackEvent
	Cleanup  func()
}

// ReportProcessStatus is the service entry point used by a process manager. A
// crashed process is removed from the live registry only after the durable
// agent_error/state transition commits. An empty status represents a process
// that vanished before it could report its terminal status.
func (s *IterationCardServiceImpl) ReportProcessStatus(ctx context.Context, cardID uuid.UUID, agentID string, status AgentProcessStatus) error {
	current, _, err := s.findCard(ctx, cardID)
	if err != nil {
		return err
	}
	data, err := ValidateCardData(current.Data)
	if err != nil {
		return err
	}

	if agentID == "" {
		s.mu.RLock()
		if process := s.processes[cardID]; process != nil {
			agentID = process.ID()
		}
		s.mu.RUnlock()
	}
	if agentID != data.AgentID {
		return ErrAgentNotRegistered
	}
	if status != "" && status != AgentProcessStatusCrashed && status != AgentProcessStatusStopped {
		return fmt.Errorf("iteration: unsupported process exit status %q", status)
	}

	// A terminal card is already durable and must not acquire a crash event.
	if isTerminalState(data.State) {
		s.removeLiveProcess(cardID)
		return nil
	}
	if current.Status != card.CardStatusActive {
		return ErrCardNotActive
	}
	if data.State == IterationStateInterrupted {
		s.removeLiveProcess(cardID)
		return nil
	}
	if status != "" {
		s.mu.RLock()
		process := s.processes[cardID]
		registered := process != nil && process.ID() == agentID
		s.mu.RUnlock()
		if !registered {
			return ErrAgentNotRegistered
		}
	}

	code := "ITERATION_AGENT_NOT_FOUND"
	message := "agent process disappeared without a status report"
	if status != "" {
		code = "ITERATION_PROCESS_CRASHED"
		message = "agent process reported an unexpected exit"
	}
	eventData, err := json.Marshal(map[string]any{
		"code": code, "message": message, "agent_id": agentID, "status": string(status),
	})
	if err != nil {
		return err
	}
	materialized, err := reduceEvent(current.Data, data.Subtype, EventAgentError, eventData)
	if err != nil {
		return err
	}
	stored, err := s.commitRecoveryEvent(ctx, current, data, EventAgentError, eventData, materialized)
	if err != nil {
		return err
	}
	s.removeLiveProcess(cardID)

	// The error event is durable and the snapshot is an in-memory notification.
	// The iteration SSE handler maps the latter to a card_snapshot frame.
	s.events.publish(cardID, stored)
	s.events.publish(cardID, IterationEvent{
		Sequence: stored.Sequence, Subtype: data.Subtype, EventType: EventCardSnapshot,
		Data: materialized, CardID: cardID, AgentID: data.AgentID,
		AgentSessionID: mustParseUUID(cardSessionID(current.Data)), CreatedAt: stored.CreatedAt,
	})
	return nil
}

// ReportMissingProcess records the crash path for a process manager that lost a
// process before receiving its final status. The empty status is intentional.
func (s *IterationCardServiceImpl) ReportMissingProcess(ctx context.Context, cardID uuid.UUID, agentID string) error {
	return s.ReportProcessStatus(ctx, cardID, agentID, "")
}

// BeginRecovery authenticates and registers a same-agent replacement, then
// issues the durable unacknowledged feedback replay before it may resume.
// Authorized-supervisor identity is future scope; RegisterProcess is the
// current authentication seam and requires the persisted agent ID.
func (s *IterationCardServiceImpl) BeginRecovery(ctx context.Context, cardID uuid.UUID, process AgentProcess) (*RecoverySession, error) {
	data, _, err := s.findCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	parsed, err := ValidateCardData(data.Data)
	if err != nil {
		return nil, err
	}
	if parsed.State != IterationStateInterrupted {
		return nil, ErrRecoveryRequired
	}
	if err := s.RegisterProcess(ctx, cardID, process); err != nil {
		return nil, err
	}
	feedback, cleanup, err := s.SubscribeFeedback(ctx, cardID)
	if err != nil {
		s.UnregisterProcess(cardID)
		return nil, err
	}
	s.mu.Lock()
	s.recovery[cardID] = recoveryState{agentID: process.ID(), replaySent: true}
	s.mu.Unlock()
	return &RecoverySession{Feedback: feedback, Cleanup: cleanup}, nil
}

// ResumeProcess atomically appends the resumed progress event and patches the
// materialized state back to running. Every durable feedback item must have
// been acknowledged after BeginRecovery; otherwise this returns
// ITERATION_RECOVERY_REQUIRED and leaves the card interrupted.
func (s *IterationCardServiceImpl) ResumeProcess(ctx context.Context, cardID uuid.UUID, agentID string) error {
	current, repo, err := s.findCard(ctx, cardID)
	if err != nil {
		return err
	}
	data, err := ValidateCardData(current.Data)
	if err != nil {
		return err
	}
	if data.State != IterationStateInterrupted {
		return ErrRecoveryRequired
	}
	s.mu.RLock()
	process := s.processes[cardID]
	recovery, replayed := s.recovery[cardID]
	s.mu.RUnlock()
	if process == nil || process.ID() != agentID || !replayed || recovery.agentID != agentID {
		return ErrRecoveryRequired
	}
	storedEvents, err := repo.ListEvents(ctx, cardID, 0, 10000)
	if err != nil {
		return err
	}
	if pending := durablePending(storedEvents); len(pending) != 0 {
		return fmt.Errorf("%w: %d feedback item(s) remain unacknowledged", ErrRecoveryRequired, len(pending))
	}

	progressData, err := json.Marshal(map[string]string{
		"message": "resumed", "recovered_from": agentID,
	})
	if err != nil {
		return err
	}
	materialized, err := reduceEvent(current.Data, data.Subtype, EventAgentProgress, progressData)
	if err != nil {
		return err
	}
	var object map[string]any
	if err := json.Unmarshal(materialized, &object); err != nil {
		return err
	}
	// Keep the state patch explicit in this transaction, rather than relying on
	// the generic progress reducer to authorize a lifecycle transition.
	object["state"] = string(IterationStateRunning)
	materialized, err = json.Marshal(object)
	if err != nil {
		return err
	}
	stored, err := s.commitRecoveryEvent(ctx, current, data, EventAgentProgress, progressData, materialized)
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.recovery, cardID)
	s.mu.Unlock()
	s.events.publish(cardID, stored)
	return nil
}

func (s *IterationCardServiceImpl) removeLiveProcess(cardID uuid.UUID) {
	s.mu.Lock()
	delete(s.processes, cardID)
	delete(s.recovery, cardID)
	s.mu.Unlock()
}

func (s *IterationCardServiceImpl) processSnapshot() map[uuid.UUID]AgentProcess {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[uuid.UUID]AgentProcess, len(s.processes))
	for cardID, process := range s.processes {
		result[cardID] = process
	}
	return result
}

func (s *IterationCardServiceImpl) commitRecoveryEvent(
	ctx context.Context,
	current *card.Card,
	data IterationCardData,
	eventType string,
	eventData, materialized json.RawMessage,
) (IterationEvent, error) {
	envelope := PayloadEnvelope{Subtype: data.Subtype, Data: eventData, AgentSessionID: mustParseUUID(cardSessionID(current.Data))}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return IterationEvent{}, err
	}
	if _, err := ValidatePayloadEnvelope(payload); err != nil {
		return IterationEvent{}, err
	}
	db, err := s.dbMgr.Database(card.CardTypeIteration)
	if err != nil {
		return IterationEvent{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return IterationEvent{}, err
	}
	createdAt := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO events (event_id, card_id, event_type, actor_kind, actor_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mustUUIDv7().String(), current.ID.String(), eventType, string(card.ActorAgent), data.AgentID, string(payload), createdAt.Format(time.RFC3339Nano))
	if err != nil {
		_ = tx.Rollback()
		return IterationEvent{}, fmt.Errorf("iteration: insert recovery event: %w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		_ = tx.Rollback()
		return IterationEvent{}, err
	}
	if s.materializeHook != nil {
		if err := s.materializeHook(tx); err != nil {
			_ = tx.Rollback()
			return IterationEvent{}, err
		}
	}
	updated, err := tx.ExecContext(ctx, `UPDATE cards SET data = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?`,
		string(materialized), createdAt.Format(time.RFC3339Nano), current.ID.String(), current.Revision)
	if err != nil {
		_ = tx.Rollback()
		return IterationEvent{}, fmt.Errorf("iteration: materialize recovery: %w", err)
	}
	affected, _ := updated.RowsAffected()
	if affected != 1 {
		_ = tx.Rollback()
		return IterationEvent{}, fmt.Errorf("iteration: materialize recovery card %s: revision conflict", current.ID)
	}
	if err := tx.Commit(); err != nil {
		return IterationEvent{}, fmt.Errorf("iteration: commit recovery: %w", err)
	}
	return IterationEvent{
		Sequence: sequence, Subtype: data.Subtype, EventType: eventType, Data: eventData,
		CardID: current.ID, AgentID: data.AgentID, AgentSessionID: mustParseUUID(cardSessionID(current.Data)), CreatedAt: createdAt,
	}, nil
}

// ProcessMonitor polls registered processes and turns crash statuses into the
// service crash transaction. Poll is public for deterministic tests; Start is
// the production ticker wrapper and never requires a sleep in callers.
type ProcessMonitor struct {
	service  *IterationCardServiceImpl
	interval time.Duration
}

func NewProcessMonitor(service *IterationCardServiceImpl, interval time.Duration) *ProcessMonitor {
	if interval <= 0 {
		interval = time.Second
	}
	return &ProcessMonitor{service: service, interval: interval}
}

func (m *ProcessMonitor) Poll(ctx context.Context) error {
	for cardID, process := range m.service.processSnapshot() {
		status := process.Status()
		if status != AgentProcessStatusCrashed && status != AgentProcessStatusStopped {
			continue
		}
		if err := m.service.ReportProcessStatus(ctx, cardID, process.ID(), status); err != nil && !errors.Is(err, ErrAgentNotRegistered) {
			return err
		}
	}
	return nil
}

func (m *ProcessMonitor) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := m.Poll(ctx); err != nil {
					// The next poll retries transient database failures; a monitor
					// failure must not take down the HTTP server.
					continue
				}
			}
		}
	}()
}
