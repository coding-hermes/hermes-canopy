// Package handler provides HTTP handlers for Canopy REST endpoints.
// Each handler group accepts the corresponding service interface.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	stdsync "sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/sync"
)

// TreeHandler wires the tree CRUD HTTP routes to the TreeService interface
// and broadcasts mutations through the SyncEngine.
type TreeHandler struct {
	svc         service.TreeService
	sync        sync.SyncEngine
	users       db.UserRepo
	members     db.TreeMemberRepo
	hub         sse.SSEHub
	presence    *presenceRegistry
}

// NewTreeHandler returns a handler wired to the given TreeService and SyncEngine.
func NewTreeHandler(svc service.TreeService, se sync.SyncEngine) *TreeHandler {
	return &TreeHandler{svc: svc, sync: se, presence: newPresenceRegistry()}
}

// WithShares wires the user/member repos and SSE hub needed for share +
// presence endpoints. Returns the receiver for chaining. When not called,
// ShareTree/Presence handlers return 501 NOT_IMPLEMENTED.
func (h *TreeHandler) WithShares(users db.UserRepo, members db.TreeMemberRepo, hub sse.SSEHub) *TreeHandler {
	h.users = users
	h.members = members
	h.hub = hub
	return h
}

// Routes mounts the tree endpoints under /trees.
func (h *TreeHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.ListTrees)
	r.Post("/", h.CreateTree)
	r.Get("/{tree_id}", h.GetTree)
	r.Patch("/{tree_id}", h.UpdateTree)
	r.Delete("/{tree_id}", h.DeleteTree)
	r.Post("/{tree_id}/share", h.ShareTree)
	r.Post("/{tree_id}/presence", h.PushPresence)
	r.Post("/{tree_id}/presence/leave", h.LeavePresence)
	return r
}

// --- Request / response helpers ---------------------------------------------

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

	// Extract authenticated user from JWT context.
	authorID := UserIDFromContext(r.Context())
	if authorID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

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
		IncludeStats:   q.Get("include_stats") != "false",
		IncludeRelated: true, // WIRE-006: always surface associations when present
	}
	out, err := h.svc.GetTree(r.Context(), id, opts)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	// Verify the requesting user owns this tree (BUG-016 fix).
	userID := UserIDFromContext(r.Context())
	if userID != uuid.Nil && out.OwnerID != uuid.Nil && out.OwnerID != userID {
		writeError(w, http.StatusForbidden, "NOT_TREE_OWNER", "you do not own this tree")
		return
	}
	writeJSON(w, 200, out)
}

func (h *TreeHandler) UpdateTree(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	// Verify the requesting user owns this tree (BUG-016 fix).
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	// Quick ownership pre-check: fetch tree to verify ownership before applying update.
	existing, err := h.svc.GetTree(r.Context(), id, service.GetTreeOptions{IncludeStats: false})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if existing.OwnerID != uuid.Nil && existing.OwnerID != userID {
		writeError(w, http.StatusForbidden, "NOT_TREE_OWNER", "you do not own this tree")
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

	// Broadcast mutation through sync engine (best-effort).
	if h.sync != nil {
		_ = h.sync.OnTreeMutation(r.Context(), sync.TreeMutation{
			Type: sync.MutTreeUpdated, TreeID: id,
		})
	}
	writeJSON(w, 200, out)
}

func (h *TreeHandler) DeleteTree(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	// Verify the requesting user owns this tree (BUG-016 fix).
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	existing, err := h.svc.GetTree(r.Context(), id, service.GetTreeOptions{IncludeStats: false})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if existing.OwnerID != uuid.Nil && existing.OwnerID != userID {
		writeError(w, http.StatusForbidden, "NOT_TREE_OWNER", "you do not own this tree")
		return
	}

	if _, err := h.svc.DeleteTree(r.Context(), id); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Internal helpers -------------------------------------------------------

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
	case errors.Is(err, service.ErrDatabaseUnavailable):
		log.Ctx(r.Context()).Error().Err(err).Str("path", r.URL.Path).Msg("tree db error")
		writeError(w, 503, "SERVICE_UNAVAILABLE", "database unavailable")
	case errors.Is(err, service.ErrTreeDeleted):
		writeError(w, 410, "TREE_DELETED", "tree has been deleted")
	default:
		log.Ctx(r.Context()).Error().Err(err).Msg("tree request failed")
		writeError(w, 500, "INTERNAL_ERROR", "internal server error")
	}
}

// --- Share + Presence (WIRE-004 / BUG-024) ----------------------------------

// shareRequestBody is the POST /trees/{tree_id}/share payload. Exactly one of
// Email or UserID must be provided; Permission is validated against the
// frontend's PermissionLevel set (viewer|editor|admin). The backend stores
// roles as tree_role (owner|admin|member|viewer), so "editor" maps to "member".
type shareRequestBody struct {
	Email      string `json:"email,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	Permission string `json:"permission,omitempty"`
	Message    string `json:"message,omitempty"`
}

// shareResponse is the 201 body returned to the caller (and surfaced by the
// ShareDialog as success feedback).
type shareResponse struct {
	TreeID     string `json:"treeId"`
	UserID     string `json:"userId"`
	Email      string `json:"email,omitempty"`
	Permission string `json:"permission"`
	MemberID   string `json:"memberId"`
}

// ShareTree grants a user access to a tree by creating a tree_members row.
// Only the tree owner may share. The invitee is resolved by user_id (UUID) or
// email. Permission maps to a tree_role: viewer→viewer, editor→member,
// admin→admin. Returns 201 with the share record.
//
// Mounted at POST /api/v1/trees/{tree_id}/share (membership-gated upstream,
// so the caller is already a member; ownership is enforced here).
func (h *TreeHandler) ShareTree(w http.ResponseWriter, r *http.Request) {
	if h.users == nil || h.members == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED",
			"share endpoints are not configured on this server")
		return
	}
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	// Only the owner may share.
	ownerID := UserIDFromContext(r.Context())
	if ownerID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	existing, err := h.svc.GetTree(r.Context(), treeID, service.GetTreeOptions{IncludeStats: false})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if existing.OwnerID != uuid.Nil && existing.OwnerID != ownerID {
		writeError(w, http.StatusForbidden, "NOT_TREE_OWNER", "only the tree owner may share")
		return
	}

	var req shareRequestBody
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}

	// Resolve the invitee: user_id takes precedence, then email.
	var invitee *db.User
	switch {
	case req.UserID != "":
		uid, parseErr := uuid.Parse(req.UserID)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "INVALID_USER_ID", "user_id must be a valid UUID")
			return
		}
		invitee, err = h.users.GetByID(r.Context(), uid)
	case req.Email != "":
		invitee, err = h.users.GetByEmail(r.Context(), req.Email)
	default:
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR",
			"either email or user_id is required")
		return
	}
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "USER_NOT_FOUND",
				"no user matches the provided email or user_id")
			return
		}
		log.Ctx(r.Context()).Error().Err(err).Msg("share: resolve invitee failed")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
		return
	}

	role := permissionToRole(req.Permission)
	member, err := h.members.Add(r.Context(), &db.TreeMember{
		TreeID:    treeID,
		UserID:    &invitee.ID,
		Role:      role,
		IsVisible: true,
		InvitedBy: &ownerID,
	})
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("share: add member failed")
		writeError(w, http.StatusConflict, "ALREADY_MEMBER",
			"user is already a member of this tree")
		return
	}

	writeJSON(w, http.StatusCreated, shareResponse{
		TreeID:     treeID.String(),
		UserID:     invitee.ID.String(),
		Email:      derefString(invitee.Email),
		Permission: roleToPermission(member.Role),
		MemberID:   member.ID.String(),
	})
}

// permissionToRole maps the frontend PermissionLevel (viewer|editor|admin) to
// the backend tree_role enum (viewer|member|admin). Unknown values default to
// "viewer" (least privilege).
func permissionToRole(permission string) string {
	switch permission {
	case "admin":
		return db.TreeRoleAdmin
	case "editor":
		return db.TreeRoleMember
	case "viewer", "":
		return db.TreeRoleViewer
	default:
		return db.TreeRoleViewer
	}
}

// roleToPermission is the inverse of permissionToRole — used for the response
// so the frontend gets back a value it understands.
func roleToPermission(role string) string {
	switch role {
	case db.TreeRoleOwner, db.TreeRoleAdmin:
		return "admin"
	case db.TreeRoleMember:
		return "editor"
	default:
		return "viewer"
	}
}

// --- Presence --------------------------------------------------------------

// presenceRequestBody mirrors the fields the frontend's _handlePresenceEvent
// parses: userId, userName, avatarColor, permission, cursor, viewport,
// isActive, lastSeen. All fields except userId are optional.
type presenceRequestBody struct {
	UserID      string          `json:"userId"`
	UserName    string          `json:"userName,omitempty"`
	AvatarColor string          `json:"avatarColor,omitempty"`
	Permission  string          `json:"permission,omitempty"`
	Cursor      json.RawMessage `json:"cursor,omitempty"`
	Viewport    json.RawMessage `json:"viewport,omitempty"`
	IsActive    *bool           `json:"isActive,omitempty"`
	LastSeen    string          `json:"lastSeen,omitempty"`
}

// PushPresence accepts a presence payload and broadcasts a presence_update
// SSE event to every subscriber of the tree's event stream. Presence state is
// held in an in-memory registry keyed by (treeID, userID) so leave events can
// signal departure. Returns 202 Accepted.
//
// Mounted at POST /api/v1/trees/{tree_id}/presence (membership-gated upstream).
func (h *TreeHandler) PushPresence(w http.ResponseWriter, r *http.Request) {
	if h.hub == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED",
			"presence endpoints are not configured on this server")
		return
	}
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	var req presenceRequestBody
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "userId is required")
		return
	}

	actorID := UserIDFromContext(r.Context())
	// Build the broadcast payload from the request, echoing back exactly the
	// fields the frontend's _handlePresenceEvent expects.
	payload := map[string]any{
		"userId":   req.UserID,
		"userName": req.UserName,
	}
	if req.AvatarColor != "" {
		payload["avatarColor"] = req.AvatarColor
	}
	if req.Permission != "" {
		payload["permission"] = req.Permission
	}
	if len(req.Cursor) > 0 {
		payload["cursor"] = json.RawMessage(req.Cursor)
	}
	if len(req.Viewport) > 0 {
		payload["viewport"] = json.RawMessage(req.Viewport)
	}
	if req.IsActive != nil {
		payload["isActive"] = *req.IsActive
	}
	if req.LastSeen != "" {
		payload["lastSeen"] = req.LastSeen
	} else {
		payload["lastSeen"] = time.Now().UTC().Format(time.RFC3339)
	}

	h.presence.set(treeID, req.UserID, payload)

	data, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not encode presence")
		return
	}
	h.hub.Broadcast(treeID, sse.ComposeEvent(treeID, actorID, "presence_update", json.RawMessage(data)))
	w.WriteHeader(http.StatusAccepted)
}

// LeavePresence broadcasts a presence_update event with type "leave" for the
// requesting user and removes them from the in-memory registry. The frontend's
// _handlePresenceEvent treats type==="leave" as a departure and deletes the
// remote user. Returns 204 No Content.
//
// Mounted at POST /api/v1/trees/{tree_id}/presence/leave.
func (h *TreeHandler) LeavePresence(w http.ResponseWriter, r *http.Request) {
	if h.hub == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED",
			"presence endpoints are not configured on this server")
		return
	}
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	userID := UserIDFromContext(r.Context())
	// Prefer the authenticated user; fall back to a body userId for clients
	// that can't carry identity (e.g. the browser's fetchCredentials path).
	leaveUserID := userID.String()
	if leaveUserID == "" || leaveUserID == uuid.Nil.String() {
		var req struct {
			UserID string `json:"userId,omitempty"`
		}
		if err := decodeJSON(r, &req); err == nil && req.UserID != "" {
			leaveUserID = req.UserID
		}
	}
	if leaveUserID == "" || leaveUserID == uuid.Nil.String() {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR",
			"could not determine user to remove; pass userId in the body")
		return
	}

	h.presence.remove(treeID, leaveUserID)

	payload, _ := json.Marshal(map[string]any{
		"userId": leaveUserID,
		"type":   "leave",
	})
	h.hub.Broadcast(treeID, sse.ComposeEvent(treeID, userID, "presence_update", json.RawMessage(payload)))
	w.WriteHeader(http.StatusNoContent)
}

// --- presenceRegistry: in-memory presence state per (tree, user) -----------
//
// Presence is intentionally ephemeral — it exists only to let the leave
// endpoint signal departure and to give operators a quick lookup. The
// authoritative real-time channel is the SSE broadcast; this registry is a
// best-effort snapshot, not a source of truth.

type presenceEntry struct {
	data map[string]any
}

type presenceRegistry struct {
	mu   stdsync.RWMutex
	data map[uuid.UUID]map[string]presenceEntry // treeID → userID → entry
}

func newPresenceRegistry() *presenceRegistry {
	return &presenceRegistry{data: make(map[uuid.UUID]map[string]presenceEntry)}
}

func (p *presenceRegistry) set(treeID uuid.UUID, userID string, data map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tree, ok := p.data[treeID]
	if !ok {
		tree = make(map[string]presenceEntry)
		p.data[treeID] = tree
	}
	tree[userID] = presenceEntry{data: data}
}

func (p *presenceRegistry) remove(treeID uuid.UUID, userID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if tree, ok := p.data[treeID]; ok {
		delete(tree, userID)
		if len(tree) == 0 {
			delete(p.data, treeID)
		}
	}
}

// derefString safely dereferences a *string, returning "" for nil.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
