// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file contains transport-layer integration tests covering SSE hub
// lifecycle, connection lifecycle, and rate limiting. Task BE-12e.
package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

// ---------------------------------------------------------------------------
// In-memory test SSEClient (package-private, matches sse_test.go pattern)
// ---------------------------------------------------------------------------

type transportTestClient struct {
	id      string
	uid     uuid.UUID
	tid     uuid.UUID
	mu      sync.Mutex
	events  []sse.SSEEvent
	raws    []string
	closed  bool
	doneCh  chan struct{}
	sendErr error
}

func newTransportTestClient(id string, uid, tid uuid.UUID) *transportTestClient {
	return &transportTestClient{id: id, uid: uid, tid: tid, doneCh: make(chan struct{})}
}

func (c *transportTestClient) ID() string            { return c.id }
func (c *transportTestClient) UserID() uuid.UUID     { return c.uid }
func (c *transportTestClient) TreeID() uuid.UUID     { return c.tid }
func (c *transportTestClient) Done() <-chan struct{} { return c.doneCh }
func (c *transportTestClient) LastEventID() string   { return "" }

func (c *transportTestClient) Send(ev sse.SSEEvent) error {
	if c.closed {
		return sse.ErrClientClosed
	}
	if c.sendErr != nil {
		return c.sendErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

func (c *transportTestClient) SendRaw(raw string) error {
	if c.closed {
		return sse.ErrClientClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raws = append(c.raws, raw)
	return nil
}

func (c *transportTestClient) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	close(c.doneCh)
	return nil
}

func (c *transportTestClient) eventCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// ---------------------------------------------------------------------------
// SSE Hub integration tests
// ---------------------------------------------------------------------------

func TestBE12e_SSEHubLifecycle(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_ = pool // no DB needed for in-memory SSE hub tests

	t.Run("subscribe_broadcast_receive", func(t *testing.T) {
		hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
		defer func() { _ = hub.Shutdown(context.Background()) }()

		treeID := uuid.New()
		userID := uuid.New()

		// Subscribe two test clients to the same tree.
		c1 := newTransportTestClient("client-1", userID, treeID)
		if err := hub.Subscribe(context.Background(), treeID, c1); err != nil {
			t.Fatalf("subscribe c1: %v", err)
		}

		c2 := newTransportTestClient("client-2", userID, treeID)
		if err := hub.Subscribe(context.Background(), treeID, c2); err != nil {
			t.Fatalf("subscribe c2: %v", err)
		}

		if got := hub.TotalConnections(); got != 2 {
			t.Fatalf("total connections = %d, want 2", got)
		}
		if got := hub.SubscriberCount(treeID); got != 2 {
			t.Fatalf("subscriber count = %d, want 2", got)
		}

		// Broadcast 5 events.
		for i := 0; i < 5; i++ {
			hub.Broadcast(treeID, sse.ComposeEvent(treeID, uuid.Nil, "node_added",
				map[string]any{"seq": i}))
		}

		// Both clients should receive all 5 events.
		if n := c1.eventCount(); n != 5 {
			t.Fatalf("c1 received %d events, want 5", n)
		}
		if n := c2.eventCount(); n != 5 {
			t.Fatalf("c2 received %d events, want 5", n)
		}
	})

	t.Run("shutdown_disconnects_clients", func(t *testing.T) {
		hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
		defer func() { _ = hub.Shutdown(context.Background()) }()

		treeID := uuid.New()
		c1 := newTransportTestClient("sd-1", uuid.New(), treeID)
		if err := hub.Subscribe(context.Background(), treeID, c1); err != nil {
			t.Fatalf("subscribe: %v", err)
		}

		if hub.TotalConnections() != 1 {
			t.Fatalf("expected 1 connection before shutdown, got %d", hub.TotalConnections())
		}

		// Shutdown should drain and disconnect all clients.
		_ = hub.Shutdown(context.Background())

		if hub.TotalConnections() != 0 {
			t.Fatalf("expected 0 connections after shutdown, got %d", hub.TotalConnections())
		}

		// Client should have received a done event and be closed.
		select {
		case <-c1.Done():
			// OK: client closed
		case <-time.After(2 * time.Second):
			t.Fatal("client did not close after shutdown")
		}
	})

	t.Run("unsubscribe_removes_client", func(t *testing.T) {
		hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
		defer func() { _ = hub.Shutdown(context.Background()) }()

		treeID := uuid.New()
		c1 := newTransportTestClient("unsub-1", uuid.New(), treeID)
		if err := hub.Subscribe(context.Background(), treeID, c1); err != nil {
			t.Fatalf("subscribe: %v", err)
		}

		if hub.SubscriberCount(treeID) != 1 {
			t.Fatalf("expected 1 subscriber, got %d", hub.SubscriberCount(treeID))
		}

		hub.Unsubscribe(treeID, c1.ID())

		if hub.SubscriberCount(treeID) != 0 {
			t.Fatalf("expected 0 subscribers after unsubscribe, got %d", hub.SubscriberCount(treeID))
		}
		if hub.TotalConnections() != 0 {
			t.Fatalf("expected 0 total connections, got %d", hub.TotalConnections())
		}
	})

	t.Run("broadcast_no_subscribers", func(t *testing.T) {
		hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
		defer func() { _ = hub.Shutdown(context.Background()) }()

		// Broadcast to a tree with no subscribers should not panic.
		hub.Broadcast(uuid.New(), sse.ComposeEvent(uuid.New(), uuid.Nil, "node_added",
			map[string]any{"hello": "world"}))

		if hub.TotalConnections() != 0 {
			t.Fatalf("expected 0 connections, got %d", hub.TotalConnections())
		}
	})

	t.Run("slow_client_dropped", func(t *testing.T) {
		hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
		defer func() { _ = hub.Shutdown(context.Background()) }()

		treeID := uuid.New()

		slow := newTransportTestClient("slow-client", uuid.Nil, treeID)
		slow.sendErr = sse.ErrClientSlow
		if err := hub.Subscribe(context.Background(), treeID, slow); err != nil {
			t.Fatalf("subscribe slow: %v", err)
		}

		fast := newTransportTestClient("fast-client", uuid.New(), treeID)
		if err := hub.Subscribe(context.Background(), treeID, fast); err != nil {
			t.Fatalf("subscribe fast: %v", err)
		}

		// Broadcast — slow client should be dropped, fast should remain.
		hub.Broadcast(treeID, sse.ComposeEvent(treeID, uuid.Nil, "node_added",
			map[string]any{"test": true}))

		if hub.SubscriberCount(treeID) != 1 {
			t.Fatalf("subscriber count should be 1 (slow dropped), got %d", hub.SubscriberCount(treeID))
		}
		if fast.eventCount() != 1 {
			t.Fatalf("fast client should have 1 event, got %d", fast.eventCount())
		}
	})
}

// ---------------------------------------------------------------------------
// Connection lifecycle integration tests
// ---------------------------------------------------------------------------

func TestBE12e_ConnectionLifecycle(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_ = pool

	t.Run("connect_creates_active_connection", func(t *testing.T) {
		hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
		defer func() { _ = hub.Shutdown(context.Background()) }()

		adapter := transport.NewSSEAdapter(hub)

		treeID := uuid.New()
		opts := transport.ConnectOptions{
			Target:        treeID.String(),
			TransportType: transport.TransportSSE,
			Metadata: map[string]string{
				"client_version": "1.0.0",
				"session_id":     "test-session",
			},
			Timeout: 5 * time.Second,
		}

		conn, err := adapter.Connect(context.Background(), opts)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		if conn == nil {
			t.Fatal("Connect returned nil connection")
		}
		if conn.State != transport.StateActive {
			t.Fatalf("conn.State = %v, want active", conn.State)
		}
		if conn.TransportType != transport.TransportSSE {
			t.Fatalf("conn.TransportType = %v, want sse", conn.TransportType)
		}
		if conn.Peer != treeID.String() {
			t.Fatalf("conn.Peer = %q, want %q", conn.Peer, treeID.String())
		}
		if conn.Metadata["client_version"] != "1.0.0" {
			t.Fatalf("metadata[client_version] = %q, want 1.0.0", conn.Metadata["client_version"])
		}
		if conn.ID == "" {
			t.Fatal("conn.ID is empty")
		}
		if conn.EstablishedAt.IsZero() {
			t.Fatal("conn.EstablishedAt is zero")
		}
	})

	t.Run("connect_with_wrong_transport_type_errors", func(t *testing.T) {
		hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
		defer func() { _ = hub.Shutdown(context.Background()) }()

		adapter := transport.NewSSEAdapter(hub)

		opts := transport.ConnectOptions{
			TransportType: transport.TransportWebRTC,
		}
		conn, err := adapter.Connect(context.Background(), opts)
		if err != transport.ErrTransportMismatch {
			t.Fatalf("expected ErrTransportMismatch, got %v", err)
		}
		if conn != nil {
			t.Fatal("expected nil connection on mismatch")
		}
	})

	t.Run("graceful_disconnect", func(t *testing.T) {
		hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
		defer func() { _ = hub.Shutdown(context.Background()) }()

		adapter := transport.NewSSEAdapter(hub)
		treeID := uuid.New()

		conn, err := adapter.Connect(context.Background(), transport.ConnectOptions{
			Target: treeID.String(),
		})
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}

		// Disconnect should succeed and transition to closed.
		if err := adapter.Disconnect(context.Background(), conn); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}
		if conn.State != transport.StateClosed {
			t.Fatalf("conn.State = %v, want closed", conn.State)
		}

		// Idempotent: second disconnect should not error.
		if err := adapter.Disconnect(context.Background(), conn); err != nil {
			t.Fatalf("Disconnect (idempotent): %v", err)
		}
	})

	t.Run("disconnect_nil_connection", func(t *testing.T) {
		hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
		defer func() { _ = hub.Shutdown(context.Background()) }()

		adapter := transport.NewSSEAdapter(hub)
		if err := adapter.Disconnect(context.Background(), nil); err != nil {
			t.Fatalf("Disconnect(nil): %v", err)
		}
	})

	t.Run("send_to_closed_connection_errors", func(t *testing.T) {
		hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
		defer func() { _ = hub.Shutdown(context.Background()) }()

		adapter := transport.NewSSEAdapter(hub)
		treeID := uuid.New()

		conn, err := adapter.Connect(context.Background(), transport.ConnectOptions{
			Target: treeID.String(),
		})
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}

		// Close the connection.
		if err := adapter.Disconnect(context.Background(), conn); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}

		// Send should fail with ErrConnectionClosed.
		err = adapter.Send(context.Background(), conn, &transport.Message{
			Opcode: transport.OpNodeAdd,
			TreeID: treeID.String(),
		})
		if err != transport.ErrConnectionClosed {
			t.Fatalf("Send on closed conn: got %v, want ErrConnectionClosed", err)
		}
	})

	t.Run("connection_manager_tracks_connections", func(t *testing.T) {
		selector := transport.NewTransportSelector(transport.ModeLocal, transport.TopologyLoopback)
		cm := transport.NewConnectionManager(selector)

		conn1 := &transport.Connection{
			ID:            "conn-1",
			TransportType: transport.TransportSSE,
			Peer:          "peer-a",
			State:         transport.StateConnecting,
		}
		conn2 := &transport.Connection{
			ID:            "conn-2",
			TransportType: transport.TransportSSE,
			Peer:          "peer-b",
			State:         transport.StateConnecting,
		}

		// Register connections.
		if err := cm.OnConnect(conn1); err != nil {
			t.Fatalf("OnConnect conn1: %v", err)
		}
		if err := cm.OnConnect(conn2); err != nil {
			t.Fatalf("OnConnect conn2: %v", err)
		}

		// Verify tracking.
		if count := cm.ConnectionCount(transport.TransportSSE); count != 2 {
			t.Fatalf("ConnectionCount = %d, want 2", count)
		}
		if count := cm.ConnectionCount(""); count != 2 {
			t.Fatalf("ConnectionCount(all) = %d, want 2", count)
		}

		// GetConnection should find active peers.
		got1, err := cm.GetConnection("peer-a")
		if err != nil {
			t.Fatalf("GetConnection peer-a: %v", err)
		}
		if got1 == nil || got1.ID != "conn-1" {
			t.Fatalf("GetConnection peer-a: got %+v", got1)
		}

		got2, err := cm.GetConnection("peer-b")
		if err != nil {
			t.Fatalf("GetConnection peer-b: %v", err)
		}
		if got2 == nil || got2.ID != "conn-2" {
			t.Fatalf("GetConnection peer-b: got %+v", got2)
		}

		// AllConnections should return both.
		all := cm.AllConnections()
		if len(all) != 2 {
			t.Fatalf("AllConnections length = %d, want 2", len(all))
		}
	})

	t.Run("connection_manager_on_disconnect", func(t *testing.T) {
		selector := transport.NewTransportSelector(transport.ModeLocal, transport.TopologyLoopback)
		cm := transport.NewConnectionManager(selector)

		conn := &transport.Connection{
			ID:            "conn-disco",
			TransportType: transport.TransportSSE,
			Peer:          "peer-disco",
			State:         transport.StateConnecting,
		}
		if err := cm.OnConnect(conn); err != nil {
			t.Fatalf("OnConnect: %v", err)
		}

		if err := cm.OnDisconnect(conn); err != nil {
			t.Fatalf("OnDisconnect: %v", err)
		}

		if cm.ConnectionCount("") != 0 {
			t.Fatalf("ConnectionCount after disconnect = %d, want 0", cm.ConnectionCount(""))
		}

		// GetConnection should return nil, nil for disconnected peer.
		got, err := cm.GetConnection("peer-disco")
		if err != nil {
			t.Fatalf("GetConnection: %v", err)
		}
		if got != nil {
			t.Fatalf("GetConnection after disconnect should return nil, got %+v", got)
		}
	})

	t.Run("connection_manager_nil_connection", func(t *testing.T) {
		selector := transport.NewTransportSelector(transport.ModeLocal, transport.TopologyLoopback)
		cm := transport.NewConnectionManager(selector)

		if err := cm.OnConnect(nil); err != transport.ErrConnectionClosed {
			t.Fatalf("OnConnect(nil): got %v, want ErrConnectionClosed", err)
		}
		if err := cm.OnDisconnect(nil); err != nil {
			t.Fatalf("OnDisconnect(nil): %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Rate limiting integration tests
// ---------------------------------------------------------------------------

func TestBE12e_RateLimiting(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_ = pool

	t.Run("token_bucket_initial_burst", func(t *testing.T) {
		// Rate of 10/sec, burst of 5.
		limiter := transport.NewRateLimiter(10, 5)

		// First 5 requests should all be allowed (full bucket).
		for i := 0; i < 5; i++ {
			if !limiter.Allow() {
				t.Fatalf("allow %d: expected true, got false (bucket should be full)", i)
			}
		}

		// 6th should be rejected (bucket empty).
		if limiter.Allow() {
			t.Fatal("allow after burst: expected false, got true")
		}
	})

	t.Run("token_refill", func(t *testing.T) {
		// Rate of 100/sec (fast refill), burst of 5.
		limiter := transport.NewRateLimiter(100, 5)

		// Drain the bucket.
		for i := 0; i < 5; i++ {
			limiter.Allow()
		}

		// Wait ~50ms — at 100/sec, should refill ~5 tokens.
		time.Sleep(50 * time.Millisecond)

		// Should have at least some tokens.
		if !limiter.Allow() {
			t.Fatal("allow after refill: expected true, got false")
		}
	})

	t.Run("zero_rate_blocks_all_after_burst", func(t *testing.T) {
		limiter := transport.NewRateLimiter(0, 2)

		// Burst of 2.
		if !limiter.AllowN(2) {
			t.Fatal("initial burst should be allowed")
		}

		// No refill at zero rate.
		if limiter.Allow() {
			t.Fatal("allow after zero-rate burst: expected false, got true")
		}
	})

	t.Run("zero_n_always_allowed", func(t *testing.T) {
		limiter := transport.NewRateLimiter(1, 1)

		// Drain the bucket.
		limiter.Allow()

		// AllowN(0) should always succeed.
		if !limiter.AllowN(0) {
			t.Fatal("AllowN(0) should always return true")
		}
	})

	t.Run("connection_manager_enforce_rate_limit", func(t *testing.T) {
		selector := transport.NewTransportSelector(transport.ModeLocal, transport.TopologyLoopback)
		cm := transport.NewConnectionManager(selector)

		conn := &transport.Connection{
			ID:            "rate-test-conn",
			TransportType: transport.TransportSSE,
			Peer:          "rate-peer",
			State:         transport.StateConnecting,
		}
		if err := cm.OnConnect(conn); err != nil {
			t.Fatalf("OnConnect: %v", err)
		}

		// The default rate limiter has rate=500, burst=1000.
		// First 1000 requests should pass.
		for i := 0; i < 1000; i++ {
			if err := cm.EnforceRateLimit("rate-peer"); err != nil {
				t.Fatalf("EnforceRateLimit #%d: expected nil, got %v", i, err)
			}
		}

		// 1001st should be rate-limited.
		if err := cm.EnforceRateLimit("rate-peer"); err != transport.ErrRateLimited {
			t.Fatalf("EnforceRateLimit after burst: expected ErrRateLimited, got %v", err)
		}
	})

	t.Run("rate_limiter_tokens_diagnostic", func(t *testing.T) {
		limiter := transport.NewRateLimiter(10, 3)

		if tok := limiter.Tokens(); tok != 3 {
			t.Fatalf("initial tokens = %v, want 3", tok)
		}

		limiter.Allow()

		if tok := limiter.Tokens(); tok != 2 {
			t.Fatalf("tokens after 1 allow = %v, want 2", tok)
		}
	})
}
