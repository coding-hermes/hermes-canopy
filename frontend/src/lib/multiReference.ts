/**
 * Hermes Canopy — Multi-message reference model: wire shapes + derivations
 * (SPEC-PL-06 §5.2, §7.1, §7.2, §7.3, §8.2, §8.3, §9.1, §9.3)
 *
 * The render half of the reference model. Two things live here rather than
 * in the components that paint them:
 *
 *   1. WIRE → VIEW normalisation. The node metadata arrives nested under
 *      `metadata.multi_reference` with camelCase keys (§8.3); the preflight
 *      (§9.1) and the provenance read (§9.3) answer in snake_case; Go
 *      marshals a nil slice as `null`, not `[]`, so `branch_span` and
 *      `sources` are null on healthy payloads that simply have none. A
 *      component that maps over them straight off the wire crashes on the
 *      happy path. This module is the only place that has to know.
 *   2. Pure derivations — palette mapping, `R#` labels to 20 sources, the
 *      badge text, and the exact ARIA description §7.2 fixes — so they can
 *      be unit-tested without a renderer (a `.tsx` module that exports
 *      non-components also breaks React Fast Refresh).
 *
 * Everything here is derived from persisted values. No edge is ever
 * inferred from `parent_id` (§7.1) and no colour is invented: `color_key` is
 * server-computed, and the palette is the fixed §7.2 ramp.
 */

import { z } from 'zod';
import { referencePalette, type ReferenceColorKey } from '../theme.ts';
import { shortNodeId } from './nodeShortId.ts';

/** UUID-validated composite convergence payload from the tree SSE stream (§10.2). */
const convergenceUuidSchema = z.string().uuid();
export const multiReferenceConvergedEventSchema = z.object({
  tree_id: convergenceUuidSchema,
  node_id: convergenceUuidSchema,
  parent_mode: z.literal('multi_reference'),
  primary_source_id: convergenceUuidSchema,
  source_node_ids: z.array(convergenceUuidSchema).min(2).max(20),
  edge_ids: z.array(convergenceUuidSchema).min(2).max(20),
  is_synthetic_merge_point: z.boolean(),
  common_ancestor_id: convergenceUuidSchema.nullable(),
  context_manifest_hash: z.string().regex(/^[a-f0-9]{64}$/),
  created_at: z.string().datetime(),
}).superRefine((payload, ctx) => {
  if (new Set(payload.source_node_ids).size !== payload.source_node_ids.length) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['source_node_ids'], message: 'source IDs must be unique' });
  }
  if (new Set(payload.edge_ids).size !== payload.edge_ids.length) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['edge_ids'], message: 'edge IDs must be unique' });
  }
  if (payload.edge_ids.length !== payload.source_node_ids.length) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['edge_ids'], message: 'one edge is required per source' });
  }
  if (!payload.source_node_ids.includes(payload.primary_source_id)) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['primary_source_id'], message: 'primary source must be selected' });
  }
});

export type MultiReferenceConvergedEvent = z.infer<typeof multiReferenceConvergedEventSchema>;

/** Parse an untrusted SSE composite event without letting malformed data mutate the replica. */
export function parseMultiReferenceConvergedEvent(raw: unknown): MultiReferenceConvergedEvent | null {
  const parsed = multiReferenceConvergedEventSchema.safeParse(raw);
  return parsed.success ? parsed.data : null;
}

// ─── Palette (§7.2) ────────────────────────────────────────────────────

/** Every §7.2 color key, in palette order. */
export const REFERENCE_COLOR_KEYS = Object.keys(referencePalette) as ReferenceColorKey[];

/** Stroke width §7.2 fixes for every reference edge. */
export const REFERENCE_STROKE_WIDTH = 2.5;

/** Fallback when a payload carries no usable `color_key` (§5.2 is authoritative). */
const DEFAULT_COLOR_KEY: ReferenceColorKey = 'ref-0';

/**
 * Coerce an arbitrary wire value into a §7.2 color key.
 * An unknown or missing key degrades to `ref-0` rather than to `undefined`
 * (an SVG stroke of `undefined` renders black and reads as a broken edge).
 */
export function normaliseColorKey(raw: unknown): ReferenceColorKey {
  if (typeof raw === 'string') {
    const key = raw.trim() as ReferenceColorKey;
    if (key in referencePalette) return key;
  }
  return DEFAULT_COLOR_KEY;
}

/** Resolve a `color_key` (or anything) to its §7.2 stroke colour. */
export function referenceStroke(raw: unknown): string {
  return referencePalette[normaliseColorKey(raw)];
}

export interface ReferenceEdgeStyle {
  stroke: string;
  strokeWidth: number;
  /** Always solid — §7.2 gives `reference` no dash pattern. */
  dash?: string;
  animated: boolean;
}

/** Full stroke spec for a convergence edge (§7.2: 2.5px, solid, target arrow). */
export function referenceEdgeStyle(colorKey: unknown): ReferenceEdgeStyle {
  return {
    stroke: referenceStroke(colorKey),
    strokeWidth: REFERENCE_STROKE_WIDTH,
    animated: false,
  };
}

/** React Flow marker spec for the §7.2 target arrow. */
export interface ReferenceArrowMarker {
  type: 'arrowclosed';
  color: string;
  width: number;
  height: number;
}

/**
 * The §7.2 arrowhead: closed arrow AT THE TARGET, in the edge's own colour.
 *
 * React Flow emits the `<marker>` definition for an `EdgeMarker` object, so
 * this stays a plain data descriptor — no hand-rolled `<defs>` and no shared
 * element id to collide across N convergence edges.
 */
export function referenceArrowMarker(colorKey: unknown): ReferenceArrowMarker {
  return {
    type: 'arrowclosed',
    color: referenceStroke(colorKey),
    width: 14,
    height: 14,
  };
}

// ─── Labels (§5.2, §7.2) ───────────────────────────────────────────────

/**
 * Accessible label for a zero-based canonical index: `R1` for index 0
 * (§5.2 — the same rule the backend applies in `db.ReferenceSourceLabel`).
 * Defined for the full 2–20 selection range, so R9…R20 stay distinct even
 * where the eight-colour palette repeats (§7.2).
 */
export function referenceSourceLabel(index: number): string {
  const i = Number.isFinite(index) && index >= 0 ? Math.floor(index) : 0;
  return `R${i + 1}`;
}

/** The six §5.2 metadata keys, as persisted on a reference edge. */
export interface ReferenceEdgeMetadata {
  referenceIndex: number;
  sourceLabel: string;
  colorKey: ReferenceColorKey;
  selectionOrder: number;
  role: string;
}

/**
 * Display-only colour for a source with no server `color_key` in hand.
 *
 * §5.2 computes the authoritative key server-side, and both real paths
 * carry it (the §9.1 preflight preview and the persisted reference edges).
 * This only covers the moment between hydration and the first wire read, and
 * it stands in with the palette entry for the source's CANONICAL INDEX — a
 * deterministic, distinct-per-source colour that the label still identifies
 * independently (§7.2), never a random or invented hex.
 */
export function fallbackColorKey(index: number): ReferenceColorKey {
  const i = Number.isFinite(index) && index >= 0 ? Math.floor(index) : 0;
  return REFERENCE_COLOR_KEYS[i % REFERENCE_COLOR_KEYS.length];
}

/**
 * Normalise a reference edge's `metadata` object into the §5.2 renderer
 * shape. Missing keys fall back to the row's own position rather than to a
 * blank: an edge without metadata still renders with a deterministic label
 * and colour, never as an unlabelled line. Returns `null` when the object
 * carries no reference metadata at all (a reply/fork/synthesis edge).
 */
export function normaliseEdgeMetadata(
  raw: unknown,
  fallbackIndex = 0,
): ReferenceEdgeMetadata | null {
  if (!raw || typeof raw !== 'object') return null;
  const obj = raw as Record<string, unknown>;

  const hasReferenceShape =
    obj.role === 'context_source' ||
    obj.source_label !== undefined ||
    obj.sourceLabel !== undefined ||
    obj.color_key !== undefined ||
    obj.colorKey !== undefined ||
    obj.reference_index !== undefined ||
    obj.referenceIndex !== undefined;
  if (!hasReferenceShape) return null;

  const index = toIndex(obj.reference_index ?? obj.referenceIndex, fallbackIndex);
  return {
    referenceIndex: index,
    sourceLabel: toStringValue(obj.source_label ?? obj.sourceLabel) || referenceSourceLabel(index),
    colorKey: normaliseColorKey(obj.color_key ?? obj.colorKey),
    selectionOrder: toIndex(obj.selection_order ?? obj.selectionOrder, index),
    role: toStringValue(obj.role) || 'context_source',
  };
}

// ─── Multi-reference node metadata (§3.4, §8.3) ────────────────────────

export interface MultiReferenceBranchSource {
  sourceId: string;
  branchRootId: string;
  distanceFromRoot: number;
}

export interface MultiReferenceBranchSpan {
  commonAncestorId: string;
  sourceBranches: MultiReferenceBranchSource[];
}

export interface MultiReferenceMetadata {
  version: number;
  primarySourceId: string;
  canonicalSourceIds: string[];
  isSyntheticMergePoint: boolean;
  branchSpan: MultiReferenceBranchSpan | null;
  contextManifestHash: string;
  contextTokenBudget: number;
}

/**
 * Read `metadata.multi_reference` off a node. Returns `null` for an ordinary
 * lineage node (a legacy node with no `parentMode` renders normally, §15.2
 * scenario 14) and never throws on a malformed object.
 *
 * `canonicalSourceIds` is the canonical order (§3.5 invariant 5): it is what
 * the source list, the badge count and the inspector are ordered by.
 */
export function normaliseMultiReferenceMetadata(raw: unknown): MultiReferenceMetadata | null {
  if (!raw || typeof raw !== 'object') return null;
  const obj = raw as Record<string, unknown>;

  const ids = toStringArray(obj.canonicalSourceIds ?? obj.canonical_source_ids);
  const primary = toStringValue(obj.primarySourceId ?? obj.primary_source_id);
  if (ids.length === 0 && !primary) return null;

  const canonical = ids.length > 0 ? ids : [primary];
  const branchSpanRaw = (obj.branchSpan ?? obj.branch_span) as Record<string, unknown> | undefined;

  return {
    version: toIndex(obj.version, 1),
    primarySourceId: primary || canonical[0] || '',
    canonicalSourceIds: canonical,
    isSyntheticMergePoint: obj.isSyntheticMergePoint === true || obj.is_synthetic_merge_point === true,
    branchSpan: normaliseBranchSpan(branchSpanRaw),
    contextManifestHash: toStringValue(obj.contextManifestHash ?? obj.context_manifest_hash),
    contextTokenBudget: toIndex(obj.contextTokenBudget ?? obj.context_token_budget, 0),
  };
}

function normaliseBranchSpan(raw: Record<string, unknown> | undefined): MultiReferenceBranchSpan | null {
  if (!raw || typeof raw !== 'object') return null;
  const commonAncestorId = toStringValue(raw.commonAncestorId ?? raw.common_ancestor_id);
  const branchesRaw = (raw.sourceBranches ?? raw.source_branches) as unknown;
  const sourceBranches: MultiReferenceBranchSource[] = Array.isArray(branchesRaw)
    ? branchesRaw
        .map((entry) => {
          const b = (entry ?? {}) as Record<string, unknown>;
          const sourceId = toStringValue(b.sourceId ?? b.source_id);
          if (!sourceId) return null;
          return {
            sourceId,
            branchRootId: toStringValue(b.branchRootId ?? b.branch_root_id) || sourceId,
            distanceFromRoot: toIndex(b.distanceFromRoot ?? b.distance_from_root, 0),
          };
        })
        .filter((b): b is MultiReferenceBranchSource => b !== null)
    : [];
  if (!commonAncestorId && sourceBranches.length === 0) return null;
  return { commonAncestorId, sourceBranches };
}

/** Metadata off a node-shaped object (`metadata.multi_reference`). */
export function multiReferenceMetadataFromNode(node: {
  metadata?: Record<string, unknown> | null;
} | null | undefined): MultiReferenceMetadata | null {
  return normaliseMultiReferenceMetadata(node?.metadata?.multi_reference);
}

/**
 * Data the canvas attaches to every React Flow edge. `edgeType` is the
 * persisted database type, so lineage derivations never have to guess from
 * the React Flow component name (§7.1).
 */
export interface ReferenceEdgeData extends Record<string, unknown> {
  edgeType: string;
  isReference: boolean;
  referenceIndex?: number;
  sourceLabel?: string;
  colorKey?: ReferenceColorKey;
  selected?: boolean;
  dimmed?: boolean;
}

/**
 * The convergence edge — or source — the user is currently pointing at.
 * `sourceId` matches every convergence edge leaving that source; `edgeId`
 * matches exactly one (SPEC-PL-06 §7.2).
 */
export interface ReferenceHighlight {
  edgeId?: string | null;
  sourceId?: string | null;
}

/**
 * §7.2 highlight state for ONE edge: the pointed-at convergence edge (and its
 * siblings from the same source) brightens; unrelated convergence edges dim;
 * nothing else on the canvas changes.
 *
 * Pure so the hover contract can be tested without a renderer, and total so a
 * stale highlight (an edge that no longer exists) cannot dim the whole graph:
 * with no active highlight every edge is neutral.
 */
export function referenceEdgeHighlightState(args: {
  isReference: boolean;
  edgeId: string;
  edgeSource: string;
  highlight?: ReferenceHighlight | null;
  /** The edge's target is inside a collapsed branch (existing behaviour). */
  collapsed?: boolean;
}): { selected: boolean; dimmed: boolean } {
  const highlight = args.highlight ?? null;
  const active = Boolean(highlight && (highlight.edgeId || highlight.sourceId));

  if (!args.isReference) {
    return { selected: false, dimmed: args.collapsed === true };
  }

  const related =
    !active ||
    (highlight?.edgeId != null && args.edgeId === highlight.edgeId) ||
    (highlight?.sourceId != null && args.edgeSource === highlight.sourceId);

  return {
    selected: active && related,
    dimmed: args.collapsed === true || (active && !related),
  };
}

/**
 * Whether a React Flow edge is a convergence edge.
 *
 * Reads the persisted `edgeType` from the edge data first (authoritative),
 * falling back to the component name for edges built before this model
 * existed.
 */
export function flowEdgeIsReference(edge: {
  type?: string | null;
  data?: unknown;
}): boolean {
  const data = edge?.data as { edgeType?: unknown; isReference?: unknown } | null | undefined;
  if (data && typeof data.isReference === 'boolean') return data.isReference;
  if (data && typeof data.edgeType === 'string') return data.edgeType === 'reference';
  return edge?.type === 'referenceEdge';
}

/** The reference-edge data for one persisted §5.2 metadata object. */
export function referenceEdgeData(metadata: ReferenceEdgeMetadata): ReferenceEdgeData {
  return {
    edgeType: 'reference',
    isReference: true,
    referenceIndex: metadata.referenceIndex,
    sourceLabel: metadata.sourceLabel,
    colorKey: metadata.colorKey,
  };
}

// ─── Node-level badge view (§7.1, §7.2) ────────────────────────────────

export interface ReferenceNodeView {
  count: number;
  badge: string;
  ariaDescription: string;
  sources: ReferenceAriaSource[];
}

/**
 * The §7.1 badge + §7.2 ARIA description for one multi-reference reply.
 *
 * `sources` are supplied in canonical order by the caller (the canvas
 * resolves each `canonicalSourceId` against the replica); when a source is
 * missing from the replica the caller's resolved text is used, so the
 * description still names every source. Returns `null` for an ordinary
 * lineage node.
 */
export function buildReferenceNodeView(args: {
  metadata: MultiReferenceMetadata | null;
  /** Resolved `R#`-ordered sources, when the caller has them. */
  sources?: readonly ReferenceAriaSource[] | null;
  /** Text for a canonical source the caller could not resolve. */
  fallbackText?: (nodeId: string) => string;
}): ReferenceNodeView | null {
  const metadata = args.metadata;
  const ids = metadata?.canonicalSourceIds ?? [];
  const provided = Array.isArray(args.sources) ? args.sources : [];

  let sources: ReferenceAriaSource[];
  if (provided.length > 0) {
    sources = provided.map((s, i) => ({
      label: s.label || referenceSourceLabel(i),
      text: s.text,
    }));
  } else if (ids.length > 0) {
    sources = ids.map((id, i) => ({
      label: referenceSourceLabel(i),
      text: args.fallbackText?.(id) || shortNodeId(id) || id,
    }));
  } else {
    sources = [];
  }

  if (sources.length === 0) return null;

  return {
    count: sources.length,
    badge: referenceBadgeLabel(sources.length),
    ariaDescription: referenceAriaDescription(sources),
    sources,
  };
}

// ─── Badge + ARIA (§7.1, §7.2) ─────────────────────────────────────────

/** `"4 references"` — the compact §7.1 badge. */
export function referenceBadgeLabel(count: number): string {
  const n = Number.isFinite(count) && count > 0 ? Math.floor(count) : 0;
  return `${n} ${n === 1 ? 'reference' : 'references'}`;
}

/** Screen-reader text for the badge. */
export function referenceBadgeAriaLabel(count: number): string {
  const n = Number.isFinite(count) && count > 0 ? Math.floor(count) : 0;
  return `${n} ${n === 1 ? 'reference' : 'references'} to this reply`;
}

/** One source as the ARIA description needs it: `R#` label + human label. */
export interface ReferenceAriaSource {
  label: string;
  /** The source's own display text (content preview / card title). */
  text: string;
}

/**
 * The exact keyboard-focus description §7.2 fixes:
 *
 *   "Multi-reference reply to 4 messages: R1 Message A, R2 Message B, R3
 *    Message C, R4 Message D."
 *
 * Every source is named by its `R#` label AND its text, so the sentence is
 * complete without edge-colour perception (§7.2 accessibility contract).
 */
export function referenceAriaDescription(sources: readonly ReferenceAriaSource[]): string {
  const n = sources.length;
  const parts = sources.map((s, i) => `${s.label || referenceSourceLabel(i)} ${s.text}`.trim());
  return `Multi-reference reply to ${n} messages: ${parts.join(', ')}.`;
}

// ─── §9.1 preflight / §9.3 provenance wire shapes ──────────────────────

/** One source as §9.1 / §9.3 answer it (snake_case, `null` slices included). */
export interface RawReferenceSource {
  node_id?: string | null;
  source_label?: string | null;
  color_key?: string | null;
  branch_root_id?: string | null;
  content_preview?: string | null;
  content?: string | null;
  truncated?: boolean | null;
  /**
   * §6.3's per-source omission count. §9.3 currently reports truncation as a
   * boolean only, so this is parsed when present and 0 otherwise — the
   * inspector shows the marker it was actually given rather than inventing a
   * number.
   */
  omitted_tokens?: number | null;
}

export interface RawBranchSpan {
  common_ancestor_id?: string | null;
  source_branches?: Array<{
    source_id?: string | null;
    branch_root_id?: string | null;
    distance_from_root?: number | null;
  }> | null;
}

/** §9.3 `GET /nodes/{node_id}/reference-context` 200 body, as it arrives. */
export interface RawReferenceContext {
  node_id?: string | null;
  tree_id?: string | null;
  parent_mode?: string | null;
  primary_source_id?: string | null;
  context?: {
    sources?: RawReferenceSource[] | null;
    is_synthetic_merge_point?: boolean | null;
    branch_span?: RawBranchSpan | null;
    token_budget?: number | null;
    tokens_used?: number | null;
    manifest_hash?: string | null;
  } | null;
  source_changed_since_creation?: boolean | null;
}

/** Normalised §9.3 provenance read. Arrays are always arrays. */
export interface ReferenceContext {
  nodeId: string;
  treeId: string;
  parentMode: string;
  primarySourceId: string;
  sources: RawReferenceSource[];
  isSyntheticMergePoint: boolean;
  branchSpan: MultiReferenceBranchSpan | null;
  tokenBudget: number;
  tokensUsed: number;
  manifestHash: string;
  /** True when the live source snapshot no longer matches creation. */
  sourceChangedSinceCreation: boolean;
}

/**
 * Wire body → renderable provenance, or `null` when the payload carried no
 * context at all. Never throws: a node whose provenance read degraded is
 * still a node on the canvas.
 */
export function normaliseReferenceContext(
  body: RawReferenceContext | null | undefined,
): ReferenceContext | null {
  const ctx = body?.context;
  if (!body || !ctx || typeof ctx !== 'object') return null;

  return {
    nodeId: body.node_id ?? '',
    treeId: body.tree_id ?? '',
    parentMode: body.parent_mode ?? '',
    primarySourceId: body.primary_source_id ?? '',
    sources: Array.isArray(ctx.sources) ? ctx.sources : [],
    isSyntheticMergePoint: ctx.is_synthetic_merge_point === true,
    branchSpan: normaliseBranchSpan(ctx.branch_span as Record<string, unknown> | undefined),
    tokenBudget: toIndex(ctx.token_budget, 0),
    tokensUsed: toIndex(ctx.tokens_used, 0),
    manifestHash: ctx.manifest_hash ?? '',
    sourceChangedSinceCreation: body.source_changed_since_creation === true,
  };
}

/** Path passed to `apiGet` for the §9.3 read. */
export function referenceContextRequestPath(nodeId: string): string {
  return `/nodes/${encodeURIComponent(nodeId)}/reference-context`;
}

// ─── Inspector view model (§7.3, §8.2) ─────────────────────────────────

/** One source, canonical order preserved. */
export interface ReferenceInspectorSource {
  referenceIndex: number;
  label: string;
  colorKey: ReferenceColorKey;
  stroke: string;
  nodeId: string;
  /** Resolved author display name, or the short node id when unknown. */
  author: string;
  createdAt: string;
  branchRootId: string;
  preview: string;
  truncated: boolean;
  omittedTokens: number;
  /** False when the source node is not in the local replica. */
  resolved: boolean;
}

export interface ReferenceBranchGroup {
  branchRootId: string;
  sources: ReferenceInspectorSource[];
}

export interface ReferenceInspectorModel {
  nodeId: string;
  sourceCount: number;
  badge: string;
  ariaDescription: string;
  /** Canonical source order (§3.5 invariant 5). */
  sources: ReferenceInspectorSource[];
  isSyntheticMergePoint: boolean;
  /**
   * "Context from 2 branches" when the selection spans branches (§8.2), else
   * null. Deliberately NOT "merged"/"resolved"/"synthesis": the node is a
   * plain `message` whose provenance spans branches (§8.2).
   */
  branchSpanLabel: string | null;
  commonAncestorId: string | null;
  branchGroups: ReferenceBranchGroup[];
  manifestHash: string;
  tokenBudget: number;
  tokensUsed: number;
  truncatedCount: number;
  omittedTokens: number;
}

/** What the panel can tell the view model about one source node. */
export interface ReferenceSourceNodeInfo {
  authorId?: string;
  authorLabel?: string;
  createdAt?: string;
  content?: string;
  label?: string;
}

export interface BuildInspectorInput {
  nodeId: string;
  /** `metadata.multi_reference` off the reply node (§8.3). */
  metadata?: MultiReferenceMetadata | null;
  /** §9.1 / §9.3 sources, when a wire payload is available. */
  sources?: readonly RawReferenceSource[] | null;
  /** §9.3 `context.branch_span`, when read from the provenance endpoint. */
  branchSpan?: MultiReferenceBranchSpan | null;
  manifestHash?: string;
  tokenBudget?: number;
  tokensUsed?: number;
  /**
   * The reply's persisted reference-edge metadata, keyed by source node
   * (SPEC-PL-06 §5.2). This is where `color_key` and `source_label`
   * actually live — the node's own metadata names its sources but carries no
   * per-source renderer data, so without this the panel could only fall back
   * to index-derived colours.
   */
  edgeMeta?: (sourceNodeId: string) => { colorKey?: string; sourceLabel?: string } | null;
  /** Look up a source node in the local replica (author, timestamp, body). */
  resolveNode?: (nodeId: string) => ReferenceSourceNodeInfo | null | undefined;
  /** Real author display names, when the caller has them. */
  authorNames?: ReadonlyMap<string, string>;
}

/**
 * Build the §7.3 inspector model from whatever the caller actually has.
 *
 * Ordering: `metadata.canonicalSourceIds` when present (§3.5 invariant 5 —
 * canonical order is the persisted truth), then any wire source that the
 * metadata did not name, appended in payload order. Colour and label come
 * from the persisted `color_key` / `source_label` when the payload supplies
 * them and fall back to the source's canonical index otherwise, so R9…R20
 * stay distinct even where the palette repeats (§7.2).
 *
 * Returns `null` when there is no multi-reference identity at all.
 */
export function buildReferenceInspectorModel(
  input: BuildInspectorInput,
): ReferenceInspectorModel | null {
  const metadata = input.metadata ?? null;
  const wire = Array.isArray(input.sources) ? input.sources : [];
  const canonical = metadata?.canonicalSourceIds ?? [];

  if (!metadata && wire.length === 0) return null;

  const byNodeId = new Map<string, RawReferenceSource>();
  for (const src of wire) {
    const id = toStringValue(src?.node_id);
    if (id && !byNodeId.has(id)) byNodeId.set(id, src);
  }

  const orderedIds: string[] = [];
  for (const id of canonical) if (!orderedIds.includes(id)) orderedIds.push(id);
  for (const id of byNodeId.keys()) if (!orderedIds.includes(id)) orderedIds.push(id);

  const branchRootBySource = new Map<string, string>();
  for (const branch of input.branchSpan?.sourceBranches ?? metadata?.branchSpan?.sourceBranches ?? []) {
    branchRootBySource.set(branch.sourceId, branch.branchRootId);
  }

  const sources: ReferenceInspectorSource[] = orderedIds.map((nodeId, index) => {
    const raw = byNodeId.get(nodeId);
    const info = input.resolveNode?.(nodeId) ?? null;
    const edgeMeta = input.edgeMeta?.(nodeId) ?? null;
    // §5.2 lives on the EDGE: the persisted reference edge supplies the
    // renderer label/colour, a wire source may preview it, and the canonical
    // index is the last-resort fallback (distinct per source, never random).
    const label =
      toStringValue(raw?.source_label) ||
      toStringValue(edgeMeta?.sourceLabel) ||
      referenceSourceLabel(index);
    const colorKey = normaliseColorKey(
      raw?.color_key ?? edgeMeta?.colorKey ?? fallbackColorKey(index),
    );
    const preview =
      toStringValue(raw?.content_preview) ||
      toStringValue(raw?.content) ||
      toStringValue(info?.label) ||
      previewOf(info?.content);
    const authorId = info?.authorId ?? '';
    const author =
      info?.authorLabel ||
      (authorId ? input.authorNames?.get(authorId) ?? '' : '') ||
      shortNodeId(nodeId) ||
      nodeId;

    return {
      referenceIndex: index,
      label,
      colorKey,
      stroke: referencePalette[colorKey],
      nodeId,
      author,
      createdAt: info?.createdAt ?? '',
      branchRootId: branchRootBySource.get(nodeId) ?? '',
      preview,
      truncated: raw?.truncated === true,
      omittedTokens: toIndex(raw?.omitted_tokens, 0),
      // False when the source node is not in the local replica (§7.3 still
      // lists it — the reference edge is the provenance, not the node body).
      resolved: info != null,
    };
  });

  const branchGroups: ReferenceBranchGroup[] = [];
  for (const source of sources) {
    const key = source.branchRootId || '';
    const group = branchGroups.find((g) => g.branchRootId === key);
    if (group) group.sources.push(source);
    else branchGroups.push({ branchRootId: key, sources: [source] });
  }

  const isSyntheticMergePoint =
    metadata?.isSyntheticMergePoint === true ||
    distinctBranchCount(sources, branchGroups) > 1;

  const commonAncestorId = input.branchSpan?.commonAncestorId || metadata?.branchSpan?.commonAncestorId || null;

  return {
    nodeId: input.nodeId,
    sourceCount: sources.length,
    badge: referenceBadgeLabel(sources.length),
    ariaDescription: referenceAriaDescription(
      sources.map((s) => ({ label: s.label, text: s.preview || shortNodeId(s.nodeId) || s.label })),
    ),
    sources,
    isSyntheticMergePoint,
    branchSpanLabel: isSyntheticMergePoint
      ? `Context from ${distinctBranchCount(sources, branchGroups)} branches`
      : null,
    commonAncestorId,
    branchGroups,
    manifestHash: input.manifestHash || metadata?.contextManifestHash || '',
    tokenBudget: input.tokenBudget ?? metadata?.contextTokenBudget ?? 0,
    tokensUsed: input.tokensUsed ?? 0,
    truncatedCount: sources.filter((s) => s.truncated).length,
    omittedTokens: sources.reduce((sum, s) => sum + s.omittedTokens, 0),
  };
}

function distinctBranchCount(
  sources: readonly ReferenceInspectorSource[],
  groups: readonly ReferenceBranchGroup[],
): number {
  const named = new Set(sources.map((s) => s.branchRootId).filter((id) => id !== ''));
  if (named.size > 0) return named.size;
  return groups.length;
}

// ─── Small helpers ─────────────────────────────────────────────────────

function toStringValue(raw: unknown): string {
  if (typeof raw === 'string') return raw;
  if (typeof raw === 'number' && Number.isFinite(raw)) return String(raw);
  return '';
}

function toIndex(raw: unknown, fallback: number): number {
  const n = typeof raw === 'number' ? raw : Number.parseInt(toStringValue(raw), 10);
  if (Number.isFinite(n) && n >= 0) return Math.floor(n);
  return Number.isFinite(fallback) && fallback >= 0 ? Math.floor(fallback) : 0;
}

function toStringArray(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  const out: string[] = [];
  for (const entry of raw) {
    const value = toStringValue(entry);
    if (value) out.push(value);
  }
  return out;
}

function previewOf(content: unknown, limit = 120): string {
  const text = toStringValue(content).trim();
  if (!text) return '';
  return text.length <= limit ? text : `${text.slice(0, limit)}…`;
}
