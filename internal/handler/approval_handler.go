// Package handler provides HTTP handlers for Canopy approval REST endpoints.
// SPEC-API-05 §§3–8 — exact route contracts.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// ApprovalHandler wires the approval REST routes to the ApprovalService.
type ApprovalHandler struct {
	svc service.ApprovalService
}

// NewApprovalHandler returns a handler wired to the given ApprovalService.
func NewApprovalHandler(svc service.ApprovalService) *ApprovalHandler {
	return &ApprovalHandler{svc: svc}
}

// Routes mounts the approval endpoints under /approvals.
func (h *ApprovalHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/pending", h.ListPending)
	r.Get("/history", h.ListHistory)
	r.Get("/{approval_id}", h.GetApproval)
	r.Post("/{approval_id}/approve", h.Approve)
	r.Post("/{approval_id}/deny", h.Deny)
	return r
}

// --- GET /approvals/pending -----------------------------------------------

func (h *ApprovalHandler) ListPending(w http.ResponseWriter, r *http.Request) {
	ownerID := extractActorID(r)
	treeIDStr := r.URL.Query().Get("tree_id")
	var treeID *uuid.UUID
	if treeIDStr != "" {
		parsed, err := uuid.Parse(treeIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_TREE_ID", "tree_id is not a valid UUID: "+treeIDStr)
			return
		}
		treeID = &parsed
	}

	limit, offset := parsePagination(r)
	approvals, total, err := h.svc.GetPending(r.Context(), ownerID, treeID, limit, offset)
	if err != nil {
		log.Error().Err(err).Str("owner_id", ownerID.String()).Msg("approval handler: list pending")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list pending approvals")
		return
	}
	if approvals == nil {
		approvals = []db.Approval{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"approvals": approvals,
		"total":     total,
		"limit":     limit,
		"offset":    offset,
	})
}

// --- GET /approvals/{approval_id} -----------------------------------------

func (h *ApprovalHandler) GetApproval(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "approval_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_APPROVAL_ID", "not a valid UUID: "+chi.URLParam(r, "approval_id"))
		return
	}

	appr, err := h.svc.GetApproval(r.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrApprovalNotFound) {
			writeError(w, http.StatusNotFound, "APPROVAL_NOT_FOUND", "no approval with id "+id.String())
			return
		}
		log.Error().Err(err).Str("approval_id", id.String()).Msg("approval handler: get approval")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get approval")
		return
	}
	writeJSON(w, http.StatusOK, appr)
}

// --- POST /approvals/{approval_id}/approve --------------------------------

func (h *ApprovalHandler) Approve(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "approval_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_APPROVAL_ID", "not a valid UUID: "+chi.URLParam(r, "approval_id"))
		return
	}
	actorID := extractActorID(r)

	updated, err := h.svc.Approve(r.Context(), id, actorID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrApprovalNotFound):
			writeError(w, http.StatusNotFound, "APPROVAL_NOT_FOUND", "no approval with id "+id.String())
		case errors.Is(err, service.ErrAlreadyDecided):
			writeError(w, http.StatusConflict, "APPROVAL_ALREADY_DECIDED", "approval "+id.String()+" is already decided")
		case errors.Is(err, service.ErrApprovalExpired):
			writeError(w, http.StatusGone, "APPROVAL_EXPIRED", "approval "+id.String()+" has expired")
		default:
			log.Error().Err(err).Str("approval_id", id.String()).Msg("approval handler: approve")
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to approve")
		}
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// --- POST /approvals/{approval_id}/deny -----------------------------------

func (h *ApprovalHandler) Deny(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "approval_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_APPROVAL_ID", "not a valid UUID: "+chi.URLParam(r, "approval_id"))
		return
	}
	actorID := extractActorID(r)

	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if body.Reason == "" {
		writeError(w, http.StatusBadRequest, "REASON_REQUIRED", "deny reason is required")
		return
	}
	if len(body.Reason) > 1000 {
		writeError(w, http.StatusBadRequest, "REASON_TOO_LONG", "deny reason exceeds 1000 characters")
		return
	}

	updated, err := h.svc.Deny(r.Context(), id, actorID, body.Reason)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrApprovalNotFound):
			writeError(w, http.StatusNotFound, "APPROVAL_NOT_FOUND", "no approval with id "+id.String())
		case errors.Is(err, service.ErrAlreadyDecided):
			writeError(w, http.StatusConflict, "APPROVAL_ALREADY_DECIDED", "approval "+id.String()+" is already decided")
		case errors.Is(err, service.ErrApprovalExpired):
			writeError(w, http.StatusGone, "APPROVAL_EXPIRED", "approval "+id.String()+" has expired")
		default:
			log.Error().Err(err).Str("approval_id", id.String()).Msg("approval handler: deny")
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to deny")
		}
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// --- GET /approvals/history -----------------------------------------------

func (h *ApprovalHandler) ListHistory(w http.ResponseWriter, r *http.Request) {
	approvalIDStr := r.URL.Query().Get("approval_id")
	treeIDStr := r.URL.Query().Get("tree_id")

	if approvalIDStr == "" && treeIDStr == "" {
		writeError(w, http.StatusBadRequest, "FILTER_REQUIRED", "provide approval_id or tree_id query parameter")
		return
	}

	var approvalID *uuid.UUID
	if approvalIDStr != "" {
		parsed, err := uuid.Parse(approvalIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_APPROVAL_ID", "not a valid UUID: "+approvalIDStr)
			return
		}
		approvalID = &parsed
	}

	var treeID *uuid.UUID
	if treeIDStr != "" {
		parsed, err := uuid.Parse(treeIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_TREE_ID", "not a valid UUID: "+treeIDStr)
			return
		}
		treeID = &parsed
	}

	limit, offset := parsePagination(r)
	entries, err := h.svc.ListHistory(r.Context(), approvalID, treeID, limit, offset)
	if err != nil {
		log.Error().Err(err).Msg("approval handler: list history")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list history")
		return
	}
	if entries == nil {
		entries = []db.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"limit":   limit,
		"offset":  offset,
	})
}

// --- Helpers ---------------------------------------------------------------

// extractActorID is a stub. In production the authenticated user's ID
// comes from JWT claims stored in the request context.
func extractActorID(r *http.Request) uuid.UUID {
	if uid, ok := r.Context().Value("actor_id").(uuid.UUID); ok {
		return uid
	}
	return uuid.Nil
}

// parsePagination extracts limit/offset from query params with defaults.
func parsePagination(r *http.Request) (limit, offset int) {
	limit = 50
	offset = 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return
}
