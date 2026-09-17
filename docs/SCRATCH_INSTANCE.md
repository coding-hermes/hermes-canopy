# Isolated scratch instance

Run a throwaway canopyd **next to** a live one — same checkout, same machine,
zero shared state. Use it for demos, experiments, card/graph work you intend to
delete, and any "let me try that once" you would rather not run against the
instance other people (and agents) are using.

This page exists because Canopy's **published defaults collide**: the quick start
serves the API on `:8091` with PostgreSQL on `:5437`, and `docker-compose.yml`
publishes canopyd on `:8092`. A second instance started with those values does not
sit beside the first one — it lands on top of it.

```bash
# One command, end to end (build → own DB → own port → own HOME → create/list → cleanup)
scripts/scratch-instance.sh
```

It prints the database name, port, and scratch root it chose, runs the checks
below, then stops its server, drops **only** its own database and deletes **only**
its own root.

---

## 1. Why the documented defaults collide

| Layer | What the docs assume | What that means for a second instance |
|-------|----------------------|---------------------------------------|
| API port | README quick start / `make run`: `HTTP_ADDR=:8091` | A scratch server on `:8091` either fails to bind or (worse) makes the CLI's historical default point at *it* |
| API port (compose) | `docker-compose.yml` publishes `8092:8080` | `:8092` is already taken by the compose stack, and is *documented* as the compose API |
| PostgreSQL | Host port `5437` (`canopy` / `canopy` / `canopy`) | Shared on purpose — but a scratch run must not use the `canopy` database |
| Container names | `canopy-server`, `canopy-pg` are **fixed** in `docker-compose.yml` | `docker compose -p <name>` does **not** isolate a second stack: container names, the published host port and the `pgdata` volume are not namespaced by `-p` for these entries, so `up -d` collides with the running stack |
| Process HOME | `~/.hermes/canopy/cards`, `~/.hermes/canopy/gateway/runs.jsonl`, `~/.canopy/files` | Every instance running as the same user shares the card SQLite databases, the gateway run registry and uploaded file bytes — a scratch server would read and write the live ones |
| CLI target | `CANOPY_SERVER_URL` (default `http://localhost:8091`) | The CLI is an HTTP **client**: `DB_*` and `HTTP_ADDR` never redirect it, and with any of them set and no `CANOPY_SERVER_URL` it refuses to run instead of guessing |

**`docker compose -p <project>` is not isolation.** For this stack the service
container names are hard-coded, the host port is a literal, and the volume is a
fixed name. Two projects would fight over all three. Isolating a compose stack
means editing the compose file (names, ports, volume, project) — out of scope
here; use this page's native-binary recipe instead.

## 2. What isolation actually requires

Four **independent** roots, plus a clean process environment. Getting three of
four is the failure mode: a fresh graph database alone still shares cards,
files and gateway runs.

| # | Root | Setting | Default (shared!) | Scratch value |
|---|------|---------|-------------------|---------------|
| 1 | Graph / authoritative store | `DB_NAME` (or `CANOPY_DB_URL`) | `canopy` on `:5437` | a unique `canopy_scratch_<tick>_<rand>` database on the existing server |
| 2 | HTTP API | `HTTP_ADDR` | `:8091` (make/native) / `:8092` (compose) | an explicit free loopback port, e.g. `127.0.0.1:8093` |
| 3 | Cards + gateway registry | `HOME` | `~/.hermes/canopy/…` | a scratch HOME, e.g. `/tmp/canopy-scratch-…/home` |
| 4 | Uploaded file bytes | `CANOPY_FILE_ROOT` | `~/.canopy/files` | a scratch file root |

Plus:

- **`env -i`** for the server process, so an inherited `CANOPY_DB_URL`,
  `DB_PASSWORD`, `API_SERVER_KEY` or `HERMES_WEBUI_GATEWAY_API_KEY` cannot leak in.
- **the Hermes gateway endpoint pointed at a closed loopback port**
  (`HERMES_WEBUI_GATEWAY_BASE_URL=http://127.0.0.1:9`), so a scratch instance can
  never submit a live agent run even if a key is present.
- **a fresh build from your checkout** — see §5.

### Store paths (source of truth)

| Store | Resolution | Code |
|-------|------------|------|
| Cards (SQLite per card type) | `$HOME/.hermes/canopy/cards/<type>.db` | `internal/card/database.go` |
| Gateway run registry | `$HOME/.hermes/canopy/gateway/runs.jsonl` | `internal/gateway/service.go` |
| File viewer bytes | `$CANOPY_FILE_ROOT`, else `$HOME/.canopy/files` | `internal/config/config.go`, `internal/fileviewer/storage.go` |
| Graph (authoritative) | PostgreSQL via `DB_*` / `CANOPY_DB_URL` | `internal/db` |

There is no environment variable for the card or gateway paths: **they follow
`HOME`.** (`CANOPY_FILE_ROOT` is the one store with an explicit override.) Note
`GO`-side `os.UserHomeDir()` reads `$HOME` on Linux, which is why exporting a
scratch `HOME` isolates both.

## 3. The recipe

The script implements exactly this; read it if you want to do the steps by hand.

```bash
# 0. Pick names that cannot collide. This example is tick-unique.
PORT=8093                                   # NOT 8091 / 8092 / 8080
DB=canopy_scratch_$(date +%s)_$RANDOM
ROOT=$(mktemp -d /tmp/canopy-scratch-XXXXXX)
mkdir -p "$ROOT/home" "$ROOT/files"

# 1. Preflight: the port must be free and PostgreSQL must be up.
ss -tln | grep -E "[:.]${PORT}\$" && { echo "port $PORT busy"; exit 1; }
pg_isready -h 127.0.0.1 -p 5437

# 2. Build from HEAD (never reuse a stale binary — see §5).
cd /path/to/hermes-canopy && git rev-parse --short HEAD
go build -o "$ROOT/canopyd" ./cmd/canopyd

# 3. Create the throwaway database on the EXISTING server. Name it uniquely;
#    never drop or reuse an existing database.
PGPASSWORD=canopy psql -h 127.0.0.1 -p 5437 -U canopy -d postgres -c "CREATE DATABASE $DB"

# 4. Start the scratch server with no inherited environment at all.
env -i PATH="$PATH" HOME="$ROOT/home" \
  HTTP_ADDR="127.0.0.1:$PORT" \
  DB_HOST=127.0.0.1 DB_PORT=5437 DB_USER=canopy DB_PASSWORD=canopy \
  DB_NAME="$DB" DB_SSLMODE=disable \
  CANOPY_FILE_ROOT="$ROOT/files" \
  HERMES_WEBUI_GATEWAY_BASE_URL=http://127.0.0.1:9 \
  METRICS_ENABLED=false LOG_FORMAT=json \
  "$ROOT/canopyd" serve >"$ROOT/canopyd.log" 2>&1 &
SRV=$!
trap 'kill -TERM "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null; \
      PGPASSWORD=canopy psql -h 127.0.0.1 -p 5437 -U canopy -d postgres \
        -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)"; rm -rf "$ROOT"' EXIT

# 5. Readiness + the schema guard (never bypass it — a mismatch means the
#    binary predates the schema; rebuild instead).
for i in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/health" && break; sleep 1; done
curl -s "http://127.0.0.1:$PORT/health"
# → {"status":"ok",…,"schema_version":47,"embedded_migrations":47,…}
#   schema_version MUST equal embedded_migrations. If it does not, the process
#   exits with `STALE BUILD` and the fix is a rebuild, not a flag.

# 6. Authenticate as the dev user. The default JWT_SECRET provisions the dev
#    user/workspace/profile on a FRESH database (README § Authentication).
TOKEN=$(python3 - <<'PY'
import base64, hashlib, hmac, json, time
b64 = lambda b: base64.urlsafe_b64encode(b).rstrip(b"=")
h = b64(json.dumps({"alg":"HS256","typ":"JWT"}, separators=(",",":")).encode())
n = int(time.time())
p = b64(json.dumps({"sub":"00000000-0000-0000-0000-000000000001",
                    "iat":n,"exp":n+3600}, separators=(",",":")).encode())
print((h+b"."+p+b"."+b64(hmac.new(b"dev-secret-change-me", h+b"."+p, hashlib.sha256).digest())).decode())
PY
)

# 7. Point the CLI at the SCRATCH API explicitly. Inherited DB_* / HTTP_ADDR are
#    deliberately present here and wrong on purpose: an explicit
#    CANOPY_SERVER_URL always wins (README § CLI, rule 1).
HTTP_ADDR=:59991 DB_PORT=5437 DB_NAME=canopy \
CANOPY_SERVER_URL="http://127.0.0.1:$PORT" CANOPY_TOKEN="$TOKEN" \
  "$ROOT/canopyd" tree create "scratch tree" --content 'isolation check'
CANOPY_SERVER_URL="http://127.0.0.1:$PORT" CANOPY_TOKEN="$TOKEN" \
  "$ROOT/canopyd" tree list

# Negative control — a write-shaped CLI run must REFUSE to guess a target:
#   env -u CANOPY_SERVER_URL DB_PORT=5437 DB_NAME=canopy "$ROOT/canopyd" tree list
#   → exit 1, "ambiguous API target — DB_NAME, DB_PORT set without CANOPY_SERVER_URL"

# 8. Authenticated read, then prove the live instance does not see it.
curl -s -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:$PORT/api/v1/trees"
curl -s -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:8091/api/v1/trees"   # no scratch tree here

# 9. Prove the NON-graph stores are scratch-local. A card write creates
#    <scratch HOME>/.hermes/canopy/cards/<type>.db — if that file appears under
#    your real HOME, HOME was not isolated.
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"treeId\":\"<tree-id>\",\"nodeId\":\"<root-node-id>\",\"appId\":\"scratch\",\"cardType\":\"compact\",\"data\":{}}" \
  "http://127.0.0.1:$PORT/api/v1/cards"
ls "$ROOT/home/.hermes/canopy/cards/"

# 10. Cleanup = stop the server, drop ONLY your database, delete ONLY your root.
kill -TERM "$SRV"
PGPASSWORD=canopy psql -h 127.0.0.1 -p 5437 -U canopy -d postgres -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)"
rm -rf "$ROOT"
```

Never print the JWT or the database password in a transcript, a log or a report.

## 4. What the script verifies

`scripts/scratch-instance.sh` is a runnable acceptance test for the recipe, not
just a launcher. Before cleanup it asserts:

- the port was free **before** anything started, and PostgreSQL was ready;
- the database name was unused, and the name is `canopy_scratch_*` (never
  `canopy` / `postgres` / `template*`);
- the server came up and `/health` reports `schema_version == embedded_migrations`
  (a stale build fails here instead of being silently accepted);
- the running server resolved `CANOPY_FILE_ROOT` to the scratch path (read from
  its own JSON log);
- the CLI **created and listed** a tree against the scratch URL *while* wrong
  `DB_*` / `HTTP_ADDR` values were set in its environment;
- the CLI **refused** the same command without `CANOPY_SERVER_URL`;
- an unauthenticated read is rejected (`401`);
- the live instance on `:8091` does **not** list the scratch tree.

After cleanup it re-verifies, so "cleanup happened" is proven rather than
claimed:

- the scratch database is gone, the scratch root is gone, the port is released;
- the live instance's pid is unchanged (it was never restarted), the live
  `/health` is byte-identical, and the PostgreSQL database list is unchanged;
- the shared `$HOME` stores (`cards/`, `files/`) are unchanged.

Any destructive step is gated on the database having been created by that same
run: if the requested name already exists, the run is refused and **nothing** is
dropped. The script never runs `docker compose up/down`, never touches systemd,
and never restarts the live service.

```bash
scripts/scratch-instance.sh --help      # usage
scripts/scratch-instance.sh --keep      # keep the DB + root for inspection
scripts/scratch-instance.sh --port 8099 # explicit scratch port

# Regression coverage for both the script and this page (hermetic by default):
bash scripts/test-scratch-instance.sh
# …plus the live safety case + a full end-to-end run:
CANOPY_SCRATCH_LIVE_TESTS=1 CANOPY_SCRATCH_E2E=1 bash scripts/test-scratch-instance.sh
```

## 5. Ports, and rebuilding a stale image

| Path | API | PostgreSQL | Notes |
|------|-----|------------|-------|
| Native / systemd live instance | `:8091` | `:5437` | `canopy-canopyd.service` → `canopyd serve` |
| `make run` (dev default) | `:8091` | `:5437` | matches the Vite dev proxy |
| Raw binary, no env | `:8080` | `:5432` | compiled-in defaults |
| **docker compose** | **`:8092`** | `:5437` (`${CANOPY_PG_HOST_PORT:-5437}`) | `container_name: canopy-server` / `canopy-pg` |
| Scratch (this page) | your choice, e.g. `:8093` | `:5437` (own database) | never 8091 / 8092 / 8080 |

**A compose image built from older HEAD can refuse to start** with:

```
STALE BUILD: database schema is newer than this binary's embedded migrations
```

That guard is deliberate — it stops a binary written for an older schema from
mutating a newer database. Do **not** disable it. Compare the two versions with
`curl -s http://localhost:8092/health` (`schema_version` vs
`embedded_migrations`) and rebuild the image from your checkout so it carries the
migrations it needs:

```bash
git pull                       # or: confirm you are on the commit you mean to run
docker compose build canopyd   # rebuild the API image from this checkout
docker compose up -d           # recreate the container
curl -s http://localhost:8092/health
```

Start (and upgrade) compose only after that rebuild; `docker compose up -d` on
its own can reuse a cached image that predates the schema.

## 6. Limits of this recipe

- **PostgreSQL is required today.** The graph/authoritative store is PostgreSQL;
  a fresh database is created by the same server that runs the embedded
  migrations. The SQLite-first pivot (**GAP-076**) is **pending** — it has not
  shipped, so do not plan a scratch instance around a SQLite graph store yet.
  What *is* SQLite today is the **card** backend (per-card-type files under
  `$HOME/.hermes/canopy/cards`), which is why the card store is isolated through
  `HOME` rather than through the database.
- **Compose cannot be isolated with `-p`** for this file (§1). A truly isolated
  second compose stack needs its own project-level edits (service names, ports,
  volume, and `-p`), which this recipe deliberately does not do to the shipped
  `docker-compose.yml`.
- **The scratch instance shares the PostgreSQL server** (port, credentials,
  resources) with the live database; it does not get its own server. It is
  isolated by *database name*, not by server.
- **It is a dev-secret instance.** Unless you set `JWT_SECRET`, the server
  provisions the dev user/workspace/profile — convenient, and exactly why it must
  stay on loopback and in a scratch database.
