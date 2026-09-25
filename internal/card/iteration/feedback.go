package iteration

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

// FeedbackAckTimeout is the phase-one acknowledgement window. A later timeout
// monitor may use this value to report a timeout without deleting the durable
// feedback event.
const FeedbackAckTimeout = 30 * time.Second

// FeedbackTimeout exposes the phase-one deadline to a future monitor without
// making the monitor part of the in-memory service.
func (s *IterationCardServiceImpl) FeedbackTimeout() time.Duration { return FeedbackAckTimeout }

func isTerminalState(state IterationState) bool {
	return state == IterationStateCompleted || state == IterationStateFailed || state == IterationStateCancelled
}

type eventHub struct {
	mu     sync.Mutex
	nextID uint64
	subs   map[uuid.UUID]map[uint64]chan IterationEvent
}

func newEventHub() *eventHub {
	return &eventHub{subs: make(map[uuid.UUID]map[uint64]chan IterationEvent)}
}

func (h *eventHub) publish(cardID uuid.UUID, event IterationEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs[cardID] {
		// Persistence is the replay source of truth. A slow subscriber must not
		// block the committing service or make a committed event disappear.
		select {
		case ch <- event:
		default:
		}
	}
}

func (h *eventHub) subscribe(ctx context.Context, cardID uuid.UUID) (<-chan IterationEvent, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	ch := make(chan IterationEvent, 32)
	if h.subs[cardID] == nil {
		h.subs[cardID] = make(map[uint64]chan IterationEvent)
	}
	h.subs[cardID][id] = ch
	h.mu.Unlock()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			h.mu.Lock()
			if subscribers := h.subs[cardID]; subscribers != nil {
				if current, ok := subscribers[id]; ok {
					delete(subscribers, id)
					close(current)
				}
				if len(subscribers) == 0 {
					delete(h.subs, cardID)
				}
			}
			h.mu.Unlock()
		})
	}
	go func() {
		<-ctx.Done()
		cleanup()
	}()
	return ch, cleanup, nil
}

type feedbackSubscription struct {
	id uint64
	ch chan FeedbackEvent
}

type feedbackHub struct {
	mu      sync.Mutex
	nextID  uint64
	subs    map[uuid.UUID]map[uint64]*feedbackSubscription
	pending map[uuid.UUID]map[uuid.UUID]FeedbackEvent
}

func newFeedbackHub() *feedbackHub {
	return &feedbackHub{
		subs:    make(map[uuid.UUID]map[uint64]*feedbackSubscription),
		pending: make(map[uuid.UUID]map[uuid.UUID]FeedbackEvent),
	}
}

// publish retains every feedback item until Acknowledge removes it. A full or
// absent in-memory subscriber therefore only delays delivery; it cannot lose a
// committed user_feedback event.
func (h *feedbackHub) publish(feedback FeedbackEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.retainLocked(feedback)
	for _, sub := range h.subs[feedback.CardID] {
		select {
		case sub.ch <- feedback:
		default:
		}
	}
}

func (h *feedbackHub) retainLocked(feedback FeedbackEvent) {
	if h.pending[feedback.CardID] == nil {
		h.pending[feedback.CardID] = make(map[uuid.UUID]FeedbackEvent)
	}
	h.pending[feedback.CardID][feedback.ID] = feedback
}

func (h *feedbackHub) subscribe(ctx context.Context, cardID uuid.UUID, durable []FeedbackEvent) (<-chan FeedbackEvent, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	ch := make(chan FeedbackEvent, 32)
	if h.subs[cardID] == nil {
		h.subs[cardID] = make(map[uint64]*feedbackSubscription)
	}
	h.subs[cardID][id] = &feedbackSubscription{id: id, ch: ch}

	seen := make(map[uuid.UUID]bool, len(durable))
	for _, feedback := range durable {
		if feedback.Acknowledged || feedback.ID == uuid.Nil {
			continue
		}
		seen[feedback.ID] = true
		h.retainLocked(feedback)
		select {
		case ch <- feedback:
		default:
		}
	}
	for feedbackID, feedback := range h.pending[cardID] {
		if seen[feedbackID] {
			continue
		}
		select {
		case ch <- feedback:
		default:
		}
	}
	h.mu.Unlock()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			h.mu.Lock()
			if subscribers := h.subs[cardID]; subscribers != nil {
				if current, ok := subscribers[id]; ok {
					delete(subscribers, id)
					close(current.ch)
				}
				if len(subscribers) == 0 {
					delete(h.subs, cardID)
				}
			}
			h.mu.Unlock()
		})
	}
	go func() {
		<-ctx.Done()
		cleanup()
	}()
	return ch, cleanup, nil
}

func (h *feedbackHub) acknowledge(feedbackID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for cardID, items := range h.pending {
		delete(items, feedbackID)
		if len(items) == 0 {
			delete(h.pending, cardID)
		}
	}
}

// durablePending reconstructs the queue from the event log. This is what makes
// feedback replay survive a service or in-memory hub restart.
func durablePending(events []card.CardEvent) []FeedbackEvent {
	pending := make([]FeedbackEvent, 0)
	acknowledged := make(map[uuid.UUID]bool)
	for _, event := range events {
		switch event.EventType {
		case card.CardEventType(EventUserFeedback):
			var feedback FeedbackEvent
			if err := json.Unmarshal(event.Payload, &feedback); err != nil || feedback.ID == uuid.Nil {
				continue
			}
			feedback.Acknowledged = false
			pending = append(pending, feedback)
		case card.CardEventType(EventFeedbackAck):
			var payload struct {
				FeedbackID uuid.UUID `json:"feedback_id"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil && payload.FeedbackID != uuid.Nil {
				acknowledged[payload.FeedbackID] = true
			}
		}
	}
	result := pending[:0]
	seen := make(map[uuid.UUID]bool, len(pending))
	for _, feedback := range pending {
		if acknowledged[feedback.ID] || seen[feedback.ID] {
			continue
		}
		seen[feedback.ID] = true
		result = append(result, feedback)
	}
	return result
}
