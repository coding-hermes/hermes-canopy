// Package handler provides HTTP handlers for Canopy REST endpoints.
// GraphHandler implements BE-16 (Graph Endpoints) with real CRUD against
// the existing NodeRepo and EdgeRepo backing the conversation DAG.
// Spec: ARCHITECTURE.md §3, SPEC-DM-01.
package handler

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// GraphHandler wires the graph-level query HTTP routes to the GraphService
// interface. These endpoints handle aggregate graph operations beyond
// single-node CRUD — subtree traversal, ancestry, and stats.
type GraphHandler struct {
	svc service.GraphService
}

// NewGraphHandler returns a handler wired to the given GraphService.
func NewGraphHandler(svc service.GraphService) *GraphHandler {
	return &GraphHandler{svc: svc}
}

// Routes mounts the graph endpoints under /graph.
//
//	GET    /trees/{tree_id}/subtree/{node_id}  — get subtree rooted at node
//	GET    /trees/{tree_id}/ancestors/{node_id} — get ancestors of node
//	GET    /trees/{tree_id}/stats              — aggregate graph stats
func (h *GraphHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/trees/{tree_id}/subtree/{node_id}", h.GetSubtree)
	r.Get("/trees/{tree_id}/ancestors/{node_id}", h.GetAncestors)
	r.Get("/trees/{tree_id}/stats", h.GetGraphStats)
	return r
}

// GetSubtree returns the subtree rooted at the given node.
// Query params: ?max_depth=N (default 0 = unbounded).
func (h *GraphHandler) GetSubtree(w http.ResponseWriter, r *http.Request) {
	_, ok := parseTreeID(w, r)
	if !ok {
		return
	}
	nodeID, ok := parseNodeID(w, r)
	if !ok {
		return
	}

	maxDepth := 0
	if d := r.URL.Query().Get("max_depth"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed >= 0 {
			maxDepth = parsed
		}
	}

	result, err := h.svc.GetSubtree(r.Context(), nodeID, maxDepth)
	if err != nil {
		if err == service.ErrNodeNotFound {
			writeError(w, http.StatusNotFound, "NODE_NOT_FOUND", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "SUBTREE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GetAncestors returns the path from the given node to the tree root.
func (h *GraphHandler) GetAncestors(w http.ResponseWriter, r *http.Request) {
	_, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	nodeID, ok := parseNodeID(w, r)
	if !ok {
		return
	}

	result, err := h.svc.GetAncestors(r.Context(), nodeID)
	if err != nil {
		if err == service.ErrNodeNotFound {
			writeError(w, http.StatusNotFound, "NODE_NOT_FOUND", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "ANCESTORS_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GetGraphStats returns aggregate graph statistics for a tree.
func (h *GraphHandler) GetGraphStats(w http.ResponseWriter, r *http.Request) {
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	stats, err := h.svc.GetGraphStats(r.Context(), treeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GRAPH_STATS_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
