# UI-09 visual regression baseline

This directory contains the committed visual baseline for the four vision-brief mockups and the side-by-side review pairs.

- `golden/` — the current Hermes Canopy app capture used by the pixel-diff tests.
- `pairs/` — 2880x900 composites with the source mockup on the left and the matching app capture on the right. The two panels are each 1440x900; the source is rendered with `object-fit: contain` so its original aspect ratio is preserved.

The source mockups are not copied into this repository. They remain available at:

- `/tmp/mockups/mockup-1.png`
- `/tmp/mockups/mockup-2.png`
- `/tmp/mockups/mockup-3.png`
- `/tmp/mockups/mockup-4.png`
- `/home/kara/hermes-canopy/vision-brief.html` (the same images embedded as base64)

## Capture and comparison harness

`frontend/tests/visual-regression.test.ts` is picked up by the existing Vitest integration configuration. It launches Chromium through Playwright, fixes the viewport at **1440x900**, navigates each route, waits for the route-specific UI/data state, captures a PNG, and compares it with the committed golden.

The harness uses `page.screenshot()` plus a small dependency-free PNG comparator rather than importing a second test runner. This is the Vitest-compatible equivalent of Playwright `toHaveScreenshot`: native Playwright screenshot assertions require Playwright's own test runner context, while the existing E2E-001 loop is Vitest-driven. The comparator requires exact 1440x900 dimensions and counts a pixel as different when its largest RGBA channel delta is greater than 8. The allowed drift is:

- `maxDiffPixelRatio = 0.02` (2% of pixels)
- channel threshold: `8`

A failed comparison writes the current capture to `/tmp/canopy-visual-regression/<mockup>-current.png` and reports the path. A dimension mismatch always fails, regardless of the ratio.

## Mockup-to-app mapping

| Mockup | Golden | Pair | App route/state | Notes |
|---|---|---|---|---|
| 1 — graph navigation | `golden/mockup-1-graph-nav.png` | `pairs/pair-1.png` | `/tree/demo` with the existing `__canopySeedDemoTree()` E2E seed | Direct graph/tree view match. |
| 2 — cards | `golden/mockup-2-cards.png` | `pairs/pair-2.png` | `/nodes`, selecting the first live backend tree | Closest current MVP surface is the UI-08 node hierarchy with UI-05 node cards. The mockup's Calendar, Search Results, and Code Execution cards are post-MVP structured-card content, so the gap is intentionally documented rather than filled with fake data. |
| 3 — collaboration | `golden/mockup-3-collaboration.png` | `pairs/pair-3.png` | `/approvals` | Closest real approval-gate route. The local MVP is single-user, so remote presence, five collaborators, threaded channels, and populated approval/activity data may be absent or reduced to the real empty/loading state. |
| 4 — topics/references | `golden/mockup-4-topics.png` | `pairs/pair-4.png` | `/topics`, selecting the first live backend tree; the persistent `TopicsRail` is also visible | Closest shipped Topics + TopicsRail surface. The mockup's inline `#` autocomplete/context popover is not yet a complete equivalent. |

Pair construction is browser-only: the mockup PNG is loaded into a Playwright page at the same 1440x900 viewport, captured as the left panel, and then composited beside the app capture in another Playwright page. No image-processing dependency is required.

## Reproducible prerequisites and state

1. Run the existing Canopy stack without restarting it: `canopyd` on `:8091`, PostgreSQL on `:5437`, and Vite on `:5173`.
2. Run commands from `/home/kara/hermes-canopy/frontend`. The Vite proxy supplies the dev JWT automatically; no token should be hard-coded in the test.
3. Keep `/tmp/mockups/mockup-{1..4}.png` and `vision-brief.html` available.
4. The graph route reuses the same browser-side `__canopySeedDemoTree()` mechanism as `frontend/tests/tree-rendering.test.ts`; it seeds only an empty Yjs document.
5. The `/nodes` and `/topics` captures select the first tree returned by the live backend tree selector, exactly like the existing CRUD integration test. They do not create fake nodes or topics. If no backend tree is available, the harness captures and documents the real empty state with a warning.
6. The approvals capture uses the real `/approvals` API response; it does not fabricate collaborators or approval records.
7. The browser clock is fixed at `2026-08-02T12:00:00Z` so relative time labels do not drift between baseline and later E2E ticks. Animations/transitions are disabled, reduced motion is enabled, and the main scroll position is reset before each capture.

## Commands

From `frontend/`:

```bash
# Initial baseline generation or intentional refresh. Also regenerates all four pairs.
UPDATE_VISUAL_GOLDENS=1 npm run test:integration -- visual-regression.test.ts

# Diff the current app against the committed goldens (four visual tests).
npm run test:integration -- visual-regression.test.ts

# E2E-001 enforcement command: includes the existing integration tests and these four tests.
npm run test:integration
```

Only refresh goldens after reviewing all four pair images and confirming the UI change is intentional. The update command overwrites `golden/*.png` and `pairs/*.png`; the source mockups remain external references.

GitHub Actions is not enabled for this repository's fleet. The E2E-001 tick is the enforcement point: the full `npm run test:integration` command must be run against the running stack, and any visual drift over the documented threshold fails the loop.
