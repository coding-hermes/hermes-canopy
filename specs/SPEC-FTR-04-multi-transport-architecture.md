# SPEC-FTR-04 — Multi-Transport Architecture

> **Status:** Spec | **Phase:** Post-MVP (FTR-04) | **Blocks:** BE-09 (Transport Adapter Layer)
> **References:** T1.8-Multi-Transport-Architecture, SPEC-API-01, SPEC-FTR-02, ARCHITECTURE.md §2.1, AGENTS.md §Architecture
> **Commit:** _(to be filled)_

---

## 1. Purpose

Define the exact implementation contract for Canopy's transport adapter layer. The sync engine must operate identically across seven deployment modes — local loopback, LAN, self-hosted behind NAT, SaaS cloud, P2P mesh, federated multi-server, and air-gapped — without knowing which transport carries its messages. A Go worker reading this document implements `TransportAdapter`, all five adapter implementations, `ConnectionManager`, `TransportSelector`, bandwidth adaptation, degradation/fallback, PostgreSQL persistence, SSE event emission, and HTTP endpoints without making transport-design decisions.

The design is protocol-agnostic: the sync engine composes `Message` structs with 13 tree-sync opcodes (defined in T1.8 §4) and hands them to a `TransportAdapter` interface. Whether that adapter writes to an HTTP/2 SSE stream, a WebRTC DataChannel, a NATS JetStream subject, a Redis Stream, or a binary TCP relay frame is invisible above the transport boundary. MLS encryption (SPEC-FTR-03) sits above this layer; the transport sees only opaque ciphertext plus routing metadata.

---

## 2. Design Decisions

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | Opcode model | 13 transport-agnostic tree-sync opcodes (0x01–0x0D); same `Message` struct over all transports | T1.8 §4 defines the complete opcode set. Self-describing messages let the sync engine work without transport-specific code paths. A single `Message` wire format reduces implementation surface and enables interoperable mixed-transport deployments. |
| 2 | Adapter pattern | Single Go `TransportAdapter` interface with 5 methods: `Connect`, `Send`, `Receive`, `Disconnect`, `Health` | One interface avoids combinatorial explosion. Every transport implements the same contract. The sync engine and `ConnectionManager` import only `package transport` — never `nats.go`, `go-redis`, or `pion/webrtc`. |
| 3 | Transport selection | `TransportSelector` driven by deployment mode + network topology, with hard-coded priority matrix per mode | T1.8 §6.2 defines the full 7-mode priority matrix. Modes are a startup-time enum, not a runtime negotiation. Selection is deterministic and auditable. |
| 4 | Degradation and fallback | Ordered fallback chain per deployment mode; three `Health()` failures in 60s mark a transport degraded; `Send()` returning `ErrConnectionClosed` triggers immediate failover | The fallback chain is the only mechanism that prevents connection loss from becoming message loss. Degradation is measured, not guessed. Immediate failover on `ErrConnectionClosed` avoids waiting for a health-check window when the transport already told us it's dead. |
| 5 | Per-transport defaults | SSE: 1MB max message, 15s heartbeat. NATS: 1MB max payload, JetStream with 24h retention. Redis: 1MB soft cap, Streams with ~100K `MAXLEN`. WebRTC: 256KB per SCTP message, 30s ICE timeout. Relay: 1MB max frame, 30s heartbeat | T1.8 §3 specifies each transport's operational parameters. These are consistent defaults — every value is overridable through `ConnectOptions` and server config, but the defaults cover all MVP and post-MVP deployment modes. |
| 6 | Encryption boundary | MLS (SPEC-FTR-03) encrypts `Message.Payload` above the transport layer; transports add channel encryption (TLS 1.3, DTLS-SRTP) and authentication (JWT, HMAC, TLS certs) | T1.7 and T1.8 §7.3 establish this layering: MLS provides end-to-end content confidentiality; transport provides point-to-point channel confidentiality. The transport never sees plaintext node content. |
| 7 | SSE as primary transport | SSE (HTTP/2, server→client) is the default for all HTTP-accessible modes (local, LAN, self-hosted, SaaS) | T1.1 §5 established SSE over HTTP/2 as the primary architecture. Built-in browser `EventSource` reconnection with `Last-Event-ID` eliminates custom reconnection logic. HTTP/2 multiplexing removes the 6-connection limit of HTTP/1.1. |
| 8 | WebRTC for P2P | `pion/webrtc` (Go, no CGo) with SSE-backed signaling, STUN, and TURN | P2P and LAN modes require direct peer-to-peer data paths. WebRTC is the only browser-standard P2P protocol with built-in NAT traversal. `pion` compiles into `canopyd` without CGo, keeping the single-binary deployment goal. Signaling reuses the existing SSE transport — no separate signaling server. |
| 9 | NATS for backend pub/sub | `nats.go` client with JetStream for offline queuing; per-tree subjects (`canopy.tree.<treeID>.events`); not exposed to browsers | SaaS and federated modes need a server-to-server message fabric. NATS provides at-least-once delivery, subject-based routing, and JetStream persistence — all without the operational complexity of Kafka. Browsers never speak NATS directly; `canopyd` bridges SSE ↔ NATS. |
| 10 | Redis Streams for Redis-native teams | `go-redis` client with Consumer Groups (`XREADGROUP`) for offline replay; per-tree stream keys (`canopy:tree:<treeID>:stream`) | Many self-hosted and SaaS teams already operate Redis for caching/sessions. Redis Streams (5.0+) provides equivalent pub/sub + persistent queue semantics to NATS JetStream. Offering Redis as a first-class alternative avoids forcing a second infrastructure component. |
| 11 | Custom relay for air-gapped | Binary wire protocol over TCP or QUIC: 46-byte frame header + HMAC-SHA256; CBOR-encoded payload; 500-session limit per relay node | Air-gapped deployments have no cloud services, no STUN/TURN, and possibly no TLS PKI. The custom relay is the only transport available. A compact binary protocol with pre-shared HMAC keys and optional TLS provides security without infrastructure dependencies. |
| 12 | Bandwidth adaptation | 10-second sliding window measuring bytes/sec, latency, and packet loss; three tiers: >1MB/s (full payload), 100KB/s–1MB/s (content trimmed to 4KB), <100KB/s (IDs + descriptions only; content fetched on-demand) | T1.8 §5.3 defines bandwidth tiers. The sync engine must adapt to constrained links (mobile, satellite, mesh radio) without dropping messages. Content trimming and on-demand fetch keep the sync protocol working when bandwidth collapses. |
| 13 | Connection pool limits | Per-peer: 3 connections max (primary + fallback + signaling). SSE: 10,000 total. WebRTC: 100 total. NATS: 1 shared connection. Redis: 1 shared connection pool. Relay: 500 sessions per node | T1.8 §5.4 enumerates limits. These prevent resource exhaustion: file descriptors for SSE, browser peer-connection limits for WebRTC, NATS/Redis connection overhead for shared transports, and TCP socket limits for relay. |
| 14 | Shutdown sequence | `Disconnect()` → flush message queue → close receive channel → remove from `ConnectionManager` → mark `StateClosed` | Idempotent `Disconnect()` (calling on `StateClosed` returns nil) simplifies error-recovery paths. The receive channel close signals downstream consumers. The queue flush attempts best-effort delivery of buffered messages before the connection is torn down. |
| 15 | Health checking | `Health()` pings the transport backend: SSE hits the endpoint, WebRTC checks ICE connectivity, NATS pings the server, Redis issues `PING`, relay does a TCP health check. Three failures in 60s → degraded | Per-transport health semantics are defined in T1.8 §2.1. A unified `Health()` method lets `ConnectionManager` poll all transports with the same interface and trigger degradation uniformly. |
| 16 | Reconnection backoff | Exponential backoff: 1s, 2s, 4s, 8s, 16s, 32s (cap); reset on successful `Connect()`; jitter ±25% | Standard exponential backoff prevents thundering-herd reconnection storms when a transport backend restarts. Jitter distributes reconnection attempts across peers. The 32s cap ensures responsiveness after prolonged outages. |
| 17 | Sequence gap recovery | Gap detected when `seq > lastSeq + 1`; receiver sends `ACK(lastSeq)`; sender retransmits from `lastSeq+1`; 3 retransmission failures → fall back to `TREE_SNAPSHOT` | T1.8 §4.3 defines gap-detection rules. Sequence numbers are per-tree, monotonically increasing, server-authoritative. The 3-retry threshold bounds recovery latency while avoiding premature full re-sync for transient gaps. |
| 18 | Rate limiting | Per-connection: 1000 messages/sec inbound, 500 messages/sec outbound. Per-transport: configurable token-bucket rate limiter. Burst: 2× steady rate for 5 seconds | Rate limits prevent a misbehaving peer or compromised client from saturating a transport. Token-bucket design allows short bursts (legitimate batch operations) while capping sustained load. Limits are enforced at the `TransportAdapter.Send()` boundary. |
| 19 | Deployment mode mapping | 7 modes → transport priority matrix defined in T1.8 §6.2. Topology detected at startup: loopback check → mDNS scan → STUN query → public IP match → air-gapped fallback | Mode detection is a startup-time one-shot probe. The priority matrix is compiled into `TransportSelector` and does not change at runtime unless an administrator explicitly reconfigures the deployment mode. This prevents mode-flapping. |
| 20 | Maximum message size | Per-transport limits enforced at `Send()`: SSE 1MB, NATS 1MB (configurable to 8MB), Redis 1MB soft cap (512MB theoretical), WebRTC 256KB per SCTP message, Relay 1MB max frame. Exceeding limit → `ErrPayloadTooLarge` | T1.8 §2.2 `ConnectOptions.MaxMessageSize` lets peers negotiate lower limits. Per-transport hard caps prevent protocol-level truncation. `ErrPayloadTooLarge` is a recoverable error — the caller can split or trim the payload and retry. |

---

## 3. Go Interface Definitions

The following package is syntactically compilable Go. All types are defined in `package transport`. The sync engine and `ConnectionManager` import only this package; they never import transport-specific libraries.

### 3.1 TransportAdapter Interface and Core Types

```go
package transport

import (
    "context"
    "crypto/tls"
    "encoding/json"
    "errors"
    "sync"
    "time"
)

// TransportAdapter is the uniform interface for all sync transports.
// Implementations: SSEAdapter, WebRTCAdapter, NATSAdapter, RedisAdapter, RelayAdapter.
type TransportAdapter interface {
    Connect(ctx context.Context, opts ConnectOptions) (*Connection, error)
    Send(ctx context.Context, conn *Connection, msg *Message) error
    Receive(ctx context.Context, conn *Connection) (<-chan *Message, error)
    Disconnect(ctx context.Context, conn *Connection) error
    Health(ctx context.Context) error
}

// ConnectOptions configures a new transport connection.
type ConnectOptions struct {
    Target         string
    TransportType  TransportType
    Auth           AuthMaterial
    Metadata       map[string]string
    TLSConfig      *tls.Config
    Timeout        time.Duration
    MaxMessageSize int64
}

// Connection represents an active transport connection to a peer.
type Connection struct {
    ID                string
    TransportType     TransportType
    Peer              string
    Metadata          map[string]string
    State             ConnectionState
    EstablishedAt     time.Time
    LastActivity      time.Time
    SequenceWatermark uint64
}

// ConnectionState models the lifecycle of a transport connection.
type ConnectionState int

const (
    StateInit         ConnectionState = iota
    StateConnecting
    StateActive
    StateDegraded
    StateDisconnecting
    StateClosed
)

// TransportType enumerates all supported transports.
type TransportType string

const (
    TransportSSE    TransportType = "sse"
    TransportWebRTC TransportType = "webrtc"
    TransportNATS   TransportType = "nats"
    TransportRedis  TransportType = "redis"
    TransportRelay  TransportType = "relay"
)

// AuthMaterial carries transport-specific credentials.
type AuthMaterial struct {
    Token    string
    CertPEM  []byte
    KeyPEM   []byte
    HMACKey  []byte
    Username string
}
```

### 3.2 Message, Opcode, and Payload Types

```go
// Message is the universal sync message across all transports.
type Message struct {
    Opcode    Opcode          `json:"op" cbor:"0"`
    TreeID    string          `json:"tree" cbor:"1"`
    Sequence  uint64          `json:"seq" cbor:"2"`
    Timestamp int64           `json:"ts" cbor:"3"`
    Payload   json.RawMessage `json:"data" cbor:"4"`
    Origin    string          `json:"origin" cbor:"5"`
}

// Opcode enumerates all tree sync operations.
type Opcode uint8

const (
    OpTreeCreate     Opcode = 0x01
    OpNodeAdd        Opcode = 0x02
    OpNodeUpdate     Opcode = 0x03
    OpNodeDelete     Opcode = 0x04
    OpEdgeAdd        Opcode = 0x05
    OpEdgeRemove     Opcode = 0x06
    OpApprovalChange Opcode = 0x07
    OpUserJoin       Opcode = 0x08
    OpUserLeave      Opcode = 0x09
    OpTreeSnapshot   Opcode = 0x0A
    OpTreeDelta      Opcode = 0x0B
    OpHeartbeat      Opcode = 0x0C
    OpAck            Opcode = 0x0D
)
```

Full per-opcode payload types (`TreeCreatePayload`, `NodeAddPayload`, `NodeUpdatePayload`, `NodeDeletePayload`, `EdgeAddPayload`, `EdgeRemovePayload`, `ApprovalChangePayload`, `UserJoinPayload`, `UserLeavePayload`, `TreeSnapshotPayload`, `TreeDeltaPayload`, `HeartbeatPayload`, `AckPayload`) are defined in T1.8 §4.2 and are incorporated by reference. The worker copies those struct definitions verbatim into `package transport`.

### 3.3 Sentinel Errors

```go
var (
    ErrConnectionRefused    = errors.New("transport: connection refused by peer")
    ErrAuthFailed           = errors.New("transport: authentication failed")
    ErrAuthExpired          = errors.New("transport: credentials expired, rotation required")
    ErrTransportUnreachable = errors.New("transport: peer unreachable (DNS, network, timeout)")
    ErrTransportMismatch    = errors.New("transport: adapter type does not match requested transport")
    ErrConnectionClosed     = errors.New("transport: operation on closed connection")
    ErrSendTimeout          = errors.New("transport: send timed out")
    ErrPayloadTooLarge      = errors.New("transport: payload exceeds max message size")
    ErrSequenceGap          = errors.New("transport: gap detected in message sequence")
    ErrRateLimited          = errors.New("transport: rate limit exceeded")
)
```

### 3.4 ConnectionManager

```go
// ConnectionManager tracks all connections across all transports.
// It routes messages between the sync engine and the active transport for each peer.
type ConnectionManager struct {
    connections   map[string][]*Connection   // peer ID → active connections
    messageQueues map[string]*MessageQueue   // peer ID → offline buffer
    selector      *TransportSelector
    bandwidth     map[string]*BandwidthProfile
    rateLimiters  map[string]*RateLimiter
    mu            sync.RWMutex
}

// MessageQueue is a bounded ring buffer for offline message storage.
type MessageQueue struct {
    peerID   string
    buffer   []*Message
    head     int
    tail     int
    capacity int
    size     int
    mu       sync.Mutex
}

// BandwidthProfile captures measured throughput for a connection.
type BandwidthProfile struct {
    BytesPerSecond int64
    LatencyMs      int64
    PacketLoss     float64
}

// RateLimiter enforces per-connection message rate limits.
type RateLimiter struct {
    rate     float64
    burst    int
    tokens   float64
    lastTime time.Time
    mu       sync.Mutex
}

// ConnectionManager methods (exact signatures):
func (cm *ConnectionManager) RouteMessage(ctx context.Context, peerID string, msg *Message) error
func (cm *ConnectionManager) OnConnect(conn *Connection) error
func (cm *ConnectionManager) OnDisconnect(conn *Connection) error
func (cm *ConnectionManager) GetConnection(peerID string) (*Connection, error)
func (cm *ConnectionManager) DegradeTransport(ctx context.Context, tt TransportType) error
func (cm *ConnectionManager) MeasureBandwidth(peerID string) *BandwidthProfile
func (cm *ConnectionManager) EnforceRateLimit(peerID string) error
```

### 3.5 TransportSelector and Deployment Types

```go
// TransportSelector picks the best transport for a deployment mode.
type TransportSelector struct {
    mode      DeploymentMode
    topology  NetworkTopology
    available []TransportType
    fallbacks map[TransportType]TransportType
}

// DeploymentMode enumerates the seven Canopy deployment modes.
type DeploymentMode int

const (
    ModeLocal        DeploymentMode = iota
    ModeLAN
    ModeSelfHosted
    ModeSaaS
    ModeP2P
    ModeFederated
    ModeAirGapped
)

// NetworkTopology describes the node's network position.
type NetworkTopology int

const (
    TopologyLoopback  NetworkTopology = iota
    TopologyLAN
    TopologyNAT
    TopologyPublic
    TopologyAirGapped
)

// Transport capability flags negotiated at connection time.
const (
    CapBinary        = "binary"
    CapOrdered       = "ordered"
    CapReliable      = "reliable"
    CapBidirectional = "bidirectional"
    CapOfflineQueue  = "offline_queue"
    CapP2P           = "p2p"
)

// TransportSelector methods:
func (ts *TransportSelector) SelectPrimary(peerID string) TransportType
func (ts *TransportSelector) SelectFallback(current TransportType) (TransportType, error)
func (ts *TransportSelector) DetectTopology() NetworkTopology
func negotiateCapabilities(local, remote []string) []string
```

### 3.6 Connection Lifecycle State Machine

```
StateInit ──Connect()──▶ StateConnecting ──success──▶ StateActive
                            │                          │
                            │ (timeout/auth fail)      │ (latency spike, packet loss)
                            ▼                          ▼
                       StateClosed              StateDegraded
                                                     │
                                                     │ (recovery or timeout)
                                                     ▼
                                             StateActive / StateClosed

StateActive ──Disconnect()──▶ StateDisconnecting ──cleanup──▶ StateClosed
```

All transports implement this state machine. `Disconnect()` is idempotent — calling it on `StateClosed` returns `nil`. The `StateDegraded` state is set by `ConnectionManager` after three failed `Health()` checks; the transport may self-recover to `StateActive` if subsequent health checks pass.

---

## 4. DDL (PostgreSQL Tables)

Three tables persist transport connection state, per-transport configuration, and transport-level audit events. All timestamps are `TIMESTAMPTZ`.

### 4.1 transport_connections

```sql
CREATE TABLE transport_connections (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    peer_id         TEXT NOT NULL,
    transport_type  TEXT NOT NULL CHECK (transport_type IN ('sse', 'webrtc', 'nats', 'redis', 'relay')),
    state           TEXT NOT NULL DEFAULT 'init'
                        CHECK (state IN ('init', 'connecting', 'active', 'degraded', 'disconnecting', 'closed')),
    target          TEXT NOT NULL,
    established_at  TIMESTAMPTZ,
    last_activity   TIMESTAMPTZ NOT NULL DEFAULT now(),
    sequence_high   BIGINT NOT NULL DEFAULT 0,
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_transport_connections_peer ON transport_connections(peer_id, transport_type);
CREATE INDEX idx_transport_connections_state ON transport_connections(state)
    WHERE state IN ('active', 'degraded');
CREATE INDEX idx_transport_connections_transport ON transport_connections(transport_type);
```

### 4.2 transport_configs

```sql
CREATE TABLE transport_configs (
    transport_type   TEXT PRIMARY KEY CHECK (transport_type IN ('sse', 'webrtc', 'nats', 'redis', 'relay')),
    enabled          BOOLEAN NOT NULL DEFAULT true,
    max_message_size BIGINT NOT NULL,
    heartbeat_secs   INTEGER NOT NULL,
    connect_timeout  INTEGER NOT NULL DEFAULT 30,
    retry_max        INTEGER NOT NULL DEFAULT 3,
    config_json      JSONB NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Pre-seeded defaults:

| transport_type | max_message_size | heartbeat_secs | connect_timeout | retry_max |
|---------------|-----------------|----------------|-----------------|-----------|
| sse | 1048576 | 15 | 30 | 3 |
| webrtc | 262144 | 30 | 60 | 2 |
| nats | 1048576 | 30 | 30 | 3 |
| redis | 1048576 | 30 | 30 | 3 |
| relay | 1048576 | 30 | 30 | 3 |

### 4.3 transport_events

```sql
CREATE TABLE transport_events (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    connection_id   UUID REFERENCES transport_connections(id) ON DELETE SET NULL,
    transport_type  TEXT NOT NULL CHECK (transport_type IN ('sse', 'webrtc', 'nats', 'redis', 'relay')),
    event_type      TEXT NOT NULL CHECK (event_type IN (
                        'connected', 'disconnected', 'degraded', 'recovered',
                        'auth_failed', 'sequence_gap', 'rate_limited',
                        'fallback_activated', 'message_dropped', 'health_failed'
                    )),
    peer_id         TEXT,
    details         JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_transport_events_connection ON transport_events(connection_id, created_at);
CREATE INDEX idx_transport_events_type ON transport_events(event_type, created_at);
CREATE INDEX idx_transport_events_transport ON transport_events(transport_type, created_at);
```

`transport_events` is append-only. Rows older than 90 days are pruned by a scheduled cleanup job. The `details` JSONB column captures event-specific data: error messages for `auth_failed`, gap range for `sequence_gap`, fallback chain for `fallback_activated`.

---

## 5. SSE Events

Following the event-stream pattern defined in SPEC-API-01, the transport layer emits three event types over the authenticated SSE stream. All events carry a `workspace_id` (or `tree_id` in MVP) so the client can scope transport status to active collaboration surfaces.

### 5.1 transport_status

**Trigger:** Connection state transitions (`init` → `connecting` → `active` → `degraded` → `closed`). Also emitted on `TransportSelector` mode detection at startup.

**Event shape:**

```json
{
  "event": "transport_status",
  "data": {
    "transport_type": "sse",
    "state": "active",
    "peer_id": "0191a8b2-7fff-7000-9000-000000000099",
    "target": "https://canopy.example.com/trees/<id>/events",
    "established_at": "2026-07-22T12:00:00Z",
    "degraded": false,
    "degradation_reason": null
  }
}
```

`degradation_reason` is populated only when `state` is `degraded` or after a `degraded` → `closed` transition. Values: `"health_check_failed"`, `"send_timeout"`, `"connection_closed"`, `"rate_limited"`, `"bandwidth_threshold"`.

### 5.2 transport_error

**Trigger:** Non-recoverable transport errors: `ErrAuthFailed`, `ErrAuthExpired`, `ErrTransportMismatch`, `ErrSequenceGap` (after 3 retransmission failures). Also emitted when all transports in the fallback chain are exhausted.

**Event shape:**

```json
{
  "event": "transport_error",
  "data": {
    "transport_type": "webrtc",
    "error_code": "auth_failed",
    "error_message": "DTLS certificate fingerprint mismatch",
    "peer_id": "0191a8b2-7fff-7000-9000-000000000042",
    "fallback_available": true,
    "next_transport": "relay"
  }
}
```

`error_code` maps to a sentinel error: `"auth_failed"` (`ErrAuthFailed`), `"auth_expired"` (`ErrAuthExpired`), `"transport_mismatch"` (`ErrTransportMismatch`), `"connection_refused"` (`ErrConnectionRefused`), `"unreachable"` (`ErrTransportUnreachable`), `"sequence_gap"` (`ErrSequenceGap`), `"payload_too_large"` (`ErrPayloadTooLarge`), `"rate_limited"` (`ErrRateLimited`).

### 5.3 transport_degradation

**Trigger:** `ConnectionManager` marks a transport degraded (3 `Health()` failures in 60s) or recovers it. Also emitted on bandwidth-tier transitions (full → reduced → minimal, and recovery).

**Event shape:**

```json
{
  "event": "transport_degradation",
  "data": {
    "transport_type": "sse",
    "connection_id": "0191a8b2-7fff-7000-9000-000000000201",
    "peer_id": "0191a8b2-7fff-7000-9000-000000000099",
    "degraded": true,
    "reason": "bandwidth_threshold",
    "bandwidth_tier": "reduced",
    "bytes_per_second": 75000,
    "latency_ms": 320,
    "packet_loss": 0.03,
    "fallback_chain": ["webrtc", "nats", "redis", "relay"]
  }
}
```

`bandwidth_tier` values: `"full"`, `"reduced"`, `"minimal"`. `degraded: false` with a `reason` of `"bandwidth_recovery"` signals the client that full-payload delivery has resumed.

---

## 6. API Endpoints

All endpoints are prefixed with `/api/v1/transports/` and require `Authorization: Bearer <token>`. Transport configuration endpoints additionally require admin-level authorization per the workspace role model in SPEC-FTR-01.

### 6.1 Route Summary

```
GET    /api/v1/transports/status                        → All transport states for the current node
GET    /api/v1/transports/{type}                        → Single transport configuration + state
PUT    /api/v1/transports/{type}                        → Update transport configuration
DELETE /api/v1/transports/{type}                        → Disable a transport + close all its connections
GET    /health/transports/{type}                        → Lightweight health probe (no auth required)
```

### 6.2 GET /api/v1/transports/status

Returns the state of all configured transports for the current `canopyd` node. No request body.

```json
// Response 200
{
  "node_id": "0191a8b2-7fff-7000-9000-000000000001",
  "deployment_mode": "saas",
  "network_topology": "public",
  "transports": [
    {
      "type": "sse",
      "enabled": true,
      "state": "active",
      "connections": 42,
      "health_ok": true,
      "last_health_check": "2026-07-22T12:00:00Z"
    },
    {
      "type": "nats",
      "enabled": true,
      "state": "active",
      "connections": 1,
      "health_ok": true,
      "last_health_check": "2026-07-22T12:00:00Z"
    },
    {
      "type": "webrtc",
      "enabled": false,
      "state": "closed",
      "connections": 0,
      "health_ok": false,
      "last_health_check": null
    }
  ],
  "active_fallback_chains": {}
}
```

### 6.3 PUT /api/v1/transports/{type}

Update configuration for a single transport. Only mutable fields are accepted; immutable fields (`transport_type`, `created_at`) are ignored if present.

```json
// Request
{
  "enabled": true,
  "max_message_size": 2097152,
  "heartbeat_secs": 10,
  "connect_timeout": 45,
  "config_json": {
    "nats_url": "nats://nats-new.example.com:4222",
    "jetstream_max_age": "48h"
  }
}
// Response 200 — same shape as GET /api/v1/transports/{type} single-item response
{
  "transport_type": "nats",
  "enabled": true,
  "max_message_size": 2097152,
  "heartbeat_secs": 10,
  "connect_timeout": 45,
  "config_json": {
    "nats_url": "nats://nats-new.example.com:4222",
    "jetstream_max_age": "48h"
  },
  "state": "active",
  "connections": 1,
  "updated_at": "2026-07-22T12:05:00Z"
}
```

Configuration changes take effect on the next `Connect()` call for that transport. Existing connections are not disrupted unless the `enabled` field is set to `false`, which triggers a graceful shutdown of all connections for that transport type.

### 6.4 DELETE /api/v1/transports/{type}

Disables a transport and closes all its active connections. Equivalent to `PUT` with `"enabled": false` plus immediate connection teardown. Returns 204 on success. Connections are drained (message queues flushed) before closing.

```json
// Response 204 — no body
```

After deletion, the transport remains in the `transport_configs` table with `enabled = false`. To fully remove a transport configuration, an admin must run a direct `DELETE FROM transport_configs` migration.

### 6.5 GET /health/transports/{type}

Unauthenticated health probe for load balancers and monitoring. Returns 200 if the transport backend is reachable; 503 if not.

```json
// Response 200
{
  "transport_type": "sse",
  "healthy": true,
  "checked_at": "2026-07-22T12:00:00Z",
  "latency_ms": 4
}
// Response 503
{
  "transport_type": "nats",
  "healthy": false,
  "checked_at": "2026-07-22T12:00:00Z",
  "error": "nats: no response from server"
}
```

---

## 7. Edge Cases

| # | Edge Case | Description | Mitigation |
|---|-----------|-------------|------------|
| 1 | All transports unavailable simultaneously | Every transport in the fallback chain fails — SSE endpoint down, NATS cluster unreachable, Redis connection refused, relay server offline, no WebRTC peers reachable | `ConnectionManager` buffers all outbound messages in per-peer `MessageQueue` (ring buffer, 10,000 message capacity). Retries primary transport on exponential backoff. After buffer exhaustion, oldest messages are evicted and `transport_events` records `message_dropped`. The sync engine receives `ErrTransportUnreachable` and can surface full offline status to the user. |
| 2 | Transport flip-flop | A transport oscillates between healthy and degraded faster than the health-check window (e.g., flapping network interface) | Hysteresis: a transport must pass 3 consecutive `Health()` checks (spaced 10s apart) before moving from `StateDegraded` back to `StateActive`. A flip-flop counter increments on each degradation event; after 5 degradations in 5 minutes, the transport is quarantined for 5 minutes before re-evaluation. |
| 3 | Network partition with multi-transport | A network partition isolates node A from NATS but not from the relay; node B is accessible via relay but not NATS | `TransportSelector` evaluates transports independently per peer, not globally. `ConnectionManager` maintains per-peer connection maps. If NATS is unreachable for peer B but the relay is active, messages for peer B route through the relay while messages for peer C (reachable via NATS) continue on NATS. |
| 4 | Partial degradation across transports | One transport degrades (SSE latency spike) while another remains healthy; messages are already inflight on the degraded transport | `ConnectionManager` does not drain inflight messages from a degraded transport — it marks the transport degraded and routes new messages to the fallback. Inflight messages may arrive out of order relative to fallback-routed messages. The receiver's sequence-number-based gap detection (4.3) identifies out-of-order messages and triggers gap recovery without data loss. |
| 5 | Oversized payload on WebRTC | A `NodeAddPayload` exceeds the 256KB SCTP message limit but is under the 1MB SSE limit | `Send()` returns `ErrPayloadTooLarge`. The caller (sync engine) splits the payload: node content is sent as a reference ID, and the full content is fetched on-demand via REST. If the transport is SSE (1MB limit), the payload goes through as-is. Transport-specific `MaxMessageSize` is checked at `Send()` time, not at message construction. |
| 6 | Concurrent reconnection storm | 1,000 browser clients lose their SSE connection simultaneously (server restart) and all reconnect within 200ms | `SSEHub` applies a per-second accept-rate cap (configurable, default 500 connections/sec). Clients above the cap receive 503 with `Retry-After: 2` header. `EventSource` respects `Retry-After` natively. Jitter (±25%) is added to the reconnect delay to spread reconnections across the window. |
| 7 | Signing key expiry mid-connection | A JWT token (SSE) or HMAC key (relay) expires while a connection is `StateActive` | Key expiry does not tear down an active connection — authentication is validated only at `Connect()` time. The next `Health()` check that requires re-authentication returns `ErrAuthExpired`. `ConnectionManager` creates a new connection with fresh credentials and swaps it in atomically (new connection reaches `StateActive` → old connection `Disconnect()`). No message gap during the swap. |
| 8 | MLS payload with insufficient routing metadata | An encrypted MLS ciphertext arrives on a transport without the `tree_id` or `sequence` fields needed for routing | This is an application-layer problem, not a transport-layer problem. The transport delivers the `Message` struct as received. The sync engine validates `Message.TreeID` and `Message.Sequence` before forwarding to the MLS layer. Malformed messages are dropped and logged as `transport_events` with `event_type = 'message_dropped'` and details `{"reason": "missing_routing_metadata"}`. |
| 9 | Transport switch during opcode batch | A batch of 5 `NODE_ADD` messages (seq 100–104) is partially sent on SSE (seq 100–101) before SSE fails; seq 102–104 are retried on WebRTC | The sender re-queues seq 102–104 for the fallback transport. The receiver sees a gap at seq 102 on the SSE stream and issues `ACK(101)`. The fallback stream delivers seq 102–104. The receiver detects continuity (seq 102 on WebRTC follows seq 101 on SSE via its per-tree sequence tracker) and does not trigger gap recovery. Duplicate detection: if seq 102 arrives on both transports, the receiver's sequence watermark (`SequenceWatermark`) drops the duplicate. |
| 10 | Sequence counter wraparound | A tree processes >2^64 messages, causing the uint64 sequence counter to wrap to 0 | At 100,000 messages/second (far above any realistic tree sync rate), wraparound takes approximately 5.8 million years. This is not a practical concern. If a future high-frequency use case emerges, the sequence model can be extended to a `(epoch, sequence)` tuple without breaking the wire format — the epoch is the existing MLS epoch from SPEC-FTR-03. |
| 11 | Relay server at capacity | A relay node reaches its 500-session limit and a new peer attempts to connect | `Connect()` returns `ErrConnectionRefused` with a `Retry-After` value encoded in the error details. `TransportSelector` treats this as a transport failure and falls back to the next transport in the chain. If relay is the only available transport (air-gapped mode), the peer retries with exponential backoff. The relay server emits a `transport_events` row with `event_type = 'connection_refused'` and `details: {"reason": "session_limit", "current_sessions": 500}`. |
| 12 | Mixed-version transport negotiation | A `canopyd` node running v2.1 connects to a peer running v1.8 with different capability flags | `negotiateCapabilities()` computes the intersection of local and remote capability flags during `Connect()`. If the intersection is empty (no shared capabilities), `Connect()` returns `ErrTransportMismatch`. The `Metadata` map in `ConnectOptions` carries a `"version"` key and a `"capabilities"` key (comma-separated). Version-skew is not a transport concern — the sync engine handles opcode-level compatibility. |

---

## 8. Test Scenarios

| # | Scenario | Verification |
|---|----------|-------------|
| 1 | **Single-transport lifecycle (SSE)** | Create SSEAdapter, Connect to local HTTP test server, Send 100 messages, Receive them in order via channel, Disconnect. Assert all 100 messages arrive with monotonic sequence numbers, connection state transitions Init → Connecting → Active → Disconnecting → Closed. |
| 2 | **Multi-transport same payload** | Send an identical `Message{Opcode: OpNodeAdd, TreeID: <uuid>, Sequence: 42, ...}` over all 5 transports. Assert each transport delivers a byte-identical `Message` after deserialization. JSON transports (SSE, NATS, Redis) produce identical JSON. Binary transports (WebRTC, relay) produce identical CBOR that decodes to the same `Message`. |
| 3 | **SSE reconnection with Last-Event-ID** | Start SSE server with 50 pre-seeded events. Connect client at seq 0, receive messages 1–25, kill server. Restart server. Client reconnects with `Last-Event-ID: 25`. Assert client receives messages 26–50 without gaps or duplicates. |
| 4 | **NATS offline replay via JetStream** | Create NATS JetStream stream with 24h retention. Publish 200 messages. Disconnect consumer. Publish 50 more messages while disconnected. Reconnect consumer with `Last-Event-ID: 200`. Assert consumer receives messages 201–250 in order via ephemeral consumer replay. |
| 5 | **Redis consumer group replay** | Create Redis Stream + Consumer Group. XADD 100 messages. XREADGROUP delivers first 50. Disconnect consumer. XADD 20 more. Reconnect with `Last-Event-ID: 50`. Assert XREADGROUP with `>` delivers messages 51–120; each XACK'd after delivery. |
| 6 | **WebRTC P2P with STUN** | Spin up two pion peers behind simulated NAT. Exchange SDP via mock signaling channel. Assert ICE completes via STUN server-reflexive candidates. Exchange 500 messages over DataChannel. Assert ordered, reliable delivery. Verify no messages routed through TURN. |
| 7 | **WebRTC fallback to TURN** | Same setup but block STUN responses. Assert ICE falls back to TURN relay candidates. Exchange messages. Assert delivery with relay latency (10–50ms simulated). Verify TURN server sees ciphertext only (MLS-encrypted payloads). |
| 8 | **Relay binary protocol round-trip** | Start TCP relay server. Client connects with HELLO frame. Assert HELLO_ACK with agreed capabilities. Send 100 frames with HMAC-SHA256. Assert all frames decode to correct `Message` structs. Verify HMAC validation rejects tampered frames. Send BYE frame. Assert clean disconnect. |
| 9 | **Degradation: SSE → WebRTC failover** | Start with active SSE connection. Inject 3 consecutive SSE `Health()` failures (server returns 503). Assert `ConnectionManager` marks SSE degraded, emits `transport_degradation`. Verify new messages route through WebRTC. Assert no message loss: sequence numbers are contiguous across the transition. |
| 10 | **Full fallback chain exhaustion** | Configure all 5 transports but inject failures: SSE → ErrConnectionRefused, WebRTC → ErrTransportUnreachable, NATS → ErrAuthFailed, Redis → ErrConnectionRefused, relay → ErrTransportUnreachable. Assert `ConnectionManager` buffers messages, returns `ErrTransportUnreachable` to caller, and retries primary transport on exponential backoff. Assert `transport_events` records `fallback_activated` for each step. |
| 11 | **Sequence gap detection and recovery** | Stream messages with seq 1, 2, 3, 7, 8, 9. Assert receiver detects gap at seq 4–6. Assert receiver sends `ACK(3)`. Assert sender retransmits seq 4, 5, 6. Assert receiver processes 4, 5, 6 and advances watermark to 9. If retransmission fails 3 times, assert full `TREE_SNAPSHOT` recovery. |
| 12 | **Bandwidth adaptation tier transition** | Start at >1MB/s bandwidth. Assert full payloads (content inline). Artificially throttle to 500KB/s. Assert `BandwidthProfile` updates within 10-second window. Assert `transport_degradation` event with `bandwidth_tier: "reduced"`. Verify content trimmed to 4KB, card data as references. Throttle to 50KB/s. Assert `bandwidth_tier: "minimal"`. Restore to 2MB/s. Assert recovery to `bandwidth_tier: "full"`. |
| 13 | **Rate limiter enforcement** | Configure 500 messages/sec outbound rate limit. Send 600 messages in 1 second. Assert first 500 are sent successfully, messages 501–525 (burst: 2× rate for 5s = 50 burst tokens) are sent within the 5-second burst window, messages 526–600 return `ErrRateLimited`. Assert `transport_events` records `rate_limited` with details `{"rejected": 75}`. |
| 14 | **Concurrent reconnection storm** | Simulate 1,000 concurrent `Connect()` calls to `SSEHub`. Assert the accept-rate cap (500/sec) limits new connections. Assert 500 connections are accepted in the first second; the remaining 500 receive 503 with `Retry-After` and succeed on retry after jitter. Verify no file-descriptor exhaustion. |

---

## 9. Implementation Plan

| Phase | Components | Description | Est. Effort |
|-------|------------|-------------|-------------|
| 1 — Core Interface + SSE | `TransportAdapter` interface, supporting types (`ConnectOptions`, `Connection`, `ConnectionState`, `AuthMaterial`, `TransportType`), sentinel errors, `Message` + `Opcode` + payload types, `SSEAdapter` + `SSEHub`, `ConnectionManager` with SSE-only routing | Ship the interface and the primary transport. `ConnectionManager` manages SSE connections, routes messages, and buffers offline messages. All types are in `package transport`. Tests: single-transport lifecycle, SSE reconnection. | 5–7 days |
| 2 — NATS + Redis | `NATSAdapter` with JetStream, `RedisAdapter` with Consumer Groups, connect both to `ConnectionManager` | Add backend pub/sub transports. Both adapters implement the full `TransportAdapter` interface. Offline replay via JetStream ephemeral consumers and Redis `XREADGROUP`. Tests: NATS offline replay, Redis consumer group replay. | 4–6 days |
| 3 — Selector + Degradation | `TransportSelector`, `DeploymentMode`, `NetworkTopology`, degradation/fallback chain, health-check loop with hysteresis, `transport_configs` DDL seeding | Wire transport selection and graceful degradation. `TransportSelector` uses the priority matrix from T1.8 §6.2. Health checking runs in a background goroutine. Tests: SSE → WebRTC failover, full fallback chain exhaustion. | 4–5 days |
| 4 — WebRTC + P2P | `WebRTCAdapter` with `pion/webrtc`, `SignalingChannel` (SSE-backed), STUN/TURN integration | Add P2P transport. Signaling reuses existing SSE transport. NAT traversal: direct → STUN → TURN. Tests: WebRTC P2P with STUN, TURN fallback, multi-transport same payload. | 5–7 days |
| 5 — Relay Adapter | `RelayAdapter` with binary wire protocol (magic `0x43414E59 "CANY"`, CBOR payloads, HMAC-SHA256), TCP + QUIC support, server and client modes | For air-gapped deployments. Binary frame encode/decode, HELLO/HELLO_ACK handshake, PING/PONG heartbeat, BYE shutdown. Tests: relay binary protocol round-trip, HMAC tamper detection. | 4–6 days |
| 6 — Bandwidth + Pool Limits | Bandwidth measurement (10-second sliding window), content-tier adaptation, connection pool enforcement, rate limiter (token bucket), `transport_events` audit logging | Performance tuning and resource protection. Bandwidth adaptation trims payloads without message loss. Pool limits prevent file-descriptor and memory exhaustion. Rate limiters prevent abuse. Tests: bandwidth tier transitions, rate limiter enforcement, concurrent reconnection storm. | 4–5 days |
| 7 — Integration + Hardening | All 14 test scenarios passing, SSE event wiring, API endpoints, mixed-transport chaos tests, `transport_events` pruning job, documentation | Final integration pass. All transports interoperate. Chaos tests (network partition, transport kill mid-stream, slow consumer). SSE events wired to SPEC-API-01 event stream. API endpoints return live `transport_connections` and `transport_configs` data. | 3–5 days |

**Total: 29–41 days.** Phase 1 must ship first as the foundation. Phases 2–5 can parallelize if staffed by separate workers (each adapter is self-contained behind the `TransportAdapter` interface). Phase 6 depends on Phase 1–5 completion. Phase 7 is the integration gate — all 14 test scenarios from §8 must pass before BE-09 is marked complete.

---

## 10. References

| Reference | Relevance |
|-----------|-----------|
| T1.8-Multi-Transport-Architecture | Primary research document. Defines `TransportAdapter` interface, all 5 transport implementations, `ConnectionManager`, `TransportSelector`, bandwidth adaptation, connection pool limits, 13 tree-sync opcodes with per-opcode payload types, deployment-mode priority matrix, network topology detection, security model, testing strategy, and Mermaid architecture diagrams. This spec formalizes T1.8 into an implementable contract. |
| SPEC-API-01 (§2, §3, §4, §7) | Defines the SSE event-stream wire format (event types, data schemas, `id:`/`data:`/`event:` framing) that `transport_status`, `transport_error`, and `transport_degradation` events follow. Also defines the `Last-Event-ID` reconnection protocol used by SSE transport. |
| SPEC-FTR-02 (§2, §3, §5, §9) | Defines the Federated Transport Layer (FTL) for server-to-server message relay. The transport adapter layer is compatible with FTL — `Message` structs from any transport can be encapsulated in FTL envelopes for cross-node delivery. FTL signatures add relay authentication but do not replace transport-layer channel encryption. |
| SPEC-FTR-03 (§2, §3, §4, §7) | Defines MLS encryption sitting above the transport layer. `Message.Payload` is an `MLSCiphertext` struct (opaque bytes + group ID + epoch + sender leaf index). The transport adapter never sees plaintext node content. Transport-level authentication (JWT, HMAC, TLS certs) and channel encryption (TLS 1.3, DTLS-SRTP) operate independently of MLS. |
| ARCHITECTURE.md (§2.1, §4, §5) | Defines the Go/PostgreSQL/SSE architecture, single-binary `canopyd` deployment, HTTP/2 requirement for SSE multiplexing, and deferred post-MVP scope (multi-user, federation, MLS, multi-transport). The transport adapter layer is the implementation of ARCHITECTURE.md's "Transport: SSE (server→client) + HTTP POST (client→server)" line — extended to support all 5 transports post-MVP. |
| AGENTS.md §Architecture | Defines the Conversation DAG, Context Compiler, View Modes, and Cards as graph nodes. The transport adapter delivers tree-sync opcodes that mutate this DAG. Transport selection is invisible to the data model — the sync engine operates on `Message` structs, not transport-specific APIs. |
