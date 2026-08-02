/**
 * Unit tests — header context resolution (UI-03 header upgrade)
 *
 * The header names whatever the user is looking at and badges it with a
 * real node count. All of that is derived from the URL plus two fetched
 * lists, so it is tested here as pure logic — the component only paints
 * what these functions return.
 */

import { describe, it, expect } from 'vitest';
import type { TopicSummary } from '../../types/topic';
import {
  FALLBACK_TITLE,
  treeIdFromPath,
  resolveActiveTreeId,
  activeViewMode,
  viewModeHref,
  viewModeSubtitle,
  resolveHeaderContext,
  type HeaderTree,
} from '../headerContext';

const TREE_A = '6d94185a-e3af-4a2b-a6fe-efe9b67e4c38';
const TREE_B = 'b1655761-2d7f-4b3c-85d5-21396da15691';
const TOPIC_A = '0d0f0b1e-8d2c-4a1f-9f3b-7c2e5a9d1b04';

function topic(over: Partial<TopicSummary> = {}): TopicSummary {
  return {
    id: TOPIC_A,
    tree_id: TREE_A,
    root_node_id: 'root-1',
    title: 'Strategy',
    description: '',
    slug: 'strategy',
    status: 'active',
    node_count: 12,
    created_at: '2026-08-01T00:00:00Z',
    ...over,
  };
}

const TREES: HeaderTree[] = [
  { id: TREE_A, title: 'Q3 Planning', node_count: 42 },
  { id: TREE_B, title: 'Research Notes', node_count: 7 },
];

// ─── Route helpers ─────────────────────────────────────────────────────

describe('treeIdFromPath', () => {
  it('extracts the id from a canvas route', () => {
    expect(treeIdFromPath(`/tree/${TREE_A}`)).toBe(TREE_A);
  });

  it('ignores trailing segments and query strings', () => {
    expect(treeIdFromPath(`/tree/${TREE_A}/node/9`)).toBe(TREE_A);
    expect(treeIdFromPath(`/tree/${TREE_A}?focus=1`)).toBe(TREE_A);
  });

  it('decodes percent-encoded ids', () => {
    expect(treeIdFromPath('/tree/my%20tree')).toBe('my tree');
  });

  it('survives malformed encoding instead of throwing', () => {
    expect(treeIdFromPath('/tree/%E0%A4%A')).toBe('%E0%A4%A');
  });

  it('returns empty for non-canvas routes', () => {
    expect(treeIdFromPath('/trees')).toBe('');
    expect(treeIdFromPath('/nodes')).toBe('');
    expect(treeIdFromPath('/')).toBe('');
  });
});

describe('resolveActiveTreeId', () => {
  it('prefers the canvas route over every other signal', () => {
    expect(
      resolveActiveTreeId({
        pathname: `/tree/${TREE_A}`,
        treeParam: TREE_B,
        storedTreeId: TREE_B,
      }),
    ).toBe(TREE_A);
  });

  it('falls back to the ?tree= deep link', () => {
    expect(
      resolveActiveTreeId({
        pathname: '/topics',
        treeParam: TREE_B,
        storedTreeId: TREE_A,
      }),
    ).toBe(TREE_B);
  });

  it('falls back to the persisted tree', () => {
    expect(
      resolveActiveTreeId({
        pathname: '/nodes',
        treeParam: '',
        storedTreeId: TREE_A,
      }),
    ).toBe(TREE_A);
  });

  it('returns empty when nothing is known', () => {
    expect(
      resolveActiveTreeId({ pathname: '/', treeParam: '', storedTreeId: '' }),
    ).toBe('');
  });
});

// ─── View mode ─────────────────────────────────────────────────────────

describe('activeViewMode', () => {
  it('maps the canvas and tree list to Tree', () => {
    expect(activeViewMode(`/tree/${TREE_A}`)).toBe('tree');
    expect(activeViewMode('/trees')).toBe('tree');
  });

  it('maps the node list to Detail', () => {
    expect(activeViewMode('/nodes')).toBe('detail');
  });

  it('maps approvals to Merge', () => {
    expect(activeViewMode('/approvals')).toBe('merge');
  });

  it('highlights nothing outside the triad', () => {
    expect(activeViewMode('/')).toBeNull();
    expect(activeViewMode('/topics')).toBeNull();
    expect(activeViewMode('/cards')).toBeNull();
  });

  it('does not confuse a lookalike prefix for a view route', () => {
    expect(activeViewMode('/treehouse')).toBeNull();
    expect(activeViewMode('/nodescape')).toBeNull();
  });
});

describe('viewModeHref', () => {
  it('routes Tree to the canvas when a tree is known', () => {
    expect(viewModeHref('tree', TREE_A)).toBe(`/tree/${TREE_A}`);
  });

  it('routes Tree to the picker when no tree is resolved', () => {
    expect(viewModeHref('tree', '')).toBe('/trees');
  });

  it('encodes the tree id', () => {
    expect(viewModeHref('tree', 'my tree')).toBe('/tree/my%20tree');
  });

  it('routes Detail and Merge to their pages', () => {
    expect(viewModeHref('detail', TREE_A)).toBe('/nodes');
    expect(viewModeHref('merge', TREE_A)).toBe('/approvals');
  });
});

describe('viewModeSubtitle', () => {
  it('names the current view', () => {
    expect(viewModeSubtitle('tree')).toBe('Macro tree view');
    expect(viewModeSubtitle('detail')).toBe('Node detail view');
    expect(viewModeSubtitle('merge')).toBe('Merge review view');
  });

  it('defaults to the macro view off-triad', () => {
    expect(viewModeSubtitle(null)).toBe('Macro tree view');
  });
});

// ─── Context resolution ────────────────────────────────────────────────

describe('resolveHeaderContext', () => {
  const base = {
    pathname: '/topics',
    topicParam: '',
    treeId: '',
    topics: [] as TopicSummary[],
    trees: TREES,
  };

  it('prefers the active topic and its node count', () => {
    expect(
      resolveHeaderContext({
        ...base,
        topicParam: TOPIC_A,
        treeId: TREE_A,
        topics: [topic()],
      }),
    ).toEqual({ title: 'Strategy', count: 12, source: 'topic' });
  });

  it('only honours a topic on the topics route', () => {
    expect(
      resolveHeaderContext({
        ...base,
        pathname: '/nodes',
        topicParam: TOPIC_A,
        treeId: TREE_A,
        topics: [topic()],
      }),
    ).toEqual({ title: 'Q3 Planning', count: 42, source: 'tree' });
  });

  it('falls through to the tree when the topic id is stale', () => {
    expect(
      resolveHeaderContext({
        ...base,
        topicParam: 'no-such-topic',
        treeId: TREE_A,
        topics: [topic()],
      }),
    ).toEqual({ title: 'Q3 Planning', count: 42, source: 'tree' });
  });

  it('falls through to the tree while topics are still loading', () => {
    expect(
      resolveHeaderContext({ ...base, topicParam: TOPIC_A, treeId: TREE_B }),
    ).toEqual({ title: 'Research Notes', count: 7, source: 'tree' });
  });

  it('falls back to the static title when nothing resolves', () => {
    expect(resolveHeaderContext(base)).toEqual({
      title: FALLBACK_TITLE,
      count: null,
      source: 'fallback',
    });
  });

  it('falls back when the tree id is not in the loaded list', () => {
    expect(
      resolveHeaderContext({ ...base, treeId: 'ghost-tree' }),
    ).toEqual({ title: FALLBACK_TITLE, count: null, source: 'fallback' });
  });

  it('hides the badge rather than inventing a count', () => {
    const noCount = resolveHeaderContext({
      ...base,
      treeId: TREE_A,
      trees: [{ id: TREE_A, title: 'Q3 Planning' }],
    });
    expect(noCount.count).toBeNull();

    const topicNoCount = resolveHeaderContext({
      ...base,
      topicParam: TOPIC_A,
      treeId: TREE_A,
      topics: [{ ...topic(), node_count: undefined as unknown as number }],
    });
    expect(topicNoCount.count).toBeNull();
  });

  it('keeps a genuine zero count distinct from unknown', () => {
    expect(
      resolveHeaderContext({
        ...base,
        topicParam: TOPIC_A,
        topics: [topic({ node_count: 0 })],
      }).count,
    ).toBe(0);
  });

  it('ignores an untitled topic and uses the tree instead', () => {
    expect(
      resolveHeaderContext({
        ...base,
        topicParam: TOPIC_A,
        treeId: TREE_A,
        topics: [topic({ title: '' })],
      }),
    ).toEqual({ title: 'Q3 Planning', count: 42, source: 'tree' });
  });
});
