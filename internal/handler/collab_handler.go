// Package handler provides HTTP handlers for Canopy REST endpoints.
// CollabHandler wires the SPEC-FTR-01 §5.1/§5.2 collaboration endpoints
// (workspace CRUD, membership, invitations) to the CollaborationService.
package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/collaboration"
)

// CollabHandler wires the collaboration REST routes to the
// CollaborationService.
type CollabHandler struct {
	svc collaboration.CollaborationService
}

// NewCollabHandler returns a handler wired to the given service.
func NewCollabHandler(svc collaboration.CollaborationService) *CollabHandler {
	return &CollabHandler{svc: svc}
}

// Routes mounts the collaboration endpoints under /collab.
// Spec: SPEC-FTR-01 §5.1 (workspace management) + §5.2 (membership &
// invitations).
func (h *CollabHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.ListWorkspaces)
	r.Post("/", h.CreateWorkspace)
	r.Get("/{workspace_id}", h.GetWorkspace)
	r.Patch("/{workspace_id}", h.UpdateWorkspace)
	r.Delete("/{workspace_id}", h.DeleteWorkspace)
	r.Get("/{workspace_id}/members", h.ListMembers)
	r.Patch("/{workspace_id}/members/{user_id}", h.UpdateMemberRole)
	r.Delete("/{workspace_id}/members/{user_id}", h.RemoveMember)
	r.Post("/{workspace_id}/invite", h.GenerateInvitation)
	r.Post("/{workspace_id}/join", h.JoinWorkspace)
	r.Post("/{workspace_id}/leave", h.LeaveWorkspace)
	return r
}

// --- request/response types ----------------------------------------------

type createWorkspaceRequest struct {
	Name string `json:"name"`
}

type updateWorkspaceRequest struct {
	Name        *string    `json:"name"`
	Description *string    `json:"description"`
	TreeID      *uuid.UUID `json:"tree_id"`
	ApprovalTTL *int64     `json:"approval_ttl"` // seconds
}

type updateMemberRoleRequest struct {
	Role int `json:"role"`
}

type createWorkspaceResponse struct {
	WorkspaceID uuid.UUID              `json:"workspace_id"`
	Name        string                 `json:"name"`
	Role        string                 `json:"role"`
	Members     []collaboration.Member `json:"members"`
}

type inviteResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// --- GET /collab ----------------------------------------------------------

// ListWorkspaces returns the caller's workspaces.
func (h *CollabHandler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

	workspaces, err := h.svc.GetUserWorkspaces(r.Context(), userID)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID.String()).Msg("collab handler: list workspaces")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list workspaces")
		return
	}
	if workspaces == nil {
		workspaces = []*collaboration.Workspace{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": workspaces})
}

// --- POST /collab ---------------------------------------------------------

// CreateWorkspace creates a workspace with the caller as admin.
// Response 201 per SPEC-FTR-01 §5.1.
func (h *CollabHandler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

	var req createWorkspaceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	ws, err := h.svc.CreateWorkspace(r.Context(), userID, req.Name)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID.String()).Msg("collab handler: create workspace")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create workspace")
		return
	}

	writeJSON(w, http.StatusCreated, createWorkspaceResponse{
		WorkspaceID: ws.ID,
		Name:        ws.Name,
		Role:        collaboration.RoleAdmin.String(),
		Members:     ws.Members,
	})
}

// --- GET /collab/{workspace_id} -------------------------------------------

// GetWorkspace returns workspace details. Members only (403 otherwise).
func (h *CollabHandler) GetWorkspace(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	ws, err := h.svc.GetWorkspace(r.Context(), workspaceID)
	if err != nil {
		h.writeCollabError(w, err)
		return
	}
	// Only members may read a workspace (SPEC-FTR-01 §5.1).
	if !h.isMember(ws, userID) {
		writeError(w, http.StatusForbidden, "NOT_WORKSPACE_MEMBER", "user is not a member of this workspace")
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

// --- PATCH /collab/{workspace_id} -----------------------------------------

// UpdateWorkspace updates workspace metadata. Admin only.
func (h *CollabHandler) UpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	var req updateWorkspaceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	// Load current workspace to apply partial updates.
	ws, err := h.svc.GetWorkspace(r.Context(), workspaceID)
	if err != nil {
		h.writeCollabError(w, err)
		return
	}
	// Admin check: the caller must be an admin member.
	if !h.isAdmin(r.Context(), workspaceID, userID) {
		writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "admin role required")
		return
	}

	name := ws.Name
	if req.Name != nil {
		name = *req.Name
	}
	description := ""
	if req.Description != nil {
		description = *req.Description
	}
	treeID := ws.TreeID
	if req.TreeID != nil {
		treeID = req.TreeID
	}
	ttl := int64(ws.ApprovalTTL / time.Second)
	if req.ApprovalTTL != nil {
		ttl = *req.ApprovalTTL
	}

	updated, err := h.svc.UpdateWorkspace(r.Context(), workspaceID, name, description, treeID, ttl)
	if err != nil {
		h.writeCollabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// --- DELETE /collab/{workspace_id} ----------------------------------------

// DeleteWorkspace removes a workspace. Admin only.
func (h *CollabHandler) DeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	if !h.isAdmin(r.Context(), workspaceID, userID) {
		writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "admin role required")
		return
	}

	if err := h.svc.DeleteWorkspace(r.Context(), workspaceID); err != nil {
		h.writeCollabError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- GET /collab/{workspace_id}/members -----------------------------------

// ListMembers returns the workspace member list. Members only.
func (h *CollabHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	ws, err := h.svc.GetWorkspace(r.Context(), workspaceID)
	if err != nil {
		h.writeCollabError(w, err)
		return
	}
	// Only members may list members (SPEC-FTR-01 §5.2).
	if !h.isMember(ws, userID) {
		writeError(w, http.StatusForbidden, "NOT_WORKSPACE_MEMBER", "user is not a member of this workspace")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": ws.Members})
}

// --- PATCH /collab/{workspace_id}/members/{user_id} ------------------------

// UpdateMemberRole changes a member's role. Admin only.
func (h *CollabHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "user_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_USER_ID", "user_id must be a valid UUID")
		return
	}

	var req updateMemberRoleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	err = h.svc.UpdateMemberRole(r.Context(), workspaceID, userID, targetID, collaboration.Role(req.Role))
	if err != nil {
		h.writeCollabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- DELETE /collab/{workspace_id}/members/{user_id} -----------------------

// RemoveMember removes a member from the workspace. Admin only.
func (h *CollabHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "user_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_USER_ID", "user_id must be a valid UUID")
		return
	}

	err = h.svc.RemoveMember(r.Context(), workspaceID, userID, targetID)
	if err != nil {
		h.writeCollabError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- POST /collab/{workspace_id}/invite ------------------------------------

// GenerateInvitation creates a one-time invite token. Admin only.
// Response 201 per SPEC-FTR-01 §5.2.
func (h *CollabHandler) GenerateInvitation(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	token, expiresAt, err := h.svc.GenerateInvitation(r.Context(), workspaceID, userID)
	if err != nil {
		h.writeCollabError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, inviteResponse{Token: token, ExpiresAt: expiresAt})
}

// --- POST /collab/{workspace_id}/join --------------------------------------

// JoinWorkspace joins the caller to the workspace via ?token=... .
// Response 200 per SPEC-FTR-01 §5.2.
func (h *CollabHandler) JoinWorkspace(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "INVALID_TOKEN", "token query parameter is required")
		return
	}

	err := h.svc.JoinWorkspace(r.Context(), workspaceID, userID, token)
	if err != nil {
		h.writeCollabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- POST /collab/{workspace_id}/leave -------------------------------------

// LeaveWorkspace removes the caller from the workspace. 204 per
// SPEC-FTR-01 §5.2.
func (h *CollabHandler) LeaveWorkspace(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	err := h.svc.LeaveWorkspace(r.Context(), workspaceID, userID)
	if err != nil {
		h.writeCollabError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---------------------------------------------------------------

// isMember reports whether the user is a member of the workspace.
func (h *CollabHandler) isMember(ws *collaboration.Workspace, userID uuid.UUID) bool {
	for _, m := range ws.Members {
		if m.UserID == userID {
			return true
		}
	}
	return false
}

// isAdmin reports whether the caller is an admin member of the workspace.
// Non-members and non-admins both return false (the caller then receives
// 403 PERMISSION_DENIED).
func (h *CollabHandler) isAdmin(ctx context.Context, workspaceID, userID uuid.UUID) bool {
	ws, err := h.svc.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return false
	}
	for _, m := range ws.Members {
		if m.UserID == userID {
			return m.Role == collaboration.RoleAdmin
		}
	}
	return false
}

// writeCollabError maps collaboration errors to HTTP status codes
// (SPEC-FTR-01 §5 + worker brief): ErrNotFound→404,
// ErrPermissionDenied/ErrNotWorkspaceMember→403, ErrInvalidToken→400,
// ErrDuplicated→409.
func (h *CollabHandler) writeCollabError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, collaboration.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "workspace not found")
	case errors.Is(err, collaboration.ErrPermissionDenied):
		writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "permission denied")
	case errors.Is(err, collaboration.ErrNotWorkspaceMember):
		writeError(w, http.StatusForbidden, "NOT_WORKSPACE_MEMBER", "user is not a member of this workspace")
	case errors.Is(err, collaboration.ErrInvalidToken):
		writeError(w, http.StatusBadRequest, "INVALID_TOKEN", "invitation token is invalid or expired")
	case errors.Is(err, collaboration.ErrDuplicated):
		writeError(w, http.StatusConflict, "DUPLICATED", "resource already exists")
	default:
		log.Error().Err(err).Msg("collab handler: internal error")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error")
	}
}
