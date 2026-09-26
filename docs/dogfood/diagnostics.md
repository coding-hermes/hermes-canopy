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

## 5. 2026-08-27 update — the gateway era (GAP-050/051)

The project's center of gravity moved: Canopy is now a **client of the live Hermes
gateway** (`hermes gateway run`, :8642). New architecture facts learned by poking it:

- **Deployment is a systemd user unit, not the repo binary.** `canopy-canopyd.service`
  runs `/home/kara/bin/canopyd` (manually copied), `canopy-vite.service` runs the
  Vite dev server. `EnvironmentFile=/home/kara/.hermes/.env`; gateway config via
  `HERMES_WEBUI_GATEWAY_BASE_URL` + `HERMES_WEBUI_GATEWAY_API_KEY` (API_SERVER_KEY
  fallback). **There is no deploy script** — after pulling backend code you must
  `make build && cp bin/canopyd /home/kara/bin/ && systemctl --user restart
  canopy-canopyd` or the live stack silently runs a stale binary. This bit us:
  the 04:05 binary predated the GAP-050 commits by ~10 min → the whole gateway
  surface 404'd for ~7h while the board said complete (GAP-052).
- **Gateway surface** (`/api/v1/gateway`): status, runs (registry), POST runs
  (starts REAL gateway runs, 202 + run_id), GET run, SSE events (bounded history
  replay + live fan-out, subscribe-before-replay so no event is missed), stop,
  approval. The API key never reaches the browser. Verified live end-to-end.
- **The run registry is in-memory** (`internal/gateway/service.go` — `runs
  map[string]*RunRecord`): a canopyd restart wipes all run history even though the
  gateway retains runs (GAP-054). Stop on a terminal run → 404 run_not_found
  (misleading; the run IS in the registry).
- **Field-casing split (the #1 API trap):** tree-create and topics take camelCase
  (`contentFormat`, `nodeType`, `treeId`); ALL node endpoints take snake_case
  (`content_format`, `node_type`, `parent_id`) with `DisallowUnknownFields()` —
  camelCase on a node endpoint returns 400 "request body must be valid JSON" even
  though the body is valid (GAP-053). The frontend (`lib/composer.ts`) correctly
  uses snake_case; the docs' tree-create example uses camelCase — a user following
  the example then hitting reply gets the misleading error.
- **contentFormat accepts only `markdown`** (default). `"text"` → 400 "invalid
  content format" (GAP-055). API.md says "string (optional)" with no enum.
- **E2E battery (61/61) has ZERO gateway coverage** — it passed while the gateway
  surface 404'd. The visual-regression goldens were re-baselined for the
  demo-tree removal (GAP-051); the demo tree is now E2E-only and was deleted from
  the live DB (0 trees → honest empty state).

## 6. The right way (2026-08-27 additions)

- **Probing the gateway:** `GET /api/v1/gateway/status` first — if 404, the
  deployed binary is stale (GAP-052); check `stat /home/kara/bin/canopyd` vs
  `git log -1 --format=%ci` of the gateway commits.
- **Starting runs:** POST `/gateway/runs` `{"message": ...}` — real tokens, keep
  prompts tiny; stop long runs with POST `/gateway/runs/{id}/stop` (works while
  running; 404 on completed runs is the misleading GAP-054 behavior).
- **Node bodies:** always snake_case (`content_format`, `node_type`, `parent_id`).
  Tree-create and topics: camelCase. When in doubt, read the struct tags in
  `internal/handler/node_handler.go` — they're the ground truth.
- **Data hygiene:** my scratch trees (dogfood-2026-08-27, UI-created tree,
  CLI-created tree) were deleted via API after the run; the canonical DB on :5437
  is the E2E/seed DB — don't leave scratch data in it.

## 7. 2026-09-10 update — the two oldest mysteries solved

Two long-standing "known quirks" turned out to be **one missing setup step and
one never-mounted route**:

- **"Fresh DB makes tree-create 503"** (recorded since the 09-04/09-07 runs as
  a hygiene note) is now root-caused: the tree-create tx inserts
  `tree_members`, whose FK needs a `users` row for the JWT subject
  (`00000000-…-0001`). Nothing provisions that user at runtime — the live DB
  has the row only because `scripts/seed-demo-data.sql` was run manually after
  the tick-416 wipe. The service then flattened ANY tx error to
  `503 SERVICE_UNAVAILABLE "database unavailable"` **and the handler's error
  log line went to a context logger that wasn't wired, so the server log
  stayed silent** (that's also why DF-HERMES-CANOPY-3's "topic 500 with zero
  logs" happened — same silent-logger family). Reproduced in psql by
  replaying the tx statements: insert into `trees` works, insert into
  `tree_members` fails with `tree_members_user_id_fkey`. → GAP-064.
  **Both halves are FIXED as of 2026-09-12 (f96467a):** the dev JWT user is
  provisioned at startup when `JWT_SECRET` is the dev default, and
  `hlog.NewHandler(log.Logger)` is wired as the first global middleware
  (`internal/server/server.go:224`) — handler/tx errors now reach the server
  log.
- **The tree-scoped `/reply` route never worked.** `NodeHandler.Routes()`
  registers `POST /nodes/{node_id}/reply`; only `TreeRoutes()` is mounted
  (server.go:164), and it has list/create/get/fork only. `git log -S
  handleReply` shows reply was only ever added in `Routes()` (bcc17b2, BE-04).
  Even the usage skill v2.0 "verified" it — that verification was wrong
  (08-27 probably exercised the UI composer, which uses node-create +
  `parent_id`, and the claim was copied into docs). The reply mechanism that
  actually exists is good: `POST /trees/{t}/nodes` with `parent_id` +
  `edge_type` → 201 `{node, edge}` + SSE broadcast. → GAP-065.
- **Deploy drift is now structural, not incidental** (GAP-052's class):
  `make deploy` exists and is good (build → atomic install → restart →
  /health poll → gateway smoke), but it only runs when someone remembers.
  The live binary sat 7 days behind HEAD again (09-02 vs 09-03 FTR-05 P7),
  so `/health/relay` 404'd while the board said FTR-05 SPEC COMPLETE. The
  systemic fix is a check, not a memory: a cron/board gate comparing
  `stat -c %Y /home/kara/bin/canopyd` vs `git log -1 --format=%ct -- internal/ cmd/`
  and flagging staleness >24h. → GAP-067.

### The right way, updated

- Writing Go: service tx errors collapse into `ErrDatabaseUnavailable` — when
  you see a 503, suspect a constraint (FK/CHECK) inside the tx, not the DB
  being down. Reproduce the tx SQL in psql before touching Go code.
- Routing: **`internal/server/server.go` mounts are the only truth.** Handlers
  can register routes that are unreachable (dead `Routes()`). Grep the mount
  before trusting API.md — or `chi` route-print if it ever gets added.
- Logging: `log.Ctx(r.Context())` now resolves to the server logger —
  `hlog.NewHandler(log.Logger)` is the first global middleware
  (`internal/server/server.go:224`, wired by GAP-064 / f96467a), so handler
  error logs DO appear. The mechanism was absent before GAP-064: logs from
  before that fix are silent for handler-level errors, so when reading OLD
  server logs, an error class that never appears went through the
  then-unwired context logger.

### 2026-09-12 update — dogfood rows closed

Every friction reported by the 2026-09-10 dogfood run (and the older "known
quirks" above) is now fixed at HEAD. Symptom → fix commit → what a user
sees today:

- **Fresh-DB 503 on every write + silent server log** (trap #1) → f96467a
  (GAP-064, closed tick 430): the dev JWT user is provisioned at startup
  when `JWT_SECRET` is the dev default and `hlog.NewHandler` is wired first
  — a fresh clone now works out of the box, and handler/tx errors show up
  in the server log.
- **Topic create returned a zero-UUID `root_node_id` despite valid input**
  (DF-HERMES-CANOPY-2 / GAP-066) → 9604690 (closed tick 431): the root node
  is preserved in summaries — `POST /api/v1/topics` now returns the real
  root node id.
- **The documented `/reply` route 404'd — never mounted** (trap #2,
  GAP-065) → cc581a4 (closed tick 429): the route is mounted on the real
  router, guarded by `route_parity_test.go` — the documented path answers
  201.
- **Compose quick start failed: `env file .../.env not found`** (trap #3,
  GAP-068) → ce3e1dc (closed tick 432): `env_file` is optional and the docs
  carry `cp .env.example .env` — `docker compose up -d --build` works on a
  fresh clone.
- **Deploy drift: the live binary sat days behind HEAD while the board said
  complete** (GAP-067) → ceb4e68 (closed tick 433): stale-canopyd detection
  and remediation is automated — staleness is caught by a check, not a
  memory.
- **Topics quick-reference drift + SPA deep links not serving** → 46b356d
  (DF-HERMES-CANOPY-4, closed tick 435): the topics quick reference matches
  the API and SPA deep links serve the PWA.

### 2026-09-14 update — the outage dogfood (GAP-069..072)

The 09-12 update above closed every prior trap, and the board shipped the
file-viewer subsystem (SPEC-PL-02 phases 1–9, migrations 43–46). This run —
the first that found the service fully DOWN — shows how the *class* survives
even when each *instance* is fixed:

- **How the system is built:** `canopyd` embeds its migrations
  (`migrations/embed.go`); on start it compares `binary_embedded_version`
  with the DB's `schema_migrations.version` and REFUSES to start on a newer
  DB (STALE BUILD). Any process that runs migrations against a DB therefore
  controls which binaries may serve it.
- **How it broke:** migrations 43–46 landed in 4ac2f15 (09-12 20:27); the
  E2E battery / foreman run that night pointed at the SHARED live DB (docker
  `canopy-pg`, host :5437) and migrated it to 46. The deployed binary
  (/home/kara/bin/canopyd, built 09-11, embeds 42) then refused every start
  — forever, silently, ~5s cycle, ≈27k restarts over ~37h.
- **Why nobody saw it:** (1) the GAP-067 deploy-checker is a daily oneshot;
  it DID exit 2 (STALE_BLOCKED, dirty worktree) on 09-13 15:34 — correct
  refusal, zero follow-through, next attempt 24h later (GAP-070); (2) no
  OnFailure/alerting on the service unit; (3) the "deploy" mental model in
  the fleet ("ExecStartPre rebuilds from HEAD" elsewhere) does not apply
  here — canopyd ships via `make deploy` only, so staleness is a real
  failure mode, not a self-heal.
- **The right way (for future agents):**
  - E2E/foreman NEVER touch :5437. Stand up a throwaway postgres
    (`docker run … -p 127.0.0.1:<port>:5432 postgres:16`) or a dedicated
    test DB; the compose file already shows the pattern.
  - After any change touching `internal/`, `cmd/`, `migrations/`: run
    `scripts/check-deploy-staleness.sh` and, when the live service matters,
    `make deploy` (clean worktree; it builds → atomic-installs → restarts →
    polls /health → runs scripts/smoke-gateway.sh).
  - Diagnosing "service down": `systemctl --user status canopy-canopyd` +
    `journalctl --user -u canopy-canopyd -n 20`; STALE BUILD names both
    versions (`binary_embedded_version=42 db_schema_version=46`) — the fix
    is a current binary, never a DB rollback.
  - New subsystem checklist (learned from GAP-071): routes → docs/API.md +
    README quick-ref BEFORE merge; anything keyed to an acting profile needs
    a fresh-DB story (dev-seed or auto-provision), because tree-create only
    provisions `users`, and `profiles`/`profile_route`/`workspaces` rows do
    not exist on clean installs.
- **Verified-fixed confirmations from this run:** GAP-068 (compose without
  .env) and GAP-066 (topic root_node_id) re-tested at HEAD 9bbe3dc — both
  hold on a fresh clone + fresh DB.

## 8. 2026-09-20 update — the collab/MLS surface is a façade on real plumbing

**What this section teaches:** the difference between "the plumbing works" and
"the feature works". Canopy's multi-user stack is both at once: the guards,
envelopes and lifecycle state machine are real code doing real things, while
the two flagship promises (MLS encryption, SPEC-API-06 invites) are shape
without substance. A user asking "can two people collaborate in Canopy?"
deserves this distinction, not a test-suite color.

**How the surface is built.** Three disjoint identity stores: `users` (JWT
sub-keyed; only `…0001` auto-provisioned by dev bootstrap), the `profiles`
table (UUID-keyed, MLS's FK target, no API path), and `profile_route`
(name-keyed per-workspace routing rows — what the "profiles" API actually
writes). Workspace membership lives in `workspace_members` (invites =
random tokens, 7-day TTL). Tree sharing adds `tree_members` rows via an
undocumented `/trees/{t}/share` route. Channels are an in-memory registry —
global, not workspace-scoped, no persistence.

**The MLS lesson (the one worth repeating).** `internal/mls/service.go`
passes an interface-shaped test suite while being unable to deliver a message
between two members: Encrypt keys on the sender's public key, Decrypt on the
recipient's — so every ciphertext has exactly one reader, its author. The
envelope types, cipher-suite strings and `mls_ciphertext_v1` wire formats are
faithful; the substance is per-sender AES-GCM with keys stored in plaintext
next to the data they "protect". Errors hit live: cross-decrypt →
`mls: gcm open: cipher: message authentication failed` (500, not even a
4xx); epoch-bump-on-join invalidates in-flight ciphertexts
(`ErrEpochMismatch`); commit-proposals bumps the epoch with zero proposals.
**Right way:** judge "shipped" crypto by a cross-principal round-trip test
(A encrypts → B decrypts), never by schema/shape parity — the same lesson as
the burndown-chart JSON-island case: read the payload, not the picture.

**The onboarding lesson.** A signed JWT authenticates (200 on reads) but the
user's first write dies on `workspaces_owner_id_fkey` with a silent 500; the
fix (a `users` row with `hermes_user_id = sub`) exists only in
bootstrap.go source. GAP-064 fixed exactly this for user `…0001`; the
multi-identity surfaces grew without inheriting the provisioning story.
**Right way:** any surface keyed to an identity needs (a) auto-provision on
first authenticated use or (b) a documented seed path — and FK failures on
identity rows should map to a 4xx that names the missing row.

**Install leg (what held up).** Fresh clone (documented public GitHub URL) →
`docker compose up -d --build` → RC=0 in 153s → `/health` 200 schema 47/47 →
authed API 200 on a virgin DB. GAP-068 (no `.env` needed) still holds. New
friction: docs assume a root-owned docker.sock; rootless layouts need
`DOCKER_HOST` (undocumented). Compose-path users don't need `go 1.25+`.

**What actually works (so nobody re-learns this):** workspace CRUD with
correct 401/403/404 envelopes; invite-token join (role 1 vs owner role 2);
live cross-user channel delivery over SSE (event `channel_message` with full
payload within one second); tree share → grantee node writes visible to the
owner; MLS group create/join/leave/epoch mechanics and the
`mls:welcome_message` SSE broadcast.

## 2026-09-22 — MCP endpoint + production proxy (why they break, and the right way)

**How the MCP endpoint is built.** `internal/handler/mcp_handler.go` is a
single-file JSON-RPC 2.0 dispatcher over plain HTTP POST: `handleJSONRPC`
checks notifications FIRST (they must never get a JSON-RPC body — they get
202), then version, then a method switch (`initialize` / `ping` /
`tools/list` / `tools/call`). Seven `toolDef`s carry JSON-Schema-ish
`inputSchema` blocks, and `dispatchTool` maps names to thin service calls.
Stateless by design: no session, no `notifications/initialized` tracking, so
any request stands alone. That design is why hand-rolled curl testing feels
flawless — every call is independent and returns HTTP 200 with plausible JSON.

**The error this run hit.** The official `modelcontextprotocol` Python SDK
(2.2.0, streamable-HTTP client) rejects every `tools/call` RESULT with
`CallToolResult: content Field required`. The spec's result envelope is
`{"content":[{"type":"text","text":"..."}], "isError":false}`; canopyd's
handlers return the bare domain object (`{"trees":[...]}`). Nothing in the Go
tree can see this: the server-side unit tests assert the shapes canopyd
itself chose. Lesson: an MCP server is only "working" if a reference client
accepts it — the interop canary belongs in CI, not in human curiosity
(DF-45). The right way to test any MCP endpoint: wire the official SDK in a
throwaway venv (`uv pip install mcp`), `streamable_http_client` +
`ClientSession.initialize()` + one `call_tool` per tool.

**Auth subtlety hit on the way:** the SDK's streamable client takes no
`headers=` argument in 2.2.0 — pass an auth-aware `httpx.AsyncClient` as
`http_client=`. The unpack is 2 values (read, write), not 3 like SSE.

**How the proxy auth is built.** `deploy/reference-proxy.py` threads every
request through `_handle`: Basic gate (`_authorized`, constant-time compare)
→ `/api`+`/health` go to `_proxy`, everything else gets the SPA fallback.
`_proxy` copies client headers minus hop-by-hop, then injects
`Authorization: Bearer <jwt>` ONLY if the client sent none. The flaw: the
Basic gate VALIDATES the credential but leaves it in the header set, so the
injection check sees "client sent Authorization" and skips. Documented recipe
(README production auth) + implementation = guaranteed 401. Lesson: a gate
that CONSUMES a header must then REMOVE it before the downstream sees it —
and the combination test (gate+token) is the one that matters, because each
behaviour works alone (DF-46). Proven by isolation: gate-less proxy injects
fine; Basic without gate passes through and blocks injection identically.

**Debug technique that paid twice:** run one arm with each factor removed.
Arm A (no gate, token, no client auth) → injection works. Arm B (client Basic,
no gate) → blocked. That 2×2 localised the defect to the gate/passthrough
interaction in minutes without touching a debugger.

**MCP-vs-REST asymmetry (DF-48).** REST `create_node` runs a membership
precheck (403 `NOT_TREE_MEMBER`); the MCP dispatch calls the service directly,
so a bogus tree reaches Postgres and comes back as FK 23503, which the service
maps to `database unavailable`. Two layers drifted: the handler-level gate is
not part of the service contract. Lesson: when a resource has TWO entry
points, the error contract has to live in the service layer, or each entry
point teaches clients a different truth.

**Install leg (source path, what held up).** `make build` on a bare Debian 13
agent: 152 s from empty module cache (pure-Go deps only — no cgo), binary
42 MB, runs, migrates a virgin Postgres 16 to schema 48, serves MCP + CLI
writes. The gap is not the build, it's the bootstrap: docs assume `go 1.25+`
on PATH, a root-owned docker.sock, and a fast clone. A frustrated user's
reality: `--depth 1` (408 s vs >15 min abandoned), userland Go tarball into
`$HOME`, rootless dockerd reachable only via `~/bin` + `XDG_RUNTIME_DIR`
(the unit exists but `systemctl --user` may fail to see it; `dockerd-rootless.sh`
needs `/usr/sbin` on PATH for `sysctl`). Also: `/tmp` on shared hosts carries
other users' residue — redirect logs to `$HOME` (hit "Permission denied" on a
pre-existing `/tmp/build.log`). None of this is code; it's a
"non-Docker install" doc section waiting to be written.

**Numbers worth keeping:** shallow clone 408 s; build 152 s; PG ready 2 s;
canopyd healthy 3 s; CLI create+list on the fresh box pass. Warm ops: MCP
read 12.7 ms, REST read 12.3 ms, MCP write 19.8 ms (hyperfine, 30 runs).
Cold restart to healthy 0.160 s — the sub-30s resume promise is not even
close to binding.

## 2026-09-23 — topics, #references, export/import (9th run)

**FK domains differ per table — and one handler passed the wrong one (DF-50).**
Canopy has two identity domains: `users.id` (auth; the JWT `sub`) and
`profiles.id` (display identity; e.g. the dev-seeded "Dev Hermes" row carries a
time-ordered UUID, not `000…001`). `node_resolved_refs.resolved_by` was
declared `REFERENCES profiles(id)` (migration 000021), while
`NodeHandler.resolveReferencesAtSend` forwards the JWT sub as `resolvedBy`.
Postgres rejects the insert (23503), `ResolveAtSend` logs a WARN and returns —
by design, "non-fatal" — so the feature degrades to a no-op that still
answers 201. Lesson: when a column FKs a *different* identity table than the
one the auth layer speaks, the type system won't save you — the names even
look compatible (`resolved_by uuid NOT NULL`). The cheap gate is an
integration test asserting `count(node_resolved_refs) == expected` after a
create with a valid `#ref`; no unit test can see a cross-table FK mismatch.

**Swallow-and-continue turned a P0 into a silent feature death.** The same
spec says resolution failures must not block the message — correct product
behavior — but the implementation dropped the second half of the contract:
nothing surfaces the loss to the user or the operator. The node saves, the
API returns 201, the WARN is one line in a JSON log, and the resolved-refs
surface (queries + `reference_resolved`/`reference_not_found` SSE) just
never has data. The compile path then *re-parses* node content at read time
and injects topic boundaries anyway, so every demo, test, and the 09-21
human-path run saw references "working". Two coupled lessons: (1) a
best-effort path needs an observable degraded signal (SSE, metric, badge —
anything louder than a log line); (2) when the read path recomputes what the
write path persists, tests written against the read path can never see the
write path dying. Probe the destination store, not the echo.

**Edgeless fixtures hid the import break (DF-51).** `ImportTree` inserts
edges without `sequence_num`; the column is NOT NULL with no default, so any
tree with ≥1 edge 503s (mislabelled "database unavailable" — the DB is
healthy, the INSERT is wrong). Every round-trip fixture was a root-only
tree, which imports 201 perfectly — green suite, dead feature. The 09-20
boardctl pitfall applies again: an export payload that carries a field
(`sequenceNum`) the importer never binds is exactly the "struct JSON tags
are an API nobody tests" class. A round-trip test needs one reply edge in
the fixture — the most common edge in any real conversation.

**Why the topic/reference run still rates PROMISING-BUT-ROUGH, not worse.**
The read/compile side is genuinely strong: search with `<mark>` highlighting,
preview, autocomplete, resolve (with honest `not_found[]` and archived-topic
reporting), inject, and a compiler that injects referenced topics with
per-reference token counts, `manifestHash`, and transparent omissions
(`omittedReason:"depth"`) — all at 1–15 ms. At 201 nodes: create 20 ms/node,
compile 10–19 ms, cold restart 0.20 s, export 160 KB in ~2 ms. Nothing was
slow enough to file a PERF row; the value lost is correctness (DF-50/51),
not speed.

**Install leg, third sampling, second verdict (DF-55).** The 09-22 run
proved source build works once Go exists (152 s, no cgo). This run proved
the *undocumented* path is the only one a toolchain-less fresh user has:
release binary download (5 s) → healthy server (<1 s) → CLI create/list.
`make build` without Go fails before any project code runs, and SELF_HOST's
`sudo mv` example fails for the exact user it addresses. Numbers worth
keeping: clone --depth 1 22 s; binary 5 s; serve→health <1 s; virgin-DB
`/health` reports `schema_version:0` until a later boot (embedded 48) —
cosmetic but confusing against the STALE BUILD guard's 48/48 message.

## 2026-09-24 — cards + search (10th run)

**The card store is a parallel universe beside Postgres — and that's fine,
once you know.** Cards don't live in the graph database at all: each card type
gets its own SQLite file under `CANOPY_CARD_DATA_DIR` (per-type
database-per-card architecture from SPEC-PL-03), while trees/nodes/topics stay
in PostgreSQL. Practical consequences a user learns the hard way: (1) the
`canopyd card export` CLI is the only card surface that reads those files
in-process — it never contacts the server, so export a card store BEFORE
switching data dirs or you'll export an empty one; (2) backing up Postgres
does NOT back up cards; (3) deleting a tree does not cascade to its cards —
the card rows keep their tree_id either way. The byte-deterministic JSONL
export (same store → same bytes, proven across a restart) is the intended
git-diff story — use `--snapshot-dir` on a timer and cards become a
git-versioned append log, which is exactly the GAP-083 pattern.

**Optimistic concurrency is real — and the error body is the re-sync
mechanism.** Every card PATCH/DELETE demands `If-Match: <revision>`; get it
wrong and the 412 response carries the CURRENT card snapshot, so the correct
client loop is: read revision → PATCH with If-Match → on 412, re-read
`revision` from the conflict body (not the stale GET cache) and decide again.
The status machine rides on the same mechanism (active→dismissed→active,
→archived terminal), and PATCH/DELETE enforce it faithfully — which is what
made DF-57 visible: the actions route is the ONE mutator that skips the status
gate, so a "terminal" archived card happily recorded two more action events
while refusing a simple PATCH with 409. When one route in a family lacks the
guard the siblings have, the asymmetry is the bug; find it by doing the same
mutation through every door.

**A search index nobody feeds is a search feature nobody has (DF-58).** The
topic search SQL has a beautiful second arm: a UNION over
`topic_node_content_search` with `ts_rank` on node text, so node content
*should* match. But that table is populated only by
`refresh_topic_node_content_index()`, whose only callers in the entire repo
are tests. Live proof: 3 topic-member nodes, 0 rows in the content index,
`q=<word that appears verbatim in a node>` → zero results while the preview
endpoint happily shows that node. Two lessons compound here: the schema-level
test green (trigger exists, function exists) says nothing about the runtime
write path, and "search works" was true only for the title/description arm
the trigger DOES feed. If a feature is a UNION of two arms, test that each
arm independently returns rows — and grep for production callers of any
"refresh" function, not just its existence.

**Casing is a per-endpoint decision — never assume.** Trees, cards, topics
create bodies are camelCase (`rootMessage`, `treeId`, `rootNodeId`); node
create is snake_case (`parent_id`, `node_type`) — sending `parentIds` (the
shape the tree-create response itself uses for edges) fails with
`INVALID_BODY unknown field`. Card responses say `"type"` where docs say
`card_type`. The failure modes are at least loud (400 naming the field), but
a consumer writing one typed client for "the Canopy API" will trip on it
repeatedly. The right way: curl the endpoint with the docs' exact body first,
and treat the response body — not the docs — as the JSON contract of record
until DF-60 lands.

**A typed-struct HTTP client dies on fields the SERVER owns, not fields you
added (2026-09-26 gateway round).** canopyd is a client of the live Hermes
gateway, and its observer died mid-run on `"error": false` — a bool riding a
field the client had declared as `string` (`RunEvent.Error`,
internal/gateway/sse.go). Note who owns the mismatch: canopyd's own comment
block even documents `tool.completed — (tool, duration, error)` from the
gateway source, and the live gateway (verified 2026-09-26) emits `"error":
false` on success. The unit-test stub (internal/gateway/service_test.go
newGatewayStub) only ever sends string errors, so the suite is structurally
blind to the real shape. Lessons: (1) when a client consumes a LIVE foreign
service, capture one REAL event stream of each event type and build fixtures
from the captures — a hand-written stub encodes the client author's
assumptions, not the peer's contract; (2) decode tolerant, not strict —
`json.RawMessage` (or a bool-or-string flexible type) for fields like
`error` whose type the peer is free to vary by outcome; (3) the failure mode
is nasty precisely because the happy-path tests pass: a tool-less run
completes perfectly, so "chat works" is demonstrable in a demo while every
real prompt (agents `ls` before answering) dies. See DF-HERMES-CANOPY-66 for
the capture (`run_c910ab4bc69547bc8fddbfc3c13df034` raw SSE).

**Run output that never lands in the domain model is demo-visible value
loss (2026-09-26).** The GAP-096 audit dialog is the best artifact in the
repo — a visible manifest with hash, budget and included sources before
send — and the run genuinely completes; but the answer exists only in the
transient run registry (pruned at maxRuns) because nothing bridges
`run.completed` into a tree node or node metadata. The conversation DAG
records the human's message and silently drops the agent's reply. When a
feature's data flow ends in a side registry, ask "what reads this a week
from now?" — if the answer is only a pruned list endpoint, the feature is
half-wired regardless of green tests (DF-HERMES-CANOPY-67).
