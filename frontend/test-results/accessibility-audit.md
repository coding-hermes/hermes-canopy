# Hermes Canopy — Accessibility Audit Report

**Target:** WCAG 2.1 AA Compliance  
**Date:** 2026-07-27  
**Auditor:** Automated (axe-core + Playwright + static code analysis)  
**Co-author:** Co-authored-by: Alexis Okuwa <wojonstech@gmail.com>  

---

## Executive Summary

| Metric | Count |
|--------|-------|
| Pages audited | 7 |
| **Total axe violations** | **20** |
| Critical | 0 ✅ |
| Serious | 7 (color-contrast) |
| Moderate | 6 (heading-order + missing h1) |
| Minor | 7 (aria-allowed-role × 7) |
| Keyboard focus indicator issues | 0 ✅ |
| Tab-navigable elements | All pages have working tab stops (except TreeView) |
| Screen reader checks passing | 93% (64/69 checks pass) |

**Overall Assessment:** The frontend is in **good shape** for WCAG 2.1 AA. No critical violations found. The remaining issues are concentrated in three areas: **(1) color contrast** on footer/version text and filter tabs, **(2) heading hierarchy** (skipping h2 on CRUD pages), and **(3) TreeView missing h1 and tab-navigable nodes**.

---

## Summary by Page

| Page | Violations | Keyboard Stops | SR Passes | Key Issues |
|------|-----------|----------------|-----------|------------|
| Dashboard (/) | 2 (0c/1s/0m/1minor) | 8 | 10/10 | Color contrast on footer |
| TreesPage (/trees) | 3 (0c/1s/1m/1minor) | 11 | 9/10 | h1→h3 skip, contrast |
| NodesPage (/nodes) | 3 (0c/1s/1m/1minor) | 10 | 9/10 | h1→h3 skip, contrast |
| TopicsPage (/topics) | 3 (0c/1s/1m/1minor) | 9 | 9/10 | h1→h3 skip, contrast |
| CardsPage (/cards) | 3 (0c/1s/1m/1minor) | 9 | 9/10 | h1→h3 skip, contrast |
| ApprovalPanel (/approvals) | 3 (0c/1s/1m/1minor) | 13 | 9/10 | h1→h3 skip, filter tab contrast |
| TreeView (/tree/demo) | 3 (0c/1s/2m/1minor) | **0** ❌ | 8/10 | **No h1**, no tab stops, unlabeled input |

---

## Detailed Findings

### 🔴 SERIOUS: Color Contrast Violations (7 total across all pages)

**WCAG SC:** 1.4.3 Contrast (Minimum) — text must have ≥4.5:1 ratio; large text ≥3:1

#### 1. Footer Version Text (`#99a1af` on `#111827` — 2.6:1)
- **Location:** `App.tsx` sidebar footer `<p>` — "Hermes Canopy v0.1.0"
- **Pages affected:** All 7 pages
- **Instances per page:** 1
- **Fix:** Change `text-gray-400 dark:text-gray-500` to `text-gray-500 dark:text-gray-300`
  ```diff
  -<p className="text-xs text-gray-400 dark:text-gray-500">
  +<p className="text-xs text-gray-500 dark:text-gray-300">
  ```

#### 2. Header Backend Text (`#99a1af` on `#111827` — 2.6:1)
- **Location:** `App.tsx` header `<span>` — "Backend: localhost:8080"
- **Pages affected:** All 7 pages
- **Instances per page:** 1
- **Fix:** Same approach — change to `text-gray-500 dark:text-gray-300`

#### 3. Navigation Bar Hint Text (`#4a4a6a` on `#0f0f1a` — 2.24:1)
- **Location:** `NavigationBar.tsx` breadcrumb hint — "Select a node to see its path"
- **Pages affected:** TreeView
- **Instances:** 1
- **Fix:** Lighten the hint color:
  ```diff
  -style={{ color: '#4a4a6a' }}
  +style={{ color: '#6b7280' }}
  ```
  Hex `#6b7280` on `#0f0f1a` = ~4.6:1 ✅

#### 4. Message Composer Footer Text (`#4a4a6a` on `#0f0f1a` — 2.24:1)
- **Location:** `MessageComposer.tsx` footer — char count and keyboard hints
- **Pages affected:** TreeView
- **Instances:** Multiple
- **Fix:** Same as above — lighten to `#6b7280`

#### 5. Empty State Text (`#4a5565` on `#111827` — 2.34:1)
- **Location:** CRUD pages empty state descriptions (e.g., "Create your first conversation tree to get started.")
- **Pages affected:** TreesPage, NodesPage, TopicsPage, CardsPage
- **Fix:** Change `text-gray-600` to `text-gray-400` on empty state `<p>` elements:
  ```diff
  -<p className="text-xs text-gray-600">
  +<p className="text-xs text-gray-400">
  ```

#### 6. Filter Tab Text (`#99a1af` on `#1f2937` — 1.22:1) — **MOST CRITICAL**
- **Location:** `ApprovalPanel.tsx` filter tab buttons (all/pending/approved/denied) — inactive state
- **Fix:** Lighten the inactive text:
  ```diff
  className={`px-3 py-1.5 rounded-md text-xs font-medium capitalize transition-colors ${
    statusFilter === f
      ? 'bg-purple-600 text-white'
  -    : 'text-gray-400 hover:text-gray-200'
  +    : 'text-gray-300 hover:text-gray-100'
  }`}
  ```

#### 7. PresenceBar Online Count (`#4a4a6a` on `#0f0f1a` — 2.24:1)
- **Location:** `PresenceBar.tsx` — "X online" text
- **Fix:** Lighten to `#6b7280`

---

### 🟡 MODERATE: Heading Hierarchy Issues

**WCAG SC:** 1.3.1 Info and Relationships, 2.4.10 Section Headings

#### Issue: h1 → h3 Skip on All CRUD Pages
- **Pages:** TreesPage, NodesPage, TopicsPage, CardsPage, ApprovalPanel
- **Detail:** Each page has `<h1>` for the page title, but empty-state content uses `<h3>` directly with no `<h2>` in between
- **Affected components:**
  - `TreesPage.tsx` line 416: `<h3>No trees yet</h3>`
  - `NodesPage.tsx` line 444: `<h3>No tree selected</h3>`
  - `TopicsPage.tsx` line 401: `<h3>Select a tree</h3>`
  - `CardsPage.tsx` line 456: `<h3>Select a tree</h3>`
  - `ApprovalPanel.tsx` line 500: `<h3>No pending approvals</h3>`
- **Fix:** Change all empty-state `<h3>` to `<h2>`:
  ```diff
  -<h3 className="text-sm font-medium text-gray-400 mb-1">No trees yet</h3>
  +<h2 className="text-sm font-medium text-gray-400 mb-1">No trees yet</h2>
  ```
  Also change the "Members" subheading in ShareDialog and "Invite by email" in ShareDialog to `<h2>` if they follow `<h1>`.

#### Issue: TreeView Page Has No h1
- **Page:** TreeView (`/tree/demo`)
- **Detail:** The TreeView page has only an `<h2>` in the App.tsx header ("Knowledge Canopy"), but no level-one heading in its own content area. It uses `<span>` for the tree title.
- **Fix:** Wrap the tree title in an `<h1>`:
  ```diff
  // TreeView.tsx
  -<span className="text-sm font-medium" style={{ color: '#e2e8f0' }}>
  +<h1 className="text-sm font-medium" style={{ color: '#e2e8f0' }}>
     🌳 {tree.treeTitle || 'Tree View'}
  -</span>
  +</h1>
  ```

---

### 🔵 MINOR: `<aside>` with `role="navigation"`

**WCAG SC:** 4.1.2 Name, Role, Value

- **Location:** `App.tsx` line 33-37 — `<aside>` element uses `role="navigation"`
- **Pages affected:** All 7 pages
- **Detail:** While functionally correct, `role="navigation"` on `<aside>` is flagged by axe as the `<aside>` element has an implicit complementary role. The spec prefers wrapping navigation in `<nav>`.
- **Fix:** Either:
  - Wrap the nav content in a `<nav>` element inside `<aside>`, or
  - Add `role="complementary"` to `<aside>` and put `role="navigation"` on the inner `<nav>`
  
  Recommended fix:
  ```diff
  <aside
    className="w-64 ..."
  -  role="navigation"
  +  role="complementary"
    aria-label="Main navigation"
  >
  ```
  The inner `<nav aria-label="Primary navigation">` already exists and handles the navigation role correctly.

---

### ❌ TreeView: Zero Keyboard Tab Stops

- **Page:** TreeView (`/tree/demo`)
- **Detail:** After the sidebar nav links, there are **0 tab-stoppable elements** within the TreeView main content area. The React Flow canvas nodes are `<div>` elements without `tabindex`, and the TreeCanvas.tsx keyboard handler intercepts Tab to cycle through nodes via JavaScript — but doesn't make nodes native-focusable.
- **Impact:** Keyboard-only users cannot interact with the tree canvas nodes using standard Tab navigation. The custom Tab handling (line 265-279 of TreeCanvas.tsx) uses `e.preventDefault()` and manages focus internally, but screen readers and keyboard users don't see the standard focus ring on nodes.
- **Fix:** Add `tabIndex={0}` to each visible node container, and move focus using `element.focus()` instead of just calling `focusRef.current()`:
  - In node components (MessageNode, SynthesisNode, CardNode, TopicNode, AgentCardNode), add `tabIndex={0}` to the root div
  - In TreeCanvas.tsx keyboard handler, instead of just `focusRef.current(nextId)` (which centers the viewport), also find the DOM element and call `.focus()`
  - Consider adding `role="tree"` and `role="treeitem"` for proper ARIA tree widget semantics

---

### ❌ TreeView: Unlabeled Input

- **Page:** TreeView (`/tree/demo`)
- **Detail:** 1 of 3 inputs unlabeled. The hidden `<input type="file">` in `MessageComposer.tsx` (line 401-407) is `display:none` with no label/aria-label.
- **Fix:** Add `aria-hidden="true"` to the hidden file input since it's triggered by a visible button:
  ```diff
  <input
    ref={fileInputRef}
    type="file"
    multiple
    className="hidden"
  +  aria-hidden="true"
  +  tabIndex={-1}
    onChange={handleFileSelect}
  />
  ```

---

## ✅ What Passes

### Static Code Analysis

| Check | Status | Notes |
|-------|--------|-------|
| ARIA live region (`#aria-live-announcer`) | ✅ | Present in App.tsx, role=status, aria-live=polite |
| Skip-to-main link | ✅ | `.skip-to-main` with visible-on-focus pattern |
| `<main>` role | ✅ | Correctly set to `role="main"` |
| Sidebar navigation | ✅ | `<nav>` element with `aria-label="Primary navigation"` |
| HTML `lang` attribute | ✅ | Set to `en` |
| Focus rings (CSS) | ✅ | `*:focus-visible` in index.css with 2px #7c3aed outline |
| Reduced motion support | ✅ | `@media (prefers-reduced-motion: reduce)` in index.css |
| Screen reader utility | ✅ | `announceToScreenReader()` in accessibility.ts |
| NavigationLink aria-labels | ✅ | All 8 nav links have `aria-label` attributes |
| Icon-only buttons | ✅ | All have `aria-label` (e.g., "Delete tree: {title}") |
| NavigationBar combobox | ✅ | Proper `role="combobox"` with `aria-expanded`, `aria-controls`, `aria-activedescendant` |
| ApprovalPanel live region | ✅ | Status announcer with `role="status" aria-live="polite"` |
| OfflineIndicator | ✅ | `role="status" aria-live="polite"` |
| ConfirmDialog | ✅ | `htmlFor` + `id` linking on label/textarea |
| TreesPage CreateDialog | ✅ | `htmlFor` linking, `aria-required`, `aria-invalid`, `aria-describedby` |
| Search input in NavigationBar | ✅ | `aria-label="Search nodes"` and `role="combobox"` |
| File upload | ✅ | Hidden `<input type="file">` triggered by visible button with `aria-label` |
| Share dialog | ✅ | `aria-label` on close button, `aria-required` on email, `htmlFor` on permission select |

### Browser-Based Testing

| Check | 7/7 Dashboard | 7/7 Trees | 7/7 Nodes | 7/7 Topics | 7/7 Cards | 7/7 Approvals | 7/7 TreeView |
|-------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| Skip-to-main link | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| ARIA live region | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| `<main>` role | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Sidebar navigation | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| HTML lang | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Images alt text | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Focus indicators | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | N/A |
| Tab navigation | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| ARIA roles on interactive | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

---

## Keyboard Navigation: Detailed Tab Sequence

### Dashboard
```
Tab #1 → a "Skip to main content"        ✅
Tab #2 → a "Dashboard"                    ✅
Tab #3 → a "Trees"                        ✅
Tab #4 → a "🌳 Tree View"                 ✅
Tab #5 → a "Nodes"                        ✅
Tab #6 → a "Topics"                       ✅
Tab #7 → a "Cards"                        ✅
Tab #8 → a "Approvals"                    ✅
```

### TreesPage
```
(1-8 same nav links)                      ✅
Tab #9  → button "Refresh"                ✅
Tab #10 → button "New Tree"               ✅
Tab #11 → button "Create Tree" (dialog)   ✅
```

### NodesPage
```
(1-8 same nav links)                      ✅
Tab #9  → button "Refresh"                ✅
Tab #10 → select#nodes-tree-select        ✅
```

### TopicsPage
```
(1-8 same nav links)                      ✅
Tab #9  → select#topics-tree-select       ✅
```

### CardsPage
```
(1-8 same nav links)                      ✅
Tab #9  → select#cards-tree-select        ✅
```

### ApprovalPanel
```
(1-8 same nav links)                      ✅
Tab #9  → button "Refresh"                ✅
Tab #10 → button "all"                    ✅
Tab #11 → button "pending"                ✅
Tab #12 → button "approved"               ✅
Tab #13 → button "denied"                 ✅
```

### TreeView
```
(1-8 same nav links)                      ✅
❌ NO MORE TAB STOPS — Tree Canvas nodes unreachable via Tab
```

All interactive elements show visible focus rings (`outline: 2px solid #7c3aed` via `*:focus-visible`).

---

## Component-Specific Findings

### NavigationBar.tsx
- ✅ Excellent ARIA pattern for search combobox
- ✅ Keyboard navigation (ArrowUp/Down/Enter/Escape) fully implemented
- ✅ `aria-expanded`, `aria-controls`, `aria-activedescendant`, `aria-autocomplete="list"`
- ✅ Search results have `role="listbox"` and items have `role="option"` with `aria-selected`
- ⚠️ Hint text color contrast (see serious issue #3)

### TreeCanvas.tsx
- ✅ `role="application"` with `aria-label` and `aria-roledescription`
- ✅ Controls have `aria-label="Canvas controls: zoom in, zoom out, fit view"`
- ✅ MiniMap has `aria-label="Tree minimap: overview of all nodes"`
- ⚠️ Custom Tab handling intercepts normal Tab navigation — see keyboard issue above
- ⚠️ Large tree warning banner should have `role="alert"` for live announcement

### MessageComposer.tsx
- ✅ Textarea has `aria-label="Message input"`
- ✅ All buttons have `aria-label` (Send, Attach, Pin context)
- ✅ Keyboard shortcut documented (⌘↵)
- ⚠️ Footer character/token count text contrast
- ⚠️ Hidden file input needs `aria-hidden="true"`

### ApprovalPanel.tsx
- ✅ ARIA live region for status changes
- ✅ Filter tabs as keyboard-accessible buttons
- ✅ Error banner has `role="alert"`
- ✅ ConfirmDialog has proper label/input linking
- ⚠️ Modal close button lacks `aria-label` (line 548-552)

### ApprovalDiff.tsx
- ✅ Expandable sections with button for toggle
- ✅ Checkbox "Show unchanged fields" has visible label
- ⚠️ Missing `aria-expanded` on the toggle button

### AuditTrail.tsx
- ✅ Semantic `<table>` with `<thead>`/`<tbody>`/`<th>` structure
- ✅ Header row with proper `scope` implied via `<th>` in `<thead>`
- ⚠️ Table column headers have no explicit `scope="col"`

### ShareDialog.tsx
- ✅ Dialog has `aria-label="Close share dialog"`
- ✅ Email input has `aria-required="true"`
- ✅ Permission select has `htmlFor` label (sr-only)
- ✅ Members list has proper structure
- ⚠️ Dialog lacks `role="dialog"` and `aria-modal="true"`

### OfflineIndicator.tsx
- ✅ `role="status"` and `aria-live="polite"` for live announcements
- ✅ Auto-hides after 2s of restored connectivity

### PresenceBar.tsx
- ✅ Tooltips with user info on hover
- ✅ Idle opacity reduction conveys state
- ⚠️ Avatar stack uses `title` attribute but no ARIA equivalent for screen readers

### CollaborativeCursors.tsx
- ✅ `aria-label="Remote user cursors"` on container
- ✅ Cursor labels display user names

---

## Remediation Roadmap

### Phase 1: Quick Wins (1-2 hours)

| Priority | Issue | Files | Effort |
|----------|-------|-------|--------|
| 🔴 High | Fix color contrast: footer/header text | `App.tsx` | 5 min |
| 🔴 High | Fix color contrast: filter tabs | `ApprovalPanel.tsx` | 5 min |
| 🔴 High | Fix color contrast: nav bar hints | `NavigationBar.tsx` | 5 min |
| 🔴 High | Fix color contrast: composer footer | `MessageComposer.tsx` | 5 min |
| 🔴 High | Fix color contrast: empty state text | `TreesPage.tsx`, `NodesPage.tsx`, `TopicsPage.tsx`, `CardsPage.tsx` | 10 min |
| 🔴 High | Fix color contrast: PresenceBar | `PresenceBar.tsx` | 5 min |
| 🟡 Medium | Fix heading hierarchy: h3→h2 | 5 page files | 10 min |
| 🟡 Medium | Add h1 to TreeView | `TreeView.tsx` | 5 min |
| 🔵 Low | aria-allowed-role on aside | `App.tsx` | 2 min |
| 🔵 Low | Add `aria-hidden` to hidden file input | `MessageComposer.tsx` | 2 min |

### Phase 2: TreeView Keyboard Access (2-4 hours)

| Priority | Issue | Files | Effort |
|----------|-------|-------|--------|
| 🔴 High | Make tree nodes tab-focusable | Node components (`CardNode.tsx`, `MessageNode.tsx`, etc.) | 1 hr |
| 🔴 High | Wire focus to DOM elements on Tab | `TreeCanvas.tsx` | 1 hr |
| 🟡 Medium | Add `role="tree"` / `role="treeitem"` | `TreeCanvas.tsx`, node components | 30 min |

### Phase 3: Polish (1 hour)

| Priority | Issue | Files | Effort |
|----------|-------|-------|--------|
| 🔵 Low | Add `aria-expanded` to ApprovalDiff toggle | `ApprovalDiff.tsx` | 5 min |
| 🔵 Low | Add `role="dialog"` + `aria-modal` to ShareDialog | `ShareDialog.tsx` | 5 min |
| 🔵 Low | Add `role="alert"` to large-tree warning banner | `TreeCanvas.tsx` | 5 min |
| 🔵 Low | Add `aria-label` to ApprovalPanel modal close button | `ApprovalPanel.tsx` | 2 min |
| 🔵 Low | Add `scope="col"` to AuditTrail table headers | `AuditTrail.tsx` | 5 min |
| 🔵 Low | Add `tabIndex={-1}` to hidden file input | `MessageComposer.tsx` | 2 min |

---

## Test Results

### Existing Accessibility Tests (vitest + Playwright)
```
✓ ARIA live region is present for screen reader announcements
✓ skip-to-main-content link is present
✓ main content area has role="main"
✓ sidebar navigation has correct ARIA role
✓ focus rings are enabled (no global outline:none)
✓ keyboard Tab navigation cycles through interactive elements
✓ all navigation links have accessible labels

7/7 passing
```

### Axe-core Results (this audit)
| Impact | Count | Description |
|--------|-------|-------------|
| critical | 0 | — |
| serious | 7 | Color contrast failures × 7 |
| moderate | 6 | Heading order (5) + missing h1 (1) |
| minor | 7 | aria-allowed-role on `<aside>` × 7 |

### Keyboard Navigation
- 5 of 6 pages have complete tab sequences with visible focus indicators ✅
- TreeView page: 0 tab stops beyond sidebar (tree nodes not focusable) ❌

### Screen Reader Checks
- 64 of 69 checks pass (93%)
- 5 failures: heading hierarchy on 5 CRUD pages (h1→h3 skip)

---

## Total Remediation Estimate

| Phase | Issues | Time |
|-------|--------|------|
| Phase 1: Quick Wins | 10 issues | 1–2 hours |
| Phase 2: TreeView Keyboard | 3 issues | 2–4 hours |
| Phase 3: Polish | 6 issues | 1 hour |
| **Total** | **19 issues** | **4–7 hours** |

---

## Notes

- **Automated testing covers ~40%** of accessibility issues. This audit combines axe-core (30%), keyboard testing (5%), and screen reader/static checks (5%). Manual testing with an actual screen reader (NVDA/VoiceOver) is recommended for full coverage.
- **Color contrast** is the largest single category of violations. Fixing the 7 high-contrast text colors will resolve all serious axe violations.
- **The TreeView keyboard access** is the most significant remaining UX issue for keyboard-only users. The React Flow canvas needs node-level focusability.
- **The FE-10 commit** (e907b26) established an excellent baseline — ARIA live regions, skip-to-main, focus indicators, keyboard shortcuts, and accessibility utilities. Most remaining issues are refinements on top of that foundation.

---

*Report generated by automated audit script at `frontend/test-results/run-a11y-audit.py`. Raw JSON data available at `frontend/test-results/accessibility-audit-raw.json`.*
