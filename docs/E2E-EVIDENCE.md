# E2E Evidence Trail

Where run evidence for end-to-end and integration suites lives, and how to produce it.

## Canonical locations

| Suite | Command | Evidence location | Contents |
|---|---|---|---|
| E2E / integration suite (frontend) | `cd frontend && npm run test:integration` | terminal + CI logs | vitest + Playwright browser suites under `frontend/tests/` (needs PostgreSQL on :5437, canopyd, vite dev server on :5173) |
| Vitest unit/component (frontend) | `cd frontend && npx vitest run` | terminal + CI logs | 522/522 as of 2026-08-09 |
| Go integration (backend, PG) | `make test` (or `go test ./...` with PG on 5437) | terminal + CI logs | 46/46 as of 2026-08-09 |
| GitReins judge verdicts | `gitreins judge <task>` | `.gitreins/history/<YYYY-MM-DD>/<verdict-id>/` | Tier-1 guard + tier-2 AI evaluation with per-AC evidence, committed to git |
| Visual-regression goldens | `cd frontend && npm run test:integration -- visual-regression` | `docs/screenshots/visual-regression/` | `golden/` (app captures), `pairs/` (mockup-vs-app 2880x900), `README.md` |
| Per-tick summaries | foreman tick | `e2e-output/tickNNN.md` | Dated notes with pass/fail summary + links |

## Conventions

- **Playwright toolchain smoke runs** (`cd frontend && npx playwright test`;
  collects `frontend/e2e/*.pw.ts` only — this is NOT the E2E suite) always
  regenerate `frontend/playwright-report/` — the dated
  run can be inspected from the HTML report's index. A run that exits non-zero
  means a failing test; the report names it.
- **Judge verdicts are the authoritative acceptance evidence.** Every completed
  board task carries `judge PASS <id>` in its board row / `ci_result`; the full
  per-AC verdict (including test re-runs the judge performed) is in
  `.gitreins/history/<date>/<verdict-id>/` and survives in git history.
- **Anti-phantom tests** (TEST-REAL-001..003) exercise real wiring only:
  two-browser realtime sync, composer→canvas, context-manifest render — no mocks,
  no seeded DOM. If a suite reports green but the wiring under test is stubbed,
  treat the run as invalid (BUG-032 class failure).
- **New runs:** after a meaningful E2E window, commit a one-paragraph summary to
  `e2e-output/tickNNN.md` (pass/fail, counts, notable flakes) so release managers
  can trace evidence without running anything.

## Full-suite baseline (2026-08-09)

- vitest: 522/522
- Playwright: 48/48
- Go integration (PG): 46/46
- Visual regression: 4 mockup pairs, goldens unchanged since 2026-08-02

## QA chaos-disconnect probe (QA-CAN-004)

The canonical, stable command for the external QA chaos-disconnect cell
(`go-test-chaos-disconnect-timeout-false-hang`) is:

```bash
make test-chaos-disconnect
```

This target runs the bounded, DB-independent short-mode suite: it is a pure
prerequisite alias for `make test-short` (`go test ./... -short -count=1
-timeout=480s`), so the flags live in exactly one place.

The cell runs under a 120s disconnect window. The full non-short suite is
invalid for that window: `internal/db` alone was measured at 135.3s and
`TestINT05_2000NodeTree` documents a 2.5–3+ minute full run, so a healthy
tree reads as a disconnect-induced false hang and the harness mis-files a
finding. `-short` skips the PostgreSQL-backed and performance integration
work (which is what pushes the full suite past the window); the target
completes well inside 120s and needs no database.

For the full integration suite — which remains the acceptance evidence for
Go work — use `make test` (`go test ./... -count=1 -timeout=600s`) with
PostgreSQL on :5437, as documented in the table above. The disconnect cell
does not replace it; it only needs a deterministic, fast, disconnect-safe
selection to distinguish "suite healthy and green" from "connection lost."
