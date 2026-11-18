// Package handler provides HTTP handlers for Canopy REST endpoints.
// Each handler group accepts the corresponding service interface.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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
	svc      service.TreeService
	sync     sync.SyncEngine
	users    db.UserRepo
	members  db.TreeMemberRepo
	hub      sse.SSEHub
	presence *presenceRegistry
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
		if params.ContentFormat == "" {
			params.ContentFormat = service.FormatMarkdown
		}
		if params.NodeType == "" {
			params.NodeType = service.NodeTypeMessage
		}
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
	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}
	q := r.URL.Query()
	opts := service.GetTreeOptions{
		IncludeStats:   q.Get("include_stats") != "false",
		IncludeMembers: includeMembersRequested(q),
		IncludeRelated: true, // WIRE-006: always surface associations when present
	}
	out, err := h.svc.GetTree(r.Context(), id, opts)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	allowed, err := h.canViewTree(r.Context(), id, userID, out.OwnerID)
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("tree access check failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not verify tree access")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "NOT_TREE_OWNER", "you do not have access to this tree")
		return
	}

	// The roster is filled only AFTER the access gate: a caller who may not
	// read the tree must never receive member identities in the 403 path.
	if opts.IncludeMembers {
		if err := h.fillDetailMembers(r.Context(), out, userID); err != nil {
			log.Ctx(r.Context()).Error().Err(err).Msg("tree detail: members roster failed")
			writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
			return
		}
		writeJSON(w, 200, newTreeDetailWithMembers(out))
		return
	}
	writeJSON(w, 200, out)
}

// treeDetailWithMembers is the GET /trees/{tree_id}?include_members=true body:
// the service detail with its roster lifted one level so the members array is
// ALWAYS present. The embedded TreeDetail already carries members /
// members_truncated / total_members, but all three are `omitempty`, which
// drops them when they are empty/false — and a caller who ASKED for the roster
// must see `members: []` for a tree with no memberships, not a missing key
// (SPEC-API-02 §5.3 documents the field as an array). The depth-0 fields here
// win over the embedded ones; every other detail field still serializes from
// the embedded struct exactly as on the plain detail path.
type treeDetailWithMembers struct {
	*service.TreeDetail
	Members          []service.MemberSummary `json:"members"`
	MembersTruncated bool                    `json:"members_truncated"`
	TotalMembers     int                     `json:"total_members"`
}

// newTreeDetailWithMembers lifts the roster off the detail so it is emitted
// even when empty. A nil Members would serialize as null, so an absent roster
// is normalized to an empty slice.
func newTreeDetailWithMembers(d *service.TreeDetail) treeDetailWithMembers {
	members := d.Members
	if members == nil {
		members = []service.MemberSummary{}
	}
	return treeDetailWithMembers{
		TreeDetail:       d,
		Members:          members,
		MembersTruncated: d.MembersTruncated,
		TotalMembers:     d.TotalMembers,
	}
}

// treeDetailMemberCap is the maximum roster size a tree-detail response
// carries (SPEC-API-02 EC-23): the array is cut at 200 entries and the body
// reports members_truncated + total_members so the client knows to page the
// rest through GET /trees/{tree_id}/members.
const treeDetailMemberCap = 200

// includeMembersRequested reports whether the caller asked for the roster.
// SPEC-API-02 §5.2 gives include_members a default of false, so ONLY an
// explicit true opts in — every other value (absent, "false", garbage) leaves
// the detail body exactly as it was before the roster existed.
func includeMembersRequested(q url.Values) bool {
	return q.Get("include_members") == "true"
}

// fillDetailMembers populates a tree detail's members roster from the same
// repository query GET /trees/{tree_id}/members serves (ListByTree, oldest
// membership first), so the two surfaces are parity-identical by
// construction, and records the SPEC-API-02 EC-23 truncation facts.
//
// Visibility follows SPEC-API-06 §9.5 exactly as ListMembers applies it:
// owners and admins see the full roster, members and viewers see is_visible
// rows only. The ownership fallback covers a tree whose owner-membership row
// is missing (CreateTree's insert is best-effort).
func (h *TreeHandler) fillDetailMembers(ctx context.Context, detail *service.TreeDetail, userID uuid.UUID) error {
	if h.members == nil {
		// Server built without share/member dependencies: leave the roster at
		// the empty non-nil slice the service seeded and keep the detail
		// readable. Worth a log line — a silently empty roster looks like a
		// tree with no members.
		log.Ctx(ctx).Warn().Str("tree_id", detail.ID.String()).
			Msg("tree detail: include_members requested but member endpoints are not configured")
		return nil
	}

	includeHidden := isTreeOwner(detail.OwnerID, userID)
	if !includeHidden {
		role, isMember, err := h.members.IsMember(ctx, detail.ID, userID)
		if err != nil {
			return err
		}
		includeHidden = isMember && (role == db.TreeRoleOwner || role == db.TreeRoleAdmin)
	}

	rows, total, err := h.members.ListByTree(ctx, detail.ID, includeHidden, treeDetailMemberCap, 0)
	if err != nil {
		return err
	}

	roster := make([]service.MemberSummary, 0, len(rows))
	for _, m := range rows {
		roster = append(roster, memberSummaryFromRow(m))
	}
	detail.Members = roster
	detail.TotalMembers = total
	detail.MembersTruncated = total > len(roster)
	return nil
}

// memberSummaryFromRow maps the repo's joined membership row onto the §9.3
// member object. Both rostered surfaces (this detail roster and the paginated
// GET /members page) go through it, so they cannot drift.
func memberSummaryFromRow(m db.TreeMemberSummary) service.MemberSummary {
	return service.MemberSummary{
		ID:              m.ID,
		TreeID:          m.TreeID,
		ParticipantType: m.ParticipantType,
		UserID:          m.UserID,
		ProfileID:       m.ProfileID,
		DisplayName:     m.DisplayName,
		ProfileName:     m.ProfileName,
		AvatarURL:       m.AvatarURL,
		Role:            m.Role,
		IsVisible:       m.IsVisible,
		AutoApproved:    m.AutoApproved,
		JoinedAt:        m.JoinedAt,
		LastSeenAt:      m.LastSeenAt,
		InvitedBy:       m.InvitedBy,
	}
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
	if !isTreeOwner(existing.OwnerID, userID) {
		writeError(w, http.StatusForbidden, "NOT_TREE_OWNER", "you do not have access to this tree")
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
	if !isTreeOwner(existing.OwnerID, userID) {
		writeError(w, http.StatusForbidden, "NOT_TREE_OWNER", "you do not have access to this tree")
		return
	}

	if _, err := h.svc.DeleteTree(r.Context(), id); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Internal helpers -------------------------------------------------------

func isTreeOwner(ownerID, userID uuid.UUID) bool {
	return ownerID != uuid.Nil && ownerID == userID
}

// canViewTree allows the owner and any active tree member to read a tree.
// Tree-level mutations intentionally use isTreeOwner instead.
func (h *TreeHandler) canViewTree(ctx context.Context, treeID, userID, ownerID uuid.UUID) (bool, error) {
	if isTreeOwner(ownerID, userID) {
		return true, nil
	}
	if h.members == nil {
		return false, nil
	}
	// DF-HERMES-CANOPY-79: this is a READ gate (owner or any active member
	// may view the tree); the role is irrelevant here and viewers must
	// still pass, so the role return value is discarded.
	_, isMember, err := h.members.IsMember(ctx, treeID, userID)
	return isMember, err
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

// inviteeNotRegisteredMessage explains the DF-HERMES-CANOPY-87 condition: the
// share target has no users row on this instance, so there is nothing to grant
// membership to yet. The invitee is named (user_id wins, matching resolution
// order) and the message states the remedy, because a bare "user not found"
// reads like the tree or route is missing rather than an unprovisioned person.
func inviteeNotRegisteredMessage(req shareRequestBody) string {
	who := "the requested invitee"
	switch {
	case req.UserID != "":
		who = "user_id " + req.UserID
	case req.Email != "":
		who = strconv.Quote(req.Email)
	}
	msg := "no account is registered for " + who + " on this Canopy instance; " +
		"the invitee must sign in to the instance once before they can be shared with"
	if req.UserID == "" && req.Email != "" {
		// DF-HERMES-CANOPY-89: signing in is exactly what provisions the
		// account, and dev-mode provisioning records no email, so obeying the
		// instruction above still leaves an email invite unresolvable. Name the
		// resolution path that works instead of leaving the caller in a loop.
		msg += "; that account is created without an email, so share by the " +
			"account's user UUID instead"
	}
	return msg
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
			// DF-HERMES-CANOPY-87: a bare USER_NOT_FOUND here reads like the
			// tree or the route is missing. The real condition is that the
			// invitee has no users row on this instance yet, so the code names
			// that condition and the message says how to unblock it.
			writeError(w, http.StatusNotFound, "INVITEE_NOT_REGISTERED",
				inviteeNotRegisteredMessage(req))
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

// memberListEntry is one member object in the SPEC-API-06 §9 response. It
// ALIASES the service's MemberSummary so the two rostered surfaces — this
// page and the tree-detail roster (SPEC-API-02 §5.3 include_members) — have
// one definition and cannot drift. Field names are the spec's exact
// snake_case JSON names (§9.3/§9.4) — the Share dialog consumes them
// verbatim, so no camelCase aliases are offered.
type memberListEntry = service.MemberSummary

// listMembersResponse is the §9.3 envelope: the page plus total/limit/offset.
type listMembersResponse struct {
	Members []memberListEntry `json:"members"`
	Total   int               `json:"total"`
	Limit   int               `json:"limit"`
	Offset  int               `json:"offset"`
}

// ListMembers returns the tree's membership roster (SPEC-API-06 §9) — the
// Share dialog's member source. Pagination: limit defaults to 50 and caps at
// 200 (§9.2's 1–200 range), offset defaults to 0; the response echoes the
// EFFECTIVE limit. Authorization (§9.1 "must be tree member"): the caller
// needs a membership row or ownership of the tree, enforced here in the
// handler so the route stays safe under any mount; non-members get 403
// NOT_TREE_MEMBER, the same code TreeMembershipMiddleware uses upstream.
// Visibility (§9.5): owners and admins see the full roster; members and
// viewers see only is_visible rows.
//
// Mounted at GET /api/v1/trees/{tree_id}/members (membership-gated upstream).
func (h *TreeHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	if h.members == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED",
			"member endpoints are not configured on this server")
		return
	}
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}

	q := r.URL.Query()
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_LIMIT", "limit must be an integer")
			return
		}
		if v < 1 {
			writeError(w, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 200")
			return
		}
		limit = v
	}
	if limit > 200 {
		limit = 200 // §9.2 cap
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, "INVALID_OFFSET", "offset must be a non-negative integer")
			return
		}
		offset = v
	}

	userID := UserIDFromContext(r.Context())
	if userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

	role, isMember, err := h.members.IsMember(r.Context(), treeID, userID)
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("list members: membership check failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not verify membership")
		return
	}
	if !isMember {
		// Owner fallback: the tree owner passes even without a membership
		// row (CreateTree's owner-membership insert is best-effort). Same
		// owner-or-member rule as canViewTree.
		existing, gerr := h.svc.GetTree(r.Context(), treeID, service.GetTreeOptions{IncludeStats: false})
		if gerr != nil {
			h.writeServiceError(w, r, gerr)
			return
		}
		if !isTreeOwner(existing.OwnerID, userID) {
			writeError(w, http.StatusForbidden, "NOT_TREE_MEMBER", "you are not a member of this tree")
			return
		}
		role = db.TreeRoleOwner
	}

	// §9.5: owners and admins see the full roster regardless of visibility;
	// members and viewers see only is_visible=true rows.
	includeHidden := role == db.TreeRoleOwner || role == db.TreeRoleAdmin

	members, total, err := h.members.ListByTree(r.Context(), treeID, includeHidden, limit, offset)
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("list members: query failed")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
		return
	}

	out := make([]memberListEntry, 0, len(members))
	for _, m := range members {
		out = append(out, memberSummaryFromRow(m))
	}
	writeJSON(w, http.StatusOK, listMembersResponse{
		Members: out,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	})
}

// --- Member management (SPEC-API-06 §10-§11) --------------------------------

// updateMemberRequestBody is the §10.2 PATCH body. Both fields are optional,
// so auto_approved is a *bool: "absent" (leave the stored flag alone) has to
// stay distinguishable from an explicit false (clear it). role is a *string
// for the same reason — an absent role must not be read as the empty role.
type updateMemberRequestBody struct {
	Role         *string `json:"role"`
	AutoApproved *bool   `json:"auto_approved"`
}

// removeMemberResponse is the §11.3 DELETE body. removed_at is formatted as
// RFC3339 (not RFC3339Nano) so the wire shape is exactly the spec's.
type removeMemberResponse struct {
	MemberID  uuid.UUID `json:"member_id"`
	TreeID    uuid.UUID `json:"tree_id"`
	RemovedAt string    `json:"removed_at"`
}

// normalizeMemberRole maps a proposed role from EITHER role vocabulary onto
// the tree_role enum the database stores, and reports whether the value is
// assignable at all.
//
// Two vocabularies meet on this route: the DB enum (owner|admin|member|viewer,
// migrations/000008) and the frontend PermissionLevel the Share dialog holds
// (viewer|editor|admin, frontend/src/types/multiUser.ts). "editor" is the
// frontend's name for the DB's "member" — the same pair permissionToRole /
// roleToPermission already translate for the share path — so a PATCH carrying
// the dialog's vocabulary is accepted rather than answered with INVALID_ROLE.
//
// "owner" is deliberately NOT accepted here: ownership is not transferable
// through this route (the handler answers CANNOT_INVITE_AS_OWNER). Anything in
// neither vocabulary ("superuser", "Editor" — the match is case-sensitive,
// like the enum itself) is unmappable and reports ok=false.
func normalizeMemberRole(raw string) (string, bool) {
	switch raw {
	case db.TreeRoleAdmin:
		return db.TreeRoleAdmin, true
	case db.TreeRoleMember, "editor":
		return db.TreeRoleMember, true
	case db.TreeRoleViewer:
		return db.TreeRoleViewer, true
	default:
		return "", false
	}
}

// parseMemberID reads and validates the {member_id} chi URL parameter
// (§11.5: an invalid member id is 400 INVALID_MEMBER_ID, never a 404).
func parseMemberID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "member_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_MEMBER_ID", "member_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

// memberEntry maps a stored membership row onto the §9.3 member object — the
// same shape a GET /members entry has, which §10.3 requires the PATCH response
// to carry. Display identity is resolved on a best-effort basis (see
// memberDisplayIdentity).
func (h *TreeHandler) memberEntry(ctx context.Context, m *db.TreeMember) memberListEntry {
	entry := memberListEntry{
		ID:              m.ID,
		TreeID:          m.TreeID,
		ParticipantType: participantTypeOf(m),
		UserID:          m.UserID,
		ProfileID:       m.ProfileID,
		Role:            m.Role,
		IsVisible:       m.IsVisible,
		AutoApproved:    m.AutoApproved,
		JoinedAt:        m.JoinedAt,
		InvitedBy:       m.InvitedBy,
	}
	h.memberDisplayIdentity(ctx, &entry)
	return entry
}

// participantTypeOf mirrors the SQL join's CASE: a human membership carries
// user_id, an agent membership carries profile_id.
func participantTypeOf(m *db.TreeMember) string {
	if m.UserID != nil {
		return "human"
	}
	return "profile"
}

// memberDisplayIdentity fills display_name / avatar_url / last_seen_at for a
// human participant from the users repo. Profile display names are left empty:
// TreeHandler is wired with the user and member repos only (WithShares), so it
// has no profile repo to resolve profiles.display_name from — the PATCH
// response then carries the stored row facts plus the identity this server can
// resolve, and docs/API.md says so. Lookup failures are not fatal: the update
// already committed, so a missing display name must not turn a 200 into a 500.
func (h *TreeHandler) memberDisplayIdentity(ctx context.Context, entry *memberListEntry) {
	if h.users == nil || entry.UserID == nil {
		return
	}
	user, err := h.users.GetByID(ctx, *entry.UserID)
	if err != nil || user == nil {
		return
	}
	entry.DisplayName = user.DisplayName
	entry.AvatarURL = user.AvatarURL
	entry.LastSeenAt = user.LastSeenAt
}

// resolveManagerRole returns the caller's member-management role for the tree
// — TreeRoleOwner or TreeRoleAdmin — or answers the error and reports false.
//
// §10.2/§11.2: "Only owners and admins can update/remove members". The owner
// passes even without a membership row (the same ownership fallback
// ListMembers uses: CreateTree's owner-membership insert is best-effort).
// Everyone else gets 403 NOT_TREE_OWNER_OR_ADMIN. This runs BEFORE the target
// member is looked up so a plain member or viewer cannot use these routes to
// probe which member ids exist.
func (h *TreeHandler) resolveManagerRole(w http.ResponseWriter, r *http.Request, treeID, userID uuid.UUID) (string, bool) {
	role, isMember, err := h.members.IsMember(r.Context(), treeID, userID)
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("members: membership check failed")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
		return "", false
	}
	if isMember && (role == db.TreeRoleOwner || role == db.TreeRoleAdmin) {
		return role, true
	}
	existing, gerr := h.svc.GetTree(r.Context(), treeID, service.GetTreeOptions{IncludeStats: false})
	if gerr != nil {
		h.writeServiceError(w, r, gerr)
		return "", false
	}
	if isTreeOwner(existing.OwnerID, userID) {
		return db.TreeRoleOwner, true
	}
	writeError(w, http.StatusForbidden, "NOT_TREE_OWNER_OR_ADMIN",
		"only owners and admins can perform this action")
	return "", false
}

// findTreeMember resolves a membership id WITHIN the tree. A member id that
// belongs to a different tree is reported exactly like a missing one
// (MEMBER_NOT_FOUND 404): the route must neither mutate nor disclose a
// membership outside the tree its caller is authorized for (§11.5).
func (h *TreeHandler) findTreeMember(w http.ResponseWriter, r *http.Request, treeID, memberID uuid.UUID) (*db.TreeMember, bool) {
	members, err := h.members.GetByTree(r.Context(), treeID)
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("members: list by tree failed")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
		return nil, false
	}
	for i := range members {
		if members[i].ID == memberID {
			return &members[i], true
		}
	}
	writeError(w, http.StatusNotFound, "MEMBER_NOT_FOUND", "tree member not found")
	return nil, false
}

// membershipIsCaller reports whether the row belongs to the calling user. Used
// for the "another admin" half of ADMIN_SCOPE_LIMITED: §10.2 forbids an admin
// from promoting or demoting OTHER admins, which is not the same as an admin
// editing their own row.
func membershipIsCaller(m *db.TreeMember, userID uuid.UUID) bool {
	return m.UserID != nil && *m.UserID == userID
}

// UpdateMember changes a membership's role and/or auto_approved flag
// (SPEC-API-06 §10). PATCH semantics: only the fields present in the body are
// written, in one statement, so a two-field patch never lands half-applied.
//
// Error contract (§10.2 constraints + §11.5 table): INVALID_MEMBER_ID 400,
// NO_FIELDS_PROVIDED 400, INVALID_ROLE 400, CANNOT_INVITE_AS_OWNER 400,
// AUTO_APPROVE_NOT_ALLOWED_FOR_VIEWER 400, INVALID_BODY 400,
// NOT_TREE_OWNER_OR_ADMIN 403, CANNOT_CHANGE_OWNER_ROLE 403,
// ADMIN_SCOPE_LIMITED 403, MEMBER_NOT_FOUND 404. Client-shape errors (ids,
// body, role vocabulary) are answered before authorization; authorization is
// answered before the target lookup. No client-side validation failure is ever
// a 500.
//
// On success the updated member is returned in the §9.3 GET-entry shape and a
// member_updated SSE event carries the changed fields (§10.4).
//
// Mounted at PATCH /api/v1/trees/{tree_id}/members/{member_id} (membership-
// gated upstream, same middleware class as the sibling tree writes).
func (h *TreeHandler) UpdateMember(w http.ResponseWriter, r *http.Request) {
	if h.members == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED",
			"member endpoints are not configured on this server")
		return
	}
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}
	memberID, ok := parseMemberID(w, r)
	if !ok {
		return
	}
	callerID := UserIDFromContext(r.Context())
	if callerID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

	var req updateMemberRequestBody
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	if req.Role == nil && req.AutoApproved == nil {
		writeError(w, http.StatusBadRequest, "NO_FIELDS_PROVIDED",
			"at least one field must be provided for update")
		return
	}

	var roleArg *string
	if req.Role != nil {
		if *req.Role == db.TreeRoleOwner {
			writeError(w, http.StatusBadRequest, "CANNOT_INVITE_AS_OWNER",
				"cannot invite a participant as owner")
			return
		}
		normalized, ok := normalizeMemberRole(*req.Role)
		if !ok {
			writeError(w, http.StatusBadRequest, "INVALID_ROLE",
				"role must be one of: admin, member, viewer")
			return
		}
		roleArg = &normalized
	}

	callerRole, ok := h.resolveManagerRole(w, r, treeID, callerID)
	if !ok {
		return
	}
	target, ok := h.findTreeMember(w, r, treeID, memberID)
	if !ok {
		return
	}

	if target.Role == db.TreeRoleOwner {
		writeError(w, http.StatusForbidden, "CANNOT_CHANGE_OWNER_ROLE",
			"cannot change the owner's role")
		return
	}
	if callerRole == db.TreeRoleAdmin && target.Role == db.TreeRoleAdmin && !membershipIsCaller(target, callerID) {
		writeError(w, http.StatusForbidden, "ADMIN_SCOPE_LIMITED",
			"admins cannot modify other admins or the owner")
		return
	}

	// §10.2: viewers cannot create content, so pre-approving them is
	// meaningless. The check reads the EFFECTIVE role, so both
	// {"role":"viewer","auto_approved":true} and a bare auto_approved on a
	// member who is already a viewer are rejected.
	effectiveRole := target.Role
	if roleArg != nil {
		effectiveRole = *roleArg
	}
	if effectiveRole == db.TreeRoleViewer && req.AutoApproved != nil && *req.AutoApproved {
		writeError(w, http.StatusBadRequest, "AUTO_APPROVE_NOT_ALLOWED_FOR_VIEWER",
			"auto_approve cannot be enabled for viewers")
		return
	}

	updated, err := h.members.UpdateMember(r.Context(), memberID, roleArg, req.AutoApproved)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// The row vanished between the roster read and the update.
			writeError(w, http.StatusNotFound, "MEMBER_NOT_FOUND", "tree member not found")
			return
		}
		log.Ctx(r.Context()).Error().Err(err).Msg("update member: write failed")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
		return
	}

	h.broadcastMemberUpdated(treeID, callerID, updated, roleArg, req.AutoApproved)

	writeJSON(w, http.StatusOK, h.memberEntry(r.Context(), updated))
}

// broadcastMemberUpdated emits the §10.4 member_updated event. changes carries
// only the fields the request patched, with the role in its normalized DB
// spelling so consumers agree with the response body. A server without an SSE
// hub (WithShares with a nil hub) simply skips the event — losing the
// notification must not fail a write that already committed.
func (h *TreeHandler) broadcastMemberUpdated(treeID, actorID uuid.UUID, updated *db.TreeMember, role *string, autoApproved *bool) {
	if h.hub == nil {
		return
	}
	changes := map[string]any{}
	if role != nil {
		changes["role"] = *role
	}
	if autoApproved != nil {
		changes["auto_approved"] = *autoApproved
	}
	data, err := json.Marshal(map[string]any{
		"member_id": updated.ID.String(),
		"tree_id":   updated.TreeID.String(),
		"changes":   changes,
	})
	if err != nil {
		return
	}
	h.hub.Broadcast(treeID, sse.ComposeEvent(treeID, actorID, "member_updated", json.RawMessage(data)))
}

// RemoveMember hard-deletes a membership (SPEC-API-06 §11). The member's nodes
// stay: removal is a membership delete, not a content delete (§11.2).
//
// Error contract (§11.2 + §11.5): INVALID_MEMBER_ID 400,
// NOT_TREE_OWNER_OR_ADMIN 403, CANNOT_REMOVE_OWNER 403, ADMIN_SCOPE_LIMITED
// 403, MEMBER_NOT_FOUND 404. The owner can never be removed, and an admin can
// neither remove another admin nor the owner.
//
// Success answers 200 with the §11.3 body (member_id, tree_id, removed_at) and
// broadcasts user_left with participant_id (§11.4).
//
// Mounted at DELETE /api/v1/trees/{tree_id}/members/{member_id} (membership-
// gated upstream, same middleware class as the sibling tree writes).
func (h *TreeHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	if h.members == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED",
			"member endpoints are not configured on this server")
		return
	}
	treeID, ok := parseTreeID(w, r)
	if !ok {
		return
	}
	memberID, ok := parseMemberID(w, r)
	if !ok {
		return
	}
	callerID := UserIDFromContext(r.Context())
	if callerID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
		return
	}

	callerRole, ok := h.resolveManagerRole(w, r, treeID, callerID)
	if !ok {
		return
	}
	target, ok := h.findTreeMember(w, r, treeID, memberID)
	if !ok {
		return
	}

	if target.Role == db.TreeRoleOwner {
		writeError(w, http.StatusForbidden, "CANNOT_REMOVE_OWNER", "cannot remove the tree owner")
		return
	}
	if callerRole == db.TreeRoleAdmin && target.Role == db.TreeRoleAdmin && !membershipIsCaller(target, callerID) {
		writeError(w, http.StatusForbidden, "ADMIN_SCOPE_LIMITED",
			"admins cannot remove other admins or the owner")
		return
	}

	if err := h.members.Remove(r.Context(), memberID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "MEMBER_NOT_FOUND", "tree member not found")
			return
		}
		log.Ctx(r.Context()).Error().Err(err).Msg("remove member: delete failed")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "database unavailable")
		return
	}

	removedAt := time.Now().UTC().Format(time.RFC3339)
	participantID := ""
	switch {
	case target.UserID != nil:
		participantID = target.UserID.String()
	case target.ProfileID != nil:
		participantID = target.ProfileID.String()
	}
	if h.hub != nil {
		data, err := json.Marshal(map[string]any{
			"member_id":      memberID.String(),
			"tree_id":        treeID.String(),
			"participant_id": participantID,
			"removed_at":     removedAt,
		})
		if err == nil {
			h.hub.Broadcast(treeID, sse.ComposeEvent(treeID, callerID, "user_left", json.RawMessage(data)))
		}
	}

	writeJSON(w, http.StatusOK, removeMemberResponse{
		MemberID:  memberID,
		TreeID:    treeID,
		RemovedAt: removedAt,
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
