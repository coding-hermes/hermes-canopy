/**
 * Component tests — AgentsPage (SPEC-023-UI-003)
 *
 * Pins the wiring of the agent roster surface:
 *  - renders the roster list from GET /agents (name, tier, trust score)
 *  - selecting an agent loads the detail (GET /agents/{id}) with the
 *    trust timeline
 *  - a failing roster endpoint surfaces an inline error
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import AgentsPage from '../../pages/AgentsPage.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Fixtures ──────────────────────────────────────────────────────────

const AGENTS = [
  {
    id: 'agent-helix',
    name: 'helix-foreman',
    tier: 'veteran',
    trust_score: 0.94,
    capabilities: { 'go-feature': { success: 120, total: 135 } },
    incidents: 2,
    last_active: '2026-08-09T12:00:00Z',
  },
  {
    id: 'agent-codex',
    name: 'codex-worker',
    tier: 'established',
    trust_score: 0.81,
    capabilities: { typescript: { success: 60, total: 75 } },
    incidents: 1,
    last_active: '2026-08-09T11:30:00Z',
  },
  {
    id: 'agent-kimi',
    name: 'kimi-scout',
    tier: 'provisional',
    trust_score: 0.58,
    capabilities: { investigation: { success: 9, total: 16 } },
    incidents: 0,
    last_active: '2026-08-09T10:15:00Z',
  },
];

const HELIX_DETAIL = {
  ...AGENTS[0],
  trust_history: [
    { score: 0.7, at: '2026-06-01T00:00:00Z' },
    { score: 0.85, at: '2026-07-01T00:00:00Z' },
    { score: 0.94, at: '2026-08-01T00:00:00Z' },
  ],
};

function okResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    text: () => Promise.resolve(JSON.stringify(body)),
    json: () => Promise.resolve(body),
  } as Response;
}

function errorResponse(status: number, message: string): Response {
  return {
    ok: false,
    status,
    text: () =>
      Promise.resolve(JSON.stringify({ error: { code: 'X', message } })),
    json: () => Promise.resolve({ error: { code: 'X', message } }),
  } as Response;
}

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;

function mount() {
  act(() => {
    root.render(
      createElement(
        MemoryRouter,
        { initialEntries: ['/agents'] },
        createElement(AgentsPage),
      ),
    );
  });
}

async function settle(n = 3): Promise<void> {
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

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const path = url.replace(/^https?:\/\/[^/]+/, '');
    // GET /agents — roster list
    if (path === '/api/v1/agents' && (!init || init.method === 'GET')) {
      return Promise.resolve(okResponse(AGENTS));
    }
    // GET /agents/{id} — detail
    if (
      path.startsWith('/api/v1/agents/') &&
      !path.endsWith('/agents') &&
      (!init || init.method === 'GET')
    ) {
      const id = path.split('/').pop();
      if (id === 'agent-helix') {
        return Promise.resolve(okResponse(HELIX_DETAIL));
      }
      const base = AGENTS.find((a) => a.id === id);
      if (base) {
        return Promise.resolve(
          okResponse({ ...base, trust_history: [] }),
        );
      }
      return Promise.resolve(errorResponse(404, 'agent not found'));
    }
    return Promise.reject(new Error(`unexpected fetch: ${url}`));
  });
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ─── Roster list ───────────────────────────────────────────────────────

describe('AgentsPage — roster list', () => {
  it('renders the seeded agents from GET /agents', async () => {
    mount();
    await settle();
    const rail = q('[data-testid="agent-roster"]');
    expect(rail).not.toBeNull();
    // All three agent names should appear in the roster rail.
    expect(rail?.textContent).toContain('helix-foreman');
    expect(rail?.textContent).toContain('codex-worker');
    expect(rail?.textContent).toContain('kimi-scout');
  });

  it('shows the tier and trust score for each agent', async () => {
    mount();
    await settle();
    const rail = q('[data-testid="agent-roster"]');
    // Tier labels are rendered.
    expect(rail?.textContent).toContain('Veteran');
    expect(rail?.textContent).toContain('Established');
    expect(rail?.textContent).toContain('Provisional');
    // Trust scores as percentages.
    expect(rail?.textContent).toContain('94.0%');
    expect(rail?.textContent).toContain('58.0%');
  });

  it('surfaces a list error when the agents endpoint fails', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(500, 'boom')),
    );
    mount();
    await settle();
    const err = q('[data-testid="agents-error"]');
    expect(err).not.toBeNull();
    expect(err?.textContent).toContain('boom');
  });
});

// ─── Detail ────────────────────────────────────────────────────────────

describe('AgentsPage — agent detail', () => {
  it('auto-selects the first agent and renders its detail with trust timeline', async () => {
    mount();
    await settle();
    // First agent (helix-foreman) is auto-selected → detail fetched.
    const detail = q('[data-testid="agent-detail"]');
    expect(detail).not.toBeNull();
    // Detail header shows the agent name.
    expect(detail?.textContent).toContain('helix-foreman');
    // Trust timeline rendered with ≥1 bar.
    const timeline = q('[data-testid="trust-timeline"]');
    expect(timeline).not.toBeNull();
    // Capabilities rendered.
    const caps = qa('[data-testid="agent-capability"]');
    expect(caps.length).toBeGreaterThanOrEqual(1);
    expect(detail?.textContent).toContain('go-feature');
  });

  it('loads a different agent detail when selecting another roster row', async () => {
    mount();
    await settle();
    // Click the "codex-worker" row.
    const codexBtn = qa('button').find(
      (b) => (b.textContent ?? '').includes('codex-worker'),
    );
    expect(codexBtn).toBeDefined();
    await act(async () => {
      codexBtn!.click();
      await Promise.resolve();
    });
    await settle();
    const detail = q('[data-testid="agent-detail"]');
    expect(detail?.textContent).toContain('codex-worker');
    expect(detail?.textContent).toContain('typescript');
  });

  it('renders the trust timeline scores', async () => {
    mount();
    await settle();
    const timeline = q('[data-testid="trust-timeline"]');
    // helix-foreman has a 0.94 latest score → should show "94.0%".
    expect(timeline?.textContent).toContain('94.0%');
  });
});
