import { useState } from 'react';
import { Activity, Clock, Hash } from 'lucide-react';
import { cardStatusLabel, cardTypeLabel, type Card, type CardEvent } from '../types/card.ts';
import type { CardActionPayload } from '../lib/cardActions.ts';

const STATUS_BADGE: Record<string, string> = {
  active: 'text-status-success',
  dismissed: 'text-content-muted',
  archived: 'text-content-muted',
};

export interface CardFrameProps {
  card: Card;
  events: readonly CardEvent[];
  lastSequence?: number;
  isLive?: boolean;
  invokeAction: (handler: string, payload: CardActionPayload) => Promise<unknown>;
}

type ActionState = {
  handler: string;
  status: 'pending' | 'success' | 'error';
  message?: string;
};

function formatTimeAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (!Number.isFinite(ms)) return iso;
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${Math.max(sec, 0)}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${Math.floor(min / 60)}h ago`;
  return `${Math.floor(hr / 24)}d ago`;
}

function shortenHash(hash: string): string {
  return hash.length > 16 ? `${hash.slice(0, 12)}…` : hash;
}

function actionErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'Action failed';
}

/**
 * Safe default card frame. All card data is passed as React text children;
 * arbitrary application HTML is never interpreted by this component.
 */
export default function CardFrame({
  card,
  events,
  lastSequence = events.at(-1)?.sequence ?? 0,
  isLive = false,
  invokeAction,
}: CardFrameProps) {
  const [hashExpanded, setHashExpanded] = useState(false);
  const [actionState, setActionState] = useState<ActionState | null>(null);

  const handleAction = async (handler: string) => {
    setActionState({ handler, status: 'pending' });
    try {
      await invokeAction(handler, {});
      setActionState({ handler, status: 'success', message: 'Action completed' });
    } catch (error) {
      setActionState({ handler, status: 'error', message: actionErrorMessage(error) });
    }
  };

  return (
    <div className="rounded-lg border border-line-subtle bg-surface-panel p-3 space-y-2">
      <div className="flex items-center gap-2 flex-wrap">
        <span className="text-sm font-medium text-content-primary">
          {cardTypeLabel(card.cardType)} Card
        </span>
        <span
          className={`text-[10px] uppercase tracking-wide rounded-xs px-1.5 py-0.5 bg-surface-input ring-1 ring-inset ring-line-subtle ${
            STATUS_BADGE[card.status] ?? 'text-content-secondary'
          }`}
          data-testid="card-status-badge"
        >
          {cardStatusLabel(card.status)}
        </span>
      </div>

      <dl className="text-[11px] text-content-muted space-y-1">
        <div className="flex gap-2">
          <dt className="text-content-faint">app</dt>
          <dd className="font-mono text-content-secondary break-all">{card.appId}</dd>
        </div>
        <div className="flex gap-2">
          <dt className="text-content-faint">created</dt>
          <dd className="flex items-center gap-1 text-content-secondary">
            <Clock className="w-3 h-3" aria-hidden="true" />
            {formatTimeAgo(card.createdAt)}
          </dd>
        </div>
        <div className="flex gap-2 items-center">
          <dt className="text-content-faint">context</dt>
          <dd className="min-w-0">
            <button
              type="button"
              onClick={() => setHashExpanded((prev) => !prev)}
              title={card.contextHash}
              aria-expanded={hashExpanded}
              className="flex items-center gap-1 font-mono text-content-secondary hover:text-content-primary transition-colors max-w-full"
            >
              <Hash className="w-3 h-3 flex-shrink-0" aria-hidden="true" />
              <span className="truncate">
                {card.contextHash === ''
                  ? '(none)'
                  : hashExpanded
                    ? card.contextHash
                    : shortenHash(card.contextHash)}
              </span>
            </button>
          </dd>
        </div>
      </dl>

      <div
        className="flex items-center gap-2 text-[11px] text-content-muted pt-1 border-t border-line-subtle"
        role="status"
        aria-live="polite"
      >
        <Activity
          className={`w-3.5 h-3.5 ${isLive ? 'text-status-success' : 'text-content-faint'}`}
          aria-hidden="true"
        />
        <span>{events.length} {events.length === 1 ? 'event' : 'events'} · last #{lastSequence}</span>
        <span className="text-content-faint">{isLive ? '(live)' : '(idle)'}</span>
      </div>

      {Object.keys(card.data).length > 0 && (
        <pre className="text-[10px] text-content-muted font-mono bg-surface-input/60 ring-1 ring-inset ring-line-subtle rounded-sm p-2 overflow-x-auto whitespace-pre-wrap break-words">
          {JSON.stringify(card.data, null, 2)}
        </pre>
      )}

      {card.actions.length > 0 && (
        <div className="flex items-center gap-1.5 flex-wrap pt-1" aria-label="Card actions">
          {card.actions.map((action) => {
            const current = actionState?.handler === action.handler ? actionState : null;
            return (
              <button
                key={action.handler}
                type="button"
                disabled={current?.status === 'pending'}
                aria-busy={current?.status === 'pending'}
                onClick={(event) => {
                  event.stopPropagation();
                  void handleAction(action.handler);
                }}
                className="px-2.5 py-1 rounded-md text-[11px] font-medium text-content-secondary bg-surface-input ring-1 ring-inset ring-line-subtle hover:text-content-primary disabled:cursor-wait disabled:opacity-60"
              >
                {current?.status === 'pending' ? 'Working…' : action.label}
              </button>
            );
          })}
        </div>
      )}

      {actionState?.status === 'success' && (
        <p role="status" className="text-[11px] text-status-success">{actionState.message}</p>
      )}
      {actionState?.status === 'error' && (
        <p role="alert" className="text-[11px] text-status-danger">{actionState.message}</p>
      )}
    </div>
  );
}

export function CompactCard(props: CardFrameProps) {
  return <CardFrame {...props} />;
}

export function ExpandedCard(props: CardFrameProps) {
  return <CardFrame {...props} />;
}

export function IterationCard(props: CardFrameProps) {
  return <CardFrame {...props} />;
}

export { CardFrame };
