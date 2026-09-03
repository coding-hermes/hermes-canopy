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
