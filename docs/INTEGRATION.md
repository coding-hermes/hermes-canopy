# Canopy Integration Guide

This guide covers everything you need to run and use the Canopy backend + frontend
for local development. Canopy is a graph-native collaboration surface for human-agent
work — messages are nodes in a DAG, every model call has a visible context manifest,
and every Card is a graph node with structured data.

## 1. Prerequisites

| Tool       | Minimum Version | Notes                                    |
|------------|-----------------|------------------------------------------|
| Go         | 1.25+           | `go version` to check                    |
| Node.js    | 22+             | `node --version` to check                |
| PostgreSQL | 16+             | Docker/Podman recommended for dev        |
| Make       | any             | `make --version` to check                |

## 2. Docker Compose (Quick Start)

The project ships a `docker-compose.yml` at the repo root that starts both
PostgreSQL and the canopyd server:

```bash
docker compose up -d
```

This starts two services:

**postgres** (container name: `canopy-pg`)
- Image: `postgres:16-alpine`
- Port: **5437** (host) → 5432 (container) — note the non-standard host port!
- Credentials: `canopy` / `canopy` / `canopy` (user / password / database)
- Health check: `pg_isready -U canopy` (3s interval, 15 retries)
- Persistent volume: `pgdata` at `/var/lib/postgresql/data`

**canopyd** (container name: `canopy-server`)
- Port: **8091** (host) → 8080 (container)
- Connects to postgres via `CANOPY_DB_URL=postgres://canopy:canopy@postgres:5432/canopy?sslmode=disable`
- Waits for postgres health check before starting
- Metrics enabled by default (`METRICS_ENABLED=true`)
- Built from `deploy/Dockerfile`

> **Important:** The compose file maps PostgreSQL to host port **5437**, not the
> default 5432. `make run` already defaults to `DB_PORT=5437`; if you run the raw
> binary outside Docker (see §4), set `DB_PORT=5437` or adjust the compose file.

## 3. Database Migrations

Migrations are **embedded in the canopyd binary** at compile time. The SQL files
live in `migrations/` and are compiled into the binary via Go's `embed` package
(see `migrations/embed.go`). There are 24 numbered migration pairs (up/down).

**How they run:** On every startup, `main.go` calls `database.Migrate(ctx)` which
uses `golang-migrate` to apply any pending up-migrations automatically. The
process is idempotent — if the database is already at the latest version, nothing
happens. No sidecar migration tool or manual step is required.

The migration source is embedded as an `iofs` filesystem, so the binary is
self-contained — no `migrations/` directory needed at runtime.

## 4. Backend Dev Server

### Build

```bash
make build
# Produces: bin/canopyd
```

Or build with version injection:

```bash
make build-embed
```

### Configuration

The backend is configured via environment variables. Defaults are defined in
`internal/config/config.go`:

| Variable         | Default            | Description                        |
|------------------|--------------------|------------------------------------|
| `HTTP_ADDR`      | `:8080`            | HTTP listen address                |
| `DB_HOST`        | `localhost`        | PostgreSQL host                    |
| `DB_PORT`        | `5432`             | PostgreSQL port                    |
| `DB_USER`        | `canopy`           | Database user                      |
| `DB_PASSWORD`    | `canopy`           | Database password                  |
| `DB_NAME`        | `canopy`           | Database name                      |
| `DB_SSLMODE`     | `disable`          | PostgreSQL SSL mode                |
| `JWT_SECRET`     | `dev-secret-change-me` | HS256 JWT signing secret       |
| `LOG_LEVEL`      | `info`             | Log level (debug/info/warn/error)  |
| `METRICS_ENABLED`| `false`            | Enable Prometheus on `/metrics`    |
| `CORS_ORIGIN`    | `*`                | CORS allowed origins (in code)     |

**Connection string override:** Set `CANOPY_DB_URL` to a full
`postgres://user:pass@host:port/dbname?sslmode=...` DSN. When set, it overrides
all individual `DB_*` variables.

### Run

```bash
# Using Docker PostgreSQL (from compose):
docker compose up -d postgres

# Run backend with dev defaults (make run sets HTTP_ADDR=:8091, DB_PORT=5437 —
# matching the Vite dev proxy target in §5 and the compose PostgreSQL host port):
make run
# or run the raw binary directly (raw binary defaults: :8080 / :5432):
./bin/canopyd

# If using compose's PostgreSQL on port 5437:
DB_PORT=5437 ./bin/canopyd
```

### Health Check

```bash
curl http://localhost:8091/health
# → {"status":"ok","service":"canopyd"}

curl http://localhost:8091/healthz
# → {"status":"ok","service":"canopyd"}

curl http://localhost:8091/version
# → {"version":"dev"}
```

### CLI Subcommands

The binary supports CLI subcommands for tree management plus an explicit
`serve` subcommand:

```bash
export CANOPY_SERVER_URL=http://localhost:8091
export CANOPY_TOKEN=your-jwt-token

./bin/canopyd serve              # start the API server (default mode; env-only config)
./bin/canopyd serve --help       # print usage and exit — does NOT start the server
./bin/canopyd tree create "My Tree" --content 'Hello from the CLI'
./bin/canopyd tree list
./bin/canopyd tree navigate <tree-id>
./bin/canopyd tree delete <tree-id>
```

Server configuration is **environment-only** — there are no server flags
(only `-version`). See the "Environment Variables" table in the README and
§4 above. `canopyd serve --help` lists the key variables.

## 5. Frontend Dev Server

### Install

```bash
cd frontend
npm install
```

### Configuration

The Vite dev server is configured in `frontend/vite.config.ts`. Key details:

- **Dev port:** `:5173` (Vite default)
- **API proxy:** All `/api` requests are proxied to the backend
- **Proxy target:** `VITE_API_URL` env var, defaults to `http://localhost:8091`
- **Dev JWT:** A pre-generated HS256 JWT is injected into every proxied request
  via the `Authorization` header. The token is set by `VITE_DEV_JWT` env var,
  falling back to a hardcoded dev token in `vite.config.ts`.
- **Dev JWT details:**
  - Secret: `dev-secret-change-me` (matches the backend's default `JWT_SECRET`)
  - Subject (user ID): `00000000-0000-0000-0000-000000000001`
  - Algorithm: HS256
  - Expiry: 365-day rolling window

### Run

```bash
cd frontend
npm run dev
# → http://localhost:5173
```

The proxy auto-injects the dev JWT, so you don't need to manually authenticate
during development. The frontend connects to the backend through the Vite proxy
at `/api` → `http://localhost:8091` (or your `VITE_API_URL`).

### Production Build

```bash
cd frontend
npm run build
# Produces: frontend/dist/ (static files)
```

**Serving the PWA:** `canopyd` is **API-only** in MVP — the binary serves the
REST/SSE API on `HTTP_ADDR` (raw binary default `:8080`; `make run` and
compose use `:8091`) and does **not** embed or serve
the frontend. The PWA must be served separately:

```bash
# Option A — any static file server
npx serve -l 3000 frontend/dist

# Option B — nginx / Caddy / your CDN pointed at frontend/dist/
```

The deployed PWA talks to the API through a reverse proxy or by setting the
API base URL at build time (`VITE_API_URL`, see §5 Configuration above). The
Docker deployment builds `frontend/dist` as a release artifact in the image
builder stage for exactly this purpose (deploy/Dockerfile).

## 6. API Walkthrough (curl)

All API endpoints are under `/api/v1` and require a JWT Bearer token. In
development, mint a fresh dev JWT at run time with the one-liner from
[README.md](../README.md) §"Authentication (dev mode)" → "Direct API access
(curl, scripts, etc.)":

```bash
# Mint a fresh 24h dev JWT (HS256, secret = default JWT_SECRET
# "dev-secret-change-me", sub = 00000000-0000-0000-0000-000000000001).
# See also: README.md §"Authentication (dev mode)".
DEV_JWT=$(node -e "
const crypto = require('crypto');
const header = Buffer.from(JSON.stringify({alg:'HS256',typ:'JWT'})).toString('base64url');
const payload = Buffer.from(JSON.stringify({
  sub:'00000000-0000-0000-0000-000000000001',
  iat:Math.floor(Date.now()/1000),
  exp:Math.floor(Date.now()/1000)+86400
})).toString('base64url');
const sig = crypto.createHmac('sha256','dev-secret-change-me').update(header+'.'+payload).digest('base64url');
console.log(header+'.'+payload+'.'+sig);
")
AUTH="Authorization: Bearer $DEV_JWT"
BASE="http://localhost:8091"
```

**Expiry note:** dev JWTs are HS256-signed with the default secret
`dev-secret-change-me`, subject `00000000-0000-0000-0000-000000000001`, and
they expire (24h here; the frontend's fallback token uses a 365-day window).
If any curl below returns `401 Unauthorized`, your token has expired — just
re-run the `node -e` mint command above to get a fresh one. Never embed a
long-lived static token in documentation.

### Create a Tree

```bash
curl -s -X POST "$BASE/api/v1/trees" \
  -H "$AUTH" \
  -H "Content-Type: application/json" \
  -d '{"title":"My First Tree","description":"A test tree","rootMessage":{"content":"Welcome to my first tree","contentFormat":"markdown","nodeType":"message"}}' | jq .
```

Response (201 Created):
```json
{
  "id": "a1b2c3d4-...",
  "title": "My First Tree",
  "description": "A test tree",
  "root_node_id": "e5f6a7b8-...",
  "owner_id": "00000000-0000-0000-0000-000000000001",
  "created_at": "2026-08-04T12:00:00Z"
}
```

Note the `root_node_id` — a root node is automatically created when the tree
is created. Save the tree ID and root node ID for subsequent steps.

### List Trees

```bash
curl -s "$BASE/api/v1/trees" -H "$AUTH" | jq .
```

Response (200 OK):
```json
{
  "trees": [
    {
      "id": "a1b2c3d4-...",
      "title": "My First Tree",
      "created_at": "2026-08-04T12:00:00Z"
    }
  ],
  "pagination": {
    "nextCursor": null,
    "hasMore": false,
    "total": 1,
    "limit": 50
  }
}
```

### Get Tree Details

```bash
curl -s "$BASE/api/v1/trees/a1b2c3d4-..." -H "$AUTH" | jq .
```

### Create a Child Node

```bash
TREE_ID="a1b2c3d4-..."
ROOT_ID="e5f6a7b8-..."

curl -s -X POST "$BASE/api/v1/trees/$TREE_ID/nodes" \
  -H "$AUTH" \
  -H "Content-Type: application/json" \
  -d "{\"parent_id\":\"$ROOT_ID\",\"content\":\"Hello from the child node!\",\"node_type\":\"message\"}" | jq .
```

Response (201 Created):
```json
{
  "node": {
    "id": "c9d0e1f2-...",
    "tree_id": "a1b2c3d4-...",
    "parent_id": "e5f6a7b8-...",
    "content": "Hello from the child node!",
    "content_format": "markdown",
    "node_type": "message",
    "author_id": "00000000-0000-0000-0000-000000000001",
    "sequence_num": 2,
    "created_at": "2026-08-04T12:01:00Z"
  },
  "edge": {
    "id": "f3a4b5c6-...",
    "source_id": "e5f6a7b8-...",
    "target_id": "c9d0e1f2-...",
    "edge_type": "reply"
  }
}
```

### List Nodes in a Tree

```bash
curl -s "$BASE/api/v1/trees/$TREE_ID/nodes" -H "$AUTH" | jq .
```

Response (200 OK):
```json
{
  "nodes": [
    {
      "id": "e5f6a7b8-...",
      "tree_id": "a1b2c3d4-...",
      "content": "",
      "node_type": "root",
      "author_id": "00000000-0000-0000-0000-000000000001",
      "sequence_num": 1,
      "created_at": "2026-08-04T12:00:00Z"
    },
    {
      "id": "c9d0e1f2-...",
      "tree_id": "a1b2c3d4-...",
      "parent_id": "e5f6a7b8-...",
      "content": "Hello from the child node!",
      "content_format": "markdown",
      "node_type": "message",
      "author_id": "00000000-0000-0000-0000-000000000001",
      "sequence_num": 2,
      "created_at": "2026-08-04T12:01:00Z"
    }
  ]
}
```

### Get Subtree (Graph Traversal)

```bash
curl -s "$BASE/api/v1/graph/trees/$TREE_ID/subtree/$ROOT_ID" -H "$AUTH" | jq .
```

### Fork a Node

Forking creates an alternative branch from a node that already has at least one
child. Forking a leaf (a message with no replies yet) is rejected with a
`400 VALIDATION_ERROR` — "fork requires parent with at least one child" —
because a leaf fork would be indistinguishable from a reply (SPEC-API-03 §7.3);
the UI surfaces this rule when you try to branch a message with no replies.
In this walkthrough the root node (ROOT_ID) has the child node created
above, so fork from the root:

```bash
NODE_ID="$ROOT_ID"

curl -s -X POST "$BASE/api/v1/trees/$TREE_ID/nodes/$NODE_ID/fork" \
  -H "$AUTH" \
  -H "Content-Type: application/json" \
  -d '{"content":"Forked branch content","node_type":"message"}' | jq .
```

### Topics

Topics are named, searchable subgraphs anchored to a node within a tree,
mounted at `/api/v1/topics` (see docs/API.md §Topics for the full contract).
The request body uses camelCase keys; responses use snake_case.

Create a topic (here anchored to the tree's root node from earlier steps):

```bash
curl -s -X POST "$BASE/api/v1/topics" \
  -H "$AUTH" \
  -H "Content-Type: application/json" \
  -d "{\"treeId\":\"$TREE_ID\",\"rootNodeId\":\"$ROOT_ID\",\"title\":\"My First Topic\",\"description\":\"Optional description\"}" | jq .
```

Response (201 Created):

```json
{
  "id": "b4c5d6e7-...",
  "tree_id": "a1b2c3d4-...",
  "root_node_id": "e5f6a7b8-...",
  "title": "My First Topic",
  "description": "Optional description",
  "slug": "my-first-topic",
  "status": "active",
  "node_count": 1,
  "created_at": "2026-08-04T12:02:00Z"
}
```

List topics (`tree_id` is required; `status`, `limit`, and `offset` are
optional):

```bash
curl -s "$BASE/api/v1/topics?tree_id=$TREE_ID&status=active&limit=50&offset=0" -H "$AUTH" | jq .
```

Response (200 OK):

```json
{
  "topics": [
    {
      "id": "b4c5d6e7-...",
      "tree_id": "a1b2c3d4-...",
      "root_node_id": "e5f6a7b8-...",
      "title": "My First Topic",
      "description": "Optional description",
      "slug": "my-first-topic",
      "status": "active",
      "node_count": 1,
      "created_at": "2026-08-04T12:02:00Z"
    }
  ]
}
```

## 7. Frontend Dev Workflow

1. Start PostgreSQL: `docker compose up -d postgres`
2. Start the backend: `./bin/canopyd` (or `make run`)
3. Start the frontend: `cd frontend && npm run dev`
4. Open `http://localhost:5173` in your browser

The frontend is a React + TypeScript PWA with:
- **React Router** for page navigation
- **React Flow** for graph visualization
- **Yjs** for CRDT-based local replicas of tree data
- **SSE** for real-time updates from the backend
- **IndexedDB** (via y-indexeddb) for offline persistence

The Vite dev server proxies `/api` requests to the backend and auto-injects the
dev JWT, so you can start using the UI immediately without manual authentication.

**What you should see:**
- A tree list page (or empty state if no trees exist)
- Clicking a tree opens the graph view
- You can create nodes, reply, fork, and navigate the DAG
- Real-time updates via SSE when other clients make changes

## 8. Testing

```bash
# All tests
make test

# Unit tests only (skip integration)
make test-short

# Frontend tests
cd frontend && npm test

# E2E tests (requires running server)
cd frontend && npx playwright test

# Integration tests (requires PG on :5437)
cd frontend && npx vitest run --config vitest.integration.config.ts
```

### 8.1 E2E Stack Prep (required once per fresh local PG)

The E2E / visual-regression suites and the dev UI's tree-create flows need two
things that a fresh local database does not have:

1. **The canonical E2E database is the compose PostgreSQL on host port 5437**
   (`docker compose up -d postgres`), NOT a local `localhost:5432` instance.
   The compose PG carries the seeded demo tree ("UI-02 Rail Demo") that the
   visual-regression goldens depend on; an empty local DB on :5432 will make
   tree creation 503 (`tree_members` FK violation, wrapped as
   "database unavailable") and mockup captures will drift.

2. **The dev JWT user must exist.** The Vite proxy auto-injects a dev JWT with
   `sub=00000000-0000-0000-0000-000000000001`; every write path inserts a
   `tree_members` row for that actor, so the `users` row must be seeded:

```bash
docker compose up -d postgres   # canonical E2E DB on :5437
PGPASSWORD=canopy psql -h localhost -p 5437 -U canopy -d canopy -c \
  "INSERT INTO users (id, hermes_user_id, email, display_name, is_active)
   VALUES ('00000000-0000-0000-0000-000000000001','dev','dev@canopy.dev','Dev User',true)
   ON CONFLICT (id) DO NOTHING;"

# Start canopyd against the canonical DB:
DB_HOST=localhost DB_PORT=5437 DB_USER=canopy DB_PASSWORD=canopy \
DB_NAME=canopy ./bin/canopyd   # or: DB_PORT=5437 make run
```

**Green probe:** `curl -s -X POST http://localhost:8091/api/v1/trees \
-H 'Authorization: Bearer <dev-jwt>' -H 'Content-Type: application/json' \
-d '{"title":"E2E Probe","description":"","root_message":{"content":"hi","content_format":"markdown","node_type":"message"}}'`
returns HTTP 201 (not 503).

## 9. Environment Variable Reference

| Variable              | Default               | Description                              |
|-----------------------|-----------------------|------------------------------------------|
| `HTTP_ADDR`           | `:8080`               | Backend HTTP listen address              |
| `DB_HOST`             | `localhost`           | PostgreSQL host                          |
| `DB_PORT`             | `5432`                | PostgreSQL port                          |
| `DB_USER`             | `canopy`              | Database user                            |
| `DB_PASSWORD`         | `canopy`              | Database password                        |
| `DB_NAME`             | `canopy`              | Database name                            |
| `DB_SSLMODE`          | `disable`             | PostgreSQL SSL mode                      |
| `CANOPY_DB_URL`       | —                     | Full DSN override (overrides DB_* vars)  |
| `JWT_SECRET`          | `dev-secret-change-me`| HS256 JWT signing secret                 |
| `LOG_LEVEL`           | `info`                | Log level                                |
| `METRICS_ENABLED`     | `false`               | Enable Prometheus metrics on `/metrics`  |
| `VITE_API_URL`        | `http://localhost:8091`| Frontend proxy target (frontend only)   |
| `VITE_DEV_JWT`        | (hardcoded dev token) | Dev JWT for proxy auth (frontend only)   |
| `CANOPY_SERVER_URL`   | `http://localhost:8091`| CLI server URL (CLI only)               |
| `CANOPY_TOKEN`        | —                     | CLI auth token (CLI only)                |
