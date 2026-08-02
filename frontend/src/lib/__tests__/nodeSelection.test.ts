/**
 * Unit tests — bulk-selection algebra (UI-08)
 *
 * Two behaviours here have real consequences and are the reason this is
 * a module rather than three inline `useState` updates:
 *
 *   - a selected id that outlives its node rides along into the next
 *     bulk DELETE. `pruneSelection` is what stops that, so it is tested
 *     against the case that produces it (a refetch after a delete).
 *   - the header checkbox is computed against VISIBLE rows, so a search
 *     cannot make "select all" claim rows the user cannot see.
 */

import { describe, it, expect } from 'vitest';
import {
  bulkActions,
  clearSelection,
  deselectAll,
  isBulkBarVisible,
  pruneSelection,
  selectAll,
  selectAllState,
  toggleAllVisible,
  toggleSelection,
} from '../nodeSelection';

const S = (...ids: string[]) => new Set(ids);
const sorted = (s: ReadonlySet<string>) => [...s].sort();

// ─── Toggling ──────────────────────────────────────────────────────────

describe('toggleSelection', () => {
  it('adds an unselected id', () => {
    expect(sorted(toggleSelection(S('a'), 'b'))).toEqual(['a', 'b']);
  });

  it('removes a selected id', () => {
    expect(sorted(toggleSelection(S('a', 'b'), 'a'))).toEqual(['b']);
  });

  it('returns a NEW set so React sees an identity change', () => {
    const before = S('a');
    const after = toggleSelection(before, 'b');
    expect(after).not.toBe(before);
    expect(sorted(before)).toEqual(['a']); // input untouched
  });
});

describe('selectAll / deselectAll / clearSelection', () => {
  it('selectAll is idempotent', () => {
    const once = selectAll(S(), ['a', 'b']);
    expect(sorted(selectAll(once, ['a', 'b']))).toEqual(['a', 'b']);
  });

  it('deselectAll leaves selections outside the given ids alone', () => {
    // A search hides 'c'; deselecting the visible rows must not silently
    // drop the hidden one the user had already checked.
    expect(sorted(deselectAll(S('a', 'b', 'c'), ['a', 'b']))).toEqual(['c']);
  });

  it('clearSelection empties everything', () => {
    expect(clearSelection().size).toBe(0);
  });

  it('ignores blank ids', () => {
    expect(selectAll(S(), ['', 'a']).size).toBe(1);
  });
});

// ─── Survival ──────────────────────────────────────────────────────────

describe('pruneSelection', () => {
  it('keeps a selection across a re-render with the same rows', () => {
    expect(sorted(pruneSelection(S('a', 'b'), ['a', 'b', 'c']))).toEqual([
      'a',
      'b',
    ]);
  });

  it('drops an id whose node is gone, so it cannot ride into a bulk delete', () => {
    // 'b' was deleted; the list refetched without it.
    expect(sorted(pruneSelection(S('a', 'b'), ['a', 'c']))).toEqual(['a']);
  });

  it('empties when every row disappears (tree switched)', () => {
    expect(pruneSelection(S('a', 'b'), []).size).toBe(0);
  });

  it('accepts a Set as well as an array of live ids', () => {
    expect(sorted(pruneSelection(S('a', 'b'), S('a')))).toEqual(['a']);
  });
});

// ─── Header checkbox ───────────────────────────────────────────────────

describe('selectAllState', () => {
  const visible = ['a', 'b', 'c'];

  it('is none when nothing is selected', () => {
    expect(selectAllState(S(), visible)).toBe('none');
  });

  it('is some when part of the visible set is selected', () => {
    expect(selectAllState(S('a'), visible)).toBe('some');
    expect(selectAllState(S('a', 'b'), visible)).toBe('some');
  });

  it('is all only when every visible row is selected', () => {
    expect(selectAllState(S('a', 'b', 'c'), visible)).toBe('all');
  });

  it('ignores selections that are not currently visible', () => {
    // 'z' is selected but filtered out — the header must not read "all".
    expect(selectAllState(S('a', 'z'), visible)).toBe('some');
    // ...and a full visible selection still reads "all" despite 'z'.
    expect(selectAllState(S('a', 'b', 'c', 'z'), visible)).toBe('all');
  });

  it('is none for an empty list rather than indeterminate', () => {
    expect(selectAllState(S(), [])).toBe('none');
    expect(selectAllState(S('a'), [])).toBe('none');
  });
});

describe('toggleAllVisible', () => {
  const visible = ['a', 'b', 'c'];

  it('selects the rest when partially selected', () => {
    expect(sorted(toggleAllVisible(S('a'), visible))).toEqual(['a', 'b', 'c']);
  });

  it('selects everything when nothing is selected', () => {
    expect(sorted(toggleAllVisible(S(), visible))).toEqual(['a', 'b', 'c']);
  });

  it('clears when everything is selected', () => {
    expect(toggleAllVisible(S('a', 'b', 'c'), visible).size).toBe(0);
  });

  it('preserves a hidden selection when clearing the visible ones', () => {
    expect(sorted(toggleAllVisible(S('a', 'b', 'c', 'z'), visible))).toEqual([
      'z',
    ]);
  });
});

// ─── Bulk bar ──────────────────────────────────────────────────────────

describe('isBulkBarVisible', () => {
  it('is hidden with nothing selected and shown with one', () => {
    expect(isBulkBarVisible(S())).toBe(false);
    expect(isBulkBarVisible(S('a'))).toBe(true);
  });
});

describe('bulkActions', () => {
  it('offers delete, merge and tag', () => {
    expect(bulkActions(2).map((a) => a.id).sort()).toEqual([
      'delete',
      'merge',
      'tag',
    ]);
  });

  it('enables delete — DELETE /nodes/{id} exists and is already wired', () => {
    const del = bulkActions(3).find((a) => a.id === 'delete')!;
    expect(del.enabled).toBe(true);
    expect(del.destructive).toBe(true);
    expect(del.reason).toBeNull();
  });

  it('disables delete with a reason when nothing is selected', () => {
    const del = bulkActions(0).find((a) => a.id === 'delete')!;
    expect(del.enabled).toBe(false);
    expect(del.reason).toBeTruthy();
  });

  it('disables merge and tag — no endpoint backs them', () => {
    for (const id of ['merge', 'tag'] as const) {
      const action = bulkActions(3).find((a) => a.id === id)!;
      expect(action.enabled).toBe(false);
      // A disabled control must say why, not just grey out.
      expect(action.reason).toBeTruthy();
      expect(action.destructive).toBe(false);
    }
  });

  it('never marks merge or tag enabled at any selection size', () => {
    for (const n of [0, 1, 2, 50]) {
      const enabled = bulkActions(n)
        .filter((a) => a.enabled)
        .map((a) => a.id);
      expect(enabled).not.toContain('merge');
      expect(enabled).not.toContain('tag');
    }
  });

  it('treats a non-finite count as zero rather than enabling delete', () => {
    expect(bulkActions(NaN).find((a) => a.id === 'delete')!.enabled).toBe(false);
  });
});
