/**
 * Hermes Canopy — MessageNode
 *
 * Renders a standard conversation message node.
 * Supports human (green) and agent (purple) variants.
 * Shows content preview, timestamp, and author badge.
 */

import { memo } from 'react';
import { Handle, Position, type NodeProps, type Node } from '@xyflow/react';
import type { TreeNodeCardData } from '../../types/tree.ts';

type MessageNodeType = Node<TreeNodeCardData, 'messageNode'>;

function MessageNodeComponent({ data, selected }: NodeProps<MessageNodeType>) {
  const typedData = data as unknown as TreeNodeCardData;

  const isAgent = typedData.isAgent;
  const badgeLabel = isAgent ? '🤖 Agent' : '👤 Human';
  const badgeClass = isAgent
    ? 'bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300'
    : 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300';
  const borderClass = isAgent
    ? 'border-l-purple-400 dark:border-l-purple-500'
    : 'border-l-green-400 dark:border-l-green-500';

  return (
    <div
      className={`rounded-lg border bg-white dark:bg-gray-800 shadow-sm transition-all duration-150 min-w-[180px] max-w-[260px] border-l-4 ${borderClass} ${
        selected
          ? 'border-purple-500 ring-2 ring-purple-500/30 shadow-md'
          : 'border-gray-200 dark:border-gray-700'
      }`}
    >
      {/* Target handle (incoming edges from parent) */}
      <Handle
        type="target"
        position={Position.Top}
        className="!bg-gray-400 !w-3 !h-3 !border-2 !border-white dark:!border-gray-800"
      />

      {/* Header */}
      <div className="flex items-center justify-between px-3 py-1.5 border-b border-gray-100 dark:border-gray-700">
        <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${badgeClass}`}>
          {badgeLabel}
        </span>
        {typedData.childCount > 0 && (
          <span className="text-xs text-gray-400 dark:text-gray-500">
            {typedData.childCount} {typedData.childCount === 1 ? 'reply' : 'replies'}
          </span>
        )}
      </div>

      {/* Content */}
      <div className="px-3 py-2">
        <p className="text-sm text-gray-900 dark:text-gray-100 line-clamp-3 whitespace-pre-wrap break-words">
          {typedData.content.length > 120
            ? `${typedData.content.slice(0, 120)}…`
            : typedData.content}
        </p>
      </div>

      {/* Footer */}
      <div className="px-3 py-1.5 border-t border-gray-100 dark:border-gray-700 flex items-center gap-2 text-xs text-gray-400 dark:text-gray-500">
        <span>{new Date(typedData.createdAt).toLocaleDateString()}</span>
      </div>

      {/* Source handle (outgoing edges to children) */}
      <Handle
        type="source"
        position={Position.Bottom}
        className="!bg-gray-400 !w-3 !h-3 !border-2 !border-white dark:!border-gray-800"
      />
    </div>
  );
}

export const MessageNode = memo(MessageNodeComponent);
