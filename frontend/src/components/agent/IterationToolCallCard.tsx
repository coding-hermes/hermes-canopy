/**
 * Hermes Canopy — IterationToolCallCard (§8.2)
 *
 * Compact: tool name and pending/running/result badge.
 * Expanded: params and result formatted through the safe JSON formatter
 * (depth + byte caps), plus duration/error. Approve/deny are offered only
 * when the call is gated and pending approval — never for a running or
 * terminal call.
 *
 * `params` and `result` are untrusted server data: they are serialized with
 * `formatJsonSafe` and rendered inside a <pre> as text, never as HTML.
 */

import { memo } from 'react';
import { Check, Wrench, X } from 'lucide-react';
import type { IterationRendererProps, ToolCallSubtypeData } from '../../types/agent.ts';
import { formatDurationMs } from '../../lib/iterationFormat.ts';
import { formatJsonSafe } from '../../lib/safeJson.ts';
import { IterationLiveRegion, IterationStatusBadge, statusLabel } from './iterationShared.tsx';

export type IterationToolCallCardProps = IterationRendererProps<ToolCallSubtypeData>;

function ToolCallCard({ cardId, data, compact, submitFeedback }: IterationToolCallCardProps) {
  const awaitingApproval = data.gated && data.status === 'pending_approval';
  const liveMessage = `${data.title}: ${statusLabel(data.status)}${awaitingApproval ? ' (approval required)' : ''}`;

  return (
    <div className="space-y-2" data-subtype="iteration_tool_call" data-testid={`iteration-tool-${cardId}`}>
      <IterationLiveRegion id={cardId} message={liveMessage} />

      {compact ? (
        <p className="text-xs text-content-secondary">
          <Wrench className="mr-1 inline h-3 w-3" aria-hidden="true" />
          <span className="font-mono text-content-primary">{data.toolName}</span>
          <span className="ml-2"><IterationStatusBadge state={data.status} /></span>
        </p>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm text-content-primary">{data.toolName}</span>
            <IterationStatusBadge state={data.status} />
            {data.gated && <span className="rounded bg-amber-500/20 px-1.5 py-0.5 text-[11px] text-amber-300">gated</span>}
            {data.durationMs != null && <span className="text-xs text-content-muted">{formatDurationMs(data.durationMs)}</span>}
          </div>

          {awaitingApproval && submitFeedback && (
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => void submitFeedback({ feedbackType: 'approve' })}
                className="inline-flex items-center gap-1 rounded bg-green-500/15 px-2 py-1 text-xs text-green-200 hover:bg-green-500/25"
                aria-label={`Approve ${data.title}`}
                data-testid={`iteration-tool-approve-${cardId}`}
              >
                <Check className="h-3 w-3" aria-hidden="true" /> Approve
              </button>
              <button
                type="button"
                onClick={() => void submitFeedback({ feedbackType: 'reject' })}
                className="inline-flex items-center gap-1 rounded bg-red-500/15 px-2 py-1 text-xs text-red-200 hover:bg-red-500/25"
                aria-label={`Deny ${data.title}`}
                data-testid={`iteration-tool-deny-${cardId}`}
              >
                <X className="h-3 w-3" aria-hidden="true" /> Deny
              </button>
            </div>
          )}

          {data.error && <p className="text-xs text-status-danger">{data.error}</p>}

          <div>
            <p className="text-xs font-medium text-content-primary">Params</p>
            <pre className="max-h-48 overflow-auto whitespace-pre-wrap rounded bg-surface-input p-2 font-mono text-xs text-content-primary">
              {formatJsonSafe(data.params)}
            </pre>
          </div>

          {data.result !== null && data.result !== undefined && (
            <div>
              <p className="text-xs font-medium text-content-primary">Result</p>
              <pre className="max-h-48 overflow-auto whitespace-pre-wrap rounded bg-surface-input p-2 font-mono text-xs text-content-primary">
                {formatJsonSafe(data.result)}
              </pre>
            </div>
          )}
        </>
      )}
    </div>
  );
}

export const IterationToolCallCard = memo(ToolCallCard);
