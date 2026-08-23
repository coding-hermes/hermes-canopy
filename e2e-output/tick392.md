# E2E-021 Tick 392 session record (2026-08-22, window 392-397)

## Result

**60/60 PASS first run (12 files, 62.31s, zero retry markers) → 61/61 after BUG-040 regression test (worker-run). Window 392-397 satisfied at T392.** Two real failures on the first battery run turned out to be TWO real regressions — both root-caused, fixed, verified, and pushed this tick (commits b3b389b + d73edff). Not flakes.

## Stack

Fresh HEAD binary `/tmp/canopyd-t392` (HEAD 5d85d35) served :8091 vs canopy-pg :5437 (compose `380370eda742_canopy-pg`, healthy) — container `canopy-server` stopped for the run per the swap convention. Vite :5173 restarted this tick (needed to serve the fixed SW; the pre-existing server was serving a stale in-memory sw.js). Container restore pending at tick end.

## Pre-run verification (all green)

- Load avg 0.73 at start (no flake-class pressure)
- `/tmp/mockups/` present (4/4)
- Proxied GET /api/v1/trees → 200 JSON; write-path probe → 400 VALIDATION_ERROR (healthy)
- Dev JWT user seeded (count=1)
- Prewarm: google-chrome dump-dom /tree/b1655761 → 136 react-flow hits (canvas mounted)
- CI: last run success (T391 push); T390's 2 CI failures = golangci-lint errcheck, already remediated by 18e6e89

## First battery run: 58/60 — 2 REAL failures (both reproduced in isolation)

1. **tree-rendering "navigating to base URL shows Dashboard" — 30s timeout** (locator('h1') never appeared). Root cause: **Service Worker wedge**. The SW's `networkFirstWithQueue` cached the SSE `/events` stream — `cache.put()` on a never-ending body wedges CacheStorage; after ~6 tree visits the pending puts froze page loads (repro: 6× /tree/demo then goto '/' → goto timeout; `/@vite/client` + `/@react-refresh` never resolved; readyState stuck at `interactive`, no #root). Dashboard itself renders fine (direct probe: h1 in 3s).
2. **visual-regression mockup 1 — 7.831% pixel drift**. Root cause: **intended product change** — BUG-038 (T388) made /tree/demo resolve to the real UI-02 Rail Demo (82 nodes · 77 edges); the golden captured the old 5-node "Demo Tree". Legit golden refresh (mockup-1 only; mockups 2-4 goldens restored after noise-only rewrites).

## Second battery run exposed the WIRE-001 root cause

After the SW fix + golden refresh, run 2 = 59/60 with **WIRE-001 failing at :126**, run 3 at **:117**. Not the known load flake: a pageerror `Cannot read properties of undefined (reading 'slice')` at `buildSnapshot` (useYjsTree.ts:95 `nodeData.content.slice(0,80)`) appeared on every composer send with the SW active (SW-blocked control passed). **Root cause: thin-node race.** The SSE hub broadcasts THIN node_added events (id/tree_id/actor_id only — proven with a live SSE probe). `applyNodeUpdate` created a Yjs node from the thin payload; when the echo beat the full data (POST response via mergeBackendNodes — which SKIPS existing ids — or the yjs_update broadcast), the content-less stub crashed the canvas and was never hydrated. This was the true mechanism behind the long-documented "two-context-sync flake" (T338/T380 failures at :117/:126). The SW timing change made it fire deterministically.

## Fixes (commit b3b389b, BUG-042 — foreman-direct, fully root-caused)

- `frontend/sw.ts`: never cache `text/event-stream` responses (`isStreamingResponse` guard in both cache strategies); 1.5s `Promise.race` timeout on `caches.match` so a wedged cache can never block navigation; SW_VERSION 1.0.1. Rebuilt dist/sw.js.
- `frontend/src/stores/yjsProvider.ts`: `applyNodeUpdate` only CREATES nodes from payloads with content — thin SSE echoes merge into existing nodes only (full data arrives via POST response locally / yjs_update remotely).
- `frontend/src/stores/useYjsTree.ts`: crash-guard label `(nodeData.content ?? '').slice(0, 80)`.
- Goldens: mockup-1 refreshed (intended BUG-038 change); mockups 2-4 restored from git (noise).

## BUG-040 (board row): same-user two-tab staleness — RESOLVED as duplicate root cause

Worker (ox-alpha-free @ opencode-go, commit d73edff): with the BUG-042 fixes live, two tabs in ONE browser context sync in BOTH directions (~1s, zero console errors) on a fresh tree and on UI-02 Rail Demo. Decisive negative control: aborting tab B's SSE still delivered the message via y-indexeddb BroadcastChannel (shared `canopy-tree-{id}` IndexedDB) — no same-user fan-out gap exists server-side (sse_hub Broadcast fans out to every subscriber). The QA "stale until reload" was the thin-node crash. Regression pin added: `frontend/tests/two-tab-sync.test.ts` (141 lines, two pages in one context, real composer Ctrl+Enter, canvas assertion without reload).

## Final verification

- Full integration suite: **61/61 first run** (60 baseline + two-tab-sync), real-wiring trio first-try
- tree-rendering 7/7 incl. Dashboard at 707ms (was 60s timeout)
- go build/vet clean, tsc 0 errors, vitest unit 673/673 (35 files)
- Live SSE probe: POST /nodes → 201, `event: node_added` broadcast received
- Probes: 6-visit → Dashboard navigation OK with SW on; two-page same-user sync both canvases ~500ms

## Off-by-one

- Discovered `canopy-two-context-sync-flake` (cached, T380 load class — superseded by this tick's real root cause)
- Submitted `canopy-wire001-thin-node-race` (post-debug, sub_ed3953, queued pos 10) — full diagnosis + fix + future diagnostic chain

## Bookkeeping

- Board: audit event appended (append_board_event.py, tick 392, ticks_total→392, last_commit=5d85d35 pre-tick HEAD) + BUG-040 row closed (task_completed event) + BUG-042 row filed/closed
- e2e-output/tick392.md committed; tasks.md tick entry appended at file bottom
- DuckBrain (HTTP :3000, ns hermes-canopy): /ticks/392 + /project/hermes-canopy/status written
- GitReins: BUG-042 (create/start/complete + judge) + BUG-040 (create/start/complete + judge) — verdicts below
- CI: green (b3b389b run success; T390 failures remediated by 18e6e89)
- Push: b3b389b + d73edff pushed; UNPUSHED=0 verified
