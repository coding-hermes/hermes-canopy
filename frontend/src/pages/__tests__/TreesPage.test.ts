/**
 * Unit tests — TreesPage load-more append/dedupe (PAG-001)
 *
 * Covers the pure merge helper that backs cursor pagination: appending the
 * next page of trees without introducing duplicates when create/delete
 * handlers or a cursor-boundary refetch have already mutated the list.
 */

import { describe, it, expect } from 'vitest';
import { appendTrees, buildCreateTreePayload } from '../TreesPage';

type TreeSummary = {
  id: string;
  title: string;
  description: string;
};

function tree(id: string): TreeSummary {
  return {
    id,
    title: `Tree ${id}`,
    description: '',
  };
}

describe('appendTrees', () => {
  it('appends a disjoint page and preserves order', () => {
    const existing = [tree('a'), tree('b')];
    const incoming = [tree('c'), tree('d')];
    expect(appendTrees(existing, incoming).map((t) => t.id)).toEqual(['a', 'b', 'c', 'd']);
  });

  it('dedupes by id and keeps the first (existing) occurrence', () => {
    const existing = [tree('a'), tree('b')];
    const incoming = [tree('b'), tree('c')];
    const merged = appendTrees(existing, incoming);
    expect(merged.map((t) => t.id)).toEqual(['a', 'b', 'c']);
    // The already-loaded copy wins — not the incoming duplicate.
    expect(merged[1]).toBe(existing[1]);
  });

  it('appends nothing when the incoming page is already present', () => {
    const existing = [tree('a'), tree('b'), tree('c')];
    const incoming = [tree('a'), tree('b'), tree('c')];
    expect(appendTrees(existing, incoming)).toHaveLength(3);
  });

  it('treats an empty incoming page as a no-op', () => {
    const existing = [tree('a')];
    expect(appendTrees(existing, [])).toEqual(existing);
  });

  it('handles an empty existing list', () => {
    const incoming = [tree('x'), tree('y')];
    expect(appendTrees([], incoming).map((t) => t.id)).toEqual(['x', 'y']);
  });
});

describe('buildCreateTreePayload', () => {
  it('includes a rootMessage with nodeType message when content is provided', () => {
    const body = buildCreateTreePayload('My Tree', '', 'Hello world');
    expect(body).not.toBeNull();
    expect(body!.title).toBe('My Tree');
    expect(body!.rootMessage).toEqual({
      content: 'Hello world',
      contentFormat: 'markdown',
      nodeType: 'message',
    });
  });

  it('omits description when empty', () => {
    const body = buildCreateTreePayload('My Tree', '', 'Hello world');
    expect(body).not.toBeNull();
    expect(body).not.toHaveProperty('description');
  });

  it('includes description when provided', () => {
    const body = buildCreateTreePayload('My Tree', 'A description', 'Hello world');
    expect(body).not.toBeNull();
    expect(body!.description).toBe('A description');
  });

  it('returns null when title is missing', () => {
    expect(buildCreateTreePayload('', 'desc', 'Hello world')).toBeNull();
  });

  it('returns null when root content is missing (title-only submit must not reach the API)', () => {
    expect(buildCreateTreePayload('My Tree', '', '')).toBeNull();
    expect(buildCreateTreePayload('My Tree', '', '   ')).toBeNull();
  });

  it('trims surrounding whitespace from title and content', () => {
    const body = buildCreateTreePayload('  My Tree  ', '', '  Hello  ');
    expect(body!.title).toBe('My Tree');
    expect((body!.rootMessage as { content: string }).content).toBe('Hello');
  });
});
