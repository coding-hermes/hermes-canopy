import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Activity, AlertTriangle, CheckCircle2, ChevronDown, ChevronRight, FileText, Loader2, Search, Terminal, Wrench, X } from 'lucide-react';
import {
  cancelIterationCard,
  dismissIterationCard,
  fetchActiveIterationCards,
  fetchIterationProgress,
  normalizeIterationCard,
  openIterationEventStream,
  type EventSourceFactory,
  type IterationCardRecord,
  type IterationEventFrame,
} from '../../lib/iterationApi.ts';
import { aggregateHeaderProgress, formatHeaderStatus } from '../../lib/iterationProgress.ts';
import type { IterationCardSubtypeData, IterationState } from '../../types/agent.ts';

interface IterationSidePanelProps {
  open: boolean;
  onClose: () => void;
  eventSourceFactory?: EventSourceFactory;
}

const ICONS = {
  iteration_search: Search,
  iteration_code_exec: Terminal,
  iteration_file_read: FileText,
  iteration_thinking: Activity,
  iteration_tool_call: Wrench,
} as const;

const LABELS = {
  iteration_search: 'Search',
  iteration_code_exec: 'Code Exec',
  iteration_file_read: 'File Read',
  iteration_thinking: 'Thinking',
  iteration_tool_call: 'Tool Call',
} as const;

function statusLabel(state: IterationState): string {
  return state === 'waiting_for_user' ? 'Waiting' : state === 'completed' ? 'Done' : state[0].toUpperCase() + state.slice(1);
}

function statusClass(state: IterationState): string {
  switch (state) {
    case 'running': return 'bg-purple-500/20 text-purple-300';
    case 'waiting_for_user': return 'bg-amber-500/20 text-amber-300';
    case 'completed': return 'bg-green-500/20 text-green-300';
    case 'failed': return 'bg-red-500/20 text-red-300';
    case 'interrupted': return 'bg-orange-500/20 text-orange-300';
    default: return 'bg-gray-500/20 text-gray-300';
  }
}

function progressText(data: IterationCardSubtypeData): string {
  switch (data.subtype) {
    case 'iteration_search':
      return `Results: ${data.currentBatch.filter((result) => result.status === 'retrieved').length}`;
    case 'iteration_code_exec':
      return data.state === 'running' ? 'Running · cancellable' : data.exitCode === null ? statusLabel(data.state) : `Exit code ${data.exitCode}`;
    case 'iteration_file_read':
      return `${data.path} · lines ${data.visibleLines.start}-${data.visibleLines.end}`;
    case 'iteration_thinking': {
      const active = data.steps.find((step) => step.id === data.currentStepId) ?? data.steps.find((step) => step.status === 'active');
      return active ? `Active step: ${active.title}` : `${data.progress.current}/${data.progress.total}`;
    }
    case 'iteration_tool_call':
      return data.status === 'pending_approval' ? `Approve ${data.toolName}` : data.status.replace(/_/g, ' ');
  }
}

function conciseProgress(data: IterationCardSubtypeData): string {
  if (data.progress.total > 0) return `${data.progress.current}/${data.progress.total} · ${progressText(data)}`;
  return progressText(data);
}

function interactionTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString();
}

function mergeProgress(card: IterationCardRecord, progress: IterationCardRecord['data']['progress']): IterationCardRecord {
  return normalizeIterationCard({
    id: card.id,
    status: card.status,
    revision: card.revision,
    createdAt: card.createdAt,
    updatedAt: progress.updatedAt,
    data: { ...card.data, progress },
  });
}

function announcement(card: IterationCardRecord): string {
  return `${card.data.title}: ${statusLabel(card.data.state)}; ${conciseProgress(card.data)}`;
}

function CompactCard({
  card,
  expanded,
  events,
  cancelling,
  dismissing,
  liveMessage,
  lastHeartbeatAt,
  onToggle,
  onCancel,
  onDismiss,
}: {
  card: IterationCardRecord;
  expanded: boolean;
  events: IterationEventFrame[];
  cancelling: boolean;
  dismissing: boolean;
  liveMessage: string;
  lastHeartbeatAt?: string;
  onToggle: () => void;
  onCancel: () => void;
  onDismiss: () => void;
}) {
  const Icon = ICONS[card.data.subtype];
  const label = LABELS[card.data.subtype];
  const interrupted = card.data.state === 'interrupted';
  return (
    <li role="listitem" className="rounded-lg border border-line-subtle bg-surface-panel p-3 shadow-sm" data-card-id={card.id} data-last-heartbeat={lastHeartbeatAt ?? ''}>
      <div className="flex items-start gap-2">
        <span className="mt-0.5 rounded bg-surface-input p-1.5 text-content-secondary" aria-hidden="true">
          <Icon className="h-4 w-4" />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="truncate text-sm font-medium text-content-primary">{card.data.title}</h3>
          <p className="text-xs text-content-muted">{label}</p>
        </div>
        <span className={`inline-flex shrink-0 items-center gap-1 rounded-full px-1.5 py-0.5 text-xs ${statusClass(card.data.state)}`} role="status">
          {card.data.state === 'running' && <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />}
          {card.data.state === 'completed' && <CheckCircle2 className="h-3 w-3" aria-hidden="true" />}
          {card.data.state === 'interrupted' && <AlertTriangle className="h-3 w-3" aria-hidden="true" />}
          {statusLabel(card.data.state)}
        </span>
      </div>

      <p className="mt-2 text-xs text-content-secondary" data-testid={`progress-${card.id}`}>{conciseProgress(card.data)}</p>
      {interrupted && <p className="mt-2 rounded bg-orange-500/10 px-2 py-1 text-xs text-orange-200">Recovery required after an interrupted agent process. Prior activity is preserved.</p>}

      <div className="mt-2 flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={onToggle}
          className="inline-flex items-center gap-1 rounded px-2 py-1 text-xs text-content-secondary hover:bg-surface-hover hover:text-content-primary"
          aria-expanded={expanded}
          aria-controls={`iteration-details-${card.id}`}
          aria-label={`${expanded ? 'Collapse' : 'Expand'} ${card.data.title}`}
        >
          {expanded ? <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" /> : <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />}
          {expanded ? 'Collapse' : 'Expand'}
        </button>
        {card.data.subtype === 'iteration_code_exec' && card.data.state === 'running' && !interrupted && (
          <button
            type="button"
            onClick={onCancel}
            disabled={cancelling}
            className="rounded bg-red-500/15 px-2 py-1 text-xs text-red-200 hover:bg-red-500/25 disabled:opacity-50"
            aria-label={`Cancel ${card.data.title}`}
          >
            {cancelling ? 'Cancelling…' : 'Cancel'}
          </button>
        )}
        <button
          type="button"
          onClick={onDismiss}
          disabled={dismissing}
          className="ml-auto rounded px-2 py-1 text-xs text-content-muted hover:bg-surface-hover hover:text-content-primary disabled:opacity-50"
          aria-label={`Dismiss ${card.data.title}`}
        >
          {dismissing ? 'Dismissing…' : 'Dismiss'}
        </button>
      </div>

      <time className="mt-2 block text-[11px] text-content-faint" dateTime={card.lastInteractionAt}>
        Last interaction: {interactionTime(card.lastInteractionAt)}
      </time>
      <div className="sr-only" role="status" aria-live="polite" aria-atomic="true" data-testid={`live-${card.id}`}>{liveMessage}</div>

      {expanded && (
        <div id={`iteration-details-${card.id}`} className="mt-3 border-t border-line-subtle pt-3 text-xs text-content-secondary">
          <p className="font-medium text-content-primary">Activity</p>
          <ol className="mt-1 space-y-1">
            {events.length === 0 && <li>No committed activity received yet.</li>}
            {events.map((event) => <li key={`${event.sequence}-${event.eventType}`}>{event.eventType.replace(/_/g, ' ')} · sequence {event.sequence}</li>)}
          </ol>
          <pre className="mt-2 max-h-32 overflow-auto whitespace-pre-wrap rounded bg-surface-input p-2">{JSON.stringify(card.data, null, 2)}</pre>
        </div>
      )}
    </li>
  );
}

export default function IterationSidePanel({ open, onClose, eventSourceFactory }: IterationSidePanelProps) {
  const [cards, setCards] = useState<IterationCardRecord[]>([]);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [events, setEvents] = useState<Record<string, IterationEventFrame[]>>({});
  const [liveMessages, setLiveMessages] = useState<Record<string, string>>({});
  const [lastHeartbeats, setLastHeartbeats] = useState<Record<string, string>>({});
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  const [dismissing, setDismissing] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const streams = useRef(new Map<string, { close: () => void; isClosed: () => boolean }>());
  const cardsRef = useRef(cards);
  const announcementAt = useRef(new Map<string, number>());
  cardsRef.current = cards;

  const updateCard = useCallback((next: IterationCardRecord) => {
    setCards((current) => current.map((card) => card.id === next.id ? next : card).sort((a, b) => b.lastInteractionAt.localeCompare(a.lastInteractionAt)));
  }, []);

  const handleCommittedEvent = useCallback((frame: IterationEventFrame, next: IterationCardRecord) => {
    if (frame.eventType === 'card_dismissed') {
      streams.current.get(next.id)?.close();
      streams.current.delete(next.id);
      setCards((current) => current.filter((card) => card.id !== next.id));
      return;
    }
    updateCard(next);
    setEvents((current) => ({ ...current, [next.id]: [...(current[next.id] ?? []), frame] }));
    const now = Date.now();
    const previous = announcementAt.current.get(next.id) ?? Number.NEGATIVE_INFINITY;
    if (frame.eventType !== 'exec_output' || now - previous >= 2000) {
      announcementAt.current.set(next.id, now);
      setLiveMessages((current) => ({ ...current, [next.id]: announcement(next) }));
    }
  }, [updateCard]);

  const closeStreams = useCallback(() => {
    for (const stream of streams.current.values()) stream.close();
    streams.current.clear();
  }, []);

  useEffect(() => {
    if (!open) {
      closeStreams();
      return;
    }
    let disposed = false;
    void Promise.all([
      fetchActiveIterationCards(),
      fetchIterationProgress().catch(() => []),
    ]).then(([active, progress]) => {
      if (disposed) return;
      const byId = new Map((progress as IterationCardRecord['data']['progress'][]).map((item) => [item.cardId, item]));
      const loaded = active.map((card) => byId.has(card.id) ? mergeProgress(card, byId.get(card.id)!) : card);
      cardsRef.current = loaded;
      setCards(loaded.sort((a, b) => b.lastInteractionAt.localeCompare(a.lastInteractionAt)));
      for (const card of loaded) {
        if (streams.current.has(card.id)) continue;
        try {
          const stream = openIterationEventStream(card, {
            onEvent: handleCommittedEvent,
            onSnapshot: (next) => {
              updateCard(next);
              setLiveMessages((current) => ({ ...current, [next.id]: announcement(next) }));
            },
            onHeartbeat: (_lastSequence, timestamp) => {
              if (timestamp) setLastHeartbeats((current) => ({ ...current, [card.id]: timestamp }));
            },
            onError: (streamError) => setError(streamError instanceof Error ? streamError.message : 'Iteration stream failed'),
          }, eventSourceFactory);
          streams.current.set(card.id, stream);
        } catch (streamError) {
          setError(streamError instanceof Error ? streamError.message : 'Iteration stream unavailable');
        }
      }
    }).catch((loadError: unknown) => {
      if (!disposed) setError(loadError instanceof Error ? loadError.message : 'Unable to load iteration cards');
    });
    return () => {
      disposed = true;
      closeStreams();
    };
  }, [closeStreams, eventSourceFactory, handleCommittedEvent, open, updateCard]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => { if (event.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [onClose, open]);

  const aggregation = useMemo(() => aggregateHeaderProgress(cards.map((card) => ({ ...card.data.progress, cardStatus: card.status, pendingApproval: card.data.subtype === 'iteration_tool_call' && card.data.status === 'pending_approval', waitingForUser: card.data.state === 'waiting_for_user' }))), [cards]);
  const headerStatus = formatHeaderStatus(aggregation) || 'No active progress';

  const onCancel = (card: IterationCardRecord) => {
    setCancelling((current) => new Set(current).add(card.id));
    void cancelIterationCard(card.id).catch((cancelError: unknown) => setError(cancelError instanceof Error ? cancelError.message : 'Unable to cancel card')).finally(() => setCancelling((current) => { const next = new Set(current); next.delete(card.id); return next; }));
  };

  const onDismiss = (card: IterationCardRecord) => {
    setDismissing((current) => new Set(current).add(card.id));
    const stream = streams.current.get(card.id);
    void dismissIterationCard(card).then(() => {
      // PATCH commits the lifecycle event; the active list changes when that
      // durable event reaches this card stream. If no live stream exists,
      // removing on PATCH success is the safe fallback for a closed stream.
      if (!stream || stream.isClosed()) {
        streams.current.get(card.id)?.close();
        streams.current.delete(card.id);
        setCards((current) => current.filter((value) => value.id !== card.id));
      }
    }).catch((dismissError: unknown) => setError(dismissError instanceof Error ? dismissError.message : 'Unable to dismiss card')).finally(() => setDismissing((current) => { const next = new Set(current); next.delete(card.id); return next; }));
  };

  if (!open) return null;
  return (
    <aside className="fixed inset-y-0 right-0 z-50 flex w-full max-w-md flex-col border-l border-line-subtle bg-surface-base shadow-2xl" aria-label="Iteration side panel" data-testid="iteration-side-panel">
      <header className="flex items-start gap-3 border-b border-line-subtle bg-surface-panel px-4 py-4">
        <div className="min-w-0 flex-1">
          <h2 className="text-base font-semibold text-content-primary">Agent activity</h2>
          <p className="mt-1 text-xs text-content-secondary" aria-live="polite" data-testid="iteration-header-status">{headerStatus}</p>
          {aggregation.overflow > 0 && <p className="sr-only">{aggregation.overflow} additional progress segments are available in the card list.</p>}
        </div>
        <button type="button" onClick={onClose} className="rounded p-1.5 text-content-muted hover:bg-surface-hover hover:text-content-primary" aria-label="Close agent activity panel">
          <X className="h-4 w-4" aria-hidden="true" />
        </button>
      </header>
      {error && <div role="alert" className="border-b border-red-500/30 bg-red-500/10 px-4 py-2 text-xs text-red-200">{error}</div>}
      <div className="flex-1 overflow-y-auto p-4">
        <ul role="list" aria-label="Active iteration cards" className="space-y-3">
          {cards.length === 0 && <li className="rounded border border-dashed border-line-subtle p-4 text-sm text-content-muted">No active agent cards.</li>}
          {cards.map((card) => (
            <CompactCard
              key={card.id}
              card={card}
              expanded={expanded.has(card.id)}
              events={events[card.id] ?? []}
              cancelling={cancelling.has(card.id)}
              dismissing={dismissing.has(card.id)}
              liveMessage={liveMessages[card.id] ?? ''}
              lastHeartbeatAt={lastHeartbeats[card.id]}
              onToggle={() => setExpanded((current) => { const next = new Set(current); if (next.has(card.id)) next.delete(card.id); else next.add(card.id); return next; })}
              onCancel={() => onCancel(card)}
              onDismiss={() => onDismiss(card)}
            />
          ))}
        </ul>
      </div>
    </aside>
  );
}
