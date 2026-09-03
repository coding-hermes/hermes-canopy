package plugin

import (
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestInstanceRateLimiterSlidingWindow(t *testing.T) {
	l := NewInstanceRateLimiter(2, time.Minute)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	a, b := uuid.New(), uuid.New()
	if !l.Allow(a) || !l.Allow(a) || l.Allow(a) {
		t.Fatal("instance a quota was not enforced")
	}
	if !l.Allow(b) {
		t.Fatal("quota leaked between instances")
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if !l.Allow(a) {
		t.Fatal("quota did not expire")
	}
}

func TestQuotaMethodsAreIndependentAndExpire(t *testing.T) {
	r := NewMethodQuotaRegistry(time.Minute)
	now := time.Unix(2000, 0)
	for _, limiter := range r.limiters {
		limiter.now = func() time.Time { return now }
	}
	id := uuid.New()
	for i := 0; i < 10; i++ {
		if !r.Allow("notify", id) {
			t.Fatalf("notify call %d denied early", i+1)
		}
	}
	if r.Allow("notify", id) {
		t.Fatal("notify quota was not enforced")
	}
	if !r.Allow("data.mutate", id) {
		t.Fatal("notify quota leaked into data.mutate")
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if !r.Allow("notify", id) {
		t.Fatal("notify quota did not expire")
	}
}
