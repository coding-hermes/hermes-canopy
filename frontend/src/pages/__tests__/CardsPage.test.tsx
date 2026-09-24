import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import CardsPage from '../../pages/CardsPage.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const TREE_ID = '0191a8b2-7fff-7000-9000-000000000001';
const NODE_A = '0191a8b2-7fff-7000-9000-000000000101';
const NODE_B = '0191a8b2-7fff-7000-9000-000000000102';
const CARD_ID = '0191a8b2-7fff-7000-9000-000000000201';

const TREE = { id: TREE_ID, title: 'Cards tree' };
const NODES = [
  {
    id: NODE_A,
    content: 'Review the deployment checklist and verify the release candidate.',
    sequenceNum: 1,
  },
  {
    id: NODE_B,
    content: 'Follow-up about the card lifecycle controls.',
    sequenceNum: 2,
  },
];

function card(status: 'active' | 'dismissed' | 'archived' = 'active', revision = 7) {
  return {
    id: CARD_ID,
    tree_id: TREE_ID,
    node_id: NODE_A,
    app_id: 'canopy',
    type: 'compact',
    status,
    revision,
    context_hash: '',
    data: { title: 'Release checklist' },
    actions: [],
    last_event_seq: revision,
    created_at: '2026-09-20T12:00:00Z',
  };
}

function okResponse(body: unknown, status = 200): Response {
  return {
    ok: true,
    status,
    text: () => Promise.resolve(JSON.stringify(body)),
    json: () => Promise.resolve(body),
  } as Response;
}

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;
let cardState: ReturnType<typeof card>;
let patches: Array<{ path: string; body: unknown; ifMatch: string | null }>;

function requestBody(init?: RequestInit): unknown {
  return typeof init?.body === 'string' ? JSON.parse(init.body) : null;
}

function requestHeader(init: RequestInit | undefined, name: string): string | null {
  return new Headers(init?.headers).get(name);
}

async function settle(n = 5): Promise<void> {
  await act(async () => {
    for (let i = 0; i < n; i += 1) await Promise.resolve();
  });
}

function query<T extends Element>(selector: string): T | null {
  return container.querySelector<T>(selector);
}

function buttonByLabel(label: string): HTMLButtonElement {
  const button = query<HTMLButtonElement>(`button[aria-label="${label}"]`);
  expect(button).not.toBeNull();
  return button as HTMLButtonElement;
}

async function selectTree(): Promise<void> {
  const select = query<HTMLSelectElement>('#cards-tree-select');
  expect(select).not.toBeNull();
  await act(async () => {
    (select as HTMLSelectElement).value = TREE_ID;
    select?.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await settle();
}

function mount(): void {
  act(() => {
    root.render(
      createElement(
        MemoryRouter,
        { initialEntries: ['/cards'] },
        createElement(CardsPage),
      ),
    );
  });
}

beforeEach(() => {
  cardState = card();
  patches = [];
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);

  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const path = url.replace(/^https?:\/\/[^/]+/, '');
    const method = init?.method ?? 'GET';

    if (path === '/api/v1/trees?limit=100' && method === 'GET') {
      return Promise.resolve(okResponse({ trees: [TREE], pagination: { total: 1 } }));
    }
    if (path === `/api/v1/trees/${TREE_ID}/nodes` && method === 'GET') {
      return Promise.resolve(okResponse({ nodes: NODES }));
    }
    if (path.startsWith('/api/v1/cards?') && method === 'GET') {
      return Promise.resolve(okResponse({ cards: [cardState] }));
    }
    if (path === `/api/v1/cards/${CARD_ID}` && method === 'PATCH') {
      const body = requestBody(init) as { status: 'active' | 'dismissed' | 'archived' };
      patches.push({ path, body, ifMatch: requestHeader(init, 'If-Match') });
      const nextRevision = cardState.revision + 1;
      cardState = { ...cardState, status: body.status, revision: nextRevision, last_event_seq: nextRevision };
      return Promise.resolve(okResponse(cardState));
    }
    throw new Error(`Unexpected ${method} ${path}`);
  });
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CardsPage', () => {
  it('loads the selected tree nodes and presents identifiable picker options', async () => {
    mount();
    await settle();
    await selectTree();

    const newCard = [...container.querySelectorAll('button')].find(
      (button) => button.textContent?.trim() === 'New Card',
    );
    expect(newCard).not.toBeUndefined();
    await act(async () => newCard?.click());
    await settle();

    const picker = query<HTMLSelectElement>('[data-testid="create-card-node-select"]');
    expect(picker).not.toBeNull();
    expect(picker?.options).toHaveLength(3);
    expect(picker?.options[1].value).toBe(NODE_A);
    expect(picker?.options[1].textContent).toContain('Review the deployment checklist');
    expect(picker?.options[1].textContent).toContain(NODE_A.slice(0, 8));
    expect(picker?.options[2].value).toBe(NODE_B);
    expect(fetchMock).toHaveBeenCalledWith(
      `/api/v1/trees/${TREE_ID}/nodes`,
      undefined,
    );
  });

  it('PATCHes dismiss, restore, and archive with the current revision and updates controls', async () => {
    mount();
    await settle();
    await selectTree();

    await act(async () => buttonByLabel('Dismiss card').click());
    await settle();
    expect(patches[0]).toEqual({
      path: `/api/v1/cards/${CARD_ID}`,
      body: { status: 'dismissed' },
      ifMatch: '7',
    });
    expect(query('button[aria-label="Dismiss card"]')).toBeNull();
    expect(query('button[aria-label="Restore card"]')).not.toBeNull();

    await act(async () => buttonByLabel('Restore card').click());
    await settle();
    expect(patches[1]).toEqual({
      path: `/api/v1/cards/${CARD_ID}`,
      body: { status: 'active' },
      ifMatch: '8',
    });
    expect(query('button[aria-label="Dismiss card"]')).not.toBeNull();

    await act(async () => buttonByLabel('Archive card').click());
    await settle();
    expect(patches[2]).toEqual({
      path: `/api/v1/cards/${CARD_ID}`,
      body: { status: 'archived' },
      ifMatch: '9',
    });
    expect(query('button[aria-label="Dismiss card"]')).toBeNull();
    expect(query('button[aria-label="Restore card"]')).toBeNull();
    expect(query('button[aria-label="Archive card"]')).toBeNull();
  });
});
