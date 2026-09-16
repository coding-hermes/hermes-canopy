/**
 * Hermes Canopy — Node card chrome (UI-04, Phase 11 Mockup Parity)
 *
 * The pieces every card on the branching canvas shares
 * (docs/mockups/mockup-1.png):
 *
 *   NodeAvatar        colour-coded circle with the author's initials
 *   ReplyBadge        "💬 n" pill, hidden entirely on a leaf
 *   CollapseChevron   circular toggle that hangs off the card's right
 *                     edge, on the connector, where the subtree sprouts
 *   NodeShell         the rounded, glassy card body with the neon
 *                     active-glow and the two React Flow handles
 *
 * Everything derives from lib/nodeAvatar, lib/nodeCard, lib/replyCounts
 * and lib/canvasGeometry — this module only paints. All colour comes from
 * design tokens; the only raw hex is the per-author avatar fill, which is
 * the shared presence palette and gets a *measured* foreground.
 */

import { memo, type ReactNode } from 'react';
import { Handle, Position } from '@xyflow/react';
import { ChevronRight, Layers, MessageSquare } from 'lucide-react';
import { describeNodeAvatar } from '../../lib/nodeAvatar.ts';
import { replyBadgeAriaLabel, replyBadgeLabel } from '../../lib/replyCounts.ts';
import { referenceBadgeLabel } from '../../lib/multiReference.ts';
import { nodeGlowShadow } from '../../lib/canvasGeometry.ts';
import { palette, alpha } from '../../theme.ts';

// ─── Avatar ────────────────────────────────────────────────────────────

export interface NodeAvatarProps {
  authorId: string;
  isAgent?: boolean;
  /** Real display names when the caller has them (presence, membership). */
  names?: ReadonlyMap<string, string>;
  size?: 'sm' | 'md';
}

/**
 * Circular author avatar. The fill is deterministic per author (shared
 * with the presence bar); the text colour is whichever of ink/white
 * actually clears WCAG AA on that fill — see lib/nodeAvatar.
 */
function NodeAvatarComponent({
  authorId,
  isAgent,
  names,
  size = 'md',
}: NodeAvatarProps) {
  const avatar = describeNodeAvatar(authorId, { names, isAgent });
  const px = size === 'sm' ? 22 : 28;

  return (
    <span
      className="inline-flex shrink-0 items-center justify-center rounded-full font-semibold select-none"
      style={{
        width: px,
        height: px,
        fontSize: size === 'sm' ? 9 : 11,
        letterSpacing: '0.02em',
        backgroundColor: avatar.background,
        color: avatar.color,
        boxShadow: `0 0 0 1px ${alpha(avatar.background, 0.5)}`,
      }}
      title={avatar.name}
      aria-hidden="true"
      data-testid="node-avatar"
    >
      {avatar.initials}
    </span>
  );
}

export const NodeAvatar = memo(NodeAvatarComponent);

// ─── Reply badge ───────────────────────────────────────────────────────

export interface ReplyBadgeProps {
  count: number;
  accent?: string;
}

/**
 * "💬 n" pill. Renders nothing at all when the node is a leaf — an
 * explicit "0 replies" is noise on a graph.
 */
function ReplyBadgeComponent({ count, accent = palette.accent }: ReplyBadgeProps) {
  const label = replyBadgeLabel(count);
  if (label === null) return null;

  return (
    <span
      className="inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[10px] font-medium leading-none"
      style={{
        backgroundColor: alpha(accent, 0.14),
        color: accent,
        border: `1px solid ${alpha(accent, 0.28)}`,
      }}
      aria-label={replyBadgeAriaLabel(count)}
      data-testid="reply-badge"
    >
      <MessageSquare className="h-2.5 w-2.5" aria-hidden="true" />
      {label}
    </span>
  );
}

export const ReplyBadge = memo(ReplyBadgeComponent);

// ─── Collapse chevron ──────────────────────────────────────────────────

export interface CollapseChevronProps {
  collapsed: boolean;
  /** Nodes hidden right now — surfaced in the tooltip / aria label. */
  hiddenCount: number;
  onToggle: () => void;
  accent?: string;
}

/**
 * Circular expand/collapse toggle. Positioned on the card's right edge,
 * straddling the connector, exactly where the mockup puts it — the
 * chevron reads as belonging to the branch, not to the card.
 */
function CollapseChevronComponent({
  collapsed,
  hiddenCount,
  onToggle,
  accent = palette.accent,
}: CollapseChevronProps) {
  const label = collapsed
    ? `Expand branch (${hiddenCount} hidden ${hiddenCount === 1 ? 'node' : 'nodes'})`
    : 'Collapse branch';

  return (
    <button
      type="button"
      className="nodrag nopan absolute top-1/2 z-10 flex h-5 w-5 -translate-y-1/2 items-center justify-center rounded-full transition-transform duration-150 hover:scale-110"
      style={{
        right: -10,
        backgroundColor: collapsed
          ? alpha(accent, 0.9)
          : 'var(--color-surface-raised)',
        border: `1px solid ${alpha(accent, collapsed ? 0.9 : 0.45)}`,
        color: collapsed ? palette.surfaceBase : accent,
        boxShadow: collapsed
          ? `0 0 10px -2px ${alpha(accent, 0.6)}`
          : undefined,
      }}
      onClick={(e) => {
        e.stopPropagation();
        onToggle();
      }}
      aria-label={label}
      aria-expanded={!collapsed}
      title={label}
      data-testid="collapse-chevron"
    >
      <ChevronRight
        className="h-3 w-3 transition-transform duration-150"
        style={{ transform: collapsed ? 'none' : 'rotate(90deg)' }}
        aria-hidden="true"
      />
    </button>
  );
}

export const CollapseChevron = memo(CollapseChevronComponent);

// ─── Reference badge (SPEC-PL-06 §7.1) ─────────────────────────────────

export interface ReferenceBadgeProps {
  /** Number of sources the reply was written against. */
  count: number;
  /** §7.2 accessibility sentence — also the button's title. */
  ariaDescription: string;
  /**
   * Opens the source list. The list itself lives OUTSIDE the graph canvas
   * (§7.1) — at 2000+ nodes the canvas is painted pixels with no DOM nodes
   * at all, so the badge must not be the only way to reach the sources.
   */
  onOpen?: () => void;
}

/**
 * Compact `N references` pill worn by a multi-reference reply (§7.1).
 *
 * A `<button>` when the page supplied a handler (keyboard-reachable, focus
 * ring included) and a plain `<span>` otherwise — the badge is descriptive
 * either way, so it never advertises an action it cannot perform.
 */
function ReferenceBadgeComponent({ count, ariaDescription, onOpen }: ReferenceBadgeProps) {
  const label = referenceBadgeLabel(count);
  const accent = palette.accent2;

  const shared = {
    className:
      'inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[10px] font-medium leading-none',
    style: {
      backgroundColor: alpha(accent, 0.14),
      color: accent,
      border: `1px solid ${alpha(accent, 0.3)}`,
    } as const,
    title: ariaDescription,
    'data-testid': 'reference-badge',
    'data-reference-count': count,
  };

  if (!onOpen) {
    return (
      <span {...shared} aria-label={ariaDescription}>
        <Layers className="h-2.5 w-2.5" aria-hidden="true" />
        {label}
      </span>
    );
  }

  return (
    <button
      type="button"
      {...shared}
      onClick={(e) => {
        e.stopPropagation();
        onOpen();
      }}
      aria-label={`${ariaDescription} Open source list.`}
    >
      <Layers className="h-2.5 w-2.5" aria-hidden="true" />
      {label}
    </button>
  );
}

export const ReferenceBadge = memo(ReferenceBadgeComponent);

// ─── Card shell ────────────────────────────────────────────────────────

export interface NodeShellProps {
  children: ReactNode;
  /** Identity colour for this node type — drives border + glow. */
  accent: string;
  selected?: boolean;
  /** Full aria-label for the card. */
  ariaLabel: string;
  /**
   * id of the element carrying this card's longer description. Used by a
   * multi-reference reply, whose §7.2 sentence ("Multi-reference reply to N
   * messages: R1 …, R2 …") must reach a screen reader on FOCUS without
   * crowding the aria-label.
   */
  ariaDescribedBy?: string;
  /** Handle colour; defaults to the accent. */
  handleColor?: string;
  minWidth?: number;
  maxWidth?: number;
  /** Chevron and any other absolutely-positioned adornments. */
  adornment?: ReactNode;
}

/**
 * The rounded, glassy card body shared by every node type.
 *
 * Handles are Left (target) / Right (source) because the canvas runs
 * left→right (UI-04) — with Top/Bottom handles the beziers would leave
 * the wrong faces and cross their own cards.
 */
function NodeShellComponent({
  children,
  accent,
  selected,
  ariaLabel,
  ariaDescribedBy,
  handleColor,
  minWidth = 200,
  maxWidth = 260,
  adornment,
}: NodeShellProps) {
  const dot = handleColor ?? accent;

  return (
    <div className="relative" style={{ minWidth, maxWidth }}>
      <div
        className="rounded-lg bg-surface-panel transition-all duration-150"
        style={{
          border: `1px solid ${alpha(accent, selected ? 0.85 : 0.24)}`,
          boxShadow: selected
            ? nodeGlowShadow(accent, 'strong')
            : '0 6px 18px -10px rgba(0,0,0,0.75)',
        }}
        role="article"
        aria-label={ariaLabel}
        aria-describedby={ariaDescribedBy}
        aria-current={selected ? 'true' : undefined}
        tabIndex={0}
      >
        <Handle
          type="target"
          position={Position.Left}
          className="!h-2 !w-2 !border-0"
          style={{ backgroundColor: alpha(dot, 0.8) }}
          isConnectable={false}
          aria-hidden="true"
        />

        {children}

        <Handle
          type="source"
          position={Position.Right}
          className="!h-2 !w-2 !border-0"
          style={{ backgroundColor: alpha(dot, 0.8) }}
          isConnectable={false}
          aria-hidden="true"
        />
      </div>
      {adornment}
    </div>
  );
}

export const NodeShell = memo(NodeShellComponent);
