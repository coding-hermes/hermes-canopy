/**
 * Component tests — TreeView multi-reference synthesis affordance
 * (DF-HERMES-CANOPY-42)
 *
 * The two-step multi-reference write path exists on the backend
 * (POST /trees/{id}/reference-selections → 200 + signed selection token,
 * then POST /trees/{id}/multi-reference-replies → 201, per
 * internal/handler/multi_reference_integration_test.go §9.1/§9.2) but had
 * zero frontend callers. This file drives the REAL TreeView header bar and
 * pins the full human-reachable flow:
 *
 *   1. the affordance is discoverable before anything is selected;
 *   2. with ≥2 nodes selected the action is enabled and a preflight POST
 *      with `source_node_ids` + `profile_context_budget` is sent, the
 *      composer opens for the synthesis text;
 *   3. send performs the token handoff — `selection_token` from step 2
 *      goes out as the create request's credential, the 201 node lands in
 *      the replica with its N reference edges (§7.1: never inferred from
 *      parent_id);
 *   4. below 2 sources the action stays disabled — the backend's
 *      REFERENCE_SOURCE_COUNT_TOO_LOW must be unreachable from the UI;
 *   5. a preflight failure surfaces the server's own message and sends
 *      nothing.
 *
 * TreeView's neighbours that need a backend, a provider or a canvas are
 * mocked (same harness as TreeViewManifestDeepLink.test.tsx); everything
 * the feature touches (selection state, header bar, the two POSTs, the
 * replica merge) is real.
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
 * The replica starts empty and only gains what mergeBackendNodes/Edges
 * put in. Recording spies let the test assert the created node + its
 * reference edges actually reached the replica (they drive the canvas).
 */
const mergedNodes: unknown[][] = [];
const mergedEdges: unknown[][] = [];

vi.mock('../../stores/treeStore', async () => {
  const actual = await vi.importActual<Record<string, unknown>>(
    '../../stores/treeStore',
  );
  return {
    ...actual,
    createTreeDoc: () => ({
      ydoc: {
        transact: (fn: () => void) => fn(),
        destroy: () => {},
      },
      meta: { get: () => undefined, set: () => {} },
      nodes: new Map(),
      edges: new Map(),
      rootOrder: [],
    }),
    bindIndexedDB: () => ({ destroy: () => {} }),
    seedDemoTree: () => {},
    // Record only merges that carry a payload — the mount-time hydration
    // also calls these with the (empty) REST node list.
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

const NODE_A = '019fb0c2-cab0-70c5-a477-fa10f136e00a';
const NODE_B = '019fb0c2-cab0-70c5-a477-fa10f136e00b';
const NODE_C = '019fb0c2-cab0-70c5-a477-fa10f136e00c';
const CREATED_ID = '019fb0c2-cab0-70c5-a477-fa10f136efff';

vi.mock('../../stores/useYjsTree', () => ({
  useYjsTree: () => ({
    nodes: [
      { id: NODE_A, data: { createdAt: '2026-09-17T09:00:00Z', content: 'first approach' } },
      { id: NODE_B, data: { createdAt: '2026-09-17T10:00:00Z', content: 'second approach' } },
      { id: NODE_C, data: { createdAt: '2026-09-17T11:00:00Z', content: 'third approach' } },
    ],
    edges: [],
    treeTitle: 'DF-42 Tree',
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
vi.mock('../ContextManifestPanel.tsx', () => ({ default: () => null }));
vi.mock('../ContextRunIndicator.tsx', () => ({ default: () => null }));
vi.mock('../ContextAuditDialog.tsx', () => ({ default: () => null }));
vi.mock('../MessageComposer.tsx', () => ({
  default: () => null,
  MultiReferenceComposer: (props: {
    sources: Array<{ id: string }>;
    draft: string;
    onDraftChange: (value: string) => void;
    onReorder: (sourceIds: string[]) => void;
    onSubmit: () => void;
    preflightRequired: boolean;
    onPreflight: () => void;
    replyDisabled: boolean;
    error: string | null;
  }) => createElement(
    'div',
    { 'data-testid': 'multi-reference-composer' },
    props.sources.map((source, index) => createElement('span', {
      key: source.id,
      'data-testid': `multi-reference-chip-${source.id}`,
    }, source.id, index > 0 && createElement('button', {
      'aria-label': `Move R${index + 1} up`,
      onClick: () => props.onReorder([
        ...props.sources.slice(0, index - 1).map((item) => item.id),
        source.id,
        props.sources[index - 1].id,
        ...props.sources.slice(index + 1).map((item) => item.id),
      ]),
    }, 'up'))),
    createElement('textarea', {
      'data-testid': 'multi-reference-content',
      value: props.draft,
      onChange: (event: Event) => props.onDraftChange((event.target as HTMLTextAreaElement).value),
    }),
    createElement('button', {
      'data-testid': 'multi-reference-send',
      disabled: props.preflightRequired || props.replyDisabled,
      onClick: props.onSubmit,
    }, 'Create synthesis'),
    props.preflightRequired && createElement('button', {
      'data-testid': 'multi-reference-preflight',
      onClick: props.onPreflight,
    }, 'Run preflight again'),
    props.error && createElement('p', { 'data-testid': 'multi-reference-error' }, props.error),
  ),
}));

// ─── Harness ───────────────────────────────────────────────────────────

const TREE_ID = '9a7f97f3-0000-7000-8000-000000000042';

interface RecordedRequest {
  url: string;
  body: Record<string, unknown> | null;
}

/** Every POST the flow made, in order. */
let posts: RecordedRequest[];
/** Overrides for the two feature endpoints; default happy-path bodies. */
let preflightResponse: unknown;
let preflightStatus: number;
let createResponse: unknown;
let createStatus: number;

let container: HTMLDivElement;
let root: Root;
const fetchMock = vi.fn();

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const PREFLIGHT_ENVELOPE = {
  tree_id: TREE_ID,
  canonical_source_ids: [NODE_A, NODE_B],
  primary_source_id: NODE_A,
  is_synthetic_merge_point: true,
  expires_at: '2026-09-17T12:00:00Z',
  selection_token: 'mrs.v1.test-payload.sig',
  context_budget: {
    available_tokens: 8192,
    minimum_required_tokens: 512,
    estimated_tokens: 640,
    fits: true,
  },
  sources: [
    {
      node_id: NODE_A,
      source_label: 'R1',
      color_key: 'ref-0',
      branch_root_id: NODE_A,
      content_hash: 'a'.repeat(64),
      sequence_num: 1,
      content_preview: 'first approach',
    },
    {
      node_id: NODE_B,
      source_label: 'R2',
      color_key: 'ref-1',
      branch_root_id: NODE_A,
      content_hash: 'b'.repeat(64),
      sequence_num: 2,
      content_preview: 'second approach',
    },
  ],
};

const CREATE_ENVELOPE = {
  node: {
    id: CREATED_ID,
    tree_id: TREE_ID,
    parent_id: NODE_A,
    parent_mode: 'multi_reference',
    author_id: '00000000-0000-0000-0000-000000000001',
    node_type: 'message',
    content: 'merged',
    content_format: 'markdown',
    sequence_num: 9,
    metadata: {
      multi_reference: {
        version: 1,
        primarySourceId: NODE_A,
        canonicalSourceIds: [NODE_A, NODE_B],
        isSyntheticMergePoint: true,
        contextManifestHash: 'c'.repeat(64),
        contextTokenBudget: 8192,
      },
    },
    created_at: '2026-09-17T11:30:00Z',
  },
  edges: [
    {
      id: 'edge-a',
      tree_id: TREE_ID,
      source_node_id: NODE_A,
      target_node_id: CREATED_ID,
      edge_type: 'reference',
      sequence_num: 1,
      metadata: { role: 'context_source', source_label: 'R1', color_key: 'ref-0', reference_index: 0 },
      created_at: '2026-09-17T11:30:00Z',
    },
    {
      id: 'edge-b',
      tree_id: TREE_ID,
      source_node_id: NODE_B,
      target_node_id: CREATED_ID,
      edge_type: 'reference',
      sequence_num: 2,
      metadata: { role: 'context_source', source_label: 'R2', color_key: 'ref-1', reference_index: 1 },
      created_at: '2026-09-17T11:30:00Z',
    },
  ],
  reference_context: {
    manifest_hash: 'c'.repeat(64),
    source_count: 2,
    is_synthetic_merge_point: true,
  },
};

function routeFetch(url: string, init?: RequestInit): Response {
  const method = init?.method ?? 'GET';
  if (method === 'POST' && url.includes('/reference-selections')) {
    posts.push({ url, body: JSON.parse(String(init?.body ?? 'null')) });
    return jsonResponse(preflightResponse, preflightStatus);
  }
  if (method === 'POST' && url.includes('/multi-reference-replies')) {
    posts.push({ url, body: JSON.parse(String(init?.body ?? 'null')) });
    return jsonResponse(createResponse, createStatus);
  }
  if (url.startsWith('/api/v1/gateway/models')) {
    return jsonResponse({ models: [] });
  }
  if (url.startsWith(`/api/v1/trees/${TREE_ID}/nodes`)) {
    return jsonResponse({ nodes: [] });
  }
  if (url.startsWith(`/api/v1/trees/${TREE_ID}`)) {
    return jsonResponse({ title: 'DF-42 Tree' });
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

function bar(): HTMLElement {
  const el = container.querySelector('[data-testid="multi-reference-bar"]');
  expect(el).not.toBeNull();
  return el as HTMLElement;
}

/** Query that throws with the selector in the message (narrowing helper). */
function q<T extends Element>(selector: string): T {
  const el = container.querySelector<T>(selector);
  if (!el) throw new Error(`missing element: ${selector}`);
  return el;
}

function synthButton(): HTMLButtonElement {
  return q<HTMLButtonElement>('[data-testid="multi-reference-synthesize"]');
}

/**
 * Drive the selection state through the checkbox list the affordance
 * renders in the header bar (the real user path; the canvas is mocked out).
 */
function toggle(id: string) {
  act(() => {
    q<HTMLInputElement>(`input[value="${id}"]`).click();
  });
}

/**
 * Type into a controlled textarea the way React's value tracker expects —
 * a plain `.value=` assignment is ignored by onChange (same helper shape
 * as TreeViewContextAuditGate.test.tsx).
 */
function setTextareaValue(el: HTMLTextAreaElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLTextAreaElement.prototype,
    'value',
  )!.set!;
  setter.call(el, value);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}

async function clickSynthesize() {
  await act(async () => {
    synthButton().click();
    await Promise.resolve();
  });
}

function sendButton(): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>(
    '[data-testid="multi-reference-send"]',
  );
  expect(el).not.toBeNull();
  return el as HTMLButtonElement;
}

beforeEach(() => {
  fetchMock.mockReset();
  posts = [];
  preflightResponse = PREFLIGHT_ENVELOPE;
  preflightStatus = 200;
  createResponse = CREATE_ENVELOPE;
  createStatus = 201;
  mergedNodes.length = 0;
  mergedEdges.length = 0;
  fetchMock.mockImplementation(
    (url: string | URL | Request, init?: RequestInit) =>
      Promise.resolve(routeFetch(String(url), init)),
  );
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  act(() => root.unmount());
  container?.remove();
  vi.unstubAllGlobals();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('TreeView — multi-reference synthesis affordance (DF-HERMES-CANOPY-42)', () => {
  it('is discoverable before any selection: bar renders with a hint', async () => {
    renderTreeView(`/tree/${TREE_ID}`);
    await flushPromises();

    expect(bar()).not.toBeNull();
    expect(synthButton()).not.toBeNull();
    // Discoverability: a first-time user must be able to find the flow,
    // so the affordance explains itself (title attribute is the floor).
    expect(synthButton().getAttribute('title')).toContain('2');
    expect(synthButton().disabled).toBe(true);
  });

  it('runs preflight → composer → create, then merges the node + reference edges', async () => {
    renderTreeView(`/tree/${TREE_ID}`);
    await flushPromises();

    toggle(NODE_A);
    toggle(NODE_B);
    expect(synthButton().disabled).toBe(false);

    await clickSynthesize();

    // §9.1 preflight shape.
    expect(posts.length).toBe(1);
    expect(posts[0].url).toContain('/reference-selections');
    expect(posts[0].body).toEqual({
      source_node_ids: [NODE_A, NODE_B],
      profile_context_budget: 16384,
    });

    // Composer opens for the synthesis text.
    const textarea = q<HTMLTextAreaElement>(
      '[data-testid="multi-reference-content"]',
    );
    act(() => {
      setTextareaValue(textarea, 'The two approaches disagree on storage semantics.');
    });

    await act(async () => {
      sendButton().click();
      await Promise.resolve();
      await Promise.resolve();
    });

    // §9.2 create shape: the opaque token from step 1 is the credential.
    expect(posts.length).toBe(2);
    expect(posts[1].url).toContain('/multi-reference-replies');
    expect(posts[1].body).toEqual({
      selection_token: 'mrs.v1.test-payload.sig',
      content: 'The two approaches disagree on storage semantics.',
      content_format: 'markdown',
    });

    // The created node + its two PERSISTED reference edges reach the
    // replica (§7.1: the canvas renders from the Yjs doc, not the REST
    // response).
    expect(mergedNodes.length).toBe(1);
    const node = (mergedNodes[0][1] as Array<Record<string, unknown>>)[0];
    expect(node.id).toBe(CREATED_ID);
    // §7.1 display anchor: parent_id = primary source; multi-reference
    // identity itself is carried by the reserved metadata (the replica
    // derives it from there, there is no parentMode column client-side).
    expect(node.parentId).toBe(NODE_A);
    expect(node.metadata).toEqual(CREATE_ENVELOPE.node.metadata);
    expect(mergedEdges.length).toBe(1);
    const edges = mergedEdges[0][1] as Array<Record<string, unknown>>;
    expect(edges.map((e) => e.edgeType)).toEqual(['reference', 'reference']);
  });

  it('keeps the action below 2 sources — the TOO_LOW rejection is unreachable', async () => {
    renderTreeView(`/tree/${TREE_ID}`);
    await flushPromises();

    toggle(NODE_A);
    expect(synthButton().disabled).toBe(true);
    await clickSynthesize();

    expect(posts.length).toBe(0);
    expect(
      container.querySelector('[data-testid="multi-reference-content"]'),
    ).toBeNull();
  });

  it('surfaces the preflight error and sends nothing', async () => {
    preflightStatus = 409;
    preflightResponse = {
      error: { code: 'REFERENCE_SELECTION_STALE', message: 'The tree changed since this selection was made.' },
    };

    renderTreeView(`/tree/${TREE_ID}`);
    await flushPromises();

    toggle(NODE_A);
    toggle(NODE_B);
    await clickSynthesize();

    expect(posts.length).toBe(1);
    expect(
      container.querySelector('[data-testid="multi-reference-error"]')?.textContent,
    ).toContain('changed since this selection');
    // No create request without a token to spend.
    expect(
      container.querySelector('[data-testid="multi-reference-content"]'),
    ).toBeNull();
  });

  it('reordering source chips invalidates the token and requires a new preflight (scenario 2, §4.3)', async () => {
    renderTreeView(`/tree/${TREE_ID}`);
    await flushPromises();
    toggle(NODE_A);
    toggle(NODE_B);
    await clickSynthesize();

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="Move R2 up"]')?.click());
    expect(container.querySelector('[data-testid="multi-reference-preflight"]')).not.toBeNull();
    expect(sendButton().disabled).toBe(true);

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[data-testid="multi-reference-preflight"]')?.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(posts).toHaveLength(2);
    expect(posts[1].body?.source_node_ids).toEqual([NODE_B, NODE_A]);
  });

  it('disables Reply and keeps the server underbudget text visible (scenario 12)', async () => {
    preflightResponse = {
      ...PREFLIGHT_ENVELOPE,
      context_budget: {
        ...PREFLIGHT_ENVELOPE.context_budget,
        fits: false,
        error: 'REFERENCE_CONTEXT_BUDGET_EXCEEDED: selected sources are too large',
      },
    };
    renderTreeView(`/tree/${TREE_ID}`);
    await flushPromises();
    toggle(NODE_A);
    toggle(NODE_B);
    await clickSynthesize();

    expect(sendButton().disabled).toBe(true);
    expect(container.textContent).toContain('REFERENCE_CONTEXT_BUDGET_EXCEEDED');
  });

  it('preserves the draft and asks for renewed preflight after stale create (scenario 13)', async () => {
    createStatus = 409;
    createResponse = {
      error: { code: 'REFERENCE_SELECTION_STALE', message: 'selection token expired' },
    };
    renderTreeView(`/tree/${TREE_ID}`);
    await flushPromises();
    toggle(NODE_A);
    toggle(NODE_B);
    await clickSynthesize();
    const textarea = q<HTMLTextAreaElement>('[data-testid="multi-reference-content"]');
    act(() => setTextareaValue(textarea, 'draft survives token expiry'));

    await act(async () => {
      sendButton().click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(q<HTMLTextAreaElement>('[data-testid="multi-reference-content"]').value).toBe('draft survives token expiry');
    expect(container.querySelector('[data-testid="multi-reference-preflight"]')).not.toBeNull();
    expect(container.textContent).toContain('Run preflight again before replying');
  });
});
