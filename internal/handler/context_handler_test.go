package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/gateway"
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

// --- GAP-080 phase 2b: the explicit-budget ceiling ---------------------------

// TestContextHandlerExplicitBudgetCeiling pins the ceiling an explicit
// ?budget= is clamped against.
//
// When the request names a model whose context window the catalog knows, the
// ceiling IS that window — an explicit request can ask for exactly what the
// same model gets by default. Every other state (no model, an unknown model, a
// model with no window, an unreachable catalog, no catalog at all) keeps the
// historical flat ceiling, defaultBudget*10, which is the live-verified
// behaviour. The clamp fails CLOSED: no catalog state can RAISE a budget.
func TestContextHandlerExplicitBudgetCeiling(t *testing.T) {
	const defaultBudget = 8000
	const bigWindow = 200000
	const smallWindow = 4096

	known := NewModelWindowCatalog(&stubModelLister{models: []gateway.ModelInfo{
		{ID: "big-model", ContextLen: bigWindow},
		{ID: "small-model", ContextLen: smallWindow},
		{ID: "no-window", ContextLen: 0},
	}}, time.Minute, nil)
	unreachable := NewModelWindowCatalog(&stubModelLister{err: errors.New("gateway down")}, time.Minute, nil)

	cases := []struct {
		name       string
		catalog    *ModelWindowCatalog
		query      string
		wantStatus int
		wantBudget int
	}{
		{"a known window is the ceiling", known, "?model=big-model&budget=999999999", http.StatusOK, bigWindow},
		{"a known window allows exactly the window", known, "?model=big-model&budget=200000", http.StatusOK, bigWindow},
		{"a known window keeps a smaller request", known, "?model=big-model&budget=5000", http.StatusOK, 5000},
		{"a window BELOW the flat ceiling lowers it", known, "?model=small-model&budget=999999999", http.StatusOK, smallWindow},
		{"an unknown model keeps the flat ceiling", known, "?model=ghost-model&budget=999999999", http.StatusOK, defaultBudget * 10},
		{"a model with no window keeps the flat ceiling", known, "?model=no-window&budget=999999999", http.StatusOK, defaultBudget * 10},
		{"no model keeps the flat ceiling", known, "?budget=999999999", http.StatusOK, defaultBudget * 10},
		{"a catalog failure keeps the flat ceiling and cannot raise it", unreachable, "?model=big-model&budget=999999999", http.StatusOK, defaultBudget * 10},
		{"no catalog wired keeps the flat ceiling", nil, "?model=big-model&budget=999999999", http.StatusOK, defaultBudget * 10},
		{"a non-numeric budget still 400s", known, "?model=big-model&budget=abc", http.StatusBadRequest, 0},
		{"a zero budget still 400s", known, "?model=big-model&budget=0", http.StatusBadRequest, 0},
		{"a negative budget still 400s", known, "?model=big-model&budget=-1", http.StatusBadRequest, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodeID := uuid.New()
			comp := &capturingCompiler{content: "c", manifest: &ctxpkg.Manifest{RequestID: "ceiling"}}
			h := NewContextHandler(comp, defaultBudget).WithModelWindowCatalog(tc.catalog, 60)
			router := chi.NewRouter()
			router.Get("/context/{node_id}", h.Compile)

			req := httptest.NewRequest(http.MethodGet, "/context/"+nodeID.String()+tc.query, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				if code := decodeAPIError(t, w.Body.String()).Error.Code; code != "INVALID_BUDGET" {
					t.Fatalf("error code = %q, want INVALID_BUDGET: %s", code, w.Body.String())
				}
				if n := len(comp.calls()); n != 0 {
					t.Fatalf("an invalid budget must not reach the compiler; %d calls", n)
				}
				return
			}
			reqs := comp.calls()
			if len(reqs) != 1 {
				t.Fatalf("want 1 compile call, got %d", len(reqs))
			}
			if reqs[0].TokenBudget != tc.wantBudget {
				t.Fatalf("compile budget = %d, want %d", reqs[0].TokenBudget, tc.wantBudget)
			}
		})
	}
}

// TestContextHandlerExplicitBudgetCeilingIsNotAlsoTheDerivedDefault proves the
// two budget paths stay separate: the ceiling for an EXPLICIT budget and the
// DEFAULT for a model-less request are independent rules, and neither is
// re-derived from the other. A 4096-window model gets 2457 by default and can
// be asked for up to 4096 explicitly.
func TestContextHandlerExplicitBudgetCeilingIsNotAlsoTheDerivedDefault(t *testing.T) {
	const defaultBudget = 8000
	catalog := NewModelWindowCatalog(&stubModelLister{models: []gateway.ModelInfo{
		{ID: "small-model", ContextLen: 4096},
	}}, time.Minute, nil)

	cases := []struct {
		query      string
		wantBudget int
	}{
		{"?model=small-model", 2457},                  // derived default
		{"?model=small-model&budget=2457", 2457},      // explicit, under the ceiling
		{"?model=small-model&budget=4096", 4096},      // explicit, at the ceiling
		{"?model=small-model&budget=999999999", 4096}, // explicit, clamped to the window
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			nodeID := uuid.New()
			comp := &capturingCompiler{content: "c", manifest: &ctxpkg.Manifest{RequestID: "both"}}
			h := NewContextHandler(comp, defaultBudget).WithModelWindowCatalog(catalog, 60)
			router := chi.NewRouter()
			router.Get("/context/{node_id}", h.Compile)

			req := httptest.NewRequest(http.MethodGet, "/context/"+nodeID.String()+tc.query, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if reqs := comp.calls(); len(reqs) != 1 || reqs[0].TokenBudget != tc.wantBudget {
				t.Fatalf("compile budget = %d, want %d", reqs[0].TokenBudget, tc.wantBudget)
			}
		})
	}
}
