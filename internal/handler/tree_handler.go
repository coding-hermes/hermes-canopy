package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

type TreeHandler struct{ service *service.TreeService }

func NewTreeHandler(s *service.TreeService) *TreeHandler { return &TreeHandler{service: s} }

func (h *TreeHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.ListTrees)
	r.Post("/", h.CreateTree)
	r.Get("/{tree_id}", h.GetTree)
	r.Patch("/{tree_id}", h.UpdateTree)
	r.Delete("/{tree_id}", h.DeleteTree)
	return r
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
type paginationBody struct {
	NextCursor *uuid.UUID `json:"nextCursor"`
	HasMore    bool       `json:"hasMore"`
	Total      int        `json:"total"`
	Limit      int        `json:"limit"`
}
type listBody struct {
	Trees      []db.Tree      `json:"trees"`
	Pagination paginationBody `json:"pagination"`
}

func (h *TreeHandler) CreateTree(w http.ResponseWriter, r *http.Request) {
	var in service.CreateTreeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	out, err := h.service.CreateTree(r.Context(), in)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/trees/"+out.ID.String())
	writeJSON(w, http.StatusCreated, out)
}
func (h *TreeHandler) ListTrees(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := service.ListTreesInput{Sort: q.Get("sort"), Status: q.Get("status"), Search: q.Get("search")}
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, 400, "INVALID_LIMIT", "limit must be an integer")
			return
		}
		in.Limit = v
	}
	if raw := q.Get("cursor"); raw != "" {
		v, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, 400, "INVALID_CURSOR", "cursor must be a valid UUID")
			return
		}
		in.Cursor = &v
	}
	out, err := h.service.ListTrees(r.Context(), in)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, 200, listBody{Trees: out.Trees, Pagination: paginationBody{NextCursor: out.NextCursor, HasMore: out.HasMore, Total: out.Total, Limit: out.Limit}})
}
func (h *TreeHandler) GetTree(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTreeID(w, r)
	if !ok {
		return
	}
	include := r.URL.Query().Get("include_stats") != "false"
	out, err := h.service.GetTree(r.Context(), id, include)
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
	var in service.UpdateTreeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, 400, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	out, err := h.service.UpdateTree(r.Context(), id, in)
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
	if err := h.service.DeleteTree(r.Context(), id); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
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
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}
func (h *TreeHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *service.ValidationError
	switch {
	case errors.As(err, &validation):
		writeError(w, 400, validation.Code, validation.Message)
	case errors.Is(err, db.ErrNotFound):
		writeError(w, 404, "TREE_NOT_FOUND", "tree not found")
	default:
		log.Ctx(r.Context()).Error().Err(err).Msg("tree request failed")
		writeError(w, 500, "INTERNAL_ERROR", "internal server error")
	}
}
