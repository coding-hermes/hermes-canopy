# Live Evidence — 2026-10-29

This evidence was gathered against the hermes-canopy dev stack running on port **:8092** (docker-compose) and port **:8091** (standalone binary).

## Environment

- **PostgreSQL**: `canopy-pg` container (postgres:16-alpine), running 9 days, healthy, host port 5437→5432
- **Canopy server**: `hermes-canopy-canopyd` container via docker-compose, running 9 days, healthy, host port 8092→8080
- **Binary**: `./bin/canopyd` — API-only build (no embedded UI; built without `-tags embedui`)

## 1. Dev Stack — Root Path

### docker-compose stack (port 8092)

```
$ curl -s -o /dev/null -w '%{http_code}' http://localhost:8092/
404
```

The compose binary is API-only (built without `-tags embedui`), so the root path returns 404. The health endpoint works:

```
$ curl -s -o /dev/null -w '%{http_code}' http://localhost:8092/health
200
```

### Standalone binary (port 8091)

Built standalone `./bin/canopyd` with `HTTP_ADDR=:8091`:

```
$ curl -s -o /dev/null -w '%{http_code}' http://localhost:8091/
404
```

Same result — API-only binary, no embedded SPA served. To serve the UI, run:
```
make build-ui  # builds SPA + binary with -tags embedui
```

### FINDING: The compose binary is API-only

The docker-compose stack serves canopyd without the embedded frontend. The `404` on `/` is expected for this binary version. The health endpoint returns `200`, confirming the API is healthy.

## 2. Negative Auth Test

```
$ curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:8092/api/v1/workspaces
401
```

The server correctly rejects unauthenticated requests with HTTP 401.

## 3. Five Latency Samples

All against docker-compose stack on `http://localhost:8092`:

| # | Route | Method | HTTP Code | Time (s) |
|---|-------|--------|-----------|----------|
| 1 | `/health` | GET | 200 | 0.000984 |
| 2 | `/api/v1/workspaces` | GET | 401 | 0.000826 |
| 3 | `/api/v1/trees` | GET | 401 | 0.000459 |
| 4 | `/api/v1/nodes` | GET | 401 | 0.000528 |
| 5 | `/api/v1/cards` | GET | 401 | 0.000497 |

**Observation**: All endpoints respond in under 1ms (sub-millisecond). Auth-required endpoints return 401 consistently. The `/health` endpoint is the only unprotected route returning 200.

## 4. Routes Not Exercised

The following routes were deliberately **not** tested:

- `POST /api/v1/trees` — requires valid JWT for creation
- `POST /api/v1/nodes` — requires auth
- `POST /api/v1/topics` — requires auth
- `POST /api/v1/cards` — requires auth
- `POST /api/v1/workspaces` — tested with no auth (got 401); a valid-auth version would need a JWT token
- `GET /api/v1/graph/subtree` — subtree graph queries
- `GET /api/v1/graph/ancestors` — ancestor queries
- Plugin sandbox endpoints (`/api/v1/plugins/*`)
- MLS group endpoints (`/api/v1/workspaces/*/mls`)
- SSE/transport endpoints (`/api/v1/sse/*`)

## Summary

- **Backend**: Healthy, responding in <1ms across all tested routes
- **Auth**: Correctly enforced (401 on all API routes without token)
- **UI**: Not served by compose binary — requires `make build-ui` for embedded SPA
- **No Playwright test was executed** — browser evidence is deferred to a future review pass
