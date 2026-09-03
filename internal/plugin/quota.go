package plugin

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

var ErrQuotaExceeded = errors.New("QUOTA_EXCEEDED")

var methodQuotaLimits = map[string]int{
	"data.query": 100, "data.mutate": 30, "notify": 10,
	"calendar.query": 30, "calendar.create": 10, "network.fetch": 60,
}

type MethodQuotaRegistry struct {
	limiters map[string]*InstanceRateLimiter
}

func NewMethodQuotaRegistry(window time.Duration) *MethodQuotaRegistry {
	r := &MethodQuotaRegistry{limiters: make(map[string]*InstanceRateLimiter, len(methodQuotaLimits))}
	for method, limit := range methodQuotaLimits {
		r.limiters[method] = NewInstanceRateLimiter(limit, window)
	}
	return r
}

func (r *MethodQuotaRegistry) Allow(method string, id uuid.UUID) bool {
	limiter, ok := r.limiters[method]
	return !ok || limiter.Allow(id)
}

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
