# Hermes Canopy

Canopy OS — graph-native collaboration surface for human-agent work.

Messages are nodes in a DAG. Every model call has a visible context manifest.  
Every Card is a graph node with structured data.

## Architecture

- **Backend:** Go (canopyd) — single binary, built-in HTTP server  
- **Frontend:** React + TypeScript + Vite — PWA with Service Worker  
- **Graph DB:** PostgreSQL (authoritative) + Yjs/IndexedDB (local replica)  
- **Card DB:** DuckDB in-process + JSONL files  
- **Transport:** SSE (server→client) + HTTP POST (client→server)  

## Quick Start

```bash
# Prerequisites
go 1.24+
PostgreSQL 17+

# Build
make build

# Run (with local PostgreSQL)
DB_HOST=localhost DB_PORT=5432 DB_USER=canopy DB_PASSWORD=canopy DB_NAME=canopy \
  ./bin/canopyd

# Run tests
make test-short
```

## Makefile Targets

| Target | Description |
|--------|-------------|
| `build` | Build the canopyd binary |
| `build-embed` | Build with version injection |
| `test` | Run all tests |
| `test-short` | Run tests (skip integration) |
| `vet` | Run go vet |
| `lint` | Run golangci-lint |
| `clean` | Remove build artifacts |

## Configuration

All configuration is via environment variables (see `.env.example`):

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `canopy` | Database user |
| `DB_PASSWORD` | `canopy` | Database password |
| `DB_NAME` | `canopy` | Database name |
| `HTTP_ADDR` | `:8080` | HTTP listen address |
| `LOG_LEVEL` | `info` | Log level |

## Project Structure

```
cmd/canopyd/         — Entry point
internal/
├── config/          — Configuration loading
├── db/              — Data layer (models, repos, migrations)
├── server/          — HTTP server
└── transport/       — Transport adapters
migrations/          — SQL migrations (embedded)
deploy/              — Deployment configs
```

## License

Proprietary — Total Windup Flight Systems
