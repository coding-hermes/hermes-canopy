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
import { ChevronRight, MessageSquare } from 'lucide-react';
import { describeNodeAvatar } from '../../lib/nodeAvatar.ts';
import { replyBadgeAriaLabel, replyBadgeLabel } from '../../lib/replyCounts.ts';
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

// ─── Card shell ────────────────────────────────────────────────────────

export interface NodeShellProps {
  children: ReactNode;
  /** Identity colour for this node type — drives border + glow. */
  accent: string;
  selected?: boolean;
  /** Full aria-label for the card. */
  ariaLabel: string;
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
