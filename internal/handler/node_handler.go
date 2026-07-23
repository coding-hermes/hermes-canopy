// Package handler provides HTTP handlers for Canopy REST endpoints.
// Each handler group accepts the corresponding service interface.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// NodeHandler wires the node CRUD HTTP routes to the NodeService interface.
type NodeHandler struct {
	svc service.NodeService
}

// NewNodeHandler returns a handler wired to the given NodeService.
func NewNodeHandler(svc service.NodeService) *NodeHandler {
	return &NodeHandler{svc: svc}
}

// Routes mounts the node endpoints.
//   POST   /trees/{tree_id}/nodes              — create node
//   GET    /trees/{tree_id}/nodes/{node_id}     — get node by ID
//   PATCH  /nodes/{node_id}                     — update node
//   DELETE /nodes/{node_id}                     — soft-delete node
//   POST   /nodes/{node_id}/reply               — reply to node
//   POST   /nodes/{node_id}/fork                — fork from node
func (h *NodeHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/{tree_id}/nodes", h.handleCreate)
	r.Get("/{tree_id}/nodes/{node_id}", h.handleGetByID)
	r.Patch("/nodes/{node_id}", h.handleUpdate)
	r.Delete("/nodes/{node_id}", h.handleDelete)
	r.Post("/nodes/{node_id}/reply", h.handleReply)
	r.Post("/nodes/{node_id}/fork", h.handleFork)
	return r
}

// --- Handlers ---------------------------------------------------------------

func (h *NodeHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	treeID, err := uuid.Parse(chi.URLParam(r, "tree_id"))
	if err != nil {
		writeError(w, 400, "INVALID_TREE_ID", "tree_id must be a valid UUID")
		return
	}

	var req struct {
		ParentID      string          `json:"parent_id"`
		Content       string          `json:"content"`
		ContentFormat string          `json:"content_format,omitempty"`
		NodeType      string          `json:"node_type,omitempty"`
		EdgeType      string          `json:"edge_type,omitempty"`
		Metadata      json.RawMessage `json:"metadata,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	// For MVP, use a sentinel UUID; auth middleware (BE-07) will wire the real user.
	authorID := uuid.Nil

	input := service.CreateNodeInput{
		Content:       req.Content,
		ContentFormat: req.ContentFormat,
		NodeType:      req.NodeType,
		EdgeType:      req.EdgeType,
		AuthorID:      authorID,
		TreeID:        treeID,
		Metadata:      req.Metadata,
	}

	// Parse parent_id if provided.
	if req.ParentID != "" {
		pid, err := uuid.Parse(req.ParentID)
		if err != nil {
			writeError(w, 400, "INVALID_PARENT_ID", "parent_id must be a valid UUID")
			return
		}
		input.ParentID = pid
	}

	out, err := h.svc.Create(r.Context(), treeID, input)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/trees/"+treeID.String()+"/nodes/"+out.Node.ID.String())
	writeJSON(w, http.StatusCreated, out)
}

func (h *NodeHandler) handleGetByID(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := parseNodeID(w, r)
	if !ok {
		return
	}

	out, err := h.svc.GetByID(r.Context(), nodeID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (h *NodeHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := parseNodeID(w, r)
	if !ok {
		return
	}

	var req struct {
		Content       *string          `json:"content,omitempty"`
		ContentFormat *string          `json:"content_format,omitempty"`
		Metadata      *json.RawMessage `json:"metadata,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	input := service.UpdateNodeInput{
		Content:       req.Content,
		ContentFormat: req.ContentFormat,
		Metadata:      req.Metadata,
	}

	out, err := h.svc.Update(r.Context(), nodeID, input)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (h *NodeHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := parseNodeID(w, r)
	if !ok {
		return
	}

	out, err := h.svc.SoftDelete(r.Context(), nodeID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (h *NodeHandler) handleReply(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := parseNodeID(w, r)
	if !ok {
		return
	}

	var req struct {
		Content       string          `json:"content"`
		ContentFormat string          `json:"content_format,omitempty"`
		NodeType      string          `json:"node_type,omitempty"`
		Metadata      json.RawMessage `json:"metadata,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	// MVP: sentinel author.
	authorID := uuid.Nil

	input := service.ReplyInput{
		Content:       req.Content,
		ContentFormat: req.ContentFormat,
		NodeType:      req.NodeType,
		AuthorID:      authorID,
		Metadata:      req.Metadata,
	}

	out, err := h.svc.Reply(r.Context(), nodeID, input)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/trees/"+out.Node.TreeID.String()+"/nodes/"+out.Node.ID.String())
	writeJSON(w, http.StatusCreated, out)
}

func (h *NodeHandler) handleFork(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := parseNodeID(w, r)
	if !ok {
		return
	}

	var req struct {
		Content       string          `json:"content"`
		ContentFormat string          `json:"content_format,omitempty"`
		NodeType      string          `json:"node_type,omitempty"`
		Metadata      json.RawMessage `json:"metadata,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	// MVP: sentinel author.
	authorID := uuid.Nil

	input := service.ForkInput{
		Content:       req.Content,
		ContentFormat: req.ContentFormat,
		NodeType:      req.NodeType,
		AuthorID:      authorID,
		Metadata:      req.Metadata,
	}

	out, err := h.svc.Fork(r.Context(), nodeID, input)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/trees/"+out.Node.TreeID.String()+"/nodes/"+out.Node.ID.String())
	writeJSON(w, http.StatusCreated, out)
}

// --- Error mapping ----------------------------------------------------------

func (h *NodeHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrNodeNotFound),
		errors.Is(err, service.ErrParentNotFound):
		writeError(w, 404, "NOT_FOUND", err.Error())
	case errors.Is(err, service.ErrNodeDeleted),
		errors.Is(err, service.ErrNodeAlreadyDeleted):
		writeError(w, 410, "GONE", err.Error())
	case errors.Is(err, service.ErrParentDeleted):
		writeError(w, 409, "CONFLICT", err.Error())
	case errors.Is(err, service.ErrContentTooLong),
		errors.Is(err, service.ErrInvalidContentFormat),
		errors.Is(err, service.ErrInvalidNodeType),
		errors.Is(err, service.ErrSynthesisViaMergeOnly),
		errors.Is(err, service.ErrSystemNodeForbidden),
		errors.Is(err, service.ErrInvalidEdgeType),
		errors.Is(err, service.ErrMetadataTooLarge),
		errors.Is(err, service.ErrForkRequiresChildren),
		errors.Is(err, service.ErrNoUpdateFields):
		writeError(w, 400, "VALIDATION_ERROR", err.Error())
	case errors.Is(err, service.ErrNodeAuthorRequired):
		writeError(w, 403, "FORBIDDEN", err.Error())
	default:
		log.Ctx(r.Context()).Error().Err(err).Msg("node request failed")
		writeError(w, 500, "INTERNAL_ERROR", "internal server error")
	}
}

// --- Helpers ----------------------------------------------------------------

func parseNodeID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "node_id"))
	if err != nil {
		writeError(w, 400, "INVALID_NODE_ID", "node_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}
