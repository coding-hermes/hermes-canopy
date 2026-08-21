# Canopy E2E Battery — Tick 380 (2026-08-20, window 380-385, first tick of window)

## Result: 60/60 PASS — 12 test files — 97.77s — zero visual drift, goldens untouched

Stack: stale 7-day container image (Aug 12, predates tree-scoped fork route c33d0e0) stopped for the run; fresh host binary `/tmp/canopyd-t380` (HEAD a245905, built 22:50 local) served :8091 vs canopy-pg :5437 (healthy, up 2 days). Vite :5173 up (existing, pid 4085191). Container restored as-found post-run (health 200).

## Suite (vitest integration config, 12 files)

| File | Tests | Result |
|---|---|---|
| accessibility.test.ts | 7 | PASS |
| approval-panel.test.ts | — | PASS |
| composer-to-canvas.test.ts | 1 | PASS |
| context-manifest.test.ts | 1 | PASS |
| crud-pages.test.ts | — | PASS |
| fork-branch.test.ts | 4 | PASS (2.3s) |
| mobile-drawer.test.ts | 5 | PASS |
| navigation.test.ts | — | PASS |
| tree-create.test.ts | 2 | PASS |
| tree-rendering.test.ts | — | PASS |
| two-context-sync.test.ts | 1 | PASS (retry x1) |
| visual-regression.test.ts | — | PASS |

Totals: Test Files 12 passed (12) · Tests 60 passed (60).

## Flake note — two-context-sync (WIRE-001), documented class, load-induced

First full run: 59/60 (two-context-sync failed line 126, retry x1 also failed). Isolation re-runs: failed 2 more times (line 117 in isolation), then passed on retry x1 (22.9s), then full battery re-run 60/60 first-try.

Debugged (post-debug submitted to off-by-one: sub_007ac4, class `canopy-two-context-sync-flake`):
- NOT a code regression — zero code diff 734adf2 (T374 PASS)..a245905 (board/docs only).
- Backend healthy: node POST 201, SSE broadcast delivers `node_added` with content payload to connected EventSource (live curl probe on tree 87b4b864, sequence 1+2, verified).
- Independent Playwright repro (console + requestfailed capture) passed A and B.
- Root cause: host oversubscription — load avg 23 on 16 cores during the failures (other fleet ticks: security.test 487% CPU, opencode 219%, dagger, hermes python, google-flights MCP). Sync latency stretched past the 20s SYNC_TIMEOUT; retry passes once load dips.

## Notes
- Backend log confirms writes landed (201/204 POSTs during the run, fork 201 + deliberate leaf-fork 400) — no phantom passes.
- VREG label preference held (UI-02 Rail Demo b1655761 selected by label). No re-baseline; goldens untouched.
- Light audit alongside: vitest unit 657/657 (34 files, 8.5s) · go build+vet clean · go test -short EXIT 0 · gofmt 16-file baseline · gitreins 74 all complete (0/0) · gh issues 0 · CI last 3 runs success · off-by-one live (1179/1354/1354/queue 11/hit_rate 1) · cooldown 21600 fleet pin + scheduler agree.
