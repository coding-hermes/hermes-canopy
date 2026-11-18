package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestRateLimitMetricsCountRefusalAndTrackedBuckets(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetricsWithRegisterer(reg)
	limiter := NewRateLimiter(0, 1)
	h := RateLimit(limiter, metrics)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i, wantStatus := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/trees/secret-tree-id", nil)
		req.RemoteAddr = "192.0.2.11:1234"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != wantStatus {
			t.Fatalf("request %d status = %d, want %d", i, rr.Code, wantStatus)
		}
	}
	counter := &dto.Metric{}
	if err := metrics.RateLimitedTotal.WithLabelValues("unmatched").Write(counter); err != nil {
		t.Fatal(err)
	}
	if got := counter.GetCounter().GetValue(); got != 1 {
		t.Fatalf("rate-limited counter = %v, want 1", got)
	}
	tracked := &dto.Metric{}
	if err := metrics.RateLimiterTrackedIPs.Write(tracked); err != nil {
		t.Fatal(err)
	}
	if got := tracked.GetGauge().GetValue(); got != 1 {
		t.Fatalf("tracked IP gauge = %v, want 1", got)
	}
	body, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range body {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetValue() == "192.0.2.11" || label.GetValue() == "secret-tree-id" {
					t.Fatalf("sensitive/request-derived label value %q", label.GetValue())
				}
			}
		}
	}
}

func TestRateLimiterPrunesVisitorsIdleMoreThanTenMinutes(t *testing.T) {
	limiter := NewRateLimiter(1, 2)
	now := time.Now()
	limiter.mu.Lock()
	limiter.visits["idle"] = &visitor{tokens: 1, lastCheck: now.Add(-rateLimiterVisitorIdleTTL - time.Second)}
	limiter.visits["active"] = &visitor{tokens: 1, lastCheck: now.Add(-rateLimiterVisitorIdleTTL + time.Second)}
	limiter.mu.Unlock()

	limiter.allowAt("new", now)
	if got := limiter.trackedIPs(); got != 2 {
		t.Fatalf("tracked visitors after pruning = %d, want active+new = 2", got)
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if _, exists := limiter.visits["idle"]; exists {
		t.Fatal("idle visitor remained after pruning")
	}
	if _, exists := limiter.visits["active"]; !exists {
		t.Fatal("active visitor was pruned")
	}
}
