package federation

import (
	"sync"
	"time"
)

// PeerRateLimiter is a fixed-window limiter for federation ingress. It is
// intentionally process-local: peer identity remains the durable trust key,
// while limits reset harmlessly on restart.
type PeerRateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]rateBucket
	now     func() time.Time
}

type rateBucket struct {
	started time.Time
	count   int
}

func NewPeerRateLimiter(limit int, window time.Duration) *PeerRateLimiter {
	return &PeerRateLimiter{limit: limit, window: window, buckets: make(map[string]rateBucket), now: time.Now}
}

func (l *PeerRateLimiter) Allow(key string) bool {
	if l == nil || l.limit <= 0 || l.window <= 0 {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket := l.buckets[key]
	if bucket.started.IsZero() || now.Sub(bucket.started) >= l.window {
		bucket = rateBucket{started: now}
	}
	if bucket.count >= l.limit {
		return false
	}
	bucket.count++
	l.buckets[key] = bucket
	return true
}
