package transport

import "context"

// --- Stub adapters (SPEC-FTR-04 §1, Implementation Plan Phase 2–5) -----------
//
// WebRTCAdapter, RedisAdapter, and RelayAdapter are placeholders
// for post-MVP implementation. Every method returns ErrTransportUnreachable
// so that the TransportSelector fallback chain and ConnectionManager degrade
// gracefully: a stub transport is treated as permanently unavailable, and the
// selector moves to the next entry in the chain.
//
// TODO(post-MVP): implement each adapter per the phase plan in SPEC-FTR-04 §9.

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
