/**
 * Hermes Canopy — d3-hierarchy Tree Layout Engine
 *
 * Converts the Yjs DAG (nodes + edges) into positioned React Flow nodes
 * using d3-hierarchy's Reingold-Tilford "tidy" tree layout algorithm.
 *
 * Handles multi-parent synthesis nodes by attaching them to their first
 * discovered parent during tree construction. Synthesis edges are rendered
 * as additional connections on top of the primary tree layout.
 *
 * Orientation (UI-04): the canvas runs LEFT→RIGHT — depth advances along
 * x, siblings stack along y (docs/mockups/mockup-1.png). d3-hierarchy
 * always lays a tree out top-down (`x` = breadth, `y` = depth), so the two
 * axes are transposed on the way out via `place()`. The extents handed to
 * `nodeSize` are transposed for the same reason: d3 wants
 * `[breadthExtent, depthExtent]`, which for a horizontal tree is driven by
 * node HEIGHT then node WIDTH.
 */

import { tree as d3Tree, hierarchy, type HierarchyNode } from 'd3-hierarchy';
import { palette } from '../theme.ts';

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

const H_SPACING = 40; // gap between siblings (along the breadth axis)
const V_SPACING = 60; // gap between levels (along the depth axis)
const MAX_LEVELS_WARN = 200; // warn if tree is deeper than this

/**
 * Breadth/depth extents handed to `d3.tree().nodeSize()`.
 *
 * Breadth (d3 `x`) becomes screen y, so it is driven by node HEIGHT plus
 * the sibling gap. Depth (d3 `y`) becomes screen x, driven by node WIDTH
 * plus the level gap. Swapping these is what makes a horizontal tree
 * overlap vertically while leaving cavernous gaps between columns.
 */
const BREADTH_EXTENT = DEFAULT_NODE_SIZE.height + H_SPACING;
const DEPTH_EXTENT = DEFAULT_NODE_SIZE.width + V_SPACING;

/**
 * Transpose a d3 (breadth, depth) pair into canvas (x, y).
 * Depth runs rightwards; breadth runs downwards.
 */
function place(breadth: number, depth: number): { x: number; y: number } {
  return { x: depth, y: breadth };
}

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
      .nodeSize([BREADTH_EXTENT, DEPTH_EXTENT])
      .separation((a, b) => {
        // Extra separation between unrelated branches
        return a.parent === b.parent ? 1 : 1.3;
      });

    const laidOut = layout(root);

    // Extract positions (transposed — d3 x = breadth, y = depth)
    laidOut.each((node: HierarchyNode<HierarchyDatum>) => {
      if (node.data.id !== '__virtual_root__') {
        positions.set(node.data.id, place(node.x ?? 0, node.y ?? 0));
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
    // Multiple roots: lay out each root as a separate tree, stacking them
    // down the breadth axis (screen y) so every root starts at x = 0 and
    // the forest reads as parallel horizontal trees.
    let breadthOffset = 0;
    const ROOT_GAP = 100;

    for (const rootId of roots) {
      const data = buildHierarchyData(rootId);
      const root = hierarchy<HierarchyDatum>(data, buildHierarchyChildren);
      const layout = d3Tree<HierarchyDatum>().nodeSize([
        BREADTH_EXTENT,
        DEPTH_EXTENT,
      ]);

      const laidOut = layout(root);

      laidOut.each((node: HierarchyNode<HierarchyDatum>) => {
        const pos = positions.get(node.data.id);
        if (pos) {
          // Already positioned via another root — skip
          return;
        }
        positions.set(
          node.data.id,
          place((node.x ?? 0) + breadthOffset, node.y ?? 0),
        );
      });

      // Measure this tree's breadth so the next root clears it
      let minBreadth = Infinity;
      let maxBreadth = -Infinity;
      laidOut.each((node: HierarchyNode<HierarchyDatum>) => {
        minBreadth = Math.min(minBreadth, node.x ?? 0);
        maxBreadth = Math.max(maxBreadth, node.x ?? 0);
      });
      breadthOffset +=
        maxBreadth - minBreadth + DEFAULT_NODE_SIZE.height + ROOT_GAP;
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

    // Position to the RIGHT of the deepest parent, centred on their breadth
    if (parents.length > 0) {
      let avgY = 0;
      let maxX = 0;
      let positioned = 0;
      for (const parentId of parents) {
        const parentPos = positions.get(parentId);
        if (parentPos) {
          avgY += parentPos.y;
          maxX = Math.max(maxX, parentPos.x);
          positioned++;
        }
      }
      if (positioned > 0) {
        const nodeType = getNodeType(nodeId) ?? 'message';
        const size = NODE_SIZES[nodeType] ?? DEFAULT_NODE_SIZE;
        positions.set(nodeId, {
          x: maxX + size.width + V_SPACING,
          y: avgY / positioned,
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

/**
 * Get edge style based on edge type.
 *
 * Colours come from the token palette (theme.ts) — the canvas is the one
 * place that needs raw hex (SVG stroke attributes sit outside the CSS
 * cascade), so it reads the mirror rather than inventing values.
 */
export function getEdgeStyle(edgeType: string): EdgeStyle {
  switch (edgeType) {
    case 'synthesis':
      return {
        stroke: palette.warning,
        strokeWidth: 2.5,
        strokeDasharray: '7,5',
        animated: true,
        markerColor: palette.warning,
      };
    case 'fork':
      return {
        stroke: palette.accent3,
        strokeWidth: 2,
        animated: false,
        markerColor: palette.accent3,
      };
    case 'reference':
      return {
        stroke: palette.contentFaint,
        strokeWidth: 1.5,
        strokeDasharray: '4,4',
        animated: false,
        markerColor: palette.contentFaint,
      };
    case 'reply':
    default:
      return {
        stroke: palette.accent,
        strokeWidth: 1.6,
        animated: false,
        markerColor: palette.accent,
      };
  }
}

// ─── Large tree detection ─────────────────────────────────────────────

export const LARGE_TREE_THRESHOLD = 500;

export function shouldUseSimplifiedMode(nodeCount: number): boolean {
  return nodeCount > LARGE_TREE_THRESHOLD;
}
