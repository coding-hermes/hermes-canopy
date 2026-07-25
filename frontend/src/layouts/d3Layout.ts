/**
 * Hermes Canopy — d3-hierarchy Tree Layout Engine
 *
 * Converts the Yjs DAG (nodes + edges) into positioned React Flow nodes
 * using d3-hierarchy's Reingold-Tilford "tidy" tree layout algorithm.
 *
 * Handles multi-parent synthesis nodes by attaching them to their first
 * discovered parent during tree construction. Synthesis edges are rendered
 * as additional connections on top of the primary tree layout.
 */

import { tree as d3Tree, hierarchy, type HierarchyNode } from 'd3-hierarchy';

// ─── Layout types ─────────────────────────────────────────────────────

export interface LayoutInput {
  nodeIds: string[];
  /** Get child IDs for a given parent */
  getChildren: (nodeId: string) => string[];
  /** Get parent ID for a given node (returns undefined for roots) */
  getParent: (nodeId: string) => string | undefined;
  /** Get node type for a given node */
  getNodeType: (nodeId: string) => string | undefined;
}

export interface LayoutOutput {
  positions: Map<string, { x: number; y: number }>;
  /** Nodes that have multiple parents (synthesis targets) */
  multiParentNodes: Set<string>;
}

// ─── Node size defaults by type ───────────────────────────────────────

const NODE_SIZES: Record<string, { width: number; height: number }> = {
  message: { width: 220, height: 120 },
  synthesis: { width: 240, height: 100 },
  card: { width: 220, height: 140 },
  topic: { width: 200, height: 90 },
  system: { width: 220, height: 100 },
};

const DEFAULT_NODE_SIZE = { width: 220, height: 100 };

const H_SPACING = 40; // horizontal gap between siblings
const V_SPACING = 60; // vertical gap between levels
const MAX_LEVELS_WARN = 200; // warn if tree is deeper than this

// ─── D3 hierarchy node shape ──────────────────────────────────────────

interface HierarchyDatum {
  id: string;
  width: number;
  height: number;
}

// ─── Implementation ───────────────────────────────────────────────────

/**
 * Build a d3-hierarchy from the DAG.
 *
 * Because d3-hierarchy requires a strict tree (each node has a single parent),
 * we attach multi-parent nodes to their first discovered parent and track
 * the additional connections separately.
 */
export function computeD3Layout(input: LayoutInput): LayoutOutput {
  const { nodeIds, getChildren, getParent, getNodeType } = input;

  // Phase 1: Detect roots and multi-parent nodes
  const roots: string[] = [];
  const multiParentNodes = new Set<string>();
  const inboundCount = new Map<string, number>();

  for (const nodeId of nodeIds) {
    const parent = getParent(nodeId);
    if (!parent) {
      roots.push(nodeId);
    }
    // Count inbound edges for multi-parent detection
    const count = inboundCount.get(nodeId) ?? 0;
    inboundCount.set(nodeId, count + 1);
  }

  // Also detect multi-parent via edges
  const childToParentEdge = new Map<string, string>();
  for (const nodeId of nodeIds) {
    const children = getChildren(nodeId);
    for (const childId of children) {
      const existing = childToParentEdge.get(childId);
      if (existing !== undefined && existing !== nodeId) {
        multiParentNodes.add(childId);
      } else {
        childToParentEdge.set(childId, nodeId);
      }
    }
  }

  // If no roots found but we have nodes, pick the node with no inbound edges
  // or the first node as fallback
  if (roots.length === 0 && nodeIds.length > 0) {
    const candidate = nodeIds.find((id) => (inboundCount.get(id) ?? 0) === 0);
    if (candidate) {
      roots.push(candidate);
    } else {
      roots.push(nodeIds[0]);
    }
  }

  // Phase 2: Build hierarchy data recursively
  const visited = new Set<string>();

  function buildHierarchyData(nodeId: string): HierarchyDatum {
    const nodeType = getNodeType(nodeId) ?? 'message';
    const size = NODE_SIZES[nodeType] ?? DEFAULT_NODE_SIZE;
    return { id: nodeId, width: size.width, height: size.height };
  }

  function buildHierarchyChildren(datum: HierarchyDatum): HierarchyDatum[] {
    const nodeId = datum.id;
    if (visited.has(nodeId)) return [];
    visited.add(nodeId);

    const children = getChildren(nodeId);
    return children
      .filter((childId) => {
        // If this child already has a primary parent, skip it (it's a multi-parent leaf)
        const primaryParent = childToParentEdge.get(childId);
        if (primaryParent !== undefined && primaryParent !== nodeId) {
          return false;
        }
        return true;
      })
      .map((childId) => buildHierarchyData(childId));
  }

  // Create a virtual root if there are multiple roots
  let rootData: HierarchyDatum;

  if (roots.length === 1) {
    rootData = buildHierarchyData(roots[0]);
  } else if (roots.length > 1) {
    // Virtual root to hold multiple roots as siblings
    rootData = { id: '__virtual_root__', width: 0, height: 0 };
    // We'll handle multi-root specially
  } else {
    return { positions: new Map(), multiParentNodes };
  }

  // Phase 3: Build hierarchy and run d3.tree layout
  const positions = new Map<string, { x: number; y: number }>();

  if (roots.length === 1) {
    const root = hierarchy<HierarchyDatum>(rootData, buildHierarchyChildren);
    const layout = d3Tree<HierarchyDatum>()
      .nodeSize([DEFAULT_NODE_SIZE.width + H_SPACING, DEFAULT_NODE_SIZE.height + V_SPACING])
      .separation((a, b) => {
        // Extra separation between unrelated branches
        return a.parent === b.parent ? 1 : 1.3;
      });

    const laidOut = layout(root);

    // Extract positions
    laidOut.each((node: HierarchyNode<HierarchyDatum>) => {
      if (node.data.id !== '__virtual_root__') {
        positions.set(node.data.id, {
          x: node.x ?? 0,
          y: node.y ?? 0,
        });
      }
    });

    // Check for deep trees
    if (laidOut.height > MAX_LEVELS_WARN) {
      console.warn(
        `[d3Layout] Tree depth ${laidOut.height} exceeds ${MAX_LEVELS_WARN}. ` +
          'Consider enabling large-tree mode.',
      );
    }
  } else {
    // Multiple roots: lay out each root as a separate tree, offsetting x
    let xOffset = 0;
    const ROOT_GAP = 100;

    for (const rootId of roots) {
      const data = buildHierarchyData(rootId);
      const root = hierarchy<HierarchyDatum>(data, buildHierarchyChildren);
      const layout = d3Tree<HierarchyDatum>()
        .nodeSize([DEFAULT_NODE_SIZE.width + H_SPACING, DEFAULT_NODE_SIZE.height + V_SPACING]);

      const laidOut = layout(root);

      laidOut.each((node: HierarchyNode<HierarchyDatum>) => {
        const pos = positions.get(node.data.id);
        if (pos) {
          // Already positioned via another root — skip
          return;
        }
        positions.set(node.data.id, {
          x: (node.x ?? 0) + xOffset,
          y: node.y ?? 0,
        });
      });

      // Calculate width of this tree to offset next root
      let minX = Infinity;
      let maxX = -Infinity;
      laidOut.each((node: HierarchyNode<HierarchyDatum>) => {
        minX = Math.min(minX, node.x ?? 0);
        maxX = Math.max(maxX, node.x ?? 0);
      });
      xOffset += (maxX - minX) + NODE_SIZES.message.width + ROOT_GAP;
    }
  }

  // Phase 4: Handle multi-parent nodes — position them near their parents
  for (const nodeId of multiParentNodes) {
    if (positions.has(nodeId)) continue; // already positioned

    // Find all parents
    const parents: string[] = [];
    for (const nid of nodeIds) {
      const children = getChildren(nid);
      if (children.includes(nodeId)) {
        parents.push(nid);
      }
    }

    // Position below the average of parents
    if (parents.length > 0) {
      let avgX = 0;
      let maxY = 0;
      let positioned = 0;
      for (const parentId of parents) {
        const parentPos = positions.get(parentId);
        if (parentPos) {
          avgX += parentPos.x;
          maxY = Math.max(maxY, parentPos.y);
          positioned++;
        }
      }
      if (positioned > 0) {
        const nodeType = getNodeType(nodeId) ?? 'message';
        const size = NODE_SIZES[nodeType] ?? DEFAULT_NODE_SIZE;
        positions.set(nodeId, {
          x: avgX / positioned,
          y: maxY + size.height + V_SPACING,
        });
      }
    }
  }

  return { positions, multiParentNodes };
}

// ─── Edge type mapping helpers ────────────────────────────────────────

/**
 * Determine the React Flow edge type string from an edge's data type
 * and whether the target is a multi-parent synthesis node.
 */
export function getFlowEdgeType(
  edgeType: string,
  isMultiParentTarget: boolean,
): string {
  if (edgeType === 'synthesis' || isMultiParentTarget) {
    return 'synthesisEdge';
  }
  if (edgeType === 'fork') {
    return 'forkEdge';
  }
  return 'replyEdge';
}

/**
 * Get edge style based on edge type.
 */
export interface EdgeStyle {
  stroke: string;
  strokeWidth: number;
  strokeDasharray?: string;
  animated: boolean;
  markerColor: string;
}

export function getEdgeStyle(edgeType: string): EdgeStyle {
  switch (edgeType) {
    case 'synthesis':
      return {
        stroke: '#f59e0b',
        strokeWidth: 2.5,
        strokeDasharray: '8,4',
        animated: true,
        markerColor: '#f59e0b',
      };
    case 'fork':
      return {
        stroke: '#8b5cf6',
        strokeWidth: 2,
        animated: false,
        markerColor: '#8b5cf6',
      };
    case 'reference':
      return {
        stroke: '#6b7280',
        strokeWidth: 1.5,
        strokeDasharray: '4,4',
        animated: false,
        markerColor: '#6b7280',
      };
    case 'reply':
    default:
      return {
        stroke: '#6b7280',
        strokeWidth: 1.5,
        animated: false,
        markerColor: '#6b7280',
      };
  }
}

// ─── Large tree detection ─────────────────────────────────────────────

export const LARGE_TREE_THRESHOLD = 500;

export function shouldUseSimplifiedMode(nodeCount: number): boolean {
  return nodeCount > LARGE_TREE_THRESHOLD;
}
