# Canopy E2E Battery — Tick 368 (2026-08-19, window 368-373, first tick of window)

## Result: 60/60 PASS — 12 test files — 121.87s — zero drift, goldens untouched

Stack: stale 7-day container image (Aug 12, predates tree-scoped fork route c33d0e0) stopped for the run; fresh host binary `/tmp/canopyd-t368` (HEAD 91ca76e, built 09:01 local) served :8091 vs canopy-pg :5437 (healthy). Vite :5173 up (existing). Container restored as-found post-run (health 200).

## Suite (vitest integration config, 12 files)

| File | Tests | Result |
|---|---|---|
| accessibility.test.ts | 7 | PASS |
| approval-panel.test.ts | — | PASS |
| composer-to-canvas.test.ts | 1 | PASS (5.7s) |
| context-manifest.test.ts | 1 | PASS (1.5s) |
| crud-pages.test.ts | — | PASS |
| fork-branch.test.ts | 4 | PASS (2.1s) |
| mobile-drawer.test.ts | 5 | PASS (2.8s) |
| navigation.test.ts | — | PASS |
| tree-create.test.ts | 2 | PASS (2.6s) |
| tree-rendering.test.ts | — | PASS |
| two-context-sync.test.ts | 1 | PASS (21.8s, retry x1 — documented WIRE-001 SSE/Yjs render-timing flake class, T338/T362 precedent; passed first-try on retry) |
| visual-regression.test.ts | — | PASS |

Totals: Test Files 12 passed (12) · Tests 60 passed (60).

## Notes
- Duration 121.87s — above the 61-75s envelope (host load; suite still fully green). No per-test timeouts.
- VREG-001 label preference held (UI-02 Rail Demo b1655761 selected by label). No re-baseline; goldens untouched.
- WIRE-001 + composer-to-canvas both passed (composer-to-canvas first-try 5.7s; two-context-sync needed its single allowed retry).
- Backend log confirms writes landed (201/204 POSTs during the run) — no phantom passes.
- Light audit alongside: vitest unit 657/657 (34 files, 4.26s) · go build+vet clean · go test -short EXIT 0 · gitreins 0/0 · gh issues 0 · gofmt 16-file baseline · CI last 3 runs green · off-by-one live (1159/1334/1334/queue 6/hit_rate 1).
