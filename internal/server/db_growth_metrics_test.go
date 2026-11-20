package server

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/coding-hermes/hermes-canopy/internal/relay"
	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
)

// gatherFamily returns the gathered metric family named name, or nil when the
// series is absent from the registry (which is the expected state for the cert
// gauge when no certificate is configured).
func gatherFamily(t *testing.T, reg *prometheus.Registry, name string) *dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	return nil
}

// TestDBLargestTableRowsAllowlist pins the label domain: exactly five distinct
// names, all non-empty, and every one of them a real table in the PostgreSQL
// schema. A typo here would mint a permanently-zero series that looks like a
// real table that lost its data.
func TestDBLargestTableRowsAllowlistHasFiveDistinctTables(t *testing.T) {
	if len(dbGrowthTables) != 5 {
		t.Fatalf("dbGrowthTables has %d entries, want exactly 5: %v", len(dbGrowthTables), dbGrowthTables)
	}
	seen := make(map[string]bool, len(dbGrowthTables))
	for _, table := range dbGrowthTables {
		if strings.TrimSpace(table) == "" {
			t.Fatalf("dbGrowthTables contains an empty name: %v", dbGrowthTables)
		}
		if seen[table] {
			t.Fatalf("dbGrowthTables contains duplicate %q: %v", table, dbGrowthTables)
		}
		seen[table] = true
	}
}

// TestDBLargestTableRowsAllowlistTablesExistInSchema is the "confirm each one
// actually exists" half of the brief: it reads the real migrations rather than
// trusting the list. Only the PostgreSQL migrations count — the card-export
// SQLite schema has its own table names.
func TestDBLargestTableRowsAllowlistTablesExistInSchema(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("no PostgreSQL migrations found under ../../migrations; cannot verify the allowlist")
	}
	var schema strings.Builder
	for _, path := range matches {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		schema.Write(content)
		schema.WriteString("\n")
	}
	for _, table := range dbGrowthTables {
		pattern := regexp.MustCompile(`(?i)CREATE TABLE (IF NOT EXISTS )?` + regexp.QuoteMeta(table) + `\s*\(`)
		if !pattern.MatchString(schema.String()) {
			t.Errorf("allowlisted table %q has no CREATE TABLE in migrations/*.up.sql", table)
		}
	}
}

// TestDBLargestTableRowsDropsTablesOutsideAllowlist is the bounded-label guard:
// a catalog row for a table the allowlist does not name must never reach the
// gauge, no matter what the query returned.
func TestDBLargestTableRowsDropsTablesOutsideAllowlist(t *testing.T) {
	estimates := []tableRowEstimate{
		{Table: "nodes", Rows: 42},
		{Table: "pg_statistic", Rows: 999},
		{Table: "users", Rows: 7},
		{Table: "schema_migrations", Rows: 3},
		{Table: "", Rows: 1},
	}
	got := dblargestTableRows(estimates)
	if len(got) != len(dbGrowthTables) {
		t.Fatalf("mapping has %d entries, want %d (allowlist only): %v", len(got), len(dbGrowthTables), got)
	}
	for name := range got {
		if !isDBGrowthTable(name) {
			t.Errorf("mapping published non-allowlisted table %q", name)
		}
	}
	if got["nodes"] != 42 {
		t.Errorf("nodes = %v, want 42", got["nodes"])
	}
	for _, name := range []string{"pg_statistic", "users", "schema_migrations", ""} {
		if _, ok := got[name]; ok {
			t.Errorf("non-allowlisted table %q was published", name)
		}
	}
}

// TestDBLargestTableRowsClampsNegativeZeroAndNaNReltuples covers the reltuples
// sentinels: -1 means "never analyzed", NaN/Inf can come out of a degenerate
// statistic, and none of them is a row count. The gauge must show 0, never a
// negative number and never NaN.
func TestDBLargestTableRowsClampsNegativeZeroAndNaNReltuples(t *testing.T) {
	cases := []struct {
		name string
		rows float64
		want float64
	}{
		{"never analyzed sentinel", -1, 0},
		{"large negative", -42, 0},
		{"zero", 0, 0},
		{"NaN", math.NaN(), 0},
		{"+Inf", math.Inf(1), 0},
		{"-Inf", math.Inf(-1), 0},
		{"positive estimate", 1234, 1234},
		{"fractional estimate", 1234.5, 1234.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dblargestTableRows([]tableRowEstimate{{Table: "tree_events", Rows: tc.rows}})["tree_events"]
			if math.IsNaN(got) {
				t.Fatalf("gauge value is NaN for input %v", tc.rows)
			}
			if got != tc.want {
				t.Fatalf("rows = %v, want %v (input %v)", got, tc.want, tc.rows)
			}
		})
	}
}

// TestDBLargestTableRowsPublishesThroughGauge proves the label wiring end to
// end: after one publish the registry exposes exactly one series per allowlisted
// table — and nothing for a table the query returned but the allowlist does not
// name.
func TestDBLargestTableRowsPublishesThroughGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetricsWithRegisterer(reg)

	publishTableRowEstimates(metrics, []tableRowEstimate{
		{Table: "nodes", Rows: 512},
		{Table: "tree_events", Rows: -1}, // never analyzed
		{Table: "pg_class", Rows: 9999},  // not allowlisted: must not appear
		{Table: "users", Rows: 12},       // not allowlisted: must not appear
	})

	family := gatherFamily(t, reg, telemetry.SeriesDBLargestTableRows)
	if family == nil {
		t.Fatalf("series %s was not published", telemetry.SeriesDBLargestTableRows)
	}
	if got, want := len(family.GetMetric()), len(dbGrowthTables); got != want {
		t.Fatalf("published %d table series, want %d: %v", got, want, dbGrowthTables)
	}
	seen := make(map[string]bool, len(family.GetMetric()))
	values := make(map[string]float64, len(family.GetMetric()))
	for _, metric := range family.GetMetric() {
		var label string
		for _, pair := range metric.GetLabel() {
			if pair.GetName() == "table" {
				label = pair.GetValue()
			} else {
				t.Errorf("unexpected label %q on %s", pair.GetName(), telemetry.SeriesDBLargestTableRows)
			}
		}
		if !isDBGrowthTable(label) {
			t.Errorf("series published non-allowlisted table label %q", label)
		}
		if seen[label] {
			t.Errorf("duplicate series for table %q", label)
		}
		seen[label] = true
		values[label] = metric.GetGauge().GetValue()
		if math.IsNaN(metric.GetGauge().GetValue()) {
			t.Errorf("table %q published NaN", label)
		}
	}
	if got := values["nodes"]; got != 512 {
		t.Errorf("nodes = %v, want 512", got)
	}
	// -1 (never analyzed) is clamped to 0, not published as a negative row count.
	if got := values["tree_events"]; got != 0 {
		t.Errorf("tree_events = %v, want 0 (reltuples -1 clamps)", got)
	}
	for _, table := range dbGrowthTables {
		if !seen[table] {
			t.Errorf("allowlisted table %q has no series", table)
		}
	}
}

// TestDBLargestTableRowsQueryUsesReltuplesNotCountStar guards the query at test
// level: a counting query is forbidden by the brief (it would full-scan on every
// scrape) and the catalog query is what makes the gauge cheap.
func TestDBLargestTableRowsQueryUsesReltuplesNotCountStar(t *testing.T) {
	query := strings.ToUpper(dbRowEstimateQuery)
	if strings.Contains(query, "COUNT(") {
		t.Fatalf("row-estimate query must not count rows: %s", dbRowEstimateQuery)
	}
	if !strings.Contains(query, "RELTUPLES") {
		t.Fatalf("row-estimate query must read pg_class.reltuples: %s", dbRowEstimateQuery)
	}
	if !strings.Contains(query, "ANY($1)") {
		t.Fatalf("row-estimate query must take the allowlist as a parameter: %s", dbRowEstimateQuery)
	}
	if !strings.Contains(query, "PG_NAMESPACE") || !strings.Contains(query, "'PUBLIC'") {
		t.Fatalf("row-estimate query must be scoped to the public schema: %s", dbRowEstimateQuery)
	}
}

// TestRelayKeyRotationsCounterIsMonotonic drives the source value the way the
// relay does and asserts the published counter only ever advances.
func TestRelayKeyRotationsCounterIsMonotonic(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetricsWithRegisterer(reg)
	state := &relayRotationCounter{}
	source := &fakeRelayHealth{rotations: 3}

	var rotationState *relayRotationCounter = state
	ctx := context.Background()
	sampleDBGrowthOnce(ctx, metrics, nil, source, nil, rotationState)

	series := func() float64 {
		family := gatherFamily(t, reg, telemetry.SeriesRelayKeyRotationsTotal)
		if family == nil || len(family.GetMetric()) == 0 {
			t.Fatalf("series %s was not published", telemetry.SeriesRelayKeyRotationsTotal)
		}
		return family.GetMetric()[0].GetCounter().GetValue()
	}

	if got := series(); got != 3 {
		t.Fatalf("first sample = %v, want 3 (rotations that predate the first tick still count)", got)
	}

	// Unchanged source: no double counting.
	sampleDBGrowthOnce(ctx, metrics, nil, source, nil, rotationState)
	if got := series(); got != 3 {
		t.Fatalf("after an unchanged source = %v, want 3 (delta must be 0)", got)
	}

	// Source advances by 4: counter advances by exactly 4.
	source.rotations = 7
	sampleDBGrowthOnce(ctx, metrics, nil, source, nil, rotationState)
	if got := series(); got != 7 {
		t.Fatalf("after a +4 advance = %v, want 7", got)
	}

	// Source goes BACKWARDS (relay state replaced): a counter cannot decrease,
	// so the baseline rebases and the published value holds.
	source.rotations = 2
	sampleDBGrowthOnce(ctx, metrics, nil, source, nil, rotationState)
	if got := series(); got != 7 {
		t.Fatalf("after a backwards source = %v, want 7 (counters never decrease)", got)
	}

	// Rebased baseline: the next real advance is counted from 2, not 7.
	source.rotations = 5
	sampleDBGrowthOnce(ctx, metrics, nil, source, nil, rotationState)
	if got := series(); got != 10 {
		t.Fatalf("after rebase then +3 = %v, want 10", got)
	}
}

// TestRelayCertExpiryGaugePublishesNotAfter asserts the gauge carries the parsed
// leaf certificate's notAfter as a unix timestamp.
func TestRelayCertExpiryGaugePublishesNotAfter(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetricsWithRegisterer(reg)
	notAfter := time.Unix(1893456000, 0).UTC()

	var state relayRotationCounter
	sampleDBGrowthOnce(context.Background(), metrics, nil, nil,
		func(context.Context) (time.Time, error) { return notAfter, nil }, &state)

	family := gatherFamily(t, reg, telemetry.SeriesRelayCertExpirySeconds)
	if family == nil || len(family.GetMetric()) == 0 {
		t.Fatalf("series %s was not published", telemetry.SeriesRelayCertExpirySeconds)
	}
	if got := family.GetMetric()[0].GetGauge().GetValue(); got != float64(notAfter.Unix()) {
		t.Fatalf("cert expiry = %v, want %v", got, float64(notAfter.Unix()))
	}
}

// TestRelayCertExpiryGaugeAbsentWhenNoCertificate asserts the skip contract: no
// certificate configured means NO series, never a zero expiry (0 reads as 1970
// and would make every alert on the series fire forever).
func TestRelayCertExpiryGaugeAbsentWhenNoCertificate(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetricsWithRegisterer(reg)

	var state relayRotationCounter
	sampleDBGrowthOnce(context.Background(), metrics, nil, nil,
		func(context.Context) (time.Time, error) { return time.Time{}, relay.ErrNoRelayCertificate }, &state)

	if family := gatherFamily(t, reg, telemetry.SeriesRelayCertExpirySeconds); family != nil {
		t.Fatalf("cert gauge published without a certificate: %v", family)
	}

	// A hard failure (unreadable file) must behave the same way.
	sampleDBGrowthOnce(context.Background(), metrics, nil, nil,
		func(context.Context) (time.Time, error) { return time.Time{}, errors.New("boom") }, &state)
	if family := gatherFamily(t, reg, telemetry.SeriesRelayCertExpirySeconds); family != nil {
		t.Fatalf("cert gauge published after a parse failure: %v", family)
	}
}

// TestDBGrowthMetricsStopsOnContextCancel proves the sampler terminates with the
// server context instead of leaking a goroutine.
func TestDBGrowthMetricsStopsOnContextCancel(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetricsWithRegisterer(reg)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDBGrowthMetrics(ctx, metrics, nil, nil, nil)
	}()

	deadline := time.After(2 * time.Second)
	cancel()
	select {
	case <-done:
	case <-deadline:
		t.Fatal("runDBGrowthMetrics did not stop after context cancellation")
	}
}

// fakeRelayHealth is a relay-health seam whose rotation count the test advances.
type fakeRelayHealth struct {
	rotations uint64
}

func (f *fakeRelayHealth) RelayHealth() relay.RelayHealth {
	return relay.RelayHealth{Rotations: f.rotations}
}
