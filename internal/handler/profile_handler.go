package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/hermes"
)

// ProfileHandler wires workspace profile-management routes to a ProfileRouter.
type ProfileHandler struct {
	router   hermes.ProfileRouter
	profiles db.ProfileRepo
}

// NewProfileHandler returns a handler wired to the given ProfileRouter.
func NewProfileHandler(router hermes.ProfileRouter) *ProfileHandler {
	return &ProfileHandler{router: router}
}

// WithProfileRepo returns the handler with the Canopy profiles repository
// wired, enabling the workspace-independent POST /api/v1/profiles surface
// (RVW2-CANOPY-104: the MLS create-group creator_profile_id discovery path).
// Without it, only the workspace-scoped mapping routes are mounted.
func (h *ProfileHandler) WithProfileRepo(profiles db.ProfileRepo) *ProfileHandler {
	h.profiles = profiles
	return h
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

// UnscopedRoutes mounts the workspace-independent profile routes under /api/v1
// (SPEC-API-06 §12). Only POST /profiles is implemented; the GET list is the
// next surface and is intentionally not mounted here yet.
func (h *ProfileHandler) UnscopedRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.CreateProfile)
	return r
}

func (h *ProfileHandler) ListProfiles(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	profiles, err := h.router.ListProfiles(r.Context(), workspaceID)
	if err != nil {
		h.writeRouterError(w, r, workspaceID, err, "list profiles")
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
		writeError(w, http.StatusBadRequest, "INVALID_BODY", invalidBodyMessage(err))
		return
	}

	if err := h.router.SetActiveProfile(r.Context(), workspaceID, req.ProfileName, req.ProfileToken); err != nil {
		h.writeRouterError(w, r, workspaceID, err, "set active profile")
		return
	}

	mapping, err := h.router.GetActiveProfile(r.Context(), workspaceID)
	if err != nil {
		h.writeRouterError(w, r, workspaceID, err, "read active profile after update")
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
		h.writeRouterError(w, r, workspaceID, err, "get active profile")
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
		h.writeRouterError(w, r, workspaceID, err, "remove profile")
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

// profileDocument is a Canopy profiles row as an API document (SPEC-API-06
// §12.1 item shape). snake_case keys, with id first: the field MLS
// create-group consumes as creator_profile_id.
type profileDocument struct {
	ID                uuid.UUID       `json:"id"`
	OwnerID           uuid.UUID       `json:"owner_id"`
	ProfileType       string          `json:"profile_type"`
	Name              string          `json:"name"`
	DisplayName       string          `json:"display_name"`
	Description       *string         `json:"description,omitempty"`
	ConfigJSON        json.RawMessage `json:"config_json,omitempty"`
	CanAutoRespond    bool            `json:"can_auto_respond"`
	ContextWindowSize int             `json:"context_window_size"`
	IsPublic          bool            `json:"is_public"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

func profileDocumentFrom(p db.Profile) profileDocument {
	configJSON := p.ConfigJSON
	if len(configJSON) == 0 {
		configJSON = json.RawMessage(`{}`)
	}
	return profileDocument{
		ID:                p.ID,
		OwnerID:           p.OwnerID,
		ProfileType:       p.ProfileType,
		Name:              p.Name,
		DisplayName:       p.DisplayName,
		Description:       p.Description,
		ConfigJSON:        configJSON,
		CanAutoRespond:    p.CanAutoRespond,
		ContextWindowSize: p.ContextWindowSize,
		IsPublic:          p.IsPublic,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
	}
}

// invalidBodyMessage converts a decodeJSON error into a client-facing
// INVALID_BODY message. Unknown-field errors name the offending field
// (the AC: "clear 'unknown field X' error, not 'must be valid JSON'");
// malformed JSON keeps the generic validity message.
func invalidBodyMessage(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "unknown field") {
		return "request body contains an " + strings.TrimPrefix(msg, "json: ")
	}
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return "request body has an invalid value for field " + ute.Field
	}
	return "request body must be valid JSON"
}

func (h *ProfileHandler) writeRouterError(w http.ResponseWriter, r *http.Request, workspaceID uuid.UUID, err error, operation string) {
	switch {
	case errors.Is(err, hermes.ErrProfileNameRequired), errors.Is(err, hermes.ErrTokenEmpty):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	case errors.Is(err, hermes.ErrNoProfileMapping):
		writeError(w, http.StatusNotFound, "PROFILE_NOT_FOUND", "profile mapping not found")
	case errors.Is(err, hermes.ErrWorkspaceNotFound):
		// A workspace id supplied by the client that does not exist is a
		// 404, not a 500: without this branch the pgx FK violation
		// (fk_profile_route_workspace) fell through to INTERNAL_ERROR.
		// The message names the id and the fix; no SQL text is echoed.
		writeError(w, http.StatusNotFound, "WORKSPACE_NOT_FOUND",
			fmt.Sprintf("workspace %s does not exist; create it before setting a profile", workspaceID))
	default:
		log.Ctx(r.Context()).Error().Err(err).Str("operation", strings.TrimSpace(operation)).Msg("profile request failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
	}
}

// --- Unscoped profile surface (SPEC-API-06 §12.2) ----------------------------
//
// POST /api/v1/profiles creates a Canopy profiles row owned by the
// authenticated user and returns its id. That id is the value space the MLS
// surface consumes as creator_profile_id (SPEC-FTR-03 §API: "every
// creator_profile_id is a Canopy profiles.id") — without this endpoint a brand
// new user had no HTTP path to obtain their first profile id (MLS create-group
// 404s on an unknown profile) and had to read the database directly.

// profileNameRe is the SPEC-API-06 §12.2 name charset: alphanumeric + hyphens.
// Length bounds are re-checked here so the error can name the cause before the
// DB CHECK constraints reject with an opaque constraint violation.
var profileNameRe = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

// profileContextWindowBounds mirrors the profiles CHK constraint
// (chk_profiles_context_window): 1024..2097152.
const (
	profileContextWindowMin = 1024
	profileContextWindowMax = 2097152
)

// createProfileRequest is the POST /api/v1/profiles request body. token is
// REQUIRED and consumed by the hermes-package gateway routing path (below):
// both fields are part of the task row's contract ({name, token}), and the
// token keeps the mapping table usable for the created profile without a
// second user round-trip.
type createProfileRequest struct {
	Name              string          `json:"name"`
	Token             string          `json:"token"`
	DisplayName       string          `json:"display_name,omitempty"`
	Description       string          `json:"description,omitempty"`
	CanAutoRespond    bool            `json:"can_auto_respond,omitempty"`
	ContextWindowSize int             `json:"context_window_size,omitempty"`
	IsPublic          bool            `json:"is_public,omitempty"`
	ConfigJSON        json.RawMessage `json:"config_json,omitempty"`
}

// validateProfileCreate applies the SPEC-API-06 §12.2 validation table.
// It returns an HTTP status, error code and message for the first failure.
func validateProfileCreate(req createProfileRequest) (int, string, string) {
	if req.Name == "" {
		return http.StatusBadRequest, "INVALID_PROFILE_NAME", "name is required"
	}
	if len(req.Name) > 64 {
		return http.StatusBadRequest, "INVALID_PROFILE_NAME", "name must be at most 64 characters"
	}
	if !profileNameRe.MatchString(req.Name) {
		return http.StatusBadRequest, "INVALID_PROFILE_NAME", "name may contain only alphanumeric characters and hyphens"
	}
	if req.Token == "" {
		return http.StatusBadRequest, "VALIDATION_ERROR", "token is required"
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Name
	}
	if len(req.DisplayName) > 200 {
		return http.StatusBadRequest, "INVALID_DISPLAY_NAME", "display_name must be at most 200 characters"
	}
	if req.ContextWindowSize != 0 && (req.ContextWindowSize < profileContextWindowMin || req.ContextWindowSize > profileContextWindowMax) {
		return http.StatusBadRequest, "INVALID_CONTEXT_WINDOW",
			fmt.Sprintf("context_window_size must be between %d and %d", profileContextWindowMin, profileContextWindowMax)
	}
	return 0, "", ""
}

// CreateProfile handles POST /api/v1/profiles.
// Status 201 with the created profile document (id first — the field callers
// feed into MLS create-group as creator_profile_id). Errors are
// SPEC-API-06 §12.2 codes in the standard envelope.
func (h *ProfileHandler) CreateProfile(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "Authorization Bearer token required")
		return
	}
	if h.profiles == nil {
		writeError(w, http.StatusServiceUnavailable, "PROFILE_SURFACE_UNAVAILABLE",
			"profile store is not wired on this server")
		return
	}

	// Pre-check the owner row exists so a bad JWT subject is 403
	// USER_NOT_PROVISIONED (SPEC-API-06 §12.2 owner-required) instead of an
	// opaque FK-violation 500. Non-NotFound repo errors fall through to the
	// insert, whose FK failure then surfaces as the generic 500.
	if getter, ok := h.profiles.(interface {
		GetByOwner(context.Context, uuid.UUID) ([]db.Profile, error)
	}); ok {
		if _, err := getter.GetByOwner(r.Context(), userID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusForbidden, "USER_NOT_PROVISIONED",
					"token subject has no user account on this server; ask the operator to provision it")
				return
			}
			if !errors.Is(err, context.Canceled) {
				log.Ctx(r.Context()).Warn().Err(err).Msg("profile owner pre-check failed")
			}
		}
	}

	var req createProfileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", invalidBodyMessage(err))
		return
	}
	if status, code, msg := validateProfileCreate(req); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Name
	}

	var description *string
	if req.Description != "" {
		description = &req.Description
	}
	configJSON := req.ConfigJSON
	if len(configJSON) == 0 {
		configJSON = json.RawMessage(`{}`)
	}

	created, err := h.profiles.Create(r.Context(), &db.Profile{
		OwnerID:           userID,
		ProfileType:       db.ProfileTypeHermesProfile,
		Name:              req.Name,
		DisplayName:       displayName,
		Description:       description,
		ConfigJSON:        configJSON,
		CanAutoRespond:    req.CanAutoRespond,
		ContextWindowSize: req.ContextWindowSize,
		IsPublic:          req.IsPublic,
	})
	if err != nil {
		h.writeCreateProfileError(w, r, req.Name, err)
		return
	}

	// Token is validated (non-empty) but intentionally NOT persisted here:
	// profile_route (the encrypted-token store) requires a workspace_id
	// (NOT NULL + fk_profile_route_workspace), and token binding to a
	// workspace is exactly what POST /api/v1/workspaces/{workspace_id}/profiles
	// does. MLS create-group needs only the returned id; gateway-token
	// activation stays on the workspace mapping surface (documented in
	// docs/API.md § Profile creation).

	writeJSON(w, http.StatusCreated, profileDocumentFrom(*created))
}

// writeCreateProfileError maps profile-repo Create failures onto
// SPEC-API-07 envelopes. pgx 23505 (uq_profiles_owner_name) → 409
// DUPLICATE_PROFILE_NAME; anything else is a generic 500.
func (h *ProfileHandler) writeCreateProfileError(w http.ResponseWriter, r *http.Request, name string, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation (uq_profiles_owner_name)
		writeError(w, http.StatusConflict, "DUPLICATE_PROFILE_NAME",
			fmt.Sprintf("profile %q already exists for this user", name))
		return
	}
	log.Ctx(r.Context()).Error().Err(err).Str("profile_name", name).Msg("create profile failed")
	writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
}
