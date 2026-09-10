# Dogfood Log — Hermes Canopy

## 2026-08-27 — Deep real-use run (cron dogfood) — gateway era

- **Verdict:** 🟡 PROMISING-BUT-ROUGH
- **Promise:** "Canopy is the live interface of Hermes — the Dashboard shows real
  gateway runs, the chat composer starts REAL agent runs, SSE streams events, and
  approvals resolve; plus the original DAG/context-manifest surface."
- **Time-to-first-success:** API ~15 s (JWT → gateway status → start real run);
  browser ~3 s (load → Dashboard with live gateway data).
- **Friction count:** 4 (stale-binary 404, camelCase/snake_case trap, misleading
  stop-404, undocumented contentFormat enum).
- **Top 3 findings:**
  1. **GAP-052 (P1)** — deployed `/home/kara/bin/canopyd` (built 01:52) predated
     GAP-050 gateway commits (01:57–02:01) → whole `/api/v1/gateway` surface 404'd
     ~7h while board said complete + E2E 61/61 passed (zero gateway coverage).
     No deploy script, no post-deploy smoke test.
  2. **GAP-053 (P1)** — API field-casing split: tree-create/topics camelCase, node
     endpoints snake_case with DisallowUnknownFields → camelCase on node endpoints
     returns misleading 400 "request body must be valid JSON".
  3. **GAP-054 (P2)** — gateway run registry in-memory: restart wipes history;
     stop on completed run → misleading 404 run_not_found.
- **Also filed:** GAP-055 (API.md contentFormat "string (optional)" no enum; only
  `markdown` accepted).
- **Verified FIXED since 08-17:** GAP-040 (UI Create-Tree works — created tree via
  browser), GAP-041 (INTEGRATION.md §6 fork path works), GAP-042 (CLI create +
  --help work), GAP-043 (fork rule enforced), GAP-045 (CLI navigate hierarchy).
- **What works (real evidence):** gateway status/runs/start/SSE/stop end-to-end
  (real Hermes runs, "ui-probe-ok" via browser composer, zero console errors);
  context manifest (28/8,000 tokens); tree/node/reply/fork/graph; CLI; UI create.
- **Left behind:** docs/dogfood/2026-08-27-integration.md · docs/dogfood/diagnostics.md
  (updated §5-6) · skills/hermes-canopy-usage/SKILL.md (v2.0) · board rows
  GAP-052..055 (tasks.jsonl + tasks.md section).
- **Foreman:** not woken (cooldown 7200s < 14400s; 4 new pending tasks on board —
  it will pick them up on its normal cycle).

## 2026-08-17 — Deep real-use run (cron dogfood)

- **Verdict:** 🟡 PROMISING-BUT-ROUGH
- **Promise:** "A user can run the local server + PWA, create a tree, post/branch/synthesize messages, see a visible context manifest, and use topics/cards — resuming work in <30 seconds."
- **Time-to-first-success:** API ~15 s (JWT → list → create tree); browser ~8 s (load → tree view with 11 canvas nodes).
- **Friction count:** 8 (3 API dead ends, 2 CLI failures, 3 UI dead ends).
- **Top 3 findings:**
  1. **GAP-040 (P0)** — UI Create-Tree dialog always 400s (rootMessage missing nodeType; "(optional)" label wrong) → new users cannot create a tree in the UI. E2E never submits the dialog.
  2. **GAP-041 (P1)** — INTEGRATION.md §6 fork walkthrough 404s; real route is `/api/v1/nodes/nodes/{id}/fork` (API.md correct).
  3. **GAP-042 (P1)** — CLI `canopyd tree create` always fails (no rootMessage sent); `--help` mishandled.
- **Also filed:** GAP-043 (no UI fork affordance; leaf-fork 400 undocumented), GAP-044 (New Topic dialog demands raw node UUID; topics schema absent from INTEGRATION.md), GAP-045 (CLI navigate output flat/ambiguous).
- **Verified first-hand (already on board):** GAP-037 — `make test` fails (handler "test timed out after 2m0s", EXIT=2).
- **What works (real evidence):** context manifest API + UI panel ("74 / 8,000 tokens"), DAG canvas render + SSE, composer→canvas sync (BUG-032), MCP tools/list + create_node, topics/cards/export APIs, JWT auth.
- **Left behind:** docs/dogfood/2026-08-17-integration.md · docs/dogfood/diagnostics.md · skills/hermes-canopy-usage/SKILL.md · board rows GAP-040..045 (tasks.jsonl + tasks.md section).
- **Foreman:** not woken (cooldown 21600s < 43200s; board has 3 pending stand-in tasks + 6 new dogfood tasks — it will pick them up on its normal cycle).
2026-09-01 | PROMISING-BUT-ROUGH | 20s t2fs | friction 11 | 5 findings
2026-09-04 | PROMISING-BUT-ROUGH | 25s t2fs | friction 9 | 5 findings

2026-09-07 | PROMISING-BUT-ROUGH | 30s t2fs | friction 10 | 5 findings

# Dogfood Log — Hermes Canopy

## 2026-09-10 — Deep real-use run (cron dogfood) — fresh-DB forensics

- **Verdict:** 🟡 PROMISING-BUT-ROUGH (5th consecutive — but two long-standing
  mysteries are now root-caused, and the product's core loop works end-to-end)
- **Promise:** "A user can run the local server + PWA, create a tree, branch
  from any message, and see a visible, budgeted context manifest — resuming
  work in <30 seconds."
- **Time-to-first-success:** ~15 s API on a provisioned DB; ~25 min on a
  genuinely fresh DB (blocked by GAP-064: undocumented dev-user seeding).
- **Friction count:** 10 (see docs/dogfood/2026-09-10-integration.md).
- **Top 3 findings:**
  1. **GAP-064 (P1)** — fresh DB + documented quick start: every write 503s
     "database unavailable" (tree_members FK on unprovisioned dev user) and
     the error is logged to an unwired context logger = silent. The live
     :5437 DB works only because seed-demo-data.sql was run manually after
     the tick-416 wipe.
  2. **GAP-065 (P1)** — the documented tree-scoped `/reply` route is a
     phantom: registered only in the never-mounted `NodeHandler.Routes()`;
     never reachable on ANY build (chi bare 404). Real reply = node-create
     with `parent_id`. Usage skill v2.0's "verified" claim was wrong.
  3. **GAP-067 (P1)** — deploy staleness recurred (GAP-052 class): live
     binary 7 days behind HEAD, `/health/relay` 404s while the board says
     FTR-05 SPEC COMPLETE. `make deploy` exists but nothing runs it.
- **Also filed:** GAP-066 (topic zero-UUID root_node_id, re-verified),
  GAP-068 (compose quick start fails without gitignored .env; bunker-verified).
- **Verified working (fresh DB, real evidence):** tree create 201 → node
  create+parent_id 201 → context manifest (79/8000 tokens, ancestry) → SSE
  node_added live → fork (leaf rule enforced) → cards 201 (field-level
  unknown-field errors now — GAP-053 fix works) → graph stats → export →
  PWA + Vite proxy + JWT auto-inject.
- **Install leg (bunker, PASSED in 356s):** clone from public GitHub OK @
  1e3647b; `docker compose up -d --build` → both containers healthy, smoke
  `/health` 200 (schema 42) + `/version` 200 + auth enforced (401). Two
  workarounds were needed: `cp .env.example .env` (undocumented — GAP-068)
  and DOCKER_HOST pointing at the bunker rootless socket (environment quirk,
  not a project bug). Docs drift: README says compose exposes :8091, actual
  mapping is :8092→8080. Caveat: the compose DB is fresh → the documented
  first write would hit GAP-064's 503.
- **Left behind:** docs/dogfood/2026-09-10-integration.md ·
  docs/dogfood/diagnostics.md §7 · skills/hermes-canopy-usage/SKILL.md v2.1
  (phantom-route + fresh-DB corrections) · board rows GAP-064..068 ·
  tasks.md Dogfood Findings section.
- **Foreman:** not woken — main foreman hermes-canopy at cooldown 7200s
  (< 14400 threshold); 5 new pending rows will be picked up on its normal
  cycle.

