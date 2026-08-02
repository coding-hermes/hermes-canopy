/**
 * Unit tests — node card text helpers (UI-04 branching tree canvas)
 *
 * Small derivations, but each one has a failure mode that reaches the
 * screen: "Invalid Date" in a card corner, an ellipsis on text that was
 * never truncated, or a raw author id where a name should be.
 */

import { describe, it, expect } from 'vitest';
import { formatNodeTime, nodeAuthorName, previewText } from '../nodeCard';

describe('nodeAuthorName', () => {
  it('resolves a known identity', () => {
    const names = new Map([['u-1', 'Sarah Chen']]);
    expect(nodeAuthorName('u-1', { names })).toBe('Sarah Chen');
  });

  it('humanises an unknown id', () => {
    expect(nodeAuthorName('demo_user_1')).toBe('Demo User 1');
  });

  it('names the local author "You"', () => {
    expect(nodeAuthorName('local')).toBe('You');
  });

  it('labels an opaque agent id', () => {
    expect(nodeAuthorName('deadbeefcafe', { isAgent: true })).toBe('Agent');
  });
});

describe('formatNodeTime', () => {
  it('renders HH:MM', () => {
    expect(formatNodeTime('2026-08-01T09:48:00Z')).toMatch(/\d{1,2}:\d{2}/);
  });

  it('returns empty string rather than "Invalid Date"', () => {
    expect(formatNodeTime('not-a-date')).toBe('');
    expect(formatNodeTime('')).toBe('');
  });

  it('never leaks the word Invalid', () => {
    expect(formatNodeTime('2026-13-45T99:99:99Z')).not.toContain('Invalid');
  });
});

describe('previewText', () => {
  it('passes short text through untouched', () => {
    expect(previewText('hello')).toBe('hello');
  });

  it('trims surrounding whitespace so cards do not lose a line', () => {
    expect(previewText('\n\n  hi  \n')).toBe('hi');
  });

  it('truncates with an ellipsis past the limit', () => {
    const out = previewText('x'.repeat(200));
    expect(out).toHaveLength(121); // 120 chars + ellipsis
    expect(out.endsWith('…')).toBe(true);
  });

  it('does not append an ellipsis at exactly the limit', () => {
    const out = previewText('y'.repeat(120));
    expect(out.endsWith('…')).toBe(false);
    expect(out).toHaveLength(120);
  });

  it('honours a custom limit', () => {
    expect(previewText('abcdef', 3)).toBe('abc…');
  });

  it('survives an empty or nullish body', () => {
    expect(previewText('')).toBe('');
    expect(previewText(undefined as unknown as string)).toBe('');
  });
});
