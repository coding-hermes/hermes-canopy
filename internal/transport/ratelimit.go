package transport

import (
	"sync"
	"time"
)

const RelayRequestsPerMinute = 600

type PeerRelayRateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]relayRateBucket
	now     func() time.Time
}

type relayRateBucket struct {
	started time.Time
	count   int
}

func NewPeerRelayRateLimiter(limit int, window time.Duration) *PeerRelayRateLimiter {
	return &PeerRelayRateLimiter{limit: limit, window: window, buckets: map[string]relayRateBucket{}, now: time.Now}
}

func (l *PeerRelayRateLimiter) Allow(peer string) bool {
	if l == nil || l.limit <= 0 || l.window <= 0 {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[peer]
	if b.started.IsZero() || now.Sub(b.started) >= l.window {
		b = relayRateBucket{started: now}
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	l.buckets[peer] = b
	return true
}
