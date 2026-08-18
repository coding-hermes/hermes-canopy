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
  /**
   * Root message for the new tree. The backend (GAP-008 contract) requires
   * content AND a valid nodeType — a title-only payload 400s with
   * VALIDATION_ERROR 'root message content is required'.
   */
  rootMessage?: {
    content: string;
    contentFormat?: string;
    nodeType?: string;
  };
}

// ─── Tree Detail / Related (WIRE-006) ─────────────────────────────────

/**
 * Lightweight {id, title} reference to a related tree (WIRE-006).
 * Mirrors the backend `RelatedRef` struct.
 */
export interface RelatedRef {
  id: string;
  title: string;
}

/** A delegation goal extracted from tree metadata (WIRE-006). */
export interface DelegationRef {
  delegation_id: string;
  goal: string;
}

/**
 * Session-lineage associations for a tree imported from a Hermes session
 * (WIRE-006). Mirrors the backend `Related` struct — every field is
 * `omitempty`, so a key is ABSENT when empty (never `null`), and the
 * whole object is absent for trees with no association metadata.
 */
export interface TreeRelated {
  parent?: RelatedRef;
  children?: RelatedRef[];
  board_task?: string;
  project?: string;
  commit_hash?: string;
  delegation_goals?: DelegationRef[];
}

/** Computed aggregate statistics on the tree detail payload (WIRE-006). */
export interface TreeStats {
  node_count: number;
  member_count: number;
  branch_count: number;
  max_depth: number;
  pending_approvals: number;
}

/** Lightweight member representation on the tree detail payload. */
export interface MemberSummary {
  user_id: string;
  display_name: string;
  role: string;
  joined_at: string;
}

/**
 * Full tree detail — `GET /trees/{id}`. Mirrors the backend `TreeDetail`
 * struct: the base summary fields plus optional stats/members/related.
 */
export interface TreeDetail {
  id: string;
  title: string;
  description: string;
  owner_id: string;
  owner_display_name: string;
  node_count: number;
  member_count: number;
  root_node_id: string;
  created_at: string;
  updated_at: string;
  role: string;
  deleted_at?: string | null;
  stats?: TreeStats | null;
  members?: MemberSummary[] | null;
  related?: TreeRelated;
}

// ─── React Flow Node/Edge Types ───────────────────────────────────────

/** React Flow node type identifiers for custom rendering. */
export type FlowNodeType =
  | 'messageNode'
  | 'synthesisNode'
  | 'cardNode'
  | 'topicNode'
  | 'agentCardNode'
  | 'ghostNode';

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

  // ─── UI-04: branching canvas chrome ─────────────────────────────────
  /**
   * Direct reply count for the badge. Derived from the graph (or the
   * authoritative `GET /trees/{id}/nodes` payload) by the canvas — never
   * hardcoded. Falls back to `childCount` when absent.
   */
  replyCount?: number;
  /** How many nodes this node is hiding while collapsed. */
  hiddenCount?: number;
  /** Toggle this node's subtree. Absent on leaves — no chevron rendered. */
  onToggleCollapse?: () => void;
  /** Real author display names, when the caller can resolve them. */
  authorNames?: ReadonlyMap<string, string>;
}

/** Data carried on a ghost placeholder node (UI-04 add-reply affordance). */
export interface GhostNodeData extends Record<string, unknown> {
  /** Node this ghost would become a child of. */
  parentId: string;
  /** Invoked when the user activates the slot. */
  onCreate?: (parentId: string) => void;
  /** Label shown inside the dashed outline. */
  label?: string;
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
