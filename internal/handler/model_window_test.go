// Package handler — model context-window budget tests (GAP-080 phase 2a).
//
// Two halves are pinned here: the catalog's own arithmetic and fallbacks
// (nothing in this file touches a network), and the two HTTP surfaces that
// consume it — the gateway run route (whose applied budget is observable on
// the run record) and the context read route (whose applied budget is
// observable on the compiler request). Phase 2b (the UI slider) is out of
// scope and not exercised.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/gateway"
)

// stubModelLister is a ModelLister with a scripted catalog and a call counter
// (the counter is what makes cache hits assertable).
type stubModelLister struct {
	calls  atomic.Int64
	models []gateway.ModelInfo
	err    error
}

func (s *stubModelLister) ListModels(context.Context) ([]gateway.ModelInfo, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.models, nil
}

// --- catalog -----------------------------------------------------------------

// TestModelWindowCatalog_DerivesPercentOfWindow pins the derivation: percent of
// the model's window, integer FLOOR (not round), and never below 1 for a
// positive window.
func TestModelWindowCatalog_DerivesPercentOfWindow(t *testing.T) {
	catalog := NewModelWindowCatalog(&stubModelLister{models: []gateway.ModelInfo{
		{ID: "big-model", ContextLen: 200000},
		{ID: "small-model", ContextLen: 4096},
		{ID: "tiny-window", ContextLen: 50},
	}}, time.Minute)

	cases := []struct {
		name    string
		model   string
		percent int
		want    int
	}{
		{"200000 @ 60", "big-model", 60, 120000},
		{"4096 @ 60 floors", "small-model", 60, 2457},
		{"200000 @ 1", "big-model", 1, 2000},
		{"200000 @ 100", "big-model", 100, 200000},
		{"50 @ 1 never drops below 1", "tiny-window", 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget, source := catalog.Budget(context.Background(), tc.model, tc.percent, 8000)
			if budget != tc.want {
				t.Fatalf("Budget(%q, %d) = %d, want %d", tc.model, tc.percent, budget, tc.want)
			}
			if source != BudgetSourceWindow {
				t.Fatalf("source = %q, want %q", source, BudgetSourceWindow)
			}
		})
	}
}

// TestModelWindowCatalog_Fallbacks pins the four sources that are NOT "window":
// the knob turned off, an unknown model, an unreachable catalog, and the
// handler-side label for "no catalog was wired at all".
func TestModelWindowCatalog_Fallbacks(t *testing.T) {
	known := []gateway.ModelInfo{{ID: "big-model", ContextLen: 200000}}

	t.Run("percent 0 disables the window path", func(t *testing.T) {
		lister := &stubModelLister{models: known}
		catalog := NewModelWindowCatalog(lister, time.Minute)
		budget, source := catalog.Budget(context.Background(), "big-model", 0, 8000)
		if budget != 8000 || source != BudgetSourceDisabled {
			t.Fatalf("Budget(percent=0) = (%d, %q), want (8000, %q)", budget, source, BudgetSourceDisabled)
		}
		if n := lister.calls.Load(); n != 0 {
			t.Fatalf("a disabled knob must not consult the catalog; got %d calls", n)
		}
	})

	t.Run("unknown model", func(t *testing.T) {
		catalog := NewModelWindowCatalog(&stubModelLister{models: known}, time.Minute)
		budget, source := catalog.Budget(context.Background(), "ghost-model", 60, 8000)
		if budget != 8000 || source != BudgetSourceUnknownModel {
			t.Fatalf("Budget(unknown) = (%d, %q), want (8000, %q)", budget, source, BudgetSourceUnknownModel)
		}
	})

	t.Run("catalog error", func(t *testing.T) {
		catalog := NewModelWindowCatalog(&stubModelLister{err: fmt.Errorf("gateway down")}, time.Minute)
		budget, source := catalog.Budget(context.Background(), "big-model", 60, 8000)
		if budget != 8000 || source != BudgetSourceCatalogError {
			t.Fatalf("Budget(error) = (%d, %q), want (8000, %q)", budget, source, BudgetSourceCatalogError)
		}
	})

	t.Run("no catalog wired", func(t *testing.T) {
		budget, source := resolveDefaultBudget(context.Background(), nil, 60, 8000, "big-model")
		if budget != 8000 || source != budgetSourceNoCatalog {
			t.Fatalf("resolveDefaultBudget(nil) = (%d, %q), want (8000, %q)", budget, source, budgetSourceNoCatalog)
		}
	})
}

// TestModelWindowCatalog_NilSafePaths proves the fallback preconditions hold
// without panicking: nil receivers, a nil lister, no model name, and a catalog
// entry with no usable window.
func TestModelWindowCatalog_NilSafePaths(t *testing.T) {
	var nilCatalog *ModelWindowCatalog
	budget, source := nilCatalog.Budget(context.Background(), "big-model", 60, 8000)
	if budget != 8000 || source != BudgetSourceCatalogError {
		t.Fatalf("nil receiver = (%d, %q), want (8000, %q)", budget, source, BudgetSourceCatalogError)
	}

	noLister := NewModelWindowCatalog(nil, time.Minute)
	budget, source = noLister.Budget(context.Background(), "big-model", 60, 8000)
	if budget != 8000 || source != BudgetSourceCatalogError {
		t.Fatalf("nil lister = (%d, %q), want (8000, %q)", budget, source, BudgetSourceCatalogError)
	}

	lister := &stubModelLister{models: []gateway.ModelInfo{
		{ID: "big-model", ContextLen: 200000},
		{ID: "no-window", ContextLen: 0},
		{ID: "negative-window", ContextLen: -1},
	}}
	catalog := NewModelWindowCatalog(lister, time.Minute)
	for _, model := range []string{"", "   "} {
		budget, source = catalog.Budget(context.Background(), model, 60, 8000)
		if budget != 8000 || source != BudgetSourceUnknownModel {
			t.Fatalf("empty model %q = (%d, %q), want (8000, %q)", model, budget, source, BudgetSourceUnknownModel)
		}
	}
	// A model the catalog knows but has no usable window for is a fallback,
	// not a derivation.
	for _, model := range []string{"no-window", "negative-window", "absent"} {
		budget, source = catalog.Budget(context.Background(), model, 60, 8000)
		if budget != 8000 || source != BudgetSourceUnknownModel {
			t.Fatalf("model %q = (%d, %q), want (8000, %q)", model, budget, source, BudgetSourceUnknownModel)
		}
	}
	// Surrounding whitespace on a real id is tolerated.
	if got, src := catalog.Budget(context.Background(), "  big-model  ", 60, 8000); got != 120000 || src != BudgetSourceWindow {
		t.Fatalf("padded model = (%d, %q), want (120000, %q)", got, src, BudgetSourceWindow)
	}
}

// TestModelWindowCatalog_CacheTTL pins the caching contract: within the ttl a
// second lookup reuses the snapshot (ONE lister call), an expired ttl refetches,
// and a zero ttl means no caching at all — but never a panic.
func TestModelWindowCatalog_CacheTTL(t *testing.T) {
	t.Run("within ttl the lister is called once", func(t *testing.T) {
		lister := &stubModelLister{models: []gateway.ModelInfo{{ID: "big-model", ContextLen: 200000}}}
		catalog := NewModelWindowCatalog(lister, time.Minute)
		for i := 0; i < 3; i++ {
			if got, src := catalog.Budget(context.Background(), "big-model", 60, 8000); got != 120000 || src != BudgetSourceWindow {
				t.Fatalf("call %d = (%d, %q), want (120000, %q)", i+1, got, src, BudgetSourceWindow)
			}
		}
		if n := lister.calls.Load(); n != 1 {
			t.Fatalf("3 lookups within the ttl made %d lister calls, want 1", n)
		}
	})

	t.Run("an expired ttl refetches", func(t *testing.T) {
		lister := &stubModelLister{models: []gateway.ModelInfo{{ID: "big-model", ContextLen: 200000}}}
		catalog := NewModelWindowCatalog(lister, 5*time.Millisecond)
		if _, src := catalog.Budget(context.Background(), "big-model", 60, 8000); src != BudgetSourceWindow {
			t.Fatalf("first lookup source = %q", src)
		}
		time.Sleep(25 * time.Millisecond)
		if _, src := catalog.Budget(context.Background(), "big-model", 60, 8000); src != BudgetSourceWindow {
			t.Fatalf("second lookup source = %q", src)
		}
		if n := lister.calls.Load(); n != 2 {
			t.Fatalf("lookup after ttl expiry made %d lister calls in total, want 2", n)
		}
	})

	t.Run("zero ttl means no caching but no panic", func(t *testing.T) {
		lister := &stubModelLister{models: []gateway.ModelInfo{{ID: "big-model", ContextLen: 200000}}}
		for _, ttl := range []time.Duration{0, -time.Second} {
			catalog := NewModelWindowCatalog(lister, ttl)
			if got, src := catalog.Budget(context.Background(), "big-model", 60, 8000); got != 120000 || src != BudgetSourceWindow {
				t.Fatalf("ttl=%v lookup = (%d, %q)", ttl, got, src)
			}
		}
		if n := lister.calls.Load(); n != 2 {
			t.Fatalf("two uncached lookups made %d lister calls, want 2", n)
		}
	})

	t.Run("catalog failures are not cached", func(t *testing.T) {
		lister := &stubModelLister{err: fmt.Errorf("gateway down")}
		catalog := NewModelWindowCatalog(lister, time.Minute)
		for i := 0; i < 2; i++ {
			if _, src := catalog.Budget(context.Background(), "big-model", 60, 8000); src != BudgetSourceCatalogError {
				t.Fatalf("call %d source = %q, want %q", i+1, src, BudgetSourceCatalogError)
			}
		}
		if n := lister.calls.Load(); n != 2 {
			t.Fatalf("a failed lookup must be retried, not cached: %d calls for 2 lookups", n)
		}
	})
}

// --- gateway run route (AC3) -------------------------------------------------

// TestGatewayContextBudgetFromModelWindow proves the applied budget on
// POST /gateway/runs: with no model the flat ContextDefaultBudget is used, with
// a known model it is window*percent/100, an unknown model falls back, an
// explicit token_budget still wins verbatim, and percent=0 disables the whole
// derivation. The run record carries the applied budget, so the assertion is on
// the 202 body's `run` object (the harness's gateway stub answers the run
// itself — no live gateway is needed).
func TestGatewayContextBudgetFromModelWindow(t *testing.T) {
	const defaultBudget = 8000
	models := []gateway.ModelInfo{{ID: "big-model", ContextLen: 200000}}

	cases := []struct {
		name        string
		percent     int
		body        string
		wantBudget  int
		wantGateway string // expected `model` on the gateway start request
	}{
		{
			name:        "no model falls back to the flat default",
			percent:     60,
			body:        `{"message":"hello","node_id":%q}`,
			wantBudget:  defaultBudget,
			wantGateway: "",
		},
		{
			name:        "a known model derives percent of its window",
			percent:     60,
			body:        `{"message":"hello","node_id":%q,"model":"big-model"}`,
			wantBudget:  120000,
			wantGateway: "big-model",
		},
		{
			name:        "an unknown model falls back",
			percent:     60,
			body:        `{"message":"hello","node_id":%q,"model":"ghost-model"}`,
			wantBudget:  defaultBudget,
			wantGateway: "ghost-model",
		},
		{
			name:        "an explicit token_budget wins verbatim",
			percent:     60,
			body:        `{"message":"hello","node_id":%q,"model":"big-model","token_budget":1234}`,
			wantBudget:  1234,
			wantGateway: "big-model",
		},
		{
			name:        "percent 0 disables the derivation",
			percent:     0,
			body:        `{"message":"hello","node_id":%q,"model":"big-model"}`,
			wantBudget:  defaultBudget,
			wantGateway: "big-model",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newContextGatewayStub()
			defer stub.Close()
			nodeID := uuid.New()
			comp := &capturingCompiler{
				content:  "COMPILED:hello",
				manifest: &ctxpkg.Manifest{RequestID: "req-window", NodeID: nodeID, CompiledAt: time.Now().UTC(), TokensUsed: 3},
			}
			catalog := NewModelWindowCatalog(&stubModelLister{models: models}, time.Minute)
			r, _ := newContextGatewayRouter(t, stub,
				WithContextCompiler(comp, defaultBudget), WithContextBudget(catalog, tc.percent))

			resp, body := gwDoJSON(t, r, http.MethodPost, "/runs", fmt.Sprintf(tc.body, nodeID.String()))
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("start: %d %s", resp.StatusCode, body)
			}
			reqs := comp.calls()
			if len(reqs) != 1 {
				t.Fatalf("want 1 compile call, got %d", len(reqs))
			}
			if reqs[0].TokenBudget != tc.wantBudget {
				t.Fatalf("compile budget = %d, want %d", reqs[0].TokenBudget, tc.wantBudget)
			}
			rec := decodeRunEnvelope(t, body)
			if rec.TokenBudget != tc.wantBudget {
				t.Fatalf("run record token_budget = %d, want %d (body: %s)", rec.TokenBudget, tc.wantBudget, body)
			}
			started := stub.started()
			if len(started) != 1 {
				t.Fatalf("want 1 gateway start request, got %d", len(started))
			}
			if tc.wantGateway == "" {
				if _, present := started[0]["model"]; present {
					t.Fatalf("a model-less run must not name a model: %v", started[0])
				}
				return
			}
			if got := started[0]["model"]; got != tc.wantGateway {
				t.Fatalf("gateway model = %v, want %q", got, tc.wantGateway)
			}
		})
	}
}

// TestGatewayContextBudgetNilCatalogUnchanged proves the nil-catalog path is
// the pre-GAP-080 behaviour: the flat default, no catalog consultation, and a
// faithfully forwarded model.
func TestGatewayContextBudgetNilCatalogUnchanged(t *testing.T) {
	stub := newContextGatewayStub()
	defer stub.Close()
	nodeID := uuid.New()
	comp := &capturingCompiler{content: "C", manifest: &ctxpkg.Manifest{RequestID: "r", TokensUsed: 1}}
	r, _ := newContextGatewayRouter(t, stub, WithContextCompiler(comp, 8000))

	resp, body := gwDoJSON(t, r, http.MethodPost, "/runs",
		fmt.Sprintf(`{"message":"hello","node_id":%q,"model":"big-model"}`, nodeID.String()))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	if reqs := comp.calls(); len(reqs) != 1 || reqs[0].TokenBudget != 8000 {
		t.Fatalf("compile requests = %+v, want a single flat 8000 budget", reqs)
	}
	if rec := decodeRunEnvelope(t, body); rec.TokenBudget != 8000 {
		t.Fatalf("run record token_budget = %d, want 8000", rec.TokenBudget)
	}
	started := stub.started()
	if len(started) != 1 || started[0]["model"] != "big-model" {
		t.Fatalf("model must still be forwarded without a catalog: %v", started)
	}
}

// --- context read route (AC4) -----------------------------------------------

// TestContextHandlerModelWindowBudget proves the read route's budget rules:
// ?model= with no ?budget= compiles with the derived budget; an explicit
// ?budget= keeps its historical parse AND its historical 10x-FLAT-default
// clamp (the derived value does not raise the ceiling); and the two invalid
// budget forms still 400 INVALID_BUDGET without reaching the compiler.
func TestContextHandlerModelWindowBudget(t *testing.T) {
	const defaultBudget = 8000
	lister := &stubModelLister{models: []gateway.ModelInfo{{ID: "big-model", ContextLen: 200000}}}
	catalog := NewModelWindowCatalog(lister, time.Minute)

	cases := []struct {
		name       string
		query      string
		wantStatus int
		wantBudget int
	}{
		{"known model derives the default", "?model=big-model", http.StatusOK, 120000},
		{"no model keeps the flat default", "", http.StatusOK, defaultBudget},
		{"empty model is treated as absent", "?model=", http.StatusOK, defaultBudget},
		{"unknown model falls back", "?model=ghost-model", http.StatusOK, defaultBudget},
		{"an explicit budget wins", "?model=big-model&budget=5000", http.StatusOK, 5000},
		{"the 10x clamp is unchanged by a larger derived budget", "?model=big-model&budget=999999999", http.StatusOK, defaultBudget * 10},
		{"a non-numeric budget still 400s", "?budget=abc", http.StatusBadRequest, 0},
		{"a zero budget still 400s", "?budget=0", http.StatusBadRequest, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodeID := uuid.New()
			comp := &capturingCompiler{content: "c", manifest: &ctxpkg.Manifest{RequestID: "read"}}
			h := NewContextHandler(comp, defaultBudget).WithModelWindowCatalog(catalog, 60)
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

// TestContextHandlerModelWindowNoCatalogUnchanged proves a handler built the
// old way (no catalog option) compiles with exactly the flat default, even
// when the request names a model — the pre-GAP-080 behaviour for every
// existing caller and test.
func TestContextHandlerModelWindowNoCatalogUnchanged(t *testing.T) {
	nodeID := uuid.New()
	comp := &capturingCompiler{content: "c", manifest: &ctxpkg.Manifest{RequestID: "read"}}
	h := NewContextHandler(comp, 8000)
	router := chi.NewRouter()
	router.Get("/context/{node_id}", h.Compile)

	req := httptest.NewRequest(http.MethodGet, "/context/"+nodeID.String()+"?model=big-model", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if reqs := comp.calls(); len(reqs) != 1 || reqs[0].TokenBudget != 8000 {
		t.Fatalf("compile requests = %+v, want a single flat 8000 budget", reqs)
	}
}

// TestModelWindowBudgetSourcesAreStable pins the four source strings: they are
// the observable contract of the derivation (debug logs today, the phase 2b
// budget slider next), so a rename must fail here first.
func TestModelWindowBudgetSourcesAreStable(t *testing.T) {
	got := []string{BudgetSourceDisabled, BudgetSourceWindow, BudgetSourceUnknownModel, BudgetSourceCatalogError}
	want := []string{"disabled", "window", "unknown_model", "catalog_error"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("source[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if strings.Contains(budgetSourceExplicit, "catalog") || strings.Contains(budgetSourceNoCatalog, "window") {
		t.Fatalf("handler labels must not collide with catalog sources: %q / %q", budgetSourceExplicit, budgetSourceNoCatalog)
	}
}

// TestModelWindowCatalog_JSONShapeOfGatewayModels guards the one wire detail
// this file's stub does not exercise end to end: the catalog is fed by
// gateway.ModelInfo, whose JSON tag is context_length — the field the real
// GET /v1/models answer uses.
func TestModelWindowCatalog_JSONShapeOfGatewayModels(t *testing.T) {
	var info gateway.ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"m","context_length":8192}`), &info); err != nil {
		t.Fatal(err)
	}
	if info.ID != "m" || info.ContextLen != 8192 {
		t.Fatalf("gateway.ModelInfo decoded as %+v", info)
	}
}
