/**
 * Hermes Canopy — Reply-count derivation (UI-04, Phase 11 Mockup Parity)
 *
 * Every card in the mockup wears a "💬 n" badge. The number is the node's
 * *direct* reply count — the branching canvas already shows depth
 * spatially, so a total-descendant count would double-tell the same story
 * and inflate the root to "everything".
 *
 * Counts are derived from the graph the canvas is already holding, never
 * hardcoded and never fetched separately: the edge list IS the answer.
 * `GET /trees/{id}/nodes` (BUG-026) can supply an authoritative override
 * when it is loaded, which `mergeReplyCounts` folds in.
 */

import { buildChildMap, childrenOf, descendantsOf, type ChildEdge, type ChildMap } from './treeCollapse';

// ─── Types ─────────────────────────────────────────────────────────────

/** node id → number of direct replies. */
export type ReplyCounts = ReadonlyMap<string, number>;

/** Shape of a node row from `GET /trees/{tree_id}/nodes`. */
export interface NodeCountSource {
  id: string;
  /** Server-computed direct child count, when the payload carries one. */
  childCount?: number;
}

// ─── Derivation ────────────────────────────────────────────────────────

/**
 * Direct reply count per node, derived from edges.
 *
 * Nodes with no replies are present in the map with `0` when their id is
 * supplied via `nodeIds`, so a caller can distinguish "leaf" (0, badge
 * hidden) from "unknown" (absent).
 */
export function deriveReplyCounts(
  edges: readonly ChildEdge[],
  nodeIds: readonly string[] = [],
): ReplyCounts {
  const childMap = buildChildMap(edges);
  const counts = new Map<string, number>();

  for (const id of nodeIds) counts.set(id, 0);
  for (const [parentId, children] of childMap) {
    counts.set(parentId, children.length);
  }

  return counts;
}

/** Direct reply count for one node — 0 when it is a leaf or unknown. */
export function replyCountFor(counts: ReplyCounts, nodeId: string): number {
  return counts.get(nodeId) ?? 0;
}

/**
 * Total nodes beneath a node (all descendants, not just direct replies).
 * Used for the "n hidden" affordance on a collapsed chevron, where the
 * whole subtree really is what disappeared.
 */
export function deriveSubtreeSize(childMap: ChildMap, nodeId: string): number {
  return descendantsOf(childMap, nodeId).size;
}

/**
 * Overlay authoritative server counts on top of the locally derived ones.
 *
 * The local Yjs replica can lag the server (a reply created in another
 * session has not synced yet), so where the API states a count we trust
 * it; everywhere else the derived value stands. Negative or non-finite
 * values from a malformed payload are ignored rather than rendered.
 */
export function mergeReplyCounts(
  derived: ReplyCounts,
  serverNodes: readonly NodeCountSource[],
): ReplyCounts {
  const merged = new Map(derived);
  for (const node of serverNodes) {
    if (!node || typeof node.id !== 'string') continue;
    const count = node.childCount;
    if (typeof count !== 'number' || !Number.isFinite(count) || count < 0) {
      continue;
    }
    merged.set(node.id, Math.floor(count));
  }
  return merged;
}

// ─── Presentation ──────────────────────────────────────────────────────

/**
 * Badge text for a reply count, or null when the badge should be omitted.
 * A leaf shows nothing — an explicit "0 replies" is noise on a canvas.
 */
export function replyBadgeLabel(count: number): string | null {
  if (!Number.isFinite(count) || count <= 0) return null;
  return count > 99 ? '99+' : String(Math.floor(count));
}

/** Screen-reader text for the badge ("3 replies"). */
export function replyBadgeAriaLabel(count: number): string {
  const n = Math.max(0, Math.floor(count));
  return `${n} ${n === 1 ? 'reply' : 'replies'}`;
}

/** Convenience: direct children of a node from a prebuilt map. */
export { childrenOf };
