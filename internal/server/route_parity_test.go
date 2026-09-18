package server

import (
	"fmt"
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

// ---------------------------------------------------------------------------
// GAP-086: § Plugins documentation parity
// ---------------------------------------------------------------------------

// pluginSectionRouteLine matches a standalone route line inside a § Plugins
// fenced block ("POST /api/v1/plugins/{name}/activate"). The method+path must
// be the WHOLE line, so a mid-prose mention — backticked or not — is never
// mistaken for a documented route.
var pluginSectionRouteLine = regexp.MustCompile(`^(GET|POST|PATCH|PUT|DELETE)[ 	]+(/\S+)$`)

// stalePluginPaths are the five routes earlier revisions of docs/API.md
// documented in § Plugins which nothing mounts today (GAP-086). They belonged
// to the first plugin handler (GAP-002) and were replaced by the PL-01
// registry/lifecycle surface, so a plugin author following the old text could
// not register a plugin: the documented POSTs matched no route, and
// /api/v1/plugins/instances was captured by the mounted GET /api/v1/plugins/{id}
// as a plugin lookup.
var stalePluginPaths = []string{
	"/api/v1/plugins/register",
	"/api/v1/plugins/{plugin_id}/install",
	"/api/v1/plugins/instances",
	"/api/v1/plugins/instances/{instance_id}/pause",
	"/api/v1/plugins/instances/{instance_id}/resume",
}

// stalePluginMarkers are non-path promises the same earlier revision made and
// the mounted source route does not satisfy.
var stalePluginMarkers = []string{"X-Source-SHA256"}

// markdownSection returns the body of the level-2 section titled title,
// excluding the heading and stopping at the next level-2 heading. docs/API.md
// is the canonical API reference, so the parity checks read it as the record
// of what an operator was told is mounted.
func markdownSection(doc, title string) (string, error) {
	heading := "## " + title
	lines := strings.Split(doc, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, " 	") == heading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("docs/API.md has no %q section", heading)
	}
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			return strings.Join(lines[start:i], "\n"), nil
		}
	}
	return strings.Join(lines[start:], "\n"), nil
}

// numberedDriftItem returns the text of drift entry n (its "n. **…**" line
// through the line before entry n+1) — the place docs/API.md records the
// direction of a known discrepancy.
func numberedDriftItem(section string, n int) (string, error) {
	lines := strings.Split(section, "\n")
	prefix := fmt.Sprintf("%d. ", n)
	next := fmt.Sprintf("%d. ", n+1)
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("docs/API.md has no drift entry %d", n)
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], next) {
			return strings.Join(lines[start:i], "\n"), nil
		}
	}
	return strings.Join(lines[start:], "\n"), nil
}

// documentedPluginRoutes extracts the routes a section documents, normalized
// with the same canonicalization the mounted route table uses, so parameter
// renames ({id} vs {plugin_id}) cannot hide a route that is documented but
// absent. Every route line in the section must be a /api/v1/plugins path: a
// foreign route documented under § Plugins is itself drift.
func documentedPluginRoutes(section string) ([]string, error) {
	var out []string
	for i, line := range strings.Split(section, "\n") {
		m := pluginSectionRouteLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if !strings.HasPrefix(m[2], "/api/v1/plugins") {
			return nil, fmt.Errorf("line %d documents %s %s — that is not a /api/v1/plugins route",
				i+1, m[1], m[2])
		}
		out = append(out, m[1]+" "+normalizeChiPattern(m[2]))
	}
	return out, nil
}

// mountedPluginRoutes walks the REAL production router (newRouter, the same
// seam New uses) and returns its /api/v1/plugins routes in the same normalized
// form, plus the size of the whole walked table so a run that enumerated
// nothing cannot be mistaken for parity. Hermetic and DB-free, exactly like
// TestRouteParityDocumentedNodeRoutes: $HOME is redirected so the gateway
// run-registry restore is a no-op, and nil services are safe because handlers
// only dereference them inside request handlers.
func mountedPluginRoutes(t *testing.T) (map[string]bool, int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".hermes", "canopy", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}
	deps := &routeDeps{
		jwtSecret: "route-parity-test-secret",
		connMgr:   transport.NewConnectionManager(nil),
		cfg:       &config.Config{},
	}
	router := newRouter(deps)

	got := map[string]bool{}
	total := 0
	err := chi.Walk(router, func(method, pattern string, h http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if method == "" {
			method = http.MethodGet
		}
		total++
		norm := normalizeChiPattern(pattern)
		if !strings.HasPrefix(norm, "/api/v1/plugins") {
			return nil
		}
		got[method+" "+norm] = true
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}
	return got, total
}

// stalePluginClaims reports every stale path or marker a section still
// asserts. It is the absence half of the guard, so it must be able to fire:
// TestRouteParityDocumentedPluginRoutes feeds it a control section that does
// contain a stale path.
func stalePluginClaims(section string) []string {
	stale := append(append([]string{}, stalePluginPaths...), stalePluginMarkers...)
	var hits []string
	for _, s := range stale {
		if strings.Contains(section, s) {
			hits = append(hits, s)
		}
	}
	return hits
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestRouteParityDocumentedPluginRoutes pins § Plugins to the mounted plugin
// surface in BOTH directions (GAP-086): every route the section documents is
// mounted on the real router, every mounted plugin route is documented, and
// the five stale paths the old revision documented (plus the digest response
// header it promised) stay absent. Drift entry 7 is pinned too, because it is
// where the document states which direction the drift ran — the old revision
// claimed those routes "are registered but not fully documented", which was
// the opposite of the truth.
//
// Deterministic and dependency-free: no DB, no network, and no HTTP probe —
// status-code probes are a false oracle here, because the auth middleware runs
// before chi's routing table and answers 401 for unknown paths too. If this
// test fails, the docs and the mount disagree: fix whichever is wrong, do not
// delete the check.
func TestRouteParityDocumentedPluginRoutes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "API.md"))
	if err != nil {
		t.Fatalf("read docs/API.md: %v", err)
	}
	docs := string(raw)

	section, err := markdownSection(docs, "Plugins")
	if err != nil {
		t.Fatal(err)
	}
	documented, err := documentedPluginRoutes(section)
	if err != nil {
		t.Fatalf("docs/API.md § Plugins: %v", err)
	}
	if len(documented) == 0 {
		t.Fatal("§ Plugins documents no route at all — the heading or the extractor moved, not the API")
	}

	mounted, walkedTotal := mountedPluginRoutes(t)
	if walkedTotal < 40 {
		t.Fatalf("chi.Walk enumerated only %d routes — it did not walk the real router", walkedTotal)
	}
	if len(mounted) == 0 {
		t.Fatal("no /api/v1/plugins route is mounted — the plugin mount disappeared from newRouter")
	}

	// Extractor control: a mid-prose mention is NOT a documented route, and
	// the {param} canonicalization must collapse.
	control := "### Register Plugin\n\n```\nPOST /api/v1/plugins/\n```\n\n" +
		"Prose that mentions POST /api/v1/plugins/register mid-sentence.\n\n" +
		"### Get Plugin\n\n```\nGET /api/v1/plugins/{id}\n```\n"
	controlGot, err := documentedPluginRoutes(control)
	if err != nil {
		t.Fatalf("extractor control: %v", err)
	}
	if want := []string{"POST /api/v1/plugins", "GET /api/v1/plugins/{}"}; !equalStringSlices(controlGot, want) {
		t.Fatalf("extractor control = %v, want %v", controlGot, want)
	}
	if _, err := documentedPluginRoutes("```\nGET /api/v1/trees/{tree_id}\n```\n"); err == nil {
		t.Fatal("extractor accepted a foreign route line — § Plugins could document a non-plugin route silently")
	}

	// Absence control: the stale check below must be able to fail.
	if len(stalePluginClaims("GET /api/v1/plugins/instances/")) == 0 {
		t.Fatal("stale-plugin detector found nothing in a section that asserts a stale path — the absence check is vacuous")
	}

	var problems []string
	documentedSet := map[string]bool{}
	for _, r := range documented {
		documentedSet[r] = true
		if !mounted[r] {
			problems = append(problems, r+" (documented in § Plugins, NOT mounted)")
		}
	}
	for r := range mounted {
		if !documentedSet[r] {
			problems = append(problems, r+" (mounted, NOT documented in § Plugins)")
		}
	}
	if hits := stalePluginClaims(section); len(hits) > 0 {
		problems = append(problems, "§ Plugins re-asserts stale claim(s): "+strings.Join(hits, ", "))
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Logf("§ Plugins documents %d routes; the router mounts %d plugin routes", len(documented), len(mounted))
		t.Fatalf("plugin documentation parity broken:\n  %s", strings.Join(problems, "\n  "))
	}

	// Drift entry 7 states the direction of the drift, so pin it: the
	// disproved claim must stay gone and the correction stay in place.
	driftSection, err := markdownSection(docs, "Spec-vs-Code Drift")
	if err != nil {
		t.Fatal(err)
	}
	item7, err := numberedDriftItem(driftSection, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(item7, "Plugin endpoints") {
		t.Errorf("drift entry 7 is no longer the plugin entry — re-point this assertion:\n%s", item7)
	}
	if !strings.Contains(strings.ToUpper(item7), "STALE") {
		t.Errorf("drift entry 7 no longer states that the old register/install + instances surface is stale:\n%s", item7)
	}
	if strings.Contains(item7, "are registered but not fully documented in the README") {
		t.Errorf("drift entry 7 re-asserts the disproved claim that the old plugin routes are registered:\n%s", item7)
	}
}
