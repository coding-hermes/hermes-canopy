package federation

import (
	"testing"
	"time"
)

func TestPeerRateLimiterIsolatedAndResets(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := NewPeerRateLimiter(2, time.Minute)
	limiter.now = func() time.Time { return now }
	if !limiter.Allow("peer-a") || !limiter.Allow("peer-a") || limiter.Allow("peer-a") {
		t.Fatal("peer-a did not enforce its two-request window")
	}
	if !limiter.Allow("peer-b") {
		t.Fatal("peer-b was affected by peer-a's quota")
	}
	now = now.Add(time.Minute)
	if !limiter.Allow("peer-a") {
		t.Fatal("peer-a quota did not reset")
	}
}
