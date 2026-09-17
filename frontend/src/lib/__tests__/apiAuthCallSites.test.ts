/**
 * Unit tests — previously-unauthenticated call sites now go through the shared
 * token source (DF-HERMES-CANOPY-13)
 *
 * DF-HERMES-CANOPY-7 gave `lib/api.ts` a bearer-token path, but that file was
 * only one of many fetch sites: fileApi, ApprovalPanel, ShareDialog, pluginApi,
 * activeTree, the hooks and the SSE feeds all sent NO Authorization header, so
 * they 401'd with TOKEN_MISSING in a static production build. These tests drive
 * REAL call sites with a stubbed `fetch` and assert both modes:
 *
 *  - a token resolves  → `Authorization: Bearer <token>` (plus the site's own
 *    headers/options, e.g. fileApi's `Range`)
 *  - no token (dev)    → no Authorization key at all, request shape unchanged
 *
 * The last case is a structural guard that mirrors the acceptance grep: no call
 * site in `src` may keep a raw `fetch(`/`new EventSource` outside the auth
 * plumbing. Real behaviour is pinned by the behavioural tests above it.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { fetchFileRange, listFiles } from '../fileApi';
import { resolveDemoAlias } from '../activeTree';
import { TOKEN_STORAGE_KEY } from '../api';
import { SSESyncProvider } from '../../stores/yjsProvider';
import { createTreeDoc } from '../../stores/treeStore';

/**
 * Every source module under `src`, read as raw text. `import.meta.glob` keeps
 * this node-free (the app tsconfig only loads `vite/client` types).
 */
const SOURCES = import.meta.glob('/src/**/*.{ts,tsx}', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** SSE response body that stays open (a live feed). */
function openFeed(): Response {
  const stream = new ReadableStream<Uint8Array>({ start() {} });
  return new Response(stream, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  });
}

/** Authorization header of a recorded call, or null when absent. */
function authOf(init: RequestInit | undefined): string | null {
  if (!init || init.headers === undefined) return null;
  return new Headers(init.headers).get('Authorization');
}

describe('call sites honour the shared token source', () => {
  const fetchMock = vi.fn();
  const eventSourceSpy = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse({})));
    vi.stubGlobal('fetch', fetchMock);
    eventSourceSpy.mockReset();
    vi.stubGlobal('EventSource', eventSourceSpy);
    window.localStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    window.localStorage.clear();
  });

  // ─── lib/fileApi.ts (a previously unauthenticated module) ───────────────

  it('fetchFileRange sends the bearer token and keeps its Range header', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    fetchMock.mockResolvedValueOnce(new Response('partial', { status: 206 }));

    await fetchFileRange('file-1', 100, 199);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/files/file-1/stream');
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer build-token-123');
    expect(headers.get('Range')).toBe('bytes=100-199');
  });

  it('fetchFileRange adds no Authorization key in dev-proxy mode', async () => {
    fetchMock.mockResolvedValueOnce(new Response('partial', { status: 206 }));

    await fetchFileRange('file-1', 100, 199);

    const [, init] = fetchMock.mock.calls[0];
    // The caller's own init survives verbatim — no added header, no Headers wrap.
    expect(init.headers).toEqual({ Range: 'bytes=100-199' });
    expect(authOf(init)).toBeNull();
  });

  it('listFiles authenticates a bare GET and stays init-less without a token', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    fetchMock.mockResolvedValueOnce(jsonResponse({ files: [], pagination: { count: 0 } }));
    await listFiles({ limit: 10 });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/files?limit=10');
    expect(authOf(init)).toBe('Bearer build-token-123');

    fetchMock.mockClear();
    vi.unstubAllEnvs();
    vi.stubEnv('VITE_API_TOKEN', '');
    fetchMock.mockResolvedValueOnce(jsonResponse({ files: [], pagination: { count: 0 } }));
    await listFiles({ limit: 10 });
    expect(fetchMock.mock.calls[0][1]).toBeUndefined();
    expect(authOf(fetchMock.mock.calls[0][1])).toBeNull();
  });

  // ─── lib/activeTree.ts (was a hardcoded /api/v1 URL) ─────────────────────

  it('resolveDemoAlias authenticates its demo-tree lookup', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    fetchMock.mockResolvedValueOnce(jsonResponse({ trees: [{ id: 'tree-resolved' }] }));

    await expect(resolveDemoAlias('demo')).resolves.toBe('tree-resolved');

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/trees?search=UI-02%20Rail%20Demo&limit=1');
    expect(authOf(init)).toBe('Bearer build-token-123');
  });

  it('resolveDemoAlias sends no Authorization header in dev-proxy mode', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ trees: [{ id: 'tree-resolved' }] }));
    await resolveDemoAlias('demo');
    expect(authOf(fetchMock.mock.calls[0][1])).toBeNull();
  });

  // ─── stores/yjsProvider.ts (tree feed + presence POSTs) ──────────────────

  it('the yjs tree feed streams over fetch with the token, not EventSource', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    fetchMock.mockImplementation(() => Promise.resolve(openFeed()));

    const doc = createTreeDoc('tree-sec-1');
    const provider = new SSESyncProvider(doc, { treeId: 'tree-sec-1' });
    provider.connect();

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/trees/tree-sec-1/events');
    expect(authOf(init)).toBe('Bearer build-token-123');
    expect(eventSourceSpy).not.toHaveBeenCalled();
    expect(init.credentials).toBe('include');

    provider.disconnect();
  });

  it('the yjs presence POSTs carry the token too', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    fetchMock.mockImplementation(() => Promise.resolve(openFeed()));

    const doc = createTreeDoc('tree-sec-2');
    const provider = new SSESyncProvider(doc, { treeId: 'tree-sec-2' });
    provider.setLocalPresence({
      userId: 'user-1',
      userName: 'Tester',
      avatarColor: '#fff',
      permission: 'editor',
      cursor: null,
      viewport: null,
      isActive: true,
    });

    await vi.waitFor(() => {
      const call = fetchMock.mock.calls.find(([url]) =>
        String(url).includes('/presence'),
      );
      expect(call).toBeDefined();
      expect(authOf(call?.[1])).toBe('Bearer build-token-123');
    });
  });

  // ─── structural guard (mirrors the acceptance grep) ─────────────────────

  it('no call site outside the auth plumbing keeps a raw fetch/EventSource', () => {
    // api.ts and sse.ts ARE the plumbing (authInit / authHeaders live there).
    const plumbing = ['lib/api.ts', 'lib/sse.ts'];
    const offenders: string[] = [];

    for (const [key, source] of Object.entries(SOURCES)) {
      const rel = key.replace(/^\//, '');
      if (rel.includes('__tests__') || rel.endsWith('.test.ts') || rel.endsWith('.test.tsx')) {
        continue;
      }
      if (plumbing.some((entry) => rel.endsWith(entry))) continue;

      const lines = source.split('\n');
      lines.forEach((line, index) => {
        if (!/(\bfetch\(|new EventSource)/.test(line)) return;
        // Multi-line calls: the auth helper sits on one of the next lines.
        const callWindow = lines.slice(index, index + 4).join('\n');
        if (/authInit\(|authHeaders\(|subscribeSse\(/.test(callWindow)) return;
        // The one deliberate exception: the public /health probe.
        if (line.includes("'/health'") && source.includes('isPublicPath')) return;
        offenders.push(`${rel}:${index + 1}: ${line.trim()}`);
      });
    }

    expect(offenders).toEqual([]);
  });

  it('the structural scan actually read the source tree', () => {
    // Guards the case above against a silent empty scan — a wrong glob would
    // otherwise make it vacuously pass.
    expect(Object.keys(SOURCES).length).toBeGreaterThan(100);
  });
});

describe('DEV-mode token source is still localStorage-aware', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse({ files: [], pagination: { count: 0 } })));
    vi.stubGlobal('fetch', fetchMock);
    window.localStorage.clear();
    vi.stubEnv('VITE_API_TOKEN', '');
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    window.localStorage.clear();
  });

  it('a pasted localStorage token authenticates a previously-open call site', async () => {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, 'stored-token-456');
    await listFiles();
    expect(new Headers(fetchMock.mock.calls[0][1].headers).get('Authorization')).toBe(
      'Bearer stored-token-456',
    );
  });
});
