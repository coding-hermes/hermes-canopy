/**
 * Hermes Canopy — card event store (SPEC-PL-03 §5.3 client store rules).
 *
 * A pure, framework-free reducer over the card SSE stream. Two adaptations to
 * this repo's shipped wire, both named where they come from:
 *
 *  - **The snapshot ordering key is `last_event_seq`, not `revision`.** §5.3
 *    says "insert a card_snapshot only when its revision is newer than local
 *    state"; the wire has no `revision` field — the snapshot body is
 *    `service.CardSummary`, whose only monotonic counter is `last_event_seq`
 *    (`internal/service/card_service.go:24-37`). The canonical `Card` carries it
 *    as `revision` (`frontend/src/types/card.ts` module header), and that is
 *    what is compared here.
 *  - **The wire is snake_case; the canonical state is camelCase.** Stored rows
 *    use `event_id`/`actor_kind`/`payload`/`created_at`
 *    (`internal/service/card_service.go:91-100`); the adapters in
 *    `frontend/src/types/card.ts` do the mapping, so nothing here sees snake_case.
 *
 * The rules implemented, verbatim from §5.3:
 *
 *  1. a `card_snapshot` is applied ONLY when its ordering key is strictly newer
 *     than the stored one;
 *  2. a `card_event` is appended ONLY when `sequence === lastSequence + 1`;
 *  3. a gap (`sequence > lastSequence + 1`) produces a REPLAY REQUEST carrying
 *     `after_sequence = lastSequence` — no synthetic event is invented and no
 *     out-of-order event is buffered as if it had been applied;
 *  4. a payload that fails validation becomes a non-destructive error entry and
 *     mutates neither the card nor the event list;
 *  5. a duplicate (`sequence <= lastSequence`) is a no-op: the server already
 *     dedupes across its replay/live boundary, the client must not double-apply.
 *
 * Transport is deliberately out of scope: this module owns no subscription, no
 * timer and no clock, so every rule above is a table-driven unit test.
 */

import {
  parseCardStreamData,
  type Card,
  type CardEvent,
  type CardStreamFrame,
} from '../types/card.ts';

/** A pending gap replay: re-subscribe with `after_sequence = afterSequence`. */
export interface CardReplayRequest {
  afterSequence: number;
}

/** One rejected payload, filed without touching card/event state (§5.3). */
export interface CardStoreError {
  /** Envelope `sequence` (or the nested row's sequence) when known. */
  sequence: number | null;
  /** The stored event's `event_id` when the payload got that far. */
  eventId: string | null;
  /** Human-readable validation failures. */
  issues: string[];
}

export interface CardStoreState {
  /** Last accepted snapshot. `null` until one arrives. */
  card: Card | null;
  /** Accepted events, contiguous by sequence and in ascending order. */
  events: CardEvent[];
  /** Highest applied sequence. A snapshot NEVER advances this (§9.1). */
  lastSequence: number;
  /** Non-null while a gap is unfilled. Drives the re-subscribe. */
  replay: CardReplayRequest | null;
  /** Non-destructive rejections, newest last, capped at `MAX_CARD_STORE_ERRORS`. */
  errors: CardStoreError[];
  /** Monotonic count of rejected payloads (survives the `errors` cap). */
  rejected: number;
}

/** Keep the rejection list bounded — a broken stream must not grow forever. */
export const MAX_CARD_STORE_ERRORS = 50;

export const initialCardStoreState: CardStoreState = {
  card: null,
  events: [],
  lastSequence: 0,
  replay: null,
  errors: [],
  rejected: 0,
};

export type CardStoreAction =
  | { type: 'stream_frame'; frame: CardStreamFrame }
  | { type: 'payload_rejected'; sequence: number | null; eventId: string | null; issues: string[] }
  | { type: 'reset' };

function appendEvent(state: CardStoreState, event: CardEvent): CardStoreState {
  // The gap this replay request asked to be filled starts at
  // afterSequence + 1, so a contiguous append from that point closes it.
  const replay =
    state.replay !== null && event.sequence === state.replay.afterSequence + 1
      ? null
      : state.replay;

  return {
    ...state,
    events: [...state.events, event],
    lastSequence: event.sequence,
    replay,
  };
}

function reduceFrame(state: CardStoreState, frame: CardStreamFrame): CardStoreState {
  switch (frame.kind) {
    case 'snapshot':
      // Rule 1: strictly newer only. `revision` is the summary's
      // `last_event_seq` (see the module header). An equal key is the common
      // reconnect case and must NOT churn state.
      if (state.card !== null && frame.card.revision <= state.card.revision) return state;
      // Rule 2 is about events: a snapshot is not a log position, so
      // `lastSequence` is left exactly as it was (§9.1 handler comment).
      return { ...state, card: frame.card };

    case 'event': {
      const seq = frame.event.sequence;
      // Rule 5: already applied (server replay/live boundary).
      if (seq <= state.lastSequence) return state;
      // Rule 2: contiguous append.
      if (seq === state.lastSequence + 1) return appendEvent(state, frame.event);
      // Rule 3: gap. Ask for a replay from the last APPLIED sequence and apply
      // nothing — not the event, not a placeholder.
      if (state.replay !== null && state.replay.afterSequence === state.lastSequence) return state;
      return { ...state, replay: { afterSequence: state.lastSequence } };
    }

    case 'heartbeat':
      // Liveness only: the hook owns the connection indicator.
      return state;
  }
}

function reduceRejection(
  state: CardStoreState,
  sequence: number | null,
  eventId: string | null,
  issues: string[],
): CardStoreState {
  const entry: CardStoreError = { sequence, eventId, issues };
  const errors = [...state.errors, entry];
  return {
    ...state,
    errors: errors.length > MAX_CARD_STORE_ERRORS ? errors.slice(errors.length - MAX_CARD_STORE_ERRORS) : errors,
    rejected: state.rejected + 1,
  };
}

/** The whole store, as one pure function of (state, action). */
export function reduceCardStore(state: CardStoreState, action: CardStoreAction): CardStoreState {
  switch (action.type) {
    case 'stream_frame':
      return reduceFrame(state, action.frame);
    case 'payload_rejected':
      return reduceRejection(state, action.sequence, action.eventId, action.issues);
    case 'reset':
      return initialCardStoreState;
  }
}

/**
 * Parse one raw `data:` line and fold it into the store — the hook's single
 * entry point. A payload that fails validation is filed as a rejection and
 * leaves the card and event list untouched (§5.3).
 */
export function ingestCardStreamPayload(state: CardStoreState, raw: string): CardStoreState {
  const parsed = parseCardStreamData(raw);
  if (parsed.ok) return reduceCardStore(state, { type: 'stream_frame', frame: parsed.value });
  return reduceCardStore(state, {
    type: 'payload_rejected',
    sequence: parsed.sequence,
    eventId: parsed.eventId,
    issues: parsed.issues,
  });
}
