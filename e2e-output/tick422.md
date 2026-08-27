# E2E-001 Battery — Tick 422 (window 422-427, first tick) — 2026-08-27

**Verdict: window 422-427 SATISFIED on the first tick — 61/61 FIRST RUN** (13 files,
60.87s, zero retry markers, zero backend 401/500/503). No re-runs, no re-baseline,
no debug session.

## Stack

- Fresh canopyd from HEAD (`/tmp/canopyd-t422`, `go build -o /tmp/canopyd-t422 ./cmd/canopyd`)
  swapped over the stale `canopy-server` container on :8091 (T374/T380 convention;
  INFRA-002: container image predates GAP-050 — never `docker compose up -d`).
- Vite dev server: the Aug-22 systemd-era orphan on :5173 was REUSED — verified
  it serves the canopy app (react-refresh HTML) and its proxy → :8091 works
  (demo tree 200 through proxy). No setup.ts / vite.config.ts patches needed.
- PG canopy-pg (380370eda742_canopy-pg) on :5437, healthy. Load avg 2.69/16 cores.
- Mockups present at /tmp/mockups/ (no ENOENT).
- canopyd started with NO JWT_SECRET override (default matches dev JWT, failure
  mode #19); env via guard-safe script (no inline env vars).

## Pre-battery data state

- 54 leaked E2E junk trees from T421's battery found in PG (T267 BUG-032 / T265 Sync /
  BUG-040 / T268 WIRE-002 / GAP040 E2E / GAP043 E2E families, created 2026-08-27 07:19Z) —
  ALL soft-deleted (`deleted_at` set), invisible to the product API (total 0).
  Sweeper dry-run matched 0 visible junk (sweeper only sees live trees). Left as
  invisible residue per T420 precedent.
- Live visible trees = 0 → seeded `scripts/seed-demo-data.sql` (idempotent):
  UI-02 Rail Demo (b1655761-2d7f-4b3c-85d5-21396da15691), 10 nodes / 9 edges / 3 topics.
- Write-path probe: `POST /api/v1/trees` with
  `{"title":"health-check-t422","rootMessage":{"content":"probe","contentFormat":"plain","nodeType":"message"}}`
  → **201** (schema requires all three rootMessage fields; bare `{title}` → 400
  VALIDATION_ERROR = healthy write path per failure mode #28). Probe tree deleted
  (204) before the battery.

## Battery run

| Run | Env | Result | Duration |
|---|---|---|---|
| 1 | seed applied, fresh binary, load 2.69 | **61/61 PASS** — 13 files, 0 retries, 0 backend errors | 60.87s |

## Post-battery cleanup

- Host canopyd killed (`fuser -k 8091/tcp`), `canopy-server` container restored
  as-found — health 200 confirmed.
- `scripts/remove-demo-data.sql` applied (DELETE 10 nodes + 1 tree, COMMIT).
  Live visible trees = 0 — EXPECTED (fixture lifecycle per GAP-051; product data
  is real Hermes gateway data).
- git tree clean: no tracked test-results deletions, no stray root `node_modules/.vite`.

## Gates (fresh, post-battery)

go build PASS · go vet PASS · go test -count=1 -p 1 (non-handler 22 pkgs) PASS (2m1.5s) ·
vitest unit 723/723 (39 files) · tsc -b PASS · gitleaks 0 leaks.

## CI

`gh run list` — last 4 runs all success. Two 07:00Z failures (GAP-050 batch:
golangci-lint resp.Body.Close check + board commit) are EXPLAINED — fixed by the
07:01Z `fix: check resp.Body.Close errors` commit, all subsequent runs green.
No INT-CI task.

## Notes for next window (T428)

- Battery procedure unchanged; expect live visible trees = 0 at window start →
  seed → battery → remove.
- The Aug-22 vite orphan on :5173 keeps serving current files (dev server); it
  proxies to :8091. If it dies, spawn fresh vite and patch `tests/setup.ts` BASE_URL.
- 54 soft-deleted junk trees accumulate; harmless but a future housekeeping task
  could hard-purge them (sweeper only handles visible trees).
