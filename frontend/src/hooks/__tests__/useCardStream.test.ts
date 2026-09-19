/**
 * Unit tests — useCardStream (SPEC-PL-03 §9 client consumption).
 *
 * `lib/sse.ts` is the app's only SSE transport, so it is mocked at the module
 * boundary: the hook's own contract — the URL it opens, the event names it
 * subscribes to, how frames move the store, and how a gap produces exactly ONE
 * bounded re-subscribe carrying the cursor — is what is pinned here.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';

vi.mock('../../lib/sse.ts', () => ({ subscribeSse: vi.fn() }));

import { subscribeSse, type SseHandlers, type SseSubscription } from '../../lib/sse.ts';
import { useCardStream, type UseCardStreamReturn } from '../useCardStream.ts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const CARD_ID = '0191a9c3-0000-7000-8000-000000000000';
const OTHER_CARD_ID = '0191a9c3-0000-7000-8000-0000000000cc';

// ─── Real frame bodies (see internal/handler/card_events_handler.go) ────

function snapshotFrame(revision: number, appId = 'canopy'): string {
  return JSON.stringify({
    event_type: 'card_snapshot',
    card_id: CARD_ID,
    card_type: 'iteration',
    sequence: revision,
    timestamp: '2026-07-22T12:00:00Z',
    data: {
      id: CARD_ID,
      tree_id: '0191a9c3-0000-7000-8000-0000000000aa',
      node_id: '0191a9c3-0000-7000-8000-0000000000bb',
      app_id: appId,
      type: 'iteration',
      status: 'active',
      context_hash: 'c4a2f1d6c7b8e9f00112233445566778899aabbccddeeff0011223344556677',
      data: { step: revision },
      actions: [{ label: 'Run', handler: 'run' }],
      last_event_seq: revision,
      created_at: '2026-07-22T12:00:00Z',
    },
  });
}

function eventFrame(sequence: number): string {
  return JSON.stringify({
    event_type: 'card_event',
    card_id: CARD_ID,
    card_type: 'iteration',
    sequence,
    timestamp: '2026-07-22T12:00:00Z',
    data: {
      sequence,
      event_id: `0191a9c3-0000-7000-8000-0000000000${String(sequence).padStart(2, '0')}`,
      card_id: CARD_ID,
      event_type: 'agent_progress',
      actor_kind: 'agent',
      actor_id: 'coding',
      payload: { message: `step ${sequence}` },
      created_at: '2026-07-22T12:00:00Z',
    },
  });
}

const HEARTBEAT_FRAME = JSON.stringify({
  event_type: 'heartbeat',
  card_id: CARD_ID,
  timestamp: '2026-07-22T12:00:05Z',
  data: { card_id: CARD_ID },
});

// ─── Harness ───────────────────────────────────────────────────────────

interface StreamRecord {
  url: string;
  handlers: SseHandlers;
  closed: number;
}

let streams: StreamRecord[];
let container: HTMLDivElement;
let root: Root | null = null;
let last: UseCardStreamReturn | null;

function Probe({ cardId }: { cardId: string | null }) {
  last = useCardStream(cardId);
  return null;
}

async function render(cardId: string | null): Promise<void> {
  const current = root;
  if (current === null) throw new Error('root missing');
  await act(async () => {
    current.render(createElement(Probe, { cardId }));
  });
}

/** Unmount once; idempotent so a test may unmount before `afterEach` runs. */
async function teardown(): Promise<void> {
  const current = root;
  root = null;
  if (current !== null) {
    await act(async () => current.unmount());
  }
  container.remove();
}

function emit(index: number, type: string, data: string): Promise<void> {
  return act(async () => {
    streams[index]?.handlers.onEvent?.(type, data);
  });
}

function fire(index: number, hook: 'onOpen' | 'onClose'): Promise<void> {
  return act(async () => {
    streams[index]?.handlers[hook]?.();
  });
}

beforeEach(() => {
  streams = [];
  last = null;
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);

  const mocked = vi.mocked(subscribeSse);
  mocked.mockReset();
  mocked.mockImplementation((url: string, handlers: SseHandlers): SseSubscription => {
    const record: StreamRecord = { url, handlers, closed: 0 };
    streams.push(record);
    return { close: () => { record.closed += 1; } };
  });
});

afterEach(async () => {
  await teardown();
});

// ─── Tests ─────────────────────────────────────────────────────────────

describe('useCardStream — subscription', () => {
  it('opens the card event stream with the three §9.3 event names', async () => {
    await render(CARD_ID);

    expect(streams).toHaveLength(1);
    expect(streams[0]?.url).toBe(`/api/v1/cards/${CARD_ID}/events`);
    expect(streams[0]?.handlers.eventTypes).toEqual(['card_snapshot', 'card_event', 'heartbeat']);
  });

  it('opens no stream when there is no card id', async () => {
    await render(null);
    expect(streams).toHaveLength(0);
    expect(last?.connection).toBe('idle');
  });

  it('closes the stream on unmount', async () => {
    await render(CARD_ID);
    await teardown();
    expect(streams[0]?.closed).toBe(1);
  });

  it('closes the old stream and resets the store when the card changes', async () => {
    await render(CARD_ID);
    await emit(0, 'card_snapshot', snapshotFrame(5));
    expect(last?.card?.revision).toBe(5);

    await render(OTHER_CARD_ID);
    expect(streams[0]?.closed).toBe(1);
    expect(streams).toHaveLength(2);
    expect(streams[1]?.url).toBe(`/api/v1/cards/${OTHER_CARD_ID}/events`);
    expect(last?.card).toBeNull();
    expect(last?.events).toEqual([]);
    expect(last?.lastSequence).toBe(0);
  });
});

describe('useCardStream — frames drive the store', () => {
  it('applies snapshot, then contiguous events', async () => {
    await render(CARD_ID);
    await emit(0, 'card_snapshot', snapshotFrame(7));
    expect(last?.card?.cardType).toBe('iteration');
    expect(last?.card?.revision).toBe(7);
    // §9.1: the snapshot is not a log position — the cursor stays at 0.
    expect(last?.lastSequence).toBe(0);

    await emit(0, 'card_event', eventFrame(1));
    await emit(0, 'card_event', eventFrame(2));
    expect(last?.events.map((e) => e.sequence)).toEqual([1, 2]);
    expect(last?.lastSequence).toBe(2);
    expect(last?.errors).toEqual([]);
  });

  it('promotes the connection to live on a heartbeat', async () => {
    await render(CARD_ID);
    expect(last?.connection).toBe('connecting');

    await fire(0, 'onOpen');
    expect(last?.connection).toBe('open');
    expect(last?.isLive).toBe(false);
    expect(last?.lastHeartbeatAt).toBeNull();

    await emit(0, 'heartbeat', HEARTBEAT_FRAME);
    expect(last?.connection).toBe('live');
    expect(last?.isLive).toBe(true);
    expect(typeof last?.lastHeartbeatAt).toBe('number');
  });

  it('distinguishes closed and error from live', async () => {
    await render(CARD_ID);
    await fire(0, 'onOpen');
    await emit(0, 'heartbeat', HEARTBEAT_FRAME);
    expect(last?.isLive).toBe(true);

    await fire(0, 'onClose');
    expect(last?.connection).toBe('closed');
    expect(last?.isLive).toBe(false);
  });

  it('reports a transport error without dropping card state', async () => {
    await render(CARD_ID);
    await emit(0, 'card_snapshot', snapshotFrame(2));
    await act(async () => {
      streams[0]?.handlers.onError?.(new Error('boom'));
    });
    expect(last?.connection).toBe('error');
    expect(last?.card?.revision).toBe(2);
  });

  it('ignores an event whose name was never subscribed', async () => {
    await render(CARD_ID);
    await emit(0, 'channel_message', '{"n":1}');
    expect(last?.errors).toEqual([]);
    expect(last?.events).toEqual([]);
  });
});

describe('useCardStream — gap replay', () => {
  async function gapSetup(): Promise<void> {
    await render(CARD_ID);
    await emit(0, 'card_snapshot', snapshotFrame(7));
    await emit(0, 'card_event', eventFrame(1));
    await emit(0, 'card_event', eventFrame(2));
    expect(last?.lastSequence).toBe(2);
  }

  it('re-subscribes exactly once with after_sequence = lastSequence', async () => {
    await gapSetup();

    await emit(0, 'card_event', eventFrame(9));
    expect(streams).toHaveLength(2);
    expect(streams[1]?.url).toBe(`/api/v1/cards/${CARD_ID}/events?after_sequence=2`);
    // The gap did not apply the out-of-order event.
    expect(last?.events.map((e) => e.sequence)).toEqual([1, 2]);
    expect(last?.lastSequence).toBe(2);
    expect(last?.replay).toEqual({ afterSequence: 2 });
  });

  it('does not reconnect again for the same gap cursor', async () => {
    await gapSetup();
    await emit(0, 'card_event', eventFrame(9));
    expect(streams).toHaveLength(2);

    // More out-of-order frames arrive before the replay lands.
    await emit(0, 'card_event', eventFrame(11));
    await emit(0, 'card_event', eventFrame(13));
    expect(streams).toHaveLength(2);
  });

  it('resumes contiguously after the replayed cursor and clears the request', async () => {
    await gapSetup();
    await emit(0, 'card_event', eventFrame(9));
    expect(streams).toHaveLength(2);

    // The re-subscribed stream replays the missing sequence.
    await emit(1, 'card_event', eventFrame(3));
    expect(last?.events.map((e) => e.sequence)).toEqual([1, 2, 3]);
    expect(last?.replay).toBeNull();
  });

  it('routes a later, different gap to a new bounded reconnect', async () => {
    await gapSetup();
    await emit(0, 'card_event', eventFrame(9));
    await emit(1, 'card_event', eventFrame(3));
    await emit(1, 'card_event', eventFrame(4));
    expect(streams).toHaveLength(2);

    await emit(1, 'card_event', eventFrame(8));
    expect(streams).toHaveLength(3);
    expect(streams[2]?.url).toBe(`/api/v1/cards/${CARD_ID}/events?after_sequence=4`);
  });
});

describe('useCardStream — rejected payloads are non-destructive', () => {
  it('files an error and keeps the last good card', async () => {
    await render(CARD_ID);
    await emit(0, 'card_snapshot', snapshotFrame(3));

    await emit(0, 'card_event', '{"event_type":"card_event","card_id":');
    expect(last?.errors).toHaveLength(1);
    expect(last?.card?.revision).toBe(3);
    expect(last?.events).toEqual([]);
    expect(last?.lastSequence).toBe(0);
  });
});
