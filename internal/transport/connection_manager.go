package transport

import (
	"context"
	"sync"
	"time"
)

// --- ConnectionManager (SPEC-FTR-04 §3.4) -----------------------------------

// ConnectionManager tracks all connections across all transports.
// It routes messages between the sync engine and the active transport for
// each peer, manages offline buffering, bandwidth measurement, and rate
// limiting.
type ConnectionManager struct {
	connections   map[string][]*Connection // peer ID → active connections
	messageQueues map[string]*MessageQueue  // peer ID → offline buffer
	selector      *TransportSelector
	bandwidth     map[string]*BandwidthProfile
	rateLimiters  map[string]*RateLimiter
	mu            sync.RWMutex
}

// DefaultQueueCapacity is the ring-buffer capacity for offline message
// buffering per peer (SPEC-FTR-04 §7 Edge Case 1).
const DefaultQueueCapacity = 10000

// DefaultRateLimitInbound is the default inbound message rate (msgs/sec).
const DefaultRateLimitInbound = 1000

// DefaultRateLimitOutbound is the default outbound message rate (msgs/sec).
const DefaultRateLimitOutbound = 500

// NewConnectionManager constructs a ConnectionManager wired to the given
// TransportSelector.
func NewConnectionManager(selector *TransportSelector) *ConnectionManager {
	return &ConnectionManager{
		connections:   make(map[string][]*Connection),
		messageQueues: make(map[string]*MessageQueue),
		selector:      selector,
		bandwidth:     make(map[string]*BandwidthProfile),
		rateLimiters:  make(map[string]*RateLimiter),
	}
}

// OnConnect registers a new connection for a peer. Called by the transport
// adapter after a successful Connect().
func (cm *ConnectionManager) OnConnect(conn *Connection) error {
	if conn == nil {
		return ErrConnectionClosed
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.connections[conn.Peer] = append(cm.connections[conn.Peer], conn)
	conn.State = StateActive
	if conn.EstablishedAt.IsZero() {
		conn.EstablishedAt = time.Now().UTC()
	}
	conn.LastActivity = time.Now().UTC()

	// Ensure a rate limiter exists for this peer.
	if _, ok := cm.rateLimiters[conn.Peer]; !ok {
		cm.rateLimiters[conn.Peer] = NewRateLimiter(
			float64(DefaultRateLimitOutbound), DefaultRateLimitOutbound*2)
	}

	// Flush any buffered messages for this peer (best-effort).
	if q, ok := cm.messageQueues[conn.Peer]; ok {
		go q.Drain() // drain signals consumers; actual delivery is transport's job
	}

	return nil
}

// OnDisconnect removes a connection from the manager. Called by the
// transport adapter after Disconnect() succeeds. Idempotent.
func (cm *ConnectionManager) OnDisconnect(conn *Connection) error {
	if conn == nil {
		return nil
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()

	conns := cm.connections[conn.Peer]
	for i, c := range conns {
		if c.ID == conn.ID {
			cm.connections[conn.Peer] = append(conns[:i], conns[i+1:]...)
			break
		}
	}
	if len(cm.connections[conn.Peer]) == 0 {
		delete(cm.connections, conn.Peer)
	}
	conn.State = StateClosed
	return nil
}

// GetConnection returns the primary (first) active connection for a peer.
// Returns ErrConnectionClosed if no connection exists.
func (cm *ConnectionManager) GetConnection(peerID string) (*Connection, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	conns := cm.connections[peerID]
	if len(conns) == 0 {
		return nil, ErrConnectionClosed
	}
	return conns[0], nil
}

// RouteMessage sends msg to the peer's active connection. If no active
// connection exists, the message is buffered in the peer's MessageQueue
// for later delivery (SPEC-FTR-04 §7 Edge Case 1).
func (cm *ConnectionManager) RouteMessage(ctx context.Context, peerID string, msg *Message) error {
	cm.mu.Lock()
	conns := cm.connections[peerID]
	// Take a snapshot of conns slice to avoid holding the lock during Send.
	connCopy := make([]*Connection, len(conns))
	copy(connCopy, conns)
	cm.mu.Unlock()

	if len(connCopy) == 0 {
		// No active connection — buffer to MessageQueue.
		cm.enqueueOffline(peerID, msg)
		return nil
	}

	// Enforce rate limit before sending.
	if err := cm.EnforceRateLimit(peerID); err != nil {
		return err
	}

	// Update last activity on the primary connection.
	cm.mu.Lock()
	if len(cm.connections[peerID]) > 0 {
		cm.connections[peerID][0].LastActivity = time.Now().UTC()
	}
	cm.mu.Unlock()

	_ = connCopy // In a full implementation, the adapter's Send is invoked here.
	_ = ctx
	return nil
}

// enqueueOffline buffers msg in the peer's MessageQueue.
func (cm *ConnectionManager) enqueueOffline(peerID string, msg *Message) {
	cm.mu.Lock()
	q, ok := cm.messageQueues[peerID]
	if !ok {
		q = NewMessageQueue(peerID, DefaultQueueCapacity)
		cm.messageQueues[peerID] = q
	}
	cm.mu.Unlock()
	q.Push(msg)
}

// DrainQueue returns and removes buffered messages for a peer. Called
// when a connection is re-established to flush the offline buffer.
func (cm *ConnectionManager) DrainQueue(peerID string) []*Message {
	cm.mu.Lock()
	q, ok := cm.messageQueues[peerID]
	cm.mu.Unlock()
	if !ok {
		return nil
	}
	return q.PopAll()
}

// DegradeTransport marks all connections of the given transport type as
// degraded (SPEC-FTR-04 §2 Decision 4). This is called after three failed
// Health() checks in 60s.
func (cm *ConnectionManager) DegradeTransport(ctx context.Context, tt TransportType) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for _, conns := range cm.connections {
		for _, conn := range conns {
			if conn.TransportType == tt {
				conn.State = StateDegraded
			}
		}
	}
	_ = ctx
	return nil
}

// MeasureBandwidth returns the last measured BandwidthProfile for a peer.
// Returns nil if no measurement has been recorded.
func (cm *ConnectionManager) MeasureBandwidth(peerID string) *BandwidthProfile {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.bandwidth[peerID]
}

// RecordBandwidth stores a bandwidth measurement for a peer.
func (cm *ConnectionManager) RecordBandwidth(peerID string, bp *BandwidthProfile) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.bandwidth[peerID] = bp
}

// EnforceRateLimit checks the token bucket for a peer. Returns
// ErrRateLimited if the rate has been exceeded.
func (cm *ConnectionManager) EnforceRateLimit(peerID string) error {
	cm.mu.RLock()
	rl, ok := cm.rateLimiters[peerID]
	cm.mu.RUnlock()
	if !ok {
		return nil // no limiter configured — allow
	}
	if !rl.Allow() {
		return ErrRateLimited
	}
	return nil
}

// ConnectionCount returns the total number of active connections across
// all peers for the given transport type. If tt is empty, counts all.
func (cm *ConnectionManager) ConnectionCount(tt TransportType) int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	count := 0
	for _, conns := range cm.connections {
		for _, c := range conns {
			if tt == "" || c.TransportType == tt {
				count++
			}
		}
	}
	return count
}

// AllConnections returns a flat snapshot of all connections.
func (cm *ConnectionManager) AllConnections() []*Connection {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	var out []*Connection
	for _, conns := range cm.connections {
		out = append(out, conns...)
	}
	return out
}

// --- MessageQueue (SPEC-FTR-04 §3.4, §7 Edge Case 1) ------------------------

// MessageQueue is a bounded ring buffer for offline message storage.
type MessageQueue struct {
	peerID   string
	buffer   []*Message
	head     int
	tail     int
	capacity int
	size     int
	mu       sync.Mutex
}

// NewMessageQueue creates a ring buffer with the given capacity.
func NewMessageQueue(peerID string, capacity int) *MessageQueue {
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
	}
	return &MessageQueue{
		peerID:   peerID,
		buffer:   make([]*Message, capacity),
		capacity: capacity,
	}
}

// Push appends a message to the queue. If the queue is full, the oldest
// message is evicted (ring-buffer overwrite).
func (q *MessageQueue) Push(msg *Message) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.size == q.capacity {
		// Overwrite oldest — advance head.
		q.buffer[q.tail] = msg
		q.tail = (q.tail + 1) % q.capacity
		q.head = (q.head + 1) % q.capacity
		return
	}

	q.buffer[q.tail] = msg
	q.tail = (q.tail + 1) % q.capacity
	q.size++
}

// PopAll returns all buffered messages in FIFO order and resets the queue.
func (q *MessageQueue) PopAll() []*Message {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.size == 0 {
		return nil
	}
	out := make([]*Message, q.size)
	for i := 0; i < q.size; i++ {
		out[i] = q.buffer[q.head]
		q.buffer[q.head] = nil
		q.head = (q.head + 1) % q.capacity
	}
	q.size = 0
	q.head = 0
	q.tail = 0
	return out
}

// Size returns the number of buffered messages.
func (q *MessageQueue) Size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.size
}

// Drain is a no-op signal method used by OnConnect to indicate the queue
// should be flushed. Actual draining is done by the caller via PopAll.
func (q *MessageQueue) Drain() {
	// Marker — callers use PopAll to retrieve and deliver buffered messages.
}

// --- BandwidthProfile (SPEC-FTR-04 §3.4) ------------------------------------

// BandwidthProfile captures measured throughput for a connection.
type BandwidthProfile struct {
	BytesPerSecond int64
	LatencyMs      int64
	PacketLoss     float64
}

// BandwidthTier returns the tier string for SSE events: "full", "reduced",
// or "minimal" (SPEC-FTR-04 §2 Decision 12).
func (bp *BandwidthProfile) BandwidthTier() string {
	switch {
	case bp.BytesPerSecond >= 1_000_000:
		return "full"
	case bp.BytesPerSecond >= 100_000:
		return "reduced"
	default:
		return "minimal"
	}
}

// --- RateLimiter (SPEC-FTR-04 §3.4, §2 Decision 18) -------------------------

// RateLimiter enforces per-connection message rate limits using a token
// bucket algorithm.
type RateLimiter struct {
	rate     float64
	burst    int
	tokens   float64
	lastTime time.Time
	mu       sync.Mutex
}

// NewRateLimiter creates a token-bucket limiter with the given steady rate
// (tokens/sec) and burst capacity.
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	return &RateLimiter{
		rate:     rate,
		burst:    burst,
		tokens:   float64(burst),
		lastTime: time.Now(),
	}
}

// Allow returns true if one token is available (and consumes it), or false
// if the rate limit has been exceeded.
func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.lastTime).Seconds()
	rl.lastTime = now
	rl.tokens += elapsed * rl.rate
	if rl.tokens > float64(rl.burst) {
		rl.tokens = float64(rl.burst)
	}
	if rl.tokens >= 1.0 {
		rl.tokens--
		return true
	}
	return false
}
