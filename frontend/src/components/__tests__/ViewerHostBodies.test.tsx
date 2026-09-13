/**
 * Component tests — ViewerHost.buildViewerDoc × viewer bodies (PL02-P3).
 *
 * Pins the phase-3 srcDoc contract:
 *   - slug 'image' / 'json' → doc contains the shim (nonce substituted) AND
 *     the corresponding body <script>
 *   - unknown-body slugs ('pdf') → unchanged phase-2 shim-only doc
 *   - the §8.2 CSP meta appears EXACTLY once, byte-identical
 *   - no __PLACEHOLDER__ survives substitution in either script
 */

import { describe, it, expect, vi } from 'vitest';
import { VIEWER_SANDBOX_CSP, buildViewerDoc } from '../ViewerHost.tsx';
import type { FileMetadata, ViewerRegistration } from '../../types/fileviewer';

const FILE_ID = '0198a7b6-c5d4-7321-8abc-def012345679';
const VIEWER_ID = '0198a7b6-c5d4-7321-8abc-def012345680';
const PROFILE_ID = '0198a7b6-c5d4-7321-8abc-def012345678';
const NONCE = 'nonce-1234';
const ORIGIN = 'https://canopy.test';

function makeFile(overrides: Partial<FileMetadata> = {}): FileMetadata {
  return {
    id: FILE_ID,
    profileId: PROFILE_ID,
    sha256: 'a3f5b8c9d1e2f4a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0',
    byteSize: 524288,
    mimeType: 'application/octet-stream',
    declaredMime: 'application/octet-stream',
    filename: 'sample.bin',
    extension: 'bin',
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

function makeViewer(slug: string): ViewerRegistration {
  return {
    id: VIEWER_ID,
    viewerSlug: slug,
    version: '0.4.2',
    canopydVersion: '0.4.2',
    displayName: slug.toUpperCase(),
    description: '',
    iconUrl: '',
    renderType: 'fullscreen',
    supportsMime: ['*/*'],
    supportsExtensions: [],
    supportsViewerHint: [],
    requiredCapabilities: [],
    bundlePath: '',
    bundleByteSize: 0,
    bundleSha256: '',
    minCanopydVersion: '0.4.0',
    isActive: true,
    installedAt: '2026-09-01T12:00:00+00:00',
  };
}

function buildFor(slug: string): string {
  return buildViewerDoc({
    file: makeFile(),
    viewer: makeViewer(slug),
    nonce: NONCE,
    parentOrigin: ORIGIN,
    streamUrl: `/api/v1/files/${FILE_ID}/stream`,
    config: {},
  });
}

describe('buildViewerDoc — phase 3 body injection', () => {
  it("slug 'image' → shim + image body with getStreamUrl wiring", () => {
    const doc = buildFor('image');
    // Shim markers, nonce substituted.
    expect(doc).toContain(`var NONCE = "${NONCE}"`);
    expect(doc).toContain('window.canopy');
    expect(doc).toContain('viewer_api_response');
    // Body markers: <img>-based, stream URL via the shim, zoom/rotate/pan.
    expect(doc).toContain('getStreamUrl');
    expect(doc).toContain('"img"');
    expect(doc).toContain('wheelZoomMultiplier');
    expect(doc).toContain('normalizeRotation');
    expect(doc).toContain('clampPan');
    expect(doc).toContain('image_error');
    // Exactly one body script appended after the shim script.
    expect(doc.match(/<script>/g)).toHaveLength(2);
  });

  it("slug 'json' → shim + json tree body with getTextContent wiring", () => {
    const doc = buildFor('json');
    expect(doc).toContain(`var NONCE = "${NONCE}"`);
    expect(doc).toContain('getTextContent');
    expect(doc).toContain('parseJsonc');
    expect(doc).toContain('json_error');
    expect(doc).toContain('matchJsonNodes');
    expect(doc.match(/<script>/g)).toHaveLength(2);
  });

  it("slug 'audio_video' → shim + native media body with custom controls", () => {
    const doc = buildFor('audio_video');
    expect(doc).toContain(`var NONCE = "${NONCE}"`);
    expect(doc).toContain('getStreamUrl');
    expect(doc).toContain("logAccess: function(action, metadata) { return callAPI('viewer.log_access', { action: action, metadata: metadata }); }");
    expect(doc).toContain('mediaKindForMime');
    expect(doc).toContain('data-media-controls');
    expect(doc).toContain('media_loaded');
    expect(doc).toContain('requestPictureInPicture');
    expect(doc.match(/<script>/g)).toHaveLength(2);
  });

  it('routes real audio_video body open/stream_start/error logs through the injected shim', () => {
    vi.useFakeTimers();
    const root = document.createElement('div');
    root.id = 'root';
    document.body.appendChild(root);
    const doc = buildViewerDoc({
      file: makeFile({ filename: 'sample.mp4', mimeType: 'video/mp4' }),
      viewer: makeViewer('audio_video'),
      nonce: NONCE,
      parentOrigin: ORIGIN,
      streamUrl: `/api/v1/files/${FILE_ID}/stream`,
      config: {},
    });
    const parsed = new DOMParser().parseFromString(doc, 'text/html');
    const scripts = Array.from(parsed.querySelectorAll('script')).map((script) => script.textContent ?? '');
    const posts: Array<Record<string, unknown>> = [];
    const postSpy = vi.spyOn(window, 'postMessage').mockImplementation((message: unknown) => {
      posts.push(message as Record<string, unknown>);
    });

    try {
      expect(scripts).toHaveLength(2);
      new Function(scripts[0])();
      new Function(scripts[1])();
      const media = document.querySelector('video') as HTMLVideoElement;
      expect(media).not.toBeNull();
      media.dispatchEvent(new window.Event('play'));
      Object.defineProperty(media, 'error', {
        value: { code: 3, message: 'decoder stopped' },
        configurable: true,
      });
      media.dispatchEvent(new window.Event('error'));

      const logCalls = posts
        .filter((message) => (message.payload as { method?: string } | undefined)?.method === 'viewer.log_access')
        .map((message) => (message.payload as { params: { action: string; metadata: Record<string, unknown> } }).params);
      expect(logCalls.map((call) => call.action)).toEqual(['open', 'stream_start', 'error']);
      expect(logCalls[2].metadata).toMatchObject({ errorCode: 'MEDIA_ERR_DECODE' });
    } finally {
      vi.clearAllTimers();
      vi.useRealTimers();
      postSpy.mockRestore();
      root.remove();
      delete (window as unknown as { canopy?: unknown }).canopy;
    }
  });

  it("slug 'pdf' (no body shipped) → unchanged shim-only doc", () => {
    const doc = buildFor('pdf');
    expect(doc).toContain(`var NONCE = "${NONCE}"`);
    expect(doc).toContain('window.canopy');
    expect(doc.match(/<script>/g)).toHaveLength(1);
    // No phase-3 body hooks leaked in.
    expect(doc).not.toContain('getStreamUrl(null)');
    expect(doc).not.toContain('parseJsonc');
  });

  it('CSP meta is present exactly once and byte-identical in every variant', () => {
    for (const slug of ['image', 'json', 'audio_video', 'pdf']) {
      const doc = buildFor(slug);
      expect(doc.split(VIEWER_SANDBOX_CSP)).toHaveLength(2); // exactly once
      expect(doc).toContain(`<meta http-equiv="Content-Security-Policy" content="${VIEWER_SANDBOX_CSP}"`);
    }
  });

  it('no __PLACEHOLDER__ survives in the body-injected doc; hostile metadata still escaped', () => {
    const hostile = buildViewerDoc({
      file: makeFile({ filename: 'ok.png', metadata: { x: '</script><script>alert(1)</script>' } }),
      viewer: makeViewer('image'),
      nonce: NONCE,
      parentOrigin: ORIGIN,
      streamUrl: '/stream',
      config: {},
    });
    expect(hostile).not.toContain('__NONCE__');
    expect(hostile).not.toContain('__FILE_ID__');
    expect(hostile).not.toContain('__STREAM_URL__');
    expect(hostile).not.toContain('__VIEWER_CONFIG_JSON__');
    expect(hostile).toContain('\\u003c/script>');
    expect(hostile).not.toContain('</script><script>alert(1)</script>');
    // Both scripts still present despite the hostile metadata.
    expect(hostile.match(/<script>/g)).toHaveLength(2);
  });

  it('body script comes after the shim script (document order)', () => {
    const doc = buildFor('json');
    const shimAt = doc.indexOf('window.canopy');
    const bodyAt = doc.indexOf('parseJsonc');
    expect(shimAt).toBeGreaterThan(-1);
    expect(bodyAt).toBeGreaterThan(shimAt);
  });
});
