/**
 * Hermes Canopy — useReviewFeed Hook (SPEC-023-UI-004)
 *
 * Subscribes to the workspace "general" channel SSE feed and surfaces
 * live `review_event` messages as React state. Mirrors the shape of
 * `useChannelFeed.ts` (which listens for `channel_message` events) but
 * filters for the `review_event` event type broadcast by the review
 * trigger endpoint.
 *
 * Backend contract (internal/handler/review_handler.go):
 *   POST /api/v1/reviews/{pr}/trigger → broadcasts `review_event`
 *   GET  /api/v1/workspace/channels/{channel_id}/feed → SSE stream
 *
 * The SSE layer wraps every event in an envelope
 * ({event_type, tree_id, data, …}); the review_event payload lives in
 * `envelope.data`.
 *
 * Transport (DF-HERMES-CANOPY-13): the feed goes through `lib/sse.ts`, which
 * sends `Authorization: Bearer <token>` via `fetch` in production and falls
 * back to native EventSource in dev (no token → the Vite dev proxy injects
 * the JWT). Native EventSource cannot set headers, so the fetch arm is the
 * only one that works against a static build.
 */

import { useState, useEffect, useRef, useCallback } from 'react';
import { apiUrl } from '../lib/api.ts';
import { subscribeSse, type SseSubscription } from '../lib/sse.ts';
import type { ReviewEvent } from '../types/review.ts';

// ─── SSE envelope (mirrors useChannelFeed.ts) ──────────────────────────

interface SSEEnvelope {
  event_type: string;
  data: unknown;
  sequence_num?: number;
  timestamp?: string;
}

export type FeedStatus = 'connecting' | 'open' | 'error';

export interface UseReviewFeedReturn {
  /** Review events received so far, in arrival order (deduped by review_id + triggered_at). */
  events: ReviewEvent[];
  /** Current connection status. */
  status: FeedStatus;
  /** True once at least one event has arrived. */
  hasReceived: boolean;
  /** Manually disconnect + reconnect (exposed for tests / refresh). */
  reconnect: () => void;
}

/**
 * @param channelId The channel UUID to subscribe to. Pass null/empty to
 *                  disconnect (no feed opened). Defaults to the general
 *                  workspace channel where review events are broadcast.
 * @param onEvent Optional callback fired for each new event.
 */
export function useReviewFeed(
  channelId: string | null,
  onEvent?: (ev: ReviewEvent) => void,
): UseReviewFeedReturn {
  const [events, setEvents] = useState<ReviewEvent[]>([]);
  const [status, setStatus] = useState<FeedStatus>('connecting');

  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  const sourceRef = useRef<SseSubscription | null>(null);

  const cleanup = useCallback(() => {
    const src = sourceRef.current;
    if (src) {
      src.close();
      sourceRef.current = null;
    }
  }, []);

  const connect = useCallback(
    (id: string) => {
      cleanup();
      setStatus('connecting');

      const url = apiUrl(`/workspace/channels/${encodeURIComponent(id)}/feed`);
      sourceRef.current = subscribeSse(url, {
        eventTypes: ['review_event'],
        onOpen: () => setStatus('open'),
        onError: () => {
          setStatus('error');
        },
        onEvent: (type, data) => {
          if (type !== 'review_event') return;

          let envelope: SSEEnvelope | undefined;
          try {
            envelope = JSON.parse(data) as SSEEnvelope;
          } catch {
            return;
          }
          if (!envelope || envelope.event_type !== 'review_event') return;

          const payload = envelope.data as ReviewEvent | undefined;
          if (!payload?.review_id) return;

          setEvents((prev) => {
            // Dedupe by review_id + triggered_at — a replay after
            // reconnect can re-deliver the last event.
            const key = `${payload.review_id}:${payload.triggered_at}`;
            if (prev.some((m) => `${m.review_id}:${m.triggered_at}` === key)) {
              return prev;
            }
            return [...prev, payload];
          });
          onEventRef.current?.(payload);
        },
      });
    },
    [cleanup],
  );

  useEffect(() => {
    setEvents([]);

    if (!channelId) {
      cleanup();
      setStatus('connecting');
      return;
    }

    connect(channelId);
    return cleanup;
  }, [channelId, connect, cleanup]);

  const reconnect = useCallback(() => {
    if (channelId) connect(channelId);
  }, [channelId, connect]);

  return {
    events,
    status,
    hasReceived: events.length > 0,
    reconnect,
  };
}
