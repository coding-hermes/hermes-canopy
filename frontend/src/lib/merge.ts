/**
 * Hermes Canopy — Synthesis (merge) client contract
 *
 * The Nodes page's bulk bar offers a Merge action. The route behind it is
 * live and is the ONLY way to create a synthesis node (SPEC-API-04 §3,
 * shipped in GAP-078):
 *
 *     POST /api/v1/trees/{tree_id}/merge
 *     201 → { node, edges, merged_source_ids }
 *
 * This module owns the client half of that contract so the dialog stays a
 * paint layer. Three things are load-bearing:
 *
 *   bounds     2–100 unique source ids. These are the SERVER's numbers —
 *              `mergeMinSources`/`mergeMaxSources` in
 *              `internal/service/merge_service.go` and the table in
 *              `docs/API.md` §"Merge Tree". They are stated once, here;
 *              `nodeSelection.bulkActions` imports `canMerge`, so the
 *              disabled button and the refused request cannot drift apart.
 *   no guessing a selection the server is KNOWN to reject (one source, 101
 *              sources, a blank or duplicated id, an unknown format) never
 *              becomes a request. Sending it would turn a fact the client
 *              already holds into a 400 in front of the user — with the
 *              selection still intact behind the error.
 *   boundary   the 201 payload is snake_case (§9 HTTP boundary rule) while
 *              the node list this page renders is camelCase; `normalize*`
 *              is where that translation happens, so no component
 *              hand-reads a loosely typed response.
 *
 * `content` is REQUIRED and may be empty, so it is always sent as a string —
 * never `undefined`, which the server answers with INVALID_BODY.
 */

// ─── Bounds (the server's own numbers) ─────────────────────────────────

/** Minimum source nodes the server accepts (§3.2 — MIN_SOURCE_NODES below). */
export const MERGE_MIN_SOURCES = 2;

/** Maximum source nodes the server accepts (§3.2 — MAX_SOURCE_NODES above). */
export const MERGE_MAX_SOURCES = 100;

/** `content_format` values the server accepts (§3.2). */
export const MERGE_CONTENT_FORMATS = ['markdown', 'plain', 'rich'] as const;

/** The format sent when the caller does not name one (§3.2 default). */
export const MERGE_DEFAULT_CONTENT_FORMAT: MergeContentFormat = 'markdown';

export type MergeContentFormat = (typeof MERGE_CONTENT_FORMATS)[number];

// ─── Count gate (the single source of truth for the bounds) ────────────

/**
 * True when `count` selected rows form a merge the server will accept.
 * Non-finite counts are treated as zero — the same defensive coercion
 * `bulkActions` applies, so a NaN count can never read as "in range".
 */
export function canMerge(count: number): boolean {
  return (
    Number.isFinite(count) &&
    count >= MERGE_MIN_SOURCES &&
    count <= MERGE_MAX_SOURCES
  );
}

/**
 * Why Merge is unavailable for this selection — the button's `title` and
 * accessible description. Null when it is available. Kept beside the
 * bounds so the wording and the limits cannot disagree.
 */
export function mergeDisabledReason(count: number): string | null {
  if (canMerge(count)) return null;
  const n = Number.isFinite(count) ? count : 0;
  return n < MERGE_MIN_SOURCES
    ? `Select at least ${MERGE_MIN_SOURCES} nodes to merge`
    : `Merge supports at most ${MERGE_MAX_SOURCES} source nodes`;
}

// ─── Request ───────────────────────────────────────────────────────────

/** Optional parts of a merge request; only `content` is ever defaulted. */
export interface MergeRequestOptions {
  /** Synthesis summary. May be empty — the server accepts it. */
  content?: string;
  /** Defaults to `markdown`. */
  contentFormat?: MergeContentFormat;
  /** Placement. Omitted → the server places the node at the tree root. */
  targetParentId?: string;
  metadata?: Record<string, unknown>;
}

/** The §3.2 request body verbatim (snake_case on this boundary). */
export interface MergeRequestBody {
  source_node_ids: string[];
  content: string;
  content_format: MergeContentFormat;
  target_parent_id?: string;
  metadata?: Record<string, unknown>;
}

/**
 * A built request, or the reason one could not be built. The failure is a
 * value rather than a thrown error because "1 node is selected" is not an
 * exception — it is a state the caller renders.
 */
export type MergeRequestResult =
  | { ok: true; body: MergeRequestBody }
  | { ok: false; reason: string };

/**
 * Map an ordered selection to the §3.2 body, preserving the caller's order
 * (`source_node_ids` is echoed back in `merged_source_ids` and drives the
 * order the synthesis edges are created in).
 *
 * Refuses anything the server would reject for a reason the client already
 * knows: a count outside 2–100, a blank id, a duplicated id, an unknown
 * content format. Validation order mirrors the server's own table (§3.3:
 * count, then duplicates, then per-id validity).
 */
export function buildMergeRequest(
  sourceIds: readonly string[],
  options: MergeRequestOptions = {},
): MergeRequestResult {
  const ids = [...sourceIds];

  if (!canMerge(ids.length)) {
    return {
      ok: false,
      reason: mergeDisabledReason(ids.length) ?? 'Unsupported source count',
    };
  }

  const seen = new Set<string>();
  for (const raw of ids) {
    // A blank member would be answered with INVALID_SOURCE_NODE_ID; ids
    // arrive from a `Set<string>` of real rows, so this is a guard against
    // a caller passing junk, not an expected path.
    if (typeof raw !== 'string' || raw.trim() === '') {
      return {
        ok: false,
        reason:
          'A selected node id is blank — reload the node list and try again',
      };
    }
    if (seen.has(raw)) {
      return {
        ok: false,
        reason:
          'The same node was selected twice — reload the node list and try again',
      };
    }
    seen.add(raw);
  }

  const contentFormat = options.contentFormat ?? MERGE_DEFAULT_CONTENT_FORMAT;
  if (!MERGE_CONTENT_FORMATS.includes(contentFormat)) {
    return {
      ok: false,
      reason: `content_format must be one of: ${MERGE_CONTENT_FORMATS.join(', ')}`,
    };
  }

  const body: MergeRequestBody = {
    source_node_ids: ids,
    // Always a string: `content` is required, and an absent field (rather
    // than an empty one) is INVALID_BODY / "content is required".
    content: typeof options.content === 'string' ? options.content : '',
    content_format: contentFormat,
  };

  const target = options.targetParentId?.trim();
  if (target) body.target_parent_id = target;
  if (options.metadata) body.metadata = options.metadata;

  return { ok: true, body };
}

// ─── Response (201) ────────────────────────────────────────────────────

/**
 * The created synthesis node, translated to the camelCase shape the node
 * list uses (`NodeDetail` in `pages/NodesPage.tsx` satisfies this
 * structurally, so the dialog's result can be inserted into the list).
 */
export interface MergeNode {
  id: string;
  treeId: string;
  parentId: string | null;
  authorId: string;
  authorDisplayName: string;
  content: string;
  contentFormat: string;
  nodeType: string;
  sequenceNum: number;
  metadata: unknown;
  depth: number;
  childCount: number;
  createdAt: string;
  editedAt: string | null;
  deletedAt: string | null;
}

/** One edge from the 201 envelope (1 reply + N synthesis edges). */
export interface MergeEdge {
  id: string;
  treeId: string;
  sourceNodeId: string;
  targetNodeId: string;
  edgeType: string;
  createdAt: string;
}

/** The normalised §3.6 envelope. */
export interface MergeResponse {
  node: MergeNode;
  edges: MergeEdge[];
  mergedSourceIds: string[];
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null
    ? (value as Record<string, unknown>)
    : null;
}

function asString(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function asNullableString(value: unknown): string | null {
  return typeof value === 'string' ? value : null;
}

function asNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}

function normalizeEdge(raw: unknown): MergeEdge | null {
  const edge = asRecord(raw);
  if (!edge) return null;
  const id = asString(edge.id);
  if (!id) return null;
  return {
    id,
    treeId: asString(edge.tree_id),
    sourceNodeId: asString(edge.source_node_id),
    targetNodeId: asString(edge.target_node_id),
    edgeType: asString(edge.edge_type),
    createdAt: asString(edge.created_at),
  };
}

/**
 * Read the 201 envelope defensively.
 *
 * Returns null — and the dialog reports a failure instead of inserting
 * anything — when the payload is not the documented shape. `node_type` is
 * required to be `synthesis`: the merge route is the only way to create one
 * (§3.1), so anything else means the response is not what this client
 * thinks it is, and a wrong node in the list is worse than an error.
 *
 * §3.6 field names are snake_case, which is why this translation exists at
 * all: the list endpoint the page already renders is camelCase
 * (`internal/service/node_service.go` NodeDetail).
 */
export function normalizeMergeResponse(raw: unknown): MergeResponse | null {
  const envelope = asRecord(raw);
  if (!envelope) return null;

  const node = asRecord(envelope.node);
  if (!node) return null;

  const id = asString(node.id);
  const nodeType = asString(node.node_type);
  if (!id || nodeType !== 'synthesis') return null;

  const edges = Array.isArray(envelope.edges)
    ? envelope.edges
        .map(normalizeEdge)
        .filter((edge): edge is MergeEdge => edge !== null)
    : [];

  const mergedSourceIds = Array.isArray(envelope.merged_source_ids)
    ? envelope.merged_source_ids.filter(
        (value): value is string => typeof value === 'string',
      )
    : [];

  return {
    node: {
      id,
      treeId: asString(node.tree_id),
      parentId: asNullableString(node.parent_id),
      authorId: asString(node.author_id),
      authorDisplayName: asString(node.author_display_name),
      content: asString(node.content),
      contentFormat: asString(node.content_format),
      nodeType,
      sequenceNum: asNumber(node.sequence_num),
      metadata: node.metadata ?? null,
      depth: asNumber(node.depth),
      childCount: asNumber(node.child_count),
      createdAt: asString(node.created_at),
      editedAt: asNullableString(node.edited_at),
      deletedAt: asNullableString(node.deleted_at),
    },
    edges,
    mergedSourceIds,
  };
}

// ─── Failure translation ───────────────────────────────────────────────

/** What a merge failure can be told apart as. Both null when unrecognised. */
export interface MergeErrorInfo {
  /** The §3.3 catalog code, when the message identifies one. */
  code: string | null;
  /** Plain-language, actionable form of that code. */
  hint: string | null;
}

/*
 * `apiPost` throws `new Error(<envelope.error.message>)` — lib/api.ts keeps
 * only the message, and this task must not change that shared signature for
 * every other caller. So the code is recognised from the message: these
 * fingerprints are the catalog's own default messages
 * (`mergeErrorCatalog` in internal/service/merge_service.go). An unmatched
 * message yields {null, null} and the caller shows the server's text
 * verbatim, which is always honest — the table only ever ADDS a hint.
 */
const MERGE_ERROR_HINTS: ReadonlyArray<{
  code: string;
  match: string;
  hint: string;
}> = [
  {
    code: 'MIN_SOURCE_NODES',
    match: 'at least 2 source nodes',
    hint: 'A merge needs at least 2 source nodes — select one more row.',
  },
  {
    code: 'MAX_SOURCE_NODES',
    match: 'at most 100 source nodes',
    hint: 'A merge takes at most 100 source nodes — narrow the selection.',
  },
  {
    code: 'DUPLICATE_SOURCE_NODES',
    match: 'duplicate entries',
    hint: 'The same node was selected twice — reload the node list and try again.',
  },
  {
    code: 'INVALID_SOURCE_NODE_ID',
    match: 'is not a valid uuidv7',
    hint: 'One selected id is not a valid node id — reload the node list and reselect.',
  },
  {
    code: 'SOURCE_NODE_NOT_FOUND',
    match: 'source nodes not found',
    hint: 'One of the sources no longer exists — reload the node list and reselect.',
  },
  {
    code: 'SOURCE_NODE_DELETED',
    match: 'source nodes have been deleted',
    hint: 'One of the sources was deleted meanwhile — reload the node list and reselect.',
  },
  {
    code: 'TREE_MISMATCH',
    match: 'belongs to a different tree',
    hint: 'One of the sources belongs to another tree — reload the node list and reselect.',
  },
  {
    // The catalog default ("target parent is one of the source nodes") and
    // the two service-side variants ("… is also a source node" for an
    // explicit target, "the merge target … is also a source node" for the
    // resolved default root) are all the same code.
    code: 'SOURCE_TARGET_OVERLAP',
    match: 'one of the source nodes',
    hint: "This tree's default placement (its root) is one of the selected nodes — deselect the root node and merge the rest.",
  },
  {
    code: 'SOURCE_TARGET_OVERLAP',
    match: 'also a source node',
    hint: "This tree's default placement (its root) is one of the selected nodes — deselect the root node and merge the rest.",
  },
  {
    code: 'TARGET_PARENT_NOT_FOUND',
    match: 'target parent node not found',
    hint: "The tree's root node is missing, so the synthesis node has nowhere to be placed.",
  },
  {
    code: 'TARGET_PARENT_DELETED',
    match: 'target parent node has been deleted',
    hint: "The tree's root node has been deleted, so the synthesis node cannot be placed.",
  },
  {
    code: 'CONTENT_TOO_LONG',
    match: 'must not exceed 65536',
    hint: 'The summary is too long (65536 characters max) — shorten it.',
  },
  {
    code: 'INVALID_CONTENT_FORMAT',
    match: 'content_format must be one of',
    hint: 'Unsupported content format — this is a client bug, please report it.',
  },
  {
    code: 'TREE_DELETED',
    match: 'tree has been deleted',
    hint: 'This tree has been deleted — pick another tree.',
  },
  {
    code: 'TREE_NOT_FOUND',
    match: 'tree not found',
    hint: 'This tree no longer exists — reload the tree list.',
  },
  {
    code: 'NOT_TREE_MEMBER',
    match: 'not a member of this tree',
    hint: 'You are not a member of this tree, so it cannot be merged.',
  },
  {
    code: 'RATE_LIMITED',
    match: 'too many requests',
    hint: 'Too many requests from this client — wait a moment and retry.',
  },
  {
    code: 'SERVICE_UNAVAILABLE',
    match: 'merge service unavailable',
    hint: 'The merge service is unavailable right now — retry shortly.',
  },
];

/**
 * Recognise a merge failure by the message `apiPost` surfaced.
 * The first match wins; order is most-specific-first.
 */
export function describeMergeError(message: string): MergeErrorInfo {
  const haystack = (message ?? '').toLowerCase();
  for (const row of MERGE_ERROR_HINTS) {
    if (haystack.includes(row.match)) {
      return { code: row.code, hint: row.hint };
    }
  }
  return { code: null, hint: null };
}
