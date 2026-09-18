// Unit tests for the per-user merge rate limit — SPEC-API-04 §13 (10
// req/min/user) and §14.3 (429 + Retry-After + RATE_LIMITED).
//
// The limiter's clock is injected, so the refill path is proven without
// sleeping a minute: every case below moves a fake clock instead of waiting.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fake clock -------------------------------------------------------------

// mergeRateClock is the injectable clock the limiter cases drive. Only Now is
// read by the limiter; advance moves the window.
type mergeRateClock struct {
	now time.Time
}

func (c *mergeRateClock) Now() time.Time { return c.now }

func (c *mergeRateClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newMergeRateClock() *mergeRateClock {
	return &mergeRateClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
}

// newTestUserRateLimiter returns a limiter over a fake clock.
func newTestUserRateLimiter(perMinute int) (*UserRateLimiter, *mergeRateClock) {
	clock := newMergeRateClock()
	limiter := NewUserRateLimiter(perMinute)
	limiter.now = clock.Now
	return limiter, clock
}

// mergeLimitedRequest builds a merge POST carrying the authenticated identity
// the way AuthMiddleware installs it (userIDContextKey).
func mergeLimitedRequest(userID uuid.UUID) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/trees/"+uuid.New().String()+"/merge", nil)
	return req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, userID))
}

// --- Allow: budget ----------------------------------------------------------

// TestUserRateLimiter_AllowsTenPerWindowThenDenies pins the §13 budget: the
// first 10 requests of a user are allowed, the 11th is denied with a wait of at
// least one second.
func TestUserRateLimiter_AllowsTenPerWindowThenDenies(t *testing.T) {
	require.Equal(t, 10, MergeRateLimitPerMinute, "§13 states 10 req/min/user — the wiring constant must match")
	limiter, _ := newTestUserRateLimiter(MergeRateLimitPerMinute)
	user := uuid.New()

	for i := 1; i <= MergeRateLimitPerMinute; i++ {
		ok, retry := limiter.Allow(user)
		require.True(t, ok, "request %d of the budget must be allowed", i)
		require.Zero(t, retry, "an allowed request reports no wait")
	}

	ok, retry := limiter.Allow(user)
	require.False(t, ok, "the 11th request inside the window must be denied")
	require.GreaterOrEqual(t, retry, 1, "§14.3: the client must be told to wait at least one second")
	require.LessOrEqual(t, retry, 60, "the wait can never exceed the window")
}

// TestUserRateLimiter_PerUserIsolation proves the budget is keyed per user: a
// user whose window is exhausted does not affect anybody else.
func TestUserRateLimiter_PerUserIsolation(t *testing.T) {
	limiter, _ := newTestUserRateLimiter(MergeRateLimitPerMinute)
	limited := uuid.New()
	untouched := uuid.New()

	for i := 0; i < MergeRateLimitPerMinute; i++ {
		ok, _ := limiter.Allow(limited)
		require.True(t, ok, "exhausting the budget of the first user")
	}
	ok, _ := limiter.Allow(limited)
	require.False(t, ok, "the first user is over budget")

	for i := 1; i <= MergeRateLimitPerMinute; i++ {
		ok, retry := limiter.Allow(untouched)
		require.True(t, ok, "the second user's request %d must be allowed", i)
		require.Zero(t, retry)
	}
	ok, _ = limiter.Allow(untouched)
	require.False(t, ok, "the second user has its OWN budget, not a shared one")

	// The first user is still limited — the second user's traffic did not
	// refill anything.
	ok, _ = limiter.Allow(limited)
	require.False(t, ok, "the first user's window must be unchanged by the second user")
}

// TestUserRateLimiter_RefillAfterWindowAndShrinkingRetryAfter proves the
// rolling window: the wait counts down as the oldest accepted request
// approaches the edge, and the budget refills once it leaves.
func TestUserRateLimiter_RefillAfterWindowAndShrinkingRetryAfter(t *testing.T) {
	limiter, clock := newTestUserRateLimiter(MergeRateLimitPerMinute)
	user := uuid.New()

	for i := 0; i < MergeRateLimitPerMinute; i++ {
		ok, _ := limiter.Allow(user)
		require.True(t, ok, "filling the budget")
	}

	ok, retry := limiter.Allow(user)
	require.False(t, ok)
	require.Equal(t, 60, retry, "a full window of age-zero acceptances means a full-window wait")

	// Denials do not consume (or extend) the budget.
	for i := 0; i < 5; i++ {
		ok, retry = limiter.Allow(user)
		require.False(t, ok, "repeated denials stay denied inside the window")
		require.Equal(t, 60, retry, "a denied request must not push the refill further out")
	}

	clock.advance(30 * time.Second)
	ok, retry = limiter.Allow(user)
	require.False(t, ok, "still inside the window at t+30s")
	require.Equal(t, 30, retry, "the wait counts down with the clock")

	clock.advance(29 * time.Second)
	_, retry = limiter.Allow(user)
	require.Equal(t, 1, retry, "one second left at t+59s")

	clock.advance(time.Second)
	ok, retry = limiter.Allow(user)
	require.True(t, ok, "the oldest acceptance left the window at t+60s — the budget refills")
	require.Zero(t, retry)

	// And the refilled slot is a real one: the budget is 10 again, not 11.
	for i := 2; i <= MergeRateLimitPerMinute; i++ {
		ok, _ := limiter.Allow(user)
		require.True(t, ok, "request %d of the refilled budget", i)
	}
	ok, _ = limiter.Allow(user)
	require.False(t, ok, "the refilled window admits exactly the budget again")
}

// TestUserRateLimiter_PrunesExpiredUsers proves the map cannot grow without
// bound: a user whose whole window has expired is swept out of the limiter.
func TestUserRateLimiter_PrunesExpiredUsers(t *testing.T) {
	limiter, clock := newTestUserRateLimiter(MergeRateLimitPerMinute)

	for i := 0; i < 5; i++ {
		ok, _ := limiter.Allow(uuid.New())
		require.True(t, ok)
	}
	require.Len(t, limiter.users, 5, "each user gets its own window entry")

	clock.advance(2 * time.Minute)
	ok, _ := limiter.Allow(uuid.New())
	require.True(t, ok)
	require.Len(t, limiter.users, 1, "users whose window fully expired must be pruned from the map")
}

// TestUserRateLimiter_ConcurrentAllowsAreBounded is the mutex proof: 50
// concurrent requests for one user admit exactly the budget (run with -race).
func TestUserRateLimiter_ConcurrentAllowsAreBounded(t *testing.T) {
	limiter := NewUserRateLimiter(MergeRateLimitPerMinute)
	user := uuid.New()

	const attempts = 50
	var allowed, denied int64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := limiter.Allow(user); ok {
				atomic.AddInt64(&allowed, 1)
				return
			}
			atomic.AddInt64(&denied, 1)
		}()
	}
	wg.Wait()

	require.Equal(t, int64(MergeRateLimitPerMinute), atomic.LoadInt64(&allowed),
		"exactly the budget is admitted under concurrency")
	require.Equal(t, int64(attempts-MergeRateLimitPerMinute), atomic.LoadInt64(&denied))
}

// TestUserRateLimiter_NonPositiveBudgetDisablesLimit pins the documented
// off-switch so a zero budget can never lock every merge out.
func TestUserRateLimiter_NonPositiveBudgetDisablesLimit(t *testing.T) {
	for _, perMinute := range []int{0, -1} {
		limiter := NewUserRateLimiter(perMinute)
		user := uuid.New()
		for i := 0; i < 3; i++ {
			ok, retry := limiter.Allow(user)
			require.True(t, ok, "perMinute=%d must not limit", perMinute)
			require.Zero(t, retry)
		}
		require.Empty(t, limiter.users, "a disabled limiter keeps no state")
	}
}

// --- MergeRateLimit middleware ---------------------------------------------

// TestMergeRateLimitMiddleware_DeniesWithCatalogIdentity drives the middleware:
// the allowed requests reach the handler, the denied one answers 429 with the
// Retry-After header and the SPEC-API-07 RATE_LIMITED identity (code, message,
// retry_after_seconds) without reaching the handler at all.
func TestMergeRateLimitMiddleware_DeniesWithCatalogIdentity(t *testing.T) {
	limiter, _ := newTestUserRateLimiter(2)
	user := uuid.New()

	var reached int
	mw := MergeRateLimit(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusCreated)
	}))

	for i := 1; i <= 2; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, mergeLimitedRequest(user))
		require.Equal(t, http.StatusCreated, rec.Code, "request %d of the budget reaches the handler", i)
	}
	require.Equal(t, 2, reached)

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, mergeLimitedRequest(user))

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "60", rec.Header().Get("Retry-After"),
		"§14.3 requires the Retry-After header on the 429")
	require.Equal(t, 2, reached, "the denied request must never reach the merge handler")

	var body struct {
		Error struct {
			Code              string `json:"code"`
			Message           string `json:"message"`
			RetryAfterSeconds int    `json:"retry_after_seconds"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body: %s", rec.Body.String())
	assert.Equal(t, "RATE_LIMITED", body.Error.Code, "SPEC-API-07 lists RATE_LIMITED for 429")
	assert.NotEmpty(t, body.Error.Message)
	assert.Equal(t, 60, body.Error.RetryAfterSeconds,
		"the body must carry the same wait as the Retry-After header")
}

// TestMergeRateLimitMiddleware_PassesThroughWithoutIdentity proves the
// uuid.Nil pass-through: an unauthenticated request is not charged to any
// budget (auth/membership own that error, not this middleware).
func TestMergeRateLimitMiddleware_PassesThroughWithoutIdentity(t *testing.T) {
	limiter := NewUserRateLimiter(1)

	var reached int
	mw := MergeRateLimit(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusCreated)
	}))

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/trees/"+uuid.New().String()+"/merge", nil))
		require.Equal(t, http.StatusCreated, rec.Code,
			"a request without an identity must pass through to the auth/membership answer")
	}
	require.Equal(t, 2, reached, "a budget of 1 must not limit identity-less requests")
	require.Empty(t, limiter.users, "an identity-less request must not create window state")
}
