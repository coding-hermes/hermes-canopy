/**
 * Component tests — Trees page count inflection (UI-08)
 *
 * A tree card used to render `{tree.node_count} nodes` / `{tree.member_count}
 * members`, so a single-member tree read "1 members" and a single-node tree
 * read "1 nodes". Both now go through `countLabel` from lib/pluralize.
 *
 * This pins the visible wire keys the page reads (`node_count`,
 * `member_count` — snake_case, as internal/service marshals them) at both
 * ends of the inflection: count 1 singular, count > 1 plural.
 *
 * Driven with React 19's `act` + `react-dom/client` inside a MemoryRouter,
 * matching pages/__tests__/TreesPageOnboarding.test.tsx.
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

const SOLO_TREE = {
  id: '0191a8b2-7fff-7000-9000-000000000011',
  title: 'Solo tree',
  description: '',
  node_count: 1,
  member_count: 1,
  root_node_id: '0191a8b2-7fff-7000-9000-000000000012',
  created_at: '2026-09-18T20:00:00Z',
  updated_at: '2026-09-18T20:00:00Z',
  role: 'owner',
  owner_id: '00000000-0000-0000-0000-000000000001',
  owner_display_name: 'Dev',
};

const SHARED_TREE = {
  id: '0191a8b2-7fff-7000-9000-000000000021',
  title: 'Shared tree',
  description: '',
  node_count: 6,
  member_count: 2,
  root_node_id: '0191a8b2-7fff-7000-9000-000000000022',
  created_at: '2026-09-18T20:00:00Z',
  updated_at: '2026-09-18T20:00:00Z',
  role: 'owner',
  owner_id: '00000000-0000-0000-0000-000000000001',
  owner_display_name: 'Dev',
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

async function settle(n = 4): Promise<void> {
  await act(async () => {
    for (let i = 0; i < n; i++) await Promise.resolve();
  });
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const path = url.replace(/^https?:\/\/[^/]+/, '');
    const method = init?.method ?? 'GET';
    if (path.startsWith('/api/v1/trees?') && method === 'GET') {
      return Promise.resolve(
        okResponse({
          trees: [SOLO_TREE, SHARED_TREE],
          pagination: { nextCursor: null, hasMore: false, total: 2, limit: 50 },
        }),
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

// ─── Tests ─────────────────────────────────────────────────────────────

describe('TreesPage — count inflection (UI-08)', () => {
  it('renders "1 node" / "1 member" for a single-node, single-member tree', async () => {
    mount();
    await settle();

    const text = container.textContent ?? '';
    expect(text).toContain('Solo tree');
    // textContent concatenates sibling spans ("1 node1 member"), so the
    // assertions key on the rendered words rather than \b boundaries.
    expect(text).toContain('1 node');
    expect(text).toContain('1 member');
    expect(text).not.toContain('1 nodes');
    expect(text).not.toContain('1 members');
  });

  it('renders "6 nodes" / "2 members" for a larger tree', async () => {
    mount();
    await settle();

    const text = container.textContent ?? '';
    expect(text).toContain('Shared tree');
    expect(text).toContain('6 nodes');
    expect(text).toContain('2 members');
  });

  it('inflects the header total ("2 conversation trees")', async () => {
    mount();
    await settle();

    expect(container.textContent).toContain('2 conversation trees');
  });

  it('never renders a "1 <plural>" count anywhere on the page', async () => {
    mount();
    await settle();

    expect(container.textContent).not.toMatch(
      /1 (nodes|members|cards|topics|trees|edges|runs|segments|results|sources|tokens|chars|steps)/,
    );
  });
});
