// Package handler provides HTTP handlers for Canopy REST endpoints.
// GraphHandler implements BE-16 (Graph Endpoints) as stub 501 responses
// until the full implementation is wired in a dedicated worker tick.
package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// GraphHandler wires the graph-level query HTTP routes to the GraphService
// interface. These endpoints handle aggregate graph operations beyond
// single-node CRUD — subtree traversal, ancestry, and stats.
// Spec: ARCHITECTURE.md §3, SPEC-DM-01.
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
func (h *GraphHandler) GetSubtree(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "GetSubtree")
}

// GetAncestors returns the path from the given node to the tree root.
func (h *GraphHandler) GetAncestors(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "GetAncestors")
}

// GetGraphStats returns aggregate graph statistics for a tree.
func (h *GraphHandler) GetGraphStats(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "GetGraphStats")
}
