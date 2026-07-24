// Package handler provides HTTP handlers for Canopy REST endpoints.
// TopicHandler implements BE-14 (Topics Endpoints) as stub 501 responses
// until the full implementation is wired in a dedicated worker tick.
package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// TopicHandler wires the topic CRUD HTTP routes to the TopicService interface.
// Spec: SPEC-TM-01, SPEC-TM-03, SPEC-TM-05.
type TopicHandler struct {
	svc service.TopicService
}

// NewTopicHandler returns a handler wired to the given TopicService.
func NewTopicHandler(svc service.TopicService) *TopicHandler {
	return &TopicHandler{svc: svc}
}

// Routes mounts the topic endpoints under /topics.
//
//	GET    /              — list topics
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

// ListTopics returns a paginated list of topics for the current tree.
func (h *TopicHandler) ListTopics(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "ListTopics")
}

// CreateTopic creates a new topic.
func (h *TopicHandler) CreateTopic(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "CreateTopic")
}

// GetTopic retrieves a single topic by ID.
func (h *TopicHandler) GetTopic(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "GetTopic")
}

// UpdateTopic updates topic metadata.
func (h *TopicHandler) UpdateTopic(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "UpdateTopic")
}

// ArchiveTopic archives (soft-deletes) a topic.
func (h *TopicHandler) ArchiveTopic(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "ArchiveTopic")
}
