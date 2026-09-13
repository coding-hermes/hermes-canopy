/**
 * Unit tests — JSON viewer pure logic (SPEC-PL-02 §9.6, phase 3).
 * Covers the JSONC tolerant parse (comments line+block, trailing commas,
 * nested, position info on malformed input), the `$` path builder (incl.
 * bracket notation), collapse-depth defaulting, type/child-count helpers,
 * search matching, and the SERIALIZED helper bundle (jsonHelpers) that the
 * in-iframe body actually executes — it must behave identically standalone
 * and serialized.
 */

import { describe, it, expect } from 'vitest';
import {
  appendJsonIndex,
  appendJsonPath,
  buildJsonPath,
  collapseDepthFromConfig,
  flagFromConfig,
  isStructuredRoot,
  jsonChildCount,
  jsonTypeName,
  matchJsonNodes,
  parseJsonc,
  scalarPreview,
  stripJsonComments,
  stripTrailingCommas,
} from '../jsonViewerLogic';
import { jsonHelpers } from '../jsonViewerBody';

describe('stripJsonComments', () => {
  it('strips line comments (length-preserving)', () => {
    // `// hello` (8 chars) blanked; the two leading spaces before it survive → 10 spaces.
    expect(stripJsonComments('{\n  // hello\n  "a": 1\n}')).toBe('{\n' + ' '.repeat(10) + '\n  "a": 1\n}');
  });

  it('strips block comments, preserving length and newlines', () => {
    const src = '{ /* one */ "a": /* two\nlines */ 1 }';
    const out = stripJsonComments(src);
    expect(out).not.toContain('one');
    expect(out).not.toContain('lines');
    expect(out.length).toBe(src.length);
    expect(out.match(/\n/g)?.length).toBe(src.match(/\n/g)?.length);
    const parsed = parseJsonc(src);
    expect(parsed.ok).toBe(true);
    if (parsed.ok) expect(parsed.value).toEqual({ a: 1 });
  });

  it('preserves comment-like sequences inside strings', () => {
    const src = '{"url": "https://x.test/a", "note": "keep /* this */ and // that"}';
    expect(stripJsonComments(src)).toBe(src);
  });

  it('preserves a URL scheme guard (// after : never starts a comment)', () => {
    expect(stripJsonComments('"a: https://b"')).toBe('"a: https://b"');
  });

  it('preserves positions (same length and newline count)', () => {
    const src = '{\n// c1\n"key": "v", /* c2 */ "k2": 2}';
    const out = stripJsonComments(src);
    expect(out.length).toBe(src.length);
    expect(out.match(/\n/g)?.length).toBe(src.match(/\n/g)?.length);
  });

  it('unterminated block comment is blanked and surfaces a strict parse error', () => {
    const out = stripJsonComments('{"a": 1 /* never closed');
    expect(out).toBe('{"a": 1 ' + ' '.repeat(15));
    const result = parseJsonc('{"a": 1 /* never closed');
    expect(result.ok).toBe(false);
    if (!result.ok) {
      // The object is never closed; the parser reports the missing brace.
      expect(result.error.message).toBe("Expected ',' or '}'");
      expect(result.error.line).toBe(1);
    }
  });
});

describe('stripTrailingCommas', () => {
  it('removes trailing commas in objects and arrays', () => {
    expect(stripTrailingCommas('{"a": 1,}')).toBe('{"a": 1}');
    expect(stripTrailingCommas('[1, 2, 3,]')).toBe('[1, 2, 3]');
    expect(stripTrailingCommas('[1, 2, 3,\n]')).toBe('[1, 2, 3\n]');
  });

  it('keeps commas inside strings', () => {
    expect(stripTrailingCommas('{"a": "x,}"}')).toBe('{"a": "x,}"}');
  });

  it('keeps meaningful commas (between members)', () => {
    expect(stripTrailingCommas('{"a": 1, "b": 2}')).toBe('{"a": 1, "b": 2}');
  });
});

describe('parseJsonc', () => {
  it('parses clean JSON', () => {
    expect(parseJsonc('{"a": [1, 2]}')).toEqual({ ok: true, value: { a: [1, 2] } });
  });

  it('tolerates line + block comments and trailing commas (nested)', () => {
    const jsonc = `{
      // top note
      "users": [ /* list */ { "name": "lexi", }, ],
      "n": 1, // trailing
    }`;
    const result = parseJsonc(jsonc);
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.value).toEqual({ users: [{ name: 'lexi' }], n: 1 });
    }
  });

  it('reports exact position info on malformed input', () => {
    const result = parseJsonc('{\n  "a": 1,\n  "b": @@@\n}');
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error.code).toBe('JSON_PARSE_ERROR');
      expect(result.error.line).toBe(3);
      expect(result.error.column).toBe(8); // the '@'
      expect(result.error.message).toBe('Unexpected token');
    }
  });

  it('error positions are 1-based and point at the offending token', () => {
    const result = parseJsonc('{"a": 1 } extra');
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error.message).toBe('Unexpected trailing characters');
      expect(result.error.line).toBe(1);
      expect(result.error.column).toBe(11); // the 'e' of "extra" (1-based)
    }
  });

  it('serializes identically: jsonHelpers.parseJsonc matches parseJsonc', () => {
    const cases = ['{"a":1}', '{ /* c */ "a": [1,], }', '{ bad', '[]', 'null'];
    for (const src of cases) {
      expect(jsonHelpers.parseJsonc.call(jsonHelpers, src)).toEqual(parseJsonc(src));
    }
  });
});

describe('path builder', () => {
  it('builds dot/bracket paths for keys and indices', () => {
    expect(buildJsonPath(['users', 0, 'email'])).toBe('$.users[0].email');
    expect(buildJsonPath([])).toBe('$');
    expect(buildJsonPath(['a'])).toBe('$.a');
  });

  it('uses bracket notation for non-identifier keys', () => {
    expect(buildJsonPath(['odd key', 'with-dash', '2'])).toBe('$["odd key"]["with-dash"]["2"]');
  });

  it('escapes quotes inside bracketed keys', () => {
    expect(buildJsonPath(['say "hi"'])).toBe('$["say \\"hi\\""]');
  });

  it('appendJsonPath / appendJsonIndex compose', () => {
    expect(appendJsonPath('$', 'ok')).toBe('$.ok');
    expect(appendJsonPath('$', 'nope nope')).toBe('$["nope nope"]');
    expect(appendJsonIndex('$.a', 3)).toBe('$.a[3]');
  });

  it('serialized jsonHelpers.buildJsonPath matches buildJsonPath', () => {
    expect(jsonHelpers.buildJsonPath.call(jsonHelpers, ['users', 0, 'email'])).toBe('$.users[0].email');
    expect(jsonHelpers.buildJsonPath.call(jsonHelpers, ['odd key'])).toBe('$["odd key"]');
  });
});

describe('config sanitizers', () => {
  it('collapseDepthFromConfig defaults to 3 on garbage', () => {
    expect(collapseDepthFromConfig(undefined)).toBe(3);
    expect(collapseDepthFromConfig(Number.NaN)).toBe(3);
    expect(collapseDepthFromConfig('deep')).toBe(3);
  });

  it('collapseDepthFromConfig clamps to [1, 8] and rounds', () => {
    expect(collapseDepthFromConfig(0)).toBe(1);
    expect(collapseDepthFromConfig(99)).toBe(8);
    expect(collapseDepthFromConfig(5.6)).toBe(6);
    expect(collapseDepthFromConfig(4)).toBe(4);
  });

  it('flagFromConfig defaults true, passes explicit booleans', () => {
    expect(flagFromConfig(undefined)).toBe(true);
    expect(flagFromConfig(false)).toBe(false);
    expect(flagFromConfig(true)).toBe(true);
    expect(flagFromConfig(0)).toBe(true); // not a boolean → default
  });
});

describe('type & preview helpers', () => {
  it('jsonTypeName color-codes the six canonical types', () => {
    expect(jsonTypeName('x')).toBe('string');
    expect(jsonTypeName(1)).toBe('number');
    expect(jsonTypeName(true)).toBe('boolean');
    expect(jsonTypeName(null)).toBe('null');
    expect(jsonTypeName({})).toBe('object');
    expect(jsonTypeName([])).toBe('array');
  });

  it('jsonChildCount counts entries; leaves get null', () => {
    expect(jsonChildCount({ a: 1, b: 2 })).toBe(2);
    expect(jsonChildCount([1, 2, 3])).toBe(3);
    expect(jsonChildCount('x')).toBeNull();
    expect(jsonChildCount(null)).toBeNull();
  });

  it('scalarPreview quotes strings and truncates long ones', () => {
    expect(scalarPreview('hi')).toBe('"hi"');
    expect(scalarPreview('x'.repeat(50), 10)).toBe('"' + 'x'.repeat(10) + '…"');
    expect(scalarPreview(42)).toBe('42');
    expect(scalarPreview(null)).toBe('null');
  });

  it('isStructuredRoot distinguishes containers', () => {
    expect(isStructuredRoot({})).toBe(true);
    expect(isStructuredRoot([])).toBe(true);
    expect(isStructuredRoot('[]')).toBe(false);
    expect(isStructuredRoot(null)).toBe(false);
  });
});

describe('matchJsonNodes', () => {
  const doc = {
    users: [{ name: 'Ada', email: 'ada@x.test' }, { name: 'Lexi' }],
    meta: { count: 2, note: 'demo users list' },
  };

  it('matches keys and scalar values case-insensitively, document order', () => {
    const hits = matchJsonNodes(doc, 'lexi');
    // key 'name' does NOT contain 'lexi'; the scalar VALUE 'Lexi' does —
    // the hit path is the full path TO the value (JSONPath semantics).
    expect(hits).toHaveLength(1);
    expect(hits[0].path).toBe('$.users[1].name');
    // 'ada' matches both the name value and the email value
    expect(matchJsonNodes(doc, 'ada').map((h) => h.path)).toEqual(['$.users[0].name', '$.users[0].email']);
  });

  it('returns hits with segments usable for tree navigation', () => {
    const hits = matchJsonNodes(doc, 'email');
    expect(hits).toHaveLength(1);
    expect(hits[0].segments).toEqual(['users', 0, 'email']);
  });

  it('searches through containers but does not match them as values', () => {
    const hits = matchJsonNodes(doc, 'users');
    // key 'users' + the 'demo users list' scalar; never a container "value" hit
    expect(hits).toHaveLength(2);
    expect(hits.some((h) => h.path === '$.users')).toBe(true);
    expect(hits.some((h) => h.path === '$.meta.note')).toBe(true);
  });

  it('empty query matches nothing', () => {
    expect(matchJsonNodes(doc, '')).toHaveLength(0);
  });

  it('serialized jsonHelpers.matchJsonNodes matches matchJsonNodes', () => {
    expect(jsonHelpers.matchJsonNodes.call(jsonHelpers, doc, 'ada')).toEqual(matchJsonNodes(doc, 'ada'));
    expect(jsonHelpers.matchJsonNodes.call(jsonHelpers, doc, 'zeta')).toEqual(matchJsonNodes(doc, 'zeta'));
  });
});
