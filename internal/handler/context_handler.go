// Package handler — context compiler HTTP endpoint.
//
// GET /api/v1/context/{node_id}?budget=8000&includeCards=true
// Requires valid JWT via authMW.
//
// Spec: SPEC-IMPL-GAP-001-context-compiler.md §4.2
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// ContextHandler serves the context compilation endpoint.
type ContextHandler struct {
	compiler      context.Compiler
	defaultBudget int
}

// NewContextHandler returns a handler wired to the given context compiler.
func NewContextHandler(compiler context.Compiler, defaultBudget int) *ContextHandler {
	return &ContextHandler{
		compiler:      compiler,
		defaultBudget: defaultBudget,
	}
}

// Compile handles GET /api/v1/context/{node_id}.
func (h *ContextHandler) Compile(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := parseNodeID(w, r)
	if !ok {
		return
	}

	// Parse budget query param
	budget := h.defaultBudget
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
	}

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

	req := context.CompileRequest{
		NodeID:       nodeID,
		TokenBudget:  budget,
		MaxAncestors: maxAncestors,
		IncludeCards: includeCards,
		ResolveRefs:  resolveRefs,
	}

	result, err := h.compiler.Compile(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, context.ErrNodeNotFound):
			writeError(w, http.StatusNotFound, "NODE_NOT_FOUND", "node not found")
		case errors.Is(err, context.ErrInvalidBudget):
			writeError(w, http.StatusBadRequest, "INVALID_BUDGET", "budget must be >= 1")
		case errors.Is(err, context.ErrDatabaseUnavailable),
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
