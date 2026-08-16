// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file implements the workspace channels SSE surface (SPEC-023 §5).
//
// Endpoints (mounted at /api/v1/workspace/channels by server.go):
//
//	GET  /                      — list channels
//	POST /{channel_id}/message  — send a message (broadcasts channel_message)
//	GET  /{channel_id}/feed     — SSE event stream for a channel
package handler

import (
	"fmt"
	"net/http"
	stdsync "sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

// --- workspaceChannelRegistry: in-memory channel registry (SPEC-023 §5) -----
//
// Channels are MVP-grade and live entirely in memory — no DB table. The
// registry is seeded with a couple of well-known channels ("general",
// "agents") on construction. Channel IDs are deterministic UUIDs derived
// from a fixed namespace so they are stable across restarts and testable.

// channelNamespace is a fixed UUID used to derive stable channel IDs via
// uuid.NewSHA1(channelNamespace, []byte(name)).
var channelNamespace = uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")

// defaultChannels are seeded on registry construction.
var defaultChannels = []string{"general", "agents"}

// channelInfo describes a single workspace channel.
type channelInfo struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type workspaceChannelRegistry struct {
	mu       stdsync.RWMutex
	channels map[uuid.UUID]channelInfo // id → info
}

func newWorkspaceChannelRegistry() *workspaceChannelRegistry {
	r := &workspaceChannelRegistry{channels: make(map[uuid.UUID]channelInfo)}
	for _, name := range defaultChannels {
		r.seed(name)
	}
	return r
}

// seed adds a named channel with a deterministic ID. Idempotent.
func (r *workspaceChannelRegistry) seed(name string) channelInfo {
	id := uuid.NewSHA1(channelNamespace, []byte(name))
	info := channelInfo{ID: id, Name: name}
	r.channels[id] = info
	return info
}

// list returns all registered channels.
func (r *workspaceChannelRegistry) list() []channelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]channelInfo, 0, len(r.channels))
	for _, ch := range r.channels {
		out = append(out, ch)
	}
	return out
}

// get returns a channel by ID and whether it exists.
func (r *workspaceChannelRegistry) get(id uuid.UUID) (channelInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ch, ok := r.channels[id]
	return ch, ok
}

// --- WorkspaceHandler -------------------------------------------------------

// WorkspaceHandler exposes the workspace channels surface (SPEC-023 §5).
type WorkspaceHandler struct {
	hub      sse.SSEHub
	channels *workspaceChannelRegistry
}

// NewWorkspaceHandler returns a handler wired to the given SSE hub. Channels
// are seeded with the default set on construction. The hub comes from
// server.New's sseHub argument — no DB dependency for MVP.
func NewWorkspaceHandler(hub sse.SSEHub) *WorkspaceHandler {
	return &WorkspaceHandler{
		hub:      hub,
		channels: newWorkspaceChannelRegistry(),
	}
}

// Routes mounts the workspace channel endpoints.
func (h *WorkspaceHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.ListChannels)
	r.Post("/{channel_id}/message", h.SendMessage)
	r.Get("/{channel_id}/feed", h.ChannelEvents)
	return r
}

// --- ListChannels -----------------------------------------------------------

// channelListItem is a single entry in the channel list response. The
// subscriber count is best-effort (computed from the live SSE hub).
type channelListItem struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	SubscriberCount int       `json:"subscriber_count,omitempty"`
}

// ListChannels returns the list of available channels as a JSON array.
func (h *WorkspaceHandler) ListChannels(w http.ResponseWriter, r *http.Request) {
	channels := h.channels.list()
	items := make([]channelListItem, 0, len(channels))
	for _, ch := range channels {
		items = append(items, channelListItem{
			ID:              ch.ID,
			Name:            ch.Name,
			SubscriberCount: h.hub.SubscriberCount(ch.ID),
		})
	}
	writeJSON(w, http.StatusOK, items)
}

// --- SendMessage ------------------------------------------------------------

// sendMessageRequest is the JSON body for POST /{channel_id}/message.
type sendMessageRequest struct {
	SenderID *uuid.UUID `json:"sender_id,omitempty"`
	Content  string     `json:"content"`
}

// sendMessageResponse is the JSON body for a successful POST.
type sendMessageResponse struct {
	MessageID uuid.UUID `json:"message_id"`
	ChannelID uuid.UUID `json:"channel_id"`
	Content   string    `json:"content"`
}

// channelMessageData is the SSE event payload broadcast to feed subscribers.
type channelMessageData struct {
	MessageID uuid.UUID `json:"message_id"`
	ChannelID uuid.UUID `json:"channel_id"`
	SenderID  uuid.UUID `json:"sender_id"`
	Content   string    `json:"content"`
	SentAt    string    `json:"sent_at"`
}

// SendMessage accepts a message body, validates it, broadcasts a
// "channel_message" event to the channel, and returns 202.
func (h *WorkspaceHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	channelID, ok := parseChannelID(w, r)
	if !ok {
		return
	}

	ch, exists := h.channels.get(channelID)
	if !exists {
		writeError(w, http.StatusNotFound, "CHANNEL_NOT_FOUND",
			fmt.Sprintf("channel %s does not exist", channelID))
		return
	}

	var req sendMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY",
			"request body must be valid JSON: "+err.Error())
		return
	}

	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "EMPTY_CONTENT",
			"content must not be empty")
		return
	}

	// Sender: explicit body value wins; fall back to authenticated user.
	var senderID uuid.UUID
	if req.SenderID != nil {
		senderID = *req.SenderID
	} else {
		senderID = UserIDFromContext(r.Context())
	}

	// Message IDs use uuidv7 (time-ordered, lexicographically sortable).
	msgID, err := uuid.NewV7()
	if err != nil {
		// uuid.NewV7 can only fail if the system clock is broken; fall back.
		msgID = uuid.New()
	}

	data := channelMessageData{
		MessageID: msgID,
		ChannelID: ch.ID,
		SenderID:  senderID,
		Content:   req.Content,
		SentAt:    time.Now().UTC().Format(time.RFC3339),
	}

	h.hub.Broadcast(ch.ID, sse.ComposeEvent(ch.ID, senderID, "channel_message", data))

	writeJSON(w, http.StatusAccepted, sendMessageResponse{
		MessageID: msgID,
		ChannelID: ch.ID,
		Content:   req.Content,
	})
}

// --- ChannelEvents (SSE) ----------------------------------------------------

// ChannelEvents streams channel events via SSE. Mirrors the established
// HandleTreeEvents structure (internal/sse/sse_handler.go:95) — in particular
// the client is subscribed BEFORE the 200 headers are written so a broadcast
// landing between the flush and Subscribe is never missed.
func (h *WorkspaceHandler) ChannelEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. Parse + validate channel_id.
	channelID, ok := parseChannelID(w, r)
	if !ok {
		return
	}
	if _, exists := h.channels.get(channelID); !exists {
		writeError(w, http.StatusNotFound, "CHANNEL_NOT_FOUND",
			fmt.Sprintf("channel %s does not exist", channelID))
		return
	}

	// 2. Parse query parameters.
	q := r.URL.Query()
	includeHeartbeat := q.Get("include_heartbeat") != "false"

	// 3. MVP: userID from auth context (used by the hub for per-user
	// connection counting). uuid.Nil when unauthenticated.
	userID := UserIDFromContext(ctx)

	// 4. Connection-limit checks.
	if h.hub.SubscriberCount(channelID) >= sse.MaxConnectionsPerTree {
		writeError(w, http.StatusTooManyRequests,
			"TOO_MANY_CONNECTIONS_CHANNEL",
			"too many SSE connections for this channel")
		return
	}
	if h.hub.TotalConnections() >= sse.MaxConnectionsTotal {
		writeError(w, http.StatusServiceUnavailable,
			"TOO_MANY_CONNECTIONS",
			"server is at maximum SSE capacity")
		return
	}

	// 5. Flusher check.
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Ctx(ctx).Error().Msg("streaming not supported (no http.Flusher)")
		writeError(w, http.StatusInternalServerError,
			"STREAMING_NOT_SUPPORTED",
			"streaming responses are not supported on this transport")
		return
	}

	// 6. Subscribe BEFORE writing the 200 + flushing. The client buffers to
	// an outbox (writes only happen on Flush), so no bytes reach the network
	// before the headers are written — but the subscription is already
	// registered by the time the client's GET returns. This closes the race
	// where a broadcast landing between the header flush and a post-flush
	// Subscribe is missed, causing block-reading clients to hang forever.
	client := sse.NewClient(
		fmt.Sprintf("ws-%d-%s", time.Now().UnixNano(), uuid.NewString()[:8]),
		userID, channelID, w, flusher,
	)
	if err := h.hub.Subscribe(ctx, channelID, client); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("channel sse subscribe failed")
		writeError(w, http.StatusInternalServerError,
			"SUBSCRIPTION_FAILED",
			"failed to subscribe to channel events")
		_ = client.Close()
		return
	}
	defer h.hub.Unsubscribe(channelID, client.ID())

	// 7. SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Drain anything the hub already sent for this client during Subscribe
	// before yielding to the event loop.
	if err := client.Flush(); err != nil {
		log.Ctx(ctx).Debug().Err(err).Msg("initial flush failed; client likely disconnected")
		return
	}

	// 8. Replay missed events via Last-Event-ID.
	if lastID := r.Header.Get("Last-Event-ID"); lastID != "" {
		if err := h.hub.ReplaySince(ctx, channelID, client.ID(), lastID); err != nil {
			log.Ctx(ctx).Warn().Err(err).Str("last_event_id", lastID).Msg("channel replay failed")
		}
	}
	if err := client.Flush(); err != nil {
		return
	}

	// 9. Heartbeat ticker.
	var heartbeatCh <-chan time.Time
	if includeHeartbeat && sse.HeartbeatInterval > 0 {
		ticker := time.NewTicker(sse.HeartbeatInterval)
		defer ticker.Stop()
		heartbeatCh = ticker.C
	}

	// 10. Event loop.
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatCh:
			if err := client.SendRaw(": heartbeat\n\n"); err != nil {
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

// --- helpers ----------------------------------------------------------------

// parseChannelID reads and validates the {channel_id} chi URL parameter.
func parseChannelID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "channel_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CHANNEL_ID",
			"channel_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}
