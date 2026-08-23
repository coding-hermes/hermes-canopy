/**
 * Hermes Canopy — Tree Canvas (React Flow)
 *
 * Renders a conversation DAG as the branching tree view from the vision
 * brief (docs/mockups/mockup-1.png):
 *
 *   - Horizontal left→right layout (d3-hierarchy, transposed in d3Layout)
 *   - Glowing bezier connectors with joint dots (edges/GlowConnector)
 *   - Colour-coded author avatars + reply badges on every card
 *   - Expand/collapse chevrons on the connector, state persisted per node
 *   - Dashed ghost slots at the frontier for "add a reply here"
 *   - Neon glow on the selected node
 *   - Large tree fallback (>500 nodes) with simplified rendering
 *   - Canvas 2D fallback above CANVAS_THRESHOLD (>2000 nodes, STACK-03):
 *     the wrapped export swaps in CanvasTreeView wholesale
 *   - Keyboard shortcuts (Ctrl+0 fit, Ctrl+= zoom in, Ctrl+- zoom out,
 *     Tab/Shift-Tab cycle, Enter collapse, Home root, Escape deselect)
 *   - MiniMap with dark theme styling
 *   - Collaborative cursors overlay (multi-user)
 *
 * Derivation (collapse algebra, reply counts, avatars, connector geometry)
 * lives in `src/lib/` as pure functions — this component wires them to
 * React Flow and paints.
 *
 * Built on @xyflow/react v12.
 */

import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  ReactFlowProvider,
  useReactFlow,
  type Node,
  type NodeTypes,
  type Edge,
  type FitViewOptions,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import type { GhostNodeData, TreeNodeCardData } from '../types/tree.ts';
import type { UseYjsTreeResult } from '../stores/useYjsTree.ts';
import { shouldUseSimplifiedMode } from '../layouts/d3Layout.ts';
import { shouldUseCanvas } from '../lib/canvasRenderer.ts';
import CanvasTreeView from './CanvasTreeView.tsx';
import { palette, nodeTypeColor } from '../theme.ts';
import {
  buildChildMap,
  childrenOf,
  hiddenCountFor,
  hiddenNodeIds,
  isCollapsible,
  pruneCollapsed,
  toggleCollapsed,
} from '../lib/treeCollapse.ts';
import { deriveReplyCounts, replyCountFor } from '../lib/replyCounts.ts';
import {
  canvasOwnsTab,
  ghostSlotPosition,
  isFrontierSlot,
} from '../lib/canvasGeometry.ts';
import {
  buildParentMap,
  drillInTarget,
  drillOutTarget,
  nextFocusIndex,
} from '../lib/shortcuts.ts';
import { useShortcuts } from '../hooks/useShortcuts.ts';

// ─── Custom nodes ─────────────────────────────────────────────────────

import { MessageNode } from './nodes/MessageNode.tsx';
import { SynthesisNode } from './nodes/SynthesisNode.tsx';
import { CardNode } from './nodes/CardNode.tsx';
import { TopicNode } from './nodes/TopicNode.tsx';
import { GhostNode } from './nodes/GhostNode.tsx';
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
  ghostNode: GhostNode,
};

const edgeTypes = {
  replyEdge: ReplyEdge,
  forkEdge: ForkEdge,
  synthesisEdge: SynthesisEdge,
};

/** Prefix marking a synthetic ghost slot node — never a real graph id. */
const GHOST_PREFIX = 'ghost:';

/** Where a ghost slot sits relative to its parent (left→right canvas). */
const GHOST_OFFSET = { dx: 260, dy: 0 };

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
  /**
   * Invoked when the user activates a ghost "add reply" slot. The parent
   * node id is passed through; the page decides how to create the reply
   * (focus the composer, POST /trees/{id}/nodes, …). When omitted, no
   * ghost slots are rendered — an affordance that does nothing is worse
   * than none at all.
   */
  onCreateReply?: (parentId: string) => void;
  /** Real author display names, when the page can resolve them. */
  authorNames?: ReadonlyMap<string, string>;
}

// ─── Main Component ───────────────────────────────────────────────────

function TreeCanvasInner({
  tree,
  onSelectionChange,
  focusNodeId,
  nodesDraggable: nodesDraggableOverride,
  collaborativeCursors,
  onCanvasMouseMove,
  onCreateReply,
  authorNames,
}: TreeCanvasProps) {
  const { nodes: allNodes, edges: allEdges, treeTitle, isReady } = tree;
  const reactFlowInstance = useReactFlow();

  // Collapse state — a set of node ids whose subtree is hidden.
  const [collapsedNodes, setCollapsedNodes] = useState<Set<string>>(new Set());

  /**
   * The node wearing the neon active glow.
   *
   * This is tracked here rather than read off React Flow's own `selected`
   * flag: the canvas re-derives its `nodes` array on every render (Yjs
   * snapshot → layout → enrichment), which replaces the objects React
   * Flow mutates, so its internal selection never survives a frame. Owning
   * the id explicitly is what makes the glow actually follow the user.
   */
  const [activeNodeId, setActiveNodeId] = useState<string | null>(null);

  // Parent→children adjacency, the basis of every derivation below.
  const childMap = useMemo(() => buildChildMap(allEdges), [allEdges]);

  // Reply counts straight from the graph — never hardcoded.
  const replyCounts = useMemo(
    () => deriveReplyCounts(allEdges, allNodes.map((n) => n.id)),
    [allEdges, allNodes],
  );

  /*
   * Drop collapse entries for nodes that have disappeared.
   *
   * The Yjs snapshot is rebuilt on every change, so this runs constantly;
   * bailing out when nothing was pruned keeps the state identity stable
   * and avoids an update loop.
   */
  useEffect(() => {
    setCollapsedNodes((prev) => {
      if (prev.size === 0) return prev;
      const pruned = pruneCollapsed(prev, allNodes.map((n) => n.id));
      return pruned.size === prev.size ? prev : pruned;
    });
  }, [allNodes]);

  // Nodes hidden because an ancestor is collapsed
  const hiddenNodes = useMemo(
    () => hiddenNodeIds(childMap, collapsedNodes),
    [childMap, collapsedNodes],
  );

  // ─── Handlers ────────────────────────────────────────────────────

  /** Toggle collapse/expand for a node's subtree */
  const toggleCollapse = useCallback((nodeId: string) => {
    setCollapsedNodes((prev) => toggleCollapsed(prev, nodeId));
  }, []);

  // ─── Visible graph ───────────────────────────────────────────────

  /**
   * Visible cards, enriched with the chrome each one needs: reply count,
   * collapse state, hidden-subtree size and a bound toggle. The toggle is
   * only attached to nodes that actually have children, so a leaf renders
   * no chevron.
   */
  const visibleNodes = useMemo(() => {
    const enriched: Node<TreeNodeCardData>[] = [];

    for (const node of allNodes) {
      if (hiddenNodes.has(node.id)) continue;

      const collapsible = isCollapsible(childMap, node.id);
      const collapsed = collapsedNodes.has(node.id);

      enriched.push({
        ...node,
        selected: node.id === activeNodeId,
        data: {
          ...node.data,
          replyCount: replyCountFor(replyCounts, node.id),
          collapsed,
          hiddenCount: collapsed ? hiddenCountFor(childMap, node.id) : 0,
          ...(collapsible
            ? { onToggleCollapse: () => toggleCollapse(node.id) }
            : {}),
          ...(authorNames ? { authorNames } : {}),
        },
      });
    }

    return enriched;
  }, [
    allNodes,
    hiddenNodes,
    childMap,
    collapsedNodes,
    replyCounts,
    toggleCollapse,
    authorNames,
    activeNodeId,
  ]);

  const visibleNodeIds = useMemo(
    () => new Set(visibleNodes.map((n) => n.id)),
    [visibleNodes],
  );

  /**
   * Ghost "add a reply here" slots at the frontier of the tree.
   *
   * The dashed marker is part of the graph's visual language (it shows
   * where the tree can still grow), so it renders for everyone — but it
   * only becomes clickable when the page supplied a handler, i.e. when
   * the current user may actually write. Suppressed on large trees, where
   * the simplified renderer is already fighting for pixels.
   */
  const ghostNodes = useMemo(() => {
    if (shouldUseSimplifiedMode(allNodes.length)) return [];

    const ghosts: Node<GhostNodeData>[] = [];
    for (const node of visibleNodes) {
      const eligible = isFrontierSlot({
        childCount: replyCountFor(replyCounts, node.id),
        collapsed: collapsedNodes.has(node.id),
      });
      if (!eligible) continue;

      ghosts.push({
        id: `${GHOST_PREFIX}${node.id}`,
        type: 'ghostNode',
        position: ghostSlotPosition(node.position, GHOST_OFFSET),
        draggable: false,
        selectable: false,
        data: {
          parentId: node.id,
          ...(onCreateReply ? { onCreate: onCreateReply } : {}),
        },
      });
    }
    return ghosts;
  }, [onCreateReply, allNodes.length, visibleNodes, replyCounts, collapsedNodes]);

  /** Ghost slots are linked to their parent by a faint reply connector. */
  const ghostEdges = useMemo(
    () =>
      ghostNodes.map<Edge>((ghost) => ({
        id: `${ghost.id}:edge`,
        source: (ghost.data as GhostNodeData).parentId,
        target: ghost.id,
        type: 'replyEdge',
        selectable: false,
        focusable: false,
        data: { dimmed: true, hideJoint: true },
      })),
    [ghostNodes],
  );

  const canvasNodes = useMemo(
    () => [...visibleNodes, ...ghostNodes] as Node<TreeNodeCardData>[],
    [visibleNodes, ghostNodes],
  );

  /**
   * Visible connectors. An edge whose source is collapsed is kept only
   * when its target is still on screen; the `dimmed` flag lets the
   * connector fade for a branch that leads into hidden content.
   */
  const visibleEdges = useMemo(() => {
    const real = allEdges
      .filter((e) => visibleNodeIds.has(e.source) && visibleNodeIds.has(e.target))
      .map((edge) => ({
        ...edge,
        data: {
          ...(edge.data ?? {}),
          dimmed: collapsedNodes.has(edge.target),
        },
      }));
    return [...real, ...ghostEdges];
  }, [allEdges, visibleNodeIds, collapsedNodes, ghostEdges]);

  // Large tree detection
  const totalCount = allNodes.length;
  const isLargeTree = shouldUseSimplifiedMode(totalCount);

  // Determine draggable state
  const isDraggable = nodesDraggableOverride ?? !isLargeTree;

  /** Handle node click: select node, toggle collapse on double-click */
  const onNodeClick = useCallback(
    (_event: React.MouseEvent, node: Node) => {
      // Ghost slots are pure affordances — they never become the selection
      if (node.id.startsWith(GHOST_PREFIX)) return;

      // Single click selects — drive the glow and notify the page
      setActiveNodeId(node.id);
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
    setActiveNodeId(null);
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
        // Tab cycles through visible nodes — but only when the canvas
        // itself holds the keyboard. This handler is bound to `window`,
        // so without the guard it swallows Tab for every control on the
        // page (the composer's @ / # / emoji / Send buttons become
        // unreachable by keyboard, failing WCAG 2.1.1).
        const active = document.activeElement;
        if (
          !canvasOwnsTab(
            active
              ? {
                  tagName: active.tagName,
                  isContentEditable:
                    active instanceof HTMLElement && active.isContentEditable,
                  tabIndexAttr: active.getAttribute('tabindex'),
                }
              : null,
          )
        ) {
          return;
        }
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
        setActiveNodeId(nextId ?? null);
        onSelectionChange?.(nextId);
        focusRef.current(nextId);
      } else if (e.key === 'Enter' && focusedNodeId) {
        // Enter toggles collapse on focused node
        e.preventDefault();
        toggleCollapse(focusedNodeId);
      } else if (e.key === 'Escape') {
        // Escape deselects
        setFocusedNodeId(null);
        setActiveNodeId(null);
        onSelectionChange?.(null);
      } else if (e.key === 'Home' && visibleNodes.length > 0) {
        // Home jumps to root node
        e.preventDefault();
        const rootId = visibleNodes[0]?.id;
        if (rootId) {
          setFocusedNodeId(rootId);
          setActiveNodeId(rootId);
          onSelectionChange?.(rootId);
          focusRef.current(rootId);
        }
      }
    }

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [reactFlowInstance, visibleNodes, focusedNodeId, toggleCollapse, onSelectionChange]);

  // ─── Vim-style navigation (UI-07: j/k walk, h/l drill) ───────────

  /** child → first parent, for `h`'s step-up out of a branch. */
  const parentMap = useMemo(() => buildParentMap(allEdges), [allEdges]);

  /**
   * Move the keyboard cursor to a node: focus ring, selection glow, page
   * notification and a camera pan — the same four effects Tab produces,
   * so j/k/h/l feel identical to the existing cycling.
   */
  const moveFocusTo = useCallback(
    (nodeId: string) => {
      setFocusedNodeId(nodeId);
      setActiveNodeId(nodeId);
      onSelectionChange?.(nodeId);
      focusRef.current(nodeId);
    },
    [onSelectionChange],
  );

  /** j / k — step through the visible nodes in layout order, wrapping. */
  const stepFocus = useCallback(
    (direction: 1 | -1) => {
      const ids = visibleNodes.map((n) => n.id);
      const current = focusedNodeId ? ids.indexOf(focusedNodeId) : -1;
      const next = nextFocusIndex(current, ids.length, direction);
      const nextId = next >= 0 ? ids[next] : undefined;
      if (nextId) moveFocusTo(nextId);
    },
    [visibleNodes, focusedNodeId, moveFocusTo],
  );

  /**
   * h / l — drill out / in.
   *
   * The decision (collapse vs step to parent, expand vs step to child) is
   * `drillOutTarget` / `drillInTarget` in lib/shortcuts.ts; this only
   * applies the outcome. Hidden nodes are never a focus target, so the
   * step-up is checked against the visible set.
   */
  const drill = useCallback(
    (direction: 'in' | 'out') => {
      if (!focusedNodeId) return;

      const outcome =
        direction === 'out'
          ? drillOutTarget({
              nodeId: focusedNodeId,
              collapsible: isCollapsible(childMap, focusedNodeId),
              collapsed: collapsedNodes.has(focusedNodeId),
              parentId: parentMap.get(focusedNodeId) ?? null,
            })
          : drillInTarget({
              nodeId: focusedNodeId,
              collapsed: collapsedNodes.has(focusedNodeId),
              firstChildId: childrenOf(childMap, focusedNodeId)[0] ?? null,
            });

      switch (outcome.kind) {
        case 'collapse':
        case 'expand':
          if (outcome.nodeId) toggleCollapse(outcome.nodeId);
          break;
        case 'focus':
          if (outcome.nodeId && visibleNodeIds.has(outcome.nodeId)) {
            moveFocusTo(outcome.nodeId);
          }
          break;
        case 'none':
          break;
      }
    },
    [
      focusedNodeId,
      childMap,
      collapsedNodes,
      parentMap,
      toggleCollapse,
      moveFocusTo,
      visibleNodeIds,
    ],
  );

  // Tree-scoped bindings only — `?` and `m` are owned by the app shell.
  useShortcuts({
    navigateNext: () => stepFocus(1),
    navigatePrev: () => stepFocus(-1),
    drillOut: () => drill('out'),
    drillIn: () => drill('in'),
  });

  // ─── Focus on node when focusNodeId prop changes ─────────────────

  // Ref to track the latest focusOnNode (avoid stale closure)
  const focusFnRef = useRef(focusOnNode);
  focusFnRef.current = focusOnNode;

  // Track previous focusNodeId to avoid re-focusing on same node
  const prevFocusRef = useRef<string | null | undefined>(undefined);

  useEffect(() => {
    if (focusNodeId && focusNodeId !== prevFocusRef.current) {
      prevFocusRef.current = focusNodeId;
      // Selection driven from outside (search, breadcrumbs, ghost slot)
      // must light the same glow as an in-canvas click.
      setActiveNodeId(focusNodeId);
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
        nodes={canvasNodes}
        edges={visibleEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        fitView
        fitViewOptions={{ padding: 0.3, duration: 300 }}
        minZoom={isLargeTree ? 0.05 : 0.1}
        maxZoom={2}
        defaultEdgeOptions={{ type: 'replyEdge' }}
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
 *
 * STACK-03: trees beyond CANVAS_THRESHOLD nodes bypass React Flow
 * entirely — CanvasTreeView paints them on a Canvas 2D surface (see
 * lib/canvasRenderer.ts). Below the threshold this wrapper is the exact
 * React Flow path it has always been; the threshold check runs before
 * the provider mounts, so nothing about that path changes.
 */
export default function TreeCanvas(props: TreeCanvasProps) {
  if (shouldUseCanvas(props.tree.nodes.length)) {
    return <CanvasTreeView {...props} />;
  }
  return (
    <ReactFlowProvider>
      <TreeCanvasInner {...props} />
    </ReactFlowProvider>
  );
}
