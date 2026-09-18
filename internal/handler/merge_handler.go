// Package handler — merge (synthesis node) endpoint (SPEC-API-04 §3).
//
//	POST /api/v1/trees/{tree_id}/merge — create a synthesis node (201)
//
// The route is registered with the same auth + tree-membership middleware as
// the other tree-scoped surfaces (§14.1), and before the /trees mount so chi
// resolves the exact pattern first. Request and response field names are
// snake_case on this boundary (§3.2, §3.6, §3.7) — the §9 HTTP boundary rule
// the multi-reference surface also follows.
//
// This endpoint is the ONLY way to create a `node_type='synthesis'` node: the
// ordinary node-create path rejects it (ErrSynthesisViaMergeOnly), which is
// why the merge service writes the node itself.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// MergeHandler wires the merge endpoint to the merge service.
type MergeHandler struct {
	svc service.MergeService
}

// NewMergeHandler returns a handler over the merge service. A nil service is
// safe at construction time (route-parity wiring walks the router with nil
// deps) and answers 503 at request time.
func NewMergeHandler(svc service.MergeService) *MergeHandler {
	return &MergeHandler{svc: svc}
}

// CreateMerge handles POST /trees/{tree_id}/merge (§3). It validates the body,
// creates one synthesis node plus one parent edge and N synthesis edges
// atomically, and answers 201 with the §3.6 envelope.
func (h *MergeHandler) CreateMerge(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "merge service unavailable")
		return
	}

	var req service.CreateMergeRequest
	if err := decodeJSON(r, &req); err != nil {
		if isMergeSourceIDsShape(err) {
			writeMergeCatalogError(w, service.ErrMergeSourceIDsInvalid, "")
			return
		}
		writeError(w, http.StatusBadRequest, "INVALID_BODY", invalidNodeBodyMessage(err))
		return
	}

	result, err := h.svc.CreateMerge(service.WithRequester(r.Context(), userID), treeID, req)
	if err != nil {
		writeMergeServiceError(w, r, err)
		return
	}

	w.Header().Set("Location", "/api/v1/trees/"+treeID.String()+"/nodes/"+result.Node.ID.String())
	writeJSON(w, http.StatusCreated, result)
}

// isMergeSourceIDsShape reports whether a decode failure is the §3.3
// "source_node_ids is not an array" row. encoding/json reports a member whose
// JSON type cannot fit the Go field as an UnmarshalTypeError naming that
// field, which is precisely the case the catalog gives its own code: the
// array shape is wrong, so INVALID_SOURCE_NODE_IDS is a better answer than
// the generic INVALID_BODY.
//
// The two shapes are deliberately split: a member that is not an array (or an
// array holding non-string members) is INVALID_SOURCE_NODE_IDS here, while a
// well-formed string member that is not a UUIDv7 is INVALID_SOURCE_NODE_ID
// from service.CreateMergeRequest.ToInput.
func isMergeSourceIDsShape(err error) bool {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		return false
	}
	return strings.EqualFold(typeErr.Field, "source_node_ids")
}

// writeMergeCatalogError emits the §3.3 identity of a sentinel without a
// service round trip (request-shape rejections the catalog covers).
func writeMergeCatalogError(w http.ResponseWriter, sentinel error, format string, args ...any) {
	apiErr, ok := service.MergeErrorFrom(service.NewMergeAPIError(sentinel, format, args...))
	if !ok {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
		return
	}
	writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
}

// writeMergeServiceError maps a merge failure onto the §3.3 catalog: a
// *MergeAPIError already carries its code and status; the remaining service
// sentinels (payload shape, auth, db) are translated here.
func writeMergeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if apiErr, ok := service.MergeErrorFrom(err); ok {
		writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	switch {
	case errors.Is(err, service.ErrMergeMetadataNotObject):
		// §3.2 types metadata as an object; the catalog carries no row for a
		// non-object, so this follows the multi-reference surface's
		// treatment of the same mistake (400 VALIDATION_ERROR).
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	case errors.Is(err, service.ErrNodeAuthorRequired):
		writeError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, service.ErrDatabaseUnavailable):
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", err.Error())
	default:
		log.Ctx(r.Context()).Error().Err(err).Msg("merge request failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
	}
}
