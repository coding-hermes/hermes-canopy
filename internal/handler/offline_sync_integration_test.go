// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file contains INT-04: Offline sync integration tests
// exercising the offline → edit → reconnect → merge flow.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sync"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ---------------------------------------------------------------------------
// INT-04: Offline sync integration
//
// Covers the offline → edit → reconnect → merge flow:
//   1. Create a tree with nodes (online state)
//   2. Capture the snapshot hash (client's last-known state)
//   3. Simulate offline edits by adding nodes via HTTP
//   4. Compute delta to verify offline edits are captured
//   5. Verify full sync for new clients (no prior hash)
//   6. Verify no-op delta for up-to-date clients
// ---------------------------------------------------------------------------

// TestINT04_OfflineSync verifies the sync engine correctly computes
// deltas when a client goes offline, edits occur, and it reconnects.
func TestINT04_OfflineSync(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	ensureTestUser(t, pool)

	// ── Step 1: Create a tree with a root node (online) ─────────────
	tree := createTestTree(t, srv)
	t.Logf("step 1 — tree created: %s, root=%s", tree.ID, tree.RootNodeID)

	// ── Step 2: Add child nodes (online) ────────────────────────────
	nodeBody1 := map[string]any{
		"content":        "First child (created online)",
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      tree.RootNodeID.String(),
	}
	req := approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes",
		srv.UserID, nodeBody1)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 2 — POST child 1: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("step 2 — child 1: status=%d, body=%s", resp.StatusCode, string(body))
	}
	var cr1 service.CreateNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&cr1); err != nil {
		t.Fatalf("step 2 — decode child 1: %v", err)
	}
	t.Logf("step 2 ✓ child 1 created: id=%s", cr1.Node.ID)

	nodeBody2 := map[string]any{
		"content":        "Second child (created online)",
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      tree.RootNodeID.String(),
	}
	req = approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes",
		srv.UserID, nodeBody2)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 2 — POST child 2: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("step 2 — child 2: status=%d, body=%s", resp.StatusCode, string(body))
	}
	var cr2 service.CreateNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&cr2); err != nil {
		t.Fatalf("step 2 — decode child 2: %v", err)
	}
	t.Logf("step 2 ✓ child 2 created: id=%s", cr2.Node.ID)

	// ── Step 3: Get latest snapshot hash (client's last-known state) ─
	eventRepo := db.NewEventRepo(pool)
	snapshotRepo := db.NewSnapshotRepo(pool)
	sseHub := sse.NewHub()
	engine := sync.NewEngine(eventRepo, snapshotRepo, sseHub, sync.DefaultEngineConfig())

	ctx := context.Background()
	initialSnapshot, err := engine.GetLatestSnapshot(ctx, tree.ID)
	if err != nil {
		t.Fatalf("step 3 — get initial snapshot: %v", err)
	}
	if initialSnapshot == nil {
		t.Fatal("step 3 — initial snapshot is nil (no snapshot created)")
	}
	lastKnownHash := initialSnapshot.Hash
	t.Logf("step 3 ✓ client last-known hash: %s (nodes=%d, edges=%d)",
		lastKnownHash, initialSnapshot.NodeCount, initialSnapshot.EdgeCount)

	// ── Step 4: Simulate offline edits ──────────────────────────────
	// Client doesn't know about these edits — they happen "while offline".
	nodeBody3 := map[string]any{
		"content":        "Edited while client was offline",
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      tree.RootNodeID.String(),
	}
	req = approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes",
		srv.UserID, nodeBody3)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 4 — POST offline edit 1: %v", err)
	}
	resp.Body.Close()

	nodeBody4 := map[string]any{
		"content":        "Second offline edit",
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      tree.RootNodeID.String(),
	}
	req = approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes",
		srv.UserID, nodeBody4)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 4 — POST offline edit 2: %v", err)
	}
	resp.Body.Close()

	// Update child 1's content (simulating edit while offline).
	updateBody := map[string]any{
		"content":        "Child updated (offline edit)",
		"content_format": "markdown",
	}
	req = approvalRequest(t, srv.Server.URL, http.MethodPatch,
		"/api/v1/nodes/nodes/"+cr1.Node.ID.String(),
		srv.UserID, updateBody)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 4 — PATCH node: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 4 — PATCH: status=%d, want 200", resp.StatusCode)
	}
	t.Log("step 4 ✓ offline edits applied (2 nodes added, 1 node updated)")

	// ── Step 5: Client reconnects and computes delta ────────────────
	delta, err := engine.ComputeDeltaForClient(ctx, tree.ID, lastKnownHash)
	if err != nil {
		t.Fatalf("step 5 — compute delta: %v", err)
	}
	t.Logf("step 5 ✓ delta: from=%s…, to=%s…, added=%d, changed=%d",
		delta.FromHash[:min(len(delta.FromHash), 12)],
		delta.ToHash[:min(len(delta.ToHash), 12)],
		len(delta.AddedNodes), len(delta.ChangedNodes))

	if len(delta.AddedNodes) < 2 {
		t.Errorf("step 5 — expected ≥2 added nodes, got %d", len(delta.AddedNodes))
	}
	if delta.FromHash != lastKnownHash {
		t.Errorf("step 5 — fromHash mismatch")
	}
	if delta.ToHash == lastKnownHash {
		t.Errorf("step 5 — toHash should differ after edits")
	}
	if len(delta.ChangedNodes) < 1 {
		t.Errorf("step 5 — expected ≥1 changed node, got %d", len(delta.ChangedNodes))
	}

	// ── Step 6: New client (no prior hash) gets full sync ───────────
	fullDelta, err := engine.ComputeDeltaForClient(ctx, tree.ID, "")
	if err != nil {
		t.Fatalf("step 6 — compute full delta: %v", err)
	}
	t.Logf("step 6 ✓ full delta: added_nodes=%d, node_count=%d",
		len(fullDelta.AddedNodes), fullDelta.NodeCount)

	if len(fullDelta.AddedNodes) == 0 {
		t.Error("step 6 — full delta should have added nodes")
	}
	if fullDelta.FromHash != "" {
		t.Errorf("step 6 — fromHash should be empty, got %q", fullDelta.FromHash)
	}
	if fullDelta.ToHash == "" {
		t.Error("step 6 — toHash should not be empty")
	}

	// ── Step 7: No-op delta (client already up-to-date) ─────────────
	latestSnapshot, err := engine.GetLatestSnapshot(ctx, tree.ID)
	if err != nil {
		t.Fatalf("step 7 — get latest snapshot: %v", err)
	}
	noopDelta, err := engine.ComputeDeltaForClient(ctx, tree.ID, latestSnapshot.Hash)
	if err != nil {
		t.Fatalf("step 7 — compute no-op delta: %v", err)
	}
	t.Logf("step 7 ✓ no-op delta: added=%d", len(noopDelta.AddedNodes))

	if len(noopDelta.AddedNodes) != 0 {
		t.Errorf("step 7 — no-op delta should have 0 added nodes, got %d",
			len(noopDelta.AddedNodes))
	}
	if noopDelta.ToHash != latestSnapshot.Hash {
		t.Errorf("step 7 — toHash mismatch: %s vs %s",
			noopDelta.ToHash, latestSnapshot.Hash)
	}

	t.Log("INT-04 offline sync integration: ALL PASS")
}

// TestINT04_SyncEngineDirect tests the sync engine directly (without HTTP),
// verifying edge cases: missing snapshot fallback, mutation chain integrity.
func TestINT04_SyncEngineDirect(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	ensureTestUser(t, pool)
	ctx := context.Background()

	eventRepo := db.NewEventRepo(pool)
	snapshotRepo := db.NewSnapshotRepo(pool)
	engine := sync.NewEngine(eventRepo, snapshotRepo, nil, sync.DefaultEngineConfig())

	// Test 1: Unknown hash returns full delta (compacted-away fallback)
	tree := createTestTree(t, srv)

	delta, err := engine.ComputeDeltaForClient(ctx, tree.ID, "nonexistent-hash")
	if err != nil {
		t.Fatalf("test 1 — compute delta with bad hash: %v", err)
	}
	if len(delta.AddedNodes) == 0 {
		t.Error("test 1 — expected added nodes as fallback for unknown hash")
	}
	t.Logf("test 1 ✓ unknown hash fallback: %d nodes (full sync)", len(delta.AddedNodes))

	// Test 2: Add nodes and verify final snapshot
	for i := 0; i < 3; i++ {
		nodeBody := map[string]any{
			"content":        fmt.Sprintf("Batch edit #%d", i+1),
			"content_format": "markdown",
			"node_type":      "message",
			"parent_id":      tree.RootNodeID.String(),
		}
		req := approvalRequest(t, srv.Server.URL, http.MethodPost,
			"/api/v1/nodes/"+tree.ID.String()+"/nodes",
			srv.UserID, nodeBody)
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("test 2 — POST node %d: %v", i+1, err)
		}
		resp.Body.Close()
	}

	latestSnap, err := engine.GetLatestSnapshot(ctx, tree.ID)
	if err != nil {
		t.Fatalf("test 2 — get latest snapshot: %v", err)
	}
	if latestSnap == nil {
		t.Fatal("test 2 — latest snapshot is nil")
	}
	t.Logf("test 2 ✓ final snapshot: hash=%s…, nodes=%d, edges=%d",
		latestSnap.Hash[:min(len(latestSnap.Hash), 12)],
		latestSnap.NodeCount, latestSnap.EdgeCount)

	if latestSnap.NodeCount < 4 {
		t.Errorf("test 2 — expected ≥4 nodes (1 root + ≥3 children), got %d",
			latestSnap.NodeCount)
	}

	t.Log("INT-04 sync engine direct: ALL PASS")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
