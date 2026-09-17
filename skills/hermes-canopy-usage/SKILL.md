---
name: hermes-canopy-usage
description: >-
  How to actually USE Hermes Canopy (canopyd + PWA): entry points, run commands,
  working API paths, UI flows, CLI, the file-viewer subsystem, the LIVE Hermes
  gateway surface (GAP-050), and the pitfalls that waste time (stale deployed
  binary can crash-loop the service — GAP-069 outage, fresh-DB profile brick
  GAP-071, casing split, docs drift). Load this before touching the stack.
  Written from the 2026-08-17, 08-27, 09-10 and 09-14 deep dogfood runs.
version: 2.2.0
category: software-development
---

# Hermes Canopy — Usage Skill

Canopy OS: graph-native collaboration surface for human-agent work — and since
GAP-050/051 (2026-08-27) the **live interface of Hermes**: the Dashboard shows real
gateway runs, the chat composer starts REAL agent runs, SSE streams events, and
approvals resolve — with the gateway API key held server-side. Go backend
(`canopyd`) + React/TS PWA + PostgreSQL + live Hermes gateway (:8642).

## Entry points

| Entry | How | Notes |
|---|---|---|
| PWA | `cd frontend && npm run dev` → http://localhost:5173 | Vite proxy → `:8091`, auto-injects dev JWT — zero auth |
| API | `curl :8091/api/v1/...` with `Authorization: Bearer <dev-jwt>` | JWT: HS256, secret `dev-secret-change-me`, sub `00000000-0000-0000-0000-000000000001` (README one-liner) |
| CLI | `CANOPY_SERVER_URL=http://localhost:8091 CANOPY_TOKEN=<jwt> ./bin/canopyd tree list` | server default :8080 — set the env vars! |
| MCP | `POST /api/v1/mcp` JSON-RPC 2.0 | `tools/list` → list_trees, get_tree, create_node, list_topics |
| Gateway | `GET /api/v1/gateway/status` etc. | canopyd proxies the live Hermes gateway; key held server-side |

## Live Hermes gateway surface (verified 2026-08-27)

- `GET  /api/v1/gateway/status` — connectivity + run counts
- `GET  /api/v1/gateway/runs` — run registry (newest first)
- `POST /api/v1/gateway/runs` — `{"message", "session_id"?}` → starts a REAL
  Hermes run (202 + run_id). **Real tokens — keep prompts tiny.**
- `GET  /api/v1/gateway/runs/{run_id}` — record with event history
- `GET  /api/v1/gateway/runs/{run_id}/events` — SSE (history replay + live fan-out)
- `POST /api/v1/gateway/runs/{run_id}/stop` — interrupt a RUNNING run
  (→ `{"status":"stopping"}`, record becomes `cancelled`)
- `POST /api/v1/gateway/runs/{run_id}/approval` — `{choice, approval_id?}`

Browser path: `http://localhost:5173/api/v1/gateway/...` proxies identically.
Dashboard flow: type in the chat composer → real run starts → SSE streams →
output appears. Zero console errors in the 2026-08-27 probe.

## Verified working flows (2026-08-17 + 2026-08-27)

- **Create tree (API):** `POST /api/v1/trees` body
  `{"title","description","rootMessage":{"content","contentFormat":"markdown","nodeType":"message"}}`
  → 201 with `root_node_id`. **rootMessage is REQUIRED and must include nodeType.**
  **camelCase here.**
- **Create node (this is ALSO the reply path):** `POST /api/v1/trees/{id}/nodes`
  body `{"parent_id","content","node_type":"message","edge_type":"reply"}`
  → 201 `{node, edge}`. **snake_case here.** NOTE: `parent_id: ""` (empty
  string) is accepted as absent → silently creates a ROOT-level node
  (GAP-072: should 400). Don't pass empty strings.
- **Fork (tree-scoped):** `POST /api/v1/trees/{tree_id}/nodes/{node_id}/fork`.
  Fork only works on nodes that already have ≥1 child (leaf fork → 400
  VALIDATION_ERROR, documented).
- ✅ **`POST .../nodes/{node_id}/reply` (tree-scoped) is REAL since tick 429
  (GAP-065 FIXED, commit cc581a4):** mounted on the production router and
  guarded by `route_parity_test.go`. The 2026-09-10 phantom-route finding is
  historical. Node-create with `parent_id` remains the PWA composer path.
- **Context manifest (headline feature):** `GET /api/v1/context/{node_id}` →
  `{content, manifest:{tokenBudget, tokensUsed, ancestry:[...]}}`. In the UI: click a
  canvas node → "Context | N / 8,000 tokens" panel.
  → *Amended 2026-09-17 (GAP-080 phase 5a):* the manifest object above is
  unchanged; it now also carries `manifestHash`, a stable 64-hex sha256 over
  the content-bearing fields (excluding `requestId`/`compiledAt` and itself).
  The panel and the run indicator both render its first 12 chars, so a preview
  compile and a run record can be compared at a glance; the full value is in
  each element's `title`. See `docs/API.md` § Compile Context.
- **Topics:** list `GET /api/v1/topics?tree_id=...`; create `POST /api/v1/topics`
  camelCase `{treeId, rootNodeId, title, description}`.
- **Export:** `GET /api/v1/trees/{id}/export` (NOT `/api/v1/export`); import
  `POST /api/v1/trees/import`.
- **Browser:** trees are clickable cards on `/trees` (not `<a>` links). Create-Tree
  dialog works (GAP-040 FIXED — title + root message required, Create disabled
  until both filled). Composer = `textarea` + send button. Canvas nodes =
  `.react-flow__node`. Keys: `⌘0` fit, `j/k` move, `h/l` drill, `m` merge, `?` help.
- **CLI:** `tree create <name> --content <text>` (content mandatory), `tree list`,
  `tree navigate <id>` (hierarchy output), `tree delete <id>`, `--help` works
  (GAP-042/045 FIXED).

## File-viewer subsystem quick reference (verified 2026-09-14, HEAD 9bbe3dc)

Source of truth `internal/fileviewer/handlers.go` (UNDOCUMENTED elsewhere — GAP-071).
All require auth AND an acting profile (fresh DBs: see pitfall #9 seed).

| Method | Path | Verified behavior |
|---|---|---|
| POST | `/api/v1/files/upload` | multipart `file` (+optional `filename`,`declaredMime`,`sourceMessageId`) → 201 `{file:{id,sha256,mimeType,viewerHint,…}, was_deduped, stream_url, expires_at}`; re-upload of identical bytes → `was_deduped:true` |
| POST | `/api/v1/files/resolve` | JSON `{hash_ref:{profile_id,sha256}}` or upload variant |
| POST | `/api/v1/files/resolve/batch` | batch of the above |
| GET | `/api/v1/files` | list (empty DB without profile seed → 404 PROFILE_NOT_FOUND) |
| GET | `/api/v1/files/recents` | ONLY populated by access-log POSTs, not uploads |
| GET | `/api/v1/files/{id}` | 200 metadata |
| GET | `/api/v1/files/{id}/stream` | 200 bytes; `Range: bytes=0-9` → 206 + `Content-Range` |
| GET/POST | `/api/v1/files/{id}/access` | POST body `{action:"open\|download\|…", viewer_slug:"code"}` → 201; GET = log |
| GET | `/api/v1/viewers` | built-in registry (audio_video, code, image, json, …) with `supportsMime`/`supportsExtensions` |
| GET | `/api/v1/viewers/{slug}` | one viewer |
| POST | `/api/v1/viewers/dispatch` | JSON `{"file_id":"…","tree_id":"…"}` → 200 `{viewerSlug, renderType, bundlePath, isBuiltIn,…}` |

## Known pitfalls (updated 2026-09-10)

1. **Fresh DB + first write = 503 "database unavailable" — root-caused
   (GAP-064):** the tree-create tx's `tree_members` insert hits FK
   `tree_members_user_id_fkey` because nothing provisions the dev user
   (`00000000-…-0001`) for a NEW database. `/health` stays 200, server log
   stays silent. Fix before first write on any fresh DB:
   ```sql
   INSERT INTO users (id, hermes_user_id, display_name)
   VALUES ('00000000-0000-0000-0000-000000000001','dev','Dev User')
   ON CONFLICT (id) DO NOTHING;
   ```
   (The live :5437 DB has the row — from `scripts/seed-demo-data.sql` after the
   tick-416 wipe. `make test`/integration tests seed their own users, which is
   why CI never caught this.)
2. **The deployed binary may be STALE (GAP-052 class; recurred 09-10 as
   GAP-067):** the live stack runs `/home/kara/bin/canopyd` via
   `systemctl --user canopy-canopyd` — NOT the repo binary. On 09-10 it was 7
   days old and 404'd `/health/relay` while the board said FTR-05 COMPLETE.
   Probe: `curl -s :8091/health/relay -o /dev/null -w '%{http_code}'` (404 =
   stale). Fix: `make deploy`. The skill's old advice (manual cp + restart) is
   superseded — `make deploy` does build → atomic install → restart → /health
   poll → gateway smoke.
3. **camelCase vs snake_case (GAP-053, mostly fixed):** tree-create + topics =
   camelCase; node endpoints accept BOTH casings now and unknown-field errors
   name the field (`json: unknown field "title"`). Still: check struct tags in
   `internal/handler/node_handler.go` when in doubt.
4. **contentFormat accepts only `markdown`** (default; GAP-055). `"text"` → 400.
5. **Gateway run registry is in-memory (GAP-054):** canopyd restart wipes run
   history; stop on a completed run → 404 run_not_found (misleading).
6. **`make test` times out on healthy code (GAP-037):** handler package needs
   >120s; use `go test ./internal/... -timeout 300s` or `make test-short`.
7. **E2E battery has ZERO gateway coverage** — it passed while the gateway
   surface 404'd. Don't trust E2E green as proof the gateway works; probe it
   directly.
8. **✅ compose works without `.env` since ce3e1dc (GAP-068 FIXED, verified
   09-14 on a fresh bunker clone):** `docker compose up -d --build` comes up
   clean; `cp .env.example .env` is now optional (docs still recommend it).
9. **File-viewer subsystem is UNDOCUMENTED and bricks on fresh DBs
   (GAP-071, 09-14):** the 12 `/api/v1/files` + `/api/v1/viewers` routes are
   in NO doc (source of truth: `internal/fileviewer/handlers.go`). Every
   call needs an ACTING PROFILE: JWT sub → `profiles` row +
   `profile_route` row tied to a `workspaces` row. Fresh DBs have none →
   404 PROFILE_NOT_FOUND everywhere; `POST /workspaces/{ws}/profiles`
   500s (`fk_profile_route_workspace`) because the workspaces row is never
   created. Until GAP-071 is fixed, seed by hand:
   ```sql
   INSERT INTO workspaces (id, name, slug)
     VALUES ('<uuid>','ws','ws') ON CONFLICT DO NOTHING;
   INSERT INTO profiles (id, owner_id, name, display_name)
     VALUES ('<uuid>','00000000-0000-0000-0000-000000000001','dev','Dev')
     ON CONFLICT DO NOTHING;
   INSERT INTO profile_route (workspace_id, profile_name, is_active)
     VALUES ('<uuid>','dev',true) ON CONFLICT DO NOTHING;
   ```
   Then it works: upload → 201 (sha256 dedup, viewerHint), dispatch → 200,
   stream → 200/206 Range, access-log POST → recents populates (uploads
   alone do NOT appear in /files/recents).
10. **STALE BUILD can take the service DOWN (GAP-069 outage, 09-12→09-14):**
    canopyd embeds its migrations and REFUSES to start if the DB schema is
    newer than the binary. Migrations 43–46 (file-viewer, 4ac2f15) were
    applied to the SHARED live :5437 DB by the E2E/foreman run; the deployed
    binary (embeds 42) then crash-looped ~37h, silently, ≈27k restarts.
    Diagnosis: `systemctl --user status canopy-canopyd` +
    `journalctl --user -u canopy-canopyd -n 20` (STALE BUILD names both
    versions). Fix: clean worktree → `make deploy`. NEVER roll the DB back.

## Stack hygiene (updated 2026-09-14 — the :5437 advice below was REVOKED)

- 🚨 **NEVER point E2E/foreman/test runs at the LIVE :5437 DB.** The old
  advice ("canonical E2E DB is the compose PG on :5437") caused the GAP-069
  outage: a test run migrated it to schema 46 and the deployed binary
  refused to start for ~37h. Stand up a throwaway postgres instead:
  `docker run -d --name canopy-test-pg -e POSTGRES_USER=canopy -e POSTGRES_PASSWORD=canopy -e POSTGRES_DB=canopy -p 127.0.0.1:5438:5432 postgres:16`
  Fresh DBs now work out of the box for the core flows (GAP-064 fixed:
  dev user auto-seeded when JWT_SECRET is the dev default). The file-viewer
  surface still needs the GAP-071 hand-seed on fresh DBs.
- After ANY change touching `internal/`, `cmd/`, `migrations/`: run
  `scripts/check-deploy-staleness.sh`; deploy with `make deploy` (clean
  worktree only — it refuses dirty trees). Check
  `systemctl --user is-active canopy-canopyd` afterward.
- Don't restart the running stack during foreman ticks; the E2E loop owns it.
- Clean up scratch trees/topics via API DELETE (204).
- The demo tree is E2E-only (GAP-051); the live DB should show real Hermes data.

## Docs truthfulness ranking

`docs/API.md` is accurate (routes, schemas, quirks — including the gateway surface
§14). `docs/INTEGRATION.md` §6 curl walkthrough is now correct (fork path fixed).
When in doubt, read `internal/server/server.go` mounts — it's the ground truth for
routing.
