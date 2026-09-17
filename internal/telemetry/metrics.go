// Package telemetry provides Prometheus metrics and structured request logging for canopyd.
package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics exported by canopyd.
type Metrics struct {
	// RequestDuration tracks HTTP request latency in seconds.
	RequestDuration *prometheus.HistogramVec
	// RequestTotal counts HTTP requests by method, path, and status code.
	RequestTotal *prometheus.CounterVec
	// ActiveConnections tracks the current number of in-flight HTTP requests.
	ActiveConnections prometheus.Gauge
	// TreeCount tracks the total number of trees (wired at startup).
	TreeCount prometheus.Gauge
	// NodeCount tracks the total number of nodes (wired at startup).
	NodeCount prometheus.Gauge
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

// NewMetrics creates and registers all Prometheus metrics.
func NewMetrics() *Metrics {
	// promauto.NewHistogram returns the prometheus.Histogram interface (only
	// the *Vec constructors return pointers), so the resume histogram is
	// registered by promauto and then held by address to keep the field type
	// the *prometheus.Histogram the resume contract names.
	resumeDuration := promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name: "resume_duration_seconds",
			Help: "Seconds between a user's first tree read after an idle gap and the compiled context they resume with.",
			// 30 is the product SLO line ("resume work in <30 seconds")
			// and must stay a bucket boundary so the SLO is directly
			// readable off the histogram.
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15, 20, 30, 45, 60, 120},
		},
	)
	m := &Metrics{
		RequestDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "request_duration_seconds",
				Help:    "HTTP request latency in seconds.",
				Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"method", "path"},
		),
		RequestTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "request_total",
				Help: "Total number of HTTP requests.",
			},
			[]string{"method", "path", "status"},
		),
		ActiveConnections: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "active_connections",
				Help: "Current number of in-flight HTTP requests.",
			},
		),
		TreeCount: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "tree_count",
				Help: "Total number of trees.",
			},
		),
		NodeCount: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "node_count",
				Help: "Total number of nodes.",
			},
		),
		ResumeDuration: &resumeDuration,
		ResumeStarted: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "resume_started_total",
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
