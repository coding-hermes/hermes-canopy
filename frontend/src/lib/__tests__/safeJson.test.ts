import { describe, expect, it } from 'vitest';
import {
  DEFAULT_JSON_MAX_BYTES,
  DEFAULT_JSON_MAX_DEPTH,
  formatJsonSafe,
} from '../safeJson.ts';

describe('formatJsonSafe — depth cap', () => {
  it('replaces nesting beyond maxDepth with a marker instead of expanding', () => {
    const value = { a: { b: { c: { d: { e: { f: { g: 'deep' } } } } } } };
    const out = formatJsonSafe(value, { maxDepth: 2 });
    expect(out).toContain('…');
    expect(out).not.toContain('deep');
  });

  it('leaves shallow values intact under the default depth cap', () => {
    const out = formatJsonSafe({ a: 1, b: [2, 3], c: 'x' }, { maxDepth: DEFAULT_JSON_MAX_DEPTH });
    expect(out).toContain('"a": 1');
    expect(out).toContain('"c": "x"');
  });

  it('does not serialize deeper than the root when maxDepth is zero', () => {
    const out = formatJsonSafe({ a: { b: 1 } }, { maxDepth: 0 });
    expect(out).not.toContain('"b"');
    expect(out).toContain('…');
  });
});

describe('formatJsonSafe — byte cap', () => {
  it('caps a large string to maxBytes and appends a truncation marker', () => {
    const out = formatJsonSafe({ blob: 'x'.repeat(100_000) }, { maxBytes: 128 });
    expect(new TextEncoder().encode(out).length).toBeLessThanOrEqual(128);
    expect(out).toContain('[truncated]');
    expect(out.length).toBeLessThan(100_000);
  });

  it('stays within the default byte cap for a very large value', () => {
    const out = formatJsonSafe({ blob: 'y'.repeat(500_000) });
    expect(new TextEncoder().encode(out).length).toBeLessThanOrEqual(DEFAULT_JSON_MAX_BYTES);
    expect(out).toContain('[truncated]');
  });

  it('returns the full string when under the cap', () => {
    const out = formatJsonSafe({ small: 'ok' }, { maxBytes: 1024 });
    expect(out).toContain('"ok"');
    expect(out).not.toContain('[truncated]');
  });
});

describe('formatJsonSafe — robustness', () => {
  it('serializes unrepresentable primitives without throwing', () => {
    expect(formatJsonSafe(undefined)).toBe('null');
    expect(formatJsonSafe(() => 0)).toBe('null');
    expect(formatJsonSafe(Symbol('s'))).toBe('null');
    expect(formatJsonSafe(Number.NaN)).toBe('null');
    expect(formatJsonSafe(Number.POSITIVE_INFINITY)).toBe('null');
    expect(formatJsonSafe(123n)).toBe('"123"');
  });

  it('never throws on an empty or odd input', () => {
    expect(() => formatJsonSafe(null)).not.toThrow();
    expect(() => formatJsonSafe({ maxBytes: 0, maxDepth: 0 })).not.toThrow();
    expect(() => formatJsonSafe([], { maxBytes: 1 })).not.toThrow();
  });
});
