package main

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// OBS-1: the tree/node gauges must be written from a real DB query reachable
// from the canopyd entrypoint, and a test must exercise that DB-backed path —
// before this test, deleting refreshDomainCountGauges' SQL body (or the 30s
// ticker wiring in main) kept the whole suite green while the gauges regressed
// to the permanent zeros the observability audit found (D2).

func TestRefreshDomainCountGaugesWritesRealCounts(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a live PostgreSQL test pool")
	}
	pool := testutil.NewIntegrationPool(t)
	treeGauge, nodeGauge := testGauges()
	m := &telemetry.Metrics{TreeCount: treeGauge, NodeCount: nodeGauge}

	refreshDomainCountGauges(context.Background(), pool, m)
	beforeTrees := gaugeValue(t, treeGauge)
	beforeNodes := gaugeValue(t, nodeGauge)

	// Seed one row in each table, refresh again, and assert each gauge moved
	// by exactly +1 — proving the gauges come from THIS database, not a stub.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO trees (id, title, owner_id) VALUES (gen_random_uuid(), 'obs1-test-tree', '00000000-0000-0000-0000-000000000001')`); err != nil {
		t.Skipf("cannot seed trees row (schema drift?): %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO nodes (id, tree_id, author_id, content) VALUES (gen_random_uuid(), (SELECT id FROM trees ORDER BY created_at LIMIT 1), '00000000-0000-0000-0000-000000000001', 'obs1-test-node')`); err != nil {
		t.Skipf("cannot seed nodes row (schema drift?): %v", err)
	}

	refreshDomainCountGauges(context.Background(), pool, m)
	afterTrees := gaugeValue(t, treeGauge)
	afterNodes := gaugeValue(t, nodeGauge)

	if afterTrees != beforeTrees+1 {
		t.Fatalf("canopy_tree_count did not move by +1 after insert: before=%v after=%v", beforeTrees, afterTrees)
	}
	if afterNodes != beforeNodes+1 {
		t.Fatalf("canopy_node_count did not move by +1 after insert: before=%v after=%v", beforeNodes, afterNodes)
	}
}

func TestRefreshDomainCountGaugesNilArgsAreNoop(t *testing.T) {
	// A nil pool or nil metrics must not panic — the refresh is called from a
	// goroutine, so the defensive nil path is part of the contract.
	refreshDomainCountGauges(context.Background(), nil, nil)
}

// testGauges returns an UNREGISTERED gauge pair, so the test can read the
// exact numbers refreshDomainCountGauges wrote without colliding with the
// default Prometheus registry.
func testGauges() (prometheus.Gauge, prometheus.Gauge) {
	opts := prometheus.GaugeOpts{
		Name: "obs1_test_domain_count",
		Help: "test-only unregistered gauge pair for canopy_tree_count/canopy_node_count writes",
	}
	return prometheus.NewGauge(opts), prometheus.NewGauge(opts)
}

// gaugeValue reads a gauge's current value through the same client-model
// encoding /metrics serves, so the assertion is on the scraped number.
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("write gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}
