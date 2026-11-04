package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// stubListMemberRepo serves the membership check and the members page from
// memory, recording the visibility flag and pagination the handler asked for
// so tests can assert the §9.2/§9.5 mapping.
type stubListMemberRepo struct {
	db.TreeMemberRepo
	roles     map[[2]uuid.UUID]string // (tree, user) → role; absent pair = non-member
	page      []db.TreeMemberSummary
	total     int
	gotTree   uuid.UUID
	gotHidden bool
	gotLimit  int
	gotOffset int
}

func (s *stubListMemberRepo) IsMember(_ context.Context, treeID, userID uuid.UUID) (string, bool, error) {
	role, ok := s.roles[[2]uuid.UUID{treeID, userID}]
	return role, ok, nil
}

func (s *stubListMemberRepo) ListByTree(_ context.Context, treeID uuid.UUID, includeHidden bool, limit, offset int) ([]db.TreeMemberSummary, int, error) {
	s.gotTree, s.gotHidden, s.gotLimit, s.gotOffset = treeID, includeHidden, limit, offset
	return s.page, s.total, nil
}

// newListMembersTestHandler builds the members route over stubs, with the
// caller injected exactly as the auth middleware injects it in production.
// The users repo and hub are unused by ListMembers.
func newListMembersTestHandler(ownerID, treeID uuid.UUID, members db.TreeMemberRepo) *TreeHandler {
	h := NewTreeHandler(stubShareTreeService{
		tree: &service.TreeDetail{TreeSummary: service.TreeSummary{ID: treeID, OwnerID: ownerID}},
	}, nil)
	return h.WithShares(nil, members, nil)
}

// getMembers drives GET /api/v1/trees/{tree_id}/members for callerID and
// returns the recorded response.
func getMembers(t *testing.T, h *TreeHandler, callerID, treeID uuid.UUID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/trees/"+treeID.String()+"/members"+query, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tree_id", treeID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, userIDContextKey{}, callerID)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.ListMembers(rec, req)
	return rec
}

// memberPageFixture returns one human and one profile membership summary, in
// the shape ListByTree's join produces.
func memberPageFixture(treeID uuid.UUID) []db.TreeMemberSummary {
	humanID := uuid.New()
	profileID := uuid.New()
	inviter := uuid.New()
	joined := time.Date(2026, 7, 20, 20, 0, 0, 0, time.UTC)
	lastSeen := time.Date(2026, 7, 20, 23, 30, 0, 0, time.UTC)
	avatar := "https://example.com/a.png"
	profileName := "coding-hermes"
	return []db.TreeMemberSummary{
		{
			TreeMember: db.TreeMember{
				ID: uuid.New(), TreeID: treeID, UserID: &humanID,
				Role: db.TreeRoleOwner, IsVisible: true,
				JoinedAt: joined,
			},
			ParticipantType: "human",
			DisplayName:     "Bane",
			AvatarURL:       &avatar,
			LastSeenAt:      &lastSeen,
		},
		{
			TreeMember: db.TreeMember{
				ID: uuid.New(), TreeID: treeID, ProfileID: &profileID,
				Role: db.TreeRoleMember, IsVisible: true, AutoApproved: true,
				JoinedAt: joined.Add(time.Hour), InvitedBy: &inviter,
			},
			ParticipantType: "profile",
			DisplayName:     "Coding Hermes",
			ProfileName:     &profileName,
		},
	}
}

// TestListMembersResponseShape pins the SPEC-API-06 §9.3 wire shape: the
// members/total/limit/offset envelope and the exact snake_case member field
// names the Share dialog consumes.
func TestListMembersResponseShape(t *testing.T) {
	ownerID := uuid.New()
	treeID := uuid.New()
	callerID := uuid.New()

	members := &stubListMemberRepo{
		roles: map[[2]uuid.UUID]string{{treeID, callerID}: db.TreeRoleMember},
		page:  memberPageFixture(treeID),
		total: 2,
	}
	h := newListMembersTestHandler(ownerID, treeID, members)

	rec := getMembers(t, h, callerID, treeID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, key := range []string{"members", "total", "limit", "offset"} {
		if _, ok := body[key]; !ok {
			t.Errorf("envelope missing %q: %s", key, rec.Body.String())
		}
	}
	if got := body["total"].(float64); got != 2 {
		t.Errorf("total = %v, want 2", got)
	}
	// §9.2 default limit is 50 and the response echoes the effective limit.
	if got := body["limit"].(float64); got != 50 {
		t.Errorf("limit = %v, want the default 50", got)
	}
	if got := body["offset"].(float64); got != 0 {
		t.Errorf("offset = %v, want 0", got)
	}

	list, ok := body["members"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("members = %v, want a 2-entry array", body["members"])
	}
	for _, key := range []string{
		"id", "tree_id", "participant_type", "user_id", "display_name",
		"role", "is_visible", "auto_approved", "joined_at", "invited_by",
	} {
		if _, ok := list[0].(map[string]any)[key]; !ok {
			t.Errorf("member object missing %q: %v", key, list[0])
		}
	}
	human := list[0].(map[string]any)
	if human["participant_type"] != "human" || human["display_name"] != "Bane" {
		t.Errorf("human entry = %v/%v, want human/Bane", human["participant_type"], human["display_name"])
	}
	if human["tree_id"] != treeID.String() {
		t.Errorf("tree_id = %v, want %s", human["tree_id"], treeID)
	}
	profile := list[1].(map[string]any)
	if profile["participant_type"] != "profile" || profile["profile_name"] != "coding-hermes" {
		t.Errorf("profile entry = %v/%v, want profile/coding-hermes", profile["participant_type"], profile["profile_name"])
	}
	if profile["auto_approved"] != true || profile["invited_by"] == nil {
		t.Errorf("profile entry auto_approved/invited_by = %v/%v", profile["auto_approved"], profile["invited_by"])
	}

	// §9.5: a plain member must not see hidden rows — the repo is asked for
	// the visible-only page.
	if members.gotHidden {
		t.Error("member caller: ListByTree got includeHidden=true, want false (§9.5)")
	}
	if members.gotTree != treeID {
		t.Errorf("ListByTree tree = %s, want %s", members.gotTree, treeID)
	}
}

// TestListMembersVisibilityByRole pins §9.5: owners and admins see the full
// roster (includeHidden=true), members and viewers see visible-only.
func TestListMembersVisibilityByRole(t *testing.T) {
	ownerID := uuid.New()
	treeID := uuid.New()

	for role, wantHidden := range map[string]bool{
		db.TreeRoleOwner:  true,
		db.TreeRoleAdmin:  true,
		db.TreeRoleMember: false,
		db.TreeRoleViewer: false,
	} {
		callerID := uuid.New()
		members := &stubListMemberRepo{
			roles: map[[2]uuid.UUID]string{{treeID, callerID}: role},
		}
		h := newListMembersTestHandler(ownerID, treeID, members)
		rec := getMembers(t, h, callerID, treeID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("role %s: status = %d, want 200 (body: %s)", role, rec.Code, rec.Body.String())
		}
		if members.gotHidden != wantHidden {
			t.Errorf("role %s: includeHidden = %v, want %v", role, members.gotHidden, wantHidden)
		}
	}
}

// TestListMembersAuthorization pins §9.1 "must be tree member": a caller
// with no membership row and no ownership gets 403 NOT_TREE_MEMBER in the
// standard envelope, while the tree owner passes via the ownership fallback
// even when their membership row is missing.
func TestListMembersAuthorization(t *testing.T) {
	ownerID := uuid.New()
	treeID := uuid.New()

	t.Run("non-member non-owner is rejected", func(t *testing.T) {
		stranger := uuid.New()
		members := &stubListMemberRepo{roles: map[[2]uuid.UUID]string{}}
		h := newListMembersTestHandler(ownerID, treeID, members)

		rec := getMembers(t, h, stranger, treeID, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		var body apiErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
		if body.Error.Code != "NOT_TREE_MEMBER" {
			t.Errorf("code = %q, want NOT_TREE_MEMBER", body.Error.Code)
		}
	})

	t.Run("owner without a membership row passes", func(t *testing.T) {
		members := &stubListMemberRepo{roles: map[[2]uuid.UUID]string{}}
		h := newListMembersTestHandler(ownerID, treeID, members)

		rec := getMembers(t, h, ownerID, treeID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
		// The ownership fallback grants the owner the full roster (§9.5).
		if !members.gotHidden {
			t.Error("owner fallback: includeHidden = false, want true")
		}
	})

	t.Run("unauthenticated caller is rejected", func(t *testing.T) {
		members := &stubListMemberRepo{roles: map[[2]uuid.UUID]string{}}
		h := newListMembersTestHandler(ownerID, treeID, members)

		rec := getMembers(t, h, uuid.Nil, treeID, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})
}

// TestListMembersPagination pins §9.2: limit defaults to 50, caps at 200,
// rejects non-integers and values below 1; offset defaults to 0 and rejects
// negatives. The repo receives the EFFECTIVE values and the response echoes
// them.
func TestListMembersPagination(t *testing.T) {
	ownerID := uuid.New()
	treeID := uuid.New()
	callerID := uuid.New()

	newHandler := func() (*TreeHandler, *stubListMemberRepo) {
		members := &stubListMemberRepo{
			roles: map[[2]uuid.UUID]string{{treeID, callerID}: db.TreeRoleMember},
		}
		return newListMembersTestHandler(ownerID, treeID, members), members
	}

	t.Run("explicit limit and offset reach the repo", func(t *testing.T) {
		h, members := newHandler()
		rec := getMembers(t, h, callerID, treeID, "?limit=25&offset=10")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if members.gotLimit != 25 || members.gotOffset != 10 {
			t.Errorf("ListByTree limit/offset = %d/%d, want 25/10", members.gotLimit, members.gotOffset)
		}
		var body listMembersResponse
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Limit != 25 || body.Offset != 10 {
			t.Errorf("echoed limit/offset = %d/%d, want 25/10", body.Limit, body.Offset)
		}
	})

	t.Run("limit above 200 is capped and the cap is echoed", func(t *testing.T) {
		h, members := newHandler()
		rec := getMembers(t, h, callerID, treeID, "?limit=500")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if members.gotLimit != 200 {
			t.Errorf("ListByTree limit = %d, want the 200 cap", members.gotLimit)
		}
		var body listMembersResponse
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Limit != 200 {
			t.Errorf("echoed limit = %d, want the effective 200", body.Limit)
		}
	})

	t.Run("invalid pagination values are 400 in the standard envelope", func(t *testing.T) {
		for query, wantCode := range map[string]string{
			"?limit=abc":  "INVALID_LIMIT",
			"?limit=0":    "INVALID_LIMIT",
			"?limit=-3":   "INVALID_LIMIT",
			"?offset=-1":  "INVALID_OFFSET",
			"?offset=1.5": "INVALID_OFFSET",
			"?offset=NaN": "INVALID_OFFSET",
		} {
			h, _ := newHandler()
			rec := getMembers(t, h, callerID, treeID, query)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400 (body: %s)", query, rec.Code, rec.Body.String())
				continue
			}
			var body apiErrorBody
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("%s: decode error body: %v", query, err)
			}
			if body.Error.Code != wantCode {
				t.Errorf("%s: code = %q, want %s", query, body.Error.Code, wantCode)
			}
		}
	})

	t.Run("empty roster is an empty array, not null", func(t *testing.T) {
		h, _ := newHandler()
		rec := getMembers(t, h, callerID, treeID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"members":[]`) {
			t.Errorf("empty roster body = %s, want \"members\":[]", rec.Body.String())
		}
	})
}

// TestListMembersNotConfigured pins the 501 the handler owes when the server
// is built without share/member dependencies — the same contract ShareTree
// already honours.
func TestListMembersNotConfigured(t *testing.T) {
	treeID := uuid.New()
	h := NewTreeHandler(stubShareTreeService{
		tree: &service.TreeDetail{TreeSummary: service.TreeSummary{ID: treeID, OwnerID: uuid.New()}},
	}, nil)

	rec := getMembers(t, h, uuid.New(), treeID, "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotImplemented, rec.Body.String())
	}
	var body apiErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != "NOT_IMPLEMENTED" {
		t.Errorf("code = %q, want NOT_IMPLEMENTED", body.Error.Code)
	}
}
