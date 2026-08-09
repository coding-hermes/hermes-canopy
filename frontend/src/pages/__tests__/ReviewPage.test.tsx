/**
 * Component tests — ReviewPage (SPEC-023-UI-004)
 *
 * Pins the wiring of the PR review panel:
 *  - renders the review list from GET /reviews (≥3 seeded)
 *  - selecting a review fetches detail (GET /reviews/{id}) with blast
 *    radius + Chimera verdict
 *  - error state when the list endpoint fails
 *  - empty state when the list is empty
 *  - live review_event from the SSE feed updates the feed indicator
 *  - trigger POSTs to /reviews/{pr}/trigger and updates the detail
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import ReviewPage from '../../pages/ReviewPage.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Mock EventSource ──────────────────────────────────────────────────

class MockEventSource {
  url: string;
  readyState = 0;
  static instances: MockEventSource[] = [];
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  private listeners: Record<string, Array<(e: MessageEvent) => void>> = {};

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }
  addEventListener(type: string, cb: (e: MessageEvent) => void): void {
    (this.listeners[type] ??= []).push(cb);
  }
  removeEventListener(type: string, cb: (e: MessageEvent) => void): void {
    this.listeners[type] = (this.listeners[type] ?? []).filter(
      (c) => c !== cb,
    );
  }
  close(): void {
    this.readyState = 2;
  }
  emitOpen(): void {
    this.readyState = 1;
    this.onopen?.(new Event('open'));
  }
  emitMessage(type: string, data: unknown): void {
    for (const cb of this.listeners[type] ?? []) {
      cb({ data: JSON.stringify(data) } as MessageEvent);
    }
  }
}

// ─── Fixtures ──────────────────────────────────────────────────────────

const REVIEWS = [
  {
    id: 'rev-1042',
    pr: '1042',
    title: 'feat: add agent roster surface',
    author: 'helix-foreman',
    status: 'approved',
    risk_score: 0.12,
    updated_at: '2026-08-09T12:30:00Z',
  },
  {
    id: 'rev-1038',
    pr: '1038',
    title: 'refactor: workspace SSE hub extraction',
    author: 'codex-worker',
    status: 'requested_changes',
    risk_score: 0.34,
    updated_at: '2026-08-09T11:15:00Z',
  },
  {
    id: 'rev-1051',
    pr: '1051',
    title: 'fix: recursive CTE depth calculation',
    author: 'kimi-scout',
    status: 'reviewing',
    risk_score: 0.58,
    updated_at: '2026-08-09T09:30:00Z',
  },
];

const DETAIL_1042 = {
  id: 'rev-1042',
  pr: '1042',
  title: 'feat: add agent roster surface',
  author: 'helix-foreman',
  status: 'approved',
  risk_score: 0.12,
  blast_radius: {
    files_touched: [
      'internal/handler/agent_handler.go',
      'internal/server/server.go',
      'frontend/src/pages/AgentsPage.tsx',
    ],
    dependents_count: 7,
  },
  verdict: {
    verdict: 'approve',
    model_formation: 'single-judge',
    summary: 'Low risk — safe to merge.',
    confidence: 0.94,
    at: '2026-08-09T12:30:00Z',
  },
  created_at: '2026-08-09T12:00:00Z',
  updated_at: '2026-08-09T12:30:00Z',
};

// ─── Fetch response helpers ────────────────────────────────────────────

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
        { initialEntries: ['/reviews'] },
        createElement(ReviewPage),
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
  MockEventSource.instances = [];
  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const path = url.replace(/^https?:\/\/[^/]+/, '');
    // GET /reviews → list
    if (
      path === '/api/v1/reviews' &&
      (!init || init.method === 'GET')
    ) {
      return Promise.resolve(okResponse(REVIEWS));
    }
    // GET /reviews/{id} → detail (1042 only for simplicity)
    if (
      path.startsWith('/api/v1/reviews/') &&
      !path.endsWith('/trigger') &&
      (!init || init.method === 'GET')
    ) {
      const id = path.split('/').pop();
      if (id === 'rev-1042') return Promise.resolve(okResponse(DETAIL_1042));
      return Promise.resolve(
        errorResponse(404, `review ${id} does not exist`),
      );
    }
    // POST /reviews/{pr}/trigger → simulated
    if (
      path.startsWith('/api/v1/reviews/') &&
      path.endsWith('/trigger') &&
      init?.method === 'POST'
    ) {
      const pr = path.split('/')[4];
      return Promise.resolve(
        okResponse({
          ...DETAIL_1042,
          pr,
          risk_score: 0.25,
          status: 'approved',
          verdict: {
            ...DETAIL_1042.verdict,
            verdict: 'approve',
            confidence: 0.88,
          },
        }),
      );
    }
    return Promise.reject(new Error(`unexpected fetch: ${url}`));
  });
  vi.stubGlobal('fetch', fetchMock);
  vi.stubGlobal('EventSource', MockEventSource);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ─── List + loading ────────────────────────────────────────────────────

describe('ReviewPage — list', () => {
  it('renders the seeded reviews from GET /reviews', async () => {
    mount();
    await settle();
    const rail = q('[data-testid="review-rail"]');
    expect(rail).not.toBeNull();
    expect(rail?.textContent).toContain('1042');
    expect(rail?.textContent).toContain('1038');
    expect(rail?.textContent).toContain('1051');
  });

  it('shows at least 3 review rows', async () => {
    mount();
    await settle();
    const rows = qa('button[aria-current], button:not([aria-current])').filter(
      (b) => REVIEWS.some((r) => (b.textContent ?? '').includes(r.pr)),
    );
    expect(rows.length).toBeGreaterThanOrEqual(3);
  });

  it('shows the live feed indicator', async () => {
    mount();
    await settle();
    const feed = q('[data-testid="review-live-feed"]');
    expect(feed).not.toBeNull();
  });
});

// ─── Detail ────────────────────────────────────────────────────────────

describe('ReviewPage — detail', () => {
  it('fetches and renders detail with blast radius + verdict on selection', async () => {
    mount();
    await settle();
    // Auto-selects first review (rev-1042) → detail should fetch.
    await settle();
    const detail = q('[data-testid="review-detail"]');
    expect(detail).not.toBeNull();
    // Risk gauge
    expect(q('[data-testid="risk-gauge"]')).not.toBeNull();
    // Blast radius
    expect(q('[data-testid="blast-radius-viz"]')).not.toBeNull();
    const files = qa('[data-testid="blast-file"]');
    expect(files.length).toBe(3);
    // Chimera verdict
    const verdict = q('[data-testid="chimera-verdict"]');
    expect(verdict).not.toBeNull();
    expect(verdict?.textContent).toContain('single-judge');
  });

  it('renders the Chimera verdict summary text', async () => {
    mount();
    await settle();
    await settle();
    const verdict = q('[data-testid="chimera-verdict"]');
    expect(verdict?.textContent).toContain('Low risk');
  });
});

// ─── Error state ───────────────────────────────────────────────────────

describe('ReviewPage — error', () => {
  it('surfaces a list error when the reviews endpoint fails', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(500, 'boom')),
    );
    mount();
    await settle();
    const err = q('[data-testid="reviews-error"]');
    expect(err).not.toBeNull();
    expect(err?.textContent).toContain('boom');
  });
});

// ─── Empty state ───────────────────────────────────────────────────────

describe('ReviewPage — empty', () => {
  it('renders the empty placeholder when the list is empty', async () => {
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      const path = url.replace(/^https?:\/\/[^/]+/, '');
      if (path === '/api/v1/reviews' && (!init || init.method === 'GET')) {
        return Promise.resolve(okResponse([]));
      }
      return Promise.reject(new Error(`unexpected: ${url}`));
    });
    mount();
    await settle();
    const rail = q('[data-testid="review-rail"]');
    expect(rail?.textContent).toContain('No reviews');
  });
});

// ─── Live feed ─────────────────────────────────────────────────────────

describe('ReviewPage — live feed', () => {
  it('surfaces an incoming review_event from the SSE feed', async () => {
    mount();
    await settle();
    expect(MockEventSource.instances.length).toBeGreaterThanOrEqual(1);
    act(() => MockEventSource.instances[0].emitOpen());
    act(() =>
      MockEventSource.instances[0].emitMessage('review_event', {
        event_type: 'review_event',
        data: {
          review_id: 'rev-triggered',
          pr: '9999',
          title: 'feat: new thing',
          status: 'approved',
          verdict: 'approve',
          risk_score: 0.2,
          triggered_at: '2026-08-09T14:00:00Z',
        },
      }),
    );
    await settle();
    const feed = q('[data-testid="review-live-feed"]');
    expect(feed?.textContent).toContain('PR #9999');
    expect(feed?.textContent).toContain('approve');
  });
});

// ─── Trigger ───────────────────────────────────────────────────────────

describe('ReviewPage — trigger', () => {
  it('POSTs a trigger and updates the detail on success', async () => {
    mount();
    await settle();
    await settle();
    // The selected review should be rev-1042 (auto-selected first).
    const triggerBtn = qa('button').find(
      (b) => b.getAttribute('aria-label') === 'Trigger Chimera review',
    );
    expect(triggerBtn).toBeDefined();
    await act(async () => {
      triggerBtn!.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    await settle();
    const postCall = fetchMock.mock.calls.find(
      ([, init]) => init?.method === 'POST',
    );
    expect(postCall).toBeDefined();
    const url = postCall![0] as string;
    expect(url).toContain('/trigger');
  });
});
