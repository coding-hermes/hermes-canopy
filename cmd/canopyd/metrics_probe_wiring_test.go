package main

import (
	"os"
	"strings"
	"testing"
)

// TestMainStartsUpstreamGatewayProbe is a composition-root guard for OBS-1 AC2.
//
// The probe's BEHAVIOUR (gauge flips, error counter, cadence, shutdown) is
// covered by internal/telemetry/gateway_probe_test.go against a real HTTP
// server. What no unit test can reach is the wiring: main() is a monolith with
// no seam, so a future refactor can silently drop the
// telemetry.StartUpstreamGatewayProbe call and leave canopy_upstream_gateway_up
// permanently 0 — a false "gateway is down" page. This asserts the call site is
// still present and still gets a gateway client built from the SAME base URL the
// run client uses (cfg.GatewayBaseURL), so the gauge cannot start probing a
// different upstream than the product actually talks to.
func TestMainStartsUpstreamGatewayProbe(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)

	for _, want := range []string{
		"telemetry.StartUpstreamGatewayProbe(ctx, telemetry.NewUpstreamGatewayProbe(metrics, gwProbe.Health))",
		"gateway.NewClient(cfg.GatewayBaseURL, cfg.GatewayAPIKey)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("main.go no longer contains %q — the upstream gateway gauge would never move (OBS-1 AC2)", want)
		}
	}

	// The probe must run only when metrics are enabled; a disabled-metrics
	// deployment should not make network calls for a series nobody scrapes.
	metricsBlock := strings.Index(body, "if cfg.MetricsEnabled {")
	if metricsBlock < 0 {
		t.Fatal("main.go no longer gates telemetry on cfg.MetricsEnabled")
	}
	probeStart := strings.Index(body, "telemetry.StartUpstreamGatewayProbe(")
	if probeStart < metricsBlock {
		t.Fatal("the gateway probe is started OUTSIDE the cfg.MetricsEnabled block")
	}
}
