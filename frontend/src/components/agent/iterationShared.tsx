/**
 * Hermes Canopy — shared iteration renderer pieces
 *
 * Small presentational pieces shared by the five §8.2 subtype renderers: a
 * status badge and the polite ARIA live region every renderer owns. The status
 * vocabulary itself (labels and tone classes) lives in ./iterationStatus.ts so
 * this module exports components only (React Fast Refresh). Nothing here
 * interprets server strings as HTML.
 */

import { statusLabel, statusTone } from './iterationStatus.ts';

/** A small status pill. Text-only; `state` is never interpreted as markup. */
export function IterationStatusBadge({ state, label }: { state: string; label?: string }) {
  return (
    <span className={`inline-flex shrink-0 items-center gap-1 rounded-full px-1.5 py-0.5 text-xs ${statusTone(state)}`} role="status">
      {label ?? statusLabel(state)}
    </span>
  );
}

/**
 * Polite live region every renderer owns. Status/progress updates are the only
 * content announced here; code output announcements are throttled by the
 * caller (IterationSidePanel) to ≥2s so this never floods a screen reader.
 */
export function IterationLiveRegion({ id, message }: { id: string; message: string }) {
  return (
    <div
      className="sr-only"
      role="status"
      aria-live="polite"
      aria-atomic="true"
      data-testid={`iteration-live-${id}`}
    >
      {message}
    </div>
  );
}
