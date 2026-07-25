/**
 * Hermes Canopy — Tree Data Model Types
 *
 * Aligned with SPEC-DM-01 §5 (TypeScript Types) and backend Go structs.
 * Uses `import type` for verbatimModuleSyntax compliance.
 */

// ─── Enums ────────────────────────────────────────────────────────────

export type ContentFormat = 'markdown' | 'plain' | 'rich';

export type NodeType =
  | 'message'
  | 'synthesis'
  | 'system'
  | 'card'
  | 'topic';

export type EdgeType = 'reply' | 'fork' | 'synthesis' | 'reference';

// ─── Core Entities ────────────────────────────────────────────────────

/** A single node (message) in a conversation tree. Matches backend `Node` struct. */
export interface TreeNode {
  id: string; // UUIDv7
  treeId: string;
  parentId: string | null; // null for root nodes
  authorId: string;
  content: string;
  contentFormat: ContentFormat;
  nodeType: NodeType;
  sequenceNum: number;
  metadata: Record<string, unknown>;
  createdAt: string; // ISO 8601
  editedAt: string | null;
  deletedAt: string | null;
}

/** A typed directed edge between two nodes. Matches backend `Edge` struct. */
export interface TreeEdge {
  id: string;
  treeId: string;
  sourceId: string;
  targetId: string;
  edgeType: EdgeType;
  sequenceNum: number;
  metadata: Record<string, unknown>;
  createdAt: string;
  deletedAt: string | null;
}

/** A conversation tree container. Matches backend `Tree` struct. */
export interface TreeMetadata {
  id: string;
  ownerId: string;
  title: string;
  description: string;
  rootNodeId: string | null;
  metadata: Record<string, unknown>;
  createdAt: string;
  editedAt: string | null;
  deletedAt: string | null;
}

// ─── Yjs CRDT Data Shapes ─────────────────────────────────────────────

/**
 * Data stored in Y.Map for each node.
 * Slimmer than TreeNode — excludes server-authoritative fields
 * (treeId, sequenceNum, deletedAt) per SPEC-DM-01 §6.3.
 */
export interface NodeData {
  id: string;
  content: string;
  contentFormat: string;
  nodeType: string;
  authorId: string;
  metadata: Record<string, unknown>;
  createdAt: string;
  editedAt: string | null;
}

/**
 * Data stored in Y.Map for each edge.
 * Slimmer than TreeEdge — excludes server-authoritative fields.
 */
export interface EdgeData {
  id: string;
  sourceId: string;
  targetId: string;
  edgeType: string;
  metadata: Record<string, unknown>;
  createdAt: string;
}

// ─── Payload Types ────────────────────────────────────────────────────

export interface CreateNodePayload {
  parentId: string | null;
  content: string;
  contentFormat?: ContentFormat;
  nodeType?: NodeType;
  edgeType?: EdgeType;
  metadata?: Record<string, unknown>;
}

export interface CreateEdgePayload {
  sourceId: string;
  targetId: string;
  edgeType?: EdgeType;
  metadata?: Record<string, unknown>;
}

export interface CreateTreePayload {
  title: string;
  description?: string;
  ownerId?: string;
  metadata?: Record<string, unknown>;
}

// ─── React Flow Node/Edge Types ───────────────────────────────────────

/** React Flow node type identifiers for custom rendering. */
export type FlowNodeType =
  | 'messageNode'
  | 'synthesisNode'
  | 'cardNode'
  | 'topicNode'
  | 'agentCardNode';

/** Extended React Flow node data carried on each flow node. */
export interface TreeNodeCardData extends Record<string, unknown> {
  label: string;
  nodeType: NodeType;
  content: string;
  authorId: string;
  createdAt: string;
  isAgent: boolean;
  isSystem: boolean;
  /** Metadata from the Yjs node — used by CardNode for structured card data */
  metadata: Record<string, unknown>;
  /** Number of children (for collapse UI) */
  childCount: number;
  /** Whether this node is currently collapsed */
  collapsed: boolean;
  /** Card-specific type (e.g. 'file', 'task', 'code') */
  cardType?: 'file' | 'task' | 'code';
  /** When true, this card node is an agent iteration card */
  isAgentCard?: boolean;
}

/** Map NodeType to the React Flow custom node type string. */
export function nodeTypeToFlowType(nodeType: string): FlowNodeType {
  switch (nodeType) {
    case 'synthesis':
      return 'synthesisNode';
    case 'card':
      return 'cardNode';
    case 'topic':
      return 'topicNode';
    case 'message':
    default:
      return 'messageNode';
  }
}
