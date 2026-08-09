/**
 * Hermes Canopy — ChimeraVerdictCard Component (SPEC-023-UI-004)
 *
 * Displays the Chimera multi-model review verdict: verdict type (approve /
 * request_changes / error), model formation (single-judge / dual-review /
 * triple-jury), confidence, summary, and timestamp. Shows a pending
 * placeholder when no verdict has been produced yet.
 */

import { memo } from 'react';
import { CheckCircle2, AlertTriangle, XCircle, Clock } from 'lucide-react';
import type { ChimeraVerdict } from '../../types/review.ts';

interface ChimeraVerdictCardProps {
  verdict: ChimeraVerdict | null;
}

const VERDICT_META = {
  approve: {
    icon: CheckCircle2,
    label: 'Approved',
    classes: 'text-status-success bg-status-success/10 ring-status-success/30',
    accent: '#22c55e',
  },
  request_changes: {
    icon: AlertTriangle,
    label: 'Changes Requested',
    classes:
      'text-status-warning bg-status-warning/10 ring-status-warning/30',
    accent: '#f59e0b',
  },
  error: {
    icon: XCircle,
    label: 'Review Error',
    classes: 'text-status-danger bg-status-danger/10 ring-status-danger/30',
    accent: '#ef4444',
  },
} as const;

function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

export const ChimeraVerdictCard = memo(function ChimeraVerdictCard({
  verdict,
}: ChimeraVerdictCardProps) {
  if (!verdict) {
    return (
      <div
        data-testid="chimera-verdict"
        className="flex items-center gap-2 rounded-lg bg-surface-input/60 p-3 ring-1 ring-inset ring-line-subtle"
      >
        <Clock className="h-4 w-4 text-content-faint" aria-hidden="true" />
        <span className="text-xs text-content-muted">
          No Chimera verdict yet — trigger a review to run the multi-model
          evaluation.
        </span>
      </div>
    );
  }

  const meta = VERDICT_META[verdict.verdict] ?? VERDICT_META.error;
  const Icon = meta.icon;
  const confidencePct = Math.round(verdict.confidence * 100);

  return (
    <div
      data-testid="chimera-verdict"
      className="rounded-lg bg-surface-input/60 p-3 ring-1 ring-inset ring-line-subtle"
    >
      <div className="flex items-center gap-2.5">
        <span
          className={`inline-flex items-center gap-1 rounded-sm px-2 py-1 text-xs font-medium ring-1 ring-inset ${meta.classes}`}
        >
          <Icon className="h-3.5 w-3.5" aria-hidden="true" />
          {meta.label}
        </span>
        <span className="rounded-sm bg-surface-base px-1.5 py-0.5 text-[11px] font-mono text-content-muted ring-1 ring-inset ring-line-subtle">
          {verdict.model_formation}
        </span>
      </div>

      <p className="mt-2 text-xs text-content-secondary">{verdict.summary}</p>

      <div className="mt-3 flex items-center gap-3">
        {/* Confidence bar */}
        <div className="flex items-center gap-1.5">
          <span className="text-[11px] text-content-muted">confidence</span>
          <div className="flex items-center gap-1">
            <div className="h-1.5 w-20 overflow-hidden rounded-full bg-surface-base">
              <div
                className="h-full rounded-full"
                style={{
                  width: `${confidencePct}%`,
                  backgroundColor: meta.accent,
                }}
              />
            </div>
            <span className="w-8 text-right text-[11px] tabular-nums text-content-muted">
              {confidencePct}%
            </span>
          </div>
        </div>
        <span className="ml-auto text-[11px] tabular-nums text-content-faint">
          {formatTime(verdict.at)}
        </span>
      </div>
    </div>
  );
});
