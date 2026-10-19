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
}

func (s stubShareUserRepo) GetByEmail(_ context.Context, email string) (*db.User, error) {
	if u, ok := s.byEmail[strings.ToLower(email)]; ok {
		return u, nil
	}
	return nil, db.ErrNotFound
}

func (s stubShareUserRepo) GetByID(_ context.Context, id uuid.UUID) (*db.User, error) {
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
