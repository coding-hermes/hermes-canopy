# Canopy E2E Battery — Tick 344 (2026-08-17 ~17:30 UTC)

## Verdict

**E2E-001 window 344–349: SATISFIED on first tick — 49/49 PASS (52.71s, 9 files)**

## Run detail

| Item | Value |
|---|---|
| Suite | `cd frontend && npm run test:integration` (vitest integration config) |
| Result | 49/49 passed, 0 failed, 0 flaky-retried |
| Duration | 52.71s (real browser tests; transform 73ms, tests 49.91s) |
| Stack | Live persistent stack (T338 convention): canopyd :8091 (pid 2354866), vite :5173 (pid 2354916), canopy-pg :5437 (docker, healthy 3 days) |
| Goldens | Untouched — zero drift, visual-regression 4/4 (9.6s) |

## ⚠️ Run-1 false-green: PLAYWRIGHT_BROWSERS_PATH fix (NEW)

Run 1 "passed" 49/49 in **2.92s** with every browser test skipped
(`⚠ Dev server not running — skipping integration test`). Root cause: this
scheduler-spawned session runs with `HOME=/tmp/dogfood-muster/home`, so
Playwright resolved its browser cache to
`/tmp/dogfood-muster/home/.cache/ms-playwright/chromium_headless_shell-1234`
(executable does not exist → `isServerRunning()` returns false → all tests skip).

Probe:
```
node -e "const {chromium}=require('@playwright/test'); ...chromium.launch({headless:true})"
→ ERROR: browserType.launch: Executable doesn't exist at /tmp/dogfood-muster/home/.cache/...
```

Fix: `PLAYWRIGHT_BROWSERS_PATH=/home/kara/.cache/ms-playwright npm run test:integration`
(real browsers at /home/kara/.cache/ms-playwright/{chromium-1234,chromium_headless_shell-1234,...}).
Suite then ran real browser tests, 49/49 in 52.71s.

Detection rule: suite duration ≈3s + "Dev server not running" warnings = all skips,
NOT a green run. Submitted post-debug to Off-by-One
(`playwright-browser-cache-scheduler-home-redirect`, sub_c202b8 queued) so future
E2E ticks across the fleet hit a cached answer.

## Per-file results

| File | Tests | Time |
|---|---|---|
| accessibility.test.ts | 7 | pass |
| approval-panel.test.ts | 5 | 5.7s |
| composer-to-canvas.test.ts | 1 | 1.2s |
| context-manifest.test.ts | 1 | 1.5s |
| crud-pages.test.ts | — | pass |
| navigation.test.ts | — | pass |
| tree-rendering.test.ts | — | pass |
| two-context-sync.test.ts | 1 | 1.5s |
| visual-regression.test.ts | 4 | 9.6s |
| **Total** | **49** | **52.71s** |

## Light-audit battery (same tick)

| Gate | Result |
|---|---|
| vitest unit (frontend) | 647/647, 33 files, 4.1s |
| gitreins task list | 64 complete / 0 pending / 0 in_progress |
| gitreins guard (full) | PASS 4/4 (secrets/build/lint/tests) |
| CI (gh run list, last 6) | 6/6 completed/success |
| gh issues open | 0 |
| gofmt -l | 16 files (baseline, no drift) |
| git fetch origin + unpushed | 0 at tick start |
| Stack probe | :8091/:5173/:5437 all listening; docker canopy-pg healthy |
| Worker scan | none (no sibling fire) |

## Housekeeping

- `dagger.db` stray removed; `frontend/playwright-report/` (untracked artifact) removed
- `.gitreins/usage.jsonl` added to .gitignore (judge telemetry, was untracked noise)
- `.vfs/graph/edges.jsonl` modified + `.vfs/.dirty` left untouched (hilo — never commit)

Next E2E-001 window: 350–355, opens at T350.
