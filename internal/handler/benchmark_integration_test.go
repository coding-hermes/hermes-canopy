package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ---------------------------------------------------------------------------
// INT-05: Performance Baseline
//
// Measures API throughput and latency under load. These are integration
// benchmarks and require a running PostgreSQL at CANOPY_DATABASE_URL (default
// localhost:5437). Tests are skipped under -short.
// ---------------------------------------------------------------------------

func TestINT05_2000NodeTree(t *testing.T) {
	if testing.Short() {
		t.Skip("perf benchmark — full 2000-node run only in non-short mode (Tick 191: -short target <60s)")
	}
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// Create a tree with root message.
	tree := createBenchTree(t, srv, pool)
	t.Logf("created tree: %s", tree.ID)

	// Create 2000 nodes as children of the tree root. Under `-short`
	// (the gitreins guard runs `go test -short`), use a reduced count —
	// the full 2000-node run takes 2.5-3+ min solo (75ms+/node on a
	// busy DB from leaked test databases) and blows guard package
	// timeouts when the whole suite runs in parallel. Full count is
	// preserved for non-short runs (CI, dedicated benchmark ticks).
	const fullNodeCount = 2000
	nodeCount := fullNodeCount
	if testing.Short() {
		nodeCount = 300
		t.Logf("short mode: creating %d nodes (full benchmark = %d)", nodeCount, fullNodeCount)
	}
	start := time.Now()
	rootNodeID := tree.RootNodeID
	for i := 0; i < nodeCount; i++ {
		parentID := rootNodeID

		createBody := map[string]any{
			"parent_id":      parentID.String(),
			"content":        fmt.Sprintf("Benchmark node %d", i),
			"content_format": "markdown",
			"node_type":      "message",
		}
		req := authenticatedRequest(t, srv.URL, http.MethodPost,
			fmt.Sprintf("/api/v1/nodes/%s/nodes", tree.ID), createBody)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("create node %d: %v", i, err)
		}
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("create node %d: status=%d, body=%s", i, resp.StatusCode, string(body))
		}
		resp.Body.Close()
	}
	elapsed := time.Since(start)
	avgPerNode := elapsed / time.Duration(nodeCount)
	t.Logf("Created %d nodes in %v (avg %v per node)", nodeCount, elapsed, avgPerNode)

	if avgPerNode > 100*time.Millisecond {
		t.Errorf("Average node creation too slow: %v per node (threshold: 100ms)", avgPerNode)
	}

	// Measure GET node latency.
	var getDurations []time.Duration
	for i := 0; i < 5; i++ {
		req := authenticatedRequest(t, srv.URL, http.MethodGet,
			fmt.Sprintf("/api/v1/nodes/%s/nodes/%s", tree.ID, tree.RootNodeID), nil)
		start := time.Now()
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("GET node %d: %v", i, err)
		}
		resp.Body.Close()
		getDurations = append(getDurations, time.Since(start))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET node %d: status=%d", i, resp.StatusCode)
		}
	}

	sort.Slice(getDurations, func(i, j int) bool {
		return getDurations[i] < getDurations[j]
	})
	p50 := getDurations[len(getDurations)/2]
	t.Logf("GET node p50 latency: %v", p50)
}

func TestINT05_LatencyP99(t *testing.T) {
	if testing.Short() {
		t.Skip("perf benchmark — full run only in non-short mode (Tick 191: -short target <60s)")
	}
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// Create a tree to have data.
	_ = createBenchTree(t, srv, pool)

	// Measure GET /api/v1/trees latency across samples.
	const samples = 50
	var latencies []time.Duration

	for i := 0; i < samples; i++ {
		req := authenticatedRequest(t, srv.URL, http.MethodGet, "/api/v1/trees", nil)
		start := time.Now()
		r, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("GET trees %d: %v", i, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("GET trees %d: status=%d", i, r.StatusCode)
		}
		latencies = append(latencies, time.Since(start))
	}

	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	p50 := latencies[samples/2]
	p95 := latencies[int(math.Ceil(float64(samples)*0.95))-1]
	p99 := latencies[int(math.Ceil(float64(samples)*0.99))-1]
	maxLat := latencies[samples-1]

	t.Logf("GET /api/v1/trees latency (n=%d):", samples)
	t.Logf("  p50:  %v", p50)
	t.Logf("  p95:  %v", p95)
	t.Logf("  p99:  %v", p99)
	t.Logf("  max:  %v", maxLat)

	const threshold = 2 * time.Second
	if p99 > threshold {
		t.Errorf("p99 latency %v exceeds threshold %v", p99, threshold)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// createBenchTree creates a tree with a root message via HTTP.
func createBenchTree(t *testing.T, srv *httptest.Server, pool *pgxpool.Pool) *service.Tree {
	t.Helper()

	// Create a test user so JWT sub resolves.
	ctx := context.Background()
	userRepo := db.NewPGUserRepo(pool)
	userID := uuid.MustParse("c0000000-0000-0000-0000-000000000001")
	_, err := userRepo.Create(ctx, &db.User{
		ID:           userID,
		HermesUserID: userID.String(),
		DisplayName:  "INT-05 Test User",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	createBody := map[string]any{
		"title":       "INT-05 Benchmark Tree",
		"description": "Performance benchmark tree",
		"rootMessage": map[string]any{
			"content":       "Root node",
			"contentFormat": "markdown",
			"nodeType":      "message",
		},
	}
	req := authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/trees", createBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("create tree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create tree: status=%d, body=%s", resp.StatusCode, string(body))
	}

	var tree service.Tree
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatalf("decode tree: %v", err)
	}
	return &tree
}
