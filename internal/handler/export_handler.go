// Package handler provides HTTP handlers for Canopy REST endpoints.
package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// ExportHandler wires the import/export HTTP routes to the ExportService.
type ExportHandler struct {
	svc service.ExportService
}

// NewExportHandler returns a handler wired to the given ExportService.
func NewExportHandler(svc service.ExportService) *ExportHandler {
	return &ExportHandler{svc: svc}
}

// Routes mounts the export endpoints.
func (h *ExportHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{tree_id}/export", h.ExportTree)
	r.Post("/import", h.ImportTree)
	return r
}

// --- GET /trees/{tree_id}/export -------------------------------------------

// ExportTree serialises the tree, all its nodes, and all its edges as JSON.
func (h *ExportHandler) ExportTree(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	// Verify the requesting user owns this tree.
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

	data, err := h.svc.ExportTree(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, data)
}

// --- POST /trees/import ----------------------------------------------------

// ImportTree accepts an export JSON payload and creates a new tree with
// freshly-generated IDs.
func (h *ExportHandler) ImportTree(w http.ResponseWriter, r *http.Request) {
	var req service.ExportData
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

	result, err := h.svc.ImportTree(r.Context(), &req, userID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	w.Header().Set("Location", "/trees/"+result.TreeID.String())
	writeJSON(w, http.StatusCreated, result)
}

// --- Internal helpers -------------------------------------------------------

func (h *ExportHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrExportNotFound):
		writeError(w, http.StatusNotFound, "TREE_NOT_FOUND", "tree not found")
	case errors.Is(err, service.ErrExportInvalidJSON),
		errors.Is(err, service.ErrExportMissingTree),
		errors.Is(err, service.ErrExportMissingRootNode),
		errors.Is(err, service.ErrExportInvalidRootNode),
		errors.Is(err, service.ErrExportEdgeNodeNotFound):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	case errors.Is(err, service.ErrDatabaseUnavailable):
		log.Ctx(r.Context()).Error().Err(err).Str("path", r.URL.Path).Msg("export db error")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
	default:
		log.Ctx(r.Context()).Error().Err(err).Msg("export request failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
	}
}
