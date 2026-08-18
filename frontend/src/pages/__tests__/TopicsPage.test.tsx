/**
 * Component tests — TopicsPage create-topic dialog (GAP-044)
 *
 * Pins the GAP-044 fix: the New Topic dialog auto-resolves the active
 * tree's root node from `GET /trees/{id}` so creating a topic never
 * requires hand-typing a UUID.
 *
 *  - the dialog opens from the ?new=1 deep link and fetches the tree
 *    detail, reading the REAL wire key `root_node_id` (snake_case —
 *    internal/service TreeSummary marshals `json:"root_node_id"`)
 *  - the resolved node renders as a read-only display (no editable
 *    root-node input remains) and Create is enabled without typing
 *  - the POST body carries the resolved rootNodeId with camelCase keys
 *    (the topics API contract: treeId/rootNodeId/title/description)
 *  - resolution failure (404 / zero-UUID root) surfaces an inline error
 *    and keeps Create disabled
 *
 * Driven with React 19's `act` + `react-dom/client` inside a
 * MemoryRouter, matching pages/__tests__/WorkspacePage.test.tsx.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import TopicsPage from '../../pages/TopicsPage.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Mock the API + active-tree lib ────────────────────────────────────

const apiMocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiDelete: vi.fn(),
}));

vi.mock('../../lib/api', () => ({
  apiGet: apiMocks.apiGet,
  apiPost: apiMocks.apiPost,
  apiDelete: apiMocks.apiDelete,
}));

vi.mock('../../lib/activeTree', () => ({
  readStoredTreeId: () => '',
  storeTreeId: vi.fn(),
  notifyTopicsChanged: vi.fn(),
}));

import { apiGet, apiPost } from '../../lib/api';

// ─── Fixtures ──────────────────────────────────────────────────────────

const TREE_ID = '019fb0c2-cab0-70c5-a477-fa10f136e000';
const ROOT_NODE_ID = '019fe85d-a01f-7eac-92a7-d031a7d0ac00';
const ZERO_UUID = '00000000-0000-0000-0000-000000000000';

/** A realistic 200 body, shaped exactly as internal/service marshals it. */
function treeDetailBody(rootNodeId = ROOT_NODE_ID) {
  return {
    id: TREE_ID,
    title: 'My Tree',
    description: '',
    owner_id: '00000000-0000-0000-0000-000000000001',
    owner_display_name: '',
    node_count: 1,
    member_count: 1,
    root_node_id: rootNodeId,
    created_at: '2026-08-09T21:11:17.790752Z',
    updated_at: '2026-08-09T21:11:17.790427Z',
    role: 'owner',
  };
}

function createdTopic(title: string) {
  return {
    id: 'topic-1',
    tree_id: TREE_ID,
    root_node_id: ROOT_NODE_ID,
    title,
    description: '',
    slug: 'my-topic',
    status: 'active',
    node_count: 1,
    created_at: '2026-08-09T21:20:00Z',
  };
}

/** Route apiGet by path — the tree list, the tree detail, and the topics list. */
function defaultApiGet(path: string): unknown {
  if (path === '/trees?limit=100') {
    return { trees: [{ id: TREE_ID, title: 'My Tree' }], pagination: { total: 1 } };
  }
  if (path === `/trees/${TREE_ID}`) {
    return treeDetailBody();
  }
  if (path.startsWith('/topics?')) {
    return { topics: [] };
  }
  throw new Error(`unexpected apiGet: ${path}`);
}

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  vi.clearAllMocks();
  apiMocks.apiGet.mockImplementation(defaultApiGet);
  apiMocks.apiPost.mockImplementation(async (_path: string, body: unknown) => {
    const b = body as { title: string };
    return createdTopic(b.title);
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

function mount(initialEntry = '/topics?tree=' + TREE_ID + '&new=1') {
  act(() => {
    root.render(
      createElement(
        MemoryRouter,
        { initialEntries: [initialEntry] },
        createElement(TopicsPage),
      ),
    );
  });
}

async function settle(n = 4): Promise<void> {
  await act(async () => {
    for (let i = 0; i < n; i++) await Promise.resolve();
  });
}

function q(selector: string): HTMLElement | null {
  return container.querySelector(selector);
}

/**
 * Set a controlled input's value the way React expects (native setter +
 * bubbling input event) — same helper as WorkspacePage.test.tsx.
 */
function setInputValue(input: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value',
  )!.set!;
  setter.call(input, value);
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

function submitButton(): HTMLButtonElement {
  const btn = q('[data-testid="create-topic-submit"]');
  expect(btn).not.toBeNull();
  return btn as HTMLButtonElement;
}

// ─── Tests ─────────────────────────────────────────────────────────────

describe('TopicsPage — CreateTopicDialog (GAP-044)', () => {
  it('opens via ?new=1, auto-resolves the root node from tree detail, and enables Create without UUID typing', async () => {
    mount();
    await settle();

    // The dialog fetched the tree detail to resolve the root node.
    expect(apiGet).toHaveBeenCalledWith(`/trees/${TREE_ID}`);

    // The resolved root node is displayed read-only (a div, not an input),
    // and no UUID-typing field exists anywhere in the dialog.
    const display = q('[data-testid="create-topic-root-node"]');
    expect(display).not.toBeNull();
    expect(display?.tagName).toBe('DIV');
    expect(display?.textContent).toContain(ROOT_NODE_ID);
    expect(q('input[placeholder="UUID of the root node"]')).toBeNull();

    // With only the title typed — no UUID anywhere — Create is enabled.
    const titleInput = q('input[placeholder="Topic title"]') as HTMLInputElement;
    expect(titleInput).not.toBeNull();
    act(() => setInputValue(titleInput, 'My First Topic'));
    await settle();
    expect(submitButton().disabled).toBe(false);
  });

  it('POSTs the resolved rootNodeId with camelCase keys', async () => {
    mount();
    await settle();

    const titleInput = q('input[placeholder="Topic title"]') as HTMLInputElement;
    expect(titleInput).not.toBeNull();
    act(() => setInputValue(titleInput, 'My First Topic'));
    await settle();
    expect(submitButton().disabled).toBe(false);

    act(() => submitButton().click());
    await settle();

    expect(apiPost).toHaveBeenCalledWith('/topics', {
      treeId: TREE_ID,
      rootNodeId: ROOT_NODE_ID,
      title: 'My First Topic',
      description: undefined,
    });
  });

  it('surfaces an inline error and disables Create when root resolution fails', async () => {
    apiMocks.apiGet.mockImplementation((path: string) => {
      if (path === `/trees/${TREE_ID}`) {
        return Promise.reject(new Error('TREE_NOT_FOUND'));
      }
      return Promise.resolve(defaultApiGet(path));
    });

    mount();
    await settle();

    const error = q('[data-testid="create-topic-error"]');
    expect(error).not.toBeNull();
    expect(error?.textContent).toContain('Could not resolve the root node');
    expect(q('[data-testid="create-topic-root-node"]')?.textContent).toBe(
      'Unavailable',
    );
    expect(submitButton().disabled).toBe(true);
  });

  it('treats a zero-UUID root as unresolved (clear inline error, Create disabled)', async () => {
    apiMocks.apiGet.mockImplementation((path: string) => {
      if (path === `/trees/${TREE_ID}`) {
        return Promise.resolve(treeDetailBody(ZERO_UUID));
      }
      return Promise.resolve(defaultApiGet(path));
    });

    mount();
    await settle();

    const error = q('[data-testid="create-topic-error"]');
    expect(error).not.toBeNull();
    expect(error?.textContent).toContain(
      'Could not resolve a root node for this tree.',
    );
    expect(submitButton().disabled).toBe(true);
  });
});
