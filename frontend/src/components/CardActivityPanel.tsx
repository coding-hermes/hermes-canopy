/**
 * Hermes Canopy — CardActivityPanel (SPEC-PL-03 §6.1 default card frame).
 *
 * One card, one live stream: the panel renders the §6.1 default frame content
 * that is actually derivable from the shipped wire — application identifier,
 * card type label, status badge, creation time, context-hash affordance,
 * activity indicator and the card's declared actions — plus the live event list
 * from `useCardStream` and a connection indicator.
 *
 * Every value is rendered as a React text child. §6.1: "The frame does not
 * render raw arbitrary HTML from `data` or event payloads" — payloads are shown
 * with `JSON.stringify` into a `<pre>` text node, `data` is shown the same way,
 * and this file uses no raw-HTML injection API at all (the panel test asserts
 * that from the source).
 *
 * Actions are submitted through the shared card-action client and show their
 * outcome in the safe frame.
 *
 * §5.3: a payload that fails validation never clears the panel — the rejected
 * payload is filed by the store and shown in a banner while the last good card
 * state stays on screen.
 */

import { AlertCircle, X } from 'lucide-react';
import { useCardStream, type CardConnectionState } from '../hooks/useCardStream.ts';
import { invokeCardAction } from '../lib/cardActions.ts';
import type { CardStoreError } from '../lib/cardStore.ts';
import CardFrame from './CardFrame.tsx';

const CONNECTION_LABEL: Record<CardConnectionState, string> = {
  idle: 'Idle',
  connecting: 'Connecting…',
  open: 'Open',
  live: 'Live',
  error: 'Stream error',
  closed: 'Closed',
};

const CONNECTION_DOT: Record<CardConnectionState, string> = {
  idle: 'bg-content-faint',
  connecting: 'bg-amber-400 animate-pulse',
  open: 'bg-amber-400',
  live: 'bg-green-400',
  error: 'bg-rose-500',
  closed: 'bg-content-muted',
};

/** Relative time for a wire timestamp, which is RFC 3339 with an offset. */
function formatTimeAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (!Number.isFinite(ms)) return iso;
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${Math.max(sec, 0)}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  return `${Math.floor(hr / 24)}d ago`;
}

function RejectionBanner({ errors }: { errors: CardStoreError[] }) {
  const newest = errors[errors.length - 1];
  if (newest === undefined) return null;

  return (
    <div
      role="alert"
      className="mx-4 mt-3 p-3 rounded-lg bg-rose-500/10 border border-rose-500/30 text-status-danger text-xs"
    >
      <div className="flex items-center gap-2 font-medium">
        <AlertCircle className="w-3.5 h-3.5 flex-shrink-0" aria-hidden="true" />
        <span>
          {errors.length} card {errors.length === 1 ? 'payload' : 'payloads'} rejected — last good
          state kept
        </span>
      </div>
      <ul className="mt-1.5 space-y-1">
        {errors.slice(-3).map((error, index) => (
          <li key={`${error.sequence ?? 'n'}-${error.eventId ?? 'x'}-${index}`} className="font-mono break-words">
            {error.sequence !== null ? `seq ${error.sequence}` : 'unknown seq'}
            {error.eventId !== null ? ` · ${error.eventId.slice(0, 8)}…` : ''} — {error.issues.join('; ')}
          </li>
        ))}
      </ul>
    </div>
  );
}

export default function CardActivityPanel({
  cardId,
  onClose,
}: {
  cardId: string;
  onClose: () => void;
}) {
  const { card, events, lastSequence, errors, rejected, replay, connection, isLive } =
    useCardStream(cardId);
  return (
    <aside
      aria-label="Card activity"
      className="fixed inset-y-0 right-0 z-40 w-full max-w-md flex flex-col bg-surface-panel border-l border-line-subtle shadow-xl"
    >
      {/* Header */}
      <div className="px-4 py-3 border-b border-line-subtle flex items-center gap-2">
        <span className="text-sm font-medium text-content-primary">Card activity</span>
        <span
          role="status"
          aria-live="polite"
          className="flex items-center gap-1.5 text-[11px] text-content-muted"
        >
          <span
            aria-hidden="true"
            className={`w-2 h-2 rounded-full flex-shrink-0 ${CONNECTION_DOT[connection]}`}
          />
          {CONNECTION_LABEL[connection]}
        </span>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close card activity"
          className="ml-auto p-1.5 rounded-md text-content-faint hover:text-content-primary hover:bg-surface-hover transition-colors"
        >
          <X className="w-4 h-4" />
        </button>
      </div>

      <RejectionBanner errors={errors} />

      <div className="flex-1 overflow-y-auto px-4 py-4 space-y-4">
        <p className="font-mono text-[10px] text-content-faint break-all">card {cardId}</p>

        {card === null ? (
          <p className="text-xs text-content-muted">
            {connection === 'error'
              ? 'The card stream could not be opened.'
              : 'Waiting for the card snapshot…'}
          </p>
        ) : (
          <>
            <CardFrame
              card={card}
              events={events}
              lastSequence={lastSequence}
              isLive={isLive}
              invokeAction={(handler, payload) => invokeCardAction(card.id, handler, payload)}
            />

            {/* Live events */}
            <div>
              <div className="flex items-center gap-2 mb-2">
                <h3 className="text-xs font-medium text-content-secondary">Events</h3>
                {replay !== null && (
                  <span className="text-[10px] text-amber-400">
                    gap — replaying from #{replay.afterSequence}
                  </span>
                )}
                {rejected > 0 && (
                  <span className="text-[10px] text-status-danger">{rejected} rejected</span>
                )}
              </div>

              {events.length === 0 ? (
                <p className="text-xs text-content-muted">No events yet.</p>
              ) : (
                <ul className="space-y-2">
                  {events.map((event) => (
                    <li
                      key={event.eventId}
                      className="rounded-md border border-line-subtle bg-surface-input/60 p-2"
                    >
                      <div className="flex items-center gap-2 text-[11px]">
                        <span className="font-mono text-content-faint">#{event.sequence}</span>
                        <span className="text-content-secondary">{event.eventType}</span>
                        <span className="text-content-faint">
                          {event.actorKind}
                          {event.actorId ? `:${event.actorId}` : ''}
                        </span>
                        <span className="ml-auto text-content-faint">
                          {formatTimeAgo(event.createdAt)}
                        </span>
                      </div>
                      {Object.keys(event.payload).length > 0 && (
                        <pre className="mt-1 text-[10px] text-content-muted font-mono overflow-x-auto whitespace-pre-wrap break-words">
                          {JSON.stringify(event.payload, null, 2)}
                        </pre>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </div>

          </>
        )}
      </div>
    </aside>
  );
}
