package telemetry

import (
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// durationFamilies are the three OBS-4 slow-path histograms, in the order the
// tests below observe them.
var durationFamilies = []string{
	SeriesUpstreamProxyDuration,
	SeriesContextCompileDuration,
	SeriesExportDuration,
}

// observeAllDurationFamilies drives one simulated observation of every OBS-4
// family into d, the private-registry instance under test.
func observeAllDurationFamilies(d *durationHistograms) {
	d.observeUpstreamProxy(UpstreamProxyOutcomeOK, 0.42)
	d.observeContextCompile(1.5)
	d.observeExport(3.25)
}

// histogramBounds returns the finite bucket upper bounds of a histogram
// family. Every failed assertion below names the family, because "a bucket
// went missing" is useless without saying which one.
func histogramBounds(t *testing.T, fam *dto.MetricFamily, name string) []float64 {
	t.Helper()
	metrics := fam.GetMetric()
	if len(metrics) == 0 {
		t.Fatalf("%s: family gathered with no series", name)
	}
	hist := metrics[0].GetHistogram()
	if hist == nil {
		t.Fatalf("%s: family is not a histogram (type %s)", name, fam.GetType())
	}
	var bounds []float64
	for _, b := range hist.GetBucket() {
		if ub := b.GetUpperBound(); !isInf(ub) {
			bounds = append(bounds, ub)
		}
	}
	return bounds
}

// isInf reports whether v is a Prometheus +Inf bucket bound.
func isInf(v float64) bool {
	return v > 1e300
}

// TestDurationHistogramsExposeBuckets is the core OBS-4 assertion: after one
// observation of each slow path, every family is served as a histogram with
// real bucket series and a non-zero sample count — i.e. the observe helpers
// are wired to the registry the /metrics handler serves.
func TestDurationHistogramsExposeBuckets(t *testing.T) {
	reg := prometheus.NewRegistry()
	observeAllDurationFamilies(newDurationHistograms(reg))

	for _, name := range durationFamilies {
		fam := familyFromRegistry(t, reg, name)
		if fam == nil {
			t.Fatalf("metric family %q is not served at all", name)
		}
		if got := fam.GetType(); got != dto.MetricType_HISTOGRAM {
			t.Errorf("%s: type = %s, want HISTOGRAM", name, got)
		}
		if bounds := histogramBounds(t, fam, name); len(bounds) == 0 {
			t.Errorf("%s: exposed no finite _bucket series", name)
		}
		hist := fam.GetMetric()[0].GetHistogram()
		if got := hist.GetSampleCount(); got != 1 {
			t.Errorf("%s: sample count = %d, want 1 (one simulated observation)", name, got)
		}
	}
}

// TestDurationHistogramBucketsStraddleSLOs pins the bucket lists to the SLO
// they encode (acceptance gate 6): the comment above each Buckets list names a
// boundary, and that boundary has to actually be in the list or a percentile
// at the SLO line cannot be read off the histogram. The compile family's 30 is
// the product "<30s resume" line and is required by the brief.
func TestDurationHistogramBucketsStraddleSLOs(t *testing.T) {
	reg := prometheus.NewRegistry()
	observeAllDurationFamilies(newDurationHistograms(reg))

	want := map[string][]float64{
		// 1s interactive line, 30s hard upstream budget.
		SeriesUpstreamProxyDuration: {1, 30},
		// <30s resume SLO; 1s/5s shares of it.
		SeriesContextCompileDuration: {1, 5, 30},
		// 10s interactive wait line, 60s stuck-export ceiling.
		SeriesExportDuration: {10, 60},
	}

	for name, required := range want {
		fam := familyFromRegistry(t, reg, name)
		if fam == nil {
			t.Fatalf("metric family %q is not served at all", name)
		}
		bounds := histogramBounds(t, fam, name)
		for _, wantBound := range required {
			found := false
			for _, b := range bounds {
				if b == wantBound {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: bucket list %v has no %g boundary (the SLO line its comment names)",
					name, bounds, wantBound)
			}
		}
	}
}

// TestDurationHistogramLabelsAreBounded is the cardinality guard: the proxy
// family carries exactly one label, `outcome`, whose values come from the
// closed set. No series may carry a label named user/peer/tree_id/node_id (or
// anything else) and no label value may be a raw URL or path — either would
// mint one time series per request path or person.
func TestDurationHistogramLabelsAreBounded(t *testing.T) {
	reg := prometheus.NewRegistry()
	d := newDurationHistograms(reg)
	observeAllDurationFamilies(d)
	// One unknown value: it must fold into the closed set, not create a new
	// series.
	d.observeUpstreamProxy("/v1/trees/abc123", 0.1)

	banned := map[string]bool{"user": true, "peer": true, "tree_id": true, "node_id": true}

	fam := familyFromRegistry(t, reg, SeriesUpstreamProxyDuration)
	if fam == nil {
		t.Fatalf("metric family %q is not served at all", SeriesUpstreamProxyDuration)
	}
	var outcomes []string
	for _, metric := range fam.GetMetric() {
		for _, label := range metric.GetLabel() {
			name, value := label.GetName(), label.GetValue()
			if banned[name] {
				t.Errorf("%s: unbounded label name %q", SeriesUpstreamProxyDuration, name)
			}
			if name != "outcome" {
				t.Errorf("%s: unexpected label %q (only outcome is allowed)", SeriesUpstreamProxyDuration, name)
			}
			if strings.Contains(value, "/") {
				t.Errorf("%s: label value %q carries a raw URL/path", SeriesUpstreamProxyDuration, value)
			}
			outcomes = append(outcomes, value)
		}
	}

	sort.Strings(outcomes)
	want := []string{UpstreamProxyOutcomeError, UpstreamProxyOutcomeOK}
	if strings.Join(outcomes, ",") != strings.Join(want, ",") {
		t.Fatalf("outcome values = %v, want exactly the closed set %v (an unknown outcome must fold, not mint a series)",
			outcomes, want)
	}
}

// TestUnlabelledDurationFamiliesHaveNoLabels keeps the no-label contract
// explicit: compile and export are process-wide slow paths, so any label on
// them would be per-request cardinality for no analytic gain.
func TestUnlabelledDurationFamiliesHaveNoLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	observeAllDurationFamilies(newDurationHistograms(reg))

	for _, name := range []string{SeriesContextCompileDuration, SeriesExportDuration} {
		fam := familyFromRegistry(t, reg, name)
		if fam == nil {
			t.Fatalf("metric family %q is not served at all", name)
		}
		for _, metric := range fam.GetMetric() {
			if labels := metric.GetLabel(); len(labels) != 0 {
				t.Errorf("%s: got %d labels, want none", name, len(labels))
			}
		}
	}
}
