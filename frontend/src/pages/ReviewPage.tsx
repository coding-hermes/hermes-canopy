/**
 * Hermes Canopy — Review Page (SPEC-023-UI-004)
 *
 * PR review panel: a list of PR reviews on the left (PR number, risk gauge
 * mini, status) and a detail view on the right showing the selected
 * review's blast radius, Chimera verdict, and risk gauge.
 *
 *   GET  /api/v1/reviews           → ReviewListItem[]  (list)
 *   GET  /api/v1/reviews/{id}      → ReviewDetail       (detail)
 *   POST /api/v1/reviews/{pr}/trigger → ReviewDetail    (simulated review)
 *
 * The panel is also fed by live workspace channel events (review_event)
 * via useReviewFeed, so triggered reviews surface in real time. State is
 * local (no Yjs / zustand for MVP). Mirrors the AgentsPage layout
 * (rail + detail panel) and reuses the same theme tokens.
 */

import { useState, useEffect, useCallback } from 'react';
import {
  RefreshCw,
  AlertCircle,
  GitPullRequest,
  Zap,
  Radio,
} from 'lucide-react';
import { apiGet, apiPost } from '../lib/api.ts';
import { useReviewFeed } from '../hooks/useReviewFeed.ts';
import type {
  ReviewListItem,
  ReviewDetail,
  ReviewStatus,
} from '../types/review.ts';
import { RiskGauge } from '../components/review/RiskGauge.tsx';
import { BlastRadiusViz } from '../components/review/BlastRadiusViz.tsx';
import { ChimeraVerdictCard } from '../components/review/ChimeraVerdict.tsx';

// ─── Helpers ───────────────────────────────────────────────────────────

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

// ─── Status badge ──────────────────────────────────────────────────────

interface StatusBadgeProps {
  status: ReviewStatus;
}

const STATUS_META: Record<
  ReviewStatus,
  { label: string; classes: string }
> = {
  approved: {
    label: 'Approved',
    classes: 'text-status-success bg-status-success/10 ring-status-success/30',
  },
  requested_changes: {
    label: 'Changes',
    classes:
      'text-status-warning bg-status-warning/10 ring-status-warning/30',
  },
  reviewing: {
    label: 'Reviewing',
    classes: 'text-accent-2-300 bg-accent-2/10 ring-accent-2/30',
  },
  pending: {
    label: 'Pending',
    classes: 'text-content-muted bg-surface-input ring-line-subtle',
  },
};

function StatusBadge({ status }: StatusBadgeProps) {
  const meta = STATUS_META[status] ?? STATUS_META.pending;
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-sm px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset ${meta.classes}`}
    >
      {meta.label}
    </span>
  );
}

// ─── Review row (list item) ────────────────────────────────────────────

interface ReviewRowProps {
  review: ReviewListItem;
  active: boolean;
  onSelect: () => void;
}

function ReviewRow({ review, active, onSelect }: ReviewRowProps) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={active ? 'true' : undefined}
      title={review.title}
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
          <GitPullRequest className="h-3.5 w-3.5" />
        </span>
        <span
          className={[
            'flex-1 min-w-0 truncate text-sm',
            active
              ? 'font-medium text-content-primary'
              : 'text-content-tertiary group-hover:text-content-primary',
          ].join(' ')}
        >
          <span className="tabular-nums">#{review.pr}</span>{' '}
          {review.title}
        </span>
      </div>
      <div className="flex items-center gap-2 pl-9">
        <StatusBadge status={review.status} />
        <RiskGauge score={review.risk_score} mini />
        <span className="text-[11px] tabular-nums text-content-muted">
          {formatLastActive(review.updated_at)}
        </span>
      </div>
    </button>
  );
}

// ─── Live feed ticker ──────────────────────────────────────────────────

interface LiveFeedProps {
  status: ReturnType<typeof useReviewFeed>['status'];
  hasReceived: boolean;
  latestEventLabel: string | null;
}

function LiveFeedIndicator({ status, hasReceived, latestEventLabel }: LiveFeedProps) {
  const dotColor =
    status === 'open'
      ? 'bg-status-success'
      : status === 'error'
        ? 'bg-status-danger'
        : 'bg-content-faint';
  return (
    <div
      data-testid="review-live-feed"
      className="flex items-center gap-1.5 rounded-md bg-surface-input/60 px-2 py-1.5 ring-1 ring-inset ring-line-subtle"
    >
      <Radio
        className={`h-3 w-3 ${status === 'open' ? 'animate-pulse text-status-success' : 'text-content-muted'}`}
        aria-hidden="true"
      />
      <span className={`h-1.5 w-1.5 rounded-full ${dotColor}`} aria-hidden="true" />
      <span className="text-[11px] tabular-nums text-content-muted">
        {hasReceived
          ? (latestEventLabel ?? 'live')
          : status === 'open'
            ? 'connected'
            : status}
      </span>
    </div>
  );
}

// ─── Detail panel ──────────────────────────────────────────────────────

interface ReviewDetailPanelProps {
  review: ReviewDetail;
  onTrigger: () => void;
  triggering: boolean;
}

function ReviewDetailPanel({
  review,
  onTrigger,
  triggering,
}: ReviewDetailPanelProps) {
  return (
    <section
      aria-label={`Review for PR #${review.pr}`}
      data-testid="review-detail"
      className="flex min-h-0 flex-1 flex-col"
    >
      {/* Header */}
      <div className="flex shrink-0 items-center gap-2.5 border-b border-line-subtle px-4 py-3">
        <span className="grid h-8 w-8 shrink-0 place-items-center rounded-md bg-accent-2/15 text-accent-2-300 ring-1 ring-inset ring-accent-2/30">
          <GitPullRequest className="h-4 w-4" aria-hidden="true" />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="min-w-0 truncate text-sm font-semibold tracking-tight text-content-primary">
            <span className="tabular-nums">#{review.pr}</span> {review.title}
          </h2>
          <div className="mt-0.5 flex items-center gap-2">
            <StatusBadge status={review.status} />
            <span className="text-[11px] tabular-nums text-content-muted">
              by {review.author} · {formatLastActive(review.updated_at)}
            </span>
          </div>
        </div>
        <button
          type="button"
          onClick={onTrigger}
          disabled={triggering}
          aria-label="Trigger Chimera review"
          title="Trigger Chimera review"
          className="inline-flex shrink-0 items-center gap-1.5 rounded-md bg-accent-2/15 px-2.5 py-1.5 text-[11px] font-medium text-accent-2-300 ring-1 ring-inset ring-accent-2/30 transition-colors hover:bg-accent-2/25 disabled:opacity-50"
        >
          <Zap
            className={`h-3 w-3 ${triggering ? 'animate-pulse' : ''}`}
            aria-hidden="true"
          />
          {triggering ? 'Reviewing…' : 'Trigger'}
        </button>
      </div>

      {/* Body */}
      <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 py-4">
        {/* Risk gauge */}
        <div>
          <div className="mb-2 text-[11px] font-medium uppercase tracking-wide text-content-faint">
            Risk
          </div>
          <RiskGauge score={review.risk_score} />
        </div>

        {/* Blast radius */}
        <div>
          <div className="mb-2 text-[11px] font-medium uppercase tracking-wide text-content-faint">
            Blast radius
          </div>
          <BlastRadiusViz
            files={review.blast_radius.files_touched}
            dependents={review.blast_radius.dependents_count}
          />
        </div>

        {/* Chimera verdict */}
        <div>
          <div className="mb-2 text-[11px] font-medium uppercase tracking-wide text-content-faint">
            Chimera verdict
          </div>
          <ChimeraVerdictCard verdict={review.verdict} />
        </div>
      </div>
    </section>
  );
}

// ─── Page ──────────────────────────────────────────────────────────────

export default function ReviewPage() {
  const [reviews, setReviews] = useState<ReviewListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [listError, setListError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);

  // Detail state (fetched on selection).
  const [detail, setDetail] = useState<ReviewDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [triggering, setTriggering] = useState(false);

  // Live review events via the workspace general channel SSE feed.
  // The general channel UUID is deterministic
  // (uuid.NewSHA1(channelNamespace, "general") → 5d66dbfd-…).
  const GENERAL_CHANNEL = '5d66dbfd-61f6-5e61-b2f7-18b5b092c379';
  const feed = useReviewFeed(GENERAL_CHANNEL);

  const loadReviews = useCallback(async () => {
    setLoading(true);
    setListError(null);
    try {
      const data = await apiGet<ReviewListItem[]>('/reviews');
      setReviews(data ?? []);
      setSelectedId((prev) => prev ?? data?.[0]?.id ?? null);
    } catch (err) {
      setListError(
        err instanceof Error ? err.message : 'Failed to load reviews',
      );
      setReviews([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadReviews();
  }, [loadReviews]);

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
    apiGet<ReviewDetail>(`/reviews/${encodeURIComponent(selectedId)}`)
      .then((d) => {
        if (!cancelled) {
          setDetail(d);
          setDetailLoading(false);
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setDetailError(
            err instanceof Error ? err.message : 'Failed to load review detail',
          );
          setDetail(null);
          setDetailLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [selectedId]);

  // When a live review_event arrives, refresh the list so the rail shows
  // the latest status + risk score.
  useEffect(() => {
    if (feed.events.length === 0) return;
    void loadReviews();
  }, [feed.events.length, loadReviews]);

  // Trigger a simulated Chimera review for the selected PR.
  const handleTrigger = useCallback(async () => {
    if (!detail) return;
    setTriggering(true);
    try {
      const updated = await apiPost<ReviewDetail>(
        `/reviews/${encodeURIComponent(detail.pr)}/trigger`,
      );
      setDetail(updated);
      void loadReviews();
    } catch (err) {
      setDetailError(
        err instanceof Error ? err.message : 'Failed to trigger review',
      );
    } finally {
      setTriggering(false);
    }
  }, [detail, loadReviews]);

  const latestEvent = feed.events.at(-1) ?? null;
  const latestLabel = latestEvent
    ? `PR #${latestEvent.pr} ${latestEvent.verdict.replace('_', ' ')}`
    : null;

  return (
    <div className="flex h-full min-h-0">
      {/* Review rail */}
      <aside
        aria-label="PR reviews"
        data-testid="review-rail"
        className="flex w-72 shrink-0 flex-col border-r border-line-subtle bg-surface-panel"
      >
        <div className="flex shrink-0 items-center gap-1.5 px-4 pt-3 pb-2">
          <h1 className="flex-1 min-w-0 text-sm font-semibold tracking-tight text-content-primary">
            Reviews
          </h1>
          <button
            type="button"
            onClick={() => void loadReviews()}
            disabled={loading}
            aria-label="Refresh reviews"
            title="Refresh reviews"
            className="grid h-7 w-7 shrink-0 place-items-center rounded-md text-content-muted transition-colors hover:bg-surface-hover hover:text-content-primary disabled:opacity-50"
          >
            <RefreshCw
              className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`}
              aria-hidden="true"
            />
          </button>
        </div>

        <div className="px-4 pb-2">
          <LiveFeedIndicator
            status={feed.status}
            hasReceived={feed.hasReceived}
            latestEventLabel={latestLabel}
          />
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
              data-testid="reviews-error"
              className="flex items-start gap-2 rounded-lg border border-rose-500/30 bg-rose-500/10 p-2.5 text-[11px] text-status-danger"
            >
              <AlertCircle
                className="mt-px h-3.5 w-3.5 shrink-0"
                aria-hidden="true"
              />
              <span className="min-w-0 break-words">{listError}</span>
            </div>
          )}

          {!loading && !listError && reviews.length === 0 && (
            <div className="mx-auto mt-8 max-w-xs rounded-lg border border-line-subtle bg-surface-panel px-4 py-6 text-center">
              <GitPullRequest
                className="mx-auto mb-2 h-6 w-6 text-content-faint"
                aria-hidden="true"
              />
              <p className="text-xs font-medium text-content-secondary">
                No reviews
              </p>
              <p className="mt-1 text-[11px] text-content-muted">
                PR reviews will appear here.
              </p>
            </div>
          )}

          {!loading &&
            !listError &&
            reviews.map((r) => (
              <ReviewRow
                key={r.id}
                review={r}
                active={r.id === selectedId}
                onSelect={() => setSelectedId(r.id)}
              />
            ))}
        </div>
      </aside>

      {/* Detail panel */}
      <div className="flex min-h-0 flex-1 flex-col">
        {detailError && (
          <div
            role="alert"
            data-testid="review-detail-error"
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

        {detail && (
          <ReviewDetailPanel
            review={detail}
            onTrigger={handleTrigger}
            triggering={triggering}
          />
        )}

        {!detail && !detailLoading && !detailError && (
          <div className="mx-auto mt-12 max-w-xs rounded-lg border border-line-subtle bg-surface-panel px-4 py-6 text-center">
            <GitPullRequest
              className="mx-auto mb-2 h-6 w-6 text-content-faint"
              aria-hidden="true"
            />
            <p className="text-xs font-medium text-content-secondary">
              Select a review
            </p>
            <p className="mt-1 text-[11px] text-content-muted">
              View blast radius, risk, and Chimera verdicts.
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
