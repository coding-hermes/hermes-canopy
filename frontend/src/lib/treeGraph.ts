/**
 * Hermes Canopy — Graph subtree wire shapes + normalisation (SPEC-PL-06 §7.1)
 *
 * `GET /api/v1/graph/trees/{tree_id}/subtree/{node_id}` is the only endpoint
 * that returns EDGES with their persisted `edges.id` and their JSONB
 * metadata, which §7.1 requires ("must use React Flow edge IDs from
 * persisted database IDs") and §5.2 needs (`color_key` is server-computed).
 *
 * The endpoint is a wire contract, not a replica shape:
 *
 *   - it speaks snake_case (`source_id`, `edge_type`) while the REST node
 *     payloads the replica already consumes are camelCase;
 *   - `nodes[].type` is the graph TYPE, `parent_id` is the display anchor and
 *     the summary carries NO metadata (the node's `metadata.multi_reference`
 *     comes from `GET /trees/{id}/nodes`, BUG-032's richer payload);
 *   - `edges[].metadata` is absent on every row whose column held `{}`.
 *
 * Normalising here keeps `treeStore` about the replica and this module about
 * the payload — and makes both unit-testable without a network.
 */

// ─── Wire shapes ───────────────────────────────────────────────────────

/** One node of `service.GraphQueryResult`, as JSON. */
export interface RawSubtreeNode {
  id?: string | null;
  tree_id?: string | null;
  parent_id?: string | null;
  type?: string | null;
  depth?: number | null;
  created_at?: string | null;
  content?: string | null;
}

/** One edge of `service.GraphQueryResult` — the additive §7.1 fields. */
export interface RawSubtreeEdge {
  id?: string | null;
  source_id?: string | null;
  target_id?: string | null;
  edge_type?: string | null;
  depth?: number | null;
  /** Decoded JSONB; absent when the column held `{}` (Go `omitempty`). */
  metadata?: Record<string, unknown> | null;
}

export interface RawSubtree {
  nodes?: RawSubtreeNode[] | null;
  edges?: RawSubtreeEdge[] | null;
}

// ─── Replica payload shapes ────────────────────────────────────────────

/**
 * An edge as it enters the local replica. `id` is the PERSISTED `edges.id`
 * (§7.1) — never a locally generated one.
 */
export interface TreeEdgePayload {
  id: string;
  sourceId: string;
  targetId: string;
  edgeType: string;
  metadata: Record<string, unknown>;
  createdAt?: string;
}

/** A graph node, in the camelCase shape the replica's merge already eats. */
export interface TreeGraphNodePayload {
  id: string;
  treeId: string;
  parentId: string | null;
  content: string;
  nodeType: string;
  createdAt?: string;
}

// ─── Requests ──────────────────────────────────────────────────────────

/**
 * Path passed to `apiGet` for the whole-tree graph read. `max_depth=0` means
 * unbounded (handler default) and is sent explicitly so the request does not
 * depend on server defaults.
 */
export function subtreeRequestPath(
  treeId: string,
  rootNodeId: string,
  maxDepth = 0,
): string {
  const depth = Number.isFinite(maxDepth) && maxDepth >= 0 ? Math.floor(maxDepth) : 0;
  return `/graph/trees/${encodeURIComponent(treeId)}/subtree/${encodeURIComponent(
    rootNodeId,
  )}?max_depth=${depth}`;
}

/**
 * The tree's root node id, derived LOCALLY from an already-fetched node list
 * (§7.1 work item B1: no extra route). `parent_id === null` is the tree
 * root; the first such node wins so the choice is deterministic when a
 * payload somehow carries several.
 *
 * Accepts either spelling — the graph read answers in snake_case while
 * `GET /trees/{id}/nodes` answers in camelCase, and both lists are on hand
 * when the graph read is issued.
 */
export function rootNodeIdOf(
  nodes: readonly {
    id?: string | null;
    parentId?: string | null;
    parent_id?: string | null;
  }[] | null | undefined,
): string | null {
  if (!Array.isArray(nodes)) return null;
  for (const node of nodes) {
    const id = clean(node?.id);
    const parent = node?.parentId !== undefined ? node.parentId : node?.parent_id;
    if (id && (parent === null || parent === undefined)) return id;
  }
  return null;
}

// ─── Normalisation ─────────────────────────────────────────────────────

/** Graph nodes → the camelCase payload `/trees/{id}/nodes` already produces. */
export function toGraphNodePayloads(
  nodes: readonly RawSubtreeNode[] | null | undefined,
): TreeGraphNodePayload[] {
  if (!Array.isArray(nodes)) return [];
  const out: TreeGraphNodePayload[] = [];
  for (const node of nodes) {
    const id = clean(node?.id);
    if (!id) continue;
    out.push({
      id,
      treeId: clean(node?.tree_id),
      parentId: node?.parent_id ? String(node.parent_id) : null,
      content: typeof node?.content === 'string' ? node.content : '',
      nodeType: clean(node?.type) || 'message',
      ...(node?.created_at ? { createdAt: String(node.created_at) } : {}),
    });
  }
  return out;
}

/**
 * Graph edges → replica payloads, preserving the persisted id and metadata.
 *
 * A row with no metadata yields `{}` (never a hole), and a row without an
 * `edge_type` degrades to `reply` — the same default the schema applies. Rows
 * missing an id or an endpoint are dropped: an edge the replica cannot
 * address or wire is not renderable, and inventing an id is exactly what §7.1
 * forbids.
 */
export function toEdgePayloads(
  edges: readonly RawSubtreeEdge[] | null | undefined,
): TreeEdgePayload[] {
  if (!Array.isArray(edges)) return [];
  const out: TreeEdgePayload[] = [];
  for (const edge of edges) {
    const id = clean(edge?.id);
    const sourceId = clean(edge?.source_id);
    const targetId = clean(edge?.target_id);
    if (!id || !sourceId || !targetId) continue;
    out.push({
      id,
      sourceId,
      targetId,
      edgeType: clean(edge?.edge_type) || 'reply',
      metadata: isPlainObject(edge?.metadata) ? (edge.metadata as Record<string, unknown>) : {},
    });
  }
  return out;
}

function clean(raw: unknown): string {
  return typeof raw === 'string' ? raw.trim() : '';
}

function isPlainObject(raw: unknown): boolean {
  return typeof raw === 'object' && raw !== null && !Array.isArray(raw);
}
