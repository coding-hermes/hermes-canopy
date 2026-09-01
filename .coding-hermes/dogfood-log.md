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
