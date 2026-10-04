/**
 * Hermes Canopy — safe JSON formatter
 *
 * Serializes untrusted values for display as plain text. Two hard caps:
 *
 *   - maxDepth: nesting deeper than this is replaced with a marker instead of
 *     being expanded, so a deeply nested or self-referential value can never
 *     blow up the serializer.
 *   - maxBytes: the returned UTF-8 string never exceeds this byte count. When
 *     the serialized value is too large, it is cut at a valid code-point
 *     boundary and a truncation marker is appended.
 *
 * This formatter is display-only. Its output is only ever rendered as a React
 * text node (or inside a <pre>), never as HTML. It is the single choke point
 * the tool-call renderer uses for `params` and `result`.
 */

export const DEFAULT_JSON_MAX_DEPTH = 6;
export const DEFAULT_JSON_MAX_BYTES = 16_000;

export interface SafeJsonOptions {
  maxDepth?: number;
  maxBytes?: number;
}

const DEPTH_MARKER = '…';
const BYTES_MARKER = '\n… [truncated]';

function safePrimitive(value: unknown): string {
  if (value === null) return 'null';
  if (typeof value === 'string') return JSON.stringify(value);
  if (typeof value === 'number') return Number.isFinite(value) ? String(value) : 'null';
  if (typeof value === 'boolean') return String(value);
  if (typeof value === 'bigint') return JSON.stringify(String(value));
  // undefined, functions, and symbols have no JSON representation.
  if (typeof value === 'undefined' || typeof value === 'function' || typeof value === 'symbol') return 'null';
  return JSON.stringify(String(value));
}

function indent(depth: number): string {
  return '  '.repeat(depth);
}

function stringifyNode(value: unknown, depth: number, maxDepth: number): string {
  if (depth > maxDepth) return JSON.stringify(DEPTH_MARKER);
  if (value === null || typeof value !== 'object') return safePrimitive(value);

  if (Array.isArray(value)) {
    if (value.length === 0) return '[]';
    const lines = value.map((item) => indent(depth + 1) + stringifyNode(item, depth + 1, maxDepth));
    return '[\n' + lines.join(',\n') + '\n' + indent(depth) + ']';
  }

  const entries = Object.entries(value as Record<string, unknown>);
  if (entries.length === 0) return '{}';
  const lines = entries.map(
    ([key, item]) => indent(depth + 1) + JSON.stringify(key) + ': ' + stringifyNode(item, depth + 1, maxDepth),
  );
  return '{\n' + lines.join(',\n') + '\n' + indent(depth) + '}';
}

function encoder(): TextEncoder {
  return new TextEncoder();
}

/** Cut `text` to the longest code-point prefix whose UTF-8 size fits `maxBytes`. */
function truncateToBytes(text: string, maxBytes: number): string {
  if (maxBytes <= 0) return '';
  if (encoder().encode(text).length <= maxBytes) return text;

  const points = Array.from(text);
  let lo = 0;
  let hi = points.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (encoder().encode(points.slice(0, mid).join('')).length <= maxBytes) lo = mid;
    else hi = mid - 1;
  }
  return points.slice(0, lo).join('');
}

export function formatJsonSafe(value: unknown, options: SafeJsonOptions = {}): string {
  const maxDepth = Math.max(0, options.maxDepth ?? DEFAULT_JSON_MAX_DEPTH);
  const maxBytes = Math.max(0, options.maxBytes ?? DEFAULT_JSON_MAX_BYTES);

  let serialized: string;
  try {
    serialized = stringifyNode(value, 0, maxDepth);
  } catch {
    return JSON.stringify('[unserializable]');
  }

  if (encoder().encode(serialized).length <= maxBytes) return serialized;

  const markerBytes = encoder().encode(BYTES_MARKER).length;
  if (maxBytes <= markerBytes) return truncateToBytes(BYTES_MARKER, maxBytes);
  const head = truncateToBytes(serialized, maxBytes - markerBytes);
  return head + BYTES_MARKER;
}
