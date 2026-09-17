// Package handler — model context-window catalog (GAP-080 phase 2a).
//
// The context compiler's DEFAULT budget used to be one flat number
// (cfg.ContextDefaultBudget, 8000 tokens) whatever model was called, so a
// model with a 200k window was used at 4% of its capacity. The vision's
// promise is "60% of the model context window", so the default budget is now
// derived from the SELECTED model's window whenever the gateway's model
// catalog can answer — and falls back to the flat default whenever it cannot
// (unknown model, unreachable catalog, or the knob turned off with 0).
//
// The compiler itself is untouched: it is handed one budget number either way.
// The ModelLister interface is owned here, like ContextCompiler and
// ReferenceSelectionLoader, so the handlers depend on the capability they need
// rather than on internal/gateway's transport details.
package handler

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/gateway"
)

// Budget source values reported by ModelWindowCatalog.Budget. They are part of
// the observable contract (debug logs today; the phase 2b budget slider next),
// so they are exactly these four strings.
const (
	// BudgetSourceDisabled: the percentage knob is <= 0, so the
	// window-derived path is off and the flat default always applies.
	BudgetSourceDisabled = "disabled"
	// BudgetSourceWindow: the budget was derived from the selected model's
	// context window.
	BudgetSourceWindow = "window"
	// BudgetSourceUnknownModel: the model catalog was consulted (or the
	// request named no model at all) and no usable window is known for it.
	BudgetSourceUnknownModel = "unknown_model"
	// BudgetSourceCatalogError: the model catalog could not be reached. The
	// caller falls back; the request is NOT failed.
	BudgetSourceCatalogError = "catalog_error"
)

// Handler-side budget labels. They are deliberately NOT among the four catalog
// sources above: nothing was asked of a catalog, so no catalog outcome can be
// claimed for them.
const (
	// budgetSourceNoCatalog: the handler was built without a catalog, so the
	// flat configured default is used unchanged.
	budgetSourceNoCatalog = "no_catalog"
	// budgetSourceExplicit: the caller supplied the budget itself, so no
	// default was resolved at all.
	budgetSourceExplicit = "explicit"
)

// ModelWindow is one entry of the model catalog as a CLIENT sees it
// (GAP-080 phase 2b): the model id, the context window the catalog reports
// for it (0 when the catalog reports none), and the budget that model would
// get at the catalog's percentage. The UI renders exactly these three fields,
// so the JSON tags are the wire contract.
type ModelWindow struct {
	ID            string `json:"id"`
	ContextWindow int    `json:"context_window"`
	DesiredBudget int    `json:"desired_budget"`
}

// ModelLister lists the models the gateway knows about. Satisfied by
// *gateway.Client; the interface lives here so the handler depends on the
// capability, not the transport.
type ModelLister interface {
	ListModels(ctx context.Context) ([]gateway.ModelInfo, error)
}

// ModelWindowCatalog resolves a model's context window into a token budget.
//
// It caches the gateway's model list for ttl so a busy surface does not call
// /v1/models per request; a zero or negative ttl means no caching is
// performed (each lookup refreshes) but is otherwise harmless. It is
// goroutine-safe, and Budget NEVER returns an error: every failure mode is a
// fallback to the caller's flat default, because a request must not fail
// because the model catalog is down.
type ModelWindowCatalog struct {
	lister ModelLister
	ttl    time.Duration

	// mu guards the snapshot. It is held across the refresh so concurrent
	// callers wait for one in-flight fetch instead of stampeding the
	// gateway — a single background-free refresh per ttl is all that is
	// needed.
	mu       sync.Mutex
	windows  map[string]int
	cachedAt time.Time
	loaded   bool
}

// NewModelWindowCatalog builds a catalog over a model lister (the live gateway
// client) with a cache ttl. It performs no I/O: construction never depends on
// the gateway being up, which is what keeps it safe on the server boot path.
// A nil lister is legal and makes every lookup fall back.
func NewModelWindowCatalog(lister ModelLister, ttl time.Duration) *ModelWindowCatalog {
	return &ModelWindowCatalog{lister: lister, ttl: ttl}
}

// Budget resolves the default compilation budget for model at percent of its
// context window, returning the budget and the source that produced it.
//
// Fallback (the flat default) is used when percent <= 0 ("disabled"), when the
// catalog cannot be reached or no lister is wired ("catalog_error"), and when
// no usable window is known for the model ("unknown_model" — the model is
// absent from the catalog, has a non-positive window, or the request named no
// model). A derived budget is floor(window * percent / 100) and at least 1 for
// any positive window. Any receiver, any lister, and any model string are
// safe: this method never panics.
func (c *ModelWindowCatalog) Budget(ctx context.Context, model string, percent int, fallback int) (int, string) {
	if percent <= 0 {
		return fallback, BudgetSourceDisabled
	}
	if c == nil || c.lister == nil {
		return fallback, BudgetSourceCatalogError
	}
	name := strings.TrimSpace(model)
	if name == "" {
		// No model was named, so there is no window to look up and no
		// catalog outcome to claim — and, deliberately, no gateway call:
		// a model-less request must never wait on the model catalog.
		return fallback, BudgetSourceUnknownModel
	}
	window, ok := c.window(ctx, name)
	if !ok {
		return fallback, BudgetSourceCatalogError
	}
	if window <= 0 {
		// The catalog answered but has no usable window for this model.
		return fallback, BudgetSourceUnknownModel
	}
	return windowBudget(window, percent, fallback), BudgetSourceWindow
}

// windowBudget is the ONE derivation of a window-derived budget: percent of
// the context window, integer FLOOR, and never below 1 for a positive window
// (a 50-token window at 1% is 1, not 0). It answers the flat fallback when
// either input is unusable (the knob is off, or no window is known). Budget
// and Models both call it, so the budget a model gets from the read route and
// the budget the UI shows for that same model cannot drift apart.
func windowBudget(window, percent, fallback int) int {
	if percent <= 0 || window <= 0 {
		return fallback
	}
	budget := window * percent / 100
	if budget < 1 {
		budget = 1
	}
	return budget
}

// Window returns the context window the catalog knows for model.
//
// ok is false when there is no answer to report at all — no catalog, no
// lister, no model name, or an unreachable catalog — and callers must treat
// that as "no window is known" rather than as a zero window. A model the
// catalog simply does not list (or lists without a window) yields (0, true):
// asked and answered, nothing usable. This is the explicit-budget ceiling's
// lookup (GAP-080 phase 2b), so it fails closed on every unknown.
func (c *ModelWindowCatalog) Window(ctx context.Context, model string) (int, bool) {
	if c == nil || c.lister == nil {
		return 0, false
	}
	name := strings.TrimSpace(model)
	if name == "" {
		return 0, false
	}
	return c.window(ctx, name)
}

// Models lists the catalog's models with the budget each would get at percent,
// plus the source that describes the answer.
//
// It is a CATALOG READ, not a derivation, and it reuses the same cached
// snapshot as Budget — one five-minute TTL for the whole server, no second
// cache and no per-request gateway call. Two deliberate differences from
// Budget follow from that:
//
//   - it consults the lister even when percent <= 0, because the knob disables
//     the window-derived BUDGET, not the model list a user picks from (every
//     desired_budget is then the flat fallback, and the source is "disabled");
//   - the source describes the LIST: "window" when at least one listed model
//     yields a window-derived budget, "unknown_model" when the catalog
//     answered but no entry carries a usable window (the live gateway's
//     OpenAI-style envelope), "disabled" when the knob is off, and
//     "catalog_error" / "no_catalog" when the list could not be read.
//
// The slice is never nil — an unreachable catalog answers `[]`, so the caller
// has no nil to leak onto the wire — and it is sorted by model id so the same
// catalog always renders in the same order.
func (c *ModelWindowCatalog) Models(ctx context.Context, percent, fallback int) ([]ModelWindow, string) {
	models := []ModelWindow{}
	if c == nil || c.lister == nil {
		return models, budgetSourceNoCatalog
	}
	windows, ok := c.snapshot(ctx)
	if !ok {
		return models, BudgetSourceCatalogError
	}

	ids := make([]string, 0, len(windows))
	for id := range windows {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	source := BudgetSourceUnknownModel
	if percent <= 0 {
		source = BudgetSourceDisabled
	}
	for _, id := range ids {
		window := windows[id]
		if window > 0 && percent > 0 {
			source = BudgetSourceWindow
		}
		models = append(models, ModelWindow{
			ID:            id,
			ContextWindow: window,
			DesiredBudget: windowBudget(window, percent, fallback),
		})
	}
	return models, source
}

// window returns a model's context window from the (possibly cached) model
// list. ok is false when the catalog could not be reached; a model that is
// simply absent yields (0, true), which Budget reports as unknown_model.
func (c *ModelWindowCatalog) window(ctx context.Context, model string) (int, bool) {
	windows, ok := c.snapshot(ctx)
	if !ok {
		return 0, false
	}
	return windows[model], true
}

// snapshot returns the model → context-window map, refreshing it when the ttl
// has expired. ok is false only when the catalog could not be reached; the
// returned map is never mutated in place (a refresh REPLACES it), so a caller
// may keep reading it without holding the lock.
func (c *ModelWindowCatalog) snapshot(ctx context.Context) (map[string]int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl > 0 && c.loaded && time.Since(c.cachedAt) < c.ttl {
		return c.windows, true
	}
	infos, err := c.lister.ListModels(ctx)
	if err != nil {
		// Failures are not cached: the next call retries rather than being
		// pinned to a stale "catalog unreachable" verdict.
		return nil, false
	}
	windows := make(map[string]int, len(infos))
	for _, info := range infos {
		if id := strings.TrimSpace(info.ID); id != "" {
			windows[id] = info.ContextLen
		}
	}
	c.windows = windows
	c.cachedAt = time.Now()
	c.loaded = true
	return windows, true
}

// resolveDefaultBudget is the single place both handlers resolve the DEFAULT
// budget from (GAP-080 phase 2a): the window-derived value when a catalog is
// wired, else the flat configured default. The source label is for debug
// logging (see the BudgetSource* constants).
func resolveDefaultBudget(ctx context.Context, catalog *ModelWindowCatalog, percent, fallback int, model string) (int, string) {
	if catalog == nil {
		return fallback, budgetSourceNoCatalog
	}
	return catalog.Budget(ctx, model, percent, fallback)
}
