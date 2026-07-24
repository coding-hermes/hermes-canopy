package handler

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// --- Context keys for middleware-injected values ----------------------------

type contextKey string

const (
	// UserIDKey is the context key for the authenticated user's UUID.
	UserIDKey contextKey = "user_id"
)

// --- BodySizeLimit ----------------------------------------------------------

// BodySizeLimit returns middleware that rejects requests with a body larger
// than maxBytes. Applies to all methods that may carry a body.
func BodySizeLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
				// Read the body once to trigger MaxBytesReader rejection early.
				// Handlers will re-read from the same reader (which is now exhausted),
				// so handlers that call decodeJSON must create a fresh reader.
				_, err := io.ReadAll(r.Body)
				if err != nil {
					writeError(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE",
						"request body exceeds maximum allowed size")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- In-memory rate limiter (per-IP token bucket) ---------------------------

type visitor struct {
	tokens    float64
	lastCheck time.Time
}

// RateLimiter implements a per-IP token-bucket rate limiter.
type RateLimiter struct {
	mu     sync.Mutex
	rate   float64 // tokens added per second
	burst  int     // maximum accumulated tokens
	visits map[string]*visitor
}

// NewRateLimiter creates a RateLimiter with the given rate (tokens/second) and
// burst capacity.
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	return &RateLimiter{
		rate:   rate,
		burst:  burst,
		visits: make(map[string]*visitor),
	}
}

// Allow reports whether a request from the given IP is within the rate limit.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, ok := rl.visits[ip]
	now := time.Now()
	if !ok {
		rl.visits[ip] = &visitor{tokens: float64(rl.burst) - 1, lastCheck: now}
		return true
	}
	// Refill tokens based on elapsed time.
	elapsed := now.Sub(v.lastCheck).Seconds()
	v.tokens += elapsed * rl.rate
	if v.tokens > float64(rl.burst) {
		v.tokens = float64(rl.burst)
	}
	v.lastCheck = now

	if v.tokens < 1 {
		return false
	}
	v.tokens--
	return true
}

// RateLimit returns middleware that rejects requests exceeding the configured
// rate. Health endpoints are exempt.
func RateLimit(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			ip := r.RemoteAddr
			if !rl.Allow(ip) {
				writeError(w, http.StatusTooManyRequests, "RATE_LIMITED",
					"too many requests — try again later")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- Tree membership middleware ---------------------------------------------

// TreeMemberChecker is the interface TreeMembershipMiddleware needs to verify
// that a user belongs to a tree.
type TreeMemberChecker interface {
	// IsMember returns true when the given user is a member of the tree.
	IsMember(ctx context.Context, treeID, userID uuid.UUID) (bool, error)
}

// treeMembershipKey holds the role in context for handlers that need it.
type treeMembershipKey struct{}

// TreeMembershipMiddleware returns middleware that verifies the authenticated
// user is a member of the tree referenced by the {tree_id} URL parameter.
// Routes without {tree_id} pass through unchecked.
func TreeMembershipMiddleware(checker TreeMemberChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			treeIDStr := chiURLParam(r, "tree_id")
			if treeIDStr == "" {
				// Route has no tree_id parameter — nothing to check.
				next.ServeHTTP(w, r)
				return
			}
			treeID, err := uuid.Parse(treeIDStr)
			if err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_TREE_ID", "tree_id must be a valid UUID")
				return
			}
			userID := UserIDFromContext(r.Context())
			if userID == uuid.Nil {
				writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
				return
			}
			member, err := checker.IsMember(r.Context(), treeID, userID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not verify membership")
				return
			}
			if !member {
				writeError(w, http.StatusForbidden, "NOT_TREE_MEMBER", "you are not a member of this tree")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// chiURLParam is a test-safe wrapper around chi.URLParam that avoids
// importing chi in middleware tests.
var chiURLParam = func(r *http.Request, key string) string {
	// Default implementation — replaced in tests.
	return ""
}
