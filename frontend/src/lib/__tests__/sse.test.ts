/**
 * Unit tests — shared SSE client (DF-HERMES-CANOPY-13)
 *
 * The server accepts a bearer token from the `Authorization` header ONLY, and
 * native `EventSource` cannot set headers — so a static production build must
 * use a fetch-based SSE client. This suite pins both arms:
 *
 *  - token resolves  → `fetch` with `Authorization`, wire format parsed here
 *  - no token (dev)  → native `EventSource`, untouched dev behaviour
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { subscribeSse } from '../sse';
import { TOKEN_STORAGE_KEY } from '../api';

/** A response whose body streams `chunks` and then closes. */
function sseResponse(chunks: string[], status = 200): Response {
  const encoder = new TextEncoder();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
      controller.close();
    },
  });
  return new Response(stream, {
    status,
    headers: { 'Content-Type': 'text/event-stream' },
  });
}

/** A response whose body streams `chunks` and never closes (live feed). */
function openSseResponse(chunks: string[], status = 200): Response {
  const encoder = new TextEncoder();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
    },
  });
  return new Response(stream, {
    status,
    headers: { 'Content-Type': 'text/event-stream' },
  });
}

interface Recorded {
  type: string;
  data: string;
}

function recorder() {
  const events: Recorded[] = [];
  return { events, onEvent: (type: string, data: string) => events.push({ type, data }) };
}

describe('lib/sse — fetch arm (production: token resolves)', () => {
  const fetchMock = vi.fn();
  let eventSourceSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
    eventSourceSpy = vi.fn();
    vi.stubGlobal('EventSource', eventSourceSpy);
    window.localStorage.clear();
    vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    window.localStorage.clear();
  });

  it('opens the stream with fetch + Authorization instead of EventSource', async () => {
    fetchMock.mockResolvedValueOnce(
      sseResponse(['event: channel_message\ndata: {"n":1}\n\n']),
    );
    const rec = recorder();
    subscribeSse('/api/v1/workspace/channels/c-1/feed', { onEvent: rec.onEvent });
    await vi.waitFor(() => expect(rec.events).toHaveLength(1));

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/workspace/channels/c-1/feed');
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer build-token-123');
    expect(eventSourceSpy).not.toHaveBeenCalled();
    expect(rec.events[0]).toEqual({ type: 'channel_message', data: '{"n":1}' });
  });

  it('parses named events, multi-line data, CRLF, comments and defaults', async () => {
    fetchMock.mockResolvedValueOnce(
      sseResponse([
        ': keep-alive comment\n',
        'retry: 5000\n',
        'id: 42\n',
        'event: review_event\r\n',
        'data: line one\r\n',
        'data: line two\r\n',
        '\r\n',
        'data: no event field\n\n',
        'data:\n\n',
      ]),
    );
    const rec = recorder();
    const open = vi.fn();
    const close = vi.fn();
    subscribeSse('/api/v1/feed', { ...rec, onOpen: open, onClose: close });
    await vi.waitFor(() => expect(close).toHaveBeenCalled());

    expect(open).toHaveBeenCalled();
    expect(rec.events).toEqual([
      { type: 'review_event', data: 'line one\nline two' },
      { type: 'message', data: 'no event field' },
      { type: 'message', data: '' },
    ]);
  });

  it('discards a frame the server never terminated with a blank line (SSE spec)', async () => {
    fetchMock.mockResolvedValueOnce(
      sseResponse(['event: complete\ndata: kept\n\n', 'event: tail\ndata: dropped']),
    );
    const rec = recorder();
    const close = vi.fn();
    subscribeSse('/api/v1/feed', { onEvent: rec.onEvent, onClose: close });
    await vi.waitFor(() => expect(close).toHaveBeenCalled());
    // Native EventSource behaves the same way — this mirrors the spec, and
    // canopyd always terminates frames with a blank line.
    expect(rec.events).toEqual([{ type: 'complete', data: 'kept' }]);
  });

  it('keeps caller request options (credentials, cache, extra headers)', async () => {
    fetchMock.mockResolvedValueOnce(sseResponse([]));
    const rec = recorder();
    subscribeSse(
      '/api/v1/plugins/p-1/events',
      { onEvent: rec.onEvent },
      { credentials: 'include', cache: 'no-store', headers: { 'X-Trace': 't1' } },
    );
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const headers = new Headers(fetchMock.mock.calls[0][1].headers);
    expect(fetchMock.mock.calls[0][1].credentials).toBe('include');
    expect(fetchMock.mock.calls[0][1].cache).toBe('no-store');
    expect(headers.get('X-Trace')).toBe('t1');
    expect(headers.get('Authorization')).toBe('Bearer build-token-123');
  });

  it('reports an HTTP failure through onError', async () => {
    fetchMock.mockResolvedValueOnce(new Response('nope', { status: 401 }));
    const onError = vi.fn();
    const sub = subscribeSse('/api/v1/feed', { onError, onClose: vi.fn() });
    await vi.waitFor(() => expect(onError).toHaveBeenCalled());
    expect(String(onError.mock.calls[0][0])).toContain('401');
    // close() cancels the pending bounded retry — a live timer would fire into
    // the NEXT test's fetch mock and corrupt it.
    sub.close();
  });

  it('retries exactly once on a transport error, then delivers', async () => {
    fetchMock
      .mockRejectedValueOnce(new Error('network down'))
      .mockResolvedValueOnce(sseResponse(['data: recovered\n\n']));
    const rec = recorder();
    const onError = vi.fn();
    const sub = subscribeSse('/api/v1/feed', { onEvent: rec.onEvent, onError });

    // The bounded retry fires after RETRY_DELAY_MS; wait it out for real rather
    // than racing vi.waitFor's own timeout.
    await new Promise((resolve) => setTimeout(resolve, 1600));
    expect(onError).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(rec.events[0]).toEqual({ type: 'message', data: 'recovered' });
    sub.close();
  }, 10_000);

  it('close() aborts the in-flight request and stops delivery', async () => {
    fetchMock.mockImplementation(async (_url: string, init: RequestInit) => {
      if (init.signal?.aborted) throw init.signal.reason;
      return openSseResponse(['data: first\n\n']);
    });
    const rec = recorder();
    const sub = subscribeSse('/api/v1/feed', { onEvent: rec.onEvent });
    await vi.waitFor(() => expect(rec.events).toHaveLength(1));

    const signal = fetchMock.mock.calls[0][1].signal as AbortSignal;
    expect(signal.aborted).toBe(false);
    sub.close();
    expect(signal.aborted).toBe(true);
  });
});

describe('lib/sse — native EventSource arm (dev: no token)', () => {
  class MockEventSource {
    static instances: MockEventSource[] = [];
    url: string;
    withCredentials: boolean;
    onopen: (() => void) | null = null;
    onerror: ((err: unknown) => void) | null = null;
    closed = false;
    listeners: Record<string, Array<(e: MessageEvent) => void>> = {};

    constructor(url: string, init?: EventSourceInit) {
      this.url = url;
      this.withCredentials = init?.withCredentials ?? false;
      MockEventSource.instances.push(this);
    }

    addEventListener(type: string, listener: (e: MessageEvent) => void): void {
      (this.listeners[type] ??= []).push(listener);
    }

    removeEventListener(type: string, listener: (e: MessageEvent) => void): void {
      this.listeners[type] = (this.listeners[type] ?? []).filter((l) => l !== listener);
    }

    close(): void {
      this.closed = true;
    }

    emit(type: string, data: string): void {
      for (const listener of this.listeners[type] ?? []) {
        listener({ data } as MessageEvent);
      }
    }
  }

  const fetchMock = vi.fn();

  beforeEach(() => {
    MockEventSource.instances = [];
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('EventSource', MockEventSource);
    window.localStorage.clear();
    vi.stubEnv('VITE_API_TOKEN', '');
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    window.localStorage.clear();
  });

  it('falls back to native EventSource and never calls fetch', async () => {
    const rec = recorder();
    const open = vi.fn();
    const sub = subscribeSse(
      '/api/v1/workspace/channels/c-1/feed',
      { ...rec, onOpen: open, eventTypes: ['channel_message'] },
      { credentials: 'include' },
    );

    expect(fetchMock).not.toHaveBeenCalled();
    const source = MockEventSource.instances[0];
    expect(source.url).toBe('/api/v1/workspace/channels/c-1/feed');
    expect(source.withCredentials).toBe(true);

    source.onopen?.();
    source.emit('channel_message', '{"n":1}');
    source.emit('message', 'plain');
    expect(open).toHaveBeenCalled();
    expect(rec.events).toEqual([
      { type: 'channel_message', data: '{"n":1}' },
      { type: 'message', data: 'plain' },
    ]);

    sub.close();
    expect(source.closed).toBe(true);
  });

  it('surfaces transport errors and removes its listeners on close', () => {
    const onError = vi.fn();
    const sub = subscribeSse('/api/v1/feed', { onError, eventTypes: ['review_event'] });
    const source = MockEventSource.instances[0];
    source.onerror?.(new Event('error'));
    expect(onError).toHaveBeenCalled();

    sub.close();
    expect(source.listeners['review_event']).toHaveLength(0);
    expect(source.listeners['message']).toHaveLength(0);
  });

  it('a stored localStorage token switches the transport to fetch', async () => {
    vi.stubEnv('VITE_API_TOKEN', '');
    window.localStorage.setItem(TOKEN_STORAGE_KEY, 'stored-token-456');
    fetchMock.mockResolvedValueOnce(sseResponse(['data: hi\n\n']));
    const rec = recorder();
    subscribeSse('/api/v1/feed', { onEvent: rec.onEvent });
    await vi.waitFor(() => expect(rec.events).toHaveLength(1));
    expect(MockEventSource.instances).toHaveLength(0);
    expect(new Headers(fetchMock.mock.calls[0][1].headers).get('Authorization')).toBe(
      'Bearer stored-token-456',
    );
  });

  it('reports an error instead of throwing when EventSource is unavailable', () => {
    vi.stubGlobal('EventSource', undefined);
    const onError = vi.fn();
    const sub = subscribeSse('/api/v1/feed', { onError });
    expect(onError).toHaveBeenCalled();
    expect(() => sub.close()).not.toThrow();
  });
});
