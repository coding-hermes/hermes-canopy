// Package transport defines the abstract transport interface for Canopy.
// Every transport (SSE, WebRTC, NATS, Relay) implements this interface.
package transport

import (
	"context"
	"time"
)

// Opcode enumerates the relay protocol operations.
type Opcode string

const (
	OpNodeAdded      Opcode = "NODE_ADDED"
	OpNodeUpdated    Opcode = "NODE_UPDATED"
	OpNodeRemoved    Opcode = "NODE_REMOVED"
	OpNodeMoved      Opcode = "NODE_MOVED"
	OpEdgeAdded      Opcode = "EDGE_ADDED"
	OpEdgeRemoved    Opcode = "EDGE_REMOVED"
	OpSnapshot       Opcode = "SNAPSHOT_CREATED"
	OpDelta          Opcode = "DELTA_COMPUTED"
	OpApprovalPend   Opcode = "APPROVAL_PENDING"
	OpApprovalGrant  Opcode = "APPROVAL_GRANTED"
	OpApprovalDeny   Opcode = "APPROVAL_DENIED"
	OpApprovalExpire Opcode = "APPROVAL_EXPIRED"
	OpHeartbeat      Opcode = "HEARTBEAT"
)

// Message is the wire-format envelope shared by all transports.
type Message struct {
	Opcode     Opcode    `json:"op"`
	TreeID     string    `json:"treeId,omitempty"`
	NodeID     string    `json:"nodeId,omitempty"`
	Sequence   int64     `json:"seq,omitempty"`
	Payload    []byte    `json:"payload,omitempty"` // CBOR or JSON
	Timestamp  time.Time `json:"ts"`
}

// ConnectOptions carries parameters for establishing a transport connection.
type ConnectOptions struct {
	TreeID      string
	ProfileID   string
	RelayAddr   string
	Encrypted   bool
	Heartbeat   time.Duration
}

// Connection represents an established transport link.
type Connection struct {
	ID        string
	Transport string // "sse", "webrtc", "nats", "relay"
	CreatedAt time.Time
}

// TransportAdapter defines the interface every transport must implement.
type TransportAdapter interface {
	// Connect establishes a transport connection.
	Connect(ctx context.Context, opts ConnectOptions) (*Connection, error)
	// Send transmits a message over the transport.
	Send(ctx context.Context, msg *Message) error
	// Receive returns a channel of inbound messages.
	Receive(ctx context.Context) (<-chan *Message, error)
	// Disconnect tears down the transport connection.
	Disconnect(ctx context.Context) error
	// Health checks the transport's liveness.
	Health(ctx context.Context) error
}
