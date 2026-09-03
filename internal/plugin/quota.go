package plugin

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

var ErrQuotaExceeded = errors.New("QUOTA_EXCEEDED")

// InstanceRateLimiter is a single-node sliding-window limiter keyed by plugin instance.
type InstanceRateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	calls  map[uuid.UUID][]time.Time
}

func NewInstanceRateLimiter(limit int, window time.Duration) *InstanceRateLimiter {
	return &InstanceRateLimiter{limit: limit, window: window, now: time.Now, calls: make(map[uuid.UUID][]time.Time)}
}

func (l *InstanceRateLimiter) Allow(id uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-l.window)
	entries := l.calls[id]
	first := 0
	for first < len(entries) && !entries[first].After(cutoff) {
		first++
	}
	entries = entries[first:]
	if len(entries) >= l.limit {
		l.calls[id] = entries
		return false
	}
	l.calls[id] = append(entries, now)
	return true
}
