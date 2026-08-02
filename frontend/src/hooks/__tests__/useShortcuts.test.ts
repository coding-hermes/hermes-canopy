/**
 * Unit tests — useShortcuts (UI-07)
 *
 * `lib/shortcuts.ts` pins the decisions against structural shapes; this
 * file pins the WIRING against a real DOM: that a genuine `<textarea>`
 * keydown produces no action (the composer-typing pitfall), that exactly
 * one listener is installed per mount and removed on unmount, and that
 * `?` is only honoured by the instance that opted in.
 *
 * The hook is driven with React 19's `act` + `react-dom/client` rather
 * than a testing library, since the project has no @testing-library
 * dependency and adding one is out of scope for UI-07.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { createElement } from 'react';
import { useShortcuts, type ShortcutHandlers } from '../useShortcuts.ts';

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;

/** Latest hook return value, refreshed on every render. */
let latest: ReturnType<typeof useShortcuts> | null = null;

function Probe({
  handlers,
  handleHelpToggle,
  enabled,
}: {
  handlers: ShortcutHandlers;
  handleHelpToggle?: boolean;
  enabled?: boolean;
}) {
  latest = useShortcuts(handlers, {
    ...(handleHelpToggle === undefined ? {} : { handleHelpToggle }),
    ...(enabled === undefined ? {} : { enabled }),
  });
  return null;
}

function mount(props: {
  handlers: ShortcutHandlers;
  handleHelpToggle?: boolean;
  enabled?: boolean;
}) {
  act(() => {
    root.render(createElement(Probe, props));
  });
}

/** Dispatch a keydown from a specific element (defaults to document.body). */
function press(
  key: string,
  opts: {
    from?: HTMLElement;
    ctrlKey?: boolean;
    metaKey?: boolean;
    altKey?: boolean;
    shiftKey?: boolean;
  } = {},
): KeyboardEvent {
  const event = new KeyboardEvent('keydown', {
    key,
    bubbles: true,
    cancelable: true,
    ctrlKey: opts.ctrlKey ?? false,
    metaKey: opts.metaKey ?? false,
    altKey: opts.altKey ?? false,
    shiftKey: opts.shiftKey ?? false,
  });
  const source = opts.from ?? document.body;
  act(() => {
    source.dispatchEvent(event);
  });
  return event;
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  latest = null;
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  document.body.innerHTML = '';
});

// ─── Dispatch ──────────────────────────────────────────────────────────

describe('useShortcuts dispatch', () => {
  it('calls the handler bound to a key', () => {
    const navigateNext = vi.fn();
    const navigatePrev = vi.fn();
    mount({ handlers: { navigateNext, navigatePrev } });

    press('j');
    expect(navigateNext).toHaveBeenCalledTimes(1);
    expect(navigatePrev).not.toHaveBeenCalled();

    press('k');
    expect(navigatePrev).toHaveBeenCalledTimes(1);
  });

  it('routes h / l to the drill handlers', () => {
    const drillOut = vi.fn();
    const drillIn = vi.fn();
    mount({ handlers: { drillOut, drillIn } });

    press('h');
    press('l');
    expect(drillOut).toHaveBeenCalledTimes(1);
    expect(drillIn).toHaveBeenCalledTimes(1);
  });

  it('calls preventDefault only for keys it actually handles', () => {
    mount({ handlers: { openMerge: vi.fn() } });

    const handled = press('m');
    expect(handled.defaultPrevented).toBe(true);

    // Bound in the registry, but no handler supplied here.
    const unhandled = press('j');
    expect(unhandled.defaultPrevented).toBe(false);

    const unbound = press('q');
    expect(unbound.defaultPrevented).toBe(false);
  });

  it('fires exactly once per keypress — no duplicate listeners', () => {
    const navigateNext = vi.fn();
    mount({ handlers: { navigateNext } });

    // Re-render with a fresh inline handler object: the listener must not
    // be re-registered, or every render would add another subscription.
    mount({ handlers: { navigateNext } });
    mount({ handlers: { navigateNext } });

    press('j');
    expect(navigateNext).toHaveBeenCalledTimes(1);
  });

  it('stops listening after unmount', () => {
    const navigateNext = vi.fn();
    mount({ handlers: { navigateNext } });
    act(() => root.unmount());

    press('j');
    expect(navigateNext).not.toHaveBeenCalled();

    // Re-create so afterEach's unmount stays valid.
    root = createRoot(container);
  });

  it('does nothing when disabled', () => {
    const navigateNext = vi.fn();
    mount({ handlers: { navigateNext }, enabled: false });

    press('j');
    expect(navigateNext).not.toHaveBeenCalled();
  });
});

// ─── The composer-typing guard (AC3) ───────────────────────────────────

describe('useShortcuts typing guard', () => {
  it('does not fire while typing in a textarea (MessageComposer)', () => {
    const handlers = {
      navigateNext: vi.fn(),
      navigatePrev: vi.fn(),
      drillIn: vi.fn(),
      drillOut: vi.fn(),
      openMerge: vi.fn(),
    };
    mount({ handlers, handleHelpToggle: true });

    const textarea = document.createElement('textarea');
    document.body.appendChild(textarea);
    textarea.focus();

    for (const key of ['j', 'k', 'h', 'l', 'm', '?']) {
      press(key, { from: textarea });
    }

    for (const fn of Object.values(handlers)) {
      expect(fn).not.toHaveBeenCalled();
    }
    expect(latest?.helpOpen).toBe(false);
  });

  it('does not fire while typing in the search input', () => {
    const openMerge = vi.fn();
    mount({ handlers: { openMerge } });

    const input = document.createElement('input');
    input.type = 'text';
    document.body.appendChild(input);

    press('m', { from: input });
    expect(openMerge).not.toHaveBeenCalled();
  });

  it('does not fire inside a contentEditable surface', () => {
    const navigateNext = vi.fn();
    mount({ handlers: { navigateNext } });

    const editable = document.createElement('div');
    editable.setAttribute('contenteditable', 'true');
    document.body.appendChild(editable);

    press('j', { from: editable });
    expect(navigateNext).not.toHaveBeenCalled();
  });

  it('does not fire from a nested child of a contentEditable region', () => {
    const navigateNext = vi.fn();
    mount({ handlers: { navigateNext } });

    const editable = document.createElement('div');
    editable.setAttribute('contenteditable', 'true');
    const inner = document.createElement('span');
    editable.appendChild(inner);
    document.body.appendChild(editable);

    press('j', { from: inner });
    expect(navigateNext).not.toHaveBeenCalled();
  });

  it('does not swallow modifier combinations (canvas Ctrl+0 / ⌘- keep working)', () => {
    const openMerge = vi.fn();
    mount({ handlers: { openMerge } });

    const withCtrl = press('m', { ctrlKey: true });
    expect(openMerge).not.toHaveBeenCalled();
    expect(withCtrl.defaultPrevented).toBe(false);

    const zoom = press('0', { ctrlKey: true });
    expect(zoom.defaultPrevented).toBe(false);
  });

  it('still fires from an ordinary element such as the canvas body', () => {
    const navigateNext = vi.fn();
    mount({ handlers: { navigateNext } });

    const pane = document.createElement('div');
    document.body.appendChild(pane);

    press('j', { from: pane });
    expect(navigateNext).toHaveBeenCalledTimes(1);
  });
});

// ─── Help overlay ownership ────────────────────────────────────────────

describe('useShortcuts help toggle', () => {
  it('toggles helpOpen when the instance owns `?`', () => {
    mount({ handlers: {}, handleHelpToggle: true });
    expect(latest?.helpOpen).toBe(false);

    press('?', { shiftKey: true });
    expect(latest?.helpOpen).toBe(true);

    press('?', { shiftKey: true });
    expect(latest?.helpOpen).toBe(false);
  });

  it('ignores `?` when another instance owns the overlay', () => {
    mount({ handlers: {}, handleHelpToggle: false });

    const event = press('?');
    expect(latest?.helpOpen).toBe(false);
    expect(event.defaultPrevented).toBe(false);
  });

  it('exposes imperative setters for the overlay close button', () => {
    mount({ handlers: {}, handleHelpToggle: true });

    act(() => latest?.setHelpOpen(true));
    expect(latest?.helpOpen).toBe(true);

    act(() => latest?.toggleHelp());
    expect(latest?.helpOpen).toBe(false);
  });
});
