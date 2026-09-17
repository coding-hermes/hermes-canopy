# Hermes Canopy

Canopy OS — graph-native collaboration surface for human-agent work.

Messages are nodes in a DAG. Every model call has a visible context manifest.
Every Card is a graph node with structured data.

<p align="center">
  <img src="docs/screenshots/canopy-hero.jpg" alt="Hermes Canopy — graph-native collaboration surface" width="100%">
</p>

## Screenshots

<p align="center">
  <img src="docs/screenshots/canopy-tree-view.png" alt="Conversation DAG in the macro tree view" width="100%">
  <br>
  <em>Conversations render as a live DAG — branch, fork, and synthesize messages as graph nodes.</em>
</p>

<p align="center">
  <img src="docs/screenshots/canopy-live-gateway.png" alt="Live Hermes gateway runs and event stream" width="100%">
  <br>
  <em>Chat with a live Hermes gateway from inside Canopy — real runs, real events, no seeded data.</em>
</p>

## Quick Start

```bash
# Prerequisites
go 1.25+
PostgreSQL 16+
Node.js 22+ (for frontend development)

# Clone and build
git clone https://github.com/coding-hermes/hermes-canopy.git
cd hermes-canopy
make build

# The DuckDB card backend (internal/card/duckdb, go-duckdb) is cgo-only and is
# build-tagged `//go:build cgo`, so it is excluded when CGO_ENABLED=0; the
# default SQLite card backend (modernc.org/sqlite) is pure Go and unaffected.

# Start PostgreSQL (Docker) — standalone option.
# Already running the compose stack? Skip this block: `docker compose up -d`
# (docs/INTEGRATION.md §2) already starts a postgres service publishing host
# port 5437, and this command would fail on the port. The standalone container
# uses a DISTINCT name (`canopy-pg-standalone`) so it never collides with
# compose's `canopy-pg`. Host port 5437 matches docker-compose.yml (which maps
# 5437:5432) so the same DB_PORT works on either path.
if docker ps --format '{{.Ports}}' | grep -q ':5437->'; then
  echo "PostgreSQL already running on :5437 (compose stack) — skipping standalone Postgres (docs/INTEGRATION.md §2)."
else
  # Skip this step after `docker compose up -d` (it already runs `canopy-pg`), or use a distinct container name.
  docker run -d --name canopy-pg-standalone \
    -e POSTGRES_USER=canopy -e POSTGRES_PASSWORD=canopy \
    -e POSTGRES_DB=canopy -p 5437:5432 postgres:16
fi

# Run (dev: backend on :8091 to match the Vite dev proxy target)
DB_HOST=localhost DB_PORT=5437 DB_USER=canopy DB_PASSWORD=canopy DB_NAME=canopy \
  HTTP_ADDR=:8091 ./bin/canopyd

# Frontend (dev mode)
cd frontend
npm install
npm run dev

# CLI (separate terminal) — the CLI is an HTTP CLIENT of an already-running
# canopyd, so it is configured with a URL, not with server settings:
#   CANOPY_SERVER_URL  → the API base URL the CLI talks to (client setting)
#   HTTP_ADDR / DB_*   → listen address + PostgreSQL of a SERVER process; they
#                        never redirect the CLI.
# Without CANOPY_SERVER_URL the CLI targets http://localhost:8091, and it
# REFUSES to run when HTTP_ADDR/DB_* are set without an explicit URL: `tree
# create` is a write, and the CLI will not guess which instance to write into.
export CANOPY_SERVER_URL=http://localhost:8091   # the API you mean to change
export CANOPY_TOKEN=your-jwt-token               # see "Authentication (dev mode)"
./bin/canopyd tree create "My Tree" --content 'Hello from the CLI'
./bin/canopyd tree list

# Troubleshooting: "STALE BUILD" at startup
# If canopyd refuses to start with `STALE BUILD: database schema is newer than
# this binary's embedded migrations`, your database was created by a NEWER
# canopyd than the one you just ran (common after `git pull` without rebuild,
# or a compose image built from older HEAD). Fix: `make build` (or rebuild the
# docker image) and run again. `/health` reports both versions for comparison:
#   curl http://localhost:8091/health
#   -> {"status":"ok","service":"canopyd","schema_version":38,"embedded_migrations":38}

# Open the frontend
open http://localhost:5173  # dev mode (Vite dev server)
# The canopyd binary is API-only in MVP — the PWA is served separately, and a
# production build needs a same-origin reverse proxy, NOT "any static server":
# see the Note under "Production (Manual)" below, or deploy/reference-proxy.py.
```

## Authentication (dev mode)

Canopy uses HS256 JWT Bearer tokens for authentication. In development you **never
need to create a token manually** — but there are no `/api/v1/auth/register|login`
endpoints (multi-user auth is deferred post-MVP per AGENTS.md).

### Frontend (`npm run dev`)

The Vite dev proxy (`frontend/vite.config.ts`) auto-injects a pre-generated dev JWT
on every `/api` request. The token is HS256-signed with the backend's default secret
`dev-secret-change-me`, with `sub=00000000-0000-0000-0000-000000000001` and a
365-day expiry. This means `npm run dev` → instant authenticated access, zero
manual auth.

Override the token with `VITE_DEV_JWT` if you need a different subject or expiry.

### Direct API access (curl, scripts, etc.)

Sign your own HS256 JWT with `JWT_SECRET` (default `dev-secret-change-me`), then
send it as a Bearer token:

```bash
# Using node (quick one-liner)
node -e "
const crypto = require('crypto');
const header = Buffer.from(JSON.stringify({alg:'HS256',typ:'JWT'})).toString('base64url');
const payload = Buffer.from(JSON.stringify({
  sub:'00000000-0000-0000-0000-000000000001',
  iat:Math.floor(Date.now()/1000),
  exp:Math.floor(Date.now()/1000)+86400
})).toString('base64url');
const sig = crypto.createHmac('sha256','dev-secret-change-me').update(header+'.'+payload).digest('base64url');
console.log(header+'.'+payload+'.'+sig);
"
```

Then:

```bash
curl -H "Authorization: Bearer <token>" http://localhost:8091/api/v1/trees
```

### Public paths (no auth)

`/health`, `/healthz`, `/version` — no token required.

### Dev-mode provisioning (fresh database)

When canopyd starts with the **default** dev secret it provisions the dev
identity itself, so a fresh database needs no manual SQL (the previously
required `scripts/seed-demo-data.sql` is optional again):

| Row | Value |
|-----|-------|
| `users` | `00000000-0000-0000-0000-000000000001` (`dev@canopy.dev`) — GAP-064 |
| `workspaces` | `00000000-0000-0000-0000-000000000010` (`Dev Workspace`, slug `dev`) — GAP-071 |
| `profiles` | `dev-hermes` (`Dev Hermes`), owned by the dev user — the file-viewer acting profile |
| `profile_route` | the dev workspace → `dev-hermes`, active |

Every insert is idempotent (a second boot inserts nothing) and the whole block
is skipped when `JWT_SECRET` is set to anything else — a production server
never mints users, workspaces or profiles. Provisioning is what makes the
[File viewers](#file-viewers-spec-pl-02) routes and
`POST /api/v1/workspaces/{ws}/profiles` work on a brand-new database.

### Production

**MUST** set a real `JWT_SECRET` environment variable. The dev secret
`dev-secret-change-me` must never leave the dev environment (see the comment in
`frontend/vite.config.ts`).

**The PWA has no login flow** (multi-user auth is deferred post-MVP — there is no
`/api/v1/auth/register|login` endpoint), and the Vite **dev** proxy is the only
component that injects a JWT today. A production static build therefore
authenticates in exactly one of these ways:

1. **The browser carries the token.** Build the PWA with
   `VITE_API_TOKEN=<jwt>`, or paste a token into the browser's
   `localStorage['canopy.token']`. `frontend/src/lib/api.ts` then sends
   `Authorization: Bearer <jwt>` on every API call (see README §"Production
   (Manual)" for the full recipe).
2. **The reverse proxy injects the token** — `deploy/reference-proxy.py --token`,
   or the commented block in `deploy/nginx.canopy.conf` / `deploy/Caddyfile`.
   Only ever behind explicit authentication (HTTP Basic / auth gate): injecting a
   JWT on an open route hands a live canopyd credential to every anonymous
   caller, and the reference proxy refuses to start in that configuration.

Either way the JWT is minted by you (below) — deployment is **single-user** in
MVP.

For full auth details (claims, error codes, middleware), see [docs/API.md](docs/API.md) §Auth.

## Architecture

### Backend (canopyd)

```
┌─────────────────────────────────────────────────────┐
│                   HTTP Server (:8091)                │
│  ┌──────────────┐  ┌──────────┐  ┌───────────────┐  │
│  │   Handlers   │  │   SSE    │  │   Telemetry   │  │
│  │  (REST API)  │  │   Hub    │  │  (Prometheus) │  │
│  └──────┬───────┘  └────┬─────┘  └───────┬───────┘  │
│         │               │                │          │
│  ┌──────┴────────────────┴────────────────┴───────┐  │
│  │              Services Layer                      │  │
│  │  Tree | Node | Edge | Topic | Card | Graph     │  │
│  │  Approval | Sync | Profile | MLS               │  │
│  └─────────────────────┬──────────────────────────┘  │
│                        │                             │
│  ┌─────────────────────┴──────────────────────────┐  │
│  │              Data Layer (db/)                    │  │
│  │  Repositories | Migrations | Models            │  │
│  │  PostgreSQL (primary) + DuckDB (cards)         │  │
│  └────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
```

### Frontend (React + TypeScript + Vite)

```
┌─────────────────────────────────────────────────────┐
│                   PWA (Service Worker)               │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────┐  │
│  │  React   │  │  React   │  │  Yjs CRDT        │  │
│  │  Router  │  │  Flow    │  │  (Local replica)  │  │
│  └────┬─────┘  └────┬─────┘  └────────┬─────────┘  │
│       │             │                 │            │
│  ┌────┴─────────────┴─────────────────┴─────────┐  │
│  │            Data Stores                        │  │
│  │  treeStore | yjsProvider | usePresence       │  │
│  │  y-indexeddb (offline persistence)           │  │
│  └─────────────────────┬────────────────────────┘  │
│                        │                            │
│  ┌─────────────────────┴────────────────────────┐  │
│  │            SSE Sync Provider                   │  │
│  │     (Server-sent Events → Yjs updates)        │  │
│  └────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
```

### Data Model

| Entity | Storage | Description |
|--------|---------|-------------|
| Tree | PostgreSQL | Root container for a graph of nodes |
| Node | PostgreSQL + Yjs | A message or entity in the DAG; CRDT content in Yjs |
| Edge | PostgreSQL | Directed relationship between nodes (reply, fork, synthesis) |
| Topic | PostgreSQL | Named, searchable subgraph with #references |
| Approval | PostgreSQL | Multi-step approval workflow for merge operations |
| MLS Group | PostgreSQL | Encrypted group messaging (post-MVP) |
| Snapshot | PostgreSQL | Point-in-time tree state for recovery |
| Event | PostgreSQL | Audit trail of all operations |
| Profile | PostgreSQL | User profiles and routing |
| Card | DuckDB+JSONL | Structured data nodes with interactive behavior |
| Transport | PostgreSQL | Multi-transport connection management (SSE, WebSocket, NATS) |

## API Reference

> **Canonical reference: [docs/API.md](docs/API.md)** — this table is a
> quick-reference of the primary endpoints, generated from
> `internal/server/server.go` + handler `Routes()` (verified live, GAP-032).
> All paths are under `/api/v1` and require a JWT Bearer token unless noted.
> Success **envelopes differ per route** — tree/topic/card create answer with a
> bare object, node create/reply/fork with `{"node":…,"edge":…}`, and
> `GET /files/recents` with a bare array. See
> [docs/API.md § Response envelopes](docs/API.md#response-envelopes-per-route).

### Trees

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/trees` | List all trees |
| `POST` | `/api/v1/trees` | Create a tree |
| `GET` | `/api/v1/trees/{tree_id}` | Get tree details |
| `PATCH` | `/api/v1/trees/{tree_id}` | Update tree metadata |
| `DELETE` | `/api/v1/trees/{tree_id}` | Soft-delete a tree |
| `POST` | `/api/v1/trees/{tree_id}/share` | Share a tree with a user |
| `POST` | `/api/v1/trees/{tree_id}/presence` | Push presence heartbeat |
| `POST` | `/api/v1/trees/{tree_id}/presence/leave` | Leave presence session |

### Nodes

Tree-scoped (primary, membership-gated):

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/trees/{tree_id}/nodes` | List nodes in a tree |
| `POST` | `/api/v1/trees/{tree_id}/nodes` | Create a node |
| `GET` | `/api/v1/trees/{tree_id}/nodes/{node_id}` | Get node details |
| `POST` | `/api/v1/trees/{tree_id}/nodes/{node_id}/fork` | Fork a node (create child branch; source must already have ≥1 child) |

Reference context (SPEC-PL-06 §9.3) — flat surface, bare node id, membership
resolved per node:

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/nodes/{node_id}/reference-context` | Stored provenance of a multi-reference reply — `include_content`, `max_source_tokens` (max 2048), `verify_hash`; `404 REFERENCE_CONTEXT_NOT_FOUND` for a node that is not a multi-reference reply |

> Edges are managed **implicitly** through node operations (reply/fork/synthesis)
> — there is no standalone edge API.

### Graph

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/graph/trees/{tree_id}/subtree/{node_id}` | Get subtree (all descendants) |
| `GET` | `/api/v1/graph/trees/{tree_id}/ancestors/{node_id}` | Get ancestor chain |
| `GET` | `/api/v1/graph/trees/{tree_id}/stats` | Graph statistics |

### Sync (Yjs)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/trees/{tree_id}/sync` | Fetch Yjs snapshot |
| `POST` | `/api/v1/trees/{tree_id}/sync` | Push Yjs update |
| `POST` | `/api/v1/trees/{tree_id}/sync/snapshot` | Trigger a snapshot |

### Topics

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/topics?tree_id={tree_id}` | List topics — `tree_id` required; optional `status`, `limit`, `offset` |
| `POST` | `/api/v1/topics` | Create a topic — body: `treeId`, `rootNodeId`, `title` (required), `description` (optional) |
| `GET` | `/api/v1/topics/{topic_id}` | Get topic details |
| `PATCH` | `/api/v1/topics/{topic_id}` | Update topic |
| `DELETE` | `/api/v1/topics/{topic_id}` | Delete a topic |

> GET requires the `tree_id` query parameter; the create body uses camelCase
> keys. Full request/response contract: docs/API.md § Topics.

### Cards

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/cards` | List cards |
| `POST` | `/api/v1/cards` | Create a card |
| `GET` | `/api/v1/cards/{card_id}` | Get card details |
| `PATCH` | `/api/v1/cards/{card_id}` | Update card |
| `DELETE` | `/api/v1/cards/{card_id}` | Delete a card |

### Approvals

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/approvals` | List approvals |
| `GET` | `/api/v1/approvals/pending` | List pending approvals |
| `GET` | `/api/v1/approvals/history` | List approval history |
| `GET` | `/api/v1/approvals/{approval_id}` | Get approval details |
| `POST` | `/api/v1/approvals/{approval_id}/approve` | Approve |
| `POST` | `/api/v1/approvals/{approval_id}/deny` | Deny |

> Approval requests are created internally by merge operations — there is no
> public create endpoint.

### File viewers (SPEC-PL-02)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/files/upload` | Upload bytes (multipart `file` part) — content-addressed by `sha256`, dedup-aware (`was_deduped`) |
| `POST` | `/api/v1/files/resolve` | Resolve a file by `hash_ref` (JSON) |
| `POST` | `/api/v1/files/resolve/batch` | Resolve up to 200 `hash_ref` entries in one call |
| `GET` | `/api/v1/files` | List files — `sort` (`created_desc` default), `mimeFilter`, `extensionFilter`, `viewableOnly`, `excludeQuarantined`, `limit` (1–200), `cursor` |
| `GET` | `/api/v1/files/recents` | Recently accessed files — `limit` (1–200, default 50) |
| `GET` | `/api/v1/files/{id}` | File metadata |
| `GET` | `/api/v1/files/{id}/stream` | File bytes — `Range` supported (206, `Content-Range`), `If-None-Match` → 304 |
| `GET` | `/api/v1/files/{id}/access` | Access-log entries (append-only table) |
| `POST` | `/api/v1/files/{id}/access` | Append one access entry — `action` + `viewer_slug` required |
| `DELETE` | `/api/v1/files/{id}` | Soft-delete a file |
| `GET` | `/api/v1/viewers` | List registered viewers (`audio_video`, `code`, `csv`, `image`, `json`, `markdown`, `pdf`) |
| `GET` | `/api/v1/viewers/{slug}` | One viewer registration |
| `POST` | `/api/v1/viewers/dispatch` | Resolve the viewer for `{file_id}` without opening it |

> The `/files` routes and `/viewers/dispatch` act as the **acting profile** of
> the JWT `sub`. On a fresh database started with the default dev secret,
> canopyd provisions that profile (plus a dev workspace) at startup — see
> § Authentication (dev mode) below. Full request/response contracts and error
> codes: [docs/API.md](docs/API.md) § File Viewers; runnable walkthrough:
> [docs/INTEGRATION.md](docs/INTEGRATION.md) § 6.

### SSE (Server-Sent Events)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/trees/{tree_id}/events` | Tree event stream (SSE) |

### Health (no auth)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check |
| `GET` | `/metrics` | Prometheus metrics (if METRICS_ENABLED=true) |

### MCP (Model Context Protocol)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/mcp` | JSON-RPC 2.0 MCP endpoint — `initialize`, notifications, `ping`, `tools/list`, `tools/call` |

The MCP endpoint speaks JSON-RPC 2.0 over HTTP POST at
`POST /api/v1/mcp` (a trailing slash is accepted too). It requires the **same
JWT Bearer token as every other `/api/v1` route** (`Authorization: Bearer
<jwt>`) — it is mounted inside the authenticated group, not public. The
endpoint is **stateless**: it issues no session id, `initialize` is not
required before `tools/list`, and a client may reconnect without
re-initializing.

Handshake (`$TOKEN` is a dev JWT you sign yourself — see § Authentication
(dev mode); nothing prints a token at startup, so mint one with the snippet there
and `export CANOPY_TOKEN=<token>`):

```bash
# 1. initialize — negotiate a protocol revision
curl -s -X POST http://localhost:8080/api/v1/mcp \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
# 200 {"jsonrpc":"2.0","id":1,"result":{
#        "protocolVersion":"2025-06-18",
#        "capabilities":{"tools":{"listChanged":false}},
#        "serverInfo":{"name":"canopyd-canopy","version":"<build version>"}}}

# 2. notifications/initialized — a notification: HTTP 202 with an EMPTY body
curl -s -i -X POST http://localhost:8080/api/v1/mcp \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'
# HTTP/1.1 202 Accepted   (no body — a notification never gets an error object)

# 3. tools/list — the 7 tools this server implements
curl -s -X POST http://localhost:8080/api/v1/mcp \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
```

Protocol revision negotiation: `protocolVersion` echoes the client's request
when the server supports it (`2025-06-18`, `2025-03-26`, `2024-11-05`);
anything else is answered with the server's **newest** supported revision and
the client decides whether it can continue. `ping` answers `{}`; any method
this endpoint does not implement answers `-32601` (for example
`resources/list`); a non-object `params` answers `-32602`.

Tools: `list_trees`, `get_tree`, `create_node`, `list_topics`,
`get_graph_stats`, `list_approvals`, `list_cards` — invoked with
`{"method":"tools/call","params":{"name":"<tool>","arguments":{…}}}`.
The revision reported by the handshake is the MCP spec revision, not the build
version; `serverInfo.version` is the binary's build version, identical to
`canopyd -version`.

Full request/response contract: [docs/API.md § MCP](docs/API.md#mcp-model-context-protocol).

## Deployment

### Stale deployments: hourly check + operator recovery

The live service is the systemd user unit `canopy-canopyd.service`, which runs
`/home/kara/bin/canopyd` — a plain `make build` never updates what systemd
executes. The systemd user timer `canopy-deploy-check.timer` re-checks the
deployed binary **every hour** (`OnBootSec=10min`, `OnUnitActiveSec=1h`,
`Persistent=true`) and auto-deploys it when it is stale and the worktree is
clean.

If a check reports `STALE_BLOCKED` (exit 2) the artifact is stale but the
worktree has tracked/untracked changes, so the auto-deploy was refused **on
purpose**: half-written worker code must never ship. The recovery path is:

```bash
git status --short              # see what is dirty
git commit …  # or: git stash push -m "deploy-check"
make deploy                     # atomic: build → install → restart → health → smoke
```

…or simply wait: the next hourly tick deploys automatically once the tree is
clean again. Two consecutive refusals raise a `deploy_stale_blocked_alert`
event in `.coding-hermes/board/events.jsonl` (one dirty tick is normal worker
traffic; two in a row is an operator problem), and its `severity` field tells
the two situations apart:

| `severity` | Meaning |
|------------|---------|
| `stale_but_serving` | the unit is up, so a stale binary is at least still serving traffic — degraded, not an outage |
| `stale_refused_to_start` | the unit is **not** up while the artifact is stale — **outage class**, escalate |

Full reference (exit codes, thresholds, systemd units): see
[Deploying → Automated staleness detection](#automated-staleness-detection-gap-067).

### Docker (Recommended)

```bash
# Optional but recommended: copy the env template (API_SERVER_KEY etc.)
cp .env.example .env

# Build and run with Docker Compose
docker compose up -d

# This starts:
#   - canopyd on :8091 (host) → :8080 (container)
#   - PostgreSQL on :5437 (host) → :5432 (container) — note the non-standard host port!
#   - Health-gated startup (canopyd waits for PG)

# Verify
curl http://localhost:8091/health

# View logs
docker compose logs -f canopyd
```

### Production (Manual)

```bash
# Build the static binary
make build

# Run with production configuration
HTTP_ADDR=:8091 \
DB_HOST=your-pg-host DB_PORT=5432 \
DB_USER=canopy DB_PASSWORD=$(cat /etc/secrets/db-password) \
DB_NAME=canopy \
LOG_LEVEL=warn \
METRICS_ENABLED=true \
  ./bin/canopyd
```

> **Note:** `canopyd` is **API-only** in MVP — it does not serve the PWA.
> The binary exposes the REST/SSE API on `HTTP_ADDR` (`:8091` via `make run`
> and compose; the raw binary defaults to `:8080`);
> the frontend must be served separately, and it must be served by a SAME-ORIGIN
> reverse proxy — **not** by "any static server":
>
> ```bash
> cd frontend && npm ci && npm run build   # produces frontend/dist/ (NOT frontend/frontend/dist)
> cd ..                                    # run the proxy from the repo root
> python3 deploy/reference-proxy.py --dist frontend/dist --port 3000 \
>     --api http://127.0.0.1:8091
> # → http://localhost:3000
> ```
>
> **Why a plain static server does not work.** An SPA fallback (`npx serve -s`,
> CDN "SPA mode") answers *every* unknown path with `index.html` — including
> `GET /api/v1/trees`. The app then runs `res.json()` over `<!doctype html>` and
> fails with `Unexpected token '<', "<!doctype "... is not valid JSON` while the
> UI reports "Backend: unreachable". The fallback is only for app routes
> (`/trees`, `/tree/<id>`, so `BrowserRouter` deep links survive a refresh);
> `/api/` and `/health` must always reach canopyd. For a real deployment use
> `deploy/nginx.canopy.conf` (`proxy_buffering off`) or `deploy/Caddyfile`
> (`flush_interval -1`) — both implement that split plus SSE-safe streaming.
>
> **The example port is not reserved.** `:3000` is a convention, not a
> guarantee — it is occupied on plenty of hosts. Set `--port` (or the nginx/Caddy
> listener) to something free; `deploy/reference-proxy.py` refuses to start on a
> busy port.
>
> **Pointing the PWA at the API.** The frontend reads `VITE_API_BASE_URL`
> (`frontend/src/lib/api.ts`), **not** `VITE_API_URL` — that name is only the
> Vite **dev** proxy target (`frontend/vite.config.ts`). Leaving
> `VITE_API_BASE_URL` unset is correct for the same-origin proxy above: the app
> calls the relative `/api/v1`. See docs/INTEGRATION.md §5 for details.
>
> **Production auth is single-user in MVP.** There are no
> `/api/v1/auth/register|login` endpoints, and the Vite **dev** proxy is the only
> thing that injects a JWT today, so a static build authenticates in one of two
> ways: bake a token at build time (`VITE_API_TOKEN=<jwt>`, or paste one into
> `localStorage['canopy.token']` in the browser), or let the reverse proxy inject
> `Authorization: Bearer <jwt>` — which `deploy/reference-proxy.py` and the nginx
> configs only do **behind explicit authentication** (HTTP Basic / auth gate).
> Mint the JWT yourself with `JWT_SECRET` (see
> [Authentication (dev mode)](#authentication-dev-mode) → "Direct API access").

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HTTP_ADDR` | `:8080` | HTTP listen address |
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `canopy` | Database user |
| `DB_PASSWORD` | `canopy` | Database password |
| `DB_NAME` | `canopy` | Database name |
| `DB_SCHEMA` | `public` | Database schema |
| `DB_SSLMODE` | `disable` | PostgreSQL SSL mode |
| `LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `LOG_FORMAT` | `text` | Log format (text, json) |
| `METRICS_ENABLED` | `false` | Enable Prometheus metrics |
| `CORS_ORIGIN` | `*` | CORS allowed origins |
| `JWT_SECRET` | `dev-secret-change-me` | HS256 signing secret (set a real value in production) |
| `CANOPY_DB_URL` | *(unset)* | Override DSN; takes priority over all `DB_*` fields |
| `CONTEXT_MAX_ANCESTORS` | `50` | Max ancestors in context compilation |
| `CONTEXT_MAX_REFS` | `5` | Max topic references (soft; hard cap is 2×) |
| `CONTEXT_DEFAULT_BUDGET` | `8000` | Default token budget for context compilation |
| `PLUGIN_MAX_SIZE` | `1048576` | Max plugin source size in bytes (1 MB) |
| `CANOPY_SERVER_URL` | `http://localhost:8091` | **CLI only** — API base URL the `tree`/`topic` subcommands call; an explicit value wins over `DB_*`/`HTTP_ADDR`, and a malformed one fails before any request (see "CLI") |
| `CANOPY_TOKEN` | *(unset)* | **CLI only** — Bearer token sent with each CLI request |

## Development

### Prerequisites

- Go 1.25+
- PostgreSQL 16+ (Docker recommended)
- Node.js 22+ (for frontend development)
- Make

### Setup

```bash
# Install Go dependencies
go mod download

# Install frontend dependencies
cd frontend && npm install

# Start dev database
docker compose up -d postgres

# Run backend (make run defaults to :8091 to match the Vite dev proxy)
make run

# Run frontend (separate terminal)
cd frontend && npm run dev
```

> `make run` uses `HTTP_ADDR=:8091` and `DB_PORT=5437` by default so it matches
> the Vite dev proxy target (`frontend/vite.config.ts`) and the compose PG host
> port. Override the defaults as needed (e.g. `export DB_PORT=5432`).

### Testing

```bash
# All tests
make test

# Unit tests only (skip integration)
make test-short

# Frontend tests
cd frontend && npm test

# E2E / integration tests (requires PostgreSQL on :5437, canopyd,
# and the vite dev server on :5173)
cd frontend && npm run test:integration
```

### Deploying

The live service is the systemd user unit `canopy-canopyd.service`, which
executes `/home/kara/bin/canopyd` — a plain `make build` alone never updates
what systemd runs:

```bash
# Build from HEAD → atomic install → systemctl --user restart → /health poll
# → gateway surface smoke test (scripts/smoke-gateway.sh)
make deploy
```

#### Automated staleness detection (GAP-067)

A deployed binary can silently lag repo HEAD (the live 2026-09-02 artifact was
768,617s behind when this shipped). `scripts/check-deploy-staleness.sh`
compares the installed binary's mtime against the newest commit touching
`internal/`, `cmd/`, `migrations/`, `go.mod`, or `go.sum`:

| Exit | Meaning | Notes |
|------|---------|-------|
| `0` | `CURRENT` | artifact within threshold |
| `1` | `STALE` | over threshold, or binary missing |
| `2` | `STALE_BLOCKED` | stale + `--deploy`, but the worktree has tracked/untracked changes — auto-deploy refused so half-written worker code is never shipped |
| `3` | `ERROR` | operational failure (bad repo, unreadable paths, still stale after deploy) |

Overrides: `CANOPYD_STALE_REPO_ROOT`, `CANOPYD_STALE_PATH`
(default `/home/kara/bin/canopyd`), `CANOPYD_STALE_THRESHOLD_S`
(default `86400`), `CANOPYD_DEPLOY_CMD` (default `make deploy`),
`CANOPYD_DEPLOY_DIR`. `--deploy` only invokes the existing atomic
`make deploy` path when stale, then re-checks and fails if still stale —
manual `make deploy` behavior is unchanged.

#### Recovering from `STALE_BLOCKED`

Exit `2` is a deliberate refusal, not a failure: the deployed binary is stale
and the worktree has tracked/untracked changes, so auto-deploy is skipped —
deploying a half-written worker tree is worse than running one commit behind.
The operator path is to make the tree deployable again, then deploy:

```bash
git status --short                 # what is dirty?
git commit -am "…"                 # commit it…
git stash push -m "deploy-check"   # …or park it
make deploy                        # atomic install + restart + health + smoke
```

Waiting is also valid — the next hourly tick deploys by itself once the tree is
clean. Confirm afterwards with `bash scripts/check-deploy-staleness.sh`
(exit `0`, `CURRENT`).

#### Board alerts (GAP-069 / GAP-070)

The checker never leaves an outage-class condition to a raw exit code:

| Board event | Raised when | Threshold |
|-------------|-------------|-----------|
| `deploy_stale_alert` | every stale run (`STALE` / `STALE_BLOCKED`) | none — one event per stale run |
| `deploy_crashloop_alert` | the unit's `NRestarts` counter climbed between two runs | `CANOPYD_CRASHLOOP_THRESHOLD` (default `50`) |
| `deploy_stale_blocked_alert` | consecutive runs ended with a stale artifact left **un-remediated** — deploy refused on a dirty worktree, the deploy command failed, or the artifact was still stale after a deploy | `CANOPYD_STALE_BLOCKED_THRESHOLD` (default `2`) |

The stale-blocked alert is edge-triggered: it fires once as the streak crosses
the threshold (not once per hourly run, which would be a drumbeat) and re-arms
when a run comes back `CURRENT`. Its `detail` carries a `severity` field that
separates the two operator situations:

* `stale_but_serving` — the unit is up, so the stale binary is at least serving
  traffic. Degraded, fix it on your schedule.
* `stale_refused_to_start` — the unit is **not** up (or its state cannot be
  proven) while the artifact is stale. **Outage class**: this is the shape of
  the 2026-09-12 crash-loop, where a stale build met a newer schema and nothing
  watched the exits.

All three events are appended (never rewritten) to
`.coding-hermes/board/events.jsonl` by the checker itself; counters live in the
git-ignored `.coding-hermes/deploy-check-state.json`, so the checker can never
dirty the worktree its own gate then refuses.

Install the hourly systemd user timer (concrete unit names
`canopy-deploy-check.service` / `canopy-deploy-check.timer` — templates cannot
be enabled against `timers.target`; renders this checkout's path into
`~/.config/systemd/user`, `daemon-reload`, `enable --now`):

```bash
make install-deploy-timer
```

The timer runs the checker with `--deploy` once per **hour** (`OnBootSec=10min`,
`OnUnitActiveSec=1h`, `Persistent=true`, units tracked under
`deploy/systemd/`; GAP-070 moved this off `24h`, which left a refusal invisible
for a full day). It never deploys from a dirty worktree. Pre-flight
verification without touching the live session:

```bash
tmp=$(mktemp -d); CANOPYD_UNIT_INSTALL_DIR="$tmp" bash scripts/install-deploy-timer.sh
systemd-analyze --user verify "$tmp"/canopy-deploy-check.{service,timer}
systemctl --user enable --dry-run "$tmp/canopy-deploy-check.timer"   # rc 0 required
```

### Makefile Targets

| Target | Description |
|--------|-------------|
| `build` | Build the canopyd binary |
| `deploy` | Build, install to `/home/kara/bin/canopyd`, restart `canopy-canopyd`, run the gateway smoke test |
| `install-deploy-timer` | Install + enable the hourly deployed-binary staleness check timer (GAP-067) |
| `run` | Build and run with dev defaults (`:8091`, DB `:5437`) |
| `test` | Run all tests |
| `test-short` | Run tests (skip integration) |
| `test-chaos-disconnect` | QA chaos-disconnect probe — bounded window; runs the `test-short` suite (see `docs/E2E-EVIDENCE.md`) |
| `vet` | Run go vet |
| `lint` | Run golangci-lint |
| `clean` | Remove build artifacts |
| `docker` | Build Docker image |

### Code Quality

- **GitReins guards:** secrets scan, build, lint, tests on every commit
- **gitleaks:** secrets detection with spec/docs allowlist
- **Go vet:** zero-warning policy
- **golangci-lint:** configured in CI workflow
- **TypeScript:** strict mode, tsc --noEmit clean
- **Hilo:** dependency graph tracking for architecture drift detection. Its local cache lives in `.vfs/` (`.gitignore`d, fully rebuildable); regenerate the dependency graph after a fresh clone with `hilo graph warm`.

### Project Structure

```
├── cmd/canopyd/              — Entry point
│   ├── cli.go               — CLI command tree
│   └── main.go              — Server startup
├── deploy/                   — Deployment configs
│   ├── Dockerfile           — Multi-stage production build
│   └── grafana/             — Grafana dashboard config
├── docker-compose.yml       — Local development stack
├── frontend/                — React + TypeScript PWA
│   ├── src/
│   │   ├── components/      — React components
│   │   ├── pages/           — Route pages
│   │   ├── stores/          — State management (Yjs)
│   │   └── hooks/           — Custom hooks
│   └── tests/               — Playwright E2E tests
├── internal/
│   ├── card/                — Card subsystem (DuckDB)
│   ├── config/              — Configuration loading
│   ├── db/                  — Data layer
│   │   ├── migrations.go    — Embedded SQL migrations
│   │   ├── models.go        — Domain models
│   │   └── *_repo.go        — Repository implementations
│   ├── handler/             — HTTP handlers (REST API)
│   ├── hermes/              — Hermes integration
│   ├── mls/                 — MLS encryption service
│   ├── server/              — HTTP server
│   ├── service/             — Business logic layer
│   ├── sse/                 — SSE hub & connections
│   ├── sync/                — Sync engine
│   ├── telemetry/           — Prometheus metrics
│   ├── testutil/            — Test helpers
│   └── transport/           — Multi-transport adapters
├── migrations/              — SQL migration files
├── specs/                   — Architecture specifications
└── .vfs/                    — Local Hilo metadata (generated, rebuildable — not tracked; see Development)
```

## Configuration

### CLI

The `canopyd` binary doubles as an API client: the `tree` and `topic`
subcommands operate on a **running** canopyd over HTTP. Keep the two kinds of
settings apart — a client setting names the API to talk to, a server setting
configures a canopyd process:

| Setting | Kind | Meaning |
|---------|------|---------|
| `CANOPY_SERVER_URL` | client | API base URL the CLI talks to (default `http://localhost:8091`) |
| `CANOPY_TOKEN` | client | Bearer token sent with each request |
| `HTTP_ADDR` | server | listen address of a `canopyd serve` process — never a CLI destination |
| `DB_*`, `CANOPY_DB_URL` | server | PostgreSQL connection of a `canopyd serve` process — never a CLI destination |

The CLI enforces four rules (DF-HERMES-CANOPY-6):

1. An explicit `CANOPY_SERVER_URL` always wins — including while `DB_*` and
   `HTTP_ADDR` are set. That is the supported way to point the CLI at a scratch
   instance. It must be an absolute `http://` or `https://` URL with a host.
2. A malformed explicit value fails **before any request is sent**; the CLI never
   falls back to the default target.
3. With no `CANOPY_SERVER_URL` but any `HTTP_ADDR` / `DB_*` / `CANOPY_DB_URL` set,
   the CLI refuses to run and tells you to set `CANOPY_SERVER_URL`. `tree create`
   is a write and `tree list` reads a specific instance — guessing the destination
   is how a "scratch" run ends up writing into the live instance. `HTTP_ADDR` is a
   *listen* address, not an API URL, and `DB_PORT` says nothing about where the API
   is, so neither can be used to derive one.
4. With nothing configured, the historical default `http://localhost:8091` still
   applies. `-h` / `--help` never validates the target and never touches the network.

```bash
# Against the dev server from the Quick Start (API on :8091)
export CANOPY_SERVER_URL=http://localhost:8091
export CANOPY_TOKEN=your-jwt-token

./bin/canopyd tree create "My Tree" --content 'Hello from the CLI'
./bin/canopyd tree list
./bin/canopyd tree navigate <tree-id>
./bin/canopyd tree delete <tree-id>
```

```bash
# Scratch target: address a SECOND, already-running canopyd instead of :8091.
# Start it once (its own HTTP_ADDR and database), then give the CLI its URL.
HTTP_ADDR=:8092 DB_PORT=5437 DB_USER=canopy DB_PASSWORD=canopy \
  DB_NAME=canopy_scratch ./bin/canopyd serve &

CANOPY_SERVER_URL=http://localhost:8092 CANOPY_TOKEN=$TOKEN \
  ./bin/canopyd tree create "Scratch tree" --content 'write target check'
```

> Setting `CANOPY_SERVER_URL` selects **which running instance** the CLI talks to;
> it does not create, reset, or otherwise isolate that instance's data — start the
> scratch server/database you want to target yourself.

> `session import` and `session associations-backfill` are the exception: they run
> in-process against PostgreSQL (they read `DB_*` / `CANOPY_DB_URL` directly, plus
> `--db` for the Hermes `state.db`), so they are configured like a server rather
> than like an HTTP client.

## Monitoring

When `METRICS_ENABLED=true`, the `/metrics` endpoint exposes:

- `http_requests_total` — Request count by method, path, status
- `http_request_duration_seconds` — Request duration histogram
- `http_requests_in_flight` — Concurrent request gauge

Import `deploy/grafana/dashboard.json` into Grafana for a pre-built monitoring dashboard.

## Contributing

1. Read the specs in `specs/` directory
2. Check `.coding-hermes/board/tasks.jsonl` for active tasks
3. Every commit must include `Co-authored-by: Alexis Okuwa <wojonstech@gmail.com>`
4. Run `make test && make vet && make lint` before pushing
5. Never commit secrets, tokens, or passwords

## License

MIT License — see [LICENSE](LICENSE). Copyright (c) 2026 Alexis Okuwa and Hermes Canopy contributors.
