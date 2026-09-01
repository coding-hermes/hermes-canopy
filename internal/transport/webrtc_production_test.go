package transport

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestPionFactoryMapsICEServers(t *testing.T) {
	f := NewPionPeerConnectionFactory()
	peer, err := f.NewPeerConnection(context.Background(), WebRTCConfig{ICEServers: []ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}, {URLs: []string{"turn:turn.example.test:3478"}, Username: "u", Credential: "p"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	p := peer.(*pionPeerConnection)
	got := p.pc.GetConfiguration().ICEServers
	if len(got) != 2 || got[1].Username != "u" || got[1].Credential != "p" {
		t.Fatalf("ICE servers = %#v", got)
	}
}

func TestPionStateMapping(t *testing.T) {
	cases := map[PeerConnectionState]ConnectionState{PeerStateNew: StateConnecting, PeerStateChecking: StateConnecting, PeerStateConnected: StateActive, PeerStateDisconnected: StateDegraded, PeerStateFailed: StateDegraded, PeerStateClosed: StateDegraded}
	for in, want := range cases {
		if got := MapPeerConnectionState(in); got != want {
			t.Errorf("MapPeerConnectionState(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestPionDataChannelRoundTrip(t *testing.T) {
	probe, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback unavailable in sandbox: %v", err)
	}
	_ = probe.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := NewPionPeerConnectionFactory()
	a, err := f.NewPeerConnection(ctx, WebRTCConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := f.NewPeerConnection(ctx, WebRTCConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	a.OnICECandidate(func(c ICECandidate) { _ = b.AddICECandidate(ctx, c) })
	b.OnICECandidate(func(c ICECandidate) { _ = a.AddICECandidate(ctx, c) })
	received := make(chan string, 1)
	b.OnMessage(func(data []byte) { received <- string(data) })
	offer, err := a.CreateOffer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.SetLocalDescription(ctx, offer); err != nil {
		t.Fatal(err)
	}
	if err = b.SetRemoteDescription(ctx, offer); err != nil {
		t.Fatal(err)
	}
	answer, err := b.CreateAnswer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.SetLocalDescription(ctx, answer); err != nil {
		t.Fatal(err)
	}
	if err = a.SetRemoteDescription(ctx, answer); err != nil {
		t.Fatal(err)
	}
	for a.State() != PeerStateConnected {
		select {
		case <-ctx.Done():
			t.Fatal("Pion peers did not connect")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := a.Send(ctx, []byte("canopy")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got != "canopy" {
			t.Fatalf("message = %q", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for DataChannel message")
	}
}

func TestWebRTCDisabledByDefault(t *testing.T) {
	t.Setenv("CANOPY_WEBRTC_ENABLED", "")
	cm := NewConnectionManager(NewTransportSelector(ModeLocal, TopologyLoopback))
	enabled, err := RegisterPionAdapterFromEnv(cm, nil)
	if err != nil || enabled || cm.HasAdapter(TransportWebRTC) {
		t.Fatalf("enabled=%v registered=%v err=%v", enabled, cm.HasAdapter(TransportWebRTC), err)
	}
}
