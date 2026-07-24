package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
)

// SSEAdapter implements TransportAdapter over HTTP/2 Server-Sent Events.
// It wraps the existing SSEHub from internal/sse (SPEC-API-01 §9).
//
// For MVP, SSE is the primary and only fully-implemented transport.
// SPEC-FTR-04 §2 Decision 7.
type SSEAdapter struct {
	hub  sse.SSEHub
	mu   sync.RWMutex
	conns map[string]*sseConnection // conn ID → metadata
}

type sseConnection struct {
	conn     *Connection
	treeID   uuid.UUID
	recvChan chan *Message
}

// NewSSEAdapter wraps an existing SSEHub.
func NewSSEAdapter(hub sse.SSEHub) *SSEAdapter {
	return &SSEAdapter{
		hub:   hub,
		conns: make(map[string]*sseConnection),
	}
}

// TransportType returns the transport type handled by this adapter.
func (a *SSEAdapter) TransportType() TransportType {
	return TransportSSE
}

// Connect validates that the requested transport is SSE, creates a
// Connection record, and registers it. The actual SSE subscription is
// managed by the SSEHub's HTTP handler (GET /trees/{tree_id}/events);
// this method records the connection metadata so that Send/Receive/Disconnect
// can operate on it.
func (a *SSEAdapter) Connect(ctx context.Context, opts ConnectOptions) (*Connection, error) {
	if opts.TransportType != "" && opts.TransportType != TransportSSE {
		return nil, ErrTransportMismatch
	}

	connID := uuid.New().String()
	now := time.Now().UTC()

	treeID, err := uuid.Parse(opts.Target)
	if err != nil {
		// Target is not a tree UUID; use uuid.Nil as a placeholder.
		// This is valid for non-tree-targeted connections.
		treeID = uuid.Nil
	}

	conn := &Connection{
		ID:            connID,
		TransportType: TransportSSE,
		Peer:          opts.Target,
		Metadata:      opts.Metadata,
		State:         StateConnecting,
		EstablishedAt: now,
		LastActivity:  now,
	}

	a.mu.Lock()
	a.conns[connID] = &sseConnection{
		conn:     conn,
		treeID:   treeID,
		recvChan: make(chan *Message, 256),
	}
	a.mu.Unlock()

	conn.State = StateActive
	_ = ctx
	return conn, nil
}

// Send encodes msg as an SSEEvent and broadcasts it via the hub.
// The Message is JSON-encoded into the SSEEvent.Data field.
func (a *SSEAdapter) Send(ctx context.Context, conn *Connection, msg *Message) error {
	if conn == nil {
		return ErrConnectionClosed
	}
	if conn.State == StateClosed {
		return ErrConnectionClosed
	}

	// Validate message size.
	maxSize := int64(1048576) // 1MB SSE default
	if conn.Metadata != nil {
		// Could check custom max from metadata in the future.
		_ = conn.Metadata
	}
	msgData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("transport: encode message: %w", err)
	}
	if int64(len(msgData)) > maxSize {
		return ErrPayloadTooLarge
	}

	a.mu.RLock()
	sc, ok := a.conns[conn.ID]
	a.mu.RUnlock()
	if !ok {
		return ErrConnectionClosed
	}

	treeID := sc.treeID
	if treeID == uuid.Nil {
		// Try to parse from message.
		if tid, err := uuid.Parse(msg.TreeID); err == nil {
			treeID = tid
		}
	}

	event := sse.SSEEvent{
		Type:      msg.Opcode.String(),
		Data:      msgData,
		Timestamp: time.Now().UTC(),
		TreeID:    treeID,
	}

	a.hub.Broadcast(treeID, event)
	conn.LastActivity = time.Now().UTC()
	_ = ctx
	return nil
}

// Receive returns a channel that yields inbound messages for the connection.
// The channel is closed when the connection is disconnected.
func (a *SSEAdapter) Receive(ctx context.Context, conn *Connection) (<-chan *Message, error) {
	if conn == nil {
		return nil, ErrConnectionClosed
	}
	a.mu.RLock()
	sc, ok := a.conns[conn.ID]
	a.mu.RUnlock()
	if !ok {
		return nil, ErrConnectionClosed
	}

	// Bridge context cancellation to channel close.
	go func() {
		select {
		case <-ctx.Done():
			close(sc.recvChan)
		case <-time.After(0):
			// Non-blocking: if ctx is not cancelled, just return.
		}
	}()

	return sc.recvChan, nil
}

// Disconnect removes the connection from the adapter and the hub.
func (a *SSEAdapter) Disconnect(ctx context.Context, conn *Connection) error {
	if conn == nil {
		return nil
	}
	a.mu.Lock()
	sc, ok := a.conns[conn.ID]
	if ok {
		delete(a.conns, conn.ID)
	}
	a.mu.Unlock()

	if !ok {
		return nil // idempotent
	}

	conn.State = StateDisconnecting
	// Unsubscribe from the hub if we have a tree subscription.
	if sc.treeID != uuid.Nil {
		a.hub.Unsubscribe(sc.treeID, conn.ID)
	}
	close(sc.recvChan)
	conn.State = StateClosed
	_ = ctx
	return nil
}

// Health checks that the SSE hub is accessible and accepting connections.
func (a *SSEAdapter) Health(ctx context.Context) error {
	_ = ctx
	// The hub is in-memory; if it exists, it's healthy.
	if a.hub == nil {
		return ErrTransportUnreachable
	}
	return nil
}
