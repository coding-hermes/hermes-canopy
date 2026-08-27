// Package handler provides HTTP handlers and shared utilities for Canopy REST endpoints.
package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

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

// nodeFieldCasing maps camelCase aliases to the snake_case keys used by the
// node request structs (create/reply/fork/update). Tree-create and topics
// accept camelCase (contentFormat/nodeType), while node endpoints historically
// took snake_case only — a client following the tree-create example then hit
// "INVALID_BODY: request body must be valid JSON" on reply even though the
// body was valid JSON with unknown fields (GAP-053).
var nodeFieldCasing = map[string]string{
	"contentFormat": "content_format",
	"nodeType":      "node_type",
	"parentId":      "parent_id",
	"edgeType":      "edge_type",
}

// decodeNodeJSON decodes a node endpoint request body leniently: camelCase
// aliases (contentFormat, nodeType, parentId, edgeType) are accepted wherever
// the snake_case key is absent, then the normalized body is strict-decoded so
// genuinely unknown fields still fail — with the offending field named in the
// error (decodeJSON's DisallowUnknownFields error includes the field name).
func decodeNodeJSON(r *http.Request, v any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for alias, snake := range nodeFieldCasing {
		camel, ok := fields[alias]
		if !ok {
			continue
		}
		if _, exists := fields[snake]; !exists {
			fields[snake] = camel
		}
		delete(fields, alias)
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(normalized))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// invalidNodeBodyMessage converts a decodeNodeJSON error into a client-facing
// INVALID_BODY message. Unknown-field errors name the offending field (the
// AC: "clear 'unknown field contentFormat' error, not 'must be valid JSON'");
// malformed JSON keeps the generic validity message.
func invalidNodeBodyMessage(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "unknown field") {
		return "request body contains an " + strings.TrimPrefix(msg, "json: ")
	}
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return "request body has an invalid value for field " + ute.Field
	}
	return "request body must be valid JSON"
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
