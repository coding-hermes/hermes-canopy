/**
 * Unit tests — synthesis (merge) client contract (DF-HERMES-CANOPY-27)
 *
 * The route this module mirrors is live:
 * `POST /api/v1/trees/{tree_id}/merge` (SPEC-API-04 §3), 2–100 sources.
 * Three things are pinned here because each one has a real failure behind
 * it:
 *
 *   - the COUNT GATE. The bounds live in this module so the disabled button
 *     and the refused request cannot drift; 1 source is MIN_SOURCE_NODES
 *     (400) and 101 is MAX_SOURCE_NODES (400) — both are facts the client
 *     already holds.
 *   - the BODY. `content` is required and may be empty, so it is always a
 *     string (an absent field is INVALID_BODY), the ids are snake_case and
 *     the caller's order survives into `source_node_ids`.
 *   - the 201 ENVELOPE is snake_case while the node list the page renders
 *     is camelCase; the normaliser is the only place that translation
 *     happens, and it must refuse anything it cannot recognise rather than
 *     hand the page a half-read node.
 */

import { describe, it, expect } from 'vitest';
import {
  MERGE_CONTENT_FORMATS,
  MERGE_DEFAULT_CONTENT_FORMAT,
  MERGE_MAX_SOURCES,
  MERGE_MIN_SOURCES,
  buildMergeRequest,
  canMerge,
  describeMergeError,
  mergeDisabledReason,
  normalizeMergeResponse,
  type MergeContentFormat,
} from '../merge';

const SRC_A = '0191a8b2-7fff-7000-9000-000000000101';
const SRC_B = '0191a8b2-7fff-7000-9000-000000000102';
const ROOT = '0191a8b2-7fff-7000-9000-000000000001';

/** The §3.6 envelope as the server sends it (snake_case). */
function envelope(overrides: Record<string, unknown> = {}) {
  return {
    node: {
      id: '0191a8b2-7fff-7000-9000-000000000301',
      tree_id: '0191a8b2-7fff-7000-9000-000000000001',
      parent_id: ROOT,
      author_id: '0191a8b2-7fff-7000-9000-000000000042',
      author_display_name: 'Bane',
      content: 'Synthesising the two approaches',
      content_format: 'markdown',
      node_type: 'synthesis',
      sequence_num: 312,
      metadata: { merge_summary: 'resolved divergence' },
      depth: 1,
      child_count: 0,
      created_at: '2026-09-18T23:15:00Z',
      edited_at: null,
      deleted_at: null,
      ...overrides,
    },
    edges: [
      {
        id: 'e-1',
        tree_id: 't-1',
        source_node_id: ROOT,
        target_node_id: '0191a8b2-7fff-7000-9000-000000000301',
        edge_type: 'reply',
        created_at: '2026-09-18T23:15:00Z',
      },
      {
        id: 'e-2',
        tree_id: 't-1',
        source_node_id: SRC_A,
        target_node_id: '0191a8b2-7fff-7000-9000-000000000301',
        edge_type: 'synthesis',
        created_at: '2026-09-18T23:15:00Z',
      },
    ],
    merged_source_ids: [SRC_A, SRC_B],
  };
}

// ─── Bounds ────────────────────────────────────────────────────────────

describe('merge bounds (the server contract)', () => {
  it('is 2–100 sources, matching SPEC-API-04 §3.2', () => {
    expect(MERGE_MIN_SOURCES).toBe(2);
    expect(MERGE_MAX_SOURCES).toBe(100);
    expect(MERGE_DEFAULT_CONTENT_FORMAT).toBe('markdown');
    expect([...MERGE_CONTENT_FORMATS]).toEqual(['markdown', 'plain', 'rich']);
  });

  it('canMerge is true exactly in range', () => {
    expect(canMerge(0)).toBe(false);
    expect(canMerge(1)).toBe(false);
    expect(canMerge(2)).toBe(true);
    expect(canMerge(100)).toBe(true);
    expect(canMerge(101)).toBe(false);
    // NaN must not read as "in range".
    expect(canMerge(NaN)).toBe(false);
    expect(canMerge(Infinity)).toBe(false);
  });

  it('mergeDisabledReason names the bound that was broken, null when available', () => {
    expect(mergeDisabledReason(2)).toBeNull();
    expect(mergeDisabledReason(100)).toBeNull();
    expect(mergeDisabledReason(1)).toContain('at least 2');
    expect(mergeDisabledReason(101)).toContain('at most 100');
    // Non-finite counts are treated as zero, not as "somewhere in range".
    expect(mergeDisabledReason(NaN)).toContain('at least 2');
  });
});

// ─── Request builder ───────────────────────────────────────────────────

describe('buildMergeRequest', () => {
  it('sends snake_case body keys and preserves the caller order', () => {
    const built = buildMergeRequest([SRC_B, SRC_A]);
    expect(built.ok).toBe(true);
    if (!built.ok) return;
    expect(Object.keys(built.body).sort()).toEqual([
      'content',
      'content_format',
      'source_node_ids',
    ]);
    // Order is load-bearing: it is echoed in merged_source_ids and decides
    // the creation order of the synthesis edges.
    expect(built.body.source_node_ids).toEqual([SRC_B, SRC_A]);
  });

  it('always sends content as a string — empty is valid, absent is not', () => {
    const noSummary = buildMergeRequest([SRC_A, SRC_B]);
    expect(noSummary.ok).toBe(true);
    if (!noSummary.ok) return;
    expect(noSummary.body.content).toBe('');
    expect(typeof noSummary.body.content).toBe('string');

    const withSummary = buildMergeRequest([SRC_A, SRC_B], {
      content: 'Conclusion: use CTEs.',
    });
    expect(withSummary.ok).toBe(true);
    if (!withSummary.ok) return;
    expect(withSummary.body.content).toBe('Conclusion: use CTEs.');
  });

  it('defaults content_format to markdown and accepts the other two', () => {
    const dflt = buildMergeRequest([SRC_A, SRC_B]);
    expect(dflt.ok && dflt.body.content_format).toBe('markdown');

    for (const format of MERGE_CONTENT_FORMATS) {
      const built = buildMergeRequest([SRC_A, SRC_B], {
        contentFormat: format,
      });
      expect(built.ok && built.body.content_format).toBe(format);
    }
  });

  it('omits target_parent_id when not given (the server then uses the tree root)', () => {
    const built = buildMergeRequest([SRC_A, SRC_B]);
    expect(built.ok).toBe(true);
    if (!built.ok) return;
    expect('target_parent_id' in built.body).toBe(false);
  });

  it('passes target_parent_id and metadata through when given', () => {
    const built = buildMergeRequest([SRC_A, SRC_B], {
      content: 'x',
      targetParentId: `  ${ROOT}  `,
      metadata: { merge_summary: 'resolved' },
    });
    expect(built.ok).toBe(true);
    if (!built.ok) return;
    expect(built.body.target_parent_id).toBe(ROOT);
    expect(built.body.metadata).toEqual({ merge_summary: 'resolved' });
  });

  it('refuses fewer than 2 or more than 100 sources in the client', () => {
    for (const count of [0, 1]) {
      const built = buildMergeRequest(
        Array.from({ length: count }, (_, i) => `id-${i}`),
      );
      expect(built.ok).toBe(false);
      if (built.ok) return;
      expect(built.reason).toContain('at least 2');
    }

    const tooMany = buildMergeRequest(
      Array.from({ length: 101 }, (_, i) => `id-${i}`),
    );
    expect(tooMany.ok).toBe(false);
    if (tooMany.ok) return;
    expect(tooMany.reason).toContain('at most 100');
  });

  it('refuses a blank id', () => {
    const built = buildMergeRequest([SRC_A, '   ']);
    expect(built.ok).toBe(false);
    if (built.ok) return;
    expect(built.reason).toContain('blank');
  });

  it('refuses a duplicated id', () => {
    const built = buildMergeRequest([SRC_A, SRC_A]);
    expect(built.ok).toBe(false);
    if (built.ok) return;
    expect(built.reason).toContain('twice');
  });

  it('refuses a content_format outside the server enum', () => {
    const built = buildMergeRequest([SRC_A, SRC_B], {
      contentFormat: 'html' as unknown as MergeContentFormat,
    });
    expect(built.ok).toBe(false);
    if (built.ok) return;
    expect(built.reason).toContain('markdown, plain, rich');
  });

  it('does not mutate the caller\'s array', () => {
    const ids = [SRC_A, SRC_B];
    buildMergeRequest(ids);
    expect(ids).toEqual([SRC_A, SRC_B]);
  });
});

// ─── Response normaliser ───────────────────────────────────────────────

describe('normalizeMergeResponse', () => {
  it('translates the snake_case §3.6 envelope to the list\'s camelCase node', () => {
    const result = normalizeMergeResponse(envelope());
    expect(result).not.toBeNull();
    expect(result?.node).toEqual({
      id: '0191a8b2-7fff-7000-9000-000000000301',
      treeId: '0191a8b2-7fff-7000-9000-000000000001',
      parentId: ROOT,
      authorId: '0191a8b2-7fff-7000-9000-000000000042',
      authorDisplayName: 'Bane',
      content: 'Synthesising the two approaches',
      contentFormat: 'markdown',
      nodeType: 'synthesis',
      sequenceNum: 312,
      metadata: { merge_summary: 'resolved divergence' },
      depth: 1,
      childCount: 0,
      createdAt: '2026-09-18T23:15:00Z',
      editedAt: null,
      deletedAt: null,
    });
  });

  it('maps every edge and echoes merged_source_ids', () => {
    const result = normalizeMergeResponse(envelope());
    expect(result?.edges.map((e) => e.edgeType)).toEqual(['reply', 'synthesis']);
    expect(result?.edges[1].sourceNodeId).toBe(SRC_A);
    expect(result?.edges[1].targetNodeId).toBe(result?.node.id);
    expect(result?.mergedSourceIds).toEqual([SRC_A, SRC_B]);
  });

  it('defaults missing edges/merged_source_ids to empty arrays', () => {
    const raw = envelope();
    const result = normalizeMergeResponse({
      node: raw.node,
    });
    expect(result?.edges).toEqual([]);
    expect(result?.mergedSourceIds).toEqual([]);
  });

  it('keeps null parent/edited/deleted rather than inventing values', () => {
    const result = normalizeMergeResponse(
      envelope({ parent_id: null, edited_at: null, deleted_at: null }),
    );
    expect(result?.node.parentId).toBeNull();
    expect(result?.node.editedAt).toBeNull();
    expect(result?.node.deletedAt).toBeNull();
  });

  it('returns null for a payload that is not the documented envelope', () => {
    expect(normalizeMergeResponse(null)).toBeNull();
    expect(normalizeMergeResponse(undefined)).toBeNull();
    expect(normalizeMergeResponse('201 Created')).toBeNull();
    expect(normalizeMergeResponse({})).toBeNull();
    // A node without an id cannot be inserted into the list.
    expect(normalizeMergeResponse({ node: { node_type: 'synthesis' } })).toBeNull();
  });

  it('returns null when the node is not a synthesis node', () => {
    // The merge route is the only way to create one (§3.1); anything else
    // means this is not the response this client thinks it is.
    expect(
      normalizeMergeResponse(envelope({ node_type: 'message' })),
    ).toBeNull();
  });

  it('drops edges that carry no id instead of half-reading them', () => {
    const raw = envelope();
    const result = normalizeMergeResponse({
      node: raw.node,
      edges: [...raw.edges, { edge_type: 'synthesis' }],
    });
    expect(result?.edges).toHaveLength(2);
  });
});

// ─── Failure translation ───────────────────────────────────────────────

describe('describeMergeError', () => {
  it('recognises the §3.3 messages apiPost surfaces, and the variants', () => {
    const cases: Array<[string, string]> = [
      ['merge requires at least 2 source nodes (received 1)', 'MIN_SOURCE_NODES'],
      ['merge supports at most 100 source nodes (received 101)', 'MAX_SOURCE_NODES'],
      ['source_node_ids contains duplicate entries: "x"', 'DUPLICATE_SOURCE_NODES'],
      ['source_node_ids[0] is not a valid UUIDv7: "x"', 'INVALID_SOURCE_NODE_ID'],
      ['one or more source nodes not found', 'SOURCE_NODE_NOT_FOUND'],
      ['one or more source nodes have been deleted', 'SOURCE_NODE_DELETED'],
      ['a source node belongs to a different tree', 'TREE_MISMATCH'],
      ['target parent is one of the source nodes', 'SOURCE_TARGET_OVERLAP'],
      ['target_parent_id "x" is also a source node', 'SOURCE_TARGET_OVERLAP'],
      ['the merge target "x" is also a source node', 'SOURCE_TARGET_OVERLAP'],
      ['target parent node not found', 'TARGET_PARENT_NOT_FOUND'],
      ['target parent node has been deleted', 'TARGET_PARENT_DELETED'],
      ['content must not exceed 65536 characters', 'CONTENT_TOO_LONG'],
      ['tree has been deleted', 'TREE_DELETED'],
      ['tree not found', 'TREE_NOT_FOUND'],
      ['you are not a member of this tree', 'NOT_TREE_MEMBER'],
      ['too many requests — try again later', 'RATE_LIMITED'],
      ['merge service unavailable', 'SERVICE_UNAVAILABLE'],
    ];
    for (const [message, code] of cases) {
      const info = describeMergeError(message);
      expect(info.code, message).toBe(code);
      expect(info.hint, message).toBeTruthy();
    }
  });

  it('does not mistake a source-count message for an overlap', () => {
    // Ordering matters: "at least 2 source nodes" also contains "source
    // nodes", so the specific rows must win.
    expect(describeMergeError('merge requires at least 2 source nodes (received 1)').code).toBe(
      'MIN_SOURCE_NODES',
    );
    expect(
      describeMergeError('one or more source nodes have been deleted').code,
    ).toBe('SOURCE_NODE_DELETED');
  });

  it('returns nulls for an unrecognised message so the caller shows it verbatim', () => {
    expect(describeMergeError('internal server error')).toEqual({
      code: null,
      hint: null,
    });
    expect(describeMergeError('')).toEqual({ code: null, hint: null });
  });
});
