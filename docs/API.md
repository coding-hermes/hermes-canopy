# Canopy API Reference

Base URL: `http://<host>:<port>/api/v1`

All authenticated endpoints require a JWT Bearer token in the `Authorization`
header. Health and version endpoints are public.

---

## Response envelopes (per route)

There is **no single uniform success envelope** in this API: each route group
has its own convention, and a client must match the route it calls. This table
is the contract as implemented (`internal/handler/*.go`, `internal/fileviewer/
handlers.go`) — documented here because consumers already depend on it, not
changed (GAP-072).

| Route | Success | Success body |
|-------|---------|--------------|
| `POST /api/v1/trees` | 201 | **bare object** — the created tree (no wrapper) |
| `POST /api/v1/topics` | 201 | **bare object** — the created topic (no wrapper) |
| `POST /api/v1/cards` | 201 | **bare object** — the created card (no wrapper) |
| `POST /api/v1/trees/{tree_id}/nodes` | 201 | **`{"node":{…},"edge":{…}}`** |
| `POST /api/v1/trees/{tree_id}/nodes/{node_id}/reply` | 201 | **`{"node":{…},"edge":{…}}`** |
| `POST /api/v1/trees/{tree_id}/nodes/{node_id}/fork` | 201 | **`{"node":{…},"edge":{…}}`** |
| `POST /api/v1/trees/{tree_id}/merge` | 201 | **`{"node":{…},"edges":[…],"merged_source_ids":[…]}`** — the single-parent routes above return one `edge`; a merge returns the whole edge set (GAP-078) |
| `POST /api/v1/files/upload` | 201 | **`{"file":{…},"was_new_upload":…,…}`** |
| `POST /api/v1/files/resolve` | 200 | **`{"file":{…},…}`** (same resolve shape) |
| `GET /api/v1/trees` | 200 | **`{"trees":[…],"pagination":{…}}`** |
| `GET /api/v1/trees/{tree_id}/nodes` | 200 | **`{"nodes":[…]}`** |
| `GET /api/v1/nodes/{node_id}/reference-context` | 200 | **bare object** — the §9.3 reference-context envelope (`node_id`, `tree_id`, `parent_mode`, `primary_source_id`, `context{…}`) |
| `GET /api/v1/files` | 200 | **`{"files":[…],"pagination":{…}}`** |
| `GET /api/v1/files/recents` | 200 | **bare array** — `[ {file}, … ]` |
| `GET /api/v1/viewers` | 200 | **bare array** |
| `GET /api/v1/agents` | 200 | **bare array** — the agent roster, `[ {agent}, … ]` |
| `GET /api/v1/agents/{agent_id}` | 200 | **bare object** — the agent, with its `trust_history` |
| `GET /api/v1/reviews` | 200 | **bare array** — the review list, newest first |
| `GET /api/v1/reviews/{review_id}` | 200 | **bare object** — the review, with `blast_radius` and `verdict` |
| `POST /api/v1/reviews/{pr}/trigger` | 200 | **bare object** — the same review detail the read route returns |
| Any failure | 4xx/5xx | **`{"error":{"code":"…","message":"…"}}`** |

### Create tree → bare object

```
POST /api/v1/trees
```
```json
{
  "id": "a1b2c3d4-0000-0000-0000-000000000000",
  "title": "My First Tree",
  "description": "A test tree",
  "owner_id": "00000000-0000-0000-0000-000000000001",
  "root_node_id": "e5f6a7b8-0000-0000-0000-000000000000",
  "node_count": 1,
  "member_count": 1,
  "created_at": "2026-09-14T12:00:00Z",
  "updated_at": "2026-09-14T12:00:00Z",
  "role": "owner"
}
```
The tree object is the WHOLE body — there is no `{"tree": …}` wrapper.

### Create topic → bare object

```
POST /api/v1/topics
```
```json
{
  "id": "b4c5d6e7-0000-0000-0000-000000000000",
  "tree_id": "a1b2c3d4-0000-0000-0000-000000000000",
  "root_node_id": "e5f6a7b8-0000-0000-0000-000000000000",
  "title": "My First Topic",
  "description": "Optional description",
  "slug": "my-first-topic",
  "status": "active",
  "node_count": 1,
  "created_at": "2026-09-14T12:02:00Z"
}
```
Also unwrapped (`POST /api/v1/cards` follows the same bare-object convention).

### Create node / reply / fork → node+edge wrapper

```
POST /api/v1/trees/{tree_id}/nodes
```
```json
{
  "node": {
    "id": "c9d0e1f2-0000-0000-0000-000000000000",
    "treeId": "a1b2c3d4-0000-0000-0000-000000000000",
    "parentId": "e5f6a7b8-0000-0000-0000-000000000000",
    "content": "Hello from the child node!",
    "contentFormat": "markdown",
    "nodeType": "message",
    "sequenceNum": 2,
    "createdAt": "2026-09-14T12:01:00Z"
  },
  "edge": {
    "id": "d0e1f2a3-0000-0000-0000-000000000000",
    "treeId": "a1b2c3d4-0000-0000-0000-000000000000",
    "sourceNodeId": "e5f6a7b8-0000-0000-0000-000000000000",
    "targetNodeId": "c9d0e1f2-0000-0000-0000-000000000000",
    "edgeType": "reply",
    "createdAt": "2026-09-14T12:01:00Z"
  }
}
```
`reply` and `fork` return the identical `{node, edge}` shape (a root node
create — no `parent_id` — returns `"edge": null`). Note the node/edge objects
use **camelCase** field names (`treeId`, `parentId`, `contentFormat`) while the
tree and topic objects above use **snake_case**: the request side accepts both
casings on the node endpoints (see § Nodes), the response side does not unify
them.

### Upload a file → file wrapper

```
POST /api/v1/files/upload
```
```json
{
  "file": { "id": "01a0a32e-…", "sha256": "bdbedd…", "byteSize": 49, "mimeType": "text/markdown", "…": "…" },
  "was_new_upload": true,
  "was_deduped": false,
  "stream_url": "/api/v1/files/01a0a32e-…/stream",
  "expires_at": "2026-09-14T13:00:00Z"
}
```

---

## Auth

### JWT Tokens

Canopy uses HS256 JWTs for authentication. The token must be sent as:

```
Authorization: Bearer <token>
```

**Token claims:**
- `sub` (subject): UUID of the authenticated user
- `iat`: Issued-at timestamp
- `exp`: Expiration timestamp

**Dev JWT** (for local development):
- Secret: `dev-secret-change-me` (matches backend default `JWT_SECRET`)
- User ID: `00000000-0000-0000-0000-000000000001`
- Algorithm: HS256
- The Vite dev proxy auto-injects this token; no manual auth needed in dev mode.

**Auth middleware** (`internal/handler/auth.go`):
- Rejects unsigned tokens (`alg: "none"`)
- Only accepts HS256 (`jwt.WithValidMethods`)
- Extracts `sub` claim as the user UUID; falls back to `user_id` claim
- Public paths: `/health`, `/healthz`, `/version` (no auth required)

**Error codes:**
| Code | HTTP Status | Description |
|------|-------------|-------------|
| `TOKEN_MISSING` | 401 | No Authorization header or not Bearer |
| `TOKEN_INVALID` | 401 | Invalid signature, expired, or bad claims |

### Public Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check → `{"status":"ok","service":"canopyd"}` |
| `GET` | `/healthz` | Same as `/health` |
| `GET` | `/version` | Version → `{"version":"dev"}` |
| `GET` | `/metrics` | Prometheus metrics (if `METRICS_ENABLED=true`) |

---

## Federation Health

`GET /api/v1/federation/health` is JWT-authenticated and reports aggregate
`peers_connected` and `queue_depth` values plus each active peer's ID, state,
pending queue depth, and last heartbeat. It does not expose tokens, key
material, or remote server URLs.

---

## Trees

Mounted at `/api/v1/trees`. All require auth.

### List Trees

```
GET /api/v1/trees
```

**Query params:** `sort`, `status`, `search`, `limit` (int), `cursor` (UUID)

**Tree fields:** each tree carries `last_activity` (RFC3339, optional) — the
`created_at` of the most recent live (non-soft-deleted) node in the tree,
i.e. `MAX(nodes.created_at)` per tree (GAP-094). It is when the conversation
last actually moved; the tree row's `updated_at` only reflects edits to the
tree row itself. Trees with no live nodes omit the field. The dashboard's
"Recent trees" resume list sorts on it (desc) and labels it as
"active `<relative time>`".

**Response (200):**
```json
{
  "trees": [
    {
      "id": "uuid",
      "title": "string",
      "created_at": "RFC3339"
    }
  ],
  "pagination": {
    "nextCursor": "uuid|null",
    "hasMore": false,
    "total": 1,
    "limit": 50
  }
}
```

### Create Tree

```
POST /api/v1/trees
```

**Request body:**
```json
{
  "title": "string (required)",
  "description": "string (optional)",
  "rootMessage": {
    "content": "string (required)",
    "contentFormat": "string (optional, enum: 'markdown'; default 'markdown' — any other value rejected with 400 VALIDATION_ERROR 'invalid content format')",
    "nodeType": "string (optional)"
  }
}
```

**Response (201):** **bare** tree object — tree detail with `root_node_id`,
`owner_id`, `created_at`. There is NO wrapper: the body is the tree itself
(unlike create node, which returns `{"node":…,"edge":…}` — see
[Response envelopes](#response-envelopes-per-route)).

**Error codes:** `INVALID_BODY` (400), `VALIDATION_ERROR` (400), `TOKEN_MISSING` (401)

### Get Tree

```
GET /api/v1/trees/{tree_id}
```

**Query params:** `include_stats` (bool, default true)

**Response (200):** Full tree detail including stats.

**Error codes:** `INVALID_TREE_ID` (400), `TREE_NOT_FOUND` (404), `NOT_TREE_OWNER` (403)

### Update Tree

```
PATCH /api/v1/trees/{tree_id}
```

**Request body:** (partial)
```json
{
  "title": "string (optional)",
  "description": "string (optional)"
}
```

**Response (200):** Updated tree detail.

**Error codes:** `INVALID_BODY` (400), `VALIDATION_ERROR` (400), `NOT_TREE_OWNER` (403), `TREE_NOT_FOUND` (404)

### Delete Tree

```
DELETE /api/v1/trees/{tree_id}
```

**Response:** `204 No Content` (no body).

**Error codes:** `NOT_TREE_OWNER` (403), `TREE_NOT_FOUND` (404)

### Share Tree

```
POST /api/v1/trees/{tree_id}/share
```

Tree sharing is owner-only. The endpoint resolves an invitee by `user_id`
(UUID) first when present, otherwise by `email`; if both are supplied,
`user_id` takes precedence. The handler's role-like request field is named
`permission` (not `role`) and accepts `viewer`, `editor`, or `admin`; an omitted
or unknown value defaults to the least-privileged `viewer` role. `message` is
accepted as an optional client message but is not persisted in the share record.

**Request body:**
```json
{
  "user_id": "uuid (optional)",
  "email": "string (optional)",
  "permission": "viewer | editor | admin (optional)",
  "message": "string (optional)"
}
```

At least one of `user_id` or `email` is required. The response is a bare
share record (not wrapped in `{"share": ...}`):

**Response (201):**
```json
{
  "treeId": "uuid",
  "userId": "uuid",
  "email": "friend@example.com",
  "permission": "viewer",
  "memberId": "uuid"
}
```

`user_id` is resolved before `email`, and a successful share creates the tree
membership immediately; there is no separate tree invite-token response.

**Error codes:** `INVALID_TREE_ID` (400), `INVALID_BODY` (400),
`INVALID_USER_ID` (400), `VALIDATION_ERROR` (400), `TOKEN_MISSING` (401),
`NOT_TREE_OWNER` (403), `TREE_NOT_FOUND` (404), `USER_NOT_FOUND` (404),
`TREE_DELETED` (410), `ALREADY_MEMBER` (409), `SERVICE_UNAVAILABLE` (503),
`NOT_IMPLEMENTED` (501 when the server is built without share dependencies),
`INTERNAL_ERROR` (500).

---

## Nodes

The node API is tree-scoped and membership-gated:

- **Tree-scoped:** `/api/v1/trees/{tree_id}/nodes` — membership-gated via
  `TreeMembershipMiddleware`

**Field naming (GAP-053):** tree-create and topic endpoints use camelCase
(`rootMessage.contentFormat`, `rootMessage.nodeType`). The node
create/reply/fork/update endpoints accept **both** camelCase and snake_case
field names — `contentFormat`/`content_format`, `nodeType`/`node_type`,
`parentId`/`parent_id`, `edgeType`/`edge_type` are equivalent on those four
endpoints (if both casings are sent, snake_case wins). Requests remain
otherwise strict: any other unknown field returns `400 INVALID_BODY` with the
offending field named in the error message (e.g.
`request body contains an unknown field "contentFormat"`), not the generic
"request body must be valid JSON".

### List Nodes (Tree-scoped)

```
GET /api/v1/trees/{tree_id}/nodes
```

**Response (200):**
```json
{
  "nodes": [
    {
      "id": "uuid",
      "tree_id": "uuid",
      "parent_id": "uuid|null",
      "content": "string",
      "content_format": "string",
      "node_type": "string",
      "author_id": "uuid",
      "sequence_num": 1,
      "created_at": "RFC3339",
      "updated_at": "RFC3339|null",
      "deleted_at": "RFC3339|null"
    }
  ]
}
```

### Create Node (Tree-scoped)

```
POST /api/v1/trees/{tree_id}/nodes
```

**Request body:**
```json
{
  "parent_id": "uuid (optional — OMIT the field to create a root node; a present-but-empty value is rejected: 400 INVALID_PARENT_ID 'parent_id must not be empty (omit the field to create a root node)')",
  "content": "string (required, max 64KB)",
  "content_format": "string (optional, enum: 'markdown'; default 'markdown' — any other value rejected with 400 VALIDATION_ERROR 'invalid content format')",
  "node_type": "string (optional, default 'message')",
  "edge_type": "string (optional, default 'reply')",
  "metadata": "object (optional)"
}
```

`parent_id` is validated **before** the content rules, so `{"parent_id":""}`
alone reports `INVALID_PARENT_ID` naming the field rather than a content error
(GAP-072). `"parent_id": null` is treated as omitted (root node).

**Response (201):** `{ "node": {...}, "edge": {...} }` — the created node and
the edge connecting it to its parent. A root node (no `parent_id`) has no
parent edge, so the response is `{ "node": {...}, "edge": null }` (BUG-029).

**Error codes:** `INVALID_TREE_ID` (400), `INVALID_BODY` (400), `EMPTY_CONTENT` (400),
`CONTENT_TOO_LARGE` (400), `INVALID_PARENT_ID` (400), `VALIDATION_ERROR` (400),
`NOT_FOUND` (404), `GONE` (410), `CONFLICT` (409), `FORBIDDEN` (403)

### Get Node

```
GET /api/v1/trees/{tree_id}/nodes/{node_id}
```

**Response (200):** Full node detail.

**Error codes:** `INVALID_NODE_ID` (400), `NODE_NOT_FOUND` (404), `NOT_TREE_MEMBER` (403)

### Update Node

```
PATCH /api/v1/trees/{tree_id}/nodes/{node_id}
```

**Request body:** (partial)
```json
{
  "content": "string (optional)",
  "content_format": "string (optional, enum: 'markdown'; default 'markdown' — any other value rejected with 400 VALIDATION_ERROR 'invalid content format')",
  "metadata": "object (optional)",
  "pinned": "boolean (optional)"
}
```

`pinned` is merged into the node's `metadata` object rather than replacing it:
`true` sets `metadata.pinned = true`, `false` removes that key, and **every other
metadata key is preserved** — including the reserved
`metadata.multi_reference` object written on multi-reference nodes
(SPEC-PL-06), which survives byte-for-byte. Other keys are never touched, so a
pin never clobbers reserved metadata. When `metadata` is present in the same
body it is applied first and the pin merges on top of it. The value must be a
JSON boolean (or `null`, meaning "not provided"); any other type is a 400
`INVALID_BODY` naming the field.

The context compiler never drops a pinned message from the compiled context: a
pinned node is exempt from the token-budget walk, so it is always included and
never counted in `omittedCount`. When pinned content alone exceeds the budget
the compiled `tokensUsed` may exceed `tokenBudget` — the overage is reported in
`manifest.warnings` (e.g. `"pinned nodes exceed the token budget by 137
tokens"`), and `manifest.pinnedCount` reports how many pinned ancestry items
were kept.

**Response (200):** Updated node detail.

### Delete Node

```
DELETE /api/v1/trees/{tree_id}/nodes/{node_id}
```

**Response (200):** Soft-deleted node detail (includes `deleted_at`).

### Reply to Node

```
POST /api/v1/trees/{tree_id}/nodes/{node_id}/reply
```

**Request body:**
```json
{
  "content": "string (required)",
  "content_format": "string (optional, enum: 'markdown'; default 'markdown' — any other value rejected with 400 VALIDATION_ERROR 'invalid content format')",
  "node_type": "string (optional)",
  "metadata": "object (optional)"
}
```

**Response (201):** `{ "node": {...}, "edge": {...} }`

### Fork from Node

```
POST /api/v1/trees/{tree_id}/nodes/{node_id}/fork
```

**Request body:**
```json
{
  "content": "string (required)",
  "content_format": "string (optional, enum: 'markdown'; default 'markdown' — any other value rejected with 400 VALIDATION_ERROR 'invalid content format')",
  "node_type": "string (optional)",
  "metadata": "object (optional)"
}
```

**Response (201):** `{ "node": {...}, "edge": {...} }` — creates a new branch
from the source node.

**Leaf rule:** the source node must already have at least one child — forking a
leaf returns `400 VALIDATION_ERROR` ("fork requires parent with at least one
child"), since a leaf fork would be indistinguishable from a reply
(SPEC-API-03 §7.3).

**Error codes:** `INVALID_NODE_ID` (400), `EMPTY_CONTENT` (400), `INVALID_BODY` (400),
`VALIDATION_ERROR` (400), `NOT_FOUND` (404), `GONE` (410), `CONFLICT` (409),
`FORBIDDEN` (403), `TOKEN_MISSING` (401)

**Empty body (GAP-072):** `content` is required. An empty body, a whitespace-only
body, `{}`, or a body omitting `content` returns `400 EMPTY_CONTENT` with the
message `content is required` — the field is named instead of the previous
misleading `400 INVALID_BODY "request body must be valid JSON"`. Malformed JSON
(no JSON at all, truncated, unknown field) still returns `400 INVALID_BODY`.

### Merge Tree (Create Synthesis Node)

```
POST /api/v1/trees/{tree_id}/merge
```

Tree-scoped and membership-gated like the other tree-scoped write routes
(SPEC-API-04 §3). Creates ONE `node_type: "synthesis"` node with multiple
parents: one `reply` edge from the placement target plus one `synthesis` edge
per source node, all in a single transaction.

This is the **only** way to create a synthesis node: the ordinary node-create
and reply/fork routes reject `node_type: "synthesis"` with `400 VALIDATION_ERROR`
(message `node service: synthesis nodes via merge endpoint only` — the service
sentinel `ErrSynthesisViaMergeOnly`; the SPEC-API-07 catalog name for the
condition is `SYNTHESIS_VIA_MERGE_ONLY`, which is not the code the node handler
currently emits). The multi-reference reply route
(`POST /api/v1/trees/{tree_id}/multi-reference-replies`, SPEC-PL-06 §9.2) is a
different surface: it creates a `message` node whose parents are `reference`
edges.

**Request body:**

```json
{
  "source_node_ids": [
    "0191a8b2-7fff-7000-9000-000000000101",
    "0191a8b2-7fff-7000-9000-000000000102"
  ],
  "content": "Synthesizing the two approaches:\n\nConclusion: use CTEs with index optimization for MVP.",
  "content_format": "markdown",
  "target_parent_id": "0191a8b2-7fff-7000-9000-000000000001",
  "metadata": {
    "merge_summary": "Resolved branch divergence on tree storage strategy",
    "decision": "CTE with index optimization"
  }
}
```

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `source_node_ids` | array of UUID strings | Yes | — | 2–100 sources. Each must exist in this tree, not be soft-deleted, and be unique. A source may itself be a synthesis node (chained merges). |
| `content` | string | Yes | — | Synthesis summary; ≤ 65536 characters. May be empty. |
| `content_format` | string | No | `"markdown"` | `markdown`, `plain`, or `rich`. |
| `target_parent_id` | UUID string | No | the tree's root node | Where the synthesis node is placed. Must exist in this tree and not be soft-deleted. Must not be one of the sources. |
| `metadata` | object | No | `{}` | ≤ 16 KB serialized (measured compactly). |

**Response (201):** `{"node": {…}, "edges": [ … ], "merged_source_ids": [ … ]}`.

```json
{
  "node": {
    "id": "0191a8b2-7fff-7000-9000-000000000301",
    "tree_id": "0191a8b2-7fff-7000-9000-000000000001",
    "parent_id": "0191a8b2-7fff-7000-9000-000000000001",
    "author_id": "0191a8b2-7fff-7000-9000-000000000042",
    "author_display_name": "Bane",
    "content": "Synthesizing the two approaches:...",
    "content_format": "markdown",
    "node_type": "synthesis",
    "sequence_num": 312,
    "metadata": {"merge_summary": "Resolved branch divergence on tree storage strategy"},
    "depth": 1,
    "child_count": 0,
    "created_at": "2026-09-18T23:15:00Z",
    "edited_at": null,
    "deleted_at": null
  },
  "edges": [
    {"id": "…401", "tree_id": "…", "source_node_id": "<target_parent_id>", "target_node_id": "<node.id>", "edge_type": "reply", "created_at": "…"},
    {"id": "…402", "tree_id": "…", "source_node_id": "<source 1>", "target_node_id": "<node.id>", "edge_type": "synthesis", "created_at": "…"},
    {"id": "…403", "tree_id": "…", "source_node_id": "<source 2>", "target_node_id": "<node.id>", "edge_type": "synthesis", "created_at": "…"}
  ],
  "merged_source_ids": ["<source 1>", "<source 2>"]
}
```

`edges` always holds **N+1** entries in creation order: the `reply` edge from
`target_parent_id` first, then one `synthesis` edge per source in the order
`source_node_ids` was sent. `node.depth` is `target_parent.depth + 1` and
`node.child_count` is always `0` on creation.

**SSE (SPEC-API-04 §3.8):** one `node_added`, then N+1 `edge_added` events (in
the same order as `edges`), then a composite `tree_merged`
(`{tree_id, merge_node_id, source_node_ids, timestamp}`) as the last event, all
on the tree's event stream. The event set is purely additive — clients that
only understand `node_added`/`edge_added` keep working.

**Error codes** (all use the standard `{"error":{"code":…,"message":…}}`
envelope): `INVALID_TREE_ID` (400), `INVALID_BODY` (400),
`INVALID_SOURCE_NODE_IDS` (400), `MIN_SOURCE_NODES` (400),
`MAX_SOURCE_NODES` (400), `DUPLICATE_SOURCE_NODES` (400),
`INVALID_SOURCE_NODE_ID` (400), `SOURCE_NODE_NOT_FOUND` (404),
`SOURCE_NODE_DELETED` (410), `TREE_MISMATCH` (400), `CONTENT_TOO_LONG` (400),
`INVALID_CONTENT_FORMAT` (400), `METADATA_TOO_LARGE` (400),
`INVALID_TARGET_PARENT_ID` (400), `TARGET_PARENT_NOT_FOUND` (404),
`TARGET_PARENT_DELETED` (409), `SOURCE_TARGET_OVERLAP` (400),
`REQUEST_TOO_LARGE` (413), `NOT_TREE_MEMBER` (403), `TREE_DELETED` (410),
`TREE_NOT_FOUND` (404), `VALIDATION_ERROR` (400, non-object `metadata`),
`TOKEN_MISSING` (401), `SERVICE_UNAVAILABLE` (503), `RATE_LIMITED` (429 — the
global per-IP limiter, **or** the per-user merge budget below).

**Rate limit (SPEC-API-04 §13, §14.3).** This endpoint is limited to **10
requests per minute per user** (merges are heavyweight operations), counted in
a rolling 60-second window keyed on the authenticated user id. The 11th request
in that window answers `429` with the `RATE_LIMITED` code, a `Retry-After`
header, and the identity's `retry_after_seconds` field (SPEC-API-07) —
`{"error":{"code":"RATE_LIMITED","message":…,"retry_after_seconds":<n>}}` — and
writes nothing. Other users and other routes are unaffected. (Until this
change the §13 per-user limit was documented here as unimplemented; the global
per-IP limiter — 100 req/s, burst 200 — still covers every route and is
unchanged.)

A rejected merge writes nothing: node, parent edge and synthesis edges are
committed together or not at all.

### Reference Selection Preflight (SPEC-PL-06 §9.1)

```
POST /api/v1/trees/{tree_id}/reference-selections
```

Tree-scoped and membership-gated like the other tree-scoped write routes. This
stateless preflight validates the ordered source selection, writes no graph rows,
and returns a signed selection token valid for five minutes plus a preview
manifest. Treat the selection token as opaque: do not parse or log it.

**Request body:**

```json
{
  "source_node_ids": [
    "0191a8b2-7fff-7000-9000-000000000101",
    "0191a8b2-7fff-7000-9000-000000000102"
  ],
  "primary_source_id": "0191a8b2-7fff-7000-9000-000000000101",
  "profile_context_budget": 2048
}
```

- `source_node_ids` — required array of UUID strings, 1..N entries, in
  selection order; order is preserved and matters.
- `primary_source_id` — optional UUID string; when present, it must be one of
  `source_node_ids`.
- `profile_context_budget` — optional integer token budget for the preview.

Source IDs are decoded as strings server-side. A malformed ID returns the
catalog error `REFERENCE_SOURCE_INVALID` (400), rather than a generic body
error.

**Response (200):**

```json
{
  "selection_token": "mrs.v1.<opaque-payload>.<opaque-mac>",
  "expires_at": "2026-09-21T13:07:00Z",
  "tree_id": "0191a8b2-7fff-7000-9000-000000000001",
  "canonical_source_ids": [
    "0191a8b2-7fff-7000-9000-000000000101",
    "0191a8b2-7fff-7000-9000-000000000102"
  ],
  "primary_source_id": "0191a8b2-7fff-7000-9000-000000000101",
  "is_synthetic_merge_point": true,
  "branch_span": {
    "common_ancestor_id": "0191a8b2-7fff-7000-9000-000000000001",
    "source_branches": [
      {
        "source_id": "0191a8b2-7fff-7000-9000-000000000101",
        "branch_root_id": "0191a8b2-7fff-7000-9000-000000000011",
        "distance_from_root": 4
      },
      {
        "source_id": "0191a8b2-7fff-7000-9000-000000000102",
        "branch_root_id": "0191a8b2-7fff-7000-9000-000000000012",
        "distance_from_root": 3
      }
    ]
  },
  "context_budget": {
    "available_tokens": 2048,
    "minimum_required_tokens": 512,
    "estimated_tokens": 480,
    "fits": true
  },
  "sources": [
    {
      "node_id": "0191a8b2-7fff-7000-9000-000000000101",
      "source_label": "R1",
      "color_key": "blue",
      "branch_root_id": "0191a8b2-7fff-7000-9000-000000000011",
      "content_hash": "91a2e5d22c17e5870f61ea6e9d501da80c2ac2735d15d5f3b6efb87c8c92856f",
      "sequence_num": 101,
      "content_preview": "First selected source preview..."
    },
    {
      "node_id": "0191a8b2-7fff-7000-9000-000000000102",
      "source_label": "R2",
      "color_key": "green",
      "branch_root_id": "0191a8b2-7fff-7000-9000-000000000012",
      "content_hash": "a2b3c4d5e6f7890123456789abcdef0123456789abcdef0123456789abcdef01",
      "sequence_num": 102,
      "content_preview": "Second selected source preview..."
    }
  ]
}
```

`canonical_source_ids` is deduplicated and returned in canonical order.
`primary_source_id` identifies the selected primary source. `branch_span` is
present only when `is_synthetic_merge_point` is `true`. `sources` contains one
entry per selected source in selection order.

**Error codes:** `REFERENCE_SOURCE_COUNT_TOO_LOW` (400),
`REFERENCE_SOURCE_COUNT_TOO_HIGH` (400), `REFERENCE_SOURCE_DUPLICATE` (400),
`REFERENCE_SOURCE_INVALID` (400), `REFERENCE_SOURCE_NOT_FOUND` (404),
`REFERENCE_SOURCE_DELETED` (410), `REFERENCE_SOURCE_SYSTEM_FORBIDDEN` (400),
`REFERENCE_TREE_MISMATCH` (400), `REFERENCE_PRIMARY_NOT_SELECTED` (400),
`REFERENCE_CONTEXT_BUDGET_EXCEEDED` (422), `NOT_TREE_MEMBER` (403),
`TREE_DELETED` (410), `TREE_NOT_FOUND` (404), `TOKEN_MISSING` (401),
`SERVICE_UNAVAILABLE` (503).

#### Handoff to §9.2

Pass the opaque `selection_token` from this response to the create route within
its five-minute lifetime. The token is consumed by
`POST /api/v1/trees/{tree_id}/multi-reference-replies`:

```json
{
  "selection_token": "mrs.v1.<opaque-payload>.<opaque-mac>",
  "content": "The reply content...",
  "content_format": "markdown",
  "metadata": {},
  "request_id": "0191a8b2-7fff-7000-9000-000000000901"
}
```

The create route answers `201` with the node, the N reference edges, and the
reference-context summary. An invalid token returns
`REFERENCE_SELECTION_TOKEN_INVALID` (400); an expired token returns
`REFERENCE_SELECTION_TOKEN_EXPIRED` (410).

### Get Reference Context (SPEC-PL-06 §9.3)

```
GET /api/v1/nodes/{node_id}/reference-context
```

Flat node surface (bare node id, SPEC-API-03 §6). Returns the **stored**
provenance of a multi-reference reply: the sources that were compiled into it,
in the order they were selected, with the branch span and token accounting.
Tree membership is resolved from the target node's own tree — the flat surface
carries no `tree_id` segment for `TreeMembershipMiddleware`.

A multi-reference reply is created through the two write endpoints of the same
spec (§9.1 `POST /api/v1/trees/{tree_id}/reference-selections` preflight, §9.2
`POST /api/v1/trees/{tree_id}/multi-reference-replies` create).

**Merge endpoint — resolved (2026-09-18, GAP-078).** This paragraph previously
reported the spec'd `POST /trees/{tree_id}/merge` as unimplemented. It is now
implemented (see § Merge Tree (Create Synthesis Node) above, SPEC-API-04 §3),
and the two paths are complementary rather than substitutes:
`POST /api/v1/trees/{tree_id}/merge` creates a `synthesis` node whose parents
are one `reply` edge plus N `synthesis` edges, while
`POST /api/v1/trees/{tree_id}/multi-reference-replies` (SPEC-PL-06 §9.2,
preceded by `POST /api/v1/trees/{tree_id}/reference-selections` §9.1) creates a
`message` node whose parents are `reference` edges. A multi-reference reply may
report `is_synthetic_merge_point: true` when its selection spans diverged
branches, but it is a `message`, not a `synthesis` node.

**Query params:**
- `include_content` — bool, default `true`. When `false` each source's
  `content` key is **omitted** (not returned empty), so a client can tell
  "not asked for" from "empty source".
- `max_source_tokens` — int, default = the source allocation recorded at
  creation (the §6.2 per-source ceiling of 2,048 tokens); a larger value is
  clamped to 2,048. A truncated source keeps its head and tail around the
  §6.1 omission marker `[... N tokens omitted from source RN ...]`.
- `verify_hash` — bool, default `true`. When a source's current content no
  longer matches the snapshot the manifest hash was built from (or a selected
  source was soft-deleted), the response adds
  `source_changed_since_creation: true`.

**Response (200):** the §9.3 envelope. `context.manifest_hash` is the value
persisted at creation and is **never recomputed** at read time — it is the
provenance record; `verify_hash` only compares a digest against it.

```json
{
  "node_id": "0191a8b2-7fff-7000-9000-000000000301",
  "tree_id": "0191a8b2-7fff-7000-9000-000000000001",
  "parent_mode": "multi_reference",
  "primary_source_id": "0191a8b2-7fff-7000-9000-000000000101",
  "context": {
    "sources": [
      {"source_label": "R1", "node_id": "0191a8b2-7fff-7000-9000-000000000101", "content": "…", "truncated": false},
      {"source_label": "R2", "node_id": "0191a8b2-7fff-7000-9000-000000000202", "content": "…", "truncated": false}
    ],
    "is_synthetic_merge_point": true,
    "branch_span": {"common_ancestor_id": "0191a8b2-7fff-7000-9000-000000000001", "source_branches": []},
    "token_budget": 8192,
    "tokens_used": 1460,
    "manifest_hash": "91a2e5d22c17e5870f61ea6e9d501da80c2ac2735d15d5f3b6efb87c8c92856f"
  }
}
```

`source_changed_since_creation: true` is present only in that case; the field is
absent on an unchanged snapshot.

**Error codes:** `INVALID_NODE_ID` (400), `TOKEN_MISSING` (401),
`NOT_TREE_MEMBER` (403), `REFERENCE_CONTEXT_NOT_FOUND` (404),
`INVALID_INCLUDE_CONTENT` / `INVALID_VERIFY_HASH` / `INVALID_MAX_SOURCE_TOKENS`
(400), `SERVICE_UNAVAILABLE` (503).

**404 semantics (§9.3):** `REFERENCE_CONTEXT_NOT_FOUND` is the answer for a node
that is not a multi-reference reply **and** for a node that does not exist —
identical status, code and message, so the route is deliberately not an
existence oracle.

---

## Edges

**Spec drift:** The README documents `/api/v1/edges` endpoints, but the code
does NOT register a standalone EdgeHandler. Edges are created implicitly as a
side-effect of node creation (reply, fork) and are managed through the node
endpoints. The `EdgeRepo` exists in the data layer and edges are returned in
node creation responses, but there are no standalone edge CRUD routes.

---

## Graph

Mounted at `/api/v1/graph`. All require auth.

### Get Subtree

```
GET /api/v1/graph/trees/{tree_id}/subtree/{node_id}
```

**Query params:** `max_depth` (int, default 0 = unbounded)

**Response (200):**
```json
{
  "nodes": [
    { "id": "uuid", "parent_id": "uuid|null", "type": "string", "depth": 0 }
  ],
  "edges": [
    {
      "id": "uuid",
      "source_id": "uuid",
      "target_id": "uuid",
      "edge_type": "string",
      "metadata": { "reference_index": 0, "source_label": "R1", "color_key": "ref-6", "selection_order": 0, "role": "context_source" }
    }
  ]
}
```

`edges[].id` is the persisted `edges.id` — React Flow renders convergence edges
with the database identity (SPEC-PL-06 §7.1). `edges[].metadata` is the decoded
JSONB object, omitted entirely when the column holds the `{}` default;
`reference` edges carry the §5.2 renderer metadata (`reference_index`,
`source_label`, `color_key`, `selection_order`, `role`).

**Error codes:** `NODE_NOT_FOUND` (404), `SUBTREE_ERROR` (500)

### Get Ancestors

```
GET /api/v1/graph/trees/{tree_id}/ancestors/{node_id}
```

**Response (200):** Same shape as subtree — the path from node to root.

### Get Graph Stats

```
GET /api/v1/graph/trees/{tree_id}/stats
```

**Response (200):** Aggregate graph statistics for the tree.

---

## Topics

Mounted at `/api/v1/topics`. All require auth.

### List Topics

```
GET /api/v1/topics
```

**Query params:** `tree_id` (UUID, required), `status` (string), `limit` (int, default 50), `offset` (int, default 0)

**Response (200):**
```json
{
  "topics": [
    {
      "id": "uuid",
      "tree_id": "uuid",
      "title": "string",
      "description": "string",
      "status": "string",
      "created_at": "RFC3339"
    }
  ]
}
```

### Create Topic

```
POST /api/v1/topics
```

**Request body:**
```json
{
  "treeId": "uuid (required)",
  "rootNodeId": "uuid (required)",
  "title": "string (required)",
  "description": "string (optional)"
}
```

**Response (201):** Created topic detail — a **bare** object (no
`{"topic": …}` wrapper; see [Response envelopes](#response-envelopes-per-route)).

### Get Topic

```
GET /api/v1/topics/{topic_id}
```

**Response (200):** Topic detail.

### Update Topic

```
PATCH /api/v1/topics/{topic_id}
```

**Request body:** (partial)
```json
{
  "title": "string (optional)",
  "description": "string (optional)",
  "status": "string (optional)"
}
```

**Response (200):** Updated topic.

### Archive Topic

```
DELETE /api/v1/topics/{topic_id}
```

**Response (200):** Archived topic detail.

---

## Cards

Mounted at `/api/v1/cards`. All require auth. Cards are structured data nodes
stored in **per-type SQLite databases** (`modernc.org/sqlite`, CGo-free, pure Go)
under `~/.hermes/canopy/cards/`, overridable with `CANOPY_CARD_DATA_DIR`.
DuckDB is **not** used: `internal/card/duckdb/` is cgo-only and has zero importers
repo-wide — it is archived, not the card backend.

### List Cards

```
GET /api/v1/cards
```

**Query params:** `tree_id` (UUID), `node_id` (UUID), `card_type` (string),
`limit` (int, default 50), `offset` (int, default 0)

**Response (200):**
```json
{
  "cards": [
    {
      "id": "uuid",
      "tree_id": "uuid",
      "node_id": "uuid",
      "app_id": "string",
      "card_type": "string",
      "data": {},
      "created_at": "RFC3339"
    }
  ]
}
```

### Create Card

```
POST /api/v1/cards
```

**Request body:**
```json
{
  "treeId": "uuid (required)",
  "nodeId": "uuid (required)",
  "appId": "string (required)",
  "cardType": "string (required)",
  "data": "object (required)"
}
```

**Response (201):** Created card detail.

### Get Card

```
GET /api/v1/cards/{card_id}
```

**Response (200):** Card detail.

### Update Card

```
PATCH /api/v1/cards/{card_id}
If-Match: {revision}
```

`If-Match` is required on every PATCH. A missing or blank header returns `428
CARD_REVISION_REQUIRED`; a non-negative integer that differs from the current
revision returns `412 CARD_REVISION_CONFLICT` and includes the current card
snapshot in the JSON response. A non-integer header returns `400
CARD_REVISION_INVALID`.

**Request body:** a non-empty object containing one or more mutable fields:
```json
{
  "data": "object (optional)",
  "actions": "array (optional)",
  "context_hash": "string (optional)",
  "status": "active | dismissed | archived (optional)"
}
```

An empty object returns `400 CARD_PATCH_EMPTY`. Status transitions follow the
lifecycle state machine:

| Current status | PATCH `status` | Event | Result |
|---|---|---|---|
| `active` | `dismissed` | `card_dismissed` | sets `dismissed_at` |
| `dismissed` | `active` | `card_restored` | clears `dismissed_at` |
| `active` or `dismissed` | `archived` | `card_archived` | sets `archived_at` |
| `archived` | any | none | `409 CARD_STATUS_ARCHIVED` |

Data/action/context mutations on a dismissed card return `409
CARD_STATUS_DISMISSED`. Invalid status transitions return `409
CARD_INVALID_TRANSITION`.

**Response (200):** Updated card.

### Archive Card

```
DELETE /api/v1/cards/{card_id}
If-Match: {revision}
```

`If-Match` is required with the same `428 CARD_REVISION_REQUIRED` and `412
CARD_REVISION_CONFLICT` semantics as PATCH. DELETE archives an active or
dismissed card and records `card_archived`; an archived card is terminal and
returns `409 CARD_STATUS_ARCHIVED`.

**Response (204):** No content.

### Submit Card Action

```
POST /api/v1/cards/{card_id}/actions
```

Submits one of the card's declared actions. `handler` must match a
`actions[].handler` value on the card, otherwise the request is rejected with
`422 CARD_ACTION_NOT_DECLARED` and **nothing** is written. A declared action is
recorded as an `action_requested` event, then executed and recorded as exactly
one terminal event: `action_completed` on success, `agent_error` on execution
failure.

Execution is an event-backed boundary (SPEC-PL-03 §4.3): the durable event pair
*is* the record, and Canopy never runs arbitrary server-side code for a card
action. A handler with no registered app adapter completes deterministically
with the payload it was sent.

**Request body:**
```json
{
  "handler": "string (required) — one of the card's declared actions[].handler",
  "payload": "object (optional) — action input, defaults to {}"
}
```

**Response (200):**
```json
{
  "card_id": "uuid",
  "handler": "string",
  "status": "completed",
  "requested_seq": 2,
  "result_seq": 3,
  "result_event_id": "uuid",
  "result_event_type": "action_completed",
  "payload": {}
}
```

`status` is `completed` or `error`; on `error` the response also carries
`error` (the failure reason) and `result_event_type` is `agent_error`.

**Errors:** `400 INVALID_JSON` (body is not valid JSON, or has unknown fields),
`400 MISSING_HANDLER` (no handler), `400 INVALID_PAYLOAD` (payload is not a JSON
object), `400 INVALID_CARD_ID`, `404 CARD_NOT_FOUND`, `422
CARD_ACTION_NOT_DECLARED`, `500 CARD_ACTION_FAILED` (execution failed and the
`agent_error` event was recorded), `500 CARD_ACTION_ERROR`.

### Stream Card Events

```
GET /api/v1/cards/{card_id}/events
```

Server-Sent Events stream of a card's activity log (SPEC-PL-03 §9). This repo's
card surface is the single-id form, so the card type is taken from the stored
card rather than from a path segment.

**Query params:** `after_sequence` (non-negative int, default 0) — replay
stored events with a higher sequence

**Headers:** `Last-Event-ID` (non-negative int) — the sequence last seen by the
client, as sent automatically by `EventSource` on reconnect

The replay cursor is the **larger** of `after_sequence` and `Last-Event-ID`.
Either one being non-integer or negative is a `400 CARD_SSE_CURSOR_INVALID`
JSON response written *before* any SSE header.

Every successful connection writes, in order:

1. one `card_snapshot` frame carrying the current materialised card (the same
   card JSON the REST card routes return), so a fresh client renders without
   replaying history. The snapshot frame carries no `id:` line on purpose: it
   is not a position in the event log, so a client that reconnects mid-replay
   keeps its previous `Last-Event-ID` instead of skipping events it never
   received — the card's sequence is in `data.last_event_seq`.
2. one `card_event` frame per stored event after the cursor, in sequence order.
   `id:` is the event's sequence and `data:` is compact single-line JSON.
3. later events appended while the connection is open, streamed live.

An idle connection receives a `heartbeat` event every 30 seconds. The card SSE
handler resets its write deadline for each frame, so an idle stream stays open
past the server's 30-second `WriteTimeout`; a stuck connection is dropped by the
bounded per-write deadline. Long-lived SSE routes are exempt from the server's
global 60-second request timeout (see the tree events section), so a stream can
stay open indefinitely; ordinary requests keep the 60s cap.

**Frame shape:**
```text
id: 42
event: card_event
data: {"event_type":"card_event","card_id":"uuid","card_type":"compact","sequence":42,"timestamp":"RFC3339","data":{"sequence":42,"event_id":"uuid","card_id":"uuid","event_type":"agent_progress","actor_kind":"agent","actor_id":"coding","payload":{},"created_at":"RFC3339"}}
```

`event:` is `card_event` for a stored event, `card_snapshot` for the
materialised card, or `heartbeat` for liveness. The `data:` body is always one
line.

**Errors:** `400 CARD_SSE_CURSOR_INVALID` (cursor is not a non-negative
integer), `413 CARD_SSE_BACKLOG_LIMIT` (the replay would exceed 10,000 events —
GET the card snapshot and reconnect with a fresh cursor), `404
CARD_NOT_FOUND`, `400 INVALID_CARD_ID`.

#### Client stream consumption

The client half of SPEC-PL-03 §5/§9 lives in four files:

| File | Role |
|------|------|
| `frontend/src/types/card.ts` | zod schemas for the three wire shapes + canonical §5.1 types + wire→canonical adapters |
| `frontend/src/lib/cardStore.ts` | pure §5.3 reducer (snapshot ordering, exact-sequence append, gap → replay, rejections) |
| `frontend/src/hooks/useCardStream.ts` | one live subscription per card, driving the store |
| `frontend/src/components/CardActivityPanel.tsx` | §6.1 default frame + live event list + connection indicator |

Transport is `subscribeSse` (`frontend/src/lib/sse.ts`) — the app's only SSE
client, because native `EventSource` cannot send the `Authorization` header.
The hook subscribes to `/cards/{card_id}/events` (adding `?after_sequence={n}`
once it holds a cursor) with `eventTypes: ['card_snapshot', 'card_event',
'heartbeat']`, validates every `data:` line, and maps the wire's snake_case onto
the canonical camelCase types.

Client rules (`frontend/src/lib/cardStore.ts`, `frontend/src/types/card.ts`):

- a `card_snapshot` is applied only when its ordering key is **strictly newer**
  than the stored one. The wire carries no `revision` field, so the key is the
  summary's `last_event_seq` (exposed as the canonical `Card.revision`);
- a snapshot **never advances the cursor** — it carries no `id:` line, so
  `lastSequence` is untouched;
- a `card_event` is appended only when `sequence === lastSequence + 1`; a
  duplicate (`sequence <= lastSequence`) is a no-op, since the server already
  dedupes across its own replay/live boundary;
- a gap (`sequence > lastSequence + 1`) is **not** applied and never buffered:
  the client re-subscribes once with `after_sequence = lastSequence` and records
  that cursor so the same gap cannot loop;
- a payload that fails validation becomes a non-destructive error entry (shown
  as a banner in the activity panel) carrying its envelope `sequence` and, when
  known, the nested `event_id`; the card and event state are left untouched. The
  SSE `id:` line itself is not available at this layer — `lib/sse.ts` parses and
  discards it;
- `heartbeat` (which carries no `card_type` and sequence 0) only promotes the
  connection indicator to *live*; it is not an event.

In the UI, clicking a card row on the Cards page opens the activity panel for
that card; exactly one stream subscription exists at a time (the panel owns it,
not the row).

---

## Approvals

Mounted at `/api/v1/approvals`. All require auth.

### List All Approvals

```
GET /api/v1/approvals
```

**Query params:** `limit` (int), `offset` (int)

**Response (200):**
```json
{
  "approvals": [...],
  "total": 0,
  "limit": 50,
  "offset": 0
}
```

### List Pending Approvals

```
GET /api/v1/approvals/pending
```

**Query params:** `tree_id` (UUID, optional), `limit` (int), `offset` (int)

**Response (200):** Same envelope as list all.

### List Approval History

```
GET /api/v1/approvals/history
```

**Query params:** `limit` (int), `offset` (int)

**Response (200):** Same envelope as list all.

### Get Approval

```
GET /api/v1/approvals/{approval_id}
```

**Response (200):** Approval detail.

### Approve

```
POST /api/v1/approvals/{approval_id}/approve
```

**Response (200):** Updated approval.

### Deny

```
POST /api/v1/approvals/{approval_id}/deny
```

**Response (200):** Updated approval.

---

## Export / Import

Registered directly on the `/api/v1/trees` router (not via Mount).

### Export Tree

```
GET /api/v1/trees/{tree_id}/export
```

**Response (200):** Full tree export as JSON (tree + nodes + edges).

### Import Tree

```
POST /api/v1/trees/import
```

**Request body:** Full export JSON (from Export Tree).

**Response (201):** `{ "tree_id": "uuid" }` with `Location` header.

---

## Sync

Mounted at `/api/v1/trees/{tree_id}/sync`. Membership-gated.

### Get Sync Delta

```
GET /api/v1/trees/{tree_id}/sync?sinceHash=<sha256>
```

**Response (200):** Delta object. Returns `204 No Content` if no changes since hash.

### Trigger Snapshot

```
POST /api/v1/trees/{tree_id}/sync/snapshot
```

**Response (201):** Created snapshot.

---

## SSE Events

### Tree Events Stream

```
GET /api/v1/trees/{tree_id}/events
```

**Query params:**
- `since` — SHA256 hex hash (64 chars) for replay
- `profiles` — comma-separated UUIDs to filter by profile
- `include_heartbeat` — bool, default true

**Response:** SSE stream (`text/event-stream`). Event types:

| Event Type | Description |
|------------|-------------|
| `node_added` | A node was created |
| `node_updated` | A node was updated |
| `node_removed` | A node was soft-deleted |
| `edge_added` | An edge was created — for a multi-reference reply, one full `reference` edge per source, ordered by `metadata.selection_order` (SPEC-PL-06 §10.1) |
| `edge_removed` | An edge was deleted |
| `tree_created` | A tree was created |
| `tree_updated` | A tree was updated |
| `tree_deleted` | A tree was deleted |
| `multi_reference_converged` | A multi-reference reply and all N of its reference edges are committed (SPEC-PL-06 §10.2 composite) |

Heartbeat: `: heartbeat` (SSE comment, every 30s).

**Request timeout exemption:** long-lived SSE routes — every GET path ending in
`/events` or `/feed` (this endpoint, the card, plugin, federation, MLS, and
gateway run streams, and the workspace channel feed) — are exempt from the
server's global 60-second request timeout; a stream may stay open
indefinitely. Ordinary requests, including `POST /api/v1/federation/events` and
`GET /api/v1/federation/events/replay`, keep the 60s cap.

**Connection limits:** 10 per user, 100 per tree, 10,000 server-wide.

**Error codes (pre-SSE, returned as JSON):** `INVALID_TREE_ID` (400),
`INVALID_SINCE_HASH` (400), `INVALID_PROFILE_ID` (400),
`TOO_MANY_CONNECTIONS_TREE` (429), `TOO_MANY_CONNECTIONS` (503),
`STREAMING_NOT_SUPPORTED` (500), `SUBSCRIPTION_FAILED` (500)

#### Multi-reference convergence (SPEC-PL-06 §10)

`POST /api/v1/trees/{tree_id}/multi-reference-replies` publishes, **only after
commit** and in this exact order:

1. `node_added` — the reply itself (`parent_mode: "multi_reference"`). First.
2. exactly **N** `edge_added` events, one full `reference` edge each, ordered by
   `metadata.selection_order` (each payload carries the §5.2 metadata:
   `selection_order`, `source_label` = `R1..RN`, `color_key`, `role`).
3. exactly **one** `multi_reference_converged` composite. Last.

Composite `data` — this field set exactly (§10.2):

```json
{
  "tree_id": "0191a8b2-7fff-7000-9000-000000000001",
  "node_id": "0191a8b2-7fff-7000-9000-000000000301",
  "parent_mode": "multi_reference",
  "primary_source_id": "0191a8b2-7fff-7000-9000-000000000101",
  "source_node_ids": ["0191a8b2-…", "0191a8b2-…"],
  "edge_ids": ["0191a8b2-…", "0191a8b2-…"],
  "is_synthetic_merge_point": true,
  "common_ancestor_id": "0191a8b2-7fff-7000-9000-000000000001",
  "context_manifest_hash": "91a2e5d22c17e5870f61ea6e9d501da80c2ac2735d15d5f3b6efb87c8c92856f",
  "created_at": "2026-07-22T12:00:00Z"
}
```

`source_node_ids` and `edge_ids` share one order (2–20 entries);
`context_manifest_hash` is 64 hex characters; `common_ancestor_id` may be null.

The composite is purely **additive**: clients that understand only
`node_added` / `edge_added` keep working, and no client should synthesize edges
from the composite when the `edge_added` events are missing — replay the stream
instead (§10.3). `reference_context_invalidated` (§10.1 row 4) is **not emitted
yet**; the read route reports the same condition as
`source_changed_since_creation` (see [§ Get Reference
Context](#get-reference-context-spec-pl-06-93)).

---

## Context Compiler

### Compile Context

```
GET /api/v1/context/{node_id}
```

**Query params:**
- `budget` — explicit token budget (int, ≥ 1). Omitted → the **derived
  default** below; as a ceiling it is bounded by the named model's context
  window when the catalog reports one, else by 10× `CONTEXT_DEFAULT_BUDGET`
- `model` — model whose context window sizes the **default** budget
  (GAP-080); trimmed, and an empty value is treated as if it were absent
- `includeCards` — bool, default false
- `resolveRefs` — bool, default true
- `maxAncestors` — int, default 0 (unbounded)

**Default budget (`?model=` + `CONTEXT_BUDGET_PERCENT`).** With no `budget`
parameter the default is `CONTEXT_DEFAULT_BUDGET` (8000) unless a model is
named and its context window is known, in which case
it becomes `floor(window × CONTEXT_BUDGET_PERCENT / 100)` (60% by default;
4096 → 2457, 200 000 → 120 000). The window is the gateway's catalog answer
when it reports one and the locally declared `CONTEXT_MODEL_WINDOWS` window
otherwise (see "Declared context windows" under the gateway section), so a
model the gateway reports no window for still derives a real budget.
`CONTEXT_BUDGET_PERCENT=0` disables the
derivation, and an unknown model, an unreachable catalog, or no `model` at all
falls back to `CONTEXT_DEFAULT_BUDGET` — the request never fails because the
catalog is down.

The catalog is the gateway's `GET /v1/models`, and its list is read from a bare
JSON array, from a `models` key, or from the OpenAI-style `data` key the gateway
actually answers with (`models` wins if an object carries both). The derivation
is conditional on a window being known: an entry with no context
window and no declaration counts as unknown, so the budget falls back to
`CONTEXT_DEFAULT_BUDGET` — that is the live gateway's shape, whose one
`Hermes Agent` entry carries no `context_length` at all.

An explicit `budget` wins as long as it fits the ceiling, and the ceiling is
**window-aware** (GAP-080 phase 2b): when the request names a `model` whose
context window the catalog reports, the ceiling IS that window — a 200 000-token
model accepts `budget=999999999` as 200000, which is exactly the budget the same
model gets by default, and a 4 096-token model caps an explicit request at 4096.
In every other case — no `model`, an unknown model, a model the catalog lists
without a window, an unreachable catalog, or a server with no catalog wired —
the ceiling stays the historical flat `CONTEXT_DEFAULT_BUDGET × 10`
(`budget=999999999` → 80000). The clamp **fails closed**: no catalog failure can
raise a client's budget, and the window path can only ever lower a budget below
that flat ceiling. The window-derived default is **not** subject to the flat
clamp (it is already bounded by the model's own window, and clamping it against
`defaultBudget × 10` would silently undo the derivation).

Non-integer and `< 1` budgets are still `400 INVALID_BUDGET` and never reach the
compiler.

**Response (200):** Compiled context with visible manifest.

**The manifest's `manifestHash` (GAP-080 phase 5a, 2026-09-17).** Every
compiled manifest carries `manifestHash`: a stable, lowercase 64-hex sha256
digest of the manifest's content-bearing fields. It exists so two manifests can
be COMPARED — the `GET /api/v1/context/{node_id}` preview (before a run) and
the manifest attached to a run record (after it) hash equally when they
describe the same compiled payload.

The digest covers `nodeId`, `tokenBudget`, `tokensUsed`, `ancestry`,
`references`, `cards`, `omittedCount`, `omittedReason`, `truncationMarkers`,
`warnings`, `pinnedCount` and `multiReference` (including each source's own
fields). It EXCLUDES the volatile, per-compile fields `requestId` and
`compiledAt` — they differ on every compile of identical content, so including
them would defeat the comparison — and it excludes `manifestHash` itself, so the
digest stays recomputable from the record it is stored on. The recipe is: copy
the manifest, clear `requestId`, `compiledAt` and `manifestHash`, then
`json.Marshal` and sha256 the bytes (`internal/context.ManifestDigest`).

`manifestHash` is NOT `omitempty`: a manifest that exists always carries its
digest. A run record written **before** this field existed has no
`manifestHash` key at all; it still yields the same digest as a new record
describing the same content, because the absent field decodes to the zero value
that the recipe clears anyway (`ManifestDigestFromJSON`). Clients must not
invent a digest for a record that carries none.

**Error codes:** `NODE_NOT_FOUND` (404), `INVALID_BUDGET` (400),
`SERVICE_UNAVAILABLE` (503), `CONTEXT_COMPILE_ERROR` (500)

---

## Plugins

Mounted at `/api/v1/plugins`. All require auth. The mount is
`handler.NewPluginHandler(pluginSvc, sseHub).Routes()`, plus the
separately registered network-proxy route and the events stream
(`internal/server/server.go`, `internal/handler/plugin_handler.go`,
`internal/handler/network_proxy_handler.go`).

The registry keeps one row per `(name, version)` in `plugin_registry`, with
exactly one `active` row per name (enforced by the unique partial index
`idx_plugin_registry_name_active`). The lifecycle routes keep the version chain
linked (`previousVersionId`, `supersededById`) and append a `plugin_audit_log`
row (`registered`, `updated`, `rolled_back`, `paused`, `uninstalled`). Plugin
rows come back as the **bare object** — no `{"plugin": …}` wrapper — and the
listing routes return `{"plugins": […]}`; an empty result is
`{"plugins": []}`, never `null`.

A plugin is a JS source whose header carries a manifest:

```javascript
/*@@canopy.manifest@@ {"name":"CSV Viewer","version":"1.0.0","description":"View CSV attachments","permissions":["data_read"],"render_type":"card","entry_point":"main"} @@end@@*/
```

The manifest is the JSON between `/*@@canopy.manifest@@` and `@@end@@*/`. It
must decode with **no unknown fields** and satisfy: `name` non-empty
(≤ 100 chars), `description` non-empty (≤ 1000), `entry_point` non-empty,
`version` matching `X.Y.Z` (an optional `-suffix` is allowed), `render_type`
one of `card` / `embed` / `background`, and `permissions` drawn from
`data_read`, `data_write`, `notification`, `calendar_read`, `calendar_write`,
`network_request`. `icon_url` is optional. A source that is missing the
markers, is 1 MiB (1048576 bytes) or larger, or fails any of those checks is
`400 INVALID_MANIFEST`.

The plugin **slug** is derived from the manifest name: lowercased, with every
run of non-alphanumerics collapsed to one `-` (`CSV Viewer` → `csv-viewer`).

`{name}` and `{id}` are not interchangeable:

| Path parameter | Value |
|---|---|
| `{name}` in `/versions`, `/activate`, `/disable`, `/archive` | the manifest `name`, verbatim (those routes resolve the plugin by name) |
| `{name}` in `/update`, `/rollback` | the plugin **slug** (both are resolved through the slug; `update` refuses a source whose manifest name does not slugify to the path parameter) |
| `{id}` | the plugin row's UUID |

### Register Plugin (or publish a new version)

```
POST /api/v1/plugins/
```

The collection route of the mount is `/`, so `POST /api/v1/plugins/` is the
canonical path (the mount itself answers without the trailing slash too).

**Request body:**
```json
{
  "source_js": "string (full plugin source, manifest header included)"
}
```

Registration publishes a version: the previously `active` row for the same
name is archived and chain-linked (`previousVersionId`, `supersededById`), and
the new row becomes `active` (`isRootVersion` is true for the first version of
a name). Re-posting a `(name, version)` pair that already exists is a conflict.

**Response (201):** the created plugin row (bare object). `source_js` is never
echoed — the payload carries `sourceSha256` and `sourceByteSize` instead.

**Error codes:** `INVALID_MANIFEST` (400 — missing/empty `source_js`, an
invalid manifest, an unknown permission, or a source of 1 MiB or more),
`VERSION_CONFLICT` (409), `INTERNAL_ERROR` (500)

### List Plugins

```
GET /api/v1/plugins/
```

**Response (200):** `{"plugins": […]}` — the `active` row of every plugin,
ordered by name. There are no pagination parameters; the route answers with
the whole active registry.

**Error codes:** `INTERNAL_ERROR` (500)

### Get Plugin

```
GET /api/v1/plugins/{id}
```

`{id}` must be a UUID. Any status is returned (`active`, `disabled`,
`archived`).

**Response (200):** the plugin row (bare object). The source is not part of
this payload — use § Get Plugin Source.

**Error codes:** `INVALID_PLUGIN_ID` (400 — `{id}` is not a UUID),
`PLUGIN_NOT_FOUND` (404), `INTERNAL_ERROR` (500)

### Get Plugin Source

```
GET /api/v1/plugins/{id}/source
```

**Response (200):** the raw source text as `application/javascript` (the body
is the source, not JSON), with `Cache-Control: no-store`. Only an `active` row
is served: a disabled or archived version answers 404 here even though § Get
Plugin still returns its metadata. There is no digest response header — the
integrity value is the row's `sourceSha256`.

**Error codes:** `INVALID_PLUGIN_ID` (400), `PLUGIN_NOT_FOUND` (404 — unknown
id, or a row that is not `active`), `INTERNAL_ERROR` (500)

### List Plugin Versions

```
GET /api/v1/plugins/{name}/versions
```

`{name}` is the manifest name. The UUID that § Register Plugin returns for the row is the `id` field (the plugin lifecycle SSE payloads carry the same value as `plugin_id`), so a reference written `GET /api/v1/plugins/{plugin_id}/versions` denotes this endpoint: the path parameter is the manifest name, not that id.

**Response (200):** `{"plugins": […]}` — every version of that plugin, newest
first. An unknown name is an empty list, not a 404.

**Error codes:** `INTERNAL_ERROR` (500)

### Activate Version

```
POST /api/v1/plugins/{name}/activate
```

**Request body:**
```json
{ "version": "string (semver, required)" }
```

Activates the named version and archives whichever version was `active`,
linking the chain. Asking for the version that is already active returns it
unchanged.

**Response (200):** the now-active plugin row (bare object).

**Error codes:** `INVALID_MANIFEST` (400 — missing `version`),
`PLUGIN_NOT_FOUND` (404 — no such plugin or version), `INTERNAL_ERROR` (500)

### Update Plugin (publish a new version)

```
POST /api/v1/plugins/{name}/update
```

`{name}` is the plugin **slug**.

**Request body:**
```json
{
  "source_js": "string (required)",
  "actor_profile_id": "uuid (required; recorded on the audit row)"
}
```

**Response (200):** the new `active` plugin row (bare object).

**Error codes:** `INVALID_MANIFEST` (400 — missing `source_js` or
`actor_profile_id`, an invalid manifest, or a manifest whose name does not
slugify to `{name}`), `PLUGIN_VERSION_EXISTS` (409 — that version already
exists), `PLUGIN_NOT_FOUND` (404), `INTERNAL_ERROR` (500)

### Rollback Version

```
POST /api/v1/plugins/{name}/rollback
```

`{name}` is the plugin **slug**.

**Request body:**
```json
{
  "target_version": "string (semver, required)",
  "actor_profile_id": "uuid (required)"
}
```

Archives the active row and re-activates `target_version`, linking the chain in
both directions.

**Response (200):** the re-activated plugin row (bare object).

**Error codes:** `INVALID_REQUEST` (400 — missing `target_version` or
`actor_profile_id`), `PLUGIN_VERSION_NOT_FOUND` (404 — the plugin has no such
version, or no active row), `INTERNAL_ERROR` (500)

### Disable Plugin

```
POST /api/v1/plugins/{name}/disable
```

No request body. Moves the `active` row of that name to `disabled` (audit
event `paused`).

**Response (200):** the updated plugin row (bare object).

**Error codes:** `PLUGIN_NOT_FOUND` (404 — unknown name, or the plugin has no
`active` row), `INTERNAL_ERROR` (500)

### Archive Plugin

```
POST /api/v1/plugins/{name}/archive
```

No request body. Moves the `active` row of that name to `archived` and stamps
`archivedAt` (audit event `uninstalled`). Archived rows drop out of § List
Plugins and stop being served by § Get Plugin Source.

**Response (200):** the updated plugin row (bare object).

**Error codes:** `PLUGIN_NOT_FOUND` (404), `INTERNAL_ERROR` (500)

### Network Proxy

```
POST /api/v1/plugins/network-proxy
```

The outbound-HTTP route the sandbox's `network_request` permission gates. It is
registered directly on the `/api/v1` router alongside the plugin mount (not
inside it) and carries the same auth as every other plugin route. `Cookie` and
`Authorization` headers are **stripped** (case-insensitively) and never
forwarded upstream; every other header is passed through. The upstream call is
made with the handler's own HTTP client.

**Request body:**
```json
{
  "url": "https://example.com/thing",
  "method": "GET",
  "headers": { "X-Custom": "kept" },
  "body": "string"
}
```

`url` must be an absolute **HTTPS** URL. `method` defaults to `GET`. `body` is
optional: a JSON string is sent as that text, any other JSON value is sent as
its JSON text.

**Response (200):** the upstream result, with the upstream payload always as a
string (it is never parsed or re-serialized):

```json
{
  "status": 201,
  "statusText": "Created",
  "headers": { "X-Upstream": "yes" },
  "body": "ok",
  "durationMs": 12
}
```

The upstream call has a 30 s budget, and an upstream response larger than
10 MiB is refused rather than truncated.

**Error codes:** `INVALID_REQUEST` (400 — the request body is not valid JSON),
`INVALID_URL` (400 — `url` is not an absolute HTTPS URL),
`PAYLOAD_TOO_LARGE` (413 — upstream response over 10 MiB), `NETWORK_ERROR`
(502 — transport failure or read error)

### Plugin Lifecycle Events

```
GET /api/v1/plugins/{tree_id}/events
```

The SSE stream registered next to the plugin mount. It reuses the tree-events
handler, which is why the path parameter is spelled `tree_id`; at this mount it
is the plugin's UUID. `update` and `rollback` broadcast on that plugin's
channel:

| Event | Payload |
|---|---|
| `plugin_updated` | `{"plugin_id": …, "slug": …, "version": …, "source_sha256": …}` |
| `plugin_rolled_back` | `{"plugin_id": …, "slug": …, "version": …, "source_sha256": …}` |

**Error codes:** `INVALID_TREE_ID` (400 — the parameter is not a UUID),
`TOO_MANY_CONNECTIONS_TREE` (429), `TOO_MANY_CONNECTIONS` (503). Wire format,
query parameters (`since`, `profiles`, `include_heartbeat`) and connection
limits are the ones documented in § SSE Events.

---

## Agents

Mounted at `/api/v1/agents` (`handler.NewAgentHandler().Routes()`,
`internal/server/server.go`; `internal/handler/agent_handler.go`, SPEC-023 §5
and §7). Every route requires auth: the JWT `Authorization: Bearer <token>`
header every other `/api/v1` route requires — this surface is not in the public
`/health` class.

The roster is **entirely in memory**. There is no DB table, nothing reached
through this surface survives a restart, and the registry is seeded on
construction with three demo agents (`helix-foreman`, `codex-worker`,
`kimi-scout`). Agent IDs are **deterministic UUIDs** — `uuid.NewSHA1` over a
fixed namespace and the agent name — so the same agent always has the same id,
across restarts and test runs. There is no create, update or delete route.

The two routes do not share one envelope: the roster is a **bare array** and the
detail is a **bare object**. Neither is wrapped in `{"agents": …}`.

### List Agents

```
GET /api/v1/agents/
```

The collection route of the mount is `/`, so `GET /api/v1/agents/` is the
canonical path (the mount answers without the trailing slash as well).

**Response (200):** **bare array** — `[ {agent}, … ]`, ordered by `name`. Each
entry carries `id`, `name`, `tier`, `trust_score` (`0.0`–`1.0`),
`capabilities`, `incidents` and `last_active`; `tier` is one of `provisional`,
`established`, `veteran`, and `last_active` is an RFC3339 timestamp string. The
trust timeline is detail-only (§ Get Agent). `capabilities` is an object keyed by
capability name, each value `{"success": n, "total": n}`.

**Error codes:** none of its own — the auth codes (`TOKEN_MISSING`,
`TOKEN_INVALID`) apply here as everywhere else.

### Get Agent

```
GET /api/v1/agents/{agent_id}
```

`{agent_id}` must be a UUID.

**Response (200):** **bare object** — the § List Agents fields plus
`trust_history`, an array of `{"score": f, "at": "RFC3339"}` points. An agent
with no history points still emits `trust_history: []`: the field is an array,
never `null`.

**Error codes:** `INVALID_AGENT_ID` (400 — `{agent_id}` is not a UUID),
`AGENT_NOT_FOUND` (404 — no agent with that id)

A method this surface does not mount — for example `POST /api/v1/agents` or `DELETE /api/v1/agents/{agent_id}` — answers `405` with an **empty body** (chi's default, not the `{"error": …}` envelope of § Error Catalog).

---

## Reviews

Mounted at `/api/v1/reviews` (`handler.NewReviewHandler(sseHub).Routes()`,
`internal/server/server.go`; `internal/handler/review_handler.go`, SPEC-023 §2,
§4 and §5). Every route requires auth (JWT `Authorization: Bearer <token>`, the
same class as every other `/api/v1` route).

**The review itself is a deterministic SIMULATION, not a model call.** Nothing
on this surface contacts a model, the network or the database: the risk score is
the FNV-1a hash of the PR identifier (`hash mod 100 ÷ 100`), and the verdict,
formation, confidence and summary are derived from that score by a fixed table.
The same PR string therefore always produces the same verdict — the only fields
that change between two runs of the same PR are `at` and `updated_at`. Do not
read a verdict here as the output of a real Chimera / multi-model review.

The registry is **in memory**: no DB table, nothing persists across a restart,
and it is seeded on construction with four demo reviews (PRs `1042`, `1038`,
`1051`, `1055`). Review IDs are **deterministic UUIDs** (`uuid.NewSHA1` over a
fixed namespace) and are stable across restarts. The list is a **bare array**;
the detail and the trigger response are a **bare object** — there is no
`{"review": …}` wrapper.

### List Reviews

```
GET /api/v1/reviews/
```

The collection route of the mount is `/`, so `GET /api/v1/reviews/` is the
canonical path (the mount answers without the trailing slash as well).

**Response (200):** **bare array** — `[ {review}, … ]`, newest `created_at`
first. Each entry carries `id`, `pr`, `title`, `author`, `status`, `risk_score`
and `updated_at`; `blast_radius` and `verdict` are detail-only.

**Error codes:** none of its own — the auth codes apply as everywhere else.

### Get Review

```
GET /api/v1/reviews/{review_id}
```

`{review_id}` is the review's **UUID**, not the PR identifier, so the PR number is not accepted here: `GET /api/v1/reviews/1042` answers `400 INVALID_REVIEW_ID`. Use the `id` returned by § List Reviews or by § Trigger Review.

**Response (200):** **bare object** with `id`, `pr`, `title`, `author`,
`status`, `risk_score`, `blast_radius`, `verdict`, `created_at` and
`updated_at`.

- `blast_radius` — `{"files_touched": ["…"], "dependents_count": n}`;
  `files_touched` is `[]`, never `null`.
- `verdict` — `null` until the review has run. Once it has run it is
  `{"verdict": "approve" | "request_changes" | "error", "model_formation": "…",
  "summary": "…", "confidence": f, "at": "RFC3339"}`.
- `status` — one of `pending`, `reviewing`, `approved`, `requested_changes`.

**Error codes:** `INVALID_REVIEW_ID` (400 — `{review_id}` is not a UUID),
`REVIEW_NOT_FOUND` (404 — no review with that id)

### Trigger Review

```
POST /api/v1/reviews/{pr}/trigger
```

No request body is read. `{pr}` is the PR identifier as an opaque string — it is
never parsed as a number. The route is an **upsert**: when a review already
exists for that PR the record is updated, otherwise a `pending` review is
created first (`title` `PR #<pr>`, `author` `external`, `risk_score` `0`, empty
blast radius, and an id derived from the PR string). Triggering the seeded PR
`1042` therefore updates that seed rather than adding a second row.

The simulated review derives, from the PR string:

| Risk score | `verdict` | `model_formation` | `status` afterwards |
|---|---|---|---|
| `< 0.40` | `approve` | `single-judge` | `approved` |
| `< 0.70` | `request_changes` | `dual-review` | `requested_changes` |
| `>= 0.70` | `error` | `triple-jury` | `reviewing` |

`confidence` is `1.0 − risk_score × 0.5` (a raw float, so a risk of `0.82`
answers `0.5900000000000001`). The `>= 0.70` band sets the status back to
`reviewing`: a failed simulation is not an approval.

**Response (200):** the review detail object (§ Get Review, bare) after the
update.

The route also broadcasts a `review_event` on the **`general`** workspace channel of the SSE hub — the same deterministic channel the workspace registry derives for `general`, whose feed is mounted at `/api/v1/workspace/channels/{channel_id}/feed` — carrying:

```json
{
  "review_id": "941257ba-04e2-52ca-9726-ceb80339d3bf",
  "pr": "1042",
  "title": "feat: add agent roster surface",
  "status": "reviewing",
  "verdict": "error",
  "risk_score": 0.7,
  "triggered_at": "2026-09-18T17:55:29Z"
}
```

**Error codes:** `INVALID_PR` (400 — the `{pr}` segment is empty), plus the auth
codes.

A method this surface does not mount — for example `PATCH /api/v1/reviews/{review_id}` — answers `405` with an **empty body** (chi's default, not the `{"error": …}` envelope).

---

## Profiles

Mounted at `/api/v1/workspaces/{workspace_id}/profiles`. All require auth.

### List Profiles

```
GET /api/v1/workspaces/{workspace_id}/profiles
```

**Response (200):**
```json
{
  "profiles": [
    {
      "workspaceId": "uuid",
      "profileName": "string",
      "displayName": "string",
      "isActive": false,
      "modelPreference": "string",
      "mappedAt": "RFC3339",
      "lastUsedAt": "RFC3339"
    }
  ]
}
```

### Set Active Profile

```
POST /api/v1/workspaces/{workspace_id}/profiles
```

**Request body:**
```json
{
  "profile_name": "string (required)",
  "profile_token": "string (required)",
  "display_name": "string (optional)",
  "model_preference": "string (optional)"
}
```

**Response (200):** Updated profile mapping.

### Get Active Profile

```
GET /api/v1/workspaces/{workspace_id}/profiles/active
```

**Response (200):** Active profile mapping.

### Remove Profile

```
DELETE /api/v1/workspaces/{workspace_id}/profiles/{profile_name}
```

**Response:** `204 No Content`.

---

## Collaboration (workspaces)

Mounted at `/api/v1/collab`. All eleven routes require
`Authorization: Bearer <token>`. This is the shipped collaboration surface;
it is workspace-scoped rather than the deferred tree-scoped routes retained in
SPEC-API-06. Unless a route says otherwise, failures use the standard
`{"error":{"code":"…","message":"…"}}` envelope.

### Route summary

| Method | Path | Auth | Success |
|---|---|---|---|
| GET | `/api/v1/collab` | Bearer | 200 `{"workspaces":[…]}` |
| POST | `/api/v1/collab` | Bearer | 201 `{"workspace_id":…,"name":…,"role":…,"members":[…]}` |
| GET | `/api/v1/collab/{workspace_id}` | Bearer | 200 bare workspace object |
| PATCH | `/api/v1/collab/{workspace_id}` | Bearer | 200 bare updated workspace object |
| DELETE | `/api/v1/collab/{workspace_id}` | Bearer | 204, no body |
| GET | `/api/v1/collab/{workspace_id}/members` | Bearer | 200 `{"members":[…]}` |
| PATCH | `/api/v1/collab/{workspace_id}/members/{user_id}` | Bearer | 200 `{"ok":true}` |
| DELETE | `/api/v1/collab/{workspace_id}/members/{user_id}` | Bearer | 204, no body |
| POST | `/api/v1/collab/{workspace_id}/invite` | Bearer | 201 `{"token":…,"expires_at":…}` |
| POST | `/api/v1/collab/{workspace_id}/join` | Bearer | 200 `{"ok":true}` |
| POST | `/api/v1/collab/{workspace_id}/leave` | Bearer | 204, no body |

### Workspace channels

Mounted at `/api/v1/workspace/channels`. The channel registry remains in memory
and the seeded `general` and `agents` channel IDs are deterministic. All three
routes accept the optional `workspace_id` query parameter:

| Method | Path | Success |
|---|---|---|
| GET | `/api/v1/workspace/channels?workspace_id={uuid}` | 200 channel array |
| POST | `/api/v1/workspace/channels/{channel_id}/message?workspace_id={uuid}` | 202 message object |
| GET | `/api/v1/workspace/channels/{channel_id}/feed?workspace_id={uuid}` | 200 SSE stream |

When `workspace_id` is present, the authenticated caller must be a member. A
non-member, missing workspace, or soft-deleted workspace receives the same
`403` envelope to avoid an existence oracle:

```json
{"error":{"code":"WORKSPACE_NOT_FOUND","message":"workspace not found"}}
```

A malformed `workspace_id` receives `400`:

```json
{"error":{"code":"INVALID_WORKSPACE_ID","message":"workspace_id must be a valid UUID"}}
```

Omitting `workspace_id` preserves the legacy unscoped channel behavior. The
message feed replays the retained, bounded channel history when a client
connects without `Last-Event-ID`, in sequence order, and then continues with
live `channel_message` events. Set `replay=false` to disable replay on a fresh
connection; reconnects carrying `Last-Event-ID` retain cursor-based replay.

---


Workspace responses are bare objects with the fields `id`, `owner_id`, `name`,
`tree_id` (omitted when unbound), `members`, `approval_ttl`, `created_at`, and
`updated_at`. A member has `user_id`, `handle`, numeric `role`, and
`joined_at`. The list routes return non-null arrays, including when the result
is empty.

### List workspaces

```
GET /api/v1/collab
```

**Response (200):** `{"workspaces":[…]}`. There is no request body.

**Error codes:** `TOKEN_MISSING` (401), `INTERNAL_ERROR` (500).

### Create workspace

```
POST /api/v1/collab
```

**Request body:** `{"name":"string"}`.

**Response (201):**
```json
{
  "workspace_id": "uuid",
  "name": "Project",
  "role": "admin",
  "members": [
    {"user_id": "uuid", "handle": "bane", "role": 2, "joined_at": "RFC3339"}
  ]
}
```

**Error codes:** `TOKEN_MISSING` (401), `INVALID_BODY` (400),
`INTERNAL_ERROR` (500).

### Get workspace

```
GET /api/v1/collab/{workspace_id}
```

**Response (200):** Bare workspace object. No request body.

**Error codes:** `INVALID_WORKSPACE_ID` (400), `TOKEN_MISSING` (401),
`NOT_WORKSPACE_MEMBER` (403), `NOT_FOUND` (404), `INTERNAL_ERROR` (500).

### Update workspace

```
PATCH /api/v1/collab/{workspace_id}
```

**Request body:** partial JSON; supported fields are `name`, `description`,
`tree_id`, and `approval_ttl` (seconds).

**Response (200):** Bare updated workspace object.

**Error codes:** `INVALID_WORKSPACE_ID` (400), `INVALID_BODY` (400),
`TOKEN_MISSING` (401), `PERMISSION_DENIED` (403), `NOT_FOUND` (404),
`INTERNAL_ERROR` (500).

### Delete workspace

```
DELETE /api/v1/collab/{workspace_id}
```

**Response:** `204 No Content`.

**Error codes:** `INVALID_WORKSPACE_ID` (400), `TOKEN_MISSING` (401),
`PERMISSION_DENIED` (403), `NOT_FOUND` (404), `INTERNAL_ERROR` (500).

### List workspace members

```
GET /api/v1/collab/{workspace_id}/members
```

**Response (200):** `{"members":[…]}` using the member shape above.

**Error codes:** `INVALID_WORKSPACE_ID` (400), `TOKEN_MISSING` (401),
`NOT_WORKSPACE_MEMBER` (403), `NOT_FOUND` (404), `INTERNAL_ERROR` (500).

### Update a member role

```
PATCH /api/v1/collab/{workspace_id}/members/{user_id}
```

**Request body:** `{"role":0|1|2}` where the collaboration roles are viewer,
editor, and admin respectively.

**Response (200):** `{"ok":true}`.

**Error codes:** `INVALID_WORKSPACE_ID` (400), `INVALID_USER_ID` (400),
`INVALID_BODY` (400), `TOKEN_MISSING` (401), `PERMISSION_DENIED` (403),
`NOT_FOUND` (404), `INTERNAL_ERROR` (500).

### Remove a member

```
DELETE /api/v1/collab/{workspace_id}/members/{user_id}
```

**Response:** `204 No Content`.

**Error codes:** `INVALID_WORKSPACE_ID` (400), `INVALID_USER_ID` (400),
`TOKEN_MISSING` (401), `PERMISSION_DENIED` (403), `NOT_FOUND` (404),
`INTERNAL_ERROR` (500).

### Generate an invitation

```
POST /api/v1/collab/{workspace_id}/invite
```

No request body is read. The caller must be an admin.

**Response (201):**
```json
{
  "token": "url-safe-token",
  "expires_at": "RFC3339"
}
```

**Error codes:** `INVALID_WORKSPACE_ID` (400), `TOKEN_MISSING` (401),
`PERMISSION_DENIED` (403), `NOT_FOUND` (404), `DUPLICATED` (409),
`INTERNAL_ERROR` (500).

### Join a workspace

```
POST /api/v1/collab/{workspace_id}/join?token=<invitation-token>
```

No request body is read. The invitation token is required as the `token` query
parameter.

**Response (200):** `{"ok":true}`.

**Error codes:** `INVALID_WORKSPACE_ID` (400), `INVALID_TOKEN` (400),
`TOKEN_MISSING` (401), `NOT_FOUND` (404), `DUPLICATED` (409),
`INTERNAL_ERROR` (500).

### Leave a workspace

```
POST /api/v1/collab/{workspace_id}/leave
```

No request body is read. The workspace owner cannot leave.

**Response:** `204 No Content`.

**Error codes:** `INVALID_WORKSPACE_ID` (400), `TOKEN_MISSING` (401),
`PERMISSION_DENIED` (403), `NOT_FOUND` (404), `NOT_WORKSPACE_MEMBER` (403),
`INTERNAL_ERROR` (500).

---

## Transports

Mounted at `/api/v1/transports`. All require auth.

### Get Transport Status

```
GET /api/v1/transports/status
```

**Response (200):** List of transport configs with state.

### Get Transport Config

```
GET /api/v1/transports/{type}
```

**Response (200):** Transport config for the given type.

### Update Transport Config

```
PUT /api/v1/transports/{type}
```

**Response (200):** Updated config.

### Disable Transport

```
DELETE /api/v1/transports/{type}
```

**Response (200):** Disabled transport.

### Transport Health Probes (public)

```
GET /health/transports/{type}
```

**Response (200):** Transport health status.

### Aggregate Transport Health

```
GET /api/v1/transports/health
```

JWT authentication is required. The response reports each transport's current
state, last transition, sent/received message counters, relay queue depth, and
the selector's current transport. Counters are process-local and reset when
`canopyd` restarts.

### Store-and-Forward Relay

```
POST /api/v1/transport/relay
GET /api/v1/transport/relay/poll?peer_id={peer_id}
```

Both endpoints require JWT authentication. `POST` accepts a relay envelope and
returns `202 Accepted`; `GET` drains the queued envelopes for the peer. Relay
POST ingress is limited to 600 requests per minute per peer. Request 601 in a
fixed one-minute window returns `429 Too Many Requests` with a sanitized JSON
error; the next window starts with a fresh allowance.

### Production Transport Gates

- WebRTC/Pion wiring is opt-in with `CANOPY_WEBRTC_ENABLED=1`.
- NATS wiring is enabled when `CANOPY_NATS_URL` is set. Optional credentials
  are read from `CANOPY_NATS_CREDS`; credentials and bearer tokens are never
  included in transport metrics or health responses.
- Relay polling and WebRTC signaling outbound operations time out after 15
  seconds. Transport reconnect delay is capped at 30 seconds.

---

## MLS (Messaging Layer Security)

Mounted at `/api/v1/workspaces/{workspace_id}/mls`. All require auth.

### Get MLS Group

```
GET /api/v1/workspaces/{workspace_id}/mls/groups
```

### Create MLS Group

```
POST /api/v1/workspaces/{workspace_id}/mls/groups
```

### Join MLS Group

```
POST /api/v1/workspaces/{workspace_id}/mls/groups/join
```

### Leave MLS Group

```
POST /api/v1/workspaces/{workspace_id}/mls/groups/leave
```

### Encrypt

```
POST /api/v1/workspaces/{workspace_id}/mls/encrypt
```

### Decrypt

```
POST /api/v1/workspaces/{workspace_id}/mls/decrypt
```

### Get MLS State

```
GET /api/v1/workspaces/{workspace_id}/mls/state
```

### Generate Key Package

```
POST /api/v1/workspaces/{workspace_id}/mls/key-packages
```

**Request body:**
```json
{
  "workspace_id": "uuid (optional; when present must match the route)",
  "profile_id": "uuid (required)",
  "credential": {
    "profile_id": "uuid (optional; defaults to profile_id)",
    "identity": "base64-profile-identity (required)",
    "credential_type": "string (required)",
    "signature_public_key": "base64-ed25519-public-key (required; 32 bytes)"
  }
}
```

The request contains public credential material only. The server never accepts,
stores, derives, or returns a client signing private key. A successful request
returns `201` with the generated key-package metadata and bytes.

### Get Key Package

```
GET /api/v1/workspaces/{workspace_id}/mls/key-packages
```

### Commit Proposals

```
POST /api/v1/workspaces/{workspace_id}/mls/commit-proposals
```

A successful commit consumes the pending proposal set and advances the group
epoch exactly once. Replaying after the proposals have been consumed returns
`409 CONFLICT` (`mls: group epoch changed during proposal commit`) when a
concurrent commit won the epoch compare-and-swap; a replay that observes no
pending proposals continues to return `ErrProposalRejected`.

### MLS Events

```
GET /api/v1/workspaces/{workspace_id}/mls/events
```

---

## MCP (Model Context Protocol)

Mounted at `/api/v1/mcp`. All require auth.

### JSON-RPC Endpoint

```
POST /api/v1/mcp
```

Accepts JSON-RPC 2.0 requests. Exposes tools for tree, node, topic, card, graph,
and approval operations for programmatic agent access.

A trailing slash (`POST /api/v1/mcp/`) is the same endpoint. The endpoint is
**stateless**: no session id is issued or required, and `initialize` is not a
precondition for `tools/list` — each request is answered from its own body.
Auth is the same JWT Bearer token as every other `/api/v1` route; without it
the response is the standard envelope, `401 {"error":{"code":"TOKEN_MISSING",
"message":"Authorization Bearer token required"}}`.

### Handshake

| Request | Response |
|---------|----------|
| `initialize` | `200` — `{"jsonrpc":"2.0","id":…,"result":{"protocolVersion":…,"capabilities":{"tools":{"listChanged":false}},"serverInfo":{"name":"canopyd-canopy","version":…}}}` |
| `notifications/initialized` (and any `notifications/…`) | `202` — **empty body** (a notification is never answered with a JSON-RPC object) |
| `ping` | `200` — `"result":{}` |
| `tools/list` | `200` — `{"tools":[…7 tools…]}` |
| `tools/call` | `200` — a successful call returns an MCP `CallToolResult` envelope; tool execution failures remain JSON-RPC error objects |
| any other method (e.g. `resources/list`) | `200` — `-32601 Method not found: <method>` |

`initialize` params: `protocolVersion` (string), `clientInfo` (object),
`capabilities` (object). Unknown fields are ignored, and a missing `params`
object is not an error. **Version negotiation:** `protocolVersion` echoes the
requested revision when the server supports it (`2025-06-18`, `2025-03-26`,
`2024-11-05`); any other value — newer or older — is answered with the server's
newest supported revision, and the client decides whether to continue.
`serverInfo.version` is the binary's build version, identical to
`canopyd -version` (never a hardcoded literal).

Error codes: `-32700` parse error (`400`), `-32600` invalid request (jsonrpc
must be `"2.0"`), `-32602` invalid params (e.g. a non-object `params`),
`-32601` method not found, `-32000` tool execution failure.

**CallToolResult envelope (2026-09-22).** A successful `tools/call` always wraps
its existing domain payload in the MCP result shape. The payload is compact JSON
inside a required text content item, and `isError` is `false`:

```json
{"result":{"content":[{"type":"text","text":"{\"trees\":[]}"}],"isError":false}}
```

Protocol-level request errors and tool execution failures keep their JSON-RPC
`error` object; they are not converted into a `CallToolResult`.

`notifications/…` requests are answered `202` with an empty body **before** any
version or method validation, because JSON-RPC forbids replying to a
notification.

### Tools

`list_trees`, `get_tree`, `create_node`, `list_topics`, `get_graph_stats`,
`list_approvals`, `list_cards`.

```
POST /api/v1/mcp
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_trees","arguments":{}}}
```

`tools/list` omits the optional `nextCursor` pagination field: the tool set fits
one page.

---

## File Viewers (SPEC-PL-02)

Mounted at `/api/v1/files` and `/api/v1/viewers`. All require auth (JWT Bearer).
Phase 1 of SPEC-PL-02 (SPEC-PL-02 §4.3): file storage with content-addressed
dedup, MIME detection, viewer registry + dispatch, streaming with HTTP Range
support and an append-only access log. Thumbnails, preview-text extraction,
overrides endpoints and viewer SSE events are later phases and are not exposed.

**Acting profile.** Every `/files` route and `POST /viewers/dispatch` resolves
the JWT `sub` (a `users.id`) to the ACTING profile — a profile whose `id`
equals the subject, else the subject's newest live owned profile. A subject
with no profile gets `404 PROFILE_NOT_FOUND`. On a fresh database the dev
server provisions both (`profiles` row `dev-hermes` owned by the dev subject,
`workspaces` row `00000000-0000-0000-0000-000000000010`, and the active
`profile_route` mapping) at startup — but only when `JWT_SECRET` is the
default dev secret. See [README.md § Authentication (dev mode)](../README.md#authentication-dev-mode)
and [docs/INTEGRATION.md § 6](INTEGRATION.md) for the runnable walkthrough.

### Routes

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/api/v1/files/upload` | Upload bytes (multipart) — content-addressed, dedup-aware |
| `POST` | `/api/v1/files/resolve` | Resolve by hash reference (`hash_ref`) — JSON |
| `POST` | `/api/v1/files/resolve/batch` | Resolve up to 200 hash references in one call |
| `GET` | `/api/v1/files/recents` | Recently accessed OR uploaded files for the acting profile (an upload counts as an access — GAP-072) |
| `GET` | `/api/v1/files` | Paginated file list for the acting profile |
| `GET` | `/api/v1/files/{id}` | File metadata |
| `GET` | `/api/v1/files/{id}/stream` | File bytes — supports `Range` (206) and `If-None-Match` (304) |
| `GET` | `/api/v1/files/{id}/access` | Access-log entries for one file (newest first) |
| `POST` | `/api/v1/files/{id}/access` | Append one access-log entry (append-only table) |
| `DELETE` | `/api/v1/files/{id}` | Soft-delete a file |
| `GET` | `/api/v1/viewers` | List registered viewers |
| `GET` | `/api/v1/viewers/{slug}` | One viewer registration |
| `POST` | `/api/v1/viewers/dispatch` | Resolve which viewer renders a file (without opening it) |

### Upload a file

```
POST /api/v1/files/upload
Content-Type: multipart/form-data
```

| Part / field | Required | Notes |
|--------------|----------|-------|
| `file` (part) | yes | The bytes. 0 bytes → `400 FILE_EMPTY`; > 500 MB → `413 FILE_TOO_LARGE` |
| `filename` | no | Falls back to the part's filename. `400 FILENAME_INVALID` if empty, > 1000 chars, or containing path separators / `..` |
| `declaredMime` | no | Falls back to the part's `Content-Type`. The stored `mimeType` is server-detected by magic bytes |
| `sourceMessageId` | no | UUID of the message the file was attached to (`400 INVALID_REQUEST` if not a UUID) |

The same bytes for the same profile are stored **once**: `sha256` (hex) is the
content address and `(profile_id, sha256)` is unique among live rows. A repeat
upload of identical bytes answers `"was_deduped": true` and returns the
ORIGINAL row (its filename is the first upload's); the stored `referenceCount`
is incremented in the database, while the echoed `file` object carries the
value read *before* the bump — verify with `GET /api/v1/files/{id}`.

**Side effect (GAP-072):** a successful upload stamps `lastAccessedAt` and
bumps `accessCount` on the file row, so the uploaded file is listed by
`GET /api/v1/files/recents` right away. No `file_access_log` row is written
(the log's action enum has no upload value) — see § Recents.

**Response (201):**
```json
{
  "file": {
    "id": "01a0a32e-63f5-7666-ac1d-a809a24ee800",
    "profileId": "01a0a32e-62a8-751b-bf9a-271bb6fbdc00",
    "sha256": "bdbeddd49e5cc697816366c3af527da048c9abcd263946e9bc3b04aa02592a5c",
    "byteSize": 49,
    "mimeType": "text/markdown",
    "declaredMime": "text/markdown",
    "filename": "note.md",
    "extension": "md",
    "storageKind": "hermes_kb",
    "sourceKind": "upload",
    "isText": true,
    "isBinary": false,
    "isViewable": true,
    "viewerHint": "markdown",
    "metadata": "e30=",
    "referenceCount": 1,
    "accessCount": 0,
    "quarantined": false,
    "createdAt": "2026-09-14T22:48:41.589474-05:00",
    "updatedAt": "2026-09-14T22:48:41.589474-05:00"
  },
  "was_new_upload": true,
  "was_deduped": false,
  "stream_url": "/api/v1/files/01a0a32e-63f5-7666-ac1d-a809a24ee800/stream",
  "expires_at": "2026-09-14T23:03:41.59780124-05:00"
}
```

`storageKind` ∈ `hermes_kb` | `hermes_fs_ref` | `external`; `sourceKind` ∈
`upload` | `reference` | `agent_message` | `import`; `viewerHint` is the
server-detected viewer slug (`''` = download-only, `isViewable: false`).

> **Byte-typed fields are base64.** `metadata` (the file's `metadata_json`) and
> the access log's `clientInfo` are Go `[]byte` values, so they serialize as
> base64 strings: `"e30="` decodes to `{}`. Decode before parsing as JSON.

### Resolve by hash reference

```
POST /api/v1/files/resolve
Content-Type: application/json
```

```json
{ "hash_ref": { "profile_id": "01a0a32e-...", "sha256": "<64 hex>" } }
```

`profile_id` may be omitted (or null) — it then means the acting profile.
`upload` is the mutually exclusive alternative: JSON resolve rejects it with
`400 INVALID_REQUEST` (bytes ride `POST /files/upload`). Unknown hash →
`404 FILE_NOT_FOUND_BY_HASH`.

**Response (200):** the same `ResolveFileOutput` shape as upload, with
`was_new_upload: false` and `was_deduped` reflecting whether the row was
already present.

```
POST /api/v1/files/resolve/batch
```

Body is a JSON **array** of `{hash_ref: {...}}` (max 200). Every entry is
resolved independently: a failing entry comes back as a positional output with
no `file` and `null` `expires_at` (never a whole-request failure).

### List files

```
GET /api/v1/files
```

| Query param | Default | Notes |
|-------------|---------|-------|
| `sort` | `created_desc` | One of `created_desc` \| `created_asc` \| `name_asc` \| `size_desc` \| `last_accessed_desc` (unknown values fall back to `created_desc`) |
| `mimeFilter` | — | Case-insensitive substring match on the MIME type |
| `extensionFilter` | — | Exact lowercase extension, no dot (`md`) |
| `viewableOnly` | `true` | `false` includes download-only files |
| `excludeQuarantined` | `true` | `false` includes quarantined files |
| `limit` | `50` | 1–200, else `400 INVALID_REQUEST` |
| `cursor` | — | Last `id` of the previous page (keyset `id > cursor`); must be a UUID or `400 INVALID_REQUEST` |

**Response (200):**
```json
{ "files": [], "pagination": { "count": 0 } }
```

Each element is the slim shape (no `metadata`, no `previewText`): `id`,
`profileId`, `sha256`, `byteSize`, `mimeType`, `filename`, `extension`,
`storageKind`, `isText`, `isViewable`, `viewerHint`, `referenceCount`,
`lastAccessedAt` (omitted until first access), `createdAt`.

### Recents

```
GET /api/v1/files/recents?limit=50
```

**Response (200):** a bare JSON array of the slim file shape (not wrapped),
newest access first. `limit` 1–200.

**Semantics (GAP-072):** recents lists the acting profile's non-deleted files
whose `last_accessed_at` is set, newest first. A file enters the list when it
has been touched — either by an explicit access entry
(`POST /api/v1/files/{id}/access`, which stamps `last_accessed_at` and bumps
`access_count`) **or by being uploaded**: `POST /api/v1/files/upload` stamps the
same fields, so a freshly uploaded file appears in recents immediately instead
of after its first explicit open (from a dogfood session, 2026-09-14, recents
returned `[]` right after an upload). Uploads do NOT write a `file_access_log`
row — that table's action enum (`open|download|thumbnail_fetch|preview_text|
stream_start|stream_end|error`, migrations/000045 CHECK constraint) has no
upload value, so it stays the audit trail of explicit viewer interactions.

### Get one file

```
GET /api/v1/files/{id}
```

**Response (200):** the full file object (as in the upload response). `{id}`
must be a UUID (`400 INVALID_FILE_ID`); unknown → `404 FILE_NOT_FOUND_BY_ID`.
Soft-deleted rows are filtered by the same query, so a deleted file also reads
as `404 FILE_NOT_FOUND_BY_ID` (the `410 FILE_SOFT_DELETED` sentinel exists but
no repo path returns it today — see § Spec-vs-Code Drift).

### Stream file content

```
GET /api/v1/files/{id}/stream
Range: bytes=0-4            # optional
If-None-Match: "<etag>"     # optional
```

**Response headers (200 / 206):**

| Header | Value |
|--------|-------|
| `Content-Type` | Server-detected MIME (`text/markdown`) |
| `ETag` | `"<sha256 hex>"` — strong validator, quoted |
| `Accept-Ranges` | `bytes` |
| `Content-Disposition` | `inline` (viewable) or `attachment` (download-only) |
| `Cache-Control` | `private, max-age=300` |
| `X-Viewer-Hint` | Server-detected viewer slug (`markdown`) |
| `Content-Range` | `bytes 0-4/49` — 206 responses only |

A `Range` header yields `206 Partial Content` with `Content-Length` = the
window length; a suffix/oversized range → `416 RANGE_NOT_SATISFIABLE` with code
`RANGE_NOT_SATISFIABLE` (malformed header → `400 INVALID_RANGE_HEADER`). A
matching `If-None-Match` yields `304 Not Modified` with no body. Quarantined
files never stream (`423 FILE_QUARANTINED`), and a soft-deleted file answers
`404 FILE_NOT_FOUND_BY_ID` (same reason as `GET /files/{id}`).

### Access log

```
GET  /api/v1/files/{id}/access?limit=50
POST /api/v1/files/{id}/access
```

`file_access_log` is **append-only** (a database trigger rejects UPDATE and
DELETE). `POST` body:

```json
{
  "action": "open",
  "viewer_slug": "code",
  "tree_id": "optional-uuid",
  "node_id": "optional-uuid",
  "duration_ms": 1200,
  "byte_offset": 4096,
  "error_code": "optional-string"
}
```

`action` must be one of `open` | `download` | `thumbnail_fetch` |
`preview_text` | `stream_start` | `stream_end` | `error`;
`viewer_slug` is required and must match `^[a-z][a-z0-9_]*$`
(`400 INVALID_VIEWER_SLUG`). Unknown file → `404 FILE_NOT_FOUND_BY_ID`.

**Response (201):** the stored entry, e.g.

```json
{
  "id": "01a0a32e-6452-76fc-8ae2-592ff00c1c00",
  "fileId": "01a0a32e-63f5-7666-ac1d-a809a24ee800",
  "profileId": "01a0a32e-62a8-751b-bf9a-271bb6fbdc00",
  "viewerSlug": "code",
  "action": "open",
  "clientInfo": "e30=",
  "createdAt": "2026-09-14T22:48:41.681579-05:00"
}
```

`GET` returns a bare JSON array of these entries (same shape), newest first;
an empty log is `[]`, never `null`. Logging an access also refreshes
`lastAccessedAt` / `accessCount`, which is what feeds `/files/recents`.

### Delete a file

```
DELETE /api/v1/files/{id}
```

Soft delete (`deleted_at` set). **Response (200):** `{}`.

### Viewer registry

```
GET /api/v1/viewers
```

**Response (200):** a JSON array of registrations (seeded on boot from the
built-in viewers compiled into canopyd — `audio_video`, `code`, `csv`,
`image`, `json`, `markdown`, `pdf`). Registry JSON shape:

```json
[
  {
    "viewerSlug": "markdown",
    "version": "0.4.2",
    "displayName": "Markdown",
    "renderType": "fullscreen",
    "supportsMime": ["text/markdown", "text/x-markdown"],
    "supportsExtensions": ["md", "markdown", "mdx"],
    "isActive": true
  }
]
```

```
GET /api/v1/viewers/{slug}
```

**Response (200):** the full registration, adding `id`, `canopydVersion`,
`description`, `iconUrl`, `supportsViewerHint`, `requiredCapabilities`,
`bundlePath`, `bundleByteSize`, `bundleSha256`, `minCanopydVersion`
(`deprecationNotice` when set) and `installedAt`:

```json
{
  "id": "01a0a32e-62b4-72f5-80dc-e1d4db306400",
  "viewerSlug": "code",
  "version": "0.4.2",
  "canopydVersion": "0.4.2",
  "displayName": "Code Editor",
  "description": "View code with Monaco Editor — 20+ languages, syntax highlighting",
  "iconUrl": "/static/viewers/code/icon.svg",
  "renderType": "fullscreen",
  "supportsMime": ["text/x-python", "text/x-go", "application/json", "..."],
  "supportsExtensions": ["py", "go", "ts", "md", "yml", "..."],
  "supportsViewerHint": [],
  "requiredCapabilities": [],
  "bundlePath": "/static/viewers/code/monaco.bundle.js",
  "bundleByteSize": 5242880,
  "bundleSha256": "",
  "minCanopydVersion": "0.4.0",
  "isActive": true,
  "installedAt": "2026-09-14T22:48:41.267703-05:00"
}
```

An unknown or malformed slug → `404 VIEWER_NOT_FOUND` / `400
INVALID_VIEWER_SLUG`.

### Dispatch

```
POST /api/v1/viewers/dispatch
Content-Type: application/json

{ "file_id": "01a0a32e-63f5-7666-ac1d-a809a24ee800", "tree_id": null }
```

Picks the viewer for a file without streaming it (per-tree/profile overrides
land in a later phase). Unknown file → `404 FILE_NOT_FOUND_BY_ID`; no active
viewer matches → `404 VIEWER_NOT_FOUND`.

**Response (200):**
```json
{
  "viewerSlug": "markdown",
  "renderType": "fullscreen",
  "bundlePath": "",
  "bundleSha256": "",
  "displayName": "Markdown",
  "iconUrl": "/static/viewers/markdown/icon.svg",
  "config": {},
  "isBuiltIn": true
}
```

### File-viewer error codes

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `INVALID_REQUEST` | 400 | Bad JSON, bad `limit`/`cursor`, mutually exclusive fields, batch > 200, bad `action` |
| `INVALID_MULTIPART` | 400 | Body is not multipart/form-data or has no `file` part |
| `INVALID_FILE_ID` | 400 | `{id}` is not a UUID |
| `INVALID_VIEWER_SLUG` | 400 | Slug fails `^[a-z][a-z0-9_]*$` |
| `INVALID_RANGE_HEADER` | 400 | `Range` header is malformed |
| `FILENAME_INVALID` | 400 | Filename empty, > 1000 chars, or contains path separators |
| `FILE_EMPTY` | 400 | 0-byte upload |
| `FILE_NOT_FOUND_BY_ID` | 404 | No `file_metadata` row for that id |
| `FILE_NOT_FOUND_BY_HASH` | 404 | No live row for that `(profile, sha256)` |
| `PROFILE_NOT_FOUND` | 404 | The acting profile (or an explicit `profile_id`) does not exist |
| `VIEWER_NOT_FOUND` | 404 | No viewer row for that slug, or no viewer matches the file |
| `FILE_SOFT_DELETED` | 410 | Declared for soft-deleted files, but NOT reachable today: `GetByID` filters `deleted_at IS NULL` and answers `FILE_NOT_FOUND_BY_ID` instead |
| `FILE_TOO_LARGE` | 413 | Upload exceeds the 500 MB limit |
| `RANGE_NOT_SATISFIABLE` | 416 | `Range` window exceeds the file size |
| `FILE_QUARANTINED` | 423 | Quarantined files cannot be streamed |
| `ACCESS_LOG_APPEND_FAILED` | 500 | The append-only log rejected the insert |

> **Known gap (SPEC-PL-02 §10.1 `STREAM_INTERRUPTED`, 503):** the storage
> sentinel `fileviewer.ErrStorageUnavailable` is NOT mapped in the handlers'
> error switch, so a storage failure currently surfaces as
> `500 INTERNAL_ERROR` instead of `503 STREAM_INTERRUPTED`. Tracked in
> § Spec-vs-Code Drift below.

---

## Error Catalog

All errors follow a consistent JSON envelope:

```json
{
  "error": {
    "code": "ERROR_CODE",
    "message": "Human-readable description"
  }
}
```

### Common Error Codes

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `INVALID_BODY` | 400 | Request body is not valid JSON |
| `INVALID_TREE_ID` | 400 | tree_id is not a valid UUID |
| `INVALID_NODE_ID` | 400 | node_id is not a valid UUID |
| `VALIDATION_ERROR` | 400 | Business rule violation (see message) |
| `TOKEN_MISSING` | 401 | No Authorization Bearer token |
| `TOKEN_INVALID` | 401 | Token is expired or signature invalid |
| `NOT_TREE_OWNER` | 403 | User does not own the tree |
| `NOT_TREE_MEMBER` | 403 | User is not a member of the tree |
| `FORBIDDEN` | 403 | Operation not permitted |
| `TREE_NOT_FOUND` | 404 | Tree does not exist |
| `NODE_NOT_FOUND` | 404 | Node does not exist |
| `NOT_FOUND` | 404 | Generic not found |
| `CONFLICT` | 409 | Operation conflicts with current state |
| `GONE` | 410 | Resource was deleted |
| `TREE_DELETED` | 410 | Tree was soft-deleted |
| `REQUEST_TOO_LARGE` | 413 | Body exceeds 1MB limit |
| `RATE_LIMITED` | 429 | Too many requests (100/s per IP, burst 200; `POST /trees/{tree_id}/merge` additionally 10 req/min per user — carries `Retry-After` and `retry_after_seconds`) |
| `SERVICE_UNAVAILABLE` | 503 | Database unavailable |
| `INTERNAL_ERROR` | 500 | Unexpected server error |

### Node-Specific Error Codes

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `EMPTY_CONTENT` | 400 | Content field is empty (create) or missing/empty on fork — message `content is required` |
| `CONTENT_TOO_LARGE` | 400 | Content exceeds 64KB |
| `INVALID_PARENT_ID` | 400 | parent_id is not a valid UUID, or is present but empty (GAP-072: omit the field for a root node) |
| `GONE` | 410 | Node was already deleted |
| `CONFLICT` | 409 | Parent node was deleted |
| `REFERENCE_CONTEXT_NOT_FOUND` | 404 | `GET /api/v1/nodes/{node_id}/reference-context`: the node is not a multi-reference reply — the SAME answer a non-existent node gets, so the route is not an existence oracle (SPEC-PL-06 §9.3/§9.4) |

### Tree-Specific Error Codes

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `TREE_NOT_FOUND` | 404 | Tree does not exist |
| `TREE_DELETED` | 410 | Tree was soft-deleted |

### File-Viewer Error Codes

The `/api/v1/files` + `/api/v1/viewers` routes (SPEC-PL-02 phase 1) use their own
code set — see [§ File Viewers](#file-viewers-spec-pl-02) for the full table
(`FILE_NOT_FOUND_BY_ID`, `FILE_NOT_FOUND_BY_HASH`, `FILE_EMPTY`,
`FILE_TOO_LARGE`, `FILENAME_INVALID`, `PROFILE_NOT_FOUND`,
`FILE_SOFT_DELETED`, `FILE_QUARANTINED`, `VIEWER_NOT_FOUND`,
`INVALID_VIEWER_SLUG`, `INVALID_RANGE_HEADER`, `RANGE_NOT_SATISFIABLE`).

---

## Spec-vs-Code Drift

The following discrepancies were found between the README API reference and the
actual code:

1. **Edges:** README documents `GET/POST /api/v1/edges` and `DELETE /api/v1/edges/{id}`,
   but no EdgeHandler is registered in `server.go`. Edges are created implicitly
   through node operations (reply, fork) and returned in node creation responses.
   There are no standalone edge CRUD routes.

2. **Approvals:** README documents `POST /api/v1/approvals` (create), but the
   `ApprovalHandler.Routes()` only registers `GET /`, `GET /pending`,
   `GET /history`, `GET /{approval_id}`, `POST /{approval_id}/approve`, and
   `POST /{approval_id}/deny`. There is no create-approval endpoint.

3. **SSE events:** README documents `GET /api/v1/events` and
   `GET /api/v1/events?tree_id={id}`, but the actual route is
   `GET /api/v1/trees/{tree_id}/events` (tree-scoped, membership-gated).

4. **Export/Import:** Registered directly on the `/api/v1/trees` router (not
   via Mount), so the paths are `/api/v1/trees/{tree_id}/export` and
   `/api/v1/trees/import` — not under a separate `/export` mount.

5. **Context compiler:** `GET /api/v1/context/{node_id}` is registered but not
   documented in the README.

6. **MCP endpoint:** `POST /api/v1/mcp` is registered and is now documented in
   the README (§ API Reference → MCP) as well as here — this entry is kept as
   history: before DF-HERMES-CANOPY-10 the endpoint answered `-32601` for
   `initialize`, so it was advertised in `entry_point` and absent from both the
   README and any usable handshake.

7. **Plugin endpoints:** earlier revisions of this document described a
   `register`/`install` route pair and an `instances` list plus pause/resume
   lifecycle. Those claims are STALE: that route set belonged to the first
   plugin handler (GAP-002) and was replaced when the PL-01 registry/lifecycle
   surface landed. None of those routes is mounted today: a documented `POST`
   to them matches no route, and `GET /api/v1/plugins/instances` is captured by
   the mounted `GET /api/v1/plugins/{id}` as a plugin lookup for the id
   `instances`. The mounted surface is the registry and lifecycle set in
   § Plugins (registration is `POST` on the plugins collection route; the
   lifecycle routes are keyed by plugin name or slug; source and detail are
   keyed by the plugin UUID), plus the separately registered
   `POST /api/v1/plugins/network-proxy` and the plugin events stream. § Plugins
   and this entry are pinned to the mounted routes by
   `TestRouteParityDocumentedPluginRoutes` (`internal/server/route_parity_test.go`),
   which fails when a documented plugin route is not mounted, when a mounted
   plugin route is undocumented, or when a stale plugin path reappears in the
   section.

8. **Profile endpoints:** Mounted at `/api/v1/workspaces/{workspace_id}/profiles`
   — not documented in the README.

9. **Transport endpoints:** Mounted at `/api/v1/transports` — not documented
    in the README.

10. **MLS endpoints:** Mounted at `/api/v1/workspaces/{workspace_id}/mls` — not
    documented in the README.

11. **Sync endpoints:** Mounted at `/api/v1/trees/{tree_id}/sync` — not
    documented in the README.

12. **Health probes:** `/health/transports/{type}` (public) — not documented in
    the README.

13. **Live Hermes gateway (GAP-050):** Mounted at `/api/v1/gateway` — canopyd
    is a CLIENT of the Hermes gateway api_server (`hermes gateway run`,
    default `http://127.0.0.1:8642`, configurable via
    `HERMES_WEBUI_GATEWAY_BASE_URL`; auth via `HERMES_WEBUI_GATEWAY_API_KEY`
    or the `API_SERVER_KEY` fallback). Endpoints:

    - `GET  /api/v1/gateway/status` — gateway connectivity + run counts
    - `GET  /api/v1/gateway/models` — the gateway's model catalog: every model
      it reports, with the budget each would get (GAP-080 phase 2b; see below)
    - `GET  /api/v1/gateway/runs` — run registry (newest first, live status
      refresh for non-terminal runs)
    - `POST /api/v1/gateway/runs` — `{message, session_id?, node_id?,
      model?, token_budget?}` → starts a REAL Hermes agent run (`POST /v1/runs`
      on the gateway; 202 + run_id). `model` is forwarded to the gateway
      verbatim and — when `token_budget` is absent — selects the model whose
      context window sizes the default budget (`CONTEXT_BUDGET_PERCENT`,
      default 60%). An explicit `token_budget` always wins, unchanged
    - `GET  /api/v1/gateway/runs/{run_id}` — run record with event history
    - `GET  /api/v1/gateway/runs/{run_id}/events` — SSE stream (history
      replay + live fan-out of gateway lifecycle events)
    - `POST /api/v1/gateway/runs/{run_id}/stop` — interrupt the run
    - `POST /api/v1/gateway/runs/{run_id}/approval` —
      `{choice: once|session|always|deny, approval_id?}` — resolve a pending
      approval

    **Context manifests on model calls (GAP-075).** `node_id` (a UUID) opts a
    run into the context compiler; the design authority is
    `specs/SPEC-FTR-07-hermes-agent-gateway-integration.md` § "Context manifest
    assembly". With `node_id` the COMPILED payload — not the raw message —
    becomes the gateway's `input`, so the model call has a visible, auditable
    manifest; `token_budget` overrides the default budget for that call only.
    Compilation never falls back to the raw message.

    **Window-derived default budget (GAP-080).** With no `token_budget` the
    default is `CONTEXT_DEFAULT_BUDGET` (8000) unless `model` names a model
    whose context window is known, in which case it
    becomes `floor(window × CONTEXT_BUDGET_PERCENT / 100)` (60% by default).
    The window comes from the gateway's model catalog first and from
    `CONTEXT_MODEL_WINDOWS` second (a window the operator declared locally —
    see "Declared context windows" below), so a model the gateway reports no
    window for still gets a real derived budget instead of the flat fallback.
    `CONTEXT_BUDGET_PERCENT=0` disables the derivation; an unknown model, no
    `model` at all, or an unreachable model catalog with nothing declared for
    that model falls back to
    `CONTEXT_DEFAULT_BUDGET` — the model list is cached for five minutes, and a
    failed lookup never fails the run. The list is read from a bare array, a
    `models` key, or the OpenAI-style `data` key the gateway sends (`models`
    wins if an object carries both); an entry that reports no context window is
    unknown, so the budget falls back rather than being invented. An explicit
    `token_budget` is applied verbatim (this route has never clamped it — the
    read route `GET /api/v1/context/{node_id}` is the only surface that clamps
    an explicit budget, and it clamps against the model's window while this one
    does not).

    **Model catalog for the UI (GAP-080 phase 2b).** `GET /api/v1/gateway/models`
    exposes the same catalog the budget derivation reads, so a client can offer
    a model choice and size a budget control without guessing:

    ```json
    {
      "models": [
        {"id": "big-model", "context_window": 200000, "desired_budget": 120000},
        {"id": "no-window", "context_window": 0,      "desired_budget": 8000}
      ],
      "percent": 60,
      "default_budget": 8000,
      "source": "window"
    }
    ```

    - `desired_budget` — `floor(context_window × percent / 100)`, never below 1
      for a positive window; the flat `default_budget` when `percent` is 0 or
      the entry reports no usable window.
    - `source` — `window` when at least one entry yields a window-derived
      budget from a window the GATEWAY reported, `configured_window` when no
      live window was seen anywhere but a listed entry was enriched from
      `CONTEXT_MODEL_WINDOWS` (below), `unknown_model` when the catalog answered
      but reports no window for anything, `disabled` when
      `CONTEXT_BUDGET_PERCENT=0`, and
      `catalog_error` / `no_catalog` when the list could not be read at all.
      With `disabled` the models are still listed (the knob turns off the
      derivation, not the catalog) and every `desired_budget` is the flat
      default.
    - The route **never answers 5xx because the catalog is down**: a failure is
      `200` with `"models":[]` — an empty ARRAY, never `null` — and the matching
      `source`, so a client degrades to "no model choice" instead of an error
      state. Declared windows cannot stand in for the gateway's own list, so a
      catalog failure lists nothing even when `CONTEXT_MODEL_WINDOWS` is set.
    - Entries are sorted by `id`, and the list comes from the same five-minute
      cache the two compile surfaces use (one gateway call per TTL for the whole
      server).

    **Declared context windows (`CONTEXT_MODEL_WINDOWS`, GAP-080 phase 2a).**
    A gateway is allowed to report no window at all, and the live Hermes gateway
    does: `GET /v1/models` answers an OpenAI-style envelope whose entry carries
    no `context_length`

    ```json
    {"object": "list", "data": [{"id": "Hermes Agent", "object": "model",
      "created": 1789707496, "owned_by": "hermes", "permission": [],
      "root": "Hermes Agent", "parent": null}]}
    ```

    so with the knob unset every budget falls back to `CONTEXT_DEFAULT_BUDGET`
    and the derivation is INERT. `CONTEXT_MODEL_WINDOWS` lets the operator
    declare the missing windows locally — comma-separated `model=window` pairs,
    e.g. `CONTEXT_MODEL_WINDOWS="Hermes Agent=200000,probe-big=128000"`
    (whitespace around a pair, the name and the number is ignored; the LAST `=`
    of a pair separates the two, so a model id may contain `=`).

    Measured A/B (2026-09-17) on a scratch canopyd against this live gateway,
    same route and same model: with the knob set

    ```json
    {"models":[{"id":"Hermes Agent","context_window":200000,"desired_budget":120000}],
     "percent":60,"default_budget":8000,"source":"configured_window"}
    ```

    and with the knob unset

    ```json
    {"models":[{"id":"Hermes Agent","context_window":0,"desired_budget":8000}],
     "percent":60,"default_budget":8000,"source":"unknown_model"}
    ```

    Precedence per model — a window the gateway reports ALWAYS wins, so a
    declaration only ever fills a gap: **live catalog window (>0) → declared
    window (>0) → neither**. The declared source answers a model the catalog
    lists without a window, a model the catalog does not list at all, and a
    model whose catalog call failed; `catalog_error` survives only when the
    catalog call failed AND nothing was declared for that model, and
    `unknown_model` only when neither source knows the model. The two surfaces
    then differ exactly as they do for the flat fallback: a compile surface
    derives `floor(declared window × percent / 100)`, while the read route
    ENRICHES rather than invents — every gateway-listed model with no live
    window is reported with its declared window and the budget derived from it,
    a model that exists only in the declarations is never listed (the UI would
    offer a model the gateway rejects), and `source` becomes
    `configured_window`.

    Unset/empty means no overrides and leaves every answer byte-identical to
    the pre-knob behaviour. A malformed pair (no `=`, a blank model name, a
    non-integer window, a window ≤ 0, an empty entry from a stray comma) is a
    **startup error** from `Validate()`: the knob exists to switch the
    derivation on, so a typo must fail loudly instead of silently doing nothing.

    The run record — the `run` object in the 202 response and the body of
    `GET /api/v1/gateway/runs/{run_id}` — then carries:

    - `source_node_id` — the compile target the manifest describes
    - `token_budget` — the effective budget applied
    - `context_tokens` — the manifest's `tokensUsed`
    - `manifest` — the compiler manifest verbatim

    All four are `omitempty`: without `node_id` the raw message is sent and the
    record keeps its previous shape.

    > **Amended 2026-09-17 (GAP-080 phase 5a).** The manifest carried here now
    > also includes `manifestHash` — the stable 64-hex digest of the compiled
    > payload — so a reader can recompute it from this record and compare it
    > with the preview compile from `GET /api/v1/context/{node_id}` (see
    > [§ Compile Context](#compile-context)). The field list above is unchanged;
    > this note is additive, and records written before the field existed are
    > unaffected (they simply carry no `manifestHash`, and still recompute to
    > the same digest for the same content).

    Node-scoped failure modes:

    - `400 invalid_request` — `node_id` is not a UUID (also a missing/blank
      `message`)
    - `404 node_not_found` — the compiler has no such node
    - `422 context_compile_failed` — any other compile failure
    - `503 context_compiler_unavailable` — the server was built without a
      context compiler wired into the gateway surface

    The gateway API key is held server-side; the browser never sees it.

14. **File-viewer routes and error mapping (GAP-071 docs pass):** the files that
    `internal/fileviewer/handlers.go` registers (SPEC-PL-02 phase 1, migrations
    43–46) — 13 routes: ten under `/api/v1/files` and three under
    `/api/v1/viewers` (the task list named twelve and omitted
    `DELETE /api/v1/files/{id}`) — were absent from README.md, docs/API.md and
    docs/INTEGRATION.md. They are now documented (see § File Viewers); two
    drifts remain between the Go sentinel contract and the handlers, both
    verified live against a fresh DB:

    - `fileviewer.ErrStorageUnavailable` is documented as `STREAM_INTERRUPTED`
      (503) in `internal/fileviewer/errors.go` but has no case in
      `handlers.go::fail`, so a storage failure falls through to
      `500 INTERNAL_ERROR`.
    - `fileviewer.ErrFileSoftDeleted` (`FILE_SOFT_DELETED`, 410) has a case in
      `handlers.go::fail` but no producer: `PGFileMetadataRepo.GetByID` filters
      `deleted_at IS NULL` and returns `ErrFileNotFound`, so a soft-deleted
      file reads back as `404 FILE_NOT_FOUND_BY_ID`. The 410 branch is dead
      code.

    `DELETE /api/v1/files/{id}` (soft delete) also exists in code and is now
    documented.

15. **Agents and reviews (GAP-087 docs pass):** the agent roster
    (`/api/v1/agents`) and the PR review panel (`/api/v1/reviews` plus
    `POST /api/v1/reviews/{pr}/trigger`) were mounted and auth-reachable but
    appeared in no shipped document — not in README.md, not in this file, not in
    `specs/` — so an operator could neither discover nor audit them. They are now
    documented in § Agents and § Reviews and pinned to the mounted router in both
    directions by `TestRouteParityDocumentedAgentRoutes` and
    `TestRouteParityDocumentedReviewRoutes`
    (`internal/server/route_parity_test.go`), which fail when a route documented
    in either section is not mounted, when a mounted agent or review route is
    undocumented, or when the route-line extractor would accept a foreign path
    inside either section. Two code facts the silence hid: both registries are
    in-memory (no DB table, demo rows seeded on construction, ids derived
    deterministically from the name/PR so they survive a restart), and the review
    verdict is a deterministic simulation derived from the PR string — no model
    is called.

16. **SPEC-API-06 multi-user routes:** SPEC-API-06 marked twelve tree-scoped
    invite, membership, and profile routes as Required, but none is mounted on
    the real router. Live probes and the route table both show raw chi 404s
    with no error envelope for those paths. The shipped collaboration surface
    is the workspace-scoped `/api/v1/collab` mount (workspace CRUD, members,
    invitations, join, and leave), plus workspace profiles at
    `/api/v1/workspaces/{workspace_id}/profiles` and tree sharing at
    `POST /api/v1/trees/{tree_id}/share`. The SPEC-API-06 Required column was
    amended to Deferred (not implemented) in this commit; the parity tests
    `TestRouteParityCollabRoutes`, `TestRouteParityWorkspaceProfileRoutes`,
    and `TestSpecAPI06RequiredRoutesAbsent` pin both the shipped and deferred
    sides.
