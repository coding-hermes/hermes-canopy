package server

import (
	"encoding/json"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/relay"
)

// redTestProbe is the minimal HealthDB fake: the identity fields surface even
// with no database behind the handler.
type redTestProbe struct{}

func (redTestProbe) SchemaVersion(_ context.Context) (int64, error) { return 48, nil }
func (redTestProbe) EmbeddedMigrations() int64                      { return 48 }
func (redTestProbe) RelayHealth() relay.RelayHealth {
	return relay.RelayHealth{Mode: "air_gapped", Status: "disabled"}
}

// TestVersionHandlerReportsBuildIdentity: GET /version must name the exact
// build — version, commit, and build timestamp — not a hardcoded literal
// (R14-03 / item 77). A deploy is only verifiable if a RUNNING daemon answers
// with the identity stamped into the binary at build time.
func TestVersionHandlerReportsBuildIdentity(t *testing.T) {
	old := buildVersion
	buildVersion = "v0.1.0-test"
	buildCommit = "deadbeef"
	buildTime = "2026-09-22T19:00:00Z"
	t.Cleanup(func() { buildVersion, buildCommit, buildTime = old, "", "" })

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	w := httptest.NewRecorder()
	versionHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got, want := body["version"], "v0.1.0-test"; got != want {
		t.Errorf("version = %q, want %q", got, want)
	}
	if got, want := body["commit"], "deadbeef"; got != want {
		t.Errorf("commit = %q, want %q", got, want)
	}
	if got, want := body["build_time"], "2026-09-22T19:00:00Z"; got != want {
		t.Errorf("build_time = %q, want %q", got, want)
	}
}

// TestVersionHandlerFallbackIsDev: with nothing stamped (plain `go build`),
// the fields must be present and honest "dev"/"unknown" rather than missing —
// the old hardcoded handler's exact shape, now self-describing.
func TestVersionHandlerFallbackIsDev(t *testing.T) {
	oldV, oldC, oldT := buildVersion, buildCommit, buildTime
	buildVersion, buildCommit, buildTime = "dev", "", ""
	t.Cleanup(func() { buildVersion, buildCommit, buildTime = oldV, oldC, oldT })

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	w := httptest.NewRecorder()
	versionHandler(w, req)

	var body map[string]string
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got, want := body["version"], "dev"; got != want {
		t.Errorf("version = %q, want %q", got, want)
	}
	if got, want := body["commit"], "unknown"; got != want {
		t.Errorf("commit = %q, want %q", got, want)
	}
	if got, want := body["build_time"], "unknown"; got != want {
		t.Errorf("build_time = %q, want %q", got, want)
	}
}

// TestHealthHandlerCarriesBuildIdentity: /health must carry the same identity
// so a running daemon can be identified without filesystem access to the
// binary. Same values on both surfaces, always (item 77a "make the two agree").
func TestHealthHandlerCarriesBuildIdentity(t *testing.T) {
	old := buildVersion
	buildVersion = "v0.1.0-test"
	buildCommit = "feedface"
	buildTime = "2026-09-22T19:00:00Z"
	t.Cleanup(func() { buildVersion, buildCommit, buildTime = old, "", "" })

	w := httptest.NewRecorder()
	healthHandler(w, httptest.NewRequest(http.MethodGet, "/health", nil))

	var body map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got, want := body["version"], "v0.1.0-test"; got != want {
		t.Errorf("health version = %q, want %q", got, want)
	}
	if got, want := body["commit"], "feedface"; got != want {
		t.Errorf("health commit = %q, want %q", got, want)
	}
	if got, want := body["build_time"], "2026-09-22T19:00:00Z"; got != want {
		t.Errorf("health build_time = %q, want %q", got, want)
	}
}
