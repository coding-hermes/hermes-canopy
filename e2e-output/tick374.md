# Canopy E2E Battery — Tick 374 (2026-08-19, window 374-379, first tick of window)

## Result: 60/60 PASS — 12 test files — 62.41s — zero drift, goldens untouched

Stack: stale 7-day container image (Aug 12, predates tree-scoped fork route c33d0e0) stopped for the run; fresh host binary `/tmp/canopyd-t374` (HEAD 734adf2, built 16:00 local) served :8091 vs canopy-pg :5437 (healthy, up 30h). Vite :5173 up (existing). Container restored as-found post-run (health 200).

## Suite (vitest integration config, 12 files)

| File | Tests | Result |
|---|---|---|
| accessibility.test.ts | 7 | PASS |
| approval-panel.test.ts | — | PASS |
| composer-to-canvas.test.ts | 1 | PASS |
| context-manifest.test.ts | 1 | PASS (1.4s) |
| crud-pages.test.ts | — | PASS |
| fork-branch.test.ts | 4 | PASS (2.3s) |
| mobile-drawer.test.ts | 5 | PASS (3.1s) |
| navigation.test.ts | — | PASS |
| tree-create.test.ts | 2 | PASS (2.7s) |
| tree-rendering.test.ts | — | PASS |
| two-context-sync.test.ts | 1 | PASS |
| visual-regression.test.ts | — | PASS |

Totals: Test Files 12 passed (12) · Tests 60 passed (60).

## Notes
- Duration 62.41s — inside the 61-75s envelope. No per-test timeouts, no retries needed this run (WIRE-001 flake class quiet).
- VREG-001 label preference held (UI-02 Rail Demo b1655761 selected by label). No re-baseline; goldens untouched.
- Backend log confirms writes landed (201/204 POSTs during the run) — no phantom passes.
- Light audit alongside: vitest unit 657/657 (34 files, 5.86s) · go build+vet clean · go test -short EXIT 0 · gitreins 0/0 · gh issues 0 · gofmt 16-file baseline · CI last 6 runs green · off-by-one live (1164/1339/1339/queue 6/hit_rate 1).
