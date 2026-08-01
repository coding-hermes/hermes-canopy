/**
 * Hermes Canopy — Tree Canvas (React Flow)
 *
 * Renders a conversation DAG as a navigable tree view with:
 *   - Custom node types (MessageNode, SynthesisNode, CardNode, TopicNode)
 *   - Custom edge types (ReplyEdge, ForkEdge, SynthesisEdge)
 *   - Expand/collapse for branches
 *   - Zoom-to-fit and focus-on-node animations
 *   - Large tree fallback (>500 nodes) with simplified rendering
 *   - Keyboard shortcuts (Ctrl+0 fit, Ctrl+= zoom in, Ctrl+- zoom out)
 *   - MiniMap with dark theme styling
 *   - Collaborative cursors overlay (multi-user)
 *
 * Built on @xyflow/react v12.
 */

import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  MarkerType,
  ReactFlowProvider,
  useReactFlow,
  type Node,
  type NodeTypes,
  type Edge,
  type FitViewOptions,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import type { TreeNodeCardData } from '../types/tree.ts';
import type { UseYjsTreeResult } from '../stores/useYjsTree.ts';
import { shouldUseSimplifiedMode } from '../layouts/d3Layout.ts';
import { palette, nodeTypeColor } from '../theme.ts';

// ─── Custom nodes ─────────────────────────────────────────────────────

import { MessageNode } from './nodes/MessageNode.tsx';
import { SynthesisNode } from './nodes/SynthesisNode.tsx';
import { CardNode } from './nodes/CardNode.tsx';
import { TopicNode } from './nodes/TopicNode.tsx';
import { AgentCardNode } from './agent/AgentCardNode.tsx';

// ─── Custom edges ─────────────────────────────────────────────────────

import { ReplyEdge } from './edges/ReplyEdge.tsx';
import { ForkEdge } from './edges/ForkEdge.tsx';
import { SynthesisEdge } from './edges/SynthesisEdge.tsx';

// ─── Registries ───────────────────────────────────────────────────────

const nodeTypes: NodeTypes = {
  messageNode: MessageNode,
  synthesisNode: SynthesisNode,
  cardNode: CardNode,
  topicNode: TopicNode,
  agentCardNode: AgentCardNode,
};

const edgeTypes = {
  replyEdge: ReplyEdge,
  forkEdge: ForkEdge,
  synthesisEdge: SynthesisEdge,
};

// ─── Tree Canvas Props ────────────────────────────────────────────────

export interface TreeCanvasProps {
  /** Tree data from useYjsTree hook */
  tree: UseYjsTreeResult;
  /** Called when the user selects a node (click) */
  onSelectionChange?: (nodeId: string | null) => void;
  /** When set, the canvas will focus/center on this node */
  focusNodeId?: string | null;
  /** Override nodesDraggable (e.g. disable for viewer permission) */
  nodesDraggable?: boolean;
  /** Collaborative cursors overlay (multi-user) */
  collaborativeCursors?: ReactNode;
  /** Called when the user's mouse moves on the canvas (screen coords) */
  onCanvasMouseMove?: (x: number, y: number) => void;
}

// ─── Collapse helpers ─────────────────────────────────────────────────

/**
 * Compute which nodes should be hidden because an ancestor is collapsed.
 */
function computeHiddenNodes(
  _nodes: Node<TreeNodeCardData>[],
  collapsedNodeIds: Set<string>,
  edges: Edge[],
): Set<string> {
  const hidden = new Set<string>();

  // Build child map
  const childrenMap = new Map<string, string[]>();
  for (const edge of edges) {
    const list = childrenMap.get(edge.source) ?? [];
    list.push(edge.target);
    childrenMap.set(edge.source, list);
  }

  function markDescendants(nodeId: string): void {
    const children = childrenMap.get(nodeId) ?? [];
    for (const childId of children) {
      if (!hidden.has(childId)) {
        hidden.add(childId);
        markDescendants(childId);
      }
    }
  }

  for (const nodeId of collapsedNodeIds) {
    markDescendants(nodeId);
  }

  return hidden;
}

// ─── Main Component ───────────────────────────────────────────────────

function TreeCanvasInner({
  tree,
  onSelectionChange,
  focusNodeId,
  nodesDraggable: nodesDraggableOverride,
  collaborativeCursors,
  onCanvasMouseMove,
}: TreeCanvasProps) {
  const { nodes: allNodes, edges: allEdges, treeTitle, isReady } = tree;
  const reactFlowInstance = useReactFlow();

  // Collapse state
  const [collapsedNodes, setCollapsedNodes] = useState<Set<string>>(new Set());

  // Compute hidden nodes from collapse state
  const hiddenNodes = useMemo(
    () => computeHiddenNodes(allNodes, collapsedNodes, allEdges),
    [allNodes, collapsedNodes, allEdges],
  );

  // Filter visible nodes and edges
  const visibleNodes = useMemo(
    () => allNodes.filter((n) => !hiddenNodes.has(n.id)),
    [allNodes, hiddenNodes],
  );

  const visibleNodeIds = useMemo(
    () => new Set(visibleNodes.map((n) => n.id)),
    [visibleNodes],
  );

  const visibleEdges = useMemo(
    () =>
      allEdges.filter(
        (e) => visibleNodeIds.has(e.source) && visibleNodeIds.has(e.target),
      ),
    [allEdges, visibleNodeIds],
  );

  // Large tree detection
  const totalCount = allNodes.length;
  const isLargeTree = shouldUseSimplifiedMode(totalCount);

  // Determine draggable state
  const isDraggable = nodesDraggableOverride ?? !isLargeTree;

  // ─── Handlers ────────────────────────────────────────────────────

  /** Toggle collapse/expand for a node's subtree */
  const toggleCollapse = useCallback(
    (nodeId: string) => {
      setCollapsedNodes((prev) => {
        const next = new Set(prev);
        if (next.has(nodeId)) {
          next.delete(nodeId);
        } else {
          next.add(nodeId);
        }
        return next;
      });
    },
    [],
  );

  /** Handle node click: select node, toggle collapse on double-click */
  const onNodeClick = useCallback(
    (_event: React.MouseEvent, node: Node) => {
      // Single click selects
      onSelectionChange?.(node.id);

      // Double-click toggles collapse
      if ((_event as unknown as { detail: number }).detail === 2) {
        toggleCollapse(node.id);
      }
    },
    [toggleCollapse, onSelectionChange],
  );

  /** Track mouse movement on canvas for cursor tracking */
  const handleMouseMove = useCallback(
    (event: React.MouseEvent) => {
      if (!onCanvasMouseMove) return;
      const bounds = event.currentTarget.getBoundingClientRect();
      const x = event.clientX - bounds.left;
      const y = event.clientY - bounds.top;
      onCanvasMouseMove(x, y);
    },
    [onCanvasMouseMove],
  );

  /** Deselect when clicking the canvas background */
  const onPaneClick = useCallback(() => {
    onSelectionChange?.(null);
  }, [onSelectionChange]);

  /** Zoom to fit all visible nodes with animation */
  const zoomToFit = useCallback(() => {
    const opts: FitViewOptions = {
      padding: 0.3,
      duration: 400,
      maxZoom: 1.5,
    };
    reactFlowInstance.fitView(opts);
  }, [reactFlowInstance]);

  /** Focus on a specific node with animation */
  const focusOnNode = useCallback(
    (nodeId: string) => {
      const node = allNodes.find((n) => n.id === nodeId);
      if (node) {
        reactFlowInstance.setCenter(node.position.x, node.position.y, {
          zoom: 1.0,
          duration: 500,
        });
      }
    },
    [allNodes, reactFlowInstance],
  );

  // Store focusOnNode in ref for keyboard shortcut access
  const focusRef = useRef(focusOnNode);
  focusRef.current = focusOnNode;
  const zoomRef = useRef(zoomToFit);
  zoomRef.current = zoomToFit;

  // ─── Keyboard shortcuts ──────────────────────────────────────────

  // Track which node has keyboard focus (separate from selection)
  const [focusedNodeId, setFocusedNodeId] = useState<string | null>(null);

  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      const mod = e.ctrlKey || e.metaKey;

      if (mod && e.key === '0') {
        e.preventDefault();
        zoomRef.current();
      } else if (mod && (e.key === '=' || e.key === '+')) {
        e.preventDefault();
        reactFlowInstance.zoomIn({ duration: 200 });
      } else if (mod && e.key === '-') {
        e.preventDefault();
        reactFlowInstance.zoomOut({ duration: 200 });
      } else if (e.key === 'Tab' && !mod) {
        // Tab cycles through visible nodes
        e.preventDefault();
        const visibleIds = visibleNodes.map((n) => n.id);
        if (visibleIds.length === 0) return;
        const currentIdx = focusedNodeId
          ? visibleIds.indexOf(focusedNodeId)
          : -1;
        const nextIdx = e.shiftKey
          ? (currentIdx <= 0 ? visibleIds.length - 1 : currentIdx - 1)
          : (currentIdx >= visibleIds.length - 1 ? 0 : currentIdx + 1);
        const nextId = visibleIds[nextIdx];
        setFocusedNodeId(nextId);
        onSelectionChange?.(nextId);
        focusRef.current(nextId);
      } else if (e.key === 'Enter' && focusedNodeId) {
        // Enter toggles collapse on focused node
        e.preventDefault();
        toggleCollapse(focusedNodeId);
      } else if (e.key === 'Escape') {
        // Escape deselects
        setFocusedNodeId(null);
        onSelectionChange?.(null);
      } else if (e.key === 'Home' && visibleNodes.length > 0) {
        // Home jumps to root node
        e.preventDefault();
        const rootId = visibleNodes[0]?.id;
        if (rootId) {
          setFocusedNodeId(rootId);
          onSelectionChange?.(rootId);
          focusRef.current(rootId);
        }
      }
    }

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [reactFlowInstance, visibleNodes, focusedNodeId, toggleCollapse, onSelectionChange]);

  // ─── Focus on node when focusNodeId prop changes ─────────────────

  // Ref to track the latest focusOnNode (avoid stale closure)
  const focusFnRef = useRef(focusOnNode);
  focusFnRef.current = focusOnNode;

  // Track previous focusNodeId to avoid re-focusing on same node
  const prevFocusRef = useRef<string | null | undefined>(undefined);

  useEffect(() => {
    if (focusNodeId && focusNodeId !== prevFocusRef.current) {
      prevFocusRef.current = focusNodeId;
      // Small delay to ensure layout is stable
      const timer = setTimeout(() => {
        focusFnRef.current(focusNodeId);
      }, 50);
      return () => clearTimeout(timer);
    }
    if (focusNodeId === null) {
      prevFocusRef.current = null;
    }
  }, [focusNodeId]);

  // ─── Loading state ───────────────────────────────────────────────

  if (!isReady) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-center">
          <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-accent mx-auto mb-3" />
          <p className="text-content-muted text-sm">
            Loading tree...
          </p>
        </div>
      </div>
    );
  }

  // ─── Empty state ─────────────────────────────────────────────────

  if (allNodes.length === 0) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-center">
          <p className="text-content-secondary text-lg mb-2">
            🌳 Empty Tree
          </p>
          <p className="text-content-muted text-sm">
            {treeTitle
              ? `"${treeTitle}" has no nodes yet.`
              : 'Create a tree to get started.'}
          </p>
        </div>
      </div>
    );
  }

  // ─── Render ──────────────────────────────────────────────────────

  return (
    <div
      className="h-full w-full relative bg-surface-base"
      onMouseMove={handleMouseMove}
      role="application"
      aria-label={`Tree canvas: ${treeTitle || 'Untitled'} — ${totalCount} nodes`}
      aria-roledescription="Interactive tree visualization"
    >
      {/* Large tree warning banner */}
      {isLargeTree && (
        <div className="glass absolute top-2 left-1/2 -translate-x-1/2 z-10 rounded-lg px-4 py-1.5 text-sm text-status-warning ring-1 ring-inset ring-amber-400/30">
          ⚠️ Large tree ({totalCount} nodes) — simplified rendering active
        </div>
      )}

      {/* Collapse info */}
      {collapsedNodes.size > 0 && (
        <div className="glass absolute top-2 right-4 z-10 rounded-lg px-3 py-1.5 text-xs text-content-secondary">
          {collapsedNodes.size} branch{collapsedNodes.size !== 1 ? 'es' : ''}{' '}
          collapsed · {hiddenNodes.size} node{hiddenNodes.size !== 1 ? 's' : ''}{' '}
          hidden
        </div>
      )}

      {/* Collaborative cursors overlay — rendered absolutely over the canvas */}
      {collaborativeCursors}

      <ReactFlow
        nodes={visibleNodes}
        edges={visibleEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        fitView
        fitViewOptions={{ padding: 0.3, duration: 300 }}
        minZoom={isLargeTree ? 0.05 : 0.1}
        maxZoom={2}
        defaultEdgeOptions={{
          type: 'replyEdge',
          markerEnd: {
            type: MarkerType.ArrowClosed,
            width: 16,
            height: 16,
            color: palette.contentFaint,
          },
        }}
        // Large tree optimizations
        onlyRenderVisibleElements={isLargeTree}
        nodesDraggable={isDraggable}
        nodesConnectable={false}
        elementsSelectable
        elevateEdgesOnSelect
        proOptions={{ hideAttribution: true }}
        onNodeClick={onNodeClick}
        onPaneClick={onPaneClick}
      >
        <Background
          color={palette.surfaceHover}
          gap={isLargeTree ? 40 : 20}
          size={1}
        />
        <Controls
          position="bottom-right"
          showZoom
          showFitView
          showInteractive={false}
          onFitView={zoomToFit}
          className="!bg-surface-panel !border-line-subtle !shadow-lg !rounded-lg !overflow-hidden [&_button]:!bg-surface-panel [&_button]:!border-line-subtle [&_button]:!text-content-primary [&_button:hover]:!bg-accent-2-600 [&_button:hover]:!text-white [&_button_svg]:!fill-content-primary [&_button:hover_svg]:!fill-white"
          aria-label="Canvas controls: zoom in, zoom out, fit view"
        />
        <MiniMap
          position="bottom-right"
          nodeColor={(n) => {
            const d = n.data as TreeNodeCardData | undefined;
            if (!d) return palette.neutral;
            // Agent cards/messages take the violet accent; everything else
            // follows the shared node-type identity palette.
            return nodeTypeColor(d.nodeType, {
              isAgent: d.isAgent || d.isAgentCard,
            });
          }}
          maskColor="rgba(11,13,23,0.72)"
          className="!bg-surface-panel !border-line-subtle !shadow-lg !rounded-lg [&_svg]:!rounded-md"
          style={{ width: 180, height: 120 }}
          aria-label="Tree minimap: overview of all nodes"
        />
      </ReactFlow>
    </div>
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
