# SPEC-DM-01 — Tree Node & Edge DDL

> **Status:** Spec | **Blocks:** SPEC-DM-02, SPEC-DM-03, SPEC-DM-04, Phase 3, Phase 4

---

## 1. Purpose

Define the exact PostgreSQL DDL, Go structs, TypeScript types, and Yjs CRDT schema for Canopy's tree nodes and edges. A worker reading this spec must produce the correct database layer, Go repository, TypeScript types, and CRDT provider with zero clarifying questions.

---

## 2. Design Decisions (from ARCHITECTURE.md)

| Decision | Choice | Source |
|----------|--------|--------|
| Data model | DAG (nodes + typed edges) | ARCHITECTURE.md §3.1 |
| UI metaphor | Tree (hierarchical) | ARCHITECTURE.md §3.1 |
| Multi-parent | Allowed for synthesis/merge nodes only | ARCHITECTURE.md §3.1 |
| Authoritative DB | PostgreSQL 17+ | ARCHITECTURE.md §2.3 |
| Driver | pgx v5 | ARCHITECTURE.md §2.1 |
| Migrations | golang-migrate | ARCHITECTURE.md §2.1 |
| CRDT | Yjs 13.6.x — Y.Map | ARCHITECTURE.md §3.3 |
| IDs | UUIDv7 (time-ordered) | tasks.md SPEC-DM-01 |
| Single-parent | Default. Multi-parent only on synthesis nodes | ARCHITECTURE.md §3.1 |
| Soft-delete | `deleted_at` column | tasks.md SPEC-DM-01 |
| owner_id | Present from MVP day one | SPEC-FTR-01 pattern (ARCHITECTURE.md §8, §11 deferred) |

---

## 3. PostgreSQL DDL

### 3.1 UUIDv7 Generation Function

```sql
-- pg_uuidv7: UUIDv7 generator (time-ordered, RFC 9562)
-- Install via: CREATE EXTENSION IF NOT EXISTS pg_uuidv7;
-- Fallback if extension unavailable (pure SQL implementation below):

CREATE OR REPLACE FUNCTION uuidv7() RETURNS uuid AS $$
DECLARE
    ts_ms  bigint;
    rand_a bigint;
    rand_b bigint;
BEGIN
    -- Milliseconds since Unix epoch
    ts_ms := (extract(epoch FROM clock_timestamp()) * 1000)::bigint;

    -- 48-bit timestamp → bytes 0-5
    -- 4-bit version (7) → byte 6, high nibble
    -- 12-bit rand_a → byte 6 low nibble + byte 7
    -- 62-bit rand_b → bytes 8-15
    -- 2-bit variant (10) → byte 8, high 2 bits
    rand_a := (random() * 4096)::bigint;          -- 12 bits
    rand_b := (random() * 4611686018427387904)::bigint; -- 62 bits

    RETURN encode(
        set_byte(set_byte(
            substring(int8send(ts_ms << 16), 1, 6) ||
            substring(int8send((7::bigint << 12) | rand_a), 1, 2) ||
            substring(int8send((2::bigint << 62) | rand_b), 1, 8)
        , 6, (get_byte(substring(int8send((7::bigint << 12) | rand_a), 1, 2), 0) & 0x0f) | 0x70)
        , 8, (get_byte(substring(int8send((2::bigint << 62) | rand_b), 1, 8), 0) & 0x3f) | 0x80)
    , 'hex')::uuid;
END;
$$ LANGUAGE plpgsql VOLATILE;
```

**Preferred path:** Use `pg_uuidv7` extension. The above function is a fallback for environments where extensions aren't available. The Go migration should try `CREATE EXTENSION IF NOT EXISTS pg_uuidv7` first; if that fails, create the fallback function.

### 3.2 Extension Setup

```sql
-- 000001_extensions.up.sql
CREATE EXTENSION IF NOT EXISTS "pgcrypto";   -- gen_random_bytes() for entropy
-- Try pg_uuidv7 extension; fallback handled in Go migration code
DO $$ BEGIN
    CREATE EXTENSION IF NOT EXISTS "pg_uuidv7";
EXCEPTION WHEN OTHERS THEN
    -- Extension not available — fallback uuidv7() function created in next migration
    RAISE NOTICE 'pg_uuidv7 extension not available, using fallback uuidv7() function';
END $$;
```

### 3.3 Nodes Table

```sql
-- 000002_nodes.up.sql

CREATE TABLE nodes (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL,
    parent_id       uuid        REFERENCES nodes(id) ON DELETE SET NULL,
    author_id       uuid        NOT NULL,              -- FK to profiles table (SPEC-DM-04)
    content         text        NOT NULL DEFAULT '',
    content_format  text        NOT NULL DEFAULT 'markdown',  -- 'markdown' | 'plain' | 'rich'
    node_type       text        NOT NULL DEFAULT 'message',    -- 'message' | 'synthesis' | 'system'
    sequence_num    bigint      NOT NULL,              -- Monotonic within tree_id for ordering
    metadata        jsonb       NOT NULL DEFAULT '{}', -- Arbitrary key-value (plugin data, card refs)
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    edited_at       timestamptz,
    deleted_at      timestamptz,                       -- Soft delete (NULL = active)
    CONSTRAINT fk_nodes_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id)
        ON DELETE CASCADE,
    CONSTRAINT chk_content_format
        CHECK (content_format IN ('markdown', 'plain', 'rich')),
    CONSTRAINT chk_node_type
        CHECK (node_type IN ('message', 'synthesis', 'system'))
);

-- Indexes
CREATE INDEX idx_nodes_tree_id        ON nodes(tree_id);
CREATE INDEX idx_nodes_tree_parent    ON nodes(tree_id, parent_id);
CREATE INDEX idx_nodes_tree_created   ON nodes(tree_id, created_at);
CREATE INDEX idx_nodes_tree_sequence  ON nodes(tree_id, sequence_num);
CREATE INDEX idx_nodes_author         ON nodes(author_id);
CREATE INDEX idx_nodes_deleted        ON nodes(tree_id) WHERE deleted_at IS NOT NULL;
-- Full-text search index on content
CREATE INDEX idx_nodes_content_fts    ON nodes USING gin(to_tsvector('english', content));
```

### 3.4 Edges Table

```sql
-- 000003_edges.up.sql

CREATE TABLE edges (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL,
    source_id       uuid        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    target_id       uuid        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    edge_type       text        NOT NULL DEFAULT 'reply',  -- 'reply' | 'fork' | 'synthesis' | 'reference'
    sequence_num    bigint      NOT NULL,                  -- Order among siblings from same source
    metadata        jsonb       NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at      timestamptz,                           -- Soft delete
    CONSTRAINT fk_edges_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id)
        ON DELETE CASCADE,
    CONSTRAINT chk_edge_type
        CHECK (edge_type IN ('reply', 'fork', 'synthesis', 'reference')),
    CONSTRAINT chk_no_self_edge
        CHECK (source_id != target_id),
    CONSTRAINT chk_unique_edge
        UNIQUE (source_id, target_id, edge_type)
);

-- Indexes
CREATE INDEX idx_edges_tree_id        ON edges(tree_id);
CREATE INDEX idx_edges_source         ON edges(source_id);
CREATE INDEX idx_edges_target         ON edges(target_id);
CREATE INDEX idx_edges_tree_source    ON edges(tree_id, source_id);
CREATE INDEX idx_edges_tree_target    ON edges(tree_id, target_id);
CREATE INDEX idx_edges_type           ON edges(tree_id, edge_type);
```

### 3.5 Single-Parent Constraint (Enforced in Application, Not DDL)

PostgreSQL cannot express "at most one parent for non-synthesis nodes" as a declarative constraint because it involves a cross-table condition (check `nodes.node_type` when inserting into `edges`). This is enforced in Go:

```go
// In EdgeRepo.Create():
// 1. Fetch target node's node_type
// 2. If node_type != 'synthesis', count existing edges WHERE target_id = $1 AND deleted_at IS NULL
// 3. If count > 0, return ErrMultipleParents
// 4. Otherwise, proceed with INSERT
```

### 3.6 Sequence Number Triggers

```sql
-- Auto-increment sequence_num within tree scope for nodes
CREATE OR REPLACE FUNCTION set_node_sequence() RETURNS trigger AS $$
BEGIN
    SELECT COALESCE(MAX(sequence_num), 0) + 1
    INTO NEW.sequence_num
    FROM nodes
    WHERE tree_id = NEW.tree_id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_node_sequence
    BEFORE INSERT ON nodes
    FOR EACH ROW
    WHEN (NEW.sequence_num IS NULL)
    EXECUTE FUNCTION set_node_sequence();

-- Auto-increment sequence_num within (tree_id, source_id) scope for edges
CREATE OR REPLACE FUNCTION set_edge_sequence() RETURNS trigger AS $$
BEGIN
    SELECT COALESCE(MAX(sequence_num), 0) + 1
    INTO NEW.sequence_num
    FROM edges
    WHERE tree_id = NEW.tree_id AND source_id = NEW.source_id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_edge_sequence
    BEFORE INSERT ON edges
    FOR EACH ROW
    WHEN (NEW.sequence_num IS NULL)
    EXECUTE FUNCTION set_edge_sequence();
```

### 3.7 edited_at Trigger

```sql
-- Auto-set edited_at on node update
CREATE OR REPLACE FUNCTION set_edited_at() RETURNS trigger AS $$
BEGIN
    NEW.edited_at = clock_timestamp();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_node_edited_at
    BEFORE UPDATE ON nodes
    FOR EACH ROW
    WHEN (OLD.content IS DISTINCT FROM NEW.content
       OR OLD.metadata IS DISTINCT FROM NEW.metadata)
    EXECUTE FUNCTION set_edited_at();
```

### 3.8 Trees Table (Minimal — Referenced by Nodes)

```sql
-- 000004_trees.up.sql

CREATE TABLE trees (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    owner_id        uuid        NOT NULL,                  -- FK to profiles (SPEC-DM-04)
    title           text        NOT NULL DEFAULT '',
    description     text        NOT NULL DEFAULT '',
    root_node_id    uuid        REFERENCES nodes(id) ON DELETE SET NULL,
    metadata        jsonb       NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    edited_at       timestamptz,
    deleted_at      timestamptz,
    CONSTRAINT chk_tree_title CHECK (char_length(title) <= 500)
);

CREATE INDEX idx_trees_owner ON trees(owner_id);
```

**Note:** `root_node_id` is nullable during tree creation — the root node is created in a transaction after the tree row.

---

## 4. Go Structs & Repository Interfaces

### 4.1 Package Layout

```
internal/
├── db/
│   ├── db.go              # Connection pool (pgxpool), migration runner
│   ├── models.go          # Go structs with db tags
│   ├── node_repo.go       # NodeRepo interface + pgx implementation
│   ├── edge_repo.go       # EdgeRepo interface + pgx implementation
│   └── tree_repo.go       # TreeRepo interface + pgx implementation
```

### 4.2 Go Structs

```go
package db

import (
    "time"
    "github.com/google/uuid"
)

// Node represents a single node (message) in a conversation tree.
// Maps to the `nodes` table.
type Node struct {
    ID            uuid.UUID  `db:"id"             json:"id"`
    TreeID        uuid.UUID  `db:"tree_id"        json:"treeId"`
    ParentID      *uuid.UUID `db:"parent_id"      json:"parentId"`       // NULL for root nodes
    AuthorID      uuid.UUID  `db:"author_id"      json:"authorId"`
    Content       string     `db:"content"        json:"content"`
    ContentFormat string     `db:"content_format" json:"contentFormat"`  // "markdown" | "plain" | "rich"
    NodeType      string     `db:"node_type"      json:"nodeType"`       // "message" | "synthesis" | "system"
    SequenceNum   int64      `db:"sequence_num"   json:"sequenceNum"`
    Metadata      []byte     `db:"metadata"       json:"metadata"`       // JSONB → []byte (use json.RawMessage for marshaling)
    CreatedAt     time.Time  `db:"created_at"     json:"createdAt"`
    EditedAt      *time.Time `db:"edited_at"      json:"editedAt"`
    DeletedAt     *time.Time `db:"deleted_at"     json:"deletedAt"`
}

// Edge represents a typed directed edge between two nodes.
// Maps to the `edges` table.
type Edge struct {
    ID          uuid.UUID  `db:"id"           json:"id"`
    TreeID      uuid.UUID  `db:"tree_id"      json:"treeId"`
    SourceID    uuid.UUID  `db:"source_id"    json:"sourceId"`
    TargetID    uuid.UUID  `db:"target_id"    json:"targetId"`
    EdgeType    string     `db:"edge_type"    json:"edgeType"`     // "reply" | "fork" | "synthesis" | "reference"
    SequenceNum int64      `db:"sequence_num" json:"sequenceNum"`
    Metadata    []byte     `db:"metadata"     json:"metadata"`
    CreatedAt   time.Time  `db:"created_at"   json:"createdAt"`
    DeletedAt   *time.Time `db:"deleted_at"   json:"deletedAt"`
}

// Tree represents a conversation tree container.
// Maps to the `trees` table.
type Tree struct {
    ID          uuid.UUID  `db:"id"            json:"id"`
    OwnerID     uuid.UUID  `db:"owner_id"      json:"ownerId"`
    Title       string     `db:"title"         json:"title"`
    Description string     `db:"description"   json:"description"`
    RootNodeID  *uuid.UUID `db:"root_node_id"  json:"rootNodeId"`
    Metadata    []byte     `db:"metadata"      json:"metadata"`
    CreatedAt   time.Time  `db:"created_at"    json:"createdAt"`
    EditedAt    *time.Time `db:"edited_at"     json:"editedAt"`
    DeletedAt   *time.Time `db:"deleted_at"    json:"deletedAt"`
}

// NodeCounts provides aggregate counts for a tree.
type NodeCounts struct {
    TreeID     uuid.UUID `json:"treeId"`
    TotalNodes int64     `json:"totalNodes"`
    ActiveNodes int64    `json:"activeNodes"`   // deleted_at IS NULL
    TotalEdges int64     `json:"totalEdges"`
    ActiveEdges int64    `json:"activeEdges"`
    MaxDepth   int       `json:"maxDepth"`
}
```

### 4.3 Repository Interfaces

```go
package db

import (
    "context"
    "github.com/google/uuid"
)

// NodeRepo handles CRUD operations on nodes.
type NodeRepo interface {
    // Create inserts a new node. Returns the created node with generated fields.
    Create(ctx context.Context, node *Node) (*Node, error)

    // GetByID retrieves a node by ID. Returns nil if not found or soft-deleted.
    GetByID(ctx context.Context, id uuid.UUID) (*Node, error)

    // GetByTree returns all active nodes in a tree, ordered by sequence_num.
    GetByTree(ctx context.Context, treeID uuid.UUID) ([]Node, error)

    // GetChildren returns active child nodes for a given parent, ordered by edge sequence_num.
    GetChildren(ctx context.Context, parentID uuid.UUID) ([]Node, error)

    // GetAncestors returns the chain from root to the given node (inclusive).
    GetAncestors(ctx context.Context, nodeID uuid.UUID) ([]Node, error)

    // GetSubtree returns all nodes in the subtree rooted at the given node, up to maxDepth.
    // maxDepth=0 means no limit.
    GetSubtree(ctx context.Context, rootID uuid.UUID, maxDepth int) ([]Node, error)

    // GetPath returns nodes on the path between two nodes (inclusive).
    GetPath(ctx context.Context, fromID, toID uuid.UUID) ([]Node, error)

    // Update modifies an existing node's content and/or metadata. Sets edited_at.
    Update(ctx context.Context, id uuid.UUID, content string, metadata []byte) (*Node, error)

    // SoftDelete marks a node and its outgoing edges as deleted.
    SoftDelete(ctx context.Context, id uuid.UUID) error

    // HardDelete permanently removes a node. Caller must verify no active children.
    HardDelete(ctx context.Context, id uuid.UUID) error

    // GetCounts returns aggregate counts for a tree.
    GetCounts(ctx context.Context, treeID uuid.UUID) (*NodeCounts, error)
}

// EdgeRepo handles CRUD operations on edges.
type EdgeRepo interface {
    // Create inserts a new edge. Enforces single-parent constraint (see §3.5).
    Create(ctx context.Context, edge *Edge) (*Edge, error)

    // GetByID retrieves an edge by ID.
    GetByID(ctx context.Context, id uuid.UUID) (*Edge, error)

    // GetBySource returns all active edges from a given source node.
    GetBySource(ctx context.Context, sourceID uuid.UUID) ([]Edge, error)

    // GetByTarget returns all active edges pointing to a given target node.
    GetByTarget(ctx context.Context, targetID uuid.UUID) ([]Edge, error)

    // GetByTree returns all active edges in a tree.
    GetByTree(ctx context.Context, treeID uuid.UUID) ([]Edge, error)

    // SoftDelete marks an edge as deleted.
    SoftDelete(ctx context.Context, id uuid.UUID) error

    // GetParents returns all source nodes that have active edges to the target.
    // Used to check single-parent constraint.
    GetParents(ctx context.Context, targetID uuid.UUID) ([]uuid.UUID, error)
}

// TreeRepo handles CRUD operations on trees.
type TreeRepo interface {
    // Create inserts a new tree. root_node_id is set later when the root node is created.
    Create(ctx context.Context, tree *Tree) (*Tree, error)

    // GetByID retrieves a tree by ID.
    GetByID(ctx context.Context, id uuid.UUID) (*Tree, error)

    // ListByOwner returns all active trees owned by a given profile.
    ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]Tree, error)

    // Update modifies a tree's title, description, or metadata.
    Update(ctx context.Context, id uuid.UUID, title, description string, metadata []byte) (*Tree, error)

    // SetRootNode sets the root node after creation.
    SetRootNode(ctx context.Context, treeID, rootNodeID uuid.UUID) error

    // SoftDelete marks a tree and all its nodes/edges as deleted.
    SoftDelete(ctx context.Context, id uuid.UUID) error
}
```

### 4.4 pgx Implementation Notes

```go
// db.go — connection pool setup
package db

import (
    "context"
    "github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
    Pool *pgxpool.Pool
}

func NewDB(ctx context.Context, dsn string) (*DB, error) {
    cfg, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return nil, fmt.Errorf("parse config: %w", err)
    }
    cfg.MaxConns = 25
    cfg.MinConns = 5
    pool, err := pgxpool.NewWithConfig(ctx, cfg)
    if err != nil {
        return nil, fmt.Errorf("connect: %w", err)
    }
    if err := pool.Ping(ctx); err != nil {
        return nil, fmt.Errorf("ping: %w", err)
    }
    return &DB{Pool: pool}, nil
}

func (db *DB) Close() {
    db.Pool.Close()
}
```

**pgx type mapping:**
- `uuid.UUID` → pgx auto-handles via `github.com/jackc/pgx/v5/pgtype`
- `*time.Time` → pgx handles nullable timestamptz
- `[]byte` → pgx handles jsonb (scans into `[]byte`; use `json.RawMessage` for marshaling/unmarshaling)
- Scan: use `pgxscan` (`github.com/georgysavva/scany/v2/pgxscan`) for struct scanning

---

## 5. TypeScript Types & Zod Validation

### 5.1 TypeScript Interfaces

```typescript
// types/tree.ts

/** Content format for node message bodies */
export type ContentFormat = 'markdown' | 'plain' | 'rich';

/** Node type classification */
export type NodeType = 'message' | 'synthesis' | 'system';

/** Edge relationship type */
export type EdgeType = 'reply' | 'fork' | 'synthesis' | 'reference';

/** A single node (message) in a conversation tree */
export interface Node {
  id: string;                    // UUIDv7
  treeId: string;
  parentId: string | null;       // null for root nodes
  authorId: string;
  content: string;
  contentFormat: ContentFormat;
  nodeType: NodeType;
  sequenceNum: number;
  metadata: Record<string, unknown>;
  createdAt: string;             // ISO 8601
  editedAt: string | null;
  deletedAt: string | null;
}

/** A typed directed edge between two nodes */
export interface Edge {
  id: string;
  treeId: string;
  sourceId: string;
  targetId: string;
  edgeType: EdgeType;
  sequenceNum: number;
  metadata: Record<string, unknown>;
  createdAt: string;
  deletedAt: string | null;
}

/** A conversation tree container */
export interface Tree {
  id: string;
  ownerId: string;
  title: string;
  description: string;
  rootNodeId: string | null;
  metadata: Record<string, unknown>;
  createdAt: string;
  editedAt: string | null;
  deletedAt: string | null;
}

/** Aggregate node counts for a tree */
export interface NodeCounts {
  treeId: string;
  totalNodes: number;
  activeNodes: number;
  totalEdges: number;
  activeEdges: number;
  maxDepth: number;
}
```

### 5.2 Zod Validation Schemas

```typescript
// schemas/tree.ts
import { z } from 'zod';

const uuidv7Pattern = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

export const uuidSchema = z.string().regex(uuidv7Pattern, 'Must be a valid UUIDv7');

export const contentFormatSchema = z.enum(['markdown', 'plain', 'rich']);
export const nodeTypeSchema = z.enum(['message', 'synthesis', 'system']);
export const edgeTypeSchema = z.enum(['reply', 'fork', 'synthesis', 'reference']);

export const metadataSchema = z.record(z.string(), z.unknown()).default({});

export const nodeSchema = z.object({
  id: uuidSchema,
  treeId: uuidSchema,
  parentId: uuidSchema.nullable(),
  authorId: uuidSchema,
  content: z.string().max(100_000, 'Content exceeds 100KB limit'),
  contentFormat: contentFormatSchema.default('markdown'),
  nodeType: nodeTypeSchema.default('message'),
  sequenceNum: z.number().int().nonnegative(),
  metadata: metadataSchema,
  createdAt: z.string().datetime(),
  editedAt: z.string().datetime().nullable(),
  deletedAt: z.string().datetime().nullable(),
});

export const createNodeSchema = z.object({
  treeId: uuidSchema,
  parentId: uuidSchema.nullable(),
  authorId: uuidSchema,
  content: z.string().min(1, 'Content cannot be empty').max(100_000),
  contentFormat: contentFormatSchema.default('markdown'),
  nodeType: nodeTypeSchema.default('message'),
  metadata: metadataSchema,
});

export const updateNodeSchema = z.object({
  content: z.string().min(1).max(100_000),
  metadata: metadataSchema.optional(),
});

export const edgeSchema = z.object({
  id: uuidSchema,
  treeId: uuidSchema,
  sourceId: uuidSchema,
  targetId: uuidSchema,
  edgeType: edgeTypeSchema.default('reply'),
  sequenceNum: z.number().int().nonnegative(),
  metadata: metadataSchema,
  createdAt: z.string().datetime(),
  deletedAt: z.string().datetime().nullable(),
});

export const createEdgeSchema = z.object({
  treeId: uuidSchema,
  sourceId: uuidSchema,
  targetId: uuidSchema,
  edgeType: edgeTypeSchema.default('reply'),
  metadata: metadataSchema,
}).refine(data => data.sourceId !== data.targetId, {
  message: 'Source and target cannot be the same node',
});

export const treeSchema = z.object({
  id: uuidSchema,
  ownerId: uuidSchema,
  title: z.string().max(500),
  description: z.string().max(10_000).default(''),
  rootNodeId: uuidSchema.nullable(),
  metadata: metadataSchema,
  createdAt: z.string().datetime(),
  editedAt: z.string().datetime().nullable(),
  deletedAt: z.string().datetime().nullable(),
});

export const createTreeSchema = z.object({
  ownerId: uuidSchema,
  title: z.string().min(1, 'Title cannot be empty').max(500),
  description: z.string().max(10_000).default(''),
  metadata: metadataSchema,
});

export const nodeCountsSchema = z.object({
  treeId: uuidSchema,
  totalNodes: z.number().int().nonnegative(),
  activeNodes: z.number().int().nonnegative(),
  totalEdges: z.number().int().nonnegative(),
  activeEdges: z.number().int().nonnegative(),
  maxDepth: z.number().int().nonnegative(),
});

// Infer types from schemas (alternative to manual interface declarations)
export type CreateNode = z.infer<typeof createNodeSchema>;
export type UpdateNode = z.infer<typeof updateNodeSchema>;
export type CreateEdge = z.infer<typeof createEdgeSchema>;
export type CreateTree = z.infer<typeof createTreeSchema>;
```

---

## 6. Yjs CRDT Schema

### 6.1 Yjs Document Structure

```typescript
// ydoc/tree-doc.ts
import * as Y from 'yjs';

export interface TreeYDoc {
  /** Y.Map<nodeId, NodeData> — all nodes in the tree */
  nodes: Y.Map<NodeData>;

  /** Y.Map<edgeId, EdgeData> — all edges in the tree */
  edges: Y.Map<EdgeData>;

  /** Y.Array<nodeId> — ordered list of root-level node IDs */
  rootOrder: Y.Array<string>;

  /** Metadata about the Yjs document */
  meta: Y.Map<any>;
}

/** Data stored in Y.Map for each node */
export interface NodeData {
  id: string;
  content: string;
  contentFormat: string;
  nodeType: string;
  authorId: string;
  metadata: Record<string, unknown>;
  createdAt: string;
  editedAt: string | null;
}

/** Data stored in Y.Map for each edge */
export interface EdgeData {
  id: string;
  sourceId: string;
  targetId: string;
  edgeType: string;
  metadata: Record<string, unknown>;
  createdAt: string;
}

export function createTreeYDoc(): TreeYDoc {
  const ydoc = new Y.Doc();
  return {
    nodes: ydoc.getMap('nodes'),
    edges: ydoc.getMap('edges'),
    rootOrder: ydoc.getArray('rootOrder'),
    meta: ydoc.getMap('meta'),
  };
}
```

### 6.2 Yjs Observe Hooks

```typescript
// ydoc/observe.ts
import { TreeYDoc } from './tree-doc';

/**
 * Observe node changes. Fires on add/update/delete of any node.
 * Delta contains { keys, added, updated, removed } per Y.Map event.
 */
export function observeNodes(
  doc: TreeYDoc,
  callback: (event: Y.YMapEvent<any>) => void
): void {
  doc.nodes.observe(callback);
}

/**
 * Observe edge changes. Fires on add/update/delete of any edge.
 */
export function observeEdges(
  doc: TreeYDoc,
  callback: (event: Y.YMapEvent<any>) => void
): void {
  doc.edges.observe(callback);
}

/**
 * Observe changes to the root ordering.
 */
export function observeRootOrder(
  doc: TreeYDoc,
  callback: (event: Y.YArrayEvent<string>) => void
): void {
  doc.rootOrder.observe(callback);
}
```

### 6.3 Yjs ↔ Server Mapping

```
Yjs Y.Map node entry            PostgreSQL nodes row
────────────────────────────────────────────────────
[id] (map key)                  id
content                         content
contentFormat                   content_format
nodeType                        node_type
authorId                        author_id
metadata                        metadata
createdAt                       created_at
editedAt                        edited_at
(deleted → remove from map)     deleted_at (soft delete)
(parentId → edge relationship)  parent_id (denormalized for queries)

Yjs Y.Map edge entry            PostgreSQL edges row
────────────────────────────────────────────────────
[id] (map key)                  id
sourceId                        source_id
targetId                        target_id
edgeType                        edge_type
metadata                        metadata
createdAt                       created_at
```

**Note on tree_id, sequence_num, deleted_at:** These are server-authoritative fields not synced to Yjs. The Yjs document stores only collaboration-relevant data. tree_id is implicit (one Y.Doc per tree). sequence_num is server-generated. deleted_at is server-side soft delete — Yjs uses map deletion for removal.

---

## 7. Wiring

### 7.1 Go — main.go

```go
package main

import (
    "context"
    "fmt"
    "os"
    "hermes-canopy/internal/db"
)

func main() {
    dsn := os.Getenv("CANOPY_DSN")
    if dsn == "" {
        dsn = "postgres://canopy:canopy@localhost:5432/canopy?sslmode=disable"
    }

    database, err := db.NewDB(context.Background(), dsn)
    if err != nil {
        fmt.Fprintf(os.Stderr, "database connect: %v\n", err)
        os.Exit(1)
    }
    defer database.Close()

    // Run migrations
    if err := db.RunMigrations(database.Pool, "file://migrations"); err != nil {
        fmt.Fprintf(os.Stderr, "migrations: %v\n", err)
        os.Exit(1)
    }

    // Wire repos
    nodeRepo := db.NewNodeRepo(database.Pool)
    edgeRepo := db.NewEdgeRepo(database.Pool)
    treeRepo := db.NewTreeRepo(database.Pool)

    // ... pass to HTTP handlers (Phase 4)
}
```

### 7.2 Go Migrations

```
migrations/
├── 000001_extensions.up.sql
├── 000001_extensions.down.sql
├── 000002_nodes.up.sql
├── 000002_nodes.down.sql
├── 000003_edges.up.sql
├── 000003_edges.down.sql
├── 000004_trees.up.sql
├── 000004_trees.down.sql
├── 000005_triggers.up.sql       -- sequence_num + edited_at triggers
└── 000005_triggers.down.sql
```

Run via:
```go
import (
    "github.com/golang-migrate/migrate/v4"
    _ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
    _ "github.com/golang-migrate/migrate/v4/source/file"
)

func RunMigrations(db *pgxpool.Pool, sourceURL string) error {
    // ... standard migrate.Up() pattern
}
```

### 7.3 Config (Environment Variables)

| Env Var | Type | Default | Description |
|---------|------|---------|-------------|
| `CANOPY_DSN` | string | `postgres://canopy:canopy@localhost:5432/canopy?sslmode=disable` | PostgreSQL connection string |
| `CANOPY_DB_MAX_CONNS` | int | 25 | Max pgxpool connections |
| `CANOPY_DB_MIN_CONNS` | int | 5 | Min pgxpool connections |
| `CANOPY_DB_MIGRATIONS_PATH` | string | `file://migrations` | golang-migrate source URL |

---

## 8. Error Catalog

| Error Code | Condition | HTTP Status | Details |
|-----------|-----------|-------------|---------|
| `NODE_NOT_FOUND` | Node ID doesn't exist or is soft-deleted | 404 | `{error: "node not found", code: "NODE_NOT_FOUND"}` |
| `NODE_DELETED` | Operation on soft-deleted node | 410 | `{error: "node has been deleted", code: "NODE_DELETED"}` |
| `INVALID_PARENT` | parent_id references non-existent or deleted node | 400 | `{error: "parent node not found", code: "INVALID_PARENT"}` |
| `MULTIPLE_PARENTS` | Non-synthesis node already has a parent | 409 | `{error: "node already has a parent — only synthesis nodes allow multiple parents", code: "MULTIPLE_PARENTS"}` |
| `SELF_EDGE` | source_id == target_id | 400 | `{error: "cannot create edge to self", code: "SELF_EDGE"}` |
| `TREE_NOT_FOUND` | Tree ID doesn't exist or is soft-deleted | 404 | `{error: "tree not found", code: "TREE_NOT_FOUND"}` |
| `DUPLICATE_EDGE` | Edge (source, target, type) already exists | 409 | `{error: "edge already exists", code: "DUPLICATE_EDGE"}` |
| `CONTENT_TOO_LARGE` | Content exceeds 100KB | 413 | `{error: "content exceeds 100KB limit", code: "CONTENT_TOO_LARGE"}` |
| `EMPTY_CONTENT` | Content is empty string | 400 | `{error: "content cannot be empty", code: "EMPTY_CONTENT"}` |
| `TITLE_TOO_LONG` | Tree title exceeds 500 chars | 400 | `{error: "title exceeds 500 characters", code: "TITLE_TOO_LONG"}` |
| `INVALID_UUID` | ID is not a valid UUIDv7 | 400 | `{error: "invalid UUID format", code: "INVALID_UUID"}` |
| `TREE_SIZE_EXCEEDED` | Tree exceeds max nodes (1M by default) | 413 | `{error: "tree exceeds maximum node count", code: "TREE_SIZE_EXCEEDED"}` |
| `HARD_DELETE_WITH_CHILDREN` | Attempting hard-delete on node with active children | 409 | `{error: "node has active children, soft-delete first", code: "HARD_DELETE_WITH_CHILDREN"}` |

---

## 9. Edge Cases

| Edge Case | Behavior |
|-----------|----------|
| **Empty content** | Reject with EMPTY_CONTENT error. Enforced by CHECK and Go validation. |
| **Nil parent_id** | Root node. Allowed only during tree creation (first node). All subsequent nodes should have a parent. |
| **parent_id = deleted node** | Reject with INVALID_PARENT. Can't attach to deleted parent. |
| **Concurrent node creation** | `sequence_num` uses `SELECT MAX + 1` — races result in duplicate sequence_nums. Mitigation: retry on unique violation, or use `SERIAL`/`IDENTITY` per tree (requires partitioning). Tradeoff: simplicity. If contention is high (100+ concurrent writers to same tree), batch inserts in transaction with `SELECT ... FOR UPDATE` on the tree row. |
| **10,000+ node tree** | Indexes handle queries. `GetChildren` on root returns 10K rows — add pagination (`LIMIT/OFFSET`) at API layer. |
| **Circular edges** | Impossible because edges are DAG by design — target is always NEWER node (created after source). Enforced: target.created_at > source.created_at at insertion time. |
| **Soft-delete cascade** | Deleting a node soft-deletes all outgoing edges. Child nodes are NOT automatically deleted — they become orphaned (parent_id set to a deleted node). GetChildren filters out soft-deleted nodes. |
| **UUIDv7 generation in Go** | Go generates UUIDv7 on the application side before INSERT (using `github.com/google/uuid` v1.6+ with `uuid.NewV7()`). The `DEFAULT uuidv7()` in DDL is a fallback for direct SQL inserts. |
| **JSONB metadata overflow** | PostgreSQL JSONB max ~1GB. Cap at 1MB in Go validation. |
| **Sequence gap after rollback** | `sequence_num` may have gaps after failed inserts. This is fine — sequence_nums are for ORDER BY, not counting. |
| **Multi-parent synthesis node** | A node with `node_type = 'synthesis'` may have multiple incoming edges. The single-parent constraint check is skipped for synthesis nodes. Visualized as a diamond merge in the tree. |

---

## 10. Testing

### 10.1 Go Unit Tests

```
TestNodeRepo_Create:
  - Creates node, verifies all fields, sequence_num=1
  - Creates second node in same tree, sequence_num=2
  - Rejects empty content

TestNodeRepo_CreateRoot:
  - Creates node with parent_id=NULL → succeeds
  - Creates second node with parent_id=NULL → succeeds (multiple root nodes allowed)

TestNodeRepo_GetByID:
  - Returns node for valid ID
  - Returns nil for non-existent ID
  - Does NOT return soft-deleted node

TestNodeRepo_GetChildren:
  - Returns direct children ordered by edge sequence_num
  - Empty list for leaf node
  - Does NOT return soft-deleted children

TestNodeRepo_GetAncestors:
  - Returns [root, mid, leaf] for a 3-level chain
  - Root node's ancestors = [root]

TestNodeRepo_SoftDelete:
  - Sets deleted_at
  - Node not returned by GetByID
  - Outgoing edges also soft-deleted

TestNodeRepo_Update:
  - Updates content, sets edited_at
  - edited_at is nil before first update
  - Rejects empty content

TestEdgeRepo_Create:
  - Creates edge, verifies fields
  - Rejects self-edge (source == target)
  - Rejects duplicate edge (same source+target+type)

TestEdgeRepo_SingleParent:
  - Creates edge to non-synthesis node → ok
  - Creates second edge to same non-synthesis node → ErrMultipleParents
  - Creates second edge to synthesis node → ok

TestEdgeRepo_DAGConstraint:
  - Rejects edge where target.created_at <= source.created_at

TestTreeRepo_Create:
  - Creates tree with null root_node_id
  - SetRootNode updates root_node_id

TestConcurrentSequence:
  - 10 goroutines create nodes concurrently in same tree
  - All sequence_nums are unique (may have gaps)
```

### 10.2 TypeScript Unit Tests

```
describe('nodeSchema'):
  - validates valid node data
  - rejects missing required fields
  - rejects empty content
  - rejects content > 100KB
  - defaults contentFormat to 'markdown'
  - defaults nodeType to 'message'

describe('edgeSchema'):
  - validates valid edge
  - rejects self-edge (sourceId === targetId)
  - defaults edgeType to 'reply'

describe('Yjs CRDT'):
  - createTreeYDoc returns empty maps
  - node insert fires observe callback
  - node update fires observe callback with keys delta
  - node delete fires observe callback
  - concurrent inserts from two docs merge correctly
```

### 10.3 Integration Tests

```
TestIntegration_CreateTreeAndRootNode:
  - Creates tree → creates root node → tree.root_node_id updated
  - Full flow with real PostgreSQL (testcontainers or docker-compose)

TestIntegration_FullBranchFlow:
  - Root → reply (child) → reply (grandchild) → fork from child (sibling)
  - Verify all edges, sequence_nums, parent_id chains

TestIntegration_SynthesisNode:
  - Two branches → synthesis node with two incoming edges
  - Verify GetParents returns both source IDs
```

---

## 11. Hilo Impact

### Dependencies (this code depends on)
- `github.com/google/uuid` — UUIDv7 generation
- `github.com/jackc/pgx/v5` — PostgreSQL driver
- `github.com/georgysavva/scany/v2` — struct scanning
- `github.com/golang-migrate/migrate/v4` — migrations
- `yjs` (npm) — CRDT library (frontend only)
- `zod` (npm) — validation (frontend only)

### Dependents (depends on this code)
- SPEC-DM-02 (Tree Snapshot & Delta Model) — uses Node/Edge structs for snapshots
- SPEC-DM-03 (Approval & Audit Trail DDL) — references nodes for approval targets
- SPEC-DM-04 (User & Profile Model) — nodes.author_id and trees.owner_id FK to profiles
- Phase 4 BE-02 (Database Layer) — implements these repos
- Phase 4 BE-03 (Tree Service) — uses TreeRepo, NodeRepo
- Phase 4 BE-04 (Node Service) — uses NodeRepo, EdgeRepo
- Phase 5 FE-02 (Tree Data Store) — uses Yjs schema + TypeScript types

---

## 12. Go Module

```
module hermes-canopy

go 1.24

require (
    github.com/google/uuid v1.6.0
    github.com/jackc/pgx/v5 v5.7.4
    github.com/georgysavva/scany/v2 v2.1.3
    github.com/golang-migrate/migrate/v4 v4.18.2
)
```
