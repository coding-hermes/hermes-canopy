package relay

import (
	"context"
	"testing"
	"time"
)

type fakeTransport struct {
	started, stopped, notified, closed bool
	sessions                           int
	drainDone                          chan struct{}
}

func (f *fakeTransport) Start(context.Context, DeploymentConfig) error { f.started = true; return nil }
func (f *fakeTransport) StopAccepting() error                          { f.stopped = true; return nil }
func (f *fakeTransport) NotifyShutdown() error                         { f.notified = true; return nil }
func (f *fakeTransport) ActiveSessions() int                           { return f.sessions }
func (f *fakeTransport) DrainDone() <-chan struct{} {
	if f.drainDone == nil {
		f.drainDone = make(chan struct{})
	}
	return f.drainDone
}
func (f *fakeTransport) Close() error { f.closed = true; return nil }

type fakeClock struct{ ch chan time.Time }

func (f fakeClock) After(time.Duration) <-chan time.Time { return f.ch }

func TestRelayServiceLifecycle(t *testing.T) {
	t.Run("air gapped stays disabled", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Mode = ModeAirGapped
		transport := &fakeTransport{}
		svc, _ := NewRelayService(cfg, transport, nil)
		if err := svc.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := svc.Health(); got.Status != StatusDisabled || got.Sessions != 0 || transport.started {
			t.Fatalf("health = %+v, transport started = %v", got, transport.started)
		}
	})

	t.Run("running and fast shutdown", func(t *testing.T) {
		transport := &fakeTransport{}
		cfg := DefaultConfig()
		cfg.Mode, cfg.Enabled = ModeSelfHosted, true
		svc, _ := NewRelayService(cfg, transport, nil)
		if err := svc.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := svc.Health(); got.Mode != ModeSelfHosted || got.Status != StatusRunning || got.Sessions != 0 {
			t.Fatalf("health = %+v", got)
		}
		if err := svc.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !transport.stopped || !transport.notified || !transport.closed {
			t.Fatalf("drain calls = %+v", transport)
		}
	})

	t.Run("active session reaches fake deadline", func(t *testing.T) {
		transport := &fakeTransport{sessions: 1}
		cfg := DefaultConfig()
		cfg.Mode, cfg.Enabled = ModeSelfHosted, true
		// Rotation off (interval<=0): the rotation ticker shares r.clock with the
		// drain backstop and can consume the single fake tick first, wedging the
		// drain select forever (guard-caught hang, FTR05-P4 follow-up).
		cfg.HMACKeyRotateInterval = 0
		svc, _ := NewRelayService(cfg, transport, nil)
		svc.clock = fakeClock{ch: make(chan time.Time, 1)}
		svc.clock.(fakeClock).ch <- time.Now()
		_ = svc.Start(context.Background())
		if err := svc.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !transport.closed || svc.Health().Status != StatusDisabled {
			t.Fatalf("transport = %+v, health = %+v", transport, svc.Health())
		}
	})
}
