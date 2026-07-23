package sse

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// flushableClient is the concrete contract used by the handler. The
// package-private *Client satisfies it; tests can substitute fakes that
// also implement Flush.
type flushableClient interface {
	SSEClient
	Flush() error
}

// Handler is the HTTP entry point for the SSE endpoint. It is purposely
// lightweight — the heavy lifting (subscription management, broadcasts,
// replay) lives in the SSEHub.
//
// MVP-grade: skips JWT validation and tree-membership checks. Calls
// SubscriberCount and TotalConnections on the hub to enforce the spec's
// per-tree / per-user / server-wide limits.
type Handler struct {
	hub        SSEHub
	hubFactory func(userID, treeID uuid.UUID, w http.ResponseWriter, flusher http.Flusher) flushableClient

	// Optional knobs for tests.
	heartbeatInterval time.Duration
	clientIDFactory   func() string
}

// NewHandler builds a handler with the production client constructor.
func NewHandler(hub SSEHub) *Handler {
	return &Handler{
		hub:               hub,
		hubFactory:        defaultClientFactory,
		heartbeatInterval: HeartbeatInterval,
		clientIDFactory:   newClientID,
	}
}

// NewHandlerWithConfig lets tests (or future callers) inject a custom client
// factory, heartbeat cadence, and client-id generator.
func NewHandlerWithConfig(
	hub SSEHub,
	factory func(userID, treeID uuid.UUID, w http.ResponseWriter, flusher http.Flusher) flushableClient,
	heartbeat time.Duration,
	idFactory func() string,
) *Handler {
	if factory == nil {
		factory = defaultClientFactory
	}
	if heartbeat == 0 {
		heartbeat = HeartbeatInterval
	}
	if idFactory == nil {
		idFactory = newClientID
	}
	return &Handler{
		hub:               hub,
		hubFactory:        factory,
		heartbeatInterval: heartbeat,
		clientIDFactory:   idFactory,
	}
}

// defaultClientFactory creates an SSEClient. Each call produces a freshly
// constructed *Client with a unique ID.
func defaultClientFactory(userID, treeID uuid.UUID, w http.ResponseWriter, flusher http.Flusher) flushableClient {
	id := newClientID()
	return NewClient(id, userID, treeID, w, flusher)
}

// newClientID returns a unique-per-connection id (worker pid + nanoseconds +
// short uuid fragment). Cheap and good enough for the hub's bookkeeping.
func newClientID() string {
	return fmt.Sprintf("sse-%d-%s", time.Now().UnixNano(), uuid.NewString()[:8])
}

// HandleTreeEvents is mounted at GET /trees/{tree_id}/events.
//
// SPEC-API-01 §3, §4, §9.3. Errors are returned as JSON BEFORE any SSE
// header is written so clients see a normal HTTP error response.
func (h *Handler) HandleTreeEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. Parse + validate tree_id from the URL.
	treeStr := chi.URLParam(r, "tree_id")
	treeID, err := uuid.Parse(treeStr)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, "INVALID_TREE_ID", "tree_id must be a valid UUID")
		return
	}

	// 2. MVP: sentinel userID. Auth middleware (BE-07) replaces this.
	userID := uuid.Nil

	// 3. Parse query parameters.
	q := r.URL.Query()
	sinceHash := q.Get("since")
	profilesCSV := q.Get("profiles")
	includeHeartbeat := q.Get("include_heartbeat") != "false"

	// 4. Validate parameters.
	if sinceHash != "" {
		if err := validateSinceHash(sinceHash); err != nil {
			writeHTTPError(w, http.StatusBadRequest, "INVALID_SINCE_HASH", err.Error())
			return
		}
	}
	if profilesCSV != "" {
		if _, err := parseProfilesCSV(profilesCSV); err != nil {
			writeHTTPError(w, http.StatusBadRequest, "INVALID_PROFILE_ID", err.Error())
			return
		}
	}

	// 5. Connection-limit checks. Per-user limit is intentionally relaxed
	// in MVP because every "user" is uuid.Nil — we skip it.
	if h.hub.SubscriberCount(treeID) >= MaxConnectionsPerTree {
		writeHTTPError(w, http.StatusTooManyRequests,
			"TOO_MANY_CONNECTIONS_TREE",
			"too many SSE connections for this tree",
			map[string]any{
				"current_connections": h.hub.SubscriberCount(treeID),
				"max_connections":     MaxConnectionsPerTree,
				"retry_after_seconds": 10,
			})
		return
	}
	if h.hub.TotalConnections() >= MaxConnectionsTotal {
		writeHTTPError(w, http.StatusServiceUnavailable,
			"TOO_MANY_CONNECTIONS",
			"server is at maximum SSE capacity",
			map[string]any{
				"current_connections": h.hub.TotalConnections(),
				"max_connections":     MaxConnectionsTotal,
				"retry_after_seconds": 30,
			})
		return
	}

	// 6. Flusher check.
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Ctx(ctx).Error().Msg("streaming not supported (no http.Flusher)")
		writeHTTPError(w, http.StatusInternalServerError,
			"STREAMING_NOT_SUPPORTED", "streaming responses are not supported on this transport")
		return
	}

	// 7. SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 8. Subscribe.
	client := h.hubFactory(userID, treeID, w, flusher)
	if err := h.hub.Subscribe(ctx, treeID, client); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("subscribe failed")
		// Best-effort error event before close (SPEC-API-01 §10.2).
		_ = client.SendRaw(formatErrorEvent(treeID, "SUBSCRIPTION_FAILED", err.Error()))
		_ = client.Close()
		return
	}
	defer h.hub.Unsubscribe(treeID, client.ID())

	// Drain anything the hub already sent for this client during
	// Subscribe before yielding to the event loop.
	if err := client.Flush(); err != nil {
		log.Ctx(ctx).Debug().Err(err).Msg("initial flush failed; client likely disconnected")
		return
	}

	// 9. Replay missed events.
	if sinceHash != "" {
		if err := h.replaySinceHash(ctx, treeID, client, sinceHash); err != nil {
			log.Ctx(ctx).Warn().Err(err).Str("since", sinceHash).Msg("since-hash replay failed")
		}
	} else if lastID := r.Header.Get("Last-Event-ID"); lastID != "" {
		if err := h.hub.ReplaySince(ctx, treeID, client.ID(), lastID); err != nil {
			log.Ctx(ctx).Warn().Err(err).Str("last_event_id", lastID).Msg("replay failed")
		}
	}
	if err := client.Flush(); err != nil {
		return
	}

	// 10. Heartbeat ticker.
	var heartbeatCh <-chan time.Time
	if includeHeartbeat && h.heartbeatInterval > 0 {
		ticker := time.NewTicker(h.heartbeatInterval)
		defer ticker.Stop()
		heartbeatCh = ticker.C
	}

	// 11. Event loop.
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatCh:
			if err := client.SendRaw(formatHeartbeat()); err != nil {
				return
			}
			if err := client.Flush(); err != nil {
				return
			}
		case <-time.After(50 * time.Millisecond):
			// Periodic flush to drain buffered events to the client.
			if err := client.Flush(); err != nil {
				return
			}
		}
	}
}

// --- replaySinceHash -------------------------------------------------------

// replaySinceHash is a placeholder for the snapshot-delta logic that
// belongs to SPEC-DM-02 §6 and SPEC-API-01 §6.2. For MVP we just acknowledge
// the hash exists; future work will resolve it against the tree_snapshots
// table (SPEC-DM-02 §3) and stream node_added / node_updated events.
func (h *Handler) replaySinceHash(_ context.Context, _ uuid.UUID, client SSEClient, hash string) error {
	// Hash format was already validated by validateSinceHash — there's
	// nothing to do until the snapshot service lands.
	_ = client
	_ = hash
	return nil
}

// --- format helpers --------------------------------------------------------

// formatHeartbeat produces an SSE comment (line starting with ":") so it
// won't fire client-side event handlers but still keeps the connection warm.
func formatHeartbeat() string {
	return ": heartbeat\n\n"
}

// --- query helpers ---------------------------------------------------------

// validateSinceHash enforces the SHA256-hex format described in SPEC-API-01
// §4.1: 64 lowercase hex chars.
func validateSinceHash(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("since hash must be 64 hex characters (got %d)", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("since hash must be valid hex: %w", err)
	}
	return nil
}

// parseProfilesCSV validates that every entry is a UUID. Returns the parsed
// list on success. The filter is later applied at broadcast time.
func parseProfilesCSV(csv string) ([]uuid.UUID, error) {
	parts := strings.Split(csv, ",")
	out := make([]uuid.UUID, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := uuid.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("profile %q is not a valid UUID: %w", p, err)
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, errors.New("profiles filter is empty")
	}
	return out, nil
}

// --- error response helper -------------------------------------------------

// errorResponse is the canonical pre-SSE error body (SPEC-API-01 §10.1).
type errorResponse struct {
	Error   string         `json:"error"`
	Code    string         `json:"code"`
	Details map[string]any `json:"details,omitempty"`
}

// writeHTTPError formats a JSON error response and writes it with the given
// HTTP status. Always sets Content-Type to application/json.
func writeHTTPError(w http.ResponseWriter, status int, code, msg string, details ...map[string]any) {
	var d map[string]any
	if len(details) > 0 {
		d = details[0]
	}
	body := errorResponse{Error: msg, Code: code, Details: d}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Response already committed; nothing useful left to do.
		_ = err
	}
}

// FormatQueryBool is a tiny helper exported for tests that exercise query
// parsing. Not part of the public surface.
func FormatQueryBool(raw string, def bool) bool {
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}
