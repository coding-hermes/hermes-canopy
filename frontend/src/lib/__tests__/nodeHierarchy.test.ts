/**
 * Unit tests — node list hierarchy (UI-08)
 *
 * The Nodes page's whole claim is that you can see which node replied to
 * which. These tests pin the three ways that claim breaks in production
 * data rather than in a fixture: a parent that got filtered out, a
 * multi-parent synthesis node, and a genuine cycle from bad data. In all
 * three the requirement is the same — every node still renders exactly
 * once, because a node that silently disappears from a list page is
 * worse than one drawn at the wrong indent.
 */

import { describe, it, expect } from 'vitest';
import {
  buildHierarchy,
  filterHierarchy,
  rowRails,
  type HierarchyNode,
} from '../nodeHierarchy';

// ─── Fixtures ──────────────────────────────────────────────────────────

interface TestNode extends HierarchyNode {
  id: string;
  parentId: string | null;
  content: string;
}

function n(id: string, parentId: string | null, content = id): TestNode {
  return { id, parentId, content };
}

/**
 * The real demo tree's shape (verified live against
 * `GET /trees/9a7f97f3…/nodes`): one root, five flat children.
 */
const DEMO: TestNode[] = [
  n('019fb0bc', null, '# Welcome to Hermes Canopy'),
  n('019fb0c0', '019fb0bc', 'Child 1: Architecture'),
  n('019fb0c2a', '019fb0bc', 'Child #2: DAG node'),
  n('019fb0c2b', '019fb0bc', 'Child #3: DAG node'),
  n('019fb0c2c', '019fb0bc', 'Child #4: DAG node'),
  n('019fb0c2d', '019fb0bc', 'Child #5: DAG node'),
];

/**
 *   root
 *   ├── a
 *   │   ├── a1
 *   │   └── a2
 *   ├── b
 *   └── c
 *       └── c1
 */
const NESTED: TestNode[] = [
  n('root', null),
  n('a', 'root'),
  n('a1', 'a'),
  n('a2', 'a'),
  n('b', 'root'),
  n('c', 'root'),
  n('c1', 'c'),
];

const ids = (rows: readonly { id: string }[]) => rows.map((r) => r.id);
const depths = (rows: readonly { depth: number }[]) => rows.map((r) => r.depth);

// ─── Structure ─────────────────────────────────────────────────────────

describe('buildHierarchy', () => {
  it('nests children under their parent in a depth-first walk', () => {
    const rows = buildHierarchy(NESTED);
    expect(ids(rows)).toEqual(['root', 'a', 'a1', 'a2', 'b', 'c', 'c1']);
    expect(depths(rows)).toEqual([0, 1, 2, 2, 1, 1, 2]);
  });

  it('indents the demo tree one level under its root', () => {
    const rows = buildHierarchy(DEMO);
    expect(rows).toHaveLength(6);
    expect(rows[0]!.depth).toBe(0);
    expect(rows.slice(1).every((r) => r.depth === 1)).toBe(true);
    expect(rows[0]!.hasChildren).toBe(true);
    expect(rows.slice(1).every((r) => !r.hasChildren)).toBe(true);
  });

  it('preserves input (sequence_num) order among siblings', () => {
    const rows = buildHierarchy(DEMO);
    expect(ids(rows).slice(1)).toEqual([
      '019fb0c0',
      '019fb0c2a',
      '019fb0c2b',
      '019fb0c2c',
      '019fb0c2d',
    ]);
  });

  it('flags only the final child of each parent as last', () => {
    const rows = buildHierarchy(NESTED);
    const last = rows.filter((r) => r.isLastChild).map((r) => r.id);
    // root is the only root, a2 ends a's children, c1 ends c's, c ends root's.
    expect(last.sort()).toEqual(['a2', 'c', 'c1', 'root']);
  });

  it('returns an empty walk for an empty list', () => {
    expect(buildHierarchy([])).toEqual([]);
  });
});

// ─── Orphans ───────────────────────────────────────────────────────────

describe('buildHierarchy — orphans', () => {
  it('renders a node whose parent is absent at depth 0 rather than dropping it', () => {
    // 'a' points at a parent that is not in the list (filtered, deleted).
    const rows = buildHierarchy([n('a', 'missing'), n('b', null)]);
    expect(ids(rows).sort()).toEqual(['a', 'b']);
    expect(rows.find((r) => r.id === 'a')!.depth).toBe(0);
  });

  it('distinguishes an orphan from a genuine root', () => {
    const rows = buildHierarchy([n('a', 'missing'), n('b', null)]);
    expect(rows.find((r) => r.id === 'a')!.isOrphan).toBe(true);
    expect(rows.find((r) => r.id === 'b')!.isOrphan).toBe(false);
  });

  it('keeps an orphan\u2019s own children nested beneath it', () => {
    const rows = buildHierarchy([n('a', 'missing'), n('a1', 'a')]);
    expect(ids(rows)).toEqual(['a', 'a1']);
    expect(depths(rows)).toEqual([0, 1]);
  });

  it('treats a self-parenting node as an orphan instead of looping', () => {
    const rows = buildHierarchy([n('a', 'a')]);
    expect(ids(rows)).toEqual(['a']);
    expect(rows[0]!.depth).toBe(0);
    expect(rows[0]!.isOrphan).toBe(true);
  });
});

// ─── Bad data ──────────────────────────────────────────────────────────

describe('buildHierarchy — cycle and duplicate safety', () => {
  it('still renders every node in a cycle exactly once', () => {
    // a → b → a. Neither is a root, so only the sweep can reach them.
    const rows = buildHierarchy([n('a', 'b'), n('b', 'a')]);
    expect(ids(rows).sort()).toEqual(['a', 'b']);
    expect(rows).toHaveLength(2);
  });

  it('renders a multi-parent synthesis node once, not once per parent', () => {
    // The DAG allows this; `parentId` names only the primary parent, so
    // the extra linkage must not duplicate the row.
    const rows = buildHierarchy([
      n('root', null),
      n('a', 'root'),
      n('b', 'root'),
      n('synth', 'a'),
    ]);
    expect(ids(rows).filter((id) => id === 'synth')).toHaveLength(1);
  });

  it('ignores a duplicate id rather than rendering it twice', () => {
    const rows = buildHierarchy([n('root', null), n('root', null)]);
    expect(rows).toHaveLength(1);
  });

  it('emits every input node exactly once for any shape', () => {
    for (const list of [DEMO, NESTED, [n('a', 'b'), n('b', 'a')]]) {
      const rows = buildHierarchy(list);
      const unique = new Set(list.map((x) => x.id));
      expect(rows).toHaveLength(unique.size);
      expect(new Set(ids(rows))).toEqual(unique);
    }
  });
});

// ─── Guide lines ───────────────────────────────────────────────────────

describe('rowRails', () => {
  it('gives a root no connector at all', () => {
    const rows = buildHierarchy(NESTED);
    expect(rowRails(rows[0]!)).toEqual({ ancestors: [], elbow: null });
  });

  it('draws a tee on a middle child and an elbow on the last', () => {
    const rows = buildHierarchy(NESTED);
    const byId = new Map(rows.map((r) => [r.id, r]));
    expect(rowRails(byId.get('a')!).elbow).toBe('tee'); // b and c follow
    expect(rowRails(byId.get('c')!).elbow).toBe('end'); // last root child
  });

  it('continues an ancestor line only while that ancestor has siblings left', () => {
    const rows = buildHierarchy(NESTED);
    const byId = new Map(rows.map((r) => [r.id, r]));

    // a1 is at depth 2, so it has two ancestor columns: root's and a's.
    // root is the only root → nothing to continue in column 0. `a` still
    // has b and c after it → its column keeps a vertical line running
    // past a1 so a2/b/c stay visually attached to the same parent.
    expect(rowRails(byId.get('a1')!).ancestors).toEqual(['space', 'line']);

    // c1 sits under c, the last root child → nothing left below in
    // either column, so no line should be drawn through it.
    expect(rowRails(byId.get('c1')!).ancestors).toEqual(['space', 'space']);
  });

  it('emits exactly one ancestor segment per depth level', () => {
    for (const row of buildHierarchy(NESTED)) {
      expect(rowRails(row).ancestors).toHaveLength(row.depth);
      expect(row.ancestorLines).toHaveLength(row.depth);
    }
  });
});

// ─── Search ────────────────────────────────────────────────────────────

describe('filterHierarchy', () => {
  const hasText = (needle: string) => (x: TestNode) =>
    x.content.toLowerCase().includes(needle);

  it('keeps a match\u2019s ancestors visible so the hit stays in context', () => {
    const { rows } = filterHierarchy(NESTED, (x) => x.id === 'a1');
    expect(ids(rows)).toEqual(['root', 'a', 'a1']);
  });

  it('reports which rows actually matched vs. which are context', () => {
    const { matched } = filterHierarchy(NESTED, (x) => x.id === 'a1');
    expect([...matched]).toEqual(['a1']);
    expect(matched.has('root')).toBe(false);
    expect(matched.has('a')).toBe(false);
  });

  it('re-computes last-child geometry after siblings are filtered out', () => {
    // Only a1 survives under a, so it must become the last child even
    // though a2 followed it in the full tree.
    const { rows } = filterHierarchy(NESTED, (x) => x.id === 'a1');
    expect(rows.find((r) => r.id === 'a1')!.isLastChild).toBe(true);
    // ...and 'a' is now root's only surviving child.
    expect(rows.find((r) => r.id === 'a')!.isLastChild).toBe(true);
  });

  it('preserves indent depth for a surviving branch', () => {
    const { rows } = filterHierarchy(NESTED, (x) => x.id === 'c1');
    expect(ids(rows)).toEqual(['root', 'c', 'c1']);
    expect(depths(rows)).toEqual([0, 1, 2]);
  });

  it('finds the demo tree\u2019s children by content', () => {
    const { rows, matched } = filterHierarchy(DEMO, hasText('child #3'));
    expect([...matched]).toEqual(['019fb0c2b']);
    // The root comes along as context, indented structure intact.
    expect(ids(rows)).toEqual(['019fb0bc', '019fb0c2b']);
    expect(depths(rows)).toEqual([0, 1]);
  });

  it('returns nothing when no node matches', () => {
    const { rows, matched } = filterHierarchy(DEMO, hasText('nonexistent'));
    expect(rows).toEqual([]);
    expect(matched.size).toBe(0);
  });

  it('keeps everything when the predicate matches everything', () => {
    const { rows, matched } = filterHierarchy(NESTED, () => true);
    expect(ids(rows)).toEqual(ids(buildHierarchy(NESTED)));
    expect(matched.size).toBe(NESTED.length);
  });

  it('does not hang when the parent chain of a match is cyclic', () => {
    const rows = filterHierarchy(
      [n('a', 'b'), n('b', 'a')],
      (x) => x.id === 'a',
    );
    expect(ids(rows.rows).sort()).toEqual(['a', 'b']);
  });
});
