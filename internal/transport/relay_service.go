package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

const DefaultRelayQueueCapacity = 10000

// RelayEnvelope is the authenticated store-and-forward wire object. EventID
// is the idempotency key and PeerID identifies the destination queue.
type RelayEnvelope struct {
	EventID    string   `json:"event_id"`
	PeerID     string   `json:"peer_id"`
	EnqueuedAt int64    `json:"enqueued_at,omitempty"`
	Message    *Message `json:"message"`
}

type relayPeerQueue struct {
	items []*RelayEnvelope
	seen  map[string]struct{}
	order []string
}

// RelayService is both the final TransportAdapter fallback and the HTTP
// store-and-forward service used by remote peers.
type RelayService struct {
	mu       sync.Mutex
	queues   map[string]*relayPeerQueue
	capacity int
	closed   bool
	limiter  *PeerRelayRateLimiter
}

func NewRelayService(capacities ...int) *RelayService {
	capacity := DefaultRelayQueueCapacity
	if len(capacities) > 0 && capacities[0] > 0 {
		capacity = capacities[0]
	}
	return &RelayService{queues: make(map[string]*relayPeerQueue), capacity: capacity, limiter: NewPeerRelayRateLimiter(RelayRequestsPerMinute, time.Minute)}
}

func (s *RelayService) TransportType() TransportType { return TransportRelay }

func (s *RelayService) Connect(_ context.Context, opts ConnectOptions) (*Connection, error) {
	Metrics().IncConnectAttempt(TransportRelay)
	if opts.TransportType != "" && opts.TransportType != TransportRelay {
		Metrics().IncConnectFailure(TransportRelay)
		return nil, ErrTransportMismatch
	}
	if opts.Target == "" {
		Metrics().IncConnectFailure(TransportRelay)
		return nil, ErrConnectionFailed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		Metrics().IncConnectFailure(TransportRelay)
		return nil, ErrConnectionClosed
	}
	now := time.Now().UTC()
	Metrics().IncConnectSuccess(TransportRelay)
	return &Connection{ID: uuid.NewString(), TransportType: TransportRelay, Peer: opts.Target, TenantID: opts.TenantID, Metadata: cloneMetadata(opts.Metadata), State: StateActive, EstablishedAt: now, LastActivity: now}, nil
}

func (s *RelayService) Send(_ context.Context, conn *Connection, msg *Message) error {
	if conn == nil || conn.State != StateActive {
		return ErrConnectionClosed
	}
	return s.Enqueue(&RelayEnvelope{EventID: relayEventID(msg), PeerID: conn.Peer, Message: msg})
}

func (s *RelayService) Receive(context.Context, *Connection) (<-chan *Message, error) {
	return nil, ErrTransportUnreachable
}

func (s *RelayService) Disconnect(_ context.Context, conn *Connection) error {
	if conn != nil {
		conn.State = StateClosed
		Metrics().RecordTransition(TransportRelay)
	}
	return nil
}

func (s *RelayService) Health(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrConnectionClosed
	}
	return nil
}

func (s *RelayService) Enqueue(envelope *RelayEnvelope) error {
	if envelope == nil || envelope.PeerID == "" || envelope.Message == nil {
		return errors.New("transport: invalid relay envelope")
	}
	if envelope.EventID == "" {
		envelope.EventID = relayEventID(envelope.Message)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrConnectionClosed
	}
	q := s.queues[envelope.PeerID]
	if q == nil {
		q = &relayPeerQueue{seen: make(map[string]struct{})}
		s.queues[envelope.PeerID] = q
	}
	if _, duplicate := q.seen[envelope.EventID]; duplicate {
		return nil
	}
	if len(q.order) == s.capacity {
		Metrics().IncRelayDrop()
		delete(q.seen, q.order[0])
		copy(q.order, q.order[1:])
		q.order[len(q.order)-1] = envelope.EventID
	} else {
		q.order = append(q.order, envelope.EventID)
	}
	copyEnvelope := *envelope
	if copyEnvelope.EnqueuedAt == 0 {
		copyEnvelope.EnqueuedAt = time.Now().UnixMilli()
	}
	if len(q.items) == s.capacity {
		copy(q.items, q.items[1:])
		q.items[len(q.items)-1] = &copyEnvelope
	} else {
		q.items = append(q.items, &copyEnvelope)
	}
	q.seen[copyEnvelope.EventID] = struct{}{}
	Metrics().IncRelayEnqueue()
	Metrics().IncMessageSent(TransportRelay)
	return nil
}

func (s *RelayService) Poll(peerID string) []*RelayEnvelope {
	Metrics().IncRelayPoll()
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[peerID]
	if q == nil {
		return []*RelayEnvelope{}
	}
	items := append([]*RelayEnvelope(nil), q.items...)
	q.items = nil
	for range items {
		Metrics().IncMessageReceived(TransportRelay)
	}
	return items
}

func (s *RelayService) QueueDepth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	depth := 0
	for _, q := range s.queues {
		depth += len(q.items)
	}
	return depth
}

// Shutdown stops new enqueue operations and drains all in-memory queues.
func (s *RelayService) Shutdown() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for peer, q := range s.queues {
		count += len(q.items)
		delete(s.queues, peer)
	}
	s.closed = true
	return count
}

func (s *RelayService) Post(w http.ResponseWriter, r *http.Request) {
	var envelope RelayEnvelope
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		relayHTTPError(w, http.StatusBadRequest, "invalid relay envelope")
		return
	}
	if !s.limiter.Allow(envelope.PeerID) {
		relayHTTPError(w, http.StatusTooManyRequests, "relay request rate limit exceeded")
		return
	}
	if err := s.Enqueue(&envelope); err != nil {
		relayHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"event_id": envelope.EventID, "status": "queued"})
}

func (s *RelayService) PollHTTP(w http.ResponseWriter, r *http.Request) {
	peerID := r.URL.Query().Get("peer_id")
	if peerID == "" {
		relayHTTPError(w, http.StatusBadRequest, "peer_id is required")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"envelopes": s.Poll(peerID)})
}

func relayHTTPError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func relayEventID(msg *Message) string {
	if msg == nil {
		return ""
	}
	b, _ := json.Marshal(msg)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var _ TransportAdapter = (*RelayService)(nil)
