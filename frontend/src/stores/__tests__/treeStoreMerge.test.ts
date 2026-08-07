/**
 * BUG-032 regression tests — mergeBackendNodes
 *
 * Verifies the bridge between the authoritative REST node state and the
 * local Yjs replica that drives the React Flow canvas: roots land in
 * rootOrder, children get reply edges, merges are idempotent, deleted
 * nodes are skipped, and base64 metadata decodes.
 */
import { describe, expect, it } from 'vitest';
import {
  createTreeDoc,
  mergeBackendNodes,
  getAllNodeIds,
  getNode,
  getChildIds,
  getParentId,
  type BackendNodePayload,
} from '../treeStore.ts';

function node(overrides: Partial<BackendNodePayload>): BackendNodePayload {
  return {
    id: crypto.randomUUID(),
    parentId: null,
    content: 'test node',
    contentFormat: 'markdown',
    nodeType: 'message',
    authorId: '00000000-0000-0000-0000-000000000001',
    metadata: 'e30=', // base64 of {}
    createdAt: '2026-08-01T00:00:00Z',
    editedAt: null,
    ...overrides,
  };
}

describe('mergeBackendNodes', () => {
  it('adds a root node and appends it to rootOrder', () => {
    const doc = createTreeDoc('t1');
    const root = node({ content: 'hello' });

    const added = mergeBackendNodes(doc, [root]);

    expect(added).toBe(1);
    expect(getAllNodeIds(doc)).toEqual([root.id]);
    expect(getNode(doc, root.id)?.content).toBe('hello');
    expect(getParentId(doc, root.id)).toBeUndefined();
  });

  it('creates reply edges for children and resolves parent/child links', () => {
    const doc = createTreeDoc('t1');
    const root = node({ content: 'root' });
    const child = node({ parentId: root.id, content: 'child' });
    const grandchild = node({ parentId: child.id, content: 'grandchild' });

    mergeBackendNodes(doc, [root, child, grandchild]);

    expect(getChildIds(doc, root.id)).toEqual([child.id]);
    expect(getChildIds(doc, child.id)).toEqual([grandchild.id]);
    expect(getParentId(doc, child.id)).toBe(root.id);
    expect(getParentId(doc, grandchild.id)).toBe(child.id);
    // 3 nodes, 2 edges → 5 top-level entries
    expect(doc.edges.size).toBe(2);
  });

  it('is idempotent — re-merging adds nothing and creates no duplicate edges', () => {
    const doc = createTreeDoc('t1');
    const root = node({ content: 'root' });
    const child = node({ parentId: root.id, content: 'child' });

    const first = mergeBackendNodes(doc, [root, child]);
    const second = mergeBackendNodes(doc, [root, child]);

    expect(first).toBe(2);
    expect(second).toBe(0);
    expect(doc.nodes.size).toBe(2);
    expect(doc.edges.size).toBe(1);
    expect(getChildIds(doc, root.id)).toEqual([child.id]);
  });

  it('skips deleted nodes', () => {
    const doc = createTreeDoc('t1');
    const live = node({ content: 'live' });
    const deleted = node({
      content: 'gone',
      deletedAt: '2026-08-02T00:00:00Z',
    });

    const added = mergeBackendNodes(doc, [live, deleted]);

    expect(added).toBe(1);
    expect(getAllNodeIds(doc)).toEqual([live.id]);
  });

  it('decodes base64-encoded metadata JSON', () => {
    const doc = createTreeDoc('t1');
    const withMeta = node({
      metadata: btoa(JSON.stringify({ cardType: 'task', priority: 1 })),
    });

    mergeBackendNodes(doc, [withMeta]);

    expect(getNode(doc, withMeta.id)?.metadata).toEqual({
      cardType: 'task',
      priority: 1,
    });
  });

  it('tolerates plain-object metadata and invalid base64', () => {
    const doc = createTreeDoc('t1');
    const plain = node({ metadata: { cardType: 'file' } });
    const garbage = node({ metadata: '%%%not-base64%%%' });

    mergeBackendNodes(doc, [plain, garbage]);

    expect(getNode(doc, plain.id)?.metadata).toEqual({ cardType: 'file' });
    expect(getNode(doc, garbage.id)?.metadata).toEqual({});
  });

  it('does not overwrite nodes that already exist in the doc (local-first)', () => {
    const doc = createTreeDoc('t1');
    // Seed via merge, then mutate locally, then re-merge the same server id
    const n = node({ content: 'server version' });
    mergeBackendNodes(doc, [n]);
    doc.nodes.get(n.id)?.set('content', 'local version');
    expect(getNode(doc, n.id)?.content).toBe('local version');

    const added = mergeBackendNodes(doc, [n]);
    expect(added).toBe(0);
    expect(getNode(doc, n.id)?.content).toBe('local version');
  });
});
