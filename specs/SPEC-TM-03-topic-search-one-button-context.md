# SPEC-TM-03 — Topic Search & One-Button Context

> **Status:** Spec | **Blocks:** BE-05 (Topic Search Service), FE-04 (Search Sidebar), FE-06 (Context Injection UX), AGENT-02 (Context Compiler)
> **References:** SPEC-TM-01, SPEC-TM-02, SPEC-DM-01, SPEC-API-01, SPEC-API-02, ARCHITECTURE.md §3, ARCHITECTURE.md §5

---

## 1. Purpose

Define Canopy's topic-search and one-click context-injection system: the full-text search index and query layer, the search-sidebar UX, the "Add to Context" button, the agent context-injection API, SSE integration, error handling, and verification scenarios. A Go worker reading this spec must implement `TopicSearchService`, the search DDL and query builders, and the context-injection HTTP handler without clarifying questions. A frontend worker must implement the search sidebar, search-result rendering, hover previews, the recent-topics panel, the "Add to Context" action, and SSE event consumption without clarifying questions.

Search transforms the topic branch metadata defined in SPEC-TM-01 from a navigable overlay into a discoverable knowledge base. One-button context turns that knowledge into actionable agent memory: a user clicks a single button and the agent receives the entire topic scope — all nodes, structured — without the user opening, copying, or summarizing anything manually.

---

## 2. Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Search engine | PostgreSQL FTS (tsvector) | Embedded in architecture, zero extra infra, sufficient for MVP with up to ~50K topics |
| Search scope | Topic title + description + all node content in topic scope | Users search for what was *said in a topic*, not just the topic's metadata row |
| Context injection | API endpoint returns `TopicContext` synchronously | Separate from SSE path; synchronous request/response for agent-window injection where the caller blocks until the payload is ready |
| Result ranking | `ts_rank` with title boost (title matches weighted 2×) | Titles are more descriptive of topic intent than arbitrary node content |
| One-button injection | Synchronous `TopicService.GetContext` call returned to agent | Zero round-trips: click → HTTP → SSE → agent reads; no polling or websocket needed |
| Multi-topic injection | Merged context with `--- topic boundary: <slug> ---` markers | Agent can distinguish which nodes belong to which topic even when topics share descendant nodes |
| Recent topics | Denormalized `last_active_at` on topics table, updated on node-add and reference-resolve events within scope | Avoids expensive recursive queries on every "recent topics" sidebar load |
| Snippet generation | `ts_headline` with `StartSel=<mark>, StopSel=</mark>` | Built-in PostgreSQL highlighting, no additional NLP library |
| Index maintenance | Trigger-based `search_vector` on title/description; application-side batch update for node content indexing | Node content changes far more frequently than topic metadata; application-side batching avoids trigger avalanches |
| `max_nodes` default | 500 | Balances agent context-window budget (~8K tokens at ~16 tokens/node for typical content) against topic depth |
| Search sidebar position | Fixed right-side panel, 360px wide, collapsible | Consistent with IDE search-panel conventions; doesn't compete with the tree graph |
| Context hash | SHA-256 of concatenated node IDs in deterministic (depth-first) order | Enables idempotent injection: same hash → agent may skip re-processing |

---

## 3. PostgreSQL DDL

### 3.1 Search Index Additions to Topics Table

```sql
-- 000030_topic_search.up.sql

-- The search_vector column was already added in SPEC-TM-01 §3.1.
-- This migration adds the node-content index table and the search-log analytics table.

-- ── Topic Node Content Index ───────────────────────────────────────────
-- Indexes node content within topic scopes for full-text search.
-- Refreshed asynchronously by the application layer (not via trigger).

CREATE TABLE topic_node_content_search (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    topic_id        uuid        NOT NULL,
    node_id         uuid        NOT NULL,
    tree_id         uuid        NOT NULL,
    content_text    text        NOT NULL,          -- Plain-text rendering of node content (markdown stripped)
    content_vector  tsvector    NOT NULL,          -- Precomputed tsvector for content search
    content_lang    text        NOT NULL DEFAULT 'english',  -- Language for text search config
    updated_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT fk_tncs_topic
        FOREIGN KEY (topic_id) REFERENCES topics(id)
        ON DELETE CASCADE,
    CONSTRAINT fk_tncs_node
        FOREIGN KEY (node_id, tree_id) REFERENCES nodes(id, tree_id)
        ON DELETE CASCADE,
    CONSTRAINT uq_tncs_topic_node
        UNIQUE (topic_id, node_id)
);

CREATE INDEX idx_tncs_topic            ON topic_node_content_search(topic_id);
CREATE INDEX idx_tncs_topic_updated    ON topic_node_content_search(topic_id, updated_at DESC);
CREATE INDEX idx_tncs_search           ON topic_node_content_search USING gin(content_vector);
CREATE INDEX idx_tncs_tree_content     ON topic_node_content_search(tree_id, content_vector);

-- ── Topic Search Vector Refresh Trigger ─────────────────────────────────
-- Updates the tsvector on the topics table when title or description changes.
-- This is the same trigger from SPEC-TM-01 §3.3, re-included here for completeness.

CREATE OR REPLACE FUNCTION update_topic_search_vector() RETURNS trigger AS $$
BEGIN
    NEW.search_vector :=
        setweight(to_tsvector('english', COALESCE(NEW.title, '')), 'A') ||
        setweight(to_tsvector('english', COALESCE(NEW.description, '')), 'B');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_topic_search_vector
    BEFORE INSERT OR UPDATE OF title, description ON topics
    FOR EACH ROW
    EXECUTE FUNCTION update_topic_search_vector();

-- ── Node Content Index Refresh Function ─────────────────────────────────
-- Application calls this after a batch of node content changes within a topic.

CREATE OR REPLACE FUNCTION refresh_topic_node_content_index(topic_id uuid, node_ids uuid[]) RETURNS integer AS $$
DECLARE
    inserted_count integer;
BEGIN
    WITH content_src AS (
        SELECT
            t.id AS topic_id,
            n.id AS node_id,
            n.tree_id,
            -- Strip markdown formatting to produce plain text for FTS
            regexp_replace(
                regexp_replace(
                    regexp_replace(
                        COALESCE(n.content->>'text', ''),
                        E'[\\[\\]()#*_~`>|\\-]', ' ', 'g'
                    ),
                    E'\\s+', ' ', 'g'
                ),
                E'^\\s+|\\s+$', '', 'g'
            ) AS content_text,
            to_tsvector(
                'english',
                regexp_replace(
                    regexp_replace(
                        COALESCE(n.content->>'text', ''),
                        E'[\\[\\]()#*_~`>|\\-]', ' ', 'g'
                    ),
                    E'\\s+', ' ', 'g'
                )
            ) AS content_vector
        FROM topics t
        JOIN topic_member_nodes tmn ON tmn.topic_id = t.id
        JOIN nodes n ON n.id = tmn.node_id
        WHERE t.id = refresh_topic_node_content_index.topic_id
          AND n.id = ANY(refresh_topic_node_content_index.node_ids)
    )
    INSERT INTO topic_node_content_search (topic_id, node_id, tree_id, content_text, content_vector)
    SELECT topic_id, node_id, tree_id, content_text, content_vector
    FROM content_src
    ON CONFLICT (topic_id, node_id)
    DO UPDATE SET
        content_text   = EXCLUDED.content_text,
        content_vector = EXCLUDED.content_vector,
        updated_at     = clock_timestamp();

    GET DIAGNOSTICS inserted_count = ROW_COUNT;
    RETURN inserted_count;
END;
$$ LANGUAGE plpgsql;

-- ── Topic Search Log (Analytics) ────────────────────────────────────────

CREATE TABLE topic_search_log (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL,
    profile_id      uuid        NOT NULL REFERENCES profiles(id) ON DELETE SET NULL,
    query_text      text        NOT NULL,
    result_count    integer     NOT NULL DEFAULT 0,
    filters_applied jsonb       NOT NULL DEFAULT '{}'::jsonb,
    injected_count  integer     NOT NULL DEFAULT 0,   -- How many topics were injected from this search
    search_duration_ms integer  NOT NULL DEFAULT 0,    -- Query execution time
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX idx_tsl_tree_created  ON topic_search_log(tree_id, created_at DESC);
CREATE INDEX idx_tsl_profile       ON topic_search_log(profile_id);
CREATE INDEX idx_tsl_query_hash    ON topic_search_log USING hash(query_text);

-- ── Topic Last Active Update Trigger ────────────────────────────────────

CREATE OR REPLACE FUNCTION update_topic_last_active() RETURNS trigger AS $$
DECLARE
    affected_topic_ids uuid[];
BEGIN
    -- Collect all topics that contain the affected node
    SELECT array_agg(DISTINCT tmn.topic_id) INTO affected_topic_ids
    FROM topic_member_nodes tmn
    WHERE tmn.node_id = COALESCE(NEW.node_id, OLD.node_id);

    -- Update last_active_at for all affected topics
    UPDATE topics
    SET last_active_at = clock_timestamp()
    WHERE id = ANY(affected_topic_ids);

    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_topic_last_active_node_insert
    AFTER INSERT ON nodes
    FOR EACH ROW
    EXECUTE FUNCTION update_topic_last_active();

CREATE TRIGGER trg_topic_last_active_reference
    AFTER UPDATE OF resolved_ref_count ON nodes
    FOR EACH ROW
    WHEN (OLD.resolved_ref_count IS DISTINCT FROM NEW.resolved_ref_count)
    EXECUTE FUNCTION update_topic_last_active();

-- ── Add last_active_at to Topics Table ──────────────────────────────────

ALTER TABLE topics
    ADD COLUMN IF NOT EXISTS last_active_at timestamptz
        NOT NULL DEFAULT clock_timestamp();

CREATE INDEX IF NOT EXISTS idx_topics_last_active
    ON topics(tree_id, last_active_at DESC NULLS LAST);
```

### 3.2 Content Vector Refresh Schedule

The `refresh_topic_node_content_index` function is called:

1. **Immediately** when a topic is created (all nodes in scope are indexed).
2. **Immediately** when a new node is added to an existing topic's scope (single-node refresh).
3. **Batch every 60 seconds** while nodes are being added rapidly (debounced by the application layer).
4. **Immediately** when a topic's root node changes (if topic retargeting is implemented post-MVP).

---

## 4. Go Interfaces

### 4.1 Package Layout

```
internal/
├── search/
│   ├── service.go             # TopicSearchService interface + impl
│   ├── service_test.go        # Tests
│   ├── context.go             # TopicContext compiler
│   └── context_test.go
├── db/
│   ├── topic_search_repo.go   # Search + context repo interface + pgx impl
│   └── topic_search_log_repo.go
```

### 4.2 Go Structs

```go
package search

import (
    "time"
    "github.com/google/uuid"
    "hermes-canopy/internal/db"
)

// TopicSearchResult is a single search result returned to the client.
type TopicSearchResult struct {
    TopicID    uuid.UUID `json:"topic_id"`
    TreeID     uuid.UUID `json:"tree_id"`
    Title      string    `json:"title"`
    Slug       string    `json:"slug"`
    Snippet    string    `json:"snippet"`      // ts_headline output with <mark> tags
    Status     string    `json:"status"`        // "active" | "archived" | "deleted"
    NodeCount  int       `json:"node_count"`
    LastActive time.Time `json:"last_active_at"`
    Relevance  float64   `json:"relevance"`     // ts_rank score
}

// SearchOptions contains pagination and filtering for search queries.
type SearchOptions struct {
    Query        string `json:"query"`
    MaxResults   int    `json:"max_results"`    // Default 20, max 100
    Offset       int    `json:"offset"`         // Default 0
    StatusFilter string `json:"status_filter"`  // "" (all) | "active" | "archived"
    SortBy       string `json:"sort_by"`        // "relevance" | "last_active" | "title"
}

// InjectContextRequest is the payload for the context injection endpoint.
type InjectContextRequest struct {
    TopicIDs  []uuid.UUID `json:"topic_ids" validate:"required,min=1,max=5"`
    MaxNodes  int         `json:"max_nodes"`   // Per-topic max, default 500
}

// TopicContext is the complete context payload for one topic.
// Injected into the agent's context window.
type TopicContext struct {
    TopicID      uuid.UUID  `json:"topic_id"`
    Title        string     `json:"title"`
    Slug         string     `json:"slug"`
    RootNodeID   uuid.UUID  `json:"root_node_id"`
    Nodes        []db.Node  `json:"nodes"`
    TotalNodes   int        `json:"total_nodes"`
    HasMore      bool       `json:"has_more"`  // True if total_nodes > maxNodes
    ContextHash  string     `json:"context_hash"` // SHA-256 of deterministic node order
}

// MultiTopicContext is the merged context for multi-topic injection.
type MultiTopicContext struct {
    Topics       []TopicContext    `json:"topics"`
    MergedText   string            `json:"merged_text"`   // Flat text with boundary markers
    TotalNodes   int               `json:"total_nodes"`
    Truncated    bool              `json:"truncated"`     // True if total_nodes > global max
}

// SearchLogEntry represents a row in topic_search_log.
type SearchLogEntry struct {
    ID                uuid.UUID       `json:"id"`
    TreeID            uuid.UUID       `json:"tree_id"`
    ProfileID         uuid.UUID       `json:"profile_id"`
    QueryText         string          `json:"query_text"`
    ResultCount       int             `json:"result_count"`
    FiltersApplied    json.RawMessage `json:"filters_applied"`
    InjectedCount     int             `json:"injected_count"`
    SearchDurationMs  int             `json:"search_duration_ms"`
    CreatedAt         time.Time       `json:"created_at"`
}
```

### 4.3 Search Service Interface

```go
package search

import (
    "context"
    "github.com/google/uuid"
)

// TopicSearchService handles full-text search and context injection for topics.
type TopicSearchService interface {
    // Search performs full-text search across topics in a tree.
    // Searches title, description, and indexed node content.
    // Returns results ordered by ts_rank (descending) unless SortBy overrides.
    // Returns total count for pagination.
    Search(ctx context.Context, treeID uuid.UUID, opts SearchOptions) ([]TopicSearchResult, int, error)

    // GetRecent returns the most recently active topics in a tree.
    // Ordered by last_active_at DESC. Excludes deleted topics.
    GetRecent(ctx context.Context, treeID uuid.UUID, limit int) ([]TopicSearchResult, error)

    // InjectContext returns the TopicContext for one or more topics.
    // Topics are compiled into a MultiTopicContext with boundary markers.
    // If total nodes exceed the global budget, the agent is warned in MergedText.
    InjectContext(ctx context.Context, treeID uuid.UUID, req InjectContextRequest) (*MultiTopicContext, error)

    // GetTopicPreview returns a lightweight preview for hover tooltips.
    // Contains first N message snippets, participant count, and last active time.
    GetTopicPreview(ctx context.Context, topicID uuid.UUID, snippetCount int) (*TopicPreview, error)

    // RefreshNodeContentIndex re-indexes node content for a topic.
    // Used by the application layer when nodes in the topic scope change.
    RefreshNodeContentIndex(ctx context.Context, topicID uuid.UUID, nodeIDs []uuid.UUID) (int, error)

    // LogSearch records a search event in the analytics log.
    LogSearch(ctx context.Context, entry SearchLogEntry) error
}

// TopicPreview is a lightweight summary for hover tooltips.
type TopicPreview struct {
    TopicID        uuid.UUID   `json:"topic_id"`
    Title          string      `json:"title"`
    Snippets       []string    `json:"snippets"`        // First N message text snippets (max 120 chars each)
    ParticipantCount int       `json:"participant_count"`
    NodeCount      int         `json:"node_count"`
    LastActive     time.Time   `json:"last_active_at"`
    LastActiveRel  string      `json:"last_active_rel"` // Relative time string ("2m ago", "1h ago")
}
```

### 4.4 Repository Interface

```go
package db

import (
    "context"
    "github.com/google/uuid"
    "hermes-canopy/internal/search"
)

// TopicSearchRepo handles data access for topic search and context injection.
type TopicSearchRepo interface {
    // SearchTopics performs tsquery search across topics table.
    // Joins with topic_node_content_search for content-level matching.
    // Uses ts_rank with title weight 'A' and description weight 'B'.
    SearchTopics(ctx context.Context, treeID uuid.UUID, opts search.SearchOptions) ([]search.TopicSearchResult, int, error)

    // GetRecentTopics returns topics ordered by last_active_at DESC.
    GetRecentTopics(ctx context.Context, treeID uuid.UUID, limit int) ([]search.TopicSearchResult, error)

    // GetTopicNodes returns nodes in a topic's scope, breadth-first, up to maxNodes.
    GetTopicNodes(ctx context.Context, topicID uuid.UUID, maxNodes int) ([]Node, int, bool, error)

    // GetTopicNodeContent returns the indexed content for nodes in a topic.
    GetTopicNodeContent(ctx context.Context, topicID uuid.UUID, nodeIDs []uuid.UUID) ([]TopicNodeContent, error)

    // UpsertTopicNodeContent inserts or updates node content in the search index.
    UpsertTopicNodeContent(ctx context.Context, entries []TopicNodeContent) error

    // DeleteTopicNodeContent removes node content from the search index.
    DeleteTopicNodeContent(ctx context.Context, topicID uuid.UUID, nodeIDs []uuid.UUID) error

    // InsertSearchLog records a search analytics entry.
    InsertSearchLog(ctx context.Context, entry search.SearchLogEntry) error

    // GetTopicPreviewNodes returns the first N nodes in a topic for preview.
    GetTopicPreviewNodes(ctx context.Context, topicID uuid.UUID, limit int) ([]Node, error)
}

// TopicNodeContent represents a row in topic_node_content_search.
type TopicNodeContent struct {
    ID           uuid.UUID `db:"id"`
    TopicID      uuid.UUID `db:"topic_id"`
    NodeID       uuid.UUID `db:"node_id"`
    TreeID       uuid.UUID `db:"tree_id"`
    ContentText  string    `db:"content_text"`
    ContentLang  string    `db:"content_lang"`
    UpdatedAt    time.Time `db:"updated_at"`
}
```

### 4.5 Context Compiler Logic

```go
// compileMultiTopicContext assembles the merged context for agent injection.
// This is the core of the one-button context feature.
func (s *topicSearchService) compileMultiTopicContext(
    ctx context.Context,
    treeID uuid.UUID,
    contexts []TopicContext,
    globalMaxNodes int,
) (*MultiTopicContext, error) {
    var merged MultiTopicContext
    merged.Topics = contexts

    var sb strings.Builder
    totalNodes := 0
    truncated := false

    for i, tc := range contexts {
        if totalNodes >= globalMaxNodes {
            truncated = true
            break
        }

        // Topic boundary marker
        sb.WriteString(fmt.Sprintf("\n--- topic boundary: %s (id: %s) ---\n", tc.Slug, tc.TopicID))
        sb.WriteString(fmt.Sprintf("Topic: %s\n", tc.Title))
        sb.WriteString(fmt.Sprintf("Root node: %s\n", tc.RootNodeID))
        sb.WriteString(fmt.Sprintf("Total nodes in topic: %d\n", tc.TotalNodes))
        sb.WriteString(fmt.Sprintf("Nodes included: %d\n\n", len(tc.Nodes)))

        budget := globalMaxNodes - totalNodes
        included := 0
        for _, node := range tc.Nodes {
            if included >= budget {
                truncated = true
                break
            }
            sb.WriteString(formatNodeForContext(node))
            sb.WriteString("\n")
            included++
            totalNodes++
        }

        // Only show has_more flag for THIS topic if we ran out of its budget
        if i < len(contexts) && included < len(tc.Nodes) {
            sb.WriteString(fmt.Sprintf("\n[... %d more nodes in topic %s — truncated by context budget]\n",
                tc.TotalNodes-included, tc.Slug))
        }
    }

    if truncated {
        sb.WriteString(fmt.Sprintf("\n[CONTEXT WARNING: %d topics requested, total nodes exceed budget. Some nodes omitted. Consider re-injecting with fewer topics or higher max_nodes.]\n", len(contexts)))
    }

    merged.MergedText = sb.String()
    merged.TotalNodes = totalNodes
    merged.Truncated = truncated
    return &merged, nil
}

// contextHash computes a deterministic SHA-256 hash of node IDs.
func contextHash(nodes []db.Node) string {
    // Sort nodes by depth-first traversal order (tree_id, parent_path, created_at)
    sorted := make([]db.Node, len(nodes))
    copy(sorted, nodes)
    sort.Slice(sorted, func(i, j int) bool {
        // Depth-first: parent before child, then by position/created_at
        return nodeOrderKey(sorted[i]) < nodeOrderKey(sorted[j])
    })

    h := sha256.New()
    for _, n := range sorted {
        h.Write(n.ID[:]) // UUID bytes
    }
    return hex.EncodeToString(h.Sum(nil))
}

func nodeOrderKey(n db.Node) string {
    // Key = depth_zero_padded + created_at_iso + id
    return fmt.Sprintf("%010d_%s_%s", n.Depth, n.CreatedAt.UTC().Format(time.RFC3339Nano), n.ID.String())
}
```

---

## 5. TypeScript Types & Zod Validation

### 5.1 TypeScript Interfaces

```typescript
// src/types/topic-search.ts

import { z } from 'zod';
import type { Node } from './node';

// ── Search Types ───────────────────────────────────────────────────────

export interface TopicSearchResult {
  topicId: string;
  treeId: string;
  title: string;
  slug: string;
  snippet: string;           // ts_headline with <mark>highlighted</mark> matches
  status: 'active' | 'archived' | 'deleted';
  nodeCount: number;
  lastActiveAt: string;      // ISO 8601
  relevance: number;         // ts_rank score
}

export interface SearchOptions {
  query: string;
  maxResults?: number;       // Default 20
  offset?: number;           // Default 0
  statusFilter?: string;     // '' | 'active' | 'archived'
  sortBy?: 'relevance' | 'last_active' | 'title';
}

export interface SearchResponse {
  results: TopicSearchResult[];
  total: number;
  queryTimeMs: number;
}

// ── Recent Topics ──────────────────────────────────────────────────────

export interface RecentTopicsResponse {
  topics: TopicSearchResult[];
}

// ── Topic Preview (Hover) ──────────────────────────────────────────────

export interface TopicPreview {
  topicId: string;
  title: string;
  snippets: string[];           // First N message snippets, ≤120 chars each
  participantCount: number;
  nodeCount: number;
  lastActiveAt: string;
  lastActiveRel: string;        // "2m ago", "1h ago", "yesterday"
}

// ── Context Injection ──────────────────────────────────────────────────

export interface InjectContextRequest {
  topicIds: string[];
  maxNodes?: number;            // Per-topic max, default 500
}

export interface TopicContext {
  topicId: string;
  title: string;
  slug: string;
  rootNodeId: string;
  nodes: Node[];
  totalNodes: number;
  hasMore: boolean;
  contextHash: string;
}

export interface MultiTopicContext {
  topics: TopicContext[];
  mergedText: string;           // Flat text with topic boundary markers
  totalNodes: number;
  truncated: boolean;
}

export interface ContextInjectResponse {
  context: MultiTopicContext;
  eventId: string;              // SSE event_id for deduplication
}

// ── SSE Event Payloads ─────────────────────────────────────────────────

export interface SSEContextInjected {
  type: 'context_injected';
  data: {
    topicId: string;
    nodeCount: number;
    contextHash: string;
    totalNodesInScope: number;
  };
}
```

### 5.2 Zod Schemas

```typescript
// ── Zod Validation Schemas ─────────────────────────────────────────────

export const SearchOptionsSchema = z.object({
  query: z.string()
    .min(2, 'Search query must be at least 2 characters')
    .max(200, 'Search query must be at most 200 characters'),
  maxResults: z.number()
    .int()
    .min(1)
    .max(100)
    .default(20)
    .optional(),
  offset: z.number()
    .int()
    .min(0)
    .default(0)
    .optional(),
  statusFilter: z.enum(['', 'active', 'archived']).default('').optional(),
  sortBy: z.enum(['relevance', 'last_active', 'title']).default('relevance').optional(),
});

export const InjectContextRequestSchema = z.object({
  topicIds: z.array(z.string().uuid())
    .min(1, 'At least one topic ID is required')
    .max(5, 'Cannot inject more than 5 topics at once'),
  maxNodes: z.number()
    .int()
    .min(1)
    .max(10000)
    .default(500)
    .optional(),
});

// ── API Response Schemas ───────────────────────────────────────────────

export const SearchResponseSchema = z.object({
  results: z.array(z.object({
    topicId: z.string().uuid(),
    treeId: z.string().uuid(),
    title: z.string(),
    slug: z.string(),
    snippet: z.string(),
    status: z.enum(['active', 'archived', 'deleted']),
    nodeCount: z.number().int(),
    lastActiveAt: z.string().datetime(),
    relevance: z.number(),
  })),
  total: z.number().int(),
  queryTimeMs: z.number().int(),
});

export const ContextInjectResponseSchema = z.object({
  context: z.object({
    topics: z.array(z.object({
      topicId: z.string().uuid(),
      title: z.string(),
      slug: z.string(),
      rootNodeId: z.string().uuid(),
      nodes: z.array(z.any()),  // Node schema from SPEC-DM-01
      totalNodes: z.number().int(),
      hasMore: z.boolean(),
      contextHash: z.string(),
    })),
    mergedText: z.string(),
    totalNodes: z.number().int(),
    truncated: z.boolean(),
  }),
  eventId: z.string(),
});
```

### 5.3 React Component Types

```typescript
// src/components/search/types.ts

export interface SearchSidebarProps {
  treeId: string;
  isOpen: boolean;
  onClose: () => void;
  onInjectContext: (topicIds: string[]) => void;
}

export interface SearchResultCardProps {
  result: TopicSearchResult;
  selected: boolean;
  onToggleSelect: (topicId: string) => void;
  onInject: (topicId: string) => void;
}

export interface RecentTopicItemProps {
  topic: TopicSearchResult;
  onInject: (topicId: string) => void;
  onClick: () => void; // Navigate to topic in tree
}

export interface TopicPreviewPopoverProps {
  topicId: string;
  preview: TopicPreview;
  anchorEl: HTMLElement;
  onInject: () => void;
}

export interface MultiInjectDialogProps {
  selectedTopics: TopicSearchResult[];
  totalNodeCount: number;
  onConfirm: () => void;
  onCancel: () => void;
  onRemoveTopic: (topicId: string) => void;
}
```

---

## 6. API Endpoints

### 6.1 GET /trees/{tree_id}/topics/search

Search topics in a tree by full-text query.

**Request:**

```
GET /trees/{tree_id}/topics/search?q={query}&status={status}&limit=20&offset=0&sort=relevance
```

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `q` | string | Yes | — | Search query (min 2, max 200 chars) |
| `status` | string | No | `active` | Filter: `active`, `archived`, or `all` |
| `limit` | integer | No | 20 | Results per page (max 100) |
| `offset` | integer | No | 0 | Pagination offset |
| `sort` | string | No | `relevance` | Sort: `relevance`, `last_active`, `title` |

**Response 200:**

```json
{
  "results": [
    {
      "topic_id": "0194f2a0-...",
      "tree_id": "0194f2a1-...",
      "title": "Database Schema Design",
      "slug": "database-schema-design",
      "snippet": "We need to decide on the <mark>schema</mark> for the <mark>topics</mark> table...",
      "status": "active",
      "node_count": 24,
      "last_active_at": "2026-07-20T14:30:00Z",
      "relevance": 0.892
    }
  ],
  "total": 3,
  "query_time_ms": 12
}
```

**Response 400:**

```json
{
  "error": {
    "code": "SEARCH_QUERY_TOO_SHORT",
    "message": "Search query must be at least 2 characters",
    "param": "q"
  }
}
```

**Response 401:** Unauthorized — caller is not authenticated.

**Response 403:** Forbidden — caller cannot search this tree.

### 6.2 GET /trees/{tree_id}/topics/recent

Returns the most recently active topics in a tree.

**Request:**

```
GET /trees/{tree_id}/topics/recent?limit=10
```

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `limit` | integer | No | 10 | Number of topics to return (max 50) |

**Response 200:**

```json
{
  "topics": [
    {
      "topic_id": "0194f2a0-...",
      "title": "Database Schema Design",
      "slug": "database-schema-design",
      "snippet": "We need to decide on the schema for the topics table...",
      "status": "active",
      "node_count": 24,
      "last_active_at": "2026-07-20T14:30:00Z",
      "relevance": 0.0
    }
  ]
}
```

### 6.3 GET /trees/{tree_id}/topics/{topic_id}/preview

Returns topic preview data for hover tooltips. Designed for high-frequency calls (debounced client-side).

**Request:**

```
GET /trees/{tree_id}/topics/{topic_id}/preview
```

**Response 200:**

```json
{
  "topic_id": "0194f2a0-...",
  "title": "Database Schema Design",
  "snippets": [
    "We need to decide on the schema for the topics table. I'm thinking UUIDv7 for the PK...",
    "Good point. What about the search_vector column — should that be maintained by a trigger?",
    "Yes, a BEFORE INSERT OR UPDATE trigger handles it..."
  ],
  "participant_count": 3,
  "node_count": 24,
  "last_active_at": "2026-07-20T14:30:00Z",
  "last_active_rel": "2h ago"
}
```

**Response 404:**

```json
{
  "error": {
    "code": "TOPIC_NOT_FOUND",
    "message": "Topic not found"
  }
}
```

### 6.4 POST /trees/{tree_id}/context/inject

Inject one or more topics into the agent's context window.

**Request:**

```
POST /trees/{tree_id}/context/inject
Content-Type: application/json

{
  "topic_ids": [
    "0194f2a0-...",
    "0194f2b0-..."
  ],
  "max_nodes": 500
}
```

**Response 200:**

```json
{
  "context": {
    "topics": [
      {
        "topic_id": "0194f2a0-...",
        "title": "Database Schema Design",
        "slug": "database-schema-design",
        "root_node_id": "0194f1a0-...",
        "nodes": [
          { "id": "0194f1a0-...", "content": { "text": "...", "type": "message" }, "depth": 0, "created_at": "..." },
          { "id": "0194f1a1-...", "content": { "text": "...", "type": "message" }, "depth": 1, "created_at": "..." }
        ],
        "total_nodes": 24,
        "has_more": false,
        "context_hash": "a1b2c3d4e5f6..."
      }
    ],
    "merged_text": "\n--- topic boundary: database-schema-design (id: 0194f2a0-...) ---\nTopic: Database Schema Design\n...",
    "total_nodes": 42,
    "truncated": false
  },
  "event_id": "evt_0194f3a0-..."
}
```

**Response 400:**

```json
{
  "error": {
    "code": "CONTEXT_TOO_MANY_TOPICS",
    "message": "Cannot inject more than 5 topics at once",
    "param": "topic_ids"
  }
}
```

**Response 413:**

```json
{
  "error": {
    "code": "CONTEXT_TOO_LARGE",
    "message": "Requested topics contain 15,000 nodes. Maximum per-injection budget is 5,000 nodes across all topics.",
    "context": {
      "requested_nodes": 15000,
      "max_allowed": 5000
    }
  }
}
```

### 6.5 Auth Requirements

All endpoints require a valid session cookie or Bearer token identifying the caller. The caller must have read access to the tree (membership or public-tree policy). Context injection additionally requires the caller to have `write` permission on the tree (injection modifies the agent's context, which is a write operation to the session).

---

## 7. SSE Events

### 7.1 `context_injected`

Emitted when a topic is successfully injected into the agent's context window via the injection endpoint.

**Event shape:**

```
event: context_injected
data: {
  "topic_id": "0194f2a0-...",
  "node_count": 24,
  "context_hash": "a1b2c3d4e5f6...",
  "total_nodes_in_scope": 42
}
```

| Field | Type | Description |
|-------|------|-------------|
| `topic_id` | uuid | ID of the injected topic |
| `node_count` | int | Number of nodes included in this injection (may be less than total_nodes_in_scope if truncated) |
| `context_hash` | string | SHA-256 of deterministic node order; enables agent-side deduplication |
| `total_nodes_in_scope` | int | Total nodes in the topic's scope (pre-truncation) |

For multi-topic injection, one `context_injected` event is emitted per topic. The events are emitted in the order of `topic_ids` in the request, named in the event ID to allow reconstruction:

```
event: context_injected:0
data: {"topic_id": "...", ...}

event: context_injected:1
data: {"topic_id": "...", ...}
```

### 7.2 `search_logged`

Emitted for analytics purposes when a search is performed. May be consumed by internal monitoring or future insight features.

```
event: search_logged
data: {
  "query": "schema design",
  "result_count": 3,
  "query_time_ms": 12
}
```

---

## 8. Error Catalog

| Error Code | HTTP Status | Condition | Message | JSON Shape |
|-----------|-------------|-----------|---------|------------|
| `SEARCH_QUERY_TOO_SHORT` | 400 | Query is fewer than 2 characters | "Search query must be at least 2 characters" | `{"error":{"code":"SEARCH_QUERY_TOO_SHORT","message":"...","param":"q"}}` |
| `SEARCH_QUERY_TOO_LONG` | 400 | Query exceeds 200 characters | "Search query must be at most 200 characters" | `{"error":{"code":"SEARCH_QUERY_TOO_LONG","message":"...","param":"q"}}` |
| `SEARCH_INVALID_SORT` | 400 | Sort parameter is not valid | "Sort must be one of: relevance, last_active, title" | `{"error":{"code":"SEARCH_INVALID_SORT","message":"...","param":"sort"}}` |
| `SEARCH_INVALID_LIMIT` | 400 | Limit is < 1 or > 100 | "Limit must be between 1 and 100" | `{"error":{"code":"SEARCH_INVALID_LIMIT","message":"...","param":"limit"}}` |
| `SEARCH_STOP_WORDS_ONLY` | 400 | Query contains only stop words after tokenization | "Search query contains only common words; try adding more specific terms" | `{"error":{"code":"SEARCH_STOP_WORDS_ONLY","message":"..."}}` |
| `SEARCH_TREE_NOT_FOUND` | 404 | The specified tree ID does not exist | "Tree not found" | `{"error":{"code":"SEARCH_TREE_NOT_FOUND","message":"..."}}` |
| `TOPIC_NOT_FOUND` | 404 | Topic ID does not exist (for injection or preview) | "Topic not found" | `{"error":{"code":"TOPIC_NOT_FOUND","message":"..."}}` |
| `TOPIC_DELETED` | 410 | Topic exists but is soft-deleted (injection not allowed) | "Topic has been deleted" | `{"error":{"code":"TOPIC_DELETED","message":"..."}}` |
| `TOPIC_ARCHIVED_INJECTION` | 409 | Attempting to inject an archived topic without explicit override | "Archived topics cannot be injected. Unarchive first." | `{"error":{"code":"TOPIC_ARCHIVED_INJECTION","message":"..."}}` |
| `CONTEXT_TOO_LARGE` | 413 | Topic nodes exceed the per-injection global max (5,000) | "Requested topics contain N nodes. Maximum per-injection budget is 5,000 nodes." | `{"error":{"code":"CONTEXT_TOO_LARGE","message":"...","context":{"requested_nodes":N,"max_allowed":5000}}}` |
| `CONTEXT_TOO_MANY_TOPICS` | 400 | More than 5 topic IDs in the request | "Cannot inject more than 5 topics at once" | `{"error":{"code":"CONTEXT_TOO_MANY_TOPICS","message":"...","param":"topic_ids"}}` |
| `CONTEXT_INJECTION_FAILED` | 500 | Internal error during context compilation | "Failed to inject context; please try again" | `{"error":{"code":"CONTEXT_INJECTION_FAILED","message":"..."}}` |
| `CONTEXT_INDEX_STALE` | 409 | Topic node content index is stale and refresh failed | "Topic content index is out of date; try again in a few seconds" | `{"error":{"code":"CONTEXT_INDEX_STALE","message":"..."}}` |
| `SEARCH_UNAUTHORIZED` | 401 | Caller has no session or invalid token | "Authentication required" | `{"error":{"code":"SEARCH_UNAUTHORIZED","message":"..."}}` |
| `SEARCH_FORBIDDEN` | 403 | Caller cannot access the tree | "Insufficient permission to search this tree" | `{"error":{"code":"SEARCH_FORBIDDEN","message":"..."}}` |
| `CONTEXT_INJECTION_UNAUTHORIZED` | 401 | Caller is not authenticated for injection | "Authentication required for context injection" | `{"error":{"code":"CONTEXT_INJECTION_UNAUTHORIZED","message":"..."}}` |
| `CONTEXT_INJECTION_FORBIDDEN` | 403 | Caller lacks write permission on the tree | "Context injection requires write access to this tree" | `{"error":{"code":"CONTEXT_INJECTION_FORBIDDEN","message":"..."}}` |
| `SEARCH_RATE_LIMITED` | 429 | Too many search requests in a short period | "Search rate limit exceeded; try again in 30 seconds" | `{"error":{"code":"SEARCH_RATE_LIMITED","message":"..."}}` |
| `CONTEXT_INJECTION_RATE_LIMITED` | 429 | Too many context injections in a short period | "Context injection rate limit exceeded; try again in 60 seconds" | `{"error":{"code":"CONTEXT_INJECTION_RATE_LIMITED","message":"..."}}` |
| `CROSS_TREE_SEARCH_NOT_SUPPORTED` | 400 | Query specifies a different tree_id than the endpoint | "Cross-tree search is not supported in this version" | `{"error":{"code":"CROSS_TREE_SEARCH_NOT_SUPPORTED","message":"..."}}` |

---

## 9. Edge Cases

| Case | Expected Behavior |
|------|-------------------|
| Empty search query | Return `SEARCH_QUERY_TOO_SHORT` if fewer than 2 characters; client should disable search button while query is empty. |
| Search with no matches | Return empty `results` array with `total: 0`. Query is still logged to search_log for analytics. |
| Search across archived topics | With `status=all` or `status=archived`, archived topics are included. With `status=active` (default), archived topics are excluded via `WHERE status = 'active'`. |
| Inject non-existent topic | Return `TOPIC_NOT_FOUND` (404). No SSE event is emitted. |
| Inject deleted topic | Return `TOPIC_DELETED` (410). Topic is soft-deleted; injection is not allowed. The caller must restore the topic first. |
| Inject topic with 10K+ nodes | `max_nodes` defaults to 500. The first 500 nodes (breadth-first) are returned. `has_more` is `true`. If the total across all requested topics exceeds the global 5,000-node budget, return `CONTEXT_TOO_LARGE`. |
| Inject 5+ topics simultaneously | `InjectContextRequestSchema` validates with `.max(5)`. If > 5 topic IDs are passed, return `CONTEXT_TOO_MANY_TOPICS` (400) without any SSE events. |
| Search in tree with no topics | Return empty `results` array. Query is still logged. |
| Special characters in query | Query is parameterized via `$1` pgx placeholder. No SQL injection possible. Characters like `!@#$%^&*()` are stripped or handled by PostgreSQL's FTS parser. If the resulting tsquery is empty, return `SEARCH_STOP_WORDS_ONLY`. |
| Concurrent context injection (two agents) | Each injection is an independent HTTP request. The context is compiled at request time, so both agents receive consistent snapshots. No locking is needed because the underlying topic DDL is read-only during injection. |
| Topic content updated after search result cached | Search results are compiled at query time from `topic_node_content_search`. If the content index is stale (outdated `updated_at`), the search results may not reflect the latest edits. The index is refreshed within 60 seconds via the batch scheduler. |
| Very long topic content snippet truncation | `ts_headline` uses `MaxWords=35, MinWords=15` for snippet generation. Snippets are capped at 300 characters. If `ts_headline` output exceeds 300 characters, it is truncated at the last word boundary and appends `...`. |
| Recent topics when all topics are archived | Return empty array; the sidebar shows a "No recent topics" empty state with a link to "Browse all topics" which triggers a search with `status=all`. |
| Search with stop words only | PostgreSQL FTS strips stop words by default. If the remaining query is empty, return `SEARCH_STOP_WORDS_ONLY` (400). The client should show a "Try more specific terms" hint. |
| Cross-tree search (not supported) | If a query somehow specifies a different tree_id via a client bug, the endpoint returns `CROSS_TREE_SEARCH_NOT_SUPPORTED`. The query parameter `q` is local to the tree in the URL path — there is no cross-tree parameter. |
| Inject topic with zero nodes | A topic with no nodes (empty scope) returns `TopicContext` with `nodes: []`, `total_nodes: 0`, `has_more: false`. The merged text contains the topic boundary marker and header but no node content. |
| Search query matches title but no node content | The title match is boosted (weight 'A' vs 'B' for description). The result appears with a snippet showing the title match and "No content matches" if no node content matched. |
| Two topics with identical content | Both topics are returned as separate results. Their `context_hash` values differ (different topic_id, root_node_id). |
| Agent context window full after injection | The agent warns: "Context window is at P% capacity after injecting topics [X, Y, Z]. Consider removing low-priority context or re-injecting with fewer topics." This is agent-side logic, not server-side — the API always returns the full requested context. |
| Network failure mid-injection | The injection is an HTTP request. If the connection drops before the response, no SSE event was emitted (the SSE event fires after successful compilation). The client retries with exponential backoff. |
| Search UI with very long topic titles (200 chars) | The UI truncates long titles at 60 characters with an ellipsis on result cards. The full title is shown on hover via the `title` attribute. |

---

## 10. Testing

### 10.1 Backend Test Scenarios

| # | Scenario | Setup | Expected |
|---|----------|-------|----------|
| 1 | Basic search by title | Tree with topic "Database Schema Design" containing 24 nodes. Search query: "schema" | Result returned with title match, snippet showing `<mark>schema</mark>`, relevance > 0. |
| 2 | Search by node content | Topic with node containing "We need to decide on the schema for the topics table." Search: "decide on the schema" | Result returned with content snippet match, relevance > 0. |
| 3 | Search with no matches | Tree with topic "Database Schema Design". Search: "quantum cryptography" | Empty results array, total = 0, query logged. |
| 4 | Search with title boost | Topic "Database Schema" has title match. Another topic "Indexing Strategies" has a node mentioning "database schema" in content. Search: "database schema" | "Database Schema" ranked higher (title weight 'A' = 2x). |
| 5 | Search with stop words only | Search: "the and of" | Status 400, `SEARCH_STOP_WORDS_ONLY`. |
| 6 | Search across archived topics | Topic A is active, Topic B is archived. Search: "schema" with `status=all` | Both topics returned. |
| 7 | Search in tree with no topics | Tree with no topics. Search: "anything" | Empty results, total = 0. |
| 8 | Inject single topic | Inject context for topic with 24 nodes, default maxNodes=500 | `TopicContext` with 24 nodes, `has_more=false`, contextHash non-empty. |
| 9 | Inject topic with truncation | Topic has 1,000 nodes. Inject with maxNodes=100 | `TopicContext` with 100 nodes, `has_more=true`, context hash computed from first 100. |
| 10 | Inject non-existent topic | Inject topic with random UUID | Status 404, `TOPIC_NOT_FOUND`, no SSE event. |
| 11 | Inject deleted topic | Create topic, soft-delete it, then inject | Status 410, `TOPIC_DELETED`. |
| 12 | Inject archived topic | Create topic, archive it, then inject without override | Status 409, `TOPIC_ARCHIVED_INJECTION`. |
| 13 | Inject 6 topics (over limit) | Request with 6 topic IDs | Status 400, `CONTEXT_TOO_MANY_TOPICS`. |
| 14 | Inject topics exceeding global budget | Topics total 6,000 nodes, global budget is 5,000 | Status 413, `CONTEXT_TOO_LARGE` with `requested_nodes: 6000, max_allowed: 5000`. |
| 15 | Recent topics ordering | 5 topics with varying last_active_at. Get recent with limit=10 | All 5 returned, ordered by last_active_at DESC. |
| 16 | Recent topics with limit | 15 topics, get recent with limit=3 | 3 results returned. |
| 17 | Recent topics when all archived | All topics archived. Get recent. | Empty array. |
| 18 | Multi-topic injection (2 topics) | Inject 2 topics via single request | `MultiTopicContext` with 2 `TopicContext` entries, merged text with boundary markers, total_nodes = sum of both. |
| 19 | Multi-topic injection truncation | Topic A has 400 nodes, Topic B has 400 nodes. Inject both with maxNodes=250 each | Total across both = 800. If global budget is 500, one topic gets fully included and the other is partially included with truncated=true. |
| 20 | Context hash determinism | Inject same topic twice | Both responses have identical `contextHash` (same nodes, same order). |
| 21 | SSE event emission on injection | Inject single topic | `context_injected` SSE event emitted with topic_id, node_count, context_hash. |
| 22 | SSE event for multi-topic injection | Inject 3 topics | 3 `context_injected:0`, `context_injected:1`, `context_injected:2` events emitted. |
| 23 | Preview endpoint | Topic with 10 nodes, 5 participants. Call preview. | 3 snippets returned (first 3 nodes, ≤120 chars each), participant_count=5, last_active_rel="N ago". |
| 24 | Preview for non-existent topic | Call preview with random UUID | Status 404, `TOPIC_NOT_FOUND`. |
| 25 | Search log recording | Perform a search query | Entry added to `topic_search_log` with query_text, result_count, search_duration_ms. |
| 26 | Content index refresh | Add 3 new nodes to topic scope, then call `RefreshNodeContentIndex` | 3 rows upserted in `topic_node_content_search`. |
| 27 | Content index after topic deletion | Delete a topic | All rows in `topic_node_content_search` for that topic are cascade-deleted. |
| 28 | SQL injection attempt | Search: `' OR 1=1 --` | Parameterized query prevents injection. Returns no matches or `SEARCH_STOP_WORDS_ONLY` if FTS parser strips it. |
| 29 | Special Unicode in query | Search: "Schéma de base de données" | PostgreSQL FTS handles Unicode. Results returned if content matches. |
| 30 | Concurrent search requests | 10 simultaneous search requests on same tree | All requests handled concurrently. No deadlocks (read-only queries on GIN indexes). |

### 10.2 Frontend Test Scenarios

| # | Scenario | Expected |
|---|----------|----------|
| 1 | Search sidebar opens | Fixed right-side panel (360px) slides in with search input focused. Recent topics visible below search box. |
| 2 | Type search query | Debounced (300ms) search fires after 2+ characters. Results replace "Recent topics" section. Loading spinner shown during fetch. |
| 3 | Zero search results | Empty state shown: "No topics match your query" with a "Clear search" link to return to recent topics. |
| 4 | Search result card renders | Card shows: title (truncated at 60 chars), snippet with `<mark>` highlights rendered as yellow background, status badge, node count, last active relative time, "Add to Context" button. |
| 5 | Hover preview | Hover over a topic shows popover with first 3 message snippets (≤120 chars), participant count, node count, "Last active: 2m ago". |
| 6 | Single "Add to Context" click | Clicking the button shows a brief loading state, then a green checkmark. Topic is added to the "Selected for injection" bar at the bottom of the sidebar. SSE `context_injected` event is received. |
| 7 | Multi-topic selection | Checkbox-select 3 topics. Bottom bar shows "3 topics selected (~147 nodes)". Confirm button is enabled. |
| 8 | Multi-topic injection dialog | After selecting multiple topics, confirmation dialog shows each topic's title + node count, total node count, estimated token usage (rough: nodes × 16), Confirm/Cancel buttons. |
| 9 | Injection with truncation warning | Selecting topics with 600+ total nodes shows warning: "Selected topics contain ~600 nodes. The context window may truncate some content. Consider selecting fewer topics." |
| 10 | Recent topics loads on sidebar open | GET /recent returns 10 topics. Each shows title, node count, last active relative time. Clicking navigates the tree view to the topic's root node. |
| 11 | Recent topics empty state | When no topics exist, sidebar shows "No topics yet. Topics are created automatically as you discuss." |
| 12 | Search while recent is loading | Search input is disabled until recent topics have loaded. Once loaded, input is enabled and debounced search replaces the list. |
| 13 | Keyboard navigation | Tab through search results. Enter triggers "Add to Context" on focused result. Escape clears search. Ctrl+Enter on selected set triggers injection. |
| 14 | Sidebar close and reopen | Sidebar state is persisted in local storage. Reopening restores the previous search state (query and results) if within 5 minutes; otherwise loads fresh recent topics. |
| 15 | SSE reconnect | If SSE connection drops during injection, the client reconnects and replays missed events by event_id. The injection button shows a "pending" state until reconnection confirms delivery. |

---

## 11. Hilo Impact

### What depends on this component:

| Component | Depends On | Reason |
|-----------|-----------|--------|
| AGENT-02 (Context Compiler) | `POST /trees/{tree_id}/context/inject` | Reads `TopicContext` from the injection endpoint to populate the agent's context window. The compiler's periodic budget calculation depends on node count metadata from this spec. |
| FE-04 (Search Sidebar) | `GET /trees/{tree_id}/topics/search`, `GET /trees/{tree_id}/topics/recent`, `GET /trees/{tree_id}/topics/{topic_id}/preview` | All search sidebar UI components consume these endpoints. |
| FE-06 (Context Injection UX) | `POST /trees/{tree_id}/context/inject`, SSE `context_injected` | The "Add to Context" button, selection bar, and injection confirmation dialog. |
| BE-05 (Topic Search Service) | `TopicSearchService`, `TopicSearchRepo` | Implementation of the full service from this spec. |
| CLI-02 (Canopy CLI) | `canopyd topic search`, `canopyd topic inject` | CLI commands wrap these same API endpoints. |
| MONITORING-01 (Analytics Pipeline) | `topic_search_log` | Search analytics feed the monitoring pipeline. |

### What this component depends on:

| Component | Required By | Reason |
|-----------|------------|--------|
| SPEC-TM-01 (Topic Data Model) | This spec | Topics table DDL, `TopicContext` struct, `TopicService.GetContext`, scope definition (recursive CTE). |
| SPEC-TM-02 (Auto-Topic Detection) | This spec | Detected topics become searchable. The `last_active_at` field is updated when detection creates a new topic. |
| SPEC-DM-01 (Tree Node & Edge DDL) | This spec | Nodes table structure used in `TopicContext.Nodes[]`, content extraction for FTS indexing. |
| SPEC-API-01 (SSE Event Stream) | This spec | SSE transport for `context_injected` and `search_logged` events. |
| SPEC-API-02 (Tree CRUD Endpoints) | This spec | Tree existence validation for search and injection endpoints. |
| ARCHITECTURE.md §3 | This spec | Topic-as-branch semantics: scope = reachable nodes from root via reply/fork. |
| ARCHITECTURE.md §5 | This spec | Context compilation pipeline: the endpoint feeds into the budgeted context manifest. |

### Hilo Dependency Graph (relevant subset)

```
SPEC-TM-01 ──> SPEC-TM-02 ──> SPEC-TM-03 ──> AGENT-02 (Context Compiler)
                                     │
                                     ├──> FE-04 (Search Sidebar)
                                     ├──> FE-06 (Context Injection UX)
                                     ├──> BE-05 (Topic Search Service)
                                     └──> CLI-02 (Canopy CLI)
```

---

## 12. Future Considerations (Post-MVP)

These are noted but NOT spec'd for MVP implementation:

1. **Meilisearch embedded** — Replace PostgreSQL FTS with embedded Meilisearch for faster search on datasets exceeding 50K topics, with typo tolerance, facet filtering, and synonym support.

2. **Cross-tree search** — Allow searching across multiple trees owned by the same user or organization. Requires a global search index or tree-agnostic query endpoint.

3. **Search within topic** — Filter nodes by content within a topic scope. Example: "In 'Database Schema Design' topic, find nodes mentioning 'index'." Implemented as `GET /trees/{tree_id}/topics/{topic_id}/search?q=index`.

4. **Saved searches / topic bookmarks** — Allow users to save search queries, bookmark topics, and receive notifications when new content matches a saved search.

5. **Search result highlighting in agent context** — When injecting context, the agent pre-highlights nodes that matched the search query, so the agent can immediately see why the user injected this topic.

6. **Analytics dashboard** — Query-frequency heatmaps, most-searched terms, search abandonment rate (queries with zero results that were not retried), topic popularity ranking.

7. **Federated search** — Search across connected Hermes Canopy instances. Requires a federation layer with signed search requests and response verification.

8. **Search suggestions** — Autocomplete search queries from the `topic_search_log` query text corpus, weighted by frequency.

9. **Search by tag** — Filter search results by `topic_tags`. Already supported by the GIN index on `topic_tags` in SPEC-TM-01; expose as `GET /trees/{tree_id}/topics/search?tags=schema,design`.

10. **Incremental content index rebuild** — Instead of batch-refreshing all topic nodes, track node-change events and update only the changed node's entry in `topic_node_content_search`.

11. **Context injection preview** — Before injecting, show the user a preview of what the agent will see: estimated token count, topic structure summary, and a "mock context" button.

12. **Multi-format context output** — In addition to `MergedText`, offer structured JSON or markdown formats for agent consumption via the `Accept` header.

13. **Search result freshness indicator** — Show "3 new nodes since you last searched" based on `last_active_at` compared to the user's previous search timestamp.

14. **Topic search RBAC** — Role-based access control restricting which topics are searchable based on topic membership (from `topic_members`) and tree-level permissions.

15. **Voice search** — Speech-to-text input for the search sidebar, converting voice queries to text for the FTS engine.
