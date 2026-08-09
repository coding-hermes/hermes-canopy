/**
 * Component tests — WorkspacePage (SPEC-023-UI-002)
 *
 * Pins the wiring of the workspace view:
 *  - renders the channel list from GET /workspace/channels (general + agents)
 *  - selecting a channel opens the feed (EventSource) and renders messages
 *  - the composer POSTs a message via apiPost and clears on success
 *  - empty-content 400 surfaces an inline error (not console-only)
 *  - unknown-channel 404 surfaces an inline error
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import WorkspacePage from '../../pages/WorkspacePage.tsx';

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
    for (const cb of this.listeners[type] ?? []) {
      cb({ data: JSON.stringify(data) } as MessageEvent);
    }
  }
}

// ─── Fixtures ──────────────────────────────────────────────────────────

const CHANNELS = [
  { id: 'ch-general', name: 'general' },
  { id: 'ch-agents', name: 'agents' },
];

function okResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    text: () => Promise.resolve(JSON.stringify(body)),
    json: () => Promise.resolve(body),
  } as Response;
}

function acceptedResponse(body: unknown): Response {
  return {
    ok: true,
    status: 202,
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
    json: () =>
      Promise.resolve({ error: { code: 'X', message } }),
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
        { initialEntries: ['/workspace'] },
        createElement(WorkspacePage),
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

/**
 * Set a controlled input's value the way React expects. Direct
 * `input.value =` assignment doesn't trigger React's value tracker, so
 * the state never updates and controlled components stay stale. The
 * native setter on the prototype bypasses React's intercepted setter.
 */
function setInputValue(input: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value',
  )!.set!;
  setter.call(input, value);
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  MockEventSource.instances = [];
  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const path = url.replace(/^https?:\/\/[^/]+/, '');
    if (
      path === '/api/v1/workspace/channels' &&
      (!init || init.method === 'GET')
    ) {
      return Promise.resolve(okResponse(CHANNELS));
    }
    if (
      path.startsWith('/api/v1/workspace/channels/') &&
      path.endsWith('/message') &&
      init?.method === 'POST'
    ) {
      const body = JSON.parse(init.body as string);
      return Promise.resolve(
        acceptedResponse({
          message_id: 'msg-post-' + Date.now(),
          channel_id: path.split('/')[4],
          content: body.content,
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

// ─── Channel list ──────────────────────────────────────────────────────

describe('WorkspacePage — channel list', () => {
  it('renders the seeded channels from GET /workspace/channels', async () => {
    mount();
    await settle();
    const rail = q('[data-testid="workspace-channels"]');
    expect(rail).not.toBeNull();
    const pills = qa('button[aria-current], button:not([aria-current])');
    const names = pills
      .map((b) => b.textContent ?? '')
      .filter((t) => CHANNELS.some((c) => t.includes(c.name)));
    expect(names.length).toBeGreaterThanOrEqual(2);
    expect(rail?.textContent).toContain('general');
    expect(rail?.textContent).toContain('agents');
  });

  it('surfaces a list error when the channels endpoint fails', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(500, 'boom')),
    );
    mount();
    await settle();
    const err = q('[data-testid="channels-error"]');
    expect(err).not.toBeNull();
    expect(err?.textContent).toContain('boom');
  });
});

// ─── Feed ──────────────────────────────────────────────────────────────

describe('WorkspacePage — message feed', () => {
  it('renders an incoming channel_message event from the SSE feed', async () => {
    mount();
    await settle();
    // Auto-selected first channel → one EventSource should exist.
    expect(MockEventSource.instances.length).toBeGreaterThanOrEqual(1);
    act(() => MockEventSource.instances[0].emitOpen());
    act(() =>
      MockEventSource.instances[0].emitMessage('channel_message', {
        event_type: 'channel_message',
        data: {
          message_id: 'sse-1',
          channel_id: 'ch-general',
          sender_id: 'sender-aaaabbbb',
          content: 'hello from the feed',
          sent_at: '2026-01-01T00:00:00Z',
        },
      }),
    );
    await settle();
    const msgs = qa('[data-testid="workspace-message"]');
    expect(msgs.length).toBeGreaterThanOrEqual(1);
    expect(container.textContent).toContain('hello from the feed');
  });

  it('opens a new feed EventSource when selecting a different channel', async () => {
    mount();
    await settle();
    const before = MockEventSource.instances.length;
    // Click the "agents" channel pill.
    const agentsBtn = qa('button').find(
      (b) => (b.textContent ?? '').includes('agents'),
    );
    expect(agentsBtn).toBeDefined();
    act(() => agentsBtn!.click());
    await settle();
    expect(MockEventSource.instances.length).toBe(before + 1);
    expect(MockEventSource.instances.at(-1)!.url).toContain('ch-agents');
  });
});

// ─── Composer ──────────────────────────────────────────────────────────

describe('WorkspacePage — composer', () => {
  it('POSTs a message on send and clears the input on success', async () => {
    mount();
    await settle();
    const input = q('input[aria-label="Message the channel"]') as HTMLInputElement;
    expect(input).not.toBeNull();
    act(() => setInputValue(input, 'a test message'));
    const sendBtn = qa('button').find(
      (b) => b.getAttribute('aria-label') === 'Send message',
    );
    expect(sendBtn).toBeDefined();
    await act(async () => {
      sendBtn!.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    await settle();
    const postCall = fetchMock.mock.calls.find(
      ([, init]) => init?.method === 'POST',
    );
    expect(postCall).toBeDefined();
    expect(input.value).toBe('');
    // The optimistically inserted message should be visible.
    expect(container.textContent).toContain('a test message');
  });

  it('surfaces an inline error on empty-content 400', async () => {
    // Override POST to return 400.
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      const path = url.replace(/^https?:\/\/[^/]+/, '');
      if (path === '/api/v1/workspace/channels' && !init?.method) {
        return Promise.resolve(okResponse(CHANNELS));
      }
      if (
        path.startsWith('/api/v1/workspace/channels/') &&
        path.endsWith('/message') &&
        init?.method === 'POST'
      ) {
        return Promise.resolve(errorResponse(400, 'content must not be empty'));
      }
      return Promise.reject(new Error(`unexpected: ${url}`));
    });
    mount();
    await settle();
    const input = q('input[aria-label="Message the channel"]') as HTMLInputElement;
    act(() => setInputValue(input, 'x'));
    const sendBtn = qa('button').find(
      (b) => b.getAttribute('aria-label') === 'Send message',
    );
    await act(async () => {
      sendBtn!.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    await settle();
    const errEl = q('[data-testid="composer-error"]');
    expect(errEl).not.toBeNull();
    expect(errEl?.textContent).toContain('content must not be empty');
  });

  it('surfaces an inline error on unknown-channel 404', async () => {
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      const path = url.replace(/^https?:\/\/[^/]+/, '');
      if (path === '/api/v1/workspace/channels' && !init?.method) {
        return Promise.resolve(okResponse(CHANNELS));
      }
      if (
        path.startsWith('/api/v1/workspace/channels/') &&
        path.endsWith('/message') &&
        init?.method === 'POST'
      ) {
        return Promise.resolve(errorResponse(404, 'channel does not exist'));
      }
      return Promise.reject(new Error(`unexpected: ${url}`));
    });
    mount();
    await settle();
    const input = q('input[aria-label="Message the channel"]') as HTMLInputElement;
    act(() => setInputValue(input, 'y'));
    const sendBtn = qa('button').find(
      (b) => b.getAttribute('aria-label') === 'Send message',
    );
    await act(async () => {
      sendBtn!.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    await settle();
    const errEl = q('[data-testid="composer-error"]');
    expect(errEl?.textContent).toContain('channel does not exist');
  });
});
