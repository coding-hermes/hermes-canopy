/**
 * Hermes Canopy — Countable phrasing (UI-08)
 *
 * The Nodes page rendered "(1 nodes)" in three places because the count
 * and the noun were concatenated without inflection. It is a one-line bug
 * with a one-line fix, which is exactly why it survived three tickets:
 * every call site fixed it locally, or not at all.
 *
 * Centralised here so a count and its noun can never again disagree, and
 * so the plural of an irregular noun is stated once.
 */

/**
 * Inflect a noun for a count.
 *
 * `plural` is optional and defaults to `singular + 's'` — pass it for
 * anything irregular ("entry"/"entries").
 */
export function pluralize(
  count: number,
  singular: string,
  plural?: string,
): string {
  return Math.abs(count) === 1 ? singular : (plural ?? `${singular}s`);
}

/**
 * `"1 node"` / `"6 nodes"` — the count and its inflected noun.
 *
 * Non-finite counts (a payload with a missing `node_count`) render as 0
 * rather than "NaN nodes".
 */
export function countLabel(
  count: number,
  singular: string,
  plural?: string,
): string {
  const n = Number.isFinite(count) ? count : 0;
  return `${n} ${pluralize(n, singular, plural)}`;
}

/** `"3 of 6 nodes"` — the noun always agrees with the TOTAL, not the subset. */
export function filteredCountLabel(
  shown: number,
  total: number,
  singular: string,
  plural?: string,
): string {
  const t = Number.isFinite(total) ? total : 0;
  const s = Number.isFinite(shown) ? shown : 0;
  return `${s} of ${t} ${pluralize(t, singular, plural)}`;
}
