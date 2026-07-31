package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	ctxpkg "github.com/totalwindupflightsystems/hermes-canopy/internal/context"
)

// stubCompiler implements context.Compiler for handler tests.
type stubCompiler struct {
	result *ctxpkg.CompiledContext
	err    error
}

func (s *stubCompiler) Compile(ctx context.Context, req ctxpkg.CompileRequest) (*ctxpkg.CompiledContext, error) {
	return s.result, s.err
}

// --- Tests ----------------------------------------------------------------

// 1. 200 OK with valid node ID, returns content + manifest JSON.
func TestContextHandler_OK(t *testing.T) {
	nodeID := uuid.New()
	s := &stubCompiler{
		result: &ctxpkg.CompiledContext{
			Content: "--- node content ---",
			Manifest: &ctxpkg.Manifest{
				RequestID:   "req-1",
				NodeID:      nodeID,
				CompiledAt:  time.Now().UTC(),
				TokenBudget: 1000,
				TokensUsed:  50,
			},
		},
	}

	h := NewContextHandler(s, 8000)

	router := chi.NewRouter()
	router.Get("/context/{node_id}", h.Compile)

	req := httptest.NewRequest(http.MethodGet, "/context/"+nodeID.String(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result ctxpkg.CompiledContext
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result.Content == "" {
		t.Error("expected non-empty content")
	}
	if result.Manifest == nil {
		t.Fatal("expected manifest")
	}
}

// 2. 400 bad budget.
func TestContextHandler_BadBudget(t *testing.T) {
	nodeID := uuid.New()
	s := &stubCompiler{
		err: ctxpkg.ErrInvalidBudget,
	}

	h := NewContextHandler(s, 8000)

	router := chi.NewRouter()
	router.Get("/context/{node_id}", h.Compile)

	req := httptest.NewRequest(http.MethodGet, "/context/"+nodeID.String()+"?budget=0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// 3. 401 no JWT — test that authMW rejects unauthenticated requests.
// This is tested indirectly: when mounted under authMW, the handler
// should 401. Since our test router doesn't have authMW, this test
// just ensures the route exists and is callable. The authMW behavior
// is tested in auth_test.go.
func TestContextHandler_UnauthenticatedRoute(t *testing.T) {
	nodeID := uuid.New()
	s := &stubCompiler{
		result: &ctxpkg.CompiledContext{
			Content: "test",
			Manifest: &ctxpkg.Manifest{
				NodeID:     nodeID,
				CompiledAt: time.Now().UTC(),
			},
		},
	}

	h := NewContextHandler(s, 8000)

	// Mount route directly (no authMW) to test the handler itself
	router := chi.NewRouter()
	router.Get("/context/{node_id}", h.Compile)

	req := httptest.NewRequest(http.MethodGet, "/context/"+nodeID.String(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Without authMW, the handler itself doesn't check auth — 200
	// The auth is enforced by the server wiring (authMW). This test
	// confirms the handler doesn't crash when no UserID is in context.
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (no authmw on test router), got %d", w.Code)
	}
}

// 4. 404 unknown node.
func TestContextHandler_NodeNotFound(t *testing.T) {
	nodeID := uuid.New()
	s := &stubCompiler{
		err: ctxpkg.ErrNodeNotFound,
	}

	h := NewContextHandler(s, 8000)

	router := chi.NewRouter()
	router.Get("/context/{node_id}", h.Compile)

	req := httptest.NewRequest(http.MethodGet, "/context/"+nodeID.String(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// 5. 503 DB-down sentinel.
func TestContextHandler_DBDown(t *testing.T) {
	nodeID := uuid.New()
	s := &stubCompiler{
		err: ctxpkg.ErrDatabaseUnavailable,
	}

	h := NewContextHandler(s, 8000)

	router := chi.NewRouter()
	router.Get("/context/{node_id}", h.Compile)

	req := httptest.NewRequest(http.MethodGet, "/context/"+nodeID.String(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}
