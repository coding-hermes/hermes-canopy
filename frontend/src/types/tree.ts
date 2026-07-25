/**
 * Hermes Canopy — Tree Data Model Types
 *
 * Aligned with SPEC-DM-01 §5 (TypeScript Types) and backend Go structs.
 * Uses `import type` for verbatimModuleSyntax compliance.
 */

// ─── Enums ────────────────────────────────────────────────────────────

export type ContentFormat = 'markdown' | 'plain' | 'rich';

export type NodeType = 'message' | 'synthesis' | 'system';

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

/** Extended React Flow node data carried on each flow node. */
export interface TreeNodeCardData extends Record<string, unknown> {
  label: string;
  nodeType: NodeType;
  content: string;
  authorId: string;
  createdAt: string;
  isAgent: boolean;
  isSystem: boolean;
}
