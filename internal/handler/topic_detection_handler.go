// Package handler — topic detection HTTP handlers.
// Implements SPEC-TM-02 §8.2 endpoints:
//   POST /v1/topic-proposals/{proposal_id}/confirm
//   POST /v1/topic-proposals/{proposal_id}/dismiss
//   GET  /trees/{tree_id}/topic-detection
//   PUT  /trees/{tree_id}/topic-detection
//
// Routes are tree-scoped where applicable and membership-gated upstream.
// Error responses follow the spec §9 error catalog.
package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// TopicDetectionHandler wires the topic-detection HTTP routes.
type TopicDetectionHandler struct {
	svc service.TopicService
}

// NewTopicDetectionHandler returns a handler wired to the TopicService.
func NewTopicDetectionHandler(svc service.TopicService) *TopicDetectionHandler {
	return &TopicDetectionHandler{svc: svc}
}

// TreeRoutes mounts the tree-scoped detection endpoints (config GET/PUT).
// These are mounted under /trees/{tree_id}/topic-detection by the server.
func (h *TopicDetectionHandler) TreeRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.GetDetectionConfig)
	r.Put("/", h.UpdateDetectionConfig)
	return r
}

// ProposalRoutes mounts the proposal-scoped endpoints (confirm/dismiss).
// These are NOT tree-scoped — they use the proposal ID directly.
func (h *TopicDetectionHandler) ProposalRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/{proposal_id}/confirm", h.ConfirmProposal)
	r.Post("/{proposal_id}/dismiss", h.DismissProposal)
	return r
}

// ── Request/response types ──────────────────────────────────────────────

type confirmProposalRequest struct {
	TitleOverride string `json:"titleOverride"`
}

type detectionConfigResponse struct {
	AutoCreate          bool   `json:"auto_create"`
	AlwaysAsk           bool   `json:"always_ask"`
	DetectionLevel      string `json:"detection_level"`
	MinMessagesPerTopic int    `json:"min_messages_per_topic"`
	ProposalCooldown    int    `json:"proposal_cooldown"`
}

type updateDetectionConfigRequest struct {
	AutoCreate          *bool   `json:"auto_create,omitempty"`
	AlwaysAsk           *bool   `json:"always_ask,omitempty"`
	DetectionLevel      *string `json:"detection_level,omitempty"`
	MinMessagesPerTopic *int    `json:"min_messages_per_topic,omitempty"`
	ProposalCooldown    *int    `json:"proposal_cooldown,omitempty"`
}

// ── Confirm proposal ────────────────────────────────────────────────────

// ConfirmProposal accepts a pending proposal and creates a topic.
// POST /v1/topic-proposals/{proposal_id}/confirm
func (h *TopicDetectionHandler) ConfirmProposal(w http.ResponseWriter, r *http.Request) {
	proposalID, ok := parseProposalID(w, r)
	if !ok {
		return
	}

	var req confirmProposalRequest
	// Body is optional — empty body means accept with generated title.
	if r.Body != nil && r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}
	}

	// Validate title override if provided.
	if req.TitleOverride != "" {
		if len([]rune(req.TitleOverride)) > 200 {
			writeError(w, http.StatusBadRequest, "TOPIC_PROPOSAL_TITLE_TOO_LONG",
				"Topic proposal title must be 1-200 characters")
			return
		}
	}

	topic, err := h.svc.ConfirmProposal(r.Context(), proposalID, req.TitleOverride)
	if err != nil {
		writeDetectionError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":    topic.ID,
		"title": topic.Title,
		"slug":  topic.Slug,
	})
}

// ── Dismiss proposal ────────────────────────────────────────────────────

// DismissProposal rejects a pending proposal.
// POST /v1/topic-proposals/{proposal_id}/dismiss
func (h *TopicDetectionHandler) DismissProposal(w http.ResponseWriter, r *http.Request) {
	proposalID, ok := parseProposalID(w, r)
	if !ok {
		return
	}

	if err := h.svc.DismissProposal(r.Context(), proposalID); err != nil {
		writeDetectionError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Get detection config ────────────────────────────────────────────────

// GetDetectionConfig returns the per-tree detection configuration.
// GET /trees/{tree_id}/topic-detection
func (h *TopicDetectionHandler) GetDetectionConfig(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	cfg, err := h.svc.GetDetectionConfig(r.Context(), treeID)
	if err != nil {
		writeDetectionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, detectionConfigResponse{
		AutoCreate:          cfg.AutoCreate,
		AlwaysAsk:           cfg.AlwaysAsk,
		DetectionLevel:      cfg.DetectionLevel,
		MinMessagesPerTopic: cfg.MinMessagesPerTopic,
		ProposalCooldown:    cfg.ProposalCooldown,
	})
}

// ── Update detection config ─────────────────────────────────────────────

// UpdateDetectionConfig updates the per-tree detection configuration.
// PUT /trees/{tree_id}/topic-detection
func (h *TopicDetectionHandler) UpdateDetectionConfig(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	var req updateDetectionConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	// Start from current config, apply overrides.
	cfg, err := h.svc.GetDetectionConfig(r.Context(), treeID)
	if err != nil {
		writeDetectionError(w, err)
		return
	}
	if req.AutoCreate != nil {
		cfg.AutoCreate = *req.AutoCreate
	}
	if req.AlwaysAsk != nil {
		cfg.AlwaysAsk = *req.AlwaysAsk
	}
	if req.DetectionLevel != nil {
		cfg.DetectionLevel = *req.DetectionLevel
	}
	if req.MinMessagesPerTopic != nil {
		cfg.MinMessagesPerTopic = *req.MinMessagesPerTopic
	}
	if req.ProposalCooldown != nil {
		cfg.ProposalCooldown = *req.ProposalCooldown
	}

	updated, err := h.svc.UpdateDetectionConfig(r.Context(), treeID, cfg)
	if err != nil {
		writeDetectionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, detectionConfigResponse{
		AutoCreate:          updated.AutoCreate,
		AlwaysAsk:           updated.AlwaysAsk,
		DetectionLevel:      updated.DetectionLevel,
		MinMessagesPerTopic: updated.MinMessagesPerTopic,
		ProposalCooldown:    updated.ProposalCooldown,
	})
}

// ── Error mapping (SPEC-TM-02 §9) ───────────────────────────────────────

// writeDetectionError maps service-level detection errors to HTTP responses.
func writeDetectionError(w http.ResponseWriter, err error) {
	log.Debug().Err(err).Msg("detection error")
	switch {
	case errors.Is(err, service.ErrProposalNotFound):
		writeError(w, http.StatusNotFound, "TOPIC_PROPOSAL_NOT_FOUND", "Topic proposal not found")
	case errors.Is(err, service.ErrProposalExpired):
		writeError(w, http.StatusConflict, "TOPIC_PROPOSAL_EXPIRED", "Topic proposal has expired")
	case errors.Is(err, service.ErrProposalAlreadyResolved):
		writeError(w, http.StatusConflict, "TOPIC_PROPOSAL_ALREADY_RESOLVED", "Topic proposal is already resolved")
	case errors.Is(err, service.ErrDetectionDisabled):
		writeError(w, http.StatusConflict, "TOPIC_DETECTION_DISABLED", "Topic detection is disabled for this tree")
	case errors.Is(err, service.ErrInvalidDetectionLevel):
		writeError(w, http.StatusBadRequest, "TOPIC_DETECTION_INVALID_LEVEL", "Detection level must be off, explicit_only, or full")
	case errors.Is(err, service.ErrInvalidDetectionConfig):
		writeError(w, http.StatusBadRequest, "TOPIC_DETECTION_INVALID_CONFIG", "Invalid topic detection configuration")
	case errors.Is(err, service.ErrSubjectCooldown):
		writeError(w, http.StatusConflict, "TOPIC_DETECTION_COOLDOWN", "Topic detection is cooling down for this subject")
	case errors.Is(err, service.ErrProposalRateLimited):
		writeError(w, http.StatusTooManyRequests, "TOPIC_DETECTION_RATE_LIMITED", "Topic proposal rate limit reached")
	case errors.Is(err, service.ErrProposalTitleRequired):
		writeError(w, http.StatusBadRequest, "TOPIC_PROPOSAL_TITLE_REQUIRED", "Topic proposal title is required")
	case errors.Is(err, service.ErrProposalTitleTooLong):
		writeError(w, http.StatusBadRequest, "TOPIC_PROPOSAL_TITLE_TOO_LONG", "Topic proposal title must be 1-200 characters")
	case errors.Is(err, service.ErrProposalRootInvalid):
		writeError(w, http.StatusBadRequest, "TOPIC_PROPOSAL_ROOT_INVALID", "Topic proposal root node is invalid")
	case errors.Is(err, service.ErrProposalDuplicate):
		writeError(w, http.StatusConflict, "TOPIC_PROPOSAL_DUPLICATE", "An existing topic already covers this node")
	default:
		writeError(w, http.StatusInternalServerError, "TOPIC_PROPOSAL_CREATE_FAILED", "Unable to create topic from proposal")
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────

// parseProposalID reads and validates the {proposal_id} chi URL parameter.
func parseProposalID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "proposal_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PROPOSAL_ID", "proposal_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}
