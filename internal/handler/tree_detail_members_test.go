package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// --- Fixtures ---------------------------------------------------------------

// stubDetailTreeService answers GetTree with a fresh copy of a fixed detail so
// the handler can fill the roster without the stub's template being mutated
// between calls, and records the options the handler built.
type stubDetailTreeService struct {
	service.TreeService
	detail  service.TreeDetail
	gotOpts service.GetTreeOptions
	calls   int
}

func (s *stubDetailTreeService) GetTree(_ context.Context, _ uuid.UUID, opts service.GetTreeOptions) (*service.TreeDetail, error) {
	s.gotOpts = opts
	s.calls++
	cp := s.detail
	return &cp, nil
}

// stubRosterRepo serves the membership check and the roster page from memory,
// applying the same visibility filter and limit/offset window the SQL does so
// the truncation and parity assertions exercise real paging behaviour.
type stubRosterRepo struct {
	db.TreeMemberRepo
	roles map[[2]uuid.UUID]string // (tree, user) → role; absent pair = non-member
	rows  []db.TreeMemberSummary

	listCalls int
	gotTree   uuid.UUID
	gotHidden []bool
	gotLimit  int
	gotOffset int
}

func (s *stubRosterRepo) IsMember(_ context.Context, treeID, userID uuid.UUID) (string, bool, error) {
	role, ok := s.roles[[2]uuid.UUID{treeID, userID}]
	return role, ok, nil
}

func (s *stubRosterRepo) ListByTree(_ context.Context, treeID uuid.UUID, includeHidden bool, limit, offset int) ([]db.TreeMemberSummary, int, error) {
	s.listCalls++
	s.gotTree = treeID
	s.gotHidden = append(s.gotHidden, includeHidden)
	s.gotLimit, s.gotOffset = limit, offset

	visible := make([]db.TreeMemberSummary, 0, len(s.rows))
	for _, m := range s.rows {
		if includeHidden || m.IsVisible {
			visible = append(visible, m)
		}
	}
	total := len(visible)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	return visible[offset:end], total, nil
}

// rosterRows builds n memberships, oldest first, with distinct member ids.
// The third row is invisible so the §9.5 visibility filter has something to
// drop.
func rosterRows(treeID uuid.UUID, n int) []db.TreeMemberSummary {
	base := time.Date(2026, 7, 20, 20, 0, 0, 0, time.UTC)
	rows := make([]db.TreeMemberSummary, 0, n)
	for i := 0; i < n; i++ {
		userID := uuid.New()
		rows = append(rows, db.TreeMemberSummary{
			TreeMember: db.TreeMember{
				ID:        uuid.New(),
				TreeID:    treeID,
				UserID:    &userID,
				Role:      db.TreeRoleMember,
				IsVisible: i != 2,
				JoinedAt:  base.Add(time.Duration(i) * time.Minute),
			},
			ParticipantType: "human",
			DisplayName:     "member-" + userID.String()[:8],
		})
	}
	return rows
}

// newDetailMembersHandler builds a handler whose GetTree and ListMembers share
// one roster repo, exactly as the production wiring does (WithShares).
func newDetailMembersHandler(ownerID, treeID uuid.UUID, members db.TreeMemberRepo) (*TreeHandler, *stubDetailTreeService) {
	svc := &stubDetailTreeService{
		detail: service.TreeDetail{TreeSummary: service.TreeSummary{ID: treeID, OwnerID: ownerID}},
	}
	return NewTreeHandler(svc, nil).WithShares(nil, members, nil), svc
}

// getTreeDetail drives GET /api/v1/trees/{tree_id} for callerID.
func getTreeDetail(t *testing.T, h *TreeHandler, callerID, treeID uuid.UUID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/trees/"+treeID.String()+query, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tree_id", treeID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, userIDContextKey{}, callerID)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.GetTree(rec, req)
	return rec
}

// decodeDetail decodes a detail body without a struct, so the assertions can
// prove a key is ABSENT (the pre-change baseline) as well as present.
func decodeDetail(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode detail body: %v", err)
	}
	return body
}

func memberIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["members"].([]any)
	if !ok {
		t.Fatalf("members = %v, want an array", body["members"])
	}
	ids := make([]string, 0, len(raw))
	for _, e := range raw {
		id, _ := e.(map[string]any)["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

// intField reads a numeric body field and reports whether it was present.
// Absence is a reportable fact here (the pre-change body has no total_members),
// never a panic — a failed type assertion would abort every remaining test in
// the binary and hide the real failures.
func intField(t *testing.T, body map[string]any, key string) (int, bool) {
	t.Helper()
	raw, ok := body[key]
	if !ok {
		return 0, false
	}
	f, ok := raw.(float64)
	if !ok {
		t.Fatalf("%s = %v (%T), want a number", key, raw, raw)
	}
	return int(f), true
}

// --- Tests ------------------------------------------------------------------

// TestGetTreeIncludeMembersBaseline pins the pre-change behaviour: a tree with
// membership rows whose detail is fetched WITHOUT include_members carries no
// members key at all, and the roster repo is never queried. This is the
// regression baseline the flag exists to change (SPEC-API-02 §5.2 defaults
// include_members to false).
func TestGetTreeIncludeMembersBaseline(t *testing.T) {
	ownerID, treeID := uuid.New(), uuid.New()

	for _, query := range []string{"", "?include_members=false", "?include_members=maybe"} {
		repo := &stubRosterRepo{
			roles: map[[2]uuid.UUID]string{{treeID, ownerID}: db.TreeRoleOwner},
			rows:  rosterRows(treeID, 3),
		}
		h, svc := newDetailMembersHandler(ownerID, treeID, repo)

		rec := getTreeDetail(t, h, ownerID, treeID, query)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d, want 200 (body: %s)", query, rec.Code, rec.Body.String())
		}
		body := decodeDetail(t, rec)
		for _, key := range []string{"members", "members_truncated", "total_members"} {
			if _, present := body[key]; present {
				t.Errorf("%q: body carries %q even though the roster was not requested: %s", query, key, rec.Body.String())
			}
		}
		if repo.listCalls != 0 {
			t.Errorf("%q: ListByTree called %d times, want 0", query, repo.listCalls)
		}
		if svc.gotOpts.IncludeMembers {
			t.Errorf("%q: handler asked the service for members, want false", query)
		}
	}
}

// TestGetTreeIncludeMembersRoster is AC-1: with include_members=true the detail
// body carries a populated members array in the SPEC-API-06 §9.3 object shape.
func TestGetTreeIncludeMembersRoster(t *testing.T) {
	ownerID, treeID, callerID := uuid.New(), uuid.New(), uuid.New()
	rows := rosterRows(treeID, 3) // row 2 is invisible; caller is a plain member
	repo := &stubRosterRepo{
		roles: map[[2]uuid.UUID]string{{treeID, callerID}: db.TreeRoleMember},
		rows:  rows,
	}
	h, svc := newDetailMembersHandler(ownerID, treeID, repo)

	rec := getTreeDetail(t, h, callerID, treeID, "?include_members=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.calls != 1 || !svc.gotOpts.IncludeMembers {
		t.Fatalf("service GetTree calls/opts = %d/%+v, want 1 with IncludeMembers=true", svc.calls, svc.gotOpts)
	}

	body := decodeDetail(t, rec)
	ids := memberIDs(t, body)
	if len(ids) == 0 {
		t.Fatalf("members = %v, want the visible roster, not an empty array", body["members"])
	}
	if got, ok := intField(t, body, "total_members"); !ok || got != 2 {
		t.Errorf("total_members = %v (present=%v), want the 2 visible memberships", got, ok)
	}
	if truncated, present := body["members_truncated"]; present && truncated != false {
		t.Errorf("members_truncated = %v, want absent/false under the cap", truncated)
	}
	// §9.5: a plain member sees only visible rows.
	if len(ids) != 2 {
		t.Errorf("members = %d entries, want the 2 visible memberships (hidden row filtered): %s", len(ids), rec.Body.String())
	}
	if len(repo.gotHidden) != 1 || repo.gotHidden[0] {
		t.Errorf("ListByTree includeHidden = %v, want [false] for a plain-member caller (§9.5)", repo.gotHidden)
	}
	// The roster window is the cap at offset 0 — not a default page size.
	if repo.gotLimit != treeDetailMemberCap || repo.gotOffset != 0 {
		t.Errorf("ListByTree limit/offset = %d/%d, want %d/0", repo.gotLimit, repo.gotOffset, treeDetailMemberCap)
	}

	entry := body["members"].([]any)[0].(map[string]any)
	for _, key := range []string{
		"id", "tree_id", "participant_type", "user_id", "profile_id",
		"display_name", "profile_name", "avatar_url", "role", "is_visible",
		"auto_approved", "joined_at", "last_seen_at", "invited_by",
	} {
		if _, ok := entry[key]; !ok {
			t.Errorf("member object missing %q: %v", key, entry)
		}
	}
}

// TestGetTreeIncludeMembersCap is AC-2 (SPEC-API-02 EC-23): a roster larger
// than the cap is cut at 200 entries, total_members reports the real count and
// members_truncated says the array was cut.
func TestGetTreeIncludeMembersCap(t *testing.T) {
	ownerID, treeID := uuid.New(), uuid.New()

	t.Run("over the cap", func(t *testing.T) {
		repo := &stubRosterRepo{
			roles: map[[2]uuid.UUID]string{{treeID, ownerID}: db.TreeRoleOwner},
			rows:  rosterRows(treeID, 250),
		}
		h, _ := newDetailMembersHandler(ownerID, treeID, repo)

		rec := getTreeDetail(t, h, ownerID, treeID, "?include_members=true")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		body := decodeDetail(t, rec)
		ids := memberIDs(t, body)
		if len(ids) != treeDetailMemberCap {
			t.Errorf("members = %d entries, want the %d cap", len(ids), treeDetailMemberCap)
		}
		if got, ok := intField(t, body, "total_members"); !ok || got != 250 {
			t.Errorf("total_members = %v (present=%v), want the real 250", got, ok)
		}
		if truncated, _ := body["members_truncated"].(bool); !truncated {
			t.Errorf("members_truncated = %v, want true over the cap", body["members_truncated"])
		}
	})

	t.Run("exactly at the cap is not truncation", func(t *testing.T) {
		repo := &stubRosterRepo{
			roles: map[[2]uuid.UUID]string{{treeID, ownerID}: db.TreeRoleOwner},
			rows:  rosterRows(treeID, treeDetailMemberCap),
		}
		h, _ := newDetailMembersHandler(ownerID, treeID, repo)

		rec := getTreeDetail(t, h, ownerID, treeID, "?include_members=true")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		body := decodeDetail(t, rec)
		if len(memberIDs(t, body)) != treeDetailMemberCap {
			t.Errorf("members = %d entries, want all %d", len(memberIDs(t, body)), treeDetailMemberCap)
		}
		if truncated, present := body["members_truncated"]; present && truncated == true {
			t.Error("members_truncated = true at exactly the cap, want absent/false")
		}
	})

	t.Run("empty roster is an empty array", func(t *testing.T) {
		repo := &stubRosterRepo{roles: map[[2]uuid.UUID]string{{treeID, ownerID}: db.TreeRoleOwner}}
		h, _ := newDetailMembersHandler(ownerID, treeID, repo)

		rec := getTreeDetail(t, h, ownerID, treeID, "?include_members=true")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); !containsMembersEmptyArray(got) {
			t.Errorf("body = %s, want \"members\":[]", got)
		}
		body := decodeDetail(t, rec)
		if got, ok := intField(t, body, "total_members"); !ok || got != 0 {
			t.Errorf("total_members = %v (present=%v), want 0", got, ok)
		}
		// The roster wrapper must not swallow the detail body around it.
		if body["id"] != treeID.String() {
			t.Errorf("detail id = %v, want %s — the roster wrapper dropped the embedded detail", body["id"], treeID)
		}
		if _, ok := body["node_count"]; !ok {
			t.Errorf("detail body lost node_count: %v", body)
		}
	})
}

// containsMembersEmptyArray reports whether the body carries an empty members
// array. Kept as a helper so the assertion reads as one fact.
func containsMembersEmptyArray(body string) bool {
	return strings.Contains(body, `"members":[]`)
}

// TestGetTreeIncludeMembersParity is AC-3: the ids the detail roster carries
// equal those GET /trees/{id}/members returns for the same caller, in the same
// order.
func TestGetTreeIncludeMembersParity(t *testing.T) {
	ownerID, treeID, callerID := uuid.New(), uuid.New(), uuid.New()
	repo := &stubRosterRepo{
		roles: map[[2]uuid.UUID]string{{treeID, callerID}: db.TreeRoleMember},
		rows:  rosterRows(treeID, 5),
	}
	h, _ := newDetailMembersHandler(ownerID, treeID, repo)

	detailRec := getTreeDetail(t, h, callerID, treeID, "?include_members=true")
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200 (body: %s)", detailRec.Code, detailRec.Body.String())
	}
	detailIDs := memberIDs(t, decodeDetail(t, detailRec))

	membersRec := getMembers(t, h, callerID, treeID, "?limit=200")
	if membersRec.Code != http.StatusOK {
		t.Fatalf("members status = %d, want 200 (body: %s)", membersRec.Code, membersRec.Body.String())
	}
	var list listMembersResponse
	if err := json.NewDecoder(membersRec.Body).Decode(&list); err != nil {
		t.Fatalf("decode members page: %v", err)
	}
	pageIDs := make([]string, 0, len(list.Members))
	for _, m := range list.Members {
		pageIDs = append(pageIDs, m.ID.String())
	}

	if len(detailIDs) == 0 || len(pageIDs) == 0 {
		t.Fatalf("detail ids = %v, page ids = %v, want both populated", detailIDs, pageIDs)
	}
	if got, want := sortedCopy(detailIDs), sortedCopy(pageIDs); !equalStrings(got, want) {
		t.Errorf("detail member ids %v != /members ids %v", got, want)
	}
	if !equalStrings(detailIDs, pageIDs) {
		t.Errorf("detail roster order %v != /members order %v (ListByTree's joined_at ASC)", detailIDs, pageIDs)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGetTreeIncludeMembersVisibility pins §9.5 on the detail roster: owners
// and admins see hidden memberships, members and viewers do not.
func TestGetTreeIncludeMembersVisibility(t *testing.T) {
	ownerID, treeID := uuid.New(), uuid.New()

	t.Run("owner sees the full roster including hidden rows", func(t *testing.T) {
		repo := &stubRosterRepo{rows: rosterRows(treeID, 3)}
		h, _ := newDetailMembersHandler(ownerID, treeID, repo)

		rec := getTreeDetail(t, h, ownerID, treeID, "?include_members=true")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		body := decodeDetail(t, rec)
		if got := len(memberIDs(t, body)); got != 3 {
			t.Errorf("members = %d entries, want all 3 (owner sees hidden rows)", got)
		}
		if got, ok := intField(t, body, "total_members"); !ok || got != 3 {
			t.Errorf("total_members = %v (present=%v), want 3", got, ok)
		}
		if len(repo.gotHidden) != 1 || !repo.gotHidden[0] {
			t.Errorf("ListByTree includeHidden = %v, want [true] for the owner (§9.5)", repo.gotHidden)
		}
	})

	t.Run("admin membership also sees hidden rows", func(t *testing.T) {
		adminID := uuid.New()
		repo := &stubRosterRepo{roles: map[[2]uuid.UUID]string{{treeID, adminID}: db.TreeRoleAdmin}, rows: rosterRows(treeID, 3)}
		h, _ := newDetailMembersHandler(ownerID, treeID, repo)

		if rec := getTreeDetail(t, h, adminID, treeID, "?include_members=true"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if len(repo.gotHidden) != 1 || !repo.gotHidden[0] {
			t.Errorf("ListByTree includeHidden = %v, want [true] for an admin (§9.5)", repo.gotHidden)
		}
	})

	t.Run("caller without access gets 403 and the roster is never queried", func(t *testing.T) {
		stranger := uuid.New()
		repo := &stubRosterRepo{roles: map[[2]uuid.UUID]string{}, rows: rosterRows(treeID, 3)}
		h, _ := newDetailMembersHandler(ownerID, treeID, repo)

		rec := getTreeDetail(t, h, stranger, treeID, "?include_members=true")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
		}
		var body apiErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
		if body.Error.Code != "NOT_TREE_OWNER" {
			t.Errorf("code = %q, want NOT_TREE_OWNER", body.Error.Code)
		}
		if repo.listCalls != 0 {
			t.Errorf("ListByTree called %d times for a 403 caller, want 0 (no roster leak)", repo.listCalls)
		}
	})
}
