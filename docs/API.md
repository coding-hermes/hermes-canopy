# Canopy API Reference

Base URL: `http://<host>:<port>/api/v1`

All authenticated endpoints require a JWT Bearer token in the `Authorization`
header. Health and version endpoints are public.

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

## Trees

Mounted at `/api/v1/trees`. All require auth.

### List Trees

```
GET /api/v1/trees
```

**Query params:** `sort`, `status`, `search`, `limit` (int), `cursor` (UUID)

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
    "contentFormat": "string (optional)",
    "nodeType": "string (optional)"
  }
}
```

**Response (201):** Tree detail with `root_node_id`, `owner_id`, `created_at`.

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

---

## Nodes

Two mount points exist:

1. **Tree-scoped:** `/api/v1/trees/{tree_id}/nodes` — membership-gated via
   `TreeMembershipMiddleware`
2. **Flat:** `/api/v1/nodes` — access-gated via `NodeAccessMiddleware` (resolves
   the node's tree and checks membership)

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
  "parent_id": "uuid (optional, empty = root child)",
  "content": "string (required, max 64KB)",
  "content_format": "string (optional, default 'markdown')",
  "node_type": "string (optional, default 'message')",
  "edge_type": "string (optional, default 'reply')",
  "metadata": "object (optional)"
}
```

**Response (201):** `{ "node": {...}, "edge": {...} }` — the created node and
the edge connecting it to its parent.

**Error codes:** `INVALID_TREE_ID` (400), `INVALID_BODY` (400), `EMPTY_CONTENT` (400),
`CONTENT_TOO_LARGE` (400), `INVALID_PARENT_ID` (400), `VALIDATION_ERROR` (400),
`NOT_FOUND` (404), `GONE` (410), `CONFLICT` (409), `FORBIDDEN` (403)

### Get Node

```
GET /api/v1/trees/{tree_id}/nodes/{node_id}   (tree-scoped)
GET /api/v1/nodes/{tree_id}/nodes/{node_id}   (flat tree-scoped form)
```

**Response (200):** Full node detail.

**Error codes:** `INVALID_NODE_ID` (400), `NODE_NOT_FOUND` (404), `NOT_TREE_MEMBER` (403)

### Update Node

```
PATCH /api/v1/nodes/nodes/{node_id}
```

**Request body:** (partial)
```json
{
  "content": "string (optional)",
  "content_format": "string (optional)",
  "metadata": "object (optional)"
}
```

**Response (200):** Updated node detail.

### Delete Node

```
DELETE /api/v1/nodes/nodes/{node_id}
```

**Response (200):** Soft-deleted node detail (includes `deleted_at`).

### Reply to Node

```
POST /api/v1/nodes/nodes/{node_id}/reply
```

**Request body:**
```json
{
  "content": "string (required)",
  "content_format": "string (optional)",
  "node_type": "string (optional)",
  "metadata": "object (optional)"
}
```

**Response (201):** `{ "node": {...}, "edge": {...} }`

### Fork from Node

Preferred (tree-scoped, membership-enforced):

```
POST /api/v1/trees/{tree_id}/nodes/{node_id}/fork
```

Deprecated (flat mount, double `/nodes/` segment — still supported for
backward compatibility):

```
POST /api/v1/nodes/nodes/{node_id}/fork
```

**Request body:**
```json
{
  "content": "string (required)",
  "content_format": "string (optional)",
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
    { "source_id": "uuid", "target_id": "uuid", "edge_type": "string" }
  ]
}
```

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

**Response (201):** Created topic detail.

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
stored in DuckDB (per-type SQLite databases under `~/.hermes/canopy/cards/`).

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
```

**Request body:**
```json
{
  "data": "object (required)"
}
```

**Response (200):** Updated card.

### Archive Card

```
DELETE /api/v1/cards/{card_id}
```

**Response (200):** Archived card detail.

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
| `edge_added` | An edge was created |
| `edge_removed` | An edge was deleted |
| `tree_created` | A tree was created |
| `tree_updated` | A tree was updated |
| `tree_deleted` | A tree was deleted |

Heartbeat: `: heartbeat` (SSE comment, every 30s).

**Connection limits:** 10 per user, 100 per tree, 10,000 server-wide.

**Error codes (pre-SSE, returned as JSON):** `INVALID_TREE_ID` (400),
`INVALID_SINCE_HASH` (400), `INVALID_PROFILE_ID` (400),
`TOO_MANY_CONNECTIONS_TREE` (429), `TOO_MANY_CONNECTIONS` (503),
`STREAMING_NOT_SUPPORTED` (500), `SUBSCRIPTION_FAILED` (500)

---

## Context Compiler

### Compile Context

```
GET /api/v1/context/{node_id}
```

**Query params:**
- `budget` — token budget (int, default 8000, max 10x default)
- `includeCards` — bool, default false
- `resolveRefs` — bool, default true
- `maxAncestors` — int, default 0 (unbounded)

**Response (200):** Compiled context with visible manifest.

**Error codes:** `NODE_NOT_FOUND` (404), `INVALID_BUDGET` (400),
`SERVICE_UNAVAILABLE` (503), `CONTEXT_COMPILE_ERROR` (500)

---

## Plugins

Mounted at `/api/v1/plugins`. All require auth.

### Register Plugin

```
POST /api/v1/plugins/register
```

**Request body:**
```json
{
  "source": "string (JS source with manifest comment block)"
}
```

**Response (201):** Registered plugin metadata.

### List Plugins

```
GET /api/v1/plugins
```

**Query params:** `limit` (int), `offset` (int)

**Response (200):** Paginated plugin list.

### Get Plugin

```
GET /api/v1/plugins/{plugin_id}
```

**Response (200):** Plugin metadata (no source).

### Get Plugin Source

```
GET /api/v1/plugins/{plugin_id}/source
```

**Response (200):** Raw source with `X-Source-SHA256` header.

### Install Plugin

```
POST /api/v1/plugins/{plugin_id}/install
```

**Request body:**
```json
{
  "treeId": "uuid (optional)",
  "grantedPermissions": ["string"]
}
```

**Response (200):** Installation result.

### List Instances

```
GET /api/v1/plugins/instances
```

**Query params:** `treeId` (UUID, optional)

**Response (200):** List of plugin instances for the caller.

### Pause Instance

```
POST /api/v1/plugins/instances/{instance_id}/pause
```

**Response (200):** Paused instance.

### Resume Instance

```
POST /api/v1/plugins/instances/{instance_id}/resume
```

**Response (200):** Resumed instance.

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

### Get Key Package

```
GET /api/v1/workspaces/{workspace_id}/mls/key-packages
```

### Commit Proposals

```
POST /api/v1/workspaces/{workspace_id}/mls/commit-proposals
```

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
| `RATE_LIMITED` | 429 | Too many requests (100/s per IP, burst 200) |
| `SERVICE_UNAVAILABLE` | 503 | Database unavailable |
| `INTERNAL_ERROR` | 500 | Unexpected server error |

### Node-Specific Error Codes

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `EMPTY_CONTENT` | 400 | Content field is empty |
| `CONTENT_TOO_LARGE` | 400 | Content exceeds 64KB |
| `INVALID_PARENT_ID` | 400 | parent_id is not a valid UUID |
| `GONE` | 410 | Node was already deleted |
| `CONFLICT` | 409 | Parent node was deleted |

### Tree-Specific Error Codes

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `TREE_NOT_FOUND` | 404 | Tree does not exist |
| `TREE_DELETED` | 410 | Tree was soft-deleted |

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

4. **Nodes flat mount:** The README shows `GET /api/v1/nodes` and
   `POST /api/v1/nodes` as flat endpoints, but the actual flat mount
   (`/api/v1/nodes`) uses a `NodeAccessMiddleware` that expects paths like
   `/api/v1/nodes/{tree_id}/nodes[/{node_id}]` (tree-scoped flat form) or
   `/api/v1/nodes/nodes/{node_id}` for update/delete/reply/fork. The tree-scoped
   mount at `/api/v1/trees/{tree_id}/nodes` is the primary list/create path.

5. **Export/Import:** Registered directly on the `/api/v1/trees` router (not
   via Mount), so the paths are `/api/v1/trees/{tree_id}/export` and
   `/api/v1/trees/import` — not under a separate `/export` mount.

6. **Context compiler:** `GET /api/v1/context/{node_id}` is registered but not
   documented in the README.

7. **MCP endpoint:** `POST /api/v1/mcp` is registered but not documented in the
   README.

8. **Plugin endpoints:** `POST /api/v1/plugins/register`, instance lifecycle,
   and source retrieval are registered but not fully documented in the README.

9. **Profile endpoints:** Mounted at `/api/v1/workspaces/{workspace_id}/profiles`
   — not documented in the README.

10. **Transport endpoints:** Mounted at `/api/v1/transports` — not documented
    in the README.

11. **MLS endpoints:** Mounted at `/api/v1/workspaces/{workspace_id}/mls` — not
    documented in the README.

12. **Sync endpoints:** Mounted at `/api/v1/trees/{tree_id}/sync` — not
    documented in the README.

13. **Health probes:** `/health/transports/{type}` (public) — not documented in
    the README.
