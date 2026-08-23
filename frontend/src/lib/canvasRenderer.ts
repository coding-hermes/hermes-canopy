/**
 * Hermes Canopy — Canvas 2D Fallback Renderer (STACK-03)
 *
 * specs/ARCHITECTURE.md §2.2: trees beyond CANVAS_THRESHOLD nodes are
 * painted by a custom Canvas 2D renderer instead of the React Flow DOM
 * renderer, which degrades sharply past ~2000 nodes. This module owns
 * every piece of maths and painting so the geometry stays unit-testable
 * without a live canvas:
 *
 *   - ViewTransform  — layout→screen affine map (pan/zoom)
 *   - computeFitTransform — initial fit-to-view
 *   - zoomAtPoint / applyPan — interaction maths
 *   - hitTestNodes — click selection
 *   - drawEdges / drawNodes / drawScene — paint routines
 *   - toCanvasNodes / toCanvasEdges — snapshot → scene mapping
 *
 * Coordinate contract: layout coordinates come straight from
 * `computeD3Layout` (d3Layout.ts) via `useYjsTree` — depth advances along
 * +x, breadth along +y — so the fallback's geometry matches the React
 * Flow view by construction. The transform maps layout → screen:
 *
 *     screen = layout * scale + offset
 *
 * Colors read the raw-hex `palette` mirror (theme.ts) because canvas
 * paint sits outside the CSS cascade — the same rule the MiniMap
 * follows. Edge styling delegates to `getEdgeStyle` so a connector keeps
 * its identity color across both renderers.
 */

import { palette, nodeTypeColor, alpha } from '../theme.ts';
import { getEdgeStyle } from '../layouts/d3Layout.ts';

// ─── Threshold ────────────────────────────────────────────────────────

/**
 * Above this many nodes the React Flow DOM renderer is swapped wholesale
 * for the Canvas 2D fallback (specs/ARCHITECTURE.md §2.2). Independent of
 * LARGE_TREE_THRESHOLD (500), which merely simplifies the React Flow
 * path — this is the point where the DOM path is abandoned entirely.
 */
export const CANVAS_THRESHOLD = 2000;

/** Strictly-greater semantics, matching `shouldUseSimplifiedMode`. */
export function shouldUseCanvas(nodeCount: number): boolean {
  return nodeCount > CANVAS_THRESHOLD;
}

// ─── Scene model ──────────────────────────────────────────────────────

/** Minimal node shape the canvas renderer consumes. */
export interface CanvasSceneNode {
  id: string;
  position: { x: number; y: number };
  label: string;
  /** Graph node type — drives the identity color via `nodeTypeColor`. */
  nodeType?: string;
}

/** Minimal edge shape the canvas renderer consumes. */
export interface CanvasSceneEdge {
  source: string;
  target: string;
  /** Logical edge kind — drives stroke color/dash via `getEdgeStyle`. */
  kind?: 'reply' | 'fork' | 'synthesis' | 'reference';
}

// ─── Snapshot mapping (React Flow shapes → scene) ─────────────────────

/** Structural subset of a React Flow node — no xyflow import needed. */
export interface RfNodeLike {
  id: string;
  position: { x: number; y: number };
  data?: { label?: string; nodeType?: string };
}

/** Structural subset of a React Flow edge. */
export interface RfEdgeLike {
  source: string;
  target: string;
  type?: string;
}

/** Map React Flow edge type strings ('replyEdge' | 'synthesis' | …). */
export function flowTypeToKind(flowType: string | undefined): NonNullable<CanvasSceneEdge['kind']> {
  if (!flowType) return 'reply';
  const bare = flowType.endsWith('Edge') ? flowType.slice(0, -'Edge'.length) : flowType;
  switch (bare) {
    case 'fork':
    case 'synthesis':
    case 'reference':
      return bare;
    default:
      return 'reply';
  }
}

export function toCanvasNodes(nodes: readonly RfNodeLike[]): CanvasSceneNode[] {
  return nodes.map((n) => ({
    id: n.id,
    position: { x: n.position.x, y: n.position.y },
    label: n.data?.label ?? '',
    ...(n.data?.nodeType ? { nodeType: n.data.nodeType } : {}),
  }));
}

export function toCanvasEdges(edges: readonly RfEdgeLike[]): CanvasSceneEdge[] {
  return edges.map((e) => ({
    source: e.source,
    target: e.target,
    kind: flowTypeToKind(e.type),
  }));
}

// ─── View transform ───────────────────────────────────────────────────

/** Affine layout→screen map: screen = layout * scale + offset. */
export interface ViewTransform {
  scale: number;
  offsetX: number;
  offsetY: number;
}

export const MIN_SCALE = 0.01;
export const MAX_SCALE = 4;

export function layoutToScreen(
  t: ViewTransform,
  x: number,
  y: number,
): { x: number; y: number } {
  return { x: x * t.scale + t.offsetX, y: y * t.scale + t.offsetY };
}

/** Translate the view by screen-space deltas. */
export function applyPan(t: ViewTransform, dx: number, dy: number): ViewTransform {
  return { scale: t.scale, offsetX: t.offsetX + dx, offsetY: t.offsetY + dy };
}

/**
 * Zoom by `factor` around a screen-space anchor so the layout point
 * under the cursor stays pinned: offset' = p − scale' · (p − offset)/scale.
 */
export function zoomAtPoint(
  t: ViewTransform,
  factor: number,
  px: number,
  py: number,
): ViewTransform {
  const scale = clampScale(t.scale * factor);
  const wx = (px - t.offsetX) / t.scale;
  const wy = (py - t.offsetY) / t.scale;
  return { scale, offsetX: px - wx * scale, offsetY: py - wy * scale };
}

function clampScale(scale: number): number {
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, scale));
}

// ─── Fit to view ──────────────────────────────────────────────────────

interface Extents {
  minX: number;
  minY: number;
  maxX: number;
  maxY: number;
}

export function sceneExtents(nodes: readonly CanvasSceneNode[]): Extents | null {
  if (nodes.length === 0) return null;
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const n of nodes) {
    minX = Math.min(minX, n.position.x);
    minY = Math.min(minY, n.position.y);
    maxX = Math.max(maxX, n.position.x);
    maxY = Math.max(maxY, n.position.y);
  }
  return { minX, minY, maxX, maxY };
}

export const FIT_PADDING = 0.1;
export const FIT_MAX_SCALE = 1.25;

/**
 * Compute the transform that centers the whole scene inside a
 * `width × height` viewport, leaving `padding` fraction of each axis
 * free (shared across both sides), capped at `maxScale`. The default
 * cap is Infinity — callers that want the interaction cap pass
 * FIT_MAX_SCALE explicitly (CanvasTreeView does). Empty scenes yield
 * the identity.
 */
export function computeFitTransform(
  nodes: readonly CanvasSceneNode[],
  width: number,
  height: number,
  padding: number = FIT_PADDING,
  maxScale: number = Infinity,
): ViewTransform {
  const extents = sceneExtents(nodes);
  if (!extents || width <= 0 || height <= 0) {
    return { scale: 1, offsetX: 0, offsetY: 0 };
  }

  const availW = width * (1 - padding);
  const availH = height * (1 - padding);
  const contentW = Math.max(extents.maxX - extents.minX, 1);
  const contentH = Math.max(extents.maxY - extents.minY, 1);

  const scale = clampFitScale(
    Math.min(availW / contentW, availH / contentH),
    maxScale,
  );

  const offsetX = (width - contentW * scale) / 2 - extents.minX * scale;
  const offsetY = (height - contentH * scale) / 2 - extents.minY * scale;
  return { scale, offsetX, offsetY };
}

function clampFitScale(scale: number, maxScale: number): number {
  return Math.min(maxScale, Math.max(MIN_SCALE, scale));
}

// ─── Hit testing ──────────────────────────────────────────────────────

export const NODE_DOT_RADIUS = 7;
/** Extra forgiveness around the dot, in screen px. */
export const HIT_SLOP = 6;

/**
 * Nearest node whose screen distance from (px, py) is within
 * NODE_DOT_RADIUS + HIT_SLOP, or null. O(n) — fine even at 10k nodes.
 */
export function hitTestNodes(
  nodes: readonly CanvasSceneNode[],
  transform: ViewTransform,
  px: number,
  py: number,
): string | null {
  const reach = NODE_DOT_RADIUS + HIT_SLOP;
  let bestId: string | null = null;
  let bestDist = reach * reach;
  for (const n of nodes) {
    const s = layoutToScreen(transform, n.position.x, n.position.y);
    const dx = s.x - px;
    const dy = s.y - py;
    const d = dx * dx + dy * dy;
    if (d <= bestDist) {
      bestDist = d;
      bestId = n.id;
    }
  }
  return bestId;
}

// ─── Painting ─────────────────────────────────────────────────────────

/** Labels render only when zoomed in far enough to be legible. */
export const LABEL_ZOOM_THRESHOLD = 0.6;
export const LABEL_MAX_CHARS = 42;

export function truncateLabel(label: string, max: number = LABEL_MAX_CHARS): string {
  const text = label.trim();
  if (text.length <= max) return text;
  return `${text.slice(0, max - 1)}…`;
}

type Ctx = CanvasRenderingContext2D;

function dashArray(spec: string | undefined): number[] | null {
  if (!spec) return null;
  const parts = spec.split(',').map((v) => Number.parseFloat(v.trim()));
  return parts.every((v) => Number.isFinite(v) && v > 0) ? parts : null;
}

/**
 * Paint connectors as straight lines in scene order. Edges with a
 * missing endpoint (multi-parent targets pruned elsewhere, transient
 * snapshots) are skipped silently.
 */
export function drawEdges(
  ctx: Ctx,
  edges: readonly CanvasSceneEdge[],
  nodes: readonly CanvasSceneNode[],
  transform: ViewTransform,
): void {
  const posById = new Map(nodes.map((n) => [n.id, n.position]));
  for (const edge of edges) {
    const from = posById.get(edge.source);
    const to = posById.get(edge.target);
    if (!from || !to) continue;

    const style = getEdgeStyle(edge.kind ?? 'reply');
    ctx.strokeStyle = style.stroke;
    ctx.lineWidth = 1;
    const dash = dashArray(style.strokeDasharray);
    if (dash) ctx.setLineDash(dash);

    const a = layoutToScreen(transform, from.x, from.y);
    const b = layoutToScreen(transform, to.x, to.y);
    ctx.beginPath();
    ctx.moveTo(a.x, a.y);
    ctx.lineTo(b.x, b.y);
    ctx.stroke();

    if (dash) ctx.setLineDash([]);
  }
}

export interface DrawNodesOptions {
  selectedNodeId?: string | null;
}

/**
 * Paint nodes as screen-space dots (constant radius keeps the overview
 * legible at heavy zoom-out), colored by graph node type. The selected
 * node gets a soft accent halo + ring. Labels appear only above
 * LABEL_ZOOM_THRESHOLD.
 */
export function drawNodes(
  ctx: Ctx,
  nodes: readonly CanvasSceneNode[],
  transform: ViewTransform,
  options: DrawNodesOptions = {},
): void {
  const showLabels = transform.scale >= LABEL_ZOOM_THRESHOLD;
  if (showLabels) {
    ctx.font = '11px system-ui, sans-serif';
  }

  for (const node of nodes) {
    const s = layoutToScreen(transform, node.position.x, node.position.y);

    if (options.selectedNodeId && node.id === options.selectedNodeId) {
      ctx.fillStyle = alpha(palette.accent, 0.18);
      ctx.beginPath();
      ctx.arc(s.x, s.y, NODE_DOT_RADIUS + 5, 0, Math.PI * 2);
      ctx.fill();
    }

    ctx.fillStyle = nodeTypeColor(node.nodeType);
    ctx.beginPath();
    ctx.arc(s.x, s.y, NODE_DOT_RADIUS, 0, Math.PI * 2);
    ctx.fill();

    if (options.selectedNodeId && node.id === options.selectedNodeId) {
      ctx.lineWidth = 2;
      ctx.strokeStyle = palette.contentPrimary;
      ctx.beginPath();
      ctx.arc(s.x, s.y, NODE_DOT_RADIUS + 1, 0, Math.PI * 2);
      ctx.stroke();
    }

    if (showLabels) {
      ctx.fillStyle = palette.contentSecondary;
      ctx.fillText(truncateLabel(node.label), s.x + NODE_DOT_RADIUS + 4, s.y + 4);
    }
  }
}

/** Paint the full scene: edges beneath, nodes above. */
export function drawScene(
  ctx: Ctx,
  nodes: readonly CanvasSceneNode[],
  edges: readonly CanvasSceneEdge[],
  transform: ViewTransform,
  options: DrawNodesOptions = {},
): void {
  drawEdges(ctx, edges, nodes, transform);
  drawNodes(ctx, nodes, transform, options);
}
