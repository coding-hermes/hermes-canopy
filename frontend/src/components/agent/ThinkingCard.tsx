/**
 * Hermes Canopy — ThinkingCard
 *
 * Displays agent reasoning/planning in a collapsible card.
 * Shows visible thought process (steps), not hidden chain-of-thought.
 * Collapsed: title + step count + progress indicator
 * Expanded: step details with status badges
 */

import { memo, useState } from 'react';
import {
  Brain,
  ChevronDown,
  ChevronRight,
  CheckCircle2,
  Loader2,
  XCircle,
  Circle,
  Clock,
} from 'lucide-react';
import type { ThinkingData, ThoughtStep } from '../../types/agent.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface ThinkingCardProps {
  data: ThinkingData;
  /** Optional class name for the outer container */
  className?: string;
}

// ─── Step status helpers ───────────────────────────────────────────────

const STEP_STATUS_ICON: Record<ThoughtStep['status'], React.ReactNode> = {
  pending: <Circle className="w-3.5 h-3.5 text-gray-500 dark:text-gray-500" />,
  active: (
    <Loader2 className="w-3.5 h-3.5 text-purple-400 animate-spin" />
  ),
  completed: (
    <CheckCircle2 className="w-3.5 h-3.5 text-green-400" />
  ),
  failed: <XCircle className="w-3.5 h-3.5 text-red-400" />,
};

const STEP_STATUS_COLOR: Record<ThoughtStep['status'], string> = {
  pending: 'text-gray-400 dark:text-gray-500',
  active: 'text-purple-300 dark:text-purple-300',
  completed: 'text-green-300 dark:text-green-300',
  failed: 'text-red-300 dark:text-red-300',
};

function formatDuration(ms: number | null): string {
  if (ms === null) return '';
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

// ─── Component ─────────────────────────────────────────────────────────

function ThinkingCardComponent({ data, className = '' }: ThinkingCardProps) {
  const [expanded, setExpanded] = useState(!data._collapsed);
  const steps = data.steps ?? [];
  const completedCount = steps.filter((s) => s.status === 'completed').length;
  const hasSteps = steps.length > 0;

  return (
    <div
      className={`rounded-lg border bg-gray-800/90 border-gray-700 shadow-sm min-w-[200px] max-w-[320px] ${className}`}
    >
      {/* Header */}
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="w-full flex items-center gap-2 px-3 py-2 rounded-t-lg hover:bg-gray-750
                   transition-colors text-left"
      >
        <Brain className="w-4 h-4 text-purple-400 flex-shrink-0" />
        <span className="text-sm font-medium text-gray-200 truncate flex-1">
          {data.title || 'Thinking'}
        </span>

        {/* Step progress */}
        {hasSteps && (
          <span className="text-xs text-gray-400 flex-shrink-0">
            {completedCount}/{steps.length}
          </span>
        )}

        {/* State badge */}
        {data.state === 'running' && (
          <span className="text-xs px-1.5 py-0.5 rounded-full bg-purple-500/20 text-purple-300 flex-shrink-0">
            running
          </span>
        )}
        {data.state === 'completed' && (
          <span className="text-xs px-1.5 py-0.5 rounded-full bg-green-500/20 text-green-300 flex-shrink-0">
            done
          </span>
        )}
        {data.state === 'failed' && (
          <span className="text-xs px-1.5 py-0.5 rounded-full bg-red-500/20 text-red-300 flex-shrink-0">
            failed
          </span>
        )}

        {expanded ? (
          <ChevronDown className="w-4 h-4 text-gray-500 flex-shrink-0" />
        ) : (
          <ChevronRight className="w-4 h-4 text-gray-500 flex-shrink-0" />
        )}
      </button>

      {/* Expanded steps */}
      {expanded && hasSteps && (
        <div className="border-t border-gray-700/60 px-3 py-2 space-y-1.5">
          {steps.map((step) => (
            <div key={step.id} className="flex items-start gap-2 text-sm">
              <span className="mt-0.5 flex-shrink-0">
                {STEP_STATUS_ICON[step.status]}
              </span>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className={`${STEP_STATUS_COLOR[step.status]} truncate`}>
                    {step.title}
                  </span>
                  {step.duration_ms != null && (
                    <span className="text-xs text-gray-500 flex-shrink-0">
                      <Clock className="w-3 h-3 inline mr-0.5" />
                      {formatDuration(step.duration_ms)}
                    </span>
                  )}
                </div>
                {step.status === 'failed' && step.error && (
                  <p className="text-xs text-red-400 mt-0.5">{step.error}</p>
                )}
                {step.content && step.status !== 'failed' && (
                  <p className="text-xs text-gray-400 mt-0.5 line-clamp-2">
                    {step.content}
                  </p>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Expanded empty state */}
      {expanded && !hasSteps && (
        <div className="border-t border-gray-700/60 px-3 py-3">
          <p className="text-xs text-gray-500 italic">No thinking steps yet…</p>
        </div>
      )}

      {/* Progress bar */}
      {hasSteps && data.progress && (
        <div className="border-t border-gray-700/60 px-3 py-1.5">
          <div className="flex items-center gap-2">
            <div className="flex-1 h-1.5 bg-gray-700 rounded-full overflow-hidden">
              <div
                className="h-full bg-purple-500 rounded-full transition-all duration-300"
                style={{
                  width: `${data.progress.total > 0 ? (data.progress.current / data.progress.total) * 100 : 0}%`,
                }}
              />
            </div>
            <span className="text-xs text-gray-500 flex-shrink-0">
              {data.progress.current}/{data.progress.total}
            </span>
          </div>
        </div>
      )}
    </div>
  );
}

export const ThinkingCard = memo(ThinkingCardComponent);
