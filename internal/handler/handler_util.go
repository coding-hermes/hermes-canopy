// Package handler provides HTTP handlers and shared utilities for Canopy REST endpoints.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// --- Error response types ---------------------------------------------------

// apiError is a single error item returned in the response body.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// apiErrorBody wraps an apiError in a consistent JSON envelope.
type apiErrorBody struct {
	Error apiError `json:"error"`
}

// --- JSON helpers -----------------------------------------------------------

// decodeJSON decodes a JSON request body with strict unknown-field rejection.
func decodeJSON(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// writeJSON serialises v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError is a convenience for writing a JSON error response.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiErrorBody{Error: apiError{Code: code, Message: message}})
}

// --- URL parameter helpers --------------------------------------------------

// parseTreeID reads and validates the {tree_id} chi URL parameter.
func parseTreeID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "tree_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TREE_ID", "tree_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

// parseWorkspaceID reads and validates the {workspace_id} chi URL parameter.
func parseWorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "workspace_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WORKSPACE_ID", "workspace_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

// parseNodeID reads and validates the {node_id} chi URL parameter.
func parseNodeID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "node_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_NODE_ID", "node_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}
