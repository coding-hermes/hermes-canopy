# Hermes Canopy

Canopy OS — graph-native collaboration surface for human-agent work.

Messages are nodes in a DAG. Every model call has a visible context manifest.
Every Card is a graph node with structured data.

## Quick Start

```bash
# Prerequisites
go 1.25+
PostgreSQL 16+
Node.js 22+ (for frontend development)

# Clone and build
git clone https://github.com/totalwindupflightsystems/hermes-canopy.git
cd hermes-canopy
make build

# Start PostgreSQL (Docker)
# Host port 5437 matches docker-compose.yml (which maps 5437:5432) so the same
# DB_PORT works whether you use the standalone container or `docker compose up`.
docker run -d --name canopy-pg \
  -e POSTGRES_USER=canopy -e POSTGRES_PASSWORD=canopy \
  -e POSTGRES_DB=canopy -p 5437:5432 postgres:16

# Run (dev: backend on :8091 to match the Vite dev proxy target)
DB_HOST=localhost DB_PORT=5437 DB_USER=canopy DB_PASSWORD=canopy DB_NAME=canopy \
  HTTP_ADDR=:8091 ./bin/canopyd

# Frontend (dev mode)
cd frontend
npm install
npm run dev

# Open the frontend
open http://localhost:5173  # dev mode
# or http://localhost:8080  # production (embedded in binary, default HTTP_ADDR)
```

## Architecture

### Backend (canopyd)

```
┌─────────────────────────────────────────────────────┐
│                   HTTP Server (:8080)                │
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

### Trees

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/trees` | List all trees |
| `POST` | `/api/v1/trees` | Create a tree |
| `GET` | `/api/v1/trees/{id}` | Get tree details |
| `PATCH` | `/api/v1/trees/{id}` | Update tree metadata |
| `DELETE` | `/api/v1/trees/{id}` | Soft-delete a tree |

### Nodes

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/nodes` | List nodes (filtered by tree_id, parent_id) |
| `POST` | `/api/v1/nodes` | Create a node |
| `GET` | `/api/v1/nodes/{id}` | Get node details |
| `PATCH` | `/api/v1/nodes/{id}` | Update node content |
| `DELETE` | `/api/v1/nodes/{id}` | Soft-delete a node |
| `POST` | `/api/v1/nodes/{id}/fork` | Fork a node (create child branch) |

### Edges

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/edges` | List edges (filtered by tree_id) |
| `POST` | `/api/v1/edges` | Create an edge |
| `DELETE` | `/api/v1/edges/{id}` | Delete an edge |

### Graph

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/graph/trees/{tree_id}/subtree/{node_id}` | Get subtree (all descendants) |
| `GET` | `/api/v1/graph/trees/{tree_id}/ancestors/{node_id}` | Get ancestor chain |
| `GET` | `/api/v1/graph/trees/{tree_id}/stats` | Graph statistics |

### Topics

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/topics` | List topics |
| `POST` | `/api/v1/topics` | Create a topic |
| `GET` | `/api/v1/topics/{id}` | Get topic details |
| `PATCH` | `/api/v1/topics/{id}` | Update topic |
| `DELETE` | `/api/v1/topics/{id}` | Delete a topic |

### Cards

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/cards` | List cards |
| `POST` | `/api/v1/cards` | Create a card |
| `GET` | `/api/v1/cards/{id}` | Get card details |
| `PATCH` | `/api/v1/cards/{id}` | Update card |
| `DELETE` | `/api/v1/cards/{id}` | Delete a card |

### Approvals

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/approvals` | List approvals (pending/history) |
| `POST` | `/api/v1/approvals` | Create an approval request |
| `GET` | `/api/v1/approvals/{id}` | Get approval details |
| `POST` | `/api/v1/approvals/{id}/approve` | Approve |
| `POST` | `/api/v1/approvals/{id}/deny` | Deny |

### SSE (Server-Sent Events)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/events` | SSE event stream |
| `GET` | `/api/v1/events?tree_id={id}` | Filter by tree |

### Health

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check |
| `GET` | `/metrics` | Prometheus metrics (if METRICS_ENABLED=true) |

## Deployment

### Docker (Recommended)

```bash
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
HTTP_ADDR=:8080 \
DB_HOST=your-pg-host DB_PORT=5432 \
DB_USER=canopy DB_PASSWORD=$(cat /etc/secrets/db-password) \
DB_NAME=canopy \
LOG_LEVEL=warn \
METRICS_ENABLED=true \
  ./bin/canopyd
```

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

# Run backend
make run

# Run frontend (separate terminal)
cd frontend && npm run dev
```

### Testing

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

### Makefile Targets

| Target | Description |
|--------|-------------|
| `build` | Build the canopyd binary |
| `run` | Build and run with development defaults |
| `test` | Run all tests |
| `test-short` | Run tests (skip integration) |
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
- **Hilo:** dependency graph tracking for architecture drift detection

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
└── .vfs/                    — Hilo dependency graph
```

## Configuration

### CLI

```bash
# Interactive CLI (hermes canopy subcommand)
# Requires CANOPY_SERVER_URL and CANOPY_TOKEN env vars
export CANOPY_SERVER_URL=http://localhost:8080
export CANOPY_TOKEN=your-jwt-token

./bin/canopyd tree create "My Tree"
./bin/canopyd tree list
./bin/canopyd tree navigate <tree-id>
./bin/canopyd tree delete <tree-id>
```

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
