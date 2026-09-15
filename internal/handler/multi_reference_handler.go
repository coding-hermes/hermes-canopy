// Package handler — multi-message reference endpoints (SPEC-PL-06 §9.1,
// §9.2, §9.3, §9.4).
//
//	POST /api/v1/trees/{tree_id}/reference-selections      — preflight (200)
//	POST /api/v1/trees/{tree_id}/multi-reference-replies   — create (201)
//	GET  /api/v1/nodes/{node_id}/reference-context         — read (200)
//
// The two tree-scoped routes are registered with the same auth + tree
// membership middleware as the other tree-scoped surfaces (§14.1 steps 6-7).
// The read route lives on the flat node surface (§6), which has no tree_id
// segment, so it resolves membership from the target node's own tree.
// Request and response field names are snake_case on this boundary (§9).
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// MultiReferenceHandler wires the multi-message reference endpoints to the
// tree (preflight + context read) and node (creation) services.
type MultiReferenceHandler struct {
	treeSvc service.TreeService
	nodeSvc service.NodeService
	// members is optional: when set, the flat-surface read route resolves
	// per-node membership itself (the flat mount carries no tree_id
	// segment, so TreeMembershipMiddleware cannot run there). Nil skips
	// the check and is only used by DB-free wiring harnesses.
	members TreeMemberChecker
}

// NewMultiReferenceHandler returns a handler over the two services.
func NewMultiReferenceHandler(treeSvc service.TreeService, nodeSvc service.NodeService) *MultiReferenceHandler {
	return &MultiReferenceHandler{treeSvc: treeSvc, nodeSvc: nodeSvc}
}

// WithMembership wires the per-node membership checker used by the §9.3
// read route. Safe to call with nil.
func (h *MultiReferenceHandler) WithMembership(checker TreeMemberChecker) *MultiReferenceHandler {
	h.members = checker
	return h
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

// --- §9.3 reference-context read ---------------------------------------------

// GetReferenceContext handles GET /nodes/{node_id}/reference-context (§9.3).
//
// It returns the stored provenance of a multi-reference reply: the sources in
// persisted selection order (labelled R1..RN), the branch span, the token
// accounting and — always as persisted — the creation manifest hash. A node
// that is not a multi-reference reply answers 404
// REFERENCE_CONTEXT_NOT_FOUND, and so does a node that does not exist, so the
// route cannot be used as an existence oracle (§9.3, §9.4).
//
// The flat surface carries no tree_id segment, so tree membership is
// resolved from the target node's own tree once the node is known to exist
// (§9.3 "authorized like a node read").
func (h *MultiReferenceHandler) GetReferenceContext(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := parseNodeID(w, r)
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

	opts, ok := parseReferenceContextOptions(w, r)
	if !ok {
		return
	}

	result, err := h.treeSvc.GetReferenceContext(r.Context(), nodeID, opts)
	if err != nil {
		writeReferenceServiceError(w, r, err)
		return
	}

	if h.members != nil && result.TreeID != uuid.Nil {
		member, err := h.members.IsMember(r.Context(), result.TreeID, userID)
		if err != nil {
			log.Ctx(r.Context()).Error().Err(err).Msg("reference-context membership check failed")
			writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "membership check unavailable")
			return
		}
		if !member {
			writeError(w, http.StatusForbidden, "NOT_TREE_MEMBER", "you are not a member of this tree")
			return
		}
		// Same order as TreeMembershipMiddleware: membership first, then the
		// soft-deleted-tree gate (members of a deleted tree get 410).
		deleted, err := h.members.IsTreeDeleted(r.Context(), result.TreeID)
		if err != nil {
			log.Ctx(r.Context()).Error().Err(err).Msg("reference-context tree-state check failed")
			writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "tree state check unavailable")
			return
		}
		if deleted {
			writeError(w, http.StatusGone, "TREE_DELETED", "tree has been deleted")
			return
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// parseReferenceContextOptions reads the §9.3 query parameters. Defaults:
// include_content=true, verify_hash=true, max_source_tokens=the source
// allocation recorded at creation (the §6.2 per-source ceiling).
//
// It writes the error response and returns ok=false on a malformed value.
// max_source_tokens above the documented 2,048 maximum is CLAMPED, not
// rejected — "maximum 2,048" bounds the allowance rather than the input.
func parseReferenceContextOptions(w http.ResponseWriter, r *http.Request) (service.ReferenceContextOptions, bool) {
	opts := service.ReferenceContextOptions{IncludeContent: true, VerifyHash: true}
	q := r.URL.Query()

	if raw := q.Get("include_content"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_INCLUDE_CONTENT", "include_content must be a boolean")
			return opts, false
		}
		opts.IncludeContent = value
	}
	if raw := q.Get("verify_hash"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_VERIFY_HASH", "verify_hash must be a boolean")
			return opts, false
		}
		opts.VerifyHash = value
	}
	if raw := q.Get("max_source_tokens"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			writeError(w, http.StatusBadRequest, "INVALID_MAX_SOURCE_TOKENS", "max_source_tokens must be a positive integer")
			return opts, false
		}
		if value > service.ReferenceMaxSourceTokens {
			value = service.ReferenceMaxSourceTokens
		}
		opts.MaxSourceTokens = value
	}
	return opts, true
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
