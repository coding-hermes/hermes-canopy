# Hermes Canopy — Dogfood Integration Report (2026-08-17)

**Verdict: 🟡 PROMISING-BUT-ROUGH** — the core engine (DAG nodes, context compiler,
SSE canvas sync, MCP) is real and works, but the primary UI onboarding flow (create a
tree) is broken and the documented CLI/curl paths have drifted from reality.

Deep real-use run (cron dogfood), live stack: PWA `:5173` (Vite) · canopyd `:8091` ·
PostgreSQL `:5437` (canopy-pg). Playwright browser session + curl API walkthrough +
CLI session. Board tasks: **GAP-040..GAP-045** (see `.coding-hermes/board/tasks.jsonl`).

---

## 1. What this project is

**Canopy OS** — a graph-native collaboration surface for human-agent work. Messages are
nodes in a DAG; every model call has a visible context manifest; Cards are graph nodes
with structured data. Single-user desktop-first PWA + local Go server (`canopyd`),
PostgreSQL-backed, Yjs/SSE sync, DuckDB cards.

**The promise (null hypothesis):** *A user can run the local server + PWA, create a
tree, post/branch/synthesize messages, see a visible context manifest, and use topics
and cards — resuming work in <30 seconds.*

## 2. How to use it for real (working paths, verified 2026-08-17)

### 2.1 Stack (already running in the foreman env; from scratch see docs/INTEGRATION.md)

```bash
docker compose up -d            # PG on :5437 + canopyd (or run canopyd bare)
cd frontend && npm run dev      # PWA on :5173, proxies /api → :8091, injects dev JWT
```

### 2.2 API user (curl) — WORKING walkthrough

Sign a dev JWT (README one-liner, secret `dev-secret-change-me`, sub
`00000000-0000-0000-0000-000000000001`) — or reuse the dev JWT in
`docs/INTEGRATION.md` §6.

```bash
AUTH="Authorization: Bearer $TOKEN"; BASE=http://localhost:8091

# create tree — rootMessage is REQUIRED, nodeType included (GAP-008 contract):
curl -s -X POST $BASE/api/v1/trees -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"title":"T","description":"d","rootMessage":{"content":"hi","contentFormat":"markdown","nodeType":"message"}}'
# → 201 {id, root_node_id}

# child node (snake_case body):
curl -s -X POST $BASE/api/v1/trees/$TREE/nodes -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"parent_id\":\"$ROOT\",\"content\":\"hello\",\"node_type\":\"message\"}"

# reply + fork live at the FLAT mount with a DOUBLE 'nodes' segment:
curl -s -X POST $BASE/api/v1/nodes/nodes/$NODE/reply -H "$AUTH" -d '{"content":"r"}'
curl -s -X POST $BASE/api/v1/nodes/nodes/$NODE/fork  -H "$AUTH" -d '{"content":"f","node_type":"message"}'
# ⚠ fork requires the node to ALREADY have ≥1 child (else 400 "fork requires parent with at least one child")

# context manifest (the core promise — WORKS):
curl -s $BASE/api/v1/context/$NODE -H "$AUTH"
# → {content, manifest:{requestId, nodeId, tokenBudget:8000, tokensUsed, ancestry:[{id,kind,title,tokenCount,truncated}]}}

# topics (camelCase body, tree-scoped list):
curl -s "$BASE/api/v1/topics?tree_id=$TREE" -H "$AUTH"
curl -s -X POST $BASE/api/v1/topics -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"treeId\":\"$TREE\",\"rootNodeId\":\"$ROOT\",\"title\":\"mytopic\"}"

# cards + export:
curl -s $BASE/api/v1/cards -H "$AUTH"
curl -s $BASE/api/v1/trees/$TREE/export -H "$AUTH"    # NOT /api/v1/export

# MCP (agent access — works, JSON-RPC 2.0):
curl -s -X POST $BASE/api/v1/mcp -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'
curl -s -X POST $BASE/api/v1/mcp -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"method\":\"tools/call\",\"id\":2,\"params\":{\"name\":\"create_node\",\"arguments\":{\"tree_id\":\"$TREE\",\"parent_id\":\"$ROOT\",\"content\":\"from agent\"}}}"
```

### 2.3 Browser user (PWA) — WORKING paths

- **Open tree → canvas:** trees list on `/trees` (cards, NOT `<a>` links — click the
  card text). Tree view renders all nodes as React Flow canvas (`/tree/{id}`).
- **Composer:** textarea at bottom → `button[aria-label="Send message"]` → node appears
  on canvas immediately (BUG-032 Yjs bridge). Verified: 11 → 13 canvas nodes, message
  visible, zero console errors.
- **Context manifest panel:** click any canvas node → right-side panel shows
  `Context | 74 / 8,000 tokens` + ancestry items (WIRE-002). THIS is the headline
  feature and it works.
- **Keyboard:** `⌘0` fit, `⌘+/-` zoom, `j/k` move, `h/l` drill, `m` merge, `?` help.
- **Add reply:** per-node button on the canvas.

### 2.4 CLI

```bash
export CANOPY_SERVER_URL=http://localhost:8091 CANOPY_TOKEN=$TOKEN
./bin/canopyd tree list        # ✅ table: ID / NAME / CREATED
./bin/canopyd tree navigate <id>  # ✅ indented tree, content snippets, unique row labels (GAP-045)
./bin/canopyd tree create "X"  # ❌ ALWAYS fails (no rootMessage sent) — GAP-042
./bin/canopyd tree delete <id> # ✅
./bin/canopyd topic detect|proposals|config   # exists, undocumented
```

## 3. Errors hit and what they meant

| Error | Where | Meaning / fix |
|---|---|---|
| `404 page not found` on `POST /api/v1/nodes/{id}/fork` | INTEGRATION.md §6 | Real path has double `nodes` segment (GAP-041) |
| `404 page not found` on `GET /api/v1/export` | my guess | Real path is `/api/v1/trees/{id}/export` (API.md has it) |
| `400 VALIDATION_ERROR root message content is required` | UI dialog, title-only | UI labels Root Message "(optional)" but API requires content (GAP-040) |
| `400 VALIDATION_ERROR invalid node type` | UI dialog, with message | UI sends rootMessage without `nodeType` (GAP-040) |
| `400 MISSING_TREE_ID` on `GET /api/v1/topics` | API | topics list requires `?tree_id=` |
| `400 INVALID_JSON unknown field "name"` on topics POST | API | body is camelCase `treeId/rootNodeId/title` (GAP-044) |
| `400 fork requires parent with at least one child` | fork on leaf node | deliberate guard; undocumented (GAP-043) |
| `[VALIDATION_ERROR] root message content is required` | `canopyd tree create` | CLI never sends rootMessage (GAP-042) |
| `make test` → `panic: test timed out after 2m0s` (internal/handler) | build | GAP-037 (already on board, verified first-hand) |

## 4. Time-to-first-success & friction

- **API:** JWT gen → list trees → create tree: **~15 s** total (server responds in ms).
- **Browser:** fresh load → tree view with 11 nodes rendered: **~8 s**.
- **Friction count: 8** (3 API dead ends, 2 CLI failures, 3 UI dead ends incl. the
  broken create dialog, topic dialog UUID requirement, no fork affordance).
- Had to read source to proceed: 3× (route wiring for fork/export, topics schema,
  CLI create payload) — each is a docs gap.

## 5. What I'd fix first (1 hour of maintainer time)

1. **GAP-040 (P0):** make the UI create-tree dialog send `nodeType:"message"` (or
   default it server-side) and either send a default root message or label the field
   required. This unblocks every new user.
2. **GAP-042 (P1):** add `--content` to `canopyd tree create`.
3. **GAP-041 (P1):** fix INTEGRATION.md §6 fork path (2-line doc fix).

## 6. Verdict

🟡 **PROMISING-BUT-ROUGH.** The differentiated core — context manifest with token
budget (`74 / 8,000 tokens` in the UI), DAG canvas, SSE live sync, MCP agent access —
genuinely works and is visible in real use. But a brand-new user's first action
(create a tree) fails in the UI, and the documented CLI/curl paths have drifted.
Value is real; usability is the blocker.
