package transport

import (
	"sync"
	"time"
)

// RateLimiter enforces per-connection message rate limits using a token bucket.
type RateLimiter struct {
	rate     float64
	burst    int
	tokens   float64
	lastTime time.Time
	mu       sync.Mutex
}

// NewRateLimiter creates a token-bucket rate limiter. The bucket starts full so
// a newly established connection can use its configured burst immediately.
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	if rate < 0 {
		rate = 0
	}
	if burst < 0 {
		burst = 0
	}
	return &RateLimiter{
		rate:     rate,
		burst:    burst,
		tokens:   float64(burst),
		lastTime: time.Now(),
	}
}

// Allow checks if one message is allowed and deducts one token on success.
func (rl *RateLimiter) Allow() bool {
	return rl.AllowN(1)
}

// AllowN checks if n messages are allowed and deducts n tokens on success.
func (rl *RateLimiter) AllowN(n int) bool {
	if n <= 0 {
		return true
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.refill(time.Now())
	requested := float64(n)
	if requested > rl.tokens {
		return false
	}
	rl.tokens -= requested
	return true
}

// Tokens returns the current token count for diagnostics.
func (rl *RateLimiter) Tokens() float64 {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.tokens
}

func (rl *RateLimiter) refill(now time.Time) {
	if rl.lastTime.IsZero() {
		rl.lastTime = now
		return
	}
	elapsed := now.Sub(rl.lastTime).Seconds()
	if elapsed > 0 {
		rl.tokens += rl.rate * elapsed
		if rl.tokens > float64(rl.burst) {
			rl.tokens = float64(rl.burst)
		}
	}
	rl.lastTime = now
}
