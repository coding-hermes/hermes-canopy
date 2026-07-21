# SPEC-TM-01 — Topic Data Model

> **Status:** Spec | **Blocks:** SPEC-TM-02, SPEC-TM-03, SPEC-TM-04, SPEC-TM-05, SPEC-PL-03 (App Card System), FE-02 (Tree Data Store)
> **References:** SPEC-DM-01, SPEC-DM-02, SPEC-DM-04, SPEC-API-02, SPEC-API-03, ARCHITECTURE.md §3

---

## 1. Purpose

Define the exact PostgreSQL DDL, Go structs, TypeScript types, and Yjs CRDT schema for Canopy's topic system. A Go worker reading this spec must produce the correct `TopicRepo`, `TopicService`, and database layer with zero clarifying questions. A TypeScript worker reading this spec must produce correct API client types and Yjs topic provider.

A topic IS a tree branch with metadata — not a separate container. Every topic is rooted at a specific node in a tree and encompasses all descendant nodes reachable through reply/fork edges. Topics transform the linear tree into a navigable, searchable, referenceable knowledge structure.

---

## 2. Design Decisions (from ARCHITECTURE.md)

| Decision | Choice | Source |
|----------|--------|--------|
| Topic identity | A topic IS a tree branch (root node + descendant scope) | ARCHITECTURE.md §3, tasks.md SPEC-TM-01 |
| Data model | topics table as metadata overlay, not a separate container | This spec §3 |
| IDs | UUIDv7 (time-ordered, RFC 9562) | SPEC-DM-01 §3.1 |
| Scoping | Topic scope = all nodes reachable from root_node_id via reply/fork edges, depth-unlimited by default | This spec §3 |
| Status lifecycle | active → archived → deleted (soft-delete) | This spec §3 |
| #Reference format | `#topic-slug` in message content, resolved server-side at send time | This spec §5, SPEC-TM-04 |
| Title uniqueness | Topic title must be unique within a tree (case-insensitive) | This spec §3 |
| Topic tags | `string[]` — free-form tags for cross-cutting classification | This spec §3 |
| Auto-detection | Agent-side logic in SPEC-TM-02 — this spec provides the data model | SPEC-TM-02 |
| Cross-topic references | Edges of type `reference` connect nodes across topics | SPEC-DM-01 §3.4, this spec §7 |
| Search | PostgreSQL FTS (tsvector) on title + description, stored in a `search_vector` column | This spec §3, SPEC-TM-03 |
| Archive retention | Archived topics retained indefinitely, restorable | This spec §3 |

---

## 3. PostgreSQL DDL

### 3.1 Topics Table

```sql
-- 000010_topics.up.sql

CREATE TABLE topics (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL,
    root_node_id    uuid        NOT NULL,
    title           text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    slug            text        NOT NULL,                  -- URL-safe version of title for #references
    parent_topic_id uuid        REFERENCES topics(id) ON DELETE SET NULL,
    status          text        NOT NULL DEFAULT 'active',  -- 'active' | 'archived' | 'deleted'
    topic_tags      text[]      NOT NULL DEFAULT '{}',
    search_vector   tsvector,                               -- Auto-maintained by trigger
    node_count      integer     NOT NULL DEFAULT 0,         -- Denormalized count of nodes in scope
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    archived_at     timestamptz,                            -- Set when status changes to 'archived'
    deleted_at      timestamptz,                            -- Soft delete (NULL = active)
    CONSTRAINT fk_topics_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id)
        ON DELETE CASCADE,
    CONSTRAINT fk_topics_root_node
        FOREIGN KEY (root_node_id, tree_id) REFERENCES nodes(id, tree_id)
        ON DELETE CASCADE,
    CONSTRAINT uq_topic_tree_slug
        UNIQUE (tree_id, slug),
    CONSTRAINT uq_topic_tree_title
        UNIQUE (tree_id, LOWER(title)),
    CONSTRAINT chk_topic_status
        CHECK (status IN ('active', 'archived', 'deleted')),
    CONSTRAINT chk_topic_title_length
        CHECK (char_length(title) BETWEEN 1 AND 200),
    CONSTRAINT chk_topic_slug_length
        CHECK (char_length(slug) BETWEEN 1 AND 256)
);

-- Indexes
CREATE INDEX idx_topics_tree_id         ON topics(tree_id);
CREATE INDEX idx_topics_tree_status     ON topics(tree_id, status);
CREATE INDEX idx_topics_root_node       ON topics(root_node_id);
CREATE INDEX idx_topics_parent          ON topics(parent_topic_id);
CREATE INDEX idx_topics_status          ON topics(status);
CREATE INDEX idx_topics_created         ON topics(tree_id, created_at DESC);
CREATE INDEX idx_topics_tags            ON topics USING gin(topic_tags);
CREATE INDEX idx_topics_search          ON topics USING gin(search_vector);
```

### 3.2 Slug Generation Function

```sql
-- Generate URL-safe slug from title
CREATE OR REPLACE FUNCTION generate_topic_slug(title text) RETURNS text AS $$
BEGIN
    RETURN lower(
        regexp_replace(
            regexp_replace(
                trim(title),
                '[^a-zA-Z0-9\\s-]', '', 'g'
            ),
            '\\s+', '-', 'g'
        )
    );
END;
$$ LANGUAGE plpgsql IMMUTABLE;
```

### 3.3 Search Vector Trigger

```sql
-- Auto-maintain tsvector on title and description
CREATE OR REPLACE FUNCTION update_topic_search_vector() RETURNS trigger AS $$
BEGIN
    NEW.search_vector := to_tsvector('english', COALESCE(NEW.title, '') || ' ' || COALESCE(NEW.description, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_topic_search_vector
    BEFORE INSERT OR UPDATE OF title, description ON topics
    FOR EACH ROW
    EXECUTE FUNCTION update_topic_search_vector();
```

### 3.4 Topic Node Membership

Topic scope is defined by reachability from `root_node_id`. The following view and helper function compute topic membership:

```sql
-- Topic member nodes view: all nodes reachable from a topic's root node
CREATE VIEW topic_member_nodes AS
WITH RECURSIVE topic_scope AS (
    -- Base: the topic's root node
    SELECT
        t.id AS topic_id,
        n.id AS node_id,
        n.tree_id,
        0 AS depth
    FROM topics t
    JOIN nodes n ON n.id = t.root_node_id AND n.tree_id = t.tree_id
    WHERE n.deleted_at IS NULL

    UNION ALL

    -- Recursive: all children via reply/fork edges
    SELECT
        ts.topic_id,
        n.id AS node_id,
        n.tree_id,
        ts.depth + 1
    FROM topic_scope ts
    JOIN edges e ON e.source_id = ts.node_id
                AND e.deleted_at IS NULL
                AND e.edge_type IN ('reply', 'fork')
    JOIN nodes n ON n.id = e.target_id AND n.deleted_at IS NULL
)
SELECT topic_id, node_id, tree_id, depth
FROM topic_scope;
```

### 3.5 Topic Stats Materialization

When a node is added, removed, or soft-deleted within a topic's scope, the `node_count` on the containing topic(s) must be updated. This is handled application-side (not in DDL triggers) to avoid recursive trigger complexity:

```sql
-- Update topic node count (called from Go service layer)
CREATE OR REPLACE FUNCTION refresh_topic_node_count(topic_id uuid) RETURNS integer AS $$
DECLARE
    cnt integer;
BEGIN
    SELECT COUNT(*) INTO cnt
    FROM topic_member_nodes
    WHERE topic_id = refresh_topic_node_count.topic_id;

    UPDATE topics SET node_count = cnt WHERE id = topic_id;
    RETURN cnt;
END;
$$ LANGUAGE plpgsql;
```

### 3.6 Topic Member Table (Explicit Membership)

For topics that need explicit member lists (beyond tree membership), an optional join table:

```sql
-- 000011_topic_members.up.sql
CREATE TABLE topic_members (
    topic_id    uuid        NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    profile_id  uuid        NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    role        text        NOT NULL DEFAULT 'viewer',   -- 'viewer' | 'contributor' | 'manager'
    joined_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (topic_id, profile_id),
    CONSTRAINT chk_topic_member_role
        CHECK (role IN ('viewer', 'contributor', 'manager'))
);

CREATE INDEX idx_topic_members_profile ON topic_members(profile_id);
```

---

## 4. Go Structs & Repository Interfaces

### 4.1 Package Layout

```
internal/
├── db/
│   ├── models.go              # + Topic struct
│   ├── topic_repo.go          # TopicRepo interface + pgx implementation
│   └── topic_member_repo.go   # TopicMemberRepo interface + pgx implementation (optional)
├── topic/
│   ├── service.go             # TopicService — business logic
│   └── service_test.go        # Tests
```

### 4.2 Go Structs

```go
package db

import (
    "time"
    "github.com/google/uuid"
    "github.com/jackc/pgx/v5/pgtype"
)

// Topic represents a named branch (topic) in a conversation tree.
// Maps to the `topics` table.
type Topic struct {
    ID            uuid.UUID      `db:"id"              json:"id"`
    TreeID        uuid.UUID      `db:"tree_id"         json:"treeId"`
    RootNodeID    uuid.UUID      `db:"root_node_id"    json:"rootNodeId"`
    Title         string         `db:"title"           json:"title"`
    Description   string         `db:"description"     json:"description"`
    Slug          string         `db:"slug"            json:"slug"`
    ParentTopicID *uuid.UUID     `db:"parent_topic_id" json:"parentTopicId"`
    Status        string         `db:"status"          json:"status"`         // "active" | "archived" | "deleted"
    TopicTags     []string       `db:"topic_tags"      json:"topicTags"`
    NodeCount     int32          `db:"node_count"      json:"nodeCount"`
    CreatedAt     time.Time      `db:"created_at"      json:"createdAt"`
    ArchivedAt    *time.Time     `db:"archived_at"     json:"archivedAt"`
    DeletedAt     *time.Time     `db:"deleted_at"      json:"deletedAt"`
}

// TopicMember represents a profile's membership in a topic.
// Maps to the `topic_members` table.
type TopicMember struct {
    TopicID   uuid.UUID `db:"topic_id"   json:"topicId"`
    ProfileID uuid.UUID `db:"profile_id" json:"profileId"`
    Role      string    `db:"role"       json:"role"`       // "viewer" | "contributor" | "manager"
    JoinedAt  time.Time `db:"joined_at"  json:"joinedAt"`
}

// TopicSummary is a lightweight representation for search results and listings.
type TopicSummary struct {
    ID          uuid.UUID `json:"id"`
    TreeID      uuid.UUID `json:"treeId"`
    Title       string    `json:"title"`
    Slug        string    `json:"slug"`
    Description string    `json:"description"`
    Status      string    `json:"status"`
    NodeCount   int32     `json:"nodeCount"`
    TopicTags   []string  `json:"topicTags"`
    CreatedAt   time.Time `json:"createdAt"`
    ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
}

// TopicCreateInput is the request payload for creating a topic.
type TopicCreateInput struct {
    TreeID        uuid.UUID  `json:"treeId" validate:"required"`
    RootNodeID    uuid.UUID  `json:"rootNodeId" validate:"required"`
    Title         string     `json:"title" validate:"required,min=1,max=200"`
    Description   string     `json:"description,omitempty"`
    ParentTopicID *uuid.UUID `json:"parentTopicId,omitempty"`
    TopicTags     []string   `json:"topicTags,omitempty"`
}

// TopicUpdateInput is the request payload for updating a topic.
type TopicUpdateInput struct {
    Title       *string   `json:"title,omitempty"`
    Description *string   `json:"description,omitempty"`
    TopicTags   *[]string `json:"topicTags,omitempty"`
}
```

### 4.3 Repository Interfaces

```go
package db

import (
    "context"
    "github.com/google/uuid"
)

// TopicRepo handles CRUD operations on topics.
type TopicRepo interface {
    // Create inserts a new topic. Generates slug from title.
    // Returns the created topic with generated fields.
    Create(ctx context.Context, input TopicCreateInput) (*Topic, error)

    // GetByID retrieves a topic by ID. Returns nil if not found or deleted.
    GetByID(ctx context.Context, id uuid.UUID) (*Topic, error)

    // GetByTree returns all active topics in a tree, ordered by created_at DESC.
    GetByTree(ctx context.Context, treeID uuid.UUID, status string) ([]Topic, error)

    // GetByRootNode returns the topic whose root_node_id matches the given node.
    // Returns nil if no topic is rooted at that node.
    GetByRootNode(ctx context.Context, nodeID uuid.UUID) (*Topic, error)

    // GetBySlug retrieves a topic by tree_id + slug.
    GetBySlug(ctx context.Context, treeID uuid.UUID, slug string) (*Topic, error)

    // Search performs full-text search across topics in a tree.
    // Uses tsquery for ranking. Returns results ordered by relevance.
    Search(ctx context.Context, treeID uuid.UUID, query string, limit, offset int) ([]TopicSummary, int, error)

    // Update modifies a topic's title, description, and/or tags.
    // If title changes, regenerates slug.
    Update(ctx context.Context, id uuid.UUID, input TopicUpdateInput) (*Topic, error)

    // Archive marks a topic as archived. Sets archived_at.
    Archive(ctx context.Context, id uuid.UUID) error

    // Restore changes a topic's status from 'archived' back to 'active'.
    // Clears archived_at.
    Restore(ctx context.Context, id uuid.UUID) error

    // SoftDelete marks a topic as deleted. Sets deleted_at.
    SoftDelete(ctx context.Context, id uuid.UUID) error

    // HardDelete permanently removes a topic. Should only be called after
    // verifying no active references exist.
    HardDelete(ctx context.Context, id uuid.UUID) error

    // GetParentTopics returns all ancestor topics for a given topic.
    GetParentTopics(ctx context.Context, topicID uuid.UUID) ([]Topic, error)

    // GetChildTopics returns direct child topics.
    GetChildTopics(ctx context.Context, parentTopicID uuid.UUID) ([]Topic, error)

    // RefreshNodeCount recalculates and updates node_count for a topic.
    RefreshNodeCount(ctx context.Context, topicID uuid.UUID) (int32, error)

    // GetTopicsForNode returns all topics that contain the given node in their scope.
    GetTopicsForNode(ctx context.Context, nodeID uuid.UUID) ([]Topic, error)

    // ListArchived returns all archived topics in a tree, ordered by archived_at DESC.
    ListArchived(ctx context.Context, treeID uuid.UUID, limit, offset int) ([]Topic, int, error)
}

// TopicMemberRepo handles CRUD operations on topic memberships.
type TopicMemberRepo interface {
    // AddMember adds a profile to a topic with the given role.
    AddMember(ctx context.Context, topicID, profileID uuid.UUID, role string) (*TopicMember, error)

    // RemoveMember removes a profile from a topic.
    RemoveMember(ctx context.Context, topicID, profileID uuid.UUID) error

    // UpdateRole changes a member's role in a topic.
    UpdateRole(ctx context.Context, topicID, profileID uuid.UUID, role string) error

    // GetMembers returns all members of a topic.
    GetMembers(ctx context.Context, topicID uuid.UUID) ([]TopicMember, error)

    // GetTopicsForProfile returns all topics a profile is a member of.
    GetTopicsForProfile(ctx context.Context, profileID uuid.UUID) ([]Topic, error)
}
```

### 4.4 Service Layer Interface

```go
package topic

import (
    "context"
    "github.com/google/uuid"
    "hermes-canopy/internal/db"
)

// TopicService handles business logic for topic management.
type TopicService interface {
    // CreateTopic creates a new topic from a node. Validates:
    //   - Tree membership for the requester
    //   - Node exists and is not deleted
    //   - Title is unique within the tree
    Create(ctx context.Context, input db.TopicCreateInput, requesterID uuid.UUID) (*db.Topic, error)

    // AutoDetect suggests a topic creation from a node.
    // Returns the proposed topic data for user confirmation.
    // Called by the agent-side auto-detection logic (SPEC-TM-02).
    AutoDetect(ctx context.Context, nodeID uuid.UUID) (*db.TopicCreateInput, error)

    // GetContext returns the full topic context: topic metadata + scope nodes.
    // This is the "one-button context" payload (SPEC-TM-03).
    GetContext(ctx context.Context, topicID uuid.UUID) (*TopicContext, error)

    // ResolveReference parses a #reference in message content and returns
    // the referenced topic's context. Called at message send time.
    ResolveReference(ctx context.Context, ref string) (*TopicContext, error)

    // MergeTopics merges a source topic into a target topic.
    // Moves topic scope: root_node_id of source becomes a child of target's scope.
    Merge(ctx context.Context, sourceID, targetID uuid.UUID, requesterID uuid.UUID) error

    // SplitTopic creates a new topic from a node within an existing topic's scope.
    // The new topic's root_node_id is the given node.
    Split(ctx context.Context, nodeID uuid.UUID, title string, requesterID uuid.UUID) (*db.Topic, error)
}

// TopicContext is the full context payload for a topic.
// Injected into the agent's context window when a topic is referenced.
type TopicContext struct {
    Topic   db.Topic          `json:"topic"`
    Scope   TopicScope        `json:"scope"`
    Members []db.TopicMember  `json:"members,omitempty"`
}

// TopicScope contains the nodes in a topic's scope.
type TopicScope struct {
    RootNode    db.Node   `json:"rootNode"`
    Nodes       []db.Node `json:"nodes"`       // All descendant nodes, breadth-first
    Depth       int       `json:"depth"`        // Maximum depth from root
    TotalNodes  int       `json:"totalNodes"`   // Total count including root
}
```

---

## 5. TypeScript Types & Zod Validation

### 5.1 TypeScript Interfaces

```typescript
// src/types/topic.ts

import { z } from 'zod';

// ── Topic ────────────────────────────────────────────────────────────

export interface Topic {
  id: string;
  treeId: string;
  rootNodeId: string;
  title: string;
  description: string;
  slug: string;
  parentTopicId: string | null;
  status: 'active' | 'archived' | 'deleted';
  topicTags: string[];
  nodeCount: number;
  createdAt: string;    // ISO 8601
  archivedAt: string | null;
  deletedAt: string | null;
}

export interface TopicSummary {
  id: string;
  treeId: string;
  title: string;
  slug: string;
  description: string;
  status: string;
  nodeCount: number;
  topicTags: string[];
  createdAt: string;
  archivedAt: string | null;
}

export interface TopicCreateInput {
  treeId: string;
  rootNodeId: string;
  title: string;
  description?: string;
  parentTopicId?: string;
  topicTags?: string[];
}

export interface TopicUpdateInput {
  title?: string;
  description?: string;
  topicTags?: string[];
}

// ── Topic Context ────────────────────────────────────────────────────

export interface TopicContext {
  topic: Topic;
  scope: TopicScope;
  members?: TopicMember[];
}

export interface TopicScope {
  rootNode: import('./node').Node;
  nodes: import('./node').Node[];
  depth: number;
  totalNodes: number;
}

// ── Topic Members ────────────────────────────────────────────────────

export interface TopicMember {
  topicId: string;
  profileId: string;
  role: 'viewer' | 'contributor' | 'manager';
  joinedAt: string;
}

// ── Topic Autocomplete ───────────────────────────────────────────────

export interface TopicAutocompleteResult {
  slug: string;
  title: string;
  matchType: 'prefix' | 'contains' | 'fuzzy';
  status: string;
}

// ── #Reference Parsing ───────────────────────────────────────────────

export interface ParsedReference {
  raw: string;              // Full match: "#topic-slug"
  slug: string;             // Extracted slug: "topic-slug"
  offset: number;           // Character offset in message
  length: number;           // Length of matched text
}

export interface ResolvedReference {
  reference: ParsedReference;
  topic: TopicSummary;
  context: TopicContext | null;
}
```

### 5.2 Zod Schemas

```typescript
// src/types/topic.zod.ts

export const TopicStatusSchema = z.enum(['active', 'archived', 'deleted']);
export const TopicMemberRoleSchema = z.enum(['viewer', 'contributor', 'manager']);

export const TopicCreateInputSchema = z.object({
  treeId: z.string().uuid(),
  rootNodeId: z.string().uuid(),
  title: z.string().min(1).max(200),
  description: z.string().max(5000).optional().default(''),
  parentTopicId: z.string().uuid().optional(),
  topicTags: z.array(z.string().max(50)).max(20).optional().default([]),
});

export const TopicUpdateInputSchema = z.object({
  title: z.string().min(1).max(200).optional(),
  description: z.string().max(5000).optional(),
  topicTags: z.array(z.string().max(50)).max(20).optional(),
}).refine(data => data.title || data.description !== undefined || data.topicTags !== undefined, {
  message: 'At least one field must be provided for update',
});

export const TopicSearchQuerySchema = z.object({
  query: z.string().min(1).max(200),
  treeId: z.string().uuid().optional(),
  status: TopicStatusSchema.optional().default('active'),
  limit: z.coerce.number().int().min(1).max(100).optional().default(20),
  offset: z.coerce.number().int().min(0).optional().default(0),
});

export const TopicAutocompleteSchema = z.object({
  prefix: z.string().min(1).max(100),
  treeId: z.string().uuid(),
  limit: z.coerce.number().int().min(1).max(20).optional().default(10),
});
```

### 5.3 Reference Parsing Utility

```typescript
// src/lib/topic-reference.ts

/**
 * Parses `#topic-slug` references from message content.
 * Reference format: `#topic-slug` where slug is [a-z0-9-]+
 * Must start with a letter and contain only lowercase alphanumeric + hyphens.
 */
const REFERENCE_REGEX = /#([a-z][a-z0-9-]*[a-z0-9]|[a-z])/g;

export function parseReferences(content: string): ParsedReference[] {
  const refs: ParsedReference[] = [];
  let match: RegExpExecArray | null;

  while ((match = REFERENCE_REGEX.exec(content)) !== null) {
    refs.push({
      raw: match[0],
      slug: match[1],
      offset: match.index,
      length: match[0].length,
    });
  }

  return refs;
}

/**
 * Renders a resolved reference as a clickable link in the UI.
 * Returns HTML anchor tag with topic tooltip data attributes.
 */
export function renderReference(ref: ResolvedReference): string {
  const topic = ref.topic;
  return `<a href="#topic-${topic.slug}" `
    + `class="topic-reference" `
    + `data-topic-id="${topic.id}" `
    + `data-topic-status="${topic.status}" `
    + `title="${topic.title}: ${topic.description.substring(0, 100)}"`
    + `>${ref.reference.raw}</a>`;
}
```

---

## 6. Yjs CRDT Schema

### 6.1 Topic Y.Map Shape

```typescript
// src/stores/yjs-topics.ts
import * as Y from 'yjs';

export interface YjsTopicMap {
  // topics: Y.Map<TopicYElement>
  // Key: topic.id (UUIDv7 string)
  topics: Y.Map<TopicYElement>;

  // topicTreeIndex: Y.Map<string[]> — maps parent_topic_id → [child_topic_id, ...]
  topicTreeIndex: Y.Map<string[]>;
}

export interface TopicYElement {
  id: string;
  treeId: string;
  rootNodeId: string;
  title: string;
  description: string;
  slug: string;
  parentTopicId: string | null;
  status: 'active' | 'archived' | 'deleted';
  topicTags: string[];
  nodeCount: number;
  createdAt: string;
  archivedAt: string | null;
}

export function createTopicDoc(): Y.Doc {
  const doc = new Y.Doc();
  doc.getMap<TopicYElement>('topics');
  doc.getMap<string[]>('topicTreeIndex');
  return doc;
}

export function topicToYElement(topic: Topic): TopicYElement {
  return {
    id: topic.id,
    treeId: topic.treeId,
    rootNodeId: topic.rootNodeId,
    title: topic.title,
    description: topic.description,
    slug: topic.slug,
    parentTopicId: topic.parentTopicId,
    status: topic.status,
    topicTags: topic.topicTags,
    nodeCount: topic.nodeCount,
    createdAt: topic.createdAt,
    archivedAt: topic.archivedAt,
  };
}
```

### 6.2 SSE Event → Yjs Update Mapping

When the client receives a `topic_added`, `topic_updated`, or `topic_removed` SSE event (SPEC-API-01 event types), it maps directly to Yjs operations:

```typescript
// src/stores/topic-sse-handler.ts

export function handleTopicSSEEvent(event: TopicSSEEvent, doc: Y.Doc): void {
  const topics = doc.getMap<TopicYElement>('topics');
  const treeIndex = doc.getMap<string[]>('topicTreeIndex');

  switch (event.type) {
    case 'topic_added':
    case 'topic_updated': {
      const topic = event.data as TopicYElement;
      topics.set(topic.id, topic);

      // Update tree index
      const siblings = treeIndex.get(topic.parentTopicId ?? '__root__') ?? [];
      if (!siblings.includes(topic.id)) {
        siblings.push(topic.id);
        treeIndex.set(topic.parentTopicId ?? '__root__', siblings);
      }
      break;
    }

    case 'topic_removed': {
      const topicId = event.data.id as string;
      const removed = topics.get(topicId);
      topics.delete(topicId);

      // Remove from tree index
      if (removed) {
        const parentKey = removed.parentTopicId ?? '__root__';
        const siblings = (treeIndex.get(parentKey) ?? [])
          .filter(id => id !== topicId);
        if (siblings.length > 0) {
          treeIndex.set(parentKey, siblings);
        } else {
          treeIndex.delete(parentKey);
        }
      }
      break;
    }
  }
}
```

---

## 7. Cross-Topic Reference Model

Cross-topic references use the existing `edges` table (SPEC-DM-01 §3.4) with `edge_type = 'reference'`:

| Edge Field | Value | Description |
|-----------|-------|-------------|
| `source_id` | Node UUID | The node containing the `#topic-slug` reference |
| `target_id` | Node UUID | The root node of the referenced topic |
| `edge_type` | `'reference'` | Distinguishes from reply/fork/synthesis |
| `metadata` | `{"topic_id": "...", "slug": "..."}` | Topic identifier for quick lookup |

### 7.1 Reference Creation Flow

1. User types `#database-schema` in a message
2. Client parses reference with `parseReferences()` (§5.3)
3. At send time, client sends unresolved reference + node content
4. Server resolves slug → topic via `TopicRepo.GetBySlug()`
5. If found: creates edge of type `reference` from the new node to the referenced topic's root node
6. If NOT found: client formats as an unresolved reference (styled differently, e.g., `#database-schema?` with red underline)
7. Agent auto-creates topic from unresolved references during context assembly

### 7.2 Reference Resolution at Context Time

When an agent processes a message containing references:

1. For each `#topic-slug` in the message:
   a. Look up the topic via the cross-topic reference edge
   b. Load the topic's context via `TopicService.GetContext()`
   c. Include referenced nodes in the agent's context window
2. If references exceed a budget threshold (configurable, default 5), the agent warns:
   "I can see 5 referenced topics — should I focus on specific ones?"
3. The agent parses the topic automatically — the user doesn't need to open and read it

### 7.3 Circular Reference Detection

The TopicService MUST detect and prevent circular reference chains:

```go
// In TopicService.ResolveReference():
// 1. Get the referenced topic
// 2. Walk ancestor topics via parent_topic_id chain
// 3. If the source node's containing topic appears in the ancestor chain,
//    return ErrCircularReference
```

---

## 8. Error Catalog

| Error Code | HTTP Status | Condition | Message |
|-----------|-------------|-----------|---------|
| `TOPIC_NOT_FOUND` | 404 | Topic ID or slug doesn't exist or is deleted | "Topic not found: {slug}" |
| `TOPIC_ALREADY_EXISTS` | 409 | Title already exists in this tree | "A topic with title '{title}' already exists in this tree" |
| `TOPIC_SLUG_CONFLICT` | 409 | Generated slug conflicts with existing | "Slug '{slug}' already exists in this tree" |
| `TOPIC_INVALID_TITLE` | 400 | Title is empty or > 200 chars | "Topic title must be 1-200 characters" |
| `TOPIC_INVALID_STATUS` | 400 | Invalid status transition | "Cannot transition topic from '{current}' to '{target}'" |
| `TOPIC_NODE_NOT_IN_TREE` | 400 | Root node is not in the specified tree | "Node {node_id} is not a member of tree {tree_id}" |
| `TOPIC_NODE_DELETED` | 400 | Root node is soft-deleted | "Cannot create topic on deleted node" |
| `TOPIC_ALREADY_ARCHIVED` | 400 | Attempted to archive already-archived topic | "Topic is already archived" |
| `TOPIC_ALREADY_ACTIVE` | 400 | Attempted to restore already-active topic | "Topic is already active" |
| `TOPIC_CANNOT_DELETE_HAS_CHILDREN` | 409 | Topic has child topics | "Cannot delete topic: it has {N} child topic(s). Delete or reassign children first." |
| `TOPIC_EXCEEDS_MAX_TAGS` | 400 | More than 20 tags | "Topic can have at most 20 tags" |
| `TOPIC_TAG_TOO_LONG` | 400 | Tag length > 50 chars | "Each tag must be 50 characters or fewer" |
| `TOPIC_REFERENCE_NOT_FOUND` | 404 | `#slug` doesn't resolve to any topic | "Topic reference '#{slug}' not found" |
| `TOPIC_CIRCULAR_REFERENCE` | 409 | Reference chain creates a cycle | "Circular reference detected: topic '{slug}' would create a reference cycle" |
| `TOPIC_SEARCH_QUERY_TOO_SHORT` | 400 | Search query < 1 char | "Search query must be at least 1 character" |
| `TOPIC_SEARCH_QUERY_TOO_LONG` | 400 | Search query > 200 chars | "Search query must be 200 characters or fewer" |
| `TOPIC_MEMBER_ALREADY_EXISTS` | 409 | Profile is already a member of the topic | "Profile {profile_id} is already a member of this topic" |
| `TOPIC_MEMBER_NOT_FOUND` | 404 | Profile is not a member of the topic | "Profile {profile_id} is not a member of this topic" |
| `TOPIC_NOT_TREE_MEMBER` | 403 | Profile is not a member of the tree | "Profile {profile_id} is not a member of tree {tree_id}" |
| `TOPIC_INSUFFICIENT_PERMISSIONS` | 403 | Requester lacks required role | "Requires role '{role}' or higher to modify this topic" |

---

## 9. Edge Cases

| Case | Expected Behavior |
|------|------------------|
| **Empty tree, first topic** | Root node is the tree's root node. Topic scope = entire tree. |
| **Node belongs to multiple topics** | A node can be in scope for multiple topics if multiple topics' root nodes are ancestors. `GetTopicsForNode()` returns all containing topics. |
| **Root node is deleted** | Topic becomes "orphaned" — status auto-set to `archived` by TopicService when root node's `deleted_at` is set. Not soft-deleted — restorable if node is restored. |
| **Topic archived, new nodes added** | No effect — archiving is metadata-only. New nodes within the scope remain part of the topic. Archived topics can be active in terms of content. |
| **Topic deleted, nodes referenced** | Orphaned references: `#topic-slug` in existing messages renders as dead link (grey, clickable but shows "topic deleted" tooltip). |
| **Tree deleted** | Cascade delete removes all topics (ON DELETE CASCADE on topics.tree_id FK). |
| **Slug collision** | `generate_topic_slug()` on a title may produce the same slug as an existing topic. `uq_topic_tree_slug` UNIQUE constraint catches this. Retry with appended suffix: `my-slug-2`, `my-slug-3`. |
| **Very long title** | `chk_topic_title_length` enforces max 200 chars. Client should pre-validate. |
| **Concurrent topic creation** | UNIQUE constraints on (tree_id, slug) and (tree_id, LOWER(title)) guarantee no duplicates. Retry on constraint violation with slug suffix. |
| **Topic with 10K+ nodes** | `TopicContext.Nodes` can be paginated. `TopicService.GetContext()` accepts optional `maxNodes` parameter (default 500). Returns truncated scope with `hasMore: true` flag. |
| **Nested topic under archived parent** | Child topics of an archived topic are NOT auto-archived. They persist independently. Archive is metadata-only, not structural. |
| **#reference to self** | Parsed as a normal reference. Creates an edge from a node to its own containing topic's root node. TopicService validates no circular reference. |
| **Reference in deleted node** | Server-side: reference edges are soft-deleted along with the node. Client-side: resolved reference data is cached, but refreshes show `TOPIC_REFERENCE_NOT_FOUND`. |
| **Non-Latin topic title** | Slug generation uses `[a-zA-Z0-9\\s-]` character class — strips non-Latin characters. Latin transliteration is a post-MVP enhancement. |
| **Multiple topics rooted at same node** | Not allowed. `fk_topics_root_node` is a simple FK — uniqueness is enforced application-side in TopicService.Create (check `GetByRootNode` before insert). |

---

## 10. Testing

### 10.1 Backend Test Scenarios

| # | Scenario | Setup | Expected |
|---|----------|-------|----------|
| 1 | Create topic on valid node | Tree with 1 node. Create topic with root_node_id = that node. | Topic created with auto-generated slug. `getByID` returns topic. |
| 2 | Create topic with duplicate title | Create topic T1. Create topic T2 with same title. | Error: `TOPIC_ALREADY_EXISTS` |
| 3 | Create topic with duplicate slug | Create topic with title "My Topic" (slug: "my-topic"). Create topic with title "My Topic!" (slug also "my-topic"). | Second topic created with slug "my-topic-2" (retry with suffix). |
| 4 | Create topic on deleted node | Set node.deleted_at. Create topic with that node as root. | Error: `TOPIC_NODE_DELETED` |
| 5 | Get topic by slug | Create topic, verify slug matches generate_topic_slug(title). | `GetBySlug(treeID, slug)` returns the topic. |
| 6 | Get topics for tree | Create 5 topics in same tree. | `GetByTree(treeID, "active")` returns 5 topics, ordered by created_at DESC. |
| 7 | Archive topic | Create topic, call Archive(). | Status = "archived", archived_at is set. GetByTree with status="active" excludes it. |
| 8 | Restore topic | Archive topic, call Restore(). | Status = "active", archived_at cleared. |
| 9 | Soft-delete topic | Create topic, call SoftDelete(). | deleted_at set. GetByID returns nil. |
| 10 | Hard-delete topic | Create topic with child nodes. HardDelete cascade | Topic row deleted. `topic_member_nodes` view no longer references it. |
| 11 | Search topics | Create topics with distinct titles. Search with query matching one. | Only matching topic returned. Ranked by ts_rank. |
| 12 | Search with no matches | Search for non-existent phrase. | Empty results, count = 0. |
| 13 | Refresh node count | Create topic with 1 node (root). Add 5 child nodes. Call RefreshNodeCount. | node_count = 6. |
| 14 | Get topics for a node | Topic T1 contains node N1 and N2. Topic T2 also contains N1. | `GetTopicsForNode(N1)` returns [T1, T2]. |
| 15 | Parse #references | Message: "See #data-model for details." | `parseReferences()` returns [{slug: "data-model", offset: 5, length: 12}]. |
| 16 | Parse multiple #references | Message: "Compare #data-model and #api-design." | Returns 2 references. |
| 17 | Parse invalid #reference | Message: "See #123-invalid-start for details." | Returns empty (slug must start with letter). |
| 18 | Resolve valid reference | Topic with slug "database-schema" exists. | `ResolveReference("database-schema")` returns TopicContext with scope nodes. |
| 19 | Resolve invalid reference | No topic with slug "non-existent". | Error: `TOPIC_REFERENCE_NOT_FOUND` |
| 20 | Cross-topic reference edge created | Message with #reference resolves successfully. | Edge created: source_id = new node, target_id = topic's root_node_id, edge_type = 'reference'. |
| 21 | Detect circular reference | Topic A references Topic B which references Topic A. | Error: `TOPIC_CIRCULAR_REFERENCE` |
| 22 | Get topic context with scope | Topic with 10 descendant nodes. | TopicContext returned with all 10 nodes in scope. Depth is correct. |
| 23 | Get topic context truncated | Topic with 1000+ nodes. maxNodes=500. | Context returned with 500 nodes, hasMore=true. |
| 24 | Auto-detect topic suggestion | Agent detects semantic shift at node N. | `AutoDetect(N)` returns TopicCreateInput with auto-generated title and description. |
| 25 | Merge topics | Topic T1 (source) merged into Topic T2 (target). | All nodes in T1's scope now also in T2's scope. T1 soft-deleted. |
| 26 | Split topic from existing | Node N within Topic T1's scope. Split with title "Sub Topic". | New topic T2 created with root_node_id = N. T2's parent_topic_id = T1.id. |
| 27 | Concurrent topic creation (same title) | Two requests simultaneously create topic with same title. | Exactly one succeeds. Other gets `TOPIC_ALREADY_EXISTS`. |

### 10.2 Frontend Test Scenarios

| # | Scenario | Expected |
|---|----------|----------|
| 1 | Render topic list from Yjs store | Topic sidebar shows all active topics for current tree. |
| 2 | Create topic from context menu | Right-click node → "Create Topic" → dialog → submit → topic appears in sidebar. |
| 3 | #reference auto-complete | Type "#dat" in composer → dropdown shows "#data-model", "#data-flow". Tab to select. |
| 4 | #reference rendering | Message with "#database-schema" renders as clickable link with tooltip. |
| 5 | Resolved reference tooltip | Hover over #reference → tooltip shows topic title, description preview, node count. |
| 6 | Unresolved reference | `#non-existent` renders with red underline, click shows "Topic not found" tooltip. |
| 7 | Topic archive from sidebar | Right-click → Archive → topic moves to Archived section, shows archived_at. |
| 8 | Topic restore from sidebar | Click on archived topic → "Restore" button → topic moves back to Active section. |
| 9 | Topic search | Type in search box → results show matching topics with relevance indicator. |
| 10 | One-button context | Click "Add to Context" on topic search result → topic scope injected into composer. |

---

## 11. Hilo Impact

### What depends on this component:
- SPEC-TM-02 (Auto-Topic Detection) — reads from this data model, writes to it
- SPEC-TM-03 (Topic Search & One-Button Context) — indexes and queries the topics table
- SPEC-TM-04 (#Reference Resolution) — resolves slugs to topics
- SPEC-TM-05 (Topic Lifecycle & Sidebar) — reads topic data for UI
- SPEC-PL-03 (App Card System) — cards are topic-addressable
- BE-03 (Tree Service) — topic CRUD depends on tree existence
- FE-02 (Tree Data Store) — Yjs topic store

### What this component depends on:
- SPEC-DM-01 (Tree Node & Edge DDL) — `root_node_id` FK to `nodes`, `edges` table for reference edges
- SPEC-DM-04 (User & Profile Model) — `topic_members` FK to `profiles`
- SPEC-API-02 (Tree CRUD) — tree existence for topic creation
- SPEC-API-01 (SSE Event Stream) — topic lifecycle events delivered via SSE

---

## 12. Future Considerations (Post-MVP)

These are noted but NOT spec'd for implementation. They inform data model decisions:

1. **Topic-level permissions**: Extended roles (read-only topic for external collaborators)
2. **Topic templates**: Pre-defined topic structures (bug report, feature request, meeting notes)
3. **Topic analytics**: Activity graphs, contribution metrics per topic
4. **Topic archiving with content pruning**: Archived topics can optionally drop node content but keep structure
5. **Topic export**: Export a topic's scope as JSON, Markdown, or PDF
6. **Subscription and notifications**: Per-topic notification preferences (all messages, mentions only, daily digest)
7. **Topic merge with conflict detection**: When two topics have overlapping but divergent scope, show diff for manual resolution
8. **Bidirectional #references**: References shown on both source and target topic (inbound reference list)
