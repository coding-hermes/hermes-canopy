package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
)

const helloTimeout = 10 * time.Second

type RelaySession struct {
	ID           string
	InstanceID   uuid.UUID
	TenantID     uuid.UUID
	Tier         string
	Conn         net.Conn
	Established  time.Time
	LastActivity time.Time
	done         chan struct{}
	closeOnce    sync.Once
	writeMu      sync.Mutex
}

type RelayHub struct {
	mu              sync.RWMutex
	listener        net.Listener
	sessions        map[string]*RelaySession
	sessionLimit    int
	auth            *FrameAuthenticator
	keyID           uint16
	drainDone       chan struct{}
	drainOnce       sync.Once
	closed          chan struct{}
	closeOnce       sync.Once
	wg              sync.WaitGroup
	heartbeat       func(context.Context, uuid.UUID) error
	instanceID      uuid.UUID
	tenantRequired  bool
	resolveInstance func(context.Context, uuid.UUID) (uuid.UUID, string, error)
	openSession     func(context.Context, *RelaySession) error
	rateLimiter     *TenantRateLimiter
}

// SetHeartbeatHook wires registry liveness updates without changing the relay protocol.
func (h *RelayHub) SetHeartbeatHook(instanceID uuid.UUID, hook func(context.Context, uuid.UUID) error) {
	h.instanceID, h.heartbeat = instanceID, hook
}

// SetTenantHooks wires SaaS identity lookup and durable session recording.
func (h *RelayHub) SetTenantHooks(
	resolve func(context.Context, uuid.UUID) (uuid.UUID, string, error),
	open func(context.Context, *RelaySession) error,
) {
	h.resolveInstance, h.openSession = resolve, open
}

func NewRelayHub(cfg DeploymentConfig) *RelayHub {
	auth := NewFrameAuthenticator(uint16(cfg.HMACKeyID), cfg.HMACKey, uint16(cfg.HMACKeyPrevID), cfg.HMACKeyPrev)
	if len(cfg.HMACKeyPrev) > 0 && cfg.HMACKeyRotatedAt != nil {
		auth.SetKeys(map[uint16]authenticationKey{
			uint16(cfg.HMACKeyID):     {key: cfg.HMACKey},
			uint16(cfg.HMACKeyPrevID): {key: cfg.HMACKeyPrev, expiresAt: cfg.HMACKeyRotatedAt.Add(hmacKeyGracePeriod)},
		})
	}
	return &RelayHub{
		sessions: make(map[string]*RelaySession), sessionLimit: cfg.MaxSessions,
		auth:  auth,
		keyID: uint16(cfg.HMACKeyID), drainDone: make(chan struct{}), closed: make(chan struct{}),
		tenantRequired: cfg.Mode == ModeSaaS, rateLimiter: NewTenantRateLimiter(time.Minute),
	}
}

func (h *RelayHub) updateHMACKeys(currentID uint16, keys map[uint16]authenticationKey) {
	h.auth.SetKeys(keys)
	h.mu.Lock()
	h.keyID = currentID
	h.mu.Unlock()
}

func (h *RelayHub) Start(ctx context.Context, cfg DeploymentConfig) error {
	u, err := url.Parse(cfg.ListenAddr)
	if err != nil {
		return err
	}
	if u.Scheme != "tcp" {
		return fmt.Errorf("relay: listener scheme %q is not implemented", u.Scheme)
	}
	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		return fmt.Errorf("relay: listen: %w", err)
	}
	h.mu.Lock()
	h.listener = ln
	h.mu.Unlock()
	h.wg.Add(1)
	go h.acceptLoop(ctx, ln)
	return nil
}

func (h *RelayHub) acceptLoop(ctx context.Context, ln net.Listener) {
	defer h.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		h.wg.Add(1)
		go func() { defer h.wg.Done(); h.serve(ctx, conn) }()
	}
}

func (h *RelayHub) serve(ctx context.Context, conn net.Conn) {
	s, err := h.acceptConn(conn)
	if err != nil {
		return
	}
	_ = h.HandleConnection(ctx, s)
}

// Accept accepts and authenticates one connection. The service accept loop uses
// the same handshake through acceptConn, but keeps handshakes concurrent.
func (h *RelayHub) Accept(ctx context.Context) (*RelaySession, error) {
	h.mu.RLock()
	ln := h.listener
	h.mu.RUnlock()
	if ln == nil {
		return nil, net.ErrClosed
	}
	conn, err := ln.Accept()
	if err != nil {
		return nil, err
	}
	return h.acceptConn(conn)
}

func (h *RelayHub) acceptConn(conn net.Conn) (*RelaySession, error) {
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	f, err := ReadFrame(conn)
	if err != nil || f.Type != FrameHello || h.auth.Verify(f) != nil {
		_ = conn.Close()
		return nil, ErrAuthFailed
	}

	var instanceID, tenantID uuid.UUID
	tier := ""
	// Phase 5 extends HELLO with the registered instance UUID as a raw 16-byte
	// payload. Empty/non-UUID payloads remain legal only for self-hosted mode.
	if len(f.Payload) == 16 {
		copy(instanceID[:], f.Payload)
	}
	if h.tenantRequired {
		if instanceID == uuid.Nil || h.resolveInstance == nil {
			_ = conn.Close()
			return nil, ErrAuthFailed
		}
		var resolveErr error
		tenantID, tier, resolveErr = h.resolveInstance(context.Background(), instanceID)
		if resolveErr != nil || tenantID == uuid.Nil {
			_ = conn.Close()
			return nil, ErrAuthFailed
		}
	}
	h.mu.Lock()
	if h.listener == nil || len(h.sessions) >= h.sessionLimit {
		h.mu.Unlock()
		h.write(conn, Frame{Type: FrameError, Payload: encodeCBORText("session limit reached")})
		_ = conn.Close()
		return nil, errors.New("relay: session limit reached")
	}
	s := &RelaySession{ID: uuid.NewString(), InstanceID: instanceID, TenantID: tenantID, Tier: tier, Conn: conn, Established: time.Now(), LastActivity: time.Now(), done: make(chan struct{})}
	h.sessions[s.ID] = s
	h.mu.Unlock()
	if h.openSession != nil && instanceID != uuid.Nil {
		if err := h.openSession(context.Background(), s); err != nil {
			h.removeSession(s)
			return nil, err
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	if err := h.writeSession(s, Frame{Type: FrameHelloAck, Payload: encodeCBORText(s.ID)}); err != nil {
		h.removeSession(s)
		return nil, err
	}
	if h.heartbeat != nil && h.instanceID != uuid.Nil {
		_ = h.heartbeat(context.Background(), h.instanceID)
	}
	return s, nil
}

// encodeCBORText covers the fixed, short text payloads used by Phase 2 control
// frames without introducing a general-purpose CBOR dependency.
func encodeCBORText(value string) []byte {
	n := len(value)
	if n < 24 {
		return append([]byte{0x60 | byte(n)}, value...)
	}
	return append([]byte{0x78, byte(n)}, value...)
}

func encodeCBORError(code string) []byte {
	return append([]byte{0xa1, 0x64, 'c', 'o', 'd', 'e'}, encodeCBORText(code)...)
}

// HandleConnection processes control frames until the session closes.
func (h *RelayHub) HandleConnection(ctx context.Context, s *RelaySession) error {
	defer h.removeSession(s)
	conn := s.Conn
	for {
		f, err := ReadFrame(conn)
		if err != nil {
			return err
		}
		if h.auth.Verify(f) != nil {
			return ErrAuthFailed
		}
		s.LastActivity = time.Now()
		switch f.Type {
		case FramePing:
			if err := h.writeSession(s, Frame{Type: FramePong, Payload: f.Payload}); err != nil {
				return err
			}
		case FrameBye:
			return nil
		case FrameData:
			if s.TenantID != uuid.Nil && h.resolveInstance != nil {
				tenantID, tier, err := h.resolveInstance(ctx, s.InstanceID)
				if err != nil || tenantID != s.TenantID {
					return ErrTenantIsolation
				}
				s.Tier = tier
			}
			if s.TenantID != uuid.Nil && !h.rateLimiter.Allow(s.TenantID, s.Tier) {
				if err := h.writeSession(s, Frame{Type: FrameError, Payload: encodeCBORError("RATE_LIMITED")}); err != nil {
					return err
				}
				continue
			}
			if len(f.Payload) < 16 {
				_ = h.writeSession(s, Frame{Type: FrameError, Payload: encodeCBORError("INVALID_TARGET")})
				continue
			}
			var target uuid.UUID
			copy(target[:], f.Payload[:16])
			if err := h.RouteToInstance(s, target, f); err != nil {
				_ = h.writeSession(s, Frame{Type: FrameError, Payload: encodeCBORError(err.Error())})
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

var ErrTenantIsolation = errors.New("TENANT_ISOLATION")

// RouteToInstance forwards a DATA frame only when source and destination have
// the same tenant. The destination UUID occupies the first 16 payload bytes.
func (h *RelayHub) RouteToInstance(source *RelaySession, target uuid.UUID, frame Frame) error {
	h.mu.RLock()
	var destination *RelaySession
	for _, candidate := range h.sessions {
		if candidate.InstanceID == target {
			destination = candidate
			break
		}
	}
	h.mu.RUnlock()
	if destination == nil {
		return errors.New("INSTANCE_NOT_CONNECTED")
	}
	if source.TenantID != destination.TenantID {
		return ErrTenantIsolation
	}
	return h.writeSession(destination, frame)
}

func (h *RelayHub) BroadcastToTenant(tenantID uuid.UUID, frame Frame) error {
	h.mu.RLock()
	sessions := make([]*RelaySession, 0)
	for _, s := range h.sessions {
		if s.TenantID == tenantID {
			sessions = append(sessions, s)
		}
	}
	h.mu.RUnlock()
	var err error
	for _, s := range sessions {
		err = errors.Join(err, h.writeSession(s, frame))
	}
	return err
}

// CloseSession sends BYE and removes the named live session.
func (h *RelayHub) CloseSession(sessionID string) error {
	h.mu.RLock()
	s := h.sessions[sessionID]
	h.mu.RUnlock()
	if s == nil {
		return errors.New("relay: session not found")
	}
	err := h.writeSession(s, Frame{Type: FrameBye})
	h.removeSession(s)
	return err
}

func (h *RelayHub) writeSession(s *RelaySession, f Frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return h.write(s.Conn, f)
}

func (h *RelayHub) write(conn net.Conn, f Frame) error {
	h.mu.RLock()
	keyID := h.keyID
	h.mu.RUnlock()
	f.KeyID = keyID
	signed, err := h.auth.Sign(f)
	if err != nil {
		return err
	}
	b, err := EncodeFrame(signed)
	if err != nil {
		return err
	}
	_, err = conn.Write(b)
	return err
}

func (h *RelayHub) removeSession(s *RelaySession) {
	s.closeOnce.Do(func() { _ = s.Conn.Close(); close(s.done) })
	h.mu.Lock()
	delete(h.sessions, s.ID)
	empty := len(h.sessions) == 0
	accepting := h.listener != nil
	h.mu.Unlock()
	if empty && !accepting {
		h.drainOnce.Do(func() { close(h.drainDone) })
	}
}

func (h *RelayHub) StopAccepting() error {
	h.mu.Lock()
	ln := h.listener
	h.listener = nil
	empty := len(h.sessions) == 0
	h.mu.Unlock()
	if empty {
		h.drainOnce.Do(func() { close(h.drainDone) })
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}

func (h *RelayHub) NotifyShutdown() error {
	h.mu.RLock()
	sessions := make([]*RelaySession, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.RUnlock()
	var err error
	for _, s := range sessions {
		err = errors.Join(err, h.writeSession(s, Frame{Type: FrameBye}))
	}
	return err
}

func (h *RelayHub) ActiveSessions() int        { h.mu.RLock(); defer h.mu.RUnlock(); return len(h.sessions) }
func (h *RelayHub) DrainDone() <-chan struct{} { return h.drainDone }

func (h *RelayHub) Close() error {
	_ = h.StopAccepting()
	h.mu.RLock()
	sessions := make([]*RelaySession, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.RUnlock()
	for _, s := range sessions {
		_ = s.Conn.Close()
	}
	h.wg.Wait()
	h.closeOnce.Do(func() { close(h.closed) })
	return nil
}
