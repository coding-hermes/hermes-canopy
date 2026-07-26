// Package telemetry provides chi middleware for recording Prometheus metrics.
package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// MetricsMiddleware returns a chi middleware that records request duration, request
// count, and active connection metrics. It must be placed after chi's URL params
// middleware so route patterns are resolved.
func MetricsMiddleware(m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.ActiveConnections.Inc()
			defer m.ActiveConnections.Dec()

			start := time.Now()

			// Wrap the response writer to capture the status code.
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			duration := time.Since(start).Seconds()

			// Derive a route pattern for the path label if available.
			routePattern := r.URL.Path
			rctx := chi.RouteContext(r.Context())
			if rctx != nil && rctx.RoutePattern() != "" {
				routePattern = rctx.RoutePattern()
			}

			status := strconv.Itoa(ww.Status())

			m.RequestDuration.WithLabelValues(r.Method, routePattern).Observe(duration)
			m.RequestTotal.WithLabelValues(r.Method, routePattern, status).Inc()
		})
	}
}
