// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file implements the live Hermes gateway surface (GAP-050).
//
// Endpoints (mounted at /api/v1/gateway by server.go):
//
//	GET  /status                  — gateway connectivity + run counts
//	GET  /runs                    — registry snapshot (newest first)
//	POST /runs                    — start a REAL Hermes gateway run; with a
//	                                node_id the model receives the COMPILED
//	                                context and the run carries its manifest
//	                                (GAP-075)
//	GET  /runs/{run_id}           — single run record
//	GET  /runs/{run_id}/events    — SSE stream (history replay + live fan-out)
//	POST /runs/{run_id}/stop      — interrupt a run on the gateway
//	POST /runs/{run_id}/approval  — resolve a pending run approval
//
// All gateway traffic flows through internal/gateway: canopyd is a CLIENT of
// the Hermes gateway api_server (:8642), never a reimplementation of it.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/gateway"
)

// ContextCompiler compiles a node's budgeted context. Satisfied by
// context.Compiler. The interface is owned by the HTTP layer (like
// ReferenceSelectionLoader in context_handler.go) so the gateway handler
// depends on the capability it needs, and so gateway.Service keeps knowing
// nothing about context compilation.
type ContextCompiler interface {
	Compile(ctx context.Context, req ctxpkg.CompileRequest) (*ctxpkg.CompiledContext, error)
}

// GatewayHandler exposes live Hermes gateway state to the Canopy frontend.
type GatewayHandler struct {
	svc *gateway.Service
	// compiler is optional: when wired, POST /gateway/runs with a node_id
	// sends the COMPILED context to the model instead of the raw message
	// (GAP-075, SPEC-FTR-07 §"Context manifest assembly"). A nil compiler
	// leaves every request on the raw path, except a node-scoped request,
	// which fails closed with context_compiler_unavailable.
	compiler ContextCompiler
	// defaultBudget is the compiler budget applied when a request does not
	// carry token_budget (mirrors cfg.ContextDefaultBudget).
	defaultBudget int
}

// GatewayHandlerOption configures the gateway handler.
type GatewayHandlerOption func(*GatewayHandler)

// WithContextCompiler wires the context compiler used to assemble the
// model-call payload for node-scoped runs, plus the default token budget
// applied when a request omits token_budget.
func WithContextCompiler(c ContextCompiler, defaultBudget int) GatewayHandlerOption {
	return func(h *GatewayHandler) {
		h.compiler = c
		h.defaultBudget = defaultBudget
	}
}

// NewGatewayHandler builds the handler around a gateway service.
func NewGatewayHandler(svc *gateway.Service, opts ...GatewayHandlerOption) *GatewayHandler {
	h := &GatewayHandler{svc: svc}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Routes returns the chi router for the gateway surface.
func (h *GatewayHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/status", h.Status)
	r.Get("/runs", h.ListRuns)
	r.Post("/runs", h.StartRun)
	r.Get("/runs/{run_id}", h.GetRun)
	r.Get("/runs/{run_id}/events", h.RunEvents)
	r.Post("/runs/{run_id}/stop", h.StopRun)
	r.Post("/runs/{run_id}/approval", h.RespondApproval)
	return r
}

// statusResponse is the dashboard's gateway connectivity summary.
type statusResponse struct {
	Connected  bool                `json:"connected"`
	BaseURL    string              `json:"base_url"`
	Error      string              `json:"error,omitempty"`
	RunCount   int                 `json:"run_count"`
	ActiveRuns int                 `json:"active_runs"`
	Runs       []gateway.RunRecord `json:"recent_runs,omitempty"`
}

// Status reports gateway connectivity and registry counts.
func (h *GatewayHandler) Status(w http.ResponseWriter, r *http.Request) {
	resp := statusResponse{BaseURL: h.svc.Client().BaseURL()}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := h.svc.Connected(ctx); err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Connected = true
	recs := h.svc.ListRuns(ctx)
	resp.RunCount = len(recs)
	for _, rec := range recs {
		if !rec.IsTerminal() {
			resp.ActiveRuns++
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListRuns returns the registry snapshot, newest first, with live status
// refresh for non-terminal runs.
func (h *GatewayHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	recs := h.svc.ListRuns(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"runs": recs})
}

// startRunRequest is the Canopy-side start-run body. `message` is the raw
// user message; `session_id` scopes the conversation when provided.
//
// `node_id` opts the run into the context compiler (GAP-075): the compiled
// payload — not the raw message — becomes the gateway's `input`, and the
// compiler's manifest is attached to the run record. `token_budget` overrides
// the configured default budget for this call only.
type startRunRequest struct {
	Message     string `json:"message"`
	SessionID   string `json:"session_id,omitempty"`
	NodeID      string `json:"node_id,omitempty"`      // UUID; enables compilation
	TokenBudget int    `json:"token_budget,omitempty"` // 0 => handler default
}

// StartRun creates a real Hermes gateway run (HTTP 202 from the gateway).
//
// With a node_id the run is context-compiled first: compile failures are
// answered here and NEVER fall back to the raw message, because a silent
// fallback would send the model a payload the manifest does not describe.
func (h *GatewayHandler) StartRun(w http.ResponseWriter, r *http.Request) {
	var req startRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "message is required")
		return
	}
	in := gateway.StartRunInput{Message: req.Message, SessionID: req.SessionID}
	if req.NodeID != "" && !h.resolveContext(w, r, &req, &in) {
		return // the compile error response is already written
	}
	rec, err := h.svc.StartRunWithContext(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadGateway, "gateway_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id": rec.RunID,
		"status": rec.Status,
		"run":    rec,
	})
}

// resolveContext compiles the node's budgeted context into the start input.
// It reports false after writing the error response; the caller must then
// return without touching the gateway.
func (h *GatewayHandler) resolveContext(w http.ResponseWriter, r *http.Request, req *startRunRequest, in *gateway.StartRunInput) bool {
	nodeID, err := uuid.Parse(req.NodeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "node_id must be a valid UUID")
		return false
	}
	if h.compiler == nil {
		writeError(w, http.StatusServiceUnavailable, "context_compiler_unavailable",
			"context compiler is not configured; a node-scoped run cannot be started")
		return false
	}
	budget := req.TokenBudget
	if budget <= 0 {
		budget = h.defaultBudget
	}
	compiled, err := h.compiler.Compile(r.Context(), ctxpkg.CompileRequest{
		NodeID:       nodeID,
		TokenBudget:  budget,
		MaxAncestors: 0,
		IncludeCards: false,
		ResolveRefs:  true,
	})
	if err != nil {
		if errors.Is(err, ctxpkg.ErrNodeNotFound) {
			writeError(w, http.StatusNotFound, "node_not_found", "node not found")
			return false
		}
		writeError(w, http.StatusUnprocessableEntity, "context_compile_failed", err.Error())
		return false
	}
	if compiled == nil {
		writeError(w, http.StatusUnprocessableEntity, "context_compile_failed", "compiler returned no context")
		return false
	}
	manifestJSON, tokensUsed, err := manifestPayload(compiled)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "context_compile_failed", err.Error())
		return false
	}
	in.Input = compiled.Content
	in.SourceNodeID = req.NodeID
	in.TokenBudget = budget
	in.ContextTokens = tokensUsed
	in.ManifestJSON = manifestJSON
	return true
}

// manifestPayload serialises the compiled manifest for the run record and
// reports the manifest's own TokensUsed. A nil manifest is legal (degraded
// compiled contexts still carry content) and yields no manifest payload.
func manifestPayload(compiled *ctxpkg.CompiledContext) (json.RawMessage, int, error) {
	if compiled.Manifest == nil {
		return nil, 0, nil
	}
	raw, err := json.Marshal(compiled.Manifest)
	if err != nil {
		return nil, 0, fmt.Errorf("manifest encode failed: %w", err)
	}
	return raw, compiled.Manifest.TokensUsed, nil
}

// GetRun returns a single run record.
func (h *GatewayHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	rec, ok := h.svc.Run(runID)
	if !ok {
		writeError(w, http.StatusNotFound, "run_not_found", "run "+runID+" not found")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// RunEvents streams a run's events to the browser: bounded history replay
// followed by live fan-out. Event payloads keep the gateway's own SSE shape
// (a RunEvent JSON object per data line) so frontend parsing matches the
// hermes-webui contract.
func (h *GatewayHandler) RunEvents(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	if _, ok := h.svc.Run(runID); !ok {
		writeError(w, http.StatusNotFound, "run_not_found", "run "+runID+" not found")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer cannot flush")
		return
	}

	// Subscribe BEFORE replaying history so no live event is missed; the
	// frontend dedupes by (event, timestamp, content) — an event can arrive
	// both in the replay and on the live channel.
	ch, cancel, err := h.svc.Subscribe(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "run_not_found", err.Error())
		return
	}
	defer cancel()

	writeSSE := func(payload []byte) {
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(payload)
		_, _ = w.Write([]byte("\n\n"))
	}

	// History replay (bounded).
	terminal := false
	if evs, ok := h.svc.Events(runID); ok {
		for _, ev := range evs {
			raw, err := json.Marshal(ev)
			if err == nil {
				writeSSE(raw)
			}
		}
	}
	if rec, ok := h.svc.Run(runID); ok && rec.IsTerminal() {
		terminal = true
	}
	flusher.Flush()

	// A terminal run has nothing left to stream — close like the gateway
	// itself does (its SSE stream ends at run completion). Non-terminal
	// runs stay open for live fan-out.
	if terminal {
		return
	}

	ctx := r.Context()
	// Heartbeat keeps the stream alive past the server's 30s WriteTimeout
	// (matching the workspace feed's heartbeat cadence).
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case se, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(se.Raw)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": heartbeat\n\n"))
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// StopRun interrupts the run on the Hermes gateway. Stopping an
// already-terminal run is idempotent (the service returns nil without
// calling the gateway), so this returns 200 with the record's ACTUAL
// status — e.g. completed/not_found — instead of a hardcoded "stopping".
func (h *GatewayHandler) StopRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	if err := h.svc.StopRun(r.Context(), runID); err != nil {
		if errors.Is(err, gateway.ErrRunNotFound) {
			writeError(w, http.StatusNotFound, "run_not_found", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "gateway_unavailable", err.Error())
		return
	}
	status := "stopping"
	if rec, ok := h.svc.Run(runID); ok {
		status = rec.Status
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "status": status})
}

// respondApprovalRequest resolves a pending approval on the gateway.
type respondApprovalRequest struct {
	Choice     string `json:"choice"`
	ApprovalID string `json:"approval_id,omitempty"`
}

// RespondApproval forwards an approval choice (once|session|always|deny).
func (h *GatewayHandler) RespondApproval(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	var req respondApprovalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	switch req.Choice {
	case "once", "session", "always", "deny":
	default:
		writeError(w, http.StatusBadRequest, "invalid_approval_choice", "choice must be one of: once, session, always, deny")
		return
	}
	if err := h.svc.RespondApproval(r.Context(), runID, req.ApprovalID, req.Choice); err != nil {
		log.Warn().Err(err).Str("run_id", runID).Msg("gateway approval failed")
		writeError(w, http.StatusBadGateway, "gateway_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "choice": req.Choice, "resolved": true})
}
