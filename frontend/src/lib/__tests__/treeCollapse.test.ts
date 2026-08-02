/**
 * Unit tests — collapse-state algebra (UI-04 branching tree canvas)
 *
 * The canvas keeps one Set of collapsed ids and derives everything else
 * from it. These tests pin the derivation, including the two cases that
 * bite in practice: a DAG with a multi-parent synthesis node, and a
 * collapsed id that outlives the node it referred to.
 */

import { describe, it, expect } from 'vitest';
import {
  buildChildMap,
  childrenOf,
  collapseSubtree,
  descendantsOf,
  expandSubtree,
  hiddenCountFor,
  hiddenNodeIds,
  isCollapsible,
  pruneCollapsed,
  summarizeCollapsed,
  toggleCollapsed,
  type ChildEdge,
} from '../treeCollapse';

/**
 *        root
 *       /  |  \
 *      a   b   c
 *     / \      |
 *   a1  a2     c1
 *              |
 *             c2
 */
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
const MAP = buildChildMap(EDGES);

describe('buildChildMap', () => {
  it('groups children under their parent in order', () => {
    expect(childrenOf(MAP, 'root')).toEqual(['a', 'b', 'c']);
    expect(childrenOf(MAP, 'a')).toEqual(['a1', 'a2']);
  });

  it('returns an empty array for leaves and unknown ids', () => {
    expect(childrenOf(MAP, 'b')).toEqual([]);
    expect(childrenOf(MAP, 'nope')).toEqual([]);
  });

  it('deduplicates repeated edges', () => {
    const map = buildChildMap([
      { source: 'p', target: 'k' },
      { source: 'p', target: 'k' },
    ]);
    expect(childrenOf(map, 'p')).toEqual(['k']);
  });

  it('skips malformed edges instead of throwing', () => {
    const map = buildChildMap([
      { source: '', target: 'x' },
      { source: 'y', target: '' },
      { source: 'p', target: 'k' },
    ] as ChildEdge[]);
    expect(childrenOf(map, 'p')).toEqual(['k']);
    expect(map.size).toBe(1);
  });

  it('handles an empty edge list', () => {
    expect(buildChildMap([]).size).toBe(0);
  });
});

describe('isCollapsible', () => {
  it('is true only for nodes with children', () => {
    expect(isCollapsible(MAP, 'root')).toBe(true);
    expect(isCollapsible(MAP, 'c1')).toBe(true);
    expect(isCollapsible(MAP, 'b')).toBe(false);
    expect(isCollapsible(MAP, 'c2')).toBe(false);
  });
});

describe('descendantsOf', () => {
  it('collects the whole subtree, excluding the node itself', () => {
    expect(descendantsOf(MAP, 'c')).toEqual(new Set(['c1', 'c2']));
    expect(descendantsOf(MAP, 'root')).toEqual(
      new Set(['a', 'b', 'c', 'a1', 'a2', 'c1', 'c2']),
    );
  });

  it('is empty for a leaf', () => {
    expect(descendantsOf(MAP, 'b').size).toBe(0);
  });

  it('handles a multi-parent (synthesis) node without duplicating it', () => {
    const dag = buildChildMap([
      { source: 'a', target: 's' },
      { source: 'b', target: 's' },
      { source: 'root', target: 'a' },
      { source: 'root', target: 'b' },
    ]);
    expect(descendantsOf(dag, 'root')).toEqual(new Set(['a', 'b', 's']));
  });

  it('terminates on a cycle instead of hanging', () => {
    const cyclic = buildChildMap([
      { source: 'x', target: 'y' },
      { source: 'y', target: 'z' },
      { source: 'z', target: 'x' },
    ]);
    expect(descendantsOf(cyclic, 'x')).toEqual(new Set(['y', 'z']));
  });

  it('terminates on a self-loop', () => {
    const loop = buildChildMap([{ source: 'x', target: 'x' }]);
    expect(descendantsOf(loop, 'x').size).toBe(0);
  });
});

describe('toggleCollapsed', () => {
  it('adds then removes', () => {
    const one = toggleCollapsed(new Set(), 'a');
    expect(one.has('a')).toBe(true);
    expect(toggleCollapsed(one, 'a').has('a')).toBe(false);
  });

  it('returns a new set (React state identity)', () => {
    const before = new Set(['a']);
    const after = toggleCollapsed(before, 'b');
    expect(after).not.toBe(before);
    expect(before.has('b')).toBe(false);
  });

  it('leaves other entries alone', () => {
    expect([...toggleCollapsed(new Set(['a', 'b']), 'c')].sort()).toEqual([
      'a',
      'b',
      'c',
    ]);
  });
});

describe('collapseSubtree / expandSubtree', () => {
  it('collapses the node and every collapsible descendant', () => {
    const next = collapseSubtree(new Set(), MAP, 'c');
    expect([...next].sort()).toEqual(['c', 'c1']);
  });

  it('does not collapse leaves — there is nothing to hide', () => {
    expect(collapseSubtree(new Set(), MAP, 'b').size).toBe(0);
  });

  it('expand undoes collapse of the same subtree', () => {
    const collapsed = collapseSubtree(new Set(), MAP, 'root');
    const expanded = expandSubtree(collapsed, MAP, 'root');
    expect(expanded.size).toBe(0);
  });

  it('expand is scoped — a sibling branch stays collapsed', () => {
    let s = collapseSubtree(new Set(), MAP, 'a');
    s = collapseSubtree(s, MAP, 'c');
    const after = expandSubtree(s, MAP, 'c');
    expect(after.has('a')).toBe(true);
    expect(after.has('c')).toBe(false);
    expect(after.has('c1')).toBe(false);
  });
});

describe('hiddenNodeIds', () => {
  it('hides descendants but never the collapsed node itself', () => {
    const hidden = hiddenNodeIds(MAP, new Set(['a']));
    expect(hidden).toEqual(new Set(['a1', 'a2']));
    expect(hidden.has('a')).toBe(false);
  });

  it('hides a nested collapsed node when its ancestor is collapsed too', () => {
    const hidden = hiddenNodeIds(MAP, new Set(['c', 'c1']));
    expect(hidden).toEqual(new Set(['c1', 'c2']));
  });

  it('is empty when nothing is collapsed', () => {
    expect(hiddenNodeIds(MAP, new Set()).size).toBe(0);
  });

  it('unions independent collapsed branches', () => {
    expect(hiddenNodeIds(MAP, new Set(['a', 'c']))).toEqual(
      new Set(['a1', 'a2', 'c1', 'c2']),
    );
  });

  it('ignores a collapsed id that is not in the graph', () => {
    expect(hiddenNodeIds(MAP, new Set(['ghost'])).size).toBe(0);
  });
});

describe('hiddenCountFor', () => {
  it('counts the full subtree', () => {
    expect(hiddenCountFor(MAP, 'c')).toBe(2);
    expect(hiddenCountFor(MAP, 'a')).toBe(2);
    expect(hiddenCountFor(MAP, 'b')).toBe(0);
  });
});

describe('pruneCollapsed', () => {
  it('drops ids that no longer exist', () => {
    const pruned = pruneCollapsed(new Set(['a', 'deleted']), ALL_IDS);
    expect([...pruned]).toEqual(['a']);
  });

  it('keeps every live id — collapse survives a re-layout', () => {
    const before = new Set(['a', 'c']);
    expect(pruneCollapsed(before, ALL_IDS)).toEqual(before);
  });

  it('accepts a Set as well as an array', () => {
    expect(pruneCollapsed(new Set(['a']), new Set(ALL_IDS)).has('a')).toBe(true);
  });

  it('empties out when the tree is gone', () => {
    expect(pruneCollapsed(new Set(['a', 'c']), []).size).toBe(0);
  });
});

describe('summarizeCollapsed', () => {
  it('reports branch and hidden-node counts for the canvas pill', () => {
    expect(summarizeCollapsed(MAP, new Set(['a', 'c']))).toEqual({
      branches: 2,
      hidden: 4,
    });
  });

  it('is all zeroes when nothing is collapsed', () => {
    expect(summarizeCollapsed(MAP, new Set())).toEqual({
      branches: 0,
      hidden: 0,
    });
  });
});
