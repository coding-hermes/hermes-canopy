/**
 * Hermes Canopy — AgentCardNode
 *
 * React Flow node that renders agent context cards (iteration cards)
 * in the tree DAG. Routes to the appropriate card component based on
 * the card subtype stored in node metadata.
 *
 * Child nodes of agent messages — represents the agent's visible
 * work activity (search, code exec, file read, thinking, tool calls).
 *
 * Handles are Left (target) / Right (source) to match the left→right
 * branching canvas introduced in UI-04 — with Top/Bottom handles the
 * glowing connectors would leave the wrong faces and cross their cards.
 */

import { memo } from 'react';
import { Handle, Position, type NodeProps, type Node } from '@xyflow/react';
import type { TreeNodeCardData } from '../../types/tree.ts';
import {
  isAgentCardMetadata,
  asAgentCardMetadata,
  type IterationCardSubtypeData,
  type ThinkingData,
} from '../../types/agent.ts';
import { ThinkingCard } from './ThinkingCard.tsx';
import { IterationCard } from './IterationCard.tsx';

// ─── Node type ─────────────────────────────────────────────────────────

type AgentCardNodeType = Node<TreeNodeCardData, 'agentCardNode'>;

// ─── Data extraction ───────────────────────────────────────────────────

function extractCardData(
  metadata: Record<string, unknown> | undefined,
): IterationCardSubtypeData | null {
  if (!metadata || !isAgentCardMetadata(metadata)) return null;
  const agentMeta = asAgentCardMetadata(metadata);
  return agentMeta.cardData ?? null;
}

// ─── Component ─────────────────────────────────────────────────────────

function AgentCardNodeComponent({
  data,
  selected,
}: NodeProps<AgentCardNodeType>) {
  const typedData = data as unknown as TreeNodeCardData;
  const cardData = extractCardData(typedData.metadata);

  if (!cardData) {
    // Fallback: render basic card node style
    return (
      <div
        className={`rounded-lg border-2 bg-gray-800/90 border-gray-700 shadow-sm min-w-[180px] max-w-[280px] ${
          selected ? 'border-purple-500 ring-2 ring-purple-500/30' : ''
        }`}
      >
        <Handle
          type="target"
          position={Position.Left}
          className="!bg-purple-500 !w-3 !h-3 !border-2 !border-gray-900"
        />
        <div className="px-3 py-2 text-sm text-gray-400">
          Agent Card (no data)
        </div>
        <Handle
          type="source"
          position={Position.Right}
          className="!bg-purple-500 !w-3 !h-3 !border-2 !border-gray-900"
        />
      </div>
    );
  }

  // Render the appropriate card based on subtype
  const isThinking = cardData.subtype === 'iteration_thinking';

  return (
    <div className={`${selected ? 'ring-2 ring-purple-500/30 rounded-lg' : ''}`}>
      {/* Target handle — agent cards receive edges from parent messages */}
      <Handle
        type="target"
        position={Position.Left}
        className="!bg-purple-500 !w-3 !h-3 !border-2 !border-gray-900"
      />

      {isThinking ? (
        <ThinkingCard data={cardData as ThinkingData} />
      ) : (
        <IterationCard data={cardData} />
      )}

      {/* Source handle — agent cards can have child nodes */}
      <Handle
        type="source"
        position={Position.Right}
        className="!bg-purple-500 !w-3 !h-3 !border-2 !border-gray-900"
      />
    </div>
  );
}

export const AgentCardNode = memo(AgentCardNodeComponent);
