package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type optionalSSEAdapter struct{}

func (optionalSSEAdapter) TransportType() TransportType { return TransportSSE }
func (optionalSSEAdapter) Connect(context.Context, ConnectOptions) (*Connection, error) {
	return nil, errors.New("unused")
}
func (optionalSSEAdapter) Send(context.Context, *Connection, *Message) error {
	return errors.New("unused")
}
func (optionalSSEAdapter) Receive(context.Context, *Connection) (<-chan *Message, error) {
	return nil, errors.New("unused")
}
func (optionalSSEAdapter) Disconnect(context.Context, *Connection) error { return nil }
func (optionalSSEAdapter) Health(context.Context) error                  { return nil }

func TestNewProductionBusRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		cfg  ProductionBusConfig
	}{
		{"malformed URL", ProductionBusConfig{URL: "://bad"}},
		{"missing credentials", ProductionBusConfig{URL: "nats://localhost:4222", Credentials: "/does/not/exist.creds"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if bus, err := NewProductionBus(tt.cfg); err == nil || bus != nil {
				t.Fatalf("NewProductionBus() = (%T, %v), want nil, error", bus, err)
			}
		})
	}
}

func TestBuildNATSOptionsMapsTransportConfig(t *testing.T) {
	cfg := ProductionBusConfig{ConnectTimeout: 7 * time.Second, RetryMax: 11, Heartbeat: 13 * time.Second}
	opts := nats.GetDefaultOptions()
	for _, option := range buildNATSOptions(cfg, func(*nats.Conn, error) {}, func(*nats.Conn) {}) {
		if err := option(&opts); err != nil {
			t.Fatal(err)
		}
	}
	if opts.Timeout != cfg.ConnectTimeout || opts.MaxReconnect != cfg.RetryMax || opts.PingInterval != cfg.Heartbeat {
		t.Fatalf("mapped options = timeout %v, retries %d, heartbeat %v", opts.Timeout, opts.MaxReconnect, opts.PingInterval)
	}
	if !opts.RetryOnFailedConnect || opts.ReconnectWait != cfg.Heartbeat {
		t.Fatalf("reconnect options = retry %v, wait %v", opts.RetryOnFailedConnect, opts.ReconnectWait)
	}
	if opts.DisconnectedErrCB == nil || opts.ReconnectedCB == nil {
		t.Fatal("connection callbacks were not mapped")
	}
}

type statusNATSBus struct {
	*fakeNATSBus
	handler func(ConnectionState, error)
}

func (b *statusNATSBus) SetStatusHandler(handler func(ConnectionState, error)) { b.handler = handler }

func TestNATSStatusCallbacksMapConnectionState(t *testing.T) {
	bus := &statusNATSBus{fakeNATSBus: newFakeNATSBus()}
	adapter := NewNATSAdapter(bus)
	conn, err := adapter.Connect(context.Background(), ConnectOptions{Target: "nats://localhost:4222"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Disconnect(context.Background(), conn) }()
	for _, tt := range []struct {
		name     string
		in, want ConnectionState
	}{{"disconnected", StateDegraded, StateDegraded}, {"reconnected", StateActive, StateActive}} {
		t.Run(tt.name, func(t *testing.T) {
			bus.handler(tt.in, nil)
			if conn.State != tt.want {
				t.Fatalf("state = %s, want %s", conn.State, tt.want)
			}
		})
	}
}

func TestOptionalNATSWiringUnsetLeavesSelectorSSEOnly(t *testing.T) {
	selector := NewTransportSelector(ModeLocal, TopologyLoopback)
	manager := NewConnectionManager(selector)
	sse := optionalSSEAdapter{}
	if err := manager.RegisterAdapter(sse); err != nil {
		t.Fatal(err)
	}
	got, err := selector.Select("tree")
	if err != nil || got != TransportSSE {
		t.Fatalf("Select() = (%s, %v), want SSE", got, err)
	}
	if _, exists := selector.adapters[TransportNATS]; exists {
		t.Fatal("NATS adapter registered with optional wiring unset")
	}
}
