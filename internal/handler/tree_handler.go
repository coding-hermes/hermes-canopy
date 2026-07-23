// Package handler provides HTTP handlers for Canopy REST endpoints.
// Each handler group accepts the corresponding service interface.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sync"
)

// TreeHandler wires the tree CRUD HTTP routes to the TreeService interface
// and broadcasts mutations through the SyncEngine.
type TreeHandler struct {
	svc  service.TreeService
	sync sync.SyncEngine
}

// NewTreeHandler returns a handler wired to the given TreeService and SyncEngine.
func NewTreeHandler(svc service.TreeService, se sync.SyncEngine) *TreeHandler {
	return &TreeHandler{svc: svc, sync: se}
}

// Routes mounts the tree endpoints under /trees.
func (h *TreeHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.ListTrees)
	r.Post("/", h.CreateTree)
	r.Get("/{tree_id}", h.GetTree)
	r.Patch("/{tree_id}", h.UpdateTree)
	r.Delete("/{tree_id}", h.DeleteTree)
	return r
}

// --- Request / response helpers ---------------------------------------------

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiErrorBody struct {
	Error apiError `json:"error"`
}

type paginationBody struct {
	NextCursor *uuid.UUID `json:"nextCursor"`
	HasMore    bool       `json:"hasMore"`
	Total      int        `json:"total"`
	Limit      int        `json:"limit"`
}

type listTreesResponse struct {
	Trees      []service.TreeSummary `json:"trees"`
	Pagination paginationBody        `json:"pagination"`
}

// --- Handlers ---------------------------------------------------------------

func (h *TreeHandler) CreateTree(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title       string `json:"title"`
		Description string `json:"description,omitempty"`
		RootMessage *struct {
			Content       string `json:"content"`
			ContentFormat string `json:"contentFormat,omitempty"`
			NodeType      string `json:"nodeType,omitempty"`
		} `json:"rootMessage"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	// Build service params — author will come from JWT context post-auth.
	// For MVP, use a sentinel UUID; auth middleware (BE-07) will wire the
	// real user.
	authorID := uuid.Nil

	params := service.CreateTreeParams{
		Title:       req.Title,
		Description: req.Description,
		OwnerID:     authorID,
	}
	if req.RootMessage != nil {
		params.RootContent = req.RootMessage.Content
		params.ContentFormat = service.ContentFormat(req.RootMessage.ContentFormat)
		params.NodeType = service.NodeType(req.RootMessage.NodeType)
	}

	out, err := h.svc.CreateTree(r.Context(), params)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	// Broadcast mutation through sync engine (best-effort).
	if h.sync != nil {
		_ = h.sync.OnTreeMutation(r.Context(), sync.TreeMutation{
			Type: sync.MutTreeCreated, TreeID: out.ID,
		})
	}
	w.Header().Set("Location", "/trees/"+out.ID.String())
	writeJSON(w, http.StatusCreated, out)
}

func (h *TreeHandler) ListTrees(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	params := service.ListTreesParams{
		Sort:   service.TreeSortOrder(q.Get("sort")),
		Status: service.TreeStatusFilter(q.Get("status")),
		Search: q.Get("search"),
	}
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, 400, "INVALID_LIMIT", "limit must be an integer")
			return
		}
		params.Limit = v
	}
	if raw := q.Get("cursor"); raw != "" {
		v, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, 400, "INVALID_CURSOR", "cursor must be a valid UUID")
			return
		}
		params.Cursor = &v
	}

	out, err := h.svc.ListTrees(r.Context(), params)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	resp := listTreesResponse{
		Trees: out.Trees,
		Pagination: paginationBody{
			NextCursor: out.NextCursor,
			HasMore:    out.HasMore,
			Total:      out.Total,
			Limit:      out.Limit,
		},
	}
	writeJSON(w, 200, resp)
}

func (h *TreeHandler) GetTree(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTreeID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	opts := service.GetTreeOptions{
		IncludeStats: q.Get("include_stats") != "false",
	}
	out, err := h.svc.GetTree(r.Context(), id, opts)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (h *TreeHandler) UpdateTree(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	var req struct {
		Title       *string `json:"title,omitempty"`
		Description *string `json:"description,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	out, err := h.svc.UpdateTree(r.Context(), id, req.Title, req.Description)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (h *TreeHandler) DeleteTree(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTreeID(w, r)
	if !ok {
		return
	}
	if _, err := h.svc.DeleteTree(r.Context(), id); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Internal helpers -------------------------------------------------------

func parseTreeID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "tree_id"))
	if err != nil {
		writeError(w, 400, "INVALID_TREE_ID", "tree_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

func decodeJSON(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiErrorBody{Error: apiError{Code: code, Message: message}})
}

func (h *TreeHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrTitleRequired),
		errors.Is(err, service.ErrTitleTooLong),
		errors.Is(err, service.ErrDescriptionTooLong),
		errors.Is(err, service.ErrRootContentRequired),
		errors.Is(err, service.ErrRootContentTooLarge),
		errors.Is(err, service.ErrInvalidContentFormat),
		errors.Is(err, service.ErrInvalidNodeType),
		errors.Is(err, service.ErrInvalidCursor),
		errors.Is(err, service.ErrInvalidSort),
		errors.Is(err, service.ErrInvalidStatus),
		errors.Is(err, service.ErrSearchTooShort):
		writeError(w, 400, "VALIDATION_ERROR", err.Error())
	case errors.Is(err, service.ErrTreeNotFound):
		writeError(w, 404, "TREE_NOT_FOUND", "tree not found")
	case errors.Is(err, service.ErrTreeDeleted):
		writeError(w, 410, "TREE_DELETED", "tree has been deleted")
	default:
		log.Ctx(r.Context()).Error().Err(err).Msg("tree request failed")
		writeError(w, 500, "INTERNAL_ERROR", "internal server error")
	}
}
