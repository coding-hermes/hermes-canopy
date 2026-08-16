# Self-Hosting Hermes Canopy

A practical guide to deploying and operating Canopy OS on your own infrastructure.

Canopy is a graph-native collaboration surface for human-agent work. It ships as a single Go binary (`canopyd`) that bundles the HTTP API server, SSE event hub, Prometheus metrics, database migrations, and the production frontend (PWA) — all in one process backed by PostgreSQL.

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

# 3. Run canopyd
DB_HOST=localhost DB_USER=canopy DB_PASSWORD=canopy DB_NAME=canopy \
  JWT_SECRET=$(openssl rand -base64 32) \
  ./canopyd

# 4. Verify
curl http://localhost:8080/health
# → {"status":"ok"}
```

The server auto-runs database migrations on startup, so the schema is created automatically the first time it connects.

Open `http://localhost:8080` in a browser to reach the bundled frontend PWA.

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
| Go          | **1.23**+      | go.mod currently targets 1.25               |
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

# Build and start (production-ready)
docker compose up -d

# View logs
docker compose logs -f canopyd

# Verify
curl http://localhost:8091/health
```

This starts:
- **canopyd** on host port `8091` (container `:8080`)
- **PostgreSQL 16** on host port `5437` (container `:5432`)
- Health-gated startup — canopyd waits for PG to pass `pg_isready` before starting

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
| `CANOPY_SERVER_URL` | `http://localhost:8080`      | Base URL of the Canopy API server.     |
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
export HTTP_ADDR=127.0.0.1:8080
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
2026-07-27T10:00:00Z INF canopyd starting version=v0.1.0 http_addr=:8080 db_host=localhost
2026-07-27T10:00:00Z INF running database migrations...
2026-07-27T10:00:01Z INF migrations complete (20 applied)
2026-07-27T10:00:01Z INF HTTP server listening addr=:8080
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
        proxy_pass http://127.0.0.1:8080;
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

### Caddy (Simplest)

Caddy auto-provisions Let's Encrypt certificates with zero configuration:

```
canopy.example.com {
    reverse_proxy 127.0.0.1:8080
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
| Card data         | Filesystem  | Card data is stored as JSONL files under `~/.hermes/canopy/cards/`. Back up this directory as well. |
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

**Scrape config** (add to `prometheus.yml`):

```yaml
scrape_configs:
  - job_name: 'canopy'
    scrape_interval: 30s
    static_configs:
      - targets: ['localhost:8080']
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

# Rebuild and restart
docker compose build --no-cache
docker compose up -d

# Verify
docker compose logs canopyd | grep "canopyd starting"
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

**Symptom:** `bind: address already in use` or `listen tcp :8080: bind: address already in use`

**Fix:**
```bash
# Find what's on the port
sudo ss -tlnp | grep 8080

# Kill it, or change the Canopy port
HTTP_ADDR=:8081 ./canopyd
```

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
HTTP_ADDR=127.0.0.1:8080
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
│                   HTTP Server (:8080)                │
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
│  │  PostgreSQL (primary) + DuckDB (cards)          │  │
│  │  Migrations auto-run on startup                │  │
│  └────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
```

- **Database:** PostgreSQL is the authoritative store for all graph data (trees, nodes, edges, topics, profiles, approvals, events, snapshots). Migrations run automatically.
- **SSE Hub:** In-memory ring buffer per tree, 10K connection cap, 1-hour event retention.
- **Cards:** DuckDB-in-process + JSONL files under `~/.hermes/canopy/cards/` (git-friendly).
- **Frontend:** React PWA with Service Worker and Yjs/IndexedDB for local-first sync. Served from the embedded filesystem when built via Docker or `make build-embed`.
- **Graceful shutdown:** 30 seconds — drains SSE connections, then shuts down HTTP.
