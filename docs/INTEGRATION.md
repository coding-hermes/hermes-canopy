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
> default 5432. If you run the backend outside Docker (see §4), you must set
> `DB_PORT=5437` or adjust the compose file.

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

# Run backend (connects to localhost:5432 by default):
make run
# or directly:
./bin/canopyd

# If using compose's PostgreSQL on port 5437:
DB_PORT=5437 ./bin/canopyd

# Custom address (use :8091 to match the Vite dev proxy target in §5):
HTTP_ADDR=:8091 ./bin/canopyd
```

### Health Check

```bash
curl http://localhost:8080/health
# → {"status":"ok","service":"canopyd"}

curl http://localhost:8080/healthz
# → {"status":"ok","service":"canopyd"}

curl http://localhost:8080/version
# → {"version":"dev"}
```

### CLI Subcommands

The binary also supports CLI subcommands for tree management:

```bash
export CANOPY_SERVER_URL=http://localhost:8080
export CANOPY_TOKEN=your-jwt-token

./bin/canopyd tree create "My Tree"
./bin/canopyd tree list
./bin/canopyd tree navigate <tree-id>
./bin/canopyd tree delete <tree-id>
```

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

## 6. API Walkthrough (curl)

All API endpoints are under `/api/v1` and require a JWT Bearer token. In
development, use the dev JWT from `vite.config.ts`:

```bash
DEV_JWT="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE4MTY0OTU5ODgsImlhdCI6MTc4NDk1OTk4OCwic3ViIjoiMDAwMDAwMDAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAxIn0.AeEXxMtrSsIeoqnuCf-8w8XMaVbB4qIP3oX3vgxXeMI"
AUTH="Authorization: Bearer $DEV_JWT"
BASE="http://localhost:8091"
```

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

```bash
NODE_ID="c9d0e1f2-..."

curl -s -X POST "$BASE/api/v1/nodes/$NODE_ID/fork" \
  -H "$AUTH" \
  -H "Content-Type: application/json" \
  -d '{"content":"Forked branch content","node_type":"message"}' | jq .
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
| `CANOPY_SERVER_URL`   | `http://localhost:8080`| CLI server URL (CLI only)               |
| `CANOPY_TOKEN`        | —                     | CLI auth token (CLI only)                |
