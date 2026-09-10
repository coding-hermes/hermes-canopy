# Dogfood Integration Report — 2026-09-10

**Run type:** cron dogfood (5th for this repo). Build under test: HEAD `1e3647b`,
locally compiled (CGO_ENABLED=0), run on a **fresh scratch database**
(`canopy_dogfood_0910`) on :8097 — plus the live stack (:8091, binary from
2026-09-02) for regression comparison, plus an **ephemeral-bunker fresh
install** on las-bunker-03 (agent 1289ee6d).

**Verdict:** 🟡 PROMISING-BUT-ROUGH — see `.coding-hermes/dogfood-log.md`.

## Promise statement

"A user can run the local server + PWA, create a tree, branch from any
message, and see a visible, budgeted context manifest for every node —
resuming work in <30 seconds."

## What worked end-to-end (real evidence, fresh DB)

| Step | Call | Result |
|---|---|---|
| Fresh DB migrate | start canopyd on empty DB | schema 0→42 in seconds, `/health` 200 |
| Create tree | `POST /api/v1/trees` `{title, description, rootMessage:{content, contentFormat}}` | **201** + `root_node_id` |
| Reply (real path) | `POST /api/v1/trees/{t}/nodes` `{parent_id, content, node_type, edge_type}` | **201** `{node, edge}` |
| Fork | `POST /api/v1/trees/{t}/nodes/{n}/fork` | 201 (leaf fork → 400, documented) |
| Context manifest | `GET /api/v1/context/{node_id}` | 200 — `79/8000 tokens`, full `ancestry[]`, topic boundary rendered |
| SSE | `GET /api/v1/trees/{t}/events` while replying | `event: node_added` delivered live (945 bytes in 3s) |
| Topics | `POST /api/v1/topics` camelCase | 201 (but `root_node_id` = zero-UUID bug, see GAP-066) |
| Cards | `POST /api/v1/cards` `{treeId, nodeId, appId, cardType, data}` | 201 (unknown-field errors now name the field — GAP-053 fix verified) |
| Graph | `GET /api/v1/graph/trees/{t}/stats` | 200 — 4 nodes / 3 edges / depth 2 |
| Export | `GET /api/v1/trees/{t}/export` | 200 |
| PWA | :5173 + Vite proxied API | 200, JWT auto-inject works |

Time-to-first-success (API): ~20 min for a NEW database — dominated by the
undocumented user-seeding step below. On an already-provisioned DB it is
~15 s (matches prior runs).

## The trap a fresh user falls into (read this first)

**1. Fresh DB + quick start = every write 503s with "database unavailable".**

The dev JWT subject is `00000000-…-0001`, but nothing creates that user row.
The tree-create transaction inserts into `tree_members`, whose FK
(`tree_members_user_id_fkey` → `users.id`) aborts the whole transaction, and
the service maps ANY tx error to `SERVICE_UNAVAILABLE "database unavailable"`.

- Symptom: `/health` = 200, `GET /api/v1/trees` = 200, `POST /api/v1/trees`
  = 503 forever. Zero error lines in the server log.
- Fix (before first write on a NEW database):
  ```sql
  INSERT INTO users (id, hermes_user_id, display_name)
  VALUES ('00000000-0000-0000-0000-000000000001', 'dev', 'Dev User')
  ON CONFLICT (id) DO NOTHING;
  ```
  (live DB already has the row — that's why the deployed stack works;
  `scripts/seed-demo-data.sql` also inserts it but is not referenced from
  README/INTEGRATION).
- Filed as **GAP-064 (P1)**.

**2. The documented reply route does not exist — never has.**

`docs/API.md` "Reply to Node" documents
`POST /api/v1/trees/{tree_id}/nodes/{node_id}/reply`. Only `Routes()`
registers it and `Routes()` is never mounted; the mounted `TreeRoutes()`
has list/create/get/fork only. chi therefore answers `404 page not found`.
The real reply is tree-scoped node-create with `parent_id` in the body
(that's what the PWA composer does). Filed as **GAP-065 (P1)**.

**3. Compose quick start fails at the first command (bunker-verified).**

`docker compose up -d --build` → `env file .../.env not found`. `.env` is
gitignored, `env_file: .env` is mandatory in docker-compose.yml, and no doc
says `cp .env.example .env`. Filed as **GAP-068 (P2)**.

## Bunker fresh-install result (las-bunker-03, agent 1289ee6d)

With those two workarounds (`cp .env.example .env`; DOCKER_HOST →
`/run/bunker/<id>/docker.sock`, a bunker-environment quirk, not a repo bug):

- `docker compose up -d --build`: **PASS, 356 s** cold (multi-stage image build
  incl. frontend); `canopy-pg` healthy, `canopy-server` up.
- Smoke: `/health` → `{"status":"ok",...,"schema_version":42}` 200;
  `/version` 200; `/api/v1/trees` → 401 unauthenticated (auth enforced).
- Extra drift found while smoking: README §Deployment says compose exposes
  **:8091**; the actual mapping is **:8092 → 8080**.
- Note: the compose database is fresh → the documented first write would hit
  GAP-064's 503 (no users row).
- Agent destroyed after smoke.

## Working quick start (as of HEAD 1e3647b, fresh machine)

```bash
git clone https://github.com/coding-hermes/hermes-canopy.git && cd hermes-canopy
make build                                   # Go 1.25+, CGO optional
docker run -d --name canopy-pg-standalone \
  -e POSTGRES_USER=canopy -e POSTGRES_PASSWORD=canopy \
  -e POSTGRES_DB=canopy -p 5437:5432 postgres:16
# ONE-TIME (any fresh DB): seed the dev user (see trap #1)
DB_HOST=localhost DB_PORT=5437 DB_USER=canopy DB_PASSWORD=canopy \
  DB_NAME=canopy HTTP_ADDR=:8091 ./bin/canopyd
cd frontend && npm install && npm run dev    # PWA at :5173, zero auth
```

Reply workflow (real route):

```bash
JWT=$(node -e "...README one-liner...")
curl -X POST localhost:8091/api/v1/trees/$TREE/nodes \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{"parent_id":"<node-uuid>","content":"my reply","node_type":"message","edge_type":"reply"}'
```

## Friction log (10 items, chronological)

1. README troubleshooting example says `schema_version:38`; HEAD ships 42 (trivial drift).
2. `POST /api/v1/trees` → 503 on fresh DB, zero log output (trap #1) — 25 min lost.
3. Retry loop gave VALIDATION_ERROR before 503 on one attempt → misleading (looks flaky, is deterministic).
4. `POST .../reply` → chi `404 page not found` while fork on the same path matched (trap #2) — 10 min lost.
5. Topic create returns `root_node_id: 00000000-…-0000` despite valid input (GAP-066, regressed from 09-07).
6. `POST /api/v1/cards` with a `title` field → 400 "unknown field" — my error, but the
   field-level message (vs GAP-053's old "must be valid JSON") is a real UX improvement.
7. `GET /health/relay` 404 on the live :8091 binary (deployed 09-02 predates FTR-05 P7, GAP-067).
8. Bunker install: compose fails without `.env` (trap #3).
9. Bunker agent has no Go/npm — only the compose path is viable there; README's
   prerequisites assume a dev machine (acceptable, but worth a note in SELF_HOST).
10. SSE + nested reply + fork + manifest all worked first try — zero friction.

## What I'd fix first with 1 hour of maintainer time

1. Seed the dev user at startup when `JWT_SECRET` is the dev default (one
   `INSERT ... ON CONFLICT DO NOTHING` behind an env guard). Kills trap #1 forever.
2. Mount `handleReply` in `TreeRoutes()` (one line) or fix API.md. Kills trap #2.
3. `env_file` optional in docker-compose.yml (or `required: false`), plus one
   README line: `cp .env.example .env`. Kills trap #3.
