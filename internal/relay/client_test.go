package relay

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

func clientConfig(addr string, key []byte) DeploymentConfig {
	cfg := DefaultConfig()
	cfg.Mode, cfg.Enabled, cfg.ConnectAddr = ModeSelfHosted, true, "tcp://"+addr
	cfg.HMACKeyID, cfg.HMACKey, cfg.HeartbeatSecs = 3, key, 1
	return cfg
}

func waitClientState(t *testing.T, client *RelayClient, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if state, _ := client.ClientHealth(); state == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	state, last := client.ClientHealth()
	t.Fatalf("client state=%q last_error=%q, want %q", state, last, want)
}

func TestRelayClientLoopbackSymmetricProtocol(t *testing.T) {
	hub, _, addr := startTestHub(t, 2)
	client := NewRelayClient(clientConfig(addr, []byte("test-key")))
	data := make(chan Frame, 1)
	client.SetDataHandler(func(frame Frame) { data <- frame })
	if err := client.Start(context.Background(), clientConfig(addr, []byte("test-key"))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	waitClientState(t, client, ClientConnected, time.Second)

	// A heartbeat crosses client -> hub -> client as PING/PONG; remaining
	// connected past one interval validates both symmetric control frames.
	time.Sleep(1100 * time.Millisecond)
	waitClientState(t, client, ClientConnected, time.Second)
	if err := hub.BroadcastToTenant(uuid.Nil, Frame{Type: FrameData, Payload: []byte("server-data")}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-data:
		if string(got.Payload) != "server-data" {
			t.Fatalf("payload=%q", got.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("client DATA handler not called")
	}
	if err := client.StopAccepting(); err != nil {
		t.Fatal(err)
	}
	if err := client.NotifyShutdown(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for hub.ActiveSessions() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hub.ActiveSessions() != 0 {
		t.Fatal("client BYE did not close hub session")
	}
}

func TestRelayClientAuthenticationReject(t *testing.T) {
	_, _, addr := startTestHub(t, 1)
	cfg := clientConfig(addr, []byte("wrong-key"))
	client := NewRelayClient(cfg)
	client.backoffMax = 20 * time.Millisecond
	if err := client.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	waitClientState(t, client, ClientBackoff, time.Second)
	_, last := client.ClientHealth()
	if last == "" {
		t.Fatal("authentication rejection missing last error")
	}
}

func TestRelayClientReconnectsAfterServerClose(t *testing.T) {
	hub, _, addr := startTestHub(t, 2)
	cfg := clientConfig(addr, []byte("test-key"))
	client := NewRelayClient(cfg)
	client.backoffMax = 20 * time.Millisecond
	if err := client.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	waitClientState(t, client, ClientConnected, time.Second)
	hub.mu.RLock()
	for _, session := range hub.sessions {
		_ = session.Conn.Close()
		break
	}
	hub.mu.RUnlock()
	waitClientState(t, client, ClientBackoff, time.Second)
	// Reconnect = dial + HELLO handshake; under package-parallel load that can
	// exceed 1s (observed: fatal saw state=connected AFTER the deadline fired).
	waitClientState(t, client, ClientConnected, 5*time.Second)
}

func TestRelayClientHonorsServerBye(t *testing.T) {
	hub, _, addr := startTestHub(t, 1)
	cfg := clientConfig(addr, []byte("test-key"))
	client := NewRelayClient(cfg)
	if err := client.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	waitClientState(t, client, ClientConnected, time.Second)
	hub.mu.RLock()
	var id string
	for id = range hub.sessions {
		break
	}
	hub.mu.RUnlock()
	if err := hub.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	waitClientState(t, client, ClientDisconnected, time.Second)
}

func TestCGNATFallbackDecision(t *testing.T) {
	cfg := clientConfig("relay.example:9443", []byte("key"))
	cfg.ListenAddr = "tcp://203.0.113.10:9443"
	tests := []struct {
		name string
		err  error
		cfg  DeploymentConfig
		want bool
	}{
		{"address unavailable", &net.OpError{Op: "listen", Err: syscall.EADDRNOTAVAIL}, cfg, true},
		{"successful outbound probe", errors.Join(errors.New("listen failed"), errCGNATProbeSucceeded), cfg, true},
		{"ordinary listen failure", syscall.EADDRINUSE, cfg, false},
		{"saas never falls back", syscall.EADDRNOTAVAIL, func() DeploymentConfig { c := cfg; c.Mode = ModeSaaS; return c }(), false},
		{"missing connect address", syscall.EADDRNOTAVAIL, func() DeploymentConfig { c := cfg; c.ConnectAddr = ""; return c }(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldFallbackToRelayClient(tt.err, tt.cfg); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

type listenFailureTransport struct{ fakeTransport }

func (f *listenFailureTransport) Start(context.Context, DeploymentConfig) error {
	return &net.OpError{Op: "listen", Err: syscall.EADDRNOTAVAIL}
}

func TestRelayServiceAutomaticallyFallsBackToClient(t *testing.T) {
	cfg := clientConfig("127.0.0.1:1", []byte("key"))
	cfg.ListenAddr = "tcp://203.0.113.10:9443"
	svc, err := NewRelayService(cfg, &listenFailureTransport{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.transport.(*RelayClient); !ok {
		t.Fatalf("fallback transport = %T", svc.transport)
	}
	if got := svc.Health(); got.Status != StatusRunning || got.ClientState == "" {
		t.Fatalf("fallback health = %+v", got)
	}
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRelayServiceClientHealthTransitions(t *testing.T) {
	_, _, addr := startTestHub(t, 1)
	cfg := clientConfig(addr, []byte("test-key"))
	client := NewRelayClient(cfg)
	svc, err := NewRelayService(cfg, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.Health(); got.ClientState != ClientDisconnected {
		t.Fatalf("initial health=%+v", got)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitClientState(t, client, ClientConnected, time.Second)
	if got := svc.Health(); got.ClientState != ClientConnected || got.Sessions != 1 {
		t.Fatalf("connected health=%+v", got)
	}
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := svc.Health(); got.Status != StatusDisabled || got.ClientState != ClientDisconnected {
		t.Fatalf("shutdown health=%+v", got)
	}
}
