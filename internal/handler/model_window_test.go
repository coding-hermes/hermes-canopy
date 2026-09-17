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
// ?budget= is clamped to the model's own window when the catalog knows it
// (GAP-080 phase 2b) and otherwise keeps its historical 10x-FLAT-default
// ceiling (the derived value does not raise that ceiling); and the two invalid
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
		{"an explicit budget is clamped to the model window", "?model=big-model&budget=999999999", http.StatusOK, 200000},
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

// TestModelWindowCatalog_JSONShapeOfGatewayModels guards the wire details this
// file's stub does not exercise end to end: the catalog is fed by
// gateway.ModelInfo, whose JSON tag is context_length — the field the real
// GET /v1/models answer would use — and, live, by the deployed gateway's
// OpenAI-style "data" envelope, whose entries carry NO context window at all.
// Window 0 is not a derivation: that list must fall back flat, with the
// unknown_model source, rather than have a window invented for it.
func TestModelWindowCatalog_JSONShapeOfGatewayModels(t *testing.T) {
	var info gateway.ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"m","context_length":8192}`), &info); err != nil {
		t.Fatal(err)
	}
	if info.ID != "m" || info.ContextLen != 8192 {
		t.Fatalf("gateway.ModelInfo decoded as %+v", info)
	}

	t.Run("the live gateway envelope through the catalog", func(t *testing.T) {
		// Verbatim GET http://127.0.0.1:8642/v1/models (the gateway
		// api_server this server passes to gateway.NewClient).
		const livePayload = `{
    "object": "list",
    "data": [
        {
            "id": "Hermes Agent",
            "object": "model",
            "created": 1789682544,
            "owned_by": "hermes",
            "permission": [],
            "root": "Hermes Agent",
            "parent": null
        }
    ]
}`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(livePayload))
		}))
		defer srv.Close()

		client, err := gateway.NewClient(srv.URL, "k")
		if err != nil {
			t.Fatal(err)
		}
		catalog := NewModelWindowCatalog(client, time.Minute)
		budget, source := catalog.Budget(context.Background(), "Hermes Agent", 60, 8000)
		if budget != 8000 || source != BudgetSourceUnknownModel {
			t.Fatalf("Budget(live payload, 60) = (%d, %q), want (8000, %q): the data envelope must decode, and a window it does not report must not be invented",
				budget, source, BudgetSourceUnknownModel)
		}

		// The same catalog still derives when the gateway DOES report a
		// window: the fallback above is about the missing field, not about
		// the envelope.
		srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Hermes Agent","object":"model","context_length":200000}]}`))
		}))
		defer srv2.Close()
		client2, err := gateway.NewClient(srv2.URL, "k")
		if err != nil {
			t.Fatal(err)
		}
		derived, src := NewModelWindowCatalog(client2, time.Minute).Budget(context.Background(), "Hermes Agent", 60, 8000)
		if derived != 120000 || src != BudgetSourceWindow {
			t.Fatalf("Budget(data envelope with a 200000 window, 60) = (%d, %q), want (120000, %q)", derived, src, BudgetSourceWindow)
		}
	})
}

// --- model catalog read (GAP-080 phase 2b) -----------------------------------

// TestModelWindowCatalog_ModelsArithmeticAndOrder pins the per-entry contract
// of the catalog read: window and desired_budget are the same derivation
// Budget uses, an entry the catalog has no window for is a flat fallback
// rather than an invented window, ids are trimmed and deduped, and the list is
// sorted so the same catalog always renders in the same order.
func TestModelWindowCatalog_ModelsArithmeticAndOrder(t *testing.T) {
	const fallback = 8000
	lister := &stubModelLister{models: []gateway.ModelInfo{
		{ID: "small-model", ContextLen: 4096},
		{ID: "big-model", ContextLen: 200000},
		{ID: "no-window", ContextLen: 0},
		{ID: "negative-window", ContextLen: -1},
		{ID: "  padded  ", ContextLen: 1000},
		{ID: "", ContextLen: 999},
		{ID: "small-model", ContextLen: 4096},
	}}
	catalog := NewModelWindowCatalog(lister, time.Minute)

	models, source := catalog.Models(context.Background(), 60, fallback)
	if source != BudgetSourceWindow {
		t.Fatalf("source = %q, want %q", source, BudgetSourceWindow)
	}
	want := []ModelWindow{
		{ID: "big-model", ContextWindow: 200000, DesiredBudget: 120000},
		{ID: "negative-window", ContextWindow: -1, DesiredBudget: fallback},
		{ID: "no-window", ContextWindow: 0, DesiredBudget: fallback},
		{ID: "padded", ContextWindow: 1000, DesiredBudget: 600},
		{ID: "small-model", ContextWindow: 4096, DesiredBudget: 2457},
	}
	if len(models) != len(want) {
		t.Fatalf("Models() returned %d entries, want %d: %+v", len(models), len(want), models)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("Models()[%d] = %+v, want %+v", i, models[i], want[i])
		}
	}

	// The tilt window floor: 1% of a 50-token window is 1, never 0.
	tiny := NewModelWindowCatalog(&stubModelLister{models: []gateway.ModelInfo{{ID: "tiny", ContextLen: 50}}}, time.Minute)
	got, _ := tiny.Models(context.Background(), 1, fallback)
	if len(got) != 1 || got[0].DesiredBudget != 1 {
		t.Fatalf("Models(tiny, 1%%) = %+v, want a single entry with desired_budget 1", got)
	}
}

// TestModelWindowCatalog_ModelsSources pins the source a catalog read reports
// in each state: the knob off, an answered catalog with no usable window
// anywhere (the live gateway's OpenAI-style envelope), an unreachable catalog,
// and no catalog at all.
func TestModelWindowCatalog_ModelsSources(t *testing.T) {
	t.Run("percent 0 reports disabled and keeps the flat budget", func(t *testing.T) {
		lister := &stubModelLister{models: []gateway.ModelInfo{{ID: "big-model", ContextLen: 200000}}}
		models, source := NewModelWindowCatalog(lister, time.Minute).Models(context.Background(), 0, 8000)
		if source != BudgetSourceDisabled {
			t.Fatalf("source = %q, want %q", source, BudgetSourceDisabled)
		}
		if len(models) != 1 || models[0].DesiredBudget != 8000 {
			t.Fatalf("Models(percent 0) = %+v, want one flat-budget entry", models)
		}
		if n := lister.calls.Load(); n != 1 {
			t.Fatalf("a catalog READ must still consult the lister with the knob off; got %d calls", n)
		}
	})

	t.Run("a catalog with no windows at all reports unknown_model", func(t *testing.T) {
		lister := &stubModelLister{models: []gateway.ModelInfo{{ID: "Hermes Agent", ContextLen: 0}}}
		models, source := NewModelWindowCatalog(lister, time.Minute).Models(context.Background(), 60, 8000)
		if source != BudgetSourceUnknownModel {
			t.Fatalf("source = %q, want %q", source, BudgetSourceUnknownModel)
		}
		if len(models) != 1 || models[0].DesiredBudget != 8000 {
			t.Fatalf("Models(no windows) = %+v", models)
		}
	})

	t.Run("an unreachable catalog reports catalog_error", func(t *testing.T) {
		models, source := NewModelWindowCatalog(&stubModelLister{err: fmt.Errorf("gateway down")}, time.Minute).
			Models(context.Background(), 60, 8000)
		if source != BudgetSourceCatalogError {
			t.Fatalf("source = %q, want %q", source, BudgetSourceCatalogError)
		}
		if models == nil || len(models) != 0 {
			t.Fatalf("Models(error) = %#v, want an empty non-nil slice", models)
		}
	})

	t.Run("no lister and no catalog report no_catalog", func(t *testing.T) {
		models, source := NewModelWindowCatalog(nil, time.Minute).Models(context.Background(), 60, 8000)
		if source != budgetSourceNoCatalog || models == nil || len(models) != 0 {
			t.Fatalf("Models(nil lister) = (%#v, %q), want an empty slice and %q", models, source, budgetSourceNoCatalog)
		}
		var nilCatalog *ModelWindowCatalog
		models, source = nilCatalog.Models(context.Background(), 60, 8000)
		if source != budgetSourceNoCatalog || models == nil || len(models) != 0 {
			t.Fatalf("nil receiver.Models() = (%#v, %q), want an empty slice and %q", models, source, budgetSourceNoCatalog)
		}
	})
}

// TestModelWindowCatalog_ModelsSharesTheCache proves the catalog read is not a
// second cache: a Models call and a Budget call inside one ttl share the single
// snapshot (ONE lister call for both), so the new route cannot double the
// gateway traffic.
func TestModelWindowCatalog_ModelsSharesTheCache(t *testing.T) {
	lister := &stubModelLister{models: []gateway.ModelInfo{{ID: "big-model", ContextLen: 200000}}}
	catalog := NewModelWindowCatalog(lister, time.Minute)
	if _, src := catalog.Budget(context.Background(), "big-model", 60, 8000); src != BudgetSourceWindow {
		t.Fatalf("Budget source = %q", src)
	}
	for i := 0; i < 3; i++ {
		if models, src := catalog.Models(context.Background(), 60, 8000); src != BudgetSourceWindow || len(models) != 1 {
			t.Fatalf("Models call %d = (%+v, %q)", i+1, models, src)
		}
	}
	if n := lister.calls.Load(); n != 1 {
		t.Fatalf("one snapshot must serve both surfaces: %d lister calls for 1 Budget + 3 Models", n)
	}
}

// TestModelWindowCatalog_WindowLookup pins the explicit-budget ceiling's
// lookup (GAP-080 phase 2b): a known model reports its window, an unknown one
// reports "no window" with ok=true, and every unknown — no model, no catalog,
// no lister, an unreachable catalog — reports ok=false so the caller can only
// ever fall back to the flat ceiling.
func TestModelWindowCatalog_WindowLookup(t *testing.T) {
	catalog := NewModelWindowCatalog(&stubModelLister{models: []gateway.ModelInfo{
		{ID: "big-model", ContextLen: 200000},
		{ID: "no-window", ContextLen: 0},
	}}, time.Minute)

	cases := []struct {
		name       string
		catalog    *ModelWindowCatalog
		model      string
		wantWindow int
		wantOK     bool
	}{
		{"a known model", catalog, "big-model", 200000, true},
		{"padded name tolerated", catalog, "  big-model  ", 200000, true},
		{"a model with no window", catalog, "no-window", 0, true},
		{"an absent model", catalog, "ghost-model", 0, true},
		{"no model named", catalog, "", 0, false},
		{"blank model named", catalog, "   ", 0, false},
		{"an unreachable catalog", NewModelWindowCatalog(&stubModelLister{err: fmt.Errorf("down")}, time.Minute), "big-model", 0, false},
		{"no lister", NewModelWindowCatalog(nil, time.Minute), "big-model", 0, false},
		{"no catalog", nil, "big-model", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			window, ok := tc.catalog.Window(context.Background(), tc.model)
			if window != tc.wantWindow || ok != tc.wantOK {
				t.Fatalf("Window(%q) = (%d, %v), want (%d, %v)", tc.model, window, ok, tc.wantWindow, tc.wantOK)
			}
		})
	}
}

// modelsEnvelope is GET /api/v1/gateway/models as the UI reads it.
type modelsEnvelope struct {
	Models []struct {
		ID            string `json:"id"`
		ContextWindow int    `json:"context_window"`
		DesiredBudget int    `json:"desired_budget"`
	} `json:"models"`
	Percent       int    `json:"percent"`
	DefaultBudget int    `json:"default_budget"`
	Source        string `json:"source"`
}

// gatewayModelsRouter mounts the real gateway router where server.go mounts it
// (/api/v1/gateway), so the path under test is the production path and not a
// test-only one.
func gatewayModelsRouter(opts ...GatewayHandlerOption) chi.Router {
	r := chi.NewRouter()
	r.Mount("/api/v1/gateway", NewGatewayHandler(nil, opts...).Routes())
	return r
}

func getModels(t *testing.T, r chi.Router) (int, string, modelsEnvelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/gateway/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env modelsEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("models body not decodable (%v): %s", err, w.Body.String())
	}
	return w.Code, w.Body.String(), env
}

// TestGatewayModelsRoute_Shape pins the wire contract of the new route: the
// exact envelope, the per-model arithmetic, and the handler-level knobs echoed
// back so the UI can size its budget control without a second call.
func TestGatewayModelsRoute_Shape(t *testing.T) {
	const defaultBudget = 8000
	lister := &stubModelLister{models: []gateway.ModelInfo{
		{ID: "small-model", ContextLen: 4096},
		{ID: "big-model", ContextLen: 200000},
		{ID: "no-window", ContextLen: 0},
	}}
	r := gatewayModelsRouter(
		WithContextCompiler(nil, defaultBudget),
		WithContextBudget(NewModelWindowCatalog(lister, time.Minute), 60))

	code, _, env := getModels(t, r)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	want := []struct {
		id     string
		window int
		budget int
	}{
		{"big-model", 200000, 120000},
		{"no-window", 0, defaultBudget},
		{"small-model", 4096, 2457},
	}
	if len(env.Models) != len(want) {
		t.Fatalf("models = %+v, want %d entries", env.Models, len(want))
	}
	for i, w := range want {
		got := env.Models[i]
		if got.ID != w.id || got.ContextWindow != w.window || got.DesiredBudget != w.budget {
			t.Fatalf("models[%d] = %+v, want {id:%s window:%d budget:%d}", i, got, w.id, w.window, w.budget)
		}
	}
	if env.Percent != 60 {
		t.Fatalf("percent = %d, want 60", env.Percent)
	}
	if env.DefaultBudget != defaultBudget {
		t.Fatalf("default_budget = %d, want %d", env.DefaultBudget, defaultBudget)
	}
	if env.Source != BudgetSourceWindow {
		t.Fatalf("source = %q, want %q", env.Source, BudgetSourceWindow)
	}
}

// TestGatewayModelsRoute_EmptyListNeverNullOnFailure is the fail-soft half of
// the contract: a catalog that cannot answer is a 200 with an EMPTY ARRAY and
// a source that names why — never a 5xx, and never a JSON null for a client to
// map over. The raw body is asserted, because `null` and `[]` decode
// identically into a Go slice but differ catastrophically in the browser.
func TestGatewayModelsRoute_EmptyListNeverNullOnFailure(t *testing.T) {
	const defaultBudget = 8000
	cases := []struct {
		name       string
		opts       []GatewayHandlerOption
		wantSource string
	}{
		{
			"an unreachable catalog",
			[]GatewayHandlerOption{WithContextBudget(NewModelWindowCatalog(&stubModelLister{err: fmt.Errorf("gateway down")}, time.Minute), 60)},
			BudgetSourceCatalogError,
		},
		{
			"no lister wired",
			[]GatewayHandlerOption{WithContextBudget(NewModelWindowCatalog(nil, time.Minute), 60)},
			budgetSourceNoCatalog,
		},
		{
			"no catalog wired at all",
			[]GatewayHandlerOption{WithContextBudget(nil, 60)},
			budgetSourceNoCatalog,
		},
		{
			"no gateway options at all",
			nil,
			budgetSourceNoCatalog,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := append([]GatewayHandlerOption{WithContextCompiler(nil, defaultBudget)}, tc.opts...)
			code, raw, env := getModels(t, gatewayModelsRouter(opts...))
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (the catalog must never 5xx): %s", code, raw)
			}
			if !strings.Contains(raw, `"models":[]`) {
				t.Fatalf("body must carry an empty ARRAY, not null: %s", raw)
			}
			if env.Models == nil {
				t.Fatalf("models decoded as nil: %s", raw)
			}
			if len(env.Models) != 0 {
				t.Fatalf("models = %+v, want none", env.Models)
			}
			if env.Source != tc.wantSource {
				t.Fatalf("source = %q, want %q: %s", env.Source, tc.wantSource, raw)
			}
			if env.DefaultBudget != defaultBudget {
				t.Fatalf("default_budget = %d, want %d", env.DefaultBudget, defaultBudget)
			}
		})
	}
}

// TestGatewayModelsRoute_PercentZero pins the knob's effect on the read: the
// model list is still served (the UI needs a choice), every desired_budget is
// the flat default, and the source says the derivation is off.
func TestGatewayModelsRoute_PercentZero(t *testing.T) {
	const defaultBudget = 8000
	lister := &stubModelLister{models: []gateway.ModelInfo{{ID: "big-model", ContextLen: 200000}}}
	r := gatewayModelsRouter(
		WithContextCompiler(nil, defaultBudget),
		WithContextBudget(NewModelWindowCatalog(lister, time.Minute), 0))

	code, _, env := getModels(t, r)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if env.Percent != 0 {
		t.Fatalf("percent = %d, want 0", env.Percent)
	}
	if env.Source != BudgetSourceDisabled {
		t.Fatalf("source = %q, want %q", env.Source, BudgetSourceDisabled)
	}
	if len(env.Models) != 1 || env.Models[0].ContextWindow != 200000 || env.Models[0].DesiredBudget != defaultBudget {
		t.Fatalf("models = %+v, want the model listed with the flat budget", env.Models)
	}
}

// TestGatewayModelsRoute_ConsultsCatalogOncePerTTL is the caching half: the
// route is served from the SAME five-minute snapshot as the compile surfaces,
// so a UI polling it does not multiply gateway traffic.
func TestGatewayModelsRoute_ConsultsCatalogOncePerTTL(t *testing.T) {
	lister := &stubModelLister{models: []gateway.ModelInfo{{ID: "big-model", ContextLen: 200000}}}
	r := gatewayModelsRouter(
		WithContextCompiler(nil, 8000),
		WithContextBudget(NewModelWindowCatalog(lister, time.Minute), 60))

	for i := 0; i < 4; i++ {
		if code, raw, env := getModels(t, r); code != http.StatusOK || env.Source != BudgetSourceWindow {
			t.Fatalf("request %d = (%d, %q): %s", i+1, code, env.Source, raw)
		}
	}
	if n := lister.calls.Load(); n != 1 {
		t.Fatalf("4 requests within the ttl made %d lister calls, want 1", n)
	}
}
