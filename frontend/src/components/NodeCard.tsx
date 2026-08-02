/**
 * Hermes Canopy — Node card (UI-05, Phase 11 Mockup Parity)
 *
 * Replaces the Nodes page's plain row (type icon + uppercase label + raw
 * mono id + content + author + time) with the rich card the mockups show
 * (docs/mockups/mockup-2.png, mockup-3.png):
 *
 *   ┌────────────────────────────────────────────────┐
 *   │ (SC)  Sarah Chen                  2h ago   ···  │  avatar / author / time / menu
 *   │       Message · Depth 2 · 3 replies             │  meta row
 *   │  We should backfill historical data in batches  │  body (line-clamped)
 *   │  #data-backfill                                 │  topic pill (only when linked)
 *   └────────────────────────────────────────────────┘
 *
 * Everything derives from `lib/nodeMeta` and the UI-04 primitives — this
 * module only paints. All colour comes from design tokens; the sole raw
 * hex is the per-author avatar fill, which is the shared presence palette
 * and gets a *measured* foreground (see lib/nodeAvatar).
 *
 * The `···` menu is a real disclosure: `aria-haspopup="menu"`,
 * `aria-expanded`, `role="menu"`/`menuitem`, Escape closes and restores
 * focus to the trigger, outside-click dismisses, and arrow keys move
 * between items. Edit and Delete stay fully keyboard reachable — they
 * were bare icon buttons before, and a hover-only dropdown that traps
 * focus would be a regression, not a redesign.
 */

import {
  memo,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from 'react';
import {
  MoreHorizontal,
  Edit3,
  Trash2,
  MessageSquare,
  GitMerge,
  FileText,
  Hash,
} from 'lucide-react';
import { NodeAvatar } from './nodes/NodeChrome.tsx';
import { describeNodeAvatar } from '../lib/nodeAvatar.ts';
import {
  formatNodeMeta,
  formatTimeAgo,
  nodeCardAriaLabel,
  parseTopicFromNode,
  topicPillLabel,
  type NodeMetaSource,
} from '../lib/nodeMeta.ts';
import { palette, alpha, nodeTypeColor } from '../theme.ts';

// ─── Types ─────────────────────────────────────────────────────────────

export interface NodeCardProps {
  node: NodeMetaSource;
  /** Real author identities when the caller has them. */
  authorNames?: ReadonlyMap<string, string>;
  /** Topic id/slug → title, so a metadata ref can render a real label. */
  topicTitles?: ReadonlyMap<string, string>;
  onEdit: () => void;
  onDelete: () => void;
  /** Opens the node's topic. Omit to render the pill as static. */
  onOpenTopic?: (topicId: string | null, slug: string) => void;
}

// ─── Type glyph ────────────────────────────────────────────────────────

function TypeIcon({ nodeType, color }: { nodeType: string; color: string }) {
  const cls = 'h-3 w-3';
  const style = { color };
  switch (nodeType) {
    case 'synthesis':
      return <GitMerge className={cls} style={style} aria-hidden="true" />;
    case 'card':
      return <FileText className={cls} style={style} aria-hidden="true" />;
    case 'topic':
    case 'system':
      return <Hash className={cls} style={style} aria-hidden="true" />;
    default:
      return <MessageSquare className={cls} style={style} aria-hidden="true" />;
  }
}

// ─── Overflow menu ─────────────────────────────────────────────────────

function OverflowMenu({
  label,
  onEdit,
  onDelete,
}: {
  label: string;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const [open, setOpen] = useState(false);
  const menuId = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const close = useCallback((restoreFocus = true) => {
    setOpen(false);
    if (restoreFocus) triggerRef.current?.focus();
  }, []);

  // Escape anywhere closes; a click outside dismisses without stealing focus.
  useEffect(() => {
    if (!open) return;

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        close();
      }
    };
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as Node;
      if (
        !menuRef.current?.contains(target) &&
        !triggerRef.current?.contains(target)
      ) {
        close(false);
      }
    };

    document.addEventListener('keydown', onKeyDown, true);
    document.addEventListener('pointerdown', onPointerDown, true);
    return () => {
      document.removeEventListener('keydown', onKeyDown, true);
      document.removeEventListener('pointerdown', onPointerDown, true);
    };
  }, [open, close]);

  // Focus the first item when the menu opens — keyboard users land inside.
  useEffect(() => {
    if (!open) return;
    const first = menuRef.current?.querySelector<HTMLButtonElement>(
      '[role="menuitem"]',
    );
    first?.focus();
  }, [open]);

  const onMenuKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLButtonElement>(
        '[role="menuitem"]',
      ) ?? [],
    );
    if (items.length === 0) return;
    const idx = items.indexOf(document.activeElement as HTMLButtonElement);
    const next =
      e.key === 'ArrowDown'
        ? items[(idx + 1) % items.length]
        : items[(idx - 1 + items.length) % items.length];
    next?.focus();
  };

  const run = (action: () => void) => {
    setOpen(false);
    action();
  };

  return (
    <div className="relative shrink-0">
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={`Actions for ${label}`}
        title="More actions"
        data-testid="node-card-menu-trigger"
        className={[
          'grid h-7 w-7 place-items-center rounded-md transition-colors',
          'text-content-muted hover:bg-surface-hover hover:text-content-primary',
          // Hover-reveal on pointer devices, but never hidden from keyboard
          // focus or while the menu is open.
          open
            ? 'bg-surface-hover text-content-primary opacity-100'
            : 'opacity-0 group-hover:opacity-100 focus-visible:opacity-100',
        ].join(' ')}
      >
        <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
      </button>

      {open && (
        <div
          ref={menuRef}
          id={menuId}
          role="menu"
          aria-label={`Actions for ${label}`}
          onKeyDown={onMenuKeyDown}
          data-testid="node-card-menu"
          className="glass-raised absolute right-0 top-8 z-20 w-36 overflow-hidden rounded-lg py-1"
        >
          <button
            type="button"
            role="menuitem"
            onClick={() => run(onEdit)}
            className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs text-content-secondary transition-colors hover:bg-surface-hover hover:text-content-primary"
          >
            <Edit3 className="h-3.5 w-3.5" aria-hidden="true" />
            Edit
          </button>
          <button
            type="button"
            role="menuitem"
            onClick={() => run(onDelete)}
            className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs text-status-danger transition-colors hover:bg-rose-500/10"
          >
            <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
            Delete
          </button>
        </div>
      )}
    </div>
  );
}

// ─── Topic pill ────────────────────────────────────────────────────────

function TopicPill({
  label,
  title,
  onOpen,
}: {
  label: string;
  title: string;
  onOpen?: () => void;
}) {
  const accent = palette.accent3;
  const style = {
    backgroundColor: alpha(accent, 0.14),
    color: accent,
    border: `1px solid ${alpha(accent, 0.32)}`,
  };
  const cls =
    'inline-flex max-w-full items-center rounded-md px-2 py-0.5 text-[11px] font-medium truncate';

  if (!onOpen) {
    return (
      <span className={cls} style={style} title={title} data-testid="node-topic-pill">
        {label}
      </span>
    );
  }

  return (
    <button
      type="button"
      onClick={onOpen}
      className={`${cls} transition-colors hover:brightness-125`}
      style={style}
      title={`Open topic ${title}`}
      data-testid="node-topic-pill"
    >
      {label}
    </button>
  );
}

// ─── Card ──────────────────────────────────────────────────────────────

function NodeCardComponent({
  node,
  authorNames,
  topicTitles,
  onEdit,
  onDelete,
  onOpenTopic,
}: NodeCardProps) {
  const isAgent = node.nodeType === 'synthesis' || node.nodeType === 'system';
  const avatar = describeNodeAvatar(node.authorId, {
    names: authorNames,
    isAgent,
  });
  const meta = formatNodeMeta(node);
  const topic = parseTopicFromNode(node, topicTitles);
  const accent = nodeTypeColor(node.nodeType, { isAgent });
  const timeAgo = formatTimeAgo(node.createdAt);

  return (
    <article
      aria-label={nodeCardAriaLabel(node, avatar.name)}
      title={`${meta.typeLabel} ${meta.shortId}`}
      data-testid="node-card"
      className="group relative rounded-xl border border-line-subtle bg-surface-panel p-4 transition-colors hover:border-line-strong hover:bg-surface-hover/25 focus-within:border-line-strong"
      style={{ borderLeft: `2px solid ${alpha(accent, 0.55)}` }}
    >
      {/* Header — avatar, author, timestamp, overflow menu */}
      <div className="flex items-start gap-3">
        <NodeAvatar
          authorId={node.authorId}
          isAgent={isAgent}
          names={authorNames}
        />

        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <span className="min-w-0 flex-1 truncate text-sm font-semibold text-content-primary">
              {avatar.name}
            </span>
            {timeAgo && (
              <time
                dateTime={node.createdAt}
                className="shrink-0 text-[11px] tabular-nums text-content-muted"
              >
                {timeAgo}
              </time>
            )}
          </div>

          {/* Meta row — replaces the raw mono-id dump */}
          <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-content-muted">
            <span className="inline-flex items-center gap-1">
              <TypeIcon nodeType={node.nodeType} color={accent} />
              {meta.typeLabel}
            </span>
            <span aria-hidden="true">·</span>
            <span>{meta.depthLabel}</span>
            {meta.replyLabel && (
              <>
                <span aria-hidden="true">·</span>
                <span>{meta.replyLabel}</span>
              </>
            )}
            {meta.edited && (
              <>
                <span aria-hidden="true">·</span>
                <span className="text-status-warning">edited</span>
              </>
            )}
          </div>
        </div>

        <OverflowMenu
          label={`${meta.typeLabel} by ${avatar.name}`}
          onEdit={onEdit}
          onDelete={onDelete}
        />
      </div>

      {/* Body */}
      <p className="mt-2.5 line-clamp-3 text-sm leading-relaxed break-words whitespace-pre-wrap text-content-secondary">
        {node.content}
      </p>

      {/* Topic pill — omitted entirely when the node carries no topic */}
      {topic && (
        <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
          <TopicPill
            label={topicPillLabel(topic)}
            title={topic.label}
            onOpen={
              onOpenTopic ? () => onOpenTopic(topic.id, topic.slug) : undefined
            }
          />
        </div>
      )}
    </article>
  );
}

export const NodeCard = memo(NodeCardComponent);
export default NodeCard;
