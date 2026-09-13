/**
 * Hermes Canopy — frontend viewer registry (SPEC-PL-02 §6).
 *
 * Fetches GET /api/v1/viewers once, caches the registrations in memory,
 * and builds the mime/extension/hint dispatch tables (spec §6.1 "Build
 * in-memory viewer dispatch table").
 *
 * selectViewer precedence (spec §6.4, frontend tier — overrides are a
 * later phase):
 *   1. explicit hint (caller-supplied viewerHint param, e.g. FileNode.viewerHint)
 *   2. file.viewerHint when the descriptor advertises it via supportsViewerHint
 *   3. mimeType against each descriptor's supportsMime globs
 *   4. extension against each descriptor's supportsExtensions
 * Quarantined or not-viewable files never render: null. No match: null
 * (caller renders the download-only / empty state).
 */

import { listViewers } from './fileApi';
import type { FileMetadata, ViewerRegistration } from '../types/fileviewer';

/** Glob matcher for viewer support lists: '*' wildcards, case-insensitive ('text/*' matches 'text/plain'). */
export function globMatch(pattern: string, value: string): boolean {
  const escaped = pattern.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.');
  return new RegExp(`^${escaped}$`, 'i').test(value);
}

export type ViewerSelection = {
  file: FileMetadata;
  viewer: ViewerRegistration;
  /** Which tier matched: 'hint' (explicit caller hint), 'mime', 'extension'. */
  via: 'hint' | 'mime' | 'extension';
};

// ── In-memory cache ─────────────────────────────────────────

let cache: ViewerRegistration[] | null = null;
let cachePromise: Promise<ViewerRegistration[]> | null = null;

/** True when the registry already holds a fetched snapshot. */
export function hasCachedViewers(): boolean {
  return cache !== null;
}

/** Test seam: clears the module-level cache. */
export function resetViewerRegistryCache(): void {
  cache = null;
  cachePromise = null;
}

/**
 * GET /viewers once per session (spec §6.1). Concurrent callers share the
 * in-flight request; a failed fetch clears the cache so the next call
 * retries. `force` bypasses a populated cache.
 */
export async function fetchViewers(force = false): Promise<ViewerRegistration[]> {
  if (!force && cache !== null) return cache;
  if (!force && cachePromise !== null) return cachePromise;
  const p = listViewers()
    .then((viewers) => {
      cache = viewers;
      cachePromise = null;
      return viewers;
    })
    .catch((err: unknown) => {
      cachePromise = null;
      throw err;
    });
  cachePromise = p;
  return p;
}

/**
 * Builds the dispatch tables from a /viewers payload. Exported for tests
 * and for callers that already hold the registrations (no fetch).
 */
export function buildViewerTables(viewers: ViewerRegistration[]): {
  bySlug: Map<string, ViewerRegistration>;
  mimeToViewer: Map<string, ViewerRegistration>;
  extensionToViewer: Map<string, ViewerRegistration>;
  hintToViewer: Map<string, ViewerRegistration>;
} {
  const bySlug = new Map<string, ViewerRegistration>();
  const mimeToViewer = new Map<string, ViewerRegistration>();
  const extensionToViewer = new Map<string, ViewerRegistration>();
  const hintToViewer = new Map<string, ViewerRegistration>();
  for (const viewer of viewers) {
    if (!viewer.isActive) continue;
    bySlug.set(viewer.viewerSlug, viewer);
    for (const hint of viewer.supportsViewerHint) hintToViewer.set(hint, viewer);
    for (const mime of viewer.supportsMime) {
      if (!mimeToViewer.has(mime)) mimeToViewer.set(mime, viewer);
    }
    for (const ext of viewer.supportsExtensions) {
      const key = ext.toLowerCase();
      if (!extensionToViewer.has(key)) extensionToViewer.set(key, viewer);
    }
  }
  return { bySlug, mimeToViewer, extensionToViewer, hintToViewer };
}

/**
 * Picks the viewer for a file.
 *
 * @param file full file metadata (use getFile / a list row)
 * @param opts.hint explicit caller hint (e.g. FileNode.viewerHint) — beats mime
 * @param opts.viewers optional pre-fetched registrations (skips the fetch)
 * @returns selection with the matched viewer, or null when the file is
 *          quarantined / not viewable / has no matching viewer.
 */
export async function selectViewer(
  file: FileMetadata,
  opts: { hint?: string; viewers?: ViewerRegistration[] } = {},
): Promise<ViewerSelection | null> {
  const viewers = opts.viewers ?? (await fetchViewers());

  // A quarantined file never renders (FILE_QUARANTINED on stream anyway);
  // isViewable=false means download-only by server classification.
  if (file.quarantined || !file.isViewable) return null;

  const tables = buildViewerTables(viewers);

  // 1. Explicit caller hint wins outright — but only if a registered
  //    viewer actually claims the slug.
  const explicit = opts.hint ? tables.bySlug.get(opts.hint) : undefined;
  if (explicit) return { file, viewer: explicit, via: 'hint' };

  // 2. Server-detected hint, honored only when the descriptor declares it.
  if (file.viewerHint) {
    const hinted = tables.hintToViewer.get(file.viewerHint);
    if (hinted) return { file, viewer: hinted, via: 'hint' };
  }

  // 3. MIME match — exact key first, then glob patterns ('text/*').
  const exact = tables.mimeToViewer.get(file.mimeType);
  if (exact) return { file, viewer: exact, via: 'mime' };
  for (const viewer of viewers) {
    if (!viewer.isActive) continue;
    for (const pattern of viewer.supportsMime) {
      if (pattern.includes('*') && globMatch(pattern, file.mimeType)) {
        return { file, viewer, via: 'mime' };
      }
    }
  }

  // 4. Extension match (case-insensitive).
  const ext = file.extension.toLowerCase();
  const byExt = tables.extensionToViewer.get(ext);
  if (byExt) return { file, viewer: byExt, via: 'extension' };

  // 5. Unsupported → null; caller renders download-only.
  return null;
}
