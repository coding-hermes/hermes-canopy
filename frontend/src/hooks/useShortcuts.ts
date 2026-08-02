/**
 * Hermes Canopy — useShortcuts (UI-07)
 *
 * ONE window keydown listener for the single-key shortcut layer. The
 * decisions (is this a shortcut? is the user typing?) all live in
 * `lib/shortcuts.ts`; this hook is only the wiring:
 *
 *   window keydown → shouldIgnoreShortcut → resolveShortcut → handler
 *
 * Handlers are kept in a ref so the listener is registered exactly once
 * per mount. Re-subscribing whenever a caller passes a fresh inline
 * handler object would churn listeners every render, and under
 * StrictMode's double-mount that is how duplicate-fire bugs start.
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  resolveShortcut,
  type ShortcutActionId,
} from '../lib/shortcuts.ts';

// ─── Types ─────────────────────────────────────────────────────────────

/** Partial map — unhandled actions simply fall through. */
export type ShortcutHandlers = Partial<
  Record<ShortcutActionId, (event: KeyboardEvent) => void>
>;

export interface UseShortcutsOptions {
  /** Turn the whole layer off (e.g. while a modal owns the keyboard). */
  enabled?: boolean;
  /**
   * Whether the hook owns `?` itself. The app-level instance does; a
   * tree-scoped instance sets this false so the two do not both toggle
   * the same overlay.
   */
  handleHelpToggle?: boolean;
}

export interface UseShortcutsResult {
  /** Whether the shortcut help overlay is open. */
  helpOpen: boolean;
  setHelpOpen: (open: boolean) => void;
  toggleHelp: () => void;
}

// ─── Hook ──────────────────────────────────────────────────────────────

export function useShortcuts(
  handlers: ShortcutHandlers = {},
  options: UseShortcutsOptions = {},
): UseShortcutsResult {
  const { enabled = true, handleHelpToggle = false } = options;

  const [helpOpen, setHelpOpen] = useState(false);

  // Latest handlers, read at dispatch time — see header note.
  const handlersRef = useRef<ShortcutHandlers>(handlers);
  handlersRef.current = handlers;

  const toggleHelp = useCallback(() => setHelpOpen((prev) => !prev), []);

  useEffect(() => {
    if (!enabled) return;

    function onKeyDown(event: KeyboardEvent) {
      const target = event.target;
      const action = resolveShortcut({
        key: event.key,
        ctrlKey: event.ctrlKey,
        metaKey: event.metaKey,
        altKey: event.altKey,
        shiftKey: event.shiftKey,
        target:
          target instanceof HTMLElement
            ? {
                tagName: target.tagName,
                isContentEditable: target.isContentEditable,
                // `closest` so a caret in a nested span of an editable
                // region still counts as typing — and so the guard is
                // provable under jsdom, which never sets
                // `isContentEditable`.
                contentEditableAttr:
                  target
                    .closest('[contenteditable]')
                    ?.getAttribute('contenteditable') ?? null,
                role: target.getAttribute('role'),
              }
            : null,
      });
      if (!action) return;

      // `?` is owned by whichever instance opted in, so the overlay has a
      // single controller even though several hooks may be mounted.
      if (action === 'toggleHelp') {
        if (!handleHelpToggle) return;
        event.preventDefault();
        setHelpOpen((prev) => !prev);
        return;
      }

      const handler = handlersRef.current[action];
      if (!handler) return;
      event.preventDefault();
      handler(event);
    }

    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [enabled, handleHelpToggle]);

  return { helpOpen, setHelpOpen, toggleHelp };
}

export default useShortcuts;
