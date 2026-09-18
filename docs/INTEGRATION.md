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
PostgreSQL and the canopyd server.

The `.env` file is optional — the compose stack starts without it — but copying
the template is recommended so the Hermes gateway key (`API_SERVER_KEY`) is
picked up:

```bash
# Optional but recommended: copy the env template (API_SERVER_KEY etc.)
cp .env.example .env
```

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
- Port: **8092** (host) → 8080 (container) — 8091 is reserved for the host
  systemd `canopy-canopyd.service` primary instance; compose is the
  containerized alternative and must not fight it for the port
- Connects to postgres via `CANOPY_DB_URL=postgres://canopy:canopy@postgres:5432/canopy?sslmode=disable`
- Waits for postgres health check before starting
- Metrics enabled by default (`METRICS_ENABLED=true`)
- Built from `deploy/Dockerfile`
- Gateway integration (GAP-050): reaches the Hermes api_server via
  `HERMES_WEBUI_GATEWAY_BASE_URL=http://host.docker.internal:8642`
  (`extra_hosts: host-gateway`), with the key supplied by `.env`
  (`API_SERVER_KEY`, gitignored). NOTE: this only works if the Hermes
  api_server listens on a non-loopback interface (e.g.
  `HERMES_API_SERVER_HOST=0.0.0.0`); bound to `127.0.0.1` the container
  cannot reach it and `/api/v1/gateway/status` reports `connected:false`.

> **Important:** The compose file maps PostgreSQL to host port **5437**, not the
> default 5432. `make run` already defaults to `DB_PORT=5437`; if you run the raw
> binary outside Docker (see §4), set `DB_PORT=5437` or adjust the compose file.

## 3. Database Migrations

Migrations are **embedded in the canopyd binary** at compile time. The SQL files
live in `migrations/` and are compiled into the binary via Go's `embed` package
(see `migrations/embed.go`). There are 32 numbered migration pairs (up/down).

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

The CLI is an **HTTP client** of an already-running canopyd: `CANOPY_SERVER_URL`
is the only setting that selects its destination. `HTTP_ADDR` (a listen address)
and `DB_*` / `CANOPY_DB_URL` (PostgreSQL settings) configure a *server* process and
never redirect the CLI — with any of them set and no `CANOPY_SERVER_URL`, the CLI
fails before sending a request rather than guessing. An explicit
`CANOPY_SERVER_URL` always wins (that is the supported isolation path), and a
malformed one also fails before any request. `session import` /
`session associations-backfill` are the exception: they run in-process against
PostgreSQL and do use `DB_*` / `CANOPY_DB_URL`.

> **Isolating a scratch instance.** A distinct `CANOPY_SERVER_URL` only picks a
> destination — it does not isolate anything. A second instance needs its own
> database, API port, `HOME` (card store + gateway run registry — or the
> per-store overrides `CANOPY_CARD_DATA_DIR` / `CANOPY_GATEWAY_STATE_FILE`) and
> `CANOPY_FILE_ROOT`; and do not reuse `:8091` (native/live), `:8092` (compose) or
> `:8080` (raw default). Runnable recipe:
> [SCRATCH_INSTANCE.md](SCRATCH_INSTANCE.md) / `scripts/scratch-instance.sh`.

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
- **Proxy target (DEV ONLY):** `VITE_API_URL` env var, defaults to `http://localhost:8091`.
  This variable is read by `frontend/vite.config.ts` for the **Vite dev server
  proxy only** — a production build ignores it (the build-time equivalent is
  `VITE_API_BASE_URL`, see §5 Production Build).
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
REST/SSE API on `HTTP_ADDR` (raw binary default `:8080`; `make run` uses `:8091`;
the compose stack publishes `:8092`) and does **not** embed or serve
the frontend. The PWA must be served separately, by a **same-origin reverse
proxy** — a plain SPA-mode static server answers `/api/v1/*` with `index.html`,
and the app then dies on `JSON.parse("<!doctype html>")`:

```bash
# Build (from the repo root: frontend/dist, NOT frontend/frontend/dist)
cd frontend && npm run build && cd ..

# Option A — shipped reference proxy (python3 stdlib only, dev-grade/single-user)
python3 deploy/reference-proxy.py --dist frontend/dist --port 3000 \
    --api http://127.0.0.1:8091
# Option B — your own nginx / Caddy: copy deploy/nginx.canopy.conf or
#            deploy/Caddyfile as the starting point.
```

Three things the server in front of `dist/` must do (all three configs above do
them): (1) SPA fallback for **app routes only** (`/trees`, `/tree/<id>` — the PWA
uses `BrowserRouter`, so deep links must return `index.html`, not 404) while
`/api/` and `/health` are **never** answered with `index.html`; (2) stream `/api`
responses unbuffered (nginx `proxy_buffering off`, Caddy `flush_interval -1`,
reference proxy chunked relay) so SSE event streams arrive incrementally; (3) pass
the client's `Authorization` header through (token injection only behind explicit
authentication — see below). The example port `:3000` is not reserved; choose a
free one (the reference proxy refuses a busy port).

The deployed PWA talks to the API on its **own origin** through that proxy, so
the build needs no API base URL at all. The build-time variable is
`VITE_API_BASE_URL` (`frontend/src/lib/api.ts`) — `VITE_API_URL` is only the Vite
**dev** proxy target (§5 Configuration above) and does nothing in a production
build. The Docker deployment builds `frontend/dist` as a release artifact in the
image builder stage for exactly this purpose (deploy/Dockerfile).

**Production auth — single-user in MVP.** The proxy boundary above has no idea about
tokens — it only relays `Authorization`. Since there is **no** `/api/v1/auth/*`
endpoint, and the Vite dev proxy (the only JWT injector today) is gone in
production, a deployed PWA authenticates in one of two ways:

1. **The browser carries the token.** Build with `VITE_API_TOKEN=<jwt>`, or paste
   a token into the browser's `localStorage['canopy.token']`. Every call site in
   `frontend/src` then sends `Authorization: Bearer <jwt>` — REST via the shared
   helpers in `frontend/src/lib/api.ts` (`apiUrl` + `authInit`/`authHeaders`,
   which send no header at all when the token is missing/blank), and the SSE
   feeds via `frontend/src/lib/sse.ts` (see "SSE streams" below).
2. **The proxy injects the token** — `deploy/reference-proxy.py --token <jwt>`
   (with `--require-auth-user/--require-auth-password`) or the commented
   `auth_request` / `basicauth` blocks in `deploy/nginx.canopy.conf` /
   `deploy/Caddyfile`. Only behind explicit authentication: an injected JWT on an
   open route is a credential handout. The reference proxy refuses to start if
   `--token` is combined with a non-loopback bind and no Basic auth.

**SSE streams.** `GET /api/v1/trees/{id}/events`,
`GET /api/v1/workspace/channels/{id}/feed`,
`GET /api/v1/gateway/runs/{id}/events` and `GET /api/v1/plugins/{id}/events` are
auth-gated by the same middleware, which accepts the bearer token from the
`Authorization` header only. Native `EventSource` cannot set headers, so in a
static build it can never authenticate. `frontend/src/lib/sse.ts` therefore
streams with `fetch` (adding the header) whenever a token resolves, and falls
back to native `EventSource` when none does — the `vite dev` case, where the dev
proxy injects the JWT. So: with `VITE_API_TOKEN`/`localStorage['canopy.token']`
the feeds work; with a header-injecting proxy they also work; either way they
are **not** public. `/health`, `/healthz` and `/version` are the only public
paths (`isPublicPath()`).

The file-stream hand-off is no longer an exception: `resolveStreamUrl(fileId)`
(`frontend/src/lib/fileApi.ts`) keeps the bare `GET /api/v1/files/{id}/stream`
URL only when **no** token resolves (the `vite dev` case, where the dev proxy
injects the JWT). When a token does resolve, the viewer host fetches the bytes
through the authenticated path first and hands the DOM a `blob:` object URL
instead — for `<a href download>`, the sandboxed iframe's
`canopy.__bootstrap.streamUrl` and `viewer.get_stream_url` alike — revoking it
when the file changes or the host unmounts. So those loads carry the bearer
token in `VITE_API_TOKEN` / `localStorage` builds without a proxy.

Mint the token out-of-band (mint one with `JWT_SECRET` — §6 below), and treat the
whole deployment as single-user; multi-user auth is deferred post-MVP.

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

### File Viewers (SPEC-PL-02): upload → resolve → stream → access → dispatch

The complete file-viewer walkthrough, runnable on a **fresh database**. Start
canopyd with the dev defaults (§ 4) — with the default `JWT_SECRET` it
provisions the dev user, the dev workspace
(`00000000-0000-0000-0000-000000000010`), the `dev-hermes` profile that the
`/files` routes act as, and the active `profile_route` mapping on boot:

```
INF dev JWT user provisioned user_id=00000000-0000-0000-0000-000000000001
INF dev workspace + profile provisioned owner_id=00000000-0000-0000-0000-000000000001 profile_name=dev-hermes workspace_id=00000000-0000-0000-0000-000000000010
INF file viewer storage ready file_root=/home/kara/.canopy/files
```

Without that provisioning every `/files` call below answers
`404 PROFILE_NOT_FOUND`, and `POST /api/v1/workspaces/{ws}/profiles` fails with
a foreign-key error surfaced as `500 INTERNAL_ERROR` (GAP-071). Reuse the
`DEV_JWT` / `AUTH` / `BASE` variables from the top of § 6.

**1. List files (empty on a fresh DB):**

```bash
curl -s "$BASE/api/v1/files" -H "$AUTH" | jq .
```
```json
{ "files": [], "pagination": { "count": 0 } }
```

**2. Upload a file** (multipart `file` part; `filename`/`declaredMime` are
optional — they fall back to the part's filename and `Content-Type`):

```bash
printf 'Hello from the walkthrough.\nSecond line.\n' > /tmp/note.md
UPLOAD=$(curl -s -X POST "$BASE/api/v1/files/upload" -H "$AUTH" \
  -F "file=@/tmp/note.md" -F "filename=note.md" -F "declaredMime=text/markdown")
echo "$UPLOAD" | jq .
FILE_ID=$(echo "$UPLOAD" | jq -r .file.id)
SHA=$(echo "$UPLOAD" | jq -r .file.sha256)
```

Response (201):
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

`sha256` is the content address and `metadata` is base64 (`"e30="` = `{}`).
Re-uploading the same bytes for the same profile is a no-op that bumps
`referenceCount` and answers `"was_deduped": true`.

**3. Resolve by hash reference** (`profile_id` omitted = the acting profile):

```bash
curl -s -X POST "$BASE/api/v1/files/resolve" -H "$AUTH" \
  -H "Content-Type: application/json" \
  -d "{\"hash_ref\":{\"sha256\":\"$SHA\"}}" | jq .
# → same ResolveFileOutput with "was_new_upload": false, "was_deduped": false
```

Batch form (JSON **array**, up to 200 entries):

```bash
curl -s -X POST "$BASE/api/v1/files/resolve/batch" -H "$AUTH" \
  -H "Content-Type: application/json" \
  -d "[{\"hash_ref\":{\"sha256\":\"$SHA\"}}]" | jq .
```

**4. Stream the bytes** — full body:

```bash
curl -s -D - "$BASE/api/v1/files/$FILE_ID/stream" -H "$AUTH"
```
```
HTTP/1.1 200 OK
Accept-Ranges: bytes
Cache-Control: private, max-age=300
Content-Disposition: inline
Content-Length: 49
Content-Type: text/markdown
Etag: "bdbeddd49e5cc697816366c3af527da048c9abcd263946e9bc3b04aa02592a5c"
X-Viewer-Hint: markdown

Hello from the walkthrough.
Second line.
```

Same route with a Range header — `206 Partial Content`:

```bash
curl -s -D - -H "Range: bytes=0-4" "$BASE/api/v1/files/$FILE_ID/stream" -H "$AUTH"
```
```
HTTP/1.1 206 Partial Content
Accept-Ranges: bytes
Content-Disposition: inline
Content-Length: 5
Content-Range: bytes 0-4/49
Content-Type: text/markdown
Etag: "bdbeddd49e5cc697816366c3af527da048c9abcd263946e9bc3b04aa02592a5c"
X-Viewer-Hint: markdown

Hello
```

`ETag` is the file's `sha256`, so a matching `If-None-Match` returns
`304 Not Modified` with no body.

**5. Append an access-log entry** (`action` and `viewer_slug` are required):

```bash
curl -s -X POST "$BASE/api/v1/files/$FILE_ID/access" -H "$AUTH" \
  -H "Content-Type: application/json" \
  -d '{"action":"open","viewer_slug":"code"}' | jq .
```
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

The table is append-only (UPDATE/DELETE are rejected by a trigger):

```bash
curl -s "$BASE/api/v1/files/$FILE_ID/access" -H "$AUTH" | jq .
```

**6. Recents** — logging the access above is what makes the file show up here:

```bash
curl -s "$BASE/api/v1/files/recents" -H "$AUTH" | jq .
```
```json
[
  {
    "id": "01a0a32e-63f5-7666-ac1d-a809a24ee800",
    "profileId": "01a0a32e-62a8-751b-bf9a-271bb6fbdc00",
    "sha256": "bdbeddd49e5cc697816366c3af527da048c9abcd263946e9bc3b04aa02592a5c",
    "byteSize": 49,
    "mimeType": "text/markdown",
    "filename": "note.md",
    "extension": "md",
    "storageKind": "hermes_kb",
    "isText": true,
    "isViewable": true,
    "viewerHint": "markdown",
    "referenceCount": 1,
    "lastAccessedAt": "2026-09-14T22:48:41.685445-05:00",
    "createdAt": "2026-09-14T22:48:41.589474-05:00"
  }
]
```

**7. Viewer registry:**

```bash
curl -s "$BASE/api/v1/viewers" -H "$AUTH" | jq -r '.[].viewerSlug'
# audio_video
# code
# csv
# image
# json
# markdown
# pdf

curl -s "$BASE/api/v1/viewers/code" -H "$AUTH" | jq .
```
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

**8. Dispatch** — which viewer renders this file?

```bash
curl -s -X POST "$BASE/api/v1/viewers/dispatch" -H "$AUTH" \
  -H "Content-Type: application/json" -d "{\"file_id\":\"$FILE_ID\"}" | jq .
```
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

**9. Workspace profile mapping** — on a fresh DB this used to fail (`500`);
the dev workspace is provisioned, so it returns 200 and can be read back:

```bash
DEV_WS=00000000-0000-0000-0000-000000000010
curl -s -X POST "$BASE/api/v1/workspaces/$DEV_WS/profiles" -H "$AUTH" \
  -H "Content-Type: application/json" \
  -d '{"profile_name":"dev-hermes","profile_token":"hprof_dev_token"}' | jq .
curl -s "$BASE/api/v1/workspaces/$DEV_WS/profiles/active" -H "$AUTH" | jq .
```
```json
{
  "workspaceId": "00000000-0000-0000-0000-000000000010",
  "profileName": "dev-hermes",
  "displayName": "Dev Hermes",
  "isActive": true,
  "mappedAt": "2026-09-14T22:48:41.253186-05:00",
  "lastUsedAt": "2026-09-14T22:48:41.784555-05:00"
}
```

An unknown workspace id is a **404**, not a 500:

```bash
curl -s -w '\nHTTP %{http_code}\n' -X POST "$BASE/api/v1/workspaces/11111111-2222-3333-4444-555555555555/profiles" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"profile_name":"dev-hermes","profile_token":"hprof_dev_token"}'
```
```json
{"error":{"code":"WORKSPACE_NOT_FOUND","message":"workspace 11111111-2222-3333-4444-555555555555 does not exist; create it before setting a profile"}}
```

**10. Soft-delete the file:**

```bash
curl -s -X DELETE "$BASE/api/v1/files/$FILE_ID" -H "$AUTH" -w '\nHTTP %{http_code}\n'
# {}
# HTTP 200
curl -s "$BASE/api/v1/files/$FILE_ID" -H "$AUTH" -w '\nHTTP %{http_code}\n'
# {"error":{"code":"FILE_NOT_FOUND_BY_ID","message":"file not found"}} — HTTP 404
```

The delete is a soft delete (`deleted_at` is set), but every read path filters
`deleted_at IS NULL`, so the file reads back as `404 FILE_NOT_FOUND_BY_ID` (not
the `410 FILE_SOFT_DELETED` the handler switch declares — that branch has no
producer today; see docs/API.md § Spec-vs-Code Drift). `GET .../stream` answers
the same 404, and resolving the hash afterwards gives
`404 FILE_NOT_FOUND_BY_HASH` because the unique `(profile_id, sha256)` index
only covers live rows.

Full route/field reference (all 13 routes, error codes, base64 notes):
[API.md](API.md) § File Viewers.

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

# E2E / integration tests (requires PostgreSQL on :5437, canopyd,
# and the vite dev server on :5173)
cd frontend && npm run test:integration
```

### 8.1 Deployed-binary staleness check (GAP-067)

The deployed artifact (`/home/kara/bin/canopyd`) can silently lag repo HEAD.
Hermetic regression coverage lives in
`scripts/test-check-deploy-staleness.sh` (synthetic git repo + fake deploy
command; never touches the real service):

```bash
bash scripts/test-check-deploy-staleness.sh   # 45 assertions, exit 0 = pass
```

Covered controls: current artifact → 0; stale → 1; missing → 1; stale +
`--deploy` (clean repo) → fake deploy invoked then CURRENT; dirty worktree +
`--deploy` → STALE_BLOCKED (2), deploy never invoked; untracked-only dirt →
also blocked; no-op deploy that leaves the artifact stale → 3.

**Crash-loop alert (GAP-069).** Every staleness run also watches the service's
restart counter: if `systemctl --user show canopy-canopyd -p NRestarts` climbed
by `CANOPYD_CRASHLOOP_THRESHOLD` (default 50) since the previous run, the
checker appends a `deploy_crashloop_alert` event to
`.coding-hermes/board/events.jsonl` — so a canopyd that crash-loops for *any*
reason (not only a stale binary) becomes visible within one timer interval,
even when the artifact is current. The baseline lives in the local, git-ignored
`.coding-hermes/deploy-check-state.json` and is rewritten every run; a missing
or corrupt file simply re-baselines with no alert. This path never changes the
checker's exit code.

**Stale-blocked alert (GAP-070).** A `STALE_BLOCKED` refusal is the right call —
and a silent one for 24h was the 2026-09-13 failure. The checker now counts
CONSECUTIVE runs that ended with a stale artifact left *un-remediated* (deploy
refused on a dirty worktree, the deploy command failed, or the artifact was
still stale after a deploy) in the same state file, and appends ONE
`deploy_stale_blocked_alert` event as that streak crosses
`CANOPYD_STALE_BLOCKED_THRESHOLD` (default 2). The alert is edge-triggered (one
event per blocked streak, re-armed when a run returns `CURRENT`) and its detail
carries a `severity` split — `stale_but_serving` when the unit is up,
`stale_refused_to_start` (outage class) when it is not. The state read/write is
a read-modify-write shared with the GAP-069 restart baseline, and like the
crash-loop path it never changes an exit code.

Install the automation (hourly timer, opt-in, concrete unit names —
`canopy-deploy-check.timer` enables cleanly against `timers.target`, template
units would not; refuses dirty worktrees). Pre-flight without touching the
live session: render to a temp dir, then `systemd-analyze --user verify` and
`systemctl --user enable --dry-run` the rendered concrete timer (must exit 0).

```bash
make install-deploy-timer
```

### 8.2 E2E Stack Prep (required once per fresh local PG)

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

# Start canopyd against the canonical DB — :8091, the port the probe below targets:
DB_HOST=localhost DB_PORT=5437 DB_USER=canopy DB_PASSWORD=canopy \
HTTP_ADDR=:8091 DB_NAME=canopy ./bin/canopyd   # or: DB_PORT=5437 make run
```

**Green probe:** `curl -s -X POST http://localhost:8091/api/v1/trees \
-H 'Authorization: Bearer <dev-jwt>' -H 'Content-Type: application/json' \
-d '{"title":"E2E Probe","description":"","rootMessage":{"content":"hi","contentFormat":"markdown","nodeType":"message"}}'`
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
| `VITE_API_URL`        | `http://localhost:8091`| Vite **DEV** proxy target (`vite.config.ts` only; ignored by production builds) |
| `VITE_DEV_JWT`        | (hardcoded dev token) | Dev JWT injected by the Vite **dev** proxy (frontend only) |
| `VITE_API_BASE_URL`   | — (relative `/api/v1`) | Frontend API base URL read at **build** time (`frontend/src/lib/api.ts`); leave unset behind a same-origin reverse proxy |
| `VITE_API_TOKEN`      | — (unset)             | Bearer token baked into a **production build**; honoured by every call site (REST via `lib/api.ts`, SSE via `lib/sse.ts`), with `localStorage['canopy.token']` as the runtime fallback |
| `CANOPY_SERVER_URL`   | `http://localhost:8091`| CLI API base URL (CLI only). An explicit value wins over `HTTP_ADDR`/`DB_*`; with those set and no URL the CLI fails before any request (§4) |
| `CANOPY_TOKEN`        | —                     | CLI auth token (CLI only)                |
