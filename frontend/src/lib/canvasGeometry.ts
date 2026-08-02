/**
 * Hermes Canopy — Canvas geometry & glow styling (UI-04, Phase 11)
 *
 * The mockup's connectors are not React Flow's default smoothstep elbows:
 * they are soft left-to-right beziers that glow, carry a joint dot where a
 * parent fans out, and dim when they lead into a collapsed branch
 * (docs/mockups/mockup-1.png).
 *
 * The maths and the colour decisions are pure, so they live here rather
 * than inside the edge components — an SVG path string is far easier to
 * assert in a unit test than a rendered `<path>`.
 *
 * All colours come from the token palette (theme.ts). No hex literals.
 */

import { palette, alpha } from '../theme';

// ─── Bezier geometry ───────────────────────────────────────────────────

export interface BezierPoints {
  sourceX: number;
  sourceY: number;
  targetX: number;
  targetY: number;
}

/** Result of `connectorPath` — the path plus the point to hang UI on. */
export interface ConnectorGeometry {
  /** SVG path `d` attribute. */
  path: string;
  /** Midpoint of the curve — where the chevron / joint dot is placed. */
  midX: number;
  midY: number;
}

/**
 * Horizontal control-point offset for the bezier.
 *
 * Scales with the horizontal gap so short hops stay tight and long ones
 * bow generously, but is clamped: an unclamped curve on a very wide span
 * loops back on itself and reads as a knot rather than a branch.
 */
export function bezierCurvature(
  points: BezierPoints,
  opts: { min?: number; max?: number; factor?: number } = {},
): number {
  const min = opts.min ?? 24;
  const max = opts.max ?? 140;
  const factor = opts.factor ?? 0.5;
  const dx = Math.abs(points.targetX - points.sourceX);
  const dy = Math.abs(points.targetY - points.sourceY);
  // A pure vertical drop still needs some bow, hence the dy contribution.
  const raw = dx * factor + dy * 0.12;
  return Math.max(min, Math.min(max, raw));
}

/**
 * Cubic bezier from a parent's source handle to a child's target handle,
 * bowing horizontally (mockup runs the tree left→right).
 */
export function connectorPath(points: BezierPoints): ConnectorGeometry {
  const { sourceX, sourceY, targetX, targetY } = points;
  const c = bezierCurvature(points);
  const c1x = sourceX + c;
  const c2x = targetX - c;

  // Cubic bezier midpoint at t=0.5 — (P0 + 3·P1 + 3·P2 + P3) / 8.
  const midX = (sourceX + 3 * c1x + 3 * c2x + targetX) / 8;
  const midY = (sourceY + 3 * sourceY + 3 * targetY + targetY) / 8;

  return {
    path: `M${sourceX},${sourceY} C${c1x},${sourceY} ${c2x},${targetY} ${targetX},${targetY}`,
    midX,
    midY,
  };
}

/**
 * Where the joint dot sits on a connector.
 *
 * The mockup puts it just off the parent, at the point where siblings
 * visually separate — not at the midpoint, which would collide with the
 * collapse chevron.
 */
export function jointDotPosition(
  points: BezierPoints,
  t = 0.18,
): { x: number; y: number } {
  const { sourceX, sourceY, targetX, targetY } = points;
  const c = bezierCurvature(points);
  const p1x = sourceX + c;
  const p2x = targetX - c;
  const clamped = Math.max(0, Math.min(1, t));
  const u = 1 - clamped;

  const x =
    u * u * u * sourceX +
    3 * u * u * clamped * p1x +
    3 * u * clamped * clamped * p2x +
    clamped * clamped * clamped * targetX;
  const y =
    u * u * u * sourceY +
    3 * u * u * clamped * sourceY +
    3 * u * clamped * clamped * targetY +
    clamped * clamped * clamped * targetY;

  return { x, y };
}

// ─── Connector styling ─────────────────────────────────────────────────

/** Which visual language a connector speaks. */
export type ConnectorKind = 'reply' | 'fork' | 'synthesis';

export interface ConnectorStyle {
  /** Core stroke colour. */
  stroke: string;
  /** Wide, low-opacity stroke painted underneath for the glow halo. */
  glow: string;
  strokeWidth: number;
  glowWidth: number;
  /** Dash pattern, or undefined for a solid line. */
  dash?: string;
  /** Joint-dot fill. */
  dot: string;
}

/** Accent per connector kind, straight off the token palette. */
export function connectorAccent(kind: ConnectorKind): string {
  switch (kind) {
    case 'synthesis':
      return palette.warning;
    case 'fork':
      return palette.accent3;
    case 'reply':
    default:
      return palette.accent;
  }
}

/**
 * Full stroke/glow spec for a connector.
 *
 * Selected connectors brighten and thicken (they are the path the user is
 * reading); connectors into a collapsed branch fade back so the collapsed
 * stub reads as "there is more here" rather than as an active thread.
 */
export function connectorStyle(
  kind: ConnectorKind,
  state: { selected?: boolean; dimmed?: boolean } = {},
): ConnectorStyle {
  const accent = connectorAccent(kind);
  const selected = state.selected === true;
  const dimmed = state.dimmed === true && !selected;

  const strokeAlpha = selected ? 0.95 : dimmed ? 0.28 : 0.6;
  const glowAlpha = selected ? 0.42 : dimmed ? 0.06 : 0.16;

  return {
    stroke: alpha(accent, strokeAlpha),
    glow: alpha(accent, glowAlpha),
    strokeWidth: selected ? 2.4 : 1.6,
    glowWidth: selected ? 9 : 6,
    dash: kind === 'synthesis' ? '7 5' : undefined,
    dot: alpha(accent, selected ? 1 : dimmed ? 0.4 : 0.8),
  };
}

// ─── Node glow ─────────────────────────────────────────────────────────

/**
 * Utility class for a node card's active glow.
 *
 * `glow-accent` / `glow-accent-2` are `@utility` rules in index.css, so the
 * neon treatment stays defined next to the tokens it uses.
 */
export function nodeGlowClass(state: {
  selected?: boolean;
  focused?: boolean;
}): string {
  if (state.selected) return 'glow-accent-2';
  if (state.focused) return 'glow-accent';
  return '';
}

/**
 * Inline box-shadow for a node card, composed from a token colour.
 *
 * Used where the intensity has to vary continuously (selected vs merely
 * hovered) and a static utility class cannot express it.
 */
export function nodeGlowShadow(
  accentHex: string,
  intensity: 'none' | 'soft' | 'strong' = 'soft',
): string | undefined {
  if (intensity === 'none') return undefined;
  const ring = intensity === 'strong' ? 0.65 : 0.3;
  const halo = intensity === 'strong' ? 0.45 : 0.18;
  const spread = intensity === 'strong' ? 26 : 14;
  return `0 0 0 1px ${alpha(accentHex, ring)}, 0 0 ${spread}px -4px ${alpha(accentHex, halo)}`;
}

// ─── Ghost slots ───────────────────────────────────────────────────────

/**
 * Whether a node sits at the frontier of the tree — i.e. whether a ghost
 * placeholder belongs after it.
 *
 * The mockup draws dashed slots at the growing edge of the graph, on
 * leaves, not hanging off every node (which would double the visual node
 * count). A collapsed node hides children it already has, so a slot there
 * would misrepresent the tree.
 *
 * This is a question about the GRAPH, deliberately independent of who is
 * looking — see `shouldShowGhostSlot` for the permission-aware variant.
 */
export function isFrontierSlot(state: {
  childCount: number;
  collapsed: boolean;
}): boolean {
  if (state.collapsed) return false;
  return state.childCount === 0;
}

/**
 * Whether a node should offer an *interactive* "add a reply here" slot.
 *
 * A frontier position the current user is allowed to write to. Viewers
 * still see the frontier marker (it is part of the graph's shape), but it
 * is inert for them — an affordance that cannot act is worse than none.
 */
export function shouldShowGhostSlot(state: {
  childCount: number;
  collapsed: boolean;
  readOnly?: boolean;
}): boolean {
  if (state.readOnly) return false;
  return isFrontierSlot(state);
}

/** Placement for a ghost slot relative to its parent node's position. */
export function ghostSlotPosition(
  parent: { x: number; y: number },
  offset: { dx?: number; dy?: number } = {},
): { x: number; y: number } {
  return {
    x: parent.x + (offset.dx ?? 0),
    y: parent.y + (offset.dy ?? 150),
  };
}

// ─── Canvas keyboard scope ─────────────────────────────────────────────

/**
 * The subset of an element the Tab-scope check reads. Structural on purpose,
 * so the rule is testable without a DOM.
 */
export interface FocusTargetLike {
  tagName?: string;
  isContentEditable?: boolean;
  /** Result of `getAttribute('tabindex')` — null when the attribute is absent. */
  tabIndexAttr?: string | null;
}

/** Elements whose own Tab behaviour must never be hijacked by the canvas. */
const NATIVE_TAB_STOPS = new Set(['INPUT', 'TEXTAREA', 'SELECT', 'BUTTON', 'A']);

/**
 * Whether the canvas may take Tab over to cycle graph nodes.
 *
 * TreeCanvas binds its shortcut handler to `window`, so an unguarded
 * `preventDefault()` on Tab swallows the key for the entire page — the
 * composer's @ / # / emoji / Send buttons then become unreachable by
 * keyboard, which fails WCAG 2.1.1. Tab belongs to the browser whenever a
 * natively focusable control or an explicit tab stop holds focus; the canvas
 * only claims it from a non-interactive resting place such as `body`.
 */
export function canvasOwnsTab(active: FocusTargetLike | null): boolean {
  if (!active) return true;
  const tag = active.tagName?.toUpperCase();
  if (tag && NATIVE_TAB_STOPS.has(tag)) return false;
  if (active.isContentEditable) return false;
  if (active.tabIndexAttr !== null && active.tabIndexAttr !== undefined) {
    return false;
  }
  return true;
}
