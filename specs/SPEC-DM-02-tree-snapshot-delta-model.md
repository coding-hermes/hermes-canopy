# SPEC-DM-02 — Tree Snapshot & Delta Model

> **Status:** Spec | **Blocks:** Phase 3 (API Specs), Phase 4 (Backend), Phase 5 (Frontend)
> **Depends on:** SPEC-DM-01 (nodes + edges DDL)

---

## 1. Purpose

Define the exact PostgreSQL DDL, Go interfaces, TypeScript types, SHA256 hash algorithm, and delta computation/application logic for Canopy's tree snapshots and incremental sync. A worker reading this spec must produce the correct snapshot repository, delta computation engine (Go server-side), and delta application logic (TypeScript client-side) with zero clarifying questions.

Snapshots provide point-in-time verifiable state of a tree. Deltas are the diff between any two snapshots — they are what the SSE stream sends to connected clients, minimizing bandwidth by transmitting only changes since the client's last-known hash.

---

## 2. Design Decisions (from ARCHITECTURE.md)

| Decision | Choice | Source |
|----------|--------|--------|
| Snapshot storage | PostgreSQL `tree_snapshots` table | ARCHITECTURE.md §3.4 |
| Snapshot hashing | SHA256 over canonical node+edge ordering | ARCHITECTURE.md §3.4 |
| Delta transport | Computed server-side, applied client-side | ARCHITECTURE.md §4.1 (SSE events) |
| Delta granularity | Whole-tree (not per-branch). Client filters by interest | ARCHITECTURE.md §4.1 |
| Snapshot frequency | On every commit (write-optimized). Background compaction merges adjacent snapshots | This spec |
| Hash stability | Deterministic: same tree state → same hash | This spec §6 |
| Client sync | Client sends `Last-Known-Hash: <sha256>` header. Server computes delta from that hash to current | This spec §7 |
| Full sync fallback | If client hash not found in snapshots (compacted away), server sends full tree | This spec §7.4 |
| Yjs / PostgreSQL boundary | Yjs is client-authoritative for local state. Snapshots are server-authoritative for sync | ARCHITECTURE.md §3.2 |

---

## 3. PostgreSQL DDL

### 3.1 tree_snapshots Table

```sql
-- 000006_snapshots.up.sql

CREATE TABLE tree_snapshots (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    parent_hash     text,                                   -- NULL for first snapshot
    hash            text        NOT NULL,                   -- SHA256 hex (64 chars)
    node_count      integer     NOT NULL DEFAULT 0,
    edge_count      integer     NOT NULL DEFAULT 0,
    snapshot_data   jsonb       NOT NULL DEFAULT '{}',      -- Full snapshot payload (compact form)
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT chk_snapshot_hash_length CHECK (char_length(hash) = 64),
    CONSTRAINT chk_snapshot_parent_hash_length CHECK (parent_hash IS NULL OR char_length(parent_hash) = 64)
);

-- Indexes
CREATE INDEX idx_snapshots_tree_id     ON tree_snapshots(tree_id);
CREATE INDEX idx_snapshots_hash        ON tree_snapshots(hash);
CREATE INDEX idx_snapshots_tree_created ON tree_snapshots(tree_id, created_at DESC);
CREATE UNIQUE INDEX idx_snapshots_tree_hash ON tree_snapshots(tree_id, hash);
```

**snapshot_data JSONB shape (compact form for storage efficiency):**

```json
{
  "nodes": {
    "<node_id>": [1, "2024-01-15T10:30:00Z", "<parent_id>", "markdown", "message"],
    "<node_id>": [2, "2024-01-15T10:31:00Z", "<parent_id>", "plain", "system"]
  },
  "edges": {
    "<edge_id>": ["<source_id>", "<target_id>", "reply"],
    "<edge_id>": ["<source_id>", "<target_id>", "fork"]
  }
}
```

Compact encoding maps each node to: `[sequence_num, created_at, parent_id, content_format, node_type]`. Each edge to: `[source_id, target_id, edge_type]`. This minimizes JSONB storage (no repeated key names) while preserving all fields needed for hash computation. Node content itself is NOT stored in snapshot_data — snapshots reference nodes by ID. Content verification is done via content_hash (see §3.2).

### 3.2 Node Content Hashes (for Delta Detection)

To detect content changes between snapshots, a lightweight hash of node content is maintained. This is added to the `nodes` table via migration:

```sql
-- 000007_node_content_hash.up.sql

ALTER TABLE nodes ADD COLUMN content_hash text;
-- Populate existing rows
UPDATE nodes SET content_hash = encode(sha256(content::bytea), 'hex') WHERE content_hash IS NULL;
-- Make NOT NULL for future inserts
ALTER TABLE nodes ALTER COLUMN content_hash SET NOT NULL;

-- Trigger to auto-compute on insert/update
CREATE OR REPLACE FUNCTION set_content_hash() RETURNS trigger AS $$
BEGIN
    NEW.content_hash := encode(sha256(NEW.content::bytea), 'hex');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_node_content_hash
    BEFORE INSERT OR UPDATE OF content ON nodes
    FOR EACH ROW
    EXECUTE FUNCTION set_content_hash();
```

### 3.3 tree_events Table (Append-Only Event Log)

Snapshots capture periodic state. Events capture every individual change for audit and replay:

```sql
-- 000008_tree_events.up.sql

CREATE TABLE tree_events (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    snapshot_id     uuid        REFERENCES tree_snapshots(id) ON DELETE SET NULL,
    event_type      text        NOT NULL,      -- 'node_added','node_updated','node_removed','edge_added','edge_removed'
    node_id         uuid,                       -- Affected node (NULL for edge-only events)
    edge_id         uuid,                       -- Affected edge (NULL for node-only events)
    payload         jsonb       NOT NULL DEFAULT '{}',   -- Event-specific data
    sequence_num    bigint      NOT NULL,       -- Monotonic per tree
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT chk_event_type CHECK (event_type IN (
        'node_added', 'node_updated', 'node_removed',
        'edge_added', 'edge_removed'
    ))
);

-- Indexes
CREATE INDEX idx_tree_events_tree     ON tree_events(tree_id, sequence_num);
CREATE INDEX idx_tree_events_snapshot ON tree_events(snapshot_id);
CREATE INDEX idx_tree_events_node     ON tree_events(node_id) WHERE node_id IS NOT NULL;
CREATE INDEX idx_tree_events_created  ON tree_events(tree_id, created_at);
```

**Event payload shapes:**

| Event Type | payload |
|------------|---------|
| `node_added` | `{node_id, parent_id, content_hash, content_format, node_type}` |
| `node_updated` | `{node_id, changed_fields: ["content", "metadata"], old_content_hash, new_content_hash}` |
| `node_removed` | `{node_id, deleted_at}` |
| `edge_added` | `{edge_id, source_id, target_id, edge_type}` |
| `edge_removed` | `{edge_id, source_id, target_id, edge_type, deleted_at}` |

---

## 4. Go Structs & Repository Interface

### 4.1 Package Layout

```
internal/
├── db/
│   ├── snapshot_repo.go    # SnapshotRepo interface + pgx implementation
│   └── event_repo.go       # EventRepo interface + pgx implementation
├── sync/
│   ├── delta.go            # Delta structs + ComputeDelta(from, to)
│   ├── hash.go             # ComputeSnapshotHash function
│   └── snapshot.go         # Snapshot creation + compaction logic
```

### 4.2 Go Structs

```go
package db

import (
    "time"
    "github.com/google/uuid"
)

// TreeSnapshot represents a point-in-time hash-verified state of a tree.
// Maps to the `tree_snapshots` table.
type TreeSnapshot struct {
    ID           uuid.UUID  `db:"id"            json:"id"`
    TreeID       uuid.UUID  `db:"tree_id"       json:"treeId"`
    ParentHash   *string    `db:"parent_hash"   json:"parentHash"`    // NULL for first snapshot
    Hash         string     `db:"hash"          json:"hash"`          // SHA256 hex, 64 chars
    NodeCount    int        `db:"node_count"    json:"nodeCount"`
    EdgeCount    int        `db:"edge_count"    json:"edgeCount"`
    SnapshotData []byte     `db:"snapshot_data" json:"snapshotData"`  // JSONB
    CreatedAt    time.Time  `db:"created_at"    json:"createdAt"`
}

// TreeEvent represents a single change event in a tree.
// Maps to the `tree_events` table.
type TreeEvent struct {
    ID           uuid.UUID  `db:"id"            json:"id"`
    TreeID       uuid.UUID  `db:"tree_id"       json:"treeId"`
    SnapshotID   *uuid.UUID `db:"snapshot_id"   json:"snapshotId"`   // Which snapshot includes this event
    EventType    string     `db:"event_type"    json:"eventType"`    // node_added, node_updated, etc.
    NodeID       *uuid.UUID `db:"node_id"       json:"nodeId"`
    EdgeID       *uuid.UUID `db:"edge_id"       json:"edgeId"`
    Payload      []byte     `db:"payload"       json:"payload"`      // JSONB
    SequenceNum  int64      `db:"sequence_num"  json:"sequenceNum"`
    CreatedAt    time.Time  `db:"created_at"    json:"createdAt"`
}
```

### 4.3 Snapshot Repository

```go
package db

import (
    "context"
    "github.com/google/uuid"
)

// SnapshotRepo manages tree snapshots.
type SnapshotRepo interface {
    // CreateSnapshot computes a new snapshot for the given tree and stores it.
    // Returns the created snapshot with hash populated.
    CreateSnapshot(ctx context.Context, treeID uuid.UUID) (*TreeSnapshot, error)

    // GetSnapshot returns a snapshot by its hash.
    // Returns nil, nil if not found.
    GetSnapshot(ctx context.Context, hash string) (*TreeSnapshot, error)

    // GetLatestSnapshot returns the most recent snapshot for a tree.
    // Returns nil, nil if no snapshots exist yet.
    GetLatestSnapshot(ctx context.Context, treeID uuid.UUID) (*TreeSnapshot, error)

    // GetSnapshotChain returns snapshots from `fromHash` to the latest (exclusive of fromHash).
    // Used for delta chain computation when a direct delta can't be computed
    // because intermediate snapshots were compacted.
    // Returns snapshots in chronological order (oldest first).
    GetSnapshotChain(ctx context.Context, treeID uuid.UUID, fromHash string) ([]TreeSnapshot, error)

    // CompactSnapshots merges snapshots older than `before` into a single snapshot.
    // Keeps the most recent snapshot per hour for the past 24h, per day for the past 30d.
    // Returns the number of snapshots compacted.
    CompactSnapshots(ctx context.Context, treeID uuid.UUID, before time.Time) (int, error)

    // DeleteSnapshotsBefore removes all snapshots older than `before`.
    // Used after compaction. Returns count deleted.
    DeleteSnapshotsBefore(ctx context.Context, treeID uuid.UUID, before time.Time) (int, error)
}
```

### 4.4 Event Repository

```go
package db

import (
    "context"
    "github.com/google/uuid"
)

// EventRepo manages the append-only tree event log.
type EventRepo interface {
    // AppendEvent writes a single event to the log.
    AppendEvent(ctx context.Context, treeID uuid.UUID, eventType string, nodeID, edgeID *uuid.UUID, payload []byte, snapshotID *uuid.UUID) (*TreeEvent, error)

    // GetEventsSince returns all events with sequence_num > `sinceSeq` for a tree.
    // Used by SSE hub to stream missed events on reconnection.
    GetEventsSince(ctx context.Context, treeID uuid.UUID, sinceSeq int64, limit int) ([]TreeEvent, error)

    // GetEventsBetweenSnapshots returns all events between two snapshots (inclusive of fromHash's events, exclusive of toHash's).
    // Used for delta computation.
    GetEventsBetweenSnapshots(ctx context.Context, fromHash, toHash string) ([]TreeEvent, error)

    // GetLatestSequenceNum returns the highest sequence_num for a tree.
    // Returns 0 if no events exist.
    GetLatestSequenceNum(ctx context.Context, treeID uuid.UUID) (int64, error)
}
```

---

## 5. TypeScript Types

### 5.1 Core Types

```typescript
// types/sync.ts

/** Compact node representation used in snapshots and deltas. */
interface CompactNode {
  /** sequence_num */
  s: number;
  /** created_at as ISO string */
  c: string;
  /** parent_id (null for root) */
  p: string | null;
  /** content_hash (SHA256 hex) */
  h: string;
  /** content_format: 'markdown' | 'plain' | 'rich' */
  f: string;
  /** node_type: 'message' | 'synthesis' | 'system' */
  t: string;
}

/** Compact edge representation used in snapshots and deltas. */
interface CompactEdge {
  /** source_id */
  s: string;
  /** target_id */
  t: string;
  /** edge_type: 'reply' | 'fork' | 'synthesis' | 'reference' */
  y: string;
}

/** A tree snapshot as received from the server. */
interface TreeSnapshot {
  id: string;
  treeId: string;
  parentHash: string | null;
  hash: string;
  nodeCount: number;
  edgeCount: number;
  createdAt: string;
}

/** Delta between two tree snapshots. */
interface TreeDelta {
  /** Hash of the snapshot this delta applies FROM. */
  fromHash: string;
  /** Hash of the snapshot this delta applies TO (the target state). */
  toHash: string;
  /** Nodes added since fromHash. Keyed by node ID. */
  addedNodes: Record<string, CompactNode>;
  /** Node IDs removed since fromHash. */
  removedNodeIds: string[];
  /** Nodes changed since fromHash. Keyed by node ID, value = changed fields. */
  changedNodes: Record<string, Partial<CompactNode>>;
  /** Edges added since fromHash. Keyed by edge ID. */
  addedEdges: Record<string, CompactEdge>;
  /** Edge IDs removed since fromHash. */
  removedEdgeIds: string[];
  /** Total node count in toHash state. */
  nodeCount: number;
  /** Total edge count in toHash state. */
  edgeCount: number;
}
```

### 5.2 Zod Validation Schemas

```typescript
// types/sync.validation.ts
import { z } from 'zod';

export const CompactNodeSchema = z.object({
  s: z.number().int().positive(),
  c: z.string().datetime(),
  p: z.string().uuid().nullable(),
  h: z.string().length(64).regex(/^[0-9a-f]{64}$/),
  f: z.enum(['markdown', 'plain', 'rich']),
  t: z.enum(['message', 'synthesis', 'system']),
});

export const CompactEdgeSchema = z.object({
  s: z.string().uuid(),
  t: z.string().uuid(),
  y: z.enum(['reply', 'fork', 'synthesis', 'reference']),
});

export const TreeDeltaSchema = z.object({
  fromHash: z.string().length(64),
  toHash: z.string().length(64),
  addedNodes: z.record(z.string().uuid(), CompactNodeSchema),
  removedNodeIds: z.array(z.string().uuid()),
  changedNodes: z.record(z.string().uuid(), CompactNodeSchema.partial()),
  addedEdges: z.record(z.string().uuid(), CompactEdgeSchema),
  removedEdgeIds: z.array(z.string().uuid()),
  nodeCount: z.number().int().nonnegative(),
  edgeCount: z.number().int().nonnegative(),
});
```

### 5.3 applyDelta Function (Client-Side)

```typescript
// sync/apply-delta.ts
import { TreeDelta, CompactNode, CompactEdge } from '../types/sync';

interface YjsTreeState {
  nodes: Map<string, CompactNode>;
  edges: Map<string, CompactEdge>;
}

/**
 * Apply a TreeDelta to a local Yjs tree state.
 * Order of operations matters: removals first, then changes, then additions.
 * This ensures no conflicts and maintains the invariant that the state
 * transitions cleanly from fromHash to toHash.
 */
export function applyDelta(state: YjsTreeState, delta: TreeDelta): void {
  // 1. Apply removals first (makes room for additions)
  for (const nodeId of delta.removedNodeIds) {
    state.nodes.delete(nodeId);
  }
  for (const edgeId of delta.removedEdgeIds) {
    state.edges.delete(edgeId);
  }

  // 2. Apply changes to existing nodes
  for (const [nodeId, changes] of Object.entries(delta.changedNodes)) {
    const existing = state.nodes.get(nodeId);
    if (existing) {
      state.nodes.set(nodeId, { ...existing, ...changes });
    }
  }

  // 3. Apply additions
  for (const [nodeId, node] of Object.entries(delta.addedNodes)) {
    state.nodes.set(nodeId, node);
  }
  for (const [edgeId, edge] of Object.entries(delta.addedEdges)) {
    state.edges.set(edgeId, edge);
  }
}

/**
 * Verify that the delta was applied correctly by comparing node/edge counts.
 * Returns true if the local state matches the expected toHash state.
 */
export function verifyDeltaApplication(
  state: YjsTreeState,
  delta: TreeDelta
): boolean {
  return (
    state.nodes.size === delta.nodeCount &&
    state.edges.size === delta.edgeCount
  );
}
```

---

## 6. SHA256 Hash Computation Algorithm

### 6.1 Deterministic Hash Input

The hash is computed over a canonical serialization of the tree state. The algorithm MUST be deterministic — same tree state always produces the same hash, regardless of platform or language.

```
Input: All active nodes + edges for a tree
Step 1: Sort nodes by (tree_id, sequence_num) ascending
Step 2: Sort edges by (tree_id, source_id, target_id, edge_type) ascending
Step 3: Build a canonical byte buffer:
  For each node in sorted order:
    Append: <node_id>:<sequence_num>:<created_at_epoch_ms>:<parent_id>:<content_hash>:<content_format>:<node_type>\n
  For each edge in sorted order:
    Append: <edge_id>:<source_id>:<target_id>:<edge_type>\n
Step 4: Compute SHA256 of the byte buffer
Step 5: Return lowercase hex string (64 characters)
```

### 6.2 Go Implementation

```go
package sync

import (
    "crypto/sha256"
    "fmt"
    "sort"
    "strings"

    "github.com/google/uuid"
)

// nodeDigest represents a node's hashable fields.
type nodeDigest struct {
    ID            uuid.UUID
    SeqNum        int64
    CreatedAtEpoch int64  // milliseconds since Unix epoch
    ParentID      string  // "nil" for root nodes
    ContentHash   string  // SHA256 hex of node content
    ContentFormat string
    NodeType      string
}

// edgeDigest represents an edge's hashable fields.
type edgeDigest struct {
    ID       uuid.UUID
    SourceID uuid.UUID
    TargetID uuid.UUID
    EdgeType string
}

// ComputeSnapshotHash produces a deterministic SHA256 hash of the tree state.
func ComputeSnapshotHash(nodes []nodeDigest, edges []edgeDigest) string {
    // Sort nodes by (seqNum, id) for determinism
    sort.Slice(nodes, func(i, j int) bool {
        if nodes[i].SeqNum != nodes[j].SeqNum {
            return nodes[i].SeqNum < nodes[j].SeqNum
        }
        return nodes[i].ID.String() < nodes[j].ID.String()
    })

    // Sort edges by (source, target, type, id)
    sort.Slice(edges, func(i, j int) bool {
        if edges[i].SourceID != edges[j].SourceID {
            return edges[i].SourceID.String() < edges[j].SourceID.String()
        }
        if edges[i].TargetID != edges[j].TargetID {
            return edges[i].TargetID.String() < edges[j].TargetID.String()
        }
        if edges[i].EdgeType != edges[j].EdgeType {
            return edges[i].EdgeType < edges[j].EdgeType
        }
        return edges[i].ID.String() < edges[j].ID.String()
    })

    var sb strings.Builder
    for _, n := range nodes {
        sb.WriteString(fmt.Sprintf("%s:%d:%d:%s:%s:%s:%s\n",
            n.ID.String(), n.SeqNum, n.CreatedAtEpoch,
            n.ParentID, n.ContentHash, n.ContentFormat, n.NodeType,
        ))
    }
    for _, e := range edges {
        sb.WriteString(fmt.Sprintf("%s:%s:%s:%s\n",
            e.ID.String(), e.SourceID.String(), e.TargetID.String(), e.EdgeType,
        ))
    }

    sum := sha256.Sum256([]byte(sb.String()))
    return fmt.Sprintf("%x", sum)
}
```

### 6.3 Hash Stability Properties

| Property | Guarantee |
|----------|-----------|
| Same tree state → same hash | ✅ Yes — all inputs sorted canonically |
| Different node content → different hash | ✅ Yes — content_hash is part of node digest |
| Insertion order independence | ✅ Yes — sorted by sequence_num, not insertion time |
| Cross-language agreement | ✅ Yes — Go and TypeScript implementations produce identical hashes for same input |
| Soft-deleted nodes excluded | ✅ Yes — only `deleted_at IS NULL` nodes/edges are hashed |
| Metadata changes → different hash | ⚠️ Partial — metadata is NOT in content_hash. Structural changes (added/removed nodes/edges) affect hash. Metadata-only changes do NOT change hash. This is intentional: metadata is UI sugar, not collaborative state. |

---

## 7. Delta Computation Algorithm

### 7.1 Go Server-Side

```go
package sync

import (
    "context"
    "fmt"

    "github.com/google/uuid"
)

// TreeDelta as computed server-side.
type TreeDelta struct {
    FromHash       string                       `json:"fromHash"`
    ToHash         string                       `json:"toHash"`
    AddedNodes     map[uuid.UUID]CompactNode    `json:"addedNodes"`
    RemovedNodeIDs []uuid.UUID                  `json:"removedNodeIds"`
    ChangedNodes   map[uuid.UUID]CompactNode    `json:"changedNodes"`
    AddedEdges     map[uuid.UUID]CompactEdge    `json:"addedEdges"`
    RemovedEdgeIDs []uuid.UUID                  `json:"removedEdgeIds"`
    NodeCount      int                          `json:"nodeCount"`
    EdgeCount      int                          `json:"edgeCount"`
}

// CompactNode sent in deltas.
type CompactNode struct {
    SeqNum        int64  `json:"s"`
    CreatedAt     string `json:"c"`    // ISO 8601
    ParentID      string `json:"p"`    // "nil" for root
    ContentHash   string `json:"h"`
    ContentFormat string `json:"f"`
    NodeType      string `json:"t"`
}

// CompactEdge sent in deltas.
type CompactEdge struct {
    SourceID string `json:"s"`
    TargetID string `json:"t"`
    EdgeType string `json:"y"`
}

// ComputeDelta calculates the delta between two snapshots.
// fromHash: the client's last-known snapshot hash.
// toHash:   the server's current snapshot hash (typically latest).
// If fromHash == "" (first sync), returns all nodes/edges as added.
func ComputeDelta(fromSnapshot, toSnapshot *db.TreeSnapshot, fromNodes, toNodes []nodeDigest, fromEdges, toEdges []edgeDigest) (*TreeDelta, error) {
    fromHash := ""
    if fromSnapshot != nil {
        fromHash = fromSnapshot.Hash
    }

    // Build lookup maps
    fromNodeMap := make(map[uuid.UUID]nodeDigest, len(fromNodes))
    for _, n := range fromNodes {
        fromNodeMap[n.ID] = n
    }
    toNodeMap := make(map[uuid.UUID]nodeDigest, len(toNodes))
    for _, n := range toNodes {
        toNodeMap[n.ID] = n
    }
    fromEdgeMap := make(map[uuid.UUID]edgeDigest, len(fromEdges))
    for _, e := range fromEdges {
        fromEdgeMap[e.ID] = e
    }
    toEdgeMap := make(map[uuid.UUID]edgeDigest, len(toEdges))
    for _, e := range toEdges {
        toEdgeMap[e.ID] = e
    }

    delta := &TreeDelta{
        FromHash:   fromHash,
        ToHash:     toSnapshot.Hash,
        NodeCount:  toSnapshot.NodeCount,
        EdgeCount:  toSnapshot.EdgeCount,
    }

    // Nodes: find added, removed, changed
    delta.AddedNodes = make(map[uuid.UUID]CompactNode)
    delta.ChangedNodes = make(map[uuid.UUID]CompactNode)

    for id, toNode := range toNodeMap {
        fromNode, existed := fromNodeMap[id]
        if !existed {
            delta.AddedNodes[id] = toCompactNode(toNode)
        } else if nodeChanged(fromNode, toNode) {
            delta.ChangedNodes[id] = toCompactNode(toNode)
        }
    }
    for id := range fromNodeMap {
        if _, stillExists := toNodeMap[id]; !stillExists {
            delta.RemovedNodeIDs = append(delta.RemovedNodeIDs, id)
        }
    }

    // Edges: find added, removed
    delta.AddedEdges = make(map[uuid.UUID]CompactEdge)
    for id, toEdge := range toEdgeMap {
        if _, existed := fromEdgeMap[id]; !existed {
            delta.AddedEdges[id] = toCompactEdge(toEdge)
        }
    }
    for id := range fromEdgeMap {
        if _, stillExists := toEdgeMap[id]; !stillExists {
            delta.RemovedEdgeIDs = append(delta.RemovedEdgeIDs, id)
        }
    }

    // If fromHash is empty (first sync), everything is "added"
    // and counts should reflect the full tree
    if fromHash == "" {
        delta.NodeCount = len(toNodes)
        delta.EdgeCount = len(toEdges)
    }

    return delta, nil
}

func toCompactNode(n nodeDigest) CompactNode {
    parentID := "nil"
    if n.ParentID != "nil" {
        parentID = n.ParentID
    }
    return CompactNode{
        SeqNum:        n.SeqNum,
        CreatedAt:     fmt.Sprintf("%d", n.CreatedAtEpoch),
        ParentID:      parentID,
        ContentHash:   n.ContentHash,
        ContentFormat: n.ContentFormat,
        NodeType:      n.NodeType,
    }
}

func toCompactEdge(e edgeDigest) CompactEdge {
    return CompactEdge{
        SourceID: e.SourceID.String(),
        TargetID: e.TargetID.String(),
        EdgeType: e.EdgeType,
    }
}

func nodeChanged(a, b nodeDigest) bool {
    return a.ContentHash != b.ContentHash ||
        a.ContentFormat != b.ContentFormat ||
        a.NodeType != b.NodeType ||
        a.ParentID != b.ParentID
}
```

### 7.2 Delta Computation Flow

```
Client connects: GET /trees/{tree_id}/events
  Header: Last-Known-Hash: <sha256>

Server:
  1. Look up latest snapshot for tree_id
  2. If Last-Known-Hash header is empty:
     → Return full tree as delta (all nodes/edges as "added")
     → Include current hash in response header: X-Tree-Hash: <sha256>
  3. If Last-Known-Hash matches latest snapshot hash:
     → Return empty delta (no changes). 204 No Content.
  4. Look up snapshot for Last-Known-Hash:
     a. Found → ComputeDelta(from_snapshot, latest_snapshot) → SSE stream delta events
     b. Not found (compacted away) → Send full tree as delta
  5. After sending delta, client's hash is updated to latest hash
```

### 7.3 SSE Event Stream Format

```
event: delta
data: {"type":"node_added","nodeId":"<uuid>","node":{"s":1,"c":"<iso>","p":null,"h":"<sha256>","f":"markdown","t":"message"}}

event: delta
data: {"type":"edge_added","edgeId":"<uuid>","edge":{"s":"<source>","t":"<target>","y":"reply"}}

event: delta
data: {"type":"node_removed","nodeId":"<uuid>"}

event: snapshot
data: {"hash":"<sha256>","nodeCount":42,"edgeCount":41}

event: heartbeat
data: {"ts":"<iso>"}
```

### 7.4 Full Sync Fallback (Hash Not Found)

When the client's `Last-Known-Hash` is not found in `tree_snapshots` (compacted away):

1. Send a `snapshot_reset` event to indicate the client should clear local state
2. Stream all current nodes as `node_added` events
3. Stream all current edges as `edge_added` events
4. Send final `snapshot` event with current hash

This is a full tree resync, not a delta. Bandwidth-intensive but correct.

---

## 8. Wiring

### 8.1 Migrations

```
migrations/
├── 000006_snapshots.up.sql
├── 000006_snapshots.down.sql
├── 000007_node_content_hash.up.sql
├── 000007_node_content_hash.down.sql
├── 000008_tree_events.up.sql
└── 000008_tree_events.down.sql
```

### 8.2 Config (Environment Variables)

| Env Var | Type | Default | Description |
|---------|------|---------|-------------|
| `CANOPY_SNAPSHOT_ON_COMMIT` | bool | true | Create snapshot after every tree mutation |
| `CANOPY_SNAPSHOT_COMPACT_INTERVAL` | duration | 1h | How often to run snapshot compaction |
| `CANOPY_SNAPSHOT_RETENTION_HOURLY` | duration | 24h | Keep hourly snapshots for this period |
| `CANOPY_SNAPSHOT_RETENTION_DAILY` | duration | 720h | Keep daily snapshots for this period (30d) |
| `CANOPY_DELTA_MAX_EVENTS` | int | 1000 | Max events in a single delta SSE batch |

### 8.3 main.go Wiring Addition

```go
// In main.go, after database initialization:
snapshotRepo := db.NewSnapshotRepo(database.Pool)
eventRepo := db.NewEventRepo(database.Pool)

// Start snapshot compactor goroutine
compactor := sync.NewSnapshotCompactor(snapshotRepo, sync.CompactorConfig{
    CompactInterval: cfg.SnapshotCompactInterval,
    RetentionHourly: cfg.SnapshotRetentionHourly,
    RetentionDaily:  cfg.SnapshotRetentionDaily,
})
go compactor.Run(ctx)

// Pass repos to sync engine (used by SSE hub)
syncEngine := sync.NewEngine(snapshotRepo, eventRepo)
```

---

## 9. Error Catalog

| Error Code | Condition | HTTP Status | Details |
|-----------|-----------|-------------|---------|
| `SNAPSHOT_NOT_FOUND` | Hash doesn't correspond to any stored snapshot | 404 | `{error: "snapshot not found", code: "SNAPSHOT_NOT_FOUND"}` |
| `SNAPSHOT_CREATE_FAILED` | Snapshot computation failed (DB error) | 500 | `{error: "failed to create snapshot", code: "SNAPSHOT_CREATE_FAILED"}` |
| `DELTA_COMPUTE_FAILED` | Delta computation failed (missing nodes, corrupt data) | 500 | `{error: "delta computation failed", code: "DELTA_COMPUTE_FAILED"}` |
| `HASH_MISMATCH` | Client hash doesn't match any known snapshot (full resync required) | 409 | `{error: "hash not recognized, full sync required", code: "HASH_MISMATCH"}` |
| `DELTA_TOO_LARGE` | Delta exceeds max events limit (use pagination) | 413 | `{error: "delta exceeds maximum event count", code: "DELTA_TOO_LARGE"}` |
| `COMPACTION_IN_PROGRESS` | Snapshot compaction is running, retry later | 503 | `{error: "compaction in progress, retry shortly", code: "COMPACTION_IN_PROGRESS"}` |

---

## 10. Edge Cases

| Edge Case | Behavior |
|-----------|----------|
| **Empty tree (no nodes)** | Hash is SHA256 of empty string. Delta from empty to populated = all nodes added. |
| **First sync (no Last-Known-Hash)** | Client receives full tree as delta. All nodes/edges in `addedNodes`/`addedEdges`. |
| **Hash not found (compacted away)** | Server sends `HASH_MISMATCH` with full tree resync. Client clears local state and replays. |
| **No changes since last sync** | Server returns 204 No Content. Client keeps current hash. |
| **100,000+ node tree** | Delta computation walks all nodes. Time complexity O(N). At 100K nodes, delta compute <50ms with indexed maps. Full sync streams in batches of 1000 events. |
| **Concurrent snapshot creation** | Two writes to the same tree within the same millisecond: first write creates snapshot, second write sees the new snapshot and creates another. No conflict — snapshots are append-only. |
| **Node deleted then re-added with same ID** | Snapshot tracks by ID. If a node is deleted and re-added (different content_hash), delta reports it as `changedNodes` (existed in from, exists in to, different hash). Actually removed+added, but collapsed to "changed" for efficiency. |
| **Snapshot compaction while client syncing** | Compaction only deletes snapshots older than retention. Client's hash is always the latest snapshot → never compacted. Only stale clients (offline >retention period) need full resync. |
| **Metadata-only node change** | Not detected by hash (metadata is NOT part of content_hash). This is intentional — metadata is UI-only. Structural changes are tracked. |
| **Hash collision (SHA256)** | Theoretically possible (2^256 space). Practically impossible. Not handled — if it happens, the universe has bigger problems. |
| **Parent hash chain breaks** | If snapshot.parent_hash points to a compacted snapshot, the chain is broken. Delta computation falls back to full tree scan using snapshot_data JSONB. Parent hash is advisory, not required. |

---

## 11. Testing

### 11.1 Go Unit Tests

```
TestComputeSnapshotHash:
  - Same nodes+edges → same hash
  - Different node content → different hash
  - Different edge set → different hash
  - Different insertion order → same hash (canonical sort)
  - Empty tree → deterministic hash (SHA256 of "")

TestComputeDelta:
  - Empty from → all nodes/edges in added
  - Same state → empty delta (no changes)
  - Node added → in addedNodes
  - Node removed → in removedNodeIds
  - Node content changed → in changedNodes
  - Edge added/removed → in addedEdges/removedEdgeIds
  - Mixed changes → all categories populated

TestSnapshotRepo:
  - CreateSnapshot: stores correct hash, node_count, edge_count
  - GetSnapshot by hash: returns correct snapshot
  - GetLatestSnapshot: returns most recent
  - GetSnapshotChain: returns chronological chain
  - CompactSnapshots: reduces count, keeps latest per period
  - DeleteSnapshotsBefore: removes old snapshots

TestEventRepo:
  - AppendEvent: writes with auto-incrementing sequence_num
  - GetEventsSince: returns events after sequence_num
  - GetEventsBetweenSnapshots: returns correct range
  - GetLatestSequenceNum: returns highest sequence

TestHashStability:
  - Go hash == TypeScript hash (pre-computed test vectors)
  - Cross-version stability (store test vectors in testdata/)
```

### 11.2 TypeScript Unit Tests

```
describe('applyDelta', () => {
  it('adds new nodes to state');
  it('removes deleted nodes from state');
  it('updates changed nodes in state');
  it('adds new edges to state');
  it('removes deleted edges from state');
  it('handles empty delta (no changes)');
  it('handles full tree delta (all nodes/edges added)');
  it('preserves untouched nodes through delta application');
});

describe('verifyDeltaApplication', () => {
  it('returns true for correct application');
  it('returns false if node count mismatches');
  it('returns false if edge count mismatches');
});
```

---

## 12. Performance

### 12.1 Complexity

| Operation | Time | Space | Notes |
|-----------|------|-------|-------|
| ComputeSnapshotHash | O(N+E) | O(N+E) | Linear scan over sorted nodes+edges |
| ComputeDelta | O(N+E) | O(N+E) | Map-based diff between two snapshots |
| CreateSnapshot | O(N+E) | O(N+E) for hash + O(1) for DB insert | Bulk insert via pgx COPY protocol |
| applyDelta (client) | O(A+C+R) | O(A) | A=added, C=changed, R=removed |
| Full sync (10K nodes) | ~50ms server | ~200KB JSONB | Server streams in batches |
| Delta (10 changes in 10K tree) | ~5ms server | ~2KB JSONB | Only changed entities |
| Compaction | O(S) per tree | O(1) | S=snapshots being compacted |

### 12.2 Benchmarks (Targets)

| Benchmark | Target |
|-----------|--------|
| Hash 10K nodes + 10K edges | <10ms |
| Delta 10K nodes (10 changes) | <5ms |
| Delta 10K nodes (1000 changes) | <50ms |
| Full snapshot creation (10K nodes) | <100ms |
| SSE delta stream start latency | <50ms from connection to first event |
