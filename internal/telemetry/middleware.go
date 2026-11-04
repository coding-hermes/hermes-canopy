// Package telemetry provides chi middleware for recording Prometheus metrics.
package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// unmatchedRouteLabel is the path-label value for a request that matched no
// chi route. The raw URL path is NEVER used as a label value: a 404 probe
// would otherwise mint one series per probed path (docs/observability-spec.md
// §4, defect D3).
const unmatchedRouteLabel = "unmatched"

// MetricsMiddleware returns a chi middleware that records request duration, request
// count, and active connection metrics. It must be placed after chi's URL params
// middleware so route patterns are resolved.
//
// The path label is chi's RoutePattern — the mounted router pattern — and
// nothing else. A request that matches no route (chi leaves RoutePattern
// empty) is folded into the "unmatched" bucket AND counted separately in
// canopy_http_unmatched_requests_total{method=...}, so probe traffic is
// visible without the probed path ever appearing in a label value.
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

			// The route pattern is the ONLY path label. chi sets it during
			// routing; an unmatched request (chi's NotFound) leaves it
			// empty, and those samples land in the unmatched bucket plus
			// the dedicated unmatched counter — never the raw r.URL.Path.
			routePattern := unmatchedRouteLabel
			matched := false
			rctx := chi.RouteContext(r.Context())
			if rctx != nil && rctx.RoutePattern() != "" {
				routePattern = rctx.RoutePattern()
				matched = true
			}
			if !matched {
				m.IncUnmatchedRequest(r.Method)
			}

			status := strconv.Itoa(ww.Status())

			m.RequestDuration.WithLabelValues(r.Method, routePattern).Observe(duration)
			m.RequestTotal.WithLabelValues(r.Method, routePattern, status).Inc()
		})
	}
}
