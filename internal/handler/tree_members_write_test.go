package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

// ---------------------------------------------------------------------------
// Stubs for the member-write routes (SPEC-API-06 §10-§11)
// ---------------------------------------------------------------------------

// stubWriteMemberRepo serves IsMember/GetByTree from memory and records the
// arguments of the two write calls, so the tests can prove BOTH that the
// handler answered the right code AND that it called the repo with the exact
// normalized arguments (or did not call it at all on a rejected request).
type stubWriteMemberRepo struct {
	db.TreeMemberRepo
	roles map[[2]uuid.UUID]string // (tree, user) → role; absent pair = non-member
	rows  []db.TreeMember

	getByTreeErr error

	updateCalls int
	gotUpdateID uuid.UUID
	gotRole     *string
	gotAuto     *bool
	updateErr   error

	removeCalls int
	gotRemoveID uuid.UUID
	removeErr   error
}

func (s *stubWriteMemberRepo) IsMember(_ context.Context, treeID, userID uuid.UUID) (string, bool, error) {
	role, ok := s.roles[[2]uuid.UUID{treeID, userID}]
	return role, ok, nil
}

func (s *stubWriteMemberRepo) GetByTree(_ context.Context, treeID uuid.UUID) ([]db.TreeMember, error) {
	if s.getByTreeErr != nil {
		return nil, s.getByTreeErr
	}
	out := make([]db.TreeMember, 0, len(s.rows))
	for _, m := range s.rows {
		if m.TreeID == treeID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *stubWriteMemberRepo) UpdateMember(_ context.Context, id uuid.UUID, role *string, autoApproved *bool) (*db.TreeMember, error) {
	s.updateCalls++
	s.gotUpdateID, s.gotRole, s.gotAuto = id, role, autoApproved
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	for _, m := range s.rows {
		if m.ID != id {
			continue
		}
		updated := m
		if role != nil {
			updated.Role = *role
		}
		if autoApproved != nil {
			updated.AutoApproved = *autoApproved
		}
		return &updated, nil
	}
	return nil, db.ErrNotFound
}

func (s *stubWriteMemberRepo) Remove(_ context.Context, id uuid.UUID) error {
	s.removeCalls++
	s.gotRemoveID = id
	if s.removeErr != nil {
		return s.removeErr
	}
	for _, m := range s.rows {
		if m.ID == id {
			return nil
		}
	}
	return db.ErrNotFound
}

// recordingHub captures the events the handlers broadcast instead of fanning
// them out to subscribers.
type recordingHub struct {
	sse.SSEHub
	events []sse.SSEEvent
}

func (h *recordingHub) Broadcast(_ uuid.UUID, event sse.SSEEvent) sse.SSEEvent {
	h.events = append(h.events, event)
	return event
}

// stubMemberUserRepo resolves display identity for human participants.
type stubMemberUserRepo struct {
	db.UserRepo
	byID map[uuid.UUID]*db.User
}

func (s stubMemberUserRepo) GetByID(_ context.Context, id uuid.UUID) (*db.User, error) {
	if u, ok := s.byID[id]; ok {
		return u, nil
	}
	return nil, db.ErrNotFound
}

// memberWriteFixture is one tree with an owner, an admin, a member, a viewer
// and an outsider, plus a foreign membership that lives in ANOTHER tree.
type memberWriteFixture struct {
	treeID     uuid.UUID
	foreignTre uuid.UUID

	ownerUser uuid.UUID
	ownerMem  uuid.UUID

	adminUser uuid.UUID
	adminMem  uuid.UUID

	memberUser uuid.UUID
	memberMem  uuid.UUID

	viewerUser uuid.UUID
	viewerMem  uuid.UUID

	outsiderUser uuid.UUID
	foreignMem   uuid.UUID

	rows []db.TreeMember
}

func newMemberWriteFixture() *memberWriteFixture {
	f := &memberWriteFixture{
		treeID:       uuid.New(),
		foreignTre:   uuid.New(),
		ownerUser:    uuid.New(),
		adminUser:    uuid.New(),
		memberUser:   uuid.New(),
		viewerUser:   uuid.New(),
		outsiderUser: uuid.New(),
	}
	joined := time.Date(2026, 7, 20, 20, 0, 0, 0, time.UTC)
	f.ownerMem = uuid.New()
	f.adminMem = uuid.New()
	f.memberMem = uuid.New()
	f.viewerMem = uuid.New()
	f.foreignMem = uuid.New()

	f.rows = []db.TreeMember{
		{ID: f.ownerMem, TreeID: f.treeID, UserID: &f.ownerUser, Role: db.TreeRoleOwner, IsVisible: true, JoinedAt: joined},
		{ID: f.adminMem, TreeID: f.treeID, UserID: &f.adminUser, Role: db.TreeRoleAdmin, IsVisible: true, JoinedAt: joined.Add(time.Minute)},
		{ID: f.memberMem, TreeID: f.treeID, UserID: &f.memberUser, Role: db.TreeRoleMember, IsVisible: true, JoinedAt: joined.Add(2 * time.Minute)},
		{ID: f.viewerMem, TreeID: f.treeID, UserID: &f.viewerUser, Role: db.TreeRoleViewer, IsVisible: true, JoinedAt: joined.Add(3 * time.Minute)},
		// A membership of a DIFFERENT tree: its id must 404 (never mutate,
		// never disclose) when used against f.treeID.
		{ID: f.foreignMem, TreeID: f.foreignTre, UserID: &f.outsiderUser, Role: db.TreeRoleMember, IsVisible: true, JoinedAt: joined},
	}
	return f
}

// repo returns a stub pre-loaded with the fixture's roster and role map.
func (f *memberWriteFixture) repo() *stubWriteMemberRepo {
	return &stubWriteMemberRepo{
		roles: map[[2]uuid.UUID]string{
			{f.treeID, f.ownerUser}:  db.TreeRoleOwner,
			{f.treeID, f.adminUser}:  db.TreeRoleAdmin,
			{f.treeID, f.memberUser}: db.TreeRoleMember,
			{f.treeID, f.viewerUser}: db.TreeRoleViewer,
		},
		rows: f.rows,
	}
}

// newWriteMembersTestHandler wires the member-write routes over stubs, with the
// caller injected exactly as the auth middleware injects it in production. The
// users repo and hub are optional: nil models a server built without them.
func newWriteMembersTestHandler(treeID uuid.UUID, members db.TreeMemberRepo, users db.UserRepo, hub sse.SSEHub) *TreeHandler {
	h := NewTreeHandler(stubShareTreeService{
		tree: &service.TreeDetail{TreeSummary: service.TreeSummary{ID: treeID, OwnerID: uuid.Nil}},
	}, nil)
	return h.WithShares(users, members, hub)
}

// callMemberWrite drives PATCH/DELETE /api/v1/trees/{tree_id}/members/{id}
// with well-formed uuid params.
func callMemberWrite(t *testing.T, h *TreeHandler, method string, callerID, treeID uuid.UUID, memberParam, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callMemberWriteRaw(t, h, method, callerID, treeID.String(), memberParam, body)
}

// callMemberWriteRaw is callMemberWrite with the raw chi params, so a test can
// drive a non-uuid tree_id.
func callMemberWriteRaw(t *testing.T, h *TreeHandler, method string, callerID uuid.UUID, treeParam, memberParam, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/trees/"+treeParam+"/members/"+memberParam, http.NoBody)
	} else {
		req = httptest.NewRequest(method, "/trees/"+treeParam+"/members/"+memberParam, strings.NewReader(body))
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tree_id", treeParam)
	rctx.URLParams.Add("member_id", memberParam)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, userIDContextKey{}, callerID)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	if method == http.MethodPatch {
		h.UpdateMember(rec, req)
	} else {
		h.RemoveMember(rec, req)
	}
	return rec
}

// decodeErrorCode returns the error code of the standard envelope.
func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body apiErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error.Code
}

// ---------------------------------------------------------------------------
// Happy paths
// ---------------------------------------------------------------------------

// TestUpdateMemberRoleSuccess pins the §10.3 response: the updated member in
// the §9.3 GET-entry shape, the normalized role reaching the repo, and the
// §10.4 member_updated broadcast.
func TestUpdateMemberRoleSuccess(t *testing.T) {
	f := newMemberWriteFixture()
	avatar := "https://example.com/bane.png"
	lastSeen := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)

	t.Run("owner patches a member to admin", func(t *testing.T) {
		repo := f.repo()
		hub := &recordingHub{}
		users := stubMemberUserRepo{byID: map[uuid.UUID]*db.User{
			f.memberUser: {ID: f.memberUser, DisplayName: "Bane", AvatarURL: &avatar, LastSeenAt: &lastSeen},
		}}
		h := newWriteMembersTestHandler(f.treeID, repo, users, hub)

		rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), `{"role":"admin"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}

		var entry memberListEntry
		if err := json.NewDecoder(rec.Body).Decode(&entry); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if entry.ID != f.memberMem || entry.TreeID != f.treeID {
			t.Errorf("id/tree_id = %s/%s, want %s/%s", entry.ID, entry.TreeID, f.memberMem, f.treeID)
		}
		if entry.Role != db.TreeRoleAdmin {
			t.Errorf("role = %q, want %q", entry.Role, db.TreeRoleAdmin)
		}
		if entry.ParticipantType != "human" || entry.UserID == nil || *entry.UserID != f.memberUser {
			t.Errorf("participant_type/user_id = %q/%v, want human/%s", entry.ParticipantType, entry.UserID, f.memberUser)
		}
		// Display identity comes from the users repo (§9.3 fields).
		if entry.DisplayName != "Bane" || entry.AvatarURL == nil || *entry.AvatarURL != avatar || entry.LastSeenAt == nil {
			t.Errorf("display identity = %q/%v/%v, want Bane/avatar/lastSeen", entry.DisplayName, entry.AvatarURL, entry.LastSeenAt)
		}
		if !entry.JoinedAt.Equal(time.Date(2026, 7, 20, 20, 2, 0, 0, time.UTC)) {
			t.Errorf("joined_at = %v, want the stored join instant", entry.JoinedAt)
		}

		if repo.updateCalls != 1 {
			t.Fatalf("UpdateMember calls = %d, want 1", repo.updateCalls)
		}
		if repo.gotUpdateID != f.memberMem {
			t.Errorf("UpdateMember id = %s, want %s", repo.gotUpdateID, f.memberMem)
		}
		if repo.gotRole == nil || *repo.gotRole != db.TreeRoleAdmin {
			t.Errorf("UpdateMember role = %v, want %q", repo.gotRole, db.TreeRoleAdmin)
		}
		if repo.gotAuto != nil {
			t.Errorf("UpdateMember auto_approved = %v, want nil (absent field must stay untouched)", *repo.gotAuto)
		}

		if len(hub.events) != 1 {
			t.Fatalf("broadcast events = %d, want 1", len(hub.events))
		}
		ev := hub.events[0]
		if ev.Type != "member_updated" {
			t.Errorf("event type = %q, want member_updated", ev.Type)
		}
		if ev.TreeID != f.treeID || ev.ActorID != f.ownerUser {
			t.Errorf("event tree/actor = %s/%s, want %s/%s", ev.TreeID, ev.ActorID, f.treeID, f.ownerUser)
		}
		var payload struct {
			MemberID string         `json:"member_id"`
			TreeID   string         `json:"tree_id"`
			Changes  map[string]any `json:"changes"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("decode event data: %v", err)
		}
		if payload.MemberID != f.memberMem.String() || payload.TreeID != f.treeID.String() {
			t.Errorf("event member/tree = %s/%s, want %s/%s", payload.MemberID, payload.TreeID, f.memberMem, f.treeID)
		}
		if payload.Changes["role"] != db.TreeRoleAdmin {
			t.Errorf("event changes = %v, want role admin", payload.Changes)
		}
		if _, ok := payload.Changes["auto_approved"]; ok {
			t.Errorf("event changes = %v, want auto_approved absent (it was not patched)", payload.Changes)
		}
	})

	t.Run("admin patches a plain member", func(t *testing.T) {
		repo := f.repo()
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)

		rec := callMemberWrite(t, h, http.MethodPatch, f.adminUser, f.treeID, f.memberMem.String(), `{"role":"viewer"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if repo.gotRole == nil || *repo.gotRole != db.TreeRoleViewer {
			t.Errorf("UpdateMember role = %v, want viewer", repo.gotRole)
		}
	})

	t.Run("patch without a hub still succeeds", func(t *testing.T) {
		repo := f.repo()
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), `{"role":"member"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (a nil hub must not fail the write) (body: %s)", rec.Code, rec.Body.String())
		}
	})
}

// TestUpdateMemberAutoApprovedSuccess pins the absent-vs-false distinction the
// *bool body field exists for: {"auto_approved":false} must reach the repo as
// an explicit false, while an absent field stays nil (proven above).
func TestUpdateMemberAutoApprovedSuccess(t *testing.T) {
	f := newMemberWriteFixture()

	t.Run("auto_approved alone", func(t *testing.T) {
		repo := f.repo()
		hub := &recordingHub{}
		h := newWriteMembersTestHandler(f.treeID, repo, nil, hub)

		rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), `{"auto_approved":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if repo.gotRole != nil {
			t.Errorf("UpdateMember role = %v, want nil", *repo.gotRole)
		}
		if repo.gotAuto == nil || !*repo.gotAuto {
			t.Fatalf("UpdateMember auto_approved = %v, want true", repo.gotAuto)
		}
		var entry memberListEntry
		if err := json.NewDecoder(rec.Body).Decode(&entry); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !entry.AutoApproved {
			t.Error("response auto_approved = false, want true")
		}
		if len(hub.events) != 1 || hub.events[0].Type != "member_updated" {
			t.Fatalf("events = %v, want one member_updated", hub.events)
		}
		var payload struct {
			Changes map[string]any `json:"changes"`
		}
		if err := json.Unmarshal(hub.events[0].Data, &payload); err != nil {
			t.Fatalf("decode event data: %v", err)
		}
		if payload.Changes["auto_approved"] != true {
			t.Errorf("changes = %v, want auto_approved true", payload.Changes)
		}
		if _, ok := payload.Changes["role"]; ok {
			t.Errorf("changes = %v, want role absent", payload.Changes)
		}
	})

	t.Run("explicit false is not absent", func(t *testing.T) {
		repo := f.repo()
		// Start the member with the flag ON so the update has something to clear.
		for i := range repo.rows {
			if repo.rows[i].ID == f.memberMem {
				repo.rows[i].AutoApproved = true
			}
		}
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)

		rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), `{"auto_approved":false}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if repo.gotAuto == nil || *repo.gotAuto {
			t.Fatalf("UpdateMember auto_approved = %v, want an explicit false", repo.gotAuto)
		}
		var entry memberListEntry
		if err := json.NewDecoder(rec.Body).Decode(&entry); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if entry.AutoApproved {
			t.Error("response auto_approved = true, want the cleared false")
		}
	})

	t.Run("both fields in one patch", func(t *testing.T) {
		repo := f.repo()
		hub := &recordingHub{}
		h := newWriteMembersTestHandler(f.treeID, repo, nil, hub)

		rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(),
			`{"role":"editor","auto_approved":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		// "editor" is the frontend's name for the DB's "member".
		if repo.gotRole == nil || *repo.gotRole != db.TreeRoleMember {
			t.Fatalf("UpdateMember role = %v, want the normalized %q", repo.gotRole, db.TreeRoleMember)
		}
		if repo.gotAuto == nil || !*repo.gotAuto {
			t.Fatalf("UpdateMember auto_approved = %v, want true", repo.gotAuto)
		}
		if repo.updateCalls != 1 {
			t.Errorf("UpdateMember calls = %d, want 1 (both fields in a single statement)", repo.updateCalls)
		}
		var payload struct {
			Changes map[string]any `json:"changes"`
		}
		if err := json.Unmarshal(hub.events[0].Data, &payload); err != nil {
			t.Fatalf("decode event data: %v", err)
		}
		if payload.Changes["role"] != db.TreeRoleMember || payload.Changes["auto_approved"] != true {
			t.Errorf("changes = %v, want both fields", payload.Changes)
		}
	})
}

// TestRemoveMemberSuccess pins the §11.3 body and the §11.4 user_left event,
// plus the "another admin" reading of ADMIN_SCOPE_LIMITED: an admin may remove
// their own membership (leaving the tree) but not a colleague's.
func TestRemoveMemberSuccess(t *testing.T) {
	f := newMemberWriteFixture()

	t.Run("owner removes a member", func(t *testing.T) {
		repo := f.repo()
		hub := &recordingHub{}
		h := newWriteMembersTestHandler(f.treeID, repo, nil, hub)

		rec := callMemberWrite(t, h, http.MethodDelete, f.ownerUser, f.treeID, f.memberMem.String(), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var body removeMemberResponse
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.MemberID != f.memberMem || body.TreeID != f.treeID {
			t.Errorf("member_id/tree_id = %s/%s, want %s/%s", body.MemberID, body.TreeID, f.memberMem, f.treeID)
		}
		if _, err := time.Parse(time.RFC3339, body.RemovedAt); err != nil {
			t.Errorf("removed_at = %q, want RFC3339: %v", body.RemovedAt, err)
		}
		if repo.removeCalls != 1 || repo.gotRemoveID != f.memberMem {
			t.Errorf("Remove calls/id = %d/%s, want 1/%s", repo.removeCalls, repo.gotRemoveID, f.memberMem)
		}

		if len(hub.events) != 1 {
			t.Fatalf("broadcast events = %d, want 1", len(hub.events))
		}
		ev := hub.events[0]
		if ev.Type != "user_left" {
			t.Errorf("event type = %q, want user_left", ev.Type)
		}
		var payload struct {
			MemberID      string `json:"member_id"`
			TreeID        string `json:"tree_id"`
			ParticipantID string `json:"participant_id"`
			RemovedAt     string `json:"removed_at"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("decode event data: %v", err)
		}
		if payload.MemberID != f.memberMem.String() || payload.TreeID != f.treeID.String() {
			t.Errorf("event member/tree = %s/%s, want %s/%s", payload.MemberID, payload.TreeID, f.memberMem, f.treeID)
		}
		if payload.ParticipantID != f.memberUser.String() {
			t.Errorf("event participant_id = %s, want the removed user %s", payload.ParticipantID, f.memberUser)
		}
		if payload.RemovedAt != body.RemovedAt {
			t.Errorf("event removed_at = %q, want the body's %q", payload.RemovedAt, body.RemovedAt)
		}
	})

	t.Run("admin may remove themselves", func(t *testing.T) {
		repo := f.repo()
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWrite(t, h, http.MethodDelete, f.adminUser, f.treeID, f.adminMem.String(), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (an admin leaving is not 'another admin') (body: %s)",
				rec.Code, rec.Body.String())
		}
		if repo.removeCalls != 1 {
			t.Errorf("Remove calls = %d, want 1", repo.removeCalls)
		}
	})

	t.Run("delete without a hub still succeeds", func(t *testing.T) {
		repo := f.repo()
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWrite(t, h, http.MethodDelete, f.ownerUser, f.treeID, f.viewerMem.String(), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// Role vocabulary (§ criterion 5)
// ---------------------------------------------------------------------------

// TestMemberRoleVocabulary covers every member of BOTH vocabularies that meet
// on this route — the DB enum owner|admin|member|viewer and the frontend
// PermissionLevel viewer|editor|admin — plus the values in neither, and proves
// the editor↔member mapping in both directions.
func TestMemberRoleVocabulary(t *testing.T) {
	f := newMemberWriteFixture()

	// Every assignable DB role is accepted and stored verbatim.
	for _, role := range []string{db.TreeRoleAdmin, db.TreeRoleMember, db.TreeRoleViewer} {
		t.Run("db role "+role, func(t *testing.T) {
			repo := f.repo()
			h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
			body := `{"role":"` + role + `"}`
			rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if repo.gotRole == nil || *repo.gotRole != role {
				t.Errorf("stored role = %v, want %q", repo.gotRole, role)
			}
			var entry memberListEntry
			if err := json.NewDecoder(rec.Body).Decode(&entry); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if entry.Role != role {
				t.Errorf("response role = %q, want %q", entry.Role, role)
			}
		})
	}

	// Every frontend permission is accepted, maps onto its DB role, and maps
	// back to the same frontend value — the round trip the Share dialog needs.
	for _, permission := range []string{"viewer", "editor", "admin"} {
		t.Run("frontend permission "+permission, func(t *testing.T) {
			repo := f.repo()
			h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
			body := `{"role":"` + permission + `"}`
			rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if repo.gotRole == nil {
				t.Fatal("UpdateMember role = nil, want the mapped DB role")
			}
			if want := permissionToRole(permission); *repo.gotRole != want {
				t.Errorf("stored role = %q, want %q (permissionToRole)", *repo.gotRole, want)
			}
			var entry memberListEntry
			if err := json.NewDecoder(rec.Body).Decode(&entry); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if got := roleToPermission(entry.Role); got != permission {
				t.Errorf("roleToPermission(%q) = %q, want the round-tripped %q", entry.Role, got, permission)
			}
		})
	}

	// editor↔member is THE cross-vocabulary pair, stated explicitly.
	t.Run("editor is stored as member", func(t *testing.T) {
		repo := f.repo()
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), `{"role":"editor"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if repo.gotRole == nil || *repo.gotRole != db.TreeRoleMember {
			t.Fatalf("stored role = %v, want %q", repo.gotRole, db.TreeRoleMember)
		}
		if got := roleToPermission(db.TreeRoleMember); got != "editor" {
			t.Errorf("roleToPermission(member) = %q, want editor", got)
		}
	})

	// owner is in the DB enum but is not assignable through this route.
	t.Run("owner is not assignable", func(t *testing.T) {
		repo := f.repo()
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), `{"role":"owner"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
		}
		if code := decodeErrorCode(t, rec); code != "CANNOT_INVITE_AS_OWNER" {
			t.Errorf("code = %q, want CANNOT_INVITE_AS_OWNER", code)
		}
		if repo.updateCalls != 0 {
			t.Errorf("UpdateMember calls = %d, want 0", repo.updateCalls)
		}
	})

	// Values in neither vocabulary (and near-misses: the match is
	// case-sensitive, like the enum itself) are unmappable.
	for _, raw := range []string{"superuser", "Editor", "OWNER", "readonly", "", "member ", "member\n"} {
		t.Run("unmappable "+raw, func(t *testing.T) {
			repo := f.repo()
			h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
			body, err := json.Marshal(map[string]string{"role": raw})
			if err != nil {
				t.Fatal(err)
			}
			rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), string(body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
			}
			if code := decodeErrorCode(t, rec); code != "INVALID_ROLE" {
				t.Errorf("code = %q, want INVALID_ROLE", code)
			}
			if repo.updateCalls != 0 {
				t.Errorf("UpdateMember calls = %d, want 0", repo.updateCalls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error contract (§11.5)
// ---------------------------------------------------------------------------

// TestUpdateMemberErrors walks every error code §11.5 assigns to the PATCH
// route and proves the repo was NOT called for each rejection.
func TestUpdateMemberErrors(t *testing.T) {
	f := newMemberWriteFixture()

	cases := []struct {
		name       string
		caller     uuid.UUID
		member     string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name: "invalid member id", caller: f.ownerUser, member: "not-a-uuid",
			body: `{"role":"admin"}`, wantStatus: http.StatusBadRequest, wantCode: "INVALID_MEMBER_ID",
		},
		{
			name: "malformed json", caller: f.ownerUser, member: f.memberMem.String(),
			body: `{"role":`, wantStatus: http.StatusBadRequest, wantCode: "INVALID_BODY",
		},
		{
			name: "unknown field", caller: f.ownerUser, member: f.memberMem.String(),
			body: `{"permission":"admin"}`, wantStatus: http.StatusBadRequest, wantCode: "INVALID_BODY",
		},
		{
			name: "no fields", caller: f.ownerUser, member: f.memberMem.String(),
			body: `{}`, wantStatus: http.StatusBadRequest, wantCode: "NO_FIELDS_PROVIDED",
		},
		{
			name: "invalid role", caller: f.ownerUser, member: f.memberMem.String(),
			body: `{"role":"superuser"}`, wantStatus: http.StatusBadRequest, wantCode: "INVALID_ROLE",
		},
		{
			name: "owner as proposed role", caller: f.ownerUser, member: f.memberMem.String(),
			body: `{"role":"owner"}`, wantStatus: http.StatusBadRequest, wantCode: "CANNOT_INVITE_AS_OWNER",
		},
		{
			name: "auto approve for a new viewer", caller: f.ownerUser, member: f.memberMem.String(),
			body:       `{"role":"viewer","auto_approved":true}`,
			wantStatus: http.StatusBadRequest, wantCode: "AUTO_APPROVE_NOT_ALLOWED_FOR_VIEWER",
		},
		{
			name: "auto approve for an existing viewer", caller: f.ownerUser, member: f.viewerMem.String(),
			body:       `{"auto_approved":true}`,
			wantStatus: http.StatusBadRequest, wantCode: "AUTO_APPROVE_NOT_ALLOWED_FOR_VIEWER",
		},
		{
			name: "unknown member", caller: f.ownerUser, member: uuid.New().String(),
			body: `{"role":"admin"}`, wantStatus: http.StatusNotFound, wantCode: "MEMBER_NOT_FOUND",
		},
		{
			// A membership of ANOTHER tree must 404, never mutate.
			name: "member of a different tree", caller: f.ownerUser, member: f.foreignMem.String(),
			body: `{"role":"admin"}`, wantStatus: http.StatusNotFound, wantCode: "MEMBER_NOT_FOUND",
		},
		{
			name: "caller is a plain member", caller: f.memberUser, member: f.viewerMem.String(),
			body: `{"role":"admin"}`, wantStatus: http.StatusForbidden, wantCode: "NOT_TREE_OWNER_OR_ADMIN",
		},
		{
			name: "caller is a viewer", caller: f.viewerUser, member: f.memberMem.String(),
			body: `{"role":"admin"}`, wantStatus: http.StatusForbidden, wantCode: "NOT_TREE_OWNER_OR_ADMIN",
		},
		{
			name: "target is the owner", caller: f.ownerUser, member: f.ownerMem.String(),
			body: `{"role":"admin"}`, wantStatus: http.StatusForbidden, wantCode: "CANNOT_CHANGE_OWNER_ROLE",
		},
		{
			name: "admin targets another admin", caller: f.adminUser, member: f.ownerMem.String(),
			body: `{"role":"member"}`, wantStatus: http.StatusForbidden, wantCode: "CANNOT_CHANGE_OWNER_ROLE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := f.repo()
			if tc.name == "admin targets another admin" {
				// Re-point the admin caller at a DIFFERENT admin row so the
				// ADMIN_SCOPE_LIMITED branch (not the owner branch) is the
				// one under test.
				otherAdmin := uuid.New()
				repo.roles[[2]uuid.UUID{f.treeID, tc.caller}] = db.TreeRoleAdmin
				repo.rows = append(repo.rows, db.TreeMember{
					ID: otherAdmin, TreeID: f.treeID, UserID: &f.viewerUser,
					Role: db.TreeRoleAdmin, IsVisible: true, JoinedAt: time.Now().UTC(),
				})
				tc.member = otherAdmin.String()
				tc.wantStatus = http.StatusForbidden
				tc.wantCode = "ADMIN_SCOPE_LIMITED"
			}
			h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)

			rec := callMemberWrite(t, h, http.MethodPatch, tc.caller, f.treeID, tc.member, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if code := decodeErrorCode(t, rec); code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if repo.updateCalls != 0 {
				t.Errorf("UpdateMember calls = %d, want 0 for a rejected request", repo.updateCalls)
			}
		})
	}

	t.Run("invalid tree id", func(t *testing.T) {
		repo := f.repo()
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWriteRaw(t, h, http.MethodPatch, f.ownerUser, "nope", f.memberMem.String(), `{"role":"admin"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
		}
		if code := decodeErrorCode(t, rec); code != "INVALID_TREE_ID" {
			t.Errorf("code = %q, want INVALID_TREE_ID", code)
		}
		if repo.updateCalls != 0 {
			t.Errorf("UpdateMember calls = %d, want 0", repo.updateCalls)
		}
	})

	t.Run("repo ErrNotFound becomes MEMBER_NOT_FOUND", func(t *testing.T) {
		repo := f.repo()
		repo.updateErr = db.ErrNotFound
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), `{"role":"admin"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
		}
		if code := decodeErrorCode(t, rec); code != "MEMBER_NOT_FOUND" {
			t.Errorf("code = %q, want MEMBER_NOT_FOUND", code)
		}
	})

	t.Run("repo failure is a 503, never a 500 for client input", func(t *testing.T) {
		repo := f.repo()
		repo.updateErr = errors.New("pool closed")
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), `{"role":"admin"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("roster read failure is a 503", func(t *testing.T) {
		repo := f.repo()
		repo.getByTreeErr = errors.New("pool closed")
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWrite(t, h, http.MethodPatch, f.ownerUser, f.treeID, f.memberMem.String(), `{"role":"admin"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
		}
		if repo.updateCalls != 0 {
			t.Errorf("UpdateMember calls = %d, want 0", repo.updateCalls)
		}
	})
}

// TestRemoveMemberErrors walks the DELETE half of the §11.5 table.
func TestRemoveMemberErrors(t *testing.T) {
	f := newMemberWriteFixture()

	cases := []struct {
		name       string
		caller     uuid.UUID
		member     string
		wantStatus int
		wantCode   string
	}{
		{
			name: "invalid member id", caller: f.ownerUser, member: "not-a-uuid",
			wantStatus: http.StatusBadRequest, wantCode: "INVALID_MEMBER_ID",
		},
		{
			name: "unknown member", caller: f.ownerUser, member: uuid.New().String(),
			wantStatus: http.StatusNotFound, wantCode: "MEMBER_NOT_FOUND",
		},
		{
			name: "member of a different tree", caller: f.ownerUser, member: f.foreignMem.String(),
			wantStatus: http.StatusNotFound, wantCode: "MEMBER_NOT_FOUND",
		},
		{
			name: "caller is a plain member", caller: f.memberUser, member: f.viewerMem.String(),
			wantStatus: http.StatusForbidden, wantCode: "NOT_TREE_OWNER_OR_ADMIN",
		},
		{
			name: "target is the owner", caller: f.ownerUser, member: f.ownerMem.String(),
			wantStatus: http.StatusForbidden, wantCode: "CANNOT_REMOVE_OWNER",
		},
		{
			name: "admin targets another admin", caller: f.adminUser, member: f.ownerMem.String(),
			wantStatus: http.StatusForbidden, wantCode: "CANNOT_REMOVE_OWNER",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := f.repo()
			if tc.name == "admin targets another admin" {
				otherAdmin := uuid.New()
				repo.rows = append(repo.rows, db.TreeMember{
					ID: otherAdmin, TreeID: f.treeID, UserID: &f.viewerUser,
					Role: db.TreeRoleAdmin, IsVisible: true, JoinedAt: time.Now().UTC(),
				})
				tc.member = otherAdmin.String()
				tc.wantStatus = http.StatusForbidden
				tc.wantCode = "ADMIN_SCOPE_LIMITED"
			}
			h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)

			rec := callMemberWrite(t, h, http.MethodDelete, tc.caller, f.treeID, tc.member, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if code := decodeErrorCode(t, rec); code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if repo.removeCalls != 0 {
				t.Errorf("Remove calls = %d, want 0 for a rejected request", repo.removeCalls)
			}
		})
	}

	t.Run("repo ErrNotFound becomes MEMBER_NOT_FOUND", func(t *testing.T) {
		repo := f.repo()
		repo.removeErr = db.ErrNotFound
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWrite(t, h, http.MethodDelete, f.ownerUser, f.treeID, f.memberMem.String(), "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
		}
		if code := decodeErrorCode(t, rec); code != "MEMBER_NOT_FOUND" {
			t.Errorf("code = %q, want MEMBER_NOT_FOUND", code)
		}
	})

	t.Run("repo failure is a 503", func(t *testing.T) {
		repo := f.repo()
		repo.removeErr = errors.New("pool closed")
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		rec := callMemberWrite(t, h, http.MethodDelete, f.ownerUser, f.treeID, f.memberMem.String(), "")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
		}
	})
}

// TestMemberWritesUnauthenticatedAndUnconfigured pins the two non-§11.5
// guards these routes owe: an unauthenticated caller is 401 TOKEN_MISSING, and
// a server built without the member repo answers 501 NOT_IMPLEMENTED (the same
// contract ListMembers and ShareTree honour).
func TestMemberWritesUnauthenticatedAndUnconfigured(t *testing.T) {
	f := newMemberWriteFixture()

	t.Run("unauthenticated caller", func(t *testing.T) {
		repo := f.repo()
		h := newWriteMembersTestHandler(f.treeID, repo, nil, nil)
		for _, method := range []string{http.MethodPatch, http.MethodDelete} {
			rec := callMemberWrite(t, h, method, uuid.Nil, f.treeID, f.memberMem.String(), `{"role":"admin"}`)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s status = %d, want 401 (body: %s)", method, rec.Code, rec.Body.String())
			}
			if code := decodeErrorCode(t, rec); code != "TOKEN_MISSING" {
				t.Errorf("%s code = %q, want TOKEN_MISSING", method, code)
			}
		}
	})

	t.Run("member repo not wired", func(t *testing.T) {
		h := NewTreeHandler(stubShareTreeService{
			tree: &service.TreeDetail{TreeSummary: service.TreeSummary{ID: f.treeID, OwnerID: f.ownerUser}},
		}, nil)
		for _, method := range []string{http.MethodPatch, http.MethodDelete} {
			rec := callMemberWrite(t, h, method, f.ownerUser, f.treeID, f.memberMem.String(), `{"role":"admin"}`)
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("%s status = %d, want 501 (body: %s)", method, rec.Code, rec.Body.String())
			}
			if code := decodeErrorCode(t, rec); code != "NOT_IMPLEMENTED" {
				t.Errorf("%s code = %q, want NOT_IMPLEMENTED", method, code)
			}
		}
	})
}
