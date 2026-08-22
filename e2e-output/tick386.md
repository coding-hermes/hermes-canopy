# E2E-020 Tick 386 session record (2026-08-22, window 386-391)

## Result

**60/60 PASS (12 files, 80.45s) — FIRST RUN, zero retry markers, zero visual drift, goldens untouched.**

## Stack

Stale container image (up 36h, built ~Aug 12, predates newer migrations) stopped for the run; fresh host binary `/tmp/canopyd-t386` (HEAD cb7943e) served :8091 vs canopy-pg :5437 (compose `380370eda742_canopy-pg`, healthy). Vite :5173 up (existing). Container restored as-found post-run (health 200).

## Pre-run verification (all green)

- Load avg 1.69 (low — no flake-class pressure; T380's load-avg-23 flake not a factor)
- `/tmp/mockups/` restored from `docs/mockups/` (was wiped — ENOENT class, failure mode #20)
- Proxied GET /api/v1/trees → 200 JSON (Vite dev JWT auth OK)
- Write-path probe POST /trees → 400 VALIDATION_ERROR "root message content is required" (healthy signature per failure mode #28)
- Dev JWT user seeded: `SELECT count(*) FROM users WHERE id='00000000-...0001'` = 1
- Prewarm: `google-chrome --headless=new --dump-dom /tree/b1655761` → 136 `react-flow` hits, 143KB DOM (canvas mounted, T338 signature)

## Notable suite content (since T380)

Suite is now 12 files / 60 tests (grew from 49): mobile-drawer (5), tree-create (2), fork-branch (4) added since T380's window; real-wiring anti-phantom trio (two-context-sync, composer-to-canvas, context-manifest) all first-try green.

## Bookkeeping

- Board: audit event appended (append_board_event.py, tick 386, ticks_total→386, ticks_idle=0, last_commit=cb7943e pre-tick HEAD)
- e2e-output/tick386.md committed; tasks.md tick entry appended at file bottom
- DuckBrain (HTTP :3000, ns hermes-canopy): /ticks/386 + /project/hermes-canopy/status written, ids confirmed
- GitReins lifecycle skipped by design (E2E verification tick, T354-T380 precedent — no code commit, no task picked)
- Push BLOCKED (INFRA-003 — bogus GitHub PAT); commit local-only, do not amend/rebase
