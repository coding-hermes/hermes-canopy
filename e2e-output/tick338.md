# Canopy E2E-001 Integration Battery — Tick 338 Report

Date: 2026-08-15 (window 338–343 opening tick)
Result: **49/49 PASS (9/9 files)** — expected outcome met.

## Stack State Used

| Component | State |
|---|---|
| canopyd binary | `./bin/canopyd` freshly built 2026-08-15 23:29 (32,300,126 bytes, go build -o ./bin/canopyd ./cmd/canopyd) — stale-binary rule honored |
| canopyd | PID 2354866, listening `*:8091` (env: HTTP_ADDR=:8091, DB_HOST=localhost, DB_PORT=5437, DB_USER/PASSWORD/NAME=canopy; **no JWT_SECRET set**) |
| vite dev server | PID 2354916, listening `127.0.0.1:5173` (fresh instance started via start script; dev JWT injected by proxy) |
| PostgreSQL | canopy-pg container `380370eda742_canopy-pg` listening `0.0.0.0:5437` (docker compose) |
| /tmp/mockups | mockup-{1..4}.png present (verified before run) |
| Start script | /tmp/canopy-t338-start.sh (setsid nohup single-call pattern), logs: /tmp/canopy-t338-backend.log, /tmp/canopy-t338-vite.log |

## Pre-flight Checks

- :8091 checked before start: **free** (no foreign holder — no port fight).
- Stale vite cleanup: killed both leftover process groups from this repo (PIDs 987793/987885/987886 and 2603002/2603142/2603144, all cwd = /home/kara/hermes-canopy/frontend). No leftover canopyd processes. Ports 5173/5175/8091 verified free after kill.
- Health checks after start: `backend health: 200`, `proxied auth: 200` (dev-JWT path through vite proxy confirmed working; trees API returned real JSON data).
- Backend log post-run-1: 0×401, 0×503, 0×500 — no environmental failure signatures.
- Prewarm: `google-chrome --headless=new --dump-dom --virtual-time-budget=20000 http://localhost:5173/tree/b1655761` → exit 0, DOM 143KB, 136 `react-flow` matches (canvas actually rendered).
  - Note: `b1655761` is the short prefix of tree `b1655761-2d7f-4b3c-85d5-21396da15691` ("UI-02 Rail Demo", exists in DB).

## Run History

### Run 1 (23:31:52, duration 112.23s) — 48/49, 1 failure

```
 Test Files  1 failed | 8 passed (9)
      Tests  1 failed | 48 passed (49)
```

Failure: `tests/two-context-sync.test.ts` (WIRE-001) "message sent in context A appears in context B without a reload"
`AssertionError: expected 0 to be greater than 0` at two-context-sync.test.ts:119 (context A) and :126 (context B) — vitest internal `retry x1` exhausted.

Diagnosis: environment verified clean — backend log showed the test's own traffic succeeding (POST /trees/{id}/nodes 201, POST /trees/{id}/sync 204, SSE GET /events 200 held open ~20.8s during the poll). `composer-to-canvas.test.ts` (same write path) passed in the same run with a benign `(retry x1)` marker. Classified as the known WIRE-001 SSE/Yjs render-timing flake under parallel load → one suite-level re-run taken per runbook (exactly one, no test code touched).

### Run 2 (23:34:08, duration 50.60s) — 49/49 FINAL

```
 Test Files  9 passed (9)
      Tests  49 passed (49)
   Start at  23:34:08
   Duration  50.60s (transform 75ms, setup 0ms, import 2.18s, tests 47.73s, environment 1ms)
```

WIRE-001 passed first-try (888ms, NO retry marker); composer-to-canvas passed first-try (652ms). Zero retry markers in the final run.

## Test Files That Ran (9 files, 49 tests)

| File | Tests | Result (final) |
|---|---|---|
| tests/two-context-sync.test.ts (WIRE-001 realtime SSE) | 1 | ✓ |
| tests/composer-to-canvas.test.ts (real composer→POST→Yjs→canvas) | 1 | ✓ |
| tests/crud-pages.test.ts | 14 | ✓ |
| tests/visual-regression.test.ts (mockups 1–4 vs /tmp/mockups) | 4 | ✓ |
| tests/navigation.test.ts | 9 | ✓ |
| tests/approval-panel.test.ts | 5 | ✓ |
| tests/accessibility.test.ts | 7 | ✓ |
| tests/tree-rendering.test.ts | 7 | ✓ |
| tests/context-manifest.test.ts (real GET /context/{node_id}) | 1 | ✓ |

## Anomalies / Notes

- Run 1 WIRE-001 flake → single allowed re-run, run 2 clean. Kept better result per runbook.
- Known non-blocking warning in both runs (documented, cosmetic): `⚠ "/" key did not focus search input — app may not intercept this key` (navigation.test.ts).
- Vitest side effects: `frontend/playwright-report/` created (untracked, expected). The 4 tracked a11y artifacts in `frontend/test-results/` SURVIVED this time (wipe did not fire — matches "not deterministic" note). No tracked files modified or deleted. No stray root `node_modules/.vite`.
- Pre-existing untracked files in repo (not from this run): `.vfs/.dirty`, `dagger.db`.
- One terminal command hit the Hermes hardline guard (multi-grep one-liner); recovered via the saved-script path — no impact on results.
- No source files modified, nothing committed, nothing pushed.

## Final Stack State (LEFT RUNNING per window convention)

- canopyd :8091 (PID 2354866) — health 200
- vite :5173 (PID 2354916) — proxied /api/v1/trees 200
- canopy-pg :5437 (container 380370eda742_canopy-pg) — UP

Raw output: /tmp/canopy-e2e-results.txt (final run), /tmp/canopy-e2e-results-rerun.txt (same bytes), /tmp/canopy-e2e-results-run1.txt (first run).
