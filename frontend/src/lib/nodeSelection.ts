/**
 * Hermes Canopy — Bulk-selection algebra (UI-08)
 *
 * The Nodes page grows a checkbox per row and a sticky action bar that
 * appears once anything is checked. All of that is set arithmetic, so it
 * lives here as pure functions and the page keeps one `Set<string>` in
 * state.
 *
 * Two behaviours are easy to get wrong and are pinned by tests:
 *
 *   survival    the list re-renders constantly (search keystrokes, a
 *               refetch after a delete). Selection must survive those,
 *               but must NOT outlive the rows themselves — a selected id
 *               whose node was deleted would silently ride along into the
 *               next bulk delete. `pruneSelection` runs on every list
 *               change; it is the reason selection is stored as ids and
 *               not as nodes.
 *   tri-state   the header checkbox is checked / unchecked / indeterminate
 *               against the VISIBLE rows, not the whole tree, so a search
 *               that hides half the list cannot make "select all" lie.
 */

import { canMerge, mergeDisabledReason } from './merge.ts';

// ─── Toggling ──────────────────────────────────────────────────────────

/**
 * Flip one id. Returns a NEW set — identity change is the re-render
 * signal, same contract as `treeCollapse.toggleCollapsed`.
 */
export function toggleSelection(
  selection: ReadonlySet<string>,
  id: string,
): Set<string> {
  const next = new Set(selection);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  return next;
}

/** Add every id (idempotent). */
export function selectAll(
  selection: ReadonlySet<string>,
  ids: Iterable<string>,
): Set<string> {
  const next = new Set(selection);
  for (const id of ids) if (id) next.add(id);
  return next;
}

/** Remove every id, leaving selections outside `ids` untouched. */
export function deselectAll(
  selection: ReadonlySet<string>,
  ids: Iterable<string>,
): Set<string> {
  const next = new Set(selection);
  for (const id of ids) next.delete(id);
  return next;
}

/** Empty the selection entirely (the bulk bar's dismiss). */
export function clearSelection(): Set<string> {
  return new Set<string>();
}

/**
 * Drop ids that are no longer in the list.
 *
 * Called whenever the node list changes. Without it a deleted node's id
 * lingers and the bulk bar counts rows that are not on screen.
 */
export function pruneSelection(
  selection: ReadonlySet<string>,
  liveIds: Iterable<string>,
): Set<string> {
  const live = liveIds instanceof Set ? liveIds : new Set(liveIds);
  const next = new Set<string>();
  for (const id of selection) if (live.has(id)) next.add(id);
  return next;
}

// ─── Header checkbox ───────────────────────────────────────────────────

/** Tri-state for the "select all visible" control. */
export type SelectAllState = 'none' | 'some' | 'all';

/**
 * State of the header checkbox against the currently VISIBLE ids.
 * An empty list is 'none' — nothing to select, nothing to indeterminate.
 */
export function selectAllState(
  selection: ReadonlySet<string>,
  visibleIds: readonly string[],
): SelectAllState {
  if (visibleIds.length === 0) return 'none';

  let hits = 0;
  for (const id of visibleIds) if (selection.has(id)) hits++;

  if (hits === 0) return 'none';
  return hits === visibleIds.length ? 'all' : 'some';
}

/**
 * What the header checkbox should do when clicked: anything short of a
 * full selection selects the rest, a full selection clears it. This is
 * the behaviour every list UI has, stated once so the page cannot invert
 * it by accident.
 */
export function toggleAllVisible(
  selection: ReadonlySet<string>,
  visibleIds: readonly string[],
): Set<string> {
  return selectAllState(selection, visibleIds) === 'all'
    ? deselectAll(selection, visibleIds)
    : selectAll(selection, visibleIds);
}

// ─── Bulk actions ──────────────────────────────────────────────────────

/** Identifier for a bulk action the bar can offer. */
export type BulkActionId = 'delete' | 'merge' | 'tag';

/** One button in the bulk-action bar. */
export interface BulkAction {
  id: BulkActionId;
  label: string;
  /** False when the action cannot run for this selection. */
  enabled: boolean;
  /**
   * Why the action is disabled — rendered as the button's `title` and
   * accessible description. Null when enabled.
   */
  reason: string | null;
  /** True when the action destroys data (paints danger, confirms first). */
  destructive: boolean;
}

/**
 * The bulk-action bar's buttons for a given selection size.
 *
 * DELETE is per-row (`DELETE /nodes/{id}`, soft delete, already wired on
 * this page). MERGE is tree-scoped and live:
 * `POST /trees/{tree_id}/merge` (SPEC-API-04 §3, since GAP-078) creates one
 * synthesis node from 2–100 source nodes — so it is enabled exactly in that
 * range and disabled, with the server's own reason, outside it. The bounds
 * come from `lib/merge.canMerge`, not from numbers repeated here, so the
 * disabled button and the refused request cannot disagree. A merge is
 * additive (a new synthesis node; nothing is deleted), hence not
 * `destructive`.
 *
 * TAG stays disabled with a stated reason, because there is still nothing
 * behind it: topics exist (`/topics` CRUD) but nothing associates an
 * EXISTING node with a topic in bulk — topic linkage is carried in node
 * metadata at create time. A disabled control that says why is a roadmap;
 * a button wired to a guessed `POST /nodes/merge` is a 404 in front of the
 * user.
 *
 * Verified against `internal/server/server.go` (the merge mount, line 350),
 * `merge_handler.go`, `node_handler.go`, `graph_handler.go`,
 * `topic_handler.go` and `docs/API.md` §"Merge Tree (Create Synthesis
 * Node)".
 */
export function bulkActions(count: number): BulkAction[] {
  const n = Number.isFinite(count) ? count : 0;
  const mergeable = canMerge(n);
  return [
    {
      id: 'merge',
      label: 'Merge',
      enabled: mergeable,
      reason: mergeable ? null : mergeDisabledReason(n),
      destructive: false,
    },
    {
      id: 'tag',
      label: 'Tag',
      enabled: false,
      reason: 'Coming soon — no bulk tag endpoint yet',
      destructive: false,
    },
    {
      id: 'delete',
      label: 'Delete',
      enabled: n > 0,
      reason: n > 0 ? null : 'Select at least one node',
      destructive: true,
    },
  ];
}

/** True when the bulk bar should be mounted at all. */
export function isBulkBarVisible(selection: ReadonlySet<string>): boolean {
  return selection.size > 0;
}
