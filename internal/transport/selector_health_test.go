package transport_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

type healthAdapter struct {
	tt        transport.TransportType
	mu        sync.Mutex
	healthErr error
	sendErr   error
	sends     int
}

func (a *healthAdapter) TransportType() transport.TransportType { return a.tt }
func (a *healthAdapter) Connect(context.Context, transport.ConnectOptions) (*transport.Connection, error) {
	return nil, nil
}
func (a *healthAdapter) Send(context.Context, *transport.Connection, *transport.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sends++
	return a.sendErr
}

func TestPriorityChains(t *testing.T) {
	tests := []struct {
		mode transport.DeploymentMode
		want []transport.TransportType
	}{
		{transport.ModeLocal, []transport.TransportType{transport.TransportSSE}},
		{transport.ModeLAN, []transport.TransportType{transport.TransportWebRTC, transport.TransportNATS, transport.TransportSSE}},
		{transport.ModeSelfHosted, []transport.TransportType{transport.TransportWebRTC, transport.TransportNATS, transport.TransportSSE, transport.TransportRedis, transport.TransportRelay}},
		{transport.ModeSaaS, []transport.TransportType{transport.TransportWebRTC, transport.TransportNATS, transport.TransportSSE, transport.TransportRelay}},
		{transport.ModeP2P, []transport.TransportType{transport.TransportWebRTC, transport.TransportNATS, transport.TransportSSE, transport.TransportRelay}},
		{transport.ModeFederated, []transport.TransportType{transport.TransportNATS, transport.TransportRedis, transport.TransportWebRTC, transport.TransportRelay}},
		{transport.ModeAirGapped, []transport.TransportType{transport.TransportRelay}},
	}
	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			got := transport.NewTransportSelector(tt.mode, transport.TopologyLoopback).Available()
			if len(got) != len(tt.want) {
				t.Fatalf("chain = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("chain = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestConnectionManagerImmediateFailoverEvent(t *testing.T) {
	s := transport.NewTransportSelector(transport.ModeSaaS, transport.TopologyPublic)
	primary := &healthAdapter{tt: transport.TransportWebRTC, sendErr: transport.ErrConnectionClosed}
	fallback := &healthAdapter{tt: transport.TransportNATS}
	cm := transport.NewConnectionManager(s, primary, fallback)
	events := make(chan transport.TransportEvent, 1)
	s.SetEventHandler(func(e transport.TransportEvent) { events <- e })
	if err := cm.OnConnect(&transport.Connection{ID: "w", Peer: "peer", TransportType: transport.TransportWebRTC}); err != nil {
		t.Fatal(err)
	}
	if err := cm.OnConnect(&transport.Connection{ID: "n", Peer: "peer", TransportType: transport.TransportNATS}); err != nil {
		t.Fatal(err)
	}
	if err := cm.RouteMessage(context.Background(), "peer", &transport.Message{}); err != nil {
		t.Fatalf("failover route: %v", err)
	}
	select {
	case e := <-events:
		if !e.Degraded || e.Type != transport.TransportWebRTC {
			t.Fatalf("event = %#v", e)
		}
	default:
		t.Fatal("no degradation event")
	}
	if fallback.sends != 1 {
		t.Fatalf("fallback sends = %d", fallback.sends)
	}
}
func (a *healthAdapter) Receive(context.Context, *transport.Connection) (<-chan *transport.Message, error) {
	return nil, nil
}
func (a *healthAdapter) Disconnect(context.Context, *transport.Connection) error { return nil }
func (a *healthAdapter) Health(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.healthErr
}
func (a *healthAdapter) setHealth(err error) { a.mu.Lock(); a.healthErr = err; a.mu.Unlock() }

func TestSelectorHysteresisAndFallback(t *testing.T) {
	s := transport.NewTransportSelector(transport.ModeSaaS, transport.TopologyPublic)
	s.HealthSettings(0, 2, 3)
	webrtc, nats := &healthAdapter{tt: transport.TransportWebRTC}, &healthAdapter{tt: transport.TransportNATS}
	if err := s.RegisterAdapter(webrtc); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAdapter(nats); err != nil {
		t.Fatal(err)
	}
	events := make([]transport.TransportEvent, 0, 2)
	h := s.StartHealthMonitor(context.Background(), func(e transport.TransportEvent) { events = append(events, e) })
	defer h.Stop()
	webrtc.setHealth(errors.New("down"))
	h.Check(context.Background())
	h.Check(context.Background())
	if got, _ := s.Select("tree"); got != transport.TransportWebRTC {
		t.Fatalf("selected before threshold = %s", got)
	}
	h.Check(context.Background())
	if got, _ := s.Select("tree"); got != transport.TransportNATS {
		t.Fatalf("fallback = %s", got)
	}
	webrtc.setHealth(nil)
	h.Check(context.Background())
	if s.IsHealthy(transport.TransportWebRTC) {
		t.Fatal("recovered before up threshold")
	}
	h.Check(context.Background())
	if !s.IsHealthy(transport.TransportWebRTC) || len(events) != 2 {
		t.Fatalf("healthy=%v events=%d", s.IsHealthy(transport.TransportWebRTC), len(events))
	}
	nats.setHealth(errors.New("down"))
	for i := 0; i < 3; i++ {
		h.Check(context.Background())
	}
	webrtc.setHealth(errors.New("down"))
	for i := 0; i < 3; i++ {
		h.Check(context.Background())
	}
	if _, err := s.Select("tree"); !errors.Is(err, transport.ErrNoTransport) {
		t.Fatalf("all-down error = %v", err)
	}
}

func TestTransportRepositoryRoundTrip(t *testing.T) {
	t.Setenv("CANOPY_REQUIRE_DB", "1")
	pool := testutil.NewIntegrationPool(t)
	repo := db.NewPGTransportConnectionRepo(pool)
	ctx := context.Background()
	id := uuid.New()
	want := &db.TransportConnection{ID: id, PeerID: "peer", TransportType: "sse", State: "active", Target: "/events", Metadata: map[string]interface{}{"tree": "t"}}
	if _, err := repo.Upsert(ctx, want); err != nil {
		t.Fatal(err)
	}
	want.State = "degraded"
	if _, err := repo.Upsert(ctx, want); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.GetByPeer(ctx, "peer", "sse")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != "degraded" || rows[0].Metadata["tree"] != "t" {
		t.Fatalf("round trip: %#v", rows)
	}
	cfgs, err := db.NewPGTransportConfigRepo(pool).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) < 2 {
		t.Fatalf("configs = %d", len(cfgs))
	}
}
