package handler

import (
	"errors"
	"net/http"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type PluginHandler struct{ svc service.PluginRegistryService }

func NewPluginHandler(s service.PluginRegistryService) *PluginHandler { return &PluginHandler{svc: s} }
func (h *PluginHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.Register)
	r.Get("/", h.List)
	r.Get("/{name}/versions", h.Versions)
	r.Post("/{name}/activate", h.Activate)
	r.Post("/{name}/disable", h.Disable)
	r.Post("/{name}/archive", h.Archive)
	r.Get("/{id}", h.Get)
	return r
}

type pluginRegisterRequest struct {
	SourceJS string `json:"source_js"`
}
type pluginActivateRequest struct {
	Version string `json:"version"`
}

func (h *PluginHandler) Register(w http.ResponseWriter, r *http.Request) {
	var q pluginRegisterRequest
	if decodeJSON(r, &q) != nil || q.SourceJS == "" {
		writeError(w, 400, "INVALID_MANIFEST", "source_js must contain a valid plugin manifest")
		return
	}
	p, e := h.svc.Register(r.Context(), q.SourceJS, UserIDFromContext(r.Context()))
	if e != nil {
		h.fail(w, e)
		return
	}
	writeJSON(w, 201, p)
}
func (h *PluginHandler) List(w http.ResponseWriter, r *http.Request) {
	p, e := h.svc.ListActive(r.Context())
	if e != nil {
		h.fail(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"plugins": p})
}
func (h *PluginHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil {
		writeError(w, 400, "INVALID_PLUGIN_ID", "id must be a UUID")
		return
	}
	p, e := h.svc.GetByID(r.Context(), id)
	if e != nil {
		h.fail(w, e)
		return
	}
	writeJSON(w, 200, p)
}
func (h *PluginHandler) Versions(w http.ResponseWriter, r *http.Request) {
	p, e := h.svc.Versions(r.Context(), chi.URLParam(r, "name"))
	if e != nil {
		h.fail(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"plugins": p})
}
func (h *PluginHandler) Activate(w http.ResponseWriter, r *http.Request) {
	var q pluginActivateRequest
	if decodeJSON(r, &q) != nil || q.Version == "" {
		writeError(w, 400, "INVALID_MANIFEST", "version is required")
		return
	}
	p, e := h.svc.Activate(r.Context(), chi.URLParam(r, "name"), q.Version, UserIDFromContext(r.Context()))
	if e != nil {
		h.fail(w, e)
		return
	}
	writeJSON(w, 200, p)
}
func (h *PluginHandler) Disable(w http.ResponseWriter, r *http.Request) {
	p, e := h.svc.Disable(r.Context(), chi.URLParam(r, "name"), UserIDFromContext(r.Context()))
	if e != nil {
		h.fail(w, e)
		return
	}
	writeJSON(w, 200, p)
}
func (h *PluginHandler) Archive(w http.ResponseWriter, r *http.Request) {
	p, e := h.svc.Archive(r.Context(), chi.URLParam(r, "name"), UserIDFromContext(r.Context()))
	if e != nil {
		h.fail(w, e)
		return
	}
	writeJSON(w, 200, p)
}
func (h *PluginHandler) fail(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, service.ErrInvalidPluginManifest):
		writeError(w, 400, "INVALID_MANIFEST", e.Error())
	case errors.Is(e, service.ErrPluginConflict), errors.Is(e, db.ErrPluginDuplicate):
		writeError(w, 409, "VERSION_CONFLICT", e.Error())
	case errors.Is(e, service.ErrPluginRegistryMissing), errors.Is(e, db.ErrPluginNotFound):
		writeError(w, 404, "PLUGIN_NOT_FOUND", "plugin not found")
	default:
		writeError(w, 500, "INTERNAL_ERROR", "internal server error")
	}
}
