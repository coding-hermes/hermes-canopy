// Merge rate limit — SPEC-API-04 §13 / §14.3.
//
// §13 gives the merge endpoint its own budget ("Rate limit — merge | 10
// req/min/user | Merges are heavyweight operations") and §14.3 requires the
// resulting 429 to carry a Retry-After header and the RATE_LIMITED code. The
// router's global limiter (middleware.go) is per-IP and covers every route, so
// it can express neither the per-user key nor the per-endpoint budget; this
// file owns the per-user merge limiter and the middleware that mounts it on
// that one route.

package handler

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MergeRateLimitPerMinute is the per-user merge budget stated by
// SPEC-API-04 §13.
const MergeRateLimitPerMinute = 10

// mergeRateWindow is the window the budget is measured over — the spec states
// the limit per minute.
const mergeRateWindow = time.Minute

// UserRateLimiter enforces a per-user request budget over a rolling window.
//
// The window is rolling (the timestamps of accepted requests, pruned as they
// age out) rather than a fixed one-minute bucket: "10 req/min" then holds for
// every 60s interval, so a burst straddling a bucket boundary cannot buy more
// than the budget.
//
// Expired state is pruned on ACCESS. The requesting user's window is pruned on
// every Allow, and a sweep over the whole map runs at most once per window
// (amortized), so a request never pays O(users). Sweep-on-insert alone was
// rejected: a user who stops calling would pin its entry forever, whereas the
// amortized sweep bounds the map to the users active in the last window plus
// those touched since the previous sweep.
type UserRateLimiter struct {
	mu        sync.Mutex
	perMinute int
	window    time.Duration
	// now is the clock seam (unexported, defaults to time.Now) so the refill
	// path is testable without sleeping a minute.
	now       func() time.Time
	users     map[uuid.UUID][]time.Time
	lastSweep time.Time
}

// NewUserRateLimiter returns a limiter allowing perMinute requests per user in
// any 60s window. perMinute <= 0 disables the limit (every request allowed):
// the wiring can then turn the budget off without unwiring the middleware.
func NewUserRateLimiter(perMinute int) *UserRateLimiter {
	return &UserRateLimiter{
		perMinute: perMinute,
		window:    mergeRateWindow,
		now:       time.Now,
		users:     make(map[uuid.UUID][]time.Time),
	}
}

// Allow reports whether userID may proceed now.
//
// retryAfterSeconds is the whole-second wait until the next allowance: it is
// only meaningful when ok is false, where it is always >= 1 (the caller's
// Retry-After header must not be "0"). It counts down as the oldest accepted
// request approaches the edge of the window.
func (l *UserRateLimiter) Allow(userID uuid.UUID) (ok bool, retryAfterSeconds int) {
	if l == nil || l.perMinute <= 0 {
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)

	// Sweep before the requesting user's window is created, so a sweep can
	// never drop the entry this request is about to write.
	l.sweepLocked(cutoff, now)

	accepted := dropExpired(l.users[userID], cutoff)
	if len(accepted) < l.perMinute {
		l.users[userID] = append(accepted, now)
		return true, 0
	}
	// Denied: keep the pruned window (it is the authoritative count) and
	// report the wait until its EARLIEST request leaves the window. The slice
	// is append-ordered, but the minimum is scanned for rather than indexed so
	// a clock that steps backwards cannot report a too-short wait.
	l.users[userID] = accepted
	earliest := accepted[0]
	for _, at := range accepted[1:] {
		if at.Before(earliest) {
			earliest = at
		}
	}
	seconds := int(math.Ceil(earliest.Add(l.window).Sub(now).Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return false, seconds
}

// sweepLocked drops every user whose accepted requests have all aged out. It
// runs at most once per window, so its cost is bounded and amortized; an idle
// user's entry can outlive its window by at most one sweep interval.
func (l *UserRateLimiter) sweepLocked(cutoff, now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	for id, accepted := range l.users {
		accepted = dropExpired(accepted, cutoff)
		if len(accepted) == 0 {
			delete(l.users, id)
			continue
		}
		l.users[id] = accepted
	}
}

// dropExpired returns the timestamps still inside the window, reusing the
// backing array — every caller stores the result back into the map.
func dropExpired(accepted []time.Time, cutoff time.Time) []time.Time {
	kept := accepted[:0]
	for _, at := range accepted {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	return kept
}

// MergeRateLimit returns middleware enforcing the per-user merge budget
// (SPEC-API-04 §13). It is mounted on the merge route only, so it must not
// exempt anything itself.
//
// A request with no authenticated identity (uuid.Nil) passes through: in
// production AuthMiddleware and TreeMembershipMiddleware answer 401/403 before
// this middleware runs, and inventing a second auth error here would only
// shadow them.
func MergeRateLimit(limiter *UserRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := UserIDFromContext(r.Context())
			if userID == uuid.Nil {
				next.ServeHTTP(w, r)
				return
			}
			ok, retryAfterSeconds := limiter.Allow(userID)
			if ok {
				next.ServeHTTP(w, r)
				return
			}
			// §14.3: the 429 carries the Retry-After header and the
			// RATE_LIMITED code; SPEC-API-07 gives that code's identity a
			// retry_after_seconds field.
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
			writeRateLimited(w, retryAfterSeconds)
		})
	}
}

// writeRateLimited writes the §14.3 429 body: the shared {"error":{…}}
// envelope (handler_util.go) plus the retry_after_seconds field of the
// RATE_LIMITED identity.
func writeRateLimited(w http.ResponseWriter, retryAfterSeconds int) {
	writeJSON(w, http.StatusTooManyRequests, apiErrorBody{Error: apiError{
		Code:              "RATE_LIMITED",
		Message:           "too many requests — try again later",
		RetryAfterSeconds: retryAfterSeconds,
	}})
}
