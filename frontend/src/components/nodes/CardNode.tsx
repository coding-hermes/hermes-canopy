/**
 * Hermes Canopy — CardNode (UI-04, Phase 11 Mockup Parity)
 *
 * A structured Card node (File, Task, Code) on the branching canvas.
 * Shares the card chrome with every other node — avatar, reply badge,
 * collapse chevron — and distinguishes itself by a type icon and a
 * per-type identity accent drawn from the token palette.
 */

import { memo } from 'react';
import { type NodeProps, type Node } from '@xyflow/react';
import { Code2, FileText, ListChecks } from 'lucide-react';
import type { TreeNodeCardData } from '../../types/tree.ts';
import { palette } from '../../theme.ts';
import { formatNodeTime, previewText } from '../../lib/nodeCard.ts';
import {
  CollapseChevron,
  NodeAvatar,
  NodeShell,
  ReplyBadge,
} from './NodeChrome.tsx';

type CardNodeType = Node<TreeNodeCardData, 'cardNode'>;

type CardKind = 'file' | 'task' | 'code';

const CARD_ICON = {
  file: FileText,
  task: ListChecks,
  code: Code2,
} as const;

/** Identity accent per card type — token palette only. */
const CARD_ACCENT: Record<CardKind, string> = {
  file: palette.info,
  task: palette.success,
  code: palette.accent2,
};

function CardNodeComponent({ data, selected }: NodeProps<CardNodeType>) {
  const d = data as unknown as TreeNodeCardData;
  const cardType: CardKind = d.cardType ?? 'file';
  const Icon = CARD_ICON[cardType] ?? FileText;
  const accent = CARD_ACCENT[cardType] ?? palette.info;

  const replyCount = d.replyCount ?? d.childCount ?? 0;
  const canCollapse = typeof d.onToggleCollapse === 'function';

  return (
    <NodeShell
      accent={accent}
      selected={selected}
      ariaLabel={`${cardType} card: ${previewText(d.content, 60) || 'untitled'}`}
      minWidth={200}
      maxWidth={260}
      adornment={
        canCollapse ? (
          <CollapseChevron
            collapsed={d.collapsed === true}
            hiddenCount={d.hiddenCount ?? 0}
            onToggle={() => d.onToggleCollapse?.()}
            accent={accent}
          />
        ) : undefined
      }
    >
      {/* Header */}
      <div className="flex items-center gap-2 px-2.5 pt-2.5">
        <NodeAvatar authorId={d.authorId} names={d.authorNames} size="sm" />
        <span
          className="inline-flex min-w-0 flex-1 items-center gap-1 truncate text-xs font-semibold capitalize"
          style={{ color: accent }}
        >
          <Icon className="h-3 w-3 shrink-0" aria-hidden="true" />
          {cardType}
        </span>
        <span className="shrink-0 text-[10px] text-content-faint">
          {formatNodeTime(d.createdAt)}
        </span>
      </div>

      {/* Body — card payloads are usually code/paths, so monospace */}
      <div className="px-2.5 pt-1.5">
        <p className="line-clamp-2 font-mono text-[11px] leading-snug break-words whitespace-pre-wrap text-content-secondary">
          {previewText(d.content, 100) || `${cardType} card`}
        </p>
      </div>

      {/* Footer */}
      <div className="flex items-center gap-1.5 px-2.5 pt-2 pb-2.5">
        <ReplyBadge count={replyCount} accent={accent} />
      </div>
    </NodeShell>
  );
}

export const CardNode = memo(CardNodeComponent);
