# SPEC-FTR-02 — Federated Multi-Agent Architecture

> **Status:** Spec | **Phase:** Post-MVP (FTR-02) | **Blocks:** BE-08 (Profile Routing), BE-09 (Transport Adapter Layer), BE-10 (Encryption Layer), FE-07 (Multi-User Features)
> **References:** SPEC-API-06, SPEC-DM-04, SPEC-API-01, T1.8-Multi-Transport-Architecture, ARCHITECTURE.md §5.5, ARCHITECTURE.md §9.3
> **Commit:** 29b61a6

---

## 1. Purpose

Define the exact implementation contract for federating multiple Canopy instances — enabling Hermes profiles running on different machines, networks, or Hermes installations to discover each other, establish trusted connections, and collaborate on shared conversation trees. A Go worker reading this document can implement the federation service, peer discovery, profile routing, cross-server SSE relay, and trust verification without additional design decisions.

The MVP is single-user with a local canopyd server. SPEC-FTR-01 extends this to multiple humans and profiles on the same server. This spec extends to the multi-server case: profiles on Server A discovering and collaborating with profiles on Server B.

**Key insight:** Federation is profile routing, not server-to-server message forwarding. Each user owns their profiles. Profiles on remote servers are first-class participants in a tree — they see events, send messages, and respond just like local profiles.

---

## 2. Design Decisions

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | Federation model | Peer-to-peer relay with optional NATS backbone (T1.8) | Each Canopy instance is autonomous. Servers peer directly. NATS is optional when latency or offline queuing matters. |
| 2 | Peer discovery | Manual (user shares invite URL) + optional DNS auto-discovery (post-MVP) | MVP-conservative: invite link shared out-of-band. |
| 3 | Trust establishment | Signed federation tokens, HMAC-verified, 24h expiry | Each server has a long-lived signing key. No external PKI in MVP. |
| 4 | Token format | Signed payload with server_id, profile_id, tree_id, key fingerprint, expiry | Purpose-scoped: one token grants access to exactly one tree on exactly one remote server for exactly one profile. |
| 5 | Event relay | SSE-to-SSE with at-least-once delivery | Simplest architecture. No polling. Each server maintains outbound SSE connections to federated peers. |
| 6 | Event ordering | Causal ordering via vector clocks. Each server maintains (server_id, counter) per tree. | Without global clock, causal ordering is the strongest guarantee in P2P federation. |
| 7 | Wire format | FTL envelope — signed, encrypted, causally-tagged JSON | Every cross-server event is wrapped: {seq, sender_server, sender_profile, clock, signature, ciphertext} |
| 8 | Encryption | Profile-level ECDH (X25519) key exchange at handshake | Each profile generates a Curve25519 keypair. Payloads encrypted with AEAD (ChaCha20-Poly1305). |
| 9 | Offline queue | Per-peer outbound queue (up to 10,000 events). Replay on reconnect. | Same pattern as SPEC-FTR-01 §6.4, per-peer instead of per-user. |
| 10 | Conflict resolution | LWW at field level with clock comparison. Structural conflicts surface as manual resolution nodes. | LWW safest for text edits. Complex merges become synthetic nodes flagged for human review. |
| 11 | Profile visibility | Per-profile federation gate. User must explicitly enable a profile for cross-server visibility. | A user may have 10 profiles but only want 2 reachable from a remote server. |
| 12 | Federation scope | Per-tree. A federation link connects exactly two servers for exactly one tree. | Prevents accidental cross-tree leakage. |
| 13 | Profile routing | profile_route table: {profile_id, server_url, public_key, tree_id} | Outbound messages for remote profiles are forwarded to the correct peer. |
| 14 | Inverse routing | Remote profile ID → local proxy participant with "remote" badge | Remote profiles appear as participants. Proxy stores routing metadata to forward replies back. |
| 15 | Heartbeat | 30s ping over SSE relay. 90s timeout → offline. | Matches SPEC-FTR-01 presence timing. |
| 16 | Reconnection | Exponential backoff 1s→2s→4s→8s→16s (max 30s). Reset on successful heartbeat. | Prevents thundering herd on reconnect. |
| 17 | Link revocation | Instant. Revocation message in next heartbeat. Drops relay, removes remote profile from tree. | Immediate stop of cross-server data flow. |
| 18 | Audit trail | Every federation event logged to existing audit trail (SPEC-DM-03) | Security-sensitive actions need immutable records. |
| 19 | Rate limiting | Per-peer: 500 events/min, 10 handshakes/hr, 5 link creations/hr | Configurable via canopyd configuration file. |
| 20 | No global namespace | No central registry. Each server identified by URL + signing key fingerprint. | Federation is a mesh, not a hub. No single point of failure. |

---

## 3. Go Interface Definitions

### 3.1 Federation Service

```go
package federation

import (
    "context"
    "time"
    "github.com/google/uuid"
)

type FederationRole int
const (
    RoleInitiator FederationRole = iota
    RoleAcceptor
)

type PeerState int
const (
    PeerDisconnected PeerState = iota
    PeerConnecting
    PeerConnected
    PeerReconnecting
    PeerRevoked
    PeerQuarantined
)

type FederationPeer struct {
    ID                uuid.UUID       `json:"id"`
    ServerURL         string          `json:"server_url"`
    SigningKeyFP      string          `json:"signing_key_fp"`
    PublicKey         []byte          `json:"public_key"`
    SharedSecret      []byte          `json:"-"`              // never serialized
    Role              FederationRole  `json:"role"`
    State             PeerState       `json:"state"`
    ConnectedAt       *time.Time      `json:"connected_at,omitempty"`
    LastHeartbeat     time.Time       `json:"last_heartbeat"`
    TreeID            uuid.UUID       `json:"tree_id"`
    OutboundQueueSize int             `json:"outbound_queue_size"`
}

type FederationService interface {
    CreateFederationLink(ctx context.Context, remoteURL string, localProfileID uuid.UUID, treeID uuid.UUID) (*FederationPeer, string, error)
    AcceptFederationLink(ctx context.Context, token string, localProfileID uuid.UUID) (*FederationPeer, error)
    RevokeFederationLink(ctx context.Context, peerID uuid.UUID) error
    GetPeer(ctx context.Context, peerID uuid.UUID) (*FederationPeer, error)
    ListPeers(ctx context.Context, treeID uuid.UUID) ([]*FederationPeer, error)
    ListAllPeers(ctx context.Context) ([]*FederationPeer, error)
    HeartbeatLoop(ctx context.Context, heartbeatInterval time.Duration, timeout time.Duration)
}
```

### 3.2 Profile Router

```go
package federation

import (
    "context"
    "time"
    "github.com/google/uuid"
)

type ProfileRoute struct {
    ProfileID  uuid.UUID `json:"profile_id"`
    HomeServer string    `json:"home_server"`
    PublicKey  []byte    `json:"public_key"`
    TreeID     uuid.UUID `json:"tree_id"`
    IsLocal    bool      `json:"is_local"`
    LocalAlias string    `json:"local_alias,omitempty"`
    Handle     string    `json:"handle"`
    LastSeen   time.Time `json:"last_seen"`
}

type ProfileRouter interface {
    RegisterRoute(ctx context.Context, route *ProfileRoute) error
    RemoveRoute(ctx context.Context, profileID uuid.UUID, treeID uuid.UUID) error
    LookupRoute(ctx context.Context, profileID uuid.UUID, treeID uuid.UUID) (*ProfileRoute, error)
    ListLocalProfiles(ctx context.Context, userID uuid.UUID) ([]ProfileRoute, error)
    ListRemoteProfiles(ctx context.Context, treeID uuid.UUID) ([]ProfileRoute, error)
    IsRemoteProfile(ctx context.Context, profileID uuid.UUID) (bool, error)
}

var ErrRouteNotFound = errors.New("federation: profile route not found")
```

### 3.3 FTL Transport Service

```go
package federation

import (
    "context"
    "time"
    "encoding/json"
    "github.com/google/uuid"
)

type FTLVersion int
const CurrentFTLVersion FTLVersion = 1

type FTLEnvelope struct {
    FTLVersion      int              `json:"ftl_version"`
    SenderServerID  uuid.UUID        `json:"sender_server_id"`
    SenderProfileID uuid.UUID        `json:"sender_profile_id"`
    Sequence        int64            `json:"seq"`
    Clock           map[string]int64 `json:"clock"`
    Timestamp       time.Time        `json:"timestamp"`
    TreeID          uuid.UUID        `json:"tree_id"`
    Ciphertext      []byte           `json:"ciphertext"`
    Nonce           []byte           `json:"nonce"`
    Signature       []byte           `json:"signature"`
    SigningKeyFP    string           `json:"signing_key_fp"`
}

type FTLInnerPayload struct {
    EventType  string          `json:"event_type"`
    Payload    json.RawMessage `json:"payload"`
    RefEventID string          `json:"ref_event_id,omitempty"`
}

type FTLTransportService interface {
    SendEvent(ctx context.Context, peerID uuid.UUID, profileID uuid.UUID, eventType string, payload json.RawMessage) error
    ReceiveEvent(ctx context.Context, envelope *FTLEnvelope) (*FTLInnerPayload, error)
    EstablishSession(ctx context.Context, peer *FederationPeer, localPrivateKey []byte) error
    GenerateSigningKeyPair() (publicKey []byte, privateKey []byte, fingerprint string)
    GenerateECDHKeyPair() (publicKey []byte, privateKey []byte)
}
```

### 3.4 Error Catalogue

```go
package federation

import "errors"

var (
    ErrFederationNotFound    = errors.New("federation: peer not found")
    ErrTokenInvalid          = errors.New("federation: federation token is invalid or expired")
    ErrTokenExpired          = errors.New("federation: federation token has expired")
    ErrHandshakeFailed       = errors.New("federation: handshake with peer failed")
    ErrSignatureMismatch     = errors.New("federation: envelope signature does not match sender")
    ErrDecryptionFailed      = errors.New("federation: payload decryption failed")
    ErrRouteNotFound         = errors.New("federation: profile route not found")
    ErrProfileNotLocal       = errors.New("federation: profile is not local to this server")
    ErrPeerOffline           = errors.New("federation: peer is offline, event queued for later delivery")
    ErrLinkAlreadyExists     = errors.New("federation: link to this server+tree already exists")
    ErrRateLimited           = errors.New("federation: rate limit exceeded for this peer")
    ErrLinkRevoked           = errors.New("federation: federation link has been revoked")
    ErrNoSharedSecret        = errors.New("federation: ECDH session not established; call EstablishSession first")
    ErrQuarantined           = errors.New("federation: peer is quarantined due to suspicious activity")
)
```

### 3.5 SSE Event Definitions

```go
package federation

type FederationEventType string

const (
    EventPeerConnected         FederationEventType = "federation_peer_connected"
    EventPeerDisconnected      FederationEventType = "federation_peer_disconnected"
    EventLinkCreated           FederationEventType = "federation_link_created"
    EventLinkRevoked           FederationEventType = "federation_link_revoked"
    EventRemoteProfileOnline   FederationEventType = "remote_profile_online"
    EventRemoteProfileOffline  FederationEventType = "remote_profile_offline"
    EventRemoteProfileJoined   FederationEventType = "remote_profile_joined"
    EventRemoteProfileLeft     FederationEventType = "remote_profile_left"
    EventRemoteNodeAdded       FederationEventType = "remote_node_added"
    EventRemoteNodeUpdated     FederationEventType = "remote_node_updated"
    EventRemoteNodeDeleted     FederationEventType = "remote_node_deleted"
    EventRemoteNodeRefAdded    FederationEventType = "remote_node_ref_added"
)

type FederationEvent struct {
    Type      FederationEventType `json:"type"`
    TreeID    uuid.UUID           `json:"tree_id"`
    PeerID    uuid.UUID           `json:"peer_id,omitempty"`
    ProfileID uuid.UUID           `json:"profile_id,omitempty"`
    Timestamp time.Time           `json:"timestamp"`
    Payload   json.RawMessage     `json:"payload"`
}
```

---

## 4. Federation Handshake Protocol

### 4.1 Sequence

```
User A → canopyd A: POST /api/v1/federation/link (server_b_url, tree_id)
canopyd A → canopyd B: POST /api/v1/federation/handshake (signed token, ecdhe_pubkey)
canopyd B → canopyd A: Accept (peer_id, ecdhe_pubkey)
Both: derive ECDH shared secret
Both: establish SSE relay connection
```

### 4.2 Federation Token Format

Signed JSON payload, URL-safe base64 encoded:

```json
{
    "token_version": 1,
    "server_id": "uuid-of-server-a",
    "server_url": "https://canopy-a.example.com",
    "profile_id": "uuid-of-profile",
    "tree_id": "uuid-of-tree",
    "issued_at": "2026-07-22T12:00:00Z",
    "expires_at": "2026-07-23T12:00:00Z",
    "signing_key_fp": "sha256:fingerprint"
}
```

Signed with Ed25519. Verify: decode, check expiry, lookup key by fp, ed25519.Verify.

### 4.3 ECDH Key Exchange

1. Server A generates X25519 (a_priv, a_pub), sends a_pub in handshake
2. Server B generates (b_priv, b_pub), computes shared = x25519(b_priv, a_pub)
3. Server B sends b_pub in response
4. Server A computes shared = x25519(a_priv, b_pub)
5. Shared secret used as AEAD key (ChaCha20-Poly1305, 256-bit)

Not persisted to disk. On restart, re-establish all links (fast — one round trip).

### 4.4 SSE Relay

```
Server A → Server B: GET /api/v1/federation/events?peer_id=<uuid>&tree_id=<uuid>
  Authorization: Bearer <federation-token>

Server B → Server A: GET /api/v1/federation/events?tree_id=<uuid>
  Authorization: Bearer <federation-token>
```

Both streams emit FTLEnvelope-wrapped events.

---

## 5. API Endpoints

Prefix: `/api/v1/federation/`. Auth: `Bearer <hermes-jwt>` for user endpoints, `Bearer <federation-token>` for P2P.

### 5.1 Link Management (User-Facing)

```
POST   /api/v1/federation/link                       → Create federation link
GET    /api/v1/federation/link                        → List active links
DELETE /api/v1/federation/link/{peer_id}              → Revoke link
```

**POST /api/v1/federation/link:**
```json
// Request
{"remote_server_url": "https://canopy-b.example.com", "local_profile_id": "uuid", "tree_id": "uuid"}
// Response 201
{"peer_id": "uuid", "tree_id": "uuid", "remote_server_url": "https://canopy-b.example.com", "state": "connected", "connected_at": "2026-07-22T12:00:00Z"}
```

### 5.2 Peer-to-Peer Endpoints (Internal)

```
POST   /api/v1/federation/handshake          → Accept handshake
GET    /api/v1/federation/events              → SSE relay stream
POST   /api/v1/federation/events              → Send FTL-wrapped event
```

**POST /api/v1/federation/handshake:**
```json
// Request
{"token": "base64-signed-token", "server_url": "https://canopy-a.example.com", "ecdhe_public_key": "base64-x25519-pubkey"}
// Response 200
{"peer_id": "uuid", "server_url": "https://canopy-b.example.com", "ecdhe_public_key": "base64-x25519-pubkey"}
```

### 5.3 Profile Routing Endpoints

```
GET    /api/v1/federation/profiles/remote                  → List remote profiles
GET    /api/v1/federation/profiles/remote/{profile_id}     → Get remote profile details
POST   /api/v1/federation/profiles/{profile_id}/ping       → Ping remote profile
```

---

## 6. Data Model

### 6.1 Federation Peers Table

```sql
CREATE TABLE federation_peers (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_url        TEXT NOT NULL,
    signing_key_fp    TEXT NOT NULL,
    ecdhe_public_key  BYTEA,
    role              INT NOT NULL DEFAULT 0,     -- 0=initiator, 1=acceptor
    state             INT NOT NULL DEFAULT 0,     -- 0=disconnected, 1=connecting, 2=connected, 3=reconnecting, 4=revoked, 5=quarantined
    tree_id           UUID NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    created_by        UUID NOT NULL REFERENCES profiles(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    connected_at      TIMESTAMPTZ,
    last_heartbeat    TIMESTAMPTZ,
    revoked_at        TIMESTAMPTZ,
    revoke_reason     TEXT,
    UNIQUE (server_url, tree_id)
);
CREATE INDEX idx_fed_peers_state ON federation_peers(state);
CREATE INDEX idx_fed_peers_tree ON federation_peers(tree_id);
```

### 6.2 Profile Routes Table

```sql
CREATE TABLE profile_routes (
    profile_id    UUID PRIMARY KEY,
    home_server   TEXT NOT NULL,
    public_key    BYTEA NOT NULL,
    tree_id       UUID NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    peer_id       UUID NOT NULL REFERENCES federation_peers(id) ON DELETE CASCADE,
    is_local      BOOLEAN NOT NULL DEFAULT false,
    local_alias   TEXT,
    handle        TEXT,
    last_seen     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (profile_id, tree_id)
);
CREATE INDEX idx_profile_routes_tree ON profile_routes(tree_id);
CREATE INDEX idx_profile_routes_peer ON profile_routes(peer_id);
```

### 6.3 Federation Event Queue

```sql
CREATE TABLE federation_event_queue (
    id                BIGSERIAL PRIMARY KEY,
    peer_id           UUID NOT NULL REFERENCES federation_peers(id) ON DELETE CASCADE,
    envelope_json     JSONB NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered         BOOLEAN NOT NULL DEFAULT false,
    delivery_attempts INT NOT NULL DEFAULT 0,
    last_error        TEXT
);
CREATE INDEX idx_fed_queue_undelivered ON federation_event_queue(peer_id, id) WHERE delivered = false;
```

---

## 7. Edge Cases

| # | Edge Case | Handling |
|---|-----------|----------|
| 1 | Peer server offline during link creation | Retry handshake with exponential backoff (1s→30s max) for 5 minutes. Link created in disconnected state. Events queued. Auto-retry when peer comes online. |
| 2 | Simultaneous link creation (both servers initiate) | Server with smaller UUID wins. Other gets 409 Conflict. Losing server's user receives federation_link_rejected event. |
| 3 | Signing key rotated | Old tokens invalid. Peer must re-establish. Server broadcasts link_revoked with reason before rotation. |
| 4 | ECDH shared secret leaked | Session ephemeral (not persisted). Revoke and re-establish with fresh ECDH. |
| 5 | Signature verification fails | Drop event, log warning. 3 consecutive failures → quarantine peer. Human review required to un-quarantine. |
| 6 | Events for non-federated tree | Reject with 403. Log for forensic purposes. |
| 7 | Peer offline for extended period | Queue up to 10,000 events per peer. Oldest pruned. On reconnect, replay + queue_overflow event if any were dropped. |
| 8 | Remote profile deleted while link active | Send profile_departed event. Remove route from table. Remote server removes proxy participant from tree. |
| 9 | Revocation during active event queue | Immediate. Queued events discarded. Stale entries marked (not deleted — audit trail). |
| 10 | Remote profile action triggers approval gate | Gate created on remote server. Approval result relayed as federation event. |
| 11 | Vector clock divergence (split-brain) | LWW at field level. Structural conflicts produce synthetic conflict node. |
| 12 | Large payload exceeds MaxMessageSize | Split into chunks. Chunked frames carry chunk_seq and chunk_total fields. Reassembled on receive side. |

---

## 8. Test Scenarios

| # | Scenario | Verification |
|---|----------|-------------|
| 1 | Create federation link between two servers | Peer in connected state. SSE relay established. Both servers show peer in ListPeers. |
| 2 | Send message in federated tree | Message appears on peer server. Remote profile visible as participant. |
| 3 | Remote profile replies | Reply arrives on originating server with remote profile ID as author. |
| 4 | Token expires during session | Existing relay continues (verified at handshake). New handshakes with expired token rejected. |
| 5 | Peer goes offline | Missing heartbeat detected at 90s. State → disconnected. Events queued. |
| 6 | Peer comes back online | Handshake retried. Queued events replayed. State → connected. |
| 7 | Revoke federation link | Revocation sent via heartbeat. Both servers log to audit trail. Profiles removed. |
| 8 | Invalid signature | Event dropped. 3 consecutive → quarantine. Human review required. |
| 9 | Simultaneous link creation | One link rejected (smaller UUID wins). Conflict event sent to loser's user. |
| 10 | Remote profile deleted | profile_departed event. Route removed. Proxy removed from tree. |
| 11 | Concurrent offline edits (vector clock) | LWW produces deterministic result. Losing edit logged. |
| 12 | Rate limit exceeded | 429 returned. Quota resets per rate window. |
| 13 | Server restart recovery | Handshake re-established. New ECDH keys generated. All links reconnect. |
| 14 | Chunked large payload | Split → send N chunks → reassemble. Verify complete assembly. |

---

## 9. Security Considerations

| Concern | Mitigation |
|---------|------------|
| Peer spoofing | Ed25519 signatures on every FTL envelope. Signing keys exchanged out-of-band at link creation. |
| MITM during handshake | ECDH (X25519) provides forward secrecy. Handshake additionally signed by federation token. |
| Replay attacks | Monotonically increasing sequence per sender. Receiver rejects seen or lower sequences. |
| Event injection | Tree-scoped tokens prevent cross-tree event fabrication. |
| DoS via spam | Per-peer rate limiting (500 events/min). Excessive invalid signatures trigger quarantine. |
| Shared secret leak | Ephemeral X25519 keys (not persisted). Forward secrecy protects past sessions. |
| Cross-tree leakage | Federation links are tree-scoped. profile_routes table enforces tree scope. |
| Data at rest | FTL protects in transit. Data-at-rest encryption is SPEC-FTR-03 (MLS). |
| Token theft | 24h expiry. Scoped to server+profile+tree. Revocable. |

---

## 10. Implementation Phasing

| Phase | Components | Est. Effort |
|-------|-----------|-------------|
| P1 — Core Federation | FederationService, peer CRUD, handshake, token gen/verify | 3-5d (backend) |
| P2 — FTL Transport | Ed25519 signing, ECDH key exchange, AEAD encryption, SSE relay | 4-6d (backend) |
| P3 — Profile Router | Route table CRUD, local/remote resolution, route lookup | 2-3d (backend) |
| P4 — Event Relay | SSE-to-SSE relay, event queue, offline buffering, replay, chunking | 3-5d (backend) |
| P5 — Federation UI | Link management, remote profile proxy, presence indicators, link status | 3-4d (frontend) |
| P6 — Conflict Resolution | Vector clock merge, LWW resolution, conflict nodes, merge UI | 3-5d (backend+frontend) |
| P7 — Hardening | Rate limiting, quarantine, audit trail, security tests, chaos | 2-3d |

**Total: 20-31 days.** P1/P2 can be parallelized. P6 depends on P3/P4.

---

## 11. Cross-References

| Reference | Relevance |
|-----------|-----------|
| SPEC-API-01 (§3) | SSE event stream — federation events ride same SSE transport, wrapped in FTL envelopes |
| SPEC-API-06 (§2, §3) | Multi-user & profile endpoints — federation extends routing across servers |
| SPEC-DM-04 (§2, §3) | User & profile DDL — federation extends profiles to span servers via profile_routes |
| SPEC-API-04 (§3.2) | Merge endpoints — conflict resolution after federated edits uses existing merge |
| SPEC-FTR-01 (§2, §5) | Multi-user collaboration — remote approval gates live on profile's home server |
| T1.8-Multi-Transport-Architecture | Transport adapter — federation can use SSE, NATS, or WebSocket as underlying relay |
| ARCHITECTURE.md §5.5 | "Profile tokens for cross-server federation" — this spec implements that design |
| ARCHITECTURE.md §9.3 | "Multi-agent federation deferred" — this spec delivers the deferred feature |
