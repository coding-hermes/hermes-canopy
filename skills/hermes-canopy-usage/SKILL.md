---
name: hermes-canopy-usage
description: >-
  How to actually USE Hermes Canopy (canopyd + PWA): entry points, run commands,
  working API paths, UI flows, CLI, the LIVE Hermes gateway surface (GAP-050),
  and the pitfalls that waste time (stale deployed binary, camelCase-vs-snake_case
  split, in-memory run registry, docs drift). Load this before touching the stack.
  Written from the 2026-08-17 + 2026-08-27 deep dogfood runs.
version: 2.0.0
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
- **Create node:** `POST /api/v1/trees/{id}/nodes` body
  `{"parent_id","content","node_type":"message"}` → 201 `{node, edge}`.
  **snake_case here.**
- **Reply/fork/update/delete (tree-scoped):** `POST /api/v1/trees/{tree_id}/nodes/{node_id}/reply`,
  `POST /api/v1/trees/{tree_id}/nodes/{node_id}/fork`,
  `PATCH /api/v1/trees/{tree_id}/nodes/{node_id}`,
  `DELETE /api/v1/trees/{tree_id}/nodes/{node_id}`. Fork only works on nodes that
  already have ≥1 child (leaf fork → 400 VALIDATION_ERROR, documented).
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

## Known pitfalls (checked 2026-08-27)

1. **The deployed binary may be STALE (GAP-052, P1):** the live stack runs
   `/home/kara/bin/canopyd` via `systemctl --user canopy-canopyd` — NOT the repo
   binary. If `GET /api/v1/gateway/status` 404s, the deployed binary predates
   GAP-050: `stat /home/kara/bin/canopyd` vs `git log -1 --format=%ci` of the
   gateway commits, then `make build && cp bin/canopyd /home/kara/bin/ &&
   systemctl --user restart canopy-canopyd`. There is no deploy script and no
   post-deploy smoke test.
2. **camelCase vs snake_case split (GAP-053, P1):** tree-create + topics =
   camelCase; ALL node endpoints = snake_case (`content_format`, `node_type`,
   `parent_id`) with strict unknown-field rejection — camelCase on a node endpoint
   returns 400 "request body must be valid JSON" even though the body IS valid.
   When in doubt, read struct tags in `internal/handler/node_handler.go`.
3. **contentFormat accepts only `markdown`** (default; GAP-055). `"text"` → 400.
4. **Gateway run registry is in-memory (GAP-054):** canopyd restart wipes run
   history; stop on a completed run → 404 run_not_found (misleading).
5. **`make test` times out on healthy code (GAP-037):** handler package needs
   >120s; use `go test ./internal/... -timeout 300s` or `make test-short`.
6. **E2E battery has ZERO gateway coverage** — it passed while the gateway surface
   404'd. Don't trust E2E green as proof the gateway works; probe it directly.

## Stack hygiene

- Canonical E2E DB is the compose PG on **:5437** (NOT localhost:5432) — a fresh
  local DB makes tree-create 503 and visual-regression drift.
- Dev user row must exist in `users` (INSERT ... ON CONFLICT, see INTEGRATION.md §8.1).
- Don't restart the running stack during foreman ticks; the E2E loop owns it.
- Clean up scratch trees/topics via API DELETE (204).
- The demo tree is E2E-only (GAP-051); the live DB should show real Hermes data.

## Docs truthfulness ranking

`docs/API.md` is accurate (routes, schemas, quirks — including the gateway surface
§14). `docs/INTEGRATION.md` §6 curl walkthrough is now correct (fork path fixed).
When in doubt, read `internal/server/server.go` mounts — it's the ground truth for
routing.
