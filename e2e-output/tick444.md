# E2E-024 — Tick 444 (window 440-445) — 2026-09-13

**Verdict: window 440-445 SATISFIED; the overdue 428-439 windows are covered by the same run.**

Result: **63/63 after a justified UI-09 golden re-baseline** — run 1 was 59/63
(87.08s) with 4/4 visual-regression mockups drifted 2.0-3.3%; re-baseline run
63/63 (77.70s); **clean verify run 63/63 in 79.23s, zero retry markers**
(`/tmp/canopy-e2e-results.txt`).

## Stack

- HEAD canopyd `/tmp/canopyd-t444` (schema_version 46, embedded_migrations 46)
  swapped onto :8091; the stale `canopy-server` container was found
  **crash-looping** (`Restarting (1)`) against the migrated DB and was stopped
  for the window — left STOPPED after (INFRA-002 doctrine: do not run the stale
  image until it is rebuilt).
- Vite: reused the 6-day systemd-era orphan on :5173 — verified identity by
  `/proc/<pid>/cwd` → `hermes-canopy/frontend` and a proxied `/api/v1/trees`
  JSON 200 (not an HTML error page = not a foreign app).
- PG `canopy-pg` :5437; load avg 23/16 cores at start (flake-class territory —
  flagged, and the run needed no re-run for timing flakes).
- `/tmp/mockups` restored from `docs/mockups/mockup-*.png` BEFORE the battery.
- Demo tree reseeded via `docker exec -i canopy-pg psql ... < scripts/seed-demo-data.sql`
  (⚠️ the `-f -` stdin form silently does nothing — use the `-i` + `<` form and
  confirm `INSERT 0 1` lines).
- Write-path probe: `POST /api/v1/trees` with all three `rootMessage` fields → 201,
  probe tree `DELETE` → 204. Canvas prewarm: 136 `react-flow` hits.
- Post-battery: `scripts/remove-demo-data.sql`; 0 LIVE trees afterwards is the
  expected GAP-051 state. The 19 rows the battery created are **soft-deleted by
  the committed self-clean (BUG-044)** — verify with
  `SELECT count(*) FILTER (WHERE deleted_at IS NULL)` before calling anything a leak.

## The load-bearing learning: classify golden drift before re-baselining

All-mockups drift with a clean suite and zero console errors has three classes;
only ONE justifies `UPDATE_VISUAL_GOLDENS=1`:

| Class | Signature | Action |
|---|---|---|
| Data-state | live trees missing/stub, reseeded demo tree, empty backend | fix data (or reseed), never re-baseline |
| Environment | JWT_SECRET override (401 wall), `/tmp/mockups` ENOENT, dead vite/proxy | fix env, never re-baseline |
| **Intentional UI evolution** | healthy render + zero console errors + the visible delta maps to a shipped commit | re-baseline, commit goldens as `test(e2e)` |

Discriminator used this tick: vision-compared golden vs
`/tmp/canopy-visual-regression/mockup-N-*-current.png`; the golden sidebar had
**9 nav items**, the capture **10** — the extra one is **Peers**, shipped by
`75709b2` (FTR-02 Phase P5 federation UI), which post-dates the goldens. Real
change → re-baseline, then a clean verify run. Drift was uniform across all 4
mockups (2.0-3.3%, max channel delta 210) because the delta is shared chrome.

Off-by-one class submitted post-debug: `canopy-visual-golden-drift-classification`
(sub_daf50e).
