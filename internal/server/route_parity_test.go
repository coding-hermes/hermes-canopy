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
	mwCount := map[route]int{}
	err := chi.Walk(router, func(method, pattern string, h http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if method == "" {
			method = http.MethodGet
		}
		key := route{method, normalizeChiPattern(pattern)}
		got[key] = true
		mwCount[key] = len(middlewares)
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
		// SPEC-PL-06 multi-message reference model (§9.1, §9.2, §9.3):
		//   POST /api/v1/trees/{tree_id}/reference-selections    preflight
		//   POST /api/v1/trees/{tree_id}/multi-reference-replies create
		//   GET  /api/v1/nodes/{node_id}/reference-context       read (§9.3)
		{http.MethodPost, "/api/v1/trees/{}/reference-selections"},
		{http.MethodPost, "/api/v1/trees/{}/multi-reference-replies"},
		{http.MethodGet, "/api/v1/nodes/{}/reference-context"},
		// GAP-080 phase 2b: the gateway's model catalog, documented in
		// docs/API.md § Live Hermes gateway and consumed by the context
		// manifest panel's model choice.
		{http.MethodGet, "/api/v1/gateway/models"},
		// GAP-078 / SPEC-API-04 §3: the merge endpoint that creates the
		// synthesis node the node-create path refuses.
		{http.MethodPost, "/api/v1/trees/{}/merge"},
	}

	// Control: tree-scoped fork was mounted before GAP-065 and must stay
	// mounted — proves the harness walks the real router rather than
	// failing vacuously.
	controls := []route{
		{http.MethodPost, "/api/v1/trees/{}/nodes/{}/fork"},
	}

	// The route whose middleware class the sibling comparison below pins
	// (SPEC-API-04 §3).
	mergeRoute := route{http.MethodPost, "/api/v1/trees/{}/merge"}

	// Floor check: chi reports the FULL inherited chain, so a bare count is
	// not proof of the inline gate by itself (a gate-less route still shows
	// the /api/v1 group's middleware) — but zero would mean the route lives
	// outside the authenticated group entirely.
	if mwCount[mergeRoute] == 0 {
		t.Errorf("merge route carries NO middleware on the real router — it is outside the authenticated /api/v1 group")
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

	// GAP-078 / SPEC-API-04 §3: the merge route must sit in the SAME
	// middleware class as its sibling tree-scoped write routes — membership
	// gated inside the authenticated /api/v1 group, which is what produces
	// NOT_TREE_MEMBER (403) and TREE_DELETED (410) for free. chi.Walk reports
	// each route's inline middleware chain, so comparing the chain LENGTH
	// with the sibling mounts catches a merge route that lost its gate.
	for _, sibling := range []route{
		{http.MethodPost, "/api/v1/trees/{}/reference-selections"},
		{http.MethodPost, "/api/v1/trees/{}/multi-reference-replies"},
	} {
		if !got[sibling] {
			t.Fatalf("sibling control route %s %s is no longer mounted — cannot compare middleware classes",
				sibling.method, sibling.pattern)
		}
		if mwCount[mergeRoute] != mwCount[sibling] {
			t.Errorf("merge route middleware class differs from %s %s: %d vs %d inline middleware",
				sibling.method, sibling.pattern, mwCount[mergeRoute], mwCount[sibling])
		}
	}
}
