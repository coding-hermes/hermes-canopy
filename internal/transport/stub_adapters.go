package transport

import "context"

// --- Stub adapters (SPEC-FTR-04 §1, Implementation Plan Phase 2–5) -----------
//
// NATSAdapter, WebRTCAdapter, RedisAdapter, and RelayAdapter are placeholders
// for post-MVP implementation. Every method returns ErrTransportUnreachable
// so that the TransportSelector fallback chain and ConnectionManager degrade
// gracefully: a stub transport is treated as permanently unavailable, and the
// selector moves to the next entry in the chain.
//
// TODO(post-MVP): implement each adapter per the phase plan in SPEC-FTR-04 §9.

// --- NATSAdapter (Phase 2) --------------------------------------------------

// NATSAdapter is a stub for the NATS JetStream transport.
// TODO(post-MVP): implement with nats.go + JetStream (SPEC-FTR-04 §9 Phase 2).
type NATSAdapter struct{}

// NewNATSAdapter returns a stub NATS adapter.
func NewNATSAdapter() *NATSAdapter { return &NATSAdapter{} }

func (a *NATSAdapter) TransportType() TransportType { return TransportNATS }

func (a *NATSAdapter) Connect(_ context.Context, _ ConnectOptions) (*Connection, error) {
	return nil, ErrTransportUnreachable
}
func (a *NATSAdapter) Send(_ context.Context, _ *Connection, _ *Message) error {
	return ErrTransportUnreachable
}
func (a *NATSAdapter) Receive(_ context.Context, _ *Connection) (<-chan *Message, error) {
	return nil, ErrTransportUnreachable
}
func (a *NATSAdapter) Disconnect(_ context.Context, _ *Connection) error {
	return ErrTransportUnreachable
}
func (a *NATSAdapter) Health(_ context.Context) error {
	return ErrTransportUnreachable
}

// --- WebRTCAdapter (Phase 4) ------------------------------------------------

// WebRTCAdapter is a stub for the WebRTC DataChannel transport.
// TODO(post-MVP): implement with pion/webrtc (SPEC-FTR-04 §9 Phase 4).
type WebRTCAdapter struct{}

// NewWebRTCAdapter returns a stub WebRTC adapter.
func NewWebRTCAdapter() *WebRTCAdapter { return &WebRTCAdapter{} }

func (a *WebRTCAdapter) TransportType() TransportType { return TransportWebRTC }

func (a *WebRTCAdapter) Connect(_ context.Context, _ ConnectOptions) (*Connection, error) {
	return nil, ErrTransportUnreachable
}
func (a *WebRTCAdapter) Send(_ context.Context, _ *Connection, _ *Message) error {
	return ErrTransportUnreachable
}
func (a *WebRTCAdapter) Receive(_ context.Context, _ *Connection) (<-chan *Message, error) {
	return nil, ErrTransportUnreachable
}
func (a *WebRTCAdapter) Disconnect(_ context.Context, _ *Connection) error {
	return ErrTransportUnreachable
}
func (a *WebRTCAdapter) Health(_ context.Context) error {
	return ErrTransportUnreachable
}

// --- RedisAdapter (Phase 2) -------------------------------------------------

// RedisAdapter is a stub for the Redis Streams transport.
// TODO(post-MVP): implement with go-redis + Consumer Groups (SPEC-FTR-04 §9 Phase 2).
type RedisAdapter struct{}

// NewRedisAdapter returns a stub Redis adapter.
func NewRedisAdapter() *RedisAdapter { return &RedisAdapter{} }

func (a *RedisAdapter) TransportType() TransportType { return TransportRedis }

func (a *RedisAdapter) Connect(_ context.Context, _ ConnectOptions) (*Connection, error) {
	return nil, ErrTransportUnreachable
}
func (a *RedisAdapter) Send(_ context.Context, _ *Connection, _ *Message) error {
	return ErrTransportUnreachable
}
func (a *RedisAdapter) Receive(_ context.Context, _ *Connection) (<-chan *Message, error) {
	return nil, ErrTransportUnreachable
}
func (a *RedisAdapter) Disconnect(_ context.Context, _ *Connection) error {
	return ErrTransportUnreachable
}
func (a *RedisAdapter) Health(_ context.Context) error {
	return ErrTransportUnreachable
}

// --- RelayAdapter (Phase 5) -------------------------------------------------

// RelayAdapter is a stub for the custom binary TCP/QUIC relay transport.
// TODO(post-MVP): implement with binary wire protocol + HMAC-SHA256
// (SPEC-FTR-04 §9 Phase 5).
type RelayAdapter struct{}

// NewRelayAdapter returns a stub relay adapter.
func NewRelayAdapter() *RelayAdapter { return &RelayAdapter{} }

func (a *RelayAdapter) TransportType() TransportType { return TransportRelay }

func (a *RelayAdapter) Connect(_ context.Context, _ ConnectOptions) (*Connection, error) {
	return nil, ErrTransportUnreachable
}
func (a *RelayAdapter) Send(_ context.Context, _ *Connection, _ *Message) error {
	return ErrTransportUnreachable
}
func (a *RelayAdapter) Receive(_ context.Context, _ *Connection) (<-chan *Message, error) {
	return nil, ErrTransportUnreachable
}
func (a *RelayAdapter) Disconnect(_ context.Context, _ *Connection) error {
	return ErrTransportUnreachable
}
func (a *RelayAdapter) Health(_ context.Context) error {
	return ErrTransportUnreachable
}
