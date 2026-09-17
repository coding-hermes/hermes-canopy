
## Dogfood Findings (2026-09-16)
Verdict: PROMISING-BUT-ROUGH
Promise: {"entry_point":"Go single binary `canopyd` (HTTP/JSON REST API server on :8091 + SSE event hub + embedded migrations, API-only — does NOT serve the PWA); companion entry points are the React/TS Vite PWA on :5173 (separate static serve in prod), CLI subcommands in the same binary (`canopyd serve|tree

- [P0] CLI ignores DB_*/HTTP_ADDR and hardcodes :8091, writing into the LIVE instance's Postgres — Judge ran the CLI with DB_PORT=15440 expecting a scratch DB but 'tree create' silently wrote a tree named 'CLI Tree' into the host's live canopy Postgres (visible in the live PWA Trees view; it was no
- [P0] The documented production PWA path yields a dead, permanently unauthenticated UI — README says to serve frontend/dist with any static server, but same-origin /api/v1/* returns index.html → 'Unexpected token '\u003c'', \u003c!doctype ... is not valid JSON\ and 'Backend: unreachable'; no rever
- [P1] Documented defaults collide with the host's live service; no scratch-instance recipe exists — docker compose up -d 'does not give a working API': hermes-canopy-canopyd exits 1 crash-looping 'STALE BUILD ... binary_embedded_version=38 db_schema_version=46', and README claims compose serves :809
- [P1] Isolation is not real and docs/schema drift: fresh DB leaks host-wide stores, schema_version 38≠47, VITE_API_URL wrong — A brand-new database still listed 4 cards and recents from other instances via the global stores ~/.hermes/canopy/cards and ~/.canopy/files; the docs example shows /health schema_version 38 while the 
- [P1] Advertised MCP endpoint cannot be initialized and is absent from the README — POST /api/v1/mcp returns -32601 'Method not found: initialize' (also for notifications/initialized), so a standards-compliant MCP client cannot handshake — yet the entry_point promise advertises 'a JS

## Tick 470 — 2026-09-17 ~05:10–06:00Z (WORK — DF-HERMES-CANOPY-7 P0 CLOSED)

**Verdict: WORK/OK.** Board at pick (parsed line-wise, LAST-WINS): 333 parsed rows / 297 unique ids / **25 pending**; CI 4/4 green on entry, both remotes at parity, tree clean. **Picked DF-HERMES-CANOPY-7** (P0, dogfood 2026-09-16, attempts=1 from tick 469's Codex-429 dead dispatch) — the only P0 on the board and the highest-leverage row (it is the "documented production path is dead" finding, not a feature).

**Premises re-verified at HEAD before dispatch** (not inherited from the row):
- `README.md:93` + the "Production (Manual)" Note told readers to serve `frontend/dist` with **any** static server → reproduced live with `npx serve -s`: `GET /api/v1/health` answered `200 text/html` `Content-Disposition: inline; filename="index.html"` — the exact "Unexpected token '<' … is not valid JSON" trap in the row; deep link `/tree/abc` → 200 text/html.
- `frontend/src/lib/api.ts` sent **no** `Authorization` header at all (only a comment about the Vite dev proxy injecting it); `grep -rn "Authorization" frontend/src` = 1 comment line. No `/api/v1/auth/*` endpoints exist → a static build was unauthenticated by construction.
- Docs named `VITE_API_URL` for a production build while the frontend reads `VITE_API_BASE_URL` (`api.ts:9`, `types/approval.ts:80`); `VITE_API_URL` is only the Vite **dev** proxy target (`vite.config.ts:25-26`).
- Port `:3000` occupied on this host (node listener) — the documented example port is not free here.

**Dispatch:** ONE worker, `gpt-5.6-luna` @ `openai-codex` (the lane re-probed live — `PROBE-OK` — after tick 469's 429, per its own "next dispatch must use a subscription lane" note; `--ignore-rules`, `-s coding-hermes-worker`, brief `/tmp/df7-brief.md`). GitReins task `DF-HERMES-CANOPY-7` was already created+started by tick 469.

**Landed (2 commits, both pushed):**
| commit | files | +/- | what |
|---|---|---|---|
| `f782adf` | 8 | +902/-53 | `api.ts` auth choke point (+14 vitest cases), `deploy/reference-proxy.py`, `deploy/nginx.canopy.conf`, `deploy/Caddyfile`, README/SELF_HOST/INTEGRATION truth pass |
| `30c2194` | 3 | +495/-1 | `deploy/tests/test_reference_proxy.py` (9 real-proxy tests), `make test-proxy`, proxy docstring verification line |

**Judge (GitReins Tier 2):** tier1 PASS both times. **Attempt 1 = FAIL (`26219ce3`)**: everything verified EXCEPT the criterion's "tests exercise the real proxy … SSE delivery" — no automated proxy test existed (the worker's proxy proof was manual). Per the foreman loop the SAME worker was re-dispatched with the judge's finding folded into the brief (`/tmp/df7-rework-brief.md`, "continue the existing tree, do not restart") → **attempt 2 = PASS (`969bbc3f`)**.

**Foreman gate battery (independent of the worker's report):**
- `cd frontend && npx vitest run` → **60 files / 1100 tests PASS** (fresh run; new file alone = 14 tests).
- `npx oxlint src/lib src/types` → exit 0, only pre-existing `lib/viewers/*` warnings (files untouched).
- `make test-proxy` → **Ran 9 tests … OK**, exit 0 (static asset, SPA deep link, unauthenticated 401 + "never the shell", injected bearer, client-header pass-through, 502 error preservation, incremental SSE, busy-port + non-loopback refusals).
- **Falsification proof (mine, not the worker's):** remove the API branch in the proxy → **6 failures + 1 error**; make the relay buffer → the SSE test fails with "event 1 arrived 2.467s … the relay is buffering"; revert → 9/9 OK, `git diff` clean.
- **Live proof driven by the foreman** against the running `canopyd :8091`: busy-port refusal rc=2; `--host 0.0.0.0 --token` refusal rc=2; `/api/v1/*` never `index.html` (401 JSON unauth, 404 JSON with token); `/tree/abc` → the dist shell; client-supplied token → `200 application/json` trees; injected token → `200 application/json`; bogus client header → 401 (pass-through wins); `/health` → `200 application/json`; no listener left behind.
- `gitleaks detect --no-git` → **0 leaks**. `nginx -t` (real `nginx:alpine`) → **successful**, but only WITH placeholder certs mounted at the documented `/etc/letsencrypt/live/…` path — without them it `emerg`-fails on the missing cert, which is expected for a TLS template (the worker's "nginx -t successful" claim is reproduced, with that prerequisite named). `caddy validate` → **Valid configuration**. No Go files in either commit (`git show --name-only` → 0 `.go`).
- **Pre-fix trap reproduced** in this tick (serve -s evidence above) so the doc fix is provably about a real failure.

**Worker findings folded into the board (honest, not hidden):**
- Disproved one brief premise: `frontend/frontend/dist` appears nowhere at HEAD — the drift was **relative-path** (docs did `cd frontend` then `npx serve … frontend/dist`); fixed by running the proxy from the repo root.
- Corrected premise: `/api/v1/health` is **not** a canopyd route (401 unauthenticated / 404 with a token); the AC's property (no `/api` path ever answered with the SPA shell) still holds and is now locked by a test.
- **Residual filed as DF-HERMES-CANOPY-13 (P1):** `api.ts` is not the only fetch site — 10 direct `fetch`/`EventSource` call sites (fileApi, pluginApi, ApprovalPanel, ShareDialog, activeTree, PluginUpdateBanner, useContextManifest, useGatewayRuns, useChannelFeed, useReviewFeed) still send no `Authorization` in a production build, and `EventSource` cannot carry headers at all (needs a decision).

**CI:** content run **35185715382** (`f782adf`) → **GREEN**. Rework run **35186701777** (`30c2194`) → **RED**, but on `internal/gateway TestStartRunEmptyManifestSurvivesPersist` (`TempDir RemoveAll cleanup: directory not empty`) — a package the commit never touched; locally `-short -count=1` passes **3/3**, previous run green ⇒ **test-teardown flake**, filed as **CI-006** (P2) with the failing run re-run (`gh run rerun`) to confirm flakiness. Nothing else red in the last runs.

**Push health:** `origin/master..HEAD` = 0 and `gitlab/master..HEAD` = 0 after each push (both remotes, verified).

**Bookkeeping:** `tasks.jsonl` — DF-HERMES-CANOPY-7 row updated in place (complete, commit_hash, worker_summary, foreman_note, ci_result) + **DF-13** and **CI-006** rows appended, compact style preserved (1 line replaced / 3 added, no reserialization). `events.jsonl` — ids **495** (task_completed DF-7), **496** (task_created DF-13), **497** (ci, flake). `board.jsonl` — `ticks_total` 469→**470**, `last_commit` = `f782adf`. Pending after close: **26** last-wins (was 25; +DF-13 +CI-006 −DF-7).

**DuckBrain:** tick key `/ticks/470` + `/project/hermes-canopy/status/2026-09-17-tick470-audit` (pre-write: tick keys contiguous through `/ticks/469`; status keys through `2026-09-17-tick469-audit`).

**Off-by-one:** health `{"status":"ok","uptime":"3h50m"}`; `POST /api/v1/problems/discover` for `spa-static-fallback-swallows-api-paths` → `not_found` (no cached answer) — no lab submission owed this tick (the fix class is project-specific, not a reusable debugging answer).

**Next tick:** the pending set is 26 rows — P1: DF-8 (documented defaults collide with the host's live service; no scratch-instance recipe), DF-9 (isolation not real + docs/schema drift), GAP-074 (docs truth pass on card-DB claims), GAP-076 (storage pivot, needs the owner ruling read carefully), **DF-13** (production auth across the remaining call sites). P2: GAP-077/078/079, DF-11/DF-12, GAP-084, **CI-006**. Watch items: `:8091` live canopyd + `:3000` occupancy; the growing `canopy_<hex>` test-DB residue; E2E-001 cadence on the next battery tick.


### Tick 470 — CI follow-up (final state)
- `24d7840` (board closeout) → run **35187164663**: **success**.
- `30c2194` (rework) → run **35186701777** **re-run**: **success** — the earlier red was `internal/gateway TestStartRunEmptyManifestSurvivesPersist` (`TempDir RemoveAll cleanup: directory not empty`), a package the commit never touched; the rerun passing on the same sha confirms the flake, and **CI-006** therefore stands as a real (if intermittent) test-teardown defect.
- `f782adf` (content) → run **35185715382**: **success**.
- No outstanding CI failures for this tick.

- **Off-by-one correction (same tick):** the line above ("no lab submission owed") was written before the CI triage and is superseded — TWO answers were submitted with `cadence: post-debug`: `sub_984028` (`gitreins-tier2-criterion-requires-automated-test-evidence`: when a criterion demands tests and the worker only produced a manual live proof, re-dispatch the SAME worker with the judge finding quoted, demand a stdlib-only suite driving the real artifact against a throwaway stub upstream + a falsification proof + one make target) and `sub_b75ea8` (`ci-flake-tempdir-removeall-cleanup-directory-not-empty`: prove an inherited failure by `git show --name-only | grep -c '\.go$'` = 0, reproduce with `-count=1`, re-run the same sha, file the flake as a teardown race). Both queued (`position` 1 and 2); `discover` for both classes returned `not_found` — an honest pre-solve miss, which is what the submissions fix.
