/**
 * Unit tests — useChannelFeed SSE hook (SPEC-023-UI-002)
 *
 * Verifies the hook opens an EventSource against the channel feed URL,
 * surfaces channel_message events as state, dedupes by message_id,
 * transitions through connecting/open/error, and tears down on unmount
 * or channel switch.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { useChannelFeed } from '../useChannelFeed.ts';
import type { ChannelMessage } from '../../types/workspace.ts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// ─── Mock EventSource ──────────────────────────────────────────────────

interface MockEventSourceCtor {
  new (url: string): MockEventSource;
  instances: MockEventSource[];
}

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

  // Test helpers
  emitOpen(): void {
    this.readyState = 1;
    this.onopen?.(new Event('open'));
  }

  emitError(): void {
    this.readyState = 0;
    this.onerror?.(new Event('error'));
  }

  emitMessage(type: string, data: unknown): void {
    const cbs = this.listeners[type] ?? [];
    const ev = { data: JSON.stringify(data) } as MessageEvent;
    for (const cb of cbs) cb(ev);
  }
}

const MockES = MockEventSource as unknown as MockEventSourceCtor;

// ─── Harness ───────────────────────────────────────────────────────────

let container: HTMLDivElement;
let root: Root;
let lastResult: ReturnType<typeof useChannelFeed> | null;

function Probe({ channelId }: { channelId: string | null }) {
  lastResult = useChannelFeed(channelId);
  return null;
}

function mount(channelId: string | null) {
  act(() => {
    root.render(createElement(Probe, { channelId }));
  });
}

async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  lastResult = null;
  MockEventSource.instances = [];
  vi.stubGlobal('EventSource', MockEventSource);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('useChannelFeed', () => {
  it('opens an EventSource at the channel feed URL', async () => {
    mount('11111111-1111-1111-1111-111111111111');
    await settle();
    expect(MockES.instances).toHaveLength(1);
    expect(MockES.instances[0].url).toBe(
      '/api/v1/workspace/channels/11111111-1111-1111-1111-111111111111/feed',
    );
  });

  it('transitions to open on EventSource onopen', async () => {
    mount('c-1');
    await settle();
    expect(lastResult?.status).toBe('connecting');
    act(() => MockES.instances[0].emitOpen());
    expect(lastResult?.status).toBe('open');
  });

  it('surfaces channel_message events as messages', async () => {
    mount('c-1');
    await settle();
    const msg: ChannelMessage = {
      message_id: 'msg-1',
      channel_id: 'c-1',
      sender_id: 's-1',
      content: 'hello',
      sent_at: '2026-01-01T00:00:00Z',
    };
    act(() =>
      MockES.instances[0].emitMessage('channel_message', {
        event_type: 'channel_message',
        data: msg,
      }),
    );
    expect(lastResult?.messages).toHaveLength(1);
    expect(lastResult?.messages[0].content).toBe('hello');
    expect(lastResult?.hasReceived).toBe(true);
  });

  it('ignores non-channel_message events', async () => {
    mount('c-1');
    await settle();
    act(() =>
      MockES.instances[0].emitMessage('channel_message', {
        event_type: 'other_event',
        data: { foo: 'bar' },
      }),
    );
    expect(lastResult?.messages).toHaveLength(0);
  });

  it('dedupes messages by message_id', async () => {
    mount('c-1');
    await settle();
    const msg: ChannelMessage = {
      message_id: 'dup',
      channel_id: 'c-1',
      sender_id: 's',
      content: 'x',
      sent_at: '2026-01-01T00:00:00Z',
    };
    act(() =>
      MockES.instances[0].emitMessage('channel_message', {
        event_type: 'channel_message',
        data: msg,
      }),
    );
    act(() =>
      MockES.instances[0].emitMessage('channel_message', {
        event_type: 'channel_message',
        data: msg,
      }),
    );
    expect(lastResult?.messages).toHaveLength(1);
  });

  it('reflects error status on EventSource onerror', async () => {
    mount('c-1');
    await settle();
    act(() => MockES.instances[0].emitOpen());
    expect(lastResult?.status).toBe('open');
    act(() => MockES.instances[0].emitError());
    expect(lastResult?.status).toBe('error');
  });

  it('closes the EventSource on unmount', async () => {
    mount('c-1');
    await settle();
    const inst = MockES.instances[0];
    const closeSpy = vi.spyOn(inst, 'close');
    act(() => root.unmount());
    expect(closeSpy).toHaveBeenCalled();
  });

  it('reconnects when the channel id changes', async () => {
    mount('c-1');
    await settle();
    expect(MockES.instances).toHaveLength(1);
    mount('c-2');
    await settle();
    expect(MockES.instances).toHaveLength(2);
    expect(MockES.instances[1].url).toContain('c-2/feed');
    // The first source was closed on switch.
    expect(MockES.instances[0].readyState).toBe(2);
  });

  it('does not open an EventSource when channelId is null', async () => {
    mount(null);
    await settle();
    expect(MockES.instances).toHaveLength(0);
  });

  it('invokes the onMessage callback for each new message', async () => {
    const received: ChannelMessage[] = [];
    function ProbeWithCb({ channelId }: { channelId: string | null }) {
      lastResult = useChannelFeed(channelId, (m) => received.push(m));
      return null;
    }
    act(() => {
      root.render(createElement(ProbeWithCb, { channelId: 'c-1' }));
    });
    await settle();
    const msg: ChannelMessage = {
      message_id: 'cb-1',
      channel_id: 'c-1',
      sender_id: 's',
      content: 'cb',
      sent_at: '2026-01-01T00:00:00Z',
    };
    act(() =>
      MockES.instances[0].emitMessage('channel_message', {
        event_type: 'channel_message',
        data: msg,
      }),
    );
    expect(received).toHaveLength(1);
    expect(received[0].content).toBe('cb');
  });
});
