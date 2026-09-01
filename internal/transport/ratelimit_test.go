package transport

import (
	"testing"
	"time"
)

func TestPeerRelayRateLimiterBoundary(t *testing.T) {
	now := time.Unix(100, 0)
	l := NewPeerRelayRateLimiter(RelayRequestsPerMinute, time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < RelayRequestsPerMinute; i++ {
		if !l.Allow("peer-a") {
			t.Fatalf("request %d rejected", i+1)
		}
	}
	if l.Allow("peer-a") {
		t.Fatal("601st request allowed")
	}
	if !l.Allow("peer-b") {
		t.Fatal("separate peer was limited")
	}
	now = now.Add(time.Minute)
	if !l.Allow("peer-a") {
		t.Fatal("window did not reset")
	}
}
