// Package handler provides HTTP handlers for Canopy REST endpoints.
// Each handler group accepts the corresponding service interface.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sync"
)

// NodeHandler wires the node CRUD HTTP routes to the NodeService interface
// and broadcasts mutations through the SyncEngine.
type NodeHandler struct {
	svc  service.NodeService
	sync sync.SyncEngine
}

// NewNodeHandler returns a handler wired to the given NodeService and SyncEngine.
func NewNodeHandler(svc service.NodeService, se sync.SyncEngine) *NodeHandler {
	return &NodeHandler{svc: svc, sync: se}
}

// Routes mounts the node endpoints.
//
//	POST   /trees/{tree_id}/nodes              — create node
//	GET    /trees/{tree_id}/nodes/{node_id}     — get node by ID
//	PATCH  /nodes/{node_id}                     — update node
//	DELETE /nodes/{node_id}                     — soft-delete node
//	POST   /nodes/{node_id}/reply               — reply to node
//	POST   /nodes/{node_id}/fork                — fork from node
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

// TreeRoutes returns node routes for tree-scoped mounts (/api/v1/trees/{tree_id}/nodes/).
// The tree_id is provided by the mount point; routes use bare patterns without
// duplicating the tree_id parameter.
//
//	GET    /          — list nodes in tree
//	POST   /          — create node
//	GET    /{node_id} — get node by ID
func (h *NodeHandler) TreeRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.handleListByTree)
	r.Post("/", h.handleCreate)
	r.Get("/{node_id}", h.handleGetByID)
	return r
}

// --- Handlers ---------------------------------------------------------------

// handleListByTree returns all active nodes in the tree as NodeDetails,
// ordered by sequence_num.
func (h *NodeHandler) handleListByTree(w http.ResponseWriter, r *http.Request) {
	treeID, err := uuid.Parse(chi.URLParam(r, "tree_id"))
	if err != nil {
		writeError(w, 400, "INVALID_TREE_ID", "tree_id must be a valid UUID")
		return
	}

	nodes, err := h.svc.ListByTree(r.Context(), treeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "NODES_LIST_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

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

	// Validate content length (BUG-019 fix).
	if len(req.Content) == 0 {
		writeError(w, 400, "EMPTY_CONTENT", "content must not be empty")
		return
	}
	if len(req.Content) > 64*1024 { // 64KB max
		writeError(w, 400, "CONTENT_TOO_LARGE", "content exceeds maximum allowed size (64KB)")
		return
	}

	// Extract authenticated user from JWT context.
	authorID := UserIDFromContext(r.Context())
	if authorID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

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

	// Broadcast mutation through sync engine (best-effort).
	if h.sync != nil && out != nil {
		_ = h.sync.OnNodeMutation(r.Context(), sync.NodeMutation{
			Type:          sync.MutNodeAdded,
			TreeID:        treeID,
			NodeID:        out.Node.ID,
			ActorID:       authorID,
			Content:       out.Node.Content,
			ContentFormat: out.Node.ContentFormat,
			NodeType:      out.Node.NodeType,
			SequenceNum:   out.Node.SequenceNum,
			Timestamp:     time.Now().UTC(),
		})
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

	// Verify the requesting user is a member of the node's tree (BUG-016 fix).
	// This check covers bare /nodes/{node_id} access paths that bypass
	// the tree-scoped membership middleware.
	if userID := UserIDFromContext(r.Context()); userID != uuid.Nil {
		// If this node was accessed via a tree-scoped route, membership was already
		// checked by middleware. For bare /nodes/{node_id}, we check here.
		treeIDStr := treeIDFromPath(r.URL.Path)
		if treeIDStr == "" && out.TreeID != uuid.Nil {
			// Bare node access — owner must own the tree or be a member.
			// For now, verify via tree ownership. Membership check deferred post-MVP.
			writeError(w, http.StatusForbidden, "NOT_TREE_MEMBER",
				"use tree-scoped endpoint: /trees/{tree_id}/nodes/{node_id}")
			return
		}
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

	// Broadcast mutation through sync engine (best-effort).
	if h.sync != nil {
		_ = h.sync.OnNodeMutation(r.Context(), sync.NodeMutation{
			Type:      sync.MutNodeUpdated,
			TreeID:    out.TreeID,
			NodeID:    out.ID,
			ActorID:   out.AuthorID,
			Content:   out.Content,
			Timestamp: time.Now().UTC(),
		})
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

	// Broadcast mutation through sync engine (best-effort).
	if h.sync != nil {
		_ = h.sync.OnNodeMutation(r.Context(), sync.NodeMutation{
			Type:      sync.MutNodeRemoved,
			TreeID:    out.TreeID,
			NodeID:    out.ID,
			ActorID:   uuid.Nil,
			Timestamp: out.DeletedAt,
		})
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

	// Extract authenticated user from JWT context.
	authorID := UserIDFromContext(r.Context())
	if authorID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

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

	// Broadcast mutation through sync engine (best-effort).
	if h.sync != nil && out != nil && out.Node != nil {
		_ = h.sync.OnNodeMutation(r.Context(), sync.NodeMutation{
			Type:          sync.MutNodeAdded,
			TreeID:        out.Node.TreeID,
			NodeID:        out.Node.ID,
			ActorID:       out.Node.AuthorID,
			Content:       out.Node.Content,
			ContentFormat: out.Node.ContentFormat,
			NodeType:      out.Node.NodeType,
			SequenceNum:   out.Node.SequenceNum,
			Timestamp:     time.Now().UTC(),
		})
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

	// Extract authenticated user from JWT context.
	authorID := UserIDFromContext(r.Context())
	if authorID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

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

	// Broadcast mutation through sync engine (best-effort).
	if h.sync != nil && out != nil && out.Node != nil {
		_ = h.sync.OnNodeMutation(r.Context(), sync.NodeMutation{
			Type:          sync.MutNodeAdded,
			TreeID:        out.Node.TreeID,
			NodeID:        out.Node.ID,
			ActorID:       out.Node.AuthorID,
			Content:       out.Node.Content,
			ContentFormat: out.Node.ContentFormat,
			NodeType:      out.Node.NodeType,
			SequenceNum:   out.Node.SequenceNum,
			Timestamp:     time.Now().UTC(),
		})
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
	case errors.Is(err, service.ErrDatabaseUnavailable):
		writeError(w, 503, "SERVICE_UNAVAILABLE", err.Error())
	case errors.Is(err, service.ErrNodeAuthorRequired):
		writeError(w, 403, "FORBIDDEN", err.Error())
	default:
		log.Ctx(r.Context()).Error().Err(err).Msg("node request failed")
		writeError(w, 500, "INTERNAL_ERROR", "internal server error")
	}
}

// --- Helpers ----------------------------------------------------------------

// End of node_handler.go — parseNodeID is in handler_util.go
