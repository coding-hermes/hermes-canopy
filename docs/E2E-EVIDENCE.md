# E2E Evidence Trail

Where run evidence for end-to-end and integration suites lives, and how to produce it.

## Canonical locations

| Suite | Command | Evidence location | Contents |
|---|---|---|---|
| Playwright E2E (frontend) | `cd frontend && npx playwright test` | `frontend/playwright-report/` | Per-run HTML report (auto-generated, git-ignored) |
| Vitest unit/component (frontend) | `cd frontend && npx vitest run` | terminal + CI logs | 522/522 as of 2026-08-09 |
| Go integration (backend, PG) | `make test` (or `go test ./...` with PG on 5437) | terminal + CI logs | 46/46 as of 2026-08-09 |
| GitReins judge verdicts | `gitreins judge <task>` | `.gitreins/history/<YYYY-MM-DD>/<verdict-id>/` | Tier-1 guard + tier-2 AI evaluation with per-AC evidence, committed to git |
| Visual-regression goldens | `cd frontend && npx playwright test visual-regression` | `docs/screenshots/visual-regression/` | `golden/` (app captures), `pairs/` (mockup-vs-app 2880x900), `README.md` |
| Per-tick summaries | foreman tick | `e2e-output/tickNNN.md` | Dated notes with pass/fail summary + links |

## Conventions

- **Playwright runs** always regenerate `frontend/playwright-report/` — the dated
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
