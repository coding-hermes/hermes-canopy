/**
 * Hermes Canopy — MessageNode (UI-04, Phase 11 Mockup Parity)
 *
 * The primary card on the branching canvas (docs/mockups/mockup-1.png):
 *
 *   ┌──────────────────────────────┐
 *   │ (SC) Sarah Chen        09:48 │  avatar + author + time
 *   │ Improve core product…        │  body preview
 *   │ 💬 3                         │  reply badge (hidden on a leaf)
 *   └──────────────────────────────┘─◗ collapse chevron on the connector
 *
 * Human and agent messages share the layout and differ only in accent:
 * cyan for people, violet for agents. Selection lights the card with the
 * neon glow. All chrome comes from NodeChrome; all colour from tokens.
 */

import { memo } from 'react';
import { type NodeProps, type Node } from '@xyflow/react';
import type { TreeNodeCardData } from '../../types/tree.ts';
import { palette, alpha } from '../../theme.ts';
import { formatNodeTime, nodeAuthorName, previewText } from '../../lib/nodeCard.ts';
import {
  CollapseChevron,
  NodeAvatar,
  NodeShell,
  ReplyBadge,
} from './NodeChrome.tsx';

type MessageNodeType = Node<TreeNodeCardData, 'messageNode'>;

function MessageNodeComponent({ data, selected }: NodeProps<MessageNodeType>) {
  const d = data as unknown as TreeNodeCardData;

  const isAgent = d.isAgent === true;
  const accent = isAgent ? palette.accent2 : palette.accent;
  const authorName = nodeAuthorName(d.authorId, {
    names: d.authorNames,
    isAgent,
  });

  // Real data only: the canvas supplies a derived reply count; childCount
  // from the Yjs snapshot is the fallback. Never a literal.
  const replyCount = d.replyCount ?? d.childCount ?? 0;
  const canCollapse = typeof d.onToggleCollapse === 'function';

  return (
    <NodeShell
      accent={accent}
      selected={selected}
      ariaLabel={`${isAgent ? 'Agent' : 'Message'} from ${authorName}: ${
        previewText(d.content, 60) || 'untitled'
      }`}
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
      {/* Header — avatar, author, timestamp */}
      <div className="flex items-center gap-2 px-2.5 pt-2.5">
        <NodeAvatar
          authorId={d.authorId}
          isAgent={isAgent}
          names={d.authorNames}
        />
        <span className="min-w-0 flex-1 truncate text-xs font-semibold text-content-primary">
          {authorName}
        </span>
        <span className="shrink-0 text-[10px] text-content-faint">
          {formatNodeTime(d.createdAt)}
        </span>
      </div>

      {/* Body */}
      <div className="px-2.5 pt-1.5">
        <p className="line-clamp-3 text-xs leading-snug break-words whitespace-pre-wrap text-content-secondary">
          {previewText(d.content)}
        </p>
      </div>

      {/* Footer — reply badge + agent tag */}
      <div className="flex items-center gap-1.5 px-2.5 pt-2 pb-2.5">
        <ReplyBadge count={replyCount} accent={accent} />
        {isAgent && (
          <span
            className="rounded-full px-1.5 py-0.5 text-[9px] font-medium tracking-wide uppercase"
            style={{
              backgroundColor: alpha(palette.accent2, 0.12),
              color: palette.accent2,
            }}
          >
            Agent
          </span>
        )}
      </div>
    </NodeShell>
  );
}

export const MessageNode = memo(MessageNodeComponent);
