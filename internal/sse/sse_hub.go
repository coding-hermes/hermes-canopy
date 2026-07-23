// Package sse implements the Server-Sent Events hub for canopyd.
//
// The SSE hub is the primary real-time channel for tree data flowing from
// server to client. Mutations originate via HTTP POST handlers and are
// broadcast to subscribed clients through GET /trees/{tree_id}/events.
//
// This package is intentionally in-memory only — events are kept in a
// ring buffer with 1-hour retention so that reconnecting clients can be
// replayed (Last-Event-ID) without depending on PostgreSQL.
//
// References:
//   SPEC-API-01 §9 — Go interfaces
//   SPEC-API-01 §11 — connection limits (10/user, 100/tree, 10000/server)
//   SPEC-API-01 §12 — middleware chain (auth/membership handled upstream)
//   SPEC-API-01 §16 — test scenarios
package sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// --- Connection limits (SPEC-API-01 §11) ------------------------------------

const (
	// MaxConnectionsPerUser is the limit on simultaneous SSE connections per
	// authenticated user. Enforced at Subscribe time.
	MaxConnectionsPerUser = 10

	// MaxConnectionsPerTree is the limit on simultaneous SSE connections per
	// tree. Enforced at Subscribe time.
	MaxConnectionsPerTree = 100

	// MaxConnectionsTotal is the server-wide cap on active SSE connections.
	// Enforced at Subscribe time.
	MaxConnectionsTotal = 10000

	// EventBufferSize is the per-client buffered channel capacity. When full,
	// the hub disconnects the client (slow-consumer protection).
	EventBufferSize = 1024

	// HeartbeatInterval is the default cadence for heartbeat comments on
	// long-lived SSE streams.
	HeartbeatInterval = 30 * time.Second

	// DefaultRetryMS is the SSE retry hint value (milliseconds).
	DefaultRetryMS = 3000

	// DefaultReplayWindow is how far back the event log maintains events.
	DefaultReplayWindow = time.Hour

	// DefaultLogSize is the ring-buffer capacity per tree.
	DefaultLogSize = 1000

	// ShutdownDrain is how long Shutdown waits for clients to consume the
	// "done" event before forcibly closing connections.
	ShutdownDrain = 5 * time.Second
)

// Errors returned by the hub. Wrap with %w for traceability.
var (
	ErrTooManyConnectionsUser = errors.New("too many SSE connections for user")
	ErrTooManyConnectionsTree = errors.New("too many SSE connections for tree")
	ErrTooManyConnections     = errors.New("too many SSE connections (server)")
	ErrClientNotFound         = errors.New("client not found")
)

// --- SSEEvent (SPEC-API-01 §9.1) -------------------------------------------

// SSEEvent is the wire representation of a single event handed to subscribers
// and stored in the event log for replay.
type SSEEvent struct {
	ID          string          // "tree_id:sequence_number" — opaque to clients
	Type        string          // Event type, e.g. "node_added"
	Data        json.RawMessage // Compact JSON payload (no embedded newlines)
	Timestamp   time.Time
	TreeID      uuid.UUID
	SequenceNum int64
	ActorID     uuid.UUID
}

// EventID formats the SSE event ID used in the wire `id:` field.
func EventID(treeID uuid.UUID, seq int64) string {
	return fmt.Sprintf("%s:%d", treeID.String(), seq)
}

// --- SSEClient interface (SPEC-API-01 §9.1) --------------------------------

// SSEClient represents a single SSE connection.
//
// Implementations must be safe for concurrent use because the hub broadcasts
// from a goroutine distinct from the connection's own event loop.
type SSEClient interface {
	ID() string
	Send(event SSEEvent) error
	SendRaw(raw string) error
	LastEventID() string
	Close() error
	Done() <-chan struct{}
	TreeID() uuid.UUID
	UserID() uuid.UUID
}

// --- SSEEventLog interface (SPEC-API-01 §9.2) ------------------------------

// SSEEventLog maintains a bounded ring of recent events for replay.
//
// Implementations are responsible for monotonic sequence numbers per tree.
// Append returns the event with its assigned SequenceNum and ID populated.
type SSEEventLog interface {
	Append(treeID uuid.UUID, eventType string, data json.RawMessage, actorID uuid.UUID) SSEEvent
	Since(treeID uuid.UUID, sinceSeqNum int64, maxEvents int) (events []SSEEvent, truncated bool, err error)
	SinceTime(treeID uuid.UUID, since time.Time, maxEvents int) (events []SSEEvent, truncated bool, err error)
	Prune(retention time.Duration) int
}

// SSEHub interface (SPEC-API-01 §9.1) -----------------------------------

// SSEHub manages SSE connections per tree. All methods are safe for concurrent
// use.
//
// SPEC-API-01 §9.1 — exact method signatures.
type SSEHub interface {
	Subscribe(ctx context.Context, treeID uuid.UUID, client SSEClient) error
	Unsubscribe(treeID uuid.UUID, clientID string)
	Broadcast(treeID uuid.UUID, event SSEEvent) SSEEvent
	ReplaySince(ctx context.Context, treeID uuid.UUID, clientID string, sinceEventID string) error
	SubscriberCount(treeID uuid.UUID) int
	TotalConnections() int
	Shutdown(ctx context.Context) error
}

// --- Implementation --------------------------------------------------------

// hub is the concrete SSEHub. It tracks subscribers per tree, routes
// broadcasts to live clients, and pulls replays from SSEEventLog.
type hub struct {
	log SSEEventLog

	mu          sync.RWMutex
	subscribers map[uuid.UUID]map[string]SSEClient // treeID → clientID → client
	clients     map[string]SSEClient               // clientID → client (global lookup)
	byUser      map[uuid.UUID]int                  // userID → active connection count

	totalConnections atomic.Int64 // duplicates len(clients) for lock-free reads

	closed  atomic.Bool
	closeCh chan struct{}

	drainTimeout time.Duration // configurable for tests
}

// NewHub returns a working SSEHub backed by an in-memory ring buffer of
// defaultLogSize events per tree with DefaultReplayWindow retention.
//
// The returned hub also runs a background prune goroutine every 60s to
// enforce DefaultReplayWindow on the event log.
func NewHub() SSEHub {
	return NewHubWithConfig(HubConfig{})
}

// HubConfig tunes hub behavior. Zero values select sane defaults.
type HubConfig struct {
	// LogSize sets the per-tree ring-buffer capacity. Default: DefaultLogSize.
	LogSize int
	// Retention sets how long events are kept. Default: DefaultReplayWindow.
	Retention time.Duration
	// PruneInterval is the cadence of background pruning. Default: 60s.
	// Set to 0 to disable the prune goroutine (useful in tests).
	PruneInterval time.Duration
	// DrainTimeout is the Shutdown drain window. Default: ShutdownDrain.
	DrainTimeout time.Duration
}

// NewHubWithConfig builds a hub with custom sizing — primarily for tests that
// need small buffers or zero prune intervals.
func NewHubWithConfig(cfg HubConfig) SSEHub {
	logSize := cfg.LogSize
	if logSize <= 0 {
		logSize = DefaultLogSize
	}
	retention := cfg.Retention
	if retention <= 0 {
		retention = DefaultReplayWindow
	}
	drain := cfg.DrainTimeout
	if drain <= 0 {
		drain = ShutdownDrain
	}

	var pruneInterval time.Duration
	switch {
	case cfg.PruneInterval < 0:
		pruneInterval = 0 // sentinel: do not start
	case cfg.PruneInterval == 0:
		pruneInterval = 60 * time.Second
	default:
		pruneInterval = cfg.PruneInterval
	}

	log := newEventLog(logSize)
	h := &hub{
		log:          log,
		subscribers:  make(map[uuid.UUID]map[string]SSEClient),
		clients:      make(map[string]SSEClient),
		byUser:       make(map[uuid.UUID]int),
		closeCh:      make(chan struct{}),
		drainTimeout: drain,
	}

	if pruneInterval > 0 {
		go h.pruneLoop(pruneInterval, retention)
	}

	return h
}

// pruneLoop is the background retention sweeper.
func (h *hub) pruneLoop(interval, retention time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.closeCh:
			return
		case <-ticker.C:
			h.log.Prune(retention)
		}
	}
}

// Subscribe registers a client for tree events. Errors are:
//   - ErrTooManyConnectionsTree   — tree has ≥ MaxConnectionsPerTree
//   - ErrTooManyConnectionsUser   — user has ≥ MaxConnectionsPerUser
//   - ErrTooManyConnections       — server has ≥ MaxConnectionsTotal
//
// On success, the hub owns the client lifecycle; callers SHOULD call
// Unsubscribe (typically via defer) when the connection ends. Sending an
// already-subscribed client ID is a programming error (returns
// ErrClientAlreadySubscribed wrapped in a fmt error).
func (h *hub) Subscribe(_ context.Context, treeID uuid.UUID, client SSEClient) error {
	if h.closed.Load() {
		return ErrClientNotFound // hub is shutting down
	}
	userID := client.UserID()

	// Per-user and per-tree limit checks. Done under write lock to keep
	// counts consistent.
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, exists := h.clients[client.ID()]; exists {
		return fmt.Errorf("client %q already subscribed: %w", client.ID(), errAlreadySubscribed)
	}
	if MaxConnectionsPerUser > 0 {
		if h.byUser[userID] >= MaxConnectionsPerUser {
			return fmt.Errorf("user %s: %w", userID, ErrTooManyConnectionsUser)
		}
	}
	if MaxConnectionsTotal > 0 && h.totalConnections.Load() >= int64(MaxConnectionsTotal) {
		return fmt.Errorf("%w (%d/%d)", ErrTooManyConnections, h.totalConnections.Load(), MaxConnectionsTotal)
	}
	treeClients, ok := h.subscribers[treeID]
	if !ok {
		treeClients = make(map[string]SSEClient)
		h.subscribers[treeID] = treeClients
	}
	if MaxConnectionsPerTree > 0 && len(treeClients) >= MaxConnectionsPerTree {
		return fmt.Errorf("tree %s: %w", treeID, ErrTooManyConnectionsTree)
	}

	treeClients[client.ID()] = client
	h.clients[client.ID()] = client
	h.byUser[userID]++
	h.totalConnections.Add(1)
	return nil
}

var errAlreadySubscribed = errors.New("already subscribed")

// Unsubscribe removes a client from a tree. Idempotent — safe to call if
// the client was never subscribed or has already been removed.
func (h *hub) Unsubscribe(treeID uuid.UUID, clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	client, ok := h.clients[clientID]
	if !ok {
		return
	}
	if treeClients, ok := h.subscribers[treeID]; ok {
		delete(treeClients, clientID)
		if len(treeClients) == 0 {
			delete(h.subscribers, treeID)
		}
	}
	delete(h.clients, clientID)
	h.byUser[client.UserID()]--
	if h.byUser[client.UserID()] < 0 {
		h.byUser[client.UserID()] = 0
	}
	h.totalConnections.Add(-1)
}

// Broadcast sends an event to every subscriber of a tree. Slow clients
// (buffer full) are closed and unsubscribed automatically per SPEC-API-01 §9.1.
//
// If the event has SequenceNum == 0, the hub assigns a fresh monotonic
// number via the event log. Otherwise the event is broadcast verbatim
// (already-numbered events are NOT re-logged — useful for replay).
func (h *hub) Broadcast(treeID uuid.UUID, event SSEEvent) SSEEvent {
	if h.closed.Load() {
		return event
	}

	// Assign sequence + log if not already populated.
	if event.SequenceNum == 0 {
		event = h.log.Append(treeID, event.Type, event.Data, event.ActorID)
	}

	if event.ID == "" {
		event.ID = EventID(event.TreeID, event.SequenceNum)
	}

	h.mu.RLock()
	treeClients := make([]SSEClient, 0, len(h.subscribers[treeID]))
	for _, c := range h.subscribers[treeID] {
		treeClients = append(treeClients, c)
	}
	h.mu.RUnlock()

	// Track which clients must be unsubscribed because they couldn't keep up.
	var toDrop []SSEClient
	for _, c := range treeClients {
		if err := c.Send(event); err != nil {
			toDrop = append(toDrop, c)
		}
	}
	for _, c := range toDrop {
		h.Unsubscribe(treeID, c.ID())
		_ = c.Close()
	}
	return event
}

// clientByID looks up a client by ID without holding the hub lock — used only
// from Broadcast's drop list.
func (h *hub) clientByID(id string) SSEClient {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clients[id]
}

// ReplaySince streams events since the provided Last-Event-ID to a single
// client. If the gap exceeds MaxEvents, the client is sent a tree_snapshot
// event (placeholder) and skipped events are dropped — full snapshot
// reconstruction is a post-MVP concern that requires the postgres layer.
//
// sinceEventID format: "tree_id:sequence_num" — anything else is treated as
// a malformed cursor and rejected.
func (h *hub) ReplaySince(_ context.Context, treeID uuid.UUID, clientID string, sinceEventID string) error {
	client := h.clientByID(clientID)
	if client == nil {
		return ErrClientNotFound
	}

	treeUUID, seq, err := parseEventID(sinceEventID)
	if err != nil {
		return fmt.Errorf("invalid sinceEventID %q: %w", sinceEventID, err)
	}
	// Per SPEC-API-01 §15 #19: parsed tree_id from the event ID is ignored —
	// replay is scoped to the URL tree_id. We only validate the format.
	_ = treeUUID

	events, truncated, err := h.log.Since(treeID, seq, DefaultLogSize)
	if err != nil {
		return err
	}
	if truncated {
		// Emit an in-stream error so the client knows it missed events,
		// then return so the next reconnect picks up a snapshot.
		_ = client.SendRaw(formatErrorEvent(treeID, "EVENT_BUFFER_OVERFLOW",
			"event buffer overflow — some events may be missing"))
	}

	for _, ev := range events {
		if err := client.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

// SubscriberCount returns the number of active subscribers for a tree.
func (h *hub) SubscriberCount(treeID uuid.UUID) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers[treeID])
}

// TotalConnections returns the global active connection count.
func (h *hub) TotalConnections() int {
	return int(h.totalConnections.Load())
}

// Shutdown sends a "done" event to every client, waits drainTimeout, then
// closes every connection. Returns ctx.Err() if ctx fires first.
func (h *hub) Shutdown(ctx context.Context) error {
	if !h.closed.CompareAndSwap(false, true) {
		return nil // already shut down
	}
	close(h.closeCh)

	// Snapshot clients under lock, then send/close without holding it.
	h.mu.RLock()
	snapshot := make([]SSEClient, 0, len(h.clients))
	for _, c := range h.clients {
		snapshot = append(snapshot, c)
	}
	h.mu.RUnlock()

	for _, c := range snapshot {
		_ = c.SendRaw(formatDoneEvent(c.TreeID(), "server_shutdown",
			"canopyd is shutting down for maintenance"))
	}

	// Wait for clients to close themselves or drainTimeout to elapse.
	drainCtx, cancel := context.WithTimeout(ctx, h.drainTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if h.TotalConnections() == 0 {
			return nil
		}
		select {
		case <-drainCtx.Done():
			// Force-close remaining connections.
			h.mu.RLock()
			remaining := make([]SSEClient, 0, len(h.clients))
			for _, c := range h.clients {
				remaining = append(remaining, c)
			}
			h.mu.RUnlock()
			for _, c := range remaining {
				h.Unsubscribe(c.TreeID(), c.ID())
				_ = c.Close()
			}
			h.totalConnections.Store(0)
			return nil
		case <-ticker.C:
			// re-check
		}
	}
}

// Prune removes events older than retention from the hub's event log
// across every tree. Returns the number removed.
func (h *hub) Prune(retention time.Duration) int {
	return h.log.Prune(retention)
}

// PruneEvents trims events older than retention from any hub that exposes
// a Prune method. Returns 0 if the hub is not the package's concrete
// implementation (e.g. a test double). Useful from tests and ops tooling.
func PruneEvents(h SSEHub, retention time.Duration) int {
	if p, ok := h.(interface{ Prune(time.Duration) int }); ok {
		return p.Prune(retention)
	}
	return 0
}

// --- helpers ---------------------------------------------------------------

// formatErrorEvent formats an in-stream error event (SPEC-API-01 §10.2).
func formatErrorEvent(treeID uuid.UUID, code, message string) string {
	payload, _ := json.Marshal(struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}{Error: message, Code: code})
	env := envelope{
		EventType:   "error",
		TreeID:      treeID.String(),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		SequenceNum: 0,
		ActorID:     zeroUUIDString,
		Data:        payload,
	}
	out, _ := json.Marshal(env)
	return "event: error\ndata: " + jsonMustString(out) + "\n\n"
}

// formatDoneEvent formats a "done" event (SPEC-API-01 §5.2.12).
func formatDoneEvent(treeID uuid.UUID, reason, message string) string {
	payload, _ := json.Marshal(struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}{Reason: reason, Message: message})
	env := envelope{
		EventType:   "done",
		TreeID:      treeID.String(),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		SequenceNum: 0,
		ActorID:     zeroUUIDString,
		Data:        payload,
	}
	out, _ := json.Marshal(env)
	return "event: done\ndata: " + jsonMustString(out) + "\n\n"
}

// envelope mirrors the §5.2 wire shape (used to build error/done events
// outside the standard broadcast path).
type envelope struct {
	EventType   string          `json:"event_type"`
	TreeID      string          `json:"tree_id"`
	Timestamp   string          `json:"timestamp"`
	SequenceNum int64           `json:"sequence_num"`
	ActorID     string          `json:"actor_id"`
	Data        json.RawMessage `json:"data"`
}

const zeroUUIDString = "00000000-0000-0000-0000-000000000000"

// jsonMustString returns a JSON-encoded string (with surrounding quotes)
// suitable for concatenation into a single SSE data: line.
func jsonMustString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// PruneEvents trims events older than retention from any hub that exposes
// a Prune method. Returns 0 if the hub is not the package's concrete
// implementation (e.g. a test double). Useful from tests and ops tooling
// parseEventID splits a "treeID:seqNum" SSE event ID. Returns ErrInvalidID
// if either component fails to parse.
func parseEventID(id string) (uuid.UUID, int64, error) {
	if id == "" {
		return uuid.Nil, 0, errors.New("empty event id")
	}
	idx := strings.LastIndex(id, ":")
	if idx < 0 {
		return uuid.Nil, 0, errors.New("missing sequence separator")
	}
	treeStr := id[:idx]
	seqStr := id[idx+1:]

	treeID, err := uuid.Parse(treeStr)
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("invalid tree_id: %w", err)
	}
	var seq int64
	if _, err := fmt.Sscanf(seqStr, "%d", &seq); err != nil {
		return uuid.Nil, 0, fmt.Errorf("invalid sequence_num: %w", err)
	}
	return treeID, seq, nil
}
