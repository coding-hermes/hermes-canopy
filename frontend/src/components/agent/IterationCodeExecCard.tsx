/**
 * Hermes Canopy — IterationCodeExecCard (§8.2)
 *
 * Compact: command summary, running/terminal badge, elapsed time.
 * Expanded: scrollable terminal with separate stdout/stderr, full command and
 * workdir, exit code. Cancel is offered only while the command is running.
 *
 * Output chunks are rendered as text inside <pre> blocks; no ANSI-to-HTML
 * conversion and no HTML interpretation of server strings.
 */

import { memo } from 'react';
import { Square, Terminal } from 'lucide-react';
import type { CodeExecSubtypeData, IterationRendererProps } from '../../types/agent.ts';
import { formatElapsed } from '../../lib/iterationFormat.ts';
import { IterationLiveRegion, IterationStatusBadge, statusLabel } from './iterationShared.tsx';

export type IterationCodeExecCardProps = IterationRendererProps<CodeExecSubtypeData>;

function joinChunks(chunks: string[]): string {
  return chunks.join('');
}

function CodeExecCard({ cardId, data, compact, cancel }: IterationCodeExecCardProps) {
  const running = data.status === 'running' && !data.cancelled;
  const elapsed = formatElapsed(data.startTime, data.endTime);
  const liveMessage = `${data.title}: ${statusLabel(data.status)}${data.exitCode !== null ? `, exit ${data.exitCode}` : ''}`;

  return (
    <div className="space-y-2" data-subtype="iteration_code_exec" data-testid={`iteration-code-${cardId}`}>
      <IterationLiveRegion id={cardId} message={liveMessage} />

      {compact ? (
        <p className="text-xs text-content-secondary">
          <Terminal className="mr-1 inline h-3 w-3" aria-hidden="true" />
          <span className="font-mono text-content-primary">{data.command || '(no command)'}</span>
          {elapsed && <span className="ml-2 text-content-muted">· {elapsed}</span>}
        </p>
      ) : (
        <>
          <div className="space-y-1">
            <p className="text-xs text-content-secondary">
              <span className="text-content-muted">$</span>{' '}
              <span className="font-mono text-content-primary">{data.command || '(no command)'}</span>
            </p>
            {data.workdir && <p className="truncate font-mono text-xs text-content-muted">in {data.workdir}</p>}
            <div className="flex flex-wrap items-center gap-2">
              <IterationStatusBadge state={data.status} />
              {elapsed && <span className="text-xs text-content-muted">{elapsed}</span>}
              {data.exitCode !== null && (
                <span className={`font-mono text-xs ${data.exitCode === 0 ? 'text-status-success' : 'text-status-danger'}`}>
                  exit {data.exitCode}
                </span>
              )}
              {running && cancel && (
                <button
                  type="button"
                  onClick={() => void cancel('Cancelled by user')}
                  className="inline-flex items-center gap-1 rounded bg-red-500/15 px-2 py-1 text-xs text-red-200 hover:bg-red-500/25"
                  aria-label={`Cancel ${data.title}`}
                >
                  <Square className="h-3 w-3" aria-hidden="true" /> Cancel
                </button>
              )}
            </div>
          </div>

          <div className="space-y-1.5">
            <div>
              <p className="text-xs font-medium text-content-primary">stdout</p>
              <pre
                className="max-h-48 overflow-auto whitespace-pre-wrap rounded bg-surface-input p-2 font-mono text-xs text-content-primary"
                data-testid={`iteration-code-stdout-${cardId}`}
              >
                {joinChunks(data.stdout) || '(no output)'}
              </pre>
            </div>
            {data.stderr.length > 0 && (
              <div>
                <p className="text-xs font-medium text-content-primary">stderr</p>
                <pre
                  className="max-h-48 overflow-auto whitespace-pre-wrap rounded bg-surface-input p-2 font-mono text-xs text-content-secondary"
                  data-testid={`iteration-code-stderr-${cardId}`}
                >
                  {joinChunks(data.stderr)}
                </pre>
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
}

export const IterationCodeExecCard = memo(CodeExecCard);
