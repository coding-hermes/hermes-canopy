/**
 * Unit tests — card wire schemas, canonical adapters and frame parsing
 * (SPEC-PL-03 §5.1/§5.2/§9.1).
 *
 * The payloads below are copies of the REAL frame bodies the shipped handler
 * emits (`internal/handler/card_events_handler.go` `cardSSEEventBody` /
 * `cardSSESnapshotBody` / `cardSSEHeartbeatBody`, verified against
 * `internal/handler/card_events_handler_test.go`), not the spec's illustrative
 * §9.1 example — the two differ (snake_case rows, `type` not `card_type`, no
 * `revision`). A schema that rejects these is the bug this suite exists to
 * catch.
 */

import { describe, it, expect } from 'vitest';
import {
  cardFromSummaryWire,
  cardEventFromWire,
  parseCardEventWire,
  parseCardSSEEnvelope,
  parseCardStreamData,
  parseCardSummaryWire,
  cardTypeLabel,
  cardStatusLabel,
  cardSSEEventNames,
} from '../card.ts';

const CARD_ID = '0191a9c3-0000-7000-8000-000000000000';
const TREE_ID = '0191a9c3-0000-7000-8000-0000000000aa';
const NODE_ID = '0191a9c3-0000-7000-8000-0000000000bb';
const EVENT_ID = '0191a9c3-0000-7000-8000-000000000001';
const CONTEXT_HASH = 'c4a2f1d6c7b8e9f00112233445566778899aabbccddeeff0011223344556677';

/** `event: card_event` — a stored row, snake_case. */
const CARD_EVENT_FRAME = JSON.stringify({
  event_type: 'card_event',
  card_id: CARD_ID,
  card_type: 'iteration',
  sequence: 42,
  timestamp: '2026-07-22T12:00:00Z',
  data: {
    sequence: 42,
    event_id: EVENT_ID,
    card_id: CARD_ID,
    event_type: 'agent_progress',
    actor_kind: 'agent',
    actor_id: 'coding',
    payload: { message: 'tests running' },
    created_at: '2026-07-22T12:00:00Z',
  },
});

/** `event: card_snapshot` — `service.CardSummary`, the REST card body. */
const CARD_SNAPSHOT_FRAME = JSON.stringify({
  event_type: 'card_snapshot',
  card_id: CARD_ID,
  card_type: 'compact',
  sequence: 7,
  timestamp: '2026-07-22T12:00:00Z',
  data: {
    id: CARD_ID,
    tree_id: TREE_ID,
    node_id: NODE_ID,
    app_id: 'canopy',
    type: 'compact',
    status: 'active',
    context_hash: CONTEXT_HASH,
    data: { title: 'Nightly build' },
    actions: [{ label: 'Run', handler: 'run' }],
    last_event_seq: 7,
    created_at: '2026-07-22T12:00:00Z',
  },
});

/** `event: heartbeat` — no `id:` line, no `card_type` on the envelope. */
const HEARTBEAT_FRAME = JSON.stringify({
  event_type: 'heartbeat',
  card_id: CARD_ID,
  timestamp: '2026-07-22T12:00:00Z',
  data: { card_id: CARD_ID },
});

/** The same heartbeat as Go actually marshals it: `sequence` present, zero. */
const HEARTBEAT_FRAME_WITH_SEQUENCE = JSON.stringify({
  event_type: 'heartbeat',
  card_id: CARD_ID,
  card_type: undefined,
  sequence: 0,
  timestamp: '2026-07-22T12:00:00Z',
  data: { card_id: CARD_ID },
});

function json(raw: string): unknown {
  return JSON.parse(raw) as unknown;
}

describe('card wire schemas — accept the real server payloads', () => {
  it('validates a stored card-event row', () => {
    const envelope = parseCardSSEEnvelope(json(CARD_EVENT_FRAME));
    expect(envelope.ok).toBe(true);
    if (!envelope.ok) return;

    const row = parseCardEventWire(envelope.value.data);
    expect(row.ok).toBe(true);
    if (!row.ok) return;
    expect(row.value.sequence).toBe(42);
    expect(row.value.event_id).toBe(EVENT_ID);
    expect(row.value.event_type).toBe('agent_progress');
    expect(row.value.actor_kind).toBe('agent');
  });

  it('validates a card summary body (wire name is `type`, not `card_type`)', () => {
    const envelope = parseCardSSEEnvelope(json(CARD_SNAPSHOT_FRAME));
    expect(envelope.ok).toBe(true);
    if (!envelope.ok) return;

    const summary = parseCardSummaryWire(envelope.value.data);
    expect(summary.ok).toBe(true);
    if (!summary.ok) return;
    expect(summary.value.type).toBe('compact');
    expect(summary.value.last_event_seq).toBe(7);
    expect(summary.value.actions).toEqual([{ label: 'Run', handler: 'run' }]);
  });

  it('accepts a heartbeat with no card_type and no sequence', () => {
    const envelope = parseCardSSEEnvelope(json(HEARTBEAT_FRAME));
    expect(envelope.ok).toBe(true);
    if (!envelope.ok) return;
    expect(envelope.value.event_type).toBe('heartbeat');
    expect(envelope.value.card_type).toBeUndefined();
    expect(envelope.value.sequence).toBeUndefined();
  });

  it('accepts the heartbeat Go marshals with sequence 0', () => {
    const envelope = parseCardSSEEnvelope(json(HEARTBEAT_FRAME_WITH_SEQUENCE));
    expect(envelope.ok).toBe(true);
    if (!envelope.ok) return;
    expect(envelope.value.sequence).toBe(0);
  });

  it('normalises a null actions slice (nil Go slice) to []', () => {
    const summary = parseCardSummaryWire({
      ...(json(CARD_SNAPSHOT_FRAME) as { data: Record<string, unknown> }).data,
      actions: null,
    });
    expect(summary.ok).toBe(true);
    if (!summary.ok) return;
    const card = cardFromSummaryWire(summary.value);
    expect(card.ok).toBe(true);
    if (!card.ok) return;
    expect(card.value.actions).toEqual([]);
  });

  it('accepts an RFC 3339 timestamp carrying a numeric UTC offset', () => {
    const envelope = parseCardSSEEnvelope({
      event_type: 'heartbeat',
      card_id: CARD_ID,
      timestamp: '2026-07-22T07:00:00.123456789-05:00',
      data: {},
    });
    expect(envelope.ok).toBe(true);
  });
});

describe('card wire schemas — reject malformed payloads', () => {
  it('rejects an event row with no event_id', () => {
    const row = parseCardEventWire({
      sequence: 1,
      card_id: CARD_ID,
      event_type: 'agent_progress',
      actor_kind: 'agent',
      actor_id: 'coding',
      payload: {},
      created_at: '2026-07-22T12:00:00Z',
    });
    expect(row.ok).toBe(false);
    if (row.ok) return;
    expect(row.issues.join(' ')).toContain('event_id');
  });

  it('rejects an unknown SSE event name', () => {
    const envelope = parseCardSSEEnvelope({
      event_type: 'card_ping',
      card_id: CARD_ID,
      timestamp: '2026-07-22T12:00:00Z',
      data: {},
    });
    expect(envelope.ok).toBe(false);
    if (envelope.ok) return;
    expect(envelope.issues.join(' ')).toContain('event_type');
  });

  it('rejects a summary whose last_event_seq is not an integer', () => {
    const summary = parseCardSummaryWire({
      ...(json(CARD_SNAPSHOT_FRAME) as { data: Record<string, unknown> }).data,
      last_event_seq: 'seven',
    });
    expect(summary.ok).toBe(false);
    if (summary.ok) return;
    expect(summary.issues.join(' ')).toContain('last_event_seq');
  });
});

describe('canonical adapters', () => {
  it('maps a snapshot body to the §5.1 Card (snake_case → camelCase, revision from last_event_seq)', () => {
    const envelope = parseCardSSEEnvelope(json(CARD_SNAPSHOT_FRAME));
    if (!envelope.ok) throw new Error('fixture envelope failed to parse');
    const summary = parseCardSummaryWire(envelope.value.data);
    if (!summary.ok) throw new Error('fixture summary failed to parse');
    const card = cardFromSummaryWire(summary.value);

    expect(card.ok).toBe(true);
    if (!card.ok) return;
    expect(card.value.cardType).toBe('compact');
    expect(card.value.appId).toBe('canopy');
    expect(card.value.treeId).toBe(TREE_ID);
    expect(card.value.nodeId).toBe(NODE_ID);
    expect(card.value.contextHash).toBe(CONTEXT_HASH);
    expect(card.value.status).toBe('active');
    // The wire has no `revision`: the summary's last_event_seq is the ordering key.
    expect(card.value.revision).toBe(7);
    expect(card.value.data).toEqual({ title: 'Nightly build' });
  });

  it('maps a stored event row to the §5.1 CardEvent', () => {
    const envelope = parseCardSSEEnvelope(json(CARD_EVENT_FRAME));
    if (!envelope.ok) throw new Error('fixture envelope failed to parse');
    const row = parseCardEventWire(envelope.value.data);
    if (!row.ok) throw new Error('fixture row failed to parse');
    const event = cardEventFromWire(row.value);

    expect(event.ok).toBe(true);
    if (!event.ok) return;
    expect(event.value).toEqual({
      sequence: 42,
      eventId: EVENT_ID,
      cardId: CARD_ID,
      eventType: 'agent_progress',
      actorKind: 'agent',
      actorId: 'coding',
      payload: { message: 'tests running' },
      createdAt: '2026-07-22T12:00:00Z',
    });
  });

  it('refuses to mis-label an unknown status or card type', () => {
    const base = (json(CARD_SNAPSHOT_FRAME) as { data: Record<string, unknown> }).data;

    const badStatus = parseCardSummaryWire({ ...base, status: 'cancelled' });
    if (!badStatus.ok) throw new Error('fixture status payload failed to parse');
    const statusResult = cardFromSummaryWire(badStatus.value);
    expect(statusResult.ok).toBe(false);
    if (statusResult.ok) return;
    expect(statusResult.issues.join(' ')).toContain('cancelled');

    const badType = parseCardSummaryWire({ ...base, type: 'hologram' });
    if (!badType.ok) throw new Error('fixture type payload failed to parse');
    expect(cardFromSummaryWire(badType.value).ok).toBe(false);

    const badEventType = parseCardEventWire({
      ...(json(CARD_EVENT_FRAME) as { data: Record<string, unknown> }).data,
      event_type: 'card_teleported',
    });
    if (!badEventType.ok) throw new Error('fixture event payload failed to parse');
    expect(cardEventFromWire(badEventType.value).ok).toBe(false);

    const badActor = parseCardEventWire({
      ...(json(CARD_EVENT_FRAME) as { data: Record<string, unknown> }).data,
      actor_kind: 'robot',
    });
    if (!badActor.ok) throw new Error('fixture actor payload failed to parse');
    expect(cardEventFromWire(badActor.value).ok).toBe(false);
  });
});

describe('parseCardStreamData — the store’s single entry point', () => {
  it('parses a snapshot frame', () => {
    const parsed = parseCardStreamData(CARD_SNAPSHOT_FRAME);
    expect(parsed.ok).toBe(true);
    if (!parsed.ok) return;
    expect(parsed.value.kind).toBe('snapshot');
    if (parsed.value.kind !== 'snapshot') return;
    expect(parsed.value.card.id).toBe(CARD_ID);
    expect(parsed.value.card.revision).toBe(7);
  });

  it('parses an event frame', () => {
    const parsed = parseCardStreamData(CARD_EVENT_FRAME);
    expect(parsed.ok).toBe(true);
    if (!parsed.ok) return;
    expect(parsed.value.kind).toBe('event');
    if (parsed.value.kind !== 'event') return;
    expect(parsed.value.event.sequence).toBe(42);
    expect(parsed.value.event.payload).toEqual({ message: 'tests running' });
  });

  it('parses a heartbeat frame', () => {
    const parsed = parseCardStreamData(HEARTBEAT_FRAME);
    expect(parsed.ok).toBe(true);
    if (!parsed.ok) return;
    expect(parsed.value.kind).toBe('heartbeat');
  });

  it('rejects invalid JSON without throwing', () => {
    const parsed = parseCardStreamData('{"event_type": "card_event", oops');
    expect(parsed.ok).toBe(false);
    if (parsed.ok) return;
    expect(parsed.issues.join(' ')).toContain('invalid JSON');
  });

  it('rejects a valid envelope with an invalid body, reporting its sequence', () => {
    const parsed = parseCardStreamData(
      JSON.stringify({
        event_type: 'card_event',
        card_id: CARD_ID,
        sequence: 42,
        timestamp: '2026-07-22T12:00:00Z',
        data: { sequence: 42, event_id: EVENT_ID },
      }),
    );
    expect(parsed.ok).toBe(false);
    if (parsed.ok) return;
    expect(parsed.sequence).toBe(42);
    expect(parsed.issues.join(' ')).toContain('actor_kind');
  });
});

describe('display helpers', () => {
  it('labels the three card types and statuses', () => {
    expect(cardTypeLabel('compact')).toBe('Compact');
    expect(cardTypeLabel('expanded')).toBe('Expanded');
    expect(cardTypeLabel('iteration')).toBe('Iteration');
    expect(cardStatusLabel('active')).toBe('Active');
    expect(cardStatusLabel('dismissed')).toBe('Dismissed');
    expect(cardStatusLabel('archived')).toBe('Archived');
  });

  it('names exactly the three stream events §9.3 maps', () => {
    expect([...cardSSEEventNames]).toEqual(['card_snapshot', 'card_event', 'heartbeat']);
  });
});
