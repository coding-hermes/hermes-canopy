/**
 * Hermes Canopy — Tree Canvas (React Flow)
 *
 * Renders a conversation DAG as a navigable tree view.
 * Custom nodes (TreeNodeCard) show message metadata.
 * Custom edges show branch/fork/synthesis styling.
 *
 * Built on @xyflow/react v12 per T1.3 decision.
 */

import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  type Node,
  type NodeProps,
  type EdgeProps,
  type NodeTypes,
  BaseEdge,
  getSmoothStepPath,
  MarkerType,
  ReactFlowProvider,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import type { TreeNodeCardData } from '../types/tree.ts';
import type { UseYjsTreeResult } from '../stores/useYjsTree.ts';

// ─── Custom Node: TreeNodeCard ────────────────────────────────────────

type TreeNodeCardNode = Node<TreeNodeCardData, 'treeNodeCard'>;

function TreeNodeCard({ data, selected }: NodeProps<TreeNodeCardNode>) {
  const typedData = data as TreeNodeCardData;
  const isSynthesis = typedData.nodeType === 'synthesis';
  const isSystem = typedData.isSystem;

  let badge: string;
  let badgeClass: string;
  if (isSynthesis) {
    badge = '⊕ Merge';
    badgeClass = 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300';
  } else if (isSystem) {
    badge = '⚙ System';
    badgeClass = 'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300';
  } else if (typedData.isAgent) {
    badge = '🤖 Agent';
    badgeClass = 'bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300';
  } else {
    badge = '👤 Human';
    badgeClass = 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300';
  }

  return (
    <div
      className={`rounded-lg border bg-white dark:bg-gray-800 shadow-sm transition-shadow min-w-[180px] max-w-[260px] ${
        selected
          ? 'border-purple-500 ring-2 ring-purple-500/30 shadow-md'
          : 'border-gray-200 dark:border-gray-700'
      }`}
    >
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-gray-100 dark:border-gray-700">
        <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${badgeClass}`}>
          {badge}
        </span>
      </div>

      {/* Content */}
      <div className="px-3 py-2">
        <p className="text-sm text-gray-900 dark:text-gray-100 line-clamp-3">
          {typedData.content.length > 120
            ? `${typedData.content.slice(0, 120)}...`
            : typedData.content}
        </p>
      </div>

      {/* Footer */}
      <div className="px-3 py-1.5 border-t border-gray-100 dark:border-gray-700 flex items-center gap-2 text-xs text-gray-400 dark:text-gray-500">
        <span>{new Date(typedData.createdAt).toLocaleDateString()}</span>
      </div>
    </div>
  );
}

// ─── Custom Edge: SynthesisEdge ───────────────────────────────────────

function SynthesisEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  style,
  markerEnd,
}: EdgeProps) {
  const [edgePath] = getSmoothStepPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
    borderRadius: 12,
  });

  return (
    <BaseEdge
      id={id}
      path={edgePath}
      style={{
        ...(style as React.CSSProperties),
        stroke: '#f59e0b',
        strokeWidth: 2,
        strokeDasharray: '6,3',
      }}
      markerEnd={markerEnd}
    />
  );
}

// ─── Custom Edge: DefaultEdge ─────────────────────────────────────────

function DefaultEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  style,
  markerEnd,
}: EdgeProps) {
  const [edgePath] = getSmoothStepPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
    borderRadius: 8,
  });

  return (
    <BaseEdge
      id={id}
      path={edgePath}
      style={style as React.CSSProperties}
      markerEnd={markerEnd}
    />
  );
}

// ─── Edge Types Registry ──────────────────────────────────────────────

const edgeTypes = {
  synthesisEdge: SynthesisEdge,
  defaultEdge: DefaultEdge,
};

// ─── Node Types Registry ──────────────────────────────────────────────

const nodeTypes: NodeTypes = {
  treeNodeCard: TreeNodeCard,
};

// ─── Tree Canvas Props ────────────────────────────────────────────────

export interface TreeCanvasProps {
  /** Tree data from useYjsTree hook */
  tree: UseYjsTreeResult;
  /** Called when nodes change */
  onNodesChange?: (nodes: UseYjsTreeResult['nodes']) => void;
}

// ─── Tree Canvas Component ────────────────────────────────────────────

function TreeCanvasInner({ tree }: TreeCanvasProps) {
  const { nodes, edges, treeTitle, isReady } = tree;

  if (!isReady) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-center">
          <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-purple-500 mx-auto mb-3" />
          <p className="text-gray-500 dark:text-gray-400 text-sm">
            Loading tree...
          </p>
        </div>
      </div>
    );
  }

  if (nodes.length === 0) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-center">
          <p className="text-gray-500 dark:text-gray-400 text-lg mb-2">
            🌳 Empty Tree
          </p>
          <p className="text-gray-400 dark:text-gray-500 text-sm">
            {treeTitle ? `"${treeTitle}" has no nodes yet.` : 'Create a tree to get started.'}
          </p>
        </div>
      </div>
    );
  }

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      edgeTypes={edgeTypes}
      fitView
      fitViewOptions={{ padding: 0.3 }}
      minZoom={0.1}
      maxZoom={1.5}
      defaultEdgeOptions={{
        type: 'defaultEdge',
        markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color: '#6b7280' },
      }}
      proOptions={{ hideAttribution: true }}
    >
      <Background color="#e5e7eb" gap={20} size={1} />
      <Controls
        position="bottom-right"
        className="!bg-white dark:!bg-gray-800 !border-gray-200 dark:!border-gray-700"
      />
      <MiniMap
        position="bottom-left"
        nodeColor={(n) => {
          const d = n.data as TreeNodeCardData | undefined;
          if (d?.isSystem) return '#3b82f6';
          if (d?.isAgent) return '#7c3aed';
          if (d?.nodeType === 'synthesis') return '#f59e0b';
          return '#22c55e';
        }}
        className="!bg-gray-100 dark:!bg-gray-900 !border-gray-200 dark:!border-gray-700"
      />
    </ReactFlow>
  );
}

// ─── Wrapped export ───────────────────────────────────────────────────

/**
 * TreeCanvas requires a ReactFlowProvider ancestor.
 * This wrapper provides it so consumers don't need to.
 */
export default function TreeCanvas(props: TreeCanvasProps) {
  return (
    <ReactFlowProvider>
      <TreeCanvasInner {...props} />
    </ReactFlowProvider>
  );
}
