/**
 * Hermes Canopy — useYjsTree React Hook
 *
 * Bridges Yjs CRDT document observation to React state.
 * Returns node and edge arrays suitable for React Flow's useNodesState/useEdgesState.
 * Re-renders only when observed Yjs data actually changes.
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import type { Node, Edge } from '@xyflow/react';
import type { TreeYDoc } from './treeStore.ts';
import type { TreeNodeCardData } from '../types/tree.ts';
import {
  getAllNodeIds,
  getNode,
  getChildIds,
  getParentId,
} from './treeStore.ts';

// ─── Layout types ─────────────────────────────────────────────────────

export interface LayoutNode {
  id: string;
  width: number;
  height: number;
  x: number;
  y: number;
}

// ─── Snapshot construction ────────────────────────────────────────────

interface TreeSnapshot {
  nodes: Node<TreeNodeCardData>[];
  edges: Edge[];
}

function buildSnapshot(doc: TreeYDoc): TreeSnapshot {
  const nodeIds = getAllNodeIds(doc);

  const rfNodes: Node<TreeNodeCardData>[] = [];
  const rfEdges: Edge[] = [];

  // Build position map via simple depth-first layout
  const positions = computeTreeLayout(doc, nodeIds);

  for (const nodeId of nodeIds) {
    const nodeData = getNode(doc, nodeId);
    if (!nodeData) continue;

    const pos = positions.get(nodeId) ?? { x: 0, y: 0 };
    const isRoot = getParentId(doc, nodeId) === undefined;

    rfNodes.push({
      id: nodeId,
      type: 'treeNodeCard',
      position: { x: pos.x, y: pos.y },
      data: {
        label: nodeData.content.slice(0, 80) || 'Untitled',
        nodeType: nodeData.nodeType as TreeNodeCardData['nodeType'],
        content: nodeData.content,
        authorId: nodeData.authorId,
        createdAt: nodeData.createdAt,
        isAgent: nodeData.authorId !== 'local' && nodeData.nodeType === 'message',
        isSystem: nodeData.nodeType === 'system',
      },
      // Style root nodes differently
      style: isRoot
        ? { border: '2px solid #7c3aed', borderRadius: '8px' }
        : undefined,
    });
  }

  // Build edges from Yjs edges map
  for (const [, edgeMap] of doc.edges.entries()) {
    const sourceId = edgeMap.get('sourceId') as string;
    const targetId = edgeMap.get('targetId') as string;
    const edgeType = edgeMap.get('edgeType') as string;
    const edgeId = edgeMap.get('id') as string;

    if (!sourceId || !targetId) continue;

    rfEdges.push({
      id: edgeId,
      source: sourceId,
      target: targetId,
      type: edgeType === 'synthesis' ? 'synthesisEdge' : 'defaultEdge',
      animated: edgeType === 'synthesis',
      style:
        edgeType === 'synthesis'
          ? { stroke: '#f59e0b', strokeWidth: 2, strokeDasharray: '6,3' }
          : edgeType === 'fork'
            ? { stroke: '#8b5cf6', strokeWidth: 2 }
            : { stroke: '#6b7280', strokeWidth: 1.5 },
    });
  }

  return { nodes: rfNodes, edges: rfEdges };
}

// ─── Simple tree layout (depth-first, top-down) ───────────────────────

function computeTreeLayout(
  doc: TreeYDoc,
  nodeIds: string[],
): Map<string, { x: number; y: number }> {
  const positions = new Map<string, { x: number; y: number }>();
  const visited = new Set<string>();

  const H_SPACING = 220;
  const V_SPACING = 120;

  // Find roots
  const roots = nodeIds.filter((id) => getParentId(doc, id) === undefined);

  if (roots.length === 0 && nodeIds.length > 0) {
    // If no detected roots (all nodes have parents), use the first node
    roots.push(nodeIds[0]);
  }

  let globalX = 0;

  function layoutSubtree(nodeId: string, depth: number): number {
    if (visited.has(nodeId)) return 0;
    visited.add(nodeId);

    const children = getChildIds(doc, nodeId);
    let subtreeWidth = 0;

    // Layout children first to determine width
    if (children.length === 0) {
      subtreeWidth = H_SPACING;
    } else {
      for (const childId of children) {
        subtreeWidth += layoutSubtree(childId, depth + 1);
      }
    }

    const x = globalX + subtreeWidth / 2 - H_SPACING / 2;
    positions.set(nodeId, { x, y: depth * V_SPACING });
    globalX += subtreeWidth;
    return subtreeWidth;
  }

  for (const rootId of roots) {
    layoutSubtree(rootId, 0);
  }

  return positions;
}

// ─── React Hook ───────────────────────────────────────────────────────

export interface UseYjsTreeResult {
  nodes: Node<TreeNodeCardData>[];
  edges: Edge[];
  treeTitle: string;
  isReady: boolean;
  refresh: () => void;
}

export function useYjsTree(doc: TreeYDoc | null): UseYjsTreeResult {
  const docRef = useRef<TreeYDoc | null>(doc);
  docRef.current = doc;

  const [snapshot, setSnapshot] = useState<TreeSnapshot>({ nodes: [], edges: [] });
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
      setSnapshot({ nodes: [], edges: [] });
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
    refresh,
  };
}
