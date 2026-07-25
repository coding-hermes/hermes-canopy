/**
 * Hermes Canopy — API helper
 *
 * Shared fetch wrapper for all REST API calls. The Vite dev server
 * auto-injects a JWT into /api requests (BUG-003), so no explicit
 * Authorization header is needed.
 */

const API_BASE = import.meta.env.VITE_API_BASE_URL ?? '/api/v1';

export function apiUrl(path: string): string {
  return `${API_BASE}${path}`;
}

/** Generic fetch wrapper with JSON handling and error typing. */
export async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(apiUrl(path));
  if (!res.ok) {
    const body = await res.text();
    let msg: string;
    try {
      msg = JSON.parse(body).error ?? body;
    } catch {
      msg = body || `HTTP ${res.status}`;
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

export async function apiPost<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(apiUrl(path), {
    method: 'POST',
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const text = await res.text();
    let msg: string;
    try {
      msg = JSON.parse(text).error ?? text;
    } catch {
      msg = text || `HTTP ${res.status}`;
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

export async function apiPatch<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(apiUrl(path), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text();
    let msg: string;
    try {
      msg = JSON.parse(text).error ?? text;
    } catch {
      msg = text || `HTTP ${res.status}`;
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

export async function apiDelete(path: string): Promise<void> {
  const res = await fetch(apiUrl(path), { method: 'DELETE' });
  if (!res.ok) {
    const text = await res.text();
    let msg: string;
    try {
      msg = JSON.parse(text).error ?? text;
    } catch {
      msg = text || `HTTP ${res.status}`;
    }
    throw new Error(msg);
  }
}
