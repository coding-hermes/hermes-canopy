/**
 * Hermes Canopy — TopicNode
 *
 * Renders a Topic node — a named, searchable subgraph with #references.
 * Visual: hashtag badge, collapsible, shows child count.
 */

import { memo } from 'react';
import { Handle, Position, type NodeProps, type Node } from '@xyflow/react';
import type { TreeNodeCardData } from '../../types/tree.ts';

type TopicNodeType = Node<TreeNodeCardData, 'topicNode'>;

function TopicNodeComponent({ data, selected }: NodeProps<TopicNodeType>) {
  const typedData = data as unknown as TreeNodeCardData;
  const topicName = typedData.label.replace(/^#/, '');

  return (
    <div
      className={`rounded-lg border bg-rose-50 dark:bg-rose-950/40 shadow-sm transition-all duration-150 min-w-[180px] max-w-[240px] ${
        selected
          ? 'border-rose-500 ring-2 ring-rose-500/30 shadow-md'
          : 'border-rose-200 dark:border-rose-800'
      }`}
      role="article"
      aria-label={`Topic: ${topicName}`}
      tabIndex={0}
    >
      <Handle
        type="target"
        position={Position.Top}
        className="!bg-rose-500 !w-3 !h-3 !border-2 !border-white dark:!border-gray-800"
        aria-label={`Connect input to topic ${topicName}`}
        role="button"
        tabIndex={0}
      />

      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-2 border-b border-rose-200 dark:border-rose-800" role="heading" aria-level={3}>
        <span className="text-rose-500 dark:text-rose-400 text-lg font-bold" aria-hidden="true">#</span>
        <span className="text-sm font-semibold text-rose-800 dark:text-rose-200 truncate">
          {topicName}
        </span>
      </div>

      {/* Content */}
      <div className="px-3 py-2">
        <p className="text-sm text-rose-900 dark:text-rose-100 line-clamp-2 whitespace-pre-wrap break-words">
          {typedData.content.length > 100
            ? `${typedData.content.slice(0, 100)}…`
            : typedData.content || `Topic with ${typedData.childCount} nodes`}
        </p>
      </div>

      {/* Footer */}
      <div className="px-3 py-1.5 border-t border-rose-200 dark:border-rose-800 flex items-center gap-2 text-xs text-rose-500 dark:text-rose-400">
        <span>{typedData.childCount > 0 ? `${typedData.childCount} nodes` : 'Empty topic'}</span>
      </div>

      <Handle
        type="source"
        position={Position.Bottom}
        className="!bg-rose-500 !w-3 !h-3 !border-2 !border-white dark:!border-gray-800"
        aria-label={`Connect output from topic ${topicName}`}
        role="button"
        tabIndex={0}
      />
    </div>
  );
}

export const TopicNode = memo(TopicNodeComponent);
