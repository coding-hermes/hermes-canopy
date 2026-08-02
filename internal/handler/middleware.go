package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
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
				// Read the body once to trigger MaxBytesReader rejection early,
				// then replace it so downstream handlers can read it.
				bodyBytes, err := io.ReadAll(r.Body)
				if err != nil {
					writeError(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE",
						"request body exceeds maximum allowed size")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
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

// treeIDFromPath extracts the tree ID from the URL path. In chi, middleware
// runs before route matching, so chi.URLParam is not available for params
// defined on mounted subrouters. Instead, we parse the path directly.
//
// Path patterns:
//
//	/api/v1/nodes/{tree_id}/nodes/{node_id}   → tree_id at segment 3
//	/api/v1/trees/{id}/...                     → tree_id at segment 3
//
// Routes without a tree ID segment (e.g., /api/v1/trees for listing) return empty.
func treeIDFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// Expected: ["api", "v1", "nodes|trees", "{id}", ...]
	if len(parts) < 4 {
		return ""
	}
	if parts[0] != "api" || parts[1] != "v1" {
		return ""
	}
	if parts[2] != "nodes" && parts[2] != "trees" {
		return ""
	}
	return parts[3]
}

// TreeMembershipMiddleware returns middleware that verifies the authenticated
// user is a member of the tree referenced by the tree_id URL segment.
// Routes without a tree_id segment pass through unchecked.
func TreeMembershipMiddleware(checker TreeMemberChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			treeIDStr := treeIDFromPath(r.URL.Path)
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

// NodeAccessMiddleware enforces tree membership on the FLAT /nodes mount
// (BUG-025). The tree-scoped mount (/trees/{tree_id}/nodes) is already
// protected by TreeMembershipMiddleware; the flat mount exposes
// /{tree_id}/nodes[/{node_id}] (tree-scoped flat forms) and bare
// /{node_id} forms (update/delete/reply/fork) which previously allowed any
// authenticated user to read or mutate ANY node by UUID — BUG-016 only
// covered the bare-path GET case.
//
// Resolution: paths shaped /api/v1/nodes/{tree_id}/nodes[...] use the
// tree_id segment directly; bare /api/v1/nodes/{node_id} routes load the
// node to determine its tree. Non-members get 403 NOT_TREE_MEMBER.
func NodeAccessMiddleware(svc service.NodeService, checker TreeMemberChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := UserIDFromContext(r.Context())
			if userID == uuid.Nil {
				writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
				return
			}

			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) < 4 {
				writeError(w, http.StatusBadRequest, "INVALID_PATH", "malformed node path")
				return
			}

			var treeID uuid.UUID
			if len(parts) >= 5 && parts[2] == "nodes" && parts[4] == "nodes" {
				// Flat tree-scoped form: /api/v1/nodes/{tree_id}/nodes[/{node_id}]
				tid, err := uuid.Parse(parts[3])
				if err != nil {
					writeError(w, http.StatusBadRequest, "INVALID_TREE_ID", "tree_id must be a valid UUID")
					return
				}
				treeID = tid
				// Validate node_id when present (parts[5]) BEFORE the membership
				// check — a malformed node_id must yield 400 INVALID_NODE_ID,
				// not 403 NOT_TREE_MEMBER (API contract, TestBE12_ValidationErrors).
				if len(parts) >= 6 {
					if _, err := uuid.Parse(parts[5]); err != nil {
						writeError(w, http.StatusBadRequest, "INVALID_NODE_ID", "node_id must be a valid UUID")
						return
					}
				}
			} else {
				// Bare node form: /api/v1/nodes/nodes/{node_id}[/reply|/fork]
				// (literal "nodes" segment at parts[3], node_id at parts[4]) or
				// legacy /api/v1/nodes/{node_id} (node_id at parts[3]) — resolve
				// the node to learn its tree.
				nodeIDStr := parts[3]
				if nodeIDStr == "nodes" && len(parts) >= 5 {
					nodeIDStr = parts[4]
				}
				nodeID, err := uuid.Parse(nodeIDStr)
				if err != nil {
					writeError(w, http.StatusBadRequest, "INVALID_NODE_ID", "node_id must be a valid UUID")
					return
				}
				node, err := svc.GetByID(r.Context(), nodeID)
				if err != nil {
					if errors.Is(err, service.ErrNodeNotFound) {
						writeError(w, http.StatusNotFound, "NODE_NOT_FOUND", "node not found")
						return
					}
					writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not resolve node")
					return
				}
				treeID = node.TreeID
			}

			if treeID == uuid.Nil {
				writeError(w, http.StatusBadRequest, "INVALID_TREE_ID", "could not determine tree")
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
//
//nolint:unused // replaced in middleware_test.go; test-only override target
var chiURLParam = func(r *http.Request, key string) string {
	// Default implementation — replaced in tests.
	return ""
}
