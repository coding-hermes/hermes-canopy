package telemetry

import (
	"strings"
	"testing"
)

// orphanedSeries returns the names of declared series whose Writer field is
// empty — series that promauto registers but nothing can ever move off zero.
// The node_count gauge was exactly this orphan until CAN-MET-01 wired it
// (docs/observability-spec.md defect D2).
func orphanedSeries(writers []SeriesWriter) []string {
	var orphans []string
	for _, w := range writers {
		if strings.TrimSpace(w.Writer) == "" {
			orphans = append(orphans, w.Series)
		}
	}
	return orphans
}

// TestEveryDeclaredSeriesHasAWriter is the orphan guard the observability
// audit asked for (OBS-6 / CAN-MET-01 step 3): it enumerates the declared
// product series through the writer registry and fails — naming the orphan —
// when any declared series has no writer. A series added to NewMetrics without
// a registry entry, or with its writer blanked, fails here.
func TestEveryDeclaredSeriesHasAWriter(t *testing.T) {
	writers := DeclaredSeriesWriters()
	if len(writers) == 0 {
		t.Fatal("the writer registry is empty — it cannot guard anything")
	}

	// Every declared series lives in the canopy_ namespace (CAN-MET-01: the
	// whole point of the rename is Prometheus attributability).
	for _, w := range writers {
		if !strings.HasPrefix(w.Series, "canopy_") {
			t.Errorf("declared series %q is outside the canopy_ namespace", w.Series)
		}
	}

	if orphans := orphanedSeries(writers); len(orphans) > 0 {
		t.Fatalf("declared series with NO writer (permanent-zero orphans): %s",
			strings.Join(orphans, ", "))
	}

	// The registry must cover exactly the series-name constants NewMetrics
	// registers — no more (a dead entry hides a rename), no less (an
	// unguarded series).
	declared := map[string]bool{}
	for _, w := range writers {
		if declared[w.Series] {
			t.Errorf("series %q appears twice in the writer registry", w.Series)
		}
		declared[w.Series] = true
	}
	for _, name := range []string{
		SeriesHTTPRequestDuration,
		SeriesHTTPRequestsTotal,
		SeriesActiveConnections,
		SeriesTreeCount,
		SeriesNodeCount,
		SeriesHTTPUnmatchedRequestsTotal,
		SeriesResumeDuration,
		SeriesResumeStarted,
	} {
		if !declared[name] {
			t.Errorf("series %q is registered by NewMetrics but has no writer-registry entry", name)
		}
	}
}

// TestOrphanDetectorNamesTheOrphan is the positive control: the guard above
// must be able to fail, and must name the series whose writer went missing.
// Removing the canopy_node_count writer — the historical D2 orphan — has to
// produce a failure string that contains "canopy_node_count".
func TestOrphanDetectorNamesTheOrphan(t *testing.T) {
	writers := DeclaredSeriesWriters()
	removed := false
	for i := range writers {
		if writers[i].Series == SeriesNodeCount {
			writers[i].Writer = ""
			removed = true
		}
	}
	if !removed {
		t.Fatalf("writer registry no longer contains %q — re-point this control", SeriesNodeCount)
	}

	orphans := orphanedSeries(writers)
	if len(orphans) != 1 || orphans[0] != SeriesNodeCount {
		t.Fatalf("orphan detector = %v, want exactly [%q]", orphans, SeriesNodeCount)
	}

	// The production table (untouched by this control) stays clean.
	if orphans := orphanedSeries(DeclaredSeriesWriters()); len(orphans) > 0 {
		t.Fatalf("control mutated the shared registry — production orphans now: %v", orphans)
	}
}
