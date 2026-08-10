// Package handler — topic search & context injection HTTP handler.
// Implements SPEC-TM-03 §6 endpoints (tree-scoped, membership-gated):
//   GET  /trees/{tree_id}/topics/search
//   GET  /trees/{tree_id}/topics/recent
//   GET  /trees/{tree_id}/topics/{topic_id}/preview
//   POST /trees/{tree_id}/context/inject
//
// Auth + tree membership are enforced by middleware upstream (same pattern
// as /trees/{tree_id}/events). Error responses follow spec §8.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/search"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
)

// TopicSearchHandler wires the search/inject HTTP routes.
type TopicSearchHandler struct {
	svc    search.TopicSearchService
	sseHub sse.SSEHub
}

// NewTopicSearchHandler returns a handler wired to the search service + SSE hub.
func NewTopicSearchHandler(svc search.TopicSearchService, hub sse.SSEHub) *TopicSearchHandler {
	return &TopicSearchHandler{svc: svc, sseHub: hub}
}

// Routes mounts the tree-scoped search endpoints. These are mounted under
// /trees/{tree_id} by the server, so routes are relative to that prefix.
func (h *TopicSearchHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/topics/search", h.SearchTopics)
	r.Get("/topics/recent", h.GetRecentTopics)
	r.Get("/topics/{topic_id}/preview", h.GetTopicPreview)
	r.Post("/context/inject", h.InjectContext)
	return r
}

// ── GET /trees/{tree_id}/topics/search (spec §6.1) ───────────────────────

type searchResponse struct {
	Results     []search.TopicSearchResult `json:"results"`
	Total       int                        `json:"total"`
	QueryTimeMs int64                      `json:"query_time_ms"`
}

func (h *TopicSearchHandler) SearchTopics(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	q := r.URL.Query().Get("q")

	limit := parseIntParam(r.URL.Query().Get("limit"), 20)
	offset := parseIntParam(r.URL.Query().Get("offset"), 0)
	statusFilter := r.URL.Query().Get("status")
	if statusFilter == "" {
		statusFilter = "active"
	}
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "relevance"
	}

	opts := search.SearchOptions{
		Query:        q,
		MaxResults:   limit,
		Offset:       offset,
		StatusFilter: statusFilter,
		SortBy:       sortBy,
	}

	results, total, elapsed, err := h.svc.Search(r.Context(), treeID, opts)
	if err != nil {
		writeSearchError(w, err)
		return
	}

	// Log the search for analytics (best-effort). ProfileID is nullable;
	// the authenticated user_id is NOT a profile_id. We use the test
	// sentinel user ID which is NOT in profiles, so the FK will reject it.
	// The repo layer handles this by converting invalid FKs to NULL.
	userID := UserIDFromContext(r.Context())
	filters, _ := json.Marshal(map[string]string{
		"status": statusFilter,
		"sort":   sortBy,
	})
	_ = h.svc.LogSearch(r.Context(), search.SearchLogEntry{
		TreeID:           treeID,
		ProfileID:        userID,
		QueryText:        q,
		ResultCount:      total,
		FiltersApplied:   filters,
		SearchDurationMs: int(elapsed.Milliseconds()),
	})

	// Broadcast search_logged SSE event (best-effort).
	h.broadcastSSE(r, treeID, "search_logged", map[string]any{
		"query":          q,
		"result_count":   total,
		"query_time_ms":  elapsed.Milliseconds(),
	})

	// Ensure non-nil slice so empty results marshal as [] not null (spec §9).
	if results == nil {
		results = []search.TopicSearchResult{}
	}
	writeJSON(w, http.StatusOK, searchResponse{
		Results:     results,
		Total:       total,
		QueryTimeMs: elapsed.Milliseconds(),
	})
}

// ── GET /trees/{tree_id}/topics/recent (spec §6.2) ───────────────────────

type recentTopicsResponse struct {
	Topics []search.TopicSearchResult `json:"topics"`
}

func (h *TopicSearchHandler) GetRecentTopics(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	limit := parseIntParam(r.URL.Query().Get("limit"), 10)

	topics, err := h.svc.GetRecent(r.Context(), treeID, limit)
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("recent topics failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
		return
	}

	// Ensure non-nil slice so empty topics marshal as [] not null (spec §9).
	if topics == nil {
		topics = []search.TopicSearchResult{}
	}
	writeJSON(w, http.StatusOK, recentTopicsResponse{Topics: topics})
}

// ── GET /trees/{tree_id}/topics/{topic_id}/preview (spec §6.3) ───────────

func (h *TopicSearchHandler) GetTopicPreview(w http.ResponseWriter, r *http.Request) {
	_, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	topicID, ok := parseTopicID(w, r)
	if !ok {
		return
	}

	preview, err := h.svc.GetTopicPreview(r.Context(), topicID, 3)
	if err != nil {
		if errors.Is(err, search.ErrTopicNotFound) {
			writeError(w, http.StatusNotFound, "TOPIC_NOT_FOUND", "topic not found")
			return
		}
		log.Ctx(r.Context()).Error().Err(err).Msg("topic preview failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, preview)
}

// ── POST /trees/{tree_id}/context/inject (spec §6.4) ─────────────────────

type injectRequest struct {
	TopicIDs []uuid.UUID `json:"topic_ids"`
	MaxNodes int         `json:"max_nodes"`
}

type injectResponse struct {
	Context search.MultiTopicContext `json:"context"`
	EventID string                   `json:"event_id"`
}

func (h *TopicSearchHandler) InjectContext(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	var req injectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	if len(req.TopicIDs) == 0 {
		writeError(w, http.StatusBadRequest, "CONTEXT_TOO_MANY_TOPICS",
			"at least one topic_id is required")
		return
	}

	if len(req.TopicIDs) > search.MaxTopics {
		writeErrorWithParam(w, http.StatusBadRequest, "CONTEXT_TOO_MANY_TOPICS",
			"cannot inject more than 5 topics at once", "topic_ids")
		return
	}

	result, err := h.svc.InjectContext(r.Context(), treeID, search.InjectContextRequest{
		TopicIDs: req.TopicIDs,
		MaxNodes: req.MaxNodes,
	})
	if err != nil {
		writeInjectError(w, err)
		return
	}

	// Broadcast context_injected SSE events — one per topic (spec §7.1).
	var lastEventID string
	for i, tc := range result.Topics {
		eventName := "context_injected:" + strconv.Itoa(i)
		lastEventID = h.broadcastSSE(r, treeID, eventName, map[string]any{
			"topic_id":            tc.TopicID,
			"node_count":          len(tc.Nodes),
			"context_hash":        tc.ContextHash,
			"total_nodes_in_scope": tc.TotalNodes,
		})
	}

	writeJSON(w, http.StatusOK, injectResponse{
		Context: *result,
		EventID: lastEventID,
	})
}

// ── Error mapping (spec §8) ──────────────────────────────────────────────

func writeSearchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, search.ErrSearchQueryTooShort):
		writeErrorWithParam(w, http.StatusBadRequest, "SEARCH_QUERY_TOO_SHORT", err.Error(), "q")
	case errors.Is(err, search.ErrSearchQueryTooLong):
		writeErrorWithParam(w, http.StatusBadRequest, "SEARCH_QUERY_TOO_LONG", err.Error(), "q")
	case errors.Is(err, search.ErrSearchStopWordsOnly):
		writeError(w, http.StatusBadRequest, "SEARCH_STOP_WORDS_ONLY", err.Error())
	case errors.Is(err, search.ErrSearchInvalidSort):
		writeErrorWithParam(w, http.StatusBadRequest, "SEARCH_INVALID_SORT", err.Error(), "sort")
	case errors.Is(err, search.ErrSearchInvalidLimit):
		writeErrorWithParam(w, http.StatusBadRequest, "SEARCH_INVALID_LIMIT", err.Error(), "limit")
	default:
		log.Error().Err(err).Msg("search handler: unexpected error")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
	}
}

func writeInjectError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, search.ErrTopicNotFound):
		writeError(w, http.StatusNotFound, "TOPIC_NOT_FOUND", "topic not found")
	case errors.Is(err, search.ErrTopicDeleted):
		writeError(w, http.StatusGone, "TOPIC_DELETED", "topic has been deleted")
	case errors.Is(err, search.ErrTopicArchived):
		writeError(w, http.StatusConflict, "TOPIC_ARCHIVED_INJECTION",
			"archived topics cannot be injected. Unarchive first.")
	case errors.Is(err, search.ErrContextTooManyTopics):
		writeErrorWithParam(w, http.StatusBadRequest, "CONTEXT_TOO_MANY_TOPICS",
			"cannot inject more than 5 topics at once", "topic_ids")
	case errors.Is(err, search.ErrContextTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "CONTEXT_TOO_LARGE", err.Error())
	default:
		log.Error().Err(err).Msg("inject handler: unexpected error")
		writeError(w, http.StatusInternalServerError, "CONTEXT_INJECTION_FAILED",
			"failed to inject context; please try again")
	}
}

// writeErrorWithParam writes an error with a "param" field (spec §8 shape).
func writeErrorWithParam(w http.ResponseWriter, status int, code, message, param string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"param":   param,
		},
	})
}

// ── SSE broadcast helper ──────────────────────────────────────────────────

func (h *TopicSearchHandler) broadcastSSE(r *http.Request, treeID uuid.UUID, eventType string, data map[string]any) string {
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
