# Dogfood Integration Report — 2026-09-22 (MCP server surface + production deploy path)

**Run type:** cron dogfood (8th for this repo), NEW ANGLE. Prior runs covered
trees/nodes/context-manifests/SSE (08-17), the live gateway (08-27), fresh-DB +
bunker compose install + file viewer (09-10/09-14), CLI + card export (09-16),
two-user collab/MLS (09-20), PWA human-path (09-21). This run took the two
surfaces NONE of them touched: **the MCP endpoint** (`POST /api/v1/mcp` —
consumed as a real external client with the official `modelcontextprotocol`
Python SDK, not just curl) and **the production deploy path** (built PWA behind
`deploy/reference-proxy.py`, the README's own flagged footgun), plus a
**non-Docker source install** on the ephemeral bunker (prior bunker run proved
compose; this one proves `make build`).

Build under test: HEAD `027f3b1b` (locally compiled `CGO_ENABLED=0`; scratch
PostgreSQL `canopy_df0922` on :5437, canopyd on 127.0.0.1:8096, isolated HOME
`/tmp/df0922-home`, gateway pointed at closed port :9 — no live writes).

**Verdict: 🟡 PROMISING-BUT-ROUGH** — the graph/API core keeps holding up
(cold-restart resume at 0.16s, MCP write lands and reads back, SSE streams
through the proxy), but this run produced a **P1 protocol-interop break**
(every MCP `tools/call` is rejected by the official SDK client) and a **P1
production-auth break** (the README's own `--token` + Basic recipe never
injects the token). Both are invisible to the repo's own test suites.

Board rows: **DF-HERMES-CANOPY-45 … 48**.

## Promise statement

README/skill: *"Messages are nodes in a DAG … a user can resume work in <30
seconds"*; `POST /api/v1/mcp` exposes the graph to external agents; the
production path is `npm run build` + same-origin `deploy/reference-proxy.py`
with token injection behind an auth gate.

## Human-path step table

| # | Step | Result | LIVE evidence |
|---|---|---|---|
| 1 | Fresh scratch instance (isolated DB/HOME/port) | PASS | `scripts/scratch-instance.sh --keep` full battery exit 0 (auth gate, refusal control, live non-contamination); then a long-lived instance started by hand on :8096, `/health` 200 schema 48/48 |
| 2 | MCP handshake (raw JSON-RPC) | PASS | `initialize` negotiates both `2025-06-18` and legacy `2024-11-05`; `ping` `{}`; `notifications/initialized` → HTTP 202 empty; unknown method → `-32601`; unauthenticated → 401 TOKEN_MISSING |
| 3 | MCP tools/list | PASS | exactly 7 tools: `list_trees, get_tree, create_node, list_topics, get_graph_stats, list_approvals, list_cards` |
| 4 | MCP tools/call — all 7 tools, real writes | PASS server-side | `create_node` via MCP → REST read-back shows the child node (`node_count` 1→2); `get_graph_stats` accurate (`active_nodes`2, `edge_type_reply`1); cards/topics/approvals list cleanly (seeded card via REST 201) |
| 5 | MCP interop with the OFFICIAL Python SDK 2.2.0 | **FAIL (P1 → DF-45)** | `initialize`+`tools/list` pass, then EVERY `call_tool` dies client-side: `CallToolResult` validation — `content: Field required`. Server returns bare domain objects instead of the spec `{"content":[{"type":"text",...}]}` shape |
| 6 | Production deploy leg (built PWA + reference proxy) | **FAIL on the documented secure mode (P1 → DF-46)** | `npm run build` exit 0; proxy serves the SPA (`/` 200, `/trees` SPA-fallback 200), `/health` proxied, no-creds → 401, SSE `node_added` events stream through. BUT `--token` + `--require-auth-*` (README's stated recipe) → every API call 401 TOKEN_MISSING: validated Basic header is passed upstream, suppressing injection. Same proxy without Basic injects fine |
| 7 | Ephemeral-bunker source install (las-bunker-03, agent `cc2d1bc3`, destroyed after) | PASS with friction | full clone crawled >15 min (abandoned, 31 MB of .git only); `--depth 1` = 408 s to HEAD 027f3b1; bare Debian agent: `go` missing, `docker-compose` missing, no passwordless sudo → Go 1.26.5 userland install + `make build` = **152 s**; rootless dockerd needed `~/bin` PATH + XDG fix; Postgres 16 (rootless docker) ready +2 s; canopyd healthy +3 s (schema 48/48); MCP `tools/list` answers; **CLI `tree create`/`tree list` succeed on the virgin box** |
| 8 | Perf: cold restart (the "<30s resume" promise) | PASS | kill → restart against existing DB → first 200 `/health` in **0.160 s**; first authenticated `GET /api/v1/trees` after restart **8 ms**, tree intact |
| 9 | Perf: warm ops (hyperfine, 30 runs, incl. curl overhead) | PASS | REST `GET /trees` 12.3 ms; MCP `tools/call list_trees` 12.7 ms; MCP `tools/call create_node` 19.8 ms. Nothing slow enough to file a PERF row |

## Findings

### DF-HERMES-CANOPY-45 (P1) — MCP results are not MCP: official SDK rejects every tools/call

`internal/handler/mcp_handler.go` returns bare domain objects
(`{"trees":[...]}`, `{"node":{...}}`) where the MCP spec requires a
`CallToolResult` containing a `content` array. Hand-rolled curl never notices
(HTTP 200 + plausible JSON), but the reference client enforces the schema and
aborts. The endpoint is therefore unusable by any spec-compliant MCP client —
which is the entire point of an MCP server. Fix is mechanical: wrap dispatch
results in `{"content":[{"type":"text","text":<json>}]}`
(+ optional `structuredContent`), and add an SDK-based canary to CI.

### DF-HERMES-CANOPY-46 (P1) — the documented production auth mode never injects

`deploy/reference-proxy.py:_handle()` validates the Basic gate but never strips
the credential; `_proxy()` then sees a client `Authorization` and correctly
skips injection — so `--token` + `--require-auth-user/password`, the recipe the
README spells out for production auth, 401s every call. Browsers auto-attach
Basic after the challenge, so the dead-end is guaranteed in real use. The proxy
test suite covers gate-less injection and client-auth-wins, but never the
gate+token combination. Fix: strip Authorization after successful Basic
validation; add a gate+token integration test.

### DF-HERMES-CANOPY-47 (P3) — node metadata base64 vs docs

Node `metadata` round-trips as a base64 string (`"e30="`) on create/list while
`docs/API.md` node examples show a JSON object; the existing "byte-typed fields
are base64" note is scoped to file objects. The frontend has its own decoder
(`frontend/src/lib/nodeMeta.ts`); fresh API consumers don't.

### DF-HERMES-CANOPY-48 (P3) — MCP path skips membership precheck; error mislabelled

Bogus `tree_id` over MCP → JSON-RPC `-32000 "database unavailable: … violates
foreign key constraint fk_nodes_tree (SQLSTATE 23503)"`; the same input over
REST → clean `403 NOT_TREE_MEMBER`. The MCP dispatch path bypasses the
membership gate and the service layer mislabels an FK violation as
"database unavailable". Minor sibling: unknown fields on tree-create still
return generic `INVALID_BODY "must be valid JSON"` (GAP-053/072 fixed node
endpoints only).

## Numbers

- **Time-to-first-success (this run):** ~25 min to a seeded scratch instance +
  first successful MCP `tools/call` (the SDK leg added ~20 min of diagnosis).
- **Fresh-machine install (source path):** shallow clone 408 s + Go bootstrap
  ~90 s + build 152 s + PG 2 s + canopyd 3 s ≈ **11 minutes** end-to-end for a
  user who knows the rootless-docker dance; the undocumented parts
  (`DOCKER_HOST`, `~/bin` PATH, no `sysctl` in default PATH, missing Go) are
  the real friction.
- **Cold restart → healthy:** 0.160 s (promise: <30 s).
- **Friction count:** 12 (2 broken documented paths, 2 API/doc mismatches,
  clone weight, no shallow-clone hint, Go toolchain assumed, rootless docker
  undocumented, compose plugin missing, `/tmp` collision on shared host,
  approval-heavy probe environment).

## What this run did NOT touch

Approvals workflow (list-only via MCP), plugins, federation, Yjs CRDT sync,
and the PWA in a real browser. Those remain open surface for the next run.
