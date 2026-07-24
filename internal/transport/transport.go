package transport

import (
	"context"
	"crypto/tls"
	"time"
)

// --- TransportAdapter interface (SPEC-FTR-04 §3.1) ------------------------

// TransportAdapter is the uniform interface for all sync transports.
// Implementations: SSEAdapter, WebRTCAdapter, NATSAdapter, RedisAdapter,
// RelayAdapter.
type TransportAdapter interface {
	// Connect establishes a transport connection to the peer described
	// in opts. Returns a Connection in StateActive on success.
	Connect(ctx context.Context, opts ConnectOptions) (*Connection, error)

	// Send transmits msg over conn. Returns ErrPayloadTooLarge if the
	// message exceeds the transport's MaxMessageSize, ErrConnectionClosed
	// if conn is not active, ErrSendTimeout on timeout.
	Send(ctx context.Context, conn *Connection, msg *Message) error

	// Receive subscribes to inbound messages on conn, returning a
	// channel that yields messages until conn is closed or ctx is
	// cancelled.
	Receive(ctx context.Context, conn *Connection) (<-chan *Message, error)

	// Disconnect tears down conn. Idempotent: calling on a closed
	// connection returns nil.
	Disconnect(ctx context.Context, conn *Connection) error

	// Health checks the transport backend's liveness. Returns nil if
	// the backend is reachable.
	Health(ctx context.Context) error
}

// --- ConnectOptions (SPEC-FTR-04 §3.1) -------------------------------------

// ConnectOptions configures a new transport connection.
type ConnectOptions struct {
	Target         string
	TransportType  TransportType
	Auth           AuthMaterial
	Metadata       map[string]string
	TLSConfig      *tls.Config
	Timeout        time.Duration
	MaxMessageSize int64
}

// --- Connection (SPEC-FTR-04 §3.1) -----------------------------------------

// Connection represents an active transport connection to a peer.
type Connection struct {
	ID                string
	TransportType     TransportType
	Peer              string
	Metadata          map[string]string
	State             ConnectionState
	EstablishedAt     time.Time
	LastActivity      time.Time
	SequenceWatermark uint64
}

// --- ConnectionState (SPEC-FTR-04 §3.1, §3.6) -------------------------------

// ConnectionState models the lifecycle of a transport connection.
type ConnectionState int

const (
	StateInit         ConnectionState = iota
	StateConnecting
	StateActive
	StateDegraded
	StateDisconnecting
	StateClosed
)

// String returns the lowercase state name used in JSON payloads and DDL.
func (s ConnectionState) String() string {
	switch s {
	case StateInit:
		return "init"
	case StateConnecting:
		return "connecting"
	case StateActive:
		return "active"
	case StateDegraded:
		return "degraded"
	case StateDisconnecting:
		return "disconnecting"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// ParseConnectionState converts a string to ConnectionState.
// Returns StateInit for unrecognised values.
func ParseConnectionState(s string) ConnectionState {
	switch s {
	case "init":
		return StateInit
	case "connecting":
		return StateConnecting
	case "active":
		return StateActive
	case "degraded":
		return StateDegraded
	case "disconnecting":
		return StateDisconnecting
	case "closed":
		return StateClosed
	default:
		return StateInit
	}
}

// --- TransportType (SPEC-FTR-04 §3.1) --------------------------------------

// TransportType enumerates all supported transports.
type TransportType string

const (
	TransportSSE    TransportType = "sse"
	TransportWebRTC TransportType = "webrtc"
	TransportNATS   TransportType = "nats"
	TransportRedis  TransportType = "redis"
	TransportRelay  TransportType = "relay"
)

// AllTransportTypes returns all known transport types in spec order.
func AllTransportTypes() []TransportType {
	return []TransportType{
		TransportSSE,
		TransportWebRTC,
		TransportNATS,
		TransportRedis,
		TransportRelay,
	}
}

// --- AuthMaterial (SPEC-FTR-04 §3.1) ---------------------------------------

// AuthMaterial carries transport-specific credentials.
type AuthMaterial struct {
	Token    string
	CertPEM  []byte
	KeyPEM   []byte
	HMACKey  []byte
	Username string
}

// --- Sentinel errors (SPEC-FTR-04 §3.3) -------------------------------------
//
// Sentinel errors are defined in errors.go to keep this file focused on
// types and interfaces. See errors.go for the full list.

// --- Capability constants (SPEC-FTR-04 §3.5) -------------------------------

const (
	CapBinary        = "binary"
	CapOrdered       = "ordered"
	CapReliable      = "reliable"
	CapBidirectional = "bidirectional"
	CapOfflineQueue  = "offline_queue"
	CapP2P           = "p2p"
)
