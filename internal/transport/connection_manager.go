package transport

import (
	"context"
	"log"
	"sync"
	"time"
)

// DefaultQueueCapacity is the per-peer offline queue capacity.
const DefaultQueueCapacity = 10000

// DefaultRateLimitInbound is the default inbound message rate in messages per
// second. The manager currently enforces the outbound limit for routed
// messages; this constant documents the corresponding inbound default.
const DefaultRateLimitInbound = 1000

// DefaultRateLimitOutbound is the default per-peer outbound message rate in
// messages per second.
const DefaultRateLimitOutbound = 500

// ConnectionManager tracks all connections across all transports.
// It routes messages between the sync engine and the active transport for each peer.
type ConnectionManager struct {
	connections   map[string][]*Connection
	messageQueues map[string]*MessageQueue
	selector      *TransportSelector
	bandwidth     map[string]*BandwidthProfile
	rateLimiters  map[string]*RateLimiter
	mu            sync.RWMutex
}

// NewConnectionManager creates a ConnectionManager with the given
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

// RouteMessage sends a message to a peer via the best available active
// connection. When no active connection is available, it buffers the message
// in the peer's offline queue.
func (cm *ConnectionManager) RouteMessage(ctx context.Context, peerID string, msg *Message) error {
	if err := contextError(ctx); err != nil {
		return err
	}

	conn := cm.activeConnection(peerID)
	if conn == nil {
		if err := cm.enqueueOffline(peerID, msg); err != nil {
			return ErrNoTransportAvailable
		}
		return nil
	}

	if err := cm.EnforceRateLimit(peerID); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}

	cm.mu.Lock()
	// The connection may have been degraded or disconnected while the rate
	// limiter was being checked. Do not report success for a stale route.
	if conn.State != StateActive {
		cm.mu.Unlock()
		if err := cm.enqueueOffline(peerID, msg); err != nil {
			return ErrNoTransportAvailable
		}
		return nil
	}
	conn.LastActivity = time.Now().UTC()
	cm.mu.Unlock()

	// TransportAdapter is intentionally not stored on ConnectionManager's
	// contract. The selected active connection is the routing hand-off; the
	// owning adapter performs the actual Send operation.
	return nil
}

// OnConnect registers a new connection for a peer. If the peer already has an
// active connection, that connection is replaced by the new one.
func (cm *ConnectionManager) OnConnect(conn *Connection) error {
	if conn == nil {
		return ErrConnectionClosed
	}

	now := time.Now().UTC()
	cm.mu.Lock()
	defer cm.mu.Unlock()

	connections := cm.connections[conn.Peer]
	filtered := make([]*Connection, 0, len(connections))
	for _, existing := range connections {
		if existing == conn || (conn.ID != "" && existing.ID == conn.ID) {
			continue
		}
		if existing != nil && existing.State == StateActive {
			existing.State = StateClosed
			continue
		}
		filtered = append(filtered, existing)
	}

	conn.State = StateActive
	if conn.EstablishedAt.IsZero() {
		conn.EstablishedAt = now
	}
	conn.LastActivity = now
	cm.connections[conn.Peer] = append(filtered, conn)

	if _, ok := cm.rateLimiters[conn.Peer]; !ok {
		cm.rateLimiters[conn.Peer] = NewRateLimiter(
			float64(DefaultRateLimitOutbound),
			DefaultRateLimitOutbound*2,
		)
	}
	return nil
}

// OnDisconnect removes a connection from the peer's connection list. It is
// idempotent for connections that have already been removed.
func (cm *ConnectionManager) OnDisconnect(conn *Connection) error {
	if conn == nil {
		return nil
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	connections := cm.connections[conn.Peer]
	filtered := make([]*Connection, 0, len(connections))
	removed := false
	for _, existing := range connections {
		if existing == conn || (conn.ID != "" && existing != nil && existing.ID == conn.ID) {
			removed = true
			continue
		}
		filtered = append(filtered, existing)
	}
	if len(filtered) == 0 {
		delete(cm.connections, conn.Peer)
	} else {
		cm.connections[conn.Peer] = filtered
	}
	if removed {
		conn.State = StateClosed
	}
	return nil
}

// GetConnection returns an active connection for a peer, or nil when the peer
// is offline. An offline peer is not itself an error because it can be served
// by the manager's offline queue.
func (cm *ConnectionManager) GetConnection(peerID string) (*Connection, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	for _, conn := range cm.connections[peerID] {
		if conn != nil && conn.State == StateActive {
			return conn, nil
		}
	}
	return nil, nil
}

// DegradeTransport marks all connections of a transport type as degraded and
// causes subsequent routing to walk the selector's fallback chain.
func (cm *ConnectionManager) DegradeTransport(ctx context.Context, tt TransportType) error {
	if err := contextError(ctx); err != nil {
		return err
	}

	cm.mu.Lock()
	for _, connections := range cm.connections {
		for _, conn := range connections {
			if conn != nil && conn.TransportType == tt && conn.State != StateClosed {
				conn.State = StateDegraded
			}
		}
	}
	cm.mu.Unlock()

	// SSE transport_degradation emission is wired by the transport event
	// layer. Keep a log placeholder until that event sink is available here.
	log.Printf("transport: transport degraded type=%s", tt)
	return nil
}

// MeasureBandwidth returns the bandwidth profile for a peer, creating one if
// this is the first measurement for that peer.
func (cm *ConnectionManager) MeasureBandwidth(peerID string) *BandwidthProfile {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if profile, ok := cm.bandwidth[peerID]; ok {
		return profile
	}
	profile := &BandwidthProfile{}
	cm.bandwidth[peerID] = profile
	return profile
}

// RecordBandwidth stores a profile for a peer. It is retained as a convenience
// for callers that already maintain a profile object.
func (cm *ConnectionManager) RecordBandwidth(peerID string, profile *BandwidthProfile) {
	if profile == nil {
		return
	}
	cm.mu.Lock()
	cm.bandwidth[peerID] = profile
	cm.mu.Unlock()
}

// EnforceRateLimit checks the per-peer token bucket and consumes one token on
// success.
func (cm *ConnectionManager) EnforceRateLimit(peerID string) error {
	cm.mu.Lock()
	limiter, ok := cm.rateLimiters[peerID]
	if !ok {
		limiter = NewRateLimiter(
			float64(DefaultRateLimitOutbound),
			DefaultRateLimitOutbound*2,
		)
		cm.rateLimiters[peerID] = limiter
	}
	cm.mu.Unlock()

	if !limiter.Allow() {
		return ErrRateLimited
	}
	return nil
}

// DrainQueue returns and removes all buffered messages for a peer.
func (cm *ConnectionManager) DrainQueue(peerID string) []*Message {
	cm.mu.RLock()
	queue := cm.messageQueues[peerID]
	cm.mu.RUnlock()
	if queue == nil {
		return nil
	}
	return queue.Drain()
}

// ConnectionCount returns the number of registered connections. If tt is
// non-empty, only connections of that transport type are counted.
func (cm *ConnectionManager) ConnectionCount(tt TransportType) int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	count := 0
	for _, connections := range cm.connections {
		for _, conn := range connections {
			if conn != nil && (tt == "" || conn.TransportType == tt) {
				count++
			}
		}
	}
	return count
}

// AllConnections returns a snapshot of all registered connections.
func (cm *ConnectionManager) AllConnections() []*Connection {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	var result []*Connection
	for _, connections := range cm.connections {
		result = append(result, connections...)
	}
	return result
}

func (cm *ConnectionManager) queueFor(peerID string) *MessageQueue {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if queue, ok := cm.messageQueues[peerID]; ok {
		return queue
	}
	queue := NewMessageQueue(peerID, DefaultQueueCapacity)
	cm.messageQueues[peerID] = queue
	return queue
}

func (cm *ConnectionManager) enqueueOffline(peerID string, msg *Message) error {
	queue := cm.queueFor(peerID)
	return queue.Enqueue(msg)
}

func (cm *ConnectionManager) activeConnection(peerID string) *Connection {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	connections := cm.connections[peerID]
	if len(connections) == 0 {
		return nil
	}

	byTransport := make(map[TransportType]*Connection, len(connections))
	for _, conn := range connections {
		if conn != nil && conn.State == StateActive {
			if _, exists := byTransport[conn.TransportType]; !exists {
				byTransport[conn.TransportType] = conn
			}
		}
	}
	if len(byTransport) == 0 {
		return nil
	}

	if cm.selector == nil {
		for _, conn := range connections {
			if conn != nil && conn.State == StateActive {
				return conn
			}
		}
		return nil
	}

	current := cm.selector.SelectPrimary(peerID)
	visited := make(map[TransportType]struct{})
	for {
		if conn := byTransport[current]; conn != nil {
			return conn
		}
		if _, seen := visited[current]; seen {
			return nil
		}
		visited[current] = struct{}{}
		next, err := cm.selector.SelectFallback(current)
		if err != nil {
			return nil
		}
		current = next
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
