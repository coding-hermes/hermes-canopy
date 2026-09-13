/**
 * Hermes Canopy — JSON viewer pure logic (SPEC-PL-02 §9.6, phase 3).
 *
 * Unit-testable helpers consumed by jsonViewerBody.ts (the in-iframe shell).
 * Everything that can be tested by vitest without a DOM lives here:
 *   - stripJsonComments: JSONC comment stripping (// and  block comments,
 *     string- and regex-shape aware: `//` inside a string literal or right
 *     after `https:` survives; `/*`-style comment openers inside strings survive).
 *   - parseJsonc: tolerant parse (comments + trailing commas) → { value } or
 *     { error } with line/column from the underlying SyntaxError position.
 *   - buildJsonPath / appendJsonPath: `$.a[0].b` path strings, bracket
 *     notation for keys that are not plain identifiers.
 *   - jsonTypeName / describeType: color-coding + display helpers.
 *   - matchJsonNodes: recursive search for the search bar.
 *
 * The BODY (jsonViewerBody.ts) contains none of this logic — it only wires
 * these helpers to the DOM tree inside the sandboxed frame.
 */

// ── JSONC comment stripping ─────────────────────────────────

/**
 * Strips `//` line comments and `/* ... *​/` block comments from JSONC,
 * preserving string literals exactly. A `/` is only a comment opener when
 * it is NOT inside a string and is not part of a URL scheme (`https:`).
 * Returns the source with comments replaced by spaces (positions and
 * line/column of later tokens are preserved).
 */
export function stripJsonComments(source: string): string {
  const out: string[] = [];
  let inString = false;
  let escaped = false;
  let i = 0;
  while (i < source.length) {
    const ch = source[i];
    if (inString) {
      out.push(ch);
      if (escaped) {
        escaped = false;
      } else if (ch === '\\') {
        escaped = true;
      } else if (ch === '"') {
        inString = false;
      }
      i += 1;
      continue;
    }
    if (ch === '"') {
      inString = true;
      out.push(ch);
      i += 1;
      continue;
    }
    if (ch === '/' && source[i + 1] === '/') {
      // A `//` line comment — unless it is a URL scheme like `https://`.
      const before = out.slice(-1)[0] ?? '';
      if (before === ':') {
        out.push(ch);
        i += 1;
        continue;
      }
      while (i < source.length && source[i] !== '\n') {
        out.push(' ');
        i += 1;
      }
      continue;
    }
    if (ch === '/' && source[i + 1] === '*') {
      const before = out.slice(-1)[0] ?? '';
      if (before === ':') {
        out.push(ch);
        i += 1;
        continue;
      }
      // Blank the comment but PRESERVE length and newlines, so line/column
      // positions after stripping still map onto the original document.
      out.push(' ', ' '); // the /* opener
      i += 2;
      while (i < source.length && !(source[i] === '*' && source[i + 1] === '/')) {
        out.push(source[i] === '\n' ? '\n' : ' ');
        i += 1;
      }
      if (i < source.length) {
        out.push(' ', ' '); // the */ closer
        i += 2;
      }
      continue;
    }
    out.push(ch);
    i += 1;
  }
  return out.join('');
}

// ── Trailing-comma tolerance ────────────────────────────────

/**
 * Removes trailing commas before `}` or `]` (JSONC tolerance). Only
 * whitespace (incl. comments already blanked) may sit between the comma
 * and the closer.
 */
export function stripTrailingCommas(source: string): string {
  let out = '';
  let inString = false;
  let escaped = false;
  for (let i = 0; i < source.length; i += 1) {
    const ch = source[i];
    if (inString) {
      out += ch;
      if (escaped) escaped = false;
      else if (ch === '\\') escaped = true;
      else if (ch === '"') inString = false;
      continue;
    }
    if (ch === '"') {
      inString = true;
      out += ch;
      continue;
    }
    if (ch === ',') {
      let j = i + 1;
      while (j < source.length && /\s/.test(source[j])) j += 1;
      if (source[j] === '}' || source[j] === ']') {
        continue; // drop the comma
      }
    }
    out += ch;
  }
  return out;
}

// ── Tolerant parse ──────────────────────────────────────────

export type JsoncParseResult =
  | { ok: true; value: unknown }
  | { ok: false; error: { code: string; message: string; line: number; column: number } };

/** True when the input carries real structure (an object or an array root). */
export function isStructuredRoot(value: unknown): boolean {
  return typeof value === 'object' && value !== null;
}

// ── Strict JSON validation (position-tracked) ───────────────

/** Shape of the error thrown by validateStrictJson (plain object: serialization-safe). */
export interface JsoncSyntaxIssue {
  __jsoncSyntaxIssue: true;
  message: string;
  line: number;
  column: number;
}

export function isJsoncSyntaxIssue(err: unknown): err is JsoncSyntaxIssue {
  return typeof err === 'object' && err !== null && (err as { __jsoncSyntaxIssue?: unknown }).__jsoncSyntaxIssue === true;
}

/**
 * Strict single-pass JSON validator over comment-blanked, trailing-comma-
 * stripped source. Tracks line/column as it walks and throws a
 * JsoncSyntaxIssue-shaped object with an exact, deterministic position —
 * engine JSON.parse messages do NOT carry "position N" on modern V8, so
 * this replaces relying on SyntaxError message parsing. Self-contained: its
 * serialized source runs inside the viewer iframe bundle (jsonHelpers).
 * Value semantics identical to JSON.parse for valid input.
 */
export function validateStrictJson(source: string): unknown {
  let pos = 0;
  let line = 1;
  let column = 1;

  function advance(): void {
    if (source[pos] === '\n') {
      line += 1;
      column = 1;
    } else {
      column += 1;
    }
    pos += 1;
  }
  function skipWs(): void {
    while (pos < source.length && /\s/.test(source[pos])) advance();
  }
  function fail(message: string): never {
    throw { __jsoncSyntaxIssue: true as const, message, line, column };
  }
  function expectChar(ch: string): void {
    skipWs();
    if (source[pos] !== ch) fail(`Expected '${ch}'`);
    advance();
  }
  function parseString(): string {
    skipWs();
    if (source[pos] !== '"') fail('Expected string');
    advance();
    let out = '';
    while (true) {
      if (pos >= source.length) fail('Unterminated string');
      const ch = source[pos];
      if (ch === '"') {
        advance();
        return out;
      }
      if (ch === '\\') {
        advance();
        if (pos >= source.length) fail('Unterminated escape');
        const esc = source[pos];
        if (esc === 'u') {
          let hex = '';
          for (let k = 0; k < 4; k += 1) {
            advance();
            if (pos >= source.length) fail('Invalid \\u escape');
            hex += source[pos];
          }
          advance();
          const code = Number.parseInt(hex, 16);
          if (Number.isNaN(code)) fail('Invalid \\u escape');
          out += String.fromCharCode(code);
          continue;
        }
        const escapes: Record<string, string> = { '"': '"', '\\': '\\', '/': '/', b: '\b', f: '\f', n: '\n', r: '\r', t: '\t' };
        if (!(esc in escapes)) fail(`Invalid escape \\${esc}`);
        out += escapes[esc];
        advance();
        continue;
      }
      if (ch < ' ') fail('Control character in string');
      out += ch;
      advance();
    }
  }
  function parseNumber(): number {
    skipWs();
    const start = pos;
    if (source[pos] === '-') advance();
    if (source[pos] === '0') {
      advance();
    } else if (/[1-9]/.test(source[pos] ?? '')) {
      advance();
      while (/[0-9]/.test(source[pos] ?? '')) advance();
    } else {
      fail('Invalid number');
    }
    if (source[pos] === '.') {
      advance();
      if (!/[0-9]/.test(source[pos] ?? '')) fail('Invalid number');
      while (/[0-9]/.test(source[pos] ?? '')) advance();
    }
    if (source[pos] === 'e' || source[pos] === 'E') {
      advance();
      if (source[pos] === '+' || source[pos] === '-') advance();
      if (!/[0-9]/.test(source[pos] ?? '')) fail('Invalid number');
      while (/[0-9]/.test(source[pos] ?? '')) advance();
    }
    return Number(source.slice(start, pos));
  }
  function parseLiteral(): unknown {
    for (const [lit, val] of [
      ['true', true],
      ['false', false],
      ['null', null],
    ] as Array<[string, unknown]>) {
      if (source.startsWith(lit, pos)) {
        for (let k = 0; k < lit.length; k += 1) advance();
        return val;
      }
    }
    fail('Unexpected token');
  }
  function parseValue(): unknown {
    skipWs();
    if (pos >= source.length) fail('Unexpected end of input');
    const ch = source[pos];
    if (ch === '{') return parseObject();
    if (ch === '[') return parseArray();
    if (ch === '"') return parseString();
    if (ch === '-' || /[0-9]/.test(ch)) return parseNumber();
    return parseLiteral();
  }
  function parseObject(): Record<string, unknown> {
    expectChar('{');
    const obj: Record<string, unknown> = {};
    skipWs();
    if (source[pos] === '}') {
      advance();
      return obj;
    }
    while (true) {
      skipWs();
      if (pos >= source.length) fail('Unterminated object');
      const key = parseString();
      expectChar(':');
      obj[key] = parseValue();
      skipWs();
      if (source[pos] === ',') {
        advance();
        continue;
      }
      if (source[pos] === '}') {
        advance();
        return obj;
      }
      fail("Expected ',' or '}'");
    }
  }
  function parseArray(): unknown[] {
    expectChar('[');
    const arr: unknown[] = [];
    skipWs();
    if (source[pos] === ']') {
      advance();
      return arr;
    }
    while (true) {
      arr.push(parseValue());
      skipWs();
      if (source[pos] === ',') {
        advance();
        continue;
      }
      if (source[pos] === ']') {
        advance();
        return arr;
      }
      fail("Expected ',' or ']'");
    }
  }

  const value = parseValue();
  skipWs();
  if (pos < source.length) fail('Unexpected trailing characters');
  return value;
}

/**
 * JSONC parse: strips comments + trailing commas, then validates with the
 * strict position-tracking parser. On failure, reports code, message, and
 * the exact line/column (1-based) of the offending token.
 */
export function parseJsonc(source: string): JsoncParseResult {
  const stripped = stripTrailingCommas(stripJsonComments(source));
  try {
    return { ok: true, value: validateStrictJson(stripped) };
  } catch (err: unknown) {
    if (isJsoncSyntaxIssue(err)) {
      return {
        ok: false,
        error: { code: 'JSON_PARSE_ERROR', message: err.message, line: err.line, column: err.column },
      };
    }
    return {
      ok: false,
      error: { code: 'JSON_PARSE_ERROR', message: err instanceof Error ? err.message : String(err), line: 1, column: 1 },
    };
  }
}

// ── Config sanitizers (§9.6 props; garbage-tolerant) ────────

/** Collapse depth from config: integer clamped to [1, 8], default 3. */
export function collapseDepthFromConfig(raw: unknown, fallback = 3): number {
  const n = typeof raw === 'number' ? raw : Number(raw);
  if (!Number.isFinite(n)) return fallback;
  return Math.min(8, Math.max(1, Math.round(n)));
}

/** Boolean viewer flag from config (enableSearch / enableCopy); default true. */
export function flagFromConfig(raw: unknown, fallback = true): boolean {
  if (typeof raw === 'boolean') return raw;
  return fallback;
}

// ── Path builder (`$.users[0].email`) ───────────────────────

/** Single key → `.key` or `["odd key"]` for non-identifier keys. */
export function appendJsonPath(base: string, key: string): string {
  const identifier = /^[A-Za-z_$][A-Za-z0-9_$]*$/;
  return identifier.test(key) ? `${base}.${key}` : `${base}[${JSON.stringify(key)}]`;
}

/** Index → `[n]`. */
export function appendJsonIndex(base: string, index: number): string {
  return `${base}[${index}]`;
}

/**
 * Builds a canonical `$`-rooted path from a chain of segments
 * (keys and numeric array indices in document order).
 */
export function buildJsonPath(segments: Array<string | number>): string {
  let path = '$';
  for (const segment of segments) {
    if (typeof segment === 'number') path = appendJsonIndex(path, segment);
    else path = appendJsonPath(path, segment);
  }
  return path;
}

// ── Type helpers ────────────────────────────────────────────

/** Canonical type name for color coding: string/number/boolean/null/object/array. */
export function jsonTypeName(value: unknown): string {
  if (value === null) return 'null';
  if (Array.isArray(value)) return 'array';
  return typeof value; // 'string' | 'number' | 'boolean' | 'object'
}

/** Child count for collapsed-node badges; null for leaves. */
export function jsonChildCount(value: unknown): number | null {
  if (Array.isArray(value)) return value.length;
  if (typeof value === 'object' && value !== null) return Object.keys(value).length;
  return null;
}

/** Truncated scalar preview for collapsed containers / search results. */
export function scalarPreview(value: unknown, maxLen = 40): string {
  if (typeof value === 'string') {
    const truncated = value.length > maxLen ? `${value.slice(0, maxLen)}…` : value;
    return JSON.stringify(truncated);
  }
  if (value === null || typeof value !== 'object') return String(value);
  return value instanceof Array ? 'Array' : 'Object';
}

// ── Search ──────────────────────────────────────────────────

export interface JsonSearchHit {
  /** Canonical `$` path of the KEY/entry whose key or scalar value matched. */
  path: string;
  /** Document-order segments for scrolling to the node in the tree. */
  segments: Array<string | number>;
}

/**
 * Depth-first, document-order search over keys and scalar values
 * (string/number/boolean/null — containers are searched THROUGH, not
 * matched as values). Case-insensitive substring on the string form.
 */
export function matchJsonNodes(root: unknown, query: string): JsonSearchHit[] {
  const q = query.toLowerCase();
  const hits: JsonSearchHit[] = [];

  function walk(value: unknown, segments: Array<string | number>): void {
    if (Array.isArray(value)) {
      value.forEach((child, index) => walk(child, [...segments, index]));
      return;
    }
    if (typeof value === 'object' && value !== null) {
      for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
        if (q !== '' && key.toLowerCase().includes(q)) {
          hits.push({ path: buildJsonPath([...segments, key]), segments: [...segments, key] });
        }
        walk(child, [...segments, key]);
      }
      return;
    }
    // Leaf scalars: match on their display form (stringified; strings unquoted).
    const display = typeof value === 'string' ? value : String(value);
    if (q !== '' && display.toLowerCase().includes(q)) {
      hits.push({ path: buildJsonPath(segments), segments: [...segments] });
    }
  }

  walk(root, []);
  return hits;
}
