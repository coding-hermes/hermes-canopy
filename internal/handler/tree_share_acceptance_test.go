// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file pins the shared-tree read and write authorization contract.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// TestDFHermesCanopy36_SharedEditorTreeAccess verifies that sharing a tree
// grants read and node-create access without granting tree-level mutations.
func TestDFHermesCanopy36_SharedEditorTreeAccess(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "DF-HERMES-CANOPY-36 shared tree")

	inviteeEmail := fmt.Sprintf("df36-%s@canopy.dev", tree.ID)
	userRepo := db.NewPGUserRepo(pool)
	invitee, err := userRepo.Create(context.Background(), &db.User{
		HermesUserID: "df36-" + tree.ID.String(),
		Email:        &inviteeEmail,
		DisplayName:  "DF36 Invitee",
	})
	if err != nil {
		t.Fatalf("create invitee: %v", err)
	}

	// Before sharing, the invitee must not be able to read the tree.
	getPath := "/api/v1/trees/" + tree.ID.String()
	req := apiRequest(t, srv.Server.URL, http.MethodGet, getPath, invitee.ID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("pre-share GET tree: %v", err)
	}
	var preShareErr apiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&preShareErr); err != nil {
		resp.Body.Close()
		t.Fatalf("decode pre-share error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pre-share GET tree: status=%d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if preShareErr.Error.Message == "you do not own this tree" {
		t.Fatal("pre-share GET still reports the misleading owner-only message")
	}

	// The owner grants editor access through the real share route.
	shareBody := map[string]any{
		"email":      inviteeEmail,
		"permission": "editor",
	}
	req = apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/trees/"+tree.ID.String()+"/share", ownerID, shareBody)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST share: %v", err)
	}
	var shareErr apiErrorBody
	if resp.StatusCode != http.StatusCreated {
		_ = json.NewDecoder(resp.Body).Decode(&shareErr)
		resp.Body.Close()
		t.Fatalf("POST share: status=%d, error=%+v", resp.StatusCode, shareErr)
	}
	resp.Body.Close()

	// Membership grants tree reads.
	req = apiRequest(t, srv.Server.URL, http.MethodGet, getPath, invitee.ID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("post-share GET tree: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		var body apiErrorBody
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		t.Fatalf("post-share GET tree: status=%d, error=%+v", resp.StatusCode, body)
	}
	resp.Body.Close()

	// The existing tree-scoped node membership path allows the editor to write
	// nodes; this assertion pins the complete share → read → write flow.
	nodeBody := map[string]any{
		"content":   "editor node",
		"parent_id": tree.RootNodeID.String(),
	}
	req = apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/trees/"+tree.ID.String()+"/nodes", invitee.ID, nodeBody)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("editor POST node: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		var body apiErrorBody
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		t.Fatalf("editor POST node: status=%d, error=%+v", resp.StatusCode, body)
	}
	resp.Body.Close()

	// Tree-level mutations remain owner-only, even for an editor member.
	req = apiRequest(t, srv.Server.URL, http.MethodPatch, getPath, invitee.ID,
		map[string]any{"title": "editor must not rename"})
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("editor PATCH tree: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		resp.Body.Close()
		t.Fatalf("editor PATCH tree: status=%d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	resp.Body.Close()

	req = apiRequest(t, srv.Server.URL, http.MethodDelete, getPath, invitee.ID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("editor DELETE tree: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		resp.Body.Close()
		t.Fatalf("editor DELETE tree: status=%d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	resp.Body.Close()

	// Sharing did not disturb owner access.
	req = apiRequest(t, srv.Server.URL, http.MethodGet, getPath, ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("owner GET tree: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("owner GET tree: status=%d, want %d", resp.StatusCode, http.StatusOK)
	}
	resp.Body.Close()
}
