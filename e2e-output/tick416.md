# E2E-001 Battery — Tick 416 (window 416-421, first tick) — 2026-08-26

**Verdict: window 416-421 SATISFIED — 61/61 PASS** (13 files, clean verify run 60.62s)
after a deterministic golden re-baseline (3 data-state drifts, see below).

## Environment finding: live DB was WIPED between T410 and T416

- T410 (2026-08-25 ~15:00Z) reported 3,902 trees + seeded dev user.
- This tick: `canopy` DB completely EMPTY (all 39 tables 0 rows — trees, nodes,
  topics, users, edges, tree_members), schema intact at migration v32
  (`schema_migrations version=32 dirty=f`), PG container up 3 days (never
  recreated), volume `hermes-canopy_pgdata` intact. Wipe happened via SQL
  against the live DB after T410; the PG log tail (20:17Z 2026-08-26) shows
  T415's contaminated gate run (CANOPY_TEST_DB_URL override, class
  go-pg-integration-env-override-contamination) as the last heavy writer.
  No TRUNCATE statement visible in the log window; exact culprit unproven.
- The original demo tree `UI-02 Rail Demo` (b1655761-2d7f-4b3c-85d5-21396da15691,
  ~82 nodes / 9 topics) is UNRECOVERABLE: the only pg_dump
  (`/home/kara/backups/canopy-20260801-112134.dump`, migration v24) predates it
  (contains 3 trees, no UI-02). Grep of git history found no seed script.
- Leftover per-test isolation DBs `canopy_7a7caf64` + `canopy_d00d2283`
  (T415-era residue) and scratch `canopy_aug1` (restore inspection) dropped.

## Fix: deterministic reseed (committed)

- `scripts/seed-demo-data.sql` (idempotent, fixed UUIDs): dev JWT user
  (00000000-...-0001), demo tree with the canonical stable UUID b1655761...,
  10 nodes (3 branches + synthesis), 9 edges, 3 topics, owner membership.
- Verified through the vite proxy: trees list 200 with UI-02 Rail Demo
  (node_count 10), write-path probe 400 VALIDATION_ERROR (healthy).

## Battery runs

| Run | Env | Result | Duration |
|---|---|---|---|
| 1 (worker, ox-alpha-free @ opencode-go) | pre-verify all healthy | 58/61 — 3 visual-regression drifts (mockup 1: 8.712%, mockup 2: 8.606%, mockup 4: 22.077%); mockup 3 PASS | 82.76s |
| retry (per flake doctrine) | identical | identical pixel counts — NOT flaky | 14.70s |
| 2 (re-baseline) | UPDATE_VISUAL_GOLDENS=1 | 61/61 — goldens + pairs refreshed | 62.37s |
| 3 (verify, clean env) | no env var | 61/61 PASS — baseline stable | 60.62s |

- All 3 real-wiring tests (two-context-sync / composer-to-canvas /
  context-manifest) passed FIRST-RUN in every run — zero retry markers.
- Drift root cause (vision-verified): golden captured the old 82-node macro
  tree; current renders the reseeded 10-node tree. App renders correctly, no
  errors — pure data-state difference, NOT a UI regression.
- Re-baseline doctrine note: "never re-baseline on environmental failure"
  presumes the golden's data state is recoverable; here it was permanently
  destroyed by the DB wipe. Goldens now encode the deterministic reseeded
  state (reproducible via scripts/seed-demo-data.sql), so future windows are
  stable. Deviation documented in tick 416 tasks.md entry.

## Pre-verify (worker, healthy)

- vite :5173 proxied /api/v1/trees?limit=1 → 200
- canopyd :8091/health → ok (container canopy-server, HEAD image from T405)
- write-path probe → 400 VALIDATION_ERROR (healthy)
- /tmp/mockups/mockup-{1..4}.png → all present

## Side effects

- No tracked deletions under frontend/test-results/ (a11y artifacts intact).
- No root node_modules/.vite residue.
- Stack left as-found (container canopy-server up, vite :5173 up, PG up).
- Judge: E2E-001-B416 (see tasks.md entry for verdict id).
