# SPEC-API-02 — Tree CRUD Endpoints

> **Status:** Spec | **Blocks:** SPEC-API-03, BE-03 (Tree Service), BE-11 (HTTP Router), BE-12 (Integration Tests), FE-02 (Tree Data Store)
> **References:** SPEC-DM-01, SPEC-DM-02, SPEC-DM-04, SPEC-API-01, ARCHITECTURE.md §3, ARCHITECTURE.md §5

---

## 1. Purpose

Define the exact REST endpoint contracts for Canopy tree lifecycle operations: create, list, get, and delete. A Go worker reading this spec must produce correct `TreeService` and `TreeHandler` implementations with zero clarifying questions. A TypeScript worker reading this spec must produce a correct API client with Zod-validated types.

Trees are the top-level organizational unit in Canopy. Every tree contains nodes (messages) connected by edges (reply/fork/synthesis). Tree CRUD is the foundational API layer — all other endpoints operate within the scope of a tree.

---

## 2. Design Decisions (from ARCHITECTURE.md)

| Decision | Choice | Source |
|----------|--------|--------|
| HTTP Router | chi or Go 1.22+ stdlib pattern mux | ARCHITECTURE.md §2.1 |
| Serialization | JSON (application/json) | ARCHITECTURE.md §5 |
| Auth | JWT Bearer token validated on every request | ARCHITECTURE.md §5.5, SPEC-API-01 §8 |
| IDs | UUIDv7 (time-ordered, RFC 9562) | SPEC-DM-01 §3.1 |
| Soft-delete | `deleted_at` column, permanent purge after 30 days | SPEC-DM-01 §3.4, this spec §6 |
| Pagination | Cursor-based (UUID), 100 max per page | This spec §3 |
| Tree creation | Auto-creates root node as first message | This spec §4 |
| Timestamps | `clock_timestamp()` server-side, immutable | SPEC-DM-01 §3 |
| Content-Type negotiation | `application/json` only (MVP). No protobuf/gRPC in Phase 3 | ARCHITECTURE.md §5 |
| Request body max | 1 MB (enforced by middleware) | This spec §12 |

---

## 3. GET /trees — List Trees

### 3.1 Route

```
GET /trees
```

| Field | Value |
|-------|-------|
| Method | GET |
| Path | `/trees` |
| Auth | Required (Bearer token) |
| Content-Type (response) | `application/json; charset=utf-8` |

### 3.2 Query Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `cursor` | string (UUID) | No | — | Return trees with `id < cursor` (ascending) or `id > cursor` (descending). Opaque pagination token. |
| `limit` | integer | No | 50 | Number of trees per page. Min 1, max 100. |
| `sort` | string | No | `created_desc` | Ordering: `created_asc`, `created_desc`, `updated_asc`, `updated_desc`, `title_asc`, `title_desc` |
| `status` | string | No | `active` | Filter: `active` (deleted_at IS NULL), `deleted` (deleted_at IS NOT NULL), `all` (both) |
| `role` | string | No | — | Filter by user role: `owner`, `admin`, `member`, `viewer`. Omitting returns all trees regardless of role. |
| `search` | string | No | — | Full-text search on tree `title` and `description`. Minimum 3 characters. |

### 3.3 Parameter Validation

- `cursor`: Must be a valid UUID. If invalid → HTTP 400 `INVALID_CURSOR`
- `limit`: Parsed as integer. If < 1 → clamp to 1. If > 100 → clamp to 100. If not an integer → HTTP 400 `INVALID_LIMIT`
- `sort`: Must be one of the 6 values above. Invalid → HTTP 400 `INVALID_SORT`
- `status`: Must be `active`, `deleted`, or `all`. Invalid → HTTP 400 `INVALID_STATUS`
- `role`: Must be `owner`, `admin`, `member`, or `viewer`. Invalid → HTTP 400 `INVALID_ROLE`
- `search`: Must be 3–200 characters. Shorter → HTTP 400 `SEARCH_TOO_SHORT`. Non-UTF8 → HTTP 400 `INVALID_SEARCH`

### 3.4 Response — 200 OK

```json
{
  "trees": [
    {
      "id": "0191a8b2-7fff-7000-9000-000000000001",
      "title": "Project Alpha",
      "description": "Main collaboration tree for Alpha project",
      "owner_id": "0191a8b2-7fff-7000-9000-000000000042",
      "owner_display_name": "Bane",
      "node_count": 247,
      "member_count": 3,
      "root_node_id": "0191a8b2-7fff-7000-9000-000000000101",
      "created_at": "2026-07-15T08:00:00Z",
      "updated_at": "2026-07-20T14:22:00Z",
      "role": "owner"
    }
  ],
  "pagination": {
    "next_cursor": "0191a8b2-7fff-7000-9000-000000000050",
    "has_more": true,
    "total": 142,
    "limit": 50
  }
}
```

### 3.5 Response Fields

| Field | Type | Source | Description |
|-------|------|--------|-------------|
| `id` | UUIDv7 | `trees.id` | Tree identifier |
| `title` | string | `trees.title` | User-visible tree name |
| `description` | string | `trees.description` | Optional tree description |
| `owner_id` | UUIDv7 | `trees.owner_id` | Creator/owner of the tree |
| `owner_display_name` | string | JOIN `users.display_name` | Owner's display name |
| `node_count` | integer | `COUNT(nodes.id) WHERE deleted_at IS NULL` | Number of active nodes in tree |
| `member_count` | integer | `COUNT(tree_members.id)` | Number of members in tree |
| `root_node_id` | UUIDv7 | `trees.root_node_id` | The tree's root node (auto-created) |
| `created_at` | ISO 8601 | `trees.created_at` | Creation timestamp |
| `updated_at` | ISO 8601 | `trees.updated_at` | Last mutation timestamp (any node/edge change) |
| `role` | string | JOIN `tree_members.role` | Current user's role in this tree |

### 3.6 Cursor-Based Pagination

Pagination uses `cursor` (a UUIDv7) for stable, offset-free iteration. UUIDv7 is time-ordered, so `created_desc` sorting uses `id < cursor ORDER BY id DESC`, and `created_asc` uses `id > cursor ORDER BY id ASC`.

**Pagination SQL pattern (created_desc):**
```sql
SELECT t.*, u.display_name AS owner_display_name,
       (SELECT COUNT(*) FROM tree_nodes n WHERE n.tree_id = t.id AND n.deleted_at IS NULL) AS node_count,
       (SELECT COUNT(*) FROM tree_members m WHERE m.tree_id = t.id) AS member_count,
       tm.role
FROM trees t
JOIN users u ON t.owner_id = u.id
JOIN tree_members tm ON tm.tree_id = t.id AND tm.user_id = $1
WHERE t.deleted_at IS NULL
  AND ($2::uuid IS NULL OR t.id < $2)
ORDER BY t.id DESC
LIMIT $3 + 1;  -- fetch one extra to determine has_more
```

- `has_more`: `true` when `len(rows) > limit`
- `next_cursor`: last row's `id` from the result set (before trimming extra)
- `total`: approximate (COUNT with filter). Can be stale by up to 5 seconds — clients must not rely on it for page calculation.

### 3.7 Edge Cases

| # | Scenario | Behavior |
|---|----------|----------|
| EC-1 | User has no trees | `trees: []`, `total: 0`, `has_more: false`, no `next_cursor` |
| EC-2 | `cursor` points to deleted tree | Tree excluded from results. Cursor still valid — results are trees before/after this cursor in sort order. |
| EC-3 | `status=deleted` | Returns only soft-deleted trees (deleted_at IS NOT NULL, retention period hasn't expired) |
| EC-4 | `search` matches no trees | `trees: []`, 200 OK |
| EC-5 | `search` with special characters | SQL LIKE escape on `%`, `_`. Backslash-escape in PostgreSQL: `\%`, `\_` |
| EC-6 | Unauthenticated request | HTTP 401 `TOKEN_MISSING` |
| EC-7 | Expired token | HTTP 401 `TOKEN_EXPIRED` |

---

## 4. POST /trees — Create Tree

### 4.1 Route

```
POST /trees
```

| Field | Value |
|-------|-------|
| Method | POST |
| Path | `/trees` |
| Auth | Required (Bearer token) |
| Content-Type (request) | `application/json; charset=utf-8` |
| Content-Type (response) | `application/json; charset=utf-8` |

### 4.2 Request Body

```json
{
  "title": "Project Alpha",
  "description": "Main collaboration tree for Alpha project",
  "root_message": {
    "content": "# Welcome to Project Alpha\n\nThis is our shared workspace.",
    "content_format": "markdown",
    "node_type": "message"
  }
}
```

| Field | Type | Required | Constraints | Description |
|-------|------|----------|-------------|-------------|
| `title` | string | Yes | 1–200 chars, non-blank after trim | Tree name |
| `description` | string | No | 0–2000 chars | Tree description |
| `root_message` | object | Yes | — | The initial message node |
| `root_message.content` | string | Yes | 1–100,000 chars | Root node content |
| `root_message.content_format` | string | No | `markdown`, `plain`, or `code` (default: `markdown`) | Content format |
| `root_message.node_type` | string | No | `message` or `announcement` (default: `message`) | Node type |

### 4.3 Atomic Transaction

Tree creation is an atomic multi-table insert. All 5 operations happen in a single PostgreSQL transaction:

1. `INSERT INTO trees` — create the tree row, return `id`
2. `INSERT INTO tree_members` — add creator as `owner`
3. `INSERT INTO tree_nodes` — create root node with `id = root_node_id`
4. `UPDATE trees SET root_node_id = <root_node_id>` — back-reference (or computed in the same INSERT via CTE)
5. `INSERT INTO tree_snapshots` — create initial snapshot with the single root node

If any step fails, the entire transaction rolls back. The client receives an error — no partial state.

### 4.4 SQL Implementation (CTE)

```sql
WITH new_tree AS (
    INSERT INTO trees (id, title, description, owner_id)
    VALUES (uuidv7(), $1, $2, $3)
    RETURNING id
),
root_node AS (
    INSERT INTO tree_nodes (id, tree_id, parent_id, content, content_format, node_type, author_id, sequence_num)
    SELECT uuidv7(), nt.id, NULL, $4, $5, $6, $3, 1
    FROM new_tree nt
    RETURNING id
),
membership AS (
    INSERT INTO tree_members (id, tree_id, user_id, role)
    SELECT uuidv7(), nt.id, $3, 'owner'
    FROM new_tree nt
),
snapshot AS (
    INSERT INTO tree_snapshots (id, tree_id, hash, node_count, edge_count, snapshot_data)
    SELECT uuidv7(), nt.id, '', 1, 0, jsonb_build_object('nodes', jsonb_build_object(rn.id::text, jsonb_build_array(1, now()::text, NULL, $5, $6)), 'edges', '{}'::jsonb)
    FROM new_tree nt, root_node rn
)
UPDATE trees SET root_node_id = rn.id
FROM root_node rn
WHERE trees.id = rn.tree_id
RETURNING trees.id, trees.title, trees.description, trees.owner_id, trees.root_node_id, trees.created_at;
```

### 4.5 Response — 201 Created

```json
{
  "id": "0191a8b2-7fff-7000-9000-000000000001",
  "title": "Project Alpha",
  "description": "Main collaboration tree for Alpha project",
  "owner_id": "0191a8b2-7fff-7000-9000-000000000042",
  "root_node_id": "0191a8b2-7fff-7000-9000-000000000101",
  "node_count": 1,
  "member_count": 1,
  "created_at": "2026-07-20T22:39:00Z",
  "updated_at": "2026-07-20T22:39:00Z",
  "role": "owner"
}
```

### 4.6 Response Headers

```
Location: /trees/0191a8b2-7fff-7000-9000-000000000001
```

### 4.7 Edge Cases

| # | Scenario | Behavior |
|---|----------|----------|
| EC-8 | `title` is empty or whitespace-only | HTTP 400 `TITLE_REQUIRED` |
| EC-9 | `title` exceeds 200 chars | HTTP 400 `TITLE_TOO_LONG` |
| EC-10 | `root_message.content` is empty | HTTP 400 `ROOT_CONTENT_REQUIRED` |
| EC-11 | `root_message.content` exceeds 100,000 chars | HTTP 400 `ROOT_CONTENT_TOO_LARGE` |
| EC-12 | `content_format` is invalid | HTTP 400 `INVALID_CONTENT_FORMAT` |
| EC-13 | `node_type` is invalid | HTTP 400 `INVALID_NODE_TYPE` |
| EC-14 | Database connection fails mid-transaction | HTTP 503 `DATABASE_UNAVAILABLE`. Transaction rolled back. |
| EC-15 | UUID collision (statistically impossible with UUIDv7) | HTTP 500, logged as critical. Retry with backoff. |
| EC-16 | User rate limited (>100 tree creates/hour) | HTTP 429 `RATE_LIMITED` with `retry_after: 3600` |
| EC-17 | `description` exceeds 2000 chars | HTTP 400 `DESCRIPTION_TOO_LONG` |

---

## 5. GET /trees/{tree_id} — Get Tree

### 5.1 Route

```
GET /trees/{tree_id}
```

| Field | Value |
|-------|-------|
| Method | GET |
| Path | `/trees/{tree_id}` |
| Path param | `tree_id` — UUIDv7 |
| Auth | Required (Bearer token) |
| Query params | `include_stats` (boolean, default `true`), `include_members` (boolean, default `false`) |

### 5.2 Query Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `include_stats` | boolean | No | `true` | Include `node_count`, `member_count`, `branch_count`, `depth` |
| `include_members` | boolean | No | `false` | Include `members` array with user details |

### 5.3 Response — 200 OK

```json
{
  "id": "0191a8b2-7fff-7000-9000-000000000001",
  "title": "Project Alpha",
  "description": "Main collaboration tree for Alpha project",
  "owner_id": "0191a8b2-7fff-7000-9000-000000000042",
  "owner_display_name": "Bane",
  "root_node_id": "0191a8b2-7fff-7000-9000-000000000101",
  "created_at": "2026-07-15T08:00:00Z",
  "updated_at": "2026-07-20T14:22:00Z",
  "deleted_at": null,
  "role": "owner",
  "stats": {
    "node_count": 247,
    "member_count": 3,
    "branch_count": 4,
    "max_depth": 12,
    "pending_approvals": 2
  },
  "members": [
    {
      "user_id": "0191a8b2-7fff-7000-9000-000000000042",
      "display_name": "Bane",
      "role": "owner",
      "joined_at": "2026-07-15T08:00:00Z"
    },
    {
      "user_id": "0191a8b2-7fff-7000-9000-000000000099",
      "display_name": "coding-hermes",
      "role": "member",
      "joined_at": "2026-07-16T10:30:00Z"
    }
  ]
}
```

### 5.4 Computed Fields (from `stats`)

| Field | Source | Description |
|-------|--------|-------------|
| `node_count` | `COUNT(nodes.id) WHERE deleted_at IS NULL` | Active nodes |
| `member_count` | `COUNT(tree_members.id)` | Total members |
| `branch_count` | `COUNT(DISTINCT parent_id) FROM nodes WHERE parent_id IS NOT NULL AND deleted_at IS NULL` | Number of branches (unique parents) |
| `max_depth` | Recursive CTE from root | Maximum depth of any node path from root |
| `pending_approvals` | `COUNT(approvals.id) WHERE status = 'pending'` | Pending approvals in this tree |

### 5.5 SQL — Computed Stats Query

```sql
-- Node count (cheap)
SELECT COUNT(*) FROM tree_nodes WHERE tree_id = $1 AND deleted_at IS NULL;

-- Branch count (cheap)
SELECT COUNT(DISTINCT parent_id) FROM tree_nodes WHERE tree_id = $1 AND parent_id IS NOT NULL AND deleted_at IS NULL;

-- Max depth (recursive CTE — moderately expensive on large trees)
WITH RECURSIVE tree_path AS (
    SELECT id, 1 AS depth FROM tree_nodes WHERE tree_id = $1 AND id = (SELECT root_node_id FROM trees WHERE id = $1)
    UNION ALL
    SELECT e.target_id, tp.depth + 1
    FROM tree_edges e
    JOIN tree_path tp ON e.source_id = tp.id
    WHERE e.edge_type IN ('reply', 'fork')
)
SELECT MAX(depth) FROM tree_path;

-- Pending approvals (cheap)
SELECT COUNT(*) FROM approvals WHERE tree_id = $1 AND status = 'pending';
```

Note: `max_depth` uses a recursive CTE with bounded depth (max 200). If a tree exceeds 200 levels deep (pathological case — loops shouldn't exist due to DAG constraint), the CTE is terminated and `max_depth` returns 200.

### 5.6 Edge Cases

| # | Scenario | Behavior |
|---|----------|----------|
| EC-18 | tree_id is not a valid UUID | HTTP 400 `INVALID_TREE_ID` |
| EC-19 | Tree not found | HTTP 404 `TREE_NOT_FOUND` |
| EC-20 | Tree is soft-deleted | HTTP 404 `TREE_DELETED` |
| EC-21 | User is not a tree member | HTTP 403 `NOT_TREE_MEMBER` |
| EC-22 | `include_stats=false` | `stats` field omitted entirely (not null) |
| EC-23 | `include_members=true` on tree with 500+ members | Members array limited to first 200. Response includes `members_truncated: true` and `total_members: 500`. Client should use `GET /trees/{tree_id}/members` for full list with pagination. |
| EC-24 | Concurrent delete: tree deleted between auth check and query | Race condition handled by transaction isolation (READ COMMITTED). Returns 404. |

---

## 6. DELETE /trees/{tree_id} — Delete Tree

### 6.1 Route

```
DELETE /trees/{tree_id}
```

| Field | Value |
|-------|-------|
| Method | DELETE |
| Path | `/trees/{tree_id}` |
| Path param | `tree_id` — UUIDv7 |
| Auth | Required (Bearer token) |

### 6.2 Soft-Delete Behavior

Trees are soft-deleted. The `deleted_at` timestamp is set to `clock_timestamp()`. All tree data (nodes, edges, approvals, snapshots) is preserved. The tree enters "retention mode."

| Phase | Timestamp | State | Behavior |
|-------|-----------|-------|----------|
| Active | `deleted_at IS NULL` | Normal | All operations allowed |
| Retention | `deleted_at IS NOT NULL AND deleted_at + 30 days > now()` | Soft-deleted | Tree visible in `status=deleted` queries. Nodes/edges are read-only. GET /trees/{id} returns 404 `TREE_DELETED` for non-owners. Owner can still GET the tree. |
| Permanent Purge | `deleted_at + 30 days <= now()` | Purged by background job | All tree data permanently deleted by `purge_expired_trees()` cron. No recovery possible. |

### 6.3 Restore (Undelete)

A deleted tree can be restored within the 30-day retention window by setting `deleted_at = NULL`. This is NOT part of this endpoint — it will be covered by a future `POST /trees/{tree_id}/restore` endpoint. For MVP, restore is done directly via database if needed.

### 6.4 Authorization

Only the tree **owner** can delete a tree. Admins, members, and viewers receive HTTP 403 `NOT_TREE_OWNER`.

### 6.5 Response — 204 No Content

```
HTTP/1.1 204 No Content
```

No response body. The tree is soft-deleted. Subsequent `GET /trees/{tree_id}` returns 404.

### 6.6 Side Effects

When a tree is soft-deleted:

1. All SSE connections to `/trees/{tree_id}/events` receive a `tree_deleted` event, then are closed by the server with a custom `done` event: `data: {"reason":"tree_deleted"}`
2. The SSE hub immediately unsubscribes all clients for this tree.
3. Active approval queue items for this tree are cancelled (status → `expired`).
4. The `tree_deleted` event is written to the audit log with actor = deleting user.

### 6.7 SQL

```sql
-- Atomic soft-delete with auth check
UPDATE trees
SET deleted_at = clock_timestamp()
WHERE id = $1
  AND deleted_at IS NULL
  AND owner_id = $2  -- inline auth: only owner
RETURNING id;

-- If RETURNING returns 0 rows, determine error:
-- Tree doesn't exist at all → 404 TREE_NOT_FOUND
-- Tree exists but deleted_at IS NOT NULL → 404 TREE_ALREADY_DELETED
-- Tree exists but owner_id doesn't match → 403 NOT_TREE_OWNER
```

### 6.8 Edge Cases

| # | Scenario | Behavior |
|---|----------|----------|
| EC-25 | tree_id is not a valid UUID | HTTP 400 `INVALID_TREE_ID` |
| EC-26 | Tree not found | HTTP 404 `TREE_NOT_FOUND` |
| EC-27 | Tree already soft-deleted | HTTP 404 `TREE_ALREADY_DELETED` |
| EC-28 | User is not the owner | HTTP 403 `NOT_TREE_OWNER` |
| EC-29 | Owner downgraded their own role? | Impossible — owner role cannot be changed via API. Owner is immutable. |
| EC-30 | Concurrent delete from two sessions | PostgreSQL row lock prevents double-delete. Second request gets 404 `TREE_ALREADY_DELETED`. |
| EC-31 | SSE hub unsubscribe fails | Logged as WARN. Delete still succeeds. Clients may receive events for a brief window before connection closure. |

---

## 7. Go Interfaces

### 7.1 TreeService

```go
package tree

import (
    "context"
    "time"

    "github.com/google/uuid"
)

// TreeService is the business logic layer for tree CRUD.
// All methods operate within a transaction context passed via ctx.
type TreeService interface {
    // ListTrees returns trees the authenticated user is a member of.
    // Pagination via cursor (UUIDv7 of the last tree from previous page).
    // Returns ListTreesResult with trees and pagination metadata.
    ListTrees(ctx context.Context, params ListTreesParams) (*ListTreesResult, error)

    // CreateTree creates a new tree with an auto-created root node.
    // The caller becomes the tree owner. All operations are atomic (single transaction).
    // Returns the created tree with populated root_node_id.
    CreateTree(ctx context.Context, params CreateTreeParams) (*Tree, error)

    // GetTree returns a single tree by ID with optional computed stats and member list.
    // The caller must be a member of the tree (enforced by middleware, but checked here too).
    GetTree(ctx context.Context, treeID uuid.UUID, opts GetTreeOptions) (*TreeDetail, error)

    // DeleteTree soft-deletes a tree. Only the owner can delete.
    // Sets deleted_at, notifies SSE hub, cancels pending approvals.
    // Returns the deleted_at timestamp for the SSE event payload.
    DeleteTree(ctx context.Context, treeID uuid.UUID) (deletedAt time.Time, err error)
}

// ListTreesParams encapsulates all query parameters for listing trees.
type ListTreesParams struct {
    UserID uuid.UUID       // authenticated user (from JWT context)
    Cursor *uuid.UUID      // pagination cursor (nil on first page)
    Limit  int             // 1–100, clamped
    Sort   TreeSortOrder   // created_desc (default), created_asc, etc.
    Status TreeStatusFilter // active, deleted, all
    Role   *MemberRole     // optional role filter (owner, admin, member, viewer)
    Search string          // full-text search, optional
}

// ListTreesResult contains the page of trees and pagination metadata.
type ListTreesResult struct {
    Trees      []TreeSummary `json:"trees"`
    NextCursor *uuid.UUID    `json:"next_cursor"` // nil if has_more == false
    HasMore    bool           `json:"has_more"`
    Total      int            `json:"total"` // approximate
    Limit      int            `json:"limit"`
}

// CreateTreeParams carries the input for tree creation.
type CreateTreeParams struct {
    OwnerID      uuid.UUID
    Title        string
    Description  string
    RootContent  string
    ContentFormat ContentFormat // markdown, plain, code
    NodeType     NodeType       // message, announcement
}

// GetTreeOptions controls whether optional fields are included.
type GetTreeOptions struct {
    IncludeStats   bool
    IncludeMembers bool
}

// Tree is the response from CreateTree (subset of fields).
type Tree struct {
    ID          uuid.UUID  `json:"id"`
    Title       string     `json:"title"`
    Description string     `json:"description"`
    OwnerID     uuid.UUID  `json:"owner_id"`
    RootNodeID  uuid.UUID  `json:"root_node_id"`
    NodeCount   int        `json:"node_count"`
    MemberCount int        `json:"member_count"`
    CreatedAt   time.Time  `json:"created_at"`
    UpdatedAt   time.Time  `json:"updated_at"`
    Role        MemberRole `json:"role"`
}

// TreeSummary is the list-view representation of a tree.
type TreeSummary struct {
    ID               uuid.UUID  `json:"id"`
    Title            string     `json:"title"`
    Description      string     `json:"description"`
    OwnerID          uuid.UUID  `json:"owner_id"`
    OwnerDisplayName string     `json:"owner_display_name"`
    NodeCount        int        `json:"node_count"`
    MemberCount      int        `json:"member_count"`
    RootNodeID       uuid.UUID  `json:"root_node_id"`
    CreatedAt        time.Time  `json:"created_at"`
    UpdatedAt        time.Time  `json:"updated_at"`
    Role             MemberRole `json:"role"`
}

// TreeDetail is the full representation with optional stats and members.
type TreeDetail struct {
    TreeSummary
    DeletedAt *time.Time       `json:"deleted_at,omitempty"`
    Stats     *TreeStats       `json:"stats,omitempty"`
    Members   []MemberSummary  `json:"members,omitempty"`
}

// TreeStats holds computed aggregate statistics.
type TreeStats struct {
    NodeCount        int `json:"node_count"`
    MemberCount      int `json:"member_count"`
    BranchCount      int `json:"branch_count"`
    MaxDepth         int `json:"max_depth"`
    PendingApprovals int `json:"pending_approvals"`
}

// MemberSummary is a lightweight member representation.
type MemberSummary struct {
    UserID      uuid.UUID  `json:"user_id"`
    DisplayName string     `json:"display_name"`
    Role        MemberRole `json:"role"`
    JoinedAt    time.Time  `json:"joined_at"`
}

// Enums

type TreeSortOrder string

const (
    SortCreatedDesc TreeSortOrder = "created_desc"
    SortCreatedAsc  TreeSortOrder = "created_asc"
    SortUpdatedDesc TreeSortOrder = "updated_desc"
    SortUpdatedAsc  TreeSortOrder = "updated_asc"
    SortTitleAsc    TreeSortOrder = "title_asc"
    SortTitleDesc   TreeSortOrder = "title_desc"
)

func (s TreeSortOrder) Valid() bool {
    switch s {
    case SortCreatedDesc, SortCreatedAsc, SortUpdatedDesc,
         SortUpdatedAsc, SortTitleAsc, SortTitleDesc:
        return true
    }
    return false
}

type TreeStatusFilter string

const (
    TreeStatusActive  TreeStatusFilter = "active"
    TreeStatusDeleted TreeStatusFilter = "deleted"
    TreeStatusAll     TreeStatusFilter = "all"
)

type MemberRole string

const (
    RoleOwner  MemberRole = "owner"
    RoleAdmin  MemberRole = "admin"
    RoleMember MemberRole = "member"
    RoleViewer MemberRole = "viewer"
)

type ContentFormat string

const (
    FormatMarkdown ContentFormat = "markdown"
    FormatPlain    ContentFormat = "plain"
    FormatCode     ContentFormat = "code"
)

type NodeType string

const (
    NodeTypeMessage      NodeType = "message"
    NodeTypeAnnouncement NodeType = "announcement"
)
```

### 7.2 TreeService Errors

```go
// Error types returned by TreeService — use errors.Is() for classification.

var (
    ErrTreeNotFound        = errors.New("tree not found")
    ErrTreeDeleted         = errors.New("tree is soft-deleted")
    ErrTreeAlreadyDeleted  = errors.New("tree already deleted")
    ErrNotTreeOwner        = errors.New("not the tree owner")
    ErrNotTreeMember       = errors.New("not a tree member")
    ErrTitleRequired       = errors.New("title is required")
    ErrTitleTooLong        = errors.New("title exceeds 200 characters")
    ErrDescriptionTooLong  = errors.New("description exceeds 2000 characters")
    ErrRootContentRequired = errors.New("root message content is required")
    ErrRootContentTooLarge = errors.New("root message content exceeds 100,000 characters")
    ErrInvalidContentFormat = errors.New("invalid content format")
    ErrInvalidNodeType     = errors.New("invalid node type")
    ErrInvalidCursor       = errors.New("invalid cursor UUID")
    ErrInvalidSort         = errors.New("invalid sort order")
    ErrInvalidStatus       = errors.New("invalid status filter")
    ErrInvalidRole         = errors.New("invalid role filter")
    ErrSearchTooShort      = errors.New("search query must be at least 3 characters")
    ErrDatabaseUnavailable = errors.New("database unavailable")
)
```

---

## 8. TypeScript Types + Zod

### 8.1 Types

```typescript
// === Enums ===
export type TreeSortOrder =
  | 'created_desc' | 'created_asc'
  | 'updated_desc' | 'updated_asc'
  | 'title_asc'    | 'title_desc';

export type TreeStatusFilter = 'active' | 'deleted' | 'all';

export type MemberRole = 'owner' | 'admin' | 'member' | 'viewer';

export type ContentFormat = 'markdown' | 'plain' | 'code';

export type NodeType = 'message' | 'announcement';

// === Request Types ===
export interface ListTreesParams {
  cursor?: string;   // UUID
  limit?: number;    // 1–100, default 50
  sort?: TreeSortOrder;
  status?: TreeStatusFilter;
  role?: MemberRole;
  search?: string;
}

export interface CreateTreeRequest {
  title: string;
  description?: string;
  root_message: {
    content: string;
    content_format?: ContentFormat;
    node_type?: NodeType;
  };
}

export interface GetTreeOptions {
  include_stats?: boolean;
  include_members?: boolean;
}

// === Response Types ===
export interface TreeSummary {
  id: string;
  title: string;
  description: string;
  owner_id: string;
  owner_display_name: string;
  node_count: number;
  member_count: number;
  root_node_id: string;
  created_at: string;   // ISO 8601
  updated_at: string;   // ISO 8601
  role: MemberRole;
}

export interface TreeStats {
  node_count: number;
  member_count: number;
  branch_count: number;
  max_depth: number;
  pending_approvals: number;
}

export interface MemberSummary {
  user_id: string;
  display_name: string;
  role: MemberRole;
  joined_at: string;    // ISO 8601
}

export interface TreeDetail extends TreeSummary {
  deleted_at?: string | null;
  stats?: TreeStats;
  members?: MemberSummary[];
}

export interface PaginationMeta {
  next_cursor: string | null;
  has_more: boolean;
  total: number;
  limit: number;
}

export interface ListTreesResponse {
  trees: TreeSummary[];
  pagination: PaginationMeta;
}

export interface CreateTreeResponse extends TreeSummary {}

// === API Error ===
export interface ApiError {
  error: string;
  code: string;
  details?: Record<string, unknown>;
}
```

### 8.2 Zod Schemas

```typescript
import { z } from 'zod';

const uuidSchema = z.string().uuid();

export const ListTreesParamsSchema = z.object({
  cursor: uuidSchema.optional(),
  limit: z.coerce.number().int().min(1).max(100).optional().default(50),
  sort: z.enum(['created_desc', 'created_asc', 'updated_desc', 'updated_asc', 'title_asc', 'title_desc']).optional().default('created_desc'),
  status: z.enum(['active', 'deleted', 'all']).optional().default('active'),
  role: z.enum(['owner', 'admin', 'member', 'viewer']).optional(),
  search: z.string().min(3).max(200).optional(),
});

export const CreateTreeRequestSchema = z.object({
  title: z.string().min(1).max(200).refine(s => s.trim().length > 0, 'Title must not be blank'),
  description: z.string().max(2000).optional(),
  root_message: z.object({
    content: z.string().min(1).max(100_000),
    content_format: z.enum(['markdown', 'plain', 'code']).optional().default('markdown'),
    node_type: z.enum(['message', 'announcement']).optional().default('message'),
  }),
});

export const GetTreeOptionsSchema = z.object({
  include_stats: z.coerce.boolean().optional().default(true),
  include_members: z.coerce.boolean().optional().default(false),
});

// Response schemas for validation in tests
export const TreeSummarySchema = z.object({
  id: uuidSchema,
  title: z.string(),
  description: z.string(),
  owner_id: uuidSchema,
  owner_display_name: z.string(),
  node_count: z.number().int().min(0),
  member_count: z.number().int().min(1),
  root_node_id: uuidSchema,
  created_at: z.string().datetime(),
  updated_at: z.string().datetime(),
  role: z.enum(['owner', 'admin', 'member', 'viewer']),
});

export const PaginationMetaSchema = z.object({
  next_cursor: uuidSchema.nullable(),
  has_more: z.boolean(),
  total: z.number().int().min(0),
  limit: z.number().int().min(1).max(100),
});

export const ListTreesResponseSchema = z.object({
  trees: z.array(TreeSummarySchema),
  pagination: PaginationMetaSchema,
});

export const CreateTreeResponseSchema = TreeSummarySchema.extend({
  node_count: z.literal(1),
  member_count: z.literal(1),
});

export const TreeStatsSchema = z.object({
  node_count: z.number().int().min(0),
  member_count: z.number().int().min(1),
  branch_count: z.number().int().min(0),
  max_depth: z.number().int().min(1),
  pending_approvals: z.number().int().min(0),
});

export const TreeDetailSchema = TreeSummarySchema.extend({
  deleted_at: z.string().datetime().nullable().optional(),
  stats: TreeStatsSchema.optional(),
  members: z.array(z.object({
    user_id: uuidSchema,
    display_name: z.string(),
    role: z.enum(['owner', 'admin', 'member', 'viewer']),
    joined_at: z.string().datetime(),
  })).optional(),
});

export const ApiErrorSchema = z.object({
  error: z.string(),
  code: z.string(),
  details: z.record(z.unknown()).optional(),
});
```

---

## 9. Error Catalog

Every error across all endpoints. Format: `{"error": "Human-readable description", "code": "ERROR_CODE", "details": {}}`.

### 9.1 Input Validation Errors (400)

| HTTP Status | Code | Message | Condition |
|-------------|------|---------|-----------|
| 400 | INVALID_CURSOR | Invalid cursor UUID | `?cursor=` is not a valid UUID |
| 400 | INVALID_LIMIT | Limit must be an integer between 1 and 100 | `?limit=` is not a number |
| 400 | INVALID_SORT | Invalid sort order | `?sort=` is not one of the 6 valid values |
| 400 | INVALID_STATUS | Invalid status filter | `?status=` is not active/deleted/all |
| 400 | INVALID_ROLE | Invalid role filter | `?role=` is not owner/admin/member/viewer |
| 400 | SEARCH_TOO_SHORT | Search query must be at least 3 characters | `?search=` has <3 chars |
| 400 | INVALID_TREE_ID | Invalid tree ID | `tree_id` path param is not a valid UUID |
| 400 | TITLE_REQUIRED | Tree title is required | POST body `title` is empty or whitespace-only |
| 400 | TITLE_TOO_LONG | Tree title exceeds 200 characters | POST body `title` > 200 chars |
| 400 | DESCRIPTION_TOO_LONG | Description exceeds 2000 characters | POST body `description` > 2000 chars |
| 400 | ROOT_CONTENT_REQUIRED | Root message content is required | POST body `root_message.content` is empty |
| 400 | ROOT_CONTENT_TOO_LARGE | Root message content exceeds 100,000 characters | POST body `root_message.content` > 100K chars |
| 400 | INVALID_CONTENT_FORMAT | Invalid content format | `content_format` not in markdown/plain/code |
| 400 | INVALID_NODE_TYPE | Invalid node type | `node_type` not in message/announcement |
| 400 | INVALID_JSON | Malformed JSON in request body | `json.Unmarshal` error |

### 9.2 Authentication Errors (401)

| HTTP Status | Code | Message | Condition |
|-------------|------|---------|-----------|
| 401 | TOKEN_MISSING | Authorization header required | No `Authorization: Bearer` header |
| 401 | TOKEN_INVALID | Invalid or malformed token | JWT parse failure |
| 401 | TOKEN_EXPIRED | Token has expired | JWT `exp` claim < now |

### 9.3 Authorization Errors (403)

| HTTP Status | Code | Message | Condition |
|-------------|------|---------|-----------|
| 403 | NOT_TREE_MEMBER | You are not a member of this tree | User not in `tree_members` for this tree |
| 403 | NOT_TREE_OWNER | Only the tree owner can perform this action | DELETE /trees/{id} by non-owner |

### 9.4 Not Found Errors (404)

| HTTP Status | Code | Message | Condition |
|-------------|------|---------|-----------|
| 404 | TREE_NOT_FOUND | Tree not found | tree_id doesn't exist in `trees` table |
| 404 | TREE_DELETED | This tree has been deleted | tree exists but `deleted_at IS NOT NULL` |
| 404 | TREE_ALREADY_DELETED | This tree has already been deleted | DELETE on already-deleted tree |

### 9.5 Rate Limiting Errors (429)

| HTTP Status | Code | Message | Details |
|-------------|------|---------|---------|
| 429 | RATE_LIMITED | Too many requests | `retry_after: <seconds>`, `limit: <max_per_hour>`, `current: <count>` |
| 429 | CREATE_LIMITED | Too many tree creations | `retry_after: 3600` (1h cooldown), `max_per_hour: 100` |

### 9.6 Server Errors (500, 503)

| HTTP Status | Code | Message | Condition |
|-------------|------|---------|-----------|
| 500 | INTERNAL_ERROR | Internal server error | Unexpected error (logged with full trace) |
| 503 | DATABASE_UNAVAILABLE | Database is temporarily unavailable | PostgreSQL connection pool exhausted or unreachable |

### 9.7 Error Response Format

```json
{
  "error": "Human-readable description",
  "code": "ERROR_CODE",
  "details": {
    "field": "title",
    "constraint": "max 200 characters",
    "received": 250
  }
}
```

---

## 10. Authentication & Authorization

### 10.1 Middleware Chain

```
HTTP Request
  → CORS middleware (Preflight, set headers)
  → Auth middleware (Validate JWT, inject UserID into context, reject 401)
  → Tree Membership middleware (GET/DELETE on /trees/{id}: check user in tree_members, reject 403)
  → Tree Owner middleware (DELETE /trees/{id}: check user.role == 'owner', reject 403)
  → Rate Limiting middleware (Per-user limits, reject 429)
  → Body Size middleware (Max 1MB, reject 413)
  → Handler (treeHandler.List / .Create / .Get / .Delete)
```

### 10.2 Auth Context

The auth middleware injects the following into `context.Context`:

```go
type contextKey string

const (
    UserIDKey    contextKey = "user_id"
    UserRoleKey  contextKey = "user_role"
    TokenExpKey  contextKey = "token_exp"
)

// Usage in handlers:
userID := ctx.Value(UserIDKey).(uuid.UUID)
```

### 10.3 Public Endpoints

None. All tree CRUD endpoints require authentication. There is no anonymous tree browsing in MVP.

---

## 11. Middleware Chain (Handler Registration)

### 11.1 Router Setup (Go 1.22+ Pattern Mux)

```go
func RegisterTreeRoutes(mux *http.ServeMux, treeService tree.TreeService, authMW, memberMW, ownerMW, rateLimitMW func(http.Handler) http.Handler) {
    treeHandler := NewTreeHandler(treeService)

    // GET /trees — list (auth + rate limit only, no tree membership)
    mux.Handle("GET /trees", rateLimitMW(authMW(http.HandlerFunc(treeHandler.List))))

    // POST /trees — create (auth + rate limit only, no tree membership — no tree exists yet)
    mux.Handle("POST /trees", rateLimitMW(authMW(http.HandlerFunc(treeHandler.Create))))

    // GET /trees/{tree_id} — get (auth + rate limit + membership)
    mux.Handle("GET /trees/{tree_id}", rateLimitMW(authMW(memberMW(http.HandlerFunc(treeHandler.Get)))))

    // DELETE /trees/{tree_id} — delete (auth + rate limit + membership + owner)
    mux.Handle("DELETE /trees/{tree_id}", rateLimitMW(authMW(memberMW(ownerMW(http.HandlerFunc(treeHandler.Delete))))))
}
```

### 11.2 Chi Router (Alternative)

```go
func RegisterTreeRoutes(r chi.Router, h *TreeHandler) {
    r.Route("/trees", func(r chi.Router) {
        r.Get("/", h.List)           // GET /trees
        r.Post("/", h.Create)        // POST /trees
        r.Route("/{tree_id}", func(r chi.Router) {
            r.Use(TreeMembershipMiddleware)
            r.Get("/", h.Get)        // GET /trees/{tree_id}
            r.Delete("/", h.Delete)  // DELETE /trees/{tree_id}
        })
    })
}
```

---

## 12. Performance & Limits

| Parameter | Value | Notes |
|-----------|-------|-------|
| Request body max | 1 MB | Enforced by middleware (413 Payload Too Large) |
| GET /trees response max | 100 trees | Paginated. Server limit, not client-controlled beyond `limit`. |
| GET /trees default page | 50 trees | Default `limit` |
| Tree creation rate limit | 100/hour per user | Return 429 `CREATE_LIMITED` |
| Tree list rate limit | 1000/hour per user | Return 429 `RATE_LIMITED` |
| GET /trees/{id} rate limit | 6000/hour per user | Read-heavy, higher limit |
| DELETE rate limit | 100/hour per user | Destructive, same as create |
| Soft-delete retention | 30 days | Background job purges expired trees |
| Max trees per user | 1,000 | Enforced at CREATE time |
| Max members per tree | 200 | Only first 200 returned in GET /trees/{id} detail view |
| Max tree depth | 200 levels | Recursive CTE terminates at 200 |
| `stats` computation timeout | 5 seconds | If stats query takes >5s, return tree without stats and log WARN |
| Response Content-Type | `application/json; charset=utf-8` | All responses |
| Pagination stability | Cursor-based (UUIDv7) | No offset drift on concurrent inserts |

---

## 13. Data Model References

All endpoints operate on tables defined in the data model specs:

| Table | Spec | Used By |
|-------|------|---------|
| `trees` | SPEC-DM-01 §3.3 | All endpoints — tree metadata |
| `tree_nodes` | SPEC-DM-01 §3.3 | POST (root node), GET (node_count, branch_count, max_depth) |
| `tree_edges` | SPEC-DM-01 §3.4 | GET (branch_count) |
| `tree_members` | SPEC-DM-04 §3 | All endpoints — membership/auth check, role, member_count |
| `users` | SPEC-DM-04 §3 | GET — owner_display_name, member info |
| `approvals` | SPEC-DM-03 §3 | GET — pending_approvals |
| `tree_snapshots` | SPEC-DM-02 §3.1 | POST — initial snapshot |
| `approval_audit_log` | SPEC-DM-03 §3 | DELETE — audit trail entry |

---

## 14. Edge Cases (Consolidated)

All edge cases from sections 3, 4, 5, and 6, plus cross-cutting concerns:

| # | Scenario | Endpoint | Behavior |
|---|----------|----------|----------|
| EC-1 | User has no trees | GET /trees | Empty list, no cursor |
| EC-2 | cursor points to deleted tree | GET /trees | Tree excluded, cursor valid |
| EC-3 | status=deleted filter | GET /trees | Only soft-deleted trees |
| EC-4 | search matches nothing | GET /trees | Empty list, 200 OK |
| EC-5 | search with SQL special chars | GET /trees | Escaped in LIKE clause |
| EC-6 | Unauthenticated | All | 401 TOKEN_MISSING |
| EC-7 | Expired token | All | 401 TOKEN_EXPIRED |
| EC-8 | Empty title on create | POST /trees | 400 TITLE_REQUIRED |
| EC-9 | Title > 200 chars | POST /trees | 400 TITLE_TOO_LONG |
| EC-10 | Empty root content | POST /trees | 400 ROOT_CONTENT_REQUIRED |
| EC-11 | Root content > 100K | POST /trees | 400 ROOT_CONTENT_TOO_LARGE |
| EC-12 | Invalid content_format | POST /trees | 400 INVALID_CONTENT_FORMAT |
| EC-13 | Invalid node_type | POST /trees | 400 INVALID_NODE_TYPE |
| EC-14 | DB fails mid-transaction | POST /trees | 503, rollback |
| EC-15 | UUID collision | POST /trees | 500, retry |
| EC-16 | Rate limited | POST /trees | 429 CREATE_LIMITED |
| EC-17 | Description > 2000 chars | POST /trees | 400 DESCRIPTION_TOO_LONG |
| EC-18 | Invalid tree_id UUID | GET/DELETE /trees/{id} | 400 INVALID_TREE_ID |
| EC-19 | Tree not found | GET/DELETE /trees/{id} | 404 TREE_NOT_FOUND |
| EC-20 | Tree soft-deleted (GET) | GET /trees/{id} | 404 TREE_DELETED |
| EC-21 | Not a member | GET /trees/{id} | 403 NOT_TREE_MEMBER |
| EC-22 | include_stats=false | GET /trees/{id} | stats field omitted |
| EC-23 | 500+ members, include_members=true | GET /trees/{id} | Truncated to 200 |
| EC-24 | Concurrent delete between check+query | GET /trees/{id} | READ COMMITTED, 404 |
| EC-25 | Invalid tree_id on delete | DELETE /trees/{id} | 400 INVALID_TREE_ID |
| EC-26 | Not owner on delete | DELETE /trees/{id} | 403 NOT_TREE_OWNER |
| EC-27 | Already deleted | DELETE /trees/{id} | 404 TREE_ALREADY_DELETED |
| EC-28 | Concurrent deletes | DELETE /trees/{id} | Row lock, second gets 404 |
| EC-29 | SSE unsubscribe fails | DELETE /trees/{id} | WARN log, delete succeeds |
| EC-30 | Tree exceeds 200-depth | GET /trees/{id} | max_depth capped at 200 |
| EC-31 | Request body not JSON | POST /trees | 400 INVALID_JSON |
| EC-32 | Content-Type not application/json | POST /trees | 415 UNSUPPORTED_MEDIA_TYPE |

---

## 15. Request/Response Sequence Diagrams

### 15.1 Tree Creation Flow

```
Client              Middleware Chain         TreeHandler           PostgreSQL
  │                       │                      │                     │
  │  POST /trees          │                      │                     │
  │  {title,root_message} │                      │                     │
  │──────────────────────>│                      │                     │
  │                       │──CORS pass           │                     │
  │                       │──Auth: validate JWT  │                     │
  │                       │──Rate Limit: check   │                     │
  │                       │──Body Size: ≤1MB     │                     │
  │                       │─────────────────────>│                     │
  │                       │                      │──BEGIN TX           │
  │                       │                      │────────────────────>│
  │                       │                      │ INSERT trees        │
  │                       │                      │ INSERT tree_members │
  │                       │                      │ INSERT tree_nodes   │
  │                       │                      │       (root node)   │
  │                       │                      │ UPDATE root_node_id │
  │                       │                      │ INSERT snapshots    │
  │                       │                      │<────────────────────│
  │                       │                      │──COMMIT             │
  │                       │                      │                     │
  │                       │                      │──SSE Hub: broadcast │
  │                       │                      │   tree_created event│
  │                       │                      │                     │
  │  201 Created          │                      │                     │
  │  Location: /trees/id  │                      │                     │
  │<─────────────────────────────────────────────│                     │
```

### 15.2 Tree Deletion Flow

```
Client              Middleware              TreeHandler           PostgreSQL       SSE Hub
  │                       │                      │                     │              │
  │  DELETE /trees/{id}   │                      │                     │              │
  │──────────────────────>│                      │                     │              │
  │                       │──Auth: validate JWT  │                     │              │
  │                       │──Member: check member│                     │              │
  │                       │──Owner: check owner  │                     │              │
  │                       │─────────────────────>│                     │              │
  │                       │                      │──UPDATE deleted_at  │              │
  │                       │                      │────────────────────>│              │
  │                       │                      │<────────────────────│              │
  │                       │                      │  RETURNING          │              │
  │                       │                      │                     │              │
  │                       │                      │──Cancel approvals   │              │
  │                       │                      │────────────────────>│              │
  │                       │                      │                     │              │
  │                       │                      │──Broadcast:        │              │
  │                       │                      │  tree_deleted event │─────────────>│
  │                       │                      │                     │              │──Unsubscribe
  │                       │                      │                     │              │  all clients
  │                       │                      │                     │              │
  │                       │                      │──Write audit log    │              │
  │                       │                      │────────────────────>│              │
  │                       │                      │                     │              │
  │  204 No Content       │                      │                     │              │
  │<─────────────────────────────────────────────│                     │              │
```

---

## 16. Test Scenarios

### 16.1 Backend (Go) Integration Tests

| # | Test Name | Description |
|---|-----------|-------------|
| T1 | ListTrees_Empty | New user with no trees → 200 OK, empty list |
| T2 | ListTrees_FirstPage | Create 60 trees, GET /trees?limit=50 → 50 trees, has_more=true |
| T3 | ListTrees_SecondPage | Use cursor from T2 → 10 trees, has_more=false |
| T4 | ListTrees_CursorStability | Create 5 trees, get page 1 cursor, create 5 more, get page 2 with same cursor → pages don't overlap |
| T5 | ListTrees_SortCreatedAsc | Trees ordered by created_at ASC |
| T6 | ListTrees_SortTitle | Trees ordered alphabetically by title |
| T7 | ListTrees_StatusDeleted | Soft-delete 2 trees, GET /trees?status=deleted → 2 trees |
| T8 | ListTrees_RoleFilter | GET /trees?role=owner → only owned trees |
| T9 | ListTrees_Search | GET /trees?search=alpha → fuzzy match on title/description |
| T10 | ListTrees_SearchTooShort | GET /trees?search=ab → 400 SEARCH_TOO_SHORT |
| T11 | ListTrees_InvalidCursor | GET /trees?cursor=not-a-uuid → 400 INVALID_CURSOR |
| T12 | ListTrees_InvalidSort | GET /trees?sort=garbage → 400 INVALID_SORT |
| T13 | ListTrees_ClampedLimit | GET /trees?limit=500 → clamped to 100 |
| T14 | ListTrees_NotAuthenticated | GET /trees without Auth header → 401 TOKEN_MISSING |
| T15 | CreateTree_Success | POST /trees with valid body → 201, tree exists in DB, root node created |
| T16 | CreateTree_Minimal | POST /trees with only title + root_message.content → 201, defaults applied |
| T17 | CreateTree_WithDescription | POST with description → 201, description persisted |
| T18 | CreateTree_EmptyTitle | POST with `"title": ""` → 400 TITLE_REQUIRED |
| T19 | CreateTree_WhitespaceTitle | POST with `"title": "   "` → 400 TITLE_REQUIRED |
| T20 | CreateTree_TitleTooLong | POST with 201-char title → 400 TITLE_TOO_LONG |
| T21 | CreateTree_DescriptionTooLong | POST with 2001-char description → 400 DESCRIPTION_TOO_LONG |
| T22 | CreateTree_EmptyRootContent | POST with `"root_message":{"content":""}` → 400 ROOT_CONTENT_REQUIRED |
| T23 | CreateTree_RootContentTooLarge | POST with 100001-char content → 400 ROOT_CONTENT_TOO_LARGE |
| T24 | CreateTree_InvalidContentFormat | POST with `"content_format":"html"` → 400 INVALID_CONTENT_FORMAT |
| T25 | CreateTree_InvalidNodeType | POST with `"node_type":"comment"` → 400 INVALID_NODE_TYPE |
| T26 | CreateTree_RateLimited | Create 101 trees in one hour → 429 CREATE_LIMITED |
| T27 | CreateTree_AtomicRollback | Mock DB failure after INSERT trees → 503, no rows in nodes/members |
| T28 | CreateTree_ResponseLocationHeader | 201 response includes `Location: /trees/{id}` |
| T29 | CreateTree_SetsOwner | Creator's tree_members row has role=owner |
| T30 | CreateTree_InvalidJSON | POST with `{bad json` → 400 INVALID_JSON |
| T31 | GetTree_Success | GET /trees/{id} → 200, all fields populated |
| T32 | GetTree_WithStats | GET /trees/{id} → stats.node_count matches actual count |
| T33 | GetTree_WithoutStats | GET /trees/{id}?include_stats=false → no stats field |
| T34 | GetTree_WithMembers | GET /trees/{id}?include_members=true → members array present |
| T35 | GetTree_MemberCountAfterInvite | Add member, GET /trees/{id} → member_count incremented |
| T36 | GetTree_MaxDepth | Create reply chain depth 10, GET → max_depth=10 |
| T37 | GetTree_BranchCount | Fork at 3 nodes, GET → branch_count=3 |
| T38 | GetTree_PendingApprovals | Create pending approval, GET → pending_approvals=1 |
| T39 | GetTree_NotFound | GET /trees/nonexistent-uuid → 404 TREE_NOT_FOUND |
| T40 | GetTree_InvalidUUID | GET /trees/not-a-uuid → 400 INVALID_TREE_ID |
| T41 | GetTree_Deleted | Soft-delete tree, GET by non-owner → 404 TREE_DELETED |
| T42 | GetTree_DeletedOwnerView | Soft-delete tree, GET by owner → 200 with deleted_at populated |
| T43 | GetTree_NotMember | GET by user not in tree_members → 403 NOT_TREE_MEMBER |
| T44 | GetTree_ManyMembersTruncation | Add 500 members, GET?include_members=true → 200 members, members_truncated=true |
| T45 | DeleteTree_Success | Owner DELETE → 204, deleted_at set |
| T46 | DeleteTree_NotFound | DELETE nonexistent → 404 TREE_NOT_FOUND |
| T47 | DeleteTree_InvalidUUID | DELETE /trees/not-a-uuid → 400 INVALID_TREE_ID |
| T48 | DeleteTree_NotOwner | Member DELETE → 403 NOT_TREE_OWNER |
| T49 | DeleteTree_NotMember | Non-member DELETE → 403 NOT_TREE_MEMBER |
| T50 | DeleteTree_AlreadyDeleted | DELETE already-deleted → 404 TREE_ALREADY_DELETED |
| T51 | DeleteTree_SSENotification | DELETE → SSE clients receive tree_deleted event |
| T52 | DeleteTree_ApprovalsCanceled | DELETE → pending approvals moved to expired |
| T53 | DeleteTree_AuditLog | DELETE → audit log entry created |

### 16.2 Frontend (TypeScript) Tests

| # | Test Name | Description |
|---|-----------|-------------|
| FT1 | listTrees returns parsed response | Zod validation passes on valid response |
| FT2 | listTrees handles empty list | `trees: []`, no crash |
| FT3 | listTrees paginates | `has_more: true`, use `next_cursor` for page 2 |
| FT4 | createTree sends correct body | Request matches CreateTreeRequestSchema |
| FT5 | createTree parses response | Response matches CreateTreeResponseSchema |
| FT6 | getTree with stats | TreeDetailSchema validates stats |
| FT7 | getTree without stats | TreeDetailSchema accepts missing stats |
| FT8 | deleteTree handles 204 | No body, no parse error |
| FT9 | ApiError handling | Parse error response, extract `code` |
| FT10 | Zod validation rejects bad response | Missing `id` field → ZodError |

---

## 17. Unresolved Design Questions

| # | Question | Notes |
|---|----------|-------|
| Q1 | Should tree creation trigger an SSE event to all user's connections (not just tree members)? | The user's device list also needs to know a new tree exists. Likely: broadcast to user-specific SSE channel at connection time, or let the PWA poll GET /trees on reconnect. |
| Q2 | Should `max_depth` be cached rather than computed each request? | For trees with 10K+ nodes, recursive CTE can take >100ms. Consider materialized view updated on node insert, or a `depth` column on nodes (computed at write time). |
| Q3 | Tree description — should it be Markdown or plain text? | SPEC-DM-01 doesn't specify a `description_format` column. For MVP, treat as plain text. Post-MVP can add `description_format` column defaulting to `plain` for backward compatibility. |
| Q4 | Should `GET /trees` return trees where user is viewer? | Yes — viewers see the tree in their list with `role: "viewer"`. They can't create nodes but can navigate the tree. |
