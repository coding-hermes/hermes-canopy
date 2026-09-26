import { apiGet, apiUrl, authInit } from './api.ts';
import { subscribeSse, type SseSubscription } from './sse.ts';
import { normalizeProgress } from './iterationProgress.ts';
import type {
  CardProgress,
  IterationCardSubtypeData,
  IterationState,
  IterationSubtype,
  ProgressStatus,
  ProgressType,
  SearchBatchItem,
} from '../types/agent.ts';

export interface IterationCardRecord {
  id: string;
  data: IterationCardSubtypeData;
  status: 'active' | 'dismissed' | 'archived';
  revision: number;
  createdAt: string;
  updatedAt: string;
  lastInteractionAt: string;
}

export interface IterationEventFrame {
  cardId: string;
  subtype: IterationSubtype;
  eventType: string;
  data: unknown;
  sequence: number;
  createdAt?: string;
}

export interface IterationStreamHandlers {
  onOpen?: () => void;
  onError?: (error: unknown) => void;
  onEvent?: (event: IterationEventFrame, card: IterationCardRecord) => void;
  onSnapshot?: (card: IterationCardRecord) => void;
  onHeartbeat?: (lastSequence: number, timestamp?: string) => void;
}

export interface IterationEventSource {
  onopen: ((event: Event) => void) | null;
  onerror: ((event: Event) => void) | null;
  addEventListener(type: string, listener: EventListener): void;
  removeEventListener(type: string, listener: EventListener): void;
  close(): void;
}

export type EventSourceFactory = (url: string) => IterationEventSource;

const SUBTYPE_LABELS: Record<IterationSubtype, string> = {
  iteration_search: 'Search',
  iteration_code_exec: 'Code Exec',
  iteration_file_read: 'File Read',
  iteration_thinking: 'Thinking',
  iteration_tool_call: 'Tool Call',
};

const PROGRESS_TYPES: Record<IterationSubtype, ProgressType> = {
  iteration_search: 'search',
  iteration_code_exec: 'code_exec',
  iteration_file_read: 'file_read',
  iteration_thinking: 'thinking',
  iteration_tool_call: 'tool_call',
};

const STATES = new Set<IterationState>([
  'running',
  'waiting_for_user',
  'completed',
  'failed',
  'cancelled',
  'interrupted',
]);

const PROGRESS_STATUSES = new Set<ProgressStatus>(['running', 'completed', 'failed', 'cancelled']);

function objectOf(value: unknown): Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function stringOf(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.length > 0 ? value : fallback;
}

function numberOf(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback;
}

function dateOf(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.length > 0 ? value : fallback;
}

function subtypeOf(value: unknown): IterationSubtype {
  return typeof value === 'string' && value in SUBTYPE_LABELS ? value as IterationSubtype : 'iteration_search';
}

function stateOf(value: unknown, fallback: IterationState): IterationState {
  return typeof value === 'string' && STATES.has(value as IterationState) ? value as IterationState : fallback;
}

function progressStatusOf(value: unknown, state: IterationState): ProgressStatus {
  if (typeof value === 'string' && PROGRESS_STATUSES.has(value as ProgressStatus)) return value as ProgressStatus;
  if (state === 'completed') return 'completed';
  if (state === 'failed' || state === 'interrupted') return 'failed';
  if (state === 'cancelled') return 'cancelled';
  return 'running';
}

function wireValue(raw: Record<string, unknown>, camel: string, snake: string): unknown {
  return raw[camel] ?? raw[snake];
}

/** Convert the backend's compact base-card response into typed renderer data. */
export function normalizeIterationCard(raw: unknown): IterationCardRecord {
  const card = objectOf(raw);
  const id = stringOf(wireValue(card, 'id', 'id'), 'unknown-card');
  const baseStatus = stringOf(wireValue(card, 'status', 'status'), 'active') as IterationCardRecord['status'];
  const createdAt = dateOf(wireValue(card, 'createdAt', 'created_at'), new Date(0).toISOString());
  const updatedAt = dateOf(wireValue(card, 'updatedAt', 'updated_at'), createdAt);
  const revision = numberOf(wireValue(card, 'revision', 'revision'), numberOf(card.last_event_seq));
  const source = objectOf(card.data);
  const subtype = subtypeOf(source.subtype);
  const fallbackState: IterationState = baseStatus === 'active' ? 'running' : baseStatus === 'archived' ? 'completed' : 'cancelled';
  const state = stateOf(source.state, fallbackState);
  const progressSource = objectOf(source.progress);
  const progress = normalizeProgress({
    cardId: stringOf(progressSource.cardId, id),
    parentCardId: typeof progressSource.parentCardId === 'string' ? progressSource.parentCardId : undefined,
    type: PROGRESS_TYPES[subtype],
    title: stringOf(progressSource.title, stringOf(source.title, SUBTYPE_LABELS[subtype])),
    current: numberOf(progressSource.current),
    total: numberOf(progressSource.total),
    status: progressStatusOf(progressSource.status, state),
    phase: typeof progressSource.phase === 'string' ? progressSource.phase : undefined,
    updatedAt: dateOf(progressSource.updatedAt, updatedAt),
  });
  const common = {
    subtype,
    title: stringOf(source.title, SUBTYPE_LABELS[subtype]),
    state,
    agentId: typeof source.agentId === 'string' ? source.agentId : undefined,
    sessionId: typeof source.sessionId === 'string' ? source.sessionId : undefined,
    topicId: typeof source.topicId === 'string' ? source.topicId : undefined,
    progress,
  };

  let data: IterationCardSubtypeData;
  switch (subtype) {
    case 'iteration_search': {
      const batch = Array.isArray(source.currentBatch) ? source.currentBatch : [];
      data = {
        ...common,
        subtype,
        urlsSearched: Array.isArray(source.urlsSearched) ? source.urlsSearched.filter((value): value is string => typeof value === 'string') : [],
        currentBatch: batch as unknown as SearchBatchItem[],
        focusUrls: Array.isArray(source.focusUrls) ? source.focusUrls.filter((value): value is string => typeof value === 'string') : null,
      };
      break;
    }
    case 'iteration_code_exec':
      data = {
        ...common,
        subtype,
        command: stringOf(source.command, ''),
        workdir: typeof source.workdir === 'string' ? source.workdir : null,
        status: (typeof source.status === 'string' ? source.status : progress.status) as 'running' | 'completed' | 'cancelled' | 'failed',
        stdout: Array.isArray(source.stdout) ? source.stdout.filter((value): value is string => typeof value === 'string') : [],
        stderr: Array.isArray(source.stderr) ? source.stderr.filter((value): value is string => typeof value === 'string') : [],
        exitCode: typeof source.exitCode === 'number' ? source.exitCode : null,
        startTime: typeof source.startTime === 'string' ? source.startTime : null,
        endTime: typeof source.endTime === 'string' ? source.endTime : null,
        cancelled: source.cancelled === true || state === 'cancelled',
      };
      break;
    case 'iteration_file_read':
      data = {
        ...common,
        subtype,
        path: stringOf(source.path, 'Unknown file'),
        absolutePath: stringOf(source.absolutePath, stringOf(source.path, '')),
        size: numberOf(source.size),
        mimeType: stringOf(source.mimeType, 'text/plain'),
        language: stringOf(source.language, 'text'),
        lineCount: numberOf(source.lineCount),
        highlights: Array.isArray(source.highlights) ? source.highlights as never : [],
        visibleLines: objectOf(source.visibleLines) as never,
      };
      if (!data.visibleLines || typeof data.visibleLines.start !== 'number') data.visibleLines = { start: 1, end: Math.max(1, numberOf(source.lineCount)) };
      break;
    case 'iteration_thinking':
      data = {
        ...common,
        subtype,
        steps: Array.isArray(source.steps) ? source.steps as never : [],
        currentStepId: typeof source.currentStepId === 'string' ? source.currentStepId : null,
      };
      break;
    case 'iteration_tool_call':
      data = {
        ...common,
        subtype,
        toolName: stringOf(source.toolName, 'Tool call'),
        params: objectOf(source.params),
        result: source.result ?? null,
        status: (typeof source.status === 'string' ? source.status : 'running') as 'pending_approval' | 'running' | 'completed' | 'denied' | 'failed',
        startTime: typeof source.startTime === 'string' ? source.startTime : null,
        endTime: typeof source.endTime === 'string' ? source.endTime : null,
        durationMs: typeof source.durationMs === 'number' ? source.durationMs : null,
        error: typeof source.error === 'string' ? source.error : null,
        gated: source.gated === true,
      };
      break;
  }

  return { id, data, status: baseStatus, revision, createdAt, updatedAt, lastInteractionAt: updatedAt };
}

function recordWithData(card: IterationCardRecord, rawData: Record<string, unknown>, eventTime?: string): IterationCardRecord {
  const nextRaw = { ...rawData, subtype: rawData.subtype ?? card.data.subtype, title: rawData.title ?? card.data.title, state: rawData.state ?? card.data.state, progress: rawData.progress ?? card.data.progress };
  return normalizeIterationCard({
    id: card.id,
    status: card.status,
    revision: card.revision,
    createdAt: card.createdAt,
    updatedAt: eventTime ?? card.updatedAt,
    data: nextRaw,
  });
}

/** Apply an accepted committed event to the materialized card view locally. */
export function applyIterationEvent(card: IterationCardRecord, event: IterationEventFrame): IterationCardRecord {
  const patch = objectOf(event.data);
  const current = card.data as unknown as Record<string, unknown>;
  const currentProgress = objectOf(current.progress);
  const progressPatch = objectOf(patch.progress);
  const progress = Object.keys(progressPatch).length > 0
    ? { ...currentProgress, ...progressPatch }
    : (typeof patch.current === 'number' || typeof patch.total === 'number' || typeof patch.status === 'string'
      ? { ...currentProgress, current: patch.current, total: patch.total, status: patch.status }
      : currentProgress);
  const next: Record<string, unknown> = { ...current, ...patch, progress };
  if (event.eventType === 'agent_error' || patch.state === 'interrupted') next.state = 'interrupted';
  return recordWithData(card, next, event.createdAt ?? card.updatedAt);
}

/** Open exactly one authenticated stream for one visible card. */
export function openIterationEventStream(
  card: IterationCardRecord,
  handlers: IterationStreamHandlers,
  factory?: EventSourceFactory,
): { close: () => void; getLastSequence: () => number } {
  const url = apiUrl(`/cards/iteration/${encodeURIComponent(card.id)}/events`);
  let currentCard = card;
  let lastSequence = 0;
  let closed = false;
  let source: IterationEventSource | null = null;
  let subscription: SseSubscription | null = null;
  const listeners: Array<[string, EventListener]> = [];

  const processFrame = (type: string, rawData: string, lastEventId = '') => {
    if (closed) return;
    try {
      const body = objectOf(JSON.parse(rawData));
      if (type === 'heartbeat') {
        handlers.onHeartbeat?.(lastSequence, typeof body.timestamp === 'string' ? body.timestamp : undefined);
        return;
      }
      if (type === 'card_snapshot') {
        const snapshot = objectOf(body.data);
        const next = Object.keys(snapshot).length > 0
          ? normalizeIterationCard(snapshot.id || snapshot.data ? snapshot : { ...currentCard, data: snapshot })
          : currentCard;
        currentCard = next;
        handlers.onSnapshot?.(next);
        return;
      }
      if (type !== 'iteration_event') return;
      const sequence = numberOf(body.sequence, numberOf(lastEventId ? Number(lastEventId) : undefined));
      if (sequence <= lastSequence) return;
      lastSequence = sequence;
      const frame: IterationEventFrame = {
        cardId: stringOf(body.cardId, card.id),
        subtype: subtypeOf(body.subtype ?? card.data.subtype),
        eventType: stringOf(body.eventType, 'iteration_event'),
        data: body.data,
        sequence,
        createdAt: typeof body.createdAt === 'string' ? body.createdAt : undefined,
      };
      currentCard = applyIterationEvent(currentCard, frame);
      handlers.onEvent?.(frame, currentCard);
    } catch (error) {
      handlers.onError?.(error);
    }
  };

  const close = () => {
    if (closed) return;
    closed = true;
    for (const [type, listener] of listeners) source?.removeEventListener(type, listener);
    source?.close();
    subscription?.close();
  };

  if (factory) {
    source = factory(url);
    const add = (type: string) => {
      const listener = ((event: MessageEvent) => processFrame(type, String(event.data ?? ''), event.lastEventId)) as EventListener;
      source?.addEventListener(type, listener);
      listeners.push([type, listener]);
    };
    source.onopen = () => { if (!closed) handlers.onOpen?.(); };
    source.onerror = (event) => { if (!closed) handlers.onError?.(event); };
    add('heartbeat');
    add('card_snapshot');
    add('iteration_event');
  } else {
    subscription = subscribeSse(url, {
      eventTypes: ['iteration_event', 'card_snapshot', 'heartbeat'],
      onOpen: handlers.onOpen,
      onError: handlers.onError,
      onEvent: processFrame,
    });
  }

  return { close, getLastSequence: () => lastSequence };
}

export function fetchActiveIterationCards(): Promise<IterationCardRecord[]> {
  return apiGet<{ cards: unknown[] }>('/cards/iteration/active').then((body) => (body.cards ?? []).map(normalizeIterationCard));
}

export function fetchIterationProgress(): Promise<CardProgress[]> {
  return apiGet<{ progress: CardProgress[] }>('/iteration/progress').then((body) => body.progress ?? []);
}

async function mutation(path: string, init: RequestInit): Promise<void> {
  const response = await fetch(apiUrl(path), authInit(init));
  if (response.ok) return;
  const text = await response.text();
  throw new Error(text || `HTTP ${response.status}`);
}

export function cancelIterationCard(cardId: string): Promise<void> {
  return mutation(`/cards/iteration/${encodeURIComponent(cardId)}/cancel`, { method: 'POST' });
}

/** Dismiss through the base-card lifecycle so the server emits card_dismissed. */
export function dismissIterationCard(card: Pick<IterationCardRecord, 'id' | 'revision'>): Promise<void> {
  return mutation(`/cards/${encodeURIComponent(card.id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', 'If-Match': String(card.revision) },
    body: JSON.stringify({ status: 'dismissed' }),
  });
}
