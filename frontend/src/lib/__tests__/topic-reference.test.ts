/**
 * Reference parsing utility tests (TM-04 frontend scenarios).
 *
 * Tests the client-side mirror of the canonical #slug regex from topic-reference.ts.
 */

import { describe, it, expect } from 'vitest';
import {
  parseReferences,
  isValidSlug,
  getActiveReferencePrefix,
  dedupeBySlug,
} from '../topic-reference';

describe('parseReferences', () => {
  it('parses a single reference', () => {
    const refs = parseReferences('See #database-schema');
    expect(refs).toHaveLength(1);
    expect(refs[0].slug).toBe('database-schema');
    expect(refs[0].raw).toBe('#database-schema');
    expect(refs[0].offset).toBe(4);
    expect(refs[0].length).toBe(16);
  });

  it('parses multiple references', () => {
    const refs = parseReferences('#schema and #data-flow');
    expect(refs).toHaveLength(2);
    expect(refs[0].slug).toBe('schema');
    expect(refs[1].slug).toBe('data-flow');
  });

  it('rejects invalid slugs', () => {
    const refs = parseReferences('#123-start #UPPER #-leading');
    expect(refs).toHaveLength(0);
  });

  it('accepts single-letter slugs', () => {
    const refs = parseReferences('See #a here');
    expect(refs).toHaveLength(1);
    expect(refs[0].slug).toBe('a');
  });

  it('returns empty for plain text', () => {
    expect(parseReferences('Just some text')).toHaveLength(0);
  });

  it('returns empty for empty content', () => {
    expect(parseReferences('')).toHaveLength(0);
  });

  it('preserves duplicates', () => {
    const refs = parseReferences('#schema and #schema again');
    expect(refs).toHaveLength(2);
  });

  it('does not match # inside URLs', () => {
    const refs = parseReferences('https://example.com/page#section');
    expect(refs).toHaveLength(0);
  });

  it('does not match markdown headings', () => {
    const refs = parseReferences('# Heading');
    expect(refs).toHaveLength(0);
  });
});

describe('isValidSlug', () => {
  it('accepts valid slugs', () => {
    expect(isValidSlug('a')).toBe(true);
    expect(isValidSlug('ab')).toBe(true);
    expect(isValidSlug('a-b')).toBe(true);
    expect(isValidSlug('database-schema')).toBe(true);
    expect(isValidSlug('topic123')).toBe(true);
  });

  it('rejects invalid slugs', () => {
    expect(isValidSlug('')).toBe(false);
    expect(isValidSlug('A')).toBe(false);
    expect(isValidSlug('1abc')).toBe(false);
    expect(isValidSlug('-ab')).toBe(false);
    expect(isValidSlug('ab-')).toBe(false);
    expect(isValidSlug('a_b')).toBe(false);
  });
});

describe('getActiveReferencePrefix', () => {
  it('extracts prefix after #', () => {
    expect(getActiveReferencePrefix('text #dat', 9)).toBe('dat');
  });

  it('returns empty string right after #', () => {
    expect(getActiveReferencePrefix('text #', 6)).toBe('');
  });

  it('returns null when not in a reference', () => {
    expect(getActiveReferencePrefix('plain text', 10)).toBe(null);
  });

  it('returns null at start without #', () => {
    expect(getActiveReferencePrefix('hello', 5)).toBe(null);
  });
});

describe('dedupeBySlug', () => {
  it('keeps first occurrence', () => {
    const refs = parseReferences('#schema and #schema again');
    const deduped = dedupeBySlug(refs);
    expect(deduped).toHaveLength(1);
    expect(deduped[0].offset).toBe(0);
  });

  it('preserves unique slugs in order', () => {
    const refs = parseReferences('#a #b #a #c');
    const deduped = dedupeBySlug(refs);
    expect(deduped).toHaveLength(3);
    expect(deduped.map((r) => r.slug)).toEqual(['a', 'b', 'c']);
  });
});
