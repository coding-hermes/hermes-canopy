/**
 * Hermes Canopy — Node card text helpers (UI-04, Phase 11 Mockup Parity)
 *
 * The small pure derivations every card on the branching canvas performs
 * before it paints: the author line, the header timestamp, and the body
 * preview. They live here rather than in NodeChrome.tsx for two reasons —
 * pure logic is unit-testable without a renderer, and a `.tsx` module that
 * exports non-components breaks React Fast Refresh.
 */

import { describeNodeAvatar } from './nodeAvatar';

// ─── Author ────────────────────────────────────────────────────────────

/**
 * Resolved display name for a node's author — the card's author line and
 * the text inside its aria-label. Thin wrapper over `describeNodeAvatar`
 * so a card that needs only the name doesn't also compute colours.
 */
export function nodeAuthorName(
  authorId: string,
  opts: { names?: ReadonlyMap<string, string>; isAgent?: boolean } = {},
): string {
  return describeNodeAvatar(authorId, opts).name;
}

// ─── Timestamp ─────────────────────────────────────────────────────────

/**
 * Header timestamp — HH:MM, matching the mockup ("09:48").
 *
 * Returns an empty string for an unparsable date so a bad record renders
 * a blank corner rather than the literal "Invalid Date".
 */
export function formatNodeTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
  });
}

// ─── Body preview ──────────────────────────────────────────────────────

/**
 * Truncate body copy to a card-sized preview, appending an ellipsis only
 * when something was actually cut. Whitespace is trimmed first so a
 * leading newline doesn't eat a visible line of the card.
 */
export function previewText(content: string, limit = 120): string {
  const text = (content ?? '').trim();
  if (text.length <= limit) return text;
  return `${text.slice(0, limit)}…`;
}
