/**
 * Unit tests — viewer body sources & registry (SPEC-PL-02 phases 3–7).
 * Pins: registry mapping (image/json/markdown non-null with expected hooks,
 * unknown → null), serialization validity (the bodies PARSE and EXECUTE
 * against a jsdom + stubbed canopy shim, exercising the load/error paths),
 * and config plumbing (collapse depth default from config).
 */

import { describe, it, expect, afterEach, vi } from 'vitest';
import { viewerBodyForSlug } from '../../viewerBodies';
import { imageHelpers, imageViewerBody } from '../imageViewerBody';
import { jsonViewerBody } from '../jsonViewerBody';
import { markdownViewerBody } from '../markdownViewerBody';
import { csvViewerBody, csvHelpers } from '../csvViewerBody';
import { mediaHelpers, mediaViewerBody } from '../mediaViewerBody';
import { codeViewerBody, codeHelpers } from '../codeViewerBody';

describe('viewerBodyForSlug registry', () => {
  it('returns non-null bodies for the four shipped slugs', () => {
    expect(viewerBodyForSlug('image')).toBe(imageViewerBody);
    expect(viewerBodyForSlug('json')).toBe(jsonViewerBody);
    expect(viewerBodyForSlug('audio_video')).toBe(mediaViewerBody);
    expect(viewerBodyForSlug('markdown')).toBe(markdownViewerBody);
    expect(viewerBodyForSlug('csv')).toBe(csvViewerBody);
    expect(viewerBodyForSlug('code')).toBe(codeViewerBody);
  });

  it('image body carries the stream-URL + zoom/rotate/pan hooks', () => {
    const body = viewerBodyForSlug('image') ?? '';
    expect(body).toContain('getStreamUrl');
    expect(body).toContain('wheelZoomMultiplier');
    expect(body).toContain('normalizeRotation');
    expect(body).toContain('clampPan');
    expect(body).toContain('image_error');
    expect(body).toContain('dblclick');
    expect(body).toContain('keydown');
    expect(body).toContain('dragstart');
  });

  it('json body carries the tree/search/copy hooks', () => {
    const body = viewerBodyForSlug('json') ?? '';
    expect(body).toContain('getTextContent');
    expect(body).toContain('parseJsonc');
    expect(body).toContain('json_error');
    expect(body).toContain('ctrlKey'); // Ctrl/Cmd+F search
    expect(body).toContain('json_parsed');
    expect(body).toContain('buildJsonPath');
  });

  it('code body carries the read-only detection, DOM, and event hooks', () => {
    expect(codeHelpers.detectCodeLanguage('x.py', '').language).toBe('python');
    const body = viewerBodyForSlug('code') ?? '';
    expect(body).toBe(codeViewerBody);
    expect(body).toContain('getTextContent');
    expect(body).toContain('detectCodeLanguage');
    expect(body).toContain('code_language_detected');
    expect(body).toContain('code_cursor_moved');
    expect(body).toContain('code_ready');
    expect(body).toContain('code_error');
    expect(body).toContain('logAccess');
    expect(body).toContain('ready');
    expect(() => new Function(body)).not.toThrow();
  });

  it('markdown body carries the render/sanitize/link hooks and §9.5 events', () => {
    const body = viewerBodyForSlug('markdown') ?? '';
    expect(body).toContain('getTextContent');
    expect(body).toContain('renderMarkdown');
    expect(body).toContain('sanitizeHtml');
    expect(body).toContain('countWords');
    expect(body).toContain('countBlocks');
    expect(body).toContain('markdown_rendered');
    expect(body).toContain('markdown_link_clicked');
    expect(body).toContain('markdown_error');
    expect(body).toContain('logAccess');
    expect(body).toContain('ready');
  });

  it('returns null for unknown slugs and the remaining built-ins', () => {
    expect(viewerBodyForSlug('pdf')).toBeNull();
    expect(viewerBodyForSlug('nonexistent')).toBeNull();
    expect(viewerBodyForSlug('Image')).toBeNull(); // case-sensitive
  });
});

describe('image body serialization contract', () => {
  it('helper bundle survives serialization: identical behavior standalone vs shipped', () => {
    // A few spot checks — full coverage lives in imageViewerLogic.test.ts.
    expect(imageHelpers.computeFitZoom(800, 600, 1600, 1200)).toBeCloseTo(0.5, 12);
    expect(imageHelpers.clampSensitivity(9)).toBe(3.0);
    expect(imageHelpers.normalizeRotation(-90)).toBe(270);
    expect(imageHelpers.clampPan(Number.NaN)).toBe(0);
  });

  it('body source is syntactically valid JS (parses with new Function)', () => {
    expect(() => new Function(imageViewerBody)).not.toThrow();
  });

  function stubRootElement(): { restore: () => void } {
    // The body looks for #root and falls back to document.body; give it a
    // dedicated element so tests do not dirty the shared body.
    const rootEl = document.createElement('div');
    rootEl.id = 'root';
    document.body.appendChild(rootEl);
    const rect = {
      width: 800,
      height: 600,
      top: 0,
      left: 0,
      right: 800,
      bottom: 600,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    } as DOMRect;
    const rectSpy = vi.spyOn(rootEl, 'getBoundingClientRect').mockReturnValue(rect);
    return {
      restore: () => {
        rectSpy.mockRestore();
        rootEl.remove();
      },
    };
  }

  afterEach(() => {
    // Body mutates the root element; a fresh one per test.
    document.getElementById('root')?.remove();
    delete (window as unknown as { canopy?: unknown }).canopy;
  });

  it('executes against a stubbed shim: renders <img>, load path, zoom events', () => {
    const rootStub = stubRootElement();
    const streamUrl = '/api/v1/files/f-1/stream';
    const handlers: Record<string, Array<(p: unknown) => void>> = {};
    (window as unknown as { canopy: unknown }).canopy = {
      version: '1.0.0',
      __handlers: handlers,
      __bootstrap: {
        fileMeta: { id: 'f-1', filename: 'photo.png', mimeType: 'image/png' },
        streamUrl,
        config: { imageZoomSensitivity: 2.0 },
      },
      viewer: {
        getStreamUrl: vi.fn(() => Promise.resolve({ url: streamUrl })),
        on: function (event: string, handler: (p: unknown) => void) {
          (handlers[event] = handlers[event] || []).push(handler);
          return function () {
            handlers[event] = handlers[event].filter(function (h) {
              return h !== handler;
            });
          };
        },
      },
    };

    // Capture the <img> the body creates, and simulate a successful decode.
    const realCreate = document.createElement.bind(document);
    let createdImg: HTMLImageElement | undefined;
    const createSpy = vi
      .spyOn(document, 'createElement')
      .mockImplementation(function (tagName: string) {
        const el = realCreate(tagName);
        if (tagName.toLowerCase() === 'img' && createdImg === undefined) createdImg = el as HTMLImageElement;
        return el;
      });

    try {
      new Function(imageViewerBody)();

      expect(createdImg).toBeDefined();
      const img = createdImg as HTMLImageElement;
      expect(img.getAttribute('src')).toBe(streamUrl);

      // Subscribe through the shim BEFORE events fire.
      const zoomEvents: unknown[] = [];
      handlers['image_zoom_changed'] = [(p: unknown) => zoomEvents.push(p)];
      const loadEvents: unknown[] = [];
      handlers['image_loaded'] = [(p: unknown) => loadEvents.push(p)];

      // Simulate decode success (jsdom never decodes).
      Object.defineProperty(img, 'naturalWidth', { value: 1600, configurable: true });
      Object.defineProperty(img, 'naturalHeight', { value: 1200, configurable: true });
      img.dispatchEvent(new window.Event('load'));

      expect(loadEvents).toHaveLength(1);
      expect(loadEvents[0]).toEqual({ width: 1600, height: 1200, mimeType: 'image/png' });
      // Fit zoom for 1600x1200 in 800x600 = 0.5 → applied transform scale.
      expect(img.style.transform).toContain('translate');
      expect(img.style.transform).toContain('scale(0.5');
      // The load path already emitted one zoom event (fit).
      expect(zoomEvents.length).toBe(1);
      zoomEvents.length = 0;

      // Keyboard zoom once.
      window.dispatchEvent(new window.KeyboardEvent('keydown', { key: '+' }));
      expect(zoomEvents.length).toBe(1);

      // getStreamUrl was consulted (authoritative reconcile).
      expect(
        (window as unknown as { canopy: { viewer: { getStreamUrl: ReturnType<typeof vi.fn> } } }).canopy.viewer
          .getStreamUrl,
      ).toHaveBeenCalled();
    } finally {
      createSpy.mockRestore();
      rootStub.restore();
    }
  });

  it('renders an honest error state on image decode failure (never a blank frame)', () => {
    const rootStub = stubRootElement();
    const handlers: Record<string, Array<(p: unknown) => void>> = {};
    (window as unknown as { canopy: unknown }).canopy = {
      __handlers: handlers,
      __bootstrap: {
        fileMeta: { filename: 'broken.png', mimeType: 'image/png' },
        streamUrl: '/stream/broken',
        config: {},
      },
      viewer: {},
    };
    const realCreate = document.createElement.bind(document);
    let createdImg: HTMLImageElement | undefined;
    const createSpy = vi
      .spyOn(document, 'createElement')
      .mockImplementation(function (tagName: string) {
        const el = realCreate(tagName);
        if (tagName.toLowerCase() === 'img' && createdImg === undefined) createdImg = el as HTMLImageElement;
        return el;
      });
    try {
      new Function(imageViewerBody)();
      const img = createdImg as HTMLImageElement;
      expect(img).toBeDefined();
      // Subscribe BEFORE the error fires.
      const errorEvents: unknown[] = [];
      handlers['image_error'] = [(p: unknown) => errorEvents.push(p)];
      img.dispatchEvent(new window.Event('error'));
      const errorBox = document.querySelector('[data-image-error]');
      expect(errorBox).not.toBeNull();
      expect(errorBox!.textContent).toContain('IMAGE_DECODE_ERROR');
      expect(errorBox!.textContent).toContain('image_error');
      expect(img.style.display).toBe('none');
      // Event reached the shim registry.
      expect(errorEvents).toHaveLength(1);
    } finally {
      createSpy.mockRestore();
      rootStub.restore();
    }
  });
});

describe('json body serialization contract', () => {
  function installShim(getText: () => Promise<string>): { handlers: Record<string, Array<(p: unknown) => void>> } {
    const handlers: Record<string, Array<(p: unknown) => void>> = {};
    (window as unknown as { canopy: unknown }).canopy = {
      version: '1.0.0',
      __handlers: handlers,
      __bootstrap: { fileMeta: { filename: 'data.json', mimeType: 'application/json' }, config: {} },
      viewer: {
        getTextContent: vi.fn(getText),
        on: function (event: string, handler: (p: unknown) => void) {
          (handlers[event] = handlers[event] || []).push(handler);
          return function () {
            handlers[event] = handlers[event].filter(function (h) {
              return h !== handler;
            });
          };
        },
      },
    };
    return { handlers };
  }

  afterEach(() => {
    document.getElementById('root')?.remove();
    delete (window as unknown as { canopy?: unknown }).canopy;
  });

  /** Fresh #root per test — the body wipes and fills it. */
  function ensureRootEl(): HTMLElement {
    let el = document.getElementById('root');
    if (el === null) {
      el = document.createElement('div');
      el.id = 'root';
      document.body.appendChild(el);
    }
    el.innerHTML = '';
    return el;
  }

  it('body source is syntactically valid JS (parses with new Function)', () => {
    expect(() => new Function(jsonViewerBody)).not.toThrow();
  });

  it('builds a collapsible tree from getTextContent (JSONC tolerated)', async () => {
    const { handlers } = installShim(() =>
      Promise.resolve('{\n  // note\n  "users": [{ "name": "Ada", "tags": ["x"] },],\n  "meta": { "n": 1, },\n}'),
    );
    const events: string[] = [];
    for (const name of ['json_parsed', 'json_error']) handlers[name] = [() => events.push(name)];
    ensureRootEl();
    new Function(jsonViewerBody)();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    const tree = document.querySelector('[data-json-tree]');
    expect(tree).not.toBeNull();
    const rows = document.querySelectorAll('[data-path]');
    expect(rows.length).toBeGreaterThan(0);
    // Root present; `$.users[0].tags` is the first container at depth 3 →
    // collapsed at the configured default depth → carries a key-count badge.
    expect(document.querySelector('[data-path="$"]')).not.toBeNull();
    const tagsRow = document.querySelector('[data-path="$.users[0].tags"]');
    expect(tagsRow).not.toBeNull();
    expect(tagsRow!.querySelector('.json-badge')).not.toBeNull();
    expect(document.querySelector('[data-json-stats]')!.textContent).toContain('entries');
    expect(events).toContain('json_parsed');
    expect(events).not.toContain('json_error');
  });

  it('renders json_error with line info on invalid JSON (never a blank frame)', async () => {
    const { handlers } = installShim(() => Promise.resolve('{\n  "a": 1,\n  "b": @@@\n}'));
    const errorEvents: Array<Record<string, unknown>> = [];
    handlers['json_error'] = [(p: unknown) => errorEvents.push(p as Record<string, unknown>)];
    ensureRootEl();
    new Function(jsonViewerBody)();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    const errorBox = document.querySelector('[data-json-error]');
    expect(errorBox).not.toBeNull();
    expect(errorBox!.textContent).toContain('json_error');
    expect(errorBox!.textContent).toContain('line 3');
    expect(errorEvents).toHaveLength(1);
    expect(errorEvents[0].code).toBe('JSON_PARSE_ERROR');
    expect(errorEvents[0].line).toBe(3);
  });

  it('defaults collapse depth from config.jsonCollapseDepth', async () => {
    const handlers: Record<string, Array<(p: unknown) => void>> = {};
    (window as unknown as { canopy: unknown }).canopy = {
      version: '1.0.0',
      __handlers: handlers,
      __bootstrap: {
        fileMeta: { filename: 'deep.json' },
        config: { jsonCollapseDepth: 1 },
      },
      viewer: { getTextContent: vi.fn(() => Promise.resolve('{"a":{"b":{"c":{"d":1}}}}')) },
    };
    ensureRootEl();
    new Function(jsonViewerBody)();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    // depth 1 → root object open (depth 0 < 1), its child (depth 1) collapsed.
    const childRow = document.querySelector('[data-path="$.a"]');
    expect(childRow).not.toBeNull();
    expect(childRow!.getAttribute('data-type')).toBe('object');
    // Only the root row's children are rendered; $.a is a badge row (collapsed).
    expect(childRow!.querySelector('.json-badge')).not.toBeNull();
    // Search + copy default enabled.
    expect(document.querySelector('[data-json-search]')).not.toBeNull();
  });

  it('search bar opens on Ctrl+F and navigates matches on Enter', async () => {
    const { handlers } = installShim(() => Promise.resolve('{"alpha": 1, "nested": { "beta": 2, "another": 3 }}'));
    const searchEvents: Array<Record<string, unknown>> = [];
    handlers['json_search_active'] = [(p: unknown) => searchEvents.push(p as Record<string, unknown>)];
    ensureRootEl();
    new Function(jsonViewerBody)();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    const bars = document.querySelectorAll('[data-json-search]');
    expect(bars.length).toBeGreaterThan(0);
    const searchBar = bars[0] as HTMLElement;
    expect(searchBar.style.display).toBe('none');

    // Ctrl+F opens the search bar.
    window.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'f', ctrlKey: true, cancelable: true }));
    expect(searchBar.style.display).toBe('block');

    const input = document.querySelector('[data-json-search-input]') as HTMLInputElement;
    expect(input).not.toBeNull();
    input.value = 'bet';
    input.dispatchEvent(new window.Event('input'));
    // hits found via the serialized matchJsonNodes
    const count = document.querySelector('[data-json-search-count]')!;
    expect(count.textContent).toContain('1/1');
    // search_active fired with the query + match count.
    expect(searchEvents.some((e) => e.query === 'bet' && e.matchCount === 1)).toBe(true);

    // Esc closes.
    input.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    expect(searchBar.style.display).toBe('none');
  });
});

describe('media body serialization and sandbox contract', () => {
  type HandlerMap = Record<string, Array<(payload: unknown) => void>>;

  function installMediaShim(options: {
    mimeType?: string;
    filename?: string;
    streamUrl?: string;
    config?: Record<string, unknown>;
    authoritativeUrl?: string;
    rejectStream?: boolean;
    includeStreamApi?: boolean;
  } = {}) {
    const handlers: HandlerMap = {};
    const getStreamUrl = vi.fn(() =>
      options.rejectStream
        ? Promise.reject(new Error('stream signing failed'))
        : Promise.resolve({ url: options.authoritativeUrl ?? options.streamUrl ?? '' }),
    );
    const logAccess = vi.fn(() => Promise.resolve({ ok: true }));
    const ready = vi.fn(() => Promise.resolve({ ok: true }));
    const viewer: Record<string, unknown> = {
      logAccess,
      ready,
      on: function (event: string, handler: (payload: unknown) => void) {
        (handlers[event] = handlers[event] || []).push(handler);
        return function () {
          handlers[event] = handlers[event].filter(function (candidate) {
            return candidate !== handler;
          });
        };
      },
    };
    if (options.includeStreamApi !== false) viewer.getStreamUrl = getStreamUrl;
    (window as unknown as { canopy: unknown }).canopy = {
      version: '1.0.0',
      fileId: 'media-1',
      __handlers: handlers,
      __bootstrap: {
        fileMeta: {
          id: 'media-1',
          filename: options.filename ?? 'sample.mp4',
          mimeType: options.mimeType ?? 'video/mp4',
        },
        streamUrl: options.streamUrl ?? '/stream/bootstrap',
        config: options.config ?? {},
      },
      viewer,
    };
    return { handlers, getStreamUrl, logAccess, ready };
  }

  function ensureMediaRoot(): HTMLElement {
    const root = document.createElement('div');
    root.id = 'root';
    document.body.appendChild(root);
    return root;
  }

  async function flushMediaPromises(): Promise<void> {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  }

  afterEach(() => {
    document.getElementById('root')?.remove();
    delete (window as unknown as { canopy?: unknown }).canopy;
    vi.restoreAllMocks();
  });

  it('serializes the tested helper bundle into syntactically valid executable JavaScript', () => {
    expect(mediaHelpers.mediaKindForMime('audio/ogg')).toBe('audio');
    expect(mediaHelpers.clampSeekTarget(150, 100)).toBe(100);
    expect(mediaHelpers.normalizePlaybackRate(Number.NaN)).toBe(1);
    expect(mediaHelpers.keyboardJumpPercentage('7')).toBe(0.7);
    expect(() => new Function(mediaViewerBody)).not.toThrow();
  });

  it('renders audio with bootstrap-first URL, authoritative reconciliation, config, controls, ready, and open logging', async () => {
    ensureMediaRoot();
    const shim = installMediaShim({
      mimeType: 'audio/mpeg',
      filename: 'interview.mp3',
      streamUrl: '/stream/bootstrap-audio',
      authoritativeUrl: '/stream/signed-audio',
      config: {
        autoPlayMedia: true,
        loopMedia: true,
        preloadMedia: 'auto',
        audioVolume: 0.35,
        playbackRate: 0.75,
      },
    });
    const plays: unknown[] = [];
    shim.handlers.media_play = [(payload: unknown) => plays.push(payload)];
    const play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(function (this: HTMLMediaElement) {
      this.dispatchEvent(new window.Event('play'));
      return Promise.resolve();
    });

    expect(() => new Function(mediaViewerBody)()).not.toThrow();
    const media = document.querySelector('audio');
    expect(media).not.toBeNull();
    expect(document.querySelector('video')).toBeNull();
    expect(media!.getAttribute('src')).toBe('/stream/bootstrap-audio');
    expect(media!.getAttribute('aria-label')).toContain('interview.mp3');
    expect(media!.autoplay).toBe(true);
    expect(media!.loop).toBe(true);
    expect(media!.preload).toBe('auto');
    expect(media!.volume).toBeCloseTo(0.35, 12);
    expect(media!.playbackRate).toBeCloseTo(0.75, 12);

    const controls = document.querySelector('[data-media-controls]');
    expect(controls).not.toBeNull();
    expect(controls!.getAttribute('role')).toBe('group');
    expect(document.querySelector('[data-media-play]')!.getAttribute('aria-label')).toBe('Play');
    expect(document.querySelector('[data-media-seek]')!.getAttribute('aria-label')).toBe('Seek');
    expect(document.querySelector('[data-media-volume]')!.getAttribute('aria-label')).toBe('Volume');
    expect(document.querySelector('[data-media-mute]')!.getAttribute('aria-label')).toBe('Mute');
    const rates = Array.from(document.querySelectorAll<HTMLSelectElement>('[data-media-rate] option')).map(
      (option) => option.value,
    );
    expect(rates).toEqual(expect.arrayContaining(['0.5', '1', '1.5', '2']));
    expect(rates).toContain('0.75');
    expect(document.querySelector('[data-media-fullscreen]')).toBeNull();
    expect(document.querySelector('[data-media-pip]')).toBeNull();

    window.dispatchEvent(new window.KeyboardEvent('keydown', { key: ' ', code: 'Space', cancelable: true }));
    await flushMediaPromises();
    expect(play).toHaveBeenCalledTimes(1);
    expect(plays).toEqual([{ currentTime: 0 }]);

    await flushMediaPromises();
    expect(media!.getAttribute('src')).toBe('/stream/signed-audio');
    expect(shim.getStreamUrl).toHaveBeenCalledWith(null);
    expect(shim.ready).toHaveBeenCalledTimes(1);
    expect(shim.logAccess).toHaveBeenCalledWith(
      'open',
      expect.objectContaining({ fileId: 'media-1', mediaKind: 'audio' }),
    );
  });

  it('renders video controls, subtitle/poster/PiP config, lifecycle payloads, and every §9.7 keyboard action', async () => {
    ensureMediaRoot();
    const originalPipEnabled = Object.getOwnPropertyDescriptor(document, 'pictureInPictureEnabled');
    Object.defineProperty(document, 'pictureInPictureEnabled', { value: true, configurable: true });
    const requestPip = vi.fn(() => Promise.resolve({}));
    Object.defineProperty(HTMLVideoElement.prototype, 'requestPictureInPicture', {
      value: requestPip,
      configurable: true,
    });
    const play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(function (this: HTMLMediaElement) {
      this.dispatchEvent(new window.Event('play'));
      return Promise.resolve();
    });
    vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(function (this: HTMLMediaElement) {
      this.dispatchEvent(new window.Event('pause'));
    });
    const shim = installMediaShim({
      mimeType: 'video/mp4',
      filename: 'launch.mp4',
      config: {
        subtitleUrl: '/captions/launch.vtt',
        showSubtitles: true,
        videoPoster: '/posters/launch.jpg',
        playbackRate: 1,
      },
    });
    const eventPayloads: Record<string, Array<Record<string, unknown>>> = {};
    for (const eventName of [
      'media_loaded',
      'media_play',
      'media_pause',
      'media_ended',
      'media_seek',
      'media_rate_change',
      'media_error',
    ]) {
      eventPayloads[eventName] = [];
      shim.handlers[eventName] = [function (payload: unknown) {
        eventPayloads[eventName].push(payload as Record<string, unknown>);
      }];
    }

    new Function(mediaViewerBody)();
    const video = document.querySelector('video') as HTMLVideoElement;
    expect(video).not.toBeNull();
    expect(video.autoplay).toBe(false);
    expect(video.loop).toBe(false);
    expect(video.preload).toBe('metadata');
    expect(video.volume).toBe(1);
    expect(video.playbackRate).toBe(1);
    expect(video.poster).toContain('/posters/launch.jpg');
    const track = video.querySelector('track');
    expect(track).not.toBeNull();
    expect(track!.kind).toBe('subtitles');
    expect(track!.src).toContain('/captions/launch.vtt');
    expect(track!.default).toBe(true);

    Object.defineProperty(video, 'duration', { value: 200, configurable: true });
    Object.defineProperty(video, 'currentTime', { value: 20, writable: true, configurable: true });
    Object.defineProperty(video, 'videoWidth', { value: 1920, configurable: true });
    Object.defineProperty(video, 'videoHeight', { value: 1080, configurable: true });
    const fullscreen = vi.fn(() => Promise.resolve());
    Object.defineProperty(video, 'requestFullscreen', { value: fullscreen, configurable: true });
    video.dispatchEvent(new window.Event('loadedmetadata'));
    expect(eventPayloads.media_loaded).toEqual([
      { duration: 200, width: 1920, height: 1080, hasAudio: false, hasVideo: true },
    ]);
    expect((document.querySelector('[data-media-time]') as HTMLElement).textContent).toBe('0:20 / 3:20');

    (document.querySelector('[data-media-play]') as HTMLButtonElement).click();
    await flushMediaPromises();
    expect(play).toHaveBeenCalledTimes(1);
    expect(eventPayloads.media_play).toEqual([{ currentTime: 20 }]);
    expect(shim.logAccess).toHaveBeenCalledWith(
      'stream_start',
      expect.objectContaining({ currentTime: 20, mediaKind: 'video' }),
    );

    window.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'ArrowRight', cancelable: true }));
    expect(video.currentTime).toBe(25);
    expect(eventPayloads.media_seek.at(-1)).toEqual({ from: 20, to: 25 });
    window.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'ArrowLeft', cancelable: true }));
    expect(video.currentTime).toBe(20);
    window.dispatchEvent(new window.KeyboardEvent('keydown', { key: '5', cancelable: true }));
    expect(video.currentTime).toBe(100);
    expect(eventPayloads.media_seek.at(-1)).toEqual({ from: 20, to: 100 });

    const beforeEditableShortcut = video.currentTime;
    const beforeEditableSeeks = eventPayloads.media_seek.length;
    const volumeInput = document.querySelector('[data-media-volume]') as HTMLInputElement;
    const rateControl = document.querySelector('[data-media-rate]') as HTMLSelectElement;
    const playControl = document.querySelector('[data-media-play]') as HTMLButtonElement;
    const textarea = document.createElement('textarea');
    const editable = document.createElement('div');
    editable.setAttribute('contenteditable', 'true');
    document.body.append(textarea, editable);
    for (const target of [volumeInput, rateControl, playControl, textarea, editable]) {
      target.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    }
    expect(video.currentTime).toBe(beforeEditableShortcut);
    expect(eventPayloads.media_seek).toHaveLength(beforeEditableSeeks);
    textarea.remove();
    editable.remove();

    volumeInput.value = '0.2';
    volumeInput.dispatchEvent(new window.Event('input'));
    expect(video.volume).toBeCloseTo(0.2, 12);

    window.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'm' }));
    expect(video.muted).toBe(true);
    window.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'f' }));
    expect(fullscreen).toHaveBeenCalledTimes(1);

    const rateSelect = document.querySelector('[data-media-rate]') as HTMLSelectElement;
    rateSelect.value = '1.5';
    rateSelect.dispatchEvent(new window.Event('change'));
    expect(video.playbackRate).toBe(1.5);
    expect(eventPayloads.media_rate_change.at(-1)).toEqual({ rate: 1.5 });

    const seek = document.querySelector('[data-media-seek]') as HTMLInputElement;
    seek.value = '999';
    seek.dispatchEvent(new window.Event('change'));
    expect(video.currentTime).toBe(200);
    expect(eventPayloads.media_seek.at(-1)).toEqual({ from: 100, to: 200 });

    (document.querySelector('[data-media-pip]') as HTMLButtonElement).click();
    await flushMediaPromises();
    expect(requestPip).toHaveBeenCalledTimes(1);
    video.dispatchEvent(new window.Event('pause'));
    video.dispatchEvent(new window.Event('ended'));
    expect(eventPayloads.media_pause.at(-1)).toEqual({ currentTime: 200 });
    expect(eventPayloads.media_ended).toEqual([{}]);

    if (originalPipEnabled) Object.defineProperty(document, 'pictureInPictureEnabled', originalPipEnabled);
    else delete (document as { pictureInPictureEnabled?: boolean }).pictureInPictureEnabled;
    delete (HTMLVideoElement.prototype as { requestPictureInPicture?: unknown }).requestPictureInPicture;
  });

  it('keeps a working bootstrap URL when authoritative reconciliation rejects', async () => {
    ensureMediaRoot();
    const shim = installMediaShim({ mimeType: 'audio/ogg', streamUrl: '/stream/working', rejectStream: true });
    new Function(mediaViewerBody)();
    const media = document.querySelector('audio')!;
    expect(media.getAttribute('src')).toBe('/stream/working');
    await flushMediaPromises();
    expect(media.getAttribute('src')).toBe('/stream/working');
    expect(shim.getStreamUrl).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[data-media-error]')!.getAttribute('data-visible')).toBe('false');
  });

  it('honors subtitle and PiP opt-outs while retaining safe media defaults', () => {
    ensureMediaRoot();
    installMediaShim({
      mimeType: 'video/quicktime',
      includeStreamApi: false,
      config: {
        subtitleUrl: '/captions/hidden.vtt',
        showSubtitles: false,
        pipEnabled: false,
        preloadMedia: 'invalid',
      },
    });
    new Function(mediaViewerBody)();
    const video = document.querySelector('video') as HTMLVideoElement;
    expect(video.querySelector('track')).toBeNull();
    expect(document.querySelector('[data-media-pip]')).toBeNull();
    expect(video.preload).toBe('metadata');
    expect(video.autoplay).toBe(false);
    expect(video.loop).toBe(false);
  });

  it.each([
    {
      name: 'unsupported MIME',
      options: { mimeType: 'application/pdf', streamUrl: '/stream/file', includeStreamApi: false },
      code: 'UNSUPPORTED_MEDIA_MIME',
    },
    {
      name: 'missing stream URL',
      options: { mimeType: 'audio/wav', streamUrl: '', includeStreamApi: false },
      code: 'STREAM_URL_MISSING',
    },
  ])('renders an honest visible error for $name and still notifies ready', ({ options, code }) => {
    ensureMediaRoot();
    const shim = installMediaShim(options);
    const errors: unknown[] = [];
    shim.handlers.media_error = [(payload: unknown) => errors.push(payload)];
    expect(() => new Function(mediaViewerBody)()).not.toThrow();
    const errorBox = document.querySelector('[data-media-error]')!;
    expect(errorBox.getAttribute('data-visible')).toBe('true');
    expect(errorBox.textContent).toContain(code);
    expect(errors).toEqual([expect.objectContaining({ code })]);
    expect(shim.logAccess).toHaveBeenCalledWith('error', expect.objectContaining({ code }));
    expect(shim.ready).toHaveBeenCalledTimes(1);
  });

  it('reports rejected play promises and native media errors without a false media_play event', async () => {
    ensureMediaRoot();
    vi.spyOn(HTMLMediaElement.prototype, 'play').mockRejectedValue(new Error('autoplay denied'));
    const shim = installMediaShim({ mimeType: 'video/webm' });
    const plays: unknown[] = [];
    const errors: Array<Record<string, unknown>> = [];
    shim.handlers.media_play = [(payload: unknown) => plays.push(payload)];
    shim.handlers.media_error = [(payload: unknown) => errors.push(payload as Record<string, unknown>)];
    new Function(mediaViewerBody)();
    const video = document.querySelector('video') as HTMLVideoElement;
    (document.querySelector('[data-media-play]') as HTMLButtonElement).click();
    await flushMediaPromises();
    expect(plays).toHaveLength(0);
    expect(errors.at(-1)).toEqual(expect.objectContaining({ code: 'MEDIA_PLAY_REJECTED' }));
    expect(document.querySelector('[data-media-error]')!.textContent).toContain('autoplay denied');

    Object.defineProperty(video, 'error', {
      value: { code: 3, message: 'decoder stopped' },
      configurable: true,
    });
    video.dispatchEvent(new window.Event('error'));
    expect(errors.at(-1)).toEqual(expect.objectContaining({ code: 'MEDIA_ERR_DECODE' }));
    expect(document.querySelector('[data-media-error]')!.textContent).toContain('decoder stopped');
    expect(shim.logAccess).toHaveBeenCalledWith(
      'error',
      expect.objectContaining({ code: 'MEDIA_ERR_DECODE' }),
    );
  });
});

describe('markdown body serialization and sandbox contract', () => {
  type HandlerMap = Record<string, Array<(payload: unknown) => void>>;

  function installMarkdownShim(options: {
    text?: () => Promise<string>;
    config?: Record<string, unknown>;
    omitTextApi?: boolean;
  } = {}) {
    const handlers: HandlerMap = {};
    const logAccess = vi.fn(() => Promise.resolve({ ok: true }));
    const ready = vi.fn(() => Promise.resolve({ ok: true }));
    const viewer: Record<string, unknown> = { logAccess, ready };
    if (!options.omitTextApi) {
      viewer.getTextContent = vi.fn(options.text ?? (() => Promise.resolve('# Hello\n\nWorld body.')));
    }
    (window as unknown as { canopy: unknown }).canopy = {
      version: '1.0.0',
      fileId: 'md-1',
      __handlers: handlers,
      __bootstrap: {
        fileMeta: { id: 'md-1', filename: 'notes.md', mimeType: 'text/markdown' },
        config: options.config ?? {},
      },
      viewer,
    };
    return { handlers, logAccess, ready };
  }

  function ensureMarkdownRoot(): HTMLElement {
    const root = document.createElement('div');
    root.id = 'root';
    document.body.appendChild(root);
    return root;
  }

  async function flushMarkdownPromises(): Promise<void> {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  }

  afterEach(() => {
    document.getElementById('root')?.remove();
    delete (window as unknown as { canopy?: unknown }).canopy;
    vi.restoreAllMocks();
  });

  it('body source is syntactically valid JS (parses with new Function)', () => {
    expect(() => new Function(markdownViewerBody)).not.toThrow();
  });

  it('renders a document via getTextContent and emits markdown_rendered with source-based counters', async () => {
    ensureMarkdownRoot();
    const shim = installMarkdownShim({
      text: () => Promise.resolve('# Title\n\nFirst para here.\n\n- [x] task\n- plain\n'),
    });
    const rendered: Array<Record<string, unknown>> = [];
    shim.handlers.markdown_rendered = [(p: unknown) => rendered.push(p as Record<string, unknown>)];

    expect(() => new Function(markdownViewerBody)()).not.toThrow();
    await flushMarkdownPromises();

    const article = document.querySelector('[data-markdown-content]');
    expect(article).not.toBeNull();
    expect(article!.querySelector('h1')!.textContent).toBe('Title');
    expect(article!.querySelector('p')!.textContent).toBe('First para here.');
    // Task list rendered as a disabled checkbox through the shipped helpers.
    expect(article!.querySelector('input[type="checkbox"][disabled]')).not.toBeNull();
    // Counters are source-based and token-shaped: #, Title, First, para,
    // here., -, [x], task, -, plain = 10 words; exactly 1 top-level paragraph.
    expect(rendered).toEqual([{ wordCount: 10, paragraphCount: 1 }]);
    // Shim surface exercised: log_access('view', …) then viewer.ready().
    expect(shim.logAccess).toHaveBeenCalledWith('view', expect.objectContaining({ fileId: 'md-1' }));
    expect(shim.ready).toHaveBeenCalledTimes(1);
  });

  it('honors config: fontSize clamp, maxWidth, task-list and linkTarget opt-outs', async () => {
    ensureMarkdownRoot();
    installMarkdownShim({
      text: () => Promise.resolve('[x](/internal)\n\n- [ ] a task'),
      config: { fontSize: 99, maxWidth: '40rem', renderTaskLists: false, linkTarget: '_self' },
    });
    new Function(markdownViewerBody)();
    await flushMarkdownPromises();

    const article = document.querySelector('[data-markdown-content]') as HTMLElement;
    const style = article.querySelector('style, [data-markdown-style]');
    const styleEl =
      style ?? (document.querySelector('[data-markdown-style]') as HTMLStyleElement | null);
    expect(styleEl).not.toBeNull();
    const css = styleEl!.textContent ?? '';
    expect(css).toContain('font-size: 20px;'); // clamped from 99 into 12–20
    expect(css).toContain('max-width: 40rem;');
    expect(article.querySelector('input[type="checkbox"]')).toBeNull(); // task rendering off
    expect(article.textContent).toContain('[ ] a task');
    expect(article.querySelector('a')!.getAttribute('target')).toBe('_self');
  });

  it('emits markdown_link_clicked with internal classification and preventDefaults internal hrefs', async () => {
    ensureMarkdownRoot();
    const shim = installMarkdownShim({
      text: () => Promise.resolve('[ext](https://example.com) and [int](#anchor)'),
    });
    const clicks: Array<Record<string, unknown>> = [];
    shim.handlers.markdown_link_clicked = [(p: unknown) => clicks.push(p as Record<string, unknown>)];
    new Function(markdownViewerBody)();
    await flushMarkdownPromises();

    const anchors = Array.from(document.querySelectorAll('[data-markdown-content] a')) as HTMLAnchorElement[];
    expect(anchors).toHaveLength(2);

    const extClick = new window.MouseEvent('click', { bubbles: true, cancelable: true });
    anchors[0].dispatchEvent(extClick);
    expect(extClick.defaultPrevented).toBe(false);
    expect(clicks).toEqual([{ href: 'https://example.com', internal: false }]);

    const intClick = new window.MouseEvent('click', { bubbles: true, cancelable: true });
    anchors[1].dispatchEvent(intClick);
    expect(intClick.defaultPrevented).toBe(true);
    expect(clicks[1]).toEqual({ href: '#anchor', internal: true });
  });

  it.each([
    {
      name: 'getTextContent rejects',
      text: () => Promise.reject(new Error('storage read failed')),
      code: 'TEXT_CONTENT_ERROR',
      message: 'storage read failed',
    },
    {
      name: 'shim lacks getTextContent',
      text: undefined,
      code: 'SHIM_MISSING',
      message: 'getTextContent',
    },
  ])('renders an honest markdown_error for $name and still notifies ready', async ({ text, code, message }) => {
    ensureMarkdownRoot();
    const shim = installMarkdownShim({ text, omitTextApi: text === undefined });
    const errors: Array<Record<string, unknown>> = [];
    shim.handlers.markdown_error = [(p: unknown) => errors.push(p as Record<string, unknown>)];

    expect(() => new Function(markdownViewerBody)()).not.toThrow();
    await flushMarkdownPromises();

    const errorBox = document.querySelector('[data-markdown-error]')!;
    expect(errorBox.getAttribute('data-visible')).toBe('true');
    expect(errorBox.textContent).toContain('markdown_error');
    expect(errorBox.textContent).toContain(code);
    expect(errors).toEqual([expect.objectContaining({ code: code, message: expect.stringContaining(message) })]);
    expect(shim.logAccess).toHaveBeenCalledWith('error', expect.objectContaining({ code: code }));
    expect(shim.ready).toHaveBeenCalledTimes(1);
  });

  it('sanitizes rendered output: script/iframe/onerror/javascript: never reach the DOM', async () => {
    ensureMarkdownRoot();
    installMarkdownShim({
      text: () =>
        Promise.resolve(
          '```\n<script>alert(1)</script>\n```\n\n<script>alert(2)</script> plain after',
        ),
    });
    new Function(markdownViewerBody)();
    await flushMarkdownPromises();

    const article = document.querySelector('[data-markdown-content]')!;
    // The fence content renders as ESCAPED code text.
    expect(article.querySelector('pre code')!.textContent).toContain('<script>');
    // No live script/iframe element exists anywhere in the rendered doc.
    expect(article.querySelector('script')).toBeNull();
    expect(article.querySelector('iframe')).toBeNull();
    expect(article.innerHTML).not.toMatch(/<script(?![a-z-])/i);
    expect(article.innerHTML).not.toContain('onerror=');
    // javascript: hrefs are dropped at render time.
    expect(article.innerHTML).not.toContain('javascript:');
  });
});

describe('csv body serialization and sandbox contract', () => {
  type HandlerMap = Record<string, Array<(payload: unknown) => void>>;

  function installCsvShim(
    text: () => Promise<string>,
    config: Record<string, unknown> = {},
  ): { handlers: HandlerMap; getTextContent: ReturnType<typeof vi.fn>; logAccess: ReturnType<typeof vi.fn>; ready: ReturnType<typeof vi.fn> } {
    const handlers: HandlerMap = {};
    const getTextContent = vi.fn(text);
    const logAccess = vi.fn(() => Promise.resolve({ ok: true }));
    const ready = vi.fn(() => Promise.resolve({ ok: true }));
    (window as unknown as { canopy: unknown }).canopy = {
      version: '1.0.0',
      fileId: 'csv-1',
      __handlers: handlers,
      __bootstrap: { fileMeta: { id: 'csv-1', filename: 'people.csv', mimeType: 'text/csv' }, config },
      viewer: { getTextContent, logAccess, ready },
    };
    return { handlers, getTextContent, logAccess, ready };
  }

  function ensureCsvRoot(): void {
    const root = document.createElement('div');
    root.id = 'root';
    document.body.appendChild(root);
  }

  async function flushCsvPromises(): Promise<void> {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  }

  afterEach(() => {
    document.getElementById('root')?.remove();
    delete (window as unknown as { canopy?: unknown }).canopy;
    vi.restoreAllMocks();
  });

  it('parses and executes the shipped serialized body without free identifiers', () => {
    expect(csvHelpers.clampRowHeight(999)).toBe(80);
    expect(() => new Function(csvViewerBody)).not.toThrow();
    ensureCsvRoot();
    installCsvShim(() => Promise.resolve('Name,Age\nAda,36'));
    expect(() => new Function(csvViewerBody)()).not.toThrow();
  });

  it('renders an accessible table through getTextContent and emits ready/log/ready hooks', async () => {
    ensureCsvRoot();
    const shim = installCsvShim(() => Promise.resolve('Name,Age\nAda,36\nGrace,28'));
    const readyEvents: unknown[] = [];
    shim.handlers.csv_ready = [(payload: unknown) => readyEvents.push(payload)];
    new Function(csvViewerBody)();
    await flushCsvPromises();

    expect(shim.getTextContent).toHaveBeenCalledTimes(1);
    const table = document.querySelector('[data-csv-table]');
    expect(table).not.toBeNull();
    expect(table!.querySelectorAll('th')).toHaveLength(4);
    expect(table!.querySelector('th button')!.textContent).toContain('Name');
    expect(table!.querySelectorAll('[data-csv-cell]')).toHaveLength(4);
    expect((table!.querySelector('[data-csv-cell]') as HTMLElement).tabIndex).toBe(0);
    expect(readyEvents).toEqual([{ rowCount: 2, colCount: 2 }]);
    expect(shim.logAccess).toHaveBeenCalledWith('view', expect.objectContaining({ fileId: 'csv-1' }));
    expect(shim.ready).toHaveBeenCalledTimes(1);
  });

  it('sorts, filters, and selects cells with exact frame-local payloads', async () => {
    ensureCsvRoot();
    const shim = installCsvShim(() => Promise.resolve('Name,Age\nAda,36\nGrace,28'));
    const sorted: unknown[] = [];
    const filtered: unknown[] = [];
    const selected: unknown[] = [];
    shim.handlers.csv_sorted = [(payload: unknown) => sorted.push(payload)];
    shim.handlers.csv_filtered = [(payload: unknown) => filtered.push(payload)];
    shim.handlers.csv_cell_selected = [(payload: unknown) => selected.push(payload)];
    new Function(csvViewerBody)();
    await flushCsvPromises();

    (document.querySelector('[data-csv-sort="1"]') as HTMLButtonElement).click();
    expect(sorted).toEqual([{ column: 1, direction: 'asc' }]);
    const filter = document.querySelector('[data-csv-filter="0"]') as HTMLInputElement;
    filter.value = 'ada';
    filter.dispatchEvent(new window.Event('input', { bubbles: true }));
    expect(filtered).toEqual([{ visibleRows: 1, totalRows: 2 }]);
    const cell = document.querySelector('[data-csv-cell]') as HTMLElement;
    cell.dispatchEvent(new window.MouseEvent('click', { bubbles: true }));
    expect(selected).toEqual([{ row: 0, col: 0, value: 'Ada' }]);
    cell.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    expect(selected).toHaveLength(2);
  });

  it('honors header, filter, row-height, and render-cap config', async () => {
    ensureCsvRoot();
    installCsvShim(
      () => Promise.resolve('1,alpha\n2,beta\n3,gamma'),
      { csvFirstRowIsHeader: false, showFilters: false, rowHeights: 99, maxRowsForInlineRender: 1 },
    );
    new Function(csvViewerBody)();
    await flushCsvPromises();

    expect(document.querySelectorAll('[data-csv-header]')).toHaveLength(2);
    expect(document.querySelectorAll('[data-csv-filter]')).toHaveLength(0);
    expect(document.querySelectorAll('[data-csv-cell]')).toHaveLength(2);
    expect(document.querySelector('[data-csv-style]')!.textContent).toContain('height:80px');
    expect(document.querySelector('[data-csv-truncated]')!.textContent).toContain('Showing 1 of 3');
  });

  it('renders parse and fetch failures as alerts and emits csv_error with location when available', async () => {
    ensureCsvRoot();
    const shim = installCsvShim(() => Promise.resolve('a,b\n"unclosed'));
    const errors: unknown[] = [];
    shim.handlers.csv_error = [(payload: unknown) => errors.push(payload)];
    new Function(csvViewerBody)();
    await flushCsvPromises();
    expect(document.querySelector('[data-csv-error]')!.getAttribute('role')).toBe('alert');
    expect(document.querySelector('[data-csv-error]')!.textContent).toContain('Unterminated');
    expect(errors).toEqual([expect.objectContaining({ code: 'CSV_PARSE_ERROR', row: 2, col: 10 })]);
    expect(shim.logAccess).toHaveBeenCalledWith('error', expect.objectContaining({ code: 'CSV_PARSE_ERROR' }));
    expect(shim.ready).toHaveBeenCalledTimes(1);

    document.getElementById('root')?.remove();
    ensureCsvRoot();
    const rejected = installCsvShim(() => Promise.reject(new Error('storage failed')));
    const fetchErrors: unknown[] = [];
    rejected.handlers.csv_error = [(payload: unknown) => fetchErrors.push(payload)];
    new Function(csvViewerBody)();
    await flushCsvPromises();
    expect(fetchErrors).toEqual([{ code: 'TEXT_CONTENT_ERROR', message: 'storage failed' }]);
  });
});
