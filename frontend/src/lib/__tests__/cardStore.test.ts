/**
 * Unit tests — card store reducer (SPEC-PL-03 §5.3).
 *
 * Every §5.3 rule is pinned here as a table-driven case over the pure reducer:
 * snapshot ordering, exact-sequence append, gap → replay request, duplicate
 * no-op, and a rejected payload that changes nothing. The reducer owns no
 * transport, clock or subscription, so these are exact.
 */

import { describe, it, expect } from 'vitest';
import {
  MAX_CARD_STORE_ERRORS,
  ingestCardStreamPayload,
  initialCardStoreState,
  reduceCardStore,
  type CardStoreState,
} from '../cardStore.ts';
import type { Card, CardEvent, CardStreamFrame } from '../../types/card.ts';

const CARD_ID = '0191a9c3-0000-7000-8000-000000000000';

function card(revision: number, over: Partial<Card> = {}): Card {
  return {
    id: CARD_ID,
    treeId: '0191a9c3-0000-7000-8000-0000000000aa',
    nodeId: '0191a9c3-0000-7000-8000-0000000000bb',
    appId: 'canopy',
    cardType: 'iteration',
    data: {},
    actions: [],
    status: 'active',
    contextHash: 'c4a2f1d6c7b8e9f00112233445566778899aabbccddeeff0011223344556677',
    revision,
    createdAt: '2026-07-22T12:00:00Z',
    ...over,
  };
}

function event(sequence: number, over: Partial<CardEvent> = {}): CardEvent {
  return {
    sequence,
    eventId: `0191a9c3-0000-7000-8000-0000000000${String(sequence).padStart(2, '0')}`,
    cardId: CARD_ID,
    eventType: 'agent_progress',
    actorKind: 'agent',
    actorId: 'coding',
    payload: { sequence },
    createdAt: '2026-07-22T12:00:00Z',
    ...over,
  };
}

function snapshot(revision: number, over: Partial<Card> = {}): CardStreamFrame {
  return { kind: 'snapshot', card: card(revision, over), sequence: revision, timestamp: '2026-07-22T12:00:00Z' };
}

function frame(ev: CardEvent): CardStreamFrame {
  return { kind: 'event', event: ev, sequence: ev.sequence, timestamp: '2026-07-22T12:00:00Z' };
}

const heartbeat: CardStreamFrame = {
  kind: 'heartbeat',
  cardId: CARD_ID,
  timestamp: '2026-07-22T12:00:00Z',
};

function apply(state: CardStoreState, ...frames: CardStreamFrame[]): CardStoreState {
  return frames.reduce((acc, f) => reduceCardStore(acc, { type: 'stream_frame', frame: f }), state);
}

describe('cardStore — initial state', () => {
  it('starts empty with no cursor', () => {
    expect(initialCardStoreState).toEqual({
      card: null,
      events: [],
      lastSequence: 0,
      replay: null,
      errors: [],
      rejected: 0,
    });
  });
});

describe('cardStore — §5.3 rule 1: snapshot ordering key', () => {
  it('applies the first snapshot', () => {
    const next = apply(initialCardStoreState, snapshot(7));
    expect(next.card?.revision).toBe(7);
    // A snapshot is not a position in the log (§9.1): the cursor stays put.
    expect(next.lastSequence).toBe(0);
  });

  it('applies a strictly newer snapshot', () => {
    const next = apply(initialCardStoreState, snapshot(7), snapshot(9, { appId: 'canopy.agent' }));
    expect(next.card?.revision).toBe(9);
    expect(next.card?.appId).toBe('canopy.agent');
  });

  it('ignores an equal snapshot (same revision) without touching state', () => {
    const at9 = apply(initialCardStoreState, snapshot(9));
    const again = apply(at9, snapshot(9, { appId: 'canopy.agent' }));
    expect(again).toBe(at9);
    expect(again.card?.appId).toBe('canopy');
  });

  it('ignores an older snapshot', () => {
    const at9 = apply(initialCardStoreState, snapshot(9));
    const older = apply(at9, snapshot(4, { appId: 'stale' }));
    expect(older).toBe(at9);
    expect(older.card?.revision).toBe(9);
  });

  it('never advances the cursor from a snapshot, even a newer one', () => {
    const state = apply(initialCardStoreState, frame(event(1)), snapshot(50));
    expect(state.lastSequence).toBe(1);
    expect(state.card?.revision).toBe(50);
  });
});

describe('cardStore — §5.3 rule 2: exact-sequence append', () => {
  it('appends sequence 1 onto an empty log', () => {
    const next = apply(initialCardStoreState, frame(event(1)));
    expect(next.events.map((e) => e.sequence)).toEqual([1]);
    expect(next.lastSequence).toBe(1);
  });

  it('appends only the exact successor', () => {
    const state = apply(initialCardStoreState, frame(event(1)), frame(event(2)));
    const skipped = apply(state, frame(event(5)));
    expect(skipped.events.map((e) => e.sequence)).toEqual([1, 2]);
    expect(skipped.lastSequence).toBe(2);
    expect(skipped.replay).toEqual({ afterSequence: 2 });
  });

  it('preserves canonical camelCase fields on append', () => {
    const next = apply(initialCardStoreState, frame(event(1, { payload: { message: 'tests running' } })));
    expect(next.events[0]?.payload).toEqual({ message: 'tests running' });
    expect(next.events[0]?.eventType).toBe('agent_progress');
    expect(next.events[0]?.actorKind).toBe('agent');
  });
});

describe('cardStore — §5.3 rule 3: gap → replay request', () => {
  it('requests a replay from lastSequence and applies nothing', () => {
    const at7 = apply(initialCardStoreState, ...Array.from({ length: 7 }, (_, i) => frame(event(i + 1))));
    const gapped = apply(at7, frame(event(9)));

    expect(gapped.replay).toEqual({ afterSequence: 7 });
    expect(gapped.events).toHaveLength(7);
    expect(gapped.lastSequence).toBe(7);
    // No synthetic event, no buffered out-of-order event.
    expect(gapped.events.some((e) => e.sequence === 9)).toBe(false);
  });

  it('requests a replay from 0 when the very first event is out of order', () => {
    const gapped = apply(initialCardStoreState, frame(event(4)));
    expect(gapped.replay).toEqual({ afterSequence: 0 });
    expect(gapped.events).toEqual([]);
  });

  it('does not re-request an identical pending replay', () => {
    const gapped = apply(initialCardStoreState, frame(event(4)));
    const again = apply(gapped, frame(event(6)));
    expect(again).toBe(gapped);
  });

  it('clears the replay request once the gap is filled contiguously', () => {
    const gapped = apply(initialCardStoreState, frame(event(4)));
    expect(gapped.replay).toEqual({ afterSequence: 0 });

    const filled = apply(gapped, frame(event(1)));
    expect(filled.replay).toBeNull();
    expect(filled.lastSequence).toBe(1);
  });
});

describe('cardStore — §5.3 rule 5: duplicates are no-ops', () => {
  it('ignores a duplicate sequence', () => {
    const state = apply(initialCardStoreState, frame(event(1)), frame(event(2)));
    const duplicate = apply(state, frame(event(2, { payload: { replayed: true } })));
    expect(duplicate).toBe(state);
    expect(duplicate.events).toHaveLength(2);
  });

  it('ignores an event at or below the cursor', () => {
    const state = apply(initialCardStoreState, frame(event(1)), frame(event(2)), frame(event(3)));
    expect(apply(state, frame(event(1)))).toBe(state);
    expect(apply(state, frame(event(3)))).toBe(state);
  });
});

describe('cardStore — heartbeat', () => {
  it('changes nothing (liveness belongs to the transport)', () => {
    const state = apply(initialCardStoreState, frame(event(1)), snapshot(1));
    expect(apply(state, heartbeat)).toBe(state);
  });
});

describe('cardStore — §5.3 rule 4: a rejected payload is non-destructive', () => {
  const good = apply(initialCardStoreState, snapshot(3), frame(event(1)), frame(event(2)));

  it('files an error entry and leaves card, events and cursor untouched', () => {
    const next = reduceCardStore(good, {
      type: 'payload_rejected',
      sequence: 3,
      eventId: '0191a9c3-0000-7000-8000-0000000000ff',
      issues: ['event_type: unrecognized card event type "card_hologram"'],
    });

    expect(next.errors).toHaveLength(1);
    expect(next.errors[0]).toEqual({
      sequence: 3,
      eventId: '0191a9c3-0000-7000-8000-0000000000ff',
      issues: ['event_type: unrecognized card event type "card_hologram"'],
    });
    expect(next.card).toBe(good.card);
    expect(next.events).toBe(good.events);
    expect(next.lastSequence).toBe(good.lastSequence);
    expect(next.rejected).toBe(1);
  });

  it('caps the error list but keeps counting', () => {
    let state = initialCardStoreState;
    for (let i = 0; i < MAX_CARD_STORE_ERRORS + 10; i++) {
      state = reduceCardStore(state, {
        type: 'payload_rejected',
        sequence: i,
        eventId: null,
        issues: ['bad'],
      });
    }
    expect(state.errors).toHaveLength(MAX_CARD_STORE_ERRORS);
    expect(state.rejected).toBe(MAX_CARD_STORE_ERRORS + 10);
    // Newest last: the retained window ends at the last rejection.
    expect(state.errors[MAX_CARD_STORE_ERRORS - 1]?.sequence).toBe(MAX_CARD_STORE_ERRORS + 9);
  });

  it('reset returns the initial state', () => {
    expect(reduceCardStore(good, { type: 'reset' })).toEqual(initialCardStoreState);
  });
});

describe('cardStore — ingestCardStreamPayload (raw frame → state)', () => {
  const SNAPSHOT_FRAME = JSON.stringify({
    event_type: 'card_snapshot',
    card_id: CARD_ID,
    card_type: 'iteration',
    sequence: 3,
    timestamp: '2026-07-22T12:00:00Z',
    data: {
      id: CARD_ID,
      tree_id: '0191a9c3-0000-7000-8000-0000000000aa',
      node_id: '0191a9c3-0000-7000-8000-0000000000bb',
      app_id: 'canopy',
      type: 'iteration',
      status: 'active',
      context_hash: 'c4a2f1d6c7b8e9f00112233445566778899aabbccddeeff0011223344556677',
      data: { step: 3 },
      actions: [],
      last_event_seq: 3,
      created_at: '2026-07-22T12:00:00Z',
    },
  });

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
        payload: { sequence },
        created_at: '2026-07-22T12:00:00Z',
      },
    });
  }

  it('folds a snapshot and the following contiguous events', () => {
    let state = ingestCardStreamPayload(initialCardStoreState, SNAPSHOT_FRAME);
    expect(state.card?.revision).toBe(3);
    expect(state.lastSequence).toBe(0);

    state = ingestCardStreamPayload(state, eventFrame(1));
    state = ingestCardStreamPayload(state, eventFrame(2));
    expect(state.lastSequence).toBe(2);
    expect(state.events.map((e) => e.sequence)).toEqual([1, 2]);
    expect(state.errors).toEqual([]);
  });

  it('files an unparsable payload as an error and keeps the good state', () => {
    const good = ingestCardStreamPayload(initialCardStoreState, SNAPSHOT_FRAME);
    const rejected = ingestCardStreamPayload(good, '{"event_type":"card_event","card_id":');
    expect(rejected.errors).toHaveLength(1);
    expect(rejected.card).toBe(good.card);
    expect(rejected.events).toBe(good.events);
    expect(rejected.lastSequence).toBe(good.lastSequence);
  });

  it('files a body that fails the canonical unions, with its sequence', () => {
    const good = ingestCardStreamPayload(initialCardStoreState, SNAPSHOT_FRAME);
    const badBody = JSON.stringify({
      event_type: 'card_event',
      card_id: CARD_ID,
      sequence: 4,
      timestamp: '2026-07-22T12:00:00Z',
      data: {
        sequence: 4,
        event_id: '0191a9c3-0000-7000-8000-0000000000ff',
        card_id: CARD_ID,
        event_type: 'card_hologram',
        actor_kind: 'agent',
        actor_id: 'coding',
        payload: {},
        created_at: '2026-07-22T12:00:00Z',
      },
    });
    const rejected = ingestCardStreamPayload(good, badBody);
    expect(rejected.errors).toHaveLength(1);
    expect(rejected.errors[0]?.sequence).toBe(4);
    expect(rejected.errors[0]?.issues.join(' ')).toContain('card_hologram');
    expect(rejected.card).toBe(good.card);
    expect(rejected.events).toEqual([]);
  });
});
