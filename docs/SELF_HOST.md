# Self-Hosting Hermes Canopy

A practical guide to deploying and operating Canopy OS on your own infrastructure.

Canopy is a graph-native collaboration surface for human-agent work. It ships as a single Go binary (`canopyd`) that bundles the HTTP API server, SSE event hub, Prometheus metrics, and database migrations — all in one process backed by PostgreSQL. The React PWA frontend is **served separately** (see [Quick Start](#quick-start)).

---

## Table of Contents

1. [Quick Start](#quick-start)
2. [Prerequisites](#prerequisites)
3. [Installation](#installation)
4. [Configuration](#configuration)
5. [PostgreSQL Setup](#postgresql-setup)
6. [TLS / HTTPS](#tls--https)
7. [Backup & Restore](#backup--restore)
8. [Monitoring](#monitoring)
9. [Upgrading](#upgrading)
10. [Troubleshooting](#troubleshooting)

---

## Quick Start

Download the latest binary for your platform (or build from source — see [Installation](#installation)), set four environment variables, and run:

```bash
# 1. Start PostgreSQL (Docker, one-liner)
docker run -d --name canopy-pg \
  -e POSTGRES_USER=canopy -e POSTGRES_PASSWORD=canopy \
  -e POSTGRES_DB=canopy -p 5432:5432 \
  postgres:16-alpine

# 2. Wait for PG to be ready
until docker exec canopy-pg pg_isready -U canopy; do sleep 1; done

# 3. Run canopyd (API on :8091 — matches the rest of the docs ecosystem)
DB_HOST=localhost DB_USER=canopy DB_PASSWORD=canopy DB_NAME=canopy \
  JWT_SECRET=$(openssl rand -base64 32) \
  HTTP_ADDR=:8091 \
  ./canopyd

# 4. Verify
curl http://localhost:8091/health
# → {"status":"ok"}
```

The server auto-runs database migrations on startup, so the schema is created automatically the first time it connects.

**Serving the frontend:** `canopyd` is **API-only** in MVP — it does not embed or serve the PWA. The React frontend must be served separately, and in production it must be served by a **same-origin reverse proxy** — a plain static server does *not* work:

```bash
# Development: Vite dev server (proxies /api to :8091 by default)
cd frontend && npm install && npm run dev
# → http://localhost:5173

# Production: build, then serve dist/ through a same-origin reverse proxy
cd frontend && npm ci && npm run build    # produces frontend/dist/ (NOT frontend/frontend/dist)
cd ..                                     # run the proxy from the repo root
python3 deploy/reference-proxy.py --dist frontend/dist --port 3000 \
    --api http://127.0.0.1:8091
# → http://localhost:3000
```

**Why not `npx serve -s` (or any other SPA-mode static server).** The SPA fallback
those servers enable answers *every* unknown path with `index.html` — including
`GET /api/v1/trees`. The app then tries to `JSON.parse("<!doctype html>")` and
fails with `Unexpected token '<', "<!doctype "... is not valid JSON`, while the UI
reports "Backend: unreachable". The `BrowserRouter` fallback is only needed for
**app routes** (`/trees`, `/tree/<id>` — so deep links survive a refresh);
`/api/` and `/health` must always be reverse-proxied to canopyd. `:3000` is a
convention, not a reserved port — it is often already taken, and
`deploy/reference-proxy.py` refuses to start on a busy port, so pick a free one.

For a real deployment use one of the shipped reference configs, which implement
the same split plus SSE-safe streaming (no response buffering):

| Config | Notes |
|--------|-------|
| `deploy/reference-proxy.py` | python3 stdlib only, no dependencies — dev-grade, single-user |
| `deploy/nginx.canopy.conf` | `proxy_buffering off`, long read timeout for `/api/` event streams |
| `deploy/Caddyfile` | `flush_interval -1`, `read_timeout 1h` |

**Pointing the PWA at the API.** The frontend reads `VITE_API_BASE_URL`
(`frontend/src/lib/api.ts`) — `VITE_API_URL` is **only** the Vite **dev** proxy
target (`frontend/vite.config.ts`). For the same-origin proxy above, leave
`VITE_API_BASE_URL` unset: the app then calls the relative `/api/v1`. See
docs/INTEGRATION.md §5 for details.

**Production auth (single-user).** There is no `/api/v1/auth/*` endpoint and the
Vite **dev** proxy is the only thing that injects a JWT today, so a static build
authenticates either by carrying the token itself (`VITE_API_TOKEN=<jwt>` at build
time, or `localStorage['canopy.token']` in the browser) or by having the reverse
proxy inject `Authorization: Bearer <jwt>` **behind explicit authentication**
(`--require-auth-user/--require-auth-password` for the reference proxy; the
commented `auth_request`/`basicauth` blocks in the nginx/Caddy configs). Mint the
token yourself with `JWT_SECRET` — see [README §Authentication](../README.md#authentication-dev-mode).

---

## Prerequisites

### Runtime

| Component    | Minimum        | Notes                                        |
|-------------|----------------|----------------------------------------------|
| PostgreSQL  | **16**+        | docker-compose uses `postgres:16-alpine`     |
| TLS cert    | —              | Recommended for production (see [TLS](#tls--https)) |

### Build-time (only if building from source)

| Component    | Minimum        | Notes                                        |
|-------------|----------------|----------------------------------------------|
| Go          | **1.25**+      | go.mod requires go 1.25.0               |
| Node.js     | **22**+        | Frontend build (Vite + React + TypeScript)  |
| Make        | any            | Wraps `go build`, `go test`, etc.           |

> **Note:** The Docker build (`deploy/Dockerfile`) uses a 3-stage process and requires no local toolchain beyond Docker itself.

---

## Installation

### Option 1: Pre-built Binary

Download the binary for your platform from the releases page, make it executable, and place it on your `$PATH`:

```bash
# Example — adjust arch/os as needed
curl -L -o canopyd https://github.com/coding-hermes/hermes-canopy/releases/latest/download/canopyd_linux_amd64
chmod +x canopyd
sudo mv canopyd /usr/local/bin/

# Verify
canopyd -version
```

### Option 2: Docker Compose

The repository includes a `docker-compose.yml` that runs `canopyd` + PostgreSQL together:

```bash
git clone https://github.com/coding-hermes/hermes-canopy.git
cd hermes-canopy

# Build the API image from THIS checkout, then start it (production-ready).
# The image carries the migrations embedded at build time, so rebuild after a
# pull or a schema change: `docker compose up -d` alone can reuse a cached image
# built from older HEAD, and that stale binary refuses to start with `STALE BUILD`.
docker compose build canopyd
docker compose up -d

# View logs
docker compose logs -f canopyd

# Verify (compose publishes the API on :8092 — see below)
curl http://localhost:8092/health
```

This starts:
- **canopyd** on host port `8092` (container `:8080`) — `:8091` is reserved for
  the host systemd `canopy-canopyd.service` primary instance, and `:8080` is the
  raw binary's default. The compose stack is the containerized alternative and
  must not fight the host instance for its port.
- **PostgreSQL 16** on host port `5437` (container `:5432`)
- Health-gated startup — canopyd waits for PG to pass `pg_isready` before starting

> **Isolating a second stack:** `docker compose -p <name>` does **not** isolate
> this file — the container names (`canopy-server`, `canopy-pg`), the published
> host port and the `pgdata` volume are hard-coded. For a throwaway instance
> beside the live one, use the native-binary recipe in
> [docs/SCRATCH_INSTANCE.md](SCRATCH_INSTANCE.md).

### Option 3: Build from Source

```bash
git clone https://github.com/coding-hermes/hermes-canopy.git
cd hermes-canopy

# Build the binary
make build
# → bin/canopyd

# Build with version injection
make build-embed
# → bin/canopyd (version from git tag)

# Cross-compile for a target platform
make build-embed-linux-amd64     # Linux x86_64
make build-embed-linux-arm64     # Linux ARM64
make build-embed-darwin-arm64    # macOS Apple Silicon
```

Cross-compilation targets available in the Makefile:

| Target                        | Output                              |
|-------------------------------|-------------------------------------|
| `build-embed-linux-amd64`     | `bin/canopyd_linux_amd64`          |
| `build-embed-linux-arm64`     | `bin/canopyd_linux_arm64`          |
| `build-embed-darwin-amd64`    | `bin/canopyd_darwin_amd64`         |
| `build-embed-darwin-arm64`    | `bin/canopyd_darwin_arm64`         |
| `build-embed-windows-amd64`   | `bin/canopyd_windows_amd64.exe`    |

---

## Configuration

All configuration is done through environment variables. There is no config file.

### Required

| Variable       | Default      | Description                              |
|---------------|--------------|------------------------------------------|
| `DB_HOST`     | `localhost`  | PostgreSQL hostname or IP                |
| `DB_PORT`     | `5432`       | PostgreSQL port                          |
| `DB_USER`     | `canopy`     | Database user                            |
| `DB_PASSWORD` | `canopy`     | Database password                        |
| `DB_NAME`     | `canopy`     | Database name                            |

### Recommended for Production

| Variable          | Default                    | Description                                |
|-------------------|----------------------------|--------------------------------------------|
| `JWT_SECRET`      | `dev-secret-change-me`     | **Change this.** Used to sign auth tokens. Generate with `openssl rand -base64 32`. |
| `DB_SSLMODE`      | `disable`                  | Set to `require` or `verify-full` for managed PG. |
| `METRICS_ENABLED` | `false`                    | Set to `true` to expose `/metrics` for Prometheus. |

### Optional

| Variable       | Default  | Description                                           |
|---------------|----------|-------------------------------------------------------|
| `HTTP_ADDR`   | `:8080`  | HTTP listen address. Use `127.0.0.1:8080` to bind localhost only, or `0.0.0.0:8080` for all interfaces. |
| `LOG_LEVEL`   | `info`   | Log verbosity: `debug`, `info`, `warn`, `error`.      |

### CLI-Only Variables

The `canopyd` binary doubles as a CLI client for remote servers. These are only needed when running `canopyd tree ...` commands against a remote instance:

| Variable            | Default                      | Description                            |
|---------------------|------------------------------|----------------------------------------|
| `CANOPY_SERVER_URL` | `http://localhost:8091`      | Base URL of the Canopy API server. The quick-start / native flows serve the API on host port `8091`; the compose stack publishes `8092` (container `:8080`) — set `CANOPY_SERVER_URL=http://localhost:8092` for CLI commands against compose. The CLI is an HTTP client: `HTTP_ADDR` and `DB_*` configure a *server* process and never redirect the CLI — with any of them set and no `CANOPY_SERVER_URL`, the CLI exits nonzero before sending a request instead of guessing a destination. |
| `CANOPY_TOKEN`      | *(none)*                     | Bearer token for authenticated requests. Without it, the CLI sends requests without auth (dev mode). |

### Example: Full Production Config

```bash
export DB_HOST=postgres.internal
export DB_PORT=5432
export DB_USER=canopy
export DB_PASSWORD=$(cat /etc/secrets/canopy-db-password)
export DB_NAME=canopy
export DB_SSLMODE=require
export JWT_SECRET=$(cat /etc/secrets/canopy-jwt-secret)
export HTTP_ADDR=127.0.0.1:8091
export LOG_LEVEL=warn
export METRICS_ENABLED=true

./canopyd
```

---

## PostgreSQL Setup

### Creating the Database

Canopy needs a dedicated PostgreSQL database. The migrations run automatically on startup, so you only need to create the database and a user:

```sql
-- Connect as a superuser
CREATE USER canopy WITH PASSWORD 'your-strong-password';
CREATE DATABASE canopy OWNER canopy;
GRANT ALL PRIVILEGES ON DATABASE canopy TO canopy;
```

If using a managed PostgreSQL service (RDS, Cloud SQL, etc.), create the database and user through their console or CLI, then set the `DB_*` environment variables accordingly.

### Migrations

Migrations are **embedded in the binary** and run automatically when `canopyd` starts:

```
2026-07-27T10:00:00Z INF canopyd starting version=v0.1.0 http_addr=:8091 db_host=localhost
2026-07-27T10:00:00Z INF running database migrations...
2026-07-27T10:00:01Z INF migrations complete (20 applied)
2026-07-27T10:00:01Z INF HTTP server listening addr=:8091
```

There is nothing you need to do — no migration CLI, no manual step. If the schema is already current, the migration step is a no-op with no downtime.

The migration files live in `migrations/` in the repo (40 SQL files — 20 up + 20 down). The binary embeds them at compile time via `iofs`, so the version you're running always contains the correct schema for that version.

### Connection String Format

The binary builds a connection string from individual `DB_*` env vars:

```
postgres://<DB_USER>:<DB_PASSWORD>@<DB_HOST>:<DB_PORT>/<DB_NAME>?sslmode=<DB_SSLMODE>
```

If you're using the Docker Compose setup, the `CANOPY_DB_URL` variable is a full-DSN override accepted by the binary (see `internal/config/config.go`):

```
postgres://canopy:canopy@postgres:5432/canopy?sslmode=disable
```

---

## TLS / HTTPS

`canopyd` itself serves **plain HTTP only**. For production HTTPS, run it behind a reverse proxy.

### nginx

```nginx
server {
    listen 443 ssl http2;
    server_name canopy.example.com;

    ssl_certificate     /etc/letsencrypt/live/canopy.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/canopy.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8091;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # SSE requires unbuffered streaming
        proxy_buffering off;
        proxy_read_timeout 86400s;  # 24h for long-lived SSE connections
        chunked_transfer_encoding on;
    }
}

# Redirect HTTP → HTTPS
server {
    listen 80;
    server_name canopy.example.com;
    return 301 https://$host$request_uri;
}
```

### Reverse-proxy client IP (CANOPY_TRUSTED_PROXIES)

Behind the nginx setup above, the TCP peer address canopyd sees is the proxy
itself (e.g. `127.0.0.1`), so by default every visitor would share a single
rate-limit bucket. To fix that, tell canopyd which proxies it may trust:

```bash
# Comma-separated CIDR prefixes of your reverse proxy / proxy chain.
CANOPY_TRUSTED_PROXIES=127.0.0.1/32
```

When set, canopyd resolves the real client from the `X-Forwarded-For` header
(`proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` above), but
only trusts entries added by proxies within those CIDRs — anything else is
treated as untrusted and ignored. The resolved client IP is used for
rate-limit bucketing; it never overwrites the connection peer address.

When `CANOPY_TRUSTED_PROXIES` is **unset** (the default), `X-Forwarded-For`
is **ignored entirely** (fail-closed) and rate limiting keys on the peer
address. This is deliberate: chi v5.3.2 deprecated its old `middleware.RealIP`
because it trusted spoofable `X-Forwarded-For` / `True-Client-IP` / `X-Real-IP`
headers from anyone, letting clients evade or poison rate limiting
(see GHSA-3fxj-6jh8-hvhx, GHSA-rjr7-jggh-pgcp, GHSA-9g5q-2w5x-hmxf). Entries
must be valid CIDRs — an invalid value fails loudly at startup.

### Caddy (Simplest)

Caddy auto-provisions Let's Encrypt certificates with zero configuration:

```
canopy.example.com {
    reverse_proxy 127.0.0.1:8091
}
```

That's it. Caddy handles TLS, renewal, and HTTP→HTTPS redirects automatically.

### Let's Encrypt

With nginx, use certbot:

```bash
# Install certbot (Ubuntu/Debian)
sudo apt install certbot python3-certbot-nginx

# Obtain and auto-configure nginx
sudo certbot --nginx -d canopy.example.com

# Renewal happens automatically via systemd timer; verify with:
sudo certbot renew --dry-run
```

### SSE Considerations

Canopy uses Server-Sent Events (SSE) for real-time sync. Ensure your reverse proxy:

- **Disables response buffering** (`proxy_buffering off` in nginx, Caddy does this by default)
- **Sets a long read timeout** (SSE connections stay open indefinitely)
- **Passes the `Host` header** (CORS and PWA scope depend on it)

---

## Backup & Restore

### Backup

Use `pg_dump` for logical backups:

```bash
# Full database backup
pg_dump \
  -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME \
  --no-owner --no-acl \
  -Fc -f canopy_$(date +%Y%m%d_%H%M%S).dump
```

The `-Fc` flag produces a compressed custom-format archive that can be restored with `pg_restore`.

**Recommended schedule:**
- **Daily** full backup, retain 7 days
- **Weekly** backup, retain 4 weeks
- **Monthly** backup, retain 6 months

Example cron entry:

```cron
# Daily backup at 2 AM UTC
0 2 * * * pg_dump -h localhost -U canopy -d canopy --no-owner --no-acl -Fc -f /backups/canopy_$(date +\%Y\%m\%d).dump
```

### Restore

```bash
# 1. Stop canopyd (prevents writes during restore)
systemctl stop canopyd

# 2. Drop and recreate the database
psql -h $DB_HOST -U postgres -c "DROP DATABASE IF EXISTS canopy;"
psql -h $DB_HOST -U postgres -c "CREATE DATABASE canopy OWNER canopy;"

# 3. Restore from backup
pg_restore \
  -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME \
  --no-owner --no-acl \
  canopy_20260727_020000.dump

# 4. Start canopyd (auto-runs any pending migrations)
systemctl start canopyd
```

> **Note:** The restore drops and recreates the database. Any changes made after the backup timestamp are lost. For point-in-time recovery, configure PostgreSQL WAL archiving — that is outside the scope of this guide.

### What to Back Up

| Data              | Method      | Notes                                       |
|-------------------|-------------|---------------------------------------------|
| PostgreSQL DB     | `pg_dump`   | All trees, nodes, edges, topics, profiles.  |
| Card data         | Filesystem  | Card data is stored in per-type SQLite `.db` files under `~/.hermes/canopy/cards/`. Back up this directory as well. |
| JWT secret        | File/secret manager | Without it, existing tokens become invalid. |

---

## Monitoring

### Prometheus Metrics

When `METRICS_ENABLED=true`, `canopyd` exposes a `/metrics` endpoint with the following metrics:

| Metric                          | Type      | Description                               |
|---------------------------------|-----------|-------------------------------------------|
| `request_total`                 | Counter   | HTTP requests by method, path, status.    |
| `request_duration_seconds`      | Histogram | Request latency distribution.             |
| `active_connections`            | Gauge     | Current concurrent HTTP connections.      |
| `tree_count`                    | Gauge     | Number of trees in the database.          |
| `node_count`                    | Gauge     | Number of nodes in the database.          |
| `resume_duration_seconds`       | Histogram | Seconds between a user's first tree-scoped read (GET/HEAD) after an idle gap and the compiled context they resume with. Buckets include the 30 second SLO line. |
| `resume_started_total`          | Counter   | Resume windows opened.                    |

`resume_duration_seconds` is the **server-observable** part of the product's
"resume work in <30 seconds" claim, and nothing more: browser render time is not
included, and a resume a user performs entirely from their local cache never
reaches the server, so it is invisible here. A resume window opens on a user's
first successful (HTTP 2xx) tree-scoped read (GET/HEAD) after at least 5 minutes
with no tree-scoped read by that user, and completes when that same user's next
successful `GET /api/v1/context/{node_id}` returns — the compiled context, i.e.
the point where the server has handed the user back their working context.
Only reads count: the tree-scoped routes include writes (PATCH/DELETE
`/api/v1/trees/{tree_id}` and the `POST` routes under it), and those never open,
refresh or complete a window.
`resume_started_total` counts windows opened, so a window that never reaches a
context compile shows up as started-but-never-observed.

**Scrape config** (add to `prometheus.yml`):

```yaml
scrape_configs:
  - job_name: 'canopy'
    scrape_interval: 30s
    static_configs:
      - targets: ['localhost:8091']
```

### Grafana Dashboard

A pre-built Grafana dashboard is included at `deploy/grafana/dashboard.json`. It provides:

- **Request Latency (p50/p95/p99)** — timeseries chart
- **Requests per Second** — by method and path
- **Active Connections** — gauge
- **Tree & Node Count** — stat cards
- **Error Rate (4xx/5xx)** — timeseries chart

**Import the dashboard:**

1. In Grafana, go to **Dashboards → New → Import**
2. Upload `deploy/grafana/dashboard.json` or paste its contents
3. Select your Prometheus data source
4. Click **Import**

The dashboard refreshes every 30 seconds and defaults to a 1-hour window.

---

## Upgrading

### Binary Upgrade (Self-Built or Pre-Built)

```bash
# 1. Download or build the new binary
make build  # or download the release binary

# 2. Stop the running instance gracefully
kill -TERM $(pgrep canopyd)
# canopyd shuts down gracefully (drains SSE connections, 30s timeout)

# 3. Replace the binary
cp bin/canopyd /usr/local/bin/canopyd

# 4. Start the new version
DB_HOST=... DB_USER=... DB_PASSWORD=... DB_NAME=... JWT_SECRET=... ./canopyd

# Migrations run automatically on first startup.
# If the schema is already current, the migration step is a no-op.
```

### Docker Compose Upgrade

```bash
# Pull latest code
git pull

# Rebuild and restart. The rebuild is REQUIRED, not optional: the image compiles
# the migrations embedded in the binary, and `docker compose up -d` alone can reuse
# an image built from older HEAD — which then refuses to start against the newer
# database with `STALE BUILD` (see Troubleshooting). Never bypass that guard.
docker compose build --no-cache
docker compose up -d

# Verify the API and the schema pair (compose publishes :8092)
docker compose logs canopyd | grep "canopyd starting"
curl -s http://localhost:8092/health   # schema_version must equal embedded_migrations
```

### Migration Safety

- Migrations are **additive-only** (new tables, new columns with defaults, new indexes). Rollback is possible by restoring a pre-upgrade backup.
- The binary and its embedded migrations are **atomically versioned** — you cannot accidentally run mismatched migrations.
- Migration failure on startup is fatal (`log.Fatal`). The server will not start with a broken schema.

### Rollback Guidance

If an upgrade causes issues:

```bash
# 1. Stop the new version
kill -TERM $(pgrep canopyd)

# 2. Restore from pre-upgrade backup (see Backup & Restore)
pg_restore -h $DB_HOST -U $DB_USER -d $DB_NAME pre_upgrade_backup.dump

# 3. Replace binary with the previous version
cp /usr/local/bin/canopyd.previous /usr/local/bin/canopyd

# 4. Start the old version — migrations that were already applied are
#    detected as "no change" and skipped safely
./canopyd
```

> **Important:** The migration framework (`golang-migrate`) tracks applied migrations by version number. If you restore a database backup from before the upgrade, the old migration state is restored as well. The old binary will then only apply the migrations it knows about.

---

## Troubleshooting

### Port Already in Use

**Symptom:** `bind: address already in use` or `listen tcp :8091: bind: address already in use`

**Fix:**
```bash
# Find what's on the port
sudo ss -tlnp | grep 8091

# Kill it, or change the Canopy port. Do NOT reuse :8091 (the native/live default)
# or :8092 (the port docker-compose publishes) — pick a free scratch port:
HTTP_ADDR=127.0.0.1:8093 ./canopyd
```

See [SCRATCH_INSTANCE.md](SCRATCH_INSTANCE.md) before starting a second instance:
a different port alone is not isolation (the database, the card store, the
gateway run registry and the file root are shared too).

### `STALE BUILD` at startup

**Symptom:** the server exits immediately with

```
STALE BUILD: database schema is newer than this binary's embedded migrations —
rebuild (make build) or redeploy a current image; refusing to start against a
schema this binary cannot understand
```

**Cause:** the database was created (or migrated) by a *newer* canopyd than the
binary you just ran — a `git pull` without a rebuild, a reused docker image built
from older HEAD, or a container left over from a previous checkout. This is the
deliberate schema guard (DF-HERMES-CANOPY-1): an older binary against a newer
schema fails silently deeper in the request path, so it refuses to boot instead.

**Fix — rebuild, never bypass.** Compare both versions and rebuild from your
checkout:

```bash
curl -s http://localhost:8091/health   # or :8092 for the compose stack
# → {"status":"ok",…,"schema_version":47,"embedded_migrations":47}
#   schema_version > embedded_migrations means this binary is stale.

make build            # native: rebuild from HEAD, then restart your instance
make deploy           # …or the atomic path for the systemd instance

docker compose build canopyd && docker compose up -d   # compose: rebuild the image
```

There is no flag to skip the check, and none should be added.

### PostgreSQL Connection Refused

**Symptom:** `dial tcp 127.0.0.1:5432: connect: connection refused`

**Checklist:**
1. Is PostgreSQL running? `pg_isready -h $DB_HOST -p $DB_PORT -U $DB_USER`
2. Is the port correct? Docker Compose maps PG to `5437`, not `5432`.
3. Is `DB_SSLMODE` set correctly? Cloud providers often require `require` or `verify-full`.
4. Is pg_hba.conf allowing connections? Check for `host all all 0.0.0.0/0 md5` or similar.

### Database Authentication Failed

**Symptom:** `password authentication failed for user "canopy"`

**Fix:**
```bash
# Verify credentials directly
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -c "SELECT 1;"

# Reset password if needed
psql -h $DB_HOST -U postgres -c "ALTER USER canopy WITH PASSWORD 'new-password';"
```

### Migration Failure

**Symptom:** `database migration failed` in logs

**Common causes:**
- Database user lacks `CREATE TABLE` / `ALTER TABLE` privileges. Grant full ownership: `ALTER DATABASE canopy OWNER TO canopy;`
- A previous migration was partially applied (dirty state). The migration framework refuses to continue on a dirty database. Restore from backup and try again, or manually clean the `schema_migrations` table.

### CORS Errors in Browser

**Symptom:** Browser console shows `Access-Control-Allow-Origin` errors.

Canopy sets permissive CORS headers (`Access-Control-Allow-Origin: *`) by default for local development. In production behind a reverse proxy, ensure:

1. The proxy passes the `Host` header.
2. The frontend PWA is served from the same origin as the API (or you configure the reverse proxy to handle CORS).
3. If accessing from a different origin, set up CORS headers in your reverse proxy:
   ```nginx
   add_header Access-Control-Allow-Origin "https://canopy.example.com" always;
   ```

### TLS Certificate Expiry

**Symptom:** `SSL certificate expired` or browser shows "Not Secure"

**Fix for Let's Encrypt (certbot):**
```bash
# Check expiry
sudo certbot certificates

# Force renewal
sudo certbot renew --force-renewal

# If auto-renewal is broken, check the timer
sudo systemctl status certbot.timer

# Manual renewal
sudo certbot renew --nginx
```

**Fix for Caddy:** Caddy handles renewal automatically. Check logs if there's an issue:
```bash
journalctl -u caddy --since "1 hour ago"
```

### SSE Connections Dropping

**Symptom:** Frontend reconnects frequently, or real-time updates stop.

**Checklist:**
1. Proxy buffering is off (`proxy_buffering off` in nginx).
2. Proxy read timeout is long enough (at least 60s, 86400s recommended).
3. No load balancer or firewall is killing idle connections. Some cloud load balancers have default idle timeouts as low as 60 seconds.
4. Check server logs for `sse` or `connection` related errors.

### High Memory Usage

**Symptom:** `canopyd` uses more memory than expected.

Canopy maintains an in-memory SSE hub with per-tree ring buffers:
- **10,000 connection cap** — beyond this, new SSE connections are rejected.
- **1-hour event retention** — per-tree ring buffers hold up to 1,000 events each.
- **Memory scales with active connections × ring buffer depth × event size.**

If memory is a concern, reduce the number of concurrent SSE connections (each open browser tab holds one) or run behind a connection-limiting proxy.

---

## Systemd Unit (Recommended)

For long-running production deployments, run `canopyd` under systemd:

```ini
# /etc/systemd/system/canopyd.service
[Unit]
Description=Hermes Canopy Server
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=canopy
Group=canopy
WorkingDirectory=/opt/canopy

EnvironmentFile=/etc/canopy/environment
ExecStart=/usr/local/bin/canopyd

# Restart policy
Restart=on-failure
RestartSec=5s

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/canopy /opt/canopy

[Install]
WantedBy=multi-user.target
```

Create the environment file:

```bash
# /etc/canopy/environment
DB_HOST=localhost
DB_PORT=5432
DB_USER=canopy
DB_PASSWORD=<redacted>
DB_NAME=canopy
DB_SSLMODE=disable
JWT_SECRET=<redacted>
HTTP_ADDR=127.0.0.1:8091
LOG_LEVEL=warn
METRICS_ENABLED=true
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now canopyd
sudo systemctl status canopyd
```

---

## Architecture Reference

For context while operating the server:

```
┌─────────────────────────────────────────────────────┐
│                   HTTP Server (:8091)                │
│  ┌──────────────┐  ┌──────────┐  ┌───────────────┐  │
│  │   Handlers   │  │   SSE    │  │  Prometheus   │  │
│  │  (REST API)  │  │   Hub    │  │  (/metrics)   │  │
│  └──────┬───────┘  └────┬─────┘  └───────┬───────┘  │
│         │               │                │          │
│  ┌──────┴────────────────┴────────────────┴───────┐  │
│  │              Services Layer                      │  │
│  │  Tree | Node | Edge | Topic | Card | Graph     │  │
│  │  Approval | Sync | Profile | MLS               │  │
│  └─────────────────────┬──────────────────────────┘  │
│                        │                             │
│  ┌─────────────────────┴──────────────────────────┐  │
│  │              Data Layer                          │  │
│  │  PostgreSQL (primary) + SQLite (cards)          │  │
│  │  Migrations auto-run on startup                │  │
│  └────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
```

- **Database:** PostgreSQL is the authoritative store for all graph data (trees, nodes, edges, topics, profiles, approvals, events, snapshots). Migrations run automatically.
- **Storage direction (2026-09-16 ruling):** PostgreSQL contradicts the product vision (single binary, no Docker, no PostgreSQL, no external dependencies) and the SQLite-native Hermes ecosystem. The owner ruling is **SQLite-first** — `modernc.org/sqlite` (pure Go, WAL) as the authoritative graph store inside one zero-external-dependency binary. **Status: declared direction, NOT landed** — tracked as board row **GAP-076**; PostgreSQL stays authoritative until it does.
- **SSE Hub:** In-memory ring buffer per tree, 10K connection cap, 1-hour event retention.
- **Cards:** per-type **SQLite** databases (`modernc.org/sqlite`, CGo-free, pure Go) under `~/.hermes/canopy/cards/`, overridable with `CANOPY_CARD_DATA_DIR`. Each card type is one `<dir>/<card-type>.db` file opened with `_journal_mode=WAL` (`internal/card/database.go`); there is no JSONL path for cards. The `internal/card/duckdb/` package is cgo-only and ARCHIVED — zero importers repo-wide, and no shipped build selects it.
- **Frontend:** React PWA with Service Worker and Yjs/IndexedDB for local-first sync. Built to `frontend/dist/` as a release artifact and served separately — `canopyd` is API-only in MVP and does not embed it (see [Quick Start](#quick-start)).
- **Graceful shutdown:** 30 seconds — drains SSE connections, then shuts down HTTP.
