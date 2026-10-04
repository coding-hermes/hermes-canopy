/**
 * Hermes Canopy — iteration display helpers
 *
 * Pure, dependency-free formatting for the §8.2 renderer contract. Kept out of
 * the React components so each rule can be unit-tested in isolation and reused
 * across the search/code/thinking/tool renderers without divergence.
 */

/** Format a nonnegative millisecond duration. Returns '' for null/absent. */
export function formatDurationMs(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return '';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

/** Elapsed wall-clock between a start time and an end time (or `nowMs`). */
export function formatElapsed(
  startIso: string | null | undefined,
  endIso?: string | null,
  nowMs?: number,
): string {
  if (!startIso) return '';
  const start = new Date(startIso).getTime();
  if (!Number.isFinite(start)) return '';
  const end = endIso ? new Date(endIso).getTime() : (nowMs ?? Date.now());
  const resolvedEnd = Number.isFinite(end) ? end : Date.now();
  const ms = Math.max(0, resolvedEnd - start);
  if (ms < 1000) return `${ms}ms elapsed`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s elapsed`;
  return `${(ms / 60_000).toFixed(1)}m elapsed`;
}

/** Human-readable byte count for file metadata. */
export function formatByteSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '0 B';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
