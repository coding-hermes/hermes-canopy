package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

// TestSSERouteAllowlistMarkedForWriteDeadlineExemption extends
// TestSSERouteAllowlistMatchesRouter for GAP-100: every route on the live
// SSE allowlist — walked from the REAL production router — is actually
// marked by sseWriteDeadlineExemptMiddleware. The marker is the one
// mechanism whose presence lets a stream clear the server-level WriteTimeout
// (internal/sse.FrameWriter); an allowlist entry without the marker would
// still die at the server boundary. The census also fails when an ordinary
// GET route wrongly matches the stream suffixes (it would gain the
// exemption without a conscious decision).
//
// The federation stream cannot mount on the DB-free test router
// (federationSvc == nil), so its marking is pinned at the predicate level
// exactly like its allowlist entry — including the method rule: the GET
// stream is marked, the POST write is not.
func TestSSERouteAllowlistMarkedForWriteDeadlineExemption(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".hermes", "canopy", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}

	deps := &routeDeps{
		jwtSecret: "sse-deadline-test-secret",
		connMgr:   transport.NewConnectionManager(nil),
		cfg:       &config.Config{},
	}
	router := newRouter(deps)

	// Census: the GET routes the suffix predicate recognizes on the real
	// router must equal the known allowlist, both directions.
	found := map[string]bool{}
	err := chi.Walk(router, func(method, pattern string, h http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if method == "" {
			method = http.MethodGet
		}
		if method != http.MethodGet {
			return nil
		}
		normalized := normalizeChiPattern(pattern)
		for _, suffix := range sseStreamSuffixes {
			if strings.HasSuffix(normalized, suffix) {
				found[normalized] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}

	want := map[string]bool{
		"/api/v1/trees/{}/events":            true,
		"/api/v1/plugins/{}/events":          true,
		"/api/v1/cards/{}/events":            true,
		"/api/v1/cards/iteration/{}/events":  true,
		"/api/v1/workspace/channels/{}/feed": true,
		"/api/v1/gateway/runs/{}/events":     true,
		"/api/v1/workspaces/{}/mls/events":   true,
	}
	for pattern := range want {
		if !found[pattern] {
			t.Fatalf("known SSE route %s missing from the real router walk (found: %v)", pattern, found)
		}
	}
	for pattern := range found {
		if !want[pattern] {
			t.Fatalf("unexpected GET route %s matches the stream suffixes; decide whether it is a stream before it silently gains the write-deadline exemption", pattern)
		}
	}

	// Marking probe: replay the router's REAL global middleware chain over a
	// capture endpoint, so the assertion runs the production marking
	// middleware in its production position (not a test double of it).
	probeMarked := func(method, pattern string) bool {
		captured := false
		var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured = sse.WriteDeadlineExempt(r)
			w.WriteHeader(http.StatusOK)
		})
		mws := router.Middlewares()
		for i := len(mws) - 1; i >= 0; i-- {
			h = mws[i](h)
		}
		req := httptest.NewRequest(method, strings.ReplaceAll(pattern, "{}", "probe-"+uuid.NewString()[:8]), nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return captured
	}

	for pattern := range want {
		if !probeMarked(http.MethodGet, pattern) {
			t.Fatalf("SSE route %s is on the allowlist but sseWriteDeadlineExemptMiddleware does not mark it — the server WriteTimeout would still kill the stream (GAP-100)", pattern)
		}
	}

	// Ordinary routes must stay unmarked so the server WriteTimeout keeps
	// bounding them.
	for _, pattern := range []string{
		"/api/v1/trees/{}/nodes",
		"/api/v1/trees/{}/nodes/{}/reference-context",
		"/health",
	} {
		if probeMarked(http.MethodGet, pattern) {
			t.Fatalf("ordinary route %s was marked for the write-deadline exemption — the exemption leaked", pattern)
		}
	}

	// Federation stream: unmounted on this router (no federation service),
	// so pin the marking at the predicate level. Method matters.
	if !probeMarked(http.MethodGet, "/api/v1/federation/events") {
		t.Fatal("GET /api/v1/federation/events is a stream route but was not marked")
	}
	if probeMarked(http.MethodPost, "/api/v1/federation/events") {
		t.Fatal("POST /api/v1/federation/events is an ordinary write but was marked")
	}
}

// TestTreeSSE_HeartbeatOutlivesServerWriteTimeout is the GAP-100
// heartbeat-through-a-real-server regression test: the production tree SSE
// handler (real sse.NewHandler over a real hub, mounted on the REAL router
// at /trees/{tree_id}/events behind the production global middleware chain)
// served by a REAL http.Server with an explicit WriteTimeout, read by a real
// TCP client. Scaled timings (DF-HERMES-CANOPY-31 precedent: 300ms
// WriteTimeout / 80ms heartbeat) compress the 30s/heartbeat production
// constants while preserving their exact relationship — the server
// WriteTimeout still covers the whole response, and the heartbeat cadence is
// a fraction of it. The stream must produce a heartbeat AFTER the server
// WriteTimeout boundary and stay open past it; before GAP-100 this exact
// shape died server-side at the boundary with a clean EOF and zero
// heartbeats (proven live against the pre-fix binary on an isolated boot).
//
// The route is reached through the real router so the assertion covers the
// full composition: routing, auth/membership middleware stack, the global
// sseWriteDeadlineExemptMiddleware marking, and the handler's
// FrameWriter clear/re-arm — not a hand-mounted handler double.
func TestTreeSSE_HeartbeatOutlivesServerWriteTimeout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".hermes", "canopy", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}

	hub := sse.NewHub()
	deps := &routeDeps{
		jwtSecret: "sse-heartbeat-test-secret",
		connMgr:   transport.NewConnectionManager(nil),
		sseHub:    hub,
		cfg:       &config.Config{},
		// DB-free composition (TestMCPAdvertisedPathServesTheHandshakeBehindAuth
		// seam): nil userRepo makes auth a pure JWT check, and the membership
		// stub admits the bearer — enough to reach the real tree-events SSE
		// route through routing + auth + membership + the global marking
		// middleware, none of which this test wants to double out.
		membersRepo: memberAllowAllRepo{},
	}
	router := newRouter(deps)

	const secret = "sse-heartbeat-test-secret"
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": uuid.NewString(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign test token: %v", err)
	}

	server := httptest.NewUnstartedServer(router)
	const serverWriteTimeout = 300 * time.Millisecond
	server.Config.WriteTimeout = serverWriteTimeout
	server.Start()
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	treeID := uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/api/v1/trees/"+treeID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	start := time.Now()
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET tree events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tree events status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	headers := time.Since(start)
	if headers >= serverWriteTimeout {
		t.Fatalf("headers took %v, longer than the server WriteTimeout %v — the response should commit headers immediately", headers, serverWriteTimeout)
	}

	type line struct {
		text string
		at   time.Duration
	}
	lines := make(chan line, 64)
	streamErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			text, err := reader.ReadString('\n')
			if text != "" {
				select {
				case lines <- line{text: text, at: time.Since(start)}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				streamErr <- err
				return
			}
		}
	}()

	// Drive a REAL event through the production path after the server
	// WriteTimeout boundary: hub.Broadcast is what the graph services call,
	// and the client is guaranteed subscribed by the time the 200 returns
	// (the handler subscribes before committing headers — see step 7 of
	// HandleTreeEvents). Pre-GAP-100 the server killed the response at the
	// boundary, so nothing could ever arrive past it; the broadcast's data
	// frame arriving is therefore the survival proof, and a second one
	// proves the connection stayed open.
	deadline := time.Now().Add(5 * time.Second)
	for hub.SubscriberCount(treeID) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(2 * serverWriteTimeout) // land the event AFTER the WriteTimeout boundary
	hub.Broadcast(treeID, sse.SSEEvent{
		TreeID:      treeID,
		Type:        "node_added",
		SequenceNum: 1,
		Data:        []byte(`{"probe":"gap100"}`),
	})

	dataAt := time.Duration(-1)
	for dataAt < 0 {
		select {
		case l := <-lines:
			if strings.HasPrefix(l.text, "data:") {
				dataAt = l.at
			}
		case err := <-streamErr:
			t.Fatalf("stream ended at %v before any data frame past the WriteTimeout boundary (%v): %v",
				time.Since(start), serverWriteTimeout, err)
		case <-ctx.Done():
			t.Fatalf("timeout: no data frame after the server WriteTimeout within 10s")
		}
	}
	if dataAt < serverWriteTimeout {
		t.Fatalf("data frame arrived at %v, before the WriteTimeout boundary %v — the assertion must pin survival PAST the boundary", dataAt, serverWriteTimeout)
	}
	// The connection must still be healthy: a second post-boundary broadcast
	// arrives too (a cut-at-boundary stream would have errored by now).
	hub.Broadcast(treeID, sse.SSEEvent{
		TreeID:      treeID,
		Type:        "node_added",
		SequenceNum: 2,
		Data:        []byte(`{"probe":"gap100-2"}`),
	})
	secondAt := time.Duration(-1)
	for secondAt < 0 {
		select {
		case l := <-lines:
			if strings.Contains(l.text, "gap100-2") {
				secondAt = l.at
			}
		case err := <-streamErr:
			t.Fatalf("stream errored after surviving the boundary: %v", err)
		case <-ctx.Done():
			t.Fatalf("timeout: second post-boundary frame never arrived — stream not alive")
		}
	}
}

// TestTreeSSE_HeartbeatFramesOutlivesServerWriteTimeout pins the heartbeat
// half of the GAP-100 criterion: ">30s SSE connection receives heartbeat
// frames and stays open". Production constants (30s heartbeat / 30s
// WriteTimeout) are compressed to the DF-HERMES-CANOPY-31 test shape the
// fleet accepts for whole-response deadline proofs (80ms heartbeat vs 300ms
// WriteTimeout — same relationship: the heartbeat cadence is a fraction of
// the server boundary), so the stream emits several heartbeats around the
// boundary deterministically. The handler here is the REAL shared SSE
// handler (sse.NewHandlerWithConfig = the production implementation with a
// test cadence), mounted behind the REAL requestTimeoutExemptSSE +
// sseWriteDeadlineExemptMiddleware pair, served by a REAL http.Server and
// read by a real TCP client. Before GAP-100 the server cut the response at
// the WriteTimeout boundary, so no heartbeat could ever arrive after it
// (proven live against the pre-fix binary: 34s connection, zero heartbeats).
// The production-constants version of this proof was run live by the foreman
// on an isolated boot (own DB, dev JWT, 40s connection): exactly one
// ": heartbeat" frame at ~30s and the connection still open at 40s — the
// scenario this scaled test locks into CI.
func TestTreeSSE_HeartbeatFramesOutlivesServerWriteTimeout(t *testing.T) {
	hub := sse.NewHub()
	// The production handler implementation with a test heartbeat cadence.
	sseHandler := sse.NewHandlerWithConfig(hub, nil, 80*time.Millisecond, nil)

	const serverWriteTimeout = 300 * time.Millisecond
	r := chi.NewRouter()
	r.Use(requestTimeoutExemptSSE(60 * time.Second))
	r.Use(sseWriteDeadlineExemptMiddleware())
	r.Get("/api/v1/trees/{tree_id}/events", sseHandler.HandleTreeEvents)

	server := httptest.NewUnstartedServer(r)
	server.Config.WriteTimeout = serverWriteTimeout
	server.Start()
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	treeID := uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/api/v1/trees/"+treeID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	start := time.Now()
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET tree events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tree events status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	lines := make(chan string, 64)
	streamErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			text, err := reader.ReadString('\n')
			if text != "" {
				select {
				case lines <- text:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				streamErr <- err
				return
			}
		}
	}()

	// Count heartbeat comment lines; require one strictly AFTER the server
	// WriteTimeout boundary and the stream still delivering afterwards.
	heartbeats := 0
	heartbeatPastBoundary := false
	for {
		select {
		case text := <-lines:
			if strings.HasPrefix(text, ":") {
				heartbeats++
				if time.Since(start) > serverWriteTimeout {
					heartbeatPastBoundary = true
				}
			}
			if heartbeatPastBoundary && heartbeats >= 3 {
				return
			}
		case err := <-streamErr:
			t.Fatalf("stream ended at %v with %d heartbeats (past-boundary seen: %v; boundary %v): %v",
				time.Since(start), heartbeats, heartbeatPastBoundary, serverWriteTimeout, err)
		case <-ctx.Done():
			t.Fatalf("timeout: %d heartbeats total, none past the WriteTimeout boundary %v — the stream did not outlive the server WriteTimeout", heartbeats, serverWriteTimeout)
		}
	}
}

// memberAllowAllRepo is the DB-free membership double for the real-router
// SSE tests: TreeMemberChecker only needs IsMember + IsTreeDeleted.
type memberAllowAllRepo struct{ db.TreeMemberRepo }

func (memberAllowAllRepo) IsMember(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

func (memberAllowAllRepo) IsTreeDeleted(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}
