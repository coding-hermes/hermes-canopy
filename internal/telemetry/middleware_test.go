package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// newMetricsTestServer builds a chi router wired exactly like the production
// server wires its metrics: MetricsMiddleware in the global chain and a
// /metrics endpoint served by promhttp — but against a FRESH registry so the
// test never collides with the default registry other telemetry tests use.
// One matched route is mounted so pattern-vs-path label behaviour is
// observable.
func newMetricsTestServer(t *testing.T) (*httptest.Server, *Metrics) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m := newMetricsWith(reg)

	r := chi.NewRouter()
	r.Use(MetricsMiddleware(m))
	r.Get("/api/v1/widgets/{widget_id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, m
}

func scrapeMetrics(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url) //nolint:bodyclose // closed below
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	return string(body)
}

// TestMetricsWireEmitsCanopyNamespace proves the wire behaviour the foreman's
// live curl checks post-merge: a scrape of /metrics serves the renamed
// canopy_ series, and ONLY canopy_-namespaced product series — never the old
// bare names.
func TestMetricsWireEmitsCanopyNamespace(t *testing.T) {
	srv, _ := newMetricsTestServer(t)

	// One matched request so the request families have samples.
	resp, err := http.Get(srv.URL + "/api/v1/widgets/w-123")
	if err != nil {
		t.Fatalf("GET matched route: %v", err)
	}
	_ = resp.Body.Close()

	out := scrapeMetrics(t, srv.URL+"/metrics")

	for _, want := range []string{
		"canopy_http_requests_total",
		"canopy_http_request_duration_seconds",
		"canopy_active_connections",
		"canopy_tree_count",
		"canopy_node_count",
		"canopy_resume_started_total",
		"canopy_resume_duration_seconds",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics output is missing %q", want)
		}
	}

	// The old bare names must be gone from the wire.
	for _, stale := range []string{
		"\nrequest_total", "\nrequest_duration_seconds", "\nactive_connections",
		"\ntree_count", "\nnode_count", "\nresume_started_total", "\nresume_duration_seconds",
	} {
		if strings.Contains("\n"+out, stale) {
			t.Errorf("/metrics still serves the pre-rename series %q", strings.TrimSpace(stale))
		}
	}

	// The route label is the chi PATTERN, never the concrete path segment.
	if !strings.Contains(out, `path="/api/v1/widgets/{widget_id}"`) {
		t.Errorf("route label is not the chi pattern; want path=\"/api/v1/widgets/{widget_id}\" in:\n%s", out)
	}
	if strings.Contains(out, "w-123") {
		t.Error("the concrete request path segment leaked into a label value")
	}
}

// TestUnmatchedRequestsNeverLeakTheRawPath is the D3 regression guard: a
// request that matches no route increments
// canopy_http_unmatched_requests_total{method=...}, lands in the "unmatched"
// path bucket for the other request series, and the sentinel path appears in
// NO label value anywhere on the wire.
func TestUnmatchedRequestsNeverLeakTheRawPath(t *testing.T) {
	srv, _ := newMetricsTestServer(t)

	const sentinel = "/api/v1/sentinel-9f3a1c"
	resp, err := http.Get(srv.URL + sentinel)
	if err != nil {
		t.Fatalf("GET unknown path: %v", err)
	}
	_ = resp.Body.Close()

	out := scrapeMetrics(t, srv.URL+"/metrics")

	// The unmatched counter exists, carries the method label, and moved.
	if !strings.Contains(out, `canopy_http_unmatched_requests_total{method="GET"} 1`) {
		t.Errorf("canopy_http_unmatched_requests_total{method=\"GET\"} = 1 not found in:\n%s", out)
	}

	// The unmatched request is folded into the unmatched bucket, not a
	// per-path series.
	if !strings.Contains(out, `path="unmatched"`) {
		t.Errorf("unmatched request did not land in the path=\"unmatched\" bucket:\n%s", out)
	}

	// The raw path appears NOWHERE on the wire.
	if strings.Contains(out, "sentinel-9f3a1c") {
		t.Error("the raw unmatched path leaked into /metrics output")
	}
}

// TestUnmatchedCounterOnlyMovesOnUnmatched pins the counter's direction: a
// matched request must NOT increment it.
func TestUnmatchedCounterOnlyMovesOnUnmatched(t *testing.T) {
	srv, _ := newMetricsTestServer(t)

	resp, err := http.Get(srv.URL + "/api/v1/widgets/w-9")
	if err != nil {
		t.Fatalf("GET matched route: %v", err)
	}
	_ = resp.Body.Close()

	out := scrapeMetrics(t, srv.URL+"/metrics")
	if strings.Contains(out, "canopy_http_unmatched_requests_total{") {
		t.Errorf("a matched request incremented the unmatched counter:\n%s", out)
	}
}

// TestUnmatchedUnderMountedSubrouter pins the PRODUCTION router shape, which
// the flat fixture above cannot reproduce: canopyd mounts the API on a
// subrouter (`r.Mount("/api/v1", apiRouter)`) whose own NotFound handler
// answers unknown API paths. chi then leaves RoutePattern at the subrouter's
// catch-all ("/api/v1/*") rather than empty, so a bare pattern check files
// probe traffic under a fake matched route and the unmatched counter never
// moves — the defect this test exists to catch. Live probe that found it:
// GET /api/v1/sentinel-9f3a1c on :8091 produced path="/api/v1/*" and no
// canopy_http_unmatched_requests_total series at all.
func TestUnmatchedUnderMountedSubrouter(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newMetricsWith(reg)

	r := chi.NewRouter()
	r.Use(MetricsMiddleware(m))

	api := chi.NewRouter()
	api.Get("/widgets/{widget_id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	api.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":"NOT_FOUND"}}`, http.StatusNotFound)
	})
	r.Mount("/api/v1", api)
	r.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	const sentinel = "/api/v1/sentinel-9f3a1c"
	resp, err := http.Get(srv.URL + sentinel)
	if err != nil {
		t.Fatalf("GET unknown path under mounted subrouter: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 from the subrouter NotFound, got %d", resp.StatusCode)
	}

	out := scrapeMetrics(t, srv.URL+"/metrics")

	if !strings.Contains(out, `canopy_http_unmatched_requests_total{method="GET"} 1`) {
		t.Errorf("mounted-subrouter 404 did not increment the unmatched counter:\n%s", out)
	}
	if !strings.Contains(out, `path="unmatched"`) {
		t.Errorf("mounted-subrouter 404 did not land in path=\"unmatched\":\n%s", out)
	}
	if strings.Contains(out, `path="/api/v1/*"`) {
		t.Errorf("catch-all resolution was recorded as a matched route:\n%s", out)
	}
	if strings.Contains(out, "sentinel-9f3a1c") {
		t.Error("the raw unmatched path leaked into /metrics output")
	}
}
