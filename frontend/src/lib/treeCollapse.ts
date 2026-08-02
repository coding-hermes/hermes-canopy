/**
 * Hermes Canopy — Collapse-state algebra (UI-04, Phase 11 Mockup Parity)
 *
 * The branching canvas hides whole subtrees behind a chevron on the node
 * that owns them (docs/mockups/mockup-1.png — Branch C is collapsed while
 * A and B are open). All of that is set arithmetic over a parent→children
 * map, so it lives here as pure functions: the canvas keeps one
 * `Set<string>` of collapsed node ids in React state and asks this module
 * what that implies.
 *
 * Every function is total and cycle-safe. The DAG can legitimately contain
 * multi-parent synthesis nodes, so a naive recursive walk would revisit —
 * or with bad data, loop forever. The walkers here carry a `seen` set.
 */

// ─── Types ─────────────────────────────────────────────────────────────

/** Minimal edge shape — React Flow edges and Yjs edges both satisfy it. */
export interface ChildEdge {
  source: string;
  target: string;
}

/** parent id → ordered child ids. */
export type ChildMap = ReadonlyMap<string, readonly string[]>;

// ─── Construction ──────────────────────────────────────────────────────

/**
 * Build the parent→children adjacency used by every other helper here.
 * Duplicate edges collapse to a single child entry; order is preserved.
 */
export function buildChildMap(edges: readonly ChildEdge[]): ChildMap {
  const map = new Map<string, string[]>();
  for (const edge of edges) {
    if (!edge || !edge.source || !edge.target) continue;
    const list = map.get(edge.source);
    if (list) {
      if (!list.includes(edge.target)) list.push(edge.target);
    } else {
      map.set(edge.source, [edge.target]);
    }
  }
  return map;
}

/** Direct children of a node (empty array when it is a leaf). */
export function childrenOf(childMap: ChildMap, nodeId: string): readonly string[] {
  return childMap.get(nodeId) ?? [];
}

/** True when the node owns at least one child and can therefore collapse. */
export function isCollapsible(childMap: ChildMap, nodeId: string): boolean {
  return childrenOf(childMap, nodeId).length > 0;
}

// ─── Descendants ───────────────────────────────────────────────────────

/**
 * Every transitive descendant of `nodeId`, excluding the node itself.
 * Cycle-safe: a node already visited is never expanded twice.
 */
export function descendantsOf(childMap: ChildMap, nodeId: string): Set<string> {
  const out = new Set<string>();
  const stack = [...childrenOf(childMap, nodeId)];

  while (stack.length > 0) {
    const current = stack.pop()!;
    if (current === nodeId || out.has(current)) continue;
    out.add(current);
    for (const child of childrenOf(childMap, current)) {
      if (!out.has(child) && child !== nodeId) stack.push(child);
    }
  }

  return out;
}

// ─── Set operations on collapse state ──────────────────────────────────

/**
 * Toggle one node's collapsed flag. Returns a NEW set — callers pass it
 * straight to `setState`, so identity change is the re-render signal.
 */
export function toggleCollapsed(
  collapsed: ReadonlySet<string>,
  nodeId: string,
): Set<string> {
  const next = new Set(collapsed);
  if (next.has(nodeId)) {
    next.delete(nodeId);
  } else {
    next.add(nodeId);
  }
  return next;
}

/** Collapse a node and every collapsible node beneath it. */
export function collapseSubtree(
  collapsed: ReadonlySet<string>,
  childMap: ChildMap,
  nodeId: string,
): Set<string> {
  const next = new Set(collapsed);
  if (isCollapsible(childMap, nodeId)) next.add(nodeId);
  for (const id of descendantsOf(childMap, nodeId)) {
    if (isCollapsible(childMap, id)) next.add(id);
  }
  return next;
}

/** Expand a node and everything beneath it. */
export function expandSubtree(
  collapsed: ReadonlySet<string>,
  childMap: ChildMap,
  nodeId: string,
): Set<string> {
  const next = new Set(collapsed);
  next.delete(nodeId);
  for (const id of descendantsOf(childMap, nodeId)) next.delete(id);
  return next;
}

/**
 * Drop collapse entries for nodes that no longer exist.
 *
 * Layout re-runs constantly (every Yjs change rebuilds the snapshot), and
 * the collapsed set must survive those — but not outlive deleted nodes, or
 * a recycled id would silently come back collapsed.
 */
export function pruneCollapsed(
  collapsed: ReadonlySet<string>,
  liveNodeIds: Iterable<string>,
): Set<string> {
  const live = liveNodeIds instanceof Set ? liveNodeIds : new Set(liveNodeIds);
  const next = new Set<string>();
  for (const id of collapsed) {
    if (live.has(id)) next.add(id);
  }
  return next;
}

// ─── Visibility ────────────────────────────────────────────────────────

/**
 * Node ids hidden because *some ancestor* is collapsed.
 *
 * A collapsed node is itself still visible — it is the thing you click to
 * expand again — so this is exactly the union of every collapsed node's
 * descendants. A collapsed node nested under another collapsed node lands
 * in that union on its own and is correctly hidden.
 */
export function hiddenNodeIds(
  childMap: ChildMap,
  collapsed: ReadonlySet<string>,
): Set<string> {
  const hidden = new Set<string>();
  for (const rootId of collapsed) {
    for (const id of descendantsOf(childMap, rootId)) {
      hidden.add(id);
    }
  }
  return hidden;
}

/** How many nodes a given collapsed node is currently hiding. */
export function hiddenCountFor(childMap: ChildMap, nodeId: string): number {
  return descendantsOf(childMap, nodeId).size;
}

/** Human-readable summary for the canvas' "n branches collapsed" pill. */
export function summarizeCollapsed(
  childMap: ChildMap,
  collapsed: ReadonlySet<string>,
): { branches: number; hidden: number } {
  return {
    branches: collapsed.size,
    hidden: hiddenNodeIds(childMap, collapsed).size,
  };
}
