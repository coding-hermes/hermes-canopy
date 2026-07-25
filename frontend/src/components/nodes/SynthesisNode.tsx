/**
 * Hermes Canopy — SynthesisNode
 *
 * Renders a synthesis/merge node that combines multiple parent branches.
 * Visual: amber/gold accent, merge icon, multi-handle for incoming edges.
 */

import { memo } from 'react';
import { Handle, Position, type NodeProps, type Node } from '@xyflow/react';
import type { TreeNodeCardData } from '../../types/tree.ts';

type SynthesisNodeType = Node<TreeNodeCardData, 'synthesisNode'>;

function SynthesisNodeComponent({ data, selected }: NodeProps<SynthesisNodeType>) {
  const typedData = data as unknown as TreeNodeCardData;

  return (
    <div
      className={`rounded-lg border bg-amber-50 dark:bg-amber-950/40 shadow-sm transition-all duration-150 min-w-[200px] max-w-[280px] ${
        selected
          ? 'border-amber-500 ring-2 ring-amber-500/30 shadow-md'
          : 'border-amber-200 dark:border-amber-800'
      }`}
      role="article"
      aria-label={`Synthesis node: ${typedData.content?.slice(0, 60) || 'untitled'}`}
      tabIndex={0}
    >
      {/* Target handle — multi-parent synthesis nodes accept from multiple sources */}
      <Handle
        type="target"
        position={Position.Top}
        className="!bg-amber-500 !w-3 !h-3 !border-2 !border-white dark:!border-gray-800"
        aria-label="Connect input from parent nodes to synthesis"
        role="button"
        tabIndex={0}
      />

      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-2 border-b border-amber-200 dark:border-amber-800" role="heading" aria-level={3}>
        <span className="text-amber-600 dark:text-amber-300 text-base" aria-hidden="true">⊕</span>
        <span className="text-sm font-semibold text-amber-800 dark:text-amber-200">
          Synthesis
        </span>
      </div>

      {/* Content */}
      <div className="px-3 py-2">
        <p className="text-sm text-amber-900 dark:text-amber-100 line-clamp-3 whitespace-pre-wrap break-words">
          {typedData.content.length > 150
            ? `${typedData.content.slice(0, 150)}…`
            : typedData.content || 'Synthesized from multiple branches'}
        </p>
      </div>

      {/* Footer */}
      <div className="px-3 py-1.5 border-t border-amber-200 dark:border-amber-800 flex items-center gap-2 text-xs text-amber-500 dark:text-amber-400">
        <span>{new Date(typedData.createdAt).toLocaleDateString()}</span>
      </div>

      {/* Source handle */}
      <Handle
        type="source"
        position={Position.Bottom}
        className="!bg-amber-500 !w-3 !h-3 !border-2 !border-white dark:!border-gray-800"
        aria-label="Connect output from synthesis node"
        role="button"
        tabIndex={0}
      />
    </div>
  );
}

export const SynthesisNode = memo(SynthesisNodeComponent);
