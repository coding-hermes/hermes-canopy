// Package telemetry provides Prometheus metrics and structured request logging for canopyd.
package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Series names emitted by canopyd. Every product series carries the canopy_
// namespace so a Prometheus operator can attribute it without joining against
// the scrape config (CAN-MET-01; docs/observability-spec.md §4).
const (
	// SeriesHTTPRequestDuration is the request-latency histogram.
	SeriesHTTPRequestDuration = "canopy_http_request_duration_seconds"
	// SeriesHTTPRequestsTotal counts HTTP requests by method, route pattern,
	// and status code.
	SeriesHTTPRequestsTotal = "canopy_http_requests_total"
	// SeriesActiveConnections tracks in-flight HTTP requests.
	SeriesActiveConnections = "canopy_active_connections"
	// SeriesTreeCount tracks the total number of trees.
	SeriesTreeCount = "canopy_tree_count"
	// SeriesNodeCount tracks the total number of nodes.
	SeriesNodeCount = "canopy_node_count"
	// SeriesResumeDuration is the server-observable resume-time histogram.
	SeriesResumeDuration = "canopy_resume_duration_seconds"
	// SeriesResumeStarted counts resume windows opened.
	SeriesResumeStarted = "canopy_resume_started_total"
	// SeriesHTTPUnmatchedRequestsTotal counts requests that matched no chi
	// route. Its only label is the method; the raw path is NEVER a label
	// value anywhere (docs/observability-spec.md §4, D3).
	SeriesHTTPUnmatchedRequestsTotal = "canopy_http_unmatched_requests_total"
)

// Metrics holds all Prometheus metrics exported by canopyd.
type Metrics struct {
	// RequestDuration tracks HTTP request latency in seconds.
	RequestDuration *prometheus.HistogramVec
	// RequestTotal counts HTTP requests by method, route pattern, and status code.
	RequestTotal *prometheus.CounterVec
	// ActiveConnections tracks the current number of in-flight HTTP requests.
	ActiveConnections prometheus.Gauge
	// TreeCount tracks the total number of trees (wired from the tree/node
	// count source in cmd/canopyd at startup and on the refresh tick).
	TreeCount prometheus.Gauge
	// NodeCount tracks the total number of nodes (wired from the same source).
	NodeCount prometheus.Gauge
	// UnmatchedRequests counts requests that matched no chi route, by method
	// only — the raw path never becomes a label value.
	UnmatchedRequests *prometheus.CounterVec
	// ResumeDuration tracks the server-observable part of the product's
	// "<30s resume" claim (GAP-079): the seconds between a user's first
	// tree-scoped read after an idle gap and the compiled context they
	// resume with. Unlabelled, and it excludes browser render time.
	ResumeDuration *prometheus.Histogram
	// ResumeStarted counts resume windows opened. It exists because the
	// histogram alone cannot distinguish "no resumes" from "resumes that
	// never completed": a window that never reaches a context compile shows
	// up here as started-but-never-observed.
	ResumeStarted prometheus.Counter
}

// NewMetrics creates and registers all Prometheus metrics on the default
// registry (the one promhttp.Handler() serves).
func NewMetrics() *Metrics {
	return newMetricsWith(prometheus.DefaultRegisterer)
}

// newMetricsWith creates and registers all Prometheus metrics on reg. Tests
// pass a fresh prometheus.NewRegistry() so several Metrics can exist in one
// process without duplicate-registration panics.
func newMetricsWith(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	// promauto.NewHistogram returns the prometheus.Histogram interface (only
	// the *Vec constructors return pointers), so the resume histogram is
	// registered by promauto and then held by address to keep the field type
	// the *prometheus.Histogram the resume contract names.
	resumeDuration := factory.NewHistogram(
		prometheus.HistogramOpts{
			Name: SeriesResumeDuration,
			Help: "Seconds between a user's first tree read after an idle gap and the compiled context they resume with.",
			// 30 is the product SLO line ("resume work in <30 seconds")
			// and must stay a bucket boundary so the SLO is directly
			// readable off the histogram.
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15, 20, 30, 45, 60, 120},
		},
	)
	m := &Metrics{
		RequestDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    SeriesHTTPRequestDuration,
				Help:    "HTTP request latency in seconds.",
				Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"method", "path"},
		),
		RequestTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: SeriesHTTPRequestsTotal,
				Help: "Total number of HTTP requests.",
			},
			[]string{"method", "path", "status"},
		),
		ActiveConnections: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: SeriesActiveConnections,
				Help: "Current number of in-flight HTTP requests.",
			},
		),
		TreeCount: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: SeriesTreeCount,
				Help: "Total number of trees.",
			},
		),
		NodeCount: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: SeriesNodeCount,
				Help: "Total number of nodes.",
			},
		),
		UnmatchedRequests: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: SeriesHTTPUnmatchedRequestsTotal,
				Help: "Requests that matched no chi route, by method. The raw path is never a label.",
			},
			[]string{"method"},
		),
		ResumeDuration: &resumeDuration,
		ResumeStarted: factory.NewCounter(
			prometheus.CounterOpts{
				Name: SeriesResumeStarted,
				Help: "Resume windows opened: a user's first tree-scoped read after an idle gap.",
			},
		),
	}
	return m
}

// IncResumeStarted records that a resume window opened.
//
// Nil-receiver safe: the resume middleware is registered conditionally, and a
// metric sink that was never constructed must be a no-op rather than a panic.
func (m *Metrics) IncResumeStarted() {
	if m == nil || m.ResumeStarted == nil {
		return
	}
	m.ResumeStarted.Inc()
}

// ObserveResumeDuration records one completed resume window, in seconds.
//
// Nil-receiver safe, for the same reason as IncResumeStarted. The field is a
// pointer-to-interface (a Go anti-pattern this repo's resume contract names),
// and a pointer to an interface has an empty method set, so the histogram is
// dereferenced before the call.
func (m *Metrics) ObserveResumeDuration(seconds float64) {
	if m == nil || m.ResumeDuration == nil {
		return
	}
	(*m.ResumeDuration).Observe(seconds)
}

// SetTreeCount sets the canopy_tree_count gauge. Nil-receiver safe, so a
// count source running against a server without metrics enabled is a no-op.
func (m *Metrics) SetTreeCount(n int) {
	if m == nil || m.TreeCount == nil {
		return
	}
	m.TreeCount.Set(float64(n))
}

// SetNodeCount sets the canopy_node_count gauge. Nil-receiver safe, for the
// same reason as SetTreeCount.
func (m *Metrics) SetNodeCount(n int) {
	if m == nil || m.NodeCount == nil {
		return
	}
	m.NodeCount.Set(float64(n))
}

// IncUnmatchedRequest records one request that matched no chi route. The
// method is the only label; the raw path is never recorded anywhere.
// Nil-receiver safe.
func (m *Metrics) IncUnmatchedRequest(method string) {
	if m == nil || m.UnmatchedRequests == nil {
		return
	}
	m.UnmatchedRequests.WithLabelValues(method).Inc()
}

// SeriesWriter names the production writer path for one declared series. The
// declared-series test (metrics_writers_test.go) fails when a series in this
// table has an empty Writer, which is how the node_count orphan (declared
// with no writer until CAN-MET-01 wired it) can never regress silently.
type SeriesWriter struct {
	Series string
	// Writer names the code path that writes the series — the method on
	// *Metrics and its production caller. Kept as a string so the guard is a
	// table test over a writer registry rather than a reflection crawl.
	Writer string
}

// declaredSeriesWriters is the writer registry: every product series NewMetrics
// declares, paired with the writer that keeps it from being a permanent zero.
// Add a series to NewMetrics without adding its writer here and the telemetry
// test fails, naming the orphan.
var declaredSeriesWriters = []SeriesWriter{
	{SeriesHTTPRequestDuration, "MetricsMiddleware: RequestDuration.Observe per request"},
	{SeriesHTTPRequestsTotal, "MetricsMiddleware: RequestTotal.Inc per request"},
	{SeriesActiveConnections, "MetricsMiddleware: ActiveConnections Inc/Dec around each request"},
	{SeriesTreeCount, "cmd/canopyd gauge refresh: Metrics.SetTreeCount from CountDomainTrees"},
	{SeriesNodeCount, "cmd/canopyd gauge refresh: Metrics.SetNodeCount from CountDomainNodes"},
	{SeriesHTTPUnmatchedRequestsTotal, "MetricsMiddleware: Metrics.IncUnmatchedRequest when chi resolves no route pattern"},
	{SeriesResumeDuration, "ResumeMiddleware: Metrics.ObserveResumeDuration on a completed resume window"},
	{SeriesResumeStarted, "ResumeMiddleware: Metrics.IncResumeStarted when a resume window opens"},
}

// DeclaredSeriesWriters returns a copy of the writer registry so tests (and
// only tests) can enumerate declared series against their writers.
func DeclaredSeriesWriters() []SeriesWriter {
	out := make([]SeriesWriter, len(declaredSeriesWriters))
	copy(out, declaredSeriesWriters)
	return out
}
