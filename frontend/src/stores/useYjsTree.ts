/**
 * Hermes Canopy — useYjsTree React Hook
 *
 * Bridges Yjs CRDT document observation to React state.
 * Uses d3-hierarchy for advanced tree layout.
 * Returns node and edge arrays suitable for React Flow.
 * Re-renders only when observed Yjs data actually changes.
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import { MarkerType, type Node, type Edge } from '@xyflow/react';
import type { TreeYDoc } from './treeStore.ts';
import type { TreeNodeCardData } from '../types/tree.ts';
import { nodeTypeToFlowType } from '../types/tree.ts';
import { isAgentCardMetadata } from '../types/agent.ts';
import {
  getAllNodeIds,
  getNode,
  getChildIds,
  getParentId,
  getNodeType,
} from './treeStore.ts';
import {
  computeD3Layout,
  getFlowEdgeType,
  getEdgeStyle,
  type LayoutOutput,
} from '../layouts/d3Layout.ts';

// ─── Snapshot construction ────────────────────────────────────────────

export interface TreeSnapshot {
  nodes: Node<TreeNodeCardData>[];
  edges: Edge[];
  /** Node IDs that are the target of multiple edges (synthesis targets) */
  multiParentNodes: Set<string>;
}

/**
 * Build a React Flow node/edge snapshot from the Yjs document.
 * Uses d3-hierarchy for layout.
 */
function buildSnapshot(doc: TreeYDoc): TreeSnapshot {
  const nodeIds = getAllNodeIds(doc);

  if (nodeIds.length === 0) {
    return { nodes: [], edges: [], multiParentNodes: new Set() };
  }

  // Compute d3-hierarchy layout
  const layout: LayoutOutput = computeD3Layout({
    nodeIds,
    getChildren: (id) => getChildIds(doc, id),
    getParent: (id) => getParentId(doc, id),
    getNodeType: (id) => getNodeType(doc, id),
  });

  // Build child counts per node (for collapse UI)
  const childCounts = new Map<string, number>();
  for (const nodeId of nodeIds) {
    childCounts.set(nodeId, getChildIds(doc, nodeId).length);
  }

  // Build React Flow nodes
  const rfNodes: Node<TreeNodeCardData>[] = [];
  for (const nodeId of nodeIds) {
    const nodeData = getNode(doc, nodeId);
    if (!nodeData) continue;

    const pos = layout.positions.get(nodeId) ?? { x: 0, y: 0 };
    const childCount = childCounts.get(nodeId) ?? 0;
    const nodeType = nodeData.nodeType;

    // Determine cardType from metadata if this is a card node
    let cardType: 'file' | 'task' | 'code' | undefined;
    let isAgentCard = false;
    if (nodeType === 'card' && nodeData.metadata) {
      const mt = nodeData.metadata.cardType as string | undefined;
      if (mt === 'file' || mt === 'task' || mt === 'code') {
        cardType = mt;
      }
      // Check if this is an agent iteration card
      if (isAgentCardMetadata(nodeData.metadata)) {
        isAgentCard = true;
      }
    }

    const flowType = isAgentCard ? 'agentCardNode' : nodeTypeToFlowType(nodeType);

    rfNodes.push({
      id: nodeId,
      type: flowType,
      position: { x: pos.x, y: pos.y },
      data: {
        label: nodeData.content.slice(0, 80) || 'Untitled',
        nodeType: nodeType as TreeNodeCardData['nodeType'],
        content: nodeData.content,
        authorId: nodeData.authorId,
        createdAt: nodeData.createdAt,
        isAgent: nodeData.authorId !== 'local' && nodeType === 'message',
        isSystem: nodeType === 'system',
        metadata: nodeData.metadata ?? {},
        childCount,
        collapsed: false,
        cardType,
        isAgentCard,
      },
    });
  }

  // Build React Flow edges from Yjs edges map
  const rfEdges: Edge[] = [];
  for (const [, edgeMap] of doc.edges.entries()) {
    const sourceId = edgeMap.get('sourceId') as string;
    const targetId = edgeMap.get('targetId') as string;
    const edgeType = (edgeMap.get('edgeType') as string) ?? 'reply';
    const edgeId = edgeMap.get('id') as string;

    if (!sourceId || !targetId) continue;

    // Skip edges where source or target doesn't exist in our node set
    if (!nodeIds.includes(sourceId) || !nodeIds.includes(targetId)) continue;

    const isMultiParent = layout.multiParentNodes.has(targetId);
    const flowEdgeType = getFlowEdgeType(edgeType, isMultiParent);
    const style = getEdgeStyle(edgeType);

    rfEdges.push({
      id: edgeId,
      source: sourceId,
      target: targetId,
      type: flowEdgeType,
      animated: style.animated,
      style: {
        stroke: style.stroke,
        strokeWidth: style.strokeWidth,
        ...(style.strokeDasharray ? { strokeDasharray: style.strokeDasharray } : {}),
      },
      markerEnd: {
        type: MarkerType.ArrowClosed,
        width: 16,
        height: 16,
        color: style.markerColor,
      },
    });
  }

  return { nodes: rfNodes, edges: rfEdges, multiParentNodes: layout.multiParentNodes };
}

// ─── React Hook ───────────────────────────────────────────────────────

export interface UseYjsTreeResult {
  nodes: Node<TreeNodeCardData>[];
  edges: Edge[];
  treeTitle: string;
  isReady: boolean;
  multiParentNodes: Set<string>;
  refresh: () => void;
}

export function useYjsTree(doc: TreeYDoc | null): UseYjsTreeResult {
  const docRef = useRef<TreeYDoc | null>(doc);
  docRef.current = doc;

  const [snapshot, setSnapshot] = useState<TreeSnapshot>({
    nodes: [],
    edges: [],
    multiParentNodes: new Set(),
  });
  const [treeTitle, setTreeTitle] = useState('');
  const [isReady, setIsReady] = useState(false);
  const [version, setVersion] = useState(0);

  const refresh = useCallback(() => {
    setVersion((v) => v + 1);
  }, []);

  // Rebuild snapshot when doc changes or version increments
  useEffect(() => {
    const current = docRef.current;
    if (!current) {
      setSnapshot({ nodes: [], edges: [], multiParentNodes: new Set() });
      setTreeTitle('');
      setIsReady(false);
      return;
    }

    const snap = buildSnapshot(current);
    setSnapshot(snap);
    setTreeTitle((current.meta.get('title') as string) ?? 'Untitled Tree');
    setIsReady(true);

    // Observe Yjs changes
    const observer = (): void => {
      refresh();
    };

    current.nodes.observe(observer);
    current.edges.observe(observer);
    current.rootOrder.observe(observer);
    current.meta.observe(observer);

    return () => {
      current.nodes.unobserve(observer);
      current.edges.unobserve(observer);
      current.rootOrder.unobserve(observer);
      current.meta.unobserve(observer);
    };
  }, [doc, version, refresh]);

  return {
    nodes: snapshot.nodes,
    edges: snapshot.edges,
    treeTitle,
    isReady,
    multiParentNodes: snapshot.multiParentNodes,
    refresh,
  };
}
