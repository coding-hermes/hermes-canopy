# Canopy E2E Integration Battery — Tick 332

**Verdict: E2E WINDOW SATISFIED** — `49 passed / 49` across 9 files, exit code 0, 72.75s.

---

## Stack

| Item | Value |
|---|---|
| Backend binary build | `go build -o bin/canopyd ./cmd/canopyd` → **0.79s** (cache-warm); rebuilt 2026-08-14T15:46:45. Newest migration `000031_tree_events_yjs_update` (Aug 11) — binary already current, no stale-binary FTL. |
| Backend env | `DB_HOST=localhost DB_PORT=5437 DB_USER=canopy DB_PASSWORD=canopy DB_NAME=canopy HTTP_ADDR=:8091` — **no `JWT_SECRET` override** (default `dev-secret-change-me` matches the vite-injected dev JWT). |
| canopyd | `./bin/canopyd`, PID 463935, detached via `setsid nohup` (guard-safe script `/tmp/start-canopyd-t332.sh`). Log: `INF canopyd starting … HTTP server listening addr=:8091`. |
| Vite | `npx vite` (v8.1.5), PID 465249, detached via `setsid nohup` (`/tmp/start-vite-t332.sh`). Ready in 221ms on `:5173`. Proxy targets `:8091`. |
| Health (direct) | `GET :8091/health` → **200** |
| Auth (proxied) | `GET :5173/api/v1/trees?limit=1` → **200** (JSON body — confirmed this is the real canopy app, not a stray vite) |
| Raw curl (expected) | `GET :8091/api/v1/trees` → **401** (expected — dev JWT only via proxy; not a failure) |
| Postgres | compose service `postgres` (container_name `canopy-pg`), actual container `380370eda742_canopy-pg` (compose project prefix). **Up (healthy)**, `0.0.0.0:5437→5432`. Started via `docker compose up -d postgres` (the task's "canopy-pg" service name maps to this compose service). |
| Dev JWT user | `users.id='00000000-0000-0000-0000-000000000001'` present (count=1) — no seeding needed. |
| Data state | 3,684 trees. Two `UI-02 Rail Demo` trees: golden **`b1655761-…` (82 nodes, 9 topics)** + `6d94185a-…` (11 nodes, 9 topics). Golden tree used for prewarm. |
| Prewarm | `google-chrome --headless --dump-dom http://localhost:5173/tree/b1655761-…` → **25.8s**, DOM contains `react-flow` (136 matches). Vite cold-compile of `.react-flow` eliminated. |

## Battery

**Raw result: `49 passed / 49` (9 files, 72.75s), exit 0.**

Per-file:

| File | Tests | Result |
|---|---|---|
| crud-pages | 14 | ✅ 14/14 |
| visual-regression | 4 | ✅ 4/4 — **zero drift** (mockups 1–4 all pass, goldens untouched) |
| tree-rendering | 7 | ✅ 7/7 |
| navigation | 9 | ✅ 9/9 (known non-blocking `"/" key not intercepted` warning) |
| approval-panel | 5 | ✅ 5/5 |
| accessibility | 7 | ✅ 7/7 |
| two-context-sync (WIRE-001) | 1 | ✅ 1/1 — `(retry x1)` internal vitest retry, passed |
| context-manifest | 1 | ✅ 1/1 |
| composer-to-canvas | 1 | ✅ 1/1 |

**Visual-regression drift status:** 4/4 passed with ZERO pixel drift — no re-baselining, goldens current.

## Notes

- **One internal retry (not a failure):** `two-context-sync.test.ts` (WIRE-001) shows `(retry x1)` — the known render-timing flake under host load. It **passed** on retry; no suite-level re-run was needed (the "retry only if the file fails" rule did not trigger). No other flakes.
- **No suite-level retry** was required — first full run was green.
- **Stack left RUNNING** (window pattern): canopyd PID 463935 on :8091, vite PID 465249 on :5173, PG `380370eda742_canopy-pg` on :5437.
- **Git untouched** — no commits, no checkout/restore. Read-only `git status` shows only pre-existing noise (`.vfs/graph/edges.jsonl` M, `.vfs/.dirty`, `dagger.db`, untracked `frontend/playwright-report/`).
- **test-results wipe did NOT fire this tick** — all 4 tracked a11y artifacts (`accessibility-audit-raw.json`, `accessibility-audit.md`, `run-a11y-audit.mjs`, `run-a11y-audit.py`) survived in `frontend/test-results/`.
- **Two `UI-02 Rail Demo` trees exist** (b1655761 = golden 82-node/9-topic; 6d94185a = 11-node/9-topic). VREG-001 `selectFirstTree` preferred the demo label and both share 9 topics, so visual-regression mockups 2+4 were unaffected. Flagging for awareness only — no action needed this tick.
- Mockups restored to `/tmp/mockups/` (all 4 present) before the run — no ENOENT.
