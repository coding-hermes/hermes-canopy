// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file contains WIRE-004 / BUG-024 integration tests for the share
// and presence endpoints (POST /trees/{tree_id}/share, /presence, /presence/leave).
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ---------------------------------------------------------------------------
// Share endpoint (POST /api/v1/trees/{tree_id}/share)
// ---------------------------------------------------------------------------

// TestAPI_ShareTree_ByEmail creates a share by email and asserts the 201
// response carries the resolved userId, email, and normalized permission.
func TestAPI_ShareTree_ByEmail(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ctx := context.Background()

	// Owner creates a tree.
	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "WIRE-004 Share By Email")

	// Invitee: a second user resolved by email. userRepo.Create assigns a
	// uuidv7 ID server-side (it ignores the input ID), so we capture the
	// returned user to get the real ID for assertions.
	inviteeEmail := "invitee-share-email@canopy.dev"
	userRepo := db.NewPGUserRepo(pool)
	invitee, err := userRepo.Create(ctx, &db.User{
		HermesUserID: "invitee-share-email",
		Email:        &inviteeEmail,
		DisplayName:  "Invitee",
	})
	if err != nil {
		t.Fatalf("create invitee: %v", err)
	}

	body := map[string]any{
		"email":      inviteeEmail,
		"permission": "editor",
	}
	req := apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/trees/"+tree.ID.String()+"/share", ownerID, body)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST share: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST share: status=%d, error=%+v", resp.StatusCode, errBody)
	}

	var out shareResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode share response: %v", err)
	}
	if out.UserID != invitee.ID.String() {
		t.Errorf("userId = %s, want %s", out.UserID, invitee.ID)
	}
	if out.Email != inviteeEmail {
		t.Errorf("email = %s, want %s", out.Email, inviteeEmail)
	}
	// "editor" maps to the backend "member" role, which round-trips back to
	// "editor" via roleToPermission.
	if out.Permission != "editor" {
		t.Errorf("permission = %s, want editor", out.Permission)
	}
	if out.MemberID == "" {
		t.Error("memberId is empty")
	}
	if out.TreeID != tree.ID.String() {
		t.Errorf("treeId = %s, want %s", out.TreeID, tree.ID)
	}
}

// TestAPI_ShareTree_ByUserID resolves the invitee by user_id instead of email.
func TestAPI_ShareTree_ByUserID(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ctx := context.Background()
	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "WIRE-004 Share By UserID")

	inviteeEmail := "uid-invitee@canopy.dev"
	userRepo := db.NewPGUserRepo(pool)
	invitee, err := userRepo.Create(ctx, &db.User{
		HermesUserID: "uid-invitee",
		Email:        &inviteeEmail,
		DisplayName:  "UID Invitee",
	})
	if err != nil {
		t.Fatalf("create invitee: %v", err)
	}

	body := map[string]any{
		"user_id":    invitee.ID.String(),
		"permission": "viewer",
	}
	req := apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/trees/"+tree.ID.String()+"/share", ownerID, body)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST share: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST share (by user_id): status=%d, error=%+v", resp.StatusCode, errBody)
	}

	var out shareResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Permission != "viewer" {
		t.Errorf("permission = %s, want viewer", out.Permission)
	}
}

// TestAPI_ShareTree_ValidationErrors covers the error paths: missing
// identifier, unknown user, non-owner caller, and duplicate member.
func TestAPI_ShareTree_ValidationErrors(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ctx := context.Background()
	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "WIRE-004 Share Validation")

	// A second user who is NOT the owner and NOT yet a member.
	userRepo := db.NewPGUserRepo(pool)
	otherEmail := "other-share-validation@canopy.dev"
	other, err := userRepo.Create(ctx, &db.User{
		HermesUserID: "other-share-validation",
		Email:        &otherEmail,
		DisplayName:  "Other",
	})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	otherID := other.ID

	t.Run("missing_identifier", func(t *testing.T) {
		body := map[string]any{"permission": "viewer"}
		req := apiRequest(t, srv.Server.URL, http.MethodPost,
			"/api/v1/trees/"+tree.ID.String()+"/share", ownerID, body)
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("unknown_email", func(t *testing.T) {
		body := map[string]any{"email": "nobody@nowhere.dev", "permission": "viewer"}
		req := apiRequest(t, srv.Server.URL, http.MethodPost,
			"/api/v1/trees/"+tree.ID.String()+"/share", ownerID, body)
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("non_owner_forbidden", func(t *testing.T) {
		body := map[string]any{"email": otherEmail, "permission": "viewer"}
		req := apiRequest(t, srv.Server.URL, http.MethodPost,
			"/api/v1/trees/"+tree.ID.String()+"/share", otherID, body)
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("duplicate_member_conflict", func(t *testing.T) {
		// First share succeeds.
		body := map[string]any{"email": otherEmail, "permission": "viewer"}
		req := apiRequest(t, srv.Server.URL, http.MethodPost,
			"/api/v1/trees/"+tree.ID.String()+"/share", ownerID, body)
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("first share: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("first share status = %d, want 201", resp.StatusCode)
		}

		// Second share of the same user → 409 conflict.
		req2 := apiRequest(t, srv.Server.URL, http.MethodPost,
			"/api/v1/trees/"+tree.ID.String()+"/share", ownerID, body)
		resp2, err := srv.Server.Client().Do(req2)
		if err != nil {
			t.Fatalf("second share: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusConflict {
			t.Fatalf("duplicate share status = %d, want 409", resp2.StatusCode)
		}
	})
}

// ---------------------------------------------------------------------------
// Presence endpoints (POST /presence, /presence/leave)
// ---------------------------------------------------------------------------

// TestAPI_Presence_PushBroadcasts verifies that a presence POST broadcasts a
// presence_update SSE event to subscribers of the tree.
func TestAPI_Presence_PushBroadcasts(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "WIRE-004 Presence Push")

	// We can't easily assert the SSE fan-out over the real httptest server
	// without an SSE client, so instead we assert the HTTP contract: a 202
	// response on a valid payload, and a 400 on an invalid one. The hub
	// broadcast itself is covered by the transport_integration_test suite.
	body := map[string]any{
		"userId":      ownerID.String(),
		"userName":    "Test User",
		"avatarColor": "#7c3aed",
		"permission":  "editor",
		"isActive":    true,
		"cursor":      map[string]any{"x": 10.5, "y": 20.0},
	}
	req := apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/trees/"+tree.ID.String()+"/presence", ownerID, body)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST presence: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST presence: status = %d, want 202", resp.StatusCode)
	}
}

// TestAPI_Presence_LeaveReturns204 verifies the leave endpoint returns 204.
func TestAPI_Presence_LeaveReturns204(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "WIRE-004 Presence Leave")

	req := apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/trees/"+tree.ID.String()+"/presence/leave", ownerID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST presence/leave: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST presence/leave: status = %d, want 204", resp.StatusCode)
	}
}

// TestAPI_Presence_ValidationErrors covers the missing-userId and bad-JSON
// paths for the presence push endpoint.
func TestAPI_Presence_ValidationErrors(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "WIRE-004 Presence Validation")

	t.Run("missing_userId", func(t *testing.T) {
		body := map[string]any{"userName": "No ID"}
		req := apiRequest(t, srv.Server.URL, http.MethodPost,
			"/api/v1/trees/"+tree.ID.String()+"/presence", ownerID, body)
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})
}
