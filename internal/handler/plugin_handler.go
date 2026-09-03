package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type PluginHandler struct {
	svc service.PluginRegistryService
	hub sse.SSEHub
}

func NewPluginHandler(s service.PluginRegistryService, hubs ...sse.SSEHub) *PluginHandler {
	h := &PluginHandler{svc: s}
	if len(hubs) > 0 {
		h.hub = hubs[0]
	}
	return h
}
func (h *PluginHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.Register)
	r.Get("/", h.List)
	r.Get("/{name}/versions", h.Versions)
	r.Post("/{name}/activate", h.Activate)
	r.Post("/{name}/update", h.Update)
	r.Post("/{name}/rollback", h.Rollback)
	r.Post("/{name}/disable", h.Disable)
	r.Post("/{name}/archive", h.Archive)
	r.Get("/{id}/source", h.Source)
	r.Get("/{id}", h.Get)
	return r
}

type pluginRegisterRequest struct {
	SourceJS string `json:"source_js"`
}
type pluginActivateRequest struct {
	Version string `json:"version"`
}
type pluginUpdateRequest struct {
	SourceJS       string    `json:"source_js"`
	ActorProfileID uuid.UUID `json:"actor_profile_id"`
}
type pluginRollbackRequest struct {
	TargetVersion  string    `json:"target_version"`
	ActorProfileID uuid.UUID `json:"actor_profile_id"`
}

func (h *PluginHandler) publish(p *db.Plugin, eventType string) {
	if h.hub == nil {
		return
	}
	b, _ := json.Marshal(map[string]any{"plugin_id": p.ID, "slug": p.Slug, "version": p.Version, "source_sha256": p.SourceSHA256})
	h.hub.Broadcast(p.ID, sse.SSEEvent{Type: eventType, Data: b})
}
func (h *PluginHandler) Update(w http.ResponseWriter, r *http.Request) {
	var q pluginUpdateRequest
	if decodeJSON(r, &q) != nil || q.SourceJS == "" || q.ActorProfileID == uuid.Nil {
		writeError(w, 400, "INVALID_MANIFEST", "source_js and actor_profile_id are required")
		return
	}
	p, e := h.svc.Update(r.Context(), chi.URLParam(r, "name"), q.SourceJS, q.ActorProfileID)
	if e != nil {
		if errors.Is(e, service.ErrPluginConflict) || errors.Is(e, db.ErrPluginDuplicate) {
			writeError(w, 409, "PLUGIN_VERSION_EXISTS", e.Error())
			return
		}
		h.fail(w, e)
		return
	}
	h.publish(p, "plugin_updated")
	writeJSON(w, 200, p)
}
func (h *PluginHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	var q pluginRollbackRequest
	if decodeJSON(r, &q) != nil || q.TargetVersion == "" || q.ActorProfileID == uuid.Nil {
		writeError(w, 400, "INVALID_REQUEST", "target_version and actor_profile_id are required")
		return
	}
	p, e := h.svc.Rollback(r.Context(), chi.URLParam(r, "name"), q.TargetVersion, q.ActorProfileID)
	if e != nil {
		if errors.Is(e, service.ErrPluginRegistryMissing) {
			writeError(w, 404, "PLUGIN_VERSION_NOT_FOUND", "plugin version not found")
			return
		}
		h.fail(w, e)
		return
	}
	h.publish(p, "plugin_rolled_back")
	writeJSON(w, 200, p)
}
func (h *PluginHandler) Source(w http.ResponseWriter, r *http.Request) {
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil {
		writeError(w, 400, "INVALID_PLUGIN_ID", "id must be a UUID")
		return
	}
	p, e := h.svc.GetByID(r.Context(), id)
	if e != nil || p.Status != "active" {
		writeError(w, 404, "PLUGIN_NOT_FOUND", "plugin not found")
		return
	}
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(p.SourceJS))
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
