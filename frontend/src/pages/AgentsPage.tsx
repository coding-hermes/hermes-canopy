/**
 * Hermes Canopy — Agents Page (SPEC-023-UI-003)
 *
 * Agent roster surface: a list of provisioned agents on the left (name,
 * tier badge, trust score) and a detail view on the right showing the
 * selected agent's capabilities, incidents, and trust timeline.
 *
 *   GET /api/v1/agents        → AgentListItem[]   (roster)
 *   GET /api/v1/agents/{id}   → AgentDetail        (detail + trust timeline)
 *
 * State is local (no Yjs / zustand for MVP per SPEC-023 §7). This mirrors
 * the WorkspacePage layout (rail + detail panel) and reuses the same
 * theme tokens (content-*, surface-*, accent-2-*, line-subtle).
 */

import { useState, useEffect, useCallback } from 'react';
import {
  Bot,
  RefreshCw,
  AlertCircle,
  ShieldCheck,
  ShieldAlert,
  ShieldQuestion,
  TrendingUp,
  Activity,
} from 'lucide-react';
import { apiGet } from '../lib/api.ts';
import type {
  AgentListItem,
  AgentDetail,
  AgentTier,
} from '../types/agents.ts';

// ─── Helpers ───────────────────────────────────────────────────────────

/** Compact relative-time label, e.g. "3m ago". Falls back to the raw string. */
function formatLastActive(iso: string): string {
  try {
    const ms = Date.now() - new Date(iso).getTime();
    const sec = Math.floor(ms / 1000);
    if (sec < 60) return 'just now';
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ago`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr}h ago`;
    const day = Math.floor(hr / 24);
    if (day < 30) return `${day}d ago`;
    return new Date(iso).toLocaleDateString();
  } catch {
    return iso;
  }
}

/** Format a 0..1 trust score as a percentage with one decimal. */
function trustPct(score: number): string {
  return `${(score * 100).toFixed(1)}%`;
}

// ─── Tier badge ────────────────────────────────────────────────────────

interface TierBadgeProps {
  tier: AgentTier;
}

const TIER_META: Record<
  AgentTier,
  { label: string; icon: typeof ShieldCheck; classes: string }
> = {
  veteran: {
    label: 'Veteran',
    icon: ShieldCheck,
    classes:
      'text-status-success bg-status-success/10 ring-status-success/30',
  },
  established: {
    label: 'Established',
    icon: ShieldCheck,
    classes: 'text-accent-2-300 bg-accent-2/10 ring-accent-2/30',
  },
  provisional: {
    label: 'Provisional',
    icon: ShieldQuestion,
    classes:
      'text-status-warning bg-status-warning/10 ring-status-warning/30',
  },
};

function TierBadge({ tier }: TierBadgeProps) {
  const meta = TIER_META[tier] ?? TIER_META.provisional;
  const Icon = meta.icon;
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-sm px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset ${meta.classes}`}
    >
      <Icon className="h-3 w-3" aria-hidden="true" />
      {meta.label}
    </span>
  );
}

// ─── Agent row (roster list item) ──────────────────────────────────────

interface AgentRowProps {
  agent: AgentListItem;
  active: boolean;
  onSelect: () => void;
}

function AgentRow({ agent, active, onSelect }: AgentRowProps) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={active ? 'true' : undefined}
      title={agent.name}
      className={[
        'group w-full flex flex-col gap-1.5 rounded-lg px-2.5 py-2.5 transition-colors text-left',
        active
          ? 'bg-accent-2/12 ring-1 ring-inset ring-accent-2/35'
          : 'ring-1 ring-inset ring-transparent hover:bg-surface-hover/50',
      ].join(' ')}
    >
      <div className="flex items-center gap-2.5">
        <span
          aria-hidden="true"
          className={[
            'grid h-7 w-7 shrink-0 place-items-center rounded-md ring-1 ring-inset transition-colors',
            active
              ? 'bg-accent-2/20 text-accent-2-300 ring-accent-2/40'
              : 'bg-surface-input text-content-tertiary ring-line-subtle group-hover:text-content-secondary',
          ].join(' ')}
        >
          <Bot className="h-3.5 w-3.5" />
        </span>
        <span
          className={[
            'flex-1 min-w-0 truncate text-sm',
            active
              ? 'font-medium text-content-primary'
              : 'text-content-tertiary group-hover:text-content-primary',
          ].join(' ')}
        >
          {agent.name}
        </span>
      </div>
      <div className="flex items-center gap-2 pl-9">
        <TierBadge tier={agent.tier} />
        <span
          className="inline-flex items-center gap-0.5 text-[11px] font-medium tabular-nums text-content-muted"
          title="Trust score"
        >
          <TrendingUp className="h-3 w-3" aria-hidden="true" />
          {trustPct(agent.trust_score)}
        </span>
      </div>
    </button>
  );
}

// ─── Trust timeline ────────────────────────────────────────────────────

function TrustTimeline({ history }: { history: AgentDetail['trust_history'] }) {
  if (!history || history.length === 0) {
    return (
      <p className="text-xs text-content-muted">No trust history yet.</p>
    );
  }
  // Compute the bar height range from the history scores (0..1).
  const max = Math.max(...history.map((h) => h.score), 0.1);
  return (
    <div
      data-testid="trust-timeline"
      className="flex items-end gap-2"
      role="img"
      aria-label="Trust score over time"
    >
      {history.map((entry, i) => {
        const heightPct = Math.max((entry.score / max) * 100, 8);
        return (
          <div
            key={`${entry.at}-${i}`}
            className="flex flex-1 flex-col items-center gap-1"
          >
            <span className="text-[10px] font-medium tabular-nums text-content-muted">
              {trustPct(entry.score)}
            </span>
            <div className="flex h-16 w-full items-end rounded-md bg-surface-input ring-1 ring-inset ring-line-subtle">
              <div
                className="w-full rounded-md bg-accent-2-600/70 transition-all"
                style={{ height: `${heightPct}%` }}
              />
            </div>
            <span
              className="text-[10px] tabular-nums text-content-faint"
              title={entry.at}
            >
              {formatLastActive(entry.at)}
            </span>
          </div>
        );
      })}
    </div>
  );
}

// ─── Agent detail panel ────────────────────────────────────────────────

interface AgentDetailPanelProps {
  agent: AgentDetail;
}

function AgentDetailPanel({ agent }: AgentDetailPanelProps) {
  return (
    <section
      aria-label={`Agent ${agent.name} detail`}
      data-testid="agent-detail"
      className="flex min-h-0 flex-1 flex-col"
    >
      {/* Header */}
      <div className="flex shrink-0 items-center gap-2.5 border-b border-line-subtle px-4 py-3">
        <span className="grid h-8 w-8 shrink-0 place-items-center rounded-md bg-accent-2/15 text-accent-2-300 ring-1 ring-inset ring-accent-2/30">
          <Bot className="h-4 w-4" aria-hidden="true" />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="min-w-0 truncate text-sm font-semibold tracking-tight text-content-primary">
            {agent.name}
          </h2>
          <div className="mt-0.5 flex items-center gap-2">
            <TierBadge tier={agent.tier} />
            <span className="text-[11px] tabular-nums text-content-muted">
              last active {formatLastActive(agent.last_active)}
            </span>
          </div>
        </div>
      </div>

      {/* Body */}
      <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 py-4">
        {/* Trust score */}
        <div>
          <div className="flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wide text-content-faint">
            <TrendingUp className="h-3 w-3" aria-hidden="true" />
            Trust score
          </div>
          <div className="mt-1 flex items-baseline gap-2">
            <span className="text-2xl font-bold tabular-nums text-content-primary">
              {trustPct(agent.trust_score)}
            </span>
            <span className="inline-flex items-center gap-0.5 text-[11px] text-content-muted">
              <ShieldAlert className="h-3 w-3" aria-hidden="true" />
              {agent.incidents} {agent.incidents === 1 ? 'incident' : 'incidents'}
            </span>
          </div>
        </div>

        {/* Trust timeline */}
        <div>
          <div className="mb-2 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wide text-content-faint">
            <Activity className="h-3 w-3" aria-hidden="true" />
            Trust timeline
          </div>
          <TrustTimeline history={agent.trust_history} />
        </div>

        {/* Capabilities */}
        <div>
          <div className="mb-2 text-[11px] font-medium uppercase tracking-wide text-content-faint">
            Capabilities
          </div>
          <div className="space-y-1.5">
            {Object.entries(agent.capabilities).map(([name, stat]) => {
              const rate =
                stat.total > 0 ? (stat.success / stat.total) * 100 : 0;
              return (
                <div
                  key={name}
                  data-testid="agent-capability"
                  className="flex items-center gap-3 rounded-md bg-surface-input/60 px-3 py-2 ring-1 ring-inset ring-line-subtle"
                >
                  <span className="flex-1 min-w-0 truncate text-sm text-content-secondary">
                    {name}
                  </span>
                  <div className="flex items-center gap-2">
                    <div className="h-1.5 w-20 overflow-hidden rounded-full bg-surface-base">
                      <div
                        className="h-full rounded-full bg-accent-2-600"
                        style={{ width: `${rate}%` }}
                      />
                    </div>
                    <span className="w-16 text-right text-[11px] tabular-nums text-content-muted">
                      {stat.success}/{stat.total}
                    </span>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      </div>
    </section>
  );
}

// ─── Page ──────────────────────────────────────────────────────────────

export default function AgentsPage() {
  const [agents, setAgents] = useState<AgentListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [listError, setListError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);

  // Detail state (fetched on selection).
  const [detail, setDetail] = useState<AgentDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<string | null>(null);

  const loadAgents = useCallback(async () => {
    setLoading(true);
    setListError(null);
    try {
      const data = await apiGet<AgentListItem[]>('/agents');
      setAgents(data ?? []);
      // Auto-select the first agent once loaded (sorted by name server-side).
      setSelectedId((prev) => prev ?? data?.[0]?.id ?? null);
    } catch (err) {
      setListError(
        err instanceof Error ? err.message : 'Failed to load agents',
      );
      setAgents([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadAgents();
  }, [loadAgents]);

  // Fetch detail when the selection changes.
  useEffect(() => {
    if (!selectedId) {
      setDetail(null);
      setDetailError(null);
      return;
    }
    let cancelled = false;
    setDetailLoading(true);
    setDetailError(null);
    apiGet<AgentDetail>(`/agents/${encodeURIComponent(selectedId)}`)
      .then((d) => {
        if (!cancelled) {
          setDetail(d);
          setDetailLoading(false);
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setDetailError(
            err instanceof Error ? err.message : 'Failed to load agent detail',
          );
          setDetail(null);
          setDetailLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [selectedId]);

  return (
    <div className="flex h-full min-h-0">
      {/* Roster rail */}
      <aside
        aria-label="Agent roster"
        data-testid="agent-roster"
        className="flex w-64 shrink-0 flex-col border-r border-line-subtle bg-surface-panel"
      >
        <div className="flex shrink-0 items-center gap-1.5 px-4 pt-3 pb-2">
          <h1 className="flex-1 min-w-0 text-sm font-semibold tracking-tight text-content-primary">
            Agents
          </h1>
          <button
            type="button"
            onClick={() => void loadAgents()}
            disabled={loading}
            aria-label="Refresh agents"
            title="Refresh agents"
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
              {[0, 1, 2].map((i) => (
                <div
                  key={i}
                  className="h-14 animate-pulse rounded-lg bg-surface-input/70"
                />
              ))}
            </div>
          )}

          {!loading && listError && (
            <div
              role="alert"
              data-testid="agents-error"
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
            agents.map((a) => (
              <AgentRow
                key={a.id}
                agent={a}
                active={a.id === selectedId}
                onSelect={() => setSelectedId(a.id)}
              />
            ))}
        </div>
      </aside>

      {/* Detail panel */}
      <div className="flex min-h-0 flex-1 flex-col">
        {detailError && (
          <div
            role="alert"
            data-testid="agent-detail-error"
            className="m-4 flex items-start gap-2 rounded-lg border border-rose-500/30 bg-rose-500/10 p-3 text-xs text-status-danger"
          >
            <AlertCircle
              className="mt-px h-4 w-4 shrink-0"
              aria-hidden="true"
            />
            <span className="min-w-0 break-words">{detailError}</span>
          </div>
        )}

        {detailLoading && !detail && (
          <div className="m-4 space-y-3" aria-hidden="true">
            <div className="h-8 w-48 animate-pulse rounded-md bg-surface-input/70" />
            <div className="h-16 w-full animate-pulse rounded-md bg-surface-input/70" />
            <div className="h-10 w-full animate-pulse rounded-md bg-surface-input/70" />
          </div>
        )}

        {detail && <AgentDetailPanel agent={detail} />}

        {!detail && !detailLoading && !detailError && (
          <div className="mx-auto mt-12 max-w-xs rounded-lg border border-line-subtle bg-surface-panel px-4 py-6 text-center">
            <Bot
              className="mx-auto mb-2 h-6 w-6 text-content-faint"
              aria-hidden="true"
            />
            <p className="text-xs font-medium text-content-secondary">
              Select an agent
            </p>
            <p className="mt-1 text-[11px] text-content-muted">
              View trust scores, capabilities, and history.
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
