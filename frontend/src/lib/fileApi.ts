/**
 * Hermes Canopy — file viewer API client (SPEC-PL-02 phase 2).
 *
 * Client over the REAL backend routes mounted by internal/server/server.go
 * (PL02-P1): `r.Mount("/files", ...)` and `r.Mount("/viewers", ...)` under
 * /api/v1 — see internal/fileviewer/handlers.go Files()/Viewers() for the
 * per-route shapes.
 *
 * Errors: the backend answers non-2xx with the repo-wide envelope
 * { error: { code, message } } where code is a SPEC-PL-02 §10 catalog code
 * (internal/fileviewer/handlers.go fail()). We surface the CODE, not just
 * the message, so callers can branch on e.g. FILE_QUARANTINED.
 */

import { apiUrl, authInit, resolveApiToken } from './api';
import {
  FileAccessEntrySchema,
  FileListPageSchema,
  FileMetadataSchema,
  FileMetadataSlimSchema,
  ResolveFileOutputSchema,
  ViewerDispatchResultSchema,
  ViewerRegistrationSchema,
  parseViewersResponse,
  type FileAccessEntry,
  type FileListPage,
  type FileMetadata,
  type FileMetadataSlim,
  type ResolveFileOutput,
  type ViewerDispatchResult,
  type ViewerRegistration,
} from '../types/fileviewer';

// ── §10 Error catalog ───────────────────────────────────────

/** File-resolution codes (§10.1). */
export const FILE_RESOLUTION_ERRORS = [
  'FILE_NOT_FOUND_BY_HASH',
  'FILE_NOT_FOUND_BY_ID',
  'FILE_EMPTY',
  'FILE_TOO_LARGE',
  'FILENAME_INVALID',
  'MIME_DETECTION_FAILED',
  'FILE_QUARANTINED',
  'FILE_SOFT_DELETED',
  'PROFILE_NOT_FOUND',
  'PROFILE_QUOTA_EXCEEDED',
] as const;

/** Stream codes (§10.2). */
export const FILE_STREAM_ERRORS = [
  'STREAM_URL_INVALID',
  'STREAM_URL_EXPIRED',
  'RANGE_NOT_SATISFIABLE',
  'STREAM_INTERRUPTED',
  'STREAM_THROTTLED',
  'CHECKSUM_MISMATCH',
] as const;

/** Viewer codes (§10.3). */
export const FILE_VIEWER_ERRORS = [
  'VIEWER_NOT_FOUND',
  'VIEWER_DISABLED',
  'VIEWER_BUNDLE_MISSING',
  'VIEWER_BUNDLE_HASH_MISMATCH',
  'VIEWER_RENDER_TIMEOUT',
  'VIEWER_API_TIMEOUT',
  'VIEWER_SANDBOX_ERROR',
  'VIEWER_CRASHED',
  'VIEWER_CONFIG_INVALID',
  'VIEWER_OVERRIDE_CONFLICT',
] as const;

/** Thumbnail / preview codes (§10.4). */
export const FILE_PREVIEW_ERRORS = [
  'THUMBNAIL_GENERATION_FAILED',
  'THUMBNAIL_NOT_AVAILABLE',
  'PREVIEW_TEXT_EXTRACTION_FAILED',
  'PREVIEW_TEXT_TOO_LARGE',
  'METADATA_EXTRACTION_FAILED',
] as const;

/** Validation sub-codes (§10.5). */
export const FILE_VALIDATION_ERRORS = [
  'INVALID_SHA256',
  'INVALID_FILE_ID',
  'INVALID_VIEWER_SLUG',
  'INVALID_RANGE_HEADER',
  'OVERRIDE_PATTERN_INVALID',
  'VIEWER_HINT_UNKNOWN',
  'CONFIG_VALUE_OUT_OF_RANGE',
] as const;

/** Access-log codes (§10.6). */
export const FILE_ACCESS_LOG_ERRORS = [
  'ACCESS_LOG_APPEND_FAILED',
  'ACCESS_LOG_FORBIDDEN',
] as const;

/** Every SPEC-PL-02 §10 code in one set, for membership checks. */
export const FILE_VIEWER_ERROR_CODES: ReadonlySet<string> = new Set([
  ...FILE_RESOLUTION_ERRORS,
  ...FILE_STREAM_ERRORS,
  ...FILE_VIEWER_ERRORS,
  ...FILE_PREVIEW_ERRORS,
  ...FILE_VALIDATION_ERRORS,
  ...FILE_ACCESS_LOG_ERRORS,
]);

/**
 * Error thrown for every non-2xx from the /files and /viewers routes.
 * `code` is the §10 catalog code from the envelope; UNKNOWN_ERROR covers
 * non-JSON bodies (proxies, HTML error pages) and unrecognized codes.
 */
export class FileApiError extends Error {
  readonly code: string;
  readonly status: number;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = 'FileApiError';
    this.code = code;
    this.status = status;
  }
}

/** Extracts (status, §10 code, message) from a non-2xx response body. */
export async function mapFileApiError(res: Response): Promise<FileApiError> {
  let code = 'UNKNOWN_ERROR';
  let message = `HTTP ${res.status}`;
  try {
    const text = await res.text();
    if (text) {
      const parsed: unknown = JSON.parse(text);
      const inner = (parsed as { error?: { code?: unknown; message?: unknown } }).error;
      if (inner && typeof inner === 'object') {
        if (typeof inner.code === 'string' && inner.code) code = inner.code;
        if (typeof inner.message === 'string' && inner.message) message = inner.message;
      } else if (typeof inner === 'string' && inner) {
        // Legacy single-string envelope: { error: "msg" }
        message = inner;
      }
    }
  } catch {
    // Non-JSON body (proxy/HTML): keep the HTTP status message.
  }
  return new FileApiError(res.status, code, message);
}

async function expectOk(res: Response): Promise<Response> {
  if (!res.ok) throw await mapFileApiError(res);
  return res;
}

// ── Query types ─────────────────────────────────────────────

export type FileSort = 'created_desc' | 'created_asc' | 'name_asc' | 'size_desc' | 'last_accessed_desc';

export interface ListFilesParams {
  cursor?: string;
  limit?: number; // 1-200 (spec §5.2; server rejects out of bounds)
  sort?: FileSort;
  mimeFilter?: string;
  extensionFilter?: string;
  viewableOnly?: boolean;
}

// ── /files routes ───────────────────────────────────────────

/**
 * GET /files — paginated list for the acting profile.
 * Server response: { files: FileMetadataSlim[], pagination: { count } }.
 */
export async function listFiles(params: ListFilesParams = {}): Promise<FileListPage> {
  const qs = new URLSearchParams();
  if (params.cursor) qs.set('cursor', params.cursor);
  if (params.limit !== undefined) qs.set('limit', String(params.limit));
  if (params.sort) qs.set('sort', params.sort);
  if (params.mimeFilter) qs.set('mimeFilter', params.mimeFilter);
  if (params.extensionFilter) qs.set('extensionFilter', params.extensionFilter);
  if (params.viewableOnly === false) qs.set('viewableOnly', 'false');
  const query = qs.toString();
  const res = await expectOk(await fetch(apiUrl(`/files${query ? `?${query}` : ''}`), authInit()));
  return FileListPageSchema.parse(await res.json());
}

/** GET /files/{id} — full metadata for one file. */
export async function getFile(fileId: string): Promise<FileMetadata> {
  const res = await expectOk(await fetch(apiUrl(`/files/${encodeURIComponent(fileId)}`), authInit()));
  return FileMetadataSchema.parse(await res.json());
}

/** GET /files/recents — most recently accessed files (FileMetadataSlim rows). */
export async function listRecentFiles(limit = 50): Promise<FileMetadataSlim[]> {
  const res = await expectOk(await fetch(apiUrl(`/files/recents?limit=${limit}`), authInit()));
  const raw: unknown = await res.json();
  return (raw as unknown[]).map((entry) => FileMetadataSlimSchema.parse(entry));
}

/**
 * Builds the stream URL for a file. Note: the phase-1 backend serves
 * GET /api/v1/files/{id}/stream with cookie/JWT auth (handlers.go Stream),
 * NOT the spec §7.5 signed-URL scheme — there is no sig/exp to append.
 */
export function streamUrl(fileId: string): string {
  return apiUrl(`/files/${encodeURIComponent(fileId)}/stream`);
}

/**
 * Resolve a DOM-usable URL for `GET /files/{id}/stream` (DF-HERMES-CANOPY-14).
 *
 * The browser issues these loads itself — `<img src>`, `<video src>`,
 * `<a href download>` and the sandboxed viewer iframe — so it cannot attach an
 * `Authorization` header. In a build whose only token is `VITE_API_TOKEN` /
 * `localStorage['canopy.token']` the bare URL therefore 401s with
 * TOKEN_MISSING (internal/handler/auth.go reads the bearer from the header
 * only). This resolver moves the bytes through the authenticated fetch path
 * and returns a `blob:` object URL instead, which the viewer sandbox CSP
 * already admits (`img-src … blob:`, `media-src 'self' blob:`,
 * `worker-src 'self' blob:` in ViewerHost).
 *
 * No token (the `vite dev` proxy injects the JWT) → the bare URL is returned
 * unchanged, so the dev-proxy path keeps its pre-fix behaviour.
 *
 * The whole body is buffered in memory — fine for preview-sized files; a
 * Range-addressed object URL is out of scope for this row. Callers must hand
 * the result to `releaseStreamUrl` on file change / unmount.
 *
 * Range note: this reads the file with the OPEN-ENDED form
 * (`fetchFileRange(fileId, 0)` → `Range: bytes=0-`). The suffix form with no
 * length (`bytes=-0`) is rejected as malformed by the backend
 * (internal/fileviewer/streaming.go: `n <= 0` → ErrInvalidRangeHeader → 400
 * INVALID_RANGE_HEADER).
 */
export async function resolveStreamUrl(fileId: string): Promise<string> {
  if (resolveApiToken() === null) return streamUrl(fileId);
  const { blob } = await fetchFileRange(fileId, 0);
  return URL.createObjectURL(blob);
}

/**
 * Release a URL produced by `resolveStreamUrl`. Only `blob:` object URLs are
 * revoked — a plain URL (the no-token dev-proxy path, where nothing was
 * created) is left untouched.
 */
export function releaseStreamUrl(url: string): void {
  if (url.startsWith('blob:')) URL.revokeObjectURL(url);
}

/**
 * GET /files/{id}/stream with a Range header — returns the 206 (or 200)
 * body as a Blob plus the resolved bounds. `start`/`end` are inclusive
 * byte offsets; `start === null` issues the suffix form "bytes=-N".
 * Throws FileApiError with code RANGE_NOT_SATISFIABLE on 416.
 */
export async function fetchFileRange(
  fileId: string,
  start: number | null,
  end?: number,
): Promise<{ blob: Blob; status: number; contentRange: string | null }> {
  const range =
    start === null
      ? `bytes=-${end ?? 0}`
      : `bytes=${start}-${end !== undefined ? end : ''}`;
  const res = await expectOk(
    await fetch(streamUrl(fileId), authInit({ headers: { Range: range } })),
  );
  const contentRange = res.headers.get('Content-Range');
  return { blob: await res.blob(), status: res.status, contentRange };
}

/** GET /files/{id}/access — recent access-log entries for a file. */
export async function getFileAccessLog(fileId: string, limit = 50): Promise<FileAccessEntry[]> {
  const res = await expectOk(
    await fetch(apiUrl(`/files/${encodeURIComponent(fileId)}/access?limit=${limit}`), authInit()),
  );
  const raw: unknown = await res.json();
  return (raw as unknown[]).map((entry) => FileAccessEntrySchema.parse(entry));
}

/**
 * POST /files/{id}/access — append one audit entry. Action must be one of
 * open|download|thumbnail_fetch|preview_text|stream_start|stream_end|error
 * (validated server-side, handlers.go PostAccessLog).
 */
export async function postFileAccess(params: {
  fileId: string;
  action: FileAccessEntry['action'];
  viewerSlug: string;
  treeId?: string;
  nodeId?: string;
  durationMs?: number;
  byteOffset?: number;
  errorCode?: string;
}): Promise<FileAccessEntry> {
  const res = await expectOk(
    await fetch(apiUrl(`/files/${encodeURIComponent(params.fileId)}/access`), authInit({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        action: params.action,
        viewer_slug: params.viewerSlug,
        tree_id: params.treeId,
        node_id: params.nodeId,
        duration_ms: params.durationMs,
        byte_offset: params.byteOffset,
        error_code: params.errorCode,
      }),
    })),
  );
  return FileAccessEntrySchema.parse(await res.json());
}

/**
 * POST /files/resolve — resolve by hash reference. The wire format is
 * snake_case (Go models.go ResolveFileOutput); normalized to camelCase.
 */
export async function resolveByHash(profileId: string, sha256: string): Promise<ResolveFileOutput> {
  const res = await expectOk(
    await fetch(apiUrl('/files/resolve'), authInit({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ hash_ref: { profile_id: profileId, sha256 } }),
    })),
  );
  return ResolveFileOutputSchema.parse(await res.json());
}

// ── /viewers routes ─────────────────────────────────────────

/** GET /viewers — all active viewer registrations. */
export async function listViewers(): Promise<ViewerRegistration[]> {
  const res = await expectOk(await fetch(apiUrl('/viewers'), authInit()));
  return parseViewersResponse(await res.json());
}

/** GET /viewers/{slug} — one viewer registration. */
export async function getViewer(slug: string): Promise<ViewerRegistration> {
  const res = await expectOk(await fetch(apiUrl(`/viewers/${encodeURIComponent(slug)}`), authInit()));
  return ViewerRegistrationSchema.parse(await res.json());
}

/**
 * POST /viewers/dispatch — server-side viewer resolution for a file
 * (preview without opening). Request body: { file_id, tree_id? }.
 */
export async function dispatchViewer(fileId: string, treeId?: string): Promise<ViewerDispatchResult> {
  const res = await expectOk(
    await fetch(apiUrl('/viewers/dispatch'), authInit({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ file_id: fileId, tree_id: treeId }),
    })),
  );
  return ViewerDispatchResultSchema.parse(await res.json());
}
