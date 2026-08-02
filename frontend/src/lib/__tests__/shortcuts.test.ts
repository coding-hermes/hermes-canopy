/**
 * Unit tests — keyboard shortcut algebra (UI-07)
 *
 * The whole point of putting this layer in `lib/` is that its risky part
 * is a *decision*, not a rendering: a single-key shortcut that fires
 * while the user is typing eats their message. That guard, the registry
 * integrity that keeps the help overlay honest, and the j/k/h/l target
 * maths are all pinned here.
 */

import { describe, it, expect } from 'vitest';
import {
  MERGE_ROUTE,
  SHORTCUTS,
  buildParentMap,
  drillInTarget,
  drillOutTarget,
  nextFocusIndex,
  resolveShortcut,
  shortcutForKey,
  shortcutsForScope,
  shouldIgnoreShortcut,
  type ShortcutEventLike,
} from '../shortcuts';

// ─── Registry integrity ────────────────────────────────────────────────

describe('SHORTCUTS registry', () => {
  it('binds every key exactly once', () => {
    const keys = SHORTCUTS.map((s) => s.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it('maps every key the task specified', () => {
    const keys = SHORTCUTS.map((s) => s.key);
    for (const key of ['j', 'k', 'h', 'l', 'm', '?']) {
      expect(keys).toContain(key);
    }
  });

  it('gives every shortcut a non-empty label and description', () => {
    for (const shortcut of SHORTCUTS) {
      expect(shortcut.label.length).toBeGreaterThan(0);
      expect(shortcut.description.length).toBeGreaterThan(0);
    }
  });

  it('binds every action exactly once, so no key is dead', () => {
    const actions = SHORTCUTS.map((s) => s.action);
    expect(new Set(actions).size).toBe(actions.length);
  });

  it('uses only single-character keys — no modifier chords here', () => {
    for (const shortcut of SHORTCUTS) {
      expect(shortcut.key.length).toBe(1);
    }
  });

  it('splits into tree and global scopes that cover the whole registry', () => {
    const tree = shortcutsForScope('tree');
    const global = shortcutsForScope('global');
    expect(tree.length + global.length).toBe(SHORTCUTS.length);
    expect(tree.map((s) => s.key)).toEqual(['j', 'k', 'h', 'l']);
    expect(global.map((s) => s.key)).toEqual(['m', '?']);
  });

  it('points the merge action at the UI-03 Merge view route', () => {
    expect(MERGE_ROUTE).toBe('/approvals');
  });
});

describe('shortcutForKey', () => {
  it('finds a bound key', () => {
    expect(shortcutForKey('j')?.action).toBe('navigateNext');
    expect(shortcutForKey('?')?.action).toBe('toggleHelp');
  });

  it('returns null for an unbound key', () => {
    expect(shortcutForKey('z')).toBeNull();
    expect(shortcutForKey('Enter')).toBeNull();
  });
});

// ─── Typing guard ──────────────────────────────────────────────────────

describe('shouldIgnoreShortcut — text entry targets', () => {
  const cases: Array<[string, ShortcutEventLike['target']]> = [
    ['input', { tagName: 'INPUT' }],
    ['textarea', { tagName: 'TEXTAREA' }],
    ['select', { tagName: 'SELECT' }],
    ['option', { tagName: 'OPTION' }],
    ['contentEditable div', { tagName: 'DIV', isContentEditable: true }],
    ['contenteditable="true" attribute', { tagName: 'DIV', contentEditableAttr: 'true' }],
    ['valueless contenteditable attribute', { tagName: 'DIV', contentEditableAttr: '' }],
    [
      'span nested inside an editable region',
      { tagName: 'SPAN', contentEditableAttr: 'true' },
    ],
    ['role=textbox', { tagName: 'DIV', role: 'textbox' }],
    ['role=searchbox', { tagName: 'DIV', role: 'searchbox' }],
    ['role=combobox', { tagName: 'DIV', role: 'combobox' }],
  ];

  for (const [name, target] of cases) {
    it(`ignores a bare key typed into a ${name}`, () => {
      expect(shouldIgnoreShortcut({ key: 'j', target })).toBe(true);
    });
  }

  it('is case-insensitive about the tag name', () => {
    expect(shouldIgnoreShortcut({ key: 'j', target: { tagName: 'textarea' } })).toBe(
      true,
    );
  });

  it('does NOT ignore keys pressed on non-text elements', () => {
    expect(shouldIgnoreShortcut({ key: 'j', target: { tagName: 'DIV' } })).toBe(false);
    expect(shouldIgnoreShortcut({ key: 'j', target: { tagName: 'BODY' } })).toBe(false);
    expect(shouldIgnoreShortcut({ key: 'j', target: { tagName: 'BUTTON' } })).toBe(
      false,
    );
  });

  it('does not ignore when there is no target at all', () => {
    expect(shouldIgnoreShortcut({ key: 'j' })).toBe(false);
    expect(shouldIgnoreShortcut({ key: 'j', target: null })).toBe(false);
  });

  it('treats contentEditable=false as ordinary content', () => {
    expect(
      shouldIgnoreShortcut({
        key: 'j',
        target: { tagName: 'DIV', isContentEditable: false },
      }),
    ).toBe(false);
  });

  it('respects an explicit contenteditable="false" opt-out', () => {
    expect(
      shouldIgnoreShortcut({
        key: 'j',
        target: { tagName: 'DIV', contentEditableAttr: 'false' },
      }),
    ).toBe(false);
  });
});

describe('shouldIgnoreShortcut — modifiers', () => {
  it('ignores ctrl / meta / alt combinations', () => {
    expect(shouldIgnoreShortcut({ key: 'm', ctrlKey: true })).toBe(true);
    expect(shouldIgnoreShortcut({ key: 'm', metaKey: true })).toBe(true);
    expect(shouldIgnoreShortcut({ key: 'm', altKey: true })).toBe(true);
  });

  it('leaves the canvas Ctrl+0 / Ctrl+= / Ctrl+- bindings alone', () => {
    for (const key of ['0', '=', '-']) {
      expect(shouldIgnoreShortcut({ key, ctrlKey: true })).toBe(true);
      expect(shouldIgnoreShortcut({ key, metaKey: true })).toBe(true);
    }
  });

  it('allows Shift — `?` is Shift+/ on most layouts', () => {
    expect(shouldIgnoreShortcut({ key: '?', shiftKey: true })).toBe(false);
  });
});

// ─── Resolution ────────────────────────────────────────────────────────

describe('resolveShortcut', () => {
  it('maps bound keys to their action', () => {
    expect(resolveShortcut({ key: 'j' })).toBe('navigateNext');
    expect(resolveShortcut({ key: 'k' })).toBe('navigatePrev');
    expect(resolveShortcut({ key: 'h' })).toBe('drillOut');
    expect(resolveShortcut({ key: 'l' })).toBe('drillIn');
    expect(resolveShortcut({ key: 'm' })).toBe('openMerge');
    expect(resolveShortcut({ key: '?' })).toBe('toggleHelp');
  });

  it('returns null for unbound keys', () => {
    expect(resolveShortcut({ key: 'q' })).toBeNull();
    expect(resolveShortcut({ key: 'Escape' })).toBeNull();
  });

  it('returns null while composing a message (the UI-07 pitfall)', () => {
    // Typing "jklm?" into the composer must produce zero actions.
    for (const key of ['j', 'k', 'l', 'm', '?', 'h']) {
      expect(
        resolveShortcut({ key, target: { tagName: 'TEXTAREA' } }),
      ).toBeNull();
    }
  });

  it('returns null while typing in the navigation search input', () => {
    expect(resolveShortcut({ key: 'm', target: { tagName: 'INPUT' } })).toBeNull();
  });

  it('does not shadow existing canvas modifier shortcuts', () => {
    expect(resolveShortcut({ key: '0', ctrlKey: true })).toBeNull();
    expect(resolveShortcut({ key: 'm', metaKey: true })).toBeNull();
  });
});

// ─── j / k cycling ─────────────────────────────────────────────────────

describe('nextFocusIndex', () => {
  it('steps forward and backward', () => {
    expect(nextFocusIndex(0, 4, 1)).toBe(1);
    expect(nextFocusIndex(2, 4, -1)).toBe(1);
  });

  it('wraps at both ends', () => {
    expect(nextFocusIndex(3, 4, 1)).toBe(0);
    expect(nextFocusIndex(0, 4, -1)).toBe(3);
  });

  it('starts at the top for j and the bottom for k when nothing is focused', () => {
    expect(nextFocusIndex(-1, 4, 1)).toBe(0);
    expect(nextFocusIndex(-1, 4, -1)).toBe(3);
  });

  it('returns -1 for an empty list', () => {
    expect(nextFocusIndex(-1, 0, 1)).toBe(-1);
    expect(nextFocusIndex(0, 0, -1)).toBe(-1);
  });

  it('recovers from an out-of-range index (node disappeared)', () => {
    expect(nextFocusIndex(99, 3, 1)).toBe(0);
    expect(nextFocusIndex(99, 3, -1)).toBe(2);
  });

  it('stays put on a single-item list', () => {
    expect(nextFocusIndex(0, 1, 1)).toBe(0);
    expect(nextFocusIndex(0, 1, -1)).toBe(0);
  });
});

// ─── h / l drilling ────────────────────────────────────────────────────

describe('drillOutTarget (h)', () => {
  it('collapses an open branch first', () => {
    expect(
      drillOutTarget({
        nodeId: 'a',
        collapsible: true,
        collapsed: false,
        parentId: 'root',
      }),
    ).toEqual({ kind: 'collapse', nodeId: 'a' });
  });

  it('steps up to the parent once the branch is already collapsed', () => {
    expect(
      drillOutTarget({
        nodeId: 'a',
        collapsible: true,
        collapsed: true,
        parentId: 'root',
      }),
    ).toEqual({ kind: 'focus', nodeId: 'root' });
  });

  it('steps up from a leaf, which has nothing to collapse', () => {
    expect(
      drillOutTarget({
        nodeId: 'leaf',
        collapsible: false,
        collapsed: false,
        parentId: 'a',
      }),
    ).toEqual({ kind: 'focus', nodeId: 'a' });
  });

  it('is a no-op at the root', () => {
    expect(
      drillOutTarget({
        nodeId: 'root',
        collapsible: false,
        collapsed: false,
        parentId: null,
      }),
    ).toEqual({ kind: 'none' });
  });

  it('is a no-op when nothing is focused', () => {
    expect(
      drillOutTarget({ nodeId: null, collapsible: true, collapsed: false }),
    ).toEqual({ kind: 'none' });
  });
});

describe('drillInTarget (l)', () => {
  it('expands a collapsed branch first', () => {
    expect(
      drillInTarget({ nodeId: 'a', collapsed: true, firstChildId: 'b' }),
    ).toEqual({ kind: 'expand', nodeId: 'a' });
  });

  it('descends into the first child once the branch is open', () => {
    expect(
      drillInTarget({ nodeId: 'a', collapsed: false, firstChildId: 'b' }),
    ).toEqual({ kind: 'focus', nodeId: 'b' });
  });

  it('is a no-op on an open leaf', () => {
    expect(
      drillInTarget({ nodeId: 'leaf', collapsed: false, firstChildId: null }),
    ).toEqual({ kind: 'none' });
  });

  it('is a no-op when nothing is focused', () => {
    expect(drillInTarget({ nodeId: null, collapsed: false })).toEqual({
      kind: 'none',
    });
  });

  it('round-trips with drillOutTarget: collapse then expand the same node', () => {
    const out = drillOutTarget({
      nodeId: 'a',
      collapsible: true,
      collapsed: false,
      parentId: 'root',
    });
    expect(out).toEqual({ kind: 'collapse', nodeId: 'a' });
    const back = drillInTarget({ nodeId: 'a', collapsed: true, firstChildId: 'b' });
    expect(back).toEqual({ kind: 'expand', nodeId: 'a' });
  });
});

// ─── Parent map ────────────────────────────────────────────────────────

describe('buildParentMap', () => {
  it('maps each child to its parent', () => {
    const parents = buildParentMap([
      { source: 'root', target: 'a' },
      { source: 'a', target: 'b' },
    ]);
    expect(parents.get('a')).toBe('root');
    expect(parents.get('b')).toBe('a');
  });

  it('keeps the first parent for a multi-parent synthesis node', () => {
    const parents = buildParentMap([
      { source: 'a', target: 'synth' },
      { source: 'b', target: 'synth' },
    ]);
    expect(parents.get('synth')).toBe('a');
  });

  it('leaves roots absent', () => {
    const parents = buildParentMap([{ source: 'root', target: 'a' }]);
    expect(parents.has('root')).toBe(false);
  });

  it('skips malformed edges', () => {
    const parents = buildParentMap([
      { source: '', target: 'a' },
      { source: 'root', target: '' },
      { source: 'root', target: 'b' },
    ]);
    expect(parents.size).toBe(1);
    expect(parents.get('b')).toBe('root');
  });

  it('handles an empty edge list', () => {
    expect(buildParentMap([]).size).toBe(0);
  });
});
