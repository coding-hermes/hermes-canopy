package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

const defaultWebRTCMaxMessageSize int64 = 256 << 10

// ICEServer is the dependency-neutral form of a WebRTC ICE server. A future
// pion integration translates it to pion.Configuration without exposing pion
// above this boundary.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type WebRTCConfig struct{ ICEServers []ICEServer }

type SessionDescription struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

type ICECandidate struct {
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdp_mid,omitempty"`
	SDPMLineIndex uint16 `json:"sdp_mline_index,omitempty"`
}

type WebRTCSignal struct {
	Type        string              `json:"type"`
	From        string              `json:"from,omitempty"`
	To          string              `json:"to,omitempty"`
	Description *SessionDescription `json:"description,omitempty"`
	Candidate   *ICECandidate       `json:"candidate,omitempty"`
}

// SignalingChannel is implemented by the existing SSE control-message path.
// It deliberately models control messages, rather than another network server.
type SignalingChannel interface {
	SendSignal(context.Context, WebRTCSignal) error
	ReceiveSignals(context.Context, string) (<-chan WebRTCSignal, error)
}

// SSESignalingChannel carries WebRTC controls as heartbeat messages over an
// already-connected SSE adapter. This is the concrete no-new-server signaling
// path; "webrtc_signal" in Origin keeps controls distinguishable from normal
// sync heartbeats.
type SSESignalingChannel struct {
	adapter TransportAdapter
	conn    *Connection
}

func NewSSESignalingChannel(adapter TransportAdapter, conn *Connection) *SSESignalingChannel {
	return &SSESignalingChannel{adapter: adapter, conn: conn}
}

func (s *SSESignalingChannel) SendSignal(ctx context.Context, signal WebRTCSignal) error {
	if s == nil || s.adapter == nil || s.conn == nil || s.conn.TransportType != TransportSSE {
		return ErrTransportMismatch
	}
	payload, err := json.Marshal(signal)
	if err != nil {
		return fmt.Errorf("transport: encode WebRTC signal: %w", err)
	}
	return s.adapter.Send(ctx, s.conn, &Message{Opcode: OpHeartbeat, TreeID: signal.To, Timestamp: time.Now().UnixMilli(), Payload: payload, Origin: "webrtc_signal"})
}

func (s *SSESignalingChannel) ReceiveSignals(ctx context.Context, id string) (<-chan WebRTCSignal, error) {
	if s == nil || s.adapter == nil || s.conn == nil || s.conn.TransportType != TransportSSE {
		return nil, ErrTransportMismatch
	}
	messages, err := s.adapter.Receive(ctx, s.conn)
	if err != nil {
		return nil, err
	}
	out := make(chan WebRTCSignal, 32)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-messages:
				if !ok {
					return
				}
				if msg == nil || msg.Opcode != OpHeartbeat || msg.Origin != "webrtc_signal" {
					continue
				}
				var signal WebRTCSignal
				if json.Unmarshal(msg.Payload, &signal) != nil || signal.To != id {
					continue
				}
				select {
				case out <- signal:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

type PeerConnectionState string

const (
	PeerStateNew          PeerConnectionState = "new"
	PeerStateChecking     PeerConnectionState = "checking"
	PeerStateConnected    PeerConnectionState = "connected"
	PeerStateDisconnected PeerConnectionState = "disconnected"
	PeerStateFailed       PeerConnectionState = "failed"
	PeerStateClosed       PeerConnectionState = "closed"
)

// MapPeerConnectionState implements the specified connectionstatechange map.
func MapPeerConnectionState(s PeerConnectionState) ConnectionState {
	switch s {
	case PeerStateChecking, PeerStateNew:
		return StateConnecting
	case PeerStateConnected:
		return StateActive
	default:
		return StateDegraded
	}
}

type PeerConnection interface {
	CreateOffer(context.Context) (SessionDescription, error)
	CreateAnswer(context.Context) (SessionDescription, error)
	SetLocalDescription(context.Context, SessionDescription) error
	SetRemoteDescription(context.Context, SessionDescription) error
	AddICECandidate(context.Context, ICECandidate) error
	OnICECandidate(func(ICECandidate))
	OnConnectionStateChange(func(PeerConnectionState))
	OnMessage(func([]byte))
	Send(context.Context, []byte) error
	State() PeerConnectionState
	Close() error
}

type PeerConnectionFactory interface {
	NewPeerConnection(context.Context, WebRTCConfig) (PeerConnection, error)
}

type unavailablePeerFactory struct{}

func (unavailablePeerFactory) NewPeerConnection(context.Context, WebRTCConfig) (PeerConnection, error) {
	return nil, ErrTransportUnreachable
}

type unavailableSignaling struct{}

func (unavailableSignaling) SendSignal(context.Context, WebRTCSignal) error {
	return ErrTransportUnreachable
}
func (unavailableSignaling) ReceiveSignals(context.Context, string) (<-chan WebRTCSignal, error) {
	return nil, ErrTransportUnreachable
}

type WebRTCAdapter struct {
	factory   PeerConnectionFactory
	signaling SignalingChannel
	mu        sync.RWMutex
	conns     map[string]*webRTCConnection
}

type WebRTCTransportAdapter = WebRTCAdapter

type webRTCConnection struct {
	conn      *Connection
	peer      PeerConnection
	recv      chan *Message
	done      chan struct{}
	cancel    context.CancelFunc
	maxSize   int64
	signalID  string
	delivery  sync.RWMutex
	closed    bool
	closeOnce sync.Once
}

func NewWebRTCAdapter(deps ...interface{}) *WebRTCAdapter {
	a := &WebRTCAdapter{factory: unavailablePeerFactory{}, signaling: unavailableSignaling{}, conns: make(map[string]*webRTCConnection)}
	for _, dep := range deps {
		if v, ok := dep.(PeerConnectionFactory); ok && v != nil {
			a.factory = v
		}
		if v, ok := dep.(SignalingChannel); ok && v != nil {
			a.signaling = v
		}
	}
	return a
}

func NewWebRTCTransportAdapter(factory PeerConnectionFactory, signaling SignalingChannel) *WebRTCTransportAdapter {
	return NewWebRTCAdapter(factory, signaling)
}

func (a *WebRTCAdapter) TransportType() TransportType { return TransportWebRTC }

func (a *WebRTCAdapter) Connect(ctx context.Context, opts ConnectOptions) (*Connection, error) {
	if opts.TransportType != "" && opts.TransportType != TransportWebRTC {
		return nil, ErrTransportMismatch
	}
	if err := contextError(ctx); err != nil {
		return nil, errors.Join(ErrConnectionFailed, err)
	}
	if opts.Target == "" {
		return nil, errors.Join(ErrConnectionFailed, errors.New("transport: empty WebRTC peer"))
	}
	ice, err := webRTCICEConfig(opts)
	if err != nil {
		return nil, errors.Join(ErrConnectionFailed, err)
	}
	peer, err := a.factory.NewPeerConnection(ctx, ice)
	if err != nil {
		return nil, errors.Join(ErrConnectionFailed, err)
	}
	now := time.Now().UTC()
	conn := &Connection{ID: uuid.NewString(), TransportType: TransportWebRTC, Peer: opts.Target, TenantID: opts.TenantID, Metadata: cloneMetadata(opts.Metadata), State: StateConnecting, EstablishedAt: now, LastActivity: now}
	maxSize := opts.MaxMessageSize
	if maxSize <= 0 || maxSize > defaultWebRTCMaxMessageSize {
		maxSize = defaultWebRTCMaxMessageSize
	}
	signalCtx, cancel := context.WithCancel(context.Background())
	signalID := opts.Metadata["signal_id"]
	if signalID == "" {
		signalID = conn.ID
	}
	wc := &webRTCConnection{conn: conn, peer: peer, recv: make(chan *Message, 256), done: make(chan struct{}), cancel: cancel, maxSize: maxSize, signalID: signalID}
	peer.OnConnectionStateChange(func(s PeerConnectionState) { a.setPeerState(wc, s) })
	peer.OnMessage(func(data []byte) { wc.deliver(data) })
	peer.OnICECandidate(func(candidate ICECandidate) {
		_ = a.signaling.SendSignal(signalCtx, WebRTCSignal{Type: "ice_candidate", From: wc.signalID, To: conn.Peer, Candidate: &candidate})
	})
	signals, err := a.signaling.ReceiveSignals(signalCtx, signalID)
	if err != nil {
		cancel()
		_ = peer.Close()
		return nil, errors.Join(ErrConnectionFailed, err)
	}
	a.mu.Lock()
	a.conns[conn.ID] = wc
	a.mu.Unlock()
	go a.handleSignals(signalCtx, wc, signals)
	if opts.Metadata["webrtc_role"] != "answerer" {
		offer, offerErr := peer.CreateOffer(ctx)
		if offerErr == nil {
			offerErr = peer.SetLocalDescription(ctx, offer)
		}
		if offerErr == nil {
			offerErr = a.signaling.SendSignal(ctx, WebRTCSignal{Type: "offer", From: wc.signalID, To: conn.Peer, Description: &offer})
		}
		if offerErr != nil {
			_ = a.Disconnect(context.Background(), conn)
			return nil, errors.Join(ErrConnectionFailed, offerErr)
		}
	}
	return conn, nil
}

func (a *WebRTCAdapter) handleSignals(ctx context.Context, wc *webRTCConnection, signals <-chan WebRTCSignal) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			switch sig.Type {
			case "offer":
				if sig.Description == nil || wc.peer.SetRemoteDescription(ctx, *sig.Description) != nil {
					continue
				}
				answer, err := wc.peer.CreateAnswer(ctx)
				if err != nil {
					continue
				}
				if wc.peer.SetLocalDescription(ctx, answer) != nil {
					continue
				}
				_ = a.signaling.SendSignal(ctx, WebRTCSignal{Type: "answer", From: wc.signalID, To: wc.conn.Peer, Description: &answer})
			case "answer":
				if sig.Description != nil {
					_ = wc.peer.SetRemoteDescription(ctx, *sig.Description)
				}
			case "ice_candidate":
				if sig.Candidate != nil {
					_ = wc.peer.AddICECandidate(ctx, *sig.Candidate)
				}
			}
		}
	}
}

func (a *WebRTCAdapter) setPeerState(wc *webRTCConnection, state PeerConnectionState) {
	mu := stateMuFor(wc.conn)
	mu.Lock()
	if wc.conn.State != StateDisconnecting && wc.conn.State != StateClosed {
		wc.conn.State = MapPeerConnectionState(state)
	}
	mu.Unlock()
}

func (a *WebRTCAdapter) Send(ctx context.Context, conn *Connection, msg *Message) error {
	if conn == nil {
		return ErrConnectionClosed
	}
	mu := stateMuFor(conn)
	mu.RLock()
	active := conn.State == StateActive
	mu.RUnlock()
	if !active {
		return ErrConnectionClosed
	}
	if msg == nil || msg.Opcode < OpTreeCreate || msg.Opcode > OpAck {
		return ErrUnsupportedOpcode
	}
	if err := contextError(ctx); err != nil {
		return errors.Join(ErrSendTimeout, err)
	}
	a.mu.RLock()
	wc := a.conns[conn.ID]
	a.mu.RUnlock()
	if wc == nil {
		return ErrNotConnected
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("transport: encode message: %w", err)
	}
	if int64(len(data)) > wc.maxSize {
		return ErrPayloadTooLarge
	}
	if err := wc.peer.Send(ctx, data); err != nil {
		if contextError(ctx) != nil {
			return errors.Join(ErrSendTimeout, err)
		}
		return err
	}
	mu.Lock()
	conn.LastActivity = time.Now().UTC()
	mu.Unlock()
	return nil
}

func (a *WebRTCAdapter) Receive(ctx context.Context, conn *Connection) (<-chan *Message, error) {
	if conn == nil {
		return nil, ErrConnectionClosed
	}
	a.mu.RLock()
	wc := a.conns[conn.ID]
	a.mu.RUnlock()
	if wc == nil {
		return nil, ErrNotConnected
	}
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = a.Disconnect(context.Background(), conn)
			case <-wc.done:
			}
		}()
	}
	return wc.recv, nil
}

func (a *WebRTCAdapter) Disconnect(_ context.Context, conn *Connection) error {
	if conn == nil {
		return nil
	}
	a.mu.Lock()
	wc := a.conns[conn.ID]
	delete(a.conns, conn.ID)
	a.mu.Unlock()
	mu := stateMuFor(conn)
	mu.Lock()
	if conn.State == StateClosed {
		mu.Unlock()
		return nil
	}
	conn.State = StateDisconnecting
	mu.Unlock()
	if wc != nil {
		wc.cancel()
		wc.delivery.Lock()
		wc.closed = true
		wc.closeOnce.Do(func() { close(wc.done); close(wc.recv) })
		wc.delivery.Unlock()
		_ = wc.peer.Close()
	}
	mu.Lock()
	conn.State = StateClosed
	mu.Unlock()
	return nil
}

func (a *WebRTCAdapter) Health(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.conns) == 0 {
		return ErrTransportUnreachable
	}
	for _, wc := range a.conns {
		if wc.peer.State() == PeerStateConnected {
			return nil
		}
	}
	return ErrTransportUnreachable
}

// Shutdown closes all Pion peer connections owned by the adapter, including
// connections that have not yet been handed to ConnectionManager.
func (a *WebRTCAdapter) Shutdown(ctx context.Context) error {
	a.mu.RLock()
	connections := make([]*Connection, 0, len(a.conns))
	for _, wc := range a.conns {
		connections = append(connections, wc.conn)
	}
	a.mu.RUnlock()
	var result error
	for _, conn := range connections {
		result = errors.Join(result, a.Disconnect(ctx, conn))
	}
	return result
}

func (wc *webRTCConnection) deliver(data []byte) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil || msg.Opcode < OpTreeCreate || msg.Opcode > OpAck {
		return
	}
	wc.delivery.RLock()
	defer wc.delivery.RUnlock()
	if wc.closed {
		return
	}
	select {
	case wc.recv <- &msg:
		mu := stateMuFor(wc.conn)
		mu.Lock()
		wc.conn.LastActivity = time.Now().UTC()
		wc.conn.SequenceWatermark = msg.Sequence
		mu.Unlock()
	default:
	}
}

func webRTCICEConfig(opts ConnectOptions) (WebRTCConfig, error) {
	servers := append([]ICEServer(nil), opts.ICEServers...)
	if len(servers) == 0 && opts.ConfigJSON != nil {
		if raw, ok := opts.ConfigJSON["ice_servers"]; ok {
			data, err := json.Marshal(raw)
			if err != nil {
				return WebRTCConfig{}, err
			}
			if err := json.Unmarshal(data, &servers); err != nil {
				return WebRTCConfig{}, fmt.Errorf("transport: invalid ice_servers: %w", err)
			}
		}
	}
	turnURL, user, cred := opts.TURNURL, opts.TURNUser, opts.TURNCred
	if opts.ConfigJSON != nil {
		if turnURL == "" {
			turnURL, _ = opts.ConfigJSON["turn_url"].(string)
		}
		if user == "" {
			user, _ = opts.ConfigJSON["turn_user"].(string)
		}
		if cred == "" {
			cred, _ = opts.ConfigJSON["turn_cred"].(string)
		}
	}
	if turnURL != "" {
		servers = append(servers, ICEServer{URLs: []string{turnURL}, Username: user, Credential: cred})
	}
	return WebRTCConfig{ICEServers: servers}, nil
}

var _ TransportAdapter = (*WebRTCAdapter)(nil)
