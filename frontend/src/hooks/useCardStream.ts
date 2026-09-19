/**
 * Hermes Canopy — useCardStream (SPEC-PL-03 §9: live card stream).
 *
 * One live subscription per card, driving the §5.3 store
 * (`frontend/src/lib/cardStore.ts`) and exposing its state to React.
 *
 *   GET /api/v1/cards/{card_id}/events[?after_sequence={n}]   (text/event-stream)
 *   event: card_snapshot | card_event | heartbeat
 *
 * Transport is the app's only SSE client, `subscribeSse`
 * (`frontend/src/lib/sse.ts`) — it carries the bearer token, lists the named
 * events this stream emits, and owns its single bounded retry. Native
 * `EventSource` cannot send `Authorization`, so it is never used directly here.
 *
 * Gap handling (§5.3 / §9.1): when the store reports a gap it holds a replay
 * request for `after_sequence = lastSequence`. This hook tears the stream down
 * and re-subscribes ONCE with that cursor. The reconnecing cursor is recorded,
 * so the same gap can never spin a loop; a second, different gap may reconnect
 * again. The snapshot frame is never used as a cursor (it is not a position in
 * the log, §9.1 handler comment) — only `lastSequence` is.
 *
 * Connection state is a single `connection` field plus `lastHeartbeatAt`: a
 * stream is `'open'` when it connects and is promoted to `'live'` only by a
 * heartbeat frame, so liveness is distinguishable from a cleanly `'closed'` or
 * failed `'error'` transport.
 */

import { useEffect, useRef, useState } from 'react';
import { apiUrl } from '../lib/api.ts';
import { subscribeSse, type SseSubscription } from '../lib/sse.ts';
import {
  initialCardStoreState,
  ingestCardStreamPayload,
  type CardReplayRequest,
  type CardStoreError,
  type CardStoreState,
} from '../lib/cardStore.ts';
import { cardSSEEventNames, type Card, type CardEvent } from '../types/card.ts';

/**
 * `connecting` — a subscription is being opened (initial connect or a gap
 * replay); `open` — the stream is up but no heartbeat has arrived yet;
 * `live` — a heartbeat has arrived (server-side liveness proven); `error` —
 * the transport failed; `closed` — the stream ended; `idle` — no card id.
 */
export type CardConnectionState = 'idle' | 'connecting' | 'open' | 'live' | 'error' | 'closed';

export interface UseCardStreamReturn {
  /** The current canonical card, or null before the first snapshot. */
  card: Card | null;
  /** Applied events, contiguous by sequence and ascending. */
  events: CardEvent[];
  /** Highest applied event sequence (never advanced by a snapshot). */
  lastSequence: number;
  /** Rejected payloads — non-destructive, the card/event state is untouched. */
  errors: CardStoreError[];
  /** Total rejected payload count (≥ `errors.length`; the list is capped). */
  rejected: number;
  /** Non-null while a gap is unfilled (a replay is in flight). */
  replay: CardReplayRequest | null;
  /** Transport state; see `CardConnectionState`. */
  connection: CardConnectionState;
  /** True once a heartbeat has arrived on the current subscription. */
  isLive: boolean;
  /** Wall-clock ms of the last heartbeat frame, or null. */
  lastHeartbeatAt: number | null;
}

/**
 * @param cardId The card UUID to stream. `null`/empty closes the stream and
 *               leaves the hook `idle` (no subscription is opened).
 */
export function useCardStream(cardId: string | null): UseCardStreamReturn {
  const [state, setState] = useState<CardStoreState>(initialCardStoreState);
  const [connection, setConnection] = useState<CardConnectionState>('idle');
  const [lastHeartbeatAt, setLastHeartbeatAt] = useState<number | null>(null);

  // The subscription callbacks are not React closures: they must read the
  // freshest store state synchronously (to detect a gap in the SAME handler
  // call that produced it) and they must survive re-renders.
  const stateRef = useRef<CardStoreState>(initialCardStoreState);
  const attemptedCursorsRef = useRef<Set<number>>(new Set());
  const subscriptionRef = useRef<SseSubscription | null>(null);

  useEffect(() => {
    // Card switch = fresh stream and fresh store (§5.3 state is per card).
    stateRef.current = initialCardStoreState;
    attemptedCursorsRef.current = new Set();
    setState(initialCardStoreState);
    setConnection('idle');
    setLastHeartbeatAt(null);

    if (!cardId) return;

    let cancelled = false;

    const close = (): void => {
      const subscription = subscriptionRef.current;
      subscriptionRef.current = null;
      // Idempotent: close() on the SSE subscription is safe to call twice.
      subscription?.close();
    };

    const open = (cursor: number | null): void => {
      close();
      if (cancelled) return;

      setConnection('connecting');
      const query = cursor === null ? '' : `?after_sequence=${cursor}`;
      const url = apiUrl(`/cards/${encodeURIComponent(cardId)}/events${query}`);

      subscriptionRef.current = subscribeSse(url, {
        eventTypes: cardSSEEventNames,
        onOpen: () => {
          if (!cancelled) setConnection((prev) => (prev === 'live' ? prev : 'open'));
        },
        onError: () => {
          if (!cancelled) setConnection('error');
        },
        onClose: () => {
          if (!cancelled) setConnection('closed');
        },
        onEvent: (type, data) => {
          if (cancelled) return;

          if (type === 'heartbeat') {
            setLastHeartbeatAt(Date.now());
            setConnection('live');
            return;
          }
          if (type !== 'card_snapshot' && type !== 'card_event') return;

          const next = ingestCardStreamPayload(stateRef.current, data);
          stateRef.current = next;
          setState(next);

          const replay = next.replay;
          if (replay === null) return;
          if (attemptedCursorsRef.current.has(replay.afterSequence)) return;
          attemptedCursorsRef.current.add(replay.afterSequence);
          // One bounded reconnect per distinct gap cursor — never a loop.
          open(replay.afterSequence);
        },
      });
    };

    open(null);

    return () => {
      cancelled = true;
      close();
    };
  }, [cardId]);

  return {
    card: state.card,
    events: state.events,
    lastSequence: state.lastSequence,
    errors: state.errors,
    rejected: state.rejected,
    replay: state.replay,
    connection,
    isLive: connection === 'live',
    lastHeartbeatAt,
  };
}
