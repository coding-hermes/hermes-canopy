/**
 * Hermes Canopy — useChannelFeed Hook (SPEC-023-UI-002)
 *
 * Subscribes to a workspace channel's SSE feed and surfaces the live
 * `channel_message` stream as React state. Mirrors the shape of
 * `usePresence.ts` (single live subscription hook) and reuses the
 * backend's SSE envelope contract established by `yjsProvider.ts`.
 *
 * Backend contract (internal/handler/workspace_handler.go):
 *   GET /api/v1/workspace/channels/{channel_id}/feed → text/event-stream
 *
 * The SSE layer wraps every event in an envelope
 * ({event_type, tree_id, data, …}) — the channel_message payload lives
 * in `envelope.data`. Event IDs (`id:` lines) are written by the server,
 * so EventSource auto-replays via the Last-Event-ID header on reconnect;
 * no manual re-subscribe is needed.
 *
 * Error handling: EventSource auto-reconnects natively (the server emits
 * `retry:` advisories). We surface readyState so the view can show a
 * "reconnecting" affordance, and we close cleanly on unmount / channel
 * switch so no orphan connections linger.
 */

import { useState, useEffect, useRef, useCallback } from 'react';
import { apiUrl } from '../lib/api.ts';
import type { ChannelMessage } from '../types/workspace.ts';

// ─── SSE envelope (mirrors yjsProvider.ts SSEEnvelope) ──────────────────

interface SSEEnvelope {
  event_type: string;
  data: unknown;
  sequence_num?: number;
  timestamp?: string;
}

export type FeedStatus = 'connecting' | 'open' | 'error';

export interface UseChannelFeedReturn {
  /** Messages received so far, in arrival order (deduped by message_id). */
  messages: ChannelMessage[];
  /** Current connection status. */
  status: FeedStatus;
  /** True once at least one message has arrived. */
  hasReceived: boolean;
  /** Manually disconnect + reconnect (exposed for tests / refresh). */
  reconnect: () => void;
}

/**
 * @param channelId The channel UUID to subscribe to. Pass null/empty to
 *                  disconnect (no feed opened).
 * @param onMessage Optional callback fired for each new message, in
 *                  addition to the `messages` state update. Useful for
 *                  the view to run side-effects (e.g. auto-scroll).
 */
export function useChannelFeed(
  channelId: string | null,
  onMessage?: (msg: ChannelMessage) => void,
): UseChannelFeedReturn {
  const [messages, setMessages] = useState<ChannelMessage[]>([]);
  const [status, setStatus] = useState<FeedStatus>('connecting');

  // Keep a ref to the latest onMessage so the effect (which only depends
  // on channelId) doesn't tear down + rebuild the EventSource whenever
  // the parent re-renders with a fresh callback identity.
  const onMessageRef = useRef(onMessage);
  onMessageRef.current = onMessage;

  const sourceRef = useRef<EventSource | null>(null);

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
      const src = new EventSource(url);
      sourceRef.current = src;

      src.onopen = () => setStatus('open');

      src.onerror = () => {
        // EventSource auto-reconnects; we only mirror readyState. The
        // browser sets readyState to CONNECTING during the retry window.
        setStatus('error');
      };

      src.addEventListener(
        'channel_message',
        ((e: MessageEvent) => {
          let envelope: SSEEnvelope | undefined;
          try {
            envelope = JSON.parse(e.data as string) as SSEEnvelope;
          } catch {
            return;
          }
          if (!envelope || envelope.event_type !== 'channel_message') return;

          const payload = envelope.data as ChannelMessage | undefined;
          if (!payload?.message_id) return;

          setMessages((prev) => {
            // Dedupe by message_id — a replay after reconnect can
            // re-deliver the last event.
            if (prev.some((m) => m.message_id === payload.message_id)) {
              return prev;
            }
            return [...prev, payload];
          });
          onMessageRef.current?.(payload);
        }) as EventListener,
      );
    },
    [cleanup],
  );

  useEffect(() => {
    // Reset the message history when switching channels.
    setMessages([]);

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
    messages,
    status,
    hasReceived: messages.length > 0,
    reconnect,
  };
}
