/**
 * Unit tests — reply-count derivation (UI-04 branching tree canvas)
 *
 * The badge on each card must come from the graph, never from a constant.
 * These tests pin that the count is *direct* replies (not descendants),
 * that a server payload can override a lagging local replica, and that a
 * leaf renders no badge at all.
 */

import { describe, it, expect } from 'vitest';
import { buildChildMap, type ChildEdge } from '../treeCollapse';
import {
  deriveReplyCounts,
  deriveSubtreeSize,
  mergeReplyCounts,
  replyBadgeAriaLabel,
  replyBadgeLabel,
  replyCountFor,
  type NodeCountSource,
} from '../replyCounts';

const EDGES: ChildEdge[] = [
  { source: 'root', target: 'a' },
  { source: 'root', target: 'b' },
  { source: 'root', target: 'c' },
  { source: 'a', target: 'a1' },
  { source: 'a', target: 'a2' },
  { source: 'c', target: 'c1' },
  { source: 'c1', target: 'c2' },
];

const ALL_IDS = ['root', 'a', 'b', 'c', 'a1', 'a2', 'c1', 'c2'];

describe('deriveReplyCounts', () => {
  it('counts direct replies, not the whole subtree', () => {
    const counts = deriveReplyCounts(EDGES, ALL_IDS);
    // root has 3 direct children even though 7 nodes sit beneath it
    expect(counts.get('root')).toBe(3);
    expect(counts.get('a')).toBe(2);
    expect(counts.get('c')).toBe(1);
  });

  it('records leaves as 0 when their ids are supplied', () => {
    const counts = deriveReplyCounts(EDGES, ALL_IDS);
    expect(counts.get('b')).toBe(0);
    expect(counts.get('c2')).toBe(0);
  });

  it('distinguishes "leaf" from "unknown"', () => {
    const counts = deriveReplyCounts(EDGES, ['b']);
    expect(counts.get('b')).toBe(0);
    expect(counts.has('mystery')).toBe(false);
  });

  it('works with no node id list at all', () => {
    const counts = deriveReplyCounts(EDGES);
    expect(counts.get('root')).toBe(3);
    expect(counts.has('b')).toBe(false);
  });

  it('is empty for an empty graph', () => {
    expect(deriveReplyCounts([], []).size).toBe(0);
  });

  it('counts a multi-parent child once per parent', () => {
    const counts = deriveReplyCounts([
      { source: 'a', target: 's' },
      { source: 'b', target: 's' },
    ]);
    expect(counts.get('a')).toBe(1);
    expect(counts.get('b')).toBe(1);
  });

  it('does not double-count duplicate edges', () => {
    const counts = deriveReplyCounts([
      { source: 'a', target: 'x' },
      { source: 'a', target: 'x' },
    ]);
    expect(counts.get('a')).toBe(1);
  });
});

describe('replyCountFor', () => {
  it('reads a known count', () => {
    expect(replyCountFor(deriveReplyCounts(EDGES, ALL_IDS), 'a')).toBe(2);
  });

  it('defaults unknown nodes to 0', () => {
    expect(replyCountFor(new Map(), 'nope')).toBe(0);
  });
});

describe('deriveSubtreeSize', () => {
  it('counts all descendants', () => {
    const map = buildChildMap(EDGES);
    expect(deriveSubtreeSize(map, 'root')).toBe(7);
    expect(deriveSubtreeSize(map, 'c')).toBe(2);
    expect(deriveSubtreeSize(map, 'b')).toBe(0);
  });
});

describe('mergeReplyCounts', () => {
  const derived = deriveReplyCounts(EDGES, ALL_IDS);

  it('server counts win over the local replica', () => {
    const server: NodeCountSource[] = [{ id: 'a', childCount: 9 }];
    expect(mergeReplyCounts(derived, server).get('a')).toBe(9);
  });

  it('leaves untouched nodes on the derived value', () => {
    const merged = mergeReplyCounts(derived, [{ id: 'a', childCount: 9 }]);
    expect(merged.get('root')).toBe(3);
  });

  it('ignores rows with no childCount', () => {
    expect(mergeReplyCounts(derived, [{ id: 'a' }]).get('a')).toBe(2);
  });

  it('ignores negative and non-finite counts', () => {
    const merged = mergeReplyCounts(derived, [
      { id: 'a', childCount: -3 },
      { id: 'c', childCount: Number.NaN },
      { id: 'root', childCount: Number.POSITIVE_INFINITY },
    ]);
    expect(merged.get('a')).toBe(2);
    expect(merged.get('c')).toBe(1);
    expect(merged.get('root')).toBe(3);
  });

  it('floors fractional counts', () => {
    expect(
      mergeReplyCounts(derived, [{ id: 'a', childCount: 4.8 }]).get('a'),
    ).toBe(4);
  });

  it('adds nodes the local replica has not seen yet', () => {
    const merged = mergeReplyCounts(derived, [{ id: 'remote', childCount: 2 }]);
    expect(merged.get('remote')).toBe(2);
  });

  it('does not mutate the input map', () => {
    mergeReplyCounts(derived, [{ id: 'a', childCount: 9 }]);
    expect(derived.get('a')).toBe(2);
  });

  it('survives a malformed payload', () => {
    const merged = mergeReplyCounts(derived, [
      null as unknown as NodeCountSource,
      { id: 42 as unknown as string, childCount: 1 },
    ]);
    expect(merged.get('a')).toBe(2);
  });
});

describe('replyBadgeLabel', () => {
  it('renders a positive count', () => {
    expect(replyBadgeLabel(3)).toBe('3');
  });

  it('hides the badge on a leaf', () => {
    expect(replyBadgeLabel(0)).toBeNull();
  });

  it('hides the badge on nonsense input', () => {
    expect(replyBadgeLabel(-1)).toBeNull();
    expect(replyBadgeLabel(Number.NaN)).toBeNull();
  });

  it('caps at 99+', () => {
    expect(replyBadgeLabel(100)).toBe('99+');
    expect(replyBadgeLabel(99)).toBe('99');
  });
});

describe('replyBadgeAriaLabel', () => {
  it('singularises one reply', () => {
    expect(replyBadgeAriaLabel(1)).toBe('1 reply');
  });

  it('pluralises everything else', () => {
    expect(replyBadgeAriaLabel(0)).toBe('0 replies');
    expect(replyBadgeAriaLabel(4)).toBe('4 replies');
  });

  it('clamps negatives', () => {
    expect(replyBadgeAriaLabel(-2)).toBe('0 replies');
  });
});
