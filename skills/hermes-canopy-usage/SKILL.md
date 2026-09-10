---
name: hermes-canopy-usage
description: >-
  How to actually USE Hermes Canopy (canopyd + PWA): entry points, run commands,
  working API paths, UI flows, CLI, the LIVE Hermes gateway surface (GAP-050),
  and the pitfalls that waste time (fresh-DB 503 = missing users row, phantom
  reply route, stale deployed binary, casing split, docs drift). Load this
  before touching the stack. Written from the 2026-08-17, 08-27 and 09-10
  deep dogfood runs.
version: 2.1.0
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
  → 201 `{node, edge}`. **snake_case here.** `parent_id` empty = root-level node.
- **Fork (tree-scoped):** `POST /api/v1/trees/{tree_id}/nodes/{node_id}/fork`.
  Fork only works on nodes that already have ≥1 child (leaf fork → 400
  VALIDATION_ERROR, documented).
- ⚠️ **`POST .../nodes/{node_id}/reply` (tree-scoped) is a PHANTOM ROUTE —
  documented in API.md but never mounted** (GAP-065, found 2026-09-10: chi
  answers bare `404 page not found`; only the unmounted `NodeHandler.Routes()`
  registers it). The skill's v2.0 claim that it worked was wrong. Reply =
  node-create with `parent_id` (that's what the PWA composer does).
  `PATCH/DELETE /trees/{t}/nodes/{n}` ARE mounted and work.
- **Context manifest (headline feature):** `GET /api/v1/context/{node_id}` →
  `{content, manifest:{tokenBudget, tokensUsed, ancestry:[...]}}`. In the UI: click a
  canvas node → "Context | N / 8,000 tokens" panel.
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
8. **`docker compose up -d` fails without `.env` (GAP-068):** `env_file: .env`
   is mandatory and .env is gitignored; no doc says `cp .env.example .env`.
   Note: `.env.example` defaults `HTTP_ADDR=:8080` while compose expects the
   container to listen on :8080 — check the compose env block before editing.

## Stack hygiene

- Canonical E2E DB is the compose PG on **:5437** (NOT localhost:5432) — a fresh
  local DB hits GAP-064 (503 on first write) unless you seed the dev user first
  (see pitfalls #1).
- Old hygiene note "a fresh local DB makes tree-create 503" is EXPLAINED by
  GAP-064 (missing users row) — it is not a flake and not a port issue.
- Dev user row must exist in `users` (INSERT ... ON CONFLICT, see INTEGRATION.md §8.1).
- Don't restart the running stack during foreman ticks; the E2E loop owns it.
- Clean up scratch trees/topics via API DELETE (204).
- The demo tree is E2E-only (GAP-051); the live DB should show real Hermes data.

## Docs truthfulness ranking

`docs/API.md` is accurate (routes, schemas, quirks — including the gateway surface
§14). `docs/INTEGRATION.md` §6 curl walkthrough is now correct (fork path fixed).
When in doubt, read `internal/server/server.go` mounts — it's the ground truth for
routing.
