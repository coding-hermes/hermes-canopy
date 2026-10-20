package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// stubShareTreeService answers GetTree with a fixed tree so the share handler
// can be exercised without a database. The embedded interface keeps the stub
// minimal: any other method call panics, which is what a test wants.
type stubShareTreeService struct {
	service.TreeService
	tree *service.TreeDetail
}

func (s stubShareTreeService) GetTree(context.Context, uuid.UUID, service.GetTreeOptions) (*service.TreeDetail, error) {
	return s.tree, nil
}

// stubShareUserRepo resolves invitees from memory.
type stubShareUserRepo struct {
	db.UserRepo
	byEmail map[string]*db.User
	// byID resolves invitees for the user_id path (DF-HERMES-CANOPY-89). A nil
	// map is safe: an unknown id falls through to the byEmail scan, which is
	// what the DF-HERMES-CANOPY-87 subtests rely on.
	byID map[string]*db.User
}

func (s stubShareUserRepo) GetByEmail(_ context.Context, email string) (*db.User, error) {
	if u, ok := s.byEmail[strings.ToLower(email)]; ok {
		return u, nil
	}
	return nil, db.ErrNotFound
}

func (s stubShareUserRepo) GetByID(_ context.Context, id uuid.UUID) (*db.User, error) {
	if u, ok := s.byID[id.String()]; ok {
		return u, nil
	}
	for _, u := range s.byEmail {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, db.ErrNotFound
}

// stubShareMemberRepo records the membership the handler creates.
type stubShareMemberRepo struct {
	db.TreeMemberRepo
	added *db.TreeMember
}

func (s *stubShareMemberRepo) Add(_ context.Context, m *db.TreeMember) (*db.TreeMember, error) {
	m.ID = uuid.New()
	s.added = m
	return m, nil
}

// newShareTestHandler builds the share route over stubs, with the caller
// injected exactly as the auth middleware injects it in production.
func newShareTestHandler(ownerID uuid.UUID, treeID uuid.UUID, users db.UserRepo, members db.TreeMemberRepo) *TreeHandler {
	h := NewTreeHandler(stubShareTreeService{
		tree: &service.TreeDetail{TreeSummary: service.TreeSummary{ID: treeID, OwnerID: ownerID}},
	}, nil)
	return h.WithShares(users, members, nil)
}

// postShare drives POST /api/v1/trees/{tree_id}/share for ownerID and returns
// the recorded response.
func postShare(t *testing.T, h *TreeHandler, ownerID, treeID uuid.UUID, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal share body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/trees/"+treeID.String()+"/share", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tree_id", treeID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, userIDContextKey{}, ownerID)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.ShareTree(rec, req)
	return rec
}

// TestDFHermesCanopy87_ShareUnknownInviteeIsActionable pins the fix for
// DF-HERMES-CANOPY-87: sharing with a person who has never made an
// authenticated call used to answer 404 USER_NOT_FOUND, which reads like the
// tree or the route is missing. The status stays 404 (the invitee really is
// absent) but the code now names the condition and the message tells the
// operator what unblocks it.
func TestDFHermesCanopy87_ShareUnknownInviteeIsActionable(t *testing.T) {
	ownerID := uuid.New()
	treeID := uuid.New()

	t.Run("unknown email", func(t *testing.T) {
		h := newShareTestHandler(ownerID, treeID, stubShareUserRepo{byEmail: map[string]*db.User{}}, &stubShareMemberRepo{})
		rec := postShare(t, h, ownerID, treeID, map[string]any{
			"email":      "never-signed-in@example.com",
			"permission": "editor",
		})

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body.String())
		}
		var body apiErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
		if body.Error.Code != "INVITEE_NOT_REGISTERED" {
			t.Errorf("code = %q, want INVITEE_NOT_REGISTERED (was USER_NOT_FOUND before DF-HERMES-CANOPY-87)", body.Error.Code)
		}
		if strings.Contains(body.Error.Message, "USER_NOT_FOUND") {
			t.Errorf("message leaks the old opaque code: %q", body.Error.Message)
		}
		for _, want := range []string{"never-signed-in@example.com", "no account is registered", "must sign in"} {
			if !strings.Contains(body.Error.Message, want) {
				t.Errorf("message = %q, missing %q", body.Error.Message, want)
			}
		}
		if strings.TrimSpace(body.Error.Message) == "" {
			t.Error("message is empty — an empty message is not actionable")
		}
	})

	t.Run("unknown user_id", func(t *testing.T) {
		unknownID := uuid.New()
		h := newShareTestHandler(ownerID, treeID, stubShareUserRepo{byEmail: map[string]*db.User{}}, &stubShareMemberRepo{})
		rec := postShare(t, h, ownerID, treeID, map[string]any{
			"user_id":    unknownID.String(),
			"permission": "viewer",
		})

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body.String())
		}
		var body apiErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
		if body.Error.Code != "INVITEE_NOT_REGISTERED" {
			t.Errorf("code = %q, want INVITEE_NOT_REGISTERED", body.Error.Code)
		}
		if !strings.Contains(body.Error.Message, unknownID.String()) {
			t.Errorf("message = %q, want the user_id named", body.Error.Message)
		}
	})

	t.Run("known invitee still succeeds unchanged", func(t *testing.T) {
		email := "known@example.com"
		invitee := &db.User{ID: uuid.New(), HermesUserID: "known", Email: &email, DisplayName: "Known"}
		members := &stubShareMemberRepo{}
		h := newShareTestHandler(ownerID, treeID, stubShareUserRepo{byEmail: map[string]*db.User{email: invitee}}, members)

		rec := postShare(t, h, ownerID, treeID, map[string]any{"email": email, "permission": "editor"})
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		var got shareResponse
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatalf("decode share response: %v", err)
		}
		if got.UserID != invitee.ID.String() || got.TreeID != treeID.String() {
			t.Errorf("response = %+v, want treeId=%s userId=%s", got, treeID, invitee.ID)
		}
		if got.Email != email || got.Permission != "editor" {
			t.Errorf("response email/permission = %q/%q, want %q/editor", got.Email, got.Permission, email)
		}
		if got.MemberID == "" {
			t.Error("memberId is empty, want the created membership id")
		}
		if members.added == nil || members.added.UserID == nil || *members.added.UserID != invitee.ID {
			t.Errorf("membership user = %+v, want invitee %s", members.added, invitee.ID)
		}
		if members.added.Role != "member" {
			t.Errorf("stored role = %q, want member (editor maps to member)", members.added.Role)
		}
	})
}

// TestDFHermesCanopy89_ShareReachesProvisionedUserByUserID pins the server half
// of DF-HERMES-CANOPY-89. The Share dialog could only send `email`, and dev-mode
// provisioning (PGUserRepo.EnsureByID) creates every account with email NULL, so
// the only invite that can succeed on a fresh instance is one that names the
// invitee's UUID. These subtests assert:
//
//  1. the user_id path reaches an email-less account and returns 201,
//  2. the 201 envelope carries NO email key for such an invitee (the UI falls
//     back to the identifier the operator typed rather than printing "undefined"),
//  3. the email path cannot reach that same account and now names the user_id
//     remedy, instead of instructing a step (sign in once) that provisions the
//     email-less account and therefore changes nothing.
func TestDFHermesCanopy89_ShareReachesProvisionedUserByUserID(t *testing.T) {
	ownerID := uuid.New()
	treeID := uuid.New()

	// Exactly the shape dev-mode provisioning leaves behind: no email, forever.
	provisionedID := uuid.New()
	provisioned := &db.User{
		ID:           provisionedID,
		HermesUserID: provisionedID.String(),
		DisplayName:  "Hermes user " + provisionedID.String(),
	}
	users := stubShareUserRepo{byID: map[string]*db.User{provisionedID.String(): provisioned}}

	t.Run("user_id invite reaches an email-less account", func(t *testing.T) {
		members := &stubShareMemberRepo{}
		h := newShareTestHandler(ownerID, treeID, users, members)

		rec := postShare(t, h, ownerID, treeID, map[string]any{
			"user_id":    provisionedID.String(),
			"permission": "editor",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		var got shareResponse
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatalf("decode share response: %v", err)
		}
		if got.UserID != provisionedID.String() {
			t.Errorf("userId = %q, want the invitee %q", got.UserID, provisionedID)
		}
		if got.TreeID != treeID.String() {
			t.Errorf("treeId = %q, want %q", got.TreeID, treeID)
		}
		if got.Permission != "editor" {
			t.Errorf("permission = %q, want editor", got.Permission)
		}
		if got.MemberID == "" {
			t.Error("memberId is empty, want the created membership id")
		}
		// email is omitempty, so the email-less invitee must not put a null or
		// an empty string on the wire — the dialog's fallback covers it.
		if got.Email != "" || strings.Contains(rec.Body.String(), `"email"`) {
			t.Errorf("201 body carries an email for an email-less invitee: %s", rec.Body.String())
		}
		if members.added == nil || members.added.UserID == nil || *members.added.UserID != provisionedID {
			t.Fatalf("membership user = %+v, want invitee %s", members.added, provisionedID)
		}
		if members.added.Role != db.TreeRoleMember {
			t.Errorf("stored role = %q, want %q", members.added.Role, db.TreeRoleMember)
		}
		if members.added.InvitedBy == nil || *members.added.InvitedBy != ownerID {
			t.Errorf("invitedBy = %v, want the sharing owner %s", members.added.InvitedBy, ownerID)
		}
	})

	t.Run("viewer permission maps to the viewer role", func(t *testing.T) {
		members := &stubShareMemberRepo{}
		h := newShareTestHandler(ownerID, treeID, users, members)

		rec := postShare(t, h, ownerID, treeID, map[string]any{
			"user_id":    provisionedID.String(),
			"permission": "viewer",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		if members.added == nil || members.added.Role != db.TreeRoleViewer {
			t.Errorf("stored role = %+v, want %q (viewer is least privilege)", members.added, db.TreeRoleViewer)
		}
	})

	t.Run("email path still works for a user that has an email", func(t *testing.T) {
		email := "dev@canopy.dev"
		seeded := &db.User{ID: uuid.New(), HermesUserID: "dev", Email: &email, DisplayName: "Dev"}
		members := &stubShareMemberRepo{}
		h := newShareTestHandler(ownerID, treeID,
			stubShareUserRepo{
				byEmail: map[string]*db.User{email: seeded},
				byID:    map[string]*db.User{seeded.ID.String(): seeded},
			}, members)

		rec := postShare(t, h, ownerID, treeID, map[string]any{"email": email, "permission": "admin"})
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		var got shareResponse
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatalf("decode share response: %v", err)
		}
		if got.Email != email || got.UserID != seeded.ID.String() {
			t.Errorf("response = %+v, want email %q / userId %s", got, email, seeded.ID)
		}
		if members.added == nil || members.added.Role != db.TreeRoleAdmin {
			t.Errorf("stored role = %+v, want %q", members.added, db.TreeRoleAdmin)
		}
	})

	t.Run("email invite names the user_id remedy when the account has no email", func(t *testing.T) {
		h := newShareTestHandler(ownerID, treeID, users, &stubShareMemberRepo{})

		rec := postShare(t, h, ownerID, treeID, map[string]any{
			"email":      "lexi.dogfood@example.com",
			"permission": "viewer",
		})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body.String())
		}
		var body apiErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
		if body.Error.Code != "INVITEE_NOT_REGISTERED" {
			t.Errorf("code = %q, want INVITEE_NOT_REGISTERED", body.Error.Code)
		}
		if !strings.Contains(body.Error.Message, "user UUID") {
			t.Errorf("email-path 404 message = %q, want it to name the user UUID remedy", body.Error.Message)
		}
		// DF-HERMES-CANOPY-87's wording must survive: the message still has to
		// say an account is missing and that signing in is what creates it.
		for _, want := range []string{"no account is registered", "must sign in"} {
			if !strings.Contains(body.Error.Message, want) {
				t.Errorf("message = %q, missing DF-87 phrase %q", body.Error.Message, want)
			}
		}
	})

	t.Run("user_id path does not claim signing in is the remedy", func(t *testing.T) {
		unknownID := uuid.New()
		h := newShareTestHandler(ownerID, treeID, users, &stubShareMemberRepo{})

		rec := postShare(t, h, ownerID, treeID, map[string]any{"user_id": unknownID.String()})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body.String())
		}
		var body apiErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
		if !strings.Contains(body.Error.Message, unknownID.String()) {
			t.Errorf("message = %q, want the user_id named", body.Error.Message)
		}
		if strings.Contains(body.Error.Message, "user UUID instead") {
			t.Errorf("user_id 404 offers the user_id remedy to a user_id caller: %q", body.Error.Message)
		}
	})
}
