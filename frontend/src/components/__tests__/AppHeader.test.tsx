/**
 * Component tests — AppHeader backend status pill (WIRE-005)
 *
 * Pins the wiring of the live backend health indicator: a green dot + service
 * label when GET /health returns {"status":"ok","service":"canopyd"}, and a
 * danger dot + "unreachable" label when the endpoint errors or returns a
 * non-ok payload.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import AppHeader from '../AppHeader.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// ─── Fixtures ──────────────────────────────────────────────────────────

function okResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    text: () => Promise.resolve(JSON.stringify(body)),
    json: () => Promise.resolve(body),
  } as Response;
}

function errorResponse(status: number): Response {
  return {
    ok: false,
    status,
    text: () => Promise.resolve(''),
    json: () => Promise.resolve({}),
  } as Response;
}

function defaultFetch(url: string): Promise<Response> {
  if (url === '/health') {
    return Promise.resolve(okResponse({ status: 'ok', service: 'canopyd' }));
  }
  if (url.startsWith('/api/v1/trees')) {
    return Promise.resolve(okResponse({ trees: [] }));
  }
  if (url.startsWith('/api/v1/topics')) {
    return Promise.resolve(okResponse({ topics: [] }));
  }
  return Promise.reject(new Error(`unexpected fetch: ${url}`));
}

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;

function mount() {
  act(() => {
    root.render(createElement(MemoryRouter, null, createElement(AppHeader)));
  });
}

async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

function q(selector: string): HTMLElement | null {
  return container.querySelector(selector);
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  fetchMock = vi.fn(defaultFetch);
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ─── Backend status ────────────────────────────────────────────────────

describe('AppHeader — backend status', () => {
  it('shows a green dot and the service name when /health is healthy', async () => {
    mount();
    await settle();

    const pill = q('[data-testid="backend-status"]');
    expect(pill).not.toBeNull();
    expect(pill?.getAttribute('title')).toBe('Backend is healthy');
    expect(pill?.getAttribute('aria-label')).toBe('Backend is healthy');
    expect(pill?.textContent).toContain('Backend: canopyd');

    const dot = pill?.querySelector('span[aria-hidden="true"]');
    expect(dot?.className).toContain('bg-status-success');
    expect(dot?.className).not.toContain('bg-status-danger');

    expect(fetchMock).toHaveBeenCalledWith('/health');
  });

  it('shows a danger dot and "unreachable" when /health returns non-ok', async () => {
    fetchMock.mockImplementation((url: string) => {
      if (url === '/health') {
        return Promise.resolve(errorResponse(503));
      }
      return defaultFetch(url);
    });

    mount();
    await settle();

    const pill = q('[data-testid="backend-status"]');
    expect(pill?.textContent).toContain('Backend: unreachable');
    expect(pill?.getAttribute('title')).toBe('Backend is unreachable');
    expect(pill?.getAttribute('aria-label')).toBe('Backend is unreachable');

    const dot = pill?.querySelector('span[aria-hidden="true"]');
    expect(dot?.className).toContain('bg-status-danger');
    expect(dot?.className).not.toContain('bg-status-success');
  });

  it('shows "unreachable" when /health fetch rejects', async () => {
    fetchMock.mockImplementation((url: string) => {
      if (url === '/health') {
        return Promise.reject(new Error('network down'));
      }
      return defaultFetch(url);
    });

    mount();
    await settle();

    const pill = q('[data-testid="backend-status"]');
    expect(pill?.textContent).toContain('Backend: unreachable');
  });

  it('treats an unexpected status field as unhealthy', async () => {
    fetchMock.mockImplementation((url: string) => {
      if (url === '/health') {
        return Promise.resolve(okResponse({ status: 'degraded' }));
      }
      return defaultFetch(url);
    });

    mount();
    await settle();

    const pill = q('[data-testid="backend-status"]');
    expect(pill?.textContent).toContain('Backend: unreachable');
  });
});
