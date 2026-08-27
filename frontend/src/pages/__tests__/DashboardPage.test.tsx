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
import { MemoryRouter } from 'react-router-dom';
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
    return jsonResponse({ runs: [RUN_RUNNING, RUN_APPROVAL, RUN_DONE] });
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
      createElement(MemoryRouter, null, createElement(DashboardPage)),
    );
  });
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
