# BE-12b: API-Level Integration Tests (Tree, Node, Edge CRUD via HTTP + DB)

## Goal

Write integration tests that exercise the **HTTP API layer** against a real PostgreSQL database. Tests should start the full server stack (or a test server with real DB), issue HTTP requests, and verify end-to-end CRUD operations.

## Where to Put Tests

Create `internal/handler/integration_test.go` — a new test file in the handler package. The testutil package at `internal/testutil/` provides helpers:

- `SkipIfNoDB(t)` — skips test if no DB available
- `NewIntegrationPool(t)` — returns a pgxpool.Pool connected to the test DB
- `TruncateAll(t, pool)` — truncates all tables between tests
- `DefaultTestDBURL` — postgres://canopy:canopy@localhost:5437/canopy?sslmode=disable

## Server Wiring

Use `httptest.NewServer` with the handler's chi router. The server needs real service implementations wired to the test DB pool. Create a helper function `newTestServer(t, pool)` that:

1. Creates real repo instances from `internal/db/` using the pool
2. Creates real service instances from `internal/service/` using those repos
3. Creates SSE hub, sync engine (can be `sync.NoopEngine` for most tests)
4. Creates auth middleware with a known JWT secret
5. Mounts routes like the real server does
6. Returns `(*httptest.Server, func())` for cleanup

**Key insight**: use `handler.AuthMiddleware` with a hardcoded secret, generate a JWT for test users, and pass it as `Authorization: Bearer <token>`.

## What to Test

### Tree CRUD

| Endpoint | Method | What to verify |
|----------|--------|----------------|
| `/api/v1/trees` | POST | Creates tree, returns 201 with Location header |
| `/api/v1/trees` | GET | Lists trees, returns 200 with paginated response |
| `/api/v1/trees/{id}` | GET | Returns single tree, 200 |
| `/api/v1/trees/{id}` | PATCH | Updates tree, returns 200 |
| `/api/v1/trees/{id}` | DELETE | Soft-deletes tree, returns 204 |
| `/api/v1/trees/bad-uuid` | GET | Invalid UUID → 400 or 404 |

### Node CRUD

| Endpoint | Method | What to verify |
|----------|--------|----------------|
| `POST /trees/{tree_id}/nodes` | POST | Creates node under tree, returns 201 |
| `GET /trees/{tree_id}/nodes/{node_id}` | GET | Returns node by ID, 200 |
| `PATCH /nodes/{node_id}` | PATCH | Updates node content, 200 |
| `DELETE /nodes/{node_id}` | DELETE | Soft-deletes node, 204 |
| `POST /nodes/{node_id}/reply` | POST | Creates child node (reply), 201 |
| `POST /nodes/{node_id}/fork` | POST | Creates fork node, 201 |

### Edge Operations

Check the edge handler/service for available endpoints. If edge routes exist, test:
- Create edge connecting two nodes
- List edges for a tree
- Delete edge

### Error Cases

- Missing auth token → 401
- Invalid JSON body → 400 with INVALID_BODY error code
- Non-existent tree/node → appropriate error status
- Membership check for non-member user on tree-scoped endpoints

## Test Structure

```go
package handler

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"
    
    "github.com/golang-jwt/jwt/v5"
    "github.com/google/uuid"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/go-chi/chi/v5"
    
    "github.com/totalwindupflightsystems/hermes-canopy/internal/db"
    "github.com/totalwindupflightsystems/hermes-canopy/internal/service"
    "github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
    "github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
    "github.com/totalwindupflightsystems/hermes-canopy/internal/sync"
)

// TestBE12_TreeCRUD tests full tree CRUD lifecycle via HTTP API.
func TestBE12_TreeCRUD(t *testing.T) {
    testutil.SkipIfNoDB(t)
    pool := testutil.NewIntegrationPool(t)
    defer pool.Close()
    testutil.TruncateAll(t, pool)
    
    // Wire up services ... 
    // Start test server ...
    // Test POST /api/v1/trees → 201
    // Test GET /api/v1/trees → 200
    // Test GET /api/v1/trees/{id} → 200
    // Test PATCH /api/v1/trees/{id} → 200
    // Test DELETE /api/v1/trees/{id} → 204
}

// TestBE12_NodeCRUD tests full node CRUD lifecycle via HTTP API.
func TestBE12_NodeCRUD(t *testing.T) { ... }

// TestBE12_EdgeCRUD tests edge lifecycle via HTTP API.
func TestBE12_EdgeCRUD(t *testing.T) { ... }

// TestBE12_AuthRejection tests that unauthenticated requests are rejected.
func TestBE12_AuthRejection(t *testing.T) { ... }

// TestBE12_ValidationErrors tests invalid inputs return proper error codes.
func TestBE12_ValidationErrors(t *testing.T) { ... }
```

## What to Use / Import

- `net/http/httptest` for test server
- `github.com/golang-jwt/jwt/v5` for generating test JWT tokens
- `github.com/jackc/pgx/v5/pgxpool` for DB pool
- Existing `internal/db` repos: `NewTreeRepo`, `NewNodeRepo`, `NewEdgeRepo`, `NewUserRepo` etc.
- Existing `internal/service` services: `NewTreeService`, `NewNodeService` etc.
- `internal/sse` for SSE hub (can use `sse.NewHub()`)
- `internal/sync` for sync engine (can use `sync.NewNoopEngine()`)
- Follow patterns from `internal/handler/auth_test.go`

## Instructions for the Worker

1. Run `docker compose up -d` first to ensure PostgreSQL is running
2. Wire the test server with real repos + services + handlers
3. Write clean, focused test functions — one per API group
4. Ensure tests pass with `go test ./internal/handler/ -run TestBE12 -v -count=1`
5. Don't modify existing production code — only add new test files
6. Use `testutil.TruncateAll(t, pool)` between test cases to ensure isolation
7. Generate JWT tokens with `jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": userID.String()})` and sign with the same secret as the server
