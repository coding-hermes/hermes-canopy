package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/hermes"
)

// ProfileHandler wires workspace profile-management routes to a ProfileRouter.
type ProfileHandler struct {
	router hermes.ProfileRouter
}

// NewProfileHandler returns a handler wired to the given ProfileRouter.
func NewProfileHandler(router hermes.ProfileRouter) *ProfileHandler {
	return &ProfileHandler{router: router}
}

// Routes mounts profile-management endpoints under a workspace profile path.
func (h *ProfileHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.ListProfiles)
	r.Post("/", h.SetActiveProfile)
	r.Get("/active", h.GetActiveProfile)
	r.Delete("/{profile_name}", h.RemoveProfile)
	return r
}

func (h *ProfileHandler) ListProfiles(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	profiles, err := h.router.ListProfiles(r.Context(), workspaceID)
	if err != nil {
		h.writeRouterError(w, r, err, "list profiles")
		return
	}
	responseProfiles := make([]profileMappingResponse, 0, len(profiles))
	for _, profile := range profiles {
		responseProfiles = append(responseProfiles, profileMappingResponseFrom(profile))
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": responseProfiles})
}

func (h *ProfileHandler) SetActiveProfile(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	var req struct {
		ProfileName     string `json:"profile_name"`
		ProfileToken    string `json:"profile_token"`
		DisplayName     string `json:"display_name,omitempty"`
		ModelPreference string `json:"model_preference,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	if err := h.router.SetActiveProfile(r.Context(), workspaceID, req.ProfileName, req.ProfileToken); err != nil {
		h.writeRouterError(w, r, err, "set active profile")
		return
	}

	mapping, err := h.router.GetActiveProfile(r.Context(), workspaceID)
	if err != nil {
		h.writeRouterError(w, r, err, "read active profile after update")
		return
	}
	writeJSON(w, http.StatusOK, profileMappingResponseFrom(*mapping))
}

func (h *ProfileHandler) GetActiveProfile(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	mapping, err := h.router.GetActiveProfile(r.Context(), workspaceID)
	if err != nil {
		h.writeRouterError(w, r, err, "get active profile")
		return
	}
	writeJSON(w, http.StatusOK, profileMappingResponseFrom(*mapping))
}

func (h *ProfileHandler) RemoveProfile(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	profileName, err := url.PathUnescape(chi.URLParam(r, "profile_name"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PROFILE_NAME", "profile_name must be valid URL encoding")
		return
	}
	if err := h.router.RemoveProfile(r.Context(), workspaceID, profileName); err != nil {
		h.writeRouterError(w, r, err, "remove profile")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type profileMappingResponse struct {
	WorkspaceID     uuid.UUID `json:"workspaceId"`
	ProfileName     string    `json:"profileName"`
	DisplayName     string    `json:"displayName"`
	IsActive        bool      `json:"isActive"`
	ModelPreference string    `json:"modelPreference,omitempty"`
	MappedAt        string    `json:"mappedAt"`
	LastUsedAt      string    `json:"lastUsedAt"`
}

func profileMappingResponseFrom(mapping hermes.ProfileMapping) profileMappingResponse {
	return profileMappingResponse{
		WorkspaceID:     mapping.WorkspaceID,
		ProfileName:     mapping.ProfileName,
		DisplayName:     mapping.DisplayName,
		IsActive:        mapping.IsActive,
		ModelPreference: mapping.ModelPreference,
		MappedAt:        mapping.MappedAt.Format(time.RFC3339Nano),
		LastUsedAt:      mapping.LastUsedAt.Format(time.RFC3339Nano),
	}
}

func (h *ProfileHandler) writeRouterError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	switch {
	case errors.Is(err, hermes.ErrProfileNameRequired), errors.Is(err, hermes.ErrTokenEmpty):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	case errors.Is(err, hermes.ErrNoProfileMapping):
		writeError(w, http.StatusNotFound, "PROFILE_NOT_FOUND", "profile mapping not found")
	default:
		log.Ctx(r.Context()).Error().Err(err).Str("operation", strings.TrimSpace(operation)).Msg("profile request failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
	}
}
