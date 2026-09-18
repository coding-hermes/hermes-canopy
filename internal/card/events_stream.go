package card

import (
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// cardEventBuffer is the per-subscriber buffer depth of a live card-event
// subscription. It is deliberately small: the hub is a liveness channel, not a
// queue. A subscriber that falls further behind than this is served by cursor
// replay (SPEC-PL-03 §9.2, "reconnect Last-Event-ID: 43 -> SELECT events WHERE
// sequence > 43"), so dropping is always recoverable.
const cardEventBuffer = 64

// CardEventHub fans freshly appended card events out to live SSE subscribers,
// keyed by card id.
//
// SPEC-PL-03 §9.2 publishes through this hub after the SQLite commit:
//
//	AppendAgentEvent -> INSERT events -> Publish(card_event, 42) -> SSE id=42
//
// Three properties are load-bearing and each is pinned by a test:
//
//   - Publish NEVER blocks. An append path must not be held up by a viewer, so
//     a subscriber whose buffer is full is skipped rather than waited for. The
//     skipped client recovers through cursor replay on its next reconnect.
//   - Subscribe/Unsubscribe are safe against a concurrent Publish. The mutex
//     covers both the send and the close, so an unsubscribe can never close a
//     channel underneath an in-flight publish (see cardEventHub for the race
//     this design is built to survive, proven under `go test -race`).
//   - Unsubscribe is idempotent and closes the subscriber channel exactly once,
//     which is what lets the SSE handler's stream loop end on channel close.
//
// A nil *CardEventHub is a valid, inert hub: every method is nil-receiver safe
// and does nothing, so a service constructed without a hub (every existing
// test, and any deployment where card streaming is not wired) behaves exactly
// as it did before this hub existed.
type CardEventHub struct {
	mu sync.Mutex
	// subs maps card id -> subscriber id -> subscriber channel. A nested map
	// (rather than a slice) keeps unsubscribe O(1) without an index scan, and
	// the per-card level is dropped as soon as its last subscriber leaves so a
	// long-lived process does not accumulate one entry per card ever streamed.
	subs   map[uuid.UUID]map[uint64]chan CardEvent
	nextID uint64

	// published counts every Publish call, including calls with no subscriber.
	// Appends publish through the service; for a brand-new card no client can
	// be subscribed yet (the SSE route 404s a card that does not exist), so
	// this counter is the only observable trace of the append->publish
	// contract on that one path. It is the counter an operator would scrape as
	// a metric.
	published atomic.Int64
}

// NewCardEventHub returns an empty hub ready to accept subscribers.
func NewCardEventHub() *CardEventHub {
	return &CardEventHub{subs: make(map[uuid.UUID]map[uint64]chan CardEvent)}
}

// Subscribe registers a live listener for cardID.
//
// It returns a receive-only channel of card events and an unsubscribe func. The
// channel is closed by unsubscribe (or by the service adapter that wraps it),
// so a stream loop can end on `ev, ok := <-ch; !ok`. The unsubscribe func is
// idempotent: calling it twice is safe and never panics.
func (h *CardEventHub) Subscribe(cardID uuid.UUID) (<-chan CardEvent, func()) {
	if h == nil {
		return nil, func() {}
	}

	ch := make(chan CardEvent, cardEventBuffer)

	h.mu.Lock()
	if h.subs == nil {
		h.subs = make(map[uuid.UUID]map[uint64]chan CardEvent)
	}
	h.nextID++
	id := h.nextID
	if h.subs[cardID] == nil {
		h.subs[cardID] = make(map[uint64]chan CardEvent)
	}
	h.subs[cardID][id] = ch
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			byID, ok := h.subs[cardID]
			if !ok {
				return
			}
			c, ok := byID[id]
			if !ok {
				return
			}
			delete(byID, id)
			if len(byID) == 0 {
				delete(h.subs, cardID)
			}
			// Closed under the SAME mutex Publish sends under: a concurrent
			// publish either delivered into this channel already or has not
			// started, never a send to a closed channel.
			close(c)
		})
	}
}

// Publish delivers ev to every live subscriber of cardID.
//
// It is non-blocking by construction: a subscriber whose buffer is full is
// skipped, so a slow (or wedged) client can never stall the append path that
// called Publish. The client recovers the gap via cursor replay instead.
func (h *CardEventHub) Publish(cardID uuid.UUID, ev CardEvent) {
	if h == nil {
		return
	}
	h.published.Add(1)

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs[cardID] {
		select {
		case ch <- ev:
		default:
			// Full buffer: drop. Never block the appender.
		}
	}
}

// SubscriberCount returns the number of live subscribers for cardID.
func (h *CardEventHub) SubscriberCount(cardID uuid.UUID) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[cardID])
}

// TotalSubscribers returns the number of live subscribers across every card.
func (h *CardEventHub) TotalSubscribers() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, byID := range h.subs {
		total += len(byID)
	}
	return total
}

// PublishedCount reports how many events the hub has been asked to publish,
// including publishes that had no subscriber.
func (h *CardEventHub) PublishedCount() int64 {
	if h == nil {
		return 0
	}
	return h.published.Load()
}

// WithEventHub installs the live fan-out hub that carries card events to
// connected SSE clients (SPEC-PL-03 §9.2). A nil hub detaches the seam, which
// makes publishing a no-op — the behaviour of every service built by
// NewCardServiceImpl alone.
func (s *CardServiceImpl) WithEventHub(hub *CardEventHub) *CardServiceImpl {
	s.hub = hub
	return s
}

// publishEvent fans a freshly appended event out to live subscribers.
//
// Publishing must never affect the append that just succeeded: a nil hub (the
// default) and a nil event are both no-ops, and the hub itself drops rather
// than blocks. Call sites therefore publish unconditionally, right after
// AppendEvent returns the stored event — the value published carries the
// sequence and event id the store assigned.
func (s *CardServiceImpl) publishEvent(ev *CardEvent) {
	if s == nil || s.hub == nil || ev == nil {
		return
	}
	s.hub.Publish(ev.CardID, *ev)
}

// toServiceCardEvent converts a stored event into the streamable
// service.CardEvent view the HTTP layer consumes. The payload is normalised to
// {} so a frame is always valid JSON.
func toServiceCardEvent(ev CardEvent) service.CardEvent {
	payload := ev.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	return service.CardEvent{
		Sequence:  ev.Sequence,
		EventID:   ev.EventID,
		CardID:    ev.CardID,
		EventType: string(ev.EventType),
		ActorKind: string(ev.ActorKind),
		ActorID:   ev.ActorID,
		Payload:   payload,
		CreatedAt: ev.CreatedAt,
	}
}

// SubscribeCardEvents implements service.CardService: it adapts the hub's
// card.CardEvent stream to the service-level view the HTTP layer consumes.
//
// The adapter exists because internal/service cannot name card.CardEvent (the
// card package depends on service, not the other way round), and it is a
// straight pass-through otherwise: a buffered out channel, one pump goroutine,
// and a drop-on-full hand-off so a slow HTTP client can never block the hub's
// reader. The returned unsubscribe is idempotent and stops the pump — the
// stream loop is therefore leak-free even when the request context is cancelled
// mid-frame.
func (s *CardServiceImpl) SubscribeCardEvents(cardID uuid.UUID) (<-chan service.CardEvent, func()) {
	if s == nil || s.hub == nil {
		// No hub: a nil channel blocks forever in a select, so the handler's
		// stream loop degrades to snapshot + replay + heartbeat.
		return nil, func() {}
	}

	src, release := s.hub.Subscribe(cardID)
	out := make(chan service.CardEvent, cap(src))
	done := make(chan struct{})
	var once sync.Once

	go func() {
		defer close(out)
		for {
			select {
			case <-done:
				return
			case ev, ok := <-src:
				if !ok {
					return
				}
				select {
				case out <- toServiceCardEvent(ev):
				case <-done:
					return
				default:
					// The HTTP client is slower than its buffer: drop, exactly
					// as the hub does. Replay covers the gap.
				}
			}
		}
	}()

	return out, func() {
		once.Do(func() {
			close(done)
			release()
		})
	}
}
