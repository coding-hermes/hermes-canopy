package relay

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTenantRateLimiterTierBoundariesAndExpiry(t *testing.T) {
	now := time.Unix(100, 0)
	for tier, limit := range tenantTierLimits {
		l := NewTenantRateLimiter(time.Minute)
		l.now = func() time.Time { return now }
		id := uuid.New()
		for i := 0; i < limit; i++ {
			if !l.Allow(id, tier) {
				t.Fatalf("%s denied at %d", tier, i)
			}
		}
		if l.Allow(id, tier) {
			t.Fatalf("%s allowed over boundary", tier)
		}
		now = now.Add(time.Minute)
		if !l.Allow(id, tier) {
			t.Fatalf("%s did not expire", tier)
		}
	}
}

func TestTenantRateLimiterConcurrent(t *testing.T) {
	l := NewTenantRateLimiter(time.Minute)
	id := uuid.New()
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 250; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow(id, "free") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 100 {
		t.Fatalf("allowed=%d want 100", got)
	}
	if !l.Allow(uuid.New(), "free") {
		t.Fatal("one tenant exhausted another tenant bucket")
	}
}
