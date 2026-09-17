// Package handler — context compiler HTTP endpoint.
//
// GET /api/v1/context/{node_id}?budget=8000&includeCards=true&model=<name>
// Requires valid JWT via authMW.
//
// GAP-080 phase 2a: with no ?budget=, ?model= selects the model whose context
// window sizes the default budget (percent of the window, configurable with
// CONTEXT_BUDGET_PERCENT). An explicit ?budget= wins and keeps its historical
// 10x-default clamp.
//
// Spec: SPEC-IMPL-GAP-001-context-compiler.md §4.2
// SPEC-PL-06 §6 (multi-reference block): when the target node is a
// multi-reference reply, its persisted selection is loaded and compiled into
// the §6.1 block; no other node's output changes.
package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// ReferenceSelectionLoader loads the persisted multi-reference selection of a
// compile target (SPEC-PL-06 §6), or nil when the target has none.
//
// The interface is owned here so the HTTP layer depends on the capability it
// needs rather than on the service's storage details; *service.TreeServiceImpl
// implements it. The loader lives in internal/service because the canonical
// snapshot digest (referenceManifestHash) is unexported there — and
// internal/service cannot import internal/context (context → card → service is
// already an import path), so the handler is the layer that maps the service
// projection onto the compiler's input type.
type ReferenceSelectionLoader interface {
	LoadCompileSelection(ctx context.Context, nodeID uuid.UUID) (*service.CompileSelection, error)
}

// ContextHandler serves the context compilation endpoint.
type ContextHandler struct {
	compiler      ctxpkg.Compiler
	defaultBudget int
	// selectionLoader is optional: when wired, a multi-reference target
	// compiles its §6.1 block. A nil loader leaves compilation exactly as it
	// was before multi-reference support (DB-free wiring harnesses).
	selectionLoader ReferenceSelectionLoader
	// windowCatalog (optional, GAP-080 phase 2a) derives the DEFAULT budget
	// from the context window of the model named by ?model=; budgetPercent is
	// that derivation's percentage (0 disables it). A nil catalog leaves the
	// flat defaultBudget behaviour byte-identical.
	windowCatalog *ModelWindowCatalog
	budgetPercent int
}

// NewContextHandler returns a handler wired to the given context compiler.
func NewContextHandler(compiler ctxpkg.Compiler, defaultBudget int) *ContextHandler {
	return &ContextHandler{
		compiler:      compiler,
		defaultBudget: defaultBudget,
	}
}

// WithReferenceSelectionLoader wires the persisted-selection loader used for
// multi-reference targets (SPEC-PL-06 §6). Safe to call with nil.
func (h *ContextHandler) WithReferenceSelectionLoader(loader ReferenceSelectionLoader) *ContextHandler {
	h.selectionLoader = loader
	return h
}

// WithModelWindowCatalog wires the model context-window catalog used to derive
// the DEFAULT budget when the request carries no ?budget= (GAP-080 phase 2a),
// plus the percentage of that window to use. Safe to call with nil (and with a
// percent of 0), both of which keep the flat defaultBudget; the option form
// keeps NewContextHandler(compiler, defaultBudget) working for existing
// callers and tests.
func (h *ContextHandler) WithModelWindowCatalog(catalog *ModelWindowCatalog, percent int) *ContextHandler {
	h.windowCatalog = catalog
	h.budgetPercent = percent
	return h
}

// Compile handles GET /api/v1/context/{node_id}.
func (h *ContextHandler) Compile(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := parseNodeID(w, r)
	if !ok {
		return
	}

	// Budget resolution. An explicit ?budget= still wins VERBATIM: it is
	// parsed and clamped exactly as it was before GAP-080, against the FLAT
	// configured default (parsed > defaultBudget*10 → defaultBudget*10). The
	// 10x ceiling is deliberately NOT raised by the window-derived default —
	// a client must not be able to use a large model window to request an
	// unbounded budget.
	//
	// With no ?budget= the default is derived from the model named by ?model=
	// when a catalog is wired (GAP-080 phase 2a); an unknown model, an
	// unreachable catalog, no model, or a percent of 0 all fall back to
	// defaultBudget. The derived value needs no 10x clamp: it is already
	// bounded by the model's own context window, and clamping it against
	// defaultBudget*10 would silently defeat the derivation (a 200k window at
	// 60% is 120k, far above 80k).
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	// An empty ?model= is treated as absent: exactly as if the parameter had
	// not been sent, never a 400.
	var (
		budget       int
		budgetSource string
	)
	if b := r.URL.Query().Get("budget"); b != "" {
		parsed, err := strconv.Atoi(b)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "INVALID_BUDGET", "budget must be a positive integer")
			return
		}
		// Defensive: clamp to 10x default (prevents malicious budget=999999999)
		if parsed > h.defaultBudget*10 {
			parsed = h.defaultBudget * 10
		}
		budget = parsed
		budgetSource = budgetSourceExplicit
	} else {
		budget, budgetSource = resolveDefaultBudget(r.Context(), h.windowCatalog, h.budgetPercent, h.defaultBudget, model)
	}
	log.Ctx(r.Context()).Debug().Str("model", model).Str("source", budgetSource).Int("budget", budget).
		Msg("context: resolved token budget")

	includeCards := false
	if ic := r.URL.Query().Get("includeCards"); ic == "true" || ic == "1" {
		includeCards = true
	}

	// Default includeCards=false per spec
	// resolveRefs defaults to true per spec — only false when explicitly "false"
	resolveRefs := true
	if rr := r.URL.Query().Get("resolveRefs"); rr == "false" || rr == "0" {
		resolveRefs = false
	}

	// MaxAncestors from query param
	maxAncestors := 0
	if ma := r.URL.Query().Get("maxAncestors"); ma != "" {
		if parsed, err := strconv.Atoi(ma); err == nil && parsed > 0 {
			maxAncestors = parsed
		}
	}

	req := ctxpkg.CompileRequest{
		NodeID:       nodeID,
		TokenBudget:  budget,
		MaxAncestors: maxAncestors,
		IncludeCards: includeCards,
		ResolveRefs:  resolveRefs,
	}

	// SPEC-PL-06 §6: a multi-reference target compiles its selected-source
	// block first. The selection is loaded BEFORE the compiler runs, and a
	// selection that cannot be loaded whole fails the request rather than
	// compiling a partial source set (§6.4).
	if h.selectionLoader != nil {
		selection, err := h.selectionLoader.LoadCompileSelection(r.Context(), nodeID)
		if err != nil {
			writeSelectionLoadError(w, r, err)
			return
		}
		if selection != nil {
			req.MultiReference = compileSelectionFrom(selection, budget)
		}
	}

	result, err := h.compiler.Compile(r.Context(), req)
	if err != nil {
		var mrErr *ctxpkg.MultiReferenceError
		switch {
		case errors.Is(err, ctxpkg.ErrNodeNotFound):
			writeError(w, http.StatusNotFound, "NODE_NOT_FOUND", "node not found")
		case errors.Is(err, ctxpkg.ErrInvalidBudget):
			writeError(w, http.StatusBadRequest, "INVALID_BUDGET", "budget must be >= 1")
		case errors.As(err, &mrErr):
			// SPEC-PL-06 §9.4: the compiler carries the exact wire code, and
			// the error already names its own status.
			if status, ok := multiReferenceStatus(mrErr.Code); ok {
				writeError(w, status, mrErr.Code, mrErr.Message)
				return
			}
			log.Ctx(r.Context()).Error().Err(err).Str("path", r.URL.Path).
				Str("code", mrErr.Code).Msg("context compiler multi-reference error")
			writeError(w, http.StatusInternalServerError, "CONTEXT_COMPILE_ERROR", "internal server error")
		case errors.Is(err, ctxpkg.ErrDatabaseUnavailable),
			errors.Is(err, service.ErrDatabaseUnavailable):
			log.Ctx(r.Context()).Error().Err(err).Str("path", r.URL.Path).Msg("context compiler db error")
			writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
		default:
			log.Ctx(r.Context()).Error().Err(err).Str("path", r.URL.Path).Msg("context compiler error")
			writeError(w, http.StatusInternalServerError, "CONTEXT_COMPILE_ERROR", "internal server error")
		}
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// compileSelectionFrom projects the service's persisted selection onto the
// compiler's input type (SPEC-PL-06 §6).
//
// ProfileBudget is the effective turn budget of THIS request — the same number
// placed in CompileRequest.TokenBudget — so the §6.2 allocation has exactly one
// budget source. Every source's SignedContentHash is its verified live hash:
// the loader has already proven the live set is the signed snapshot, so the
// §6.4 per-source check cannot manufacture a false staleness.
func compileSelectionFrom(sel *service.CompileSelection, profileBudget int) *ctxpkg.MultiReferenceSelection {
	out := &ctxpkg.MultiReferenceSelection{
		TreeID:        sel.TreeID,
		Metadata:      sel.Metadata,
		ProfileBudget: profileBudget,
		Sources:       make([]ctxpkg.MultiReferenceSourceInput, 0, len(sel.Sources)),
	}
	for _, src := range sel.Sources {
		out.Sources = append(out.Sources, ctxpkg.MultiReferenceSourceInput{
			NodeID:            src.NodeID,
			AuthorID:          src.AuthorID,
			NodeType:          src.NodeType,
			SequenceNum:       src.SequenceNum,
			CreatedAt:         src.CreatedAt,
			BranchRootID:      src.BranchRootID,
			ContentHash:       src.ContentHash,
			SignedContentHash: src.ContentHash,
			Content:           src.Content,
			Label:             src.Label,
			ColorKey:          src.ColorKey,
		})
	}
	return out
}

// multiReferenceStatus maps a §9.4 compilation code onto its HTTP status.
// An unknown code is not a client error — the caller answers 500.
func multiReferenceStatus(code string) (int, bool) {
	switch code {
	case ctxpkg.CodeReferenceContextBudgetExceeded:
		return http.StatusUnprocessableEntity, true
	case ctxpkg.CodeReferenceSelectionStale:
		return http.StatusConflict, true
	case ctxpkg.CodeReferenceSourceNotFound:
		return http.StatusNotFound, true
	case ctxpkg.CodeReferenceSourceCountTooLow, ctxpkg.CodeReferenceSourceCountTooHigh:
		return http.StatusBadRequest, true
	default:
		return 0, false
	}
}

// writeSelectionLoadError maps a selection-loading failure onto the §9.4
// catalog and the existing error envelope. The service answers with the
// catalog identity itself (REFERENCE_SELECTION_STALE for a modified or
// deleted source set); a database failure is a 503, and a corrupt reserved
// manifest is a 500 — never a fabricated empty block.
func writeSelectionLoadError(w http.ResponseWriter, r *http.Request, err error) {
	if apiErr, ok := service.ReferenceErrorFrom(err); ok {
		writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	if errors.Is(err, service.ErrDatabaseUnavailable) {
		log.Ctx(r.Context()).Error().Err(err).Str("path", r.URL.Path).Msg("multi-reference selection load db error")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
		return
	}
	log.Ctx(r.Context()).Error().Err(err).Str("path", r.URL.Path).Msg("multi-reference selection load failed")
	writeError(w, http.StatusInternalServerError, "CONTEXT_COMPILE_ERROR", "internal server error")
}
