/**
 * Hermes Canopy — Workspace Page (SPEC-023-UI-002)
 *
 * Shared workspace view: a list of channels on the left, a real-time
 * message feed for the selected channel on the right, and a composer
 * that POSTs to the backend. Consumes the live SSE surface delivered
 * in UI-001 (internal/handler/workspace_handler.go).
 *
 *   GET  /api/v1/workspace/channels                    → channel list
 *   POST /api/v1/workspace/channels/{id}/message       → send a message
 *   GET  /api/v1/workspace/channels/{id}/feed (SSE)    → channel_message stream
 *
 * State is local (no Yjs / zustand for MVP per SPEC-023 §7). The feed
 * is driven by useChannelFeed; the composer calls apiPost and optimistically
 * inserts the echoed message so the UI doesn't wait for the SSE round-trip.
 */

import {
  useState,
  useEffect,
  useCallback,
  useRef,
  type KeyboardEvent,
} from 'react';
import {
  Hash,
  Send,
  RefreshCw,
  AlertCircle,
  Inbox,
  Radio,
  Users,
} from 'lucide-react';
import { apiGet, apiPost } from '../lib/api.ts';
import { useChannelFeed } from '../hooks/useChannelFeed.ts';
import type {
  ChannelSummary,
  ChannelMessage,
  SendMessageResponse,
} from '../types/workspace.ts';

// ─── Types ─────────────────────────────────────────────────────────────

// ─── Helpers ───────────────────────────────────────────────────────────

/** Compact relative-time label, e.g. "3m ago". Falls back to the raw string. */
function formatSentAt(iso: string): string {
  try {
    const ms = Date.now() - new Date(iso).getTime();
    const sec = Math.floor(ms / 1000);
    if (sec < 60) return 'just now';
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ago`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr}h ago`;
    return new Date(iso).toLocaleDateString();
  } catch {
    return iso;
  }
}

/** Short label for a sender id — last 6 chars of a UUID, lowercased. */
function senderLabel(senderId: string): string {
  const tail = senderId.slice(-8).toLowerCase();
  return tail ? `…${tail}` : 'unknown';
}

// ─── Channel list item ─────────────────────────────────────────────────

function ChannelPill({
  channel,
  active,
  subscriberCount,
  onSelect,
}: {
  channel: ChannelSummary;
  active: boolean;
  subscriberCount?: number;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={active ? 'true' : undefined}
      title={channel.name}
      className={[
        'group w-full flex items-center gap-2.5 rounded-lg px-2.5 py-2 transition-colors text-left',
        active
          ? 'bg-accent-2/12 ring-1 ring-inset ring-accent-2/35'
          : 'ring-1 ring-inset ring-transparent hover:bg-surface-hover/50',
      ].join(' ')}
    >
      <span
        aria-hidden="true"
        className={[
          'grid h-7 w-7 shrink-0 place-items-center rounded-md ring-1 ring-inset transition-colors',
          active
            ? 'bg-accent-2/20 text-accent-2-300 ring-accent-2/40'
            : 'bg-surface-input text-content-tertiary ring-line-subtle group-hover:text-content-secondary',
        ].join(' ')}
      >
        <Hash className="h-3.5 w-3.5" />
      </span>
      <span
        className={[
          'flex-1 min-w-0 truncate text-sm',
          active
            ? 'font-medium text-content-primary'
            : 'text-content-tertiary group-hover:text-content-primary',
        ].join(' ')}
      >
        {channel.name}
      </span>
      {typeof subscriberCount === 'number' && subscriberCount > 0 && (
        <span
          className="shrink-0 inline-flex items-center gap-0.5 rounded-sm px-1.5 py-0.5 text-[11px] font-medium tabular-nums ring-1 ring-inset text-content-muted bg-surface-input ring-line-subtle"
          title={`${subscriberCount} listening`}
        >
          <Users className="h-3 w-3" aria-hidden="true" />
          {subscriberCount}
        </span>
      )}
    </button>
  );
}

// ─── Message bubble ────────────────────────────────────────────────────

function MessageBubble({ msg }: { msg: ChannelMessage }) {
  return (
    <div
      data-testid="workspace-message"
      data-message-id={msg.message_id}
      className="flex flex-col gap-1 px-4 py-2"
    >
      <div className="flex items-baseline gap-2">
        <span className="text-xs font-semibold text-accent-2-300">
          {senderLabel(msg.sender_id)}
        </span>
        <span
          className="text-[11px] tabular-nums text-content-faint"
          title={msg.sent_at}
        >
          {formatSentAt(msg.sent_at)}
        </span>
      </div>
      <p className="min-w-0 break-words text-sm text-content-secondary">
        {msg.content}
      </p>
    </div>
  );
}

// ─── Feed panel ────────────────────────────────────────────────────────

function FeedPanel({
  channel,
  messages,
  status,
  hasReceived,
  onAutoScroll,
}: {
  channel: ChannelSummary;
  messages: ChannelMessage[];
  status: ReturnType<typeof useChannelFeed>['status'];
  hasReceived: boolean;
  onAutoScroll: () => void;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);

  // Auto-scroll to the newest message unless the user has scrolled up.
  const maybeScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const nearBottom =
      el.scrollHeight - el.scrollTop - el.clientHeight < 120;
    if (nearBottom) {
      el.scrollTop = el.scrollHeight;
    }
    onAutoScroll();
  }, [onAutoScroll]);

  useEffect(() => {
    maybeScroll();
  }, [messages, maybeScroll]);

  const reconnecting = status === 'error';

  return (
    <section
      aria-label={`Channel ${channel.name} feed`}
      className="flex min-h-0 flex-1 flex-col"
    >
      {/* Feed header */}
      <div className="flex shrink-0 items-center gap-2 border-b border-line-subtle px-4 py-2.5">
        <Hash className="h-4 w-4 text-accent-2-300" aria-hidden="true" />
        <h2 className="min-w-0 truncate text-sm font-semibold tracking-tight text-content-primary">
          {channel.name}
        </h2>
        <span
          data-testid="feed-status"
          className={[
            'ml-auto inline-flex items-center gap-1 rounded-sm px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
            status === 'open'
              ? 'text-status-success bg-surface-input ring-line-subtle'
              : reconnecting
                ? 'text-status-warning bg-surface-input ring-line-subtle'
                : 'text-content-muted bg-surface-input ring-line-subtle',
          ].join(' ')}
        >
          <Radio
            className={`h-3 w-3 ${status === 'connecting' ? 'animate-pulse' : ''}`}
            aria-hidden="true"
          />
          {status === 'open'
            ? 'live'
            : reconnecting
              ? 'reconnecting'
              : 'connecting'}
        </span>
      </div>

      {/* Message list */}
      <div
        ref={scrollRef}
        data-testid="workspace-feed"
        className="min-h-0 flex-1 overflow-y-auto"
      >
        {!hasReceived && (
          <div className="mx-auto mt-12 max-w-xs rounded-lg border border-line-subtle bg-surface-panel px-4 py-6 text-center">
            <Inbox
              className="mx-auto mb-2 h-6 w-6 text-content-faint"
              aria-hidden="true"
            />
            <p className="text-xs font-medium text-content-secondary">
              No messages yet
            </p>
            <p className="mt-1 text-[11px] text-content-muted">
              Send a message to start the conversation.
            </p>
          </div>
        )}
        {messages.map((m) => (
          <MessageBubble key={m.message_id} msg={m} />
        ))}
      </div>
    </section>
  );
}

// ─── Composer ──────────────────────────────────────────────────────────

interface ComposerProps {
  channelId: string;
  disabled?: boolean;
  onSent: (msg: ChannelMessage) => void;
}

function Composer({ channelId, disabled = false, onSent }: ComposerProps) {
  const [text, setText] = useState('');
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const canSend = !disabled && !sending && text.trim().length > 0;

  const send = useCallback(async () => {
    if (!canSend) return;
    const content = text;
    setSending(true);
    setError(null);
    try {
      const res = await apiPost<SendMessageResponse>(
        `/workspace/channels/${encodeURIComponent(channelId)}/message`,
        { content },
      );
      // Optimistic insert: surface the echoed message immediately so the
      // UI doesn't wait for the SSE round-trip (deduped later by message_id).
      onSent({
        message_id: res.message_id,
        channel_id: res.channel_id,
        sender_id: '',
        content: res.content,
        sent_at: new Date().toISOString(),
      });
      setText('');
      inputRef.current?.focus();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to send message');
    } finally {
      setSending(false);
    }
  }, [canSend, text, channelId, onSent]);

  const handleKeyDown = useCallback(
    (e: KeyboardEvent<HTMLInputElement>) => {
      // Enter sends, Shift+Enter inserts a newline (the input is single-line,
      // but we keep the convention consistent with MessageComposer).
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        void send();
      }
    },
    [send],
  );

  return (
    <div className="shrink-0 border-t border-line-subtle px-4 py-3">
      {error && (
        <div
          role="alert"
          data-testid="composer-error"
          className="mb-2 flex items-start gap-2 rounded-md border border-rose-500/30 bg-rose-500/10 p-2 text-[11px] text-status-danger"
        >
          <AlertCircle
            className="mt-px h-3.5 w-3.5 shrink-0"
            aria-hidden="true"
          />
          <span className="min-w-0 break-words">{error}</span>
        </div>
      )}
      <div className="flex items-center gap-2">
        <input
          ref={inputRef}
          type="text"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={handleKeyDown}
          disabled={disabled}
          placeholder={
            disabled ? 'Select a channel…' : 'Message the channel…'
          }
          aria-label="Message the channel"
          className="flex-1 rounded-md bg-surface-input px-3 py-2 text-sm text-content-primary placeholder:text-content-faint ring-1 ring-inset ring-line-subtle transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:opacity-50"
        />
        <button
          type="button"
          onClick={() => void send()}
          disabled={!canSend}
          aria-label="Send message"
          title="Send (Enter)"
          className={[
            'inline-flex items-center gap-1.5 rounded-md px-3 py-2 text-sm font-medium transition-colors',
            canSend
              ? 'bg-accent-2-600 text-white hover:bg-accent-2-500'
              : 'bg-surface-input text-content-faint',
          ].join(' ')}
        >
          <Send className="h-4 w-4" aria-hidden="true" />
          <span className="hidden sm:inline">
            {sending ? 'Sending…' : 'Send'}
          </span>
        </button>
      </div>
    </div>
  );
}

// ─── Page ──────────────────────────────────────────────────────────────

export default function WorkspacePage() {
  const [channels, setChannels] = useState<ChannelSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [listError, setListError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const loadChannels = useCallback(async () => {
    setLoading(true);
    setListError(null);
    try {
      const data = await apiGet<ChannelSummary[]>('/workspace/channels');
      setChannels(data ?? []);
      // Auto-select the first channel once loaded (general is seeded first).
      setSelectedId((prev) => prev ?? data?.[0]?.id ?? null);
    } catch (err) {
      setListError(
        err instanceof Error ? err.message : 'Failed to load channels',
      );
      setChannels([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadChannels();
  }, [loadChannels]);

  // The live feed for the selected channel. We keep a local optimistic
  // buffer merged with the SSE stream so a sent message shows instantly
  // and dedupes once the SSE broadcast arrives.
  const [optimistic, setOptimistic] = useState<ChannelMessage[]>([]);
  const { messages: feedMessages, status } = useChannelFeed(selectedId);

  // Reset the optimistic buffer when switching channels.
  useEffect(() => {
    setOptimistic([]);
  }, [selectedId]);

  // Merge: optimistic messages that haven't appeared in the feed yet
  // (matched by message_id) come first, then the live feed.
  const merged: ChannelMessage[] = (() => {
    const feedIds = new Set(feedMessages.map((m) => m.message_id));
    const pending = optimistic.filter((m) => !feedIds.has(m.message_id));
    // Sort by sent_at for a stable timeline once both sources populate.
    return [...pending, ...feedMessages].sort((a, b) =>
      a.sent_at.localeCompare(b.sent_at),
    );
  })();

  const selected =
    channels.find((c) => c.id === selectedId) ?? null;

  return (
    <div className="flex h-full min-h-0">
      {/* Channel rail */}
      <aside
        aria-label="Workspace channels"
        data-testid="workspace-channels"
        className="flex w-64 shrink-0 flex-col border-r border-line-subtle bg-surface-panel"
      >
        <div className="flex shrink-0 items-center gap-1.5 px-4 pt-3 pb-2">
          <h1 className="flex-1 min-w-0 text-sm font-semibold tracking-tight text-content-primary">
            Workspace
          </h1>
          <button
            type="button"
            onClick={() => void loadChannels()}
            disabled={loading}
            aria-label="Refresh channels"
            title="Refresh channels"
            className="grid h-7 w-7 shrink-0 place-items-center rounded-md text-content-muted transition-colors hover:bg-surface-hover hover:text-content-primary disabled:opacity-50"
          >
            <RefreshCw
              className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`}
              aria-hidden="true"
            />
          </button>
        </div>

        <div className="min-h-0 flex-1 space-y-1 overflow-y-auto px-3 py-1">
          {loading && (
            <div className="space-y-1" aria-hidden="true">
              {[0, 1].map((i) => (
                <div
                  key={i}
                  className="h-11 animate-pulse rounded-lg bg-surface-input/70"
                />
              ))}
            </div>
          )}

          {!loading && listError && (
            <div
              role="alert"
              data-testid="channels-error"
              className="flex items-start gap-2 rounded-lg border border-rose-500/30 bg-rose-500/10 p-2.5 text-[11px] text-status-danger"
            >
              <AlertCircle
                className="mt-px h-3.5 w-3.5 shrink-0"
                aria-hidden="true"
              />
              <span className="min-w-0 break-words">{listError}</span>
            </div>
          )}

          {!loading &&
            !listError &&
            channels.map((ch) => (
              <ChannelPill
                key={ch.id}
                channel={ch}
                active={ch.id === selectedId}
                subscriberCount={ch.subscriber_count}
                onSelect={() => setSelectedId(ch.id)}
              />
            ))}
        </div>
      </aside>

      {/* Feed + composer */}
      <div className="flex min-h-0 flex-1 flex-col">
        {selected ? (
          <>
            <FeedPanel
              channel={selected}
              messages={merged}
              status={status}
              hasReceived={merged.length > 0}
              onAutoScroll={() => undefined}
            />
            <Composer
              channelId={selected.id}
              onSent={(msg) =>
                setOptimistic((prev) => [...prev, msg])
              }
            />
          </>
        ) : (
          <div className="mx-auto mt-16 max-w-sm text-center">
            <Inbox
              className="mx-auto mb-3 h-8 w-8 text-content-faint"
              aria-hidden="true"
            />
            <h2 className="text-sm font-semibold text-content-secondary">
              Select a channel
            </h2>
            <p className="mt-1 text-xs text-content-muted">
              Choose a channel on the left to see its messages.
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
