/**
 * Hermes Canopy — Node list hierarchy (UI-08)
 *
 * The Nodes page rendered `GET /trees/{id}/nodes` as a flat stack, which
 * throws away the one thing the payload already tells us: `parentId`.
 * Six rows all reading "Depth 1" say *that* they are siblings but never
 * *whose* — you have to cross-reference ids by eye. This module turns the
 * flat list into an ordered, indented walk with guide-line geometry.
 *
 * Everything here is pure set/graph arithmetic over the list, so the page
 * only paints. Three properties are load-bearing and unit-tested:
 *
 *   totality    every input node appears exactly once in the output. A
 *               node whose parent is missing from the list (a filtered
 *               subtree, a soft-deleted parent) is an ORPHAN and renders
 *               at depth 0 rather than vanishing.
 *   cycle-safe  the DAG legitimately carries multi-parent synthesis nodes
 *               and bad data can carry a genuine cycle. A naive recursive
 *               walk would revisit or hang; this one carries a `seen` set
 *               and sweeps up anything the walk could not reach.
 *   local depth the RENDERED depth is derived from the list, not from the
 *               server's `depth` column. A search that hides a parent must
 *               not leave its child indented into empty space.
 */

// ─── Types ─────────────────────────────────────────────────────────────

/** Minimal node shape — `NodeDetail` from the list endpoint satisfies it. */
export interface HierarchyNode {
  id: string;
  parentId?: string | null;
}

/** One rendered row: the node plus everything the indent chrome needs. */
export interface HierarchyRow<T extends HierarchyNode> {
  node: T;
  id: string;
  /** Nesting level in the RENDERED tree. Roots and orphans are 0. */
  depth: number;
  /** True when at least one child of this node is also in the list. */
  hasChildren: boolean;
  /** True when this node is the last child of its parent (draws `└`). */
  isLastChild: boolean;
  /**
   * One entry per ancestor level, outermost first. `true` means that
   * ancestor still has a following sibling, so a vertical guide line has
   * to continue through this row at that level. Length always === depth.
   */
  ancestorLines: readonly boolean[];
  /** True when the node's parent is absent from the list. */
  isOrphan: boolean;
}

/** Guide-line geometry for one row — purely decorative, `aria-hidden`. */
export type RailSegment = 'line' | 'space';

export interface RowRails {
  /** One segment per ancestor level, outermost first. */
  ancestors: readonly RailSegment[];
  /** The node's own connector: `tee` = `├`, `end` = `└`, null at root. */
  elbow: 'tee' | 'end' | null;
}

// ─── Construction ──────────────────────────────────────────────────────

/**
 * Group nodes by parent, preserving input order.
 *
 * A node is treated as a ROOT when it has no `parentId` *or* when that
 * parent is not present in the list. Both cases render at depth 0; only
 * the second is flagged `isOrphan`, because "root of the tree" and
 * "parent got filtered out" look identical on screen but are not the
 * same fact.
 */
function partition<T extends HierarchyNode>(
  nodes: readonly T[],
): {
  byId: Map<string, T>;
  children: Map<string, string[]>;
  roots: string[];
  orphans: Set<string>;
} {
  const byId = new Map<string, T>();
  for (const node of nodes) {
    if (node && typeof node.id === 'string' && node.id && !byId.has(node.id)) {
      byId.set(node.id, node);
    }
  }

  const children = new Map<string, string[]>();
  const roots: string[] = [];
  const orphans = new Set<string>();

  for (const node of nodes) {
    const id = node?.id;
    if (!id || byId.get(id) !== node) continue; // skip dupes / malformed

    const parentId = node.parentId ?? null;
    // Self-parenting is data corruption, not a root — treat it as an orphan
    // so the node still renders instead of building a 1-cycle.
    if (!parentId || parentId === id || !byId.has(parentId)) {
      roots.push(id);
      if (parentId && parentId !== id) orphans.add(id);
      else if (parentId === id) orphans.add(id);
      continue;
    }

    const siblings = children.get(parentId);
    if (siblings) siblings.push(id);
    else children.set(parentId, [id]);
  }

  return { byId, children, roots, orphans };
}

/**
 * Flatten a node list into an ordered, indented walk.
 *
 * Sibling order follows input order, which is the endpoint's
 * `sequence_num` ordering — so the visual tree matches the order the
 * conversation actually happened in.
 */
export function buildHierarchy<T extends HierarchyNode>(
  nodes: readonly T[],
): HierarchyRow<T>[] {
  const { byId, children, roots, orphans } = partition(nodes);
  const rows: HierarchyRow<T>[] = [];
  const seen = new Set<string>();

  const emit = (
    id: string,
    depth: number,
    ancestorLines: readonly boolean[],
    isLastChild: boolean,
    isOrphan: boolean,
  ) => {
    const node = byId.get(id);
    if (!node || seen.has(id)) return;
    seen.add(id);

    const kids = children.get(id) ?? [];
    rows.push({
      node,
      id,
      depth,
      hasChildren: kids.length > 0,
      isLastChild,
      ancestorLines,
      isOrphan,
    });

    kids.forEach((childId, i) => {
      // A line continues through the child's rows at THIS node's level
      // exactly when this node still has a following sibling.
      emit(
        childId,
        depth + 1,
        [...ancestorLines, !isLastChild],
        i === kids.length - 1,
        false,
      );
    });
  };

  roots.forEach((id, i) => {
    emit(id, 0, [], i === roots.length - 1, orphans.has(id));
  });

  // Cycle sweep: anything the walk could not reach (every node in a cycle
  // has an in-list parent, so none of them is a root) still has to render.
  for (const node of nodes) {
    const id = node?.id;
    if (!id || seen.has(id) || byId.get(id) !== node) continue;
    seen.add(id);
    rows.push({
      node,
      id,
      depth: 0,
      hasChildren: (children.get(id) ?? []).length > 0,
      isLastChild: true,
      ancestorLines: [],
      isOrphan: true,
    });
  }

  return rows;
}

// ─── Guide lines ───────────────────────────────────────────────────────

/**
 * Rail segments for one row. Decorative only — the real parent/child
 * relationship is carried to assistive tech by `aria-level` on the row.
 */
export function rowRails<T extends HierarchyNode>(
  row: HierarchyRow<T>,
): RowRails {
  return {
    ancestors: row.ancestorLines.map((live) => (live ? 'line' : 'space')),
    elbow: row.depth === 0 ? null : row.isLastChild ? 'end' : 'tee',
  };
}

// ─── Search ────────────────────────────────────────────────────────────

/** Outcome of a filtered hierarchy build. */
export interface FilteredHierarchy<T extends HierarchyNode> {
  rows: HierarchyRow<T>[];
  /** Ids that matched the predicate themselves (not pulled in as context). */
  matched: Set<string>;
}

/**
 * Filter, then RE-HIERARCHISE.
 *
 * Filtering a tree has two defensible answers: flatten the matches, or
 * keep them in place. This takes the second — a match keeps every
 * ancestor visible so you can still see *where* in the conversation the
 * hit lives, which is the entire reason the page grew a hierarchy. Those
 * ancestors are reported separately in `matched` so the page can dim them
 * as context rather than pretending they are hits.
 *
 * The surviving set is re-walked rather than having rows pruned out of an
 * existing walk: dropping a leaf changes which of its siblings is now
 * "last", and reusing stale geometry leaves dangling guide lines.
 */
export function filterHierarchy<T extends HierarchyNode>(
  nodes: readonly T[],
  predicate: (node: T) => boolean,
): FilteredHierarchy<T> {
  const byId = new Map<string, T>();
  for (const node of nodes) {
    if (node?.id && !byId.has(node.id)) byId.set(node.id, node);
  }

  const matched = new Set<string>();
  const keep = new Set<string>();

  for (const node of nodes) {
    if (!node?.id || byId.get(node.id) !== node) continue;
    if (!predicate(node)) continue;

    matched.add(node.id);
    keep.add(node.id);

    // Walk up, guarding against a cycle in the parent chain.
    let cursor: string | null | undefined = node.parentId ?? null;
    const guard = new Set<string>([node.id]);
    while (cursor && byId.has(cursor) && !guard.has(cursor)) {
      guard.add(cursor);
      keep.add(cursor);
      cursor = byId.get(cursor)?.parentId ?? null;
    }
  }

  const survivors = nodes.filter((n) => n?.id && keep.has(n.id));
  return { rows: buildHierarchy(survivors), matched };
}
