/**
 * Component tests — TreeView's audit-before-send gate (GAP-096)
 *
 * The typed criterion: a UI-initiated context run against a selected
 * node must show the compiled-context manifest with an explicit
 * Send / Adjust / Cancel choice BEFORE any POST /gateway/runs leaves
 * the UI. This file drives the same real chain as the GAP-084 suite
 * (TreeView → MessageComposer → useGatewayRuns → gatewayApi) against a
 * stubbed `fetch`, plus the real `contextApi` and the real
 * `ContextAuditDialog`, so the assertion is made on requests that leave
 * the UI, not on mocked layers.
 *
 * TreeView's neighbours are mocked (canvas, Yjs stores, presence) exactly
 * as in TreeViewContextRun.test.tsx — none of them are under test here.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import TreeView from '../TreeView.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Neighbour mocks (same as TreeViewContextRun.test.tsx) ─────────────

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
    treeTitle: 'GAP-096 Tree',
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

/** The compiled payload GET /api/v1/context/{node_id} returns. */
const PREVIEW_BODY = {
  content: 'Welcome to Hermes Canopy',
  manifest: {
    requestId: 'req-96',
    nodeId: NODE_A,
    compiledAt: '2026-09-21T10:00:00Z',
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
      {
        id: '019fb0c2-cab0-70c5-a477-fa10f136e001',
        kind: 'node',
        title: 'Child 1: Architecture',
        tokenCount: 312,
        truncated: true,
      },
    ],
    references: null,
    cards: null,
    omittedCount: 3,
    omittedReason: 'budget',
    truncationMarkers: null,
    warnings: null,
    manifestHash: 'a'.repeat(64),
  },
};

/** The preview returned once the user adjusts to ?budget=4096. */
const PREVIEW_BODY_4096 = {
  content: 'Welcome to Hermes Canopy',
  manifest: {
    ...PREVIEW_BODY.manifest,
    tokenBudget: 4096,
    tokensUsed: 900,
  },
};

/** The run record canopyd returns once the context-aware run is started. */
const CONTEXT_RUN = {
  run_id: RUN_ID,
  session_id: '',
  message: 'summarise this thread',
  model: 'hermes',
  status: 'running',
  created_at: '2026-09-21T10:00:00Z',
  events: [],
  source_node_id: NODE_A,
  token_budget: 8000,
  context_tokens: 1240,
  manifest: PREVIEW_BODY.manifest,
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
/** Context preview URLs requested, in order. */
let previewGets: string[] = [];
/** When non-null, the next context preview resolves with this error. */
let previewFailure: string | null = null;

function routeFetch(url: string, init?: RequestInit): Response {
  const method = init?.method ?? 'GET';

  if (url.startsWith('/api/v1/gateway/status')) {
    return jsonResponse({
      connected: true,
      base_url: 'http://127.0.0.1:8642',
      run_count: 1,
      active_runs: contextRunStarted ? 1 : 0,
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
    previewGets.push(url);
    if (previewFailure !== null) {
      return jsonResponse(
        { error: { message: previewFailure } },
        500,
      );
    }
    if (url.endsWith('budget=4096')) {
      return jsonResponse(PREVIEW_BODY_4096);
    }
    return jsonResponse(PREVIEW_BODY);
  }

  if (url === `/api/v1/trees/${TREE_ID}`) {
    return jsonResponse({ title: 'GAP-096 Tree' });
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

/** Same, for plain <input> elements (the dialog's budget field). */
function setInputValue(input: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value',
  )!.set!;
  setter.call(input, value);
  input.dispatchEvent(new Event('input', { bubbles: true }));
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

function selectNode(): void {
  act(() => {
    (
      container.querySelector('[data-testid="mock-select-node"]') as HTMLButtonElement
    ).click();
  });
}

function auditDialog(): Element | null {
  return container.querySelector('[data-testid="context-audit-dialog"]');
}

/** Select node → type message → click Run with context (opens the dialog). */
async function openAuditDialog(): Promise<void> {
  selectNode();
  await flushPromises();
  act(() => setComposerValue(composerTextarea(), 'summarise this thread'));
  await flushPromises();
  await act(async () => {
    contextRunButton().click();
    await Promise.resolve();
    await Promise.resolve();
  });
  await flushPromises();
}

function q<T extends Element>(testid: string): T {
  return container.querySelector(`[data-testid="${testid}"]`) as T;
}

beforeEach(() => {
  fetchMock.mockReset();
  fetchMock.mockImplementation((url: string, init?: RequestInit) =>
    Promise.resolve(routeFetch(String(url), init)),
  );
  vi.stubGlobal('fetch', fetchMock);
  contextRunStarted = false;
  startRunPosts = [];
  previewGets = [];
  previewFailure = null;
});

afterEach(() => {
  act(() => root.unmount());
  container?.remove();
  vi.unstubAllGlobals();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('TreeView — audit-before-send gate (GAP-096)', () => {
  it('shows the manifest audit dialog BEFORE any POST /gateway/runs', async () => {
    renderTreeView();
    await flushPromises();

    // No dialog with no interaction.
    expect(auditDialog()).toBeNull();

    await openAuditDialog();

    // The dialog is up, and the PREVIEW request (not the run POST) went out.
    expect(auditDialog()).not.toBeNull();
    expect(previewGets).toEqual([`/api/v1/context/${NODE_A}`]);
    expect(startRunPosts).toHaveLength(0);
    expect(
      fetchMock.mock.calls.some(
        ([u, init]) =>
          String(u) === '/api/v1/gateway/runs' &&
          (init as RequestInit | undefined)?.method === 'POST',
      ),
    ).toBe(false);

    // The audit facts, verbatim from the preview payload.
    expect(q('context-audit-budget').textContent).toBe('8,000');
    expect(q('context-audit-tokens-used').textContent).toBe(
      '1,240 / 8,000 tokens',
    );
    expect(q('context-audit-included').textContent).toBe('2 sources');
    expect(q('context-audit-omitted').textContent).toBe('3 items (budget)');
    expect(q('context-audit-hash').textContent).toBe('a'.repeat(12));
    // Top included sources — first two ancestry items, trimmed titles.
    const sources = container.querySelectorAll('[data-testid="context-audit-source"]');
    expect(sources).toHaveLength(2);
    expect(sources[0].textContent).toContain('Welcome to Hermes Canopy');
    expect(sources[1].textContent).toContain('Child 1: Architecture');
  });

  it('Send proceeds with the previewed budget as token_budget', async () => {
    renderTreeView();
    await flushPromises();
    await openAuditDialog();

    await act(async () => {
      q<HTMLButtonElement>('context-audit-send').click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();

    // Exactly one run POST, carrying the previewed budget.
    expect(startRunPosts).toHaveLength(1);
    expect(startRunPosts[0]).toEqual({
      message: 'summarise this thread',
      node_id: NODE_A,
      token_budget: 8000,
    });
    // The dialog closed after Send.
    expect(auditDialog()).toBeNull();
    // The composer cleared its text (the promise resolved on Send).
    expect(composerTextarea().value).toBe('');
  });

  it('Cancel makes no run request and closes the dialog', async () => {
    renderTreeView();
    await flushPromises();
    await openAuditDialog();
    expect(auditDialog()).not.toBeNull();

    await act(async () => {
      q<HTMLButtonElement>('context-audit-cancel').click();
      await Promise.resolve();
    });

    expect(auditDialog()).toBeNull();
    expect(startRunPosts).toHaveLength(0);
    expect(
      fetchMock.mock.calls.some(
        ([u, init]) =>
          String(u) === '/api/v1/gateway/runs' &&
          (init as RequestInit | undefined)?.method === 'POST',
      ),
    ).toBe(false);
    // The composer kept the user's text (the promise rejected on Cancel).
    expect(composerTextarea().value).toBe('summarise this thread');
  });

  it('Adjust with an invalid budget shows the inline error and makes no run request', async () => {
    renderTreeView();
    await flushPromises();
    await openAuditDialog();

    act(() => q<HTMLButtonElement>('context-audit-adjust').click());

    const input = q<HTMLInputElement>('context-audit-budget-input');
    expect(input).not.toBeNull();
    act(() => setInputValue(input, '0'));
    await act(async () => {
      q<HTMLButtonElement>('context-audit-budget-apply').click();
      await Promise.resolve();
    });

    // Inline rejection, dialog stays open, no re-preview, no run POST.
    expect(q('context-audit-budget-error').textContent).toBe(
      'Budget must be a positive whole number of tokens.',
    );
    expect(auditDialog()).not.toBeNull();
    expect(previewGets).toHaveLength(1); // only the initial preview
    expect(startRunPosts).toHaveLength(0);
  });

  it('Adjust re-previews with ?budget= and Send uses the adjusted budget', async () => {
    renderTreeView();
    await flushPromises();
    await openAuditDialog();

    act(() => q<HTMLButtonElement>('context-audit-adjust').click());
    act(() => setInputValue(q<HTMLInputElement>('context-audit-budget-input'), '4096'));
    await act(async () => {
      q<HTMLButtonElement>('context-audit-budget-apply').click();
      await Promise.resolve();
    });
    await flushPromises();

    // The re-preview went out with the adjusted budget, and the dialog
    // shows the NEW effective numbers.
    expect(previewGets).toEqual([
      `/api/v1/context/${NODE_A}`,
      `/api/v1/context/${NODE_A}?budget=4096`,
    ]);
    expect(q('context-audit-budget').textContent).toBe('4,096');
    expect(q('context-audit-tokens-used').textContent).toBe('900 / 4,096 tokens');
    expect(startRunPosts).toHaveLength(0);

    await act(async () => {
      q<HTMLButtonElement>('context-audit-send').click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();

    expect(startRunPosts).toHaveLength(1);
    expect(startRunPosts[0]).toEqual({
      message: 'summarise this thread',
      node_id: NODE_A,
      token_budget: 4096,
    });
  });

  it('preview failure shows the error with Cancel/Retry and never auto-sends', async () => {
    renderTreeView();
    await flushPromises();

    previewFailure = 'context compiler error';
    await openAuditDialog();

    expect(auditDialog()).not.toBeNull();
    expect(q('context-audit-error')).not.toBeNull();
    expect(startRunPosts).toHaveLength(0);
    // Send is disabled while no manifest is shown.
    expect(q<HTMLButtonElement>('context-audit-send').disabled).toBe(true);

    // Retry recovers: the preview succeeds and the manifest appears.
    previewFailure = null;
    await act(async () => {
      q<HTMLButtonElement>('context-audit-retry').click();
      await Promise.resolve();
    });
    await flushPromises();
    expect(q('context-audit-budget').textContent).toBe('8,000');

    await act(async () => {
      q<HTMLButtonElement>('context-audit-send').click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();
    expect(startRunPosts).toHaveLength(1);
    expect(startRunPosts[0]?.token_budget).toBe(8000);
  });

  it('no-node path is unchanged: reject, explanation, and no requests', async () => {
    renderTreeView();
    await flushPromises();

    act(() => setComposerValue(composerTextarea(), 'no node selected'));
    await flushPromises();

    expect(contextRunButton().disabled).toBe(true);
    expect(contextRunButton().getAttribute('title')).toBe(
      'Select a node to run with its compiled context',
    );
    act(() => contextRunButton().click());
    await flushPromises();

    expect(auditDialog()).toBeNull();
    expect(previewGets).toHaveLength(0);
    expect(startRunPosts).toHaveLength(0);
    expect(composerTextarea().value).toBe('no node selected');
    expect(container.querySelector('[data-testid="context-run-error"]')).toBeNull();
  });
});
