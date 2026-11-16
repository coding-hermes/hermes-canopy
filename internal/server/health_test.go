package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/relay"
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

// ch:trace row=CAN-OPS-01 spec=none:live-incident-and-acceptance wave=none:serial-test test=internal/server:TestHealthHandlerDatabaseReadinessBranches doc=none:code-change evidence=commit-and-test-output witness=none:owner-gated-live-probe verdict=none:foreman-binds-after-judge commit=none:worker-commit memory=none:foreman-writes
