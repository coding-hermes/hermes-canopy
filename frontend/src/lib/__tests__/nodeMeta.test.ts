/**
 * Unit tests — node card metadata derivation (UI-05 node card redesign)
 *
 * These helpers stand between a raw API record and what a user reads on
 * the Nodes page, and every one of them has a failure mode that reaches
 * the screen: a base64 blob rendered as a topic name, "User 00000000" on
 * every card in a tree, "0 replies" noise on leaves, or a `#` pill with
 * nothing after it.
 *
 * The metadata cases are anchored on the REAL wire format — the Go
 * handler marshals the `metadata` column (`[]byte`) as a base64 string,
 * so `"e30="` is what an empty metadata field actually looks like.
 */

import { describe, it, expect } from 'vitest';
import {
  formatNodeMeta,
  formatTimeAgo,
  indexTopicTitles,
  isUuidLike,
  nodeAuthorNames,
  nodeCardAriaLabel,
  nodeTypeLabel,
  parseNodeMetadata,
  parseTopicFromNode,
  shortAuthorLabel,
  slugifyTopic,
  topicPillLabel,
  type NodeMetaSource,
} from '../nodeMeta';

// ─── Fixtures ──────────────────────────────────────────────────────────

const DEV_USER = '00000000-0000-0000-0000-000000000001';

/** Base64 of a JSON object — exactly what canopyd puts on the wire. */
function b64(value: unknown): string {
  return btoa(JSON.stringify(value));
}

function node(overrides: Partial<NodeMetaSource> = {}): NodeMetaSource {
  return {
    id: '019fbf2d-13d9-714b-9077-735abfc6dc00',
    authorId: DEV_USER,
    authorDisplayName: '',
    content: 'Kickoff message for the demo tree.',
    nodeType: 'message',
    metadata: 'e30=',
    depth: 0,
    childCount: 0,
    createdAt: '2026-08-01T16:13:50.297429-05:00',
    editedAt: null,
    ...overrides,
  };
}

// ─── Author identity ───────────────────────────────────────────────────

describe('isUuidLike', () => {
  it('recognises the ids the API actually returns', () => {
    expect(isUuidLike(DEV_USER)).toBe(true);
    expect(isUuidLike('019FBF2D-13D9-714B-9077-735ABFC6DC00')).toBe(true);
  });

  it('rejects readable ids so they keep their humanised name', () => {
    expect(isUuidLike('demo_user_1')).toBe(false);
    expect(isUuidLike('local')).toBe(false);
    expect(isUuidLike('')).toBe(false);
  });
});

describe('shortAuthorLabel', () => {
  it('keys on the tail, which is what distinguishes seeded uuids', () => {
    expect(shortAuthorLabel(DEV_USER)).toBe('User 0001');
  });

  it('gives colliding-prefix uuids distinct labels', () => {
    const a = shortAuthorLabel('019fbf2d-13d9-714b-9077-735abfc6dc00');
    const b = shortAuthorLabel('019fbf2d-13d9-714b-9077-735abfc6de11');
    expect(a).not.toBe(b);
  });
});

describe('nodeAuthorNames', () => {
  it('prefers the display name the API supplied', () => {
    const names = nodeAuthorNames([
      node({ authorId: 'u-1', authorDisplayName: 'Sarah Chen' }),
    ]);
    expect(names.get('u-1')).toBe('Sarah Chen');
  });

  it('labels a bare uuid author rather than leaving it to be humanised', () => {
    // Without this the UI-04 humaniser renders "00000000 0000 0000".
    expect(nodeAuthorNames([node()]).get(DEV_USER)).toBe('User 0001');
  });

  it('leaves readable ids alone so the humaniser can name them', () => {
    const names = nodeAuthorNames([node({ authorId: 'demo_user_1' })]);
    expect(names.has('demo_user_1')).toBe(false);
  });

  it('first non-empty display name wins over a later blank one', () => {
    const names = nodeAuthorNames([
      node({ authorId: 'u-1', authorDisplayName: 'Sarah Chen' }),
      node({ authorId: 'u-1', authorDisplayName: '' }),
    ]);
    expect(names.get('u-1')).toBe('Sarah Chen');
  });

  it('ignores empty author ids without throwing', () => {
    expect(nodeAuthorNames([node({ authorId: '' })]).size).toBe(0);
    expect(nodeAuthorNames([]).size).toBe(0);
  });
});

// ─── Metadata decoding ─────────────────────────────────────────────────

describe('parseNodeMetadata', () => {
  it('decodes the base64 the Go handler emits', () => {
    expect(parseNodeMetadata(b64({ topic: 'roadmap' }))).toEqual({
      topic: 'roadmap',
    });
  });

  it('treats "e30=" (empty object) as empty', () => {
    expect(parseNodeMetadata('e30=')).toEqual({});
  });

  it('accepts an already-decoded object', () => {
    expect(parseNodeMetadata({ topic: 'roadmap' })).toEqual({
      topic: 'roadmap',
    });
  });

  it('accepts a raw JSON string', () => {
    expect(parseNodeMetadata('{"topic":"roadmap"}')).toEqual({
      topic: 'roadmap',
    });
  });

  it('returns an empty record for nullish or non-object payloads', () => {
    expect(parseNodeMetadata(null)).toEqual({});
    expect(parseNodeMetadata(undefined)).toEqual({});
    expect(parseNodeMetadata('')).toEqual({});
    expect(parseNodeMetadata(42)).toEqual({});
    expect(parseNodeMetadata([1, 2, 3])).toEqual({});
  });

  it('never throws on a malformed blob — one bad row must not blank the page', () => {
    expect(() => parseNodeMetadata('!!!not-base64!!!')).not.toThrow();
    expect(parseNodeMetadata('!!!not-base64!!!')).toEqual({});
    expect(parseNodeMetadata(btoa('not json at all'))).toEqual({});
    expect(parseNodeMetadata(b64([1, 2]))).toEqual({});
  });
});

// ─── Topic pill ────────────────────────────────────────────────────────

describe('slugifyTopic', () => {
  it('lower-cases and hyphenates', () => {
    expect(slugifyTopic('Data Backfill Plan')).toBe('data-backfill-plan');
  });

  it('collapses punctuation and trims stray hyphens', () => {
    expect(slugifyTopic('  Risks & Constraints! ')).toBe('risks-constraints');
  });

  it('returns an empty string when nothing survives', () => {
    expect(slugifyTopic('   ')).toBe('');
    expect(slugifyTopic('###')).toBe('');
  });
});

describe('parseTopicFromNode', () => {
  it('renders no pill when the node carries no topic', () => {
    expect(parseTopicFromNode(node())).toBeNull();
  });

  it('reads a topic slug out of base64 metadata', () => {
    const ref = parseTopicFromNode(
      node({ metadata: b64({ topic: 'Data Backfill Plan' }) }),
    );
    expect(ref?.slug).toBe('data-backfill-plan');
    expect(ref?.label).toBe('Data Backfill Plan');
  });

  it('accepts the snake_case key the Go side would write', () => {
    expect(
      parseTopicFromNode(node({ metadata: b64({ topic_slug: 'roadmap' }) }))
        ?.slug,
    ).toBe('roadmap');
  });

  it('resolves a topic id to its real title when the titles are known', () => {
    const titles = new Map([['t-1', 'Market Research']]);
    const ref = parseTopicFromNode(
      node({ metadata: b64({ topicId: 't-1' }) }),
      titles,
    );
    expect(ref?.label).toBe('Market Research');
    expect(ref?.slug).toBe('market-research');
    expect(ref?.id).toBe('t-1');
  });

  it('falls back to the raw id when no title is known', () => {
    const ref = parseTopicFromNode(node({ metadata: b64({ topicId: 't-9' }) }));
    expect(ref?.id).toBe('t-9');
    expect(ref?.slug).toBe('t-9');
  });

  it('reads an object-shaped topic reference', () => {
    const ref = parseTopicFromNode(
      node({
        metadata: b64({ topic: { id: 't-2', title: 'Launch Plan' } }),
      }),
    );
    expect(ref).toEqual({ id: 't-2', slug: 'launch-plan', label: 'Launch Plan' });
  });

  it('takes the first entry of a topics array', () => {
    const ref = parseTopicFromNode(
      node({ metadata: b64({ topics: [{ slug: 'roadmap', title: 'Roadmap' }] }) }),
    );
    expect(ref?.slug).toBe('roadmap');
  });

  it('falls back to an inline #reference in the body', () => {
    const ref = parseTopicFromNode(
      node({ content: 'Check the ETL approach in #data-pipeline before we ship.' }),
    );
    expect(ref?.slug).toBe('data-pipeline');
  });

  it('does not mistake a markdown heading for a topic reference', () => {
    // Seeded content starts "# Strategy" — a heading, not a #reference.
    expect(parseTopicFromNode(node({ content: '# Strategy\n\nRoot node.' }))).toBeNull();
  });

  it('ignores a metadata topic that slugifies to nothing', () => {
    expect(parseTopicFromNode(node({ metadata: b64({ topic: '###' }) }))).toBeNull();
  });

  it('metadata wins over an inline reference', () => {
    const ref = parseTopicFromNode(
      node({
        metadata: b64({ topic: 'roadmap' }),
        content: 'see #other-topic',
      }),
    );
    expect(ref?.slug).toBe('roadmap');
  });
});

describe('topicPillLabel', () => {
  it('renders exactly one leading hash', () => {
    expect(topicPillLabel({ id: null, slug: 'roadmap', label: 'Roadmap' })).toBe(
      '#roadmap',
    );
  });
});

describe('indexTopicTitles', () => {
  it('indexes by id and by slug so either reference form resolves', () => {
    const index = indexTopicTitles([
      { id: 't-1', title: 'Market Research', slug: 'market-research' },
    ]);
    expect(index.get('t-1')).toBe('Market Research');
    expect(index.get('market-research')).toBe('Market Research');
  });

  it('derives a slug when the payload omits one', () => {
    const index = indexTopicTitles([{ id: 't-2', title: 'Risks & Constraints' }]);
    expect(index.get('risks-constraints')).toBe('Risks & Constraints');
  });

  it('skips titleless topics', () => {
    expect(indexTopicTitles([{ id: 't-3', title: '  ' }]).size).toBe(0);
  });
});

// ─── Meta row ──────────────────────────────────────────────────────────

describe('nodeTypeLabel', () => {
  it('maps the known types', () => {
    expect(nodeTypeLabel('message')).toBe('Message');
    expect(nodeTypeLabel('synthesis')).toBe('Synthesis');
    expect(nodeTypeLabel('topic')).toBe('Topic');
  });

  it('passes an unknown type through rather than blanking it', () => {
    expect(nodeTypeLabel('quantum')).toBe('quantum');
  });
});

describe('formatNodeMeta', () => {
  it('phrases depth and replies for a branching node', () => {
    const meta = formatNodeMeta(node({ depth: 2, childCount: 3 }));
    expect(meta.depthLabel).toBe('Depth 2');
    expect(meta.replyLabel).toBe('3 replies');
  });

  it('singularises a lone reply', () => {
    expect(formatNodeMeta(node({ childCount: 1 })).replyLabel).toBe('1 reply');
  });

  it('omits the reply label on a leaf instead of saying "0 replies"', () => {
    expect(formatNodeMeta(node({ childCount: 0 })).replyLabel).toBeNull();
  });

  it('flags edited nodes', () => {
    expect(formatNodeMeta(node()).edited).toBe(false);
    expect(formatNodeMeta(node({ editedAt: '2026-08-01T17:00:00Z' })).edited).toBe(
      true,
    );
  });

  it('keeps the short id for aria/title only', () => {
    expect(formatNodeMeta(node()).shortId).toBe('019fbf2d');
  });

  it('survives a record with non-finite counters', () => {
    const meta = formatNodeMeta(
      node({ depth: NaN, childCount: NaN }),
    );
    expect(meta.depthLabel).toBe('Depth 0');
    expect(meta.replyLabel).toBeNull();
  });
});

// ─── Timestamp ─────────────────────────────────────────────────────────

describe('formatTimeAgo', () => {
  const now = Date.parse('2026-08-01T12:00:00Z');

  it('renders each magnitude', () => {
    expect(formatTimeAgo('2026-08-01T11:59:30Z', now)).toBe('30s ago');
    expect(formatTimeAgo('2026-08-01T11:45:00Z', now)).toBe('15m ago');
    expect(formatTimeAgo('2026-08-01T09:00:00Z', now)).toBe('3h ago');
    expect(formatTimeAgo('2026-07-29T12:00:00Z', now)).toBe('3d ago');
  });

  it('returns empty rather than the literal "Invalid Date"', () => {
    expect(formatTimeAgo('not-a-date', now)).toBe('');
    expect(formatTimeAgo('', now)).toBe('');
  });

  it('does not render a negative age for clock skew', () => {
    expect(formatTimeAgo('2026-08-01T12:00:05Z', now)).toBe('just now');
  });
});

// ─── Accessible label ──────────────────────────────────────────────────

describe('nodeCardAriaLabel', () => {
  it('names the node for assistive tech, id included', () => {
    const label = nodeCardAriaLabel(
      node({ depth: 1, childCount: 2 }),
      'Sarah Chen',
    );
    expect(label).toContain('Message by Sarah Chen');
    expect(label).toContain('Depth 1');
    expect(label).toContain('2 replies');
    expect(label).toContain('id 019fbf2d');
  });

  it('omits the reply and edited clauses when they do not apply', () => {
    const label = nodeCardAriaLabel(node(), 'You');
    expect(label).not.toContain('replies');
    expect(label).not.toContain('edited');
  });

  it('announces an edited node', () => {
    const label = nodeCardAriaLabel(
      node({ editedAt: '2026-08-01T17:00:00Z' }),
      'You',
    );
    expect(label).toContain('edited');
  });
});
