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
}

// NewMetrics creates and registers all Prometheus metrics.
func NewMetrics() *Metrics {
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
	}
	return m
}
