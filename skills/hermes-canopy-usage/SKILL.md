---
name: hermes-canopy-usage
description: >-
  How to actually USE Hermes Canopy (canopyd + PWA): entry points, run commands,
  working API paths, UI flows, CLI, and the pitfalls that waste time (double-nodes
  routes, broken UI tree-create, CLI create dead, docs drift). Load this before
  touching the stack. Written from the 2026-08-17 deep dogfood run.
version: 1.0.0
category: software-development
---

# Hermes Canopy — Usage Skill

Canopy OS: graph-native collaboration surface for human-agent work. Messages are
nodes in a DAG, every model call has a visible context manifest, Cards are graph
nodes with structured data. Go backend (`canopyd`) + React/TS PWA + PostgreSQL.

## Entry points

| Entry | How | Notes |
|---|---|---|
| PWA | `cd frontend && npm run dev` → http://localhost:5173 | Vite proxy → `:8091`, auto-injects dev JWT — zero auth |
| API | `curl :8091/api/v1/...` with `Authorization: Bearer <dev-jwt>` | JWT: HS256, secret `dev-secret-change-me`, sub `00000000-0000-0000-0000-000000000001` (README one-liner) |
| CLI | `CANOPY_SERVER_URL=http://localhost:8091 CANOPY_TOKEN=<jwt> ./bin/canopyd tree list` | server default :8080 — set the env vars! |
| MCP | `POST /api/v1/mcp` JSON-RPC 2.0 | `tools/list` → list_trees, get_tree, create_node, list_topics |

## Verified working flows (2026-08-17)

- **Create tree (API):** `POST /api/v1/trees` body
  `{"title","description","rootMessage":{"content","contentFormat":"markdown","nodeType":"message"}}`
  → 201 with `root_node_id`. **rootMessage is REQUIRED and must include nodeType.**
- **Create node:** `POST /api/v1/trees/{id}/nodes` body
  `{"parent_id","content","node_type":"message"}` → 201 `{node, edge}`.
- **Reply/fork/update/delete (flat mount):** `/api/v1/nodes/nodes/{node_id}/reply`
  and `/fork` — note the **double `nodes` segment**. Fork only works on nodes that
  already have ≥1 child.
- **Context manifest (headline feature):** `GET /api/v1/context/{node_id}` →
  `{content, manifest:{tokenBudget, tokensUsed, ancestry:[...]}}`. In the UI: click a
  canvas node → "Context | N / 8,000 tokens" panel.
- **Topics:** list `GET /api/v1/topics?tree_id=...`; create `POST /api/v1/topics`
  camelCase `{treeId, rootNodeId, title, description}`.
- **Export:** `GET /api/v1/trees/{id}/export` (NOT `/api/v1/export`); import
  `POST /api/v1/trees/import`.
- **Browser:** trees are clickable cards on `/trees` (not `<a>` links). Composer =
  `textarea` + `button[aria-label="Send message"]`. Canvas nodes =
  `.react-flow__node`. Keys: `⌘0` fit, `j/k` move, `h/l` drill, `m` merge, `?` help.

## Known pitfalls (checked 2026-08-17 — board GAP-040..045)

1. **UI tree creation is BROKEN (GAP-040, P0):** the Create-Tree dialog always 400s
   (sends rootMessage without nodeType; labels Root Message "optional" though the
   API requires it). Use the API to create trees until fixed.
2. **`canopyd tree create` always fails (GAP-042):** CLI sends no rootMessage. Use
   `tree list` / `navigate` / `delete`; create via API.
3. **INTEGRATION.md §6 fork path 404s (GAP-041):** use the double-nodes path above.
4. **`make test` times out on healthy code (GAP-037):** handler package needs >120s;
   use `go test ./internal/... -timeout 300s` or `make test-short` for fast checks.
5. **Topics in UI need a raw node UUID** (GAP-044); create via API with the tree's
   `root_node_id`.
6. **No UI fork affordance** (GAP-043): "branch from any message" is API-only.

## Stack hygiene

- Canonical E2E DB is the compose PG on **:5437** (NOT localhost:5432) — a fresh
  local DB makes tree-create 503 and visual-regression drift.
- Dev user row must exist in `users` (INSERT ... ON CONFLICT, see INTEGRATION.md §8.1).
- Don't restart the running stack during foreman ticks; the E2E loop owns it.
- Clean up scratch trees/topics via API DELETE (204).

## Docs truthfulness ranking

`docs/API.md` is accurate (routes, schemas, quirks). `docs/INTEGRATION.md` §6 curl
walkthrough has drift (fork path; topics missing). When in doubt, read
`internal/server/server.go` mounts — it's the ground truth for routing.
