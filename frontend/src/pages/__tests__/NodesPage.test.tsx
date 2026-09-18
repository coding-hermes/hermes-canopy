/**
 * Component tests — Nodes page bulk merge (DF-HERMES-CANOPY-27)
 *
 * The bulk Merge action used to be a disabled roadmap button. It is now
 * wired to the live tree-scoped route
 *
 *     POST /api/v1/trees/{tree_id}/merge   → 201 {node, edges, merged_source_ids}
 *
 * These tests drive the real page (stubbed `fetch`, same createRoot + act +
 * MemoryRouter harness as AgentsPage.test.tsx) and pin the four facts the
 * unit tests cannot see:
 *
 *   - the button is enabled at 2 selected rows and disabled at 1;
 *   - clicking it opens the merge dialog, which lists the sources;
 *   - submitting sends the §3.2 body (snake_case, explicit `content`) to the
 *     tree-scoped route, and the returned synthesis node appears in the list
 *     with the selection cleared;
 *   - a server rejection keeps the dialog open, shows the message, and does
 *     NOT clear the selection, so the user can fix and retry.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import NodesPage from '../../pages/NodesPage.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Fixtures ──────────────────────────────────────────────────────────

const TREE_ID = '0191a8b2-7fff-7000-9000-000000000001';
const NODE_A = '0191a8b2-7fff-7000-9000-000000000101';
const NODE_B = '0191a8b2-7fff-7000-9000-000000000102';
const NODE_C = '0191a8b2-7fff-7000-9000-000000000103';
const SYNTH_ID = '0191a8b2-7fff-7000-9000-000000000301';

const TREE = {
  id: TREE_ID,
  title: 'Merge tree',
  description: '',
  node_count: 3,
  root_node_id: NODE_A,
  created_at: '2026-09-18T20:00:00Z',
};

/** A list-endpoint node (camelCase — `GET /trees/{id}/nodes`). */
function listNode(id: string, sequenceNum: number, parentId: string | null) {
  return {
    id,
    treeId: TREE_ID,
    parentId,
    authorId: '0191a8b2-7fff-7000-9000-000000000042',
    authorDisplayName: 'Bane',
    content: `content for ${id}`,
    contentFormat: 'markdown',
    nodeType: 'message',
    sequenceNum,
    metadata: {},
    depth: parentId ? 1 : 0,
    childCount: 0,
    createdAt: '2026-09-18T20:00:00Z',
    editedAt: null,
    deletedAt: null,
  };
}

const NODES = [
  listNode(NODE_A, 1, null),
  listNode(NODE_B, 2, NODE_A),
  listNode(NODE_C, 3, NODE_A),
];

/** The §3.6 201 envelope (snake_case on that boundary). */
const MERGE_201 = {
  node: {
    id: SYNTH_ID,
    tree_id: TREE_ID,
    parent_id: NODE_A,
    author_id: '0191a8b2-7fff-7000-9000-000000000042',
    author_display_name: 'Bane',
    content: 'Synthesis summary',
    content_format: 'markdown',
    node_type: 'synthesis',
    sequence_num: 4,
    metadata: {},
    depth: 1,
    child_count: 0,
    created_at: '2026-09-18T23:15:00Z',
    edited_at: null,
    deleted_at: null,
  },
  edges: [
    {
      id: 'e-1',
      tree_id: TREE_ID,
      source_node_id: NODE_A,
      target_node_id: SYNTH_ID,
      edge_type: 'reply',
      created_at: '2026-09-18T23:15:00Z',
    },
    {
      id: 'e-2',
      tree_id: TREE_ID,
      source_node_id: NODE_B,
      target_node_id: SYNTH_ID,
      edge_type: 'synthesis',
      created_at: '2026-09-18T23:15:00Z',
    },
    {
      id: 'e-3',
      tree_id: TREE_ID,
      source_node_id: NODE_C,
      target_node_id: SYNTH_ID,
      edge_type: 'synthesis',
      created_at: '2026-09-18T23:15:00Z',
    },
  ],
  merged_source_ids: [NODE_B, NODE_C],
};

function okResponse(body: unknown, status = 200): Response {
  return {
    ok: true,
    status,
    text: () => Promise.resolve(JSON.stringify(body)),
    json: () => Promise.resolve(body),
  } as Response;
}

function errorResponse(status: number, code: string, message: string): Response {
  return {
    ok: false,
    status,
    text: () => Promise.resolve(JSON.stringify({ error: { code, message } })),
    json: () => Promise.resolve({ error: { code, message } }),
  } as Response;
}

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;
let mergePosts: Array<{ path: string; body: unknown }>;
let mergeResponse: () => Response;

function postBodyOf(init?: RequestInit): unknown {
  const raw = init?.body;
  return typeof raw === 'string' ? JSON.parse(raw) : null;
}

async function settle(n = 4): Promise<void> {
  await act(async () => {
    for (let i = 0; i < n; i++) await Promise.resolve();
  });
}

function q(selector: string): HTMLElement | null {
  return container.querySelector(selector);
}

function qa(selector: string): HTMLElement[] {
  return Array.from(container.querySelectorAll(selector));
}

function mount() {
  act(() => {
    root.render(
      createElement(
        MemoryRouter,
        { initialEntries: [`/nodes?tree=${TREE_ID}`] },
        createElement(NodesPage),
      ),
    );
  });
}

/** Check the row at `index` and let the bulk bar re-render. */
async function checkRow(index: number): Promise<void> {
  const boxes = qa('[data-testid="node-row-checkbox"]');
  expect(boxes.length).toBeGreaterThan(index);
  await act(async () => boxes[index].click());
  await settle();
}

function mergeButton(): HTMLButtonElement {
  const btn = q('[data-testid="bulk-action-merge"]');
  expect(btn).not.toBeNull();
  return btn as HTMLButtonElement;
}

beforeEach(() => {
  mergePosts = [];
  mergeResponse = () => okResponse(MERGE_201, 201);

  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);

  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const path = url.replace(/^https?:\/\/[^/]+/, '');
    const method = init?.method ?? 'GET';

    if (path.endsWith('/merge') && method === 'POST') {
      mergePosts.push({ path, body: postBodyOf(init) });
      return Promise.resolve(mergeResponse());
    }
    if (path === `/api/v1/trees/${TREE_ID}/nodes` && method === 'GET') {
      return Promise.resolve(okResponse({ nodes: NODES }));
    }
    if (path.startsWith('/api/v1/trees?') && method === 'GET') {
      return Promise.resolve(okResponse({ trees: [TREE], pagination: { total: 1 } }));
    }
    if (path.startsWith('/api/v1/topics?') && method === 'GET') {
      return Promise.resolve(okResponse({ topics: [] }));
    }
    if (path === `/api/v1/trees/${TREE_ID}` && method === 'GET') {
      return Promise.resolve(okResponse({ ...TREE, related: null }));
    }
    return Promise.resolve(okResponse({}));
  });
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('NodesPage — bulk merge', () => {
  it('enables Merge at 2 selected rows and disables it at 1', async () => {
    mount();
    await settle();

    await checkRow(0);
    expect(mergeButton().disabled).toBe(true);
    expect(mergeButton().title).toContain('at least 2');

    await checkRow(1); // now two rows are checked
    expect(mergeButton().disabled).toBe(false);
  });

  it('opens the merge dialog when Merge is clicked', async () => {
    mount();
    await settle();
    await checkRow(0);
    await checkRow(1);

    expect(q('[data-testid="merge-dialog"]')).toBeNull();
    await act(async () => mergeButton().click());
    await settle();

    const dialog = q('[data-testid="merge-dialog"]');
    expect(dialog).not.toBeNull();
    // The dialog names what is about to be merged.
    const sources = q('[data-testid="merge-sources"]');
    expect(sources?.querySelectorAll('li').length).toBe(2);
    expect(dialog?.textContent).toContain('2 nodes');
  });

  it('posts the §3.2 body to the tree-scoped route and shows the new synthesis node', async () => {
    mount();
    await settle();
    await checkRow(0);
    await checkRow(1);
    await act(async () => mergeButton().click());
    await settle();

    const textarea = q('[data-testid="merge-content"]') as HTMLTextAreaElement;
    const setValue = Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype,
      'value',
    )!.set!;
    await act(async () => {
      setValue.call(textarea, 'Both approaches converge on CTEs.');
      textarea.dispatchEvent(new Event('input', { bubbles: true }));
    });

    await act(async () => {
      (q('[data-testid="merge-submit"]') as HTMLButtonElement).click();
      await Promise.resolve();
    });
    await settle();

    // Exactly one request, to the ONLY merge route, with the §3.2 body.
    expect(mergePosts).toHaveLength(1);
    expect(mergePosts[0].path).toBe(`/api/v1/trees/${TREE_ID}/merge`);
    expect(mergePosts[0].body).toEqual({
      source_node_ids: [NODE_A, NODE_B],
      content: 'Both approaches converge on CTEs.',
      content_format: 'markdown',
    });

    // Dialog closed, selection cleared (the bar unmounts at size 0)…
    expect(q('[data-testid="merge-dialog"]')).toBeNull();
    expect(q('[data-testid="bulk-action-bar"]')).toBeNull();

    // …and the synthesis node is in the list.
    const ids = qa('[data-testid="node-id-link"]').map((el) => el.title);
    expect(ids).toContain(SYNTH_ID);
    expect(ids).toHaveLength(NODES.length + 1);
  });

  it('keeps the dialog and the selection when the server rejects the merge', async () => {
    mergeResponse = () =>
      errorResponse(400, 'SOURCE_TARGET_OVERLAP', 'target parent is one of the source nodes');

    mount();
    await settle();
    await checkRow(0);
    await checkRow(1);
    await act(async () => mergeButton().click());
    await settle();

    await act(async () => {
      (q('[data-testid="merge-submit"]') as HTMLButtonElement).click();
      await Promise.resolve();
    });
    await settle();

    expect(mergePosts).toHaveLength(1);
    // The dialog stays open with the translated failure inside it…
    expect(q('[data-testid="merge-dialog"]')).not.toBeNull();
    const err = q('[data-testid="merge-error"]');
    expect(err).not.toBeNull();
    expect(err?.textContent).toContain('deselect the root node');
    // The §3.3 code surfaces too — `apiPost` keeps only the message, so
    // the code is recognised from it and shown for a bug report.
    expect(err?.textContent).toContain('SOURCE_TARGET_OVERLAP');

    // …and the selection survives, so the user can fix and retry.
    expect(q('[data-testid="bulk-action-bar"]')).not.toBeNull();
    const checked = qa('[data-testid="node-row-checkbox"]').filter(
      (el) => (el as HTMLInputElement).checked,
    );
    expect(checked).toHaveLength(2);
  });

  it('refuses to open the dialog for a 1-node selection', async () => {
    mount();
    await settle();
    await checkRow(0);

    // The button is disabled, so this is the handler-level guard: it must
    // not depend on the button for correctness.
    await act(async () => mergeButton().click());
    await settle();
    expect(q('[data-testid="merge-dialog"]')).toBeNull();
  });
});
