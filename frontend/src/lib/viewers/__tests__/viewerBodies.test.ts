/**
 * Unit tests — viewer body sources & registry (SPEC-PL-02 phase 3).
 * Pins: registry mapping (image/json non-null with expected hooks, unknown
 * → null), serialization validity (both bodies PARSE and EXECUTE against a
 * jsdom + stubbed canopy shim, exercising the load/error paths), and
 * config plumbing (collapse depth default from config).
 */

import { describe, it, expect, afterEach, vi } from 'vitest';
import { viewerBodyForSlug } from '../../viewerBodies';
import { imageHelpers, imageViewerBody } from '../imageViewerBody';
import { jsonViewerBody } from '../jsonViewerBody';

describe('viewerBodyForSlug registry', () => {
  it('returns non-null bodies for the two shipped slugs', () => {
    expect(viewerBodyForSlug('image')).toBe(imageViewerBody);
    expect(viewerBodyForSlug('json')).toBe(jsonViewerBody);
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

  it('returns null for unknown slugs and the other built-ins', () => {
    expect(viewerBodyForSlug('pdf')).toBeNull();
    expect(viewerBodyForSlug('code')).toBeNull();
    expect(viewerBodyForSlug('csv')).toBeNull();
    expect(viewerBodyForSlug('markdown')).toBeNull();
    expect(viewerBodyForSlug('audio_video')).toBeNull();
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
