/**
 * Hermes Canopy — Yjs CRDT Tree Store
 *
 * Implements the canonical Y.Doc shape from SPEC-DM-01 §6:
 *   nodes:   Y.Map<nodeId, NodeData>
 *   edges:   Y.Map<edgeId, EdgeData>
 *   rootOrder: Y.Array<nodeId>
 *   meta:    Y.Map (tree metadata)
 *
 * Uses y-indexeddb for offline persistence.
 */

import * as Y from 'yjs';
import { IndexeddbPersistence } from 'y-indexeddb';
import type { TreeEdgePayload } from '../lib/treeGraph.ts';
import type {
  NodeData,
  EdgeData,
  TreeMetadata,
  CreateNodePayload,
  CreateEdgePayload,
  CreateTreePayload,
  NodeType,
} from '../types/tree.ts';

// ─── Document shape ───────────────────────────────────────────────────

export interface TreeYDoc {
  ydoc: Y.Doc;
  nodes: Y.Map<Y.Map<unknown>>;
  edges: Y.Map<Y.Map<unknown>>;
  rootOrder: Y.Array<string>;
  meta: Y.Map<unknown>;
}

// ─── Helpers ──────────────────────────────────────────────────────────

function nowISO(): string {
  return new Date().toISOString();
}

function makeId(): string {
  // crypto.randomUUID is only available in secure contexts (https or
  // localhost) — Bane 08-22: the app is served over plain http on
  // Tailscale, where it threw "crypto.randomUUID is not a function" and
  // killed tree hydration. Fall back to a v4 UUID built from Math.random.
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

/** Extract plain JS object from a Y.Map of primitives / JSON-serializable values. */
function mapToObject(map: Y.Map<unknown>): Record<string, unknown> {
  const obj: Record<string, unknown> = {};
  for (const [key, value] of map.entries()) {
    obj[key] = value;
  }
  return obj;
}

/** Create a Y.Map from a plain JS object. */
function objectToMap(obj: Record<string, unknown>): Y.Map<unknown> {
  const map = new Y.Map<unknown>();
  for (const [key, value] of Object.entries(obj)) {
    map.set(key, value);
  }
  return map;
}

/** Create a Y.Map from untrusted object data. */
function dataToMap(data: unknown): Y.Map<unknown> {
  return objectToMap(data as Record<string, unknown>);
}

// ─── Factory ──────────────────────────────────────────────────────────

/**
 * Create a new Yjs document for a tree.
 * One Y.Doc per tree — treeId is the document name used for IndexedDB key.
 */
export function createTreeDoc(_treeId: string): TreeYDoc {
  const ydoc = new Y.Doc();
  return {
    ydoc,
    nodes: ydoc.getMap('nodes'),
    edges: ydoc.getMap('edges'),
    rootOrder: ydoc.getArray('rootOrder'),
    meta: ydoc.getMap('meta'),
  };
}

// ─── IndexedDB persistence ────────────────────────────────────────────

export function bindIndexedDB(_treeId: string, ydoc: Y.Doc): IndexeddbPersistence {
  return new IndexeddbPersistence(`canopy-tree-${_treeId}`, ydoc);
}

// ─── Tree metadata ────────────────────────────────────────────────────

export function createTree(
  doc: TreeYDoc,
  payload: CreateTreePayload,
): TreeMetadata {
  const id = makeId();
  const now = nowISO();
  const meta: TreeMetadata = {
    id,
    ownerId: payload.ownerId ?? '',
    title: payload.title,
    description: payload.description ?? '',
    rootNodeId: null,
    metadata: payload.metadata ?? {},
    createdAt: now,
    editedAt: null,
    deletedAt: null,
  };

  doc.ydoc.transact(() => {
    for (const [key, value] of Object.entries(meta)) {
      doc.meta.set(key, value);
    }
  });

  return meta;
}

export function getTreeMeta(doc: TreeYDoc): TreeMetadata {
  return mapToObject(doc.meta) as unknown as TreeMetadata;
}

// ─── Node CRUD ────────────────────────────────────────────────────────

export function createNode(
  doc: TreeYDoc,
  payload: CreateNodePayload,
): NodeData {
  const id = makeId();
  const now = nowISO();

  const data: NodeData = {
    id,
    content: payload.content,
    contentFormat: payload.contentFormat ?? 'markdown',
    nodeType: payload.nodeType ?? 'message',
    authorId: 'local', // replaced by server if synced
    metadata: payload.metadata ?? {},
    createdAt: now,
    editedAt: null,
  };

  const nodeMap = dataToMap(data);

  doc.ydoc.transact(() => {
    doc.nodes.set(id, nodeMap);

    // If this is a root node (no parent), add to rootOrder
    if (payload.parentId === null) {
      doc.rootOrder.push([id]);
    }
  });

  // If there's a parent, create the edge in the same transaction
  if (payload.parentId !== null) {
    createEdge(doc, {
      sourceId: payload.parentId,
      targetId: id,
      edgeType: payload.edgeType ?? 'reply',
      metadata: {},
    });
  }

  return data;
}

export function getNode(
  doc: TreeYDoc,
  nodeId: string,
): NodeData | undefined {
  const entry = doc.nodes.get(nodeId);
  if (!entry) return undefined;
  return mapToObject(entry) as unknown as NodeData;
}

// ─── Backend hydration (BUG-032) ──────────────────────────────────────

/**
 * Node shape as returned by the REST API (camelCase JSON). `metadata` is a
 * base64-encoded JSON string (e.g. "e30=" for `{}`), `parentId` is null for
 * root nodes. Matches internal/model Node JSON tags.
 */
export interface BackendNodePayload {
  id: string;
  treeId?: string;
  parentId: string | null;
  authorId?: string;
  authorDisplayName?: string;
  content: string;
  contentFormat?: string;
  nodeType?: string;
  sequenceNum?: number;
  /** base64-encoded JSON string from the API, or a plain object (tests). */
  metadata?: string | Record<string, unknown> | null;
  depth?: number;
  childCount?: number;
  createdAt?: string;
  editedAt?: string | null;
  deletedAt?: string | null;
}

/** Decode the API's base64-encoded metadata JSON; tolerate plain objects. */
function decodeMetadata(raw: unknown): Record<string, unknown> {
  if (raw == null) return {};
  if (typeof raw !== 'string') return raw as Record<string, unknown>;
  try {
    // atob is global in browsers, jsdom and Node >= 16.
    return JSON.parse(atob(raw)) as Record<string, unknown>;
  } catch {
    return {};
  }
}

// ─── Edge ingestion + lineage derivation ──────────────────────────────

/** A convergence edge is provenance, not a reply (SPEC-PL-06 §7.1). */
function isReferenceEdgeType(edgeType: unknown): boolean {
  return edgeType === 'reference';
}

/** True when the node's reserved metadata declares a multi-reference reply. */
function isMultiReferenceMetadata(metadata: Record<string, unknown>): boolean {
  const reserved = metadata?.multi_reference ?? metadata?.multiReference;
  if (!reserved || typeof reserved !== 'object') return false;
  const obj = reserved as Record<string, unknown>;
  const ids = obj.canonicalSourceIds ?? obj.canonical_source_ids;
  return Array.isArray(ids) ? ids.length > 0 : obj.primarySourceId !== undefined;
}

/**
 * Insert persisted edges into the replica, keeping their DATABASE ids.
 *
 * SPEC-PL-06 §7.1: React Flow edge identity must be the persisted
 * `edges.id` — that is what makes an edge re-readable, inspectable and
 * linkable from outside the canvas. Dedupe is by id, and then by
 * (source, target, type) so a previously synthesised local edge for the same
 * pair cannot double-wire the graph.
 *
 * Returns the number of edges newly added.
 */
export function mergeBackendEdges(
  doc: TreeYDoc,
  edges: readonly TreeEdgePayload[],
): number {
  let added = 0;
  doc.ydoc.transact(() => {
    added = addEdges(doc, edges);
  });
  return added;
}

/** Untransacted core — callers wrap it so nodes+edges land in one transaction. */
function addEdges(doc: TreeYDoc, edges: readonly TreeEdgePayload[]): number {
  let added = 0;
  for (const edge of edges) {
    if (!edge || !edge.id || !edge.sourceId || !edge.targetId) continue;
    if (doc.edges.has(edge.id)) continue;

    let duplicatePair = false;
    for (const [, edgeMap] of doc.edges.entries()) {
      if (
        edgeMap.get('sourceId') === edge.sourceId &&
        edgeMap.get('targetId') === edge.targetId &&
        (edgeMap.get('edgeType') ?? 'reply') === edge.edgeType
      ) {
        duplicatePair = true;
        break;
      }
    }
    if (duplicatePair) continue;

    doc.edges.set(
      edge.id,
      objectToMap({
        id: edge.id,
        sourceId: edge.sourceId,
        targetId: edge.targetId,
        edgeType: edge.edgeType,
        metadata: edge.metadata ?? {},
        createdAt: edge.createdAt ?? nowISO(),
      }),
    );
    added++;
  }
  return added;
}

/** True when the replica already wires an incoming edge to `nodeId`. */
function hasIncomingEdge(doc: TreeYDoc, nodeId: string): boolean {
  for (const [, edgeMap] of doc.edges.entries()) {
    if (edgeMap.get('targetId') === nodeId) return true;
  }
  return false;
}

/**
 * Merge backend nodes into the Yjs doc (BUG-032 fix).
 *
 * Bridges the authoritative REST state into the local replica that drives
 * React Flow. Idempotent: nodes already present are skipped, edges are
 * deduped by (sourceId, targetId), deleted nodes are ignored. Root nodes
 * (parentId === null) are appended to rootOrder; children get a `reply`
 * edge to their parent.
 *
 * `edges` (optional) are PERSISTED edges — the graph subtree payload, which
 * carries `edges.id` and the §5.2 metadata a synthesised edge cannot have.
 * They are merged FIRST so the lineage synthesis below can see what the
 * database already wired. Two rules follow from SPEC-PL-06 §7.1:
 *
 *   - a multi-reference reply never gets a synthetic `reply` edge (its
 *     edges ARE the reference edges — `parent_id` is only a display anchor,
 *     and a synthetic reply edge would duplicate one of them);
 *   - a node the database already wired is never synthesised twice.
 *
 * Returns the number of nodes newly added.
 */
export function mergeBackendNodes(
  doc: TreeYDoc,
  nodes: BackendNodePayload[],
  edges: readonly TreeEdgePayload[] = [],
): number {
  let added = 0;
  const now = nowISO();

  doc.ydoc.transact(() => {
    const existingIds = new Set(doc.nodes.keys());

    if (edges.length > 0) addEdges(doc, edges);

    for (const n of nodes) {
      if (!n || !n.id || n.deletedAt) continue;
      if (existingIds.has(n.id)) continue;

      const metadata = decodeMetadata(n.metadata);
      const nodeMap = objectToMap({
        id: n.id,
        // Display anchor (§7.1). Stored so the hierarchy can place a
        // multi-reference reply under its primary source without inventing
        // a reply edge for it.
        parentId: n.parentId ?? null,
        content: n.content ?? '',
        contentFormat: n.contentFormat ?? 'markdown',
        nodeType: n.nodeType ?? 'message',
        authorId: n.authorId ?? 'local',
        metadata,
        createdAt: n.createdAt ?? now,
        editedAt: n.editedAt ?? null,
      });
      doc.nodes.set(n.id, nodeMap);
      existingIds.add(n.id);
      added++;

      if (n.parentId == null) {
        doc.rootOrder.push([n.id]);
        continue;
      }

      // §7.1: reference edges are never inferred from parent_id, and a
      // multi-reference reply carries no lineage edge at all.
      if (isMultiReferenceMetadata(metadata)) continue;
      if (hasIncomingEdge(doc, n.id)) continue;

      // Dedupe edges by (sourceId, targetId) so re-hydration never
      // double-wires a parent/child pair.
      let hasEdge = false;
      for (const [, edgeMap] of doc.edges.entries()) {
        if (
          edgeMap.get('sourceId') === n.parentId &&
          edgeMap.get('targetId') === n.id
        ) {
          hasEdge = true;
          break;
        }
      }
      if (hasEdge) continue;

      const edgeId = makeId();
      doc.edges.set(
        edgeId,
        objectToMap({
          id: edgeId,
          sourceId: n.parentId,
          targetId: n.id,
          edgeType: 'reply',
          metadata: {},
          createdAt: n.createdAt ?? now,
        }),
      );
    }
  });

  return added;
}

/**
 * parent id → ordered lineage child ids, in ONE pass over the replica
 * (O(nodes + edges)). Every hot path (the tree walk, the d3 layout, the
 * collapse map) needs this same derivation, and calling `getChildIds` per
 * node would be O(n²) on a large tree.
 *
 * Lineage is `reply` / `fork` / `synthesis` edges PLUS each node's stored
 * display anchor (`parentId`). `reference` edges are excluded: a
 * multi-reference reply is not a reply of sources R2…RN, and treating a
 * convergence edge as lineage would place the node under whichever source
 * the edge map happened to yield first (SPEC-PL-06 §7.1).
 */
export function buildLineageChildMap(doc: TreeYDoc): Map<string, string[]> {
  const map = new Map<string, string[]>();

  const push = (parentId: unknown, childId: unknown): void => {
    if (typeof parentId !== 'string' || typeof childId !== 'string') return;
    if (!parentId || !childId) return;
    const list = map.get(parentId);
    if (list) {
      if (!list.includes(childId)) list.push(childId);
    } else {
      map.set(parentId, [childId]);
    }
  };

  for (const [, edgeMap] of doc.edges.entries()) {
    if (isReferenceEdgeType(edgeMap.get('edgeType'))) continue;
    push(edgeMap.get('sourceId'), edgeMap.get('targetId'));
  }

  // Display anchors with no lineage edge — a multi-reference reply's
  // parent_id is its primary source and its only incoming edges are
  // `reference` (§3.5 invariant 6).
  for (const [nodeId, nodeMap] of doc.nodes.entries()) {
    const anchored = nodeMap.get('parentId');
    if (typeof anchored === 'string' && anchored) push(anchored, nodeId);
  }

  return map;
}

/**
 * Return all node IDs in the tree.
 *
 * Membership is the replica's node list; the child map only orders the
 * walk. That matters for a multi-reference reply: it is reached through its
 * display anchor, never through a synthesised edge, and an unreachable node
 * would otherwise vanish from the canvas.
 */
export function getAllNodeIds(doc: TreeYDoc): string[] {
  const childMap = buildLineageChildMap(doc);
  const visited = new Set<string>();

  function walk(nodeId: string): void {
    if (visited.has(nodeId) || !doc.nodes.has(nodeId)) return;
    visited.add(nodeId);
    for (const childId of childMap.get(nodeId) ?? []) {
      walk(childId);
    }
  }

  for (const rootId of doc.rootOrder.toArray()) {
    walk(rootId);
  }
  for (const nodeId of doc.nodes.keys()) {
    walk(nodeId);
  }

  return Array.from(visited);
}

export function updateNode(
  doc: TreeYDoc,
  nodeId: string,
  updates: { content?: string; metadata?: Record<string, unknown> },
): void {
  const existing = doc.nodes.get(nodeId);
  if (!existing) return;

  doc.ydoc.transact(() => {
    if (updates.content !== undefined) {
      existing.set('content', updates.content);
      existing.set('editedAt', nowISO());
    }
    if (updates.metadata !== undefined) {
      existing.set('metadata', updates.metadata);
    }
  });
}

export function deleteNode(doc: TreeYDoc, nodeId: string): void {
  doc.ydoc.transact(() => {
    doc.nodes.delete(nodeId);
    // Remove from rootOrder if present
    const rootIdx = doc.rootOrder.toArray().indexOf(nodeId);
    if (rootIdx !== -1) {
      doc.rootOrder.delete(rootIdx, 1);
    }
    // Delete all edges involving this node
    for (const [edgeId, edgeMap] of doc.edges.entries()) {
      const sourceId = edgeMap.get('sourceId');
      const targetId = edgeMap.get('targetId');
      if (sourceId === nodeId || targetId === nodeId) {
        doc.edges.delete(edgeId);
      }
    }
  });
}

export function moveNode(
  doc: TreeYDoc,
  nodeId: string,
  newParentId: string,
): void {
  doc.ydoc.transact(() => {
    // Remove old incoming edge(s)
    for (const [edgeId, edgeMap] of doc.edges.entries()) {
      if (edgeMap.get('targetId') === nodeId) {
        doc.edges.delete(edgeId);
      }
    }
    // Create new edge
    const edgeId = makeId();
    const edgeMap = objectToMap({
      id: edgeId,
      sourceId: newParentId,
      targetId: nodeId,
      edgeType: 'fork',
      metadata: {},
      createdAt: nowISO(),
    });
    doc.edges.set(edgeId, edgeMap);
  });
}

// ─── Edge CRUD ────────────────────────────────────────────────────────

export function createEdge(
  doc: TreeYDoc,
  payload: CreateEdgePayload,
): EdgeData {
  const id = makeId();
  const now = nowISO();

  const data: EdgeData = {
    id,
    sourceId: payload.sourceId,
    targetId: payload.targetId,
    edgeType: payload.edgeType ?? 'reply',
    metadata: payload.metadata ?? {},
    createdAt: now,
  };

  const edgeMap = dataToMap(data);

  doc.ydoc.transact(() => {
    doc.edges.set(id, edgeMap);
  });

  return data;
}

export function getEdge(
  doc: TreeYDoc,
  edgeId: string,
): EdgeData | undefined {
  const entry = doc.edges.get(edgeId);
  if (!entry) return undefined;
  return mapToObject(entry) as unknown as EdgeData;
}

/**
 * Get all child node IDs for a given parent node.
 *
 * Lineage only (SPEC-PL-06 §7.1): `reference` edges are excluded — they are
 * provenance, not replies — and each node's stored display anchor
 * (`parentId`) counts, which is what places a multi-reference reply under
 * its primary source without inventing an edge for it. Prefer
 * `buildLineageChildMap` inside a loop; this helper rebuilds the map.
 */
export function getChildIds(doc: TreeYDoc, parentId: string): string[] {
  return [...(buildLineageChildMap(doc).get(parentId) ?? [])];
}

/**
 * Get the parent node ID for a given node.
 * Returns undefined for root nodes (no incoming edges).
 *
 * Lineage edges win (a `fork` from `moveNode` re-parents a node without
 * rewriting anything else); the stored display anchor is the fallback, which
 * is the only lineage a multi-reference reply has. `reference` edges are
 * never a parent (SPEC-PL-06 §7.1).
 */
export function getParentId(
  doc: TreeYDoc,
  nodeId: string,
): string | undefined {
  for (const [, edgeMap] of doc.edges.entries()) {
    if (isReferenceEdgeType(edgeMap.get('edgeType'))) continue;
    if (edgeMap.get('targetId') === nodeId) {
      return edgeMap.get('sourceId') as string | undefined;
    }
  }
  const anchored = doc.nodes.get(nodeId)?.get('parentId');
  if (typeof anchored === 'string' && anchored) return anchored;
  return undefined;
}

export function deleteEdge(doc: TreeYDoc, edgeId: string): void {
  doc.ydoc.transact(() => {
    doc.edges.delete(edgeId);
  });
}

// ─── Seed demo data ───────────────────────────────────────────────────

/**
 * Seed a tree document with demo nodes — E2E-ONLY TEST FIXTURE (GAP-051).
 * Called exclusively via `window.__canopySeedDemoTree` by the E2E battery
 * (tree-rendering / visual-regression tests). Never invoked by product
 * code; the demo tree is confined to tests and removed from the live DB
 * outside E2E windows (scripts/remove-demo-data.sql).
 * Creates a root node with several child reply nodes.
 */
export function seedDemoTree(doc: TreeYDoc): void {
  // Only seed if the tree is empty
  if (getAllNodeIds(doc).length > 0) return;

  doc.ydoc.transact(() => {
    // Create tree metadata
    doc.meta.set('title', 'Demo Tree');
    doc.meta.set('description', 'Seeded demo tree for E2E tests');

    // Create root node
    const rootId = crypto.randomUUID();
    const rootMap = objectToMap({
      id: rootId,
      content: 'Welcome to the Demo Tree 🌳',
      contentFormat: 'markdown',
      nodeType: 'message',
      authorId: 'local',
      metadata: {},
      createdAt: new Date().toISOString(),
      editedAt: null,
    });
    doc.nodes.set(rootId, rootMap);
    doc.rootOrder.push([rootId]);

    // Create child nodes
    const childData = [
      { content: 'This is a reply to the root node', authorId: 'local', nodeType: 'message' },
      { content: 'Another branch of the conversation', authorId: 'local', nodeType: 'message' },
      { content: 'A third reply for more tree depth', authorId: 'local', nodeType: 'message' },
      { content: 'This is a synthesis node', authorId: 'local', nodeType: 'synthesis' },
    ];

    for (const cd of childData) {
      const childId = crypto.randomUUID();
      const childMap = objectToMap({
        id: childId,
        content: cd.content,
        contentFormat: 'markdown',
        nodeType: cd.nodeType,
        authorId: cd.authorId,
        metadata: {},
        createdAt: new Date().toISOString(),
        editedAt: null,
      });
      doc.nodes.set(childId, childMap);

      // Create edge from root to child
      const edgeId = crypto.randomUUID();
      const edgeMap = objectToMap({
        id: edgeId,
        sourceId: rootId,
        targetId: childId,
        edgeType: 'reply',
        metadata: {},
        createdAt: new Date().toISOString(),
      });
      doc.edges.set(edgeId, edgeMap);
    }
  });
}

// ─── Node type helpers ────────────────────────────────────────────────

export function isSynthesisNode(doc: TreeYDoc, nodeId: string): boolean {
  const node = getNode(doc, nodeId);
  return node?.nodeType === 'synthesis';
}

export function getNodeType(doc: TreeYDoc, nodeId: string): NodeType | undefined {
  const node = getNode(doc, nodeId);
  return node?.nodeType as NodeType | undefined;
}
