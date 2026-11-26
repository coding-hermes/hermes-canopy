package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
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

// firstNonLoopbackIPv4 returns the address of the first interface that is up,
// non-loopback, and has an IPv4 address. Pion ICE excludes loopback
// candidates entirely, so the exchange must run on a real interface.
func firstNonLoopbackIPv4() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				return ipn.IP.String(), nil
			}
		}
	}
	return "", errors.New("no up, non-loopback IPv4 interface found")
}

// TestPionDataChannelRoundTrip exercises the pion adapter end to end: two
// local peer connections exchange trickle ICE over the adapter's candidate
// mapping, connect, and send one message over the negotiated DataChannel.
//
// ICE is deliberately pinned to a single non-loopback interface. Pion gathers
// one host candidate per interface, so an unpinned agent on a host with many
// interfaces (container bridges, veths, VPNs) gathers dozens of candidates,
// needs tens of seconds of gathering alone, and O(N^2) connectivity checks
// that never converge within a test budget — REVIEW-CANOPY-005: the unpinned
// test failed 100% of attempts on a host with ~90 interfaces. Both peers share
// the same host, so one pinned interface yields a single candidate pair that
// connects in seconds (verified with an unfiltered control on a host with a
// normal interface count, where the unpinned path connects as before).
func TestPionDataChannelRoundTrip(t *testing.T) {
	probe, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback unavailable in sandbox: %v", err)
	}
	_ = probe.Close()
	pin, err := firstNonLoopbackIPv4()
	if err != nil {
		// pion cannot gather loopback candidates, so with no real interface
		// the exchange is unsupported on this host — skip with proof.
		t.Skipf("no non-loopback IPv4 interface for ICE pin (%v): pion cannot gather loopback candidates, two-peer exchange unsupported", err)
	}
	var se pion.SettingEngine
	se.SetIPFilter(func(ip net.IP) bool { return ip.String() == pin })
	factory := &PionPeerConnectionFactory{SettingEngine: &se}
	if err := roundTripOnce(t, factory, 20*time.Second); err != nil {
		t.Fatalf("Pion round trip failed: %v", err)
	}
}

func roundTripOnce(t *testing.T, f PeerConnectionFactory, budget time.Duration) (err error) {
	// recover pion panics from closed-peer races on retry
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pion panic: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	a, err := f.NewPeerConnection(ctx, WebRTCConfig{})
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := f.NewPeerConnection(ctx, WebRTCConfig{})
	if err != nil {
		return err
	}
	defer b.Close()
	a.OnICECandidate(func(c ICECandidate) { _ = b.AddICECandidate(ctx, c) })
	b.OnICECandidate(func(c ICECandidate) { _ = a.AddICECandidate(ctx, c) })
	received := make(chan string, 1)
	b.OnMessage(func(data []byte) { received <- string(data) })
	offer, err := a.CreateOffer(ctx)
	if err != nil {
		return err
	}
	if err = a.SetLocalDescription(ctx, offer); err != nil {
		return err
	}
	if err = b.SetRemoteDescription(ctx, offer); err != nil {
		return err
	}
	answer, err := b.CreateAnswer(ctx)
	if err != nil {
		return err
	}
	if err = b.SetLocalDescription(ctx, answer); err != nil {
		return err
	}
	if err = a.SetRemoteDescription(ctx, answer); err != nil {
		return err
	}
	for a.State() != PeerStateConnected {
		select {
		case <-ctx.Done():
			return errors.New("peers did not connect")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := a.Send(ctx, []byte("canopy")); err != nil {
		return err
	}
	select {
	case got := <-received:
		if got != "canopy" {
			return fmt.Errorf("message = %q", got)
		}
		return nil
	case <-ctx.Done():
		return errors.New("timed out waiting for DataChannel message")
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
