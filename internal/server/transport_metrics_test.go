package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/relay"
	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

type transportMetricsHealthProbe struct{ sessions int }

func (p transportMetricsHealthProbe) SchemaVersion(context.Context) (int64, error) { return 0, nil }
func (p transportMetricsHealthProbe) EmbeddedMigrations() int64                    { return 0 }
func (p transportMetricsHealthProbe) RelayHealth() relay.RelayHealth {
	return relay.RelayHealth{Sessions: p.sessions}
}

func TestTransportAndRelayMetricsThroughProductionMetricsRoute(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetricsWithRegisterer(reg)
	relayAdapter := transport.NewRelayService(4)
	manager := transport.NewConnectionManager(nil, relayAdapter)
	probe := transportMetricsHealthProbe{sessions: 3}
	router := newRouter(&routeDeps{
		healthProbe: probe,
		jwtSecret:   "metrics-test-secret",
		connMgr:     manager,
		metrics:     metrics,
		cfg:         &config.Config{},
	})

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	before := get("/metrics").Body.String()

	conn, err := relayAdapter.Connect(context.Background(), transport.ConnectOptions{Target: "safe-test-peer", TransportType: transport.TransportRelay})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.OnConnect(conn); err != nil {
		t.Fatal(err)
	}
	if err := manager.RouteMessage(context.Background(), "safe-test-peer", &transport.Message{Opcode: transport.OpHeartbeat, Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("RouteMessage: %v", err)
	}

	scrape := get("/metrics")
	if scrape.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d: %s", scrape.Code, scrape.Body.String())
	}
	families := scrapeFamilies(scrape.Body.String())
	want := []string{
		telemetry.SeriesTransportConnections, telemetry.SeriesTransportConnectTotal,
		telemetry.SeriesTransportReconnectsTotal, telemetry.SeriesTransportMessagesTotal,
		telemetry.SeriesRelaySessions, telemetry.SeriesRelayQueueDepth, telemetry.SeriesRelayDropsTotal,
	}
	for _, name := range want {
		if !families[name] {
			t.Errorf("/metrics missing family %s", name)
		}
	}
	beforeConnect := sampleValue(t, before, telemetry.SeriesTransportConnectTotal, `transport="relay"`)
	if got := sampleValue(t, scrape.Body.String(), telemetry.SeriesTransportConnectTotal, `transport="relay"`); got <= beforeConnect {
		t.Errorf("relay connect counter did not advance: before=%v after=%v", beforeConnect, got)
	}
	beforeSent := sampleValue(t, before, telemetry.SeriesTransportMessagesTotal, `transport="relay",direction="sent"`)
	if got := sampleValue(t, scrape.Body.String(), telemetry.SeriesTransportMessagesTotal, `transport="relay",direction="sent"`); got <= beforeSent {
		t.Errorf("relay sent counter did not advance: before=%v", beforeSent)
	}
	if got := sampleValue(t, scrape.Body.String(), telemetry.SeriesTransportConnections, `transport="relay"`); got != 1 {
		t.Errorf("relay connections = %v, want 1", got)
	}

	health := get("/health")
	var healthBody struct {
		Relay struct {
			Sessions int `json:"sessions"`
		} `json:"relay"`
	}
	if err := json.Unmarshal(health.Body.Bytes(), &healthBody); err != nil {
		t.Fatal(err)
	}
	if got := sampleValue(t, scrape.Body.String(), telemetry.SeriesRelaySessions, ""); got != float64(healthBody.Relay.Sessions) {
		t.Errorf("relay sessions metric = %v, /health relay.sessions = %d", got, healthBody.Relay.Sessions)
	}
	if got := healthBody.Relay.Sessions; got != probe.sessions {
		t.Fatalf("health sessions = %d, want probe source %d", got, probe.sessions)
	}

	uuidLike := regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	labelValue := regexp.MustCompile(`([a-z_]+)="([^"]*)"`)
	transportTypes := map[string]bool{"relay": true, "sse": true, "webrtc": true, "nats": true, "redis": true}
	for _, line := range strings.Split(scrape.Body.String(), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		for _, match := range labelValue.FindAllStringSubmatch(line, -1) {
			labelName, value := match[1], match[2]
			if uuidLike.MatchString(value) {
				t.Errorf("UUID-like metric label value %q in %s", value, line)
			}
			if strings.HasPrefix(line, "canopy_transport_") {
				switch labelName {
				case "transport":
					if !transportTypes[value] {
						t.Errorf("unbounded transport label %q", value)
					}
				case "direction":
					if value != "sent" && value != "received" {
						t.Errorf("unbounded direction label %q", value)
					}
				default:
					t.Errorf("unexpected transport metric label %q", labelName)
				}
			}
		}
	}
}

func scrapeFamilies(body string) map[string]bool {
	families := make(map[string]bool)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# HELP ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				families[parts[2]] = true
			}
		}
	}
	return families
}

func sampleValue(t *testing.T, body, name, labels string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		if labels != "" {
			if !strings.Contains(line, "{") {
				continue
			}
			matched := true
			for _, label := range strings.Split(labels, ",") {
				matched = matched && strings.Contains(line, label)
			}
			if !matched {
				continue
			}
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			value, err := strconv.ParseFloat(fields[len(fields)-1], 64)
			if err == nil {
				return value
			}
		}
	}
	t.Fatalf("sample for %s{%s} missing from scrape", name, labels)
	return 0
}
