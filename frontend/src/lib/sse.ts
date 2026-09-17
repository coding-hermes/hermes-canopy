/**
 * Hermes Canopy — SSE client with auth (DF-HERMES-CANOPY-13)
 *
 * The server's AuthMiddleware accepts a bearer token from the `Authorization`
 * header ONLY (`internal/handler/auth.go`), and `isPublicPath()` whitelists just
 * `/health`, `/healthz` and `/version`. Native `EventSource` cannot set request
 * headers, so in a static production build it can never authenticate — every
 * feed 401s with TOKEN_MISSING.
 *
 * This module is the one SSE transport for the app, with two arms:
 *
 *  - **Production (a token resolves): `fetch`-based stream.** `fetch` can send
 *    `Authorization: Bearer <token>`, so the response body is read with a
 *    `ReadableStream` reader and the `text/event-stream` wire format is parsed
 *    here (`event:`, `data:`, `id:`, `retry:`, `:` comments; fields split on the
 *    first colon; multi-line `data` joined with `\n`; events dispatched on a
 *    blank line; CRLF tolerant). An event with no `event:` field is dispatched
 *    as type `message`.
 *  - **Dev (`npm run dev`, no token): native `EventSource`.** The Vite dev proxy
 *    injects the dev JWT (`frontend/vite.config.ts`), so the battle-tested native
 *    transport stays in charge and dev behaviour is unchanged. `credentials:
 *    'include'` maps onto `EventSourceInit.withCredentials`.
 *
 * Transport errors get ONE bounded retry after a short delay — not an infinite
 * reconnect loop (native `EventSource` reconnects on its own; the fetch arm does
 * not pretend to). Callers that need a long-lived feed should re-subscribe from
 * their own state, e.g. the hooks that own a channel id.
 *
 * @see frontend/src/lib/api.ts — `resolveApiToken` / `authHeaders` / `authInit`
 */

import { authHeaders, resolveApiToken } from './api';

/** Delay before the single bounded retry of the fetch arm. */
const RETRY_DELAY_MS = 1000;

export interface SseHandlers {
  /**
   * Every frame, with `data` already joined (multi-line frames arrive as
   * `line1\nline2`) and `type` defaulting to `message` when the frame carried
   * no `event:` field.
   */
  onEvent?(type: string, data: string): void;
  /** Transport/HTTP error. Fires before the bounded retry and again if it fails. */
  onError?(err: unknown): void;
  /** The stream is open and events will follow. */
  onOpen?(): void;
  /** The stream ended (cleanly, or after the retry gave up). Not called on `close()`. */
  onClose?(): void;
  /**
   * Named event types to subscribe to on the native `EventSource` arm — native
   * `EventSource` needs an explicit `addEventListener` per named event. Ignored
   * by the fetch arm, which sees every frame. `message` is always subscribed.
   */
  eventTypes?: readonly string[];
}

export interface SseSubscription {
  /** Stop receiving events and release the connection. Idempotent. */
  close(): void;
}

interface SseFrame {
  type: string;
  data: string;
}

/**
 * Incremental `text/event-stream` decoder.
 *
 * Feed it decoded chunks with `push`. A frame is dispatched by a BLANK LINE
 * only, so a frame the server never terminated is dropped at end of stream —
 * that is the SSE spec, and it matches native `EventSource` (canopyd always
 * terminates its frames with `\n\n`, see internal/sse/sse_client.go).
 */
class SseDecoder {
  private buffer = '';
  private eventType = '';
  private dataLines: string[] = [];

  push(chunk: string): SseFrame[] {
    this.buffer += chunk;
    const frames: SseFrame[] = [];
    let line: string | null;
    while ((line = this.extractLine()) !== null) {
      const frame = this.consumeLine(line);
      if (frame !== null) frames.push(frame);
    }
    return frames;
  }

  /** Next complete line (terminator stripped), or null when more bytes are needed. */
  private extractLine(): string | null {
    const buffer = this.buffer;
    for (let i = 0; i < buffer.length; i++) {
      const ch = buffer[i];
      if (ch === '\n') {
        const line = buffer.slice(0, i);
        this.buffer = buffer.slice(i + 1);
        return line.endsWith('\r') ? line.slice(0, -1) : line;
      }
      if (ch === '\r') {
        // Could be CRLF — wait for the next byte before deciding.
        if (i === buffer.length - 1) return null;
        const line = buffer.slice(0, i);
        this.buffer = buffer[i + 1] === '\n' ? buffer.slice(i + 2) : buffer.slice(i + 1);
        return line;
      }
    }
    return null;
  }

  /** Apply one wire line; returns a frame when the line dispatched an event. */
  private consumeLine(line: string): SseFrame | null {
    if (line === '') return this.dispatch();
    if (line.startsWith(':')) return null; // comment / keep-alive
    const colon = line.indexOf(':');
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? '' : line.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') this.eventType = value;
    else if (field === 'data') this.dataLines.push(value);
    // `id:` and `retry:` are accepted and ignored: the fetch arm reconnects at
    // its own bounded delay and cannot replay Last-Event-ID.
    return null;
  }

  private dispatch(): SseFrame | null {
    if (this.dataLines.length === 0 && this.eventType === '') return null;
    const frame: SseFrame = {
      type: this.eventType === '' ? 'message' : this.eventType,
      data: this.dataLines.join('\n'),
    };
    this.eventType = '';
    this.dataLines = [];
    return frame;
  }
}

/**
 * Subscribe to an SSE endpoint with the shared token source.
 *
 * @param url    Absolute or root-relative feed URL (build it with `apiUrl`).
 * @param handlers Event/error/open/close callbacks.
 * @param init   Optional request init for the fetch arm (`credentials`,
 *               `cache`, extra headers). `credentials: 'include'` is mirrored
 *               onto the `EventSource` fallback's `withCredentials`.
 */
export function subscribeSse(
  url: string,
  handlers: SseHandlers,
  init?: RequestInit,
): SseSubscription {
  if (resolveApiToken() === null) return subscribeViaEventSource(url, handlers, init);
  return subscribeViaFetch(url, handlers, init);
}

/** Dev arm: the Vite dev proxy injects the JWT, so native EventSource works. */
function subscribeViaEventSource(
  url: string,
  handlers: SseHandlers,
  init?: RequestInit,
): SseSubscription {
  if (typeof EventSource === 'undefined') {
    handlers.onError?.(new Error('EventSource is unavailable in this environment'));
    return { close: () => {} };
  }

  const source = new EventSource(url, {
    withCredentials: init?.credentials === 'include',
  });
  source.onopen = () => handlers.onOpen?.();
  source.onerror = (err) => handlers.onError?.(err);

  const types = new Set<string>(['message', ...(handlers.eventTypes ?? [])]);
  const listeners: Array<{ type: string; listener: EventListener }> = [];
  for (const type of types) {
    const listener = ((event: MessageEvent) => {
      const data = event.data;
      handlers.onEvent?.(type, typeof data === 'string' ? data : String(data ?? ''));
    }) as EventListener;
    source.addEventListener(type, listener);
    listeners.push({ type, listener });
  }

  return {
    close() {
      for (const { type, listener } of listeners) source.removeEventListener(type, listener);
      source.close();
    },
  };
}

/** Production arm: `fetch` carries the bearer token; the wire format is parsed here. */
function subscribeViaFetch(
  url: string,
  handlers: SseHandlers,
  init?: RequestInit,
): SseSubscription {
  const controller = new AbortController();
  let closed = false;
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let retried = false;

  const run = async (): Promise<void> => {
    try {
      const response = await fetch(url, { ...init, headers: authHeaders(init?.headers), signal: controller.signal });
      if (closed) return;
      if (!response.ok || response.body === null) {
        throw new Error(`SSE request failed (HTTP ${response.status})`);
      }

      handlers.onOpen?.();
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      const sse = new SseDecoder();

      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        for (const frame of sse.push(decoder.decode(value, { stream: true }))) {
          handlers.onEvent?.(frame.type, frame.data);
        }
      }
      // EOS: an unterminated trailing frame stays unparsed and is discarded.
      if (closed) return;
      handlers.onClose?.();
    } catch (err) {
      if (closed || controller.signal.aborted) return;
      handlers.onError?.(err);
      if (retried) {
        handlers.onClose?.();
        return;
      }
      retried = true;
      retryTimer = setTimeout(() => {
        retryTimer = null;
        void run();
      }, RETRY_DELAY_MS);
    }
  };

  void run();

  return {
    close() {
      closed = true;
      if (retryTimer !== null) {
        clearTimeout(retryTimer);
        retryTimer = null;
      }
      controller.abort();
    },
  };
}
