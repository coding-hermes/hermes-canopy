/**
 * Unit tests — useChannelFeed under a resolved token (DF-HERMES-CANOPY-13)
 *
 * The pre-existing useChannelFeed suite runs with no token, i.e. the dev-proxy
 * arm (native EventSource). This file pins the PRODUCTION arm: when
 * `VITE_API_TOKEN` resolves, the hook must stream over `fetch` carrying
 * `Authorization: Bearer <token>` and must NOT construct an EventSource.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { useChannelFeed } from '../useChannelFeed.ts';
import type { ChannelMessage } from '../../types/workspace.ts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

const CHANNEL_ID = '11111111-1111-1111-1111-111111111111';
const FEED_URL = `/api/v1/workspace/channels/${CHANNEL_ID}/feed`;

const MESSAGE: ChannelMessage = {
  message_id: 'msg-1',
  channel_id: CHANNEL_ID,
  sender_id: 's-1',
  content: 'hello over fetch',
  sent_at: '2026-01-01T00:00:00Z',
};

/** SSE response that emits `frames` and stays open (a live feed). */
function openFeed(frames: string[]): Response {
  const encoder = new TextEncoder();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const frame of frames) controller.enqueue(encoder.encode(frame));
    },
  });
  return new Response(stream, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  });
}

function frame(eventType: string, data: unknown): string {
  return `event: ${eventType}\ndata: ${JSON.stringify(data)}\n\n`;
}

let container: HTMLDivElement;
let root: Root;
let lastResult: ReturnType<typeof useChannelFeed> | null;
const fetchMock = vi.fn();
const eventSourceSpy = vi.fn();

function Probe({ channelId }: { channelId: string | null }) {
  lastResult = useChannelFeed(channelId);
  return null;
}

async function flush(): Promise<void> {
  for (let i = 0; i < 10; i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  lastResult = null;
  fetchMock.mockReset();
  fetchMock.mockImplementation(() =>
    Promise.resolve(openFeed([frame('channel_message', { event_type: 'channel_message', data: MESSAGE })])),
  );
  vi.stubGlobal('fetch', fetchMock);
  eventSourceSpy.mockReset();
  vi.stubGlobal('EventSource', eventSourceSpy);
  vi.stubEnv('VITE_API_TOKEN', 'build-token-123');
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe('useChannelFeed with a resolved token', () => {
  it('streams over fetch with Authorization and does not build an EventSource', async () => {
    act(() => {
      root.render(createElement(Probe, { channelId: CHANNEL_ID }));
    });
    await flush();

    expect(eventSourceSpy).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalled();
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(FEED_URL);
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer build-token-123');
  });

  it('surfaces a parsed channel_message frame as hook state', async () => {
    act(() => {
      root.render(createElement(Probe, { channelId: CHANNEL_ID }));
    });
    await flush();

    expect(lastResult?.messages).toHaveLength(1);
    expect(lastResult?.messages[0].content).toBe('hello over fetch');
    expect(lastResult?.status).toBe('open');
  });

  it('aborts the stream on unmount', async () => {
    act(() => {
      root.render(createElement(Probe, { channelId: CHANNEL_ID }));
    });
    await flush();

    const signal = fetchMock.mock.calls[0][1].signal as AbortSignal;
    expect(signal.aborted).toBe(false);
    act(() => root.unmount());
    expect(signal.aborted).toBe(true);
  });
});
