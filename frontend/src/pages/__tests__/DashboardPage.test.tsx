/**
 * Component tests — DashboardPage (GAP-050)
 *
 * Pins the live-gateway dashboard wiring:
 *  - renders the gateway live/offline banner from GET /gateway/status
 *  - lists real runs from GET /gateway/runs with status badges
 *  - the composer POSTs a message and selects the new run
 *  - an approval.request surfaces the approval card with all 4 choices
 *  - the stop control POSTs to the run stop endpoint
 *  - the SSE event feed renders streamed events (deduped)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import DashboardPage from '../DashboardPage.tsx';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Mock EventSource ──────────────────────────────────────────────────

class MockEventSource {
  url: string;
  readyState = 0;
  static instances: MockEventSource[] = [];
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  private listeners: Record<string, Array<(e: MessageEvent) => void>> = {};

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }
  addEventListener(type: string, cb: (e: MessageEvent) => void): void {
    (this.listeners[type] ??= []).push(cb);
  }
  removeEventListener(type: string, cb: (e: MessageEvent) => void): void {
    this.listeners[type] = (this.listeners[type] ?? []).filter((c) => c !== cb);
  }
  close(): void {
    this.readyState = 2;
  }
  emitOpen(): void {
    this.readyState = 1;
    this.onopen?.(new Event('open'));
  }
  emitMessage(type: string, data: unknown): void {
    const cbs = this.listeners[type] ?? [];
    const ev = { data: JSON.stringify(data) } as MessageEvent;
    for (const cb of cbs) cb(ev);
    if (type === 'message' && this.onmessage) {
      this.onmessage(ev);
    }
  }
}

// ─── Fixtures ──────────────────────────────────────────────────────────

const RUN_RUNNING = {
  run_id: 'run_abc',
  session_id: '',
  message: 'hello',
  model: '',
  status: 'running',
  created_at: new Date().toISOString(),
  last_event: 'message.delta',
  events: [],
};

const RUN_APPROVAL = {
  run_id: 'run_appr',
  session_id: '',
  message: 'do something',
  model: '',
  status: 'waiting_for_approval',
  created_at: new Date().toISOString(),
  last_event: 'approval.request',
  events: [
    {
      event: 'approval.request',
      run_id: 'run_appr',
      timestamp: 1,
      command: 'rm -rf /tmp/x',
      choices: ['once', 'session', 'always', 'deny'],
    },
  ],
};

const RUN_DONE = {
  run_id: 'run_done',
  session_id: '',
  message: 'short task',
  model: '',
  status: 'completed',
  created_at: new Date().toISOString(),
  last_event: 'run.completed',
  output: 'ok',
  events: [{ event: 'run.completed', run_id: 'run_done', timestamp: 2, output: 'ok' }],
};

const RUN_DISCONNECTED = {
  run_id: 'run_disconnected',
  session_id: '',
  message: 'stream ended',
  model: '',
  status: 'disconnected',
  created_at: new Date().toISOString(),
  last_event: 'run.stream_closed',
  events: [{ event: 'run.stream_closed', run_id: 'run_disconnected', timestamp: 3 }],
};

// ─── Recent trees fixtures (GAP-094) ───────────────────────────────────

/**
 * Deliberately NOT in last-activity order: the list must sort by
 * `last_activity` desc. TIMES are relative to a fixed "now" so the relative
 * label is deterministic.
 */
const NOW = Date.parse('2026-09-19T12:00:00Z');

function minutesAgo(min: number): string {
  return new Date(NOW - min * 60_000).toISOString();
}

const TREE_STALE = {
  id: 'tree-stale',
  title: 'Stale conversation',
  node_count: 3,
  last_activity: minutesAgo(600),
  created_at: minutesAgo(5000),
};
const TREE_FRESH = {
  id: 'tree-fresh',
  title: 'Fresh conversation',
  node_count: 7,
  last_activity: minutesAgo(2),
  created_at: minutesAgo(300),
};
const TREE_MIDDLE = {
  id: 'tree-middle',
  title: 'Middle conversation',
  node_count: 1,
  last_activity: minutesAgo(45),
  created_at: minutesAgo(1000),
};
/** No live nodes → no last_activity; sorts after every active tree. */
const TREE_QUIET = {
  id: 'tree-quiet',
  title: 'Quiet conversation',
  node_count: 0,
  created_at: minutesAgo(1),
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
/** Trees the /trees branch answers with; tests mutate this per case. */
let treeFixtures: unknown[] = [];
/** Every location the router visited, so navigation can be asserted. */
let visited: Array<{ pathname: string; search: string }> = [];

function routeFetch(url: string): Response {
  if (url.startsWith('/api/v1/gateway/status')) {
    return jsonResponse({ connected: true, base_url: 'http://127.0.0.1:8642', run_count: 3, active_runs: 2 });
  }
  if (url.startsWith('/api/v1/gateway/runs')) {
    if (url.endsWith('/stop')) {
      return jsonResponse({ run_id: 'run_abc', status: 'stopping' });
    }
    if (url.endsWith('/approval')) {
      return jsonResponse({ run_id: 'run_appr', choice: 'once', resolved: true });
    }
    return jsonResponse({ runs: [RUN_RUNNING, RUN_APPROVAL, RUN_DONE, RUN_DISCONNECTED] });
  }
  if (url.startsWith('/api/v1/trees')) {
    return jsonResponse({ trees: treeFixtures });
  }
  return jsonResponse({ error: { message: `unexpected ${url}` } }, 404);
}

function setComposerValue(textarea: HTMLTextAreaElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLTextAreaElement.prototype,
    'value',
  )!.set!;
  setter.call(textarea, value);
  textarea.dispatchEvent(new Event('input', { bubbles: true }));
}

function renderDashboard() {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => {
    root.render(
      createElement(
        MemoryRouter,
        { initialEntries: ['/'] },
        createElement(RouteWatcher),
        createElement(Routes, null, [
          createElement(Route, { key: 'dash', path: '/', element: createElement(DashboardPage) }),
          createElement(Route, {
            key: 'tree',
            path: '/tree/:treeId',
            element: createElement('div', { 'data-testid': 'tree-view-stub' }, 'tree view'),
          }),
        ]),
      ),
    );
  });
}

/** Records every location the router moves to (navigation assertions). */
function RouteWatcher() {
  const location = useLocation();
  visited.push({ pathname: location.pathname, search: location.search });
  return null;
}

async function flushPromises(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  fetchMock.mockReset();
  fetchMock.mockImplementation((url: string) => Promise.resolve(routeFetch(String(url))));
  vi.stubGlobal('fetch', fetchMock);
  MockEventSource.instances = [];
  vi.stubGlobal('EventSource', MockEventSource);
  treeFixtures = [];
  visited = [];
});

afterEach(() => {
  act(() => root.unmount());
  container?.remove();
  vi.unstubAllGlobals();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('DashboardPage', () => {
  it('shows the gateway-live banner and real runs', async () => {
    renderDashboard();
    await flushPromises();
    expect(container.querySelector('[data-testid="gateway-live"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="run-row-run_abc"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="run-row-run_appr"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="run-status-waiting_for_approval"]')).not.toBeNull();
  });

  it('renders the approval card with all choices for a pending approval', async () => {
    renderDashboard();
    await flushPromises();
    // The approval run is not the newest — select it explicitly.
    const row = container.querySelector('[data-testid="run-row-run_appr"]') as HTMLButtonElement;
    act(() => row.click());
    await flushPromises();
    const card = container.querySelector('[data-testid="approval-card"]');
    expect(card).not.toBeNull();
    expect(card?.textContent).toContain('rm -rf /tmp/x');
    for (const choice of ['once', 'session', 'always', 'deny']) {
      expect(container.querySelector(`[data-testid="approval-${choice}"]`)).not.toBeNull();
    }
  });

  it('composer starts a real run and selects it', async () => {
    renderDashboard();
    await flushPromises();

    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      const u = String(url);
      if (init?.method === 'POST') {
        return Promise.resolve(jsonResponse({ run_id: 'run_new', status: 'started' }));
      }
      if (u.startsWith('/api/v1/gateway/runs')) {
        // The refresh after start returns the list including the new run.
        const fresh = { ...RUN_RUNNING, run_id: 'run_new', message: 'start me' };
        return Promise.resolve(jsonResponse({ runs: [fresh, RUN_DONE] }));
      }
      return Promise.resolve(routeFetch(u));
    });

    const input = container.querySelector('[data-testid="composer-input"]') as HTMLTextAreaElement;
    const sendBtn = container.querySelector('[data-testid="composer-send"]') as HTMLButtonElement;

    act(() => setComposerValue(input, 'start me'));

    await act(async () => {
      sendBtn.click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    const postCalls = fetchMock.mock.calls.filter(
      ([url, init]) => String(url) === '/api/v1/gateway/runs' && (init as RequestInit | undefined)?.method === 'POST',
    );
    expect(postCalls.length).toBeGreaterThan(0);
    expect(JSON.parse(String((postCalls[0][1] as RequestInit).body))).toEqual({ message: 'start me' });

    expect(container.querySelector('[data-testid="run-row-run_new"]')).not.toBeNull();
    // The new run is selected → an EventSource opens for its events.
    const es = [...MockEventSource.instances].reverse().find((i) => i.url.includes('/run_new/events'));
    expect(es).toBeTruthy();
  });

  it('streams SSE events into the feed with dedupe', async () => {
    renderDashboard();
    await flushPromises();

    // Select the running run by clicking its row.
    const row = container.querySelector('[data-testid="run-row-run_abc"]') as HTMLButtonElement;
    act(() => row.click());
    await flushPromises();

    const es = MockEventSource.instances.find((i) => i.url.includes('/run_abc/events'));
    expect(es).toBeTruthy();

    act(() => {
      es?.emitOpen();
      es?.emitMessage('message', { event: 'message.delta', run_id: 'run_abc', timestamp: 1, delta: 'Hel' });
      es?.emitMessage('message', { event: 'message.delta', run_id: 'run_abc', timestamp: 2, delta: 'lo' });
      es?.emitMessage('message', { event: 'message.delta', run_id: 'run_abc', timestamp: 1, delta: 'Hel' });
    });

    const feed = container.querySelector('[data-testid="event-feed"]');
    expect(feed).not.toBeNull();
    const transcript = container.querySelector('[data-testid="transcript"]');
    expect(transcript?.textContent).toBe('Hello');
  });

  it('treats a disconnected run as terminal and does not offer stop or approval controls', async () => {
    renderDashboard();
    await flushPromises();

    expect(container.querySelector('[data-testid="run-status-disconnected"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="stop-run_run_disconnected"]')).toBeNull();
    expect(container.querySelector('[data-testid="approval-card"]')).toBeNull();
  });

  it('stop control POSTs to the stop endpoint', async () => {
    renderDashboard();
    await flushPromises();
    const stop = container.querySelector('[data-testid="stop-run_abc"]') as HTMLElement;
    expect(stop).not.toBeNull();
    const stopCalls = fetchMock.mock.calls.filter(
      ([url]) => String(url) === '/api/v1/gateway/runs/run_abc/stop',
    );
    expect(stopCalls).toHaveLength(0);
    await act(async () => {
      stop.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    const after = fetchMock.mock.calls.filter(
      ([url]) => String(url) === '/api/v1/gateway/runs/run_abc/stop',
    );
    expect(after.length).toBeGreaterThan(0);
  });
});

// ─── Recent trees resume affordance (GAP-094) ──────────────────────────

describe('DashboardPage — recent trees (GAP-094)', () => {
  it('renders the recent list sorted by last activity desc with node counts', async () => {
    treeFixtures = [TREE_STALE, TREE_QUIET, TREE_FRESH, TREE_MIDDLE];
    renderDashboard();
    await flushPromises();

    const list = container.querySelector('[data-testid="recent-trees-list"]');
    expect(list).not.toBeNull();

    // Order is by last_activity DESC — the fixture array is deliberately
    // not in that order, and TREE_QUIET (no activity) goes last even though
    // it is the most recently CREATED tree.
    const ids = Array.from(list!.querySelectorAll('li button')).map((el) =>
      el.getAttribute('data-testid')?.replace('recent-tree-', ''),
    );
    expect(ids).toEqual(['tree-fresh', 'tree-middle', 'tree-stale', 'tree-quiet']);

    // Entry fields: title, relative last-activity label, node count.
    const fresh = container.querySelector('[data-testid="recent-tree-tree-fresh"]')!;
    expect(fresh.textContent).toContain('Fresh conversation');
    expect(fresh.textContent).toContain('active');
    expect(fresh.textContent).toContain('7 nodes');

    // Singular node label, and an honest no-activity label instead of a
    // fabricated timestamp.
    const middle = container.querySelector('[data-testid="recent-tree-tree-middle"]')!;
    expect(middle.textContent).toContain('1 node');
    expect(middle.textContent).not.toContain('1 nodes');
    const quiet = container.querySelector('[data-testid="recent-tree-tree-quiet"]')!;
    expect(quiet.textContent).toContain('no activity yet');
    expect(quiet.textContent).not.toContain('active');
  });

  it('caps the list at the 8 most recent trees', async () => {
    treeFixtures = Array.from({ length: 12 }, (_, i) => ({
      id: `tree-${i}`,
      title: `Conversation ${i}`,
      node_count: i,
      last_activity: minutesAgo(i),
      created_at: minutesAgo(1000 + i),
    }));
    renderDashboard();
    await flushPromises();

    const rows = container.querySelectorAll('[data-testid="recent-trees-list"] li');
    expect(rows).toHaveLength(8);
    const ids = Array.from(rows).map((el) =>
      el.querySelector('button')?.getAttribute('data-testid')?.replace('recent-tree-', ''),
    );
    expect(ids).toEqual(['tree-0', 'tree-1', 'tree-2', 'tree-3', 'tree-4', 'tree-5', 'tree-6', 'tree-7']);
  });

  it('clicking an entry navigates to /tree/<id>?manifest=1', async () => {
    treeFixtures = [TREE_FRESH, TREE_STALE];
    renderDashboard();
    await flushPromises();

    expect(visited.some((v) => v.pathname === '/')).toBe(true);

    const entry = container.querySelector(
      '[data-testid="recent-tree-tree-fresh"]',
    ) as HTMLButtonElement;
    expect(entry).not.toBeNull();
    await act(async () => {
      entry.click();
      await Promise.resolve();
    });
    await flushPromises();

    // The URL the manifest deep-link contract requires (AC2).
    expect(visited).toContainEqual({ pathname: '/tree/tree-fresh', search: '?manifest=1' });
    // And the tree route actually rendered — the navigation is real, not
    // just a location change.
    expect(container.querySelector('[data-testid="tree-view-stub"]')).not.toBeNull();
  });

  it('shows the empty state when there are no trees', async () => {
    treeFixtures = [];
    renderDashboard();
    await flushPromises();
    expect(container.querySelector('[data-testid="recent-trees-empty"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="recent-trees-list"]')).toBeNull();
  });

  it('surfaces a trees API failure without breaking the dashboard', async () => {
    fetchMock.mockImplementation((url: string) => {
      const u = String(url);
      if (u.startsWith('/api/v1/trees')) {
        return Promise.resolve(
          jsonResponse({ error: { message: 'trees are down' } }, 500),
        );
      }
      return Promise.resolve(routeFetch(u));
    });
    renderDashboard();
    await flushPromises();

    const err = container.querySelector('[data-testid="recent-trees-error"]');
    expect(err?.textContent).toContain('trees are down');
    // The rest of the dashboard is unaffected.
    expect(container.querySelector('[data-testid="run-row-run_abc"]')).not.toBeNull();
  });
});
