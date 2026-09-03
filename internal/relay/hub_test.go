package relay

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func startTestHub(t *testing.T, limit int) (*RelayHub, *FrameAuthenticator, string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Mode = ModeSelfHosted
	cfg.Enabled = true
	cfg.ListenAddr = "tcp://127.0.0.1:0"
	cfg.MaxSessions = limit
	cfg.HMACKeyID = 3
	cfg.HMACKey = []byte("test-key")
	h := NewRelayHub(cfg)
	if err := h.Start(context.Background(), cfg); err != nil {
		if errors.Is(err, errors.ErrUnsupported) || strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("loopback sockets unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, NewFrameAuthenticator(3, []byte("test-key"), 0, nil), h.listener.Addr().String()
}

func dialHello(t *testing.T, addr string, auth *FrameAuthenticator) (net.Conn, Frame) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	f, err := auth.Sign(Frame{Type: FrameHello, KeyID: 3, Payload: []byte{0xa0}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := EncodeFrame(f)
	if _, err = c.Write(b); err != nil {
		t.Fatal(err)
	}
	ack, err := ReadFrame(c)
	if err != nil {
		t.Fatal(err)
	}
	return c, ack
}

func TestRelayHubHandshakePingAndBye(t *testing.T) {
	h, auth, addr := startTestHub(t, 2)
	c, ack := dialHello(t, addr, auth)
	defer c.Close()
	if ack.Type != FrameHelloAck || auth.Verify(ack) != nil {
		t.Fatalf("ack = %#v", ack)
	}
	ping, _ := auth.Sign(Frame{Type: FramePing, KeyID: 3, Payload: []byte("beat")})
	b, _ := EncodeFrame(ping)
	_, _ = c.Write(b)
	pong, err := ReadFrame(c)
	if err != nil || pong.Type != FramePong || string(pong.Payload) != "beat" || auth.Verify(pong) != nil {
		t.Fatalf("pong=%#v err=%v", pong, err)
	}
	bye, _ := auth.Sign(Frame{Type: FrameBye, KeyID: 3})
	b, _ = EncodeFrame(bye)
	_, _ = c.Write(b)
	deadline := time.Now().Add(time.Second)
	for h.ActiveSessions() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if h.ActiveSessions() != 0 {
		t.Fatal("session not removed")
	}
}

func TestRelayHubRejectsAuthenticationAndLimit(t *testing.T) {
	h, auth, addr := startTestHub(t, 1)
	bad := NewFrameAuthenticator(3, []byte("wrong"), 0, nil)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := bad.Sign(Frame{Type: FrameHello, KeyID: 3})
	b, _ := EncodeFrame(f)
	_, _ = c.Write(b)
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = ReadFrame(c); err == nil {
		t.Fatal("bad authentication accepted")
	}
	_ = c.Close()

	first, _ := dialHello(t, addr, auth)
	defer first.Close()
	second, got := dialHello(t, addr, auth)
	defer second.Close()
	if got.Type != FrameError || auth.Verify(got) != nil {
		t.Fatalf("limit response = %#v", got)
	}
	if h.ActiveSessions() != 1 {
		t.Fatalf("sessions = %d", h.ActiveSessions())
	}
}

func TestRelayHubDrainMidSession(t *testing.T) {
	h, auth, addr := startTestHub(t, 1)
	c, _ := dialHello(t, addr, auth)
	defer c.Close()
	if err := h.StopAccepting(); err != nil {
		t.Fatal(err)
	}
	if err := h.NotifyShutdown(); err != nil {
		t.Fatal(err)
	}
	bye, err := ReadFrame(c)
	if err != nil || bye.Type != FrameBye || auth.Verify(bye) != nil {
		t.Fatalf("bye=%#v err=%v", bye, err)
	}
	_ = c.Close()
	select {
	case <-h.DrainDone():
	case <-time.After(time.Second):
		t.Fatal("drain did not complete")
	}
	if _, err = net.Dial("tcp", addr); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("listener still accepted: %v", err)
	}
}
