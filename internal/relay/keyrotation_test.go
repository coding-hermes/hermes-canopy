package relay

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type rotationClock struct {
	durations chan time.Duration
	ticks     chan time.Time
}

func (c *rotationClock) After(d time.Duration) <-chan time.Time {
	c.durations <- d
	return c.ticks
}

// countingClock records every After call so a test can prove WHICH consumer
// subscribed to the clock, and hands each caller a channel chosen by the test.
type countingClock struct {
	mu    sync.Mutex
	calls []time.Duration
	next  <-chan time.Time
}

func (c *countingClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, d)
	return c.next
}

func (c *countingClock) armNext(ch <-chan time.Time) {
	c.mu.Lock()
	c.next = ch
	c.mu.Unlock()
}

func (c *countingClock) afterCalls() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.calls...)
}

func TestKeyRotation(t *testing.T) {
	t.Run("persists increment and grace expiry", func(t *testing.T) {
		now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
		oldKey := []byte("01234567890123456789012345678901")
		cfg := DefaultConfig()
		cfg.Mode, cfg.Enabled, cfg.HMACKeyID, cfg.HMACKey = ModeSelfHosted, true, 7, oldKey
		hub := NewRelayHub(cfg)
		svc, err := NewRelayService(cfg, hub, nil)
		if err != nil {
			t.Fatal(err)
		}
		svc.now = func() time.Time { return now }
		hub.auth.now = func() time.Time { return now }
		var persistedAt time.Time
		var persistedID uint32
		svc.SetRotationPersister(func(_ context.Context, at time.Time, id uint32) error {
			persistedAt, persistedID = at, id
			return nil
		})

		oldFrame, err := NewFrameAuthenticator(7, oldKey, 0, nil).Sign(Frame{Type: FramePing, KeyID: 7})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.RotateNow(context.Background()); err != nil {
			t.Fatal(err)
		}
		if persistedID != 8 || !persistedAt.Equal(now) {
			t.Fatalf("persisted (%d, %v)", persistedID, persistedAt)
		}
		if got := svc.Health(); got.HMACKeyID != 8 || got.Rotations != 1 {
			t.Fatalf("health = %+v", got)
		}
		if err := hub.auth.Verify(oldFrame); err != nil {
			t.Fatalf("old key rejected in grace: %v", err)
		}
		now = now.Add(hmacKeyGracePeriod)
		if err := hub.auth.Verify(oldFrame); !errors.Is(err, ErrAuthFailed) {
			t.Fatalf("expired key error = %v", err)
		}
		unknown := oldFrame
		unknown.KeyID = 99
		if err := hub.auth.Verify(unknown); !errors.Is(err, ErrAuthFailed) {
			t.Fatalf("unknown key error = %v", err)
		}
	})

	t.Run("configured interval", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Mode, cfg.Enabled, cfg.HMACKeyRotateInterval = ModeSelfHosted, true, 3*time.Hour
		clock := &rotationClock{durations: make(chan time.Duration, 2), ticks: make(chan time.Time, 1)}
		svc, _ := NewRelayService(cfg, &fakeTransport{}, nil)
		svc.clock = clock
		if err := svc.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := <-clock.durations; got != 3*time.Hour {
			t.Fatalf("interval = %v", got)
		}
		clock.ticks <- time.Now()
		deadline := time.After(time.Second)
		for svc.Health().Rotations != 1 {
			select {
			case <-deadline:
				t.Fatal("rotation did not run")
			default:
			}
		}
		if err := svc.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("concurrent verification", func(t *testing.T) {
		key := []byte("01234567890123456789012345678901")
		cfg := DefaultConfig()
		cfg.HMACKeyID, cfg.HMACKey = 1, key
		hub := NewRelayHub(cfg)
		svc, _ := NewRelayService(cfg, hub, nil)
		frame, _ := NewFrameAuthenticator(1, key, 0, nil).Sign(Frame{Type: FramePing, KeyID: 1})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				_ = hub.auth.Verify(frame)
			}
		}()
		if err := svc.RotateNow(context.Background()); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
	})

	t.Run("disabled interval never consumes drain clock", func(t *testing.T) {
		// Regression for 4862be6 (FTR05-P4): with HMACKeyRotateInterval <= 0,
		// Start must NOT arm the rotation loop — before the fix it defaulted to
		// 168h and its goroutine shared r.clock with Shutdown's drain backstop,
		// so a single preloaded fake tick could be eaten by rotation and park
		// the drain select forever. Prove the invariant directly: zero clock
		// subscriptions from Start, and Shutdown's drain backstop is the ONLY
		// After caller, consuming exactly the one buffered drain tick.
		transport := &fakeTransport{sessions: 1}
		cfg := DefaultConfig()
		cfg.Mode, cfg.Enabled, cfg.HMACKeyRotateInterval = ModeSelfHosted, true, 0
		drainTick := make(chan time.Time, 1)
		drainTick <- time.Now()
		clock := &countingClock{}
		svc, err := NewRelayService(cfg, transport, nil)
		if err != nil {
			t.Fatal(err)
		}
		svc.clock = clock
		if err := svc.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if svc.rotationStop != nil || svc.rotationDone != nil {
			t.Fatalf("rotation loop armed with disabled interval: stop=%v done=%v", svc.rotationStop, svc.rotationDone)
		}
		if calls := clock.afterCalls(); len(calls) != 0 {
			t.Fatalf("Start subscribed to clock with disabled interval: %v", calls)
		}
		if got := svc.Health(); got.Status != StatusRunning {
			t.Fatalf("health = %+v, want running", got)
		}

		// Automatic rotation is off, but manual RotateNow must still work.
		if err := svc.RotateNow(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := svc.Health(); got.HMACKeyID != 1 || got.Rotations != 1 {
			t.Fatalf("manual rotation health = %+v", got)
		}
		if calls := clock.afterCalls(); len(calls) != 0 {
			t.Fatalf("RotateNow subscribed to clock: %v", calls)
		}

		// Drain path is the sole clock consumer: arm it just before Shutdown so
		// even a hypothetical subscriber could not win the single tick.
		clock.armNext(drainTick)
		if err := svc.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		want := []time.Duration{time.Duration(cfg.DrainTimeoutSecs) * time.Second}
		if calls := clock.afterCalls(); len(calls) != 1 || calls[0] != want[0] {
			t.Fatalf("clock calls = %v, want exactly %v (drain backstop)", calls, want)
		}
		if !transport.stopped || !transport.notified || !transport.closed {
			t.Fatalf("transport lifecycle = %+v", transport)
		}
		if got := svc.Health(); got.Status != StatusDisabled || got.Sessions != 0 {
			t.Fatalf("post-shutdown health = %+v", got)
		}
	})
}
