// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file contains PAG-002 integration tests: ListTrees cursor
// pagination through the real HTTP router against PostgreSQL.
package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// TestPAG002_TotalIsRealCount verifies that pagination.total reflects the
// real active-tree count, not the fetched window size (the proven failure
// from tick 288: limit=2 → total=3 instead of the real count).
func TestPAG002_TotalIsRealCount(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// Seed 5 trees via the API.
	for i := 0; i < 5; i++ {
		body := map[string]any{
			"title": "PAG-002 Count Test",
			"rootMessage": map[string]any{
				"content":       "root",
				"contentFormat": "markdown",
				"nodeType":      "message",
			},
		}
		req := authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/trees", body)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("create tree %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create tree %d: status=%d", i, resp.StatusCode)
		}
	}

	// GET /trees?limit=2 → total should be 5, not 3 (the old bug).
	req := authenticatedRequest(t, srv.URL, http.MethodGet, "/api/v1/trees?limit=2", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET trees: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET trees: status=%d", resp.StatusCode)
	}

	var list listTreesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if list.Pagination.Total != 5 {
		t.Fatalf("pagination.total = %d, want 5 (real active count, not window)", list.Pagination.Total)
	}
	if len(list.Trees) != 2 {
		t.Fatalf("trees = %d, want 2 (limit)", len(list.Trees))
	}
	if !list.Pagination.HasMore {
		t.Fatal("hasMore = false, want true (5 > limit 2)")
	}
}

// TestPAG002_TwoPageWalk verifies that page 2 with a cursor returns the
// correct trees — the proven failure was page 2 returning 0 trees.
func TestPAG002_TwoPageWalk(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// Seed 5 trees.
	var seededIDs []uuid.UUID
	for i := 0; i < 5; i++ {
		body := map[string]any{
			"title": "PAG-002 Walk Test",
			"rootMessage": map[string]any{
				"content":       "root",
				"contentFormat": "markdown",
				"nodeType":      "message",
			},
		}
		req := authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/trees", body)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("create tree %d: %v", i, err)
		}
		var tree struct {
			ID uuid.UUID `json:"id"`
		}
		json.NewDecoder(resp.Body).Decode(&tree)
		resp.Body.Close()
		seededIDs = append(seededIDs, tree.ID)
	}

	// Page 1.
	req := authenticatedRequest(t, srv.URL, http.MethodGet, "/api/v1/trees?limit=2", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET page1: %v", err)
	}
	defer resp.Body.Close()

	var page1 listTreesResponse
	if err := json.NewDecoder(resp.Body).Decode(&page1); err != nil {
		t.Fatalf("decode page1: %v", err)
	}
	if len(page1.Trees) != 2 {
		t.Fatalf("page1: %d trees, want 2", len(page1.Trees))
	}
	if page1.Pagination.NextCursor == nil {
		t.Fatal("page1 nextCursor is nil")
	}

	// Page 2: use the cursor from page 1.
	cursorStr := page1.Pagination.NextCursor.String()
	req2 := authenticatedRequest(t, srv.URL, http.MethodGet, "/api/v1/trees?limit=2&cursor="+cursorStr, nil)
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatalf("GET page2: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("page2: status=%d", resp2.StatusCode)
	}

	var page2 listTreesResponse
	if err := json.NewDecoder(resp2.Body).Decode(&page2); err != nil {
		t.Fatalf("decode page2: %v", err)
	}

	// THIS IS THE BUG PROOF: page 2 must NOT be empty.
	if len(page2.Trees) == 0 {
		t.Fatal("page2 returned 0 trees — the PAG-002 bug (cursor filter ran after LIMIT window)")
	}
	if len(page2.Trees) != 2 {
		t.Fatalf("page2: %d trees, want 2", len(page2.Trees))
	}

	// No overlap between page1 and page2.
	seen := make(map[uuid.UUID]bool)
	for _, ts := range page1.Trees {
		seen[ts.ID] = true
	}
	for _, ts := range page2.Trees {
		if seen[ts.ID] {
			t.Fatalf("tree %s appeared in both page 1 and page 2 (overlap)", ts.ID)
		}
		seen[ts.ID] = true
	}
}

// TestPAG002_ThreePageWalk walks 3 pages over 5 trees (limit=2) and asserts
// no dupes, no gaps, and eventually hasMore=false. This proves the cursor
// pagination works beyond the first two pages.
func TestPAG002_ThreePageWalk(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// Seed 5 trees.
	for i := 0; i < 5; i++ {
		body := map[string]any{
			"title": "PAG-002 Three Page Walk",
			"rootMessage": map[string]any{
				"content":       "root",
				"contentFormat": "markdown",
				"nodeType":      "message",
			},
		}
		req := authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/trees", body)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("create tree %d: %v", i, err)
		}
		resp.Body.Close()
	}

	var allIDs []uuid.UUID
	cursor := ""
	pageNum := 0

	for {
		pageNum++
		if pageNum > 10 {
			t.Fatal("too many pages, possible infinite loop")
		}

		url := "/api/v1/trees?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		req := authenticatedRequest(t, srv.URL, http.MethodGet, url, nil)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("page %d: %v", pageNum, err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("page %d: status=%d", pageNum, resp.StatusCode)
		}

		var page listTreesResponse
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			t.Fatalf("page %d decode: %v", pageNum, err)
		}
		resp.Body.Close()

		for _, ts := range page.Trees {
			allIDs = append(allIDs, ts.ID)
		}

		if !page.Pagination.HasMore {
			break
		}
		if page.Pagination.NextCursor == nil {
			t.Fatal("hasMore=true but nextCursor is nil")
		}
		cursor = page.Pagination.NextCursor.String()
	}

	if len(allIDs) != 5 {
		t.Fatalf("walked %d total IDs across all pages, want 5", len(allIDs))
	}

	// No dupes.
	seen := make(map[uuid.UUID]bool)
	for _, id := range allIDs {
		if seen[id] {
			t.Fatalf("duplicate ID %s in walk", id)
		}
		seen[id] = true
	}
}
