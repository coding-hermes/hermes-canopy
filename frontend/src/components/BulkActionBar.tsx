/**
 * Hermes Canopy — Bulk-action bar (UI-08)
 *
 * Appears at the bottom of the Nodes page the moment a row is checked and
 * leaves when the selection empties. Shows what is selected and what can
 * be done to it.
 *
 *   ┌──────────────────────────────────────────────────────────┐
 *   │ 3 nodes selected   [Clear]        Merge   Tag   Delete   │
 *   └──────────────────────────────────────────────────────────┘
 *
 * Merge and Tag render DISABLED with a stated reason rather than being
 * hidden or wired to a guessed endpoint — see `lib/nodeSelection`
 * (`bulkActions`) for which routes were checked and what is missing. A
 * disabled control that says why is a roadmap; an invented `POST
 * /nodes/merge` is a 404 in front of the user.
 *
 * Accessibility: the bar is a labelled `<section>`, and the count lives
 * in an `aria-live="polite"` region so checking rows is announced without
 * stealing focus. Disabled buttons keep their reason reachable through
 * `aria-describedby` — `title` alone is invisible to a screen reader.
 */

import { memo, useId } from 'react';
import { Trash2, GitMerge, Hash, X } from 'lucide-react';
import { bulkActions, type BulkActionId } from '../lib/nodeSelection.ts';
import { countLabel } from '../lib/pluralize.ts';

// ─── Types ─────────────────────────────────────────────────────────────

export interface BulkActionBarProps {
  count: number;
  onClear: () => void;
  onAction: (action: BulkActionId) => void;
}

const ICONS = {
  merge: GitMerge,
  tag: Hash,
  delete: Trash2,
} as const;

// ─── Component ─────────────────────────────────────────────────────────

function BulkActionBarComponent({
  count,
  onClear,
  onAction,
}: BulkActionBarProps) {
  const reasonId = useId();
  const actions = bulkActions(count);

  return (
    <section
      aria-label="Bulk actions"
      data-testid="bulk-action-bar"
      className="glass-raised sticky bottom-4 z-30 mt-4 flex flex-wrap items-center gap-3 rounded-xl px-4 py-3"
    >
      <span
        aria-live="polite"
        data-testid="bulk-action-count"
        className="text-sm font-medium text-content-primary"
      >
        {countLabel(count, 'node')} selected
      </span>

      <button
        type="button"
        onClick={onClear}
        data-testid="bulk-action-clear"
        className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-content-muted transition-colors hover:bg-surface-hover hover:text-content-primary"
      >
        <X className="h-3 w-3" aria-hidden="true" />
        Clear
      </button>

      <div className="ml-auto flex flex-wrap items-center gap-2">
        {actions.map((action) => {
          const Icon = ICONS[action.id];
          const describedBy = action.reason
            ? `${reasonId}-${action.id}`
            : undefined;

          return (
            <span key={action.id} className="inline-flex">
              <button
                type="button"
                disabled={!action.enabled}
                onClick={() => onAction(action.id)}
                title={action.reason ?? `${action.label} selected nodes`}
                aria-describedby={describedBy}
                data-testid={`bulk-action-${action.id}`}
                className={[
                  'inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5',
                  'text-xs font-semibold transition-colors',
                  'disabled:cursor-not-allowed disabled:opacity-45',
                  action.destructive
                    ? 'bg-rose-600 text-white hover:bg-rose-500 disabled:bg-surface-input disabled:text-content-muted'
                    : 'bg-surface-input text-content-secondary ring-1 ring-inset ring-line-subtle hover:bg-surface-hover hover:text-content-primary',
                ].join(' ')}
              >
                <Icon className="h-3.5 w-3.5" aria-hidden="true" />
                {action.label}
              </button>

              {action.reason && (
                <span id={describedBy} className="sr-only">
                  {action.reason}
                </span>
              )}
            </span>
          );
        })}
      </div>
    </section>
  );
}

export const BulkActionBar = memo(BulkActionBarComponent);
export default BulkActionBar;
