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

  // ── WIRE-004: de-stubbed presence endpoints ──────────────────────────

  it('setLocalPresence POSTs the presence payload to /trees/{treeId}/presence', async () => {
    const treeId = 'tree-pres-001';
    const doc = createTreeDoc(treeId);
    const provider = new SSESyncProvider(doc, { treeId });
    provider.connect();
    await vi.waitFor(() => expect(provider.connected).toBe(true));

    // Reset the fetch mock so we only see the presence call.
    fetchMock.mockClear();

    provider.setLocalPresence({
      userId: 'user-abc',
      userName: 'Alice',
      avatarColor: '#7c3aed',
      permission: 'editor',
      cursor: null,
      viewport: null,
      isActive: true,
    });

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0] as [
      string,
      { method: string; headers: Record<string, string>; credentials: string; body: string },
    ];
    expect(url).toBe(`/api/v1/trees/${treeId}/presence`);
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('include');
    expect(init.headers['Content-Type']).toBe('application/json');

    const body = JSON.parse(init.body) as { userId: string; userName: string };
    expect(body.userId).toBe('user-abc');
    expect(body.userName).toBe('Alice');
  });

  it('disconnect POSTs a leave to /trees/{treeId}/presence/leave', async () => {
    const treeId = 'tree-pres-002';
    const doc = createTreeDoc(treeId);
    const provider = new SSESyncProvider(doc, { treeId });
    provider.connect();
    await vi.waitFor(() => expect(provider.connected).toBe(true));

    // Establish local presence first so the leave has a userId to send.
    provider.setLocalPresence({
      userId: 'user-leave',
      userName: 'Leaving',
      avatarColor: '#3b82f6',
      permission: 'viewer',
      cursor: null,
      viewport: null,
      isActive: true,
    });
    // Drain the presence push so only the leave call is observed next.
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    fetchMock.mockClear();

    provider.disconnect();

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const leaveCall = fetchMock.mock.calls.find(
      (c) => (c[0] as string).endsWith('/presence/leave'),
    );
    expect(leaveCall).toBeDefined();
    const [url, init] = leaveCall as [
      string,
      { method: string; headers: Record<string, string>; credentials: string; body?: string },
    ];
    expect(url).toBe(`/api/v1/trees/${treeId}/presence/leave`);
    expect(init.method).toBe('POST');
    // The leave body carries the userId.
    if (init.body) {
      const body = JSON.parse(init.body) as { userId?: string };
      expect(body.userId).toBe('user-leave');
    }
  });

  it('handles an incoming presence_update SSE event and updates remote presence', async () => {
    const treeId = 'tree-pres-003';
    const doc = createTreeDoc(treeId);
    const provider = new SSESyncProvider(doc, { treeId });
    provider.connect();
    await vi.waitFor(() => expect(provider.connected).toBe(true));

    const es = lastInstance();
    es.dispatch(
      'presence_update',
      makeEnvelope(treeId, 'presence_update', {
        userId: 'remote-user-1',
        userName: 'Remote Bob',
        avatarColor: '#22c55e',
        permission: 'viewer',
        isActive: true,
        lastSeen: '2026-08-09T10:00:00Z',
      }),
    );

    await vi.waitFor(() =>
      expect(provider.getRemotePresence().get('remote-user-1')).toBeDefined(),
    );
    const remote = provider.getRemotePresence().get('remote-user-1')!;
    expect(remote.userName).toBe('Remote Bob');
    expect(remote.avatarColor).toBe('#22c55e');
  });

  it('clearLocalPresence does not crash when fetch rejects (API down)', async () => {
    const treeId = 'tree-pres-004';
    const doc = createTreeDoc(treeId);
    const provider = new SSESyncProvider(doc, { treeId });

    // Establish presence, then make fetch reject on the leave call.
    provider.setLocalPresence({
      userId: 'crash-user',
      userName: 'Crash',
      avatarColor: '#ef4444',
      permission: 'editor',
      cursor: null,
      viewport: null,
      isActive: true,
    });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());

    fetchMock.mockRejectedValueOnce(new Error('network down'));

    // Should not throw.
    expect(() => provider.clearLocalPresence()).not.toThrow();
  });
});
