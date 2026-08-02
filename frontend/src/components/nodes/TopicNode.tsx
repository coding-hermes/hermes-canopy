/**
 * Hermes Canopy — TopicNode (UI-04, Phase 11 Mockup Parity)
 *
 * A Topic node — a named, searchable subgraph with #references. Wears the
 * shared branching-canvas chrome with the magenta topic accent, matching
 * the topic pills in the sidebar rail (UI-02).
 */

import { memo } from 'react';
import { type NodeProps, type Node } from '@xyflow/react';
import { Hash } from 'lucide-react';
import type { TreeNodeCardData } from '../../types/tree.ts';
import { palette } from '../../theme.ts';
import { previewText } from '../../lib/nodeCard.ts';
import {
  CollapseChevron,
  NodeAvatar,
  NodeShell,
  ReplyBadge,
} from './NodeChrome.tsx';

type TopicNodeType = Node<TreeNodeCardData, 'topicNode'>;

const ACCENT = palette.accent3;

function TopicNodeComponent({ data, selected }: NodeProps<TopicNodeType>) {
  const d = data as unknown as TreeNodeCardData;
  const topicName = (d.label ?? '').replace(/^#/, '') || 'topic';

  const replyCount = d.replyCount ?? d.childCount ?? 0;
  const canCollapse = typeof d.onToggleCollapse === 'function';

  return (
    <NodeShell
      accent={ACCENT}
      selected={selected}
      ariaLabel={`Topic: ${topicName}`}
      minWidth={180}
      maxWidth={240}
      adornment={
        canCollapse ? (
          <CollapseChevron
            collapsed={d.collapsed === true}
            hiddenCount={d.hiddenCount ?? 0}
            onToggle={() => d.onToggleCollapse?.()}
            accent={ACCENT}
          />
        ) : undefined
      }
    >
      {/* Header */}
      <div className="flex items-center gap-2 px-2.5 pt-2.5">
        <NodeAvatar authorId={d.authorId} names={d.authorNames} size="sm" />
        <span
          className="inline-flex min-w-0 flex-1 items-center gap-0.5 truncate text-xs font-semibold"
          style={{ color: ACCENT }}
        >
          <Hash className="h-3 w-3 shrink-0" aria-hidden="true" />
          {topicName}
        </span>
      </div>

      {/* Body */}
      <div className="px-2.5 pt-1.5">
        <p className="line-clamp-2 text-xs leading-snug break-words whitespace-pre-wrap text-content-secondary">
          {previewText(d.content, 100) ||
            (replyCount > 0 ? `Topic with ${replyCount} nodes` : 'Empty topic')}
        </p>
      </div>

      {/* Footer */}
      <div className="flex items-center gap-1.5 px-2.5 pt-2 pb-2.5">
        <ReplyBadge count={replyCount} accent={ACCENT} />
      </div>
    </NodeShell>
  );
}

export const TopicNode = memo(TopicNodeComponent);
