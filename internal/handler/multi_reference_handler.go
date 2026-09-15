// Package handler — multi-message reference endpoints (SPEC-PL-06 §9.1,
// §9.2, §9.4).
//
//	POST /api/v1/trees/{tree_id}/reference-selections   — preflight (200)
//	POST /api/v1/trees/{tree_id}/multi-reference-replies — create  (201)
//
// Both are tree-scoped and registered with the same auth + tree-membership
// middleware as the other tree-scoped surfaces (§14.1 steps 6-7). Request
// and response field names are snake_case on this boundary (§9).
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// MultiReferenceHandler wires the multi-message reference endpoints to the
// tree (preflight) and node (creation) services.
type MultiReferenceHandler struct {
	treeSvc service.TreeService
	nodeSvc service.NodeService
}

// NewMultiReferenceHandler returns a handler over the two services.
func NewMultiReferenceHandler(treeSvc service.TreeService, nodeSvc service.NodeService) *MultiReferenceHandler {
	return &MultiReferenceHandler{treeSvc: treeSvc, nodeSvc: nodeSvc}
}

// referenceSelectionRequest is the §9.1 request body. Source ids decode as
// strings so a malformed id can be answered with REFERENCE_SOURCE_INVALID
// (a catalog code) instead of a generic body-shape error.
type referenceSelectionRequest struct {
	SourceNodeIDs        []string `json:"source_node_ids"`
	PrimarySourceID      *string  `json:"primary_source_id"`
	ProfileContextBudget int      `json:"profile_context_budget"`
}

// multiReferenceReplyRequest is the §9.2 request body.
type multiReferenceReplyRequest struct {
	SelectionToken string          `json:"selection_token"`
	Content        string          `json:"content"`
	ContentFormat  string          `json:"content_format"`
	Metadata       json.RawMessage `json:"metadata"`
	RequestID      *string         `json:"request_id"`
}

// ValidateReferenceSelection handles POST /trees/{tree_id}/reference-selections.
// It is a stateless preflight: it validates the ordered selection and returns
// a signed, five-minute selection token plus a preview manifest, writing no
// graph rows (§9.1).
func (h *MultiReferenceHandler) ValidateReferenceSelection(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	if h.treeSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "tree service unavailable")
		return
	}

	var req referenceSelectionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", invalidNodeBodyMessage(err))
		return
	}

	sourceIDs := make([]uuid.UUID, 0, len(req.SourceNodeIDs))
	for _, raw := range req.SourceNodeIDs {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			// §9.4: a source id that is not a usable UUID.
			writeReferenceCatalogError(w, service.ErrReferenceSourceInvalid)
			return
		}
		sourceIDs = append(sourceIDs, id)
	}

	input := service.ReferenceSelectionInput{
		SourceNodeIDs:        sourceIDs,
		ProfileContextBudget: req.ProfileContextBudget,
	}
	if req.PrimarySourceID != nil {
		primary, err := uuid.Parse(*req.PrimarySourceID)
		if err != nil || primary == uuid.Nil {
			writeReferenceCatalogError(w, service.ErrReferenceSourceInvalid)
			return
		}
		input.PrimarySourceID = &primary
	}

	result, err := h.treeSvc.ValidateReferenceSelection(
		service.WithRequester(r.Context(), userID), treeID, input)
	if err != nil {
		writeReferenceServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// CreateMultiReferenceReply handles POST /trees/{tree_id}/multi-reference-replies.
// It creates the message target and its N reference edges atomically and
// answers 201 with the node, the edges, and the reference-context summary
// (§9.2). The caller identity becomes the created node's author.
func (h *MultiReferenceHandler) CreateMultiReferenceReply(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	if h.nodeSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "node service unavailable")
		return
	}

	var req multiReferenceReplyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", invalidNodeBodyMessage(err))
		return
	}

	input := service.CreateMultiReferenceReplyInput{
		SelectionToken: req.SelectionToken,
		Content:        req.Content,
		ContentFormat:  req.ContentFormat,
		Metadata:       req.Metadata,
	}
	if req.RequestID != nil && *req.RequestID != "" {
		requestID, err := uuid.Parse(*req.RequestID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_BODY", "request_id must be a valid UUID")
			return
		}
		input.RequestID = &requestID
	}

	result, err := h.nodeSvc.CreateMultiReferenceReply(
		service.WithRequester(r.Context(), userID), treeID, input)
	if err != nil {
		writeReferenceServiceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/nodes/"+result.Node.ID.String()+"/reference-context")
	writeJSON(w, http.StatusCreated, result)
}

// --- Error mapping (SPEC-PL-06 §9.4) ----------------------------------------

// writeReferenceCatalogError emits the catalog identity of a sentinel without
// a service round trip (request-shape rejections that the catalog covers).
func writeReferenceCatalogError(w http.ResponseWriter, sentinel error) {
	apiErr, ok := service.ReferenceErrorFrom(service.NewReferenceAPIError(sentinel, ""))
	if !ok {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
		return
	}
	writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
}

// writeReferenceServiceError maps a reference operation failure onto the §9.4
// catalog: a *ReferenceAPIError already carries its code and status; the
// remaining service sentinels (auth/payload/db) are translated here.
func writeReferenceServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if apiErr, ok := service.ReferenceErrorFrom(err); ok {
		writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	switch {
	case errors.Is(err, service.ErrNotTreeMember):
		writeError(w, http.StatusForbidden, "NOT_TREE_MEMBER", "you are not a member of this tree")
	case errors.Is(err, service.ErrTreeNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, service.ErrTreeDeleted):
		writeError(w, http.StatusGone, "TREE_DELETED", "tree has been deleted")
	case errors.Is(err, service.ErrContentTooLong),
		errors.Is(err, service.ErrInvalidContentFormat),
		errors.Is(err, service.ErrMetadataTooLarge),
		errors.Is(err, service.ErrReferenceMetadataNotObject),
		errors.Is(err, service.ErrParentNotFound):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	case errors.Is(err, service.ErrNodeAuthorRequired):
		writeError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, service.ErrDatabaseUnavailable),
		errors.Is(err, service.ErrReferenceSelectionUnconfigured):
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", err.Error())
	default:
		log.Ctx(r.Context()).Error().Err(err).Msg("multi-reference request failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
	}
}
