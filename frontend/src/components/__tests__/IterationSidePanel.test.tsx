import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import IterationSidePanel from '../agent/IterationSidePanel.tsx';
import type { EventSourceFactory, IterationEventSource } from '../../lib/iterationApi.ts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function ok(body: unknown): Response {
  return { ok: true, status: 200, json: () => Promise.resolve(body), text: () => Promise.resolve(JSON.stringify(body)) } as Response;
}

function card(id: string, subtype: string, updatedAt: string, state = 'running') {
  const progressType = subtype.replace('iteration_', '');
  const common = {
    subtype,
    title: `${subtype} card`,
    state,
    progress: { cardId: id, type: progressType, title: `${subtype} card`, current: subtype === 'iteration_thinking' ? 1 : 0, total: subtype === 'iteration_thinking' ? 3 : 0, status: state === 'completed' ? 'completed' : state === 'failed' ? 'failed' : 'running', updatedAt },
  };
  const data = subtype === 'iteration_search'
    ? { ...common, urlsSearched: [], currentBatch: [{ url: 'https://example.com', snippet: 'result', snippetId: 'r1', status: 'retrieved' }], focusUrls: null }
    : subtype === 'iteration_code_exec'
      ? { ...common, command: 'npm test', workdir: null, status: state === 'interrupted' ? 'running' : state, stdout: [], stderr: [], exitCode: null, startTime: updatedAt, endTime: null, cancelled: false }
      : subtype === 'iteration_file_read'
        ? { ...common, path: 'main.go', absolutePath: '/repo/main.go', size: 100, mimeType: 'text/plain', language: 'go', lineCount: 20, highlights: [], visibleLines: { start: 1, end: 10 } }
        : subtype === 'iteration_tool_call'
          ? { ...common, toolName: 'file_write', params: {}, result: null, status: 'pending_approval', startTime: null, endTime: null, durationMs: null, error: null, gated: true }
          : { ...common, steps: [{ id: 'step-1', title: 'Gather sources', status: 'active', content: null, duration_ms: null, error: null }], currentStepId: 'step-1' };
  return { id, status: 'active', revision: 1, created_at: '2026-09-26T12:00:00Z', updated_at: updatedAt, data };
}

class FakeSource implements IterationEventSource {
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  listeners = new Map<string, EventListener[]>();
  closed = false;
  readonly url: string;
  constructor(url: string) { this.url = url; }
  addEventListener(type: string, listener: EventListener): void { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]); }
  removeEventListener(type: string, listener: EventListener): void { this.listeners.set(type, (this.listeners.get(type) ?? []).filter((item) => item !== listener)); }
  close(): void { this.closed = true; }
  emit(type: string, body: unknown): void {
    const event = { data: JSON.stringify(body), lastEventId: String((body as { sequence?: number }).sequence ?? '') } as MessageEvent;
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;
let sources: FakeSource[];

async function settle(): Promise<void> {
  await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
}

function mount(factory?: EventSourceFactory) {
  act(() => root.render(createElement(IterationSidePanel, { open: true, onClose: vi.fn(), eventSourceFactory: factory })));
}

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe('IterationSidePanel — §8.1 shell and compact renderers', () => {
  it('loads and sorts all subtype cards, renders the header shape and immediate controls', async () => {
    const active = [
      card('search', 'iteration_search', '2026-09-26T12:01:00Z'),
      card('code', 'iteration_code_exec', '2026-09-26T12:02:00Z'),
      card('file', 'iteration_file_read', '2026-09-26T12:03:00Z'),
      card('thinking', 'iteration_thinking', '2026-09-26T12:04:00Z'),
      card('tool', 'iteration_tool_call', '2026-09-26T12:05:00Z', 'waiting_for_user'),
    ];
    fetchMock = vi.fn().mockResolvedValueOnce(ok({ cards: active })).mockResolvedValueOnce(ok({ progress: [] })).mockResolvedValue(ok({}));
    vi.stubGlobal('fetch', fetchMock);
    sources = [];
    const factory: EventSourceFactory = (url) => { const source = new FakeSource(url); sources.push(source); return source; };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount(factory);
    await settle();

    expect(container.querySelector('[data-testid="iteration-side-panel"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="iteration-header-status"]')?.textContent).toBe('Tool');
    expect(container.textContent).toContain('Results: 1');
    expect(container.textContent).toContain('Running · cancellable');
    expect(container.textContent).toContain('main.go · lines 1-10');
    expect(container.textContent).toContain('Active step: Gather sources');
    expect(container.textContent).toContain('Approve file_write');
    expect(container.querySelectorAll('[role="listitem"]')).toHaveLength(5);
    expect([...container.querySelectorAll<HTMLElement>('[data-card-id]')].map((item) => item.dataset.cardId)).toEqual(['tool', 'thinking', 'file', 'code', 'search']);
    expect(sources).toHaveLength(5);
    expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/cards/iteration/active')).toBe(true);
    expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/iteration/progress')).toBe(true);
  });

  it('waits for card_dismissed before removing a card, even when PATCH is unresolved', async () => {
    let resolveDismiss!: (response: Response) => void;
    const pendingDismiss = new Promise<Response>((resolve) => { resolveDismiss = resolve; });
    fetchMock = vi.fn()
      .mockResolvedValueOnce(ok({ cards: [card('code', 'iteration_code_exec', '2026-09-26T12:01:00Z')] }))
      .mockResolvedValueOnce(ok({ progress: [] }))
      .mockImplementation((url: string) => url === '/api/v1/cards/code' ? pendingDismiss : Promise.resolve(ok({})));
    vi.stubGlobal('fetch', fetchMock);
    sources = [];
    const factory: EventSourceFactory = (url) => { const source = new FakeSource(url); sources.push(source); return source; };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount(factory);
    await settle();

    const dismiss = container.querySelector('[aria-label="Dismiss iteration_code_exec card"]') as HTMLButtonElement;
    act(() => dismiss.click());
    await settle();
    expect(container.querySelector('[data-card-id="code"]')).not.toBeNull();

    const source = sources[0]!;
    act(() => source.emit('card_dismissed', { cardId: 'code', eventType: 'card_dismissed', sequence: 9, createdAt: '2026-09-26T12:09:00Z', data: { status: 'dismissed' } }));
    expect(container.querySelector('[data-card-id="code"]')).toBeNull();
    resolveDismiss(ok({}));
    await settle();
    expect(container.querySelector('[data-card-id="code"]')).toBeNull();
  });

  it('reorders from a committed event without losing the focused expand control', async () => {
    fetchMock = vi.fn().mockResolvedValueOnce(ok({ cards: [card('old', 'iteration_thinking', '2026-09-26T12:01:00Z'), card('new', 'iteration_search', '2026-09-26T12:02:00Z')] })).mockResolvedValueOnce(ok({ progress: [] }));
    vi.stubGlobal('fetch', fetchMock);
    sources = [];
    const factory: EventSourceFactory = (url) => { const source = new FakeSource(url); sources.push(source); return source; };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount(factory);
    await settle();
    const focused = container.querySelector('[aria-label="Expand iteration_search card"]') as HTMLButtonElement;
    focused.focus();
    expect(document.activeElement).toBe(focused);
    const oldSource = sources.find((source) => source.url.endsWith('/old/events'))!;
    act(() => oldSource.emit('iteration_event', { cardId: 'old', subtype: 'iteration_thinking', eventType: 'thought_progress', sequence: 1, createdAt: '2026-09-26T13:00:00Z', data: { progress: { current: 2, total: 3, status: 'running' } } }));
    expect(document.activeElement).toBe(focused);
    expect(container.querySelector('li[data-card-id="old"]')?.parentElement?.firstElementChild?.getAttribute('data-card-id')).toBe('old');
  });

  it('cancels running code, dismisses durably, and closes its source', async () => {
    fetchMock = vi.fn().mockResolvedValueOnce(ok({ cards: [card('code', 'iteration_code_exec', '2026-09-26T12:01:00Z')] })).mockResolvedValueOnce(ok({ progress: [] })).mockResolvedValue(ok({}));
    vi.stubGlobal('fetch', fetchMock);
    sources = [];
    const factory: EventSourceFactory = (url) => { const source = new FakeSource(url); sources.push(source); return source; };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount(factory);
    await settle();
    const cancel = container.querySelector('[aria-label="Cancel iteration_code_exec card"]') as HTMLButtonElement;
    act(() => cancel.click());
    await settle();
    expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/cards/iteration/code/cancel')).toBe(true);
    const dismiss = container.querySelector('[aria-label="Dismiss iteration_code_exec card"]') as HTMLButtonElement;
    act(() => dismiss.click());
    await settle();
    expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/cards/code')).toBe(true);
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false);
    const dismissCall = fetchMock.mock.calls.find(([url]) => url === '/api/v1/cards/code');
    expect(dismissCall).toBeDefined();
    const dismissInit = dismissCall![1] as RequestInit;
    expect(dismissInit.method).toBe('PATCH');
    expect(dismissInit.body).toBe(JSON.stringify({ status: 'dismissed' }));
    expect(container.querySelector('[data-card-id="code"]')).not.toBeNull();
    expect(sources[0]?.closed).toBe(false);
    act(() => sources[0]!.emit('card_dismissed', { cardId: 'code', eventType: 'card_dismissed', sequence: 3, createdAt: '2026-09-26T12:03:00Z', data: { status: 'dismissed' } }));
    expect(container.querySelector('[data-card-id="code"]')).toBeNull();
    expect(sources[0]?.closed).toBe(true);
  });
  it('keeps expand and dismiss controls keyboard reachable and operable', async () => {
    fetchMock = vi.fn().mockResolvedValueOnce(ok({ cards: [card('code', 'iteration_code_exec', '2026-09-26T12:01:00Z')] })).mockResolvedValueOnce(ok({ progress: [] })).mockResolvedValue(ok({}));
    vi.stubGlobal('fetch', fetchMock);
    sources = [];
    const factory: EventSourceFactory = (url) => { const source = new FakeSource(url); sources.push(source); return source; };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount(factory);
    await settle();

    const expand = container.querySelector('[aria-label="Expand iteration_code_exec card"]') as HTMLButtonElement;
    const dismiss = container.querySelector('[aria-label="Dismiss iteration_code_exec card"]') as HTMLButtonElement;
    act(() => {
      expand.focus();
      expand.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    });
    expect(document.activeElement).toBe(expand);
    expect(expand.tabIndex).toBe(0);
    act(() => expand.click());
    expect(expand.getAttribute('aria-expanded')).toBe('true');
    act(() => {
      dismiss.focus();
      dismiss.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
      dismiss.click();
    });
    expect(document.activeElement).toBe(dismiss);
    expect(dismiss.tabIndex).toBe(0);
    expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/cards/code')).toBe(true);
  });

  it('wires expand and collapse with aria-expanded and aria-controls', async () => {
    fetchMock = vi.fn().mockResolvedValueOnce(ok({ cards: [card('thinking', 'iteration_thinking', '2026-09-26T12:01:00Z')] })).mockResolvedValueOnce(ok({ progress: [] }));
    vi.stubGlobal('fetch', fetchMock);
    sources = [];
    const factory: EventSourceFactory = (url) => { const source = new FakeSource(url); sources.push(source); return source; };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount(factory);
    await settle();

    const expand = container.querySelector('[aria-label="Expand iteration_thinking card"]') as HTMLButtonElement;
    const detailsID = expand.getAttribute('aria-controls');
    expect(expand.getAttribute('aria-expanded')).toBe('false');
    expect(detailsID).toBe('iteration-details-thinking');
    expect(container.querySelector(`#${detailsID}`)).toBeNull();
    act(() => expand.click());
    expect(expand.getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelector(`#${detailsID}`)).not.toBeNull();
    act(() => expand.click());
    expect(expand.getAttribute('aria-expanded')).toBe('false');
    expect(container.querySelector(`#${detailsID}`)).toBeNull();
  });

  it('keeps prior activity visible for interrupted cards and removes stale cancel controls', async () => {
    fetchMock = vi.fn().mockResolvedValueOnce(ok({ cards: [card('interrupted', 'iteration_code_exec', '2026-09-26T12:01:00Z', 'interrupted')] })).mockResolvedValueOnce(ok({ progress: [] }));
    vi.stubGlobal('fetch', fetchMock);
    sources = [];
    const factory: EventSourceFactory = (url) => { const source = new FakeSource(url); sources.push(source); return source; };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount(factory);
    await settle();
    expect(container.textContent).toContain('Recovery required after an interrupted agent process');
    expect(container.querySelector('[aria-label="Cancel iteration_code_exec card"]')).toBeNull();
    const expand = container.querySelector('[aria-label="Expand iteration_code_exec card"]') as HTMLButtonElement;
    act(() => expand.click());
    expect(container.textContent).toContain('No committed activity received yet.');
  });

  it('announces committed status in a polite live region and throttles code output announcements', async () => {
    vi.useFakeTimers();
    fetchMock = vi.fn().mockResolvedValueOnce(ok({ cards: [card('code', 'iteration_code_exec', '2026-09-26T12:01:00Z')] })).mockResolvedValueOnce(ok({ progress: [] }));
    vi.stubGlobal('fetch', fetchMock);
    sources = [];
    const factory: EventSourceFactory = (url) => { const source = new FakeSource(url); sources.push(source); return source; };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount(factory);
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });
    const source = sources[0]!;
    const live = container.querySelector('[data-testid="live-code"]')!;
    expect(live.getAttribute('role')).toBe('status');
    expect(live.getAttribute('aria-live')).toBe('polite');
    act(() => source.emit('iteration_event', { cardId: 'code', subtype: 'iteration_code_exec', eventType: 'exec_output', sequence: 1, data: { state: 'completed' } }));
    expect(live.textContent).toContain('Done');
    act(() => source.emit('iteration_event', { cardId: 'code', subtype: 'iteration_code_exec', eventType: 'exec_output', sequence: 2, data: { state: 'failed' } }));
    expect(live.textContent).toContain('Done');
    act(() => { vi.advanceTimersByTime(2000); source.emit('iteration_event', { cardId: 'code', subtype: 'iteration_code_exec', eventType: 'exec_output', sequence: 3, data: { state: 'failed' } }); });
    expect(live.textContent).toContain('Failed');
  });

  it('closes every visible card stream when the panel closes', async () => {
    fetchMock = vi.fn().mockResolvedValueOnce(ok({ cards: [card('one', 'iteration_thinking', '2026-09-26T12:01:00Z'), card('two', 'iteration_search', '2026-09-26T12:02:00Z')] })).mockResolvedValueOnce(ok({ progress: [] }));
    vi.stubGlobal('fetch', fetchMock);
    sources = [];
    const factory: EventSourceFactory = (url) => { const source = new FakeSource(url); sources.push(source); return source; };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mount(factory);
    await settle();
    act(() => root.render(createElement(IterationSidePanel, { open: false, onClose: vi.fn(), eventSourceFactory: factory })));
    expect(sources).toHaveLength(2);
    expect(sources.every((source) => source.closed)).toBe(true);
  });
});
