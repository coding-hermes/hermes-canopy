/**
 * Hermes Canopy — IterationSearchCard (§8.2)
 *
 * Compact: title, completed/total, current URL.
 * Expanded: URLs, snippets, result state, focus markers, and per-result
 * controls (highlight/focus, relevance, correction, approve/reject).
 *
 * Every server-supplied string (URLs, snippets) is rendered as text — never
 * interpreted as HTML.
 */

import { memo, useState } from 'react';
import { Check, Highlighter, Link2, ThumbsUp, X } from 'lucide-react';
import type {
  IterationFeedbackInput,
  IterationRendererProps,
  SearchBatchItem,
  SearchCardSubtypeData,
} from '../../types/agent.ts';
import { IterationLiveRegion, IterationStatusBadge, statusLabel } from './iterationShared.tsx';

export type IterationSearchCardProps = IterationRendererProps<SearchCardSubtypeData>;

const RETRIEVED = new Set(['retrieved', 'approved']);

function currentUrl(data: SearchCardSubtypeData): string {
  if (data.focusUrls && data.focusUrls.length > 0) return data.focusUrls[0]!;
  const last = data.currentBatch[data.currentBatch.length - 1];
  if (last) return last.url;
  if (data.urlsSearched.length > 0) return data.urlsSearched[data.urlsSearched.length - 1]!;
  return '';
}

function resultKey(result: SearchBatchItem): string {
  return result.snippetId || result.url;
}

function ResultControls({
  result,
  title,
  submitFeedback,
}: {
  result: SearchBatchItem;
  title: string;
  submitFeedback?: (input: IterationFeedbackInput) => void | Promise<void>;
}) {
  const target = { url: result.url, snippetId: result.snippetId };
  const send = (feedbackType: IterationFeedbackInput['feedbackType']) => {
    void submitFeedback?.({ feedbackType, target });
  };
  const button =
    'inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-content-secondary hover:bg-surface-hover hover:text-content-primary';
  return (
    <div className="flex flex-wrap items-center gap-1">
      <button type="button" className={button} onClick={() => send('highlight')} aria-label={`Highlight ${title}`}>
        <Highlighter className="h-3 w-3" aria-hidden="true" /> Focus
      </button>
      <button type="button" className={button} onClick={() => send('relevance')} aria-label={`Mark ${title} relevant`}>
        <ThumbsUp className="h-3 w-3" aria-hidden="true" /> Relevant
      </button>
      <button type="button" className={button} onClick={() => send('correction')} aria-label={`Correct ${title}`}>
        Correction
      </button>
      <button
        type="button"
        className={button}
        onClick={() => send('approve')}
        aria-label={`Approve ${title}`}
        data-testid={`search-approve-${result.snippetId || result.url}`}
      >
        <Check className="h-3 w-3" aria-hidden="true" /> Approve
      </button>
      <button
        type="button"
        className={button}
        onClick={() => send('reject')}
        aria-label={`Reject ${title}`}
        data-testid={`search-reject-${result.snippetId || result.url}`}
      >
        <X className="h-3 w-3" aria-hidden="true" /> Reject
      </button>
    </div>
  );
}

function SearchCard({ cardId, data, compact, submitFeedback }: IterationSearchCardProps) {
  const [highlighted, setHighlighted] = useState<Set<string>>(() => new Set());

  const retrieved = data.currentBatch.filter((result) => RETRIEVED.has(result.status)).length;
  const total = data.progress.total > 0 ? data.progress.total : data.currentBatch.length;
  const focusUrls = new Set(data.focusUrls ?? []);
  const url = currentUrl(data);

  const liveMessage = `${data.title}: ${statusLabel(data.state)}; ${retrieved} of ${total} results`;
  const toggleHighlight = (result: SearchBatchItem) => {
    const key = resultKey(result);
    setHighlighted((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
    void submitFeedback?.({ feedbackType: 'highlight', target: { url: result.url, snippetId: result.snippetId } });
  };

  return (
    <div className="space-y-2" data-subtype="iteration_search" data-testid={`iteration-search-${cardId}`}>
      <IterationLiveRegion id={cardId} message={liveMessage} />

      {compact ? (
        <p className="text-xs text-content-secondary">
          <span className="font-mono text-content-primary">{retrieved}/{total}</span>
          {url ? <span className="ml-1 truncate font-mono text-content-muted">· {url}</span> : null}
        </p>
      ) : (
        <>
          {data.urlsSearched.length > 0 && (
            <div>
              <p className="text-xs font-medium text-content-primary">URLs searched</p>
              <ul className="mt-1 space-y-0.5">
                {data.urlsSearched.map((item, index) => (
                  <li key={`${item}-${index}`} className="truncate font-mono text-xs text-content-muted">
                    <Link2 className="mr-1 inline h-3 w-3" aria-hidden="true" />
                    {item}
                  </li>
                ))}
              </ul>
            </div>
          )}

          <div>
            <p className="text-xs font-medium text-content-primary">Results ({retrieved}/{total})</p>
            {data.currentBatch.length === 0 && <p className="mt-1 text-xs text-content-muted">No results yet.</p>}
            <ul className="mt-1 space-y-2">
              {data.currentBatch.map((result) => {
                const key = resultKey(result);
                const focused = highlighted.has(key) || focusUrls.has(result.url);
                return (
                  <li
                    key={key}
                    className={`rounded border p-2 ${focused ? 'border-purple-500/50 ring-1 ring-purple-500/40' : 'border-line-subtle'}`}
                  >
                    <div className="flex items-center gap-2">
                      <span className="min-w-0 flex-1 truncate font-mono text-xs text-content-secondary">{result.url}</span>
                      <IterationStatusBadge state={result.status} />
                    </div>
                    {result.snippet && <p className="mt-1 whitespace-pre-wrap text-xs text-content-primary">{result.snippet}</p>}
                    <div className="mt-1.5 flex items-center gap-2">
                      <button
                        type="button"
                        onClick={() => toggleHighlight(result)}
                        aria-pressed={focused}
                        aria-label={`Focus ${data.title}`}
                        className={`rounded px-1.5 py-0.5 text-[11px] ${focused ? 'bg-purple-500/20 text-purple-300' : 'text-content-secondary hover:bg-surface-hover'}`}
                      >
                        {focused ? 'Focused' : 'Focus'}
                      </button>
                      {submitFeedback && <ResultControls result={result} title={data.title} submitFeedback={submitFeedback} />}
                    </div>
                  </li>
                );
              })}
            </ul>
          </div>
        </>
      )}
    </div>
  );
}

export const IterationSearchCard = memo(SearchCard);
