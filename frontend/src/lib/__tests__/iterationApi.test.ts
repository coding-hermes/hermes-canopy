import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  cancelIterationCard,
  dismissIterationCard,
  fetchActiveIterationCards,
  fetchIterationProgress,
  normalizeIterationCard,
  openIterationEventStream,
  type IterationEventSource,
} from '../iterationApi.ts';

function response(body: unknown, ok = true): Response {
  return { ok, status: ok ? 200 : 500, json: () => Promise.resolve(body), text: () => Promise.resolve(JSON.stringify(body)) } as Response;
}

function rawCard(id = 'card-1') {
  return {
    id,
    card_type: 'iteration',
    status: 'active',
    revision: 7,
    created_at: '2026-09-26T12:00:00Z',
    updated_at: '2026-09-26T12:01:00Z',
    data: {
      subtype: 'iteration_thinking',
      title: 'Thinking',
      state: 'running',
      progress: { cardId: id, type: 'thinking', title: 'Thinking', current: 1, total: 3, status: 'running', updatedAt: '2026-09-26T12:01:00Z' },
      steps: [],
      currentStepId: null,
    },
  };
}

class FakeEventSource implements IterationEventSource {
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  readonly listeners = new Map<string, EventListener[]>();
  closed = false;
  readonly url: string;
  constructor(url: string) { this.url = url; }
  addEventListener(type: string, listener: EventListener): void { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]); }
  removeEventListener(type: string, listener: EventListener): void { this.listeners.set(type, (this.listeners.get(type) ?? []).filter((value) => value !== listener)); }
  close(): void { this.closed = true; }
  emit(type: string, data: unknown, lastEventId = ''): void {
    const event = { data: JSON.stringify(data), lastEventId } as MessageEvent;
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe('iterationApi REST surface', () => {
  it('loads active cards and the separately-mounted aggregate progress route', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({ cards: [rawCard()] }))
      .mockResolvedValueOnce(response({ progress: [rawCard().data.progress] }));
    vi.stubGlobal('fetch', fetchMock);

    const cards = await fetchActiveIterationCards();
    const progress = await fetchIterationProgress();
    expect(cards[0]?.data.title).toBe('Thinking');
    expect(progress[0]?.cardId).toBe('card-1');
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(['/api/v1/cards/iteration/active', '/api/v1/iteration/progress']);
  });

  it('uses the base card PATCH for durable dismissal and the iteration cancel endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response({}));
    vi.stubGlobal('fetch', fetchMock);
    const card = normalizeIterationCard(rawCard());
    await dismissIterationCard(card);
    await cancelIterationCard(card.id);
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/cards/card-1');
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ method: 'PATCH' });
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get('If-Match')).toBe('7');
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/cards/iteration/card-1/cancel');
  });
});

describe('iterationApi EventSource stream', () => {
  it('delivers a durable dismissal frame even when its base-card sequence is lower', () => {
    let source!: FakeEventSource;
    const factory = vi.fn((url: string) => {
      source = new FakeEventSource(url);
      return source;
    });
    const card = normalizeIterationCard(rawCard());
    const received: string[] = [];
    const stream = openIterationEventStream(card, {
      onEvent: (event) => received.push(`${event.eventType}:${event.cardId}:${event.createdAt}`),
    }, factory);

    source.emit('iteration_event', { cardId: 'card-1', subtype: 'iteration_thinking', eventType: 'thought_progress', sequence: 9, data: { progress: { current: 2, total: 3, status: 'running' } } });
    source.emit('card_dismissed', { card_id: 'card-1', event_type: 'card_dismissed', sequence: 2, created_at: '2026-09-26T12:03:00Z', data: { status: 'dismissed' } });

    expect(received).toEqual(['thought_progress:card-1:undefined', 'card_dismissed:card-1:2026-09-26T12:03:00Z']);
    expect(stream.getLastSequence()).toBe(9);
    stream.close();
  });

  it('opens one source, applies snapshots/events, gates duplicate sequences, tracks heartbeat, and closes', () => {
    let source!: FakeEventSource;
    const factory = vi.fn((url: string) => {
      source = new FakeEventSource(url);
      return source;
    });
    const card = normalizeIterationCard(rawCard());
    const events: number[] = [];
    const snapshots: string[] = [];
    const heartbeats: number[] = [];
    const stream = openIterationEventStream(card, {
      onEvent: (event, next) => { events.push(event.sequence); expect(next.data.progress.current).toBe(2); },
      onSnapshot: (next) => snapshots.push(next.data.state),
      onHeartbeat: (last) => heartbeats.push(last),
    }, factory);

    expect(factory).toHaveBeenCalledTimes(1);
    expect(source.url).toBe('/api/v1/cards/iteration/card-1/events');
    source.emit('iteration_event', { cardId: 'card-1', subtype: 'iteration_thinking', eventType: 'thought_progress', sequence: 2, data: { progress: { current: 2, total: 3, status: 'running' } } });
    source.emit('iteration_event', { cardId: 'card-1', subtype: 'iteration_thinking', eventType: 'thought_progress', sequence: 2, data: { progress: { current: 99, total: 100, status: 'running' } } });
    source.emit('iteration_event', { cardId: 'card-1', subtype: 'iteration_thinking', eventType: 'thought_progress', sequence: 1, data: {} });
    source.emit('heartbeat', { lastSequence: 2, timestamp: '2026-09-26T12:02:00Z' });
    source.emit('card_snapshot', { cardId: 'card-1', subtype: 'iteration_thinking', data: { subtype: 'iteration_thinking', title: 'Thinking', state: 'interrupted', progress: { cardId: 'card-1', type: 'thinking', title: 'Thinking', current: 2, total: 3, status: 'running', updatedAt: '2026-09-26T12:02:00Z' }, steps: [], currentStepId: null } });

    expect(events).toEqual([2]);
    expect(heartbeats).toEqual([2]);
    expect(snapshots).toEqual(['interrupted']);
    expect(stream.getLastSequence()).toBe(2);
    stream.close();
    expect(source.closed).toBe(true);
  });
});
