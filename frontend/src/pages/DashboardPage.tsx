/**
 * Hermes Canopy — Dashboard (GAP-050)
 *
 * The live Hermes surface: real gateway state instead of the old blank
 * stub. Shows gateway connectivity, the run registry (real /v1/runs
 * created through the gateway), a chat composer that starts real agent
 * runs, the live SSE event stream of the selected run, and approval
 * resolution — the hermes-webui gateway-client pattern surfaced in Canopy.
 */

import { useState, useRef, useEffect, useCallback } from 'react';
import {
  Activity,
  Send,
  Square,
  ShieldCheck,
  Bot,
  Wifi,
  WifiOff,
  RefreshCw,
  Loader2,
  CheckCircle2,
  XCircle,
  TerminalSquare,
  MessageSquareText,
} from 'lucide-react';
import {
  useGatewayRuns,
  useRunEventStream,
  type GatewayRun,
  type GatewayRunEvent,
} from '../hooks/useGatewayRuns';

// ─── helpers ──────────────────────────────────────────────────────────

function shortRunId(runId: string): string {
  return runId.length > 18 ? `${runId.slice(0, 18)}…` : runId;
}

function timeAgo(iso: string): string {
  try {
    const ms = Date.now() - new Date(iso).getTime();
    const sec = Math.floor(ms / 1000);
    if (sec < 60) return `${sec}s ago`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ago`;
    return `${Math.floor(min / 60)}h ago`;
  } catch {
    return iso;
  }
}

const STATUS_STYLES: Record<string, string> = {
  queued: 'bg-amber-500/15 text-amber-300 ring-amber-500/30',
  running: 'bg-sky-500/15 text-sky-300 ring-sky-500/30',
  started: 'bg-sky-500/15 text-sky-300 ring-sky-500/30',
  stopping: 'bg-amber-500/15 text-amber-300 ring-amber-500/30',
  waiting_for_approval: 'bg-violet-500/15 text-violet-300 ring-violet-500/30',
  completed: 'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30',
  failed: 'bg-rose-500/15 text-rose-300 ring-rose-500/30',
  cancelled: 'bg-surface-hover text-content-muted ring-line-subtle',
  disconnected: 'bg-surface-hover text-content-muted ring-line-subtle',
  not_found: 'bg-surface-hover text-content-muted ring-line-subtle',
};

function StatusBadge({ status }: { status: string }) {
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-medium ring-1 ring-inset ${STATUS_STYLES[status] ?? 'bg-surface-hover text-content-muted ring-line-subtle'}`}
      data-testid={`run-status-${status}`}
    >
      {status}
    </span>
  );
}

// ─── approval card ────────────────────────────────────────────────────

function ApprovalCard({ run, onRespond }: { run: GatewayRun; onRespond: (choice: 'once' | 'session' | 'always' | 'deny', approvalId?: string) => void }) {
  const pending = [...run.events].reverse().find((ev) => ev.event === 'approval.request');
  if (!pending) return null;
  return (
    <div
      className="rounded-lg border border-violet-500/30 bg-violet-500/10 p-3"
      data-testid="approval-card"
    >
      <div className="flex items-center gap-2 text-xs font-semibold text-violet-300">
        <ShieldCheck className="h-3.5 w-3.5" />
        Approval requested — Hermes wants to run:
      </div>
      <code className="mt-2 block overflow-x-auto rounded bg-surface-input px-2 py-1.5 text-[11px] text-content-primary">
        {pending.command || '(no command preview)'}
      </code>
      <div className="mt-2 flex flex-wrap gap-1.5">
        {(['once', 'session', 'always', 'deny'] as const).map((choice) => (
          <button
            key={choice}
            type="button"
            onClick={() => onRespond(choice, pending.approval_id)}
            className="rounded-md border border-line-subtle bg-surface-panel px-2.5 py-1 text-[11px] font-medium text-content-secondary transition-colors hover:border-accent/40 hover:text-content-primary"
            data-testid={`approval-${choice}`}
          >
            {choice}
          </button>
        ))}
      </div>
    </div>
  );
}

// ─── event feed ───────────────────────────────────────────────────────

function EventIcon({ ev }: { ev: GatewayRunEvent }) {
  switch (ev.event) {
    case 'message.delta':
      return <MessageSquareText className="h-3 w-3 shrink-0 text-sky-300" />;
    case 'tool.started':
    case 'tool.completed':
      return <TerminalSquare className="h-3 w-3 shrink-0 text-amber-300" />;
    case 'run.completed':
      return <CheckCircle2 className="h-3 w-3 shrink-0 text-emerald-300" />;
    case 'run.failed':
      return <XCircle className="h-3 w-3 shrink-0 text-rose-300" />;
    default:
      return <Activity className="h-3 w-3 shrink-0 text-content-muted" />;
  }
}

function EventFeed({ events, transcript }: { events: GatewayRunEvent[]; transcript: string }) {
  const endRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    // jsdom lacks scrollIntoView; the optional call keeps tests green.
    endRef.current?.scrollIntoView?.({ block: 'end' });
  }, [events.length, transcript.length]);

  if (events.length === 0) {
    return (
      <p className="px-3 py-4 text-center text-xs text-content-faint" data-testid="feed-empty">
        Waiting for live events… (SSE stream connected)
      </p>
    );
  }

  return (
    <div className="space-y-1 px-3 py-2" data-testid="event-feed">
      {events.map((ev, i) => {
        if (ev.event === 'message.delta') {
          // Render consecutive deltas as one block: only show the first
          // delta row; the transcript below carries the accumulated text.
          if (i > 0 && events[i - 1].event === 'message.delta') return null;
          return (
            <div key={i} className="flex items-start gap-2 text-[11px] text-content-muted">
              <EventIcon ev={ev} />
              <span>streaming response…</span>
            </div>
          );
        }
        const label =
          ev.event === 'tool.started' || ev.event === 'tool.completed'
            ? `${ev.event} — ${ev.tool ?? ''}${ev.preview ? ` (${ev.preview})` : ''}`
            : ev.event;
        return (
          <div key={i} className="flex items-start gap-2 text-[11px] text-content-muted" data-testid={`event-${ev.event}`}>
            <EventIcon ev={ev} />
            <span className="min-w-0 break-words">
              {label}
              {ev.error ? ` — ${ev.error}` : ''}
              {ev.output ? ` — ${ev.output}` : ''}
            </span>
          </div>
        );
      })}
      {transcript && (
        <div className="mt-2 whitespace-pre-wrap rounded-md bg-surface-input/60 px-2 py-1.5 text-xs text-content-primary" data-testid="transcript">
          {transcript}
        </div>
      )}
      <div ref={endRef} />
    </div>
  );
}

// ─── page ─────────────────────────────────────────────────────────────

export default function DashboardPage() {
  const { status, runs, loading, error, refresh, startRun, stopRun, respondApproval } = useGatewayRuns();
  const [composer, setComposer] = useState('');
  const [sending, setSending] = useState(false);
  const [composerError, setComposerError] = useState<string | null>(null);
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null);
  const feedRef = useRef<HTMLDivElement>(null);

  const selectedRun = runs.find((r) => r.run_id === selectedRunId) ?? null;
  const { events, status: feedStatus, transcript } = useRunEventStream(
    selectedRun?.status && !['completed', 'failed', 'cancelled', 'not_found'].includes(selectedRun.status)
      ? selectedRun.run_id
      : selectedRunId,
  );

  // Auto-select the newest run when the list refreshes and nothing is selected.
  useEffect(() => {
    if (!selectedRunId && runs.length > 0) {
      setSelectedRunId(runs[0].run_id);
    }
  }, [runs, selectedRunId]);

  const send = useCallback(async () => {
    const message = composer.trim();
    if (!message || sending) return;
    setSending(true);
    setComposerError(null);
    try {
      const runId = await startRun(message);
      setComposer('');
      setSelectedRunId(runId);
      // Reconnect the feed to the fresh run.
      requestAnimationFrame(() => feedRef.current?.scrollIntoView?.({ block: 'end' }));
    } catch (err) {
      setComposerError(err instanceof Error ? err.message : String(err));
    } finally {
      setSending(false);
    }
  }, [composer, sending, startRun]);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 flex-wrap items-center gap-3 border-b border-line-subtle px-5 py-3">
        <div>
          <h1 className="text-xl font-bold tracking-tight text-content-primary">Dashboard</h1>
          <p className="text-xs text-content-muted">
            Live Hermes gateway — real runs, real events, no seeded data.
          </p>
        </div>
        <div className="ml-auto flex items-center gap-2">
          {status?.connected ? (
            <span
              className="inline-flex items-center gap-1.5 rounded-full bg-emerald-500/15 px-2.5 py-1 text-[11px] font-medium text-emerald-300 ring-1 ring-inset ring-emerald-500/30"
              data-testid="gateway-live"
            >
              <Wifi className="h-3 w-3" />
              Gateway live · {status.base_url}
            </span>
          ) : (
            <span
              className="inline-flex items-center gap-1.5 rounded-full bg-rose-500/15 px-2.5 py-1 text-[11px] font-medium text-rose-300 ring-1 ring-inset ring-rose-500/30"
              data-testid="gateway-offline"
            >
              <WifiOff className="h-3 w-3" />
              Gateway offline{status?.error ? ` · ${status.error}` : ''}
            </span>
          )}
          <button
            type="button"
            onClick={() => void refresh()}
            className="grid h-7 w-7 place-items-center rounded-md text-content-muted transition-colors hover:bg-surface-hover hover:text-content-primary"
            aria-label="Refresh gateway state"
            data-testid="refresh-gateway"
          >
            <RefreshCw className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>

      {error && (
        <div className="border-b border-rose-500/30 bg-rose-500/10 px-5 py-2 text-[11px] text-status-danger" data-testid="gateway-error">
          {error}
        </div>
      )}

      <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 overflow-y-auto p-5 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        {/* Left column: composer + live feed */}
        <div className="flex min-h-0 flex-col gap-4">
          <section className="rounded-lg border border-line-subtle bg-surface-panel" data-testid="composer-card">
            <div className="flex items-center gap-2 border-b border-line-subtle px-4 py-2.5">
              <Bot className="h-4 w-4 text-accent-2-300" />
              <span className="text-sm font-semibold tracking-tight text-content-primary">
                Chat with Hermes
              </span>
              <span className="ml-auto text-[10px] text-content-faint">
                starts a real run on the gateway (POST /v1/runs)
              </span>
            </div>
            <div className="p-4">
              <textarea
                value={composer}
                onChange={(e) => setComposer(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault();
                    void send();
                  }
                }}
                placeholder="Send a message to the live Hermes agent… (Enter to send)"
                rows={3}
                className="w-full resize-none rounded-md bg-surface-input px-3 py-2 text-sm text-content-primary placeholder:text-content-faint ring-1 ring-inset ring-line-subtle transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                data-testid="composer-input"
              />
              {composerError && (
                <p className="mt-2 text-[11px] text-status-danger" data-testid="composer-error">
                  {composerError}
                </p>
              )}
              <div className="mt-2 flex items-center justify-end gap-2">
                <span className="mr-auto text-[10px] text-content-faint">
                  Tiny prompts only — real tokens, stop runs when done.
                </span>
                <button
                  type="button"
                  onClick={() => void send()}
                  disabled={sending || !composer.trim()}
                  className="inline-flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-xs font-semibold text-white transition-colors hover:bg-accent/90 disabled:opacity-50"
                  data-testid="composer-send"
                >
                  {sending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Send className="h-3.5 w-3.5" />}
                  Send
                </button>
              </div>
            </div>
          </section>

          <section className="flex min-h-0 flex-1 flex-col rounded-lg border border-line-subtle bg-surface-panel">
            <div className="flex shrink-0 items-center gap-2 border-b border-line-subtle px-4 py-2.5">
              <Activity className="h-4 w-4 text-accent-2-300" />
              <span className="text-sm font-semibold tracking-tight text-content-primary">
                Live event stream
              </span>
              {selectedRun && (
                <span className="ml-auto inline-flex items-center gap-1.5 text-[10px] text-content-faint">
                  <span
                    className={`h-1.5 w-1.5 rounded-full ${
                      feedStatus === 'open' ? 'bg-emerald-400' : feedStatus === 'closed' ? 'bg-content-faint' : feedStatus === 'connecting' ? 'bg-amber-400' : 'bg-rose-400'
                    }`}
                  />
                  {feedStatus} · {shortRunId(selectedRun.run_id)}
                </span>
              )}
            </div>
            <div ref={feedRef} className="min-h-0 flex-1 overflow-y-auto">
              {selectedRun ? (
                <>
                  <ApprovalCard run={selectedRun} onRespond={(choice, approvalId) => void respondApproval(selectedRun.run_id, choice, approvalId)} />
                  <EventFeed events={events} transcript={transcript} />
                </>
              ) : (
                <p className="px-3 py-6 text-center text-xs text-content-faint" data-testid="no-run-selected">
                  No run selected — send a message to start one.
                </p>
              )}
            </div>
          </section>
        </div>

        {/* Right column: run registry */}
        <section className="flex min-h-0 flex-col rounded-lg border border-line-subtle bg-surface-panel">
          <div className="flex shrink-0 items-center gap-2 border-b border-line-subtle px-4 py-2.5">
            <Activity className="h-4 w-4 text-accent-2-300" />
            <span className="text-sm font-semibold tracking-tight text-content-primary">Hermes runs</span>
            {status && (
              <span className="ml-auto text-[10px] text-content-faint" data-testid="run-counts">
                {status.active_runs} active · {status.run_count} total
              </span>
            )}
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto">
            {loading && runs.length === 0 ? (
              <p className="px-3 py-6 text-center text-xs text-content-faint">Loading gateway state…</p>
            ) : runs.length === 0 ? (
              <p className="px-3 py-6 text-center text-xs text-content-faint" data-testid="runs-empty">
                No runs yet. Send a message to create the first real Hermes run.
              </p>
            ) : (
              <ul className="divide-y divide-line-subtle" data-testid="runs-list">
                {runs.map((run) => (
                  <RunRow
                    key={run.run_id}
                    run={run}
                    selected={run.run_id === selectedRunId}
                    onSelect={() => setSelectedRunId(run.run_id)}
                    onStop={() => void stopRun(run.run_id)}
                  />
                ))}
              </ul>
            )}
          </div>
        </section>
      </div>
    </div>
  );
}

function RunRow({
  run,
  selected,
  onSelect,
  onStop,
}: {
  run: GatewayRun;
  selected: boolean;
  onSelect: () => void;
  onStop: () => void;
}) {
  const isTerminal = ['completed', 'failed', 'cancelled', 'not_found'].includes(run.status);
  const pendingApproval = run.events.some((ev) => ev.event === 'approval.request');
  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        className={`flex w-full items-start gap-2 px-4 py-2.5 text-left transition-colors ${
          selected ? 'bg-accent-2/10' : 'hover:bg-surface-hover/60'
        }`}
        data-testid={`run-row-${run.run_id}`}
      >
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate font-mono text-[11px] text-content-primary">{shortRunId(run.run_id)}</span>
            <StatusBadge status={run.status} />
            {pendingApproval && !isTerminal && (
              <ShieldCheck className="h-3 w-3 shrink-0 text-violet-300" aria-label="approval pending" />
            )}
          </div>
          <p className="mt-0.5 truncate text-xs text-content-secondary">{run.message}</p>
          <p className="mt-0.5 text-[10px] text-content-faint">
            {timeAgo(run.created_at)} · last: {run.last_event ?? '—'}
            {run.error ? ` · ${run.error}` : ''}
          </p>
        </div>
        {!isTerminal && (
          <span
            role="button"
            tabIndex={0}
            onClick={(e) => {
              e.stopPropagation();
              onStop();
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.stopPropagation();
                onStop();
              }
            }}
            className="grid h-7 w-7 shrink-0 place-items-center rounded-md text-content-muted transition-colors hover:bg-rose-500/15 hover:text-rose-300"
            aria-label={`Stop run ${run.run_id}`}
            data-testid={`stop-${run.run_id}`}
          >
            <Square className="h-3.5 w-3.5" />
          </span>
        )}
      </button>
    </li>
  );
}
