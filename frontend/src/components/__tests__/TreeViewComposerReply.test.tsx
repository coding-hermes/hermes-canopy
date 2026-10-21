/**
 * Component tests — composer Send files under the selected node
 * (DF-HERMES-CANOPY-93)
 *
 * The row's typed criterion is end-to-end: selecting a node on the canvas
 * and pressing Send must issue the node-create POST with `parent_id` =
 * that node's id, the header counts must move to N+1 nodes / +1 edge, and
 * with nothing selected Send must still create a root (`parent_id: null`).
 * This file drives the REAL `TreeView` → real `MessageComposer` →
 * `apiPost` chain against a stubbed `fetch`, so every assertion is made on
 * the request that leaves the UI and the header the user sees, not on a
 * mock of the layer under test.
 *
 * TreeView's neighbours are mocked (canvas, Yjs stores, presence) because
 * none of them are under test here and all of them need a backend, an
 * SSE provider or a browser canvas to exist at all. Everything the row
 * touches — selection wiring, composer chip, POST body, header counts,
 * replica merge — is real.
 *
 * ch:trace row=DF-HERMES-CANOPY-93 spec=README.md#quick-start wave=hermes-canopy-foreman-2026-10-21-06-04-00#DF-HERMES-CANOPY-93 test=frontend/src/components/__tests__/TreeViewComposerReply.test.tsx::selection+Send+POSTs+parent_id=selected+node+id doc=README.md prompt=/tmp/hermes-canopy-foreman-2026-10-21-06-04-00-DF-HERMES-CANOPY-93-brief.txt evidence=frontend/src/components/__tests__/TreeViewComposerReply.test.tsx witness=none:component-harness-stubs-fetch-no-live-browser-this-tick verdict=none:judge-verdict-lives-on-board-row-DF-HERMES-CANOPY-93 commit=b8b422ed1199cde6b65df009735aa78954f483b1 memory=none:duckbrain-write-not-verified-this-tick
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

/**
 * Recording spies over the REAL merge functions: the created node and its
 * persisted reply edge must actually reach the replica (the header counts
 * derive from it). `createTreeDoc` stays the REAL one — the replica is a
 * real Yjs doc so mergeBackendNodes/Edges and the header snapshot run the
 * production code paths; only IndexedDB persistence is stubbed out.
 */
const mergedNodes: unknown[][] = [];
const mergedEdges: unknown[][] = [];

vi.mock('../../stores/treeStore', async () => {
  const actual = await vi.importActual<Record<string, unknown>>(
    '../../stores/treeStore',
  );
  return {
    ...actual,
    bindIndexedDB: () => ({ destroy: () => {} }),
    seedDemoTree: () => {},
    mergeBackendNodes: (...args: unknown[]) => {
      if (((args[1] as unknown[]) ?? []).length > 0) mergedNodes.push(args);
      return (
        actual.mergeBackendNodes as (...a: unknown[]) => number
      )(...args);
    },
    mergeBackendEdges: (...args: unknown[]) => {
      if (((args[1] as unknown[]) ?? []).length > 0) mergedEdges.push(args);
      return (
        actual.mergeBackendEdges as (...a: unknown[]) => number
      )(...args);
    },
  };
});

vi.mock('../../stores/yjsProvider', () => ({
  SSESyncProvider: class {
    connect() {}
    disconnect() {}
  },
}));

vi.mock('../../hooks/usePresence', () => ({
  usePresence: () => ({
    remotePresence: new Map(),
    userId: 'user-1',
    permission: 'editor',
  }),
}));

const NODE_A = '019fb0c2-cab0-70c5-a477-fa10f136e00a';
const CREATED_ID = '019fb0c2-cab0-70c5-a477-fa10f136efff';

/**
 * The canvas stand-in exposes TreeView's two reply paths the way the real
 * TreeCanvas exposes them: `onSelectionChange` (click a node / the empty
 * canvas) and `onCreateReply` (the ghost "Add reply" slot, UI-04).
 */
let canvasSelection: string | null = null;
vi.mock('../TreeCanvas.tsx', () => ({
  default: ({
    onSelectionChange,
    onCreateReply,
  }: {
    onSelectionChange?: (id: string | null) => void;
    onCreateReply?: (parentId: string) => void;
  }) =>
    createElement(
      'div',
      { 'data-testid': 'mock-canvas' },
      createElement(
        'button',
        {
          'data-testid': 'mock-select-node',
          onClick: () => {
            canvasSelection = NODE_A;
            onSelectionChange?.(NODE_A);
          },
        },
        'select node',
      ),
      createElement(
        'button',
        {
          'data-testid': 'mock-deselect',
          onClick: () => {
            canvasSelection = null;
            onSelectionChange?.(null);
          },
        },
        'deselect',
      ),
      createElement(
        'button',
        {
          'data-testid': 'mock-ghost-reply',
          onClick: () => {
            canvasSelection = NODE_A;
            onSelectionChange?.(NODE_A);
            onCreateReply?.(NODE_A);
          },
        },
        'add reply',
      ),
    ),
}));

vi.mock('../NavigationBar.tsx', () => ({ default: () => null }));
vi.mock('../PresenceBar.tsx', () => ({ default: () => null }));
vi.mock('../CollaborativeCursors.tsx', () => ({ default: () => null }));
vi.mock('../ShareDialog.tsx', () => ({ default: () => null }));
vi.mock('../ReferenceInspectorPanel.tsx', () => ({ default: () => null }));
vi.mock('../ContextManifestPanel.tsx', () => ({ default: () => null }));
vi.mock('../ContextRunIndicator.tsx', () => ({ default: () => null }));
vi.mock('../ContextAuditDialog.tsx', () => ({ default: () => null }));
vi.mock('../RelatedPanel.tsx', () => ({ default: () => null }));

// ─── Fixtures ──────────────────────────────────────────────────────────

const TREE_ID = '9a7f97f3-0000-7000-8000-000000000093';

/** Nodes served by `GET /trees/{id}/nodes` (flat camelCase API shape). */
const REPLICA_NODES = [
  {
    id: NODE_A,
    treeId: TREE_ID,
    parentId: null,
    content: 'first message',
    nodeType: 'message',
    metadata: 'e30=',
    createdAt: '2026-10-21T09:00:00Z',
  },
];

/** The 201 envelope `POST /trees/{id}/nodes` answers (CreateNodeResult). */
function createEnvelope() {
  return {
    node: {
      id: CREATED_ID,
      treeId: TREE_ID,
      parentId: canvasSelection ?? null,
      content: 'from composer',
      nodeType: 'message',
      metadata: 'e30=',
      createdAt: '2026-10-21T10:00:00Z',
    },
    edge: {
      id: 'edge-created-1',
      source_id: NODE_A,
      target_id: CREATED_ID,
      edge_type: 'reply',
      metadata: {},
    },
  };
}

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
/** Requests recorded as node-create POSTs, in order. */
let createPosts: Array<Record<string, unknown>> = [];

function routeFetch(url: string, init?: RequestInit): Response {
  const method = init?.method ?? 'GET';

  if (
    method === 'POST' &&
    url === `/api/v1/trees/${TREE_ID}/nodes`
  ) {
    createPosts.push(JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>);
    return jsonResponse(createEnvelope(), 201);
  }

  if (url.startsWith('/api/v1/gateway/status')) {
    return jsonResponse({ connected: false, run_count: 0, active_runs: 0 });
  }
  if (url.startsWith('/api/v1/gateway/runs')) {
    return jsonResponse({ runs: [] });
  }
  if (url.startsWith('/api/v1/gateway/models')) {
    return jsonResponse({ models: [] });
  }
  if (url === `/api/v1/trees/${TREE_ID}`) {
    return jsonResponse({ title: 'DF-93 Tree' });
  }
  if (url === `/api/v1/trees/${TREE_ID}/nodes`) {
    return jsonResponse({ nodes: REPLICA_NODES });
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

function sendButton(): HTMLButtonElement {
  return container.querySelector(
    '[data-testid="composer-bar"] button[aria-label="Send message"]',
  ) as HTMLButtonElement;
}

async function typeAndSend(): Promise<void> {
  act(() => setComposerValue(composerTextarea(), 'from composer'));
  await flushPromises();
  await act(async () => {
    sendButton().click();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
  await flushPromises();
}

beforeEach(() => {
  fetchMock.mockReset();
  fetchMock.mockImplementation((url: string, init?: RequestInit) =>
    Promise.resolve(routeFetch(String(url), init)),
  );
  vi.stubGlobal('fetch', fetchMock);
  canvasSelection = null;
  createPosts = [];
  mergedNodes.length = 0;
  mergedEdges.length = 0;
});

afterEach(() => {
  act(() => root.unmount());
  container?.remove();
  vi.unstubAllGlobals();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('TreeView composer — Send files under the selected node (DF-HERMES-CANOPY-93)', () => {
  it('selection + Send POSTs parent_id = selected node id, and the header shows N+1 nodes / +1 edge', async () => {
    renderTreeView();
    await flushPromises();

    // No selection yet: the two paths are distinguishable — no chip, plain
    // Send, root placeholder.
    expect(
      container.querySelector('[data-testid="composer-reply-target"]'),
    ).toBeNull();
    expect(
      container.querySelector('[data-testid="composer-send-label"]')?.textContent,
    ).toBe('Send');

    act(() => {
      (
        container.querySelector('[data-testid="mock-select-node"]') as HTMLButtonElement
      ).click();
    });
    await flushPromises();

    // AC4: the reply path is visible — chip with the target + a labelled
    // Send reply affordance.
    const chip = container.querySelector('[data-testid="composer-reply-target"]');
    expect(chip).not.toBeNull();
    expect(chip?.textContent).toContain('Replying to selected');
    expect(chip?.textContent).toContain('first message');
    expect(
      container.querySelector('[data-testid="composer-send-label"]')?.textContent,
    ).toBe('Send reply');

    await typeAndSend();

    // AC1: the request that left the UI carries the selected node id.
    expect(createPosts).toHaveLength(1);
    expect(createPosts[0].parent_id).toBe(NODE_A);
    expect(createPosts[0].content).toBe('from composer');

    // AC2: the created node + the persisted reply edge reached the
    // replica, so the header count moved to N+1 / +1 edge.
    expect(mergedNodes.some(([, nodes]) => (nodes as { id: string }[]).some((n) => n.id === CREATED_ID))).toBe(true);
    expect(mergedEdges.length).toBe(1);
    expect(
      container.querySelector('[data-testid="tree-counts"]')?.textContent,
    ).toBe('2 nodes · 1 edge');
  });

  it('no selection + Send still creates a root (parent_id null), no reply chrome', async () => {
    renderTreeView();
    await flushPromises();

    expect(
      container.querySelector('[data-testid="composer-reply-target"]'),
    ).toBeNull();
    expect(
      container.querySelector('[data-testid="composer-send-label"]')?.textContent,
    ).toBe('Send');

    await typeAndSend();

    expect(createPosts).toHaveLength(1);
    expect(createPosts[0].parent_id).toBeNull();
    expect(createPosts[0].content).toBe('from composer');

    // The root still lands in the replica.
    expect(
      mergedNodes.some(([, nodes]) =>
        (nodes as { id: string }[]).some((n) => n.id === CREATED_ID),
      ),
    ).toBe(true);
  });

  it('the ghost Add reply slot still wins over the selection path', async () => {
    renderTreeView();
    await flushPromises();

    act(() => {
      (
        container.querySelector('[data-testid="mock-ghost-reply"]') as HTMLButtonElement
      ).click();
    });
    await flushPromises();

    await typeAndSend();

    expect(createPosts).toHaveLength(1);
    expect(createPosts[0].parent_id).toBe(NODE_A);

    // The chip shows the explicit-ghost copy, not the selection copy.
    expect(
      container.querySelector('[data-testid="composer-reply-target"]')?.textContent,
    ).toContain('Replying to');
  });

  it('deselecting on the canvas drops a ghost-armed target (next Send is a root)', async () => {
    renderTreeView();
    await flushPromises();

    act(() => {
      (
        container.querySelector('[data-testid="mock-ghost-reply"]') as HTMLButtonElement
    ).click();
    });
    await flushPromises();

    act(() => {
      (
        container.querySelector('[data-testid="mock-deselect"]') as HTMLButtonElement
      ).click();
    });
    await flushPromises();

    // Ghost chrome gone, back to the root path.
    expect(
      container.querySelector('[data-testid="composer-reply-target"]'),
    ).toBeNull();
    expect(
      container.querySelector('[data-testid="composer-send-label"]')?.textContent,
    ).toBe('Send');

    await typeAndSend();

    expect(createPosts).toHaveLength(1);
    expect(createPosts[0].parent_id).toBeNull();
  });
});
