# Hermes Canopy — Diagnostics Trail (2026-08-17 dogfood run)

*How the system is built, why, the errors encountered during real use, and the right
way to do things. Written from a real user's perspective, not raw logs.*

## 1. How it's built (what a user learns by poking it)

- **`canopyd`** = one Go binary: REST/SSE API on `HTTP_ADDR` (dev `:8091` to match the
  Vite proxy; default `:8080`). Config is 100% environment variables (no server flags
  besides `-version`). Migrations are embedded and auto-run at startup.
- **Frontend** = React+TS+Vite PWA on `:5173`; Vite proxy injects a dev HS256 JWT
  (secret `dev-secret-change-me`, sub `...0001`) so a dev user never authenticates.
- **Routing quirk (the big one):** node routes are mounted TWICE —
  `/api/v1/trees/{tree_id}/nodes` (tree-scoped, membership-gated) and
  `/api/v1/nodes` (flat). The flat mount prepends `nodeHandler.Routes()`, whose
  patterns already start with `/nodes/...`, so flat update/reply/fork/delete live at
  **`/api/v1/nodes/nodes/{node_id}/...`** — the double `nodes` segment. This is
  deliberate (BUG-025 fixed authz on it) and correctly documented in `docs/API.md`,
  but it's invisible in `docs/INTEGRATION.md` §6, which shows the single-segment path
  that 404s. When probing any node endpoint, try both mounts.
- **Core promise — context compiler:** `GET /api/v1/context/{node_id}` walks node
  ancestry, estimates tokens per node, and returns `{content, manifest:{tokenBudget,
  tokensUsed, ancestry:[{id,kind,title,tokenCount,truncated}]}}`. In the UI, clicking
  a canvas node opens a "Context" panel showing `N / 8,000 tokens`. This is real and
  it works end-to-end (WIRE-002).
- **MCP:** `POST /api/v1/mcp` speaks JSON-RPC 2.0 (`tools/list` → list_trees,
  get_tree, create_node, list_topics, ...). Agents can drive Canopy through it.
- **Topics:** list requires `?tree_id=`; create body is camelCase
  `{treeId, rootNodeId, title, description}`. UI "New Topic" dialog demands a raw
  Root Node ID UUID.
- **Fork:** service requires the source node to already have ≥1 child
  (`ErrForkRequiresChildren` — unit-tested, deliberate). "Fork" creates a child of
  the target; it is NOT exposed in the UI at all.

## 2. Errors I hit (the ones that cost time) and the right way

1. **`POST /api/v1/nodes/{id}/fork` → 404.** Right way: `/api/v1/nodes/nodes/{id}/fork`.
   Lesson: when a documented endpoint 404s, check `internal/server/server.go`
   mounts before blaming the binary.
2. **UI Create Tree → 400 twice.** The dialog sends
   `{title, rootMessage:{content, contentFormat}}` — no `nodeType`, and no
   `rootMessage` at all when left blank. The API (since GAP-008) requires
   `rootMessage.content` and `rootMessage.nodeType`. The UI labels Root Message
   "(optional)" — it isn't. Right way: the dialog must send `nodeType:"message"`
   and either default content or be marked required. **This is why no test caught
   it: E2E asserts the "New Tree" button exists but never submits the dialog.**
3. **`canopyd tree create "X"` → VALIDATION_ERROR.** cli.go's `treeCreateRequest`
   only has `title`. The API grew a required `rootMessage` (GAP-008) and the CLI
   never grew with it. Right way: add a `--content` flag; also `--help` on
   subcommands is unhandled (`tree create --help` tries to create a tree named
   "--help").
4. **`GET /api/v1/topics` → MISSING_TREE_ID.** Not a bug — a parameter. But nothing
   in INTEGRATION.md tells you. Right way: `?tree_id=` on list; camelCase body on
   create.
5. **`GET /api/v1/export` → 404.** Export was wired directly on the trees router:
   `GET /api/v1/trees/{id}/export`, `POST /api/v1/trees/import` (server.go comment
   explains why it's not under `/export`). API.md documents this; INTEGRATION.md
   doesn't mention export at all.
6. **`make test` → `panic: test timed out after 2m0s` (internal/handler).** The
   Makefile `test` target uses `-timeout=120s`; the handler package alone takes
   ~140s+ (live-PG integration tests). Green code, red build command (GAP-037).

## 3. The right way (patterns that work)

- **Probing endpoints:** use `docs/API.md` (accurate, includes the double-nodes note
  and export paths) over `docs/INTEGRATION.md` (walkthrough has drifted).
- **Real-use browser flows:** trees are clickable cards (not links); composer is
  `textarea` + `button[aria-label="Send message"]`; canvas nodes are
  `.react-flow__node`; context panel appears on node click.
- **Auth:** dev JWT via README one-liner, or the hardcoded token in INTEGRATION.md
  §6. Public: `/health`, `/healthz`, `/version`.
- **Data hygiene:** my scratch tree/topic were deleted via API (204) after the run —
  the canonical DB on :5437 carries 3,600+ trees (E2E seeds, session imports);
  visual-regression selects the "UI-02 Rail Demo" tree so new trees don't drift
  goldens.

## 4. Project's own history worth knowing

The board (`.coding-hermes/board/tasks.jsonl` canonical) shows a heavily
self-audited project: ~160 completed tasks across backend (BE-*), frontend (FE-*,
UI-*), integration (INT-*), hardening (BUG-*), and docs (GAP-*). Recurring themes:
API-contract drift between layers (GAP-008 payload, BUG-023 double mount, GAP-041
docs drift), test-suite speed vs CI timeouts (GAP-003, GAP-037, GAP-039), and
"all tests green but the real flow is broken" (this run's GAP-040 — the exact
premature-completion pattern this dogfood loop exists to catch).
