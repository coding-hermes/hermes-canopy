package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// --- BodySizeLimit tests ---------------------------------------------------

func TestBodySizeLimitSmallBodyPasses(t *testing.T) {
	body := `{"name":"test"}`
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Body can be read by the handler.
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})
	h := BodySizeLimit(1024)(next)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBufferString(body))
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
	}
}

func TestBodySizeLimitRejectsLargeBody(t *testing.T) {
	// 2048 bytes — exceeds 1024 limit
	large := bytes.Repeat([]byte("a"), 2048)
	blocked := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(blocked)
		t.Fatal("handler called despite oversized body")
	})
	h := BodySizeLimit(1024)(next)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(large))
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}
	// Verify the error body matches our established JSON error format.
	assertErrorResponse(t, rr, "REQUEST_TOO_LARGE")
}

func TestBodySizeLimitNilBodyPasses(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := BodySizeLimit(1024)(next)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
	}
}

// --- RateLimit tests -------------------------------------------------------

func TestRateLimitAllowsRequest(t *testing.T) {
	rl := NewRateLimiter(100, 200)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := RateLimit(rl)(next)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
	}
}

func TestRateLimitExemptsPublicPaths(t *testing.T) {
	rl := NewRateLimiter(0.0, 1) // rate = 0, burst = 1 → only first request passes
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := RateLimit(rl)(next)

	// Exhaust the single token.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("first non-public request should pass; status=%d", rr.Code)
	}

	// Now the token bucket is empty — non-public requests should be rate-limited.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/trees", nil)
	req2.RemoteAddr = "10.0.0.1:54321"
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited request should return 429; got %d", rr2.Code)
	}

	// Public paths should still pass.
	for _, path := range []string{"/health", "/healthz", "/version"} {
		rr3 := httptest.NewRecorder()
		req3 := httptest.NewRequest(http.MethodGet, path, nil)
		req3.RemoteAddr = "10.0.0.1:54321"
		h.ServeHTTP(rr3, req3)
		if rr3.Code != http.StatusNoContent {
			t.Fatalf("public path %s should not be rate-limited; status=%d", path, rr3.Code)
		}
	}
}

func TestRateLimitConcurrentSafe(t *testing.T) {
	rl := NewRateLimiter(1000, 1000) // high rate, high burst
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = rl.Allow("192.168.1.100")
		}()
	}
	wg.Wait()
	// If Allow panics, the test fails. No deadlock means concurrent access is safe.
}

// --- TreeMembershipMiddleware tests ----------------------------------------

type memberCheckerStub struct {
	isMember bool
	err      error
}

func (s *memberCheckerStub) IsMember(_ context.Context, _, _ uuid.UUID) (bool, error) {
	return s.isMember, s.err
}

func TestTreeMembershipAllowMember(t *testing.T) {
	treeID := uuid.New()
	userID := uuid.New()
	checker := &memberCheckerStub{isMember: true, err: nil}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := UserIDFromContext(r.Context())
		if got == uuid.Nil {
			t.Fatal("handler called without user in context")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := TreeMembershipMiddleware(checker)(next)

	// Override chiURLParam to return our test tree ID.
	saved := chiURLParam
	chiURLParam = func(r *http.Request, key string) string {
		if key == "tree_id" {
			return treeID.String()
		}
		return ""
	}
	defer func() { chiURLParam = saved }()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees/"+treeID.String()+"/nodes", nil)
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, userID))
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
	}
}

func TestTreeMembershipRejectsNonMember(t *testing.T) {
	treeID := uuid.New()
	userID := uuid.New()
	checker := &memberCheckerStub{isMember: false, err: nil}

	blocked := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(blocked)
		t.Fatal("handler called for non-member")
	})
	h := TreeMembershipMiddleware(checker)(next)

	saved := chiURLParam
	chiURLParam = func(r *http.Request, key string) string {
		if key == "tree_id" {
			return treeID.String()
		}
		return ""
	}
	defer func() { chiURLParam = saved }()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees/"+treeID.String()+"/nodes", nil)
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, userID))
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusForbidden, rr.Body.String())
	}
	assertErrorResponse(t, rr, "NOT_TREE_MEMBER")
}

func TestTreeMembershipRejectsMissingUser(t *testing.T) {
	treeID := uuid.New()
	checker := &memberCheckerStub{isMember: true, err: nil}

	blocked := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(blocked)
		t.Fatal("handler called without user context")
	})
	h := TreeMembershipMiddleware(checker)(next)

	saved := chiURLParam
	chiURLParam = func(r *http.Request, key string) string {
		if key == "tree_id" {
			return treeID.String()
		}
		return ""
	}
	defer func() { chiURLParam = saved }()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees/"+treeID.String()+"/nodes", nil)
	// No user in context.
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
	assertErrorResponse(t, rr, "TOKEN_MISSING")
}

func TestTreeMembershipRejectsInvalidTreeID(t *testing.T) {
	userID := uuid.New()
	checker := &memberCheckerStub{isMember: true, err: nil}

	blocked := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(blocked)
		t.Fatal("handler called with invalid tree_id")
	})
	h := TreeMembershipMiddleware(checker)(next)

	saved := chiURLParam
	chiURLParam = func(r *http.Request, key string) string {
		if key == "tree_id" {
			return "not-a-uuid"
		}
		return ""
	}
	defer func() { chiURLParam = saved }()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees/not-a-uuid/nodes", nil)
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, userID))
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	assertErrorResponse(t, rr, "INVALID_TREE_ID")
}

func TestTreeMembershipAllowsRoutesWithoutTreeID(t *testing.T) {
	userID := uuid.New()
	checker := &memberCheckerStub{isMember: false, err: nil} // would reject if called

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := TreeMembershipMiddleware(checker)(next)

	// chiURLParam returns "" for any key — simulates a route without {tree_id}.
	saved := chiURLParam
	chiURLParam = func(r *http.Request, key string) string {
		return ""
	}
	defer func() { chiURLParam = saved }()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/approvals/pending", nil)
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, userID))
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
	}
}

func TestTreeMembershipCheckerError(t *testing.T) {
	treeID := uuid.New()
	userID := uuid.New()
	checker := &memberCheckerStub{isMember: false, err: io.ErrUnexpectedEOF}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler called when checker errored")
	})
	h := TreeMembershipMiddleware(checker)(next)

	saved := chiURLParam
	chiURLParam = func(r *http.Request, key string) string {
		if key == "tree_id" {
			return treeID.String()
		}
		return ""
	}
	defer func() { chiURLParam = saved }()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees/"+treeID.String()+"/nodes", nil)
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, userID))
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusInternalServerError, rr.Body.String())
	}
	assertErrorResponse(t, rr, "INTERNAL_ERROR")
}

// --- Helper -----------------------------------------------------------------

func assertErrorResponse(t *testing.T, rr *httptest.ResponseRecorder, expectedCode string) {
	t.Helper()
	var body apiErrorBody
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if body.Error.Code != expectedCode {
		t.Fatalf("error code = %q, want %q; message=%q", body.Error.Code, expectedCode, body.Error.Message)
	}
}
