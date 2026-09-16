/**
 * SPEC-PL-06 §5.2 / §7.1 / §7.2 / §7.3 derivations — lib/multiReference.ts
 *
 * The render half of the reference model is pure derivation plus a thin
 * paint layer, so this file pins the derivation: the §7.2 palette map, the
 * `R#` labels up to the 20-source ceiling, the badge text, the EXACT §7.2
 * accessibility sentence, the §5.2 edge metadata normalisation, the §9.1 /
 * §9.3 wire normalisation, and the §7.3 inspector view model (including the
 * §8.2 synthetic-merge case).
 */

import { describe, expect, it } from 'vitest';
import { referencePalette } from '../../theme.ts';
import {
  REFERENCE_COLOR_KEYS,
  REFERENCE_STROKE_WIDTH,
  buildReferenceInspectorModel,
  buildReferenceNodeView,
  flowEdgeIsReference,
  multiReferenceMetadataFromNode,
  normaliseColorKey,
  normaliseEdgeMetadata,
  normaliseMultiReferenceMetadata,
  normaliseReferenceContext,
  referenceArrowMarker,
  referenceAriaDescription,
  referenceBadgeAriaLabel,
  referenceBadgeLabel,
  referenceContextRequestPath,
  referenceEdgeData,
  referenceEdgeHighlightState,
  referenceEdgeStyle,
  referenceSourceLabel,
  referenceStroke,
} from '../../lib/multiReference.ts';

// ─── §7.2 palette ──────────────────────────────────────────────────────

describe('§7.2 edge style contract', () => {
  const SPEC_PALETTE: Array<[string, string]> = [
    ['ref-0', '#2563EB'],
    ['ref-1', '#059669'],
    ['ref-2', '#D97706'],
    ['ref-3', '#7C3AED'],
    ['ref-4', '#DB2777'],
    ['ref-5', '#0891B2'],
    ['ref-6', '#65A30D'],
    ['ref-7', '#C2410C'],
  ];

  it('maps every color_key to the exact spec stroke', () => {
    for (const [key, hex] of SPEC_PALETTE) {
      expect(referenceStroke(key)).toBe(hex);
      expect(referencePalette[key as keyof typeof referencePalette]).toBe(hex);
    }
    expect(REFERENCE_COLOR_KEYS).toEqual(SPEC_PALETTE.map(([k]) => k));
  });

  it('uses a 2.5px solid stroke with no dash for every key', () => {
    for (const key of REFERENCE_COLOR_KEYS) {
      const style = referenceEdgeStyle(key);
      expect(style.strokeWidth).toBe(REFERENCE_STROKE_WIDTH);
      expect(REFERENCE_STROKE_WIDTH).toBe(2.5);
      expect(style.dash).toBeUndefined();
      expect(style.animated).toBe(false);
    }
  });

  it('degrades an unknown key to ref-0 rather than to an undefined stroke', () => {
    expect(normaliseColorKey('ref-99')).toBe('ref-0');
    expect(normaliseColorKey(undefined)).toBe('ref-0');
    expect(normaliseColorKey({ nope: true })).toBe('ref-0');
    expect(referenceStroke('  ref-3  ')).toBe('#7C3AED');
  });

  it('puts the §7.2 arrow at the target in the edge colour', () => {
    const marker = referenceArrowMarker('ref-4');
    expect(marker).toEqual({
      type: 'arrowclosed',
      color: '#DB2777',
      width: 14,
      height: 14,
    });
  });
});

// ─── §5.2 labels ───────────────────────────────────────────────────────

describe('§5.2 source labels', () => {
  it('labels index 0 as R1', () => {
    expect(referenceSourceLabel(0)).toBe('R1');
    expect(referenceSourceLabel(1)).toBe('R2');
  });

  it('keeps all 20 selections distinct even though the palette repeats', () => {
    const labels = Array.from({ length: 20 }, (_, i) => referenceSourceLabel(i));
    expect(new Set(labels).size).toBe(20);
    expect(labels[0]).toBe('R1');
    expect(labels[7]).toBe('R8');
    expect(labels[8]).toBe('R9');
    expect(labels[16]).toBe('R17');
    expect(labels[19]).toBe('R20');

    // R9 and R17 share a palette entry (index mod 8), so the label is what
    // keeps them apart (§7.2: colour is never the sole identifier).
    expect(REFERENCE_COLOR_KEYS.length).toBe(8);
    expect(referenceStroke('ref-0')).toBe(referenceStroke('ref-0'));
  });

  it('normalises a persisted §5.2 metadata object', () => {
    const meta = normaliseEdgeMetadata({
      reference_index: 6,
      source_label: 'R7',
      color_key: 'ref-6',
      selection_order: 6,
      role: 'context_source',
    });
    expect(meta).toEqual({
      referenceIndex: 6,
      sourceLabel: 'R7',
      colorKey: 'ref-6',
      selectionOrder: 6,
      role: 'context_source',
    });
    expect(referenceEdgeData(meta!)).toEqual({
      edgeType: 'reference',
      isReference: true,
      referenceIndex: 6,
      sourceLabel: 'R7',
      colorKey: 'ref-6',
    });
  });

  it('falls back to the row position for a metadata-poor edge, and rejects a non-reference one', () => {
    expect(normaliseEdgeMetadata({ role: 'context_source' }, 3)).toEqual({
      referenceIndex: 3,
      sourceLabel: 'R4',
      colorKey: 'ref-0',
      selectionOrder: 3,
      role: 'context_source',
    });
    expect(normaliseEdgeMetadata({}, 0)).toBeNull();
    expect(normaliseEdgeMetadata(null, 0)).toBeNull();
  });
});

// ─── §7.1 badge + §7.2 ARIA ────────────────────────────────────────────

describe('§7.1 badge and §7.2 accessibility description', () => {
  it('pluralises the badge', () => {
    expect(referenceBadgeLabel(4)).toBe('4 references');
    expect(referenceBadgeLabel(1)).toBe('1 reference');
    expect(referenceBadgeLabel(20)).toBe('20 references');
    expect(referenceBadgeAriaLabel(4)).toBe('4 references to this reply');
  });

  it('produces the exact §7.2 sentence', () => {
    const description = referenceAriaDescription([
      { label: 'R1', text: 'Message A' },
      { label: 'R2', text: 'Message B' },
      { label: 'R3', text: 'Message C' },
      { label: 'R4', text: 'Message D' },
    ]);
    expect(description).toBe(
      'Multi-reference reply to 4 messages: R1 Message A, R2 Message B, R3 Message C, R4 Message D.',
    );
  });

  it('names every source for a 20-source selection', () => {
    const sources = Array.from({ length: 20 }, (_, i) => ({
      label: referenceSourceLabel(i),
      text: `Source ${i + 1}`,
    }));
    const description = referenceAriaDescription(sources);
    expect(description.startsWith('Multi-reference reply to 20 messages: R1 Source 1, ')).toBe(true);
    expect(description.endsWith('R20 Source 20.')).toBe(true);
    expect(description.split(',').length).toBe(20);
  });
});

// ─── §3.4 / §8.3 node metadata ─────────────────────────────────────────

describe('multi_reference node metadata', () => {
  const MERGE_METADATA = {
    version: 1,
    primarySourceId: '0191a8b2-7fff-7000-9000-000000000101',
    canonicalSourceIds: [
      '0191a8b2-7fff-7000-9000-000000000101',
      '0191a8b2-7fff-7000-9000-000000000202',
    ],
    isSyntheticMergePoint: true,
    branchSpan: {
      commonAncestorId: '0191a8b2-7fff-7000-9000-000000000001',
      sourceBranches: [
        {
          sourceId: '0191a8b2-7fff-7000-9000-000000000101',
          branchRootId: '0191a8b2-7fff-7000-9000-000000000010',
          distanceFromRoot: 4,
        },
        {
          sourceId: '0191a8b2-7fff-7000-9000-000000000202',
          branchRootId: '0191a8b2-7fff-7000-9000-000000000020',
          distanceFromRoot: 3,
        },
      ],
    },
    contextManifestHash: '91a2e5d22c17e5870f61ea6e9d501da80c2ac2735d15d5f3b6efb87c8c92856f',
    contextTokenBudget: 8192,
  };

  it('reads §8.3 metadata off a node', () => {
    const metadata = multiReferenceMetadataFromNode({ metadata: { multi_reference: MERGE_METADATA } });
    expect(metadata).not.toBeNull();
    expect(metadata!.canonicalSourceIds).toHaveLength(2);
    expect(metadata!.isSyntheticMergePoint).toBe(true);
    expect(metadata!.branchSpan!.commonAncestorId).toBe(
      '0191a8b2-7fff-7000-9000-000000000001',
    );
    expect(metadata!.contextTokenBudget).toBe(8192);
  });

  it('returns null for a lineage node and never throws on junk', () => {
    expect(multiReferenceMetadataFromNode({ metadata: {} })).toBeNull();
    expect(multiReferenceMetadataFromNode(null)).toBeNull();
    expect(normaliseMultiReferenceMetadata('nope')).toBeNull();
    expect(normaliseMultiReferenceMetadata({ canonicalSourceIds: null })).toBeNull();
  });

  it('builds the §7.1 node view with the §7.2 sentence', () => {
    const metadata = normaliseMultiReferenceMetadata(MERGE_METADATA)!;
    const view = buildReferenceNodeView({
      metadata,
      fallbackText: (id) =>
        id.endsWith('101') ? 'Message A' : id.endsWith('202') ? 'Message B' : '',
    });
    expect(view!.count).toBe(2);
    expect(view!.badge).toBe('2 references');
    expect(view!.ariaDescription).toBe(
      'Multi-reference reply to 2 messages: R1 Message A, R2 Message B.',
    );
    expect(view!.sources.map((s) => s.label)).toEqual(['R1', 'R2']);
    expect(buildReferenceNodeView({ metadata: null })).toBeNull();
  });
});

// ─── §9.3 wire ─────────────────────────────────────────────────────────

describe('§9.3 provenance wire normalisation', () => {
  it('normalises the documented body', () => {
    const context = normaliseReferenceContext({
      node_id: '0191a8b2-7fff-7000-9000-000000000301',
      tree_id: '0191a8b2-7fff-7000-9000-000000000001',
      parent_mode: 'multi_reference',
      primary_source_id: '0191a8b2-7fff-7000-9000-000000000101',
      context: {
        sources: [
          {
            source_label: 'R1',
            node_id: '0191a8b2-7fff-7000-9000-000000000101',
            content: 'first source body',
            truncated: false,
          },
        ],
        is_synthetic_merge_point: true,
        branch_span: { common_ancestor_id: '0191a8b2-7fff-7000-9000-000000000001', source_branches: [] },
        token_budget: 8192,
        tokens_used: 1460,
        manifest_hash: '91a2e5d22c17e5870f61ea6e9d501da80c2ac2735d15d5f3b6efb87c8c92856f',
      },
    });

    expect(context).not.toBeNull();
    expect(context!.sources).toHaveLength(1);
    expect(context!.tokenBudget).toBe(8192);
    expect(context!.tokensUsed).toBe(1460);
    expect(context!.isSyntheticMergePoint).toBe(true);
    expect(context!.sourceChangedSinceCreation).toBe(false);
  });

  it('treats null slices as empty and a missing context as null', () => {
    const context = normaliseReferenceContext({
      node_id: 'n',
      context: { sources: null, branch_span: null, token_budget: null },
    });
    expect(context!.sources).toEqual([]);
    expect(context!.branchSpan).toBeNull();
    expect(context!.tokenBudget).toBe(0);
    expect(normaliseReferenceContext({ node_id: 'n' })).toBeNull();
    expect(normaliseReferenceContext(null)).toBeNull();
  });

  it('requests the route the backend serves', () => {
    expect(referenceContextRequestPath('abc-123')).toBe('/nodes/abc-123/reference-context');
    expect(referenceContextRequestPath('a/b')).toBe('/nodes/a%2Fb/reference-context');
  });
});

// ─── §7.3 inspector view model ─────────────────────────────────────────

describe('§7.3 inspector view model', () => {
  const MERGE = '0191a8b2-7fff-7000-9000-000000000301';
  const SRC_A = '0191a8b2-7fff-7000-9000-000000000101';
  const SRC_B = '0191a8b2-7fff-7000-9000-000000000202';
  const ANCESTOR = '0191a8b2-7fff-7000-9000-000000000001';
  const BRANCH_LEFT = '0191a8b2-7fff-7000-9000-000000000010';
  const BRANCH_RIGHT = '0191a8b2-7fff-7000-9000-000000000020';

  const METADATA = {
    version: 1,
    primarySourceId: SRC_A,
    canonicalSourceIds: [SRC_A, SRC_B],
    isSyntheticMergePoint: true,
    branchSpan: {
      commonAncestorId: ANCESTOR,
      sourceBranches: [
        { sourceId: SRC_A, branchRootId: BRANCH_LEFT, distanceFromRoot: 4 },
        { sourceId: SRC_B, branchRootId: BRANCH_RIGHT, distanceFromRoot: 3 },
      ],
    },
    contextManifestHash: '91a2e5d22c17e5870f61ea6e9d501da80c2ac2735d15d5f3b6efb87c8c92856f',
    contextTokenBudget: 8192,
  };

  /** §9.1 preflight payload for the same selection. */
  const PREFLIGHT_SOURCES = [
    {
      node_id: SRC_B,
      source_label: 'R2',
      color_key: 'ref-1',
      branch_root_id: BRANCH_RIGHT,
      content_preview: 'Right branch evidence',
    },
    {
      node_id: SRC_A,
      source_label: 'R1',
      color_key: 'ref-6',
      branch_root_id: BRANCH_LEFT,
      content_preview: 'Left branch claim',
    },
  ];

  const resolveNode = (id: string) =>
    id === SRC_A
      ? { authorId: 'u1', authorLabel: 'Lexi', createdAt: '2026-07-22T11:50:00Z', content: 'Left branch claim' }
      : id === SRC_B
        ? { authorId: 'u2', authorLabel: 'Lexi', createdAt: '2026-07-22T11:55:00Z', content: 'Right branch evidence' }
        : null;

  it('orders sources canonically and keeps persisted colour/label', () => {
    const model = buildReferenceInspectorModel({
      nodeId: MERGE,
      metadata: normaliseMultiReferenceMetadata(METADATA),
      sources: PREFLIGHT_SOURCES,
      resolveNode,
    })!;

    // Canonical order (metadata), not payload order.
    expect(model.sources.map((s) => s.nodeId)).toEqual([SRC_A, SRC_B]);
    expect(model.sources.map((s) => s.label)).toEqual(['R1', 'R2']);
    expect(model.sources.map((s) => s.colorKey)).toEqual(['ref-6', 'ref-1']);
    expect(model.sources.map((s) => s.stroke)).toEqual(['#65A30D', '#059669']);
    expect(model.sourceCount).toBe(2);
    expect(model.badge).toBe('2 references');
    expect(model.ariaDescription).toBe(
      'Multi-reference reply to 2 messages: R1 Left branch claim, R2 Right branch evidence.',
    );
  });

  it('carries author, timestamp, branch root and preview per source', () => {
    const model = buildReferenceInspectorModel({
      nodeId: MERGE,
      metadata: normaliseMultiReferenceMetadata(METADATA),
      sources: PREFLIGHT_SOURCES,
      resolveNode,
    })!;

    expect(model.sources[0]).toMatchObject({
      nodeId: SRC_A,
      author: 'Lexi',
      createdAt: '2026-07-22T11:50:00Z',
      branchRootId: BRANCH_LEFT,
      preview: 'Left branch claim',
      resolved: true,
    });
    expect(model.sources[1].branchRootId).toBe(BRANCH_RIGHT);
  });

  it('marks the node a synthetic context merge point and groups by branch root', () => {
    const model = buildReferenceInspectorModel({
      nodeId: MERGE,
      metadata: normaliseMultiReferenceMetadata(METADATA),
      sources: PREFLIGHT_SOURCES,
      resolveNode,
    })!;

    expect(model.isSyntheticMergePoint).toBe(true);
    expect(model.branchSpanLabel).toBe('Context from 2 branches');
    expect(model.commonAncestorId).toBe(ANCESTOR);
    expect(model.branchGroups.map((g) => g.branchRootId)).toEqual([BRANCH_LEFT, BRANCH_RIGHT]);
    // §8.2: never "merged" / "resolved" / "synthesis".
    expect(model.branchSpanLabel).not.toMatch(/merg|resolv|synthesis/i);
  });

  it('does not claim a synthetic merge for a same-branch selection', () => {
    const model = buildReferenceInspectorModel({
      nodeId: MERGE,
      metadata: normaliseMultiReferenceMetadata({
        ...METADATA,
        isSyntheticMergePoint: false,
        branchSpan: {
          commonAncestorId: ANCESTOR,
          sourceBranches: [
            { sourceId: SRC_A, branchRootId: BRANCH_LEFT, distanceFromRoot: 1 },
            { sourceId: SRC_B, branchRootId: BRANCH_LEFT, distanceFromRoot: 2 },
          ],
        },
      }),
      sources: PREFLIGHT_SOURCES,
      resolveNode,
    })!;

    expect(model.isSyntheticMergePoint).toBe(false);
    expect(model.branchSpanLabel).toBeNull();
  });

  it('exposes the manifest hash, token allocation and truncation indicators', () => {
    const model = buildReferenceInspectorModel({
      nodeId: MERGE,
      metadata: normaliseMultiReferenceMetadata(METADATA),
      sources: [
        PREFLIGHT_SOURCES[0],
        { ...PREFLIGHT_SOURCES[1], truncated: true, omitted_tokens: 320 },
      ],
      branchSpan: {
        commonAncestorId: ANCESTOR,
        sourceBranches: [
          { sourceId: SRC_A, branchRootId: BRANCH_LEFT, distanceFromRoot: 4 },
          { sourceId: SRC_B, branchRootId: BRANCH_RIGHT, distanceFromRoot: 3 },
        ],
      },
      manifestHash: 'deadbeef'.repeat(8),
      tokenBudget: 8192,
      tokensUsed: 1460,
      resolveNode,
    })!;

    expect(model.manifestHash).toBe('deadbeef'.repeat(8));
    expect(model.tokenBudget).toBe(8192);
    expect(model.tokensUsed).toBe(1460);
    expect(model.truncatedCount).toBe(1);
    expect(model.omittedTokens).toBe(320);
    expect(model.sources[0].truncated).toBe(true);
  });

  it('works with metadata alone — no wire payload, no replica', () => {
    const model = buildReferenceInspectorModel({
      nodeId: MERGE,
      metadata: normaliseMultiReferenceMetadata(METADATA),
    })!;

    // The R# labels are canonical order's own; the text falls back to the
    // short node id so the sentence still names every source.
    expect(model.sources.map((s) => s.label)).toEqual(['R1', 'R2']);
    expect(model.sources.every((s) => s.resolved === false)).toBe(true);
    expect(model.sourceCount).toBe(2);
  });

  it('returns null when the node is not a reference reply', () => {
    expect(buildReferenceInspectorModel({ nodeId: MERGE })).toBeNull();
    expect(
      buildReferenceInspectorModel({ nodeId: MERGE, metadata: null, sources: null }),
    ).toBeNull();
  });
});

// ─── Edge identity helper ──────────────────────────────────────────────

describe('§7.2 hover highlight contract', () => {
  const base = { isReference: true, edgeId: 'e1', edgeSource: 'src-a' };

  it('leaves every edge neutral with no active highlight', () => {
    expect(referenceEdgeHighlightState({ ...base, highlight: null })).toEqual({
      selected: false,
      dimmed: false,
    });
    expect(referenceEdgeHighlightState({ ...base, highlight: { sourceId: null, edgeId: null } })).toEqual(
      { selected: false, dimmed: false },
    );
  });

  it('highlights the exact edge and dims the other convergence edges', () => {
    const highlight = { edgeId: 'e1' };
    expect(referenceEdgeHighlightState({ ...base, highlight })).toEqual({
      selected: true,
      dimmed: false,
    });
    expect(
      referenceEdgeHighlightState({ ...base, edgeId: 'e2', edgeSource: 'src-b', highlight }),
    ).toEqual({ selected: false, dimmed: true });
  });

  it('highlights every edge leaving the pointed-at source', () => {
    const highlight = { sourceId: 'src-a' };
    expect(referenceEdgeHighlightState({ ...base, highlight }).selected).toBe(true);
    expect(
      referenceEdgeHighlightState({ ...base, edgeId: 'e9', edgeSource: 'src-a', highlight }).selected,
    ).toBe(true);
    expect(
      referenceEdgeHighlightState({ ...base, edgeSource: 'src-b', highlight }).dimmed,
    ).toBe(true);
  });

  it('never restyles lineage edges', () => {
    expect(
      referenceEdgeHighlightState({
        isReference: false,
        edgeId: 'r1',
        edgeSource: 'src-a',
        highlight: { edgeId: 'e1' },
      }),
    ).toEqual({ selected: false, dimmed: false });
    // The existing collapse dimming still applies to them.
    expect(
      referenceEdgeHighlightState({
        isReference: false,
        edgeId: 'r1',
        edgeSource: 'src-a',
        highlight: null,
        collapsed: true,
      }).dimmed,
    ).toBe(true);
  });
});

describe('flowEdgeIsReference', () => {
  it('reads the persisted edge type from the edge data', () => {
    expect(flowEdgeIsReference({ type: 'referenceEdge', data: { edgeType: 'reference', isReference: true } })).toBe(true);
    expect(flowEdgeIsReference({ type: 'referenceEdge', data: { edgeType: 'reference' } })).toBe(true);
    // A reply edge that happens to be named referenceEdge must still lose to
    // its persisted type.
    expect(flowEdgeIsReference({ type: 'referenceEdge', data: { edgeType: 'reply' } })).toBe(false);
    expect(flowEdgeIsReference({ type: 'replyEdge', data: { edgeType: 'reply' } })).toBe(false);
  });

  it('falls back to the component name for pre-model edges', () => {
    expect(flowEdgeIsReference({ type: 'referenceEdge' })).toBe(true);
    expect(flowEdgeIsReference({ type: 'replyEdge' })).toBe(false);
    expect(flowEdgeIsReference({})).toBe(false);
  });
});
