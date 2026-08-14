# E2E Battery Report — Tick 326 (2026-08-14 ~01:36 UTC)

**Verdict: E2E WINDOW SATISFIED** — window 326-331 opened this tick (fixture rule: first tick of window); battery green on first run, zero drift, no re-baseline. 49/49 PASS (9 files, 58.02s, exit 0).

## Stack

- Backend: host binary canopyd rebuilt fresh (01:32 UTC, embeds current migrations), DB_HOST=localhost DB_PORT=5437 DB_USER=canopy DB_PASSWORD=canopy DB_NAME=canopy HTTP_ADDR=:8091, NO JWT_SECRET override (vite dev-proxy JWT validates). `/health` 200; `GET /api/v1/trees?limit=1` through vite proxy 200 (auth verified).
- Vite: :5173 up (200), dev proxy targets :8091.
- Postgres: canopy-pg container (380370eda742_canopy-pg, postgres:16-alpine) Up 2 days, healthy, :5437.
- Prewarm: headless chromium load of golden tree b1655761 (UI-02 Rail Demo) — `.react-flow` rendered OK before suite.

## Battery

`cd frontend && npx vitest run --config vitest.integration.config.ts`

- 49 passed / 49 (9 files, 58.02s): accessibility, approval-panel, composer-to-canvas, context-manifest, crud-pages, navigation, tree-rendering, two-context-sync, visual-regression.
- Visual-regression goldens untouched (golden tree b1655761 present). No WIRE-001 flake this window.

## Other Gates (same tick)

- Unit vitest: 647/647 (33 files, 4.18s).
- Go gate: 18/18 packages ok via `go test -count=1 -p 1 $(go list ./... | grep -v /handler)` with :5437 env (db 73.9s, plugin 28.1s — inside envelope).
- gitreins: 61 complete / 0 pending / 0 in_progress.
- CI: last 6 runs green (T325 push 31769880730 → T321 31738222086).
- gh issues: 0 open. gofmt -l: 16-file pre-existing baseline (unchanged).
- git fetch: 0 new remote commits; origin/master..HEAD = 0 unpushed (pre-commit).

## Notes

- canopyd host binary left running on :8091 for the window (T308 pattern); next window 332-337 opens at T332.
