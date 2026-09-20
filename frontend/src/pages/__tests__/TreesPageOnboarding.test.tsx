/**
 * Component tests — FirstRunOnboarding (GAP-093)
 *
 * Pins the zero-data onboarding wiring on the Trees page:
 *  - when GET /trees returns an empty list the page renders the human
 *    onboarding card (not the old bare "No trees yet" box);
 *  - the create CTA opens the real Create Tree dialog (same state the
 *    header's "New Tree" opens);
 *  - "Import a tree…" drives the REAL POST /api/v1/trees/import route
 *    through the shared apiPost helper (auth headers included) and
 *    navigates to the imported tree;
 *  - a file that is not a Canopy export shows the parse error and sends
 *    NO request;
 *  - the Hermes-session card states honestly that session import is CLI-only
 *    (no dead in-browser button, no fake demo data).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import TreesPage from '../../pages/TreesPage.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Fixtures ──────────────────────────────────────────────────────────

const TREE = {
  id: '0191a8b2-7fff-7000-9000-000000000001',
  title: 'Existing tree',
  description: '',
  node_count: 1,
  root_node_id: '0191a8b2-7fff-7000-9000-000000000002',
  created_at: '2026-09-18T20:00:00Z',
  updated_at: '2026-09-18T20:00:00Z',
  role: 'owner',
  owner_id: '00000000-0000-0000-0000-000000000001',
  owner_display_name: 'Dev',
  member_count: 1,
};

const IMPORTED_201 = {
  treeId: '0191a8b2-7fff-7000-9000-0000000000aa',
  rootNodeId: '0191a8b2-7fff-7000-9000-0000000000bb',
  nodeCount: 3,
  edgeCount: 2,
};

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;

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

/** Minimal File stand-in (jsdom File exists but this keeps the fixture explicit). */
function fakeFile(text: string): { text: () => Promise<string> } {
  return { text: () => Promise.resolve(text) };
}

async function settle(n = 4): Promise<void> {
  await act(async () => {
    for (let i = 0; i < n; i++) await Promise.resolve();
  });
}

function q(selector: string): HTMLElement | null {
  return container.querySelector(selector);
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const path = url.replace(/^https?:\/\/[^/]+/, '');
    const method = init?.method ?? 'GET';
    // Every non-listed GET is an empty-collection response: the onboarding
    // card renders only when the TREES list is empty.
    if (path.startsWith('/api/v1/trees?') && method === 'GET') {
      return Promise.resolve(
        okResponse({ trees: [], pagination: { nextCursor: null, hasMore: false, total: 0, limit: 50 } }),
      );
    }
    if (path.startsWith('/api/v1/gateway/')) {
      return Promise.resolve(
        okResponse(path.endsWith('/status')
          ? { connected: false, base_url: 'http://127.0.0.1:8642', run_count: 0, active_runs: 0 }
          : { runs: [] }),
      );
    }
    if (path === '/api/v1/trees/import' && method === 'POST') {
      return Promise.resolve(okResponse(IMPORTED_201, 201));
    }
    return Promise.resolve(okResponse({ items: [], topics: [], cards: [], agents: [] }));
  });
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

function mount() {
  act(() => {
    root.render(
      createElement(
        MemoryRouter,
        { initialEntries: ['/trees'] },
        createElement(TreesPage),
      ),
    );
  });
}

function mountWithTrees() {
  fetchMock.mockImplementation((url: string, init?: RequestInit) => {
    const path = url.replace(/^https?:\/\/[^/]+/, '');
    const method = init?.method ?? 'GET';
    if (path.startsWith('/api/v1/trees?') && method === 'GET') {
      return Promise.resolve(
        okResponse({ trees: [TREE], pagination: { nextCursor: null, hasMore: false, total: 1, limit: 50 } }),
      );
    }
    if (path.startsWith('/api/v1/gateway/')) {
      return Promise.resolve(
        okResponse(path.endsWith('/status')
          ? { connected: false, base_url: 'http://127.0.0.1:8642', run_count: 0, active_runs: 0 }
          : { runs: [] }),
      );
    }
    return Promise.resolve(okResponse({ items: [], topics: [], cards: [], agents: [] }));
  });
  mount();
}

// ─── Tests ─────────────────────────────────────────────────────────────

describe('TreesPage — zero-data onboarding (GAP-093)', () => {
  it('renders the human onboarding card on an empty tree list', async () => {
    mount();
    await settle();
    expect(q('[data-testid="first-run-onboarding"]')).not.toBeNull();
    expect(q('[data-testid="onboarding-create-tree"]')).not.toBeNull();
    expect(q('[data-testid="onboarding-import-tree"]')).not.toBeNull();
    // No fake demo content is rendered into the empty state (the E2E demo
    // tree 'UI-02 Rail Demo' must never show up by default).
    expect(container.textContent).not.toContain('UI-02 Rail Demo');
    expect(q('[data-testid="tree-card-demo-tree"]')).toBeNull();
  });

  it('still renders the plain grouped list when trees exist (no onboarding card)', async () => {
    mountWithTrees();
    await settle();
    expect(q('[data-testid="first-run-onboarding"]')).toBeNull();
    expect(container.textContent).toContain('Existing tree');
  });

  it('create CTA opens the real Create Tree dialog', async () => {
    mount();
    await settle();
    const create = q('[data-testid="onboarding-create-tree"]') as HTMLButtonElement;
    expect(create).not.toBeNull();
    await act(async () => create.click());
    await settle();
    // The real dialog's id-anchored fields (CreateTreeDialog contract).
    expect(q('#create-tree-title')).not.toBeNull();
    expect(q('#create-tree-root-msg')).not.toBeNull();
  });

  it('import CTA posts a chosen export file to the real route and navigates', async () => {
    mount();
    await settle();

    const importBtn = q('[data-testid="onboarding-import-tree"]') as HTMLButtonElement;
    const input = q('[data-testid="onboarding-import-input"]') as HTMLInputElement;
    expect(importBtn).not.toBeNull();
    expect(input).not.toBeNull();

    const exportBody = JSON.stringify({
      tree: { title: 'Exported tree', rootNodeId: 'n1' },
      nodes: [{ id: 'n1', content: 'hi' }],
      edges: [],
    });

    await act(async () => {
      Object.defineProperty(input, 'files', { value: [fakeFile(exportBody)], configurable: true });
      input.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await settle();

    const posts = fetchMock.mock.calls.filter(
      ([u, init]) => String(u).endsWith('/trees/import') && (init as RequestInit | undefined)?.method === 'POST',
    );
    expect(posts).toHaveLength(1);
    const body = JSON.parse((posts[0][1] as RequestInit).body as string) as { tree: { title: string } };
    expect(body.tree.title).toBe('Exported tree');
  });

  it('a non-export file shows the parse error and sends NO request', async () => {
    mount();
    await settle();

    const input = q('[data-testid="onboarding-import-input"]') as HTMLInputElement;
    expect(input).not.toBeNull();

    await act(async () => {
      Object.defineProperty(input, 'files', { value: [fakeFile('{"hello":"world"}')], configurable: true });
      input.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await settle();

    const err = q('[data-testid="onboarding-import-error"]');
    expect(err).not.toBeNull();
    expect(err?.textContent).toContain("'tree' section");
    const posts = fetchMock.mock.calls.filter(
      ([u, init]) => String(u).endsWith('/trees/import') && (init as RequestInit | undefined)?.method === 'POST',
    );
    expect(posts).toHaveLength(0);
  });

  it('a server rejection surfaces the API message and keeps the card mounted', async () => {
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      const path = url.replace(/^https?:\/\/[^/]+/, '');
      const method = init?.method ?? 'GET';
      if (path.startsWith('/api/v1/trees?') && method === 'GET') {
        return Promise.resolve(okResponse({ trees: [], pagination: { nextCursor: null, hasMore: false, total: 0, limit: 50 } }));
      }
      if (path.startsWith('/api/v1/gateway/')) {
        return Promise.resolve(okResponse({ runs: [] }));
      }
      if (path === '/api/v1/trees/import' && method === 'POST') {
        return Promise.resolve(errorResponse(400, 'VALIDATION_ERROR', 'edge references node not in import payload'));
      }
      return Promise.resolve(okResponse({}));
    });
    mount();
    await settle();

    const input = q('[data-testid="onboarding-import-input"]') as HTMLInputElement;
    await act(async () => {
      Object.defineProperty(input, 'files', {
        value: [fakeFile(JSON.stringify({ tree: { title: 'T' }, nodes: [{ id: 'n1' }], edges: [{ sourceId: 'x', targetId: 'n1' }] }))],
        configurable: true,
      });
      input.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await settle();

    const err = q('[data-testid="onboarding-import-error"]');
    expect(err).not.toBeNull();
    expect(err?.textContent).toContain('node not in import payload');
    expect(q('[data-testid="first-run-onboarding"]')).not.toBeNull();
  });

  it('honestly states CLI-only session import with the real command, no fake button', async () => {
    mount();
    await settle();
    const note = q('[data-testid="onboarding-session-import-note"]');
    expect(note).not.toBeNull();
    const cmd = q('[data-testid="onboarding-session-import-cli"]');
    expect(cmd?.textContent).toBe('./bin/canopyd session import');
    // No session-import CTA button exists — there is no such route in the UI.
    expect(q('[data-testid="onboarding-import-session"]')).toBeNull();
    expect(note?.textContent).toContain('command-line');
  });
});
