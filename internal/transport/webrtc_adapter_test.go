package transport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeSignaling struct {
	mu    sync.Mutex
	chans map[string]chan WebRTCSignal
	sent  []WebRTCSignal
}

func newFakeSignaling() *fakeSignaling {
	return &fakeSignaling{chans: make(map[string]chan WebRTCSignal)}
}
func (s *fakeSignaling) ReceiveSignals(_ context.Context, id string) (<-chan WebRTCSignal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.chans[id]
	if ch == nil {
		ch = make(chan WebRTCSignal, 16)
		s.chans[id] = ch
	}
	return ch, nil
}
func (s *fakeSignaling) SendSignal(_ context.Context, signal WebRTCSignal) error {
	s.mu.Lock()
	s.sent = append(s.sent, signal)
	ch := s.chans[signal.To]
	if ch == nil {
		ch = make(chan WebRTCSignal, 16)
		s.chans[signal.To] = ch
	}
	s.mu.Unlock()
	ch <- signal
	return nil
}

type fakePeerFactory struct {
	mu      sync.Mutex
	peers   []*fakePeer
	configs []WebRTCConfig
}

func (f *fakePeerFactory) NewPeerConnection(_ context.Context, cfg WebRTCConfig) (PeerConnection, error) {
	p := &fakePeer{state: PeerStateChecking}
	f.mu.Lock()
	f.peers = append(f.peers, p)
	f.configs = append(f.configs, cfg)
	f.mu.Unlock()
	return p, nil
}

type fakePeer struct {
	mu         sync.Mutex
	state      PeerConnectionState
	onICE      func(ICECandidate)
	onState    func(PeerConnectionState)
	onMessage  func([]byte)
	remote     []SessionDescription
	candidates []ICECandidate
	closed     bool
}

func (p *fakePeer) CreateOffer(context.Context) (SessionDescription, error) {
	return SessionDescription{Type: "offer", SDP: "offer-sdp"}, nil
}
func (p *fakePeer) CreateAnswer(context.Context) (SessionDescription, error) {
	return SessionDescription{Type: "answer", SDP: "answer-sdp"}, nil
}
func (p *fakePeer) SetLocalDescription(context.Context, SessionDescription) error { return nil }
func (p *fakePeer) SetRemoteDescription(_ context.Context, d SessionDescription) error {
	p.mu.Lock()
	p.remote = append(p.remote, d)
	p.mu.Unlock()
	return nil
}
func (p *fakePeer) AddICECandidate(_ context.Context, c ICECandidate) error {
	p.mu.Lock()
	p.candidates = append(p.candidates, c)
	p.mu.Unlock()
	return nil
}
func (p *fakePeer) OnICECandidate(fn func(ICECandidate)) { p.mu.Lock(); p.onICE = fn; p.mu.Unlock() }
func (p *fakePeer) OnConnectionStateChange(fn func(PeerConnectionState)) {
	p.mu.Lock()
	p.onState = fn
	p.mu.Unlock()
}
func (p *fakePeer) OnMessage(fn func([]byte)) { p.mu.Lock(); p.onMessage = fn; p.mu.Unlock() }
func (p *fakePeer) Send(_ context.Context, data []byte) error {
	p.mu.Lock()
	fn := p.onMessage
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return ErrConnectionClosed
	}
	if fn != nil {
		fn(append([]byte(nil), data...))
	}
	return nil
}
func (p *fakePeer) State() PeerConnectionState { p.mu.Lock(); defer p.mu.Unlock(); return p.state }
func (p *fakePeer) Close() error {
	p.mu.Lock()
	p.closed = true
	p.state = PeerStateClosed
	p.mu.Unlock()
	return nil
}
func (p *fakePeer) transition(s PeerConnectionState) {
	p.mu.Lock()
	p.state = s
	fn := p.onState
	p.mu.Unlock()
	if fn != nil {
		fn(s)
	}
}

func TestWebRTCOfferAnswerICEAndMessages(t *testing.T) {
	sig, factory := newFakeSignaling(), &fakePeerFactory{}
	a := NewWebRTCTransportAdapter(factory, sig)
	conn, err := a.Connect(context.Background(), ConnectOptions{Target: "peer-b", TransportType: TransportWebRTC, Metadata: map[string]string{"signal_id": "peer-a"}, ConfigJSON: map[string]interface{}{"ice_servers": []interface{}{map[string]interface{}{"urls": []interface{}{"stun:stun.example"}}}, "turn_url": "turn:relay.example", "turn_user": "u", "turn_cred": "p"}})
	if err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	peer := factory.peers[0]
	cfg := factory.configs[0]
	factory.mu.Unlock()
	if len(cfg.ICEServers) != 2 || cfg.ICEServers[1].URLs[0] != "turn:relay.example" {
		t.Fatalf("ICE config = %#v", cfg)
	}
	answer := SessionDescription{Type: "answer", SDP: "answer-sdp"}
	candidate := ICECandidate{Candidate: "candidate:relay"}
	if err := sig.SendSignal(context.Background(), WebRTCSignal{Type: "answer", To: "peer-a", Description: &answer}); err != nil {
		t.Fatal(err)
	}
	if err := sig.SendSignal(context.Background(), WebRTCSignal{Type: "ice_candidate", To: "peer-a", Candidate: &candidate}); err != nil {
		t.Fatal(err)
	}
	peer.transition(PeerStateConnected)
	recv, err := a.Receive(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send(context.Background(), conn, &Message{Opcode: OpNodeAdd, TreeID: "tree", Sequence: 9}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-recv:
		if got.Sequence != 9 {
			t.Fatalf("message=%#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("message timeout")
	}
	deadline := time.Now().Add(time.Second)
	for {
		peer.mu.Lock()
		nRemote, nICE := len(peer.remote), len(peer.candidates)
		peer.mu.Unlock()
		if nRemote == 1 && nICE == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote=%d ICE=%d", nRemote, nICE)
		}
		time.Sleep(time.Millisecond)
	}
	if err := a.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	if conn.State != StateClosed {
		t.Fatalf("state=%s", conn.State)
	}
	if _, ok := <-recv; ok {
		t.Fatal("receive channel open")
	}
	if err := a.Disconnect(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
}

func TestWebRTCAnswererRoundTrip(t *testing.T) {
	sig, factory := newFakeSignaling(), &fakePeerFactory{}
	a := NewWebRTCAdapter(factory, sig)
	_, err := a.Connect(context.Background(), ConnectOptions{Target: "offerer", Metadata: map[string]string{"signal_id": "answerer", "webrtc_role": "answerer"}})
	if err != nil {
		t.Fatal(err)
	}
	offer := SessionDescription{Type: "offer", SDP: "sdp"}
	_ = sig.SendSignal(context.Background(), WebRTCSignal{Type: "offer", From: "offerer", To: "answerer", Description: &offer})
	deadline := time.Now().Add(time.Second)
	for {
		sig.mu.Lock()
		found := false
		for _, s := range sig.sent {
			if s.Type == "answer" {
				found = true
			}
		}
		sig.mu.Unlock()
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("answer not signaled")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMapPeerConnectionState(t *testing.T) {
	tests := map[PeerConnectionState]ConnectionState{PeerStateChecking: StateConnecting, PeerStateConnected: StateActive, PeerStateFailed: StateDegraded, PeerStateDisconnected: StateDegraded, PeerStateClosed: StateDegraded}
	for in, want := range tests {
		if got := MapPeerConnectionState(in); got != want {
			t.Errorf("%s => %s, want %s", in, got, want)
		}
	}
}

func TestWebRTCOversizeAndClosed(t *testing.T) {
	sig, factory := newFakeSignaling(), &fakePeerFactory{}
	a := NewWebRTCAdapter(factory, sig)
	conn, err := a.Connect(context.Background(), ConnectOptions{Target: "b", Metadata: map[string]string{"signal_id": "a"}, MaxMessageSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	p := factory.peers[0]
	factory.mu.Unlock()
	p.transition(PeerStateConnected)
	err = a.Send(context.Background(), conn, &Message{Opcode: OpNodeAdd, TreeID: "tree", Payload: []byte(`"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"`)})
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversize=%v", err)
	}
	_ = a.Disconnect(context.Background(), conn)
	if err := a.Send(context.Background(), conn, &Message{Opcode: OpAck}); !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("closed send=%v", err)
	}
}
