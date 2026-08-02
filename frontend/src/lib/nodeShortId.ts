/**
 * Hermes Canopy — Distinguishable short node ids (UI-08)
 *
 * ## The "duplicate seed data" finding, and what it actually was
 *
 * A screenshot of the Nodes page showed `019fb0c2` on four consecutive
 * rows and was filed as "dedupe demo tree node IDs (019fb0c2 repeated
 * ×4 in seed data)". The seed data is not duplicated. Queried live
 * (`GET /api/v1/trees/9a7f97f3…/nodes`, 6 rows):
 *
 *     019fb0bc-ddcb-710f-8497-387ee327b400   # Welcome to Hermes Canopy
 *     019fb0c0-58c1-7d9b-b4dc-ecc04872c800   Child 1: Architecture
 *     019fb0c2-cab0-70c5-a477-fa10f136e000   Child #2: …
 *     019fb0c2-cad5-75b5-a291-2dde84047400   Child #3: …
 *     019fb0c2-caed-7dca-b471-d03e71f6b000   Child #4: …
 *     019fb0c2-cb05-75f0-9854-def889e34000   Child #5: …
 *
 * Six distinct ids; three distinct first groups. The duplication is
 * introduced by the DISPLAY, which truncated at `id.split('-')[0]`.
 *
 * These are UUIDv7 (`migrations/000001_extensions.up.sql` — every table
 * is `DEFAULT uuidv7()`). In v7 the leading 48 bits are a Unix
 * millisecond timestamp, so the first group is the TOP 32 BITS OF A
 * TIMESTAMP: it only changes once every 2^16 ms ≈ 65.5 seconds. Four
 * nodes seeded inside the same minute are *guaranteed* to share it.
 * A prefix-truncated UUIDv7 is not an identifier, it is a coarse clock.
 *
 * The fix is therefore a display fix, and it cannot be "show more
 * characters" alone — extending the prefix just narrows the window.
 * Bytes that actually vary between two same-millisecond v7 ids live in
 * the RANDOM tail, so the short form has to reach the tail:
 *
 *     019fb0c2-cab0-…   →   019fb0c2…e000
 *
 * `shortNodeId` renders head + tail; `disambiguateNodeIds` then widens
 * only those labels that still collide within the rendered set, so the
 * common case stays short and a genuine collision stays honest.
 */

// ─── Constants ─────────────────────────────────────────────────────────

/** Leading hex characters kept (the UUID's first group). */
const HEAD = 8;

/** Trailing hex characters kept — these come from the v7 random tail. */
const TAIL = 4;

/** Ellipsis joining head and tail. */
const GAP = '…';

// ─── Short form ────────────────────────────────────────────────────────

/**
 * A short, human-quotable node id that is actually distinguishing.
 *
 * Ids too short to benefit (anything that would not shrink) pass through
 * verbatim rather than being padded into a fake ellipsis.
 */
export function shortNodeId(id: string, tail: number = TAIL): string {
  const raw = (id ?? '').trim();
  if (!raw) return '';

  const keep = Math.max(0, tail);
  if (raw.length <= HEAD + keep + GAP.length) return raw;

  const head = raw.slice(0, HEAD);
  return keep === 0 ? head : `${head}${GAP}${raw.slice(-keep)}`;
}

/**
 * Short labels for a whole rendered list, guaranteed unique within it.
 *
 * Starts at the default tail and widens for everyone only while some
 * pair still collides — a list where four ids share a prefix must not
 * show four identical labels, and a list where none collide must not pay
 * for that with a wall of hex. Falls back to the full id if even the
 * widest form collides (identical ids in one payload, which is a data
 * bug worth showing verbatim).
 */
export function disambiguateNodeIds(
  ids: readonly string[],
): Map<string, string> {
  const unique = Array.from(
    new Set(ids.map((id) => (id ?? '').trim()).filter(Boolean)),
  );
  const out = new Map<string, string>();
  if (unique.length === 0) return out;

  const longest = unique.reduce((max, id) => Math.max(max, id.length), 0);

  for (let tail = TAIL; tail <= longest; tail += 2) {
    const labels = unique.map((id) => shortNodeId(id, tail));
    if (new Set(labels).size === unique.length) {
      unique.forEach((id, i) => out.set(id, labels[i]!));
      return out;
    }
  }

  for (const id of unique) out.set(id, id);
  return out;
}

// ─── Accessible label ──────────────────────────────────────────────────

/**
 * Screen-reader label for the id link. The visible label is elided, so
 * the accessible name carries the id in full — a user who cannot see the
 * ellipsis must still be able to identify (and dictate) the node.
 */
export function nodeIdLinkLabel(id: string): string {
  return `Open node ${id}`;
}
