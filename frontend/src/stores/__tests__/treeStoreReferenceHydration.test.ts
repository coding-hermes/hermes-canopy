/**
 * SPEC-PL-06 §7.1 — hydration of persisted edges into the Yjs replica.
 *
 * `mergeBackendNodes` already bridged REST nodes into the replica (BUG-032).
 * The reference model needs the other half: the graph read's edges must land
 * with their PERSISTED database ids and their §5.2 metadata, so React Flow
 * renders convergence edges that mean something outside the canvas (§7.1),
 * and `parent_id` must never be turned into a synthetic reply edge for a
 * multi-reference reply (§3.5 invariant 6).
 *
 * Also pins the lineage derivations that keep a convergence edge out of the
 * hierarchy: it is provenance, not a reply, so it must not become a
 * child of R2, and it must not swallow the display anchor under R1.
 */

import { describe, expect, it } from 'vitest';
import {
  buildLineageChildMap,
  createTreeDoc,
  getAllNodeIds,
  getChildIds,
  getNode,
  getParentId,
  mergeBackendEdges,
  mergeBackendNodes,
  type BackendNodePayload,
} from '../treeStore.ts';
import {
  toEdgePayloads,
  toGraphNodePayloads,
  type RawSubtree,
} from '../../lib/treeGraph.ts';

// ─── Fixtures ──────────────────────────────────────────────────────────

const TREE = '0191a8b2-7fff-7000-9000-000000000001';
const ROOT = '0191a8b2-7fff-7000-9000-000000000002';
const SRC_A = '0191a8b2-7fff-7000-9000-000000000101';
const SRC_B = '0191a8b2-7fff-7000-9000-000000000202';
const SRC_C = '0191a8b2-7fff-7000-9000-000000000203';
const REPLY = '0191a8b2-7fff-7000-9000-000000000301';
const EDGE_A = '0191a8b2-7fff-7000-9000-0000000401';
const EDGE_B = '0191a8b2-7fff-7000-9000-0000000402';
const EDGE_C = '0191a8b2-7fff-7000-9000-0000000403';
const EDGE_ROOT_A = '0191a8b2-7fff-7000-9000-0000000411';
const EDGE_ROOT_B = '0191a8b2-7fff-7000-9000-0000000412';
const EDGE_ROOT_C = '0191a8b2-7fff-7000-9000-0000000413';

const MULTI_REFERENCE_METADATA = {
  multi_reference: {
    version: 1,
    primarySourceId: SRC_A,
    canonicalSourceIds: [SRC_A, SRC_B, SRC_C],
    isSyntheticMergePoint: false,
    contextManifestHash: 'a'.repeat(64),
    contextTokenBudget: 8192,
  },
};

function node(overrides: Partial<BackendNodePayload> & { id: string }): BackendNodePayload {
  return {
    parentId: null,
    content: 'test node',
    contentFormat: 'markdown',
    nodeType: 'message',
    authorId: '00000000-0000-0000-0000-000000000001',
    metadata: 'e30=', // base64 of {}
    createdAt: '2026-07-22T10:00:00Z',
    editedAt: null,
    ...overrides,
  };
}

/** The graph read's answer for the reference reply. */
function graphBody(): RawSubtree {
  return {
    nodes: [
      { id: ROOT, tree_id: TREE, parent_id: null, type: 'message', depth: 0 },
      { id: SRC_A, tree_id: TREE, parent_id: ROOT, type: 'message', depth: 1 },
      { id: SRC_B, tree_id: TREE, parent_id: ROOT, type: 'message', depth: 1 },
      { id: SRC_C, tree_id: TREE, parent_id: ROOT, type: 'message', depth: 1 },
      { id: REPLY, tree_id: TREE, parent_id: SRC_A, type: 'message', depth: 2 },
    ],
    edges: [
      { id: EDGE_ROOT_A, source_id: ROOT, target_id: SRC_A, edge_type: 'reply' },
      { id: EDGE_ROOT_B, source_id: ROOT, target_id: SRC_B, edge_type: 'reply' },
      { id: EDGE_ROOT_C, source_id: ROOT, target_id: SRC_C, edge_type: 'reply' },
      {
        id: EDGE_A,
        source_id: SRC_A,
        target_id: REPLY,
        edge_type: 'reference',
        metadata: {
          reference_index: 0,
          source_label: 'R1',
          color_key: 'ref-6',
          selection_order: 0,
          role: 'context_source',
        },
      },
      {
        id: EDGE_B,
        source_id: SRC_B,
        target_id: REPLY,
        edge_type: 'reference',
        metadata: {
          reference_index: 1,
          source_label: 'R2',
          color_key: 'ref-1',
          selection_order: 1,
          role: 'context_source',
        },
      },
      {
        id: EDGE_C,
        source_id: SRC_C,
        target_id: REPLY,
        edge_type: 'reference',
        metadata: {
          reference_index: 2,
          source_label: 'R3',
          color_key: 'ref-3',
          selection_order: 2,
          role: 'context_source',
        },
      },
    ],
  };
}

/** The REST node list for the same tree (the richer payload, merged first). */
function restNodes(): BackendNodePayload[] {
  return [
    node({ id: ROOT, content: 'Root' }),
    node({ id: SRC_A, parentId: ROOT, content: 'Message A' }),
    node({ id: SRC_B, parentId: ROOT, content: 'Message B' }),
    node({ id: SRC_C, parentId: ROOT, content: 'Message C' }),
    node({
      id: REPLY,
      parentId: SRC_A,
      content: 'Agent reply',
      metadata: btoa(JSON.stringify(MULTI_REFERENCE_METADATA)),
    }),
  ];
}

/** Everything TreeView's hydration does, in the order it does it. */
function hydrate() {
  const doc = createTreeDoc(TREE);
  const graph = graphBody();
  // Edges first, so every edge the database holds keeps its own id and the
  // lineage synthesis has nothing left to invent (TreeView merges in this
  // order for exactly that reason).
  mergeBackendEdges(doc, toEdgePayloads(graph.edges));
  mergeBackendNodes(doc, restNodes());
  mergeBackendNodes(doc, toGraphNodePayloads(graph.nodes));
  return { doc, graph };
}

// ─── Persisted edge identity ───────────────────────────────────────────

describe('§7.1 hydration — persisted edge identity', () => {
  it('inserts reference edges under their database ids with §5.2 metadata', () => {
    const { doc } = hydrate();

    const edge = doc.edges.get(EDGE_B);
    expect(edge).toBeDefined();
    expect(edge!.get('sourceId')).toBe(SRC_B);
    expect(edge!.get('targetId')).toBe(REPLY);
    expect(edge!.get('edgeType')).toBe('reference');
    expect(edge!.get('metadata')).toEqual({
      reference_index: 1,
      source_label: 'R2',
      color_key: 'ref-1',
      selection_order: 1,
      role: 'context_source',
    });
    // The id on the map entry is the persisted one, not a generated uuid.
    expect(edge!.get('id')).toBe(EDGE_B);
  });

  it('inserts all three reference edges and the three lineage edges', () => {
    const { doc } = hydrate();
    const ids = [...doc.edges.keys()].sort();
    expect(ids).toEqual([EDGE_A, EDGE_B, EDGE_C, EDGE_ROOT_A, EDGE_ROOT_B, EDGE_ROOT_C].sort());
  });

  it('is idempotent — re-hydration adds no second edge', () => {
    const { doc, graph } = hydrate();
    const again = mergeBackendEdges(doc, toEdgePayloads(graph.edges));
    expect(again).toBe(0);
    expect(doc.edges.size).toBe(6);
  });

  it('does not duplicate an already-synthesised parent/child pair', () => {
    const doc = createTreeDoc(TREE);
    // Synthesise root→A first (no persisted edges available).
    mergeBackendNodes(doc, [node({ id: ROOT }), node({ id: SRC_A, parentId: ROOT })]);
    expect(doc.edges.size).toBe(1);

    const added = mergeBackendEdges(doc, toEdgePayloads(graphBody().edges));
    // root→A is already wired (reply), so the persisted row is skipped; the
    // five others land. A duplicate pair would double-draw the connector.
    expect(added).toBe(5);
    const pairs = [...doc.edges.values()].map((e) => `${e.get('sourceId')}->${e.get('targetId')}`);
    expect(new Set(pairs).size).toBe(pairs.length);
  });

  it('skips a row with no usable identity', () => {
    const doc = createTreeDoc(TREE);
    const added = mergeBackendEdges(doc, [
      { id: '', sourceId: SRC_A, targetId: REPLY, edgeType: 'reference', metadata: {} },
      { id: EDGE_A, sourceId: '', targetId: REPLY, edgeType: 'reference', metadata: {} },
    ]);
    expect(added).toBe(0);
    expect(doc.edges.size).toBe(0);
  });
});

// ─── No synthetic edge for a multi-reference reply ─────────────────────

describe('§7.1 hydration — no duplicate synthetic reply edge', () => {
  it('never synthesises a reply edge for a multi_reference node', () => {
    const { doc } = hydrate();

    // Only the three persisted reference edges point at the reply.
    const incoming = [...doc.edges.values()].filter((e) => e.get('targetId') === REPLY);
    expect(incoming).toHaveLength(3);
    expect(incoming.map((e) => e.get('edgeType'))).toEqual([
      'reference',
      'reference',
      'reference',
    ]);
    // …and R1 → Reply appears exactly once, so the parent/reference pair is
    // not drawn twice.
    expect(incoming.filter((e) => e.get('sourceId') === SRC_A)).toHaveLength(1);
  });

  it('keeps the reply reachable through its display anchor', () => {
    const { doc } = hydrate();
    // Depth-first walk from rootOrder, then any node the walk could not
    // reach — membership never depends on an invented edge.
    expect(getAllNodeIds(doc).sort()).toEqual([ROOT, SRC_A, SRC_B, SRC_C, REPLY].sort());
    expect(getAllNodeIds(doc)[0]).toBe(ROOT);
    // parent_id is the display anchor (R1), not a synthetic edge.
    expect(getParentId(doc, REPLY)).toBe(SRC_A);
    expect(getNode(doc, REPLY)?.parentId).toBe(SRC_A);
  });

  it('does not create a synthetic reply edge when the reply node is merged alone', () => {
    const doc = createTreeDoc(TREE);
    mergeBackendNodes(doc, [node({ id: ROOT }), node({ id: SRC_A, parentId: ROOT })]);
    mergeBackendNodes(doc, [
      node({
        id: REPLY,
        parentId: SRC_A,
        metadata: btoa(JSON.stringify(MULTI_REFERENCE_METADATA)),
      }),
    ]);

    expect([...doc.edges.values()].filter((e) => e.get('targetId') === REPLY)).toHaveLength(0);
    // Still rendered: the anchor is enough to place it.
    expect(getAllNodeIds(doc)).toContain(REPLY);
  });

  it('still synthesises the reply edge for an ordinary lineage node', () => {
    const doc = createTreeDoc(TREE);
    mergeBackendNodes(doc, [node({ id: ROOT }), node({ id: SRC_A, parentId: ROOT })]);
    const edges = [...doc.edges.values()];
    expect(edges).toHaveLength(1);
    expect(edges[0].get('edgeType')).toBe('reply');
    expect(edges[0].get('sourceId')).toBe(ROOT);
    expect(edges[0].get('targetId')).toBe(SRC_A);
  });

  it('does not synthesise a second edge when the database already wired the node', () => {
    const doc = createTreeDoc(TREE);
    const graph = graphBody();
    mergeBackendEdges(doc, toEdgePayloads(graph.edges));
    mergeBackendNodes(doc, restNodes());
    expect(doc.edges.size).toBe(6);
  });
});

// ─── Lineage derivation excludes convergence edges ─────────────────────

describe('lineage derivation excludes reference edges', () => {
  it('does not treat R2 as a parent of the reply', () => {
    const { doc } = hydrate();
    const childMap = buildLineageChildMap(doc);

    expect(childMap.get(SRC_A)).toEqual([REPLY]);
    expect(childMap.get(SRC_B) ?? []).toEqual([]);
    expect(childMap.get(SRC_C) ?? []).toEqual([]);
    expect(childMap.get(ROOT)).toEqual([SRC_A, SRC_B, SRC_C]);
  });

  it('getChildIds/getParentId agree with the child map', () => {
    const { doc } = hydrate();
    expect(getChildIds(doc, SRC_A)).toEqual([REPLY]);
    expect(getChildIds(doc, SRC_B)).toEqual([]);
    expect(getParentId(doc, REPLY)).toBe(SRC_A);
    expect(getParentId(doc, SRC_B)).toBe(ROOT);
  });

  it('keeps the display anchor as the only lineage for a reference-only node', () => {
    const doc = createTreeDoc(TREE);
    // No nodes/edges from REST beyond the source: the reply is wired ONLY by
    // reference edges, and its parent_id is its sole lineage.
    mergeBackendNodes(doc, [node({ id: ROOT }), node({ id: SRC_A, parentId: ROOT })]);
    mergeBackendEdges(doc, [
      {
        id: EDGE_A,
        sourceId: SRC_A,
        targetId: REPLY,
        edgeType: 'reference',
        metadata: { source_label: 'R1' },
      },
    ]);
    mergeBackendNodes(doc, [node({ id: REPLY, parentId: SRC_A })]);

    expect(getChildIds(doc, SRC_A)).toEqual([REPLY]);
    expect(getParentId(doc, REPLY)).toBe(SRC_A);
    expect(buildLineageChildMap(doc).get(SRC_A)).toEqual([REPLY]);
  });
});
