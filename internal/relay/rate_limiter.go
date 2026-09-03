package relay

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

var tenantTierLimits = map[string]int{"free": 100, "pro": 1000, "enterprise": 10000}

// TenantRateLimiter mirrors plugin.InstanceRateLimiter: a single-node sliding
// window keyed by tenant, with expired entries removed on access.
type TenantRateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	calls  map[uuid.UUID][]time.Time
}

func NewTenantRateLimiter(window time.Duration) *TenantRateLimiter {
	return &TenantRateLimiter{window: window, now: time.Now, calls: make(map[uuid.UUID][]time.Time)}
}

func (l *TenantRateLimiter) Allow(id uuid.UUID, tier string) bool {
	limit, ok := tenantTierLimits[tier]
	if id == uuid.Nil || !ok {
		return id == uuid.Nil
	}
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
	if len(entries) >= limit {
		l.calls[id] = entries
		return false
	}
	l.calls[id] = append(entries, now)
	return true
}
