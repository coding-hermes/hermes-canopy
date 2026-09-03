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
	Conn         net.Conn
	Established  time.Time
	LastActivity time.Time
	done         chan struct{}
	closeOnce    sync.Once
	writeMu      sync.Mutex
}

type RelayHub struct {
	mu           sync.RWMutex
	listener     net.Listener
	sessions     map[string]*RelaySession
	sessionLimit int
	auth         *FrameAuthenticator
	keyID        uint16
	drainDone    chan struct{}
	drainOnce    sync.Once
	closed       chan struct{}
	closeOnce    sync.Once
	wg           sync.WaitGroup
}

func NewRelayHub(cfg DeploymentConfig) *RelayHub {
	return &RelayHub{
		sessions: make(map[string]*RelaySession), sessionLimit: cfg.MaxSessions,
		auth:  NewFrameAuthenticator(uint16(cfg.HMACKeyID), cfg.HMACKey, uint16(cfg.HMACKeyPrevID), cfg.HMACKeyPrev),
		keyID: uint16(cfg.HMACKeyID), drainDone: make(chan struct{}), closed: make(chan struct{}),
	}
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

	h.mu.Lock()
	if h.listener == nil || len(h.sessions) >= h.sessionLimit {
		h.mu.Unlock()
		h.write(conn, Frame{Type: FrameError, Payload: encodeCBORText("session limit reached")})
		_ = conn.Close()
		return nil, errors.New("relay: session limit reached")
	}
	s := &RelaySession{ID: uuid.NewString(), Conn: conn, Established: time.Now(), LastActivity: time.Now(), done: make(chan struct{})}
	h.sessions[s.ID] = s
	h.mu.Unlock()
	_ = conn.SetReadDeadline(time.Time{})
	if err := h.writeSession(s, Frame{Type: FrameHelloAck, Payload: encodeCBORText(s.ID)}); err != nil {
		h.removeSession(s)
		return nil, err
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
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
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
	f.KeyID = h.keyID
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
