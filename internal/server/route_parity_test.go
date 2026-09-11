package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

// normalizeChiPattern canonicalizes a chi route pattern for parity checks:
// every {param} placeholder collapses to {} (so parameter-name drift cannot
// hide a missing route), mount-point "/*" wildcards and duplicate/trailing
// slashes collapse, and the string is trimmed.
func normalizeChiPattern(p string) string {
	p = regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(p, "{}")
	p = strings.ReplaceAll(p, "/*", "")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return strings.TrimSuffix(p, "/")
}

// TestRouteParityDocumentedNodeRoutes walks the REAL production router — the
// same newRouter seam func New uses — and asserts the node routes documented
// in docs/API.md and specs/SPEC-API-03-node-crud-endpoints.md are actually
// mounted (GAP-065). DB-free by design: services may be nil at wiring time
// because handlers only dereference them inside request handlers.
//
// Status-code probes are a false oracle here: the auth middleware runs before
// chi's NotFound, so unmatched paths also answer 401. The router must be
// enumerated with chi.Walk instead of probed with HTTP requests.
//
// If this test ever fails, a documented route lost its production mount —
// fix the wiring in newRouter, not the test.
func TestRouteParityDocumentedNodeRoutes(t *testing.T) {
	// Hermetic gateway wiring: DefaultStateFile() derives from $HOME; point it
	// at an empty dir so the run-registry restore is a no-op.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".hermes", "canopy", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}

	deps := &routeDeps{
		jwtSecret: "route-parity-test-secret",
		connMgr:   transport.NewConnectionManager(nil),
		// relayRegistry nil is safe: DiscoveryAPIEnabled() is
		// nil-receiver-safe and returns false (relay/registry.go).
		cfg: &config.Config{},
	}
	router := newRouter(deps)

	type route struct{ method, pattern string }

	got := map[route]bool{}
	err := chi.Walk(router, func(method, pattern string, h http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if method == "" {
			method = http.MethodGet
		}
		got[route{method, normalizeChiPattern(pattern)}] = true
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}

	// Documented node routes (docs/API.md; SPEC-API-03 §6):
	//   PATCH  /api/v1/trees/{tree_id}/nodes/{node_id}        Update Node
	//   DELETE /api/v1/trees/{tree_id}/nodes/{node_id}        Delete Node
	//   POST   /api/v1/trees/{tree_id}/nodes/{node_id}/reply  Reply to Node
	//   POST   /api/v1/nodes/{node_id}/reply                  flat surface
	//   POST   /api/v1/nodes/{node_id}/fork                   flat surface
	want := []route{
		{http.MethodPost, "/api/v1/trees/{}/nodes/{}/reply"},
		{http.MethodPatch, "/api/v1/trees/{}/nodes/{}"},
		{http.MethodDelete, "/api/v1/trees/{}/nodes/{}"},
		{http.MethodPost, "/api/v1/nodes/{}/reply"},
		{http.MethodPost, "/api/v1/nodes/{}/fork"},
	}

	// Control: tree-scoped fork was mounted before GAP-065 and must stay
	// mounted — proves the harness walks the real router rather than
	// failing vacuously.
	controls := []route{
		{http.MethodPost, "/api/v1/trees/{}/nodes/{}/fork"},
	}

	var missing []string
	for _, w := range controls {
		if !got[w] {
			missing = append(missing, w.method+" "+w.pattern+" (control — pre-existing mount vanished)")
		}
	}
	for _, w := range want {
		if !got[w] {
			missing = append(missing, w.method+" "+w.pattern)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Logf("walked route table (%d routes):", len(got))
		patterns := make([]string, 0, len(got))
		for r := range got {
			patterns = append(patterns, r.method+" "+r.pattern)
		}
		sort.Strings(patterns)
		for _, p := range patterns {
			t.Logf("  %s", p)
		}
		t.Fatalf("documented node route(s) NOT mounted on the real router:\n  %s",
			strings.Join(missing, "\n  "))
	}
}
