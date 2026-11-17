package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/coding-hermes/hermes-canopy/internal/relay"
	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
)

type healthReadinessProbe struct {
	err error
}

func (p healthReadinessProbe) SchemaVersion(context.Context) (int64, error) { return 48, nil }
func (p healthReadinessProbe) EmbeddedMigrations() int64                    { return 48 }
func (p healthReadinessProbe) Readiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.err
}
func (p healthReadinessProbe) RelayHealth() relay.RelayHealth {
	return relay.RelayHealth{Mode: "air_gapped", Status: "disabled"}
}

func TestHealthHandlerDatabaseReadinessBranches(t *testing.T) {
	for _, tc := range []struct {
		name       string
		probeErr   error
		wantCode   int
		wantStatus string
		wantDB     string
	}{
		{name: "healthy", wantCode: http.StatusOK, wantStatus: "ok", wantDB: "ready"},
		{name: "unavailable", probeErr: errors.New("connection refused"), wantCode: http.StatusServiceUnavailable, wantStatus: "degraded", wantDB: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), healthSchemaKey{}, healthReadinessProbe{err: tc.probeErr})
			for _, path := range []string{"/health", "/healthz"} {
				req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
				w := httptest.NewRecorder()
				healthHandler(w, req)

				if w.Code != tc.wantCode {
					t.Fatalf("%s status = %d, want %d", path, w.Code, tc.wantCode)
				}
				var body map[string]any
				if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
					t.Fatalf("decode %s response: %v", path, err)
				}
				if body["status"] != tc.wantStatus {
					t.Errorf("%s status body = %v, want %q", path, body["status"], tc.wantStatus)
				}
				if body["database_status"] != tc.wantDB {
					t.Errorf("%s database_status = %v, want %q", path, body["database_status"], tc.wantDB)
				}
				if body["service"] != "canopyd" || body["schema_version"] != float64(48) || body["embedded_migrations"] != float64(48) {
					t.Errorf("%s lost identity fields: %#v", path, body)
				}
			}
		})
	}
}

func TestHealthHandlerUnavailableCannotSatisfyStatusOKAssertion(t *testing.T) {
	ctx := context.WithValue(context.Background(), healthSchemaKey{}, healthReadinessProbe{err: errors.New("database unavailable")})
	w := httptest.NewRecorder()
	healthHandler(w, httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(ctx))

	var body map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if w.Code == http.StatusOK || body["status"] == "ok" {
		t.Fatalf("failed database probe must not satisfy old status-ok assertion: code=%d body=%v", w.Code, body)
	}
}

// dbUpTestMetrics returns a Metrics value holding an UNREGISTERED gauge, so a
// test can assert the health handler wrote canopy_db_up without touching (or
// colliding with) the default Prometheus registry.
func dbUpTestMetrics() (*telemetry.Metrics, prometheus.Gauge) {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: telemetry.SeriesDBUp,
		Help: "test-only canopy_db_up",
	})
	return &telemetry.Metrics{DBUp: gauge}, gauge
}

// gaugeValue reads a gauge's current value through the same client-model
// encoding /metrics serves, so the assertion is on the scraped number.
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("write gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

// TestHealthHandlerWritesDBUpGauge pins the two halves of CAN-OPS-01
// acceptance criterion 4 on the handler itself: the health path writes
// canopy_db_up 1 when the probe succeeds and 0 when it fails, and the same
// answer appears in the body's checks map.
func TestHealthHandlerWritesDBUpGauge(t *testing.T) {
	// The accepted operator command is `curl -s :8091/metrics | grep
	// '^canopy_db_up 0'`, so the series name itself is part of the contract.
	if telemetry.SeriesDBUp != "canopy_db_up" {
		t.Fatalf("SeriesDBUp = %q, want canopy_db_up", telemetry.SeriesDBUp)
	}
	for _, tc := range []struct {
		name      string
		probeErr  error
		wantCode  int
		wantGauge float64
		wantCheck string
	}{
		{name: "healthy", wantCode: http.StatusOK, wantGauge: 1, wantCheck: "up"},
		{name: "unavailable", probeErr: errors.New("connection refused"), wantCode: http.StatusServiceUnavailable, wantGauge: 0, wantCheck: "down"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics, gauge := dbUpTestMetrics()
			ctx := context.WithValue(context.Background(), healthSchemaKey{}, healthReadinessProbe{err: tc.probeErr})
			ctx = context.WithValue(ctx, healthMetricsKey{}, metrics)
			w := httptest.NewRecorder()
			healthHandler(w, httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(ctx))

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
			if got := gaugeValue(t, gauge); got != tc.wantGauge {
				t.Errorf("%s = %v, want %v", telemetry.SeriesDBUp, got, tc.wantGauge)
			}
			var body map[string]any
			if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			checks, ok := body["checks"].(map[string]any)
			if !ok {
				t.Fatalf("checks map missing/wrong shape: %#v", body["checks"])
			}
			if checks["db"] != tc.wantCheck {
				t.Errorf("checks.db = %v, want %q", checks["db"], tc.wantCheck)
			}
		})
	}
}

// dialReadinessProbe is a real readiness probe: it dials a TCP address the way
// a database ping would. Tests point it at 127.0.0.1:1 (nothing listens) so the
// failing branch is exercised with an actual network error, never a stub error
// and never a live database.
type dialReadinessProbe struct {
	addr     string
	timeout  time.Duration
	embedded int64
}

func (p dialReadinessProbe) SchemaVersion(ctx context.Context) (int64, error) {
	return p.embedded, ctx.Err()
}
func (p dialReadinessProbe) EmbeddedMigrations() int64 { return p.embedded }
func (p dialReadinessProbe) Readiness(ctx context.Context) error {
	dialer := net.Dialer{Timeout: p.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

// TestHealthHandlerClosedPortProbeReportsUnavailable is the reproduction of the
// live defect: with the database genuinely unreachable, /health must not answer
// status "ok" (CAN-OPS-01 falsifier). The probe is a real closed-port dial, the
// gauge must be 0, and the body must not leak a DSN or credentials.
func TestHealthHandlerClosedPortProbeReportsUnavailable(t *testing.T) {
	metrics, gauge := dbUpTestMetrics()
	probe := dialReadinessProbe{addr: "127.0.0.1:1", timeout: time.Second, embedded: 48}
	ctx := context.WithValue(context.Background(), healthSchemaKey{}, probe)
	ctx = context.WithValue(ctx, healthMetricsKey{}, metrics)

	start := time.Now()
	w := httptest.NewRecorder()
	healthHandler(w, httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(ctx))
	elapsed := time.Since(start)

	if elapsed > 4*time.Second {
		t.Fatalf("health handler took %s against a dead database, want <= 4s", elapsed)
	}
	if w.Code == http.StatusOK {
		t.Fatalf("closed-port probe answered HTTP 200, want non-200")
	}
	var body map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] == "ok" {
		t.Fatalf("closed-port probe answered status ok: %#v", body)
	}
	if got := gaugeValue(t, gauge); got != 0 {
		t.Errorf("%s = %v with the database down, want 0", telemetry.SeriesDBUp, got)
	}
	raw := w.Body.String()
	for _, forbidden := range []string{"postgres://", "password", "sslmode"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("health body leaks %q: %s", forbidden, raw)
		}
	}
}

// blockingReadinessProbe never returns before its context deadline, standing in
// for a database host that accepts the connection and then hangs.
type blockingReadinessProbe struct{}

func (blockingReadinessProbe) SchemaVersion(ctx context.Context) (int64, error) {
	return -1, ctx.Err()
}
func (blockingReadinessProbe) EmbeddedMigrations() int64 { return -1 }
func (blockingReadinessProbe) Readiness(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestHealthHandlerProbeIsBounded proves the "can never hang" half of the
// contract: a probe that hangs is cut off by healthReadinessTimeout and still
// produces a degraded answer.
func TestHealthHandlerProbeIsBounded(t *testing.T) {
	ctx := context.WithValue(context.Background(), healthSchemaKey{}, blockingReadinessProbe{})
	start := time.Now()
	w := httptest.NewRecorder()
	healthHandler(w, httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(ctx))
	elapsed := time.Since(start)

	if elapsed > healthReadinessTimeout+time.Second {
		t.Fatalf("hanging probe was not bounded: handler took %s (timeout %s)", elapsed, healthReadinessTimeout)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("hanging probe answered %d, want 503", w.Code)
	}
}

// ch:trace row=CAN-OPS-01 spec=none:live-incident-and-acceptance wave=none:serial-test test=internal/server:TestHealthHandlerDatabaseReadinessBranches,TestHealthHandlerWritesDBUpGauge,TestHealthHandlerClosedPortProbeReportsUnavailable,TestHealthHandlerProbeIsBounded doc=none:code-change evidence=commit-and-test-output witness=none:owner-gated-live-probe verdict=none:foreman-binds-after-judge commit=none:worker-commit memory=none:foreman-writes
