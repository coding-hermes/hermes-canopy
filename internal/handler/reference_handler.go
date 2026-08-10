// Package handler — reference resolution HTTP handler.
// Implements SPEC-TM-04 §6 endpoints (tree-scoped, membership-gated):
//
//	GET  /trees/{tree_id}/references/autocomplete
//	POST /trees/{tree_id}/references/resolve
//	POST /trees/{tree_id}/references/inject
//
// SSE events reference_resolved / reference_not_found / references_too_many
// are emitted by the send-time path (node_handler), not by these endpoints.
// Error responses follow spec §10.1 error catalog.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/reference"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/search"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
)

// ReferenceHandler wires the reference resolution HTTP routes.
type ReferenceHandler struct {
	svc    reference.ReferenceService
	sseHub sse.SSEHub
}

// NewReferenceHandler returns a handler wired to the reference service + SSE hub.
func NewReferenceHandler(svc reference.ReferenceService, hub sse.SSEHub) *ReferenceHandler {
	return &ReferenceHandler{svc: svc, sseHub: hub}
}

// Routes mounts the reference endpoints. These are mounted under
// /trees/{tree_id} by the server.
func (h *ReferenceHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/references/autocomplete", h.Autocomplete)
	r.Post("/references/resolve", h.Resolve)
	r.Post("/references/inject", h.Inject)
	return r
}

// ── GET /trees/{tree_id}/references/autocomplete (spec §6.1) ──────────────

type autocompleteResponse struct {
	Results []reference.ReferenceAutocompleteResult `json:"results"`
}

func (h *ReferenceHandler) Autocomplete(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	prefix := r.URL.Query().Get("prefix")
	if len(prefix) == 0 {
		writeErrorWithParam(w, http.StatusBadRequest, "REFERENCE_PREFIX_TOO_SHORT",
			"Autocomplete prefix must be at least 1 character", "prefix")
		return
	}
	if len(prefix) > 100 {
		writeErrorWithParam(w, http.StatusBadRequest, "REFERENCE_PREFIX_TOO_LONG",
			"Autocomplete prefix must be at most 100 characters", "prefix")
		return
	}

	include := r.URL.Query().Get("include")
	if include == "" {
		include = "active"
	}
	if include != "active" && include != "archived" && include != "all" {
		writeErrorWithParam(w, http.StatusBadRequest, "REFERENCE_INVALID_INCLUDE",
			"Include must be one of: active, archived, all", "include")
		return
	}

	limit := parseIntParam(r.URL.Query().Get("limit"), 10)

	results, err := h.svc.Autocomplete(r.Context(), reference.ReferenceAutocompleteRequest{
		TreeID:  treeID,
		Prefix:  prefix,
		Limit:   limit,
		Include: include,
	})
	if err != nil {
		writeReferenceError(w, err)
		return
	}

	// Ensure non-nil slice so empty results marshal as [] not null.
	if results == nil {
		results = []reference.ReferenceAutocompleteResult{}
	}
	writeJSON(w, http.StatusOK, autocompleteResponse{Results: results})
}

// ── POST /trees/{tree_id}/references/resolve (spec §6.2) ──────────────────

type resolveRequest struct {
	Content     string `json:"content"`
	MaxNodes    int    `json:"max_nodes"`
	WithContext bool   `json:"with_context"`
}

func (h *ReferenceHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	var req resolveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	if len(req.Content) > 50000 {
		writeErrorWithParam(w, http.StatusBadRequest, "REFERENCE_CONTENT_TOO_LONG",
			"Message content is too long for reference parsing", "content")
		return
	}

	result, err := h.svc.ResolveReferences(r.Context(), reference.ResolveReferencesRequest{
		TreeID:      treeID,
		Content:     req.Content,
		MaxNodes:    req.MaxNodes,
		WithContext: req.WithContext,
	})
	if err != nil {
		writeReferenceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// ── POST /trees/{tree_id}/references/inject (spec §6.3) ───────────────────

type injectRefsRequest struct {
	TopicIDs   []uuid.UUID `json:"topic_ids"`
	References []string    `json:"references"`
	MaxNodes   int         `json:"max_nodes"`
}

type injectRefsResponse struct {
	Context  search.MultiTopicContext    `json:"context"`
	EventID  string                      `json:"event_id"`
	NotFound []reference.ParsedReference `json:"not_found,omitempty"`
	TooMany  bool                        `json:"too_many"`
	Warning  string                      `json:"warning,omitempty"`
}

func (h *ReferenceHandler) Inject(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	var req injectRefsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	if len(req.TopicIDs) == 0 && len(req.References) == 0 {
		writeError(w, http.StatusBadRequest, "REFERENCES_INVALID_INPUT",
			"At least one topicId or reference must be provided")
		return
	}

	userID := UserIDFromContext(r.Context())

	merged, err := h.svc.InjectWithReferences(r.Context(), treeID, reference.InjectWithReferencesRequest{
		TopicIDs:   req.TopicIDs,
		References: req.References,
		MaxNodes:   req.MaxNodes,
	}, userID)
	if err != nil {
		writeReferenceError(w, err)
		return
	}

	// Broadcast context_injected SSE events — one per topic (spec §7.4).
	var lastEventID string
	for i, tc := range merged.Topics {
		eventName := "context_injected:" + strconv.Itoa(i)
		lastEventID = h.broadcastSSE(r, treeID, eventName, map[string]any{
			"topic_id":             tc.TopicID,
			"node_count":           len(tc.Nodes),
			"context_hash":         tc.ContextHash,
			"total_nodes_in_scope": tc.TotalNodes,
			"trigger":              "reference",
		})
	}

	writeJSON(w, http.StatusOK, injectRefsResponse{
		Context: *merged,
		EventID: lastEventID,
	})
}

// ── Send-time SSE broadcast helpers (called by node_handler) ───────────────

// BroadcastReferenceResolved emits a reference_resolved SSE event.
func (h *ReferenceHandler) BroadcastReferenceResolved(r *http.Request, treeID, nodeID uuid.UUID, ref reference.ParsedReference, topic reference.TopicSummary, contextHash string) {
	if h.sseHub == nil {
		return
	}
	h.broadcastSSE(r, treeID, "reference_resolved", map[string]any{
		"nodeId":      nodeID,
		"treeId":      treeID,
		"reference":   ref,
		"topicId":     topic.ID,
		"slug":        topic.Slug,
		"title":       topic.Title,
		"nodeCount":   topic.NodeCount,
		"contextHash": contextHash,
	})
}

// BroadcastReferenceNotFound emits a reference_not_found SSE event.
func (h *ReferenceHandler) BroadcastReferenceNotFound(r *http.Request, treeID, nodeID uuid.UUID, ref reference.ParsedReference) {
	if h.sseHub == nil {
		return
	}
	h.broadcastSSE(r, treeID, "reference_not_found", map[string]any{
		"nodeId":    nodeID,
		"treeId":    treeID,
		"reference": ref,
	})
}

// BroadcastReferencesTooMany emits a references_too_many SSE event.
func (h *ReferenceHandler) BroadcastReferencesTooMany(r *http.Request, treeID, nodeID uuid.UUID, count int, warning string) {
	if h.sseHub == nil {
		return
	}
	h.broadcastSSE(r, treeID, "references_too_many", map[string]any{
		"nodeId":  nodeID,
		"treeId":  treeID,
		"count":   count,
		"softCap": reference.SoftCap,
		"hardCap": reference.HardCap,
		"warning": warning,
	})
}

// ── Error mapping (spec §10.1) ────────────────────────────────────────────

func writeReferenceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, reference.ErrPrefixTooShort):
		writeErrorWithParam(w, http.StatusBadRequest, "REFERENCE_PREFIX_TOO_SHORT", err.Error(), "prefix")
	case errors.Is(err, reference.ErrPrefixTooLong):
		writeErrorWithParam(w, http.StatusBadRequest, "REFERENCE_PREFIX_TOO_LONG", err.Error(), "prefix")
	case errors.Is(err, reference.ErrInvalidInclude):
		writeErrorWithParam(w, http.StatusBadRequest, "REFERENCE_INVALID_INCLUDE", err.Error(), "include")
	case errors.Is(err, reference.ErrReferencesTooMany):
		writeError(w, http.StatusBadRequest, "REFERENCES_TOO_MANY", err.Error())
	case errors.Is(err, reference.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "REFERENCES_INVALID_INPUT", err.Error())
	case errors.Is(err, reference.ErrTooManyTopics):
		writeError(w, http.StatusBadRequest, "REFERENCES_TOO_MANY_TOPICS", err.Error())
	case errors.Is(err, reference.ErrContentTooLong):
		writeErrorWithParam(w, http.StatusBadRequest, "REFERENCE_CONTENT_TOO_LONG", err.Error(), "content")
	case errors.Is(err, reference.ErrInjectionFailed):
		log.Error().Err(err).Msg("reference handler: injection failed")
		writeError(w, http.StatusInternalServerError, "REFERENCE_INJECTION_FAILED", err.Error())
	default:
		log.Error().Err(err).Msg("reference handler: unexpected error")
		writeError(w, http.StatusInternalServerError, "REFERENCE_RESOLUTION_FAILED", "Failed to resolve references; please try again")
	}
}

// ── SSE broadcast helper ──────────────────────────────────────────────────

func (h *ReferenceHandler) broadcastSSE(r *http.Request, treeID uuid.UUID, eventType string, data map[string]any) string {
	if h.sseHub == nil {
		return ""
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	userID := UserIDFromContext(r.Context())
	event := h.sseHub.Broadcast(treeID, sse.SSEEvent{
		Type:    eventType,
		Data:    payload,
		TreeID:  treeID,
		ActorID: userID,
	})
	return event.ID
}
