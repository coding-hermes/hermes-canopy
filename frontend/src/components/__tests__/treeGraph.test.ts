/**
 * SPEC-PL-06 §7.1 — graph subtree wire normalisation (lib/treeGraph.ts)
 *
 * The graph read is the ONLY source of edges with a persisted `edges.id`.
 * These tests pin the wire contract it has to survive: snake_case keys,
 * `metadata` absent on a `{}` column, a nil (Go `null`) node/edge slice, and
 * rows that are unusable (no id, no endpoint) being DROPPED rather than
 * given a locally generated identity — which is exactly what §7.1 forbids.
 */

import { describe, expect, it } from 'vitest';
import {
  rootNodeIdOf,
  subtreeRequestPath,
  toEdgePayloads,
  toGraphNodePayloads,
} from '../../lib/treeGraph.ts';

const TREE = '0191a8b2-7fff-7000-9000-000000000001';
const ROOT = '0191a8b2-7fff-7000-9000-000000000002';
const SRC_A = '0191a8b2-7fff-7000-9000-000000000101';
const SRC_B = '0191a8b2-7fff-7000-9000-000000000202';
const REPLY = '0191a8b2-7fff-7000-9000-000000000301';
const EDGE_A = '0191a8b2-7fff-7000-9000-000000000401';
const EDGE_B = '0191a8b2-7fff-7000-9000-000000000402';
const EDGE_REPLY = '0191a8b2-7fff-7000-9000-000000000403';

function subtreeBody() {
  return {
    nodes: [
      { id: ROOT, tree_id: TREE, parent_id: null, type: 'message', depth: 0, created_at: '2026-07-22T10:00:00Z' },
      { id: SRC_A, tree_id: TREE, parent_id: ROOT, type: 'message', depth: 1, created_at: '2026-07-22T10:01:00Z' },
      { id: SRC_B, tree_id: TREE, parent_id: ROOT, type: 'message', depth: 1, created_at: '2026-07-22T10:02:00Z' },
      { id: REPLY, tree_id: TREE, parent_id: SRC_A, type: 'message', depth: 2, created_at: '2026-07-22T10:03:00Z' },
    ],
    edges: [
      {
        id: EDGE_A,
        source_id: SRC_A,
        target_id: REPLY,
        edge_type: 'reference',
        depth: 0,
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
        depth: 0,
        metadata: {
          reference_index: 1,
          source_label: 'R2',
          color_key: 'ref-1',
          selection_order: 1,
          role: 'context_source',
        },
      },
      // A lineage reply edge whose metadata column held the `{}` default —
      // Go omits the key entirely.
      { id: EDGE_REPLY, source_id: ROOT, target_id: SRC_A, edge_type: 'reply', depth: 0 },
    ],
  };
}

describe('graph subtree normalisation', () => {
  it('builds the request path the handler serves', () => {
    expect(subtreeRequestPath(TREE, ROOT)).toBe(
      `/graph/trees/${TREE}/subtree/${ROOT}?max_depth=0`,
    );
    expect(subtreeRequestPath(TREE, ROOT, 3)).toBe(
      `/graph/trees/${TREE}/subtree/${ROOT}?max_depth=3`,
    );
  });

  it('derives the root locally from the node list', () => {
    expect(rootNodeIdOf(subtreeBody().nodes)).toBe(ROOT);
    // camelCase (`GET /trees/{id}/nodes`) works too.
    expect(rootNodeIdOf([{ id: SRC_A, parentId: ROOT }, { id: ROOT, parentId: null }])).toBe(ROOT);
    expect(rootNodeIdOf([])).toBeNull();
    expect(rootNodeIdOf(null)).toBeNull();
  });

  it('keeps the persisted edge id and the §5.2 metadata', () => {
    const edges = toEdgePayloads(subtreeBody().edges);
    expect(edges).toHaveLength(3);

    const [first, second, reply] = edges;
    expect(first).toMatchObject({
      id: EDGE_A,
      sourceId: SRC_A,
      targetId: REPLY,
      edgeType: 'reference',
    });
    expect(first.metadata).toEqual({
      reference_index: 0,
      source_label: 'R1',
      color_key: 'ref-6',
      selection_order: 0,
      role: 'context_source',
    });
    expect(second.id).toBe(EDGE_B);
    expect(second.metadata.reference_index).toBe(1);

    // An absent metadata column becomes an empty object, never a hole.
    expect(reply.edgeType).toBe('reply');
    expect(reply.metadata).toEqual({});
  });

  it('drops rows it cannot address instead of inventing an id', () => {
    const edges = toEdgePayloads([
      { id: EDGE_A, source_id: SRC_A, target_id: REPLY, edge_type: 'reference' },
      { source_id: SRC_A, target_id: REPLY, edge_type: 'reference' }, // no id
      { id: 'x', source_id: SRC_A, edge_type: 'reference' }, // no target
      { id: 'y', target_id: REPLY, edge_type: 'reference' }, // no source
    ]);
    expect(edges.map((e) => e.id)).toEqual([EDGE_A]);
  });

  it('tolerates a null edge/node slice', () => {
    expect(toEdgePayloads(null)).toEqual([]);
    expect(toEdgePayloads(undefined)).toEqual([]);
    expect(toGraphNodePayloads(null)).toEqual([]);
  });

  it('maps graph nodes into the replica payload shape', () => {
    const nodes = toGraphNodePayloads(subtreeBody().nodes);
    expect(nodes).toHaveLength(4);
    expect(nodes[0]).toEqual({
      id: ROOT,
      treeId: TREE,
      parentId: null,
      content: '',
      nodeType: 'message',
      createdAt: '2026-07-22T10:00:00Z',
    });
    expect(nodes[1].parentId).toBe(ROOT);
  });

  it('defaults a missing edge_type to reply, matching the schema', () => {
    const [edge] = toEdgePayloads([{ id: EDGE_A, source_id: SRC_A, target_id: REPLY }]);
    expect(edge.edgeType).toBe('reply');
  });
});
