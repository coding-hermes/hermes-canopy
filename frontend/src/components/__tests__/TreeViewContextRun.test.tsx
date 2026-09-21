/**
 * Component tests — TreeView's node-scoped run path (GAP-084)
 *
 * The row's typed criterion is an end-to-end one: with a node selected,
 * submitting through the composer's context-run affordance must issue a
 * start-run request that CARRIES that node id. This file drives the real
 * `TreeView` → real `MessageComposer` → real `useGatewayRuns` → real
 * `gatewayApi` chain against a stubbed `fetch`, so the assertion is made on
 * the request that leaves the UI, not on a mock of the layer under test.
 *
 * TreeView's neighbours are mocked (canvas, Yjs stores, presence) because
 * none of them are under test here and all of them need a backend, a
 * WebSocket/SSE provider or a browser canvas to exist at all. Everything
 * the feature touches — composer, hook, API client, indicator — is real.
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

vi.mock('../../stores/useYjsTree', () => ({
  useYjsTree: () => ({
    nodes: [],
    edges: [],
    treeTitle: 'GAP-084 Tree',
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

const NODE_A = '019fb0c2-cab0-70c5-a477-fa10f136e000';

/**
 * The canvas is the only source of a selection in TreeView; this stand-in
 * exposes that same `onSelectionChange` contract as a clickable node.
 */
vi.mock('../TreeCanvas.tsx', () => ({
  default: ({ onSelectionChange }: { onSelectionChange?: (id: string) => void }) =>
    createElement(
      'button',
      {
        'data-testid': 'mock-select-node',
        onClick: () => onSelectionChange?.('019fb0c2-cab0-70c5-a477-fa10f136e000'),
      },
      'select node',
    ),
}));

/** The BEFORE-side panel fetches on mount; it is not part of this test. */
vi.mock('../ContextManifestPanel.tsx', () => ({ default: () => null }));
vi.mock('../NavigationBar.tsx', () => ({ default: () => null }));
vi.mock('../PresenceBar.tsx', () => ({ default: () => null }));
vi.mock('../CollaborativeCursors.tsx', () => ({ default: () => null }));
vi.mock('../ShareDialog.tsx', () => ({ default: () => null }));
vi.mock('../ReferenceInspectorPanel.tsx', () => ({ default: () => null }));

// ─── Fixtures ──────────────────────────────────────────────────────────

const TREE_ID = '9a7f97f3-0000-7000-8000-000000000001';
const RUN_ID = 'run_ctx_1';

/** The run record canopyd returns once the context-aware run is started. */
const CONTEXT_RUN = {
  run_id: RUN_ID,
  session_id: '',
  message: 'summarise this thread',
  model: 'hermes',
  status: 'running',
  created_at: '2026-09-17T10:00:00Z',
  events: [],
  source_node_id: NODE_A,
  token_budget: 8000,
  context_tokens: 1240,
  manifest: {
    requestId: 'req-1',
    nodeId: NODE_A,
    compiledAt: '2026-09-17T10:00:00Z',
    tokenBudget: 8000,
    tokensUsed: 1240,
    ancestry: [
      {
        id: NODE_A,
        kind: 'node',
        title: 'Welcome to Hermes Canopy',
        tokenCount: 412,
        truncated: false,
      },
    ],
    references: null,
    cards: null,
    omittedCount: 0,
    omittedReason: '',
    truncationMarkers: null,
    warnings: null,
  },
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;
const fetchMock = vi.fn();
/** Flipped by the POST branch: the refresh after start sees the new run. */
let contextRunStarted = false;
/** Requests recorded as start-run POSTs, in order. */
let startRunPosts: Array<Record<string, unknown>> = [];

function routeFetch(url: string, init?: RequestInit): Response {
  const method = init?.method ?? 'GET';

  if (url.startsWith('/api/v1/gateway/status')) {
    return jsonResponse({
      connected: true,
      base_url: 'http://127.0.0.1:8642',
      run_count: 1,
      active_runs: 1,
    });
  }

  if (url === '/api/v1/gateway/runs') {
    if (method === 'POST') {
      startRunPosts.push(
        JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>,
      );
      contextRunStarted = true;
      return jsonResponse({ run_id: RUN_ID, status: 'started' });
    }
    return jsonResponse({ runs: contextRunStarted ? [CONTEXT_RUN] : [] });
  }

  if (url.startsWith('/api/v1/context/')) {
    // GAP-096: the audit dialog previews the compile before Send.
    return jsonResponse({ content: 'Welcome to Hermes Canopy', manifest: CONTEXT_RUN.manifest });
  }

  if (url === `/api/v1/trees/${TREE_ID}`) {
    return jsonResponse({ title: 'GAP-084 Tree' });
  }
  if (url === `/api/v1/trees/${TREE_ID}/nodes`) {
    return jsonResponse({ nodes: [] });
  }

  return jsonResponse({ error: { message: `unexpected ${method} ${url}` } }, 404);
}

function renderTreeView() {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => {
    root.render(
      createElement(
        MemoryRouter,
        { initialEntries: [`/tree/${TREE_ID}`] },
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

function setComposerValue(textarea: HTMLTextAreaElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLTextAreaElement.prototype,
    'value',
  )!.set!;
  setter.call(textarea, value);
  textarea.dispatchEvent(new Event('input', { bubbles: true }));
}

function composerTextarea(): HTMLTextAreaElement {
  return container.querySelector(
    '[data-testid="composer-bar"] textarea',
  ) as HTMLTextAreaElement;
}

function contextRunButton(): HTMLButtonElement {
  return container.querySelector(
    '[data-testid="composer-run-with-context"]',
  ) as HTMLButtonElement;
}

beforeEach(() => {
  fetchMock.mockReset();
  fetchMock.mockImplementation((url: string, init?: RequestInit) =>
    Promise.resolve(routeFetch(String(url), init)),
  );
  vi.stubGlobal('fetch', fetchMock);
  contextRunStarted = false;
  startRunPosts = [];
});

afterEach(() => {
  act(() => root.unmount());
  container?.remove();
  vi.unstubAllGlobals();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('TreeView — node-scoped gateway runs (GAP-084)', () => {
  it('submitting through the composer’s run path POSTs the selected node id', async () => {
    renderTreeView();
    await flushPromises();

    // The composer is present, and the run path is gated until a node is
    // selected — TreeView is the only page that can supply one.
    expect(contextRunButton()).not.toBeNull();
    expect(contextRunButton().disabled).toBe(true);
    expect(contextRunButton().getAttribute('title')).toBe(
      'Select a node to run with its compiled context',
    );

    act(() => {
      (
        container.querySelector('[data-testid="mock-select-node"]') as HTMLButtonElement
      ).click();
    });
    await flushPromises();

    // The node gate is open now; the local text gate (the same one Send
    // has) is not — and the title changes to say which one is holding.
    expect(contextRunButton().disabled).toBe(true);
    expect(contextRunButton().getAttribute('title')).toBe(
      'Type a message to run with context',
    );

    act(() => setComposerValue(composerTextarea(), 'summarise this thread'));
    await flushPromises();
    expect(contextRunButton().disabled).toBe(false);

    await act(async () => {
      contextRunButton().click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();

    // GAP-096: the click opens the audit dialog (a preview GET, no POST
    // yet). Confirming Send is what issues the context-aware start-run.
    const send = container.querySelector(
      '[data-testid="context-audit-send"]',
    ) as HTMLButtonElement;
    expect(send).not.toBeNull();
    expect(startRunPosts).toHaveLength(0);

    await act(async () => {
      send.click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();

    // The criterion: the request that leaves the UI carries node_id —
    // now alongside the budget the previewed manifest was computed with.
    expect(startRunPosts).toHaveLength(1);
    expect(startRunPosts[0]).toEqual({
      message: 'summarise this thread',
      node_id: NODE_A,
      token_budget: 8000,
    });

    const url = String(fetchMock.mock.calls[0][0]);
    expect(url).toBe('/api/v1/gateway/status');
    expect(
      fetchMock.mock.calls.some(
        ([u, init]) =>
          String(u) === '/api/v1/gateway/runs' &&
          (init as RequestInit | undefined)?.method === 'POST',
      ),
    ).toBe(true);
  });

  it('never starts a context run with no node selected', async () => {
    renderTreeView();
    await flushPromises();

    act(() => setComposerValue(composerTextarea(), 'no node selected'));
    await flushPromises();

    // Disabled, explained, and — the point — no request of any kind.
    expect(contextRunButton().disabled).toBe(true);
    expect(contextRunButton().getAttribute('title')).toBe(
      'Select a node to run with its compiled context',
    );
    act(() => contextRunButton().click());
    await flushPromises();

    expect(startRunPosts).toHaveLength(0);
    expect(composerTextarea().value).toBe('no node selected');
  });

  it('surfaces the started run’s provenance once the registry has it', async () => {
    renderTreeView();
    await flushPromises();

    // Nothing to report before a run is started.
    expect(
      container.querySelector('[data-testid="context-run-indicator"]'),
    ).toBeNull();

    act(() => {
      (
        container.querySelector('[data-testid="mock-select-node"]') as HTMLButtonElement
      ).click();
    });
    await flushPromises();
    act(() => setComposerValue(composerTextarea(), 'summarise this thread'));
    await flushPromises();

    await act(async () => {
      contextRunButton().click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();

    // GAP-096: confirm Send in the audit dialog before the run exists.
    await act(async () => {
      (
        container.querySelector(
          '[data-testid="context-audit-send"]',
        ) as HTMLButtonElement
      ).click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();

    const indicator = container.querySelector(
      '[data-testid="context-run-indicator"]',
    );
    expect(indicator).not.toBeNull();
    expect(
      container.querySelector('[data-testid="context-run-window"]')?.textContent,
    ).toBe('Context window: 1,240 / 8,000 tokens');
    expect(
      container.querySelector('[data-testid="context-run-source"]')?.textContent,
    ).toBe('019fb0c2…e000');

    // The composer kept its text until the POST resolved, then cleared it.
    expect(composerTextarea().value).toBe('');
  });

  it('does not render an indicator for the raw-text runs in the registry', async () => {
    renderTreeView();
    await flushPromises();

    act(() => {
      (
        container.querySelector('[data-testid="mock-select-node"]') as HTMLButtonElement
      ).click();
    });
    await flushPromises();

    // Send still exists and is untouched by the context path.
    expect(
      container.querySelector('[data-testid="composer-run-with-context"]'),
    ).not.toBeNull();
    expect(
      container.querySelector('[data-testid="composer-bar"]'),
    ).not.toBeNull();
    expect(
      container.querySelector('[data-testid="context-run-indicator"]'),
    ).toBeNull();
  });
});
