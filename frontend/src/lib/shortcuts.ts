/**
 * Hermes Canopy — Keyboard shortcut algebra (UI-07)
 *
 * The app already had scattered keydown handlers (TreeCanvas zoom/Tab,
 * NavigationBar search arrows, MessageComposer ⌘↵). UI-07 adds the
 * vim-flavoured navigation layer — `j`/`k` to walk the graph, `h`/`l` to
 * drill out/in, `m` to jump to the merge queue, `?` for help — and those
 * are *single-key* shortcuts, which is exactly the class of shortcut that
 * breaks typing if it is wired carelessly.
 *
 * So the decisions live here as pure functions:
 *
 *   - `SHORTCUTS`            single source of truth (hook + help overlay)
 *   - `shouldIgnoreShortcut` the typing guard (input/textarea/CE/modifier)
 *   - `resolveShortcut`      event → action id, or null
 *   - `nextFocusIndex`       cycling for j/k
 *   - `drillOutTarget` /
 *     `drillInTarget`        what h/l mean for a given node's state
 *
 * Nothing in this module touches the DOM or React: every input is a plain
 * structural shape, which is what makes the guard matrix cheap to pin in
 * a unit test instead of eyeballing it in a browser.
 */

// ─── Actions ───────────────────────────────────────────────────────────

/** Every action a single-key shortcut can request. */
export type ShortcutActionId =
  | 'navigateNext'
  | 'navigatePrev'
  | 'drillOut'
  | 'drillIn'
  | 'openMerge'
  | 'toggleHelp';

/** Where a shortcut is meaningful — drives the help overlay's grouping. */
export type ShortcutScope = 'tree' | 'global';

export interface ShortcutDef {
  /** `KeyboardEvent.key` value that triggers it. */
  key: string;
  /** What the key is called in the footer strip / help overlay. */
  label: string;
  /** The action dispatched to the handler map. */
  action: ShortcutActionId;
  /** One-line explanation shown in the help overlay. */
  description: string;
  scope: ShortcutScope;
}

/**
 * The registry. Both `useShortcuts` and the help overlay read this, so a
 * key can never be bound to something the help text does not mention.
 *
 * `j`/`k`/`h`/`l` are tree-scoped: they resolve to an action here, but only
 * the canvas registers handlers for them, so they are inert elsewhere
 * rather than silently doing something surprising on a list page.
 */
export const SHORTCUTS: readonly ShortcutDef[] = [
  {
    key: 'j',
    label: 'j',
    action: 'navigateNext',
    description: 'Move to the next node',
    scope: 'tree',
  },
  {
    key: 'k',
    label: 'k',
    action: 'navigatePrev',
    description: 'Move to the previous node',
    scope: 'tree',
  },
  {
    key: 'h',
    label: 'h',
    action: 'drillOut',
    description: 'Drill out — collapse the branch, or step up to the parent',
    scope: 'tree',
  },
  {
    key: 'l',
    label: 'l',
    action: 'drillIn',
    description: 'Drill in — expand the branch, or step down to the first reply',
    scope: 'tree',
  },
  {
    key: 'm',
    label: 'm',
    action: 'openMerge',
    description: 'Open the merge view (approval queue)',
    scope: 'global',
  },
  {
    key: '?',
    label: '?',
    action: 'toggleHelp',
    description: 'Show or hide this shortcut help',
    scope: 'global',
  },
] as const;

/** Route the `openMerge` action navigates to — the UI-03 "Merge" view. */
export const MERGE_ROUTE = '/approvals';

/** Shortcuts belonging to one scope, in registry order. */
export function shortcutsForScope(scope: ShortcutScope): readonly ShortcutDef[] {
  return SHORTCUTS.filter((s) => s.scope === scope);
}

// ─── Typing guard ──────────────────────────────────────────────────────

/**
 * The subset of an event target the guard reads. Structural on purpose —
 * the rule is then testable without a DOM, exactly like `canvasOwnsTab`
 * in `canvasGeometry.ts`.
 */
export interface ShortcutTargetLike {
  tagName?: string;
  isContentEditable?: boolean;
  /**
   * Value of the nearest `contenteditable` attribute (self or ancestor),
   * or null when there is none.
   *
   * `isContentEditable` alone is not enough: jsdom does not implement the
   * property (it is always false there), so a guard resting only on it
   * cannot be proven in a unit test — and a caret inside a *nested*
   * element of an editable region is the common real case anyway.
   */
  contentEditableAttr?: string | null;
  /** ARIA role, when the element declares one (`role="textbox"` etc). */
  role?: string | null;
}

/** The subset of a KeyboardEvent the resolver reads. */
export interface ShortcutEventLike {
  key: string;
  ctrlKey?: boolean;
  metaKey?: boolean;
  altKey?: boolean;
  shiftKey?: boolean;
  target?: ShortcutTargetLike | null;
}

/** Elements whose keystrokes always belong to the element, never to us. */
const TEXT_ENTRY_TAGS = new Set(['INPUT', 'TEXTAREA', 'SELECT', 'OPTION']);

/** ARIA roles that behave like a text field even on a non-input element. */
const TEXT_ENTRY_ROLES = new Set(['textbox', 'searchbox', 'combobox', 'spinbutton']);

/**
 * Whether a keydown must NOT be treated as a single-key shortcut.
 *
 * Two independent reasons to bail:
 *
 *   1. **The user is typing.** MessageComposer is a `<textarea>`, the
 *      NavigationBar search is an `<input>`, and plugins may render
 *      `contentEditable` surfaces — a bare `j` there is the letter j, not
 *      "next node". This is the pitfall UI-07 exists to avoid.
 *   2. **A modifier is held.** `Ctrl`/`⌘`/`Alt` combinations belong to the
 *      browser, the OS, assistive tech, or the canvas' own zoom bindings
 *      (Ctrl+0 / Ctrl+= / Ctrl+-), none of which we may shadow.
 *
 * `Shift` is deliberately NOT a blocker: `?` is Shift+/ on most layouts,
 * so blocking Shift would make the help key unreachable.
 */
export function shouldIgnoreShortcut(event: ShortcutEventLike): boolean {
  if (event.ctrlKey || event.metaKey || event.altKey) return true;

  const target = event.target;
  if (!target) return false;

  const tag = target.tagName?.toUpperCase();
  if (tag && TEXT_ENTRY_TAGS.has(tag)) return true;
  if (target.isContentEditable) return true;

  // `contenteditable="false"` explicitly opts back OUT of editing, so only
  // the other values (including the valueless `contenteditable=""`) count.
  const ce = target.contentEditableAttr;
  if (ce !== null && ce !== undefined && ce.toLowerCase() !== 'false') return true;

  const role = target.role?.toLowerCase();
  if (role && TEXT_ENTRY_ROLES.has(role)) return true;

  return false;
}

// ─── Resolution ────────────────────────────────────────────────────────

const BY_KEY: ReadonlyMap<string, ShortcutDef> = new Map(
  SHORTCUTS.map((s) => [s.key, s]),
);

/** The shortcut bound to a key, if any. */
export function shortcutForKey(key: string): ShortcutDef | null {
  return BY_KEY.get(key) ?? null;
}

/**
 * Action for an event, or `null` when the event is not ours — either
 * because the guard rejected it or because nothing is bound to the key.
 */
export function resolveShortcut(event: ShortcutEventLike): ShortcutActionId | null {
  if (shouldIgnoreShortcut(event)) return null;
  return shortcutForKey(event.key)?.action ?? null;
}

// ─── j / k cycling ─────────────────────────────────────────────────────

/**
 * Index of the next/previous item, wrapping at both ends.
 *
 * `current < 0` means "nothing focused yet": `j` then starts at the top of
 * the list and `k` at the bottom, which is what makes the very first
 * keypress feel right rather than jumping to index 1.
 *
 * Returns `-1` for an empty list so callers have a single "nothing to do"
 * check instead of guarding length themselves.
 */
export function nextFocusIndex(
  current: number,
  length: number,
  direction: 1 | -1,
): number {
  if (length <= 0) return -1;
  if (current < 0 || current >= length) return direction === 1 ? 0 : length - 1;
  const next = current + direction;
  if (next < 0) return length - 1;
  if (next >= length) return 0;
  return next;
}

// ─── h / l drilling ────────────────────────────────────────────────────

/**
 * What a drill key resolves to for the focused node.
 *
 * `collapse`/`expand` mutate collapse state; `focus` moves the cursor to
 * another node; `none` means the key is a no-op in this position (the
 * root has no parent, a leaf has no children).
 */
export interface DrillOutcome {
  kind: 'collapse' | 'expand' | 'focus' | 'none';
  /** Node to act on — the focused node itself, or the node to move to. */
  nodeId?: string;
}

/**
 * `h` — drill out.
 *
 * An open branch closes first (you are stepping out of what you can see);
 * once there is nothing left to close, the key walks up to the parent.
 * That two-stage behaviour is what makes `h` feel like "back" in a tree
 * rather than a collapse-only toggle.
 */
export function drillOutTarget(state: {
  nodeId: string | null;
  collapsible: boolean;
  collapsed: boolean;
  parentId?: string | null;
}): DrillOutcome {
  if (!state.nodeId) return { kind: 'none' };
  if (state.collapsible && !state.collapsed) {
    return { kind: 'collapse', nodeId: state.nodeId };
  }
  if (state.parentId) return { kind: 'focus', nodeId: state.parentId };
  return { kind: 'none' };
}

/**
 * `l` — drill in.
 *
 * Mirror image of `h`: reveal hidden children first, then descend into
 * them. Expanding and moving in one keypress would skip past the content
 * the user just uncovered.
 */
export function drillInTarget(state: {
  nodeId: string | null;
  collapsed: boolean;
  firstChildId?: string | null;
}): DrillOutcome {
  if (!state.nodeId) return { kind: 'none' };
  if (state.collapsed) return { kind: 'expand', nodeId: state.nodeId };
  if (state.firstChildId) return { kind: 'focus', nodeId: state.firstChildId };
  return { kind: 'none' };
}

// ─── Reverse adjacency ─────────────────────────────────────────────────

/**
 * child id → first parent id, for `h`'s step-up.
 *
 * The graph is a DAG: a synthesis node legitimately has several parents.
 * Keyboard navigation needs one deterministic answer, so the first edge
 * in graph order wins — the same order the canvas draws them in.
 */
export function buildParentMap(
  edges: readonly { source: string; target: string }[],
): ReadonlyMap<string, string> {
  const parents = new Map<string, string>();
  for (const edge of edges) {
    if (!edge || !edge.source || !edge.target) continue;
    if (!parents.has(edge.target)) parents.set(edge.target, edge.source);
  }
  return parents;
}
