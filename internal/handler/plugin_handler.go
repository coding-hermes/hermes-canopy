// Package handler provides HTTP handlers for Canopy REST endpoints.
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/plugin"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// PluginHandler wires the plugin sandbox HTTP routes (GAP-002 §4.2) to the
// plugin.Service interface: register/list/get/source/install + instance
// lifecycle, all behind auth.
type PluginHandler struct {
	svc plugin.Service
}

// NewPluginHandler returns a handler wired to the given plugin.Service.
func NewPluginHandler(svc plugin.Service) *PluginHandler {
	return &PluginHandler{svc: svc}
}

// Routes mounts the plugin endpoints under /plugins.
//
//	POST /register                    — register a plugin (manifest + source)
//	GET  /instances                   — list caller's instances (?treeId=)
//	POST /instances/{instance_id}/pause
//	POST /instances/{instance_id}/resume
//	GET  /                            — list plugins (?limit=&offset=)
//	GET  /{plugin_id}                 — plugin metadata (no source)
//	GET  /{plugin_id}/source          — raw source + X-Source-SHA256
//	POST /{plugin_id}/install         — install to tree/profile
//
// Static /instances routes are registered before /{plugin_id} so chi never
// treats "instances" as a plugin id.
func (h *PluginHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/register", h.Register)
	r.Get("/instances", h.ListInstances)
	r.Post("/instances/{instance_id}/pause", h.PauseInstance)
	r.Post("/instances/{instance_id}/resume", h.ResumeInstance)
	r.Get("/", h.List)
	r.Get("/{plugin_id}", h.Get)
	r.Get("/{plugin_id}/source", h.GetSource)
	r.Post("/{plugin_id}/install", h.Install)
	return r
}

// registerRequest is the POST /plugins/register body.
type registerRequest struct {
	Source string `json:"source"`
}

// installRequest is the POST /plugins/{id}/install body.
type installRequest struct {
	TreeID             *string  `json:"treeId"`
	GrantedPermissions []string `json:"grantedPermissions"`
}

// --- POST /plugins/register ----------------------------------------------

// Register stores a plugin: manifest-as-comment-block + source JS, with
// SHA-256 integrity and the configured size cap.
func (h *PluginHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	// Parse the manifest first so validation errors map to their exact codes.
	manifest, err := plugin.ParseManifest(req.Source)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

	created, err := h.svc.Register(r.Context(), *manifest, req.Source, userID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]*plugin.Plugin{"plugin": created})
}

// --- GET /plugins ---------------------------------------------------------

// List returns plugins with limit/offset pagination.
func (h *PluginHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := 20
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	plugins, total, err := h.svc.List(r.Context(), limit, offset)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plugins": plugins,
		"total":   total,
	})
}

// --- GET /plugins/{plugin_id} ---------------------------------------------

// Get returns plugin metadata (source is never serialized — json:"-").
func (h *PluginHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePluginID(w, r)
	if !ok {
		return
	}
	p, err := h.svc.Get(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]*plugin.Plugin{"plugin": p})
}

// --- GET /plugins/{plugin_id}/source --------------------------------------

// GetSource serves the raw plugin JS with an X-Source-SHA256 integrity
// header so clients can verify the source they execute.
func (h *PluginHandler) GetSource(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePluginID(w, r)
	if !ok {
		return
	}
	source, sha, err := h.svc.GetSource(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("X-Source-SHA256", sha)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(source))
}

// --- POST /plugins/{plugin_id}/install ------------------------------------

// Install creates a per-tree/per-profile instance with a granted permission
// snapshot (must be a subset of the plugin's declared permissions).
func (h *PluginHandler) Install(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePluginID(w, r)
	if !ok {
		return
	}
	var req installRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	var treeID *uuid.UUID
	if req.TreeID != nil && *req.TreeID != "" {
		parsed, err := uuid.Parse(*req.TreeID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_TREE_ID", "treeId must be a valid UUID")
			return
		}
		treeID = &parsed
	}

	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

	granted := make([]plugin.Permission, 0, len(req.GrantedPermissions))
	for _, p := range req.GrantedPermissions {
		granted = append(granted, plugin.Permission(p))
	}

	inst, err := h.svc.Install(r.Context(), id, treeID, userID, granted)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]*plugin.PluginInstance{"instance": inst})
}

// --- GET /plugins/instances ------------------------------------------------

// ListInstances returns the caller's instances, optionally scoped to a tree.
func (h *PluginHandler) ListInstances(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	var treeID *uuid.UUID
	if v := r.URL.Query().Get("treeId"); v != "" {
		parsed, err := uuid.Parse(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_TREE_ID", "treeId must be a valid UUID")
			return
		}
		treeID = &parsed
	}

	instances, err := h.svc.ListInstances(r.Context(), userID, treeID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": instances})
}

// --- POST /plugins/instances/{instance_id}/pause | /resume -----------------

// PauseInstance pauses an instance.
func (h *PluginHandler) PauseInstance(w http.ResponseWriter, r *http.Request) {
	id, ok := parseInstanceID(w, r)
	if !ok {
		return
	}
	if err := h.svc.PauseInstance(r.Context(), id); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// ResumeInstance resumes an instance.
func (h *PluginHandler) ResumeInstance(w http.ResponseWriter, r *http.Request) {
	id, ok := parseInstanceID(w, r)
	if !ok {
		return
	}
	if err := h.svc.ResumeInstance(r.Context(), id); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "active"})
}

// --- Internal helpers -----------------------------------------------------

// writeServiceError maps the plugin error catalog (GAP-002 §6) to HTTP
// statuses. Unknown errors are 500s with the real error via zerolog
// (BUG-020 pattern — see export_handler.go).
func (h *PluginHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, plugin.ErrInvalidManifest):
		writeError(w, http.StatusBadRequest, "INVALID_MANIFEST", err.Error())
	case errors.Is(err, plugin.ErrManifestValidationFailed):
		writeError(w, http.StatusBadRequest, "MANIFEST_VALIDATION_FAILED", err.Error())
	case errors.Is(err, plugin.ErrInvalidPermission):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_PERMISSION", err.Error())
	case errors.Is(err, plugin.ErrPluginTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "PLUGIN_TOO_LARGE", err.Error())
	case errors.Is(err, plugin.ErrPluginNotFound):
		writeError(w, http.StatusNotFound, "PLUGIN_NOT_FOUND", "plugin not found")
	case errors.Is(err, plugin.ErrPluginDisabled):
		writeError(w, http.StatusGone, "PLUGIN_DISABLED", err.Error())
	case errors.Is(err, plugin.ErrPluginArchived):
		writeError(w, http.StatusGone, "PLUGIN_ARCHIVED", err.Error())
	case errors.Is(err, plugin.ErrAlreadyInstalled):
		writeError(w, http.StatusConflict, "PLUGIN_ALREADY_INSTALLED", err.Error())
	case errors.Is(err, plugin.ErrPermissionNotDeclared):
		writeError(w, http.StatusForbidden, "PERMISSION_NOT_DECLARED", err.Error())
	case errors.Is(err, plugin.ErrInstanceNotFound):
		writeError(w, http.StatusNotFound, "INSTANCE_NOT_FOUND", "plugin instance not found")
	case errors.Is(err, service.ErrDatabaseUnavailable):
		log.Ctx(r.Context()).Error().Err(err).Str("path", r.URL.Path).Msg("plugin db error")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
	default:
		log.Ctx(r.Context()).Error().Err(err).Msg("plugin request failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
	}
}

// parsePluginID reads and validates the {plugin_id} chi URL parameter.
func parsePluginID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "plugin_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PLUGIN_ID", "plugin_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

// parseInstanceID reads and validates the {instance_id} chi URL parameter.
func parseInstanceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "instance_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INSTANCE_ID", "instance_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}
