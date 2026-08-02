/**
 * Hermes Canopy — Shortcut help overlay (UI-07)
 *
 * The `?` panel. Every row is generated from the `SHORTCUTS` registry in
 * `lib/shortcuts.ts`, so the help can never drift from what the keys
 * actually do — adding a shortcut there adds it here.
 *
 * Dark tokens throughout (surface-raised on a dimmed backdrop, kbd chips
 * matching the NavigationBar footer strip). Dismisses on Escape or a
 * click on the backdrop; labelled `role="dialog"` + `aria-modal` so
 * assistive tech announces it as a modal.
 */

import { useEffect, useRef } from 'react';
import { X, Keyboard } from 'lucide-react';
import { token } from '../theme.ts';
import { SHORTCUTS, type ShortcutScope } from '../lib/shortcuts.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface ShortcutHelpProps {
  open: boolean;
  onClose: () => void;
}

const SCOPE_LABEL: Record<ShortcutScope, string> = {
  tree: 'Tree view',
  global: 'Anywhere',
};

const SCOPE_ORDER: readonly ShortcutScope[] = ['tree', 'global'];

const TITLE_ID = 'shortcut-help-title';

// ─── Component ─────────────────────────────────────────────────────────

export default function ShortcutHelp({ open, onClose }: ShortcutHelpProps) {
  const closeRef = useRef<HTMLButtonElement>(null);

  // Escape closes. Bound only while open, so the key stays available to
  // TreeCanvas (deselect) the rest of the time.
  useEffect(() => {
    if (!open) return;
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onClose();
      }
    }
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [open, onClose]);

  // Move focus into the dialog so a keyboard user is not stranded behind
  // it — the close button is the one guaranteed control.
  useEffect(() => {
    if (open) closeRef.current?.focus();
  }, [open]);

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-60 flex items-center justify-center p-4"
      style={{ backgroundColor: 'rgba(0,0,0,0.66)' }}
      onClick={onClose}
      data-testid="shortcut-help-backdrop"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={TITLE_ID}
        className="relative w-full max-w-md rounded-xl border shadow-2xl"
        style={{
          backgroundColor: token.surfaceRaised,
          borderColor: token.lineSubtle,
        }}
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div
          className="flex items-center justify-between px-5 py-3.5 border-b"
          style={{ borderColor: token.lineSubtle }}
        >
          <div className="flex items-center gap-2">
            <Keyboard
              className="w-4 h-4"
              style={{ color: token.accent }}
              aria-hidden="true"
            />
            <h2
              id={TITLE_ID}
              className="text-base font-semibold"
              style={{ color: token.contentPrimary }}
            >
              Keyboard shortcuts
            </h2>
          </div>
          <button
            ref={closeRef}
            type="button"
            onClick={onClose}
            className="p-1 rounded-md transition-colors hover:bg-white/5"
            style={{ color: token.contentMuted }}
            aria-label="Close keyboard shortcuts"
          >
            <X className="w-4 h-4" aria-hidden="true" />
          </button>
        </div>

        {/* Body — one group per scope, rows straight from the registry */}
        <div className="px-5 py-4 space-y-4">
          {SCOPE_ORDER.map((scope) => {
            const rows = SHORTCUTS.filter((s) => s.scope === scope);
            if (rows.length === 0) return null;
            return (
              <div key={scope} className="space-y-2">
                <h3
                  className="text-xs font-medium uppercase tracking-wide"
                  style={{ color: token.contentMuted }}
                >
                  {SCOPE_LABEL[scope]}
                </h3>
                <ul className="space-y-1.5">
                  {rows.map((shortcut) => (
                    <li
                      key={shortcut.key}
                      className="flex items-baseline gap-3 text-sm"
                    >
                      <kbd className="shrink-0 min-w-[1.75rem] text-center px-1.5 py-0.5 rounded-xs text-[11px] font-mono bg-surface-input border border-line-subtle text-content-secondary">
                        {shortcut.label}
                      </kbd>
                      <span style={{ color: token.contentSecondary }}>
                        {shortcut.description}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            );
          })}
        </div>

        {/* Footer hint */}
        <div
          className="px-5 py-3 border-t text-xs"
          style={{
            borderColor: token.lineSubtle,
            color: token.contentMuted,
          }}
        >
          Shortcuts are ignored while you are typing in a message, search
          box, or any other text field.
        </div>
      </div>
    </div>
  );
}
