/**
 * Unit tests — api.ts bearer-token choke point (DF-HERMES-CANOPY-7)
 *
 * The dev proxy injects a JWT for `npm run dev`; a production static build has
 * no proxy, so api.ts must attach `Authorization: Bearer <token>` itself when
 * `VITE_API_TOKEN` (build time) or `localStorage['canopy.token']` holds one —
 * and must add NO Authorization key at all when neither does (the dev-proxy
 * request stays exactly as it was).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  apiGet,
  apiPost,
  apiPut,
  apiPatch,
  apiDelete,
  authHeaders,
  resolveApiToken,
  TOKEN_STORAGE_KEY,
} from '../api';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** Authorization header value of the recorded call, or null when absent. */
function authOf(init: RequestInit | undefined): string | null {
  if (!init || init.headers === undefined) return null;
  return new Headers(init.headers).get('Authorization');
}

describe('api.ts auth (DF-HERMES-CANOPY-7)', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    // Fresh Response per call — a Response body can only be read once.
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse({ ok: true })));
    vi.stubGlobal('fetch', fetchMock);
    window.localStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    window.localStorage.clear();
  });

  // ─── token present (VITE_API_TOKEN, build time) ──────────────────────────

  it('GET sends Authorization: Bearer <token> when VITE_API_TOKEN is baked in', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    await apiGet('/trees');
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/trees');
    expect(authOf(init)).toBe('Bearer build-token-123');
  });

  it('POST sends the bearer token and keeps Content-Type', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    await apiPost('/trees', { title: 't' });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/trees');
    expect(init.method).toBe('POST');
    expect(authOf(init)).toBe('Bearer build-token-123');
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json');
    expect(JSON.parse(init.body)).toEqual({ title: 't' });
  });

  it('PUT, PATCH and DELETE also carry the bearer token', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    await apiPut('/nodes/n1', { content: 'x' });
    await apiPatch('/nodes/n1', { content: 'y' });
    await apiDelete('/nodes/n1');
    expect(authOf(fetchMock.mock.calls[0][1])).toBe('Bearer build-token-123');
    expect(authOf(fetchMock.mock.calls[1][1])).toBe('Bearer build-token-123');
    expect(authOf(fetchMock.mock.calls[2][1])).toBe('Bearer build-token-123');
    expect(fetchMock.mock.calls[2][1].method).toBe('DELETE');
  });

  // ─── token absent (dev proxy) ───────────────────────────────────────────

  it('GET without any token adds no Authorization key and no headers init', async () => {
    await apiGet('/trees');
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/trees');
    // Dev-proxy shape is unchanged: no init at all for a bare GET.
    expect(init).toBeUndefined();
    expect(authOf(init)).toBeNull();
  });

  it('POST without any token adds no Authorization key', async () => {
    await apiPost('/trees', { title: 't' });
    const [, init] = fetchMock.mock.calls[0];
    expect(authOf(init)).toBeNull();
    expect(Object.keys(init.headers)).toEqual(['Content-Type']);
  });

  it('POST without a body and without a token sends no headers key at all', async () => {
    await apiPost('/trees');
    const [, init] = fetchMock.mock.calls[0];
    expect(init.headers).toBeUndefined();
    expect(authOf(init)).toBeNull();
  });

  it('whitespace-only token counts as absent in both sources', async () => {
    vi.stubEnv('VITE_API_TOKEN', '   ');
    window.localStorage.setItem(TOKEN_STORAGE_KEY, '\t\n ');
    expect(resolveApiToken()).toBeNull();
    await apiGet('/trees');
    expect(authOf(fetchMock.mock.calls[0][1])).toBeNull();
  });

  it('empty-string token counts as absent', async () => {
    vi.stubEnv('VITE_API_TOKEN', '');
    expect(resolveApiToken()).toBeNull();
  });

  // ─── localStorage fallback ──────────────────────────────────────────────

  it('falls back to localStorage["canopy.token"] when no build-time token', async () => {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, 'stored-token-456');
    await apiGet('/trees');
    expect(authOf(fetchMock.mock.calls[0][1])).toBe('Bearer stored-token-456');
  });

  it('falls back to localStorage when VITE_API_TOKEN is whitespace-only', async () => {
    vi.stubEnv('VITE_API_TOKEN', '  ');
    window.localStorage.setItem(TOKEN_STORAGE_KEY, 'stored-token-456');
    await apiGet('/trees');
    expect(authOf(fetchMock.mock.calls[0][1])).toBe('Bearer stored-token-456');
  });

  it('build-time token wins over localStorage', async () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    window.localStorage.setItem(TOKEN_STORAGE_KEY, 'stored-token-456');
    await apiGet('/trees');
    expect(authOf(fetchMock.mock.calls[0][1])).toBe('Bearer build-token-123');
  });

  it('trims surrounding whitespace off a resolved token', async () => {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, '  padded-token  ');
    expect(resolveApiToken()).toBe('padded-token');
    await apiGet('/trees');
    expect(authOf(fetchMock.mock.calls[0][1])).toBe('Bearer padded-token');
  });

  // ─── the choke point itself ─────────────────────────────────────────────

  it('authHeaders returns the caller headers verbatim when no token resolves', () => {
    const extra = { Accept: 'application/json' };
    expect(authHeaders(extra)).toBe(extra);
    expect(authHeaders()).toBeUndefined();
  });

  it('authHeaders preserves caller headers alongside Authorization', () => {
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
    const headers = new Headers(authHeaders({ 'X-Request-Id': 'r1' }));
    expect(headers.get('X-Request-Id')).toBe('r1');
    expect(headers.get('Authorization')).toBe('Bearer build-token-123');
  });
});
