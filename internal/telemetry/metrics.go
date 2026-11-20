// Package telemetry provides Prometheus metrics and structured request logging for canopyd.
package telemetry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
	// SeriesDBUp tracks whether the database readiness probe succeeded.
	SeriesDBUp = "canopy_db_up"
	// SeriesResumeDuration is the server-observable resume-time histogram.
	SeriesResumeDuration = "canopy_resume_duration_seconds"
	// SeriesResumeStarted counts resume windows opened.
	SeriesResumeStarted = "canopy_resume_started_total"
	// SeriesHTTPUnmatchedRequestsTotal counts requests that matched no chi
	// route. Its only label is the method; the raw path is NEVER a label
	// value anywhere (docs/observability-spec.md §4, D3).
	SeriesHTTPUnmatchedRequestsTotal = "canopy_http_unmatched_requests_total"
	// SeriesBuildInfo is the build identity gauge: value 1, labels
	// version/commit. It is the only series that says WHICH binary a scraped
	// host is running (docs/observability-spec.md §4, P0 table; OBS-1).
	SeriesBuildInfo = "canopy_build_info"
	// SeriesUpstreamGatewayUp reports whether the upstream Hermes gateway is
	// reachable (1) or not (0). Canopy answers 2xx while every AI run fails
	// when this is 0 — the page nobody was getting (spec §3.4; OBS-1 AC2).
	SeriesUpstreamGatewayUp = "canopy_upstream_gateway_up"
	// SeriesUpstreamGatewayCheckErrorsTotal counts failed gateway checks, by
	// closed-set kind label (timeout/non_200/conn_refused/other), so a
	// flapping gateway is countable and not just visible (spec §4, OBS-1 AC2).
	SeriesUpstreamGatewayCheckErrorsTotal = "canopy_upstream_gateway_check_errors_total"
	SeriesTransportConnections            = "canopy_transport_connections"
	SeriesTransportConnectTotal           = "canopy_transport_connect_total"
	SeriesTransportReconnectsTotal        = "canopy_transport_reconnects_total"
	SeriesTransportMessagesTotal          = "canopy_transport_messages_total"
	SeriesRelaySessions                   = "canopy_relay_sessions"
	SeriesRelayQueueDepth                 = "canopy_relay_queue_depth"
	SeriesRelayDropsTotal                 = "canopy_relay_drops_total"
	// SeriesUpstreamProxyDuration is the duration histogram over outbound
	// upstream calls: the network proxy to the Hermes gateway and the
	// transport health probe. Today both stamp a duration into their
	// response body and no series ever sees it (OBS-4; spec §4 P2).
	SeriesUpstreamProxyDuration = "canopy_upstream_proxy_duration_seconds"
	// SeriesContextCompileDuration is the server-side context-compile
	// duration — the CPU-bound build behind the "<30s resume" claim, which
	// only the end-to-end resume window measured before OBS-4.
	SeriesContextCompileDuration = "canopy_context_compile_duration_seconds"
	// SeriesExportDuration is the card-export wall time: the CLI snapshot
	// lane's own duration, which had no record anywhere (OBS-4).
	SeriesExportDuration        = "canopy_export_duration_seconds"
	SeriesDBPoolAcquired        = "canopy_db_pool_acquired"
	SeriesDBPoolTotal           = "canopy_db_pool_total"
	SeriesFederationQueueDepth  = "canopy_federation_queue_depth"
	SeriesRateLimitedTotal      = "canopy_ratelimited_total"
	SeriesRateLimiterTrackedIPs = "canopy_ratelimiter_tracked_ips"
	// SeriesDBLargestTableRows is the row estimate of one content table,
	// labelled by a BOUNDED `table` allowlist (five names hardcoded in
	// internal/server/db_growth_metrics.go). The number is
	// pg_class.reltuples — PostgreSQL's own planner statistic — so "is our
	// data growing?" is answerable from a scrape without ever counting rows
	// (OBS-5).
	SeriesDBLargestTableRows = "canopy_db_largest_table_rows"
	// SeriesRelayCertExpirySeconds is the unix timestamp of the relay TLS leaf
	// certificate's notAfter: the one number that answers "is our relay
	// certificate expiring?" without opening /health by hand (OBS-5). It is a
	// timestamp, not a duration, so a scraper can compare it against time()
	// whatever the scrape interval is.
	SeriesRelayCertExpirySeconds = "canopy_relay_cert_expiry_seconds"
	// SeriesRelayKeyRotationsTotal counts relay HMAC key rotations since
	// process start, mirroring RelayHealth.Rotations as a counter that can be
	// rate()d — a rotation that cannot be counted cannot be alerted on
	// (OBS-5).
	SeriesRelayKeyRotationsTotal = "canopy_relay_key_rotations_total"
)

// TransportSnapshot is a bounded, label-safe snapshot of transport and relay state.
type TransportSnapshot struct {
	Connections      map[string]int
	Connects         map[string]int64
	Reconnects       map[string]int64
	MessagesSent     map[string]int64
	MessagesReceived map[string]int64
	RelaySessions    int
	RelayQueueDepth  int
	RelayDrops       int64
}

type transportSnapshotCollector struct {
	mu                                                                       sync.RWMutex
	source                                                                   func() TransportSnapshot
	connections, connects, reconnects, messages, sessions, queueDepth, drops *prometheus.Desc
}

func newTransportSnapshotCollector() *transportSnapshotCollector {
	return &transportSnapshotCollector{
		connections: prometheus.NewDesc(SeriesTransportConnections, "Active transport connections.", []string{"transport"}, nil),
		connects:    prometheus.NewDesc(SeriesTransportConnectTotal, "Transport connection attempts.", []string{"transport"}, nil),
		reconnects:  prometheus.NewDesc(SeriesTransportReconnectsTotal, "Transport reconnect attempts.", []string{"transport"}, nil),
		messages:    prometheus.NewDesc(SeriesTransportMessagesTotal, "Transport messages sent or received.", []string{"transport", "direction"}, nil),
		sessions:    prometheus.NewDesc(SeriesRelaySessions, "Active relay sessions.", nil, nil),
		queueDepth:  prometheus.NewDesc(SeriesRelayQueueDepth, "Current relay queue depth.", nil, nil),
		drops:       prometheus.NewDesc(SeriesRelayDropsTotal, "Relay messages dropped.", nil, nil),
	}
}

func (c *transportSnapshotCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.connections
	ch <- c.connects
	ch <- c.reconnects
	ch <- c.messages
	ch <- c.sessions
	ch <- c.queueDepth
	ch <- c.drops
}

func (c *transportSnapshotCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	source := c.source
	c.mu.RUnlock()
	if source == nil {
		return
	}
	s := source()
	for _, tt := range []string{"relay", "sse", "webrtc", "nats", "redis"} {
		ch <- prometheus.MustNewConstMetric(c.connections, prometheus.GaugeValue, float64(s.Connections[tt]), tt)
		ch <- prometheus.MustNewConstMetric(c.connects, prometheus.CounterValue, float64(s.Connects[tt]), tt)
		ch <- prometheus.MustNewConstMetric(c.reconnects, prometheus.CounterValue, float64(s.Reconnects[tt]), tt)
		ch <- prometheus.MustNewConstMetric(c.messages, prometheus.CounterValue, float64(s.MessagesSent[tt]), tt, "sent")
		ch <- prometheus.MustNewConstMetric(c.messages, prometheus.CounterValue, float64(s.MessagesReceived[tt]), tt, "received")
	}
	ch <- prometheus.MustNewConstMetric(c.sessions, prometheus.GaugeValue, float64(s.RelaySessions))
	ch <- prometheus.MustNewConstMetric(c.queueDepth, prometheus.GaugeValue, float64(s.RelayQueueDepth))
	ch <- prometheus.MustNewConstMetric(c.drops, prometheus.CounterValue, float64(s.RelayDrops))
}

// Metrics holds all Prometheus metrics exported by canopyd.
type Metrics struct {
	gatherer           prometheus.Gatherer
	transportCollector *transportSnapshotCollector
	// DBPoolAcquired and DBPoolTotal expose the latest pgxpool capacity snapshot.
	DBPoolAcquired        prometheus.Gauge
	DBPoolTotal           prometheus.Gauge
	FederationQueueDepth  prometheus.Gauge
	RateLimitedTotal      *prometheus.CounterVec
	RateLimiterTrackedIPs prometheus.Gauge
	// DBLargestTableRows is the per-table row estimate. Its ONLY label is
	// `table`, and its values are allowlist-only (see
	// internal/server/db_growth_metrics.go) so the series can never grow one
	// child per table PostgreSQL happens to have (OBS-5).
	DBLargestTableRows *prometheus.GaugeVec
	// RelayCertExpirySeconds is the relay TLS leaf certificate's notAfter as a
	// unix timestamp. It is a label-less GaugeVec rather than a Gauge so the
	// series can be genuinely ABSENT (not 0) while no certificate is
	// configured: a registered plain Gauge exports its zero value, which would
	// read as "expired in 1970" and fire every alert on it.
	RelayCertExpirySeconds *prometheus.GaugeVec
	// RelayKeyRotationsTotal counts relay HMAC key rotations since process
	// start.
	RelayKeyRotationsTotal prometheus.Counter
	// BuildInfo is the build identity gauge: value 1, labels version/commit,
	// so a scrape can attribute the series to the running binary.
	BuildInfo *prometheus.GaugeVec
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
	// DBUp tracks the most recent database readiness result.
	DBUp prometheus.Gauge
	// UnmatchedRequests counts requests that matched no chi route, by method
	// only — the raw path never becomes a label value.
	UnmatchedRequests *prometheus.CounterVec
	// UpstreamGatewayUp is the most recent upstream Hermes gateway
	// reachability result (1 reachable, 0 not).
	UpstreamGatewayUp prometheus.Gauge
	// UpstreamGatewayCheckErrors counts failed gateway checks by a closed-set
	// kind label — never by an error string (that would mint a series per
	// distinct failure message).
	UpstreamGatewayCheckErrors *prometheus.CounterVec
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

// NewMetricsWithRegisterer creates and registers all Prometheus metrics on reg.
// Production uses NewMetrics (default registry); tests and embedders that must
// not touch the process-global registry use this.
func NewMetricsWithRegisterer(reg prometheus.Registerer) *Metrics {
	return newMetricsWith(reg)
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
	collector := newTransportSnapshotCollector()
	if err := reg.Register(collector); err != nil {
		panic(err)
	}
	m := &Metrics{
		gatherer: func() prometheus.Gatherer {
			if gatherer, ok := reg.(prometheus.Gatherer); ok {
				return gatherer
			}
			return prometheus.DefaultGatherer
		}(),
		transportCollector:    collector,
		DBPoolAcquired:        factory.NewGauge(prometheus.GaugeOpts{Name: SeriesDBPoolAcquired, Help: "Number of PostgreSQL pool connections currently acquired."}),
		DBPoolTotal:           factory.NewGauge(prometheus.GaugeOpts{Name: SeriesDBPoolTotal, Help: "Total number of PostgreSQL pool connections currently open."}),
		FederationQueueDepth:  factory.NewGauge(prometheus.GaugeOpts{Name: SeriesFederationQueueDepth, Help: "Total pending federation events across all peers."}),
		RateLimitedTotal:      factory.NewCounterVec(prometheus.CounterOpts{Name: SeriesRateLimitedTotal, Help: "Requests rejected by the rate limiter, by bounded chi route pattern."}, []string{"route"}),
		RateLimiterTrackedIPs: factory.NewGauge(prometheus.GaugeOpts{Name: SeriesRateLimiterTrackedIPs, Help: "Number of client buckets currently tracked by the rate limiter."}),
		DBLargestTableRows: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: SeriesDBLargestTableRows,
				Help: "Estimated row count of a content table, from pg_class.reltuples (allowlist-only `table` label).",
			},
			[]string{"table"},
		),
		RelayCertExpirySeconds: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: SeriesRelayCertExpirySeconds,
			Help: "Unix timestamp of the relay TLS leaf certificate's notAfter. Absent while no certificate is configured.",
		}, nil),
		RelayKeyRotationsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: SeriesRelayKeyRotationsTotal,
			Help: "Relay HMAC key rotations since process start.",
		}),
		BuildInfo: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: SeriesBuildInfo,
				Help: "Build identity of the running canopyd binary (value 1), labelled with version and commit.",
			},
			[]string{"version", "commit"},
		),
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
		DBUp: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: SeriesDBUp,
				Help: "Whether the database readiness probe is healthy (1) or unavailable (0).",
			},
		),
		UnmatchedRequests: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: SeriesHTTPUnmatchedRequestsTotal,
				Help: "Requests that matched no chi route, by method. The raw path is never a label.",
			},
			[]string{"method"},
		),
		UpstreamGatewayUp: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: SeriesUpstreamGatewayUp,
				Help: "Whether the upstream Hermes gateway answered its health check on the last poll (1) or not (0).",
			},
		),
		UpstreamGatewayCheckErrors: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: SeriesUpstreamGatewayCheckErrorsTotal,
				Help: "Failed upstream Hermes gateway health checks, by kind (timeout/non_200/conn_refused/other).",
			},
			[]string{"kind"},
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

// ── Slow-path duration histograms (OBS-4) ───────────────────────────────────
//
// Four sites computed a duration that no series ever saw: the network proxy to
// the Hermes gateway (body-only durationMs), the transport health probe
// (body-only latency_ms — the same two sites spec §3.2 calls out), the context
// compiler (no record), and the card export (no record). They share three
// families because the two outbound calls are one slow-path class (an upstream
// call an agent waits on) and the two local compute paths are distinct. Each
// bucket list straddles the SLO line the path is judged against.
//
// These are package-level promauto histograms on the DEFAULT registry (the one
// /metrics serves) with package-level Observe helpers, deliberately NOT fields
// on Metrics: the call sites (a handler, the CPU-bound compiler, a CLI export)
// have no metrics handle and threading *Metrics through their constructors
// would be interface surgery for one Observe call. newDurationHistograms takes
// a Registerer so tests can gather them from a private registry instead.

// Outcomes for SeriesUpstreamProxyDuration. This is a CLOSED set — anything
// else folds into UpstreamProxyOutcomeError, so a caller can never mint one
// series per distinct failure. It is derived from the status the call
// produced, never from a URL, user id, tree id or node id (spec §4: no
// unbounded label values).
const (
	// UpstreamProxyOutcomeOK is an upstream call that answered 2xx/3xx.
	UpstreamProxyOutcomeOK = "ok"
	// UpstreamProxyOutcomeError is an upstream call that failed or answered
	// a 4xx/5xx.
	UpstreamProxyOutcomeError = "error"
	// UpstreamProxyOutcomeTimeout is an upstream call Canopy abandoned at
	// its own deadline. It is distinct from UpstreamProxyOutcomeError
	// because "the gateway is slow" and "the gateway is broken" page
	// differently.
	UpstreamProxyOutcomeTimeout = "timeout"
)

// durationHistograms holds the OBS-4 slow-path histograms. The struct exists
// so a test can build a private-registry instance (newDurationHistograms) and
// the exported Observe helpers can share one process-wide instance
// (defaultDurations) without threading a *Metrics to the call sites.
type durationHistograms struct {
	upstreamProxy  *prometheus.HistogramVec
	contextCompile prometheus.Histogram
	export         prometheus.Histogram
}

// newDurationHistograms registers the OBS-4 histograms on reg.
func newDurationHistograms(reg prometheus.Registerer) *durationHistograms {
	factory := promauto.With(reg)
	return &durationHistograms{
		upstreamProxy: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name: SeriesUpstreamProxyDuration,
				Help: "Seconds spent on an outbound upstream call (network proxy to the Hermes gateway, transport health probe), by outcome.",
				// SLO boundaries: 1s is the interactive latency line for a
				// proxied call an agent is waiting on, and 30s is the
				// handler's hard upstream budget (both the client Timeout
				// and the request-context timeout are 30s), so a sample
				// beyond 30 can only be a timeout outcome.
				Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15, 20, 30, 45, 60},
			},
			[]string{"outcome"},
		),
		contextCompile: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name: SeriesContextCompileDuration,
				Help: "Seconds spent compiling a context (the CPU-bound build behind a resume), excluding browser render time.",
				// SLO boundaries: 30 is the product "<30s resume" line and
				// stays a boundary here so the SLO is readable straight off
				// the compile histogram (the same 30 the resume histogram
				// carries); 1s and 5s are the shares of that budget a
				// single interactive compile is expected to stay inside.
				Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15, 20, 30, 45, 60},
			},
		),
		export: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name: SeriesExportDuration,
				Help: "Seconds spent writing a card-export snapshot.",
				// SLO boundaries: 10s is the interactive wait line for a
				// user-run `canopy export`, and 60s is the ceiling of the
				// short-lived CLI lane it belongs to (spec §3.9 — an export
				// that crosses a minute is the stuck-export case). Per §3.9
				// export must NOT grow its own scrape target, so the sample
				// rides the process registry: visible when export runs
				// in-process, inert (and free) in a standalone CLI run.
				Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
			},
		),
	}
}

// defaultDurations is the process-wide instance the exported Observe helpers
// write to: registered on the default registry, i.e. the families appear on
// the /metrics scrape.
var defaultDurations = newDurationHistograms(prometheus.DefaultRegisterer)

// ObserveUpstreamProxyDuration records one completed outbound upstream call.
// outcome is a CLOSED set (UpstreamProxyOutcomeOK/Error/Timeout); anything
// else is recorded as UpstreamProxyOutcomeError rather than creating a series.
func ObserveUpstreamProxyDuration(outcome string, seconds float64) {
	defaultDurations.observeUpstreamProxy(outcome, seconds)
}

// ObserveContextCompileDuration records one completed context compile.
func ObserveContextCompileDuration(seconds float64) {
	defaultDurations.observeContextCompile(seconds)
}

// UpstreamProxyOutcomeForStatus maps the status an upstream call produced onto
// the closed outcome set, so the label is derived from the result rather than
// from anything request-specific.
func UpstreamProxyOutcomeForStatus(code int) string {
	if code >= 400 {
		return UpstreamProxyOutcomeError
	}
	return UpstreamProxyOutcomeOK
}

// UpstreamProxyOutcomeForError classifies a failed upstream call: a call Canopy
// abandoned at its own deadline is a timeout, anything else is an error.
func UpstreamProxyOutcomeForError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return UpstreamProxyOutcomeTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return UpstreamProxyOutcomeTimeout
	}
	return UpstreamProxyOutcomeError
}

// ObserveExportDuration records one completed card export.
func ObserveExportDuration(seconds float64) {
	defaultDurations.observeExport(seconds)
}

func (d *durationHistograms) observeUpstreamProxy(outcome string, seconds float64) {
	switch outcome {
	case UpstreamProxyOutcomeOK, UpstreamProxyOutcomeError, UpstreamProxyOutcomeTimeout:
	default:
		outcome = UpstreamProxyOutcomeError
	}
	d.upstreamProxy.WithLabelValues(outcome).Observe(seconds)
}

func (d *durationHistograms) observeContextCompile(seconds float64) {
	d.contextCompile.Observe(seconds)
}

func (d *durationHistograms) observeExport(seconds float64) {
	d.export.Observe(seconds)
}

// Handler returns a Prometheus exposition handler backed by this metrics registry.
func (m *Metrics) Handler() http.Handler {
	gatherer := prometheus.DefaultGatherer
	if m != nil && m.gatherer != nil {
		gatherer = m.gatherer
	}
	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})
}

// SetTransportSnapshotSource installs the live source read by the scrape collector.
// Labels are generated from the collector's fixed transport/direction sets.
func (m *Metrics) SetTransportSnapshotSource(source func() TransportSnapshot) {
	if m == nil || m.transportCollector == nil {
		return
	}
	m.transportCollector.mu.Lock()
	m.transportCollector.source = source
	m.transportCollector.mu.Unlock()
}

// SetDBPoolSnapshot publishes one pgxpool.Stat() reading.
func (m *Metrics) SetDBPoolSnapshot(acquired, total int) {
	if m == nil {
		return
	}
	m.DBPoolAcquired.Set(float64(acquired))
	m.DBPoolTotal.Set(float64(total))
}

// SetFederationQueueDepth publishes the aggregate queue depth without peer labels.
func (m *Metrics) SetFederationQueueDepth(depth int) {
	if m != nil {
		m.FederationQueueDepth.Set(float64(depth))
	}
}

// IncRateLimited records a refusal using a bounded route label.
func (m *Metrics) IncRateLimited(route string) {
	if m == nil {
		return
	}
	if route == "" {
		route = "unmatched"
	}
	m.RateLimitedTotal.WithLabelValues(route).Inc()
}

// SetRateLimiterTrackedIPs publishes the number of limiter buckets.
func (m *Metrics) SetRateLimiterTrackedIPs(count int) {
	if m != nil {
		m.RateLimiterTrackedIPs.Set(float64(count))
	}
}

// SetDBLargestTableRows publishes one content table's row estimate.
//
// The `table` label is BOUNDED — the db-growth sampler only ever passes names
// from its own hardcoded allowlist (internal/server/db_growth_metrics.go) — and
// a blank name is dropped here rather than minting a label-less child. Rows are
// already clamped by the sampler: reltuples is -1 for a never-analyzed table,
// and a negative row count is not a fact, it is "no estimate", which reads as 0.
// Nil-receiver safe.
func (m *Metrics) SetDBLargestTableRows(table string, rows float64) {
	if m == nil || m.DBLargestTableRows == nil || table == "" {
		return
	}
	m.DBLargestTableRows.WithLabelValues(table).Set(rows)
}

// SetRelayCertExpiry publishes the relay TLS leaf certificate's notAfter as a
// unix timestamp.
//
// Callers must SKIP (not publish 0) when no certificate is configured: 0 would
// render as an expiry in 1970 and every alert on the series would fire forever.
// The series is a label-less GaugeVec precisely so "skip" leaves it absent from
// the registry instead of exposing a zero. Nil-receiver safe.
func (m *Metrics) SetRelayCertExpiry(notAfter time.Time) {
	if m == nil || m.RelayCertExpirySeconds == nil || notAfter.IsZero() {
		return
	}
	m.RelayCertExpirySeconds.WithLabelValues().Set(float64(notAfter.Unix()))
}

// AddRelayKeyRotations advances the rotation counter by delta, the increase the
// sampler observed in RelayHealth.Rotations since its previous cycle. A
// counter cannot decrease, so a delta of 0 (or a source that went backwards
// because the relay state was replaced) is a no-op here — the sampler is what
// rebases its own baseline. Nil-receiver safe.
func (m *Metrics) AddRelayKeyRotations(delta uint64) {
	if m == nil || m.RelayKeyRotationsTotal == nil || delta == 0 {
		return
	}
	m.RelayKeyRotationsTotal.Add(float64(delta))
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

// SetDBUp records the latest database readiness result. Nil-receiver safe so
// health remains independent of whether metrics are enabled.
func (m *Metrics) SetDBUp(up bool) {
	if m == nil || m.DBUp == nil {
		return
	}
	if up {
		m.DBUp.Set(1)
		return
	}
	m.DBUp.Set(0)
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

// SetBuildInfo publishes the running binary's identity as
// canopy_build_info{version,commit} = 1.
//
// Empty values fall back to the same placeholders /version and /health use
// ("dev"/"unknown"), so an unstamped build is labelled honestly instead of
// serving an empty label value. The vec is Reset first: a running process has
// exactly ONE build identity, and a re-stamp must never leave a ghost series
// behind for a PromQL join to pick up. Nil-receiver safe.
func (m *Metrics) SetBuildInfo(version, commit string) {
	if m == nil || m.BuildInfo == nil {
		return
	}
	if version == "" {
		version = "dev"
	}
	if commit == "" {
		commit = "unknown"
	}
	m.BuildInfo.Reset()
	m.BuildInfo.WithLabelValues(version, commit).Set(1)
}

// SetUpstreamGatewayUp records the latest upstream Hermes gateway
// reachability result. Nil-receiver safe, so a probe running against a server
// without metrics enabled is a no-op.
func (m *Metrics) SetUpstreamGatewayUp(up bool) {
	if m == nil || m.UpstreamGatewayUp == nil {
		return
	}
	if up {
		m.UpstreamGatewayUp.Set(1)
		return
	}
	m.UpstreamGatewayUp.Set(0)
}

// IncUpstreamGatewayCheckError counts one failed gateway health check.
//
// The kind label is a CLOSED set: anything outside the documented values folds
// into GatewayCheckErrorOther, so a caller (or a future error type) can never
// mint one series per distinct failure message. Nil-receiver safe.
func (m *Metrics) IncUpstreamGatewayCheckError(kind string) {
	if m == nil || m.UpstreamGatewayCheckErrors == nil {
		return
	}
	switch kind {
	case GatewayCheckErrorTimeout, GatewayCheckErrorNon200,
		GatewayCheckErrorConnRefused, GatewayCheckErrorOther:
	default:
		kind = GatewayCheckErrorOther
	}
	m.UpstreamGatewayCheckErrors.WithLabelValues(kind).Inc()
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
	{SeriesDBUp, "healthHandler: Metrics.SetDBUp from the database readiness probe"},
	{SeriesHTTPUnmatchedRequestsTotal, "MetricsMiddleware: Metrics.IncUnmatchedRequest when chi resolves no route pattern"},
	{SeriesResumeDuration, "ResumeMiddleware: Metrics.ObserveResumeDuration on a completed resume window"},
	{SeriesResumeStarted, "ResumeMiddleware: Metrics.IncResumeStarted when a resume window opens"},
	{SeriesBuildInfo, "router wiring (internal/server): Metrics.SetBuildInfo from currentBuildInfo()"},
	{SeriesUpstreamGatewayUp, "UpstreamGatewayProbe.Check: Metrics.SetUpstreamGatewayUp on every gateway poll"},
	{SeriesUpstreamGatewayCheckErrorsTotal, "UpstreamGatewayProbe.Check: Metrics.IncUpstreamGatewayCheckError on a failed gateway poll"},
	{SeriesTransportConnections, "transport snapshot collector: ConnectionManager.ConnectionCount per closed-set transport"},
	{SeriesTransportConnectTotal, "transport snapshot collector: TransportMetrics.ConnectAttempts"},
	{SeriesTransportReconnectsTotal, "transport snapshot collector: TransportMetrics.Reconnects"},
	{SeriesTransportMessagesTotal, "transport snapshot collector: TransportMetrics.MessagesSent/MessagesReceived"},
	{SeriesRelaySessions, "transport snapshot collector: the same RelayHealth source served as /health relay.sessions"},
	{SeriesRelayQueueDepth, "transport snapshot collector: relay transport adapter QueueDepth"},
	{SeriesRelayDropsTotal, "transport snapshot collector: TransportMetrics.RelayDrops"},
	{SeriesUpstreamProxyDuration, "telemetry.ObserveUpstreamProxyDuration: network proxy handler once per proxied request, transport health probe once per probe (both derived from the status they wrote)"},
	{SeriesContextCompileDuration, "telemetry.ObserveContextCompileDuration: compilerImpl.Compile on every compile attempt"},
	{SeriesExportDuration, "telemetry.ObserveExportDuration: cardexport.Export on every completed export"},
	{SeriesDBPoolAcquired, "server saturation sampler: pgxpool.Stat().AcquiredConns"},
	{SeriesDBPoolTotal, "server saturation sampler: pgxpool.Stat().MaxConns"},
	{SeriesFederationQueueDepth, "server saturation sampler: sum of RelayService.QueueDepth across peers"},
	{SeriesRateLimitedTotal, "RateLimit middleware refusal branch, by bounded chi route pattern"},
	{SeriesRateLimiterTrackedIPs, "RateLimit middleware publishes the pruned limiter bucket count"},
	{SeriesDBLargestTableRows, "server db-growth sampler (internal/server/db_growth_metrics.go): pg_class.reltuples per allowlisted table -> Metrics.SetDBLargestTableRows"},
	{SeriesRelayCertExpirySeconds, "server db-growth sampler (internal/server/db_growth_metrics.go): relay.CertificateExpiry (TLS leaf notAfter) -> Metrics.SetRelayCertExpiry"},
	{SeriesRelayKeyRotationsTotal, "server db-growth sampler (internal/server/db_growth_metrics.go): RelayHealth().Rotations delta -> Metrics.AddRelayKeyRotations"},
}

// DeclaredSeriesWriters returns a copy of the writer registry so tests (and
// only tests) can enumerate declared series against their writers.
func DeclaredSeriesWriters() []SeriesWriter {
	out := make([]SeriesWriter, len(declaredSeriesWriters))
	copy(out, declaredSeriesWriters)
	return out
}
