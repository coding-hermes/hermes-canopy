/**
 * Component tests — ViewerHost (SPEC-PL-02 §8, phase 2)
 *
 * Driven with React 19 `act` + react-dom/client (no @testing-library in
 * this repo — same harness as ContextManifestPanel.test.tsx). Pins:
 *   - the srcDoc carries the §8.2 CSP and REAL substituted nonce/file-id
 *   - sandbox + referrerpolicy attributes match the spec
 *   - hostile messages with wrong origin / wrong nonce / wrong target are
 *     ignored (no API response, no state change)
 *   - selectViewer null => "No viewer available for this file" empty state
 *   - a well-formed viewer_api_call is answered through the mocked frame
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ViewerHost, { VIEWER_SANDBOX_CSP, buildViewerDoc, viewerIframeName, type ViewerHostProps } from '../ViewerHost.tsx';
import type { FileMetadata, ViewerRegistration } from '../../types/fileviewer';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// ─── Fixtures ───────────────────────────────────────────────────────────

const FILE_ID = '0198a7b6-c5d4-7321-8abc-def012345679';
const VIEWER_ID = '0198a7b6-c5d4-7321-8abc-def012345680';
const PROFILE_ID = '0198a7b6-c5d4-7321-8abc-def012345678';

function makeFile(overrides: Partial<FileMetadata> = {}): FileMetadata {
  return {
    id: FILE_ID,
    profileId: PROFILE_ID,
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
    viewerHint: 'pdf',
    metadata: {},
    referenceCount: 1,
    accessCount: 0,
    quarantined: false,
    createdAt: '2026-09-01T12:00:00Z',
    updatedAt: '2026-09-01T12:00:00Z',
    ...overrides,
  };
}

function makeViewer(overrides: Partial<ViewerRegistration> = {}): ViewerRegistration {
  return {
    id: VIEWER_ID,
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

// ─── Harness ────────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;

function mountViewer(props: {
  file: FileMetadata;
  viewer: ViewerRegistration | null;
  fetchRange?: ViewerHostProps['fetchRange'];
  postAccess?: ViewerHostProps['postAccess'];
}) {
  act(() => {
    root.render(createElement(ViewerHost, props));
  });
}

async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  fetchMock = vi.fn(() => Promise.resolve(new Response('{}', { status: 200 })));
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  window.localStorage.clear();
});

function q(selector: string): Element | null {
  return container.querySelector(selector);
}

/** One captured postMessage call from the stubbed frame window. */
interface CapturedPost {
  m: unknown;
  o: string;
}

/** Installs a recording stub for iframe.contentWindow; returns the log. */
function interceptFrame(iframe: HTMLIFrameElement): CapturedPost[] {
  const responses: CapturedPost[] = [];
  Object.defineProperty(iframe, 'contentWindow', {
    configurable: true,
    value: { postMessage: (m: unknown, o: string) => responses.push({ m, o }) },
  });
  return responses;
}

// ─── srcDoc / iframe contract (A4) ──────────────────────────────────────

describe('ViewerHost — sandbox iframe (A4)', () => {
  it('renders an iframe with the §8.2 sandbox + referrerpolicy and CSP-carrying srcDoc', () => {
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    const iframe = q('iframe');
    expect(iframe).not.toBeNull();
    expect(iframe!.getAttribute('sandbox')).toBe('allow-scripts allow-same-origin');
    expect(iframe!.getAttribute('referrerpolicy')).toBe('no-referrer');
    const srcDoc = iframe!.getAttribute('srcdoc') ?? '';
    expect(srcDoc).toContain(VIEWER_SANDBOX_CSP);
    expect(srcDoc).toContain('Content-Security-Policy');
    // No §8.2 placeholder survives substitution.
    expect(srcDoc).not.toContain('__NONCE__');
    expect(srcDoc).not.toContain('__FILE_ID__');
    expect(srcDoc).not.toContain('__FILE_META_JSON__');
  });

  it('substitutes a REAL per-mount nonce and the file id into the shim', () => {
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    const srcDoc = q('iframe')!.getAttribute('srcdoc') ?? '';
    // NONCE = "<uuid>" — a v4 UUID literal is inlined, and it differs per mount.
    const nonceMatch = srcDoc.match(/var NONCE = "([^"]+)"/);
    expect(nonceMatch).not.toBeNull();
    expect(nonceMatch![1]).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    const second = document.createElement('div');
    document.body.appendChild(second);
    let secondDoc = '';
    act(() => {
      const secondRoot = createRoot(second);
      secondRoot.render(createElement(ViewerHost, { file: makeFile(), viewer: makeViewer() }));
      secondDoc = second.querySelector('iframe')?.getAttribute('srcdoc') ?? '';
      secondRoot.unmount();
    });
    second.remove();
    const secondNonce = secondDoc.match(/var NONCE = "([^"]+)"/)?.[1] ?? '';
    expect(secondNonce).not.toBe(nonceMatch![1]);
    expect(srcDoc).toContain(`var FILE_ID = "${FILE_ID}"`);
  });

  it('inlines the file metadata JSON and stream URL', () => {
    const file = makeFile({ metadata: { pageCount: 12 } });
    mountViewer({ file, viewer: makeViewer() });
    const srcDoc = q('iframe')!.getAttribute('srcdoc') ?? '';
    expect(srcDoc).toContain('"pageCount":12');
    expect(srcDoc).toContain(`/api/v1/files/${FILE_ID}/stream`);
  });

  it('iframe name follows canopy-viewer-{slug}-{fileId} (§8.2)', () => {
    expect(viewerIframeName('pdf', FILE_ID)).toBe(`canopy-viewer-pdf-${FILE_ID}`);
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    expect(q('iframe')!.getAttribute('name')).toBe(`canopy-viewer-pdf-${FILE_ID}`);
  });

  it('buildViewerDoc escapes hostile metadata (no raw <script> breakouts)', () => {
    const hostile = makeFile({ filename: 'ok.pdf', metadata: { x: '</script><script>alert(1)</script>' } });
    const doc = buildViewerDoc({
      file: hostile,
      viewer: makeViewer(),
      nonce: 'test-nonce',
      parentOrigin: 'https://canopy.test',
      streamUrl: '/stream',
      config: {},
    });
    expect(doc).toContain('\\u003c/script>');
    expect(doc).not.toContain('</script><script>alert(1)</script>');
  });
});

// ─── Empty state ────────────────────────────────────────────────────────

describe('ViewerHost — empty state', () => {
  it('renders "No viewer available for this file" when viewer is null', () => {
    mountViewer({ file: makeFile(), viewer: null });
    expect(q('[data-empty-state]')).not.toBeNull();
    expect(container.textContent).toContain('No viewer available for this file');
    expect(q('iframe')).toBeNull();
    // Download-only fallback: raw bytes remain reachable.
    const link = q('[data-empty-state] a');
    expect(link?.getAttribute('href')).toBe(`/api/v1/files/${FILE_ID}/stream`);
    expect(link?.getAttribute('download')).toBe('report.pdf');
  });
});

// ─── Message handler ────────────────────────────────────────────────────

describe('ViewerHost — message validation', () => {
  const ORIGIN = window.location.origin;

  function dispatchMessage(init: { origin?: string; data?: unknown }): void {
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', { origin: init.origin ?? ORIGIN, data: init.data }),
      );
    });
  }

  function validCall(overrides: Record<string, unknown> = {}): Record<string, unknown> {
    const srcDoc = q('iframe')?.getAttribute('srcdoc') ?? '';
    const nonce = srcDoc.match(/var NONCE = "([^"]+)"/)?.[1] ?? '';
    return { type: 'viewer_api_call', id: 'viewer-call-1', target: 'host', nonce, payload: { method: 'viewer.ready', params: {} }, timestamp: Date.now(), ...overrides };
  }

  it('answers a well-formed viewer_api_call on the happy path', async () => {
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({ data: validCall() });
    await settle();

    expect(responses).toHaveLength(1);
    const msg = responses[0].m as Record<string, unknown>;
    expect(msg.type).toBe('viewer_api_response');
    expect(msg.id).toBe('viewer-call-1');
    expect(msg.target).toBe(`viewer:pdf:${FILE_ID}`);
    expect(msg.nonce).toBe((q('iframe')!.getAttribute('srcdoc') ?? '').match(/var NONCE = "([^"]+)"/)![1]);
    expect((msg.result as Record<string, unknown>).fileId).toBe(FILE_ID);
  });

  it('ignores messages from a WRONG ORIGIN (no response, no state change)', async () => {
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({ origin: 'https://evil.example', data: validCall() });
    await settle();

    expect(responses).toHaveLength(0);
    expect(q('[data-viewer-header]')).not.toBeNull(); // still mounted normally
  });

  it('ignores messages with a WRONG NONCE (A4)', async () => {
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({ data: validCall({ nonce: 'attacker-guessed-nonce' }) });
    await settle();

    expect(responses).toHaveLength(0);
  });

  it('ignores messages not addressed to the host and unknown message types', async () => {
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({ data: validCall({ target: 'somewhere-else' }) });
    dispatchMessage({ data: validCall({ type: 'viewer_event', payload: { event: 'theme' } }) });
    dispatchMessage({ data: { junk: true } });
    await settle();

    expect(responses).toHaveLength(0);
  });

  it('viewer_ready flips readiness and is answered', async () => {
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({ data: validCall({ type: 'viewer_ready', id: 'ready-1' }) });
    await settle();

    expect(responses).toHaveLength(1);
    expect((responses[0].m as Record<string, unknown>).type).toBe('viewer_api_response');
    expect(q('iframe')).not.toBeNull(); // stays mounted
  });

  it('serves viewer.get_file_metadata with the full file object', async () => {
    const file = makeFile({ metadata: { pageCount: 7 } });
    mountViewer({ file, viewer: makeViewer() });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({ data: validCall({ payload: { method: 'viewer.get_file_metadata', params: {} } }) });
    await settle();

    const msg = responses[0].m as Record<string, unknown>;
    expect((msg.result as Record<string, unknown>).metadata).toEqual({ pageCount: 7 });
  });

  it('serves viewer.log_access through the API seam with host-authoritative identity and mapped §3.3 metadata', async () => {
    const postAccess = vi.fn().mockResolvedValue({ accepted: true });
    mountViewer({ file: makeFile(), viewer: makeViewer(), postAccess });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({
      data: validCall({
        id: 'access-call-1',
        payload: {
          method: 'viewer.log_access',
          params: {
            action: 'stream_end',
            fileId: 'spoofed-top-level-file',
            viewerSlug: 'spoofed_top_level_viewer',
            metadata: {
              fileId: 'spoofed-metadata-file',
              viewerSlug: 'spoofed_metadata_viewer',
              treeId: PROFILE_ID,
              nodeId: VIEWER_ID,
              durationMs: 1234,
              byteOffset: 4096,
              errorCode: 'MEDIA_ERR_DECODE',
              ignoredField: 'not forwarded',
            },
          },
        },
      }),
    });
    await settle();

    expect(postAccess).toHaveBeenCalledTimes(1);
    expect(postAccess).toHaveBeenCalledWith({
      fileId: FILE_ID,
      action: 'stream_end',
      viewerSlug: 'pdf',
      treeId: PROFILE_ID,
      nodeId: VIEWER_ID,
      durationMs: 1234,
      byteOffset: 4096,
      errorCode: 'MEDIA_ERR_DECODE',
    });
    expect(responses).toHaveLength(1);
    expect(responses[0].m).toMatchObject({
      type: 'viewer_api_response',
      id: 'access-call-1',
      target: `viewer:pdf:${FILE_ID}`,
      result: { accepted: true },
    });
  });

  it('uses postFileAccess by default for a validated viewer.log_access call', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          id: VIEWER_ID,
          fileId: FILE_ID,
          profileId: PROFILE_ID,
          viewerSlug: 'pdf',
          action: 'open',
          clientInfo: {},
          createdAt: '2026-09-01T12:00:00Z',
        }),
        { status: 201, headers: { 'Content-Type': 'application/json' } },
      ),
    );
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({
      data: validCall({
        id: 'default-access-call',
        payload: { method: 'viewer.log_access', params: { action: 'open', metadata: {} } },
      }),
    });
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`/api/v1/files/${FILE_ID}/access`);
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toMatchObject({
      action: 'open',
      viewer_slug: 'pdf',
    });
    expect(responses).toHaveLength(1);
    expect(responses[0].m).toMatchObject({
      id: 'default-access-call',
      result: { fileId: FILE_ID, viewerSlug: 'pdf', action: 'open' },
    });
  });

  it('rejects unsupported viewer.log_access actions without calling the API', async () => {
    const postAccess = vi.fn().mockResolvedValue({ accepted: true });
    mountViewer({ file: makeFile(), viewer: makeViewer(), postAccess });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({
      data: validCall({
        id: 'invalid-action',
        payload: { method: 'viewer.log_access', params: { action: 'delete', metadata: {} } },
      }),
    });
    await settle();

    expect(postAccess).not.toHaveBeenCalled();
    expect(responses).toHaveLength(1);
    expect((responses[0].m as Record<string, unknown>).error).toMatchObject({
      code: 'VIEWER_INVALID_ACCESS_ACTION',
    });
  });

  it.each([
    ['negative durationMs', { durationMs: -1 }],
    ['fractional byteOffset', { byteOffset: 1.5 }],
  ])('rejects malformed numeric access metadata: %s', async (_name, metadata) => {
    const postAccess = vi.fn().mockResolvedValue({ accepted: true });
    mountViewer({ file: makeFile(), viewer: makeViewer(), postAccess });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({
      data: validCall({
        id: 'invalid-number',
        payload: { method: 'viewer.log_access', params: { action: 'open', metadata } },
      }),
    });
    await settle();

    expect(postAccess).not.toHaveBeenCalled();
    expect(responses).toHaveLength(1);
    expect((responses[0].m as Record<string, unknown>).error).toMatchObject({
      code: 'VIEWER_INVALID_ACCESS_METADATA',
    });
  });

  it('refuses unserved methods with an error response', async () => {
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    const responses = interceptFrame(q('iframe') as HTMLIFrameElement);

    dispatchMessage({ data: validCall({ payload: { method: 'viewer.open_externally', params: {} } }) });
    await settle();

    const msg = responses[0].m as Record<string, unknown>;
    expect(msg.error).toMatchObject({ code: 'VIEWER_NOT_IMPLEMENTED' });
  });

  it('answers NOTHING when a hostile message arrives while viewer is null', async () => {
    mountViewer({ file: makeFile(), viewer: null });
    // No iframe exists; a leaked valid message must not throw.
    const srcDoc = '';
    expect(srcDoc).toBe('');
    expect(() =>
      dispatchMessage({
        data: { type: 'viewer_api_call', id: 'x', target: 'host', nonce: 'anything', payload: {}, timestamp: 0 },
      }),
    ).not.toThrow();
    await settle();
    expect(q('[data-empty-state]')).not.toBeNull();
  });
});

// ─── DF-HERMES-CANOPY-14: no bare stream URL reaches the DOM ─────────────
//
// All three hand-off points are loaded by the BROWSER (no Authorization
// header possible): the sandbox doc's canopy.__bootstrap.streamUrl, the
// download-only empty state's <a href download>, and viewer.get_stream_url.
// In a build whose only token is VITE_API_TOKEN / localStorage['canopy.token']
// the bare URL 401s TOKEN_MISSING, so the host must hand the DOM a blob:
// object URL resolved through the auth'd fetch path — and keep the bare URL
// (dev-proxy behaviour) when no token resolves.

describe('ViewerHost — authenticated stream URL hand-off (DF-HERMES-CANOPY-14)', () => {
  const SECOND_FILE_ID = '0198a7b6-c5d4-7321-8abc-def01234567a';
  const BARE_PATH = `/api/v1/files/${FILE_ID}/stream`;
  const BARE_SUFFIX = `/files/${FILE_ID}/stream`;
  const OBJECT_URL_1 = 'blob:http://localhost/canopy-obj-1';
  const OBJECT_URL_2 = 'blob:http://localhost/canopy-obj-2';

  const createObjectURL = vi.fn<(blob: Blob | MediaSource) => string>();
  const revokeObjectURL = vi.fn();
  const originalCreateObjectURL = URL.createObjectURL;
  const originalRevokeObjectURL = URL.revokeObjectURL;

  beforeEach(() => {
    let seq = 0;
    createObjectURL.mockReset();
    createObjectURL.mockImplementation(() => `blob:http://localhost/canopy-obj-${(seq += 1)}`);
    revokeObjectURL.mockReset();
    // jsdom implements neither createObjectURL nor revokeObjectURL.
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = revokeObjectURL;
    window.localStorage.clear();
    // A fresh Response per call — a Response body can only be read once.
    fetchMock.mockImplementation(() => Promise.resolve(new Response('pdf-bytes', { status: 200 })));
  });

  afterEach(() => {
    URL.createObjectURL = originalCreateObjectURL;
    URL.revokeObjectURL = originalRevokeObjectURL;
    vi.unstubAllEnvs();
    window.localStorage.clear();
  });

  /** Flushes the mount effect → fetch → blob → setState chain. */
  async function settleResolution(): Promise<void> {
    for (let round = 0; round < 4; round += 1) {
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 0));
        for (let tick = 0; tick < 8; tick += 1) await Promise.resolve();
      });
    }
  }

  it('hands the sandbox doc a blob: URL — never the bare stream path — when a token resolves', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    // Pending: the sandbox doc is not built until the URL resolves.
    expect(q('iframe')).toBeNull();

    await settleResolution();

    const iframe = q('iframe');
    expect(iframe).not.toBeNull();
    const srcDoc = iframe!.getAttribute('srcdoc') ?? '';
    expect(srcDoc).toContain('blob:');
    expect(srcDoc).toContain(OBJECT_URL_1);
    expect(srcDoc).not.toContain(BARE_PATH);
    expect(srcDoc).not.toContain(BARE_SUFFIX);
    // Nothing anywhere in the rendered DOM carries the bare API path.
    expect(container.innerHTML).not.toContain(BARE_SUFFIX);

    // The bytes came through the auth'd fetch path with the token, as one
    // whole-file open-ended range.
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [requested, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(requested).toBe(BARE_PATH);
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer build-token-123');
    expect(headers.get('Range')).toBe('bytes=0-');
  });

  it('gives the download-only empty state a blob: href (aria-disabled, no href, while pending)', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    mountViewer({ file: makeFile(), viewer: null });

    const pendingLink = q('[data-empty-state] a');
    expect(pendingLink).not.toBeNull();
    expect(pendingLink!.getAttribute('href')).toBeNull();
    expect(pendingLink!.getAttribute('aria-disabled')).toBe('true');

    await settleResolution();

    const link = q('[data-empty-state] a');
    expect(link!.getAttribute('href')).toBe(OBJECT_URL_1);
    expect(link!.getAttribute('aria-disabled')).toBeNull();
    expect(link!.getAttribute('download')).toBe('report.pdf');
    expect(container.innerHTML).not.toContain(BARE_SUFFIX);
  });

  it('answers viewer.get_stream_url with the resolved blob: URL (cached, no second fetch)', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    await settleResolution();

    const iframe = q('iframe') as HTMLIFrameElement;
    const responses = interceptFrame(iframe);
    const nonce = (iframe.getAttribute('srcdoc') ?? '').match(/var NONCE = "([^"]+)"/)![1];
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          origin: window.location.origin,
          data: {
            type: 'viewer_api_call',
            id: 'stream-call-1',
            target: 'host',
            nonce,
            payload: { method: 'viewer.get_stream_url', params: { range: null } },
            timestamp: Date.now(),
          },
        }),
      );
    });
    await settle();

    expect(responses).toHaveLength(1);
    expect((responses[0].m as Record<string, unknown>).result).toEqual({ url: OBJECT_URL_1 });
    expect(fetchMock).toHaveBeenCalledTimes(1); // the mount resolution is reused
  });

  it('keeps the bare URL and creates no object URL when no token resolves (dev proxy)', async () => {
    vi.stubEnv('VITE_API_TOKEN', '');
    mountViewer({ file: makeFile(), viewer: makeViewer() });

    // Synchronous — the dev-proxy path resolves nothing.
    const srcDoc = q('iframe')!.getAttribute('srcdoc') ?? '';
    expect(srcDoc).toContain(BARE_PATH);
    // No object URL was handed over (the CSP meta legitimately contains the
    // bare "blob:" keyword, so assert on the object-URL form).
    expect(srcDoc).not.toContain('blob:http://localhost');

    await settleResolution();

    expect(createObjectURL).not.toHaveBeenCalled();
    expect(fetchMock).not.toHaveBeenCalled();

    act(() => root.render(createElement(ViewerHost, { file: makeFile(), viewer: null })));
    const link = q('[data-empty-state] a');
    expect(link!.getAttribute('href')).toBe(BARE_PATH);
    expect(link!.getAttribute('aria-disabled')).toBeNull();
  });

  it('revokes the object URL on unmount (AC6)', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    await settleResolution();
    expect(revokeObjectURL).not.toHaveBeenCalled();

    act(() => root.unmount());
    expect(revokeObjectURL).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).toHaveBeenCalledWith(OBJECT_URL_1);

    // Leave a live root behind for the shared afterEach unmount.
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  it('revokes the previous object URL when the file changes (AC6)', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    mountViewer({ file: makeFile(), viewer: makeViewer() });
    await settleResolution();
    expect(q('iframe')!.getAttribute('srcdoc')).toContain(OBJECT_URL_1);

    const second = makeFile({ id: SECOND_FILE_ID, filename: 'other.pdf' });
    act(() => root.render(createElement(ViewerHost, { file: second, viewer: makeViewer() })));
    await settleResolution();

    expect(revokeObjectURL).toHaveBeenCalledWith(OBJECT_URL_1);
    const srcDoc = q('iframe')!.getAttribute('srcdoc') ?? '';
    expect(srcDoc).toContain(OBJECT_URL_2);
    expect(srcDoc).not.toContain(`/files/${SECOND_FILE_ID}/stream`);
  });
});
