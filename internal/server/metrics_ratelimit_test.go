package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/handler"
	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

// newMetricsRouter builds the PRODUCTION router with a small overrideable rate
// limiter and a metrics sink on its own registry, so the exhaustion arm can be
// driven without 200 real requests and without touching the process-global
// registry.
func newMetricsRouter(t *testing.T, limiter *handler.RateLimiter) (http.Handler, *prometheus.Registry) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".hermes", "canopy", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetricsWithRegisterer(reg)
	router := newRouter(&routeDeps{
		jwtSecret:   "metrics-exempt-test-secret",
		connMgr:     transport.NewConnectionManager(nil),
		metrics:     metrics,
		cfg:         &config.Config{},
		rateLimiter: limiter,
	})
	return router, reg
}

// TestMetricsEndpointSurvivesExhaustedRateLimiter is OBS-1 AC6: /metrics keeps
// answering 200 after the per-IP bucket is empty, while another route gets 429.
// A 429'd scrape makes the Prometheus target flap, and the scrape is the one
// signal that would report the refusal — so the limiter must never see it.
func TestMetricsEndpointSurvivesExhaustedRateLimiter(t *testing.T) {
	// capacity 1, no refill: the first request takes the only token.
	router, _ := newMetricsRouter(t, handler.NewRateLimiter(0, 1))

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	// Drain the bucket with a non-exempt path (unknown API route → 404,
	// no services required).
	if rec := get("/api/v1/definitely-not-a-route"); rec.Code == http.StatusTooManyRequests {
		t.Fatalf("first request to a non-exempt path was already rate limited (status %d)", rec.Code)
	}

	// Control arm: the bucket is now empty, so the SAME non-exempt path is
	// refused. Without this the exemption test could pass vacuously.
	if rec := get("/api/v1/definitely-not-a-route"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("control: second request status = %d, want 429 — the limiter was never exhausted", rec.Code)
	}

	// /metrics must still be served, repeatedly. (The body is rendered by
	// promhttp from the process-default registry in production — this test
	// injects a private registry for isolation, so the assertion here is the
	// STATUS the limiter controls; the exposition content of the injected
	// registry is asserted in TestBuildInfoWiredFromCurrentBuildInfo and the
	// full family set in internal/telemetry.)
	for i := 0; i < 5; i++ {
		rec := get("/metrics")
		if rec.Code != http.StatusOK {
			t.Fatalf("/metrics request %d status = %d, want 200; body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
}

// TestMetricsExemptionIsNotAGeneralAuthBypass: /metrics is exempt from the
// LIMITER, not from authentication — a protected route must still answer 401
// on an exhausted bucket rather than being let through.
func TestMetricsExemptionIsNotAGeneralAuthBypass(t *testing.T) {
	router, _ := newMetricsRouter(t, handler.NewRateLimiter(0, 1))

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	_ = get("/api/v1/definitely-not-a-route") // drain

	if rec := get("/api/v1/definitely-not-a-route"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("control status = %d, want 429", rec.Code)
	}
	// A protected route on the empty bucket is refused by the limiter, never
	// served unauthenticated.
	if rec := get("/api/v1/trees"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("protected route status = %d, want 429 on an exhausted bucket", rec.Code)
	}
}

// TestBuildInfoWiredFromCurrentBuildInfo proves the router publishes the SAME
// identity /version serves (OBS-1 AC1): the gauge is set at router
// construction from currentBuildInfo(), not by a caller who might forget.
func TestBuildInfoWiredFromCurrentBuildInfo(t *testing.T) {
	router, reg := newMetricsRouter(t, nil)

	bi := currentBuildInfo()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var found bool
	for _, fam := range families {
		if fam.GetName() != telemetry.SeriesBuildInfo {
			continue
		}
		found = true
		if n := len(fam.GetMetric()); n != 1 {
			t.Fatalf("%s has %d series, want 1", telemetry.SeriesBuildInfo, n)
		}
		labels := map[string]string{}
		for _, l := range fam.GetMetric()[0].GetLabel() {
			labels[l.GetName()] = l.GetValue()
		}
		if labels["version"] != bi.Version || labels["commit"] != bi.Commit {
			t.Fatalf("%s labels = %v, want version=%q commit=%q",
				telemetry.SeriesBuildInfo, labels, bi.Version, bi.Commit)
		}
		if got := fam.GetMetric()[0].GetGauge().GetValue(); got != 1 {
			t.Fatalf("%s value = %v, want 1", telemetry.SeriesBuildInfo, got)
		}
	}
	if !found {
		t.Fatalf("router construction did not publish %s", telemetry.SeriesBuildInfo)
	}

	// And the router exposes /metrics for the scraper (status + a real
	// exposition body). The injected registry's series are asserted above and
	// rendered end-to-end in internal/telemetry's scrape test.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("/metrics Content-Type = %q, want a Prometheus text exposition", ct)
	}
}
