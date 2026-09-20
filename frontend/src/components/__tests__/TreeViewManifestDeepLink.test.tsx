/**
 * Component tests — TreeView manifest deep-link (GAP-094)
 *
 * The dashboard's "Recent trees" resume entry links to
 * `/tree/<id>?manifest=1`. The promise is that arriving from a resume click
 * surfaces the context manifest, so this file drives the real TreeView with
 * the real ContextManifestPanel and asserts the panel is on screen after
 * the deep-link — and that it is NOT selected when the param is absent
 * (otherwise the test would pass on TreeView's ordinary behaviour).
 *
 * TreeView's neighbours that need a backend, a provider or a canvas are
 * mocked; everything the feature touches (param read, selection, panel) is
 * real.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import TreeView from '../TreeView.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Neighbour mocks ───────────────────────────────────────────────────

vi.mock('../../lib/activeTree', () => ({
  resolveDemoAliasSync: (id: string) => id,
  storeTreeId: () => {},
}));

vi.mock('../../stores/treeStore', () => ({
  createTreeDoc: () => ({
    ydoc: {
      transact: (fn: () => void) => fn(),
      destroy: () => {},
    },
    meta: { get: () => undefined, set: () => {} },
  }),
  bindIndexedDB: () => ({ destroy: () => {} }),
  seedDemoTree: () => {},
  mergeBackendNodes: () => 0,
  mergeBackendEdges: () => 0,
}));

vi.mock('../../stores/yjsProvider', () => ({
  SSESyncProvider: class {
    connect() {}
    disconnect() {}
  },
}));

const NODE_OLD = '019fb0c2-cab0-70c5-a477-fa10f136e001';
const NODE_NEW = '019fb0c2-cab0-70c5-a477-fa10f136e002';

/**
 * Two hydrated nodes with distinct createdAt values, so "the newest node"
 * is unambiguous. Ordered OLD-first on purpose: the effect must pick by
 * timestamp, not by array position.
 */
vi.mock('../../stores/useYjsTree', () => ({
  useYjsTree: () => ({
    nodes: [
      { id: '019fb0c2-cab0-70c5-a477-fa10f136e001', data: { createdAt: '2026-09-17T09:00:00Z' } },
      { id: '019fb0c2-cab0-70c5-a477-fa10f136e002', data: { createdAt: '2026-09-17T11:00:00Z' } },
    ],
    edges: [],
    treeTitle: 'GAP-094 Tree',
    isReady: true,
    multiParentNodes: new Set<string>(),
    refresh: () => {},
  }),
}));

vi.mock('../../hooks/usePresence', () => ({
  usePresence: () => ({
    remotePresence: new Map(),
    userId: 'user-1',
    permission: 'editor',
  }),
}));

vi.mock('../TreeCanvas.tsx', () => ({ default: () => null }));
vi.mock('../NavigationBar.tsx', () => ({ default: () => null }));
vi.mock('../PresenceBar.tsx', () => ({ default: () => null }));
vi.mock('../CollaborativeCursors.tsx', () => ({ default: () => null }));
vi.mock('../ShareDialog.tsx', () => ({ default: () => null }));
vi.mock('../ReferenceInspectorPanel.tsx', () => ({ default: () => null }));

// ─── Harness ───────────────────────────────────────────────────────────

const TREE_ID = '9a7f97f3-0000-7000-8000-000000000094';

let container: HTMLDivElement;
let root: Root;
const fetchMock = vi.fn();
/** Node ids the context compiler was asked about, in order. */
let manifestRequests: string[] = [];

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function routeFetch(url: string): Response {
  if (url.startsWith('/api/v1/gateway/models')) {
    return jsonResponse({ models: [] });
  }
  if (url.startsWith('/api/v1/context/')) {
    const nodeId = decodeURIComponent(url.split('/api/v1/context/')[1].split('?')[0]);
    manifestRequests.push(nodeId);
    return jsonResponse({
      requestId: 'req-1',
      nodeId,
      compiledAt: '2026-09-17T11:00:00Z',
      tokenBudget: 8000,
      tokensUsed: 1240,
      ancestry: [],
      references: null,
      cards: null,
      omittedCount: 0,
      omittedReason: '',
      truncationMarkers: null,
      warnings: null,
    });
  }
  if (url.startsWith(`/api/v1/trees/${TREE_ID}/nodes`)) {
    return jsonResponse({ nodes: [] });
  }
  if (url.startsWith(`/api/v1/trees/${TREE_ID}`)) {
    return jsonResponse({ title: 'GAP-094 Tree' });
  }
  if (url.startsWith('/api/v1/gateway/runs')) {
    return jsonResponse({ runs: [] });
  }
  if (url.startsWith('/api/v1/gateway/status')) {
    return jsonResponse({ connected: true, base_url: 'x', run_count: 0, active_runs: 0 });
  }
  return jsonResponse({ error: { message: `unexpected ${url}` } }, 404);
}

function renderTreeView(path: string) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => {
    root.render(
      createElement(
        MemoryRouter,
        { initialEntries: [path] },
        createElement(
          Routes,
          null,
          createElement(Route, {
            path: '/tree/:treeId',
            element: createElement(TreeView),
          }),
        ),
      ),
    );
  });
}

async function flushPromises(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  fetchMock.mockReset();
  fetchMock.mockImplementation((url: string) =>
    Promise.resolve(routeFetch(String(url))),
  );
  vi.stubGlobal('fetch', fetchMock);
  manifestRequests = [];
});

afterEach(() => {
  act(() => root.unmount());
  container?.remove();
  vi.unstubAllGlobals();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('TreeView — manifest deep-link (GAP-094)', () => {
  it('opens the context manifest for the newest node when ?manifest=1 is present', async () => {
    renderTreeView(`/tree/${TREE_ID}?manifest=1`);
    await flushPromises();

    expect(container.querySelector('[data-testid="context-manifest-panel"]')).not.toBeNull();
    // The panel is for the NEWEST node, chosen by createdAt rather than by
    // array position (the mock lists the older node first).
    expect(manifestRequests).toEqual([NODE_NEW]);
  });

  it('leaves the manifest closed without the param', async () => {
    renderTreeView(`/tree/${TREE_ID}`);
    await flushPromises();

    // No selection → ContextManifestPanel renders nothing, so the resume
    // deep-link is doing real work rather than describing default state.
    expect(container.querySelector('[data-testid="context-manifest-panel"]')).toBeNull();
    expect(manifestRequests).toEqual([]);
  });

  it('treats any value other than 1 as absent', async () => {
    renderTreeView(`/tree/${TREE_ID}?manifest=true`);
    await flushPromises();

    expect(container.querySelector('[data-testid="context-manifest-panel"]')).toBeNull();
    expect(manifestRequests).toEqual([]);
  });

  it('honors an explicit ?node= deep-link over the manifest default', async () => {
    // Recovery path: the dashboard's resume link and a node deep-link can
    // both be present (a reader following a link that already carried a
    // node). The explicit node wins — the manifest effect must not stomp
    // the chosen selection.
    renderTreeView(`/tree/${TREE_ID}?node=${NODE_OLD}&manifest=1`);
    await flushPromises();

    expect(container.querySelector('[data-testid="context-manifest-panel"]')).not.toBeNull();
    expect(manifestRequests).toEqual([NODE_OLD]);
  });
});
