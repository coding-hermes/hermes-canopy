/**
 * Hermes Canopy — API helper
 *
 * Shared fetch wrapper for all REST API calls. Authentication has two modes:
 *
 *  1. Dev (`npm run dev`) — the Vite dev server proxies `/api` to canopyd and
 *     auto-injects an HS256 dev JWT (see frontend/vite.config.ts). No token is
 *     resolved here, so NO Authorization header is sent and the proxied call is
 *     authenticated by the dev server — byte-identical to the pre-D1 behaviour.
 *  2. Production (a static `frontend/dist` build) — the Vite dev proxy does not
 *     exist, so the browser must carry the token itself. `resolveApiToken()`
 *     reads `VITE_API_TOKEN` (baked in at build time) and falls back to
 *     `window.localStorage['canopy.token']` (so an operator can paste a token
 *     into an already-built bundle). When a token resolves, every call in this
 *     file sends `Authorization: Bearer <token>`.
 *
 * There are no `/api/v1/auth/*` endpoints (multi-user auth is deferred
 * post-MVP), so a production token must be minted out-of-band. In production
 * prefer letting a same-origin reverse proxy inject the header behind real
 * authentication — see deploy/reference-proxy.py, deploy/nginx.canopy.conf,
 * deploy/Caddyfile, and README.md §"Authentication".
 */

const API_BASE = import.meta.env.VITE_API_BASE_URL ?? '/api/v1';

/** localStorage key holding an operator-supplied bearer token. */
export const TOKEN_STORAGE_KEY = 'canopy.token';

export function apiUrl(path: string): string {
  return `${API_BASE}${path}`;
}

/**
 * Non-empty, non-whitespace token, or null when the value counts as absent.
 * Both token sources treat "" and "   " as "not configured".
 */
function normalizeToken(raw: string | null | undefined): string | null {
  if (typeof raw !== 'string') return null;
  const trimmed = raw.trim();
  return trimmed.length > 0 ? trimmed : null;
}

/**
 * Resolve the bearer token for a non-dev-proxy deployment.
 *
 * Resolution order: build-time `VITE_API_TOKEN`, then
 * `window.localStorage['canopy.token']`. Returns null when neither holds a
 * non-blank value — the pure dev-proxy case.
 */
export function resolveApiToken(): string | null {
  const builtIn: unknown = import.meta.env.VITE_API_TOKEN;
  const fromBuild = normalizeToken(typeof builtIn === 'string' ? builtIn : null);
  if (fromBuild !== null) return fromBuild;

  const storage = typeof window !== 'undefined' ? window.localStorage : null;
  return normalizeToken(storage?.getItem(TOKEN_STORAGE_KEY) ?? null);
}

/**
 * THE choke point: every API call in this file builds its headers here.
 *
 * With a token → the caller's headers plus `Authorization: Bearer <token>`.
 * Without one → the caller's `extra` verbatim, so no `Authorization` key is
 * ever added in dev-proxy mode (returns undefined when `extra` was undefined,
 * which keeps the request init identical to the pre-D1 shape).
 */
export function authHeaders(extra?: HeadersInit): HeadersInit | undefined {
  const token = resolveApiToken();
  if (token === null) return extra;

  const headers = new Headers(extra);
  headers.set('Authorization', `Bearer ${token}`);
  return headers;
}

/**
 * Request init for one API call. When no token is configured and the caller
 * passed no headers, the caller's init is returned unchanged (undefined for a
 * bare GET) so the dev-proxy request is exactly what it was before.
 *
 * Exported for the call sites that cannot use the typed apiGet/apiPost helpers
 * (abortable reads, blob/stream responses, multipart uploads, plugin sandbox
 * calls). Every network call in frontend/src goes through this or
 * `authHeaders` — `lib/sse.ts` is the only other door, and it borrows
 * `authHeaders` too.
 */
export function authInit(init?: RequestInit): RequestInit | undefined {
  const headers = authHeaders(init?.headers);
  if (headers === undefined) return init;
  return { ...init, headers };
}

/** Generic fetch wrapper with JSON handling and error typing. */
export async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(apiUrl(path), authInit());
  if (!res.ok) {
    const body = await res.text();
    let msg: string;
    try {
      const parsed = JSON.parse(body);
      const e = parsed.error;
      msg = (typeof e === 'object' && e !== null ? e.message : e) ?? body;
    } catch {
      msg = body || `HTTP ${res.status}`;
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

export async function apiPost<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(apiUrl(path), authInit({
    method: 'POST',
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  }));
  if (!res.ok) {
    const text = await res.text();
    let msg: string;
    try {
      const parsed = JSON.parse(text);
      const e = parsed.error;
      msg = (typeof e === 'object' && e !== null ? e.message : e) ?? text;
    } catch {
      msg = text || `HTTP ${res.status}`;
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

export async function apiPatch<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(apiUrl(path), authInit({
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }));
  if (!res.ok) {
    const text = await res.text();
    let msg: string;
    try {
      const parsed = JSON.parse(text);
      const e = parsed.error;
      msg = (typeof e === 'object' && e !== null ? e.message : e) ?? text;
    } catch {
      msg = text || `HTTP ${res.status}`;
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

export async function apiPut<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(apiUrl(path), authInit({
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }));
  if (!res.ok) {
    const text = await res.text();
    let msg: string;
    try {
      const parsed = JSON.parse(text);
      const e = parsed.error;
      msg = (typeof e === 'object' && e !== null ? e.message : e) ?? text;
    } catch {
      msg = text || `HTTP ${res.status}`;
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

export async function apiDelete(path: string): Promise<void> {
  const res = await fetch(apiUrl(path), authInit({ method: 'DELETE' }));
  if (!res.ok) {
    const text = await res.text();
    let msg: string;
    try {
      const parsed = JSON.parse(text);
      const e = parsed.error;
      msg = (typeof e === 'object' && e !== null ? e.message : e) ?? text;
    } catch {
      msg = text || `HTTP ${res.status}`;
    }
    throw new Error(msg);
  }
}
