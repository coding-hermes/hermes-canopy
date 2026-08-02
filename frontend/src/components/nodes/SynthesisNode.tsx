/**
 * Hermes Canopy — SynthesisNode (UI-04, Phase 11 Mockup Parity)
 *
 * A synthesis node combines multiple parent branches. It wears the same
 * card chrome as every other node on the branching canvas (avatar, reply
 * badge, collapse chevron) with an amber identity accent and a ⊕ marker
 * so a merge stays legible at a glance.
 */

import { memo } from 'react';
import { Handle, Position, type NodeProps, type Node } from '@xyflow/react';
import { GitMerge } from 'lucide-react';
import type { TreeNodeCardData } from '../../types/tree.ts';
import { palette, alpha } from '../../theme.ts';
import { formatNodeTime, nodeAuthorName, previewText } from '../../lib/nodeCard.ts';
import {
  CollapseChevron,
  NodeAvatar,
  NodeShell,
  ReplyBadge,
} from './NodeChrome.tsx';

type SynthesisNodeType = Node<TreeNodeCardData, 'synthesisNode'>;

const ACCENT = palette.warning;

function SynthesisNodeComponent({
  data,
  selected,
}: NodeProps<SynthesisNodeType>) {
  const d = data as unknown as TreeNodeCardData;
  const authorName = nodeAuthorName(d.authorId, { names: d.authorNames });
  const replyCount = d.replyCount ?? d.childCount ?? 0;
  const canCollapse = typeof d.onToggleCollapse === 'function';

  return (
    <NodeShell
      accent={ACCENT}
      selected={selected}
      ariaLabel={`Synthesis node: ${previewText(d.content, 60) || 'untitled'}`}
      minWidth={210}
      maxWidth={280}
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
      {/*
        Synthesis nodes accept several inbound edges. React Flow needs one
        handle per side; the extra top handle gives multi-parent links a
        second face to land on so they don't stack on the left edge.
      */}
      <Handle
        type="target"
        position={Position.Top}
        id="merge"
        className="!h-2 !w-2 !border-0"
        style={{ backgroundColor: alpha(ACCENT, 0.8) }}
        isConnectable={false}
        aria-hidden="true"
      />

      {/* Header */}
      <div className="flex items-center gap-2 px-2.5 pt-2.5">
        <NodeAvatar authorId={d.authorId} names={d.authorNames} />
        <span
          className="inline-flex min-w-0 flex-1 items-center gap-1 truncate text-xs font-semibold"
          style={{ color: ACCENT }}
        >
          <GitMerge className="h-3 w-3 shrink-0" aria-hidden="true" />
          Synthesis
        </span>
        <span className="shrink-0 text-[10px] text-content-faint">
          {formatNodeTime(d.createdAt)}
        </span>
      </div>

      {/* Body */}
      <div className="px-2.5 pt-1.5">
        <p className="line-clamp-3 text-xs leading-snug break-words whitespace-pre-wrap text-content-secondary">
          {previewText(d.content, 150) || 'Synthesized from multiple branches'}
        </p>
      </div>

      {/* Footer */}
      <div className="flex items-center gap-1.5 px-2.5 pt-2 pb-2.5">
        <ReplyBadge count={replyCount} accent={ACCENT} />
        <span className="truncate text-[10px] text-content-faint">
          {authorName}
        </span>
      </div>
    </NodeShell>
  );
}

export const SynthesisNode = memo(SynthesisNodeComponent);
