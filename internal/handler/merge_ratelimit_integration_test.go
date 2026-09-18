// Per-user merge rate limit through the real route stack — SPEC-API-04 §13
// (10 req/min/user) and §14.3 (429 + Retry-After + RATE_LIMITED) — against a
// real PostgreSQL.
//
// The chain under test is assembled exactly as production mounts it
// (internal/server/server.go): /api/v1 group → AuthMiddleware →
// TreeMembershipMiddleware → MergeRateLimit → CreateMerge. See
// newMergeHarnessWithLimiter in merge_integration_test.go.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// rateLimitErrorEnvelope is the §14.3 429 body: the shared {"error":{…}}
// envelope plus the retry_after_seconds field of the RATE_LIMITED identity
// (SPEC-API-07).
type rateLimitErrorEnvelope struct {
	Error struct {
		Code              string `json:"code"`
		Message           string `json:"message"`
		RetryAfterSeconds int    `json:"retry_after_seconds"`
	} `json:"error"`
}

// secondMember adds another 'member' of the tree (its own users row) and
// returns its id — the isolation arm needs a second identity inside the SAME
// tree and the same window.
func (h *mergeHarness) secondMember(t *testing.T, treeID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	userID := uuid.New()
	_, err := h.pool.Exec(ctx,
		`INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1, $2, 'Second Member')`,
		userID, "merge-limit-member-"+userID.String())
	require.NoError(t, err, "insert second member")
	_, err = h.pool.Exec(ctx,
		`INSERT INTO tree_members (tree_id, user_id, role) VALUES ($1, $2, 'member')`, treeID, userID)
	require.NoError(t, err, "add second member to tree")
	return userID
}

// postMergeRaw issues a merge POST as userID and returns the RESPONSE so the
// headers are visible — the shared do() helper drops them, and Retry-After is
// half of the §14.3 contract.
func (h *mergeHarness) postMergeRaw(t *testing.T, userID, treeID uuid.UUID, body any) (*http.Response, []byte) {
	t.Helper()
	resp, err := h.srv.Client().Do(h.mergeRequest(t, userID, http.MethodPost, h.mergePath(treeID), body))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, raw
}

// synthesisNodes counts the tree's synthesis nodes — the non-vacuity probe: the
// allowed requests must be REAL merges, and the denied one must write nothing.
func (h *mergeHarness) synthesisNodes(t *testing.T, treeID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM nodes WHERE tree_id = $1 AND node_type = 'synthesis'`, treeID).Scan(&n))
	return n
}

// TestMergeIntegration_PerUserRateLimit is the end-to-end acceptance of §13:
// the 11th merge POST by one authenticated user inside the window answers 429
// with RATE_LIMITED + Retry-After (+ retry_after_seconds), a second user in the
// same window is unaffected, and other routes keep serving the limited user.
func TestMergeIntegration_PerUserRateLimit(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	limiter := NewUserRateLimiter(MergeRateLimitPerMinute)
	h := newMergeHarnessWithLimiter(t, pool, limiter)

	tree := h.newTree(t)
	sourceA := h.node(t, tree.ID, &tree.RootID)
	sourceB := h.node(t, tree.ID, &tree.RootID)
	body := mergeBody([]uuid.UUID{sourceA, sourceB}, nil)

	second := h.secondMember(t, tree.ID)

	// §13's budget, through the real chain: every request inside the window is
	// a real 201 merge.
	for i := 1; i <= MergeRateLimitPerMinute; i++ {
		status, raw := h.postMerge(t, tree.ID, body)
		require.Equal(t, http.StatusCreated, status,
			"merge %d of the budget must succeed: %s", i, string(raw))
	}
	require.Equal(t, MergeRateLimitPerMinute, h.synthesisNodes(t, tree.ID),
		"the allowed requests were real merges, not silently dropped ones")

	// The 11th request of the same user, same window.
	resp, raw := h.postMergeRaw(t, h.userID, tree.ID, body)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "body: %s", string(raw))

	retryAfter := resp.Header.Get("Retry-After")
	require.NotEmpty(t, retryAfter, "§14.3: the 429 must carry a Retry-After header")
	seconds, err := strconv.Atoi(retryAfter)
	require.NoError(t, err, "Retry-After must be a whole number of seconds, got %q", retryAfter)
	assert.GreaterOrEqual(t, seconds, 1, "the wait must be at least one second")

	var env rateLimitErrorEnvelope
	require.NoError(t, json.Unmarshal(raw, &env), "decode 429 body: %s", string(raw))
	t.Logf("observed 429: Retry-After=%q body=%s", retryAfter, string(raw))
	assert.Equal(t, "RATE_LIMITED", env.Error.Code, "SPEC-API-07 lists RATE_LIMITED for 429")
	assert.NotEmpty(t, env.Error.Message)
	assert.Equal(t, seconds, env.Error.RetryAfterSeconds,
		"the body's retry_after_seconds must agree with the Retry-After header")

	require.Equal(t, MergeRateLimitPerMinute, h.synthesisNodes(t, tree.ID),
		"the rate-limited request must not have written a node")

	// A second user inside the SAME window is unaffected (§13 is per user).
	status, otherRaw := h.do(t, h.mergeRequest(t, second, http.MethodPost, h.mergePath(tree.ID), body))
	require.Equal(t, http.StatusCreated, status, "the second user must be unaffected: %s", string(otherRaw))
	require.Equal(t, MergeRateLimitPerMinute+1, h.synthesisNodes(t, tree.ID))

	// Other routes are unaffected for the limited user.
	status, listRaw := h.do(t, h.mergeRequest(t, h.userID, http.MethodGet, h.nodesPath(tree.ID), nil))
	require.Equal(t, http.StatusOK, status,
		"the merge budget must not touch other routes on the tree: %s", string(listRaw))
}

// TestMergeIntegration_PerUserRateLimitLeavesAuthAndMembershipOwn pins the
// position of the limiter inside the chain: an UNAUTHENTICATED merge POST still
// gets the auth answer (401) and a non-member still gets 403 — the budget is
// keyed on the authenticated identity and never shadows auth/membership.
func TestMergeIntegration_PerUserRateLimitLeavesAuthAndMembershipOwn(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	h := newMergeHarnessWithLimiter(t, pool, NewUserRateLimiter(1))

	tree := h.newTree(t)
	sourceA := h.node(t, tree.ID, &tree.RootID)
	sourceB := h.node(t, tree.ID, &tree.RootID)
	body := mergeBody([]uuid.UUID{sourceA, sourceB}, nil)

	// A non-member is rejected by membership (403) even when the caller's own
	// budget is exhausted — the gate order is auth → membership → limiter.
	nonMember := uuid.New()
	status, raw := h.postMerge(t, tree.ID, body)
	require.Equal(t, http.StatusCreated, status, "filling the single-slot budget: %s", string(raw))
	status, raw = h.postMerge(t, tree.ID, body)
	require.Equal(t, http.StatusTooManyRequests, status, "the owner's budget is exhausted: %s", string(raw))

	status, raw = h.do(t, h.mergeRequest(t, nonMember, http.MethodPost, h.mergePath(tree.ID), body))
	require.Equal(t, http.StatusForbidden, status,
		"a non-member is answered by membership (403), not by the limiter: %s", string(raw))
	require.Equal(t, "NOT_TREE_MEMBER", mergeErrorCode(t, raw))

	// No token at all: the auth middleware answers 401 before the limiter.
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+h.mergePath(tree.ID), nil)
	require.NoError(t, err)
	status, raw = h.do(t, req)
	require.Equal(t, http.StatusUnauthorized, status, "no token must answer 401: %s", string(raw))
}
