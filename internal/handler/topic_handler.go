// Package handler provides HTTP handlers for Canopy REST endpoints.
// TopicHandler implements BE-14 (Topics Endpoints) with real CRUD.
// Spec: SPEC-TM-01, SPEC-TM-03, SPEC-TM-05.
package handler

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// TopicHandler wires the topic CRUD HTTP routes to the TopicService.
type TopicHandler struct {
	svc service.TopicService
}

// NewTopicHandler returns a handler wired to the given TopicService.
func NewTopicHandler(svc service.TopicService) *TopicHandler {
	return &TopicHandler{svc: svc}
}

// Routes mounts the topic endpoints.
//
//	GET    /              — list topics (query: ?tree_id=&status=&limit=&offset=)
//	POST   /              — create topic
//	GET    /{topic_id}    — get topic by ID
//	PATCH  /{topic_id}    — update topic
//	DELETE /{topic_id}    — archive topic
func (h *TopicHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.ListTopics)
	r.Post("/", h.CreateTopic)
	r.Get("/{topic_id}", h.GetTopic)
	r.Patch("/{topic_id}", h.UpdateTopic)
	r.Delete("/{topic_id}", h.ArchiveTopic)
	return r
}

// topicCreateRequest is the JSON body for creating a topic.
type topicCreateRequest struct {
	TreeID      uuid.UUID `json:"treeId"`
	RootNodeID  uuid.UUID `json:"rootNodeId"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
}

// topicUpdateRequest is the JSON body for updating a topic.
type topicUpdateRequest struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

// topicListResponse wraps a list of topic summaries.
type topicListResponse struct {
	Topics []service.TopicSummary `json:"topics"`
}

// ListTopics returns a paginated list of topics for a tree.
func (h *TopicHandler) ListTopics(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeIDFromQuery(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	limit := parseIntParam(r.URL.Query().Get("limit"), 50)
	offset := parseIntParam(r.URL.Query().Get("offset"), 0)

	topics, err := h.svc.ListTopics(r.Context(), treeID, status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOPIC_LIST_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, topicListResponse{Topics: topics})
}

// CreateTopic creates a new topic.
func (h *TopicHandler) CreateTopic(w http.ResponseWriter, r *http.Request) {
	var req topicCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "MISSING_TITLE", "title is required")
		return
	}

	topic, err := h.svc.CreateTopic(r.Context(), req.TreeID, req.RootNodeID, req.Title, req.Description)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOPIC_CREATE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, topic)
}

// GetTopic retrieves a single topic by ID.
func (h *TopicHandler) GetTopic(w http.ResponseWriter, r *http.Request) {
	topicID, ok := parseTopicID(w, r)
	if !ok {
		return
	}

	topic, err := h.svc.GetTopic(r.Context(), topicID)
	if err != nil {
		writeError(w, http.StatusNotFound, "TOPIC_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, topic)
}

// UpdateTopic updates topic metadata.
func (h *TopicHandler) UpdateTopic(w http.ResponseWriter, r *http.Request) {
	topicID, ok := parseTopicID(w, r)
	if !ok {
		return
	}

	var req topicUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	topic, err := h.svc.UpdateTopic(r.Context(), topicID, req.Title, req.Description, req.Status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOPIC_UPDATE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, topic)
}

// ArchiveTopic archives (soft-deletes) a topic.
func (h *TopicHandler) ArchiveTopic(w http.ResponseWriter, r *http.Request) {
	topicID, ok := parseTopicID(w, r)
	if !ok {
		return
	}

	if err := h.svc.ArchiveTopic(r.Context(), topicID); err != nil {
		writeError(w, http.StatusInternalServerError, "TOPIC_ARCHIVE_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ─────────────────────────────────────────────────────────

// parseTopicID reads and validates the {topic_id} chi URL parameter.
func parseTopicID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "topic_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TOPIC_ID", "topic_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

// parseTreeIDFromQuery reads the tree_id query parameter.
func parseTreeIDFromQuery(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.URL.Query().Get("tree_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "MISSING_TREE_ID", "tree_id query parameter is required")
		return uuid.Nil, false
	}
	return id, true
}

// parseIntParam parses an integer query parameter with a default fallback.
func parseIntParam(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return defaultVal
	}
	return n
}
