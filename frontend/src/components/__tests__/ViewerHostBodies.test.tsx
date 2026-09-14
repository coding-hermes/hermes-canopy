/**
 * Component tests — ViewerHost.buildViewerDoc × viewer bodies (PL02-P3, pdf
 * extended by PL02-P9).
 *
 * Pins the srcDoc contract:
 *   - built-in slugs → doc contains the shim (nonce substituted), the
 *     phase-9 bootstrap-extras script, AND the corresponding body <script>
 *   - slug 'pdf' additionally carries ONLY local same-origin pdf.js asset
 *     URLs in its bootstrap (no CDN / data: / network source)
 *   - unknown-body slugs → shim + empty bootstrap extras, no body script
 *   - the §8.2 CSP meta appears EXACTLY once, byte-identical; the only
 *     phase-9 addition is the minimum local worker rule
 *   - no __PLACEHOLDER__ survives substitution in any script
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
    // Shim + bootstrap-extras + exactly one body script after the shim.
    expect(doc.match(/<script>/g)).toHaveLength(3);
    // Non-pdf slugs carry NO pdf.js asset URLs in their bootstrap.
    expect(doc).not.toContain('"pdfjs"');
  });

  it("slug 'json' → shim + json tree body with getTextContent wiring", () => {
    const doc = buildFor('json');
    expect(doc).toContain(`var NONCE = "${NONCE}"`);
    expect(doc).toContain('getTextContent');
    expect(doc).toContain('parseJsonc');
    expect(doc).toContain('json_error');
    expect(doc).toContain('matchJsonNodes');
    expect(doc.match(/<script>/g)).toHaveLength(3);
    expect(doc).not.toContain('"pdfjs"');
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
    expect(doc.match(/<script>/g)).toHaveLength(3);
    expect(doc).not.toContain('"pdfjs"');
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
      expect(scripts).toHaveLength(3);
      new Function(scripts[0])();
      new Function(scripts[1])();
      new Function(scripts[2])();
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

  it("slug 'pdf' (phase 9 body shipped) → shim + pdf.js bootstrap + canvas body injected", () => {
    const doc = buildFor('pdf');
    expect(doc).toContain(`var NONCE = "${NONCE}"`);
    expect(doc).toContain('window.canopy');
    // Shim + bootstrap-extras script + body script, body after the shim.
    expect(doc.match(/<script>/g)).toHaveLength(3);
    const shimAt = doc.indexOf('window.canopy');
    const bodyAt = doc.indexOf('GlobalWorkerOptions');
    expect(shimAt).toBeGreaterThan(-1);
    expect(bodyAt).toBeGreaterThan(shimAt);
    // The bootstrap-extras script carries ONLY the local pdf.js asset URLs.
    expect(doc).toContain('window.canopy.__bootstrap = Object.assign(window.canopy.__bootstrap, {"pdfjs":');
    expect(doc).toContain('"moduleUrl"');
    expect(doc).toContain('"workerUrl"');
    // pdf.js body markers (canvas + fallback + §9.1 events).
    expect(doc).toContain('GlobalWorkerOptions.workerSrc');
    expect(doc).toContain('getDocument');
    expect(doc).toContain('data-pdf-canvas');
    expect(doc).toContain('pdf_ready');
    expect(doc).toContain('pdf_page_visible');
    expect(doc).toContain('pdf_zoom_changed');
    expect(doc).toContain('pdf_error');
    // The native <object> embed path is gone as the primary route.
    expect(doc).not.toContain("embed.type = 'application/pdf'");
  });

  it('unknown slug (no body shipped) → shim + empty bootstrap extras, no pdf.js assets, no body', () => {
    const doc = buildFor('definitely-not-a-slug');
    expect(doc).toContain(`var NONCE = "${NONCE}"`);
    expect(doc).toContain('window.canopy');
    // Shim + bootstrap-extras (empty for unknown slugs); no body script.
    expect(doc.match(/<script>/g)).toHaveLength(2);
    expect(doc).toContain('Object.assign(window.canopy.__bootstrap, {})');
    // No built-in body hooks leaked in.
    expect(doc).not.toContain('getStreamUrl(null)');
    expect(doc).not.toContain('parseJsonc');
    expect(doc).not.toContain('GlobalWorkerOptions');
    expect(doc).not.toContain('"pdfjs"');
  });

  it('CSP meta is present exactly once and byte-identical in every variant', () => {
    for (const slug of ['image', 'json', 'audio_video', 'pdf']) {
      const doc = buildFor(slug);
      expect(doc.split(VIEWER_SANDBOX_CSP)).toHaveLength(2); // exactly once
      expect(doc).toContain(`<meta http-equiv="Content-Security-Policy" content="${VIEWER_SANDBOX_CSP}"`);
    }
  });

  it('CSP stays least-privilege: adds only the local worker rule over §8.2', () => {
    const directives = VIEWER_SANDBOX_CSP.split('; ').map((entry) => entry.trim());
    const byName = new Map(directives.map((directive) => [directive.split(' ', 1)[0], directive]));
    // Exactly one new directive vs the phase-2 CSP, and it is the worker rule.
    expect(directives).toHaveLength(8);
    expect(byName.get('worker-src')).toBe("worker-src 'self' blob:");
    // No wildcards and no remote origins anywhere.
    for (const directive of directives) {
      expect(directive).not.toContain('*');
      expect(directive).not.toMatch(/https?:\/\//);
    }
    // Scripts (frame + worker) admit no data: sources.
    expect(byName.get('script-src')).toBe("script-src 'self' 'unsafe-inline'");
    expect(byName.get('worker-src')).not.toContain('data:');
    // Pre-existing directives are byte-identical to the phase-2 contract.
    expect(byName.get('default-src')).toBe("default-src 'none'");
    expect(byName.get('connect-src')).toBe("connect-src 'self'");
  });

  it('pdf bootstrap carries same-origin asset URLs only (no CDN, no data:, no network)', () => {
    const doc = buildFor('pdf');
    const extrasAt = doc.indexOf('Object.assign(window.canopy.__bootstrap,');
    expect(extrasAt).toBeGreaterThan(-1);
    const extras = doc.slice(extrasAt, doc.indexOf('});', extrasAt) + 3);
    expect(extras).not.toMatch(/https?:\/\//);
    expect(extras).not.toContain('data:');
    expect(extras).toContain('"pdfjs"');
    expect(extras).toContain('"moduleUrl"');
    expect(extras).toContain('"workerUrl"');
    // Vite emits the assets under its hashed assets dir (relative URLs).
    expect(extras).toMatch(/"moduleUrl":"[^"]+"/);
    expect(extras).toMatch(/"workerUrl":"[^"]+"/);
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
    // All three scripts still present despite the hostile metadata.
    expect(hostile.match(/<script>/g)).toHaveLength(3);
  });

  it('body script comes after the shim script (document order)', () => {
    const doc = buildFor('json');
    const shimAt = doc.indexOf('window.canopy');
    const bodyAt = doc.indexOf('parseJsonc');
    expect(shimAt).toBeGreaterThan(-1);
    expect(bodyAt).toBeGreaterThan(shimAt);
  });
});
