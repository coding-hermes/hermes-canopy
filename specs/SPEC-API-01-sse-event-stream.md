# SPEC-API-01 — SSE Event Stream Spec

> **Status:** Spec | **Blocks:** SPEC-API-02, BE-05 (SSE Hub), BE-06 (Sync Engine), BE-11 (HTTP Router), FE-02 (Tree Data Store)
> **References:** SPEC-DM-01, SPEC-DM-02, SPEC-DM-03, SPEC-DM-04, ARCHITECTURE.md §4, T1.1-transport-research.md, T1.8-multi-transport-architecture.md

---

## 1. Purpose

Define the exact Server-Sent Events (SSE) endpoint contract for real-time tree synchronization between canopyd (Go backend) and the Canopy frontend (PWA). A Go worker reading this spec must produce a correct SSE hub, handler, and connection manager with zero clarifying questions. A TypeScript worker reading this spec must produce a correct SSE Yjs provider that maps SSE events to Yjs updates.

The SSE endpoint is the **primary real-time channel** for tree data flowing from server to client. All mutations originate via HTTP POST (SPEC-API-02, SPEC-API-03) and are broadcast to subscribed clients through this endpoint.

---

## 2. Design Decisions (from ARCHITECTURE.md)

| Decision | Choice | Source |
|----------|--------|--------|
| Transport | SSE (HTTP/2) primary — server→client push | ARCHITECTURE.md §4.1, T1.1 |
| Client→Server | HTTP POST via REST endpoints | ARCHITECTURE.md §4.2 |
| Reconnection | Browser EventSource auto-reconnect, Last-Event-ID | ARCHITECTURE.md §4.5, T1.1 §4 |
| HTTP/2 | Required for >6 concurrent SSE connections per browser | ARCHITECTURE.md §2.1, T1.1 §1 |
| Event ID format | tree_id:sequence_number (opaque) | This spec §5 |
| Heartbeat | 30s interval | T1.1 §6, ARCHITECTURE.md §4.1 |
| Auth | JWT Bearer token validated at connection time | ARCHITECTURE.md §5.5 |
| Maximum connections per tree | 100 per tree, 10 per user | This spec §12 |
| SSE over HTTP/1.1 fallback | Supported with 6-connection browser limit | T1.1 §1 |
| Relay protocol opcodes | 13 opcodes (NODE_ADDED, EDGE_ADDED, etc.) | T1.8-multi-transport.md §3 |

---

## 3. Endpoint

### 3.1 Route

```
GET /trees/{tree_id}/events
```

| Field | Value |
|-------|-------|
| Method | GET |
| Path | `/trees/{tree_id}/events` |
| tree_id | UUIDv7 (from SPEC-DM-01 §3.1) |
| Content-Type (response) | `text/event-stream` |
| Cache-Control | `no-cache` |
| Connection | `keep-alive` |
| X-Accel-Buffering | `no` (for nginx proxies) |

### 3.2 Response Headers

```http
HTTP/1.1 200 OK
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Accel-Buffering: no
Access-Control-Allow-Origin: *
```

Note: Auth failures return standard HTTP error responses BEFORE SSE headers are set. The client receives a regular JSON error, not an SSE event.

---

## 4. Query Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `since` | string (SHA256 hex) | No | — | Tree snapshot hash. Server computes delta from this snapshot. If omitted, full tree sync. |
| `profiles` | string (CSV UUIDs) | No | — | Comma-separated profile UUIDs. Filter events to only those authored by these profiles. |
| `include_heartbeat` | boolean | No | `true` | Whether to include heartbeat events in the stream. |

### 4.1 Parameter Validation

- `since`: Must be a valid 64-character hex string (SHA256). If invalid → HTTP 400 `{"error": "INVALID_SINCE_HASH", "code": "INVALID_PARAMETER"}`
- `profiles`: Each UUID validated via `uuid.Parse()`. Invalid UUID → HTTP 400 `{"error": "INVALID_PROFILE_ID", "code": "INVALID_PARAMETER"}`
- `include_heartbeat`: Parsed as boolean (`"true"` or `"false"`, case-insensitive). Invalid → treated as `true`.

All validation errors are returned as standard HTTP JSON responses (not SSE events) and the connection is closed immediately.

---

## 5. Event Format

### 5.1 SSE Wire Format

Every event follows the SSE specification (WHATWG / W3C):

```
id: {tree_id}:{sequence_number}
event: {event_type}
data: {json_payload}
retry: 3000

```

- `id`: Format `{tree_id}:{sequence_number}`. Opaque to clients — used only for Last-Event-ID.
- `event`: Event type string (see §5.2).
- `data`: Single-line JSON. Must NOT contain newlines in the JSON payload. Use compact JSON encoding.
- `retry`: Reconnection backoff hint in milliseconds. Initial: 3000ms. Adjusts based on server load.

### 5.2 Event Types & JSON Shapes

Every event data field contains:

```json
{
  "event_type": "string",
  "tree_id": "uuid",
  "timestamp": "ISO8601",
  "sequence_num": "int64",
  "actor_id": "uuid",
  "data": { ... }
}
```

#### 5.2.1 `node_added`

Emitted when a new node is created in the tree (via POST `/trees/{tree_id}/nodes`).

```json
{
  "event_type": "node_added",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:23:04Z",
  "sequence_num": 47,
  "actor_id": "0191a8b2-...",
  "data": {
    "node": {
      "id": "0191a9c3-...",
      "tree_id": "0191a8b2-...",
      "parent_id": "0191a8c0-...",
      "author_id": "0191a8b2-...",
      "content": "string",
      "content_format": "markdown",
      "node_type": "message",
      "sequence_num": 47,
      "metadata": {},
      "created_at": "2026-07-20T20:23:04Z"
    }
  }
}
```

The `node` object uses the full Node shape from SPEC-DM-01 §4 (Go struct) and SPEC-DM-01 §5 (TS type). Fields excluded: `edited_at`, `deleted_at` (not applicable on creation).

#### 5.2.2 `node_updated`

Emitted when a node's content or metadata is modified (via PATCH `/nodes/{node_id}`).

```json
{
  "event_type": "node_updated",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:24:00Z",
  "sequence_num": 48,
  "actor_id": "0191a8b2-...",
  "data": {
    "node_id": "0191a9c3-...",
    "changes": {
      "content": "updated content",
      "edited_at": "2026-07-20T20:24:00Z"
    },
    "version": 2
  }
}
```

`changes` is a flat map of field-name → new-value. Only includes fields that changed. The `version` counter increments on every update.

#### 5.2.3 `node_removed`

Emitted when a node is soft-deleted (via DELETE `/nodes/{node_id}`).

```json
{
  "event_type": "node_removed",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:25:00Z",
  "sequence_num": 49,
  "actor_id": "0191a8b2-...",
  "data": {
    "node_id": "0191a9c3-...",
    "deleted_at": "2026-07-20T20:25:00Z",
    "cascade_removed_edge_ids": ["0191a9c4-...", "0191a9c5-..."]
  }
}
```

`cascade_removed_edge_ids`: edges that were deleted because their source or target node was removed (CASCADE behavior per SPEC-DM-01 §3.4 FK constraint).

#### 5.2.4 `edge_added`

Emitted when a new edge is created between nodes.

```json
{
  "event_type": "edge_added",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:23:04Z",
  "sequence_num": 47,
  "actor_id": "0191a8b2-...",
  "data": {
    "edge": {
      "id": "0191a9c6-...",
      "tree_id": "0191a8b2-...",
      "source_id": "0191a8c0-...",
      "target_id": "0191a9c3-...",
      "edge_type": "reply",
      "sequence_num": 1,
      "metadata": {},
      "created_at": "2026-07-20T20:23:04Z"
    }
  }
}
```

Full Edge shape from SPEC-DM-01 §4 and §5.

#### 5.2.5 `edge_removed`

Emitted when an edge is removed.

```json
{
  "event_type": "edge_removed",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:26:00Z",
  "sequence_num": 50,
  "actor_id": "0191a8b2-...",
  "data": {
    "edge_id": "0191a9c6-...",
    "source_id": "0191a8c0-...",
    "target_id": "0191a9c3-...",
    "edge_type": "reply"
  }
}
```

#### 5.2.6 `approval_changed`

Emitted when an approval's status changes (per SPEC-DM-03 §3.2 approval FSM).

```json
{
  "event_type": "approval_changed",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:27:00Z",
  "sequence_num": 51,
  "actor_id": "0191a8b2-...",
  "data": {
    "approval_id": "0191a9d0-...",
    "node_id": "0191a9c3-...",
    "old_status": "pending",
    "new_status": "approved",
    "rule_id": null
  }
}
```

Status values per SPEC-DM-03 §3.2: `pending`, `approved`, `denied`, `expired`.

#### 5.2.7 `user_joined`

Emitted when a user or profile joins a tree (per SPEC-DM-04 §3.3 tree_members table).

```json
{
  "event_type": "user_joined",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:28:00Z",
  "sequence_num": 52,
  "actor_id": "0191a8b2-...",
  "data": {
    "user_id": "0191a9d1-...",
    "profile_id": null,
    "role": "member"
  }
}
```

`profile_id`: non-null when a Hermes profile (not a human user) joins. Role values per SPEC-DM-04 §3.3: `owner`, `admin`, `member`, `viewer`.

#### 5.2.8 `user_left`

Emitted when a user or profile is removed from a tree.

```json
{
  "event_type": "user_left",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:29:00Z",
  "sequence_num": 53,
  "actor_id": "0191a8b2-...",
  "data": {
    "user_id": "0191a9d1-...",
    "profile_id": null,
    "previous_role": "member"
  }
}
```

#### 5.2.9 `tree_snapshot`

Emitted periodically as a sync anchor, and on initial connection when no `since` hash is provided.

```json
{
  "event_type": "tree_snapshot",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:30:00Z",
  "sequence_num": 54,
  "actor_id": "00000000-0000-0000-0000-000000000000",
  "data": {
    "snapshot": {
      "id": "0191a9d2-...",
      "tree_id": "0191a8b2-...",
      "snapshot_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "node_count": 47,
      "edge_count": 46,
      "created_at": "2026-07-20T20:30:00Z"
    }
  }
}
```

Full TreeSnapshot shape from SPEC-DM-02 §3. Snapshot hash is SHA256 of the canonical tree representation (SPEC-DM-02 §5).

Snapshots are emitted:
- On initial connection (if no `since` hash or hash not found)
- Every 5 minutes as a sync anchor
- After a batch of 100+ events (to give clients a catch-up point)

#### 5.2.10 `heartbeat`

Emitted every 30 seconds of silence (no other events).

```json
{
  "event_type": "heartbeat",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:30:30Z",
  "sequence_num": 0,
  "actor_id": "00000000-0000-0000-0000-000000000000",
  "data": {
    "sequence": 12
  }
}
```

`sequence` is a monotonic heartbeat counter (resets on reconnect). `sequence_num` is 0 for heartbeats (they don't increment the event log). `actor_id` is the zero UUID.

#### 5.2.11 `error`

Emitted when a server-side error occurs that doesn't terminate the connection.

```json
{
  "event_type": "error",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:31:00Z",
  "sequence_num": 0,
  "actor_id": "00000000-0000-0000-0000-000000000000",
  "data": {
    "error": "Event buffer overflow — some events may be missing",
    "code": "EVENT_BUFFER_OVERFLOW"
  }
}
```

#### 5.2.12 `done`

Emitted on graceful server shutdown. The server sends this before closing the connection.

```json
{
  "event_type": "done",
  "tree_id": "0191a8b2-...",
  "timestamp": "2026-07-20T20:32:00Z",
  "sequence_num": 0,
  "actor_id": "00000000-0000-0000-0000-000000000000",
  "data": {
    "reason": "server_shutdown",
    "message": "canopyd is shutting down for maintenance"
  }
}
```

`reason` values: `server_shutdown`, `tree_deleted`, `user_removed`, `token_expired`.

---

## 6. Reconnection Behavior

### 6.1 Last-Event-ID

The browser's EventSource API automatically sends the `Last-Event-ID` HTTP header on reconnection. The value is the `id` field from the last received event.

```
Last-Event-ID: 0191a8b2-7fff-...:000047
```

Server behavior:
1. Parse `tree_id` and `sequence_num` from the ID.
2. Query the event log: `SELECT * FROM events WHERE tree_id = $1 AND sequence_num > $2 ORDER BY sequence_num LIMIT 1000`.
3. Stream missed events in order.
4. If the gap is >1000 events → send a `tree_snapshot` event with the current state, then continue with subsequent events.

### 6.2 Since Hash

When the `?since=<hash>` query parameter is provided:
1. Look up the snapshot by hash in `tree_snapshots` table (SPEC-DM-02 §3).
2. If not found → send current `tree_snapshot` event (full sync).
3. If found → compute delta between that snapshot and current state (SPEC-DM-02 §6 `ComputeDelta`).
4. Stream delta as events: `node_added` for new nodes, `node_updated` for changed nodes, `node_removed` for deleted nodes, `edge_added`/`edge_removed` for edge changes.
5. After delta, switch to live event streaming.

### 6.3 Replay Window

| Parameter | Value |
|-----------|-------|
| Max replay events | 1,000 |
| Max replay time window | 1 hour |
| Behavior on overflow | Send full `tree_snapshot` event, then live events |
| Event log retention | 1 hour of events kept in memory ring buffer |

### 6.4 Retry Field

The `retry:` field in the SSE stream controls the browser's reconnection backoff:

```
retry: 3000
```

- Default: 3000ms (3 seconds)
- Under load (>5000 concurrent connections): dynamically increases to 5000ms
- The browser uses this value as a base for exponential backoff

---

## 7. Heartbeat & Timeout

| Parameter | Value | Notes |
|-----------|-------|-------|
| Heartbeat interval | 30 seconds | Reset on every real event |
| Client read timeout | 90 seconds | No event received in 90s → client should reconnect |
| Server-side keepalive | Write a heartbeat event every 30s | Keeps proxies/load balancers from closing idle connections |
| Max events per connection | Unlimited | Stream until client disconnects |
| Graceful shutdown drain | 5 seconds | Server sends `done` event, waits 5s for clients to receive, then closes |

### 7.1 Client Timeout Handling

If the client receives no event for 90 seconds:
1. EventSource fires `onerror`.
2. Client closes the connection.
3. Client reconnects with `Last-Event-ID` set to the last received event ID.
4. Server replays missed events (§6).

### 7.2 Server-Side Timeout

If the server hasn't written to a connection in 25 seconds (5 seconds before client timeout):
1. Server writes a heartbeat event.
2. Timer resets.

---

## 8. Authentication & Authorization

### 8.1 Connection-Time Validation

Auth is validated ONCE at connection time, before the first SSE event is sent.

```
Request Flow:
Client → GET /trees/{tree_id}/events + Authorization: Bearer <token>
  → Server validates JWT
  → Server checks tree membership
  → Server sets SSE headers (200 OK)
  → Server begins streaming events
```

### 8.2 Error Responses (Pre-SSE)

All auth errors return standard HTTP JSON responses BEFORE SSE headers are set:

| HTTP Status | Error Code | JSON Response | Condition |
|-------------|------------|---------------|-----------|
| 401 | TOKEN_MISSING | `{"error": "Authorization header required", "code": "TOKEN_MISSING"}` | No Bearer token |
| 401 | TOKEN_INVALID | `{"error": "Invalid or malformed token", "code": "TOKEN_INVALID"}` | JWT parse failure |
| 401 | TOKEN_EXPIRED | `{"error": "Token has expired", "code": "TOKEN_EXPIRED"}` | JWT `exp` claim in the past |
| 403 | NOT_TREE_MEMBER | `{"error": "You are not a member of this tree", "code": "NOT_TREE_MEMBER"}` | User not in `tree_members` |
| 404 | TREE_NOT_FOUND | `{"error": "Tree not found", "code": "TREE_NOT_FOUND"}` | tree_id not in `trees` table |
| 429 | TOO_MANY_CONNECTIONS | `{"error": "Too many SSE connections", "code": "TOO_MANY_CONNECTIONS", "retry_after": 30}` | User has >10 connections or tree has >100 |

### 8.3 Mid-Stream Token Expiry

If the JWT expires DURING an active SSE connection:
- The connection is NOT terminated.
- The client was authorized at connection time.
- The next reconnection will require a fresh token.

---

## 9. Go Interfaces

### 9.1 SSEHub

```go
package sse

import (
    "context"
    "encoding/json"
    "time"

    "github.com/google/uuid"
)

// SSEHub manages SSE connections per tree. Thread-safe.
type SSEHub interface {
    // Subscribe registers a client for tree events.
    // Returns an error if the client cannot be subscribed (e.g., tree has 100+ connections).
    Subscribe(ctx context.Context, treeID uuid.UUID, client SSEClient) error

    // Unsubscribe removes a client from a tree.
    // Idempotent — safe to call if client is already unsubscribed.
    Unsubscribe(treeID uuid.UUID, clientID string)

    // Broadcast sends an event to all subscribers of a tree.
    // Slow clients (full buffer) are disconnected and unsubscribed automatically.
    Broadcast(treeID uuid.UUID, event SSEEvent)

    // ReplaySince sends events since a given event ID to a specific client.
    // Used on reconnection. If the gap exceeds the replay window, sends
    // a tree_snapshot event instead.
    ReplaySince(ctx context.Context, treeID uuid.UUID, clientID string, sinceEventID string) error

    // SubscriberCount returns the number of active subscribers for a tree.
    SubscriberCount(treeID uuid.UUID) int

    // TotalConnections returns the total number of active SSE connections.
    TotalConnections() int

    // Shutdown gracefully closes all connections.
    // Sends "done" event to every client, waits drainTimeout, then closes.
    Shutdown(ctx context.Context) error
}

// SSEClient represents a single SSE connection.
type SSEClient interface {
    // ID returns the client's unique identifier.
    ID() string

    // Send writes an SSE-formatted event to the client.
    // Must be safe for concurrent use (SSEHub may broadcast from multiple goroutines).
    Send(event SSEEvent) error

    // SendRaw writes a raw SSE-formatted string to the client.
    // Used for heartbeats and system events that don't go through the event log.
    SendRaw(raw string) error

    // LastEventID returns the ID of the last event sent to this client.
    LastEventID() string

    // Close closes the client connection.
    Close() error

    // Done returns a channel that is closed when the client disconnects.
    Done() <-chan struct{}

    // TreeID returns the tree this client is subscribed to.
    TreeID() uuid.UUID

    // UserID returns the authenticated user ID for this client.
    UserID() uuid.UUID
}

// SSEEvent represents a single event in the event log.
type SSEEvent struct {
    ID           string          // "tree_id:sequence_number" format
    Type         string          // Event type: "node_added", "edge_added", etc.
    Data         json.RawMessage // JSON payload
    Timestamp    time.Time
    TreeID       uuid.UUID
    SequenceNum  int64
    ActorID      uuid.UUID
}
```

### 9.2 SSEEventLog

```go
// SSEEventLog maintains a ring buffer of recent events for replay.
type SSEEventLog interface {
    // Append adds an event to the log and assigns a sequence number.
    Append(treeID uuid.UUID, eventType string, data json.RawMessage, actorID uuid.UUID) SSEEvent

    // Since returns events with sequence_num > since for the given tree.
    // Capped at maxEvents. If exceeded, returns the cap and sets truncated=true.
    Since(treeID uuid.UUID, sinceSeqNum int64, maxEvents int) (events []SSEEvent, truncated bool, err error)

    // SinceTime returns events with timestamp > since for the given tree.
    SinceTime(treeID uuid.UUID, since time.Time, maxEvents int) (events []SSEEvent, truncated bool, err error)

    // Prune removes events older than the retention period.
    Prune(retention time.Duration) int
}
```

### 9.3 HTTP Handler Signature

```go
// handleTreeEvents is the HTTP handler for GET /trees/{tree_id}/events.
//
// Middleware chain (applied before this handler):
//   HTTP Request → CORS → Auth (JWT validation) → Tree Membership → Rate Limiting → handleTreeEvents
//
// The auth middleware injects the authenticated user into the request context.
// The tree membership middleware verifies the user is a member of the requested tree.
func (s *Server) handleTreeEvents(w http.ResponseWriter, r *http.Request) {
    // 1. tree_id already validated by router path parsing
    treeID := chi.URLParam(r, "tree_id")

    // 2. UserID from auth middleware context
    userID := r.Context().Value(auth.UserIDKey).(uuid.UUID)

    // 3. Parse query params
    sinceHash := r.URL.Query().Get("since")
    profilesCSV := r.URL.Query().Get("profiles")
    includeHeartbeat := r.URL.Query().Get("include_heartbeat") != "false"

    // 4. Validate params (return HTTP 400 for invalid params — before SSE headers)
    // ...

    // 5. Check connection limits
    if s.sseHub.SubscriberCount(treeID) >= 100 {
        s.writeJSON(w, http.StatusTooManyRequests, ErrorResponse{...})
        return
    }
    if s.sseHub.TotalConnections() >= 10000 {
        s.writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{...})
        return
    }

    // 6. Flusher check — SSE requires http.Flusher
    flusher, ok := w.(http.Flusher)
    if !ok {
        s.logger.Error("streaming not supported")
        s.writeJSON(w, http.StatusInternalServerError, ErrorResponse{...})
        return
    }

    // 7. Set SSE headers
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no")
    w.WriteHeader(http.StatusOK)
    flusher.Flush()

    // 8. Create client, subscribe to hub
    client := newSSEClient(userID, treeID, w, flusher)
    if err := s.sseHub.Subscribe(r.Context(), treeID, client); err != nil {
        // Subscription failed — send error event then close
        client.SendRaw(formatSSEError("SUBSCRIPTION_FAILED", err.Error()))
        return
    }
    defer s.sseHub.Unsubscribe(treeID, client.ID())

    // 9. Replay missed events
    if sinceHash != "" {
        if err := s.replaySinceHash(r.Context(), treeID, client, sinceHash); err != nil {
            s.logger.Error("replay failed", "error", err)
        }
    } else if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
        if err := s.sseHub.ReplaySince(r.Context(), treeID, client.ID(), lastEventID); err != nil {
            s.logger.Error("replay failed", "error", err)
        }
    }

    // 10. Heartbeat ticker
    var heartbeatCh <-chan time.Time
    if includeHeartbeat {
        ticker := time.NewTicker(30 * time.Second)
        defer ticker.Stop()
        heartbeatCh = ticker.C
    }

    // 11. Event loop — blocks until client disconnects
    for {
        select {
        case <-r.Context().Done():
            // Client disconnected
            return
        case <-heartbeatCh:
            if err := client.SendRaw(formatSSEComment("heartbeat")); err != nil {
                return
            }
            flusher.Flush()
        }
    }
}
```

---

## 10. Error Catalog

Every error case with exact HTTP behavior:

| Error Code | HTTP Status | SSE Event | Condition |
|------------|-------------|-----------|-----------|
| TOKEN_MISSING | 401 | No | No `Authorization: Bearer` header |
| TOKEN_INVALID | 401 | No | JWT parse/malformed |
| TOKEN_EXPIRED | 401 | No | JWT `exp` claim < now |
| NOT_TREE_MEMBER | 403 | No | User not in `tree_members` table |
| TREE_NOT_FOUND | 404 | No | tree_id not in `trees` table |
| TOO_MANY_CONNECTIONS_USER | 429 | No | User has >10 active SSE connections |
| TOO_MANY_CONNECTIONS_TREE | 429 | No | Tree has >100 active SSE connections |
| INVALID_SINCE_HASH | 400 | No | `?since=` value not 64-char hex |
| INVALID_PROFILE_ID | 400 | No | `?profiles=` has non-UUID value |
| STREAMING_NOT_SUPPORTED | 500 | No | `http.Flusher` not available (should never happen with HTTP/1.1+) |
| SUBSCRIPTION_FAILED | 500 | Yes (then close) | Internal error during client subscription |
| EVENT_BUFFER_OVERFLOW | N/A | Yes (warning) | Client fell >1000 events behind; snapshot sent |
| SERVER_SHUTDOWN | N/A | Yes (`done`) | Graceful server shutdown |

### 10.1 Pre-SSE Error Response Format

```json
{
  "error": "Human-readable description",
  "code": "ERROR_CODE",
  "details": {}
}
```

`details` is optional and error-specific. Example for `TOO_MANY_CONNECTIONS_USER`:
```json
{
  "error": "Too many concurrent SSE connections",
  "code": "TOO_MANY_CONNECTIONS_USER",
  "details": {
    "current_connections": 11,
    "max_connections": 10,
    "retry_after_seconds": 30
  }
}
```

### 10.2 In-Stream Error Event Format

When an error doesn't terminate the connection, it's sent as an SSE event:

```
event: error
data: {"event_type":"error","tree_id":"...","timestamp":"...","sequence_num":0,"actor_id":"00000000-0000-0000-0000-000000000000","data":{"error":"...","code":"..."}}

```

---

## 11. Performance & Limits

| Parameter | Value | Notes |
|-----------|-------|-------|
| Max concurrent SSE connections (server) | 10,000 | Per canopyd instance |
| Max connections per user | 10 | Enforced by user_id rate limiter |
| Max connections per tree | 100 | Enforced at subscribe time |
| Event buffer per client (ring buffer) | 1,000 events | If client falls behind >1000, send snapshot + current |
| Memory per connection (idle) | <1 KB | Excluding ring buffer |
| Ring buffer memory per connection | ~100 KB | 1,000 events × ~100 bytes each |
| Broadcast latency (p99) | <100 ms | From POST handler return to SSE client receive |
| Heartbeat interval | 30 seconds | Reset on every real event |
| Snapshot interval | 5 minutes | Periodic sync anchor |
| Event log retention (in-memory) | 1 hour | Events older than 1 hour pruned |
| Max SSE connections per browser (HTTP/1.1) | 6 | Browser limit; use HTTP/2 to exceed |
| HTTP/2 max streams per connection | 100 | Per RFC 7540 |

---

## 12. Middleware Chain

The SSE handler runs after these middleware, in order:

```
HTTP Request
  → CORS middleware (handle preflight, set headers)
  → Auth middleware (validate JWT, inject UserID into context, reject 401)
  → Tree Membership middleware (check user is in tree_members, reject 403)
  → Rate Limiting middleware (check connection limits, reject 429)
  → SSE Handler (handleTreeEvents)
```

Important ordering constraint: Auth and membership checks MUST run BEFORE SSE headers are set. This ensures error responses are standard HTTP JSON, not SSE events. The first `w.WriteHeader(200)` call (setting SSE headers) only happens after all auth gates pass.

---

## 13. Data Model References

The SSE endpoint reads from these tables (defined in Phase 2 specs):

| Table | Spec | Read Purpose |
|-------|------|-------------|
| `trees` | SPEC-DM-01 §3.1 | Validate tree_id exists |
| `nodes` | SPEC-DM-01 §3.3 | Replay missed nodes, build snapshot |
| `edges` | SPEC-DM-01 §3.4 | Replay missed edges |
| `tree_snapshots` | SPEC-DM-02 §3 | Compute deltas from since hash |
| `approvals` | SPEC-DM-03 §3.1 | Broadcast approval status changes |
| `tree_members` | SPEC-DM-04 §3.3 | Validate user membership at connection time |
| `profiles` | SPEC-DM-04 §3.1 | Resolve profile_id for user_joined events |
| `users` | SPEC-DM-04 §3.1 | Resolve user_id from JWT claims |

The SSE endpoint does NOT write to any table. All writes happen through the REST endpoints (SPEC-API-02, SPEC-API-03, SPEC-API-05). The SSE hub is notified after a successful write via an internal Go channel or pub/sub.

---

## 14. Sequence Diagram

```
┌─────────┐     ┌──────────┐     ┌─────────┐     ┌──────────┐
│ Browser │     │ canopyd  │     │ SSE Hub │     │ Postgres │
└────┬────┘     └────┬─────┘     └────┬────┘     └────┬─────┘
     │               │               │               │
     │  GET /trees/X/events          │               │
     │  Authorization: Bearer <jwt>  │               │
     │──────────────>│               │               │
     │               │ validate JWT  │               │
     │               │──────────────>│               │
     │               │               │               │
     │               │ check tree membership         │
     │               │──────────────────────────────>│
     │               │<──────────────────────────────│
     │               │               │               │
     │               │ Subscribe(client)             │
     │               │──────────────>│               │
     │               │               │               │
     │  200 OK       │               │               │
     │  Content-Type: text/event-stream              │
     │<──────────────│               │               │
     │               │               │               │
     │  event: tree_snapshot         │               │
     │  data: {snapshot}             │               │
     │<──────────────│               │               │
     │               │               │               │
     │  event: heartbeat (every 30s) │               │
     │<──────────────│               │               │
     │               │               │               │
     │  POST /trees/X/nodes          │               │
     │──────────────>│               │               │
     │               │ INSERT node   │               │
     │               │──────────────────────────────>│
     │               │<──────────────────────────────│
     │               │               │               │
     │               │ Broadcast(node_added)         │
     │               │──────────────>│               │
     │               │               │               │
     │  event: node_added            │               │
     │  data: {node}                 │               │
     │<──────────────│               │               │
     │               │               │               │
     │  ── disconnect ──             │               │
     │               │               │               │
     │  GET /trees/X/events          │               │
     │  Last-Event-ID: X:000047      │               │
     │──────────────>│               │               │
     │               │ ReplaySince(X, "X:000047")    │
     │               │──────────────>│               │
     │               │               │ query events  │
     │               │               │──────────────>│
     │               │               │<──────────────│
     │               │               │               │
     │  event: node_added (replayed) │               │
     │  event: node_updated          │               │
     │  ... (all missed events)      │               │
     │<──────────────│               │               │
     │               │               │               │
     │  ── server shutdown ──        │               │
     │               │ Shutdown()    │               │
     │               │──────────────>│               │
     │               │               │               │
     │  event: done  │               │               │
     │  data: {reason: server_shutdown}              │
     │<──────────────│               │               │
     │               │               │               │
     │  connection closed            │               │
```

---

## 15. Edge Cases

| # | Scenario | Behavior |
|---|----------|----------|
| 1 | Client connects with invalid `since` hash | HTTP 400, connection closed |
| 2 | Client connects to non-existent tree | HTTP 404, connection closed |
| 3 | Client connects with valid token but not a member | HTTP 403, connection closed |
| 4 | Client exceeds 10-connection limit | HTTP 429 with `Retry-After: 30` |
| 5 | Tree exceeds 100-connection limit | HTTP 429 with `Retry-After: 10` |
| 6 | Client disconnects mid-stream | Goroutine exits on `r.Context().Done()`, unsubscribe called via defer |
| 7 | Event ID overflow (>2^63) | Sequence number wraps — use UUIDv7 timestamp fallback |
| 8 | Tree deleted while clients connected | Server sends `done` event with `reason: tree_deleted`, then closes |
| 9 | User removed from tree while connected | Server sends `done` event with `reason: user_removed`, then closes |
| 10 | Token expires mid-connection | Connection stays open. Next reconnect requires fresh token |
| 11 | Server runs out of file descriptors | New connections get HTTP 503. Existing connections unaffected |
| 12 | Client sends request body | Body is ignored. SSE is unidirectional from server |
| 13 | Client reconnects after >1 hour gap | Event log pruned — server sends `tree_snapshot` event (full sync) |
| 14 | `?profiles=` filter with no matching profiles | Stream is empty except heartbeats. No error. |
| 15 | `?include_heartbeat=false` | No heartbeat events sent. If no other events for >90s, client will timeout |
| 16 | Two nodes created simultaneously | Each gets own sequence_num via DB trigger. Broadcast in sequence_num order |
| 17 | Node deleted → cascade removes edges | `node_removed` event includes `cascade_removed_edge_ids`. No separate `edge_removed` events for cascaded edges |
| 18 | Profiles filter with deleted profile UUID | UUID validated against SPEC-DM-04 profiles table. If deleted, filter matches nothing |
| 19 | Client sends `Last-Event-ID` for different tree | Parsed tree_id from event ID is ignored — replay is scoped to current tree_id from URL |
| 20 | HTTP/1.1 client tries >6 connections | Browser enforces, not server. Server accepts all connections |

---

## 16. Test Scenarios

These are test NAMES with scenarios — not implementation. The worker writes actual Go test code.

### 16.1 SSEHub Unit Tests

1. `TestSSEHub_Subscribe` — client subscribes, appears in subscriber count
2. `TestSSEHub_Subscribe_TreeLimit` — 101st subscriber for a tree returns error
3. `TestSSEHub_Unsubscribe` — client unsubscribes, subscriber count decrements
4. `TestSSEHub_Unsubscribe_Idempotent` — unsubscribing twice is safe
5. `TestSSEHub_Broadcast` — event broadcast reaches all subscribed clients
6. `TestSSEHub_Broadcast_NoSubscribers` — broadcast with zero subscribers is a no-op
7. `TestSSEHub_Broadcast_SlowClient` — client with full buffer is disconnected and unsubscribed
8. `TestSSEHub_ReplaySince` — replay missed events since a given event ID
9. `TestSSEHub_ReplaySince_Truncated` — gap >1000 events triggers snapshot
10. `TestSSEHub_ReplaySince_EmptyLog` — replay when event log is empty returns nothing
11. `TestSSEHub_Shutdown` — shutdown sends `done` to all clients, drains within timeout
12. `TestSSEHub_Shutdown_Timeout` — client that doesn't disconnect within drain timeout is force-closed

### 16.2 HTTP Handler Integration Tests

13. `TestHandleTreeEvents_Connect` — client connects, receives initial snapshot or heartbeat
14. `TestHandleTreeEvents_Heartbeat` — client receives heartbeat events every 30s (use shorter interval in test)
15. `TestHandleTreeEvents_SinceHash` — client connects with `?since=<valid_hash>`, receives only delta
16. `TestHandleTreeEvents_SinceHash_NotFound` — client connects with unknown hash, receives full snapshot
17. `TestHandleTreeEvents_Reconnect` — client disconnects, reconnects with Last-Event-ID, receives missed events
18. `TestHandleTreeEvents_NoToken` — no Authorization header, receives 401
19. `TestHandleTreeEvents_InvalidToken` — malformed JWT, receives 401
20. `TestHandleTreeEvents_ExpiredToken` — expired JWT, receives 401
21. `TestHandleTreeEvents_NotMember` — valid token but not a tree member, receives 403
22. `TestHandleTreeEvents_TreeNotFound` — non-existent tree_id, receives 404
23. `TestHandleTreeEvents_TooManyConnections_User` — user has 11 connections, 11th receives 429
24. `TestHandleTreeEvents_TooManyConnections_Tree` — tree has 101 connections, 101st receives 429
25. `TestHandleTreeEvents_NodeCreated_Broadcast` — node created via REST, SSE client receives `node_added`
26. `TestHandleTreeEvents_NodeUpdated_Broadcast` — node updated, SSE client receives `node_updated`
27. `TestHandleTreeEvents_NodeDeleted_Broadcast` — node deleted, SSE client receives `node_removed` with cascade edge IDs
28. `TestHandleTreeEvents_EdgeCreated_Broadcast` — edge created, SSE client receives `edge_added`
29. `TestHandleTreeEvents_EdgeRemoved_Broadcast` — edge removed, SSE client receives `edge_removed`
30. `TestHandleTreeEvents_ApprovalChanged_Broadcast` — approval status changes, SSE client receives `approval_changed`
31. `TestHandleTreeEvents_UserJoined_Broadcast` — user joins tree, SSE client receives `user_joined`
32. `TestHandleTreeEvents_UserLeft_Broadcast` — user leaves tree, SSE client receives `user_left`
33. `TestHandleTreeEvents_MultipleClients` — two SSE clients subscribe, both receive broadcast events
34. `TestHandleTreeEvents_ProfilesFilter` — `?profiles=A,B` only delivers events from profiles A and B
35. `TestHandleTreeEvents_ProfilesFilter_NoMatch` — `?profiles=X` where X has no events, stream is heartbeat-only
36. `TestHandleTreeEvents_IncludeHeartbeat_False` — `?include_heartbeat=false`, no heartbeat events
37. `TestHandleTreeEvents_EventOrdering` — events received in sequence_num order
38. `TestHandleTreeEvents_ServerShutdown` — graceful shutdown sends `done` event then closes
39. `TestHandleTreeEvents_TreeDeleted` — tree deleted mid-connection, sends `done: tree_deleted` then closes
40. `TestHandleTreeEvents_UserRemoved` — user removed from tree mid-connection, sends `done: user_removed` then closes

### 16.3 SSEEventLog Unit Tests

41. `TestSSEEventLog_Append` — append event, sequence number auto-increments
42. `TestSSEEventLog_Since` — query events since given sequence number
43. `TestSSEEventLog_Since_Truncated` — query exceeds maxEvents, returns truncated=true
44. `TestSSEEventLog_SinceTime` — query events since given timestamp
45. `TestSSEEventLog_Prune` — prune events older than retention, count of pruned events returned
46. `TestSSEEventLog_Prune_Empty` — prune on empty log is a no-op

---

## 17. Security Considerations

| Concern | Mitigation |
|---------|-----------|
| Token sent over plain HTTP | Production: HTTPS required. Local dev: localhost exempt. |
| Token logged in access logs | `Authorization` header redacted in logs. Query params redacted. |
| Token in URL query string | Tokens only in `Authorization` header, never in query params. |
| Event stream readable by unauthorized | Auth validated BEFORE SSE headers set. Unauthorized clients never receive event data. |
| Connection hijacking | TLS required in production. `Strict-Transport-Security` header set. |
| Rate limiting bypass | Per-user and per-tree connection limits enforced at subscribe time. |
| SQL injection | All database queries use parameterized `$1, $2` placeholders. No string concatenation. |
| Event injection via content | Event data is JSON-encoded. Content never contains raw SSE `event:` or `data:` fields. |
| Replay attack | JWT `exp` claim validated at connection time. Replay window bounded by token lifetime. |
| Information leak via heartbeat | Heartbeat events contain only a sequence counter. No tree data, no user data. |

---

## 18. Worker Implementation Checklist

Before marking this spec task complete, verify:

- [ ] `SSEHub` interface implemented with all methods (Subscribe, Unsubscribe, Broadcast, ReplaySince, SubscriberCount, TotalConnections, Shutdown)
- [ ] `SSEClient` interface implemented with all methods (ID, Send, SendRaw, LastEventID, Close, Done, TreeID, UserID)
- [ ] `SSEEventLog` ring buffer implemented with Append, Since, SinceTime, Prune
- [ ] HTTP handler `handleTreeEvents` wires all query params, auth, replay
- [ ] Event format matches spec: `id`, `event`, `data`, `retry` fields
- [ ] All 12 event types implemented with correct JSON shapes
- [ ] Heartbeat goroutine fires every 30 seconds
- [ ] Reconnection: Last-Event-ID replay, since hash delta, truncated snapshot fallback
- [ ] Connection limits: 10/user, 100/tree, 10000/server
- [ ] Graceful shutdown: `done` event, drain timeout, force close
- [ ] Auth: JWT validation, tree membership check, all error codes return correct HTTP status
- [ ] Error responses before SSE headers are JSON, not SSE events
- [ ] All 46 test scenarios have passing tests
- [ ] `go build ./... && go vet ./...` passes
- [ ] Benchmark: broadcast latency p99 < 100ms with 100 connected clients
