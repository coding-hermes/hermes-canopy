/**
 * Hermes Canopy — IterationThinkingCard (§8.2)
 *
 * Compact: active step title and current/total.
 * Expanded: collapsible reasoning-digest steps with durations and errors. The
 * only control is per-step expand/collapse (client-local presentation state).
 *
 * Step content is user-visible digest text rendered as text; it must never
 * carry hidden chain-of-thought and is never interpreted as HTML.
 */

import { memo, useState } from 'react';
import { Brain, CheckCircle2, ChevronDown, ChevronRight, Circle, Loader2, XCircle } from 'lucide-react';
import type { IterationRendererProps, ThinkingData, ThoughtStep } from '../../types/agent.ts';
import { formatDurationMs } from '../../lib/iterationFormat.ts';
import { IterationLiveRegion, statusLabel } from './iterationShared.tsx';

export type IterationThinkingCardProps = IterationRendererProps<ThinkingData>;

const STEP_ICON: Record<ThoughtStep['status'], React.ReactNode> = {
  pending: <Circle className="h-3.5 w-3.5 text-content-muted" aria-hidden="true" />,
  active: <Loader2 className="h-3.5 w-3.5 animate-spin text-purple-300" aria-hidden="true" />,
  completed: <CheckCircle2 className="h-3.5 w-3.5 text-status-success" aria-hidden="true" />,
  failed: <XCircle className="h-3.5 w-3.5 text-status-danger" aria-hidden="true" />,
};

function ThinkingCard({ cardId, data, compact }: IterationThinkingCardProps) {
  const steps = data.steps ?? [];
  const [expandedSteps, setExpandedSteps] = useState<Set<string>>(() => new Set());

  const active = steps.find((step) => step.id === data.currentStepId) ?? steps.find((step) => step.status === 'active');
  const completed = steps.filter((step) => step.status === 'completed').length;
  const liveMessage = `${data.title}: ${statusLabel(data.state)}; ${active ? `active step ${active.title}` : `${completed}/${steps.length} steps`}`;

  const toggle = (id: string) => setExpandedSteps((current) => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    return next;
  });

  return (
    <div className="space-y-2" data-subtype="iteration_thinking" data-testid={`iteration-thinking-${cardId}`}>
      <IterationLiveRegion id={cardId} message={liveMessage} />

      {compact ? (
        <p className="text-xs text-content-secondary">
          <Brain className="mr-1 inline h-3 w-3" aria-hidden="true" />
          <span className="text-content-primary">{active ? active.title : `${completed}/${steps.length} steps`}</span>
          <span className="ml-2 text-content-muted">· {data.progress.current}/{data.progress.total}</span>
        </p>
      ) : steps.length === 0 ? (
        <p className="text-xs text-content-muted">No thinking steps yet.</p>
      ) : (
        <ul className="space-y-1">
          {steps.map((step) => {
            const isOpen = expandedSteps.has(step.id);
            return (
              <li key={step.id} className="rounded border border-line-subtle">
                <button
                  type="button"
                  onClick={() => toggle(step.id)}
                  aria-expanded={isOpen}
                  aria-controls={`iteration-step-${cardId}-${step.id}`}
                  aria-label={`${isOpen ? 'Collapse' : 'Expand'} step ${step.title} for ${data.title}`}
                  className="flex w-full items-center gap-2 px-2 py-1.5 text-left text-xs hover:bg-surface-hover"
                >
                  <span className="flex-shrink-0">{STEP_ICON[step.status]}</span>
                  <span className="min-w-0 flex-1 truncate text-content-primary">{step.title}</span>
                  {step.duration_ms != null && <span className="shrink-0 text-content-muted">{formatDurationMs(step.duration_ms)}</span>}
                  {isOpen ? <ChevronDown className="h-3.5 w-3.5 shrink-0 text-content-muted" aria-hidden="true" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0 text-content-muted" aria-hidden="true" />}
                </button>
                {isOpen && (
                  <div id={`iteration-step-${cardId}-${step.id}`} className="border-t border-line-subtle px-2 py-1.5">
                    {step.content && <p className="whitespace-pre-wrap text-xs text-content-primary">{step.content}</p>}
                    {step.status === 'failed' && step.error && <p className="mt-0.5 text-xs text-status-danger">{step.error}</p>}
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

export const IterationThinkingCard = memo(ThinkingCard);
