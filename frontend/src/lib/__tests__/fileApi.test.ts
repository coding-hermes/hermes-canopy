/**
 * Unit tests — fileApi lib (SPEC-PL-02 phase 2)
 *
 * Mocks fetch (no live server): envelope parsing against the REAL backend
 * shapes (camelCase rows, snake_case resolve output, {error:{code,message}}
 * envelopes), §10 error-code mapping, and Range-header construction.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  FileApiError,
  dispatchViewer,
  fetchFileRange,
  getFile,
  getFileAccessLog,
  listViewers,
  listFiles,
  listRecentFiles,
  mapFileApiError,
  postFileAccess,
  releaseStreamUrl,
  resolveByHash,
  resolveStreamUrl,
  streamUrl,
} from '../fileApi';
import { parseViewersResponse } from '../../types/fileviewer';
import { TOKEN_STORAGE_KEY } from '../api';

// ── Fixtures (shapes as served by internal/fileviewer) ─────

const PROFILE_ID = '0198a7b6-c5d4-7321-8abc-def012345678';
const FILE_ID = '0198a7b6-c5d4-7321-8abc-def012345679';
const SHA = 'a3f5b8c9d1e2f4a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0';

function slimRow(overrides: Record<string, unknown> = {}) {
  return {
    id: FILE_ID,
    profileId: PROFILE_ID,
    sha256: SHA,
    byteSize: 524288,
    mimeType: 'application/pdf',
    filename: 'report.pdf',
    extension: 'pdf',
    storageKind: 'hermes_kb',
    isText: false,
    isViewable: true,
    viewerHint: 'pdf',
    referenceCount: 1,
    createdAt: '2026-09-01T12:00:00Z',
    ...overrides,
  };
}

function fullRow(overrides: Record<string, unknown> = {}) {
  return {
    ...slimRow(),
    declaredMime: 'application/pdf',
    sourceKind: 'upload',
    isBinary: true,
    metadata: { pageCount: 12 }, // non-nil map from Go
    accessCount: 3,
    quarantined: false,
    updatedAt: '2026-09-02T08:30:00Z',
    ...overrides,
  };
}

function viewerRow(overrides: Record<string, unknown> = {}) {
  return {
    id: '0198a7b6-c5d4-7321-8abc-def01234567a',
    viewerSlug: 'pdf',
    version: '0.4.2',
    canopydVersion: '0.4.2',
    displayName: 'PDF',
    description: 'Render PDF documents with pdf.js',
    iconUrl: '/static/viewers/pdf/icon.svg',
    renderType: 'fullscreen',
    supportsMime: ['application/pdf'],
    supportsExtensions: ['pdf'],
    supportsViewerHint: null, // nil Go slice on the wire
    requiredCapabilities: null,
    bundlePath: '',
    bundleByteSize: 2097152,
    bundleSha256: '', // phase-1 seeds carry ''
    minCanopydVersion: '0.4.0',
    isActive: true,
    installedAt: '2026-09-01T12:00:00+00:00', // numeric offset form
    ...overrides,
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorResponse(status: number, code: string, message: string): Response {
  return jsonResponse({ error: { code, message } }, status);
}

describe('fileApi — listFiles', () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('GETs /api/v1/files with query params and parses the envelope', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ files: [slimRow()], pagination: { count: 1 } }),
    );
    const page = await listFiles({ limit: 25, sort: 'name_asc', mimeFilter: 'image/', viewableOnly: false });
    const [url] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/files?limit=25&sort=name_asc&mimeFilter=image%2F&viewableOnly=false');
    expect(page.files).toHaveLength(1);
    expect(page.files[0].filename).toBe('report.pdf');
    expect(page.pagination.count).toBe(1);
  });

  it('omits the query string when no params are given', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ files: [], pagination: { count: 0 } }));
    const page = await listFiles();
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/files');
    expect(page.files).toEqual([]);
  });

  it('maps FILE_QUARANTINED envelope to FileApiError with the catalog code', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(423, 'FILE_QUARANTINED', 'file is quarantined and cannot be streamed'));
    const err = await listFiles().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(FileApiError);
    expect((err as FileApiError).code).toBe('FILE_QUARANTINED');
    expect((err as FileApiError).status).toBe(423);
  });
});

describe('fileApi — getFile', () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('GETs /api/v1/files/{id} and validates a realistic full metadata row', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(fullRow()));
    const file = await getFile(FILE_ID);
    expect(fetchMock.mock.calls[0][0]).toBe(`/api/v1/files/${FILE_ID}`);
    expect(file.metadata).toEqual({ pageCount: 12 });
    expect(file.byteSize).toBe(524288);
  });

  it('rejects a row with a malformed sha256', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(fullRow({ sha256: 'nothex' })));
    await expect(getFile(FILE_ID)).rejects.toThrow();
  });
});

describe('fileApi — listRecentFiles', () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('GETs /api/v1/files/recents and parses slim rows', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([slimRow({ lastAccessedAt: '2026-09-02T10:00:00Z' })]));
    const rows = await listRecentFiles(10);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/files/recents?limit=10');
    expect(rows).toHaveLength(1);
    expect(rows[0].lastAccessedAt).toBe('2026-09-02T10:00:00Z');
  });
});

describe('fileApi — streaming', () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('streamUrl builds the /stream route the backend mounts', () => {
    expect(streamUrl(FILE_ID)).toBe(`/api/v1/files/${FILE_ID}/stream`);
  });

  it('fetchFileRange sends an inclusive Range header and returns 206 metadata', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response('partial-bytes', {
        status: 206,
        headers: { 'Content-Range': `bytes 100-199/524288`, 'Content-Type': 'application/pdf' },
      }),
    );
    const { blob, status, contentRange } = await fetchFileRange(FILE_ID, 100, 199);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(`/api/v1/files/${FILE_ID}/stream`);
    expect((init.headers as Record<string, string>).Range).toBe('bytes=100-199');
    expect(status).toBe(206);
    expect(contentRange).toBe('bytes 100-199/524288');
    expect(blob.size).toBe(13);
  });

  it('fetchFileRange supports open-ended and suffix ranges', async () => {
    fetchMock.mockImplementation(async () => new Response('x', { status: 206 }));
    await fetchFileRange(FILE_ID, 0);
    await fetchFileRange(FILE_ID, null, 512);
    const ranges = fetchMock.mock.calls.map((c) => (c[1].headers as Record<string, string>).Range);
    expect(ranges).toEqual(['bytes=0-', 'bytes=-512']);
  });

  it('maps a 416 to RANGE_NOT_SATISFIABLE', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(416, 'RANGE_NOT_SATISFIABLE', 'range request exceeds file size'));
    const err = await fetchFileRange(FILE_ID, 99999999).catch((e: unknown) => e);
    expect((err as FileApiError).code).toBe('RANGE_NOT_SATISFIABLE');
  });
});

describe('fileApi — access log', () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('GETs /files/{id}/access and parses entries (null clientInfo tolerated)', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse([
        {
          id: '0198a7b6-c5d4-7321-8abc-def01234567b',
          fileId: FILE_ID,
          profileId: PROFILE_ID,
          viewerSlug: 'pdf',
          action: 'open',
          clientInfo: null, // nil Go []byte on the wire
          createdAt: '2026-09-02T09:00:00Z',
        },
      ]),
    );
    const entries = await getFileAccessLog(FILE_ID);
    expect(fetchMock.mock.calls[0][0]).toBe(`/api/v1/files/${FILE_ID}/access?limit=50`);
    expect(entries[0].action).toBe('open');
    expect(entries[0].clientInfo).toEqual({});
  });

  it('POSTs snake_case access bodies and returns the created entry', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(
        {
          id: '0198a7b6-c5d4-7321-8abc-def01234567c',
          fileId: FILE_ID,
          profileId: PROFILE_ID,
          viewerSlug: 'pdf',
          action: 'open',
          clientInfo: {},
          createdAt: '2026-09-02T09:00:00Z',
        },
        201,
      ),
    );
    const entry = await postFileAccess({ fileId: FILE_ID, action: 'open', viewerSlug: 'pdf', treeId: PROFILE_ID });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(`/api/v1/files/${FILE_ID}/access`);
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({
      action: 'open',
      viewer_slug: 'pdf',
      tree_id: PROFILE_ID,
      node_id: undefined,
      duration_ms: undefined,
      byte_offset: undefined,
      error_code: undefined,
    });
    expect(entry.id).toBe('0198a7b6-c5d4-7321-8abc-def01234567c');
  });
});

describe('fileApi — resolve + viewers', () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('resolveByHash POSTs the snake_case hash_ref body and normalizes to camelCase', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        file: fullRow(),
        was_new_upload: false,
        was_deduped: true,
        stream_url: '/api/v1/files/x/stream',
        expires_at: '2026-09-02T09:15:00Z',
      }),
    );
    const out = await resolveByHash(PROFILE_ID, SHA);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/files/resolve');
    expect(JSON.parse(init.body)).toEqual({ hash_ref: { profile_id: PROFILE_ID, sha256: SHA } });
    expect(out.wasNewUpload).toBe(false);
    expect(out.wasDeduped).toBe(true);
    expect(out.streamUrl).toBe('/api/v1/files/x/stream');
    expect(out.file.metadata).toEqual({ pageCount: 12 });
  });

  it('listViewers GETs /viewers and parses the 7-viewer payload shape', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse([viewerRow(), viewerRow({ viewerSlug: 'image', supportsMime: ['image/png'] })]),
    );
    const viewers = await listViewers();
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/viewers');
    expect(viewers).toHaveLength(2);
    expect(viewers[0].supportsViewerHint).toEqual([]);
    expect(viewers[1].viewerSlug).toBe('image');
  });

  it('A1: parseViewersResponse accepts a realistic 7-viewer /viewers response', () => {
    const all = ['pdf', 'image', 'code', 'csv', 'markdown', 'json', 'audio_video'].map((slug, i) =>
      viewerRow({
        viewerSlug: slug,
        id: `0198a7b6-c5d4-7321-8abc-def01234567${i}`,
        supportsMime: slug === 'image' ? ['image/png', 'image/jpeg'] : [viewerRow().supportsMime[0]],
      }),
    );
    const parsed = parseViewersResponse(all);
    expect(parsed).toHaveLength(7);
    expect(parsed.map((v) => v.viewerSlug)).toContain('audio_video');
  });

  it('dispatchViewer POSTs file_id and parses ViewerDispatchResult', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        viewerSlug: 'pdf',
        renderType: 'fullscreen',
        bundlePath: '',
        bundleSha256: '',
        displayName: 'PDF',
        iconUrl: '/static/viewers/pdf/icon.svg',
        config: null, // nil Go map on the wire
        isBuiltIn: true,
      }),
    );
    const result = await dispatchViewer(FILE_ID);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/viewers/dispatch');
    expect(JSON.parse(init.body)).toEqual({ file_id: FILE_ID, tree_id: undefined });
    expect(result.config).toEqual({});
    expect(result.isBuiltIn).toBe(true);
  });

  it('maps VIEWER_NOT_FOUND (404) with the catalog code', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(404, 'VIEWER_NOT_FOUND', 'no active viewer matches this file'));
    await expect(dispatchViewer(FILE_ID)).rejects.toMatchObject({ code: 'VIEWER_NOT_FOUND', status: 404 });
  });
});

describe('fileApi — error envelope mapping', () => {
  it('mapFileApiError extracts code + message from the envelope', async () => {
    const err = await mapFileApiError(errorResponse(410, 'FILE_SOFT_DELETED', 'file has been soft-deleted'));
    expect(err.code).toBe('FILE_SOFT_DELETED');
    expect(err.status).toBe(410);
    expect(err.message).toBe('file has been soft-deleted');
  });

  it('mapFileApiError falls back to UNKNOWN_ERROR on non-JSON bodies', async () => {
    const res = new Response('<html>gateway timeout</html>', { status: 502 });
    const err = await mapFileApiError(res);
    expect(err.code).toBe('UNKNOWN_ERROR');
    expect(err.status).toBe(502);
  });

  it('mapFileApiError handles the legacy string envelope', async () => {
    const res = new Response(JSON.stringify({ error: 'plain message' }), { status: 500 });
    const err = await mapFileApiError(res);
    expect(err.code).toBe('UNKNOWN_ERROR');
    expect(err.message).toBe('plain message');
  });

  it('mapFileApiError keeps HTTP status text when the body is empty', async () => {
    const err = await mapFileApiError(new Response('', { status: 503 }));
    expect(err.code).toBe('UNKNOWN_ERROR');
    expect(err.message).toBe('HTTP 503');
  });
});

// ── DF-HERMES-CANOPY-14: DOM-usable stream URLs ───────────────────────────
//
// The three hand-off points (`<a href download>`, the sandbox doc's
// canopy.__bootstrap.streamUrl, `viewer.get_stream_url`) are loaded by the
// BROWSER, which cannot attach a bearer header — in a token-only build they
// 401 TOKEN_MISSING. resolveStreamUrl moves the bytes through the auth'd
// fetch path and returns a blob: object URL instead; with no token (the vite
// dev proxy injects the JWT) the bare URL is returned unchanged.

describe('fileApi — resolveStreamUrl / releaseStreamUrl (DF-HERMES-CANOPY-14)', () => {
  const fetchMock = vi.fn();
  const createObjectURL = vi.fn<(blob: Blob | MediaSource) => string>(() => 'blob:http://localhost/canopy-obj-1');
  const revokeObjectURL = vi.fn();
  const originalCreateObjectURL = URL.createObjectURL;
  const originalRevokeObjectURL = URL.revokeObjectURL;

  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue(new Response('pdf-bytes', { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    createObjectURL.mockClear();
    revokeObjectURL.mockClear();
    // jsdom implements neither createObjectURL nor revokeObjectURL.
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = revokeObjectURL;
    window.localStorage.clear();
  });

  afterEach(() => {
    URL.createObjectURL = originalCreateObjectURL;
    URL.revokeObjectURL = originalRevokeObjectURL;
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    window.localStorage.clear();
  });

  it('returns a blob: URL fetched through the auth’d path when VITE_API_TOKEN resolves', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');

    const url = await resolveStreamUrl(FILE_ID);

    expect(url).toBe('blob:http://localhost/canopy-obj-1');
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [requested, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(requested).toBe(`/api/v1/files/${FILE_ID}/stream`);
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer build-token-123');
    // Open-ended whole-file range: the suffix form with no length would be
    // "bytes=-0", which the backend rejects as malformed (400).
    expect(headers.get('Range')).toBe('bytes=0-');
    // The DOM gets the Blob, not the bytes.
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    const [blob] = createObjectURL.mock.calls[0] as [Blob];
    // Node's (undici) Blob and jsdom's Blob are distinct constructors, so the
    // shape is asserted rather than the identity.
    expect(blob.size).toBe('pdf-bytes'.length);
    expect(await blob.text()).toBe('pdf-bytes');
  });

  it('returns a blob: URL for a pasted localStorage token', async () => {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, 'stored-token-456');

    await expect(resolveStreamUrl(FILE_ID)).resolves.toBe('blob:http://localhost/canopy-obj-1');

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer stored-token-456');
  });

  it('returns exactly streamUrl(id) — and fetches nothing — when no token resolves', async () => {
    vi.stubEnv('VITE_API_TOKEN', '');

    const url = await resolveStreamUrl(FILE_ID);

    expect(url).toBe(streamUrl(FILE_ID));
    expect(url).toBe(`/api/v1/files/${FILE_ID}/stream`);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it('propagates a failed fetch instead of handing the DOM a bare URL', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ error: { code: 'TOKEN_MISSING', message: 'missing bearer token' } }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      }),
    );

    await expect(resolveStreamUrl(FILE_ID)).rejects.toMatchObject({ code: 'TOKEN_MISSING', status: 401 });
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it('releaseStreamUrl revokes blob: URLs only', () => {
    releaseStreamUrl('blob:http://localhost/canopy-obj-1');
    expect(revokeObjectURL).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:http://localhost/canopy-obj-1');

    // A plain URL is not an object URL — nothing to release.
    releaseStreamUrl('/api/v1/files/file-1/stream');
    releaseStreamUrl('https://canopy.test/api/v1/files/file-1/stream');
    expect(revokeObjectURL).toHaveBeenCalledTimes(1);
  });
});
