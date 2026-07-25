/**
 * Hermes Canopy — CardNode
 *
 * Renders a structured Card node (File, Task, Code).
 * Visual: card-like container with type icon, structured data display.
 */

import { memo } from 'react';
import { Handle, Position, type NodeProps, type Node } from '@xyflow/react';
import type { TreeNodeCardData } from '../../types/tree.ts';

type CardNodeType = Node<TreeNodeCardData, 'cardNode'>;

const CARD_ICONS: Record<string, string> = {
  file: '📄',
  task: '✅',
  code: '💻',
};

const CARD_COLORS: Record<string, { border: string; bg: string; text: string; darkBg: string; darkText: string }> = {
  file: {
    border: 'border-blue-200 dark:border-blue-800',
    bg: 'bg-blue-50 dark:bg-blue-950/40',
    text: 'text-blue-800 dark:text-blue-200',
    darkBg: 'dark:bg-blue-950/40',
    darkText: 'dark:text-blue-200',
  },
  task: {
    border: 'border-green-200 dark:border-green-800',
    bg: 'bg-green-50 dark:bg-green-950/40',
    text: 'text-green-800 dark:text-green-200',
    darkBg: 'dark:bg-green-950/40',
    darkText: 'dark:text-green-200',
  },
  code: {
    border: 'border-indigo-200 dark:border-indigo-800',
    bg: 'bg-indigo-50 dark:bg-indigo-950/40',
    text: 'text-indigo-800 dark:text-indigo-200',
    darkBg: 'dark:bg-indigo-950/40',
    darkText: 'dark:text-indigo-200',
  },
};

function CardNodeComponent({ data, selected }: NodeProps<CardNodeType>) {
  const typedData = data as unknown as TreeNodeCardData;
  const cardType = typedData.cardType ?? 'file';
  const icon = CARD_ICONS[cardType] ?? '📋';
  const colors = CARD_COLORS[cardType] ?? CARD_COLORS.file;

  return (
    <div
      className={`rounded-lg border-2 bg-white dark:bg-gray-800 shadow-sm transition-all duration-150 min-w-[200px] max-w-[260px] ${colors.border} ${
        selected
          ? 'border-purple-500 ring-2 ring-purple-500/30 shadow-md'
          : ''
      }`}
    >
      <Handle
        type="target"
        position={Position.Top}
        className="!bg-blue-500 !w-3 !h-3 !border-2 !border-white dark:!border-gray-800"
      />

      {/* Header with card type */}
      <div className={`flex items-center gap-2 px-3 py-2 rounded-t-md ${colors.bg}`}>
        <span className="text-base">{icon}</span>
        <span className={`text-sm font-semibold capitalize ${colors.text}`}>
          {cardType}
        </span>
      </div>

      {/* Content */}
      <div className="px-3 py-2">
        <p className="text-sm text-gray-900 dark:text-gray-100 line-clamp-2 whitespace-pre-wrap break-words font-mono text-xs">
          {typedData.content.length > 100
            ? `${typedData.content.slice(0, 100)}…`
            : typedData.content || `${cardType} card`}
        </p>
      </div>

      {/* Footer */}
      <div className="px-3 py-1.5 border-t border-gray-100 dark:border-gray-700 flex items-center gap-2 text-xs text-gray-400 dark:text-gray-500">
        <span>{new Date(typedData.createdAt).toLocaleDateString()}</span>
      </div>

      <Handle
        type="source"
        position={Position.Bottom}
        className="!bg-blue-500 !w-3 !h-3 !border-2 !border-white dark:!border-gray-800"
      />
    </div>
  );
}

export const CardNode = memo(CardNodeComponent);
