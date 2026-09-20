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

// TestDFHermesCanopy36_TreeScopedReadsAreMembershipGated pins the BEHAVIOUR of
// the tree-scoped reads that are mounted on this test router: a non-member is
// refused with NOT_TREE_MEMBER, an unauthenticated caller gets TOKEN_MISSING,
// the share grant makes the reads legal, and the owner is never locked out.
//
// Route-level proof for the surfaces this harness does not mount (export,
// graph) lives in internal/server/route_parity_test.go, which walks the REAL
// router; a green test here would otherwise prove nothing about production
// wiring. The gap this closes was found by the DF-HERMES-CANOPY-36 Tier 2
// judge: ExportTree's "Verify the requesting user owns this tree" comment sat
// above a check that only tested userID != Nil (any authenticated caller could
// export any tree — IDOR), and GET /graph/trees/{id}/stats had no per-tree
// authorization at all.
func TestDFHermesCanopy36_TreeScopedReadsAreMembershipGated(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "DF-HERMES-CANOPY-36 read gates")

	strangerEmail := fmt.Sprintf("df36b-%s@canopy.dev", tree.ID)
	stranger, err := db.NewPGUserRepo(pool).Create(context.Background(), &db.User{
		HermesUserID: "df36b-" + tree.ID.String(),
		Email:        &strangerEmail,
		DisplayName:  "DF36 Stranger",
	})
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}

	base := "/api/v1/trees/" + tree.ID.String()
	graphBase := "/api/v1/graph/trees/" + tree.ID.String()
	readPaths := []string{
		graphBase + "/stats",
		graphBase + "/subtree/" + tree.RootNodeID.String(),
		graphBase + "/ancestors/" + tree.RootNodeID.String(),
	}

	// 1. Non-member refused on every tree-scoped read.
	for _, p := range readPaths {
		req := apiRequest(t, srv.Server.URL, http.MethodGet, p, stranger.ID, nil)
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("stranger GET %s: %v", p, err)
		}
		var body apiErrorBody
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("stranger GET %s: status=%d error=%+v, want %d", p, resp.StatusCode, body, http.StatusForbidden)
		}
	}

	// 2. Unauthenticated callers still get TOKEN_MISSING.
	for _, p := range readPaths {
		req, err := http.NewRequest(http.MethodGet, srv.Server.URL+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("anon GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anon GET %s: status=%d, want %d", p, resp.StatusCode, http.StatusUnauthorized)
		}
	}

	// 3. The share grant makes the same reads legal for the new member.
	shareBody := map[string]any{"email": strangerEmail, "permission": "editor"}
	req := apiRequest(t, srv.Server.URL, http.MethodPost, base+"/share", ownerID, shareBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST share: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		var body apiErrorBody
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		t.Fatalf("POST share: status=%d error=%+v, want 201", resp.StatusCode, body)
	}
	resp.Body.Close()

	for _, p := range readPaths {
		req = apiRequest(t, srv.Server.URL, http.MethodGet, p, stranger.ID, nil)
		resp, err = srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("member GET %s: %v", p, err)
		}
		if resp.StatusCode != http.StatusOK {
			var body apiErrorBody
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			t.Fatalf("member GET %s: status=%d error=%+v, want 200", p, resp.StatusCode, body)
		}
		resp.Body.Close()
	}

	// 4. Owner never locked out of their own tree's reads.
	for _, p := range readPaths {
		req = apiRequest(t, srv.Server.URL, http.MethodGet, p, ownerID, nil)
		resp, err = srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("owner GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("owner GET %s: status=%d, want %d", p, resp.StatusCode, http.StatusOK)
		}
	}
}
