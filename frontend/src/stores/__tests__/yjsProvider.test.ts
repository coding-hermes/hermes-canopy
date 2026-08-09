import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { SSESyncProvider } from '../yjsProvider.ts';
import { createTreeDoc, getNode } from '../treeStore.ts';

class MockEventSource {
  url: string;
  withCredentials: boolean;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  readyState = 0;
  private listeners: Record<string, Array<(e: MessageEvent) => void>> = {};
  static instances: MockEventSource[] = [];

  constructor(url: string, options?: { withCredentials?: boolean }) {
    this.url = url;
    this.withCredentials = options?.withCredentials ?? false;
    MockEventSource.instances.push(this);
    setTimeout(() => {
      this.readyState = 1;
      this.onopen?.();
    }, 0);
  }

  addEventListener(type: string, fn: (e: MessageEvent) => void): void {
    if (!this.listeners[type]) this.listeners[type] = [];
    this.listeners[type].push(fn);
  }

  removeEventListener(_type: string, _fn: (e: MessageEvent) => void): void {
    // no-op for tests
  }

  close(): void {
    this.readyState = 2;
  }

  dispatch(type: string, data: string): void {
    const ev = { data } as MessageEvent;
    this.listeners[type]?.forEach((fn) => fn(ev));
    if (type === 'message') this.onmessage?.(ev);
  }
}

function makeEnvelope(
  treeId: string,
  eventType: string,
  data: Record<string, unknown>,
): string {
  return JSON.stringify({ event_type: eventType, tree_id: treeId, data });
}

describe('SSESyncProvider', () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    MockEventSource.instances = [];
    fetchMock = vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 204,
        text: () => Promise.resolve(''),
      } as Response),
    );
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('EventSource', MockEventSource as unknown as typeof EventSource);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  function lastInstance(): MockEventSource {
    const inst = MockEventSource.instances[MockEventSource.instances.length - 1];
    if (!inst) throw new Error('no EventSource instance created');
    return inst;
  }

  it('connects to the tree-scoped SSE endpoint and closes on disconnect', async () => {
    const treeId = 'tree-0001';
    const doc = createTreeDoc(treeId);
    const provider = new SSESyncProvider(doc, { treeId });

    provider.connect();
    await vi.waitFor(() => expect(provider.connected).toBe(true));

    const es = lastInstance();
    expect(es.url).toBe(`/api/v1/trees/${treeId}/events`);
    expect(es.withCredentials).toBe(true);

    provider.disconnect();
    expect(es.readyState).toBe(2);
    expect(provider.connected).toBe(false);
  });

  it('applies a backend node_added SSE event to the Yjs doc', async () => {
    const treeId = 'tree-0002';
    const doc = createTreeDoc(treeId);
    const provider = new SSESyncProvider(doc, { treeId });
    provider.connect();
    await vi.waitFor(() => expect(provider.connected).toBe(true));

    const es = lastInstance();
    es.dispatch(
      'node_added',
      makeEnvelope(treeId, 'node_added', {
        node_id: 'node-abc',
        content: 'hello from SSE',
        content_format: 'markdown',
        node_type: 'message',
        actor_id: 'actor-1',
        timestamp: '2026-08-08T12:00:00Z',
        parent_id: null,
      }),
    );

    await vi.waitFor(() => expect(getNode(doc, 'node-abc')).toBeDefined());
    const node = getNode(doc, 'node-abc')!;
    expect(node.id).toBe('node-abc');
    expect(node.content).toBe('hello from SSE');
    expect(node.contentFormat).toBe('markdown');
    expect(node.nodeType).toBe('message');
  });

  it('ignores SSE events for a different tree', async () => {
    const treeId = 'tree-0003';
    const doc = createTreeDoc(treeId);
    const provider = new SSESyncProvider(doc, { treeId });
    provider.connect();
    await vi.waitFor(() => expect(provider.connected).toBe(true));

    const es = lastInstance();
    es.dispatch(
      'node_added',
      makeEnvelope('foreign-tree', 'node_added', {
        node_id: 'node-intruder',
        content: 'not mine',
        content_format: 'markdown',
        node_type: 'message',
      }),
    );

    // Give Yjs a tick to apply, then assert it was skipped.
    await new Promise((r) => setTimeout(r, 50));
    expect(getNode(doc, 'node-intruder')).toBeUndefined();
  });

  it('pushUpdate POSTs the Yjs update to /trees/{treeId}/sync', async () => {
    const treeId = 'tree-0004';
    const doc = createTreeDoc(treeId);
    const provider = new SSESyncProvider(doc, { treeId });
    provider.connect();
    await vi.waitFor(() => expect(provider.connected).toBe(true));

    // Trigger a local Yjs update (not from sse-provider origin).
    doc.ydoc.transact(() => {
      doc.meta.set('title', 'pushed title');
    }, 'local-test');

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [
      string,
      { method: string; headers: Record<string, string>; credentials: string; body: unknown },
    ];
    expect(url).toBe(`/api/v1/trees/${treeId}/sync`);
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('include');
    expect(init.headers['Content-Type']).toBe('application/octet-stream');
    expect(init.body).toBeInstanceOf(Uint8Array);
    expect((init.body as Uint8Array).byteLength).toBeGreaterThan(0);
  });
});
