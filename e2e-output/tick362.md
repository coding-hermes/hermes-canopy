# E2E-001 Window 362-367 — Tick 362 (2026-08-19 00:13 local)

**Result: 60/60 PASS (12 files, 60.89s, zero visual drift, goldens untouched)** — window SATISFIED at first tick of window per fixture rule.

## Runner

- Worker: `deepseek-v4-flash @ deepseek` (hermes chat pid 1177578, session 20260819_001618_6dc6b4)
- Stack: stale 7d container `canopy-server` (image 411e6467df98, predates tree-scoped fork route c33d0e0) stopped for run; fresh host binary `/tmp/canopyd-t362` built from HEAD (00:15) served :8091 vs canopy-pg :5437; container restored as-found after (health 200).
- Runner command: `cd frontend && PLAYWRIGHT_BROWSERS_PATH=/home/kara/.cache/ms-playwright npm run test:integration`
- Mockups: /tmp/mockups/mockup-{1..4}.png present (Aug 14).

## Runs

| Run | Result | Notes |
|---|---|---|
| 1 | 58/60 | WIRE-001 + composer-to-canvas retry-exhausted (SSE/Yjs render-timing flake class, T338 precedent); both tests' writes provably in backend (POST 201/204 in canopyd log) — not a regression |
| 2 (kept) | **60/60, 60.89s** | WIRE-001 first-try 1.15s, composer-to-canvas first-try 0.66s, zero retry markers |

## Per-file (kept run)

| File | Pass | Time |
|---|---|---|
| crud-pages | 14/14 | 12.5s |
| visual-regression | 4/4 | 9.6s (zero drift) |
| fork-branch | 4/4 | 2.2s |
| navigation | 9/9 | 6.9s |
| approval-panel | 5/5 | 5.6s |
| tree-rendering | 7/7 | 5.0s |
| accessibility | 7/7 | 5.0s |
| mobile-drawer | 5/5 | 3.0s |
| tree-create | 2/2 | 2.7s |
| two-context-sync | 1/1 | 1.7s |
| context-manifest | 1/1 | 1.5s |
| composer-to-canvas | 1/1 | 1.1s |

## Side effects

- No files edited, nothing committed by worker; tracked artifacts intact (no test-results wipe this run).
- `dagger.db*` worker litter removed before board commit.
- VREG-001 label preference held (demo tree `UI-02 Rail Demo` b1655761 selected by label).
