/**
 * Unit tests — viewerRegistry lib (SPEC-PL-02 §6, phase 2)
 *
 * Mocked fetch only. Pins the dispatch precedence: explicit hint >
 * server hint > mime > extension; quarantined / not-viewable / no-match
 * all return null (caller renders download-only).
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  buildViewerTables,
  fetchViewers,
  globMatch,
  resetViewerRegistryCache,
  selectViewer,
  type ViewerSelection,
} from '../viewerRegistry';
import type { FileMetadata, ViewerRegistration } from '../../types/fileviewer';

// ── Fixtures ────────────────────────────────────────────────

const FILE_ID = '0198a7b6-c5d4-7321-8abc-def012345679';
const PDF_VIEWER_ID = '0198a7b6-c5d4-7321-8abc-def012345680';
const IMG_VIEWER_ID = '0198a7b6-c5d4-7321-8abc-def012345681';
const CODE_VIEWER_ID = '0198a7b6-c5d4-7321-8abc-def012345682';

export function makeViewer(overrides: Partial<ViewerRegistration> = {}): ViewerRegistration {
  return {
    id: PDF_VIEWER_ID,
    viewerSlug: 'pdf',
    version: '0.4.2',
    canopydVersion: '0.4.2',
    displayName: 'PDF',
    description: 'Render PDF documents with pdf.js',
    iconUrl: '/static/viewers/pdf/icon.svg',
    renderType: 'fullscreen',
    supportsMime: ['application/pdf'],
    supportsExtensions: ['pdf'],
    supportsViewerHint: [],
    requiredCapabilities: [],
    bundlePath: '',
    bundleByteSize: 0,
    bundleSha256: '',
    minCanopydVersion: '0.4.0',
    isActive: true,
    installedAt: '2026-09-01T12:00:00+00:00',
    ...overrides,
  };
}

export function makeFile(overrides: Partial<FileMetadata> = {}): FileMetadata {
  return {
    id: FILE_ID,
    profileId: '0198a7b6-c5d4-7321-8abc-def012345678',
    sha256: 'a3f5b8c9d1e2f4a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0',
    byteSize: 524288,
    mimeType: 'application/pdf',
    declaredMime: 'application/pdf',
    filename: 'report.pdf',
    extension: 'pdf',
    storageKind: 'hermes_kb',
    sourceKind: 'upload',
    isText: false,
    isBinary: true,
    isViewable: true,
    viewerHint: '',
    metadata: {},
    referenceCount: 1,
    accessCount: 0,
    quarantined: false,
    createdAt: '2026-09-01T12:00:00Z',
    updatedAt: '2026-09-01T12:00:00Z',
    ...overrides,
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('viewerRegistry — globMatch', () => {
  it('matches wildcard and exact patterns, case-insensitively', () => {
    expect(globMatch('text/*', 'text/plain')).toBe(true);
    expect(globMatch('image/*', 'IMAGE/SVG+xml')).toBe(true);
    expect(globMatch('application/pdf', 'application/pdf')).toBe(true);
    expect(globMatch('application/pdf', 'application/json')).toBe(false);
    expect(globMatch('text/?tml', 'text/html')).toBe(true);
  });
});

describe('viewerRegistry — fetchViewers caching', () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
    resetViewerRegistryCache();
  });
  afterEach(() => vi.unstubAllGlobals());

  it('fetches /api/v1/viewers once and serves repeat calls from cache', async () => {
    fetchMock.mockResolvedValue(jsonResponse([makeViewer()]));
    const first = await fetchViewers();
    const second = await fetchViewers();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/viewers');
    expect(second).toBe(first);
  });

  it('retries after a failed fetch instead of caching the failure', async () => {
    fetchMock.mockRejectedValueOnce(new Error('boom'));
    await expect(fetchViewers()).rejects.toThrow('boom');
    fetchMock.mockResolvedValue(jsonResponse([makeViewer()]));
    const viewers = await fetchViewers();
    expect(viewers).toHaveLength(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});

describe('viewerRegistry — selectViewer (A3)', () => {
  const pdfViewer = makeViewer();
  const imageViewer = makeViewer({
    id: IMG_VIEWER_ID,
    viewerSlug: 'image',
    displayName: 'Image',
    supportsMime: ['image/png', 'image/jpeg'],
    supportsExtensions: ['png', 'jpg'],
  });
  const codeViewer = makeViewer({
    id: CODE_VIEWER_ID,
    viewerSlug: 'code',
    displayName: 'Code Editor',
    supportsMime: ['text/x-python', 'text/x-go'],
    supportsExtensions: ['py', 'go', 'txt'],
  });
  const viewers = [pdfViewer, imageViewer, codeViewer];

  beforeEach(() => resetViewerRegistryCache());

  it('explicit hint beats mime match (hint → code viewer for a .txt file)', async () => {
    const file = makeFile({ mimeType: 'text/plain', extension: 'txt', viewerHint: '' });
    const sel = (await selectViewer(file, { hint: 'code', viewers })) as ViewerSelection;
    expect(sel.via).toBe('hint');
    expect(sel.viewer.viewerSlug).toBe('code');
  });

  it('hint is ignored when no registered viewer claims the slug (falls to mime)', async () => {
    const file = makeFile({ mimeType: 'application/pdf', extension: 'pdf' });
    const sel = (await selectViewer(file, { hint: 'nonexistent-viewer', viewers })) as ViewerSelection;
    expect(sel.via).toBe('mime');
    expect(sel.viewer.viewerSlug).toBe('pdf');
  });

  it('server viewerHint routes when the descriptor advertises it', async () => {
    const hintAware = makeViewer({ supportsViewerHint: ['marky'] , viewerSlug: 'markdown', displayName: 'Markdown' });
    const file = makeFile({
      mimeType: 'application/octet-stream',
      extension: '',
      viewerHint: 'marky',
      isText: true,
    });
    const sel = (await selectViewer(file, { viewers: [hintAware] })) as ViewerSelection;
    expect(sel.via).toBe('hint');
    expect(sel.viewer.viewerSlug).toBe('markdown');
  });

  it('matches by mime when no hint exists', async () => {
    const file = makeFile({ mimeType: 'image/png', extension: 'png', viewerHint: '' });
    const sel = (await selectViewer(file, { viewers })) as ViewerSelection;
    expect(sel.via).toBe('mime');
    expect(sel.viewer.viewerSlug).toBe('image');
  });

  it('unknown mime + no hint falls to extension match', async () => {
    const file = makeFile({ mimeType: 'application/octet-stream', extension: 'PY', viewerHint: '' });
    const sel = (await selectViewer(file, { viewers })) as ViewerSelection;
    expect(sel.via).toBe('extension');
    expect(sel.viewer.viewerSlug).toBe('code');
  });

  it('unknown mime + no hint + unknown extension => null (A3)', async () => {
    const file = makeFile({ mimeType: 'application/x-unknown', extension: 'docx', viewerHint: '' });
    expect(await selectViewer(file, { viewers })).toBeNull();
  });

  it('quarantined file => null even when a viewer matches (A3)', async () => {
    const file = makeFile({ quarantined: true });
    expect(await selectViewer(file, { viewers })).toBeNull();
    // even with an explicit hint
    expect(await selectViewer(file, { hint: 'pdf', viewers })).toBeNull();
  });

  it('isViewable=false => null (download-only classification)', async () => {
    const file = makeFile({ isViewable: false });
    expect(await selectViewer(file, { viewers })).toBeNull();
  });

  it('inactive viewers are skipped', async () => {
    const file = makeFile();
    expect(await selectViewer(file, { viewers: [makeViewer({ isActive: false })] })).toBeNull();
  });

  it('wildcard mime patterns on a descriptor match concrete mimes', async () => {
    const textViewer = makeViewer({ viewerSlug: 'code', supportsMime: ['text/*'] });
    const file = makeFile({ mimeType: 'text/plain', extension: '' });
    const sel = (await selectViewer(file, { viewers: [textViewer] })) as ViewerSelection;
    expect(sel.via).toBe('mime');
    expect(sel.viewer.viewerSlug).toBe('code');
  });

  it('fetches /viewers lazily when no viewers are supplied', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse([pdfViewer]));
    vi.stubGlobal('fetch', fetchMock);
    try {
      const file = makeFile();
      const sel = (await selectViewer(file)) as ViewerSelection;
      expect(fetchMock).toHaveBeenCalledWith('/api/v1/viewers');
      expect(sel.viewer.viewerSlug).toBe('pdf');
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe('viewerRegistry — buildViewerTables', () => {
  it('indexes by slug, mime, extension (lowercased), and hint', () => {
    const { bySlug, mimeToViewer, extensionToViewer, hintToViewer } = buildViewerTables([
      makeViewer({ supportsViewerHint: ['pdf'] }),
      makeViewer({ id: IMG_VIEWER_ID, viewerSlug: 'image', supportsExtensions: ['PNG'] }),
    ]);
    expect(bySlug.get('pdf')?.viewerSlug).toBe('pdf');
    expect(mimeToViewer.get('application/pdf')?.viewerSlug).toBe('pdf');
    expect(extensionToViewer.get('png')?.viewerSlug).toBe('image');
    expect(hintToViewer.get('pdf')?.viewerSlug).toBe('pdf');
  });
});
