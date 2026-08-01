# E2E Report — Tick 111 (2026-08-01)

**Server:** canopyd on :8091 (health OK) + Vite :5173
**Suite:** 41/41 PASS ✅
**Duration:** 37.32s

## Results by Suite

| Suite | Tests | Status | Duration |
|---|---|---|---|
| navigation | 10 | ✅ PASS | 6,736ms |
| accessibility | 7 | ✅ PASS | 4,980ms |
| approval-panel | 5 | ✅ PASS | 5,653ms |
| crud-pages | 12 | ✅ PASS | ~11,400ms |
| tree-rendering | 7 | ✅ PASS | ~6,600ms |
| **Total** | **41** | **✅ ALL PASS** | **37.32s** |

## Per-Suite Detail

### navigation.test.ts (10/10)
- Sidebar navigation links present
- Clicking Trees/Cards/Approvals links navigates correctly
- Search input present and functional on tree view
- Breadcrumbs area present
- Forward slash (/) does not trigger browser search
- Escape key does not crash the page

### accessibility.test.ts (7/7)
- ARIA live region present
- Skip-to-main-content link present
- Main content area has role="main"
- Sidebar navigation has correct ARIA role
- Focus rings enabled (no global outline:none)
- Keyboard Tab navigation cycles through interactive elements
- All navigation links have accessible labels

### approval-panel.test.ts (5/5)
- Approval panel heading rendered
- Refresh button present
- Status filter tabs (all, pending, approved, denied) present
- Page does not crash and has body content
- Pending filter tab selected by default

### crud-pages.test.ts (12/12)
- All 12 CRUD page tests passed (trees, nodes, topics, cards pages with their selectors, forms, and interactions)

### tree-rendering.test.ts (7/7)
- All 7 tree rendering tests passed (React Flow canvas, grid, zoom controls, MiniMap, DAG visualization)

## Screenshots

| View | Path | Size |
|---|---|---|
| Trees | `/tmp/canopy-e2e-tick111-trees.png` | 49KB (1440×900) |
| Approvals | `/tmp/canopy-e2e-tick111-approvals.png` | 43KB (1440×900) |
| Cards | `/tmp/canopy-e2e-tick111-cards.png` | 45KB (1440×900) |

All h1 headings confirmed: "Trees", "Approvals", "Cards" — sidebar heading "Knowledge Canopy".

## Findings

- **OK** — All 41 integration tests pass cleanly. No failures, no flakes, no skips.
- **Proxy** — Vite proxy to canopyd :8091 confirmed working (JWT injection active, API returns tree data).
- **Binary** — Pre-built `bin/canopyd` (Jul 30) works correctly, no rebuild needed.

## Notes

- No bugs found in this tick — all tests green, all screenshots captured successfully.
- canopyd started cleanly on :8091 against PostgreSQL :5437, health endpoint responded in 1s.
- Vite dev server was pre-existing on :5173 (PID 2357897); proxy config already correct.
- Screenshots captured via CJS Playwright script (`capture-screenshots-tick111.cjs`).
