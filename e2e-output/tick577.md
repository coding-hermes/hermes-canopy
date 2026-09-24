# E2E Tick 577 — window-satisfaction battery (2026-09-24)

## Verdict
**73/73 PASS, 16 files, FIRST RUN, zero retries, zero skips, 68.81s.**
E2E-001 cadence: satisfied. The battery had not run since tick 444 (63/63, window
440-445) — ~132 ticks of "no identifiable battery tick in the window" audit notes;
this run clears the watch item.

## Environment discovered (differs from the skill's container-swap era)
- :8091 is NO LONGER a docker `canopy-server` container — the live deployment is a
  systemd **user unit** `canopy-canopyd.service` running `/home/kara/bin/canopyd serve`
  (PID 2117088, built 2026-09-24T07:17:55Z, commit `47effa62`, schema 48/48).
  `docker ps` shows only `canopy-pg` (healthy, :5437).
- Deployed..HEAD Go delta = ONE gofmt-only commit (`934a0e1b style: apply gofmt`),
  so a port-swap was **unnecessary**; the suite ran against the live deployed binary
  as-is. Fresh HEAD build produced anyway (`/tmp/canopyd-t577`, 1.4s warm) as
  evidence; not used.
- Vite :5173 = pre-existing canopy instance (skill's T374+ reuse convention; identity
  verified via proxied `/api/v1/trees` JSON + `canopy-vite.service` unit).
  :5174 is a docker-pr (foreign container), not canopy.
- Attempted the skill's stop-the-unit swap first — `systemctl --user stop` was
  approval-gated on this unattended session (command never executed). The gofmt-only
  delta made the workaround moot rather than lossy (stated, per the honesty rule).

## Data-state recovery (documented T416 recipe)
- Live DB pre-battery: 8 trees, ALL 09-23 battery-leak residue ("T265 Sync",
  "BUG-040", "GAP040 E2E", …); canonical demo tree `UI-02 Rail Demo` absent
  (sweeper's 162-regex matched 0 of them).
- Applied `scripts/seed-demo-data.sql` (idempotent, fixed UUIDs): 9 trees /
  24 nodes / 3 topics. The current goldens encode exactly this seeded state
  (T444 re-baseline + T416 doctrine), so no golden drift occurred — confirmed by
  visual-regression passing first run.

## Pre-verify (all green)
- Proxied auth: GET :5173/api/v1/trees?limit=1 → 200
- Write path: POST :5173/api/v1/trees {"title":"health-check"} → 400
  VALIDATION_ERROR "root message content is required" = HEALTHY (DB write path reachable)
- Raw :8091 without Authorization → 401 (expected)
- /health → 200, schema_version 48 == embedded_migrations 48
- /tmp/mockups/mockup-{1..4}.png restored from git-tracked docs/mockups/ (known #20 ENOENT)

## Battery
`npm run test:integration` (vitest.integration.config.ts):
```
Test Files  16 passed (16)
     Tests  73 passed (73)
   Duration  68.81s
```
Raw output: /tmp/canopy-e2e-results-t577.txt (grep for retry/skip/todo: zero hits).

## Restores / teardown
- Deployed :8091 (systemd unit): never touched — still active as found (6h+ uptime).
- Vite :5173: untouched, running.
- Docker PG: untouched, healthy.
- Tracked a11y artifacts in frontend/test-results/: survived (no wipe this run).
- No stray root node_modules/ appeared; working tree clean (only pre-existing
  .gitreins/logs/ + tasks.yaml.lock untracked).
- Battery trees: 9 → 16 during the run, then the suite's BUG-044 afterAll self-cleanup
  soft-deleted its 7 (API total back to 2: demo tree + "Card CRUD Test" 09-23 residue
  outside the sweeper's match — left as-found).
- No source files modified; verification-only run; no worker dispatch (foreman-direct
  per the skill's window procedure).

## Gates (T422 window-close convention, run at post-battery HEAD)
See the tick entry in .coding-hermes/tasks.md for the recorded results
(go vet / go test non-handler 22pkgs shared-DB-allowed / golangci-lint full /
vitest unit / tsc / gitleaks).
