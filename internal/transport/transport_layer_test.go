package transport

import (
	"testing"
	"time"
)

func TestMessageQueueFIFOAndOverflow(t *testing.T) {
	queue := NewMessageQueue("peer", 2)
	first := &Message{Sequence: 1}
	second := &Message{Sequence: 2}
	third := &Message{Sequence: 3}

	if err := queue.Enqueue(first); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := queue.Enqueue(second); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	if err := queue.Enqueue(third); err != ErrNoTransportAvailable {
		t.Fatalf("enqueue overflow error = %v, want %v", err, ErrNoTransportAvailable)
	}
	if got := queue.Len(); got != 2 {
		t.Fatalf("queue length = %d, want 2", got)
	}
	if got := queue.Dequeue(); got != second {
		t.Fatalf("first dequeue = %#v, want second message", got)
	}
	if got := queue.Dequeue(); got != third {
		t.Fatalf("second dequeue = %#v, want third message", got)
	}
	if got := queue.Dequeue(); got != nil {
		t.Fatalf("empty dequeue = %#v, want nil", got)
	}
}

func TestRateLimiterAllowN(t *testing.T) {
	limiter := NewRateLimiter(0, 2)
	if !limiter.AllowN(2) {
		t.Fatal("initial burst should be available")
	}
	if limiter.Allow() {
		t.Fatal("empty bucket should reject")
	}
	if limiter.AllowN(0) == false {
		t.Fatal("zero-message request should be allowed")
	}
}

func TestBandwidthProfileAndTier(t *testing.T) {
	profile := &BandwidthProfile{}
	profile.Record(1_000_001, 2500*time.Microsecond)
	bps, latency, loss := profile.Snapshot()
	if bps != 1_000_001 || latency != 2 || loss != 0 {
		t.Fatalf("snapshot = (%d, %d, %v), want (1000001, 2, 0)", bps, latency, loss)
	}
	if got := profile.Tier(); got != "full" {
		t.Fatalf("tier = %q, want full", got)
	}
}

func TestTransportSelector(t *testing.T) {
	selector := NewTransportSelector(ModeP2P, TopologyNAT)
	if got := selector.SelectPrimary("peer"); got != TransportSSE {
		t.Fatalf("primary = %q, want %q for MVP", got, TransportSSE)
	}
	fallback, err := selector.SelectFallback(TransportWebRTC)
	if err != nil || fallback != TransportSSE {
		t.Fatalf("fallback = (%q, %v), want (%q, nil)", fallback, err, TransportSSE)
	}
	if _, err := selector.SelectFallback(TransportRelay); err != ErrNoTransportAvailable {
		t.Fatalf("terminal fallback error = %v, want %v", err, ErrNoTransportAvailable)
	}
	if got := selector.DetectTopology(); got != TopologyLoopback {
		t.Fatalf("detected topology = %v, want loopback", got)
	}
}
