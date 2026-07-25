/**
 * Hermes Canopy — SearchResultCard
 *
 * Displays an individual search result with URL, snippet,
 * relevance indicator, and user feedback controls.
 *
 * Rendered within an IterationCard (search subtype) or standalone
 * as a card node child of an agent message.
 */

import { memo, useState, useCallback } from 'react';
import {
  ThumbsUp,
  ThumbsDown,
  Highlighter,
  ExternalLink,
  EyeOff,
} from 'lucide-react';
import type { SearchResult, SearchFeedbackPayload } from '../../types/agent.ts';

// ─── Props ─────────────────────────────────────────────────────────────

export interface SearchResultCardProps {
  result: SearchResult;
  /** Called when user provides feedback on this result */
  onFeedback?: (result: SearchResult, feedback: SearchFeedbackPayload) => void;
  /** Whether feedback controls are enabled */
  feedbackEnabled?: boolean;
  className?: string;
}

// ─── Relevance color ──────────────────────────────────────────────────

function relevanceColor(score: number | undefined): string {
  if (score == null) return 'bg-gray-600';
  if (score >= 0.8) return 'bg-green-500';
  if (score >= 0.5) return 'bg-amber-500';
  return 'bg-red-500';
}

// ─── Truncate URL for display ──────────────────────────────────────────

function displayUrl(url: string): string {
  try {
    const u = new URL(url);
    const path =
      u.pathname.length > 30
        ? u.pathname.slice(0, 27) + '…'
        : u.pathname;
    return `${u.host}${path}`;
  } catch {
    return url.length > 50 ? url.slice(0, 47) + '…' : url;
  }
}

// ─── Component ─────────────────────────────────────────────────────────

function SearchResultCardComponent({
  result,
  onFeedback,
  feedbackEnabled = true,
  className = '',
}: SearchResultCardProps) {
  const [highlighted, setHighlighted] = useState(result.highlighted ?? false);
  const [feedbackKind, setFeedbackKind] = useState<'none' | 'approve' | 'reject'>('none');

  const handleApprove = useCallback(() => {
    const newKind = feedbackKind === 'approve' ? 'none' : 'approve';
    setFeedbackKind(newKind);
    onFeedback?.(result, {
      kind: newKind === 'approve' ? 'approve' : 'reject',
      target: { url: result.url, snippetId: result.snippetId },
    });
  }, [result, onFeedback, feedbackKind]);

  const handleReject = useCallback(() => {
    const newKind = feedbackKind === 'reject' ? 'none' : 'reject';
    setFeedbackKind(newKind);
    onFeedback?.(result, {
      kind: newKind === 'reject' ? 'reject' : 'approve',
      target: { url: result.url, snippetId: result.snippetId },
    });
  }, [result, onFeedback, feedbackKind]);

  const handleToggleHighlight = useCallback(() => {
    setHighlighted((v) => !v);
  }, []);

  const isRetrieved = result.status === 'retrieved';
  const isSearching = result.status === 'searching' || result.status === 'queued';

  return (
    <div
      className={`rounded-lg border bg-gray-800/90 border-gray-700 shadow-sm min-w-[220px] max-w-[320px] 
        ${highlighted ? 'ring-1 ring-purple-500/50 border-purple-500/50' : ''} 
        ${feedbackKind === 'approve' ? 'ring-1 ring-green-500/50' : ''}
        ${feedbackKind === 'reject' ? 'ring-1 ring-red-500/50 opacity-70' : ''}
        ${className}`}
    >
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-2">
        {/* Relevance indicator */}
        <div
          className={`w-2 h-2 rounded-full flex-shrink-0 ${relevanceColor(result.relevance)}`}
          title={
            result.relevance != null
              ? `Relevance: ${Math.round(result.relevance * 100)}%`
              : 'Unknown relevance'
          }
        />

        {/* URL */}
        <a
          href={result.url}
          target="_blank"
          rel="noopener noreferrer"
          className="flex-1 text-xs text-blue-400 hover:text-blue-300 truncate font-mono min-w-0"
          onClick={(e) => e.stopPropagation()}
        >
          {displayUrl(result.url)}
          <ExternalLink className="w-3 h-3 inline ml-1 flex-shrink-0" />
        </a>

        {/* Status badge */}
        {isSearching && (
          <span className="text-xs px-1.5 py-0.5 rounded-full bg-purple-500/20 text-purple-300 flex-shrink-0 animate-pulse">
            searching
          </span>
        )}
        {result.status === 'error' && (
          <span className="text-xs px-1.5 py-0.5 rounded-full bg-red-500/20 text-red-300 flex-shrink-0">
            error
          </span>
        )}
      </div>

      {/* Snippet */}
      {isRetrieved && result.snippet && (
        <div className="px-3 pb-2">
          <p className="text-xs text-gray-300 line-clamp-3 whitespace-pre-wrap">
            {result.snippet}
          </p>
        </div>
      )}

      {isSearching && (
        <div className="px-3 pb-2">
          <p className="text-xs text-gray-500 italic">Searching…</p>
        </div>
      )}

      {/* Feedback controls */}
      {isRetrieved && feedbackEnabled && (
        <div className="flex items-center gap-1 px-3 py-1.5 border-t border-gray-700/60">
          <button
            type="button"
            onClick={handleApprove}
            className={`p-1 rounded transition-colors ${
              feedbackKind === 'approve'
                ? 'text-green-400 bg-green-500/20'
                : 'text-gray-500 hover:text-green-400 hover:bg-green-500/10'
            }`}
            title="Relevant"
            aria-label="Mark as relevant"
          >
            <ThumbsUp className="w-3.5 h-3.5" />
          </button>

          <button
            type="button"
            onClick={handleReject}
            className={`p-1 rounded transition-colors ${
              feedbackKind === 'reject'
                ? 'text-red-400 bg-red-500/20'
                : 'text-gray-500 hover:text-red-400 hover:bg-red-500/10'
            }`}
            title="Not relevant"
            aria-label="Mark as not relevant"
          >
            <ThumbsDown className="w-3.5 h-3.5" />
          </button>

          <div className="flex-1" />

          <button
            type="button"
            onClick={handleToggleHighlight}
            className={`p-1 rounded transition-colors ${
              highlighted
                ? 'text-purple-400 bg-purple-500/20'
                : 'text-gray-500 hover:text-purple-400 hover:bg-purple-500/10'
            }`}
            title={highlighted ? 'Remove highlight' : 'Highlight'}
            aria-label="Toggle highlight"
          >
            {highlighted ? (
              <EyeOff className="w-3.5 h-3.5" />
            ) : (
              <Highlighter className="w-3.5 h-3.5" />
            )}
          </button>
        </div>
      )}
    </div>
  );
}

export const SearchResultCard = memo(SearchResultCardComponent);
