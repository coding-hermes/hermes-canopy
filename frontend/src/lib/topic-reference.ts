/**
 * Hermes Canopy — Reference parsing utility (TM-04)
 *
 * Client-side mirror of the Go canonical #topic-slug regex from SPEC-TM-01 §5.3.
 * Used by the composer autocomplete to detect reference tokens at the cursor.
 *
 * slug = [a-z]([a-z0-9-]*[a-z0-9])? — must start with a letter, may contain
 * lowercase alphanumeric and hyphens, must not end with a hyphen.
 * Single letters are allowed.
 */

import type { ParsedReference } from '../types/reference';

/**
 * The canonical reference regex. Matches '#slug' where the '#' is at the start
 * of text or preceded by a non-word, non-'#' character.
 *
 * The prefix group (?:^|[^a-zA-Z0-9#]) ensures '#' inside words, URLs, and
 * markdown headings is not matched.
 */
const REFERENCE_REGEX = /(?:^|[^a-zA-Z0-9#])#([a-z](?:[a-z0-9-]*[a-z0-9])?|[a-z])/g;

/**
 * Parses `#topic-slug` references from message content.
 * Returns all references found, preserving duplicates (dedup is caller's job).
 */
export function parseReferences(content: string): ParsedReference[] {
  if (!content) return [];

  const refs: ParsedReference[] = [];
  let match: RegExpExecArray | null;

  // Reset regex state for repeatable parsing.
  REFERENCE_REGEX.lastIndex = 0;

  while ((match = REFERENCE_REGEX.exec(content)) !== null) {
    const slug = match[1];
    const slugStart = match.index + (match[0].length - slug.length);
    const hashIdx = slugStart - 1;

    refs.push({
      raw: content.slice(hashIdx, slugStart + slug.length),
      slug,
      offset: hashIdx,
      length: slug.length + 1, // +1 for '#'
    });
  }

  return refs;
}

/**
 * Returns true if s is a valid reference slug.
 */
export function isValidSlug(s: string): boolean {
  return /^[a-z]([a-z0-9-]*[a-z0-9])?$/.test(s);
}

/**
 * Extracts the incomplete reference prefix being typed at the cursor position.
 * Walks backwards from cursor to find the '#' trigger.
 * Returns null if the cursor is not inside a reference token.
 */
export function getActiveReferencePrefix(
  content: string,
  cursorOffset: number,
): string | null {
  let start = -1;
  for (let i = cursorOffset - 1; i >= 0; i--) {
    const ch = content[i];
    if (ch === '#') {
      start = i + 1;
      break;
    }
    if (!/[a-z0-9-]/.test(ch)) {
      return null;
    }
  }
  if (start === -1) return null;
  return content.slice(start, cursorOffset);
}

/**
 * Returns true if the cursor is positioned inside or immediately after a
 * '#' reference token (i.e., '#' followed by slug characters).
 */
export function isInsideReferenceToken(
  content: string,
  cursorOffset: number,
): boolean {
  return getActiveReferencePrefix(content, cursorOffset) !== null;
}

/**
 * Deduplicates references by slug, keeping the first occurrence.
 */
export function dedupeBySlug(refs: ParsedReference[]): ParsedReference[] {
  const seen = new Set<string>();
  const out: ParsedReference[] = [];
  for (const r of refs) {
    if (seen.has(r.slug)) continue;
    seen.add(r.slug);
    out.push(r);
  }
  return out;
}
