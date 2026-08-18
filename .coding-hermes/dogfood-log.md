# Dogfood Log — Hermes Canopy

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
