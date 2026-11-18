package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/federation"
	"github.com/coding-hermes/hermes-canopy/internal/handler"
	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

func TestFederationQueueSamplerSumsPeersWithoutLabels(t *testing.T) {
	peers := []*federation.FederationPeer{{ID: uuid.New()}, {ID: uuid.New()}}
	source := fakeFederationQueueSource{peers: peers, depths: map[uuid.UUID]int{peers[0].ID: 3, peers[1].ID: 5}}
	got, err := aggregateFederationQueueDepth(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if got != 8 {
		t.Fatalf("aggregate queue depth=%d, want 8", got)
	}
}

type fakeFederationQueueSource struct {
	peers  []*federation.FederationPeer
	depths map[uuid.UUID]int
}

func (f fakeFederationQueueSource) ListAllPeers(context.Context) ([]*federation.FederationPeer, error) {
	return f.peers, nil
}

func (f fakeFederationQueueSource) QueueDepth(_ context.Context, peerID uuid.UUID) (int, error) {
	return f.depths[peerID], nil
}

func TestOBS3MetricsFamiliesAndRateLimitOnProductionRouter(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetricsWithRegisterer(reg)
	metrics.SetDBPoolSnapshot(2, 4)
	metrics.SetFederationQueueDepth(7)
	limiter := handler.NewRateLimiter(0, 1)
	router := newRouter(&routeDeps{metrics: metrics, rateLimiter: limiter, connMgr: transport.NewConnectionManager(nil, nil), cfg: &config.Config{}})
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/health/metrics-test", nil)
		req.RemoteAddr = "198.51.100.8:443"
		router.ServeHTTP(httptest.NewRecorder(), req)
	}
	scrape := httptest.NewRecorder()
	router.ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if scrape.Code != http.StatusOK {
		t.Fatalf("/metrics status=%d: %s", scrape.Code, scrape.Body.String())
	}
	families := scrapeFamilies(scrape.Body.String())
	want := []string{
		telemetry.SeriesDBPoolAcquired,
		telemetry.SeriesDBPoolTotal,
		telemetry.SeriesFederationQueueDepth,
		telemetry.SeriesRateLimitedTotal,
		telemetry.SeriesRateLimiterTrackedIPs,
	}
	for _, name := range want {
		if !families[name] {
			t.Errorf("missing metric family %s", name)
		}
	}
	body := scrape.Body.String()
	if got := sampleValue(t, body, telemetry.SeriesDBPoolAcquired, ""); got != 2 {
		t.Errorf("pool acquired=%v, want 2", got)
	}
	if got := sampleValue(t, body, telemetry.SeriesDBPoolTotal, ""); got != 4 || got < 1 {
		t.Errorf("pool total=%v, want 4 and >=1", got)
	}
	if got := sampleValue(t, body, telemetry.SeriesFederationQueueDepth, ""); got != 7 {
		t.Errorf("federation depth=%v, want 7", got)
	}
	if !strings.Contains(body, telemetry.SeriesRateLimitedTotal+`{route="unmatched"} 1`) {
		t.Errorf("rate-limited counter did not increment: %s", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "canopy_ratelimit") && !strings.HasPrefix(line, "canopy_federation") {
			continue
		}
		if strings.Contains(line, "198.51.100.8") || strings.Contains(line, "metrics-test") {
			t.Errorf("request or IP value exposed in metric line: %s", line)
		}
	}
}
